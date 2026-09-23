package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
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
)

// SEC-01 acceptance for the HTTP permission table (jobcredential.go): what a member
// and a leader job credential may and may not do, and that `as_job` no longer decides
// anything.

// seedJobToken registers a live credential for jobID and returns the plaintext. The
// row is written through the store's own seam — the same row the job service mints on
// a real run (TestJobTokenIssuedAndRevoked proves that half) — so a test can pin the
// identity it is exercising without waiting on a live job.
func seedJobToken(t *testing.T, s *Server, jobID, kind, planID string) string {
	t.Helper()
	token := "gjt_" + jobID + "_" + strings.Repeat("0123456789abcdef", 2)
	sum := sha256.Sum256([]byte(token))
	if err := s.jobs.Meta().UpsertJobToken(jobstore.JobTokenRecord{
		JobID:     jobID,
		TokenHash: hex.EncodeToString(sum[:]),
		Kind:      kind,
		PlanID:    planID,
		ExpiresAt: time.Now().Unix() + 3600,
		CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("seed job token: %v", err)
	}
	return token
}

// newCredentialServer builds a Server whose config carries explicit agent/role
// definitions, so the SEC-01 submit rule (can_submit / submit_agents) can be
// exercised. sc is layered onto the same single "self" project newTestServerCfg uses;
// agents/roles are added on top.
func newCredentialServer(t *testing.T, sc config.ServerConfig, agents map[string]config.AgentConfig, roles map[string]config.RoleConfig) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  sc,
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec", "omp"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: agents,
		Roles:  roles,
	}
	projects := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agentReg, runners, st, nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, sc.Token, sc.AllowEmptyToken, jobs, eng, projects, agentReg, nil, nil, nil, nil)
}

// submitExecJob submits a runnable exec job with the given token and waits for it, so
// the test has a real job row (its agent key, project) to hang a credential on.
func submitExecJob(t *testing.T, s *Server, token string) job.JobResult {
	t.Helper()
	created, status := createJob(t, s, token)
	if status != http.StatusOK {
		t.Fatalf("submit job status=%d", status)
	}
	return waitDoneTok(t, s, created.ID, token)
}

// TestMemberTokenPermissions is the member half of the SEC-01 table: reading works,
// commenting/asking/waking-itself work, and every privileged write is refused with the
// documented message.
func TestMemberTokenPermissions(t *testing.T) {
	const userTok = "tok-user"
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userTok}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)

	member := submitExecJob(t, s, userTok)
	victim := submitExecJob(t, s, userTok)
	tok := seedJobToken(t, s, member.ID, jobstore.JobCredentialMember, "")

	// --- reads pass ------------------------------------------------------------
	for _, path := range []string{"/v1/jobs/" + member.ID, "/v1/jobs", "/v1/plans"} {
		if resp := do(t, s, http.MethodGet, path, tok, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s as a member job = %d, want 200", path, resp.StatusCode)
		}
	}

	// --- comment: authored by the JOB's agent, and as_job cannot change that -----
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+member.ID+"/comments", tok,
		map[string]string{"body": "progress", "as_job": victim.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member comment status=%d, want 200", resp.StatusCode)
	}
	var cv commentView
	decode(t, resp, &cv)
	if cv.AuthorKind != jobstore.CommentAuthorAgent || cv.Author != member.Agent {
		t.Fatalf("member comment author = %s/%s, want %s/agent (as_job must be ignored)",
			cv.Author, cv.AuthorKind, member.Agent)
	}

	// --- ask a human, and register a wakeup on ITSELF ---------------------------
	if resp := do(t, s, http.MethodPost, "/v1/decisions", tok,
		map[string]any{"title": "which way?", "question": "left or right?"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("ask_human as a member job = %d, want 200", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/jobs/"+member.ID+"/wakeups", tok,
		map[string]any{"kind": "at", "at": time.Now().Unix() + 60}); resp.StatusCode != http.StatusOK {
		t.Fatalf("own-job wakeup as a member job = %d, want 200", resp.StatusCode)
	}
	// …but not on ANOTHER job: the credential reaches only its own.
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+victim.ID+"/wakeups", tok, map[string]any{"kind": "at", "at": time.Now().Unix() + 60})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wakeup on another job = %d, want 403", resp.StatusCode)
	}
	assertJobCredentialRefusal(t, resp)

	// --- everything privileged is refused --------------------------------------
	denied := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/jobs", job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: "."}},
		{http.MethodPost, "/v1/jobs/" + victim.ID + "/cancel", nil},
		{http.MethodPost, "/v1/jobs/" + victim.ID + "/accept", map[string]any{"note": "ok"}},
		{http.MethodPost, "/v1/jobs/" + victim.ID + "/reject", map[string]any{"note": "no"}},
		{http.MethodPut, "/v1/config/server", map[string]any{"max_job_timeout_sec": 60}},
		{http.MethodPost, "/v1/skills/import", map[string]any{"name": "x"}},
		{http.MethodPost, "/v1/xfer", map[string]any{"op": "put"}},
		{http.MethodPut, "/v1/xfer/x1/content", nil},
	}
	for _, d := range denied {
		resp := do(t, s, d.method, d.path, tok, d.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s as a member job = %d, want 403", d.method, d.path, resp.StatusCode)
		}
		assertJobCredentialRefusal(t, resp)
	}
}

