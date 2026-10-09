package work

import (
	"context"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestParseSummaryMilestone(t *testing.T) {
	// Absent key (an older prompt / model): no milestone, still a valid summary.
	o, err := ParseSummary(goodJSON)
	assert.NoErr(t, err)
	assert.Eq(t, "", o.Milestone)

	o, err = ParseSummary(`{"goal":"g","milestone":"  CSV 导出\n完成  "}`)
	assert.NoErr(t, err)
	assert.Eq(t, "CSV 导出 完成", o.Milestone)

	long := strings.Repeat("长", 60)
	o, err = ParseSummary(`{"milestone":"` + long + `"}`)
	assert.NoErr(t, err)
	assert.Eq(t, 40, len([]rune(o.Milestone)))
	assert.True(t, strings.HasSuffix(o.Milestone, "…"))
}

func TestSummarizeWritesMilestone(t *testing.T) {
	out := strings.TrimSuffix(goodJSON, `}`) + `,"milestone":"导出按钮完成"}`
	svc, st, w, os := sumFixture(t, out)
	res, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	assert.NoErr(t, err)
	assert.Eq(t, "导出按钮完成", res.Milestone)
	assert.Contains(t, os.calls[0].Prompt, `"milestone"`)

	ms, err := st.ListWorkJournalLevel(w.ID, 0, 0, jobstore.WorkLevelMilestone)
	assert.NoErr(t, err)
	last := ms[len(ms)-1]
	assert.Eq(t, "导出按钮完成", last.Text)
	assert.Eq(t, jobstore.WorkJournalSteward, last.Kind)
	assert.Eq(t, "summarizer(claude)", last.By)

	// The flow line ("整理完成…") stays a detail.
	all, err := st.ListWorkJournal(w.ID, 0, 0)
	assert.NoErr(t, err)
	for _, e := range all {
		if strings.HasPrefix(e.Text, "整理完成") {
			assert.Eq(t, jobstore.WorkLevelDetail, e.Level)
		}
	}
}

func TestSummarizeWithoutMilestoneWritesNone(t *testing.T) {
	svc, st, w, _ := sumFixture(t, goodJSON)
	_, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	assert.NoErr(t, err)
	ms, err := st.ListWorkJournalLevel(w.ID, 0, 0, jobstore.WorkLevelMilestone)
	assert.NoErr(t, err)
	for _, e := range ms {
		assert.NotEq(t, jobstore.WorkJournalSteward, e.Kind)
	}
}
