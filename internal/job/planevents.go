package job

// planevents.go is the ONE entry of plan progression (gofer-eeod, plan R1/R2).
//
// Every trigger that may move a plan — a todo finished, a human's `plan run` / `resume` /
// `set-todo`, a failed chain job — goes through emitPlanEvent. It is write-ahead and
// synchronous: the event row is inserted first, the advance runs in the caller's own
// call (so "finished, then immediately advanced" keeps holding), and the row is deleted
// once the advance succeeded. A process that dies in between leaves the row, which the
// periodic scan (SweepPlanEvents) replays; an advance that fails keeps its row the same way.
//
// One handler per plan at a time. The advance can re-enter itself — advancePlan →
// MaybeDispatchTodo → a job that ends synchronously → linkTodoOutcome → advancePlan — and
// from other goroutines too, so a second caller never waits on the handler (that would
// deadlock the re-entrant case): it queues its event on the plan's gate and returns, and
// the running handler drains the queue before it lets go. Every queued event is therefore
// processed exactly once, in order, by the one handler of that plan.

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Plan event kinds.
const (
	// PlanEventAdvance moves the chain forward (a todo finished / changed, run, resume).
	PlanEventAdvance = "advance"
	// PlanEventBlock parks the plan on a failed chain job (carries JobID and TodoID).
	PlanEventBlock = "block"
	// PlanEventSweep is the periodic catch-up advance; it is never persisted.
	PlanEventSweep = "sweep"
	// PlanEventObserve tells the plan driver that a plan item's job reached a terminal
	// state that moves nothing by itself (needs_review, a done job whose todo stays open).
	// It advances and blocks nothing and, like a sweep, is never persisted: it is only a
	// notification, and one lost to a crash costs a missing observation, not progress.
	PlanEventObserve = "observe"
)

// PlanEventDroppedAudit is the audit_events kind written when a persisted plan event is
// dropped after planEventMaxAttempts failed runs (target = plan id).
const PlanEventDroppedAudit = "plan.event_dropped"

// planSweepInterval is the cadence of the plan progress scan (plan R2).
const planSweepInterval = 60 * time.Second

// planEventReplayBatch bounds one replay pass over the leftover rows.
const planEventReplayBatch = 200

// planEventMaxAttempts is how many failed runs a persisted event gets before it is
// dropped (with an audit row): a permanently failing advance must not retry forever.
const planEventMaxAttempts = 5

// PlanEvent is one trigger of plan progression. By is the authenticated caller the
// advance acts for (see jobstore.PlanEventRow); it is not part of the event's identity.
type PlanEvent struct {
	PlanID string
	TodoID string
	JobID  string
	Kind   string
	By     string
}

// queuedPlanEvent is an event waiting for its plan's handler. id is the plan_events row
// (0 = not persisted); snap is the in-memory job result of a block event (nil on replay,
// where it is rebuilt from the persisted job).
type queuedPlanEvent struct {
	id   int64
	ev   PlanEvent
	snap *JobResult
}

type planGate struct {
	busy    bool
	pending []queuedPlanEvent
}

// planEventHub is the per-plan serialisation state; the zero value is ready to use.
type planEventHub struct {
	mu       sync.Mutex
	gates    map[string]*planGate
	inflight map[int64]struct{} // persisted rows currently owned by a handler (queued or running)
}

// emitPlanEvent records ev and runs the progression it asks for (see the file comment).
func (s *Service) emitPlanEvent(ev PlanEvent, snap *JobResult) {
	var id int64
	if ev.Kind != PlanEventSweep && ev.Kind != PlanEventObserve {
		row, err := s.meta.InsertPlanEvent(jobstore.PlanEventRow{
			PlanID: ev.PlanID, TodoID: ev.TodoID, JobID: ev.JobID, Kind: ev.Kind, By: ev.By,
		})
		if err != nil {
			// Without the row the advance still runs; only the crash safety net is lost.
			slog.Warn("plan event: persist", "plan_id", ev.PlanID, "kind", ev.Kind, "err", err)
		} else {
			id = row
		}
	}
	s.enqueuePlanEvent(queuedPlanEvent{id: id, ev: ev, snap: snap})
}

// enqueuePlanEvent hands q to its plan's gate and, when no handler owns the gate, becomes
// the handler and drains it.
func (s *Service) enqueuePlanEvent(q queuedPlanEvent) {
	h := &s.planEvents
	h.mu.Lock()
	if h.gates == nil {
		h.gates = map[string]*planGate{}
		h.inflight = map[int64]struct{}{}
	}
	g := h.gates[q.ev.PlanID]
	if g == nil {
		g = &planGate{}
		h.gates[q.ev.PlanID] = g
	}
	g.pending = append(g.pending, q)
	if q.id != 0 {
		h.inflight[q.id] = struct{}{}
	}
	if g.busy {
		h.mu.Unlock()
		return
	}
	g.busy = true
	h.mu.Unlock()

	clean := false
	defer func() {
		if clean {
			return
		}
		// A panic in an advance: let go of the gate; the unprocessed rows stay for replay.
		h.mu.Lock()
		for _, p := range g.pending {
			delete(h.inflight, p.id)
		}
		g.pending, g.busy = nil, false
		delete(h.gates, q.ev.PlanID)
		h.mu.Unlock()
	}()
	for {
		h.mu.Lock()
		if len(g.pending) == 0 {
			g.busy = false
			delete(h.gates, q.ev.PlanID)
			h.mu.Unlock()
			clean = true
			return
		}
		batch := g.pending
		g.pending = nil
		h.mu.Unlock()
		for _, item := range batch {
			s.runPlanEvent(item)
		}
	}
}

