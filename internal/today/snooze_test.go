package today

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// newClockService is newTestService with a clock the test moves.
func newClockService(t *testing.T) (*Service, *jobstore.Store, *time.Time) {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "snooze.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	now := testNow
	clk := func() time.Time { return now }
	st.SetClock(clk)
	ws := work.New(st)
	t.Cleanup(func() { _ = ws.Close() }) // before the store closes (gofer-r7am)
	ws.SetNow(clk)
	svc := New(Deps{Store: st, Work: ws, Now: clk,
		Runners: func() RunnerStatus { return RunnerStatus{Online: 1, Total: 1} },
		Version: func() string { return "test" }})
	return svc, st, &now
}

func needsMe(t *testing.T, st *jobstore.Store, title string) jobstore.WorkItem {
	t.Helper()
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: title, Status: jobstore.WorkNeedsMe, By: "me"})
	assert.NoErr(t, err)
	return w
}

func TestSnoozeHidesUntilTimeThenWakes(t *testing.T) {
	svc, st, now := newClockService(t)
	w := needsMe(t, st, "等我拍板")
	key := KindWork + ":" + w.ID

	sc, err := svc.Snooze(SnoozeInput{CardKey: key, UntilAt: now.Unix() + 3600}, "me")
	assert.NoErr(t, err)
	assert.Eq(t, "等我拍板", sc.Title)
	assert.Eq(t, KindWork, sc.Kind)

	resp, err := svc.Today(Query{})
	assert.NoErr(t, err)
	assert.Eq(t, 1, resp.Snoozed)
	assert.Len(t, resp.Decisions, 0)
	list, err := svc.Snoozed(false)
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	assert.Eq(t, key, list[0].CardKey)
	assert.Eq(t, now.Unix()+3600, list[0].UntilAt)

	// The snooze is a today.action audit row.
	handled, err := svc.HandledSince(1)
	assert.NoErr(t, err)
	assert.Len(t, handled, 1)
	assert.Eq(t, SnoozeActionID, handled[0].ActionID)
	assert.Eq(t, "等我拍板", handled[0].Title)

	// Time passes: the card is back, flagged.
	*now = now.Add(61 * time.Minute)
	resp, err = svc.Today(Query{})
	assert.NoErr(t, err)
	assert.Eq(t, 0, resp.Snoozed)
	assert.Eq(t, []string{key}, keys(resp.Decisions))
	assert.True(t, resp.Decisions[0].Woke)
	assert.Eq(t, WokeTime, resp.Decisions[0].WokeReason)
	// The flag stays on the next read, then ages out after a day (row cleaned).
	resp, _ = svc.Today(Query{})
	assert.True(t, resp.Decisions[0].Woke)
	*now = now.Add(25 * time.Hour)
	resp, _ = svc.Today(Query{})
	assert.False(t, resp.Decisions[0].Woke)
	_, ok, _ := st.GetTodaySnooze(key)
	assert.False(t, ok)
}

func TestSnoozeWakesOnActivity(t *testing.T) {
	svc, st, now := newClockService(t)
	w := needsMe(t, st, "等我")
	key := KindWork + ":" + w.ID
	_, err := svc.Snooze(SnoozeInput{CardKey: key, UntilAt: now.Unix() + 86400}, "me")
	assert.NoErr(t, err)

	// A session heartbeat is not activity (the work card reads the stored activity).
	*now = now.Add(time.Minute)
	resp, _ := svc.Today(Query{})
	assert.Eq(t, 1, resp.Snoozed)

	_, err = st.AppendWorkJournal(w.ID, jobstore.WorkJournalReport, "进展：卡在权限", "agent")
	assert.NoErr(t, err)
	resp, err = svc.Today(Query{})
	assert.NoErr(t, err)
	assert.Eq(t, 0, resp.Snoozed)
	assert.Len(t, resp.Decisions, 1)
	assert.Eq(t, WokeActivity, resp.Decisions[0].WokeReason)

	// Re-snoozing records the new activity and clears the flag.
	_, err = svc.Snooze(SnoozeInput{CardKey: key, UntilAt: now.Unix() + 3600}, "me")
	assert.NoErr(t, err)
	resp, _ = svc.Today(Query{})
	assert.Eq(t, 1, resp.Snoozed)
}

