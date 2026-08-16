package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func adminReq(t *testing.T, method, url, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Host = "localhost" // bare base domain → admin
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, out
}

func TestAdminAPI(t *testing.T) {
	dataDir := t.TempDir()
	controlPort, httpPort := freePort(t), freePort(t)
	const admin = "super-secret-admin"
	startServer(t, dataDir, controlPort, httpPort, "--admin-token", admin)
	waitPort(t, httpPort)

	base := fmt.Sprintf("http://127.0.0.1:%d", httpPort)

	t.Run("auth_required", func(t *testing.T) {
		resp, _ := adminReq(t, "GET", base+"/api/users", "", nil)
		if resp.StatusCode != 401 {
			t.Fatalf("no token: %d, want 401", resp.StatusCode)
		}
		resp, _ = adminReq(t, "GET", base+"/api/users", "wrong", nil)
		if resp.StatusCode != 401 {
			t.Fatalf("wrong token: %d, want 401", resp.StatusCode)
		}
	})

	var userToken string
	t.Run("create_user_via_api", func(t *testing.T) {
		resp, body := adminReq(t, "POST", base+"/api/users", admin, map[string]string{"username": "heidi"})
		if resp.StatusCode != 201 {
			t.Fatalf("create: %d %s", resp.StatusCode, body)
		}
		var out struct{ Username, Token string }
		json.Unmarshal(body, &out)
		if out.Username != "heidi" || !strings.HasPrefix(out.Token, "furo_") {
			t.Fatalf("bad create response: %s", body)
		}
		userToken = out.Token

		resp, body = adminReq(t, "POST", base+"/api/users", admin, map[string]string{"username": "heidi"})
		if resp.StatusCode != 409 {
			t.Fatalf("duplicate: %d %s, want 409", resp.StatusCode, body)
		}
		resp, _ = adminReq(t, "POST", base+"/api/users", admin, map[string]string{"username": "Not_Valid"})
		if resp.StatusCode != 400 {
			t.Fatalf("invalid name: %d, want 400", resp.StatusCode)
		}
	})

	t.Run("active_tunnels_and_online_count", func(t *testing.T) {
		svc := localService(t, "heidi-svc")
		startClient(t, localPort(svc), fmt.Sprintf("127.0.0.1:%d", controlPort), userToken,
			"--name", "app", "--plaintext")
		waitHTTP(t, base+"/", "app.heidi.localhost", "hello from heidi-svc")

		_, body := adminReq(t, "GET", base+"/api/tunnels", admin, nil)
		var tunnels []struct {
			Username, Name, URL string
			UptimeSeconds       int64 `json:"uptime_seconds"`
		}
		json.Unmarshal(body, &tunnels)
		if len(tunnels) != 1 || tunnels[0].Username != "heidi" || tunnels[0].Name != "app" {
			t.Fatalf("tunnels: %s", body)
		}
		if !strings.Contains(tunnels[0].URL, "app.heidi.localhost") {
			t.Fatalf("bad url: %s", tunnels[0].URL)
		}

		_, body = adminReq(t, "GET", base+"/api/users", admin, nil)
		var users []struct {
			Username      string `json:"username"`
			TokenCount    int    `json:"token_count"`
			TunnelsOnline int    `json:"tunnels_online"`
		}
		json.Unmarshal(body, &users)
		if len(users) != 1 || users[0].TunnelsOnline != 1 || users[0].TokenCount != 1 {
			t.Fatalf("users: %s", body)
		}
	})

	t.Run("tokens_create_list_revoke", func(t *testing.T) {
		resp, body := adminReq(t, "POST", base+"/api/users/heidi/tokens", admin, map[string]string{"label": "ci"})
		if resp.StatusCode != 201 {
			t.Fatalf("token add: %d %s", resp.StatusCode, body)
		}
		_, body = adminReq(t, "GET", base+"/api/users/heidi/tokens", admin, nil)
		var tokens []struct {
			HashPrefix, Label string
			Revoked           bool
		}
		json.Unmarshal(body, &tokens)
		if len(tokens) != 2 {
			t.Fatalf("tokens: %s", body)
		}
		var ciPrefix string
		for _, tk := range tokens {
			if tk.Label == "ci" {
				ciPrefix = tk.HashPrefix
			}
		}
		resp, body = adminReq(t, "POST", base+"/api/tokens/revoke", admin, map[string]string{"prefix": ciPrefix})
		if resp.StatusCode != 200 {
			t.Fatalf("revoke: %d %s", resp.StatusCode, body)
		}
		_, body = adminReq(t, "GET", base+"/api/users/heidi/tokens", admin, nil)
		json.Unmarshal(body, &tokens)
		revoked := 0
		for _, tk := range tokens {
			if tk.Revoked {
				revoked++
			}
		}
		if revoked != 1 {
			t.Fatalf("revoked=%d, want 1: %s", revoked, body)
		}
	})

	t.Run("spa_served_on_base_host", func(t *testing.T) {
		resp, body := adminReq(t, "GET", base+"/", "", nil)
		if resp.StatusCode != 200 || !strings.Contains(string(body), "<div id=\"root\">") {
			t.Fatalf("SPA: %d %q", resp.StatusCode, body[:min(len(body), 200)])
		}
	})
}

func TestAdminDisabledWithoutToken(t *testing.T) {
	dataDir := t.TempDir()
	controlPort, httpPort := freePort(t), freePort(t)
	startServer(t, dataDir, controlPort, httpPort) // no --admin-token
	waitPort(t, httpPort)

	base := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	resp, _ := adminReq(t, "GET", base+"/api/users", "anything", nil)
	if resp.StatusCode != 503 {
		t.Fatalf("admin without token configured: %d, want 503", resp.StatusCode)
	}
}
