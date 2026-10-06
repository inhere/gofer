package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/pushhub"
	"github.com/inhere/gofer/internal/work"
)

type wItem struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Goal         string   `json:"goal"`
	Status       string   `json:"status"`
	StatusSource string   `json:"status_source"`
	Unsorted     bool     `json:"unsorted"`
	Rev          int64    `json:"rev"`
	SessionIDs   []string `json:"session_ids"`
	MergedInto   string   `json:"merged_into"`
	Due          bool     `json:"due"`
	NextStep     string   `json:"next_step"`
	Sessions     []struct {
		SessionID string `json:"session_id"`
		Role      string `json:"role"`
		Agent     string `json:"agent"`
		Offline   bool   `json:"offline"`
	} `json:"sessions"`
	Links   []struct{ Kind, Ref string } `json:"links"`
	Journal []struct {
		Kind       string `json:"kind"`
		Text       string `json:"text"`
		By         string `json:"by"`
		OriginItem string `json:"origin_item"`
	} `json:"journal"`
}

func workGet(t *testing.T, s *Server, path string) wItem {
	t.Helper()
	resp := do(t, s, http.MethodGet, path, testToken, nil)
	var out wItem
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %d", path, resp.StatusCode)
	}
	decode(t, resp, &out)
	return out
}

func workList(t *testing.T, s *Server, q string) (items []wItem, summary map[string]int) {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/work-items"+q, testToken, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list %s = %d", q, resp.StatusCode)
	}
	var out struct {
		Items   []wItem        `json:"items"`
		Summary map[string]int `json:"summary"`
	}
	decode(t, resp, &out)
	return out.Items, out.Summary
}

func workCall(t *testing.T, s *Server, method, path, token string, body any, want int) wItem {
	t.Helper()
	resp := do(t, s, method, path, token, body)
	if resp.StatusCode != want {
		var raw map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		t.Fatalf("%s %s = %d, want %d: %v", method, path, resp.StatusCode, want, raw)
	}
	var out wItem
	if want < 300 {
		decode(t, resp, &out)
	} else {
		resp.Body.Close()
	}
	return out
}

func registerSession(t *testing.T, s *Server, sid, cwd string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": sid, "agent": "claude", "cwd": cwd, "runner": "local", "event": "SessionStart",
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("register %s = %d", sid, resp.StatusCode)
	}
}

func humanPrompt(t *testing.T, s *Server, sid, title string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions/"+sid+"/heartbeat", testToken, map[string]any{
		"event": "UserPromptSubmit", "title": title,
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("heartbeat %s = %d", sid, resp.StatusCode)
	}
}

func TestWorkItemsFirstHumanPromptDraftsOnceAndShowsUnsorted(t *testing.T) {
	s := newTestServer(t, testToken, false)
	registerSession(t, s, "sess-w1-aaaa", "/ws/a")
	// Register alone (SessionStart) makes nothing; the first human prompt makes one.
	if items, _ := workList(t, s, ""); len(items) != 0 {
		t.Fatalf("items before prompt = %d", len(items))
	}
	humanPrompt(t, s, "sess-w1-aaaa", "ws: 修登录页")
	humanPrompt(t, s, "sess-w1-aaaa", "ws: 另一个问题")
	items, sum := workList(t, s, "")
	if len(items) != 1 || items[0].Title != "ws: 修登录页" || !items[0].Unsorted || items[0].Status != "active" {
		t.Fatalf("items = %+v", items)
	}
	if sum["open"] != 1 || sum["needs_me"] != 0 {
		t.Fatalf("summary = %v", sum)
	}
	if got, _ := workList(t, s, "?unsorted=1"); len(got) != 1 {
		t.Fatalf("unsorted filter = %d", len(got))
	}
	if got, _ := workList(t, s, "?unsorted=0"); len(got) != 0 {
		t.Fatalf("sorted filter = %d", len(got))
	}
	if got, _ := workList(t, s, "?session=sess-w1-aaaa"); len(got) != 1 {
		t.Fatalf("session filter = %d", len(got))
	}
	d := workGet(t, s, "/v1/work-items/"+items[0].ID)
	if len(d.Sessions) != 1 || d.Sessions[0].Agent != "claude" || len(d.Journal) == 0 {
		t.Fatalf("detail = %+v", d)
	}
}

