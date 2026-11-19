package jobstore

import "testing"

func TestWaitingNoticeOneOpenRowPerSubject(t *testing.T) {
	s := openTest(t)
	a, existed, err := s.EnterWaitingNotice("u1", "session:s1", "answer", 100)
	if err != nil || existed {
		t.Fatalf("first enter: existed=%v err=%v", existed, err)
	}
	b, existed, err := s.EnterWaitingNotice("u1", "session:s1", "answer", 200)
	if err != nil || !existed || b.ID != a.ID || b.EnteredAt != 100 {
		t.Fatalf("second enter must return the open row: %+v existed=%v err=%v", b, existed, err)
	}
	if err := s.MarkWaitingNotified(a.ID, 101); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkWaitingReminded(a.ID, 150); err != nil {
		t.Fatal(err)
	}
	open, err := s.ListOpenWaitingNotices()
	if err != nil || len(open) != 1 || open[0].NotifiedAt != 101 || open[0].RemindedAt != 150 || open[0].ClearedAt != 0 {
		t.Fatalf("open rows = %+v err=%v", open, err)
	}
	// leaving closes the stay; entering again is a new row
	if err := s.ClearWaitingNotice(a.ID, 300); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.ListOpenWaitingNotices(); len(open) != 0 {
		t.Fatalf("a cleared subject is still open: %+v", open)
	}
	c, existed, err := s.EnterWaitingNotice("u1", "session:s1", "answer", 400)
	if err != nil || existed || c.ID == a.ID || c.NotifiedAt != 0 {
		t.Fatalf("re-entry must be a new row: %+v existed=%v err=%v", c, existed, err)
	}
	if _, _, err := s.EnterWaitingNotice("u1", " ", "answer", 1); err == nil {
		t.Fatal("an empty subject key must be refused")
	}
}
