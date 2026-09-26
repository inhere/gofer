package jobstore

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/inhere/gofer/internal/rule"
)

// rules.go is the SQLite index of the rule library (JOB-06①, design §一.1): one row
// per <config-dir>/rules/<name>.md. The text itself never comes near the database —
// it is a config-adjacent asset the operator edits — and the row keeps only what
// `agent rule ls`, the binding resolver and the version report need.
//
// The direction is the same as the skill index next door: jobstore imports rule
// (the data layer implements the interface the service declares), never the reverse.
var _ rule.Repo = (*Store)(nil)

const selectRuleCols = `SELECT name, COALESCE(description,''), COALESCE(size,0), COALESCE(sha256,''),
  COALESCE(updated_at,0), COALESCE(updated_by,'')
  FROM rules`

// scanRule reads one row (in selectRuleCols order) into a rule.Rule.
func scanRule(sc rowScanner) (rule.Rule, error) {
	var r rule.Rule
	if err := sc.Scan(&r.Name, &r.Description, &r.Size, &r.SHA256, &r.UpdatedAt, &r.UpdatedBy); err != nil {
		return rule.Rule{}, err
	}
	return r, nil
}

// UpsertRule writes one index row, replacing an existing one of the same name
// (`rule set` on an existing name is a replace by design).
func (s *Store) UpsertRule(r rule.Rule) error {
	const q = `INSERT INTO rules (name, description, size, sha256, updated_at, updated_by)
  VALUES (?, ?, ?, ?, ?, ?)
  ON CONFLICT(name) DO UPDATE SET description=excluded.description, size=excluded.size,
    sha256=excluded.sha256, updated_at=excluded.updated_at, updated_by=excluded.updated_by`
	if _, err := s.db.Exec(q, r.Name, r.Description, r.Size, r.SHA256, r.UpdatedAt, r.UpdatedBy); err != nil {
		return fmt.Errorf("jobstore: upsert rule %q: %w", r.Name, err)
	}
	return nil
}

// GetRule reads one rule's index row. ok=false (nil error) means the library has no
// such rule.
func (s *Store) GetRule(name string) (rule.Rule, bool, error) {
	row := s.db.QueryRow(selectRuleCols+` WHERE name = ?`, name)
	r, err := scanRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return rule.Rule{}, false, nil
	}
	if err != nil {
		return rule.Rule{}, false, fmt.Errorf("jobstore: get rule %q: %w", name, err)
	}
	return r, true, nil
}

// ListRules returns every indexed rule, ordered by name so `rule ls` is stable.
func (s *Store) ListRules() ([]rule.Rule, error) {
	rows, err := s.db.Query(selectRuleCols + ` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list rules: %w", err)
	}
	defer rows.Close()
	var out []rule.Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: list rules: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list rules: %w", err)
	}
	return out, nil
}

// DeleteRule removes one index row. It reports no error for a name that is not
// indexed: the caller (rule.Store.Remove) already checked, and a concurrent delete
// is not a failure the operator needs to see.
func (s *Store) DeleteRule(name string) error {
	if _, err := s.db.Exec(`DELETE FROM rules WHERE name = ?`, name); err != nil {
		return fmt.Errorf("jobstore: delete rule %q: %w", name, err)
	}
	return nil
}
