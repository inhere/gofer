package hookrelay

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/client"
)

// API is the hub surface the runner needs; *client.Client satisfies it.
type API interface {
	RegisterSession(in client.SessionRegister) (client.AgentSession, error)
	HeartbeatSession(sid string, hb client.SessionHeartbeat) (client.AgentSession, error)
	OpenSessionTurn(sid, msg string, timeoutSec int64) (client.Decision, error)
	WaitSessionTurn(sid, decisionID string, waitSec int) (client.TurnStatus, error)
	ReleaseSessionTurn(sid, decisionID string, idleSec int64) (bool, error)
	SetSessionRelayMode(sid, mode string) (client.AgentSession, error)
	AddSessionJobWatch(sid, jobID string) (client.SessionJobWatch, error)
	ListSessionJobWatches(sid string) ([]client.SessionJobWatch, error)
	RemoveSessionJobWatch(sid, jobID string) error
	CompleteSessionWatchTurn(sid, turnID string, jobIDs []string) (bool, error)
	CompleteSessionWatches(sid string, jobIDs []string) (bool, error)
}

// Options tunes one hook invocation. Zero values pick the defaults below.
type Options struct {
	// Runner labels where the session lives (server | <worker-id>).
	Runner string
	// ProjectKey overrides the server's cwd→project match (env GOFER_PROJECT).
	ProjectKey string
	// TmuxPane is $TMUX_PANE when the session runs inside tmux (optional).
	TmuxPane string
	// Wait is the Stop hook's total blocking budget (the hook's own timeout
	// minus a margin). Default 540s (the Claude default hook timeout is 600s).
	Wait time.Duration
	// PollSec is the per-request long-poll window (server caps at 25). An
	// idle-armed wait caps it at autoArmPollSec instead, so the human's return
	// is noticed promptly.
	PollSec int
	// MaxMessage caps the relayed last message (bytes). Default 64 KiB.
	MaxMessage int
	// CurrentFile, when set, receives the session id on SessionStart so the CLI
	// can resolve "the session in this directory" offline.
	CurrentFile string
	// PeerSessionsDir overrides ~/.claude/sessions for tests and isolated
	// runners. Empty uses the host user's normal Claude directory.
	PeerSessionsDir string
	// Log receives one line per notable step (nil = discard). Never stderr:
	// Claude Code shows hook stderr to the user.
	Log io.Writer
	// ProgressInterval throttles PostToolUse progress reports. A non-positive
	// value disables progress reporting. ProgressStateDir stores only local
	// timestamps, one file per session, so the hook remains fast and stateless.
	ProgressInterval time.Duration
	ProgressStateDir string
	// UsageStateDir holds the per-session transcript read offsets of terminal-session
	// usage collection (N2 §A); empty disables collection. UsageReadBudget overrides
	// the per-invocation read cap in bytes (tests; 0 = default).
	UsageStateDir   string
	UsageReadBudget int64
	// PromptMemories loads the memories a human UserPromptSubmit may match by
	// when.keywords (design 2026-10-09 §2.9, P1b); nil disables the injection.
	// MemoryStateDir keeps the per-session record of injected memories (one file
	// per session, pruned after MemoryStateTTL); MemoryBudget caps the injected
	// bytes per prompt (default 2 KiB).
	PromptMemories MemoryLoader
	MemoryStateDir string
	MemoryStateTTL time.Duration
	MemoryBudget   int
	// CommandMemories loads the memories a shell PreToolUse may match by
	// when.commands (§2.9, P5); nil disables it. It shares MemoryStateDir (one
	// injected set per session for prompt and command injection) and
	// MemoryBudget. CommandDeadline is the hard budget of that path (default
	// DefaultCommandMemoryDeadline): past it the hook injects nothing.
	CommandMemories MemoryLoader
	CommandDeadline time.Duration

	now   func() time.Time
	sleep func(time.Duration)
}

