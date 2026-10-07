package servicemgr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrLockBusy = errors.New("service instance is busy")

// Lock holds an OS file lock for an entire lifecycle operation. The lock file
// is intentionally retained after release: removing it could let a contender
// create a different inode while another process still holds the old one.
type Lock struct {
	f    *os.File
	once sync.Once
	err  error
}

func AcquireLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := tryFileLock(f); err != nil {
		f.Close()
		if errors.Is(err, ErrLockBusy) {
			return nil, fmt.Errorf("%w: %s", ErrLockBusy, path)
		}
		return nil, fmt.Errorf("lock service instance %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.err = errors.Join(unlockFile(l.f), l.f.Close())
	})
	return l.err
}
