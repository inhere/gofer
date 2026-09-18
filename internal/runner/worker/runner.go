// Package worker implements the ws-worker host-side runner (main plan §4,
// internal/runner/worker). It is the third execution location after local and
// peer-http: instead of running a child process (local) or forwarding over HTTP
// (peer-http), it dispatches the job to a remote worker over the hub WebSocket
// and mirrors the worker's log frames back into the host job's stdout.log /
// stderr.log so /logs, /stream and list stay transparently usable.
//
// The runner is constructed by commands.buildCore with the hub singleton; one
// worker-runner targets one configured worker_id (dynamic routing is WP4).
//
// Import note: this package's import path is internal/runner/worker; callers
// alias it (e.g. workerrunner) to avoid clashing with internal/worker (the
// client side).
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inhere/gofer/internal/ptyrelay"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

// maxWSFrameBytes caps the text of a single mirrored log frame (review #3,
// aligned with the C4 SSE maxSSEFrameBytes). A larger frame is truncated with a
// marker. It is a var so tests can shrink it.
var maxWSFrameBytes = 1 << 20 // 1 MiB

// hostCancelGrace bounds how long an INTERACTIVE Run waits for the serve relay to
// finish draining (relayRegistry.Done) after a worker result / a host cancel,
// before finishing anyway (D-P2-6). Done is a pre-closed chan for pending/missing
// relays, so this grace only ever elapses for a genuinely stuck open relay. It is
// a var (not const) so tests can shrink it to exercise the fallback without a real
// 10s wait; the stage/request timeout must stay > hostCancelGrace (§待办).
var hostCancelGrace = 10 * time.Second

// sinkTruncateMark is appended once when a job's mirrored output is truncated by
// back-pressure, so the reader sees that bytes were dropped (review #3).
const sinkTruncateMark = "\n[gofer: log frame truncated by worker back-pressure]\n"

// dispatcher is the subset of *wshub.Hub the runner uses. It is an interface so
// the runner's sink-lifecycle can be unit-tested with a fake (the production
// value is the concrete hub singleton). *wshub.Hub satisfies it.
type dispatcher interface {
	LiveInstance(workerID string) (instanceID string, ok bool)
	// WorkerProtocol reports the wire protocol version the target worker registered
	// with (ok=false when offline), for negotiation of additive dispatch fields.
	WorkerProtocol(workerID string) (proto int, ok bool)
	RegisterSink(workerID, jobID string, sk wshub.JobSink) error
	DeregisterSink(workerID, jobID string)
	Dispatch(workerID string, d wsproto.Dispatch) error
	// Answer sends the host-side answer of a worker interaction back over WS so
	// the worker's local job resumes (P2).
	Answer(workerID, jobID, interactionID, answer string) error
	// Cancel sends a cancel frame to the worker for jobID (P2, best-effort on a
	// host ctx cancel/timeout).
	Cancel(workerID, jobID string) error
}

type nonceIssuer interface {
	Issue(ptyrelay.NonceBinding) string
}

type relayPreparer interface {
	Prepare(ptyrelay.RelayBinding) *ptyrelay.RelayEntry
	// Done reports the serve-drain completion signal for jobID (T2 registry): a
	// live relay's recordLoop-EOF chan, or a pre-closed chan for pending/finalized/
	// missing (nothing to drain). Run waits on it before finishing an interactive
	// job so the browser sees the pty tail bytes (D-P2-2/6).
	Done(jobID string) <-chan struct{}
	Close(jobID, reason string)
}

// Runner forwards a job to a worker over the hub and returns the worker's
// authoritative terminal result.
type Runner struct {
	name          string
	workerID      string
	hub           dispatcher
	nonceStore    nonceIssuer
	relayRegistry relayPreparer
	nowUnix       func() int64
}

// Option configures a Runner.
type Option func(*Runner)

