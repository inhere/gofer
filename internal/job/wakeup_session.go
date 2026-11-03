package job

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
)

// Child-receipt say path (design 2026-11-03-resident-job-child-wakeup §总体方案).
//
// A wakeup whose target is a LIVE resident ACP job is not continued (resume would
// start a second process for a session that is still there): the receipt is SPOKEN
// into the session, opening the next turn. When the session is in the middle of a
// turn the receipt is parked as "pending delivery" (the `await_input` sentinel on the
// wakeup row) and delivered the moment the session comes back to awaiting_input.
//
// Everything that touches one parent job's receipts runs under that parent's receipt
// lock (wakeupSayLock), in a FIXED order: the receipt lock first, entry.mu second.
// That lock is what makes "read the pending set, merge it, send one say, remember the
// batch" a single step, so two children finishing at the same moment produce one
// message and a confirmation cannot race the send it belongs to.
//
// Delivery is confirmed by the session itself: a say only proves it was written out
// (a worker frame, a bounded channel buffer), so a batch counts as delivered once the
// session is back in awaiting_input with a HIGHER turn number than the one it was sent
// on. Only that batch is cleared, and a child whose state changed in between keeps its
// sentinel so the changed outcome still reaches the parent.

const (
	// childWakeNeedsInputEverySec is the floor between two "needs your input" receipts
	// of ONE child job: a child can raise tool approvals in a burst, and each one costs
	// the parent a full turn (design 2026-11-03 回执事件 限频). A rate-limited receipt
	// stays pending — the tick (T9) re-delivers it once the window is over, and the next
	// idle transition picks it up meanwhile.
	childWakeNeedsInputEverySec = 60
	// childWakeQueueMarginSec is the slack a child receipt's TTL adds on top of the
	// child job's own limits: the job may wait for a directory lock or a concurrency
	// slot before it starts (server.dir_lock_max_wait_sec defaults to 1h).
	childWakeQueueMarginSec = 3600
	// childWakeEntryWait bounds how long the receipt path waits for the child's
	// in-process entry to close. job.terminal is recorded a few lines BEFORE the status
	// flips (execute's E13 note), so reading the row without waiting would read the
	// state from before the job ended.
	childWakeEntryWait = wakeupTerminalWait
	// childWakeReasonParentEnded / childWakeReasonExpired are the MACHINE values a receipt
	// reports when it could not reach its parent (the alert's detail.reason): the inbox and
	// the notification text map them to their own wording, so a reader never sees a gofer
	// sentence in place of a cause. expired is the TTL sweep's reason (T9 uses it).
	childWakeReasonParentEnded = "parent_ended"
	childWakeReasonExpired     = "expired"
)

// wakeupSayItem is one child receipt inside a delivered batch.
type wakeupSayItem struct {
	// wakeupID is the row whose sentinel this item clears on confirmation.
	wakeupID string
	// childJobID / status / exitCode are what the message says and what the
	// confirmation compares against (a child that changed state in between keeps its
	// sentinel).
	childJobID string
	status     string
	exitCode   int
	// needsInput marks the "you cannot answer for it, tell a person" class (a tool
	// approval / a question), which is also the rate-limited class.
	needsInput bool
	// mode is the row's mode: a once row is consumed by its first delivery.
	mode string
}

// wakeupSayBatch is the batch a parent session is working through.
type wakeupSayBatch struct {
	// turnNo is the session's turn number when the say was sent. Coming back to
	// awaiting_input with a HIGHER number is the delivery proof.
	turnNo int
	items  []wakeupSayItem
}

// wakeupSayState is the process-local receipt state of one parent job.
type wakeupSayState struct {
	mu sync.Mutex
	// batch is the last delivered, not yet confirmed batch (nil = none).
	batch *wakeupSayBatch
	// pending marks the parent as having receipts waiting for delivery, so an idle
	// transition of a parent that has none costs nothing.
	pending bool
	// lastNeedsInput is when this parent was last woken for a given child's "needs
	// input" receipt (the rate window). In memory by design: a restart may send one
	// extra receipt, which is harmless, while a lost window is not.
	lastNeedsInput map[string]int64
	// retriggered holds the receipt rows that fired again AFTER the current batch was
	// spoken. Their sentinel must survive that batch's confirmation: the child's state
	// may not have changed (a second tool approval on a running job looks identical), so
	// a status comparison alone would silently swallow the newer receipt.
	retriggered map[string]bool
}

