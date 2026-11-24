package jobstore

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ScheduleTargetSeatPrefix starts the Target of a schedule that delivers its text to a seat
// instead of submitting a job.
const ScheduleTargetSeatPrefix = "seat:"

// ScheduleTargetSeat builds the Target that delivers to the seat.
func ScheduleTargetSeat(seatID string) string { return ScheduleTargetSeatPrefix + seatID }

// SeatOfScheduleTarget returns the seat id of a seat target; ok is false for "" (a job
// schedule) and for any other target.
func SeatOfScheduleTarget(target string) (seatID string, ok bool) {
	id, found := strings.CutPrefix(strings.TrimSpace(target), ScheduleTargetSeatPrefix)
	id = strings.TrimSpace(id)
	return id, found && id != ""
}

// How the last delivery of a seat schedule ended (schedules.last_result). Each value may be
// followed by ":<detail>" (the job id of a restart, the reason of a failure).
const (
	// ScheduleResultDelivered: the text went to the sitting terminal session or the live ACP job.
	ScheduleResultDelivered = "delivered"
	// ScheduleResultRestarted: the ACP job had ended; the seat was restarted with the text as
	// its first turn (detail = the new job id).
	ScheduleResultRestarted = "restarted"
	// ScheduleResultOffline: nobody could take the text (no session, or it is offline / ended);
	// a card in 「等我」 asks the person to open the seat.
	ScheduleResultOffline = "offline"
	// ScheduleResultFailed: the delivery was tried and refused (detail = why).
	ScheduleResultFailed = "failed"
)

// Paused reasons of a seat schedule whose seat went away (schedules.paused_reason).
const (
	SchedulePausedSeatArchived = "seat_archived"
	SchedulePausedSeatDeleted  = "seat_deleted"
)

// SetScheduleResult records how the last seat delivery of a schedule ended.
func (s *Store) SetScheduleResult(id, result string) error {
	defer s.emit(Change{Kind: ChangeSchedule})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE schedules SET last_result = ?, updated_at = ? WHERE id = ?`,
		result, time.Now().Unix(), id,
	); err != nil {
		return fmt.Errorf("jobstore: set schedule %q result: %w", id, err)
	}
	return nil
}

// ListSeatSchedules lists the schedules that deliver to the seat, newest first.
func (s *Store) ListSeatSchedules(seatID string) ([]ScheduleRecord, error) {
	return s.listSchedules(selectScheduleCols+` WHERE target = ? ORDER BY created_at DESC, id DESC`, ScheduleTargetSeat(seatID))
}

// pauseSeatSchedulesTx disables the enabled schedules that deliver to the seat and keeps
// the reason on each row; nothing is deleted.
func pauseSeatSchedulesTx(tx *sql.Tx, seatID, reason string, now int64) error {
	_, err := tx.Exec(
		`UPDATE schedules SET enabled = 0, paused_reason = ?, updated_at = ? WHERE target = ? AND enabled = 1`,
		reason, now, ScheduleTargetSeat(seatID),
	)
	return err
}
