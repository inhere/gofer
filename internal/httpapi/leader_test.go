package httpapi

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
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

// TestLeaderCannotAccept (MCP-05 阶段 B, re-pinned on SEC-01 credentials): the leader
// job's identity may comment, move todos, wake and ask — but never ACCEPT or REJECT a
// delivery (GATE-01 §3: an agent never signs off, and a leader is an agent). Since
// SEC-01 the identity is the leader job's own CREDENTIAL (the retired `as_job` field
// decides nothing), which is the only way the server can tell a leader job from the
// human sitting next to it.
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
	// The credential is seeded for the leader job AFTER it has run: a live job's own
	// terminal path revokes the row its id holds (SEC-01), so a seeded credential has to
	// outlive the run — the same order the member tests use (submit, wait, then seed).
	waitJobTerminal(t, s.jobs, leader.ID, 15*time.Second)
	leaderTok := seedJobToken(t, s, leader.ID, jobstore.JobCredentialLeader, "plan-1")

	delivery := submitReviewJob(t, s, testToken)

	// 1. accept, spoken by the leader's credential: refused, and the job stays where it
	// was. The route is not on the SEC-01 allowlist at all, so the middleware answers
	// before the handler — the 403 body names the action and the credential kind.
	rej := do(t, s, http.MethodPost, "/v1/jobs/"+delivery.ID+"/accept", leaderTok,
		map[string]any{"note": "looks good"})
	if rej.StatusCode != http.StatusForbidden {
		t.Fatalf("leader accept status=%d, want 403 (body: %s)", rej.StatusCode, bodyString(t, rej))
	}
	assertJobCredentialRefusal(t, rej)
	got := do(t, s, http.MethodGet, "/v1/jobs/"+delivery.ID, testToken, nil)
	var after job.JobResult
	decode(t, got, &after)
	if after.Status != job.StatusNeedsReview {
		t.Fatalf("job status = %s after a refused leader accept, want needs_review", after.Status)
	}

	// 2. reject, spoken by the leader's credential: refused too (a leader must not hand
	// work back on its own either — that is the human's call).
	rej = do(t, s, http.MethodPost, "/v1/jobs/"+delivery.ID+"/reject", leaderTok,
		map[string]any{"note": "not good enough"})
	if rej.StatusCode != http.StatusForbidden {
		t.Fatalf("leader reject status=%d, want 403 (body: %s)", rej.StatusCode, bodyString(t, rej))
	}
	assertJobCredentialRefusal(t, rej)
	// Even a body that still names a job cannot hand the leader a verdict: `as_job` is
	// ignored for a user caller (SEC-01), so this stays the human's accept.
	ok := do(t, s, http.MethodPost, "/v1/jobs/"+delivery.ID+"/accept", testToken,
		map[string]any{"note": "looks good", "as_job": leader.ID})
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("human accept status=%d, want 200 (body: %s)", ok.StatusCode, bodyString(t, ok))
	}
}
