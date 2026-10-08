package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestHTTPSourceSessionTrustAndPlanBindingRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	cwd := s.projects.Config().Projects["self"].HostPath
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "http-source-session", "agent": "suag", "project_key": "self", "runner": "local", "cwd": cwd,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register session status=%d, want 200", resp.StatusCode)
	}
	if _, err := s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: "foreign-http-source", Agent: "suag", ProjectKey: "self", Runner: "local", Cwd: cwd, CallerID: "other",
	}); err != nil {
		t.Fatal(err)
	}

	resp = do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".",
		CallerID: "forged-caller", SourceSessionID: "http-source-session",
	})
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("source-bound submit status=%d, want 200 or 202", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.CallerID != "default" || created.SourceSessionID != "http-source-session" {
		t.Fatalf("HTTP auth stamp/provenance mismatch: caller=%q source=%q", created.CallerID, created.SourceSessionID)
	}
	waitDone(t, s, created.ID)
	watches, err := s.jobs.Meta().ListSessionJobWatches("http-source-session")
	if err != nil || len(watches) != 1 || watches[0].JobID != created.ID {
		t.Fatalf("source watch mismatch: watches=%+v err=%v", watches, err)
	}

	resp = do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".",
		SourceSessionID: "foreign-http-source",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("foreign source session status=%d, want 400", resp.StatusCode)
	}
	jobs, err := s.jobs.ListJobs(job.ListOpts{Project: "self"})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("foreign source submit created job: jobs=%d err=%v", len(jobs), err)
	}

	resp = do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]any{
		"plan_id": "plan-source-http", "title": "source plan", "project": "self", "supervisor_session_id": "http-source-session",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bound plan create status=%d, want 200", resp.StatusCode)
	}
	var plan struct {
		PlanID              string `json:"plan_id"`
		Owner               string `json:"owner"`
		SupervisorSessionID string `json:"supervisor_session_id"`
	}
	decode(t, resp, &plan)
	if plan.Owner != "default" || plan.SupervisorSessionID != "http-source-session" {
		t.Fatalf("plan binding did not round trip: %+v", plan)
	}
	resp = do(t, s, http.MethodPatch, "/v1/plans/plan-source-http", testToken, map[string]any{"supervisor_session_id": ""})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear supervisor session status=%d, want 200", resp.StatusCode)
	}
	plan = struct {
		PlanID              string `json:"plan_id"`
		Owner               string `json:"owner"`
		SupervisorSessionID string `json:"supervisor_session_id"`
	}{}
	decode(t, resp, &plan)
	if plan.SupervisorSessionID != "" {
		t.Fatalf("cleared supervisor session = %q", plan.SupervisorSessionID)
	}
}
