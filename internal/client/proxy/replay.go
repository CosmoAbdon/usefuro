package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

var ErrTruncatedReplay = errors.New("request was truncated in capture; cannot replay")

// Replay resends a captured request straight to the tunnel's local port
// (never through the server) and records the exchange as a new entry
// marked is_replay.
func Replay(ring *Ring, e *Entry) error {
	if e.rawReqTruncated {
		return ErrTruncatedReplay
	}
	if len(e.rawReq) == 0 {
		return errors.New("no captured request bytes")
	}

	conn, err := net.DialTimeout("tcp", e.localAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial local %s: %w", e.localAddr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(60 * time.Second))

	tap := NewTap(ring, e.Tunnel, e.localAddr)
	tap.isReplay = true
	defer tap.Finish()

	tap.req.Write(e.rawReq) // capture the request as sent
	if _, err := conn.Write(e.rawReq); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	// Parse the response off the wire (teed into the capture) and drain the
	// body so framing decides where the exchange ends.
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(e.rawReq)))
	if err != nil {
		return fmt.Errorf("reparse request: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tap.RespReader(conn)), req)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}
