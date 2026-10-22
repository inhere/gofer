package sessionrelay

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
)

// The mailbox's raw paths (plan M5): DeliverLetter and Wake carry text verbatim and leave NO
// web trace — no prefix, no turn answered, no outbox row, no audit decision, no state change.

// rawMessenger records the one-shot messenger prompts and answers 已发送.
type rawMessenger struct{ prompts []string }

func (m *rawMessenger) SubmitMessenger(_, _, _ string, command []string, _, _ string) (string, error) {
	m.prompts = append(m.prompts, strings.Join(command, " "))
	return "job-msgr", nil
}

func (m *rawMessenger) MessengerJob(string) (bool, string, int, string, error) {
	return true, "done", 0, "已发送", nil
}

func assertNoWebTrace(t *testing.T, s *Service, sid, wantState string) {
	t.Helper()
	out, err := s.store.ReadSessionOutbox(sid)
	assert.NoErr(t, err)
	assert.Len(t, out, 0)
	rows, err := s.store.ListSessionDecisions(sid, "", 5, "")
	assert.NoErr(t, err)
	assert.Len(t, rows.Decisions, 0)
	a, _, _ := s.store.GetAgentSession(sid)
	assert.Eq(t, wantState, a.State)
}

func TestWakeCommandPathVerbatimNoWebTrace(t *testing.T) {
	s := newSvc(t)
	inj := &scriptedInjector{}
	s.SetInjector(inj)
	s.SetDeliverPlanner(planFor(true))
	cmdSession(t, s, "sid-wake-cmd", "%1")
	before, _, _ := s.store.GetAgentSession("sid-wake-cmd")

	res, err := s.Wake(context.Background(), "sid-wake-cmd", "<gofer-mail-wake count=\"2\"/>", "gofer-mail", "gofer-mail")
	assert.NoErr(t, err)
	assert.Eq(t, PathCommand, res.Path)
	assert.Len(t, inj.reqs, 1)
	assert.Eq(t, "<gofer-mail-wake count=\"2\"/>", inj.reqs[0].Stdin) // no person's web prefix
	assertNoWebTrace(t, s, "sid-wake-cmd", before.State)
}

func TestWakeTmuxPathNoPrefixNoWebTrace(t *testing.T) {
	s := newSvc(t)
	inj := &scriptedInjector{}
	s.SetInjector(inj)
	tmuxSession(t, s, "sid-wake-tmux")
	_, err := s.Heartbeat("sid-wake-tmux", HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)
	before, _, _ := s.store.GetAgentSession("sid-wake-tmux")

	res, err := s.Wake(context.Background(), "sid-wake-tmux", "WAKE-TEXT", "gofer-mail", "gofer-mail")
	assert.NoErr(t, err)
	assert.Eq(t, PathTmux, res.Path)
	assert.Len(t, inj.reqs, 1)
	script := strings.Join(inj.reqs[0].Cmd, " ")
	assert.Contains(t, script, "WAKE-TEXT")
	assert.False(t, strings.Contains(script, msgprefix.WebReplyOpen))
	assertNoWebTrace(t, s, "sid-wake-tmux", before.State)
}

func TestWakeMessengerPathHasNoWebPrefix(t *testing.T) {
	s := newSvc(t)
	s.ConfigureMessaging(true, "claude", 2*time.Second, 0)
	m := &rawMessenger{}
	s.SetMessenger(m)
	_, err := s.Register(RegisterInput{SessionID: "sid-wake-msgr", Agent: "claude", ProjectKey: "self",
		Runner: "w-x", Cwd: "/w", PeerName: "peer-1", PeerMessaging: true, Event: EventSessionStart})
	assert.NoErr(t, err)
	_, err = s.Heartbeat("sid-wake-msgr", HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)
	before, _, _ := s.store.GetAgentSession("sid-wake-msgr")

	res, err := s.Wake(context.Background(), "sid-wake-msgr", "WAKE-TEXT", "gofer-mail", "gofer-mail")
	assert.NoErr(t, err)
	assert.Eq(t, PathMessenger, res.Path)
	assert.Len(t, m.prompts, 1)
	assert.Contains(t, m.prompts[0], "WAKE-TEXT")
	assert.False(t, strings.Contains(m.prompts[0], WebMessagePrefix))
	assertNoWebTrace(t, s, "sid-wake-msgr", before.State)
}

func TestWakeNeverAnswersAnOpenTurn(t *testing.T) {
	s := newSvc(t)
	inj := &scriptedInjector{}
	s.SetInjector(inj)
	tmuxSession(t, s, "sid-wake-turn")
	_, err := s.SetRelayMode("sid-wake-turn", jobstore.RelayModeOn)
	assert.NoErr(t, err)
	turn, err := s.OpenTurn("sid-wake-turn", "which plan?", 60)
	assert.NoErr(t, err)

	_, err = s.Wake(context.Background(), "sid-wake-turn", "WAKE-TEXT", "gofer-mail", "gofer-mail")
	assert.NoErr(t, err)
	d, ok, err := s.store.GetDecision(turn.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", d.Answer) // the turn is still open: a wake is not an answer
}

func TestDeliverLetterExitCodes(t *testing.T) {
	s := newSvc(t)
	inj := &scriptedInjector{deliverExit: 3}
	s.SetInjector(inj)
	s.SetDeliverPlanner(planFor(true))
	cmdSession(t, s, "sid-letter", "%1")

	_, err := s.DeliverLetter(context.Background(), "sid-letter", "LETTER", "gofer-mail", "gofer-mail")
	assert.Eq(t, ReasonNotRunning, DeliverReason(err))
	assert.Len(t, inj.reqs, 1) // tmux is never tried for a letter

	inj.deliverExit = 0
	res, err := s.DeliverLetter(context.Background(), "sid-letter", "LETTER", "gofer-mail", "gofer-mail")
	assert.NoErr(t, err)
	assert.Eq(t, PathCommand, res.Path)
	assert.Eq(t, "LETTER", inj.reqs[1].Stdin)

	inj.deliverExit = 1
	inj.deliverOut = "boom"
	_, err = s.DeliverLetter(context.Background(), "sid-letter", "LETTER", "gofer-mail", "gofer-mail")
	assert.Eq(t, DeliverFailedPrefix+"boom", DeliverReason(err))
}

func TestDeliverLetterWithoutDeliverCommand(t *testing.T) {
	s := newSvc(t)
	s.SetInjector(&scriptedInjector{})
	s.SetDeliverPlanner(planFor(true))
	tmuxSession(t, s, "sid-letter-none") // agent "claude": no deliver_command
	_, err := s.DeliverLetter(context.Background(), "sid-letter-none", "LETTER", "gofer-mail", "gofer-mail")
	assert.Eq(t, ReasonNoDeliverCommand, DeliverReason(err))
}

// TestWakeMessengerPromptSaysTheNoticeIsTheMessage: a live test had the one-shot messenger
// refuse a wake ("no message body, only a pending-mail notice"); the prompt must state that
// the notice is the whole message and carry it verbatim.
func TestWakeMessengerPromptSaysTheNoticeIsTheMessage(t *testing.T) {
	p := wakeMessengerPrompt("peer-a", "<gofer-mail-wake count=\"1\"/>")
	for _, want := range []string{"peer-a", "完整消息", "不要因为没有正文而拒绝", "<gofer-mail-wake count=\"1\"/>"} {
		if !strings.Contains(p, want) {
			t.Fatalf("wake prompt misses %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, WebMessagePrefix) {
		t.Fatalf("a wake must not carry the web prefix:\n%s", p)
	}
}
