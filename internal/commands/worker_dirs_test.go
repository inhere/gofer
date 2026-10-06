package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
)

// The heartbeat directory report reads the live config's execution paths and the
// roots of the worker.yaml most recently loaded (start or reload).
func TestWorkerDirsFnReportsRootsAndProjectPaths(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "worker.yaml")
	yaml := "worker_id: w1\nprojects:\n  p1:\n    host_path: " + filepath.ToSlash(proj) + "\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	wc, err := loadWorkerConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	det := &availabilityRecorder{inner: &fakeDetector{res: map[string]agent.DetectResult{}}}
	cr, err := core.Build(workerConfigToConfig(wc), core.WithAgentDetector(det))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cr.Close() }()

	setWorkerRoots([]config.WorkerRoot{{From: "/srv", To: dir}})
	roots, projects := workerDirsFn(cr)()
	if len(roots) != 1 || roots[0].To != dir {
		t.Fatalf("roots = %+v", roots)
	}
	if projects["p1"] != filepath.ToSlash(proj) && projects["p1"] != proj {
		t.Fatalf("projects = %+v, want p1 -> %s", projects, proj)
	}
	// A reload re-reads worker.yaml and refreshes the roots the heartbeat reports.
	if err := os.WriteFile(path, []byte(yaml+"roots:\n  - from: /new\n    to: "+filepath.ToSlash(dir)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = newWorkerReloadFn(cr, det, path, "w1")(nil)
	if roots, _ := workerDirsFn(cr)(); len(roots) != 1 || roots[0].From != "/new" {
		t.Fatalf("roots after reload = %+v", roots)
	}
}

// `worker show` prints the live messenger state from the heartbeat (not the
// register-time value) and warns when the worker's default workspace is missing.
func TestWorkerShowPrintsHeartbeatMessengerAndWorkspace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"worker_id": "w1", "connected": true,
			"worker": map[string]any{
				"protocol_version": 17, "messenger_status": "stopped",
				"messenger_detail": map[string]any{"status": "busy"},
				"dirs":             map[string]any{"workspace": map[string]any{"path": "/w/ws", "exists": false}},
			},
		})
	}))
	defer srv.Close()
	clientNode(t, srv.URL)
	out := captureOutput(t, func() {
		c := bindCmd(NewWorkerShowCmd())
		c.Arg("id").Set("w1")
		if err := runWorkerShow(c, nil); err != nil {
			t.Fatalf("worker show: %v", err)
		}
	})
	if !strings.Contains(out, "messenger: busy") || strings.Contains(out, "messenger: stopped") {
		t.Fatalf("output = %q, want the heartbeat messenger state", out)
	}
	if !strings.Contains(out, "workspace: /w/ws (missing") {
		t.Fatalf("output = %q, want the missing-workspace warning", out)
	}
}
