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
	"sync"
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
		TimeoutSec:        f.TimeoutSec,
		Interactive:       f.Interactive,
		Cols:              f.Cols,
		Rows:              f.Rows,
		ResumeSourceAgent: f.ResumeSourceAgent,
		RelayNonce:        relayNonce,
		PtySessionID:      ptySessionID, // T1: worker echoes it in pty-connect hello for serve-side check
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
			Err:      errFromResult(res),
			Outcome:  outcomeFrom(sink.takeOutcome(), workerID),
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

// outcomeFrom projects a worker-sent Outcome frame onto a runner.Outcome,
// stamping Source="worker:<id>" so the host job records WHERE it ran (P4-c). A
// nil frame (old worker, no产出回传) yields nil so the host job outcome stays
// empty (回归红线). Artifacts is the raw清单 JSON the worker already serialised —
// passed through verbatim (大产物文件本身留 worker 侧, D6).
func outcomeFrom(o *wsproto.Outcome, workerID string) *runner.Outcome {
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
	}
}

// errFromResult maps a worker terminal result to a runner error (mirrors
// peerhttp.errFromStatus): done → nil; failed/timeout/cancelled or a non-terminal
// status → an error so the host job service classifies the job. The host still
// inspects its own ctx, so cancel/timeout stay correct.
func errFromResult(res wsproto.Result) error {
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

	mu              sync.Mutex
	truncated       bool
	renderedApplied bool // onRendered fired (guards the once semantics)
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
func (s *boundedSink) WriteLog(stream string, _ int, text string) {
	if text == "" {
		return
	}
	w := s.stdout
	if stream == "stderr" {
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
		_, _ = io.WriteString(w, text)
		if first {
			_, _ = io.WriteString(w, sinkTruncateMark)
		}
		return
	}
	_, _ = io.WriteString(w, text)
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
// (a duplicate result is dropped).
func (s *boundedSink) Finish(res wsproto.Result) {
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
	} `json:"options,omitempty"`
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
		opts = append(opts, runner.RemoteInteractionOption{Value: o.Value, Label: o.Label})
	}
	ansCh, err := b.sinks.Open(b.ctx, runner.RemoteInteraction{
		ID:      wi.ID,
		Type:    wi.Type,
		Prompt:  wi.Prompt,
		Options: opts,
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