func TestSnoozeWakesWhenJobEnds(t *testing.T) {
	svc, st, _ := newClockService(t)
	putJob(t, st, jobstore.JobRecord{ID: "j-run", ProjectKey: "p1", Agent: "codex", Status: job.StatusRunning})
	putJob(t, st, jobstore.JobRecord{ID: "j-dep", ProjectKey: "p1", Agent: "codex", Status: job.StatusRunning})
	assert.NoErr(t, st.UpsertInteraction(jobstore.InteractionRecord{ID: "i1", JobID: "j-run", Type: job.InteractionTypeQuestion,
		Prompt: "继续吗？", Status: "pending", CreatedAt: ago(2), ExpiresAt: testNow.Unix() + 600}))
	key := "interaction:j-run/i1"

	// A card that will time out may be snoozed; its own timeout still applies.
	sc, err := svc.Snooze(SnoozeInput{CardKey: key, UntilJobID: "j-dep"}, "me")
	assert.NoErr(t, err)
	assert.Eq(t, testNow.Unix()+600, sc.ExpiresAt)
	resp, _ := svc.Today(Query{})
	assert.Eq(t, 1, resp.Snoozed)

	putJob(t, st, jobstore.JobRecord{ID: "j-dep", ProjectKey: "p1", Agent: "codex", Status: job.StatusDone, EndedAt: testNow.Unix()})
	resp, err = svc.Today(Query{})
	assert.NoErr(t, err)
	assert.Eq(t, 0, resp.Snoozed)
	assert.Eq(t, []string{key}, keys(resp.Decisions))
	assert.Eq(t, WokeJob, resp.Decisions[0].WokeReason)
}

func TestSnoozeCleansGoneCardsAndKeepsHiddenExec(t *testing.T) {
	svc, st, now := newClockService(t)
	w := needsMe(t, st, "会被处理")
	key := KindWork + ":" + w.ID
	putJob(t, st, jobstore.JobRecord{ID: "j-exec", ProjectKey: "p1", Agent: "exec", Status: job.StatusNeedsReview, EndedAt: ago(5)})
	_, err := svc.Snooze(SnoozeInput{CardKey: key, UntilAt: now.Unix() + 3600}, "me")
	assert.NoErr(t, err)
	_, err = svc.Snooze(SnoozeInput{CardKey: "review:j-exec", UntilAt: now.Unix() + 3600}, "me")
	assert.NoErr(t, err)

	// The work item stops waiting: its card is gone, so is the row. The exec review is only
	// left out of this read, so its row stays (and is not counted).
	active := jobstore.WorkActive
	_, _, err = st.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{Status: &active}, w.Rev, "me")
	assert.NoErr(t, err)
	resp, err := svc.Today(Query{})
	assert.NoErr(t, err)
	assert.Eq(t, 0, resp.Snoozed)
	_, ok, _ := st.GetTodaySnooze(key)
	assert.False(t, ok)
	_, ok, _ = st.GetTodaySnooze("review:j-exec")
	assert.True(t, ok)
	resp, _ = svc.Today(Query{IncludeExec: true})
	assert.Eq(t, 1, resp.Snoozed)

	// Accepted: the exec review row goes too.
	putJob(t, st, jobstore.JobRecord{ID: "j-exec", ProjectKey: "p1", Agent: "exec", Status: job.StatusDone, EndedAt: ago(5)})
	_, _ = svc.Today(Query{})
	_, ok, _ = st.GetTodaySnooze("review:j-exec")
	assert.False(t, ok)
}

func TestSnoozeValidationAndUnsnooze(t *testing.T) {
	svc, st, now := newClockService(t)
	w := needsMe(t, st, "等我")
	key := KindWork + ":" + w.ID
	putJob(t, st, jobstore.JobRecord{ID: "j-done", ProjectKey: "p1", Agent: "codex", Status: job.StatusDone, EndedAt: ago(1)})

	for _, in := range []SnoozeInput{
		{CardKey: "", UntilAt: now.Unix() + 60},
		{CardKey: key},
		{CardKey: key, UntilAt: now.Unix() + 60, UntilJobID: "j-done"},
		{CardKey: key, UntilAt: now.Unix() - 1},
		{CardKey: key, UntilAt: now.Unix() + MaxSnoozeSec + 1},
		{CardKey: key, UntilJobID: "nope"},
		{CardKey: key, UntilJobID: "j-done"},
	} {
		_, err := svc.Snooze(in, "me")
		assert.True(t, errors.Is(err, ErrInvalidSnooze), "%+v: %v", in, err)
	}
	_, err := svc.Snooze(SnoozeInput{CardKey: "work:missing", UntilAt: now.Unix() + 60}, "me")
	assert.True(t, errors.Is(err, ErrCardNotQueued))

	_, err = svc.Snooze(SnoozeInput{CardKey: key, UntilAt: now.Unix() + 60}, "me")
	assert.NoErr(t, err)
	ok, err := svc.Unsnooze(key)
	assert.NoErr(t, err)
	assert.True(t, ok)
	ok, err = svc.Unsnooze(key)
	assert.NoErr(t, err)
	assert.False(t, ok)
	resp, _ := svc.Today(Query{})
	assert.Eq(t, 0, resp.Snoozed)
	assert.Len(t, resp.Decisions, 1)
	assert.False(t, resp.Decisions[0].Woke)
}
