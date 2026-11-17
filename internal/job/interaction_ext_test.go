package job

import (
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/wait"
)

var askOptions = []InteractionOption{
	{Value: "a", Label: "a", Description: "first"},
	{Value: "b", Label: "b"},
}

func raiseChoice(t *testing.T, s *Service, jobID string, in InteractionInput) Interaction {
	t.Helper()
	in.Type = InteractionTypeChoice
	in.Prompt = "which?"
	in.Options = askOptions
	it, err := s.CreateInteraction(jobID, in)
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}
	return it
}

func TestInteractionExtRoundTripsThroughThePersistedRow(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)
	it := raiseChoice(t, s, jobID, InteractionInput{Multi: true, AllowOther: true, Recommended: "b"})

	recs, err := s.meta.ListInteractions(jobID)
	if err != nil || len(recs) != 1 {
		t.Fatalf("ListInteractions = %v, %v", recs, err)
	}
	got := fromInteractionRecord(recs[0])
	if got.ID != it.ID || !got.Multi || !got.AllowOther || got.Recommended != "b" ||
		len(got.Options) != 2 || got.Options[0].Description != "first" {
		t.Fatalf("persisted interaction lost its extension: %+v", got)
	}
}

func TestInteractionWithoutExtKeepsThePlainOptionsArray(t *testing.T) {
	t.Parallel()
	enc := encodeInteractionOptions(Interaction{Options: askOptions})
	if enc == "" || enc[0] != '[' {
		t.Fatalf("an interaction without extension must keep the plain array form: %q", enc)
	}
	opts, ext := DecodeInteractionOptions(enc)
	if len(opts) != 2 || ext != (InteractionExt{}) {
		t.Fatalf("decode = %+v %+v", opts, ext)
	}
}

func TestInteractionAnswerValidation(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	single := raiseChoice(t, s, jobID, InteractionInput{})
	if _, err := s.AnswerInteractionByHuman(jobID, single.ID, "zzz", ""); !errors.Is(err, ErrInvalidInteraction) {
		t.Fatalf("single-select answer outside the options = %v, want ErrInvalidInteraction", err)
	}
	if got, err := s.AnswerInteractionByHuman(jobID, single.ID, "a", ""); err != nil || got.Answer != "a" {
		t.Fatalf("single-select answer a = %+v, %v", got, err)
	}

	other := raiseChoice(t, s, jobID, InteractionInput{AllowOther: true})
	if got, err := s.AnswerInteractionByHuman(jobID, other.ID, "something else", ""); err != nil || got.Answer != "something else" {
		t.Fatalf("allow_other free text = %+v, %v", got, err)
	}

	multi := raiseChoice(t, s, jobID, InteractionInput{Multi: true})
	for _, bad := range []string{"a", `["zzz"]`, `[]`} {
		if _, err := s.AnswerInteractionByHuman(jobID, multi.ID, bad, ""); !errors.Is(err, ErrInvalidInteraction) {
			t.Fatalf("multi answer %q = %v, want ErrInvalidInteraction", bad, err)
		}
	}
	if got, err := s.AnswerInteractionByHuman(jobID, multi.ID, `["a","b"]`, ""); err != nil || got.Answer != `["a","b"]` {
		t.Fatalf("multi answer = %+v, %v", got, err)
	}

	free, err := s.CreateInteraction(jobID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "why?"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerInteractionByHuman(jobID, free.ID, "because", ""); err != nil {
		t.Fatalf("free-text question must accept any answer: %v", err)
	}
}

func TestInteractionExtRejectedWithoutMatchingOptions(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)
	for _, in := range []InteractionInput{
		{Type: InteractionTypeQuestion, Prompt: "q", Multi: true},
		{Type: InteractionTypeChoice, Prompt: "q", Options: askOptions, Recommended: "zzz"},
	} {
		if _, err := s.CreateInteraction(jobID, in); !errors.Is(err, ErrInvalidInteraction) {
			t.Fatalf("CreateInteraction(%+v) = %v, want ErrInvalidInteraction", in, err)
		}
	}
}

// An interaction raised with a deadline is cancelled by the server when nobody answers, and
// the job leaves pending_interaction; an answered one is left alone.
func TestInteractionDeadlineCancelsAndFreesTheJob(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	it, err := s.CreateInteraction(jobID, InteractionInput{
		Type: InteractionTypeQuestion, Prompt: "hurry", ExpiresAt: time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wait.Until(t, 10*time.Second, "interaction cancelled at its deadline", func() bool {
		list, _ := s.GetInteractions(jobID)
		return len(list) == 1 && list[0].ID == it.ID && list[0].Status == InteractionCancelled
	})
	if j, _ := s.Get(jobID); j.Status != StatusRunning {
		t.Fatalf("job status after the expiry = %s, want running", j.Status)
	}
}