func TestWorkItemsStatusFollowsSessionAndHumanWins(t *testing.T) {
	s := newTestServer(t, testToken, false)
	registerSession(t, s, "sess-w2", "/ws/a")
	humanPrompt(t, s, "sess-w2", "ws: t")
	id := func() string { items, _ := workList(t, s, ""); return items[0].ID }()

	resp := do(t, s, http.MethodPost, "/v1/sessions/sess-w2/heartbeat", testToken, map[string]any{"event": "Stop", "state": "waiting_reply"})
	resp.Body.Close()
	s.work.SyncAll()
	if got := workGet(t, s, "/v1/work-items/"+id); got.Status != "needs_me" {
		t.Fatalf("status = %s, want needs_me", got.Status)
	}
	// Human picks "needs_onsite"; the running session no longer overrides it.
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+id, testToken, map[string]any{"status": "needs_onsite"}, 200)
	resp = do(t, s, http.MethodPost, "/v1/sessions/sess-w2/heartbeat", testToken, map[string]any{"event": "UserPromptSubmit", "title": "x"})
	resp.Body.Close()
	s.work.SyncAll()
	got := workGet(t, s, "/v1/work-items/"+id)
	if got.Status != "needs_onsite" || got.StatusSource != "human" {
		t.Fatalf("after human = %+v", got)
	}
	// Handing it back to auto (explicitly) lets the session map again.
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+id, testToken, map[string]any{"status_source": "auto"}, 200)
	if got := workGet(t, s, "/v1/work-items/"+id); got.Status != "active" || got.StatusSource != "auto" {
		t.Fatalf("after auto = %+v", got)
	}
	// A human cannot spoof other sources.
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+id, testToken, map[string]any{"status_source": "report"}, 400)
}

func TestWorkItemsPatchRevConflictAndNotFound(t *testing.T) {
	s := newTestServer(t, testToken, false)
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "买设备", "project_key": "self"}, 200)
	if w.ID == "" || w.Rev != 1 {
		t.Fatalf("created = %+v", w)
	}
	workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": " "}, 400)
	p := workCall(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, testToken, map[string]any{"rev": 1, "goal": "两台"}, 200)
	if p.Rev != 2 || p.Goal != "两台" {
		t.Fatalf("patched = %+v", p)
	}
	resp := do(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, testToken, map[string]any{"rev": 1, "goal": "三台"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale rev = %d, want 409", resp.StatusCode)
	}
	var conflict struct {
		Current wItem `json:"current"`
	}
	decode(t, resp, &conflict)
	if conflict.Current.Goal != "两台" || conflict.Current.Rev != 2 {
		t.Fatalf("conflict current = %+v", conflict.Current)
	}
	workCall(t, s, http.MethodPatch, "/v1/work-items/w-nope", testToken, map[string]any{"goal": "x"}, 404)
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, testToken, map[string]any{"status": "bogus"}, 400)
	workCall(t, s, http.MethodGet, "/v1/work-items/w-nope", testToken, nil, 404)
	// No auth, no data.
	workCall(t, s, http.MethodGet, "/v1/work-items", "", nil, 401)
}

