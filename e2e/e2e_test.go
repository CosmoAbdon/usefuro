// Package e2e builds the real furo-server and furo binaries, wires them to a
// local test service and proves the full path: external request → public
// listener → yamux data stream → local service → response. Includes a raw
// WebSocket-style upgrade test (byte-level duplex after 101).
package e2e

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	serverBin string
	clientBin string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "furo-e2e")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	serverBin = filepath.Join(dir, "furo-server")
	clientBin = filepath.Join(dir, "furo")
	for bin, pkg := range map[string]string{
		serverBin: "../server/cmd/furo-server",
		clientBin: "../client/cmd/furo",
	} {
		out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, err, out)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// freePort grabs an ephemeral port and releases it for the process under test.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func startProc(t *testing.T, bin string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
}

func waitHTTP(t *testing.T, url, host, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", url, nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && strings.Contains(string(body), want) {
				return
			}
			lastErr = fmt.Errorf("status %d body %q", resp.StatusCode, body)
		} else {
			lastErr = err
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("tunnel never came up: %v", lastErr)
}

// localService: "/" answers hello; "/ws" hijacks, answers 101 and echoes bytes.
func localService(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from local (fwd-for=%s fwd-host=%s)",
			r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Forwarded-Host"))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected upgrade", 400)
			return
		}
		conn, bufrw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		bufrw.Flush()
		for {
			line, err := bufrw.ReadString('\n')
			if err != nil {
				return
			}
			bufrw.WriteString("echo:" + line)
			bufrw.Flush()
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestEndToEnd(t *testing.T) {
	local := localService(t)
	localPort := strings.TrimPrefix(local.URL, "http://127.0.0.1:")

	controlPort := freePort(t)
	httpPort := freePort(t)

	startProc(t, serverBin,
		"--control", fmt.Sprintf("127.0.0.1:%d", controlPort),
		"--http", fmt.Sprintf("127.0.0.1:%d", httpPort),
		"--domain", "localhost")
	time.Sleep(200 * time.Millisecond)

	startProc(t, clientBin, "http", localPort,
		"--name", "test",
		"--server", fmt.Sprintf("127.0.0.1:%d", controlPort))

	publicURL := fmt.Sprintf("http://127.0.0.1:%d/", httpPort)
	waitHTTP(t, publicURL, "test.dev.localhost", "hello from local")

	t.Run("http_proxy_headers", func(t *testing.T) {
		req, _ := http.NewRequest("GET", publicURL, nil)
		req.Host = "test.dev.localhost"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if !strings.Contains(string(body), "fwd-for=127.0.0.1") {
			t.Errorf("missing X-Forwarded-For, body: %s", body)
		}
		if !strings.Contains(string(body), "fwd-host=test.dev.localhost") {
			t.Errorf("missing X-Forwarded-Host, body: %s", body)
		}
	})

	t.Run("websocket_upgrade_duplex", func(t *testing.T) {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", httpPort))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))

		fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: test.dev.localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		br := bufio.NewReader(conn)
		status, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(status, "101") {
			t.Fatalf("expected 101, got %q", status)
		}
		// drain response headers
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if line == "\r\n" {
				break
			}
		}
		// duplex echo, multiple round trips over the same upgraded conn
		for i := 0; i < 3; i++ {
			msg := fmt.Sprintf("ping-%d\n", i)
			if _, err := conn.Write([]byte(msg)); err != nil {
				t.Fatal(err)
			}
			reply, err := br.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if reply != "echo:"+msg {
				t.Fatalf("got %q want %q", reply, "echo:"+msg)
			}
		}
	})

	t.Run("unknown_host_offline_404", func(t *testing.T) {
		// Second registered tunnel would defeat the single-tunnel fallback;
		// with one tunnel up, any Host matches (M1 behavior). So test the
		// explicit-label miss path only when labels parse but don't match —
		// covered properly in M2 with multiuser routing. Here: bad request
		// to a stopped server port must 404 once no tunnels... skip until M2.
		t.Skip("strict Host-miss 404 depends on M2 multiuser routing")
	})
}