// Result is what the hook must do after Run: when Blocked, print the
// `{"decision":"block","reason":Reason}` continuation and exit 0. Notice is a
// one-line message the SERVER wants the person at this terminal to see (session
// relay §9.1 B: the session was taken over on the web) — the caller prints it on
// stderr, which is the only channel the human at the keyboard reads.
type Result struct {
	Blocked bool
	Reason  string
	Notice  string
	// Context is extra context for the agent on SessionStart / UserPromptSubmit
	// (undelivered "[gofer job 完成]" notices, caught up after a missed Stop) and
	// on PreToolUse (when.commands-matched memories). The
	// caller prints it as hookSpecificOutput.additionalContext.
	Context string
}

// ReplyPrefix marks an injected web reply so the model knows the source is
// equivalent to terminal input (design D7).
const ReplyPrefix = "[gofer web 回复] "

// JobDoneTag opens every watched-job completion notice (formatWatchedJobCompletion).
// The notice is fed back to the agent as its next input, so the following
// UserPromptSubmit carries it and must not count as a human prompt.
const JobDoneTag = "[gofer job 完成]"

// OffCommand typed as the web reply switches relay off and lets the agent
// stop normally (the human is back at the keyboard).
const OffCommand = "/off"

const (
	defaultWait       = 540 * time.Second
	defaultPollSec    = 25
	defaultMaxMessage = 64 * 1024
	// transientRetries is how many consecutive transport failures the Stop wait
	// loop tolerates before giving up (never blocking the terminal on a dead hub).
	transientRetries = 5
	transientBackoff = 2 * time.Second
	// autoArmPollSec caps a poll round on an idle-armed wait: the human's return
	// is only visible at a poll boundary, so the wait checks more often than the
	// 25s a switched-on relay uses.
	autoArmPollSec = 5
)

// noMessageFallback is relayed when the last assistant text cannot be read.
const noMessageFallback = "(无法读取最后一条消息，请查看终端)"

// Run executes one hook event. Network failures never surface as errors — the
// terminal must never be blocked by a dead hub — they are logged and the hook
// exits quietly. The returned error is reserved for programmer/IO faults the
// caller may report on stderr (exit 1, non-blocking for the CLI).
func Run(api API, p Payload, opts Options) (Result, error) {
	opts = opts.withDefaults()
	log := func(format string, args ...any) {
		if opts.Log != nil {
			v := reflect.ValueOf(opts.Log)
			if (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface || v.Kind() == reflect.Map || v.Kind() == reflect.Func || v.Kind() == reflect.Slice) && v.IsNil() {
				return
			}
			fmt.Fprintf(opts.Log, "%s %s %s %s "+format+"\n",
				append([]any{opts.now().Format(time.RFC3339), p.Agent, shortID(p.SessionID), p.Event}, args...)...)
		}
	}
	r := &runner{api: api, p: p, opts: opts, log: log}
	switch p.Event {
	case "SessionStart":
		a, err := r.sessionStart()
		if err != nil {
			return Result{}, nil // logged; the next event re-registers
		}
		return r.catchUp(Result{}, a), nil
	case "UserPromptSubmit":
		// Only a prompt a HUMAN typed means "back at the keyboard" (auto-off).
		// Claude Code also raises UserPromptSubmit for harness-generated turns:
		// our own Stop-hook continuation (ReplyPrefix), a reply path A typed into
		// the terminal (§9.1 A — it arrives with NO turn behind it), background-task
		// notifications, system reminders... Those are reported as injected so
		// the server keeps relay on.
		if isHarnessInput(p) {
			log("harness prompt %q → injected", head(p.Prompt, 60))
			a := r.beatAndLog(client.SessionHeartbeat{Event: p.Event, Injected: true})
			return r.catchUp(noticeResult(a), a), nil
		}
		log("human prompt %q", head(p.Prompt, 60))
		a := r.beatAndLog(client.SessionHeartbeat{Event: p.Event, Title: makeTitle(p.Cwd, p.Prompt)})
		return r.injectPromptMemories(r.catchUp(noticeResult(a), a)), nil
	case "Stop":
		return r.stop(), nil
	case "PostToolUse":
		return r.postToolUse(), nil
	case "PreToolUse":
		return r.preToolUse(), nil
	case "SubagentStart", "SubagentStop":
		// Sub-agent bookkeeping (N1 §C): the server counts the session's running
		// sub-agents; a Stop that arrives while any run is not armed, and the last
		// SubagentStop releases an already-blocked wait. Nothing to print.
		delta := 1
		if p.Event == "SubagentStop" {
			delta = -1
		}
		log("subagent id=%q type=%q delta=%d", p.AgentID, p.AgentType, delta)
		r.beatAndLog(client.SessionHeartbeat{Event: p.Event, SubagentID: p.AgentID, SubagentDelta: delta})
		return Result{}, nil
	case "SessionEnd", "Interrupt":
		r.beatAndLog(client.SessionHeartbeat{Event: p.Event})
		return Result{}, nil
	case "Notification":
		hb := client.SessionHeartbeat{Event: p.Event, LastMessage: strings.TrimSpace(p.Message)}
		if p.NotificationType == "idle_prompt" {
			hb.State = "idle"
			// The agent is sitting at an idle prompt: report how long the machine
			// has been untouched, which is exactly the auto-arm evidence.
			hb.IdleSec = idleSecPtr()
		}
		r.beatAndLog(hb)
		return Result{}, nil
	default:
		log("ignored")
		return Result{}, nil
	}
}

