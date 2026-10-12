package jobstore

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/tracker"
)

func globalRecords(t *testing.T, s *Store) map[string]TrackerRecord {
	t.Helper()
	recs, err := s.ListTrackerMemories(tracker.GlobalTrackerID, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]TrackerRecord{}
	for _, r := range recs {
		out[r.ID] = r
	}
	return out
}

// gofer-j6qn: the scoped API's global scope reads and writes the global tracker, so
// every write is a new revision a local copy can pull.
func TestGlobalScopeIsTheGlobalTracker(t *testing.T) {
	s := openTest(t)
	kind := tracker.MemoryKindRule
	if _, err := s.PutScopedMemoryPatch(ScopedMemoryGlobal, "", "lang", "reply in Chinese", []string{"style"},
		ScopedMemoryMetaPatch{Kind: &kind}, "alice"); err != nil {
		t.Fatal(err)
	}
	rec := globalRecords(t, s)["lang"]
	var body tracker.Memory
	if err := json.Unmarshal(rec.Body, &body); err != nil || rec.Rev != 1 || rec.Deleted ||
		body.Content != "reply in Chinese" || body.By != "alice" || body.Kind != tracker.MemoryKindRule || len(body.Tags) != 1 {
		t.Fatalf("record = %+v body %+v err %v", rec, body, err)
	}
	got, err := s.GetScopedMemory(ScopedMemoryGlobal, "", "lang")
	if err != nil || got.Content != "reply in Chinese" || got.UpdatedBy != "alice" || got.Kind != tracker.MemoryKindRule {
		t.Fatalf("get = %+v %v", got, err)
	}
	// An update keeps the meta it does not mention and bumps the revision.
	if _, err := s.PutScopedMemory(ScopedMemoryGlobal, "", "lang", "reply in Chinese, always", []string{"style"}, "bob"); err != nil {
		t.Fatal(err)
	}
	if rec := globalRecords(t, s)["lang"]; rec.Rev != 2 {
		t.Fatalf("rev after update = %d", rec.Rev)
	}
	if got, _ := s.GetScopedMemory(ScopedMemoryGlobal, "", "lang"); got.Kind != tracker.MemoryKindRule || got.UpdatedBy != "bob" {
		t.Fatalf("after update = %+v", got)
	}
	// A flag is a revision too (other machines must see it), without touching updated_at.
	before, _ := s.GetScopedMemory(ScopedMemoryGlobal, "", "lang")
	if _, err := s.FlagScopedMemory(ScopedMemoryGlobal, "", "lang", &tracker.MemoryFlag{By: "agent", Reason: "stale"}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetScopedMemory(ScopedMemoryGlobal, "", "lang")
	if globalRecords(t, s)["lang"].Rev != 3 || len(after.Flags) != 1 || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("flag: %+v", after)
	}
	if list, err := s.ListScopedMemories(ScopedMemoryGlobal, "", "chinese", nil); err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	// Delete writes a tombstone.
	if err := s.DeleteScopedMemory(ScopedMemoryGlobal, "", "lang", "carol"); err != nil {
		t.Fatal(err)
	}
	if rec := globalRecords(t, s)["lang"]; !rec.Deleted || rec.DeletedBy != "carol" || rec.Rev != 4 {
		t.Fatalf("tombstone = %+v", rec)
	}
	if list, _ := s.ListScopedMemories(ScopedMemoryGlobal, "", "", nil); len(list) != 0 {
		t.Fatalf("deleted memory listed: %+v", list)
	}
	if err := s.DeleteScopedMemory(ScopedMemoryGlobal, "", "lang", "carol"); !errors.Is(err, ErrScopedMemoryNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	// A memory deleted and written again comes back live.
	if _, err := s.PutScopedMemory(ScopedMemoryGlobal, "", "lang", "back", nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if rec := globalRecords(t, s)["lang"]; rec.Deleted || rec.Rev != 5 {
		t.Fatalf("revived = %+v", rec)
	}
	// Project scope stays where it was.
	if _, err := s.PutScopedMemory(ScopedMemoryProject, "p", "k", "v", nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := globalRecords(t, s)["k"]; ok {
		t.Fatal("a project memory landed in the global tracker")
	}
}

func TestGlobalTrackerIsNotARepository(t *testing.T) {
	s := openTest(t)
	if _, err := s.PutScopedMemory(ScopedMemoryGlobal, "", "k", "v", nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTrackerRepo(TrackerRepo{TrackerID: "tracker-0123456789"}); err != nil {
		t.Fatal(err)
	}
	repos, err := s.ListTrackerRepos()
	if err != nil || len(repos) != 1 || repos[0].TrackerID != "tracker-0123456789" {
		t.Fatalf("repos = %+v %v", repos, err)
	}
	if _, err := s.DeleteTracker(tracker.GlobalTrackerID); !errors.Is(err, ErrReservedTracker) {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.RenameTracker(tracker.GlobalTrackerID, "tracker-x"); !errors.Is(err, ErrReservedTracker) {
		t.Fatalf("rename: %v", err)
	}
	if _, err := s.RenameTracker("tracker-0123456789", tracker.GlobalTrackerID); !errors.Is(err, ErrReservedTracker) {
		t.Fatalf("rename onto: %v", err)
	}
}

// The global rows of scoped_memories move into the global tracker once; a key the
// tracker already holds keeps the later write.
func TestMigrateGlobalScopedMemories(t *testing.T) {
	s := openTest(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	insert := `INSERT INTO scoped_memories(scope,scope_key,key,content,tags_json,updated_at,updated_by,deleted,meta_json) VALUES(?,?,?,?,?,?,?,?,?)`
	exec(insert, "global", "", "old", "legacy rule", `["x"]`, "2026-10-01T00:00:00Z", "alice", 0, `{"kind":"rule","summary":"s"}`)
	exec(insert, "global", "", "gone", "deleted", `[]`, "2026-10-01T00:00:00Z", "alice", 1, ``)
	exec(insert, "global", "", "newer", "stale copy", `[]`, "2026-10-01T00:00:00Z", "alice", 0, ``)
	exec(insert, "project", "p", "proj", "stays", `[]`, "2026-10-01T00:00:00Z", "alice", 0, ``)
	// The tracker already holds a later "newer".
	if _, err := s.PutScopedMemory(ScopedMemoryGlobal, "", "newer", "fresh", nil, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateGlobalScopedMemories(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateGlobalScopedMemories(); err != nil { // idempotent
		t.Fatal(err)
	}
	old, err := s.GetScopedMemory(ScopedMemoryGlobal, "", "old")
	if err != nil || old.Content != "legacy rule" || old.Kind != tracker.MemoryKindRule || old.Summary != "s" ||
		old.UpdatedBy != "alice" || old.UpdatedAt != "2026-10-01T00:00:00Z" || len(old.Tags) != 1 {
		t.Fatalf("migrated = %+v %v", old, err)
	}
	if got, _ := s.GetScopedMemory(ScopedMemoryGlobal, "", "newer"); got.Content != "fresh" {
		t.Fatalf("newer = %+v", got)
	}
	if _, err := s.GetScopedMemory(ScopedMemoryGlobal, "", "gone"); !errors.Is(err, ErrScopedMemoryNotFound) {
		t.Fatalf("a deleted legacy row was migrated: %v", err)
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM scoped_memories WHERE scope='global'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d global rows left in scoped_memories", n)
	}
	if got, err := s.GetScopedMemory(ScopedMemoryProject, "p", "proj"); err != nil || got.Content != "stays" {
		t.Fatalf("project row = %+v %v", got, err)
	}
	if rec := globalRecords(t, s)["old"]; rec.Rev != 1 {
		t.Fatalf("migrated rev = %d", rec.Rev)
	}
}
