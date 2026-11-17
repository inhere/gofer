package job

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// markTodoDone writes a finished item without going through a job, the way a human's
// `plan set-todo --status done` does before it notifies the plan.
func markTodoDone(t *testing.T, s *Service, todoID string) {
	t.Helper()
	if ok, err := s.Meta().UpdateTodoStatus(todoID, jobstore.TodoDone, nil); err != nil || !ok {
		t.Fatalf("mark %s done: ok=%v err=%v", todoID, ok, err)
	}
}

// waitNoPlanEvents waits for the event rows to drain: the last job's own terminal advance
// is still in flight when its todo already reads done.
func waitNoPlanEvents(t *testing.T, s *Service) {
	t.Helper()
	wait.For(t, 10*time.Second, "plan_events rows drained", func() (bool, any) {
		rows := pendingPlanEvents(t, s)
		return len(rows) == 0, rows
	})
}

func pendingPlanEvents(t *testing.T, s *Service) []jobstore.PlanEventRow {
	t.Helper()
	rows, err := s.Meta().ListPlanEvents(100)
	if err != nil {
		t.Fatalf("list plan events: %v", err)
	}
	return rows
}

// TestPlanEventDuplicateDoesNotRedispatch: the same trigger delivered many times (and a
// sweep on top) queues and dispatches the dependent item exactly once, and every
// processed row is gone afterwards.
func TestPlanEventDuplicateDoesNotRedispatch(t *testing.T) {
	t.Parallel()
	s := newChainService(t, t.TempDir())
	seedChain(t, s, "plan-ev-dup",
		chainTodo("todo-d1", "first", "ok"),
		chainTodo("todo-d2", "second", "ok", "todo-d1"),
	)
	markTodoDone(t, s, "todo-d1")
	for i := 0; i < 3; i++ {
		s.PlanTodoChanged("todo-d1", jobstore.TodoDone, "alice")
	}
	if err := s.SweepPlans(); err != nil {
		t.Fatalf("SweepPlans: %v", err)
	}
	waitTodoStatus(t, s, "todo-d2", jobstore.TodoDone, 30*time.Second)
	if n := countPlanEvent(t, s, "plan-ev-dup", EventPlanTodoAdvanced); n != 1 {
		t.Fatalf("plan.todo_advanced events = %d, want exactly 1", n)
	}
	waitNoPlanEvents(t, s)
}

// TestPlanEventConcurrentSerialised: many goroutines fire the same advance at once; the
// plan ends in the right state (dependent queued once, plan done), nothing is queued
// twice and no row survives.
func TestPlanEventConcurrentSerialised(t *testing.T) {
	t.Parallel()
	s := newChainService(t, t.TempDir())
	seedChain(t, s, "plan-ev-conc",
		chainTodo("todo-c1", "first", "ok"),
		chainTodo("todo-c2", "second", "ok", "todo-c1"),
	)
	markTodoDone(t, s, "todo-c1")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.PlanTodoChanged("todo-c1", jobstore.TodoDone, "alice")
		}()
	}
	wg.Wait()
	if got := waitPlanStatus(t, s, "plan-ev-conc", jobstore.PlanDone, 30*time.Second); got.Status != jobstore.PlanDone {
		t.Fatalf("plan status = %q, want done", got.Status)
	}
	if n := countPlanEvent(t, s, "plan-ev-conc", EventPlanTodoAdvanced); n != 1 {
		t.Fatalf("plan.todo_advanced events = %d, want exactly 1", n)
	}
	waitNoPlanEvents(t, s)
	wait.Until(t, 10*time.Second, "gate state released", func() bool {
		s.planEvents.mu.Lock()
		defer s.planEvents.mu.Unlock()
		return len(s.planEvents.gates) == 0 && len(s.planEvents.inflight) == 0
	})
}