// WithPtyRelay injects the serve-side relay lifecycle dependencies used by
// interactive worker dispatches.
func WithPtyRelay(nonces nonceIssuer, relays relayPreparer) Option {
	return func(r *Runner) {
		r.nonceStore = nonces
		r.relayRegistry = relays
	}
}

// New builds a worker runner named name that dispatches to workerID via hub.
func New(name, workerID string, hub *wshub.Hub, opts ...Option) *Runner {
	r := &Runner{name: name, workerID: workerID, hub: hub, nowUnix: func() int64 { return time.Now().Unix() }}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Name implements runner.Runner.
func (r *Runner) Name() string { return r.name }

// Run dispatches req.Forward to the worker and returns the worker's terminal
// result. Lifecycle (review #2):
//
//	(a) register a per-job sink on the hub BEFORE dispatch (so the worker's first
//	    log frame is never lost);
//	(b) send the dispatch frame;
//	(c) the hub's read loop mirrors inbound log frames into the sink (=
//	    req.Stdout/Stderr) and delivers the terminal result;
//	(d) wait for the result (or ctx end);
//	(e) deregister the sink.
func (r *Runner) Run(ctx context.Context, req runner.Request) runner.Result {
	f := req.Forward
	if f == nil {
		return runner.Result{ExitCode: -1, Err: errors.New("worker runner requires forward request")}
	}

	// Resolve the target worker (P2 dynamic routing): the Submit-resolved
	// Forward.WorkerID (explicit or label-selected) takes precedence; if empty fall
	// back to the runner's configured default worker (D4 — the legacy "one runner
	// binds one worker" config). A runner with neither has no target.
	workerID := f.WorkerID
	if workerID == "" {
		workerID = r.workerID
	}
	if workerID == "" {
		return runner.Result{ExitCode: -1, Err: errors.New("worker runner: no target worker_id")}
	}

	var relayPrepared bool
	var relayNonce string
	var ptySessionID string
	relayCloseReason := "runner_returned"
	if f.Interactive {
		if r.nonceStore == nil || r.relayRegistry == nil {
			return runner.Result{ExitCode: -1, Err: errors.New("worker runner: pty relay dependencies not configured")}
		}
		instanceID, ok := r.hub.LiveInstance(workerID)
		if !ok {
			return runner.Result{ExitCode: -1, Err: wshub.ErrWorkerOffline}
		}
		ptySessionID = newPtySessionID(req.JobID)
		expiry := r.nowUnix() + relayNonceTTLSeconds
		relayNonce = r.nonceStore.Issue(ptyrelay.NonceBinding{
			WorkerID:     workerID,
			InstanceID:   instanceID,
			JobID:        req.JobID,
			PtySessionID: ptySessionID,
			Expiry:       expiry,
		})
		r.relayRegistry.Prepare(ptyrelay.RelayBinding{
			WorkerID:     workerID,
			InstanceID:   instanceID,
			JobID:        req.JobID,
			PtySessionID: ptySessionID,
			Nonce:        relayNonce,
			Expiry:       expiry,
			Cols:         f.Cols, // D-P3-2: initial window for the cast header (0 = sink default)
			Rows:         f.Rows,
		})
		relayPrepared = true
	}
	defer func() {
		if relayPrepared {
			r.relayRegistry.Close(req.JobID, relayCloseReason)
		}
	}()

	sink := newBoundedSink(req.Stdout, req.Stderr, req.OnRendered)
	// RECOV-01: the hub holds this job in `recovering` while the worker's connection
	// is down (Suspend) and returns it to `running` when the same worker process
	// proves it still runs it (Resume). Both are nil for a local job.
	sink.onSuspend = req.OnSuspend
	sink.onResume = req.OnResume
	// Wire the interaction bridge: an inbound interaction{open} is injected onto
	// the host job (via req.Interactions, the same remoteInteractionSink peer-http
	// uses) and the host-side answer is sent back over WS (hub.Answer). Mirrors
	// peerhttp.handleFrame exactly, swapping "POST answer" for "WS answer".
	sink.bridge = &interactionBridge{
		ctx:     ctx,
		sinks:   req.Interactions,
		answer:  func(iid, ans string) { _ = r.hub.Answer(workerID, req.JobID, iid, ans) },
		seen:    map[string]bool{},
		jobID:   req.JobID,
		hasSink: req.Interactions != nil,
	}

	// (a) sink-before-dispatch.
	if err := r.hub.RegisterSink(workerID, req.JobID, sink); err != nil {
		relayCloseReason = "register_failed"
		return runner.Result{ExitCode: -1, Err: err} // worker offline
	}
	defer r.hub.DeregisterSink(workerID, req.JobID) // (e)

	// RECOV-01 R4: the target is now FINAL and the connection is proven live, so the
	// host row can record which worker process owns this job. Reported BEFORE the
	// dispatch frame so a crash between the two never leaves the job running on a
	// worker the row cannot name (which would make it unadoptable after a restart).
	// LiveInstance can only fail for an offline worker, and RegisterSink just proved
	// the opposite, so an empty instance id here means a legacy worker connection
	// with no instance — reported as-is (the row is then correctly never adopted).
	if req.OnDispatchedWorker != nil {
		instanceID, _ := r.hub.LiveInstance(workerID)
		req.OnDispatchedWorker(workerID, instanceID)
	}

	// (b) dispatch (runner is always local on the worker side).
	d := wsproto.Dispatch{
		JobID:             req.JobID,
		ProjectKey:        f.ProjectKey,
		Agent:             f.Agent,
		Runner:            "local",
		Prompt:            f.Prompt,
		AgentArgs:         f.AgentArgs,
		SystemPrompt:      f.SystemPrompt,
		Cmd:               f.Cmd,
		Cwd:               f.Cwd,
		Worktree:          f.Worktree,     // WT-01: created on the worker (below)
		WorktreeBase:      f.WorktreeBase, // WT-01: base ref, empty = that checkout's HEAD
		TimeoutSec:        f.TimeoutSec,
		Interactive:       f.Interactive,
		Cols:              f.Cols,
		Rows:              f.Rows,
		ResumeSourceAgent: f.ResumeSourceAgent,
		RelayNonce:        relayNonce,
		PtySessionID:      ptySessionID, // T1: worker echoes it in pty-connect hello for serve-side check
		// bd h-aii-0ql3: the worker applies the read-only mode with its own agent config
		// (and its own admission, which refuses the job if that agent has no mode).
		ReadOnly: f.ReadOnly,
		// Session relay §9.1 B: the worker starts the pty, so path B's priming text
		// and the quiet window the hub resolved travel with the dispatch.
		InitialInput:        f.InitialInput,
		InitialInputQuietMs: f.InitialInputQuietMs,
		// SUP-01 C: display-only on the worker — the todo itself is the hub's, so the
		// worker shows the id and links nothing (see JobRequest.TodoForeign).
		TodoID: f.TodoID,
	}
	// ACP-01 S2: a continuation carries its session + lineage so the worker's local
	// job resolves the same session/load. Set ONLY for a resume — a plain job's
	// session_id is not a continuation and never travels here.
	if f.ResumedFrom != "" {
		d.SessionID = f.SessionID
		d.ResumedFrom = f.ResumedFrom
	}
	// The dispatch fields above are additive, so a worker built before
	// wsproto.SessionLoadMinProtocolVersion IGNORES them: it opens a fresh session for
	// a resume and runs a read-only job writable. The hub cannot fix that — the
	// worker's own code decides — so it records what will actually happen instead of
	// failing a job whose only defect is the peer's vintage.
	if f.ResumedFrom != "" || f.ReadOnly {
		if proto, ok := r.hub.WorkerProtocol(workerID); ok && !wsproto.SupportsSessionLoad(proto) {
			slog.Warn("worker runner: target worker predates the resume/read-only dispatch fields; it will open a new session / run writable",
				"worker_id", workerID, "worker_proto", proto, "job_id", req.JobID,
				"resume", f.ResumedFrom != "", "read_only", f.ReadOnly)
		}
	}
	// Same negotiation for path B's priming: a worker below v7 silently drops the
	// fields, so the takeover's first message never reaches the resumed TUI. Say so
	// per dispatch — failing the job would not make the worker type anything.
	if f.InitialInput != "" {
		if proto, ok := r.hub.WorkerProtocol(workerID); ok && !wsproto.SupportsInitialInput(proto) {
			slog.Warn("worker runner: target worker predates the initial-input dispatch fields; the takeover message will not be typed into the session",
				"worker_id", workerID, "worker_proto", proto, "job_id", req.JobID)
		}
	}
	if err := r.hub.Dispatch(workerID, d); err != nil {
		relayCloseReason = "dispatch_failed"
		return runner.Result{ExitCode: -1, Err: err}
	}

	// (c)(d) wait for the worker's authoritative terminal result, a worker-lost
	// disconnect (§5.3) or ctx end.
	select {
	case res := <-sink.resultCh:
		relayCloseReason = "worker_result"
		// D-P2-6 (interactive only): the worker has finished, but its pty tail may
		// still be draining through the serve relay to the browser. Wait for the
		// relay's drain-complete signal (recordLoop EOF) — bounded by hostCancelGrace
		// — before returning, so the terminal Result never truncates the visible
		// output. Done is a pre-closed chan for a pending/missing relay, so a
		// non-attached interactive job proceeds at once. Non-interactive is untouched
		// (returns immediately — bytes unchanged, G023).
		if f.Interactive {
			select {
			case <-r.relayRegistry.Done(req.JobID):
			case <-time.After(hostCancelGrace):
			}
		}
		// P4: attach the worker-captured产出 (delivered just before this result via
		// OnOutcome). Source marks it ran on this worker so the详情 can标注 it (大
		// 产物文件留 worker 侧, only清单+小结果回传 — D6). nil when the worker is old
		// and sent no outcome frame (host job outcome then stays empty —回归红线).
		return runner.Result{
			ExitCode: res.ExitCode,
			Err:      ResultErr(res),
			Outcome:  OutcomeFrom(sink.takeOutcome(), workerID),
		}
	case err := <-sink.lostCh:
		relayCloseReason = "worker_lost"
		// WP3 worker-lost (§5.3): the hub dropped the worker connection while this
		// job was in flight. Return a non-nil Err with NO ctx deadline/cancel, so
		// classify (service.go) maps it to StatusFailed and the "worker disconnected"
		// text flows verbatim into jobs.error. No cancel frame is sent — the worker
		// is gone. The deferred DeregisterSink frees the sink. (Interactive is NOT
		// waited here — the worker is already gone, nothing more will drain.)
		return runner.Result{ExitCode: -1, Err: err}
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			relayCloseReason = "ctx_timeout"
		} else {
			relayCloseReason = "cancelled"
		}
		// P2: a host cancel/timeout forwards a cancel frame to the worker (best-effort,
		// same as peerhttp's r.c.CancelJob) so its local job tears down its child
		// process; the job service still classifies timeout vs cancelled from ctx.
		_ = r.hub.Cancel(workerID, req.JobID)
		// D-P2-6 (interactive only): three-way wait so the browser sees the pty tail
		// the worker emits while tearing down (e.g. a cancel-triggered sentinel).
		// Resolve on whichever comes first: the relay drained (Done), the worker
		// dropped (lostCh), or the grace elapsed. Non-interactive returns immediately
		// (unchanged截尾 semantics, G023).
		if f.Interactive {
			select {
			case <-r.relayRegistry.Done(req.JobID):
			case <-sink.lostCh:
			case <-time.After(hostCancelGrace):
			}
		}
		// G1: no outcome is attached on timeout/cancel here. The rendered command was
		// already applied to the RUNNING host entry the moment the worker reported it
		// (req.OnRendered → boundedSink.onRendered), so a timed-out/cancelled job keeps
		// it without stamping a worker Source (which a non-completed job must not have).
		return runner.Result{ExitCode: -1, Err: ctx.Err()}
	}
}

