package jobstore

import (
	"fmt"
	"time"
)

// InboxItemState is caller-local UI state; it never changes a source item.
type InboxItemState struct {
	CallerID       string
	ItemKey        string
	Column         string
	SourceRevision string
	SeenAt         *int64
	ClearedAt      *int64
	RestoredAt     *int64
	UpdatedAt      int64
}

func (s *Store) ListInboxItemStates(callerID string) (map[string]InboxItemState, error) {
	rows, err := s.db.Query(`SELECT caller_id,item_key,column_name,source_revision,seen_at,cleared_at,restored_at,updated_at FROM inbox_item_state WHERE caller_id=?`, callerID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list inbox state: %w", err)
	}
	defer rows.Close()
	out := map[string]InboxItemState{}
	for rows.Next() {
		var v InboxItemState
		if err := rows.Scan(&v.CallerID, &v.ItemKey, &v.Column, &v.SourceRevision, &v.SeenAt, &v.ClearedAt, &v.RestoredAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("jobstore: scan inbox state: %w", err)
		}
		out[v.ItemKey] = v
	}
	return out, rows.Err()
}

// SetInboxItemState applies an idempotent caller-local action.
func (s *Store) SetInboxItemState(callerID, key, column, revision, action string, now time.Time) error {
	if callerID == "" || key == "" {
		return fmt.Errorf("jobstore: inbox state requires caller and item")
	}
	ts := now.Unix()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO inbox_item_state(caller_id,item_key,column_name,source_revision,seen_at,cleared_at,restored_at,updated_at)
VALUES(?,?,?,?,NULL,NULL,NULL,?) ON CONFLICT(caller_id,item_key) DO UPDATE SET column_name=excluded.column_name, source_revision=excluded.source_revision, updated_at=excluded.updated_at`, callerID, key, column, revision, ts)
	if err != nil {
		return fmt.Errorf("jobstore: upsert inbox state: %w", err)
	}
	var q string
	switch action {
	case "seen":
		q = `UPDATE inbox_item_state SET seen_at=?, updated_at=? WHERE caller_id=? AND item_key=?`
	case "clear":
		q = `UPDATE inbox_item_state SET seen_at=COALESCE(seen_at,?), cleared_at=?, updated_at=? WHERE caller_id=? AND item_key=?`
	case "restore":
		q = `UPDATE inbox_item_state SET restored_at=?, cleared_at=NULL, updated_at=? WHERE caller_id=? AND item_key=?`
	default:
		return fmt.Errorf("jobstore: unknown inbox state action %q", action)
	}
	var args []any
	switch action {
	case "seen":
		args = []any{ts, ts, callerID, key}
	case "clear":
		args = []any{ts, ts, ts, callerID, key}
	case "restore":
		args = []any{ts, ts, callerID, key}
	}
	if _, err := s.db.Exec(q, args...); err != nil {
		return fmt.Errorf("jobstore: set inbox state: %w", err)
	}
	return nil
}

func (s *Store) ClearInboxItemStates(callerID, column string, now time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	q := `UPDATE inbox_item_state SET seen_at=COALESCE(seen_at,?), cleared_at=?, updated_at=? WHERE caller_id=? AND cleared_at IS NULL`
	args := []any{now.Unix(), now.Unix(), now.Unix(), callerID}
	if column != "" {
		q += ` AND column_name=?`
		args = append(args, column)
	}
	if _, err := s.db.Exec(q, args...); err != nil {
		return fmt.Errorf("jobstore: clear inbox states: %w", err)
	}
	return nil
}
