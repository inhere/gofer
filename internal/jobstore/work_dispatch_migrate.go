package jobstore

import "fmt"

// migrateCapturedDispatchFinalizes converts the old capture dispatch outcome (parked)
// to the final outcome (done), but only when both the link and dispatch journal prove the
// item left the capture box through issue/todo. The marker makes this one-time migration cheap.
//
// DEPRECATED(v0.200): remove in v0.203.
func (s *Store) migrateCapturedDispatchFinalizes() error {
	const marker = "migrated_captured_dispatch_finalizes"
	if v, err := s.GetWorkKV(marker); err != nil || v != "" {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: begin captured dispatch migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.unixNow()
	_, err = tx.Exec(`UPDATE work_items
SET status='done', status_source='human', closed_at=?, status_at=?, updated_at=?, last_activity_at=?
WHERE status='parked' AND EXISTS (
  SELECT 1 FROM work_links l WHERE l.work_item_id=work_items.id AND l.kind IN ('issue','todo')
) AND EXISTS (
  SELECT 1 FROM work_journal j WHERE j.work_item_id=work_items.id
    AND (j.text LIKE '已转为 issue %' OR j.text LIKE '已转为 todo %'
      OR j.text LIKE 'Filed as issue %' OR j.text LIKE 'Filed as todo %')
)`, now, now, now, now)
	if err != nil {
		return fmt.Errorf("jobstore: migrate captured dispatch rows: %w", err)
	}
	if _, err = tx.Exec(`INSERT INTO work_kv(k, v) VALUES (?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, marker, "1"); err != nil {
		return fmt.Errorf("jobstore: mark captured dispatch migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("jobstore: commit captured dispatch migration: %w", err)
	}
	return nil
}
