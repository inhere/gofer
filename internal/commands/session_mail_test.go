package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

type mailCall struct{ method, uri, body, session string }

// mailServer answers the mailbox routes with canned JSON and records every call.
func mailServer(t *testing.T, calls *[]mailCall) {
	t.Helper()
	letter := func(id int, state string) map[string]any {
		return map[string]any{"id": id, "thread_id": "m1", "from": map[string]any{"kind": "session", "id": "sess-a", "caller": "alice", "display": "会话 sess-a（调用方 alice）"},
			"to_address": "session:sess-b", "to": map[string]any{"kind": "session", "id": "sess-b"}, "body": "line one\nline two", "state": state, "created_at": 1760000000}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*calls = append(*calls, mailCall{r.Method, r.URL.RequestURI(), string(b), r.Header.Get(client.SessionHeader)})
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/mail":
			_ = json.NewEncoder(w).Encode(map[string]any{"mail": []any{letter(7, "pending")}, "count": 1})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(letter(7, "pending"))
		case strings.HasSuffix(r.URL.Path, "/ack"):
			_ = json.NewEncoder(w).Encode(letter(7, "delivered"))
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			_ = json.NewEncoder(w).Encode(letter(7, "cancelled"))
		case strings.Contains(string(b), "topic:") || strings.Contains(string(b), "ambiguous"):
			m := letter(8, "held")
			m["to"] = map[string]any{"kind": ""}
			m["last_error"] = "topic:billing matched 2 sessions"
			_ = json.NewEncoder(w).Encode(map[string]any{"mail": m, "held": true, "capture_item_id": "w-1",
				"candidates": []any{map[string]any{"kind": "session", "id": "sess-x", "display": "会话 sess-x（billing，可能不在线）"}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"mail": letter(9, "pending")})
		}
	}))
	t.Cleanup(srv.Close)
	clientNode(t, srv.URL)
}

func resetMailOpts(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		sessionMailOpts.urgent, sessionMailOpts.bodyFile = false, ""
		sessionMailOpts.in, sessionMailOpts.out = false, false
		sessionMailOpts.thread, sessionMailOpts.state, sessionMailOpts.session, sessionMailOpts.limit = "", "", "", 0
	})
}

func TestSessionMailSendCLI(t *testing.T) {
	var calls []mailCall
	mailServer(t, &calls)
	resetMailOpts(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "cur-session")

	c := bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "send"))
	c.Arg("address").Set("session:sess-b")
	c.Arg("text").Set("hello there")
	sessionMailOpts.urgent = true
	out := captureOutput(t, func() {
		if err := runSessionMailSend(c, nil); err != nil {
			t.Fatal(err)
		}
	})
	if len(calls) != 1 || calls[0].method != "POST" || calls[0].uri != "/v1/mail" || calls[0].session != "cur-session" {
		t.Fatalf("calls = %+v, want POST /v1/mail with the current session header", calls)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(calls[0].body), &body)
	if body["to"] != "session:sess-b" || body["body"] != "hello there" || body["urgent"] != true {
		t.Fatalf("body = %s", calls[0].body)
	}
	if !strings.Contains(out, "sent letter #9") || !strings.Contains(out, "pending") {
		t.Fatalf("out = %q", out)
	}

	// --body-file, and an address that held: the candidates are listed.
	file := filepath.Join(t.TempDir(), "letter.txt")
	if err := os.WriteFile(file, []byte("from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionMailOpts.urgent, sessionMailOpts.bodyFile = false, file
	calls = nil
	c = bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "send"))
	c.Arg("address").Set("topic:billing")
	out = captureOutput(t, func() {
		if err := runSessionMailSend(c, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(calls[0].body, `"body":"from a file"`) {
		t.Fatalf("body = %s", calls[0].body)
	}
	for _, want := range []string{"held", "w-1", "candidate: 会话 sess-x", "session:sess-x"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, missing %q", out, want)
		}
	}

	// An empty letter, and text together with --body-file, never reach the server.
	calls = nil
	c = bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "send"))
	c.Arg("address").Set("caller")
	c.Arg("text").Set("x")
	if err := runSessionMailSend(c, nil); err == nil {
		t.Fatal("text with --body-file must be refused")
	}
	sessionMailOpts.bodyFile = ""
	c = bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "send"))
	c.Arg("address").Set("caller")
	if err := runSessionMailSend(c, nil); err == nil || len(calls) != 0 {
		t.Fatalf("an empty letter: err=%v calls=%v", err, calls)
	}
}