// ReportInterrupt is what a Stop hook does when the terminal kills it while it
// blocks for a web reply (Esc on "hook running": Claude Code aborts the hook
// with a signal, and nothing else runs afterwards). It tells the hub the wait is
// over — an Interrupt beat, which settles the OPEN turn whatever the relay
// switch says and moves the session back to idle — so the web stops offering a
// reply box that no process listens to. The switch is left alone: the next stop
// is relayed again. Best effort, like every hook→hub call.
func ReportInterrupt(api API, p Payload, opts Options) {
	opts = opts.withDefaults()
	log := func(format string, args ...any) {
		if opts.Log != nil {
			fmt.Fprintf(opts.Log, "%s %s %s %s "+format+"\n",
				append([]any{opts.now().Format(time.RFC3339), p.Agent, shortID(p.SessionID), p.Event}, args...)...)
		}
	}
	r := &runner{api: api, p: p, opts: opts, log: log}
	log("interrupted by the terminal while waiting, releasing the turn")
	r.beatAndLog(client.SessionHeartbeat{Event: "Interrupt"})
}

func (r *runner) postToolUse() Result {
	if r.opts.ProgressInterval > 0 && strings.TrimSpace(r.p.TranscriptPath) != "" && r.p.dialect() != DialectGeneric {
		text, err := LastAssistantText(r.p.TranscriptPath, r.opts.MaxMessage)
		if err == nil && strings.TrimSpace(text) != "" && r.progressDue() {
			if _, ok := r.heartbeat(client.SessionHeartbeat{
				Event: "PostToolUse", ProgressText: text, ProgressAt: r.opts.now().Unix(),
			}); ok {
				r.markProgressReported()
			}
		}
	}
	for _, jobID := range extractJobWatchCandidates(r.p.ToolName, r.p.ToolOutput) {
		if _, err := r.api.AddSessionJobWatch(r.p.SessionID, jobID); err != nil {
			r.log("register job watch %s failed: %v", jobID, err)
		}
	}
	return Result{}
}

func (r *runner) progressStatePath() string {
	dir := strings.TrimSpace(r.opts.ProgressStateDir)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "gofer-session-progress")
	}
	hash := sha1.Sum([]byte(r.p.SessionID))
	return filepath.Join(dir, fmt.Sprintf("%x.ts", hash[:]))
}

func (r *runner) progressDue() bool {
	path := r.progressStatePath()
	b, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var last int64
	if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &last); err != nil {
		return true
	}
	return r.opts.now().Sub(time.Unix(last, 0)) >= r.opts.ProgressInterval
}

func (r *runner) markProgressReported() {
	path := r.progressStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d\n", r.opts.now().Unix())), 0o600)
}

