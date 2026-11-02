package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// WorkbenchPageQuery selects one keyset page of workbench threads, newest activity first.
//
// A thread is a session chain of jobs (id "s:<session>"), a lone interactive job ("j:<id>")
// or a relay session ("r:<session>"); its activity is the newest updated_at of its rows.
// Threads that are live (a non-terminal job, a relay session that is neither ended nor
// offline — an offline one stopped heart-beating and may be long dead) or pinned
// are exceptions: they are returned on the first page (no cursor) whatever their activity
// and never count towards Limit; later pages carry ordinary threads only.
type WorkbenchPageQuery struct {
	// Since is the activity floor of the ordinary threads (0 = no floor).
	Since int64
	// Cursor is the position after which ordinary threads are returned (zero = first page).
	CursorAt int64
	CursorID string
	// Limit is the number of ordinary threads wanted; one more is read to learn HasMore.
	Limit int
	// Project narrows to one project key (row level; "" = all).
	Project string
	// PinnedIDs are the caller's pinned thread ids.
	PinnedIDs       []string
	IncludeInternal bool
}

// WorkbenchThreadKey is one thread's position in the activity order.
type WorkbenchThreadKey struct {
	ID       string
	Activity int64
	// Exception marks a live or pinned thread delivered outside the window / limit.
	Exception bool
}

// WorkbenchPage is one page: Keys holds the exceptions (first page only) followed by the
// ordinary threads in (activity desc, id desc) order.
type WorkbenchPage struct {
	Snapshot WorkbenchSnapshot
	Keys     []WorkbenchThreadKey
	// HasMore: ordinary threads remain at or above Since after the last returned one.
	HasMore bool
	// BelowFloor: ordinary threads older than Since exist (only computed when HasMore is false).
	BelowFloor bool
}

// workbenchThreadCTE lists every workbench thread with its activity and exception flag.
// Parameters: ?1 pinned ids (JSON array), ?2 project ("" = all). Jobs are filtered the way
// projectThreads keeps them (interactive or resident-session jobs) so a batch job never
// makes a thread key.
func workbenchThreadCTE(includeInternal bool) string {
	visible := workbenchVisibleSQL
	if includeInternal {
		visible = `1=1`
	}
	return `WITH th AS (
  SELECT CASE WHEN COALESCE(session_id,'')<>'' THEN 's:' || session_id ELSE 'j:' || id END AS tid,
    MAX(updated_at) AS act,
    MAX(CASE WHEN ` + workbenchNonTerminalSQL + ` THEN 1 ELSE 0 END) AS live,
    MAX(CASE WHEN 'j:' || id IN (SELECT value FROM json_each(?1)) THEN 1 ELSE 0 END) AS legacy_pin
  FROM jobs
  WHERE ` + visible + ` AND (COALESCE(interactive,0)<>0 OR COALESCE(session_state_json,'')<>'')
    AND (?2='' OR project_key=?2)
  GROUP BY tid
  UNION ALL
  SELECT 'r:' || session_id, last_seen_at, CASE WHEN state NOT IN ('` + SessionEnded + `','` + SessionOffline + `') THEN 1 ELSE 0 END, 0
  FROM agent_sessions
  WHERE ` + internalSessionSQL + ` AND (?2='' OR project_key=?2)
), t AS (
  SELECT tid, act,
    CASE WHEN live=1 OR legacy_pin=1 OR tid IN (SELECT value FROM json_each(?1)) THEN 1 ELSE 0 END AS exc
  FROM th
)
`
}

// workbenchCursorSQL is the keyset condition "strictly after the cursor" (?4 at, ?5 id,
// ?6 has-cursor flag) over (activity desc, id desc).
const workbenchCursorSQL = `(?6=0 OR act<?4 OR (act=?4 AND tid<?5))`

