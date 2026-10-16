package today

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestCaptureBoxSummaryCardAndNoPerItemCard(t *testing.T) {
	svc, st := newTestService(t)

	// Empty box: no card.
	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	_, ok := find(cards, CaptureCardKey)
	assert.False(t, ok)

	var ids []string
	for _, text := range []string{"想法一", "想法二", "想法三"} {
		w, err := st.CaptureWorkItem(jobstore.CaptureInput{Text: text, Kind: jobstore.WorkKindIdea, By: "human:t"})
		assert.NoErr(t, err)
		ids = append(ids, w.ID)
	}
	// A due reminder must not turn a captured item into a work card.
	past := ago(5)
	_, _, err = st.UpdateWorkItem(ids[0], jobstore.WorkItemPatch{RemindAt: &past}, 0, "human:t")
	assert.NoErr(t, err)

	cards, err = svc.Decisions(false)
	assert.NoErr(t, err)
	sum, ok := find(cards, CaptureCardKey)
	assert.True(t, ok)
	assert.Eq(t, KindCapture, sum.Kind)
	assert.Eq(t, "收集箱 3 条待处理", sum.Title)
	assert.Eq(t, []string{"open"}, actionIDs(sum.Actions))
	assert.Eq(t, CaptureBoxLink, sum.Actions[0].Value)
	for _, c := range cards {
		assert.NotEq(t, KindWork, c.Kind)
		for _, id := range ids {
			assert.NotEq(t, "work:"+id, c.Key)
		}
	}
	alive, err := svc.cardAlive(CaptureCardKey)
	assert.NoErr(t, err)
	assert.True(t, alive)

	// Archiving one drops the count.
	parked := jobstore.WorkParked
	_, _, err = st.UpdateWorkItem(ids[1], jobstore.WorkItemPatch{Status: &parked, FromCapture: true}, 0, "human:t")
	assert.NoErr(t, err)
	cards, _ = svc.Decisions(false)
	sum, _ = find(cards, CaptureCardKey)
	assert.Eq(t, "收集箱 2 条待处理", sum.Title)
}

func TestLanesExcludeCaptured(t *testing.T) {
	f := newFixture(t)
	c, err := f.st.CaptureWorkItem(jobstore.CaptureInput{Text: "随手记", Kind: jobstore.WorkKindIdea, By: "human:t"})
	assert.NoErr(t, err)
	n, err := f.st.CreateWorkItem(jobstore.WorkItemInput{Title: "普通"})
	assert.NoErr(t, err)
	v, err := f.b.Build()
	assert.NoErr(t, err)
	_, ok := laneByID(v, c.ID)
	assert.False(t, ok)
	_, ok = laneByID(v, n.ID)
	assert.True(t, ok)
}
