// Package tunnel implements the furo-server side of the control protocol:
// the control listener (yamux sessions from clients) and the public HTTP
// listener that routes incoming requests by Host into client data streams.
package tunnel

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/cosmoabdon/usefuro/internal/proto"
	"github.com/cosmoabdon/usefuro/internal/server/names"
)

type Config struct {
	ControlAddr string // e.g. ":7835"
	HTTPAddr    string // e.g. ":8080"
	BaseDomain  string // e.g. "proxy.duto.sh"
	// TLS for each listener; nil serves plaintext (dev). With TLS the
	// public scheme becomes https.
	ControlTLS *tls.Config
	PublicTLS  *tls.Config
	// Authenticate resolves a client token to a username.
	Authenticate func(token string) (string, error)
	// Heartbeat: server pings every PingInterval; a session without a pong
	// for PongTimeout is closed and its tunnels unregistered.
	PingInterval time.Duration // default 30s
	PongTimeout  time.Duration // default 90s
	// AdminHandler serves requests whose Host is the bare base domain
	// (admin SPA + REST API). Nil → 404 for those requests.
	AdminHandler http.Handler
	// OnUserAuth runs (async) whenever a client authenticates. Used to make
	// sure the user's wildcard cert is loaded/issued even when the user was
	// created by the CLI after this server started.
	OnUserAuth func(username string)
	Log        *slog.Logger
}

type Server struct {
	cfg Config
	log *slog.Logger

	mu          sync.Mutex
	tunnels     map[string]*tunnelEntry // key: username + "/" + name
	sessWriters map[*yamux.Session]*ctlWriter

	controlLn net.Listener
	httpLn    net.Listener

	// adminLn feeds admin-host connections into an internal http.Server.
	adminLn *chanListener
	// publicConns tracks in-flight public connections for graceful drain.
	publicConns sync.WaitGroup
}

// chanListener turns handed-off net.Conns into an http.Server feed.
type chanListener struct {
	ch     chan net.Conn
	closed chan struct{}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *chanListener) Close() error   { close(l.closed); return nil }
func (l *chanListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero} }

type tunnelEntry struct {
	id       string
	username string
	name     string
	sess     *yamux.Session
	since    time.Time
}

// Info is the read-only view of an active tunnel (admin API).
type Info struct {
	Username string    `json:"username"`
	Name     string    `json:"name"`
	URL      string    `json:"url"`
	Since    time.Time `json:"since"`
}

func New(cfg Config) *Server {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.PingInterval == 0 {
		cfg.PingInterval = 30 * time.Second
	}
	if cfg.PongTimeout == 0 {
		cfg.PongTimeout = 90 * time.Second
	}
	return &Server{
		cfg: cfg, log: cfg.Log,
		tunnels:     make(map[string]*tunnelEntry),
		sessWriters: make(map[*yamux.Session]*ctlWriter),
	}
}

// Start binds both listeners and serves in background goroutines.
func (s *Server) Start() error {
	var err error
	s.controlLn, err = net.Listen("tcp", s.cfg.ControlAddr)
	if err != nil {
		return fmt.Errorf("control listen: %w", err)
	}
	s.httpLn, err = net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		s.controlLn.Close()
		return fmt.Errorf("http listen: %w", err)
	}
	if s.cfg.ControlTLS != nil {
		s.controlLn = tls.NewListener(s.controlLn, s.cfg.ControlTLS)
	}
	if s.cfg.PublicTLS != nil {
		s.httpLn = tls.NewListener(s.httpLn, s.cfg.PublicTLS)
	}
	s.log.Info("furo-server listening",
		"control", s.controlLn.Addr().String(),
		"http", s.httpLn.Addr().String(),
		"base_domain", s.cfg.BaseDomain)

	if s.cfg.AdminHandler != nil {
		s.adminLn = &chanListener{ch: make(chan net.Conn), closed: make(chan struct{})}
		go http.Serve(s.adminLn, s.cfg.AdminHandler)
	}

	go s.acceptLoop(s.controlLn, s.handleControlConn)
	go s.acceptLoop(s.httpLn, s.handlePublicConn)
	return nil
}

