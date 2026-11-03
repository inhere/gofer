package jobstore

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The two letter transitions the store reports itself (mail_log.go): the hook's confirmed take
// (the only place a hook delivery is written) and the expiry of a broadcast copy.

type logRec struct {
	msg   string
	attrs map[string]string
}

func (r logRec) attr(k string) string { return r.attrs[k] }

type logRecorder struct {
	mu      sync.Mutex
	records []logRec
}

func newLogRecorder(t *testing.T) *logRecorder {
	t.Helper()
	rec := &logRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	attrs := make(map[string]string, rec.NumAttrs())
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	r.mu.Lock()
	r.records = append(r.records, logRec{msg: rec.Message, attrs: attrs})
	r.mu.Unlock()
	return nil
}

func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *logRecorder) WithGroup(string) slog.Handler      { return r }

func (r *logRecorder) find(event string) []logRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []logRec
	for _, rec := range r.records {
		if rec.msg == event {
			out = append(out, rec)
		}
	}
	return out
}

// TestHookTakeLogsDelivered: claim → confirm is the hook's delivery (path "hook"), reported
// once the confirmation landed — with how long the letter waited, and never its body.
func TestHookTakeLogsDelivered(t *testing.T) {
	f := newMailFixture(t)
	if _, err := f.store.UpsertAgentSession(AgentSession{SessionID: "sess-b", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	const body = "hook-private-body-31c"
	m, err := f.store.InsertMail(sampleMail(func(in *MailInput) { in.Body = body }))
	if err != nil {
		t.Fatal(err)
	}
	rec := newLogRecorder(t)
	if _, _, err := f.store.ClaimSessionMail("sess-b", TakeMailOpts{}); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.find("mail.delivered")); n != 0 {
		t.Fatalf("a claim was logged as a delivery: %d", n)
	}

	f.advance(45 * time.Second)
	done, err := f.store.ConfirmSessionMail("sess-b", []int64{m.ID})
	if err != nil || len(done) != 1 {
		t.Fatalf("confirm = %+v err=%v", done, err)
	}
	got := rec.find("mail.delivered")
	if len(got) != 1 {
		t.Fatalf("mail.delivered lines = %d", len(got))
	}
	if got[0].attr("mail_id") != strconv.FormatInt(m.ID, 10) || got[0].attr("path") != MailPathHook ||
		got[0].attr("to_id") != "sess-b" || got[0].attr("latency_sec") != "45" {
		t.Fatalf("mail.delivered = %+v", got[0].attrs)
	}
	if rec.hasText(body) {
		t.Fatalf("the letter body reached the log")
	}
}

// hasText reports whether one recorded value contains sub (the "no body in the log" assertion).
func (r *logRecorder) hasText(sub string) bool {
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

// TestExpireOverdueLogsExpiredCopies: a broadcast copy that passes the delivery deadline is
// dropped by the sweep without being returned, so the store is the only place that can say so.
func TestExpireOverdueLogsExpiredCopies(t *testing.T) {
	f := newMailFixture(t)
	m, err := f.store.InsertMail(sampleMail(func(in *MailInput) {
		in.BroadcastID, in.DeliverBy = 7, 1
	}))
	if err != nil {
		t.Fatal(err)
	}
	rec := newLogRecorder(t)
	held, err := f.store.ExpireOverdue(f.now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Fatalf("a broadcast copy must not be parked for a person: %+v", held)
	}
	got := rec.find("mail.expired")
	if len(got) != 1 || got[0].attr("mail_id") != strconv.FormatInt(m.ID, 10) ||
		got[0].attr("broadcast_id") != "7" || got[0].attr("reason") == "" {
		t.Fatalf("mail.expired = %+v (%d)", got, len(got))
	}
}
