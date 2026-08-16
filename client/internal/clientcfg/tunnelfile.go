package clientcfg

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/cosmoabdon/furo/client/internal/tunnel"
)

// TunnelFile is the furo.yml format (compose-style):
//
//	tunnels:
//	  api:
//	    proto: http
//	    port: 3003
//	    name: api-orbium
//	  web:
//	    proto: http
//	    port: 3000        # no name → server-generated
type TunnelFile struct {
	Tunnels map[string]TunnelEntry `yaml:"tunnels"`
}

type TunnelEntry struct {
	Proto string `yaml:"proto"` // only "http" in v1; empty defaults to http
	Port  int    `yaml:"port"`
	Name  string `yaml:"name"` // public name; empty → server-generated
}

// ParseTunnelFile validates and converts furo.yml bytes into tunnel specs,
// ordered by their key for deterministic registration.
func ParseTunnelFile(b []byte) ([]tunnel.TunnelSpec, error) {
	var f TunnelFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if len(f.Tunnels) == 0 {
		return nil, fmt.Errorf("no tunnels defined (expected a top-level 'tunnels:' map)")
	}
	keys := make([]string, 0, len(f.Tunnels))
	for k := range f.Tunnels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	specs := make([]tunnel.TunnelSpec, 0, len(keys))
	for _, k := range keys {
		e := f.Tunnels[k]
		if e.Proto != "" && e.Proto != "http" {
			return nil, fmt.Errorf("tunnel %q: unsupported proto %q (v1 supports http only)", k, e.Proto)
		}
		if e.Port <= 0 || e.Port > 65535 {
			return nil, fmt.Errorf("tunnel %q: invalid port %d", k, e.Port)
		}
		specs = append(specs, tunnel.TunnelSpec{
			Name: e.Name,
			// "localhost" so IPv6-only local servers (Node 17+) also work.
			LocalAddr: fmt.Sprintf("localhost:%d", e.Port),
		})
	}
	return specs, nil
}

// LoadTunnelFile reads and parses furo.yml from path.
func LoadTunnelFile(path string) ([]tunnel.TunnelSpec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseTunnelFile(b)
}
