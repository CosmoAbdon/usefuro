// Package inspector serves the local dashboard (default localhost:4040):
// captured requests list + detail, SSE live feed, replay and clear.
package inspector

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/cosmoabdon/usefuro/client/internal/proxy"
	"github.com/cosmoabdon/usefuro/client/internal/tunnel"
	webinspector "github.com/cosmoabdon/usefuro/web-inspector"
)

// PortAttempts limits the auto-increment search (4040, 4041, ...). Exported
// so `furo status` scans the same range.
const PortAttempts = 20

// StatusFunc reports the live tunnels of this process (furo status).
type StatusFunc func() []tunnel.TunnelStatus

type Server struct {
	ring   *proxy.Ring
	status StatusFunc
	log    *slog.Logger
	ln     net.Listener
}

func New(ring *proxy.Ring, status StatusFunc, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{ring: ring, status: status, log: log}
}

// Start listens on the first free port from basePort up and serves in the
// background. Returns the URL to print (http://localhost:<port>).
func (s *Server) Start(basePort int) (string, error) {
	var err error
	for port := basePort; port < basePort+PortAttempts; port++ {
		s.ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			go http.Serve(s.ln, s.handler())
			return fmt.Sprintf("http://localhost:%d", port), nil
		}
	}
	return "", fmt.Errorf("no free inspector port in %d-%d: %w", basePort, basePort+PortAttempts-1, err)
}

func (s *Server) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/requests", s.handleList)
	mux.HandleFunc("GET /api/requests/{id}", s.handleDetail)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/replay/{id}", s.handleReplay)
	mux.HandleFunc("POST /api/clear", s.handleClear)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.Handle("/", http.FileServerFS(webinspector.Dist()))
	return mux
}

// handleStatus identifies this furo process for `furo status`.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	tunnels := []tunnel.TunnelStatus{}
	if s.status != nil {
		tunnels = s.status()
	}
	writeJSON(w, map[string]any{"app": "furo", "tunnels": tunnels})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	list := s.ring.List()
	if list == nil {
		list = []proxy.Summary{}
	}
	writeJSON(w, list)
}

// detail is the API shape of a full Entry; bodies go out base64 (binary-safe).
type detail struct {
	proxy.Summary
	ReqHeaders    http.Header `json:"req_headers"`
	ReqBody       []byte      `json:"req_body"` // base64 via encoding/json
	ReqTruncated  bool        `json:"req_truncated"`
	RespHeaders   http.Header `json:"resp_headers"`
	RespBody      []byte      `json:"resp_body"`
	RespTruncated bool        `json:"resp_truncated"`
}

func (s *Server) entryFromPath(w http.ResponseWriter, r *http.Request) *proxy.Entry {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return nil
	}
	e := s.ring.Get(id)
	if e == nil {
		http.Error(w, "not found (evicted or cleared)", http.StatusNotFound)
		return nil
	}
	return e
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	e := s.entryFromPath(w, r)
	if e == nil {
		return
	}
	writeJSON(w, detail{
		Summary:       e.Summary(),
		ReqHeaders:    e.ReqHeaders,
		ReqBody:       e.ReqBody,
		ReqTruncated:  e.ReqTruncated,
		RespHeaders:   e.RespHeaders,
		RespBody:      e.RespBody,
		RespTruncated: e.RespTruncated,
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := s.ring.Subscribe()
	defer cancel()
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	e := s.entryFromPath(w, r)
	if e == nil {
		return
	}
	if err := proxy.Replay(s.ring, e); err != nil {
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "truncated") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, map[string]string{"status": "replayed"})
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	s.ring.Clear()
	writeJSON(w, map[string]string{"status": "cleared"})
}
