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
	if events[0].detail["old_instance"] != "a" || events[0].detail["new_instance"] != "b" || events[0].detail["suppressed"] != 0 {
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
