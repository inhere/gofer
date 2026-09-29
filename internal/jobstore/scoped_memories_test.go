package jobstore

import (
	"testing"
)

func TestScopedMemoryCRUDAndPermissions(t *testing.T) {
	s := openTest(t)
	global, err := s.PutScopedMemory(ScopedMemoryGlobal, "", "shared", "all agents", nil, "alice")
	if err != nil || global.Scope != ScopedMemoryGlobal {
		t.Fatalf("put global: %+v %v", global, err)
	}
	_, err = s.PutScopedMemory(ScopedMemoryProject, "proj-a", "private", "project only", []string{"agent:claude"}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListScopedMemories(ScopedMemoryProject, "proj-a", "", []string{"agent:claude"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("list project: %+v %v", rows, err)
	}
	if err := s.DeleteScopedMemory(ScopedMemoryProject, "proj-a", "private", "alice"); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ListScopedMemories(ScopedMemoryProject, "proj-a", "", nil); err != nil || len(rows) != 0 {
		t.Fatalf("deleted memory visible: %+v %v", rows, err)
	}
	row, err := s.GetScopedMemory(ScopedMemoryProject, "proj-a", "private")
	if err != nil || !row.Deleted {
		t.Fatalf("tombstone missing: %+v %v", row, err)
	}
}
