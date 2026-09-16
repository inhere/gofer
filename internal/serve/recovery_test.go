package serve

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// recoverExpiryFixture opens a temp-file jobstore holding ONE job left in
// `recovering` (exactly what ReconcileOrphanJobs leaves for a worker job) plus a
// job service over it, so the RECOV-01 expiry path can be driven for real.
func recoverExpiryFixture(t *testing.T) (*jobstore.Store, *job.Service) {
	t.Helper()
	s, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.NoErr(t, s.UpsertJob(jobstore.JobRecord{
		ID: "wj1", ProjectKey: "proj", Agent: "claude", Runner: "worker", WorkerID: "w1",
		Status: "recovering", Cwd: ".", ResultDir: filepath.Join(t.TempDir(), "results", "wj1"),
		StartedAt: 100, UpdatedAt: 100,
	}))
	return s, job.NewService(&config.Config{}, nil, nil, nil, s, nil)
}

// waitStatus polls until the job reaches want (the expiry runs on a timer) or fails
// the test on timeout.
func waitStatus(t *testing.T, s *jobstore.Store, id, want string, timeout time.Duration) jobstore.JobRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		rec, ok, err := s.GetJob(id)
		assert.NoErr(t, err)
		assert.True(t, ok)
		if rec.Status == want {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s status = %q after %s, want %q", id, rec.Status, timeout, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStartRecoverExpiryFailsRecoveringAfterWindow: with the window enabled the job
// held `recovering` at startup is NOT failed immediately — it is failed when the
// one-shot timer expires, with the worker-lost cause stamped so CLI/web show why.
func TestStartRecoverExpiryFailsRecoveringAfterWindow(t *testing.T) {
	s, jobs := recoverExpiryFixture(t)
	stop := make(chan struct{})
	defer close(stop)

	c := gcli.NewCommand("serve", "", nil)
	startRecoverExpiry(c, jobs, 60*time.Millisecond, stop)

	if rec, _, _ := s.GetJob("wj1"); rec.Status != "recovering" {
		t.Fatalf("status = %q right after arming, want the job still recovering (the window defers it)", rec.Status)
	}
	rec := waitStatus(t, s, "wj1", "failed", 5*time.Second)
	assert.Eq(t, recoveringFailReason, rec.Error)
	assert.True(t, rec.EndedAt > 0)
	assert.Eq(t, rec.EndedAt, rec.UpdatedAt)
}

// TestStartRecoverExpiryStopLeavesJobsRecovering: serve returning inside the window
// exits the timer cleanly WITHOUT failing anything — the rows stay `recovering` for
// the next serve to re-reconcile and re-arm.
func TestStartRecoverExpiryStopLeavesJobsRecovering(t *testing.T) {
	s, jobs := recoverExpiryFixture(t)
	stop := make(chan struct{})

	c := gcli.NewCommand("serve", "", nil)
	startRecoverExpiry(c, jobs, 80*time.Millisecond, stop)
	close(stop) // serve returns before the window elapses

	time.Sleep(250 * time.Millisecond) // well past the window the timer would have fired at
	rec, ok, err := s.GetJob("wj1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "recovering", rec.Status)
}

// TestStartRecoverExpiryDisabledFailsAtOnce: job_recover_window_sec=0 disables
// recovery, so there is no window to hold the jobs under — they are failed at once
// (the pre-RECOV-01 behaviour) instead of sitting `recovering` with no timer.
func TestStartRecoverExpiryDisabledFailsAtOnce(t *testing.T) {
	s, jobs := recoverExpiryFixture(t)
	stop := make(chan struct{})
	defer close(stop)

	c := gcli.NewCommand("serve", "", nil)
	startRecoverExpiry(c, jobs, 0, stop)

	rec, ok, err := s.GetJob("wj1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "failed", rec.Status)
	assert.Eq(t, recoveringFailReason, rec.Error)
	assert.True(t, rec.EndedAt > 0)
}
