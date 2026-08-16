package tlsmgr

import (
	"context"
	"crypto/tls"
	"fmt"
	"path/filepath"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"

	"github.com/cosmoabdon/furo/server/internal/config"
)

// acmeManager issues wildcards via Let's Encrypt DNS-01 (certmagic + libdns).
type acmeManager struct {
	magic *certmagic.Config
	base  string
}

// dnsProvider resolves a libdns provider by name. Cloudflare ships in v1;
// adding another provider is one import + one case here.
func dnsProvider(name, token string) (certmagic.DNSProvider, error) {
	switch name {
	case "cloudflare":
		return &cloudflare.Provider{APIToken: token}, nil
	default:
		return nil, fmt.Errorf("unsupported dns_provider %q (supported: cloudflare)", name)
	}
}

func newACME(cfg config.Config) (*acmeManager, error) {
	provider, err := dnsProvider(cfg.DNSProvider, cfg.DNSToken)
	if err != nil {
		return nil, err
	}

	var magic *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return magic, nil
		},
	})
	magic = certmagic.New(cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: filepath.Join(cfg.DataDir, "certs")},
	})
	issuer := certmagic.NewACMEIssuer(magic, certmagic.ACMEIssuer{
		CA:     certmagic.LetsEncryptProductionCA,
		Email:  cfg.ACMEEmail,
		Agreed: true,
		DNS01Solver: &certmagic.DNS01Solver{
			DNSManager: certmagic.DNSManager{DNSProvider: provider},
		},
	})
	magic.Issuers = []certmagic.Issuer{issuer}

	return &acmeManager{magic: magic, base: cfg.BaseDomain}, nil
}

func (a *acmeManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return a.magic.GetCertificate(hello)
}

func (a *acmeManager) EnsureBase() error {
	return a.magic.ManageSync(context.Background(), []string{a.base, "*." + a.base})
}

func (a *acmeManager) EnsureUser(username string) error {
	return a.magic.ManageSync(context.Background(), []string{"*." + username + "." + a.base})
}

// EnsureUsersAsync kicks off issuance for existing users without blocking
// startup (renewals also happen here).
func (a *acmeManager) EnsureUsersAsync(usernames []string) error {
	domains := []string{a.base, "*." + a.base}
	for _, u := range usernames {
		domains = append(domains, "*."+u+"."+a.base)
	}
	return a.magic.ManageAsync(context.Background(), domains)
}
