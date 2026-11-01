package jobstore

import "fmt"

// StewardObservation is one write a proactive steward review asked for while the steward is in
// observe mode (steward.act_proactively off): the server recorded it instead of doing it, so a
// person can see what the steward would have done without being disturbed by it.
type StewardObservation struct {
	ID       int64  `json:"id"`
	ReviewID int64  `json:"review_id,omitempty"`
	At       int64  `json:"at"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	// Body is the request body the steward sent (capped).
	Body string `json:"body,omitempty"`
}

const (
	maxObservationBody = 4000
	// keepObservations bounds the table: the newest rows stay.
	keepObservations = 500
)

// AddStewardObservation records one skipped write and trims the table to the newest rows.
func (s *Store) AddStewardObservation(reviewID int64, method, path, body string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`INSERT INTO steward_observations(review_id, at, method, path, body) VALUES (?,?,?,?,?)`,
		reviewID, s.unixNow(), method, path, capText(body, maxObservationBody)); err != nil {
		return fmt.Errorf("jobstore: add steward observation: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM steward_observations WHERE id <= (SELECT COALESCE(MAX(id),0) FROM steward_observations) - ?`, keepObservations); err != nil {
		return fmt.Errorf("jobstore: trim steward observations: %w", err)
	}
	return nil
}

// ListStewardObservations lists the recorded observations, newest first.
func (s *Store) ListStewardObservations(limit int) ([]StewardObservation, error) {
	if limit <= 0 || limit > keepObservations {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, review_id, at, method, path, body FROM steward_observations ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list steward observations: %w", err)
	}
	defer rows.Close()
	out := make([]StewardObservation, 0)
	for rows.Next() {
		var o StewardObservation
		if err := rows.Scan(&o.ID, &o.ReviewID, &o.At, &o.Method, &o.Path, &o.Body); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
