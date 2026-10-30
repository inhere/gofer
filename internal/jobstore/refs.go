package jobstore

import (
	"fmt"
	"time"

	"github.com/inhere/gofer/internal/refs"
)

// RefRecord is one reverse-link row. Source titles are resolved by the caller
// because the source objects live in different tables (jobs, plans, tracker, ...).
type RefRecord struct {
	SrcKind   string
	SrcID     string
	DstKind   string
	DstID     string
	Field     string
	CreatedAt int64
}

// ReplaceRefs atomically replaces all references extracted from one source field.
// It is safe to call with an empty list, which removes all previous references.
func (s *Store) ReplaceRefs(srcKind, srcID, field string, found []refs.Reference) error {
	if srcKind == "" || srcID == "" || field == "" {
		return fmt.Errorf("jobstore: replace refs: source kind, id and field are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: begin replace refs: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM refs WHERE src_kind=? AND src_id=? AND field=?`, srcKind, srcID, field); err != nil {
		return fmt.Errorf("jobstore: delete refs: %w", err)
	}
	now := time.Now().Unix()
	for _, ref := range found {
		if ref.Kind == "" || ref.ID == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO refs(src_kind,src_id,dst_kind,dst_id,field,created_at) VALUES(?,?,?,?,?,?)`,
			srcKind, srcID, string(ref.Kind), ref.ID, field, now); err != nil {
			return fmt.Errorf("jobstore: insert ref %s: %w", ref.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("jobstore: commit refs: %w", err)
	}
	return nil
}

// SyncTextRefs extracts and replaces references in a source field.
func (s *Store) SyncTextRefs(srcKind, srcID, field, text string) error {
	return s.ReplaceRefs(srcKind, srcID, field, refs.Extract(text))
}

// ListRefs returns sources that reference the destination, newest first.
func (s *Store) ListRefs(dstKind, dstID string) ([]RefRecord, error) {
	rows, err := s.db.Query(`SELECT src_kind,src_id,dst_kind,dst_id,field,created_at
FROM refs WHERE dst_kind=? AND dst_id=? ORDER BY created_at DESC,src_kind,src_id,field`, dstKind, dstID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list refs: %w", err)
	}
	defer rows.Close()
	out := make([]RefRecord, 0)
	for rows.Next() {
		var r RefRecord
		if err := rows.Scan(&r.SrcKind, &r.SrcID, &r.DstKind, &r.DstID, &r.Field, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("jobstore: scan ref: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list refs rows: %w", err)
	}
	return out, nil
}
