package seat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
	"github.com/inhere/gofer/internal/testutil/wait"
)

func queuedCount(t *testing.T, e *env, scheduleID string) int {
	t.Helper()
	n, err := e.st.SeatPendingCount(scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// An offline run is queued, then goes out exactly once when the session is back: through the
// sweep, with the late note, and the schedule reads delivered afterwards.
func TestPendingOfflineQueuesThenFlushesOnce(t *testing.T) {
	e, m, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	if err := e.st.InsertSchedule(jobstore.ScheduleRecord{ID: "sch-1", Name: "weekly", Enabled: 1, Target: jobstore.ScheduleTargetSeat(x.ID), LastResult: jobstore.ScheduleResultOffline, RequestJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	e.session(t, "s1", "/d/work/proj")
	if _, err := e.svc.Bind(x.ID, "s1", false, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SetSessionState("s1", jobstore.SessionOffline); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID))
	if err != nil || res.Outcome != jobstore.ScheduleResultOffline {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if queuedCount(t, e, "sch-1") != 1 || len(m.sent) != 0 {
		t.Fatalf("queued=%d sent=%+v", queuedCount(t, e, "sch-1"), m.sent)
	}
	// Still offline: the sweep sends nothing and keeps the row.
	if n, err := e.svc.FlushPending(context.Background()); err != nil || n != 0 || queuedCount(t, e, "sch-1") != 1 {
		t.Fatalf("offline flush = %d, %v", n, err)
	}
	if _, err := e.st.SetSessionState("s1", jobstore.SessionRunning); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := e.svc.FlushPending(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.sent) != 1 || m.sent[0].sid != "s1" {
		t.Fatalf("sent = %+v, want exactly one", m.sent)
	}
	body := m.sent[0].text
	if !strings.HasPrefix(body, "[gofer · ") || msgprefix.IsPerson(body) || !strings.Contains(body, "weekly report please") || !strings.Contains(body, "1") {
		t.Fatalf("late text = %q", body)
	}
	if queuedCount(t, e, "sch-1") != 0 {
		t.Fatal("a delivered row must leave the queue")
	}
	if rec, ok, err := e.st.GetSchedule("sch-1"); err != nil || !ok || rec.LastResult != jobstore.ScheduleResultDelivered {
		t.Fatalf("schedule after the late send = %+v, %v", rec, err)
	}
}

// Runs of one schedule pile into one row: the latest text, a growing count.
func TestPendingSameScheduleKeepsOneRow(t *testing.T) {
	e, m, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	for i, txt := range []string{"first", "second", "third"} {
		req := deliverReq(x.ID)
		req.Text = txt
		if _, err := e.svc.DeliverToSeat(context.Background(), req); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	rows, err := e.st.ListSeatPending(x.ID)
	if err != nil || len(rows) != 1 || rows[0].Count != 3 || rows[0].Text != "third" {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	e.session(t, "s1", "/d/work/proj")
	if _, err := e.svc.Bind(x.ID, "s1", false, "user"); err != nil {
		t.Fatal(err)
	}
	// Bind flushes at once.
	if len(m.sent) != 1 || !strings.Contains(m.sent[0].text, "third") || !strings.Contains(m.sent[0].text, "3") {
		t.Fatalf("sent = %+v", m.sent)
	}
}

// FlushSeatOf is the immediate late send of a session that just came back: it finds the seat
// through the session, sends the queued text once (a second report does nothing), and does
// nothing at all for a session in no seat.
func TestFlushSeatOfSendsTheQueueOfItsSeat(t *testing.T) {
	e, m, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	e.session(t, "s1", "/d/work/proj")
	if _, err := e.svc.Bind(x.ID, "s1", false, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SetSessionState("s1", jobstore.SessionOffline); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID)); err != nil {
		t.Fatal(err)
	}
	if queuedCount(t, e, "sch-1") != 1 || len(m.sent) != 0 {
		t.Fatalf("queued=%d sent=%+v", queuedCount(t, e, "sch-1"), m.sent)
	}
	// Still offline: the came-back report sends nothing and keeps the row.
	if n, err := e.svc.FlushSeatOf(context.Background(), "s1"); err != nil || n != 0 || queuedCount(t, e, "sch-1") != 1 {
		t.Fatalf("offline flush = %d, %v (queued %d)", n, err, queuedCount(t, e, "sch-1"))
	}
	// Back: one more report sends it, and a further one sends nothing.
	if _, err := e.st.SetSessionState("s1", jobstore.SessionRunning); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.FlushSeatOf(context.Background(), "s1"); err != nil || n != 1 {
		t.Fatalf("flush after the comeback = %d, %v", n, err)
	}
	if n, err := e.svc.FlushSeatOf(context.Background(), "s1"); err != nil || n != 0 {
		t.Fatalf("second flush = %d, %v", n, err)
	}
	if len(m.sent) != 1 || m.sent[0].sid != "s1" || !strings.Contains(m.sent[0].text, "weekly report please") {
		t.Fatalf("sent = %+v, want exactly one late send", m.sent)
	}
	if queuedCount(t, e, "sch-1") != 0 {
		t.Fatal("a delivered row must leave the queue")
	}
	// A session that sits in no seat is a no-op, not an error.
	e.session(t, "s-free", "/d/work/proj")
	if n, err := e.svc.FlushSeatOf(context.Background(), "s-free"); err != nil || n != 0 {
		t.Fatalf("flush of a session in no seat = %d, %v", n, err)
	}
}

// A text waiting longer than the TTL is dropped, not sent late.
func TestPendingExpiresAfterTTL(t *testing.T) {
	e, m, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	if _, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID)); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(jobstore.SeatPendingTTL + time.Minute)
	e.session(t, "s1", "/d/work/proj")
	if _, err := e.st.BindSeat(x.ID, "s1", false, "user"); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.FlushPending(context.Background()); err != nil || n != 0 {
		t.Fatalf("flush = %d, %v", n, err)
	}
	if len(m.sent) != 0 || queuedCount(t, e, "sch-1") != 0 {
		t.Fatalf("sent=%+v queued=%d", m.sent, queuedCount(t, e, "sch-1"))
	}
}

// Archiving the seat drops what waits for it.
func TestPendingDroppedOnArchive(t *testing.T) {
	e, _, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	if _, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Archive(x.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if queuedCount(t, e, "sch-1") != 0 {
		t.Fatal("archive must drop the queue")
	}
}

// Deleting the schedule drops its queued text.
func TestPendingDroppedWithSchedule(t *testing.T) {
	e, _, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	if _, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteSchedule("sch-1"); err != nil {
		t.Fatal(err)
	}
	if queuedCount(t, e, "sch-1") != 0 {
		t.Fatal("deleting the schedule must drop its queue")
	}
}

// An ACP seat with no job queues the text; binding a job sends it through the same say path.
func TestPendingACPVacantThenBind(t *testing.T) {
	e, _, sy := scheduleEnv(t)
	x, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "weekly", Kind: jobstore.SeatKindACP, Agent: "acp"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID))
	if err != nil || res.Outcome != jobstore.ScheduleResultOffline || queuedCount(t, e, "sch-1") != 1 {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if err := e.st.UpsertJob(jobstore.JobRecord{ID: "job-live", ProjectKey: "proj", Agent: "acp", Runner: "local", Status: job.StatusRunning, Cwd: ".", ResultDir: "/r", RequestJSON: "{}", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Bind(x.ID, "job-live", false, "user"); err != nil {
		t.Fatal(err)
	}
	// A live ACP job gets the seat's charter at bind time first, then the queued text.
	if len(sy.said) != 2 || sy.said[1][0] != "job-live" || !strings.Contains(sy.said[1][1], "weekly report please") {
		t.Fatalf("said = %+v", sy.said)
	}
	if queuedCount(t, e, "sch-1") != 0 {
		t.Fatal("the queue must be empty after the late send")
	}
}

// Two sweeps racing for one row send it once.
func TestPendingConcurrentFlushSendsOnce(t *testing.T) {
	e, m, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	if _, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID)); err != nil {
		t.Fatal(err)
	}
	e.session(t, "s1", "/d/work/proj")
	if _, err := e.st.BindSeat(x.ID, "s1", false, "user"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, _ = e.svc.FlushPending(context.Background())
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	wait.For(t, 10*time.Second, "the queue empties", func() (bool, any) { return queuedCount(t, e, "sch-1") == 0, nil })
	if len(m.sent) != 1 {
		t.Fatalf("sent %d times, want once", len(m.sent))
	}
}
