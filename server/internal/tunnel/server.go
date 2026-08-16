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

	"github.com/hashicorp/yamux"

	"github.com/cosmoabdon/furo/proto"
)

// Config for M1: single hardcoded user/token, plain TCP (no TLS).
type Config struct {
	ControlAddr string // e.g. ":7835"
	HTTPAddr    string // e.g. ":8080"
	BaseDomain  string // e.g. "localhost"
	AuthToken   string // M1: single shared token
	Username    string // M1: single hardcoded username
	Log         *slog.Logger
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
	enc := json.NewEncoder(ctl)
	sc := bufio.NewScanner(ctl)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)

	// First message must be auth.
	msg, err := readMsg(sc)
	if err != nil || msg.Type != proto.TypeAuth || msg.Token != s.cfg.AuthToken {
		enc.Encode(proto.Message{Type: proto.TypeAuthErr, Reason: "invalid_token"})
		return
	}
	username := s.cfg.Username
	enc.Encode(proto.Message{Type: proto.TypeAuthOK, Username: username})
	s.log.Info("client authenticated", "username", username, "remote", conn.RemoteAddr())

	defer s.unregisterSession(sess)

	for {
		msg, err := readMsg(sc)
		if err != nil {
			return
		}
		switch msg.Type {
		case proto.TypeRegister:
			s.register(enc, sess, username, msg)
		case proto.TypeUnregister:
			s.unregisterByID(msg.TunnelID)
		case proto.TypePong:
			// heartbeat tracking lands in M2
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

func (s *Server) register(enc *json.Encoder, sess *yamux.Session, username string, msg proto.Message) {
	key := username + "/" + msg.Name
	s.mu.Lock()
	if _, taken := s.tunnels[key]; taken {
		s.mu.Unlock()
		enc.Encode(proto.Message{Type: proto.TypeRegisterErr, Name: msg.Name, Reason: "name_taken"})
		return
	}
	id := "t_" + randHex(8)
	s.tunnels[key] = &tunnelEntry{id: id, username: username, name: msg.Name, sess: sess}
	s.mu.Unlock()

	url := fmt.Sprintf("http://%s.%s.%s", msg.Name, username, s.cfg.BaseDomain)
	enc.Encode(proto.Message{Type: proto.TypeRegistered, TunnelID: id, URL: url})
	s.log.Info("tunnel registered", "id", id, "name", msg.Name, "username", username)
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
// name.username.<base_domain>. M1 fallback: a single active tunnel matches
// any Host, so local testing works without DNS.
func (s *Server) lookup(host string) *tunnelEntry {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	suffix := "." + s.cfg.BaseDomain
	if strings.HasSuffix(host, suffix) {
		labels := strings.Split(strings.TrimSuffix(host, suffix), ".")
		if len(labels) == 2 {
			if t, ok := s.tunnels[labels[1]+"/"+labels[0]]; ok {
				return t
			}
		}
	}
	if len(s.tunnels) == 1 {
		for _, t := range s.tunnels {
			return t
		}
	}
	return nil
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
