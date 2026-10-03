package workerupgrade

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func TestStageComputesDigestItself(t *testing.T) {
	m := New(t.TempDir())
	data := []byte("candidate")
	st, err := m.Stage("w1", bytes.NewReader(data), 0)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if st.SHA256 != hex.EncodeToString(sum[:]) || st.Size != int64(len(data)) {
		t.Fatalf("staged = %+v", st)
	}
	again, err := m.Staged("w1")
	if err != nil || again.SHA256 != st.SHA256 {
		t.Fatalf("Staged = %+v err=%v", again, err)
	}
	if _, err := m.Staged("w2"); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("unstaged worker err = %v", err)
	}
	if _, err := m.Stage("../x", bytes.NewReader(data), 0); err == nil {
		t.Fatal("path-like worker id accepted")
	}
	if _, err := m.Stage("w1", bytes.NewReader(make([]byte, 20)), 10); err == nil {
		t.Fatal("oversized upload accepted")
	}
	if got, _ := m.Staged("w1"); got.SHA256 != st.SHA256 {
		t.Fatal("a rejected upload must not replace the staged binary")
	}
}

func TestRecordLifecycleAndPersistence(t *testing.T) {
	dir := t.TempDir()
	now := time.UnixMilli(1_000_000)
	m := New(dir)
	m.SetClock(func() time.Time { return now })
	if _, err := m.Begin("w1", "u1", "v1", "v2", "abc", false, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Begin("w1", "u2", "v1", "v2", "abc", false, time.Hour); !errors.Is(err, ErrInProgress) {
		t.Fatalf("second Begin err = %v", err)
	}
	now = now.Add(3 * time.Second)
	if _, ok := m.Finish("w1", "stale", StateFailed, "x", ""); ok {
		t.Fatal("a report for another upgrade id must be ignored")
	}
	rec, ok := m.Finish("w1", "u1", StateSucceeded, "", "v2.1")
	if !ok || rec.State != StateSucceeded || rec.DurationMS != 3000 || rec.TargetVersion != "v2.1" {
		t.Fatalf("finish = %+v ok=%v", rec, ok)
	}
	// A terminal failure cannot overwrite it, but a later success report can win over a failure.
	if _, ok := m.Finish("w1", "u1", StateFailed, "late", ""); ok {
		t.Fatal("terminal record overwritten by a failure")
	}
	// Survives a restart.
	m2 := New(dir)
	got, ok := m2.Latest("w1")
	if !ok || got.State != StateSucceeded || got.UpgradeID != "u1" {
		t.Fatalf("reloaded = %+v ok=%v", got, ok)
	}
	// Stale pending records do not block forever.
	m3 := New(t.TempDir())
	m3.SetClock(func() time.Time { return now })
	_, _ = m3.Begin("w1", "a", "", "", "", false, time.Minute)
	now = now.Add(2 * time.Minute)
	if _, err := m3.Begin("w1", "b", "", "", "", false, time.Minute); err != nil {
		t.Fatalf("stale pending should be superseded: %v", err)
	}
}
