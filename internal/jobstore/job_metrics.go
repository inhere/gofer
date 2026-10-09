package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/runner"
)

// Dashboard per-job derived metrics (gofer-yelm P2, design
// docs/design/2026-10-09-dashboard-redesign-design.md §新表 job_metrics). The job
// service writes one row when a job ends; `gofer tool stats-backfill` recomputes rows
// for jobs that ended before the table existed. A nil field is "no source for this
// metric" (the overview renders 「—」), never zero.

// JobMetricsVersion is the derivation formula generation stored on every row. Bump it
// when a formula changes; the backfill recomputes rows of an older version.
const JobMetricsVersion = 1

// JobMetrics is one job_metrics row.
type JobMetrics struct {
	JobID        string
	Model        string
	Turns        *int64
	ToolCalls    *int64
	HumanCount   *int64
	HumanWaitSec *int64
	ActiveSec    *int64
	Commits      *int64
	FilesChanged *int64
	Insertions   *int64
	Deletions    *int64
	InputTokens  *int64
	OutputTokens *int64
	CacheRead    *int64
	CostUSD      *float64
	ComputedAt   int64
	Version      int
}

// StatsGen is the dashboard overview's invalidation counter: it moves whenever a job
// ends, is reviewed or deleted, terminal-session usage lands, or a job_metrics row is
// written. A cached overview built under an older value may be stale.
func (s *Store) StatsGen() uint64 { return s.statsGen.Load() }

// statsEndStatus reports whether a job status puts the job into the overview's ended
// set (terminal, or parked in needs_review). Literals: jobstore does not import job.
func statsEndStatus(status string) bool {
	switch status {
	case "done", "failed", "timeout", "cancelled", "rejected", "needs_review":
		return true
	}
	return false
}

// UpsertJobMetrics writes (or replaces) a job's derived metrics row.
func (s *Store) UpsertJobMetrics(m JobMetrics) error {
	if m.JobID == "" {
		return errors.New("jobstore: UpsertJobMetrics: empty job id")
	}
	if m.ComputedAt == 0 {
		m.ComputedAt = s.unixNow()
	}
	if m.Version == 0 {
		m.Version = JobMetricsVersion
	}
	s.writeMu.Lock()
	_, err := s.db.Exec(`INSERT INTO job_metrics
  (job_id, model, turns, tool_calls, human_count, human_wait_sec, active_sec, commits, files_changed,
   insertions, deletions, input_tokens, output_tokens, cache_read_tokens, cost_usd, computed_at, version)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(job_id) DO UPDATE SET
    model=excluded.model, turns=excluded.turns, tool_calls=excluded.tool_calls,
    human_count=excluded.human_count, human_wait_sec=excluded.human_wait_sec, active_sec=excluded.active_sec,
    commits=excluded.commits, files_changed=excluded.files_changed, insertions=excluded.insertions,
    deletions=excluded.deletions, input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens,
    cache_read_tokens=excluded.cache_read_tokens, cost_usd=excluded.cost_usd,
    computed_at=excluded.computed_at, version=excluded.version`,
		m.JobID, nullStr(m.Model), nullIntPtr(m.Turns), nullIntPtr(m.ToolCalls), nullIntPtr(m.HumanCount),
		nullIntPtr(m.HumanWaitSec), nullIntPtr(m.ActiveSec), nullIntPtr(m.Commits), nullIntPtr(m.FilesChanged),
		nullIntPtr(m.Insertions), nullIntPtr(m.Deletions), nullIntPtr(m.InputTokens), nullIntPtr(m.OutputTokens),
		nullIntPtr(m.CacheRead), nullFloat(m.CostUSD), m.ComputedAt, m.Version)
	s.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("jobstore: upsert job metrics %q: %w", m.JobID, err)
	}
	s.statsGen.Add(1)
	return nil
}

