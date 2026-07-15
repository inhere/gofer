package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// countingDetector reports the keys in avail as available and counts its calls.
type countingDetector struct {
	avail map[string]bool
	calls int
}

func (d *countingDetector) Detect(agents map[string]config.AgentConfig) map[string]agent.DetectResult {
	d.calls++
	out := make(map[string]agent.DetectResult, len(agents))
	for key := range agents {
		out[key] = agent.DetectResult{Available: d.avail[key]}
	}
	return out
}

func detectorFor(keys ...string) *countingDetector {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return &countingDetector{avail: m}
}

// resolveTestConfig is an operator config with ONE hand-written agent and no `claude`.
func resolveTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: t.TempDir()},
		Agents: map[string]config.AgentConfig{
			"mine": {Type: agent.TypeCLIAgent, Command: "mine", Args: []string{"{{prompt}}"}},
		},
	}
	config.ApplyDefaults(cfg)
	return cfg
}

// TestBuildDoesNotPersistTemplateAgents is the P2 verification #2 — the one whose
// absence cancelled the whole point of P2.
//
// The *config.Config a Core holds is the SAME pointer project.Registry writes back
// through, and `agents` is a MANAGED top-level key (re-emitted from the struct on every
// save). So without the injected-key strip in config.render, one "add project" from the
// web console freezes every detected template into the operator's config.yaml — where it
// becomes an explicitly declared agent that the iron rule then keeps forever, even after
// its CLI is gone. Falsification: revert render() to marshal cfg directly and this test
// finds `claude` in the file.
func TestBuildDoesNotPersistTemplateAgents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir) // project.Registry.save() -> <dir>/config.yaml
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := resolveTestConfig(t)
	cr, err := Build(cfg, WithAgentDetector(detectorFor("claude")))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cr.Close() }()

	// Precondition: the template really did materialize (otherwise this test proves
	// nothing — it would pass simply because there was nothing to leak).
	if _, ok := cr.Config().Agents["claude"]; !ok {
		t.Fatalf("precondition failed: template claude was not materialized; agents=%v", cr.Config().Agents)
	}

	// The trigger: exactly what one click on "new project" in the web console does
	// (httpapi.handleCreateProject -> s.projects.Add -> save -> config.Save).
	if err := cr.Projects.Add("alpha", config.ProjectConfig{HostPath: t.TempDir()}, false); err != nil {
		t.Fatalf("project add: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read back config: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, "claude") {
		t.Fatalf("template agent was PERSISTED into the operator's config (detect gate is now dead for it):\n%s", got)
	}
	// The operator's own agent must survive the strip, and the project must be written.
	if !strings.Contains(got, "mine") {
		t.Fatalf("save dropped the operator's own agent:\n%s", got)
	}
	if !strings.Contains(got, "alpha") {
		t.Fatalf("save dropped the project it was called for:\n%s", got)
	}
}

// TestSaveOfAllInjectedAgentsWritesNoAgentsBlock: an operator who never wrote an
// `agents:` block must not grow one (not even an empty `agents: {}`).
func TestSaveOfAllInjectedAgentsWritesNoAgentsBlock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := &config.Config{Storage: config.StorageConfig{Root: t.TempDir()}}
	config.ApplyDefaults(cfg)

	cr, err := Build(cfg, WithAgentDetector(detectorFor("claude")))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cr.Close() }()
	if _, ok := cr.Config().Agents["claude"]; !ok {
		t.Fatal("precondition failed: template claude was not materialized")
	}

	if err := cr.Projects.Add("alpha", config.ProjectConfig{HostPath: t.TempDir()}, false); err != nil {
		t.Fatalf("project add: %v", err)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read back config: %v", err)
	}
	if strings.Contains(string(raw), "agents:") {
		t.Fatalf("save invented an agents block the operator never wrote:\n%s", raw)
	}
}

// TestBuildDetectsExactlyOnce: Build owns the single merge, so it probes the host once.
func TestBuildDetectsExactlyOnce(t *testing.T) {
	d := detectorFor("claude")
	cr, err := Build(resolveTestConfig(t), WithAgentDetector(d))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cr.Close() }()

	if d.calls != 1 {
		t.Fatalf("detector called %d times during Build, want exactly 1", d.calls)
	}
}

// TestReloadWithReGatesTemplatesOnce: a reload is a NEW snapshot, so it re-detects
// exactly once (that is how a newly installed CLI appears / an uninstalled one leaves) —
// and it must re-gate rather than inherit, i.e. the template must not have been promoted
// to an operator declaration by the first pass.
func TestReloadWithReGatesTemplatesOnce(t *testing.T) {
	d := detectorFor("claude")
	cr, err := Build(resolveTestConfig(t), WithAgentDetector(d))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cr.Close() }()
	if d.calls != 1 {
		t.Fatalf("after Build: %d detect calls, want 1", d.calls)
	}

	// Same detector, fresh config snapshot: exactly ONE more probe.
	next := resolveTestConfig(t)
	if err := cr.ReloadWith(next); err != nil {
		t.Fatalf("ReloadWith: %v", err)
	}
	if d.calls != 2 {
		t.Fatalf("after one reload: %d detect calls, want 2 (one per config snapshot)", d.calls)
	}
	if _, ok := cr.Config().Agents["claude"]; !ok {
		t.Fatal("reload lost the template agent")
	}
	// ReloadWith resolves IN PLACE, so the caller's own snapshot shows what was applied
	// (a worker derives its advertised caps from exactly this pointer).
	if _, ok := next.Agents["claude"]; !ok {
		t.Fatal("ReloadWith did not resolve the caller's config in place: advertised caps would differ from what was applied")
	}

	// The CLI goes away -> the injected agent goes with it; the operator's own stays.
	cr.detector = detectorFor()
	if err := cr.ReloadWith(resolveTestConfig(t)); err != nil {
		t.Fatalf("ReloadWith: %v", err)
	}
	if _, ok := cr.Config().Agents["claude"]; ok {
		t.Fatal("template survived a reload whose probe failed (it was promoted to an escape hatch)")
	}
	if _, ok := cr.Config().Agents["mine"]; !ok {
		t.Fatal("reload dropped the operator's own agent")
	}
}

// TestBuildDefaultDetectorIsRealProbe guards the seam itself: production callers that
// pass no option must get the real detector, not the hermetic one used by tests.
func TestBuildDefaultDetectorIsRealProbe(t *testing.T) {
	cr, err := Build(resolveTestConfig(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cr.Close() }()

	if _, isNoop := cr.detector.(agent.NoopDetector); isNoop || cr.detector == nil {
		t.Fatalf("default detector = %T, want the real probe", cr.detector)
	}
}
