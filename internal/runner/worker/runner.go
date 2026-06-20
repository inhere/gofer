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
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

// maxWSFrameBytes caps the text of a single mirrored log frame (review #3,
// aligned with the C4 SSE maxSSEFrameBytes). A larger frame is truncated with a
// marker. It is a var so tests can shrink it.
var maxWSFrameBytes = 1 << 20 // 1 MiB

// sinkTruncateMark is appended once when a job's mirrored output is truncated by
// back-pressure, so the reader sees that bytes were dropped (review #3).
const sinkTruncateMark = "\n[gofer: log frame truncated by worker back-pressure]\n"

// dispatcher is the subset of *wshub.Hub the runner uses. It is an interface so
// the runner's sink-lifecycle can be unit-tested with a fake (the production
// value is the concrete hub singleton). *wshub.Hub satisfies it.
type dispatcher interface {
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

// Runner forwards a job to a worker over the hub and returns the worker's
// authoritative terminal result.
type Runner struct {
	name     string
	workerID string
	hub      dispatcher
}

// New builds a worker runner named name that dispatches to workerID via hub.
func New(name, workerID string, hub *wshub.Hub) *Runner {
	return &Runner{name: name, workerID: workerID, hub: hub}
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

	sink := newBoundedSink(req.Stdout, req.Stderr)
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
		return runner.Result{ExitCode: -1, Err: err} // worker offline
	}
	defer r.hub.DeregisterSink(workerID, req.JobID) // (e)

	// (b) dispatch (runner is always local on the worker side).
	d := wsproto.Dispatch{
		JobID:      req.JobID,
		ProjectKey: f.ProjectKey,
		Agent:      f.Agent,
		Runner:     "local",
		Prompt:     f.Prompt,
		Cmd:        f.Cmd,
		Cwd:        f.Cwd,
		TimeoutSec: f.TimeoutSec,
	}
	if err := r.hub.Dispatch(workerID, d); err != nil {
		return runner.Result{ExitCode: -1, Err: err}
	}

	// (c)(d) wait for the worker's authoritative terminal result, a worker-lost
	// disconnect (§5.3) or ctx end.
	select {
	case res := <-sink.resultCh:
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
		// WP3 worker-lost (§5.3): the hub dropped the worker connection while this
		// job was in flight. Return a non-nil Err with NO ctx deadline/cancel, so
		// classify (service.go) maps it to StatusFailed and the "worker disconnected"
		// text flows verbatim into jobs.error. No cancel frame is sent — the worker
		// is gone. The deferred DeregisterSink frees the sink.
		return runner.Result{ExitCode: -1, Err: err}
	case <-ctx.Done():
		// P2: a host cancel/timeout forwards a cancel frame to the worker (best-effort,
		// same as peerhttp's r.c.CancelJob) so its local job tears down its child
		// process; the job service still classifies timeout vs cancelled from ctx. We
		// return ctx.Err() immediately without waiting for the worker's late result —
		// the deferred DeregisterSink frees the sink; a stray late result frame finds
		// no sink and is dropped.
		_ = r.hub.Cancel(workerID, req.JobID)
		return runner.Result{ExitCode: -1, Err: ctx.Err()}
	}
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

	mu        sync.Mutex
	truncated bool
	// outcome stashes the P4 worker-captured产出 frame, delivered just before the
	// terminal result frame (strict read-loop ordering). Run reads it after the
	// result lands and returns it on runner.Result.Outcome. nil when an old worker
	// sends no outcome frame (回归红线: host job outcome stays empty).
	outcome *wsproto.Outcome
}

func newBoundedSink(stdout, stderr io.Writer) *boundedSink {
	return &boundedSink{
		stdout:   stdout,
		stderr:   stderr,
		resultCh: make(chan wsproto.Result, 1),
		lostCh:   make(chan error, 1),
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
// (P4). It arrives strictly before Finish (the worker sends it just before the
// result frame, enforced by the hub's single in-order read loop), so Run reads
// s.outcome only after the result lands — no lock needed there beyond this write.
func (s *boundedSink) OnOutcome(o wsproto.Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := o
	s.outcome = &cp
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
