package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sqlite "modernc.org/sqlite"
)

// ErrRequestIDConflict is returned by UpsertJob when an INSERT of a NEW job id
// violates the partial unique index on request_id — i.e. another job already
// claimed the same request_id (C5 idempotency race). The caller (job.Submit)
// recovers by looking up and returning the job that won the race. It is NOT
// raised when the same job's row is updated in place (same id), only on a
// competing insert.
var ErrRequestIDConflict = errors.New("jobstore: request_id already exists")

// sqliteConstraintUnique is SQLITE_CONSTRAINT_UNIQUE (extended result code) as
// reported by modernc.org/sqlite. We avoid importing the heavy lib subpackage
// for the constant and pin the literal here.
const sqliteConstraintUnique = 2067

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
	// CallerID is the authenticated submitter id (C2). Empty for jobs created
	// without a caller token (legacy / allow_empty_token).
	CallerID string
	// RequestID is the optional client-supplied idempotency key (C5). Empty means
	// "no idempotency key"; only non-empty values are unique-constrained.
	RequestID string
	// 产出与审计（job-outcomes-audit）：job 终态时捕获的产出字段，best-effort 写入。
	RenderedCommand string // 渲染后实际 argv {command,args,env_keys} JSON（E15）
	ResultJSON      string // <result_dir>/result.json 内容（E6）
	ArtifactsJSON   string // [{name,size,mtime}] 产物清单（E1，P2）
	DiffSummary     string // git diff --stat 截断摘要（E12，P3）
	// Source 标记 job 实际执行位置（P4）：""(local) / worker:<id> / peer:<name>。
	Source string
	// TagsJSON 是 job 标签的 JSON 数组原文（E5），如 `["a","b"]`。空表示无标签。
	// 入库后用于 tags_json LIKE 检索（ListQuery.Tag）；与 job.JobResult.Tags 互转。
	TagsJSON string
	// WorkflowID 关联此 job 所属的 workflow（工作流/job 链）。空表示普通（非工作流）job。
	// 由工作流引擎在起 step-job 时设置；ListWorkflowJobs 据此 + StepIndex 排序回取。
	WorkflowID string
	// StepIndex 是此 job 在工作流中的 1-based 步序号（第 1 步=1）。非工作流 job 为 0。
	StepIndex int
	// Attempt 是 step-job 的 1-based 重试尝试号（P1，工作流 v2）。首次=1；重试起的新 job
	// attempt+1。持久化到 jobs.attempt；旧库/普通 job 经 selectCols COALESCE 成 1。
	// 与 StepIndex 一起区分同一 step 的多次重试运行（确定性 request_id 的 a<attempt> 段）。
	Attempt int
}

// ListQuery filters/bounds a ListJobs query. A zero value lists every project's
// jobs (no status filter), newest first, capped at DefaultListLimit.
type ListQuery struct {
	Project string // exact project_key match when non-empty
	Status  string // exact status match when non-empty
	Caller  string // exact caller_id match when non-empty (C2)
	Tag     string // tags_json contains this tag element when non-empty (E5)
	Agent   string // exact agent match when non-empty (E5)
	Runner  string // exact runner match when non-empty (E5)
	Limit   int    // <= 0 => DefaultListLimit
	Offset  int    // skip the first Offset rows (pagination); ignored when <= 0
	Since   int64  // when > 0, keep only jobs with started_at >= Since
}

