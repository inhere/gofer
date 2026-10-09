package agent

import (
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestBuiltinSessionDefaultsOmp(t *testing.T) {
	def := builtinSessionDefaults["omp"]
	// Two branches, one group each (ndjson session row, then the TUI exit banner):
	// the ndjson row fires branch 1, so group 1 holds the id and group 2 is empty.
	m := regexp.MustCompile(def.SessionCapture).FindStringSubmatch(`{"type":"session","id":"123e4567-e89b-12d3-a456-426614174000"}`)
	if len(m) != 3 || m[1] != "123e4567-e89b-12d3-a456-426614174000" || m[2] != "" {
		t.Fatalf("omp session capture = %v", m)
	}
	if got, want := def.SessionResume, []string{"--resume", "{{session_id}}", "-p", "{{prompt}}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("omp resume = %v, want %v", got, want)
	}
	if got, want := def.SessionResumeInteractive, []string{"--resume", "{{session_id}}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("omp interactive resume = %v, want %v", got, want)
	}
}

// TestBuiltinTemplatesTable pins every field of every template. These entries are
// auto-injected on every host that has the CLI on PATH, so a wrong arg does not fail
// loudly — it makes the box ADVERTISE a capability whose jobs then hang or drop their
// prompt. Update this table only against a real `<cli> --help`.
func TestBuiltinTemplatesTable(t *testing.T) {
	want := map[string]config.AgentConfig{
		"claude": {
			Type:            TypeCLIAgent,
			Command:         "claude",
			GlobalArgs:      []string{},
			Args:            []string{"-p", "--output-format", "stream-json", "--verbose", "{{prompt}}"},
			InteractiveArgs: []string{},
			OutputFormat:    "ndjson",
		},
		"codex": {
			Type:            TypeCLIAgent,
			Command:         "codex",
			GlobalArgs:      []string{"-s", "danger-full-access", "-a", "never"},
			Args:            []string{"exec", "{{prompt}}"},
			InteractiveArgs: []string{},
		},
		"opencode": {
			Type:    TypeCLIAgent,
			Command: "opencode",
			Args:    []string{"run", "{{prompt}}"},
		},
		// ACP-01 §一.1: the four ACP server adapters. Their args are the SERVER's
		// launch argv (no {{prompt}}: the prompt travels over the protocol).
		"claude-acp": {
			Type:    TypeACPAgent,
			Command: "npx",
			Args:    []string{"-y", "@agentclientprotocol/claude-agent-acp"},
			Detect:  config.DetectConfig{Command: "npx", Args: []string{"-y", "@agentclientprotocol/claude-agent-acp", "--version"}},
			// F12 (2026-09-25): the adapter's session modes are
			// default / acceptEdits / plan / bypassPermissions, so read-only is `plan`
			// (from the adapter's own `availableModes`, real-machine ACP-02).
			// F14 (2026-09-25): the Claude Agent SDK reads the key from claude's
			// settings file, not from gofer's environment, so the template opts into
			// inheriting that file's env block.
			ACP: &config.ACPConfig{
				Modes:             map[string]string{"read_only": "plan"},
				ClaudeSettingsEnv: boolRef(true),
			},
		},
		"codex-acp": {
			Type:    TypeACPAgent,
			Command: "npx",
			Args:    []string{"-y", "@agentclientprotocol/codex-acp"},
			Detect:  config.DetectConfig{Command: "npx", Args: []string{"-y", "@agentclientprotocol/codex-acp", "--version"}},
			ACP: &config.ACPConfig{
				Modes: map[string]string{"read_only": "read-only"},
			},
		},
		"gemini-acp": {
			Type:    TypeACPAgent,
			Command: "gemini",
			Args:    []string{"--acp"},
		},
		"omp-acp": {
			Type:    TypeACPAgent,
			Command: "omp",
			Args:    []string{"acp"},
		},
		"jcode-acp": {
			Type:    TypeACPAgent,
			Command: "jcode",
			Args:    []string{"acp"},
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

// TestInteractiveCapableTemplatesPassJobGate mirrors the admission an interactive job
// hits (job.validate: the agent must have an interactive mode, and an interactive
// agent must be non-exec). claude / codex are dual-mode (batch args with {{prompt}}
// plus `interactive_args: []` = bare TUI launch), so the same key runs batch AND in a
// pty (`job run -a claude --interactive`, `job resume --mode interactive`, takeover).
// Asserted here rather than via job.validate because internal/job imports internal/agent.
func TestInteractiveCapableTemplatesPassJobGate(t *testing.T) {
	for _, key := range []string{"claude", "codex"} {
		tpl, ok := builtinTemplates[key]
		if !ok {
			t.Fatalf("template %q missing", key)
		}
		batch, interactive := Modes(tpl)
		if !batch || !interactive {
			t.Fatalf("%s modes=(%v,%v), want dual-mode", key, batch, interactive)
		}
		if tpl.Type == TypeExec || tpl.Interactive {
			t.Fatalf("%s: must be a non-legacy cli-agent (dual-mode via interactive_args): %+v", key, tpl)
		}
		if len(tpl.InteractiveArgs) != 0 {
			t.Fatalf("%s: interactive_args must be empty (bare TUI launch): %v", key, tpl.InteractiveArgs)
		}
		// The interactive resume (takeover / `job resume --mode interactive`) comes
		// from the session defaults and must be a TUI argv with no {{prompt}}.
		cfg, _ := Resolve(&config.Config{}, newFake(key))
		ac, ok := NewRegistry(cfg).Get(key)
		if !ok || len(ac.SessionResumeInteractive) == 0 || hasPrompt(ac.SessionResumeInteractive) {
			t.Fatalf("%s: session_resume_interactive = %v", key, ac.SessionResumeInteractive)
		}
	}
	// Every non-interactive template must carry the prompt placeholder — an agent
	// submitted in batch mode otherwise renders an argv with no prompt at all.
	for _, key := range []string{"claude", "codex", "opencode"} {
		tpl := builtinTemplates[key]
		if tpl.Interactive {
			t.Fatalf("%s: template marked legacy-interactive", key)
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
		// Same key as a DUAL-MODE template, declared batch-only on purpose.
		"codex": {Type: TypeCLIAgent, Command: "/opt/my/codex", Args: []string{"exec", "{{prompt}}"}},
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
	if got.Agents["codex"].InteractiveArgs != nil || got.Agents["codex"].GlobalArgs != nil {
		t.Fatalf("template fields leaked into the escape hatch: %+v", got.Agents["codex"])
	}
	// The templates the operator did NOT claim are still injected.
	if !got.IsInjectedAgent("opencode") {
		t.Fatalf("unclaimed templates were not injected: %v", got.InjectedAgents())
	}
}

// TestTemplatesInheritSessionDefaults: the templates carry no session fields; they pick
// them up from builtinSessionDefaults at read time (by agent key), including the
// interactive resume that a pty takeover / `job resume --mode interactive` runs.
func TestTemplatesInheritSessionDefaults(t *testing.T) {
	cfg, _ := Resolve(&config.Config{}, newFake(templateKeys()...))
	reg := NewRegistry(cfg)

	claude, _ := reg.Get("claude")
	if !reflect.DeepEqual(claude.SessionInject, builtinSessionDefaults["claude"].SessionInject) {
		t.Fatalf("claude template did not inherit the session-inject default: %v", claude.SessionInject)
	}
	if !reflect.DeepEqual(claude.SessionResumeInteractive, builtinSessionDefaults["claude"].SessionResumeInteractive) {
		t.Fatalf("claude did not inherit its interactive resume: %v", claude.SessionResumeInteractive)
	}

	codex, _ := reg.Get("codex")
	if codex.SessionCapture != builtinSessionDefaults["codex"].SessionCapture {
		t.Fatalf("codex template did not inherit the session-capture default: %q", codex.SessionCapture)
	}
	if !reflect.DeepEqual(codex.SessionResumeInteractive, builtinSessionDefaults["codex"].SessionResumeInteractive) {
		t.Fatalf("codex did not inherit its interactive resume: %v", codex.SessionResumeInteractive)
	}
	if !reflect.DeepEqual(codex.SystemInject, builtinSessionDefaults["codex"].SystemInject) {
		t.Fatalf("codex did not inherit its system-inject: %v", codex.SystemInject)
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
