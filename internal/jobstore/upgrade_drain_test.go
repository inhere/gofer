package jobstore

import "testing"

func TestCountUpgradeInFlightDurableStatuses(t *testing.T) {
	s := openTest(t)
	statuses := []string{"queued", "running", "awaiting_input", "pending_interaction", "waiting_dir", "recovering", "needs_review", "done"}
	for _, status := range statuses {
		rec := sampleJob("upgrade-"+status, "self", 1)
		rec.Status = status
		if err := s.UpsertJob(rec); err != nil {
			t.Fatal(err)
		}
	}
	count, err := s.CountUpgradeInFlight("")
	if err != nil || count != 6 {
		t.Fatalf("count = %d, %v", count, err)
	}
	count, err = s.CountUpgradeInFlight("upgrade-running")
	if err != nil || count != 5 {
		t.Fatalf("count excluding verified source = %d, %v", count, err)
	}
	count, err = s.CountUpgradeInFlightExcluding([]string{"upgrade-running", "upgrade-awaiting_input"})
	if err != nil || count != 4 {
		t.Fatalf("count excluding source and idle session = %d, %v", count, err)
	}
}

// TestListUpgradeInFlightNamesBlockers: the drain log names the jobs it waits
// for, with the same status set and exclusions as the count.
func TestListUpgradeInFlightNamesBlockers(t *testing.T) {
	s := openTest(t)
	for i, status := range []string{"running", "awaiting_input", "done", "needs_review"} {
		rec := sampleJob("blocker-"+status, "self", int64(i+1))
		rec.Status = status
		rec.Runner = "w-1"
		if err := s.UpsertJob(rec); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListUpgradeInFlightExcluding([]string{"blocker-running", ""}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "blocker-awaiting_input" || got[0].Status != "awaiting_input" || got[0].Runner != "w-1" {
		t.Fatalf("blockers = %+v", got)
	}
	got, err = s.ListUpgradeInFlightExcluding(nil, 1)
	if err != nil || len(got) != 1 || got[0].ID != "blocker-running" {
		t.Fatalf("limited blockers = %+v, %v", got, err)
	}
}
