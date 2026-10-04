package pushhub

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func nextFrame(t *testing.T, c *Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, err := c.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bad frame %q: %v", b, err)
	}
	return m
}

func TestPushHubNonBlockingAndResync(t *testing.T) {
	t.Parallel()
	h := New(Options{QueueSize: 4})
	c, err := h.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if rej := c.Subscribe([]string{JobTopic("j1")}, nil); len(rej) != 0 {
		t.Fatalf("rejected %v", rej)
	}

	// The consumer never reads: publishing far more than the queue holds must neither
	// block nor grow memory — the overflow just flags a resync.
	start := time.Now()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			h.PublishJobEvent("j1", JobEvent{Seq: int64(i), Type: "job.test"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("PublishJobEvent blocked on a stuck consumer")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("publishing took %v", d)
	}
	if dropped, _ := h.Stats(); dropped == 0 {
		t.Fatal("expected dropped frames")
	}

	// The slow consumer is told to resync before the stale queue, then keeps getting frames.
	if m := nextFrame(t, c); m["t"] != "resync" {
		t.Fatalf("first frame = %v, want resync", m)
	}
	if _, resyncs := h.Stats(); resyncs != 1 {
		t.Fatalf("resyncs=%d", resyncs)
	}
	if m := nextFrame(t, c); m["t"] != "hello" {
		t.Fatalf("queued frame = %v, want the hello that fit", m)
	}
}

func TestStatsCoalesced(t *testing.T) {
	t.Parallel()
	h := New(Options{StatsInterval: 150 * time.Millisecond})
	var calls atomic.Int64
	h.SetSnapshot(TopicStats, func() (any, error) {
		calls.Add(1)
		return map[string]int{"n": int(calls.Load())}, nil
	})
	c, _ := h.Register("alice")
	defer c.Close()
	c.Subscribe([]string{TopicStats}, nil)
	if calls.Load() != 1 {
		t.Fatalf("subscribe should compute once, got %d", calls.Load())
	}

	// 200 notifications inside one interval collapse into a leading emission plus at
	// most one trailing one.
	for i := 0; i < 200; i++ {
		h.Notify(TopicStats)
	}
	time.Sleep(450 * time.Millisecond)
	got := calls.Load() - 1
	if got < 1 || got > 2 {
		t.Fatalf("snapshot computed %d times for a 200-notify burst, want 1..2", got)
	}

	// Nobody subscribed -> nothing is computed.
	c.Unsubscribe([]string{TopicStats})
	before := calls.Load()
	h.Notify(TopicStats)
	time.Sleep(300 * time.Millisecond)
	if calls.Load() != before {
		t.Fatal("stats computed with no subscribers")
	}
}

func TestPushHubTopicsAndLimits(t *testing.T) {
	t.Parallel()
	h := New(Options{MaxConnsPerCaller: 2, InvalInterval: 20 * time.Millisecond})
	a1, _ := h.Register("a")
	a2, _ := h.Register("a")
	if _, err := h.Register("a"); err != ErrTooManyConns {
		t.Fatalf("third conn err=%v, want ErrTooManyConns", err)
	}
	if _, err := h.Register("b"); err != nil {
		t.Fatalf("other caller refused: %v", err)
	}
	a2.Close()
	if _, err := h.Register("a"); err != nil {
		t.Fatalf("slot not released: %v", err)
	}

	if rej := a1.Subscribe([]string{"jobs", "bogus", "job:"}, nil); len(rej) != 2 {
		t.Fatalf("rejected=%v", rej)
	}
	nextFrame(t, a1) // hello
	h.NotifyJob("j9", "running")
	m := nextFrame(t, a1)
	if m["t"] != "inval" || m["topic"] != "jobs" {
		t.Fatalf("frame=%v", m)
	}
	list := m["data"].(map[string]any)["jobs"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != "j9" {
		t.Fatalf("jobs inval payload=%v", m["data"])
	}
}

func TestPushHubJobBackfill(t *testing.T) {
	t.Parallel()
	h := New(Options{})
	h.SetBackfill(func(id string, since int64) ([]JobEvent, error) {
		return []JobEvent{{Seq: since + 1, Type: "a"}, {Seq: since + 2, Type: "b"}}, nil
	})
	c, _ := h.Register("a")
	nextFrame(t, c) // hello
	c.Subscribe([]string{JobTopic("j")}, map[string]int64{JobTopic("j"): 10})
	if m := nextFrame(t, c); m["data"].(map[string]any)["seq"].(float64) != 11 {
		t.Fatalf("first backfill frame=%v", m)
	}
	nextFrame(t, c)
	h.PublishJobEvent("j", JobEvent{Seq: 12, Type: "c"})
	if m := nextFrame(t, c); m["data"].(map[string]any)["type"] != "c" {
		t.Fatalf("live frame=%v", m)
	}
}
