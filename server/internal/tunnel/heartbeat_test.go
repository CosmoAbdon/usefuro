package tunnel

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/cosmoabdon/furo/proto"
)

// A client that authenticates and registers but never answers pings must be
// dropped after PongTimeout and its tunnels unregistered.
func TestHeartbeatDropsSilentSession(t *testing.T) {
	s := New(Config{
		ControlAddr:  "127.0.0.1:0",
		HTTPAddr:     "127.0.0.1:0",
		BaseDomain:   "localhost",
		Authenticate: func(string) (string, error) { return "alice", nil },
		PingInterval: 30 * time.Millisecond,
		PongTimeout:  100 * time.Millisecond,
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	conn, err := net.Dial("tcp", s.ControlAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sess, err := yamux.Client(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := sess.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(ctl)
	sc := bufio.NewScanner(ctl)

	enc.Encode(proto.Message{Type: proto.TypeAuth, Token: "x"})
	if msg, err := readMsg(sc); err != nil || msg.Type != proto.TypeAuthOK {
		t.Fatalf("auth: %+v, %v", msg, err)
	}
	enc.Encode(proto.Message{Type: proto.TypeRegister, Name: "web"})
	if msg, err := readMsg(sc); err != nil || msg.Type != proto.TypeRegistered {
		t.Fatalf("register: %+v, %v", msg, err)
	}
	if s.lookup("web.alice.localhost") == nil {
		t.Fatal("tunnel not registered")
	}

	// Ignore pings entirely; the server must close the session.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.lookup("web.alice.localhost") == nil {
			return // unregistered — heartbeat worked
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("silent session never dropped")
}
