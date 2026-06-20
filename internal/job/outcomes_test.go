package job

import (
	"encoding/json"
	"os"
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
