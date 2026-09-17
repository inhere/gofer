package job

import (
	"testing"

	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestTerminalEventRecordedBeforeStatusObservable pins the E13 ordering finish()
// promises: whenever a reader observes a terminal status (here: Wait returning),
// the job.terminal event row is ALREADY in the log. The auto-resume batch (v0.42)
// had moved the record after persist, which let an SSE client that opened the
// stream right after seeing "done" replay only submitted/running — the flake
// httpapi's TestStreamEventFrames caught under load. Twenty quick jobs widen the
// window enough to make a regression fail reliably rather than occasionally.
func TestTerminalEventRecordedBeforeStatusObservable(t *testing.T) {
	s := newTestService(t, t.TempDir())
	bin := testcmd.Path(t)
	for i := 0; i < 20; i++ {
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{bin, "printf", "x\n"}, Cwd: ".", TimeoutSec: 30,
		})
		if final.Status != StatusDone {
			t.Fatalf("run %d: expected done, got %s (%s)", i, final.Status, final.Error)
		}
		types := eventTypes(t, s, final.ID)
		if !hasSubsequence(types, []string{EventJobSubmitted, EventJobRunning, EventJobTerminal}) {
			t.Fatalf("run %d: job.terminal not recorded by the time the status was observable: %v", i, types)
		}
	}
}
