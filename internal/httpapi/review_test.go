package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newReviewServer wires a server whose project runs one resumable cli-agent ("codex",
// the test binary: exit 0, resume template renders the prompt), so a reviewed job can
// be accepted/rejected and — with --resume — continued. sc carries the caller/
// governance/worker configuration under test.
func newReviewServer(t *testing.T, sc config.ServerConfig) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  sc,
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"codex"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Type: agent.TypeCLIAgent, Command: testcmd.Path(t),
				Args:          []string{"stderr-exit", "0", "", "{{prompt}}"},
				SessionResume: []string{"stderr-exit", "0", "resumed {{prompt}}"},
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	jobs := job.NewService(cfg, projects, agents, runners, st, nil)
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, sc.Token, sc.AllowEmptyToken, jobs, eng, projects, agents, nil, nil, nil, nil)
}

// waitReviewTok polls GET /v1/jobs/{id} until the job parks in needs_review. The shared
// waitDone/waitDoneTok helpers stop at a TERMINAL status, which a reviewed job is not
// (its review is still pending), so the review tests need their own waiter.
func waitReviewTok(t *testing.T, s *Server, id, token string) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp := do(t, s, http.MethodGet, "/v1/jobs/"+id, token, nil)
		var jr job.JobResult
		decode(t, resp, &jr)
		if jr.Status == job.StatusNeedsReview {
			return jr
		}
		if job.IsTerminal(jr.Status) {
			t.Fatalf("job %s reached %s, want needs_review", id, jr.Status)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %s never reached needs_review", id)
	return job.JobResult{}
}

// submitReviewJob submits a reviewed job (explicit session id, so reject --resume has
// something to continue) and waits for it to park.
func submitReviewJob(t *testing.T, s *Server, token string) job.JobResult {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", token, job.JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
		Review: true, SessionID: "sess-http",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if !created.RequireReview {
		t.Fatalf("created job = %+v, want require_review:true", created)
	}
	return waitReviewTok(t, s, created.ID, token)
}

// TestSubmitJobReviewFlag: `review: true` is bound from the body, echoed on the GET
// (a persisted property, not a request echo) and really gates the job — while a plain
// submit still finishes done.
func TestSubmitJobReviewFlag(t *testing.T) {
	s := newReviewServer(t, config.ServerConfig{Token: testToken})

	created := submitReviewJob(t, s, testToken)

	rr := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID, testToken, nil)
	var fetched job.JobResult
	decode(t, rr, &fetched)
	if fetched.Status != job.StatusNeedsReview || !fetched.RequireReview {
		t.Fatalf("GET /v1/jobs/{id} = %+v, want needs_review + require_review", fetched)
	}

	plain := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
	})
	var plainJob job.JobResult
	decode(t, plain, &plainJob)
	if plainJob.RequireReview {
		t.Fatalf("a submit without review = %+v, want require_review:false", plainJob)
	}
	if done := waitDone(t, s, plainJob.ID); done.Status != job.StatusDone {
		t.Fatalf("plain job = %s, want done", done.Status)
	}
}

// TestAcceptJobRequiresUserCaller: accept/reject are human-only — a WORKER token is
// refused with 403 ("an agent never accepts its own work"), a user token is accepted
// and the job moves to done with the reviewer recorded.
func TestAcceptJobRequiresUserCaller(t *testing.T) {
	s := newReviewServer(t, config.ServerConfig{
		Token: testToken,
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
		},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-worker"}},
	})
	created := submitReviewJob(t, s, "tok-alice")

	for _, action := range []string{"accept", "reject"} {
		resp := do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/"+action, "tok-worker",
			map[string]string{"note": "definitely fine"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("worker token %s status=%d, want 403", action, resp.StatusCode)
		}
		resp.Body.Close()
	}

	resp := do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/accept", "tok-alice",
		map[string]string{"note": "looks good"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept status=%d, want 200", resp.StatusCode)
	}
	var out job.ReviewOutcome
	decode(t, resp, &out)
	if out.Status != job.StatusDone || out.ReviewedBy != "alice" || out.ReviewNote != "looks good" {
		t.Fatalf("accept result = %+v, want done by alice", out)
	}

	// The decision is durable and terminal: the poll helper now stops.
	if done := waitDoneTok(t, s, created.ID, "tok-alice"); done.Status != job.StatusDone {
		t.Fatalf("job after accept = %s, want done", done.Status)
	}
}

// TestAcceptJobRequiresCanAnswerWhenGoverned: with governance.require_answer_capability
// on, only a caller holding can_answer may sign off a delivery (the same capability
// that lets a caller answer an interaction — both are "speak for the human").
func TestAcceptJobRequiresCanAnswerWhenGoverned(t *testing.T) {
	s := newReviewServer(t, config.ServerConfig{
		Token: testToken,
		Callers: []config.CallerConfig{
			{ID: "readonly", Token: "tok-readonly"},
			{ID: "operator", Token: "tok-operator", CanAnswer: true},
		},
		Governance: config.GovernanceConfig{RequireAnswerCapability: true},
	})
	created := submitReviewJob(t, s, "tok-readonly")

	resp := do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/accept", "tok-readonly", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("caller without can_answer status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/accept", "tok-operator", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator accept status=%d, want 200", resp.StatusCode)
	}
	var out job.ReviewOutcome
	decode(t, resp, &out)
	if out.Status != job.StatusDone || out.ReviewedBy != "operator" {
		t.Fatalf("accept result = %+v, want done by operator", out)
	}
}

// TestRejectJobResumeReturnsResumeJobID: POST /reject --resume refuses the delivery,
// starts the continuation with the note as its prompt, and reports the new job id.
func TestRejectJobResumeReturnsResumeJobID(t *testing.T) {
	s := newReviewServer(t, config.ServerConfig{Token: testToken})
	created := submitReviewJob(t, s, testToken)

	// A note is required (it is the reason AND the continuation prompt).
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/reject", testToken,
		map[string]any{"resume": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reject without a note status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/reject", testToken,
		map[string]any{"note": "fix the failing tests", "resume": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reject status=%d, want 200", resp.StatusCode)
	}
	var out job.ReviewOutcome
	decode(t, resp, &out)
	if out.Status != job.StatusRejected {
		t.Fatalf("reject result status=%s, want rejected", out.Status)
	}
	if out.ResumeJobID == "" {
		t.Fatalf("reject --resume must report the continuation: %+v", out)
	}

	rr := do(t, s, http.MethodGet, "/v1/jobs/"+out.ResumeJobID, testToken, nil)
	var cont job.JobResult
	decode(t, rr, &cont)
	if cont.ResumedFrom != created.ID {
		t.Fatalf("continuation %s resumed_from=%q, want %s", cont.ID, cont.ResumedFrom, created.ID)
	}
}

// TestReviewEndpointsRejectWrongState: accept/reject are only legal on a needs_review
// job, cancel refuses one (nothing left to cancel — reject is the verb), and an
// unknown id is a 404.
func TestReviewEndpointsRejectWrongState(t *testing.T) {
	s := newReviewServer(t, config.ServerConfig{Token: testToken})

	// A done job: not awaiting review.
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
	})
	var done job.JobResult
	decode(t, resp, &done)
	waitDone(t, s, done.ID)

	resp = do(t, s, http.MethodPost, "/v1/jobs/"+done.ID+"/accept", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("accept on a done job status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodPost, "/v1/jobs/nope/accept", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("accept on an unknown job status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	created := submitReviewJob(t, s, testToken)
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/cancel", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cancel of a needs_review job status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}
