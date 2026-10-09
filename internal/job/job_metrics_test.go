package job

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
)

func TestParseShortStat(t *testing.T) {
	st, ok := parseShortStat(" 3 files changed, 10 insertions(+), 2 deletions(-)\n")
	assert.True(t, ok)
	assert.Eq(t, gitShortStat{3, 10, 2}, st)
	st, ok = parseShortStat(" 1 file changed, 1 deletion(-)")
	assert.True(t, ok)
	assert.Eq(t, gitShortStat{1, 0, 1}, st)
	// A worktree summary carries one line per section: both count.
	st, _ = parseShortStat("=== committed ===\n a | 1 +\n 1 file changed, 1 insertion(+)\n\n=== uncommitted ===\n 2 files changed, 4 insertions(+), 3 deletions(-)\n")
	assert.Eq(t, gitShortStat{3, 5, 3}, st)
	_, ok = parseShortStat("")
	assert.False(t, ok)
}

// TestBuildJobMetricsRules pins the source order of every metric and that a metric
// without a source stays nil.
func TestBuildJobMetricsRules(t *testing.T) {
	base := jobstore.JobRecord{ID: "j", Agent: "claude", StartedAt: 100, EndedAt: 400,
		RequestJSON: `{"prompt":"x"}`, CommitsJSON: `[{"sha":"a","subject":"s"},{"sha":"b","subject":"t"}]`,
		UsageJSON: `{"input_tokens":10,"output_tokens":5,"cache_read_tokens":7,"cost_usd":0.5}`}

	t.Run("acp summaries win", func(t *testing.T) {
		stream := &ndjsonfilter.Signals{Turns: 9, ToolCalls: 9, Known: true}
		m := buildJobMetrics(base, jobstore.MetricsEvidence{ACPTurns: 2, ACPToolCalls: 5, SessionTurns: 2}, liveSignals{stream: stream}, 1)
		assert.Eq(t, int64(2), *m.Turns)
		assert.Eq(t, int64(5), *m.ToolCalls)
	})
	t.Run("session turns plus stream tools", func(t *testing.T) {
		m := buildJobMetrics(base, jobstore.MetricsEvidence{SessionTurns: 3, SessionSays: 2, SessionIdleSec: 50},
			liveSignals{stream: &ndjsonfilter.Signals{Turns: 1, ToolCalls: 4, Known: true}}, 1)
		assert.Eq(t, int64(3), *m.Turns)
		assert.Eq(t, int64(4), *m.ToolCalls)
		assert.Eq(t, int64(2), *m.HumanCount)
		assert.Eq(t, int64(50), *m.HumanWaitSec)
		assert.Eq(t, int64(250), *m.ActiveSec) // wall 300 - wait 50
	})
	t.Run("stream counters and model", func(t *testing.T) {
		m := buildJobMetrics(base, jobstore.MetricsEvidence{}, liveSignals{stream: &ndjsonfilter.Signals{Turns: 6, ToolCalls: 11, Model: "m-s", Known: true}}, 1)
		assert.Eq(t, int64(6), *m.Turns)
		assert.Eq(t, int64(11), *m.ToolCalls)
		assert.Eq(t, "m-s", m.Model)
	})
	t.Run("requested model wins", func(t *testing.T) {
		rec := base
		rec.RequestJSON = `{"model":"m-req"}`
		m := buildJobMetrics(rec, jobstore.MetricsEvidence{}, liveSignals{stream: &ndjsonfilter.Signals{Model: "m-s"}}, 1)
		assert.Eq(t, "m-req", m.Model)
	})
	t.Run("no source stays nil", func(t *testing.T) {
		m := buildJobMetrics(base, jobstore.MetricsEvidence{}, liveSignals{}, 1)
		assert.Nil(t, m.Turns)
		assert.Nil(t, m.ToolCalls)
		assert.Nil(t, m.FilesChanged)
		assert.Eq(t, int64(2), *m.Commits)
		assert.Eq(t, int64(0), *m.HumanCount)
		assert.Eq(t, int64(300), *m.ActiveSec)
		assert.Eq(t, int64(10), *m.InputTokens)
		assert.Eq(t, 0.5, *m.CostUSD)
	})
	t.Run("exec has zero turns, resume carrier does not", func(t *testing.T) {
		rec := base
		rec.Agent = "exec"
		m := buildJobMetrics(rec, jobstore.MetricsEvidence{}, liveSignals{}, 1)
		assert.Eq(t, int64(0), *m.Turns)
		rec.ResumeAgent = "claude"
		m = buildJobMetrics(rec, jobstore.MetricsEvidence{}, liveSignals{}, 1)
		assert.Nil(t, m.Turns)
	})
	t.Run("no usage stays nil", func(t *testing.T) {
		rec := base
		rec.UsageJSON = ""
		m := buildJobMetrics(rec, jobstore.MetricsEvidence{}, liveSignals{git: &gitShortStat{1, 2, 3}}, 1)
		assert.Nil(t, m.InputTokens)
		assert.Nil(t, m.CostUSD)
		assert.Eq(t, int64(2), *m.Insertions)
	})
}

