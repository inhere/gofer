package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// TunnelPresetRecord is one server-side forward preset (TUN-03: the preset store the
// CLI and the console share, replacing the machine-local tunnels.yaml as the source of
// truth). SpecsJSON holds the rule list as a JSON array of strings — the CLI's own
// spelling, one rule per entry — and stays an opaque string here so this package needs
// no import of internal/tunnel, exactly like schedules.request_json.
type TunnelPresetRecord struct {
	Name string
	// Worker is the worker a `tun forward -n` run reaches the target through.
	Worker string
	// SpecsJSON is `["1502:10.0.0.5:502", ...]`, already split (never one compound
	// comma-joined entry).
	SpecsJSON string
	Note      string
	// UpdatedAt / UpdatedBy are the write audit: unix SECONDS (like every other
	// timestamp in this store) and the authenticated caller that saved it.
	UpdatedAt int64
	UpdatedBy string
}

const selectTunnelPresetCols = `SELECT name, worker, specs_json, COALESCE(note,''), updated_at,
  COALESCE(updated_by,'') FROM tunnel_presets`

func scanTunnelPreset(sc rowScanner) (TunnelPresetRecord, error) {
	var r TunnelPresetRecord
	err := sc.Scan(&r.Name, &r.Worker, &r.SpecsJSON, &r.Note, &r.UpdatedAt, &r.UpdatedBy)
	return r, err
}

// UpsertTunnelPreset writes a preset, replacing the row of the same name. It is an
// upsert rather than an insert because whether a name may be replaced is the CLI's
// `--force` question (asked over the whole store, with a 409 when the answer is no),
// not a storage detail.
func (s *Store) UpsertTunnelPreset(rec TunnelPresetRecord) error {
	if rec.Name == "" {
		return errors.New("jobstore: UpsertTunnelPreset: empty preset name")
	}
	const q = `INSERT INTO tunnel_presets (name, worker, specs_json, note, updated_at, updated_by)
  VALUES (?,?,?,?,?,?)
  ON CONFLICT(name) DO UPDATE SET
    worker=excluded.worker, specs_json=excluded.specs_json, note=excluded.note,
    updated_at=excluded.updated_at, updated_by=excluded.updated_by`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, rec.Name, rec.Worker, rec.SpecsJSON, rec.Note, rec.UpdatedAt, rec.UpdatedBy); err != nil {
		return fmt.Errorf("jobstore: upsert tunnel preset %q: %w", rec.Name, err)
	}
	return nil
}

// GetTunnelPreset reads one preset; ok=false means the name is not stored.
func (s *Store) GetTunnelPreset(name string) (TunnelPresetRecord, bool, error) {
	r, err := scanTunnelPreset(s.db.QueryRow(selectTunnelPresetCols+" WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return TunnelPresetRecord{}, false, nil
	}
	if err != nil {
		return TunnelPresetRecord{}, false, fmt.Errorf("jobstore: get tunnel preset %q: %w", name, err)
	}
	return r, true, nil
}

// ListTunnelPresets returns every preset, name-ordered (the map-like local file had no
// order; a listing must be stable to be readable).
func (s *Store) ListTunnelPresets() ([]TunnelPresetRecord, error) {
	rows, err := s.db.Query(selectTunnelPresetCols + " ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("jobstore: list tunnel presets: %w", err)
	}
	defer rows.Close()
	out := make([]TunnelPresetRecord, 0)
	for rows.Next() {
		r, err := scanTunnelPreset(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan tunnel preset: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list tunnel presets: %w", err)
	}
	return out, nil
}

// ErrTunnelPresetNotFound is what DeleteTunnelPreset reports for a name that was never
// stored. It is a sentinel (not a bare string) so the HTTP layer can answer 404 while a
// real storage failure stays a 500 — the two are different promises to the caller.
var ErrTunnelPresetNotFound = errors.New("jobstore: tunnel preset not found")

// DeleteTunnelPreset removes a preset, reporting a name that was never stored.
func (s *Store) DeleteTunnelPreset(name string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM tunnel_presets WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("jobstore: delete tunnel preset %q: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("jobstore: delete tunnel preset %q: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %q", ErrTunnelPresetNotFound, name)
	}
	return nil
}
