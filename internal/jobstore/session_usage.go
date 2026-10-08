package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inhere/gofer/internal/runner"
)

// Terminal-session token usage (N2 §A, OBS-14). The hook reports DELTAS; the store
// adds them to the session row (agent_sessions.usage_json, additive) and to the per-day
// per-model tally (session_usage_daily) the stats window sums.

// ParseSessionUsage reads a session's stored usage_json ("" or unreadable = zero).
func ParseSessionUsage(raw string) runner.SessionUsage {
	var u runner.SessionUsage
	if raw == "" {
		return u
	}
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return runner.SessionUsage{}
	}
	return u
}

// SessionUsageDay is the daily-tally bucket of a unix time: its UTC date.
func SessionUsageDay(unix int64) string { return time.Unix(unix, 0).UTC().Format("2006-01-02") }

// AddSessionUsage adds one reported delta to the session and to today's tally. A
// session unknown to the store, or an empty delta, is a no-op (ok=false for unknown).
func (s *Store) AddSessionUsage(sid string, delta runner.SessionUsage) (bool, error) {
	if delta.Empty() {
		return true, nil
	}
	now := s.unixNow()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("jobstore: add session usage: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var raw, project, agent string
	err = tx.QueryRow(`SELECT COALESCE(usage_json,''), COALESCE(project_key,''), COALESCE(agent,'')
FROM agent_sessions WHERE session_id=?`, sid).Scan(&raw, &project, &agent)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("jobstore: add session usage: %w", err)
	}
	total := ParseSessionUsage(raw)
	total.Add(delta)
	b, err := json.Marshal(total)
	if err != nil {
		return false, fmt.Errorf("jobstore: add session usage: %w", err)
	}
	if _, err := tx.Exec(`UPDATE agent_sessions SET usage_json=? WHERE session_id=?`, string(b), sid); err != nil {
		return false, fmt.Errorf("jobstore: add session usage: %w", err)
	}
	day := SessionUsageDay(now)
	for model, u := range dailyModels(delta) {
		if _, err := tx.Exec(`INSERT INTO session_usage_daily
(day, session_id, model, project_key, agent, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_tokens, cost_usd)
VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(day, session_id, model) DO UPDATE SET
  input_tokens=input_tokens+excluded.input_tokens, output_tokens=output_tokens+excluded.output_tokens,
  cache_read_tokens=cache_read_tokens+excluded.cache_read_tokens,
  cache_write_tokens=cache_write_tokens+excluded.cache_write_tokens,
  total_tokens=total_tokens+excluded.total_tokens, cost_usd=cost_usd+excluded.cost_usd,
  project_key=excluded.project_key, agent=excluded.agent`,
			day, sid, model, project, agent, u.InputTokens, u.OutputTokens, u.CacheReadTokens,
			u.CacheWriteTokens, u.TotalTokens, u.CostUSD); err != nil {
			return false, fmt.Errorf("jobstore: add session usage daily: %w", err)
		}
	}
	return true, tx.Commit()
}

// dailyModels is the per-model split of a delta; a delta with no model breakdown (a
// transcript that never named its models) is booked under "unknown" so the day total
// still counts it.
func dailyModels(d runner.SessionUsage) map[string]runner.Usage {
	if len(d.ByModel) > 0 {
		return d.ByModel
	}
	return map[string]runner.Usage{"unknown": d.Total()}
}

// SessionUsageWindow is the terminal-session usage inside one stats window.
type SessionUsageWindow struct {
	Sessions int
	Total    runner.Usage
	ByAgent  map[string]runner.Usage
}

// SessionUsageStats is SessionUsageWindow per window label ("24h", "7d"). The tally is
// per UTC day, so a window covers every day bucket from the day of (now - window) up to
// today: up to one extra day at the old edge.
type SessionUsageStats struct {
	Windows map[string]SessionUsageWindow
}

// SessionUsageStats sums session_usage_daily for each window ending at now.
func (s *Store) SessionUsageStats(now int64, windows []time.Duration) (SessionUsageStats, error) {
	out := SessionUsageStats{Windows: make(map[string]SessionUsageWindow, len(windows))}
	for _, w := range windows {
		since := SessionUsageDay(now - int64(w/time.Second))
		rows, err := s.db.Query(`SELECT agent, COUNT(DISTINCT session_id), SUM(input_tokens), SUM(output_tokens),
  SUM(cache_read_tokens), SUM(cache_write_tokens), SUM(total_tokens), SUM(cost_usd)
FROM session_usage_daily WHERE day >= ? GROUP BY agent`, since)
		if err != nil {
			return SessionUsageStats{}, fmt.Errorf("jobstore: session usage stats: %w", err)
		}
		win := SessionUsageWindow{ByAgent: map[string]runner.Usage{}}
		for rows.Next() {
			var agent string
			var n int
			var u runner.Usage
			if err := rows.Scan(&agent, &n, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens,
				&u.CacheWriteTokens, &u.TotalTokens, &u.CostUSD); err != nil {
				_ = rows.Close()
				return SessionUsageStats{}, fmt.Errorf("jobstore: scan session usage: %w", err)
			}
			win.ByAgent[agent] = u
			win.Sessions += n // a session has one agent, so per-agent distinct counts add up
			win.Total = win.Total.Plus(u)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return SessionUsageStats{}, fmt.Errorf("jobstore: session usage stats: %w", err)
		}
		_ = rows.Close()
		out.Windows[windowLabel(w)] = win
	}
	return out, nil
}
