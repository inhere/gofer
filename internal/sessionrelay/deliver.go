package sessionrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// Delivery paths reported by DeliverResult.Path (design §9.1 选路).
const (
	// PathTurn is phase 1: the reply answers the session's OPEN turn and the
	// blocked Stop hook injects it back into the same terminal.
	PathTurn = "turn"
	// PathTmux is path A: the session is idle at its prompt, so the reply is
	// typed into its tmux pane by an internal exec job.
	PathTmux = "tmux"
	// PathTakeover is path B: the session had no usable tmux pane, so an
	// interactive pty job continues it (`--resume`) and the reply is primed into
	// that new terminal.
	PathTakeover = "takeover"
	// PathCommand is path C: the session's agent declares a deliver_command, which is
	// run on the session's runner and hands the text to the LIVE agent process (the
	// agent owns its own local inter-process channel; gofer only runs the command).
	PathCommand = "command"
)

// TagRelayDeliver labels path C's internal exec jobs (`gofer job ls --tag relay-deliver`).
const TagRelayDeliver = "relay-deliver"

// Path C outcomes besides success (the deliver_command's exit code contract).
const (
	// ReasonNotRunning: the deliver_command ran and reported (exit 3) that no process
	// of this session is running. Delivery continues down the ladder (tmux, takeover).
	ReasonNotRunning = "not_running"
	// DeliverFailedPrefix introduces a deliver_command failure (any other exit code,
	// or the job could not run); the remainder is the command's stderr / job error.
	// Delivery STOPS here — falling through to tmux could type the message twice.
	DeliverFailedPrefix = "deliver_failed:"
	// ReasonSessionAlive: a takeover was refused because the original process still
	// looks alive (a recent hook heartbeat, or — with a deliver_command — never
	// reported not_running), and two processes writing one session would fork it.
	ReasonSessionAlive = "session_alive"
	// deliverExitNotRunning is the deliver_command's "no such live process" exit code.
	deliverExitNotRunning = 3
)

// MaxDeliverText caps a reply delivered to a session (bytes). The tmux path
// types it into a live terminal, so it is capped well below the hook's own
// message limits (design §9.1 A: 8KB).
const MaxDeliverText = 8 * 1024

// TagRelayInject labels the internal exec jobs path A dispatches, so they can be
// told apart from human-submitted jobs in `gofer job ls --tag`.
const TagRelayInject = "relay-inject"

// TagRelayTakeover labels path B's interactive pty jobs (the same tagging idea as
// TagRelayInject: `gofer job ls --tag relay-takeover` lists every session a web
// user has taken over).
const TagRelayTakeover = "relay-takeover"

// InjectPrefix marks the text path A types into the terminal (design D7). The
// hook keys on it to recognise its own input: it MUST stay identical to
// hookrelay.ReplyPrefix, which a test in that package pins.
const InjectPrefix = "[gofer web 回复] "

// injectTimeoutSec is the deadline of the internal injection job: the script
// only talks to tmux, so anything slower is a broken runner, not a slow pane.
const injectTimeoutSec = 30

// Why a delivery could not even be attempted (design §9.1 v0.5 failure codes).
// An injection that was attempted but failed reports InjectFailedPrefix plus the
// pane check's own code instead.
const (
	// ReasonNoRunner: the session never registered an execution machine, so
	// there is nothing to dispatch to (a container session needs a gofer worker
	// in the container and GOFER_HOOK_RUNNER pointing at it).
	ReasonNoRunner = "no_runner"
	// ReasonNoTmux: no tmux pane was registered — the session cannot be typed
	// into (path B, `--resume` takeover, is the fallback for those).
	ReasonNoTmux = "no_tmux"
	// ReasonEnded: the CLI session is over.
	ReasonEnded = "ended"
	// InjectFailedPrefix introduces a failure of an ATTEMPTED injection; the
	// remainder is pane_missing, pane_busy:<cmd> or runner_error.
	InjectFailedPrefix = "inject_failed:"
	// ReasonNoResumeTemplate: path B's precondition — the session's agent is not a
	// cli-agent with an interactive resume template (`--resume <sid>`), so there is
	// no argv that would continue the conversation.
	ReasonNoResumeTemplate = "no_resume_template"
	// ReasonInteractiveNotAllowed: path B's precondition — the session's project
	// does not allow interactive (pty) jobs, so gofer will not start one.
	ReasonInteractiveNotAllowed = "interactive_not_allowed"
	// ReasonCwdOutsideProject: path B's precondition — the session's cwd cannot be
	// expressed relative to the project root the RUNNER sees, so the resumed
	// process would start in the wrong directory (a real possibility when POLICY
	// root mapping gives a worker a different absolute path than the hub's).
	ReasonCwdOutsideProject = "cwd_outside_project"
	// HandedOffPrefix introduces path B's "already taken over" reason: the session
	// is held by the job named after the prefix, and that job must be released
	// before anything else may speak to this session.
	HandedOffPrefix = "handed_off:"
)

