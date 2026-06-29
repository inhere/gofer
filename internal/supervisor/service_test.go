package supervisor

import (
	"context"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// mockJobOps is a programmable JobOps: it returns a fixed pending list, an optional
// per-job snapshot map (owner-first routing), and records every AnswerInteraction /
// MarkInteractionEscalated call (optionally returning a forced answer error).
type mockJobOps struct {
	mu        sync.Mutex
	pending   []job.Interaction
	jobs      map[string]job.JobResult // jobID -> snapshot (OriginAgent/EscalateTo)
	answers   []answeredCall
	answerErr error
	escalated []escalatedCall // MarkInteractionEscalated calls (dedup落表)
}

type answeredCall struct{ jobID, interactionID, answer string }

type escalatedCall struct {
	jobID, interactionID string
	ts                   int64
}

func (m *mockJobOps) ListPendingInteractions() ([]job.Interaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]job.Interaction(nil), m.pending...), nil
}

func (m *mockJobOps) AnswerInteraction(jobID, interactionID, answer string) (job.Interaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.answerErr != nil {
		return job.Interaction{}, m.answerErr
	}
	m.answers = append(m.answers, answeredCall{jobID, interactionID, answer})
	return job.Interaction{ID: interactionID, JobID: jobID, Status: job.InteractionAnswered, Answer: answer}, nil
}

// Get returns the recorded job snapshot (owner-first routing). A missing entry yields
// the zero JobResult + false, mirroring job.Service.Get for a non-MCP job (no owner).
func (m *mockJobOps) Get(jobID string) (job.JobResult, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.jobs[jobID]
	return r, ok
}

// MarkInteractionEscalated records the dedup stamp and reflects it into the pending
// list, so a later tick sees EscalatedAt>0 — the DB-backed dedup the real store does.
func (m *mockJobOps) MarkInteractionEscalated(jobID, interactionID string, ts int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.escalated = append(m.escalated, escalatedCall{jobID, interactionID, ts})
	for i := range m.pending {
		if m.pending[i].JobID == jobID && m.pending[i].ID == interactionID {
			m.pending[i].EscalatedAt = ts
		}
	}
	return nil
}

// mockPresence records every Post (escalation). deliver, when set, returns the
// delivered count per target (to simulate an unreachable owner); nil → always 1.
type mockPresence struct {
	mu      sync.Mutex
	posts   []postCall
	deliver func(to string) int
}

type postCall struct{ from, to, kind, body, ref string }

func (m *mockPresence) Post(from, to, kind, body, ref string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posts = append(m.posts, postCall{from, to, kind, body, ref})
	if m.deliver != nil {
		return m.deliver(to), nil
	}
	return 1, nil
}

func choiceIt(id, jobID, prompt string, opts ...string) job.Interaction {
	it := job.Interaction{ID: id, JobID: jobID, Type: job.InteractionTypeChoice, Prompt: prompt, Status: job.InteractionPending}
	for _, o := range opts {
		it.Options = append(it.Options, job.InteractionOption{Value: o})
	}
	return it
}

// newSvc builds a Service with AutoAnswer on and a permissive whitelist by default;
// callers tweak the returned policy fields via the mocks/inputs.
func newSvc(jobs JobOps, pres PresenceOps, p Policy) *Service {
	p.Enabled = true
	return NewService(jobs, pres, p)
}

func TestDecideEscalatesConfirmationAndQuestion(t *testing.T) {
	jobs := &mockJobOps{pending: []job.Interaction{
		{ID: "c1", JobID: "j1", Type: job.InteractionTypeConfirmation, Prompt: "delete?", Status: job.InteractionPending},
		{ID: "q1", JobID: "j2", Type: job.InteractionTypeQuestion, Prompt: "name?", Status: job.InteractionPending},
	}}
	pres := &mockPresence{}
	s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}})
	s.tick(context.Background())

	if len(jobs.answers) != 0 {
		t.Fatalf("confirmation/question must not be auto-answered: %+v", jobs.answers)
	}
	if len(pres.posts) != 2 {
		t.Fatalf("expected 2 escalations, got %d", len(pres.posts))
	}
	for _, p := range pres.posts {
		if p.kind != escalateKind || p.to != "role-one:supervisor" {
			t.Fatalf("bad escalation: %+v", p)
		}
	}
}

func TestChoiceWithOptionsAutoAnswered(t *testing.T) {
	jobs := &mockJobOps{pending: []job.Interaction{choiceIt("i1", "j1", "pick one", "yes", "no")}}
	pres := &mockPresence{}
	s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}})
	s.tick(context.Background())

	if len(jobs.answers) != 1 {
		t.Fatalf("expected 1 auto-answer, got %d", len(jobs.answers))
	}
	if jobs.answers[0].answer != "yes" { // first option (default)
		t.Fatalf("auto-answer chose %q, want first option 'yes'", jobs.answers[0].answer)
	}
	if len(pres.posts) != 0 {
		t.Fatalf("auto-answered choice must not escalate: %+v", pres.posts)
	}
}

