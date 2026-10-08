package jobstore

import (
	"encoding/json"
	"fmt"
	"strings"
)

// WorkDeletedEvent is the job_events type of the audit row DeleteWorkItem leaves behind
// (job_id holds the work item id; the detail carries only the actor, never the title).
const WorkDeletedEvent = "work.deleted"

// DeleteWorkItem permanently removes one FINISHED (done / dropped) work item and every
// row that hangs off it, in a single transaction: attached sessions, links, the journal,
// field sources, the request ledger, suggestions, the summarizer run log, pending merge
// suggestions that name it, and the steward events noted about it.
//
// Only a finished item may go: deleting something still open would silently throw away a
// live thread of work, and "dropped" is already the soft-delete step.
//
// merged_into: other items that were merged INTO this one keep existing (they are
// finished records of their own); their merged_into pointer is cleared so it does not
// dangle. Refusing instead would make a merge target undeletable until every source is
// gone, and a cleanup of "all dropped" would then need an ordering. The journal lines
// that were moved to this item at merge time are deleted with it.
//
// The audit row (job_events, type work.deleted) records the id and the actor only, the
// same shape as job.deleted: no title, goal or any other content survives.
func (s *Store) DeleteWorkItem(id, actor string) error {
	id = strings.TrimSpace(id)
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	if id == "" {
		return fmt.Errorf("%w: empty work item id", ErrWorkInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	w, ok, err := s.getWorkItemOn(s.db, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrWorkItemNotFound
	}
	if !WorkStatusFinal(w.Status) {
		return fmt.Errorf("%w: work item %s is %s; only done or dropped items can be deleted", ErrWorkInvalid, id, w.Status)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: delete work item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM work_item_sessions WHERE work_item_id=?`,
		`DELETE FROM work_links WHERE work_item_id=?`,
		`DELETE FROM work_journal WHERE work_item_id=?`,
		`DELETE FROM work_field_sources WHERE work_item_id=?`,
		`DELETE FROM work_requests WHERE work_item_id=?`,
		`DELETE FROM work_suggestions WHERE work_item_id=?`,
		`DELETE FROM work_summaries WHERE work_item_id=?`,
		`DELETE FROM work_merge_suggestions WHERE target_id=? OR source_id=?`,
		`DELETE FROM steward_events WHERE ref=? OR ref LIKE ?`,
		`UPDATE work_items SET merged_into='' WHERE merged_into=?`,
		`DELETE FROM work_items WHERE id=?`,
	} {
		args := []any{id}
		switch {
		case strings.Contains(stmt, "target_id"):
			args = []any{id, id}
		case strings.Contains(stmt, "ref LIKE"):
			args = []any{id, id + "|%"}
		}
		if _, err := tx.Exec(stmt, args...); err != nil {
			return fmt.Errorf("jobstore: delete work item %q: %w", id, err)
		}
	}
	detail, _ := json.Marshal(map[string]string{"actor": actor})
	if _, err := tx.Exec(`INSERT INTO job_events (job_id,type,detail_json,at) VALUES (?,?,?,?)`, id, WorkDeletedEvent, string(detail), s.unixNow()); err != nil {
		return fmt.Errorf("jobstore: delete work item audit %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("jobstore: delete work item commit: %w", err)
	}
	s.purgeWAL()
	return nil
}

// ListFinalWorkItemIDs returns the ids of every item in the given final status
// (done | dropped), merged sources included — the set a "delete all dropped" sweep
// removes.
func (s *Store) ListFinalWorkItemIDs(status string) ([]string, error) {
	if !WorkStatusFinal(status) {
		return nil, fmt.Errorf("%w: status must be done or dropped, got %q", ErrWorkInvalid, status)
	}
	rows, err := s.db.Query(`SELECT id FROM work_items WHERE status=? ORDER BY updated_at, id`, status)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list %s work items: %w", status, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
