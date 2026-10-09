package work

import (
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestComputeHealth(t *testing.T) {
	now := int64(1_000_000)
	stall := 4 * time.Hour
	old := now - int64(5*time.Hour/time.Second)
	failed := &jobstore.JobRecord{ID: "20261009-abcdef", Status: "failed"}
	budget := &jobstore.JobRecord{ID: "20261009-bbbbbb", Status: "failed", FailureClass: "budget"}
	cases := []struct {
		name   string
		in     HealthInput
		want   string
		reason string
	}{
		{"ok", HealthInput{InProgress: true, LastActivity: now - 60}, HealthOK, ""},
		{"blocked plan wins", HealthInput{BlockedPlan: "「N2」", Blocker: "x", LastActivity: old, InProgress: true, LatestJob: failed}, HealthBlocked, "plan 「N2」 阻塞"},
		{"blocker", HealthInput{Blocker: "等  账号\n开通", LastActivity: old, InProgress: true}, HealthBlocked, "阻塞：等 账号 开通"},
		{"stalled", HealthInput{InProgress: true, LastActivity: old, LatestJob: failed}, HealthStalled, "5 小时没有活动"},
		{"running agent never stalls", HealthInput{InProgress: true, LastActivity: old, AgentRunning: true}, HealthOK, ""},
		{"waiting never stalls", HealthInput{InProgress: false, LastActivity: old}, HealthOK, ""},
		{"no activity known", HealthInput{InProgress: true}, HealthOK, ""},
		{"at risk failed", HealthInput{InProgress: true, LastActivity: now, LatestJob: failed}, HealthAtRisk, "最近的 job 20261009 失败"},
		{"at risk budget", HealthInput{LatestJob: budget}, HealthAtRisk, "最近的 job 20261009 预算熔断"},
		{"latest job fine", HealthInput{LatestJob: &jobstore.JobRecord{ID: "j", Status: "done"}}, HealthOK, ""},
	}
	for _, c := range cases {
		h, r := ComputeHealth(c.in, now, stall)
		assert.Eq(t, c.want, h, c.name)
		assert.Eq(t, c.reason, r, c.name)
	}
	assert.True(t, HealthRank(HealthBlocked) < HealthRank(HealthStalled))
	assert.True(t, HealthRank(HealthStalled) < HealthRank(HealthAtRisk))
	assert.True(t, HealthRank(HealthAtRisk) < HealthRank(HealthOK))
	_, r := ComputeHealth(HealthInput{InProgress: true, LastActivity: now - 3*24*3600}, now, stall)
	assert.Eq(t, "3 天没有活动", r)
}

func TestOutcomeLine(t *testing.T) {
	cases := []struct {
		o     JobOutcome
		level string
		frag  string
	}{
		{JobOutcome{ID: "j1", Agent: "codex", Status: "done", Commits: 3}, jobstore.WorkLevelMilestone, "完成，3 个提交"},
		{JobOutcome{ID: "j1", Status: "done"}, jobstore.WorkLevelDetail, "完成"},
		{JobOutcome{ID: "j1", Status: "needs_review", Commits: 1}, jobstore.WorkLevelMilestone, "完成，待验收，1 个提交"},
		{JobOutcome{ID: "j1", Status: "failed", Error: "exit 1"}, jobstore.WorkLevelMilestone, "失败：exit 1"},
		{JobOutcome{ID: "j1", Status: "timeout"}, jobstore.WorkLevelMilestone, "失败"},
		{JobOutcome{ID: "j1", Status: "failed", FailureClass: "budget"}, jobstore.WorkLevelMilestone, "预算熔断"},
		{JobOutcome{ID: "j1", Status: "cancelled"}, jobstore.WorkLevelDetail, "已取消"},
		{JobOutcome{ID: "j1", Status: "rejected", Reviewed: true}, jobstore.WorkLevelMilestone, "验收未通过"},
		{JobOutcome{ID: "j1", Status: "done", Reviewed: true}, jobstore.WorkLevelMilestone, "验收通过"},
	}
	for _, c := range cases {
		text, level := OutcomeLine(c.o)
		assert.Eq(t, c.level, level, text)
		assert.Contains(t, text, c.frag)
	}
}

func TestNoteJobOutcomeJournalsLinkedItems(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "w", By: "human"})
	assert.NoErr(t, err)
	_, err = st.AddWorkLink(w.ID, jobstore.WorkLinkJob, "job-1", "human")
	assert.NoErr(t, err)
	other, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "other", By: "human"})
	assert.NoErr(t, err)

	svc.NoteJobOutcome(JobOutcome{ID: "job-1", Agent: "codex", Status: "done", Commits: 2})
	svc.NoteJobOutcome(JobOutcome{ID: "job-1", Status: "cancelled"})
	j, err := st.ListWorkJournal(w.ID, 0, 0)
	assert.NoErr(t, err)
	n := len(j)
	assert.Eq(t, "job:job-1", j[n-2].By)
	assert.Eq(t, jobstore.WorkLevelMilestone, j[n-2].Level)
	assert.Contains(t, j[n-2].Text, "2 个提交")
	assert.Eq(t, jobstore.WorkLevelDetail, j[n-1].Level)

	oj, _ := st.ListWorkJournal(other.ID, 0, 0)
	assert.Len(t, oj, 1) // only its creation line
}