// wakeupSayLock returns the receipt lock of one parent job, creating it on first use.
// The lock is never removed: the set is bounded by the resident parents this process
// ever served a receipt for, which is the number of supervising jobs, not of jobs.
func (s *Service) wakeupSayLock(parentJobID string) *sync.Mutex {
	if v, ok := s.wakeupSayLocks.LoadOrStore(parentJobID, &sync.Mutex{}); ok {
		return v.(*sync.Mutex)
	}
	v, _ := s.wakeupSayLocks.Load(parentJobID)
	return v.(*sync.Mutex)
}

// wakeupSayOf returns the receipt state of one parent job, creating it on first use.
func (s *Service) wakeupSayOf(parentJobID string) *wakeupSayState {
	fresh := &wakeupSayState{lastNeedsInput: make(map[string]int64, 2), retriggered: map[string]bool{}}
	if v, ok := s.wakeupSayStates.LoadOrStore(parentJobID, fresh); ok {
		return v.(*wakeupSayState)
	}
	return fresh
}

// hasWakeupSayWork reports whether this parent has anything the say path could act on
// (an unconfirmed batch or receipts waiting for delivery). The idle hook asks first so
// a resident session that is nobody's parent costs no query and no lock.
func (s *Service) hasWakeupSayWork(parentJobID string) bool {
	v, ok := s.wakeupSayStates.Load(parentJobID)
	if !ok {
		return false
	}
	st := v.(*wakeupSayState)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.batch != nil || st.pending
}

// residentWakeupTarget reports whether w's target is a resident ACP session job, i.e.
// a wakeup that must go through the say path instead of resume/rebuild. Only EVENT
// wakeups are routed: a timer on a live session keeps the behaviour it always had
// (design 2026-11-03 非目标).
func (s *Service) residentWakeupTarget(w jobstore.WakeupRecord) (JobResult, bool) {
	if w.Kind != jobstore.WakeupKindEvent {
		return JobResult{}, false
	}
	res, ok := s.Get(w.JobID)
	if !ok || !res.Session {
		return JobResult{}, false
	}
	return res, true
}

// routeChildReceipt is dispatchWakeup's say branch: it decides — from the PARENT's
// state — whether this event becomes a spoken receipt, a receipt waiting for the
// session to go idle, or a parent-end report. handled=false means the caller keeps
// its historical behaviour (resume / rebuild), which is what a manually registered
// wakeup on an already-ended job still gets.
//
// The terminal and review events are recorded BEFORE the status flips, so their
// receipt waits for the CHILD's in-process entry to close and only then reads the
// state it reports. The entry is taken HERE, synchronously, because it is evicted
// once the job ends.
func (s *Service) routeChildReceipt(w jobstore.WakeupRecord, reason string, parent JobResult) (handled bool) {
	if IsFinished(parent.Status) {
		if w.AutoChild != 1 {
			return false // a manual wakeup on an ended job: resume / rebuild, unchanged
		}
		// The parent is gone: the receipt cannot be spoken. Report it once, to a person.
		s.GoBackground(func() { s.endParentChildWakes(w.JobID, childWakeReasonParentEnded) })
		return true
	}
	var child *jobEntry
	if flipsJobState(reason) {
		child = s.entry(w.FilterJobID)
	}
	s.GoBackground(func() {
		s.waitChildEntry(child)
		s.deliverChildReceipt(w, reason)
	})
	return true
}

// flipsJobState reports whether reason is one of the events recorded before the job's
// own status moves (terminal / needs_review), i.e. the ones whose receipt must wait for
// the child's entry to close. interaction.created and job.awaiting_approval do not.
func flipsJobState(reason string) bool {
	return reason == EventJobTerminal || reason == EventJobNeedsReview
}

