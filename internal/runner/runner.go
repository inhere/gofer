// Package runner abstracts where a job's process executes. The MVP ships the
// local runner (internal/runner/local); the peer-http runner
// (internal/runner/peerhttp), added in P7, forwards a job to a peer bridge.
// See plan §6.3, §9 (P4) and §11.1 (P7).
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

// Request is a ready-to-execute command for a runner.
//
// Stdout/Stderr meaning differs by runner:
//   - The LOCAL runner wires these io.Writers directly to the child process so
//     the job service can stream output straight into stdout.log / stderr.log as
//     it is produced.
//   - REMOTE runners (peer-http, P7) do NOT resolve a local command. They
//     re-submit the original request (carried in Forward) to a peer bridge and
//     MIRROR the peer's log stream back into these same writers, so the local
//     job's stdout.log / stderr.log (and thus /logs, /stream and list) stay
//     transparently usable for the proxied job. Command/Args/WorkDir are unset
//     for remote runners (the peer resolves them with its own config).
//
// Forward is nil for local jobs (the local runner ignores it) and set by the
// job service for remote-runner jobs.
type Request struct {
	JobID   string
	WorkDir string // absolute host dir; the job service supplies a SafeJoin'd path
	Command string
	Args    []string
	Env     map[string]string // agent-config env, layered over the process env
	Stdout  io.Writer         // child stdout sink / mirrored remote stdout (see type doc)
	Stderr  io.Writer         // child stderr sink / mirrored remote stderr (see type doc)

	// Forward carries the original (pre-resolution) request a remote runner
	// re-submits to a peer bridge. Nil for local jobs.
	Forward *Forward
}

// Forward carries the original (pre-resolution) request a remote runner needs to
// re-submit to a peer bridge (peer resolves agent/cwd/command with its own
// config). Nil for local jobs; the local runner ignores it.
type Forward struct {
	ProjectKey string
	Agent      string
	PeerRunner string // runner to use on the peer; default "local"
	Prompt     string
	Cmd        []string
	Cwd        string // ORIGINAL relative cwd; peer SafeJoins against ITS project
	TimeoutSec int
}

// Result is the outcome of a single Run. ExitCode is the process exit status
// (or a synthetic non-zero when the process could not start / was killed); Err
// carries the underlying error when execution failed or the context ended. The
// job service maps ExitCode/Err plus the context reason to a job status.
type Result struct {
	ExitCode int
	Err      error
}
