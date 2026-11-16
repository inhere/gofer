package jobstore

import "fmt"

// healthProbeDDL is the storage self-check table (design 2026-11-14 §5): ONE row, so a
// write to it proves the database file and its directory accept writes without growing
// the store. It is created from migrate() (not from schemaStmts) so a database written
// before this table existed gains it on the next Open — and so nothing in the base
// schema ever references it (G057).
const healthProbeDDL = `CREATE TABLE IF NOT EXISTS health_probe (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  checked_at INTEGER NOT NULL,
  note TEXT NOT NULL DEFAULT ''
)`

// ensureHealthProbe creates the health_probe table (idempotent, every Open).
func (s *Store) ensureHealthProbe() error {
	if _, err := s.db.Exec(healthProbeDDL); err != nil {
		return fmt.Errorf("jobstore: migrate health probe: %w", err)
	}
	return nil
}

// HealthProbeWrite is the storage self-check probe: it upserts the single health_probe
// row, which proves the file is writable (a read-only database, a full disk or a broken
// WAL all fail here). now is the unix second stamped on the row.
func (s *Store) HealthProbeWrite(now int64) error {
	if _, err := s.db.Exec(`INSERT INTO health_probe (id, checked_at, note) VALUES (1, ?, '')
ON CONFLICT(id) DO UPDATE SET checked_at = excluded.checked_at`, now); err != nil {
		return fmt.Errorf("jobstore: health probe write: %w", err)
	}
	return nil
}

// HealthProbeAt reads back the stamp of the last successful probe (0 = never written).
func (s *Store) HealthProbeAt() (int64, error) {
	var at int64
	if err := s.db.QueryRow(`SELECT checked_at FROM health_probe WHERE id = 1`).Scan(&at); err != nil {
		return 0, fmt.Errorf("jobstore: health probe read: %w", err)
	}
	return at, nil
}
