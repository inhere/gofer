package work

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/settings"
)

// noiseSvc is a work service reading the real settings registry over the test store.
func noiseSvc(t *testing.T) (*Service, *jobstore.Store, *settings.Service) {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "noise.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	svc := New(st)
	t.Cleanup(func() { assert.NoErr(t, svc.Close()) })
	set := settings.NewService(st)
	svc.SetSettings(set)
	return svc, st, set
}

func noiseSession(t *testing.T, st *jobstore.Store, sid, project, cwd string) jobstore.AgentSession {
	t.Helper()
	a, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: sid, Agent: "claude", ProjectKey: project, Cwd: cwd, State: jobstore.SessionRunning})
	assert.NoErr(t, err)
	return a
}

func drafts(t *testing.T, svc *Service) []ItemView {
	t.Helper()
	items, err := svc.List(jobstore.WorkListOpts{})
	assert.NoErr(t, err)
	return items
}

func promptCount(t *testing.T, st *jobstore.Store, sid string) int {
	t.Helper()
	a, ok, err := st.GetAgentSession(sid)
	assert.NoErr(t, err)
	assert.True(t, ok)
	return a.HumanPrompts
}

func TestDraftWaitsForTheSecondPrompt(t *testing.T) {
	svc, st, _ := noiseSvc(t)
	a := noiseSession(t, st, "sess-n0000001", "p1", "/ws/a")

	svc.OnHumanPrompt(a, "试试这个功能")
	assert.Eq(t, 0, len(drafts(t, svc)))
	assert.Eq(t, 1, promptCount(t, st, a.SessionID))

	// The hook retried the SAME beat (its answer never arrived): the same ask, not a second one.
	svc.OnHumanPrompt(a, "试试这个功能")
	assert.Eq(t, 0, len(drafts(t, svc)))
	assert.Eq(t, 1, promptCount(t, st, a.SessionID))

	svc.OnHumanPrompt(a, "那再帮我看看导出")
	items := drafts(t, svc)
	assert.Eq(t, 1, len(items))
	assert.Eq(t, "那再帮我看看导出", items[0].Title)
	assert.Eq(t, 2, promptCount(t, st, a.SessionID))

	// Further prompts never spawn a second draft.
	svc.OnHumanPrompt(a, "继续")
	assert.Eq(t, 1, len(drafts(t, svc)))
}

func TestDraftGateExclusions(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		value   any
		project string
		cwd     string
	}{
		{name: "no project", project: "", cwd: "/ws/a"},
		{name: "excluded project", key: settings.KeyWorkDraftExcludeProjects, value: []string{"sandbox"}, project: "sandbox", cwd: "/ws/a"},
		{name: "excluded cwd", key: settings.KeyWorkDraftExcludeCwds, value: []string{"/ws/scratch"}, project: "p1", cwd: "/ws/scratch/try"},
		{name: "excluded title", key: settings.KeyWorkDraftExcludeTitlePatterns, value: []string{"试一下"}, project: "p1", cwd: "/ws/a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, st, set := noiseSvc(t)
			if c.key != "" {
				_, _, err := set.Set(c.key, c.value, "test")
				assert.NoErr(t, err)
			}
			a := noiseSession(t, st, "sess-n0000002", c.project, c.cwd)
			svc.OnHumanPrompt(a, "试一下导出")
			svc.OnHumanPrompt(a, "再试一下导出")
			assert.Eq(t, 0, len(drafts(t, svc)))
			assert.Eq(t, 2, promptCount(t, st, a.SessionID)) // counted, just not drafted
		})
	}
}

func TestDraftGateLeavesExistingDraftAlone(t *testing.T) {
	svc, st, _ := noiseSvc(t)
	a := noiseSession(t, st, "sess-n0000003", "p1", "/ws/a")
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "已有", SessionIDs: []string{a.SessionID}, By: "human:me"})
	assert.NoErr(t, err)

	svc.OnHumanPrompt(a, "第一问")
	svc.OnHumanPrompt(a, "第二问")
	items := drafts(t, svc)
	assert.Eq(t, 1, len(items))
	assert.Eq(t, w.ID, items[0].ID)
	assert.Eq(t, "已有", items[0].Title)
}

