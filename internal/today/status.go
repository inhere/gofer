package today

import (
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// Status is the bottom status bar (design §1.4): runner, today's usage, the steward and
// the version, plus the alerts that turn an item red and move it first.
type Status struct {
	UsageToday   UsageToday   `json:"usage_today"`
	StewardToday StewardToday `json:"steward_today"`
	Runners      Runners      `json:"runners"`
	Version      string       `json:"version"`
	Alerts       []string     `json:"alerts"`
}

// UsageToday sums today's job usage (by start time, server-local day) and today's
// terminal-session usage (OBS-14, tallied per UTC day).
type UsageToday struct {
	Jobs          int     `json:"jobs"`
	TotalTokens   int64   `json:"total_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	SessionTokens int64   `json:"session_tokens"`
}

// StewardToday is the steward's day: whether it runs, its state, the notes it wrote and
// the auto tidy-ups done today, and today's review state.
type StewardToday struct {
	Enabled   bool   `json:"enabled"`
	State     string `json:"state,omitempty"`
	Notes     int    `json:"notes"`
	Summaries int    `json:"summaries"`
	Review    string `json:"review,omitempty"`
}

// Runners is the runner part of the status bar plus the jobs running now.
type Runners struct {
	RunnerStatus
	RunningJobs int `json:"running_jobs"`
}

func (s *Service) digest(now time.Time, since int64) (Digest, error) {
	if since <= 0 {
		since = startOfDay(now).Unix()
	}
	counts, err := s.d.Store.JobOutcomesSince(since)
	if err != nil {
		return Digest{}, err
	}
	d := Digest{SinceLast: SinceLast{Since: since, JobsDone: counts.Done, JobsFailed: counts.Failed, Commits: counts.Commits}}
	if s.d.Work != nil {
		wd, err := s.d.Work.BuildDigest(now)
		if err != nil {
			return Digest{}, err
		}
		d.Title, d.Text, d.Commentary = wd.Title, wd.Text, wd.Commentary
	}
	return d, nil
}

func (s *Service) status(now time.Time) (Status, error) {
	st := Status{Alerts: []string{}}
	midnight := startOfDay(now)
	usage, err := s.d.Store.UsageStats(now.Unix(), []time.Duration{now.Sub(midnight) + time.Second}, 200*time.Millisecond)
	if err != nil {
		return Status{}, err
	}
	for _, w := range usage.Windows {
		st.UsageToday.Jobs, st.UsageToday.TotalTokens, st.UsageToday.CostUSD = w.Total.Jobs, w.Total.TotalTokens, w.Total.CostUSD
	}
	sess, err := s.d.Store.SessionUsageStats(now.Unix(), []time.Duration{0})
	if err != nil {
		return Status{}, err
	}
	for _, w := range sess.Windows {
		st.UsageToday.SessionTokens = w.Total.TotalTokens
	}

	if s.d.Steward != nil {
		ss := s.d.Steward.Status()
		st.StewardToday.Enabled, st.StewardToday.State = ss.Enabled, ss.State
		if ss.LastReview != nil && ss.LastReview.Day == now.Format("2006-01-02") {
			st.StewardToday.Review = ss.LastReview.State
			if ss.LastReview.State == jobstore.StewardReviewFailed {
				st.Alerts = append(st.Alerts, "管家今日巡检失败")
			}
		}
		if ss.AgentError != "" && ss.Enabled {
			st.Alerts = append(st.Alerts, "管家 agent 不可用")
		}
	}
	journal, err := s.d.Store.ListRecentWorkJournal(midnight.Unix(), 2000)
	if err != nil {
		return Status{}, err
	}
	for _, e := range journal {
		if e.Kind == jobstore.WorkJournalSteward {
			st.StewardToday.Notes++
		}
	}
	if st.StewardToday.Summaries, err = s.d.Store.CountAutoWorkSummaries(midnight.Unix()); err != nil {
		return Status{}, err
	}

	if s.d.Runners != nil {
		st.Runners.RunnerStatus = s.d.Runners()
		for _, name := range st.Runners.Offline {
			st.Alerts = append([]string{"runner " + name + " 离线"}, st.Alerts...)
		}
	}
	byStatus, err := s.d.Store.CountJobsByStatus()
	if err != nil {
		return Status{}, err
	}
	st.Runners.RunningJobs = byStatus[job.StatusRunning]
	if s.d.Version != nil {
		st.Version = s.d.Version()
	}
	return st, nil
}
