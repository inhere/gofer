package job

import (
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The child-receipt TICK (design 2026-11-03-resident-job-child-wakeup T9 扫描与对账).
//
// Everything the receipt path does outside an event is driven from here, on the
// schedule loop's cadence: the say that a CRASH left parked, the receipt a rate window
// held back, the frame a worker never answered, and the automatic receipts whose child
// ended while nobody watched it at all.
//
// The tick reads the DATABASE, not the process: the per-parent receipt state
// (wakeupSayStates) is empty after a restart, so an idle hook alone would skip every
// sentinel a previous process wrote. A batch, by contrast, lives in memory only: a
// restart loses the record that a say was spoken, so that receipt goes out once more —
// saying it twice is harmless, losing it is not.

const (
	// childWakeResendAfterSec is how long an unconfirmed frame of a WORKER parent waits
	// before it is written out again with the same command id (design 送达确认: 超时重发
	// 只限 worker 父 job). A local parent is confirmed but never re-sent: re-sending means
	// writing into its capacity-1 channel, and once the idle timer has fired nobody reads
	// that channel any more.
	childWakeResendAfterSec = 60
	// childWakeReconcileGraceSec is the window the reconciliation waits after a child
	// job's ended_at before acting on it: finish records the terminal event a few lines
	// BEFORE it writes the durable retry row and the takeover marker, so a sweep inside
	// that window would report an outcome that is not the child's last word yet.
	childWakeReconcileGraceSec = 120
	// childWakeReasonSweep is the log reason of a receipt the tick delivered (as opposed
	// to one an event delivered).
	childWakeReasonSweep = "sweep"
)

// SweepPendingSays is the child-receipt tick: it reconciles the automatic receipts whose
// child job ended unobserved, then delivers every receipt the database still holds as
// waiting, and finally re-sends the worker frames nothing confirmed. It is what makes
// the whole path survive a restart and a dropped frame, and it is cheap when there is
// nothing to do (two indexed queries).
//
// now is the sweep instant (serve passes the wall clock; tests pass a frozen one).
func (s *Service) SweepPendingSays(now time.Time) {
	s.reconcileChildWakeReceipts(now)
	for _, parentID := range s.pendingSayParents() {
		s.sweepParentSays(parentID, now)
	}
}

// pendingSayParents lists, once, the parent jobs of every receipt waiting for delivery.
// The parent order follows the rows so a merged message reads in registration order.
func (s *Service) pendingSayParents() []string {
	rows, err := s.meta.ListPendingSay()
	if err != nil {
		slog.Warn("wakeup say sweep: list pending", "err", err)
		return nil
	}
	seen := make(map[string]bool, len(rows))
	parents := make([]string, 0, len(rows))
	for _, w := range rows {
		if w.JobID == "" || seen[w.JobID] {
			continue
		}
		seen[w.JobID] = true
		parents = append(parents, w.JobID)
	}
	return parents
}

// sweepParentSays runs one parent's receipt pass under its receipt lock: confirm the
// batch the session worked through, re-send an unconfirmed worker frame whose window
// passed, then merge and deliver whatever is still waiting. Sharing the lock with the
// event path (deliverChildReceipt, onParentAwaitingInput) is what keeps one row from
// being spoken twice when a child ends exactly on a tick.
func (s *Service) sweepParentSays(parentID string, now time.Time) {
	lock := s.wakeupSayLock(parentID)
	lock.Lock()
	defer lock.Unlock()
	s.confirmSayBatchLocked(parentID)
	s.resendUnconfirmedLocked(parentID)
	s.flushPendingSaysLocked(parentID, nil, childWakeReasonSweep)
}

// resendUnconfirmedLocked writes a worker parent's unconfirmed batch out again. Callers
// hold the parent's receipt lock.
//
// The age of the last attempt is measured on the service clock, the same clock the batch
// recorded it with — the sweep instant is the caller's (serve's wall clock), while this
// is in-process state.
//
// resendSessionSay decides whether the batch still owns the say slot and whether a turn
// has started since: a batch the worker already consumed (or that a newer batch
// replaced) is left alone, and the confirmation clears it on the next idle transition.
func (s *Service) resendUnconfirmedLocked(parentID string) {
	st := s.wakeupSayOf(parentID)
	st.mu.Lock()
	batch := st.batch
	st.mu.Unlock()
	if batch == nil || batch.cmdID == "" || batch.text == "" {
		return
	}
	if s.nowFn().Unix()-batch.sentAt < childWakeResendAfterSec {
		return
	}
	entry := s.entry(parentID)
	if entry == nil {
		return
	}
	entry.mu.Lock()
	remote := entry.result.Session && entry.sessionCommands == nil &&
		isWorkerRemoteSession(s.cfg.Load(), entry.result)
	entry.mu.Unlock()
	if !remote {
		return // a local parent's say is confirmed, never re-sent (see childWakeResendAfterSec)
	}
	if err := s.resendSessionSay(parentID, batch.cmdID, batch.text); err != nil {
		// Consumed already, superseded by a newer batch, worker away: the next tick asks
		// again, and the idle transition confirms or replaces the batch meanwhile.
		slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "job_id", parentID,
			"reason", "resend_deferred", "err", err)
		return
	}
	st.mu.Lock()
	batch.sentAt = s.nowFn().Unix()
	count := len(batch.items)
	st.mu.Unlock()
	slog.Info("wakeup.say_sent", "event", "wakeup.say_sent", "job_id", parentID,
		"count", count, "reason", "resend")
}