// TestPlanEventReplayAfterRestart: a row written before the process died (the advance
// never ran) is picked up by a freshly built Service's scan, which moves the plan on.
func TestPlanEventReplayAfterRestart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := newChainService(t, root)
	seedChain(t, old, "plan-ev-crash",
		chainTodo("todo-r1", "first", "ok"),
		chainTodo("todo-r2", "second", "ok", "todo-r1"),
	)
	markTodoDone(t, old, "todo-r1")
	if _, err := old.Meta().InsertPlanEvent(jobstore.PlanEventRow{
		PlanID: "plan-ev-crash", TodoID: "todo-r1", Kind: PlanEventAdvance, By: "alice",
	}); err != nil {
		t.Fatalf("insert event row: %v", err)
	}

	fresh := newChainService(t, root)
	if got := todoStatus(t, fresh, "todo-r2"); got != jobstore.TodoPending {
		t.Fatalf("todo-r2 = %q before the scan, want pending", got)
	}
	n, err := fresh.ReplayPlanEvents()
	if err != nil || n != 1 {
		t.Fatalf("ReplayPlanEvents = %d, %v; want 1, nil", n, err)
	}
	waitTodoStatus(t, fresh, "todo-r2", jobstore.TodoDone, 30*time.Second)
	waitNoPlanEvents(t, fresh)
}

// TestPlanSweepAdvancesWithoutEventRow: even with no leftover row (the trigger was lost
// entirely), the scan finds the stalled plan and moves it.
func TestPlanSweepAdvancesWithoutEventRow(t *testing.T) {
	t.Parallel()
	s := newChainService(t, t.TempDir())
	seedChain(t, s, "plan-ev-sweep",
		chainTodo("todo-s1", "first", "ok"),
		chainTodo("todo-s2", "second", "ok", "todo-s1"),
	)
	markTodoDone(t, s, "todo-s1")
	if err := s.SweepPlans(); err != nil {
		t.Fatalf("SweepPlans: %v", err)
	}
	waitTodoStatus(t, s, "todo-s2", jobstore.TodoDone, 30*time.Second)
}

// TestPlanSweepSkipsPausedBlockedDone: the scan leaves a paused, a blocked and a done plan
// alone (no queued item, no plan.advance_paused noise), while an open one moves.
func TestPlanSweepSkipsPausedBlockedDone(t *testing.T) {
	t.Parallel()
	s := newChainService(t, t.TempDir())
	for _, id := range []string{"plan-sw-paused", "plan-sw-blocked", "plan-sw-done"} {
		seedChain(t, s, id,
			chainTodo(id+"-1", "first", "ok"),
			chainTodo(id+"-2", "second", "ok", id+"-1"),
		)
		markTodoDone(t, s, id+"-1")
	}
	if _, err := s.PausePlan("plan-sw-paused"); err != nil {
		t.Fatal(err)
	}
	if err := s.Meta().SetPlanBlocked("plan-sw-blocked", "plan-sw-blocked-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Meta().SetPlanStatus("plan-sw-done", jobstore.PlanDone, -1); err != nil {
		t.Fatal(err)
	}
	seedChain(t, s, "plan-sw-open",
		chainTodo("plan-sw-open-1", "first", "ok"),
		chainTodo("plan-sw-open-2", "second", "ok", "plan-sw-open-1"),
	)
	markTodoDone(t, s, "plan-sw-open-1")

	if err := s.SweepPlans(); err != nil {
		t.Fatalf("SweepPlans: %v", err)
	}
	waitTodoStatus(t, s, "plan-sw-open-2", jobstore.TodoDone, 30*time.Second)
	for _, id := range []string{"plan-sw-paused", "plan-sw-blocked", "plan-sw-done"} {
		if got := todoStatus(t, s, id+"-2"); got != jobstore.TodoPending {
			t.Fatalf("%s second item = %q, the scan must not touch it", id, got)
		}
		if n := countPlanEvent(t, s, id, EventPlanAdvancePaused); n != 0 {
			t.Fatalf("%s: scan recorded %d plan.advance_paused events", id, n)
		}
	}
}

