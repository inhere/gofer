package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// JobRecord is the SQLite-persisted projection of a job: the queryable job
// metadata (== the job package's JobResult fields) plus submission/bookkeeping
// columns (WorkerID, RequestJSON, UpdatedAt). It is a neutral struct, decoupled
// from internal/job, to keep this package free of a job import (see package doc).
//
// Zero values mean "unset": EndedAt == 0 is "not ended yet" and empty strings are
// stored/read as such (the SELECTs COALESCE NULLs to the zero value), matching
// JobResult's omitempty semantics.
type JobRecord struct {
	ID          string
	ProjectKey  string
	Agent       string
	Runner      string
	WorkerID    string // reserved for ws-worker; empty for local/peer jobs
	Status      string
	ExitCode    int
	Cwd         string
	ResultDir   string // per-job log/artifact directory (logs stay on disk)
	RequestJSON string // original JobRequest JSON, for re-submit/audit
	Error       string
	StartedAt   int64
	EndedAt     int64
	UpdatedAt   int64
}

// ListQuery filters/bounds a ListJobs query. A zero value lists every project's
// jobs (no status filter), newest first, capped at DefaultListLimit.
type ListQuery struct {
	Project string // exact project_key match when non-empty
	Status  string // exact status match when non-empty
	Limit   int    // <= 0 => DefaultListLimit
	Offset  int    // skip the first Offset rows (pagination); ignored when <= 0
	Since   int64  // when > 0, keep only jobs with started_at >= Since
}

// selectCols is the shared projection for GetJob/ListJobs. COALESCE guards the
// nullable columns so a NULL (from any future writer) scans into the zero value
// instead of failing the scan into a plain string/int64.
const selectCols = `SELECT id, project_key, agent, runner, COALESCE(worker_id,''),
  status, exit_code, COALESCE(cwd,''), result_dir, COALESCE(request_json,''),
  COALESCE(error,''), started_at, COALESCE(ended_at,0), updated_at FROM jobs`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob reads one row (in selectCols order) into a JobRecord.
func scanJob(sc rowScanner) (JobRecord, error) {
	var r JobRecord
	err := sc.Scan(
		&r.ID, &r.ProjectKey, &r.Agent, &r.Runner, &r.WorkerID,
		&r.Status, &r.ExitCode, &r.Cwd, &r.ResultDir, &r.RequestJSON,
		&r.Error, &r.StartedAt, &r.EndedAt, &r.UpdatedAt,
	)
	return r, err
}

// UpsertJob inserts a job row or updates the existing one with the same id. The
// create and finish writes for a job are two upserts on the same row (not two
// appended lines as in jobs.jsonl), so the index stays naturally deduplicated.
// UpdatedAt falls back to StartedAt when the caller leaves it zero, so ordering /
// retention always have a value.
func (s *Store) UpsertJob(rec JobRecord) error {
	if rec.ID == "" {
		return errors.New("jobstore: UpsertJob: empty job id")
	}
	if rec.UpdatedAt == 0 {
		rec.UpdatedAt = rec.StartedAt
	}
	const q = `INSERT INTO jobs
  (id, project_key, agent, runner, worker_id, status, exit_code, cwd, result_dir,
   request_json, error, started_at, ended_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(id) DO UPDATE SET
    project_key=excluded.project_key,
    agent=excluded.agent,
    runner=excluded.runner,
    worker_id=excluded.worker_id,
    status=excluded.status,
    exit_code=excluded.exit_code,
    cwd=excluded.cwd,
    result_dir=excluded.result_dir,
    request_json=excluded.request_json,
    error=excluded.error,
    started_at=excluded.started_at,
    ended_at=excluded.ended_at,
    updated_at=excluded.updated_at`
	// Serialise writes in-process (see Store.writeMu) so SQLite never sees two
	// concurrent writers and cannot return SQLITE_BUSY under burst.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(q,
		rec.ID, rec.ProjectKey, rec.Agent, rec.Runner, rec.WorkerID,
		rec.Status, rec.ExitCode, rec.Cwd, rec.ResultDir, rec.RequestJSON,
		rec.Error, rec.StartedAt, rec.EndedAt, rec.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("jobstore: upsert job %q: %w", rec.ID, err)
	}
	return nil
}

// GetJob returns the job by id. The bool is false (with a nil error) when no
// such job exists, distinguishing "not found" from a real query error.
func (s *Store) GetJob(id string) (JobRecord, bool, error) {
	rec, err := scanJob(s.db.QueryRow(selectCols+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return JobRecord{}, false, nil
	}
	if err != nil {
		return JobRecord{}, false, fmt.Errorf("jobstore: get job %q: %w", id, err)
	}
	return rec, true, nil
}

// ListJobs returns job records matching q, newest first (started_at desc, id
// desc as a stable tiebreaker), with DB-side filtering, ordering and pagination.
func (s *Store) ListJobs(q ListQuery) ([]JobRecord, error) {
	var where []string
	var args []any
	if q.Project != "" {
		where = append(where, "project_key = ?")
		args = append(args, q.Project)
	}
	if q.Status != "" {
		where = append(where, "status = ?")
		args = append(args, q.Status)
	}
	if q.Since > 0 {
		where = append(where, "started_at >= ?")
		args = append(args, q.Since)
	}

	query := selectCols
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY started_at DESC, id DESC"

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	query += " LIMIT ?"
	args = append(args, limit)
	if q.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, q.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list jobs: %w", err)
	}
	defer rows.Close()

	out := make([]JobRecord, 0, limit)
	for rows.Next() {
		rec, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan job row: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list jobs rows: %w", err)
	}
	return out, nil
}
