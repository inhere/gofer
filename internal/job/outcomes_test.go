package job

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/runner"
)

// TestCaptureRenderedCommandLocalExec proves a local exec job records its
// rendered command (E15) into RenderedCommand after it finishes, round-tripping
// through Get (DB-backed once the entry is evicted).
func TestCaptureRenderedCommandLocalExec(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}

	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get(%s) not found", final.ID)
	}
	if got.RenderedCommand == "" {
		t.Fatalf("RenderedCommand is empty, want rendered argv JSON")
	}
	var rc struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(got.RenderedCommand), &rc); err != nil {
		t.Fatalf("RenderedCommand is not valid JSON: %v (%q)", err, got.RenderedCommand)
	}
	if rc.Command != "go" || len(rc.Args) != 1 || rc.Args[0] != "version" {
		t.Fatalf("unexpected rendered command: %+v", rc)
	}
}

// TestCaptureDiffEndToEnd proves a local exec job whose cwd is a git work tree
// with an uncommitted tracked change records a DiffSummary (E12) after finishing,
// round-tripping through Get, and drops changes.diff into the result dir.
func TestCaptureDiffEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping diff end-to-end test")
	}
	root := t.TempDir()
	s := newTestService(t, root)
	// Make the "self" project's host path (root) a git repo with an uncommitted
	// change so the job's cwd ("." → root) has a non-empty `git diff`.
	initGitRepo(t, root)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}

	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get(%s) not found", final.ID)
	}
	if !strings.Contains(got.DiffSummary, "tracked.txt") {
		t.Fatalf("DiffSummary should mention the changed file, got %q", got.DiffSummary)
	}
	diffPath := filepath.Join(got.ResultDir, "changes.diff")
	if b, err := os.ReadFile(diffPath); err != nil {
		t.Fatalf("changes.diff not written for job: %v", err)
	} else if !strings.Contains(string(b), "modified content") {
		t.Fatalf("changes.diff missing tracked change:\n%s", b)
	}
}

// TestCaptureDiffDisabledByProject proves an explicit capture_diff:false on the
// project skips diff capture entirely: no DiffSummary, no changes.diff — even
// though the cwd is a dirty git repo.
func TestCaptureDiffDisabledByProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	s := newTestService(t, root)
	initGitRepo(t, root)

	// Flip capture_diff:false on the "self" project (same pattern as prune_test's
	// in-place config mutation).
	disabled := false
	proj := s.config().Projects["self"]
	proj.CaptureDiff = &disabled
	s.config().Projects["self"] = proj
	// shouldCaptureDiff must now report false for the project.
	if s.shouldCaptureDiff("self") {
		t.Fatalf("shouldCaptureDiff(self) should be false when capture_diff:false")
	}

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}
	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get(%s) not found", final.ID)
	}
	if got.DiffSummary != "" {
		t.Fatalf("capture_diff:false must skip diff, got DiffSummary=%q", got.DiffSummary)
	}
	if _, err := os.Stat(filepath.Join(got.ResultDir, "changes.diff")); !os.IsNotExist(err) {
		t.Fatalf("capture_diff:false must not write changes.diff (err=%v)", err)
	}
}

// TestShouldCaptureDiffDefaults checks the resolver: nil (unset) → true (defer to
// is-git probe); explicit true → true; unknown project → true.
func TestShouldCaptureDiffDefaults(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if !s.shouldCaptureDiff("self") {
		t.Fatalf("unset capture_diff should default to true (defer to is-git probe)")
	}
	if !s.shouldCaptureDiff("does-not-exist") {
		t.Fatalf("unknown project should default to true")
	}
	enabled := true
	proj := s.config().Projects["self"]
	proj.CaptureDiff = &enabled
	s.config().Projects["self"] = proj
	if !s.shouldCaptureDiff("self") {
		t.Fatalf("explicit capture_diff:true should be true")
	}
}

// TestCaptureBestEffortPanicSwallowed proves a panicking capture step does NOT
// change the job's terminal status (best-effort): the job still finishes done.
func TestCaptureBestEffortPanicSwallowed(t *testing.T) {
	prev := captureHook
	captureHook = func(*jobEntry, runner.Request) { panic("boom") }
	t.Cleanup(func() { captureHook = prev })

	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("a panicking capture must not flip the job: got %s (err=%s)", final.Status, final.Error)
	}
}

