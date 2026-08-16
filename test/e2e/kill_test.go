package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Admin kills one tunnel of two: it 404s and is never re-registered, the
// other keeps working. Killing the last one shuts the client down.
func TestAdminKillTunnel(t *testing.T) {
	dataDir := t.TempDir()
	token := userAdd(t, dataDir, "judy")
	controlPort, httpPort := freePort(t), freePort(t)
	const admin = "kill-test-admin"
	startServer(t, dataDir, controlPort, httpPort, "--admin-token", admin)
	waitPort(t, httpPort)

	apiSvc := localService(t, "judy-api")
	webSvc := localService(t, "judy-web")
	furoYml := filepath.Join(t.TempDir(), "furo.yml")
	os.WriteFile(furoYml, []byte(fmt.Sprintf(
		"tunnels:\n  api:\n    port: %s\n    name: api\n  web:\n    port: %s\n    name: web\n",
		localPort(apiSvc), localPort(webSvc))), 0o644)

	client := startProc(t, clientBin, "start",
		"--file", furoYml,
		"--server", fmt.Sprintf("127.0.0.1:%d", controlPort),
		"--token", token, "--plaintext",
		"--inspector-port", fmt.Sprint(freePort(t)))

	base := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	waitHTTP(t, base+"/", "api.judy.localhost", "hello from judy-api")
	waitHTTP(t, base+"/", "web.judy.localhost", "hello from judy-web")

	t.Run("kill_one_of_two", func(t *testing.T) {
		resp, body := adminReq(t, "DELETE", base+"/api/tunnels/judy/api", admin, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("kill: %d %s", resp.StatusCode, body)
		}
		// Killed tunnel goes offline and stays offline (no re-register).
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if status, _, _ := get(base+"/", "api.judy.localhost"); status == 404 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		status, respBody, err := get(base+"/", "api.judy.localhost")
		if err != nil || status != 404 {
			t.Fatalf("killed tunnel: status=%d body=%q err=%v, want 404", status, respBody, err)
		}
		// Give a would-be re-register time to happen, then check again.
		time.Sleep(2 * time.Second)
		if status, _, _ := get(base+"/", "api.judy.localhost"); status != 404 {
			t.Fatalf("killed tunnel came back: %d", status)
		}
		// Sibling tunnel unaffected.
		if _, respBody, err := get(base+"/", "web.judy.localhost"); err != nil || !strings.Contains(respBody, "judy-web") {
			t.Fatalf("sibling tunnel broken: %q %v", respBody, err)
		}
		// Client process still alive.
		select {
		case <-client.exited:
			t.Fatal("client died after partial kill")
		default:
		}
	})

	t.Run("kill_last_stops_client", func(t *testing.T) {
		resp, body := adminReq(t, "DELETE", base+"/api/tunnels/judy/web", admin, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("kill: %d %s", resp.StatusCode, body)
		}
		select {
		case <-client.exited: // client exited on its own
		case <-time.After(10 * time.Second):
			t.Fatal("client still running after all tunnels were killed")
		}
	})

	t.Run("kill_missing_404", func(t *testing.T) {
		resp, _ := adminReq(t, "DELETE", base+"/api/tunnels/judy/ghost", admin, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("missing tunnel kill: %d, want 404", resp.StatusCode)
		}
	})
}
