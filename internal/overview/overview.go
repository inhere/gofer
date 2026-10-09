// Package overview builds the dashboard statistics wall (gofer-yelm, design
// docs/design/2026-10-09-dashboard-redesign-design.md): GET /v1/stats/overview folds
// the jobs that ENDED inside a range (plus their job_metrics rows), the reviews, the
// plans and the terminal-session usage into one payload, and caches it per (range, tz).
//
// The httpapi layer binds / validates and forwards here (G021); the SQL reads live in
// jobstore (stats_overview.go). Every metric without a real source is nil on the wire
// (the page shows 「—」), never a made-up zero.
package overview

import (
	"errors"
	"fmt"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Range keys.
const (
	Range7d  = "7d"
	Range30d = "30d"
	RangeAll = "all"
)

// ErrInvalidQuery marks a bad range / tz.
var ErrInvalidQuery = errors.New("invalid overview query")

// maxTZMin bounds the tz offset (UTC-14:00 .. UTC+14:00).
const maxTZMin = 14 * 60

// Query is the GET /v1/stats/overview input.
type Query struct {
	// Range is 7d / 30d / all ("" = 30d).
	Range string
	// TZMin is the viewer's UTC offset in minutes (east positive: UTC+8 = 480).
	TZMin int
}

// Normalize fills the default range and validates both fields.
func (q Query) Normalize() (Query, error) {
	if q.Range == "" {
		q.Range = Range30d
	}
	switch q.Range {
	case Range7d, Range30d, RangeAll:
	default:
		return q, fmt.Errorf("%w: range must be 7d, 30d or all", ErrInvalidQuery)
	}
	if q.TZMin < -maxTZMin || q.TZMin > maxTZMin {
		return q, fmt.Errorf("%w: tz must be within ±%d minutes", ErrInvalidQuery, maxTZMin)
	}
	return q, nil
}

// Overview is the GET /v1/stats/overview payload.
type Overview struct {
	Range       RangeInfo    `json:"range"`
	Totals      Totals       `json:"totals"`
	Jobs        JobsBlock    `json:"jobs"`
	Time        TimeBlock    `json:"time"`
	Git         GitBlock     `json:"git"`
	Signal      *SignalBlock `json:"signal"`
	Daily       []DayRow     `json:"daily"`
	Best        Best         `json:"best"`
	Heatmap     Heatmap      `json:"heatmap"`
	Agents      []AgentRow   `json:"agents"`
	Projects    []ProjectRow `json:"projects"`
	Review      ReviewBlock  `json:"review"`
	Workload    Workload     `json:"workload"`
	Usage       *UsageBlock  `json:"usage"`
	Notes       []string     `json:"notes"`
	GeneratedAt int64        `json:"generated_at"`
	Cached      bool         `json:"cached"`
}

// RangeInfo echoes the resolved window: [From, To) in unix seconds, the tz used, and
// the first job's start (the 「全部」 origin).
type RangeInfo struct {
	Key        string `json:"key"`
	From       int64  `json:"from"`
	To         int64  `json:"to"`
	TZ         int    `json:"tz"`
	FirstJobAt int64  `json:"first_job_at"`
}

// Totals is the title-line summary.
type Totals struct {
	Jobs     int   `json:"jobs"`
	Sessions int   `json:"sessions"`
	WallSec  int64 `json:"wall_sec"`
}

// JobsBlock is the Jobs card. Failed folds timeout in; InProgress is a live snapshot
// (not range-bound). SuccessRate = done / (done + failed + timeout + rejected).
type JobsBlock struct {
	Total       int      `json:"total"`
	Done        int      `json:"done"`
	Failed      int      `json:"failed"`
	Cancelled   int      `json:"cancelled"`
	Rejected    int      `json:"rejected"`
	NeedsReview int      `json:"needs_review"`
	InProgress  int      `json:"in_progress"`
	SuccessRate *float64 `json:"success_rate"`
}

// TimeBlock is the 耗时 card. Active / human-wait come from job_metrics rows only
// (nil when none of the range's jobs has one).
type TimeBlock struct {
	WallSec      int64  `json:"wall_sec"`
	AvgSec       *int64 `json:"avg_sec"`
	MedianSec    *int64 `json:"median_sec"`
	ActiveSec    *int64 `json:"active_sec"`
	HumanWaitSec *int64 `json:"human_wait_sec"`
}

// GitBlock is the Git 活动 card. Commits come from every job's commit list; the line
// counts only from jobs whose metrics recorded them (GitJobs of them).
type GitBlock struct {
	Commits         int64  `json:"commits"`
	JobsWithCommits int    `json:"jobs_with_commits"`
	Files           *int64 `json:"files"`
	Insertions      *int64 `json:"insertions"`
	Deletions       *int64 `json:"deletions"`
	GitJobs         int    `json:"git_jobs"`
}

// SignalBlock is the 信号 card over the non-exec ended jobs (DenominatorJobs). Turns /
// ToolCalls sum the jobs that recorded them (TurnJobs / ToolJobs); Coverage is the share
// of the denominator with a job_metrics row. nil when no such job has a row.
type SignalBlock struct {
	Turns           *int64  `json:"turns"`
	ToolCalls       *int64  `json:"tool_calls"`
	Human           int64   `json:"human"`
	JobsWithHuman   int     `json:"jobs_with_human"`
	DenominatorJobs int     `json:"denominator_jobs"`
	TurnJobs        int     `json:"turn_jobs"`
	ToolJobs        int     `json:"tool_jobs"`
	Coverage        float64 `json:"coverage"`
}

// DayRow is one local day of the range (every day present, zero-filled).
type DayRow struct {
	Day     string `json:"day"`
	Done    int    `json:"done"`
	Failed  int    `json:"failed"`
	Commits int64  `json:"commits"`
	WallSec int64  `json:"wall_sec"`
}

// Best is the 最高产 panel.
type Best struct {
	Weekday    *BestWeekday `json:"weekday"`
	Day        *BestDay     `json:"day"`
	Month      *BestMonth   `json:"month,omitempty"`
	DailyAvg   *float64     `json:"daily_avg,omitempty"`
	StreakDays int          `json:"streak_days"`
}

// BestWeekday: Dow is 0=Sunday … 6=Saturday (JS Date.getDay), Avg the mean done/day.
type BestWeekday struct {
	Dow int     `json:"dow"`
	Avg float64 `json:"avg"`
}

// BestDay is the single most productive day.
type BestDay struct {
	Day  string `json:"day"`
	Done int    `json:"done"`
}

// BestMonth is the most productive month (range=all only).
type BestMonth struct {
	Month string `json:"month"`
	Done  int    `json:"done"`
}

// Heatmap is the activity grid: Weeks columns ending this week, Days the done count of
// every producing day in that window, Levels the three thresholds (25th / 50th / 80th
// percentile of the producing days) that split the four shades.
type Heatmap struct {
	Weeks  int       `json:"weeks"`
	Levels []int     `json:"levels"`
	Days   []HeatDay `json:"days"`
}

// HeatDay is one producing day of the heatmap window.
type HeatDay struct {
	Day  string `json:"day"`
	Done int    `json:"done"`
}

// AgentRow is one agent bar.
type AgentRow struct {
	Agent       string   `json:"agent"`
	Jobs        int      `json:"jobs"`
	Done        int      `json:"done"`
	SuccessRate *float64 `json:"success_rate"`
	AvgSec      *int64   `json:"avg_sec"`
}

// ProjectRow is one project (the page ranks the top 3 per column).
type ProjectRow struct {
	Project string `json:"project"`
	Jobs    int    `json:"jobs"`
	WallSec int64  `json:"wall_sec"`
	Commits int64  `json:"commits"`
}

// ReviewBlock is the 验收与计划 row.
type ReviewBlock struct {
	Reviewed      int      `json:"reviewed"`
	Accepted      int      `json:"accepted"`
	Rejected      int      `json:"rejected"`
	Rerun         int      `json:"rerun"`
	AcceptRate    *float64 `json:"accept_rate"`
	RejectRate    *float64 `json:"reject_rate"`
	WaitAvgSec    *int64   `json:"wait_avg_sec"`
	WaitMedianSec *int64   `json:"wait_median_sec"`
	PendingNow    int      `json:"pending_now"`
	PlansDone     int      `json:"plans_done"`
	TodosDone     int      `json:"todos_done"`
}

// Workload is the 耗时分布 pair.
type Workload struct {
	Longest  []JobBrief `json:"longest"`
	Quickest []JobBrief `json:"quickest"`
}

// JobBrief is one workload row. ActiveSec nil = the job has no metrics row (ranked by
// its wall time instead).
type JobBrief struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Agent     string `json:"agent"`
	Project   string `json:"project"`
	Status    string `json:"status"`
	Turns     *int64 `json:"turns"`
	ActiveSec *int64 `json:"active_sec"`
	WallSec   int64  `json:"wall_sec"`
}

