package jobstore

import (
	"fmt"
	"strings"
)

// upgradeInFlightStatuses carry executable work. Human review is excluded;
// recovering worker/session work is included.
const upgradeInFlightStatuses = `('queued','running','awaiting_input','pending_interaction','waiting_dir','recovering')`

// CountUpgradeInFlight reads the durable job rows that still carry executable
// work. The caller may exclude only a separately verified source job ID.
func (s *Store) CountUpgradeInFlight(verifiedSourceJobID string) (int, error) {
	if verifiedSourceJobID == "" {
		return s.CountUpgradeInFlightExcluding(nil)
	}
	return s.CountUpgradeInFlightExcluding([]string{verifiedSourceJobID})
}

func (s *Store) CountUpgradeInFlightExcluding(verifiedIDs []string) (int, error) {
	where, args := upgradeInFlightWhere(verifiedIDs)
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs`+where, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("jobstore: count upgrade in-flight jobs: %w", err)
	}
	return count, nil
}

// UpgradeBlocker is one job row that keeps an upgrade drain waiting.
type UpgradeBlocker struct {
	ID     string
	Status string
	Runner string
}

// ListUpgradeInFlightExcluding returns up to limit of the rows the drain census
// counts (oldest first), so an operator can see what an upgrade is waiting for.
func (s *Store) ListUpgradeInFlightExcluding(verifiedIDs []string, limit int) ([]UpgradeBlocker, error) {
	if limit <= 0 {
		limit = 10
	}
	where, args := upgradeInFlightWhere(verifiedIDs)
	rows, err := s.db.Query(`SELECT id, status, runner FROM jobs`+where+` ORDER BY started_at, id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list upgrade in-flight jobs: %w", err)
	}
	defer rows.Close()
	out := make([]UpgradeBlocker, 0)
	for rows.Next() {
		var b UpgradeBlocker
		if err := rows.Scan(&b.ID, &b.Status, &b.Runner); err != nil {
			return nil, fmt.Errorf("jobstore: scan upgrade in-flight job: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list upgrade in-flight rows: %w", err)
	}
	return out, nil
}

func upgradeInFlightWhere(verifiedIDs []string) (string, []any) {
	where := ` WHERE status IN ` + upgradeInFlightStatuses
	ids := make([]any, 0, len(verifiedIDs))
	for _, id := range verifiedIDs {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		where += ` AND id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
	}
	return where, ids
}