// Path B's job shape (design §9.1 B): a terminal larger than the pty default (a
// resumed TUI is unusable in 80x24), and an hour of budget — the takeover is a
// working session, not a scripted injection. The job service clamps both to the
// project's/server's own ceilings.
const (
	takeoverCols    = 120
	takeoverRows    = 40
	takeoverTimeout = 3600
)

// pane-check failure codes the injection script prints (the reason vocabulary
// behind InjectFailedPrefix).
const (
	injectPaneMissing = "pane_missing"
	injectRunnerError = "runner_error"
)

// Exit codes of the injection script: 0 = typed, 3 = the pane is gone, 4 = the
// pane's foreground process is not an agent CLI. Anything else is a runner fault.
const (
	injectExitPaneMissing = 3
	injectExitPaneBusy    = 4
)

// defaultInjectCommands is the foreground-process whitelist of path A: the pane
// must be sitting in one of these CLIs (or the node/binary hosting one), else the
// reply is not typed into whatever else the human has running there.
var defaultInjectCommands = []string{"claude", "codex", "omp", "node", "gemini", "opencode"}

// DefaultInjectCommands returns a copy of the built-in whitelist.
func DefaultInjectCommands() []string { return append([]string(nil), defaultInjectCommands...) }

// ErrUndeliverable reports that a session cannot receive a message right now.
// The concrete reason is on UndeliverableError (see DeliverReason); this sentinel
// is what callers match with errors.Is.
var ErrUndeliverable = errors.New("sessionrelay: session cannot be delivered to")

// UndeliverableError carries the machine-readable reason code behind
// ErrUndeliverable (design §9.1 v0.5: no_runner | no_tmux | ended |
// inject_failed:<pane_missing|pane_busy:<cmd>|runner_error>).
type UndeliverableError struct {
	Reason string
	Err    error
}

func (e *UndeliverableError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("sessionrelay: undeliverable (%s): %v", e.Reason, e.Err)
	}
	return "sessionrelay: undeliverable (" + e.Reason + ")"
}

// Is makes errors.Is(err, ErrUndeliverable) hold for every reason.
func (e *UndeliverableError) Is(target error) bool { return target == ErrUndeliverable }

// Unwrap exposes the underlying cause (injector / configuration error).
func (e *UndeliverableError) Unwrap() error { return e.Err }

// DeliverReason extracts the reason code from a Deliver error ("" when err is
// not an undeliverable error). The entry layer maps the code to its HTTP status.
func DeliverReason(err error) string {
	var ue *UndeliverableError
	if errors.As(err, &ue) {
		return ue.Reason
	}
	return ""
}

func undeliverable(reason string, err error) error {
	return &UndeliverableError{Reason: reason, Err: err}
}

// InjectRequest is the internal exec job behind path A (design §9.1 A). It is a
// value type owned by this package so the host (job.Service) can be injected
// without the relay importing the job layer (G022).
type InjectRequest struct {
	// ProjectKey is the session's project: the job runs in that project's tree.
	ProjectKey string
	// Runner is the machine the session registered (agent_sessions.runner); the
	// host maps its own vocabulary (the server's label is its local runner).
	Runner string
	// Cwd is project-relative; the injection only needs tmux, so "." is fine.
	Cwd        string
	Title      string
	Tags       []string
	TimeoutSec int
	// Cmd is the argv to run: ONE shell invocation carrying the inject script.
	Cmd []string
	// Stdin is piped to Cmd's standard input (path C with deliver_stdin).
	Stdin string
	// SourceAgent, when set, runs Cmd as that agent's own command: admission is
	// judged on the agent (not the broad allow_exec) and its configured env applies
	// (path C — the same carrier identity a takeover / resume uses).
	SourceAgent string
}

// CommandPlan is what the host rendered from an agent's deliver_command for one
// message: the full argv (agent command first) and the stdin text. Empty Argv = the
// agent has no deliver_command. The relay cannot read agent config (G022), so the
// host renders the template.
type CommandPlan struct {
	Argv  []string
	Stdin string
	// OfflineMatch (nil = none): a non-zero exit whose output matches it counts as
	// exit 3 (not running). Judged here on the server from the finished job's result,
	// so it covers remote workers without any protocol change.
	OfflineMatch *regexp.Regexp
}

// InjectResult is the host's report of one injection job.
type InjectResult struct {
	JobID string
	// ExitCode is the script's exit code: 0 = typed into the pane. A job that
	// never ran (or did not finish) is reported non-zero by the host, so 0 always
	// means "the pane got the text".
	ExitCode int
	// Output is the stdout tail; the pane check prints its reason code there
	// (pane_missing / pane_busy:<cmd>).
	Output string
}

// Injector runs one internal injection job SYNCHRONOUSLY and reports its result.
// The host owns job submission, runner resolution and log access; the relay owns
// the policy (what to run, and what the exit code means).
type Injector interface {
	InjectSession(ctx context.Context, req InjectRequest) (InjectResult, error)
}

