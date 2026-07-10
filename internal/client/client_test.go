package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

const testToken = "dev-token"

// openTestStore opens a metadata store under root (cleaned up automatically) for
// wiring a job.Service in tests.
func openTestStore(t *testing.T, root string) *jobstore.Store {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newServer wires a real in-memory httpapi server with a single "self" project
// that allows the exec agent + local runner + raw exec, and returns an httptest
// server fronting it plus its temp storage root.
func newServer(t *testing.T, token string, allowEmpty bool) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: token, AllowEmptyToken: allowEmpty},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	jobsEng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(jobsEng)
	srv := httpapi.New(&cfg.Server, token, allowEmpty, jobs, jobsEng, projects, agents, nil, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// waitDone polls the client until the job reaches a terminal state.
func waitDone(t *testing.T, c *Client, id string) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := c.GetJob(id)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		switch res.Status {
		case job.StatusDone, job.StatusFailed, job.StatusCancelled, job.StatusTimeout:
			return res
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach terminal state", id)
	return job.JobResult{}
}

func TestSubmitGetLogs(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	created, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("no job id: %+v", created)
	}

	final := waitDone(t, c, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("status=%s want done (err=%s)", final.Status, final.Error)
	}
	if final.ExitCode != 0 {
		t.Fatalf("exit_code=%d want 0", final.ExitCode)
	}

	out, err := c.GetLogs(created.ID, "stdout")
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if !strings.Contains(out, "go version") {
		t.Fatalf("stdout log missing output: %q", out)
	}
}

func TestCancelCompletedStable(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	created, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	final := waitDone(t, c, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("setup status=%s want done", final.Status)
	}

	after, err := c.CancelJob(created.ID)
	if err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if after.Status != job.StatusDone {
		t.Fatalf("cancel of completed job changed status to %s", after.Status)
	}
}

func TestCancelUnknownJobError(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	_, err := c.CancelJob("nope")
	if err == nil {
		t.Fatal("expected error cancelling unknown job")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404 status: %v", err)
	}
}

// TestResumeUnknownJobError exercises the client's ResumeJob round-trip and
// error decoding: resuming an unknown id surfaces the server's 404 as an error.
func TestResumeUnknownJobError(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	_, err := c.ResumeJob("nope", "hi", "")
	if err == nil {
		t.Fatal("expected error resuming unknown job")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404 status: %v", err)
	}
}

// TestResumeNoSessionError: resuming an exec job (no captured session) surfaces
// the server's 400 — proving the client posts the body and decodes the error.
func TestResumeNoSessionError(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	created, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	waitDone(t, c, created.ID)

	_, err = c.ResumeJob(created.ID, "again", "")
	if err == nil {
		t.Fatal("expected error resuming a job with no session")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error should mention 400 status: %v", err)
	}
}

func TestAuthMissingTokenRejected(t *testing.T) {
	ts := newServer(t, testToken, false) // server requires a token
	c := New(ts.URL, "")                 // client sends none

	_, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".",
	})
	if err == nil {
		t.Fatal("expected 401 error without token")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should mention 401: %v", err)
	}
}

func TestAuthCorrectTokenSucceeds(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	_, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".",
	})
	if err != nil {
		t.Fatalf("SubmitJob with token: %v", err)
	}
}

func TestSubmitUnknownProjectFriendlyError(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	_, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "ghost", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".",
	})
	if err == nil {
		t.Fatal("expected error for unknown project")
	}
	// Friendly error must carry the server's error summary.
	if !strings.Contains(err.Error(), "404") || !strings.Contains(strings.ToLower(err.Error()), "project") {
		t.Fatalf("error not friendly enough: %v", err)
	}
}

func TestGetLogsInvalidStream(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)
	if _, err := c.GetLogs("x", "nope"); err == nil {
		t.Fatal("expected error for invalid stream")
	}
}

// TestSubmitJobSyncReturnsTerminal: a fast sync submit returns the terminal
// result and Async=false (the server finished within its wait cap).
func TestSubmitJobSyncReturnsTerminal(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	out, err := c.SubmitJobSync(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		Sync: true,
	})
	if err != nil {
		t.Fatalf("SubmitJobSync: %v", err)
	}
	if out.Async {
		t.Fatalf("Async=true on a completed sync submit, want false")
	}
	if out.Job.Status != job.StatusDone {
		t.Fatalf("status=%s want done", out.Job.Status)
	}
}