func (o Options) withDefaults() Options {
	if o.Wait <= 0 {
		o.Wait = defaultWait
	}
	if o.PollSec <= 0 {
		o.PollSec = defaultPollSec
	}
	if o.MaxMessage <= 0 {
		o.MaxMessage = defaultMaxMessage
	}
	if o.MemoryBudget <= 0 {
		o.MemoryBudget = DefaultPromptMemoryBudget
	}
	if o.MemoryStateTTL <= 0 {
		o.MemoryStateTTL = DefaultPromptMemoryStateTTL
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.sleep == nil {
		o.sleep = time.Sleep
	}
	return o
}

type runner struct {
	api  API
	p    Payload
	opts Options
	log  func(string, ...any)
}

func (r *runner) register(event string) (client.AgentSession, error) {
	peer, detail := r.peerIdentity()
	if detail != "" {
		r.log("peer identity unavailable: %s", detail)
	}
	a, err := r.api.RegisterSession(client.SessionRegister{
		SessionID: r.p.SessionID, Agent: r.p.Agent, ProjectKey: r.opts.ProjectKey,
		Runner: r.opts.Runner, Cwd: r.p.Cwd, Transcript: r.p.TranscriptPath,
		TmuxPane: r.opts.TmuxPane, Event: event, PeerName: peer.Name,
		PeerStatus: peer.Status, PeerMessaging: peer.Messaging, PeerNameSource: peer.NameSource,
	})
	if err != nil {
		r.log("register failed: %v", err)
	}
	return a, err
}

// peerIdentity reads Claude Code's own session bookkeeping; a generic agent has none.
func (r *runner) peerIdentity() (peerIdentity, string) {
	if r.p.dialect() == DialectGeneric {
		return peerIdentity{}, ""
	}
	return readPeerIdentity(r.p.SessionID, r.opts.PeerSessionsDir)
}

func (r *runner) sessionStart() (client.AgentSession, error) {
	a, err := r.register(r.p.Event)
	if err != nil {
		return a, err // logged; the next event re-registers
	}
	if r.opts.CurrentFile != "" {
		if err := os.MkdirAll(filepath.Dir(r.opts.CurrentFile), 0o755); err == nil {
			_ = os.WriteFile(r.opts.CurrentFile, []byte(r.p.SessionID+"\n"), 0o644)
		}
	}
	r.log("registered")
	return a, nil
}

// heartbeat reports an event; an unknown session (hooks installed mid-session,
// or hub restarted with a fresh db) is registered first and the beat retried.
// ok is false when the hub could not be reached.
func (r *runner) heartbeat(hb client.SessionHeartbeat) (a client.AgentSession, ok bool) {
	peer, detail := r.peerIdentity()
	if detail != "" {
		r.log("peer identity unavailable: %s", detail)
	}
	hb.PeerName, hb.PeerNameSource, hb.PeerStatus = peer.Name, peer.NameSource, peer.Status
	hb.PeerMessaging = &peer.Messaging
	// Terminal-session usage (N2 §A): read the transcript's new bytes on Stop /
	// SubagentStop / SessionEnd. The offset is only saved once the hub took the beat.
	var commitUsage func(bool)
	if hb.UsageDelta == nil {
		hb.UsageDelta, commitUsage = r.collectUsage()
	}
	if commitUsage != nil {
		sent := hb.UsageDelta != nil
		defer func() { commitUsage(ok || !sent) }()
	}
	// The hook's own cwd rides every beat: the web shows it as the session's
	// "current directory" (display only — registration keeps its own cwd).
	if hb.Cwd == "" {
		hb.Cwd = r.p.Cwd
	}
	a, err := r.api.HeartbeatSession(r.p.SessionID, hb)
	if err != nil && client.StatusOf(err) == 404 {
		if _, rerr := r.register(hb.Event); rerr == nil {
			a, err = r.api.HeartbeatSession(r.p.SessionID, hb)
		}
	}
	if err != nil {
		r.log("heartbeat failed: %v", err)
		return client.AgentSession{}, false
	}
	return a, true
}

// beatAndLog is heartbeat plus a one-line trace of the resulting state. It
// returns the server's view of the session (zero when the beat failed) so a
// caller can act on what the server said — see noticeResult.
func (r *runner) beatAndLog(hb client.SessionHeartbeat) client.AgentSession {
	a, ok := r.heartbeat(hb)
	if !ok {
		return client.AgentSession{}
	}
	r.log("state=%s relay=%s wait=%s wait_detail=%s", a.State, a.RelayMode, a.WaitReason, a.WaitReasonDetail)
	return a
}

// noticeResult turns the server's `notice` into the hook's result: a session that
// was taken over on the web (§9.1 B) is announced to the person at THIS terminal,
// and nothing is blocked (the takeover process owns the conversation now).
func noticeResult(a client.AgentSession) Result {
	if a.Notice == "" {
		return Result{}
	}
	return Result{Notice: a.Notice}
}

// stop is the relay main path (design §7): while the server says this session
// waits — the human's explicit switch, the keyboard idle rule, or (on a
// terminal that cannot be probed at all) the time since their last input (R2) —
// the agent's last message becomes a turn and the hook blocks here until the
// human answers on the web.
func (r *runner) stop() Result {
	last := r.lastMessage()
	if ObserveOnly(r.p.dialect()) {
		// The agent runs this hook detached: report the stop (state + last message)
		// and return — there is nothing to block and no way to inject a reply.
		r.beatAndLog(client.SessionHeartbeat{Event: r.p.Event, LastMessage: last, ClearProgress: true})
		return Result{}
	}
	a, ok := r.heartbeat(client.SessionHeartbeat{
		Event: r.p.Event, LastMessage: last, IdleSec: idleSecPtr(), ClearProgress: true,
	})
	if !ok {
		return Result{}
	}
	// The session belongs to a takeover job now (§9.1 B): say so at this terminal
	// and let the agent stop. Checked BEFORE the wait rules — this is not a wait,
	// it is a handoff.
	if a.Notice != "" {
		r.log("session handed off: %s", a.Notice)
		return Result{Notice: a.Notice}
	}
	reason := a.WaitReason
	if reason == "" {
		// Not a relay wait (switch off, SUP-01 D supervision gate, or the auto rules
		// did not arm). The web-input relay stays off, but pending job watches still
		// need a delivery channel: once the Stop returns and the session idles,
		// nothing else could push a job-completion event in.
		if a.WatchCount > 0 {
			return r.waitJobsOnly(a)
		}
		if a.WaitReasonDetail != "" {
			r.log("relay off, released: %s but no watched job to wait for", a.WaitReasonDetail)
		} else {
			r.log("relay off, released")
		}
		return Result{}
	}
	// probeArmed marks the wait that exists only because the human is away AND
	// that the hook itself can end: it re-reads the keyboard every poll and lets
	// the server release the turn. An explicit switch is the human's to turn off
	// (typing in the terminal, or /off), and the turn-age fallback has no reading
	// to re-probe — its release arrives as a human-input event instead (Esc /
	// typing, handled server-side on UserPromptSubmit / Interrupt).
	probeArmed := reason == client.WaitIdleProbe
	// The server may cap the wait (wait_budget_sec: session.relay_on_wait_sec for
	// `on`, relay_auto_wait_sec for an auto-armed wait); an older server sends none.
	wait := r.opts.Wait
	if a.WaitBudgetSec > 0 {
		if budget := time.Duration(a.WaitBudgetSec) * time.Second; budget < wait {
			r.log("wait capped by server budget: %s (--wait %s)", budget, wait)
			wait = budget
		}
	}
	turn, err := r.api.OpenSessionTurn(r.p.SessionID, last, int64(wait/time.Second))
	if err != nil {
		r.log("open turn failed: %v", err) // 409 = relay flipped off in between
		return Result{}
	}
	watched := []WatchedJob(nil)
	if a.WatchCount > 0 {
		watched = r.watchedJobs()
		if completed := r.releaseWatchedJobs(turn.ID, watched); len(completed) > 0 {
			return Result{Blocked: true, Reason: mergeWatchedTerminals(completed)}
		}
	}
	pollSec := r.opts.PollSec
	if len(watched) > 0 && pollSec > 5 {
		pollSec = 5
	}
	if probeArmed && pollSec > autoArmPollSec {
		// The human's return can only be noticed at a poll boundary, so an
		// idle-armed wait re-reads the keyboard more often than a switched-on one.
		pollSec = autoArmPollSec
	}
	r.log("turn %s open (reason=%s), waiting up to %s", turn.ID, reason, wait)
	deadline := r.opts.now().Add(wait)
	failures := 0
	for {
		remaining := time.Until(deadline)
		if r.opts.now().After(deadline) || remaining <= 0 {
			break
		}
		wait := pollSec
		if rs := int(remaining / time.Second); rs < wait {
			wait = rs
		}
		if wait < 1 {
			wait = 1
		}
		st, err := r.api.WaitSessionTurn(r.p.SessionID, turn.ID, wait)
		if err != nil {
			failures++
			r.log("wait failed (%d/%d): %v", failures, transientRetries, err)
			if failures >= transientRetries || client.StatusOf(err) == 404 {
				return Result{}
			}
			r.opts.sleep(transientBackoff)
			continue
		}
		failures = 0
		if a.WatchCount > 0 {
			watched = r.watchedJobs()
			if completed := r.releaseWatchedJobs(turn.ID, watched); len(completed) > 0 {
				return Result{Blocked: true, Reason: mergeWatchedTerminals(completed)}
			}
		}
		switch st.Outcome {
		case "answered":
			reply := strings.TrimSpace(st.Decision.Answer)
			if strings.EqualFold(reply, OffCommand) {
				// /off means "stop waiting altogether", i.e. the explicit off mode (not
				// the automatic rules) — the legacy boolean form said the same thing.
				if _, err := r.api.SetSessionRelayMode(r.p.SessionID, client.RelayModeOff); err != nil {
					r.log("relay off failed: %v", err)
				}
				r.log("answered /off, released")
				return Result{}
			}
			r.log("answered (%d bytes), continuing", len(reply))
			return Result{Blocked: true, Reason: ReplyPrefix + reply}
		case "expired", "relay_off":
			r.log("turn %s, released", st.Outcome)
			return Result{}
		}
		if probeArmed && r.releasedOnUserReturn(turn.ID) {
			return Result{}
		}
	}
	r.log("wait budget exhausted, released")
	_, _ = r.api.HeartbeatSession(r.p.SessionID, client.SessionHeartbeat{Event: r.p.Event, State: "idle"})
	return Result{}
}

// waitJobsOnly is the Stop path of a session that does NOT relay web input but
// still has pending job watches: block (within the --wait budget) until a watched
// job reaches a terminal state, then hand its "[gofer job 完成]" notice to the
// agent as the block reason. No turn is opened, so nothing reaches the web and a
// web reply is never injected. Released when every watch is gone (delivered or
// removed) or the budget runs out; a still-pending watch is then caught up by
// the next SessionStart / UserPromptSubmit.
func (r *runner) waitJobsOnly(a client.AgentSession) Result {
	r.log("relay not waiting (mode=%s) but %d watched job(s) pending (%s), waiting up to %s for job events only",
		a.RelayMode, a.WatchCount, strings.TrimSpace(a.WaitReasonDetail), r.opts.Wait)
	pollSec := r.opts.PollSec
	if pollSec > autoArmPollSec {
		pollSec = autoArmPollSec
	}
	deadline := r.opts.now().Add(r.opts.Wait)
	failures := 0
	for {
		rows, err := r.api.ListSessionJobWatches(r.p.SessionID)
		if err != nil {
			failures++
			r.log("list job watches failed (%d/%d): %v", failures, transientRetries, err)
			if failures >= transientRetries || client.StatusOf(err) == 404 {
				return Result{}
			}
		} else {
			failures = 0
			jobs := watchedFromRows(rows)
			if len(jobs) == 0 {
				r.log("no watched jobs left, released")
				return Result{}
			}
			if done := r.deliverTerminal(jobs); len(done) > 0 {
				r.log("watched job(s) finished while relay off, delivering %d notice(s)", len(done))
				return Result{Blocked: true, Reason: mergeWatchedTerminals(done)}
			}
		}
		remaining := deadline.Sub(r.opts.now())
		if remaining <= 0 {
			break
		}
		step := time.Duration(pollSec) * time.Second
		if remaining < step {
			step = remaining
		}
		r.opts.sleep(step)
		if !r.opts.now().Before(deadline) {
			break
		}
	}
	r.log("wait budget exhausted with watched jobs still pending, released")
	return Result{}
}

// catchUp injects the notices of watched jobs that already finished but were never
// delivered (the Stop that should have carried them was released, or the session
// was idle when they ended) as additional context on SessionStart /
// UserPromptSubmit. Only for agents whose hook output reaches the model (claude,
// codex): omp's extension discards it and jcode runs detached, so consuming the
// watch there would lose the notice.
func (r *runner) catchUp(res Result, a client.AgentSession) Result {
	if a.WatchCount == 0 || !CatchUpAgent(r.p.dialect()) {
		return res
	}
	rows, err := r.api.ListSessionJobWatches(r.p.SessionID)
	if err != nil {
		r.log("catch-up list job watches failed: %v", err)
		return res
	}
	done := r.deliverTerminal(watchedFromRows(rows))
	if len(done) == 0 {
		return res
	}
	r.log("catch-up: injecting %d undelivered job notice(s)", len(done))
	res.Context = mergeWatchedTerminals(done)
	return res
}

// CatchUpAgent reports whether the agent's SessionStart / UserPromptSubmit hook
// output (hookSpecificOutput.additionalContext) is fed to the model.
func CatchUpAgent(dialect string) bool {
	return dialect == AgentClaude || dialect == AgentCodex || dialect == DialectGeneric
}

// deliverTerminal acks the terminal jobs among jobs (no relay turn involved) and
// returns them when this hook won the delivery.
func (r *runner) deliverTerminal(jobs []WatchedJob) []WatchedJob {
	term := make([]WatchedJob, 0, len(jobs))
	ids := make([]string, 0, len(jobs))
	for _, j := range jobs {
		if isTerminalStatus(j.Status) {
			term = append(term, j)
			ids = append(ids, j.ID)
		}
	}
	if len(term) == 0 {
		return nil
	}
	ok, err := r.api.CompleteSessionWatches(r.p.SessionID, ids)
	if err != nil || !ok {
		r.log("ack watched jobs failed: completed=%v err=%v", ok, err)
		return nil
	}
	return term
}

func watchedFromRows(rows []client.SessionJobWatch) []WatchedJob {
	out := make([]WatchedJob, 0, len(rows))
	for _, row := range rows {
		out = append(out, WatchedJob{ID: row.JobID, Title: row.Title, Status: row.Status,
			ExitCode: row.ExitCode, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
			Duration: time.Duration(row.Duration) * time.Second})
	}
	return out
}

func (r *runner) watchedJobs() []WatchedJob {
	rows, err := r.api.ListSessionJobWatches(r.p.SessionID)
	if err != nil {
		r.log("list job watches failed: %v", err)
		return nil
	}
	out := make([]WatchedJob, 0, len(rows))
	for _, row := range rows {
		out = append(out, WatchedJob{ID: row.JobID, Title: row.Title, Status: row.Status,
			ExitCode: row.ExitCode, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
			Duration: time.Duration(row.Duration) * time.Second})
	}
	return out
}

func (r *runner) releaseWatchedJobs(turnID string, jobs []WatchedJob) []WatchedJob {
	term := make([]WatchedJob, 0, len(jobs))
	for _, item := range jobs {
		if !isTerminalStatus(item.Status) {
			continue
		}
		term = append(term, item)
	}
	if len(term) == 0 {
		return nil
	}
	ids := make([]string, 0, len(term))
	for _, item := range term {
		ids = append(ids, item.ID)
	}
	completed, err := r.api.CompleteSessionWatchTurn(r.p.SessionID, turnID, ids)
	if err != nil || !completed {
		r.log("complete watched turn failed: completed=%v err=%v", completed, err)
		return nil
	}
	return term
}

func isTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "failed", "cancelled", "canceled", "timeout", "rejected", "expired":
		return true
	default:
		return false
	}
}

