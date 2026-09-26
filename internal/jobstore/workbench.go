package jobstore

import (
	"errors"
	"fmt"
)

// WorkbenchThreadPref is the per-caller presentation state for one canonical
// workbench thread id. It deliberately contains no derived thread status.
type WorkbenchThreadPref struct {
	CallerID string
	ThreadID string
	Title    string
	SeenAt   int64
	Pinned   bool
}

// WorkbenchSnapshot is the fixed-query input to the workbench domain projector.
// Each slice is loaded in bulk; consumers must join it in memory rather than issue
// a query per thread/job.
type WorkbenchSnapshot struct {
	Jobs                []JobRecord
	PendingInteractions []InteractionRecord
	StalledJobIDs       map[string]bool
	Sessions            []AgentSession
	RelayDecisions      []PlanDecision
	BlockedPlans        []Plan
	OpenPlanDecisions   []PlanDecision
	Prefs               []WorkbenchThreadPref
}

// UpsertWorkbenchThreadPref replaces one caller/thread preference row.
func (s *Store) UpsertWorkbenchThreadPref(pref WorkbenchThreadPref) error {
	if pref.CallerID == "" {
		return errors.New("jobstore: workbench pref: empty caller id")
	}
	if pref.ThreadID == "" {
		return errors.New("jobstore: workbench pref: empty thread id")
	}
	pinned := 0
	if pref.Pinned {
		pinned = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO workbench_thread_prefs
  (caller_id, thread_id, title, seen_at, pinned) VALUES (?,?,?,?,?)
  ON CONFLICT(caller_id, thread_id) DO UPDATE SET
    title=excluded.title, seen_at=excluded.seen_at, pinned=excluded.pinned`,
		pref.CallerID, pref.ThreadID, pref.Title, pref.SeenAt, pinned)
	if err != nil {
		return fmt.Errorf("jobstore: upsert workbench pref %q/%q: %w", pref.CallerID, pref.ThreadID, err)
	}
	return nil
}

// ListWorkbenchThreadPrefs returns every preference row for caller. The table's
// compound primary key keeps this bounded to that caller without a second index.
func (s *Store) ListWorkbenchThreadPrefs(callerID string) ([]WorkbenchThreadPref, error) {
	if callerID == "" {
		return nil, errors.New("jobstore: list workbench prefs: empty caller id")
	}
	rows, err := s.db.Query(`SELECT caller_id, thread_id, title, seen_at, pinned
  FROM workbench_thread_prefs WHERE caller_id=? ORDER BY thread_id`, callerID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workbench prefs: %w", err)
	}
	defer rows.Close()
	out := make([]WorkbenchThreadPref, 0)
	for rows.Next() {
		var pref WorkbenchThreadPref
		var pinned int
		if err := rows.Scan(&pref.CallerID, &pref.ThreadID, &pref.Title, &pref.SeenAt, &pinned); err != nil {
			return nil, fmt.Errorf("jobstore: scan workbench pref: %w", err)
		}
		pref.Pinned = pinned != 0
		out = append(out, pref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workbench prefs rows: %w", err)
	}
	return out, nil
}

// LoadWorkbenchSnapshot loads every W1 projection input with a fixed number of
// bulk queries. since is a unix-second activity watermark. A qualifying job with
// a session selects the entire session chain so its first title and turn count do
// not disappear when only the latest turn is recent.
func (s *Store) LoadWorkbenchSnapshot(callerID string, since int64) (WorkbenchSnapshot, error) {
	if since < 0 {
		return WorkbenchSnapshot{}, errors.New("jobstore: workbench snapshot: negative since")
	}
	jobs, err := s.listWorkbenchJobs(since)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	pending, err := s.ListPendingInteractions()
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	stalled, err := s.listWorkbenchStalledJobs()
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	sessions, err := s.listWorkbenchSessions(since)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	// Keep the decision channel's existing lazy-expiry contract before selecting
	// OPEN relay/project decisions for the attention projection.
	if err := s.expireDueDecisions(); err != nil {
		return WorkbenchSnapshot{}, err
	}
	relayDecisions, err := s.listWorkbenchDecisions(true)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	blockedPlans, err := s.listWorkbenchBlockedPlans()
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	openPlanDecisions, err := s.listWorkbenchDecisions(false)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	prefs, err := s.ListWorkbenchThreadPrefs(callerID)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	return WorkbenchSnapshot{
		Jobs:                jobs,
		PendingInteractions: pending,
		StalledJobIDs:       stalled,
		Sessions:            sessions,
		RelayDecisions:      relayDecisions,
		BlockedPlans:        blockedPlans,
		OpenPlanDecisions:   openPlanDecisions,
		Prefs:               prefs,
	}, nil
}

const workbenchNonTerminalSQL = `status NOT IN ('done','failed','cancelled','timeout','rejected')`

func (s *Store) listWorkbenchJobs(since int64) ([]JobRecord, error) {
	query := selectCols + ` WHERE
  (COALESCE(session_id,'') = '' AND (updated_at >= ? OR ` + workbenchNonTerminalSQL + `))
  OR
  (COALESCE(session_id,'') <> '' AND session_id IN (
    SELECT DISTINCT session_id FROM jobs
    WHERE COALESCE(session_id,'') <> '' AND (updated_at >= ? OR ` + workbenchNonTerminalSQL + `)
  ))
  ORDER BY started_at ASC, id ASC`
	rows, err := s.db.Query(query, since, since)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workbench jobs: %w", err)
	}
	defer rows.Close()
	out := make([]JobRecord, 0)
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan workbench job: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workbench jobs rows: %w", err)
	}
	return out, nil
}

func (s *Store) listWorkbenchStalledJobs() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT e.job_id
  FROM job_events e JOIN jobs j ON j.id=e.job_id
  WHERE e.type='job.stalled' AND j.status='running'`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workbench stalled jobs: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("jobstore: scan workbench stalled job: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workbench stalled rows: %w", err)
	}
	return out, nil
}

