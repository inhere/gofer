package httpapi

import (
	"io"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// bodyString reads a response body for a failure message (the error shape is what says
// WHY a request was refused, so a status-only assertion hides the regression).
func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// TestLeaderCannotAccept (MCP-05 阶段 B): the leader job's identity may comment, move
// todos, wake and ask — but never ACCEPT or REJECT a delivery (GATE-01 §3: an agent
// never signs off, and a leader is an agent). The in-job identity arrives as the
// review body's as_job (exactly like a comment's), so the server can tell a leader job
// from the human it is running beside — a caller token alone cannot.
func TestLeaderCannotAccept(t *testing.T) {
	s := newReviewServer(t, config.ServerConfig{Token: testToken})

	// The leader job: the leader round submits it IN-PROCESS with the server-set plan
	// marker. A submit over the wire cannot produce one — the field is `json:"-"`
	// precisely so no caller can promote itself to leader (asserted below).
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, map[string]any{
		"project_key": "self", "agent": "codex", "runner": "local", "cwd": ".",
		"prompt": "not a leader", "timeout_sec": 30, "plan_id": "plan-1",
		"leader_of_plan": "plan-1", // not a field of the request: ignored by the binder
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status=%d, want 200", resp.StatusCode)
	}
	var wireJob job.JobResult
	decode(t, resp, &wireJob)
	if s.jobs.IsLeaderJob(wireJob.ID) {
		t.Fatalf("a job submitted over the wire carries the leader marker: %s", wireJob.ID)
	}

	leader, err := s.jobs.Submit(job.JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local", Cwd: ".",
		Prompt: "leader turn", TimeoutSec: 30,
		PlanID: "plan-1", LeaderOfPlan: "plan-1",
	})
	if err != nil {
		t.Fatalf("submit leader job: %v", err)
	}
	if !s.jobs.IsLeaderJob(leader.ID) {
		t.Fatalf("job %s is not recognised as a leader job", leader.ID)
	}

	delivery := submitReviewJob(t, s, testToken)

	// 1. accept, spoken by the leader: refused, and the job stays where it was.
	rej := do(t, s, http.MethodPost, "/v1/jobs/"+delivery.ID+"/accept", testToken,
		map[string]any{"note": "looks good", "as_job": leader.ID})
	if rej.StatusCode != http.StatusForbidden {
		t.Fatalf("leader accept status=%d, want 403 (body: %s)", rej.StatusCode, bodyString(t, rej))
	}
	got := do(t, s, http.MethodGet, "/v1/jobs/"+delivery.ID, testToken, nil)
	var after job.JobResult
	decode(t, got, &after)
	if after.Status != job.StatusNeedsReview {
		t.Fatalf("job status = %s after a refused leader accept, want needs_review", after.Status)
	}

	// 2. reject, spoken by the leader: refused too (a leader must not hand work back on
	// its own either — that is the human's call).
	rej = do(t, s, http.MethodPost, "/v1/jobs/"+delivery.ID+"/reject", testToken,
		map[string]any{"note": "not good enough", "as_job": leader.ID})
	if rej.StatusCode != http.StatusForbidden {
		t.Fatalf("leader reject status=%d, want 403 (body: %s)", rej.StatusCode, bodyString(t, rej))
	}

	// 3. the same request from the HUMAN (no as_job) is the normal path: the gate is
	// the identity, not the route.
	ok := do(t, s, http.MethodPost, "/v1/jobs/"+delivery.ID+"/accept", testToken, map[string]any{"note": "looks good"})
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("human accept status=%d, want 200 (body: %s)", ok.StatusCode, bodyString(t, ok))
	}
}
