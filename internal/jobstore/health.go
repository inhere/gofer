package jobstore

import "fmt"

// agentHealthSuccessSQL is the status predicate that counts as a PROVIDER success in
// the health aggregate (SUP-01 P3). `needs_review` counts: the agent DELIVERED the
// work and the provider behaved — a human deciding on the delivery is not a provider
// failure, and excluding it would leave a require_review project permanently
// degraded after its first three provider errors.
const agentHealthSuccessSQL = `j.status IN ('done','needs_review')`

// agentHealthQuery aggregates the jobs table per agent over a time window
// (SUP-01 P3). One statement answers every agent's 1h picture — the read behind
// `gofer agent status`, the web badge and the pre-dispatch decision — because the
// per-agent loop it replaces would issue one query per agent per page render.
//
// Columns: agent, jobs, ok, transient_fail, other_fail, last_ok_at,
// last_transient_at, ok_since_transient. The LEFT JOIN freezes each agent's last
// transient failure time so ok_since_transient (the RECOVERY count) is computed in
// the same pass. Times are COALESCEd to ended_at, falling back to started_at: a
// terminal row always has ended_at, and the fallback keeps a half-written row from
// reading as a success at time 0.
const agentHealthQuery = `SELECT j.agent,
  COUNT(*),
  COALESCE(SUM(CASE WHEN ` + agentHealthSuccessSQL + ` THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN j.failure_class = 'transient' THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN j.status = 'failed' AND COALESCE(j.failure_class,'') <> 'transient' THEN 1 ELSE 0 END),0),
  COALESCE(MAX(CASE WHEN ` + agentHealthSuccessSQL + ` THEN COALESCE(j.ended_at, j.started_at) END),0),
  COALESCE(MAX(CASE WHEN j.failure_class = 'transient' THEN COALESCE(j.ended_at, j.started_at) END),0),
  COALESCE(SUM(CASE WHEN ` + agentHealthSuccessSQL + `
        AND t.last_transient_at IS NOT NULL
        AND COALESCE(j.ended_at, j.started_at) > t.last_transient_at THEN 1 ELSE 0 END),0)
FROM jobs j
LEFT JOIN (
  SELECT agent, MAX(COALESCE(ended_at, started_at)) AS last_transient_at
  FROM jobs WHERE started_at >= ? AND failure_class = 'transient' GROUP BY agent
) t ON t.agent = j.agent
WHERE j.started_at >= ? AND (? = '' OR j.agent = ?)
GROUP BY j.agent`

// AgentHealth is the recent-job picture of ONE agent (SUP-01 P3): how many jobs it
// ran inside the health window, how they ended, and when the two kinds of outcome
// last happened. It is a pure aggregation of the jobs table — no separate health
// store, no background task, and every value is explainable from the job rows.
type AgentHealth struct {
	Agent string
	// Jobs is every job of the agent inside the window, whatever its status.
	Jobs int
	// OK counts delivered jobs (done or needs_review — see agentHealthSuccessSQL).
	OK int
	// TransientFail counts failures classified as provider errors, OtherFail every
	// other failure. Cancelled/timeout/queued jobs count in neither.
	TransientFail int
	OtherFail     int
	// OKSinceTransient counts the successes AFTER the last transient failure — the
	// recovery evidence HealthState needs (a configured recover_after_ok > 1 cannot
	// be decided from LastOKAt alone).
	OKSinceTransient int
	// LastOKAt / LastTransientAt are the unix seconds of those last outcomes
	// (0 = none inside the window).
	LastOKAt        int64
	LastTransientAt int64
}

// AgentHealth aggregates one agent's jobs with started_at >= since. An agent with no
// job in the window yields a zero record (Jobs == 0), which is what makes "no
// evidence" distinguishable from "healthy" upstream.
func (s *Store) AgentHealth(agent string, since int64) (AgentHealth, error) {
	all, err := s.agentHealth(since, agent)
	if err != nil {
		return AgentHealth{}, err
	}
	h, ok := all[agent]
	if !ok {
		return AgentHealth{Agent: agent}, nil
	}
	return h, nil
}

// AgentHealthAll aggregates every agent's jobs with started_at >= since in one
// query, keyed by agent key. Agents with no job in the window are simply absent from
// the map (callers substitute a zero record).
func (s *Store) AgentHealthAll(since int64) (map[string]AgentHealth, error) {
	return s.agentHealth(since, "")
}

// agentHealth runs the shared aggregation; a non-empty agent restricts it to that one
// agent (the single-agent read). Reads need no write lock (see Store.writeMu).
func (s *Store) agentHealth(since int64, agent string) (map[string]AgentHealth, error) {
	rows, err := s.db.Query(agentHealthQuery, since, since, agent, agent)
	if err != nil {
		return nil, fmt.Errorf("jobstore: agent health: %w", err)
	}
	defer rows.Close()
	out := map[string]AgentHealth{}
	for rows.Next() {
		var h AgentHealth
		if err := rows.Scan(&h.Agent, &h.Jobs, &h.OK, &h.TransientFail, &h.OtherFail,
			&h.LastOKAt, &h.LastTransientAt, &h.OKSinceTransient); err != nil {
			return nil, fmt.Errorf("jobstore: scan agent health: %w", err)
		}
		out[h.Agent] = h
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: agent health rows: %w", err)
	}
	return out, nil
}
