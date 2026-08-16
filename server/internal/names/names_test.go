package names

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	valid := []string{"a", "api", "api-orbium", "a1", "x0-y9", strings.Repeat("a", 63)}
	for _, s := range valid {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "1api", "-api", "api-", "Api", "api_x", "api.x", strings.Repeat("a", 64)}
	for _, s := range invalid {
		if Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}

func TestGenerate(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		n := Generate()
		if len(n) != 8 {
			t.Fatalf("len(%q) = %d, want 8", n, len(n))
		}
		if !Valid(n) {
			t.Fatalf("Generate() produced invalid name %q", n)
		}
		if n[0] >= '0' && n[0] <= '9' {
			t.Fatalf("first char is a digit: %q", n)
		}
		seen[n] = true
	}
	if len(seen) < 990 {
		t.Fatalf("too many collisions in 1000 names: %d unique", len(seen))
	}
}
