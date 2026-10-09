package job

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/store"
)

// Dashboard job metrics (gofer-yelm P2, design
// docs/design/2026-10-09-dashboard-redesign-design.md §新表 job_metrics): when a job
// ends, the derived numbers the overview needs are computed once and stored in
// job_metrics, so GET /v1/stats/overview never parses logs or JSON. Every metric
// without a real source stays nil (the page shows 「—」); nothing is guessed.

// gitShortStat is a `git diff --shortstat` tally.
type gitShortStat struct {
	Files, Insertions, Deletions int64
}

// liveSignals is what only the running job could observe: the structured stream's
// turn / tool-call counts and model, and the end-of-run git shortstat.
type liveSignals struct {
	stream *ndjsonfilter.Signals
	git    *gitShortStat
}

// shortStatRe matches one `N files changed, X insertions(+), Y deletions(-)` summary
// (either count may be absent: a pure deletion prints no insertions).
var shortStatRe = regexp.MustCompile(`(\d+) files? changed(?:, (\d+) insertions?\(\+\))?(?:, (\d+) deletions?\(-\))?`)

// parseShortStat sums every summary line in s (a worktree job's diff summary has one
// per section). ok=false when s holds none.
func parseShortStat(s string) (gitShortStat, bool) {
	var out gitShortStat
	ms := shortStatRe.FindAllStringSubmatch(s, -1)
	for _, m := range ms {
		out.Files += atoi64(m[1])
		out.Insertions += atoi64(m[2])
		out.Deletions += atoi64(m[3])
	}
	return out, len(ms) > 0
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// captureShortStat tallies what a job changed in dir since base: `git diff --shortstat
// <base>` compares the working tree with the base, so it covers the job's commits AND
// its uncommitted tracked edits in one pass (a file touched by both counts once).
// Untracked new files are not included (git diff never lists them). nil = no base, no
// repository, or git failed — the metric then has no source.
func captureShortStat(dir, base string) *gitShortStat {
	if dir == "" || base == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	out, err := gitOut(ctx, dir, "diff", "--shortstat", base)
	if err != nil {
		return nil
	}
	st, _ := parseShortStat(out) // empty output = nothing changed = a known zero
	return &st
}

// isExecJob reports whether the job ran the exec agent itself (not a resume carrier,
// which mechanically carries Agent="exec" but runs the source agent).
func isExecJob(rec jobstore.JobRecord) bool {
	return rec.Agent == config.BuiltinExecAgentKey && rec.ResumeAgent == ""
}

func i64(v int64) *int64 { return &v }

// buildJobMetrics composes a job's metrics row from the job row, the store's evidence
// (acp summaries, session turn events, interactions) and the live signals (nil-safe).
// Pure: the unit tests pin every rule here.
//
// turns / tool_calls, first source that has them: acp per-turn summaries → the
// persistent-session turn events (+ stream tool calls) → the ndjson stream counters →
// exec agent (0, it has no turns) → unknown.
func buildJobMetrics(rec jobstore.JobRecord, ev jobstore.MetricsEvidence, live liveSignals, now int64) jobstore.JobMetrics {
	m := jobstore.JobMetrics{JobID: rec.ID, ComputedAt: now, Version: jobstore.JobMetricsVersion}

	if req, err := unmarshalRequestModel(rec.RequestJSON); err == nil && req != "" {
		m.Model = req
	} else if live.stream != nil && live.stream.Model != "" {
		m.Model = live.stream.Model
	}

	switch {
	case ev.ACPTurns > 0:
		m.Turns, m.ToolCalls = i64(ev.ACPTurns), i64(ev.ACPToolCalls)
	case ev.SessionTurns > 0:
		m.Turns = i64(ev.SessionTurns)
		if live.stream != nil && live.stream.Known {
			m.ToolCalls = i64(live.stream.ToolCalls)
		}
	case live.stream != nil && live.stream.Known:
		m.Turns, m.ToolCalls = i64(live.stream.Turns), i64(live.stream.ToolCalls)
	case isExecJob(rec):
		m.Turns, m.ToolCalls = i64(0), i64(0)
	}

	human := ev.HumanAnswers + ev.SessionSays
	wait := ev.InteractionWaitSec + ev.SessionIdleSec
	m.HumanCount, m.HumanWaitSec = i64(human), i64(wait)
	if rec.EndedAt > 0 && rec.StartedAt > 0 && rec.EndedAt >= rec.StartedAt {
		active := rec.EndedAt - rec.StartedAt - wait
		if active < 0 {
			active = 0
		}
		m.ActiveSec = i64(active)
	}

	m.Commits = i64(int64(len(unmarshalCommits(rec.CommitsJSON))))
	if live.git != nil {
		m.FilesChanged, m.Insertions, m.Deletions = i64(live.git.Files), i64(live.git.Insertions), i64(live.git.Deletions)
	}

	if u := unmarshalUsage(rec.UsageJSON); u != nil {
		m.InputTokens, m.OutputTokens, m.CacheRead = i64(u.InputTokens), i64(u.OutputTokens), i64(u.CacheReadTokens)
		cost := u.CostUSD
		m.CostUSD = &cost
	}
	return m
}

// unmarshalRequestModel reads the model a job asked for out of its request_json.
func unmarshalRequestModel(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return "", err
	}
	return req.Model, nil
}

