package serve

import (
	"errors"
	"testing"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/testutil/wait"
)

type fakeAuditPruner struct {
	cutoffs chan int64
	err     error
}

func (f *fakeAuditPruner) PruneAuditBefore(cutoff int64) (int64, int64, error) {
	f.cutoffs <- cutoff
	return 0, 0, f.err
}

// TestPruneAuditCutoffFollowsRetention: the sweep deletes rows older than the window, and
// a non-positive window (server.audit_retention_days < 0) keeps everything.
func TestPruneAuditCutoffFollowsRetention(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	f := &fakeAuditPruner{cutoffs: make(chan int64, 4)}
	pruneAudit(gcli.NewCommand("t", ""), f, 90, now)
	if got, want := <-f.cutoffs, now.Add(-90*24*time.Hour).Unix(); got != want {
		t.Fatalf("cutoff = %d, want %d", got, want)
	}
	pruneAudit(gcli.NewCommand("t", ""), f, -1, now)
	pruneAudit(gcli.NewCommand("t", ""), f, 0, now)
	if len(f.cutoffs) != 0 {
		t.Fatal("a non-positive retention must not prune")
	}
	f.err = errors.New("boom")
	pruneAudit(gcli.NewCommand("t", ""), f, 1, now) // an error is reported, not fatal
	<-f.cutoffs
}

// TestAuditPruneLoopAlwaysRuns: the loop sweeps at startup with no storage.retention
// anywhere, then keeps ticking until stop closes.
func TestAuditPruneLoopAlwaysRuns(t *testing.T) {
	f := &fakeAuditPruner{cutoffs: make(chan int64, 16)}
	stop := make(chan struct{})
	startAuditPruneLoop(gcli.NewCommand("t", ""), f, func() int { return 90 }, 20*time.Millisecond, stop)
	wait.Until(t, 10*time.Second, "audit sweep at startup and on a tick", func() bool { return len(f.cutoffs) >= 2 })
	close(stop)
}