func TestWorkItemsJournalLinksSessionsMergeSplit(t *testing.T) {
	s := newTestServer(t, testToken, false)
	registerSession(t, s, "sess-m1", "/ws/a")
	registerSession(t, s, "sess-m2", "/ws/a")
	humanPrompt(t, s, "sess-m1", "ws: 一")
	humanPrompt(t, s, "sess-m2", "ws: 二")
	items, _ := workList(t, s, "")
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	a, b := items[0], items[1]
	_ = a

	// Notes append to the journal; kind is always note for a person.
	n := do(t, s, http.MethodPost, "/v1/work-items/"+a.ID+"/journal", testToken, map[string]any{"text": "先查日志"})
	if n.StatusCode != 200 {
		t.Fatalf("note = %d", n.StatusCode)
	}
	n.Body.Close()
	resp := do(t, s, http.MethodPost, "/v1/work-items/"+a.ID+"/journal", testToken, map[string]any{"text": " "})
	if resp.StatusCode != 400 {
		t.Fatalf("empty note = %d", resp.StatusCode)
	}
	resp.Body.Close()
	d := workCall(t, s, http.MethodPost, "/v1/work-items/"+a.ID+"/links", testToken, map[string]any{"kind": "issue", "ref": "ISS-9"}, 200)
	if len(d.Links) != 1 || d.Links[0].Ref != "ISS-9" {
		t.Fatalf("links = %+v", d.Links)
	}
	workCall(t, s, http.MethodPost, "/v1/work-items/"+a.ID+"/links", testToken, map[string]any{"kind": "x", "ref": "y"}, 400)

	// Merge b into a: one item, two current sessions, b's journal moved with its origin.
	m := workCall(t, s, http.MethodPost, "/v1/work-items/"+a.ID+"/merge", testToken, map[string]any{"sources": []string{b.ID}}, 200)
	if len(m.SessionIDs) != 2 {
		t.Fatalf("merged sessions = %+v", m.SessionIDs)
	}
	sawOrigin := false
	for _, e := range m.Journal {
		if e.OriginItem == b.ID {
			sawOrigin = true
		}
	}
	if !sawOrigin {
		t.Fatalf("merged journal lost origin: %+v", m.Journal)
	}
	if got, _ := workList(t, s, ""); len(got) != 1 {
		t.Fatalf("after merge list = %d", len(got))
	}
	if got := workGet(t, s, "/v1/work-items/"+b.ID); got.MergedInto != a.ID {
		t.Fatalf("source = %+v", got)
	}
	workCall(t, s, http.MethodPost, "/v1/work-items/"+a.ID+"/merge", testToken, map[string]any{"sources": []string{a.ID}}, 400)

	// Split sess-m2 back out.
	resp = do(t, s, http.MethodPost, "/v1/work-items/"+a.ID+"/split", testToken, map[string]any{"title": "拆出来的", "session_ids": []string{"sess-m2"}})
	var sp struct {
		Source wItem `json:"source"`
		Item   wItem `json:"item"`
	}
	decode(t, resp, &sp)
	if sp.Item.Title != "拆出来的" || len(sp.Item.SessionIDs) != 1 || sp.Item.SessionIDs[0] != "sess-m2" || len(sp.Source.SessionIDs) != 1 {
		t.Fatalf("split = %+v", sp)
	}

	// Detach / attach round trip.
	workCall(t, s, http.MethodDelete, "/v1/work-items/"+sp.Item.ID+"/sessions/sess-m2", testToken, nil, 200)
	got := workCall(t, s, http.MethodPost, "/v1/work-items/"+sp.Item.ID+"/sessions", testToken, map[string]any{"session_id": "sess-m2"}, 200)
	if len(got.SessionIDs) != 1 {
		t.Fatalf("reattached = %+v", got)
	}
	workCall(t, s, http.MethodPost, "/v1/work-items/"+sp.Item.ID+"/sessions", testToken, map[string]any{"session_id": "sess-none"}, 404)
}

func TestWorkItemsReportAndJobCredential(t *testing.T) {
	s := newTestServer(t, testToken, false)
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "t"}, 200)
	r := workCall(t, s, http.MethodPost, "/v1/work-items/"+w.ID+"/report", testToken, map[string]any{
		"goal": "g", "status": "waiting_resource", "blocker": "缺账号", "next": "拿到账号后跑回归", "summary": "做到一半", "session_id": "sess-xyz",
	}, 200)
	if r.Goal != "g" || r.Status != "waiting_resource" || r.StatusSource != "report" || r.NextStep != "拿到账号后跑回归" {
		t.Fatalf("report = %+v", r)
	}
	last := r.Journal[len(r.Journal)-1]
	if last.Kind != "report" || last.By != "session:sess-xyz" {
		t.Fatalf("journal tail = %+v", last)
	}
	workCall(t, s, http.MethodPost, "/v1/work-items/"+w.ID+"/report", testToken, map[string]any{}, 400)

	// A job credential may report, nothing else (default deny).
	tok := seedJobToken(t, s, "job-w1", "member", "")
	rj := workCall(t, s, http.MethodPost, "/v1/work-items/"+w.ID+"/report", tok, map[string]any{"summary": "来自 job"}, 200)
	if tail := rj.Journal[len(rj.Journal)-1]; tail.By != "job:job-w1" {
		t.Fatalf("job report by = %q", tail.By)
	}
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, tok, map[string]any{"goal": "hijack"}, 403)
	workCall(t, s, http.MethodPost, "/v1/work-items", tok, map[string]any{"title": "x"}, 403)
	workCall(t, s, http.MethodPost, "/v1/work-items/"+w.ID+"/merge", tok, map[string]any{"sources": []string{"x"}}, 403)
	workCall(t, s, http.MethodPost, "/v1/work-items/"+w.ID+"/report-request", tok, map[string]any{}, 403)
	// Reading is open to a job (like every GET).
	workCall(t, s, http.MethodGet, "/v1/work-items/"+w.ID, tok, nil, 200)
}