// InjectorFunc adapts a function to Injector.
type InjectorFunc func(ctx context.Context, req InjectRequest) (InjectResult, error)

// InjectSession implements Injector.
func (f InjectorFunc) InjectSession(ctx context.Context, req InjectRequest) (InjectResult, error) {
	return f(ctx, req)
}

// SetInjector wires the internal-job executor for path A. Without one (a server
// built with no job service, or a test) Deliver reports no_runner rather than
// pretending to have typed anything.
func (s *Service) SetInjector(i Injector) { s.injector = i }

// TakeoverPlan is what the host knows about continuing a session's CLI
// interactively (design §9.1 B). The relay owns the routing policy and the
// failure reason codes, but it neither reads agent config nor project config, so
// the host resolves the three facts it needs and answers them here:
//   - Argv: the interactive resume command for this agent and session
//     ([agent command] + rendered SessionResumeInteractive), empty when the agent
//     has none — the relay then reports no_resume_template.
//   - AllowInteractive: the project's allow_interactive switch.
//   - ExecRoot: the project root AS THE RUNNER SEES IT (the server's own execution
//     path view for a server-run session, the host path for a worker), against
//     which the relay converts the session's absolute cwd into a relative one.
//     Empty when the project is unknown.
type TakeoverPlan struct {
	Argv             []string
	AllowInteractive bool
	ExecRoot         string
	// AltRoots are the project root's OTHER spellings (host path / container path):
	// a session's cwd or transcript may have been recorded in a different view than
	// the one the runner executes in, and is mapped onto ExecRoot through these.
	AltRoots []string
	// DirExists reports whether an ExecRoot-view directory exists on the runner.
	// nil — or known=false — means the runner cannot be asked (a worker): the wake-up
	// directory is then chosen by string inference only (ChooseResumeCwd says so).
	DirExists func(path string) (exists, known bool)
}

// TakeoverRequest is path B's interactive pty job (design §9.1 B): a value type
// owned by this package so the host can submit it without the relay importing the
// job layer (G022). The host maps it onto its own request (an interactive exec job
// carrying the resume argv, authorised as the SOURCE agent so it does not need the
// broad allow_exec — the same rule as a job resume).
type TakeoverRequest struct {
	ProjectKey string
	Runner     string
	// Cmd is the argv that continues the session (TakeoverPlan.Argv).
	Cmd []string
	// Cwd is project-RELATIVE (the job service SafeJoins it on the runner), so the
	// resumed process opens in the session's own directory.
	Cwd   string
	Title string
	Tags  []string
	// Cols/Rows are the new terminal's initial size.
	Cols, Rows int
	TimeoutSec int
	// SessionID is the CLI session the takeover continues; ResumeSourceAgent is the
	// agent that owns it (the exec carrier's access-control identity).
	SessionID         string
	ResumeSourceAgent string
	// InitialInput is the reply, prefixed and Enter-terminated: the pty runner
	// types it once the resumed TUI has settled (see runner.Request.InitialInput).
	InitialInput string
	CallerID     string
}

// TakeoverResult is the host's report of one takeover job.
type TakeoverResult struct {
	// JobID is the interactive pty job the web attaches to.
	JobID string
}

// Takeoverer is path B's host seam. The host owns agent/project config resolution
// and job submission; the relay owns when a takeover is allowed and what each
// failure means.
type Takeoverer interface {
	// PlanTakeover answers what continuing this session would take (see TakeoverPlan).
	// A zero plan (no argv, no admission) is a legitimate answer, not an error.
	PlanTakeover(agentKey, projectKey, runner, sessionID string) TakeoverPlan
	// TakeoverSession submits the interactive resume job and returns its id. It
	// returns as soon as the job is ACCEPTED — the job runs for as long as the human
	// keeps talking.
	TakeoverSession(ctx context.Context, req TakeoverRequest) (TakeoverResult, error)
	// CancelTakeover stops a takeover job that is still running. A job that already
	// ended (or is not known any more) is NOT an error: the caller wants the session
	// back, and a process that is gone holds nothing.
	CancelTakeover(ctx context.Context, jobID string) error
}

// SetTakeoverer wires path B's planner/submitter. Without one (a server built with
// no job service, or a test) a takeover reports no_runner rather than dispatching
// nothing and claiming success.
func (s *Service) SetTakeoverer(t Takeoverer) { s.takeoverer = t }

// SetAgentInjectLookup wires the per-agent extra foreground-process names (agent
// config `inject_process`). The lookup is consulted per session with that session's
// registered agent key, so the extras widen the whitelist ONLY for sessions that
// really are that agent (a global widening would let any session's pane be typed into
// whenever some agent's process name happened to be in the pane).
func (s *Service) SetAgentInjectLookup(fn func(agentKey string) []string) { s.agentInject = fn }

