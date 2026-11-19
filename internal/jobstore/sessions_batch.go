package jobstore

import (
	"fmt"
	"strings"
)

// GetAgentSessionsByIDs reads many sessions in one query (a roster must not read them one by
// one). Unknown ids are simply absent from the result.
func (s *Store) GetAgentSessionsByIDs(ids []string) (map[string]AgentSession, error) {
	uniq := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	out := make(map[string]AgentSession, len(uniq))
	if len(uniq) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(selectSessionCols+` WHERE session_id IN (`+mailPlaceholders(len(uniq))+`)`, mailAnySlice(uniq)...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: sessions by ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: sessions by ids: %w", err)
		}
		out[a.SessionID] = a
	}
	return out, rows.Err()
}
