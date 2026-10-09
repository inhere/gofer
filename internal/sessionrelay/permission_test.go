package sessionrelay

import (
	"context"
	"errors"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func permInput(fp string) PermissionInput {
	return PermissionInput{PermissionDetail: PermissionDetail{
		ToolName: "Bash", Summary: "Bash `rm -rf node_modules`", Input: `{"command":"rm -rf node_modules"}`,
		Suggestions: []PermissionSuggestion{{Label: "规则 Bash(rm:*)"}}, Fingerprint: fp,
	}, TimeoutSec: 600}
}

func TestPermissionOpenNeedsAWaitingSession(t *testing.T) {
	s := newSvc(t)
	_, err := s.Register(RegisterInput{SessionID: "sid-p", Agent: "claude"})
	assert.NoErr(t, err)
	// auto with nothing armed: the person is at the keyboard → the terminal handles it
	_, err = s.OpenPermission("sid-p", permInput("fp1"))
	assert.True(t, errors.Is(err, ErrRelayOff))

	_, err = s.SetRelayMode("sid-p", jobstore.RelayModeOn)
	assert.NoErr(t, err)
	d, err := s.OpenPermission("sid-p", permInput("fp1"))
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.DecisionKindPermission, d.Kind)
	assert.Eq(t, "需要授权：Bash `rm -rf node_modules`", d.Question)
	assert.Eq(t, "fp1", ParsePermissionDetail(d.Detail).Fingerprint)
	a, _ := s.Session("sid-p")
	assert.Eq(t, jobstore.SessionNeedsAttention, a.State)

	// a permission prompt is not a turn: say / deliver never answer it with free text
	_, err = s.Say("sid-p", "yes do it", "u1")
	assert.True(t, errors.Is(err, ErrNoOpenTurn))

	// a newer prompt supersedes the old one
	d2, err := s.OpenPermission("sid-p", permInput("fp2"))
	assert.NoErr(t, err)
	old, _, _ := s.store.GetDecision(d.ID)
	assert.Eq(t, jobstore.DecisionExpired, old.State)
	assert.Eq(t, ReleaseBySuperseded, old.ReleasedBy)

	// the long poll sees the answer
	got, err := s.AnswerPermission("sid-p", d2.ID, "always:0", "u1")
	assert.NoErr(t, err)
	assert.Eq(t, "always:0", got.Answer)
	st, err := s.WaitTurn(context.Background(), "sid-p", d2.ID, 0)
	assert.NoErr(t, err)
	assert.Eq(t, TurnAnswered, st.Outcome)
	a, _ = s.Session("sid-p")
	assert.Eq(t, jobstore.SessionRunning, a.State)
	ev, ok, err := s.store.LatestAuditEvent(AuditPermissionAnswered, "sid-p")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "u1", ev.Actor)
}

func TestPermissionAnswerValidation(t *testing.T) {
	s := newSvc(t)
	_, _ = s.Register(RegisterInput{SessionID: "sid-v", Agent: "claude"})
	_, _ = s.SetRelayMode("sid-v", jobstore.RelayModeOn)
	d, err := s.OpenPermission("sid-v", permInput("fp"))
	assert.NoErr(t, err)
	for _, bad := range []string{"always:1", "always:-1", "yes", ""} {
		_, err := s.AnswerPermission("sid-v", d.ID, bad, "u1")
		assert.True(t, errors.Is(err, ErrInvalidInput), bad)
	}
	_, err = s.AnswerPermission("other", d.ID, "allow", "u1")
	assert.True(t, errors.Is(err, ErrUnknownTurn))
	got, err := s.AnswerPermission("sid-v", d.ID, "deny: 不要删 ", "u1")
	assert.NoErr(t, err)
	assert.Eq(t, "deny:不要删", got.Answer)
	_, err = s.AnswerPermission("sid-v", d.ID, "allow", "u1")
	assert.True(t, errors.Is(err, ErrNoOpenTurn))

	// a relay turn is not answerable as a permission
	turn, err := s.OpenTurn("sid-v", "done", 600)
	assert.NoErr(t, err)
	_, err = s.AnswerPermission("sid-v", turn.ID, "allow", "u1")
	assert.True(t, errors.Is(err, ErrUnknownTurn))
}