// LoadWorkbenchPage loads one keyset page of threads and the projection inputs for exactly
// those threads. The window (Since) and the cursor are applied in SQL; only the shared,
// window-independent parts (pending interactions, decisions, plans, prefs) are loaded whole.
func (s *Store) LoadWorkbenchPage(callerID string, q WorkbenchPageQuery) (WorkbenchPage, error) {
	if q.Since < 0 || q.Limit <= 0 {
		return WorkbenchPage{}, fmt.Errorf("jobstore: workbench page: invalid since/limit")
	}
	pinned, err := json.Marshal(append([]string{}, q.PinnedIDs...))
	if err != nil {
		return WorkbenchPage{}, fmt.Errorf("jobstore: workbench page: %w", err)
	}
	hasCursor := 0
	if q.CursorAt > 0 || q.CursorID != "" {
		hasCursor = 1
	}
	cte := workbenchThreadCTE(q.IncludeInternal)
	var page WorkbenchPage
	if hasCursor == 0 {
		keys, err := s.queryWorkbenchKeys(cte+`SELECT tid, act, exc FROM t WHERE exc=1 ORDER BY act DESC, tid DESC`,
			string(pinned), q.Project)
		if err != nil {
			return WorkbenchPage{}, err
		}
		page.Keys = keys
	}
	ordinary, err := s.queryWorkbenchKeys(cte+`SELECT tid, act, exc FROM t
  WHERE exc=0 AND act>=?3 AND `+workbenchCursorSQL+` ORDER BY act DESC, tid DESC LIMIT ?7`,
		string(pinned), q.Project, q.Since, q.CursorAt, q.CursorID, hasCursor, q.Limit+1)
	if err != nil {
		return WorkbenchPage{}, err
	}
	if len(ordinary) > q.Limit {
		ordinary, page.HasMore = ordinary[:q.Limit], true
	}
	page.Keys = append(page.Keys, ordinary...)
	if !page.HasMore && q.Since > 0 {
		var one int
		err := s.db.QueryRow(cte+`SELECT 1 FROM t WHERE exc=0 AND act<?3 AND `+workbenchCursorSQL+` LIMIT 1`,
			string(pinned), q.Project, q.Since, q.CursorAt, q.CursorID, hasCursor).Scan(&one)
		switch {
		case err == nil:
			page.BelowFloor = true
		case !errors.Is(err, sql.ErrNoRows):
			return WorkbenchPage{}, fmt.Errorf("jobstore: workbench page below floor: %w", err)
		}
	}
	snap, err := s.loadWorkbenchShared(callerID)
	if err != nil {
		return WorkbenchPage{}, err
	}
	var sessionIDs, jobIDs, relayIDs []string
	for _, k := range page.Keys {
		switch {
		case strings.HasPrefix(k.ID, "s:"):
			sessionIDs = append(sessionIDs, k.ID[2:])
		case strings.HasPrefix(k.ID, "j:"):
			jobIDs = append(jobIDs, k.ID[2:])
		case strings.HasPrefix(k.ID, "r:"):
			relayIDs = append(relayIDs, k.ID[2:])
		}
	}
	if snap.Jobs, err = s.listWorkbenchJobsByThread(sessionIDs, jobIDs, q.IncludeInternal); err != nil {
		return WorkbenchPage{}, err
	}
	if snap.Sessions, err = s.listWorkbenchSessionsByID(relayIDs); err != nil {
		return WorkbenchPage{}, err
	}
	page.Snapshot = snap
	return page, nil
}

func (s *Store) queryWorkbenchKeys(query string, args ...any) ([]WorkbenchThreadKey, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: workbench thread keys: %w", err)
	}
	defer rows.Close()
	var out []WorkbenchThreadKey
	for rows.Next() {
		var k WorkbenchThreadKey
		var exc int
		if err := rows.Scan(&k.ID, &k.Activity, &exc); err != nil {
			return nil, fmt.Errorf("jobstore: scan workbench thread key: %w", err)
		}
		k.Exception = exc != 0
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: workbench thread keys rows: %w", err)
	}
	return out, nil
}

// listWorkbenchJobsByThread loads the job rows of the given session chains and lone jobs.
func (s *Store) listWorkbenchJobsByThread(sessionIDs, jobIDs []string, includeInternal bool) ([]JobRecord, error) {
	if len(sessionIDs) == 0 && len(jobIDs) == 0 {
		return []JobRecord{}, nil
	}
	visible := workbenchVisibleSQL
	if includeInternal {
		visible = `1=1`
	}
	sessions, _ := json.Marshal(append([]string{}, sessionIDs...))
	jobs, _ := json.Marshal(append([]string{}, jobIDs...))
	rows, err := s.db.Query(selectCols+` WHERE `+visible+` AND (
	(COALESCE(session_id,'')<>'' AND session_id IN (SELECT value FROM json_each(?1)))
	OR (COALESCE(session_id,'')='' AND id IN (SELECT value FROM json_each(?2))))
  ORDER BY started_at ASC, id ASC`, string(sessions), string(jobs))
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workbench page jobs: %w", err)
	}
	defer rows.Close()
	out := make([]JobRecord, 0)
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan workbench page job: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workbench page jobs rows: %w", err)
	}
	return out, nil
}

func (s *Store) listWorkbenchSessionsByID(ids []string) ([]AgentSession, error) {
	if len(ids) == 0 {
		return []AgentSession{}, nil
	}
	raw, _ := json.Marshal(ids)
	rows, err := s.db.Query(workbenchSessionSelect()+`
  WHERE session_id IN (SELECT value FROM json_each(?1)) ORDER BY started_at ASC, session_id ASC`, string(raw))
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workbench page sessions: %w", err)
	}
	defer rows.Close()
	out := make([]AgentSession, 0)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan workbench page session: %w", err)
		}
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workbench page session rows: %w", err)
	}
	return out, nil
}
