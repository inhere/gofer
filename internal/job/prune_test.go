package job

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// TestPruneRemovesTerminalJobAndLogDir runs a job to completion, configures a
// zero-age retention (everything terminal is overdue) and asserts Prune deletes
// the DB row and best-effort removes the on-disk log directory.
func TestPruneRemovesTerminalJobAndLogDir(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	// Retention: prune any terminal job at least 0 days old -> MaxAgeDays must be
	// >0 to enable; use a large age window but pin the clock far in the future so
	// the finished job is "old". Simpler: use MaxCount=0 + MaxAgeDays via clock.
	s.config().Storage.Retention = config.RetentionConfig{MaxAgeDays: 1}

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})

	// The log dir exists after the run.
	if _, err := os.Stat(final.ResultDir); err != nil {
		t.Fatalf("result dir missing before prune: %v", err)
	}

	// Advance the clock two days so the terminal job is older than MaxAgeDays=1.
	base := time.Now()
	s.nowFn = func() time.Time { return base.Add(48 * time.Hour) }

	deleted, err := s.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 pruned job, got %d", deleted)
	}
	// DB row gone.
	if _, ok, _ := s.meta.GetJob(final.ID); ok {
		t.Fatalf("job %q still in DB after prune", final.ID)
	}
	// Log dir removed (best-effort).
	if _, err := os.Stat(final.ResultDir); !os.IsNotExist(err) {
		t.Fatalf("result dir not removed after prune: stat err=%v", err)
	}
}

// TestPruneNoRetentionIsNoop asserts Prune with an unconfigured retention policy
// deletes nothing and leaves the job and its log dir intact.
func TestPruneNoRetentionIsNoop(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	// no retention configured (zero value).

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})

	deleted, err := s.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected 0 pruned (no retention), got %d", deleted)
	}
	if _, ok, _ := s.meta.GetJob(final.ID); !ok {
		t.Fatalf("job %q should remain in DB", final.ID)
	}
	if _, err := os.Stat(filepath.Join(root, "self", final.ID)); err != nil {
		t.Fatalf("result dir should remain: %v", err)
	}
}
