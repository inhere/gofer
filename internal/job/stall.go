package job

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"
)

// defaultStallTick is how often the AUTO-05 watchdog samples a running job's last
// output. It bounds how LATE a stall is noticed, so it is an order of magnitude below
// the window itself; tests shorten it (Service.stallTick) to make a 1s window
// observable in test time.
const defaultStallTick = 30 * time.Second

// activityWriter marks a job's output activity (AUTO-05): every non-empty write bumps
// the job's lastOutputAt, which the stall watchdog compares against its window. It is
// the ONLY definition of "the job produced something" — stdout, stderr and the
// ndjson projected events all pass through it, and a runner that reports over its own
// channel (acp) writes its updates to the same stderr.
type activityWriter struct {
	w    io.Writer
	mark func()
}

func (a activityWriter) Write(p []byte) (int, error) {
	n, err := a.w.Write(p)
	if n > 0 && a.mark != nil {
		a.mark()
	}
	return n, err
}

// markOutput records that the job just produced n bytes (a non-empty write).
func (s *Service) markOutput(entry *jobEntry) {
	entry.lastOutputAt.Store(s.nowFn().UnixNano())
}

// pauseStall suspends the AUTO-05 clock while the job legitimately has nothing to say:
// it is parked on a human's answer, or running its verify step (which has its own
// deadline). Without the pause a job waiting for input would be killed as "silent"
// exactly when it is behaving correctly.
func (s *Service) pauseStall(entry *jobEntry) {
	entry.stallPaused.Store(true)
}

// resumeStall restarts the clock with a FRESH window: the silence that accrued while
// paused was not the job's fault, so it must not count against it.
func (s *Service) resumeStall(entry *jobEntry) {
	entry.lastOutputAt.Store(s.nowFn().UnixNano())
	entry.stallPaused.Store(false)
}

// startStallWatchdog arms the AUTO-05 output-stall watchdog for a running job and
// returns the stop func. window <= 0 means "do not watch" and returns a no-op.
//
// On a stall it records job.stalled, cancels the job's context (the same path a cancel
// takes, so the runner kills the child) and stores the failure the caller must report:
// execute applies it as `failed: stalled: no output for <N>s` after the runner returns.
// That classification is what makes the failure TRANSIENT (the built-in transient
// patterns match the message), so the auto-resume / failover chain takes over instead
// of leaving a hung provider to burn the job's whole deadline.
func (s *Service) startStallWatchdog(ctx context.Context, entry *jobEntry, jobID string, windowSec int) func() {
	if windowSec <= 0 {
		return func() {}
	}
	window := time.Duration(windowSec) * time.Second
	tick := s.stallTick
	if tick <= 0 {
		tick = defaultStallTick
	}
	// The window starts HERE (the job is about to run): without this an unwritten
	// lastOutputAt would read as "silent since 1970" and kill the job on the first tick.
	entry.lastOutputAt.Store(s.nowFn().UnixNano())
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if entry.stallPaused.Load() {
				continue
			}
			from := entry.lastOutputAt.Load()
			if from == 0 {
				continue
			}
			silent := s.nowFn().Sub(time.Unix(0, from))
			if silent <= window {
				continue
			}
			sec := int(silent / time.Second)
			msg := fmt.Sprintf("stalled: no output for %ds", sec)
			entry.stallErr.Store(&msg)
			slog.Warn("job.stalled", "job_id", jobID, "silent_sec", sec, "stall_timeout_sec", windowSec)
			s.recordEvent(jobID, EventJobStalled, map[string]any{
				"silent_sec": sec, "stall_timeout_sec": windowSec,
			})
			entry.mu.Lock()
			cancel := entry.cancel
			entry.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			return
		}
	}()
	var once atomic.Bool
	return func() {
		if once.CompareAndSwap(false, true) {
			close(done)
		}
	}
}

// takeStall returns the stall failure the watchdog recorded, if it fired.
func (entry *jobEntry) takeStall() error {
	if p := entry.stallErr.Load(); p != nil {
		return fmt.Errorf("%s", *p)
	}
	return nil
}
