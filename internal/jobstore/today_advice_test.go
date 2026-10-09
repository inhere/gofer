package jobstore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDecisionAdviceCRUDAndAuditCount(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "advice.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_760_000_000, 0)
	s.SetClock(func() time.Time { return now })

	if _, err := s.UpsertDecisionAdvice(DecisionAdvice{}); err == nil {
		t.Fatal("empty card key accepted")
	}
	a, err := s.UpsertDecisionAdvice(DecisionAdvice{CardKey: "review:j1", Text: "可以通过", ActionID: "accept", Digest: "a\nb", By: "steward(codex)", JobID: "st-1"})
	if err != nil || a.At != now.Unix() {
		t.Fatalf("upsert = %+v, %v", a, err)
	}
	// Replace in place.
	if _, err := s.UpsertDecisionAdvice(DecisionAdvice{CardKey: "review:j1", Text: "建议退回", ActionID: "rerun", At: now.Unix() + 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertDecisionAdvice(DecisionAdvice{CardKey: "work:w1", Text: "背景"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetDecisionAdvice("review:j1")
	if err != nil || !ok || got.Text != "建议退回" || got.ActionID != "rerun" || got.Digest != "" || got.At != now.Unix()+5 {
		t.Fatalf("get = %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := s.GetDecisionAdvice("nope"); ok {
		t.Fatal("missing advice found")
	}
	list, err := s.ListDecisionAdvice()
	if err != nil || len(list) != 2 || list[0].CardKey != "work:w1" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := s.DeleteDecisionAdvice("work:w1", "missing"); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.ListDecisionAdvice(); len(list) != 1 {
		t.Fatalf("after delete = %+v", list)
	}

	for _, k := range []string{"review:j1", "review:j1", "work:w1"} {
		if _, err := s.AppendAuditEvent(TodayAdviceAudit, k, "steward(codex)", "{}"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppendAuditEvent(TodayAdviceAudit, "work:w2", "human:me", "{}"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountAuditTargetsSince(TodayAdviceAudit, "steward", now.Unix()); err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}
	if n, _ := s.CountAuditTargetsSince(TodayAdviceAudit, "", now.Unix()); n != 3 {
		t.Fatalf("count all = %d", n)
	}
	if n, _ := s.CountAuditTargetsSince(TodayAdviceAudit, "steward", now.Unix()+1); n != 0 {
		t.Fatalf("count after = %d", n)
	}
}
