package job

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// fixedClock returns a nowFn pinned to a settable unix time, for deterministic
// backoff / due assertions in the sweeper tests.
type fixedClock struct{ t atomic.Int64 }

func (f *fixedClock) set(unix int64) { f.t.Store(unix) }
func (f *fixedClock) now() int64     { return f.t.Load() }

// submitDeliveredJob runs a job (which enqueues a job.terminal delivery) and
// returns the job id plus the single enqueued delivery.
func submitDeliveredJob(t *testing.T, s *Service) (string, jobstore.Delivery) {
	t.Helper()
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}
	ds, err := s.ListDeliveriesByJob(final.ID)
	if err != nil {
		t.Fatalf("ListDeliveriesByJob: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("expected 1 enqueued delivery, got %d", len(ds))
	}
	return final.ID, ds[0]
}

// TestDeliverDueSuccess proves a 2xx POST marks the delivery delivered and the
// body carries the right event type + job summary.
func TestDeliverDueSuccess(t *testing.T) {
	s := newNotifyService(t, t.TempDir(), []config.WebhookConfig{
		{URL: "https://hooks.example.com/gofer"},
	}, nil)

	var gotType string
	var gotBody []byte
	s.postFn = func(_ context.Context, _, eventType string, body []byte, _ string, _ config.NotificationConfig) error {
		gotType = eventType
		gotBody = body
		return nil
	}

	jobID, _ := submitDeliveredJob(t, s)
	n := s.DeliverDue(context.Background())
	if n != 1 {
		t.Fatalf("DeliverDue processed %d, want 1", n)
	}
	if gotType != EventJobTerminal {
		t.Errorf("posted event type = %q", gotType)
	}
	if len(gotBody) == 0 {
		t.Error("empty body posted")
	}

	ds, _ := s.ListDeliveriesByJob(jobID)
	if ds[0].Status != jobstore.DeliveryDelivered {
		t.Fatalf("status = %q, want delivered", ds[0].Status)
	}
}

// TestDeliverDueRetryThenFail proves a failing POST bumps attempts + sets the
// backoff next_retry_at on each sweep, and finally marks the delivery failed once
// the retry cap is hit. The clock is pinned so next_retry_at is exact.
func TestDeliverDueRetryThenFail(t *testing.T) {
	root := t.TempDir()
	s := newNotifyService(t, root, []config.WebhookConfig{
		{URL: "https://hooks.example.com/gofer"},
	}, nil)
	// max_attempts=3 so the third attempt fails the delivery (faster than the
	// default 6); backoff[0]=30s, backoff[1]=2m.
	s.config().Server.Notification.MaxAttempts = 3

	clk := &fixedClock{}
	clk.set(1000)
	s.nowFn = func() time.Time { return time.Unix(clk.now(), 0) }

	var calls atomic.Int64
	s.postFn = func(_ context.Context, _, _ string, _ []byte, _ string, _ config.NotificationConfig) error {
		calls.Add(1)
		return errors.New("boom 500")
	}

	jobID, _ := submitDeliveredJob(t, s)

	// Sweep 1 (now=1000): attempt 1 fails -> attempts=1, next_retry_at=1000+30=1030.
	if got := s.DeliverDue(context.Background()); got != 1 {
		t.Fatalf("sweep1 processed %d, want 1", got)
	}
	d := mustOneDelivery(t, s, jobID)
	if d.Status != jobstore.DeliveryPending || d.Attempts != 1 || d.NextRetryAt != 1030 {
		t.Fatalf("after sweep1: status=%s attempts=%d next=%d (want pending/1/1030)", d.Status, d.Attempts, d.NextRetryAt)
	}

	// Not yet due at now=1029: claim takes nothing.
	clk.set(1029)
	if got := s.DeliverDue(context.Background()); got != 0 {
		t.Fatalf("sweep at 1029 processed %d, want 0 (not due)", got)
	}

	// Sweep 2 (now=1030): attempt 2 fails -> attempts=2, next_retry_at=1030+120=1150.
	clk.set(1030)
	if got := s.DeliverDue(context.Background()); got != 1 {
		t.Fatalf("sweep2 processed %d, want 1", got)
	}
	d = mustOneDelivery(t, s, jobID)
	if d.Status != jobstore.DeliveryPending || d.Attempts != 2 || d.NextRetryAt != 1150 {
		t.Fatalf("after sweep2: status=%s attempts=%d next=%d (want pending/2/1150)", d.Status, d.Attempts, d.NextRetryAt)
	}

	// Sweep 3 (now=1150): attempt 3 hits the cap -> failed.
	clk.set(1150)
	if got := s.DeliverDue(context.Background()); got != 1 {
		t.Fatalf("sweep3 processed %d, want 1", got)
	}
	d = mustOneDelivery(t, s, jobID)
	if d.Status != jobstore.DeliveryFailed || d.Attempts != 3 {
		t.Fatalf("after sweep3: status=%s attempts=%d (want failed/3)", d.Status, d.Attempts)
	}
	if d.LastError == "" {
		t.Error("failed delivery should record last_error")
	}

	// A failed delivery is never claimed again.
	clk.set(1_000_000)
	if got := s.DeliverDue(context.Background()); got != 0 {
		t.Fatalf("sweep after fail processed %d, want 0", got)
	}
	if calls.Load() != 3 {
		t.Fatalf("post attempted %d times, want exactly 3", calls.Load())
	}
}