// UsageBlock is the 用量 section: job + terminal-session spend and tokens. nil when
// neither side reported anything in the range.
type UsageBlock struct {
	CostUSD         float64    `json:"cost_usd"`
	JobCostUSD      float64    `json:"job_cost_usd"`
	SessionCostUSD  float64    `json:"session_cost_usd"`
	InputTokens     int64      `json:"input_tokens"`
	OutputTokens    int64      `json:"output_tokens"`
	CacheReadTokens int64      `json:"cache_read_tokens"`
	PerTurnUSD      *float64   `json:"per_turn_usd"`
	PerJobUSD       *float64   `json:"per_job_usd"`
	JobsWithUsage   int        `json:"jobs_with_usage"`
	Sessions        int        `json:"sessions"`
	ByModel         []ModelRow `json:"by_model"`
}

// ModelRow is one model's spend. Model "" = the agent's default model (Agent names
// it); Source is job / session / job+session.
type ModelRow struct {
	Model           string  `json:"model"`
	Agent           string  `json:"agent,omitempty"`
	Source          string  `json:"source"`
	CostUSD         float64 `json:"cost_usd"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CacheReadTokens int64   `json:"cache_read_tokens"`
}

// Build reads the store and folds one overview (uncached).
func Build(st *jobstore.Store, q Query, now time.Time) (*Overview, error) {
	q, err := q.Normalize()
	if err != nil {
		return nil, err
	}
	in, err := load(st, q, now.Unix())
	if err != nil {
		return nil, err
	}
	return aggregate(in, q, now.Unix()), nil
}