// TestRenderedCommandJSON exercises the pure serialiser for both agent shapes and
// asserts env stores KEY names only (no values; SR403/SR805).
func TestRenderedCommandJSON(t *testing.T) {
	// exec: command=go, args=[version], no env.
	exec := renderedCommandJSON(runner.Request{JobID: "j", Command: "go", Args: []string{"version"}})
	var e struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		EnvKeys []string `json:"env_keys"`
	}
	if err := json.Unmarshal([]byte(exec), &e); err != nil {
		t.Fatalf("exec render not valid JSON: %v", err)
	}
	if e.Command != "go" || len(e.Args) != 1 || e.Args[0] != "version" {
		t.Fatalf("unexpected exec render: %+v", e)
	}
	if len(e.EnvKeys) != 0 {
		t.Fatalf("exec render should have no env keys: %+v", e.EnvKeys)
	}

	// cli-agent: command=codex, args=[exec, "<prompt>"], env with secret value.
	cli := renderedCommandJSON(runner.Request{
		JobID:   "j2",
		Command: "codex",
		Args:    []string{"exec", "do the thing"},
		Env:     map[string]string{"OPENAI_API_KEY": "sk-secret", "FOO": "bar"},
	})
	// env VALUES must never appear in the audit JSON.
	if strings.Contains(cli, "sk-secret") || strings.Contains(cli, "\"bar\"") {
		t.Fatalf("rendered command leaked an env value: %q", cli)
	}
	var c struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		EnvKeys []string `json:"env_keys"`
	}
	if err := json.Unmarshal([]byte(cli), &c); err != nil {
		t.Fatalf("cli render not valid JSON: %v", err)
	}
	if c.Command != "codex" || len(c.Args) != 2 || c.Args[0] != "exec" {
		t.Fatalf("unexpected cli render: %+v", c)
	}
	// env_keys present, sorted, KEY names only.
	if len(c.EnvKeys) != 2 || c.EnvKeys[0] != "FOO" || c.EnvKeys[1] != "OPENAI_API_KEY" {
		t.Fatalf("env_keys should be sorted key names: %+v", c.EnvKeys)
	}

	// Empty command (remote runner) → "".
	if got := renderedCommandJSON(runner.Request{JobID: "j3"}); got != "" {
		t.Fatalf("empty command should render \"\", got %q", got)
	}
}

// TestReadResultJSON covers present/valid, missing, oversize and invalid cases.
func TestReadResultJSON(t *testing.T) {
	dir := t.TempDir()

	// Missing → "".
	if got := readResultJSON(dir); got != "" {
		t.Fatalf("missing result.json should read \"\", got %q", got)
	}
	// Empty result_dir → "".
	if got := readResultJSON(""); got != "" {
		t.Fatalf("empty result_dir should read \"\", got %q", got)
	}

	// Valid JSON → returned verbatim.
	valid := `{"ok":true,"items":[1,2,3]}`
	writeFile(t, filepath.Join(dir, "result.json"), valid)
	if got := readResultJSON(dir); got != valid {
		t.Fatalf("valid result.json mismatch: got %q want %q", got, valid)
	}

	// Invalid JSON → "".
	invDir := t.TempDir()
	writeFile(t, filepath.Join(invDir, "result.json"), "{not json")
	if got := readResultJSON(invDir); got != "" {
		t.Fatalf("invalid result.json should read \"\", got %q", got)
	}

	// Oversize (> maxResultJSONBytes) → "".
	bigDir := t.TempDir()
	big := "[\"" + strings.Repeat("a", maxResultJSONBytes+10) + "\"]"
	writeFile(t, filepath.Join(bigDir, "result.json"), big)
	if got := readResultJSON(bigDir); got != "" {
		t.Fatalf("oversize result.json should read \"\", got len=%d", len(got))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestScanArtifacts covers: recursive listing with relative (slash) names and
// correct sizes; missing/empty dir → nil.
func TestScanArtifacts(t *testing.T) {
	dir := t.TempDir()

	// No artifacts dir → nil.
	if got := scanArtifacts(dir); got != nil {
		t.Fatalf("missing artifacts dir should scan nil, got %+v", got)
	}
	// Empty result_dir → nil.
	if got := scanArtifacts(""); got != nil {
		t.Fatalf("empty result_dir should scan nil, got %+v", got)
	}

	// artifacts/a.txt + artifacts/sub/b.bin → 2 items.
	artDir := filepath.Join(dir, "artifacts")
	if err := os.MkdirAll(filepath.Join(artDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(artDir, "a.txt"), "hello")           // 5 bytes
	writeFile(t, filepath.Join(artDir, "sub", "b.bin"), "abcdefgh") // 8 bytes

	items := scanArtifacts(dir)
	if len(items) != 2 {
		t.Fatalf("expected 2 artifacts, got %d: %+v", len(items), items)
	}
	bySize := map[string]int64{}
	for _, it := range items {
		bySize[it.Name] = it.Size
		if it.Mtime == 0 {
			t.Fatalf("artifact %q has zero mtime", it.Name)
		}
	}
	if bySize["a.txt"] != 5 {
		t.Fatalf("a.txt size=%d, want 5 (items=%+v)", bySize["a.txt"], items)
	}
	// Relative name must use forward slash and include the subdir.
	if bySize["sub/b.bin"] != 8 {
		t.Fatalf("sub/b.bin missing or wrong size: %+v", items)
	}
}

// TestScanArtifactsEmptyDir asserts an existing-but-empty artifacts dir yields
// nil (no items), not a non-nil empty slice that would marshal into the column.
func TestScanArtifactsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := scanArtifacts(dir); len(got) != 0 {
		t.Fatalf("empty artifacts dir should yield no items, got %+v", got)
	}
}
