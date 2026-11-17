package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// runFailedJob submits a portable failing command (testcmd exits non-zero) and waits for
// its terminal row, so the diagnosis has a real failure to explain.
func runFailedJob(t *testing.T, s *Server) string {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "stderr-exit", "3", "boom"), Cwd: ".", TimeoutSec: 30,
	})
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatalf("create returned no id")
	}
	waitDone(t, s, created.ID)
	return created.ID
}

// TestGetJobDiagnosisRequiresAuth asserts the diagnosis endpoint is not public: no token
// is a 401, exactly like GET /v1/jobs/{id}.
func TestGetJobDiagnosisRequiresAuth(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	id := runFailedJob(t, s)
	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/diagnosis", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}
}

// TestGetJobDiagnosisReportsFailure asserts the endpoint names the derived reason, fills
// the timeline from the recorded stamps and always answers with next steps — plus that an
// unknown id is a 404.
func TestGetJobDiagnosisReportsFailure(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	id := runFailedJob(t, s)

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/diagnosis", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var d job.Diagnosis
	decode(t, resp, &d)
	if d.JobID != id || d.Status != job.StatusFailed {
		t.Fatalf("job=%q status=%q", d.JobID, d.Status)
	}
	if !d.Finished || d.Reason != job.TerminationAgentError {
		t.Fatalf("finished=%v reason=%q, want finished agent_error", d.Finished, d.Reason)
	}
	if d.ReasonLabel == "" || d.ReasonLabel == d.Reason {
		t.Fatalf("reason label = %q, want a localized label", d.ReasonLabel)
	}
	if len(d.Timeline) != 5 {
		t.Fatalf("timeline has %d stages, want 5: %+v", len(d.Timeline), d.Timeline)
	}
	if !d.Timeline[1].Present {
		t.Fatalf("started stage is not present: %+v", d.Timeline[1])
	}
	if len(d.NextSteps) == 0 {
		t.Fatalf("no next steps")
	}

	missing := do(t, s, http.MethodGet, "/v1/jobs/no-such-job/diagnosis", testToken, nil)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job status=%d, want 404", missing.StatusCode)
	}
}
