package job

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// T9 (design 2026-11-03-resident-job-child-wakeup 扫描与对账): the tick delivers what the
// receipt path left waiting, re-sends a worker frame nothing confirmed, and reconciles
// the automatic receipts whose child ended unobserved. Every case here drives
// Service.SweepPendingSays — the function serve's schedule loop calls — so the wiring
// that matters is the one under test.

// pendingSayService is a service over a SHARED root: two of them are two serve
// processes over one database, which is what the restart cases need.
func pendingSayService(t *testing.T, root string) (*Service, *recordingSessionSender) {
	t.Helper()
	projects, runners := childWakeProjectCfg(root)
	cfg := &config.Config{
		Storage:  config.StorageConfig{Root: root},
		Projects: projects,
		Runners:  runners,
		Agents:   map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}},
	}
	return newChildWakeService(t, cfg, root)
}

// addFinishedChild stores a terminal child row with an ended_at, i.e. the shape the
// reconciliation query selects.
func addFinishedChild(t *testing.T, s *Service, id, status string, endedAt int64) JobResult {
	t.Helper()
	res := JobResult{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Status: status, EndedAt: endedAt, Cwd: ".", ResultDir: t.TempDir(),
	}
	if err := s.persist(res); err != nil {
		t.Fatalf("persist child %s: %v", id, err)
	}
	return res
}

// TestSweepPendingSaysDeliversAfterRestart: the receipt state is process-local, so after
// a restart nothing in memory says a receipt is waiting. The tick reads the rows and
// delivers them anyway — saying one twice is harmless, losing it is not.
func TestSweepPendingSaysDeliversAfterRestart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first, firstSender := pendingSayService(t, root)
	// The parent was mid-turn when the child finished: the receipt parked.
	busy := addRemoteSaySession(t, first, "parent-job", StatusRunning, 1)
	addFinishedChild(t, first, "child-restart", StatusDone, first.nowFn().Unix())
	w := addChildReceipt(t, first, jobstore.WakeupRecord{
		JobID: busy.result.ID, FilterJobID: "child-restart", AutoChild: 1,
	})
	first.deliverChildReceipt(w, EventJobTerminal)
	assert.Eq(t, 0, len(sayTexts(firstSender)), "a busy parent was interrupted")

	// The process dies and a new one comes up over the same store; the worker reports the
	// session idle again.
	second, secondSender := pendingSayService(t, root)
	addRemoteSaySession(t, second, "parent-job", StatusAwaitingInput, 2)
	second.SweepPendingSays(second.nowFn())

	texts := sayTexts(secondSender)
	assert.Eq(t, 1, len(texts), "the receipt a restart left parked was not delivered: %v", texts)
	assert.True(t, strings.Contains(texts[0], "child-restart"), "say = %q", texts[0])
	if row := childWakeRow(t, second, w.ID); row.ContinuationJobID != jobstore.WakeupPendingSay {
		t.Fatalf("continuation = %q, want the sentinel kept until the session confirms", row.ContinuationJobID)
	}
}

