package serve

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// fakeHoldSweeper records every sweep's clock reading.
type fakeHoldSweeper struct {
	mu    sync.Mutex
	calls []int64
	err   error
}

func (f *fakeHoldSweeper) SweepExpiredHolds(now int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, now)
	return len(f.calls), f.err
}

func (f *fakeHoldSweeper) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestHoldExpiryLoopSweepsAtStartupThenTicks: the first sweep runs at once (holds that
// expired while serve was down), later ones on the ticker, each with the loop's clock;
// closing stop ends the goroutine.
func TestHoldExpiryLoopSweepsAtStartupThenTicks(t *testing.T) {
	f := &fakeHoldSweeper{err: errors.New("db busy")}
	clock := time.Unix(1_800_000_000, 0)
	var reportsMu sync.Mutex
	var errs int
	stop := make(chan struct{})
	done := runHoldExpiryLoop(f, 5*time.Millisecond, func() time.Time { return clock }, func(_ int, err error) {
		reportsMu.Lock()
		defer reportsMu.Unlock()
		if err != nil {
			errs++
		}
	}, stop)
	t.Cleanup(func() { <-done })
	defer close(stop)

	wait.Until(t, 5*time.Second, "startup sweep + one tick", func() bool { return f.count() >= 2 })
	f.mu.Lock()
	first := f.calls[0]
	f.mu.Unlock()
	assert.Eq(t, clock.Unix(), first)
	reportsMu.Lock()
	assert.True(t, errs >= 2, "a failing sweep is reported, and the loop keeps going")
	reportsMu.Unlock()
}

// TestHoldExpiryLoopCancelsExpiredHold drives the real job service: a held row whose
// deadline passed (as after a restart) is cancelled by the startup sweep, while a hold
// still inside its window keeps waiting.
func TestHoldExpiryLoopCancelsExpiredHold(t *testing.T) {
	dir := t.TempDir()
	s, err := jobstore.Open(filepath.Join(dir, "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = s.Close() })
	for id, expires := range map[string]int64{"due": 150, "later": 10_000} {
		assert.NoErr(t, s.UpsertJob(jobstore.JobRecord{
			ID: id, ProjectKey: "proj", Agent: "exec", Runner: "local",
			Status: job.StatusAwaitingApproval, Cwd: ".", ResultDir: filepath.Join(dir, "results", id),
			StartedAt: 100, UpdatedAt: 100, HoldExpiresAt: expires,
			HoldJSON: `{"reason":"push","timeout_sec":50,"expires_at":` + itoa(expires) + `}`,
		}))
	}
	jobs := job.NewService(&config.Config{}, nil, nil, nil, s, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = jobs.Drain(ctx)
	})

	stop := make(chan struct{})
	done := runHoldExpiryLoop(jobs, time.Hour, func() time.Time { return time.Unix(200, 0) }, func(int, error) {}, stop)
	t.Cleanup(func() { close(stop); <-done })

	wait.For(t, 5*time.Second, "expired hold cancelled", func() (bool, any) {
		rec, _, _ := s.GetJob("due")
		return rec.Status == job.StatusCancelled, rec.Status
	})
	rec, _, _ := s.GetJob("due")
	assert.Eq(t, "hold expired", rec.Error)
	later, _, _ := s.GetJob("later")
	assert.Eq(t, job.StatusAwaitingApproval, later.Status)
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
