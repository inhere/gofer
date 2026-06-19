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

	sink := newBoundedSink(req.Stdout, req.Stderr)

	// (a) sink-before-dispatch.
	if err := r.hub.RegisterSink(r.workerID, req.JobID, sink); err != nil {
		return runner.Result{ExitCode: -1, Err: err} // worker offline
	}
	defer r.hub.DeregisterSink(r.workerID, req.JobID) // (e)

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
	if err := r.hub.Dispatch(r.workerID, d); err != nil {
		return runner.Result{ExitCode: -1, Err: err}
	}

	// (c)(d) wait for the worker's authoritative terminal result, or ctx end.
	select {
	case res := <-sink.resultCh:
		return runner.Result{ExitCode: res.ExitCode, Err: errFromResult(res)}
	case <-ctx.Done():
		// WP1: a host cancel/timeout returns immediately; forwarding a cancel frame
		// to the worker is P2. The job service classifies timeout vs cancelled from
		// ctx (same as peer-http).
		return runner.Result{ExitCode: -1, Err: ctx.Err()}
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

	mu        sync.Mutex
	truncated bool
}

func newBoundedSink(stdout, stderr io.Writer) *boundedSink {
	return &boundedSink{stdout: stdout, stderr: stderr, resultCh: make(chan wsproto.Result, 1)}
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

// Finish implements wshub.JobSink: it delivers the terminal result, non-blocking
// (a duplicate result is dropped).
func (s *boundedSink) Finish(res wsproto.Result) {
	select {
	case s.resultCh <- res:
	default:
	}
}
