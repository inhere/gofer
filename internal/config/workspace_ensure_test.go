package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureWorkspaceDirCreatesMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ws", "nested")
	t.Setenv(EnvWorkspace, dir)
	got, created, err := EnsureWorkspaceDir()
	if err != nil || !created || got != dir {
		t.Fatalf("EnsureWorkspaceDir = %q, %v, %v", got, created, err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("workspace not created: %v", err)
	}
	// Second call: already there, not reported as created.
	if _, created, err := EnsureWorkspaceDir(); err != nil || created {
		t.Fatalf("second call created=%v err=%v", created, err)
	}
}

func TestEnsureWorkspaceDirFailureIsReported(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvWorkspace, filepath.Join(file, "ws")) // parent is a file
	if _, _, err := EnsureWorkspaceDir(); err == nil {
		t.Fatal("expected an error when the parent is a file")
	}
}
