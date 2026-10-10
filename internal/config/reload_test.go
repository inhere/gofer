package config

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestReloadResultRoundTrip(t *testing.T) {
	path := t.TempDir() + "/run/reload.json"
	want := ReloadResult{Rev: 7, Path: "config.yaml", Changed: []string{"server"}, RestartRequired: []string{"server.addr"}}
	if err := WriteReloadResult(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReloadResult(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rev != want.Rev || got.Path != want.Path || len(got.RestartRequired) != 1 {
		t.Fatalf("result=%+v, want %+v", got, want)
	}
}

func stubRename(t *testing.T, fn func(from, to string) error) {
	t.Helper()
	oldFn, oldPeriod := renameFile, renameRetryPeriod
	renameFile, renameRetryPeriod = fn, time.Millisecond
	t.Cleanup(func() { renameFile, renameRetryPeriod = oldFn, oldPeriod })
}

func TestWriteReloadResultRetriesBusyRename(t *testing.T) {
	path := t.TempDir() + "/run/reload.json"
	calls := 0
	stubRename(t, func(from, to string) error {
		calls++
		if calls <= 3 {
			return errors.New("file in use")
		}
		return os.Rename(from, to)
	})
	if err := WriteReloadResult(path, ReloadResult{Rev: 4}); err != nil {
		t.Fatalf("write after transient rename failures: %v", err)
	}
	if calls != 4 {
		t.Fatalf("rename calls=%d, want 4", calls)
	}
	if got, err := ReadReloadResult(path); err != nil || got.Rev != 4 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestWriteReloadResultGivesUpAndCleansTmp(t *testing.T) {
	dir := t.TempDir() + "/run"
	path := dir + "/reload.json"
	calls := 0
	stubRename(t, func(from, to string) error { calls++; return errors.New("file in use") })
	if err := WriteReloadResult(path, ReloadResult{Rev: 1}); err == nil {
		t.Fatal("expected error after retries are exhausted")
	}
	if calls != renameRetries+1 {
		t.Fatalf("rename calls=%d, want %d", calls, renameRetries+1)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("leftover files: %v", entries)
	}
}
