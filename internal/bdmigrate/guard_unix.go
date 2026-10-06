//go:build !windows

package bdmigrate

import (
	"errors"
	"os"
	"syscall"
)

// lockHeld probes a lock file with a non-blocking exclusive flock and releases
// it at once. Not being able to take it means another process holds it.
func lockHeld(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