func (s *Store) listWorkbenchSessions(since int64) ([]AgentSession, error) {
	rows, err := s.db.Query(selectSessionCols+`
  WHERE last_seen_at >= ? OR state <> ?
  ORDER BY started_at ASC, session_id ASC`, since, SessionEnded)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workbench sessions: %w", err)
	}
	defer rows.Close()
	out := make([]AgentSession, 0)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan workbench session: %w", err)
		}
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workbench session rows: %w", err)
	}
	return out, nil
}

func (s *Store) listWorkbenchDecisions(relay bool) ([]PlanDecision, error) {
	query := selectDecisionCols + ` WHERE state=?`
	args := []any{DecisionOpen}
	if relay {
		query += ` AND kind=? AND COALESCE(session_id,'')<>''`
		args = append(args, DecisionKindRelay)
	} else {
		query += ` AND COALESCE(kind,'')<>? AND COALESCE(plan_id,'')<>''`
		args = append(args, DecisionKindRelay)
	}
	query += ` ORDER BY asked_at ASC, id ASC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workbench decisions: %w", err)
	}
	defer rows.Close()
	out := make([]PlanDecision, 0)
	for rows.Next() {
		decision, err := scanDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan workbench decision: %w", err)
		}
		out = append(out, decision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workbench decision rows: %w", err)
	}
	return out, nil
}

func (s *Store) listWorkbenchBlockedPlans() ([]Plan, error) {
	rows, err := s.db.Query(selectPlanCols+` WHERE status=? ORDER BY updated_at ASC, plan_id ASC`, PlanBlocked)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workbench blocked plans: %w", err)
	}
	defer rows.Close()
	out := make([]Plan, 0)
	for rows.Next() {
		plan, err := scanPlan(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan workbench blocked plan: %w", err)
		}
		out = append(out, plan)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workbench blocked plan rows: %w", err)
	}
	return out, nil
}