func TestWorkItemsWorkerTokenRefused(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken, Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "w-token"}}})
	workCall(t, s, http.MethodGet, "/v1/work-items", "w-token", nil, 403)
	workCall(t, s, http.MethodPost, "/v1/work-items", "w-token", map[string]any{"title": "x"}, 403)
}

func TestWorkItemsReportRequestOnlyReachesRunningSessions(t *testing.T) {
	s := newTestServer(t, testToken, false)
	registerSession(t, s, "sess-rr1", "/ws/a")
	humanPrompt(t, s, "sess-rr1", "ws: t")
	items, _ := workList(t, s, "")
	id := items[0].ID

	// Running session without a project: the message channel reports why it cannot deliver,
	// and the response says so instead of pretending.
	resp := do(t, s, http.MethodPost, "/v1/work-items/"+id+"/report-request", testToken, map[string]any{})
	var out struct {
		Sent    bool `json:"sent"`
		Results []struct {
			SessionID string `json:"session_id"`
			Sent      bool   `json:"sent"`
			Reason    string `json:"reason"`
		} `json:"results"`
	}
	decode(t, resp, &out)
	if out.Sent || len(out.Results) != 1 || out.Results[0].Reason == "" {
		t.Fatalf("running-undeliverable = %+v", out)
	}

	// An ended session is skipped with the phase-2 hint.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sess-rr1/heartbeat", testToken, map[string]any{"event": "SessionEnd"})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/work-items/"+id+"/report-request", testToken, map[string]any{})
	decode(t, resp, &out)
	if out.Sent || len(out.Results) != 1 || !strings.Contains(out.Results[0].Reason, "二期") {
		t.Fatalf("ended = %+v", out)
	}

	// No session at all: 409.
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "no session"}, 200)
	workCall(t, s, http.MethodPost, "/v1/work-items/"+w.ID+"/report-request", testToken, map[string]any{}, 409)
}

func TestWorkItemsDueAndDigestEndpoints(t *testing.T) {
	s := newTestServer(t, testToken, false)
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "回访"}, 200)
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, testToken, map[string]any{"remind_at": 1}, 200)
	items, sum := workList(t, s, "?due=1")
	if len(items) != 1 || !items[0].Due || sum["due"] != 1 {
		t.Fatalf("due = %+v / %v", items, sum)
	}
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, testToken, map[string]any{"status": "needs_me"}, 200)

	resp := do(t, s, http.MethodGet, "/v1/work-items/digest", testToken, nil)
	var d work.Digest
	decode(t, resp, &d)
	if d.NeedsMe != 1 || !strings.HasPrefix(d.Text, "等我 1 · ") {
		t.Fatalf("digest = %+v", d)
	}
	resp = do(t, s, http.MethodPost, "/v1/work-items/digest", testToken, nil)
	var sent struct {
		Digest work.Digest `json:"digest"`
		Queued int         `json:"queued"`
	}
	decode(t, resp, &sent)
	if sent.Digest.Title == "" || sent.Queued != 0 { // no webhook configured in the test server
		t.Fatalf("sent = %+v", sent)
	}
}

func TestWorkPushTopicIsValid(t *testing.T) {
	if !pushhub.ValidTopic(pushhub.TopicWork) || pushhub.TopicWork != "work" {
		t.Fatal("work topic must be a valid global topic")
	}
}

// A real hook heartbeat (not announced by the store) moves the work item through the
// relay's work hook, without an explicit sync and without waiting for the sweep.
func TestWorkItemsFollowHookHeartbeatsLive(t *testing.T) {
	s := newTestServer(t, testToken, false)
	stop := make(chan struct{})
	defer close(stop)
	go s.work.Run(stop)
	registerSession(t, s, "sess-live-0001", "/ws/a")
	humanPrompt(t, s, "sess-live-0001", "ws: live")
	items, _ := workList(t, s, "")
	id := items[0].ID

	resp := do(t, s, http.MethodPost, "/v1/sessions/sess-live-0001/heartbeat", testToken, map[string]any{"event": "Stop", "state": "waiting_reply"})
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := workGet(t, s, "/v1/work-items/"+id); got.Status == "needs_me" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("work item never moved to needs_me after the Stop heartbeat")
		}
		time.Sleep(30 * time.Millisecond)
	}
	humanPrompt(t, s, "sess-live-0001", "ws: live again")
	deadline = time.Now().Add(3 * time.Second)
	for {
		if got := workGet(t, s, "/v1/work-items/"+id); got.Status == "active" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("work item never moved back to active after the next prompt")
		}
		time.Sleep(30 * time.Millisecond)
	}
}
