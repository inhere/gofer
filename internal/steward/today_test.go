package steward

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// N3 T4: a review with no changed work item still runs while 「今天」 cards wait for advice,
// and its prompt carries the advice step; with nothing unadvised the step is left out.
func TestReviewAdvisesOnTheTodayQueue(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 1, 5, 9, 0, 0, 0, time.Local)
	e.st.SetClock(func() time.Time { return base })
	e.item(t, "旧事")
	e.setCutoff(base.Add(time.Hour))

	var pending []string
	e.svc.SetTodayUnadvised(func() ([]string, error) { return pending, nil })
	res, err := e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.True(t, res.Skipped)

	pending = []string{"review:j1", "suggestion:w1/goal", "merge:w2/0"}
	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.False(t, res.Skipped)
	e.svc.WaitIdle()
	prompt := e.host.started[0].Prompt
	for _, want := range []string{"有 3 张卡还没有建议", "gofer_today_list", "gofer_today_card", "gofer_today_advise", "不给 action_id、不替人选", "6. 最后调用"} {
		assert.True(t, strings.Contains(prompt, want), want)
	}
	// The role prompt names the tools.
	assert.True(t, strings.Contains(prompt, "gofer_today_advise（"))

	// The same cards, skipped by the steward, do not wake another review on their own;
	// a new card does.
	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.True(t, res.Skipped)
	pending = append(pending, "review:j9")
	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.False(t, res.Skipped)
}

func TestTodaySectionStates(t *testing.T) {
	assert.Eq(t, "", todaySection(5, 0))
	assert.True(t, strings.HasPrefix(todaySection(5, -1), "5. 「今天」待决策队列："))
	p := reviewPrompt(TriggerDaily, "2026-10-06", nil, 0, nil, false, time.Now(), 0)
	assert.False(t, strings.Contains(p, "gofer_today_advise"))
	assert.True(t, strings.Contains(p, "5. 最后调用"))
}
