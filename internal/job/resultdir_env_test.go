package job

import (
	"strings"
	"testing"
)

// TestGoferJobEnvInjects proves goferJobEnv layers the three gofer-owned vars on
// top of the agent-config env without mutating the input map.
func TestGoferJobEnvInjects(t *testing.T) {
	base := map[string]string{"FOO": "bar"}
	env := goferJobEnv(base, "job-1", "/work/cwd", "/work/cwd/.exchange/gofer/job-1")

	if env["FOO"] != "bar" {
		t.Fatalf("agent-config env not preserved: %v", env)
	}
	if env["GOFER_JOB_ID"] != "job-1" {
		t.Fatalf("GOFER_JOB_ID = %q", env["GOFER_JOB_ID"])
	}
	if env["GOFER_CWD"] != "/work/cwd" {
		t.Fatalf("GOFER_CWD = %q", env["GOFER_CWD"])
	}
	if env["GOFER_RESULT_DIR"] != "/work/cwd/.exchange/gofer/job-1" {
		t.Fatalf("GOFER_RESULT_DIR = %q", env["GOFER_RESULT_DIR"])
	}
	// Input map must be untouched (returns a fresh map).
	if _, ok := base["GOFER_RESULT_DIR"]; ok {
		t.Fatalf("input base map was mutated: %v", base)
	}
}

// TestExecJobLocatesResultDirViaEnv is the end-to-end proof for gofer-udi:
// an exec job (argv executed verbatim, no {{result_dir}} templating) uses the
// injected $GOFER_RESULT_DIR to write result.json (E6) and an artifact (E1), and
// captureOutcomes picks both up. Before the env injection an exec agent had no way
// to learn its result_dir.
func TestExecJobLocatesResultDirViaEnv(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	script := `mkdir -p "$GOFER_RESULT_DIR/artifacts" && ` +
		`printf '{"ok":true}' > "$GOFER_RESULT_DIR/result.json" && ` +
		`printf 'hi' > "$GOFER_RESULT_DIR/artifacts/out.txt"`

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", script}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}

	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get(%s): not found", final.ID)
	}
	if got.ResultJSON != `{"ok":true}` {
		t.Fatalf("E6: result.json not captured via $GOFER_RESULT_DIR, got %q", got.ResultJSON)
	}
	if !strings.Contains(got.ArtifactsJSON, "out.txt") {
		t.Fatalf("E1: artifact not captured via $GOFER_RESULT_DIR, got %q", got.ArtifactsJSON)
	}
}
