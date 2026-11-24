package pushhub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// drain reads and discards frames until the connection has been quiet for quiet.
func drain(t *testing.T, c *Conn, quiet time.Duration) {
	t.Helper()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), quiet)
		_, err := c.Next(ctx)
		cancel()
		if err != nil {
			return
		}
	}
}

// The `waiting` topic (the 「等我」 list) is notified from the event tap too, so a managed job
// parking in awaiting_input does not have to wait for the 30s scan: the exact set is still
// computed by the snapshot source, the tap only says "look". A session turn opening / closing
// is a decision row, and that notice comes from the store hook (serve.wireLivePush).
func TestNoteEventNotifiesWaiting(t *testing.T) {
	t.Parallel()
	h := New(Options{WaitingInterval: 20 * time.Millisecond})
	var calls atomic.Int64
	h.SetSnapshot(TopicWaiting, func() (any, error) {
		calls.Add(1)
		return []map[string]any{{"key": "session:s1", "category": "answer"}}, nil
	})
	h.SetJobProject(func(id string) (string, bool) {
		return map[string]string{"j1": "p1"}[id], true
	})
	c, err := h.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Subscribe([]string{TopicWaiting}, nil)
	drain(t, c, 100*time.Millisecond) // hello + the subscribe-time snapshot

	cases := []struct {
		name  string
		scope string
		typ   string
	}{
		// A managed session job parking in / leaving awaiting_input.
		{"job parked in awaiting_input", "j1", "job.awaiting_input"},
		{"job left awaiting_input", "j1", "job.turn_started"},
		// A job held for approval and the decision that ends the hold.
		{"job held for approval", "j1", "job.awaiting_approval"},
		{"hold decided", "j1", "job.hold_approved"},
	}
	for _, tc := range cases {
		before := calls.Load()
		h.NoteEvent(tc.scope, JobEvent{Type: tc.typ})
		if m := nextFrame(t, c); m["t"] != "snap" || m["topic"] != TopicWaiting {
			t.Fatalf("%s: frame = %v, want a waiting snapshot", tc.name, m)
		}
		if calls.Load() == before {
			t.Fatalf("%s: the snapshot was not recomputed", tc.name)
		}
		drain(t, c, 50*time.Millisecond)
	}
}

// A job event that changes no waiting state does not poke the waiting topic.
func TestNoteEventIgnoresUnrelatedEvents(t *testing.T) {
	t.Parallel()
	h := New(Options{WaitingInterval: 20 * time.Millisecond})
	var calls atomic.Int64
	h.SetSnapshot(TopicWaiting, func() (any, error) {
		calls.Add(1)
		return []any{}, nil
	})
	c, err := h.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Subscribe([]string{TopicWaiting}, nil)
	drain(t, c, 100*time.Millisecond)
	before := calls.Load()
	h.NoteEvent("j1", JobEvent{Type: "job.dispatched"})
	h.NoteEvent("j1", JobEvent{Type: "job.rules_injected"})
	time.Sleep(120 * time.Millisecond)
	if calls.Load() != before {
		t.Fatalf("an unrelated event recomputed the waiting snapshot")
	}
}
