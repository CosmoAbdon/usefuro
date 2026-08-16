package e2e

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPrometheusMetrics(t *testing.T) {
	dataDir := t.TempDir()
	token := userAdd(t, dataDir, "kate")
	controlPort, httpPort, metricsPort := freePort(t), freePort(t), freePort(t)
	startServer(t, dataDir, controlPort, httpPort, "--metrics-port", fmt.Sprint(metricsPort))
	waitPort(t, httpPort)

	svc := localService(t, "kate-svc")
	startClient(t, localPort(svc), fmt.Sprintf("127.0.0.1:%d", controlPort), token,
		"--name", "app", "--plaintext")

	base := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	waitHTTP(t, base+"/", "app.kate.localhost", "hello from kate-svc")
	// One guaranteed error-path hit.
	if status, _, err := get(base+"/", "ghost.kate.localhost"); err != nil || status != 404 {
		t.Fatalf("offline hit: %d %v", status, err)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", metricsPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	metrics := string(body)

	// Gauges are asserted with exact values; counters only for presence —
	// waitHTTP's warm-up polls hit the offline path a variable number of
	// times before the tunnel registers.
	for _, want := range []string{
		`furo_sessions_active 1`,
		`furo_tunnels_active{username="kate"} 1`,
		`furo_auth_total{outcome="ok"}`,
		`furo_registrations_total{outcome="ok"}`,
		`furo_http_requests_total{status_class="2xx",username="kate"}`,
		`furo_http_errors_total{reason="tunnel_offline"}`,
		`furo_http_request_duration_seconds_bucket`,
		`furo_public_bytes_total{direction="in"}`,
		`furo_public_bytes_total{direction="out"}`,
		`furo_user_bytes_total{direction="in",username="kate"}`,
		`furo_user_bytes_total{direction="out",username="kate"}`,
		`go_goroutines`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestMetricsDisabledByDefault(t *testing.T) {
	dataDir := t.TempDir()
	userAdd(t, dataDir, "leo")
	controlPort, httpPort := freePort(t), freePort(t)
	startServer(t, dataDir, controlPort, httpPort)
	waitPort(t, httpPort)

	// No metrics listener anywhere near the defaults.
	client := http.Client{Timeout: 500 * time.Millisecond}
	if resp, err := client.Get("http://127.0.0.1:9091/metrics"); err == nil {
		resp.Body.Close()
		// Something answered — only fail if it's actually furo metrics.
		b, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(b), "furo_") {
			t.Fatal("metrics served without being enabled")
		}
	}
}
