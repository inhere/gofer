package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// The hook's delivery must be findable in the server log: the claim/confirm HTTP route is the
// path every Claude Code / codex hook takes, and a letter that sat in a session for minutes left
// no trace there at all (the store's ConfirmSessionMail reports it — see jobstore/mail_log.go).

type mailLogRec struct {
	msg   string
	attrs map[string]string
}

func (r mailLogRec) attr(k string) string { return r.attrs[k] }

type mailLogRecorder struct {
	mu      sync.Mutex
	records []mailLogRec
}

func newMailLogRecorder(t *testing.T) *mailLogRecorder {
	t.Helper()
	rec := &mailLogRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec
}

func (r *mailLogRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *mailLogRecorder) Handle(_ context.Context, rec slog.Record) error {
	attrs := make(map[string]string, rec.NumAttrs())
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	r.mu.Lock()
	r.records = append(r.records, mailLogRec{msg: rec.Message, attrs: attrs})
	r.mu.Unlock()
	return nil
}

func (r *mailLogRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *mailLogRecorder) WithGroup(string) slog.Handler      { return r }

func (r *mailLogRecorder) find(event string) []mailLogRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []mailLogRec
	for _, rec := range r.records {
		if rec.msg == event {
			out = append(out, rec)
		}
	}
	return out
}

func (r *mailLogRecorder) hasText(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if strings.Contains(rec.msg, sub) {
			return true
		}
		for _, v := range rec.attrs {
			if strings.Contains(v, sub) {
				return true
			}
		}
	}
	return false
}

// TestSessionMailConfirmLogsDelivery: POST /v1/sessions/{sid}/mail/confirm logs one
// mail.delivered per confirmed letter (path "hook"), and the body stays out of the log.
func TestSessionMailConfirmLogsDelivery(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	const body = "hook-route-private-8f2"
	id := e.putMail(t, e.sA, false, body)
	rec := newMailLogRecorder(t)

	status, v := e.claim(t, mailTokAlice, e.sA, map[string]any{})
	if status != http.StatusOK || len(v.Mails) != 1 {
		t.Fatalf("claim = %d %+v", status, v)
	}
	if n := len(rec.find("mail.delivered")); n != 0 {
		t.Fatalf("a claim was logged as a delivery: %d", n)
	}

	status, raw := e.req(t, mailTokAlice, "", "POST", "/v1/sessions/"+e.sA+"/mail/confirm",
		map[string]any{"ids": []int64{id}})
	if status != http.StatusOK || !strings.Contains(raw, "\"confirmed\":["+jsonID(id)+"]") {
		t.Fatalf("confirm = %d %s", status, raw)
	}
	got := rec.find("mail.delivered")
	if len(got) != 1 {
		t.Fatalf("mail.delivered lines = %d, want 1", len(got))
	}
	if got[0].attr("mail_id") != strconv.FormatInt(id, 10) || got[0].attr("path") != jobstore.MailPathHook ||
		got[0].attr("to_id") != e.sA {
		t.Fatalf("mail.delivered = %+v", got[0].attrs)
	}
	if _, ok := got[0].attrs["latency_sec"]; !ok {
		t.Fatalf("mail.delivered carries no age: %+v", got[0].attrs)
	}
	if rec.hasText(body) {
		t.Fatalf("the letter body reached the log")
	}
}

// TestSessionMailCompleteTurnLogsDelivery: the Stop hook's take (complete-mail) is the same
// delivery, so it reports itself the same way.
func TestSessionMailCompleteTurnLogsDelivery(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	turn := e.openTurn(t)
	id := e.putMail(t, e.sA, false, "stop-path")
	rec := newMailLogRecorder(t)

	status, raw := e.req(t, mailTokAlice, "", "POST",
		"/v1/sessions/"+e.sA+"/turns/"+turn+"/complete-mail", map[string]any{})
	if status != http.StatusOK || !strings.Contains(raw, "\"completed\":true") {
		t.Fatalf("complete-mail = %d %s", status, raw)
	}
	got := rec.find("mail.delivered")
	if len(got) != 1 || got[0].attr("mail_id") != strconv.FormatInt(id, 10) ||
		got[0].attr("path") != jobstore.MailPathHook {
		t.Fatalf("mail.delivered = %+v (%d)", got, len(got))
	}
}
