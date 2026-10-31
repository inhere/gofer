package desktop

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
)

// recordingNotifier captures what would have been shown; no popup is executed.
type recordingNotifier struct {
	mu   sync.Mutex
	seen []Notification
	err  error
}

func (r *recordingNotifier) Show(n Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.seen = append(r.seen, n)
	return nil
}

func (r *recordingNotifier) shown() []Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Notification(nil), r.seen...)
}

// setErr changes the verdict of later Show calls (the fake desktop settings switching
// off and on again).
func (r *recordingNotifier) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// A window of 0 pops immediately: nothing is merged, so a single event arrives whole.
func TestDispatcherShowsImmediatelyWithoutWindow(t *testing.T) {
	rec := &recordingNotifier{}
	d := NewDispatcher(rec, 0)
	d.Push(Notification{EventType: "job.needs_review", Title: "job 等待验收：a", Text: "project p", Link: "https://x/jobs/a"})

	got := rec.shown()
	if len(got) != 1 {
		t.Fatalf("shown = %d, want 1", len(got))
	}
	if got[0].Title != "job 等待验收：a" || got[0].Link != "https://x/jobs/a" {
		t.Errorf("notification = %+v", got[0])
	}
}

// Same-type events inside the window become ONE popup naming the count; a different
// type stays a popup of its own.
func TestDispatcherMergesSameTypeInsideWindow(t *testing.T) {
	rec := &recordingNotifier{}
	d := NewDispatcher(rec, time.Hour) // the timer never fires: Flush drives the test
	d.Push(Notification{EventType: "job.awaiting_approval", Title: "待批准：a", Text: "理由：x", Link: "https://x/jobs/a"})
	d.Push(Notification{EventType: "job.awaiting_approval", Title: "待批准：b", Link: "https://x/jobs/b"})
	d.Push(Notification{EventType: "job.needs_review", Title: "job 等待验收：c"})

	if got := rec.shown(); len(got) != 0 {
		t.Fatalf("shown before flush = %d, want 0 (still queued)", len(got))
	}
	d.Flush()

	got := rec.shown()
	if len(got) != 2 {
		t.Fatalf("shown = %d, want 2 (%+v)", len(got), got)
	}
	merged := got[0]
	if merged.Title != "2 个 job 等待批准" {
		t.Errorf("merged title = %q, want %q", merged.Title, "2 个 job 等待批准")
	}
	if !strings.Contains(merged.Text, "待批准：a") || !strings.Contains(merged.Text, "待批准：b") {
		t.Errorf("merged text = %q, want both items", merged.Text)
	}
	if merged.Link != "https://x/jobs/a" {
		t.Errorf("merged link = %q, want the first item's", merged.Link)
	}
	if got[1].EventType != "job.needs_review" || got[1].Title != "job 等待验收：c" {
		t.Errorf("single item = %+v, want it unmerged", got[1])
	}
}

// A merged body lists at most maxMergedLines items and says how many are left.
func TestDispatcherMergedBodyIsBounded(t *testing.T) {
	rec := &recordingNotifier{}
	// The timer never fires here: Flush drives the test through the same merge path.
	d := NewDispatcher(rec, time.Hour)
	for i := 0; i < maxMergedLines+3; i++ {
		d.Push(Notification{EventType: "session.awaiting_reply", Title: "会话等你回复：s"})
	}
	d.Flush()

	got := rec.shown()
	if len(got) != 1 {
		t.Fatalf("shown = %d, want 1 merged popup", len(got))
	}
	if got[0].Title != "11 个会话等你回复" {
		t.Errorf("title = %q", got[0].Title)
	}
	lines := strings.Split(got[0].Text, "\n")
	if len(lines) != maxMergedLines+1 || !strings.Contains(lines[maxMergedLines], "另有 3 条") {
		t.Errorf("body lines = %v, want %d items + a summary", lines, maxMergedLines)
	}
}

