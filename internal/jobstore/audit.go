package jobstore

import (
	"fmt"
)

// AuditEvent is one row of audit_events: who did what to which (now possibly gone)
// target, and when. It deliberately carries no titles or other content.
type AuditEvent struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	TargetID string `json:"target_id"`
	Actor    string `json:"actor,omitempty"`
	At       int64  `json:"at"`
}

// ListAuditEvents returns the audit rows of one kind (and, when targetID is set, one
// target), oldest first.
func (s *Store) ListAuditEvents(kind, targetID string) ([]AuditEvent, error) {
	q := `SELECT id, kind, target_id, actor, at FROM audit_events WHERE kind=?`
	args := []any{kind}
	if targetID != "" {
		q += ` AND target_id=?`
		args = append(args, targetID)
	}
	rows, err := s.db.Query(q+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list audit events: %w", err)
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.Kind, &e.TargetID, &e.Actor, &e.At); err != nil {
			return nil, fmt.Errorf("jobstore: scan audit event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// migrateWorkDeletedAudit moves the v0.122 work.deleted audit rows out of job_events
// (where job_id held a work item id) into audit_events. One transaction, so an interrupted
// run retries; a work_kv marker makes it a cheap no-op on every Open afterwards (job_events has no
// index on type, so we must not scan it each time).
//
// DEPRECATED(v0.123): remove in v0.126 (delete this function and its call in migrate;
// by then every database that ever held a v0.122 audit row has been opened by a newer build).
func (s *Store) migrateWorkDeletedAudit() error {
	const marker = "migrated_work_deleted_audit"
	if v, err := s.GetWorkKV(marker); err != nil || v != "" {
		return err // done already (or the probe failed: surface it)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: begin work.deleted audit migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO audit_events (kind,target_id,actor,at)
SELECT type, job_id, COALESCE(json_extract(detail_json,'$.actor'),''), at FROM job_events WHERE type=? ORDER BY seq`, WorkDeletedEvent); err != nil {
		return fmt.Errorf("jobstore: migrate work.deleted audit: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM job_events WHERE type=?`, WorkDeletedEvent); err != nil {
		return fmt.Errorf("jobstore: migrate work.deleted audit cleanup: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO work_kv(k, v) VALUES (?,?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, marker, "1"); err != nil {
		return fmt.Errorf("jobstore: mark work.deleted audit migration: %w", err)
	}
	return tx.Commit()
}
