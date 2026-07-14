package agent

import (
	"reflect"
	"sort"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// fakeDetector reports exactly the keys in avail as available and records how many
// times it was called plus the candidate set of the last call.
type fakeDetector struct {
	avail map[string]bool
	calls int
	last  []string
}

func (f *fakeDetector) Detect(agents map[string]config.AgentConfig) map[string]DetectResult {
	f.calls++
	f.last = f.last[:0]
	out := make(map[string]DetectResult, len(agents))
	for key := range agents {
		f.last = append(f.last, key)
		out[key] = DetectResult{Available: f.avail[key]}
	}
	sort.Strings(f.last)
	return out
}

func newFake(avail ...string) *fakeDetector {
	m := map[string]bool{}
	for _, a := range avail {
		m[a] = true
	}
	return &fakeDetector{avail: m}
}

func agentKeys(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Agents))
	for k := range cfg.Agents {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestResolveInjectsDetectedTemplate: a template key the operator never declared is
// materialized when — and only when — the detector says its CLI is on this host.
func TestResolveInjectsDetectedTemplate(t *testing.T) {
	cfg := &config.Config{}
	got, detected := Resolve(cfg, newFake("claude"))

	if _, ok := got.Agents["claude"]; !ok {
		t.Fatalf("template claude not materialized; agents=%v", agentKeys(got))
	}
	if !got.IsInjectedAgent("claude") {
		t.Fatal("materialized claude was not marked injected (it would be persisted on save)")
	}
	if !detected["claude"].Available {
		t.Fatal("detect result for claude missing from the returned map")
	}
	// exec is always probed so its availability is reported alongside the rest.
	if _, ok := detected[ExecAgentKey]; !ok {
		t.Fatal("exec missing from the detect result map")
	}
}

// TestResolveGatesUndetectedTemplate: not installed => not offered.
func TestResolveGatesUndetectedTemplate(t *testing.T) {
	cfg := &config.Config{}
	got, _ := Resolve(cfg, newFake()) // nothing available

	if _, ok := got.Agents["claude"]; ok {
		t.Fatal("undetected template claude was injected anyway (detect gate is dead)")
	}
	if len(got.InjectedAgents()) != 0 {
		t.Fatalf("expected no injected keys, got %v", got.InjectedAgents())
	}
}

// TestResolveEscapeHatchWinsWholeEntry: the IRON RULE. A declared agent overrides the
// template ENTIRELY — no field-level merge (the template's Args must NOT leak in).
func TestResolveEscapeHatchWinsWholeEntry(t *testing.T) {
	mine := config.AgentConfig{Type: TypeCLIAgent, Command: "/opt/my/claude"}
	cfg := &config.Config{Agents: map[string]config.AgentConfig{"claude": mine}}

	got, _ := Resolve(cfg, newFake("claude"))

	if !reflect.DeepEqual(got.Agents["claude"], mine) {
		t.Fatalf("escape hatch was not preserved verbatim: got %+v want %+v", got.Agents["claude"], mine)
	}
	if len(got.Agents["claude"].Args) != 0 {
		t.Fatalf("template Args leaked into the escape hatch: %v", got.Agents["claude"].Args)
	}
	if got.IsInjectedAgent("claude") {
		t.Fatal("an operator-declared agent was marked injected (it would be STRIPPED from their config on save)")
	}
}

// TestResolveEscapeHatchSurvivesFailedProbe: the other half of the IRON RULE. An agent
// the operator declared is NEVER removed because its probe failed — that is exactly the
// D13 blast radius (every cli-agent vanishing from a live worker's caps).
func TestResolveEscapeHatchSurvivesFailedProbe(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude":    {Type: TypeCLIAgent, Command: "/opt/fake/claude"},
		"tty-demo":  {Type: TypeCLIAgent, Command: "__no_such_cli__", Interactive: true},
		"homegrown": {Type: TypeCLIAgent, Command: "homegrown"},
	}}

	got, _ := Resolve(cfg, newFake()) // every probe fails

	for _, key := range []string{"claude", "tty-demo", "homegrown"} {
		if _, ok := got.Agents[key]; !ok {
			t.Fatalf("declared agent %q was dropped after a failed probe (iron rule violated)", key)
		}
	}
}

// TestResolveIdempotent: resolve∘resolve == resolve. A second pass must NOT read the
// keys the first pass injected as operator declarations (which would promote them to
// un-gated escape hatches), and must re-gate them from scratch.
func TestResolveIdempotent(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"mine": {Type: TypeCLIAgent, Command: "mine"},
	}}

	once, _ := Resolve(cfg, newFake("claude"))
	keysOnce := agentKeys(once)
	injOnce := once.InjectedAgents()

	twice, _ := Resolve(once, newFake("claude"))
	if !reflect.DeepEqual(agentKeys(twice), keysOnce) {
		t.Fatalf("merge is not idempotent: once=%v twice=%v", keysOnce, agentKeys(twice))
	}
	if !reflect.DeepEqual(twice.InjectedAgents(), injOnce) {
		t.Fatalf("injected marks drifted: once=%v twice=%v", injOnce, twice.InjectedAgents())
	}
	if !twice.IsInjectedAgent("claude") {
		t.Fatal("second pass demoted an injected template to an operator declaration (detect gate would be dead from here on)")
	}

	// And re-gating really happens: the CLI "disappears" => the injected key goes away,
	// while the operator's own agent stays.
	gone, _ := Resolve(twice, newFake())
	if _, ok := gone.Agents["claude"]; ok {
		t.Fatal("a previously injected template survived a re-resolve whose probe failed")
	}
	if _, ok := gone.Agents["mine"]; !ok {
		t.Fatal("re-resolve dropped the operator's own agent")
	}
}

// TestResolveDetectsOncePerCall asserts the Detector contract: ONE call, carrying every
// candidate (operator agents + injectable templates + built-in exec).
func TestResolveDetectsOncePerCall(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"mine": {Type: TypeCLIAgent, Command: "mine"},
	}}
	d := newFake("claude")

	Resolve(cfg, d)

	if d.calls != 1 {
		t.Fatalf("detector called %d times, want exactly 1", d.calls)
	}
	// Every injectable template + the operator's agent + the built-in exec, in one call.
	want := append(templateKeys(), ExecAgentKey, "mine")
	sort.Strings(want)
	if !reflect.DeepEqual(d.last, want) {
		t.Fatalf("detector candidate set = %v, want %v", d.last, want)
	}
}

// TestNoopDetectorInjectsNothing: the seam tests rely on — a config resolves to exactly
// the agent set it declared, whatever is installed on the machine running `go test`.
func TestNoopDetectorInjectsNothing(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"mine": {Type: TypeCLIAgent, Command: "mine"},
	}}

	got, _ := Resolve(cfg, NoopDetector{})

	if !reflect.DeepEqual(agentKeys(got), []string{"mine"}) {
		t.Fatalf("NoopDetector resolved to %v, want [mine]", agentKeys(got))
	}
}

// TestLookPathProbeExecIsAlwaysAvailable: exec needs no external CLI, and that check
// must precede any command lookup — otherwise a host without `sh` (Windows) would report
// the BUILT-IN exec agent unavailable.
func TestLookPathProbeExecIsAlwaysAvailable(t *testing.T) {
	res := lookPathProbe(config.AgentConfig{Type: TypeExec, Detect: config.DetectConfig{Command: "sh", Args: []string{"-c", "true"}}})
	if !res.Available {
		t.Fatalf("built-in exec probed unavailable: %+v", res)
	}
}