// Close stops accepting, waits up to 10s for in-flight public requests to
// drain, then tears down client sessions.
func (s *Server) Close() error {
	if s.controlLn != nil {
		s.controlLn.Close()
	}
	if s.httpLn != nil {
		s.httpLn.Close()
	}
	done := make(chan struct{})
	go func() { s.publicConns.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		s.log.Warn("drain timeout, closing active connections")
	}
	if s.adminLn != nil {
		s.adminLn.Close()
	}
	s.mu.Lock()
	sessions := map[*yamux.Session]struct{}{}
	for _, t := range s.tunnels {
		sessions[t.sess] = struct{}{}
	}
	s.mu.Unlock()
	for sess := range sessions {
		sess.Close()
	}
	return nil
}

// SetAdminHandler installs the admin SPA/API handler. Call before Start.
func (s *Server) SetAdminHandler(h http.Handler) { s.cfg.AdminHandler = h }

// ControlAddr returns the bound control address (useful with ":0" in tests).
func (s *Server) ControlAddr() string { return s.controlLn.Addr().String() }

// HTTPAddr returns the bound public HTTP address.
func (s *Server) HTTPAddr() string { return s.httpLn.Addr().String() }

func (s *Server) acceptLoop(ln net.Listener, handle func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handle(conn)
	}
}

// ---- control side ----

// ctlWriter serializes control-stream writes (replies vs ping loop).
type ctlWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (w *ctlWriter) send(m proto.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enc.Encode(m)
}

// bufConn lets us peek at the first bytes before handing off to yamux.
type bufConn struct {
	br *bufio.Reader
	net.Conn
}

func (c *bufConn) Read(p []byte) (int, error) { return c.br.Read(p) }

var httpMethods = []string{"GET ", "POST ", "HEAD ", "PUT ", "DELETE", "OPTIONS", "PATCH", "CONNECT", "TRACE"}

func looksLikeHTTP(b []byte) bool {
	for _, m := range httpMethods {
		n := min(len(m), len(b))
		if string(b[:n]) == m[:n] {
			return true
		}
	}
	return false
}

func (s *Server) handleControlConn(conn net.Conn) {
	defer conn.Close()

	// Browsers/probes hitting the control port with HTTP get a clear answer
	// instead of yamux protocol-version noise.
	pk := bufio.NewReader(conn)
	if first, err := pk.Peek(4); err == nil && looksLikeHTTP(first) {
		body := "this is the furo control port (for furo clients) — the HTTP endpoint is on the public port"
		fmt.Fprintf(conn, "HTTP/1.1 400 Bad Request\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		return
	}

	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	sess, err := yamux.Server(&bufConn{br: pk, Conn: conn}, ycfg)
	if err != nil {
		s.log.Error("yamux server", "err", err)
		return
	}
	defer sess.Close()

	ctl, err := sess.AcceptStream()
	if err != nil {
		return
	}
	w := &ctlWriter{enc: json.NewEncoder(ctl)}
	sc := bufio.NewScanner(ctl)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)

	// First message must be auth.
	msg, err := readMsg(sc)
	if err != nil || msg.Type != proto.TypeAuth {
		w.send(proto.Message{Type: proto.TypeAuthErr, Reason: "invalid_token"})
		return
	}
	username, err := s.cfg.Authenticate(msg.Token)
	if err != nil {
		w.send(proto.Message{Type: proto.TypeAuthErr, Reason: "invalid_token"})
		return
	}
	w.send(proto.Message{Type: proto.TypeAuthOK, Username: username})
	s.log.Info("client authenticated", "username", username, "remote", conn.RemoteAddr())
	if s.cfg.OnUserAuth != nil {
		go s.cfg.OnUserAuth(username)
	}

	s.mu.Lock()
	s.sessWriters[sess] = w
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessWriters, sess)
		s.mu.Unlock()
	}()
	defer s.unregisterSession(sess)

	// Heartbeat.
	var pongMu sync.Mutex
	lastPong := time.Now()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(s.cfg.PingInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				pongMu.Lock()
				silent := time.Since(lastPong)
				pongMu.Unlock()
				if silent > s.cfg.PongTimeout {
					s.log.Warn("heartbeat timeout, closing session", "username", username)
					sess.Close()
					return
				}
				w.send(proto.Message{Type: proto.TypePing, TS: time.Now().Unix()})
			}
		}
	}()

	for {
		msg, err := readMsg(sc)
		if err != nil {
			return
		}
		switch msg.Type {
		case proto.TypeRegister:
			s.register(w, sess, username, msg)
		case proto.TypeUnregister:
			s.unregisterByID(msg.TunnelID)
		case proto.TypePong:
			pongMu.Lock()
			lastPong = time.Now()
			pongMu.Unlock()
		}
	}
}