// The timer alone flushes: a queued notification is shown after the window passes.
func TestDispatcherTimerFlushes(t *testing.T) {
	rec := &recordingNotifier{}
	d := NewDispatcher(rec, 15*time.Millisecond)
	d.Push(Notification{EventType: "interaction.created", Title: "需要审批：x"})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.shown()) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("shown = %d after the window, want 1", len(rec.shown()))
}

// Close flushes the tail and stops accepting: shutdown must not swallow a queued popup.
func TestDispatcherCloseFlushesAndStops(t *testing.T) {
	rec := &recordingNotifier{}
	d := NewDispatcher(rec, time.Hour)
	d.Push(Notification{EventType: "job.needs_review", Title: "job 等待验收：a"})
	d.Close()

	if got := rec.shown(); len(got) != 1 {
		t.Fatalf("shown after Close = %d, want 1", len(got))
	}
	d.Push(Notification{EventType: "job.needs_review", Title: "job 等待验收：b"})
	d.Flush()
	if got := rec.shown(); len(got) != 1 {
		t.Errorf("shown = %d after pushing to a closed dispatcher, want still 1", len(got))
	}
}

// A skip (no desktop / tool missing) is reported once per reason; a real failure is
// news every time. The assertions are on counters, so no log sink is needed.
func TestDispatcherReportsSkipOncePerReason(t *testing.T) {
	rec := &recordingNotifier{err: &SkipError{Reason: "windows session 0 (service session, no interactive desktop)"}}
	d := NewDispatcher(rec, 0)
	for i := 0; i < 3; i++ {
		d.Push(Notification{EventType: "job.needs_review", Title: "x"})
	}
	if len(d.logged) != 1 || !d.logged["windows session 0 (service session, no interactive desktop)"] {
		t.Fatalf("logged = %v, want the one skip reason", d.logged)
	}
	// A failing popup command is not a skip: nothing is remembered, so the next event
	// is reported again.
	d2 := NewDispatcher(&recordingNotifier{err: errors.New("toast failed")}, 0)
	d2.Push(Notification{EventType: "job.needs_review", Title: "x"})
	if len(d2.logged) != 0 {
		t.Errorf("logged = %v, want nothing for a real failure", d2.logged)
	}
}

// logCapture records the slog calls of the dispatcher, so "once per cause" is asserted
// on the real log lines instead of on internal counters.
type logCapture struct {
	mu      sync.Mutex
	skipped []string // reason attribute of each notify.desktop_skipped line
}

func (h *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *logCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "notify.desktop_skipped" {
		return nil
	}
	reason := ""
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "reason" {
			reason = a.Value.String()
		}
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.skipped = append(h.skipped, reason)
	return nil
}

func (h *logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logCapture) WithGroup(string) slog.Handler      { return h }

func (h *logCapture) reasons() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.skipped...)
}

