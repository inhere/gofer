package steward

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func (e *env) offlineSession(t *testing.T, w jobstore.WorkItem, sid string, seenAt int64) {
	t.Helper()
	_, err := e.st.UpsertAgentSession(jobstore.AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "p", Cwd: "/ws"})
	assert.NoErr(t, err)
	_, _, err = e.st.TouchAgentSession(sid, jobstore.SessionHeartbeat{State: jobstore.SessionOffline})
	assert.NoErr(t, err)
	_ = seenAt
	assert.NoErr(t, e.st.AttachWorkSession(w.ID, sid, "human:a"))
}

// By default events are only noted (the next review handles them); no session starts.
func TestEventsAreOnlyNotedByDefault(t *testing.T) {
	e := newEnv(t)
	w := e.item(t, "现场调试")
	e.offlineSession(t, w, "sess-evt00001", 100)
	e.svc.Tick(context.Background(), time.Date(2026, 10, 6, 6, 0, 0, 0, time.Local))
	ev, _ := e.st.PendingStewardEvents(10)
	assert.Eq(t, 1, len(ev))
	assert.Eq(t, jobstore.StewardEventSession, ev[0].Kind)
	assert.True(t, strings.Contains(ev[0].Detail, "现场调试") && strings.Contains(ev[0].Detail, "离线"))
	assert.Eq(t, 0, len(e.host.started))
	// The same occurrence is noted once however many ticks see it.
	e.svc.Tick(context.Background(), time.Date(2026, 10, 6, 6, 1, 0, 0, time.Local))
	ev, _ = e.st.PendingStewardEvents(10)
	assert.Eq(t, 1, len(ev))
}

func TestEventWakeIsBatchedAndThrottled(t *testing.T) {
	e := newEnv(t)
	e.cfg.EventWake = true
	t0 := time.Date(2026, 10, 6, 6, 0, 0, 0, time.Local)
	w1 := e.item(t, "事一")
	e.offlineSession(t, w1, "sess-wake0001", 100)
	e.svc.Tick(context.Background(), t0)
	e.svc.WaitIdle()
	assert.Eq(t, 1, len(e.host.started))
	assert.True(t, strings.Contains(e.host.started[0].Prompt, "事件整理"))
	pend, _ := e.st.PendingStewardEvents(10)
	assert.Eq(t, 0, len(pend)) // handed to the steward

	// A new event 10 minutes later is only noted: still inside the 30-minute window.
	w2 := e.item(t, "事二")
	e.offlineSession(t, w2, "sess-wake0002", 200)
	e.svc.Tick(context.Background(), t0.Add(10*time.Minute))
	e.svc.WaitIdle()
	pend, _ = e.st.PendingStewardEvents(10)
	assert.Eq(t, 1, len(pend))
	assert.Eq(t, 0, len(e.host.said(e.svc.kv(kvJobID))))

	// After the window it wakes the (still live) steward once with the whole batch.
	e.svc.Tick(context.Background(), t0.Add(31*time.Minute))
	e.svc.WaitIdle()
	pend, _ = e.st.PendingStewardEvents(10)
	assert.Eq(t, 0, len(pend))
	said := e.host.said(e.svc.kv(kvJobID))
	assert.Eq(t, 1, len(said))
	assert.True(t, strings.Contains(said[0], "事二") && !strings.Contains(strings.SplitN(said[0], "待处理的事件", 2)[0], "事一"))
}

func TestDraftPileNotesOneEventPerDay(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		w := e.item(t, "草稿")
		unsorted := true
		_, _, err := e.st.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{Unsorted: &unsorted}, 0, "system")
		assert.NoErr(t, err)
	}
	now := time.Date(2026, 10, 6, 6, 0, 0, 0, time.Local)
	e.svc.Tick(context.Background(), now)
	e.svc.Tick(context.Background(), now.Add(time.Minute))
	ev, _ := e.st.PendingStewardEvents(10)
	assert.Eq(t, 1, len(ev))
	assert.Eq(t, jobstore.StewardEventDrafts, ev[0].Kind)
	assert.True(t, strings.Contains(ev[0].Detail, "5 个"))
}

func TestDueHookNotesAnEventOnlyWhenEnabled(t *testing.T) {
	e := newEnv(t)
	w := e.item(t, "搁置到期")
	e.svc.NoteDue(w, "搁置到期", 500)
	e.svc.NoteDue(w, "搁置到期", 500)
	ev, _ := e.st.PendingStewardEvents(10)
	assert.Eq(t, 1, len(ev))
	assert.Eq(t, jobstore.StewardEventDue, ev[0].Kind)
	assert.Eq(t, w.ID, eventItemID(ev[0]))

	e.cfg.Enabled = false
	e.svc.NoteDue(w, "x", 900)
	ev, _ = e.st.PendingStewardEvents(10)
	assert.Eq(t, 1, len(ev))
}

func TestDailyReviewRunsOncePerDayAfterReviewTime(t *testing.T) {
	e := newEnv(t)
	e.item(t, "每日要看的事")
	day := time.Date(2026, 10, 6, 0, 0, 0, 0, time.Local)

	e.svc.Tick(context.Background(), day.Add(8*time.Hour+40*time.Minute)) // 08:40: before 08:50
	e.svc.WaitIdle()
	assert.Eq(t, 0, len(e.host.started))

	e.svc.Tick(context.Background(), day.Add(8*time.Hour+51*time.Minute)) // 08:51
	e.svc.WaitIdle()
	assert.Eq(t, 1, len(e.host.started))
	assert.True(t, strings.Contains(e.host.started[0].Prompt, "每日巡检"))
	rv, _ := e.st.ListStewardReviews(5)
	assert.Eq(t, TriggerDaily, rv[0].Trigger)

	e.item(t, "又来一件") // changed again, but today's review already ran
	e.svc.Tick(context.Background(), day.Add(9*time.Hour))
	e.svc.WaitIdle()
	rv, _ = e.st.ListStewardReviews(5)
	assert.Eq(t, 1, len(rv))

	// Next day it runs again; a server that comes up 7 hours late does not run a stale one.
	e.svc.Tick(context.Background(), day.AddDate(0, 0, 1).Add(15*time.Hour))
	e.svc.WaitIdle()
	rv, _ = e.st.ListStewardReviews(5)
	assert.Eq(t, 1, len(rv))
}

func TestTickDoesNothingWhileDisabled(t *testing.T) {
	e := newEnv(t)
	e.cfg.Enabled = false
	w := e.item(t, "x")
	e.offlineSession(t, w, "sess-off00001", 100)
	e.svc.Tick(context.Background(), time.Date(2026, 10, 6, 9, 0, 0, 0, time.Local))
	ev, _ := e.st.PendingStewardEvents(10)
	assert.Eq(t, 0, len(ev))
	assert.Eq(t, 0, len(e.host.started))
}
