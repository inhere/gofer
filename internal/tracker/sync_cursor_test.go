package tracker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

func TestSyncSecondRequestContainsOnlyDelta(t *testing.T) {
	root := t.TempDir()
	s, _, err := Init(root, "cur", true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateIssue(Issue{Title: "one", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	var lengths []int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Issues []struct {
				ID string `json:"id"`
			} `json:"issues"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		lengths = append(lengths, len(body.Issues))
		accepted := map[string]int64{}
		for _, i := range body.Issues {
			accepted[i.ID] = 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issue_cursor": 1, "memory_cursor": 0, "issues": []any{}, "memories": []any{},
			"accepted": map[string]any{"issues": accepted}})
	}))
	defer ts.Close()
	ctx := context.Background()
	if _, err := SyncHTTP(ctx, s, ts.URL); err != nil {
		t.Fatal(err)
	}
	// First sync: full pull (rev map bootstrap), then the push of the new issue.
	if len(lengths) != 2 || lengths[0] != 0 || lengths[1] != 1 {
		t.Fatalf("first sync lengths=%v", lengths)
	}
	if _, err := SyncHTTP(ctx, s, ts.URL); err != nil {
		t.Fatal(err)
	}
	if len(lengths) != 3 || lengths[2] != 0 {
		t.Fatalf("second sync lengths=%v", lengths)
	}
}

// TestSyncSendsClientHost: every sync request names the machine it comes from
// (client_host = os.Hostname), so the server can keep one sync record per machine.
func TestSyncSendsClientHost(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		t.Skipf("no hostname: %v", err)
	}
	s, _, err := Init(t.TempDir(), "host", true)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var hosts []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ClientHost *string `json:"client_host"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if body.ClientHost == nil {
			hosts = append(hosts, "<absent>")
		} else {
			hosts = append(hosts, *body.ClientHost)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issue_cursor": 0, "memory_cursor": 0, "issues": []any{}, "memories": []any{}})
	}))
	defer ts.Close()
	if _, err := SyncHTTP(context.Background(), s, ts.URL); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hosts) == 0 {
		t.Fatal("no sync request reached the server")
	}
	for _, h := range hosts {
		if h != want {
			t.Fatalf("client_host = %q, want %q (all: %v)", h, want, hosts)
		}
	}
}
