package jobstore

import (
	"database/sql"
	"fmt"
	"strings"
)

// WaitingNotice is one stay of a subject in the 「等我」 list and what was told about it
// (table waiting_notices). Timestamps are unix seconds; 0 = not yet.
type WaitingNotice struct {
	ID int64
	// CallerID is the owner the notice went to.
	CallerID string
	// SubjectKey is the 「等我」 subject key, e.g. "session:<id>" or "job:<id>".
	SubjectKey string
	Category   string
	EnteredAt  int64
	NotifiedAt int64
	RemindedAt int64
	ClearedAt  int64
}

const selectWaitingNoticeCols = `SELECT id, caller_id, subject_key, category, entered_at, COALESCE(notified_at,0),
  COALESCE(reminded_at,0), COALESCE(cleared_at,0) FROM waiting_notices`

// ListOpenWaitingNotices returns the rows that have not been cleared, oldest first.
func (s *Store) ListOpenWaitingNotices() ([]WaitingNotice, error) {
	rows, err := s.db.Query(selectWaitingNoticeCols + ` WHERE cleared_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list waiting notices: %w", err)
	}
	defer rows.Close()
	var out []WaitingNotice
	for rows.Next() {
		var n WaitingNotice
		if err := rows.Scan(&n.ID, &n.CallerID, &n.SubjectKey, &n.Category, &n.EnteredAt, &n.NotifiedAt, &n.RemindedAt, &n.ClearedAt); err != nil {
			return nil, fmt.Errorf("jobstore: scan waiting notice: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// EnterWaitingNotice opens a row for a subject that entered the list. A subject that already
// has an open row gets that row back unchanged (existed = true), so entering twice is one stay.
func (s *Store) EnterWaitingNotice(caller, subjectKey, category string, at int64) (n WaitingNotice, existed bool, err error) {
	subjectKey = strings.TrimSpace(subjectKey)
	if subjectKey == "" {
		return WaitingNotice{}, false, fmt.Errorf("jobstore: waiting notice needs a subject key")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row := s.db.QueryRow(selectWaitingNoticeCols+` WHERE subject_key = ? AND cleared_at IS NULL`, subjectKey)
	switch err := row.Scan(&n.ID, &n.CallerID, &n.SubjectKey, &n.Category, &n.EnteredAt, &n.NotifiedAt, &n.RemindedAt, &n.ClearedAt); err {
	case nil:
		return n, true, nil
	case sql.ErrNoRows:
	default:
		return WaitingNotice{}, false, fmt.Errorf("jobstore: read waiting notice: %w", err)
	}
	res, err := s.db.Exec(`INSERT INTO waiting_notices (caller_id, subject_key, category, entered_at) VALUES (?,?,?,?)`,
		caller, subjectKey, category, at)
	if err != nil {
		return WaitingNotice{}, false, fmt.Errorf("jobstore: insert waiting notice: %w", err)
	}
	id, _ := res.LastInsertId()
	return WaitingNotice{ID: id, CallerID: caller, SubjectKey: subjectKey, Category: category, EnteredAt: at}, false, nil
}

// MarkWaitingNotified stamps the first notice of a stay.
func (s *Store) MarkWaitingNotified(id, at int64) error {
	return s.stampWaitingNotice(`UPDATE waiting_notices SET notified_at = ? WHERE id = ?`, at, id)
}

// MarkWaitingReminded stamps the one reminder of a stay.
func (s *Store) MarkWaitingReminded(id, at int64) error {
	return s.stampWaitingNotice(`UPDATE waiting_notices SET reminded_at = ? WHERE id = ?`, at, id)
}

// ClearWaitingNotice closes the stay of a subject that left the list.
func (s *Store) ClearWaitingNotice(id, at int64) error {
	return s.stampWaitingNotice(`UPDATE waiting_notices SET cleared_at = ? WHERE id = ? AND cleared_at IS NULL`, at, id)
}

func (s *Store) stampWaitingNotice(q string, at, id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, at, id); err != nil {
		return fmt.Errorf("jobstore: update waiting notice: %w", err)
	}
	return nil
}

// waitingNoticesInitKey marks that the waiting-notice scan has run once on this database.
const waitingNoticesInitKey = "waiting_notices_initialized"

// WaitingNoticesInitialized reports whether the first scan has already recorded the subjects
// that were waiting when the table came into use.
func (s *Store) WaitingNoticesInitialized() (bool, error) {
	v, err := s.GetWorkKV(waitingNoticesInitKey)
	return v != "", err
}

// MarkWaitingNoticesInitialized sets the mark once; later calls keep it.
func (s *Store) MarkWaitingNoticesInitialized(at int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`INSERT INTO work_kv(k, v) VALUES (?,?) ON CONFLICT(k) DO NOTHING`,
		waitingNoticesInitKey, fmt.Sprint(at)); err != nil {
		return fmt.Errorf("jobstore: mark waiting notices initialized: %w", err)
	}
	return nil
}
