package agent

import (
	"reflect"
	"sort"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestBuiltinTemplatesTable pins every field of every template. These entries are
// auto-injected on every host that has the CLI on PATH, so a wrong arg does not fail
// loudly — it makes the box ADVERTISE a capability whose jobs then hang or drop their
// prompt. Update this table only against a real `<cli> --help`.
func TestBuiltinTemplatesTable(t *testing.T) {
	want := map[string]config.AgentConfig{
		"claude": {
			Type:    TypeCLIAgent,
			Command: "claude",
			Args:    []string{"-p", "--output-format", "stream-json", "--verbose", "{{prompt}}"},
		},
		"codex": {
			Type:    TypeCLIAgent,
			Command: "codex",
			Args:    []string{"exec", "{{prompt}}"},
		},
		"opencode": {
			Type:    TypeCLIAgent,
			Command: "opencode",
			Args:    []string{"run", "{{prompt}}"},
		},
		"tty-claude": {
			Type:        TypeCLIAgent,
			Command:     "claude",
			Interactive: true,
			NoRawCmd:    true,
		},
		"tty-codex": {
			Type:        TypeCLIAgent,
			Command:     "codex",
			Interactive: true,
			NoRawCmd:    true,
		},
	}
	if !reflect.DeepEqual(builtinTemplates, want) {
		t.Fatalf("builtinTemplates drifted:\n got=%+v\nwant=%+v", builtinTemplates, want)
	}
}

// TestBuiltinTemplatesExcludeExec: exec is built in via builtinExecAgent. Declaring it
// here as well would inject it as a config key, i.e. turn it into an operator
// declaration (escape hatch) and change how it resolves.
func TestBuiltinTemplatesExcludeExec(t *testing.T) {
	if _, ok := builtinTemplates[ExecAgentKey]; ok {
		t.Fatal("exec redeclared in builtinTemplates; it is already built in (builtinExecAgent)")
	}
	for key, tpl := range builtinTemplates {
		if tpl.Type == TypeExec {
			t.Fatalf("template %q is type exec; templates only cover cli-agents", key)
		}
	}
}

// TestInteractiveTemplatesPassJobGate mirrors the admission gate an interactive job
// hits (job.validate: "interactive agent must be no-raw-cmd and non-exec", plus the
// "agent is not interactive" check before it). A tty-* template that failed it would
// be injectable yet un-runnable — rejected at submit on every host. Asserted here
// rather than by calling job.validate because internal/job imports internal/agent.
func TestInteractiveTemplatesPassJobGate(t *testing.T) {
	for _, key := range []string{"tty-claude", "tty-codex"} {
		tpl, ok := builtinTemplates[key]
		if !ok {
			t.Fatalf("interactive template %q missing", key)
		}
		if !tpl.Interactive {
			t.Fatalf("%s: Interactive=false; an interactive job would be rejected as not interactive", key)
		}
		if tpl.Type == TypeExec || !tpl.NoRawCmd {
			t.Fatalf("%s: violates the job gate (must be no-raw-cmd and non-exec): %+v", key, tpl)
		}
		if len(tpl.Args) != 0 {
			t.Fatalf("%s: interactive templates take no args (the pty owns the session): %v", key, tpl.Args)
		}
	}
	// The non-interactive templates are the other half of the invariant: they must NOT
	// be interactive, and they must carry the prompt placeholder — an interactive agent
	// submitted non-interactively renders an argv with no prompt at all.
	for _, key := range []string{"claude", "codex", "opencode"} {
		tpl := builtinTemplates[key]
		if tpl.Interactive {
			t.Fatalf("%s: non-interactive template marked interactive", key)
		}
		if !hasArg(tpl.Args, "{{prompt}}") {
			t.Fatalf("%s: args carry no {{prompt}}; the prompt would be silently dropped: %v", key, tpl.Args)
		}
	}
}

// TestResolveInjectsEveryTemplate: with every CLI present, all five keys materialize
// and are marked injected (so they are stripped again before any config save).
func TestResolveInjectsEveryTemplate(t *testing.T) {
	cfg := &config.Config{}
	got, _ := Resolve(cfg, newFake(templateKeys()...))

	// exec is NOT among them: it resolves without a config entry (builtinExecAgent).
	if !reflect.DeepEqual(agentKeys(got), templateKeys()) {
		t.Fatalf("resolved agents = %v, want %v", agentKeys(got), templateKeys())
	}
	for _, key := range templateKeys() {
		if !got.IsInjectedAgent(key) {
			t.Fatalf("%s was materialized but not marked injected (it would be persisted into the operator's config)", key)
		}
	}
	// A host with only one CLI installed gets only that one.
	only := &config.Config{}
	only, _ = Resolve(only, newFake("codex"))
	if !reflect.DeepEqual(agentKeys(only), []string{"codex"}) {
		t.Fatalf("partial host resolved to %v, want [codex]", agentKeys(only))
	}
}

// TestTemplateNeverPollutesEscapeHatch: the iron rule, per template. An operator entry
// wins the WHOLE key — the template's Args/Interactive/NoRawCmd must not bleed in, in
// either direction.
func TestTemplateNeverPollutesEscapeHatch(t *testing.T) {
	mine := map[string]config.AgentConfig{
		// Same key as a template, but this host's own binary and its own args.
		"claude": {Type: TypeCLIAgent, Command: "/opt/my/claude", Args: []string{"-p", "{{prompt}}"}},
		// Same key as an INTERACTIVE template, declared non-interactive on purpose.
		"tty-codex": {Type: TypeCLIAgent, Command: "/opt/my/codex"},
	}
	cfg := &config.Config{Agents: map[string]config.AgentConfig{}}
	for k, v := range mine {
		cfg.Agents[k] = v
	}

	got, _ := Resolve(cfg, newFake(templateKeys()...))

	for key, declared := range mine {
		if !reflect.DeepEqual(got.Agents[key], declared) {
			t.Fatalf("%s: escape hatch not preserved verbatim: got %+v want %+v", key, got.Agents[key], declared)
		}
		if got.IsInjectedAgent(key) {
			t.Fatalf("%s: operator-declared agent marked injected (it would be STRIPPED from their config on save)", key)
		}
	}
	if got.Agents["tty-codex"].Interactive || got.Agents["tty-codex"].NoRawCmd {
		t.Fatalf("template flags leaked into the escape hatch: %+v", got.Agents["tty-codex"])
	}
	// The templates the operator did NOT claim are still injected.
	if !got.IsInjectedAgent("tty-claude") || !got.IsInjectedAgent("codex") {
		t.Fatalf("unclaimed templates were not injected: %v", got.InjectedAgents())
	}
}

// TestTemplatesInheritSessionDefaults: the templates carry no session fields; they pick
// them up from builtinSessionDefaults at read time. tty-* match by the BASE NAME of
// Command (builtinSessionDefaultFor), which is what lets tty-codex inherit codex's
// capture/resume/system-inject without restating them.
func TestTemplatesInheritSessionDefaults(t *testing.T) {
	cfg, _ := Resolve(&config.Config{}, newFake(templateKeys()...))
	reg := NewRegistry(cfg)

	claude, _ := reg.Get("claude")
	if !reflect.DeepEqual(claude.SessionInject, builtinSessionDefaults["claude"].SessionInject) {
		t.Fatalf("claude template did not inherit the session-inject default: %v", claude.SessionInject)
	}

	ttyClaude, _ := reg.Get("tty-claude")
	if !reflect.DeepEqual(ttyClaude.SessionResumeInteractive, builtinSessionDefaults["claude"].SessionResumeInteractive) {
		t.Fatalf("tty-claude did not inherit claude's interactive resume: %v", ttyClaude.SessionResumeInteractive)
	}

	codex, _ := reg.Get("codex")
	if codex.SessionCapture != builtinSessionDefaults["codex"].SessionCapture {
		t.Fatalf("codex template did not inherit the session-capture default: %q", codex.SessionCapture)
	}

	ttyCodex, _ := reg.Get("tty-codex")
	if !reflect.DeepEqual(ttyCodex.SessionResumeInteractive, builtinSessionDefaults["codex"].SessionResumeInteractive) {
		t.Fatalf("tty-codex did not inherit codex's interactive resume: %v", ttyCodex.SessionResumeInteractive)
	}
	if ttyCodex.SessionCapture != builtinSessionDefaults["codex"].SessionCapture {
		t.Fatalf("tty-codex did not inherit codex's session-capture: %q", ttyCodex.SessionCapture)
	}
	if !reflect.DeepEqual(ttyCodex.SystemInject, builtinSessionDefaults["codex"].SystemInject) {
		t.Fatalf("tty-codex did not inherit codex's system-inject: %v", ttyCodex.SystemInject)
	}
}

func templateKeys() []string {
	out := make([]string, 0, len(builtinTemplates))
	for k := range builtinTemplates {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