// TestDeliverDueNoConfig proves a service with no notification config sweeps
// nothing (regression).
func TestDeliverDueNoConfig(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if got := s.DeliverDue(context.Background()); got != 0 {
		t.Fatalf("no-config DeliverDue processed %d, want 0", got)
	}
}

// TestDeliverDueNoDoubleDeliver proves concurrent sweeps never POST the same
// delivery twice (SR303 single-claim through the sweeper). Many goroutines run
// DeliverDue over a batch of due deliveries; each delivery's POST must fire once.
func TestDeliverDueNoDoubleDeliver(t *testing.T) {
	root := t.TempDir()
	s := newNotifyService(t, root, []config.WebhookConfig{
		{URL: "https://hooks.example.com/gofer"},
	}, nil)

	clk := &fixedClock{}
	clk.set(5000)
	s.nowFn = func() time.Time { return time.Unix(clk.now(), 0) }

	var posts atomic.Int64
	s.postFn = func(_ context.Context, _, _ string, _ []byte, _ string, _ config.NotificationConfig) error {
		posts.Add(1)
		return nil
	}

	// Enqueue many deliveries by running many jobs (each enqueues one terminal
	// delivery). All become due now.
	const jobs = 25
	var ids []string
	for i := 0; i < jobs; i++ {
		id, _ := submitDeliveredJob(t, s)
		ids = append(ids, id)
	}

	// Race many sweeps.
	var wg sync.WaitGroup
	var totalProcessed atomic.Int64
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n := s.DeliverDue(context.Background())
				if n == 0 {
					return
				}
				totalProcessed.Add(int64(n))
			}
		}()
	}
	wg.Wait()

	// Every delivery delivered exactly once: total processed equals the job count,
	// and each job's single delivery is in the delivered state.
	if got := totalProcessed.Load(); got != int64(jobs) {
		t.Fatalf("total processed = %d, want %d (no delivery claimed twice)", got, jobs)
	}
	if got := posts.Load(); got != int64(jobs) {
		t.Fatalf("POST fired %d times, want exactly %d (no double-POST)", got, jobs)
	}
	delivered := 0
	for _, id := range ids {
		d := mustOneDelivery(t, s, id)
		if d.Status == jobstore.DeliveryDelivered {
			delivered++
		}
	}
	if delivered != jobs {
		t.Fatalf("%d/%d deliveries delivered", delivered, jobs)
	}
}

func mustOneDelivery(t *testing.T, s *Service, jobID string) jobstore.Delivery {
	t.Helper()
	ds, err := s.ListDeliveriesByJob(jobID)
	if err != nil {
		t.Fatalf("ListDeliveriesByJob: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("expected 1 delivery for %s, got %d", jobID, len(ds))
	}
	return ds[0]
}
