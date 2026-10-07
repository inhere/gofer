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
