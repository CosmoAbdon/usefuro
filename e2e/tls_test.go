package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitFile blocks until path exists (the server writes certs/ca.pem at boot).
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("file never appeared: %s", path)
}

// httpsClient trusts caPath and dials 127.0.0.1:port whatever the URL host —
// SNI/Host keep the tunnel hostname while the bytes go to the local server.
func httpsClient(t *testing.T, caPath string, port int) *http.Client {
	t.Helper()
	pem, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("bad CA file")
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			},
		},
	}
}

func TestTLSSelfSignedEndToEnd(t *testing.T) {
	dataDir := t.TempDir()
	token := userAdd(t, dataDir, "eve")
	controlPort, httpPort := freePort(t), freePort(t)

	startServer(t, dataDir, controlPort, httpPort, "--tls", "self-signed")
	caPath := filepath.Join(dataDir, "certs", "ca.pem")
	waitFile(t, caPath)

	svc := localService(t, "eve-svc")
	startClient(t, localPort(svc), fmt.Sprintf("127.0.0.1:%d", controlPort), token,
		"--name", "web", "--ca", caPath)

	client := httpsClient(t, caPath, httpPort)
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp, err := client.Get("https://web.eve.localhost/")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && strings.Contains(string(body), "hello from eve-svc") {
				if !strings.Contains(string(body), "fwd-proto=https") {
					t.Fatalf("expected X-Forwarded-Proto https, body: %s", body)
				}
				return
			}
			last = fmt.Sprintf("status=%d body=%q", resp.StatusCode, body)
		} else {
			last = err.Error()
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("TLS tunnel never came up: %s", last)
}

func TestLoginStoresConfig(t *testing.T) {
	dataDir := t.TempDir()
	token := userAdd(t, dataDir, "frank")
	controlPort, httpPort := freePort(t), freePort(t)
	startServer(t, dataDir, controlPort, httpPort)
	time.Sleep(200 * time.Millisecond)

	home := t.TempDir()
	login := exec.Command(clientBin, "login", token,
		"--server", fmt.Sprintf("127.0.0.1:%d", controlPort), "--plaintext")
	login.Env = append(os.Environ(), "HOME="+home)
	if out, err := login.CombinedOutput(); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}

	svc := localService(t, "frank-svc")
	// No --server/--token/--plaintext: everything must come from the config.
	cmd := exec.Command(clientBin, "http", localPort(svc), "--name", "app")
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/", httpPort), "app.frank.localhost", "hello from frank-svc")
}
