package job

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// gofer-dzv3 (docs/design/2026-10-14-update-wait-idle-design.md): a one-click update
// parks new jobs instead of refusing them while it waits for the machine to go idle.

// pauseProbeRequest is a plain exec job whose only effect is creating probe — the
// evidence of whether it ran. Same shape as probeRequest, without the --hold.
func pauseProbeRequest(bin, probe string) JobRequest {
	return JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{bin, "write-files", probe, "ran"}, Cwd: ".", TimeoutSec: 30,
	}
}

// mustParkForUpdate submits a request while the pause is on and checks it parked:
// paused_for_update, no in-memory entry, nothing run.
func mustParkForUpdate(t *testing.T, s *Service, req JobRequest, probe string) JobResult {
	t.Helper()
	res, err := s.Submit(req)
	if err != nil {
		t.Fatalf("Submit(paused): %v", err)
	}
	if res.Status != StatusPausedForUpdate {
		t.Fatalf("status = %s, want %s", res.Status, StatusPausedForUpdate)
	}
	if s.entry(res.ID) != nil {
		t.Fatalf("a job parked for an update must have no in-memory entry")
	}
	if probe != "" {
		assertNoProbe(t, probe)
	}
	return res
}

// TestUpdatePauseParksAndReleasesUnderTheSameID: with the pause on, a submit is
// admitted and parked (not refused); releasing it runs the SAME job id to done.
func TestUpdatePauseParksAndReleasesUnderTheSameID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe.txt")

	s.PauseStartsForUpdate("apply-1")
	if owner := s.UpdatePauseOwner(); owner != "apply-1" {
		t.Fatalf("pause owner = %q", owner)
	}
	parked := mustParkForUpdate(t, s, pauseProbeRequest(bin, probe), probe)

	got, ok := s.Get(parked.ID)
	if !ok || got.Status != StatusPausedForUpdate {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
	if got.Hold == nil || got.Hold.Kind != HoldKindUpdate || got.Hold.Reason == "" {
		t.Fatalf("park record = %+v, want kind %q and a reason", got.Hold, HoldKindUpdate)
	}
	// An update park has no deadline of its own: the update owns its end.
	if got.Hold.ExpiresAt != 0 || got.Hold.TimeoutSec != 0 {
		t.Fatalf("park deadline = %+v, want none", got.Hold)
	}
	if got.Title == "" {
		t.Fatalf("a parked job keeps its title")
	}
	if n := countEvents(eventTypes(t, s, parked.ID), EventJobPausedForUpdate); n != 1 {
		t.Fatalf("job.paused_for_update recorded %d times", n)
	}
	if n := countEvents(eventTypes(t, s, parked.ID), EventJobSubmitted); n != 1 {
		t.Fatalf("job.submitted recorded %d times, want 1", n)
	}
	// The count/list the apply progress reads.
	if n, err := s.CountUpdatePaused(); err != nil || n != 1 {
		t.Fatalf("CountUpdatePaused = %d, %v", n, err)
	}
	if list, err := s.ListUpdatePaused(10); err != nil || len(list) != 1 || list[0].ID != parked.ID {
		t.Fatalf("ListUpdatePaused = %+v, %v", list, err)
	}

	released, err := s.ResumeStartsForUpdate()
	if err != nil || released != 1 {
		t.Fatalf("ResumeStartsForUpdate = %d, %v", released, err)
	}
	if owner := s.UpdatePauseOwner(); owner != "" {
		t.Fatalf("pause owner after release = %q", owner)
	}
	final, ok := s.Wait(parked.ID)
	if !ok || final.Status != StatusDone {
		t.Fatalf("released job = %s (%s), %v", final.Status, final.Error, ok)
	}
	if _, err := os.Stat(probe); err != nil {
		t.Fatalf("probe missing: the released job did not run: %v", err)
	}
	if n := countEvents(eventTypes(t, s, parked.ID), EventJobUpdateResumed); n != 1 {
		t.Fatalf("job.update_resumed recorded %d times", n)
	}
	if n, err := s.CountUpdatePaused(); err != nil || n != 0 {
		t.Fatalf("CountUpdatePaused after release = %d, %v", n, err)
	}
	// A released job is an ordinary job: its row keeps no park record and it submitted
	// itself once more.
	if got, _ := s.Get(parked.ID); got.Hold != nil {
		t.Fatalf("released row still carries a park record: %+v", got.Hold)
	}
}

