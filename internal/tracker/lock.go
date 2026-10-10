package tracker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"
)

const lockTTL = 30 * time.Second

// lockWait is how long AcquireLock waits on a busy lock; a var so tests can shorten it.
var lockWait = 10 * time.Second

// lockGOOS and openLockExcl are test seams for the Windows contention path below.
var (
	lockGOOS     = runtime.GOOS
	openLockExcl = func(path string) (*os.File, error) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
)

// lockContended reports whether an exclusive create failed because another process
// holds — or is just releasing — the lock. On Windows a lock file whose removal is
// still pending (another process deleted it while a third had it open to check
// staleness) answers the create with "Access is denied", not "exists"; that is the
// same busy lock and must be retried, not reported (gofer-r7am, seen in a full
// Windows run of TestTrackerLockContention).
func lockContended(err error) (contended, readable bool) {
	if errors.Is(err, os.ErrExist) {
		return true, true
	}
	if lockGOOS == "windows" && errors.Is(err, os.ErrPermission) {
		return true, false
	}
	return false, false
}

type lockBody struct {
	PID  int    `json:"pid"`
	Host string `json:"host"`
	At   string `json:"at"`
	// Seq makes every acquisition's body unique, even two in one process within the
	// clock's resolution (coarse on Windows): waiters tell a new holder by the body,
	// and Release only removes the exact body it wrote.
	Seq uint64 `json:"seq,omitempty"`
}

var lockSeq atomic.Uint64

type Lock struct {
	path string
	body []byte
}

var errLockChanged = errors.New("tracker lock ownership changed")

func (s *Store) AcquireLock() (*Lock, error) {
	path := filepath.Join(s.Dir, ".local", "lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(lockBody{PID: os.Getpid(), Host: host, At: Now(), Seq: lockSeq.Add(1)})
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	// The deadline bounds how long one holder may keep the lock, not the whole wait:
	// every time the waiter sees a different holder the lock is making progress, so
	// the wait restarts. A fixed overall deadline let a waiter that kept losing the
	// race to a re-acquiring holder report "busy" on a live lock (gofer-rgnw).
	deadline := time.Now().Add(lockWait)
	var seen []byte
	for {
		f, err := openLockExcl(path)
		if err == nil {
			if _, err = f.Write(body); err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				os.Remove(path)
				return nil, err
			}
			return &Lock{path: path, body: body}, nil
		}
		contended, readable := lockContended(err)
		if !contended {
			return nil, err
		}
		if !readable {
			// A pending delete: nothing to inspect, just wait for it to go.
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("tracker lock busy: %s", path)
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		stale, old, err := staleLock(path)
		if err == nil && !bytes.Equal(old, seen) {
			seen = old
			deadline = time.Now().Add(lockWait)
		}
		if err == nil && stale {
			if removeErr := removeMatchingLock(path, old); removeErr != nil && !errors.Is(removeErr, errLockChanged) {
				return nil, fmt.Errorf("reclaim expired tracker lock: %w", removeErr)
			}
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("tracker lock busy: %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func staleLock(path string) (bool, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, nil, err
	}
	var body lockBody
	if json.Unmarshal(data, &body) == nil {
		if at, err := time.Parse(time.RFC3339Nano, body.At); err == nil {
			return time.Since(at) > lockTTL, data, nil
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, data, err
	}
	return time.Since(info.ModTime()) > lockTTL, data, nil
}

func (l *Lock) Release() error {
	return removeMatchingLock(l.path, l.body)
}

// A contender may be reading the file when Windows attempts removal. Retry only
// that local sharing window, checking ownership again before every attempt.
func removeMatchingLock(path string, expected []byte) error {
	deadline := time.Now().Add(time.Second)
	for {
		current, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil && !bytes.Equal(current, expected) {
			return errLockChanged
		}
		if err == nil {
			err = os.Remove(path)
			if err == nil || errors.Is(err, os.ErrNotExist) {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
