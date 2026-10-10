package work

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/wait"
)

type sent struct{ event, project, title, text, link string }

type fakeNotifier struct {
	got []sent
	// nobody makes NotifyWork report 0 matched webhooks (OBS-13).
	nobody bool
}

func (f *fakeNotifier) NotifyWork(event, project, title, text, link, _ string) int {
	f.got = append(f.got, sent{event, project, title, text, link})
	if f.nobody {
		return 0
	}
	return 1
}
func (f *fakeNotifier) WebURL(path string) string { return "http://gofer.test" + path }

type fakeProbe map[string]JobState

func (f fakeProbe) JobStates(ids []string) map[string]JobState {
	out := map[string]JobState{}
	for _, id := range ids {
		if st, ok := f[id]; ok {
			out[id] = st
		}
	}
	return out
}

func newSvc(t *testing.T) (*Service, *jobstore.Store, *fakeNotifier) {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "w.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	svc := New(st)
	// LIFO: runs before the store closes, so no background task (Park's auto hand-over)
	// outlives the DB (gofer-r7am).
	t.Cleanup(func() { assert.NoErr(t, svc.Close()) })
	n := &fakeNotifier{}
	svc.SetNotifier(n)
	return svc, st, n
}

func session(t *testing.T, st *jobstore.Store, sid, state string) jobstore.AgentSession {
	t.Helper()
	a, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "p1", Cwd: "/ws", State: state})
	assert.NoErr(t, err)
	return a
}

func TestFirstHumanPromptCreatesOneDraftAndMapsStatus(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-aaaa1111", jobstore.SessionRunning)
	svc.OnHumanPrompt(a, "给订单页加导出按钮\n细节...")
	svc.OnHumanPrompt(a, "再问点别的")
	items, err := svc.List(jobstore.WorkListOpts{})
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(items))
	it := items[0]
	assert.Eq(t, "给订单页加导出按钮", it.Title)
	assert.True(t, it.Unsorted)
	assert.Eq(t, jobstore.WorkActive, it.Status)
	assert.Eq(t, []string{"sess-aaaa1111"}, it.SessionIDs)
	assert.Eq(t, "claude", it.Sessions[0].Agent)

	// waiting_reply -> needs_me, running again -> active.
	_, _, err = st.TouchAgentSession("sess-aaaa1111", jobstore.SessionHeartbeat{State: jobstore.SessionWaitingReply})
	assert.NoErr(t, err)
	svc.SyncAll()
	got, _, _ := st.GetWorkItem(it.ID)
	assert.Eq(t, jobstore.WorkNeedsMe, got.Status)
	_, _, _ = st.TouchAgentSession("sess-aaaa1111", jobstore.SessionHeartbeat{State: jobstore.SessionRunning})
	svc.SyncAll()
	got, _, _ = st.GetWorkItem(it.ID)
	assert.Eq(t, jobstore.WorkActive, got.Status)

	// Offline / ended never change the status, the view only labels it.
	_, _, _ = st.TouchAgentSession("sess-aaaa1111", jobstore.SessionHeartbeat{State: jobstore.SessionEnded})
	svc.SyncAll()
	got, _, _ = st.GetWorkItem(it.ID)
	assert.Eq(t, jobstore.WorkActive, got.Status)
	v, _ := svc.Detail(it.ID, 0)
	assert.True(t, v.SessionOffline)
}

