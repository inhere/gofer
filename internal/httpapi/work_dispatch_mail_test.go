package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// 收集箱「交给会话」改为发信（第二期 M6，设计 2026-10-16 §3「分派动作」）：分派 = 经
// internal/mail 发一封信，条目关联信件 id 并从收集箱移出；权限与限额与 POST /v1/mail 一致，
// 失败时条目保持 captured；旧的第一期「经 web 发消息」路径已删除。

// mailOfItem returns the letter a dispatch recorded the item on (id 0 when there is none).
func mailOfItem(t *testing.T, s *Server, itemID string) jobstore.Mail {
	t.Helper()
	rows, err := s.jobs.Meta().ListMail(jobstore.ListMailOpts{})
	if err != nil {
		t.Fatalf("list mail: %v", err)
	}
	for _, m := range rows {
		if m.CaptureItemID == itemID {
			return m
		}
	}
	return jobstore.Mail{}
}

// startSession registers a session through the hook route (the fixture's usual way).
func startSession(t *testing.T, s *Server, token, sid string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions", token, map[string]any{
		"session_id": sid, "agent": "claude", "runner": "server", "project_key": "self",
		"event": "SessionStart",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start session %s = %d", sid, resp.StatusCode)
	}
}

func TestWorkDispatchToSessionSendsMail(t *testing.T) {
	s := newTestServer(t, testToken, false)
	startSession(t, s, testToken, "disp-ok")
	id := newCaptured(t, s, "把导出做成异步")

	resp := dispatch(t, s, testToken, id, map[string]any{"to": "session", "session_id": "disp-ok"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dispatch = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	// The item left the capture box and its journal names the letter.
	d := itemDetail(t, s, id)
	if d.Status != jobstore.WorkActive {
		t.Fatalf("item status after dispatch = %q, want active", d.Status)
	}
	m := mailOfItem(t, s, id)
	if m.ID == 0 {
		t.Fatal("no letter carries the dispatched item")
	}
	if m.State != jobstore.MailPending || m.ToKind != jobstore.MailKindSession || m.ToID != "disp-ok" {
		t.Fatalf("letter = %+v, want a pending letter to session disp-ok", m)
	}
	// No session header: the dispatcher is the calling user (the same origin POST /v1/mail uses).
	if m.FromKind != jobstore.MailKindCaller || m.FromID != "" {
		t.Fatalf("letter origin = %s/%s, want a caller origin", m.FromKind, m.FromID)
	}
	if !strings.Contains(m.Body, "把导出做成异步") {
		t.Fatalf("letter body = %q, want the item's text", m.Body)
	}
	if txt := journalText(t, s, id); !strings.Contains(txt, fmt.Sprintf("信件 #%d", m.ID)) {
		t.Fatalf("journal lacks the letter id: %q", txt)
	}

	// The first phase's web-message path is gone: the outbox stays empty.
	out, err := s.jobs.Meta().ReadSessionOutbox("disp-ok")
	if err != nil || len(out) != 0 {
		t.Fatalf("outbox = %+v (%v), want nothing (dispatch no longer sends a web message)", out, err)
	}

	// Dispatching again is refused: the item left the capture box.
	resp = dispatch(t, s, testToken, id, map[string]any{"to": "session", "session_id": "disp-ok"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second dispatch = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestWorkDispatchToSessionRefusalsKeepCaptured(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := newCaptured(t, s, "送不出去的想法")

	// Missing session id = 400, unknown session = 404: no letter, the item stays captured.
	for body, want := range map[string]int{"": http.StatusBadRequest, "nope": http.StatusNotFound} {
		resp := dispatch(t, s, testToken, id, map[string]any{"to": "session", "session_id": body})
		if resp.StatusCode != want {
			t.Fatalf("session %q = %d, want %d", body, resp.StatusCode, want)
		}
		resp.Body.Close()
	}
	// A session that can no longer receive mail (ended) = 409, still no dispatch.
	startSession(t, s, testToken, "disp-ended")
	resp := do(t, s, http.MethodPost, "/v1/sessions/disp-ended/heartbeat", testToken,
		map[string]any{"event": "SessionEnd"})
	resp.Body.Close()
	resp = dispatch(t, s, testToken, id, map[string]any{"to": "session", "session_id": "disp-ended"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ended session = %d, want 409: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	if d := itemDetail(t, s, id); d.Status != jobstore.WorkCaptured {
		t.Fatalf("item status after the refusals = %q, want captured", d.Status)
	}
	if m := mailOfItem(t, s, id); m.ID != 0 {
		t.Fatalf("a refused dispatch stored letter %+v", m)
	}
	if txt := journalText(t, s, id); strings.Contains(txt, "已送出") {
		t.Fatalf("journal claims a hand-over that failed: %q", txt)
	}
}

// TestWorkDispatchToSessionPermissionKeepsCaptured: dispatching to ANOTHER caller's session needs
// session.reply.others, exactly like POST /v1/mail; without it the item stays in the box.
func TestWorkDispatchToSessionPermissionKeepsCaptured(t *testing.T) {
	s := topicServer(t)
	seedTopicSession(t, s, jobstore.AgentSession{SessionID: "sess-alice-1", CallerID: "alice"})
	id := captureItem(t, s, "tok-alice", "别人的会话", "")

	resp := dispatch(t, s, "tok-bob", id, map[string]any{"to": "session", "session_id": "sess-alice-1"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob -> alice's session = %d, want 403: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
	if w, _, _ := s.jobs.Meta().GetWorkItem(id); w.Status != jobstore.WorkCaptured {
		t.Fatalf("item status = %q, want captured", w.Status)
	}
	rows, err := s.jobs.Meta().ListMail(jobstore.ListMailOpts{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("letters after a refused dispatch = %+v (%v), want none", rows, err)
	}
}

// TestWorkDispatchToSessionRateLimitKeepsCaptured: the send limits are the mailbox's own — the
// 11th dispatch of the same caller in a minute is a 429 and that item stays captured.
func TestWorkDispatchToSessionRateLimitKeepsCaptured(t *testing.T) {
	s := newTestServer(t, testToken, false)
	startSession(t, s, testToken, "disp-rate")
	ids := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		id := newCaptured(t, s, fmt.Sprintf("第 %d 条", i))
		resp := dispatch(t, s, testToken, id, map[string]any{"to": "session", "session_id": "disp-rate"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("dispatch %d = %d: %s", i, resp.StatusCode, readBody(t, resp))
		}
		resp.Body.Close()
		ids = append(ids, id)
	}
	last := newCaptured(t, s, "超出限速的一条")
	resp := dispatch(t, s, testToken, last, map[string]any{"to": "session", "session_id": "disp-rate"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("dispatch past the rate limit = %d, want 429: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
	if w, _, _ := s.jobs.Meta().GetWorkItem(last); w.Status != jobstore.WorkCaptured {
		t.Fatalf("item status = %q, want captured", w.Status)
	}
	if m := mailOfItem(t, s, last); m.ID != 0 {
		t.Fatalf("a rate limited dispatch stored letter %+v", m)
	}
	_ = ids
}

// TestWorkDispatchMailWriteback: the whole second-phase chain — dispatch, delivery, reply — lands
// in the item's journal through the store's letter transitions.
func TestWorkDispatchMailWriteback(t *testing.T) {
	s := newTestServer(t, testToken, false)
	startSession(t, s, testToken, "disp-fb")
	id := newCaptured(t, s, "回写看看")
	resp := dispatch(t, s, testToken, id, map[string]any{"to": "session", "session_id": "disp-fb"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dispatch = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
	m := mailOfItem(t, s, id)
	if m.ID == 0 {
		t.Fatal("no letter for the dispatched item")
	}

	// The recipient reads it: reading IS delivery, and the item's log says so.
	resp = do(t, s, http.MethodPost, fmt.Sprintf("/v1/mail/%d/ack", m.ID), testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
	txt := journalText(t, s, id)
	if !strings.Contains(txt, fmt.Sprintf("信件 #%d", m.ID)) || !strings.Contains(txt, "已送达") {
		t.Fatalf("journal after delivery = %q", txt)
	}

	// The recipient answers: the item keeps the reply's head, not its body.
	resp = do(t, s, http.MethodPost, fmt.Sprintf("/v1/mail/%d/reply", m.ID), testToken,
		map[string]any{"body": "已记 issue，排在 W4"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reply = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
	txt = journalText(t, s, id)
	if !strings.Contains(txt, fmt.Sprintf("信件 #%d 收到回复：已记 issue，排在 W4", m.ID)) {
		t.Fatalf("journal after the reply = %q", txt)
	}
	// The dispatch line is still there and the item is still the one the letter came from.
	if !strings.Contains(txt, "已送出到会话 disp-fb") {
		t.Fatalf("journal lost the dispatch line: %q", txt)
	}
	if after, _, _ := s.jobs.Meta().GetMail(m.ID); after.State != jobstore.MailAnswered {
		t.Fatalf("letter state = %q, want answered", after.State)
	}
}
