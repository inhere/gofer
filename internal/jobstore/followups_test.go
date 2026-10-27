package jobstore

import "testing"

func TestFollowUpQueueClaimDrop(t *testing.T) {
	s, err := Open(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.AddFollowUp("job-1", "first", "caller", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AddFollowUp("job-1", "second", "caller", true)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.ListFollowUps("job-1")
	if err != nil || len(items) != 2 {
		t.Fatalf("list = %v, err=%v", items, err)
	}
	claimed, err := s.ClaimFollowUps("job-1", 10)
	if err != nil || len(claimed) != 1 || claimed[0].ID != first.ID {
		t.Fatalf("claimed = %v, err=%v", claimed, err)
	}
	if err := s.CompleteFollowUps([]int64{first.ID}, "job-next"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DropFollowUp("job-1", second.ID); err != nil || !ok {
		t.Fatalf("drop = %v, err=%v", ok, err)
	}
}