// TestSweepPendingSaysRedeliversAJobFailedInTheStore: FailRecoveringJobs writes a worker
// child's failure straight into the database — no job.terminal, so no event ever fired
// for it. The reconciliation finds the receipt and delivers it, but only after the grace
// window, which covers finish's own writes.
func TestSweepPendingSaysRedeliversAJobFailedInTheStore(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	now := s.nowFn()
	if err := s.persist(JobResult{
		ID: "child-recovered", ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerID: "w1", Status: StatusRecovering, Cwd: ".", ResultDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("persist recovering child: %v", err)
	}
	addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-recovered", AutoChild: 1})

	// The reconnect window expires: the child becomes failed without any event.
	if _, err := s.meta.FailRecoveringJobs(now.Unix(), "worker lost"); err != nil {
		t.Fatalf("FailRecoveringJobs: %v", err)
	}
	s.SweepPendingSays(now)
	assert.Eq(t, 0, len(sayTexts(sender)), "a receipt inside the grace window was delivered")

	s.SweepPendingSays(now.Add((childWakeReconcileGraceSec + 1) * time.Second))
	texts := sayTexts(sender)
	assert.Eq(t, 1, len(texts), "the receipt of a job failed in the store was lost: %v", texts)
	assert.True(t, strings.Contains(texts[0], "child-recovered") && strings.Contains(texts[0], StatusFailed),
		"say = %q", texts[0])
	// The receipt came from the scan, not from an event: the failure recorded none.
	assert.False(t, contains(eventTypes(t, s, "child-recovered"), EventJobTerminal),
		"fixture: the failed child recorded job.terminal")
}

// TestChildWakeReconcileLeavesReviewAndReceiptedRowsAlone: the reconciliation only
// answers for a child whose LAST word nobody heard. A child waiting for an operator's
// review, a child that already got its receipt, and a child still running are all left
// alone — waking the parent for them would wake it again on every idle window.
func TestChildWakeReconcileLeavesReviewAndReceiptedRowsAlone(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	now := s.nowFn()
	ended := now.Add(-10 * childWakeReconcileGraceSec * time.Second).Unix()

	addFinishedChild(t, s, "child-review", StatusNeedsReview, ended)
	addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-review", AutoChild: 1})
	addFinishedChild(t, s, "child-counted", StatusDone, ended)
	counted := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-counted", AutoChild: 1})
	// The receipt was delivered and confirmed: its sentinel is cleared and the row retired.
	if err := s.meta.MarkWakeupPendingSay(counted.ID); err != nil {
		t.Fatalf("park the delivered receipt: %v", err)
	}
	if err := s.meta.CompleteWakeupSay(counted.ID, true); err != nil {
		t.Fatalf("retire the delivered receipt: %v", err)
	}
	addFinishedChild(t, s, "child-live", StatusRunning, ended)
	addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-live", AutoChild: 1})

	s.SweepPendingSays(now)
	assert.Eq(t, 0, len(sayTexts(sender)), "the reconciliation woke the parent for a row nobody owes a receipt for")
}

// TestSweepPendingSaysResendsAnUnclaimedWorkerFrame: a frame the worker never consumed
// is written out again after the re-send window, with the SAME command id and the SAME
// text (the worker drops an id it has seen, so a fresh one would be a second receipt),
// and only once per window.
func TestSweepPendingSaysResendsAnUnclaimedWorkerFrame(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	clk := &fakeClock{unix: 1_800_000_000}
	s.nowFn = clk.now
	addFinishedChild(t, s, "child-lost-frame", StatusDone, clk.now().Unix())
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-lost-frame", AutoChild: 1,
	})
	s.deliverChildReceipt(w, EventJobTerminal)

	sent := sender.sentCommands()
	assert.Eq(t, 1, len(sent), "the receipt was not spoken")
	// Inside the window the tick only retries the delivery path, which the reserved say
	// slot refuses: no second frame.
	s.SweepPendingSays(clk.now())
	assert.Eq(t, 1, len(sender.sentCommands()), "a frame was re-sent inside the re-send window")

	clk.advance(childWakeResendAfterSec)
	s.SweepPendingSays(clk.now())
	sent = sender.sentCommands()
	if len(sent) != 2 {
		t.Fatalf("frames sent = %d, want the lost frame written out again", len(sent))
	}
	assert.Eq(t, sent[0].cmdID, sent[1].cmdID, "the re-send changed the command id")
	assert.Eq(t, sent[0].prompt, sent[1].prompt, "the re-send changed the message")
	assert.True(t, strings.Contains(sent[1].prompt, "child-lost-frame"), "say = %q", sent[1].prompt)
}

