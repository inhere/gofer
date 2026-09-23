package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// JobCredentialKinds are the two kinds of job-scoped credential (SEC-01): a member
// job's and a leader job's. The kind is what the hub's permission table keys on —
// a leader may move its own plan's checklist items and @-dispatch from a plan
// comment, a member may not do either.
const (
	JobCredentialMember = "member"
	JobCredentialLeader = "leader"
)

// ValidJobCredentialKind reports whether k is a known credential kind.
func ValidJobCredentialKind(k string) bool {
	return k == JobCredentialMember || k == JobCredentialLeader
}

// JobTokenRecord is one row of job_tokens: the hub's record of the credential it
// minted for a job.
//
// Only the sha256 of the token is stored — never the token itself (design §横切).
// The secret exists in exactly two places: the job process's environment, and the
// dispatch frame that carried it to a worker. A stolen database therefore yields no
// usable credential.
type JobTokenRecord struct {
	// JobID is the HUB job id. A worker executing a dispatched job uses this id (not
	// its own local id) because every credential check happens on the hub.
	JobID string
	// TokenHash is the hex sha256 of the token (the lookup key).
	TokenHash string
	// Kind is JobCredentialMember or JobCredentialLeader.
	Kind string
	// PlanID is the plan the job belongs to ("" for an unattached job). A leader job
	// always carries one: its widened rights are scoped to it.
	PlanID string
	// ExpiresAt is the fallback deadline (unix seconds): a job whose terminal path
	// never ran — a crashed hub, a killed process — still loses its credential.
	ExpiresAt int64
	// RevokedAt is when the terminal path revoked it; 0 while live.
	RevokedAt int64
	CreatedAt int64
}

// UpsertJobToken stores (or replaces) the credential row of a job. Replacing is the
// right shape for a re-submitted/rebuild job that reuses an id — there is at most one
// live credential per job, and the new one supersedes the old.
func (s *Store) UpsertJobToken(rec JobTokenRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO job_tokens (job_id, token_hash, kind, plan_id, expires_at, revoked_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(job_id) DO UPDATE SET
		   token_hash=excluded.token_hash, kind=excluded.kind, plan_id=excluded.plan_id,
		   expires_at=excluded.expires_at, revoked_at=excluded.revoked_at, created_at=excluded.created_at`,
		rec.JobID, rec.TokenHash, rec.Kind, rec.PlanID, rec.ExpiresAt, rec.RevokedAt, rec.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("jobstore: upsert job token %q: %w", rec.JobID, err)
	}
	return nil
}

// GetJobTokenByHash resolves a presented credential by its sha256. It returns
// ok=false when no row matches (an unknown token) and leaves the LIVE/expired/
// revoked decision to the caller, which needs to distinguish "not a credential at
// all" from "a credential that no longer works" (bd-free: both are a 401, but the
// caller logs them differently).
func (s *Store) GetJobTokenByHash(hash string) (JobTokenRecord, bool, error) {
	if hash == "" {
		return JobTokenRecord{}, false, nil
	}
	row := s.db.QueryRow(
		`SELECT job_id, token_hash, kind, COALESCE(plan_id,''), expires_at, COALESCE(revoked_at,0), created_at
		 FROM job_tokens WHERE token_hash = ?`, hash)
	rec, err := scanJobToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return JobTokenRecord{}, false, nil
	}
	if err != nil {
		return JobTokenRecord{}, false, fmt.Errorf("jobstore: get job token: %w", err)
	}
	return rec, true, nil
}

// GetJobToken returns the credential row of a job ("" id, false when the job has
// none). It answers "does this job hold a credential, and is it still live" for the
// job detail/audit surface.
func (s *Store) GetJobToken(jobID string) (JobTokenRecord, bool, error) {
	if jobID == "" {
		return JobTokenRecord{}, false, nil
	}
	row := s.db.QueryRow(
		`SELECT job_id, token_hash, kind, COALESCE(plan_id,''), expires_at, COALESCE(revoked_at,0), created_at
		 FROM job_tokens WHERE job_id = ?`, jobID)
	rec, err := scanJobToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return JobTokenRecord{}, false, nil
	}
	if err != nil {
		return JobTokenRecord{}, false, fmt.Errorf("jobstore: get job token for %q: %w", jobID, err)
	}
	return rec, true, nil
}

// RevokeJobToken marks a job's credential dead (revoked_at = now). It reports
// whether a row was actually flipped (false = the job holds no credential, or it was
// already revoked) so the terminal path can stay silent about a no-op.
func (s *Store) RevokeJobToken(jobID string, now int64) (bool, error) {
	if jobID == "" {
		return false, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE job_tokens SET revoked_at = ? WHERE job_id = ? AND COALESCE(revoked_at,0) = 0`, now, jobID)
	if err != nil {
		return false, fmt.Errorf("jobstore: revoke job token %q: %w", jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: revoke job token %q: %w", jobID, err)
	}
	return n > 0, nil
}

// PruneJobTokens deletes credential rows whose job is long gone (revoked or expired
// before cutoff). The rows a pruned JOB leaves behind are deleted with the job
// itself (see PruneJobs); this sweeper covers the rest — a credential revoked by
// the terminal path but whose job row is retained for its result dir.
func (s *Store) PruneJobTokens(cutoff int64) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		`DELETE FROM job_tokens WHERE COALESCE(revoked_at, 0) > 0 AND revoked_at < ?
		   OR COALESCE(revoked_at, 0) = 0 AND expires_at < ?`, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("jobstore: prune job tokens: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: prune job tokens: %w", err)
	}
	return int(n), nil
}

// scanJobToken reads one job_tokens row in the canonical column order.
func scanJobToken(sc rowScanner) (JobTokenRecord, error) {
	var rec JobTokenRecord
	err := sc.Scan(&rec.JobID, &rec.TokenHash, &rec.Kind, &rec.PlanID, &rec.ExpiresAt, &rec.RevokedAt, &rec.CreatedAt)
	return rec, err
}
