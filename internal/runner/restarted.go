package runner

import "errors"

// ErrWorkerRestarted is what a remote run ends with when the worker PROCESS that ran the
// job is gone because it restarted (a new process instance registered under the same
// worker id), as opposed to a connection that dropped or a recovery window that elapsed.
// Nothing the old process was running survived; a persistent session may be reloaded on the
// new process (session/load), so the job service tells this apart from any other failure.
var ErrWorkerRestarted = errors.New("worker restarted")

// workerRestartedError carries the readable text of a worker-restart failure and still
// matches ErrWorkerRestarted.
type workerRestartedError struct{ msg string }

func (e *workerRestartedError) Error() string { return e.msg }

func (e *workerRestartedError) Is(target error) bool { return target == ErrWorkerRestarted }

// NewWorkerRestartedError wraps err's text so that errors.Is(result, ErrWorkerRestarted)
// holds while the message stays exactly what a failed job would have shown.
func NewWorkerRestartedError(err error) error {
	if err == nil {
		return nil
	}
	return &workerRestartedError{msg: err.Error()}
}
