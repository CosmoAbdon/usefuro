// Package proxy captures request/response traffic flowing through the client
// tunnel into an in-memory ring buffer, feeding the local inspector.
package proxy

import (
	"net/http"
	"sync"
	"time"
)

const (
	// RingSize is how many requests the inspector keeps.
	RingSize = 500
	// MaxBody caps captured bodies; larger bodies are truncated with a flag.
	MaxBody = 1 << 20 // 1 MB
)

// Entry is one captured exchange.
type Entry struct {
	ID       int64         `json:"id"`
	Tunnel   string        `json:"tunnel"`
	Method   string        `json:"method"`
	Path     string        `json:"path"`
	Status   int           `json:"status"` // 0 when the response never parsed
	Start    time.Time     `json:"start"`
	Duration time.Duration `json:"duration"`
	ReqSize  int64         `json:"req_size"`  // wire bytes, uncapped
	RespSize int64         `json:"resp_size"` // wire bytes, uncapped
	IsReplay bool          `json:"is_replay"`

	ReqHeaders    http.Header `json:"req_headers"`
	ReqBody       []byte      `json:"req_body"`
	ReqTruncated  bool        `json:"req_truncated"`
	RespHeaders   http.Header `json:"resp_headers"`
	RespBody      []byte      `json:"resp_body"`
	RespTruncated bool        `json:"resp_truncated"`

	// rawReq is the request exactly as it came off the wire (capped), used
	// for replay. Not exposed over the API.
	rawReq          []byte
	rawReqTruncated bool
	localAddr       string
}

// Summary is the list/SSE view of an Entry.
type Summary struct {
	ID       int64         `json:"id"`
	Tunnel   string        `json:"tunnel"`
	Method   string        `json:"method"`
	Path     string        `json:"path"`
	Status   int           `json:"status"`
	Start    time.Time     `json:"start"`
	Duration time.Duration `json:"duration"`
	ReqSize  int64         `json:"req_size"`
	RespSize int64         `json:"resp_size"`
	IsReplay bool          `json:"is_replay"`
}

func (e *Entry) Summary() Summary {
	return Summary{
		ID: e.ID, Tunnel: e.Tunnel, Method: e.Method, Path: e.Path,
		Status: e.Status, Start: e.Start, Duration: e.Duration,
		ReqSize: e.ReqSize, RespSize: e.RespSize, IsReplay: e.IsReplay,
	}
}

// Event is what SSE subscribers receive.
type Event struct {
	Type  string   `json:"type"` // "entry" | "clear"
	Entry *Summary `json:"entry,omitempty"`
}

// Ring is a fixed-capacity buffer of recent entries with subscribers.
type Ring struct {
	mu      sync.Mutex
	entries []*Entry
	nextID  int64
	subs    map[chan Event]struct{}
}

func NewRing() *Ring {
	return &Ring{subs: make(map[chan Event]struct{})}
}

func (r *Ring) Add(e *Entry) {
	r.mu.Lock()
	r.nextID++
	e.ID = r.nextID
	r.entries = append(r.entries, e)
	if len(r.entries) > RingSize {
		r.entries = r.entries[len(r.entries)-RingSize:]
	}
	s := e.Summary()
	for ch := range r.subs {
		select {
		case ch <- Event{Type: "entry", Entry: &s}:
		default: // slow subscriber: drop rather than block the tunnel
		}
	}
	r.mu.Unlock()
}

func (r *Ring) List() []Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Summary, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.Summary()
	}
	return out
}

func (r *Ring) Get(id int64) *Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.ID == id {
			return e
		}
	}
	return nil
}

func (r *Ring) Clear() {
	r.mu.Lock()
	r.entries = nil
	for ch := range r.subs {
		select {
		case ch <- Event{Type: "clear"}:
		default:
		}
	}
	r.mu.Unlock()
}

// Subscribe returns a channel of events plus an unsubscribe func.
func (r *Ring) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}
