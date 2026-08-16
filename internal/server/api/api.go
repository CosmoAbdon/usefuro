// Package api serves the admin REST API and the embedded admin SPA on the
// bare base domain. Auth: Authorization: Bearer <admin_token>.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cosmoabdon/usefuro/internal/server/store"
	"github.com/cosmoabdon/usefuro/internal/server/tunnel"
	webadmin "github.com/cosmoabdon/usefuro/web/admin"
)

// TunnelSource is the live-registry view the API needs.
type TunnelSource interface {
	ActiveTunnels() []tunnel.Info
	KillTunnel(username, name string) bool
}

// CertIssuer is called after a user is created (acme wildcard); may be nil.
type CertIssuer func(username string) error

type Server struct {
	store      *store.Store
	tunnels    TunnelSource
	adminToken string
	issueCert  CertIssuer
	log        *slog.Logger
}

func New(st *store.Store, tunnels TunnelSource, adminToken string, issueCert CertIssuer, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{store: st, tunnels: tunnels, adminToken: adminToken, issueCert: issueCert, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/users", s.auth(s.listUsers))
	mux.HandleFunc("POST /api/users", s.auth(s.createUser))
	mux.HandleFunc("DELETE /api/users/{username}", s.auth(s.deleteUser))
	mux.HandleFunc("GET /api/users/{username}/tokens", s.auth(s.listTokens))
	mux.HandleFunc("POST /api/users/{username}/tokens", s.auth(s.createToken))
	mux.HandleFunc("POST /api/tokens/revoke", s.auth(s.revokeToken))
	mux.HandleFunc("GET /api/tunnels", s.auth(s.listTunnels))
	mux.HandleFunc("DELETE /api/tunnels/{username}/{name}", s.auth(s.killTunnel))
	mux.Handle("/", http.FileServerFS(webadmin.Dist()))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken == "" {
			http.Error(w, "admin_token not configured on the server", http.StatusServiceUnavailable)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.adminToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrAmbiguous):
		status = http.StatusBadRequest
	case strings.Contains(err.Error(), "UNIQUE"):
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// ---- users ----

type userView struct {
	Username      string `json:"username"`
	CreatedAt     string `json:"created_at"`
	TokenCount    int    `json:"token_count"`
	TunnelsOnline int    `json:"tunnels_online"`
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.Users()
	if err != nil {
		writeErr(w, err)
		return
	}
	online := map[string]int{}
	for _, t := range s.tunnels.ActiveTunnels() {
		online[t.Username]++
	}
	out := make([]userView, 0, len(users))
	for _, u := range users {
		out = append(out, userView{
			Username: u.Username, CreatedAt: u.CreatedAt,
			TokenCount: u.TokenCount, TunnelsOnline: online[u.Username],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if err := s.store.CreateUser(body.Username); err != nil {
		writeErr(w, err)
		return
	}
	token, err := s.store.CreateToken(body.Username, "default")
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.issueCert != nil {
		go func(u string) {
			if err := s.issueCert(u); err != nil {
				s.log.Error("cert issuance (user)", "username", u, "err", err)
			}
		}(body.Username)
	}
	writeJSON(w, http.StatusCreated, map[string]string{"username": body.Username, "token": token})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteUser(r.PathValue("username")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---- tokens ----

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.Tokens(r.PathValue("username"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if tokens == nil {
		tokens = []store.TokenInfo{}
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label string `json:"label"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	token, err := s.store.CreateToken(r.PathValue("username"), body.Label)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": token})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if err := s.store.RevokeToken(body.Prefix); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// ---- tunnels ----

type tunnelView struct {
	Username      string `json:"username"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

func (s *Server) killTunnel(w http.ResponseWriter, r *http.Request) {
	username, name := r.PathValue("username"), r.PathValue("name")
	if !s.tunnels.KillTunnel(username, name) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tunnel not active"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "killed"})
}

func (s *Server) listTunnels(w http.ResponseWriter, r *http.Request) {
	active := s.tunnels.ActiveTunnels()
	out := make([]tunnelView, 0, len(active))
	for _, t := range active {
		out = append(out, tunnelView{
			Username: t.Username, Name: t.Name, URL: t.URL,
			UptimeSeconds: int64(time.Since(t.Since).Seconds()),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
