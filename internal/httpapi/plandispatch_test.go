package httpapi

import (
	"net/http"
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

// newTodoDispatchServer wires a server whose project allows one prompt-driven
// cli-agent ("reader" = the test binary echoing its argv), so a todo assigned to it
// really runs: the PLAN-02 dispatch rule is "assign an agent and the job appears".
func newTodoDispatchServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"reader"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"reader": {
				Type: agent.TypeCLIAgent, Command: testcmd.Path(t),
				Args: []string{"argv", "{{prompt}}"},
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, nil)
}

// planJobsOf lists the jobs the plan currently owns (the dispatch proof).
func planJobsOf(t *testing.T, s *Server, planID string) []job.JobResult {
	t.Helper()
	list, err := s.jobs.ListJobs(job.ListOpts{Plan: planID, Limit: 50})
	if err != nil {
		t.Fatalf("list plan jobs: %v", err)
	}
	return list
}

// waitDispatchedDone waits for a dispatched job's terminal snapshot with a budget a
// whole-repo `go test ./...` cannot exhaust: these jobs are real child processes, and
// under the parallel load of the full suite the shared 10s waitDone budget is tight.
func waitDispatchedDone(t *testing.T, s *Server, id string) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		res, ok := s.jobs.Get(id)
		if ok && job.IsTerminal(res.Status) {
			return res
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach terminal state in time", id)
	return job.JobResult{}
}

