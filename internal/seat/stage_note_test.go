package seat

import (
	"context"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// stageFlow starts a small flow (doing > verify > done) on a new work item.
func stageFlow(t *testing.T, e *env) string {
	t.Helper()
	it, err := e.st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", By: "human:a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.StartWorkStage(jobstore.WorkStage{WorkItemID: it.ID, Template: "small", Steps: []string{"doing", "verify", "done"}}, "human:a"); err != nil {
		t.Fatal(err)
	}
	return it.ID
}

func TestStageNoteSessionOnNoSeatIsNotHandled(t *testing.T) {
	e, _, _ := scheduleEnv(t)
	e.session(t, "s9", "/d/work/proj")
	_, handled, err := e.svc.DeliverStageNote(context.Background(), "w-1", "s9", "doing", "x")
	if err != nil || handled {
		t.Fatalf("handled=%v err=%v, want not handled", handled, err)
	}
}

func TestStageNoteOnlineSeatIsDeliveredNotQueued(t *testing.T) {
	e, m, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	e.session(t, "s1", "/d/work/proj")
	if _, err := e.svc.Bind(x.ID, "s1", false, "user"); err != nil {
		t.Fatal(err)
	}
	wid := stageFlow(t, e)
	out, handled, err := e.svc.DeliverStageNote(context.Background(), wid, "s1", "doing", "[gofer · stage] go on")
	if err != nil || !handled || out != StageNoteDelivered {
		t.Fatalf("out=%q handled=%v err=%v", out, handled, err)
	}
	if len(m.sent) != 1 || m.sent[0].sid != "s1" || m.sent[0].text != "[gofer · stage] go on" {
		t.Fatalf("sent = %+v", m.sent)
	}
	if rows, _ := e.st.ListSeatPending(x.ID); len(rows) != 0 {
		t.Fatalf("queue = %+v", rows)
	}
}

func TestStageNoteOfflineSeatQueuesLatestThenFlushesOnce(t *testing.T) {
	e, m, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	e.session(t, "s1", "/d/work/proj")
	if _, err := e.svc.Bind(x.ID, "s1", false, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SetSessionState("s1", jobstore.SessionOffline); err != nil {
		t.Fatal(err)
	}
	wid := stageFlow(t, e)
	for _, txt := range []string{"first", "second"} {
		out, handled, err := e.svc.DeliverStageNote(context.Background(), wid, "s1", "doing", txt)
		if err != nil || !handled || out != StageNoteQueued {
			t.Fatalf("out=%q handled=%v err=%v", out, handled, err)
		}
	}
	rows, err := e.st.ListSeatPending(x.ID)
	if err != nil || len(rows) != 1 || rows[0].Text != "second" || rows[0].ScheduleID != jobstore.StagePendingID(wid) {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	// Still offline: nothing goes out.
	if n, err := e.svc.FlushSeatOf(context.Background(), "s1"); err != nil || n != 0 || len(m.sent) != 0 {
		t.Fatalf("offline flush = %d, %v, sent %+v", n, err, m.sent)
	}
	if _, err := e.st.SetSessionState("s1", jobstore.SessionRunning); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := e.svc.FlushSeatOf(context.Background(), "s1"); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.sent) != 1 || m.sent[0].text != "second" {
		t.Fatalf("sent = %+v, want the latest text exactly once", m.sent)
	}
	if rows, _ := e.st.ListSeatPending(x.ID); len(rows) != 0 {
		t.Fatalf("queue after the flush = %+v", rows)
	}
}

// A reminder queued for a step the flow has since left is dropped, not sent late.
func TestStageNoteQueuedForAPassedStepIsDropped(t *testing.T) {
	e, m, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	e.session(t, "s1", "/d/work/proj")
	if _, err := e.svc.Bind(x.ID, "s1", false, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SetSessionState("s1", jobstore.SessionOffline); err != nil {
		t.Fatal(err)
	}
	wid := stageFlow(t, e)
	if _, _, err := e.svc.DeliverStageNote(context.Background(), wid, "s1", "doing", "go on"); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.st.TransitionWorkStage(jobstore.StageTransition{WorkItemID: wid, FromStep: 0, ToStep: 1, ToStatus: jobstore.StageActive, Journal: "moved"}); err != nil || !ok {
		t.Fatalf("transition = %v %v", ok, err)
	}
	if _, err := e.st.SetSessionState("s1", jobstore.SessionRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.FlushSeatOf(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if len(m.sent) != 0 {
		t.Fatalf("a stale reminder was sent: %+v", m.sent)
	}
	if rows, _ := e.st.ListSeatPending(x.ID); len(rows) != 0 {
		t.Fatalf("queue = %+v, want the stale row dropped", rows)
	}
}
