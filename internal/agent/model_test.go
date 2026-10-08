package agent

import (
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestWithModelArgsPlacement(t *testing.T) {
	ma := []string{"--model", "{{model}}"}
	if got := WithModelArgs([]string{"-p", "{{prompt}}"}, ma); !reflect.DeepEqual(got, []string{"-p", "--model", "{{model}}", "{{prompt}}"}) {
		t.Fatalf("before prompt: %#v", got)
	}
	// No prompt argument (interactive shape): appended at the end.
	if got := WithModelArgs([]string{"resume", "{{session_id}}"}, ma); !reflect.DeepEqual(got, []string{"resume", "{{session_id}}", "--model", "{{model}}"}) {
		t.Fatalf("no prompt: %#v", got)
	}
	// Empty model_args: the template itself comes back (nothing changes).
	tmpl := []string{"exec", "{{prompt}}"}
	if got := WithModelArgs(tmpl, nil); !reflect.DeepEqual(got, tmpl) {
		t.Fatalf("empty model_args: %#v", got)
	}
	// The input template is never modified.
	WithModelArgs(tmpl, ma)
	if !reflect.DeepEqual(tmpl, []string{"exec", "{{prompt}}"}) {
		t.Fatalf("template mutated: %#v", tmpl)
	}
}

func TestBuiltinTemplatesCarryModelArgs(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": builtinTemplates["claude"],
		"codex":  builtinTemplates["codex"],
	}}
	cases := map[string][]string{
		"claude": {"-p", "--output-format", "stream-json", "--verbose", "--model", "opus", "do it"},
		"codex":  {"-s", "danger-full-access", "-a", "never", "exec", "-m", "gpt-5", "do it"},
	}
	for key, want := range cases {
		model := map[string]string{"claude": "opus", "codex": "gpt-5"}[key]
		res, err := BuildFrom(cfg, key, "do it", nil, Vars{}, BuildOptions{Model: model})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if !reflect.DeepEqual(res.Args, want) {
			t.Fatalf("%s argv = %#v, want %#v", key, res.Args, want)
		}
	}
}

// TestBuildWithoutModelIsUnchanged locks the "no --model => argv exactly as before"
// contract for the built-in templates, batch and interactive.
func TestBuildWithoutModelIsUnchanged(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": builtinTemplates["claude"],
		"codex":  builtinTemplates["codex"],
	}}
	cases := []struct {
		key         string
		interactive bool
		want        []string
	}{
		{"claude", false, []string{"-p", "--output-format", "stream-json", "--verbose", "do it"}},
		{"codex", false, []string{"-s", "danger-full-access", "-a", "never", "exec", "do it"}},
		{"claude", true, []string{}},
		{"codex", true, []string{"-s", "danger-full-access", "-a", "never"}},
	}
	for _, c := range cases {
		res, err := BuildFrom(cfg, c.key, "do it", nil, Vars{}, BuildOptions{Interactive: c.interactive, AllowEmptyPrompt: c.interactive})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(res.Args, c.want) {
			t.Fatalf("%s interactive=%v argv = %#v, want %#v", c.key, c.interactive, res.Args, c.want)
		}
	}
}

func TestBuildModelInteractiveAppendsBeforeAgentArgs(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{"codex": builtinTemplates["codex"]}}
	res, err := BuildFrom(cfg, "codex", "", nil, Vars{}, BuildOptions{
		Interactive: true, AllowEmptyPrompt: true, Model: "gpt-5", AgentArgs: []string{"--x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-s", "danger-full-access", "-a", "never", "-m", "gpt-5", "--x"}
	if !reflect.DeepEqual(res.Args, want) {
		t.Fatalf("argv = %#v, want %#v", res.Args, want)
	}
}

func TestBuildModelRequiresModelArgs(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"my-tool": {Type: TypeCLIAgent, Command: "my-tool", Args: []string{"{{prompt}}"}},
	}}
	if _, err := BuildFrom(cfg, "my-tool", "x", nil, Vars{}, BuildOptions{Model: "m"}); err == nil {
		t.Fatal("agent without model_args must refuse a model")
	}
	// Custom model_args win over the built-in table and render {{model}}.
	cfg.Agents["my-tool"] = config.AgentConfig{Type: TypeCLIAgent, Command: "my-tool", Args: []string{"run", "{{prompt}}"}, ModelArgs: []string{"--use={{model}}"}}
	res, err := BuildFrom(cfg, "my-tool", "x", nil, Vars{}, BuildOptions{Model: "m1"})
	if err != nil || !reflect.DeepEqual(res.Args, []string{"run", "--use=m1", "x"}) {
		t.Fatalf("custom model_args: %#v %v", res.Args, err)
	}
}

func TestApplyModelDefaults(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		// A declared claude without model_args inherits the built-in one by key.
		"claude": {Type: TypeCLIAgent, Command: "claude", Args: []string{"-p", "{{prompt}}"}},
		// ... and a renamed wrapper by the base name of its command.
		"my-codex": {Type: TypeCLIAgent, Command: "/usr/bin/codex", Args: []string{"exec", "{{prompt}}"}},
		"unknown":  {Type: TypeCLIAgent, Command: "x", Args: []string{"{{prompt}}"}},
		"explicit": {Type: TypeCLIAgent, Command: "claude", Args: []string{"{{prompt}}"}, ModelArgs: []string{"--m", "{{model}}"}},
		"acp":      {Type: TypeACPAgent, Command: "codex"},
	}}
	for key, want := range map[string][]string{
		"claude":   {"--model", "{{model}}"},
		"my-codex": {"-m", "{{model}}"},
		"unknown":  nil,
		"explicit": {"--m", "{{model}}"},
		"acp":      nil,
	} {
		ac, _ := ResolveAgent(cfg, key)
		if !reflect.DeepEqual(ac.ModelArgs, want) {
			t.Fatalf("%s model_args = %#v, want %#v", key, ac.ModelArgs, want)
		}
	}
}
