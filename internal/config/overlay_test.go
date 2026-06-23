package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
func boolPtr(b bool) *bool    { return &b }

// TestMergeProjectConfig_AllNilNoChange verifies an empty overlay (all fields
// nil) leaves base untouched (D8 "absent != zero").
func TestMergeProjectConfig_AllNilNoChange(t *testing.T) {
	base := ProjectConfig{
		HostPath:          "/abs/SIV",
		ContainerPath:     "/work/SIV",
		ExchangeSubdir:    "tmp",
		ResultSubdir:      "gofer",
		DefaultAgent:      "claude",
		AllowedAgents:     []string{"claude"},
		AllowedRunners:    []string{"local"},
		AllowExec:         true,
		MaxConcurrentJobs: 3,
		CaptureDiff:       boolPtr(true),
		NotifyEnabled:     boolPtr(true),
	}
	got := MergeProjectConfig(base, ProjectOverlay{})
	if !reflect.DeepEqual(got, base) {
		t.Fatalf("empty overlay must not change base:\n got=%+v\nbase=%+v", got, base)
	}
}

// TestMergeProjectConfig_NonNilOverrides verifies every whitelisted field is
// overridden by a non-nil overlay value, including a *bool false (explicit
// disable must win over a nil/true base).
func TestMergeProjectConfig_NonNilOverrides(t *testing.T) {
	base := ProjectConfig{
		ExchangeSubdir:    "tmp",
		ResultSubdir:      "gofer",
		DefaultAgent:      "claude",
		MaxConcurrentJobs: 1,
		CaptureDiff:       boolPtr(true),
		NotifyEnabled:     nil,
	}
	ov := ProjectOverlay{
		ExchangeSubdir:    strPtr("ex2"),
		ResultSubdir:      strPtr("out"),
		DefaultAgent:      strPtr("codex"),
		MaxConcurrentJobs: intPtr(5),
		CaptureDiff:       boolPtr(false), // explicit false must override base true
		NotifyEnabled:     boolPtr(false), // explicit false must override base nil
	}
	got := MergeProjectConfig(base, ov)

	if got.ExchangeSubdir != "ex2" {
		t.Errorf("ExchangeSubdir = %q, want ex2", got.ExchangeSubdir)
	}
	if got.ResultSubdir != "out" {
		t.Errorf("ResultSubdir = %q, want out", got.ResultSubdir)
	}
	if got.DefaultAgent != "codex" {
		t.Errorf("DefaultAgent = %q, want codex", got.DefaultAgent)
	}
	if got.MaxConcurrentJobs != 5 {
		t.Errorf("MaxConcurrentJobs = %d, want 5", got.MaxConcurrentJobs)
	}
	if got.CaptureDiff == nil || *got.CaptureDiff != false {
		t.Errorf("CaptureDiff = %v, want explicit false", got.CaptureDiff)
	}
	if got.NotifyEnabled == nil || *got.NotifyEnabled != false {
		t.Errorf("NotifyEnabled = %v, want explicit false", got.NotifyEnabled)
	}
}

// TestMergeProjectConfig_NeverTouchesAdmissionFields is the D2 guard at the merge
// level: ProjectOverlay has no admission/anchor fields, so they must survive any
// overlay value unchanged.
func TestMergeProjectConfig_NeverTouchesAdmissionFields(t *testing.T) {
	base := ProjectConfig{
		HostPath:       "/abs/SIV",
		ContainerPath:  "/work/SIV",
		AllowedAgents:  []string{"claude"},
		AllowedRunners: []string{"local"},
		AllowExec:      true,
	}
	got := MergeProjectConfig(base, ProjectOverlay{
		DefaultAgent: strPtr("codex"),
		ResultSubdir: strPtr("out"),
	})
	if got.HostPath != base.HostPath {
		t.Errorf("HostPath changed: %q", got.HostPath)
	}
	if got.ContainerPath != base.ContainerPath {
		t.Errorf("ContainerPath changed: %q", got.ContainerPath)
	}
	if len(got.AllowedAgents) != 1 || got.AllowedAgents[0] != "claude" {
		t.Errorf("AllowedAgents changed: %v", got.AllowedAgents)
	}
	if len(got.AllowedRunners) != 1 || got.AllowedRunners[0] != "local" {
		t.Errorf("AllowedRunners changed: %v", got.AllowedRunners)
	}
	if !got.AllowExec {
		t.Errorf("AllowExec changed: %v", got.AllowExec)
	}
}

