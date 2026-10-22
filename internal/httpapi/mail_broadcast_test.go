package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// POST /v1/mail with an ARRAY of addresses is a broadcast (设计 2026-10-22「D5」): the answer
// carries the group id and the addressee table, GET /v1/mail/{id} carries the group summary, and
// the refusals are the design's (urgent, a job credential, no recipient, one bad recipient).
func TestMailBroadcastHTTP(t *testing.T) {
	e := newMailEnv(t)
	e.grant()

	// Alice to two of her own sessions: one letter each, one thread, one group.
	status, body := e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{
		"to": []string{"session:" + e.sA, "session:" + e.sSup}, "body": "v0.183.1 已发布",
	})
	if status != http.StatusOK {
		t.Fatalf("broadcast = %d (%s)", status, body)
	}
	sent := decodeSent(t, body)
	if sent.BroadcastID <= 0 || sent.Delivered != 2 || len(sent.Recipients) != 2 {
		t.Fatalf("broadcast answer = %+v, want a group of 2", sent)
	}
	if sent.ThreadID == "" || sent.ThreadID != sent.Mail.ThreadID {
		t.Fatalf("thread = %q / %q, want the broadcast's own thread", sent.ThreadID, sent.Mail.ThreadID)
	}
	for _, rc := range sent.Recipients {
		if rc.MailID <= 0 || rc.Skipped {
			t.Fatalf("recipient %+v, want a stored copy", rc)
		}
	}
	// The letter of the answer is one copy, and the copies are all in the group.
	status, body = e.req(t, mailTokAlice, "", "GET", fmt.Sprintf("/v1/mail/%d", sent.Mail.ID), nil)
	if status != http.StatusOK {
		t.Fatalf("get = %d (%s)", status, body)
	}
	var view mailView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if view.Broadcast == nil || view.Broadcast.ID != sent.BroadcastID || view.Broadcast.Count != 2 {
		t.Fatalf("broadcast section = %+v, want group %d with 2 copies", view.Broadcast, sent.BroadcastID)
	}
	if view.BroadcastID != sent.BroadcastID {
		t.Fatalf("letter broadcast_id = %d, want %d", view.BroadcastID, sent.BroadcastID)
	}

	// A single address keeps the single-letter answer (no broadcast section).
	if status, body = e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{"to": "session:" + e.sA, "body": "单发"}); status != http.StatusOK {
		t.Fatalf("single = %d (%s)", status, body)
	}
	if single := decodeSent(t, body); single.BroadcastID != 0 || len(single.Recipients) != 0 {
		t.Fatalf("a single letter answered as a broadcast: %+v", single)
	}

	// An each: selector fans out and reports what it skipped.
	if status, body = e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{"to": []string{"each:project:p1"}, "body": "群发"}); status != http.StatusOK {
		t.Fatalf("each:project: = %d (%s)", status, body)
	}
	if sel := decodeSent(t, body); sel.Delivered < 2 {
		t.Fatalf("each:project:p1 delivered %d, want at least the two of alice's live sessions", sel.Delivered)
	}

	// 400s of the design.
	for _, tc := range []struct {
		name string
		to   []string
		want int
	}{
		{"urgent broadcast", []string{"session:" + e.sA, "session:" + e.sSup}, http.StatusBadRequest},
		{"no recipient", []string{"each:agent:nobody-runs-this"}, http.StatusBadRequest},
		{"unknown session", []string{"session:" + e.sA, "session:no-such-session"}, http.StatusNotFound},
	} {
		body := map[string]any{"to": tc.to, "body": "x"}
		if tc.name == "urgent broadcast" {
			body["urgent"] = true
		}
		if status, got := e.req(t, mailTokAlice, "", "POST", "/v1/mail", body); status != tc.want {
			t.Errorf("%s = %d (%s), want %d", tc.name, status, got, tc.want)
		}
	}

	// A job credential may not broadcast (403), and its single-address ceiling still holds.
	if status, got := e.req(t, e.tokSrc, "", "POST", "/v1/mail", map[string]any{
		"to": []string{"session:" + e.sA, "session:" + e.sSup}, "body": "x",
	}); status != http.StatusForbidden {
		t.Fatalf("job credential broadcast = %d (%s), want 403", status, got)
	}
}

// TestMailBroadcastExplicitRefusalIsWhole: an explicitly named recipient alice may not mail
// refuses the whole broadcast (no copy stored for the one she may reach).
func TestMailBroadcastExplicitRefusalIsWhole(t *testing.T) {
	e := newMailEnv(t)
	e.grant() // no session.reply.others: bob's session is not reachable
	status, body := e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{
		"to": []string{"session:" + e.sA, "session:" + e.sB}, "body": "x",
	})
	wantPermission(t, "explicit broadcast to another caller", status, body, "session.reply.others")

	rows, err := e.s.jobs.Meta().ListMail(jobstore.ListMailOpts{FromCaller: "alice", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("%d letters stored by a refused broadcast", len(rows))
	}
}