// TestSubmitJobSyncAsyncFallback: a slow sync submit that exceeds the (clamped)
// wait cap is reported as Async=true so the caller switches to polling.
func TestSubmitJobSyncAsyncFallback(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	out, err := c.SubmitJobSync(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 30,
		Sync: true, WaitTimeoutSec: 1,
	})
	if err != nil {
		t.Fatalf("SubmitJobSync: %v", err)
	}
	t.Cleanup(func() {
		if out.Job.ID != "" {
			_, _ = c.CancelJob(out.Job.ID)
			waitDone(t, c, out.Job.ID)
		}
	})
	if !out.Async {
		t.Fatalf("Async=false on a sync submit that exceeded the wait cap, want true")
	}
	if out.Job.ID == "" {
		t.Fatalf("async fallback missing job id: %+v", out.Job)
	}
}

// TestSubmitMarkdownExecRejected: SubmitMarkdown posts text/markdown; an md
// submit declaring agent=exec is rejected by the server (400) and surfaced as a
// friendly client error.
func TestSubmitMarkdownExecRejected(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	md := []byte("---\nproject_key: self\nagent: exec\nrunner: local\n---\ndo a thing\n")
	if _, err := c.SubmitMarkdown(md); err == nil {
		t.Fatal("expected error for exec via markdown")
	} else if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error should mention 400: %v", err)
	}
}

// waitWorkflowDone polls GetWorkflow until the workflow reaches a terminal state.
func waitWorkflowDone(t *testing.T, c *Client, id string) Workflow {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		wf, err := c.GetWorkflow(id)
		if err != nil {
			t.Fatalf("GetWorkflow: %v", err)
		}
		switch wf.Status {
		case "done", "failed", "cancelled":
			return wf
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("workflow %s did not reach terminal state", id)
	return Workflow{}
}

// TestSubmitGetWorkflow round-trips a two-step exec workflow: SubmitWorkflow
// returns the running header (step 1 started), GetWorkflow inlines the step
// chain, and ListWorkflows surfaces it. The chain reaches done with both steps
// done.
func TestSubmitGetWorkflow(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	spec := workflow.Spec{
		Title: "chain",
		Steps: []workflow.StepSpec{
			{Name: "one", ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30},
			{Name: "two", ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "env", "GOOS"}, Cwd: ".", TimeoutSec: 30},
		},
	}
	wf, err := c.SubmitWorkflow(spec)
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	if wf.ID == "" {
		t.Fatalf("no workflow id: %+v", wf)
	}
	if wf.Status != "running" {
		t.Fatalf("status=%s want running", wf.Status)
	}
	if wf.TotalSteps != 2 {
		t.Fatalf("total_steps=%d want 2", wf.TotalSteps)
	}

	final := waitWorkflowDone(t, c, wf.ID)
	if final.Status != "done" {
		t.Fatalf("workflow status=%s want done (err=%s)", final.Status, final.Error)
	}
	if len(final.Steps) != 2 {
		t.Fatalf("got %d steps want 2: %+v", len(final.Steps), final.Steps)
	}
	for _, st := range final.Steps {
		if st.Status != "done" {
			t.Fatalf("step %d status=%s want done", st.StepIndex, st.Status)
		}
		if st.JobID == "" {
			t.Fatalf("step %d missing job id", st.StepIndex)
		}
	}
	if final.Steps[0].Name != "one" || final.Steps[1].Name != "two" {
		t.Fatalf("step names lost: %+v", final.Steps)
	}

	list, err := c.ListWorkflows("")
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	found := false
	for _, w := range list {
		if w.ID == wf.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("submitted workflow %s missing from list", wf.ID)
	}
}

func TestPlanClientRoundTrip(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	p, err := c.CreatePlan("plan-client", "client plan", "desc")
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if p.PlanID != "plan-client" || p.Status != "open" || p.Owner != "default" {
		t.Fatalf("created plan mismatch: %+v", p)
	}

	jobOut, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		PlanID: "plan-direct", Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob with PlanID: %v", err)
	}
	final := waitDone(t, c, jobOut.ID)
	if final.PlanID != "plan-direct" {
		t.Fatalf("final PlanID = %q, want plan-direct", final.PlanID)
	}

	filtered, err := c.ListJobs(job.ListOpts{Plan: "plan-direct"})
	if err != nil {
		t.Fatalf("ListJobs(plan): %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != jobOut.ID || filtered[0].PlanID != "plan-direct" {
		t.Fatalf("filtered jobs mismatch: %+v", filtered)
	}

	if _, err := c.AttachJob("plan-client", jobOut.ID); err != nil {
		t.Fatalf("AttachJob: %v", err)
	}
	detail, err := c.GetPlan("plan-client")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if detail.PlanID != "plan-client" || len(detail.Jobs) != 1 || detail.Jobs[0].ID != jobOut.ID {
		t.Fatalf("plan detail mismatch: %+v", detail)
	}

	plans, err := c.ListPlans("open")
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	var found bool
	for _, plan := range plans {
		if plan.PlanID == "plan-client" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan-client missing from ListPlans: %+v", plans)
	}
}