// SetDeliverPlanner wires the per-agent deliver_command renderer (path C). nil = no
// agent has one.
func (s *Service) SetDeliverPlanner(fn func(agentKey, sessionID, text string) CommandPlan) {
	s.deliverPlan = fn
}

// SetTakeoverAliveSec sets the liveness window of the takeover guard (session
// .takeover_alive_sec); <= 0 turns the heartbeat guard off.
func (s *Service) SetTakeoverAliveSec(sec int) { s.takeoverAliveSec = sec }

// SetInjectCommands overrides the foreground-process whitelist of path A
// (session.inject_commands). Empty keeps the built-in list (DefaultInjectCommands).
func (s *Service) SetInjectCommands(cmds []string) {
	s.injectCommands = append([]string(nil), cmds...)
}

// DeliverResult is where a delivered message ended up.
type DeliverResult struct {
	Path string
	// JobID is the internal exec job of PathTmux ("" on the turn path) or the
	// interactive pty job of PathTakeover.
	JobID string
	// DecisionID is the answered turn of PathTurn, or the audit row of the
	// terminal paths.
	DecisionID string
}

// Deliver routes a reply to a session (design §9.1 选路): an OPEN turn is
// answered exactly as Say does (phase 1 — the blocked hook injects it into the
// same terminal), otherwise the reply is typed into the session's tmux pane
// (path A), and — only when the caller explicitly allows it (allowTakeover, the
// web's confirmation) and A has no usable pane — a NEW interactive pty job
// continues the session with `--resume` and gets the reply as its first input
// (path B). Say itself keeps its old turn-only semantics.
//
// Failures carry a reason code (see DeliverReason): no_runner, no_tmux, ended,
// handed_off:<job>, inject_failed:<...>, no_resume_template,
// interactive_not_allowed, cwd_outside_project. An over-long or empty reply is
// ErrInvalidInput.
func (s *Service) Deliver(ctx context.Context, sid, text, by string, allowTakeover bool) (DeliverResult, error) {
	if strings.TrimSpace(text) == "" {
		return DeliverResult{}, fmt.Errorf("%w: empty text", ErrInvalidInput)
	}
	if len(text) > MaxDeliverText {
		return DeliverResult{}, fmt.Errorf("%w: text is %d bytes, limit %d", ErrInvalidInput, len(text), MaxDeliverText)
	}
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return DeliverResult{}, err
	}
	if !ok {
		return DeliverResult{}, ErrUnknownSession
	}

	// Phase 1 first: with a turn OPEN the agent is blocked on a question, and the
	// hook's long poll is the delivery channel that reaches it.
	open, err := s.store.ListOpenSessionTurns(sid, 1)
	if err != nil {
		return DeliverResult{}, err
	}
	if len(open) > 0 {
		d, err := s.Say(sid, text, by)
		switch {
		case err == nil:
			return DeliverResult{Path: PathTurn, DecisionID: d.ID}, nil
		case !errors.Is(err, ErrNoOpenTurn):
			return DeliverResult{}, err
		}
		// The turn settled between the read and the answer: fall through to A.
	}
	// Path C: the agent's own live-process channel, when it has one. It is tried
	// before tmux because it reaches the process directly (no pane, no foreground
	// whitelist) — and a delivered or FAILED command ends the ladder (a failure could
	// still have delivered, so a second path would risk double delivery).
	notRunning := false
	if res, tried, err := s.deliverCommand(ctx, a, text, by); tried {
		switch {
		case err == nil:
			return res, nil
		case DeliverReason(err) == ReasonNotRunning:
			notRunning = true
		default:
			return DeliverResult{}, err
		}
	}
	res, err := s.deliverTmux(ctx, a, text, by)
	if err == nil {
		return res, nil
	}
	// Path B is the fallback for the failures A cannot fix: the session has no
	// usable tmux pane, or the INJECTION ITSELF could not run (the runner refused
	// the job / the script died before it could look at a pane). Anything else (an
	// ended or already handed-off session, no execution machine, a pane the human
	// is using) would fail on B too, and reporting the real reason is more useful
	// than a second attempt.
	if !allowTakeover || !takeoverFallback(DeliverReason(err)) {
		return DeliverResult{}, err
	}
	if err := s.takeoverAliveCheck(a, notRunning); err != nil {
		return DeliverResult{}, err
	}
	return s.deliverTakeover(ctx, a, text, by)
}

// takeoverAliveCheck refuses a takeover while the original process still looks alive:
// the new `--resume` process and the old one would append to the same session store
// and fork the conversation. notRunning is the positive evidence of an agent's
// deliver_command (exit 3), which settles it; otherwise the last hook heartbeat
// decides — younger than the window (session.takeover_alive_sec) is alive. An ended
// or offline session is never alive.
func (s *Service) takeoverAliveCheck(a jobstore.AgentSession, notRunning bool) error {
	if notRunning || s.takeoverAliveSec <= 0 {
		return nil
	}
	switch a.State {
	case jobstore.SessionEnded, jobstore.SessionOffline:
		return nil
	}
	if a.LastSeenAt <= 0 {
		return nil
	}
	age := s.nowFn().Unix() - a.LastSeenAt
	if age < int64(s.takeoverAliveSec) {
		return undeliverable(ReasonSessionAlive, fmt.Errorf(
			"the session's process reported in %ds ago (< %ds): it is still running; continue in its terminal instead of taking it over",
			max(age, 0), s.takeoverAliveSec))
	}
	return nil
}

