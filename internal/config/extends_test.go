package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"
)

// fakeTemplates stands in for the agent package's built-in templates.
func fakeTemplates(t *testing.T) {
	t.Helper()
	prevT, prevN := BuiltinAgentTemplate, BuiltinAgentTemplateNames
	t.Cleanup(func() { BuiltinAgentTemplate, BuiltinAgentTemplateNames = prevT, prevN })
	tpl := map[string]AgentConfig{
		"tool": {
			Type: "cli-agent", Command: "tool", NoRawCmd: true,
			Args: []string{"-p", "{{prompt}}"}, InteractiveArgs: ArgList{},
			Env: map[string]string{"A": "1"}, OutputFormat: "ndjson",
		},
	}
	BuiltinAgentTemplate = func(name string) (AgentConfig, bool) { a, ok := tpl[name]; return a, ok }
	BuiltinAgentTemplateNames = func() []string { return []string{"tool"} }
}

func TestAgentExtendsOverridesOnlyWrittenKeys(t *testing.T) {
	fakeTemplates(t)
	agents := map[string]AgentConfig{}
	raw := []byte("agents:\n  tool:\n    extends: builtin\n    no_raw_cmd: false\n    args: [run, \"{{prompt}}\"]\n    rules: [house]\n")
	if err := yamlUnmarshalAgents(raw, &agents); err != nil {
		t.Fatal(err)
	}
	if err := ApplyAgentExtends(agents, raw); err != nil {
		t.Fatal(err)
	}
	a := agents["tool"]
	if a.Command != "tool" || a.Type != "cli-agent" || a.OutputFormat != "ndjson" || a.Env["A"] != "1" {
		t.Fatalf("template fields not inherited: %+v", a)
	}
	if a.NoRawCmd {
		t.Fatal("no_raw_cmd: false must override the template's true")
	}
	if !reflect.DeepEqual(a.Args, []string{"run", "{{prompt}}"}) || !reflect.DeepEqual(a.Rules, []string{"house"}) {
		t.Fatalf("written keys not applied: args=%v rules=%v", a.Args, a.Rules)
	}
	if got := a.DeclaredKeys(); !reflect.DeepEqual(got, []string{"args", "no_raw_cmd", "rules"}) {
		t.Fatalf("DeclaredKeys = %v", got)
	}
	// The template handed out is a copy: changing the merged agent leaves it alone.
	a.Env["A"] = "changed"
	if tpl, _ := BuiltinAgentTemplate("tool"); tpl.Env["A"] != "1" {
		t.Fatal("merged agent aliases the template's map")
	}
}

func TestAgentExtendsErrors(t *testing.T) {
	fakeTemplates(t)
	for raw, want := range map[string]string{
		"agents:\n  tool:\n    extends: other\n":                                      `"other" is neither`,
		"agents:\n  nosuch:\n    extends: builtin\n":                                  `built-in template named "nosuch"`,
		"agents:\n  x:\n    extends: builtin:nosuch\n":                                `built-in template named "nosuch"`,
		"agents:\n  x:\n    extends: x\n":                                             `cannot extend itself`,
		"agents:\n  a:\n    extends: b\n  b:\n    extends: a\n":                       `cycle a -> b -> a`,
		"agents:\n  a:\n    extends: b\n  b:\n    extends: c\n  c:\n    extends: b\n": `cycle`,
	} {
		agents := map[string]AgentConfig{}
		if err := yamlUnmarshalAgents([]byte(raw), &agents); err != nil {
			t.Fatal(err)
		}
		if err := ApplyAgentExtends(agents, []byte(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err=%v, want %q", raw, err, want)
		}
	}
}

// Saving an edited extends agent writes only its difference from the template: the
// keys it declared plus the field the edit changed — never the template's values.
func TestAgentExtendsSaveWritesOnlyTheDifference(t *testing.T) {
	fakeTemplates(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	src := "agents:\n  tool:\n    extends: builtin   # start from the built-in\n    rules: [house]\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agents["tool"].Command != "tool" {
		t.Fatalf("extends not applied on Load: %+v", cfg.Agents["tool"])
	}
	// No edit: the file keeps its bytes.
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, p); got != src {
		t.Fatalf("unchanged save rewrote the file:\n%s", got)
	}
	// An edit to one field: only extends, the declared rules and that field.
	a := cfg.Agents["tool"]
	a.SessionCapture = "id=(\\S+)"
	a.NoRawCmd = false // template has true: an override to false must be written
	cfg.Agents["tool"] = a
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	got := readText(t, p)
	for _, want := range []string{"extends: builtin", "rules:", "session_capture:", "no_raw_cmd: false"} {
		if !strings.Contains(got, want) {
			t.Fatalf("saved file lacks %q:\n%s", want, got)
		}
	}
	for _, leaked := range []string{"command:", "type:", "output_format:", "args:"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("template value %q materialised into the file:\n%s", leaked, got)
		}
	}
	again, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if b := again.Agents["tool"]; b.NoRawCmd || b.SessionCapture == "" || b.Command != "tool" {
		t.Fatalf("round trip: %+v", b)
	}
}

func yamlUnmarshalAgents(raw []byte, agents *map[string]AgentConfig) error {
	var doc struct {
		Agents map[string]AgentConfig `yaml:"agents"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	*agents = doc.Agents
	return nil
}

// An agent can extend another declared agent (resolved first, chains allowed) or a
// built-in template under another name; saving still writes only the difference.
func TestAgentExtendsDeclaredAgentAndNamedTemplate(t *testing.T) {
	fakeTemplates(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	src := "agents:\n" +
		"  fast:\n    extends: base\n    env: {MODEL: small}\n" +
		"  base:\n    extends: builtin:tool\n    args: [run, \"{{prompt}}\"]\n" +
		"  plain:\n    type: cli-agent\n    command: plain\n    args: [\"{{prompt}}\"]\n" +
		"  child:\n    extends: plain\n    rules: [house]\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	fast := cfg.Agents["fast"]
	if fast.Command != "tool" || fast.OutputFormat != "ndjson" || !reflect.DeepEqual(fast.Args, []string{"run", "{{prompt}}"}) {
		t.Fatalf("fast must inherit base (template + base's args): %+v", fast)
	}
	if !reflect.DeepEqual(fast.Env, map[string]string{"MODEL": "small"}) {
		t.Fatalf("a written map replaces the base's: %v", fast.Env)
	}
	if got := cfg.Agents["base"]; got.Command != "tool" || got.Extends != "builtin:tool" {
		t.Fatalf("builtin:<template>: %+v", got)
	}
	if child := cfg.Agents["child"]; child.Command != "plain" || len(child.Rules) != 1 {
		t.Fatalf("child of a plain declared agent: %+v", child)
	}

	a := cfg.Agents["fast"]
	a.SessionCapture = "id=(\\S+)"
	cfg.Agents["fast"] = a
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	got := readText(t, p)
	for _, want := range []string{"extends: base", "MODEL: small", "session_capture:", "extends: builtin:tool"} {
		if !strings.Contains(got, want) {
			t.Fatalf("saved file lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "command:") != 1 { // only plain's own
		t.Fatalf("base values materialised into the file:\n%s", got)
	}
}
