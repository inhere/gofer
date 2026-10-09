package today

import (
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// seedAdviceQueue puts a review card, an interaction card and a decision card in the queue.
func seedAdviceQueue(t *testing.T, st *jobstore.Store) {
	t.Helper()
	putJob(t, st, jobstore.JobRecord{ID: "j-rev", ProjectKey: "p1", Agent: "codex", Status: job.StatusNeedsReview, EndedAt: ago(30)})
	putJob(t, st, jobstore.JobRecord{ID: "j-run", ProjectKey: "p1", Agent: "codex", Status: job.StatusRunning})
	assert.NoErr(t, st.UpsertInteraction(jobstore.InteractionRecord{ID: "i1", JobID: "j-run", Type: job.InteractionTypePermission,
		Prompt: "go test ./...", Status: "pending", CreatedAt: ago(5),
		OptionsJSON: `[{"value":"a1","label":"允许","kind":"allow_once"},{"value":"r1","label":"拒绝","kind":"reject_once"}]`}))
	assert.NoErr(t, st.InsertDecision(&jobstore.PlanDecision{ID: "d1", Title: "分页方式", Question: "A 还是 B？", OptionsJSON: `["A","B"]`,
		AskedAt: ago(5), TimeoutSec: 86400}))
}

func TestAdviseValidation(t *testing.T) {
	svc, st := newTestService(t)
	seedAdviceQueue(t, st)

	cases := []struct {
		name string
		in   AdviceInput
		want error
	}{
		{"no key", AdviceInput{Text: "x"}, ErrInvalidAdvice},
		{"no text", AdviceInput{CardKey: "review:j-rev"}, ErrInvalidAdvice},
		{"text too long", AdviceInput{CardKey: "review:j-rev", Text: strings.Repeat("长", AdviceTextRunes+1)}, ErrInvalidAdvice},
		{"digest too long", AdviceInput{CardKey: "review:j-rev", Text: "x", Digest: "1\n2\n3\n4\n5\n6"}, ErrInvalidAdvice},
		{"unknown card", AdviceInput{CardKey: "review:nope", Text: "x"}, ErrUnknownCard},
		{"not an action", AdviceInput{CardKey: "review:j-rev", Text: "x", ActionID: "merge"}, ErrInvalidAdvice},
		{"link action", AdviceInput{CardKey: "review:j-rev", Text: "x", ActionID: "diff"}, ErrInvalidAdvice},
		{"bare answer", AdviceInput{CardKey: "interaction:j-run/i1", Text: "x", ActionID: "answer"}, ErrInvalidAdvice},
		{"decision pick", AdviceInput{CardKey: "decision:d1", Text: "x", ActionID: "answer:A"}, ErrInvalidAdvice},
	}
	for _, c := range cases {
		_, err := svc.Advise(c.in, "steward(codex)", "st-1")
		if !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	rows, err := st.ListDecisionAdvice()
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(rows))

	// Valid: an interaction option, a decision's background (blank lines in the digest dropped).
	a, err := svc.Advise(AdviceInput{CardKey: "interaction:j-run/i1", Text: "  只读命令，  可以允许 ", ActionID: "answer:a1"}, "steward(codex)", "st-1")
	assert.NoErr(t, err)
	assert.Eq(t, "只读命令， 可以允许", a.Text)
	assert.Eq(t, "answer:a1", a.ActionID)
	assert.Eq(t, testNow.Unix(), a.At)
	_, err = svc.Advise(AdviceInput{CardKey: "decision:d1", Text: "B 与现有接口一致", Digest: "\n背景一\n\n背景二\n"}, "steward(codex)", "")
	assert.NoErr(t, err)
	got, ok, err := st.GetDecisionAdvice("decision:d1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "背景一\n背景二", got.Digest)
}

func TestTodayFillsAdviceAndPrunesGoneCards(t *testing.T) {
	svc, st := newTestService(t)
	seedAdviceQueue(t, st)
	_, err := svc.Advise(AdviceInput{CardKey: "review:j-rev", Text: "改动小、verify 通过", ActionID: "accept",
		Digest: "改动：导出分页\n风险：低\n测试：已补"}, "steward(codex)", "st-1")
	assert.NoErr(t, err)
	_, err = svc.Advise(AdviceInput{CardKey: "interaction:j-run/i1", Text: "允许", ActionID: "answer:a1"}, "steward(codex)", "st-1")
	assert.NoErr(t, err)
	// An exec review hidden by the toggle keeps its advice while the job still waits.
	putJob(t, st, jobstore.JobRecord{ID: "j-exec", ProjectKey: "p1", Agent: "exec", Status: job.StatusNeedsReview, EndedAt: ago(10)})
	_, err = svc.Advise(AdviceInput{CardKey: "review:j-exec", Text: "可以通过", ActionID: "accept"}, "human:me", "")
	assert.NoErr(t, err)
	// A stale row whose card is long gone.
	_, err = st.UpsertDecisionAdvice(jobstore.DecisionAdvice{CardKey: "plan_blocked:p-gone", Text: "旧"})
	assert.NoErr(t, err)

	resp, err := svc.Today(Query{})
	assert.NoErr(t, err)
	rev, ok := find(resp.Decisions, "review:j-rev")
	assert.True(t, ok)
	assert.NotNil(t, rev.Advice)
	assert.Eq(t, "accept", rev.Advice.ActionID)
	assert.Eq(t, "steward(codex)", rev.Advice.By)
	assert.Eq(t, "改动：导出分页\n风险：低\n测试：已补", rev.Review.Digest)
	dec, _ := find(resp.Decisions, "decision:d1")
	assert.Nil(t, dec.Advice)
	assert.Eq(t, 2, resp.Status.StewardToday.Advice) // the steward's two; the person's advice is not counted
	_, gone, _ := st.GetDecisionAdvice("plan_blocked:p-gone")
	assert.False(t, gone)
	_, kept, _ := st.GetDecisionAdvice("review:j-exec")
	assert.True(t, kept)

	n, err := svc.UnadvisedCount()
	assert.NoErr(t, err)
	assert.Eq(t, 1, n) // only decision:d1

	// The review is handled and the interaction answered: their advice goes on the next build.
	putJob(t, st, jobstore.JobRecord{ID: "j-rev", ProjectKey: "p1", Agent: "codex", Status: job.StatusDone, EndedAt: ago(1)})
	assert.NoErr(t, st.UpsertInteraction(jobstore.InteractionRecord{ID: "i1", JobID: "j-run", Type: job.InteractionTypePermission,
		Prompt: "go test ./...", Status: "answered", CreatedAt: ago(5)}))
	_, err = svc.Today(Query{})
	assert.NoErr(t, err)
	rows, err := st.ListDecisionAdvice()
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(rows))
	assert.Eq(t, "review:j-exec", rows[0].CardKey)

	putJob(t, st, jobstore.JobRecord{ID: "j-exec", ProjectKey: "p1", Agent: "exec", Status: job.StatusDone, EndedAt: ago(1)})
	removed, err := svc.PruneAdvice()
	assert.NoErr(t, err)
	assert.Eq(t, 1, removed)
}

func TestRecordActionKeepsTheAdviceOfTheMoment(t *testing.T) {
	svc, st := newTestService(t)
	seedAdviceQueue(t, st)
	_, err := svc.Advise(AdviceInput{CardKey: "review:j-rev", Text: "可以通过", ActionID: "accept"}, "steward(codex)", "")
	assert.NoErr(t, err)

	// The console sent nothing about the advice: the stored one is recorded.
	h, err := svc.RecordAction(ActionInput{CardKey: "review:j-rev", ActionID: "rerun", Label: "附意见重跑"}, "me")
	assert.NoErr(t, err)
	assert.Eq(t, "accept", h.AdviceActionID)
	assert.Eq(t, "可以通过", h.AdviceText)
	assert.False(t, h.ViaAdvice)

	_, err = svc.RecordAction(ActionInput{CardKey: "review:j-rev", ActionID: "accept", Label: "通过", ViaAdvice: true,
		AdviceActionID: "accept", AdviceLabel: "通过", AdviceText: "可以通过"}, "me")
	assert.NoErr(t, err)
	rows, err := svc.HandledSince(1)
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(rows))
	assert.True(t, rows[0].ViaAdvice || rows[1].ViaAdvice)
	for _, r := range rows {
		assert.Eq(t, "可以通过", r.AdviceText)
	}
}
