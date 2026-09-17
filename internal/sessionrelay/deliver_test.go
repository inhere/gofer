package sessionrelay

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// fakeInjector records the internal exec jobs the relay dispatched (design
// §9.1 A) and answers with a scripted result.
type fakeInjector struct {
	reqs []InjectRequest
	res  InjectResult
	err  error
}

func (f *fakeInjector) InjectSession(_ context.Context, req InjectRequest) (InjectResult, error) {
	f.reqs = append(f.reqs, req)
	return f.res, f.err
}

// tmuxSession registers a session that path A can reach: a tmux pane plus the
// runner that owns it.
func tmuxSession(t *testing.T, s *Service, sid string) jobstore.AgentSession {
	t.Helper()
	a, err := s.Register(RegisterInput{
		SessionID: sid, Agent: "claude", ProjectKey: "self", Runner: "w-claude",
		Cwd: "/w/repo", TmuxPane: "%3", Event: EventSessionStart,
	})
	assert.NoErr(t, err)
	return a
}

// TestDeliverAnswersOpenTurnFirst pins the routing order: while a turn is OPEN
// the reply is an ANSWER (phase 1), never a terminal injection — and the injector
// is not even consulted.
func TestDeliverAnswersOpenTurnFirst(t *testing.T) {
	s := newSvc(t)
	inj := &fakeInjector{}
	s.SetInjector(inj)
	tmuxSession(t, s, "sid-turn-first")
	_, err := s.SetRelayMode("sid-turn-first", jobstore.RelayModeOn)
	assert.NoErr(t, err)
	turn, err := s.OpenTurn("sid-turn-first", "which plan?", 60)
	assert.NoErr(t, err)

	res, err := s.Deliver(context.Background(), "sid-turn-first", "plan B", "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathTurn, res.Path)
	assert.Eq(t, turn.ID, res.DecisionID)
	assert.Eq(t, "", res.JobID)
	assert.Len(t, inj.reqs, 0)

	d, ok, err := s.store.GetDecision(turn.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.DecisionAnswered, d.State)
	assert.Eq(t, "plan B", d.Answer)
	assert.Eq(t, "alice", d.AnsweredBy)
	a, ok, err := s.store.GetAgentSession("sid-turn-first")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.SessionRunning, a.State)
}

// TestDeliverTmuxDispatchesInjectJob pins path A's dispatch contract: one
// internal exec job on the session's own runner, in its project, whose argv is a
// single `sh -c` script that checks the pane and then types the prefixed reply —
// plus the success bookkeeping (state running, audit row naming the job).
func TestDeliverTmuxDispatchesInjectJob(t *testing.T) {
	s := newSvc(t)
	inj := &fakeInjector{res: InjectResult{JobID: "job-inject-1", ExitCode: 0}}
	s.SetInjector(inj)
	tmuxSession(t, s, "sid-tmux-0001")
	_, err := s.Heartbeat("sid-tmux-0001", HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)

	res, err := s.Deliver(context.Background(), "sid-tmux-0001", "carry on", "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathTmux, res.Path)
	assert.Eq(t, "job-inject-1", res.JobID)

	assert.Len(t, inj.reqs, 1)
	req := inj.reqs[0]
	assert.Eq(t, "self", req.ProjectKey)
	assert.Eq(t, "w-claude", req.Runner)
	assert.Eq(t, ".", req.Cwd)
	assert.Eq(t, "relay inject → sid-tmux", req.Title)
	assert.Eq(t, "relay-inject", strings.Join(req.Tags, ","))
	assert.Eq(t, 30, req.TimeoutSec)
	assert.Len(t, req.Cmd, 3)
	assert.Eq(t, "sh", req.Cmd[0])
	assert.Eq(t, "-c", req.Cmd[1])
	script := req.Cmd[2]
	assert.Contains(t, script, "tmux display -p -t '%3' '#{pane_current_command}'")
	assert.Contains(t, script, "echo pane_missing; exit 3")
	assert.Contains(t, script, "send-keys -t '%3' -l -- '[gofer web 回复] carry on'")
	assert.Contains(t, script, "send-keys -t '%3' Enter")

	a, ok, err := s.store.GetAgentSession("sid-tmux-0001")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.SessionRunning, a.State)

	d := latestRow(t, s, "sid-tmux-0001")
	assert.Eq(t, res.DecisionID, d.ID)
	assert.Eq(t, jobstore.DecisionAnswered, d.State)
	assert.Eq(t, "carry on", d.Answer)
	assert.Eq(t, "alice", d.AnsweredBy)
	assert.Eq(t, jobstore.DecisionKindRelay, d.Kind)
	assert.Contains(t, d.Detail, `"path":"tmux"`)
	assert.Contains(t, d.Detail, `"job_id":"job-inject-1"`)
}

