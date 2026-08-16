// Package tlsmgr provides server certificates: ACME (Let's Encrypt DNS-01
// via certmagic + libdns) for production, or a local self-signed CA for
// dev/tests. Wildcards are per user: *.username.<base_domain>.
package tlsmgr

import (
	"crypto/tls"
	"fmt"

	"github.com/cosmoabdon/furo/server/internal/config"
)

// Manager hands out certs for the control and public listeners and emits
// per-user wildcards.
type Manager interface {
	GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
	// EnsureBase obtains certs for base_domain and *.base_domain (admin +
	// control endpoint).
	EnsureBase() error
	// EnsureUser obtains the *.username.base_domain wildcard. Called on
	// `user add` and at serve startup for existing users.
	EnsureUser(username string) error
}

// New returns nil (no TLS) for mode off.
func New(cfg config.Config) (Manager, error) {
	switch cfg.TLS {
	case config.TLSOff:
		return nil, nil
	case config.TLSSelfSigned:
		return newSelfSigned(cfg.DataDir)
	case config.TLSACME:
		return newACME(cfg)
	default:
		return nil, fmt.Errorf("unknown tls mode %q", cfg.TLS)
	}
}

// ServerTLSConfig wraps a Manager for use on a listener. HTTP/1.1 only —
// the tunnel proxy is a byte-level HTTP/1.1 path.
func ServerTLSConfig(m Manager) *tls.Config {
	return &tls.Config{
		GetCertificate: m.GetCertificate,
		NextProtos:     []string{"http/1.1"},
		MinVersion:     tls.VersionTLS12,
	}
}
