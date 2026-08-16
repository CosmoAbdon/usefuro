// Package tunnel implements the furo client side: one persistent yamux
// session to the server carrying N logical tunnels, automatic reconnection
// with backoff + re-register, and per-request data-stream handling (server
// opens a stream per HTTP request; we pipe it byte-level to the local port).
package tunnel

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/cosmoabdon/usefuro/client/internal/proxy"
	"github.com/cosmoabdon/usefuro/proto"
)

const (
	clientVersion = "0.1.0"
	maxBackoff    = 30 * time.Second
)

// TunnelSpec is one tunnel the client wants up.
type TunnelSpec struct {
	Name      string // public name; empty → server generates one
	LocalAddr string // e.g. "127.0.0.1:3003"
}

// TunnelStatus is the live view of one tunnel (furo status / inspector API).
type TunnelStatus struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	LocalAddr string    `json:"local"`
	Since     time.Time `json:"since"`
}

type Config struct {
	ServerAddr string // control address, e.g. "control.proxy.duto.sh:7835"
	Token      string
	Tunnels    []TunnelSpec
	Plaintext  bool        // no TLS on the control connection (dev servers)
	CAFile     string      // extra CA bundle (self-signed servers)
	Insecure   bool        // skip TLS verification
	Ring       *proxy.Ring // capture buffer for the inspector; nil disables capture
	Log        *slog.Logger
}

// tunnelState carries a spec plus what the server assigned to it.
type tunnelState struct {
	spec    TunnelSpec
	name    string // assigned name (server echo), stable across reconnects
	id      string // tunnel_id of the current session
	url     string
	since   time.Time
	removed bool // killed by the server (admin); never re-register
}

type Client struct {
	cfg    Config
	log    *slog.Logger
	tlsCfg *tls.Config
	states []*tunnelState

	mu        sync.Mutex
	byID      map[string]*tunnelState
	connected bool
}

// errPermanent aborts the reconnect loop (bad token, name taken on first try).
var errPermanent = errors.New("permanent")

func New(cfg Config) (*Client, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if len(cfg.Tunnels) == 0 {
		return nil, errors.New("no tunnels configured")
	}
	c := &Client{cfg: cfg, log: cfg.Log, byID: map[string]*tunnelState{}}
	for _, spec := range cfg.Tunnels {
		c.states = append(c.states, &tunnelState{spec: spec, name: spec.Name})
	}
	if !cfg.Plaintext {
		c.tlsCfg = &tls.Config{InsecureSkipVerify: cfg.Insecure, MinVersion: tls.VersionTLS12}
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("read ca file: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("no certificates in %s", cfg.CAFile)
			}
			c.tlsCfg.RootCAs = pool
		}
	}
	return c, nil
}

// Status returns the live tunnels of this process (empty when disconnected).
func (c *Client) Status() []TunnelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return []TunnelStatus{}
	}
	out := make([]TunnelStatus, 0, len(c.states))
	for _, st := range c.states {
		if st.removed {
			continue
		}
		out = append(out, TunnelStatus{
			Name: st.name, URL: st.url, LocalAddr: st.spec.LocalAddr, Since: st.since,
		})
	}
	return out
}

// remaining counts tunnels not killed by the server. Callers hold c.mu.
func (c *Client) remainingLocked() int {
	n := 0
	for _, st := range c.states {
		if !st.removed {
			n++
		}
	}
	return n
}

func (c *Client) dial() (net.Conn, error) {
	if c.tlsCfg == nil {
		return net.Dial("tcp", c.cfg.ServerAddr)
	}
	return tls.Dial("tcp", c.cfg.ServerAddr, c.tlsCfg)
}