// TestSweepPendingSaysRedeliversAfterTheRateWindow: a "needs a person" receipt held back
// by the rate window stays pending and the tick delivers it once the window is over —
// without the tick that receipt would be lost for good.
func TestSweepPendingSaysRedeliversAfterTheRateWindow(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	clk := &fakeClock{unix: 1_800_000_000}
	s.nowFn = clk.now
	addChildJob(t, s, "child-window", StatusRunning, 0, "")
	seedPendingInteraction(t, s, "int-win-1", "child-window")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-window", AutoChild: 1,
		EventTypesJSON: jobstore.EncodeStringList([]string{EventJobTerminal, EventInteractionCreated}),
	})

	s.deliverChildReceipt(w, EventInteractionCreated)
	assert.Eq(t, 1, len(sayTexts(sender)))
	// The parent works it through; a second approval lands inside the window.
	s.applyRemoteSessionState(parent, WorkerInflightJob{
		JobID: parent.result.ID, SessionStatus: StatusAwaitingInput, TurnNo: 2,
	}, false)
	seedPendingInteraction(t, s, "int-win-2", "child-window")
	s.deliverChildReceipt(w, EventInteractionCreated)
	assert.Eq(t, 1, len(sayTexts(sender)), "a receipt inside the window woke the parent")

	// Still inside the window: the tick respects it.
	clk.advance(30)
	s.SweepPendingSays(clk.now())
	assert.Eq(t, 1, len(sayTexts(sender)), "the tick ignored the rate window")

	// The window is over: the held receipt goes out.
	clk.advance(childWakeNeedsInputEverySec)
	s.SweepPendingSays(clk.now())
	texts := sayTexts(sender)
	assert.Eq(t, 2, len(texts), "the rate-limited receipt was never re-delivered: %v", texts)
	assert.True(t, strings.Contains(texts[1], "child-window"), "say = %q", texts[1])
}

// TestChildWakeReconcileFollowsACommittedRetry: a crash between "the retry was submitted"
// and "the receipt moved onto it" leaves the receipt watching a job whose retry already
// has a new id. The reconciliation finishes that move and reports the new job.
func TestChildWakeReconcileFollowsACommittedRetry(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	now := s.nowFn()
	ended := now.Add(-10 * childWakeReconcileGraceSec * time.Second).Unix()
	addFinishedChild(t, s, "child-retried", StatusFailed, ended)
	addFinishedChild(t, s, "child-retried-2", StatusDone, ended)
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-retried", AutoChild: 1,
	})
	if err := s.Meta().InsertRetry(jobstore.RetryRecord{
		ID: jobstore.NewRetryID(), SourceJobID: "child-retried", Attempt: 2,
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"local","cmd":["go","version"],"cwd":"."}`,
		State:       jobstore.RetryDone, NewJobID: "child-retried-2", CreatedAt: ended,
	}); err != nil {
		t.Fatalf("InsertRetry: %v", err)
	}

	s.SweepPendingSays(now)

	assert.Eq(t, "child-retried-2", childWakeRow(t, s, w.ID).FilterJobID,
		"the receipt kept watching the job the retry replaced")
	// The post-CAS re-check reports the new job on its own goroutine.
	texts := waitForSay(t, sender)
	assert.True(t, strings.Contains(texts[0], "child-retried-2"), "say = %q", texts[0])
}

// TestChildWakeReconcileWaitsForAPendingRetry: while a retry is still to run the failure
// is not the child's last word, so the reconciliation says nothing (and the receipt stays
// armed for the retry's own result).
func TestChildWakeReconcileWaitsForAPendingRetry(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	now := s.nowFn()
	ended := now.Add(-10 * childWakeReconcileGraceSec * time.Second).Unix()
	addFinishedChild(t, s, "child-waiting", StatusFailed, ended)
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-waiting", AutoChild: 1,
	})
	if err := s.Meta().InsertRetry(jobstore.RetryRecord{
		ID: jobstore.NewRetryID(), SourceJobID: "child-waiting", Attempt: 2,
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"local","cmd":["go","version"],"cwd":"."}`,
		State:       jobstore.RetryPending, NextRunAt: now.Unix() + 3600, CreatedAt: ended,
	}); err != nil {
		t.Fatalf("InsertRetry: %v", err)
	}

	s.SweepPendingSays(now)

	assert.Eq(t, 0, len(sayTexts(sender)), "a child with a retry to run was reported as final")
	row := childWakeRow(t, s, w.ID)
	assert.Eq(t, "child-waiting", row.FilterJobID)
	assert.Eq(t, 1, row.Enabled, "the held receipt was retired")
}