// writeOverlay writes a .gofer.project.yaml into dir and fails the test on error.
func writeOverlay(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, ProjectOverlayName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write overlay %s: %v", path, err)
	}
}

// TestApplyProjectOverlays_MergesFromFile verifies a valid overlay file is read
// and merged into cfg.Projects[key].
func TestApplyProjectOverlays_MergesFromFile(t *testing.T) {
	dir := t.TempDir()
	writeOverlay(t, dir, "result_subdir: out\ndefault_agent: claude\nmax_concurrent_jobs: 7\n")

	cfg := &Config{Projects: map[string]ProjectConfig{
		"siv": {HostPath: dir, ResultSubdir: "gofer", DefaultAgent: "codex", MaxConcurrentJobs: 1},
	}}
	warns := ApplyProjectOverlays(cfg)
	if len(warns) != 0 {
		t.Fatalf("expected no warnings, got %v", warns)
	}
	p := cfg.Projects["siv"]
	if p.ResultSubdir != "out" {
		t.Errorf("ResultSubdir = %q, want out", p.ResultSubdir)
	}
	if p.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", p.DefaultAgent)
	}
	if p.MaxConcurrentJobs != 7 {
		t.Errorf("MaxConcurrentJobs = %d, want 7", p.MaxConcurrentJobs)
	}
}

// TestApplyProjectOverlays_MissingFileNoChange verifies a project without an
// overlay file keeps its global values and yields no warning (D9 backward compat).
func TestApplyProjectOverlays_MissingFileNoChange(t *testing.T) {
	dir := t.TempDir() // no overlay written
	cfg := &Config{Projects: map[string]ProjectConfig{
		"siv": {HostPath: dir, ResultSubdir: "gofer", DefaultAgent: "codex"},
	}}
	warns := ApplyProjectOverlays(cfg)
	if len(warns) != 0 {
		t.Fatalf("missing file must not warn, got %v", warns)
	}
	p := cfg.Projects["siv"]
	if p.ResultSubdir != "gofer" || p.DefaultAgent != "codex" {
		t.Errorf("project changed despite no overlay: %+v", p)
	}
}

// TestApplyProjectOverlays_BadYAMLWarnsAndKeepsGlobal verifies a malformed
// overlay produces a warning and leaves the project at its global value.
func TestApplyProjectOverlays_BadYAMLWarnsAndKeepsGlobal(t *testing.T) {
	dir := t.TempDir()
	// Invalid: result_subdir wants a string but gets a mapping → decode error.
	writeOverlay(t, dir, "result_subdir: {a: 1, : :\n  oops")

	cfg := &Config{Projects: map[string]ProjectConfig{
		"siv": {HostPath: dir, ResultSubdir: "gofer"},
	}}
	warns := ApplyProjectOverlays(cfg)
	if len(warns) == 0 {
		t.Fatalf("bad YAML must warn")
	}
	if p := cfg.Projects["siv"]; p.ResultSubdir != "gofer" {
		t.Errorf("bad overlay must keep global value, got ResultSubdir=%q", p.ResultSubdir)
	}
}

