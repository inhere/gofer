// Package runner abstracts where a job's process executes. The MVP ships only
// the local runner (internal/runner/local); peer-http and docker-exec runners
// arrive in P7. See plan §6.3 and §9 (P4).
package runner

import (
	"context"
	"io"
)

// Runner executes one resolved command and reports how it ended.
type Runner interface {
	// Name returns the runner's stable identifier (e.g. "local").
	Name() string
	// Run executes req under ctx and returns the exit code and any error.
	Run(ctx context.Context, req Request) Result
}

// Request is a fully resolved, ready-to-execute command for a runner.
//
// IMPORTANT — Stdout/Stderr are a LOCAL-RUNNER-ONLY streaming convention.
// The local runner wires these io.Writers directly to the child process so the
// job service can stream output straight into stdout.log / stderr.log as it is
// produced. Remote runners (peer-http / docker-exec, added in P7) do NOT use
// this pair: they forward the request to a remote bridge and the authoritative
// logs/result are obtained by polling the remote /v1/jobs API, not through
// these writers. Do not assume all runners share the streaming semantics.
type Request struct {
	JobID   string
	WorkDir string // absolute host dir; the job service supplies a SafeJoin'd path
	Command string
	Args    []string
	Env     map[string]string // agent-config env, layered over the process env
	Stdout  io.Writer         // local-only: child stdout sink (see type doc)
	Stderr  io.Writer         // local-only: child stderr sink (see type doc)
}

// Result is the outcome of a single Run. ExitCode is the process exit status
// (or a synthetic non-zero when the process could not start / was killed); Err
// carries the underlying error when execution failed or the context ended. The
// job service maps ExitCode/Err plus the context reason to a job status.
type Result struct {
	ExitCode int
	Err      error
}
