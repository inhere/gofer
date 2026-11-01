package steward

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/bgagent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
)

// resumeHost is a SessionHost that can also load an earlier session.
type resumeHost struct {
	*fakeHost
	resumed   []ResumeSpec
	resumeErr error
}

func (h *resumeHost) ResumeSession(sp ResumeSpec) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.resumeErr != nil {
		return "", h.resumeErr
	}
	h.seq++
	id := fmt.Sprintf("job-%d", h.seq)
	h.resumed = append(h.resumed, sp)
	h.jobs[id] = &fakeJob{info: JobInfo{ID: id, Agent: "claude-acp", Status: "awaiting_input", Live: true, AwaitingInput: true, TurnNo: 1}}
	return id, nil
}

// endWithUsage ends a job the way an idle end does and leaves the usage it reported.
func (h *resumeHost) endWithUsage(id, sessionID string, u *runner.Usage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[id]
	j.info.Live, j.info.AwaitingInput, j.info.Status = false, false, "done"
	j.info.SessionID, j.info.Usage = sessionID, u
}

func newResumeEnv(t *testing.T) (*env, *resumeHost) {
	t.Helper()
	e := newEnv(t)
	h := &resumeHost{fakeHost: e.host}
	e.svc.host = h
	return e, h
}

func startNew(t *testing.T, e *env) StartResult {
	t.Helper()
	r, err := e.svc.Start(context.Background())
	assert.NoErr(t, err)
	assert.True(t, r.Started)
	return r
}

func bgAudit(t *testing.T, e *env, kind string) []string {
	t.Helper()
	evs, err := e.st.ListAuditEventsSince(kind, 0, 50)
	assert.NoErr(t, err)
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Detail)
	}
	return out
}

func TestStewardResumesItsSessionAfterIdleEnd(t *testing.T) {
	e, h := newResumeEnv(t)
	first := startNew(t, e)
	assert.Eq(t, 1, len(h.started), "the first start opens the session from the prime")
	assert.True(t, strings.Contains(h.started[0].Prompt, "---"))

	h.endWithUsage(first.JobID, "agent-sess-1", &runner.Usage{TotalTokens: 10, Context: &runner.ContextUsage{Ratio: 0.2}})
	second := startNew(t, e)
	assert.Eq(t, 1, len(h.started), "the idle end is followed by a resume, not a rebuild")
	assert.Eq(t, 1, len(h.resumed))
	assert.Eq(t, first.JobID, h.resumed[0].JobID)
	assert.NotEq(t, first.JobID, second.JobID)
	assert.False(t, strings.Contains(h.resumed[0].Prompt, "---"), "a resumed session does not get the prime again")

	// the book follows: one session, two runs, the agent-side id learned from the ended job
	st, err := e.svc.bgRunner().Status(bgagent.KindSteward, bgagent.Policy{})
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(st.Sessions))
	assert.Eq(t, "agent-sess-1", st.Sessions[0].ID)
	assert.Eq(t, 2, st.Sessions[0].Runs)
	assert.Eq(t, second.JobID, st.Sessions[0].LastJobID)
}

func TestStewardRebuildsWhenResumeFails(t *testing.T) {
	e, h := newResumeEnv(t)
	first := startNew(t, e)
	h.endWithUsage(first.JobID, "s1", nil)
	h.resumeErr = errors.New("session not found")
	second := startNew(t, e)
	assert.Eq(t, 2, len(h.started), "a failed load rebuilds from the prime")
	assert.Eq(t, 0, len(h.resumed))
	assert.True(t, second.Started)
	st, _ := e.svc.bgRunner().Status(bgagent.KindSteward, bgagent.Policy{})
	assert.Eq(t, 1, len(st.Sessions))
	assert.Eq(t, 1, st.Sessions[0].Runs, "the rebuilt session starts counting again")
	assert.Eq(t, second.JobID, st.Sessions[0].LastJobID)
}

func TestStewardHostWithoutResumeAlwaysRebuilds(t *testing.T) {
	e := newEnv(t) // the plain fake host cannot resume
	first := startNew(t, e)
	assert.NoErr(t, e.svc.host.End(first.JobID))
	startNew(t, e)
	assert.Eq(t, 2, len(e.host.started))
}

