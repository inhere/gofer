package jobstore

import (
	"errors"
	"fmt"
)

// statusAwaitingApproval is the hold-for-approval state (gofer-9b1b). The literal is
// kept local (like nonTerminalJobStatuses) so jobstore never imports job.
const statusAwaitingApproval = "awaiting_approval"

// HoldDecision is one compare-and-set transition of a held job's row: the row moves
// From -> To only while it is still in From, so an approve, a reject, a cancel and the
// expiry sweep racing on the same job resolve to exactly one winner.
type HoldDecision struct {
	ID string
	// From is the state the row must still be in; "" means awaiting_approval.
	From string
	To   string
	// HoldJSON replaces the row's hold record (the decision, who, when, note).
	HoldJSON string
	// Error is written to the error column ("" keeps the column as it is).
	Error string
	// At stamps updated_at, and ended_at when To is not queued (a decision that ends
	// the job without running it).
	At int64
}

// DecideHold applies d as a conditional UPDATE and reports whether this call won the
// transition (false: the row is gone or no longer in From — somebody else decided).
func (s *Store) DecideHold(d HoldDecision) (bool, error) {
	if d.ID == "" || d.To == "" {
		return false, errors.New("jobstore: DecideHold: empty job id or target status")
	}
	from := d.From
	if from == "" {
		from = statusAwaitingApproval
	}
	var ended any
	if d.To != "queued" {
		ended = d.At
	}
	s.writeMu.Lock()
	res, err := s.db.Exec(`UPDATE jobs SET status = ?, hold_json = ?, updated_at = ?,
  error = CASE WHEN ? <> '' THEN ? ELSE error END,
  ended_at = COALESCE(?, ended_at)
  WHERE id = ? AND status = ?`,
		d.To, d.HoldJSON, d.At, d.Error, d.Error, ended, d.ID, from)
	s.writeMu.Unlock()
	if err != nil {
		return false, fmt.Errorf("jobstore: decide hold %q: %w", d.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: decide hold %q rows: %w", d.ID, err)
	}
	if n == 1 {
		s.emit(Change{Kind: ChangeJob, ID: d.ID, Status: d.To})
	}
	return n == 1, nil
}

// ListDueHolds returns the awaiting_approval jobs whose hold expired at or before now,
// oldest deadline first, at most limit rows (<= 0 means DefaultListLimit). It is the
// read behind the expiry sweep, served by the partial idx_jobs_hold_due index.
func (s *Store) ListDueHolds(now int64, limit int) ([]JobRecord, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	rows, err := s.db.Query(selectCols+` WHERE status = ? AND hold_expires_at > 0 AND hold_expires_at <= ?
  ORDER BY hold_expires_at, id LIMIT ?`, statusAwaitingApproval, now, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list due holds: %w", err)
	}
	defer rows.Close()
	var out []JobRecord
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan due hold: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
