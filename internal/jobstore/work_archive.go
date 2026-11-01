package jobstore

import "fmt"

// WorkArchivedAutoEvent is the audit_events kind of the row a one-shot "archive every
// auto-generated work item" leaves per archived item (target_id is the work item id; only
// the actor is recorded).
const WorkArchivedAutoEvent = "work.archived_auto"

// ListAutoArchivableWorkItems lists the open work items the one-shot auto archive may
// close (design 2026-11-01 §4.2): created by the old per-session auto draft
// (source=auto), still open, not in the capture box, not merged away, and never touched by
// a person — the status was never set by a person (status_source<>human) and neither the
// journal nor the field sources carry a human speaker. A merged-away or captured item is
// someone's decision already, so it is left alone.
func (s *Store) ListAutoArchivableWorkItems() ([]WorkItem, error) {
	rows, err := s.db.Query(selectWorkCols+` WHERE source = ? AND status NOT IN ('done','dropped','captured')
  AND merged_into = '' AND status_source <> ?
  AND NOT EXISTS (SELECT 1 FROM work_journal j WHERE j.work_item_id = work_items.id AND (j.by = 'human' OR j.by LIKE 'human:%'))
  AND NOT EXISTS (SELECT 1 FROM work_field_sources f WHERE f.work_item_id = work_items.id AND (f.by = 'human' OR f.by LIKE 'human:%'))
  ORDER BY created_at, id`, WorkOriginAuto, WorkSourceHuman)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list auto-archivable work items: %w", err)
	}
	defer rows.Close()
	out := make([]WorkItem, 0)
	for rows.Next() {
		w, err := scanWorkItem(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan work item: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
