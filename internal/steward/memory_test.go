package steward

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// P4: a review with no changed work item still runs when the server-side memory doctor
// has a finding the steward was never shown; its prompt carries the memory step.
func TestReviewProposesMemoryCleanup(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 1, 5, 9, 0, 0, 0, time.Local)
	e.st.SetClock(func() time.Time { return base })
	e.item(t, "旧事")
	e.setCutoff(base.Add(time.Hour))

	h := MemoryHygiene{}
	e.svc.SetMemoryHygiene(func() (MemoryHygiene, error) { return h, nil })
	res, err := e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.True(t, res.Skipped)

	h = MemoryHygiene{Findings: 2, Remaining: 5, Signatures: []string{"trk/a/archive", "trk/b/summary"},
		Lines: []string{"trk · a（note）：note-stale", "trk · b（rule）：summary-missing"}}
	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.False(t, res.Skipped)
	e.svc.WaitIdle()
	prompt := e.host.started[0].Prompt
	for _, want := range []string{"仓库记忆整理", "2 条有待处理的问题（今天还可提 5 条建议）", "trk · a（note）：note-stale", "gofer_memory_findings", "gofer_memory_suggest", "30 天内不要再提"} {
		assert.True(t, strings.Contains(prompt, want), want)
	}

	// The same findings do not wake another review; a new one does, unless today's cap is used up.
	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.True(t, res.Skipped)
	h.Signatures = append(h.Signatures, "trk/c/merge")
	h.Remaining = 0
	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.True(t, res.Skipped)
	h.Remaining = 3
	res, err = e.svc.RunReview(context.Background(), ReviewOpts{Trigger: TriggerDaily})
	assert.NoErr(t, err)
	assert.False(t, res.Skipped)
}

func TestMemorySectionStates(t *testing.T) {
	assert.Eq(t, "", memorySection(5, nil))
	assert.Eq(t, "", memorySection(5, &MemoryHygiene{Findings: 3, Remaining: 0}))
	sec := memorySection(5, &MemoryHygiene{Findings: 10, Remaining: 2, Lines: []string{"x"}})
	assert.True(t, strings.HasPrefix(sec, "5. 仓库记忆整理"))
	assert.True(t, strings.Contains(sec, "…另有 9 条"))
}
