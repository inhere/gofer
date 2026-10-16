package jobstore

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/tracker"
)

func TestMigrateAddsServerCreatedToOldTrackerIssues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-tracker.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE tracker_issues (
  tracker_id TEXT NOT NULL,
  issue_id TEXT NOT NULL,
  body_json TEXT NOT NULL,
  rev INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (tracker_id, issue_id)
)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO tracker_issues(tracker_id,issue_id,body_json,rev,updated_at) VALUES('t','p-0001','{}',1,'x')`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()
	assert.True(t, tableHasColumn(t, s, "tracker_issues", "server_created"))
	recs, err := s.ListTrackerIssues("t", -1)
	assert.NoErr(t, err)
	assert.Len(t, recs, 1)
	assert.False(t, recs[0].ServerCreated) // an existing row is not server-created
}

func TestCreateServerTrackerIssueNeedsPrefix(t *testing.T) {
	s := openTest(t)
	_, err := s.CreateServerTrackerIssue("nope", tracker.Issue{Title: "x"}, "u")
	assert.ErrIs(t, err, ErrTrackerRepoUnknown)
	assert.NoErr(t, s.UpsertTrackerRepo(TrackerRepo{TrackerID: "r1"}))
	_, err = s.CreateServerTrackerIssue("r1", tracker.Issue{Title: "x"}, "u")
	assert.ErrIs(t, err, ErrTrackerNoPrefix)
	assert.NoErr(t, s.UpsertTrackerRepo(TrackerRepo{TrackerID: "r1", Prefix: "ab"}))
	rec, err := s.CreateServerTrackerIssue("r1", tracker.Issue{Title: "x", Parent: "zz", Deps: []tracker.Dep{{ID: "zz"}}}, "u")
	assert.NoErr(t, err)
	assert.True(t, strings.HasPrefix(rec.ID, "ab-"))
	assert.True(t, rec.ServerCreated)
	var is tracker.Issue
	assert.NoErr(t, json.Unmarshal(rec.Body, &is))
	assert.Eq(t, "", is.Parent)
	assert.Len(t, is.Deps, 0)
}

func TestClearServerCreatedPulledByCursor(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertTrackerRepo(TrackerRepo{TrackerID: "r1", Prefix: "ab"}))
	a, _ := s.CreateServerTrackerIssue("r1", tracker.Issue{Title: "a"}, "u")
	b, _ := s.CreateServerTrackerIssue("r1", tracker.Issue{Title: "b"}, "u")
	assert.NoErr(t, s.ClearServerCreatedPulled("r1", 0)) // no cursor proves nothing
	assert.NoErr(t, s.ClearServerCreatedPulled("r1", a.ChangedSeq))
	recs, _ := s.ListTrackerIssues("r1", 0)
	flag := map[string]bool{}
	for _, r := range recs {
		flag[r.ID] = r.ServerCreated
	}
	assert.False(t, flag[a.ID])
	assert.True(t, flag[b.ID])
}

// The rename rewrites the other issues' parent / deps and the work links, journals the
// linked item, and a push with a base revision that includes the record only clears the flag.
func TestSyncRenamesServerCreatedIssueOnCollision(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertTrackerRepo(TrackerRepo{TrackerID: "r1", Prefix: "ab"}))
	srv, err := s.CreateServerTrackerIssue("r1", tracker.Issue{Title: "server"}, "u")
	assert.NoErr(t, err)
	// another mirrored issue that (added on the web) refers to the server one
	ref := tracker.Issue{ID: "ab-ref1", Title: "ref", Parent: srv.ID, Deps: []tracker.Dep{{ID: srv.ID, Type: "blocks"}}, Status: "open", Type: "task"}
	rb, _ := json.Marshal(ref)
	assert.NoErr(t, s.UpsertTrackerIssue(TrackerRecord{TrackerID: "r1", ID: ref.ID, Body: rb, Rev: 1, UpdatedAt: "x"}))
	w, err := s.CreateWorkItem(WorkItemInput{Title: "linked", By: "u"})
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(w.ID, WorkLinkIssue, srv.ID, "u")
	assert.NoErr(t, err)
	links, _ := s.ListWorkLinks(w.ID)
	assert.True(t, links[0].PendingSync)

	pushed, _ := json.Marshal(tracker.Issue{ID: srv.ID, Title: "repo", Status: "open", Type: "task"})
	res, err := s.SyncTrackerIssue(TrackerRecord{TrackerID: "r1", ID: srv.ID, Body: pushed, UpdatedAt: "now"}, 0)
	assert.NoErr(t, err)
	assert.False(t, res.Conflict)
	assert.NotNil(t, res.Renamed)
	assert.Eq(t, srv.ID, res.Renamed.From)
	assert.NotEq(t, srv.ID, res.Renamed.To)
	assert.Eq(t, int64(1), res.Rec.Rev)

	recs, _ := s.ListTrackerIssues("r1", 0)
	byID := map[string]TrackerRecord{}
	for _, r := range recs {
		byID[r.ID] = r
	}
	assert.Len(t, recs, 3)
	assert.False(t, byID[srv.ID].ServerCreated)
	assert.True(t, byID[res.Renamed.To].ServerCreated)
	var moved, refd tracker.Issue
	assert.NoErr(t, json.Unmarshal(byID[res.Renamed.To].Body, &moved))
	assert.NoErr(t, json.Unmarshal(byID["ab-ref1"].Body, &refd))
	assert.Eq(t, res.Renamed.To, moved.ID)
	assert.Eq(t, "server", moved.Title)
	assert.Eq(t, res.Renamed.To, refd.Parent)
	assert.Eq(t, res.Renamed.To, refd.Deps[0].ID)

	links, _ = s.ListWorkLinks(w.ID)
	assert.Len(t, links, 1)
	assert.Eq(t, res.Renamed.To, links[0].Ref)
	j, _ := s.ListWorkJournal(w.ID, 20, 0)
	found := false
	for _, e := range j {
		if strings.Contains(e.Text, srv.ID+" -> "+res.Renamed.To) {
			found = true
		}
	}
	assert.True(t, found)

	// A base revision that already includes the record is an ordinary update: no rename,
	// and the push clears the flag.
	other, _ := s.CreateServerTrackerIssue("r1", tracker.Issue{Title: "other"}, "u")
	res2, err := s.SyncTrackerIssue(TrackerRecord{TrackerID: "r1", ID: other.ID, Body: other.Body, UpdatedAt: "now"}, other.Rev)
	assert.NoErr(t, err)
	assert.Nil(t, res2.Renamed)
	recs, _ = s.ListTrackerIssues("r1", 0)
	for _, r := range recs {
		if r.ID == other.ID {
			assert.False(t, r.ServerCreated)
		}
	}
}
