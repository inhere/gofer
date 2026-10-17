package serve

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/pushhub"
)

// TestHeldJobEventsNotifyPending (gofer-5wvc): a job parking in awaiting_approval — and
// the approval / rejection / expiry that ends the wait — tells the bell's topic
// (`pending`) that its data changed, so a browser toasts a new held job and drops a
// decided one. The event names come from internal/job: pushhub matches them as literals
// (it is a leaf package and cannot import job), so a rename has to fail here.
func TestHeldJobEventsNotifyPending(t *testing.T) {
	for _, ev := range []string{
		job.EventJobAwaitingApproval, job.EventJobHoldApproved, job.EventJobHoldRejected, job.EventJobHoldExpired,
	} {
		t.Run(ev, func(t *testing.T) {
			conn, jobs, snaps := holdPushFixture(t)
			// The subscription itself pushes the current snapshot; the next one is what a
			// notification produced.
			expectFrame(t, conn, "snap", pushhub.TopicPending)

			jobs.RecordJobEvent("j-held", ev, map[string]any{"reason": "push the release branch"})
			f := expectFrame(t, conn, "snap", pushhub.TopicPending)
			if got, _ := f["data"].(map[string]any)["n"].(float64); got != 2 {
				t.Fatalf("pending snapshot after %s = %v, want a recomputed snapshot", ev, f)
			}

			// An unrelated write-side event must NOT wake the bell.
			jobs.RecordJobEvent("j-held", job.EventJobRunning, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			if _, err := conn.Next(ctx); err == nil {
				t.Fatalf("a %s event notified the pending topic", job.EventJobRunning)
			}
			if snaps.Load() != 2 {
				t.Fatalf("pending snapshots computed = %d, want 2", snaps.Load())
			}
		})
	}
}

// holdPushFixture wires the push sources (as serve does) around a store holding one held
// job and returns a `pending` subscriber plus the number of snapshots computed.
func holdPushFixture(t *testing.T) (*pushhub.Conn, *job.Service, *atomic.Int64) {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "live.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() }) // last: components stop first (G051)
	now := time.Now().Unix()
	assert.NoErr(t, st.UpsertJob(jobstore.JobRecord{
		ID: "j-held", ProjectKey: "p", Agent: "exec", Runner: "local",
		Status: job.StatusAwaitingApproval, Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, UpdatedAt: now,
	}))
	jobs := job.NewService(&config.Config{}, nil, nil, nil, st, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = jobs.Drain(ctx)
	})

	var snaps atomic.Int64
	ph := pushhub.New(pushhub.Options{PendingInterval: 5 * time.Millisecond})
	ph.SetSnapshot(pushhub.TopicPending, func() (any, error) { return map[string]any{"n": snaps.Add(1)}, nil })
	wireLivePush(ph, st, jobs, nil, nil)
	conn, err := ph.Register("alice")
	assert.NoErr(t, err)
	t.Cleanup(conn.Close)
	conn.Subscribe([]string{pushhub.TopicPending}, nil)
	return conn, jobs, &snaps
}
