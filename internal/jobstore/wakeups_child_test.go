package jobstore

import (
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestWakeupAutoChildRoundTrip: the auto_child marker survives a round trip and a
// caller-registered row (the JOB-09 case) reads back as 0, so an upgraded database
// behaves exactly as before for every wakeup a human or agent registered.
func TestWakeupAutoChildRoundTrip(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-auto", JobID: "parent-1", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal"}),
		FilterJobID:    "child-1", Mode: WakeupModeContinuous, Enabled: 1,
		CreatedBy: "alice", CreatedAt: 10, AutoChild: 1,
	}))
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-manual", JobID: "parent-1", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal"}),
		FilterJobID:    "child-2", Mode: WakeupModeOnce, Enabled: 1, CreatedAt: 20,
	}))

	got, ok, err := s.GetWakeup("wk-auto")
	assert.NoErr(t, err)
	assert.True(t, ok)
	if got.AutoChild != 1 || got.FilterJobID != "child-1" {
		t.Fatalf("row = %+v, want an auto_child row", got)
	}
	got, _, _ = s.GetWakeup("wk-manual")
	if got.AutoChild != 0 {
		t.Fatalf("row = %+v, want a caller-registered row to stay auto_child=0", got)
	}

	// The parent listing returns the parent's rows only.
	rows, err := s.ListAutoChildByParent("parent-1")
	assert.NoErr(t, err)
	if len(rows) != 1 || rows[0].ID != "wk-auto" {
		t.Fatalf("ListAutoChildByParent = %+v, want only wk-auto", rows)
	}
	if other, err := s.ListAutoChildByParent("parent-2"); err != nil || len(other) != 0 {
		t.Fatalf("ListAutoChildByParent(other) = %+v/%v, want empty", other, err)
	}
}

// TestWakeupPendingSay: the pending-delivery sentinel is durable, invisible to the
// "triggered" queries, counted only when the receipt actually lands, and cleared
// once — a second completion cannot count the same delivery twice.
func TestWakeupPendingSay(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-1", JobID: "parent-1", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal"}),
		FilterJobID:    "child-1", Mode: WakeupModeOnce, Enabled: 1, CreatedAt: 1, AutoChild: 1,
	}))

	assert.NoErr(t, s.MarkWakeupPendingSay("wk-1"))
	row, _, _ := s.GetWakeup("wk-1")
	if row.ContinuationJobID != WakeupPendingSay || row.FiredCount != 0 {
		t.Fatalf("row = %+v, want the await_input sentinel and no count", row)
	}
	if row.IsContinuationPending() {
		t.Fatalf("the say sentinel must not read as an in-flight submit claim")
	}
	pending, err := s.ListPendingSay()
	assert.NoErr(t, err)
	if len(pending) != 1 || pending[0].ID != "wk-1" {
		t.Fatalf("ListPendingSay = %+v, want wk-1", pending)
	}

	// Completion clears the sentinel, counts the delivery and consumes a once row.
	assert.NoErr(t, s.CompleteWakeupSay("wk-1", true))
	row, _, _ = s.GetWakeup("wk-1")
	if row.ContinuationJobID != "" || row.FiredCount != 1 || row.Enabled != 0 {
		t.Fatalf("row = %+v, want the sentinel cleared, counted once and disabled", row)
	}
	if pending, err := s.ListPendingSay(); err != nil || len(pending) != 0 {
		t.Fatalf("ListPendingSay after completion = %+v/%v, want empty", pending, err)
	}

	// A second completion is a no-op: the sentinel it CASes on is gone.
	assert.NoErr(t, s.CompleteWakeupSay("wk-1", true))
	row, _, _ = s.GetWakeup("wk-1")
	if row.FiredCount != 1 {
		t.Fatalf("row = %+v, want the double completion not to count twice", row)
	}

	// A continuous row stays armed after the receipt landed.
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-2", JobID: "parent-1", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal"}),
		FilterJobID:    "child-2", Mode: WakeupModeContinuous, Enabled: 1, CreatedAt: 2, AutoChild: 1,
	}))
	assert.NoErr(t, s.MarkWakeupPendingSay("wk-2"))
	assert.NoErr(t, s.CompleteWakeupSay("wk-2", false))
	row, _, _ = s.GetWakeup("wk-2")
	if row.Enabled != 1 || row.FiredCount != 1 {
		t.Fatalf("row = %+v, want a continuous receipt armed and counted", row)
	}
}

