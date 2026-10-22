package sessionrelay

// gofer-k23r (design 2026-10-22 D3–D6): the terminal delivery guard, the three delivery
// modes, and the "held" message that the session's next hook injects. The pane captures
// below are the fixtures the agent rules are written against: an empty input box, a box
// with text a person typed, and a question dialog with its selection cursor.

import (
	"context"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// claudeEmptyPane is Claude Code at its prompt with nothing typed (placeholder shown).
const claudeEmptyPane = `╭──────────────────────────────────────────────────────────╮
│ > Try "fix the failing test"                             │
╰──────────────────────────────────────────────────────────╯
  ? for shortcuts                                    ⏵⏵ accept edits
`

// claudeTypedPane is the same box with text the person has not submitted yet.
const claudeTypedPane = `╭──────────────────────────────────────────────────────────╮
│ > explain the retry logic in deliver.go                  │
╰──────────────────────────────────────────────────────────╯
`

// claudeSelectingPane is the AskUserQuestion / permission dialog.
const claudeSelectingPane = `╭──────────────────────────────────────────────────────────╮
│ Do you want to proceed?                                  │
│ ❯ 1. Yes                                                 │
│   2. No, tell me what to do instead                      │
╰──────────────────────────────────────────────────────────╯
`

// codexEmptyPane / codexTypedPane / codexSelectingPane are the codex TUI equivalents.
const codexEmptyPane = `›
  ? for shortcuts
`

const codexTypedPane = `› ship the guard and the three modes
  ? for shortcuts
`

const codexSelectingPane = `  Question
› 1. Yes
  2. No
`

// TestClassifyInjectGuard pins the per-agent fixture judgement: only a positively empty
// input area reads GuardEmpty, a dialog is judged before anything else, and a screen the
// rules do not know stays GuardUnknown (never "empty").
func TestClassifyInjectGuard(t *testing.T) {
	for _, tc := range []struct {
		name    string
		agent   string
		capture string
		want    string
	}{
		{"claude empty", "claude", claudeEmptyPane, GuardEmpty},
		{"claude typed", "claude", claudeTypedPane, GuardHasInput},
		{"claude dialog", "claude", claudeSelectingPane, GuardSelecting},
		{"codex empty", "codex", codexEmptyPane, GuardEmpty},
		{"codex typed", "codex", codexTypedPane, GuardHasInput},
		{"codex dialog", "codex", codexSelectingPane, GuardSelecting},
		{"unknown screen", "claude", "some build output\nno prompt in sight\n", GuardUnknown},
		{"no rules for the agent", "myagent", claudeEmptyPane, GuardUnknown},
		{"blank capture", "claude", "\n\n   \n", GuardUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := DefaultInjectGuard(tc.agent)
			assert.Eq(t, tc.want, ClassifyInjectGuard(tc.capture, rules))
		})
	}
}

// guardInjector answers the guard probe with a scripted capture and the injection with a
// success, keeping the two kinds of job apart by their tags.
type guardInjector struct {
	capture string
	// exit / output override the probe's answer (a pane that cannot be read).
	exit int
	reqs []InjectRequest
}

func (g *guardInjector) InjectSession(_ context.Context, req InjectRequest) (InjectResult, error) {
	g.reqs = append(g.reqs, req)
	if len(req.Tags) > 0 && req.Tags[0] == TagRelayGuard {
		return InjectResult{JobID: "job-guard", ExitCode: g.exit, Output: g.capture}, nil
	}
	return InjectResult{JobID: "job-paste", ExitCode: 0}, nil
}

// guardReqCount counts the probe jobs and the paste jobs separately.
func (g *guardInjector) counts() (probes, pastes int) {
	for _, r := range g.reqs {
		if len(r.Tags) > 0 && r.Tags[0] == TagRelayGuard {
			probes++
		} else {
			pastes++
		}
	}
	return probes, pastes
}