// TestItemViewHealth drives the view's health through the real store: a blocker, a
// failed latest linked job, a stall past work.stall_after, and a parked item that never
// stalls.
func TestItemViewHealth(t *testing.T) {
	svc, st, _ := newSvc(t)
	clk := &testClock{t: clockT0}
	st.SetClock(clk.Now)
	svc.SetNow(clk.Now)
	svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{StallAfter: "2h"} })

	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "w", By: "human"})
	assert.NoErr(t, err)
	d, err := svc.Detail(w.ID, 50)
	assert.NoErr(t, err)
	assert.Eq(t, HealthOK, d.Health)
	assert.Len(t, d.Milestones, 1) // the creation line

	// A failed linked job → at risk.
	job := jobstore.JobRecord{ID: "20261006-fail01", ProjectKey: "p", Agent: "codex", Runner: "local", Status: "failed",
		ResultDir: "/tmp/x", StartedAt: clockT0.Unix(), EndedAt: clockT0.Unix()}
	assert.NoErr(t, st.UpsertJob(job))
	_, err = st.AddWorkLink(w.ID, jobstore.WorkLinkJob, job.ID, "human")
	assert.NoErr(t, err)
	d, _ = svc.Detail(w.ID, 50)
	assert.Eq(t, HealthAtRisk, d.Health)
	assert.Contains(t, d.HealthReason, "失败")

	// Quiet for 3h → stalled (outranks at risk).
	clk.Advance(3 * time.Hour)
	d, _ = svc.Detail(w.ID, 50)
	assert.Eq(t, HealthStalled, d.Health)
	assert.Eq(t, "3 小时没有活动", d.HealthReason)

	// A blocker → blocked.
	blk := "等账号"
	_, err = svc.Update(w.ID, jobstore.WorkItemPatch{BlockerText: &blk}, 0, "human")
	assert.NoErr(t, err)
	d, _ = svc.Detail(w.ID, 50)
	assert.Eq(t, HealthBlocked, d.Health)

	// Parked and quiet: no stall, blocker cleared, latest job fine → ok.
	empty := ""
	_, err = svc.Update(w.ID, jobstore.WorkItemPatch{BlockerText: &empty}, 0, "human")
	assert.NoErr(t, err)
	job.Status = "done"
	assert.NoErr(t, st.UpsertJob(job))
	_, err = svc.Park(w.ID, 0, "", "human")
	assert.NoErr(t, err)
	clk.Advance(10 * time.Hour)
	d, _ = svc.Detail(w.ID, 50)
	assert.Eq(t, HealthOK, d.Health)
	assert.True(t, len(d.Milestones) <= milestonesInView)
	last := d.Milestones[len(d.Milestones)-1]
	assert.True(t, strings.Contains(last.Text, "parked"), last.Text)
}

func TestItemViewBlockedPlan(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "w", By: "human"})
	assert.NoErr(t, err)
	assert.NoErr(t, st.InsertPlan(jobstore.Plan{PlanID: "plan-blk00001", Title: "N2", Status: jobstore.PlanBlocked, CreatedAt: 1, UpdatedAt: 1}))
	_, err = st.AddWorkLink(w.ID, jobstore.WorkLinkPlan, "plan-blk00001", "human")
	assert.NoErr(t, err)
	d, err := svc.Detail(w.ID, 50)
	assert.NoErr(t, err)
	assert.Eq(t, HealthBlocked, d.Health)
	assert.Eq(t, "plan 「N2」 阻塞", d.HealthReason)
	assert.Len(t, d.Plans, 1)
}

