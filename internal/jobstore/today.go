package jobstore

import (
	"fmt"
	"strings"
)

// N3 「今天」 (decision home) reads. The aggregation itself lives in internal/today;
// these are the few bulk queries it needs that no other surface had yet.

// TodayActionAudit is the audit_events kind the home page records after a card action
// succeeded (design §2.3 「已处理」): target_id is the card key, detail_json carries the
// action id, the advice action id and the display title of the card at that moment.
const TodayActionAudit = "today.action"

// AuditEventDetail is an audit row with its detail blob.
type AuditEventDetail struct {
	AuditEvent
	Detail string `json:"detail,omitempty"`
}

// AppendAuditEvent appends one audit row and returns its id.
func (s *Store) AppendAuditEvent(kind, targetID, actor, detailJSON string) (int64, error) {
	kind, targetID = strings.TrimSpace(kind), strings.TrimSpace(targetID)
	if kind == "" || targetID == "" {
		return 0, fmt.Errorf("%w: audit kind and target are required", ErrWorkInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`INSERT INTO audit_events (kind,target_id,actor,at,detail_json) VALUES (?,?,?,?,?)`,
		kind, targetID, actor, s.unixNow(), detailJSON)
	if err != nil {
		return 0, fmt.Errorf("jobstore: append audit event: %w", err)
	}
	return res.LastInsertId()
}

// ListAuditEventsSince returns the rows of one kind at or after since, newest first,
// capped at limit (<= 0 means 500).
func (s *Store) ListAuditEventsSince(kind string, since int64, limit int) ([]AuditEventDetail, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT id, kind, target_id, actor, at, detail_json FROM audit_events
  WHERE kind=? AND at>=? ORDER BY at DESC, id DESC LIMIT ?`, kind, since, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list audit events since: %w", err)
	}
	defer rows.Close()
	out := make([]AuditEventDetail, 0)
	for rows.Next() {
		var e AuditEventDetail
		if err := rows.Scan(&e.ID, &e.Kind, &e.TargetID, &e.Actor, &e.At, &e.Detail); err != nil {
			return nil, fmt.Errorf("jobstore: scan audit event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListPendingWorkSuggestionsFor returns every pending suggestion for the given fields
// across all open (not closed, not merged-away) work items, oldest first.
func (s *Store) ListPendingWorkSuggestionsFor(fields []string) ([]WorkSuggestion, error) {
	if len(fields) == 0 {
		return []WorkSuggestion{}, nil
	}
	ph := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields))
	for _, f := range fields {
		ph = append(ph, "?")
		args = append(args, f)
	}
	rows, err := s.db.Query(`SELECT g.work_item_id, g.field, g.value, g.confidence, g.by, g.at, g.state, g.job_id
  FROM work_suggestions g JOIN work_items w ON w.id = g.work_item_id
  WHERE g.state = 'pending' AND g.field IN (`+strings.Join(ph, ",")+`)
    AND w.status NOT IN ('done','dropped') AND COALESCE(w.merged_into,'') = ''
  ORDER BY g.at ASC, g.work_item_id ASC, g.field ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list pending work suggestions: %w", err)
	}
	defer rows.Close()
	out := make([]WorkSuggestion, 0)
	for rows.Next() {
		var sg WorkSuggestion
		if err := rows.Scan(&sg.WorkItemID, &sg.Field, &sg.Value, &sg.Confidence, &sg.By, &sg.At, &sg.State, &sg.JobID); err != nil {
			return nil, fmt.Errorf("jobstore: scan pending work suggestion: %w", err)
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

// JobOutcomeCounts is the 「自上次打开」 line: jobs that ended since a moment, split into
// done and failed (failed + timeout), and the commits the ended jobs delivered.
type JobOutcomeCounts struct {
	Done    int
	Failed  int
	Commits int
}

// JobOutcomesSince counts the jobs that ended at or after since.
func (s *Store) JobOutcomesSince(since int64) (JobOutcomeCounts, error) {
	var out JobOutcomeCounts
	err := s.db.QueryRow(`SELECT
  COALESCE(SUM(CASE WHEN status='done' THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN status IN ('failed','timeout') THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN json_valid(commits_json) AND json_type(commits_json)='array' THEN json_array_length(commits_json) ELSE 0 END),0)
  FROM jobs WHERE COALESCE(ended_at,0) >= ? AND COALESCE(ended_at,0) > 0`, since).Scan(&out.Done, &out.Failed, &out.Commits)
	if err != nil {
		return JobOutcomeCounts{}, fmt.Errorf("jobstore: job outcomes since: %w", err)
	}
	return out, nil
}