// deliverCommand is path C. tried=false means the agent has no deliver_command (or the
// session cannot be reached by one) and the caller proceeds as before. A not-running
// answer is returned as an UndeliverableError{ReasonNotRunning}.
func (s *Service) deliverCommand(ctx context.Context, a jobstore.AgentSession, text, by string) (DeliverResult, bool, error) {
	if s.deliverPlan == nil || s.injector == nil {
		return DeliverResult{}, false, nil
	}
	if a.State == jobstore.SessionEnded {
		return DeliverResult{}, false, nil // the tmux path reports `ended`
	}
	if a.State == jobstore.SessionHandedOff {
		return DeliverResult{}, false, nil // the tmux path reports handed_off:<job>
	}
	plan := s.deliverPlan(a.Agent, a.SessionID, InjectPrefix+text)
	if len(plan.Argv) == 0 {
		return DeliverResult{}, false, nil
	}
	if strings.TrimSpace(a.Runner) == "" {
		return DeliverResult{}, true, undeliverable(DeliverFailedPrefix+ReasonNoRunner, errors.New(
			"the session did not register an execution machine, so its deliver_command cannot be run"))
	}
	res, err := s.injector.InjectSession(ctx, InjectRequest{
		ProjectKey:  a.ProjectKey,
		Runner:      a.Runner,
		Cwd:         ".",
		Title:       "relay deliver → " + shortSessionID(a.SessionID),
		Tags:        []string{TagRelayDeliver},
		TimeoutSec:  injectTimeoutSec,
		Cmd:         plan.Argv,
		Stdin:       plan.Stdin,
		SourceAgent: a.Agent,
	})
	if err != nil {
		return DeliverResult{}, true, undeliverable(DeliverFailedPrefix+truncateRunes(err.Error(), 160), err)
	}
	exit := res.ExitCode
	if exit != 0 && exit != deliverExitNotRunning && plan.OfflineMatch != nil && plan.OfflineMatch.MatchString(res.Output) {
		exit = deliverExitNotRunning
	}
	switch exit {
	case 0:
	case deliverExitNotRunning:
		return DeliverResult{}, true, undeliverable(ReasonNotRunning, fmt.Errorf("job %s: the session's process is not running", res.JobID))
	default:
		detail := strings.TrimSpace(res.Output)
		if detail == "" {
			detail = fmt.Sprintf("exit code %d", res.ExitCode)
		}
		return DeliverResult{}, true, undeliverable(DeliverFailedPrefix+truncateRunes(detail, 160), fmt.Errorf("job %s: %s", res.JobID, detail))
	}
	if _, err := s.store.SetSessionState(a.SessionID, jobstore.SessionRunning); err != nil {
		return DeliverResult{}, true, err
	}
	d, err := s.recordCommand(a, text, by, res.JobID)
	if err != nil {
		return DeliverResult{}, true, err
	}
	return DeliverResult{Path: PathCommand, JobID: res.JobID, DecisionID: d.ID}, true, nil
}

// recordCommand writes path C's audit row (the same shape as an injection's).
func (s *Service) recordCommand(a jobstore.AgentSession, text, by, jobID string) (jobstore.PlanDecision, error) {
	detail, err := json.Marshal(map[string]string{"path": PathCommand, "job_id": jobID})
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	d := jobstore.PlanDecision{
		Title:      fmt.Sprintf("%s · 在线送话", sessionLabel(a)),
		Question:   "（会话未在等待回复：这条消息已通过 agent 的送话命令交给在线进程）",
		TimeoutSec: 60,
		SessionID:  a.SessionID,
		Kind:       jobstore.DecisionKindRelay,
		Detail:     string(detail),
	}
	if err := s.store.InsertDecision(&d); err != nil {
		return jobstore.PlanDecision{}, err
	}
	answered, err := s.store.AnswerDecision(d.ID, text, by)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	if !answered {
		return jobstore.PlanDecision{}, ErrUnknownTurn
	}
	stored, _, err := s.store.GetDecision(d.ID)
	return stored, err
}

// takeoverFallback reports whether a failed path-A delivery is worth retrying as
// path B: the session has no usable tmux pane (none was ever registered, or the
// pane is gone since it was), or the injection job never got far enough to judge
// one (a runner-side failure: the dispatch errored, or the script exited with a
// code that is not the pane check's own). In both cases A learned nothing about
// the pane, and a new process is the only way left to speak to the session.
//
// A pane held by ANOTHER program is deliberately not in this set: that terminal
// belongs to a human who is using it, and answering "the pane is busy" with "then
// move your conversation to a new process" would take it away from them.
func takeoverFallback(reason string) bool {
	return reason == ReasonNoTmux ||
		reason == InjectFailedPrefix+injectPaneMissing ||
		reason == InjectFailedPrefix+injectRunnerError
}

