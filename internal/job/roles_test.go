package job

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newRoleService builds a Service like newClaudeInjectService but with an E35
// `reviewer` role preset (base claude + a resident system prompt + project/tags).
// The claude agent's command is the harmless `echo` so jobs run without a real CLI.
func newRoleService(t *testing.T, root string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"claude", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"claude": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}},
		},
		Roles: map[string]config.RoleConfig{
			"reviewer": {
				Agent:        "claude",
				SystemPrompt: "You are a strict reviewer",
				Project:      "self",
				Tags:         []string{"review"},
			},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return NewService(cfg, projReg, agentReg, runners, meta, nil)
}

// renderedArgs unmarshals a finished job's RenderedCommand JSON into its argv.
func renderedArgs(t *testing.T, rc string) []string {
	t.Helper()
	var v struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(rc), &v); err != nil {
		t.Fatalf("RenderedCommand not valid JSON: %v (%q)", err, rc)
	}
	return v.Args
}

// argvHasPair reports whether argv contains flag immediately followed by val.
func argvHasPair(argv []string, flag, val string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return true
		}
	}
	return false
}

// TestSubmitRoleFillsDefaults: a --role reviewer submit (no agent/project/tags/
// system_prompt) inherits all four from the preset, and the resident system prompt
// is injected into argv via claude --append-system-prompt.
func TestSubmitRoleFillsDefaults(t *testing.T) {
	root := t.TempDir()
	s := newRoleService(t, root)

	res, err := s.Submit(JobRequest{Role: "reviewer", Runner: "local", Prompt: "hi", Cwd: ".", TimeoutSec: 30})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Agent != "claude" {
		t.Fatalf("role did not fill agent: %q", res.Agent)
	}
	if res.ProjectKey != "self" {
		t.Fatalf("role did not fill project: %q", res.ProjectKey)
	}

	final, _ := s.Wait(res.ID)
	argv := renderedArgs(t, final.RenderedCommand)
	if !argvHasPair(argv, "--append-system-prompt", "You are a strict reviewer") {
		t.Fatalf("argv missing role system prompt injection: %#v", argv)
	}
}

// TestSubmitRoleExplicitWins: explicit request fields override the preset's
// defaults (system_prompt here); the explicit value is what reaches argv.
func TestSubmitRoleExplicitWins(t *testing.T) {
	root := t.TempDir()
	s := newRoleService(t, root)

	res, err := s.Submit(JobRequest{
		Role: "reviewer", Runner: "local", Prompt: "hi", Cwd: ".", TimeoutSec: 30,
		SystemPrompt: "OVERRIDE prompt",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final, _ := s.Wait(res.ID)
	argv := renderedArgs(t, final.RenderedCommand)
	if !argvHasPair(argv, "--append-system-prompt", "OVERRIDE prompt") {
		t.Fatalf("explicit system prompt did not win: %#v", argv)
	}
	if argvHasPair(argv, "--append-system-prompt", "You are a strict reviewer") {
		t.Fatalf("preset system prompt leaked despite explicit override: %#v", argv)
	}
}

// TestSubmitUnknownRole: an unknown role is rejected with ErrUnknownRole.
func TestSubmitUnknownRole(t *testing.T) {
	root := t.TempDir()
	s := newRoleService(t, root)

	_, err := s.Submit(JobRequest{Role: "ghost", Runner: "local", Prompt: "x", Cwd: ".", TimeoutSec: 30})
	if !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("err = %v, want ErrUnknownRole", err)
	}
}

// TestSubmitSystemInjectWithoutRole: a direct system_prompt (no role) on a claude
// job is injected into argv too (system_inject is independent of the role path).
func TestSubmitSystemInjectWithoutRole(t *testing.T) {
	root := t.TempDir()
	s := newRoleService(t, root)

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "hi", Cwd: ".", TimeoutSec: 30, SystemPrompt: "direct sys",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final, _ := s.Wait(res.ID)
	argv := renderedArgs(t, final.RenderedCommand)
	if !argvHasPair(argv, "--append-system-prompt", "direct sys") {
		t.Fatalf("direct system prompt not injected: %#v", argv)
	}
}

// TestResumeDoesNotReinjectSystemPrompt: resuming a role/system-prompt job does NOT
// re-apply --append-system-prompt onto the resume argv. 实测定稿 2026-06-28 (claude-cli
// 2.1.191): `claude --resume <sid>` natively restores the system prompt set on the
// source session, so re-injecting it would only double the prompt (see resume.go).
func TestResumeDoesNotReinjectSystemPrompt(t *testing.T) {
	root := t.TempDir()
	s := newRoleService(t, root)

	src, err := s.Submit(JobRequest{Role: "reviewer", Runner: "local", Prompt: "first", Cwd: ".", TimeoutSec: 30})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, ok := s.Wait(src.ID); !ok {
		t.Fatal("source job not found after wait")
	}

	resumed, err := s.ResumeJob(src.ID, "next turn", "", "caller")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	final, _ := s.Wait(resumed.ID)
	argv := renderedArgs(t, final.RenderedCommand)
	// Sanity: it IS a resume argv (so the assertion below isn't trivially true on a broken argv).
	if !argvHasPair(argv, "--resume", src.SessionID) {
		t.Fatalf("resume argv missing --resume %q: %#v", src.SessionID, argv)
	}
	// The role system prompt must NOT be re-appended (claude --resume restores it natively).
	if argvHasPair(argv, "--append-system-prompt", "You are a strict reviewer") {
		t.Fatalf("resume should NOT re-inject role system prompt (claude --resume restores it): %#v", argv)
	}
}
