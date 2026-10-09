package httpapi

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestTodaySnoozeEndpoints(t *testing.T) {
	s := newTestServer(t, testToken, false)
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "等我拍板"}, 200)
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, testToken, map[string]any{"status": "needs_me", "rev": w.Rev}, 200)
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{ID: "j-snz", ProjectKey: "p", Agent: "codex", Status: job.StatusRunning,
		RequestJSON: `{"title":"t"}`, StartedAt: now - 60, UpdatedAt: now - 60}); err != nil {
		t.Fatal(err)
	}
	if err := s.jobs.Meta().UpsertInteraction(jobstore.InteractionRecord{ID: "i1", JobID: "j-snz", Type: job.InteractionTypeQuestion,
		Prompt: "继续吗？", Status: "pending", CreatedAt: now - 30}); err != nil {
		t.Fatal(err)
	}
	workKey, intKey := "work:"+w.ID, "interaction:j-snz/i1"

	var got todayResp
	todayCall(t, s, http.MethodGet, "/v1/today", testToken, nil, 200, &got)
	if len(got.Decisions) != 2 || got.Snoozed != 0 {
		t.Fatalf("before = %+v", got)
	}

	// Bad bodies.
	todayCall(t, s, http.MethodPost, "/v1/today/snooze", testToken, map[string]any{"card_key": workKey}, 400, nil)
	todayCall(t, s, http.MethodPost, "/v1/today/snooze", testToken, map[string]any{"card_key": workKey, "until_at": now - 10}, 400, nil)
	todayCall(t, s, http.MethodPost, "/v1/today/snooze", testToken, map[string]any{"card_key": "work:nope", "until_at": now + 3600}, 404, nil)

	var sc map[string]any
	todayCall(t, s, http.MethodPost, "/v1/today/snooze", testToken, map[string]any{"card_key": workKey, "until_at": now + 3600}, 200, &sc)
	if sc["card_key"] != workKey || sc["title"] != "等我拍板" {
		t.Fatalf("snooze = %v", sc)
	}
	todayCall(t, s, http.MethodPost, "/v1/today/snooze", testToken, map[string]any{"card_key": intKey, "until_job_id": "j-snz"}, 200, nil)

	todayCall(t, s, http.MethodGet, "/v1/today", testToken, nil, 200, &got)
	if len(got.Decisions) != 0 || got.Snoozed != 2 {
		t.Fatalf("after snooze = %+v", got)
	}
	var list struct {
		Snoozed []map[string]any `json:"snoozed"`
	}
	todayCall(t, s, http.MethodGet, "/v1/today/snoozed", testToken, nil, 200, &list)
	if len(list.Snoozed) != 2 {
		t.Fatalf("snoozed = %+v", list)
	}
	var handled struct {
		Handled []map[string]any `json:"handled"`
	}
	todayCall(t, s, http.MethodGet, "/v1/today/handled", testToken, nil, 200, &handled)
	if len(handled.Handled) != 2 || handled.Handled[0]["action_id"] != "snooze" {
		t.Fatalf("handled = %+v", handled)
	}

	// A job credential reads the list but may neither snooze nor put back.
	tok := seedJobToken(t, s, "job-t3", "member", "")
	todayCall(t, s, http.MethodGet, "/v1/today/snoozed", tok, nil, 200, nil)
	todayCall(t, s, http.MethodPost, "/v1/today/snooze", tok, map[string]any{"card_key": workKey, "until_at": now + 60}, 403, nil)
	todayCall(t, s, http.MethodDelete, "/v1/today/snooze/"+url.PathEscape(workKey), tok, nil, 403, nil)

	// Put back — the interaction key holds a '/', escaped in the path.
	todayCall(t, s, http.MethodDelete, "/v1/today/snooze/"+url.PathEscape(intKey), testToken, nil, 200, nil)
	todayCall(t, s, http.MethodDelete, "/v1/today/snooze/"+url.PathEscape(intKey), testToken, nil, 404, nil)
	todayCall(t, s, http.MethodGet, "/v1/today", testToken, nil, 200, &got)
	if len(got.Decisions) != 1 || got.Decisions[0].Key != intKey || got.Snoozed != 1 {
		t.Fatalf("after put back = %+v", got)
	}
}
