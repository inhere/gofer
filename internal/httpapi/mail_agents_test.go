package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/mail"
)

// The `agent:` routes (设计 2026-10-22 mail-agent-address「D6」): the listing, the manual end, and
// that sending to an agent costs job.submit (it may start a resident session job).

func mailEnvWithAgent(t *testing.T) *mailEnv {
	t.Helper()
	e := newMailEnv(t)
	e.s.mail.SetResidents(mail.ResidentsConfig{Max: 6, Agents: map[string]mail.ResidentAgentSpec{
		"suga": {Name: "suga", Agent: "suga-acp", Idle: 120 * time.Minute, MaxLetters: 50, MaxHours: 24},
	}})
	return e
}

func TestMailAgentsListing(t *testing.T) {
	e := mailEnvWithAgent(t)
	status, body := e.req(t, mailTokBob, "", "GET", "/v1/mail/agents", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/mail/agents = %d (%s)", status, body)
	}
	var got struct {
		MaxResident int `json:"agents_max_resident"`
		Agents      []struct {
			Name       string `json:"name"`
			Agent      string `json:"agent"`
			IdleMinute int    `json:"idle_minutes"`
		} `json:"agents"`
		Residents []json.RawMessage `json:"residents"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if got.MaxResident != 6 || len(got.Agents) != 1 || got.Agents[0].Name != "suga" || got.Agents[0].IdleMinute != 120 {
		t.Fatalf("listing = %s", body)
	}
	if got.Residents == nil {
		t.Fatalf("residents must be an empty array, not null: %s", body)
	}
}

func TestMailAgentEndWithoutASession(t *testing.T) {
	e := mailEnvWithAgent(t)
	status, body := e.req(t, mailTokBob, "", "POST", "/v1/mail/agents/suga/end?project=p1", map[string]any{})
	if status != http.StatusNotFound {
		t.Fatalf("ending a session that does not exist = %d (%s), want 404", status, body)
	}
}

func TestMailAgentSendRespectsTheCallerCeiling(t *testing.T) {
	e := mailEnvWithAgent(t)
	e.grant(authz.PermJobSubmit)
	// carol's ceiling is project p1: a resident session in p2 is refused before anything is
	// stored (it would otherwise sit pending until the session start is refused and expires).
	status, body := e.req(t, mailTokCarol, "", "POST", "/v1/mail",
		map[string]any{"to": "agent:suga@p2", "body": "hello", "project": "p1"})
	if status != http.StatusForbidden {
		t.Fatalf("agent: send outside the ceiling = %d (%s), want 403", status, body)
	}
	if n, err := e.s.jobs.Meta().CountMailFrom(jobstore.MailKindCaller, "", "carol", 0); err != nil || n != 0 {
		t.Fatalf("carol's letters = %d (err=%v), want none stored", n, err)
	}
	// Inside the ceiling the same send goes through.
	status, body = e.req(t, mailTokCarol, "", "POST", "/v1/mail",
		map[string]any{"to": "agent:suga@p1", "body": "hello", "project": "p1"})
	if status != http.StatusOK {
		t.Fatalf("agent: send inside the ceiling = %d (%s), want 200", status, body)
	}
}
func TestMailAgentSendNeedsJobSubmit(t *testing.T) {
	e := mailEnvWithAgent(t)
	// No permissions at all: the agent address refuses before anything is stored.
	e.grant()
	status, body := e.req(t, mailTokBob, "", "POST", "/v1/mail",
		map[string]any{"to": "agent:suga", "body": "hello", "project": "p1"})
	wantPermission(t, "agent: send", status, body, authz.PermJobSubmit)

	// With job.submit the letter is stored pending for the resident session.
	e.grant(authz.PermJobSubmit)
	status, body = e.req(t, mailTokBob, "", "POST", "/v1/mail",
		map[string]any{"to": "agent:suga@other", "body": "hello", "project": "p1"})
	if status != http.StatusOK {
		t.Fatalf("agent: send with job.submit = %d (%s)", status, body)
	}
	v := decodeSent(t, body)
	if v.Mail.To.Kind != "agent" || v.Mail.To.ID != "suga@other" {
		t.Fatalf("the letter's recipient = %+v, want the agent address", v.Mail.To)
	}

	// A name nobody configured: 400 naming what is available.
	status, body = e.req(t, mailTokBob, "", "POST", "/v1/mail",
		map[string]any{"to": "agent:nobody", "body": "hello", "project": "p1"})
	if status != http.StatusBadRequest || !strings.Contains(body, "suga") {
		t.Fatalf("unknown agent = %d (%s), want 400 naming the configured names", status, body)
	}
}
