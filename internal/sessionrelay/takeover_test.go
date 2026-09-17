package sessionrelay

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// planCall records one PlanTakeover question, so a test can pin WHAT the relay
// asked the host to resolve (agent + project + runner + session).
type planCall struct {
	agent, project, runner, sessionID string
}

// fakeTakeoverer stands in for the host behind path B (design §9.1 B): it records
// the plan questions and the dispatched takeover jobs, and answers with a scripted
// result. planFn overrides the default plan (which is the built-in claude shape)
// for the precondition cases.
type fakeTakeoverer struct {
	planFn    func() TakeoverPlan
	calls     []planCall
	reqs      []TakeoverRequest
	res       TakeoverResult
	err       error
	cancels   []string
	cancelErr error
}

func (f *fakeTakeoverer) PlanTakeover(agentKey, projectKey, runner, sessionID string) TakeoverPlan {
	f.calls = append(f.calls, planCall{agentKey, projectKey, runner, sessionID})
	if f.planFn != nil {
		return f.planFn()
	}
	return TakeoverPlan{
		Argv:             []string{agentKey, "--resume", sessionID},
		AllowInteractive: true,
		ExecRoot:         defaultTakeoverRoot,
	}
}

func (f *fakeTakeoverer) TakeoverSession(_ context.Context, req TakeoverRequest) (TakeoverResult, error) {
	f.reqs = append(f.reqs, req)
	return f.res, f.err
}

func (f *fakeTakeoverer) CancelTakeover(_ context.Context, jobID string) error {
	f.cancels = append(f.cancels, jobID)
	return f.cancelErr
}

// defaultTakeoverRoot is the project root the fake host reports for the session
// cwd below ("sub" is its relative form).
const defaultTakeoverRoot = "/work/repo"

// takeoverSession registers a session path B can continue: a cli-agent session on
// a worker runner with a cwd under the project root and NO tmux pane (the case
// that reaches B in the first place).
func takeoverSession(t *testing.T, s *Service, sid string) jobstore.AgentSession {
	t.Helper()
	a, err := s.Register(RegisterInput{
		SessionID: sid, Agent: "claude", ProjectKey: "self", Runner: "w-claude",
		Cwd: defaultTakeoverRoot + "/sub", Event: EventSessionStart,
	})
	assert.NoErr(t, err)
	// A Stop with relay off leaves it idle at its prompt — exactly the state web
	// input finds it in (§9.1: no OPEN turn).
	_, err = s.Heartbeat(sid, HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)
	return a
}

// TestDeliverTakeoverSubmitsInteractiveResume pins path B's dispatch contract: one
// interactive pty job that continues the SAME CLI session (`--resume <sid>`), in
// the session's own project/runner and project-relative cwd, primed with the
// prefixed reply — plus the bookkeeping the web and the original terminal depend
// on (state handed_off, handed_off_job_id, the audit row naming the job).
func TestDeliverTakeoverSubmitsInteractiveResume(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-takeover-1"}}
	s.SetTakeoverer(to)
	nf := &fakeNotifier{}
	s.SetNotifier(nf)
	takeoverSession(t, s, "sid-takeover-01")

	res, err := s.Deliver(context.Background(), "sid-takeover-01", "carry on", "alice", true)
	assert.NoErr(t, err)
	assert.Eq(t, PathTakeover, res.Path)
	assert.Eq(t, "job-takeover-1", res.JobID)

	// The relay asked the host to plan with the SESSION's identity, not its own.
	assert.Len(t, to.calls, 1)
	assert.Eq(t, "claude", to.calls[0].agent)
	assert.Eq(t, "self", to.calls[0].project)
	assert.Eq(t, "w-claude", to.calls[0].runner)
	assert.Eq(t, "sid-takeover-01", to.calls[0].sessionID)

	assert.Len(t, to.reqs, 1)
	req := to.reqs[0]
	assert.Eq(t, "claude --resume sid-takeover-01", strings.Join(req.Cmd, " "))
	assert.Eq(t, "self", req.ProjectKey)
	assert.Eq(t, "w-claude", req.Runner)
	// The cwd is project-RELATIVE (the job service SafeJoins it on the runner),
	// which is what makes the resumed session open in the same directory.
	assert.Eq(t, "sub", req.Cwd)
	assert.Eq(t, "relay takeover → sid-take", req.Title)
	assert.Eq(t, "relay-takeover", strings.Join(req.Tags, ","))
	assert.Eq(t, 120, req.Cols)
	assert.Eq(t, 40, req.Rows)
	assert.Eq(t, 3600, req.TimeoutSec)
	assert.Eq(t, "sid-takeover-01", req.SessionID)
	// The exec carrier is authorised as the SOURCE agent (same exemption as a job
	// resume), so the takeover does not need the broad allow_exec.
	assert.Eq(t, "claude", req.ResumeSourceAgent)
	assert.Eq(t, "alice", req.CallerID)
	// The reply is the pty's FIRST input, with the injection prefix the hook and
	// the model key on, plus the Enter that submits it.
	assert.Eq(t, "[gofer web 回复] carry on\r", req.InitialInput)

	a, ok, err := s.store.GetAgentSession("sid-takeover-01")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.SessionHandedOff, a.State)
	assert.Eq(t, "job-takeover-1", a.HandedOffJobID)
	assert.True(t, a.HandedOffAt > 0)

	d := latestRow(t, s, "sid-takeover-01")
	assert.Eq(t, res.DecisionID, d.ID)
	assert.Eq(t, jobstore.DecisionAnswered, d.State)
	assert.Eq(t, "carry on", d.Answer)
	assert.Eq(t, "alice", d.AnsweredBy)
	assert.Eq(t, jobstore.DecisionKindRelay, d.Kind)
	assert.Contains(t, d.Detail, `"path":"takeover"`)
	assert.Contains(t, d.Detail, `"job_id":"job-takeover-1"`)

	// The human's phone is told the session moved to a job — the terminal they left
	// is no longer where the conversation continues.
	assert.Eq(t, 1, len(nf.handedOff))
	assert.Contains(t, nf.handedOff[0], "sid-takeover-01:job-takeover-1")
}