// heldMails returns the session's pending letters (what its next hook would claim).
// It only READS them: claiming is what would take them away from the test.
func heldMails(t *testing.T, s *Service, sid string) []jobstore.Mail {
	t.Helper()
	list, err := s.store.ListMail(jobstore.ListMailOpts{
		ToKind: jobstore.MailKindSession, ToID: sid,
	})
	assert.NoErr(t, err)
	pending := make([]jobstore.Mail, 0, len(list))
	for _, m := range list {
		if m.State == jobstore.MailPending {
			pending = append(pending, m)
		}
	}
	return pending
}

// claimHeld takes the session's pending letters the way its hook would.
func claimHeld(t *testing.T, s *Service, sid string) []jobstore.TakenMail {
	t.Helper()
	taken, _, err := s.store.ClaimSessionMail(sid, jobstore.TakeMailOpts{Limit: 10})
	assert.NoErr(t, err)
	return taken
}

// TestDeliverAutoModeRoutesByInputArea pins the auto rule (D4/D5): an empty input area is
// the only one typed into; a typed line, a dialog and an unreadable screen each leave the
// message waiting in the mailbox, with the reason recorded as an event.
func TestDeliverAutoModeRoutesByInputArea(t *testing.T) {
	for _, tc := range []struct {
		name    string
		capture string
		exit    int
		want    string // PathTmux (typed) or PathHeld
		reason  string // the recorded hold reason
	}{
		{"empty", claudeEmptyPane, 0, PathTmux, ""},
		{"typed", claudeTypedPane, 0, PathHeld, ReasonInputHasInput},
		{"dialog", claudeSelectingPane, 0, PathHeld, ReasonSelecting},
		{"unknown", "no known prompt here\n", 0, PathHeld, ReasonGuardUnknown},
		{"pane gone", "", 3, PathHeld, ReasonGuardUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSvc(t)
			inj := &guardInjector{capture: tc.capture, exit: tc.exit}
			s.SetInjector(inj)
			s.SetAgentInjectGuard(DefaultInjectGuard)
			tmuxSession(t, s, "sid-guard")
			_, err := s.Heartbeat("sid-guard", HeartbeatInput{Event: EventStop})
			assert.NoErr(t, err)

			res, err := s.Deliver(context.Background(), "sid-guard", "carry on", "alice", "alice", false)
			assert.NoErr(t, err)
			assert.Eq(t, tc.want, res.Path)
			probes, pastes := inj.counts()
			assert.Eq(t, 1, probes)
			if tc.want == PathHeld {
				assert.Eq(t, 0, pastes)
				assert.True(t, res.MailID > 0)
				mails := heldMails(t, s, "sid-guard")
				assert.Len(t, mails, 1)
				assert.Contains(t, mails[0].Body, "carry on")
				ev, ok, err := s.store.LatestAuditEvent(AuditInjectHold, "sid-guard")
				assert.NoErr(t, err)
				assert.True(t, ok)
				assert.Contains(t, ev.Detail, tc.reason)
			} else {
				assert.Eq(t, 1, pastes)
			}
		})
	}
}

// TestDeliverHoldAndForceModes pins the other two modes: hold never dispatches anything
// (not even the probe), force pastes without looking at the pane.
func TestDeliverHoldAndForceModes(t *testing.T) {
	t.Run("hold", func(t *testing.T) {
		s := newSvc(t)
		inj := &guardInjector{capture: claudeEmptyPane}
		s.SetInjector(inj)
		s.SetAgentInjectGuard(DefaultInjectGuard)
		tmuxSession(t, s, "sid-hold")
		_, err := s.SetInjectMode("sid-hold", jobstore.InjectModeHold, "alice")
		assert.NoErr(t, err)

		res, err := s.Deliver(context.Background(), "sid-hold", "carry on", "alice", "alice", false)
		assert.NoErr(t, err)
		assert.Eq(t, PathHeld, res.Path)
		probes, pastes := inj.counts()
		assert.Eq(t, 0, probes)
		assert.Eq(t, 0, pastes)
		ev, ok, err := s.store.LatestAuditEvent(AuditInjectHold, "sid-hold")
		assert.NoErr(t, err)
		assert.True(t, ok)
		assert.Contains(t, ev.Detail, ReasonHoldMode)
	})

	t.Run("force", func(t *testing.T) {
		s := newSvc(t)
		inj := &guardInjector{capture: claudeTypedPane}
		s.SetInjector(inj)
		s.SetAgentInjectGuard(DefaultInjectGuard)
		tmuxSession(t, s, "sid-force")
		_, err := s.SetInjectMode("sid-force", jobstore.InjectModeForce, "alice")
		assert.NoErr(t, err)

		res, err := s.Deliver(context.Background(), "sid-force", "carry on", "alice", "alice", false)
		assert.NoErr(t, err)
		assert.Eq(t, PathTmux, res.Path)
		probes, pastes := inj.counts()
		assert.Eq(t, 0, probes)
		assert.Eq(t, 1, pastes)
	})
}

