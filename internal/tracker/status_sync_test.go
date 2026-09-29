package tracker

import "testing"

func TestStatusPendingSyncCountsUnsyncedEntries(t *testing.T) {
	s, _, err := Init(t.TempDir(), "p", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateIssue(Issue{Title: "x", Type: "task"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetMemory("k", "v", "test"); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.PendingSync != 2 {
		t.Fatalf("pending_sync=%d", st.PendingSync)
	}
}
