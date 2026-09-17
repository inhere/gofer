package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// httpTakeoverer stands in for the host behind path B (§9.1 B): the HTTP contract
// is about routing, status codes and the session projection, not about running a
// resumed CLI.
type httpTakeoverer struct {
	plan     sessionrelay.TakeoverPlan
	reqs     []sessionrelay.TakeoverRequest
	res      sessionrelay.TakeoverResult
	err      error
	cancels  []string
	cancelFn func(jobID string) error
}

func (f *httpTakeoverer) PlanTakeover(_, _, _, _ string) sessionrelay.TakeoverPlan {
	if f.plan.Argv == nil {
		return sessionrelay.TakeoverPlan{
			Argv: []string{"claude", "--resume", "sid"}, AllowInteractive: true, ExecRoot: "/w/repo",
		}
	}
	return f.plan
}

func (f *httpTakeoverer) TakeoverSession(_ context.Context, req sessionrelay.TakeoverRequest) (sessionrelay.TakeoverResult, error) {
	f.reqs = append(f.reqs, req)
	return f.res, f.err
}

func (f *httpTakeoverer) CancelTakeover(_ context.Context, jobID string) error {
	f.cancels = append(f.cancels, jobID)
	if f.cancelFn != nil {
		return f.cancelFn(jobID)
	}
	return nil
}

// registerTakeoverSession registers a session with no OPEN turn (the state that sends
// a web reply down the §9.1 routing) and returns its id.
func registerTakeoverSession(t *testing.T, s *Server, sid, cwd, tmuxPane string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": sid, "agent": "claude", "runner": "server", "cwd": cwd, "tmux_pane": tmuxPane,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register %s status=%d, want 200", sid, resp.StatusCode)
	}
	resp.Body.Close()
}

// sessionState reads back one session's projected state.
func sessionState(t *testing.T, s *Server, sid string) sessionView {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/sessions/"+sid, testToken, nil)
	var detail struct {
		Session sessionView `json:"session"`
	}
	decode(t, resp, &detail)
	return detail.Session
}

// TestSessionDeliverTakeover walks path B over HTTP: allow_takeover is what turns
// a session path A cannot reach into a NEW interactive process, the receipt names
// the job to attach to, and the session reports the takeover (state + job) so the
// web can link to it.
func TestSessionDeliverTakeover(t *testing.T) {
	s := newTestServer(t, testToken, false)
	to := &httpTakeoverer{res: sessionrelay.TakeoverResult{JobID: "job-http-takeover"}}
	s.relay.SetTakeoverer(to)
	registerTakeoverSession(t, s, "sid-http-b", "/w/repo", "")

	// Without the opt-in the session stops at path A's no_tmux refusal: taking a
	// session over starts a second process, so it is never implicit.
	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-http-b/deliver", testToken,
		map[string]any{"text": "carry on", "allow_takeover": false})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("deliver without the opt-in status=%d, want 409", resp.StatusCode)
	}
	var env struct {
		Error string `json:"error"`
	}
	decode(t, resp, &env)
	if env.Error != "deliver failed: no_tmux" {
		t.Fatalf("error=%q, want the no_tmux reason", env.Error)
	}
	if len(to.reqs) != 0 {
		t.Fatalf("a plain deliver must not take the session over: %+v", to.reqs)
	}

	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http-b/deliver", testToken,
		map[string]any{"text": "carry on", "allow_takeover": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("takeover deliver status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		Path       string `json:"path"`
		JobID      string `json:"job_id"`
		DecisionID string `json:"decision_id"`
	}
	decode(t, resp, &out)
	if out.Path != "takeover" || out.JobID != "job-http-takeover" || out.DecisionID == "" {
		t.Fatalf("takeover deliver result=%+v, want path=takeover with the job and audit ids", out)
	}
	if len(to.reqs) != 1 || to.reqs[0].InitialInput != "[gofer web 回复] carry on\r" {
		t.Fatalf("takeover requests=%+v, want the reply primed with the injection prefix", to.reqs)
	}

	got := sessionState(t, s, "sid-http-b")
	if got.State != "handed_off" || got.HandedOffJobID != "job-http-takeover" || got.HandedOffAt == 0 {
		t.Fatalf("session=%+v, want handed_off with the job and timestamp", got)
	}

	// The original terminal gets the explanation on its next hook event.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http-b/heartbeat", testToken,
		map[string]any{"event": "Stop"})
	var beat sessionView
	decode(t, resp, &beat)
	if beat.Notice == "" || !strings.Contains(beat.Notice, "job-http-takeover") {
		t.Fatalf("heartbeat notice=%q, want the takeover explanation naming the job", beat.Notice)
	}
	if beat.WaitReason != "" {
		t.Fatalf("wait_reason=%q, want the handed-off terminal to stop waiting", beat.WaitReason)
	}
}