// TestExportWorkflowRoundTrip: a submitted workflow exports back to a runnable spec
// (T4.1) with secrets stripped, and the export re-imports (SubmitWorkflow) cleanly.
func TestExportWorkflowRoundTrip(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	spec := workflow.Spec{
		Title: "export-me",
		Steps: []workflow.StepSpec{
			{Name: "leaky", ProjectKey: "self", Agent: "exec", Runner: "local",
				Cmd: []string{"sh", "-c", "deploy --token sk-secret-99 && go version"}, Cwd: ".", TimeoutSec: 30},
		},
	}
	wf, err := c.SubmitWorkflow(spec)
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	_ = waitWorkflowDone(t, c, wf.ID)

	exported, redacted, err := c.ExportWorkflow(wf.ID)
	if err != nil {
		t.Fatalf("ExportWorkflow: %v", err)
	}
	if !redacted {
		t.Fatal("export carrying a --token should report redacted=true")
	}
	if exported.Title != "export-me" || len(exported.Steps) != 1 {
		t.Fatalf("export shape wrong: %+v", exported)
	}
	joined := strings.Join(exported.Steps[0].Cmd, " ")
	if strings.Contains(joined, "sk-secret-99") {
		t.Fatalf("secret leaked into export: %q", joined)
	}
	// The redacted export is still re-importable (the structure is intact).
	wf2, err := c.SubmitWorkflow(exported)
	if err != nil {
		t.Fatalf("re-import exported spec: %v", err)
	}
	if wf2.ID == wf.ID {
		t.Fatal("re-import should create a new workflow id")
	}
	_ = waitWorkflowDone(t, c, wf2.ID)
}

// TestExportUnknownWorkflow: exporting an unknown id is a 404.
func TestExportUnknownWorkflow(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)
	if _, _, err := c.ExportWorkflow("wf-nope"); err == nil {
		t.Fatal("expected a 404 error for an unknown workflow export")
	} else if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404: %v", err)
	}
}

// TestListWorkflowEvents: a completed workflow exposes its lifecycle timeline via the
// events API (T4.3), including the submitted + terminal frames, in seq order.
func TestListWorkflowEvents(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	wf, err := c.SubmitWorkflow(workflow.Spec{
		Title: "evented",
		Steps: []workflow.StepSpec{
			{Name: "a", ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30},
		},
	})
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	_ = waitWorkflowDone(t, c, wf.ID)

	events, err := c.ListWorkflowEvents(wf.ID, 0)
	if err != nil {
		t.Fatalf("ListWorkflowEvents: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected >= 2 events (submitted + terminal), got %d: %+v", len(events), events)
	}
	var sawSubmitted, sawTerminal bool
	var lastSeq int64
	for _, ev := range events {
		if ev.Seq <= lastSeq && lastSeq != 0 {
			t.Fatalf("events not in ascending seq order: %d after %d", ev.Seq, lastSeq)
		}
		lastSeq = ev.Seq
		switch ev.Type {
		case "workflow.submitted":
			sawSubmitted = true
		case "workflow.terminal":
			sawTerminal = true
		}
	}
	if !sawSubmitted || !sawTerminal {
		t.Fatalf("missing lifecycle frames: submitted=%v terminal=%v", sawSubmitted, sawTerminal)
	}

	// ?since cursor: events strictly after the last seq is empty.
	rest, err := c.ListWorkflowEvents(wf.ID, lastSeq)
	if err != nil {
		t.Fatalf("ListWorkflowEvents since: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("since=last should be empty, got %d", len(rest))
	}
}

// TestSubmitWorkflowNoStepsRejected: an empty workflow is rejected by the server
// (400) and surfaced as a friendly client error.
func TestSubmitWorkflowNoStepsRejected(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	if _, err := c.SubmitWorkflow(workflow.Spec{Title: "empty"}); err == nil {
		t.Fatal("expected error for a workflow with no steps")
	} else if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error should mention 400: %v", err)
	}
}

// TestCancelUnknownWorkflowError: cancelling an unknown workflow is a 404.
func TestCancelUnknownWorkflowError(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	if _, err := c.CancelWorkflow("nope"); err == nil {
		t.Fatal("expected error cancelling unknown workflow")
	} else if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404: %v", err)
	}
}

