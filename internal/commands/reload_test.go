package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// writeConfig writes cfg to path via config.Save and fails the test on error.
func writeConfig(t *testing.T, path string, cfg *config.Config) {
	t.Helper()
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// TestCoreReloadAddsProject verifies Core.Reload re-loads the config file and
// swaps the new projects into the registries and job service (C3).
func TestCoreReloadAddsProject(t *testing.T) {
	host := t.TempDir()
	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "bridge.yaml")

	writeConfig(t, cfgPath, &config.Config{
		Storage:  config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"alpha": {HostPath: host}},
	})
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	core, err := buildCore(cfg)
	if err != nil {
		t.Fatalf("buildCore: %v", err)
	}
	defer func() { _ = core.Close() }()

	if _, err := core.Projects.Get("beta"); err == nil {
		t.Fatal("beta should not exist before reload")
	}

	// Rewrite the file adding beta, then reload from the same path.
	writeConfig(t, cfgPath, &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"alpha": {HostPath: host},
			"beta":  {HostPath: host},
		},
	})
	if err := core.Reload(cfgPath); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := core.Projects.Get("beta"); err != nil {
		t.Fatalf("beta should exist after reload: %v", err)
	}
}

// TestCoreReloadFailSafeKeepsOldConfig verifies that a reload from an
// unloadable (corrupt) config keeps the previous config intact (no partial
// apply) and returns an error (C3 fail-safe).
func TestCoreReloadFailSafeKeepsOldConfig(t *testing.T) {
	host := t.TempDir()
	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "bridge.yaml")

	writeConfig(t, cfgPath, &config.Config{
		Storage:  config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"alpha": {HostPath: host}},
	})
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	core, err := buildCore(cfg)
	if err != nil {
		t.Fatalf("buildCore: %v", err)
	}
	defer func() { _ = core.Close() }()

	// Corrupt the config file so config.Load fails to decode it.
	if err := os.WriteFile(cfgPath, []byte("{ this is : not valid yaml :::\n  - ]["), 0o644); err != nil {
		t.Fatalf("write corrupt config: %v", err)
	}

	if err := core.Reload(cfgPath); err == nil {
		t.Fatal("reload of corrupt config should return an error")
	}

	// Old config must survive across every component (no partial apply).
	if _, err := core.Projects.Get("alpha"); err != nil {
		t.Fatalf("alpha must survive a failed reload (projects): %v", err)
	}
	if core.Cfg.Projects["alpha"].HostPath != host {
		t.Fatal("Core.Cfg must still reference the old config after a failed reload")
	}
}
