package httpapi

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
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

// G043 spelling-consistency matrix: every entry that receives or stores a runner
// label is driven once with `server` and once with `local`, and must land on the
// same canonical value. A new entry that forgets to normalize shows up here as a
// row whose two spellings diverge.
var runnerSpellings = []string{config.BuiltinLocalRunnerAlias, config.BuiltinLocalRunner}

// newSpellingServer builds a server over a "self" project that allows the built-in
// runner (listed under its CLI spelling `server`).
func newSpellingServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: filepath.Join(root, "store")},
		Runners: map[string]config.RunnerConfig{},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"exec"}, AllowExec: true,
				AllowedRunners: []string{config.BuiltinLocalRunnerAlias}},
		},
	}
	projects := project.NewRegistry(cfg, filepath.Join(root, "config.yaml"))
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, cfg.Runners, nil, nil)
}

func TestRunnerSpellingMatrix(t *testing.T) {
	t.Parallel()
	s := newSpellingServer(t)
	canon := config.BuiltinLocalRunner

	for _, sp := range runnerSpellings {
		t.Run("job_submit/"+sp, func(t *testing.T) {
			resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
				ProjectKey: "self", Agent: "exec", Runner: sp,
				Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30, Sync: true,
			})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			var jr job.JobResult
			decode(t, resp, &jr)
			if jr.Runner != canon {
				t.Fatalf("stored runner=%q, want %q", jr.Runner, canon)
			}
		})
		t.Run("job_list_filter/"+sp, func(t *testing.T) {
			resp := do(t, s, http.MethodGet, "/v1/jobs?runner="+sp, testToken, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			var out struct {
				Jobs []job.JobResult `json:"jobs"`
			}
			decode(t, resp, &out)
			if len(out.Jobs) == 0 {
				t.Fatalf("filter %q found no jobs; both spellings must match the canonical rows", sp)
			}
		})
		t.Run("plan_todo/"+sp, func(t *testing.T) {
			resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]any{"title": "p-" + sp})
			var plan struct {
				PlanID string `json:"plan_id"`
				ID     string `json:"id"`
			}
			decode(t, resp, &plan)
			pid := plan.PlanID
			if pid == "" {
				pid = plan.ID
			}
			resp = do(t, s, http.MethodPost, "/v1/plans/"+pid+"/todos", testToken, map[string]any{"title": "t", "runner": sp})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("add todo status=%d", resp.StatusCode)
			}
			var tv todoView
			decode(t, resp, &tv)
			if tv.Runner != canon {
				t.Fatalf("created todo runner=%q, want %q", tv.Runner, canon)
			}
			other := config.BuiltinLocalRunnerAlias
			if sp == other {
				other = config.BuiltinLocalRunner
			}
			// A patch with the other spelling is still canonicalized.
			resp = do(t, s, http.MethodPatch, "/v1/todos/"+tv.TodoID, testToken, map[string]any{"runner": other})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("patch todo status=%d", resp.StatusCode)
			}
			decode(t, resp, &tv)
			if tv.Runner != canon {
				t.Fatalf("patched todo runner=%q, want %q", tv.Runner, canon)
			}
		})
		t.Run("schedule/"+sp, func(t *testing.T) {
			resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, createScheduleReq{
				Name: "n-" + sp, Cron: "*/5 * * * *",
				Request: job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: sp,
					Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30},
			})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			var sv scheduleView
			decode(t, resp, &sv)
			if sv.Request.Runner != canon {
				t.Fatalf("stored schedule runner=%q, want %q", sv.Request.Runner, canon)
			}
		})
		t.Run("session_register/"+sp, func(t *testing.T) {
			sid := "sid-" + sp
			resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
				"session_id": sid, "agent": "claude", "runner": sp, "event": "SessionStart"})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			var sv sessionView
			decode(t, resp, &sv)
			if sv.Runner != canon {
				t.Fatalf("view runner=%q, want %q", sv.Runner, canon)
			}
			a, ok, err := s.jobs.Meta().GetAgentSession(sid)
			if err != nil || !ok || a.Runner != canon {
				t.Fatalf("stored session runner=%q ok=%v err=%v, want %q", a.Runner, ok, err, canon)
			}
			if !s.relay.IsServerLocalRunner(a.Runner) || !s.relay.IsServerLocalRunner(sp) {
				t.Fatalf("relay does not recognize %q as the server-local runner", sp)
			}
		})
		t.Run("project_allowed_runners/"+sp, func(t *testing.T) {
			resp := do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{
				Key: "proj-" + sp, HostPath: strPtr(t.TempDir()), AllowedRunners: strsPtr(sp), AllowExec: boolptr(true)})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			resp = do(t, s, http.MethodGet, "/v1/projects/proj-"+sp, testToken, nil)
			var pv projectView
			decode(t, resp, &pv)
			if !slices.Equal(pv.AllowedRunners, []string{canon}) {
				t.Fatalf("stored allowed_runners=%v, want [%s]", pv.AllowedRunners, canon)
			}
		})
		t.Run("workflow_spec/"+sp, func(t *testing.T) {
			resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, workflow.Spec{
				Steps: []workflow.StepSpec{{Name: "a", ProjectKey: "self", Agent: "exec", Runner: sp,
					Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30}}})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			var created struct {
				ID string `json:"id"`
			}
			decode(t, resp, &created)
			wf, ok, err := s.workflow.GetWorkflow(created.ID)
			if err != nil || !ok {
				t.Fatalf("get workflow: ok=%v err=%v", ok, err)
			}
			if !strings.Contains(wf.SpecJSON, `"runner":"`+canon+`"`) {
				t.Fatalf("stored spec runner not canonical: %s", wf.SpecJSON)
			}
			waitWorkflowStatus(t, s, created.ID, "done")
		})
	}
}

