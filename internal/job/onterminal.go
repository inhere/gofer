package job

import (
	"context"
	"log/slog"
	"time"
)

// JobTerminalHook observes a job the moment it reaches a TERMINAL state (SUP-02
// R1). It is the reverse seam the assembled hub uses to react to an outcome the
// job service itself has no business knowing about: a finished path-B takeover job
// hands its session back (sessionrelay.ReleaseTakeoverForJob).
//
// The hook runs AFTER the terminal state is durable and the entry is evicted, in
// its own goroutine: it can neither delay finish nor change the job's outcome, and
// a slow or panicking one is that hook's own problem (the panic is recovered and
// logged, and the other hooks still run).
type JobTerminalHook func(JobResult)

// OnTerminal registers a terminal hook (see JobTerminalHook). It is called once at
// assemble time, may be called more than once, and is safe for concurrent use with
// running jobs: the hook list is copied under a mutex before any hook is started.
func (s *Service) OnTerminal(fn JobTerminalHook) {
	if fn == nil {
		return
	}
	s.terminalMu.Lock()
	s.terminalHooks = append(s.terminalHooks, fn)
	s.terminalMu.Unlock()
}

// notifyTerminalHooks runs every registered hook on its own goroutine. Best-effort
// by construction: hooks are observers, so a panic is logged and swallowed rather
// than allowed to take the process (or the sibling hooks) down with it.
func (s *Service) notifyTerminalHooks(snap JobResult) {
	s.terminalMu.Lock()
	hooks := append([]JobTerminalHook(nil), s.terminalHooks...)
	s.terminalMu.Unlock()
	for _, fn := range hooks {
		h := fn
		s.goBG(func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("job: terminal hook panicked", "job_id", snap.ID, "panic", r)
				}
			}()
			h(snap)
		})
	}
}

// WaitTerminalHooks waits up to timeout for the service's background work — the
// terminal hooks started so far, and the finishes still dispatching theirs — and
// reports whether it all returned. A hook may still touch the store after the job is
// terminal, so whoever closes the store waits here (or in Shutdown) first.
func (s *Service) WaitTerminalHooks(timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.bg.wait(ctx) == nil
}
