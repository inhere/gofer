package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestConfigSaveDoesNotPersistRuntimeOverlays(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "base")
	if err := os.Mkdir(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, config.ProjectOverlayName), []byte("exchange_subdir: overlay-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	original := "projects:\n  base:\n    host_path: " + filepath.ToSlash(baseDir) + "\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if warns := config.ApplyProjectOverlays(cfg); len(warns) != 0 {
		t.Fatalf("overlay warnings: %v", warns)
	}
	if cfg.Projects["base"].ExchangeSubdir != "overlay-only" {
		t.Fatal("overlay was not applied in memory")
	}
	cfg.Storage.Root = filepath.Join(dir, "runtime-storage")
	cfg.Server.WebDir = filepath.Join(dir, "runtime-web")
	webEnabled := false
	cfg.Server.WebEnabled = &webEnabled
	cr, err := Build(cfg, WithConfigPath(path), WithAgentDetector(detectorFor("claude")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cr.Close() })
	if _, ok := cr.Config().Agents["claude"]; !ok {
		t.Fatal("injected agent precondition failed")
	}
	if err := cr.Projects.Add("added", config.ProjectConfig{HostPath: filepath.Join(dir, "added")}, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "added:") {
		t.Fatalf("new project missing:\n%s", got)
	}
	for _, leaked := range []string{"overlay-only", "runtime-web", "runtime-storage", "claude", "web_enabled:", "agents:", "storage:", "server:"} {
		if strings.Contains(got, leaked) {
			t.Errorf("runtime value %q persisted:\n%s", leaked, got)
		}
	}
}
