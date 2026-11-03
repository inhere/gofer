package job

import (
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// T8 (design 2026-11-03 关键流程: 子 job 接管链 / 子 job 重试): a receipt follows the work into
// the job that finally carries the result — an automatic continuation, a transfer, a
// retry — and a child whose retry is still to run does not wake its parent yet.

// retargetService builds a service with a resident remote parent session (the receipt's
// target) and the gated agents, so a child job can be watched while it runs.
func retargetService(t *testing.T) (*Service, *recordingSessionSender, childWakeGate) {
	t.Helper()
	root, gate := t.TempDir(), newChildWakeGate(t)
	projects, runners := childWakeProjectCfg(root)
	cfg := &config.Config{
		Storage:  config.StorageConfig{Root: root},
		Projects: projects,
		Runners:  runners,
		Agents:   childWakeAgents(testcmd.Path(t), gate),
	}
	s, sender := newChildWakeService(t, cfg, root)
	return s, sender, gate
}

// waitForSay waits until the parent session was spoken to at least once and returns the
// say texts.
func waitForSay(t *testing.T, sender *recordingSessionSender) []string {
	t.Helper()
	var texts []string
	wait.For(t, 15*time.Second, "a receipt say", func() (bool, any) {
		texts = sayTexts(sender)
		return len(texts) > 0, texts
	})
	return texts
}

// TestChildWakeRetargetFollowsTakeover: a child that is auto-continued ends without a
// terminal event of its own, so its receipt has to move onto the continuation — otherwise
// the parent would never hear about the result.
func TestChildWakeRetargetFollowsTakeover(t *testing.T) {
	t.Parallel()
	s, sender, gate := retargetService(t)
	parent := addRemoteSaySession(t, s, "parent-job", StatusAwaitingInput, 1)

	child := mustSubmit(t, s, gatedParentReq("codex"))
	w := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: child.ID, AutoChild: 1})
	gate.Release(t)

	waitAutoResumed(t, s, child.ID, true)
	var row jobstore.WakeupRecord
	wait.For(t, 15*time.Second, "the receipt moved onto the continuation", func() (bool, any) {
		got, _ := s.Get(child.ID)
		row = childWakeRow(t, s, w.ID)
		return got.AutoResumedBy != "" && row.FilterJobID == got.AutoResumedBy, row.FilterJobID
	})
	// The continuation is one-shot `printf`, so it is very likely already over: the receipt
	// goes out for the new job id either by its own event or by the post-CAS re-check.
	texts := waitForSay(t, sender)
	assert.True(t, strings.Contains(texts[0], row.FilterJobID),
		"say = %q, want the job that carries the result (%s)", texts[0], row.FilterJobID)
	assert.False(t, strings.Contains(texts[0], "supervise the children"), "say carried the prompt: %q", texts[0])
}

// TestChildWakeRetargetReportsEndedTarget pins the re-check AFTER the CAS: a job submitted a
// moment ago can already be over, and then no event will ever fire for the moved receipt.
func TestChildWakeRetargetReportsEndedTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		newStatus string
		wantSay   bool
	}{
		{"the new job already ended", StatusDone, true},
		{"the new job still runs", StatusRunning, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, sender, _ := retargetService(t)
			parent := addRemoteSaySession(t, s, "parent-job", StatusAwaitingInput, 1)
			addChildJob(t, s, "child-old", StatusFailed, 1, "")
			addChildJob(t, s, "child-new", tc.newStatus, 0, "")
			w := addChildReceipt(t, s, jobstore.WakeupRecord{
				JobID: parent.result.ID, FilterJobID: "child-old", AutoChild: 1,
			})

			s.retargetChildWakes("child-old", "child-new")

			assert.Eq(t, "child-new", childWakeRow(t, s, w.ID).FilterJobID)
			if !tc.wantSay {
				// Nothing to report yet: the running job's own events will do it.
				time.Sleep(200 * time.Millisecond)
				assert.Eq(t, 0, len(sayTexts(sender)), "a running child was reported early")
				return
			}
			texts := waitForSay(t, sender)
			assert.True(t, strings.Contains(texts[0], "child-new"), "say = %q", texts[0])
		})
	}
}

