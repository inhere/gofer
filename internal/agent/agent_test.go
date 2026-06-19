package agent

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// testConfig builds a config with codex (cli-agent) and an explicit exec entry.
func testConfig() *config.Config {
	return &config.Config{
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:      "/tmp/self",
				AllowedAgents: []string{"codex", "exec"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Type:    TypeCLIAgent,
				Command: "codex",
				Args:    []string{"exec", "{{prompt}}"},
				Detect:  config.DetectConfig{Command: "codex", Args: []string{"--version"}},
			},
		},
	}
}

// TestRenderKeepsArgvForPromptWithSpacesAndQuotes is the core security check:
// a prompt with spaces/quotes must stay a SINGLE argv element (plan §11).
func TestRenderKeepsArgvForPromptWithSpacesAndQuotes(t *testing.T) {
	prompt := `fix the bug "x" now`
	got := Render([]string{"exec", "{{prompt}}"}, Vars{Prompt: prompt})
	if len(got) != 2 {
		t.Fatalf("argv length = %d, want 2 (no shell tokenisation): %#v", len(got), got)
	}
	if got[0] != "exec" {
		t.Errorf("argv[0] = %q, want %q", got[0], "exec")
	}
	if got[1] != prompt {
		t.Errorf("argv[1] = %q, want the whole prompt %q as one element", got[1], prompt)
	}
}

