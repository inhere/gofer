package jobstore

import "testing"

// TestCountUpgradeRestartBlockers: the census counts exactly the jobs a restart would
// kill — every live state of a job this server owns — and ignores the ones it does not:
// terminal work, a job parked by the update that is asking, and a job a worker already
// runs (the next serve adopts those).
func TestCountUpgradeRestartBlockers(t *testing.T) {
	s := openTest(t)
	blocking := []string{"queued", "running", "awaiting_input", "pending_interaction", "waiting_dir"}
	ignored := []string{"recovering", "needs_review", "done", "paused_for_update"}
	for _, status := range blocking {
		rec := sampleJob("blocker-"+status, "self", 1)
		rec.Status = status
		if err := s.UpsertJob(rec); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range ignored {
		rec := sampleJob("ignored-"+status, "self", 2)
		rec.Status = status
		if err := s.UpsertJob(rec); err != nil {
			t.Fatal(err)
		}
	}
	count, err := s.CountUpgradeRestartBlockers(nil, nil)
	if err != nil || count != len(blocking) {
		t.Fatalf("count = %d, %v (want %d)", count, err, len(blocking))
	}
	count, err = s.CountUpgradeRestartBlockers([]string{"blocker-running"}, nil)
	if err != nil || count != len(blocking)-1 {
		t.Fatalf("count excluding a verified source = %d, %v", count, err)
	}
	count, err = s.CountUpgradeRestartBlockers([]string{"blocker-running", "blocker-awaiting_input"}, nil)
	if err != nil || count != len(blocking)-2 {
		t.Fatalf("count excluding source and idle session = %d, %v", count, err)
	}
}

// TestCountUpgradeRestartBlockersSkipsRunningWorkerJobs: a worker job the worker is
// running survives a restart (the next serve holds it as `recovering` and the worker
// claims it back), so it does not block; a worker job the worker has NOT taken over —
// `queued`, or queued on the directory lock before dispatch — does, because a restart
// would fail it as "worker lost" instead of adopting it.
func TestCountUpgradeRestartBlockersSkipsRunningWorkerJobs(t *testing.T) {
	s := openTest(t)
	worker := []string{"w-1"}
	statuses := []string{"queued", "running", "awaiting_input", "pending_interaction", "waiting_dir"}
	// Both halves of the worker predicate are exercised: the rows with an even index
	// carry the resolved worker_id, the odd ones only the worker-type runner (D4: the
	// hub picks the worker at dispatch time, so the column is still empty).
	for i, status := range statuses {
		rec := sampleJob("worker-"+status, "self", 1)
		rec.Status, rec.Runner, rec.WorkerID = status, "w-1", "w-1"
		if i%2 == 1 {
			rec.WorkerID = ""
		}
		if err := s.UpsertJob(rec); err != nil {
			t.Fatal(err)
		}
	}
	count, err := s.CountUpgradeRestartBlockers(nil, worker)
	if err != nil || count != 2 {
		t.Fatalf("worker blockers = %d, %v (want the queued and waiting_dir rows)", count, err)
	}
	got, err := s.ListUpgradeRestartBlockers(nil, worker, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "worker-queued" || got[1].ID != "worker-waiting_dir" {
		t.Fatalf("worker blockers = %+v", got)
	}
	// Without the worker runner names only the explicit worker_id still marks a worker
	// job, so the two runner-only rows block as well.
	count, err = s.CountUpgradeRestartBlockers(nil, nil)
	if err != nil || count != 4 {
		t.Fatalf("count without the worker runner names = %d, %v (want 4)", count, err)
	}
}

// TestListUpgradeRestartBlockersNamesBlockers: the drain log and the update progress
// name the jobs they wait for, with the same predicate and exclusions as the count.
func TestListUpgradeRestartBlockersNamesBlockers(t *testing.T) {
	s := openTest(t)
	for i, status := range []string{"running", "awaiting_input", "done", "needs_review"} {
		rec := sampleJob("blocker-"+status, "self", int64(i+1))
		rec.Status = status
		if err := s.UpsertJob(rec); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListUpgradeRestartBlockers([]string{"blocker-running", ""}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "blocker-awaiting_input" || got[0].Status != "awaiting_input" || got[0].Runner != "local" {
		t.Fatalf("blockers = %+v", got)
	}
	if got[0].StartedAt != 2 {
		t.Fatalf("blocker detail = %+v", got[0])
	}
	got, err = s.ListUpgradeRestartBlockers(nil, nil, 1)
	if err != nil || len(got) != 1 || got[0].ID != "blocker-running" {
		t.Fatalf("limited blockers = %+v, %v", got, err)
	}
}
