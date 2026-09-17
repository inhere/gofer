package httpapi

import (
	"context"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/store"
)

// injectWaitSec is how long the HTTP request waits for the injection job before
// reporting it as still running. It is deliberately BELOW the CLI client's 30s
// HTTP timeout (and below the job's own 30s deadline) so the caller always gets
// a real answer instead of a client-side timeout racing the server.
const injectWaitSec = 25

// sessionInjector adapts the job service to the relay's injection seam (design
// §9.1 A): path A's internal exec job is an ordinary job — it runs on the
// session's own runner, in its project, under its timeout — so it shows up in
// `gofer job ls --tag relay-inject` with full logs, like any other work.
//
// It exists here, in the entry/assembly layer, so the relay stays job-agnostic
// (G021/G022): the relay decides WHAT to type and reads the exit code; this type
// owns the submission vocabulary.
type sessionInjector struct {
	jobs *job.Service
}

// InjectSession runs one injection job to completion and reports its exit code
// plus the stdout tail (the pane check writes its reason code there).
func (x sessionInjector) InjectSession(_ context.Context, req sessionrelay.InjectRequest) (sessionrelay.InjectResult, error) {
	out, async, err := x.jobs.SubmitSync(job.JobRequest{
		ProjectKey: req.ProjectKey,
		Agent:      agent.ExecAgentKey,
		Runner:     runnerKeyForSession(req.Runner),
		Cmd:        req.Cmd,
		Cwd:        req.Cwd,
		Title:      req.Title,
		Tags:       req.Tags,
		TimeoutSec: req.TimeoutSec,
		// The wait must finish inside the CLI client's 30s HTTP budget (the job's own
		// deadline is 30s): stop waiting a little earlier and report the still-running
		// job as a failure carrying its id — the job finishes on its own and
		// `gofer job show` tells the truth (never a silent double-injection).
		WaitTimeoutSec: injectWaitSec,
	}, true)
	if err != nil {
		return sessionrelay.InjectResult{}, err
	}
	res := sessionrelay.InjectResult{JobID: out.ID, ExitCode: out.ExitCode}
	if b, lerr := x.jobs.TailLog(out.ID, store.StreamStdout, 4096); lerr == nil {
		res.Output = string(b)
	}
	// A job that never ran (rejected/queued/timeout/cancelled) or one that ran
	// under a non-done status must never read as "the pane got the text". The
	// runner reports -1 for a job it could not start; keep any real exit code.
	if async || out.Status != job.StatusDone {
		if res.ExitCode == 0 {
			res.ExitCode = -1
		}
		if res.Output == "" {
			res.Output = out.Error
		} else if out.Error != "" {
			res.Output = out.Error + "\n" + res.Output
		}
	}
	return res, nil
}

// runnerKeyForSession maps a registered session's runner label to a job runner
// key: the server's own label ("server", or empty on an old registration) means
// its built-in local runner; a configured runner id (a worker's key) passes
// through, and the job service resolves/rejects it like any other submit.
func runnerKeyForSession(runner string) string {
	switch strings.TrimSpace(runner) {
	case "", "server":
		return "local"
	default:
		return runner
	}
}
