// Package e2e builds the real furo-server and furo binaries, wires them to
// local test services and proves the full path: external request → public
// listener → yamux data stream → local service → response. Covers multiuser
// routing, WebSocket-style upgrades, reconnection with re-register and
// server-generated tunnel names.
package e2e

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
		serverBin: "../../cmd/furo-server",
		clientBin: "../../cmd/furo",
	} {
		out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, err, out)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// ---- helpers ----

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type proc struct {
	cmd    *exec.Cmd
	stdout *lockedBuffer
	exited chan struct{} // closed when the process is reaped
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startProc(t *testing.T, bin string, args ...string) *proc {
	t.Helper()
	cmd := exec.Command(bin, args...)
	// Isolated HOME so ~/.config/furo of the developer never leaks in.
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out := &lockedBuffer{}
	cmd.Stdout = io.MultiWriter(out, os.Stderr)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	p := &proc{cmd: cmd, stdout: out, exited: make(chan struct{})}
	go func() { cmd.Wait(); close(p.exited) }()
	t.Cleanup(func() { p.kill() })
	return p
}

func (p *proc) kill() {
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
		<-p.exited
	}
}

// userAdd runs `furo-server user add` and returns the printed token.
func userAdd(t *testing.T, dataDir, username string) string {
	t.Helper()
	out, err := exec.Command(serverBin, "user", "add", username, "--data-dir", dataDir).CombinedOutput()
	if err != nil {
		t.Fatalf("user add: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`token: (furo_\S+)`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("no token in output:\n%s", out)
	}
	return string(m[1])
}

func startServer(t *testing.T, dataDir string, controlPort, httpPort int, extra ...string) *proc {
	t.Helper()
	args := append([]string{"serve",
		"--control", fmt.Sprintf("127.0.0.1:%d", controlPort),
		"--http", fmt.Sprintf("127.0.0.1:%d", httpPort),
		"--domain", "localhost",
		"--data-dir", dataDir}, extra...)
	return startProc(t, serverBin, args...)
}

func startClient(t *testing.T, port, controlAddr, token string, extra ...string) *proc {
	t.Helper()
	args := append([]string{"http", port, "--server", controlAddr, "--token", token}, extra...)
	return startProc(t, clientBin, args...)
}

// waitPort blocks until something accepts TCP on 127.0.0.1:port.
func waitPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("port %d never opened", port)
}

func get(url, host string) (int, string, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), nil
}

func waitHTTP(t *testing.T, url, host, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		status, body, err := get(url, host)
		if err == nil && status == 200 && strings.Contains(body, want) {
			return
		}
		last = fmt.Sprintf("status=%d body=%q err=%v", status, body, err)
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("tunnel never came up (%s @ %s): %s", host, url, last)
}

