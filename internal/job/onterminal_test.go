package job

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// nextTerminal waits for one hook invocation, failing the test if none arrives.
func nextTerminal(t *testing.T, calls <-chan JobResult) JobResult {
	t.Helper()
	select {
	case r := <-calls:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("terminal hook never ran")
		return JobResult{}
	}
}

// assertNoTerminal asserts no further hook invocation arrives within a short window
// (the "exactly once" half of the contract).
func assertNoTerminal(t *testing.T, calls <-chan JobResult) {
	t.Helper()
	select {
	case r := <-calls:
		t.Fatalf("terminal hook ran again for %s (%s)", r.ID, r.Status)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestOnTerminalHooksRunAfterFinish pins the terminal-hook contract (SUP-02 R1):
// every registered hook sees a terminal job EXACTLY ONCE, hooks run off the finish
// path (so a slow or panicking one can never delay or break the job), and a panic
// in one hook does not cost the others their invocation.
func TestOnTerminalHooksRunAfterFinish(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	calls := make(chan JobResult, 8)
	s.OnTerminal(func(JobResult) { panic("this hook is broken") })
	s.OnTerminal(func(r JobResult) { calls <- r })

	done := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "exit", "0"}, Cwd: ".", TimeoutSec: 30,
	})
	failed := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "exit", "3"}, Cwd: ".", TimeoutSec: 30,
	})

	got := map[string]string{}
	for range 2 {
		r := nextTerminal(t, calls)
		got[r.ID] = r.Status
	}
	if got[done.ID] != StatusDone || got[failed.ID] != StatusFailed {
		t.Fatalf("hook statuses = %v, want %s=done / %s=failed", got, done.ID, failed.ID)
	}
	assertNoTerminal(t, calls)

	// The hooks observe the job; they never change it. The panicking one is the
	// proof: the jobs below reached their normal terminal state regardless.
	if again, ok := s.Get(done.ID); !ok || again.Status != StatusDone {
		t.Fatalf("done job after hooks = %+v (ok=%v)", again, ok)
	}
}

// TestOnTerminalHooksRunOnAcceptReject pins the OTHER terminal path: a job gated on
// human验收 is not terminal while it parks in needs_review (no hook), and the
// reviewer's verdict IS its terminal state — the hook fires with done on accept and
// with rejected on reject.
func TestOnTerminalHooksRunOnAcceptReject(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	calls := make(chan JobResult, 4)
	s.OnTerminal(func(r JobResult) { calls <- r })

	park := func() JobResult {
		return submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{testcmd.Path(t), "exit", "0"}, Cwd: ".", TimeoutSec: 30,
			Review: true, ReviewFixed: true,
		})
	}

	accepted := park()
	if accepted.Status != StatusNeedsReview {
		t.Fatalf("reviewed job status = %s, want %s", accepted.Status, StatusNeedsReview)
	}
	// Waiting for a human is not terminal: the hooks must stay silent.
	assertNoTerminal(t, calls)

	if _, err := s.AcceptJob(accepted.ID, "alice", "looks good"); err != nil {
		t.Fatalf("AcceptJob: %v", err)
	}
	r := nextTerminal(t, calls)
	if r.ID != accepted.ID || r.Status != StatusDone {
		t.Fatalf("accept hook = %s/%s, want %s/%s", r.ID, r.Status, accepted.ID, StatusDone)
	}
	assertNoTerminal(t, calls)

	rejected := park()
	if _, err := s.RejectJob(rejected.ID, "alice", "the tests are red", false); err != nil {
		t.Fatalf("RejectJob: %v", err)
	}
	r = nextTerminal(t, calls)
	if r.ID != rejected.ID || r.Status != StatusRejected {
		t.Fatalf("reject hook = %s/%s, want %s/%s", r.ID, r.Status, rejected.ID, StatusRejected)
	}
	assertNoTerminal(t, calls)
}
