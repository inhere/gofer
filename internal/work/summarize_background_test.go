package work

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/bgagent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
)

// usageOneShot is the summarizer's job seam with the session book switched on whose runs
// report usage (the way the job layer hands a finished job's usage back).
type usageOneShot struct {
	*fakeOneShot
	canResume bool
	usage     *runner.Usage
	resumes   []ResumeRequest
	resumeErr error
}

func (p *usageOneShot) CanContinueSession(string) bool { return p.canResume }

func (p *usageOneShot) Run(ctx context.Context, r OneShotRequest) (OneShotResult, error) {
	res, err := p.fakeOneShot.Run(ctx, r)
	res.SessionID = "sess-" + res.JobID
	res.Usage = p.usage
	return res, err
}

func (p *usageOneShot) Resume(_ context.Context, r ResumeRequest) (OneShotResult, error) {
	if p.resumeErr != nil {
		err := p.resumeErr
		p.resumeErr = nil
		return OneShotResult{}, err
	}
	p.resumes = append(p.resumes, r)
	return OneShotResult{JobID: "job-res-" + string(rune('0'+len(p.resumes))), SessionID: "sess-res", Output: goodJSON, Usage: p.usage}, nil
}

func ratio(r float64) *runner.Usage {
	return &runner.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110,
		Context: &runner.ContextUsage{Ratio: r, Source: runner.ContextSourceTurnEnd}}
}

func tidy(t *testing.T, svc *Service, id string) error {
	t.Helper()
	_, err := svc.RunSummarize(context.Background(), id, SummarizeOpts{Cause: CauseManual})
	return err
}

func auditDetails(t *testing.T, st *jobstore.Store, kind string) []string {
	t.Helper()
	evs, err := st.ListAuditEventsSince(kind, 0, 50)
	assert.NoErr(t, err)
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Detail)
	}
	return out
}

func TestSummarizerRotatesWhenContextIsFull(t *testing.T) {
	svc, st, w, fake := sumFixture(t, goodJSON)
	po := &usageOneShot{fakeOneShot: fake, canResume: true, usage: ratio(0.9)}
	svc.SetOneShot(po)

	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.Eq(t, 2, len(fake.calls), "a session at 0.9 of its window is replaced, not continued")
	assert.Eq(t, 0, len(po.resumes))
	got := auditDetails(t, st, jobstore.AuditBackgroundRotated)
	assert.Eq(t, 1, len(got))
	assert.True(t, strings.Contains(got[0], `"reason":"context_ratio"`), got[0])
	assert.True(t, strings.Contains(got[0], `"kind":"summarizer"`), got[0])
}

func TestSummarizerContinuesWhileContextHasRoom(t *testing.T) {
	svc, _, w, fake := sumFixture(t, goodJSON)
	po := &usageOneShot{fakeOneShot: fake, canResume: true, usage: ratio(0.3)}
	svc.SetOneShot(po)
	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.Eq(t, 1, len(po.resumes))
	assert.Eq(t, 1, len(fake.calls))
}

func TestSummarizerUnifiedThresholdsAndLegacyOverride(t *testing.T) {
	svc, _, w, fake := sumFixture(t, goodJSON)
	po := &usageOneShot{fakeOneShot: fake, canResume: true}
	svc.SetOneShot(po)
	svc.SetBackgroundAgentsFn(func() config.BackgroundAgentsConfig {
		return config.BackgroundAgentsConfig{Session: config.BackgroundAgentSessionConfig{MaxRuns: 1}}
	})
	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.Eq(t, 2, len(fake.calls), "server.background_agents.session.max_runs rotates the summarizer's session")

	// An explicit legacy value still wins over the unified default (one-time fallback).
	svc2, _, w2, fake2 := sumFixture(t, goodJSON)
	po2 := &usageOneShot{fakeOneShot: fake2, canResume: true}
	svc2.SetOneShot(po2)
	svc2.SetBackgroundAgentsFn(func() config.BackgroundAgentsConfig {
		return config.BackgroundAgentsConfig{Session: config.BackgroundAgentSessionConfig{MaxRuns: 1}}
	})
	svc2.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{SummarizerSessionMaxRuns: 5} })
	assert.NoErr(t, tidy(t, svc2, w2.ID))
	assert.NoErr(t, tidy(t, svc2, w2.ID))
	assert.Eq(t, 1, len(po2.resumes), "work.summarizer_session_max_runs overrides the unified value when set")
}