// waitChildEntry blocks until the child's in-process entry has closed (its finish ran),
// bounded so a receipt never hangs on a job another process owns.
func (s *Service) waitChildEntry(entry *jobEntry) {
	if entry == nil {
		return
	}
	timer := time.NewTimer(childWakeEntryWait)
	defer timer.Stop()
	select {
	case <-entry.done:
	case <-timer.C:
	}
}

// isNeedsInputReason reports whether reason belongs to the "the child needs a person"
// class (a tool approval or a question). It is the TRIGGER side of that class only; what
// a receipt SAYS about it is decided from the child's own state (see childNeedsInput).
func isNeedsInputReason(reason string) bool {
	return reason == EventInteractionCreated || reason == EventJobAwaitingApproval
}

// deliverChildReceipt parks the trigger durably and delivers it as soon as the parent
// session is idle. It runs on its own goroutine (see routeChildReceipt): a receipt must
// never block the child's finish or the hub's read loop.
//
// The sentinel is written FIRST, before the parent is looked at and before any rate
// limit: a receipt that is refused now (a turn in progress, a burst of approvals, an
// offline worker) must be durable, or the next confirmation would clear a slot nothing
// ever filled.
func (s *Service) deliverChildReceipt(w jobstore.WakeupRecord, reason string) {
	lock := s.wakeupSayLock(w.JobID)
	lock.Lock()
	defer lock.Unlock()

	st := s.wakeupSayOf(w.JobID)
	st.mu.Lock()
	st.pending = true
	if st.batch != nil {
		// A batch is already spoken and not yet confirmed: this trigger arrived after it,
		// so the confirmation of that batch must not clear THIS row's sentinel.
		st.retriggered[w.ID] = true
	}
	st.mu.Unlock()

	// Write the receipt BEFORE looking at the parent again: a parent that goes idle in
	// between must find the receipt waiting, not lose it (扫描兜底 is the other half).
	if err := s.meta.MarkWakeupPendingSay(w.ID); err != nil {
		slog.Warn("wakeup pending say: mark", "wakeup_id", w.ID, "job_id", w.JobID, "err", err)
		return
	}
	s.flushPendingSaysLocked(w.JobID, &w, reason)
}

