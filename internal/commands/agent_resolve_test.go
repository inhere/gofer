package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
)

// cmdCountingDetector reports the keys in avail as available and counts its calls.
type cmdCountingDetector struct {
	avail map[string]bool
	calls int
}

func (d *cmdCountingDetector) Detect(agents map[string]config.AgentConfig) map[string]agent.DetectResult {
	d.calls++
	out := make(map[string]agent.DetectResult, len(agents))
	for key := range agents {
		out[key] = agent.DetectResult{Available: d.avail[key]}
	}
	return out
}

// TestWorkerStartupMergesExactlyOnce walks the worker's REAL startup chain
// (workerConfigToConfig -> core.Build -> workerCaps) and pins P2 verification #3:
// the host is probed exactly once and the templates are merged exactly once.
//
// The failure this guards against: if workerConfigToConfig merged too, Build's pass
// would see the injected keys already present in cfg.Agents, read them as OPERATOR
// declarations, and promote them to escape hatches — leaving the detect gate effective
// only on the first of the two passes.
func TestWorkerStartupMergesExactlyOnce(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	wc := &config.WorkerConfig{
		WorkerID: "w1",
		Agents: map[string]config.AgentConfig{
			"mine": {Type: agent.TypeCLIAgent, Command: "mine"},
		},
		Storage: config.StorageConfig{Root: t.TempDir()},
	}
	d := &cmdCountingDetector{avail: map[string]bool{"claude": true}}

	// Step 1: structure mapping only — it must NOT detect and must NOT merge.
	wcfg := workerConfigToConfig(wc)
	if d.calls != 0 {
		t.Fatalf("workerConfigToConfig ran detect (%d calls); it must be a pure structure mapping", d.calls)
	}
	if _, ok := wcfg.Agents["claude"]; ok {
		t.Fatal("workerConfigToConfig merged a template; the merge belongs to core.Build alone")
	}

	// Step 2: core.Build — the single merge point.
	cr, err := core.Build(wcfg, core.WithAgentDetector(d))
	if err != nil {
		t.Fatalf("core.Build: %v", err)
	}
	defer func() { _ = cr.Close() }()

	if d.calls != 1 {
		t.Fatalf("worker startup detected %d times, want exactly 1", d.calls)
	}

	// Step 3: the caps report is derived from the SAME snapshot Build resolved, so what
	// the worker advertises is exactly what it will accept on dispatch.
	caps := workerCaps(wc, wcfg, nil)
	if !slices.Contains(caps.Agents, "claude") {
		t.Fatalf("advertised caps miss the materialized template: %v", caps.Agents)
	}
	for _, want := range []string{"mine", agent.ExecAgentKey} {
		if !slices.Contains(caps.Agents, want) {
			t.Fatalf("advertised caps lost %q: %v", want, caps.Agents)
		}
	}

	// A reload is one more snapshot => exactly one more probe (never two).
	next := workerConfigToConfig(wc)
	if err := cr.ReloadWith(next); err != nil {
		t.Fatalf("ReloadWith: %v", err)
	}
	if d.calls != 2 {
		t.Fatalf("after one reload: %d detect calls, want 2 (one per config snapshot)", d.calls)
	}
	if !slices.Contains(workerCaps(wc, next, nil).Agents, "claude") {
		t.Fatal("reload caps lost the materialized template")
	}
}

// TestAgentListSeesTemplateAgents pins P2 verification #4 for the ONE enumeration path
// that never builds a Core: `gofer agent list` (loadAgentRegistry). Before T0-C it ran
// config.Load + agent.NewRegistry directly, so serve would happily run a template agent
// while an operator debugging the box with `gofer agent list` was told it did not exist.
//
// Hermetic: a stub `claude` is planted on PATH, so the result does not depend on what is
// installed on the machine running `go test`.
func TestAgentListSeesTemplateAgents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH stub needs an executable bit; the LookPath/PATHEXT variant is covered by T2")
	}
	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("plant stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := "agents:\n  mine:\n    type: cli-agent\n    command: mine\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	reg, err := loadAgentRegistry(cfgPath)
	if err != nil {
		t.Fatalf("loadAgentRegistry: %v", err)
	}
	names := reg.Names()

	if !slices.Contains(names, "claude") {
		t.Fatalf("`gofer agent list` misses the template agent serve would happily run: %v", names)
	}
	if !slices.Contains(names, "mine") || !slices.Contains(names, agent.ExecAgentKey) {
		t.Fatalf("`gofer agent list` lost a declared/built-in agent: %v", names)
	}

	// And the resolved definition is the template's, not an empty shell.
	ac, ok := reg.Get("claude")
	if !ok || ac.Command != "claude" || len(ac.Args) == 0 {
		t.Fatalf("template agent resolved to a useless definition: %+v", ac)
	}
}
