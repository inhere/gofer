package jobstore

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func stageItem(t *testing.T, s *Store) WorkItem {
	t.Helper()
	w, err := s.CreateWorkItem(WorkItemInput{Title: "stage item", ProjectKey: "p1", By: "human:a"})
	assert.NoErr(t, err)
	return w
}

func startStage(t *testing.T, s *Store, id string) WorkStage {
	t.Helper()
	ws, err := s.StartWorkStage(WorkStage{WorkItemID: id, Template: "feature", Steps: []string{"design", "review", "plan"}, ReviewMax: 2, PlanDoc: true}, "human:a")
	assert.NoErr(t, err)
	return ws
}

func TestStartWorkStageSnapshotsStepsAndRejectsSecondStart(t *testing.T) {
	s := openTest(t)
	w := stageItem(t, s)
	ws := startStage(t, s, w.ID)
	assert.Eq(t, []string{"design", "review", "plan"}, ws.Steps)
	assert.Eq(t, 0, ws.Step)
	assert.Eq(t, StageActive, ws.Status)
	assert.Eq(t, -1, ws.ApprovedStep)
	assert.True(t, ws.PlanDoc)
	assert.Eq(t, "design", ws.StepName())

	_, err := s.StartWorkStage(WorkStage{WorkItemID: w.ID, Template: "small", Steps: []string{"doing"}}, "human:a")
	assert.True(t, errors.Is(err, ErrStageActive))

	// The journal has the start line, kind stage, milestone level.
	j, err := s.ListWorkJournal(w.ID, 0, 0)
	assert.NoErr(t, err)
	last := j[len(j)-1]
	assert.Eq(t, WorkJournalStage, last.Kind)
	assert.Eq(t, WorkLevelMilestone, last.Level)

	// A finished flow can be replaced.
	ok, err := s.TransitionWorkStage(StageTransition{WorkItemID: w.ID, FromStep: 0, ToStep: 0, ToStatus: StageDropped, Journal: "stage drop"})
	assert.NoErr(t, err)
	assert.True(t, ok)
	ws, err = s.StartWorkStage(WorkStage{WorkItemID: w.ID, Template: "small", Steps: []string{"doing", "verify"}}, "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, "small", ws.Template)
	assert.Eq(t, StageActive, ws.Status)
}

func TestTransitionWorkStageIsConditionalOnStep(t *testing.T) {
	s := openTest(t)
	w := stageItem(t, s)
	startStage(t, s, w.ID)

	const racers = 8
	var applied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.TransitionWorkStage(StageTransition{WorkItemID: w.ID, FromStep: 0, ToStep: 1, ToStatus: StageActive, Journal: "stage step: design > review", By: "human:a"})
			if err != nil {
				t.Errorf("transition: %v", err)
				return
			}
			if ok {
				applied.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Eq(t, int32(1), applied.Load())
	ws, _, _ := s.GetWorkStage(w.ID)
	assert.Eq(t, 1, ws.Step)

	// Entering a step clears an approval of the previous one.
	two := 1
	ok, err := s.TransitionWorkStage(StageTransition{WorkItemID: w.ID, FromStep: 1, ToStep: 1, ToStatus: StageActive, ApprovedStep: &two, Journal: "stage approve"})
	assert.NoErr(t, err)
	assert.True(t, ok)
	ws, _, _ = s.GetWorkStage(w.ID)
	assert.Eq(t, 1, ws.ApprovedStep)
	ok, err = s.TransitionWorkStage(StageTransition{WorkItemID: w.ID, FromStep: 1, ToStep: 2, ToStatus: StageActive, Journal: "stage step"})
	assert.NoErr(t, err)
	assert.True(t, ok)
	ws, _, _ = s.GetWorkStage(w.ID)
	assert.Eq(t, -1, ws.ApprovedStep)

	// A finished flow takes no further transition.
	ok, err = s.TransitionWorkStage(StageTransition{WorkItemID: w.ID, FromStep: 2, ToStep: 2, ToStatus: StageDone, Journal: "stage done"})
	assert.NoErr(t, err)
	assert.True(t, ok)
	ok, err = s.TransitionWorkStage(StageTransition{WorkItemID: w.ID, FromStep: 2, ToStep: 2, ToStatus: StageActive, Journal: "again"})
	assert.NoErr(t, err)
	assert.False(t, ok)
}

func TestWorkLinkDocRoleAndMigration(t *testing.T) {
	s := openTest(t)
	w := stageItem(t, s)
	_, err := s.AddWorkLinkRole(w.ID, WorkLinkDoc, "docs/design/x.md", "bogus", "human:a")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	_, err = s.AddWorkLinkRole(w.ID, WorkLinkPlan, "p-1", WorkDocRoleDesign, "human:a")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	_, err = s.AddWorkLinkRole(w.ID, WorkLinkDoc, "docs/design/x.md", WorkDocRoleDesign, "human:a")
	assert.NoErr(t, err)
	_, err = s.AddWorkLinkRole(w.ID, WorkLinkJob, "j-1", WorkJobRoleReview, "human:a")
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(w.ID, WorkLinkIssue, "gofer-1", "human:a")
	assert.NoErr(t, err)

	links, err := s.ListWorkLinksRole(w.ID, "")
	assert.NoErr(t, err)
	assert.Eq(t, 3, len(links))
	docs, err := s.ListWorkLinksRole(w.ID, WorkLinkDoc)
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(docs))
	assert.Eq(t, WorkDocRoleDesign, docs[0].Role)

	// The same doc re-linked with another role changes the role.
	_, err = s.AddWorkLinkRole(w.ID, WorkLinkDoc, "docs/design/x.md", WorkDocRolePlan, "human:a")
	assert.NoErr(t, err)
	docs, _ = s.ListWorkLinksRole(w.ID, WorkLinkDoc)
	assert.Eq(t, 1, len(docs))
	assert.Eq(t, WorkDocRolePlan, docs[0].Role)
}

func TestWorkStagesForSessionAndIssueLookup(t *testing.T) {
	s := openTest(t)
	w := stageItem(t, s)
	mustSession(t, s, "s-1")
	assert.NoErr(t, s.AttachWorkSession(w.ID, "s-1", "human:a"))
	_, err := s.AddWorkLink(w.ID, WorkLinkIssue, "gofer-9", "human:a")
	assert.NoErr(t, err)

	got, err := s.WorkStagesForSession("s-1")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(got))
	startStage(t, s, w.ID)
	got, err = s.WorkStagesForSession("s-1")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(got))
	got, _ = s.WorkStagesForSession("other")
	assert.Eq(t, 0, len(got))

	open, err := s.ListOpenWorkStages("p1")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(open))
	open, _ = s.ListOpenWorkStages("nope")
	assert.Eq(t, 0, len(open))

	items, err := s.WorkItemsLinkedToIssue("gofer-9")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(items))
	done := WorkDone
	_, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{Status: &done}, 0, "human:a")
	assert.NoErr(t, err)
	items, _ = s.WorkItemsLinkedToIssue("gofer-9")
	assert.Eq(t, 0, len(items))
}

func TestDeleteWorkItemRemovesItsStage(t *testing.T) {
	s := openTest(t)
	w := stageItem(t, s)
	startStage(t, s, w.ID)
	dropped := WorkDropped
	_, _, err := s.UpdateWorkItem(w.ID, WorkItemPatch{Status: &dropped}, 0, "human:a")
	assert.NoErr(t, err)
	assert.NoErr(t, s.DeleteWorkItem(w.ID, "human:a"))
	_, ok, err := s.GetWorkStage(w.ID)
	assert.NoErr(t, err)
	assert.False(t, ok)
}
