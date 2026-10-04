package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// newWakeServer builds a server whose "self" project allows interactive jobs and
// whose claude agent has an interactive resume template, over a REAL project dir.
func newWakeServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	allow := true
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: filepath.Join(root, "store")},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"claude"}, AllowInteractive: &allow},
		},
		Agents: map[string]config.AgentConfig{
			"claude": {Type: agent.TypeCLIAgent, Command: "claude",
				SessionResumeInteractive: []string{"--resume", "{{session_id}}"}},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, cfg.Runners, nil, nil), root
}

func takeoverPlan(t *testing.T, s *Server, sid string) sessionrelay.ResumePlan {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/sessions/"+sid+"/takeover-plan", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("takeover-plan status=%d", resp.StatusCode)
	}
	var plan sessionrelay.ResumePlan
	decode(t, resp, &plan)
	return plan
}

// TestTakeoverPlanUsesOriginalDirWhenWorktreeDeleted: the session registered from a
// temporary worktree that no longer exists, but its transcript lives under the
// directory name of the directory it was started in — the plan must point there.
func TestTakeoverPlanUsesOriginalDirWhenWorktreeDeleted(t *testing.T) {
	t.Parallel()
	s, root := newWakeServer(t)
	origin := filepath.Join(root, "pkg", "app")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(origin, ".worktrees", "tmp-1") // never created: "deleted"
	transcript := filepath.Join(t.TempDir(), ".claude", "projects",
		sessionrelay.EncodeClaudeProjectDir(origin), "sid-wake.jsonl")

	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "sid-wake", "agent": "claude", "runner": "server", "cwd": gone,
		"transcript": transcript, "event": "SessionStart"})
	resp.Body.Close()

	plan := takeoverPlan(t, s, "sid-wake")
	if !plan.Can {
		t.Fatalf("plan=%+v, want can=true", plan)
	}
	if plan.CwdSource != sessionrelay.CwdSourceTranscript || plan.CwdAbs != origin || plan.Cwd != "pkg/app" {
		t.Fatalf("cwd=%q abs=%q source=%q, want the transcript-verified original %q",
			plan.Cwd, plan.CwdAbs, plan.CwdSource, origin)
	}
	if !strings.Contains(plan.CwdReason, "会话文件") || !strings.Contains(plan.Message, plan.CwdReason) {
		t.Fatalf("reason %q should explain the choice and be part of the message %q", plan.CwdReason, plan.Message)
	}
	// One conclusion + one basis: the path lives in the message only, not again in the reason.
	if strings.Contains(plan.CwdReason, plan.CwdAbs) || strings.Count(plan.Message, plan.CwdAbs) != 1 {
		t.Fatalf("the directory should appear once (message %q, reason %q)", plan.Message, plan.CwdReason)
	}
}

// TestTakeoverPlanFallsBackToProjectRoot: nothing verifies and the registered cwd is
// gone — the project root is the last resort, and the text says so.
func TestTakeoverPlanFallsBackToProjectRoot(t *testing.T) {
	t.Parallel()
	s, root := newWakeServer(t)
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "sid-root", "agent": "claude", "runner": "local",
		"cwd": filepath.Join(root, ".worktrees", "gone"), "event": "SessionStart"})
	resp.Body.Close()
	plan := takeoverPlan(t, s, "sid-root")
	if !plan.Can || plan.CwdSource != sessionrelay.CwdSourceProjectRoot || plan.CwdAbs != root || plan.Cwd != "." {
		t.Fatalf("plan=%+v, want the project root fallback", plan)
	}
	if !strings.Contains(plan.CwdReason, "已不存在") {
		t.Fatalf("reason %q should say the registered directory is gone", plan.CwdReason)
	}
}

// TestTakeoverPlanUsesRegisteredDirWhenItExists: the common case is unchanged.
func TestTakeoverPlanUsesRegisteredDirWhenItExists(t *testing.T) {
	t.Parallel()
	s, root := newWakeServer(t)
	dir := filepath.Join(root, "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "sid-reg", "agent": "claude", "runner": "server", "cwd": dir, "event": "SessionStart"})
	resp.Body.Close()
	plan := takeoverPlan(t, s, "sid-reg")
	if !plan.Can || plan.CwdSource != sessionrelay.CwdSourceRegistered || plan.CwdAbs != dir || plan.Cwd != "sub" {
		t.Fatalf("plan=%+v, want the registered directory", plan)
	}
}

// TestHeartbeatRecordsLastCwdForDisplayOnly: a beat's cwd becomes last_cwd, and the
// registered cwd (what the wake-up logic reads) is untouched.
func TestHeartbeatRecordsLastCwdForDisplayOnly(t *testing.T) {
	t.Parallel()
	s, root := newWakeServer(t)
	registered := filepath.Join(root, "reg")
	current := filepath.Join(root, "now")
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "sid-beat", "agent": "claude", "runner": "server", "cwd": registered, "event": "SessionStart"})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-beat/heartbeat", testToken, map[string]any{
		"event": "Stop", "cwd": current})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status=%d", resp.StatusCode)
	}
	var sv sessionView
	decode(t, resp, &sv)
	if sv.LastCwd != current || sv.Cwd != registered {
		t.Fatalf("last_cwd=%q cwd=%q, want last_cwd=%q and the registered cwd %q unchanged", sv.LastCwd, sv.Cwd, current, registered)
	}
	// A beat from the registered directory adds nothing to show.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-beat/heartbeat", testToken, map[string]any{
		"event": "Stop", "cwd": registered})
	var again sessionView
	decode(t, resp, &again)
	if again.LastCwd != "" || again.Cwd != registered {
		t.Fatalf("last_cwd=%q cwd=%q, want no extra directory to show", again.LastCwd, again.Cwd)
	}
}
