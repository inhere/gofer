package jobstore

import (
	"database/sql"
	"fmt"
	"time"
)

// SeatPendingTTL is how long a queued seat-schedule text waits for its seat; older rows are
// dropped instead of being sent late.
const SeatPendingTTL = 24 * time.Hour

// seatPendingClaimTTL is how long a claim (a delivery in flight) holds a row before another
// sweep may take it over, for a claim whose owner died.
const seatPendingClaimTTL = 5 * time.Minute

// SeatPending is the queued text of one seat schedule that found nobody to take it. Count is
// how many due runs have been folded into it; Text is the latest one.
type SeatPending struct {
	ID           int64
	SeatID       string
	ScheduleID   string
	ScheduleName string
	Text         string
	// Origin is the ceiling owner of the delivery (the schedule's creator).
	Origin    string
	Count     int
	CreatedAt int64
	// UpdatedAt is when the latest run was due.
	UpdatedAt int64
}

const selectSeatPendingCols = `SELECT id, seat_id, schedule_id, schedule_name, text, origin, count, created_at, updated_at FROM seat_pending_messages`

func scanSeatPending(sc interface{ Scan(...any) error }) (SeatPending, error) {
	var p SeatPending
	err := sc.Scan(&p.ID, &p.SeatID, &p.ScheduleID, &p.ScheduleName, &p.Text, &p.Origin, &p.Count, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// EnqueueSeatPending queues the text of a due run for the seat. A live row of the same
// (seat, schedule) is updated (latest text, count + 1) instead of a second one; the new total
// count is returned.
func (s *Store) EnqueueSeatPending(seatID, scheduleID, name, text, origin string) (int, error) {
	now := s.unixNow()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`INSERT INTO seat_pending_messages
(seat_id, schedule_id, schedule_name, text, origin, count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 1, ?, ?)
ON CONFLICT(seat_id, schedule_id) WHERE delivered_at = 0 AND dropped_at = 0
DO UPDATE SET schedule_name = excluded.schedule_name, text = excluded.text, origin = excluded.origin,
  count = count + 1, updated_at = excluded.updated_at`,
		seatID, scheduleID, name, text, origin, now, now); err != nil {
		return 0, fmt.Errorf("jobstore: queue seat message: %w", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count FROM seat_pending_messages WHERE seat_id = ? AND schedule_id = ? AND delivered_at = 0 AND dropped_at = 0`,
		seatID, scheduleID).Scan(&n); err != nil {
		return 0, fmt.Errorf("jobstore: read queued seat message: %w", err)
	}
	return n, nil
}

// SeatPendingSeatIDs lists the seats that have a live queued text.
func (s *Store) SeatPendingSeatIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT seat_id FROM seat_pending_messages WHERE delivered_at = 0 AND dropped_at = 0 ORDER BY seat_id`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list seats with queued messages: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListSeatPending lists the live queued texts of a seat, oldest first.
func (s *Store) ListSeatPending(seatID string) ([]SeatPending, error) {
	rows, err := s.db.Query(selectSeatPendingCols+` WHERE seat_id = ? AND delivered_at = 0 AND dropped_at = 0 ORDER BY created_at, id`, seatID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list queued seat messages: %w", err)
	}
	defer rows.Close()
	var out []SeatPending
	for rows.Next() {
		p, err := scanSeatPending(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SeatPendingCount is how many due runs of the schedule wait in the queue (0 = nothing queued).
func (s *Store) SeatPendingCount(scheduleID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COALESCE(SUM(count), 0) FROM seat_pending_messages WHERE schedule_id = ? AND delivered_at = 0 AND dropped_at = 0`, scheduleID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("jobstore: count queued seat messages: %w", err)
	}
	return n, nil
}

// ClaimSeatPending takes the row for delivery; false means it is gone (delivered, dropped) or
// another sweep holds it. The claim is conditional on the row, so two sweeps never both win.
func (s *Store) ClaimSeatPending(id int64) (bool, error) {
	now := s.unixNow()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE seat_pending_messages SET claimed_at = ?
WHERE id = ? AND delivered_at = 0 AND dropped_at = 0 AND (claimed_at = 0 OR claimed_at <= ?)`,
		now, id, now-int64(seatPendingClaimTTL/time.Second))
	if err != nil {
		return false, fmt.Errorf("jobstore: claim queued seat message: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ReleaseSeatPending gives a claimed row back after a failed delivery; it stays queued.
func (s *Store) ReleaseSeatPending(id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE seat_pending_messages SET claimed_at = 0 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("jobstore: release queued seat message: %w", err)
	}
	return nil
}

// FinishSeatPending marks a claimed row delivered. When a later run was folded into the row
// while it was in flight (its updated_at moved on from the one that was sent), the row stays
// queued for that newer text with the sent runs taken off its count.
func (s *Store) FinishSeatPending(p SeatPending) error {
	now := s.unixNow()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE seat_pending_messages SET delivered_at = ?, claimed_at = 0
WHERE id = ? AND delivered_at = 0 AND dropped_at = 0 AND updated_at = ? AND count = ?`, now, p.ID, p.UpdatedAt, p.Count)
	if err != nil {
		return fmt.Errorf("jobstore: finish queued seat message: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := s.db.Exec(`UPDATE seat_pending_messages SET claimed_at = 0, count = MAX(count - ?, 1)
WHERE id = ? AND delivered_at = 0 AND dropped_at = 0`, p.Count, p.ID); err != nil {
		return fmt.Errorf("jobstore: requeue queued seat message: %w", err)
	}
	return nil
}

// DropSeatPendingOf drops the live queued text of one (seat, schedule): a delivery that went
// through directly supersedes it.
func (s *Store) DropSeatPendingOf(seatID, scheduleID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE seat_pending_messages SET dropped_at = ? WHERE seat_id = ? AND schedule_id = ? AND delivered_at = 0 AND dropped_at = 0`,
		s.unixNow(), seatID, scheduleID); err != nil {
		return fmt.Errorf("jobstore: drop queued seat message: %w", err)
	}
	return nil
}

// DropSeatPending drops every live queued text of the seat (it is gone or has nobody to ask).
func (s *Store) DropSeatPending(seatID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := dropSeatPendingTx(tx, seatID, s.unixNow()); err != nil {
		return err
	}
	return tx.Commit()
}

// DropExpiredSeatPending drops the live rows created more than SeatPendingTTL ago and returns
// how many.
func (s *Store) DropExpiredSeatPending() (int, error) {
	cutoff := s.unixNow() - int64(SeatPendingTTL/time.Second)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE seat_pending_messages SET dropped_at = ? WHERE delivered_at = 0 AND dropped_at = 0 AND created_at < ?`, s.unixNow(), cutoff)
	if err != nil {
		return 0, fmt.Errorf("jobstore: expire queued seat messages: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func dropSeatPendingTx(tx *sql.Tx, seatID string, now int64) error {
	_, err := tx.Exec(`UPDATE seat_pending_messages SET dropped_at = ? WHERE seat_id = ? AND delivered_at = 0 AND dropped_at = 0`, now, seatID)
	return err
}
