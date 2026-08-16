package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartMultiTunnelAndStatus(t *testing.T) {
	dataDir := t.TempDir()
	token := userAdd(t, dataDir, "ivan")
	controlPort, httpPort := freePort(t), freePort(t)
	startServer(t, dataDir, controlPort, httpPort)
	waitPort(t, httpPort)

	apiSvc := localService(t, "ivan-api")
	webSvc := localService(t, "ivan-web")

	furoYml := filepath.Join(t.TempDir(), "furo.yml")
	os.WriteFile(furoYml, []byte(fmt.Sprintf(`
tunnels:
  api:
    proto: http
    port: %s
    name: api
  web:
    port: %s
`, localPort(apiSvc), localPort(webSvc))), 0o644)

	inspBase := freePort(t)
	client := startProc(t, clientBin, "start",
		"--file", furoYml,
		"--server", fmt.Sprintf("127.0.0.1:%d", controlPort),
		"--token", token, "--plaintext",
		"--inspector-port", fmt.Sprint(inspBase))

	publicURL := fmt.Sprintf("http://127.0.0.1:%d/", httpPort)
	waitHTTP(t, publicURL, "api.ivan.localhost", "hello from ivan-api")

	// Second tunnel got a server-generated name — read it from stdout.
	var webName string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(client.stdout.String(), "\n") {
			if strings.Contains(line, ".ivan.localhost") && !strings.Contains(line, "api.ivan") {
				webName = strings.TrimPrefix(strings.Fields(line)[1], "http://")
				webName = strings.SplitN(webName, ".", 2)[0]
			}
		}
		if webName != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if webName == "" {
		t.Fatalf("no generated name printed, stdout:\n%s", client.stdout.String())
	}
	waitHTTP(t, publicURL, webName+".ivan.localhost", "hello from ivan-web")

	t.Run("both_tunnels_share_one_session", func(t *testing.T) {
		// Same process serves both names, routed by tunnel_id.
		_, body, err := get(publicURL, "api.ivan.localhost")
		if err != nil || !strings.Contains(body, "ivan-api") {
			t.Fatalf("api: %q %v", body, err)
		}
		_, body, err = get(publicURL, webName+".ivan.localhost")
		if err != nil || !strings.Contains(body, "ivan-web") {
			t.Fatalf("web: %q %v", body, err)
		}
	})

	t.Run("status_lists_both", func(t *testing.T) {
		cmd := exec.Command(clientBin, "status", "--inspector-port", fmt.Sprint(inspBase))
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("status: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "api.ivan.localhost") || !strings.Contains(s, webName+".ivan.localhost") {
			t.Fatalf("status output missing tunnels:\n%s", s)
		}
		if !strings.Contains(s, "NAME") || !strings.Contains(s, "INSPECTOR") {
			t.Fatalf("status output missing header:\n%s", s)
		}
	})

	t.Run("status_empty_range", func(t *testing.T) {
		empty := freePort(t)
		cmd := exec.Command(clientBin, "status", "--inspector-port", fmt.Sprint(empty))
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
		out, _ := cmd.CombinedOutput()
		if !strings.Contains(string(out), "no active tunnels") {
			t.Fatalf("expected empty message, got:\n%s", out)
		}
	})
}