const relayNonceTTLSeconds = 60

func newPtySessionID(jobID string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	if jobID == "" {
		return "pty-" + hex.EncodeToString(b[:])
	}
	return jobID + "-pty-" + hex.EncodeToString(b[:])
}

// OutcomeFrom projects a worker-sent Outcome frame onto a runner.Outcome,
// stamping the Source as worker:<id> so the host job records WHERE it ran. It is
// exported because the serve-side ADOPTION path (RECOV-01 R4, internal/core) must
// apply exactly the same projection to a frame that arrives for a job this process
// did not dispatch — one mapping, two entry points.
// stamping Source="worker:<id>" so the host job records WHERE it ran (P4-c). A
// nil frame (old worker, no产出回传) yields nil so the host job outcome stays
// empty (回归红线). Artifacts is the raw清单 JSON the worker already serialised —
// passed through verbatim (大产物文件本身留 worker 侧, D6).
func OutcomeFrom(o *wsproto.Outcome, workerID string) *runner.Outcome {
	if o == nil {
		return nil
	}
	return &runner.Outcome{
		RenderedCommand: o.RenderedCommand,
		ResultJSON:      o.ResultJSON,
		DiffSummary:     o.DiffSummary,
		Artifacts:       o.Artifacts,
		Source:          "worker:" + workerID,
		SessionID:       o.SessionID, // worker 本地捕获/注入的 agent 会话标识 (P3)
		// WT-01: the worker owns the worktree paths it reports; pass them through so the
		// host row shows where the deliverable branch lives (empty for an old worker).
		WorktreePath:    o.WorktreePath,
		WorktreeBranch:  o.WorktreeBranch,
		WorktreeBaseSHA: o.WorktreeBaseSHA,
		WorktreeHeadSHA: o.WorktreeHeadSHA,
		CommitsAhead:    o.CommitsAhead,
		// SUP-01 C: the worker captured the commits on ITS checkout, so they travel
		// with the outcome like the worktree state does.
		BaseSHA: o.BaseSHA,
		Commits: commitsFromFrame(o.Commits),
	}
}

