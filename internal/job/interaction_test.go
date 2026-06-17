package job

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dev-agent-bridge/internal/store"
)

// submitRunning submits a long-lived exec job and waits until it is running, so
// interactions are raised while the job is genuinely live (not terminal). It
// returns the job id and registers cleanup that cancels + drains the job to stop
// its goroutine before the temp dir is removed.
func submitRunning(t *testing.T, s *Service) string {
	t.Helper()
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "30"}, Cwd: ".", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForStatus(t, s, res.ID, StatusRunning, 2*time.Second)
	t.Cleanup(func() {
		_ = s.Cancel(res.ID)
		s.Wait(res.ID)
	})
	return res.ID
}

func TestCreateInteractionFlipsStatus(t *testing.T) {
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	it, err := s.CreateInteraction(jobID, InteractionInput{
		Type:   InteractionTypeQuestion,
		Prompt: "continue?",
	})
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}
	if it.Status != InteractionPending || it.ID == "" || it.JobID != jobID {
		t.Fatalf("unexpected interaction: %+v", it)
	}
	if j, _ := s.Get(jobID); j.Status != StatusPendingInteraction {
		t.Fatalf("expected job status pending_interaction, got %s", j.Status)
	}
	got, err := s.GetInteractions(jobID)
	if err != nil {
		t.Fatalf("GetInteractions: %v", err)
	}
	if len(got) != 1 || got[0].ID != it.ID || got[0].Status != InteractionPending {
		t.Fatalf("expected 1 pending interaction, got %+v", got)
	}
}

func TestAnswerInteractionResumesRunning(t *testing.T) {
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	it, err := s.CreateInteraction(jobID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "q"})
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}

	// WaitAnswer in another goroutine must be woken by the answer.
	woke := make(chan Interaction, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		w, werr := s.WaitAnswer(ctx, jobID, it.ID)
		if werr != nil {
			t.Errorf("WaitAnswer: %v", werr)
			close(woke)
			return
		}
		woke <- w
	}()

	answered, err := s.AnswerInteraction(jobID, it.ID, "yes")
	if err != nil {
		t.Fatalf("AnswerInteraction: %v", err)
	}
	if answered.Status != InteractionAnswered || answered.Answer != "yes" || answered.AnsweredAt == 0 {
		t.Fatalf("unexpected answered interaction: %+v", answered)
	}
	// No pending interactions remain -> job returns to running.
	if j, _ := s.Get(jobID); j.Status != StatusRunning {
		t.Fatalf("expected job back to running, got %s", j.Status)
	}

	select {
	case w, ok := <-woke:
		if !ok {
			t.Fatalf("WaitAnswer errored (see above)")
		}
		if w.Status != InteractionAnswered || w.Answer != "yes" {
			t.Fatalf("WaitAnswer returned wrong interaction: %+v", w)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("WaitAnswer was not woken by the answer")
	}
}

func TestMultiplePendingKeepsStatusUntilAllAnswered(t *testing.T) {
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	i1, err := s.CreateInteraction(jobID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "q1"})
	if err != nil {
		t.Fatal(err)
	}
	i2, err := s.CreateInteraction(jobID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "q2"})
	if err != nil {
		t.Fatal(err)
	}

	// Answering one of two leaves the job parked in pending_interaction.
	if _, err := s.AnswerInteraction(jobID, i1.ID, "a1"); err != nil {
		t.Fatalf("answer i1: %v", err)
	}
	if j, _ := s.Get(jobID); j.Status != StatusPendingInteraction {
		t.Fatalf("expected still pending_interaction with one open, got %s", j.Status)
	}
	// Answering the last pending one resumes running.
	if _, err := s.AnswerInteraction(jobID, i2.ID, "a2"); err != nil {
		t.Fatalf("answer i2: %v", err)
	}
	if j, _ := s.Get(jobID); j.Status != StatusRunning {
		t.Fatalf("expected running after all answered, got %s", j.Status)
	}
}

func TestCreateInteractionOnTerminalJobErrors(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}
	_, err := s.CreateInteraction(final.ID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "q"})
	if !errors.Is(err, ErrJobTerminal) {
		t.Fatalf("expected ErrJobTerminal creating interaction on terminal job, got %v", err)
	}
}