// deliverTmux is path A: dispatch the internal job that types the reply into the
// session's tmux pane. The pane must exist, its foreground process must be an
// agent CLI (the human may be editing a file in that pane), and the whole thing
// is auditable — the row is the only trace left when someone asks later "who
// typed this into my terminal?".
func (s *Service) deliverTmux(ctx context.Context, a jobstore.AgentSession, text, by string) (DeliverResult, error) {
	// An `ended` session has no terminal left.
	if a.State == jobstore.SessionEnded {
		return DeliverResult{}, undeliverable(ReasonEnded, errors.New("the session has ended"))
	}
	// A handed-off session's terminal belongs to path B's takeover job: typing into
	// the ORIGINAL pane would feed a process that no longer owns the conversation
	// (design §9.1 B). The caller releases the takeover first.
	if a.State == jobstore.SessionHandedOff {
		return DeliverResult{}, undeliverable(HandedOffPrefix+a.HandedOffJobID, fmt.Errorf(
			"the session is already taken over by job %s; release the takeover to speak to this terminal again", a.HandedOffJobID))
	}
	if strings.TrimSpace(a.Runner) == "" {
		return DeliverResult{}, undeliverable(ReasonNoRunner, errors.New(
			"the session did not register an execution machine: run a gofer worker where the session runs and point GOFER_HOOK_RUNNER at it"))
	}
	if strings.TrimSpace(a.TmuxPane) == "" {
		return DeliverResult{}, undeliverable(ReasonNoTmux, errors.New(
			"the session is not running inside tmux (no pane registered); start it as `tmux new -A -s claude`"))
	}
	if s.injector == nil {
		return DeliverResult{}, undeliverable(ReasonNoRunner, errors.New("no injection executor is wired on this server"))
	}

	res, err := s.injector.InjectSession(ctx, InjectRequest{
		ProjectKey: a.ProjectKey,
		Runner:     a.Runner,
		Cwd:        ".",
		Title:      "relay inject → " + shortSessionID(a.SessionID),
		Tags:       []string{TagRelayInject},
		TimeoutSec: injectTimeoutSec,
		Cmd:        []string{"sh", "-c", injectScript(a.TmuxPane, InjectPrefix+text, s.injectAllowListFor(a.Agent))},
	})
	if err != nil {
		return DeliverResult{}, undeliverable(InjectFailedPrefix+injectRunnerError, err)
	}
	if reason := injectResultReason(res); reason != "" {
		detail := strings.TrimSpace(res.Output)
		if detail == "" {
			detail = "the injection job failed"
		}
		return DeliverResult{}, undeliverable(reason, fmt.Errorf("job %s: %s", res.JobID, detail))
	}

	// Typed into the pane: the agent is about to run again.
	if _, err := s.store.SetSessionState(a.SessionID, jobstore.SessionRunning); err != nil {
		return DeliverResult{}, err
	}
	d, err := s.recordInject(a, text, by, res.JobID)
	if err != nil {
		return DeliverResult{}, err
	}
	return DeliverResult{Path: PathTmux, JobID: res.JobID, DecisionID: d.ID}, nil
}

// takeoverCheck runs path B's four preconditions and returns what the takeover
// job needs: the resume plan and the project-relative cwd. Each failure carries its
// own reason code (design §9.1 v0.5): the session is already taken over, no resume
// argv for this agent, a project that forbids interactive jobs, a cwd that cannot be
// expressed relative to the project root the runner sees.
//
// An `ended` session passes: a closed terminal is exactly what a takeover is for —
// `--resume <sid>` reopens the CLI conversation in a new process.
func (s *Service) takeoverCheck(a jobstore.AgentSession) (TakeoverPlan, CwdChoice, error) {
	if a.State == jobstore.SessionHandedOff {
		return TakeoverPlan{}, CwdChoice{}, undeliverable(HandedOffPrefix+a.HandedOffJobID, fmt.Errorf(
			"the session is already taken over by job %s", a.HandedOffJobID))
	}
	if strings.TrimSpace(a.Runner) == "" {
		return TakeoverPlan{}, CwdChoice{}, undeliverable(ReasonNoRunner, errors.New(
			"the session did not register an execution machine: the takeover process has to run where the session runs"))
	}
	if s.takeoverer == nil {
		return TakeoverPlan{}, CwdChoice{}, undeliverable(ReasonNoRunner, errors.New("no takeover executor is wired on this server"))
	}
	plan := s.takeoverer.PlanTakeover(a.Agent, a.ProjectKey, a.Runner, a.SessionID)
	if len(plan.Argv) == 0 {
		return TakeoverPlan{}, CwdChoice{}, undeliverable(ReasonNoResumeTemplate, fmt.Errorf(
			"agent %q has no interactive resume template, so no new process could continue this session (start it inside tmux for path A)", a.Agent))
	}
	if !plan.AllowInteractive {
		return TakeoverPlan{}, CwdChoice{}, undeliverable(ReasonInteractiveNotAllowed, fmt.Errorf(
			"project %q does not allow interactive jobs (allow_interactive)", a.ProjectKey))
	}
	choice, ok := ChooseResumeCwd(a, plan)
	if !ok {
		return TakeoverPlan{}, CwdChoice{}, undeliverable(ReasonCwdOutsideProject, fmt.Errorf(
			"session cwd %q is not under the project root %q as runner %q sees it", a.Cwd, plan.ExecRoot, a.Runner))
	}
	return plan, choice, nil
}