// TestPlanSweeperStopsOnShutdown: the scan goroutine is service-owned background work —
// Shutdown returns (it waits for it) and nothing is left running.
func TestPlanSweeperStopsOnShutdown(t *testing.T) {
	t.Parallel()
	s := newChainService(t, t.TempDir())
	if !s.StartPlanSweeper(5 * time.Millisecond) {
		t.Fatal("StartPlanSweeper refused on a live service")
	}
	time.Sleep(30 * time.Millisecond) // a few passes must be harmless
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with a running sweeper: %v", err)
	}
	s.bg.mu.Lock()
	n := s.bg.n
	s.bg.mu.Unlock()
	if n != 0 {
		t.Fatalf("background work left after Shutdown: %d", n)
	}
	if s.StartPlanSweeper(time.Millisecond) {
		t.Fatal("StartPlanSweeper must refuse after Shutdown")
	}
}

// TestPlanSweepDispatchesSupervisorBoundPlan: a plan bound to a supervisor session only
// dispatches for its owner, so the scan must act with the plan's owner — without any event
// row, the next item is still started.
func TestPlanSweepDispatchesSupervisorBoundPlan(t *testing.T) {
	t.Parallel()
	s := newChainService(t, t.TempDir())
	if err := s.Meta().InsertPlan(jobstore.Plan{
		PlanID: "plan-ev-bound", Title: "bound", Status: jobstore.PlanOpen, ProjectKey: "self",
		Owner: "alice", SupervisorSessionID: "sess-1", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	for i, td := range []jobstore.PlanTodo{
		chainTodo("todo-b1", "first", "ok"),
		chainTodo("todo-b2", "second", "ok", "todo-b1"),
	} {
		td.PlanID, td.Sort = "plan-ev-bound", (i+1)*10
		if err := s.Meta().InsertTodo(td); err != nil {
			t.Fatal(err)
		}
	}
	markTodoDone(t, s, "todo-b1")
	if err := s.SweepPlans(); err != nil {
		t.Fatalf("SweepPlans: %v", err)
	}
	waitTodoStatus(t, s, "todo-b2", jobstore.TodoDone, 30*time.Second)
}

// TestPlanSweepDispatchesStrandedReadyOnce: an item left `ready` with no job (the process
// died between queueing and dispatching) is dispatched by a fresh Service's scan, once —
// a second pass does not run it again.
func TestPlanSweepDispatchesStrandedReadyOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := newChainService(t, root)
	seedChain(t, old, "plan-ev-ready",
		chainTodo("todo-y1", "first", "ok"),
		chainTodo("todo-y2", "second", "ok", "todo-y1"),
	)
	markTodoDone(t, old, "todo-y1")
	if ok, err := old.Meta().SetTodoReadyIfPending("todo-y2"); err != nil || !ok {
		t.Fatalf("queue todo-y2: ok=%v err=%v", ok, err)
	}
	fresh := newChainService(t, root)
	for i := 0; i < 2; i++ {
		if err := fresh.SweepPlans(); err != nil {
			t.Fatalf("SweepPlans: %v", err)
		}
	}
	waitTodoStatus(t, fresh, "todo-y2", jobstore.TodoDone, 30*time.Second)
	if err := fresh.SweepPlans(); err != nil {
		t.Fatal(err)
	}
	recs, err := fresh.Meta().ListJobsByTodo("todo-y2", 10)
	if err != nil || len(recs) != 1 {
		t.Fatalf("jobs of todo-y2 = %d (err %v), want exactly 1", len(recs), err)
	}
}

// TestPlanSweepLeavesFailedDispatchAlone: a ready item whose last dispatch failed is a
// human's business; the scan must not retry it.
func TestPlanSweepLeavesFailedDispatchAlone(t *testing.T) {
	t.Parallel()
	s := newChainService(t, t.TempDir())
	seedChain(t, s, "plan-ev-derr", chainTodo("todo-e1", "only", "ok"))
	if ok, err := s.Meta().UpdateTodoStatus("todo-e1", jobstore.TodoReady, nil); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := s.Meta().SetTodoDispatchError("todo-e1", "no such agent"); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepPlans(); err != nil {
		t.Fatal(err)
	}
	if got := todoStatus(t, s, "todo-e1"); got != jobstore.TodoReady {
		t.Fatalf("todo-e1 = %q, the scan must leave a failed dispatch alone", got)
	}
}

// TestPlanEventDroppedAfterMaxAttempts: a persisted event that keeps failing is retried
// until the cap, then deleted with an audit row naming the plan.
func TestPlanEventDroppedAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	s := newChainService(t, t.TempDir())
	id, err := s.Meta().InsertPlanEvent(jobstore.PlanEventRow{PlanID: "plan-ev-bad", Kind: PlanEventAdvance})
	if err != nil {
		t.Fatal(err)
	}
	q := queuedPlanEvent{id: id, ev: PlanEvent{PlanID: "plan-ev-bad", Kind: PlanEventAdvance}}
	for i := 1; i < planEventMaxAttempts; i++ {
		s.planEventFailed(q, errTestAdvance)
		rows := pendingPlanEvents(t, s)
		if len(rows) != 1 || rows[0].Attempts != i {
			t.Fatalf("after failure %d rows = %+v, want one row with attempts=%d", i, rows, i)
		}
	}
	if a, _ := s.Meta().ListAuditEvents(PlanEventDroppedAudit, "plan-ev-bad"); len(a) != 0 {
		t.Fatalf("audit written before the cap: %+v", a)
	}
	s.planEventFailed(q, errTestAdvance)
	if rows := pendingPlanEvents(t, s); len(rows) != 0 {
		t.Fatalf("row survived the cap: %+v", rows)
	}
	if a, err := s.Meta().ListAuditEvents(PlanEventDroppedAudit, "plan-ev-bad"); err != nil || len(a) != 1 {
		t.Fatalf("audit rows = %+v, err = %v; want 1", a, err)
	}
}

