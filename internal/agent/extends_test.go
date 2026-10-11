package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// extends: builtin against the real templates: the declaration only carries what it
// changes, the rest comes from the built-in definition, and the agent is an operator
// declaration (never detect-gated).
func TestExtendsBuiltinUsesRealTemplate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	src := "agents:\n  claude:\n    extends: builtin\n    interactive_args: [--enable-auto-mode]\n    rules: [house]\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	tpl := builtinTemplates["claude"]
	got := cfg.Agents["claude"]
	if got.Command != tpl.Command || !reflect.DeepEqual(got.Args, tpl.Args) || got.OutputFormat != tpl.OutputFormat {
		t.Fatalf("template fields not inherited: %+v", got)
	}
	if !reflect.DeepEqual([]string(got.InteractiveArgs), []string{"--enable-auto-mode"}) || !reflect.DeepEqual(got.Rules, []string{"house"}) {
		t.Fatalf("overrides not applied: %+v", got)
	}
	resolved, _ := Resolve(cfg, newFake()) // claude's CLI "not found": still kept
	if _, ok := resolved.Agents["claude"]; !ok || resolved.IsInjectedAgent("claude") {
		t.Fatal("an extends agent is an operator declaration: never gated or marked injected")
	}
	if ra, ok := ResolveAgent(resolved, "claude"); !ok || ra.Command != "claude" {
		t.Fatalf("ResolveAgent = %+v, %v", ra, ok)
	}
}
