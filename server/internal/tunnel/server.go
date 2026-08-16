// Package tunnel implements the furo-server side of the control protocol:
// the control listener (yamux sessions from clients) and the public HTTP
// listener that routes incoming requests by Host into client data streams.
package tunnel

import (
	"bufio"
	"crypto/rand"
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

	"github.com/cosmoabdon/furo/proto"
	"github.com/cosmoabdon/furo/server/internal/names"
)

type Config struct {
	ControlAddr string // e.g. ":7835"
	HTTPAddr    string // e.g. ":8080"
	BaseDomain  string // e.g. "proxy.duto.sh"
	// Authenticate resolves a client token to a username.
	Authenticate func(token string) (string, error)
	// Heartbeat: server pings every PingInterval; a session without a pong
	// for PongTimeout is closed and its tunnels unregistered.
	PingInterval time.Duration // default 30s
	PongTimeout  time.Duration // default 90s
	Log          *slog.Logger
}

type Server struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	tunnels map[string]*tunnelEntry // key: username + "/" + name

	controlLn net.Listener
	httpLn    net.Listener
}

type tunnelEntry struct {
	id       string
	username string
	name     string
	sess     *yamux.Session
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
	return &Server{cfg: cfg, log: cfg.Log, tunnels: make(map[string]*tunnelEntry)}
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
	s.log.Info("furo-server listening",
		"control", s.controlLn.Addr().String(),
		"http", s.httpLn.Addr().String(),
		"base_domain", s.cfg.BaseDomain)

	go s.acceptLoop(s.controlLn, s.handleControlConn)
	go s.acceptLoop(s.httpLn, s.handlePublicConn)
	return nil
}

func (s *Server) Close() error {
	if s.controlLn != nil {
		s.controlLn.Close()
	}
	if s.httpLn != nil {
		s.httpLn.Close()
	}
	return nil
}

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

func (s *Server) handleControlConn(conn net.Conn) {
	defer conn.Close()
	sess, err := yamux.Server(conn, nil)
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
	s.tunnels[username+"/"+name] = &tunnelEntry{id: id, username: username, name: name, sess: sess}
	s.mu.Unlock()

	url := fmt.Sprintf("http://%s.%s.%s", name, username, s.cfg.BaseDomain)
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

// ---- public HTTP side ----

func (s *Server) handlePublicConn(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
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
		req.Header.Set("X-Forwarded-Proto", "http") // becomes https in M3
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
