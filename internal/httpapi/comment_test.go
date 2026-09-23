package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

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

// newCommentServer builds a Server whose project "self" also allows a cli-agent "ok"
// (echoing its argv) and a role "reviewer" over it, so the HTTP layer's @-mention
// dispatch is exercisable end to end. The shared newTestServer fixture is exec-only,
// which no mention can use: an exec job's argv belongs to the caller, not to a comment.
func newCommentServer(t *testing.T, token string, workers map[string]config.WorkerAuthConfig) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: token, Workers: workers},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"ok", agent.ExecAgentKey},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"ok": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"argv", "{{prompt}}"}},
		},
		Roles: map[string]config.RoleConfig{"reviewer": {Agent: "ok"}},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, token, false, jobs, eng, projects, agents, nil, nil, nil, nil)
}

// createdJobID POSTs one HTTP job and returns its id.
func createdJobID(t *testing.T, s *Server, body job.JobRequest) string {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create job status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatalf("created job has no id: %+v", created)
	}
	return created.ID
}

// TestCommentEndpoints covers the three comment scopes over HTTP: posting and listing
// a thread on a job, a plan and a plan's todo, the @-mention dispatch a user's comment
// performs, the worker-caller refusal, and the parent-404s.
func TestCommentEndpoints(t *testing.T) {
	s := newCommentServer(t, testToken, nil)
	src := createdJobID(t, s, job.JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".",
		Title: "源 job", Prompt: "REPORT-MARKER-77", TimeoutSec: 30,
	})

	// --- job scope: a user's mention dispatches ------------------------------
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+src+"/comments", testToken,
		map[string]string{"body": "@ok 补上测试"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post job comment status=%d, want 200", resp.StatusCode)
	}
	var posted commentView
	decode(t, resp, &posted)
	if posted.ID == "" || posted.Scope != jobstore.CommentScopeJob || posted.ScopeID != src {
		t.Fatalf("posted comment = %+v", posted)
	}
	if posted.AuthorKind != jobstore.CommentAuthorUser || posted.Author != "default" {
		t.Fatalf("author = %s/%s, want default/user", posted.Author, posted.AuthorKind)
	}
	if len(posted.Dispatched) != 1 || posted.Dispatched[0].JobID == "" {
		t.Fatalf("dispatched = %+v, want one job", posted.Dispatched)
	}
	if posted.TriggeredJobID != posted.Dispatched[0].JobID {
		t.Fatalf("triggered_job_id = %q, want the dispatched job %q", posted.TriggeredJobID, posted.Dispatched[0].JobID)
	}
	if len(posted.Mentions) != 1 || posted.Mentions[0] != "ok" {
		t.Fatalf("mentions = %v, want [ok]", posted.Mentions)
	}

	resp = do(t, s, http.MethodGet, "/v1/jobs/"+src+"/comments", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list job comments status=%d, want 200", resp.StatusCode)
	}
	var listed struct {
		Comments []commentView `json:"comments"`
	}
	decode(t, resp, &listed)
	if len(listed.Comments) != 1 || listed.Comments[0].ID != posted.ID {
		t.Fatalf("comments = %+v, want the posted one", listed.Comments)
	}

	// --- agent author: an in-job MCP caller's identity is stamped, never forged ---
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+src+"/comments", testToken,
		map[string]string{"body": "@ok 我又说话了", "as_job": src})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post as_job comment status=%d, want 200", resp.StatusCode)
	}
	var asAgent commentView
	decode(t, resp, &asAgent)
	if asAgent.AuthorKind != jobstore.CommentAuthorAgent || asAgent.Author != "ok" {
		t.Fatalf("as_job author = %s/%s, want ok/agent", asAgent.Author, asAgent.AuthorKind)
	}
	if len(asAgent.Dispatched) != 0 || asAgent.TriggeredJobID != "" {
		t.Fatalf("an agent comment dispatched: %+v", asAgent)
	}
	// An unknown as_job is a 404, not a silently anonymous user comment.
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+src+"/comments", testToken,
		map[string]string{"body": "@ok hi", "as_job": "job-nope"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown as_job status=%d, want 404", resp.StatusCode)
	}

	// --- plan scope -----------------------------------------------------------
	resp = do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": "plan-comments", "project": "self"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/plans/plan-comments/comments", testToken, map[string]string{"body": "计划怎么看"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post plan comment status=%d, want 200", resp.StatusCode)
	}
	var planCm commentView
	decode(t, resp, &planCm)
	if planCm.Scope != jobstore.CommentScopePlan || planCm.ScopeID != "plan-comments" || len(planCm.Dispatched) != 0 {
		t.Fatalf("plan comment = %+v", planCm)
	}
	resp = do(t, s, http.MethodGet, "/v1/plans/plan-comments/comments", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list plan comments status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &listed)
	if len(listed.Comments) != 1 {
		t.Fatalf("plan comments = %+v", listed.Comments)
	}

	// --- todo scope (nested route) --------------------------------------------
	resp = do(t, s, http.MethodPost, "/v1/plans/plan-comments/todos", testToken, map[string]string{"title": "补测试"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add todo status=%d", resp.StatusCode)
	}
	var todo struct {
		TodoID string `json:"todo_id"`
	}
	decode(t, resp, &todo)
	if todo.TodoID == "" {
		t.Fatal("todo has no id")
	}
	resp = do(t, s, http.MethodPost, "/v1/plans/plan-comments/todos/"+todo.TodoID+"/comments", testToken,
		map[string]string{"body": "@reviewer 你来"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post todo comment status=%d, want 200", resp.StatusCode)
	}
	var todoCm commentView
	decode(t, resp, &todoCm)
	if todoCm.Scope != jobstore.CommentScopeTodo || todoCm.ScopeID != todo.TodoID {
		t.Fatalf("todo comment = %+v", todoCm)
	}
	if len(todoCm.Dispatched) != 1 {
		t.Fatalf("todo mention dispatched = %+v, want one", todoCm.Dispatched)
	}
	if res, ok := s.jobs.Get(todoCm.Dispatched[0].JobID); !ok || res.TodoID != todo.TodoID {
		t.Fatalf("dispatched job is not bound to the todo: %+v", res)
	}
	// The same thread is readable by the id-only route the CLI/MCP address it with.
	resp = do(t, s, http.MethodGet, "/v1/todos/"+todo.TodoID+"/comments", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list todo comments status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &listed)
	if len(listed.Comments) != 1 || listed.Comments[0].ID != todoCm.ID {
		t.Fatalf("todo comments = %+v", listed.Comments)
	}
	// --- validation + parent 404s ---------------------------------------------
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+src+"/comments", testToken, map[string]string{"body": "   "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty body status=%d, want 400", resp.StatusCode)
	}
	resp = do(t, s, http.MethodPost, "/v1/jobs/job-nope/comments", testToken, map[string]string{"body": "hi"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job status=%d, want 404", resp.StatusCode)
	}
	resp = do(t, s, http.MethodPost, "/v1/plans/plan-nope/comments", testToken, map[string]string{"body": "hi"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown plan status=%d, want 404", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/plans/plan-nope/comments", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("list unknown plan status=%d, want 404", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/jobs/job-nope/comments", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("list unknown job status=%d, want 404", resp.StatusCode)
	}
	// The nested route refuses a todo id that is not that plan's.
	resp = do(t, s, http.MethodPost, "/v1/plans/plan-comments/todos/todo-nope/comments", testToken, map[string]string{"body": "hi"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown todo status=%d, want 404", resp.StatusCode)
	}
}

// TestCommentPostRejectsWorkerCaller: an executing machine (a worker token) cannot
// speak in a thread — a comment can start work, and only a person starts work.
// Reading stays open: a worker mirrors the job it runs, timeline included.
func TestCommentPostRejectsWorkerCaller(t *testing.T) {
	const workerToken = "worker-secret"
	s := newCommentServer(t, testToken, map[string]config.WorkerAuthConfig{"w-1": {Token: workerToken}})
	src := createdJobID(t, s, job.JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", TimeoutSec: 30,
	})

	resp := do(t, s, http.MethodPost, "/v1/jobs/"+src+"/comments", workerToken, map[string]string{"body": "hi"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("worker comment status=%d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if resp = do(t, s, http.MethodGet, "/v1/jobs/"+src+"/comments", workerToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("worker list status=%d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestCommentEmptyThreadShape guards the JSON contract the web client reads: the list
// endpoint answers an empty array (never null) for a thread nobody used.
func TestCommentEmptyThreadShape(t *testing.T) {
	s := newCommentServer(t, testToken, nil)
	src := createdJobID(t, s, job.JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", TimeoutSec: 30,
	})
	resp := do(t, s, http.MethodGet, "/v1/jobs/"+src+"/comments", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var raw map[string]json.RawMessage
	decode(t, resp, &raw)
	if string(raw["comments"]) != "[]" {
		t.Fatalf("comments = %s, want []", raw["comments"])
	}
}