var errTestAdvance = errors.New("store unavailable")

// seedOrphanedDoing leaves the first item of a two-item chain `doing`, bound to a job row
// written straight in the given terminal status — what a serve restart leaves behind (the
// store flips the row without finish() ever running).
func seedOrphanedDoing(t *testing.T, s *Service, planID, status string) {
	t.Helper()
	seedChain(t, s, planID,
		chainTodo(planID+"-1", "first", "ok"),
		chainTodo(planID+"-2", "second", "ok", planID+"-1"),
	)
	jobID := planID + "-job"
	rec := jobstore.JobRecord{ID: jobID, ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: status, TodoID: planID + "-1", PlanID: planID, StartedAt: 1, EndedAt: 2, UpdatedAt: 2}
	if status == StatusFailed {
		rec.Error = "orphaned: serve restarted while job was non-terminal"
	}
	if err := s.Meta().UpsertJob(rec); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	if ok, err := s.Meta().UpdateTodoStatus(planID+"-1", jobstore.TodoDoing, nil); err != nil || !ok {
		t.Fatalf("todo doing: ok=%v err=%v", ok, err)
	}
	if _, err := s.Meta().SetTodoJob(planID+"-1", jobID); err != nil {
		t.Fatal(err)
	}
}

// TestPlanSweepBlocksOnOrphanedFailedJob: a `doing` item whose job was failed by the
// restart reconcile (no finish) parks the plan on the next scan, exactly once.
func TestPlanSweepBlocksOnOrphanedFailedJob(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := newChainService(t, root)
	seedOrphanedDoing(t, old, "plan-ev-orph", StatusFailed)
	fresh := newChainService(t, root)
	for i := 0; i < 3; i++ {
		if err := fresh.SweepPlans(); err != nil {
			t.Fatalf("SweepPlans: %v", err)
		}
	}
	p := waitPlanStatus(t, fresh, "plan-ev-orph", jobstore.PlanBlocked, 10*time.Second)
	if p.Status != jobstore.PlanBlocked || p.BlockedTodo != "plan-ev-orph-1" {
		t.Fatalf("plan = %+v, want blocked on the orphaned item", p)
	}
	if n := countPlanEvent(t, fresh, "plan-ev-orph", EventPlanBlocked); n != 1 {
		t.Fatalf("plan.blocked events = %d, want exactly 1", n)
	}
	if got := todoStatus(t, fresh, "plan-ev-orph-2"); got != jobstore.TodoPending {
		t.Fatalf("second item = %q, a blocked chain must not move", got)
	}
}

