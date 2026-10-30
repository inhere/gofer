package memorysynthesis

import (
	"testing"
)

func TestBuildScrubsDeduplicatesAndDigests(t *testing.T) {
	items := []Item{
		{ID: "2", Kind: "job", OccurredAt: "2026-10-28T02:00:00Z", Title: "done", Text: "token=secret-value", SourceRef: "job:2#report"},
		{ID: "1", Kind: "job", OccurredAt: "2026-10-28T01:00:00Z", Title: "same", Text: "fact", SourceRef: "job:1#report"},
		{ID: "3", Kind: "job", OccurredAt: "2026-10-28T03:00:00Z", Title: "same", Text: "fact", SourceRef: "job:3#report"},
	}
	a, err := Build("proj", "2026-10-28", "+08:00", items, Options{MaxInputBytes: 24000})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Items) != 2 || a.Items[0].SourceRef != "job:1#report" {
		t.Fatalf("items=%+v", a.Items)
	}
	if a.Items[0].SourceRefs == nil || len(a.Items[0].SourceRefs) != 1 || a.Items[0].SourceRefs[0] != "job:3#report" {
		t.Fatalf("refs=%v", a.Items[0].SourceRefs)
	}
	if a.Items[1].Text != "[redacted]" || a.Items[1].Sensitivity != "redacted" {
		t.Fatalf("scrub=%+v", a.Items[1])
	}
	b, err := Build("proj", "2026-10-28", "+08:00", items, Options{MaxInputBytes: 24000})
	if err != nil || a.InputDigest != b.InputDigest {
		t.Fatalf("digest not stable: %q %q %v", a.InputDigest, b.InputDigest, err)
	}
}

func TestBuildBudgetAndPerKindLimit(t *testing.T) {
	items := []Item{{Kind: "work", SourceRef: "a", Text: "12345"}, {Kind: "work", SourceRef: "b", Text: "67890"}}
	got, err := Build("p", "2026-10-28", "UTC", items, Options{MaxInputBytes: 280, MaxItemsPerKind: 1, MaxRunesPerText: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Omitted.Items == 0 {
		t.Fatalf("got=%+v", got)
	}
}