func TestSummarizerModelChangeOpensANewSession(t *testing.T) {
	svc, _, w, fake := sumFixture(t, goodJSON)
	po := &usageOneShot{fakeOneShot: fake, canResume: true}
	svc.SetOneShot(po)
	model := "haiku"
	svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{SummarizerArgs: []string{"--model", model}} })
	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.Eq(t, 1, len(po.resumes))
	model = "sonnet"
	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.Eq(t, 2, len(fake.calls), "another model must not continue the old session")
	assert.Eq(t, 1, len(po.resumes))
}

func TestSummarizerBudgetHardStop(t *testing.T) {
	svc, st, w, fake := sumFixture(t, goodJSON)
	po := &usageOneShot{fakeOneShot: fake, canResume: true, usage: ratio(0.1)} // 110 tokens a run
	svc.SetOneShot(po)
	svc.SetBackgroundAgentsFn(func() config.BackgroundAgentsConfig { return config.BackgroundAgentsConfig{DailyTokens: 100} })
	assert.NoErr(t, tidy(t, svc, w.ID))
	err := tidy(t, svc, w.ID)
	assert.Err(t, err)
	assert.Eq(t, 1, len(fake.calls)+len(po.resumes), "a tripped budget must not start the agent")
	got := auditDetails(t, st, jobstore.AuditBackgroundStopped)
	assert.Eq(t, 1, len(got))
	assert.True(t, strings.Contains(got[0], `"reason":"daily_token_budget"`), got[0])
}

// A book the older release kept (bound to no agent) continues in the first run that asks
// and is bound to it afterwards.
func TestSummarizerAdoptsTheImportedBook(t *testing.T) {
	svc, st, w, fake := sumFixture(t, goodJSON)
	po := &usageOneShot{fakeOneShot: fake, canResume: true}
	svc.SetOneShot(po)
	clk := clockOf(svc).Now().Unix()
	assert.NoErr(t, st.ReplaceBackgroundSession(jobstore.BackgroundSession{Kind: bgagent.KindSummarizer, SessionID: "sess-old",
		Project: "p1", CreatedAt: clk, LastUsedAt: clk, Runs: 4, LastJobID: "job-old"}))

	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.Eq(t, 1, len(po.resumes))
	assert.Eq(t, "job-old", po.resumes[0].JobID, "the imported record's job is the one continued")
	assert.Eq(t, 0, len(fake.calls))
	b, ok, err := st.ActiveBackgroundSession(bgagent.KindSummarizer, "p1", "", config.WorkConfig{}.SummarizerAgentName(),
		bgagent.ModelFromArgs(config.WorkConfig{}.SummarizerArgsOrDefault()))
	assert.NoErr(t, err)
	assert.True(t, ok, "the adopted session is bound to the agent that continued it")
	assert.Eq(t, 5, b.Runs)
}

func TestSummarizerRunsAreLogged(t *testing.T) {
	svc, st, w, fake := sumFixture(t, goodJSON)
	po := &usageOneShot{fakeOneShot: fake, canResume: true, usage: ratio(0.2)}
	svc.SetOneShot(po)
	assert.NoErr(t, tidy(t, svc, w.ID))
	assert.NoErr(t, tidy(t, svc, w.ID))
	stats, err := st.BackgroundStatsSince(bgagent.KindSummarizer, 0)
	assert.NoErr(t, err)
	assert.Eq(t, 2, stats.Runs)
	assert.Eq(t, 2, stats.UsageRuns)
	assert.Eq(t, int64(220), stats.TotalTokens)
}