// deliverTakeover is path B: start a new interactive pty job that continues the
// session's CLI conversation and prime it with the reply (text may be empty for a
// plain wake-up: the new terminal then just opens on the resumed conversation).
func (s *Service) deliverTakeover(ctx context.Context, a jobstore.AgentSession, text, by string) (DeliverResult, error) {
	plan, choice, err := s.takeoverCheck(a)
	if err != nil {
		return DeliverResult{}, err
	}
	initial := ""
	if strings.TrimSpace(text) != "" {
		// The reply is the new terminal's first input, carrying the same prefix path
		// A types (design D7) so the hook's UserPromptSubmit recognises it as ours
		// and does not treat it as the human returning to the keyboard.
		initial = InjectPrefix + text + "\r"
	}
	res, err := s.takeoverer.TakeoverSession(ctx, TakeoverRequest{
		ProjectKey: a.ProjectKey,
		Runner:     a.Runner,
		Cmd:        plan.Argv,
		Cwd:        choice.Rel,
		Title:      "relay takeover → " + shortSessionID(a.SessionID),
		Tags:       []string{TagRelayTakeover},
		Cols:       takeoverCols,
		Rows:       takeoverRows,
		TimeoutSec: takeoverTimeout,
		SessionID:  a.SessionID,
		// The exec carrier is authorised as the session's OWN agent (the same
		// exemption a job resume uses): the takeover runs the agent's resume argv,
		// it is not a new grant of exec.
		ResumeSourceAgent: a.Agent,
		InitialInput:      initial,
		CallerID:          by,
	})
	if err != nil {
		// An attempted takeover that could not be submitted is a runner-side failure;
		// it shares path A's code for "we tried and the execution machine said no".
		return DeliverResult{}, undeliverable(InjectFailedPrefix+injectRunnerError, err)
	}

	// The session now belongs to the new process: from here its original terminal
	// stops relaying (WaitReason) and refuses new turns (OpenTurn).
	if _, err := s.store.SetSessionHandedOff(a.SessionID, res.JobID); err != nil {
		return DeliverResult{}, err
	}
	d, err := s.recordTakeover(a, text, by, res.JobID)
	if err != nil {
		return DeliverResult{}, err
	}
	if s.notifier != nil {
		s.notifier.NotifySessionHandedOff(a.SessionID, a.ProjectKey, a.Title, res.JobID)
	}
	return DeliverResult{Path: PathTakeover, JobID: res.JobID, DecisionID: d.ID}, nil
}

// recordTakeover writes path B's audit row: as with an injection, nothing was
// WAITING, so the row records where the conversation went instead. question keeps
// the timeline readable — the agent side of this row says why there is no agent
// message.
func (s *Service) recordTakeover(a jobstore.AgentSession, text, by, jobID string) (jobstore.PlanDecision, error) {
	detail, err := json.Marshal(map[string]string{"path": PathTakeover, "job_id": jobID})
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	d := jobstore.PlanDecision{
		Title:      fmt.Sprintf("%s · 接管", sessionLabel(a)),
		Question:   "（会话未在等待回复：这条消息由 web 新起的 `--resume` 进程接收）",
		TimeoutSec: 60,
		SessionID:  a.SessionID,
		Kind:       jobstore.DecisionKindRelay,
		Detail:     string(detail),
	}
	if err := s.store.InsertDecision(&d); err != nil {
		return jobstore.PlanDecision{}, err
	}
	if strings.TrimSpace(text) == "" {
		text = "（唤醒：没有附带首条消息）"
	}
	answered, err := s.store.AnswerDecision(d.ID, text, by)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	if !answered {
		return jobstore.PlanDecision{}, ErrUnknownTurn
	}
	stored, _, err := s.store.GetDecision(d.ID)
	return stored, err
}

// injectResultReason maps the injection job's exit code to the failure reason
// ("" = the text was typed into the pane).
func injectResultReason(res InjectResult) string {
	switch res.ExitCode {
	case 0:
		return ""
	case injectExitPaneMissing:
		return InjectFailedPrefix + injectPaneMissing
	case injectExitPaneBusy:
		return InjectFailedPrefix + paneBusyReason(res.Output)
	default:
		return InjectFailedPrefix + injectRunnerError
	}
}