// GetJobMetrics reads a job's metrics row; ok=false when none was written.
func (s *Store) GetJobMetrics(jobID string) (JobMetrics, bool, error) {
	var m JobMetrics
	var model sql.NullString
	var turns, tools, human, wait, active, commits, files, ins, del, in, out, cache sql.NullInt64
	var cost sql.NullFloat64
	err := s.db.QueryRow(`SELECT job_id, model, turns, tool_calls, human_count, human_wait_sec, active_sec,
  commits, files_changed, insertions, deletions, input_tokens, output_tokens, cache_read_tokens, cost_usd,
  computed_at, version FROM job_metrics WHERE job_id = ?`, jobID).Scan(&m.JobID, &model, &turns, &tools,
		&human, &wait, &active, &commits, &files, &ins, &del, &in, &out, &cache, &cost, &m.ComputedAt, &m.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return JobMetrics{}, false, nil
	}
	if err != nil {
		return JobMetrics{}, false, fmt.Errorf("jobstore: get job metrics %q: %w", jobID, err)
	}
	m.Model = model.String
	m.Turns, m.ToolCalls, m.HumanCount = intPtr(turns), intPtr(tools), intPtr(human)
	m.HumanWaitSec, m.ActiveSec, m.Commits = intPtr(wait), intPtr(active), intPtr(commits)
	m.FilesChanged, m.Insertions, m.Deletions = intPtr(files), intPtr(ins), intPtr(del)
	m.InputTokens, m.OutputTokens, m.CacheRead = intPtr(in), intPtr(out), intPtr(cache)
	if cost.Valid {
		v := cost.Float64
		m.CostUSD = &v
	}
	return m, true, nil
}

// ListJobsForMetrics pages through ended jobs (ended_at >= since) in (ended_at, id)
// order after the cursor — the backfill's walk. onlyStale skips jobs whose metrics row
// already exists at the current JobMetricsVersion, which is what makes a re-run a
// no-op.
func (s *Store) ListJobsForMetrics(since, afterEnded int64, afterID string, limit int, onlyStale bool) ([]JobRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	q := selectCols + ` WHERE ended_at > 0 AND ended_at >= ? AND status IN ('done','failed','timeout','cancelled','rejected','needs_review')
  AND (ended_at > ? OR (ended_at = ? AND id > ?))`
	if onlyStale {
		q += ` AND NOT EXISTS (SELECT 1 FROM job_metrics m WHERE m.job_id = jobs.id AND m.version >= ?)`
	}
	q += ` ORDER BY ended_at, id LIMIT ?`
	args := []any{since, afterEnded, afterEnded, afterID}
	if onlyStale {
		args = append(args, JobMetricsVersion)
	}
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list jobs for metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]JobRecord, 0, limit)
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan job for metrics: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list jobs for metrics: %w", err)
	}
	return out, nil
}

// The job events the metrics read. The turn literals mirror job.EventJobTurn* /
// job.EventJobAwaitingInput (jobstore must not import job, G022).
const (
	evTurnStarted   = "job.turn_started"
	evTurnEnded     = "job.turn_ended"
	evAwaitingInput = "job.awaiting_input"
)

// MetricsEvidence is what the database knows about a job's turns and human involvement:
// the acp per-turn summaries, the persistent-session turn events and the interactions.
type MetricsEvidence struct {
	// ACPTurns / ACPToolCalls: one job.acp_summary per acp prompt turn, carrying that
	// turn's distinct tool calls.
	ACPTurns     int64
	ACPToolCalls int64
	// SessionTurns counts job.turn_ended of a persistent session job; SessionSays the
	// follow-up prompts (job.turn_started with turn_no > 1); SessionIdleSec the gaps
	// from job.awaiting_input to the next job.turn_started (the session waited for
	// someone to speak). Replayed recovery events are skipped.
	SessionTurns   int64
	SessionSays    int64
	SessionIdleSec int64
	// HumanAnswers counts interactions a person answered (answered_by human / push).
	// InteractionWaitSec sums answered_at - created_at over every interaction that was
	// not auto-answered (L0 rules answer at once; they are not a wait).
	HumanAnswers       int64
	InteractionWaitSec int64
}

