package tunnel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/cosmoabdon/usefuro/internal/proto"
)

// fuzzServer builds a full server: one registered tunnel whose client echoes
// a fixed 200 to every data stream, plus an admin handler — so fuzz inputs
// can reach the proxy path, the admin bridge and the error paths.
func fuzzServer(f *testing.F) *Server {
	f.Helper()
	s := New(Config{
		ControlAddr:  "127.0.0.1:0",
		HTTPAddr:     "127.0.0.1:0",
		BaseDomain:   "localhost",
		Authenticate: func(string) (string, error) { return "fuzz", nil },
		AdminHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "admin ok")
		}),
	})
	if err := s.Start(); err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { s.Close() })

	conn, err := net.Dial("tcp", s.ControlAddr())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { conn.Close() })
	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	sess, err := yamux.Client(conn, ycfg)
	if err != nil {
		f.Fatal(err)
	}
	ctl, err := sess.OpenStream()
	if err != nil {
		f.Fatal(err)
	}
	enc := json.NewEncoder(ctl)
	sc := bufio.NewScanner(ctl)
	enc.Encode(proto.Message{Type: proto.TypeAuth, Token: "x"})
	if msg, err := readMsg(sc); err != nil || msg.Type != proto.TypeAuthOK {
		f.Fatalf("auth: %+v %v", msg, err)
	}
	enc.Encode(proto.Message{Type: proto.TypeRegister, Name: "app"})
	if msg, err := readMsg(sc); err != nil || msg.Type != proto.TypeRegistered {
		f.Fatalf("register: %+v %v", msg, err)
	}
	go func() { // drain pings
		for {
			if _, err := readMsg(sc); err != nil {
				return
			}
		}
	}()
	go func() { // echo a valid response on every data stream
		for {
			stream, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func(st *yamux.Stream) {
				defer st.Close()
				br := bufio.NewReader(st)
				if _, err := br.ReadBytes('\n'); err != nil { // header line
					return
				}
				// Consume whatever request bytes arrive, then answer.
				st.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
				io.Copy(io.Discard, br)
				fmt.Fprint(st, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}(stream)
		}
	}()
	return s
}

// FuzzPublicConn throws arbitrary bytes at the public listener — the raw
// HTTP/1.1 parser loop, Host routing, admin bridge and upgrade path. The
// server must never panic or hang; any well-formed or garbage input must end
// with the connection closed.
func FuzzPublicConn(f *testing.F) {
	s := fuzzServer(f)
	addr := s.HTTPAddr()

	f.Add([]byte("GET / HTTP/1.1\r\nHost: app.fuzz.localhost\r\n\r\n"))
	f.Add([]byte("POST /x HTTP/1.1\r\nHost: app.fuzz.localhost\r\nContent-Length: 3\r\n\r\nabc"))
	f.Add([]byte("GET /ws HTTP/1.1\r\nHost: app.fuzz.localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nframe"))
	f.Add([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"))                                        // admin
	f.Add([]byte("GET / HTTP/1.1\r\nHost: nope.ghost.localhost\r\n\r\n"))                             // 404
	f.Add([]byte("GET / HTTP/1.1\r\nHost: app.fuzz.localhost\r\nContent-Length: 99999\r\n\r\nshort")) // lying length
	f.Add([]byte("garbage\r\n\r\n"))
	f.Add(bytes.Repeat([]byte("A"), 10000))
	f.Add([]byte("GET / HTTP/1.1\r\nHost: app.fuzz.localhost\r\n" + string(bytes.Repeat([]byte("X-Big: y\r\n"), 500)) + "\r\n"))
	f.Add([]byte{0x00, 0xff, 0x13, 0x37})

	f.Fuzz(func(t *testing.T, data []byte) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Skip() // listener saturated momentarily
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		conn.Write(data)
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		// Drain whatever the server answers; deadline guards against hangs.
		io.Copy(io.Discard, conn)
	})
}
