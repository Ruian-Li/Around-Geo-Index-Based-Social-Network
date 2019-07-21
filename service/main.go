package main

import (
    "context"
	"fmt"
	"io"
	"net/http"
	"encoding/json"
	"log"
	"reflect"
	"strconv"

	"github.com/olivere/elastic"
	"github.com/pborman/uuid"
	"github.com/gorilla/mux"

	"cloud.google.com/go/storage"
	"cloud.google.com/go/bigtable"
)

const (
    POST_INDEX = "post"
    POST_TYPE = "post"
    DISTANCE = "200km"
    ES_URL = "..."

    BUCKET_NAME = "..."
)

type Location struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type Post struct {
	User string `json:"user"`
	Message string `json:"message"`
	Location Location `json:"location"`
	Url string `json:"url"`
}

func main() {
	// Create a client
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		panic(err)
		return
	}

	// Use the IndexExists service to check if a specified index exists.
	exists, err := client.IndexExists(INDEX).Do()
	if err != nil {
		panic(err)
	}
	if !exists {
		// Create a new index.
		mapping := `{
                    "mappings":{
                           "post":{
                                  "properties":{
                                         "location":{
                                                "type":"geo_point"
                                         }
                                  }
                           }
                    }
             }
             `
        // geo_point : ES use geo-indexing for them(kd tree search)
		_, err := client.CreateIndex(INDEX).Body(mapping).Do()
		if err != nil {
			panic(err) // Handle error
		}
	}
	fmt.Println("Started service successfully")
	// Here we are instantiating the gorilla/mux router
	r := mux.NewRouter()

	var jwtMiddleware = jwtmiddleware.New(jwtmiddleware.Options{
		ValidationKeyGetter: func(token *jwt.Token) (interface{}, error) {
			return mySigningKey, nil
		},
		SigningMethod: jwt.SigningMethodHS256,
	})

	var rs_client *redis.Client
	if ENABLE_MEMCACHE {
		rs_client = redis.NewClient(&redis.Options{
			Addr:     REDIS_URL,
			Password: REDIS_PASSWORD,
			DB:       0, // use default DB
		})
	}

	r.Handle("/post", jwtMiddleware.Handler(http.HandlerFunc(handlerPost)))
	r.Handle("/search", jwtMiddleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){
		handlerSearch(w, r, rs_client)
	})))
	r.Handle("/login", http.HandlerFunc(loginHandler))
	r.Handle("/signup", http.HandlerFunc(signupHandler))

	http.Handle("/", r)
	log.Fatal(http.ListenAndServe(":8080", nil))

}

// Save an image to GCS.
func saveToGCS(ctx context.Context, r io.Reader, bucket, name string) (*storage.ObjectHandle, *storage.ObjectAttrs, error) {
	// Creates a client.
	client, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
		return nil, nil, err
	}
	defer client.Close()

	// Sets the name for the new bucket.
	bh  := client.Bucket(bucket)
	// Next check if the bucket exists, 看能否获取bucket.attribute 来看bucket是否存在
	if _, err = bh.Attrs(ctx); err != nil {
		return nil, nil, err
	}

	obj:= bh.Object(name)
	w:= obj.NewWriter(ctx)		//a writer
	if _, err = io.Copy(w, r); err!=nil{
		return nil, nil, err
	}

	if err := w.Close(); err!=nil{
		return nil, nil, err
	}
	//ACL : 管理文件访问权限 -- access control list
	if err:= obj.ACL().Set(ctx, storage.AllUsers, storage.RoleReader); err!=nil {
		return nil, nil, err
	}

	attrs, err := obj.Attrs(ctx)
	fmt.Printf("Post is saved to GCS:%s\n", attrs.MediaLink)
	return obj, attrs, err
}

// Save a post to ElasticSearch
func saveToES(p *Post, id string) {
	// Create a client
	es_client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		panic(err)
		return
	}

	// Save it to index
	_, err = es_client.Index().
		Index(INDEX).
		Type(TYPE).
		Id(id).
		BodyJson(p).
		Refresh(true).
		Do()
	if err != nil {
		panic(err)
		return
	}

	fmt.Printf("Post is saved to Index: %s\n", p.Message)
}

// Save a post to BigTable
func saveToBigTable(p *Post, id string) {
	ctx := context.Background()
	bt_client, err := bigtable.NewClient(ctx, BIGTABLE_PROJECT_ID, BT_INSTANCE)
	if err != nil {
		panic(err)
		return
	}

	tbl := bt_client.Open("post")
	mut := bigtable.NewMutation()
	t := bigtable.Now()
	mut.Set("post", "user", t, []byte(p.User))
	mut.Set("post", "message", t, []byte(p.Message))
	mut.Set("location", "lat", t, []byte(strconv.FormatFloat(p.Location.Lat, 'f', -1, 64)))
	mut.Set("location", "lon", t, []byte(strconv.FormatFloat(p.Location.Lon, 'f', -1, 64)))

	err = tbl.Apply(ctx, id, mut)
	if err != nil {
		panic(err)
		return
	}
	fmt.Printf("Post is saved to BigTable: %s\n", p.Message)
}

