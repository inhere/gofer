package tracker

import (
	"strings"
	"testing"
)

func seed(t *testing.T, s *Store, items ...Issue) {
	t.Helper()
	if err := s.WriteIssues(items); err != nil {
		t.Fatal(err)
	}
}

func i2p(v int) *int       { return &v }
func s2p(v string) *string { return &v }

func TestUpdateIssueFieldsAndClear(t *testing.T) {
	s := testStore(t)
	seed(t, s, Issue{ID: "x-1", Title: "one", Type: "task", Status: "open", Priority: 2, Description: "old", Assignee: "ann", Owner: "bob", Design: "d", AcceptanceCriteria: "a", Parent: "x-2"},
		Issue{ID: "x-2", Title: "two", Type: "epic", Status: "open"})
	got, err := s.UpdateIssue("x-1", IssuePatch{Type: "bug", Priority: i2p(0), Description: s2p("new"), Design: s2p("D2"), Acceptance: s2p("A2"), Assignee: s2p("cat"), Owner: s2p("dan")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "bug" || got.Priority != 0 || got.Description != "new" || got.Design != "D2" || got.AcceptanceCriteria != "A2" || got.Assignee != "cat" || got.Owner != "dan" {
		t.Fatalf("fields not applied: %+v", got)
	}
	// Priority 0 is a real value (pointer), not "unset".
	if got, _ = s.UpdateIssue("x-1", IssuePatch{Title: "renamed"}); got.Priority != 0 {
		t.Fatalf("untouched priority changed: %d", got.Priority)
	}
	got, err = s.UpdateIssue("x-1", IssuePatch{Clear: []string{"description", "design", "acceptance", "assignee", "owner", "parent"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "" || got.Design != "" || got.AcceptanceCriteria != "" || got.Assignee != "" || got.Owner != "" || got.Parent != "" {
		t.Fatalf("clear left values: %+v", got)
	}
	if _, err := s.UpdateIssue("x-1", IssuePatch{Clear: []string{"title"}}); err == nil || !strings.Contains(err.Error(), "cannot clear") {
		t.Fatalf("clearing title must fail: %v", err)
	}
	if _, err := s.UpdateIssue("x-1", IssuePatch{Priority: i2p(9)}); err == nil {
		t.Fatal("priority 9 must be rejected")
	}
}

func TestUpdateIssueParentValidation(t *testing.T) {
	s := testStore(t)
	seed(t, s, Issue{ID: "x-1", Title: "a", Status: "open"}, Issue{ID: "x-2", Title: "b", Status: "open", Parent: "x-1"}, Issue{ID: "x-3", Title: "c", Status: "open", Parent: "x-2"})
	for name, parent := range map[string]string{"self": "x-1", "missing": "x-9", "descendant": "x-3"} {
		if _, err := s.UpdateIssue("x-1", IssuePatch{Parent: s2p(parent)}); err == nil {
			t.Fatalf("%s parent %q must be rejected", name, parent)
		}
	}
	got, err := s.UpdateIssue("x-3", IssuePatch{Parent: s2p("x-1")})
	if err != nil || got.Parent != "x-1" {
		t.Fatalf("valid reparent: %+v %v", got, err)
	}
}

func TestStatusTransitionsKeepClosingFieldsConsistent(t *testing.T) {
	s := testStore(t)
	seed(t, s, Issue{ID: "x-1", Title: "a", Status: "open"})
	if _, err := s.CloseIssue("x-1", "done"); err != nil {
		t.Fatal(err)
	}
	got, err := s.UpdateIssue("x-1", IssuePatch{Status: "in_progress"})
	if err != nil || got.ClosedAt != "" || got.CloseReason != "" {
		t.Fatalf("leaving closed must clear closing fields: %+v %v", got, err)
	}
	got, err = s.UpdateIssue("x-1", IssuePatch{Status: "closed"})
	if err != nil || got.ClosedAt == "" {
		t.Fatalf("closing via update must stamp closed_at: %+v %v", got, err)
	}
}

func TestReopenAndComment(t *testing.T) {
	s := testStore(t)
	seed(t, s, Issue{ID: "x-1", Title: "a", Status: "open"})
	if _, err := s.ReopenIssue("x-1", "", "me"); err == nil {
		t.Fatal("reopening an open issue must fail")
	}
	if _, err := s.CloseIssue("x-1", "done"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReopenIssue("x-1", "regressed", "me")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "open" || got.ClosedAt != "" || got.CloseReason != "" || len(got.Comments) != 1 || !strings.Contains(got.Comments[0].Text, "regressed") || got.Comments[0].By != "me" {
		t.Fatalf("reopen: %+v", got)
	}
	got, err = s.AddComment("x-1", "second", "you")
	if err != nil || len(got.Comments) != 2 || got.Comments[1].Text != "second" {
		t.Fatalf("comment: %+v %v", got, err)
	}
	if _, err := s.AddComment("x-1", "  ", "you"); err == nil {
		t.Fatal("blank comment must be rejected")
	}
	if _, err := s.AddComment("nope", "x", "you"); err == nil {
		t.Fatal("unknown issue must be rejected")
	}
}

func TestDepAddRemoveAndRelations(t *testing.T) {
	s := testStore(t)
	seed(t, s, Issue{ID: "x-1", Title: "a", Status: "open"}, Issue{ID: "x-2", Title: "b", Status: "open", Parent: "x-1"}, Issue{ID: "x-3", Title: "c", Status: "open"})
	if _, err := s.AddDepType("x-2", "x-3", "blocks"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDepType("x-3", "x-2", "blocks"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("blocking cycle must be rejected: %v", err)
	}
	if _, err := s.AddDepType("x-1", "x-3", "related"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDepType("x-1", "x-3", "parent-child"); err == nil {
		t.Fatal("parent-child dep must be rejected (use the parent field)")
	}
	_, rel, err := s.Relations("x-3")
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Blocks) != 1 || rel.Blocks[0] != "x-2" || len(rel.Linked) != 1 || rel.Linked[0].ID != "x-1" || rel.Linked[0].Type != "related" {
		t.Fatalf("x-3 relations: %+v", rel)
	}
	_, rel, _ = s.Relations("x-2")
	if rel.Parent != "x-1" || len(rel.BlockedBy) != 1 || rel.BlockedBy[0].ID != "x-3" {
		t.Fatalf("x-2 relations: %+v", rel)
	}
	_, rel, _ = s.Relations("x-1")
	if len(rel.Children) != 1 || rel.Children[0] != "x-2" {
		t.Fatalf("x-1 relations: %+v", rel)
	}
	// A closed blocker stops blocking.
	if _, err := s.CloseIssue("x-3", "done"); err != nil {
		t.Fatal(err)
	}
	if _, rel, _ = s.Relations("x-2"); len(rel.BlockedBy) != 0 {
		t.Fatalf("closed blocker still blocking: %+v", rel)
	}
	if _, err := s.RemoveDep("x-2", "x-3", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveDep("x-2", "x-3", ""); err == nil {
		t.Fatal("removing a missing dependency must fail")
	}
	if got, _ := s.Issue("x-1"); len(got.Deps) != 1 {
		t.Fatalf("unrelated deps touched: %+v", got.Deps)
	}
	if _, err := s.RemoveDep("x-1", "x-3", "blocks"); err == nil {
		t.Fatal("--type must restrict removal to that kind")
	}
	if _, err := s.RemoveDep("x-1", "x-3", "related"); err != nil {
		t.Fatal(err)
	}
}

func TestListIssuesFiltersAndSort(t *testing.T) {
	s := testStore(t)
	seed(t, s,
		Issue{ID: "x-1", Title: "a", Status: "open", Priority: 3, Assignee: "ann", CreatedAt: "2026-01-03", UpdatedAt: "2026-02-01"},
		Issue{ID: "x-2", Title: "b", Status: "open", Priority: 1, Assignee: "bob", CreatedAt: "2026-01-02", UpdatedAt: "2026-03-01"},
		Issue{ID: "x-3", Title: "c", Status: "in_progress", Priority: 1, Assignee: "ann", CreatedAt: "2026-01-01", UpdatedAt: "2026-01-01"})
	ids := func(f IssueFilter) string {
		items, err := s.ListIssues(f)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.ID
		}
		return strings.Join(out, ",")
	}
	if got := ids(IssueFilter{}); got != "x-1,x-2,x-3" {
		t.Fatalf("default order: %s", got)
	}
	if got := ids(IssueFilter{Assignee: "ann"}); got != "x-1,x-3" {
		t.Fatalf("assignee: %s", got)
	}
	if got := ids(IssueFilter{Priority: i2p(1)}); got != "x-2,x-3" {
		t.Fatalf("priority: %s", got)
	}
	if got := ids(IssueFilter{Sort: "priority"}); got != "x-2,x-3,x-1" {
		t.Fatalf("sort priority: %s", got)
	}
	if got := ids(IssueFilter{Sort: "created"}); got != "x-3,x-2,x-1" {
		t.Fatalf("sort created: %s", got)
	}
	if got := ids(IssueFilter{Sort: "updated"}); got != "x-2,x-1,x-3" {
		t.Fatalf("sort updated (newest first): %s", got)
	}
	if got := ids(IssueFilter{Sort: "created", Reverse: true}); got != "x-1,x-2,x-3" {
		t.Fatalf("reverse: %s", got)
	}
	if got := ids(IssueFilter{Sort: "priority", Limit: 2}); got != "x-2,x-3" {
		t.Fatalf("limit: %s", got)
	}
	if _, err := s.ListIssues(IssueFilter{Sort: "nope"}); err == nil {
		t.Fatal("unknown sort must fail")
	}
}

func TestListMemoriesSearchesKeyAndContentCaseInsensitively(t *testing.T) {
	s := testStore(t)
	for _, kv := range [][2]string{{"deploy-notes", "Use the BLUE cluster"}, {"other", "nothing"}} {
		if _, err := s.SetMemory(kv[0], kv[1], "me"); err != nil {
			t.Fatal(err)
		}
	}
	for kw, want := range map[string]int{"blue": 1, "DEPLOY": 1, "": 2, "zzz": 0} {
		got, err := s.ListMemories(kw)
		if err != nil || len(got) != want {
			t.Fatalf("keyword %q: %d, %v (want %d)", kw, len(got), err, want)
		}
	}
}
