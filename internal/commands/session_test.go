package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"
)

// TestSessionSayTakeoverFlag pins the CLI switch for path B (§9.1 B): `say
// --deliver --takeover` asks the server to START A NEW PROCESS that continues the
// session, so the flag has to reach the wire explicitly (a plain --deliver must
// never take a session over), and the receipt names the takeover job to attach to.
// --takeover without --deliver is refused rather than ignored: answering a waiting
// turn and taking a session over are different acts.
func TestSessionSayTakeoverFlag(t *testing.T) {
	const sid = "9f2c1e40-1111-2222-3333-444455556666"

	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{"path": "takeover", "job_id": "job-9"})
	}))
	defer srv.Close()
	clientNode(t, srv.URL)

	newSayCmd := func() *gcli.Command {
		c := bindCmd(findSub(t, NewSessionCmd(), "say"))
		c.Arg("id").Set(sid)
		c.Arg("text").Set("carry on")
		return c
	}

	out := captureOutput(t, func() {
		// The flags are bound by Config (which resets them to their defaults), so set
		// them after building the command — exactly how the parser fills them.
		c := newSayCmd()
		sessionSayOpts.deliver, sessionSayOpts.takeover = true, true
		t.Cleanup(func() { sessionSayOpts.deliver, sessionSayOpts.takeover = false, false })
		if err := runSessionSay(c, nil); err != nil {
			t.Fatalf("session say --deliver --takeover: %v", err)
		}
	})
	if gotPath != "/v1/sessions/"+sid+"/deliver" {
		t.Fatalf("path=%q, want the deliver endpoint", gotPath)
	}
	if gotBody != `{"allow_takeover":true,"text":"carry on"}` {
		t.Fatalf("body=%q, want the takeover opt-in on the wire", gotBody)
	}
	if !strings.Contains(out, "job-9") {
		t.Fatalf("output=%q, want the takeover job the caller attaches to", out)
	}

	// A plain --deliver stays byte-identical to the pre-B request (no opt-in).
	c := newSayCmd()
	sessionSayOpts.deliver, sessionSayOpts.takeover = true, false
	if err := runSessionSay(c, nil); err != nil {
		t.Fatalf("session say --deliver: %v", err)
	}
	if gotBody != `{"text":"carry on"}` {
		t.Fatalf("body=%q, want no takeover field", gotBody)
	}

	// --takeover alone: refused before any request is made.
	sessionSayOpts.deliver, sessionSayOpts.takeover = false, true
	t.Cleanup(func() { sessionSayOpts.deliver, sessionSayOpts.takeover = false, false })
	gotPath = ""
	err := runSessionSay(newSayCmd(), nil)
	if err == nil || !strings.Contains(err.Error(), "--deliver") {
		t.Fatalf("err=%v, want a refusal naming --deliver", err)
	}
	if gotPath != "" {
		t.Fatalf("a refused invocation must not call the hub (path=%q)", gotPath)
	}
}

// TestSessionSayDeliverFlag pins `session say`'s two routings: by default it is
// the old "answer the newest OPEN turn" call (scripts keep working), and with
// --deliver it goes to the routed endpoint that can also reach an IDLE session
// (§9.1 A).
func TestSessionSayDeliverFlag(t *testing.T) {
	const sid = "9f2c1e40-1111-2222-3333-444455556666"

	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if strings.HasSuffix(r.URL.Path, "/deliver") {
			_ = json.NewEncoder(w).Encode(map[string]any{"path": "tmux", "job_id": "job-9"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "dec-9", "title": "t", "question": "q", "state": "ANSWERED",
		})
	}))
	defer srv.Close()
	clientNode(t, srv.URL)

	newSayCmd := func() *gcli.Command {
		c := bindCmd(findSub(t, NewSessionCmd(), "say"))
		c.Arg("id").Set(sid)
		c.Arg("text").Set("carry on")
		return c
	}

	// --deliver: the routed endpoint, which reaches an idle tmux session.
	out := captureOutput(t, func() {
		// The flag is bound by Config (which resets it to its default), so set it
		// after building the command — exactly how the parser fills it at runtime.
		c := newSayCmd()
		sessionSayOpts.deliver = true
		t.Cleanup(func() { sessionSayOpts.deliver = false })
		if err := runSessionSay(c, nil); err != nil {
			t.Fatalf("session say --deliver: %v", err)
		}
	})
	if gotPath != "/v1/sessions/"+sid+"/deliver" {
		t.Fatalf("path=%q, want the deliver endpoint", gotPath)
	}
	if gotBody != `{"text":"carry on"}` {
		t.Fatalf("body=%q, want the text field", gotBody)
	}
	if !strings.Contains(out, "typed into the terminal") {
		t.Fatalf("output=%q, want the tmux confirmation", out)
	}

	// Default: unchanged turn-only semantics.
	sessionSayOpts.deliver = false
	out = captureOutput(t, func() {
		if err := runSessionSay(newSayCmd(), nil); err != nil {
			t.Fatalf("session say: %v", err)
		}
	})
	if gotPath != "/v1/sessions/"+sid+"/say" {
		t.Fatalf("path=%q, want the say endpoint", gotPath)
	}
	if gotBody != `{"answer":"carry on"}` {
		t.Fatalf("body=%q, want the answer field", gotBody)
	}
	if !strings.Contains(out, "dec-9") {
		t.Fatalf("output=%q, want the answered turn id", out)
	}
}
