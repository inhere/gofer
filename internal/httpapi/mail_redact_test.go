package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// M8 of the mailbox (design 2026-10-16「敏感信息」) over HTTP: the scan report carries a mail
// section the caller's own letters are filtered into, and POST /v1/mail/{id}/redact follows job
// redaction's rule — the letter's two sides or an administrator — with a body the secret never
// travels back through.

const httpMailSecret = "http-mail-secret-b41"

// mailSecretServer builds the four-caller fixture these tests share: alice (the sender), bob (the
// owner of the recipient session), carol (unrelated) and an administrator.
func mailSecretServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
		{ID: "carol", Token: "tok-carol"},
		{ID: "admin", Token: "tok-admin", CanAdmin: true},
	}})
}

// insertLetterToBob stores one letter from caller alice (from_caller) to the session bob owns.
func insertLetterToBob(t *testing.T, s *Server, body string) jobstore.Mail {
	t.Helper()
	if _, err := s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: "sess-bob", Agent: "claude", ProjectKey: "self", CallerID: "bob",
		State: jobstore.SessionRunning, LastSeenAt: 100, StartedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	m, err := s.jobs.Meta().InsertMail(jobstore.MailInput{
		FromKind: jobstore.MailKindSession, FromID: "sess-alice", FromCaller: "alice",
		ToAddress: "session:sess-bob", ToKind: jobstore.MailKindSession, ToID: "sess-bob",
		Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestSecretScanReportHasMailSection: the report's mail section shows the caller's own letters
// (either side), keeps the secret out of the answer, and an administrator sees every letter.
func TestSecretScanReportHasMailSection(t *testing.T) {
	s := mailSecretServer(t)
	mine := insertLetterToBob(t, s, "the token is "+httpMailSecret)
	// A letter carol sent to alice's session: alice is its recipient.
	if _, err := s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: "sess-alice", Agent: "claude", ProjectKey: "self", CallerID: "alice",
		State: jobstore.SessionRunning, LastSeenAt: 100, StartedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	toAlice, err := s.jobs.Meta().InsertMail(jobstore.MailInput{
		FromKind: jobstore.MailKindSession, FromID: "sess-carol", FromCaller: "carol",
		ToAddress: "session:sess-alice", ToKind: jobstore.MailKindSession, ToID: "sess-alice",
		Body: "carol wrote " + httpMailSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"literals": []string{httpMailSecret}}

	raw := readBody(t, do(t, s, http.MethodPost, "/v1/jobs/secret-scan", "tok-alice", body))
	if strings.Contains(raw, httpMailSecret) {
		t.Fatalf("scan echoed the literal: %s", raw)
	}
	if !strings.Contains(raw, `"mail"`) || strings.Count(raw, `"mail_id"`) != 2 {
		t.Fatalf("alice's scan = %s, want both her letters in the mail section", raw)
	}
	if !strings.Contains(raw, `"thread_id":"`+mine.ThreadID+`"`) || !strings.Contains(raw, `"db:mail.body"`) {
		t.Fatalf("alice's scan = %s, want the letter's thread and masked location", raw)
	}
	if !strings.Contains(raw, `"mail_id":`+itoaInt(mine.ID)) || !strings.Contains(raw, `"mail_id":`+itoaInt(toAlice.ID)) {
		t.Fatalf("alice's scan = %s, want letters %d and %d", raw, mine.ID, toAlice.ID)
	}

	// carol is only the sender of one of them.
	carol := readBody(t, do(t, s, http.MethodPost, "/v1/jobs/secret-scan", "tok-carol", body))
	if strings.Count(carol, `"mail_id"`) != 1 || !strings.Contains(carol, `"mail_id":`+itoaInt(toAlice.ID)) {
		t.Fatalf("carol's scan = %s, want only the letter she sent", carol)
	}
	// The administrator is unfiltered.
	admin := readBody(t, do(t, s, http.MethodPost, "/v1/jobs/secret-scan", "tok-admin", body))
	if strings.Count(admin, `"mail_id"`) != 2 {
		t.Fatalf("admin's scan = %s, want both letters", admin)
	}
}

// TestMailRedactRoutePermissions: the letter's two sides or an administrator may redact; anybody
// else is refused, and the answer carries counts only.
func TestMailRedactRoutePermissions(t *testing.T) {
	s := mailSecretServer(t)
	m := insertLetterToBob(t, s, "the token is "+httpMailSecret)
	path := "/v1/mail/" + itoaInt(m.ID) + "/redact"
	body := map[string]any{"literals": []string{httpMailSecret}}

	// carol is neither side.
	if resp := do(t, s, http.MethodPost, path, "tok-carol", body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("carol's redaction = %d: %s", resp.StatusCode, readBody(t, resp))
	} else {
		raw := readBody(t, resp)
		if strings.Contains(raw, httpMailSecret) {
			t.Fatalf("the refusal echoed the literal: %s", raw)
		}
	}
	// An empty input is a 400, not a silent no-op.
	if resp := do(t, s, http.MethodPost, path, "tok-alice", map[string]any{}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty redaction = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	// Unknown letter: 404.
	if resp := do(t, s, http.MethodPost, "/v1/mail/987654/redact", "tok-admin", body); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown letter = %d: %s", resp.StatusCode, readBody(t, resp))
	}

	// bob owns the recipient: he may redact, and the report is counts only.
	resp := do(t, s, http.MethodPost, path, "tok-bob", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob's redaction = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	raw := readBody(t, resp)
	if strings.Contains(raw, httpMailSecret) {
		t.Fatalf("the report echoed the literal: %s", raw)
	}
	if !strings.Contains(raw, `"DBMatches":1`) {
		t.Fatalf("report = %s, want one db match", raw)
	}
	got, ok, err := s.jobs.Meta().GetMail(m.ID)
	if err != nil || !ok {
		t.Fatalf("read the letter back: ok=%t err=%v", ok, err)
	}
	if strings.Contains(got.Body, httpMailSecret) {
		t.Fatalf("letter body = %q, want the literal gone", got.Body)
	}
	// The sender side and the administrator reach an already redacted letter too (a no-op for
	// them, still allowed).
	for _, token := range []string{"tok-alice", "tok-admin"} {
		if resp := do(t, s, http.MethodPost, path, token, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s redaction = %d: %s", token, resp.StatusCode, readBody(t, resp))
		}
	}
}

func itoaInt(id int64) string { return strconv.FormatInt(id, 10) }
