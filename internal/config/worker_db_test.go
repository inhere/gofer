package config

import (
	"path/filepath"
	"testing"
)

// TestResolveWorkerDBPath covers the three-branch worker db resolution: explicit
// db_path > <root>/<id>.db > <config-dir>/worker/<id>.db. Crucially an explicit
// Root=<config-dir>/worker and the bare default converge on the same path.
func TestResolveWorkerDBPath(t *testing.T) {
	t.Run("explicit db_path wins over root", func(t *testing.T) {
		c := &Config{Storage: StorageConfig{DBPath: "/custom/my.db", Root: "/ignored"}}
		if got := c.ResolveWorkerDBPath("w1"); got != "/custom/my.db" {
			t.Fatalf("got %q, want /custom/my.db", got)
		}
	})

	t.Run("root -> <root>/<id>.db (not gofer.db)", func(t *testing.T) {
		c := &Config{Storage: StorageConfig{Root: "/data/wkr"}}
		want := filepath.Join("/data/wkr", "w-claude.db")
		if got := c.ResolveWorkerDBPath("w-claude"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("default -> <config-dir>/worker/<id>.db", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(EnvConfigDir, dir)
		c := &Config{}
		want := filepath.Join(dir, "worker", "w-claude.db")
		if got := c.ResolveWorkerDBPath("w-claude"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("Root=<config-dir>/worker converges with default", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(EnvConfigDir, dir)
		want := filepath.Join(dir, "worker", "w-claude.db")
		viaRoot := (&Config{Storage: StorageConfig{Root: filepath.Join(dir, "worker")}}).ResolveWorkerDBPath("w-claude")
		viaDefault := (&Config{}).ResolveWorkerDBPath("w-claude")
		if viaRoot != want || viaDefault != want {
			t.Fatalf("viaRoot=%q viaDefault=%q want=%q", viaRoot, viaDefault, want)
		}
	})
}
