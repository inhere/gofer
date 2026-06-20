package job

import (
	"testing"
	"time"
)

// TestWaitForHitsTerminal: a fast command reaches a terminal state well within
// the timeout, so WaitFor returns ok=true with the final (done) snapshot.
func TestWaitForHitsTerminal(t *testing.T) {
	s := newTestService(t, t.TempDir())
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	final, ok := s.WaitFor(res.ID, 10*time.Second)
	if !ok {
		t.Fatalf("WaitFor ok=false, want true (job should have finished)")
	}
	if final.Status != StatusDone {
		t.Fatalf("status=%s want done (err=%s)", final.Status, final.Error)
	}
	if final.ExitCode != 0 {
		t.Fatalf("exit_code=%d want 0", final.ExitCode)
	}
}

// TestWaitForTimesOut: a slow command does not finish within a short timeout, so
// WaitFor returns ok=false and the job keeps running (not cancelled).
func TestWaitForTimesOut(t *testing.T) {
	s := newTestService(t, t.TempDir())
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	snap, ok := s.WaitFor(res.ID, 50*time.Millisecond)
	if ok {
		t.Fatalf("WaitFor ok=true, want false (job should still be running)")
	}
	// The job must NOT be terminal: WaitFor timing out does not cancel it.
	if IsTerminal(snap.Status) {
		t.Fatalf("job is terminal (%s) after WaitFor timeout; should still be running", snap.Status)
	}
	cur, _ := s.Get(res.ID)
	if IsTerminal(cur.Status) {
		t.Fatalf("job %s went terminal (%s) — WaitFor must not cancel it", res.ID, cur.Status)
	}
}

// TestWaitForUnknownID: an unknown id is reported as ok=false.
func TestWaitForUnknownID(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if _, ok := s.WaitFor("does-not-exist", time.Second); ok {
		t.Fatal("WaitFor ok=true for unknown id, want false")
	}
}