// TestDeliverTakeoverRequiresAllowTakeover pins the opt-in: path B starts a NEW
// process against the session, so it never happens on a plain deliver — the
// session stops at the same 409 no_tmux path A reports, and only an explicit
// allow_takeover continues. The same switch is what turns a DEAD tmux pane
// (pane_missing, path A's other "the session cannot be typed into") into a
// takeover.
func TestDeliverTakeoverRequiresAllowTakeover(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-takeover-2"}}
	s.SetTakeoverer(to)
	s.SetInjector(&fakeInjector{})
	takeoverSession(t, s, "sid-noperm")

	// Without the opt-in the reply stops where path A stops: no pane to type into.
	// A takeover starts a SECOND process against the session, so it never happens
	// implicitly (the web asks first).
	_, err := s.Deliver(context.Background(), "sid-noperm", "hello", "alice", false)
	assert.True(t, errors.Is(err, ErrUndeliverable))
	assert.Eq(t, ReasonNoTmux, DeliverReason(err))
	assert.Len(t, to.reqs, 0)

	res, err := s.Deliver(context.Background(), "sid-noperm", "hello", "alice", true)
	assert.NoErr(t, err)
	assert.Eq(t, PathTakeover, res.Path)

	// A registered pane that is GONE by the time the reply arrives: path A fails
	// with pane_missing, and the takeover takes over from there.
	paneSession(t, s, "sid-stale-pane", "%9")
	s.SetInjector(&fakeInjector{res: InjectResult{JobID: "job-gone", ExitCode: 3, Output: "pane_missing\n"}})
	res, err = s.Deliver(context.Background(), "sid-stale-pane", "hi", "alice", true)
	assert.NoErr(t, err)
	assert.Eq(t, PathTakeover, res.Path)

	// A pane held by something else is NOT a takeover trigger: the human is using
	// that terminal, and typing into it was correctly refused.
	paneSession(t, s, "sid-busy-pane", "%10")
	s.SetInjector(&fakeInjector{res: InjectResult{JobID: "job-busy", ExitCode: 4, Output: "pane_busy:vim\n"}})
	_, err = s.Deliver(context.Background(), "sid-busy-pane", "hi", "alice", true)
	assert.Eq(t, "inject_failed:pane_busy:vim", DeliverReason(err))
}

// paneSession registers a session that path A can reach (a tmux pane) at the
// takeover root, so a test can drive path A's failures and assert what happens next.
func paneSession(t *testing.T, s *Service, sid, pane string) {
	t.Helper()
	_, err := s.Register(RegisterInput{
		SessionID: sid, Agent: "claude", ProjectKey: "self", Runner: "w-claude",
		Cwd: defaultTakeoverRoot + "/sub", TmuxPane: pane, Event: EventSessionStart,
	})
	assert.NoErr(t, err)
	_, err = s.Heartbeat(sid, HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)
}