// metricsEvent / metricsInteraction are the narrow rows deriveEvidence folds.
type metricsEvent struct {
	Type   string
	Detail string
	At     int64
}

type metricsInteraction struct {
	CreatedAt  int64
	AnsweredAt int64
	AnsweredBy string
}

// JobMetricsEvidence reads the job's metric-relevant events and interactions and folds
// them (two indexed per-job queries).
func (s *Store) JobMetricsEvidence(jobID string) (MetricsEvidence, error) {
	rows, err := s.db.Query(`SELECT type, COALESCE(detail_json,''), at FROM job_events
WHERE job_id = ? AND type IN (?,?,?,?) ORDER BY seq`, jobID,
		runner.EventACPSummary, evTurnStarted, evTurnEnded, evAwaitingInput)
	if err != nil {
		return MetricsEvidence{}, fmt.Errorf("jobstore: metrics events %q: %w", jobID, err)
	}
	var events []metricsEvent
	for rows.Next() {
		var ev metricsEvent
		if err := rows.Scan(&ev.Type, &ev.Detail, &ev.At); err != nil {
			_ = rows.Close()
			return MetricsEvidence{}, fmt.Errorf("jobstore: scan metrics event: %w", err)
		}
		events = append(events, ev)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return MetricsEvidence{}, fmt.Errorf("jobstore: metrics events %q: %w", jobID, err)
	}

	irows, err := s.db.Query(`SELECT created_at, COALESCE(answered_at,0), COALESCE(answered_by,'')
FROM interactions WHERE job_id = ?`, jobID)
	if err != nil {
		return MetricsEvidence{}, fmt.Errorf("jobstore: metrics interactions %q: %w", jobID, err)
	}
	defer func() { _ = irows.Close() }()
	var inters []metricsInteraction
	for irows.Next() {
		var it metricsInteraction
		if err := irows.Scan(&it.CreatedAt, &it.AnsweredAt, &it.AnsweredBy); err != nil {
			return MetricsEvidence{}, fmt.Errorf("jobstore: scan metrics interaction: %w", err)
		}
		inters = append(inters, it)
	}
	if err := irows.Err(); err != nil {
		return MetricsEvidence{}, fmt.Errorf("jobstore: metrics interactions %q: %w", jobID, err)
	}
	return deriveEvidence(events, inters), nil
}

// deriveEvidence folds the rows JobMetricsEvidence read (pure; unit-tested).
func deriveEvidence(events []metricsEvent, inters []metricsInteraction) MetricsEvidence {
	var ev MetricsEvidence
	var waitingSince int64 // the open job.awaiting_input, 0 = none
	for _, e := range events {
		detail := map[string]any{}
		if e.Detail != "" {
			_ = json.Unmarshal([]byte(e.Detail), &detail)
		}
		if replayed, _ := detail["replayed"].(bool); replayed {
			continue
		}
		switch e.Type {
		case runner.EventACPSummary:
			ev.ACPTurns++
			if n, ok := detail["tool_calls"].(float64); ok && n > 0 {
				ev.ACPToolCalls += int64(n)
			}
		case evTurnEnded:
			ev.SessionTurns++
		case evAwaitingInput:
			waitingSince = e.At
		case evTurnStarted:
			if n, _ := detail["turn_no"].(float64); n > 1 {
				ev.SessionSays++
			}
			if waitingSince > 0 && e.At > waitingSince {
				ev.SessionIdleSec += e.At - waitingSince
			}
			waitingSince = 0
		}
	}
	for _, it := range inters {
		by := strings.TrimSpace(it.AnsweredBy)
		if by == "human" || by == "push" {
			ev.HumanAnswers++
		}
		if strings.HasPrefix(by, "auto:") {
			continue
		}
		if it.AnsweredAt > it.CreatedAt && it.CreatedAt > 0 {
			ev.InteractionWaitSec += it.AnsweredAt - it.CreatedAt
		}
	}
	return ev
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIntPtr(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func intPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}
