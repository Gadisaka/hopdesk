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
// The password is never sent anywhere. After WebRTC connects, the agent
// proves its identity and the viewer proves it knows the password, both tied
// to that exact connection (see internal/auth).
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
	From       string      `json:"from,omitempty"` // viewer's IP, added by the server for per-source lockouts
	ICEServers []ICEServer `json:"iceServers,omitempty"`
}
