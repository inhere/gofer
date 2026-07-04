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

// TestListJobsSessionFilter proves the P3 ListOpts.Session filter flows through
// ListOpts -> jobstore.ListQuery -> WHERE session_id=? : stamp the two finished
// jobs with distinct session_ids (re-upsert their DB records), then a session
// query returns only the matching job.
func TestListJobsSessionFilter(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	j1 := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	j2 := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})

	// Stamp distinct session_ids onto the persisted records (the exec agent does
	// not produce one; we seed it to exercise the filter end-to-end).
	r1, ok := s.Get(j1.ID)
	if !ok {
		t.Fatalf("Get(%s) not found", j1.ID)
	}
	r1.SessionID = "sess-one"
	if err := s.Meta().UpsertJob(toRecord(r1)); err != nil {
		t.Fatalf("upsert j1: %v", err)
	}
	r2, _ := s.Get(j2.ID)
	r2.SessionID = "sess-two"
	if err := s.Meta().UpsertJob(toRecord(r2)); err != nil {
		t.Fatalf("upsert j2: %v", err)
	}

	// session=sess-one -> only j1.
	one, err := s.ListJobs(ListOpts{Session: "sess-one"})
	if err != nil {
		t.Fatalf("ListJobs(session): %v", err)
	}
	if len(one) != 1 || one[0].ID != j1.ID {
		t.Fatalf("session=sess-one expected only j1, got %+v", one)
	}
	if one[0].SessionID != "sess-one" {
		t.Fatalf("expected session_id echoed, got %q", one[0].SessionID)
	}

	// session=sess-two -> only j2.
	two, err := s.ListJobs(ListOpts{Session: "sess-two"})
	if err != nil {
		t.Fatalf("ListJobs(session): %v", err)
	}
	if len(two) != 1 || two[0].ID != j2.ID {
		t.Fatalf("session=sess-two expected only j2, got %+v", two)
	}

	// unknown session -> none.
	none, err := s.ListJobs(ListOpts{Session: "sess-none"})
	if err != nil {
		t.Fatalf("ListJobs(session none): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown session expected 0, got %d", len(none))
	}
}

// TestInteractiveRoundTripsThroughDB proves WEB-03 interactive survives Submit →
// persist → Get/ListJobs on the DB read path after terminal eviction.
func TestInteractiveRoundTripsThroughDB(t *testing.T) {
	root := t.TempDir()
	s := newWorkerTestService(t, root, &stubWorkerRunner{})
	pty := &recordingRunner{name: builtinPtyRunner}
	s.runners[builtinPtyRunner] = pty

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "term", Runner: "local",
		Interactive: true, Cols: 120, Rows: 40,
		Prompt: "hi", Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}
	if e := s.entry(final.ID); e != nil {
		t.Fatalf("setup: expected job evicted after terminal")
	}

	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get(%s) not found", final.ID)
	}
	if !got.Interactive {
		t.Fatalf("Get: interactive did not round-trip")
	}

	plain := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	gotPlain, ok := s.Get(plain.ID)
	if !ok {
		t.Fatalf("Get(%s) not found", plain.ID)
	}
	if gotPlain.Interactive {
		t.Fatalf("Get: non-interactive job returned interactive=true")
	}

	list, err := s.ListJobs(ListOpts{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	byID := map[string]JobResult{}
	for _, j := range list {
		byID[j.ID] = j
	}
	if !byID[final.ID].Interactive {
		t.Fatalf("ListJobs: interactive job did not round-trip")
	}
	if byID[plain.ID].Interactive {
		t.Fatalf("ListJobs: non-interactive job returned interactive=true")
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
