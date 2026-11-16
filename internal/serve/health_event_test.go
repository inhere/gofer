package serve

import (
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/serve/healthcheck"
)

// healthEvent is one recorded transition as the fake sink saw it.
type healthEvent struct {
	scope, eventType, projectKey string
	detail                       map[string]any
}

// TestHealthEventRecorderAnnouncesTransitionsOnce: a fault is recorded exactly once on the
// server's own event stream (not on every failing run), and the first success records the
// recovery, so a subscriber hears about the fault and about its end and nothing in between.
func TestHealthEventRecorderAnnouncesTransitionsOnce(t *testing.T) {
	var got []healthEvent
	rec := func(scope, eventType, projectKey string, detail map[string]any) {
		got = append(got, healthEvent{scope: scope, eventType: eventType, projectKey: projectKey, detail: detail})
	}
	failing, recovered := healthEventRecorder(rec)

	stub := &stubErr{err: errors.New("low disk space: 200MiB free")}
	runner := healthcheck.New(3,
		healthcheck.Check{Name: healthcheck.CheckStorage, Probe: func() error { return nil }},
		healthcheck.Check{Name: healthcheck.CheckDisk, Probe: stub.probe},
	)
	runner.OnTransition(failing, recovered)

	// Below the threshold: a miss or two is noise and is not announced.
	runner.RunOnce()
	runner.RunOnce()
	if len(got) != 0 {
		t.Fatalf("recorded %+v below the threshold, want nothing", got)
	}

	// Third consecutive failure: one event, carrying the item, the streak and the error.
	runner.RunOnce()
	if len(got) != 1 {
		t.Fatalf("recorded %+v, want exactly one failure event", got)
	}
	ev := got[0]
	if ev.scope != job.ServerEventScope() || ev.eventType != job.EventServerHealthFailing || ev.projectKey != "" {
		t.Fatalf("event = %+v, want %q / %q with no project", ev, job.ServerEventScope(), job.EventServerHealthFailing)
	}
	if ev.detail["check"] != healthcheck.CheckDisk || ev.detail["failures"] != 3 || ev.detail["error"] != "low disk space: 200MiB free" {
		t.Fatalf("detail = %+v, want the check, the streak and the error", ev.detail)
	}

	// Still failing: the fault is not announced again.
	runner.RunOnce()
	runner.RunOnce()
	if len(got) != 1 {
		t.Fatalf("recorded %+v while the fault persists, want no repeat", got)
	}

	// One success: the recovery closes the fault (the streak is back to zero).
	stub.err = nil
	runner.RunOnce()
	if len(got) != 2 {
		t.Fatalf("recorded %+v, want the recovery event", got)
	}
	rec2 := got[1]
	if rec2.eventType != job.EventServerHealthRecovered || rec2.scope != job.ServerEventScope() || rec2.detail["check"] != healthcheck.CheckDisk {
		t.Fatalf("recovery = %+v, want %q for %q", rec2, job.EventServerHealthRecovered, healthcheck.CheckDisk)
	}
	if rec2.detail["failures"] != 0 {
		t.Fatalf("recovery detail = %+v, want the cleared streak", rec2.detail)
	}
	if _, present := rec2.detail["error"]; present {
		t.Fatalf("recovery detail = %+v, want no error left", rec2.detail)
	}

	// Healthy again: nothing more is recorded.
	runner.RunOnce()
	if len(got) != 2 {
		t.Fatalf("recorded %+v after the recovery, want nothing", got)
	}
}

// stubErr is a probe whose outcome the test flips.
type stubErr struct{ err error }

func (s *stubErr) probe() error { return s.err }