func TestHumanStatusWinsUntilReportUnblocks(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-bbbb2222", jobstore.SessionRunning)
	svc.OnHumanPrompt(a, "ask")
	items, _ := svc.List(jobstore.WorkListOpts{})
	id := items[0].ID

	onsite := jobstore.WorkNeedsOnsite
	_, err := svc.Update(id, jobstore.WorkItemPatch{Status: &onsite}, 0, "human:me")
	assert.NoErr(t, err)
	svc.SyncAll()
	got, _, _ := st.GetWorkItem(id)
	assert.Eq(t, onsite, got.Status) // running session does not override

	// A report asking for another blocked status is refused under the human lock.
	_, err = svc.Report(id, ReportInput{Status: jobstore.WorkWaitingResource, By: "session:x"})
	assert.NoErr(t, err)
	got, _, _ = st.GetWorkItem(id)
	assert.Eq(t, onsite, got.Status)

	// done/dropped are never taken from a report.
	_, err = svc.Report(id, ReportInput{Status: jobstore.WorkDone, By: "session:x"})
	assert.NoErr(t, err)
	got, _, _ = st.GetWorkItem(id)
	assert.Eq(t, onsite, got.Status)

	// An explicit "active" releases the lock; the mapping owns the status again.
	_, err = svc.Report(id, ReportInput{Status: jobstore.WorkActive, Next: "继续验证", By: "session:x"})
	assert.NoErr(t, err)
	got, _, _ = st.GetWorkItem(id)
	assert.Eq(t, jobstore.WorkActive, got.Status)
	assert.Eq(t, jobstore.WorkSourceAuto, got.StatusSource)
	assert.Eq(t, "继续验证", got.NextStep)
	_, _, _ = st.TouchAgentSession("sess-bbbb2222", jobstore.SessionHeartbeat{State: jobstore.SessionWaitingReply})
	svc.SyncAll()
	got, _, _ = st.GetWorkItem(id)
	assert.Eq(t, jobstore.WorkNeedsMe, got.Status)

	// Report lines are journaled as `report`, one per report.
	d, _ := svc.Detail(id, 0)
	reports := 0
	for _, e := range d.Journal {
		if e.Kind == jobstore.WorkJournalReport {
			reports++
			assert.Eq(t, "session:x", e.By)
		}
	}
	assert.Eq(t, 3, reports)
}

func TestReportFillsGoalAndSortsDraftAndRejectsEmpty(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-cccc3333", jobstore.SessionRunning)
	svc.OnHumanPrompt(a, "ask")
	items, _ := svc.List(jobstore.WorkListOpts{})
	id := items[0].ID
	assert.True(t, items[0].Unsorted)

	_, err := svc.Report(id, ReportInput{})
	assert.True(t, errors.Is(err, ErrEmptyReport))
	_, err = svc.Report(id, ReportInput{Status: "bogus"})
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))
	_, err = svc.Report("w-nope", ReportInput{Goal: "x"})
	assert.True(t, errors.Is(err, jobstore.ErrWorkItemNotFound))

	w, err := svc.Report(id, ReportInput{Goal: "导出订单为 xlsx", Blocker: "缺测试账号", By: "session:c"})
	assert.NoErr(t, err)
	assert.False(t, w.Unsorted)
	assert.Eq(t, "导出订单为 xlsx", w.Goal)
	assert.Eq(t, "缺测试账号", w.BlockerText)

	// Setting a draft's goal through Update also marks it sorted.
	b := session(t, st, "sess-dddd4444", jobstore.SessionRunning)
	svc.OnHumanPrompt(b, "other")
	list, _ := svc.List(jobstore.WorkListOpts{SessionID: "sess-dddd4444"})
	goal := "g"
	u, err := svc.Update(list[0].ID, jobstore.WorkItemPatch{Goal: &goal}, 0, "human:a")
	assert.NoErr(t, err)
	assert.False(t, u.Unsorted)
}

func TestLinkedJobsDriveReviewAndNeedsMe(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-eeee5555", jobstore.SessionIdle)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", SessionIDs: []string{a.SessionID}, Source: jobstore.WorkOriginAuto})
	assert.NoErr(t, err)
	_, err = st.AddWorkLink(w.ID, jobstore.WorkLinkJob, "job-1", "h")
	assert.NoErr(t, err)
	probe := fakeProbe{"job-1": {Status: "needs_review"}}
	svc.SetJobProbe(probe)
	svc.SyncAll()
	got, _, _ := st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkReview, got.Status)

	probe["job-1"] = JobState{Status: "running", PendingInteraction: true}
	svc.SyncAll()
	got, _, _ = st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkNeedsMe, got.Status)

	// A session watch is picked up the same way.
	_, err = st.AddSessionJobWatch(a.SessionID, "job-2")
	assert.NoErr(t, err)
	delete(probe, "job-1")
	probe["job-2"] = JobState{Status: "needs_review"}
	svc.SyncAll()
	got, _, _ = st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkReview, got.Status)
}