func readMsg(sc *bufio.Scanner) (proto.Message, error) {
	var msg proto.Message
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return msg, err
		}
		return msg, io.EOF
	}
	err := json.Unmarshal(sc.Bytes(), &msg)
	return msg, err
}

func (s *Server) register(w *ctlWriter, sess *yamux.Session, username string, msg proto.Message) {
	name := msg.Name
	generated := name == ""
	if !generated && !names.Valid(name) {
		w.send(proto.Message{Type: proto.TypeRegisterErr, Name: name, Reason: "invalid_name"})
		return
	}

	s.mu.Lock()
	if generated {
		for {
			name = names.Generate()
			if _, taken := s.tunnels[username+"/"+name]; !taken {
				break
			}
		}
	} else if _, taken := s.tunnels[username+"/"+name]; taken {
		s.mu.Unlock()
		w.send(proto.Message{Type: proto.TypeRegisterErr, Name: name, Reason: "name_taken"})
		return
	}
	id := "t_" + randHex(8)
	s.tunnels[username+"/"+name] = &tunnelEntry{id: id, username: username, name: name, sess: sess, since: time.Now()}
	s.mu.Unlock()

	url := fmt.Sprintf("%s://%s.%s.%s", s.scheme(), name, username, s.cfg.BaseDomain)
	w.send(proto.Message{Type: proto.TypeRegistered, TunnelID: id, Name: name, URL: url})
	s.log.Info("tunnel registered", "id", id, "name", name, "username", username)
}

func (s *Server) unregisterSession(sess *yamux.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, t := range s.tunnels {
		if t.sess == sess {
			delete(s.tunnels, key)
			s.log.Info("tunnel unregistered", "id", t.id, "name", t.name)
		}
	}
}

func (s *Server) unregisterByID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, t := range s.tunnels {
		if t.id == id {
			delete(s.tunnels, key)
			s.log.Info("tunnel unregistered", "id", t.id, "name", t.name)
		}
	}
}

// KillTunnel removes an active tunnel and tells its client not to
// re-register it. Returns false when the tunnel is not active.
func (s *Server) KillTunnel(username, name string) bool {
	key := username + "/" + name
	s.mu.Lock()
	t := s.tunnels[key]
	if t == nil {
		s.mu.Unlock()
		return false
	}
	delete(s.tunnels, key)
	w := s.sessWriters[t.sess]
	s.mu.Unlock()

	if w != nil {
		w.send(proto.Message{Type: proto.TypeUnregistered, TunnelID: t.id, Name: name, Reason: "closed_by_admin"})
	}
	s.log.Info("tunnel killed by admin", "id", t.id, "name", name, "username", username)
	return true
}

// ActiveTunnels returns a snapshot of the registry for the admin API.
func (s *Server) ActiveTunnels() []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Info, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		out = append(out, Info{
			Username: t.username,
			Name:     t.name,
			URL:      fmt.Sprintf("%s://%s.%s.%s", s.scheme(), t.name, t.username, s.cfg.BaseDomain),
			Since:    t.since,
		})
	}
	return out
}

// ---- public HTTP side ----

