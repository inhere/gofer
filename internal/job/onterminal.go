package job

import (
	"log/slog"
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
		go func(h JobTerminalHook) {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("job: terminal hook panicked", "job_id", snap.ID, "panic", r)
				}
			}()
			h(snap)
		}(fn)
	}
}
