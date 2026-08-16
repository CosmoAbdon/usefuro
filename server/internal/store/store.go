// Package store persists users, tokens and reserved names in SQLite
// (modernc.org/sqlite, no CGO). Active tunnels never touch the database.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/cosmoabdon/usefuro/server/internal/names"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrAmbiguous = errors.New("prefix matches more than one token")
	ErrInvalid   = errors.New("invalid value")
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS tokens (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  label TEXT,
  revoked_at TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS reserved_names (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  PRIMARY KEY (user_id, name)
);
`

type Store struct {
	db *sql.DB
}

// Open creates dataDir if needed and opens (migrating) furo.db inside it.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dataDir, "furo.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ---- users ----

func (s *Store) CreateUser(username string) error {
	if !names.Valid(username) {
		return fmt.Errorf("%w: username must be a DNS label (lowercase, a-z0-9, hyphens inside)", ErrInvalid)
	}
	_, err := s.db.Exec(`INSERT INTO users (username) VALUES (?)`, username)
	return err
}

func (s *Store) DeleteUser(username string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type UserInfo struct {
	Username   string
	CreatedAt  string
	TokenCount int
}

func (s *Store) Users() ([]UserInfo, error) {
	rows, err := s.db.Query(`
		SELECT u.username, u.created_at,
		       (SELECT COUNT(*) FROM tokens t WHERE t.user_id = u.id AND t.revoked_at IS NULL)
		FROM users u ORDER BY u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserInfo
	for rows.Next() {
		var u UserInfo
		if err := rows.Scan(&u.Username, &u.CreatedAt, &u.TokenCount); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---- tokens ----

const tokenAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	out := make([]byte, 32)
	for i := range out {
		out[i] = tokenAlphabet[int(b[i])%len(tokenAlphabet)]
	}
	return "furo_" + string(out)
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateToken mints a token for username and returns the plaintext — the only
// time it is ever available. Only the SHA-256 is stored.
func (s *Store) CreateToken(username, label string) (string, error) {
	token := newToken()
	res, err := s.db.Exec(`
		INSERT INTO tokens (user_id, token_hash, label)
		SELECT id, ?, ? FROM users WHERE username = ?`,
		hashToken(token), label, username)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("user %q: %w", username, ErrNotFound)
	}
	return token, nil
}

type TokenInfo struct {
	HashPrefix string
	Label      string
	CreatedAt  string
	Revoked    bool
}

func (s *Store) Tokens(username string) ([]TokenInfo, error) {
	rows, err := s.db.Query(`
		SELECT substr(t.token_hash, 1, 12), COALESCE(t.label, ''), t.created_at, t.revoked_at IS NOT NULL
		FROM tokens t JOIN users u ON u.id = t.user_id
		WHERE u.username = ? ORDER BY t.created_at`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		var t TokenInfo
		if err := rows.Scan(&t.HashPrefix, &t.Label, &t.CreatedAt, &t.Revoked); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken revokes the single active token whose hash starts with prefix.
func (s *Store) RevokeToken(prefix string) error {
	if prefix == "" {
		return ErrInvalid
	}
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE token_hash LIKE ? || '%' AND revoked_at IS NULL`,
		prefix).Scan(&count)
	if err != nil {
		return err
	}
	switch {
	case count == 0:
		return ErrNotFound
	case count > 1:
		return ErrAmbiguous
	}
	_, err = s.db.Exec(`UPDATE tokens SET revoked_at = datetime('now') WHERE token_hash LIKE ? || '%' AND revoked_at IS NULL`,
		prefix)
	return err
}

// Authenticate resolves a plaintext token to its username. Revoked or unknown
// tokens fail.
func (s *Store) Authenticate(token string) (string, error) {
	var username string
	err := s.db.QueryRow(`
		SELECT u.username FROM tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ? AND t.revoked_at IS NULL`,
		hashToken(token)).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("invalid token: %w", ErrNotFound)
	}
	return username, err
}
