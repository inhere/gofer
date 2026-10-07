package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLocalPathAtFrozenDirectory(t *testing.T) {
	root := t.TempDir()
	dirA, dirB := filepath.Join(root, "a 目录"), filepath.Join(root, "b 目录")
	t.Setenv(EnvConfigDir, filepath.Join(root, "ambient"))
	for _, tc := range []struct{ dir, want string }{
		{dirA, filepath.Join(dirA, "logs", "serve.log")},
		{dirB, filepath.Join(dirB, "logs", "serve.log")},
	} {
		got, err := ResolveLocalPathAt("log.file", "{config_dir}/logs/serve.log", tc.dir)
		if err != nil || got != tc.want {
			t.Fatalf("ResolveLocalPathAt(%q) = %q, %v", tc.dir, got, err)
		}
	}
	if got := os.Getenv(EnvConfigDir); got != filepath.Join(root, "ambient") {
		t.Fatalf("ambient config dir changed: %q", got)
	}
	if got, err := ResolveLocalPathAt("log.file", "relative.log", ""); err != nil || got != "relative.log" {
		t.Fatalf("relative path = %q, %v", got, err)
	}
}
