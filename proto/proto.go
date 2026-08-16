// Package proto defines the NDJSON control-protocol messages exchanged on
// yamux stream 0 and the per-request data-stream header.
package proto

// Message is the envelope for every control-stream message, both directions.
// Type discriminates; unused fields stay empty.
type Message struct {
	Type string `json:"type"`

	// auth (client → server)
	Token         string `json:"token,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`

	// register (client → server)
	Proto string `json:"proto,omitempty"`
	Name  string `json:"name,omitempty"`
	Local string `json:"local,omitempty"`

	// server → client
	Username string `json:"username,omitempty"`
	Reason   string `json:"reason,omitempty"`
	TunnelID string `json:"tunnel_id,omitempty"`
	URL      string `json:"url,omitempty"`

	// ping/pong
	TS int64 `json:"ts,omitempty"`
}

// Message types.
const (
	TypeAuth        = "auth"
	TypeAuthOK      = "auth_ok"
	TypeAuthErr     = "auth_err"
	TypeRegister    = "register"
	TypeRegistered  = "registered"
	TypeRegisterErr = "register_err"
	TypeUnregister  = "unregister"
	TypePing        = "ping"
	TypePong        = "pong"
)

// DataHeader is the single JSON line the server writes at the start of every
// data stream, before the raw HTTP/1.1 request bytes.
type DataHeader struct {
	TunnelID   string `json:"tunnel_id"`
	RemoteAddr string `json:"remote_addr"`
}
