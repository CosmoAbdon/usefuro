package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRingCapacityAndIDs(t *testing.T) {
	r := NewRing()
	for i := 0; i < RingSize+50; i++ {
		r.Add(&Entry{Method: "GET"})
	}
	list := r.List()
	if len(list) != RingSize {
		t.Fatalf("len = %d, want %d", len(list), RingSize)
	}
	if list[0].ID != 51 || list[len(list)-1].ID != RingSize+50 {
		t.Fatalf("ids %d..%d, want 51..%d", list[0].ID, list[len(list)-1].ID, RingSize+50)
	}
	if r.Get(50) != nil {
		t.Fatal("evicted entry still reachable")
	}
	r.Clear()
	if len(r.List()) != 0 {
		t.Fatal("clear did not empty the ring")
	}
}

func TestTapParsesExchange(t *testing.T) {
	ring := NewRing()
	tap := NewTap(ring, "web", "127.0.0.1:1")

	reqRaw := "POST /api/items?x=1 HTTP/1.1\r\nHost: web.alice.local\r\nContent-Type: application/json\r\nContent-Length: 13\r\n\r\n{\"a\":\"hello\"}"
	respRaw := "HTTP/1.1 201 Created\r\nContent-Type: application/json\r\nContent-Length: 11\r\n\r\n{\"id\":\"42\"}"
	io.Copy(io.Discard, tap.ReqReader(strings.NewReader(reqRaw)))
	io.Copy(io.Discard, tap.RespReader(strings.NewReader(respRaw)))
	tap.Finish()

	list := ring.List()
	if len(list) != 1 {
		t.Fatalf("entries = %d, want 1", len(list))
	}
	e := ring.Get(list[0].ID)
	if e.Method != "POST" || e.Path != "/api/items?x=1" || e.Status != 201 {
		t.Fatalf("parsed %s %s %d", e.Method, e.Path, e.Status)
	}
	if string(e.ReqBody) != `{"a":"hello"}` || string(e.RespBody) != `{"id":"42"}` {
		t.Fatalf("bodies: %q / %q", e.ReqBody, e.RespBody)
	}
	if e.ReqTruncated || e.RespTruncated {
		t.Fatal("small bodies flagged truncated")
	}
	if e.ReqSize != int64(len(reqRaw)) || e.RespSize != int64(len(respRaw)) {
		t.Fatalf("sizes %d/%d, want %d/%d", e.ReqSize, e.RespSize, len(reqRaw), len(respRaw))
	}
}

func TestTapTruncatesBigBodies(t *testing.T) {
	ring := NewRing()
	tap := NewTap(ring, "web", "127.0.0.1:1")

	big := bytes.Repeat([]byte("x"), 3*MaxBody)
	respRaw := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(big))
	io.Copy(io.Discard, tap.ReqReader(strings.NewReader("GET /big HTTP/1.1\r\nHost: a\r\n\r\n")))
	io.Copy(io.Discard, tap.RespReader(io.MultiReader(strings.NewReader(respRaw), bytes.NewReader(big))))
	tap.Finish()

	e := ring.Get(ring.List()[0].ID)
	if !e.RespTruncated {
		t.Fatal("3MB body not flagged truncated")
	}
	if len(e.RespBody) > MaxBody {
		t.Fatalf("stored body %d bytes, cap is %d", len(e.RespBody), MaxBody)
	}
	if e.RespSize != int64(len(respRaw)+len(big)) {
		t.Fatalf("wire size %d, want %d", e.RespSize, len(respRaw)+len(big))
	}
}

func TestReplay(t *testing.T) {
	hits := 0
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "echo:%s", body)
	}))

	ring := NewRing()
	// Seed an entry as if captured live.
	tap := NewTap(ring, "web", ln.Addr().String())
	reqRaw := "POST /x HTTP/1.1\r\nHost: a\r\nContent-Length: 2\r\nConnection: close\r\n\r\nhi"
	io.Copy(io.Discard, tap.ReqReader(strings.NewReader(reqRaw)))
	io.Copy(io.Discard, tap.RespReader(strings.NewReader("HTTP/1.1 200 OK\r\nContent-Length: 7\r\n\r\necho:hi")))
	tap.Finish()

	orig := ring.Get(ring.List()[0].ID)
	if err := Replay(ring, orig); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for hits == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits != 1 {
		t.Fatalf("local service hits = %d, want 1", hits)
	}

	list := ring.List()
	if len(list) != 2 {
		t.Fatalf("entries = %d, want 2", len(list))
	}
	replayed := ring.Get(list[1].ID)
	if !replayed.IsReplay || replayed.Status != 200 || string(replayed.RespBody) != "echo:hi" {
		t.Fatalf("replay entry: replay=%v status=%d body=%q", replayed.IsReplay, replayed.Status, replayed.RespBody)
	}

	// Truncated originals must refuse to replay.
	orig.rawReqTruncated = true
	if err := Replay(ring, orig); err != ErrTruncatedReplay {
		t.Fatalf("truncated replay: %v, want ErrTruncatedReplay", err)
	}
}