func TestTickFiresReminderOnceAndParkedDue(t *testing.T) {
	svc, st, n := newSvc(t)
	now := time.Unix(1_800_000_000, 0)
	svc.SetNow(func() time.Time { return now })
	w, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "回访客户", ProjectKey: "p1", Goal: "确认报价"})
	p, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "等设备到货"})
	remind := now.Unix() - 5
	_, _, _ = st.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{RemindAt: &remind}, 0, "h")
	_, err := svc.Park(p.ID, now.Unix()-1, "到货后继续", "human:a")
	assert.NoErr(t, err)

	svc.Tick(now)
	assert.Eq(t, 2, len(n.got))
	byTitle := map[string]sent{}
	for _, s := range n.got {
		assert.Eq(t, "work.remind", s.event)
		byTitle[s.title] = s
	}
	r := byTitle["工作项提醒 · 回访客户"]
	assert.Eq(t, "p1", r.project)
	assert.True(t, strings.Contains(r.text, "确认报价"))
	assert.Eq(t, "http://gofer.test/work?id="+w.ID, r.link)
	pk := byTitle["工作项提醒 · 等设备到货"]
	assert.True(t, strings.Contains(pk.text, "搁置到期"))
	assert.True(t, strings.Contains(pk.text, "到货后继续"))

	svc.Tick(now.Add(time.Minute)) // nothing new
	assert.Eq(t, 2, len(n.got))
	// ...but it stays in the "due" view until the person acts.
	list, _ := svc.List(jobstore.WorkListOpts{Due: true})
	assert.Eq(t, 2, len(list))
	for _, v := range list {
		assert.True(t, v.Due)
	}
	sum, _ := svc.Summarize()
	assert.Eq(t, 2, sum.Due)

	// Back to active clears the park deadline's relevance.
	act := jobstore.WorkActive
	_, _, _ = st.UpdateWorkItem(p.ID, jobstore.WorkItemPatch{Status: &act}, 0, "human:a")
	list, _ = svc.List(jobstore.WorkListOpts{Due: true})
	assert.Eq(t, 1, len(list))
}

func TestDigestContentIsDeterministicAndSentOncePerDay(t *testing.T) {
	svc, st, n := newSvc(t)
	loc := time.FixedZone("x", 8*3600)
	now := time.Date(2026, 10, 5, 9, 10, 0, 0, loc)
	svc.SetNow(func() time.Time { return now })

	mk := func(title, status string) jobstore.WorkItem {
		w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: title})
		assert.NoErr(t, err)
		s := status
		src := jobstore.WorkSourceHuman
		w, _, err = st.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{Status: &s, StatusSource: &src}, 0, "h")
		assert.NoErr(t, err)
		return w
	}
	st.SetClock(func() time.Time { return now.Add(-8 * 24 * time.Hour) })
	old := mk("老搁置", jobstore.WorkParked)
	st.SetClock(func() time.Time { return time.Date(2026, 10, 4, 15, 0, 0, 0, loc) })
	yw := mk("昨天动过", jobstore.WorkActive)
	st.SetClock(func() time.Time { return now })
	mk("等我1", jobstore.WorkNeedsMe)
	mk("等我2", jobstore.WorkNeedsMe)
	mk("等资源1", jobstore.WorkWaitingResource)
	mk("现场1", jobstore.WorkNeedsOnsite)

	d1, err := svc.BuildDigest(now)
	assert.NoErr(t, err)
	d2, _ := svc.BuildDigest(now)
	assert.Eq(t, d1.Text, d2.Text) // deterministic
	assert.Eq(t, 2, d1.NeedsMe)
	assert.Eq(t, 1, d1.Waiting)
	assert.Eq(t, 1, d1.Onsite)
	assert.Eq(t, 1, d1.ParkedOver)
	assert.Eq(t, 1, d1.Yesterday)
	assert.True(t, strings.HasPrefix(d1.Text, "等我 2 · 等资源 1 · 需现场 1 · 待验收 0"))
	assert.True(t, strings.Contains(d1.Text, "搁置超过 7 天（1）："))
	assert.True(t, strings.Contains(d1.Text, "[老搁置](http://gofer.test/work?id="+old.ID+")"))
	assert.True(t, strings.Contains(d1.Text, "[昨天动过](http://gofer.test/work?id="+yw.ID+")（进行中）"))

	// Before the digest time: nothing. In the window: one send; again: nothing.
	svc.Tick(time.Date(2026, 10, 5, 8, 59, 0, 0, loc))
	assert.Eq(t, 0, countEvent(n, "work.digest"))
	svc.Tick(now)
	assert.Eq(t, 1, countEvent(n, "work.digest"))
	svc.Tick(now.Add(time.Hour))
	assert.Eq(t, 1, countEvent(n, "work.digest"))
	// Next day sends again.
	svc.Tick(now.Add(24 * time.Hour))
	assert.Eq(t, 2, countEvent(n, "work.digest"))
	// Too late in the day (past the 6h window) never sends a stale digest.
	svc.Tick(time.Date(2026, 10, 7, 16, 0, 0, 0, loc))
	assert.Eq(t, 2, countEvent(n, "work.digest"))
}

