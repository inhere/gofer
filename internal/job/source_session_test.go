package job

import (
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestValidateSourceSessionRequiresTrustedMatchingContext(t *testing.T) {
	db, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	defer db.Close()
	workDir := filepath.Join(t.TempDir(), "project")
	_, err = db.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "source-session-1", Agent: "suag", ProjectKey: "project",
		Runner: "local", Cwd: workDir, CallerID: "caller-a",
	})
	assert.NoErr(t, err)
	_, err = db.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "source-session-foreign", Agent: "suag", ProjectKey: "project",
		Runner: "local", Cwd: workDir, CallerID: "caller-b",
	})
	assert.NoErr(t, err)
	err = db.InsertPlan(jobstore.Plan{PlanID: "plan-source-1", Owner: "caller-a", SupervisorSessionID: "source-session-1"})
	assert.NoErr(t, err)

	svc := &Service{meta: db}
	base := JobRequest{ProjectKey: "project", Runner: "local", CallerID: "caller-a", SourceSessionID: "source-session-1"}
	assert.NoErr(t, svc.validateSourceSession(base, workDir, false))

	for _, tc := range []struct {
		name   string
		mutate func(*JobRequest)
	}{
		{"missing authenticated caller", func(r *JobRequest) { r.CallerID = "" }},
		{"foreign session", func(r *JobRequest) { r.SourceSessionID = "source-session-foreign" }},
		{"unknown session", func(r *JobRequest) { r.SourceSessionID = "missing" }},
		{"project mismatch", func(r *JobRequest) { r.ProjectKey = "other" }},
		{"runner mismatch", func(r *JobRequest) { r.Runner = "worker-x" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mutate(&req)
			assert.Err(t, svc.validateSourceSession(req, workDir, false))
		})
	}
	assert.Err(t, svc.validateSourceSession(base, filepath.Join(workDir, "subdir"), false))
	assert.Err(t, svc.validateSourceSession(base, workDir, true))

	planReq := base
	planReq.PlanID = "plan-source-1"
	assert.NoErr(t, svc.validateSourceSession(planReq, workDir, false))
	planReq.CallerID = "caller-b"
	assert.Err(t, svc.validateSourceSession(planReq, workDir, false))
}

func TestSubmitSourceSessionWatchesAndReadsPersistedProvenance(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	svc := newTestService(t, root)
	_, err := svc.meta.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "submit-source-session", Agent: "suag", ProjectKey: "self",
		Runner: "local", Cwd: root, CallerID: "caller-a",
	})
	assert.NoErr(t, err)

	res, err := svc.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", CallerID: "caller-a", SourceSessionID: "submit-source-session",
	})
	assert.NoErr(t, err)
	_, ok := svc.Wait(res.ID)
	assert.True(t, ok)
	rec, ok, err := svc.meta.GetJob(res.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "submit-source-session", rec.SourceSessionID)
	assert.Eq(t, "submit-source-session", fromRecord(rec).SourceSessionID)
	watches, err := svc.meta.ListSessionJobWatches("submit-source-session")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(watches))
	assert.Eq(t, res.ID, watches[0].JobID)
}

func TestSubmitInvalidSourceSessionCreatesNoJobOrWatch(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, t.TempDir())
	_, err := svc.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", CallerID: "caller-a", SourceSessionID: "missing-source-session",
	})
	assert.Err(t, err)
	jobs, err := svc.ListJobs(ListOpts{Project: "self"})
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(jobs))
	watches, err := svc.meta.ListSessionJobWatches("missing-source-session")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(watches))
}

