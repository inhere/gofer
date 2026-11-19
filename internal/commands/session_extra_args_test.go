package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sessionArgStub answers the routes a relay / inject-mode flip needs and records every request, so a
// test can prove that a refused command reached no server at all.
type sessionArgStub struct {
	paths []string
}

func (s *sessionArgStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/v1/sessions" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{
				map[string]any{"session_id": "sess-aaaa1111", "agent": "claude", "state": "idle"},
			}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "sess-aaaa1111", "relay_mode": "on", "inject_mode": "auto"})
		}
	})
}

// sessionArgStubStart starts the stub and points the CLI's connection flags at it.
func sessionArgStubStart(t *testing.T, s *sessionArgStub) {
	t.Helper()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	jobConnOpts.server, jobConnOpts.token = srv.URL, "tok"
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
}

// TestSessionCommandsRejectAStrayPositionalID: `gofer session relay on <sid>` used to drop the id and
// flip the mode of the session of the current directory. The id belongs to --session, and a stray
// positional argument is refused before anything is sent.
func TestSessionCommandsRejectAStrayPositionalID(t *testing.T) {
	for _, argv := range [][]string{
		{"session", "relay", "on", "sess-aaaa1111"},
		{"session", "inject-mode", "force", "sess-aaaa1111"},
		{"session", "watch", "job-1", "sess-aaaa1111"},
		{"session", "topic", "fix login", "sess-aaaa1111"},
	} {
		t.Run(strings.Join(argv[1:3], " "), func(t *testing.T) {
			stub := &sessionArgStub{}
			sessionArgStubStart(t, stub)
			prevRelay, prevInject, prevWatch, prevTopic := sessionRelayOpts.session, sessionInjectModeOpts.session, sessionWatchOpts.session, sessionTopicOpts.session
			t.Cleanup(func() {
				sessionRelayOpts.session, sessionInjectModeOpts.session = prevRelay, prevInject
				sessionWatchOpts.session, sessionTopicOpts.session = prevWatch, prevTopic
			})
			var code int
			out := captureOutput(t, func() {
				code = NewApp("test").Run(argv)
			})
			if code == 0 {
				t.Fatalf("%v must be refused, out=%s", argv, out)
			}
			if !strings.Contains(out, "unexpected argument") || !strings.Contains(out, "--session") {
				t.Fatalf("%v must name the extra argument and point at --session: %s", argv, out)
			}
			if len(stub.paths) != 0 {
				t.Fatalf("%v reached the server anyway: %v", argv, stub.paths)
			}
		})
	}
}

// TestSessionRelayTakesTheSessionFlag: the documented spelling still works.
func TestSessionRelayTakesTheSessionFlag(t *testing.T) {
	stub := &sessionArgStub{}
	sessionArgStubStart(t, stub)
	prev := sessionRelayOpts.session
	t.Cleanup(func() { sessionRelayOpts.session = prev })
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"session", "relay", "on", "--session", "sess-aaaa1111"})
	})
	if code != 0 {
		t.Fatalf("relay on --session exit=%d out=%s", code, out)
	}
	want := "POST /v1/sessions/sess-aaaa1111/relay"
	for _, p := range stub.paths {
		if p == want {
			return
		}
	}
	t.Fatalf("requests = %v, want %s", stub.paths, want)
}
