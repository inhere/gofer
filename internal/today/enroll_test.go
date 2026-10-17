package today

import (
	"strings"
	"testing"
)

func TestWorkerEnrollCardNoneNoCard(t *testing.T) {
	if _, ok := WorkerEnrollCard(nil); ok {
		t.Fatal("no pending requests must produce no card")
	}
}

func TestWorkerEnrollCardShape(t *testing.T) {
	c, ok := WorkerEnrollCard([]EnrollPending{
		{Hostname: "box-b", RequestedID: "gpu2", CreatedAt: 200, DecideUntil: 900},
		{Hostname: "box-a", RequestedID: "gpu1", CreatedAt: 100, DecideUntil: 500},
	})
	if !ok {
		t.Fatal("want a card")
	}
	if c.Kind != KindWorkerEnroll || c.Key != WorkerEnrollCardKey || c.Urgency != UrgencyNow {
		t.Fatalf("card = %+v", c)
	}
	if c.Title != "2 个 worker 申请注册" || c.ExpiresAt != 500 || c.WaitingSince != 100 {
		t.Fatalf("title/expiry = %q %d %d", c.Title, c.ExpiresAt, c.WaitingSince)
	}
	if !strings.Contains(c.Summary, "box-a") || !strings.Contains(c.Summary, "gpu2") {
		t.Fatalf("summary = %q", c.Summary)
	}
	if len(c.Actions) != 1 || c.Actions[0].Label != "去处理" || c.Actions[0].Value != "/runners#enrollments" {
		t.Fatalf("actions = %+v", c.Actions)
	}
}

func TestWorkerEnrollCardSummaryIsCapped(t *testing.T) {
	var in []EnrollPending
	for i := 0; i < 6; i++ {
		in = append(in, EnrollPending{Hostname: "h", RequestedID: "id", CreatedAt: 1, DecideUntil: 2})
	}
	c, _ := WorkerEnrollCard(in)
	if !strings.Contains(c.Summary, "等 6 个") {
		t.Fatalf("summary = %q", c.Summary)
	}
}
