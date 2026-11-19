package hookrelay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/seat"
)

// seatSessionView seeds the answer the stub server gives for sid: the session view carrying a
// seat (nil = the session sits in none). The language is pinned to English so the injected
// block is exactly the text the assertions below spell out.
func seatSessionView(f *fakeAPI, sid string, s *client.SessionSeat) {
	f.sessions[sid] = client.AgentSession{SessionID: sid, Agent: "claude", Language: string(i18n.EN), Seat: s}
}

// seatStartPayload is a SessionStart of the given source (startup / resume / compact).
func seatStartPayload(t *testing.T, agent, sid, source string) Payload {
	t.Helper()
	m := map[string]any{"session_id": sid, "hook_event_name": "SessionStart", "cwd": "/w/repo"}
	if source != "" {
		m["source"] = source
	}
	return payload(t, agent, m)
}

// TestSeatBlockInjectedOnEverySessionStart: a session sitting in a seat is told the seat's name,
// purpose and charter on startup, resume and compact alike (every SessionStart, not just the
// first one).
func TestSeatBlockInjectedOnEverySessionStart(t *testing.T) {
	const charter = "always run the package tests before reporting"
	for _, source := range []string{"startup", "resume", "compact"} {
		t.Run(source, func(t *testing.T) {
			f := newFake()
			sid := "sid-seat-" + source
			seatSessionView(f, sid, &client.SessionSeat{ID: "seat-1", Name: "dev", Purpose: "pr review", Charter: charter})
			res, err := Run(f, seatStartPayload(t, "claude", sid, source), fastOpts(nil))
			if err != nil {
				t.Fatal(err)
			}
			want := i18n.T(i18n.EN, "seat.hook.heading_purpose", "name", "dev", "purpose", "pr review") +
				"\n" + i18n.T(i18n.EN, "seat.continuation.charter") + "\n" + charter
			if !strings.Contains(res.Context, want) {
				t.Fatalf("context=%q, want the seat block %q", res.Context, want)
			}
		})
	}
}

// TestSeatContinuationRidesTheFirstStart: the one-time continuation the server hands out on the
// registration answer is injected with the seat block; a later answer (the server then leaves it
// empty) carries the seat block alone.
func TestSeatContinuationRidesTheFirstStart(t *testing.T) {
	const continuation = "earlier: the parser fix is half done"
	f := newFake()
	sid := "sid-cont"
	seatSessionView(f, sid, &client.SessionSeat{ID: "seat-1", Name: "dev", Continuation: continuation})
	res, err := Run(f, seatStartPayload(t, "claude", sid, "startup"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Context, continuation) {
		t.Fatalf("context=%q, want the continuation text", res.Context)
	}
	// The server hands the text out once: the next registration answer carries the seat, not it.
	f.mu.Lock()
	a := f.sessions[sid]
	a.Seat.Continuation = ""
	f.sessions[sid] = a
	f.mu.Unlock()
	res, err = Run(f, seatStartPayload(t, "claude", sid, "resume"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Context, continuation) {
		t.Fatalf("context=%q, the continuation must not repeat", res.Context)
	}
	if !strings.Contains(res.Context, i18n.T(i18n.EN, "seat.hook.heading", "name", "dev")) {
		t.Fatalf("context=%q, the seat block must stay", res.Context)
	}
}

// TestSeatBlockAbsentWhenSessionSitsInNoSeat: a session in no seat gets no seat content at all —
// the SessionStart context is exactly the two reminders every session gets.
func TestSeatBlockAbsentWhenSessionSitsInNoSeat(t *testing.T) {
	f := newFake()
	sid := "sid-vacant"
	seatSessionView(f, sid, nil)
	res, err := Run(f, seatStartPayload(t, "claude", sid, "startup"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := TopicReminder + "\n\n" + NeedsReplyReminder; res.Context != want {
		t.Fatalf("context=%q, want %q", res.Context, want)
	}
}

// TestSeatCharterPathIsReadFromTheServerRoot: a charter written as "@<path>" is read from the
// project root the view named, and a file that is not there costs only the charter — the heading
// of the seat block stays.
func TestSeatCharterPathIsReadFromTheServerRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "charter.md"), []byte("file charter text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	sid := "sid-charter-path"
	seatSessionView(f, sid, &client.SessionSeat{ID: "seat-1", Name: "dev", CharterPath: "docs/charter.md", Root: root})
	res, err := Run(f, seatStartPayload(t, "claude", sid, "startup"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := i18n.T(i18n.EN, "seat.continuation.charter") + "\n" + "file charter text"; !strings.Contains(res.Context, want) {
		t.Fatalf("context=%q, want the charter file's text", res.Context)
	}

	// The file is gone: the charter is dropped, everything else is injected as before.
	seatSessionView(f, sid, &client.SessionSeat{ID: "seat-1", Name: "dev", CharterPath: "docs/gone.md", Root: root})
	res, err = Run(f, seatStartPayload(t, "claude", sid, "startup"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Context, i18n.T(i18n.EN, "seat.hook.heading", "name", "dev")) {
		t.Fatalf("context=%q, want the seat heading without the charter", res.Context)
	}
	if strings.Contains(res.Context, i18n.T(i18n.EN, "seat.continuation.charter")) {
		t.Fatalf("context=%q, a missing charter file must leave no charter section", res.Context)
	}
}

// TestSeatCharterOverBudgetIsCut: a charter longer than the budget reaches the session cut, on a
// rune boundary, instead of pushing the rest of the context out.
func TestSeatCharterOverBudgetIsCut(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "charter.md"), []byte(strings.Repeat("字", seat.CharterBudget)), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	sid := "sid-charter-big"
	seatSessionView(f, sid, &client.SessionSeat{ID: "seat-1", Name: "dev", CharterPath: "charter.md", Root: root})
	res, err := Run(f, seatStartPayload(t, "claude", sid, "startup"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	label := i18n.T(i18n.EN, "seat.continuation.charter") + "\n"
	i := strings.Index(res.Context, label)
	if i < 0 {
		t.Fatalf("context=%q, want the charter section", res.Context)
	}
	body := res.Context[i+len(label):]
	if j := strings.Index(body, "\n\n"+NeedsReplyReminder); j >= 0 {
		body = body[:j]
	}
	if len(body) > seat.CharterBudget+4 {
		t.Fatalf("the charter reached the session with %d bytes, budget %d", len(body), seat.CharterBudget)
	}
	if strings.Contains(body, "…") != (len(body) >= seat.CharterBudget) {
		t.Fatalf("a cut charter must be marked: %d bytes, %q", len(body), body)
	}
	if !utf8.ValidString(body) {
		t.Fatalf("the cut split a rune: %q", body)
	}
}
