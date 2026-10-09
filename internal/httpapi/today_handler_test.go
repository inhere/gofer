package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

type todayResp struct {
	Digest struct {
		SinceLast struct {
			Since    int64 `json:"since"`
			JobsDone int   `json:"jobs_done"`
		} `json:"since_last"`
		Title string `json:"title"`
	} `json:"digest"`
	Decisions []struct {
		Key     string `json:"key"`
		Kind    string `json:"kind"`
		Urgency string `json:"urgency"`
		Blocks  struct {
			Score int `json:"score"`
		} `json:"blocks"`
		Actions []struct {
			ID string `json:"id"`
		} `json:"actions"`
		Advice *json.RawMessage `json:"advice"`
	} `json:"decisions"`
	Snoozed int `json:"snoozed"`
	Status  struct {
		Runners struct {
			Online int `json:"online"`
			Total  int `json:"total"`
		} `json:"runners"`
		Alerts []string `json:"alerts"`
	} `json:"status"`
	GeneratedAt int64 `json:"generated_at"`
}

func todayCall(t *testing.T, s *Server, method, path, token string, body any, want int, out any) {
	t.Helper()
	resp := do(t, s, method, path, token, body)
	defer resp.Body.Close()
	if resp.StatusCode != want {
		var raw map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		t.Fatalf("%s %s = %d, want %d: %v", method, path, resp.StatusCode, want, raw)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
}

func TestTodayEndpoint(t *testing.T) {
	s := newTestServer(t, testToken, false)
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "等我拍板"}, 200)
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, testToken, map[string]any{"status": "needs_me", "rev": w.Rev}, 200)

	var got todayResp
	todayCall(t, s, http.MethodGet, "/v1/today?since=1", testToken, nil, 200, &got)
	if got.Digest.SinceLast.Since != 1 || got.Digest.Title == "" || got.GeneratedAt == 0 || got.Snoozed != 0 {
		t.Fatalf("today = %+v", got)
	}
	if len(got.Decisions) != 1 || got.Decisions[0].Key != "work:"+w.ID || got.Decisions[0].Kind != "work" ||
		got.Decisions[0].Urgency != "normal" || got.Decisions[0].Advice != nil || len(got.Decisions[0].Actions) != 3 {
		t.Fatalf("decisions = %+v", got.Decisions)
	}
	if got.Status.Runners.Total < 1 || got.Status.Runners.Online < 1 || got.Status.Alerts == nil {
		t.Fatalf("status = %+v", got.Status)
	}
	todayCall(t, s, http.MethodGet, "/v1/today?include_exec=1", testToken, nil, 200, nil)
	todayCall(t, s, http.MethodGet, "/v1/today?since=x", testToken, nil, 400, nil)
}

func TestTodayActionsAndHandled(t *testing.T) {
	s := newTestServer(t, testToken, false)
	todayCall(t, s, http.MethodPost, "/v1/today/actions", testToken, map[string]any{"card_key": "work:w1"}, 400, nil)
	var h map[string]any
	todayCall(t, s, http.MethodPost, "/v1/today/actions", testToken, map[string]any{
		"card_key": "review:j1", "action_id": "accept", "title": "导出", "label": "通过",
	}, 200, &h)
	if h["kind"] != "review" || h["action_id"] != "accept" {
		t.Fatalf("recorded = %v", h)
	}
	var list struct {
		Handled []map[string]any `json:"handled"`
		Days    int              `json:"days"`
	}
	todayCall(t, s, http.MethodGet, "/v1/today/handled", testToken, nil, 200, &list)
	if list.Days != 7 || len(list.Handled) != 1 || list.Handled[0]["title"] != "导出" || list.Handled[0]["card_key"] != "review:j1" {
		t.Fatalf("handled = %+v", list)
	}
	todayCall(t, s, http.MethodGet, "/v1/today/handled?days=0", testToken, nil, 400, nil)
	todayCall(t, s, http.MethodGet, "/v1/today/handled?days=31", testToken, nil, 400, nil)

	// A job credential reads but may not record a person's action.
	tok := seedJobToken(t, s, "job-t1", "member", "")
	todayCall(t, s, http.MethodGet, "/v1/today", tok, nil, 200, nil)
	todayCall(t, s, http.MethodPost, "/v1/today/actions", tok, map[string]any{"card_key": "review:j1", "action_id": "accept"}, 403, nil)
}

func TestTodayWorkerTokenRefused(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken, Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "w-token"}}})
	todayCall(t, s, http.MethodGet, "/v1/today", "w-token", nil, 403, nil)
	// The registered-but-disconnected worker is an offline runner alert.
	var got todayResp
	todayCall(t, s, http.MethodGet, "/v1/today", testToken, nil, 200, &got)
	if got.Status.Runners.Total != 2 || got.Status.Runners.Online != 1 || len(got.Status.Alerts) == 0 || got.Status.Alerts[0] != "runner w1 离线" {
		t.Fatalf("status = %+v", got.Status)
	}
}