// commitsFromFrame copies the frame's commit list onto the runner's own Commit
// type (wsproto stays a leaf and defines its own, like Outcome.Artifacts).
func commitsFromFrame(in []wsproto.Commit) []runner.Commit {
	if len(in) == 0 {
		return nil
	}
	out := make([]runner.Commit, 0, len(in))
	for _, c := range in {
		out = append(out, runner.Commit{SHA: c.SHA, Subject: c.Subject})
	}
	return out
}

// ResultErr maps a worker terminal result to a runner error (mirrors
// peerhttp.errFromStatus): done → nil; failed/timeout/cancelled or a non-terminal
// status → an error so the host job service classifies the job. The host still
// inspects its own ctx, so cancel/timeout stay correct. Exported for the RECOV-01 R4
// adoption path (internal/core), which finishes a job from the same wire frame.
func ResultErr(res wsproto.Result) error {
	switch res.Status {
	case "done":
		return nil
	case "failed", "timeout", "cancelled":
		if res.Error != "" {
			return errors.New(res.Error)
		}
		return errors.New("worker job " + res.Status)
	default:
		if res.Error != "" {
			return errors.New(res.Error)
		}
		return errors.New("worker job not terminal: " + res.Status)
	}
}

// boundedSink mirrors the worker's log frames into the host job's stdout/stderr
// writers (the same store.LogWriter files the local runner uses, so /logs +
// SSE work unchanged) and delivers the terminal result.
//
// Back-pressure (review #3, WP1 baseline): WriteLog caps each frame's text to
// maxWSFrameBytes (appending a one-time truncation marker) and writes
// synchronously to the file (FileStore append is fast). It does NOT spawn a
// per-frame goroutine — that would break the hub's in-order delivery (review #2).
// A stronger per-job async bounded buffer is a P3 enhancement (design §15 TODO).
type boundedSink struct {
	stdout, stderr io.Writer
	resultCh       chan wsproto.Result // buffered 1: first result wins
	lostCh         chan error          // buffered 1: worker-lost wakes Run (§5.3)
	bridge         *interactionBridge  // P2 interaction passthrough (nil-safe)
	// onRendered (nil-safe) pushes the worker's rendered command onto the RUNNING
	// host job entry the moment it arrives (G1), so `job show`/web reflect WHAT is
	// running immediately — not only at completion. Fired at most once.
	onRendered func(string)
	// onSuspend / onResume (nil-safe) drive the host job's `recovering` state
	// (RECOV-01): the hub calls Suspend when the worker connection dropped but the
	// job is being held, Resume when the same worker process proved it still runs it.
	onSuspend func(reason string)
	onResume  func()

	mu              sync.Mutex
	truncated       bool
	renderedApplied bool // onRendered fired (guards the once semantics)
	// delivered latches the terminal result: Finish delivers at most one result per
	// job even if the worker replays it (RECOV-01).
	delivered atomic.Bool
	// stdoutOff / stderrOff count the bytes this sink has DURABLY written to each
	// stream. They are the SERVER-side offsets a resuming worker is rewound to
	// (RECOV-01): a frame that fails to write never advances them, so "what the host
	// has" and "what the worker may re-send from" can never drift apart.
	stdoutOff, stderrOff int64
	// outcome stashes the latest P4 worker-captured产出 frame, delivered just before
	// the terminal result frame (strict read-loop ordering). Run reads it after the
	// result lands and returns it on runner.Result.Outcome. nil when an old worker
	// sends no outcome frame (回归红线: host job outcome stays empty).
	outcome *wsproto.Outcome
}