func TestChoiceNoOptionsEscalates(t *testing.T) {
	jobs := &mockJobOps{pending: []job.Interaction{choiceIt("i1", "j1", "pick")}} // no options
	pres := &mockPresence{}
	s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}})
	s.tick(context.Background())
	if len(jobs.answers) != 0 || len(pres.posts) != 1 {
		t.Fatalf("no-options choice should escalate: answers=%d posts=%d", len(jobs.answers), len(pres.posts))
	}
}

func TestAutoAnswerOffEscalatesChoice(t *testing.T) {
	jobs := &mockJobOps{pending: []job.Interaction{choiceIt("i1", "j1", "pick", "a", "b")}}
	pres := &mockPresence{}
	s := newSvc(jobs, pres, Policy{AutoAnswer: false, AllowPromptRegex: []string{".*"}})
	s.tick(context.Background())
	if len(jobs.answers) != 0 || len(pres.posts) != 1 {
		t.Fatalf("auto_answer=false must escalate: answers=%d posts=%d", len(jobs.answers), len(pres.posts))
	}
}

func TestEmptyWhitelistEscalatesChoice(t *testing.T) {
	jobs := &mockJobOps{pending: []job.Interaction{choiceIt("i1", "j1", "pick", "a", "b")}}
	pres := &mockPresence{}
	s := newSvc(jobs, pres, Policy{AutoAnswer: true}) // no AllowPromptRegex → nothing whitelisted
	s.tick(context.Background())
	if len(jobs.answers) != 0 || len(pres.posts) != 1 {
		t.Fatalf("empty whitelist must escalate: answers=%d posts=%d", len(jobs.answers), len(pres.posts))
	}
}

func TestPromptRegexGatesAutoAnswer(t *testing.T) {
	jobs := &mockJobOps{pending: []job.Interaction{
		choiceIt("ok", "j1", "deploy to staging?", "yes", "no"),
		choiceIt("no", "j2", "deploy to PROD?", "yes", "no"),
	}}
	pres := &mockPresence{}
	s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{"staging"}})
	s.tick(context.Background())

	if len(jobs.answers) != 1 || jobs.answers[0].interactionID != "ok" {
		t.Fatalf("only the whitelisted prompt should auto-answer: %+v", jobs.answers)
	}
	if len(pres.posts) != 1 || pres.posts[0].ref != "job:j2#no" {
		t.Fatalf("non-whitelisted prompt should escalate: %+v", pres.posts)
	}
}

func TestOverMaxRoundsEscalates(t *testing.T) {
	// MaxRounds=1: the first whitelisted choice auto-answers (round→1), the second
	// (same job) is over budget → escalate.
	jobs := &mockJobOps{pending: []job.Interaction{
		choiceIt("i1", "j1", "ok one", "a"),
		choiceIt("i2", "j1", "ok two", "a"),
	}}
	pres := &mockPresence{}
	s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}, MaxRoundsPerJob: 1})
	s.tick(context.Background())

	if len(jobs.answers) != 1 {
		t.Fatalf("expected 1 auto-answer before budget spent, got %d", len(jobs.answers))
	}
	if len(pres.posts) != 1 {
		t.Fatalf("over-budget interaction should escalate, got %d posts", len(pres.posts))
	}
}

func TestZombieInteractionSkippedSilently(t *testing.T) {
	jobs := &mockJobOps{
		pending:   []job.Interaction{choiceIt("i1", "j1", "pick", "a", "b")},
		answerErr: job.ErrJobTerminal,
	}
	pres := &mockPresence{}
	s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}})
	s.tick(context.Background()) // must not panic, must not escalate a terminal job
	if len(pres.posts) != 0 {
		t.Fatalf("zombie auto-answer should be skipped, not escalated: %+v", pres.posts)
	}
}

func TestNoDuplicateEscalation(t *testing.T) {
	jobs := &mockJobOps{pending: []job.Interaction{
		{ID: "c1", JobID: "j1", Type: job.InteractionTypeConfirmation, Prompt: "ok?", Status: job.InteractionPending},
	}}
	pres := &mockPresence{}
	s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}})
	s.tick(context.Background())
	s.tick(context.Background()) // interaction still pending next tick
	s.tick(context.Background())
	if len(pres.posts) != 1 {
		t.Fatalf("same interaction escalated %d times, want 1 (dedup)", len(pres.posts))
	}
}

func TestDisabledRunReturns(t *testing.T) {
	jobs := &mockJobOps{}
	s := NewService(jobs, &mockPresence{}, Policy{Enabled: false})
	s.Run(context.Background()) // must return immediately (no hang)
}

// confirmIt builds a pending confirmation interaction (always escalated by decide).
func confirmIt(id, jobID string) job.Interaction {
	return job.Interaction{ID: id, JobID: jobID, Type: job.InteractionTypeConfirmation, Prompt: "ok?", Status: job.InteractionPending}
}

