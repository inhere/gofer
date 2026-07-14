package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/wsproto"
)

// workerCfg builds the resolved worker config snapshot exactly as runWorker does,
// so the capability helpers are exercised on the same input production feeds them.
func workerCfg(t *testing.T, agents map[string]config.AgentConfig) *config.Config {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir()) // hermetic db-path resolution
	return workerConfigToConfig(&config.WorkerConfig{WorkerID: "w1", Agents: agents})
}

// briefFor returns the reported brief for key (zero value when absent).
func briefFor(briefs []wsproto.AgentBrief, key string) (wsproto.AgentBrief, bool) {
	for _, b := range briefs {
		if b.Key == key {
			return b, true
		}
	}
	return wsproto.AgentBrief{}, false
}

// TestAgentBriefsIncludesBuiltinExec: a worker with NO agents block at all (the
// canonical exec-only worker) must still advertise the built-in exec agent —
// agent.ResolveAgent makes it runnable regardless, and the hub now treats this
// report as authoritative, so under-reporting would get every exec job rejected.
func TestAgentBriefsIncludesBuiltinExec(t *testing.T) {
	cfg := workerCfg(t, nil)

	briefs := agentBriefs(cfg, nil)
	got, ok := briefFor(briefs, agent.ExecAgentKey)
	if !ok {
		t.Fatalf("built-in exec missing from agent_caps: %+v", briefs)
	}
	if got.Type != agent.TypeExec {
		t.Fatalf("built-in exec Type = %q, want %q", got.Type, agent.TypeExec)
	}
	if got.Interactive {
		t.Fatal("built-in exec must not be reported interactive")
	}
	// The back-compat key list must agree — P3 validates on it.
	if keys := agentKeys(cfg); !reflect.DeepEqual(keys, []string{agent.ExecAgentKey}) {
		t.Fatalf("agents key list = %v, want [%s]", keys, agent.ExecAgentKey)
	}
}

// TestAgentBriefsBareExecBlockKeepsExecType: a declared but bare `exec:` block (no
// explicit `type:`) is normalised to exec by agent.ResolveAgent — the report must
// carry that true type, not the raw config's empty string.
func TestAgentBriefsBareExecBlockKeepsExecType(t *testing.T) {
	cfg := workerCfg(t, map[string]config.AgentConfig{
		agent.ExecAgentKey: {}, // bare `exec:` — no type
	})

	got, ok := briefFor(agentBriefs(cfg, nil), agent.ExecAgentKey)
	if !ok {
		t.Fatal("exec missing from agent_caps")
	}
	if got.Type != agent.TypeExec {
		t.Fatalf("bare exec block reported Type = %q, want %q (the raw map would give \"\")",
			got.Type, agent.TypeExec)
	}
}

// TestAgentBriefsDeclaredCLIAgent: a declared cli-agent reports its real key, type
// and interactive flag (the fields the UI cascade exists for) — alongside the
// always-present built-in exec.
func TestAgentBriefsDeclaredCLIAgent(t *testing.T) {
	cfg := workerCfg(t, map[string]config.AgentConfig{
		"claude": {Type: agent.TypeCLIAgent, Command: "claude", Interactive: true},
		"codex":  {Type: agent.TypeCLIAgent, Command: "codex"},
	})

	briefs := agentBriefs(cfg, nil)
	want := []wsproto.AgentBrief{
		{Key: "claude", Type: agent.TypeCLIAgent, Interactive: true},
		{Key: "codex", Type: agent.TypeCLIAgent, Interactive: false},
		{Key: agent.ExecAgentKey, Type: agent.TypeExec, Interactive: false},
	}
	if !reflect.DeepEqual(briefs, want) {
		t.Fatalf("agent_caps:\n got %+v\nwant %+v", briefs, want)
	}
}