// retryHoldFixture submits a child that FAILS with a retry policy whose backoff keeps the
// retry pending, registers the receipt a parent would have, and releases the gate.
func retryHoldFixture(t *testing.T) (*Service, *recordingSessionSender, string, jobstore.WakeupRecord) {
	t.Helper()
	s, sender, gate := retargetService(t)
	parent := addRemoteSaySession(t, s, "parent-job", StatusAwaitingInput, 1)
	req := gatedParentReq("plain")
	req.SessionID = ""
	req.Retry = &RetryPolicy{MaxAttempts: 2, BackoffSec: []int{3600}}
	child := mustSubmit(t, s, req)
	w := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: child.ID, AutoChild: 1})
	gate.Release(t)
	settled(t, s, child.ID)
	return s, sender, child.ID, w
}

// TestChildWakeRetryHeldDuringBackoff: a failed child with a retry waiting to run has not
// said its last word — the parent is not woken, and the receipt stays armed for the retry's
// own result.
func TestChildWakeRetryHeldDuringBackoff(t *testing.T) {
	t.Parallel()
	s, sender, childID, w := retryHoldFixture(t)

	if rows := retryRows(t, s, childID); len(rows) != 1 || rows[0].State != jobstore.RetryPending {
		t.Fatalf("retry rows = %+v, want one pending retry", rows)
	}
	// The receipt was triggered by the failure; the say must NOT have gone out.
	time.Sleep(300 * time.Millisecond)
	assert.Eq(t, 0, len(sayTexts(sender)), "a child with a pending retry woke its parent")
	row := childWakeRow(t, s, w.ID)
	assert.Eq(t, jobstore.WakeupPendingSay, row.ContinuationJobID, "the held receipt lost its sentinel")
	assert.Eq(t, 1, row.Enabled, "the held receipt was retired")

	// The retry is dropped: now the failure is the child's last word.
	if err := s.CancelRetry(rowsOf(t, s, childID).ID); err != nil {
		t.Fatalf("CancelRetry: %v", err)
	}
	texts := waitForSay(t, sender)
	assert.True(t, strings.Contains(texts[0], childID), "say = %q, want the failed child", texts[0])
}

// rowsOf is retryRows for the single row the fixture schedules.
func rowsOf(t *testing.T, s *Service, childID string) jobstore.RetryRecord {
	t.Helper()
	rows := retryRows(t, s, childID)
	if len(rows) != 1 {
		t.Fatalf("retry rows = %+v, want one", rows)
	}
	return rows[0]
}

// addFailedChild stores a finished failed child row owned by callerID.
func addFailedChild(t *testing.T, s *Service, id, callerID string) {
	t.Helper()
	res := JobResult{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "local", Status: StatusFailed,
		ExitCode: 1, Cwd: ".", ResultDir: t.TempDir(), CallerID: callerID, OriginCaller: callerID,
	}
	if err := s.persist(res); err != nil {
		t.Fatalf("persist child %s: %v", id, err)
	}
}

