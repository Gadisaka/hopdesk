// Package proto holds the JSON messages exchanged with the signaling server.
//
// Flow:
//
//	agent  -> server : {type:"register", id, key}
//	server -> agent  : {type:"registered", iceServers}
//	viewer -> server : {type:"offer", id, sdp}
//	server -> agent  : {type:"offer", session, sdp}
//	agent  -> server : {type:"answer", session, sdp}   (or {type:"error", session, message})
//	server -> viewer : {type:"answer", sdp}            (or {type:"error", message})
//
// The password is NOT sent through the server. The viewer sends it over the
// encrypted WebRTC data channel, and the agent checks it there.
package proto

type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type Msg struct {
	Type       string      `json:"type"`
	ID         string      `json:"id,omitempty"`
	Key        string      `json:"key,omitempty"`
	Session    string      `json:"session,omitempty"`
	SDP        string      `json:"sdp,omitempty"`
	Message    string      `json:"message,omitempty"`
	ICEServers []ICEServer `json:"iceServers,omitempty"`
}