// runPlanEvent executes one queued event and deletes its row on success.
func (s *Service) runPlanEvent(q queuedPlanEvent) {
	ticket := s.planDriverBefore(q)
	err := s.processPlanEvent(q)
	h := &s.planEvents
	h.mu.Lock()
	delete(h.inflight, q.id)
	h.mu.Unlock()
	if err != nil {
		s.planEventFailed(q, err)
		return
	}
	if q.id != 0 {
		if derr := s.meta.DeletePlanEvent(q.id); derr != nil {
			slog.Warn("plan event: delete", "id", q.id, "err", derr)
		}
	}
	s.planDriverAfter(q, ticket)
}

// planEventFailed counts a failed run of a persisted event; the row stays for replay until
// it has failed planEventMaxAttempts times, then it is dropped and audited.
func (s *Service) planEventFailed(q queuedPlanEvent, cause error) {
	if q.id == 0 {
		slog.Warn("plan event: advance failed", "plan_id", q.ev.PlanID, "kind", q.ev.Kind, "err", cause)
		return
	}
	n, err := s.meta.BumpPlanEventAttempts(q.id)
	if err != nil {
		slog.Warn("plan event: count attempt", "id", q.id, "err", err)
		return
	}
	if n < planEventMaxAttempts {
		slog.Warn("plan event: advance failed, kept for replay", "plan_id", q.ev.PlanID, "kind", q.ev.Kind, "attempt", n, "err", cause)
		return
	}
	slog.Warn("plan event: dropped after repeated failures", "plan_id", q.ev.PlanID, "kind", q.ev.Kind, "attempts", n, "err", cause)
	detail, _ := json.Marshal(map[string]any{"kind": q.ev.Kind, "todo_id": q.ev.TodoID, "job_id": q.ev.JobID, "attempts": n, "error": firstRunes(cause.Error(), maxTodoNoteErrorRunes)})
	if _, aerr := s.meta.AppendAuditEvent(PlanEventDroppedAudit, q.ev.PlanID, "", string(detail)); aerr != nil {
		slog.Warn("plan event: audit drop", "id", q.id, "err", aerr)
	}
	if derr := s.meta.DeletePlanEvent(q.id); derr != nil {
		slog.Warn("plan event: delete dropped row", "id", q.id, "err", derr)
	}
}

func (s *Service) processPlanEvent(q queuedPlanEvent) error {
	if hook := s.planEventHook; hook != nil {
		defer hook(q.ev)()
	}
	switch q.ev.Kind {
	case PlanEventAdvance:
		return s.doAdvancePlan(q.ev.PlanID, q.ev.By, false)
	case PlanEventObserve:
		return nil // the driver (runPlanEvent) is the only consumer
	case PlanEventSweep:
		p, ok, err := s.meta.GetPlan(q.ev.PlanID)
		if err != nil {
			return err
		}
		if !ok || p.Status != jobstore.PlanOpen || p.Paused {
			return nil
		}
		return s.doAdvancePlan(q.ev.PlanID, q.ev.By, true)
	case PlanEventBlock:
		snap := q.snap
		if snap == nil {
			rec, ok, err := s.meta.GetJob(q.ev.JobID)
			if err != nil {
				return err
			}
			if !ok {
				return nil // the job is gone; nothing left to park on
			}
			r := fromRecord(rec)
			snap = &r
		}
		return s.doBlockPlan(*snap)
	default:
		slog.Warn("plan event: unknown kind dropped", "kind", q.ev.Kind, "plan_id", q.ev.PlanID)
		return nil
	}
}

// ReplayPlanEvents re-runs the events a previous process (or a failed advance) left in
// plan_events, skipping rows a live handler already owns. It returns how many it ran.
func (s *Service) ReplayPlanEvents() (int, error) {
	rows, err := s.meta.ListPlanEvents(planEventReplayBatch)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		h := &s.planEvents
		h.mu.Lock()
		_, live := h.inflight[r.ID]
		h.mu.Unlock()
		if live {
			continue
		}
		s.enqueuePlanEvent(queuedPlanEvent{id: r.ID, ev: PlanEvent{
			PlanID: r.PlanID, TodoID: r.TodoID, JobID: r.JobID, Kind: r.Kind, By: r.By,
		}})
		n++
	}
	return n, nil
}