// flushPendingSaysLocked merges every pending receipt of parentID into ONE say and
// sends it when the session is idle. Callers hold the parent's receipt lock.
//
// trigger is the row the current call came from (nil on the idle hook, where every
// pending row is already known), so the caller can tell whether its own receipt was
// delivered — that is what the needs-input rate clock is advanced from.
func (s *Service) flushPendingSaysLocked(parentID string, trigger *jobstore.WakeupRecord, reason string) bool {
	st := s.wakeupSayOf(parentID)
	rows, err := s.meta.ListPendingSay()
	if err != nil {
		slog.Warn("wakeup say: list pending", "job_id", parentID, "err", err)
		return false
	}
	items := make([]wakeupSayItem, 0, len(rows))
	instruction := ""
	delivered := false
	deferred := false
	for _, w := range rows {
		if w.JobID != parentID {
			continue
		}
		child, ok := s.Get(w.FilterJobID)
		if !ok {
			// The child row is gone (pruned): nothing truthful to report.
			continue
		}
		needsInput := s.childNeedsInput(child)
		if needsInput && s.withinNeedsInputWindow(st, child.ID) {
			// Rate limited: this child already woke the parent for a "needs a person"
			// receipt inside the window. The sentinel stays, and the tick re-delivers it
			// once the window is over (terminal / needs_review receipts are never limited).
			deferred = true
			slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "wakeup_id", w.ID,
				"job_id", parentID, "child_job_id", child.ID, "reason", "rate_limited")
			continue
		}
		if s.hasPendingRetry(child.ID) {
			// A durable retry is on its way: this outcome is not the child's last word, so the
			// parent is not woken for it and the row stays armed. retargetChildWakes moves the
			// row onto the retry's own job, whose outcome then fires (or is reported by
			// reportAlreadyEndedChild when it was already over).
			deferred = true
			slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "wakeup_id", w.ID,
				"job_id", parentID, "child_job_id", child.ID, "reason", "retry_pending")
			continue
		}
		items = append(items, wakeupSayItem{
			wakeupID:   w.ID,
			childJobID: child.ID,
			status:     child.Status,
			exitCode:   child.ExitCode,
			needsInput: needsInput,
			mode:       w.Mode,
		})
		if strings.TrimSpace(instruction) == "" && strings.TrimSpace(w.Instruction) != "" {
			// A manual wakeup's instruction rides the receipt as the caller's own text
			// (design 总体方案: 手动 wakeup 的 instruction 附在模板后).
			instruction = strings.TrimSpace(w.Instruction)
		}
	}
	if len(items) == 0 {
		st.mu.Lock()
		st.pending = deferred // a deferred receipt is still work waiting for its window
		st.mu.Unlock()
		return false
	}

	entry := s.entry(parentID)
	if entry == nil {
		s.settlePendingWithoutSessionLocked(parentID)
		return false
	}
	entry.mu.Lock()
	status := entry.result.Status
	ending := entry.sessionEnding
	turnNo := entry.result.TurnNo
	entry.mu.Unlock()
	if ending {
		s.endParentChildWakesLocked(parentID, childWakeReasonParentEnded)
		return false
	}
	if status != StatusAwaitingInput {
		// A turn is in progress: the receipt waits for the idle transition.
		slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "job_id", parentID,
			"count", len(items), "reason", reason, "status", status)
		return false
	}

	text := s.childReceiptText(parentID, items, instruction)
	if err := s.saySessionCommand(parentID, text, nil, sessionCommandID(parentID)); err != nil {
		if errors.Is(err, ErrSessionEnding) {
			s.endParentChildWakesLocked(parentID, childWakeReasonParentEnded)
			return false
		}
		if errors.Is(err, ErrNoLiveSession) {
			s.settlePendingWithoutSessionLocked(parentID)
			return false
		}
		// Turn busy / already queued / worker offline / draining / anything unforeseen:
		// the receipt stays pending and is retried by the idle hook or the tick.
		slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "job_id", parentID,
			"count", len(items), "reason", reason, "err", err)
		return false
	}

	now := s.nowFn().Unix()
	st.mu.Lock()
	st.batch = &wakeupSayBatch{turnNo: turnNo, items: items}
	st.pending = deferred
	// A new batch starts a new confirmation window: only triggers that arrive AFTER it
	// was spoken are protected from its confirmation.
	st.retriggered = map[string]bool{}
	for _, it := range items {
		if it.needsInput {
			st.lastNeedsInput[it.childJobID] = now
		}
	}
	st.mu.Unlock()
	if trigger != nil {
		for _, it := range items {
			if it.wakeupID == trigger.ID {
				delivered = true
			}
		}
	}
	slog.Info("wakeup.say_sent", "event", "wakeup.say_sent", "job_id", parentID,
		"count", len(items), "turn_no", turnNo, "reason", reason)
	return delivered
}

// settlePendingWithoutSessionLocked handles the pending receipts of a parent whose
// session this process cannot speak to: a recovering parent keeps them (the session
// comes back), a parent waiting for review or already ended reports them to a person.
// Anything else keeps them too — a `gofer` restart in the middle of a turn looks the
// same from here, and dropping a receipt because of a transient state would lose it.
func (s *Service) settlePendingWithoutSessionLocked(parentID string) {
	res, ok := s.Get(parentID)
	if !ok {
		s.endParentChildWakesLocked(parentID, childWakeReasonParentEnded)
		return
	}
	if res.Status == StatusRecovering {
		slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "job_id", parentID,
			"reason", "parent_recovering")
		return
	}
	if IsFinished(res.Status) {
		s.endParentChildWakesLocked(parentID, childWakeReasonParentEnded)
		return
	}
	slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "job_id", parentID,
		"reason", "no_live_session", "status", res.Status)
}

