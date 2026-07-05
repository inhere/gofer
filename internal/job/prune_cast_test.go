package job

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestPruneExpiresCastRecording: with cast recording enabled and a CLOSED pty
// session whose cast TTL has elapsed, Prune (regime 1, D-P3-6) clears the row's
// recording_uri (row retained for audit) and best-effort deletes the on-disk cast
// file. Job/workflow retention is left unconfigured so the cast sweep is exercised
// in isolation.
func TestPruneExpiresCastRecording(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	s.config().Storage.Cast = config.CastConfig{Enabled: true, RetentionTTLHours: 24}

	// A real cast file on disk that the sweep must delete.
	castPath := filepath.Join(root, "pty.cast")
	if err := os.WriteFile(castPath, []byte("cast-bytes"), 0o600); err != nil {
		t.Fatalf("write cast file: %v", err)
	}

	base := time.Now()
	rec := jobstore.PtySessionRecord{
		PtySessionID: "ps-exp", JobID: "job-exp", State: "closed",
		RecordingURI: castPath, Encrypted: 1,
		StartedAt: base.Add(-time.Hour).Unix(), EndedAt: base.Unix(),
	}
	if err := s.meta.UpsertPtySession(rec); err != nil {
		t.Fatalf("upsert pty session: %v", err)
	}

	// Advance the clock two days so ended_at + 24h < now (session is expired).
	s.nowFn = func() time.Time { return base.Add(48 * time.Hour) }

	if _, err := s.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	// The cast file is deleted.
	if _, err := os.Stat(castPath); !os.IsNotExist(err) {
		t.Fatalf("cast file not removed: stat err=%v", err)
	}
	// The session ROW is retained but its recording pointer is cleared (encrypted->2).
	got, ok, err := s.meta.GetPtySessionByJob("job-exp")
	if err != nil || !ok {
		t.Fatalf("session row should be retained: ok=%v err=%v", ok, err)
	}
	if got.RecordingURI != "" {
		t.Fatalf("recording_uri not cleared: %q", got.RecordingURI)
	}
	if got.Encrypted != 2 {
		t.Fatalf("encrypted not reset to 2: %d", got.Encrypted)
	}
}

// TestPruneCastDisabledLeavesRecording (G023): with cast recording DISABLED, Prune
// never touches pty sessions even when a recording is well past any TTL — the sweep
// is skipped entirely, so the file and the row survive (zero behaviour change for a
// deployment that never enabled recording).
func TestPruneCastDisabledLeavesRecording(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	// Cast + retention both unconfigured (zero value).

	castPath := filepath.Join(root, "pty.cast")
	if err := os.WriteFile(castPath, []byte("cast-bytes"), 0o600); err != nil {
		t.Fatalf("write cast file: %v", err)
	}
	base := time.Now()
	rec := jobstore.PtySessionRecord{
		PtySessionID: "ps-keep", JobID: "job-keep", State: "closed",
		RecordingURI: castPath, Encrypted: 1,
		StartedAt: base.Add(-time.Hour).Unix(), EndedAt: base.Unix(),
	}
	if err := s.meta.UpsertPtySession(rec); err != nil {
		t.Fatalf("upsert pty session: %v", err)
	}
	s.nowFn = func() time.Time { return base.Add(1000 * time.Hour) }

	if _, err := s.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(castPath); err != nil {
		t.Fatalf("cast file should remain when cast disabled: %v", err)
	}
	got, ok, _ := s.meta.GetPtySessionByJob("job-keep")
	if !ok || got.RecordingURI != castPath {
		t.Fatalf("session recording_uri should be untouched: ok=%v uri=%q", ok, got.RecordingURI)
	}
}
