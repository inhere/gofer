package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// writeDiff drops a changes.diff into a job's result dir (simulating an E12
// capture) and returns the path.
func writeDiff(t *testing.T, s *Server, id, content string) string {
	t.Helper()
	res, ok := s.jobs.Get(id)
	if !ok {
		t.Fatalf("job %s not found", id)
	}
	p := filepath.Join(res.ResultDir, "changes.diff")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write changes.diff: %v", err)
	}
	return p
}

// TestGetDiffSummary asserts the default endpoint returns 200 + a "summary" field.
// The job's cwd is a fresh (non-git) temp dir so no diff is captured → "".
func TestGetDiffSummary(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)
	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/diff", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff summary status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Summary string `json:"summary"`
	}
	decode(t, resp, &body)
	if body.Summary != "" {
		t.Fatalf("non-git job should have empty diff summary, got %q", body.Summary)
	}
}

// TestGetDiffFull asserts ?full=1 streams the changes.diff content verbatim.
func TestGetDiffFull(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)
	const diff = "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n"
	writeDiff(t, s, id, diff)

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/diff?full=1", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("full diff status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != diff {
		t.Fatalf("full diff body=%q, want %q", body, diff)
	}
}

// TestGetDiffFullMissing asserts ?full=1 is a 404 when the job captured no diff.
func TestGetDiffFullMissing(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/diff?full=1", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing full diff status=%d, want 404", resp.StatusCode)
	}
}

// TestGetDiffUnknownJob asserts an unknown id is 404 (both shapes).
func TestGetDiffUnknownJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/jobs/ghost/diff", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job diff status=%d, want 404", resp.StatusCode)
	}
	resp2 := do(t, s, http.MethodGet, "/v1/jobs/ghost/diff?full=1", testToken, nil)
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job full diff status=%d, want 404", resp2.StatusCode)
	}
}

// TestGetDiffSummaryShape pins the JSON shape: a job with no diff returns
// {"summary":""} (not null / not an error).
func TestGetDiffSummaryShape(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)
	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/diff", testToken, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("diff summary not an object: %v (%s)", err, raw)
	}
	if _, ok := m["summary"]; !ok {
		t.Fatalf("diff summary response missing 'summary' key: %s", raw)
	}
}