// TestPlanSweepLinksFinishedDoneJob: a `doing` item whose job row is already done gets
// completed by the scan and the chain moves on, dispatching the next item once.
func TestPlanSweepLinksFinishedDoneJob(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := newChainService(t, root)
	seedOrphanedDoing(t, old, "plan-ev-fin", StatusDone)
	fresh := newChainService(t, root)
	for i := 0; i < 2; i++ {
		if err := fresh.SweepPlans(); err != nil {
			t.Fatalf("SweepPlans: %v", err)
		}
	}
	waitTodoStatus(t, fresh, "plan-ev-fin-2", jobstore.TodoDone, 30*time.Second)
	if got := todoStatus(t, fresh, "plan-ev-fin-1"); got != jobstore.TodoDone {
		t.Fatalf("first item = %q, want done", got)
	}
	if n := countPlanEvent(t, fresh, "plan-ev-fin", EventPlanTodoAdvanced); n != 1 {
		t.Fatalf("plan.todo_advanced events = %d, want exactly 1", n)
	}
	recs, err := fresh.Meta().ListJobsByTodo("plan-ev-fin-2", 10)
	if err != nil || len(recs) != 1 {
		t.Fatalf("jobs of the second item = %d (err %v), want exactly 1", len(recs), err)
	}
}

// TestPlanEventSerialPerPlan pins the per-plan serialisation itself (the duplicate and
// concurrency tests above are also satisfied by the store's own state checks): while one
// event of a plan is being processed, no other event of that plan enters processing, yet
// another plan's event is not held up.
func TestPlanEventSerialPerPlan(t *testing.T) {
	t.Parallel()
	s := newChainService(t, t.TempDir())
	seedChain(t, s, "plan-ser-a", chainTodo("todo-sa", "only", "ok"))
	seedChain(t, s, "plan-ser-b", chainTodo("todo-sb", "only", "ok"))

	var mu sync.Mutex
	active := map[string]int{}
	maxActive := map[string]int{}
	entered := map[string]int{}
	release := make(chan struct{})
	s.planEventHook = func(ev PlanEvent) func() {
		mu.Lock()
		active[ev.PlanID]++
		entered[ev.PlanID]++
		if active[ev.PlanID] > maxActive[ev.PlanID] {
			maxActive[ev.PlanID] = active[ev.PlanID]
		}
		mu.Unlock()
		<-release // hold the event "in processing" until the test lets go
		return func() {
			mu.Lock()
			active[ev.PlanID]--
			mu.Unlock()
		}
	}
	read := func(m map[string]int, k string) int {
		mu.Lock()
		defer mu.Unlock()
		return m[k]
	}

	var all sync.WaitGroup
	fire := func(planID string) {
		all.Add(1)
		go func() {
			defer all.Done()
			s.emitPlanEvent(PlanEvent{PlanID: planID, Kind: PlanEventAdvance, By: "alice"}, nil)
		}()
	}
	fire("plan-ser-a")
	wait.Until(t, 10*time.Second, "first plan-a event in processing", func() bool { return read(entered, "plan-ser-a") == 1 })

	// Further events of the same plan: with the gate they queue and their callers return;
	// without it they would enter processing next to the held one.
	var others sync.WaitGroup
	for i := 0; i < 3; i++ {
		others.Add(1)
		go func() {
			defer others.Done()
			s.emitPlanEvent(PlanEvent{PlanID: "plan-ser-a", Kind: PlanEventAdvance, By: "alice"}, nil)
		}()
	}
	returned := make(chan struct{})
	go func() { others.Wait(); close(returned) }()
	wait.Until(t, 10*time.Second, "other plan-a callers returned or overlapped", func() bool {
		select {
		case <-returned:
			return true
		default:
		}
		return read(maxActive, "plan-ser-a") > 1
	})

	// Another plan is not held up by plan-a's blocked handler.
	fire("plan-ser-b")
	wait.Until(t, 10*time.Second, "plan-b event enters while plan-a is held", func() bool { return read(entered, "plan-ser-b") == 1 })

	close(release)
	all.Wait()
	others.Wait()
	if got := read(maxActive, "plan-ser-a"); got != 1 {
		t.Fatalf("plan-a events processed concurrently: max active = %d, want 1", got)
	}
	if got := read(entered, "plan-ser-a"); got != 4 {
		t.Fatalf("plan-a events processed = %d, want 4 (every queued event runs)", got)
	}
}