func TestBoundPlanAutoChainKeepsAuthenticatedSourceSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	svc := newTestService(t, root)
	_, err := svc.meta.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "chain-source-session", Agent: "suag", ProjectKey: "self", Runner: "local", Cwd: root, CallerID: "caller-a",
	})
	assert.NoErr(t, err)
	assert.NoErr(t, svc.meta.InsertPlan(jobstore.Plan{
		PlanID: "plan-bound-auto-chain", Owner: "caller-a", ProjectKey: "self", SupervisorSessionID: "chain-source-session",
	}))
	assert.NoErr(t, svc.meta.InsertTodo(jobstore.PlanTodo{
		TodoID: "todo-bound-root", PlanID: "plan-bound-auto-chain", Title: "root", Assignee: "exec", Cmd: []string{"go", "version"}, Runner: "local", Cwd: ".",
	}))
	assert.NoErr(t, svc.meta.InsertTodo(jobstore.PlanTodo{
		TodoID: "todo-bound-next", PlanID: "plan-bound-auto-chain", Title: "next", Assignee: "exec", Cmd: []string{"go", "version"}, Runner: "local", Cwd: ".", After: []string{"todo-bound-root"}, Auto: true,
	}))

	if _, err := svc.RunPlan("plan-bound-auto-chain", "caller-a"); err != nil {
		t.Fatal(err)
	}
	rootTodo, ok, err := svc.meta.GetTodo("todo-bound-root")
	assert.NoErr(t, err)
	assert.True(t, ok)
	if rootTodo.JobID == "" {
		t.Fatal("plan run did not dispatch root todo")
	}
	_, ok = svc.Wait(rootTodo.JobID)
	assert.True(t, ok)

	nextTodo, ok, err := svc.meta.GetTodo("todo-bound-next")
	assert.NoErr(t, err)
	assert.True(t, ok)
	if nextTodo.JobID == "" {
		t.Fatalf("bound plan did not auto-dispatch dependent todo: status=%s error=%q", nextTodo.Status, nextTodo.DispatchError)
	}
	_, ok = svc.Wait(nextTodo.JobID)
	assert.True(t, ok)
	for _, id := range []string{rootTodo.JobID, nextTodo.JobID} {
		rec, ok, err := svc.meta.GetJob(id)
		assert.NoErr(t, err)
		assert.True(t, ok)
		assert.Eq(t, "chain-source-session", rec.SourceSessionID)
	}
	watches, err := svc.meta.ListSessionJobWatches("chain-source-session")
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(watches))
}

// TestBoundPlanDispatchesForASessionElsewhere: the supervising session runs on a
// worker (a container terminal), the todo runs on the server. The job cannot claim
// the session as its trusted source, so it is dispatched without one, and the
// session still gets the job's completion watch.
func TestBoundPlanDispatchesForASessionElsewhere(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	svc := newTestService(t, root)
	_, err := svc.meta.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "container-session", Agent: "claude", ProjectKey: "self", Runner: "w-docker", Cwd: "/workspace/repo", CallerID: "caller-a",
	})
	assert.NoErr(t, err)
	assert.NoErr(t, svc.meta.InsertPlan(jobstore.Plan{
		PlanID: "plan-bound-elsewhere", Owner: "caller-a", ProjectKey: "self", SupervisorSessionID: "container-session",
	}))
	assert.NoErr(t, svc.meta.InsertTodo(jobstore.PlanTodo{
		TodoID: "todo-elsewhere", PlanID: "plan-bound-elsewhere", Title: "build", Assignee: "exec", Cmd: []string{"go", "version"}, Runner: "local", Cwd: ".",
	}))

	if _, err := svc.RunPlan("plan-bound-elsewhere", "caller-a"); err != nil {
		t.Fatal(err)
	}
	todo, ok, err := svc.meta.GetTodo("todo-elsewhere")
	assert.NoErr(t, err)
	assert.True(t, ok)
	if todo.JobID == "" {
		t.Fatalf("todo not dispatched: status=%s error=%q", todo.Status, todo.DispatchError)
	}
	_, ok = svc.Wait(todo.JobID)
	assert.True(t, ok)
	rec, ok, err := svc.meta.GetJob(todo.JobID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", rec.SourceSessionID)
	watches, err := svc.meta.ListSessionJobWatches("container-session")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(watches))
}