// TestDeliverTmuxShellEscaping pins the shell safety of the injection script: the
// reply is a single-quoted literal, so quotes are escaped and `$` / backticks
// stay inert, and a multi-line reply is sent line by line (each line gets its own
// `-l` literal plus the Enter that submits it).
func TestDeliverTmuxShellEscaping(t *testing.T) {
	s := newSvc(t)
	inj := &fakeInjector{res: InjectResult{JobID: "job-x", ExitCode: 0}}
	s.SetInjector(inj)
	tmuxSession(t, s, "sid-escape")

	text := "it's $(x) `y`\nsecond 'line'"
	_, err := s.Deliver(context.Background(), "sid-escape", text, "alice", false)
	assert.NoErr(t, err)

	script := inj.reqs[0].Cmd[2]
	// ' → '\'' ; $ and backticks are inside the single quotes and stay literal.
	assert.Contains(t, script, `-l -- '[gofer web 回复] it'\''s $(x) `+"`y`"+`'`)
	assert.Contains(t, script, `-l -- 'second '\''line'\'''`)
	// Line by line: two literals, two Enters, and no raw newline inside a literal.
	assert.Eq(t, 2, strings.Count(script, " -l -- "))
	assert.Eq(t, 2, strings.Count(script, "send-keys -t '%3' Enter"))
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, " -l -- ") {
			assert.True(t, strings.HasSuffix(line, "' || exit 1"), line)
		}
	}
}

// TestDeliverRefusesNoRunnerNoTmuxEnded pins the three "cannot even try" reasons
// (design §9.1 v0.5 failure codes): an unregistered execution machine, a session
// with no tmux pane, and an ended session. None of them dispatches a job.
func TestDeliverRefusesNoRunnerNoTmuxEnded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runner string
		pane   string
		ended  bool
		want   string
	}{
		{"no_runner", "", "%1", false, ReasonNoRunner},
		{"no_tmux", "w-1", "", false, ReasonNoTmux},
		{"ended", "w-1", "%1", true, ReasonEnded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSvc(t)
			inj := &fakeInjector{}
			s.SetInjector(inj)
			_, err := s.Register(RegisterInput{
				SessionID: "sid-" + tc.name, Agent: "claude", Runner: tc.runner,
				Cwd: "/w", TmuxPane: tc.pane, Event: EventSessionStart,
			})
			assert.NoErr(t, err)
			if tc.ended {
				_, err = s.Heartbeat("sid-"+tc.name, HeartbeatInput{Event: EventSessionEnd})
				assert.NoErr(t, err)
			}

			_, err = s.Deliver(context.Background(), "sid-"+tc.name, "hello", "alice", false)
			assert.True(t, errors.Is(err, ErrUndeliverable))
			assert.Eq(t, tc.want, DeliverReason(err))
			assert.Len(t, inj.reqs, 0)
		})
	}
}