func TestSessionMailSendBroadcastCLI(t *testing.T) {
	var calls []mailCall
	mailServer(t, &calls)
	resetMailOpts(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "cur-session")

	// Repeated --to (and the positional address) go out as an ARRAY: one broadcast.
	c := bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "send"))
	c.Arg("text").Set("v0.183.1 已发布")
	sessionMailOpts.to = []string{"session:sess-a", "each:project:gofer"}
	out := captureOutput(t, func() {
		if err := runSessionMailSend(c, nil); err != nil {
			t.Fatal(err)
		}
	})
	var body map[string]any
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatal(err)
	}
	to, ok := body["to"].([]any)
	if !ok || len(to) != 2 || to[0] != "session:sess-a" || to[1] != "each:project:gofer" {
		t.Fatalf("body = %s, want the two addresses as an array", calls[0].body)
	}
	_ = out
}

func TestFormatMailBroadcastRecipients(t *testing.T) {
	out := formatMail(client.Mail{
		ID: 12, ThreadID: "m12", State: "pending", Body: "群发", CreatedAt: 1760000000,
		BroadcastID: 12,
		Broadcast: &client.MailBroadcast{
			ID: 12, Count: 2, Delivered: 1, Answered: 1,
			Recipients: []client.MailBroadcastRecipient{
				{Kind: "session", ID: "sess-a", MailID: 12, State: "answered"},
				{Address: "each:project:p1", Kind: "session", Skipped: true, Reason: "another caller's session"},
			},
		},
	})
	for _, want := range []string{"broadcast b12: 2 recipients", "delivered 1", "answered 1", "#12 session sess-a (answered)", "skipped: another caller's session"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, missing %q", out, want)
		}
	}
}

func TestSessionMailListReadReplyCancelCLI(t *testing.T) {
	var calls []mailCall
	mailServer(t, &calls)
	resetMailOpts(t)

	lc := bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "list"))
	sessionMailOpts.in, sessionMailOpts.thread, sessionMailOpts.state = true, "m1", "pending,held"
	out := captureOutput(t, func() {
		if err := runSessionMailList(lc, nil); err != nil {
			t.Fatal(err)
		}
	})
	if calls[0].uri != "/v1/mail?box=in&state=pending%2Cheld&thread=m1" {
		t.Fatalf("list uri = %s", calls[0].uri)
	}
	for _, want := range []string{"#7", "pending", "会话 sess-a（调用方 alice）", "line one line two"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list out = %q, missing %q", out, want)
		}
	}
	lc = bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "list"))
	sessionMailOpts.in, sessionMailOpts.out = true, true
	if err := runSessionMailList(lc, nil); err == nil {
		t.Fatal("--in with --out must be refused")
	}

	// read: GET then ack (reading is delivery).
	calls = nil
	c := bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "read"))
	c.Arg("id").Set("#7")
	out = captureOutput(t, func() {
		if err := runSessionMailRead(c, nil); err != nil {
			t.Fatal(err)
		}
	})
	if len(calls) != 2 || calls[0].method != "GET" || calls[0].uri != "/v1/mail/7" || calls[1].method != "POST" || calls[1].uri != "/v1/mail/7/ack" {
		t.Fatalf("read calls = %+v, want GET then ack", calls)
	}
	for _, want := range []string{"letter #7", "[delivered]", "from: 会话 sess-a", "line one\nline two"} {
		if !strings.Contains(out, want) {
			t.Fatalf("read out = %q, missing %q", out, want)
		}
	}

	// reply / cancel.
	calls = nil
	c = bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "reply"))
	c.Arg("id").Set("7")
	c.Arg("text").Set("on it")
	out = captureOutput(t, func() {
		if err := runSessionMailReply(c, nil); err != nil {
			t.Fatal(err)
		}
	})
	if calls[0].uri != "/v1/mail/7/reply" || !strings.Contains(calls[0].body, `"body":"on it"`) || !strings.Contains(out, "replied to #7") {
		t.Fatalf("reply: %+v %q", calls, out)
	}
	calls = nil
	c = bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "cancel"))
	c.Arg("id").Set("7")
	out = captureOutput(t, func() {
		if err := runSessionMailCancel(c, nil); err != nil {
			t.Fatal(err)
		}
	})
	if calls[0].uri != "/v1/mail/7/cancel" || !strings.Contains(out, "cancelled letter #7") {
		t.Fatalf("cancel: %+v %q", calls, out)
	}
	c = bindCmd(findSub(t, findSub(t, NewSessionCmd(), "mail"), "read"))
	c.Arg("id").Set("seven")
	if err := runSessionMailRead(c, nil); err == nil {
		t.Fatal("a non-numeric id must be refused")
	}
}
