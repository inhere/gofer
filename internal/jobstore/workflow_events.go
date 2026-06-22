package jobstore

import (
	"fmt"
)

// WorkflowEvent is the SQLite-persisted projection of one append-only workflow
// lifecycle event (P1, design §5.4), the workflow analogue of JobEvent. Like every
// type here it is a neutral struct decoupled from internal/job so the
// workflow_events table can be populated without forming a job -> jobstore -> job
// import cycle.
//
// Detail holds the marshalled detail_json original (already a JSON string); an
// empty string means "no detail" (the column stays NULL). Seq is the monotonic
// global insertion order assigned by SQLite (AUTOINCREMENT), used as the poll
// cursor; it is 0 on a value being inserted and set on read.
type WorkflowEvent struct {
	Seq        int64  `json:"seq"`
	WorkflowID string `json:"workflow_id"`
	Type       string `json:"type"`
	Detail     string `json:"detail,omitempty"`
	At         int64  `json:"at"`
}

// selectWorkflowEventCols is the shared projection for ListWorkflowEvents. COALESCE
// guards the nullable detail_json column so a NULL scans into "" instead of failing.
const selectWorkflowEventCols = `SELECT seq, workflow_id, type, COALESCE(detail_json,''), at
  FROM workflow_events`

// scanWorkflowEvent reads one row (in selectWorkflowEventCols order) into a WorkflowEvent.
func scanWorkflowEvent(sc rowScanner) (WorkflowEvent, error) {
	var e WorkflowEvent
	err := sc.Scan(&e.Seq, &e.WorkflowID, &e.Type, &e.Detail, &e.At)
	return e, err
}

// InsertWorkflowEvent appends one workflow lifecycle event row (append-only —
// INSERT only, never update/upsert: the stream is the immutable history). It
// returns the assigned seq (lastInsertId). Writes go through s.writeMu (like every
// other writer) so SQLite never sees two concurrent writers and cannot return
// SQLITE_BUSY.
func (s *Store) InsertWorkflowEvent(e WorkflowEvent) (int64, error) {
	if e.WorkflowID == "" {
		return 0, fmt.Errorf("jobstore: InsertWorkflowEvent: empty workflow id")
	}
	if e.Type == "" {
		return 0, fmt.Errorf("jobstore: InsertWorkflowEvent: empty event type")
	}
	const q = `INSERT INTO workflow_events (workflow_id, type, detail_json, at) VALUES (?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// detail_json stays NULL when Detail is empty (keeps the column truly optional).
	var detail any
	if e.Detail != "" {
		detail = e.Detail
	}
	res, err := s.db.Exec(q, e.WorkflowID, e.Type, detail, e.At)
	if err != nil {
		return 0, fmt.Errorf("jobstore: insert workflow event %q/%q: %w", e.WorkflowID, e.Type, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("jobstore: insert workflow event %q/%q last id: %w", e.WorkflowID, e.Type, err)
	}
	return seq, nil
}

// ListWorkflowEvents returns a workflow's events in insertion order (seq ASC). When
// sinceSeq > 0 only events strictly after it are returned (the incremental cursor
// for poll). A workflow with no events yields an empty slice and no error.
func (s *Store) ListWorkflowEvents(wfID string, sinceSeq int64) ([]WorkflowEvent, error) {
	q := selectWorkflowEventCols + " WHERE workflow_id = ?"
	args := []any{wfID}
	if sinceSeq > 0 {
		q += " AND seq > ?"
		args = append(args, sinceSeq)
	}
	q += " ORDER BY seq ASC"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workflow events %q: %w", wfID, err)
	}
	defer rows.Close()

	out := make([]WorkflowEvent, 0)
	for rows.Next() {
		ev, scanErr := scanWorkflowEvent(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan workflow event row: %w", scanErr)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workflow events %q rows: %w", wfID, err)
	}
	return out, nil
}
