package jobstore

import (
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestSourceSessionProvenancePersistsAndCannotBeReplaced(t *testing.T) {
	s := openTest(t)
	r := sampleJob("job-source-1", "proj", 100)
	r.SourceSessionID = "source-session-a"
	assert.NoErr(t, s.UpsertJob(r))

	got, ok, err := s.GetJob(r.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "source-session-a", got.SourceSessionID)

	// Result/outcome updates may rewrite ordinary job state, but provenance is fixed
	// by the authenticated submit and cannot be replaced by a later writer.
	r.SourceSessionID = "worker-claimed-session"
	r.Status = "running"
	assert.NoErr(t, s.UpsertJob(r))
	got, ok, err = s.GetJob(r.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "source-session-a", got.SourceSessionID)
}

func TestPlanSupervisorSessionPersistsAcrossReopenAndCanClear(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gofer.db")
	s, err := Open(dbPath)
	assert.NoErr(t, err)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-source-1", Title: "T", Owner: "caller-a", SupervisorSessionID: "session-a"}))
	assert.NoErr(t, s.Close())

	s, err = Open(dbPath)
	assert.NoErr(t, err)
	defer s.Close()
	p, ok, err := s.GetPlan("plan-source-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "session-a", p.SupervisorSessionID)
	assert.NoErr(t, s.SetPlanSupervisorSessionID(p.PlanID, ""))
	p, ok, err = s.GetPlan(p.PlanID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", p.SupervisorSessionID)
}
