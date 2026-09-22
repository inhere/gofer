package jobstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Retry states (R2/AUTO-03, design §二.1). A row is created PENDING by the failing
// job's finish path, moves to CLAIMED while a sweeper owns it for one submit
// attempt, and ends DONE (submitted, new_job_id recorded) or CANCELLED (an operator
// dropped it). A claimed row whose process died is NOT lost: its lease lapses and a
// later sweep claims it again (at-least-once, see ClaimDueRetries).
const (
	RetryPending   = "pending"
	RetryClaimed   = "claimed"
	RetryDone      = "done"
	RetryCancelled = "cancelled"
)

// RetryRecord is the SQLite-persisted durable retry of ONE failed job (R2/AUTO-03,
// design §二.1). It is a neutral struct — this package must not import internal/job
// (G022) — so RequestJSON is the re-submittable JobRequest as an opaque string: the
// sweeper (the job package) unmarshals it and submits it.
//
// SourceJobID is the job that FAILED (the row exists to re-run it); Attempt is the
// 1-based number of the attempt about to run (>= 2, the first run being attempt 1);
// Reason is why it was scheduled (`exit_code=N`); NextRunAt is the unix second the
// row becomes due; LeaseUntil is the claim lease (0 while pending, now+lease while
// claimed); NewJobID is the submitted job once done.
type RetryRecord struct {
	ID          string `json:"id"`
	SourceJobID string `json:"source_job_id"`
	Attempt     int    `json:"attempt"`
	RequestJSON string `json:"-"`
	Reason      string `json:"reason"`
	NextRunAt   int64  `json:"next_run_at"`
	LeaseUntil  int64  `json:"lease_until,omitempty"`
	State       string `json:"state"`
	NewJobID    string `json:"new_job_id,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

const selectRetryCols = `SELECT id, source_job_id, attempt, request_json, reason,
  next_run_at, lease_until, state, COALESCE(new_job_id,''), created_at
  FROM job_retries`

// retryDuePredicate is the "this row may be handed out now" condition, shared by the
// candidate SELECT and the per-row claim CAS so two claims can never disagree about
// what due means: a pending row whose next_run_at arrived, OR a claimed row whose
// LEASE lapsed (the process that claimed it died before submitting — at-least-once).
const retryDuePredicate = `(state = ? AND next_run_at <= ?) OR (state = ? AND lease_until <= ?)`

func scanRetry(sc rowScanner) (RetryRecord, error) {
	var r RetryRecord
	err := sc.Scan(
		&r.ID, &r.SourceJobID, &r.Attempt, &r.RequestJSON, &r.Reason,
		&r.NextRunAt, &r.LeaseUntil, &r.State, &r.NewJobID, &r.CreatedAt,
	)
	return r, err
}

// NewRetryID mints a retry id: `rt-` + 8 random hex chars, the XFER-02 short-id
// shape (2 letters + 8 hex, like the transfers' `xf-` and the wakeups' `wk-`). It is
// what a human reads in `job retry ls` / an event scope, so it stays short; the id
// is a PRIMARY KEY, so a collision is absorbed by the insert failing rather than by
// handing one row out twice.
func NewRetryID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "rt-" + hex.EncodeToString(b[:])
	}
	// crypto/rand never fails in practice; a time-based fallback keeps the process
	// alive rather than panicking on a theoretical syscall error.
	return fmt.Sprintf("rt-%08x", uint32(time.Now().UnixNano()))
}

// InsertRetry records one pending retry. It refuses a half-built row (no id, no
// source job, no request to re-submit, attempt < 1) and defaults the state to
// pending, so a row never reads back as "unset". CreatedAt is taken as given (the
// callers pass the clock, keeping this store testable/clock-free).
func (s *Store) InsertRetry(r RetryRecord) error {
	if r.ID == "" {
		return errors.New("jobstore: InsertRetry: empty retry id")
	}
	if r.SourceJobID == "" {
		return fmt.Errorf("jobstore: InsertRetry %q: missing source_job_id", r.ID)
	}
	if r.RequestJSON == "" {
		return fmt.Errorf("jobstore: InsertRetry %q: missing request_json", r.ID)
	}
	if r.Attempt < 1 {
		return fmt.Errorf("jobstore: InsertRetry %q: attempt must be >= 1 (got %d)", r.ID, r.Attempt)
	}
	if r.State == "" {
		r.State = RetryPending
	}
	const q = `INSERT INTO job_retries
  (id, source_job_id, attempt, request_json, reason, next_run_at, lease_until, state, new_job_id, created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?)`
	var newJobID any
	if r.NewJobID != "" {
		newJobID = r.NewJobID
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, r.ID, r.SourceJobID, r.Attempt, r.RequestJSON, r.Reason,
		r.NextRunAt, r.LeaseUntil, r.State, newJobID, r.CreatedAt); err != nil {
		return fmt.Errorf("jobstore: insert retry %q: %w", r.ID, err)
	}
	return nil
}

// ClaimDueRetries atomically claims up to limit retry rows that are due at now and
// returns them for the sweeper to submit, leasing each one for `lease` seconds (see
// retryDuePredicate for what "due" means).
//
// The claim is a conditional UPDATE per candidate: only an UPDATE that still finds
// the row due (`... WHERE id = ? AND (<predicate>)`) affects a row and hands it out,
// so two concurrent claims (or a claim racing a lease lapse) can never submit the
// SAME row twice. Claiming moves the row to state=claimed with lease_until=now+lease,
// which takes it out of the due window until the lease lapses — and THAT is what
// makes a crash between claim and submit recoverable: the work is late by at most one
// lease, never lost (at-least-once, the mirror of ClaimDueDeliveries).
//
// All work runs under writeMu so the SELECT and the per-row UPDATEs never interleave
// with another writer. now/limit/lease are injected so tests can pin the clock, batch
// size and lease; a non-positive limit yields no rows, and a non-positive lease falls
// back to ClaimLeaseSeconds.
func (s *Store) ClaimDueRetries(now int64, limit int, lease int64) ([]RetryRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	if lease <= 0 {
		lease = ClaimLeaseSeconds
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := s.db.Query(selectRetryCols+` WHERE `+retryDuePredicate+
		` ORDER BY next_run_at ASC, id ASC LIMIT ?`,
		RetryPending, now, RetryClaimed, now, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("jobstore: claim due retries select: %w", err)
	}
	var candidates []RetryRecord
	for rows.Next() {
		r, scanErr := scanRetry(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("jobstore: claim due retries scan: %w", scanErr)
		}
		candidates = append(candidates, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("jobstore: claim due retries rows: %w", err)
	}
	rows.Close()

	leaseUntil := now + lease
	out := make([]RetryRecord, 0, len(candidates))
	for _, r := range candidates {
		res, uerr := s.db.Exec(
			`UPDATE job_retries SET state = ?, lease_until = ? WHERE id = ? AND (`+retryDuePredicate+`)`,
			RetryClaimed, leaseUntil, r.ID, RetryPending, now, RetryClaimed, now,
		)
		if uerr != nil {
			return nil, fmt.Errorf("jobstore: claim retry %q: %w", r.ID, uerr)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			r.State = RetryClaimed
			r.LeaseUntil = leaseUntil
			out = append(out, r)
		}
	}
	return out, nil
}

// MarkRetryDone records that a claimed retry was submitted as newJobID: the row
// becomes done, releases its lease and keeps the id of the job it produced. Only a
// CLAIMED row can be completed (that is the state the sweeper just took it in), so a
// row cancelled or completed elsewhere is reported as an error instead of being
// silently overwritten.
func (s *Store) MarkRetryDone(id, newJobID string) error {
	if newJobID == "" {
		return fmt.Errorf("jobstore: MarkRetryDone %q: empty new job id", id)
	}
	return s.transitionRetry(id, `UPDATE job_retries SET state = ?, new_job_id = ?, lease_until = 0
    WHERE id = ? AND state = ?`,
		RetryDone, newJobID, id, RetryClaimed)
}

// ReleaseRetry gives a claimed retry back for a LATER attempt: it returns to pending
// with a new next_run_at (a failed submit must not lose the row — the design's "最多
// 晚一轮") and drops the lease. Only a claimed row can be released.
func (s *Store) ReleaseRetry(id string, nextRunAt int64) error {
	return s.transitionRetry(id, `UPDATE job_retries SET state = ?, next_run_at = ?, lease_until = 0
    WHERE id = ? AND state = ?`,
		RetryPending, nextRunAt, id, RetryClaimed)
}

// CancelRetry drops a retry an operator no longer wants: pending or claimed rows
// become cancelled. A row that already reached done/cancelled is terminal and this
// returns an error (the fixed choice — a rejected transition is reported).
func (s *Store) CancelRetry(id string) error {
	return s.transitionRetry(id, `UPDATE job_retries SET state = ?, lease_until = 0
    WHERE id = ? AND state IN (?, ?)`,
		RetryCancelled, id, RetryPending, RetryClaimed)
}

// transitionRetry runs one conditional UPDATE and turns "0 rows changed" into an
// error naming the id — every caller here is a state transition that MUST have
// matched a row it owned.
func (s *Store) transitionRetry(id, q string, args ...any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return fmt.Errorf("jobstore: retry %q transition: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("jobstore: retry %q: no row in the required state", id)
	}
	return nil
}

// ListRetriesByJob returns a job's retry chain oldest attempt first (the order the
// job detail and `job retry ls` read it in). A job with no retries yields an empty
// slice, not an error — every job without a retry policy is that case.
func (s *Store) ListRetriesByJob(jobID string) ([]RetryRecord, error) {
	rows, err := s.db.Query(selectRetryCols+` WHERE source_job_id = ? ORDER BY attempt ASC, id ASC`, jobID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list retries by job %q: %w", jobID, err)
	}
	defer rows.Close()
	var out []RetryRecord
	for rows.Next() {
		r, scanErr := scanRetry(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: list retries by job %q: %w", jobID, scanErr)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
