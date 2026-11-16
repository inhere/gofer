package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// expiryFixture is a real-Core server with a pinned auth clock and one stored caller `ci`
// created with the given expires value.
func expiryFixture(t *testing.T, expires string) (s *Server, clock *time.Time, token string) {
	t.Helper()
	s, _, _ = callerAdminServer(t)
	base := time.Unix(1_900_000_000, 0)
	clock = &base
	s.authClock = func() time.Time { return *clock }
	body := map[string]any{"id": "ci", "ceiling": ceilingCase("self")}
	if expires != "" {
		body["expires"] = expires
	}
	created, status := addCaller(t, s, body)
	if status != http.StatusCreated {
		t.Fatalf("add caller = %d", status)
	}
	return s, clock, created.Token
}

func storedRow(t *testing.T, s *Server, id string) jobstore.StoredCaller {
	t.Helper()
	row, ok, err := s.jobs.Meta().GetStoredCaller(id)
	if err != nil || !ok {
		t.Fatalf("stored caller %s: ok=%v err=%v", id, ok, err)
	}
	return row
}

// TestCallerTokenExpiryRefusesWith401AndAudits: before the expiry the token works; from the
// expiry on it is a 401 and auth_audit gets request.denied reason=token_expired; the root
// administrator is unaffected.
func TestCallerTokenExpiryRefusesWith401AndAudits(t *testing.T) {
	s, clock, token := expiryFixture(t, "1d")
	if got := storedRow(t, s, "ci").ExpiresAt; got != clock.Unix()+86400 {
		t.Fatalf("expires_at = %d, want now+1d", got)
	}
	if resp := do(t, s, http.MethodGet, "/v1/projects", token, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("before the expiry = %d, want 200", resp.StatusCode)
	}
	*clock = clock.Add(24*time.Hour - time.Second)
	if resp := do(t, s, http.MethodGet, "/v1/projects", token, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("one second before the expiry = %d, want 200", resp.StatusCode)
	}
	*clock = clock.Add(time.Second)
	if resp := do(t, s, http.MethodGet, "/v1/projects", token, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("at the expiry = %d, want 401", resp.StatusCode)
	}
	entries, err := s.jobs.Meta().ListAuthAudit(jobstore.AuthAuditFilter{Subject: "caller:ci"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "request.denied" && e.Reason == "token_expired" && e.Status == http.StatusUnauthorized && e.Actor == "ci" && e.Path == "/v1/projects" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no token_expired audit line: %+v", entries)
	}
	if resp := do(t, s, http.MethodGet, "/v1/projects", "tok-root", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("root administrator = %d, want 200 (a file caller never expires)", resp.StatusCode)
	}
}

// TestCallerTokenNeverExpiresByDefault: no expires (or never) leaves the token valid however
// far the clock moves.
func TestCallerTokenNeverExpiresByDefault(t *testing.T) {
	for _, expires := range []string{"", "never"} {
		s, clock, token := expiryFixture(t, expires)
		if got := storedRow(t, s, "ci").ExpiresAt; got != 0 {
			t.Fatalf("expires=%q: expires_at = %d, want 0", expires, got)
		}
		*clock = clock.Add(20 * 365 * 24 * time.Hour)
		if resp := do(t, s, http.MethodGet, "/v1/projects", token, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("expires=%q: far future = %d, want 200", expires, resp.StatusCode)
		}
	}
}

// TestCallerRotateKeepsOrReplacesExpiry: a rotation without expires keeps the old expiry, with
// one it replaces it, `never` clears it; the new token is live and the old one dead.
func TestCallerRotateKeepsOrReplacesExpiry(t *testing.T) {
	s, clock, token := expiryFixture(t, "30d")
	want := clock.Unix() + 30*86400
	rotate := func(body any) config.CallerChange {
		t.Helper()
		resp := do(t, s, http.MethodPost, "/v1/callers/ci/rotate", "tok-root", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("rotate %v = %d", body, resp.StatusCode)
		}
		var out config.CallerChange
		decode(t, resp, &out)
		return out
	}
	kept := rotate(nil)
	if kept.Token == "" || kept.Token == token || storedRow(t, s, "ci").ExpiresAt != want || kept.Caller.ExpiresAt != want {
		t.Fatalf("rotation without expires: %+v row=%d, want expiry kept at %d", kept.Caller, storedRow(t, s, "ci").ExpiresAt, want)
	}
	if resp := do(t, s, http.MethodGet, "/v1/projects", token, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the old token = %d, want 401", resp.StatusCode)
	}
	rotate(map[string]any{"expires": "90d"})
	if got := storedRow(t, s, "ci").ExpiresAt; got != clock.Unix()+90*86400 {
		t.Fatalf("expires 90d: %d", got)
	}
	last := rotate(map[string]any{"expires": "never"})
	if got := storedRow(t, s, "ci").ExpiresAt; got != 0 || last.Caller.ExpiresAt != 0 {
		t.Fatalf("expires never: %d", got)
	}
	if resp := do(t, s, http.MethodGet, "/v1/projects", last.Token, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the new token = %d, want 200", resp.StatusCode)
	}
}

// TestCallerExpiresValueIsValidated: a bad or past value is a 400 and nothing is created.
func TestCallerExpiresValueIsValidated(t *testing.T) {
	s, _, _ := callerAdminServer(t)
	s.authClock = func() time.Time { return time.Unix(1_900_000_000, 0) }
	for _, bad := range []string{"soon", "0d", "-3d", "2001-01-01"} {
		if _, status := addCaller(t, s, map[string]any{"id": "x" + strings.Trim(bad, "-"), "ceiling": ceilingCase("self"), "expires": bad}); status != http.StatusBadRequest {
			t.Errorf("expires %q = %d, want 400", bad, status)
		}
	}
	if _, status := addCaller(t, s, map[string]any{"id": "ok", "ceiling": ceilingCase("self"), "expires": "2031-01-31"}); status != http.StatusCreated {
		t.Errorf("a date = %d, want 201", status)
	}
	if got := storedRow(t, s, "ok").ExpiresAt; got != time.Date(2031, 1, 31, 23, 59, 59, 0, time.UTC).Unix() {
		t.Errorf("a date means the end of that day (UTC): %d", got)
	}
}

// TestCallerLastUsedIsBatchedAndStamped: authenticating does not write the database; the stamp
// lands when the batch is flushed, carries the request time, and an expired token never stamps.
func TestCallerLastUsedIsBatchedAndStamped(t *testing.T) {
	s, clock, token := expiryFixture(t, "1d")
	do(t, s, http.MethodGet, "/v1/projects", token, nil)
	if got := storedRow(t, s, "ci").LastUsedAt; got != 0 {
		t.Fatalf("last_used_at written before the batch: %d", got)
	}
	used := *clock
	*clock = clock.Add(10 * time.Second)
	do(t, s, http.MethodGet, "/v1/projects", token, nil) // a later use before the flush: the stamp follows
	s.FlushCallerUsage()
	if got := storedRow(t, s, "ci").LastUsedAt; got != used.Unix()+10 {
		t.Fatalf("last_used_at = %d, want %d", got, used.Unix()+10)
	}
	var listed callersView
	decode(t, do(t, s, http.MethodGet, "/v1/callers", "tok-root", nil), &listed)
	var seen int64
	for _, v := range listed.Callers {
		if v.ID == "ci" {
			seen = v.LastUsedAt
		}
	}
	if seen != used.Unix()+10 {
		t.Fatalf("the list shows last_used_at %d", seen)
	}
	*clock = clock.Add(48 * time.Hour)
	do(t, s, http.MethodGet, "/v1/projects", token, nil) // expired: refused, not stamped
	s.FlushCallerUsage()
	if got := storedRow(t, s, "ci").LastUsedAt; got != used.Unix()+10 {
		t.Fatalf("an expired request moved last_used_at to %d", got)
	}
}

// TestCallerUsageTrackerFlushesOnTimerAndDrain: the timer writes the batch by itself, and
// Drain writes what is left.
func TestCallerUsageTrackerFlushesOnTimerAndDrain(t *testing.T) {
	sink := &fakeUsageSink{got: make(chan map[string]int64, 4)}
	now := time.Unix(100, 0)
	u := newCallerUsage(func() usageSink { return sink }, 20*time.Millisecond, func() time.Time { return now })
	u.mark("a")
	select {
	case m := <-sink.got:
		if m["a"] != 100 {
			t.Fatalf("batch = %v", m)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the timer did not flush")
	}
	u.interval = time.Hour
	u.mark("b")
	u.flush()
	select {
	case m := <-sink.got:
		if m["b"] != 100 || len(m) != 1 {
			t.Fatalf("drain batch = %v", m)
		}
	default:
		t.Fatal("flush wrote nothing")
	}
	u.flush() // nothing waiting: no empty write
	select {
	case m := <-sink.got:
		t.Fatalf("an empty batch was written: %v", m)
	default:
	}
}

type fakeUsageSink struct{ got chan map[string]int64 }

func (f *fakeUsageSink) TouchCallersUsed(m map[string]int64) error { f.got <- m; return nil }

// TestExpiringTokenCardOnlyForCallerAdmin: the 「等我」 list of a person who can manage callers
// carries the card from 7 days before the expiry; another caller's list does not.
func TestExpiringTokenCardOnlyForCallerAdmin(t *testing.T) {
	s, clock, _ := expiryFixture(t, "3d")
	read := func(tok string) string {
		resp := do(t, s, http.MethodGet, "/v1/inbox", tok, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/inbox = %d", resp.StatusCode)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return string(b)
	}
	if body := read("tok-root"); !strings.Contains(body, "caller.token_expiring:ci") || !strings.Contains(body, `"waiting":"attention"`) {
		t.Fatalf("the root administrator's inbox lacks the token card:\n%s", body)
	}
	if body := read("tok-alice"); strings.Contains(body, "caller.token_expiring") {
		t.Fatalf("a plain caller must not see the token card:\n%s", body)
	}
	*clock = clock.Add(4 * 24 * time.Hour)
	if body := read("tok-root"); !strings.Contains(body, "caller.token_expiring:ci") {
		t.Fatalf("the card must stay once the token expired:\n%s", body)
	}
}