// TestDeliverTakeoverPreconditions pins the four reasons path B refuses BEFORE
// dispatching anything (design §9.1 v0.5 failure codes): the agent has no
// interactive resume template, the project does not allow interactive jobs, the
// session's cwd cannot be expressed as a project-relative path, and the session
// is already taken over.
func TestDeliverTakeoverPreconditions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plan  TakeoverPlan
		state string
		want  string
	}{
		{
			name: "no_resume_template",
			plan: TakeoverPlan{AllowInteractive: true, ExecRoot: defaultTakeoverRoot},
			want: ReasonNoResumeTemplate,
		},
		{
			name: "interactive_not_allowed",
			plan: TakeoverPlan{Argv: []string{"claude", "--resume", "s"}, ExecRoot: defaultTakeoverRoot},
			want: ReasonInteractiveNotAllowed,
		},
		{
			name: "cwd_outside_project",
			plan: TakeoverPlan{Argv: []string{"claude", "--resume", "s"}, AllowInteractive: true, ExecRoot: "/elsewhere/repo"},
			want: ReasonCwdOutsideProject,
		},
		{
			name:  "handed_off",
			plan:  TakeoverPlan{Argv: []string{"claude", "--resume", "s"}, AllowInteractive: true, ExecRoot: defaultTakeoverRoot},
			state: jobstore.SessionHandedOff,
			want:  "handed_off:job-prev",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSvc(t)
			to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-should-not-run"}}
			to.planFn = func() TakeoverPlan { return tc.plan }
			s.SetTakeoverer(to)
			takeoverSession(t, s, "sid-pre")
			if tc.state != "" {
				_, err := s.store.SetSessionHandedOff("sid-pre", "job-prev")
				assert.NoErr(t, err)
			}

			_, err := s.Deliver(context.Background(), "sid-pre", "hello", "alice", true)
			assert.True(t, errors.Is(err, ErrUndeliverable))
			assert.Eq(t, tc.want, DeliverReason(err))
			assert.Len(t, to.reqs, 0)

			a, ok, err := s.store.GetAgentSession("sid-pre")
			assert.NoErr(t, err)
			assert.True(t, ok)
			if tc.state == "" {
				// A refused takeover leaves the session exactly as it was: nothing
				// claims it was handed off.
				assert.Eq(t, jobstore.SessionIdle, a.State)
				assert.Eq(t, "", a.HandedOffJobID)
			}
		})
	}
}

// TestOpenTurnRefusedWhenHandedOff pins the takeover's one-way door: the ORIGINAL
// terminal stops relaying — a Stop there must not open a turn (the hook then lets
// the agent stop normally), and no wait rule may arm it, because a second writer
// on the same CLI session diverges it (design §9.1 B).
func TestOpenTurnRefusedWhenHandedOff(t *testing.T) {
	s := newSvc(t)
	s.SetTakeoverer(&fakeTakeoverer{})
	takeoverSession(t, s, "sid-taken")
	_, err := s.SetRelayMode("sid-taken", jobstore.RelayModeOn)
	assert.NoErr(t, err)

	a, err := s.store.SetSessionHandedOff("sid-taken", "job-takeover-9")
	_ = a
	assert.NoErr(t, err)

	got, ok, err := s.store.GetAgentSession("sid-taken")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", s.WaitReason(got))

	_, err = s.OpenTurn("sid-taken", "still here?", 60)
	assert.True(t, errors.Is(err, ErrRelayOff))

	// The original terminal keeps beating (its agent stops, the human types, the CLI
	// quits): none of it may hand the session back — a live pty job still owns it.
	for _, ev := range []struct {
		event string
		state string
	}{{EventStop, ""}, {EventUserPromptSubmit, ""}, {EventNotification, jobstore.SessionIdle}, {EventSessionEnd, ""}} {
		_, err := s.Heartbeat("sid-taken", HeartbeatInput{Event: ev.event, State: ev.state})
		assert.NoErr(t, err)
		after, ok, err := s.store.GetAgentSession("sid-taken")
		assert.NoErr(t, err)
		assert.True(t, ok)
		assert.Eq(t, jobstore.SessionHandedOff, after.State)
		assert.Eq(t, "job-takeover-9", after.HandedOffJobID)
	}

	// Only the release ends it.
	_, err = s.ReleaseTakeover(context.Background(), "sid-taken")
	assert.NoErr(t, err)
	after, ok, err := s.store.GetAgentSession("sid-taken")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.SessionIdle, after.State)
}

// TestReleaseTakeover pins the way back: releasing cancels the takeover job FIRST
// (it still holds the session) and only then returns the session to idle with
// handed_off_* cleared, so the original terminal can relay again. Releasing a
// session that was never taken over is refused rather than silently ignored.
func TestReleaseTakeover(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-takeover-3"}}
	s.SetTakeoverer(to)
	takeoverSession(t, s, "sid-release")

	_, err := s.Deliver(context.Background(), "sid-release", "hello", "alice", true)
	assert.NoErr(t, err)

	a, err := s.ReleaseTakeover(context.Background(), "sid-release")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.SessionIdle, a.State)
	assert.Eq(t, "", a.HandedOffJobID)
	assert.Eq(t, int64(0), a.HandedOffAt)
	assert.Eq(t, "job-takeover-3", strings.Join(to.cancels, ","))

	// Idempotence is NOT assumed: a second release is a distinct, refused request
	// (the web button is shown only while the session is handed off).
	_, err = s.ReleaseTakeover(context.Background(), "sid-release")
	assert.True(t, errors.Is(err, ErrNotHandedOff))
	assert.Len(t, to.cancels, 1)

	_, err = s.ReleaseTakeover(context.Background(), "sid-unknown")
	assert.True(t, errors.Is(err, ErrUnknownSession))
}
