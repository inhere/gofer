//go:build !windows

package commands

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// TestAbortReportOnSignal: the terminal's Esc reaches the blocked Stop hook as a
// signal; the hook must report the interrupt (once) and exit 130, and a hook that
// finished on its own must not react to a later signal.
func TestAbortReportOnSignal(t *testing.T) {
	reported := make(chan struct{}, 2)
	exited := make(chan int, 1)
	disarm := abortReportOnSignal(func() { reported <- struct{}{} }, func(code int) { exited <- code })
	defer disarm()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Skipf("cannot signal self: %v", err)
	}
	select {
	case code := <-exited:
		if code != 130 {
			t.Fatalf("exit code = %d, want 130", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hook did not exit after SIGTERM")
	}
	if len(reported) != 1 {
		t.Fatalf("reports = %d, want exactly 1", len(reported))
	}
}
