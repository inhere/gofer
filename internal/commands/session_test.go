package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
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

	// --takeover alone: refused before any request is made. (The command is built
	// FIRST: Config resets the option globals to their defaults.)
	onlyTakeover := newSayCmd()
	sessionSayOpts.deliver, sessionSayOpts.takeover = false, true
	t.Cleanup(func() { sessionSayOpts.deliver, sessionSayOpts.takeover = false, false })
	gotPath = ""
	err := runSessionSay(onlyTakeover, nil)
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

// TestSessionReleaseTakeoverCommand pins the CLI way back from path B (§9.1 B,
// SUP-02 R1): `session release-takeover <sid>` calls the existing endpoint and
// prints the session's NEW state, so the person sitting at the original terminal
// can take their session back without opening the web — and a full id needs no
// listing round trip first.
func TestSessionReleaseTakeoverCommand(t *testing.T) {
	const sid = "9f2c1e40-1111-2222-3333-444455556666"

	calls := 0
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": sid, "state": "idle", "relay_mode": "auto",
		})
	}))
	defer srv.Close()
	clientNode(t, srv.URL)

	out := captureOutput(t, func() {
		c := bindCmd(findSub(t, NewSessionCmd(), "release-takeover"))
		c.Arg("id").Set(sid)
		if err := runSessionReleaseTakeover(c, nil); err != nil {
			t.Fatalf("session release-takeover: %v", err)
		}
	})
	if gotMethod != http.MethodPost || gotPath != "/v1/sessions/"+sid+"/release-takeover" {
		t.Fatalf("request = %s %s, want POST the release endpoint", gotMethod, gotPath)
	}
	if gotBody != "" {
		t.Fatalf("body=%q, want an empty body (the session id is the whole request)", gotBody)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want exactly one (a full id needs no lookup)", calls)
	}
	if !strings.Contains(out, "state=idle") {
		t.Fatalf("output=%q, want the session's new state", out)
	}
}

// `session resume` posts to the resume endpoint (first text only when given) and
// names the job to attach to; --plan only reads the takeover-plan and starts nothing.
func TestSessionResumeCommand(t *testing.T) {
	const sid = "9f2c1e40-1111-2222-3333-444455556666"
	var calls []string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if strings.HasSuffix(r.URL.Path, "/takeover-plan") {
			_ = json.NewEncoder(w).Encode(map[string]any{"can": false, "reason": "interactive_not_allowed", "message": "项目没有开启交互终端", "state": "ended"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"path": "takeover", "job_id": "job-77"})
	}))
	defer srv.Close()
	clientNode(t, srv.URL)

	run := func(input string, plan bool) string {
		return captureOutput(t, func() {
			c := bindCmd(findSub(t, NewSessionCmd(), "resume"))
			c.Arg("id").Set(sid)
			sessionResumeOpts.input, sessionResumeOpts.plan = input, plan
			t.Cleanup(func() { sessionResumeOpts.input, sessionResumeOpts.plan = "", false })
			if err := runSessionResume(c, nil); err != nil {
				t.Fatalf("session resume: %v", err)
			}
		})
	}
	out := run("", false)
	if calls[len(calls)-1] != "POST /v1/sessions/"+sid+"/resume" || gotBody != `{}` || !strings.Contains(out, "job-77") {
		t.Fatalf("resume call=%v body=%q out=%q", calls, gotBody, out)
	}
	run("接着干", false)
	if gotBody != `{"initial_input":"接着干"}` {
		t.Fatalf("body = %q, want the first input on the wire", gotBody)
	}
	n := len(calls)
	out = run("", true)
	if calls[n] != "GET /v1/sessions/"+sid+"/takeover-plan" || len(calls) != n+1 {
		t.Fatalf("--plan must only read the plan, calls=%v", calls[n:])
	}
	if !strings.Contains(out, "can: false") || !strings.Contains(out, "interactive_not_allowed") || !strings.Contains(out, "交互终端") {
		t.Fatalf("plan output = %q", out)
	}
}

func TestFormatSessionListShowsPeerName(t *testing.T) {
	out := formatSessionList([]client.AgentSession{
		{SessionID: "abcdef1234567890", Agent: "claude", State: "idle", RelayMode: "auto", PeerName: "my-tools-dev-22", ProjectKey: "p"},
		{SessionID: "0123456789abcdef", Agent: "codex", State: "running", RelayMode: "off"},
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "NAME") || !strings.Contains(lines[1], "my-tools-dev-22") {
		t.Fatalf("list output:\n%s", out)
	}
	if f := strings.Fields(lines[2]); len(f) < 2 || f[1] != "-" {
		t.Fatalf("unnamed session must show '-': %q", lines[2])
	}
}
