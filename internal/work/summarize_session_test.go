package work

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
)

// pinnedOneShot is the summarizer's job seam with the session book switched on: it takes
// a pre-allocated session id and can continue the previous job (gofer-ayu0).
type pinnedOneShot struct {
	*fakeOneShot
	inject, resume bool
	resumes        []ResumeRequest
	resumeErr      error
}

func (p *pinnedOneShot) SessionCaps(string) (bool, bool) { return p.inject, p.resume }

func (p *pinnedOneShot) Resume(_ context.Context, r ResumeRequest) (OneShotResult, error) {
	if p.resumeErr != nil {
		err := p.resumeErr
		p.resumeErr = nil
		return OneShotResult{}, err
	}
	p.resumes = append(p.resumes, r)
	return OneShotResult{JobID: "job-res-1", Output: goodJSON}, nil
}

func TestSummarizerReusesOneSessionPerProject(t *testing.T) {
	svc, st, w, fake := sumFixture(t, goodJSON)
	po := &pinnedOneShot{fakeOneShot: fake, inject: true, resume: true}
	svc.SetOneShot(po)

	firstRes, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	if err != nil {
		t.Fatalf("first tidy-up: %v", err)
	}
	first := fake.calls[0].SessionID
	assert.True(t, first != "", "the first run opens the project's session with a pre-allocated id")

	// The second tidy-up continues the first one's session instead of opening a new one.
	if _, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual}); err != nil {
		t.Fatalf("second tidy-up: %v", err)
	}
	assert.Eq(t, 1, len(po.resumes))
	assert.Eq(t, firstRes.JobID, po.resumes[0].JobID)
	assert.True(t, strings.HasPrefix(po.resumes[0].Prompt, "【这是新的一件工作】"),
		"a continued session is told the earlier tidy-ups are done with")
	assert.Eq(t, 1, len(fake.calls), "the second run did not submit a fresh job")

	rec, ok, err := st.GetSummarizerSession("p1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, first, rec.SessionID)
	assert.Eq(t, 2, rec.Runs)
	assert.Eq(t, "job-res-1", rec.LastJobID, "the continuation is the job the next run resumes")
}

func TestSummarizerRotatesSessionAfterMaxRuns(t *testing.T) {
	svc, st, w, fake := sumFixture(t, goodJSON)
	svc.SetOneShot(&pinnedOneShot{fakeOneShot: fake, inject: true, resume: true})
	svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{SummarizerSessionMaxRuns: 1} })

	if _, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual}); err != nil {
		t.Fatalf("first tidy-up: %v", err)
	}
	first := fake.calls[0].SessionID

	if _, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual}); err != nil {
		t.Fatalf("second tidy-up: %v", err)
	}
	assert.Eq(t, 2, len(fake.calls), "a rotated session opens a fresh job")
	second := fake.calls[1].SessionID
	assert.True(t, second != "" && second != first, "the rotated run gets a NEW session id")
	rec, _, _ := st.GetSummarizerSession("p1")
	assert.Eq(t, second, rec.SessionID)
	assert.Eq(t, 1, rec.Runs, "the rotation starts the count over")
}

func TestSummarizerRotatesSessionWhenTooOld(t *testing.T) {
	svc, st, w, fake := sumFixture(t, goodJSON)
	svc.SetOneShot(&pinnedOneShot{fakeOneShot: fake, inject: true, resume: true})

	if _, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual}); err != nil {
		t.Fatalf("first tidy-up: %v", err)
	}
	first := fake.calls[0].SessionID
	clockOf(svc).Advance(25 * time.Hour) // work.summarizer_session_max_age defaults to 24h

	if _, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual}); err != nil {
		t.Fatalf("second tidy-up: %v", err)
	}
	assert.Eq(t, 2, len(fake.calls), "an expired session is replaced instead of continued")
	assert.True(t, fake.calls[1].SessionID != first, "the rotated run gets a new session id")
	rec, _, _ := st.GetSummarizerSession("p1")
	assert.Eq(t, 1, rec.Runs)
}

func TestSummarizerFallsBackToNewSessionWhenResumeFails(t *testing.T) {
	svc, st, w, fake := sumFixture(t, goodJSON)
	po := &pinnedOneShot{fakeOneShot: fake, inject: true, resume: true, resumeErr: errors.New("session not found")}
	svc.SetOneShot(po)

	if _, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual}); err != nil {
		t.Fatalf("first tidy-up: %v", err)
	}
	first := fake.calls[0].SessionID
	if _, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual}); err != nil {
		t.Fatalf("a refused continuation must not fail the tidy-up: %v", err)
	}
	assert.Eq(t, 2, len(fake.calls), "the failed continuation falls back to a fresh job")
	assert.True(t, fake.calls[1].SessionID != "" && fake.calls[1].SessionID != first,
		"the fallback opens a new session")
	assert.Eq(t, 0, len(po.resumes))
	rec, ok, _ := st.GetSummarizerSession("p1")
	assert.True(t, ok)
	assert.Eq(t, fake.calls[1].SessionID, rec.SessionID)
}

func TestSummarizerSessionUnsupportedAgentKeepsNewSessionPerRun(t *testing.T) {
	svc, st, w, fake := sumFixture(t, goodJSON)
	svc.SetOneShot(fake) // no SessionCaps at all

	if _, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual}); err != nil {
		t.Fatalf("first tidy-up: %v", err)
	}
	if _, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual}); err != nil {
		t.Fatalf("second tidy-up: %v", err)
	}
	assert.Eq(t, 2, len(fake.calls))
	assert.Eq(t, "", fake.calls[0].SessionID, "an agent without session_inject is not pinned")
	assert.Eq(t, "", fake.calls[1].SessionID)
	_, _, err := st.GetSummarizerSession("p1")
	assert.NoErr(t, err)
}
