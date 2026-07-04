package ptyrunner

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/inhere/gofer/internal/pty"
	"github.com/inhere/gofer/internal/runner"
)

// Name is the runner identifier the job service routes interactive jobs to. It
// is registered into the runners map (like "local") ONLY when Available() so the
// job package can select it by key without importing this package (G024).
const Name = "pty"

// default initial terminal size when the request carries none.
const (
	defaultCols = 80
	defaultRows = 24
)

// Available reports whether a pty backend is usable on this build/host. core
// registers PtyRunner only when true, so "pty runner present in the map" is the
// capability signal the job service keys on (it never calls pty.IsAvailable).
func Available() bool { return pty.IsAvailable() }

// PtyRunner runs interactive jobs under a pty. It satisfies runner.Runner.
type PtyRunner struct {
	reg *registry
}

// New builds a PtyRunner with its own session registry.
func New() *PtyRunner { return &PtyRunner{reg: newRegistry()} }

// Name implements runner.Runner.
func (r *PtyRunner) Name() string { return Name }

// Sessions exposes the live session registry (P1+ relay wiring / cancel; the
// spike uses it to assert a running job's session is discoverable).
func (r *PtyRunner) Sessions() *registry { return r.reg }

// Run starts req under a pty, registers the PtySession and blocks until the
// child exits (or ctx cancels → ordered teardown). Output is drained off the pty
// (in P1+ it feeds the serve relay/cast — design §11 "pty output只入 cast/attach",
// NOT stdout.log — so req.Stdout is intentionally not wired here).
func (r *PtyRunner) Run(ctx context.Context, req runner.Request) runner.Result {
	sess, err := r.start(req)
	if err != nil {
		return runner.Result{ExitCode: -1, Err: err}
	}
	r.reg.add(req.JobID, sess)
	defer r.reg.remove(req.JobID)

	// Drain output so the pty's slave side never blocks on a full buffer. The
	// spike discards it; P1 replaces this sink with the relay recorder.
	go func() { _, _ = io.Copy(io.Discard, sess) }()

	code, runErr := sess.run(ctx)
	return runner.Result{ExitCode: code, Err: runErr}
}

// start builds the pty Spec from the runner.Request and starts the pty.
func (r *PtyRunner) start(req runner.Request) (*PtySession, error) {
	cols, rows := req.Cols, req.Rows
	if cols <= 0 {
		cols = defaultCols
	}
	if rows <= 0 {
		rows = defaultRows
	}
	p, err := pty.Start(pty.Spec{
		Command: req.Command,
		Args:    req.Args,
		Env:     mergedEnv(req.Env),
		Dir:     req.WorkDir,
		Cols:    cols,
		Rows:    rows,
	})
	if err != nil {
		return nil, err
	}
	return newSession(req.JobID, p), nil
}

// mergedEnv returns os.Environ() with extra layered on top (parity with
// local.Runner: agent/job env overrides inherited vars). Empty extra => nil so
// the pty backend inherits os.Environ directly.
func mergedEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return nil
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// registry maps a job id to its live PtySession. It is the session-registry seam
// the design keeps parallel to jobEntry (NOT inside jobEntry): the job package
// never holds a session, keeping fd ownership here (G024 / design §5).
type registry struct {
	mu sync.Mutex
	m  map[string]*PtySession
}

func newRegistry() *registry { return &registry{m: map[string]*PtySession{}} }

func (r *registry) add(jobID string, s *PtySession) {
	r.mu.Lock()
	r.m[jobID] = s
	r.mu.Unlock()
}

func (r *registry) remove(jobID string) {
	r.mu.Lock()
	delete(r.m, jobID)
	r.mu.Unlock()
}

// Lookup returns the live session for jobID, if any.
func (r *registry) Lookup(jobID string) (*PtySession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[jobID]
	return s, ok
}
