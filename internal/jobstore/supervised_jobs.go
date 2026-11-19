package jobstore

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/util"
)

// CountActiveJobsBySourceSession counts the jobs this one session submitted
// (jobs.source_session_id) that are still in flight and started at or after
// `since` (unix seconds; 0 = no lower bound). Unlike CountActiveJobsByCaller it
// never looks at the caller, so another terminal session sharing the same
// caller does not count. A resident session job (one carrying session state)
// parked in awaiting_input is excluded: it is itself waiting for a person, not
// work someone is watching. An empty sessionID counts nothing.
func (s *Store) CountActiveJobsBySourceSession(sessionID string, since int64) (int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(supervisedJobStatuses)), ",")
	q := `SELECT COUNT(*) FROM jobs WHERE source_session_id = ? AND started_at >= ? AND status IN (` + placeholders + `)
AND NOT (status = 'awaiting_input' AND COALESCE(session_state_json,'') <> '')`
	args := make([]any, 0, util.CapSum(len(supervisedJobStatuses), 2))
	args = append(args, sessionID, since)
	for _, st := range supervisedJobStatuses {
		args = append(args, st)
	}
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("jobstore: count active jobs by source session: %w", err)
	}
	return n, nil
}
