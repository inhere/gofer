package jobstore

import (
	"encoding/json"
	"fmt"
)

// AwaitingTurn is the stored fact about the turn a resident ACP session job just ended: the
// session is in awaiting_input and the turn's final message carried the needs-reply marker
// (the job layer records it as needs_reply on the job.awaiting_input event). At is when the
// turn ended. The fact is read from rows only, so it holds across a server restart.
type AwaitingTurn struct {
	JobID  string
	TurnNo int64
	At     int64
}

// ListMarkedAwaitingTurns returns, per session job that is in awaiting_input right now, the
// newest job.awaiting_input event when it carries needs_reply. A job whose newest turn has
// no marker is absent: it is a report, not a question. The map key is the job id.
func (s *Store) ListMarkedAwaitingTurns() (map[string]AwaitingTurn, error) {
	rows, err := s.db.Query(`SELECT e.job_id, e.at, COALESCE(e.detail_json,'')
FROM job_events e JOIN jobs j ON j.id = e.job_id
WHERE e.type = ? AND j.status = 'awaiting_input' AND COALESCE(j.session_state_json,'') <> ''
  AND e.seq = (SELECT MAX(x.seq) FROM job_events x WHERE x.job_id = e.job_id AND x.type = ?)`, evAwaitingInput, evAwaitingInput)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list marked awaiting turns: %w", err)
	}
	defer rows.Close()
	out := map[string]AwaitingTurn{}
	for rows.Next() {
		var t AwaitingTurn
		var detail string
		if err := rows.Scan(&t.JobID, &t.At, &detail); err != nil {
			return nil, fmt.Errorf("jobstore: scan marked awaiting turn: %w", err)
		}
		var d struct {
			NeedsReply bool  `json:"needs_reply"`
			TurnNo     int64 `json:"turn_no"`
		}
		if json.Unmarshal([]byte(detail), &d) != nil || !d.NeedsReply {
			continue
		}
		t.TurnNo = d.TurnNo
		out[t.JobID] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: marked awaiting turns rows: %w", err)
	}
	return out, nil
}
