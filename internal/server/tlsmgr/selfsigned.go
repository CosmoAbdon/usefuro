package tlsmgr

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// selfSigned mints leaf certs on demand, signed by a local CA persisted in
// <dataDir>/certs (ca.pem / ca.key). Clients trust ca.pem via `furo --ca`.
type selfSigned struct {
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey

	mu    sync.Mutex
	cache map[string]*tls.Certificate // by SNI host ("" → fallback with IP SANs)
}

func newSelfSigned(dataDir string) (*selfSigned, error) {
	dir := filepath.Join(dataDir, "certs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &selfSigned{cache: make(map[string]*tls.Certificate)}
	if err := s.loadOrCreateCA(filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key")); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *selfSigned) loadOrCreateCA(certPath, keyPath string) error {
	certPEM, errC := os.ReadFile(certPath)
	keyPEM, errK := os.ReadFile(keyPath)
	if errC == nil && errK == nil {
		cb, _ := pem.Decode(certPEM)
		kb, _ := pem.Decode(keyPEM)
		if cb == nil || kb == nil {
			return fmt.Errorf("corrupt CA files in %s", filepath.Dir(certPath))
		}
		cert, err := x509.ParseCertificate(cb.Bytes)
		if err != nil {
			return err
		}
		key, err := x509.ParseECPrivateKey(kb.Bytes)
		if err != nil {
			return err
		}
		s.caCert, s.caKey = cert, key
		return nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "furo dev CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	s.caCert, s.caKey = cert, key
	return nil
}

func (s *selfSigned) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := hello.ServerName // "" when the client connected by IP
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[host]; ok {
		return c, nil
	}
	c, err := s.mint(host)
	if err != nil {
		return nil, err
	}
	s.cache[host] = c
	return c, nil
}

// mint signs a leaf for host; empty host → fallback cert with localhost +
// loopback IP SANs (clients dialing 127.0.0.1 send no SNI).
func (s *selfSigned) mint(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "furo dev"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if host == "" {
		tmpl.DNSNames = []string{"localhost"}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	} else if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.caCert, &key.PublicKey, s.caKey)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, s.caCert.Raw}, PrivateKey: key}, nil
}

func (s *selfSigned) EnsureBase() error                { return nil } // minted on demand
func (s *selfSigned) EnsureUser(username string) error { return nil } // minted on demand
