package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	}, false)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "NAME") || !strings.Contains(lines[1], "my-tools-dev-22") {
		t.Fatalf("list output:\n%s", out)
	}
	if f := strings.Fields(lines[2]); len(f) < 2 || f[1] != "-" {
		t.Fatalf("unnamed session must show '-': %q", lines[2])
	}
}

func TestSessionTopicCommand(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"error":"patch session failed","detail":"ended"}`))
			return
		}
		_, _ = w.Write([]byte(`{"session_id":"cur-session-1234","topic":"fix login","scope":["web/src"],"topic_source":"self"}`))
	}))
	defer srv.Close()
	clientNode(t, srv.URL)
	for _, k := range []string{"GOFER_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID", "CLAUDE_CODE_SESSION_ID"} {
		t.Setenv(k, "")
	}
	build := func(topic string) *gcli.Command {
		c := bindCmd(findSub(t, NewSessionCmd(), "topic"))
		if topic != "" {
			c.Arg("topic").Set(topic)
		}
		return c
	}
	t.Cleanup(func() { sessionTopicOpts.session, sessionTopicOpts.scope, sessionTopicOpts.clearScope = "", nil, false })

	// No session in the environment: asks for --session, nothing is sent.
	c := build("fix login")
	err := runSessionTopic(c, nil)
	if err == nil || !strings.Contains(err.Error(), "--session") || gotPath != "" {
		t.Fatalf("no env: err=%v path=%q", err, gotPath)
	}

	// Several different candidates: error, nothing sent.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "a")
	t.Setenv("CODEX_THREAD_ID", "b")
	c = build("fix login")
	err = runSessionTopic(c, nil)
	if err == nil || !strings.Contains(err.Error(), "several") || gotPath != "" {
		t.Fatalf("ambiguous: err=%v path=%q", err, gotPath)
	}

	// One candidate: PATCH it, with scopes.
	t.Setenv("CODEX_THREAD_ID", "")
	c = build("fix login")
	sessionTopicOpts.scope = []string{"web/src", "web/test"}
	out := captureOutput(t, func() {
		if err := runSessionTopic(c, nil); err != nil {
			t.Fatal(err)
		}
	})
	if gotMethod != "PATCH" || gotPath != "/v1/sessions/a" || gotBody != `{"topic":"fix login","scope":["web/src","web/test"]}` {
		t.Fatalf("%s %s %s", gotMethod, gotPath, gotBody)
	}
	if !strings.Contains(out, "fix login") {
		t.Fatalf("out=%q", out)
	}

	// --clear-scope alone sends an empty array; with --scope it is refused.
	c = build("")
	sessionTopicOpts.clearScope, sessionTopicOpts.scope = true, nil
	if err := runSessionTopic(c, nil); err != nil || gotBody != `{"scope":[]}` {
		t.Fatalf("clear: %v %s", err, gotBody)
	}
	c = build("x")
	sessionTopicOpts.clearScope, sessionTopicOpts.scope = true, []string{"a"}
	if err := runSessionTopic(c, nil); err == nil {
		t.Fatal("--scope with --clear-scope must be refused")
	}

	// An ended session is a clear 409.
	c = build("fix login")
	sessionTopicOpts.clearScope, sessionTopicOpts.scope = false, nil
	status = http.StatusConflict
	err = runSessionTopic(c, nil)
	if err == nil || !strings.Contains(err.Error(), "has ended") {
		t.Fatalf("ended: %v", err)
	}
}

func TestFormatSessionFind(t *testing.T) {
	list := []client.SessionFound{
		{AgentSession: client.AgentSession{SessionID: "abcdef1234567890", Agent: "claude", State: "running", Topic: "修复登录", TopicSource: "self", Scope: []string{"web/src"}},
			Hits: []client.SessionHit{{Kind: "scope", Match: "web/src"}, {Kind: "topic", Match: "登录"}}},
		{AgentSession: client.AgentSession{SessionID: "11112222", Agent: "codex", State: "offline", Topic: "web · peer", TopicSource: "inferred"},
			MaybeOffline: true, Hits: []client.SessionHit{{Kind: "peer_name", Match: "peer"}}},
	}
	out := formatSessionFind(list)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", out)
	}
	for _, want := range []string{"abcdef12 ", "claude", "running", "修复登录 [self]", "scope=web/src", "hit=scope:web/src;topic:登录"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("line 0 lacks %q: %q", want, lines[0])
		}
	}
	for _, want := range []string{"11112222", "codex", "offline(可能不在线)", "[inferred]", "scope=-", "hit=peer_name:peer"} {
		if !strings.Contains(lines[1], want) {
			t.Fatalf("line 1 lacks %q: %q", want, lines[1])
		}
	}
}

func TestSessionFindCommandTreatsPathLikeArgAsPath(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"sessions":[]}`))
	}))
	defer srv.Close()
	clientNode(t, srv.URL)
	t.Cleanup(func() { sessionFindOpts.path, sessionFindOpts.project, sessionFindOpts.limit = "", "", 0 })
	c := bindCmd(findSub(t, NewSessionCmd(), "find"))
	c.Arg("query").Set("web/src/api")
	out := captureOutput(t, func() {
		if err := runSessionFind(c, nil); err != nil {
			t.Fatal(err)
		}
	})
	if gotQuery != "path=web%2Fsrc%2Fapi" || !strings.Contains(out, "no session matches") {
		t.Fatalf("query=%q out=%q", gotQuery, out)
	}
	c = bindCmd(findSub(t, NewSessionCmd(), "find"))
	if err := runSessionFind(c, nil); err == nil {
		t.Fatal("empty find must be refused")
	}
}