func TestCreateInteractionUnknownJobErrors(t *testing.T) {
	s := newTestService(t, t.TempDir())
	// Prompt is non-empty so validation passes and we reach the unknown-job check.
	_, err := s.CreateInteraction("nope", InteractionInput{Type: InteractionTypeQuestion, Prompt: "q"})
	if !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("expected ErrUnknownJob, got %v", err)
	}
}

func TestCreateInteractionInvalidPayloadErrors(t *testing.T) {
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	// Empty prompt fails validation.
	_, err := s.CreateInteraction(jobID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "   "})
	if !errors.Is(err, ErrInvalidInteraction) {
		t.Fatalf("expected ErrInvalidInteraction for empty prompt, got %v", err)
	}
	// Unknown type fails validation.
	_, err = s.CreateInteraction(jobID, InteractionInput{Type: "bogus", Prompt: "q"})
	if !errors.Is(err, ErrInvalidInteraction) {
		t.Fatalf("expected ErrInvalidInteraction for bad type, got %v", err)
	}
	// Empty type defaults to question and succeeds.
	it, err := s.CreateInteraction(jobID, InteractionInput{Prompt: "q"})
	if err != nil {
		t.Fatalf("empty type should default to question, got %v", err)
	}
	if it.Type != InteractionTypeQuestion {
		t.Fatalf("expected defaulted type question, got %q", it.Type)
	}
}

func TestAnswerUnknownInteractionErrors(t *testing.T) {
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)
	// Unknown job id.
	_, err := s.AnswerInteraction("nope", "ghost", "x")
	if !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("expected ErrUnknownJob, got %v", err)
	}
	// Known job, unknown interaction id.
	_, err = s.AnswerInteraction(jobID, "ghost", "x")
	if !errors.Is(err, ErrUnknownInteraction) {
		t.Fatalf("expected ErrUnknownInteraction, got %v", err)
	}
}

func TestDoubleAnswerErrors(t *testing.T) {
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)
	it, err := s.CreateInteraction(jobID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerInteraction(jobID, it.ID, "first"); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	_, err = s.AnswerInteraction(jobID, it.ID, "second")
	if !errors.Is(err, ErrInteractionState) {
		t.Fatalf("expected ErrInteractionState on second answer, got %v", err)
	}
}

func TestGetPersistedInteractionsFoldsFromDisk(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	jobID := submitRunning(t, s)

	it, err := s.CreateInteraction(jobID, InteractionInput{
		Type:    InteractionTypeChoice,
		Prompt:  "pick",
		Options: []InteractionOption{{Value: "a", Label: "A"}, {Value: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerInteraction(jobID, it.ID, "a"); err != nil {
		t.Fatal(err)
	}

	base := filepath.Join(root, "self")

	// Verify the raw append log holds two snapshots (pending + answered) for the
	// single id before folding.
	raw, err := store.NewFileStore(base).ReadInteractions(jobID)
	if err != nil {
		t.Fatalf("raw ReadInteractions: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("expected 2 raw snapshots, got %d", len(raw))
	}

	// A fresh service with no in-memory entry must fall back to the file and fold
	// to the latest snapshot per id (only the answered one survives).
	fresh := newTestService(t, root)
	got, err := fresh.GetPersistedInteractions(base, jobID)
	if err != nil {
		t.Fatalf("GetPersistedInteractions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 folded interaction, got %d", len(got))
	}
	if got[0].ID != it.ID || got[0].Status != InteractionAnswered || got[0].Answer != "a" {
		t.Fatalf("folded interaction mismatch: %+v", got[0])
	}
	if len(got[0].Options) != 2 || got[0].Options[0].Value != "a" || got[0].Options[0].Label != "A" {
		t.Fatalf("options not preserved through persistence: %+v", got[0].Options)
	}
}

func TestGetPersistedInteractionsMissingIsEmpty(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	got, err := s.GetPersistedInteractions(filepath.Join(root, "self"), "no-such-job")
	if err != nil {
		t.Fatalf("expected no error for missing job, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %d", len(got))
	}
}