// releasedOnUserReturn reports this round's keyboard reading to the server,
// which owns the threshold and decides whether it means "the human is back"
// (then it closes the turn with released_by=user_returned). A hub that cannot
// answer is simply ignored: the wait continues.
func (r *runner) releasedOnUserReturn(turnID string) bool {
	sec := idleSeconds()
	released, err := r.api.ReleaseSessionTurn(r.p.SessionID, turnID, sec)
	if err != nil {
		r.log("release probe failed: %v", err)
		return false
	}
	if released {
		r.log("user returned (idle %ds), release confirmed", sec)
	}
	return released
}

// lastMessage picks the agent's last assistant text: Codex hands it over on
// stdin, Claude keeps it in the transcript.
func (r *runner) lastMessage() string {
	if msg := strings.TrimSpace(r.p.LastAssistantMessage); msg != "" {
		return truncate(msg, r.opts.MaxMessage)
	}
	if r.p.TranscriptPath != "" && r.p.dialect() != DialectGeneric {
		text, err := LastAssistantText(r.p.TranscriptPath, r.opts.MaxMessage)
		if err != nil {
			r.log("transcript read failed: %v", err)
		} else if text != "" {
			return text
		}
	}
	return noMessageFallback
}

// isHarnessInput is the single verdict for "this UserPromptSubmit is not a
// human typing": the agent flagged it injected (generic hook), or the prompt
// text looks harness-generated (IsHarnessPrompt).
func isHarnessInput(p Payload) bool {
	return p.Injected || IsHarnessPrompt(p.Prompt)
}

