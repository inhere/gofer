// Package local implements the in-process (local host) runner. It executes the
// resolved command as a child process via exec.CommandContext and streams its
// stdout/stderr straight into the writers the job service provides. See plan
// §6.3 (Stdout/Stderr local-only convention) and §9 (P4).
package local

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"dev-agent-bridge/internal/runner"
)

// Name is the runner identifier ("local").
const Name = "local"

// Runner runs commands as local child processes.
type Runner struct{}

// New returns a local runner.
func New() *Runner { return &Runner{} }

// Name implements runner.Runner.
func (r *Runner) Name() string { return Name }

// Run executes req.Command with req.Args under ctx.
//
//   - The working directory is req.WorkDir (the job service passes a SafeJoin'd
//     absolute host path).
//   - The environment is the process env (os.Environ) with req.Env layered on
//     top, so agent-configured vars override inherited ones.
//   - Stdout/Stderr are wired directly to the provided writers (the job
//     service's stdout.log / stderr.log files) for live streaming.
//
// Exit handling: a zero exit yields ExitCode 0. A non-zero process exit is read
// from *exec.ExitError. A context cancel/timeout surfaces as a non-zero exit
// with Err set to ctx.Err(); the job service inspects the context reason to
// distinguish cancelled vs timeout. A failure to start the process returns a
// synthetic exit code (-1) with the start error.
func (r *Runner) Run(ctx context.Context, req runner.Request) runner.Result {
	cmd := exec.CommandContext(ctx, req.Command, req.Args...)
	cmd.Dir = req.WorkDir
	cmd.Env = mergedEnv(req.Env)
	cmd.Stdout = req.Stdout
	cmd.Stderr = req.Stderr

	err := cmd.Run()
	if err == nil {
		return runner.Result{ExitCode: 0}
	}

	// Prefer the context error so the job service can classify timeout/cancel
	// regardless of how the killed process surfaced (it usually appears as an
	// ExitError with a non-zero code on Linux).
	if ctxErr := ctx.Err(); ctxErr != nil {
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		return runner.Result{ExitCode: code, Err: ctxErr}
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// Process ran and exited non-zero; not a runner-level error.
		return runner.Result{ExitCode: exitErr.ExitCode()}
	}

	// Could not start (command not found, bad cwd, etc.).
	return runner.Result{ExitCode: -1, Err: err}
}

// mergedEnv returns os.Environ() with extra layered on top. extra entries
// override inherited ones with the same key.
func mergedEnv(extra map[string]string) []string {
	base := os.Environ()
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}
