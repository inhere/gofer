package job

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dev-agent-bridge/internal/store"
)

// liveCount returns the number of entries currently tracked in the in-memory
// job map. Same-package test access proves the SP3 invariant that the map is
// bounded by the LIVE job set, not the historical job count (C1 in-memory side).
func (s *Service) liveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

// TestFinishEvictsInMemoryEntry is the SP3 core: a job that reaches a terminal
// state is removed from the in-memory map (s.entry == nil), yet remains fully
// queryable via Get (DB fallback) and ListJobs. This is the direct proof that the
// in-memory side of C1 (the never-evicted s.jobs map) is rooted out.
func TestFinishEvictsInMemoryEntry(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}

	// In-memory entry is gone after finish.
	if e := s.entry(final.ID); e != nil {
		t.Fatalf("expected job evicted from memory after finish, entry still present")
	}
	if got := s.liveCount(); got != 0 {
		t.Fatalf("expected 0 live entries after the only job finished, got %d", got)
	}

	// ... but Get still returns the terminal snapshot (DB fallback).
	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get after eviction: job not found")
	}
	if got.Status != StatusDone || got.ID != final.ID {
		t.Fatalf("Get after eviction: unexpected %+v", got)
	}
	if got.ResultDir != final.ResultDir {
		t.Fatalf("Get after eviction: result_dir mismatch %q != %q", got.ResultDir, final.ResultDir)
	}

	// ... and ListJobs still includes it.
	list, err := s.ListJobs(ListOpts{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !containsJob(list, final.ID) {
		t.Fatalf("ListJobs after eviction does not contain %s", final.ID)
	}
}

// TestMemoryBoundedByLiveNotHistory runs many short jobs to completion and proves
// the in-memory map returns to live-only (0) afterwards — i.e. memory grows with
// concurrency, not with the cumulative number of jobs ever run (C1 root cause).
func TestMemoryBoundedByLiveNotHistory(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	const n = 25
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		res, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"sh", "-c", "exit 0"}, Cwd: ".", TimeoutSec: 30,
		})
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		ids = append(ids, res.ID)
	}
	for _, id := range ids {
		if _, ok := s.Wait(id); !ok {
			t.Fatalf("Wait %s: not found", id)
		}
	}

	// All jobs terminated => the in-memory map is empty (no historical retention).
	if got := s.liveCount(); got != 0 {
		t.Fatalf("expected 0 live entries after %d jobs finished, got %d", n, got)
	}
	// But every job is still listable from the DB.
	list, err := s.ListJobs(ListOpts{Limit: n + 10})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list) != n {
		t.Fatalf("expected %d jobs in DB-backed list, got %d", n, len(list))
	}
}

// TestWaitAfterEviction asserts Wait on an already-evicted terminal job returns
// the terminal snapshot from the metadata store immediately (no blocking, no
// false "unknown job").
func TestWaitAfterEviction(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if e := s.entry(final.ID); e != nil {
		t.Fatalf("setup: expected evicted entry")
	}

	done := make(chan struct{})
	var got JobResult
	var ok bool
	go func() {
		got, ok = s.Wait(final.ID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Wait on evicted job blocked instead of returning the terminal snapshot")
	}
	if !ok || got.Status != StatusDone || got.ID != final.ID {
		t.Fatalf("Wait after eviction: ok=%v %+v", ok, got)
	}
}

// TestWaitUnknownJob asserts Wait still returns false for an id that was never
// submitted (eviction must not turn unknown jobs into a hang or a phantom hit).
func TestWaitUnknownJob(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if _, ok := s.Wait("never-existed"); ok {
		t.Fatalf("expected Wait(unknown) to return false")
	}
}

// TestCancelAfterEviction asserts cancelling an already-evicted terminal job is a
// stable no-op (nil error), preserving the pre-SP3 "cancel of a finished job is a
// no-op" contract now that the entry is gone from memory.
func TestCancelAfterEviction(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if e := s.entry(final.ID); e != nil {
		t.Fatalf("setup: expected evicted entry")
	}
	if err := s.Cancel(final.ID); err != nil {
		t.Fatalf("Cancel of evicted terminal job should be a no-op, got %v", err)
	}
	// An unknown id must still error.
	if err := s.Cancel("never-existed"); err == nil {
		t.Fatalf("expected error cancelling unknown job id")
	}
}

// TestLogsReadableAfterEviction asserts a terminal job's stdout/stderr stay
// readable from the result directory after the in-memory entry is evicted: the
// log read path resolves result_dir via the DB fallback in Get, not the live map.
func TestLogsReadableAfterEviction(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if e := s.entry(final.ID); e != nil {
		t.Fatalf("setup: expected evicted entry")
	}

	// TailLog uses Get (DB fallback) -> result_dir -> file; must still find output.
	out, err := s.TailLog(final.ID, store.StreamStdout, 0)
	if err != nil {
		t.Fatalf("TailLog after eviction: %v", err)
	}
	if !strings.Contains(string(out), "go version") {
		t.Fatalf("stdout missing after eviction: %q", out)
	}
}

// TestInteractionsPersistAfterEviction raises an interaction on a live job,
// answers it, then drives the job to terminal/evicted and asserts the interaction
// history is still listable via GetPersistedInteractions (interactions.jsonl
// fallback) — the in-memory-only GetInteractions would be empty post-eviction.
func TestInteractionsPersistAfterEviction(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	// A short interactive-style job: it sleeps long enough to raise + answer one
	// interaction, then exits, so we can drive it to terminal deterministically.
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "30"}, Cwd: ".", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	jobID := res.ID
	waitForStatus(t, s, jobID, StatusRunning, 2*time.Second)

	it, err := s.CreateInteraction(jobID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "continue?"})
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}
	if _, err := s.AnswerInteraction(jobID, it.ID, "yes"); err != nil {
		t.Fatalf("AnswerInteraction: %v", err)
	}

	// Drive the job to terminal (cancel) and wait for eviction.
	if err := s.Cancel(jobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, ok := s.Wait(jobID); !ok {
		t.Fatalf("Wait: job not found")
	}
	if e := s.entry(jobID); e != nil {
		t.Fatalf("expected job evicted after terminal")
	}

	// In-memory-only view is now empty...
	if mem, _ := s.GetInteractions(jobID); len(mem) != 0 {
		t.Fatalf("expected empty in-memory interactions after eviction, got %d", len(mem))
	}
	// ...but the persisted view (interactions.jsonl) still lists the answered one.
	base := filepath.Join(root, "self")
	persisted, err := s.GetPersistedInteractions(base, jobID)
	if err != nil {
		t.Fatalf("GetPersistedInteractions: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted interaction after eviction, got %d", len(persisted))
	}
	if persisted[0].Status != InteractionAnswered || persisted[0].Answer != "yes" {
		t.Fatalf("persisted interaction not the answered one: %+v", persisted[0])
	}
}

// containsJob reports whether the list holds a job with the given id.
func containsJob(list []JobResult, id string) bool {
	for _, j := range list {
		if j.ID == id {
			return true
		}
	}
	return false
}