// childNeedsInput reports whether the child needs a PERSON right now (design 回执事件
// 需要输入类): it is still alive, not waiting for review, and either parked on a hold or
// carrying an unanswered interaction. The trigger REASON is deliberately not part of the
// test — a row that subscribes to interaction.created still gets terminal receipts for a
// child that then finished, and those must not claim the parent could not answer for it.
func (s *Service) childNeedsInput(child JobResult) bool {
	if IsFinished(child.Status) {
		return false // terminal or needs_review: the receipt reports the outcome, not a question
	}
	if child.Status == StatusAwaitingApproval {
		return true
	}
	recs, err := s.meta.ListInteractions(child.ID)
	if err != nil {
		slog.Warn("child wake: list interactions", "job_id", child.ID, "err", err)
		return false
	}
	for _, rec := range recs {
		if rec.Status == InteractionPending {
			return true
		}
	}
	return false
}

// withinNeedsInputWindow reports whether childID already woke this parent for a
// "needs a person" receipt inside the rate window (design 回执事件 限频).
func (s *Service) withinNeedsInputWindow(st *wakeupSayState, childJobID string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	at, ok := st.lastNeedsInput[childJobID]
	return ok && s.nowFn().Unix()-at < childWakeNeedsInputEverySec
}

// onParentAwaitingInput is the idle-transition hook shared by both ways a resident
// session becomes idle again (the local runner's awaitSessionInput and the worker's
// applyRemoteSessionState): it confirms the batch the session just worked through and
// then delivers whatever is waiting. It does nothing for a parent with no receipt work,
// so every other resident session pays one map lookup.
func (s *Service) onParentAwaitingInput(parentJobID string) {
	if parentJobID == "" || !s.hasWakeupSayWork(parentJobID) {
		return
	}
	lock := s.wakeupSayLock(parentJobID)
	lock.Lock()
	defer lock.Unlock()
	s.confirmSayBatchLocked(parentJobID)
	s.flushPendingSaysLocked(parentJobID, nil, EventJobAwaitingInput)
}

// confirmSayBatchLocked clears the sentinel of the batch the session has now worked
// through. Callers hold the parent's receipt lock.
//
// The proof of delivery is the session's own turn counter: awaiting_input with a turn
// number ABOVE the one the say was sent on means the agent consumed the prompt. A turn
// that dies in the middle never returns to awaiting_input, so it is never mistaken for
// a delivered receipt. Only this batch is touched, and a child whose state changed
// since the send keeps its sentinel — otherwise the newer outcome (a terminal receipt
// arriving while an approval receipt was on its way) would be lost.
func (s *Service) confirmSayBatchLocked(parentJobID string) {
	st := s.wakeupSayOf(parentJobID)
	st.mu.Lock()
	batch := st.batch
	st.mu.Unlock()
	if batch == nil {
		return
	}
	entry := s.entry(parentJobID)
	if entry == nil {
		return
	}
	entry.mu.Lock()
	status := entry.result.Status
	turnNo := entry.result.TurnNo
	entry.mu.Unlock()
	if status != StatusAwaitingInput || turnNo <= batch.turnNo {
		return
	}
	for _, it := range batch.items {
		st.mu.Lock()
		again := st.retriggered[it.wakeupID]
		st.mu.Unlock()
		if again {
			// The row fired again after this batch was spoken: its sentinel belongs to that
			// newer receipt, which the parent has not seen yet.
			st.mu.Lock()
			st.pending = true
			st.mu.Unlock()
			continue
		}
		child, ok := s.Get(it.childJobID)
		if !ok || child.Status != it.status {
			continue // the child moved on: keep the sentinel so the new state fires again
		}
		disable := IsTerminal(child.Status) || it.mode == jobstore.WakeupModeOnce
		if err := s.meta.CompleteWakeupSay(it.wakeupID, disable); err != nil {
			slog.Warn("wakeup say: confirm", "wakeup_id", it.wakeupID, "job_id", parentJobID, "err", err)
		}
	}
	st.mu.Lock()
	st.batch = nil
	st.retriggered = map[string]bool{}
	st.mu.Unlock()
}

