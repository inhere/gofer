package tracker

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".gofer", "tracker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return NewStore(dir)
}

func TestTrackerRoundTripStableOrder(t *testing.T) {
	s := testStore(t)
	issues := []Issue{
		{ID: "x-0003", Title: "third", Status: "open", CreatedAt: "2026-09-27T00:00:03Z"},
		{ID: "x-0001", Title: "first", Status: "open", CreatedAt: "2026-09-27T00:00:01Z"},
		{ID: "x-0002", Title: "second", Status: "open", CreatedAt: "2026-09-27T00:00:02Z"},
	}
	if err := s.WriteIssues(issues); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "issues.jsonl")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(first), "\n"), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"id":"x-0001"`) || !strings.Contains(lines[2], `"id":"x-0003"`) {
		t.Fatalf("unstable id order: %q", lines)
	}
	if !strings.HasPrefix(lines[0], `{"id":"x-0001","title":"first","type":`) {
		t.Fatalf("field order: %s", lines[0])
	}
	read, err := s.ReadIssues()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteIssues(read); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(first, again) {
		t.Fatal("read/write changed bytes")
	}
	read[1].Title = "changed"
	if err := s.WriteIssues(read); err != nil {
		t.Fatal(err)
	}
	changed, _ := os.ReadFile(path)
	next := strings.Split(strings.TrimSuffix(string(changed), "\n"), "\n")
	if lines[0] != next[0] || lines[1] == next[1] || lines[2] != next[2] {
		t.Fatalf("expected only second line to change: before=%q after=%q", lines, next)
	}
}

func TestTrackerLockContention(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
					if len(items) == 0 {
						items = []Issue{{ID: "x-0001", Title: "shared", Status: "open"}}
					}
					items[0].Notes = append(items[0].Notes, NoteEntry{Text: "note"})
					return items, nil
				}); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	items, err := s.ReadIssues()
	if err != nil || len(items) != 1 || len(items[0].Notes) != 100 {
		t.Fatalf("lost notes: len=%d err=%v", len(items[0].Notes), err)
	}
	lock := filepath.Join(s.Dir, ".local", "lock")
	stale := `{"pid":1,"host":"old","at":"2000-01-01T00:00:00Z"}`
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateIssues(func(items []Issue) ([]Issue, error) { return items, nil }); err != nil {
		t.Fatalf("expired lock not reclaimed: %v", err)
	}
	info, err := s.AcquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer info.Release()
	content, err := os.ReadFile(lock)
	if err != nil || !strings.Contains(string(content), `"pid":`) || !strings.Contains(string(content), `"host":`) || !strings.Contains(string(content), `"at":`) {
		t.Fatalf("unreadable lock: %q %v", content, err)
	}
}

func TestIssueReadyRespectsDeps(t *testing.T) {
	s := testStore(t)
	a := Issue{ID: "x-a", Title: "A", Status: "open", Priority: 2, CreatedAt: "2026-09-27T00:00:01Z"}
	b := Issue{ID: "x-b", Title: "B", Status: "open", Priority: 0, CreatedAt: "2026-09-27T00:00:02Z", Deps: []Dep{{ID: a.ID, Type: "blocks"}}}
	c := Issue{ID: "x-c", Title: "C", Status: "open", Priority: 1, CreatedAt: "2026-09-27T00:00:03Z"}
	if err := s.WriteIssues([]Issue{c, b, a}); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Ready()
	if err != nil {
		t.Fatal(err)
	}
	if got := issueIDs(ready); !reflect.DeepEqual(got, []string{"x-c", "x-a"}) {
		t.Fatalf("ready before close: %v", got)
	}
	a.Status = "closed"
	if err := s.WriteIssues([]Issue{c, b, a}); err != nil {
		t.Fatal(err)
	}
	ready, err = s.Ready()
	if err != nil {
		t.Fatal(err)
	}
	if got := issueIDs(ready); !reflect.DeepEqual(got, []string{"x-b", "x-c"}) {
		t.Fatalf("ready after close: %v", got)
	}
}

func issueIDs(items []Issue) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}

func TestIssueIDGeneration(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 25; i++ {
		id, err := s.NextIssueID("proj", "")
		if err != nil || !strings.HasPrefix(id, "proj-") || len(id) != len("proj-0000") {
			t.Fatalf("id=%q err=%v", id, err)
		}
		for _, ch := range id[len("proj-"):] {
			if !strings.ContainsRune("0123456789abcdefghijklmnopqrstuvwxyz", ch) {
				t.Fatalf("invalid base36 id: %s", id)
			}
		}
		if err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
			return append(items, Issue{ID: id, Title: id, Status: "open"}), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	parent := "proj-abcd"
	if err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
		return append(items, Issue{ID: parent, Title: "parent", Status: "open"}, Issue{ID: parent + ".1", Title: "child", Status: "open"}), nil
	}); err != nil {
		t.Fatal(err)
	}
	id, err := s.NextIssueID("proj", parent)
	if err != nil || id != parent+".2" {
		t.Fatalf("next child=%q err=%v", id, err)
	}
}
