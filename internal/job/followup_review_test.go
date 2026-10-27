package job

import "testing"

func TestPendingFollowUpGateUsesQueueOrder(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if _, err := s.Meta().AddFollowUp("job-gate", "first", "caller", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Meta().AddFollowUp("job-gate", "second", "caller", true); err != nil {
		t.Fatal(err)
	}
	has, reviewEach := s.pendingFollowUpGate("job-gate")
	if !has || reviewEach {
		t.Fatalf("gate = %v/%v, want pending non-review_each", has, reviewEach)
	}
	if ok, err := s.Meta().DropFollowUp("job-gate", 1); err != nil || !ok {
		t.Fatalf("drop first = %v, %v", ok, err)
	}
	has, reviewEach = s.pendingFollowUpGate("job-gate")
	if !has || !reviewEach {
		t.Fatalf("gate after drop = %v/%v, want pending review_each", has, reviewEach)
	}
}
