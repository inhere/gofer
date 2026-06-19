package job

import (
	"path/filepath"
	"testing"
)

// TestListJobsMergedAndSorted runs one done + one failed job, then asserts
// ListJobs returns both, sorted by started_at desc, with the expected fields.
func TestListJobsMergedAndSorted(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	done := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if done.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", done.Status)
	}
	failed := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 3"}, Cwd: ".", TimeoutSec: 30,
	})
	if failed.Status != StatusFailed || failed.ExitCode != 3 {
		t.Fatalf("setup: expected failed exit 3, got %s/%d", failed.Status, failed.ExitCode)
	}

	list, err := s.ListJobs(ListOpts{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 jobs, got %d: %+v", len(list), list)
	}
	// Sorted by started_at desc, then id desc. started_at is wall-clock seconds
	// so the two may share a second; assert the order is at least deterministic
	// (non-increasing started_at).
	if list[0].StartedAt < list[1].StartedAt {
		t.Fatalf("not sorted by started_at desc: %d before %d", list[0].StartedAt, list[1].StartedAt)
	}

	byID := map[string]JobResult{}
	for _, r := range list {
		byID[r.ID] = r
		if r.ProjectKey != "self" || r.Agent != "exec" {
			t.Fatalf("unexpected fields: %+v", r)
		}
	}
	if got := byID[done.ID]; got.Status != StatusDone || got.ExitCode != 0 {
		t.Fatalf("done job mismatch: %+v", got)
	}
	if got := byID[failed.ID]; got.Status != StatusFailed || got.ExitCode != 3 {
		t.Fatalf("failed job mismatch: %+v", got)
	}
}

// TestListJobsFilters covers status, project and limit filtering.
func TestListJobsFilters(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	done := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	failed := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 3"}, Cwd: ".", TimeoutSec: 30,
	})
	_ = failed

	// status=done -> only the done job.
	onlyDone, err := s.ListJobs(ListOpts{Status: StatusDone})
	if err != nil {
		t.Fatalf("ListJobs(status): %v", err)
	}
	if len(onlyDone) != 1 || onlyDone[0].ID != done.ID {
		t.Fatalf("status=done filter wrong: %+v", onlyDone)
	}

	// project=self -> both (only project in the test cfg with jobs).
	proj, err := s.ListJobs(ListOpts{Project: "self"})
	if err != nil {
		t.Fatalf("ListJobs(project): %v", err)
	}
	if len(proj) != 2 {
		t.Fatalf("project=self expected 2, got %d", len(proj))
	}

	// unknown project -> empty (non-nil) slice, no error.
	none, err := s.ListJobs(ListOpts{Project: "ghost"})
	if err != nil {
		t.Fatalf("ListJobs(ghost): %v", err)
	}
	if none == nil || len(none) != 0 {
		t.Fatalf("unknown project should yield empty non-nil slice, got %+v (nil=%v)", none, none == nil)
	}

	// limit=1 -> truncated to 1.
	limited, err := s.ListJobs(ListOpts{Limit: 1})
	if err != nil {
		t.Fatalf("ListJobs(limit): %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limit=1 expected 1, got %d", len(limited))
	}
}

// TestListJobsRestartRecoversFromIndex runs jobs on serviceA (which upserts them
// into the metadata db), then a fresh serviceB built over the SAME db file
// (empty in-memory map) must still list the historical jobs from the DB.
func TestListJobsRestartRecoversFromIndex(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "gofer.db")
	serviceA := newTestServiceWithDB(t, root, dbPath)

	a1 := submitAndWait(t, serviceA, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	a2 := submitAndWait(t, serviceA, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if a1.Status != StatusDone || a2.Status != StatusDone {
		t.Fatalf("setup: both should be done, got %s/%s", a1.Status, a2.Status)
	}

	// Fresh service over the same db file -> empty in-memory jobs map.
	serviceB := newTestServiceWithDB(t, root, dbPath)
	if len(serviceB.jobs) != 0 {
		t.Fatalf("serviceB should start with no in-memory jobs")
	}

	list, err := serviceB.ListJobs(ListOpts{})
	if err != nil {
		t.Fatalf("serviceB.ListJobs: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("serviceB should recover 2 jobs from index, got %d", len(list))
	}
	for _, r := range list {
		if r.Status != StatusDone {
			t.Fatalf("recovered job should be terminal (done), got %s for %s", r.Status, r.ID)
		}
	}
}