// TestListAgents: ListAgents decodes the /v1/agents wire shape (httpapi.agentView:
// key/type/available/version/error) and folds it into the mcp bridge contract
// (name/type/available/detail), with detail=version when available else the probe
// error — mirroring the in-process mcpserver list-agents handler (E28 P1).
func TestListAgents(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agents": []map[string]any{
				{"key": "claude", "type": "cli", "available": true, "version": "1.2.3"},
				{"key": "exec", "type": "exec", "available": false, "error": "not found"},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	agents, err := New(ts.URL, "").ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents want 2: %+v", len(agents), agents)
	}
	// Available agent: name=key, detail=version.
	if agents[0].Name != "claude" || agents[0].Type != "cli" || !agents[0].Available || agents[0].Detail != "1.2.3" {
		t.Fatalf("agent[0] mismatch: %+v", agents[0])
	}
	// Unavailable agent: detail folds the probe error.
	if agents[1].Name != "exec" || agents[1].Available || agents[1].Detail != "not found" {
		t.Fatalf("agent[1] mismatch: %+v", agents[1])
	}
}

// TestGetInteractions: GetInteractions unwraps {"interactions":[...]} into a
// []job.Interaction with all fields decoded (E28 P1).
func TestGetInteractions(t *testing.T) {
	want := []job.Interaction{
		{ID: "int-1", JobID: "job-1", Type: "question", Prompt: "ok?", Status: "pending", CreatedAt: 100},
		{ID: "int-2", JobID: "job-1", Type: "confirmation", Prompt: "go?", Status: "answered", Answer: "yes", CreatedAt: 101, AnsweredAt: 102},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/job-1/interactions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"interactions": want})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	got, err := New(ts.URL, "").GetInteractions("job-1")
	if err != nil {
		t.Fatalf("GetInteractions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d interactions want 2: %+v", len(got), got)
	}
	if got[0].ID != "int-1" || got[0].Status != "pending" || got[0].Prompt != "ok?" {
		t.Fatalf("interaction[0] mismatch: %+v", got[0])
	}
	if got[1].ID != "int-2" || got[1].Answer != "yes" || got[1].AnsweredAt != 102 {
		t.Fatalf("interaction[1] mismatch: %+v", got[1])
	}
}

// TestGetInteractionsUnknownJobError: an unknown job id surfaces the server's
// 404 as a friendly client error.
func TestGetInteractionsUnknownJobError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/nope/interactions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown job", "detail": "no job with id nope"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if _, err := New(ts.URL, "").GetInteractions("nope"); err == nil {
		t.Fatal("expected 404 error for unknown job interactions")
	} else if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404: %v", err)
	}
}

// TestAnswerInteractionReturnsUpdated: AnswerInteraction POSTs the answer and
// decodes the bare job.Interaction the endpoint echoes back (E28 P1 enhancement).
func TestAnswerInteractionReturnsUpdated(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/job-1/interactions/int-1/answer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Answer string `json:"answer"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(job.Interaction{
			ID: "int-1", JobID: "job-1", Type: "question", Prompt: "ok?",
			Status: "answered", Answer: body.Answer, CreatedAt: 100, AnsweredAt: 200,
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	it, err := New(ts.URL, "").AnswerInteraction("job-1", "int-1", "yes", "")
	if err != nil {
		t.Fatalf("AnswerInteraction: %v", err)
	}
	if it.ID != "int-1" || it.Status != "answered" || it.Answer != "yes" || it.AnsweredAt != 200 {
		t.Fatalf("answered interaction mismatch: %+v", it)
	}
}

// TestAnswerInteractionUnknownError: answering an unknown interaction surfaces
// the server's 404 as an error (and an empty Interaction).
func TestAnswerInteractionUnknownError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/job-1/interactions/ghost/answer", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown interaction", "detail": "ghost"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	it, err := New(ts.URL, "").AnswerInteraction("job-1", "ghost", "x", "")
	if err == nil {
		t.Fatal("expected 404 error answering unknown interaction")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404: %v", err)
	}
	if it.ID != "" {
		t.Fatalf("expected zero Interaction on error, got %+v", it)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8765":      "http://127.0.0.1:8765",
		"0.0.0.0:8765":        "http://127.0.0.1:8765",
		"http://0.0.0.0:9000": "http://127.0.0.1:9000",
		"http://host:8080/":   "http://host:8080",
		"https://example.com": "https://example.com",
		"0.0.0.0":             "http://127.0.0.1",
		"localhost:8765":      "http://localhost:8765",
	}
	for in, want := range cases {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q)=%q want %q", in, got, want)
		}
	}
}
