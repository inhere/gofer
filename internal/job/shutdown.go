package job

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// shutdownAdmissionOwner marks the admission gate as closed by Shutdown (not by an
// upgrade), so an upgrade bridge can never reopen it afterwards.
const shutdownAdmissionOwner = "service-shutdown"

// shutdownStartGrace is how long Shutdown waits for a job whose execute goroutine is
// launched but has not installed its cancel func yet (see shutdown).
const shutdownStartGrace = 2 * time.Second

// bgGroup counts the service's background work: every goroutine or timer callback
// that can still touch the store after the job it belongs to looks finished —
// finish itself, terminal hooks, workflow advances, the stall watchdog, wakeups and
// the session reply reminder (design 2026-10-10-flaky-tests-root-cause §2.2).
//
// It is a counter with an idle channel rather than a sync.WaitGroup: Shutdown waits
// while new work may still be added (a finish that started before it dispatches its
// hooks), and a WaitGroup forbids an Add from zero that races a Wait.
type bgGroup struct {
	mu      sync.Mutex
	n       int
	idle    chan struct{} // open while n > 0, closed when it drops back to 0
	closed  bool          // Shutdown started: tryAdd refuses deferred work
	closing chan struct{} // closed together with closed, for selects
}

func (g *bgGroup) addLocked() {
	if g.n == 0 {
		g.idle = make(chan struct{})
	}
	g.n++
}

// add registers work that must run even during Shutdown (a finish already under
// way, the hooks it dispatches).
func (g *bgGroup) add() {
	g.mu.Lock()
	g.addLocked()
	g.mu.Unlock()
}

// tryAdd registers DEFERRED work (a timer callback, a delayed wakeup) unless
// Shutdown has started, in which case the work is dropped.
func (g *bgGroup) tryAdd() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.addLocked()
	return true
}

func (g *bgGroup) done() {
	g.mu.Lock()
	g.n--
	if g.n == 0 {
		close(g.idle)
		g.idle = nil
	}
	g.mu.Unlock()
}

// close refuses further deferred work and wakes everything selecting on closingCh.
func (g *bgGroup) close() {
	g.mu.Lock()
	if !g.closed {
		g.closed = true
		if g.closing == nil {
			g.closing = make(chan struct{})
		}
		close(g.closing)
	}
	g.mu.Unlock()
}

// closingCh is closed once Shutdown starts.
func (g *bgGroup) closingCh() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing == nil {
		g.closing = make(chan struct{})
	}
	return g.closing
}

// wait blocks until no background work is in flight. Work added while it waits
// (by work it is waiting for) is waited for too.
func (g *bgGroup) wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		idle := g.idle
		g.mu.Unlock()
		if idle == nil {
			return nil
		}
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// goBG runs fn on its own goroutine as tracked background work (see bgGroup.add).
func (s *Service) goBG(fn func()) {
	s.bg.add()
	go func() {
		defer s.bg.done()
		fn()
	}()
}

// GoBackground runs fn on its own goroutine as tracked background work, unless
// Shutdown has started (then fn is dropped and false is returned). It is the seam the
// workflow engine uses for its asynchronous advances, so Shutdown waits for them too.
func (s *Service) GoBackground(fn func()) bool {
	if !s.bg.tryAdd() {
		return false
	}
	go func() {
		defer s.bg.done()
		fn()
	}()
	return true
}

// Shutdown is the job service's close protocol (design §2.2). After it returns, no
// job this process executes and no background work of the service touches the store,
// so the caller may close it:
//
//  1. admission closes for good — Submit, workflow advances and wakeups are refused —
//     and the producers already admitted are waited for;
//  2. every job this process executes is cancelled; the jobs a worker executes are
//     left alone (see below);
//  3. the cancelled jobs are waited for until their execute goroutine has returned
//     (entry.done), not merely until their status reads terminal;
//  4. the background work (finish, terminal hooks, advances, timers) is waited for.
//
// A job cancelled here keeps the nonterminal row it had, exactly as if the process
// had exited: the next serve's startup reconcile settles it as it always did (a local
// job fails as orphaned, a resident ACP session is resumed). Finishing it as
// cancelled instead would read as a user's cancel — no retry, the workflow and plan
// would follow it — on every restart.
//
// A job a worker executes (the set ReconcileOrphanJobs holds in `recovering`) is not
// cancelled: that would send the worker a cancel frame and end work the next serve
// adopts. It is not waited for either. Drain also ends those, for a teardown no
// later serve follows.
//
// ctx bounds the whole protocol; on expiry the error says which phase was left
// unfinished. Shutdown is idempotent.
func (s *Service) Shutdown(ctx context.Context) error { return s.shutdown(ctx, false) }