// childReceiptText renders the receipt: child id, state, exit code, the command that
// shows the result, and (for the "needs a person" class) the line saying the parent
// cannot answer for the child. It never carries the child's title or output — a child
// controls its own title, so quoting it would let a child inject text into the prompt
// of the job that supervises it (design 安全).
func (s *Service) childReceiptText(parentJobID string, items []wakeupSayItem, instruction string) string {
	lang := i18n.Default
	if parent, ok := s.Get(parentJobID); ok {
		lang = s.notifyLang(parent.ProjectKey)
	}
	lines := make([]prompts.ChildReceiptLine, 0, len(items))
	needsInput := false
	for _, it := range items {
		if it.needsInput {
			needsInput = true
		}
		lines = append(lines, prompts.ChildReceiptLine{
			JobID: it.childJobID, Status: it.status, ExitCode: it.exitCode, NeedsInput: it.needsInput,
		})
	}
	body := prompts.MustRender(lang, prompts.NameChildReceipt, prompts.ChildReceiptData{
		Children: lines, NeedsInput: needsInput, Instruction: instruction,
	})
	return msgprefix.FormatIn(lang, msgprefix.Wakeup, "", body)
}

// endParentChildWakes retires every child receipt of a parent job that is going away,
// so nothing silently waits for a session that will never speak again (design 关键流程:
// 父 job 结束). It reports each receipt that never reached the parent once
// (job.child_wake_failed, the event the notification channels carry), disables the
// manual "tell me when it is done" receipts still waiting for delivery with the
// existing job.wakeup_failed, and never resumes the parent.
//
// Called from the parent's end paths (T7) and from the receipt path itself when it
// finds the parent already finished.
func (s *Service) endParentChildWakes(parentJobID, reason string) {
	if parentJobID == "" {
		return
	}
	lock := s.wakeupSayLock(parentJobID)
	lock.Lock()
	defer lock.Unlock()
	s.endParentChildWakesLocked(parentJobID, reason)
}

// retargetChildWakes moves every receipt that watches fromJobID onto toJobID, the job
// that now carries the child's outcome (design 2026-11-03 关键流程: 子 job 接管链). A
// takeover — the same agent's continuation, a transfer to another agent, a delivery-check
// continuation, an uncommitted-work continuation — and a durable retry all submit a NEW
// job id, and a receipt left on the old one would never fire again: that job ends without
// a job.terminal event (a takeover) or with one that was already spent.
//
// The move is a per-row CAS (RetargetWakeupFilter): the event path and the retry sweep can
// both arrive here for the same row, and exactly one move is the truth. Right after a row
// moves, the new job is read once more — Submit starts it immediately, so it may have
// ENDED before the CAS landed, and no further event will fire for it then. That is the one
// case this function has to report itself.
func (s *Service) retargetChildWakes(fromJobID, toJobID string) {
	if fromJobID == "" || toJobID == "" || fromJobID == toJobID {
		return
	}
	rows, err := s.meta.ListEventWakeupsByChild(fromJobID)
	if err != nil {
		slog.Warn("child wake: list receipts to retarget", "job_id", fromJobID, "to_job_id", toJobID, "err", err)
		return
	}
	for _, w := range rows {
		moved, merr := s.meta.RetargetWakeupFilter(w.ID, fromJobID, toJobID)
		if merr != nil {
			slog.Warn("child wake: retarget receipt", "wakeup_id", w.ID,
				"job_id", fromJobID, "to_job_id", toJobID, "err", merr)
			continue
		}
		if !moved {
			continue // another path already moved (or consumed) this row
		}
		w.FilterJobID = toJobID
		s.reportAlreadyEndedChild(w)
	}
}

