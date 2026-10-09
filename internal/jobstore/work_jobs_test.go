package jobstore

import (
	"fmt"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestWorkItemLinkedJobs: one query returns what the item's current sessions watch, its
// job links and the newest perPlan jobs of each linked plan, newest submitted first (a
// queued remote job with started_at=0 sorts by its submit/updated time), capped at limit.
func TestWorkItemLinkedJobs(t *testing.T) {
	s := openTest(t)
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "sess-a", Agent: "claude"})
	assert.NoErr(t, err)
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "sess-past", Agent: "claude"})
	assert.NoErr(t, err)
	w, err := s.CreateWorkItem(WorkItemInput{Title: "t", By: "human", SessionIDs: []string{"sess-a", "sess-past"}})
	assert.NoErr(t, err)
	assert.NoErr(t, s.DetachWorkSession(w.ID, "sess-past", "human"))

	put := func(id string, started, updated int64, plan string) {
		j := sampleJob(id, "p", started)
		j.UpdatedAt, j.PlanID = updated, plan
		assert.NoErr(t, s.UpsertJob(j))
	}
	put("job-watch", 100, 100, "")
	put("job-link", 200, 200, "")
	put("job-queued", 0, 500, "")
	put("job-past", 400, 400, "")
	for i := 0; i < 4; i++ {
		put(fmt.Sprintf("job-plan%d", i), int64(10+i), int64(10+i), "plan-x0000001")
	}
	put("job-unrelated", 900, 900, "")
	_, err = s.AddSessionJobWatch("sess-a", "job-watch")
	assert.NoErr(t, err)
	_, err = s.AddSessionJobWatch("sess-a", "job-queued")
	assert.NoErr(t, err)
	_, err = s.AddSessionJobWatch("sess-past", "job-past")
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(w.ID, WorkLinkJob, "job-link", "human")
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(w.ID, WorkLinkPlan, "plan-x0000001", "human")
	assert.NoErr(t, err)

	ids := func(recs []JobRecord) []string {
		out := []string{}
		for _, r := range recs {
			out = append(out, r.ID)
		}
		return out
	}
	got, err := s.WorkItemLinkedJobs(w.ID, 50, 2)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"job-queued", "job-link", "job-watch", "job-plan3", "job-plan2"}, ids(got))

	got, err = s.WorkItemLinkedJobs(w.ID, 2, 2)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"job-queued", "job-link"}, ids(got))

	m, err := s.GetJobsByIDs([]string{"job-link", "job-link", "nope", ""})
	assert.NoErr(t, err)
	assert.Len(t, m, 1)
	assert.Eq(t, "job-link", m["job-link"].ID)
	m, err = s.GetJobsByIDs(nil)
	assert.NoErr(t, err)
	assert.Len(t, m, 0)
}
