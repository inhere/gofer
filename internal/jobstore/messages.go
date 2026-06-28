package jobstore

import (
	"errors"
	"fmt"
	"strings"
)

// Message statuses (E36, design §9). Kept as literals so this package stays
// decoupled from internal/presence (no presence -> jobstore -> presence cycle).
const (
	MessageUnread = "unread"
	MessageRead   = "read"
)

// MessageRecord is the SQLite-persisted projection of one inbox message (E36,
// design §9). Like JobRecord it is a neutral struct, decoupled from
// internal/presence. ToSpec records the original addressing of a fanned-out send
// (e.g. "role:reviewer"); for a direct send it is the same as ToAgent's id or "".
// ExpiresAt 0 means no TTL; ReadAt 0 means still unread. The nullable columns
// COALESCE to "" / 0 on read (see selectMessageCols).
type MessageRecord struct {
	ID        string
	ToAgent   string
	FromAgent string
	ToSpec    string
	Kind      string
	Body      string
	Ref       string
	Status    string
	CreatedAt int64
	ExpiresAt int64
	ReadAt    int64
}

// selectMessageCols is the shared projection for inbox reads. COALESCE guards the
// nullable columns so NULLs scan into "" / 0 instead of failing.
const selectMessageCols = `SELECT id, to_agent, from_agent, COALESCE(to_spec,''),
  kind, COALESCE(body,''), COALESCE(ref,''), status, created_at,
  COALESCE(expires_at,0), COALESCE(read_at,0)
  FROM messages`

// scanMessage reads one row (in selectMessageCols order) into a MessageRecord.
func scanMessage(sc rowScanner) (MessageRecord, error) {
	var r MessageRecord
	err := sc.Scan(
		&r.ID, &r.ToAgent, &r.FromAgent, &r.ToSpec,
		&r.Kind, &r.Body, &r.Ref, &r.Status, &r.CreatedAt,
		&r.ExpiresAt, &r.ReadAt,
	)
	return r, err
}

// InsertMessages persists a batch of inbox rows in a single transaction. A direct
// send is one record; a role:/broadcast fan-out is many (one per recipient) — all
// inserted atomically so a partial fan-out never reaches the DB. An empty batch is
// a no-op (nil error). Writes go through s.writeMu (like every other writer).
func (s *Store) InsertMessages(recs []MessageRecord) error {
	if len(recs) == 0 {
		return nil
	}
	for _, r := range recs {
		if r.ID == "" {
			return errors.New("jobstore: InsertMessages: empty message id")
		}
		if r.ToAgent == "" {
			return errors.New("jobstore: InsertMessages: empty to_agent")
		}
	}
	const q = `INSERT INTO messages
  (id, to_agent, from_agent, to_spec, kind, body, ref, status, created_at, expires_at, read_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: insert messages begin tx: %w", err)
	}
	for _, r := range recs {
		status := r.Status
		if status == "" {
			status = MessageUnread
		}
		if _, err := tx.Exec(q,
			r.ID, r.ToAgent, r.FromAgent, r.ToSpec, r.Kind, r.Body, r.Ref,
			status, r.CreatedAt, r.ExpiresAt, r.ReadAt,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("jobstore: insert message %q: %w", r.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("jobstore: insert messages commit: %w", err)
	}
	return nil
}

// ListInbox returns an agent's messages in creation order (created_at asc, id asc
// as a stable tiebreaker). When includeRead is false only unread rows are
// returned (the default inbox poll); when true the full history is returned. An
// agent with no messages yields an empty slice and no error.
func (s *Store) ListInbox(agentID string, includeRead bool) ([]MessageRecord, error) {
	q := selectMessageCols + " WHERE to_agent = ?"
	args := []any{agentID}
	if !includeRead {
		q += " AND status = ?"
		args = append(args, MessageUnread)
	}
	q += " ORDER BY created_at ASC, id ASC"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list inbox %q: %w", agentID, err)
	}
	defer rows.Close()

	out := make([]MessageRecord, 0)
	for rows.Next() {
		rec, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan message row: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list inbox %q rows: %w", agentID, err)
	}
	return out, nil
}

// MarkRead flips the given message ids to read and stamps read_at=ts. Ids already
// read are updated again harmlessly (idempotent). An empty id slice is a no-op.
func (s *Store) MarkRead(ids []string, ts int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	q := fmt.Sprintf("UPDATE messages SET status = ?, read_at = ? WHERE id IN (%s)", placeholders)
	args := make([]any, 0, len(ids)+2)
	args = append(args, MessageRead, ts)
	for _, id := range ids {
		args = append(args, id)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, args...); err != nil {
		return fmt.Errorf("jobstore: mark read: %w", err)
	}
	return nil
}

// PruneMessages deletes messages that are already read OR past their TTL
// (expires_at > 0 AND expires_at < now). It returns the number of rows removed.
// Driven by the serve sweeper (mirrors PrunePresence).
func (s *Store) PruneMessages(now int64) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		"DELETE FROM messages WHERE status = ? OR (expires_at > 0 AND expires_at < ?)",
		MessageRead, now,
	)
	if err != nil {
		return 0, fmt.Errorf("jobstore: prune messages: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: prune messages rows affected: %w", err)
	}
	return int(n), nil
}
