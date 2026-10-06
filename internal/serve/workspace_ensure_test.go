package serve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestEnsureDefaultWorkspaceCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing-ws")
	t.Setenv(config.EnvWorkspace, dir)
	ensureDefaultWorkspace()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("default workspace not created: %v", err)
	}
}

func TestEnsureDefaultWorkspaceFailureDoesNotPanic(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvWorkspace, filepath.Join(file, "ws"))
	ensureDefaultWorkspace() // warns only
}
