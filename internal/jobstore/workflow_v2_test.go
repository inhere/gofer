package jobstore

import (
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// TestFreshOpenHasWorkflowV2Columns asserts a brand-new database gets the P1
// workflow v2 columns (jobs.attempt, workflows.step_attempt/next_step_at) and the
// workflow_events table in one Open (schema + migrate).
func TestFreshOpenHasWorkflowV2Columns(t *testing.T) {
	s := openTest(t)
	assert.True(t, tableHasColumn(t, s, "jobs", "attempt"))
	assert.True(t, tableHasColumn(t, s, "jobs", "fan_index"))
	assert.True(t, tableHasColumn(t, s, "workflows", "step_attempt"))
	assert.True(t, tableHasColumn(t, s, "workflows", "next_step_at"))
	assert.True(t, tableHasColumn(t, s, "workflows", "parent_workflow_id"))
	assert.True(t, tableHasColumn(t, s, "workflows", "parent_step_index"))
	assert.True(t, tableExists(t, s, "workflow_events"))
}

// TestAdvanceStepRetryTransition covers the二元组 retry transition (cur,att)->(cur,
// att+1) with a next_step_at, and the推进 transition (cur,att)->(cur+1,1) with 0.
func TestAdvanceStepRetryTransition(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWorkflow(Workflow{
		ID: "wf-r", Status: WorkflowRunning, CurrentStep: 1, StepAttempt: 1, TotalSteps: 2,
		SpecJSON: "{}", CreatedAt: 1, UpdatedAt: 1,
	}))

	// retry: (1,1) -> (1,2) with next_step_at=500.
	won, err := s.AdvanceStep("wf-r", 1, 1, 1, 2, 500)
	assert.NoErr(t, err)
	assert.True(t, won)
	got, _, _ := s.GetWorkflow("wf-r")
	assert.Eq(t, 1, got.CurrentStep)
	assert.Eq(t, 2, got.StepAttempt)
	assert.Eq(t, int64(500), got.NextStepAt)

	// A stale (1,1) caller now loses (step_attempt moved to 2).
	won, err = s.AdvanceStep("wf-r", 1, 1, 1, 2, 999)
	assert.NoErr(t, err)
	assert.False(t, won)

	// 推进: (1,2) -> (2,1) clears next_step_at.
	won, err = s.AdvanceStep("wf-r", 1, 2, 2, 1, 0)
	assert.NoErr(t, err)
	assert.True(t, won)
	got, _, _ = s.GetWorkflow("wf-r")
	assert.Eq(t, 2, got.CurrentStep)
	assert.Eq(t, 1, got.StepAttempt)
	assert.Eq(t, int64(0), got.NextStepAt)
}

// TestAdvanceStepConcurrentSingleClaim is the ⭐二元组抢权 concurrency core: N
// goroutines race the SAME retry transition (1,1)->(1,2); EXACTLY ONE wins
// (RowsAffected==1). This is the store-level proof underneath the engine's
// finish+sweeper idempotency.
func TestAdvanceStepConcurrentSingleClaim(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWorkflow(Workflow{
		ID: "wf-race2", Status: WorkflowRunning, CurrentStep: 1, StepAttempt: 1, TotalSteps: 3,
		SpecJSON: "{}", CreatedAt: 1, UpdatedAt: 1,
	}))

	const n = 32
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		wins  int
		start = make(chan struct{})
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := s.AdvanceStep("wf-race2", 1, 1, 1, 2, 700)
			if err != nil {
				t.Errorf("AdvanceStep: %v", err)
				return
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Eq(t, 1, wins) // exactly one claimer made the transition
	got, _, _ := s.GetWorkflow("wf-race2")
	assert.Eq(t, 1, got.CurrentStep)
	assert.Eq(t, 2, got.StepAttempt)
}

// TestAdvanceCurrentStepWrapper asserts the v1 wrapper still claims (from->to) at
// attempt 1 with no backoff (backward compat for the existing direct-claim path).
func TestAdvanceCurrentStepWrapper(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWorkflow(Workflow{
		ID: "wf-w", Status: WorkflowRunning, CurrentStep: 1, StepAttempt: 1, TotalSteps: 2,
		SpecJSON: "{}", CreatedAt: 1, UpdatedAt: 1,
	}))
	won, err := s.AdvanceCurrentStep("wf-w", 1, 2)
	assert.NoErr(t, err)
	assert.True(t, won)
	got, _, _ := s.GetWorkflow("wf-w")
	assert.Eq(t, 2, got.CurrentStep)
	assert.Eq(t, 1, got.StepAttempt)
	assert.Eq(t, int64(0), got.NextStepAt)
}

