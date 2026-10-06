package steward

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func setStatus(t *testing.T, e *env, id, status string) {
	t.Helper()
	assert.NoErr(t, setStatusErr(e, id, status))
}

func setStatusErr(e *env, id, status string) error {
	_, _, err := e.st.UpdateWorkItem(id, jobstore.WorkItemPatch{Status: &status}, 0, "human:a")
	return err
}

// Over the cap the prime keeps 等我 first, then 到期, then 需现场/等资源, and drops the rest
// (saying how many it left out); the role and the notes are never cut.
func TestPrimeTruncatesByPriority(t *testing.T) {
	e := newEnv(t)
	e.svc.primeMax = 7600
	pad := strings.Repeat("很长的标题", 12)

	var needsMe, due, onsite, waiting, others []string
	for i := 0; i < 4; i++ {
		w := e.item(t, fmt.Sprintf("等我-%d-%s", i, pad))
		setStatus(t, e, w.ID, jobstore.WorkNeedsMe)
		needsMe = append(needsMe, w.ID)
	}
	for i := 0; i < 3; i++ {
		w := e.item(t, fmt.Sprintf("到期-%d-%s", i, pad))
		past := time.Now().Add(-time.Hour).Unix()
		_, _, err := e.st.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{RemindAt: &past}, 0, "human:a")
		assert.NoErr(t, err)
		due = append(due, w.ID)
	}
	for i := 0; i < 3; i++ {
		w := e.item(t, fmt.Sprintf("现场-%d-%s", i, pad))
		setStatus(t, e, w.ID, jobstore.WorkNeedsOnsite)
		onsite = append(onsite, w.ID)
		w2 := e.item(t, fmt.Sprintf("资源-%d-%s", i, pad))
		setStatus(t, e, w2.ID, jobstore.WorkWaitingResource)
		waiting = append(waiting, w2.ID)
	}
	for i := 0; i < 25; i++ {
		w := e.item(t, fmt.Sprintf("其他-%d-%s", i, pad))
		others = append(others, w.ID)
	}
	_, err := e.svc.SetNotes("- 长期偏好：周三去现场\n", "human:a", 0)
	assert.NoErr(t, err)

	p, err := e.svc.BuildPrime(time.Now())
	assert.NoErr(t, err)
	assert.True(t, p.Bytes <= e.svc.primeMax, fmt.Sprintf("prime %d bytes over the %d cap", p.Bytes, e.svc.primeMax))
	assert.True(t, strings.Contains(p.Text, "# 你是工作管家"))
	assert.True(t, strings.Contains(p.Text, "长期偏好：周三去现场"))
	assert.True(t, p.Stats.Truncated)
	assert.Eq(t, 38, p.Stats.ItemsTotal)
	assert.True(t, p.Stats.ItemsShown < 38)

	// The item section only (the journal below also names items that were cut).
	section := p.Text[strings.Index(p.Text, "## 未结工作项"):strings.Index(p.Text, "## 在途请求账本")]

	// Everything waiting on me made it; the lowest class is what got dropped first.
	for _, id := range needsMe {
		assert.True(t, strings.Contains(section, id), "needs_me item "+id+" must be in the prime")
	}
	shownOthers := 0
	for _, id := range others {
		if strings.Contains(section, id) {
			shownOthers++
		}
	}
	assert.True(t, shownOthers < len(others))
	assert.True(t, strings.Contains(p.Text, fmt.Sprintf("另有 %d 项未列出", 38-p.Stats.ItemsShown)))

	// Strict priority: once any item of a class is cut, no item of a LOWER class is shown.
	classes := [][]string{needsMe, due, append(append([]string{}, onsite...), waiting...), others}
	cut := false
	for _, ids := range classes {
		shown := 0
		for _, id := range ids {
			if strings.Contains(section, id) {
				shown++
			}
		}
		if cut {
			assert.Eq(t, 0, shown)
		}
		if shown < len(ids) {
			cut = true
		}
	}
	// The order inside the prime follows the classes.
	idxMe := strings.Index(p.Text, needsMe[0])
	idxDue := strings.Index(p.Text, due[0])
	assert.True(t, idxMe >= 0 && idxDue > idxMe)
}

func TestPrimeListsTheLedgerAndRecentJournalWithinTheCap(t *testing.T) {
	e := newEnv(t)
	w := e.item(t, "整理线路")
	_, err := e.st.AppendWorkJournal(w.ID, jobstore.WorkJournalNote, "供应商说周四到货", "human:a")
	assert.NoErr(t, err)
	_, err = e.st.CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: w.ID, Kind: jobstore.WorkRequestReport, By: "steward(x)"})
	assert.NoErr(t, err)
	p, err := e.svc.BuildPrime(time.Now())
	assert.NoErr(t, err)
	for _, want := range []string{"## 在途请求账本", "汇报请求", "待发送", "## 最近 24 小时的工作项日志", "供应商说周四到货", w.ID} {
		assert.True(t, strings.Contains(p.Text, want), want)
	}
	assert.True(t, p.Bytes <= e.svc.primeMax)
	assert.False(t, p.Stats.Truncated)

	// A finished request older than 24h is no longer listed; an in-flight one always is.
	old, _ := e.st.CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: w.ID, Kind: jobstore.WorkRequestSummarize, By: "system"})
	_, _, _ = e.st.MarkWorkRequest(old.ID, jobstore.WorkRequestAnswered, "", "")
	p, _ = e.svc.BuildPrime(time.Now().Add(48 * time.Hour))
	assert.False(t, strings.Contains(p.Text, old.ID))
}

func TestPrimeWithNothingSaysSo(t *testing.T) {
	e := newEnv(t)
	p, err := e.svc.BuildPrime(time.Now())
	assert.NoErr(t, err)
	for _, want := range []string{"还没有笔记", "没有未结工作项", "没有在途请求", "最近 24 小时没有日志"} {
		assert.True(t, strings.Contains(p.Text, want), want)
	}
}

func TestPrimeNeverCutsTheNotesButCapsAHugeOne(t *testing.T) {
	e := newEnv(t)
	huge := strings.Repeat("规则一条。\n", 1000) // 16000 bytes: under the 16KB hard cap, over the prime's 12KB cap
	_, err := e.svc.SetNotes(huge, "human:a", 0)
	assert.NoErr(t, err)
	p, err := e.svc.BuildPrime(time.Now())
	assert.NoErr(t, err)
	assert.True(t, strings.Contains(p.Text, "笔记过长"))
	assert.True(t, strings.Contains(p.Text, "（v1，"))
}
