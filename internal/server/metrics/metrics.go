// Package metrics defines the server's Prometheus collectors and the
// optional /metrics endpoint. Collectors are always live (they cost nearly
// nothing); the metrics_port config only controls whether they are exposed.
//
// Cardinality rule: labels may carry usernames (bounded by the user table)
// but never tunnel names (random, unbounded).
package metrics

import (
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Control plane.
	SessionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "furo_sessions_active",
		Help: "Client control sessions currently connected.",
	})
	AuthTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "furo_auth_total",
		Help: "Authentication attempts on the control port.",
	}, []string{"outcome"}) // ok | invalid_token
	HeartbeatTimeouts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "furo_heartbeat_timeouts_total",
		Help: "Sessions dropped for missing pongs.",
	})

	// Registry.
	TunnelsActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "furo_tunnels_active",
		Help: "Active tunnels in the registry.",
	}, []string{"username"})
	RegistrationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "furo_registrations_total",
		Help: "Tunnel registration attempts.",
	}, []string{"outcome"}) // ok | name_taken | invalid_name
	TunnelsKilled = promauto.NewCounter(prometheus.CounterOpts{
		Name: "furo_tunnels_killed_total",
		Help: "Tunnels removed by an admin.",
	})

	// Data plane (public listener).
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "furo_http_requests_total",
		Help: "HTTP requests proxied through tunnels.",
	}, []string{"username", "status_class"}) // 1xx..5xx | err
	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "furo_http_request_duration_seconds",
		Help:    "Time from request parsed to response fully written.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"status_class"})
	ErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "furo_http_errors_total",
		Help: "Requests the proxy answered itself with an error.",
	}, []string{"reason"}) // tunnel_offline | tunnel_unavailable | bad_gateway
	BytesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "furo_public_bytes_total",
		Help: "Bytes through the public listener.",
	}, []string{"direction"}) // in | out
	UserBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "furo_user_bytes_total",
		Help: "Bytes proxied per tunnel owner (in = request to the tunnel, out = response to the visitor). The practical cost-per-user signal: proxy CPU scales with bytes moved.",
	}, []string{"username", "direction"})

	// Upgraded (WebSocket-style) connections.
	UpgradesActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "furo_upgrades_active",
		Help: "Upgraded (duplex) connections currently open.",
	})
	UpgradesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "furo_upgrades_total",
		Help: "Connections that switched to duplex via Upgrade.",
	})
)

// CountWriter wraps w, adding everything written to counter c.
func CountWriter(w io.Writer, c prometheus.Counter) io.Writer {
	return &countWriter{w: w, c: c}
}

type countWriter struct {
	w io.Writer
	c prometheus.Counter
}

func (cw *countWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.c.Add(float64(n))
	return n, err
}

// StatusClass maps an HTTP status to its metric label ("2xx"; 0 → "err").
func StatusClass(status int) string {
	if status < 100 || status > 599 {
		return "err"
	}
	return fmt.Sprintf("%dxx", status/100)
}

// Start exposes /metrics on addr. Returns the listener (Close to stop).
func Start(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("metrics listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	go http.Serve(ln, mux)
	return ln, nil
}
