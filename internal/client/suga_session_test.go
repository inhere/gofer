package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentSessionEnvKeysOrder(t *testing.T) {
	want := []string{"SUGA_SESSION_ID", "GOFER_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID"}
	if strings.Join(AgentSessionEnvKeys, ",") != strings.Join(want, ",") {
		t.Fatalf("AgentSessionEnvKeys = %v, want %v", AgentSessionEnvKeys, want)
	}
}

// A suga session's own id beats an inherited / explicit GOFER_SESSION_ID (a gofer job running
// suga carries both), and beats disagreeing Claude / Codex ids; SUGA_SESSION_NAME is no address.
func TestSugaSessionIDWinsOverGoferSessionID(t *testing.T) {
	env := map[string]string{
		"SUGA_SESSION_ID": "sess_261020_abc", "GOFER_SESSION_ID": "job-session",
		"CLAUDE_CODE_SESSION_ID": "cc", "CODEX_THREAD_ID": "cx", "SUGA_SESSION_NAME": "my-name",
	}
	getenv := func(k string) string { return env[k] }
	if id, src := SessionIDFromEnv(getenv); id != "sess_261020_abc" || src != "SUGA_SESSION_ID" {
		t.Fatalf("SessionIDFromEnv = (%q, %q)", id, src)
	}
	if id, err := ResolveCurrentSessionID(getenv); err != nil || id != "sess_261020_abc" {
		t.Fatalf("ResolveCurrentSessionID = (%q, %v)", id, err)
	}
	if c := CurrentAgentSessionCandidates(getenv); len(c) == 0 || c[0].EnvKey != "SUGA_SESSION_ID" {
		t.Fatalf("candidates = %+v, want suga first", c)
	}
	// Without suga the GOFER_SESSION_ID rule is unchanged.
	delete(env, "SUGA_SESSION_ID")
	if id, src := SessionIDFromEnv(getenv); id != "job-session" || src != "GOFER_SESSION_ID" {
		t.Fatalf("no suga: (%q, %q)", id, src)
	}
}

func TestPlanAutoBindDoesNotFallBackFromSuga(t *testing.T) {
	var probes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes = append(probes, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/gofer-sess") {
			_, _ = w.Write([]byte(`{"session":{"session_id":"gofer-sess","state":"idle"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"get session failed","detail":"session not found"}`))
	}))
	defer srv.Close()
	env := map[string]string{"SUGA_SESSION_ID": "sess_missing", "GOFER_SESSION_ID": "gofer-sess"}
	c := New(srv.URL, "")
	res := c.LookupCurrentAgentSession(func(k string) string { return env[k] }, "")
	if res.Bound() || res.EnvKey != "SUGA_SESSION_ID" || !strings.Contains(res.Reason, "$SUGA_SESSION_ID") {
		t.Fatalf("result = %+v, want unbound with the suga variable named", res)
	}
	if len(probes) != 1 {
		t.Fatalf("probed %v, want only the suga id", probes)
	}
	// Without suga, the old fallthrough across candidates is untouched.
	env = map[string]string{"CLAUDE_CODE_SESSION_ID": "gone", "CODEX_THREAD_ID": "gofer-sess"}
	if res = c.LookupCurrentAgentSession(func(k string) string { return env[k] }, ""); !res.Bound() {
		t.Fatalf("claude->codex fallthrough broke: %+v", res)
	}
}

func TestUnknownSugaSessionErrorNamesTheVariable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"unknown session","detail":"the session named by X-Gofer-Session-Id is not registered"}`))
	}))
	defer srv.Close()
	for k, v := range map[string]string{"SUGA_SESSION_ID": "sess_x", "GOFER_SESSION_ID": "", "CLAUDE_CODE_SESSION_ID": "", "CODEX_THREAD_ID": "", "CODEX_SESSION_ID": ""} {
		t.Setenv(k, v)
	}
	c := New(srv.URL, "")
	err := c.doJSON(http.MethodPost, "/v1/mail", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "$SUGA_SESSION_ID=sess_x") || StatusOf(err) != http.StatusNotFound {
		t.Fatalf("err = %v (status %d)", err, StatusOf(err))
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatal("the StatusError must stay reachable")
	}
	// Claude ids keep the plain server error.
	t.Setenv("SUGA_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "cc")
	err = New(srv.URL, "").doJSON(http.MethodPost, "/v1/mail", nil, nil)
	if err == nil || strings.Contains(err.Error(), "SUGA_SESSION_ID") {
		t.Fatalf("claude err = %v", err)
	}
}
