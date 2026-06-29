package answerguard

import (
	"errors"
	"testing"
)

// roleStub is a RoleLookup over a fixed agent_id→role map (a presence.Role double).
type roleStub map[string]string

func (r roleStub) Role(id string) (string, bool) {
	v, ok := r[id]
	return v, ok
}

const (
	owner = "agt_owner"
	sup   = "agt_sup"
	other = "agt_other"
)

func newGuard() *Guard {
	// Whitelist: only prompts starting with "pick " are auto-answerable.
	return New([]string{"^pick "}, roleStub{owner: "", sup: "supervisor", other: ""})
}

func TestCheckHumanAndOwnerAlwaysAllowed(t *testing.T) {
	g := newGuard()
	// Human (no responder) may answer anything, even a confirmation.
	if err := g.Check("", owner, "confirmation", false, "delete prod?"); err != nil {
		t.Fatalf("human must be allowed: %v", err)
	}
	// Owner (responder==origin_agent) may answer anything, even a non-whitelisted choice.
	if err := g.Check(owner, owner, "choice", true, "which DB?"); err != nil {
		t.Fatalf("owner must be allowed: %v", err)
	}
	// Owner may answer a confirmation too.
	if err := g.Check(owner, owner, "confirmation", false, "proceed?"); err != nil {
		t.Fatalf("owner must be allowed on confirmation: %v", err)
	}
}

func TestCheckNonSupervisorDriverAllowed(t *testing.T) {
	g := newGuard()
	// A driver that is neither the owner nor a supervisor is not gated.
	if err := g.Check(other, owner, "confirmation", false, "anything"); err != nil {
		t.Fatalf("non-supervisor driver must be allowed: %v", err)
	}
}

func TestCheckSupervisorGatedByType(t *testing.T) {
	g := newGuard()
	// A supervisor may NOT answer a confirmation (高危) — even if the prompt是 whitelisted.
	if err := g.Check(sup, owner, "confirmation", false, "pick one"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("sup confirmation must be refused, got %v", err)
	}
	// A supervisor may NOT answer a free-text question.
	if err := g.Check(sup, owner, "question", false, "pick a name"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("sup question must be refused, got %v", err)
	}
	// A supervisor may NOT answer a choice with no enumerable options.
	if err := g.Check(sup, owner, "choice", false, "pick one"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("sup no-options choice must be refused, got %v", err)
	}
}

func TestCheckSupervisorGatedByWhitelist(t *testing.T) {
	g := newGuard()
	// A choice OUTSIDE the whitelist is refused for a supervisor.
	if err := g.Check(sup, owner, "choice", true, "delete which file?"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("sup non-whitelisted choice must be refused, got %v", err)
	}
	// A whitelisted choice with options IS allowed for a supervisor.
	if err := g.Check(sup, owner, "choice", true, "pick one format"); err != nil {
		t.Fatalf("sup whitelisted choice must be allowed: %v", err)
	}
}

func TestCheckEmptyWhitelistRefusesSupervisor(t *testing.T) {
	// No patterns ⇒ nothing whitelisted ⇒ a supervisor can derive-answer nothing.
	g := New(nil, roleStub{sup: "supervisor"})
	if err := g.Check(sup, owner, "choice", true, "pick one"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("empty whitelist must refuse sup, got %v", err)
	}
	// Owner/human still pass with no whitelist.
	if err := g.Check("", owner, "choice", true, "pick one"); err != nil {
		t.Fatalf("human must pass even with empty whitelist: %v", err)
	}
}

func TestCheckNilRolesDegradesToOwnerHumanOnly(t *testing.T) {
	// With no role lookup wired, an unknown responder is treated as a non-supervisor → allowed
	// (the guard then only enforces the owner/human distinction — conservative degraded mode).
	g := New([]string{"^pick "}, nil)
	if err := g.Check(sup, owner, "confirmation", false, "x"); err != nil {
		t.Fatalf("nil-roles guard must allow a non-graded responder: %v", err)
	}
}
