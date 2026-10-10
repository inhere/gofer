package tracker

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/wait"
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

// A holder that keeps re-acquiring the lock is progress, not a stuck lock: a waiter
// must not report "busy" just because its own turn took longer than lockWait while
// the lock changed hands the whole time (gofer-rgnw, Windows TestTrackerLockContention).
func TestAcquireLockWaitsWhileHolderMakesProgress(t *testing.T) {
	s := testStore(t)
	oldWait := lockWait
	t.Cleanup(func() { lockWait = oldWait })
	lockWait = 200 * time.Millisecond

	waiterDone := make(chan struct{})
	busyUntil := time.Now().Add(5 * lockWait)
	holderErr := make(chan error, 1)
	go func() {
		// Keep the lock nearly always held — each turn simulates a slow write
		// (Defender-scanned disk) and re-acquires at once — for well past lockWait.
		for time.Now().Before(busyUntil) {
			select {
			case <-waiterDone:
				holderErr <- nil
				return
			default:
			}
			l, err := s.AcquireLock()
			if err != nil {
				holderErr <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
			if err := l.Release(); err != nil {
				holderErr <- err
				return
			}
		}
		holderErr <- nil
	}()

	// Start waiting only once the holder owns the lock.
	lockPath := filepath.Join(s.Dir, ".local", "lock")
	wait.For(t, 5*time.Second, "holder owns the lock", func() (bool, any) {
		_, err := os.Stat(lockPath)
		return err == nil, err
	})
	l, err := s.AcquireLock()
	close(waiterDone)
	if err != nil {
		t.Fatalf("waiter gave up while the lock kept changing hands: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-holderErr; err != nil {
		t.Fatal(err)
	}
}
