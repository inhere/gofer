package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// JobEvent is the SQLite-persisted projection of one append-only lifecycle event
// (E13, design §5.1). Like JobRecord/InteractionRecord it is a neutral struct,
// decoupled from internal/job, so the job_events table can be populated without
// this package importing job (which would form a job -> jobstore -> job cycle).
//
// Detail holds the marshalled detail_json original (already a JSON string); an
// empty string means "no detail" (the column stays NULL/empty). Seq is the
// monotonic global insertion order assigned by SQLite (AUTOINCREMENT) and used
// as the SSE/poll cursor; it is 0 on a value being inserted and set on read.
type JobEvent struct {
	Seq    int64  `json:"seq"`
	JobID  string `json:"job_id"`
	Type   string `json:"type"`
	Detail string `json:"detail,omitempty"`
	At     int64  `json:"at"`
}

// selectEventCols is the shared projection for ListJobEvents. COALESCE guards the
// nullable detail_json column so a NULL scans into "" instead of failing the scan.
const selectEventCols = `SELECT seq, job_id, type, COALESCE(detail_json,''), at
  FROM job_events`

// scanEvent reads one row (in selectEventCols order) into a JobEvent.
func scanEvent(sc rowScanner) (JobEvent, error) {
	var e JobEvent
	err := sc.Scan(&e.Seq, &e.JobID, &e.Type, &e.Detail, &e.At)
	return e, err
}

// InsertJobEvent appends one lifecycle event row (append-only — INSERT only, never
// update/upsert: the stream is the immutable history). It returns the assigned
// seq (lastInsertId), which P2 (notify dispatch) uses as the queue cursor. Writes
// go through s.writeMu (like UpsertJob/UpsertInteraction) so SQLite never sees two
// concurrent writers and cannot return SQLITE_BUSY.
func (s *Store) InsertJobEvent(e JobEvent) (int64, error) {
	if e.JobID == "" {
		return 0, errors.New("jobstore: InsertJobEvent: empty job id")
	}
	if e.Type == "" {
		return 0, errors.New("jobstore: InsertJobEvent: empty event type")
	}
	const q = `INSERT INTO job_events (job_id, type, detail_json, at) VALUES (?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// detail_json stays NULL when Detail is empty (keeps the column truly optional).
	var detail any
	if e.Detail != "" {
		detail = e.Detail
	}
	res, err := s.db.Exec(q, e.JobID, e.Type, detail, e.At)
	if err != nil {
		return 0, fmt.Errorf("jobstore: insert job event %q/%q: %w", e.JobID, e.Type, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("jobstore: insert job event %q/%q last id: %w", e.JobID, e.Type, err)
	}
	return seq, nil
}

// GetEvent returns the single event with the given seq. The boolean is false when
// no such event exists (e.g. a pruned job's event). It is used by the E14
// delivery sweeper to rebuild a webhook body (type/detail/at) for a queued
// delivery that only stores the event_seq.
func (s *Store) GetEvent(seq int64) (JobEvent, bool, error) {
	row := s.db.QueryRow(selectEventCols+" WHERE seq = ?", seq)
	ev, err := scanEvent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return JobEvent{}, false, nil
		}
		return JobEvent{}, false, fmt.Errorf("jobstore: get event %d: %w", seq, err)
	}
	return ev, true, nil
}

// ListJobEvents returns a job's events in insertion order (seq ASC). When
// sinceSeq > 0 only events strictly after it are returned (the incremental cursor
// for SSE/poll). A job with no events yields an empty slice and no error.
func (s *Store) ListJobEvents(jobID string, sinceSeq int64) ([]JobEvent, error) {
	q := selectEventCols + " WHERE job_id = ?"
	args := []any{jobID}
	if sinceSeq > 0 {
		q += " AND seq > ?"
		args = append(args, sinceSeq)
	}
	q += " ORDER BY seq ASC"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list job events %q: %w", jobID, err)
	}
	defer rows.Close()

	out := make([]JobEvent, 0)
	for rows.Next() {
		ev, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan job event row: %w", scanErr)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list job events %q rows: %w", jobID, err)
	}
	return out, nil
}
