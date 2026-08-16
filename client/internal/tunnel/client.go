// Package tunnel implements the furo client side: the persistent yamux
// session to the server and per-request data-stream handling (server opens a
// stream per HTTP request; we pipe it byte-level to the local port).
package tunnel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/hashicorp/yamux"

	"github.com/cosmoabdon/furo/proto"
)

const clientVersion = "0.1.0-m1"

type Config struct {
	ServerAddr string // control address, e.g. "127.0.0.1:7835"
	Token      string
	Name       string // tunnel name
	LocalAddr  string // e.g. "127.0.0.1:3003"
	Log        *slog.Logger
}

type Client struct {
	cfg Config
	log *slog.Logger
}

func New(cfg Config) *Client {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Client{cfg: cfg, log: cfg.Log}
}

// Run connects, authenticates, registers the tunnel and serves data streams
// until the session ends. Reconnection with backoff lands in M2.
func (c *Client) Run() error {
	conn, err := net.Dial("tcp", c.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}
	defer conn.Close()

	sess, err := yamux.Client(conn, nil)
	if err != nil {
		return fmt.Errorf("yamux: %w", err)
	}
	defer sess.Close()

	ctl, err := sess.OpenStream()
	if err != nil {
		return fmt.Errorf("open control stream: %w", err)
	}
	enc := json.NewEncoder(ctl)
	sc := bufio.NewScanner(ctl)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)

	enc.Encode(proto.Message{Type: proto.TypeAuth, Token: c.cfg.Token, ClientVersion: clientVersion})
	msg, err := readMsg(sc)
	if err != nil {
		return fmt.Errorf("read auth reply: %w", err)
	}
	if msg.Type != proto.TypeAuthOK {
		return fmt.Errorf("auth failed: %s", msg.Reason)
	}
	c.log.Info("authenticated", "username", msg.Username)

	enc.Encode(proto.Message{Type: proto.TypeRegister, Proto: "http", Name: c.cfg.Name, Local: c.cfg.LocalAddr})
	msg, err = readMsg(sc)
	if err != nil {
		return fmt.Errorf("read register reply: %w", err)
	}
	if msg.Type != proto.TypeRegistered {
		return fmt.Errorf("register failed: %s", msg.Reason)
	}
	fmt.Printf("Forwarding  %s  →  %s\n", msg.URL, c.cfg.LocalAddr)

	// Keep draining the control stream (ping handling lands in M2).
	go func() {
		for {
			m, err := readMsg(sc)
			if err != nil {
				return
			}
			if m.Type == proto.TypePing {
				enc.Encode(proto.Message{Type: proto.TypePong, TS: m.TS})
			}
		}
	}()

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return fmt.Errorf("session closed: %w", err)
		}
		go c.handleStream(stream)
	}
}

// handleStream reads the one-line JSON header then pipes bytes between the
// yamux stream and the local port. Never buffers whole bodies.
func (c *Client) handleStream(stream *yamux.Stream) {
	defer stream.Close()
	br := bufio.NewReader(stream)

	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	var hdr proto.DataHeader
	if err := json.Unmarshal(line, &hdr); err != nil {
		c.log.Error("bad data header", "err", err)
		return
	}

	local, err := net.Dial("tcp", c.cfg.LocalAddr)
	if err != nil {
		c.log.Error("local dial failed", "addr", c.cfg.LocalAddr, "err", err)
		fmt.Fprintf(stream, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 21\r\nConnection: close\r\n\r\nlocal service is down")
		return
	}
	defer local.Close()

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(local, br)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(stream, local)
		done <- struct{}{}
	}()
	<-done
	// Closing both (via defers) unblocks the remaining copy.
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