// TestApplyProjectOverlays_ForbiddenKeyWarnsAndAdmissionUntouched is the core D2
// guard: an overlay attempting to set allowed_agents must warn AND must NOT
// change the project's AllowedAgents (admission stays a global-only truth).
func TestApplyProjectOverlays_ForbiddenKeyWarnsAndAdmissionUntouched(t *testing.T) {
	dir := t.TempDir()
	// Overlay tries to self-grant codex via allowed_agents + flip allow_exec, and
	// also carries a legitimate result_subdir that should still apply.
	writeOverlay(t, dir, "allowed_agents:\n  - codex\nallow_exec: true\nresult_subdir: out\n")

	cfg := &Config{Projects: map[string]ProjectConfig{
		"siv": {
			HostPath:      dir,
			ResultSubdir:  "gofer",
			AllowedAgents: []string{"claude"},
			AllowExec:     false,
		},
	}}
	warns := ApplyProjectOverlays(cfg)

	// Must surface the forbidden keys.
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "allowed_agents") {
		t.Errorf("expected warning about allowed_agents, got: %v", warns)
	}
	if !strings.Contains(joined, "allow_exec") {
		t.Errorf("expected warning about allow_exec, got: %v", warns)
	}

	p := cfg.Projects["siv"]
	// D2: admission fields untouched.
	if len(p.AllowedAgents) != 1 || p.AllowedAgents[0] != "claude" {
		t.Errorf("AllowedAgents was modified by overlay: %v (D2 violation)", p.AllowedAgents)
	}
	if p.AllowExec {
		t.Errorf("AllowExec was flipped by overlay (D2 violation)")
	}
	// The legitimate whitelisted field still merges.
	if p.ResultSubdir != "out" {
		t.Errorf("ResultSubdir = %q, want out (legit field should still merge)", p.ResultSubdir)
	}
}

// TestApplyProjectOverlays_ContainerPathPreferred verifies the read dir is
// ContainerPath when both paths are set: an overlay placed only in the container
// dir is found and merged (D4 — gofer runs in-container).
func TestApplyProjectOverlays_ContainerPathPreferred(t *testing.T) {
	hostDir := t.TempDir()
	containerDir := t.TempDir()
	// Overlay ONLY in the container dir.
	writeOverlay(t, containerDir, "result_subdir: from_container\n")
	// A decoy in the host dir that must NOT be read.
	writeOverlay(t, hostDir, "result_subdir: from_host\n")

	cfg := &Config{Projects: map[string]ProjectConfig{
		"siv": {HostPath: hostDir, ContainerPath: containerDir, ResultSubdir: "gofer"},
	}}
	if warns := ApplyProjectOverlays(cfg); len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if p := cfg.Projects["siv"]; p.ResultSubdir != "from_container" {
		t.Errorf("ResultSubdir = %q, want from_container (ContainerPath must win, D4)", p.ResultSubdir)
	}
}

// TestApplyProjectOverlays_FallbackToHostPath verifies HostPath is used when
// ContainerPath is empty.
func TestApplyProjectOverlays_FallbackToHostPath(t *testing.T) {
	hostDir := t.TempDir()
	writeOverlay(t, hostDir, "result_subdir: from_host\n")

	cfg := &Config{Projects: map[string]ProjectConfig{
		"siv": {HostPath: hostDir, ResultSubdir: "gofer"},
	}}
	if warns := ApplyProjectOverlays(cfg); len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if p := cfg.Projects["siv"]; p.ResultSubdir != "from_host" {
		t.Errorf("ResultSubdir = %q, want from_host (HostPath fallback)", p.ResultSubdir)
	}
}

// TestApplyProjectOverlays_NoPathSkipped verifies a project with neither path set
// is skipped without warning.
func TestApplyProjectOverlays_NoPathSkipped(t *testing.T) {
	cfg := &Config{Projects: map[string]ProjectConfig{
		"siv": {ResultSubdir: "gofer"},
	}}
	if warns := ApplyProjectOverlays(cfg); len(warns) != 0 {
		t.Fatalf("project with no path must be skipped silently, got %v", warns)
	}
	if p := cfg.Projects["siv"]; p.ResultSubdir != "gofer" {
		t.Errorf("project changed: %+v", p)
	}
}
