package ptyrunner

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/pty"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/util"
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

// defaultInitialInputMaxWait bounds the priming wait (design §9.1 B): a TUI that
// is still painting 10s in will not go quiet, and the message must not be held
// hostage to it.
const defaultInitialInputMaxWait = 10 * time.Second

// defaultInitialInputQuietMs is the quiet window used when the request carries
// none (session.takeover_input_delay_ms is resolved by the job service, so this
// is only the last-resort default).
const defaultInitialInputQuietMs = 1500

// Available reports whether a pty backend is usable on this build/host. core
// registers PtyRunner only when true, so "pty runner present in the map" is the
// capability signal the job service keys on (it never calls pty.IsAvailable).
func Available() bool { return pty.IsAvailable() }

// SessionObserver is notified once, synchronously, right after a PtySession is
// registered and BEFORE PtyRunner.Run blocks on the child — the observer then
// becomes the SOLE reader of the session's pty output (design §11 / D-P2-3). The
// implementation MUST be non-blocking (only hand off / start a goroutine
// internally); a blocking OnSessionStart would stall PtyRunner.Run → job.execute.
type SessionObserver interface {
	OnSessionStart(jobID string, sess *PtySession)
}

// PtyRunner runs interactive jobs under a pty. It satisfies runner.Runner.
type PtyRunner struct {
	reg      *registry
	observer SessionObserver // nil = tests / pty-less attach disabled → keep the default discard drain
	// initialInputMaxWait bounds how long priming waits for the TUI to settle
	// (design §9.1 B). 0 = defaultInitialInputMaxWait; overridable in tests.
	initialInputMaxWait time.Duration
}

// New builds a PtyRunner with its own session registry.
func New() *PtyRunner { return &PtyRunner{reg: newRegistry()} }

// SetObserver injects the session observer. Worker uses it to pump remote
// interactive jobs to the hub; serve uses it to expose local interactive jobs via
// the same relay/attach stack. Leaving it nil keeps the default discard drain.
func (r *PtyRunner) SetObserver(o SessionObserver) { r.observer = o }

// SetInitialInputMaxWait overrides how long priming waits for a quiet terminal
// before writing anyway (design §9.1 B). Tests use it to prove the bounded path
// without a 10s wait; <= 0 restores the default.
func (r *PtyRunner) SetInitialInputMaxWait(d time.Duration) {
	if d > 0 {
		r.initialInputMaxWait = d
	}
}

func (r *PtyRunner) maxInitialInputWait() time.Duration {
	if r.initialInputMaxWait > 0 {
		return r.initialInputMaxWait
	}
	return defaultInitialInputMaxWait
}

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

	if r.observer != nil {
		// Hand the SOLE reader ownership to the observer (worker pty ws pump or
		// serve-local relay). No discard here — a second reader would steal bytes.
		// OnSessionStart is a synchronous, non-blocking contract (see SessionObserver);
		// Run does NOT spawn a fallback goroutine for a misbehaving observer.
		r.observer.OnSessionStart(req.JobID, sess)
	} else {
		// Tests / no observer: keep the pty drained so the slave side never blocks
		// on a full buffer (the P0 spike behaviour).
		go func() { _, _ = io.Copy(io.Discard, sess) }()
	}

	// Path B priming (design §9.1 B): the takeover job's first message is typed in
	// once the resumed TUI has drawn its prompt. The wait rides on Read's activity
	// clock, so this NEVER reads the pty itself (a second reader would steal the
	// relay's bytes). Cancelled here when Run returns, i.e. when the child is gone.
	if req.InitialInput != "" {
		primeCtx, cancelPrime := context.WithCancel(ctx)
		defer cancelPrime()
		go r.primeInitialInput(primeCtx, req, sess)
	}

	code, runErr := sess.run(ctx)
	return runner.Result{ExitCode: code, Err: runErr}
}

// primeInitialInput writes req.InitialInput to the child's stdin once the
// terminal has settled, and records what it did as a job event — a takeover that
// silently typed nothing into the resumed session must be visible in
// `gofer job events`, not inferred from the absence of an answer.
func (r *PtyRunner) primeInitialInput(ctx context.Context, req runner.Request, sess *PtySession) {
	quiet := time.Duration(req.InitialInputQuietMs) * time.Millisecond
	if req.InitialInputQuietMs < 0 {
		quiet = 0
	} else if req.InitialInputQuietMs == 0 {
		quiet = defaultInitialInputQuietMs * time.Millisecond
	}
	if !sess.WaitOutputQuiet(ctx, quiet, r.maxInitialInputWait()) {
		return // the session ended / was cancelled before the wait resolved
	}
	n, err := sess.WriteInput([]byte(req.InitialInput))
	detail := map[string]any{"bytes": len(req.InitialInput), "written": n, "quiet_ms": req.InitialInputQuietMs}
	if err != nil {
		detail["error"] = err.Error()
	}
	if req.OnJobEvent != nil {
		req.OnJobEvent(runner.EventInputInjected, detail)
	}
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
		Env:     util.Environ(req.Env),
		Dir:     req.WorkDir,
		Cols:    cols,
		Rows:    rows,
	})
	if err != nil {
		return nil, err
	}
	return newSession(req.JobID, p), nil
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