// TestEscalateOwnerFirst covers the §8.1 owner-first routing: an interaction is routed
// to its job's origin agent first (BARE agent_id, not "agent:"-prefixed), falls back to
// the policy default when there is no owner / the owner is unreachable, stops at the
// first delivered target, and is deduped across ticks via interactions.escalated_at.
func TestEscalateOwnerFirst(t *testing.T) {
	const policyTo = "role-one:supervisor"

	t.Run("owner_present_routes_to_owner", func(t *testing.T) {
		jobs := &mockJobOps{
			pending: []job.Interaction{confirmIt("c1", "j1")},
			jobs:    map[string]job.JobResult{"j1": {ID: "j1", OriginAgent: "agt_owner"}},
		}
		pres := &mockPresence{}
		s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}, EscalateTo: policyTo})
		s.tick(context.Background())

		if len(pres.posts) != 1 || pres.posts[0].to != "agt_owner" {
			t.Fatalf("owner-first must direct-投 the BARE owner agent_id: %+v", pres.posts)
		}
		if len(jobs.escalated) != 1 || jobs.escalated[0].interactionID != "c1" || jobs.escalated[0].ts == 0 {
			t.Fatalf("a delivered escalation must stamp escalated_at: %+v", jobs.escalated)
		}
	})

	t.Run("owner_empty_routes_to_policy", func(t *testing.T) {
		// No jobs entry → Get returns false → empty owner cols → policy default only.
		jobs := &mockJobOps{pending: []job.Interaction{confirmIt("c1", "j1")}}
		pres := &mockPresence{}
		s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}, EscalateTo: policyTo})
		s.tick(context.Background())

		if len(pres.posts) != 1 || pres.posts[0].to != policyTo {
			t.Fatalf("empty owner must route to the policy default: %+v", pres.posts)
		}
	})

	t.Run("first_delivered_stops_at_owner", func(t *testing.T) {
		// Owner + job-level override + policy all reachable: only the owner is tried.
		jobs := &mockJobOps{
			pending: []job.Interaction{confirmIt("c1", "j1")},
			jobs:    map[string]job.JobResult{"j1": {ID: "j1", OriginAgent: "agt_owner", EscalateTo: "role-one:other"}},
		}
		pres := &mockPresence{}
		s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}, EscalateTo: policyTo})
		s.tick(context.Background())

		if len(pres.posts) != 1 || pres.posts[0].to != "agt_owner" {
			t.Fatalf("first delivered target must stop the chain at the owner: %+v", pres.posts)
		}
	})

	t.Run("owner_unreachable_falls_to_policy", func(t *testing.T) {
		// Owner delivers 0 (offline + pruned) → fall through to the policy default.
		jobs := &mockJobOps{
			pending: []job.Interaction{confirmIt("c1", "j1")},
			jobs:    map[string]job.JobResult{"j1": {ID: "j1", OriginAgent: "agt_gone"}},
		}
		pres := &mockPresence{deliver: func(to string) int {
			if to == "agt_gone" {
				return 0
			}
			return 1
		}}
		s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}, EscalateTo: policyTo})
		s.tick(context.Background())

		if len(pres.posts) != 2 {
			t.Fatalf("must try owner then policy, got %d posts: %+v", len(pres.posts), pres.posts)
		}
		if pres.posts[0].to != "agt_gone" || pres.posts[1].to != policyTo {
			t.Fatalf("fallback order wrong: %+v", pres.posts)
		}
		if len(jobs.escalated) != 1 {
			t.Fatalf("the delivered (policy) escalation must stamp escalated_at: %+v", jobs.escalated)
		}
	})

	t.Run("no_recipient_leaves_pending_unstamped", func(t *testing.T) {
		// Owner gone AND no online sup → delivered=0 everywhere: not stamped, retries.
		jobs := &mockJobOps{
			pending: []job.Interaction{confirmIt("c1", "j1")},
			jobs:    map[string]job.JobResult{"j1": {ID: "j1", OriginAgent: "agt_gone"}},
		}
		pres := &mockPresence{deliver: func(string) int { return 0 }}
		s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}, EscalateTo: policyTo})
		s.tick(context.Background())

		if len(jobs.escalated) != 0 {
			t.Fatalf("an undelivered escalation must NOT stamp escalated_at: %+v", jobs.escalated)
		}
	})

	t.Run("dedup_across_ticks", func(t *testing.T) {
		jobs := &mockJobOps{
			pending: []job.Interaction{confirmIt("c1", "j1")},
			jobs:    map[string]job.JobResult{"j1": {ID: "j1", OriginAgent: "agt_owner"}},
		}
		pres := &mockPresence{}
		s := newSvc(jobs, pres, Policy{AutoAnswer: true, AllowPromptRegex: []string{".*"}, EscalateTo: policyTo})
		s.tick(context.Background())
		s.tick(context.Background()) // escalated_at now set → skipped
		s.tick(context.Background())

		if len(pres.posts) != 1 {
			t.Fatalf("escalated_at dedup failed: escalated %d times, want 1", len(pres.posts))
		}
	})
}
