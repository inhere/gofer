package serve

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// gofer-81b4: `mail.agents.<名字>.approval` reaches the resident session's spec (and from
// there the job), so a 收件会话 can ask a human while the project keeps auto-allowing.
func TestMailResidentsCarriesApproval(t *testing.T) {
	ask := &config.ApprovalConfig{Mode: config.ApprovalAsk, TimeoutSec: 90}
	m := config.MailConfig{Agents: map[string]config.MailAgentConfig{
		"suga":  {Agent: "suga-acp", Approval: ask},
		"plain": {Agent: "codex-acp"},
	}}
	rc := mailResidents(m)

	got, ok := rc.Agent("suga")
	if !ok || got.Approval == nil {
		t.Fatalf("suga spec = %+v, want the configured approval", got)
	}
	if got.Approval.Mode != config.ApprovalAsk || got.Approval.TimeoutSec != 90 {
		t.Fatalf("approval = %+v, want mode=ask timeout=90", *got.Approval)
	}
	// Unset stays nil: the session is then approved by the project's policy.
	if plain, _ := rc.Agent("plain"); plain.Approval != nil {
		t.Fatalf("plain approval = %+v, want nil", plain.Approval)
	}

	// The spec is a snapshot: a later config edit cannot rewrite what the session runs under.
	ask.Mode = config.ApprovalStrict
	if got, _ := rc.Agent("suga"); got.Approval.Mode != config.ApprovalAsk {
		t.Fatalf("the spec shares the config's block: mode = %q", got.Approval.Mode)
	}
}