// reportAlreadyEndedChild is the 补查 after a retarget: when the job the receipt now
// watches has already finished, its own events are in the past and nothing else would
// wake the parent. dispatchWakeup re-reads the state and routes the receipt exactly as a
// live event would (say when the parent is a resident session, the parent-end report when
// it is gone), which is why this path invents no reporting of its own.
func (s *Service) reportAlreadyEndedChild(w jobstore.WakeupRecord) {
	child, ok := s.Get(w.FilterJobID)
	if !ok || !IsFinished(child.Status) {
		return // still running: its own events will report it
	}
	slog.Info("wakeup.say_pending", "event", "wakeup.say_pending", "wakeup_id", w.ID,
		"job_id", w.JobID, "child_job_id", child.ID, "reason", "retargeted_child_ended")
	s.dispatchWakeup(w, EventJobTerminal)
}

// hasPendingRetry reports whether a durable retry of childJobID is still waiting to run
// (pending, or claimed by a sweeper). While one is, the child's outcome is not the last
// word: its receipt is held back instead of waking the parent for a result the retry
// replaces (design 关键流程: 子 job 重试 先查有没有待执行的重试：有就不发也不停用).
func (s *Service) hasPendingRetry(childJobID string) bool {
	rows, err := s.meta.ListRetriesByJob(childJobID)
	if err != nil {
		// A failed read must not swallow a receipt: report the child instead.
		slog.Warn("child wake: list retries", "job_id", childJobID, "err", err)
		return false
	}
	for _, r := range rows {
		if r.State == jobstore.RetryPending || r.State == jobstore.RetryClaimed {
			return true
		}
	}
	return false
}

// deliverRetryCancelledReceipts reports a child whose retry was CANCELLED: the retry will
// never run, so the failure it was going to replace is the child's last word after all and
// its receipt goes out now. The four cancelling paths call this (an undecodable request, a
// missing ceiling owner, a refused admission and `job retry cancel`); a retry that could
// not be SUBMITTED is merely late and must stay quiet (design 关键流程: 子 job 重试).
func (s *Service) deliverRetryCancelledReceipts(sourceJobID string) {
	if sourceJobID == "" {
		return
	}
	rows, err := s.meta.ListEventWakeupsByChild(sourceJobID)
	if err != nil {
		slog.Warn("child wake: list receipts of a cancelled retry", "job_id", sourceJobID, "err", err)
		return
	}
	for _, w := range rows {
		s.dispatchWakeup(w, EventJobTerminal)
	}
}

// endParentChildWakesLocked is endParentChildWakes' body; callers hold the parent's
// receipt lock.
func (s *Service) endParentChildWakesLocked(parentJobID, reason string) {
	s.wakeupSayStates.Delete(parentJobID)
	rows, err := s.meta.ListAutoChildByParent(parentJobID)
	if err != nil {
		slog.Warn("child wake: list auto receipts", "job_id", parentJobID, "err", err)
		return
	}
	for _, w := range rows {
		if w.Enabled != 1 {
			continue // already receipted, or already reported on an earlier end
		}
		if err := s.meta.SetWakeupEnabled(w.ID, 0); err != nil {
			slog.Warn("child wake: disable receipt", "wakeup_id", w.ID, "job_id", parentJobID, "err", err)
			continue
		}
		s.recordEvent(parentJobID, EventJobChildWakeFailed, map[string]any{
			"wakeup_id": w.ID, "child_job_id": w.FilterJobID, "reason": reason,
		})
	}
	// A manually registered event wakeup parked for the same parent loses its delivery
	// too. It is not an automatic receipt, so it keeps the event it always had.
	pending, err := s.meta.ListPendingSay()
	if err != nil {
		slog.Warn("child wake: list pending says", "job_id", parentJobID, "err", err)
		return
	}
	for _, w := range pending {
		if w.JobID != parentJobID || w.AutoChild == 1 {
			continue
		}
		if err := s.meta.SetWakeupEnabled(w.ID, 0); err != nil {
			slog.Warn("child wake: disable pending manual wakeup", "wakeup_id", w.ID, "job_id", parentJobID, "err", err)
			continue
		}
		s.recordEvent(parentJobID, EventJobWakeupFailed, map[string]any{
			"wakeup_id": w.ID, "kind": w.Kind, "reason": reason, "error": "receipt could not be delivered",
		})
	}
}