func TestFormatSessionListUniquePrefixAndLong(t *testing.T) {
	list := []client.AgentSession{
		{SessionID: "sess_261020_aaaa1111", Agent: "suga", State: "idle", RelayMode: "auto"},
		{SessionID: "sess_261020_aaaa2222", Agent: "suga", State: "idle", RelayMode: "auto"},
		{SessionID: "cf0bb835-1111-2222-3333-444455556666", Agent: "claude", State: "idle", RelayMode: "auto"},
	}
	out := formatSessionList(list, false)
	for _, want := range []string{"sess_261020_aaaa1 ", "sess_261020_aaaa2 ", "cf0bb835 "} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sess_261020_aaaa1111") || strings.Contains(out, "cf0bb835-") {
		t.Fatalf("prefix mode printed full ids:\n%s", out)
	}
	full := formatSessionList(list, true)
	for _, l := range list {
		if !strings.Contains(full, l.SessionID) {
			t.Fatalf("--long missing %s:\n%s", l.SessionID, full)
		}
	}
}

func TestUniqueSIDPrefixesWholeIDWhenPrefixOfAnother(t *testing.T) {
	got := uniqueSIDPrefixes([]string{"abcdefgh", "abcdefghij", "zzzzzzzzzz"})
	if got[0] != "abcdefgh" || got[1] != "abcdefghi" || got[2] != "zzzzzzzz" {
		t.Fatalf("got %v", got)
	}
}

func TestHideStaleSessions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	day := int64(24 * 3600)
	old, recent := now.Unix()-6*day, now.Unix()-4*day
	list := []client.AgentSession{
		{SessionID: "old-offline", State: "offline", LastSeenAt: old},
		{SessionID: "old-ended", State: "ended", LastSeenAt: old},
		{SessionID: "recent-offline", State: "offline", LastSeenAt: recent},
		{SessionID: "old-waiting", State: "waiting_reply", LastSeenAt: old},
		{SessionID: "old-idle", State: "idle", LastSeenAt: old},
		{SessionID: "never-offline", State: "offline"},
	}
	kept, hidden := hideStaleSessions(list, now)
	var ids []string
	for _, a := range kept {
		ids = append(ids, a.SessionID)
	}
	if hidden != 3 || strings.Join(ids, ",") != "recent-offline,old-waiting,old-idle" {
		t.Fatalf("kept=%v hidden=%d", ids, hidden)
	}
}
