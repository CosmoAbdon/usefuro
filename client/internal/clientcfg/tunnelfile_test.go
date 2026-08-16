package clientcfg

import (
	"strings"
	"testing"
)

func TestParseTunnelFile(t *testing.T) {
	specs, err := ParseTunnelFile([]byte(`
tunnels:
  api:
    proto: http
    port: 3003
    name: custom-api
  web:
    port: 3000
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("specs = %d, want 2", len(specs))
	}
	// sorted by key: api, web
	if specs[0].Name != "custom-api" || specs[0].LocalAddr != "localhost:3003" {
		t.Fatalf("api spec: %+v", specs[0])
	}
	if specs[1].Name != "" || specs[1].LocalAddr != "localhost:3000" {
		t.Fatalf("web spec: %+v", specs[1])
	}
}

func TestParseTunnelFileErrors(t *testing.T) {
	cases := map[string]string{
		"empty":        `tunnels: {}`,
		"no map":       `foo: bar`,
		"bad proto":    "tunnels:\n  a:\n    proto: tcp\n    port: 1",
		"bad port":     "tunnels:\n  a:\n    port: 0",
		"port too big": "tunnels:\n  a:\n    port: 70000",
		"bad yaml":     `{{{`,
	}
	for name, src := range cases {
		if _, err := ParseTunnelFile([]byte(src)); err == nil {
			t.Errorf("%s: no error for %q", name, src)
		}
	}
}

func TestParseTunnelFileProtoMessage(t *testing.T) {
	_, err := ParseTunnelFile([]byte("tunnels:\n  a:\n    proto: tcp\n    port: 1"))
	if err == nil || !strings.Contains(err.Error(), "http only") {
		t.Fatalf("err = %v", err)
	}
}
