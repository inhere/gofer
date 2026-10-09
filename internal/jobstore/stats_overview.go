package jobstore

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/util"
)

// The raw reads behind the dashboard overview (GET /v1/stats/overview, gofer-yelm).
// jobstore only reads rows here; the folding (rates, medians, buckets, rankings) lives
// in internal/overview, so the SQL stays a handful of indexed scans.

// OverviewJobRow is one ended job (ended_at in range, terminal or needs_review) joined
// with its job_metrics row. The Usage* fields come from jobs.usage_json (HasUsage =
// the job reported any); the M* pointers from job_metrics (nil = no row or no value).
type OverviewJobRow struct {
	ID           string
	Project      string
	Agent        string
	ResumeAgent  string
	Status       string
	StartedAt    int64
	EndedAt      int64
	CommitsLen   int64
	RequestModel string

	HasUsage    bool
	UsageInput  int64
	UsageOutput int64
	UsageCache  int64
	UsageCost   float64

	HasMetrics bool
	MModel     string
	MTurns     *int64
	MToolCalls *int64
	MHuman     *int64
	MHumanWait *int64
	MActive    *int64
	MCommits   *int64
	MFiles     *int64
	MIns       *int64
	MDel       *int64
}

// overviewEndStatuses is the SQL list of statsEndStatus.
const overviewEndStatuses = `('done','failed','timeout','cancelled','rejected','needs_review')`

const overviewJobsQuery = `SELECT j.id, j.project_key, j.agent, COALESCE(j.resume_agent,''), j.status,
  j.started_at, j.ended_at,
  CASE WHEN json_valid(j.commits_json) THEN json_array_length(j.commits_json) ELSE 0 END,
  CASE WHEN json_valid(j.request_json) THEN COALESCE(json_extract(j.request_json,'$.model'),'') ELSE '' END,
  CASE WHEN json_valid(j.usage_json) THEN 1 ELSE 0 END,
  CASE WHEN json_valid(j.usage_json) THEN COALESCE(json_extract(j.usage_json,'$.input_tokens'),0) ELSE 0 END,
  CASE WHEN json_valid(j.usage_json) THEN COALESCE(json_extract(j.usage_json,'$.output_tokens'),0) ELSE 0 END,
  CASE WHEN json_valid(j.usage_json) THEN COALESCE(json_extract(j.usage_json,'$.cache_read_tokens'),0) ELSE 0 END,
  CASE WHEN json_valid(j.usage_json) THEN COALESCE(json_extract(j.usage_json,'$.cost_usd'),0) ELSE 0 END,
  m.job_id IS NOT NULL, COALESCE(m.model,''), m.turns, m.tool_calls, m.human_count, m.human_wait_sec,
  m.active_sec, m.commits, m.files_changed, m.insertions, m.deletions
FROM jobs j LEFT JOIN job_metrics m ON m.job_id = j.id
WHERE j.ended_at >= ? AND j.ended_at > 0 AND j.status IN ` + overviewEndStatuses

