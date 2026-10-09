package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// TodaySnooze is one today_snooze row (N3 T3 「稍后」, design §2.3). A row is ACTIVE while
// WokeAt is 0; once a wake rule fired (time / job / activity) it stays as a marker so the
// card returns flagged 「有新动静」 until it is handled or the marker ages out.
type TodaySnooze struct {
	CardKey    string `json:"card_key"`
	UntilAt    int64  `json:"until_at,omitempty"`
	UntilJobID string `json:"until_job_id,omitempty"`
	ActivityAt int64  `json:"activity_at"`
	CreatedAt  int64  `json:"created_at"`
	WokeAt     int64  `json:"woke_at,omitempty"`
	WokeReason string `json:"woke_reason,omitempty"`
}

// UpsertTodaySnooze snoozes (or re-snoozes) one card; a re-snooze clears the wake marker.
// CreatedAt is stamped by the store.
func (s *Store) UpsertTodaySnooze(in TodaySnooze) (TodaySnooze, error) {
	in.CardKey, in.UntilJobID = strings.TrimSpace(in.CardKey), strings.TrimSpace(in.UntilJobID)
	if in.CardKey == "" {
		return TodaySnooze{}, fmt.Errorf("%w: snooze card key is required", ErrWorkInvalid)
	}
	in.CreatedAt, in.WokeAt, in.WokeReason = s.unixNow(), 0, ""
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO today_snooze (card_key,until_at,until_job_id,activity_at,created_at,woke_at,woke_reason)
  VALUES (?,?,?,?,?,0,'')
  ON CONFLICT(card_key) DO UPDATE SET until_at=excluded.until_at, until_job_id=excluded.until_job_id,
    activity_at=excluded.activity_at, created_at=excluded.created_at, woke_at=0, woke_reason=''`,
		in.CardKey, in.UntilAt, in.UntilJobID, in.ActivityAt, in.CreatedAt)
	if err != nil {
		return TodaySnooze{}, fmt.Errorf("jobstore: upsert today snooze: %w", err)
	}
	return in, nil
}

// GetTodaySnooze reads one row; ok is false when the card is not snoozed.
func (s *Store) GetTodaySnooze(cardKey string) (TodaySnooze, bool, error) {
	var r TodaySnooze
	err := s.db.QueryRow(`SELECT card_key,until_at,until_job_id,activity_at,created_at,woke_at,woke_reason
  FROM today_snooze WHERE card_key=?`, strings.TrimSpace(cardKey)).
		Scan(&r.CardKey, &r.UntilAt, &r.UntilJobID, &r.ActivityAt, &r.CreatedAt, &r.WokeAt, &r.WokeReason)
	if errors.Is(err, sql.ErrNoRows) {
		return TodaySnooze{}, false, nil
	}
	if err != nil {
		return TodaySnooze{}, false, fmt.Errorf("jobstore: get today snooze: %w", err)
	}
	return r, true, nil
}

// ListTodaySnoozes returns every row (active and woken), oldest first.
func (s *Store) ListTodaySnoozes() ([]TodaySnooze, error) {
	rows, err := s.db.Query(`SELECT card_key,until_at,until_job_id,activity_at,created_at,woke_at,woke_reason
  FROM today_snooze ORDER BY created_at ASC, card_key ASC`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list today snoozes: %w", err)
	}
	defer rows.Close()
	out := make([]TodaySnooze, 0)
	for rows.Next() {
		var r TodaySnooze
		if err := rows.Scan(&r.CardKey, &r.UntilAt, &r.UntilJobID, &r.ActivityAt, &r.CreatedAt, &r.WokeAt, &r.WokeReason); err != nil {
			return nil, fmt.Errorf("jobstore: scan today snooze: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WakeTodaySnooze marks an active row as woken (reason: time / job / activity). It is a
// no-op on a row that already woke or is gone, so concurrent readers agree on one wake.
func (s *Store) WakeTodaySnooze(cardKey, reason string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE today_snooze SET woke_at=?, woke_reason=? WHERE card_key=? AND woke_at=0`,
		s.unixNow(), reason, cardKey); err != nil {
		return fmt.Errorf("jobstore: wake today snooze: %w", err)
	}
	return nil
}

// DeleteTodaySnoozes removes rows by card key and returns how many went.
func (s *Store) DeleteTodaySnoozes(cardKeys ...string) (int, error) {
	if len(cardKeys) == 0 {
		return 0, nil
	}
	ph := make([]string, 0, len(cardKeys))
	args := make([]any, 0, len(cardKeys))
	for _, k := range cardKeys {
		ph = append(ph, "?")
		args = append(args, strings.TrimSpace(k))
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM today_snooze WHERE card_key IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("jobstore: delete today snoozes: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
