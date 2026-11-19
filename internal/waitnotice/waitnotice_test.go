package waitnotice

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

type fakeSource struct {
	mu   sync.Mutex
	subs []Subject
}

func (f *fakeSource) set(subs ...Subject) { f.mu.Lock(); f.subs = subs; f.mu.Unlock() }
func (f *fakeSource) Waiting() ([]Subject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Subject(nil), f.subs...), nil
}

type fakeSender struct{ got []Notice }

func (f *fakeSender) Send(n Notice) { f.got = append(f.got, n) }

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func openStore(t *testing.T, path string) *jobstore.Store {
	t.Helper()
	st, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type rig struct {
	path   string
	st     *jobstore.Store
	src    *fakeSource
	out    *fakeSender
	clk    *clock
	remind time.Duration
	sc     *Scanner
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{src: &fakeSource{}, out: &fakeSender{}, clk: &clock{t: time.Unix(1_800_000_000, 0)}, remind: 30 * time.Minute}
	r.path = filepath.Join(t.TempDir(), "gofer.db")
	r.st = openStore(t, r.path)
	r.rebuild()
	return r
}

// reopen closes the store and opens the same file again, as a restart does.
func (r *rig) reopen(t *testing.T) {
	t.Helper()
	if err := r.st.Close(); err != nil {
		t.Fatal(err)
	}
	r.st = openStore(t, r.path)
	r.rebuild()
}

// rebuild makes a fresh scanner over the same store, as a restarted server has.
func (r *rig) rebuild() {
	r.sc = &Scanner{Store: r.st, Source: r.src, Sender: r.out, Now: r.clk.now, ReminderAfter: func() time.Duration { return r.remind }}
}

func (r *rig) sweep(t *testing.T) {
	t.Helper()
	if err := r.sc.Sweep(); err != nil {
		t.Fatal(err)
	}
}

func sub(key string) Subject {
	return Subject{Key: key, Category: "answer", CallerID: "u1", ProjectKey: "p", Title: "dev", ThreadID: "r:" + key}
}

func (r *rig) count(reminder bool) (n int) {
	for _, g := range r.out.got {
		if g.Reminder == reminder {
			n++
		}
	}
	return n
}

func TestNothingSentWhileNothingWaits(t *testing.T) {
	r := newRig(t)
	r.sweep(t)
	r.sweep(t)
	if len(r.out.got) != 0 {
		t.Fatalf("sent %+v with an empty list", r.out.got)
	}
}

func TestEntryNotifiesOnceAcrossManySweeps(t *testing.T) {
	r := newRig(t)
	r.src.set(sub("session:s1"))
	for i := 0; i < 5; i++ {
		r.sweep(t)
		r.clk.advance(30 * time.Second)
	}
	if len(r.out.got) != 1 || r.out.got[0].Reminder || r.out.got[0].Key != "session:s1" {
		t.Fatalf("sent = %+v, want exactly one first notice", r.out.got)
	}
}

func TestLeavingBeforeEntryNeverNotifies(t *testing.T) {
	// A turn answered inside the grace period, or a supervised session, never reaches the list:
	// the source simply does not return it.
	r := newRig(t)
	r.sweep(t)
	r.sweep(t)
	if len(r.out.got) != 0 {
		t.Fatalf("sent %+v", r.out.got)
	}
}

func TestSupervisionEndThenEntryNotifies(t *testing.T) {
	r := newRig(t)
	r.sweep(t) // supervised: not in the list
	if len(r.out.got) != 0 {
		t.Fatalf("sent while supervised: %+v", r.out.got)
	}
	r.src.set(sub("session:s1")) // supervision ended, the turn is past its grace
	r.sweep(t)
	if len(r.out.got) != 1 {
		t.Fatalf("sent = %+v, want one on entry", r.out.got)
	}
}

func TestRestartDoesNotResendAndDoesNotMiss(t *testing.T) {
	r := newRig(t)
	r.src.set(sub("session:s1"))
	r.sweep(t)
	r.reopen(t) // restart: the store is opened again, the scanner is new
	r.sweep(t)
	if r.count(false) != 1 {
		t.Fatalf("a notified subject was sent again after a restart: %+v", r.out.got)
	}

	// A subject that entered but was never notified (the server stopped in between) is sent
	// after the restart.
	if _, _, err := r.st.EnterWaitingNotice("u1", "session:s2", "answer", r.clk.now().Unix()); err != nil {
		t.Fatal(err)
	}
	r.src.set(sub("session:s1"), sub("session:s2"))
	r.reopen(t)
	r.sweep(t)
	if r.count(false) != 2 || r.out.got[len(r.out.got)-1].Key != "session:s2" {
		t.Fatalf("an entered but unnotified subject was missed: %+v", r.out.got)
	}
}

func TestReminderOnceAfterTheDelay(t *testing.T) {
	r := newRig(t)
	r.src.set(sub("session:s1"))
	r.sweep(t)
	r.clk.advance(29 * time.Minute)
	r.sweep(t)
	if r.count(true) != 0 {
		t.Fatalf("reminded too early: %+v", r.out.got)
	}
	r.clk.advance(2 * time.Minute)
	r.sweep(t)
	r.clk.advance(time.Hour)
	r.reopen(t)
	r.sweep(t)
	r.sweep(t)
	if r.count(false) != 1 || r.count(true) != 1 {
		t.Fatalf("want one notice and one reminder, got %+v", r.out.got)
	}
}

func TestReminderDelayLoweredRemindsOnce(t *testing.T) {
	r := newRig(t)
	r.src.set(sub("session:s1"))
	r.sweep(t)
	r.remind = time.Minute
	r.clk.advance(2 * time.Minute)
	r.sweep(t)
	r.sweep(t)
	r.clk.advance(10 * time.Minute)
	r.sweep(t)
	if r.count(true) != 1 {
		t.Fatalf("reminders = %d, want 1: %+v", r.count(true), r.out.got)
	}
}

func TestReminderZeroNeverReminds(t *testing.T) {
	r := newRig(t)
	r.remind = 0
	r.src.set(sub("session:s1"))
	r.sweep(t)
	r.clk.advance(48 * time.Hour)
	r.sweep(t)
	if r.count(true) != 0 || r.count(false) != 1 {
		t.Fatalf("sent = %+v", r.out.got)
	}
}

func TestLeavingClearsAndReentryIsANewStay(t *testing.T) {
	r := newRig(t)
	r.src.set(sub("session:s1"))
	r.sweep(t)
	r.src.set() // answered
	r.sweep(t)
	if open, _ := r.st.ListOpenWaitingNotices(); len(open) != 0 {
		t.Fatalf("a subject that left is still open: %+v", open)
	}
	r.src.set(sub("session:s1")) // a later turn waits again
	r.sweep(t)
	if r.count(false) != 2 {
		t.Fatalf("re-entry must notify again: %+v", r.out.got)
	}
}

type failingSource struct{}

func (failingSource) Waiting() ([]Subject, error) { return nil, errTest }

type testErr struct{}

func (testErr) Error() string { return "boom" }

var errTest = testErr{}

func TestUnreadableListClearsNothing(t *testing.T) {
	r := newRig(t)
	r.src.set(sub("session:s1"))
	r.sweep(t)
	r.sc.Source = failingSource{}
	if err := r.sc.Sweep(); err == nil {
		t.Fatal("a failing source must surface")
	}
	if open, _ := r.st.ListOpenWaitingNotices(); len(open) != 1 {
		t.Fatalf("a failed read must not clear rows: %+v", open)
	}
}