// TestUpdatePausedIsNotAHumanHold: nothing about a job parked by an update may reach
// the surfaces that ask a person to approve a --hold submit — the待批准 list, the
// approval call, and the hold expiry sweep.
func TestUpdatePausedIsNotAHumanHold(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe.txt")

	s.PauseStartsForUpdate("apply-2")
	parked := mustParkForUpdate(t, s, pauseProbeRequest(bin, probe), probe)
	held := mustHold(t, s, probeRequest(bin, filepath.Join(root, "held.txt")), "")

	// The待批准 list (GET /v1/jobs?status=awaiting_approval, the web board's filter)
	// holds the --hold job and only that one.
	list, err := s.ListJobs(ListOpts{Status: StatusAwaitingApproval, Limit: 50})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list) != 1 || list[0].ID != held.ID {
		t.Fatalf("awaiting_approval list = %+v, want only %s", list, held.ID)
	}
	// Approving it is refused: it is not waiting for a person.
	if _, err := s.ApproveJob(parked.ID, "alice", ""); !errors.Is(err, ErrJobNotAwaitingApproval) {
		t.Fatalf("ApproveJob(paused) err = %v, want ErrJobNotAwaitingApproval", err)
	}
	// The expiry sweep only sees awaiting_approval rows: a long pass leaves the parked
	// job untouched (the hold it does expire proves the sweep ran).
	expired, err := s.SweepExpiredHolds(held.Hold.ExpiresAt + 1)
	if err != nil || expired != 1 {
		t.Fatalf("SweepExpiredHolds = %d, %v (want the held job expired)", expired, err)
	}
	if got, _ := s.Get(parked.ID); got.Status != StatusPausedForUpdate {
		t.Fatalf("parked job after the sweep = %s, want %s", got.Status, StatusPausedForUpdate)
	}
	// ... and it does not keep a restart drain waiting either (gofer-dzv3).
	blockers, err := s.CountUpgradeRestartBlockers(nil)
	if err != nil || blockers != 0 {
		t.Fatalf("restart blockers = %d, %v, want 0 (a parked job is what the restart waits for)", blockers, err)
	}
}

// TestUpdatePausedSurvivesRestart: a parked job is database state only, so a new
// Service over the same store (a restart) plus the orphan reconcile still finds it
// parked — and releasing it there runs it.
func TestUpdatePausedSurvivesRestart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	first := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe.txt")

	first.PauseStartsForUpdate("apply-3")
	parked := mustParkForUpdate(t, first, pauseProbeRequest(bin, probe), probe)

	second := newHoldService(t, root, nil)
	if _, err := second.ReconcileOrphanJobs(); err != nil {
		t.Fatalf("ReconcileOrphanJobs: %v", err)
	}
	// The pause does not survive the process: nothing owns it any more, so the new
	// server only has to release what the old one parked.
	if owner := second.UpdatePauseOwner(); owner != "" {
		t.Fatalf("pause owner after restart = %q, want none", owner)
	}
	got, ok := second.Get(parked.ID)
	if !ok || got.Status != StatusPausedForUpdate {
		t.Fatalf("after restart: %s, %v", got.Status, ok)
	}
	assertNoProbe(t, probe)

	released, err := second.ResumeStartsForUpdate()
	if err != nil || released != 1 {
		t.Fatalf("ResumeStartsForUpdate after restart = %d, %v", released, err)
	}
	final, ok := second.Wait(parked.ID)
	if !ok || final.Status != StatusDone {
		t.Fatalf("released job = %s (%s), %v", final.Status, final.Error, ok)
	}
	if _, err := os.Stat(probe); err != nil {
		t.Fatalf("probe missing after the restart release: %v", err)
	}
}

// TestUpdatePausedRejectsTheWholeBacklog: every parked job comes back, not just the
// first one, and the release is idempotent.
func TestUpdatePausedReleasesTheWholeBacklog(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)

	s.PauseStartsForUpdate("apply-4")
	ids := make([]string, 0, 3)
	for _, name := range []string{"a", "b", "c"} {
		res := mustParkForUpdate(t, s, pauseProbeRequest(bin, filepath.Join(root, name+".txt")), "")
		ids = append(ids, res.ID)
	}
	released, err := s.ResumeStartsForUpdate()
	if err != nil || released != 3 {
		t.Fatalf("ResumeStartsForUpdate = %d, %v", released, err)
	}
	for _, id := range ids {
		final, ok := s.Wait(id)
		if !ok || final.Status != StatusDone {
			t.Fatalf("job %s = %s (%s), %v", id, final.Status, final.Error, ok)
		}
	}
	if again, err := s.ResumeStartsForUpdate(); err != nil || again != 0 {
		t.Fatalf("second release = %d, %v, want a no-op", again, err)
	}
}