// TestChildWakeRetryCancelDeliversReceipt: every way a retry stops for good reports the
// child it was going to re-run — an unreadable row, a refused admission and an operator's
// cancel.
func TestChildWakeRetryCancelDeliversReceipt(t *testing.T) {
	t.Parallel()
	const childID = "child-cancel"
	cases := []struct {
		name  string
		setup func(t *testing.T, s *Service) jobstore.RetryRecord
		run   func(t *testing.T, s *Service, rec jobstore.RetryRecord)
	}{
		{
			name: "unreadable request",
			setup: func(t *testing.T, s *Service) jobstore.RetryRecord {
				return insertRetry(t, s, childID, "not json at all", 0)
			},
			run: func(t *testing.T, s *Service, _ jobstore.RetryRecord) {
				if _, failed, err := s.SweepDueRetries(time.Now().Unix(), 10, 60); err != nil || failed != 1 {
					t.Fatalf("sweep = failed %d err %v, want the row cancelled", failed, err)
				}
			},
		},
		{
			name: "admission refused",
			setup: func(t *testing.T, s *Service) jobstore.RetryRecord {
				rec := insertRetry(t, s, childID,
					`{"project_key":"self","agent":"exec","runner":"local","cmd":["go","version"],"cwd":".","caller_id":"ci-bot"}`, 0)
				// The caller's ceiling tightened after the work it caused was admitted, so the
				// re-run is refused (ErrCeilingDenied: permanent, so the row is cancelled).
				reloadCeiling(t, s, "ci-bot", nil)
				return rec
			},
			run: func(t *testing.T, s *Service, _ jobstore.RetryRecord) {
				if _, failed, err := s.SweepDueRetries(time.Now().Unix(), 10, 60); err != nil || failed != 1 {
					t.Fatalf("sweep = failed %d err %v, want the row cancelled", failed, err)
				}
			},
		},
		{
			name: "operator cancel",
			setup: func(t *testing.T, s *Service) jobstore.RetryRecord {
				return insertRetry(t, s, childID,
					`{"project_key":"self","agent":"exec","runner":"local","cmd":["go","version"],"cwd":".","caller_id":"ci-bot"}`, 3600)
			},
			run: func(t *testing.T, s *Service, rec jobstore.RetryRecord) {
				if err := s.CancelRetry(rec.ID); err != nil {
					t.Fatalf("CancelRetry(%s): %v", rec.ID, err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, sender, _ := retargetService(t)
			parent := addRemoteSaySession(t, s, "parent-job", StatusAwaitingInput, 1)
			addFailedChild(t, s, childID, "ci-bot")
			addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: childID, AutoChild: 1})
			rec := tc.setup(t, s)

			tc.run(t, s, rec)

			texts := waitForSay(t, sender)
			assert.True(t, strings.Contains(texts[0], childID), "say = %q, want the child whose retry was dropped", texts[0])
		})
	}
}

// insertRetry stores one pending retry row due at now+dueIn.
func insertRetry(t *testing.T, s *Service, sourceJobID, requestJSON string, dueIn int64) jobstore.RetryRecord {
	t.Helper()
	now := time.Now().Unix()
	rec := jobstore.RetryRecord{
		ID: jobstore.NewRetryID(), SourceJobID: sourceJobID, Attempt: 2,
		RequestJSON: requestJSON, State: jobstore.RetryPending,
		NextRunAt: now + dueIn, CreatedAt: now,
	}
	if err := s.Meta().InsertRetry(rec); err != nil {
		t.Fatalf("InsertRetry: %v", err)
	}
	return rec
}

// jobCount is how many job rows the store holds — the "nothing new was submitted" measure
// a negative assertion needs (a fired wakeup either submits a continuation or records
// job.wakeup_failed, and both are visible this way).
func jobCount(t *testing.T, s *Service) int {
	t.Helper()
	jobs, err := s.Meta().ListJobs(jobstore.ListQuery{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	return len(jobs)
}

// TestChildWakeRetargetLeavesManualWakeupOnPlainJob: a manually registered wakeup whose
// target is an ORDINARY job (not a resident session) is not a receipt — it already fired
// for the child's own failure, and neither the takeover nor a cancelled retry may fire it
// again (that would resume a job nobody asked about).
func TestChildWakeRetargetLeavesManualWakeupOnPlainJob(t *testing.T) {
	t.Parallel()
	s, _, gate := retargetService(t)
	// The wakeup's target: a plain (non-session) job that has ended.
	target := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "gated", Runner: "local",
		Prompt: "an unrelated job", Cwd: ".", TimeoutSec: 30,
	})
	gate.Release(t)
	settled(t, s, target.ID)
	if got, _ := s.Get(target.ID); got.Session {
		t.Fatalf("fixture: %s is a resident session, want a plain job", target.ID)
	}
	w := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: target.ID, FilterJobID: "child-plain"})

	s.retargetChildWakes("child-plain", "child-moved")
	assert.Eq(t, "child-plain", childWakeRow(t, s, w.ID).FilterJobID,
		"a manual wakeup on a plain job followed the work")

	jobsBefore := jobCount(t, s)
	s.deliverRetryCancelledReceipts("child-plain")
	assert.Eq(t, jobsBefore, jobCount(t, s), "a cancelled retry fired a manual wakeup a second time")
	types := eventTypes(t, s, target.ID)
	assert.False(t, contains(types, EventJobWakeupFired) || contains(types, EventJobWakeupFailed),
		"a cancelled retry fired the manual wakeup: %v", types)
	assert.Eq(t, int64(0), childWakeRow(t, s, w.ID).FiredCount)
}

