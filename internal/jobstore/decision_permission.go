package jobstore

import (
	"errors"
	"fmt"
)

// DecisionKindPermission marks a decision that is a terminal agent's tool
// permission prompt (Claude Code's PermissionRequest hook) mirrored to the web:
// the hook waits on it while the person is away, and the answer (allow /
// allow-always / deny) becomes the hook's permission decision. It shares the
// session decision rows with relay turns (session_id set) but is never a turn:
// the relay's say / deliver paths skip it (ListOpenSessionTurns).
const DecisionKindPermission = "permission"

// ListOpenSessionTurns returns the session's OPEN decisions that are relay turns
// (every kind except DecisionKindPermission), newest first. It is what "answer
// the session's open turn" must look at: a pending permission prompt is answered
// through its own endpoint, never by free text typed into the reply box.
func (s *Store) ListOpenSessionTurns(sessionID string, limit int) ([]*PlanDecision, error) {
	return s.listOpenSessionDecisions(sessionID, "COALESCE(kind,'') <> ?", DecisionKindPermission, limit)
}

// ListOpenSessionPermissions returns the session's OPEN permission decisions,
// newest first.
func (s *Store) ListOpenSessionPermissions(sessionID string, limit int) ([]*PlanDecision, error) {
	return s.listOpenSessionDecisions(sessionID, "kind = ?", DecisionKindPermission, limit)
}

func (s *Store) listOpenSessionDecisions(sessionID, kindCond, kind string, limit int) ([]*PlanDecision, error) {
	if sessionID == "" {
		return nil, errors.New("jobstore: list open session decisions: empty session_id")
	}
	if err := s.expireDueDecisions(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(selectDecisionCols+" WHERE session_id = ? AND state = 'OPEN' AND "+kindCond+
		" ORDER BY asked_at DESC, rowid DESC LIMIT ?", sessionID, kind, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list open session decisions: %w", err)
	}
	defer rows.Close()
	out := make([]*PlanDecision, 0)
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan decision row: %w", err)
		}
		dd := d
		out = append(out, &dd)
	}
	return out, rows.Err()
}
