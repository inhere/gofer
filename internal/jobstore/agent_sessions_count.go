package jobstore

import "fmt"

// ActiveACPSessionCounts reports resident session processes per agent. Queued
// jobs have no process yet and terminal jobs no longer occupy a slot.
func (s *Store) ActiveACPSessionCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT agent, COUNT(*) FROM jobs
  WHERE COALESCE(session_state_json,'') <> ''
    AND status IN ('running','awaiting_input','pending_interaction','recovering')
  GROUP BY agent`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: count ACP sessions: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var agent string
		var count int
		if err := rows.Scan(&agent, &count); err != nil {
			return nil, fmt.Errorf("jobstore: scan ACP sessions: %w", err)
		}
		counts[agent] = count
	}
	return counts, rows.Err()
}
