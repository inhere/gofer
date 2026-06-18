package jobstore

import (
	"errors"
	"fmt"
)

// InteractionRecord is the SQLite-persisted projection of one running-job
// interaction (SP4, design §8). Like JobRecord it is a neutral struct, decoupled
// from internal/job, so the interactions table can be populated without this
// package importing job (which would form a job -> jobstore -> job cycle).
//
// OptionsJSON holds the marshalled []InteractionOption (the choice/confirmation
// options) as the job package serialises it; an empty string means "no options".
// Answer/AnsweredAt are unset (empty / 0) while the interaction is still pending;
// the nullable columns COALESCE to those zero values on read (see selectInterCols).
type InteractionRecord struct {
	ID          string
	JobID       string
	Type        string
	Prompt      string
	OptionsJSON string
	Status      string
	Answer      string
	CreatedAt   int64
	AnsweredAt  int64
}

// selectInterCols is the shared projection for ListInteractions. COALESCE guards
// the nullable columns (options_json/answer/answered_at) so a NULL scans into the
// zero value instead of failing the scan, mirroring jobs.selectCols.
const selectInterCols = `SELECT id, job_id, type, prompt, COALESCE(options_json,''),
  status, COALESCE(answer,''), created_at, COALESCE(answered_at,0)
  FROM interactions`

// scanInteraction reads one row (in selectInterCols order) into an InteractionRecord.
func scanInteraction(sc rowScanner) (InteractionRecord, error) {
	var r InteractionRecord
	err := sc.Scan(
		&r.ID, &r.JobID, &r.Type, &r.Prompt, &r.OptionsJSON,
		&r.Status, &r.Answer, &r.CreatedAt, &r.AnsweredAt,
	)
	return r, err
}

// UpsertInteraction inserts an interaction row or updates the existing one with
// the same (job_id, id). The pending and answered writes for one interaction are
// two upserts on the same row (the answered snapshot overwrites the pending one),
// so the table keeps a single, latest row per interaction — the DB equivalent of
// folding interactions.jsonl. Writes go through s.writeMu (like UpsertJob) so
// SQLite never sees two concurrent writers and cannot return SQLITE_BUSY.
func (s *Store) UpsertInteraction(rec InteractionRecord) error {
	if rec.ID == "" {
		return errors.New("jobstore: UpsertInteraction: empty interaction id")
	}
	if rec.JobID == "" {
		return errors.New("jobstore: UpsertInteraction: empty job id")
	}
	const q = `INSERT INTO interactions
  (id, job_id, type, prompt, options_json, status, answer, created_at, answered_at)
  VALUES (?,?,?,?,?,?,?,?,?)
  ON CONFLICT(job_id, id) DO UPDATE SET
    type=excluded.type,
    prompt=excluded.prompt,
    options_json=excluded.options_json,
    status=excluded.status,
    answer=excluded.answer,
    created_at=excluded.created_at,
    answered_at=excluded.answered_at`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(q,
		rec.ID, rec.JobID, rec.Type, rec.Prompt, rec.OptionsJSON,
		rec.Status, rec.Answer, rec.CreatedAt, rec.AnsweredAt,
	)
	if err != nil {
		return fmt.Errorf("jobstore: upsert interaction %q/%q: %w", rec.JobID, rec.ID, err)
	}
	return nil
}

// ListInteractions returns the interactions for a job in creation order
// (created_at asc, id asc as a stable tiebreaker). A job with no interactions
// yields an empty slice and no error.
func (s *Store) ListInteractions(jobID string) ([]InteractionRecord, error) {
	rows, err := s.db.Query(selectInterCols+" WHERE job_id = ? ORDER BY created_at ASC, id ASC", jobID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list interactions %q: %w", jobID, err)
	}
	defer rows.Close()

	out := make([]InteractionRecord, 0)
	for rows.Next() {
		rec, scanErr := scanInteraction(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan interaction row: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list interactions %q rows: %w", jobID, err)
	}
	return out, nil
}
