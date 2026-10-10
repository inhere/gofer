package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// Hold-for-approval HTTP surface (gofer-9b1b): POST /v1/jobs with hold:true answers
// 202 with the approve_url, /approve is a person's (job credential and worker token
// 403), and /reject of a held job needs no reason.

const holdWebBase = "https://gofer.example"

func newHoldHTTPServer(t *testing.T) *Server {
	t.Helper()
	return newCredentialServer(t, config.ServerConfig{
		Callers:    []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
		Workers:    map[string]config.WorkerAuthConfig{"w1": {Token: "tok-worker"}},
		WebBaseURL: holdWebBase,
	}, map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)
}

// submitHeld posts a held exec job and returns the decoded submit body.
func submitHeld(t *testing.T, s *Server, token string) submitResponse {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", token, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		Hold: true, HoldReason: "push the release branch",
	})
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("X-Gofer-Async") != "1" {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("held submit = %d %s, want 202 + X-Gofer-Async", resp.StatusCode, b)
	}
	var out submitResponse
	decode(t, resp, &out)
	if out.Status != job.StatusAwaitingApproval || out.Hold == nil || out.Hold.Reason != "push the release branch" {
		t.Fatalf("held submit body = %+v", out)
	}
	return out
}

// TestApproveHeldJobCallers is the acceptance of the approve permission: a job
// credential gets the SEC-01 403, a worker token the human-only 403, a user token
// approves it and the job runs to done under the same id.
func TestApproveHeldJobCallers(t *testing.T) {
	t.Parallel()
	s := newHoldHTTPServer(t)
	member := submitExecJob(t, s, "tok-alice")
	jobTok := seedJobToken(t, s, member.ID, jobstore.JobCredentialMember, "")

	held := submitHeld(t, s, "tok-alice")
	if want := holdWebBase + "/jobs/" + held.ID; held.ApproveURL != want {
		t.Fatalf("approve_url = %q, want %q", held.ApproveURL, want)
	}

	resp := do(t, s, http.MethodPost, "/v1/jobs/"+held.ID+"/approve", jobTok, map[string]string{"note": "self"})
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "approve a held job") {
		t.Fatalf("job credential approve = %d %s, want 403 naming the action", resp.StatusCode, b)
	}
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+held.ID+"/approve", "tok-worker", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("worker approve = %d, want 403", resp.StatusCode)
	}
	if got, _ := s.jobs.Get(held.ID); got.Status != job.StatusAwaitingApproval {
		t.Fatalf("a refused approval moved the job to %s", got.Status)
	}

	resp = do(t, s, http.MethodPost, "/v1/jobs/"+held.ID+"/approve", "tok-alice", map[string]string{"note": "go ahead"})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("user approve = %d %s, want 200", resp.StatusCode, b)
	}
	var approved job.JobResult
	decode(t, resp, &approved)
	if approved.ID != held.ID {
		t.Fatalf("approved id = %s, want %s", approved.ID, held.ID)
	}
	final := waitDoneTok(t, s, held.ID, "tok-alice")
	if final.Status != job.StatusDone || final.Hold == nil || final.Hold.DecidedBy != "alice" || final.Hold.Note != "go ahead" {
		t.Fatalf("approved job = %s hold=%+v", final.Status, final.Hold)
	}

	// Approving it again: the job is no longer awaiting approval.
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+held.ID+"/approve", "tok-alice", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second approve = %d, want 409", resp.StatusCode)
	}
	resp = do(t, s, http.MethodPost, "/v1/jobs/no-such-job/approve", "tok-alice", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("approve unknown = %d, want 404", resp.StatusCode)
	}
}

// TestRejectHeldJob: /reject of a held job dispatches on its state — no reason
// needed, `reason` is a synonym of `note`, resume is a 400, and the job ends
// cancelled without running.
func TestRejectHeldJob(t *testing.T) {
	t.Parallel()
	s := newHoldHTTPServer(t)

	bare := submitHeld(t, s, "tok-alice")
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+bare.ID+"/reject", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("reason-less reject of a held job = %d %s, want 200", resp.StatusCode, b)
	}
	var out job.ReviewOutcome
	decode(t, resp, &out)
	if out.Status != job.StatusCancelled || out.Error != "hold rejected by alice" {
		t.Fatalf("reject outcome = %s %q", out.Status, out.Error)
	}

	reasoned := submitHeld(t, s, "tok-alice")
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+reasoned.ID+"/reject", "tok-alice", map[string]any{"reason": "wrong branch", "resume": true})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reject --resume of a held job = %d, want 400", resp.StatusCode)
	}
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+reasoned.ID+"/reject", "tok-alice", map[string]string{"reason": "wrong branch"})
	decode(t, resp, &out)
	if out.Status != job.StatusCancelled || !strings.Contains(out.Error, "wrong branch") {
		t.Fatalf("reject with reason = %s %q", out.Status, out.Error)
	}

	resp = do(t, s, http.MethodPost, "/v1/jobs/"+reasoned.ID+"/approve", "tok-alice", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("approve after reject = %d, want 409", resp.StatusCode)
	}
}
