package messenger

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUsableDirSkipsMissing: a session's reported cwd can be a removed worktree;
// the messenger must fall back instead of failing to chdir.
func TestUsableDirSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "removed-worktree")
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := usableDir(gone, file, dir); got != dir {
		t.Fatalf("usableDir = %q, want %q", got, dir)
	}
	if got := usableDir(gone, ""); got != "" {
		t.Fatalf("usableDir with no existing dir = %q, want empty", got)
	}
}
