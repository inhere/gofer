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

// TargetAttachments is FileXferRequest.Target's value for a put into the executing
// machine's OWN attachment directory (<config-dir>/run/attachments/<date>/) instead of a
// project tree. The wire carries the same literal (wsproto.FileXferTargetAttachments):
// the assembly adapter converts one into the other explicitly, so neither side has to
// import the other (G022).
const TargetAttachments = "attachments"

// FileXferRequest is one transfer instruction for a worker. It deliberately
// mirrors the wire frame instead of sharing it, so this package stays free of the
// wire protocol (wsproto is imported only by the hub/worker and the assembly
// adapter that converts one into the other).
type FileXferRequest struct {
	XferID     string
	Op         string
	ProjectKey string
	// Target names the base Path is relative to: empty = the project ProjectKey names,
	// TargetAttachments = the worker's own attachment directory (no project at all).
	Target string
	Path   string
	Size   int64
	SHA256 string
	Force  bool
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
	// AbsPath is the absolute destination the worker reports for a put (empty for a
	// get, and for a worker below wsproto.XferAbsPathMinProtocolVersion).
	AbsPath string
}

// WorkerSender sends one transfer instruction to a worker and waits for its
// result. It is the seam the hub satisfies (through an adapter) and the one
// tests stub; a nil sender means "this deployment cannot reach workers".
type WorkerSender interface {
	SendFileXfer(ctx context.Context, workerID string, req FileXferRequest) (FileXferResult, error)
}

// WorkerCapabilities is the optional query half of WorkerSender: it answers which
// optional capabilities a CONNECTED worker declared at registration. A sender that cannot
// answer (a stub in a test, a deployment with no hub) does not implement it at all, and
// the caller then treats that worker as lacking the capability — refusing a placement is
// always safer than sending a frame the peer silently drops.
type WorkerCapabilities interface {
	// SupportsWorkerAttach reports whether workerID accepts a transfer into its own
	// attachment directory (wsproto.SupportsWorkerAttach of the version it registered).
	SupportsWorkerAttach(workerID string) bool
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
func (r *Router) Run(ctx context.Context, rec jobstore.XferRecord) (RunResult, error) {
	if config.NormalizeRunnerName(rec.Runner) == config.BuiltinLocalRunner {
		if r.Local == nil {
			return RunResult{}, errors.New("server runner is not available")
		}
		return r.Local.Run(ctx, rec)
	}
	if r.Sender == nil {
		return RunResult{}, errors.New("worker transfers are not available")
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
		Target:     rec.Target,
		Path:       rec.Path,
		Size:       rec.Size,
		SHA256:     rec.SHA256,
		Force:      rec.Force == 1,
		URLPath:    ContentPath(rec.ID),
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return RunResult{}, fmt.Errorf("worker xfer timed out after %s", timeout)
		}
		return RunResult{}, err
	}
	if !res.OK {
		if res.Error == ErrExists.Error() {
			return RunResult{}, ErrExists
		}
		if res.Error != "" {
			return RunResult{}, errors.New(res.Error)
		}
		return RunResult{}, errors.New("worker refused the transfer")
	}
	return RunResult{AbsPath: res.AbsPath}, nil
}

// TargetCapabilities is the optional query side of Runner: whether the executor a
// transfer names can take a placement into that machine's OWN attachment directory
// (TargetAttachments). The file-delivery layer asks it before it promises a
// project-less session that its images can be placed.
type TargetCapabilities interface {
	SupportsWorkerAttach(runner string) bool
}

// OwnAttachmentsSupported reports whether runner can take a put into its machine's own
// attachment directory: the builtin local runner always can (this process writes its own
// runtime directory), a worker only when it registered with the capability.
func (m *Manager) OwnAttachmentsSupported(runner string) bool {
	if config.NormalizeRunnerName(runner) == config.BuiltinLocalRunner {
		return true
	}
	caps, ok := m.runnerOrNil().(TargetCapabilities)
	if !ok {
		return false
	}
	return caps.SupportsWorkerAttach(runner)
}

// SupportsWorkerAttach implements TargetCapabilities for the router: the answer comes from
// the sender, which is the only side that knows what each connected worker declared.
func (r *Router) SupportsWorkerAttach(workerID string) bool {
	caps, ok := r.Sender.(WorkerCapabilities)
	if !ok {
		return false
	}
	return caps.SupportsWorkerAttach(workerID)
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
	runRes, runErr := runner.Run(ctx, rec)
	switch {
	case runErr == nil:
		// The far side's absolute destination is recorded BEFORE the transfer is settled
		// done: a caller that waits for `done` and then reads the row must find the path in
		// that same read (it is what the file-delivery message is built from). A recording
		// failure is logged, not fatal — the bytes are already in place.
		m.recordAbsPath(id, runRes.AbsPath)
		return m.MarkDone(id)
	case errors.Is(runErr, ErrExists):
		return m.MarkFailed(id, ErrExists.Error())
	case errors.Is(runErr, ErrNotFound):
		return m.MarkFailed(id, "source not found")
	default:
		return m.MarkFailed(id, runErr.Error())
	}
}

// recordAbsPath writes the executing machine's reported destination, best effort: a
// journal write failure must not undo a placement that succeeded.
func (m *Manager) recordAbsPath(id, absPath string) {
	if err := m.SetAbsPath(id, absPath); err != nil {
		slog.Warn("xfer.abs_path_failed", "event", "xfer.abs_path_failed", "component", "server",
			"xfer_id", id, "err", err)
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
	m.inflight.Add(1)
	go func() {
		defer m.inflight.Done()
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
		// A report that arrives after its waiter gave up still carries the destination, so
		// the journal ends up with the absolute path the file-delivery path needs.
		m.recordAbsPath(res.XferID, res.AbsPath)
		return m.MarkDone(res.XferID)
	}
	reason := res.Error
	if reason == "" {
		reason = "worker reported failure"
	}
	return m.MarkFailed(res.XferID, reason)
}
