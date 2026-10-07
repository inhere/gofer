package jobstore

import (
	"fmt"
	"strings"
)

// CountUpgradeInFlight reads the durable job rows that still carry executable
// work. Human review is excluded; recovering worker/session work is included.
// The caller may exclude only a separately verified source job ID.
func (s *Store) CountUpgradeInFlight(verifiedSourceJobID string) (int, error) {
	if verifiedSourceJobID == "" {
		return s.CountUpgradeInFlightExcluding(nil)
	}
	return s.CountUpgradeInFlightExcluding([]string{verifiedSourceJobID})
}

func (s *Store) CountUpgradeInFlightExcluding(verifiedIDs []string) (int, error) {
	const statuses = `('queued','running','awaiting_input','pending_interaction','waiting_dir','recovering')`
	q := `SELECT COUNT(*) FROM jobs WHERE status IN ` + statuses
	args := []any{}
	if len(verifiedIDs) > 0 {
		q += ` AND id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(verifiedIDs)), ",") + `)`
		for _, id := range verifiedIDs {
			args = append(args, id)
		}
	}
	var count int
	if err := s.db.QueryRow(q, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("jobstore: count upgrade in-flight jobs: %w", err)
	}
	return count, nil
}
