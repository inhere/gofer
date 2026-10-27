package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// waitAnswer is one /wait answer.
type waitAnswer struct {
	Session sessionView `json:"session"`
	State   string      `json:"state"`
	Until   []string    `json:"until"`
	Reached bool        `json:"reached"`
}

func waitSession(t *testing.T, s *Server, token, sid, query string) (int, waitAnswer) {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/sessions/"+sid+"/wait?"+query, token, nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return resp.StatusCode, waitAnswer{}
	}
	var out waitAnswer
	decode(t, resp, &out)
	return resp.StatusCode, out
}

// openSessionTurn sets the relay switch on and opens a turn, which is what makes a
// session waiting_reply (the state another agent waits to answer).
func openSessionTurn(t *testing.T, s *Server, token, sid string) {
	t.Helper()
	for _, step := range []struct {
		path string
		body any
	}{
		{"/v1/sessions/" + sid + "/relay", map[string]any{"mode": "on"}},
		{"/v1/sessions/" + sid + "/turns", map[string]any{"body": "A or B?", "timeout_sec": 120}},
	} {
		resp := do(t, s, http.MethodPost, step.path, token, step.body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s status=%d, want 200", step.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestSessionWaitReturnsAtOnceWhenAlreadyInState: a wait on a state the session is
// already in answers immediately — an agent asking "can I hand it something?" must
// not pay a poll interval for the answer.
func TestSessionWaitReturnsAtOnceWhenAlreadyInState(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	registerIdleSession(t, s, "sid-wait-already")
	openSessionTurn(t, s, testToken, "sid-wait-already")

	start := time.Now()
	status, got := waitSession(t, s, testToken, "sid-wait-already", "until=waiting_reply&timeout=30")
	if status != http.StatusOK {
		t.Fatalf("wait status=%d, want 200", status)
	}
	if !got.Reached || got.State != jobstore.SessionWaitingReply {
		t.Fatalf("wait = reached=%v state=%q, want reached=true waiting_reply", got.Reached, got.State)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("an already-satisfied wait took %s", elapsed)
	}
	// The answer carries the session itself, so the CLI can print the final state.
	if got.Session.SessionID != "sid-wait-already" {
		t.Fatalf("session in the answer = %q", got.Session.SessionID)
	}
}

// TestSessionWaitTimesOutWithoutReaching: the budget runs out with the state still
// not the wanted one. That is reached=false (the CLI turns it into exit code 2), not
// an error, and the answer still says which state it found.
func TestSessionWaitTimesOutWithoutReaching(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	registerIdleSession(t, s, "sid-wait-timeout")

	start := time.Now()
	status, got := waitSession(t, s, testToken, "sid-wait-timeout", "until=ended&timeout=1")
	if status != http.StatusOK {
		t.Fatalf("wait status=%d, want 200", status)
	}
	if got.Reached {
		t.Fatalf("wait reached=true, want a timeout")
	}
	if got.State != jobstore.SessionRunning {
		t.Fatalf("state on a timed-out wait = %q, want running", got.State)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("a timeout=1 wait returned after %s", elapsed)
	}
	// timeout=0 (or none) looks once and answers at once.
	start = time.Now()
	if status, got := waitSession(t, s, testToken, "sid-wait-timeout", "until=ended"); status != http.StatusOK || got.Reached {
		t.Fatalf("no-timeout wait = status %d reached %v", status, got.Reached)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("a timeout=0 wait took %s", elapsed)
	}
}

// TestSessionWaitReturnsWhenTheStateChanges: the wait really blocks until the
// session moves — opening a turn (which the relay makes waiting_reply) releases it.
func TestSessionWaitReturnsWhenTheStateChanges(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	registerIdleSession(t, s, "sid-wait-change")

	type answer struct {
		reached bool
		state   string
	}
	done := make(chan answer, 1)
	go func() {
		_, got := waitSession(t, s, testToken, "sid-wait-change", "until=waiting_reply&timeout=30")
		done <- answer{got.Reached, got.State}
	}()

	// The change lands after the long poll is in flight (the poll re-reads every
	// 250ms, so the order does not matter for correctness — only for what it proves).
	wait.For(t, 5*time.Second, "the session detail is readable", func() (bool, any) {
		resp := do(t, s, http.MethodGet, "/v1/sessions/sid-wait-change", testToken, nil)
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK, resp.StatusCode
	})
	time.Sleep(300 * time.Millisecond)
	openSessionTurn(t, s, testToken, "sid-wait-change")

	select {
	case got := <-done:
		if !got.reached || got.state != jobstore.SessionWaitingReply {
			t.Fatalf("wait = reached=%v state=%q, want reached=true waiting_reply", got.reached, got.state)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the wait did not return after the state changed")
	}
}

// TestSessionWaitRejectsAnUnknownUntil: a typo is a 400 naming the accepted set, not
// a wait that silently never fires.
func TestSessionWaitRejectsAnUnknownUntil(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	registerIdleSession(t, s, "sid-wait-bad")

	resp := do(t, s, http.MethodGet, "/v1/sessions/sid-wait-bad/wait?until=sleeping", testToken, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown until = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-wait-bad/wait", testToken, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing until = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	// An unknown session is a 404, not a wait that sits on nothing.
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-nope/wait?until=idle&timeout=5", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session wait = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestSessionScreenServesRelayTurns: a terminal (relay) session's screen is its
// recent turns — the same rows `session show` prints — oldest first.
func TestSessionScreenServesRelayTurns(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	registerIdleSession(t, s, "sid-screen-turns")
	openSessionTurn(t, s, testToken, "sid-screen-turns")

	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-screen-turns/say", testToken, map[string]string{"answer": "the second one"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("say status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-screen-turns/screen?n=50", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("screen status=%d, want 200", resp.StatusCode)
	}
	var got struct {
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
		Text      string `json:"text"`
	}
	decode(t, resp, &got)
	if got.SessionID != "sid-screen-turns" || got.Source != "turns" {
		t.Fatalf("screen = id %q source %q, want sid-screen-turns / turns", got.SessionID, got.Source)
	}
	if !strings.Contains(got.Text, "A or B?") || !strings.Contains(got.Text, "the second one") {
		t.Fatalf("screen text = %q, want the turn's question and answer", got.Text)
	}

	// n bounds the answer by lines.
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-screen-turns/screen?n=1", testToken, nil)
	decode(t, resp, &got)
	if lines := strings.Count(got.Text, "\n") + 1; lines > 1 {
		t.Fatalf("n=1 returned %d lines: %q", lines, got.Text)
	}
}

// TestSessionScreenFallsBackToTheLastMessage: a session with no turns yet still has
// something to show, and it must come out with no ANSI in it.
func TestSessionScreenFallsBackToTheLastMessage(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	registerIdleSession(t, s, "sid-screen-msg")
	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-screen-msg/heartbeat", testToken, map[string]any{
		"event": "Progress", "last_message": "\x1b[32mbuild\x1b[0m finished", "idle_sec": 0,
	})
	resp.Body.Close()

	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-screen-msg/screen", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("screen status=%d, want 200", resp.StatusCode)
	}
	var got struct {
		Source string `json:"source"`
		Text   string `json:"text"`
	}
	decode(t, resp, &got)
	if got.Source != "message" {
		t.Fatalf("source = %q, want message (no turns yet)", got.Source)
	}
	if strings.ContainsRune(got.Text, 0x1b) || !strings.Contains(got.Text, "build") {
		t.Fatalf("screen text = %q, want the message with no ESC byte", got.Text)
	}
}

// TestSessionScreenServesTheTakeoverPtyTranscript: a session carried by an
// interactive pty job (path B's takeover) is read the way `job logs` reads it — the
// pty.txt tail — and the text comes back with no escape sequence.
func TestSessionScreenServesTheTakeoverPtyTranscript(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	registerIdleSession(t, s, "sid-screen-pty")

	now := time.Now().Unix()
	resultDir := filepath.Join(t.TempDir(), "job-pty-screen")
	if err := os.MkdirAll(resultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// pty.txt is written de-ANSI'd, but a stray escape (a pasted line) must not reach
	// the caller either.
	transcript := "\x1b[1mtui line one\x1b[0m\ntui line two\n"
	if err := os.WriteFile(filepath.Join(resultDir, store.PtyTranscriptFile), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: "job-pty-screen", ProjectKey: "self", Agent: "claude", Runner: "local",
		Interactive: true, Status: "running", Cwd: ".", ResultDir: resultDir,
		RequestJSON: `{"project_key":"self","agent":"claude","runner":"local","interactive":true}`,
		StartedAt:   now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	if _, err := s.jobs.Meta().SetSessionHandedOff("sid-screen-pty", "job-pty-screen"); err != nil {
		t.Fatalf("set handed off: %v", err)
	}

	resp := do(t, s, http.MethodGet, "/v1/sessions/sid-screen-pty/screen?n=10", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("screen status=%d, want 200", resp.StatusCode)
	}
	var got struct {
		Source string `json:"source"`
		JobID  string `json:"job_id"`
		Text   string `json:"text"`
	}
	decode(t, resp, &got)
	if got.Source != "pty" || got.JobID != "job-pty-screen" {
		t.Fatalf("screen = source %q job %q, want pty / job-pty-screen", got.Source, got.JobID)
	}
	if strings.ContainsRune(got.Text, 0x1b) {
		t.Fatalf("screen text still holds an ESC byte: %q", got.Text)
	}
	if !strings.Contains(got.Text, "tui line one") || !strings.Contains(got.Text, "tui line two") {
		t.Fatalf("screen text = %q, want the pty tail", got.Text)
	}
}