func TestDigestDisabledAndCustomTime(t *testing.T) {
	svc, _, n := newSvc(t)
	loc := time.FixedZone("x", 0)
	off := false
	cfg := config.WorkConfig{DigestEnabled: &off}
	svc.SetConfigFn(func() config.WorkConfig { return cfg })
	svc.Tick(time.Date(2026, 10, 5, 9, 30, 0, 0, loc))
	assert.Eq(t, 0, countEvent(n, "work.digest"))

	cfg = config.WorkConfig{DigestTime: "18:30"}
	svc.Tick(time.Date(2026, 10, 5, 9, 30, 0, 0, loc))
	assert.Eq(t, 0, countEvent(n, "work.digest"))
	svc.Tick(time.Date(2026, 10, 5, 18, 31, 0, 0, loc))
	assert.Eq(t, 1, countEvent(n, "work.digest"))

	// A malformed time falls back to 09:00 rather than silently disabling the digest.
	h, m := config.WorkConfig{DigestTime: "soon"}.DigestClock()
	assert.Eq(t, 9, h)
	assert.Eq(t, 0, m)
}

func TestManualDigestSendCountsWebhooks(t *testing.T) {
	svc, _, n := newSvc(t)
	d, sentTo, err := svc.SendDigest(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	assert.NoErr(t, err)
	assert.Eq(t, 1, sentTo)
	assert.True(t, strings.HasPrefix(d.Title, "工作摘要 · 2026-10-05"))
	assert.Eq(t, 1, countEvent(n, "work.digest"))
	assert.True(t, strings.Contains(d.Text, "今天没有需要关注的工作项。"))
}

func countEvent(n *fakeNotifier, ev string) int {
	c := 0
	for _, s := range n.got {
		if s.event == ev {
			c++
		}
	}
	return c
}

// The hook heartbeat is NOT announced by the store (too chatty), so the relay tells the
// service about it: a Stop beat that starts waiting for the person must move the item
// to "needs me" within moments, without waiting for the 30s sweep.
func TestSessionBeatMovesTheItemWithoutTheSweep(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-beat-0001", jobstore.SessionRunning)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", Source: jobstore.WorkOriginAuto, SessionIDs: []string{a.SessionID}})
	assert.NoErr(t, err)
	// an unrelated session's beat must not touch this item
	other := session(t, st, "sess-other-0002", jobstore.SessionRunning)

	stop := make(chan struct{})
	defer close(stop)
	go svc.Run(stop)

	waitStatus := func(want string) {
		t.Helper()
		wait.For(t, 3*time.Second, "work item status "+want, func() (bool, any) {
			got, _, _ := st.GetWorkItem(w.ID)
			return got.Status == want, got.Status
		})
	}

	b, ok, err := st.TouchAgentSession(a.SessionID, jobstore.SessionHeartbeat{Event: "Stop", State: jobstore.SessionWaitingReply})
	assert.NoErr(t, err)
	assert.True(t, ok)
	svc.OnSessionBeat(b)
	waitStatus(jobstore.WorkNeedsMe)

	// the person answers: the next prompt beat flips it back
	b, _, _ = st.TouchAgentSession(a.SessionID, jobstore.SessionHeartbeat{Event: "UserPromptSubmit", State: jobstore.SessionRunning})
	svc.OnSessionBeat(b)
	waitStatus(jobstore.WorkActive)

	// a beat of another session leaves it alone
	o, _, _ := st.TouchAgentSession(other.SessionID, jobstore.SessionHeartbeat{Event: "Stop", State: jobstore.SessionWaitingReply})
	svc.OnSessionBeat(o)
	time.Sleep(600 * time.Millisecond)
	got, _, _ := st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkActive, got.Status)
}