// localService: "/" identifies itself; "/ws" hijacks, answers 101, echoes.
func localService(t *testing.T, id string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s (fwd-for=%s fwd-host=%s fwd-proto=%s)",
			id, r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Forwarded-Proto"))
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

func localPort(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
}

// ---- tests ----

func TestMultiUserRouting(t *testing.T) {
	dataDir := t.TempDir()
	aliceToken := userAdd(t, dataDir, "alice")
	bobToken := userAdd(t, dataDir, "bob")

	controlPort, httpPort := freePort(t), freePort(t)
	startServer(t, dataDir, controlPort, httpPort)
	time.Sleep(200 * time.Millisecond)

	aliceSvc := localService(t, "alice-svc")
	bobSvc := localService(t, "bob-svc")
	controlAddr := fmt.Sprintf("127.0.0.1:%d", controlPort)
	startClient(t, localPort(aliceSvc), controlAddr, aliceToken, "--name", "web", "--plaintext")
	startClient(t, localPort(bobSvc), controlAddr, bobToken, "--name", "web", "--plaintext")

	publicURL := fmt.Sprintf("http://127.0.0.1:%d/", httpPort)
	waitHTTP(t, publicURL, "web.alice.localhost", "hello from alice-svc")
	waitHTTP(t, publicURL, "web.bob.localhost", "hello from bob-svc")

	t.Run("same_name_different_users_route_apart", func(t *testing.T) {
		_, body, err := get(publicURL, "web.alice.localhost")
		if err != nil || !strings.Contains(body, "alice-svc") {
			t.Fatalf("alice routing: %q %v", body, err)
		}
		_, body, err = get(publicURL, "web.bob.localhost")
		if err != nil || !strings.Contains(body, "bob-svc") {
			t.Fatalf("bob routing: %q %v", body, err)
		}
	})

	t.Run("unknown_tunnel_404", func(t *testing.T) {
		status, body, err := get(publicURL, "nope.alice.localhost")
		if err != nil {
			t.Fatal(err)
		}
		if status != 404 || !strings.Contains(body, "tunnel offline") {
			t.Fatalf("got %d %q, want 404 tunnel offline", status, body)
		}
	})

	t.Run("unknown_user_404", func(t *testing.T) {
		status, _, err := get(publicURL, "web.ghost.localhost")
		if err != nil {
			t.Fatal(err)
		}
		if status != 404 {
			t.Fatalf("got %d, want 404", status)
		}
	})

	t.Run("websocket_upgrade_duplex", func(t *testing.T) {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", httpPort))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))

		fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: web.alice.localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		br := bufio.NewReader(conn)
		status, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(status, "101") {
			t.Fatalf("expected 101, got %q", status)
		}
		for { // drain response headers
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if line == "\r\n" {
				break
			}
		}
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

	t.Run("revoked_token_rejected", func(t *testing.T) {
		out, err := exec.Command(serverBin, "token", "ls", "alice", "--data-dir", dataDir).CombinedOutput()
		if err != nil {
			t.Fatalf("token ls: %v\n%s", err, out)
		}
		prefix := strings.Fields(string(out))[0]
		if out, err := exec.Command(serverBin, "token", "revoke", prefix, "--data-dir", dataDir).CombinedOutput(); err != nil {
			t.Fatalf("token revoke: %v\n%s", err, out)
		}
		// New session with the revoked token must fail fast (auth_err → exit 1).
		cmd := exec.Command(clientBin, "http", localPort(aliceSvc), "--server", controlAddr, "--token", aliceToken, "--plaintext")
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
		out, err = cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("client with revoked token succeeded:\n%s", out)
		}
		if !strings.Contains(string(out), "auth failed") {
			t.Fatalf("expected auth failure, got:\n%s", out)
		}
	})
}

func TestReconnectReregisters(t *testing.T) {
	dataDir := t.TempDir()
	token := userAdd(t, dataDir, "carol")
	controlPort, httpPort := freePort(t), freePort(t)

	srv := startServer(t, dataDir, controlPort, httpPort)
	time.Sleep(200 * time.Millisecond)

	svc := localService(t, "carol-svc")
	startClient(t, localPort(svc), fmt.Sprintf("127.0.0.1:%d", controlPort), token, "--name", "api", "--plaintext")

	publicURL := fmt.Sprintf("http://127.0.0.1:%d/", httpPort)
	waitHTTP(t, publicURL, "api.carol.localhost", "hello from carol-svc")

	// Kill the server; client must reconnect and re-register on its own.
	srv.kill()
	startServer(t, dataDir, controlPort, httpPort)
	waitHTTP(t, publicURL, "api.carol.localhost", "hello from carol-svc")
}

func TestServerGeneratedName(t *testing.T) {
	dataDir := t.TempDir()
	token := userAdd(t, dataDir, "dave")
	controlPort, httpPort := freePort(t), freePort(t)
	startServer(t, dataDir, controlPort, httpPort)
	time.Sleep(200 * time.Millisecond)

	svc := localService(t, "dave-svc")
	client := startClient(t, localPort(svc), fmt.Sprintf("127.0.0.1:%d", controlPort), token, "--plaintext")

	// Client prints "Forwarding http://<name>.dave.localhost -> ..." — parse it.
	re := regexp.MustCompile(`Forwarding http://([a-z][a-z0-9]{7})\.dave\.localhost`)
	var name string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := re.FindStringSubmatch(client.stdout.String()); m != nil {
			name = m[1]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if name == "" {
		t.Fatalf("no generated-name URL printed, stdout:\n%s", client.stdout.String())
	}

	publicURL := fmt.Sprintf("http://127.0.0.1:%d/", httpPort)
	waitHTTP(t, publicURL, name+".dave.localhost", "hello from dave-svc")
}
