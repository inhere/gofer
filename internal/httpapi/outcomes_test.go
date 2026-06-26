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

// TestGetJobIncludesSessionID: a <result_dir>/session_id file (option C fallback)
// is captured at终态 and surfaces in GET /v1/jobs/{id} as session_id (T1.5: the
// API serializes JobResult.SessionID directly, omitempty).
func TestGetJobIncludesSessionID(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created := createSleepJob(t, s)

	const sid = "deadbeef-1234-4abc-8def-aabbccddeeff"
	if err := os.WriteFile(filepath.Join(created.ResultDir, "session_id"), []byte(sid+"\n"), 0o600); err != nil {
		t.Fatalf("write session_id: %v", err)
	}

	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("job status=%s, want done (err=%s)", final.Status, final.Error)
	}
	if final.SessionID != sid {
		t.Fatalf("session_id mismatch over HTTP: got %q want %q", final.SessionID, sid)
	}
}

// TestListJobsEndpointSession: GET /v1/jobs?session=<id> maps the query param
// through to the session_id filter (P3). Two jobs carry distinct captured
// session_ids (via the option-C session_id file); the filter returns only the
// matching one, and omitting the param returns both (regression-safe).
func TestListJobsEndpointSession(t *testing.T) {
	s := newTestServer(t, testToken, false)

	jobA := createSleepJob(t, s)
	jobB := createSleepJob(t, s)
	const sidA = "11111111-1111-4111-8111-111111111111"
	const sidB = "22222222-2222-4222-8222-222222222222"
	if err := os.WriteFile(filepath.Join(jobA.ResultDir, "session_id"), []byte(sidA+"\n"), 0o600); err != nil {
		t.Fatalf("write session_id A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(jobB.ResultDir, "session_id"), []byte(sidB+"\n"), 0o600); err != nil {
		t.Fatalf("write session_id B: %v", err)
	}
	waitDone(t, s, jobA.ID)
	waitDone(t, s, jobB.ID)

	// No session param -> both jobs (regression: param is opt-in).
	all := listJobs(t, s, "")
	if len(all) != 2 {
		t.Fatalf("no-session list expected 2 jobs, got %d", len(all))
	}

	// ?session=sidA -> only jobA, echoing its session_id.
	bySession := listJobs(t, s, "?session="+sidA)
	if len(bySession) != 1 || bySession[0].ID != jobA.ID {
		t.Fatalf("session=sidA filter wrong: %+v", bySession)
	}
	if bySession[0].SessionID != sidA {
		t.Fatalf("expected session_id %q echoed, got %q", sidA, bySession[0].SessionID)
	}

	// ?session=<unknown> -> none (proves the param is actually applied).
	none := listJobs(t, s, "?session=99999999-9999-4999-8999-999999999999")
	if len(none) != 0 {
		t.Fatalf("unknown session expected 0, got %d", len(none))
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