// TestRenderAllPlaceholders verifies every supported placeholder substitutes.
func TestRenderAllPlaceholders(t *testing.T) {
	got := Render(
		[]string{"{{prompt}}", "{{cwd}}", "{{job_id}}", "{{result_dir}}"},
		Vars{Prompt: "P", Cwd: "C", JobID: "J", ResultDir: "R"},
	)
	want := []string{"P", "C", "J", "R"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestBuildCodexKeepsArgv drives the adapter end-to-end for the codex agent.
func TestBuildCodexKeepsArgv(t *testing.T) {
	reg := NewRegistry(testConfig())
	prompt := `fix the bug "x" now`
	res, err := reg.Build("codex", prompt, nil, Vars{})
	if err != nil {
		t.Fatalf("build codex: %v", err)
	}
	if res.Command != "codex" {
		t.Errorf("command = %q, want codex", res.Command)
	}
	if len(res.Args) != 2 || res.Args[0] != "exec" || res.Args[1] != prompt {
		t.Errorf("args = %#v, want [exec, %q]", res.Args, prompt)
	}
}

// TestBuildCLIAgentEmptyPromptFails: cli-agent with empty prompt must error.
func TestBuildCLIAgentEmptyPromptFails(t *testing.T) {
	reg := NewRegistry(testConfig())
	if _, err := reg.Build("codex", "", nil, Vars{}); err == nil {
		t.Fatal("expected error for empty prompt on cli-agent, got nil")
	}
}

// TestBuildExecNoCmdFails: exec agent with no cmd must error.
func TestBuildExecNoCmdFails(t *testing.T) {
	reg := NewRegistry(testConfig())
	if _, err := reg.Build("exec", "", nil, Vars{}); err == nil {
		t.Fatal("expected error for exec agent with no cmd, got nil")
	}
}

// TestBuildExecWithCmdReturnsArgv: exec agent returns the request argv verbatim.
func TestBuildExecWithCmdReturnsArgv(t *testing.T) {
	reg := NewRegistry(testConfig())
	res, err := reg.Build("exec", "", []string{"go", "version"}, Vars{})
	if err != nil {
		t.Fatalf("build exec: %v", err)
	}
	if res.Command != "go" {
		t.Errorf("command = %q, want go", res.Command)
	}
	if len(res.Args) != 1 || res.Args[0] != "version" {
		t.Errorf("args = %#v, want [version]", res.Args)
	}
}

// TestExecIsBuiltinWithoutConfig: Get("exec") resolves with no config entry.
func TestExecIsBuiltinWithoutConfig(t *testing.T) {
	reg := NewRegistry(&config.Config{})
	ac, ok := reg.Get("exec")
	if !ok {
		t.Fatal("built-in exec agent should resolve without config")
	}
	if ac.Type != TypeExec {
		t.Errorf("built-in exec type = %q, want %q", ac.Type, TypeExec)
	}
	// And it should appear in Names().
	found := false
	for _, n := range reg.Names() {
		if n == "exec" {
			found = true
		}
	}
	if !found {
		t.Error("exec missing from Names()")
	}
}

// TestCheckAllowedRejectsDisallowedAgent: project allowlist is enforced.
func TestCheckAllowedRejectsDisallowedAgent(t *testing.T) {
	cfg := testConfig()
	// claude is not in self's allowed_agents.
	if err := CheckAllowed(cfg, "self", "claude"); err == nil {
		t.Fatal("expected disallowed agent to fail")
	}
	// codex and exec are allowed.
	if err := CheckAllowed(cfg, "self", "codex"); err != nil {
		t.Errorf("codex should be allowed: %v", err)
	}
	if err := CheckAllowed(cfg, "self", "exec"); err != nil {
		t.Errorf("exec should be allowed when listed: %v", err)
	}
	// unknown project fails.
	if err := CheckAllowed(cfg, "ghost", "codex"); err == nil {
		t.Fatal("expected unknown project to fail")
	}
}

// TestCheckAllowedExecNotExempt: exec is NOT auto-allowed; it must be listed.
func TestCheckAllowedExecNotExempt(t *testing.T) {
	cfg := &config.Config{
		Projects: map[string]config.ProjectConfig{
			"noexec": {HostPath: "/tmp/x", AllowedAgents: []string{"codex"}},
		},
	}
	if err := CheckAllowed(cfg, "noexec", "exec"); err == nil {
		t.Fatal("exec must not bypass the project allowlist")
	}
}

// TestDetectMissingCLI: a non-existent CLI yields unavailable without panic.
func TestDetectMissingCLI(t *testing.T) {
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"ghost": {
				Type:    TypeCLIAgent,
				Command: "__no_such_cli_xyz__",
				Detect:  config.DetectConfig{Command: "__no_such_cli_xyz__", Args: []string{"--version"}},
			},
		},
	}
	reg := NewRegistry(cfg)
	res := reg.Detect("ghost")
	if res.Available {
		t.Fatal("missing CLI should be unavailable")
	}
	if res.Error == "" {
		t.Error("missing CLI detect should report an error")
	}
}

// TestDetectExistingCLI: a present command yields available + non-empty version.
func TestDetectExistingCLI(t *testing.T) {
	// Use `go version` which is guaranteed available in this environment.
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"goagent": {
				Type:    TypeCLIAgent,
				Command: "go",
				Detect:  config.DetectConfig{Command: "go", Args: []string{"version"}},
			},
		},
	}
	reg := NewRegistry(cfg)
	res := reg.Detect("goagent")
	if !res.Available {
		t.Fatalf("go should be available: %+v", res)
	}
	if !strings.Contains(res.Version, "go") {
		t.Errorf("version = %q, expected to contain 'go'", res.Version)
	}
}

// TestDetectBuiltinExec: the built-in exec agent reports available.
func TestDetectBuiltinExec(t *testing.T) {
	reg := NewRegistry(&config.Config{})
	res := reg.Detect("exec")
	if !res.Available {
		t.Fatalf("built-in exec should be available: %+v", res)
	}
}

// TestDetectUnknownAgent: unknown agent -> unavailable with error, no panic.
func TestDetectUnknownAgent(t *testing.T) {
	reg := NewRegistry(&config.Config{})
	res := reg.Detect("nope")
	if res.Available || res.Error == "" {
		t.Fatalf("unknown agent should be unavailable with error: %+v", res)
	}
}
