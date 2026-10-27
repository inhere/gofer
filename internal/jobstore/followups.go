package jobstore

import (
	"fmt"
	"time"
)

// FollowUpRecord is one queued request to continue a live job's agent session.
// It is durable so a server restart does not silently discard work the caller added.
type FollowUpRecord struct {
	ID             int64
	JobID          string
	Prompt         string
	CreatedAt      int64
	CreatedBy      string
	ReviewEach     bool
	Status         string
	ContinuationID string
}

const (
	FollowUpPending = "pending"
	FollowUpClaimed = "claimed"
	FollowUpDone    = "done"
	FollowUpDropped = "dropped"
)

func scanFollowUp(sc rowScanner) (FollowUpRecord, error) {
	var r FollowUpRecord
	var review int
	err := sc.Scan(&r.ID, &r.JobID, &r.Prompt, &r.CreatedAt, &r.CreatedBy, &review, &r.Status, &r.ContinuationID)
	r.ReviewEach = review != 0
	return r, err
}

// AddFollowUp appends a request to a job's durable queue.
func (s *Store) AddFollowUp(jobID, prompt, createdBy string, reviewEach bool) (FollowUpRecord, error) {
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO job_followups(job_id,prompt,created_at,created_by,review_each,status) VALUES(?,?,?,?,?,?)`,
		jobID, prompt, now, createdBy, boolIntFollowup(reviewEach), FollowUpPending)
	if err != nil {
		return FollowUpRecord{}, fmt.Errorf("jobstore: add follow-up: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return FollowUpRecord{}, fmt.Errorf("jobstore: follow-up id: %w", err)
	}
	return FollowUpRecord{ID: id, JobID: jobID, Prompt: prompt, CreatedAt: now, CreatedBy: createdBy, ReviewEach: reviewEach, Status: FollowUpPending}, nil
}

// ListFollowUps returns a job's queue, oldest first. Dropped/completed entries are
// included for audit; callers can filter them for a pending-only view.
func (s *Store) ListFollowUps(jobID string) ([]FollowUpRecord, error) {
	rows, err := s.db.Query(`SELECT id,job_id,prompt,created_at,COALESCE(created_by,''),COALESCE(review_each,0),status,COALESCE(continuation_id,'') FROM job_followups WHERE job_id=? ORDER BY id`, jobID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list follow-ups: %w", err)
	}
	defer rows.Close()
	out := make([]FollowUpRecord, 0)
	for rows.Next() {
		r, scanErr := scanFollowUp(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan follow-up: %w", scanErr)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClaimFollowUps atomically marks pending entries claimed and returns them. When
// one entry requests review_each, only that first entry is claimed so acceptance
// can gate the next continuation.
func (s *Store) ClaimFollowUps(jobID string, now int64) ([]FollowUpRecord, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.db.Query(`SELECT id,job_id,prompt,created_at,COALESCE(created_by,''),COALESCE(review_each,0),status,COALESCE(continuation_id,'') FROM job_followups WHERE job_id=? AND status=? ORDER BY id`, jobID, FollowUpPending)
	if err != nil {
		return nil, fmt.Errorf("jobstore: claim follow-ups: %w", err)
	}
	var out []FollowUpRecord
	for rows.Next() {
		r, scanErr := scanFollowUp(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		if r.ReviewEach && len(out) > 0 {
			break
		}
		out = append(out, r)
		if r.ReviewEach {
			break
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	for _, r := range out {
		if _, err := s.db.Exec(`UPDATE job_followups SET status=?,claimed_at=? WHERE id=? AND status=?`, FollowUpClaimed, now, r.ID, FollowUpPending); err != nil {
			return nil, fmt.Errorf("jobstore: mark follow-up claimed: %w", err)
		}
	}
	return out, nil
}

func (s *Store) CompleteFollowUps(ids []int64, continuationID string) error {
	if len(ids) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE job_followups SET status=?,continuation_id=? WHERE id=? AND status=?`, FollowUpDone, continuationID, id, FollowUpClaimed); err != nil {
			return fmt.Errorf("jobstore: complete follow-up: %w", err)
		}
	}
	return nil
}

func (s *Store) ReleaseFollowUps(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE job_followups SET status=? WHERE id=? AND status=?`, FollowUpPending, id, FollowUpClaimed); err != nil {
			return fmt.Errorf("jobstore: release follow-up: %w", err)
		}
	}
	return nil
}

func (s *Store) MovePendingFollowUps(fromJobID, toJobID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE job_followups SET job_id=? WHERE job_id=? AND status=?`, toJobID, fromJobID, FollowUpPending); err != nil {
		return fmt.Errorf("jobstore: move follow-ups: %w", err)
	}
	return nil
}

// DropFollowUp removes a still-pending queue entry. It returns false when the
// entry is missing or already claimed, allowing the API to report a stable conflict.
func (s *Store) DropFollowUp(jobID string, id int64) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE job_followups SET status=? WHERE id=? AND job_id=? AND status=?`, FollowUpDropped, id, jobID, FollowUpPending)
	if err != nil {
		return false, fmt.Errorf("jobstore: drop follow-up: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func boolIntFollowup(v bool) int {
	if v {
		return 1
	}
	return 0
}
