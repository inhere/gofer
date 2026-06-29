package job

import (
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/answerguard"
)

// roleStub is a RoleLookup over a fixed agent_id→role map (a presence.Role double): it lets
// the integration test grade a responder as owner / supervisor / other without a real presence.
type roleStub map[string]string

func (r roleStub) Role(id string) (string, bool) { v, ok := r[id]; return v, ok }

const (
	gateOwner = "agt_owner"
	gateSup   = "agt_sup"
)

// submitRunningOwned submits a long-lived exec job stamped with origin_agent=owner and waits
// until it is running, so interactions are raised while the job is genuinely live.
func submitRunningOwned(t *testing.T, s *Service, owner string) string {
	t.Helper()
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "30"}, Cwd: ".", TimeoutSec: 60,
		OriginAgent: owner,
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

func mustPending(t *testing.T, s *Service, jobID, iid string) {
	t.Helper()
	list, _ := s.GetInteractions(jobID)
	for _, it := range list {
		if it.ID == iid {
			if it.Status != InteractionPending {
				t.Fatalf("interaction %s expected pending, got %s (answered_by=%q)", iid, it.Status, it.AnsweredBy)
			}
			return
		}
	}
	t.Fatalf("interaction %s not found", iid)
}

// TestDerivedAnswerWhitelistGate proves P3.1: a通用 sup driver is refused on a confirmation and
// on a non-whitelisted choice (interaction stays pending), while the owner answers the same ones.
func TestDerivedAnswerWhitelistGate(t *testing.T) {
	s := newTestService(t, t.TempDir())
	s.SetAnswerGuard(answerguard.New([]string{"^pick "}, roleStub{gateOwner: "", gateSup: "supervisor"}))
	jobID := submitRunningOwned(t, s, gateOwner)

	// (A) sup answering a confirmation → refused, stays pending; owner then succeeds.
	c1 := mkInteraction(t, s, jobID, InteractionTypeConfirmation, "delete prod?")
	if _, err := s.AnswerInteractionBy(jobID, c1.ID, "yes", gateSup); !errors.Is(err, ErrAnswerNotAllowed) {
		t.Fatalf("sup confirmation must be refused, got %v", err)
	}
	mustPending(t, s, jobID, c1.ID)
	owned, err := s.AnswerInteractionBy(jobID, c1.ID, "yes", gateOwner)
	if err != nil {
		t.Fatalf("owner must answer the confirmation: %v", err)
	}
	if owned.AnsweredBy != "agent:"+gateOwner {
		t.Fatalf("owner answered_by = %q, want agent:%s", owned.AnsweredBy, gateOwner)
	}

	// (B) sup answering a non-whitelisted choice → refused, stays pending.
	ch1 := mkInteraction(t, s, jobID, InteractionTypeChoice, "which file to delete?", "a", "b")
	if _, err := s.AnswerInteractionBy(jobID, ch1.ID, "a", gateSup); !errors.Is(err, ErrAnswerNotAllowed) {
		t.Fatalf("sup non-whitelisted choice must be refused, got %v", err)
	}
	mustPending(t, s, jobID, ch1.ID)
	if _, err := s.AnswerInteractionBy(jobID, ch1.ID, "a", gateOwner); err != nil {
		t.Fatalf("owner must answer the non-whitelisted choice: %v", err)
	}

	// (C) sup answering a WHITELISTED choice with options → allowed, answered_by=agent:sup.
	ch2 := mkInteraction(t, s, jobID, InteractionTypeChoice, "pick one format", "json", "yaml")
	supAns, err := s.AnswerInteractionBy(jobID, ch2.ID, "json", gateSup)
	if err != nil {
		t.Fatalf("sup whitelisted choice must be allowed: %v", err)
	}
	if supAns.AnsweredBy != "agent:"+gateSup {
		t.Fatalf("sup answered_by = %q, want agent:%s", supAns.AnsweredBy, gateSup)
	}
}

// TestAnsweredBySources proves P3.2: the four answer sources stamp distinct answered_by tags
// (auto:<policy> / agent:<owner> / agent:<sup> / human), each persisted and round-tripped.
func TestAnsweredBySources(t *testing.T) {
	s := newTestService(t, t.TempDir())
	s.SetAnswerGuard(answerguard.New([]string{"^pick "}, roleStub{gateOwner: "", gateSup: "supervisor"}))
	jobID := submitRunningOwned(t, s, gateOwner)

	cases := []struct {
		name   string
		prompt string
		do     func(iid string) (Interaction, error)
		wantBy string
	}{
		{"auto", "pick auto", func(iid string) (Interaction, error) {
			return s.AnswerInteractionAuto(jobID, iid, "json", "choice")
		}, "auto:choice"},
		{"owner", "pick owner", func(iid string) (Interaction, error) {
			return s.AnswerInteractionBy(jobID, iid, "json", gateOwner)
		}, "agent:" + gateOwner},
		{"sup", "pick sup", func(iid string) (Interaction, error) {
			return s.AnswerInteractionBy(jobID, iid, "json", gateSup)
		}, "agent:" + gateSup},
		{"human", "pick human", func(iid string) (Interaction, error) {
			return s.AnswerInteractionBy(jobID, iid, "json", "")
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
