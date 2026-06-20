package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// createSleepJob submits an exec job that sleeps briefly (so the test can drop a
// result.json into its result dir before captureOutcomes runs at finish) and
// returns the created snapshot (with the assigned id + result_dir).
func createSleepJob(t *testing.T, s *Server) job.JobResult {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "0.4"}, Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" || created.ResultDir == "" {
		t.Fatalf("created job missing id/result_dir: %+v", created)
	}
	return created
}

// TestGetJobIncludesResultJSON: a valid <result_dir>/result.json surfaces in the
// GET /v1/jobs/{id} response as result_json (E6).
func TestGetJobIncludesResultJSON(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created := createSleepJob(t, s)

	valid := `{"ok":true,"summary":"3 failed cases"}`
	if err := os.WriteFile(filepath.Join(created.ResultDir, "result.json"), []byte(valid), 0o600); err != nil {
		t.Fatalf("write result.json: %v", err)
	}

	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("job status=%s, want done (err=%s)", final.Status, final.Error)
	}
	if final.ResultJSON != valid {
		t.Fatalf("result_json mismatch: got %q want %q", final.ResultJSON, valid)
	}
	// The rendered command rides along too (E15).
	if final.RenderedCommand == "" {
		t.Fatalf("rendered_command should be populated for a local exec job")
	}
}

// TestGetJobSkipsOversizeResultJSON: an oversize result.json is left on disk but
// NOT inlined (and the request still succeeds — best-effort).
func TestGetJobSkipsOversizeResultJSON(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created := createSleepJob(t, s)

	big := "[\"" + strings.Repeat("a", maxResultJSONOverCap) + "\"]"
	if err := os.WriteFile(filepath.Join(created.ResultDir, "result.json"), []byte(big), 0o600); err != nil {
		t.Fatalf("write result.json: %v", err)
	}

	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("job status=%s, want done", final.Status)
	}
	if final.ResultJSON != "" {
		t.Fatalf("oversize result.json must not be inlined, got len=%d", len(final.ResultJSON))
	}
}

// TestGetJobSkipsInvalidResultJSON: a malformed result.json is ignored (not
// inlined) and the job still completes.
func TestGetJobSkipsInvalidResultJSON(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created := createSleepJob(t, s)

	if err := os.WriteFile(filepath.Join(created.ResultDir, "result.json"), []byte("{not valid"), 0o600); err != nil {
		t.Fatalf("write result.json: %v", err)
	}

	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("job status=%s, want done", final.Status)
	}
	if final.ResultJSON != "" {
		t.Fatalf("invalid result.json must not be inlined, got %q", final.ResultJSON)
	}
}

// maxResultJSONOverCap is one over the 256KB cap enforced in internal/job
// (kept local so this package does not export the internal const).
const maxResultJSONOverCap = 256*1024 + 10
