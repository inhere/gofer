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
	src := newCountingSource()
	res := job.JobResult{ID: "j1", Status: job.StatusRunning, ResultDir: t.TempDir() + "/j1"}
	buf := &syncBuf{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		StreamJob(context.Background(), buf, nopFlusher{}, src, "j1", res, true, StreamOpts{})
	}()

	time.Sleep(100 * time.Millisecond) // initial replay + catch-up
	g0, e0, i0 := src.gets.Load(), src.evs.Load(), src.its.Load()
	if e0 == 0 || i0 == 0 {
		t.Fatalf("initial replay must read events/interactions once (evs=%d its=%d)", e0, i0)
	}

	time.Sleep(6 * StreamPollInterval) // idle: ~6 log-poll ticks
	if g, e, i := src.gets.Load(), src.evs.Load(), src.its.Load(); g != g0 || e != e0 || i != i0 {
		t.Fatalf("idle stream queried state: gets %d->%d evs %d->%d its %d->%d", g0, g, e0, e, i0, i)
	}

	src.setStatus(job.StatusDone)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not end promptly after the change signal")
	}
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
	time.Sleep(50 * time.Millisecond)
	src.mu.Lock()
	src.status = job.StatusFailed // no signal
	src.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("safety tick did not pick up the silent status change")
	}
}