// The adapter contract: the summarizer's OneShot / SessionResumer seams behave as the
// common runner's Executor.
func TestOneShotExecutorContract(t *testing.T) {
	fake := &fakeOneShot{outputs: []string{goodJSON}}
	po := &usageOneShot{fakeOneShot: fake, canResume: true, usage: ratio(0.5)}
	ex := oneShotExecutor{oneShot: po, noResume: errors.New("no channel")}

	assert.Eq(t, bgagent.Caps{CanResume: true}, ex.Caps("claude"))
	assert.Eq(t, bgagent.Caps{}, oneShotExecutor{oneShot: fake}.Caps("claude"), "an agent without session_resume cannot continue")

	res, err := ex.Execute(context.Background(), bgagent.ExecRequest{Key: bgagent.Key{Agent: "claude", Project: "p1"},
		Mode: bgagent.ModeFresh, Prompt: "tidy", Args: []string{"--x"}, Title: "t"})
	assert.NoErr(t, err)
	assert.Eq(t, "tidy", fake.calls[0].Prompt)
	assert.Eq(t, "claude", fake.calls[0].Agent)
	assert.Eq(t, []string{"--x"}, fake.calls[0].Args)
	assert.Eq(t, goodJSON, string(res.Output))
	assert.Eq(t, 0.5, res.Usage.Context.Ratio)

	res, err = ex.Execute(context.Background(), bgagent.ExecRequest{Key: bgagent.Key{Project: "p1"}, Mode: bgagent.ModeResume,
		LastJobID: "job-9", Prompt: "again"})
	assert.NoErr(t, err)
	assert.Eq(t, "job-9", po.resumes[0].JobID)
	assert.Eq(t, "sess-res", res.SessionID)

	po.resumeErr = errors.New("session not found")
	_, err = ex.Execute(context.Background(), bgagent.ExecRequest{Mode: bgagent.ModeResume, LastJobID: "job-9"})
	assert.True(t, errors.Is(err, bgagent.ErrSessionLost), "a failed continuation is a lost session: the runner falls back")

	_, err = ex.Execute(context.Background(), bgagent.ExecRequest{Mode: bgagent.ModeClearContext})
	assert.True(t, errors.Is(err, bgagent.ErrClearUnsupported))

	_, err = oneShotExecutor{oneShot: fake, noResume: errors.New("no channel")}.Execute(context.Background(),
		bgagent.ExecRequest{Mode: bgagent.ModeResume})
	assert.Eq(t, "no channel", err.Error())
}

// Driving the summarizer's executor through the common runner end to end: fresh, resume,
// and a refused resume falling back to a new session.
func TestOneShotExecutorThroughTheRunner(t *testing.T) {
	svc, _, _, fake := sumFixture(t, goodJSON)
	po := &usageOneShot{fakeOneShot: fake, canResume: true, usage: ratio(0.2)}
	ex := oneShotExecutor{oneShot: po, noResume: errors.New("no channel")}
	r := svc.bgRunner()
	req := bgagent.Request{Key: bgagent.Key{Kind: bgagent.KindSummarizer, Agent: "claude", Project: "p9"}, Prompt: "tidy",
		Policy: bgagent.Policy{MaxRuns: 10}, Limits: bgagent.Limits{}}
	res, err := r.Run(context.Background(), ex, req)
	assert.NoErr(t, err)
	assert.Eq(t, bgagent.ModeFresh, res.Mode)
	res, err = r.Run(context.Background(), ex, req)
	assert.NoErr(t, err)
	assert.Eq(t, bgagent.ModeResume, res.Mode)
	po.resumeErr = errors.New("session not found")
	res, err = r.Run(context.Background(), ex, req)
	assert.NoErr(t, err)
	assert.Eq(t, bgagent.ModeFresh, res.Mode)
	assert.Eq(t, bgagent.ReasonSessionLost, res.Reason)
}
