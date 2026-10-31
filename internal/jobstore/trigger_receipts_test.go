package jobstore

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

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

func TestTriggerAuditStoresHashedEventAndNoRawID(t *testing.T) {
	s := openTest(t)
	const raw = "raw-delivery-id-xyz"
	ev := TriggerAuditEvent{Kind: "trigger.received", Trigger: "push", Owner: "automation", Project: "demo", Provider: "github", RequestID: "r1", EventHash: HashTriggerEventID("push", raw), Remote: "abcd"}
	if err := s.AppendTriggerAudit(ev); err != nil {
		t.Fatal(err)
	}
	var detail string
	if err := s.db.QueryRow(`SELECT detail_json FROM audit_events WHERE kind='trigger.received'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var d map[string]string
	if err := json.Unmarshal([]byte(detail), &d); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail, raw) || len(d["external_event_hash"]) != 16 || d["provider"] != "github" || d["project"] != "demo" || d["remote"] != "abcd" {
		t.Fatalf("detail=%s", detail)
	}
}

func TestReclaimTriggerReceiptOnlyTakesFailedOrStale(t *testing.T) {
	s := openTest(t)
	h := HashTriggerBody([]byte("b"))
	claim := func(id, status string, at int64) {
		if _, _, err := s.ClaimTriggerReceipt(TriggerReceipt{TriggerName: "t", ExternalEventID: id, RequestID: "r", BodyHash: h, ReceivedAt: at, ExpiresAt: 1 << 40, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	claim("sub", TriggerReceiptSubmitted, 10)
	claim("fail", TriggerReceiptFailed, 10)
	claim("stale", TriggerReceiptClaimed, 10)
	claim("fresh", TriggerReceiptClaimed, 100)
	for id, want := range map[string]bool{"sub": false, "fail": true, "stale": true, "fresh": false} {
		got, err := s.ReclaimTriggerReceipt("t", id, h, 200, 50)
		if err != nil || got != want {
			t.Fatalf("%s: got=%v err=%v want=%v", id, got, err, want)
		}
	}
	// A reused id with another body is never taken over.
	claim("other", TriggerReceiptFailed, 10)
	if got, _ := s.ReclaimTriggerReceipt("t", "other", "different", 200, 50); got {
		t.Fatal("reclaimed with a different body hash")
	}
	// The takeover is exclusive: the receipt is claimed and fresh again.
	if got, _ := s.ReclaimTriggerReceipt("t", "fail", h, 200, 50); got {
		t.Fatal("second reclaim also won")
	}
}

func TestConcurrentClaimHasOneWinner(t *testing.T) {
	s := openTest(t)
	h := HashTriggerBody([]byte("b"))
	const n = 20
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, dup, err := s.ClaimTriggerReceipt(TriggerReceipt{TriggerName: "t", ExternalEventID: "e", RequestID: "r" + strconv.Itoa(i), BodyHash: h, ReceivedAt: 1, ExpiresAt: 100})
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			if !dup {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners=%d", winners.Load())
	}
}

func TestPruneAndSettleTriggerReceipts(t *testing.T) {
	s := openTest(t)
	h := HashTriggerBody([]byte("b"))
	for _, id := range []string{"a", "b"} {
		exp := int64(100)
		if id == "b" {
			exp = 1000
		}
		if _, _, err := s.ClaimTriggerReceipt(TriggerReceipt{TriggerName: "t", ExternalEventID: id, RequestID: "r", BodyHash: h, ReceivedAt: 1, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.PruneTriggerReceipts(500); err != nil || n != 1 {
		t.Fatalf("pruned=%d err=%v", n, err)
	}
	if ok, _ := s.SettleTriggerReceipt("t", "b", TriggerReceiptSubmitted, TriggerReceiptFailed, "", "x"); ok {
		t.Fatal("settled from the wrong status")
	}
	if ok, err := s.SettleTriggerReceipt("t", "b", TriggerReceiptClaimed, TriggerReceiptFailed, "", "x"); err != nil || !ok {
		t.Fatalf("settle ok=%v err=%v", ok, err)
	}
}
