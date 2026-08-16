// Package clientcfg persists `furo login` settings in ~/.config/furo/config.yml.
package clientcfg

import (
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

type File struct {
	Server    string `yaml:"server"`              // control address host:port
	Token     string `yaml:"token"`               // furo_...
	CA        string `yaml:"ca,omitempty"`        // path to a CA bundle (self-signed servers)
	Insecure  bool   `yaml:"insecure,omitempty"`  // skip TLS verification
	Plaintext bool   `yaml:"plaintext,omitempty"` // no TLS on the control connection (dev)
}

func Path() (string, error) {
	if runtime.GOOS == "windows" {
		dir, err := os.UserConfigDir() // %AppData%
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "furo", "config.yml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "furo", "config.yml"), nil
}

// Load returns a zero File (no error) when the config does not exist yet.
func Load() (File, error) {
	var f File
	path, err := Path()
	if err != nil {
		return f, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	return f, yaml.Unmarshal(b, &f)
}

func Save(f File) (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	b, err := yaml.Marshal(f)
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, b, 0o600)
}
