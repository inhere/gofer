package jobstore

import "testing"

func TestTriggerReceiptClaimIsIdempotent(t *testing.T) {
	s := openTest(t)
	first := TriggerReceipt{TriggerName: "push", ExternalEventID: "d1", RequestID: "r1", BodyHash: HashTriggerBody([]byte("one")), ReceivedAt: 10, ExpiresAt: 20}
	got, dup, err := s.ClaimTriggerReceipt(first)
	if err != nil || dup || got != nil {
		t.Fatalf("first claim got=%+v dup=%v err=%v", got, dup, err)
	}
	got, dup, err = s.ClaimTriggerReceipt(TriggerReceipt{TriggerName: "push", ExternalEventID: "d1", RequestID: "r2", BodyHash: first.BodyHash, ReceivedAt: 11, ExpiresAt: 20})
	if err != nil || !dup || got == nil || got.RequestID != "r1" {
		t.Fatalf("duplicate got=%+v dup=%v err=%v", got, dup, err)
	}
	if _, _, err = s.ClaimTriggerReceipt(TriggerReceipt{TriggerName: "push", ExternalEventID: "d1", RequestID: "r3", BodyHash: "different", ReceivedAt: 11, ExpiresAt: 20}); err != ErrTriggerReceiptConflict {
		t.Fatalf("conflict err=%v", err)
	}
	if err := s.UpdateTriggerReceipt("push", "d1", "submitted", "job-1", ""); err != nil {
		t.Fatal(err)
	}
}