// TestMemberTokenCannotMoveTodo: the checklist is a leader's tool, not a member's.
func TestMemberTokenCannotMoveTodo(t *testing.T) {
	const userTok = "tok-user"
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userTok}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)

	member := submitExecJob(t, s, userTok)
	tok := seedJobToken(t, s, member.ID, jobstore.JobCredentialMember, "")

	planID, todoID := newPlanWithTodo(t, s, userTok, "")
	resp := do(t, s, http.MethodPatch, "/v1/todos/"+todoID, tok, map[string]any{"status": "ready"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member set-todo = %d, want 403 (plan %s)", resp.StatusCode, planID)
	}
	assertJobCredentialRefusal(t, resp)
}

// TestLeaderTokenPermissions: a leader may move its OWN plan's items to ready/skipped
// and nothing else — no other status, no other plan, no verdict, no submit.
func TestLeaderTokenPermissions(t *testing.T) {
	const userTok = "tok-user"
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userTok}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}, "omp": {Type: agent.TypeCLIAgent, Command: "omp"}}, nil)

	planID, todoID := newPlanWithTodo(t, s, userTok, "")
	otherPlan, otherTodo := newPlanWithTodo(t, s, userTok, "")

	leader := submitExecJob(t, s, userTok)
	tok := seedJobToken(t, s, leader.ID, jobstore.JobCredentialLeader, planID)

	// ready and skipped are the two moves the leader round is given.
	for _, status := range []string{"ready", "skipped"} {
		resp := do(t, s, http.MethodPatch, "/v1/todos/"+todoID, tok, map[string]any{"status": status})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("leader set-todo %s = %d, want 200", status, resp.StatusCode)
		}
	}
	// Any other status is a human's or the chain's decision.
	resp := do(t, s, http.MethodPatch, "/v1/todos/"+todoID, tok, map[string]any{"status": "done"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("leader set-todo done = %d, want 403", resp.StatusCode)
	}
	assertJobCredentialRefusal(t, resp)
	// …and another plan's item is out of scope entirely.
	resp = do(t, s, http.MethodPatch, "/v1/todos/"+otherTodo, tok, map[string]any{"status": "ready"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("leader set-todo on plan %s = %d, want 403", otherPlan, resp.StatusCode)
	}
	assertJobCredentialRefusal(t, resp)
	// A leader does not start work and does not judge deliveries.
	for _, d := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/jobs", job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: "."}},
		{http.MethodPost, "/v1/jobs/" + leader.ID + "/accept", map[string]any{"note": "ok"}},
	} {
		resp := do(t, s, d.method, d.path, tok, d.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s as a leader job = %d, want 403", d.method, d.path, resp.StatusCode)
		}
		assertJobCredentialRefusal(t, resp)
	}
}

// TestUserCallerAsJobIgnored: `as_job` is retired — a user caller that still sends it
// is treated as the user, both for a comment's authorship (so it still dispatches) and
// for the review path (a leader job can no longer wave itself through by naming a job).
func TestUserCallerAsJobIgnored(t *testing.T) {
	const userTok = "tok-user"
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userTok}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)

	other := submitExecJob(t, s, userTok)

	resp := do(t, s, http.MethodPost, "/v1/jobs/"+other.ID+"/comments", userTok,
		map[string]string{"body": "@exec hello", "as_job": other.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("user comment status=%d, want 200", resp.StatusCode)
	}
	var cv commentView
	decode(t, resp, &cv)
	if cv.AuthorKind != jobstore.CommentAuthorUser || cv.Author != "alice" {
		t.Fatalf("user comment with as_job = %s/%s, want alice/user — as_job must be ignored", cv.Author, cv.AuthorKind)
	}

	// The review path ignores it too: the same request is the ordinary human path.
	reviewed := submitReviewedJob(t, s, userTok)
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+reviewed.ID+"/accept", userTok,
		map[string]any{"note": "looks good", "as_job": reviewed.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept with as_job = %d, want 200 (as_job must be ignored)", resp.StatusCode)
	}
}

// assertJobCredentialRefusal checks the documented 403 body: `job credential may not …`.
func assertJobCredentialRefusal(t *testing.T, resp *http.Response) {
	t.Helper()
	defer resp.Body.Close()
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode refusal body: %v", err)
	}
	if !strings.Contains(body.Error, "job credential may not") {
		t.Fatalf("refusal %q does not say the credential is the reason", body.Error)
	}
}

