package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

// holdSessionServer answers the session probe the env-detected session goes through
// (and nothing else): the session exists in a project and runner that are NOT the
// ones the job below targets — the remote / other-project shape.
func holdSessionServer(t *testing.T, sid string) *client.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions/"+sid {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.SessionDetail{Session: client.AgentSession{
			SessionID: sid, ProjectKey: "session-project", Runner: "session-runner", State: "waiting_reply"}})
	}))
	t.Cleanup(ts.Close)
	return client.New(ts.URL, "tok")
}

// TestHoldJobRunNotifiesTheEnvSessionByDefault (gofer-fxbu): a --hold job submitted
// from inside an agent session notifies that session without --notify, even when the
// job targets another project; --no-notify and a plain (non-hold) job stay silent.
func TestHoldJobRunNotifiesTheEnvSessionByDefault(t *testing.T) {
	isolateAgentSessionEnv(t)
	const sid = "11111111-2222-3333-4444-555555555555"
	t.Setenv("GOFER_SESSION_ID", sid)
	cli := holdSessionServer(t, sid)

	run := func(t *testing.T, args ...string) (bool, string, error) {
		t.Helper()
		jobRunOpts = jobRunFlags{}
		t.Cleanup(func() { jobRunOpts = jobRunFlags{} })
		base := []string{"job", "run", "-p", "job-project", "-a", "exec"}
		_, _, _, _, _, _, _ = parseRun(t, append(append(base, args...), "--", "go", "version"))
		return resolveJobRunNotify(cli)
	}

	t.Run("--hold defaults to the env session", func(t *testing.T) {
		notify, got, err := run(t, "--hold")
		if err != nil || !notify || got != sid {
			t.Fatalf("notify=%v sid=%q err=%v, want the env session %s", notify, got, err, sid)
		}
	})
	t.Run("--no-notify opts out", func(t *testing.T) {
		notify, got, err := run(t, "--hold", "--no-notify")
		if err != nil || notify || got != "" {
			t.Fatalf("notify=%v sid=%q err=%v, want off", notify, got, err)
		}
	})
	t.Run("--no-notify conflicts with --notify", func(t *testing.T) {
		if _, _, err := run(t, "--hold", "--no-notify", "--notify"); err == nil || !strings.Contains(err.Error(), "--no-notify") {
			t.Fatalf("err=%v, want a usage error naming --no-notify", err)
		}
	})
	t.Run("a plain job stays silent", func(t *testing.T) {
		notify, got, err := run(t)
		if err != nil || notify || got != "" {
			t.Fatalf("notify=%v sid=%q err=%v, want off", notify, got, err)
		}
	})
	t.Run("an explicit target wins", func(t *testing.T) {
		notify, got, err := run(t, "--hold", "--notify-session", "other")
		if err != nil || !notify || got != "other" {
			t.Fatalf("notify=%v sid=%q err=%v, want other", notify, got, err)
		}
	})
	t.Run("outside a session --hold submits without a notify target", func(t *testing.T) {
		t.Setenv("GOFER_SESSION_ID", "")
		notify, got, err := run(t, "--hold")
		if err != nil || notify || got != "" {
			t.Fatalf("notify=%v sid=%q err=%v, want off and no error", notify, got, err)
		}
	})
}