func createIndexIfNotExist() {
    client, err := elastic.NewClient(elastic.SetURL()
}

func handlerPost(w http.ResponseWriter, r *http.Request) {
	//handle cors
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")

	if r.Method != "POST" {
		return
	}

	user := r.Context().Value("user")  //  key  "user": jwt will add it to context when you login
	if user == nil {
		m := fmt.Sprintf("Unable to find user in context")
		fmt.Println(m)
		http.Error(w, m, http.StatusBadRequest)
		return
	}
	claims := user.(*jwt.Token).Claims
	username := claims.(jwt.MapClaims)["username"]

	// 32 << 20 is the maxMemory param for ParseMultipartForm, equals to 32MB (1MB = 1024 * 1024 bytes = 2^20 bytes)
	// After you call ParseMultipartForm, the file will be saved in the server memory with maxMemory size.
	// If the file size is larger than maxMemory, the rest of the data will be saved in a system temporary file.
	r.ParseMultipartForm(32 << 20)

	// Parse from form data.
	fmt.Printf("Received one post request %s\n", r.FormValue("message"))

	lat, _ := strconv.ParseFloat(r.FormValue("lat"), 64)
	lon, _ := strconv.ParseFloat(r.FormValue("lon"), 64)
	p := &Post{
		User:    username.(string),
		Message: r.FormValue("message"),
		Location: Location{
			Lat: lat,
			Lon: lon,
		},
	}

	id := uuid.New()

	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "Image is not available", http.StatusInternalServerError)
		fmt.Printf("Image is not available %v.\n", err)
		return
	}

	ctx := context.Background()

	defer file.Close()

	// replace it with your real bucket name.
	_, attrs, err := saveToGCS(ctx, file, GCS_BUCKET, id)
	if err != nil {
		http.Error(w, "GCS is not setup", http.StatusInternalServerError)
		fmt.Printf("GCS is not setup %v\n", err)
		return
	}

	// Update the media link after saving to GCS.
	p.Url = attrs.MediaLink

	// Save to ES.
	go saveToES(p, id)

	// Save to BigTable.
	if ENABLE_BIGTABLE {
		go saveToBigTable(p, id)
	}

}

func handlerSearch(w http.ResponseWriter, r *http.Request, rs_client *redis.Client) {
	fmt.Println("Received one request for search")
	// handle cors
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")

	if r.Method != "GET" {
		return
	}

	lat, _ := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, _ := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	// range is optional
	ran := DISTANCE
	if val := r.URL.Query().Get("range"); val != "" {
		ran = val + "km"
	}

	fmt.Printf( "Search received: %f %f %s\n", lat, lon, ran)

	// add Redis cache
	key := r.URL.Query().Get("lat") + ":" + r.URL.Query().Get("lon") + ":" + ran
	if ENABLE_MEMCACHE {
		val, err := rs_client.Get(key).Result()
		if err != nil {
			fmt.Printf("Redis cannot find the key %s as %v.\n", key, err)
		} else {
			fmt.Printf("Redis find the key %s.\n", key)
			w.Write([]byte(val))
			return
		}
	}

	// Create a client
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		http.Error(w, "ES is not setup", http.StatusInternalServerError)
		fmt.Printf("ES is not setup %v\n", err)
		return
	}

	// Define geo distance query as specified in
	// https://www.elastic.co/guide/en/elasticsearch/reference/5.2/query-dsl-geo-distance-query.html
	q := elastic.NewGeoDistanceQuery("location")
	q = q.Distance(ran).Lat(lat).Lon(lon)

	// Some delay may range from seconds to minutes. So if you don't get enough results. Try it later.
	searchResult, err := client.Search().
		Index(INDEX).
		Query(q).
		Pretty(true).
		Do()
	if err != nil {
		// Handle error
		m := fmt.Sprintf("Failed to search ES %v", err)
		fmt.Println(m)
		http.Error(w, m, http.StatusInternalServerError)
	}

	// searchResult is of type SearchResult and returns hits, suggestions,
	// and all kinds of other information from Elasticsearch.
	fmt.Printf("Query took %d milliseconds\n", searchResult.TookInMillis)
	// TotalHits is another convenience function that works even when something goes wrong.
	fmt.Printf("Found a total of %d post\n", searchResult.TotalHits())

	// Each is a convenience function that iterates over hits in a search result.
	// It makes sure you don't need to check for nil values in the response.
	// However, it ignores errors in serialization.
	var typ Post
	var ps []Post
	for _, item := range searchResult.Each(reflect.TypeOf(typ)) { // instance of
		p := item.(Post) // p = (Post) item
		fmt.Printf("Post by %s: %s at lat %v and lon %v\n", p.User, p.Message, p.Location.Lat, p.Location.Lon)
		// TODO(): Perform filtering based on keywords such as web spam etc.
		if !containsFilteredWords(&p.Message) {
			ps = append(ps, p)
		}
	}
	js, err := json.Marshal(ps)
	if err != nil {
		m := fmt.Sprintf("Failed to parse post object %v", err)
		fmt.Println(m)
		http.Error(w, m, http.StatusInternalServerError)
		return
	}

	if ENABLE_MEMCACHE {
		// Set the cache expiration to be 30 seconds
		err := rs_client.Set(key, string(js), time.Second*30).Err()
		if err != nil {
			fmt.Printf("Redis cannot save the key %s as %v.\n", key, err)
		}
	}

	w.Write(js)
}