// OBS-13: a digest that reached no webhook must not burn the day: once a subscription
// exists, the same day still gets its summary (retries are spaced, not per tick).
func TestDigestWithoutSubscriberDoesNotMarkDay(t *testing.T) {
	svc, st, n := newSvc(t)
	loc := time.FixedZone("x", 0)
	now := time.Date(2026, 10, 5, 9, 10, 0, 0, loc)
	svc.SetNow(func() time.Time { return now })
	n.nobody = true

	svc.Tick(now)
	assert.Eq(t, 1, countEvent(n, "work.digest"))
	last, _ := st.GetWorkKV(digestKVKey)
	assert.Eq(t, "", last)
	// Within the retry spacing nothing is re-attempted.
	svc.Tick(now.Add(time.Minute))
	assert.Eq(t, 1, countEvent(n, "work.digest"))

	// A subscriber appears: the next attempt after the spacing delivers and marks the day.
	n.nobody = false
	svc.Tick(now.Add(digestRetryEvery))
	assert.Eq(t, 2, countEvent(n, "work.digest"))
	last, _ = st.GetWorkKV(digestKVKey)
	assert.Eq(t, "2026-10-05", last)
	svc.Tick(now.Add(digestRetryEvery + time.Hour))
	assert.Eq(t, 2, countEvent(n, "work.digest"))
}

func TestItemViewSumsCurrentSessionUsage(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-usage-0001", jobstore.SessionRunning)
	b := session(t, st, "sess-usage-0002", jobstore.SessionRunning)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", Source: jobstore.WorkOriginAuto, SessionIDs: []string{a.SessionID, b.SessionID}})
	assert.NoErr(t, err)

	d, err := svc.Detail(w.ID, 5)
	assert.NoErr(t, err)
	assert.Nil(t, d.Usage) // nothing reported: no usage, not zero usage

	_, err = st.AddSessionUsage(a.SessionID, runner.SessionUsage{Main: runner.Usage{TotalTokens: 100}, Sub: runner.Usage{TotalTokens: 20}})
	assert.NoErr(t, err)
	_, err = st.AddSessionUsage(b.SessionID, runner.SessionUsage{Main: runner.Usage{TotalTokens: 5}})
	assert.NoErr(t, err)
	d, err = svc.Detail(w.ID, 5)
	assert.NoErr(t, err)
	if d.Usage == nil {
		t.Fatal("usage missing")
	}
	assert.Eq(t, int64(125), d.Usage.TotalTokens)
}

func TestCloseStopsRunAndWaitsForBackgroundTasks(t *testing.T) {
	svc, _, _ := newSvc(t)
	runDone := make(chan struct{})
	go func() { svc.Run(make(chan struct{})); close(runDone) }() // the stop channel never closes

	started, finished := make(chan struct{}), make(chan struct{})
	assert.True(t, svc.spawn(func() {
		close(started)
		<-svc.BackgroundContext().Done() // a task that only ends when Close cancels it
		close(finished)
	}))
	<-started
	assert.NoErr(t, svc.Close())
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before the background task ended")
	}
	wait.Until(t, 2*time.Second, "Run returns after Close", func() bool {
		select {
		case <-runDone:
			return true
		default:
			return false
		}
	})
	assert.False(t, svc.spawn(func() { t.Error("spawn ran after Close") }))
	assert.NoErr(t, svc.Close()) // idempotent
}

func TestCloseIsBounded(t *testing.T) {
	svc, _, _ := newSvc(t)
	old := closeWait
	closeWait = 50 * time.Millisecond
	defer func() { closeWait = old }()
	release := make(chan struct{})
	assert.True(t, svc.spawn(func() { <-release })) // ignores the cancellation
	assert.Err(t, svc.Close())
	close(release)
	svc.WaitIdle()
}
