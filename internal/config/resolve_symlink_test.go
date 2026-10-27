//go:build linux || darwin

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectForPathResolvesSymlinks(t *testing.T) {
	realRoot := t.TempDir()
	realProject := filepath.Join(realRoot, "project")
	if err := os.MkdirAll(filepath.Join(realProject, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	linkProject := filepath.Join(linkRoot, "project")

	t.Run("configured symlink cwd real", func(t *testing.T) {
		cfg := &Config{Projects: map[string]ProjectConfig{
			"demo": {HostPath: linkProject},
		}}
		if got, ok := cfg.ProjectForPath(filepath.Join(realProject, "sub")); !ok || got != "demo" {
			t.Fatalf("real cwd under symlink project = (%q, %v), want (demo, true)", got, ok)
		}
	})

	t.Run("configured real cwd symlink", func(t *testing.T) {
		cfg := &Config{Projects: map[string]ProjectConfig{
			"demo": {HostPath: realProject},
		}}
		if got, ok := cfg.ProjectForPath(filepath.Join(linkProject, "sub")); !ok || got != "demo" {
			t.Fatalf("symlink cwd under real project = (%q, %v), want (demo, true)", got, ok)
		}
	})
}
