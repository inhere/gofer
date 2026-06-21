package job

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// eventTypes reads a job's recorded events (seq order) and returns just the type
// sequence, for compact ordering assertions.
func eventTypes(t *testing.T, s *Service, jobID string) []string {
	t.Helper()
	evs, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

// hasSubsequence reports whether want appears in order (not necessarily
// contiguous) within got.
func hasSubsequence(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// TestEventsExecJobLifecycle proves a plain exec job records
// submitted -> running -> terminal(done) in order, with the terminal detail
// carrying the status.
func TestEventsExecJobLifecycle(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}
	types := eventTypes(t, s, final.ID)
	want := []string{EventJobSubmitted, EventJobRunning, EventJobTerminal}
	if !hasSubsequence(types, want) {
		t.Fatalf("event order mismatch: got %v want subsequence %v", types, want)
	}
	// The terminal event detail must carry the done status.
	evs, _ := s.ListJobEvents(final.ID, 0)
	last := evs[len(evs)-1]
	if last.Type != EventJobTerminal {
		t.Fatalf("last event is %s, want %s", last.Type, EventJobTerminal)
	}
	if !strings.Contains(last.Detail, `"status":"done"`) {
		t.Fatalf("terminal detail missing done status: %q", last.Detail)
	}
	// No remote runner -> no dispatched event.
	for _, ty := range types {
		if ty == EventJobDispatched {
			t.Fatalf("local job must not record job.dispatched: %v", types)
		}
	}
}

// TestEventsInteractionLifecycle proves an interactive job records
// interaction.created then interaction.answered.
func TestEventsInteractionLifecycle(t *testing.T) {
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	it, err := s.CreateInteraction(jobID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "continue?"})
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}
	if _, err := s.AnswerInteraction(jobID, it.ID, "yes"); err != nil {
		t.Fatalf("AnswerInteraction: %v", err)
	}
	types := eventTypes(t, s, jobID)
	if !hasSubsequence(types, []string{EventInteractionCreated, EventInteractionAnswered}) {
		t.Fatalf("interaction events missing/out of order: %v", types)
	}
	// answered detail carries the answer.
	evs, _ := s.ListJobEvents(jobID, 0)
	var answered *jobstore.JobEvent
	for i := range evs {
		if evs[i].Type == EventInteractionAnswered {
			answered = &evs[i]
		}
	}
	if answered == nil || !strings.Contains(answered.Detail, `"answer":"yes"`) {
		t.Fatalf("answered event detail missing answer: %+v", answered)
	}
}

// TestEventsCancelledJob proves a cancelled live job records job.cancelled and a
// terminal event with status=cancelled.
func TestEventsCancelledJob(t *testing.T) {
	s := newTestService(t, t.TempDir())
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForStatus(t, s, res.ID, StatusRunning, 2*time.Second)
	if err := s.Cancel(res.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	final, _ := s.Wait(res.ID)
	if final.Status != StatusCancelled {
		t.Fatalf("expected cancelled, got %s", final.Status)
	}
	types := eventTypes(t, s, res.ID)
	if !hasSubsequence(types, []string{EventJobCancelled, EventJobTerminal}) {
		t.Fatalf("cancel events missing/out of order: %v", types)
	}
	evs, _ := s.ListJobEvents(res.ID, 0)
	last := evs[len(evs)-1]
	if last.Type != EventJobTerminal || !strings.Contains(last.Detail, `"status":"cancelled"`) {
		t.Fatalf("terminal(cancelled) event missing: %+v", last)
	}
}

// failingEventSink always fails InsertJobEvent, to prove recordEvent is
// best-effort. calls is atomic because recordEvent fires from both the submit
// goroutine and the execute goroutine.
type failingEventSink struct{ calls atomic.Int64 }

func (f *failingEventSink) InsertJobEvent(jobstore.JobEvent) (int64, error) {
	f.calls.Add(1)
	return 0, errors.New("boom")
}

// TestEventsBestEffortDoesNotAffectTerminal proves that when the event sink fails
// on every insert, the job still reaches its normal terminal state (recordEvent
// must not panic or change status). The failing sink is invoked (proving the
// insertion points fire) but the failure is swallowed.
func TestEventsBestEffortDoesNotAffectTerminal(t *testing.T) {
	s := newTestService(t, t.TempDir())
	sink := &failingEventSink{}
	s.events = sink // inject the failing sink (recordEvent uses it over s.meta)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("job terminal state changed by failing event sink: got %s", final.Status)
	}
	if final.ExitCode != 0 {
		t.Fatalf("expected exit 0 despite event failures, got %d", final.ExitCode)
	}
	// The insertion points fired (submitted + running + terminal at least).
	if got := sink.calls.Load(); got < 3 {
		t.Fatalf("expected >=3 recordEvent attempts, got %d", got)
	}
}