// Drain is Shutdown that also ends the jobs a worker executes. It is for a process
// whose remote jobs nobody adopts afterwards — a test tearing down its TempDir.
func (s *Service) Drain(ctx context.Context) error { return s.shutdown(ctx, true) }

func (s *Service) shutdown(ctx context.Context, includeRemote bool) error {
	s.bg.close()

	s.admissionMu.Lock()
	s.admissionClosed = true
	if s.admissionUpgradeID == "" {
		s.admissionUpgradeID = shutdownAdmissionOwner
	}
	idle := s.admissionIdle
	s.admissionMu.Unlock()
	if idle != nil {
		select {
		case <-idle:
		case <-ctx.Done():
			return fmt.Errorf("job shutdown: admitted producers still running: %w", ctx.Err())
		}
	}

	cfg := s.config()
	s.mu.Lock()
	entries := make([]*jobEntry, 0, len(s.jobs))
	for _, e := range s.jobs {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	type pending struct {
		e       *jobEntry
		started chan struct{} // non-nil: execute has not started yet, wait for it first
	}
	var waits []pending
	for _, e := range entries {
		e.mu.Lock()
		remote := e.adopted != nil || e.result.WorkerID != "" || (cfg != nil && isWorkerRunner(cfg, e.result.Runner))
		if remote && !includeRemote {
			e.mu.Unlock()
			continue
		}
		driven := e.executing || e.adopted != nil
		if !driven && e.cancel != nil {
			// A cancel func but nothing behind it: a record a test planted directly.
			// There is nothing to stop or to wait for.
			e.mu.Unlock()
			continue
		}
		e.shutdownRequested = true
		if e.result.Session {
			e.sessionEnding = true
			s.cancelSessionReplyTimerLocked(e)
		}
		cancel := e.cancel
		p := pending{e: e}
		if !driven {
			// execute has not installed its context yet: it honours the recorded intent
			// the moment it does (F3) and closes started then.
			e.cancelRequested = true
			if e.started == nil {
				e.started = make(chan struct{})
			}
			p.started = e.started
		}
		e.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		waits = append(waits, p)
	}
	for _, p := range waits {
		if p.started != nil {
			// Every Submit has returned (admission is idle), so this job's execute
			// goroutine exists and starts almost at once. An entry nothing drives (a
			// record a test planted directly) never starts: it is skipped after the
			// grace instead of being waited for until ctx expires.
			grace := time.NewTimer(shutdownStartGrace)
			select {
			case <-p.started:
				grace.Stop()
			case <-grace.C:
				continue
			case <-ctx.Done():
				grace.Stop()
				return fmt.Errorf("job shutdown: job %s not started: %w", p.e.snapshot().ID, ctx.Err())
			}
		}
		select {
		case <-p.e.done:
		case <-ctx.Done():
			return fmt.Errorf("job shutdown: job %s still running: %w", p.e.snapshot().ID, ctx.Err())
		}
	}

	if err := s.bg.wait(ctx); err != nil {
		return fmt.Errorf("job shutdown: background work still running: %w", err)
	}
	return nil
}

// skipFinishForShutdown reports whether Shutdown cancelled this job, in which case
// finish leaves its row as it is (see Shutdown). An accepted manual end of a resident
// session is the exception: that end was the user's, so it finishes normally.
func (entry *jobEntry) skipFinishForShutdown() bool {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.shutdownRequested && !entry.manualEndRequested
}

// waitFinishedOrClosing is WaitFor for background work: it also gives up (closing
// true) once Shutdown starts, so a delayed wakeup never holds Shutdown up waiting for
// a job Shutdown left running.
func (s *Service) waitFinishedOrClosing(id string, timeout time.Duration) (reached, closing bool) {
	entry := s.entry(id)
	if entry == nil {
		_, ok := s.WaitFor(id, timeout)
		return ok, false
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-entry.done:
		return true, false
	case <-s.bg.closingCh():
		return false, true
	case <-t.C:
		return false, false
	}
}