// errSettings is a settings service that cannot read: every rule must fall back to the
// registry defaults instead of stalling the summarizer / the draft.
type errSettings struct{}

func (errSettings) GetInt(string) (int, error)          { return 0, errors.New("db is gone") }
func (errSettings) GetString(string) (string, error)    { return "", errors.New("db is gone") }
func (errSettings) GetStrings(string) ([]string, error) { return nil, errors.New("db is gone") }
func (errSettings) Patterns(string) ([]*regexp.Regexp, error) {
	return nil, errors.New("db is gone")
}

func TestSettingsReadFailureFallsBackToDefaults(t *testing.T) {
	svc, st, _ := noiseSvc(t)
	svc.SetSettings(errSettings{})
	a := noiseSession(t, st, "sess-n0000004", "p1", "/ws/a")

	svc.OnHumanPrompt(a, "第一问")
	assert.Eq(t, 0, len(drafts(t, svc)))
	svc.OnHumanPrompt(a, "第二问")
	assert.Eq(t, 1, len(drafts(t, svc))) // default min_prompts = 2

	// Short-session defaults (10 minutes / 3 prompts) still apply.
	now := time.Now()
	if _, short := svc.ShortSession(jobstore.AgentSession{State: jobstore.SessionEnded,
		StartedAt: now.Unix() - 60, EndedAt: now.Unix(), HumanPrompts: 1}, now); !short {
		t.Fatalf("short session not detected with default thresholds")
	}
	if _, short := svc.ShortSession(jobstore.AgentSession{State: jobstore.SessionEnded,
		StartedAt: now.Unix() - 3600, EndedAt: now.Unix(), HumanPrompts: 1}, now); short {
		t.Fatalf("an hour-long session judged short")
	}
}

func TestScanSummarizeSkipsShortSessionAndKeepsQuota(t *testing.T) {
	svc, st, w, os := sumFixture(t, goodJSON)
	clk := clockOf(svc)
	// The session ended right away after one prompt: a test / try-out session.
	_, _, err := st.TouchAgentSession("sess-sum00001", jobstore.SessionHeartbeat{State: jobstore.SessionEnded})
	assert.NoErr(t, err)
	_, err = st.IncrementSessionHumanPrompts("sess-sum00001")
	assert.NoErr(t, err)

	svc.scanSummarize(svc.nowFn())
	svc.WaitIdle()
	assert.Eq(t, 0, os.callCount())

	runs, err := st.ListWorkSummaries(w.ID, 10)
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(runs))
	assert.Eq(t, jobstore.SummarySkipped, runs[0].State)
	assert.True(t, strings.Contains(runs[0].Error, "短命会话"))

	used, err := st.CountAutoWorkSummaries(svc.startOfDay())
	assert.NoErr(t, err)
	assert.Eq(t, 0, used) // the skip costs nothing

	// The same skip is not recorded again on the next tick.
	svc.scanSummarize(svc.nowFn())
	runs, _ = st.ListWorkSummaries(w.ID, 10)
	assert.Eq(t, 1, len(runs))

	// Once the session is no longer short, the daily quota is still whole: with a cap of
	// one it summarises normally.
	svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{SummarizeDailyLimit: 1} })
	clk.Advance(31 * time.Minute) // past the per-session gap since the skipped run
	_, _, err = st.TouchAgentSession("sess-sum00001", jobstore.SessionHeartbeat{State: jobstore.SessionEnded, LastMessage: "又做了点"})
	assert.NoErr(t, err)
	svc.scanSummarize(svc.nowFn())
	svc.WaitIdle()
	assert.Eq(t, 1, os.callCount())
}
