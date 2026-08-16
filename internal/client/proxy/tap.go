package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"time"
)

// cappedBuffer stores up to cap bytes but counts everything written.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
	total int64
}

func newCappedBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit} }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.total += int64(len(p))
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return len(p), nil // never fail the wire path
}

func (b *cappedBuffer) Truncated() bool { return b.total > int64(b.buf.Len()) }
func (b *cappedBuffer) Bytes() []byte   { return b.buf.Bytes() }

// Tap tees one tunneled exchange into capture buffers. Wire bytes are never
// altered or blocked; parsing happens after the exchange finishes.
type Tap struct {
	ring      *Ring
	tunnel    string
	localAddr string
	isReplay  bool
	start     time.Time
	req       *cappedBuffer
	resp      *cappedBuffer
}

// headRoom on top of MaxBody so a 1 MB body still keeps its headers.
const headRoom = 64 << 10

func NewTap(ring *Ring, tunnel, localAddr string) *Tap {
	return &Tap{
		ring: ring, tunnel: tunnel, localAddr: localAddr,
		start: time.Now(),
		req:   newCappedBuffer(MaxBody + headRoom),
		resp:  newCappedBuffer(MaxBody + headRoom),
	}
}

// ReqReader tees the client→local direction (the raw HTTP request).
func (t *Tap) ReqReader(r io.Reader) io.Reader { return io.TeeReader(r, t.req) }

// RespReader tees the local→client direction (the raw HTTP response).
func (t *Tap) RespReader(r io.Reader) io.Reader { return io.TeeReader(r, t.resp) }

// Finish parses the captured bytes and pushes the entry into the ring.
// Call after both directions are done.
func (t *Tap) Finish() {
	if t.ring == nil || (t.req.total == 0 && t.resp.total == 0) {
		return
	}
	e := &Entry{
		Tunnel:          t.tunnel,
		Start:           t.start,
		Duration:        time.Since(t.start),
		ReqSize:         t.req.total,
		RespSize:        t.resp.total,
		IsReplay:        t.isReplay,
		rawReq:          append([]byte(nil), t.req.Bytes()...),
		rawReqTruncated: t.req.Truncated(),
		localAddr:       t.localAddr,
	}

	if req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(t.req.Bytes()))); err == nil {
		e.Method = req.Method
		e.Path = req.URL.RequestURI()
		e.Host = req.Host
		e.ReqHeaders = req.Header
		e.ReqBody, e.ReqTruncated = readCappedBody(req.Body, t.req.Truncated())
		if resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(t.resp.Bytes())), req); err == nil {
			e.Status = resp.StatusCode
			e.RespHeaders = resp.Header
			e.RespBody, e.RespTruncated = readCappedBody(resp.Body, t.resp.Truncated())
			resp.Body.Close()
		}
	}
	t.ring.Add(e)
}

// readCappedBody reads a (possibly chunk-decoded) body from a capture that
// may be cut off mid-stream; read errors just end the body.
func readCappedBody(r io.Reader, rawTruncated bool) ([]byte, bool) {
	body := make([]byte, 0, 4096)
	buf := make([]byte, 32*1024)
	truncated := rawTruncated
	for len(body) <= MaxBody {
		n, err := r.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	if len(body) > MaxBody {
		body = body[:MaxBody]
		truncated = true
	}
	return body, truncated
}