// recordJobMetrics writes the ended job's metrics row (called from finish, after the
// terminal row is durable). Best-effort: a failure only warns — the metric is then
// missing (coverage < 1) and the backfill can still compute it.
func (s *Service) recordJobMetrics(entry *jobEntry, snap JobResult) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("job.metrics: recovered panic", "job_id", snap.ID, "panic", r)
		}
	}()
	if s.meta == nil {
		return
	}
	started := time.Now()
	entry.mu.Lock()
	live := entry.metricsLive
	entry.mu.Unlock()
	ev, err := s.meta.JobMetricsEvidence(snap.ID)
	if err != nil {
		slog.Warn("job.metrics: evidence", "job_id", snap.ID, "err", err)
		return
	}
	m := buildJobMetrics(toRecord(snap), ev, live, s.nowFn().Unix())
	if err := s.meta.UpsertJobMetrics(m); err != nil {
		slog.Warn("job.metrics: write", "job_id", snap.ID, "err", err)
		return
	}
	slog.Debug("job.metrics recorded", "job_id", snap.ID, "duration_ms", time.Since(started).Milliseconds())
}

// BackfillOptions drives one backfill batch (`gofer tool stats-backfill`).
type BackfillOptions struct {
	// Since limits the walk to jobs that ended at or after it (unix seconds; 0 = all).
	Since int64
	// AfterEnded / AfterID is the cursor the previous batch returned.
	AfterEnded int64
	AfterID    string
	// Limit caps the jobs examined in this batch (<= 0 = 100, max 1000).
	Limit int
	// Force recomputes rows that already exist at the current formula version.
	Force bool
	// Budget caps the batch's wall time (<= 0 = 15s): the batch stops early and
	// returns its cursor, so one HTTP call never outlives the client's deadline.
	Budget time.Duration
}

// BackfillResult is one batch's outcome. Done=false means call again with the cursor.
type BackfillResult struct {
	Scanned    int    `json:"scanned"`
	Written    int    `json:"written"`
	Failed     int    `json:"failed"`
	WithSignal int    `json:"with_signal"`
	WithGit    int    `json:"with_git"`
	AfterEnded int64  `json:"after_ended"`
	AfterID    string `json:"after_id"`
	Done       bool   `json:"done"`
}

// ErrNoMetaStore is returned by BackfillMetrics on a service without a store.
var ErrNoMetaStore = errors.New("job metadata store not configured")

