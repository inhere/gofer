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
	"log/slog"
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
)

// planSweepInterval is the cadence of the plan progress scan (plan R2).
const planSweepInterval = 60 * time.Second

// planEventReplayBatch bounds one replay pass over the leftover rows.
const planEventReplayBatch = 200

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
	if ev.Kind != PlanEventSweep {
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
	err := s.processPlanEvent(q)
	h := &s.planEvents
	h.mu.Lock()
	delete(h.inflight, q.id)
	h.mu.Unlock()
	if err != nil {
		slog.Warn("plan event: advance failed, kept for replay", "plan_id", q.ev.PlanID, "kind", q.ev.Kind, "err", err)
		return
	}
	if q.id != 0 {
		if derr := s.meta.DeletePlanEvent(q.id); derr != nil {
			slog.Warn("plan event: delete", "id", q.id, "err", derr)
		}
	}
}

func (s *Service) processPlanEvent(q queuedPlanEvent) error {
	switch q.ev.Kind {
	case PlanEventAdvance:
		return s.doAdvancePlan(q.ev.PlanID, q.ev.By, false)
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
			s.emitPlanEvent(PlanEvent{PlanID: p.PlanID, Kind: PlanEventSweep}, nil)
		}
		if len(plans) < jobstore.PlanListMaxLimit {
			return nil
		}
	}
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