// TestDeliverTmuxInjectFailure pins how a failed injection is reported: the pane
// check's exit codes become the reason code (pane gone / agent not in the
// foreground), anything else is a runner error — and the session is left exactly
// as it was, with no audit row claiming a delivery.
func TestDeliverTmuxInjectFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  InjectResult
		err  error
		want string
	}{
		{"pane_missing", InjectResult{JobID: "j1", ExitCode: 3, Output: "pane_missing\n"}, nil, "inject_failed:pane_missing"},
		{"pane_busy", InjectResult{JobID: "j2", ExitCode: 4, Output: "pane_busy:vim\n"}, nil, "inject_failed:pane_busy:vim"},
		{"runner_error", InjectResult{}, errors.New("runner \"w-1\" is not available"), "inject_failed:runner_error"},
		{"silent_failure", InjectResult{JobID: "j3", ExitCode: -1, Output: "exec agent not allowed"}, nil, "inject_failed:runner_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSvc(t)
			s.SetInjector(&fakeInjector{res: tc.res, err: tc.err})
			tmuxSession(t, s, "sid-fail")
			_, err := s.Heartbeat("sid-fail", HeartbeatInput{Event: EventStop})
			assert.NoErr(t, err)

			_, err = s.Deliver(context.Background(), "sid-fail", "hello", "alice", false)
			assert.True(t, errors.Is(err, ErrUndeliverable))
			assert.Eq(t, tc.want, DeliverReason(err))

			a, ok, err := s.store.GetAgentSession("sid-fail")
			assert.NoErr(t, err)
			assert.True(t, ok)
			assert.Eq(t, jobstore.SessionIdle, a.State)
			turns, err := s.store.ListSessionDecisions("sid-fail", "", 10)
			assert.NoErr(t, err)
			assert.Len(t, turns, 0)
		})
	}
}

// TestDeliverTooLong pins the 8KB ceiling on an injected reply: one byte over is
// refused (no job), the exact ceiling goes through.
func TestDeliverTooLong(t *testing.T) {
	s := newSvc(t)
	inj := &fakeInjector{res: InjectResult{JobID: "job-max", ExitCode: 0}}
	s.SetInjector(inj)
	tmuxSession(t, s, "sid-long")

	_, err := s.Deliver(context.Background(), "sid-long", strings.Repeat("a", MaxDeliverText+1), "alice", false)
	assert.True(t, errors.Is(err, ErrInvalidInput))
	assert.Len(t, inj.reqs, 0)

	res, err := s.Deliver(context.Background(), "sid-long", strings.Repeat("a", MaxDeliverText), "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathTmux, res.Path)
	assert.Len(t, inj.reqs, 1)
	assert.Contains(t, inj.reqs[0].Cmd[2], "[gofer web 回复] "+strings.Repeat("a", MaxDeliverText))
}

// TestDeliverInjectCommandsWhitelist pins the pane whitelist plumbing: the
// configured commands replace the built-in list, and an unusable entry is dropped
// rather than mangled — an operator may narrow who gets typed into, never smuggle
// script into the injection command line.
func TestDeliverInjectCommandsWhitelist(t *testing.T) {
	s := newSvc(t)
	inj := &fakeInjector{res: InjectResult{JobID: "job-w", ExitCode: 0}}
	s.SetInjector(inj)
	s.SetInjectCommands([]string{"claude", "my agent; rm -rf /", "$(x)"})
	tmuxSession(t, s, "sid-whitelist")

	_, err := s.Deliver(context.Background(), "sid-whitelist", "hello", "alice", false)
	assert.NoErr(t, err)

	script := inj.reqs[0].Cmd[2]
	assert.Contains(t, script, "claude) ;;")
	assert.True(t, !strings.Contains(script, "rm -rf"), script)
	assert.True(t, !strings.Contains(script, "$(x)"), script)
}

// latestRow returns a session's newest plan_decisions row: the audit row of an
// injection has no OPEN turn behind it, so it is simply the most recent one.
func latestRow(t *testing.T, s *Service, sid string) jobstore.PlanDecision {
	t.Helper()
	rows, err := s.store.ListSessionDecisions(sid, "", 1)
	assert.NoErr(t, err)
	if len(rows) == 0 {
		t.Fatalf("no audit row for session %s", sid)
	}
	return *rows[0]
}
