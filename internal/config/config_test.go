package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadDefaultsWhenMissing verifies a missing config yields a defaulted empty
// Config and an empty path without error (so `project add` can bootstrap).
func TestLoadDefaultsWhenMissing(t *testing.T) {
	// Point lookup at a non-existent explicit path's parent by using an empty
	// explicit path and isolating env + cwd.
	t.Setenv(EnvConfigPath, "")
	t.Setenv(EnvConfigDir, "") // isolate from an ambient GOFER_CONFIG_DIR (wins over HOME)
	dir := t.TempDir()
	chdir(t, dir)
	t.Setenv("HOME", dir) // user-level candidate won't exist either

	cfg, path, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path != "" {
		t.Fatalf("expected empty path, got %q", path)
	}
	if cfg.Server.Addr != DefaultAddr {
		t.Errorf("addr default = %q, want %q", cfg.Server.Addr, DefaultAddr)
	}
	if cfg.Storage.DefaultExchangeSubdir != DefaultExchangeSubdir {
		t.Errorf("exchange default = %q", cfg.Storage.DefaultExchangeSubdir)
	}
	if cfg.Storage.DefaultResultSubdir != DefaultResultSubdir {
		t.Errorf("result default = %q", cfg.Storage.DefaultResultSubdir)
	}
	if cfg.Projects == nil || cfg.Agents == nil || cfg.Runners == nil {
		t.Error("maps should be initialized")
	}
}

// TestLoadExplicitAndDecode verifies decoding into the typed model + defaults.
func TestLoadExplicitAndDecode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
server:
  token_env: MY_TOKEN
projects:
  demo:
    host_path: /tmp/demo
    default_agent: codex
    allowed_agents: [codex, exec]
agents:
  codex:
    type: cli-agent
    command: codex
`)
	cfg, path, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path == "" {
		t.Fatal("expected resolved path")
	}
	if cfg.Server.TokenEnv != "MY_TOKEN" {
		t.Errorf("token_env = %q", cfg.Server.TokenEnv)
	}
	// defaults still applied
	if cfg.Server.Addr != DefaultAddr {
		t.Errorf("addr default = %q", cfg.Server.Addr)
	}
	dp, ok := cfg.Projects["demo"]
	if !ok {
		t.Fatal("missing project demo")
	}
	if dp.HostPath != "/tmp/demo" || dp.DefaultAgent != "codex" {
		t.Errorf("project decoded wrong: %+v", dp)
	}
	if len(dp.AllowedAgents) != 2 {
		t.Errorf("allowed_agents = %v", dp.AllowedAgents)
	}
}

// TestResolveLookupOrder verifies explicit > env > cwd file ordering.
func TestResolveLookupOrder(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	home := filepath.Join(dir, "home")
	t.Setenv("HOME", home)

	// cwd file (highest-priority current-dir name)
	cwdFile := filepath.Join(dir, CurrentDirConfigNames[0])
	write(t, cwdFile, "server:\n  addr: a\n")
	// env file
	envFile := filepath.Join(dir, "env.yaml")
	write(t, envFile, "server:\n  addr: b\n")
	// explicit file
	expFile := filepath.Join(dir, "explicit.yaml")
	write(t, expFile, "server:\n  addr: c\n")

	// explicit wins
	t.Setenv(EnvConfigPath, envFile)
	got, err := Resolve(expFile)
	if err != nil {
		t.Fatal(err)
	}
	if got != mustAbs(t, expFile) {
		t.Errorf("explicit: got %q", got)
	}

	// env wins over cwd
	got, err = Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != mustAbs(t, envFile) {
		t.Errorf("env: got %q", got)
	}

	// cwd used when env empty
	t.Setenv(EnvConfigPath, "")
	got, err = Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(t, got, cwdFile) {
		t.Errorf("cwd: got %q", got)
	}
}

// TestResolveCurrentDirLocalWins verifies the per-directory override: when both
// .gofer.local.yaml and .gofer.yaml exist in the cwd, the .local one wins (it is
// first in CurrentDirConfigNames).
func TestResolveCurrentDirLocalWins(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv(EnvConfigPath, "")

	local := filepath.Join(dir, CurrentDirConfigNames[0]) // .gofer.local.yaml
	base := filepath.Join(dir, CurrentDirConfigNames[1])  // .gofer.yaml
	write(t, base, "server:\n  addr: base\n")
	write(t, local, "server:\n  addr: local\n")

	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(t, got, local) {
		t.Errorf("expected .gofer.local.yaml to win, got %q", got)
	}
}

// TestSavePreservesUnknownTopKeys is the §12 critical test: an unknown top-level
// field plus an existing project must survive a `project add`-style rewrite.
func TestSavePreservesUnknownTopKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `custom_top: 123
notes:
  - keep me
projects:
  a:
    host_path: /x
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Simulate `project add b`.
	cfg.Projects["b"] = ProjectConfig{HostPath: "/y"}
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out := read(t, p)
	if !strings.Contains(out, "custom_top: 123") {
		t.Errorf("unknown top key custom_top lost:\n%s", out)
	}
	if !strings.Contains(out, "keep me") {
		t.Errorf("unknown top key notes lost:\n%s", out)
	}
	// New project present, old project retained.
	reloaded, _, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reloaded.Projects["a"]; !ok {
		t.Error("project a lost after save")
	}
	if _, ok := reloaded.Projects["b"]; !ok {
		t.Error("project b not saved")
	}
}

// TestSaveCreatesFileAndDirs verifies Save creates parent dirs for a new file.
func TestSaveCreatesFileAndDirs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", "deep", "config.yaml")
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.Projects["x"] = ProjectConfig{HostPath: "/x"}
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

// helpers

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func samePath(t *testing.T, got, want string) bool {
	t.Helper()
	gotPath := mustAbs(t, got)
	wantPath := mustAbs(t, want)
	if gotEval, err := filepath.EvalSymlinks(gotPath); err == nil {
		gotPath = gotEval
	}
	if wantEval, err := filepath.EvalSymlinks(wantPath); err == nil {
		wantPath = wantEval
	}
	return gotPath == wantPath
}
