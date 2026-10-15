package job

import (
	"context"
	"log/slog"
	"time"
)

// JobEndedHook observes a job that has ENDED — a terminal state (done / failed /
// timeout / cancelled / rejected), or a delivery PARKED for a human's review
// (needs_review), which the terminal hook below deliberately does not see.
//
// It exists for the end-notification (gofer-3db4): whoever sent a job wants to
// hear that it stopped, and "it stopped, waiting for your review" is exactly the
// case they must not miss. Unlike JobTerminalHook the hook therefore fires for
// needs_review too, and a later accept/reject fires it AGAIN — the notification
// itself is deduplicated by its own durable record (see sessionrelay.NotifyJobEnded),
// so a hook must not assume it runs once per job.
//
// Like the terminal hook it runs after the outcome is durable, in its own
// goroutine: it can neither delay nor change the job's ending, and a panicking or
// slow hook is logged and swallowed.
type JobEndedHook func(JobResult)

// OnJobEnded registers an ended-hook (see JobEndedHook). Called once at assemble
// time; safe for concurrent use with running jobs (the list is copied under the
// mutex before any hook starts).
func (s *Service) OnJobEnded(fn JobEndedHook) {
	if fn == nil {
		return
	}
	s.endedMu.Lock()
	s.endedHooks = append(s.endedHooks, fn)
	s.endedMu.Unlock()
}

// notifyJobEnded runs every ended-hook on its own goroutine. Best-effort by
// construction: a panic is logged and swallowed rather than taking the process
// (or the sibling hooks) down with it.
func (s *Service) notifyJobEnded(snap JobResult) {
	s.endedMu.Lock()
	hooks := append([]JobEndedHook(nil), s.endedHooks...)
	s.endedMu.Unlock()
	for _, fn := range hooks {
		h := fn
		s.goBG(func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("job: ended hook panicked", "job_id", snap.ID, "panic", r)
				}
			}()
			h(snap)
		})
	}
}

// WaitEndedHooks waits up to timeout for the service's background work to return —
// ended-hooks included, since they run through the same background group as the
// terminal hooks (WaitTerminalHooks). Whoever closes the store waits here first.
func (s *Service) WaitEndedHooks(timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.bg.wait(ctx) == nil
}
