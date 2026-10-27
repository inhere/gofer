package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"
)

// exitCoder is what gcli reads to turn an error into a process exit code
// (goutil's errorx.ErrorCoder: Code() int).
type exitCoder interface{ Code() int }

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	if ec, ok := err.(exitCoder); ok {
		return ec.Code()
	}
	return 1
}

// TestSessionWaitExitCodes pins the contract a script branches on: reached → 0
// (no error), timeout → exit code 2 with the current state in the message, and the
// wait asks the server for exactly the states it was given.
func TestSessionWaitExitCodes(t *testing.T) {
	const sid = "9f2c1e40-1111-2222-3333-444455556666"
	var gotPath, gotQuery string
	reached := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		state := "waiting_reply"
		if !reached {
			state = "idle"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{"session_id": sid, "agent": "claude", "state": state},
			"state":   state,
			"until":   []string{"waiting_reply"},
			"reached": reached,
		})
	}))
	defer srv.Close()
	clientNode(t, srv.URL)

	newWaitCmd := func() *gcli.Command {
		c := bindCmd(findSub(t, NewSessionCmd(), "wait"))
		c.Arg("id").Set(sid)
		return c
	}

	out := captureOutput(t, func() {
		c := newWaitCmd()
		sessionWaitOpts.until, sessionWaitOpts.timeout, sessionWaitOpts.jsonOut = "waiting_reply", "5s", false
		t.Cleanup(func() { sessionWaitOpts.until, sessionWaitOpts.timeout = "idle", "10m" })
		if err := runSessionWait(c, nil); err != nil {
			t.Fatalf("a reached wait must not be an error: %v", err)
		}
	})
	if gotPath != "/v1/sessions/"+sid+"/wait" {
		t.Fatalf("path = %q, want the wait endpoint", gotPath)
	}
	if !strings.Contains(gotQuery, "until=waiting_reply") || !strings.Contains(gotQuery, "timeout=") {
		t.Fatalf("query = %q, want until and timeout on the wire", gotQuery)
	}
	if !strings.Contains(out, "waiting_reply") {
		t.Fatalf("output = %q, want the reached state", out)
	}

	// A timeout exits 2, not 1: it is an outcome a script acts on, not a failure.
	reached = false
	var timeoutErr error
	captureOutput(t, func() {
		c := newWaitCmd()
		sessionWaitOpts.until, sessionWaitOpts.timeout, sessionWaitOpts.jsonOut = "waiting_reply", "1s", false
		timeoutErr = runSessionWait(c, nil)
	})
	if code := exitCodeOf(timeoutErr); code != 2 || sessionWaitTimeoutExit != 2 {
		t.Fatalf("timeout exit code = %d (%v), want 2", code, timeoutErr)
	}
	if timeoutErr == nil || !strings.Contains(timeoutErr.Error(), "idle") {
		t.Fatalf("timeout error = %v, want it to name the state it found", timeoutErr)
	}
}

// TestSessionWaitRejectsABadUntil: a typo fails before any request is sent.
func TestSessionWaitRejectsABadUntil(t *testing.T) {
	c := bindCmd(findSub(t, NewSessionCmd(), "wait"))
	c.Arg("id").Set("9f2c1e40-1111-2222-3333-444455556666")
	sessionWaitOpts.until = "sleeping"
	t.Cleanup(func() { sessionWaitOpts.until = "idle" })
	err := runSessionWait(c, nil)
	if code := exitCodeOf(err); code != configExitErr {
		t.Fatalf("bad --until exit code = %d (%v), want %d", code, err, configExitErr)
	}
}

// TestSessionScreenPrintsTheText: `session screen` prints what the server read, and
// nothing that carries an escape sequence.
func TestSessionScreenPrintsTheText(t *testing.T) {
	const sid = "9f2c1e40-aaaa-bbbb-cccc-444455556666"
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": sid, "state": "waiting_reply", "source": "turns",
			"text": "[OPEN] A or B?\nagent> A or B?\nhuman> (waiting for a reply)",
		})
	}))
	defer srv.Close()
	clientNode(t, srv.URL)

	out := captureOutput(t, func() {
		c := bindCmd(findSub(t, NewSessionCmd(), "screen"))
		c.Arg("id").Set(sid)
		sessionScreenOpts.lines, sessionScreenOpts.jsonOut = 7, false
		t.Cleanup(func() { sessionScreenOpts.lines = 50 })
		if err := runSessionScreen(c, nil); err != nil {
			t.Fatalf("session screen: %v", err)
		}
	})
	if gotPath != "/v1/sessions/"+sid+"/screen" || !strings.Contains(gotQuery, "n=7") {
		t.Fatalf("request = %q?%q, want the screen endpoint with n", gotPath, gotQuery)
	}
	if !strings.Contains(out, "A or B?") || !strings.Contains(out, "source=turns") {
		t.Fatalf("output = %q, want the screen text and its source", out)
	}
}