// reconcileChildWakeReceipts handles the automatic receipts whose child job reached a
// TERMINAL state at least the grace window ago and that were never triggered. That
// state has three causes, and each one has its own answer:
//
//   - the child was taken over (auto-resumed, transferred to another agent): the work
//     lives on in a NEW job id, so the receipt moves with it (retargetChildWakes), which
//     also reports the new job when it is already over;
//   - a durable retry is still to run: the failure is not the last word, so nothing is
//     said; a retry that has already been SUBMITTED and left a job id gets the same move
//     (the recovery of a crash between the submit and the CAS);
//   - neither: the trigger was lost. FailRecoveringJobs writes a worker child's failure
//     straight into the database without recording job.terminal, so no event ever fired
//     for it and its receipt waits for nothing. It is delivered here instead.
//
// Rows that still hold the pending sentinel are not touched: the delivery scan handles
// them, and re-triggering them would say the same thing twice. Rows waiting for an
// operator's review are not terminal and are not in the query at all — a parent woken
// on every review would be woken again every idle window (design 2026-11-03 总体方案).
func (s *Service) reconcileChildWakeReceipts(now time.Time) {
	cutoff := now.Add(-childWakeReconcileGraceSec * time.Second).Unix()
	rows, err := s.meta.ListAutoChildFinishedBefore(cutoff)
	if err != nil {
		slog.Warn("child wake: reconcile query", "err", err)
		return
	}
	for _, w := range rows {
		if w.ContinuationJobID != "" {
			continue // parked (the delivery scan has it) or holding a continuation slot
		}
		child, ok := s.Get(w.FilterJobID)
		if !ok || !IsTerminal(child.Status) {
			continue
		}
		if target := childWakeTakeoverTarget(child); target != "" {
			slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "wakeup_id", w.ID,
				"job_id", w.JobID, "child_job_id", child.ID, "to_job_id", target, "reason", "reconcile_takeover")
			s.retargetChildWakes(child.ID, target)
			continue
		}
		if s.hasPendingRetry(child.ID) {
			// A durable retry is still to run (pending, or claimed by a sweeper): the
			// failure is not the child's last word, so nothing is said now.
			continue
		}
		if to := s.committedRetryTarget(child.ID); to != "" {
			// The retry was SUBMITTED and left a job id, but the CAS that moves the receipt
			// never landed (a crash between the two): finish it here, and re-check the new job
			// like every other retarget does.
			slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "wakeup_id", w.ID,
				"job_id", w.JobID, "child_job_id", child.ID, "to_job_id", to, "reason", "reconcile_retry")
			s.retargetChildWakes(child.ID, to)
			continue
		}
		slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "wakeup_id", w.ID,
			"job_id", w.JobID, "child_job_id", child.ID, "reason", "reconcile_lost_receipt")
		s.deliverChildReceipt(w, EventJobTerminal)
	}
}

// committedRetryTarget returns the job a SUBMITTED retry of childJobID created, or ""
// when the child holds no such row. The highest attempt wins: a row that ran later is
// the one that carries the result.
func (s *Service) committedRetryTarget(childJobID string) string {
	rows, err := s.meta.ListRetriesByJob(childJobID)
	if err != nil {
		slog.Warn("child wake: list retries", "job_id", childJobID, "err", err)
		return ""
	}
	target, attempt := "", 0
	for _, r := range rows {
		if r.State != jobstore.RetryDone || r.NewJobID == "" {
			continue
		}
		if r.Attempt > attempt {
			target, attempt = r.NewJobID, r.Attempt
		}
	}
	return target
}

// childWakeTakeoverTarget reports the job that carries child's work after a takeover:
// an automatic continuation (AutoResumedBy) or a transfer to another agent (FellBackTo).
// Such a job ends WITHOUT a job.terminal event of its own, so a receipt that did not
// follow it would wait for an event that never comes.
func childWakeTakeoverTarget(child JobResult) string {
	if child.AutoResumedBy != "" {
		return child.AutoResumedBy
	}
	return child.FellBackTo
}