// TestRunnerSpellingLegacySessionRow: a session row stored under the alias before
// registration normalized it must read back as the canonical runner.
func TestRunnerSpellingLegacySessionRow(t *testing.T) {
	t.Parallel()
	s := newSpellingServer(t)
	if _, err := s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: "legacy-1", Agent: "claude", Runner: config.BuiltinLocalRunnerAlias}); err != nil {
		t.Fatal(err)
	}
	resp := do(t, s, http.MethodGet, "/v1/sessions/legacy-1", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var detail struct {
		Session sessionView `json:"session"`
	}
	decode(t, resp, &detail)
	if sv := detail.Session; sv.Runner != config.BuiltinLocalRunner {
		t.Fatalf("legacy row runner=%q, want %q", sv.Runner, config.BuiltinLocalRunner)
	}
}

// TestRunnerSpellingLegacyJobRowListed: a jobs row stored under the alias is found
// by `runner=local` and `runner=server` alike.
func TestRunnerSpellingLegacyJobRowListed(t *testing.T) {
	t.Parallel()
	s := newSpellingServer(t)
	rec := jobstore.JobRecord{ID: "20260101-000000-legacy", ProjectKey: "self", Agent: "exec",
		Runner: config.BuiltinLocalRunnerAlias, Status: "done", StartedAt: 1, EndedAt: 2}
	if err := s.jobs.Meta().UpsertJob(rec); err != nil {
		t.Fatalf("seed legacy job row: %v", err)
	}
	for _, sp := range runnerSpellings {
		resp := do(t, s, http.MethodGet, "/v1/jobs?runner="+sp, testToken, nil)
		var out struct {
			Jobs []job.JobResult `json:"jobs"`
		}
		decode(t, resp, &out)
		found := false
		for _, j := range out.Jobs {
			found = found || j.ID == rec.ID
		}
		if !found {
			t.Fatalf("runner=%s did not list the legacy row (%d jobs)", sp, len(out.Jobs))
		}
	}
}
