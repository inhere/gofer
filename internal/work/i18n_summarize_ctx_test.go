package work

import (
	"context"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestSummarizeFailureReasonLanguages: a failure reason is stored in the request ledger in
// the item's project language and returned in the caller's language (ctx), the two
// being written separately from the same catalog key (design S3a rule 3).
func TestSummarizeFailureReasonLanguages(t *testing.T) {
	svc, st, w, _ := sumFixture(t, goodJSON)
	svc.SetLanguageFor(func(string) i18n.Lang { return i18n.ZhCN })
	svc.SetOneShot(nil)
	req, _ := st.CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: w.ID, Kind: jobstore.WorkRequestSummarize})
	ctx := i18n.WithLang(context.Background(), i18n.EN)
	_, err := svc.RunSummarize(ctx, w.ID, SummarizeOpts{Cause: CauseRequest, RequestID: req.ID})
	assert.Err(t, err)
	assert.Eq(t, i18n.T(i18n.EN, "work.summarize.engine_missing"), err.Error())
	r, _, _ := st.GetWorkRequest(req.ID)
	assert.Eq(t, i18n.T(i18n.ZhCN, "work.summarize.engine_missing"), r.Error)
	assert.Eq(t, "整理器未接入", r.Error)

	// No language in ctx: the caller sees the project's language too.
	_, err = svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	assert.Eq(t, "整理器未接入", err.Error())
}

// TestSummarizerStatusReasonFollowsContext: the status text is shown in the settings page,
// so ctx's request language beats the server's.
func TestSummarizerStatusReasonFollowsContext(t *testing.T) {
	svc, _, _, _ := sumFixture(t, goodJSON)
	svc.SetLanguageFor(func(string) i18n.Lang { return i18n.ZhCN })
	svc.SetOneShot(nil)
	assert.Eq(t, "整理功能未接入（server 没有提供一次性 job 通道）", svc.SummarizerStatus(context.Background()).Reason)
	en := svc.SummarizerStatus(i18n.WithLang(context.Background(), i18n.EN)).Reason
	assert.Eq(t, i18n.T(i18n.EN, "work.summarize.not_wired"), en)
	assert.False(t, han.MatchString(en))
}

// TestSummarizerPromptFollowsProjectLanguage: an English project gets the English
// instruction, labels and transcript role labels, and the JSON format closes the prompt.
func TestSummarizerPromptFollowsProjectLanguage(t *testing.T) {
	svc, _, w, fake := sumFixture(t, goodJSON)
	svc.SetLanguageFor(func(string) i18n.Lang { return i18n.EN })
	_, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	assert.NoErr(t, err)
	p := fake.calls[0].Prompt
	assert.True(t, strings.HasPrefix(p, defaultSummarizerPrompt(i18n.EN)))
	assert.True(t, strings.Contains(p, "[Current record of the work item]"))
	assert.True(t, strings.Contains(p, "User:\n"), p)
	assert.True(t, strings.HasSuffix(strings.TrimRight(p, "\n"), summaryFormatSpec(i18n.EN)))
}
