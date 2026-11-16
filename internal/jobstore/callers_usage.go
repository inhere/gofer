package jobstore

import "fmt"

// migrateCallerUsage adds the token lifetime columns of auth_callers (design 2026-11-14 §4):
// expires_at (NULL / 0 = never) and last_used_at. Both are additive; the CREATE TABLE of
// the base schema stays as it was (G057), so a database from before the columns is
// upgraded here. An older binary simply ignores them.
func (s *Store) migrateCallerUsage() error {
	cols, err := s.tableColumns("auth_callers")
	if err != nil {
		return err
	}
	for _, c := range []struct{ name, ddl string }{
		{"expires_at", "expires_at INTEGER"},
		{"last_used_at", "last_used_at INTEGER"},
	} {
		if _, ok := cols[c.name]; ok {
			continue
		}
		if _, err := s.db.Exec("ALTER TABLE auth_callers ADD COLUMN " + c.ddl); err != nil {
			return fmt.Errorf("jobstore: migrate auth_callers add %s: %w", c.name, err)
		}
	}
	return nil
}

// TouchCallersUsed raises last_used_at of the listed stored callers (caller id -> unix
// seconds) in one transaction. It never lowers a stamp, and an id that has no row is
// skipped. This is the only writer of last_used_at, so a concurrent caller edit
// (UpdateCallerTx) cannot lose or resurrect it.
func (s *Store) TouchCallersUsed(used map[string]int64) error {
	if len(used) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: begin touch callers: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for id, at := range used {
		if _, err := tx.Exec(`UPDATE auth_callers SET last_used_at=? WHERE id=? AND COALESCE(last_used_at,0) < ?`, at, id, at); err != nil {
			return fmt.Errorf("jobstore: touch caller %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("jobstore: commit touch callers: %w", err)
	}
	return nil
}
