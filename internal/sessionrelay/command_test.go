package sessionrelay

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// scriptedInjector answers the deliver job (tag relay-deliver) with deliverExit and
// everything else (tmux injection) with tmuxExit, recording the order of the calls.
type scriptedInjector struct {
	deliverExit, tmuxExit int
	deliverOut            string
	reqs                  []InjectRequest
}

func (f *scriptedInjector) InjectSession(_ context.Context, req InjectRequest) (InjectResult, error) {
	f.reqs = append(f.reqs, req)
	for _, tg := range req.Tags {
		if tg == TagRelayDeliver {
			return InjectResult{JobID: "job-deliver", ExitCode: f.deliverExit, Output: f.deliverOut}, nil
		}
	}
	return InjectResult{JobID: "job-tmux", ExitCode: f.tmuxExit}, nil
}

func planFor(stdin bool) func(agentKey, sid, text string) CommandPlan {
	return func(agentKey, sid, text string) CommandPlan {
		if agentKey != "myagent" {
			return CommandPlan{}
		}
		p := CommandPlan{Argv: []string{"myagent", "send", "--session", sid}}
		if stdin {
			p.Stdin = text
		} else {
			p.Argv = append(p.Argv, "--text", text)
		}
		return p
	}
}

func cmdSession(t *testing.T, s *Service, sid, pane string) {
	t.Helper()
	_, err := s.Register(RegisterInput{SessionID: sid, Agent: "myagent", ProjectKey: "self", Runner: "w-x",
		Cwd: defaultTakeoverRoot + "/sub", TmuxPane: pane, Event: EventSessionStart})
	assert.NoErr(t, err)
	_, err = s.Heartbeat(sid, HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)
}

func TestDeliverCommandDelivered(t *testing.T) {
	for _, stdin := range []bool{false, true} {
		s := newSvc(t)
		inj := &scriptedInjector{}
		s.SetInjector(inj)
		s.SetDeliverPlanner(planFor(stdin))
		cmdSession(t, s, "sid-cmd-ok", "%1")

		res, err := s.Deliver(context.Background(), "sid-cmd-ok", "hello", "alice", false)
		assert.NoErr(t, err)
		assert.Eq(t, PathCommand, res.Path)
		assert.Len(t, inj.reqs, 1) // tmux never touched
		r := inj.reqs[0]
		assert.Eq(t, "myagent", r.SourceAgent)
		assert.Eq(t, []string{TagRelayDeliver}, r.Tags)
		if stdin {
			assert.Eq(t, InjectPrefix+"hello", r.Stdin)
			assert.False(t, strings.Contains(strings.Join(r.Cmd, " "), "hello"))
		} else {
			assert.Eq(t, "", r.Stdin)
			assert.Eq(t, InjectPrefix+"hello", r.Cmd[len(r.Cmd)-1])
		}
		a, _, _ := s.store.GetAgentSession("sid-cmd-ok")
		assert.Eq(t, jobstore.SessionRunning, a.State)
		assert.Contains(t, latestRow(t, s, "sid-cmd-ok").Detail, `"path":"command"`)
	}
}

