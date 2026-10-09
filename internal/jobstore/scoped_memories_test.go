package jobstore

import (
	"testing"

	"github.com/inhere/gofer/internal/tracker"
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

func TestScopedMemoryMetaRoundTripAndKeep(t *testing.T) {
	s := openTest(t)
	kind, summary, source := "rule", "一句话", "plan:p1"
	when := &tracker.MemoryWhen{Keywords: []string{"发版"}, Paths: []string{"web/**"}}
	put, err := s.PutScopedMemoryPatch(ScopedMemoryProject, "proj", "r", "body", []string{"web"}, ScopedMemoryMetaPatch{Kind: &kind, Summary: &summary, Source: &source, When: when}, "alice")
	if err != nil || put.Kind != "rule" || put.CreatedAt == "" {
		t.Fatalf("put: %+v %v", put, err)
	}
	got, err := s.GetScopedMemory(ScopedMemoryProject, "proj", "r")
	if err != nil || got.Kind != "rule" || got.Summary != summary || got.Source != source || got.When == nil || got.When.Paths[0] != "web/**" || got.CreatedAt != put.CreatedAt {
		t.Fatalf("get: %+v %v", got, err)
	}
	// A plain content/tags write (web / MCP) keeps the meta fields.
	kept, err := s.PutScopedMemory(ScopedMemoryProject, "proj", "r", "body v2", nil, "mcp")
	if err != nil || kept.Kind != "rule" || kept.Summary != summary || kept.CreatedAt != put.CreatedAt {
		t.Fatalf("keep meta: %+v %v", kept, err)
	}
	rows, err := s.ListScopedMemories(ScopedMemoryProject, "proj", "", nil)
	if err != nil || len(rows) != 1 || rows[0].Kind != "rule" || rows[0].When == nil {
		t.Fatalf("list: %+v %v", rows, err)
	}
	// A handoff gets the default expiry.
	handoff := "handoff"
	h, err := s.PutScopedMemoryPatch(ScopedMemoryGlobal, "", "h", "x", nil, ScopedMemoryMetaPatch{Kind: &handoff}, "alice")
	if err != nil || h.ExpiresAt == "" {
		t.Fatalf("handoff ttl: %+v %v", h, err)
	}
}
