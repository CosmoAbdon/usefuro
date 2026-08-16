// Package names validates and generates DNS-label tunnel/user names.
package names

import (
	"crypto/rand"
	"regexp"
)

// DNS label: lowercase, no hyphen at the edges, max 63 chars.
var re = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

func Valid(s string) bool { return re.MatchString(s) }

const (
	alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	letters  = "abcdefghijklmnopqrstuvwxyz"
	genLen   = 8
)

// Generate returns a random tunnel name: 8 chars of a-z0-9, first char
// always a letter (so it is a valid DNS label).
func Generate() string {
	b := make([]byte, genLen)
	rand.Read(b)
	out := make([]byte, genLen)
	out[0] = letters[int(b[0])%len(letters)]
	for i := 1; i < genLen; i++ {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}