// TestListAutoChildFinishedBefore: the reconcile scan returns the enabled child
// receipts whose child job ENDED before the cut-off — never one still running, never
// one awaiting a human decision (needs_review is not terminal), never a row that was
// already disabled or is not a child receipt, and never one whose child ended inside
// the grace window.
func TestListAutoChildFinishedBefore(t *testing.T) {
	s := openTest(t)
	const ended = int64(1000)
	for _, rec := range []JobRecord{
		func() JobRecord { r := sampleJob("child-done", "self", 1); r.Status, r.EndedAt, r.UpdatedAt = "done", ended, ended; return r }(),
		func() JobRecord {
			r := sampleJob("child-review", "self", 1)
			r.Status, r.EndedAt, r.UpdatedAt = "needs_review", ended, ended
			return r
		}(),
		func() JobRecord { r := sampleJob("child-running", "self", 1); r.Status, r.UpdatedAt = "running", 1; return r }(),
	} {
		assert.NoErr(t, s.UpsertJob(rec))
	}
	auto := func(id, child string, enabled int) WakeupRecord {
		return WakeupRecord{
			ID: id, JobID: "parent-1", Kind: WakeupKindEvent,
			EventTypesJSON: EncodeStringList([]string{"job.terminal"}),
			FilterJobID:    child, Mode: WakeupModeContinuous, Enabled: enabled,
			CreatedAt: 1, AutoChild: 1,
		}
	}
	assert.NoErr(t, s.InsertWakeup(auto("wk-done", "child-done", 1)))
	assert.NoErr(t, s.InsertWakeup(auto("wk-review", "child-review", 1)))
	assert.NoErr(t, s.InsertWakeup(auto("wk-running", "child-running", 1)))
	assert.NoErr(t, s.InsertWakeup(auto("wk-off", "child-done", 0)))
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-manual", JobID: "parent-1", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal"}),
		FilterJobID:    "child-done", Mode: WakeupModeOnce, Enabled: 1, CreatedAt: 1,
	}))

	rows, err := s.ListAutoChildFinishedBefore(ended - 1)
	assert.NoErr(t, err)
	if len(rows) != 0 {
		t.Fatalf("finished-before(%d) = %+v, want nothing inside the grace window", ended-1, rows)
	}
	rows, err = s.ListAutoChildFinishedBefore(ended)
	assert.NoErr(t, err)
	if len(rows) != 1 || rows[0].ID != "wk-done" {
		t.Fatalf("finished-before(%d) = %+v, want only wk-done", ended, rows)
	}
}

// TestRetargetWakeupFilterCAS: moving a receipt to the job that now carries the
// result is a compare-and-swap — of two racers (the retry submit and the event path)
// exactly one wins, and the loser leaves the row alone. A stale value never moves it.
func TestRetargetWakeupFilterCAS(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-1", JobID: "parent-1", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal"}),
		FilterJobID:    "child-old", Mode: WakeupModeContinuous, Enabled: 1, CreatedAt: 1, AutoChild: 1,
	}))

	ok, err := s.RetargetWakeupFilter("wk-1", "child-old", "child-new")
	assert.NoErr(t, err)
	assert.True(t, ok)

	// The loser of a race sees the old value gone.
	ok, err = s.RetargetWakeupFilter("wk-1", "child-old", "child-other")
	assert.NoErr(t, err)
	if ok {
		t.Fatalf("the retarget CAS won twice for the same source job")
	}
	row, _, _ := s.GetWakeup("wk-1")
	if row.FilterJobID != "child-new" {
		t.Fatalf("row = %+v, want the winner's target", row)
	}

	// Two concurrent retargets of the same source: exactly one succeeds.
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-2", JobID: "parent-1", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal"}),
		FilterJobID:    "child-a", Mode: WakeupModeContinuous, Enabled: 1, CreatedAt: 2, AutoChild: 1,
	}))
	var wg sync.WaitGroup
	wins := make([]bool, 2)
	for i, target := range []string{"child-b", "child-c"} {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			won, rerr := s.RetargetWakeupFilter("wk-2", "child-a", target)
			if rerr != nil {
				t.Errorf("retarget: %v", rerr)
			}
			wins[i] = won
		}(i, target)
	}
	wg.Wait()
	if wins[0] == wins[1] {
		t.Fatalf("wins = %v, want exactly one of the two racing retargets to win", wins)
	}
}

// TestListJobEventsByType: the cross-job reader returns only the named type inside
// the time window, newest first — the inbox card source.
func TestListJobEventsByType(t *testing.T) {
	s := openTest(t)
	for _, ev := range []JobEvent{
		{JobID: "p1", Type: "job.child_wake_failed", Detail: `{"wakeup_id":"wk-1"}`, At: 100},
		{JobID: "p1", Type: "job.terminal", At: 110},
		{JobID: "p2", Type: "job.child_wake_failed", Detail: `{"wakeup_id":"wk-2"}`, At: 200},
		{JobID: "p2", Type: "job.child_wake_failed", Detail: `{"wakeup_id":"wk-3"}`, At: 300},
	} {
		if _, err := s.InsertJobEvent(ev); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}

	rows, err := s.ListJobEventsByType("job.child_wake_failed", 0)
	assert.NoErr(t, err)
	if len(rows) != 3 || rows[0].At != 300 || rows[1].At != 200 || rows[2].At != 100 {
		t.Fatalf("by type = %+v, want the three alerts newest first", rows)
	}
	rows, err = s.ListJobEventsByType("job.child_wake_failed", 150)
	assert.NoErr(t, err)
	if len(rows) != 2 || rows[0].JobID != "p2" || rows[1].JobID != "p2" {
		t.Fatalf("by type since 150 = %+v, want only the two newer rows", rows)
	}
	if rows, err := s.ListJobEventsByType("job.child_wake_failed", 500); err != nil || len(rows) != 0 {
		t.Fatalf("by type since 500 = %+v/%v, want empty", rows, err)
	}
}
