package agent

import (
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestReadOnlyArgsOverrideBuiltin: the built-in read-only args fill in ONLY when the
// agent declares none (an explicit read_only_args wins — the operator's agent is the
// authority on its own CLI), and the lookup falls back from the agent KEY to the base
// name of its command, so `my-claude` running claude inherits claude's.
func TestReadOnlyArgsOverrideBuiltin(t *testing.T) {
	cfg := testConfig()
	ac := cfg.Agents["codex"]
	ac.ReadOnlyArgs = []string{"--custom-read-only"}
	cfg.Agents["codex"] = ac

	got, ok := ResolveAgent(cfg, "codex")
	if !ok {
		t.Fatal("ResolveAgent(codex) not found")
	}
	if !reflect.DeepEqual(got.ReadOnlyArgs, []string{"--custom-read-only"}) {
		t.Fatalf("explicit read_only_args = %#v, want the configured value (built-in must not overwrite)", got.ReadOnlyArgs)
	}

	cfg2 := &config.Config{Agents: map[string]config.AgentConfig{
		// Key is not in the built-in table; the command's base name is.
		"my-claude": {Type: TypeCLIAgent, Command: "/usr/local/bin/claude", Args: []string{"-p", "{{prompt}}"}},
		// Neither the key nor the command is known: no read-only args, so a
		// --read-only submit is refused instead of silently running writable.
		"my-tool": {Type: TypeCLIAgent, Command: "my-tool", Args: []string{"{{prompt}}"}},
		// An acp-agent never gets argv-suffix defaults (its read-only mode is the
		// protocol's, mapped in acp.modes.read_only).
		"claude-acp": {Type: TypeACPAgent, Command: "npx"},
	}}
	inherited, _ := ResolveAgent(cfg2, "my-claude")
	if !reflect.DeepEqual(inherited.ReadOnlyArgs, config.BuiltinReadOnlyArgs("claude")) {
		t.Fatalf("my-claude read_only_args = %#v, want claude's built-in %#v",
			inherited.ReadOnlyArgs, config.BuiltinReadOnlyArgs("claude"))
	}
	unknown, _ := ResolveAgent(cfg2, "my-tool")
	if len(unknown.ReadOnlyArgs) != 0 {
		t.Fatalf("my-tool read_only_args = %#v, want none", unknown.ReadOnlyArgs)
	}
	acp, _ := ResolveAgent(cfg2, "claude-acp")
	if len(acp.ReadOnlyArgs) != 0 {
		t.Fatalf("acp-agent read_only_args = %#v, want none", acp.ReadOnlyArgs)
	}
}

// TestBuildAppendsReadOnlyArgs: ReadOnly=true appends the resolved read-only args to
// the END of a cli-agent's argv, for BOTH argv shapes (batch args and interactive
// interactive_args) — the same position AgentArgs uses; ReadOnly=false changes nothing;
// an exec agent's argv is not touched (it cannot run read-only at all, which submit
// rejects separately).
func TestBuildAppendsReadOnlyArgs(t *testing.T) {
	cfg := testConfig()
	ac := cfg.Agents["codex"]
	ac.InteractiveArgs = []string{"--interactive"}
	cfg.Agents["codex"] = ac
	reg := NewRegistry(cfg)

	want := config.BuiltinReadOnlyArgs("codex")
	if len(want) == 0 {
		t.Fatal("setup: codex has no built-in read-only args")
	}

	batch, err := reg.BuildWithOptions("codex", "prompt", nil, Vars{}, BuildOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("BuildWithOptions(batch, read-only): %v", err)
	}
	if got := batch.Args[len(batch.Args)-len(want):]; !reflect.DeepEqual(got, want) {
		t.Fatalf("batch argv tail = %#v, want %#v (full argv %#v)", got, want, batch.Args)
	}
	inter, err := reg.BuildWithOptions("codex", "prompt", nil, Vars{}, BuildOptions{ReadOnly: true, Interactive: true})
	if err != nil {
		t.Fatalf("BuildWithOptions(interactive, read-only): %v", err)
	}
	if got := inter.Args[len(inter.Args)-len(want):]; !reflect.DeepEqual(got, want) {
		t.Fatalf("interactive argv tail = %#v, want %#v (full argv %#v)", got, want, inter.Args)
	}

	plain, err := reg.BuildWithOptions("codex", "prompt", nil, Vars{}, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildWithOptions(batch): %v", err)
	}
	if hasArg(plain.Args, "read-only") {
		t.Fatalf("ReadOnly=false must not add read-only args: %#v", plain.Args)
	}

	ex, err := reg.BuildWithOptions(ExecAgentKey, "", []string{"go", "version"}, Vars{}, BuildOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("BuildWithOptions(exec): %v", err)
	}
	if !reflect.DeepEqual(ex.Args, []string{"version"}) {
		t.Fatalf("exec argv = %#v, want the request's argv untouched", ex.Args)
	}
}
