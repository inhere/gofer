package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newLeaderPlanServer builds a Server whose config carries a supervisor.leader block
// (master switch ON, agent `exec`) — the LEAD-02 fixture. The per-plan `leader` column
// is what the tests below flip, so the global block must be present and enabled for the
// plan-level gate to be the one under test.
func newLeaderPlanServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Supervisor: &config.SupervisorConfig{Leader: &config.LeaderConfig{
			Enabled:           true,
			Agent:             "exec",
			Scopes:            []string{config.LeaderPlanScope},
			WakeDelaySec:      3600,
			MaxRoundsPerScope: 3,
		}},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, st, nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, nil)
}

// TestPlanLeaderToggleWarnsOnRunningMembers: switching a plan's leader on while member
// jobs are still in flight warns how many will wake a round when they finish — those
// jobs were started before the switch existed, so the warning is the only notice the
// operator gets (design §二, 0.2 decision ④).
func TestPlanLeaderToggleWarnsOnRunningMembers(t *testing.T) {
	s := newLeaderPlanServer(t)
	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]any{"plan_id": "plan-lead"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d, want 200", resp.StatusCode)
	}
	// A member job that stays in flight: an exec job sleeping far longer than the test.
	resp = do(t, s, http.MethodPost, "/v1/jobs", testToken, map[string]any{
		"project_key": "self", "agent": "exec", "runner": "local",
		"cmd": []string{"sleep", "30s"}, "cwd": ".", "timeout_sec": 60,
		"plan_id": "plan-lead", "title": "成员 job",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit member status=%d, want 200 (body: %s)", resp.StatusCode, bodyString(t, resp))
	}
	var member job.JobResult
	decode(t, resp, &member)
	waitRunning(t, s, member.ID)

	type leaderResp struct {
		PlanID   string   `json:"plan_id"`
		Leader   string   `json:"leader"`
		Warnings []string `json:"warnings"`
	}
	var updated leaderResp
	resp = do(t, s, http.MethodPatch, "/v1/plans/plan-lead", testToken, map[string]any{"leader": "on"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch leader on status=%d, want 200 (body: %s)", resp.StatusCode, bodyString(t, resp))
	}
	decode(t, resp, &updated)
	if updated.Leader != jobstore.PlanLeaderOn {
		t.Fatalf("leader = %q, want %q", updated.Leader, jobstore.PlanLeaderOn)
	}
	want := "1 running job(s) will wake the leader when they finish"
	if len(updated.Warnings) != 1 || updated.Warnings[0] != want {
		t.Fatalf("warnings = %v, want [%q]", updated.Warnings, want)
	}

	// Switching it back off is not a warning case: nothing will wake.
	resp = do(t, s, http.MethodPatch, "/v1/plans/plan-lead", testToken, map[string]any{"leader": "off"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch leader off status=%d, want 200", resp.StatusCode)
	}
	var off leaderResp // a fresh decode: an omitted `warnings` must not inherit the old one
	decode(t, resp, &off)
	if off.Leader != jobstore.PlanLeaderOff || len(off.Warnings) != 0 {
		t.Fatalf("leader off response = %+v, want off with no warnings", off)
	}

	// The plan switch is a human's decision: a job credential may not reach the route at
	// all (SEC-01's default-deny; the member job above is the proof it is a real caller).
	tok := seedJobToken(t, s, member.ID, jobstore.JobCredentialMember, "plan-lead")
	resp = do(t, s, http.MethodPatch, "/v1/plans/plan-lead", tok, map[string]any{"leader": "on"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("job caller patch leader status=%d, want 403 (body: %s)", resp.StatusCode, bodyString(t, resp))
	}
}

// TestConfigViewShowsLeader: GET /v1/config's supervisor view carries the leader block
// (LEAD-02 closes the S3 leftover: the block was configurable but unreadable over HTTP).
func TestConfigViewShowsLeader(t *testing.T) {
	s := newLeaderPlanServer(t)
	resp := do(t, s, http.MethodGet, "/v1/config", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get config status=%d, want 200", resp.StatusCode)
	}
	var view struct {
		Supervisor *struct {
			Leader *struct {
				Enabled           bool     `json:"enabled"`
				Agent             string   `json:"agent"`
				Scopes            []string `json:"scopes"`
				MaxRoundsPerScope int      `json:"max_rounds_per_scope"`
				WakeDelaySec      int      `json:"wake_delay_sec"`
				OnMemberDone      bool     `json:"on_member_done"`
			} `json:"leader"`
		} `json:"supervisor"`
	}
	decode(t, resp, &view)
	if view.Supervisor == nil || view.Supervisor.Leader == nil {
		t.Fatalf("config view has no supervisor.leader block: %+v", view)
	}
	l := view.Supervisor.Leader
	if !l.Enabled || l.Agent != "exec" {
		t.Fatalf("leader = %+v, want enabled with agent exec", l)
	}
	if len(l.Scopes) != 1 || l.Scopes[0] != config.LeaderPlanScope {
		t.Fatalf("leader scopes = %v, want [%s]", l.Scopes, config.LeaderPlanScope)
	}
	if l.MaxRoundsPerScope != 3 || l.WakeDelaySec != 3600 || !l.OnMemberDone {
		t.Fatalf("leader params = %+v, want rounds=3 delay=3600 on_member_done=true", l)
	}
}

// TestPlanEventsEndpoint: GET /v1/plans/{id}/events serves the plan scope's event stream
// newest-first with a `before` cursor — the plan page's event area (S4 leftover: the
// events existed, nothing rendered them).
func TestPlanEventsEndpoint(t *testing.T) {
	s := newLeaderPlanServer(t)
	for _, id := range []string{"plan-events", "plan-other"} {
		resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]any{"plan_id": id})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create %s status=%d, want 200", id, resp.StatusCode)
		}
	}
	// Three events on the plan under test, one on a sibling plan (must not appear).
	for _, reason := range []string{"paused", "plan_done", "global_off"} {
		s.jobs.RecordScopedEvent(job.PlanEventScope("plan-events"), job.EventPlanLeaderSkipped, "self",
			map[string]any{"plan_id": "plan-events", "reason": reason})
	}
	s.jobs.RecordScopedEvent(job.PlanEventScope("plan-other"), job.EventPlanLeaderSkipped, "self",
		map[string]any{"plan_id": "plan-other", "reason": "paused"})

	var page struct {
		Events []jobstore.JobEvent `json:"events"`
	}
	resp := do(t, s, http.MethodGet, "/v1/plans/plan-events/events?limit=2", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list plan events status=%d, want 200 (body: %s)", resp.StatusCode, bodyString(t, resp))
	}
	decode(t, resp, &page)
	if len(page.Events) != 2 {
		t.Fatalf("events = %d rows, want 2 (limit)", len(page.Events))
	}
	// Newest first: the last recorded reason leads.
	if page.Events[0].Seq <= page.Events[1].Seq {
		t.Fatalf("events are not newest-first: %+v", page.Events)
	}
	if got := eventReason(t, page.Events[0].Detail); got != "global_off" {
		t.Fatalf("first event reason = %q, want global_off", got)
	}
	for _, ev := range page.Events {
		if ev.JobID != job.PlanEventScope("plan-events") {
			t.Fatalf("event %+v leaked from another scope", ev)
		}
	}

	// The `before` cursor pages backwards without repeating a row.
	resp = do(t, s, http.MethodGet,
		"/v1/plans/plan-events/events?limit=2&before="+itoa(page.Events[1].Seq), testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("paged plan events status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &page)
	if len(page.Events) != 1 || eventReason(t, page.Events[0].Detail) != "paused" {
		t.Fatalf("second page = %+v, want only the oldest event", page.Events)
	}

	// An unknown plan is a 404 (consistent with handleGetPlan), not an empty stream.
	resp = do(t, s, http.MethodGet, "/v1/plans/plan-nope/events", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown plan events status=%d, want 404", resp.StatusCode)
	}

	// A job credential may READ the stream (SEC-01: the whole GET surface is open — a
	// leader must be able to see what happened on its own plan).
	tok := seedJobToken(t, s, "job-reader", jobstore.JobCredentialLeader, "plan-events")
	resp = do(t, s, http.MethodGet, "/v1/plans/plan-events/events", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job caller list plan events status=%d, want 200 (body: %s)", resp.StatusCode, bodyString(t, resp))
	}
	decode(t, resp, &page)
	if len(page.Events) != 3 {
		t.Fatalf("job caller saw %d events, want the plan's 3", len(page.Events))
	}
}

// eventReason reads the `reason` field of a scoped event's detail_json.
func eventReason(t *testing.T, detail string) string {
	t.Helper()
	var d struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(detail), &d); err != nil {
		t.Fatalf("decode event detail %q: %v", detail, err)
	}
	return d.Reason
}