// TestAgentKeysMatchAgentBriefs: the key list and the typed caps are built from the
// same resolved set, so they can never drift (P3 validates on the keys, the UI reads
// the caps — a mismatch would silently accept/reject the wrong agents).
func TestAgentKeysMatchAgentBriefs(t *testing.T) {
	cfg := workerCfg(t, map[string]config.AgentConfig{
		"claude": {Type: agent.TypeCLIAgent, Command: "claude", Interactive: true},
	})

	keys := agentKeys(cfg)
	briefs := agentBriefs(cfg, nil)
	briefKeys := make([]string, 0, len(briefs))
	for _, b := range briefs {
		briefKeys = append(briefKeys, b.Key)
	}
	if !reflect.DeepEqual(keys, briefKeys) {
		t.Fatalf("key-set drift: agents=%v vs agent_caps=%v", keys, briefKeys)
	}
	if !reflect.DeepEqual(keys, []string{"claude", agent.ExecAgentKey}) {
		t.Fatalf("keys = %v, want [claude exec] (sorted, incl. built-in exec)", keys)
	}
}

// TestShippedWorkerExampleReportsExec is the concrete regression lock: the SHIPPED
// config/worker.example.yaml has its entire `agents:` block commented out ("纯 exec
// job 不需要本段") while its project runs exec jobs. That canonical worker must
// advertise exec in BOTH the typed caps and the back-compat key list.
func TestShippedWorkerExampleReportsExec(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	wc, err := loadWorkerConfig(filepath.Join("..", "..", "config", "worker.example.yaml"))
	if err != nil {
		t.Fatalf("load shipped worker.example.yaml: %v", err)
	}
	if len(wc.Agents) != 0 {
		t.Fatalf("fixture drift: worker.example.yaml now declares agents %v — this test "+
			"guards the commented-out (no agents) case", wc.Agents)
	}
	cfg := workerConfigToConfig(wc)
	t.Logf("shipped exec-only worker reports: agents=%v agent_caps=%+v",
		agentKeys(cfg), agentBriefs(cfg, nil))

	if keys := agentKeys(cfg); !reflect.DeepEqual(keys, []string{agent.ExecAgentKey}) {
		t.Fatalf("shipped exec-only worker advertises agents %v, want [%s]",
			keys, agent.ExecAgentKey)
	}
	got, ok := briefFor(agentBriefs(cfg, nil), agent.ExecAgentKey)
	if !ok {
		t.Fatal("shipped exec-only worker advertises NO exec in agent_caps")
	}
	if got.Type != agent.TypeExec {
		t.Fatalf("exec reported Type = %q, want %q", got.Type, agent.TypeExec)
	}
}

// --- P2 T3: detect wired into the register/reload capability report ---

// fakeDetector reports a canned DetectResult per key (a key absent from res probes as
// the zero value: Available=false) and counts its calls, so a test can assert BOTH what
// the worker advertises and how many times the host was probed to get there.
type fakeDetector struct {
	res   map[string]agent.DetectResult
	calls int
}

func (d *fakeDetector) Detect(agents map[string]config.AgentConfig) map[string]agent.DetectResult {
	d.calls++
	out := make(map[string]agent.DetectResult, len(agents))
	for key := range agents {
		out[key] = d.res[key]
	}
	return out
}

