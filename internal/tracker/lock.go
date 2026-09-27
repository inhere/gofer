package tracker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const lockTTL = 30 * time.Second

type lockBody struct {
	PID  int    `json:"pid"`
	Host string `json:"host"`
	At   string `json:"at"`
}

type Lock struct {
	path string
	body []byte
}

func (s *Store) AcquireLock() (*Lock, error) {
	path := filepath.Join(s.Dir, ".local", "lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(lockBody{PID: os.Getpid(), Host: host, At: Now()})
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		stale, old, err := staleLock(path)
		if err == nil && stale {
			// Check again before unlinking. A replaced lock never belongs to us.
			current, readErr := os.ReadFile(path)
			if readErr == nil && bytes.Equal(old, current) {
				_ = os.Remove(path)
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
	current, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(current, l.body) {
		return errors.New("tracker lock ownership changed")
	}
	return os.Remove(l.path)
}