// TestSessionReleaseTakeover pins the way back over HTTP: the endpoint cancels the
// takeover job (the process that holds the session) and returns it to idle, so the
// original terminal relays again — and a session that is not handed off is refused
// rather than silently "released".
func TestSessionReleaseTakeover(t *testing.T) {
	s := newTestServer(t, testToken, false)
	to := &httpTakeoverer{res: sessionrelay.TakeoverResult{JobID: "job-http-release"}}
	s.relay.SetTakeoverer(to)
	registerTakeoverSession(t, s, "sid-http-release", "/w/repo", "")

	// Nothing to release yet.
	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-http-release/release-takeover", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("release of an idle session status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http-release/deliver", testToken,
		map[string]any{"text": "carry on", "allow_takeover": true})
	resp.Body.Close()

	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http-release/release-takeover", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("release status=%d, want 200", resp.StatusCode)
	}
	var out sessionView
	decode(t, resp, &out)
	if out.State != "idle" || out.HandedOffJobID != "" || out.HandedOffAt != 0 {
		t.Fatalf("released session=%+v, want idle with the takeover cleared", out)
	}
	if len(to.cancels) != 1 || to.cancels[0] != "job-http-release" {
		t.Fatalf("cancels=%v, want the takeover job cancelled once", to.cancels)
	}
	// The session is releasable again in the same process (no stale state).
	if got := sessionState(t, s, "sid-http-release"); got.State != "idle" {
		t.Fatalf("stored session=%+v, want idle after the release", got)
	}
}

// TestSessionInjectorPlanTakeover pins the host-side planning behind path B (§9.1
// B): the argv comes from the AGENT's interactive resume template, the project
// switch decides whether interactive jobs are admitted, and the path root follows
// the runner — a server-run session is rooted at the server's own execution view
// of the project (G002), a worker-run one at the host path, because the pty job
// resolves its cwd on the runner.
func TestSessionInjectorPlanTakeover(t *testing.T) {
	root := t.TempDir()
	container := t.TempDir()
	allow := true
	cfg := &config.Config{
		Server:  config.ServerConfig{PathView: "container"},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, ContainerPath: container, AllowedAgents: []string{"claude"}, AllowInteractive: &allow},
			"no":   {HostPath: root},
		},
		Agents: map[string]config.AgentConfig{
			"claude": {
				Type: agent.TypeCLIAgent, Command: "claude",
				SessionResumeInteractive: []string{"--resume", "{{session_id}}"},
			},
			"plain": {Type: agent.TypeCLIAgent, Command: "plain"},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	x := sessionInjector{jobs: nil, projects: projects, agents: agents}

	plan := x.PlanTakeover("claude", "self", "server", "sid-1")
	if strings.Join(plan.Argv, " ") != "claude --resume sid-1" {
		t.Fatalf("argv=%v, want the agent's interactive resume template", plan.Argv)
	}
	if !plan.AllowInteractive {
		t.Fatal("allow_interactive must be reported from the project")
	}
	// G002: the server executes against its own path view of the project.
	if plan.ExecRoot != container {
		t.Fatalf("ExecRoot=%q, want the container path %q for the server's execution view", plan.ExecRoot, container)
	}
	// A worker runs the pty on ITS filesystem, so the root is the host path.
	if plan := x.PlanTakeover("claude", "self", "w-claude", "sid-1"); plan.ExecRoot != root {
		t.Fatalf("ExecRoot=%q, want the host path %q for a worker runner", plan.ExecRoot, root)
	}
	// An agent without an interactive resume template has nothing to run.
	if plan := x.PlanTakeover("plain", "self", "server", "sid-1"); len(plan.Argv) != 0 {
		t.Fatalf("argv=%v, want none for an agent with no interactive resume template", plan.Argv)
	}
	// A project without the switch admits no interactive job.
	if plan := x.PlanTakeover("claude", "no", "server", "sid-1"); plan.AllowInteractive {
		t.Fatal("allow_interactive must be false for a project that does not set it")
	}
	// An unknown project plans no interactive job: the agent argv is still resolvable,
	// but nothing admits the job, so the relay reports interactive_not_allowed.
	if plan := x.PlanTakeover("claude", "missing", "server", "sid-1"); plan.AllowInteractive || plan.ExecRoot != "" {
		t.Fatalf("plan=%+v, want no admission and no root for an unknown project", plan)
	}
}
