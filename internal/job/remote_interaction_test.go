package job

import (
	"errors"
	"testing"
)

// TestInjectInteraction exercises the remote-runner injection path: a peer-raised
// interaction (carrying the PEER's id) is recorded on a live host job, flips the
// job to pending_interaction, is idempotent on a repeated id, and is rejected for
// terminal / unknown jobs.
func TestInjectInteraction(t *testing.T) {
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	it := Interaction{
		ID:        "peer-int-1",
		JobID:     jobID,
		Type:      InteractionTypeQuestion,
		Prompt:    "continue?",
		Status:    InteractionPending,
		CreatedAt: s.nowFn().Unix(),
	}
	if err := s.injectInteraction(jobID, it); err != nil {
		t.Fatalf("injectInteraction: %v", err)
	}

	got, err := s.GetInteractions(jobID)
	if err != nil {
		t.Fatalf("GetInteractions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "peer-int-1" || got[0].Status != InteractionPending {
		t.Fatalf("expected 1 pending interaction with peer id, got %+v", got)
	}
	if j, _ := s.Get(jobID); j.Status != StatusPendingInteraction {
		t.Fatalf("expected job status pending_interaction, got %s", j.Status)
	}

	// Idempotent: re-injecting the same id must not add a second record.
	if err := s.injectInteraction(jobID, it); err != nil {
		t.Fatalf("idempotent injectInteraction: %v", err)
	}
	got, _ = s.GetInteractions(jobID)
	if len(got) != 1 {
		t.Fatalf("expected still 1 interaction after repeat inject, got %d", len(got))
	}
}

// TestInjectInteractionUnknownJob asserts injecting onto an untracked job id
// reports ErrUnknownJob.
func TestInjectInteractionUnknownJob(t *testing.T) {
	s := newTestService(t, t.TempDir())
	err := s.injectInteraction("does-not-exist", Interaction{ID: "x", Status: InteractionPending})
	if !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("expected ErrUnknownJob, got %v", err)
	}
}

// TestInjectInteractionTerminalJob asserts injecting onto a finished job reports
// ErrJobTerminal (no live agent to consume the answer).
func TestInjectInteractionTerminalJob(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if !IsTerminal(final.Status) {
		t.Fatalf("expected terminal job, got %s", final.Status)
	}
	err := s.injectInteraction(final.ID, Interaction{ID: "x", Status: InteractionPending})
	if !errors.Is(err, ErrJobTerminal) {
		t.Fatalf("expected ErrJobTerminal, got %v", err)
	}
}
