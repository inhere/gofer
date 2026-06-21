package jobstore

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestFreshOpenHasWorkflowsTableAndJobCols asserts a brand-new database gets the
// workflows table (+ status index) and the two job-chain columns on jobs in one
// Open (schema + migrate).
func TestFreshOpenHasWorkflowsTableAndJobCols(t *testing.T) {
	s := openTest(t)
	assert.True(t, tableExists(t, s, "workflows"))
	assert.True(t, indexExists(t, s, "idx_workflows_status"))
	assert.True(t, tableHasColumn(t, s, "jobs", "workflow_id"))
	assert.True(t, tableHasColumn(t, s, "jobs", "step_index"))
}

// TestWorkflowInsertGetRoundTrip covers InsertWorkflow -> GetWorkflow.
func TestWorkflowInsertGetRoundTrip(t *testing.T) {
	s := openTest(t)
	w := Workflow{
		ID: "wf-1", Title: "chain", Status: WorkflowRunning,
		CurrentStep: 1, TotalSteps: 3,
		SpecJSON: `{"steps":[{},{},{}]}`, CallerID: "alice",
		CreatedAt: 100, UpdatedAt: 100,
	}
	assert.NoErr(t, s.InsertWorkflow(w))

	got, ok, err := s.GetWorkflow("wf-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "wf-1", got.ID)
	assert.Eq(t, "chain", got.Title)
	assert.Eq(t, WorkflowRunning, got.Status)
	assert.Eq(t, 1, got.CurrentStep)
	assert.Eq(t, 3, got.TotalSteps)
	assert.Eq(t, `{"steps":[{},{},{}]}`, got.SpecJSON)
	assert.Eq(t, "alice", got.CallerID)

	// Unknown id -> (zero, false, nil).
	_, ok, err = s.GetWorkflow("nope")
	assert.NoErr(t, err)
	assert.False(t, ok)
}

// TestWorkflowJobRoundTrip asserts a job carrying workflow_id/step_index
// round-trips through Upsert/Get, and ListWorkflowJobs returns step-ordered jobs.
func TestWorkflowJobRoundTrip(t *testing.T) {
	s := openTest(t)

	// Two step-jobs of one workflow (inserted out of order) + an unrelated job.
	j2 := sampleJob("j2", "p", 200)
	j2.WorkflowID = "wf-x"
	j2.StepIndex = 2
	j1 := sampleJob("j1", "p", 100)
	j1.WorkflowID = "wf-x"
	j1.StepIndex = 1
	other := sampleJob("other", "p", 300) // no workflow
	assert.NoErr(t, s.UpsertJob(j2))
	assert.NoErr(t, s.UpsertJob(j1))
	assert.NoErr(t, s.UpsertJob(other))

	got1, ok, err := s.GetJob("j1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "wf-x", got1.WorkflowID)
	assert.Eq(t, 1, got1.StepIndex)

	// Unrelated job has empty/zero workflow fields.
	gotOther, _, _ := s.GetJob("other")
	assert.Eq(t, "", gotOther.WorkflowID)
	assert.Eq(t, 0, gotOther.StepIndex)

	jobs, err := s.ListWorkflowJobs("wf-x")
	assert.NoErr(t, err)
	assert.Len(t, jobs, 2)
	assert.Eq(t, "j1", jobs[0].ID) // step_index ASC
	assert.Eq(t, "j2", jobs[1].ID)

	// No jobs for an unknown workflow -> empty slice, no error.
	none, err := s.ListWorkflowJobs("ghost")
	assert.NoErr(t, err)
	assert.Len(t, none, 0)
}

// TestListWorkflowsStatusFilter asserts the status filter + newest-first order.
func TestListWorkflowsStatusFilter(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWorkflow(Workflow{ID: "a", Status: WorkflowRunning, CurrentStep: 1, TotalSteps: 1, SpecJSON: "{}", CreatedAt: 100, UpdatedAt: 100}))
	assert.NoErr(t, s.InsertWorkflow(Workflow{ID: "b", Status: WorkflowDone, CurrentStep: 1, TotalSteps: 1, SpecJSON: "{}", CreatedAt: 200, UpdatedAt: 200}))
	assert.NoErr(t, s.InsertWorkflow(Workflow{ID: "c", Status: WorkflowRunning, CurrentStep: 1, TotalSteps: 1, SpecJSON: "{}", CreatedAt: 300, UpdatedAt: 300}))

	running, err := s.ListWorkflows(WorkflowRunning, 0)
	assert.NoErr(t, err)
	assert.Len(t, running, 2)
	assert.Eq(t, "c", running[0].ID) // created_at DESC
	assert.Eq(t, "a", running[1].ID)

	all, err := s.ListWorkflows("", 0)
	assert.NoErr(t, err)
	assert.Len(t, all, 3)
}

// TestAdvanceCurrentStepConcurrentSingleClaim is the推进幂等 core: N goroutines
// race AdvanceCurrentStep(from=1 -> to=2) on one running workflow; EXACTLY ONE
// must win (RowsAffected==1). The conditional UPDATE is the single-claim barrier.
func TestAdvanceCurrentStepConcurrentSingleClaim(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWorkflow(Workflow{
		ID: "wf-race", Status: WorkflowRunning, CurrentStep: 1, TotalSteps: 3,
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
			ok, err := s.AdvanceCurrentStep("wf-race", 1, 2)
			if err != nil {
				t.Errorf("AdvanceCurrentStep: %v", err)
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

	assert.Eq(t, 1, wins) // exactly one claimer advanced the step

	// current_step is now 2 (moved once, not n times).
	got, _, _ := s.GetWorkflow("wf-race")
	assert.Eq(t, 2, got.CurrentStep)
}

// TestAdvanceCurrentStepGuards asserts a from-mismatch or a non-running status
// returns false (no advance).
func TestAdvanceCurrentStepGuards(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWorkflow(Workflow{ID: "wf-g", Status: WorkflowRunning, CurrentStep: 2, TotalSteps: 3, SpecJSON: "{}", CreatedAt: 1, UpdatedAt: 1}))

	// from does not match current_step (2) -> false, no change.
	ok, err := s.AdvanceCurrentStep("wf-g", 1, 2)
	assert.NoErr(t, err)
	assert.False(t, ok)
	got, _, _ := s.GetWorkflow("wf-g")
	assert.Eq(t, 2, got.CurrentStep)

	// status != running -> false even with matching from.
	assert.NoErr(t, s.SetWorkflowStatus("wf-g", WorkflowFailed, "boom"))
	ok, err = s.AdvanceCurrentStep("wf-g", 2, 3)
	assert.NoErr(t, err)
	assert.False(t, ok)
	got, _, _ = s.GetWorkflow("wf-g")
	assert.Eq(t, WorkflowFailed, got.Status)
	assert.Eq(t, "boom", got.Error)
}

// TestMigrateAddsWorkflowSupportToOldDB simulates a pre-existing DB whose jobs
// table lacks workflow_id/step_index and has no workflows table; re-Open must
// add both columns and create the workflows table additively, and an old job
// reads back with empty workflow fields.
func TestMigrateAddsWorkflowSupportToOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE jobs (
	  id           TEXT PRIMARY KEY,
	  project_key  TEXT NOT NULL,
	  agent        TEXT NOT NULL,
	  runner       TEXT NOT NULL,
	  worker_id    TEXT,
	  status       TEXT NOT NULL,
	  exit_code    INTEGER NOT NULL DEFAULT 0,
	  cwd          TEXT,
	  result_dir   TEXT NOT NULL,
	  request_json TEXT,
	  error        TEXT,
	  started_at   INTEGER NOT NULL,
	  ended_at     INTEGER,
	  updated_at   INTEGER NOT NULL
	)`)
	assert.NoErr(t, err)
	// An old job inserted before the chain columns existed.
	_, err = raw.Exec(`INSERT INTO jobs (id, project_key, agent, runner, status, result_dir, started_at, updated_at)
	  VALUES ('old-1','p','exec','local','done','/tmp/r/old-1', 10, 10)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()

	assert.True(t, tableHasColumn(t, s, "jobs", "workflow_id"))
	assert.True(t, tableHasColumn(t, s, "jobs", "step_index"))
	assert.True(t, tableExists(t, s, "workflows"))

	gotOld, ok, err := s.GetJob("old-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", gotOld.WorkflowID) // COALESCE NULL -> ""
	assert.Eq(t, 0, gotOld.StepIndex)   // COALESCE NULL -> 0

	// The migrated DB is usable: a workflow + step-job round-trip.
	assert.NoErr(t, s.InsertWorkflow(Workflow{ID: "wf", Status: WorkflowRunning, CurrentStep: 1, TotalSteps: 1, SpecJSON: "{}", CreatedAt: 20, UpdatedAt: 20}))
	step := sampleJob("s1", "p", 30)
	step.WorkflowID = "wf"
	step.StepIndex = 1
	assert.NoErr(t, s.UpsertJob(step))
	jobs, err := s.ListWorkflowJobs("wf")
	assert.NoErr(t, err)
	assert.Len(t, jobs, 1)
}

// tableExists reports whether a table of the given name exists on the db.
func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n string
	err := s.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name = ?", name,
	).Scan(&n)
	if err == sql.ErrNoRows {
		return false
	}
	assert.NoErr(t, err)
	return n == name
}
