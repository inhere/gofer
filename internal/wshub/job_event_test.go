package wshub

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// TestHubDedupsJobEvent: a worker re-sending an event it already delivered (the
// duplicate the mirror has to tolerate, e.g. a reconnect replay) must reach the host
// job ONCE — the sink callback is keyed on (job_id, type, ts, interaction_id) within
// a bounded per-connection window. A genuinely different event (another ts, another
// interaction, another type) still gets through.
func TestHubDedupsJobEvent(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if !reg.Accepted {
		t.Fatal("register rejected")
	}

	sink := newFakeSink()
	if err := registerSink(t, hub, "w1", "j1", sink); err != nil {
		t.Fatalf("RegisterSink: %v", err)
	}
	push := func(ev wsproto.JobEvent) {
		if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeJobEvent, JobID: ev.JobID, Payload: mustRaw(ev)}); err != nil {
			panic(err)
		}
	}

	first := wsproto.JobEvent{
		JobID: "j1", Type: "job.permission_requested", TS: 1700000000, InteractionID: "i-1",
		Detail: mustRaw(map[string]any{"interaction_id": "i-1", "kind": "edit"}),
	}
	push(first)
	push(first) // exact duplicate: dropped

	// A retry of the SAME interaction a moment later is a new event (different ts).
	push(wsproto.JobEvent{JobID: "j1", Type: "job.permission_requested", TS: 1700000001, InteractionID: "i-1"})
	// A different type on the same job is a different event.
	push(wsproto.JobEvent{JobID: "j1", Type: "job.verify_started", TS: 1700000001})
	// A different interaction id is a different event.
	push(wsproto.JobEvent{JobID: "j1", Type: "job.permission_requested", TS: 1700000001, InteractionID: "i-2"})

	// Prove the loop drained everything: the last frame is followed by a result the
	// sink observes, and only then do we assert (no sleeps, no flakiness).
	terminal := wsproto.Result{JobID: "j1", Status: "done"}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeResult, JobID: "j1", Payload: mustRaw(terminal)}); err != nil {
		panic(err)
	}
	select {
	case <-sink.finished:
	case <-ctx.Done():
		t.Fatal("did not observe the terminal result")
	}

	events := sink.snapshot()
	want := []string{
		"event:job.permission_requested:1700000000:i-1",
		"event:job.permission_requested:1700000001:i-1",
		"event:job.verify_started:1700000001:",
		"event:job.permission_requested:1700000001:i-2",
		"finish:done",
	}
	if len(events) != len(want) {
		t.Fatalf("sink events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (all=%v)", i, events[i], want[i], events)
		}
	}
}
