package sessionrelay

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// The relay's log events (log_events.go), driven through the real call paths: a state
// transition is reported once, and a delivery says which path carried it (or why none did).

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

func (r *logRecorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, rec := range r.records {
		b.WriteString(rec.msg)
		for k, v := range rec.attrs {
			b.WriteString(" " + k + "=" + v)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TestHeartbeatLogsStateChange: a hook event that moves the session's state is one line
// (session / from / to / event), and a beat that changes nothing is none.
func TestHeartbeatLogsStateChange(t *testing.T) {
	s := newSvc(t)
	if _, err := s.Register(RegisterInput{SessionID: "sid-state", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	rec := newLogRecorder(t)

	// The first beat only establishes the state this process observed.
	if _, err := s.Heartbeat("sid-state", HeartbeatInput{Event: EventUserPromptSubmit}); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.find(EventStateChanged)); n != 0 {
		t.Fatalf("the first observation was reported as a change: %s", rec.text())
	}

	if _, err := s.Heartbeat("sid-state", HeartbeatInput{Event: EventStop}); err != nil {
		t.Fatal(err)
	}
	got := rec.find(EventStateChanged)
	if len(got) != 1 {
		t.Fatalf("session.state_changed lines = %d: %s", len(got), rec.text())
	}
	if got[0].attr("session") != "sid-state" || got[0].attr("from") != jobstore.SessionRunning ||
		got[0].attr("to") != jobstore.SessionIdle || got[0].attr("event") != EventStop {
		t.Fatalf("session.state_changed = %+v", got[0].attrs)
	}

	// Another Stop keeps the state: heartbeats themselves are not logged.
	if _, err := s.Heartbeat("sid-state", HeartbeatInput{Event: EventStop}); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.find(EventStateChanged)); n != 1 {
		t.Fatalf("an unchanged state was reported again: %d", n)
	}
}

// TestDeliverLogsPathAndFailure: one session.deliver line per attempt — the path that carried
// the text, and the reason a failed attempt had none. The text itself never reaches the log.
func TestDeliverLogsPathAndFailure(t *testing.T) {
	s := newSvc(t)
	s.SetInjector(&fakeInjector{res: InjectResult{JobID: "job-inject-7", ExitCode: 0}})
	tmuxSession(t, s, "sid-log-0001")
	rec := newLogRecorder(t)
	const text = "private-relay-payload-77"

	res, err := s.Deliver(context.Background(), "sid-log-0001", text, "alice", "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathTmux, res.Path)
	ok := rec.find(EventDeliver)
	if len(ok) != 1 || ok[0].attr("session") != "sid-log-0001" || ok[0].attr("path") != PathTmux ||
		ok[0].attr("outcome") != "ok" || ok[0].attr("job_id") != "job-inject-7" || ok[0].attr("by") != "alice" {
		t.Fatalf("session.deliver = %+v (%d): %s", ok, len(ok), rec.text())
	}
	if strings.Contains(rec.text(), text) {
		t.Fatalf("the delivered text reached the log: %s", rec.text())
	}

	// A session with no way to be reached: the failure carries the reason code.
	_, err = s.Register(RegisterInput{SessionID: "sid-log-0002", Agent: "claude", Event: EventSessionStart})
	assert.NoErr(t, err)
	_, err = s.Deliver(context.Background(), "sid-log-0002", "hello", "alice", "alice", false)
	assert.Eq(t, ReasonNoRunner, DeliverReason(err))
	failed := rec.find(EventDeliver)
	if len(failed) != 2 || failed[1].attr("outcome") != "failed" || failed[1].attr("reason") != ReasonNoRunner ||
		failed[1].attr("session") != "sid-log-0002" {
		t.Fatalf("failed session.deliver = %+v (%d): %s", failed, len(failed), rec.text())
	}

	// A held message is a result of its own kind (it waits in the mailbox).
	s2 := newSvc(t)
	s2.SetInjector(&fakeInjector{res: InjectResult{JobID: "job-inject-8", ExitCode: 0}})
	tmuxSession(t, s2, "sid-log-0003")
	if _, err := s2.store.SetSessionInjectMode("sid-log-0003", jobstore.InjectModeHold); err != nil {
		t.Fatal(err)
	}
	held, err := s2.Deliver(context.Background(), "sid-log-0003", "keep me", "alice", "alice", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathHeld, held.Path)
	if held.MailID <= 0 {
		t.Fatalf("a held delivery has no letter: %+v", held)
	}
	out := rec.find(EventDeliver)
	if len(out) != 3 || out[2].attr("path") != PathHeld || out[2].attr("outcome") != "ok" ||
		out[2].attr("mail_id") != strconv.FormatInt(held.MailID, 10) {
		t.Fatalf("held session.deliver = %+v (%d): %s", out, len(out), rec.text())
	}
}
