// Package config loads and saves the furo-server config.yml.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// TLS modes.
const (
	TLSOff        = "off"         // plain HTTP/TCP (dev)
	TLSSelfSigned = "self-signed" // local CA in data_dir (dev/tests)
	TLSACME       = "acme"        // Let's Encrypt via DNS-01 (production)
)

type Config struct {
	BaseDomain  string `yaml:"base_domain"`
	ACMEEmail   string `yaml:"acme_email,omitempty"`
	DNSProvider string `yaml:"dns_provider,omitempty"` // libdns provider ("cloudflare")
	DNSToken    string `yaml:"dns_token,omitempty"`    // supports ${ENV_VAR}
	TLS         string `yaml:"tls"`                    // off | self-signed | acme
	ControlPort int    `yaml:"control_port"`
	HTTPPort    int    `yaml:"http_port"`
	AdminToken  string `yaml:"admin_token,omitempty"` // supports ${ENV_VAR}; used by the admin SPA/API (M5)
	DataDir     string `yaml:"data_dir"`
	// MetricsPort exposes Prometheus /metrics on this port; 0 disables it.
	// Plain HTTP with no auth — bind-scope or firewall it yourself.
	MetricsPort int `yaml:"metrics_port,omitempty"`
}

func Default() Config {
	return Config{
		BaseDomain:  "localhost",
		TLS:         TLSOff,
		ControlPort: 7835,
		HTTPPort:    8080,
		DataDir:     "./data",
	}
}

// Load reads path and expands ${ENV} references in secret fields.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.DNSToken = os.ExpandEnv(cfg.DNSToken)
	cfg.AdminToken = os.ExpandEnv(cfg.AdminToken)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	switch c.TLS {
	case TLSOff, TLSSelfSigned:
	case TLSACME:
		if c.ACMEEmail == "" || c.DNSProvider == "" || c.DNSToken == "" {
			return fmt.Errorf("tls: acme requires acme_email, dns_provider and dns_token")
		}
	default:
		return fmt.Errorf("tls: unknown mode %q (off | self-signed | acme)", c.TLS)
	}
	if c.BaseDomain == "" {
		return fmt.Errorf("base_domain is required")
	}
	return nil
}

// Save writes config.yml. Secrets given as ${ENV} references are saved
// verbatim (raw values passed in are saved raw — the wizard warns about it).
func Save(path string, c Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
