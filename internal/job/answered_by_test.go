package job

import "testing"

func mkInteraction(t *testing.T, s *Service, jobID, typ, prompt string, opts ...string) Interaction {
	t.Helper()
	in := InteractionInput{Type: typ, Prompt: prompt}
	for _, o := range opts {
		in.Options = append(in.Options, InteractionOption{Value: o})
	}
	it, err := s.CreateInteraction(jobID, in)
	if err != nil {
		t.Fatalf("CreateInteraction(%s): %v", prompt, err)
	}
	return it
}

func TestHumanAnswerStampsCaller(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	it := mkInteraction(t, s, jobID, InteractionTypeConfirmation, "delete prod?")
	ans, err := s.AnswerInteractionByHuman(jobID, it.ID, "yes", "alice")
	if err != nil {
		t.Fatalf("human answer: %v", err)
	}
	if ans.AnsweredBy != "alice" {
		t.Fatalf("human answered_by = %q, want alice", ans.AnsweredBy)
	}
}

func TestHumanAnswerEmptyCallerFallsBackToHuman(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	it := mkInteraction(t, s, jobID, InteractionTypeQuestion, "continue?")
	ans, err := s.AnswerInteractionByHuman(jobID, it.ID, "ok", "")
	if err != nil {
		t.Fatalf("human answer with empty caller: %v", err)
	}
	if ans.AnsweredBy != "human" {
		t.Fatalf("empty-caller answered_by = %q, want human", ans.AnsweredBy)
	}
}

// TestAnsweredBySources proves the answer sources stamp distinct answered_by tags
// (auto:<policy> / human), each persisted and round-tripped; the bare relay path
// leaves it empty.
func TestAnsweredBySources(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	cases := []struct {
		name   string
		prompt string
		do     func(iid string) (Interaction, error)
		wantBy string
	}{
		{"auto", "pick auto", func(iid string) (Interaction, error) {
			return s.AnswerInteractionAuto(jobID, iid, "json", "choice")
		}, "auto:choice"},
		{"human", "pick human", func(iid string) (Interaction, error) {
			return s.AnswerInteractionByHuman(jobID, iid, "json", "")
		}, "human"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := mkInteraction(t, s, jobID, InteractionTypeChoice, tc.prompt, "json", "yaml")
			ans, err := tc.do(it.ID)
			if err != nil {
				t.Fatalf("answer: %v", err)
			}
			if ans.AnsweredBy != tc.wantBy {
				t.Fatalf("answered_by = %q, want %q", ans.AnsweredBy, tc.wantBy)
			}
			// Round-trips through GetInteractions (persisted snapshot).
			list, _ := s.GetInteractions(jobID)
			for _, g := range list {
				if g.ID == it.ID && g.AnsweredBy != tc.wantBy {
					t.Fatalf("persisted answered_by = %q, want %q", g.AnsweredBy, tc.wantBy)
				}
			}
		})
	}

	// The bare/unattributed path (internal relay) leaves answered_by empty.
	it := mkInteraction(t, s, jobID, InteractionTypeQuestion, "relayed?")
	relay, err := s.AnswerInteraction(jobID, it.ID, "ok")
	if err != nil {
		t.Fatalf("bare answer: %v", err)
	}
	if relay.AnsweredBy != "" {
		t.Fatalf("unattributed answered_by = %q, want empty", relay.AnsweredBy)
	}
}