func (s *Server) handlePublicConn(conn net.Conn) {
	s.publicConns.Add(1)
	defer s.publicConns.Done()
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		if s.isAdminHost(req.Host) {
			if !s.serveAdmin(conn, req) {
				return
			}
			continue // connection stays request-routed (Host may change per request)
		}
		t := s.lookup(req.Host)
		if t == nil {
			writeSimpleResponse(conn, 404, "tunnel offline")
			return
		}

		stream, err := t.sess.OpenStream()
		if err != nil {
			writeSimpleResponse(conn, 502, "tunnel unavailable")
			return
		}

		hdr, _ := json.Marshal(proto.DataHeader{TunnelID: t.id, RemoteAddr: conn.RemoteAddr().String()})
		if _, err := stream.Write(append(hdr, '\n')); err != nil {
			stream.Close()
			return
		}

		clientIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		req.Header.Set("X-Forwarded-For", clientIP)
		req.Header.Set("X-Forwarded-Proto", s.scheme())
		req.Header.Set("X-Forwarded-Host", req.Host)

		if isUpgrade(req) {
			s.proxyUpgrade(conn, br, stream, req)
			return
		}

		if err := req.Write(stream); err != nil {
			stream.Close()
			return
		}
		resp, err := http.ReadResponse(bufio.NewReader(stream), req)
		if err != nil {
			stream.Close()
			writeSimpleResponse(conn, 502, "bad gateway")
			return
		}
		err = resp.Write(conn) // streams the body; never buffers it whole
		stream.Close()
		if err != nil || resp.Close || req.Close {
			return
		}
	}
}

// proxyUpgrade forwards the upgrade request then goes fully byte-level in
// both directions so WebSocket/raw duplex traffic flows untouched.
func (s *Server) proxyUpgrade(conn net.Conn, br *bufio.Reader, stream *yamux.Stream, req *http.Request) {
	defer stream.Close()
	if err := req.Write(stream); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(stream, br) // br may hold bytes already buffered past the request
		done <- struct{}{}
	}()
	go func() {
		io.Copy(conn, stream)
		done <- struct{}{}
	}()
	<-done
	// Closing both unblocks the remaining copy.
	conn.Close()
}

// isAdminHost: bare base domain (no tunnel labels) → admin SPA + API.
func (s *Server) isAdminHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host == s.cfg.BaseDomain
}

// serveAdmin proxies ONE request through the internal admin http.Server via
// an in-memory pipe, then returns control to the raw loop — keep-alive
// connections may switch Host between requests, so routing is per request.
// Returns false when the connection must close.
func (s *Server) serveAdmin(conn net.Conn, req *http.Request) bool {
	if s.adminLn == nil {
		writeSimpleResponse(conn, 404, "no admin configured")
		return false
	}
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	select {
	case s.adminLn.ch <- serverSide:
	case <-s.adminLn.closed:
		writeSimpleResponse(conn, 503, "shutting down")
		return false
	}
	go req.Write(clientSide)
	resp, err := http.ReadResponse(bufio.NewReader(clientSide), req)
	if err != nil {
		writeSimpleResponse(conn, 502, "admin unavailable")
		return false
	}
	err = resp.Write(conn)
	return err == nil && !resp.Close && !req.Close
}

func (s *Server) scheme() string {
	if s.cfg.PublicTLS != nil {
		return "https"
	}
	return "http"
}

func isUpgrade(req *http.Request) bool {
	return strings.EqualFold(req.Header.Get("Upgrade"), "websocket") ||
		strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade")
}

// lookup resolves the target tunnel from the request Host:
// name.username.<base_domain>, strict match on the in-memory registry.
func (s *Server) lookup(host string) *tunnelEntry {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	suffix := "." + s.cfg.BaseDomain
	if !strings.HasSuffix(host, suffix) {
		return nil
	}
	labels := strings.Split(strings.TrimSuffix(host, suffix), ".")
	if len(labels) != 2 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tunnels[labels[1]+"/"+labels[0]]
}

func writeSimpleResponse(conn net.Conn, status int, body string) {
	fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