// Run connects and serves; on session loss it reconnects with exponential
// backoff (1s → 30s, jitter), re-authenticates and re-registers every tunnel
// under the same names. Returns only on permanent errors.
func (c *Client) Run() error {
	backoff := time.Second
	first := true
	for {
		start := time.Now()
		err := c.connectOnce(first)
		if errors.Is(err, errPermanent) {
			return err
		}
		if first && err != nil && time.Since(start) < time.Second {
			// Never got a session up — likely bad address; still retry, but say so.
			c.log.Warn("initial connection failed", "err", err)
		}
		first = false
		if time.Since(start) > time.Minute {
			backoff = time.Second // session was healthy; start backoff over
		}
		sleep := backoff + time.Duration(rand.Int63n(int64(backoff/2+1)))
		c.log.Info("reconnecting", "in", sleep, "err", err)
		time.Sleep(sleep)
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Client) connectOnce(first bool) error {
	conn, err := c.dial()
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}
	defer conn.Close()

	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	sess, err := yamux.Client(conn, ycfg)
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
		return fmt.Errorf("%w: auth failed: %s", errPermanent, msg.Reason)
	}

	c.mu.Lock()
	c.byID = map[string]*tunnelState{}
	c.mu.Unlock()

	c.mu.Lock()
	if c.remainingLocked() == 0 {
		c.mu.Unlock()
		return fmt.Errorf("%w: all tunnels were closed by the server admin", errPermanent)
	}
	c.mu.Unlock()

	for _, st := range c.states {
		if st.removed {
			continue
		}
		enc.Encode(proto.Message{Type: proto.TypeRegister, Proto: "http", Name: st.name, Local: st.spec.LocalAddr})
		msg, err = readMsg(sc)
		if err != nil {
			return fmt.Errorf("read register reply: %w", err)
		}
		if msg.Type != proto.TypeRegistered {
			err := fmt.Errorf("register %q failed: %s", st.name, msg.Reason)
			if first {
				return fmt.Errorf("%w: %v", errPermanent, err)
			}
			// On reconnect the old session may not be cleaned up yet — retry.
			return err
		}
		c.mu.Lock()
		st.name = msg.Name // stable across reconnects, even when server-generated
		st.id = msg.TunnelID
		st.url = msg.URL
		st.since = time.Now()
		c.byID[st.id] = st
		c.mu.Unlock()
		fmt.Printf("Forwarding %s -> %s\n", msg.URL, st.spec.LocalAddr)
	}

	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
	}()

	// Control reader: answer pings, honor admin kicks.
	go func() {
		for {
			m, err := readMsg(sc)
			if err != nil {
				return
			}
			switch m.Type {
			case proto.TypePing:
				enc.Encode(proto.Message{Type: proto.TypePong, TS: m.TS})
			case proto.TypeUnregistered:
				c.mu.Lock()
				if st := c.byID[m.TunnelID]; st != nil {
					st.removed = true
					delete(c.byID, m.TunnelID)
				}
				left := c.remainingLocked()
				c.mu.Unlock()
				c.log.Warn("tunnel closed by server", "name", m.Name, "reason", m.Reason)
				if left == 0 {
					sess.Close() // unblocks AcceptStream; Run exits permanently
				}
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

// handleStream reads the one-line JSON header, routes by tunnel_id, then
// pipes bytes between the yamux stream and the local port. Never buffers
// whole bodies.
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
	c.mu.Lock()
	st := c.byID[hdr.TunnelID]
	c.mu.Unlock()
	if st == nil {
		c.log.Error("stream for unknown tunnel", "tunnel_id", hdr.TunnelID)
		return
	}

	local, err := net.Dial("tcp", st.spec.LocalAddr)
	if err != nil {
		c.log.Error("local dial failed", "addr", st.spec.LocalAddr, "err", err)
		fmt.Fprintf(stream, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 21\r\nConnection: close\r\n\r\nlocal service is down")
		return
	}
	defer local.Close()

	var reqR io.Reader = br
	var respR io.Reader = local
	var tap *proxy.Tap
	if c.cfg.Ring != nil {
		tap = proxy.NewTap(c.cfg.Ring, st.name, st.spec.LocalAddr)
		reqR = tap.ReqReader(br)
		respR = tap.RespReader(local)
	}

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(local, reqR)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(stream, respR)
		done <- struct{}{}
	}()
	<-done
	stream.Close()
	local.Close() // closing both unblocks the remaining copy
	<-done
	if tap != nil {
		tap.Finish()
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
