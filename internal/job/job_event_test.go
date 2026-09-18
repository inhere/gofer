package job

import (
	"sync"
	"testing"
)

// TestEventObserverWhitelist pins the worker→hub event mirror's SOURCE side (SUP-01
// G): an observer is told only about the WHITELISTED event types — the ones the hub
// cannot see for a job it dispatched to a worker (the approval gate and the verify
// step) — never about the lifecycle events the hub records itself (submitted /
// running / terminal), which would only duplicate its own rows.
func TestEventObserverWhitelist(t *testing.T) {
	s := newTestService(t, t.TempDir())

	type seen struct {
		jobID  string
		typ    string
		detail map[string]any
	}
	var (
		mu  sync.Mutex
		got []seen
	)
	s.SetEventObserver(func(jobID, eventType string, detail map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, seen{jobID: jobID, typ: eventType, detail: detail})
	})
	snapshot := func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen(nil), got...)
	}

	s.recordEvent("job-1", EventJobVerifyStarted, map[string]any{"command": "go test ./..."})
	s.recordEvent("job-1", EventJobVerifyFinished, map[string]any{"status": "failed", "exit_code": 3, "duration_ms": 1200})
	s.recordEvent("job-1", EventJobPermissionRequested, map[string]any{"interaction_id": "i-1", "kind": "edit"})

	for _, typ := range []string{EventJobSubmitted, EventJobRunning, EventJobTerminal, EventJobNeedsReview} {
		s.recordEvent("job-1", typ, map[string]any{"status": "failed"})
	}

	seenEvents := snapshot()
	if len(seenEvents) != 3 {
		t.Fatalf("observer saw %d events (%+v), want exactly the 3 whitelisted ones", len(seenEvents), seenEvents)
	}
	if seenEvents[0].typ != EventJobVerifyStarted || seenEvents[0].jobID != "job-1" {
		t.Fatalf("first observed event = %+v, want %s for job-1", seenEvents[0], EventJobVerifyStarted)
	}
	if seenEvents[0].detail["command"] != "go test ./..." {
		t.Fatalf("verify_started detail = %v, want the command", seenEvents[0].detail)
	}
	if seenEvents[1].typ != EventJobVerifyFinished || seenEvents[1].detail["status"] != "failed" {
		t.Fatalf("second observed event = %+v, want the verify outcome", seenEvents[1])
	}
	if seenEvents[2].typ != EventJobPermissionRequested || seenEvents[2].detail["interaction_id"] != "i-1" {
		t.Fatalf("third observed event = %+v, want the permission request", seenEvents[2])
	}

	// The observer is an ADDITION: the event is still durably recorded on this side.
	if evs := jobEventDetails(t, s, "job-1", EventJobVerifyFinished); len(evs) != 1 {
		t.Fatalf("verify_finished was not persisted: %d rows", len(evs))
	}

	// Clearing the observer (the default for a hub-side service) is a no-op, not a
	// panic — and a detail-less event is passed through as nil rather than failing.
	s.SetEventObserver(nil)
	s.recordEvent("job-1", EventJobVerifyStarted, nil)
	if n := len(snapshot()); n != 3 {
		t.Fatalf("observer saw %d events after being cleared, want the 3 from before", n)
	}
}