// newPlanWithTodo creates a plan (with a caller-chosen id when given) and one
// checklist item on it, returning both ids.
func newPlanWithTodo(t *testing.T, s *Server, token, planID string) (string, string) {
	t.Helper()
	if planID == "" {
		planID = "plan-sec01-" + job.RandomSuffix() + job.RandomSuffix()
	}
	resp := do(t, s, http.MethodPost, "/v1/plans", token, map[string]any{"plan_id": planID, "project": "self"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan = %d", resp.StatusCode)
	}
	var plan struct {
		PlanID string `json:"plan_id"`
	}
	decode(t, resp, &plan)
	resp = do(t, s, http.MethodPost, "/v1/plans/"+plan.PlanID+"/todos", token, map[string]any{"title": "step"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add todo = %d", resp.StatusCode)
	}
	var todo struct {
		TodoID string `json:"todo_id"`
	}
	decode(t, resp, &todo)
	return plan.PlanID, todo.TodoID
}

// submitReviewedJob submits an exec job that finishes into needs_review (the project
// has no require_review default here, so the request asks for it).
func submitReviewedJob(t *testing.T, s *Server, token string) job.JobResult {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", token, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30, Review: true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit reviewed job = %d", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	return waitReviewTok(t, s, created.ID, token)
}

// TestMemberCanSubmitWhenAllowed is the SEC-01 submit half: a member job may start
// work only when its agent (or role) opens can_submit, only in its own project, and
// only for the agents its definition lists (default [exec]); the new job carries the
// `submitted_by_job:<id>` provenance tag.
func TestMemberCanSubmitWhenAllowed(t *testing.T) {
	const userTok = "tok-user"

	t.Run("agent_grant", func(t *testing.T) {
		s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userTok}}},
			map[string]config.AgentConfig{
				"exec": {Type: agent.TypeExec, CanSubmit: true},
				"omp":  {Type: agent.TypeCLIAgent, Command: "omp"},
			}, nil)
		assertSubmitRule(t, s, userTok)
	})

	t.Run("role_grant", func(t *testing.T) {
		// The asking job ran under a ROLE that opens the gate; its agent does not.
		s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userTok}}},
			map[string]config.AgentConfig{
				"exec": {Type: agent.TypeExec},
				"omp":  {Type: agent.TypeCLIAgent, Command: "omp"},
			}, map[string]config.RoleConfig{"sup": {Agent: "exec", CanSubmit: true}})

		asking := submitRoleExecJob(t, s, userTok, "sup")
		tok := seedJobToken(t, s, asking.ID, jobstore.JobCredentialMember, "")
		assertSubmitAllowed(t, s, tok, asking, "exec")
		assertSubmitRefused(t, s, tok, "omp")
	})

	t.Run("no_grant", func(t *testing.T) {
		s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userTok}}},
			map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)
		asking := submitExecJob(t, s, userTok)
		tok := seedJobToken(t, s, asking.ID, jobstore.JobCredentialMember, "")
		resp := do(t, s, http.MethodPost, "/v1/jobs", tok, map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local", "cmd": []string{"go", "version"}, "cwd": ".",
		})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("submit without can_submit = %d, want 403", resp.StatusCode)
		}
		assertJobCredentialRefusal(t, resp)
	})
}

// assertSubmitRule drives the two positive halves of the agent-grant case: the allowed
// submit succeeds (and is tagged), the agent outside submit_agents does not.
func assertSubmitRule(t *testing.T, s *Server, userTok string) {
	t.Helper()
	asking := submitExecJob(t, s, userTok)
	tok := seedJobToken(t, s, asking.ID, jobstore.JobCredentialMember, "")
	created := assertSubmitAllowed(t, s, tok, asking, "exec")
	assertSubmitRefused(t, s, tok, "omp")
	_ = created
}

// assertSubmitAllowed submits an exec job as the job credential and returns the created
// job, asserting the provenance tag.
func assertSubmitAllowed(t *testing.T, s *Server, tok string, asking job.JobResult, agentKey string) job.JobResult {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", tok, map[string]any{
		"project_key": "self", "agent": agentKey, "runner": "local",
		"cmd": []string{"go", "version"}, "cwd": ".",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit %s as a granted member job = %d", agentKey, resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	want := "submitted_by_job:" + asking.ID
	for _, tag := range created.Tags {
		if tag == want {
			return created
		}
	}
	t.Fatalf("job %s tags = %v, want %s", created.ID, created.Tags, want)
	return created
}

// assertSubmitRefused proves an agent outside submit_agents is refused.
func assertSubmitRefused(t *testing.T, s *Server, tok, agentKey string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", tok, map[string]any{
		"project_key": "self", "agent": agentKey, "runner": "local",
		"prompt": "hi", "cwd": ".",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("submit %s (outside submit_agents) = %d, want 403", agentKey, resp.StatusCode)
	}
	assertJobCredentialRefusal(t, resp)
}

// submitRoleExecJob submits an exec job under an E35 role and waits for it.
func submitRoleExecJob(t *testing.T, s *Server, token, role string) job.JobResult {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", token, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Role: role, Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit role job = %d", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	return waitDoneTok(t, s, created.ID, token)
}
