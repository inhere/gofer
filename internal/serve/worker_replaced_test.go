package serve

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wshub"
)

type recordedEvent struct {
	scope, typ string
	detail     map[string]any
}

// TestReplaceReporterRateLimitsEvents: every replacement is logged, but one worker
// gets one worker.replaced event per 10 minutes carrying the suppressed count.
func TestReplaceReporterRateLimitsEvents(t *testing.T) {
	var events []recordedEvent
	r := newReplaceReporter(func(scope, typ, _ string, detail map[string]any) {
		events = append(events, recordedEvent{scope, typ, detail})
	})
	now := time.Unix(1_000_000, 0)
	r.now = func() time.Time { return now }
	info := wshub.ReplaceInfo{WorkerID: "w1", OldInstance: "a", NewInstance: "b", OldRemote: "1.1.1.1:1", NewRemote: "2.2.2.2:2"}
	// "b" was displaced earlier, so every b-over-a replacement below is a fight.
	r.report(wshub.ReplaceInfo{WorkerID: "w1", OldInstance: "b", NewInstance: "a"})
	r.report(wshub.ReplaceInfo{WorkerID: "w2", OldInstance: "b", NewInstance: "a"})
	if len(events) != 0 {
		t.Fatalf("first replacements recorded %d events, want 0 (handover)", len(events))
	}

	r.report(info)
	now = now.Add(time.Minute)
	r.report(info)
	r.report(info)
	other := info
	other.WorkerID = "w2"
	r.report(other) // another worker has its own window
	if len(events) != 2 {
		t.Fatalf("events within the window = %d, want 2 (one per worker)", len(events))
	}
	if events[0].scope != job.WorkerEventScope("w1") || events[0].typ != job.EventWorkerReplaced {
		t.Fatalf("first event = %+v", events[0])
	}
	if events[0].detail["old_instance"] != "a" || events[0].detail["new_instance"] != "b" || events[0].detail["suppressed"] != 0 || events[0].detail["conflict"] != true {
		t.Fatalf("first detail = %+v", events[0].detail)
	}

	now = now.Add(workerReplacedEventInterval)
	r.report(info)
	if len(events) != 3 {
		t.Fatalf("events after the window = %d, want 3", len(events))
	}
	if events[2].detail["suppressed"] != 2 {
		t.Fatalf("suppressed = %v, want 2", events[2].detail["suppressed"])
	}
}

// TestReplaceReporterHandoverIsNotAConflict: a restart or an upgrade re-exec takes
// over its predecessor's connection once, and the predecessor never comes back; no
// worker.replaced event (the worker page warning) is recorded for that, however
// often the worker restarts.
func TestReplaceReporterHandoverIsNotAConflict(t *testing.T) {
	var events []recordedEvent
	r := newReplaceReporter(func(scope, typ, _ string, detail map[string]any) {
		events = append(events, recordedEvent{scope, typ, detail})
	})
	now := time.Unix(1_000_000, 0)
	r.now = func() time.Time { return now }
	r.report(wshub.ReplaceInfo{WorkerID: "w1", OldInstance: "a", NewInstance: "b"})
	now = now.Add(time.Minute)
	r.report(wshub.ReplaceInfo{WorkerID: "w1", OldInstance: "b", NewInstance: "c"})
	if len(events) != 0 {
		t.Fatalf("handovers recorded %+v, want none", events)
	}

	// A displaced instance that comes back after the memory window is a new handover too.
	now = now.Add(workerDisplacedTTL)
	r.report(wshub.ReplaceInfo{WorkerID: "w1", OldInstance: "c", NewInstance: "a"})
	if len(events) != 0 {
		t.Fatalf("return after the window recorded %+v, want none", events)
	}

	// Within the window a displaced instance taking over again is the fight.
	now = now.Add(time.Second)
	r.report(wshub.ReplaceInfo{WorkerID: "w1", OldInstance: "a", NewInstance: "c"})
	if len(events) != 1 || events[0].detail["old_instance"] != "a" || events[0].detail["new_instance"] != "c" {
		t.Fatalf("events = %+v, want one a->c conflict", events)
	}
}