func TestStewardRotatesByRunCount(t *testing.T) {
	e, h := newResumeEnv(t)
	e.svc.SetBackgroundAgentsFn(func() config.BackgroundAgentsConfig {
		return config.BackgroundAgentsConfig{Session: config.BackgroundAgentSessionConfig{MaxRuns: 2}}
	})
	r := startNew(t, e) // run 1
	h.endWithUsage(r.JobID, "s1", nil)
	r = startNew(t, e) // run 2: resumed
	assert.Eq(t, 1, len(h.resumed))
	h.endWithUsage(r.JobID, "s1", nil)
	startNew(t, e) // the 2-run session rotates: rebuilt from the prime
	assert.Eq(t, 2, len(h.started))
	assert.Eq(t, 1, len(h.resumed))
	got := bgAudit(t, e, jobstore.AuditBackgroundRotated)
	assert.Eq(t, 1, len(got))
	assert.True(t, strings.Contains(got[0], `"reason":"max_runs"`), got[0])
}

func TestStewardRotatesOnTheContextTheEndedJobReported(t *testing.T) {
	e, h := newResumeEnv(t)
	r := startNew(t, e)
	h.endWithUsage(r.JobID, "s1", &runner.Usage{Context: &runner.ContextUsage{UsedTokens: 180000, LimitTokens: 200000}})
	startNew(t, e)
	assert.Eq(t, 2, len(h.started), "a 0.9 full session is replaced by a rebuild")
	assert.Eq(t, 0, len(h.resumed))
	got := bgAudit(t, e, jobstore.AuditBackgroundRotated)
	assert.Eq(t, 1, len(got))
	assert.True(t, strings.Contains(got[0], `"reason":"context_ratio"`), got[0])
}

func TestStewardManualResetRebuildsNextTime(t *testing.T) {
	e, h := newResumeEnv(t)
	r := startNew(t, e)
	h.endWithUsage(r.JobID, "s1", nil)
	n, err := e.svc.bgRunner().Reset(bgagent.KindSteward, e.cfg.ProjectKey(), "", "person:a")
	assert.NoErr(t, err)
	assert.Eq(t, int64(1), n)
	startNew(t, e)
	assert.Eq(t, 2, len(h.started))
	assert.Eq(t, 0, len(h.resumed))
}

func TestStewardBudgetHardStop(t *testing.T) {
	e, h := newResumeEnv(t)
	e.svc.SetBackgroundAgentsFn(func() config.BackgroundAgentsConfig { return config.BackgroundAgentsConfig{DailyTokens: 50} })
	r := startNew(t, e)
	h.endWithUsage(r.JobID, "s1", &runner.Usage{TotalTokens: 100})
	_, err := e.svc.Start(context.Background())
	var stop *bgagent.HardStopError
	assert.True(t, errors.As(err, &stop), "err = %v", err)
	assert.Eq(t, bgagent.StopDailyTokens, stop.Reason)
	assert.Eq(t, 1, len(h.started), "a tripped budget starts nothing")
	assert.Eq(t, 0, len(h.resumed))
	assert.Eq(t, 1, len(bgAudit(t, e, jobstore.AuditBackgroundStopped)))
}

// The record is in the database: a new service over the same store (a restart) still
// continues the session.
func TestStewardRestartStillResumes(t *testing.T) {
	e, h := newResumeEnv(t)
	r := startNew(t, e)
	h.endWithUsage(r.JobID, "s1", nil)

	svc2 := New(e.st, e.work, h)
	svc2.SetConfigFn(func() (config.StewardConfig, config.WorkConfig) { return *e.cfg, *e.wcfg })
	svc2.SetNow(func() time.Time { return time.Now() })
	t.Cleanup(func() { _ = svc2.Close() })
	res, err := svc2.Start(context.Background())
	assert.NoErr(t, err)
	assert.True(t, res.Started)
	assert.Eq(t, 1, len(h.resumed))
	assert.Eq(t, r.JobID, h.resumed[0].JobID)
}
