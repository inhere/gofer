package job

import (
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
		s.terminalRunning.Add(1)
		go func(h JobTerminalHook) {
			defer s.terminalRunning.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("job: terminal hook panicked", "job_id", snap.ID, "panic", r)
				}
			}()
			h(snap)
		}(fn)
	}
}

// WaitTerminalHooks waits up to timeout for the terminal hooks started so far to
// return, and reports whether they all did. A hook may still touch the store after
// the job is terminal, so whoever closes the store (tests tearing down a TempDir)
// waits here first.
func (s *Service) WaitTerminalHooks(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.terminalRunning.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
