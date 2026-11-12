package today

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// awaitingMergeFixture seeds a complete_on=merge plan with one doing todo bound to a done
// worktree job; mut shapes the job row, and the returned card (when any) is the plan's
// awaiting-merge card.
func awaitingMergeCard(t *testing.T, completeOn string, mut func(*jobstore.JobRecord), todoStatus string, events ...string) (Card, bool) {
	t.Helper()
	svc, st := newTestService(t)
	assert.NoErr(t, st.InsertPlan(jobstore.Plan{PlanID: "p-am", Title: "收尾计划", Status: jobstore.PlanOpen, ProjectKey: "p1", CompleteOn: completeOn}))
	assert.NoErr(t, st.InsertTodo(jobstore.PlanTodo{TodoID: "t-am", PlanID: "p-am", Title: "后端", Status: todoStatus, JobID: "j-am"}))
	assert.NoErr(t, st.InsertTodo(jobstore.PlanTodo{TodoID: "t-next", PlanID: "p-am", Title: "前端", After: []string{"t-am"}}))
	rec := jobstore.JobRecord{ID: "j-am", ProjectKey: "p1", Agent: "codex", Status: job.StatusDone, EndedAt: ago(30),
		PlanID: "p-am", TodoID: "t-am", WorktreePath: "/wt/am", WorktreeBranch: "gofer/j-am", CommitsAhead: 2}
	if mut != nil {
		mut(&rec)
	}
	putJob(t, st, rec)
	for _, ev := range events {
		_, err := st.InsertJobEvent(jobstore.JobEvent{JobID: "j-am", Type: ev, Detail: `{"todo_id":"t-am"}`, At: ago(10)})
		assert.NoErr(t, err)
	}
	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	return find(cards, KindTodoAwaitingMerge+":t-am")
}

// TestAwaitingMergeCard: the card appears for a doing todo of a merge-mode plan whose job
// ended done with the branch unmerged, and says what blocks (a successor waits on it).
func TestAwaitingMergeCard(t *testing.T) {
	c, ok := awaitingMergeCard(t, jobstore.PlanCompleteOnMerge, nil, jobstore.TodoDoing)
	assert.True(t, ok)
	assert.Eq(t, AwaitingMergeWaiting, c.Subtype)
	assert.Eq(t, "p-am", c.Refs.PlanID)
	assert.Eq(t, "t-am", c.Refs.TodoID)
	assert.Eq(t, "j-am", c.Refs.JobID)
	assert.Eq(t, "p1", c.ProjectKey)
	assert.Eq(t, []string{"open"}, actionIDs(c.Actions))
	assert.NotEmpty(t, c.Summary)
}

// TestAwaitingMergeCardAbsent: no card for default-mode plans, finished todos, a job that
// is not done (a running follow-up replaced it), a still-pending delivery, a merged
// delivery with nothing wrong, a continued job, or a job that is no worktree job.
func TestAwaitingMergeCardAbsent(t *testing.T) {
	cases := []struct {
		name       string
		completeOn string
		todoStatus string
		mut        func(*jobstore.JobRecord)
	}{
		{"job mode plan", jobstore.PlanCompleteOnJob, jobstore.TodoDoing, nil},
		{"todo done", jobstore.PlanCompleteOnMerge, jobstore.TodoDone, nil},
		{"follow-up job running", jobstore.PlanCompleteOnMerge, jobstore.TodoDoing, func(r *jobstore.JobRecord) { r.Status = job.StatusRunning }},
		{"delivery pending", jobstore.PlanCompleteOnMerge, jobstore.TodoDoing, func(r *jobstore.JobRecord) { r.DeliveryJSON = `{"mode":"merge","state":"pending"}` }},
		{"delivery merged", jobstore.PlanCompleteOnMerge, jobstore.TodoDoing, func(r *jobstore.JobRecord) { r.DeliveryJSON = `{"mode":"merge","state":"merged"}` }},
		{"continued", jobstore.PlanCompleteOnMerge, jobstore.TodoDoing, func(r *jobstore.JobRecord) { r.AutoResumedBy = "j-next" }},
		{"not a worktree job", jobstore.PlanCompleteOnMerge, jobstore.TodoDoing, func(r *jobstore.JobRecord) { r.WorktreeBranch, r.WorktreePath = "", "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := awaitingMergeCard(t, tc.completeOn, tc.mut, tc.todoStatus)
			assert.False(t, ok)
		})
	}
}

// TestAwaitingMergeCardSubtypes: delivery pushed / PR / failed, a removed worktree, and the
// merged-but-unmarked event each get their own subtype (and the right buttons).
func TestAwaitingMergeCardSubtypes(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*jobstore.JobRecord)
		events  []string
		want    string
		actions []string
	}{
		{"pushed", func(r *jobstore.JobRecord) { r.DeliveryJSON = `{"mode":"push","state":"pushed"}` }, nil, AwaitingMergePushed, []string{"open"}},
		{"pr opened", func(r *jobstore.JobRecord) { r.DeliveryJSON = `{"mode":"pr","state":"pr_opened"}` }, nil, AwaitingMergePushed, []string{"open"}},
		{"failed", func(r *jobstore.JobRecord) { r.DeliveryJSON = `{"mode":"merge","state":"failed","error":"conflict"}` }, nil, AwaitingMergeFailed, []string{"open"}},
		{"discarded", func(r *jobstore.JobRecord) { r.WorktreePath = "" }, nil, AwaitingMergeDiscarded, []string{"todo_skip", "todo_done", "open"}},
		{"merged but not marked", func(r *jobstore.JobRecord) { r.WorktreePath = "" }, []string{job.EventTodoCompleteFailed}, AwaitingMergeUnmarked, []string{"todo_done", "open"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := awaitingMergeCard(t, jobstore.PlanCompleteOnMerge, tc.mut, jobstore.TodoDoing, tc.events...)
			assert.True(t, ok)
			assert.Eq(t, tc.want, c.Subtype)
			assert.Eq(t, tc.actions, actionIDs(c.Actions))
		})
	}
}
