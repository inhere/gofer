package tracker

import (
	"reflect"
	"testing"
)

func TestThreeWayMergeKeepsRemovalsAndUnionsComments(t *testing.T) {
	base := Issue{ID: "x-1", Title: "t", Status: "open", Tags: []string{"a", "b"}, Deps: []Dep{{ID: "x-2", Type: "blocks"}}, UpdatedAt: "2026-01-01T00:00:00Z"}
	local := base
	local.Tags = []string{"a"} // local untagged b
	local.Deps = nil           // local removed the dep
	local.Comments = []Comment{{At: "2026-01-02T00:00:00Z", By: "l", Text: "from local"}}
	local.UpdatedAt = "2026-01-02T00:00:00Z"
	remote := base
	remote.Tags = []string{"a", "b", "c"} // remote added c, still has b
	remote.Comments = []Comment{{At: "2026-01-03T00:00:00Z", By: "r", Text: "from remote"}}
	remote.Parent = "x-9"
	remote.ExternalRef = "EXT-1"
	remote.UpdatedAt = "2026-01-03T00:00:00Z"
	merged, report := ThreeWayMerge(SyncSnapshot{Issues: []Issue{base}}, SyncSnapshot{Issues: []Issue{local}}, SyncSnapshot{Issues: []Issue{remote}})
	got := merged.Issues[0]
	if !reflect.DeepEqual(got.Tags, []string{"a", "c"}) {
		t.Fatalf("tags: removal of b lost or c dropped: %v", got.Tags)
	}
	if len(got.Deps) != 0 {
		t.Fatalf("a locally removed dependency was resurrected: %+v", got.Deps)
	}
	if len(got.Comments) != 2 {
		t.Fatalf("comments must be the union: %+v", got.Comments)
	}
	if got.Parent != "x-9" || got.ExternalRef != "EXT-1" {
		t.Fatalf("remote-only parent/external_ref lost: %+v", got)
	}
	if len(report.Conflicts) != 0 {
		t.Fatalf("no conflict expected: %+v", report.Conflicts)
	}
}

func TestThreeWayMergeReopenPropagatesClosedAt(t *testing.T) {
	base := Issue{ID: "x-1", Title: "t", Status: "closed", ClosedAt: "2026-01-01T00:00:00Z", CloseReason: "done", UpdatedAt: "2026-01-01T00:00:00Z"}
	local := base
	local.Status, local.ClosedAt, local.CloseReason, local.UpdatedAt = "open", "", "", "2026-01-02T00:00:00Z"
	merged, _ := ThreeWayMerge(SyncSnapshot{Issues: []Issue{base}}, SyncSnapshot{Issues: []Issue{local}}, SyncSnapshot{Issues: []Issue{base}})
	if got := merged.Issues[0]; got.Status != "open" || got.ClosedAt != "" || got.CloseReason != "" {
		t.Fatalf("reopen did not propagate: %+v", got)
	}
}
