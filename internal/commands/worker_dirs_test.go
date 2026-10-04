package commands

import (
	"os"
	"path/filepath"
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
