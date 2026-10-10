package tracker

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// On Windows an exclusive create of a lock file whose removal is still pending fails
// with "Access is denied"; AcquireLock must wait and retry like for an existing lock.
func TestAcquireLockRetriesWindowsPendingDelete(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: filepath.Join(dir, ".gofer", "tracker")}
	oldGOOS, oldOpen := lockGOOS, openLockExcl
	t.Cleanup(func() { lockGOOS, openLockExcl = oldGOOS, oldOpen })
	lockGOOS = "windows"
	denied := 3
	openLockExcl = func(path string) (*os.File, error) {
		if denied > 0 {
			denied--
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
		}
		return oldOpen(path)
	}
	l, err := s.AcquireLock()
	if err != nil {
		t.Fatalf("AcquireLock gave up on a pending delete: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
}

// Elsewhere a permission error is real and is reported at once.
func TestAcquireLockReportsPermissionOffWindows(t *testing.T) {
	oldGOOS := lockGOOS
	t.Cleanup(func() { lockGOOS = oldGOOS })
	lockGOOS = "linux"
	if c, _ := lockContended(&fs.PathError{Op: "open", Err: fs.ErrPermission}); c {
		t.Fatal("permission error treated as contention off Windows")
	}
}