// TestHeldMessageIsInjectedByTheNextHook pins the "nothing is lost" promise (D5/D6): a
// held message becomes an ordinary pending letter, so the EXISTING hook take (claim →
// confirm) hands it to the agent, and the box is empty again afterwards.
func TestHeldMessageIsInjectedByTheNextHook(t *testing.T) {
	s := newSvc(t)
	inj := &guardInjector{capture: claudeSelectingPane}
	s.SetInjector(inj)
	s.SetAgentInjectGuard(DefaultInjectGuard)
	tmuxSession(t, s, "sid-later")

	res, err := s.Deliver(context.Background(), "sid-later", "go with option 2", "alice", "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathHeld, res.Path)

	taken := claimHeld(t, s, "sid-later")
	assert.Len(t, taken, 1)
	assert.Contains(t, taken[0].Body, "go with option 2")

	ids := []int64{taken[0].ID}
	confirmed, err := s.store.ConfirmSessionMail("sid-later", ids)
	assert.NoErr(t, err)
	assert.Len(t, confirmed, 1)
	assert.Len(t, heldMails(t, s, "sid-later"), 0)
}

// TestDeliverHeldSendsTheWaitingLetters pins 「现在送」: the waiting letters are pasted
// into the terminal at once, past the guard (the person asked), and marked delivered.
func TestDeliverHeldSendsTheWaitingLetters(t *testing.T) {
	s := newSvc(t)
	inj := &guardInjector{capture: claudeTypedPane}
	s.SetInjector(inj)
	s.SetAgentInjectGuard(DefaultInjectGuard)
	tmuxSession(t, s, "sid-flush")

	for _, text := range []string{"first waiting", "second waiting"} {
		_, err := s.Deliver(context.Background(), "sid-flush", text, "alice", "alice", false)
		assert.NoErr(t, err)
	}
	assert.Len(t, heldMails(t, s, "sid-flush"), 2)

	res, err := s.DeliverHeld(context.Background(), "sid-flush", "alice", "alice")
	assert.NoErr(t, err)
	assert.Eq(t, PathTmux, res.Path)
	paste := inj.reqs[len(inj.reqs)-1]
	assert.True(t, len(paste.Tags) > 0 && paste.Tags[0] == TagRelayInject, "the flush must be an ordinary injection job")
	script := paste.Cmd[2]
	assert.Contains(t, script, "first waiting")
	assert.Contains(t, script, "second waiting")
	assert.Eq(t, 1, strings.Count(script, "tmux paste-buffer -p"))

	assert.Len(t, heldMails(t, s, "sid-flush"), 0)
	_, err = s.DeliverHeld(context.Background(), "sid-flush", "alice", "alice")
	assert.True(t, err != nil)
	assert.Eq(t, "", DeliverReason(err))
}

// TestSetInjectModeRejectsUnknownMode: the mode vocabulary is closed.
func TestSetInjectModeRejectsUnknownMode(t *testing.T) {
	s := newSvc(t)
	tmuxSession(t, s, "sid-mode")
	_, err := s.SetInjectMode("sid-mode", "sometimes", "alice")
	assert.Err(t, err)
	_, err = s.SetInjectMode("sid-nobody", jobstore.InjectModeHold, "alice")
	assert.Eq(t, ErrUnknownSession, err)
}
