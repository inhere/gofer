package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// The home page's new shape (design 2026-11-01 §4.3): the 「等我」 list, what is running, and no
// card made of a model's tidy-up suggestion. There is no legacy `decisions` superset any more.
func TestTodayShapeWaitingRunningAndNoSuggestionCards(t *testing.T) {
	s := newTestServer(t, testToken, false)
	registerSessionInProject(t, s, "sess-run-1", "/ws/a", "p1")
	registerSessionInProject(t, s, "sess-run-2", "/ws/a", "p1")
	for _, sid := range []string{"sess-run-1", "sess-run-2"} {
		resp := do(t, s, http.MethodPost, "/v1/sessions/"+sid+"/heartbeat", testToken, map[string]any{"event": "UserPromptSubmit", "state": "running", "title": "x"})
		resp.Body.Close()
	}
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "有模型建议的事"}, 200)
	if _, err := s.work.Store().UpsertWorkSuggestion(jobstore.WorkSuggestion{
		WorkItemID: w.ID, Field: jobstore.SuggestStatusHint, Value: jobstore.WorkReview, By: "summarizer"}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		todayResp
		Waiting []struct {
			Key  string `json:"key"`
			Kind string `json:"kind"`
		} `json:"waiting"`
		Running struct {
			Sessions int `json:"sessions"`
			Jobs     int `json:"jobs"`
		} `json:"running"`
		Digest struct {
			Commentary string `json:"commentary"`
		} `json:"digest"`
	}
	todayCall(t, s, http.MethodGet, "/v1/today", testToken, nil, 200, &got)
	if got.Running.Sessions != 2 || got.Running.Jobs != 0 {
		t.Fatalf("running = %+v, want 2 sessions and no jobs", got.Running)
	}
	if got.Digest.Commentary != "" {
		t.Fatalf("digest still carries a model commentary: %q", got.Digest.Commentary)
	}
	for _, c := range got.Waiting {
		if c.Kind == "suggestion" || c.Kind == "work" {
			t.Fatalf("waiting carries a %s card: %+v", c.Kind, c)
		}
	}
}

func TestArchiveAutoWorkItemsRoute(t *testing.T) {
	s := newTestServer(t, testToken, false)
	mk := func(title, origin string) string {
		w, err := s.work.Store().CreateWorkItem(jobstore.WorkItemInput{Title: title, Source: origin, By: "system"})
		if err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	auto1, auto2 := mk("auto-1", jobstore.WorkOriginAuto), mk("auto-2", jobstore.WorkOriginAuto)
	mine := mk("mine", jobstore.WorkOriginHuman)

	// Only a person may run the cleanup: a job credential is refused before the handler.
	for name, kind := range map[string]string{"member": jobstore.JobCredentialMember, "leader": jobstore.JobCredentialLeader, "steward": jobstore.JobCredentialSteward} {
		tok := seedJobToken(t, s, "job-arch-"+name, kind, "")
		resp := do(t, s, http.MethodPost, "/v1/work-items/archive-auto", tok, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s credential archive-auto = %d, want 403", name, resp.StatusCode)
		}
	}
	if resp := do(t, s, http.MethodPost, "/v1/work-items/archive-auto", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous archive-auto = %d, want 401", resp.StatusCode)
	}

	type result struct {
		Archived int      `json:"archived"`
		IDs      []string `json:"ids"`
		DryRun   bool     `json:"dry_run"`
	}
	var dry, res, again result
	todayCall(t, s, http.MethodPost, "/v1/work-items/archive-auto?dry_run=1", testToken, nil, 200, &dry)
	if !dry.DryRun || dry.Archived != 0 || len(dry.IDs) != 2 {
		t.Fatalf("dry run = %+v", dry)
	}
	if got := workGet(t, s, "/v1/work-items/"+auto1); got.Status != "active" {
		t.Fatalf("dry run changed %s: %+v", auto1, got)
	}
	todayCall(t, s, http.MethodPost, "/v1/work-items/archive-auto", testToken, nil, 200, &res)
	if res.Archived != 2 || len(res.IDs) != 2 {
		t.Fatalf("archive = %+v", res)
	}
	for _, id := range []string{auto1, auto2} {
		if got := workGet(t, s, "/v1/work-items/"+id); got.Status != "dropped" {
			t.Fatalf("%s status = %s, want dropped", id, got.Status)
		}
	}
	if got := workGet(t, s, "/v1/work-items/"+mine); got.Status != "active" {
		t.Fatalf("a person's item was archived: %+v", got)
	}
	todayCall(t, s, http.MethodPost, "/v1/work-items/archive-auto", testToken, nil, 200, &again)
	if again.Archived != 0 || len(again.IDs) != 0 {
		t.Fatalf("second archive = %+v, want nothing (idempotent)", again)
	}
	rows, err := s.work.Store().ListAuditEvents(jobstore.WorkArchivedAutoEvent, "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("audit rows = %+v err=%v", rows, err)
	}
}