// OverviewJobs reads every job that ended at or after from (unix seconds).
func (s *Store) OverviewJobs(from int64) ([]OverviewJobRow, error) {
	rows, err := s.db.Query(overviewJobsQuery, from)
	if err != nil {
		return nil, fmt.Errorf("jobstore: overview jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]OverviewJobRow, 0, 64)
	for rows.Next() {
		var r OverviewJobRow
		var model sql.NullString
		var hasUsage, hasMetrics int
		var turns, tools, human, wait, active, commits, files, ins, del sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Project, &r.Agent, &r.ResumeAgent, &r.Status, &r.StartedAt, &r.EndedAt,
			&r.CommitsLen, &model, &hasUsage, &r.UsageInput, &r.UsageOutput, &r.UsageCache, &r.UsageCost,
			&hasMetrics, &r.MModel, &turns, &tools, &human, &wait, &active, &commits, &files, &ins, &del); err != nil {
			return nil, fmt.Errorf("jobstore: scan overview job: %w", err)
		}
		r.RequestModel = model.String
		r.HasUsage, r.HasMetrics = hasUsage == 1, hasMetrics == 1
		r.MTurns, r.MToolCalls, r.MHuman, r.MHumanWait = intPtr(turns), intPtr(tools), intPtr(human), intPtr(wait)
		r.MActive, r.MCommits, r.MFiles, r.MIns, r.MDel = intPtr(active), intPtr(commits), intPtr(files), intPtr(ins), intPtr(del)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: overview jobs: %w", err)
	}
	return out, nil
}

// OverviewReviewRow is one job a human ruled on (reviewed_at in range). Rerun = a
// later job names it as its source (a rejection that was resumed with the note).
type OverviewReviewRow struct {
	ID         string
	Status     string
	EndedAt    int64
	ReviewedAt int64
	Rerun      bool
}

// OverviewReviews reads the jobs reviewed at or after from.
func (s *Store) OverviewReviews(from int64) ([]OverviewReviewRow, error) {
	rows, err := s.db.Query(`SELECT j.id, j.status, COALESCE(j.ended_at,0), j.reviewed_at,
  EXISTS (SELECT 1 FROM jobs c WHERE c.source_job_id = j.id)
FROM jobs j WHERE j.reviewed_at > 0 AND j.reviewed_at >= ?`, from)
	if err != nil {
		return nil, fmt.Errorf("jobstore: overview reviews: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []OverviewReviewRow
	for rows.Next() {
		var r OverviewReviewRow
		var rerun int
		if err := rows.Scan(&r.ID, &r.Status, &r.EndedAt, &r.ReviewedAt, &rerun); err != nil {
			return nil, fmt.Errorf("jobstore: scan overview review: %w", err)
		}
		r.Rerun = rerun == 1
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: overview reviews: %w", err)
	}
	return out, nil
}

// OverviewPlans counts plans whose status is done and whose last update falls at or
// after from (plans keep no completion stamp, so updated_at approximates it), and the
// todos marked done in the same window.
func (s *Store) OverviewPlans(from int64) (plansDone, todosDone int, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM plans WHERE status = ? AND updated_at >= ?`, PlanDone, from).
		Scan(&plansDone); err != nil {
		return 0, 0, fmt.Errorf("jobstore: overview plans: %w", err)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM plan_todos WHERE done = 1 AND updated_at >= ?`, from).
		Scan(&todosDone); err != nil {
		return 0, 0, fmt.Errorf("jobstore: overview todos: %w", err)
	}
	return plansDone, todosDone, nil
}

// DoneJobsByDay counts done jobs per LOCAL day (tzSec = the viewer's UTC offset in
// seconds) for jobs that ended at or after from. Keys are YYYY-MM-DD.
func (s *Store) DoneJobsByDay(from int64, tzSec int) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT date(ended_at + ?, 'unixepoch'), COUNT(*) FROM jobs
WHERE status = 'done' AND ended_at > 0 AND ended_at >= ? GROUP BY 1`, tzSec, from)
	if err != nil {
		return nil, fmt.Errorf("jobstore: done jobs by day: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, fmt.Errorf("jobstore: scan done day: %w", err)
		}
		out[day] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: done jobs by day: %w", err)
	}
	return out, nil
}

// FirstJobAt is the start time of the oldest job (0 = no job yet).
func (s *Store) FirstJobAt() (int64, error) {
	var at sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(started_at) FROM jobs WHERE started_at > 0`).Scan(&at); err != nil {
		return 0, fmt.Errorf("jobstore: first job: %w", err)
	}
	return at.Int64, nil
}

// SessionModelUsage is the terminal-session usage of one model inside a day range.
type SessionModelUsage struct {
	Model        string
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CostUSD      float64
}

// OverviewSessionUsage sums session_usage_daily from fromDay (UTC YYYY-MM-DD) on, per
// model, plus the number of distinct sessions that reported usage.
func (s *Store) OverviewSessionUsage(fromDay string) ([]SessionModelUsage, int, error) {
	var sessions int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT session_id) FROM session_usage_daily WHERE day >= ?`, fromDay).
		Scan(&sessions); err != nil {
		return nil, 0, fmt.Errorf("jobstore: overview session count: %w", err)
	}
	rows, err := s.db.Query(`SELECT model, SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cost_usd)
FROM session_usage_daily WHERE day >= ? GROUP BY model`, fromDay)
	if err != nil {
		return nil, 0, fmt.Errorf("jobstore: overview session usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SessionModelUsage
	for rows.Next() {
		var u SessionModelUsage
		if err := rows.Scan(&u.Model, &u.InputTokens, &u.OutputTokens, &u.CacheRead, &u.CostUSD); err != nil {
			return nil, 0, fmt.Errorf("jobstore: scan overview session usage: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("jobstore: overview session usage: %w", err)
	}
	return out, sessions, nil
}

// JobTitles returns each job's display title: request title, else the first line of
// its prompt (the caller truncates). Unknown ids are absent.
func (s *Store) JobTitles(ids []string) (map[string]string, error) {
	out := make(map[string]string, util.CapSum(len(ids)))
	if len(ids) == 0 {
		return out, nil
	}
	q := `SELECT id,
  CASE WHEN json_valid(request_json) THEN COALESCE(json_extract(request_json,'$.title'),'') ELSE '' END,
  CASE WHEN json_valid(request_json) THEN COALESCE(json_extract(request_json,'$.prompt'),'') ELSE '' END
FROM jobs WHERE id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
	args := make([]any, 0, util.CapSum(len(ids)))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: job titles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, title, prompt string
		if err := rows.Scan(&id, &title, &prompt); err != nil {
			return nil, fmt.Errorf("jobstore: scan job title: %w", err)
		}
		if strings.TrimSpace(title) == "" {
			title, _, _ = strings.Cut(strings.TrimSpace(prompt), "\n")
		}
		out[id] = strings.TrimSpace(title)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: job titles: %w", err)
	}
	return out, nil
}
