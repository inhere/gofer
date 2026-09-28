package tracker

import (
	"reflect"
	"testing"
)

func TestSyncThreeWayMerge(t *testing.T) {
	base := SyncSnapshot{
		Issues:   []Issue{{ID: "p4-1", Title: "base", Status: "open", UpdatedAt: "2026-09-29T00:00:00Z"}},
		Memories: []Memory{{Key: "k", Content: "base", UpdatedAt: "2026-09-29T00:00:00Z", By: "base"}},
	}
	local := cloneSnapshot(base)
	local.Issues[0].Description = "local"
	local.Issues[0].Tags = []string{"local"}
	local.Issues[0].Status = "in_progress"
	local.Memories[0].Tags = []string{"a"}
	local.Memories[0].UpdatedAt = "2026-09-29T00:00:01Z"
	remote := cloneSnapshot(base)
	remote.Issues[0].Status = "blocked"
	remote.Issues[0].Tags = []string{"remote"}
	remote.Issues[0].UpdatedAt = "2026-09-29T00:00:02Z"
	remote.Memories[0].Content = "remote"
	remote.Memories[0].Tags = []string{"b"}
	remote.Memories[0].UpdatedAt = "2026-09-29T00:00:03Z"
	merged, report := ThreeWayMerge(base, local, remote)
	if got := merged.Issues[0].Description; got != "local" {
		t.Fatalf("local scalar lost: %q", got)
	}
	if got := merged.Issues[0].Status; got != "blocked" {
		t.Fatalf("remote scalar lost: %q", got)
	}
	if !reflect.DeepEqual(merged.Issues[0].Tags, []string{"local", "remote"}) {
		t.Fatalf("tags = %#v", merged.Issues[0].Tags)
	}
	if got := merged.Memories[0].Content; got != "remote" {
		t.Fatalf("memory scalar = %q", got)
	}
	if !reflect.DeepEqual(merged.Memories[0].Tags, []string{"a", "b"}) {
		t.Fatalf("memory tags = %#v", merged.Memories[0].Tags)
	}
	if len(report.Conflicts) == 0 {
		t.Fatal("expected scalar conflict report")
	}
}

func TestSyncOfflineThenCatchUp(t *testing.T) {
	base := SyncSnapshot{Memories: []Memory{{Key: "k", Content: "one", UpdatedAt: "2026-09-29T00:00:00Z"}}}
	local := SyncSnapshot{Memories: []Memory{{Key: "k", Content: "two", UpdatedAt: "2026-09-29T00:00:01Z"}}}
	state := SyncState{Base: base}
	if err := state.Apply(local, nil); err != nil {
		t.Fatal(err)
	}
	if state.Pending() == 0 {
		t.Fatal("offline local change was not retained")
	}
	remote := SyncSnapshot{Memories: []Memory{{Key: "k", Content: "three", UpdatedAt: "2026-09-29T00:00:02Z"}}}
	if err := state.Apply(local, &remote); err != nil {
		t.Fatal(err)
	}
	if state.Pending() != 0 {
		t.Fatalf("pending after catch-up = %d", state.Pending())
	}
	if err := state.Apply(local, &remote); err != nil {
		t.Fatal(err)
	}
}

func TestJobIssueLinkAppendsNotes(t *testing.T) {
	issue := Issue{ID: "p4-1", Status: "open", UpdatedAt: "2026-09-29T00:00:00Z"}
	started := LinkIssueToJob(issue, JobIssueEvent{JobID: "job-1", Phase: "started", At: "2026-09-29T00:00:01Z"})
	if started.Status != "in_progress" {
		t.Fatalf("started status = %q", started.Status)
	}
	finished := LinkIssueToJob(started, JobIssueEvent{JobID: "job-1", Phase: "finished", Status: "done", At: "2026-09-29T00:00:02Z", Commits: []string{"abc123"}, Uncommitted: []string{"x.go"}})
	if len(finished.Notes) != 1 || finished.Notes[0].Text == "" {
		t.Fatalf("finish notes = %#v", finished.Notes)
	}
}
