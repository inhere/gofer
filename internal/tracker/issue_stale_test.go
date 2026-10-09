package tracker

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestListIssuesStale(t *testing.T) {
	s := primeTestStore(t)
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	assert.Require(t, assert.NoErr(t, s.WriteIssues([]Issue{
		{ID: "s-fresh", Title: "fresh", Status: "open", CreatedAt: "2026-10-01T00:00:00Z", UpdatedAt: "2026-10-01T00:00:00Z"},
		{ID: "s-old", Title: "old", Status: "open", CreatedAt: "2026-07-01T00:00:00Z", UpdatedAt: "2026-08-01T00:00:00Z"},
		{ID: "s-older", Title: "older", Status: "in_progress", CreatedAt: "2026-06-01T00:00:00Z", StartedAt: "2026-06-02T00:00:00Z"},
		{ID: "s-blocked", Title: "blocked", Status: "blocked", CreatedAt: "2026-06-01T00:00:00Z"},
		{ID: "s-closed", Title: "closed", Status: "closed", CreatedAt: "2026-06-01T00:00:00Z"},
	})))
	ids := func(filter IssueFilter) []string {
		items, err := s.ListIssues(filter)
		assert.Require(t, assert.NoErr(t, err))
		out := []string{}
		for _, item := range items {
			out = append(out, item.ID)
		}
		return out
	}
	// Default: open + in_progress, oldest first.
	assert.Eq(t, []string{"s-older", "s-old"}, ids(IssueFilter{StaleDays: 30, Now: now}))
	assert.Eq(t, []string{"s-older"}, ids(IssueFilter{StaleDays: 100, Now: now}))
	assert.Eq(t, []string{"s-blocked"}, ids(IssueFilter{StaleDays: 30, Status: "blocked", Now: now}))
	assert.Eq(t, []string{"s-old", "s-older"}, ids(IssueFilter{StaleDays: 30, Reverse: true, Now: now}))
	assert.Eq(t, []string{"s-old", "s-older"}, ids(IssueFilter{StaleDays: 30, Sort: "priority", Now: now}))
	assert.Eq(t, "2026-06-02T00:00:00Z", IssueTouchedAt(Issue{CreatedAt: "2026-06-01T00:00:00Z", StartedAt: "2026-06-02T00:00:00Z"}))
}
