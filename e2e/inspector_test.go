package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

var inspectorURLRe = regexp.MustCompile(`Inspector: (http://localhost:\d+)`)

func waitInspectorURL(t *testing.T, p *proc) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := inspectorURLRe.FindStringSubmatch(p.stdout.String()); m != nil {
			return m[1]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("inspector URL never printed, stdout:\n%s", p.stdout.String())
	return ""
}

type apiSummary struct {
	ID       int64  `json:"id"`
	Tunnel   string `json:"tunnel"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Status   int    `json:"status"`
	IsReplay bool   `json:"is_replay"`
}

type apiDetail struct {
	apiSummary
	RespBody      []byte `json:"resp_body"`
	RespTruncated bool   `json:"resp_truncated"`
}

func fetchList(t *testing.T, base string) []apiSummary {
	t.Helper()
	resp, err := http.Get(base + "/api/requests")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []apiSummary
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	return list
}

func TestInspector(t *testing.T) {
	dataDir := t.TempDir()
	token := userAdd(t, dataDir, "grace")
	controlPort, httpPort := freePort(t), freePort(t)
	startServer(t, dataDir, controlPort, httpPort)
	time.Sleep(200 * time.Millisecond)

	// Local service: normal route, big route (3 MB), counter route for replay.
	hits := make(chan string, 100)
	mux := http.NewServeMux()
	mux.HandleFunc("/items", func(w http.ResponseWriter, r *http.Request) {
		hits <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[1,2,3]}`)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 3<<20))
	})
	svc := httptest.NewServer(mux)
	t.Cleanup(svc.Close)

	inspBase := freePort(t)
	controlAddr := fmt.Sprintf("127.0.0.1:%d", controlPort)
	client := startClient(t, localPort(svc), controlAddr, token,
		"--name", "api", "--plaintext", "--inspector-port", fmt.Sprint(inspBase))
	inspURL := waitInspectorURL(t, client)

	publicURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	// Generate traffic through the tunnel.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status, _, err := get(publicURL+"/items", "api.grace.localhost")
		if err == nil && status == 200 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if _, _, err := get(publicURL+"/big", "api.grace.localhost"); err != nil {
		t.Fatal(err)
	}

	t.Run("capture_list", func(t *testing.T) {
		var list []apiSummary
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			list = fetchList(t, inspURL)
			if len(list) >= 2 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if len(list) < 2 {
			t.Fatalf("captured %d entries, want >= 2", len(list))
		}
		last := list[len(list)-1]
		if last.Method != "GET" || last.Path != "/big" || last.Status != 200 || last.Tunnel != "api" {
			t.Fatalf("unexpected last entry: %+v", last)
		}
	})

	t.Run("body_truncated_at_1mb", func(t *testing.T) {
		list := fetchList(t, inspURL)
		big := list[len(list)-1]
		resp, err := http.Get(fmt.Sprintf("%s/api/requests/%d", inspURL, big.ID))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var d apiDetail
		if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
			t.Fatal(err)
		}
		if !d.RespTruncated {
			t.Fatal("3MB response not flagged truncated")
		}
		if len(d.RespBody) > 1<<20 {
			t.Fatalf("stored body %d bytes, cap is 1MB", len(d.RespBody))
		}
	})

	t.Run("replay_hits_local_directly", func(t *testing.T) {
		list := fetchList(t, inspURL)
		var itemsID int64 = -1
		for _, e := range list {
			if e.Path == "/items" && !e.IsReplay {
				itemsID = e.ID
			}
		}
		if itemsID < 0 {
			t.Fatal("no /items entry to replay")
		}
		for len(hits) > 0 {
			<-hits
		}
		resp, err := http.Post(fmt.Sprintf("%s/api/replay/%d", inspURL, itemsID), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("replay: %d %s", resp.StatusCode, body)
		}
		select {
		case <-hits:
		case <-time.After(5 * time.Second):
			t.Fatal("replay never reached the local service")
		}
		// The replayed exchange must appear as a new entry marked replay.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			list = fetchList(t, inspURL)
			last := list[len(list)-1]
			if last.IsReplay && last.Path == "/items" && last.Status == 200 {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("no replay entry, list tail: %+v", list[len(list)-1])
	})

	t.Run("clear", func(t *testing.T) {
		if _, err := http.Post(inspURL+"/api/clear", "", nil); err != nil {
			t.Fatal(err)
		}
		if list := fetchList(t, inspURL); len(list) != 0 {
			t.Fatalf("after clear: %d entries", len(list))
		}
	})

	t.Run("spa_served", func(t *testing.T) {
		resp, err := http.Get(inspURL + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || !strings.Contains(string(body), "<div id=\"root\">") {
			t.Fatalf("SPA not served: %d %q", resp.StatusCode, body[:min(len(body), 200)])
		}
	})

	t.Run("second_client_increments_port", func(t *testing.T) {
		svc2 := localService(t, "grace-2")
		client2 := startClient(t, localPort(svc2), controlAddr, token,
			"--name", "second", "--plaintext", "--inspector-port", fmt.Sprint(inspBase))
		url2 := waitInspectorURL(t, client2)
		if url2 == inspURL {
			t.Fatalf("second inspector reused %s", inspURL)
		}
	})
}
