import React from 'react';
import { Marker, InfoWindow } from 'react-google-maps';

export class AroundMarker extends React.Component {
 state = {
   isOpen: false,
 }

 toggleOpen = () => {
   this.setState(prevState => ({ isOpen: !prevState.isOpen }));
 }

 render() {
   const { user, message, url, location } = this.props.post;
   const { lat, lon: lng } = location;
   return (
     <Marker
       position={{ lat, lng }}
       onMouseOver={this.toggleOpen}
       onMouseOut={this.toggleOpen}
     >
       {this.state.isOpen ? (
         <InfoWindow onCloseClick={this.toggleOpen}>
           <div>
             <img src={url} alt={message} className="around-marker-image"/>
             <p>{`${user}: ${message}`}</p>
           </div>
         </InfoWindow>
       ) : null}
     </Marker>
   );
 }
}