// Exit 3 = no live process: delivery falls through to tmux; exit 3 and no pane/tmux
// then reaches the takeover (allowed even though the heartbeat is fresh).
func TestDeliverCommandNotRunningFallsThrough(t *testing.T) {
	s := newSvc(t)
	inj := &scriptedInjector{deliverExit: 3}
	s.SetInjector(inj)
	s.SetDeliverPlanner(planFor(true))
	s.SetTakeoverAliveSec(120)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-to"}}
	s.SetTakeoverer(to)

	cmdSession(t, s, "sid-nr-tmux", "%1")
	res, err := s.Deliver(context.Background(), "sid-nr-tmux", "hi", "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathTmux, res.Path)
	assert.Len(t, inj.reqs, 2) // deliver first, then tmux

	cmdSession(t, s, "sid-nr-takeover", "")
	res, err = s.Deliver(context.Background(), "sid-nr-takeover", "hi", "alice", true)
	assert.NoErr(t, err)
	assert.Eq(t, PathTakeover, res.Path) // exit 3 is the evidence the old process is gone
}

// Any other exit code stops the ladder: tmux is NOT tried (a failed command may still
// have delivered; a second path could type the message twice).
func TestDeliverCommandFailureStops(t *testing.T) {
	s := newSvc(t)
	inj := &scriptedInjector{deliverExit: 1, deliverOut: "socket refused"}
	s.SetInjector(inj)
	s.SetDeliverPlanner(planFor(false))
	cmdSession(t, s, "sid-cmd-fail", "%1")

	_, err := s.Deliver(context.Background(), "sid-cmd-fail", "hi", "alice", true)
	assert.Err(t, err)
	assert.Eq(t, DeliverFailedPrefix+"socket refused", DeliverReason(err))
	assert.Len(t, inj.reqs, 1)
	a, _, _ := s.store.GetAgentSession("sid-cmd-fail")
	assert.True(t, a.State != jobstore.SessionRunning)
}

// deliver_offline_match: a non-zero exit whose output matches the plan's regexp counts as
// exit 3 (ladder continues); a non-matching one stays deliver_failed; exit 0 delivers.
func TestDeliverCommandOfflineMatch(t *testing.T) {
	plan := func(re string) func(string, string, string) CommandPlan {
		return func(agentKey, sid, text string) CommandPlan {
			p := planFor(true)(agentKey, sid, text)
			p.OfflineMatch = regexp.MustCompile(re)
			return p
		}
	}
	// hit -> falls through to tmux
	s := newSvc(t)
	inj := &scriptedInjector{deliverExit: 1, deliverOut: "Error: no rollout found for thread id x"}
	s.SetInjector(inj)
	s.SetDeliverPlanner(plan("no rollout found"))
	cmdSession(t, s, "sid-om-hit", "%1")
	res, err := s.Deliver(context.Background(), "sid-om-hit", "hi", "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathTmux, res.Path)
	assert.Len(t, inj.reqs, 2)

	// miss -> deliver_failed, ladder stops
	s = newSvc(t)
	inj = &scriptedInjector{deliverExit: 1, deliverOut: "socket refused"}
	s.SetInjector(inj)
	s.SetDeliverPlanner(plan("no rollout found"))
	cmdSession(t, s, "sid-om-miss", "%1")
	_, err = s.Deliver(context.Background(), "sid-om-miss", "hi", "alice", true)
	assert.Err(t, err)
	assert.Eq(t, DeliverFailedPrefix+"socket refused", DeliverReason(err))
	assert.Len(t, inj.reqs, 1)

	// exit 0 -> delivered even though the output would match
	s = newSvc(t)
	inj = &scriptedInjector{deliverExit: 0, deliverOut: "no rollout found"}
	s.SetInjector(inj)
	s.SetDeliverPlanner(plan("no rollout found"))
	cmdSession(t, s, "sid-om-ok", "%1")
	res, err = s.Deliver(context.Background(), "sid-om-ok", "hi", "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathCommand, res.Path)
}

// An agent with no deliver_command keeps the old ladder untouched.
func TestDeliverCommandAbsentUsesTmux(t *testing.T) {
	s := newSvc(t)
	inj := &scriptedInjector{}
	s.SetInjector(inj)
	s.SetDeliverPlanner(planFor(false))
	tmuxSession(t, s, "sid-plain") // agent claude
	res, err := s.Deliver(context.Background(), "sid-plain", "hi", "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathTmux, res.Path)
}

// An OPEN turn still wins over the command.
func TestDeliverCommandOpenTurnFirst(t *testing.T) {
	s := newSvc(t)
	inj := &scriptedInjector{}
	s.SetInjector(inj)
	s.SetDeliverPlanner(planFor(false))
	cmdSession(t, s, "sid-cmd-turn", "%1")
	_, err := s.SetRelayMode("sid-cmd-turn", jobstore.RelayModeOn)
	assert.NoErr(t, err)
	_, err = s.OpenTurn("sid-cmd-turn", "q?", 60)
	assert.NoErr(t, err)
	res, err := s.Deliver(context.Background(), "sid-cmd-turn", "a", "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathTurn, res.Path)
	assert.Len(t, inj.reqs, 0)
}

// The heartbeat guard: an agent WITHOUT a deliver_command is refused a takeover while
// its last heartbeat is inside takeover_alive_sec, and allowed once it is older (or the
// session is offline/ended); 0 turns the guard off.
func TestTakeoverRefusedWhileHeartbeatFresh(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-to"}}
	s.SetTakeoverer(to)
	s.SetTakeoverAliveSec(120)
	takeoverSession(t, s, "sid-alive")

	_, err := s.Deliver(context.Background(), "sid-alive", "hi", "alice", true)
	assert.Err(t, err)
	assert.Eq(t, ReasonSessionAlive, DeliverReason(err))
	assert.Len(t, to.reqs, 0)
	_, err = s.Resume(context.Background(), "sid-alive", "", "alice")
	assert.Eq(t, ReasonSessionAlive, DeliverReason(err))

	real := time.Now
	s.nowFn = func() time.Time { return real().Add(200 * time.Second) }
	res, err := s.Deliver(context.Background(), "sid-alive", "hi", "alice", true)
	assert.NoErr(t, err)
	assert.Eq(t, PathTakeover, res.Path)

	s.nowFn = real
	s.SetTakeoverAliveSec(0)
	takeoverSession(t, s, "sid-alive-off")
	res, err = s.Deliver(context.Background(), "sid-alive-off", "hi", "alice", true)
	assert.NoErr(t, err)
	assert.Eq(t, PathTakeover, res.Path)
}

// The web's plain send (SendMessage) reaches a session with no Claude SendMessage
// address through the deliver ladder: the agent's command first; a failure keeps its
// reason code in the message error so the web can offer the takeover.
func TestSendMessageFallsBackToDeliverLadder(t *testing.T) {
	s := newSvc(t)
	inj := &scriptedInjector{}
	s.SetInjector(inj)
	s.SetDeliverPlanner(planFor(true))
	cmdSession(t, s, "sid-msg-cmd", "")
	m, err := s.SendMessage(context.Background(), "sid-msg-cmd", "hello", "alice")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.SessionMessageDelivered, m.Status)
	assert.Eq(t, PathCommand, m.Channel)

	// No command, no pane: failed, with the reason code first.
	_, err = s.Register(RegisterInput{SessionID: "sid-msg-none", Agent: "other", ProjectKey: "self", Runner: "w-x", Event: EventSessionStart})
	assert.NoErr(t, err)
	m, err = s.SendMessage(context.Background(), "sid-msg-none", "hello", "alice")
	assert.Err(t, err)
	assert.Eq(t, jobstore.SessionMessageFailed, m.Status)
	assert.True(t, strings.HasPrefix(m.Error, ReasonNoTmux+":"), m.Error)
}