// IsHarnessPrompt reports whether a UserPromptSubmit prompt was generated by
// the agent harness rather than typed by a human: empty, our own injected
// reply (the Stop continuation, or a reply path A typed into the terminal with
// no turn behind it — §9.1 A), or tagged system content (`<task-notification>`,
// `<system-reminder>`, anything starting with an XML-ish tag).
func IsHarnessPrompt(prompt string) bool {
	t := strings.TrimSpace(prompt)
	switch {
	case t == "":
		return true
	case strings.HasPrefix(t, strings.TrimSpace(ReplyPrefix)):
		return true
	case strings.HasPrefix(t, JobDoneTag):
		return true
	case len(t) > 1 && t[0] == '<' && (t[1] == '/' || (t[1] >= 'a' && t[1] <= 'z') || (t[1] >= 'A' && t[1] <= 'Z')):
		return true // XML-ish tag such as <task-notification> / <system-reminder>
	case strings.Contains(t, "<task-notification>"), strings.Contains(t, "<system-reminder>"):
		return true
	}
	return false
}

// head returns the first n runes of s on one line (log helper).
func head(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}

// makeTitle names a session from its cwd and the first prompt:
// "<cwd base>: <first line of prompt, ≤40 runes>".
func makeTitle(cwd, prompt string) string {
	base := filepath.Base(strings.TrimRight(cwd, "/\\"))
	if base == "." || base == "" || base == string(filepath.Separator) {
		base = cwd
	}
	line := strings.TrimSpace(prompt)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if utf8.RuneCountInString(line) > 40 {
		rs := []rune(line)
		line = string(rs[:40]) + "…"
	}
	if line == "" {
		return base
	}
	return base + ": " + line
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// BlockJSON renders the Stop-hook continuation both Claude Code and Codex
// understand (legacy `decision` form): the reason becomes the next prompt.
func BlockJSON(reason string) []byte {
	b, _ := json.Marshal(map[string]string{"decision": "block", "reason": reason})
	return b
}
