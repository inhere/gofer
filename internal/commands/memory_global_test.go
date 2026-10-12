package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/tracker"
)

// gofer-j6qn: `memory --global` works on this machine's copy under
// <config-dir>/tracker — with no server at all — and syncs it when one is there.
func TestGlobalMemoryLocalCopy(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("GOFER_CONFIG_DIR", cfgDir)
	t.Setenv("GOFER_JOB_TOKEN", "")
	t.Setenv("GOFER_JOB_ID", "")
	t.Setenv("GOFER_SESSION_ID", "")
	// Sync goes to a server that records the push and answers with one more memory.
	var mu sync.Mutex
	var pushed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tracker/sync" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var req struct {
			TrackerID string `json:"tracker_id"`
			Memories  []struct {
				ID string `json:"id"`
			} `json:"memories"`
		}
		_ = json.Unmarshal(b, &req)
		mu.Lock()
		for _, m := range req.Memories {
			pushed = append(pushed, req.TrackerID+":"+m.ID)
		}
		mu.Unlock()
		accepted := map[string]int64{}
		for _, m := range req.Memories {
			accepted[m.ID] = 1
		}
		body, _ := json.Marshal(tracker.Memory{Key: "from-server", Content: "server side", UpdatedAt: "2026-10-12T00:00:00Z", By: "web"})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issues": []any{}, "memories": []map[string]any{{"tracker_id": "global", "id": "from-server", "body": json.RawMessage(body), "rev": 1, "changed_seq": 1}},
			"issue_cursor": 0, "memory_cursor": 1,
			"accepted":  map[string]any{"issues": map[string]int64{}, "memories": accepted},
			"conflicts": map[string]any{"issues": []any{}, "memories": []any{}},
		})
	}))
	defer srv.Close()
	t.Setenv("GOFER_SERVER_ADDR", srv.URL)
	t.Setenv("GOFER_SERVER_TOKEN", "tok")
	root := t.TempDir() // no repository tracker here: --global does not need one

	trackerRunOK(t, root, "memory", "set", "lang", "reply in Chinese", "--global", "--kind", "rule")
	out := trackerRunOK(t, root, "memory", "ls", "--global")
	assert.Contains(t, out, "lang")
	assert.Contains(t, out, "from-server") // the first use pulled the server's memories
	assert.Contains(t, trackerRunOK(t, root, "memory", "show", "lang", "--global"), "reply in Chinese")
	b, err := os.ReadFile(filepath.Join(cfgDir, tracker.GlobalDirName, "memories.jsonl"))
	assert.Require(t, assert.NoErr(t, err))
	assert.Contains(t, string(b), "reply in Chinese")
	mu.Lock()
	assert.Contains(t, strings.Join(pushed, ","), "global:lang")
	mu.Unlock()

	// archive / restore / doctor / sync work on the global copy too.
	trackerRunOK(t, root, "memory", "archive", "lang", "--global", "--reason", "test")
	assert.NotContains(t, trackerRunOK(t, root, "memory", "ls", "--global"), "lang ")
	trackerRunOK(t, root, "memory", "restore", "lang", "--global")
	trackerRunOK(t, root, "memory", "doctor", "--global")
	assert.Contains(t, trackerRunOK(t, root, "memory", "sync", "--global"), "")
	out, code := trackerCLI(t, root, "memory", "sync")
	assert.NotEq(t, 0, code)
	assert.Contains(t, out, "repo sync")

	// Inside a job the global memories are read-only.
	t.Setenv("GOFER_JOB_TOKEN", "job-tok")
	out, code = trackerCLI(t, root, "memory", "set", "x", "y", "--global")
	assert.NotEq(t, 0, code)
	assert.Contains(t, out, "a job cannot change global memories")
	out, code = trackerCLI(t, root, "memory", "rm", "lang", "--global")
	assert.NotEq(t, 0, code)
	assert.Contains(t, out, "a job cannot change global memories")
	assert.Contains(t, trackerRunOK(t, root, "memory", "ls", "--global"), "lang")
}

// Without any server the global copy still works.
func TestGlobalMemoryOffline(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("GOFER_CONFIG_DIR", cfgDir)
	t.Setenv("GOFER_JOB_TOKEN", "")
	t.Setenv("GOFER_SERVER_ADDR", "http://127.0.0.1:1")
	t.Setenv("GOFER_SERVER_TOKEN", "")
	root := t.TempDir()
	trackerRunOK(t, root, "memory", "set", "k", "offline value", "--global")
	assert.Contains(t, trackerRunOK(t, root, "memory", "show", "k", "--global"), "offline value")
	trackerRunOK(t, root, "memory", "rm", "k", "--global")
	assert.NotContains(t, trackerRunOK(t, root, "memory", "ls", "--global"), "offline value")
}
