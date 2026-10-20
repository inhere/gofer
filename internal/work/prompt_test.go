package work

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/settings"
)

// promptItem is a work item to render a prompt for (no session needed: the material is a
// parameter).
func promptItem(t *testing.T, st *jobstore.Store) jobstore.WorkItem {
	t.Helper()
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "订单导出", Goal: "给订单页加导出", By: "human:a"})
	assert.NoErr(t, err)
	return w
}

// endsWithSpec asserts the fixed JSON format spec closes the prompt, the one property the
// prompt settings must never break (ParseSummary reads that JSON).
func endsWithSpec(t *testing.T, p string) {
	t.Helper()
	if got := strings.TrimRight(p, "\n"); !strings.HasSuffix(got, summaryFormatSpec) {
		t.Fatalf("prompt does not end with the JSON format spec:\n%s", p)
	}
}

func TestSummarizerPromptDefaults(t *testing.T) {
	svc, st, _ := noiseSvc(t)
	w := promptItem(t, st)

	p := svc.buildPrompt(w, "MATERIAL-1", false)
	assert.True(t, strings.HasPrefix(p, defaultSummarizerPrompt))
	assert.False(t, strings.Contains(p, settings.ExtraHeading))
	// The rewritten default says what the noise rule wants and keeps the parse keys.
	for _, want := range []string{"试用、测试、演示性质的操作不算工作进展", "信息不足", "milestone"} {
		assert.True(t, strings.Contains(p, want), want)
	}
	assert.True(t, strings.Contains(p, "MATERIAL-1"))
	assert.True(t, strings.Contains(p, "标题：订单导出"))
	endsWithSpec(t, p)
}

func TestSummarizerPromptReplacement(t *testing.T) {
	svc, st, set := noiseSvc(t)
	w := promptItem(t, st)
	_, _, err := set.Set(settings.KeyPromptWorkSummarizer, "只记录有实质进展的事。", "human:me")
	assert.NoErr(t, err)

	p := svc.buildPrompt(w, "MATERIAL-2", false)
	assert.True(t, strings.HasPrefix(p, "只记录有实质进展的事。"))
	assert.False(t, strings.Contains(p, "你是一个只读的"))
	// The material still comes from the code, so a replacement cannot lose it.
	assert.True(t, strings.Contains(p, "MATERIAL-2"))
	assert.True(t, strings.Contains(p, "标题：订单导出"))
	endsWithSpec(t, p)
}

func TestSummarizerPromptExtraIsAppended(t *testing.T) {
	svc, st, set := noiseSvc(t)
	w := promptItem(t, st)
	_, _, err := set.Set(settings.KeyPromptWorkSummarizerExtra, "少说废话；不确定的字段一律留空。", "human:me")
	assert.NoErr(t, err)

	p := svc.buildPrompt(w, "MATERIAL-3", true)
	// The extra follows the default instruction it was appended to.
	assert.True(t, strings.HasPrefix(p, defaultSummarizerPrompt))
	assert.True(t, strings.Contains(p, settings.ExtraHeading+"\n少说废话；不确定的字段一律留空。"))
	assert.True(t, strings.Index(p, settings.ExtraHeading) > strings.Index(p, "记录原则"))
	assert.True(t, strings.Index(p, settings.ExtraHeading) < strings.Index(p, "【工作项当前记录】"))
	endsWithSpec(t, p)
}

func TestSummarizerPromptReplacementAndExtra(t *testing.T) {
	svc, st, set := noiseSvc(t)
	w := promptItem(t, st)
	_, _, err := set.Set(settings.KeyPromptWorkSummarizer, "自己写的指令。", "human:me")
	assert.NoErr(t, err)
	_, _, err = set.Set(settings.KeyPromptWorkSummarizerExtra, "补充一条偏好。", "human:me")
	assert.NoErr(t, err)

	p := svc.buildPrompt(w, "MATERIAL-4", false)
	assert.True(t, strings.HasPrefix(p, "自己写的指令。\n\n"+settings.ExtraHeading+"\n补充一条偏好。"))
	assert.False(t, strings.Contains(p, "你是一个只读的"))
	endsWithSpec(t, p)
}

// A replacement that tells the model to answer in prose can still not remove the format
// spec: it is appended after everything, and a prompt that parses keeps working.
func TestReplacementCannotRemoveTheFormatSpec(t *testing.T) {
	svc, st, set := noiseSvc(t)
	w := promptItem(t, st)
	_, _, err := set.Set(settings.KeyPromptWorkSummarizer, "不要输出 JSON。", "human:me")
	assert.NoErr(t, err)

	p := svc.buildPrompt(w, "MATERIAL-5", false)
	endsWithSpec(t, p)
	assert.True(t, strings.Index(p, "不要输出 JSON。") < strings.Index(p, summaryFormatSpec))
}

// A settings read failure falls back to the code default (and never blocks a tidy-up).
func TestSummarizerPromptSettingsFailureFallsBack(t *testing.T) {
	svc, st, _ := noiseSvc(t)
	svc.SetSettings(errSettings{})
	w := promptItem(t, st)

	p := svc.buildPrompt(w, "MATERIAL-6", false)
	assert.True(t, strings.HasPrefix(p, defaultSummarizerPrompt))
	assert.False(t, strings.Contains(p, settings.ExtraHeading))
	endsWithSpec(t, p)
}