func TestPermissionSettledInTheTerminal(t *testing.T) {
	s := newSvc(t)
	_, _ = s.Register(RegisterInput{SessionID: "sid-t", Agent: "claude"})
	_, _ = s.SetRelayMode("sid-t", jobstore.RelayModeOn)

	// PostToolUse of ANOTHER call leaves it; the matching call settles it
	d, _ := s.OpenPermission("sid-t", permInput("fp-a"))
	n, err := s.ResolvePermissions("sid-t", "fp-other")
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
	n, err = s.ResolvePermissions("sid-t", "fp-a")
	assert.NoErr(t, err)
	assert.Eq(t, 1, n)
	got, _, _ := s.store.GetDecision(d.ID)
	assert.Eq(t, ReleaseByTerminal, got.ReleasedBy)
	st, _ := s.WaitTurn(context.Background(), "sid-t", d.ID, 0)
	assert.Eq(t, TurnExpired, st.Outcome)

	// a typed prompt (even with relay `on`) / a Stop settle every pending prompt
	for _, ev := range []string{EventUserPromptSubmit, EventStop} {
		d, err := s.OpenPermission("sid-t", permInput("fp-b"))
		assert.NoErr(t, err)
		_, err = s.Heartbeat("sid-t", HeartbeatInput{Event: ev, Injected: true})
		assert.NoErr(t, err)
		got, _, _ := s.store.GetDecision(d.ID)
		assert.Eq(t, jobstore.DecisionExpired, got.State, ev)
		assert.Eq(t, ReleaseByTerminal, got.ReleasedBy, ev)
	}
}

func TestPermissionNotificationKeepsPreciseMessage(t *testing.T) {
	s := newSvc(t)
	f := &fakeNotifier{}
	s.SetNotifier(f)
	_, _ = s.Register(RegisterInput{SessionID: "sid-m", Agent: "claude"})
	_, _ = s.SetRelayMode("sid-m", jobstore.RelayModeOn)
	a, err := s.Heartbeat("sid-m", HeartbeatInput{Event: EventPermissionRequest, LastMessage: "需要授权：Bash `ls`"})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.SessionNeedsAttention, a.State)
	a, err = s.Heartbeat("sid-m", HeartbeatInput{Event: EventNotification, LastMessage: "Claude needs your permission"})
	assert.NoErr(t, err)
	assert.Eq(t, "需要授权：Bash `ls`", a.LastMessage)
	assert.Eq(t, []string{"sid-m:需要授权：Bash `ls`"}, f.attention)
}

// TestPermissionIgnoresSupervisingGate: the SUP-01 D gate (running sub-agents, the
// caller's live jobs) keeps a Stop from parking, but a permission dialog already
// blocks the terminal — open, wait and the idle-probe release must not apply it, or a
// supervisor away from the keyboard could never answer the prompt from the web.
func TestPermissionIgnoresSupervisingGate(t *testing.T) {
	s := newSvc(t)
	s.AutoArmIdleSec, s.SkipWhenSupervising, s.SupervisingWindowSec = 300, true, 7200
	_, err := s.Register(RegisterInput{SessionID: "sid-g", Agent: "claude"})
	assert.NoErr(t, err)
	idle := int64(600)
	_, err = s.Heartbeat("sid-g", HeartbeatInput{Event: EventPermissionRequest, IdleSec: &idle})
	assert.NoErr(t, err)
	subBeat(t, s, "sid-g", EventSubagentStart, "sub-1")

	a, _ := s.Session("sid-g")
	reason, detail := s.WaitDecision(a)
	assert.Eq(t, "", reason) // a Stop does not wait: the gate holds
	assert.Contains(t, detail, "supervising 1 subagents")
	assert.Eq(t, WaitIdleProbe, s.PermissionWaitReason(a))

	d, err := s.OpenPermission("sid-g", permInput("fp-g"))
	assert.NoErr(t, err)
	st, err := s.WaitTurn(context.Background(), "sid-g", d.ID, 0)
	assert.NoErr(t, err)
	assert.Eq(t, TurnOpen, st.Outcome) // not released as relay_off
	assert.True(t, st.Relay)
	assert.Eq(t, WaitIdleProbe, st.Reason)

	// a second sub-agent starting mid-wait still leaves the prompt open
	subBeat(t, s, "sid-g", EventSubagentStart, "sub-2")
	st, _ = s.WaitTurn(context.Background(), "sid-g", d.ID, 0)
	assert.Eq(t, TurnOpen, st.Outcome)

	// the idle-probe release still works for the permission wait
	released, err := s.ReleaseTurn("sid-g", d.ID, 5)
	assert.NoErr(t, err)
	assert.True(t, released)

	// relay turns (Stop) keep the gate
	_, err = s.OpenTurn("sid-g", "done", 600)
	assert.True(t, errors.Is(err, ErrRelayOff))
}