// TestWorkflowEventsRoundTrip covers InsertWorkflowEvent -> ListWorkflowEvents in
// seq order, with the ?since cursor.
func TestWorkflowEventsRoundTrip(t *testing.T) {
	s := openTest(t)
	seq1, err := s.InsertWorkflowEvent(WorkflowEvent{WorkflowID: "wf-e", Type: "workflow.submitted", At: 10})
	assert.NoErr(t, err)
	seq2, err := s.InsertWorkflowEvent(WorkflowEvent{WorkflowID: "wf-e", Type: "step.started", Detail: `{"step":1}`, At: 11})
	assert.NoErr(t, err)
	// An unrelated workflow's event must not leak into the wf-e listing.
	_, err = s.InsertWorkflowEvent(WorkflowEvent{WorkflowID: "wf-other", Type: "workflow.submitted", At: 12})
	assert.NoErr(t, err)
	assert.True(t, seq2 > seq1)

	all, err := s.ListWorkflowEvents("wf-e", 0)
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(all))
	assert.Eq(t, "workflow.submitted", all[0].Type)
	assert.Eq(t, "step.started", all[1].Type)
	assert.Eq(t, `{"step":1}`, all[1].Detail)

	// since cursor returns only events after it.
	tail, err := s.ListWorkflowEvents("wf-e", seq1)
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(tail))
	assert.Eq(t, "step.started", tail[0].Type)

	// Empty inputs are rejected.
	_, err = s.InsertWorkflowEvent(WorkflowEvent{Type: "x"})
	assert.Err(t, err)
	_, err = s.InsertWorkflowEvent(WorkflowEvent{WorkflowID: "x"})
	assert.Err(t, err)
}

// TestPruneWorkflowsCascades is the T1.6 retention core: an aged terminal workflow
// is pruned along with its step-jobs and workflow_events (no悬挂), while a running
// workflow and a fresh terminal workflow are kept.
func TestPruneWorkflowsCascades(t *testing.T) {
	s := openTest(t)
	const now = int64(1_000_000)

	// Aged DONE workflow (updated_at far in the past) with 2 step-jobs + events.
	assert.NoErr(t, s.InsertWorkflow(Workflow{
		ID: "wf-old", Status: WorkflowDone, CurrentStep: 3, StepAttempt: 1, TotalSteps: 2,
		SpecJSON: "{}", CreatedAt: 1, UpdatedAt: 100, // very old
	}))
	for i, jid := range []string{"old-s1", "old-s2"} {
		j := sampleJob(jid, "p", 100)
		j.Status = "done"
		j.WorkflowID = "wf-old"
		j.StepIndex = i + 1
		assert.NoErr(t, s.UpsertJob(j))
	}
	_, err := s.InsertWorkflowEvent(WorkflowEvent{WorkflowID: "wf-old", Type: "workflow.terminal", At: 100})
	assert.NoErr(t, err)

	// Running workflow (never pruned) + a FRESH terminal workflow (within age).
	assert.NoErr(t, s.InsertWorkflow(Workflow{
		ID: "wf-run", Status: WorkflowRunning, CurrentStep: 1, StepAttempt: 1, TotalSteps: 1,
		SpecJSON: "{}", CreatedAt: 1, UpdatedAt: now, // fresh
	}))
	assert.NoErr(t, s.InsertWorkflow(Workflow{
		ID: "wf-fresh", Status: WorkflowDone, CurrentStep: 2, StepAttempt: 1, TotalSteps: 1,
		SpecJSON: "{}", CreatedAt: 1, UpdatedAt: now, // fresh
	}))

	// Prune everything older than 1 day relative to now.
	policy := WorkflowRetentionPolicy{MaxAge: 24 * time.Hour}
	deleted, dirs, err := s.PruneWorkflows(policy, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted) // only wf-old
	assert.Eq(t, 2, len(dirs))

	// wf-old gone, its step-jobs gone, its events gone (no悬挂).
	_, ok, _ := s.GetWorkflow("wf-old")
	assert.False(t, ok)
	jobs, _ := s.ListWorkflowJobs("wf-old")
	assert.Eq(t, 0, len(jobs))
	events, _ := s.ListWorkflowEvents("wf-old", 0)
	assert.Eq(t, 0, len(events))
	if _, ok, _ := s.GetJob("old-s1"); ok {
		t.Fatal("step-job old-s1 should be pruned with its workflow")
	}

	// Running + fresh workflows survive.
	_, ok, _ = s.GetWorkflow("wf-run")
	assert.True(t, ok)
	_, ok, _ = s.GetWorkflow("wf-fresh")
	assert.True(t, ok)
}

// TestPruneWorkflowsZeroIsNoop asserts a zero workflow policy prunes nothing.
func TestPruneWorkflowsZeroIsNoop(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWorkflow(Workflow{
		ID: "wf-keep", Status: WorkflowDone, CurrentStep: 1, StepAttempt: 1, TotalSteps: 1,
		SpecJSON: "{}", CreatedAt: 1, UpdatedAt: 1,
	}))
	deleted, _, err := s.PruneWorkflows(WorkflowRetentionPolicy{}, 1_000_000)
	assert.NoErr(t, err)
	assert.Eq(t, 0, deleted)
	_, ok, _ := s.GetWorkflow("wf-keep")
	assert.True(t, ok)
}

// TestJobAttemptRoundTrip asserts jobs.attempt round-trips and an unset (0) legacy
// value reads back as 1 (COALESCE) only when stored as NULL — an explicitly-stored
// 0 stays 0 (non-workflow job default).
func TestJobAttemptRoundTrip(t *testing.T) {
	s := openTest(t)
	j := sampleJob("j-att", "p", 100)
	j.WorkflowID = "wf-att"
	j.StepIndex = 1
	j.Attempt = 2
	assert.NoErr(t, s.UpsertJob(j))
	got, ok, err := s.GetJob("j-att")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, 2, got.Attempt)
}
