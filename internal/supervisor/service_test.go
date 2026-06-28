package supervisor

import (
	"context"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// mockJobOps is a programmable JobOps: it returns a fixed pending list and records
// every AnswerInteraction call (optionally returning a forced error).
type mockJobOps struct {
	mu        sync.Mutex
	pending   []job.Interaction
	answers   []answeredCall
	answerErr error
}

type answeredCall struct{ jobID, interactionID, answer string }

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

// mockPresence records every Post (escalation).
type mockPresence struct {
	mu    sync.Mutex
	posts []postCall
}

type postCall struct{ from, to, kind, body, ref string }

func (m *mockPresence) Post(from, to, kind, body, ref string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posts = append(m.posts, postCall{from, to, kind, body, ref})
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
		if p.kind != escalateKind || p.to != "role:supervisor" {
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