func newBoundedSink(stdout, stderr io.Writer, onRendered func(string)) *boundedSink {
	return &boundedSink{
		stdout:     stdout,
		stderr:     stderr,
		resultCh:   make(chan wsproto.Result, 1),
		lostCh:     make(chan error, 1),
		onRendered: onRendered,
	}
}

// WriteLog implements wshub.JobSink: it writes text to the matching stream
// writer, capping oversize frames and appending a one-time truncation marker.
//
// It also counts the bytes actually written per stream (RECOV-01): those offsets
// are the authoritative server-side positions a resuming worker is rewound to, so
// the count must reflect ONLY what landed (a short/failed write advances it by what
// the writer took, and no write at all advances nothing).
func (s *boundedSink) WriteLog(stream string, _ int, text string) {
	if text == "" {
		return
	}
	w := s.stdout
	stderrStream := stream == "stderr"
	if stderrStream {
		w = s.stderr
	}
	if w == nil {
		return
	}
	if len(text) > maxWSFrameBytes {
		text = text[:maxWSFrameBytes]
		s.mu.Lock()
		first := !s.truncated
		s.truncated = true
		s.mu.Unlock()
		n, _ := io.WriteString(w, text)
		if first {
			n2, _ := io.WriteString(w, sinkTruncateMark)
			n += n2
		}
		s.addOffset(stderrStream, int64(n))
		return
	}
	n, _ := io.WriteString(w, text)
	s.addOffset(stderrStream, int64(n))
}

