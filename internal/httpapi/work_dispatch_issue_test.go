package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

func TestWorkDispatchToIssue(t *testing.T) {
	s := newTestServer(t, testToken, false)
	s.SetTrackerStore(s.jobs.Meta())
	meta := s.jobs.Meta()
	if err := meta.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "r1", ProjectKey: "self", Prefix: "zz"}); err != nil {
		t.Fatal(err)
	}
	if err := meta.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "bare", ProjectKey: "self"}); err != nil {
		t.Fatal(err)
	}
	w, err := meta.CaptureWorkItem(jobstore.CaptureInput{Text: "login page 500s", Kind: "issue", ProjectKey: "self", By: "t"})
	if err != nil {
		t.Fatal(err)
	}

	// no tracker_id -> 400; a repo without a prefix -> 409; the item stays captured.
	for body, want := range map[string]int{"": http.StatusBadRequest, "bare": http.StatusConflict, "missing": http.StatusNotFound} {
		resp := dispatch(t, s, testToken, w.ID, map[string]any{"to": "issue", "tracker_id": body})
		if resp.StatusCode != want {
			t.Fatalf("tracker_id=%q = %d, want %d", body, resp.StatusCode, want)
		}
		resp.Body.Close()
	}
	if d := itemDetail(t, s, w.ID); d.Status != jobstore.WorkCaptured {
		t.Fatalf("status after refusals = %q", d.Status)
	}

	resp := dispatch(t, s, testToken, w.ID, map[string]any{"to": "issue", "tracker_id": "r1", "title": "Login 500"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dispatch = %d", resp.StatusCode)
	}
	resp.Body.Close()
	d := itemDetail(t, s, w.ID)
	if d.Status != jobstore.WorkParked {
		t.Fatalf("status = %q, want parked", d.Status)
	}
	links, err := meta.ListWorkLinks(w.ID)
	if err != nil || len(links) != 1 || links[0].Kind != jobstore.WorkLinkIssue || !strings.HasPrefix(links[0].Ref, "zz-") || !links[0].PendingSync {
		t.Fatalf("links = %+v err=%v", links, err)
	}
	recs, _ := meta.ListTrackerIssues("r1", 0)
	if len(recs) != 1 || !recs[0].ServerCreated || recs[0].ID != links[0].Ref {
		t.Fatalf("mirror = %+v", recs)
	}
	var is tracker.Issue
	_ = json.Unmarshal(recs[0].Body, &is)
	if is.Title != "Login 500" || is.Type != "bug" || !strings.Contains(is.Description, w.ID) {
		t.Fatalf("issue = %+v", is)
	}
	// The title was shortened on the way over, so the captured text has to be in the description
	// (gofer-wcga).
	if !strings.Contains(is.Description, "login page 500s") || !strings.Contains(is.Description, "captured by t at ") {
		t.Fatalf("description lost the captured text or its source: %q", is.Description)
	}
	if j := journalText(t, s, w.ID); !strings.Contains(j, links[0].Ref) {
		t.Fatalf("journal lacks the issue id: %q", j)
	}
	// a second dispatch of the same item is refused and creates nothing
	resp = dispatch(t, s, testToken, w.ID, map[string]any{"to": "issue", "tracker_id": "r1"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second dispatch = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
	if recs, _ = meta.ListTrackerIssues("r1", 0); len(recs) != 1 {
		t.Fatalf("second dispatch created an issue: %d", len(recs))
	}

	// kinds other than issue become tasks
	idea, _ := meta.CaptureWorkItem(jobstore.CaptureInput{Text: "faster export", Kind: "idea", ProjectKey: "self", By: "t"})
	resp = dispatch(t, s, testToken, idea.ID, map[string]any{"to": "issue", "tracker_id": "r1"})
	resp.Body.Close()
	links, _ = meta.ListWorkLinks(idea.ID)
	recs, _ = meta.ListTrackerIssues("r1", 0)
	for _, r := range recs {
		if len(links) == 1 && r.ID == links[0].Ref {
			_ = json.Unmarshal(r.Body, &is)
		}
	}
	if is.Type != "task" || is.Title != "faster export" {
		t.Fatalf("idea issue = %+v", is)
	}
}