// paneBusyReason reads the pane's foreground command out of the script's
// `pane_busy:<cmd>` line, so the human is told WHAT is holding the pane.
func paneBusyReason(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pane_busy:") && len(line) > len("pane_busy:") {
			return truncateRunes(line, 64)
		}
	}
	return "pane_busy:unknown"
}

// recordInject writes the audit row of an injection (design §9.1 A: the reply is
// not a turn — nothing was waiting — so the row records that it went to a
// terminal instead, with the job that typed it). question keeps the timeline
// readable: the agent side of this row says WHY there is no agent message.
func (s *Service) recordInject(a jobstore.AgentSession, text, by, jobID string) (jobstore.PlanDecision, error) {
	detail, err := json.Marshal(map[string]string{"path": PathTmux, "job_id": jobID})
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	d := jobstore.PlanDecision{
		Title:      fmt.Sprintf("%s · tmux 注入", sessionLabel(a)),
		Question:   "（会话未在等待回复：这条消息已直接送入终端 tmux）",
		TimeoutSec: 60,
		SessionID:  a.SessionID,
		Kind:       jobstore.DecisionKindRelay,
		Detail:     string(detail),
	}
	if err := s.store.InsertDecision(&d); err != nil {
		return jobstore.PlanDecision{}, err
	}
	answered, err := s.store.AnswerDecision(d.ID, text, by)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	if !answered {
		return jobstore.PlanDecision{}, ErrUnknownTurn
	}
	stored, _, err := s.store.GetDecision(d.ID)
	return stored, err
}

// injectAllowList resolves the whitelist actually used by the script: the
// configured commands, kept only when they are plain shell words (anything else
// is DROPPED — an operator-supplied entry may narrow who can be typed into, never
// smuggle script into the command line), else the built-in list.
func (s *Service) injectAllowList() []string {
	out := make([]string, 0, len(s.injectCommands))
	for _, c := range s.injectCommands {
		if n := sanitizeCommandName(c); n != "" {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return defaultInjectCommands
	}
	return out
}

// injectAllowListFor is injectAllowList plus the agent's own inject_process names (the
// union, de-duplicated, same plain-shell-word filter).
func (s *Service) injectAllowListFor(agentKey string) []string {
	base := s.injectAllowList()
	if s.agentInject == nil || strings.TrimSpace(agentKey) == "" {
		return base
	}
	extra := s.agentInject(agentKey)
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, c := range append(append([]string(nil), base...), extra...) {
		n := sanitizeCommandName(c)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// sanitizeCommandName returns s when it is a plain shell word (what a command
// name is made of) and "" when it is not — an unusable entry is dropped, not
// mangled into a name that would silently never match.
func sanitizeCommandName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '+':
		default:
			return ""
		}
	}
	return s
}

// injectScript builds the ONE POSIX shell script path A runs on the session's
// execution machine: confirm the pane still exists and is sitting in an agent CLI
// (the human may have that pane in an editor by now), then type the reply into it
// line by line with `-l` (literal) plus the Enter that submits each line.
//
// Both the pane id and the text go in as single-quoted shell words, so a reply
// containing quotes, `$` or backticks is typed VERBATIM and can never execute.
// The pane check reports its reason on stdout and by exit code (3 = gone,
// 4 = busy), which the caller turns into the failure reason.
func injectScript(pane, text string, allow []string) string {
	if len(allow) == 0 {
		allow = defaultInjectCommands
	}
	q := shQuote(pane)
	var b strings.Builder
	b.WriteString("cur=$(tmux display -p -t " + q + " '#{pane_current_command}' 2>/dev/null) || { echo " + injectPaneMissing + "; exit 3; }\n")
	b.WriteString(`[ -n "$cur" ] || { echo ` + injectPaneMissing + "; exit 3; }\n")
	b.WriteString("case \"$cur\" in\n")
	b.WriteString(strings.Join(allow, "|") + ") ;;\n")
	b.WriteString("*) echo \"pane_busy:$cur\"; exit 4 ;;\n")
	b.WriteString("esac\n")
	for _, line := range injectLines(text) {
		b.WriteString("tmux send-keys -t " + q + " -l -- " + shQuote(line) + " || exit 1\n")
		b.WriteString("tmux send-keys -t " + q + " Enter || exit 1\n")
	}
	return b.String()
}

// injectLines splits a reply into the lines typed one by one. A trailing newline
// is not an extra empty line: the last line already gets the Enter that sends it.
func injectLines(text string) []string {
	lines := strings.Split(text, "\n")
	if n := len(lines); n > 1 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// shQuote wraps s in single quotes for a POSIX shell: everything inside is
// literal (including $ and backticks), and a quote itself is closed, escaped and
// reopened.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shortSessionID is the 8-char session prefix used in job titles.
func shortSessionID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}

// truncateRunes caps s at n runes on one line (reason codes stay readable).
func truncateRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}