// BackfillMetrics computes job_metrics rows for jobs that ended before the live write
// existed. It is idempotent: without Force only jobs lacking a current-version row are
// visited, and a row is an UPSERT keyed by job id. Sources, in addition to what the
// live path uses: the compact stderr.log for claude / omp turn and tool counts, and
// for git line counts the stored diff summary plus `git diff --shortstat base head`
// when the checkout still has those commits. Anything else stays nil.
func (s *Service) BackfillMetrics(opt BackfillOptions) (BackfillResult, error) {
	if s.meta == nil {
		return BackfillResult{}, ErrNoMetaStore
	}
	if opt.Limit <= 0 {
		opt.Limit = 100
	}
	if opt.Limit > 1000 {
		opt.Limit = 1000
	}
	if opt.Budget <= 0 {
		opt.Budget = 15 * time.Second
	}
	res := BackfillResult{AfterEnded: opt.AfterEnded, AfterID: opt.AfterID}
	recs, err := s.meta.ListJobsForMetrics(opt.Since, opt.AfterEnded, opt.AfterID, opt.Limit, !opt.Force)
	if err != nil {
		return res, err
	}
	started := time.Now()
	for _, rec := range recs {
		if time.Since(started) > opt.Budget {
			return res, nil
		}
		res.Scanned++
		res.AfterEnded, res.AfterID = rec.EndedAt, rec.ID
		ev, err := s.meta.JobMetricsEvidence(rec.ID)
		if err != nil {
			res.Failed++
			slog.Warn("stats backfill: evidence", "job_id", rec.ID, "err", err)
			continue
		}
		live := s.backfillLive(rec)
		m := buildJobMetrics(rec, ev, live, s.nowFn().Unix())
		if err := s.meta.UpsertJobMetrics(m); err != nil {
			res.Failed++
			slog.Warn("stats backfill: write", "job_id", rec.ID, "err", err)
			continue
		}
		res.Written++
		if m.Turns != nil {
			res.WithSignal++
		}
		if m.FilesChanged != nil {
			res.WithGit++
		}
	}
	res.Done = len(recs) < opt.Limit
	return res, nil
}

// backfillLive reconstructs the live-only signals of an ended job from what survives.
func (s *Service) backfillLive(rec jobstore.JobRecord) liveSignals {
	var live liveSignals
	if rec.ResultDir != "" {
		agentKey := rec.Agent
		if rec.ResumeAgent != "" {
			agentKey = rec.ResumeAgent
		}
		if ac, ok := s.agents.Get(agentKey); ok && ac.NDJSONOutput() {
			proj := agent.NDJSONProjectorFor(agentKey, ac)
			if f, err := os.Open(filepath.Join(rec.ResultDir, store.StderrFile)); err == nil {
				sig := ndjsonfilter.CountCompactSignals(f, proj)
				_ = f.Close()
				if sig.Known || sig.Model != "" {
					live.stream = &sig
				}
			}
		}
	}
	live.git = backfillGitStat(rec)
	return live
}

// backfillGitStat recovers a job's changed files / lines after the fact:
//   - a worktree job's diff summary already carries both sections (committed and
//     uncommitted), so its summary lines are the answer;
//   - otherwise the committed part is `git diff --shortstat base newest-commit` in the
//     job's checkout (when the commits are still there) and the uncommitted part is
//     the stored diff summary (which only ever held uncommitted edits).
//
// nil when neither source exists (no repository at start, a remote job, or the
// commits are gone).
func backfillGitStat(rec jobstore.JobRecord) *gitShortStat {
	if rec.WorktreePath != "" {
		if st, ok := parseShortStat(rec.DiffSummary); ok {
			return &st
		}
	}
	if rec.BaseSHA == "" {
		return nil
	}
	var total gitShortStat
	commits := unmarshalCommits(rec.CommitsJSON)
	if len(commits) == 0 && rec.DiffSummary == "" {
		// Nothing committed and no stored summary: either nothing changed or the diff
		// capture was off — the record cannot tell, so the metric stays unknown.
		return nil
	}
	if len(commits) > 0 {
		dir := rec.Cwd
		if rec.WorktreePath != "" {
			dir = rec.WorktreePath
		}
		if dir == "" {
			return nil
		}
		if _, err := os.Stat(dir); err != nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
		out, err := gitOut(ctx, dir, "diff", "--shortstat", rec.BaseSHA, commits[0].SHA)
		cancel()
		if err != nil {
			return nil
		}
		total, _ = parseShortStat(out)
	}
	if st, ok := parseShortStat(rec.DiffSummary); ok && rec.WorktreePath == "" {
		total.Files += st.Files
		total.Insertions += st.Insertions
		total.Deletions += st.Deletions
	}
	return &total
}