// addOffset advances the per-stream mirrored-byte counter by n (n <= 0 is a no-op:
// a failed write must never move the resume point forward).
func (s *boundedSink) addOffset(stderr bool, n int64) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	if stderr {
		s.stderrOff += n
	} else {
		s.stdoutOff += n
	}
	s.mu.Unlock()
}

// Suspend implements wshub.JobSink (RECOV-01): the worker connection dropped while
// this job was in flight and the hub is holding it for a possible reconnect. It
// moves the host job to `recovering` and returns the byte counts the host has
// durably written, which the hub echoes in the resume ack.
func (s *boundedSink) Suspend(reason string) (int64, int64) {
	s.mu.Lock()
	out, errOut := s.stdoutOff, s.stderrOff
	s.mu.Unlock()
	if s.onSuspend != nil {
		s.onSuspend(reason)
	}
	return out, errOut
}

// Resume implements wshub.JobSink (RECOV-01): the same worker process came back
// and still runs this job, so the host job returns to `running`.
func (s *boundedSink) Resume() {
	if s.onResume != nil {
		s.onResume()
	}
}

// OnInteraction implements wshub.JobSink: it forwards one worker interaction frame
// to the interaction bridge (nil-safe — a job with no host interaction sink simply
// ignores it). The call is non-blocking on the hub's read loop: the bridge only
// records/injects synchronously and spawns the WaitAnswer wait in its own goroutine.
func (s *boundedSink) OnInteraction(action string, interaction json.RawMessage) {
	if s.bridge != nil {
		s.bridge.handle(action, interaction)
	}
}

