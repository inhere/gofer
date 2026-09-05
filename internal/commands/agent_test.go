package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
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
	list := findSub(t, cmd, "list")
	bound := gcli.NewCommand(list.Name, list.Desc, nil)
	list.Config(bound)
	if bound.Opts()["runner"] == nil {
		t.Fatal("agent list should expose --runner")
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
	// argKey reads the gcli-bound <key> arg; supply it via CliArg per call.
	setArg := func(name, val string) {
		if a := c.Arg(name); a != nil {
			a.WithValue(val)
		}
	}

	// The config path is the app-level global -c (config.InputCfgFile); reset it
	// after the test so the package-level global never leaks into other tests.
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })

	if err := runAgentList(c, nil); err != nil {
		t.Fatalf("list: %v", err)
	}

	// detect must succeed (exit 0) even though codex's detect CLI is missing.
	if err := runAgentDetect(c, nil); err != nil {
		t.Fatalf("detect should not fail the command for missing CLI: %v", err)
	}

	// show codex (config-declared) and exec (built-in) both succeed.
	setArg("key", "codex")
	if err := runAgentShow(c, nil); err != nil {
		t.Fatalf("show codex: %v", err)
	}
	setArg("key", "exec")
	if err := runAgentShow(c, nil); err != nil {
		t.Fatalf("show exec (built-in): %v", err)
	}
	setArg("key", "ghost")
	if err := runAgentShow(c, nil); err == nil {
		t.Fatal("show of unknown agent should fail")
	}
}

func TestAgentListRemoteServerAndRunner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": []any{map[string]any{"key": "claude", "type": "cli-agent", "available": true, "version": "1.0"}}})
		case "/v1/runners":
			_ = json.NewEncoder(w).Encode(map[string]any{"runners": []any{map[string]any{"name": "w1", "type": "worker", "status": "connected", "capabilities": map[string]any{"agent_caps": []any{map[string]any{"key": "claude", "type": "cli-agent", "available": true}}}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	jobConnOpts.server = server.URL
	jobConnOpts.token = ""
	defer func() { jobConnOpts.server, jobConnOpts.token = "", "" }()
	c := bindCmd(NewAgentCmd().Subs[0])
	if err := runAgentListRemote(c, "server"); err != nil {
		t.Fatalf("server agent list: %v", err)
	}
	if err := runAgentListRemote(c, "w1"); err != nil {
		t.Fatalf("runner agent list: %v", err)
	}
}
