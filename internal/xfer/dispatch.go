package xfer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// DefaultWorkerTimeout bounds one transfer on a worker (worker.xfer_timeout_sec
// overrides it). A transfer that cannot finish in the window is failed rather
// than left dispatched forever: nothing here is resumable, so a stalled worker
// (or a worker that died mid-file) must not hold a record open.
const DefaultWorkerTimeout = 10 * time.Minute

// FileXferRequest is one transfer instruction for a worker. It deliberately
// mirrors the wire frame instead of sharing it, so this package stays free of the
// wire protocol (wsproto is imported only by the hub/worker and the assembly
// adapter that converts one into the other).
type FileXferRequest struct {
	XferID     string
	Op         string
	ProjectKey string
	Path       string
	Size       int64
	SHA256     string
	Force      bool
	// URLPath is the server-relative content URL the worker fetches (put) or
	// uploads to (get).
	URLPath string
}

// FileXferResult is a worker's report for one transfer.
type FileXferResult struct {
	XferID     string
	OK         bool
	Size       int64
	SHA256     string
	Error      string
	DurationMS int64
}

// WorkerSender sends one transfer instruction to a worker and waits for its
// result. It is the seam the hub satisfies (through an adapter) and the one
// tests stub; a nil sender means "this deployment cannot reach workers".
type WorkerSender interface {
	SendFileXfer(ctx context.Context, workerID string, req FileXferRequest) (FileXferResult, error)
}

// Router picks the executor for a staged transfer from its runner name: the
// server's own machine (`local`, the canonical spelling of `server`) is executed
// in-process, anything else is a worker.
type Router struct {
	// Local executes a transfer against the server's own project roots. nil means
	// the local runner is not wired (a `server:` target then fails cleanly).
	Local Runner
	// Sender reaches workers. nil means no worker can be reached.
	Sender WorkerSender
	// Timeout bounds one worker transfer; 0 = DefaultWorkerTimeout.
	Timeout time.Duration
}

// Run implements Runner.
func (r *Router) Run(ctx context.Context, rec jobstore.XferRecord) error {
	if config.NormalizeRunnerName(rec.Runner) == config.BuiltinLocalRunner {
		if r.Local == nil {
			return errors.New("server runner is not available")
		}
		return r.Local.Run(ctx, rec)
	}
	if r.Sender == nil {
		return errors.New("worker transfers are not available")
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultWorkerTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := r.Sender.SendFileXfer(ctx, rec.Runner, FileXferRequest{
		XferID:     rec.ID,
		Op:         rec.Op,
		ProjectKey: rec.ProjectKey,
		Path:       rec.Path,
		Size:       rec.Size,
		SHA256:     rec.SHA256,
		Force:      rec.Force == 1,
		URLPath:    ContentPath(rec.ID),
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("worker xfer timed out after %s", timeout)
		}
		return err
	}
	if !res.OK {
		if res.Error == ErrExists.Error() {
			return ErrExists
		}
		if res.Error != "" {
			return errors.New(res.Error)
		}
		return errors.New("worker refused the transfer")
	}
	return nil
}

// ContentPath is the server-relative URL of a transfer's payload: the endpoint a
// worker downloads a put's source from and uploads a get's result to.
func ContentPath(id string) string { return "/v1/xfer/" + id + "/content" }

// Deliver drives one staged transfer to a terminal state: it marks the record
// dispatched, runs it through the configured Router and settles it done/failed
// from the outcome. A record that is not staged is left untouched, so a duplicate
// dispatch (a retried HTTP call, a re-delivered result) is a no-op.
func (m *Manager) Deliver(ctx context.Context, id string) error {
	rec, ok, err := m.repo.GetXfer(id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if State(rec.State) != StateStaged {
		return nil
	}
	if err := m.repo.UpdateXferState(id, string(StateDispatched), "", 0); err != nil {
		return err
	}
	runner := m.runnerOrNil()
	if runner == nil {
		return m.MarkFailed(id, "xfer: no runner wired")
	}
	runErr := runner.Run(ctx, rec)
	switch {
	case runErr == nil:
		return m.MarkDone(id)
	case errors.Is(runErr, ErrExists):
		return m.MarkFailed(id, ErrExists.Error())
	case errors.Is(runErr, ErrNotFound):
		return m.MarkFailed(id, "source not found")
	default:
		return m.MarkFailed(id, runErr.Error())
	}
}

// Dispatch runs Deliver in the background so the HTTP request that created the
// transfer returns as soon as the payload is staged; the client follows progress
// through GET /v1/xfer/{id}. Without a runner wired (mcp, tests) it is a no-op:
// the record stays staged rather than being failed for a deployment that simply
// does not execute transfers.
func (m *Manager) Dispatch(ctx context.Context, id string) {
	if m.runnerOrNil() == nil {
		return
	}
	go func() {
		if err := m.Deliver(ctx, id); err != nil {
			slog.Warn("xfer.deliver_failed", "event", "xfer.deliver_failed", "component", "server",
				"xfer_id", id, "err", err)
		}
	}()
}

// OnWorkerResult applies a worker result that arrived with no waiter (a frame for
// a transfer whose dispatch already gave up, or a duplicate). It only settles a
// record that is still dispatched — a settled transfer is never reopened.
func (m *Manager) OnWorkerResult(res FileXferResult) error {
	rec, ok, err := m.repo.GetXfer(res.XferID)
	if err != nil {
		return err
	}
	if !ok || State(rec.State) != StateDispatched {
		return nil
	}
	if res.OK {
		return m.MarkDone(res.XferID)
	}
	reason := res.Error
	if reason == "" {
		reason = "worker reported failure"
	}
	return m.MarkFailed(res.XferID, reason)
}
