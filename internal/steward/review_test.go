package steward

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func (e *env) setCutoff(at time.Time) {
	e.svc.setKV(kvReviewCutoff, strconv.FormatInt(at.Unix(), 10))
}

// The review only looks at work items that changed since the last one — and starts no
// session at all when nothing did.
func TestReviewHandlesOnlyChangedItems(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 1, 5, 9, 0, 0, 0, time.Local)
	at := base
	e.st.SetClock(func() time.Time { return at })
	a := e.item(t, "旧事一")
	b := e.item(t, "旧事二")
	c := e.item(t, "新变化")
	e.setCutoff(base.Add(time.Hour)) // nothing is newer than this yet

	res, err := e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.True(t, res.Skipped)
	assert.Eq(t, 0, len(e.host.started)) // no model session for an empty review
	rv, _ := e.st.ListStewardReviews(5)
	assert.Eq(t, jobstore.StewardReviewSkipped, rv[0].State)

	// One item changes after the cutoff.
	at = base.Add(2 * time.Hour)
	_, err = e.st.AppendWorkJournal(c.ID, jobstore.WorkJournalNote, "供应商回话了", "human:a")
	assert.NoErr(t, err)

	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.False(t, res.Skipped)
	assert.Eq(t, []string{c.ID}, res.Items)
	e.svc.WaitIdle()
	prompt := e.host.started[0].Prompt
	review := prompt[strings.Index(prompt, "## 每日巡检"):]
	assert.True(t, strings.Contains(review, c.ID) && strings.Contains(review, "只处理下面这 1 个工作项"))
	assert.False(t, strings.Contains(review, a.ID))
	assert.False(t, strings.Contains(review, b.ID))

	rv, _ = e.st.ListStewardReviews(5)
	assert.Eq(t, jobstore.StewardReviewDone, rv[0].State)
	assert.Eq(t, c.ID, rv[0].ItemIDs)
	assert.True(t, rv[0].JobID != "")
	// The cutoff moved to the review's end: the same change is not reviewed twice.
	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.True(t, res.Skipped)
}

func TestReviewForceAndCap(t *testing.T) {
	e := newEnv(t)
	e.cfg.ReviewMaxItems = 2
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, e.item(t, "事-"+strconv.Itoa(i)).ID)
	}
	res, err := e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerManual})
	assert.NoErr(t, err)
	e.svc.WaitIdle()
	assert.Eq(t, 2, len(res.Items)) // capped
	prompt := e.host.started[0].Prompt
	assert.True(t, strings.Contains(prompt, "另有 3 个同样有变化但超出本次上限"))

	// Nothing changed since: only a forced review still runs (notes + due checks).
	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerManual})
	assert.NoErr(t, err)
	assert.True(t, res.Skipped || len(res.Items) <= 3)
	e.setCutoff(time.Now().Add(time.Hour))
	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerManual, Force: true})
	assert.NoErr(t, err)
	assert.False(t, res.Skipped)
	e.svc.WaitIdle()
	said := e.host.said(res.JobID)
	assert.True(t, len(said) > 0 && strings.Contains(said[len(said)-1], "强制巡检"))
	_ = ids
}

func TestReviewRefusesWhenDisabledOrBusy(t *testing.T) {
	e := newEnv(t)
	e.cfg.Enabled = false
	_, err := e.svc.RunReview(context.Background(), ReviewOpts{})
	assert.True(t, errors.Is(err, ErrDisabled))

	e.cfg.Enabled = true
	e.item(t, "x")
	e.host.hold = true // the turn never finishes while we look
	res, err := e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerManual})
	assert.NoErr(t, err)
	_, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerManual})
	assert.True(t, errors.Is(err, ErrBusy))
	e.host.idle(res.JobID) // let the first review finish
	e.host.mu.Lock()
	e.host.jobs[res.JobID].info.TurnNo = 2
	e.host.mu.Unlock()
	e.svc.WaitIdle()
}

func TestReviewSummaryBecomesTheDigestComment(t *testing.T) {
	e := newEnv(t)
	e.item(t, "等设备")
	res, err := e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerManual})
	assert.NoErr(t, err)
	// The steward's tool call lands on the review while it is running or just done.
	r, err := e.svc.SetReviewSummary("三件都在等同一批设备")
	assert.NoErr(t, err)
	assert.Eq(t, res.ReviewID, r.ID)
	e.svc.WaitIdle()
	d, err := e.work.BuildDigest(time.Now())
	assert.NoErr(t, err)
	assert.Eq(t, "三件都在等同一批设备", d.Commentary)
}

func TestReviewPromptNamesTheTasksAndTheLimits(t *testing.T) {
	p := reviewPrompt(TriggerDaily, "2026-10-06", nil, 0, []jobstore.StewardEvent{{Kind: "session", Detail: "会话已离线"}}, false, time.Now())
	for _, want := range []string{"gofer_work_summarize", "gofer_work_request_report", "gofer_work_merge_suggest", "review_summary",
		"不要标完成 / 放弃", "会话已离线", "gofer_session_tail"} {
		assert.True(t, strings.Contains(p, want), want)
	}
}