// TestUpdatePausedCanBeCancelled: a parked job never ran, so withdrawing it is one
// transition to cancelled rather than a silent no-op.
func TestUpdatePausedCanBeCancelled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe.txt")

	s.PauseStartsForUpdate("apply-5")
	parked := mustParkForUpdate(t, s, pauseProbeRequest(bin, probe), probe)
	if err := s.Cancel(parked.ID); err != nil {
		t.Fatalf("Cancel(parked): %v", err)
	}
	final, _ := s.Wait(parked.ID)
	if final.Status != StatusCancelled {
		t.Fatalf("cancelled job = %s (%s)", final.Status, final.Error)
	}
	assertNoProbe(t, probe)
	if released, err := s.ResumeStartsForUpdate(); err != nil || released != 0 {
		t.Fatalf("release after cancel = %d, %v, want 0", released, err)
	}
}

// TestUpdatePauseBlocksInternalProducers: the plan chain dispatches through Submit, so
// a paused start must park its step job rather than fail the dispatch — and the chain
// must carry on once the update releases it.
func TestUpdatePauseBlocksInternalProducers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newChainService(t, root)
	seedChain(t, s, "plan-pause", chainTodo("todo-p", "step one", "ok"))

	s.PauseStartsForUpdate("apply-6")
	d, err := s.DispatchTodo("todo-p", "alice")
	if err != nil {
		t.Fatalf("DispatchTodo while paused: %v", err)
	}
	if d.Job == nil || d.Job.Status != StatusPausedForUpdate {
		t.Fatalf("dispatched job = %+v (reason %q), want a job parked for the update", d.Job, d.Reason)
	}
	// The item follows its parked job like any other: it is "active", so a second
	// dispatch does not start a duplicate.
	again, err := s.DispatchTodo("todo-p", "alice")
	if err != nil || again.Job != nil {
		t.Fatalf("second dispatch = %+v, %v, want none", again.Job, err)
	}

	if released, err := s.ResumeStartsForUpdate(); err != nil || released != 1 {
		t.Fatalf("ResumeStartsForUpdate = %d, %v", released, err)
	}
	final, ok := s.Wait(d.Job.ID)
	if !ok || final.Status != StatusDone {
		t.Fatalf("released step job = %s (%s), %v", final.Status, final.Error, ok)
	}
	// The released step keeps its plan linkage, so the chain closes the item and the
	// plan.
	wait.Until(t, wait.Timeout(t, 10*time.Second), "todo-p to reach done after the release", func() bool {
		todo, ok, err := s.Meta().GetTodo("todo-p")
		return err == nil && ok && todo.Status == jobstore.TodoDone
	})
	if got := final.PlanID; got != "plan-pause" {
		t.Fatalf("released step plan_id = %q, want plan-pause", got)
	}
	if got := final.TodoID; got != "todo-p" {
		t.Fatalf("released step todo_id = %q, want todo-p", got)
	}
}

// TestUpdatePauseCarriesSubmitThroughTheDrain: while an update drains the server,
// admission is closed and a submit is refused — except when that update is the one
// pausing starts, where the job parks instead of being lost. A manual `serve upgrade`
// (no pause) keeps refusing.
func TestUpdatePauseCarriesSubmitThroughTheDrain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	ctx := context.Background()

	if err := s.CloseUpgradeAdmission(ctx, "upgrade-1"); err != nil {
		t.Fatalf("CloseUpgradeAdmission: %v", err)
	}
	if _, err := s.Submit(pauseProbeRequest(bin, filepath.Join(root, "manual.txt"))); !errors.Is(err, ErrUpgradeDraining) {
		t.Fatalf("submit during a manual drain err = %v, want ErrUpgradeDraining", err)
	}

	s.PauseStartsForUpdate("apply-7")
	parked := mustParkForUpdate(t, s, pauseProbeRequest(bin, filepath.Join(root, "update.txt")), "")
	if released, err := s.ResumeStartsForUpdate(); err != nil || released != 1 {
		t.Fatalf("ResumeStartsForUpdate = %d, %v", released, err)
	}
	final, _ := s.Wait(parked.ID)
	if final.Status != StatusDone {
		t.Fatalf("released job = %s (%s)", final.Status, final.Error)
	}
}

// TestUpdatePauseOnlyParksNewJobs: starting the pause does not disturb work already
// admitted — the job that is already running finishes, and only what arrives after the
// pause parks.
func TestUpdatePauseOnlyParksNewJobs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	first := filepath.Join(root, "first.txt")

	running, err := s.Submit(pauseProbeRequest(bin, first))
	if err != nil {
		t.Fatalf("Submit(before pause): %v", err)
	}
	s.PauseStartsForUpdate("apply-8")
	parked := mustParkForUpdate(t, s, pauseProbeRequest(bin, filepath.Join(root, "second.txt")), "")
	if parked.ID == running.ID {
		t.Fatalf("parked job reused the running job's id")
	}
	final, _ := s.Wait(running.ID)
	if final.Status != StatusDone {
		t.Fatalf("job submitted before the pause = %s (%s)", final.Status, final.Error)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("the job admitted before the pause did not run: %v", err)
	}
}