// TestUpdateTodoReadyDispatches (PLAN-02 P2): the two write paths that make a todo
// dispatchable both go through PATCH /v1/todos/{id} — moving it to `ready` when it
// already has an assignee, and setting the assignee on an already-ready item — and
// each one starts the job immediately, with the todo ending up done.
func TestUpdateTodoReadyDispatches(t *testing.T) {
	s := newTodoDispatchServer(t)
	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{
		"plan_id": "plan-http-dispatch", "title": "HTTP dispatch", "project": "self",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &struct{}{})

	// A: the assignee is set first, then the item turns ready.
	resp = do(t, s, http.MethodPost, "/v1/plans/plan-http-dispatch/todos", testToken, map[string]any{
		"title": "assigned first", "assignee": "reader",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add todo status=%d, want 200", resp.StatusCode)
	}
	var todoA struct {
		TodoID   string `json:"todo_id"`
		Assignee string `json:"assignee"`
		Status   string `json:"status"`
	}
	decode(t, resp, &todoA)
	if todoA.Assignee != "reader" || todoA.Status != jobstore.TodoPending {
		t.Fatalf("created todo = %+v, want reader assigned and still pending", todoA)
	}

	resp = do(t, s, http.MethodPatch, "/v1/todos/"+todoA.TodoID, testToken, map[string]string{
		"status": jobstore.TodoReady,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch todo ready status=%d, want 200", resp.StatusCode)
	}
	var todoAView struct {
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
		JobID    string `json:"job_id"`
	}
	decode(t, resp, &todoAView)
	if todoAView.Status != jobstore.TodoDoing || todoAView.JobID == "" {
		t.Fatalf("todo after ready = %+v, want it doing and bound to a job", todoAView)
	}

	// B: the item is already ready, the assignee arrives second.
	resp = do(t, s, http.MethodPost, "/v1/plans/plan-http-dispatch/todos", testToken, map[string]any{
		"title": "ready first",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add todo B status=%d, want 200", resp.StatusCode)
	}
	var todoB struct {
		TodoID string `json:"todo_id"`
	}
	decode(t, resp, &todoB)
	resp = do(t, s, http.MethodPatch, "/v1/todos/"+todoB.TodoID, testToken, map[string]string{
		"status": jobstore.TodoReady,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch todo B ready status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &struct{}{})
	if jobs := planJobsOf(t, s, "plan-http-dispatch"); len(jobs) != 1 {
		t.Fatalf("a ready todo with no assignee must not dispatch: %+v", jobs)
	}

	resp = do(t, s, http.MethodPatch, "/v1/todos/"+todoB.TodoID, testToken, map[string]string{
		"assignee": "reader",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch todo B assignee status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &struct{}{})

	jobs := planJobsOf(t, s, "plan-http-dispatch")
	if len(jobs) != 2 {
		t.Fatalf("both paths must have dispatched, got %d jobs: %+v", len(jobs), jobs)
	}
	byTodo := map[string]job.JobResult{}
	for _, j := range jobs {
		byTodo[j.TodoID] = j
	}
	for todoID, wantID := range map[string]string{todoA.TodoID: "plan-http-dispatch", todoB.TodoID: "plan-http-dispatch"} {
		j, ok := byTodo[todoID]
		if !ok {
			t.Fatalf("no job for todo %s: %+v", todoID, jobs)
		}
		if j.Agent != "reader" || j.ProjectKey != "self" || j.PlanID != wantID {
			t.Fatalf("dispatched job = %+v, want the todo's agent/project/plan", j)
		}
		final := waitDispatchedDone(t, s, j.ID)
		if final.Status != job.StatusDone {
			t.Fatalf("job %s status = %s (err=%s), want done", j.ID, final.Status, final.Error)
		}
	}

	// SUP-01 C linkage closes the items once their runs are done.
	resp = do(t, s, http.MethodGet, "/v1/plans/plan-http-dispatch", testToken, nil)
	var detail struct {
		Project string `json:"project"`
		Todos   []struct {
			TodoID   string `json:"todo_id"`
			Status   string `json:"status"`
			Assignee string `json:"assignee"`
		} `json:"todos"`
	}
	decode(t, resp, &detail)
	if detail.Project != "self" {
		t.Fatalf("plan project = %q, want self", detail.Project)
	}
	if len(detail.Todos) != 2 {
		t.Fatalf("plan todos = %+v, want 2", detail.Todos)
	}
	for _, tv := range detail.Todos {
		if tv.Status != jobstore.TodoDone {
			t.Fatalf("todo %s status = %q, want done", tv.TodoID, tv.Status)
		}
	}
}

// TestPlanDispatchEndpoint: POST /v1/todos/{id}/dispatch is the explicit path — it
// ignores the todo's status (a pending item is fine) but still needs an assignee, and
// it reports what happened instead of failing the request.
func TestPlanDispatchEndpoint(t *testing.T) {
	s := newTodoDispatchServer(t)
	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{
		"plan_id": "plan-http-now", "title": "Explicit dispatch", "project": "self",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &struct{}{})

	resp = do(t, s, http.MethodPost, "/v1/plans/plan-http-now/todos", testToken, map[string]any{
		"title": "explicit", "assignee": "reader",
	})
	var todo struct {
		TodoID string `json:"todo_id"`
	}
	decode(t, resp, &todo)

	// The item is still `pending`: an explicit dispatch ignores that.
	resp = do(t, s, http.MethodPost, "/v1/todos/"+todo.TodoID+"/dispatch", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dispatch status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		Todo       todoView       `json:"todo"`
		Job        *job.JobResult `json:"job"`
		Dispatched bool           `json:"dispatched"`
		Reason     string         `json:"reason"`
	}
	decode(t, resp, &out)
	if !out.Dispatched || out.Job == nil {
		t.Fatalf("dispatch response = %+v, want a job", out)
	}
	if out.Job.Agent != "reader" || out.Job.TodoID != todo.TodoID {
		t.Fatalf("dispatched job = %+v, want the todo's agent", out.Job)
	}
	if out.Todo.Status != jobstore.TodoDoing {
		t.Fatalf("dispatch response todo status = %q, want doing", out.Todo.Status)
	}
	final := waitDispatchedDone(t, s, out.Job.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("job status = %s (err=%s), want done", final.Status, final.Error)
	}

	// A second explicit dispatch once that job is terminal starts a fresh run: the
	// explicit path requires an assignee and no ACTIVE job, nothing more.
	resp = do(t, s, http.MethodPost, "/v1/todos/"+todo.TodoID+"/dispatch", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("redispatch status=%d, want 200", resp.StatusCode)
	}
	var second struct {
		Job        *job.JobResult `json:"job"`
		Dispatched bool           `json:"dispatched"`
	}
	decode(t, resp, &second)
	if !second.Dispatched || second.Job == nil || second.Job.ID == out.Job.ID {
		t.Fatalf("redispatch response = %+v, want a new job", second)
	}
	waitDispatchedDone(t, s, second.Job.ID)

	resp = do(t, s, http.MethodPost, "/v1/plans/plan-http-now/todos", testToken, map[string]any{
		"title": "unassigned",
	})
	var bare struct {
		TodoID string `json:"todo_id"`
	}
	decode(t, resp, &bare)
	resp = do(t, s, http.MethodPost, "/v1/todos/"+bare.TodoID+"/dispatch", testToken, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("dispatch without an assignee status=%d, want 400", resp.StatusCode)
	}
	decode(t, resp, &struct{}{})

	resp = do(t, s, http.MethodPost, "/v1/todos/todo-missing/dispatch", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("dispatch unknown todo status=%d, want 404", resp.StatusCode)
	}
	decode(t, resp, &struct{}{})
}

// TestPlanShowIncludesUsage (PLAN-02 P2): the plan detail rolls up what its jobs
// reported, per agent and in total — the number a reader asks for without opening
// every job of the plan.
func TestPlanShowIncludesUsage(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": "plan-usage"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &struct{}{})

	for _, rec := range []jobstore.JobRecord{
		{
			ID: "usage-omp-1", ProjectKey: "self", Agent: "omp", Runner: "local",
			Status: job.StatusDone, ResultDir: "/tmp/results/usage-omp-1",
			UsageJSON: `{"total_tokens":1000,"input_tokens":600,"output_tokens":400,"cost_usd":0.5}`,
			StartedAt: 1, UpdatedAt: 1, EndedAt: 1,
		},
		{
			ID: "usage-omp-2", ProjectKey: "self", Agent: "omp", Runner: "local",
			Status: job.StatusDone, ResultDir: "/tmp/results/usage-omp-2",
			UsageJSON: `{"total_tokens":500,"input_tokens":200,"output_tokens":300,"cost_usd":0.25}`,
			StartedAt: 2, UpdatedAt: 2, EndedAt: 2,
		},
		{
			ID: "usage-codex-1", ProjectKey: "self", Agent: "codex", Runner: "local",
			Status: job.StatusDone, ResultDir: "/tmp/results/usage-codex-1",
			UsageJSON: `{"total_tokens":200,"input_tokens":100,"output_tokens":100,"cost_usd":0.05}`,
			StartedAt: 3, UpdatedAt: 3, EndedAt: 3,
		},
		{
			// A job that captured no usage still RAN: it counts in jobs, not in tokens.
			ID: "usage-silent", ProjectKey: "self", Agent: "omp", Runner: "local",
			Status: job.StatusFailed, ResultDir: "/tmp/results/usage-silent",
			StartedAt: 4, UpdatedAt: 4, EndedAt: 4,
		},
	} {
		if err := s.jobs.Meta().UpsertJob(rec); err != nil {
			t.Fatalf("upsert %s: %v", rec.ID, err)
		}
		resp := do(t, s, http.MethodPost, "/v1/plans/plan-usage/jobs", testToken, map[string]string{"job_id": rec.ID})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("attach %s status=%d, want 200", rec.ID, resp.StatusCode)
		}
		decode(t, resp, &struct{}{})
	}

	resp = do(t, s, http.MethodGet, "/v1/plans/plan-usage", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get plan status=%d, want 200", resp.StatusCode)
	}
	var detail struct {
		Usage struct {
			Jobs        int     `json:"jobs"`
			TotalTokens int64   `json:"total_tokens"`
			CostUSD     float64 `json:"cost_usd"`
			ByAgent     map[string]struct {
				Jobs         int     `json:"jobs"`
				TotalTokens  int64   `json:"total_tokens"`
				InputTokens  int64   `json:"input_tokens"`
				OutputTokens int64   `json:"output_tokens"`
				CostUSD      float64 `json:"cost_usd"`
			} `json:"by_agent"`
		} `json:"usage"`
	}
	decode(t, resp, &detail)
	if detail.Usage.Jobs != 4 {
		t.Fatalf("usage jobs = %d, want 4 (a job that reported nothing still ran)", detail.Usage.Jobs)
	}
	if detail.Usage.TotalTokens != 1700 {
		t.Fatalf("usage total_tokens = %d, want 1700", detail.Usage.TotalTokens)
	}
	if detail.Usage.CostUSD < 0.79 || detail.Usage.CostUSD > 0.81 {
		t.Fatalf("usage cost_usd = %v, want 0.80", detail.Usage.CostUSD)
	}
	omp := detail.Usage.ByAgent["omp"]
	if omp.Jobs != 3 || omp.TotalTokens != 1500 || omp.InputTokens != 800 || omp.OutputTokens != 700 {
		t.Fatalf("omp usage = %+v, want 3 jobs / 1500 total", omp)
	}
	if codex := detail.Usage.ByAgent["codex"]; codex.Jobs != 1 || codex.TotalTokens != 200 {
		t.Fatalf("codex usage = %+v, want 1 job / 200 tokens", codex)
	}
}
