package agent

import (
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestACPAgentModes pins the capability bits of the acp-agent type: it is a BATCH
// agent (one job = one prompt turn) and never claims an interactive mode — pty
// sessions stay the cli-agent type's business (design §一.1).
func TestACPAgentModes(t *testing.T) {
	batch, interactive := Modes(config.AgentConfig{Type: TypeACPAgent, Command: "omp", Args: []string{"acp"}})
	if !batch {
		t.Fatal("acp-agent batch = false, want true")
	}
	if interactive {
		t.Fatal("acp-agent interactive = true, want false (pty stays cli-agent)")
	}
	// Even an acp-agent that carries interactive_args is not interactive: the
	// capability is the type's, not the argv's.
	if batch, interactive := Modes(config.AgentConfig{Type: TypeACPAgent, Command: "omp", Args: []string{"acp"}, InteractiveArgs: []string{}}); !batch || interactive {
		t.Fatalf("acp-agent with interactive_args: batch=%v interactive=%v, want true/false", batch, interactive)
	}

	// The launch argv is the ACP server's argv: it carries no {{prompt}} and the
	// prompt is not required at build time (it travels over the protocol).
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"omp-acp": {Type: TypeACPAgent, Command: "omp", Args: []string{"acp"}},
	}}
	resolved, err := BuildFrom(cfg, "omp-acp", "", nil, Vars{}, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildFrom: %v", err)
	}
	if resolved.Command != "omp" || !reflect.DeepEqual(resolved.Args, []string{"acp"}) {
		t.Fatalf("resolved = %s %#v, want omp [acp]", resolved.Command, resolved.Args)
	}
}

// TestACPAgentTemplatesAreBuiltIn pins the built-in adapters (design §一.1):
// the CLI adapters are detect-gated like every other template, so an operator who
// declares the same key keeps their own entry.
func TestACPAgentTemplatesAreBuiltIn(t *testing.T) {
	want := map[string]struct {
		command string
		args    []string
	}{
		"claude-acp": {command: "npx", args: []string{"-y", "@zed-industries/claude-code-acp"}},
		"codex-acp":  {command: "codex-acp"},
		"gemini-acp": {command: "gemini", args: []string{"--acp"}},
		"omp-acp":    {command: "omp", args: []string{"acp"}},
		"jcode-acp":  {command: "jcode", args: []string{"acp"}},
	}
	for key, w := range want {
		tpl, ok := builtinTemplates[key]
		if !ok {
			t.Fatalf("built-in template %q is missing", key)
		}
		if tpl.Type != TypeACPAgent {
			t.Fatalf("%s type = %q, want %q", key, tpl.Type, TypeACPAgent)
		}
		if tpl.Command != w.command || !reflect.DeepEqual(tpl.Args, w.args) {
			t.Fatalf("%s = %s %#v, want %s %#v", key, tpl.Command, tpl.Args, w.command, w.args)
		}
		if hasPrompt(tpl.Args) {
			t.Fatalf("%s args carry {{prompt}}: the prompt travels over the protocol", key)
		}
	}
	// claude-acp runs through npx, whose own --version says nothing about the
	// adapter, so it is the one template that needs an explicit version probe: the
	// default `<command> --version` is right for every other adapter, jcode-acp
	// included (`jcode --version` prints "jcode v0.85.0 (<hash>)").
	if got := builtinTemplates["claude-acp"].Detect; got.Command != "npx" || len(got.Args) == 0 {
		t.Fatalf("claude-acp detect = %+v, want the adapter's version probe", got)
	}
	if got := builtinTemplates["jcode-acp"].Detect; got.Command != "" {
		t.Fatalf("jcode-acp detect = %+v, want the default `<command> --version` probe", got)
	}
}