// useLogCapture routes the default logger into a capture handler for one test.
func useLogCapture(t *testing.T) *logCapture {
	t.Helper()
	h := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// The skip line is written once per CAUSE, not once per event: a cause that changes is
// reported again, and so is the same cause after the channel recovered (the person
// switched notifications back on and later off again).
func TestDispatcherReportsSkipOncePerCauseAndAgainAfterRecovery(t *testing.T) {
	h := useLogCapture(t)
	rec := &recordingNotifier{}
	d := NewDispatcher(rec, 0)

	disabled := &SkipError{Reason: toastDisabledReason("DisabledForUser")}
	rec.setErr(disabled)
	for i := 0; i < 3; i++ {
		d.Push(Notification{EventType: "job.needs_review", Title: "x"})
	}
	if got := h.reasons(); len(got) != 1 {
		t.Fatalf("skipped lines = %v, want 1 for three events on the same cause", got)
	}

	// Same cause still disabled: still one line.
	d.Push(Notification{EventType: "job.needs_review", Title: "y"})
	if got := h.reasons(); len(got) != 1 {
		t.Fatalf("skipped lines = %v, want still 1", got)
	}

	// A different setting value is a different cause.
	rec.setErr(&SkipError{Reason: toastDisabledReason("DisabledByGroupPolicy")})
	d.Push(Notification{EventType: "job.needs_review", Title: "z"})
	if got := h.reasons(); len(got) != 2 {
		t.Fatalf("skipped lines = %v, want a second line for the changed cause", got)
	}

	// The popup works again, then the same cause returns: report it again.
	rec.setErr(nil)
	d.Push(Notification{EventType: "job.needs_review", Title: "ok"})
	rec.setErr(disabled)
	d.Push(Notification{EventType: "job.needs_review", Title: "w"})
	reasons := h.reasons()
	if len(reasons) != 3 {
		t.Fatalf("skipped lines = %v, want a third line after recovery", reasons)
	}
	if !strings.Contains(reasons[2], "DisabledForUser") || !strings.Contains(reasons[2], "Windows 设置") {
		t.Errorf("reason after recovery = %q, want the setting value and the fix hint", reasons[2])
	}
}

func TestDispatcherSkipsEmptyNotification(t *testing.T) {
	rec := &recordingNotifier{}
	d := NewDispatcher(rec, 0)
	d.Push(Notification{EventType: "job.needs_review"})
	if got := rec.shown(); len(got) != 0 {
		t.Errorf("shown = %+v, want nothing for an empty notification", got)
	}
}

// The toast XML carries the title, the body lines and the click target, and escapes
// everything a job title may contain.
func TestToastXML(t *testing.T) {
	xml := toastXML(Notification{
		Title: `待批准：a & b <c>`,
		Text:  "理由：x\n要执行：rm -rf /",
		Link:  "https://x/jobs/a?q=1&r=2",
	})
	for _, want := range []string{
		`<toast activationType="protocol" launch="https://x/jobs/a?q=1&amp;r=2">`,
		`<text>待批准：a &amp; b &lt;c&gt;</text>`,
		`<text>理由：x</text>`,
		`<text>要执行：rm -rf /</text>`,
		`<action content="打开" activationType="protocol" arguments="https://x/jobs/a?q=1&amp;r=2"/>`,
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("toast xml missing %q:\n%s", want, xml)
		}
	}
}

// No link, no activation: a toast without a click target must not carry an empty one.
func TestToastXMLWithoutLink(t *testing.T) {
	xml := toastXML(Notification{Title: "job 等待验收：a", Text: "project p"})
	if strings.Contains(xml, "activationType") || strings.Contains(xml, "<actions>") {
		t.Errorf("toast xml has an activation without a link:\n%s", xml)
	}
}

// A merged popup and the toast's open button follow the notification's language; an
// event type with no label of its own keeps its wire name in either language.
func TestMergedPopupAndToastFollowLanguage(t *testing.T) {
	rec := &recordingNotifier{}
	d := NewDispatcher(rec, time.Hour)
	for i := 0; i < maxMergedLines+3; i++ {
		d.Push(Notification{EventType: "session.awaiting_reply", Title: "Session waits: s", Lang: i18n.EN})
	}
	for i := 0; i < 2; i++ {
		d.Push(Notification{EventType: "x.custom", Title: "t", Lang: i18n.EN})
	}
	d.Flush()
	got := rec.shown()
	if len(got) != 2 {
		t.Fatalf("shown = %d, want 2 merged popups", len(got))
	}
	if got[0].Title != "11 sessions wait for your reply" {
		t.Errorf("title = %q", got[0].Title)
	}
	if last := strings.Split(got[0].Text, "\n"); !strings.Contains(last[len(last)-1], "and 3 more") {
		t.Errorf("summary line = %q", last[len(last)-1])
	}
	if got[1].Title != "2 notifications (x.custom)" {
		t.Errorf("custom title = %q", got[1].Title)
	}
	xml := toastXML(Notification{Title: "t", Link: "https://x/a", Lang: i18n.EN})
	if !strings.Contains(xml, `<action content="Open" `) {
		t.Errorf("toast xml has no English open button:\n%s", xml)
	}
}