// TestChildWakeRetargetLeavesSelfWatchingWakeup: a manual wakeup registered ON the job it
// watches keeps watching that job. Following the work would point the receipt at the new
// job while the target stays the old, ended one — the next result would resume a dead job.
func TestChildWakeRetargetLeavesSelfWatchingWakeup(t *testing.T) {
	t.Parallel()
	s, _, gate := retargetService(t)
	child := mustSubmit(t, s, gatedParentReq("codex"))
	// The shape `job wakeup add <job> --event job.terminal` produces: filter defaults to the
	// job itself, and it is not an automatic receipt.
	w := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: child.ID, FilterJobID: child.ID})
	gate.Release(t)

	waitAutoResumed(t, s, child.ID, true)
	row := childWakeRow(t, s, w.ID)
	assert.Eq(t, child.ID, row.FilterJobID, "a self-watching manual wakeup followed the work")
	assert.Eq(t, 1, row.Enabled)
	assert.Eq(t, int64(0), row.FiredCount)
}

// TestChildWakeRetargetCarriesManualWakeupOnResidentParent: a manual wakeup on ANOTHER job
// whose target is a live resident session IS answered by a receipt (T5 routing), so it keeps
// the receipt behaviour: it follows a takeover and is delivered when a retry is cancelled.
func TestChildWakeRetargetCarriesManualWakeupOnResidentParent(t *testing.T) {
	t.Parallel()
	t.Run("follows a takeover", func(t *testing.T) {
		s, sender, gate := retargetService(t)
		parent := addRemoteSaySession(t, s, "parent-job", StatusAwaitingInput, 1)
		child := mustSubmit(t, s, gatedParentReq("codex"))
		w := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: child.ID})
		gate.Release(t)

		waitAutoResumed(t, s, child.ID, true)
		var row jobstore.WakeupRecord
		wait.For(t, 15*time.Second, "the manual wakeup moved onto the continuation", func() (bool, any) {
			got, _ := s.Get(child.ID)
			row = childWakeRow(t, s, w.ID)
			return got.AutoResumedBy != "" && row.FilterJobID == got.AutoResumedBy, row.FilterJobID
		})
		texts := waitForSay(t, sender)
		assert.True(t, strings.Contains(texts[0], row.FilterJobID), "say = %q", texts[0])
	})
	t.Run("delivered on a cancelled retry", func(t *testing.T) {
		s, sender, _ := retargetService(t)
		parent := addRemoteSaySession(t, s, "parent-job", StatusAwaitingInput, 1)
		addFailedChild(t, s, "child-manual", "ci-bot")
		addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-manual"})
		rec := insertRetry(t, s, "child-manual",
			`{"project_key":"self","agent":"exec","runner":"local","cmd":["go","version"],"cwd":"."}`, 3600)

		if err := s.CancelRetry(rec.ID); err != nil {
			t.Fatalf("CancelRetry(%s): %v", rec.ID, err)
		}
		texts := waitForSay(t, sender)
		assert.True(t, strings.Contains(texts[0], "child-manual"), "say = %q", texts[0])
	})
}
