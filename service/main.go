package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"strconv"

	"cloud.google.com/go/storage"
	"github.com/olivere/elastic"
	"github.com/pborman/uuid"

	"cloud.google.com/go/bigtable"
	"google.golang.org/api/option"

	"github.com/gorilla/mux"

	jwtmiddleware "github.com/auth0/go-jwt-middleware"
	jwt "github.com/dgrijalva/jwt-go"

	"path/filepath"
)

const (
	POST_INDEX = "post"
	POST_TYPE  = "post"

	DISTANCE    = "200km"
	ES_URL      = "http://35.236.87.33:9200"
	BUCKET_NAME = "projectaround-post-images"

	ENABLE_BIGTABLE     = false
	BIGTABLE_PROJECT_ID = "project-around-123456"
	BT_INSTANCE         = "around-post"

	API_PREFIX = "/api/v1"
)


type Location struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type Post struct {
	User     string   `json:"user"`
	Message  string   `json:"message"`
	Location Location `json:"location"`
	Url      string   `json:"url"`
	Type     string   `json:"type"`
	Face     float64  `json:"face"`
}

var (
	mediaTypes = map[string]string{
		".jpeg": "image",
		".jpg":  "image",
		".gif":  "image",
		".png":  "image",
		".mov":  "video",
		".mp4":  "video",
		".avi":  "video",
		".flv":  "video",
		".wmv":  "video",
	}
)

func main() {
	fmt.Println("started-service")
	createIndexIfNotExist()

	jwtMiddleware := jwtmiddleware.New(jwtmiddleware.Options{
		ValidationKeyGetter: func(token *jwt.Token) (interface{}, error) {
			return []byte(mySigningKey), nil
		},
		SigningMethod: jwt.SigningMethodHS256,
	})

	r := mux.NewRouter()

	r.Handle(API_PREFIX+"/post", jwtMiddleware.Handler(http.HandlerFunc(handlerPost))).Methods("POST", "OPTIONS")
	r.Handle(API_PREFIX+"/search", jwtMiddleware.Handler(http.HandlerFunc(handlerSearch))).Methods("GET", "OPTIONS")
	r.Handle(API_PREFIX+"/cluster", jwtMiddleware.Handler(http.HandlerFunc(handlerCluster))).Methods("GET", "OPTIONS")
	r.Handle(API_PREFIX+"/signup", http.HandlerFunc(handlerSignup)).Methods("POST", "OPTIONS")
	r.Handle(API_PREFIX+"/login", http.HandlerFunc(handlerLogin)).Methods("POST", "OPTIONS")

	// Backend endpoints.
	http.Handle(API_PREFIX+"/", r)

	// // Frontend endpoints.
	// http.Handle("/", http.FileServer(http.Dir("build")))

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
	// Parse from body of request to get a json object.
	fmt.Println("Received one post request")

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")

	user := r.Context().Value("user")
	claims := user.(*jwt.Token).Claims
	username := claims.(jwt.MapClaims)["username"]

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
		http.Error(w, "Image is not available", http.StatusBadRequest)
		fmt.Printf("Image is not available %v.\n", err)
		return
	}
	attrs, err := saveToGCS(file, BUCKET_NAME, id)
	if err != nil {
		http.Error(w, "Failed to save image to GCS", http.StatusInternalServerError)
		fmt.Printf("Failed to save image to GCS %v.\n", err)
		return
	}

	im, header, _ := r.FormFile("image")
	defer im.Close()
	suffix := filepath.Ext(header.Filename)

	// Client needs to know the media type so as to render it.
	if t, ok := mediaTypes[suffix]; ok {
		p.Type = t
	} else {
		p.Type = "unknown"
	}
	// ML Engine only supports jpeg.
	if suffix == ".jpeg" {
		if score, err := annotate(im); err != nil {
			http.Error(w, "Failed to annotate the image", http.StatusInternalServerError)
			fmt.Printf("Failed to annotate the image %v\n", err)
			return
		} else {
			p.Face = score
		}
	}

	// Update the media link after saving to GCS.
	p.Url = attrs.MediaLink

	err = saveToES(p, id)
	if err != nil {
		http.Error(w, "Failed to save post to ElasticSearch", http.StatusInternalServerError)
		fmt.Printf("Failed to save post to ElasticSearch %v.\n", err)
		return
	}
	fmt.Printf("Saved one post to ElasticSearch: %s.\n", p.Message)

	if ENABLE_BIGTABLE {
		saveToBigTable(p, id)
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

func handlerCluster(w http.ResponseWriter, r *http.Request) {
	// Parse from body of request to get a json object.
	fmt.Println("Received one post request")

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")

	if r.Method == "OPTIONS" {
		return
	}

	if r.Method != "GET" {
		return
	}

	term := r.URL.Query().Get("term")

	// Create a client
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		http.Error(w, "ES is not setup", http.StatusInternalServerError)
		fmt.Printf("ES is not setup %v\n", err)
		return
	}

	// Range query.
	// For details, https://www.elastic.co/guide/en/elasticsearch/reference/current/query-dsl-range-query.html
	q := elastic.NewRangeQuery(term).Gte(0.9)

	searchResult, err := client.Search().
		Index(POST_INDEX).
		Query(q).
		Pretty(true).
		Do(context.Background())

	if err != nil {
		// Handle error
		m := fmt.Sprintf("Failed to query ES %v", err)
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
	for _, item := range searchResult.Each(reflect.TypeOf(typ)) {
		p := item.(Post)
		ps = append(ps, p)
	}
	js, err := json.Marshal(ps)
	if err != nil {
		m := fmt.Sprintf("Failed to parse post object %v", err)
		fmt.Println(m)
		http.Error(w, m, http.StatusInternalServerError)
		return
	}

	w.Write(js)
}

// Read a post from ElasticSearch
func readFromES(lat, lon float64, ran string) ([]Post, error) {
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		return nil, err
	}

	query := elastic.NewGeoDistanceQuery("location")
	query = query.Distance(ran).Lat(lat).Lon(lon)

	searchResult, err := client.Search().
		Index(POST_INDEX).
		Query(query).
		Size(100).
		Pretty(true).
		Do(context.Background())
	if err != nil {
		return nil, err
	}

	// searchResult is of type SearchResult and returns hits, suggestions,
	// and all kinds of other information from Elasticsearch.
	fmt.Printf("Query took %d milliseconds.\n", searchResult.TookInMillis)

	// Each is a convenience function that iterates over hits in a search result.
	// It makes sure you don't need to check for nil values in the response.
	// However, it ignores errors in serialization. If you want full control
	// over iterating the hits, see below.
	var ptyp Post
	var posts []Post
	for _, item := range searchResult.Each(reflect.TypeOf(ptyp)) {
		if p, ok := item.(Post); ok {
			posts = append(posts, p)
		}
	}

	return posts, nil
}