// TestFinishRecordsJobMetrics: every job that reaches finish gets its metrics row.
func TestFinishRecordsJobMetrics(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30})
	assert.Eq(t, StatusDone, final.Status)
	m, ok, err := s.Meta().GetJobMetrics(final.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(0), *m.Turns) // exec: no agent turns
	assert.Eq(t, int64(0), *m.Commits)
	assert.Eq(t, int64(0), *m.HumanCount)
	assert.Nil(t, m.FilesChanged) // not a git checkout
}

func metricsGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestFinishRecordsGitShortStat: a job that committed gets its changed files / lines
// (commits + uncommitted edits against the base).
func TestFinishRecordsGitShortStat(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Parallel()
	root := t.TempDir()
	metricsGit(t, root, "init", "-q")
	assert.NoErr(t, os.WriteFile(filepath.Join(root, "keep.txt"), []byte("a\nb\n"), 0o644))
	metricsGit(t, root, "add", "keep.txt")
	metricsGit(t, root, "commit", "-qm", "base")
	s := newTestService(t, root)
	script := "printf 'x\\ny\\n' > new.txt && git add new.txt && git -c user.name=t -c user.email=t@example.com commit -qm add && printf 'a\\n' > keep.txt"
	final := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", script}, Cwd: ".", TimeoutSec: 30})
	assert.Eq(t, StatusDone, final.Status)
	m, ok, err := s.Meta().GetJobMetrics(final.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(1), *m.Commits)
	assert.Eq(t, int64(2), *m.FilesChanged)
	assert.Eq(t, int64(2), *m.Insertions)
	assert.Eq(t, int64(1), *m.Deletions)
}

// TestBackfillMetricsIdempotent: the backfill writes a row for every ended job without
// one, a second run finds nothing to do, and --force recomputes.
func TestBackfillMetricsIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newTestService(t, root)
	for i, id := range []string{"bf-1", "bf-2", "bf-3"} {
		assert.NoErr(t, s.Meta().UpsertJob(jobstore.JobRecord{ID: id, ProjectKey: "self", Agent: "exec", Runner: "local",
			Status: StatusDone, ResultDir: filepath.Join(root, id), StartedAt: 100, EndedAt: int64(200 + i),
			CommitsJSON: `[{"sha":"a","subject":"s"}]`}))
	}
	assert.NoErr(t, s.Meta().UpsertJob(jobstore.JobRecord{ID: "bf-run", ProjectKey: "self", Agent: "exec",
		Runner: "local", Status: StatusRunning, ResultDir: root, StartedAt: 100}))

	res, err := s.BackfillMetrics(BackfillOptions{Limit: 2})
	assert.NoErr(t, err)
	assert.Eq(t, 2, res.Written)
	assert.False(t, res.Done)
	res, err = s.BackfillMetrics(BackfillOptions{Limit: 2, AfterEnded: res.AfterEnded, AfterID: res.AfterID})
	assert.NoErr(t, err)
	assert.Eq(t, 1, res.Written)
	assert.True(t, res.Done)

	m, ok, err := s.Meta().GetJobMetrics("bf-2")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(1), *m.Commits)
	assert.Eq(t, int64(101), *m.ActiveSec)
	_, ok, _ = s.Meta().GetJobMetrics("bf-run")
	assert.False(t, ok)

	res, err = s.BackfillMetrics(BackfillOptions{})
	assert.NoErr(t, err)
	assert.Eq(t, 0, res.Scanned)
	assert.True(t, res.Done)

	res, err = s.BackfillMetrics(BackfillOptions{Force: true})
	assert.NoErr(t, err)
	assert.Eq(t, 3, res.Written)
}
