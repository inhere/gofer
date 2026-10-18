package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestAuthWhoamiReportsCallerCeiling: whoami tells the caller what its own token is limited
// to (the effective values of `server.callers[].ceiling`), omits the key when there is no
// ceiling, and follows a live configuration swap — the ceiling is read from the same snapshot
// the judgment reads, so a reload applies to the next request.
func TestAuthWhoamiReportsCallerCeiling(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "root", Token: "tok-root", CanAdmin: true},
		{ID: "ci", Token: "tok-ci", Ceiling: &config.CallerCeiling{
			Projects: []string{"app-*", "docs"},
			Runners:  []string{"w-linux-*"},
			Agents:   []string{"codex"},
			ReadOnly: boolptr(true),
		}},
		{ID: "alice", Token: "tok-alice"},
	}})

	var who authWhoamiView
	decode(t, do(t, s, http.MethodGet, "/v1/auth/whoami", "tok-ci", nil), &who)
	if who.Ceiling == nil {
		t.Fatalf("whoami for a restricted caller carries no ceiling: %+v", who)
	}
	if strings.Join(who.Ceiling.Projects, ",") != "app-*,docs" ||
		strings.Join(who.Ceiling.Runners, ",") != "w-linux-*" ||
		strings.Join(who.Ceiling.Agents, ",") != "codex" {
		t.Errorf("ceiling lists = %+v, want the configured globs", who.Ceiling)
	}
	// The switches are the EFFECTIVE values: read_only was set, exec / global memory were not
	// (a present ceiling defaults both to off).
	if !who.Ceiling.ReadOnly || who.Ceiling.AllowExec || who.Ceiling.GlobalMemory {
		t.Errorf("ceiling switches = %+v, want read_only only", who.Ceiling)
	}

	// A caller without a ceiling omits the key entirely.
	body := readBody(t, do(t, s, http.MethodGet, "/v1/auth/whoami", "tok-alice", nil))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("decode whoami body: %v", err)
	}
	if _, present := raw["ceiling"]; present {
		t.Errorf("whoami of an unrestricted caller must omit ceiling: %s", body)
	}

	// A live swap (what a config reload does) is what the next request reports.
	s.SetServerConfig(&config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "root", Token: "tok-root", CanAdmin: true},
		{ID: "ci", Token: "tok-ci", Ceiling: &config.CallerCeiling{
			Projects:  []string{"docs"},
			AllowExec: boolptr(true),
		}},
	}})
	decode(t, do(t, s, http.MethodGet, "/v1/auth/whoami", "tok-ci", nil), &who)
	if who.Ceiling == nil || strings.Join(who.Ceiling.Projects, ",") != "docs" {
		t.Fatalf("ceiling after the swap = %+v, want the reloaded projects", who.Ceiling)
	}
	if !who.Ceiling.AllowExec || who.Ceiling.ReadOnly {
		t.Errorf("ceiling switches after the swap = %+v, want allow_exec only", who.Ceiling)
	}

	// The ceiling disappears with the caller's entry.
	s.SetServerConfig(&config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "root", Token: "tok-root", CanAdmin: true},
		{ID: "ci", Token: "tok-ci"},
	}})
	body = readBody(t, do(t, s, http.MethodGet, "/v1/auth/whoami", "tok-ci", nil))
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("decode whoami body: %v", err)
	}
	if _, present := raw["ceiling"]; present {
		t.Errorf("removing the ceiling must remove the field: %s", body)
	}
}
