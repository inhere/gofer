package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// 收集箱的快速记录与目标推荐（第一期收集箱设计「三、收集箱」）。反向用例与调用断言在
// authz_inhandler_test.go 的 "GET /v1/work-items/{id}/dispatch-targets" 用例里；这里测语义。

func captureReq(t *testing.T, s *Server, token, sid string, body any) *http.Response {
	t.Helper()
	req := jsonRequest(t, http.MethodPost, "/v1/work-items/capture", token, body)
	if sid != "" {
		req.Header.Set(sessionHeader, sid)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func TestWorkCaptureRoute(t *testing.T) {
	s := topicServer(t)

	// A person: captured, human, the given kind and project; no session header = no source.
	resp := captureReq(t, s, "tok-alice", "", map[string]any{"text": "想做个导出", "kind": "idea", "project": "self"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("capture = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	var d work.DetailView
	decode(t, resp, &d)
	if d.Status != jobstore.WorkCaptured || d.StatusSource != jobstore.WorkSourceHuman || d.Kind != "idea" ||
		d.ProjectKey != "self" || d.CaptureSourceSession != "" || d.CaptureSourceJob != "" || d.Title != "想做个导出" {
		t.Fatalf("captured item = %+v", d.WorkItem)
	}

	// The kind defaults to note; the session header is recorded as the source session.
	resp = captureReq(t, s, "tok-alice", "sess-cap-1", map[string]any{"text": "随手记"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("capture = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	decode(t, resp, &d)
	if d.Kind != "note" || d.CaptureSourceSession != "sess-cap-1" {
		t.Fatalf("default kind / source session: %+v", d.WorkItem)
	}

	// Invalid kind and empty text are 400 and write nothing.
	for _, body := range []map[string]any{{"text": "x", "kind": "bogus"}, {"text": "  ", "kind": "idea"}, {"kind": "idea"}} {
		resp = captureReq(t, s, "tok-alice", "", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("capture %v = %d, want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
	items, err := s.jobs.Meta().ListWorkItems(jobstore.WorkListOpts{Statuses: []string{jobstore.WorkCaptured}})
	if err != nil || len(items) != 2 {
		t.Fatalf("captured items = %d (%v), want 2", len(items), err)
	}

	// A job credential may capture, and the item records the job (and the session header).
	jobTok := seedJobToken(t, s, "job-cap", jobstore.JobCredentialMember, "")
	resp = captureReq(t, s, jobTok, "sess-cap-2", map[string]any{"text": "job 发现的问题", "kind": "issue"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job capture = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	decode(t, resp, &d)
	if d.Kind != "issue" || d.CaptureSourceJob != "job-cap" || d.CaptureSourceSession != "sess-cap-2" || d.Status != jobstore.WorkCaptured {
		t.Fatalf("job-captured item = %+v", d.WorkItem)
	}

	// A worker token is refused.
	resp = captureReq(t, s, "tok-worker", "", map[string]any{"text": "x", "kind": "idea"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("worker capture = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func dispatchTargets(t *testing.T, s *Server, token, id string) *http.Response {
	t.Helper()
	req := jsonRequest(t, http.MethodGet, "/v1/work-items/"+id+"/dispatch-targets", token, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func captureItem(t *testing.T, s *Server, token, text, project string) string {
	t.Helper()
	resp := captureReq(t, s, token, "", map[string]any{"text": text, "kind": "idea", "project": project})
	var d work.DetailView
	decode(t, resp, &d)
	return d.ID
}

type targetsOut struct {
	Targets []struct {
		SessionID   string `json:"session_id"`
		TopicSource string `json:"topic_source"`
		Hits        []struct {
			Kind   string `json:"kind"`
			Match  string `json:"match"`
			Source string `json:"source"`
		} `json:"hits"`
	} `json:"targets"`
}

func TestWorkDispatchTargetsTopThreeAndFiltering(t *testing.T) {
	s := topicServer(t)
	seed := func(sid, caller, topic string, ls int64) {
		seedTopicSession(t, s, jobstore.AgentSession{SessionID: sid, CallerID: caller, LastSeenAt: ls})
		if topic != "" {
			src := jobstore.SessionTopicHuman
			if _, _, _, err := s.jobs.Meta().SetSessionTopic(sid, &topic, nil, src); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Five of alice's sessions match the text in decreasing strength; bob has a perfect match.
	seed("s-a1", "alice", "导出 订单 报表", 100)
	seed("s-a2", "alice", "导出 订单", 101)
	seed("s-a3", "alice", "导出", 102)
	seed("s-a4", "alice", "订单", 103)
	seed("s-a5", "alice", "无关主题", 104)
	seed("s-b1", "bob", "导出 订单 报表", 105)

	id := captureItem(t, s, "tok-alice", "做 订单 导出 报表", "")

	resp := dispatchTargets(t, s, "tok-alice", id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("targets = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	var out targetsOut
	decode(t, resp, &out)
	var ids []string
	for _, tg := range out.Targets {
		ids = append(ids, tg.SessionID)
	}
	// Exactly three, strongest first, bob's session (no session.read.others) filtered out
	// BEFORE ranking, and the unrelated one absent.
	if got := strings.Join(ids, ","); got != "s-a1,s-a2,s-a3" && got != "s-a1,s-a2,s-a4" {
		t.Fatalf("targets = %s, want the three best of alice's own sessions", got)
	}
	if len(out.Targets) != 3 || len(out.Targets[0].Hits) == 0 || out.Targets[0].Hits[0].Kind == "" {
		t.Fatalf("targets/hits shape: %+v", out)
	}

	// With the permission bob's session joins the ranking (and ties are broken by recency).
	s.setDBGrants(grantsFor([]string{authz.PermSessionReadOthers}))
	resp = dispatchTargets(t, s, "tok-alice", id)
	decode(t, resp, &out)
	found := false
	for _, tg := range out.Targets {
		found = found || tg.SessionID == "s-b1"
	}
	if !found || len(out.Targets) != 3 {
		t.Fatalf("with session.read.others: %+v", out)
	}
	s.setDBGrants(nil)

	// Unknown item = 404; worker token and job credential are refused.
	resp = dispatchTargets(t, s, "tok-alice", "w-nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown item = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = dispatchTargets(t, s, "tok-worker", id)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("worker = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	jobTok := seedJobToken(t, s, "job-tg", jobstore.JobCredentialMember, "")
	resp = dispatchTargets(t, s, jobTok, id)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("job credential = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}