// SweepPlans is one scan pass (plan R2): replay leftover event rows, then deliver a sweep
// event to every open, unpaused, non-blocked plan. All of it is idempotent — the advance
// only touches items that are still pending and moves them with a conditional update.
func (s *Service) SweepPlans() error {
	if _, err := s.ReplayPlanEvents(); err != nil {
		return err
	}
	for offset := 0; ; offset += jobstore.PlanListMaxLimit {
		plans, err := s.meta.ListPlans(jobstore.PlanFilter{
			Status: jobstore.PlanOpen, Limit: jobstore.PlanListMaxLimit, Offset: offset,
		})
		if err != nil {
			return err
		}
		for _, p := range plans {
			if p.Paused {
				continue
			}
			s.emitPlanEvent(PlanEvent{PlanID: p.PlanID, Kind: PlanEventSweep, By: planSweepIdentity(p)}, nil)
		}
		if len(plans) < jobstore.PlanListMaxLimit {
			return nil
		}
	}
}

// planSweepIdentity is the caller identity a sweep acts for: the plan's owner, which is
// what dispatch admission compares against for a supervisor-bound plan (plan.Owner is
// stamped from the authenticated creator). A bound plan without an owner cannot be
// dispatched by anyone but itself, so the sweep says so instead of failing silently.
func planSweepIdentity(p jobstore.Plan) string {
	if p.Owner == "" && p.SupervisorSessionID != "" {
		slog.Warn("plan sweep: supervisor-bound plan has no owner; dispatch will be refused", "plan_id", p.PlanID)
	}
	return p.Owner
}

// StartPlanSweeper runs SweepPlans once now and then every interval (<=0 uses the 60s
// default) on a tracked background goroutine: Shutdown stops it and waits for it (G051).
// It returns false when the service is already shutting down.
func (s *Service) StartPlanSweeper(interval time.Duration) bool {
	if interval <= 0 {
		interval = planSweepInterval
	}
	return s.GoBackground(func() {
		closing := s.bg.closingCh()
		run := func() {
			if err := s.SweepPlans(); err != nil {
				slog.Warn("plan sweep failed", "err", err)
			}
		}
		run()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-closing:
				return
			case <-t.C:
				run()
			}
		}
	})
}

// dispatchStrandedReady is the sweep's second catch-up (plan R2): an item that was queued
// (`ready`) but never got its job — the process died between the two writes — is
// dispatched through the ordinary entry. Only an item the chain would have started itself
// qualifies (assignee, auto), with no live job and no recorded dispatch failure (a failed
// attempt is a human's business, and retrying it every pass would only repeat the event).
// A job that ran and failed leaves its item `doing`, never `ready`, so a failed item is not
// re-run here; MaybeDispatchTodo re-checks status and live jobs itself.
func (s *Service) dispatchStrandedReady(p jobstore.Plan, todos []jobstore.PlanTodo, by string) {
	for _, t := range todos {
		if t.Status != jobstore.TodoReady || t.Assignee == "" || !t.Auto || t.DispatchError != "" {
			continue
		}
		if _, busy := s.todoActiveJob(t.TodoID); busy {
			continue
		}
		if _, err := s.MaybeDispatchTodo(t.TodoID, by); err != nil {
			slog.Warn("plan sweep: dispatch ready todo", "plan_id", p.PlanID, "todo_id", t.TodoID, "err", err)
		}
	}
}

// relinkFinishedDoing is the sweep's third catch-up (plan R2): an item still `doing` whose
// job has already reached a terminal state never had its outcome linked — a serve restart
// fails the orphaned local job in the store without running finish(), so neither
// linkTodoOutcome nor maybeBlockPlan ran and the plan would stall silently. The outcome is
// linked through the same path a normal ending takes, from the persisted row.
//
// Idempotent: an item whose note already names the job was linked (every outcome line
// carries the job id), and a block parks the plan, which the scan no longer visits.
// needs_review is not terminal here; a taken-over job follows maybeBlockPlan's own skip.
func (s *Service) relinkFinishedDoing(todos []jobstore.PlanTodo) {
	for _, t := range todos {
		if t.Status != jobstore.TodoDoing || t.JobID == "" {
			continue
		}
		rec, ok, err := s.meta.GetJob(t.JobID)
		if err != nil || !ok {
			continue
		}
		switch rec.Status {
		case StatusDone, StatusFailed, StatusCancelled, StatusTimeout, StatusRejected:
		default:
			continue
		}
		if strings.Contains(t.Note, rec.ID) {
			continue
		}
		snap := fromRecord(rec)
		slog.Info("plan sweep: relinking the outcome of a finished job", "plan_id", t.PlanID, "todo_id", t.TodoID, "job_id", rec.ID, "status", rec.Status)
		s.linkTodoOutcome(snap, false)
		if rec.Status != StatusDone {
			s.maybeBlockPlan(snap)
		}
	}
}