// OnOutcome implements wshub.JobSink: it stashes the worker-captured产出 frame
// (P4), latest-wins, and — on the first frame carrying a rendered command — pushes
// it onto the running host entry via onRendered (G1: WHAT ran is visible while the
// job runs, not only at completion). The worker sends it twice: an early
// rendered-command-only frame right after the job starts, then the full frame just
// before the terminal result. Run reads the stash after the result lands
// (takeOutcome); the mutex guards the stash + the fire-once flag.
func (s *boundedSink) OnOutcome(o wsproto.Outcome) {
	s.mu.Lock()
	cp := o
	s.outcome = &cp
	fire := o.RenderedCommand != "" && !s.renderedApplied && s.onRendered != nil
	if fire {
		s.renderedApplied = true
	}
	s.mu.Unlock()
	// Invoke outside the sink lock: onRendered locks the host job entry (a different
	// mutex) — keep the two lock domains disjoint.
	if fire {
		s.onRendered(o.RenderedCommand)
	}
}

// takeOutcome returns the stashed outcome frame (nil when the worker sent none).
func (s *boundedSink) takeOutcome() *wsproto.Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcome
}

// Finish implements wshub.JobSink: it delivers the terminal result, non-blocking
// and EXACTLY ONCE (a duplicate result is dropped with a debug line).
//
// Recovery (RECOV-01) makes the duplicate case reachable: a worker that finished a
// job while its connection was down replays the Result after reconnecting (and the
// design relies on that replay being idempotent). A job must never be finished twice
// — the second Finish would re-enter the host job's terminal path (a second event,
// a second workflow advance attempt) for a job that is already terminal.
func (s *boundedSink) Finish(res wsproto.Result) {
	if !s.delivered.CompareAndSwap(false, true) {
		slog.Debug("worker.result_duplicate", "event", "worker.result_duplicate", "component", "server",
			"job_id", res.JobID, "status", res.Status, "reason", "result already delivered")
		return
	}
	select {
	case s.resultCh <- res:
	default:
	}
}

// OnDisconnect implements wshub.JobSink: it wakes the Run wait with a worker-lost
// error (§5.3), non-blocking. If a real result already landed (resultCh full or
// drained), Run will have selected the result first — the buffered lostCh value
// is then simply never read (the deferred DeregisterSink GC's the sink), so a
// disconnect arriving after a completed job never overrides its true outcome.
func (s *boundedSink) OnDisconnect(err error) {
	select {
	case s.lostCh <- err:
	default:
	}
}