// TestItemViewHeartbeatDoesNotHideStall: a current terminal session that only
// heartbeats (last_seen_at keeps moving, state idle) is not activity — the item still
// stalls once the journal and linked jobs are quiet past work.stall_after.
func TestItemViewHeartbeatDoesNotHideStall(t *testing.T) {
	svc, st, _ := newSvc(t)
	clk := &testClock{t: clockT0}
	st.SetClock(clk.Now)
	svc.SetNow(clk.Now)
	svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{StallAfter: "2h"} })

	_, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-hb", Agent: "claude", State: jobstore.SessionIdle})
	assert.NoErr(t, err)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "w", By: "human", SessionIDs: []string{"sess-hb"}})
	assert.NoErr(t, err)
	clk.Advance(3 * time.Hour)
	_, err = st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-hb", Agent: "claude", State: jobstore.SessionIdle})
	assert.NoErr(t, err)

	d, err := svc.Detail(w.ID, 50)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkActive, d.Status)
	assert.Eq(t, HealthStalled, d.Health)
	// The view's own activity time still shows the heartbeat (sort order unchanged).
	assert.Eq(t, clk.Now().Unix(), d.LastActivityAt)

	// A running session still prevents stalled.
	_, err = st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-hb", Agent: "claude", State: jobstore.SessionRunning})
	assert.NoErr(t, err)
	d, _ = svc.Detail(w.ID, 50)
	assert.Eq(t, HealthOK, d.Health)
}

// TestItemViewQueuedRerunIsLatest: a rerun queued on a remote runner (started_at still
// 0) after a failed job is the latest job — the item is not "at risk" for the old failure.
func TestItemViewQueuedRerunIsLatest(t *testing.T) {
	svc, st, _ := newSvc(t)
	clk := &testClock{t: clockT0}
	st.SetClock(clk.Now)
	svc.SetNow(clk.Now)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "w", By: "human"})
	assert.NoErr(t, err)
	failed := jobstore.JobRecord{ID: "20261006-fail02", ProjectKey: "p", Agent: "codex", Runner: "w1", Status: "failed",
		ResultDir: "/tmp/x", StartedAt: clockT0.Unix() - 600, EndedAt: clockT0.Unix() - 60, UpdatedAt: clockT0.Unix() - 60}
	rerun := jobstore.JobRecord{ID: "20261006-rerun2", ProjectKey: "p", Agent: "codex", Runner: "w1", Status: "queued",
		ResultDir: "/tmp/y", StartedAt: 0, UpdatedAt: clockT0.Unix()}
	assert.NoErr(t, st.UpsertJob(failed))
	assert.NoErr(t, st.UpsertJob(rerun))
	for _, id := range []string{failed.ID, rerun.ID} {
		_, err = st.AddWorkLink(w.ID, jobstore.WorkLinkJob, id, "human")
		assert.NoErr(t, err)
	}
	d, err := svc.Detail(w.ID, 50)
	assert.NoErr(t, err)
	assert.Eq(t, rerun.ID, d.LinkedJobs[0].ID)
	assert.Eq(t, HealthOK, d.Health)
}

// TestItemViewFinalSkipsLinkedJobs: a finished item is ok and reads no plans / jobs.
func TestItemViewFinalSkipsLinkedJobs(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "w", By: "human"})
	assert.NoErr(t, err)
	job := jobstore.JobRecord{ID: "20261006-fail03", ProjectKey: "p", Agent: "codex", Runner: "local", Status: "failed",
		ResultDir: "/tmp/x", StartedAt: 1, EndedAt: 2}
	assert.NoErr(t, st.UpsertJob(job))
	_, err = st.AddWorkLink(w.ID, jobstore.WorkLinkJob, job.ID, "human")
	assert.NoErr(t, err)
	done := jobstore.WorkDone
	_, err = svc.Update(w.ID, jobstore.WorkItemPatch{Status: &done}, 0, "human")
	assert.NoErr(t, err)
	items, err := svc.List(jobstore.WorkListOpts{IncludeClosed: true})
	assert.NoErr(t, err)
	assert.Len(t, items, 1)
	assert.Eq(t, HealthOK, items[0].Health)
	assert.Len(t, items[0].LinkedJobs, 0)
	assert.True(t, len(items[0].Milestones) > 0)
}
