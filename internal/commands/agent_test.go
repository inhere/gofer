package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/gcli/v3"
)

// TestAgentCmdSubsRegistered verifies the agent group registers list/detect/show.
func TestAgentCmdSubsRegistered(t *testing.T) {
	cmd := NewAgentCmd()
	if cmd.Name != "agent" {
		t.Fatalf("unexpected name %q", cmd.Name)
	}
	want := map[string]bool{"list": false, "detect": false, "show": false}
	for _, sub := range cmd.Subs {
		if _, ok := want[sub.Name]; ok {
			want[sub.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing agent sub-command %q", name)
		}
	}
}

// TestAgentListDetectShow drives the runner funcs against a temp config,
// exercising flag binding and verifying the built-in exec is listed.
func TestAgentListDetectShow(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "bridge.yaml")
	yaml := `
projects:
  self:
    host_path: /tmp
    allowed_agents: [codex, exec]
agents:
  codex:
    type: cli-agent
    command: codex
    args: [exec, "{{prompt}}"]
    detect:
      command: __no_such_cli_xyz__
      args: [--version]
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := NewAgentCmd()
	showCmd := findSub(t, cmd, "show")
	c := gcli.NewCommand(showCmd.Name, showCmd.Desc, nil)
	if showCmd.Config != nil {
		showCmd.Config(c)
	}

	agentOpts.listConfig = cfgPath
	if err := runAgentList(c, nil); err != nil {
		t.Fatalf("list: %v", err)
	}

	// detect must succeed (exit 0) even though codex's detect CLI is missing.
	agentOpts.detectConfig = cfgPath
	if err := runAgentDetect(c, nil); err != nil {
		t.Fatalf("detect should not fail the command for missing CLI: %v", err)
	}

	// show codex (config-declared) and exec (built-in) both succeed.
	agentOpts.showConfig = cfgPath
	if err := runAgentShow(c, []string{"codex"}); err != nil {
		t.Fatalf("show codex: %v", err)
	}
	if err := runAgentShow(c, []string{"exec"}); err != nil {
		t.Fatalf("show exec (built-in): %v", err)
	}
	if err := runAgentShow(c, []string{"ghost"}); err == nil {
		t.Fatal("show of unknown agent should fail")
	}
}
