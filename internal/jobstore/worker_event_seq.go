package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// InsertWorkerJobEvent appends one worker-mirrored event, de-duplicated by the
// worker's per-job sequence number (protocol v30). In ONE transaction it reads the
// job's jobs.worker_event_seq, and only when seq is above it inserts the row and moves
// the counter up to seq. A replayed event (seq <= the counter) inserts nothing and
// reports applied=false, so the caller must skip every side effect of a new event
// (webhook enqueue, observers, push). rowSeq is the job_events row's seq (0 when not
// applied).
//
// The job row must exist: the counter lives on it. A missing job is an error rather
// than an unconditional insert, because without the row there is nothing to dedup on
// and a later replay would insert a second copy.
func (s *Store) InsertWorkerJobEvent(e JobEvent, seq int64) (rowSeq int64, applied bool, err error) {
	if e.JobID == "" {
		return 0, false, errors.New("jobstore: InsertWorkerJobEvent: empty job id")
	}
	if e.Type == "" {
		return 0, false, errors.New("jobstore: InsertWorkerJobEvent: empty event type")
	}
	if seq <= 0 {
		return 0, false, errors.New("jobstore: InsertWorkerJobEvent: seq must be positive")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("jobstore: insert worker job event %q/%q: begin: %w", e.JobID, e.Type, err)
	}
	defer func() { _ = tx.Rollback() }()
	var applied0 int64
	if err := tx.QueryRow(`SELECT worker_event_seq FROM jobs WHERE id = ?`, e.JobID).Scan(&applied0); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, fmt.Errorf("jobstore: insert worker job event %q/%q: unknown job", e.JobID, e.Type)
		}
		return 0, false, fmt.Errorf("jobstore: insert worker job event %q/%q: read cursor: %w", e.JobID, e.Type, err)
	}
	if seq <= applied0 {
		return 0, false, nil
	}
	var detail any
	if e.Detail != "" {
		detail = e.Detail
	}
	res, err := tx.Exec(`INSERT INTO job_events (job_id, type, detail_json, at) VALUES (?,?,?,?)`, e.JobID, e.Type, detail, e.At)
	if err != nil {
		return 0, false, fmt.Errorf("jobstore: insert worker job event %q/%q: %w", e.JobID, e.Type, err)
	}
	rowSeq, err = res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("jobstore: insert worker job event %q/%q last id: %w", e.JobID, e.Type, err)
	}
	if _, err := tx.Exec(`UPDATE jobs SET worker_event_seq = ? WHERE id = ?`, seq, e.JobID); err != nil {
		return 0, false, fmt.Errorf("jobstore: insert worker job event %q/%q: advance cursor: %w", e.JobID, e.Type, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("jobstore: insert worker job event %q/%q: commit: %w", e.JobID, e.Type, err)
	}
	return rowSeq, true, nil
}

// WorkerEventSeq returns the highest worker event sequence applied for a job (0 for a
// job with none applied or an unknown job): the cursor the hub echoes in the resume
// ack (ResumeJob.EventSeq).
func (s *Store) WorkerEventSeq(jobID string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT worker_event_seq FROM jobs WHERE id = ?`, jobID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("jobstore: worker event seq %q: %w", jobID, err)
	}
	return n, nil
}