// selectCols is the shared projection for GetJob/ListJobs. COALESCE guards the
// nullable columns so a NULL (from any future writer) scans into the zero value
// instead of failing the scan into a plain string/int64.
const selectCols = `SELECT id, project_key, agent, runner, COALESCE(worker_id,''),
  status, exit_code, COALESCE(cwd,''), result_dir, COALESCE(request_json,''),
  COALESCE(error,''), started_at, COALESCE(ended_at,0), updated_at,
  COALESCE(caller_id,''), COALESCE(request_id,''),
  COALESCE(rendered_command,''), COALESCE(result_json,''),
  COALESCE(artifacts_json,''), COALESCE(diff_summary,''),
  COALESCE(source,''), COALESCE(tags_json,''),
  COALESCE(workflow_id,''), COALESCE(step_index,0),
  COALESCE(attempt,1) FROM jobs`

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
		&r.CallerID, &r.RequestID,
		&r.RenderedCommand, &r.ResultJSON, &r.ArtifactsJSON, &r.DiffSummary,
		&r.Source, &r.TagsJSON,
		&r.WorkflowID, &r.StepIndex, &r.Attempt,
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
   request_json, error, started_at, ended_at, updated_at, caller_id, request_id,
   rendered_command, result_json, artifacts_json, diff_summary, source, tags_json,
   workflow_id, step_index, attempt)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
    updated_at=excluded.updated_at,
    caller_id=excluded.caller_id,
    request_id=excluded.request_id,
    rendered_command=excluded.rendered_command,
    result_json=excluded.result_json,
    artifacts_json=excluded.artifacts_json,
    diff_summary=excluded.diff_summary,
    source=excluded.source,
    tags_json=excluded.tags_json,
    workflow_id=excluded.workflow_id,
    step_index=excluded.step_index,
    attempt=excluded.attempt`
	// Serialise writes in-process (see Store.writeMu) so SQLite never sees two
	// concurrent writers and cannot return SQLITE_BUSY under burst.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(q,
		rec.ID, rec.ProjectKey, rec.Agent, rec.Runner, rec.WorkerID,
		rec.Status, rec.ExitCode, rec.Cwd, rec.ResultDir, rec.RequestJSON,
		rec.Error, rec.StartedAt, rec.EndedAt, rec.UpdatedAt,
		rec.CallerID, rec.RequestID,
		rec.RenderedCommand, rec.ResultJSON, rec.ArtifactsJSON, rec.DiffSummary,
		rec.Source, rec.TagsJSON,
		rec.WorkflowID, rec.StepIndex, rec.Attempt,
	)
	if err != nil {
		// A competing INSERT with the same non-empty request_id (different id)
		// violates the partial unique index. Surface the sentinel directly (not
		// wrapped) so job.Submit can recover via errors.Is and return the winner.
		if isRequestIDConflict(err) {
			return ErrRequestIDConflict
		}
		return fmt.Errorf("jobstore: upsert job %q: %w", rec.ID, err)
	}
	return nil
}

// isRequestIDConflict reports whether err is a SQLite UNIQUE-constraint failure
// on the jobs.request_id partial index (the C5 idempotency race). It matches on
// both the extended result code and the offending column so an unrelated UNIQUE
// failure is never misclassified.
func isRequestIDConflict(err error) bool {
	var serr *sqlite.Error
	return errors.As(err, &serr) &&
		serr.Code() == sqliteConstraintUnique &&
		strings.Contains(serr.Error(), "request_id")
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

// GetJobByRequestID returns the job carrying the given (non-empty) request_id,
// the idempotency lookup for C5. An empty reqID is treated as "no key" and
// returns (zero, false, nil) without touching the DB (matching the partial
// unique index, which does not constrain empty request_id). The bool is false
// (nil error) when no such job exists.
func (s *Store) GetJobByRequestID(reqID string) (JobRecord, bool, error) {
	if reqID == "" {
		return JobRecord{}, false, nil
	}
	rec, err := scanJob(s.db.QueryRow(selectCols+" WHERE request_id = ?", reqID))
	if errors.Is(err, sql.ErrNoRows) {
		return JobRecord{}, false, nil
	}
	if err != nil {
		return JobRecord{}, false, fmt.Errorf("jobstore: get job by request_id %q: %w", reqID, err)
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
	if q.Caller != "" {
		where = append(where, "caller_id = ?")
		args = append(args, q.Caller)
	}
	if q.Tag != "" {
		// 匹配 JSON 数组里的 "<tag>" 元素：含引号避免子串误命中（查 a 不命中 ["ab"]）。
		// 走预编译占位符，tag 值仅作为参数传入，杜绝注入（D2 子串近似可接受）。
		where = append(where, "tags_json LIKE ?")
		args = append(args, "%\""+q.Tag+"\"%")
	}
	if q.Agent != "" {
		where = append(where, "agent = ?")
		args = append(args, q.Agent)
	}
	if q.Runner != "" {
		where = append(where, "runner = ?")
		args = append(args, q.Runner)
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
