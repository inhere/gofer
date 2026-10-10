package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

func TestScopedDoctorReportFlaggedAndSkipsRepoChecks(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	meta := tracker.MemoryMeta{Kind: tracker.MemoryKindRule, Flags: []tracker.MemoryFlag{{Reason: "outdated", Job: "j1", By: "agent", At: now.Format(time.RFC3339)}}}
	items := []client.ScopedMemory{
		{Scope: "project", ScopeKey: "p", Key: "a", Content: "see /no/such/path.go and commit deadbeef12", UpdatedAt: now.Format(time.RFC3339), MemoryMeta: meta},
		{Scope: "project", ScopeKey: "p", Key: "gone", Content: "x", Deleted: true, MemoryMeta: meta},
	}
	r := scopedDoctorReport(items, now)
	if r.Checked != 1 || r.Flagged != 1 || len(r.Memories) != 1 {
		t.Fatalf("report = %+v", r)
	}
	var slugs []string
	for _, f := range r.Memories[0].Findings {
		slugs = append(slugs, f.Slug)
	}
	if len(slugs) != 1 || slugs[0] != tracker.DoctorFlagged {
		t.Fatalf("slugs = %v, want only flagged (path/commit checks skipped)", slugs)
	}
	if d := r.Memories[0].Findings[0].Detail; !strings.Contains(d, "outdated") || !strings.Contains(d, "job:j1") {
		t.Fatalf("detail = %q", d)
	}
}

func TestStaleCandidates(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	day := int64(24 * 3600)
	cands := []jobstore.MemoryCandidate{
		{ID: 1, Status: "pending", ProjectKey: "p", JobID: "j", Text: "old", CreatedAt: now.Unix() - 31*day},
		{ID: 2, Status: "pending", Text: "new", CreatedAt: now.Unix() - 29*day},
		{ID: 3, Status: "accepted", Text: "done", CreatedAt: now.Unix() - 90*day},
	}
	got := staleCandidates(cands, now)
	if len(got) != 1 || got[0].ID != 1 || got[0].AgeDays != 31 {
		t.Fatalf("got %+v", got)
	}
	if out := formatStaleCandidates(got); !strings.Contains(out, "#1 p j (31 days) old") {
		t.Fatalf("out = %q", out)
	}
	if formatStaleCandidates(nil) != "" {
		t.Fatal("empty list must print nothing")
	}
}
