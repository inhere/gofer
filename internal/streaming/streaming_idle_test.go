package streaming

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// countingSource is a StreamSource that counts every state read, standing in for
// the DB-backed job service: a read here is a read the real service would answer
// from SQLite (events/interactions) or memory (Get).
type countingSource struct {
	mu     sync.Mutex
	status string
	wake   chan struct{}

	gets, evs, its atomic.Int64
}

func newCountingSource() *countingSource {
	return &countingSource{status: job.StatusRunning, wake: make(chan struct{}, 1)}
}

func (c *countingSource) Get(string) (job.JobResult, bool) {
	c.gets.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	return job.JobResult{ID: "j1", Status: c.status}, true
}
func (c *countingSource) GetPersistedInteractions(string, string) ([]job.Interaction, error) {
	c.its.Add(1)
	return nil, nil
}
func (c *countingSource) ListJobEvents(string, int64) ([]jobstore.JobEvent, error) {
	c.evs.Add(1)
	return nil, nil
}
func (c *countingSource) WatchJob(string) (<-chan struct{}, func()) { return c.wake, func() {} }

// waitReplayed blocks until the SSE loop has left its start-up behind: the initial
// replay pumps interactions and events, and the catch-up re-read that follows the
// watch subscription (onChange) reads the status. All three counters non-zero
// therefore means the loop has settled and a counter snapshot is a valid baseline.
// A fixed sleep before that snapshot is a race on a loaded machine (G052).
func (c *countingSource) waitReplayed(t *testing.T) {
	t.Helper()
	wait.Until(t, 10*time.Second, "initial replay + catch-up to read status/events/interactions", func() bool {
		return c.gets.Load() > 0 && c.evs.Load() > 0 && c.its.Load() > 0
	})
}

// waitForDone awaits the stream goroutine's return; the deadline is scaled by
// GOFER_TEST_TIMEOUT_SCALE like every other success-path wait (G052).
func waitForDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	wait.Until(t, 5*time.Second, what, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
}

func (c *countingSource) setStatus(st string) {
	c.mu.Lock()
	c.status = st
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

type syncBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}
func (b *syncBuf) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.sb.String() }

type nopFlusher struct{}

func (nopFlusher) Flush() {}

var _ http.Flusher = nopFlusher{}

// TestStreamIdleDoesNoStateReads proves the SSE loop is event-driven: once the
// initial replay is done, an idle connection (log files still being tailed every
// poll) issues NO status/event/interaction reads, and a change signal produces
// exactly the re-read plus the status+end frames.
func TestStreamIdleDoesNoStateReads(t *testing.T) {
	// The safety tick is the one poll of an idle stream that reads state (it calls
	// Get before comparing the status), so keep it out of the observation window
	// below: the window is meant to bracket the log-poll loop only, and whether a
	// 5s tick happens to land in a 1.5s window is wall-clock luck, not loop
	// behaviour. The safety net itself is covered by
	// TestStreamSafetyNetCatchesSilentChange.
	prevSafety := StreamSafetyInterval
	StreamSafetyInterval = time.Minute
	t.Cleanup(func() { StreamSafetyInterval = prevSafety })

	src := newCountingSource()
	res := job.JobResult{ID: "j1", Status: job.StatusRunning, ResultDir: t.TempDir() + "/j1"}
	buf := &syncBuf{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		StreamJob(context.Background(), buf, nopFlusher{}, src, "j1", res, true, StreamOpts{})
	}()

	src.waitReplayed(t)
	g0, e0, i0 := src.gets.Load(), src.evs.Load(), src.its.Load()
	if e0 == 0 || i0 == 0 {
		t.Fatalf("initial replay must read events/interactions once (evs=%d its=%d)", e0, i0)
	}

	// Idle: ~6 log-poll ticks, each tailing the log files only. This one stays a
	// fixed sleep — it is an observation window, not a deadline to wait out: it must
	// outlive several ticks, and the StreamSafetyInterval override above keeps it
	// clear of the safety tick.
	time.Sleep(6 * StreamPollInterval)
	if g, e, i := src.gets.Load(), src.evs.Load(), src.its.Load(); g != g0 || e != e0 || i != i0 {
		t.Fatalf("idle stream queried state: gets %d->%d evs %d->%d its %d->%d", g0, g, e0, e, i0, i)
	}

	src.setStatus(job.StatusDone)
	waitForDone(t, done, "stream did not end promptly after the change signal")
	out := buf.String()
	if !strings.Contains(out, "event: end") || !strings.Contains(out, `"status":"`+job.StatusDone+`"`) {
		t.Fatalf("missing status/end frames:\n%s", out)
	}
}

// TestStreamSafetyNetCatchesSilentChange: a status change with no signal is still
// noticed through the in-memory safety tick.
func TestStreamSafetyNetCatchesSilentChange(t *testing.T) {
	prev := StreamSafetyInterval
	StreamSafetyInterval = 60 * time.Millisecond
	t.Cleanup(func() { StreamSafetyInterval = prev })

	src := newCountingSource()
	res := job.JobResult{ID: "j1", Status: job.StatusRunning, ResultDir: t.TempDir() + "/j1"}
	buf := &syncBuf{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		StreamJob(context.Background(), buf, nopFlusher{}, src, "j1", res, true, StreamOpts{})
	}()
	src.waitReplayed(t)
	src.mu.Lock()
	src.status = job.StatusFailed // no signal
	src.mu.Unlock()
	waitForDone(t, done, "safety tick did not pick up the silent status change")
}
