package job

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// TestCallerConcurrencyLimit (E17, design §7.2) proves the per-caller concurrency
// semaphore QUEUES the second job (status `queued`, NOT rejected) until the first
// reaches a terminal state, mirroring the project-semaphore削峰 semantics. The
// project itself is unbounded here so the gating can only come from the caller
// slot.
func TestCallerConcurrencyLimit(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	// Cap caller "ci" to 1 concurrent job via the governance default (project is
	// left unbounded so only the caller slot can gate).
	cfg := s.config()
	cfg.Server.Governance.DefaultCallerMaxConcurrent = 1
	s.cfg.Store(cfg)

	const caller = "ci"
	// Submit job1 (sleep) under caller ci and wait until it actually runs and holds
	// the single caller slot before submitting job2 — deterministic slot ownership.
	r1, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", CallerID: caller,
		Cmd: []string{"sleep", "1"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s, r1.ID, StatusRunning, 2*time.Second)

	r2, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", CallerID: caller,
		Cmd: []string{"sh", "-c", "exit 0"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("second submit must NOT be rejected (queue, not reject): %v", err)
	}
	// Let job2's goroutine reach the (blocked) caller-slot acquisition.
	time.Sleep(50 * time.Millisecond)
	// While job1 runs, job2 must still be queued (caller slot held by job1).
	if j2, _ := s.Get(r2.ID); j2.Status != StatusQueued {
		t.Fatalf("expected job2 queued while job1 holds the caller slot, got %s", j2.Status)
	}

	// Once job1 reaches a terminal state, job2 acquires the freed caller slot and
	// also completes.
	f1, _ := s.Wait(r1.ID)
	f2, _ := s.Wait(r2.ID)
	if f1.Status != StatusDone || f2.Status != StatusDone {
		t.Fatalf("expected both done, got %s/%s", f1.Status, f2.Status)
	}
}

// TestCallerConcurrencyUnlimited (E17) proves that with no caller cap (governance
// default 0 and no per-caller override) two jobs from the same caller run
// concurrently — the caller semaphore is nil, so it never gates.
func TestCallerConcurrencyUnlimited(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	// No governance default, no caller override → unlimited.
	if got := s.config().Server.CallerConcurrencyLimit("ci"); got != 0 {
		t.Fatalf("precondition: expected unlimited (0), got %d", got)
	}

	const caller = "ci"
	r1, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", CallerID: caller,
		Cmd: []string{"sleep", "1"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s, r1.ID, StatusRunning, 2*time.Second)

	r2, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", CallerID: caller,
		Cmd: []string{"sleep", "1"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	// With no caller gating, job2 should also reach running while job1 still runs.
	waitForStatus(t, s, r2.ID, StatusRunning, 2*time.Second)

	s.Wait(r1.ID)
	s.Wait(r2.ID)
}

// TestCallerConcurrencyOverride (E17) proves a per-caller override (> 0) wins over
// the governance default for the concurrency cap. Caller "ci-bot" overrides to 2
// while the governance default is 1; two of its jobs run concurrently.
func TestCallerConcurrencyOverride(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	cfg := s.config()
	cfg.Server.Governance.DefaultCallerMaxConcurrent = 1
	cfg.Server.Callers = []config.CallerConfig{{ID: "ci-bot", MaxConcurrentJobs: 2}}
	s.cfg.Store(cfg)

	const caller = "ci-bot"
	if got := s.config().Server.CallerConcurrencyLimit(caller); got != 2 {
		t.Fatalf("precondition: expected override 2, got %d", got)
	}
	r1, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", CallerID: caller,
		Cmd: []string{"sleep", "1"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", CallerID: caller,
		Cmd: []string{"sleep", "1"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both should reach running concurrently (cap is 2).
	waitForStatus(t, s, r1.ID, StatusRunning, 2*time.Second)
	waitForStatus(t, s, r2.ID, StatusRunning, 2*time.Second)
	s.Wait(r1.ID)
	s.Wait(r2.ID)
}
