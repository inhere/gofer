package sessionrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

// MaxDeliverText caps a reply delivered to a session (bytes). The tmux path
// types it into a live terminal, so it is capped well below the hook's own
// message limits (design §9.1 A: 8KB).
const MaxDeliverText = 8 * 1024

// TagRelayInject labels the internal exec jobs path A dispatches, so they can be
// told apart from human-submitted jobs in `gofer job ls --tag`.
const TagRelayInject = "relay-inject"

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

// SetInjectCommands overrides the foreground-process whitelist of path A
// (session.inject_commands). Empty keeps the built-in list (DefaultInjectCommands).
func (s *Service) SetInjectCommands(cmds []string) {
	s.injectCommands = append([]string(nil), cmds...)
}

// DeliverResult is where a delivered message ended up.
type DeliverResult struct {
	Path string
	// JobID is the internal exec job of PathTmux ("" on the turn path).
	JobID string
	// DecisionID is the answered turn of PathTurn, or the audit row of PathTmux.
	DecisionID string
}

// Deliver routes a reply to a session (design §9.1 选路): an OPEN turn is
// answered exactly as Say does (phase 1 — the blocked hook injects it into the
// same terminal), otherwise the reply is typed into the session's tmux pane
// (path A). Say itself keeps its old turn-only semantics.
//
// Failures carry a reason code (see DeliverReason): no_runner, no_tmux, ended,
// inject_failed:<...>. An over-long or empty reply is ErrInvalidInput.
func (s *Service) Deliver(ctx context.Context, sid, text, by string) (DeliverResult, error) {
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
	open, err := s.store.ListSessionDecisions(sid, jobstore.DecisionOpen, 1)
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
	return s.deliverTmux(ctx, a, text, by)
}

// deliverTmux is path A: dispatch the internal job that types the reply into the
// session's tmux pane. The pane must exist, its foreground process must be an
// agent CLI (the human may be editing a file in that pane), and the whole thing
// is auditable — the row is the only trace left when someone asks later "who
// typed this into my terminal?".
func (s *Service) deliverTmux(ctx context.Context, a jobstore.AgentSession, text, by string) (DeliverResult, error) {
	// An `ended` session has no terminal left. (A `handed_off` session — path B's
	// takeover, design §9.1 v0.5 — joins this switch when that state lands.)
	if a.State == jobstore.SessionEnded {
		return DeliverResult{}, undeliverable(ReasonEnded, errors.New("the session has ended"))
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
		Cmd:        []string{"sh", "-c", injectScript(a.TmuxPane, InjectPrefix+text, s.injectAllowList())},
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