// TestChildWakeReconcileFollowsATakeoverMarker: a child taken over by an automatic
// continuation ends without a job.terminal of its own. The reconciliation moves the
// receipt onto the job that carries the work (the same move the event path makes).
func TestChildWakeReconcileFollowsATakeoverMarker(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	now := s.nowFn()
	ended := now.Add(-10 * childWakeReconcileGraceSec * time.Second).Unix()
	taken := addFinishedChild(t, s, "child-taken", StatusFailed, ended)
	taken.AutoResumedBy = "child-continuation"
	if err := s.persist(taken); err != nil {
		t.Fatalf("persist the takeover marker: %v", err)
	}
	addFinishedChild(t, s, "child-continuation", StatusDone, ended)
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-taken", AutoChild: 1,
	})

	s.SweepPendingSays(now)

	assert.Eq(t, "child-continuation", childWakeRow(t, s, w.ID).FilterJobID,
		"the receipt stayed on the job the takeover replaced")
	texts := waitForSay(t, sender)
	assert.True(t, strings.Contains(texts[0], "child-continuation"), "say = %q", texts[0])
}

// TestChildWakeReconcileExpiredRowReportsToAPerson: an automatic receipt whose TTL runs
// out never reached its parent, so the loss is reported as job.child_wake_failed with the
// machine reason "expired" — not as job.wakeup_expired, which no notification carries.
func TestChildWakeReconcileExpiredRowReportsToAPerson(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-expired", StatusRunning, 0, "")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-expired", AutoChild: 1,
	})
	// The row is armed and waiting; its TTL passes first.
	w.ExpiresAt = s.nowFn().Add(-time.Minute).Unix()
	if err := s.meta.UpdateWakeup(w); err != nil {
		t.Fatalf("set the TTL: %v", err)
	}

	s.ExpireWakeup(w.ID)

	assert.Eq(t, 0, childWakeRow(t, s, w.ID).Enabled, "the expired receipt stayed armed")
	types := eventTypes(t, s, parent.result.ID)
	assert.Eq(t, 1, countEvents(types, EventJobChildWakeFailed), "the lost receipt was not reported")
	assert.Eq(t, 0, countEvents(types, EventJobWakeupExpired),
		"an automatic receipt reported the timer expiry event: %v", types)
	events, err := s.ListJobEvents(parent.result.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	for _, ev := range events {
		if ev.Type != EventJobChildWakeFailed {
			continue
		}
		assert.True(t, strings.Contains(ev.Detail, `"reason":"`+childWakeReasonExpired+`"`),
			"detail = %q, want the machine reason %q", ev.Detail, childWakeReasonExpired)
		assert.True(t, strings.Contains(ev.Detail, "child-expired"), "detail = %q", ev.Detail)
	}
	assert.Eq(t, 0, len(sayTexts(sender)), "an expired receipt was spoken")
}

// TestSweepPendingSaysSharesTheLockWithATrigger: the tick and the event path run at the
// same moment for one child. The receipt lock makes it ONE say — the loser finds the say
// slot taken and leaves the receipt pending instead of writing a second frame.
func TestSweepPendingSaysSharesTheLockWithATrigger(t *testing.T) {
	t.Parallel()
	s, sender := remoteSayService(t, 50*time.Millisecond)
	parent := addRemoteSaySession(t, s, "parent-job", StatusAwaitingInput, 1)
	addFinishedChild(t, s, "child-both", StatusDone, s.nowFn().Unix())
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-both", AutoChild: 1,
	})

	var wg sync.WaitGroup
	for _, run := range []func(){
		func() { s.deliverChildReceipt(w, EventJobTerminal) },
		func() { s.SweepPendingSays(s.nowFn()) },
	} {
		wg.Add(1)
		go func(run func()) { defer wg.Done(); run() }(run)
	}
	wg.Wait()

	texts := sayTexts(sender)
	assert.Eq(t, 1, len(texts), "the tick and the trigger each spoke: %v", texts)
	assert.True(t, strings.Contains(texts[0], "child-both"), "say = %q", texts[0])
	row := childWakeRow(t, s, w.ID)
	assert.True(t, row.ContinuationJobID == jobstore.WakeupPendingSay || row.ContinuationJobID == "",
		"continuation = %q, want the receipt delivered once, not duplicated", row.ContinuationJobID)
}
