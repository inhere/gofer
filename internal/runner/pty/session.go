// Package ptyrunner is the worker-side runner variant for interactive
// (pty-attached) jobs (WEB-03 design §5/§6, D1). PtyRunner satisfies
// runner.Runner and is selected by the job service ONLY for interactive jobs
// (submit routing), so a plain job keeps running on the local runner unchanged
// (G023). Run starts a pty child via internal/pty, wraps it in a PtySession
// state machine, registers the session, and blocks until the child exits.
//
// The package imports internal/pty (backend) + internal/runner (the interface);
// it does NOT import internal/job (dependency stays one-way, G022/G024): the job
// package reaches PtyRunner only through the runner.Runner interface via the
// runners map, and NEVER holds the PtySession — fd ownership/teardown lives here.
package ptyrunner

import (
	"context"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/pty"
)

// Session state names (design §5 state machine). Kept as strings so the test
// hook can assert an exact transition + teardown-step ORDER.
const (
	StateStarting   = "starting"
	StateRunning    = "running"
	StateCancelling = "cancelling" // cancel requested; teardown about to run
	StateExiting    = "exiting"    // ordered close sequence executing
	StateClosed     = "closed"
)

// Teardown step hook labels (design §5 close order:
// stop input → close master/kill child → wait child → publish exit → closed).
const (
	stepStopInput   = "close:stop-input"
	stepCloseMaster = "close:close-master"
	stepKillChild   = "close:kill-child"
	stepWaitChild   = "close:wait-child"
	stepWaitTimeout = "close:wait-timeout" // grace expired before reap
	stepPublishExit = "close:publish-exit"
)

// defaultGrace bounds how long teardown waits for the killed child to be reaped
// before proceeding with a synthetic exit (design §5 "bounded grace").
const defaultGrace = 5 * time.Second

// PtySession is one pty execution's handle + state machine. It owns the master
// fd lifecycle: teardown (Close on the pty + reap) runs here in a fixed order,
// exactly once (unified CAS close). The job's finish never closes the fd — it
// only observes the terminal result via Run's return (design §5 "finish 不拥有 fd
// close").
type PtySession struct {
	jobID string
	p     pty.Pty
	grace time.Duration

	// onEvent is an optional test hook recording state transitions + teardown
	// steps in order. nil in production.
	onEvent func(string)

	mu           sync.Mutex
	state        string
	inputStopped bool
	exitCode     int
	exitErr      error

	childExited chan struct{} // closed by the wait goroutine after cmd reaped
	teardownOne sync.Once
	done        chan struct{} // closed when the session reaches StateClosed
}

func newSession(jobID string, p pty.Pty) *PtySession {
	return &PtySession{
		jobID:       jobID,
		p:           p,
		grace:       defaultGrace,
		state:       StateStarting,
		childExited: make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// State returns the current state name.
func (ps *PtySession) State() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.state
}

// Done is closed when the session is fully closed (post publish-exit).
func (ps *PtySession) Done() <-chan struct{} { return ps.done }

// ExitCode returns the reaped child's exit code (valid after Done is closed).
func (ps *PtySession) ExitCode() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.exitCode
}

// WriteInput forwards stdin bytes to the child. It is refused once the teardown
// "stop input" step has run (input is the FIRST thing severed on close).
func (ps *PtySession) WriteInput(b []byte) (int, error) {
	ps.mu.Lock()
	stopped := ps.inputStopped
	ps.mu.Unlock()
	if stopped {
		return 0, context.Canceled
	}
	return ps.p.Write(b)
}

// Read exposes the raw pty output stream (the relay/recorder consumes it).
func (ps *PtySession) Read(b []byte) (int, error) { return ps.p.Read(b) }

// Resize forwards a window-size change to the pty.
func (ps *PtySession) Resize(cols, rows int) error { return ps.p.Resize(cols, rows) }

func (ps *PtySession) setState(s string) {
	ps.mu.Lock()
	ps.state = s
	ps.mu.Unlock()
	ps.emit("state:" + s)
}

func (ps *PtySession) emit(ev string) {
	if ps.onEvent != nil {
		ps.onEvent(ev)
	}
}

// run drives the session to completion: it reaps the child in the background and
// waits for EITHER a natural child exit OR ctx cancellation. Either way it funnels
// into the SAME ordered teardown (design §5) and returns only after the child is
// reaped and the exit published — i.e. cancel is NOT "signal and return"
// (contrast: local.Runner's exec.CommandContext returns as soon as Wait unblocks,
// with no explicit ordered teardown / publish step).
func (ps *PtySession) run(ctx context.Context) (int, error) {
	ps.setState(StateRunning)

	// Background reaper: the SINGLE pty.Wait call. It stores the exit result and
	// closes childExited. Killed (via Close on cancel) or natural, it returns here.
	go func() {
		code, err := ps.p.Wait(context.Background())
		ps.mu.Lock()
		ps.exitCode, ps.exitErr = code, err
		ps.mu.Unlock()
		close(ps.childExited)
	}()

	select {
	case <-ps.childExited:
		// Natural exit: run teardown to release the fd + publish (no kill needed).
		ps.teardown(false)
	case <-ctx.Done():
		// Cancel: go through cancelling → ordered teardown (kills the child).
		ps.setState(StateCancelling)
		ps.teardown(true)
	}

	<-ps.done
	ps.mu.Lock()
	code, cerr := ps.exitCode, ps.exitErr
	ps.mu.Unlock()
	// Prefer ctx error so the job service classifies cancel/timeout (mirrors
	// local.Runner): the ordered teardown already ran, this only shapes the Result.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return code, ctxErr
	}
	return code, cerr
}

// teardown runs the fixed close sequence exactly once (design §5):
//
//	stop input → close master (+kill child) → wait child (bounded grace)
//	→ publish exit → closed
//
// kill=true when triggered by cancel; false on a natural child exit (the child
// is already gone, but the fd is still closed here and the exit is published
// through the same path so there is ONE terminal path).
func (ps *PtySession) teardown(kill bool) {
	ps.teardownOne.Do(func() {
		ps.setState(StateExiting)

		// 1. stop input — sever stdin first so no late byte races the close.
		ps.emit(stepStopInput)
		ps.mu.Lock()
		ps.inputStopped = true
		ps.mu.Unlock()

		// 2. close master (+ kill child on unix). One primitive, two logical steps.
		ps.emit(stepCloseMaster)
		_ = ps.p.Close()
		if kill {
			ps.emit(stepKillChild)
		}

		// 3. wait child — reap with a bounded grace (the background reaper closes
		// childExited once cmd.Wait returns after the kill/exit).
		ps.emit(stepWaitChild)
		select {
		case <-ps.childExited:
		case <-time.After(ps.grace):
			// Grace expired: proceed with whatever exit is recorded (or synthetic).
			ps.emit(stepWaitTimeout)
			ps.mu.Lock()
			if ps.exitCode == 0 && ps.exitErr == nil {
				ps.exitCode = -1
			}
			ps.mu.Unlock()
		}

		// 4. publish exit — the terminal result is now final.
		ps.emit(stepPublishExit)

		// 5. closed.
		ps.setState(StateClosed)
		close(ps.done)
	})
}