// capsFor runs the worker's REAL startup chain — workerConfigToConfig → core.Build (the
// single merge point, carrying the recorder) → workerCaps — and returns what the worker
// would advertise on register.
func capsFor(t *testing.T, wc *config.WorkerConfig, inner agent.Detector) wsproto.Caps {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	det := &availabilityRecorder{inner: inner}
	cfg := workerConfigToConfig(wc)
	cr, err := core.Build(cfg, core.WithAgentDetector(det))
	if err != nil {
		t.Fatalf("core.Build: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })
	return workerCaps(wc, cfg, det.snapshot())
}

// availabilityOf returns the reported availability of key as a printable tri-state.
func availabilityOf(briefs []wsproto.AgentBrief, key string) string {
	b, ok := briefFor(briefs, key)
	if !ok {
		return "MISSING"
	}
	if b.Available == nil {
		return "unknown"
	}
	return fmt.Sprintf("%v", *b.Available)
}

// TestWorkerCapsKeepsEscapeHatchesWhenEveryProbeFails is THE iron-rule lock (P2
// verification #1/#6), shaped exactly like the live w-container-example worker.yaml: three
// cli-agents declared, not one of them carrying a detect block, one pointing at a
// command that cannot exist. Every probe is then made to fail (worst case).
//
// The whole agent set must survive. The failure this guards against was实证-ed: gate the
// caps report on the probe and the report collapses to [exec], after which every
// claude/tty-claude job is rejected with HTTP 400 "agent not on worker" — a live worker
// broken by a binary upgrade with zero config change. Only TEMPLATE-injected agents are
// detect-gated; an operator declaration is an escape hatch and is never withdrawn.
func TestWorkerCapsKeepsEscapeHatchesWhenEveryProbeFails(t *testing.T) {
	wc := &config.WorkerConfig{
		WorkerID: "w-container-example",
		Agents: map[string]config.AgentConfig{
			"claude":     {Type: agent.TypeCLIAgent, Command: "claude", Args: []string{"-p", "{{prompt}}"}},
			"tty-claude": {Type: agent.TypeCLIAgent, Command: "claude", Interactive: true, NoRawCmd: true},
			"tty-demo":   {Type: agent.TypeCLIAgent, Command: "__no_such_cli__", Interactive: true, NoRawCmd: true},
		},
	}

	caps := capsFor(t, wc, &fakeDetector{}) // nothing on this host is available

	want := []string{"claude", agent.ExecAgentKey, "tty-claude", "tty-demo"}
	if !reflect.DeepEqual(caps.Agents, want) {
		t.Fatalf("a failing probe REMOVED operator-declared agents: got %v, want %v\n"+
			"this is the live-breaking regression: the worker would answer 400 \"agent not on worker\"",
			caps.Agents, want)
	}
	for _, key := range want {
		if _, ok := briefFor(caps.AgentCaps, key); !ok {
			t.Fatalf("agent %q missing from agent_caps: %+v", key, caps.AgentCaps)
		}
	}
	// The probe verdict is still REPORTED (display) — it just must not gate.
	if got := availabilityOf(caps.AgentCaps, "claude"); got != "false" {
		t.Fatalf("claude availability = %s, want false (reported, not enforced)", got)
	}
	if b, _ := briefFor(caps.AgentCaps, "tty-claude"); !b.Interactive {
		t.Fatalf("tty-claude lost its interactive flag: %+v", b)
	}
}

// TestWorkerCapsRealProbeKeepsUnfindableEscapeHatch runs the same iron rule through the
// REAL PATH probe (agent.DefaultDetector) instead of a fake, on a command that cannot
// exist on any host — so the "declared but not installed" agent is produced by the
// production detector, not by a test double.
func TestWorkerCapsRealProbeKeepsUnfindableEscapeHatch(t *testing.T) {
	wc := &config.WorkerConfig{
		WorkerID: "w1",
		Agents: map[string]config.AgentConfig{
			"ghost": {Type: agent.TypeCLIAgent, Command: "__no_such_cli__"},
		},
	}

	caps := capsFor(t, wc, agent.DefaultDetector())

	if !slices.Contains(caps.Agents, "ghost") {
		t.Fatalf("the real probe dropped a declared-but-uninstalled agent: %v", caps.Agents)
	}
	if got := availabilityOf(caps.AgentCaps, "ghost"); got != "false" {
		t.Fatalf("ghost availability = %s, want false (unavailable but STILL advertised)", got)
	}
}

// TestWorkerCapsZeroConfigWorker (P2 verification #5): a worker.yaml with NO agents block
// gets the installed templates for free and only those — an uninstalled CLI must not be
// advertised (nobody declared it, so nothing is being withdrawn).
func TestWorkerCapsZeroConfigWorker(t *testing.T) {
	installed := &fakeDetector{res: map[string]agent.DetectResult{
		"claude":     {Available: true, Version: "2.1.208"},
		"tty-claude": {Available: true},
	}}

	caps := capsFor(t, &config.WorkerConfig{WorkerID: "w1"}, installed)

	want := []string{"claude", agent.ExecAgentKey, "tty-claude"}
	if !reflect.DeepEqual(caps.Agents, want) {
		t.Fatalf("zero-config worker caps = %v, want %v", caps.Agents, want)
	}
	for _, notInstalled := range []string{"codex", "opencode", "tty-codex"} {
		if slices.Contains(caps.Agents, notInstalled) {
			t.Fatalf("advertised %q, whose CLI is NOT on this host: %v", notInstalled, caps.Agents)
		}
	}
	b, _ := briefFor(caps.AgentCaps, "claude")
	if b.Available == nil || !*b.Available || b.Version != "2.1.208" {
		t.Fatalf("template agent lost its detect detail: %+v", b)
	}
}

// TestWorkerCapsDetectsOncePerSnapshot (P2 verification #3, T3 invariant): the whole
// startup + one reload probes the host exactly twice — once per config snapshot — and
// each capability report carries THAT pass's result. Building the report from a second
// probe would let the worker advertise a set it never applied.
func TestWorkerCapsDetectsOncePerSnapshot(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	yaml := "worker_id: w1\nagents:\n  mine:\n    type: cli-agent\n    command: mine\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write worker.yaml: %v", err)
	}

	d := &fakeDetector{res: map[string]agent.DetectResult{"claude": {Available: true, Version: "9.9"}}}
	det := &availabilityRecorder{inner: d}

	wc, err := loadWorkerConfig(path)
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	cfg := workerConfigToConfig(wc)
	cr, err := core.Build(cfg, core.WithAgentDetector(det))
	if err != nil {
		t.Fatalf("core.Build: %v", err)
	}
	defer func() { _ = cr.Close() }()

	if d.calls != 1 {
		t.Fatalf("startup probed the host %d times, want exactly 1", d.calls)
	}
	caps := workerCaps(wc, cfg, det.snapshot())
	if b, ok := briefFor(caps.AgentCaps, "claude"); !ok || b.Available == nil || !*b.Available || b.Version != "9.9" {
		t.Fatalf("register caps carry no detect detail: %+v", caps.AgentCaps)
	}

	// One reload = one more snapshot = exactly one more probe, and the receipt reports
	// that pass — not a third one.
	rcaps, err := newWorkerReloadFn(cr, det, path, "w1")()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if d.calls != 2 {
		t.Fatalf("startup + one reload probed the host %d times, want exactly 2 (one per snapshot)", d.calls)
	}
	if b, ok := briefFor(rcaps.AgentCaps, "claude"); !ok || b.Available == nil || !*b.Available {
		t.Fatalf("reload caps lost the detect detail: %+v", rcaps.AgentCaps)
	}
	if got := availabilityOf(rcaps.AgentCaps, "mine"); got != "false" {
		t.Fatalf("declared-but-missing agent 'mine' availability = %s, want false", got)
	}
	if !slices.Contains(rcaps.Agents, "mine") {
		t.Fatalf("reload dropped the declared agent: %v", rcaps.Agents)
	}
}

// TestAgentBriefsUnprobedAgentIsUnknown: with no detect result at hand, availability is
// left UNKNOWN (nil) rather than defaulted to false — the same wire shape an old worker
// produces, and the reason the field is a *bool.
func TestAgentBriefsUnprobedAgentIsUnknown(t *testing.T) {
	cfg := workerCfg(t, map[string]config.AgentConfig{
		"claude": {Type: agent.TypeCLIAgent, Command: "claude"},
	})

	for _, b := range agentBriefs(cfg, nil) {
		if b.Available != nil {
			t.Fatalf("agent %q reported Available=%v with no probe result; want nil (unknown)",
				b.Key, *b.Available)
		}
	}
}
