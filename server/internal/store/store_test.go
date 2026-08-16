package store

import (
	"errors"
	"strings"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUserCRUD(t *testing.T) {
	s := open(t)

	if err := s.CreateUser("alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("alice"); err == nil {
		t.Fatal("duplicate username accepted")
	}
	if err := s.CreateUser("Not_A_Label"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid username: got %v, want ErrInvalid", err)
	}

	users, err := s.Users()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Username != "alice" || users[0].TokenCount != 0 {
		t.Fatalf("unexpected users: %+v", users)
	}

	if err := s.DeleteUser("bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing user: got %v, want ErrNotFound", err)
	}
	if err := s.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
}

func TestTokenLifecycle(t *testing.T) {
	s := open(t)
	if err := s.CreateUser("alice"); err != nil {
		t.Fatal(err)
	}

	token, err := s.CreateToken("alice", "notebook")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "furo_") || len(token) != len("furo_")+32 {
		t.Fatalf("bad token format: %q", token)
	}

	if _, err := s.CreateToken("ghost", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token for missing user: got %v, want ErrNotFound", err)
	}

	username, err := s.Authenticate(token)
	if err != nil || username != "alice" {
		t.Fatalf("auth: got (%q, %v), want (alice, nil)", username, err)
	}
	if _, err := s.Authenticate("furo_bogus"); err == nil {
		t.Fatal("bogus token authenticated")
	}

	tokens, err := s.Tokens("alice")
	if err != nil || len(tokens) != 1 || tokens[0].Label != "notebook" || tokens[0].Revoked {
		t.Fatalf("tokens: %+v, err %v", tokens, err)
	}

	if err := s.RevokeToken(tokens[0].HashPrefix); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(token); err == nil {
		t.Fatal("revoked token still authenticates")
	}
	if err := s.RevokeToken(tokens[0].HashPrefix); !errors.Is(err, ErrNotFound) {
		t.Fatalf("re-revoke: got %v, want ErrNotFound", err)
	}
}

func TestDeleteUserCascadesTokens(t *testing.T) {
	s := open(t)
	if err := s.CreateUser("alice"); err != nil {
		t.Fatal(err)
	}
	token, err := s.CreateToken("alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(token); err == nil {
		t.Fatal("token of deleted user still authenticates")
	}
}