// wireInteraction mirrors job.Interaction's wire shape for decoding the raw
// interaction body off the WS frame. It is declared here (not imported from job)
// to keep the runner cycle-free: job imports runner, so runner must project the
// interaction onto runner.RemoteInteraction itself.
type wireInteraction struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Prompt  string `json:"prompt"`
	Options []struct {
		Value string `json:"value"`
		Label string `json:"label,omitempty"`
		ID    string `json:"id,omitempty"`
		Kind  string `json:"kind,omitempty"`
	} `json:"options,omitempty"`
	// ToolCall / PolicyHint carry a permission interaction's approval detail
	// (GATE-01 §1); absent on every other interaction type.
	ToolCall *struct {
		ID              string   `json:"id"`
		Title           string   `json:"title,omitempty"`
		Kind            string   `json:"kind,omitempty"`
		Locations       []string `json:"locations,omitempty"`
		RawInputSummary string   `json:"raw_input_summary,omitempty"`
	} `json:"tool_call,omitempty"`
	PolicyHint string `json:"policy_hint,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
}

// interactionBridge bridges worker-raised interactions onto the host job. It is
// the WS analogue of peerhttp.handleFrame's interaction branch: an open frame
// injects the interaction via the host InteractionSink (-> pending_interaction)
// and a goroutine waits for the host answer to send it back over WS. seen dedupes
// a re-sent open (e.g. a worker resend) so the answer is forwarded only once.
type interactionBridge struct {
	ctx     context.Context
	sinks   runner.InteractionSink
	answer  func(interactionID, answer string)
	jobID   string
	hasSink bool

	mu   sync.Mutex
	seen map[string]bool
}

// handle processes one interaction frame. Only action "open" drives the bridge
// (matching peer-http): answered/cancelled are state-cleanup actions the host
// already owns via its own interaction record, so they are accepted and ignored
// (forward-compatible per P2 §3.1). An unparseable body is dropped.
func (b *interactionBridge) handle(action string, raw json.RawMessage) {
	if !b.hasSink || action != "open" {
		return
	}
	var wi wireInteraction
	if err := json.Unmarshal(raw, &wi); err != nil || wi.ID == "" {
		return
	}

	b.mu.Lock()
	if b.seen[wi.ID] {
		b.mu.Unlock()
		return
	}
	b.seen[wi.ID] = true
	b.mu.Unlock()

	opts := make([]runner.RemoteInteractionOption, 0, len(wi.Options))
	for _, o := range wi.Options {
		opts = append(opts, runner.RemoteInteractionOption{Value: o.Value, Label: o.Label, ID: o.ID, Kind: o.Kind})
	}
	var tc *runner.RemoteInteractionToolCall
	if wi.ToolCall != nil {
		tc = &runner.RemoteInteractionToolCall{
			ID:              wi.ToolCall.ID,
			Title:           wi.ToolCall.Title,
			Kind:            wi.ToolCall.Kind,
			Locations:       wi.ToolCall.Locations,
			RawInputSummary: wi.ToolCall.RawInputSummary,
		}
	}
	ansCh, err := b.sinks.Open(b.ctx, runner.RemoteInteraction{
		ID:         wi.ID,
		Type:       wi.Type,
		Prompt:     wi.Prompt,
		Options:    opts,
		ToolCall:   tc,
		PolicyHint: wi.PolicyHint,
		ExpiresAt:  wi.ExpiresAt,
	})
	if err != nil {
		return
	}
	iid := wi.ID
	go func() {
		// The host answer arrives on ansCh; forward it to the worker so its local
		// job resumes. If the channel closes without a value (job ended / ctx
		// cancelled), do nothing (no answer to forward).
		if ans, ok := <-ansCh; ok {
			b.answer(iid, ans)
		}
	}()
}
