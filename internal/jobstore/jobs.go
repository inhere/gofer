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
	Interactive bool
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
	// FanIndex 是 fan-out step 内同一 (step,attempt) 的并行 job 1-based 序号（P2，工作流
	// v2，design §5.3）。FanOut>1 的 step 起 N 个 job，以 FanIndex=1..N 区分；非 fan-out
	// job（含 v1/P1 单 job 路径）为 0。持久化到 jobs.fan_index（旧库/普通 job 经 selectCols
	// COALESCE 成 0）。与 StepIndex/Attempt 一起构成确定性 request_id 的 f<fanIndex> 段，
	// 保证每个 (step,attempt,fan) 只起一个 job（C5 幂等延续）。
	FanIndex int
	// SessionID 是底层 agent CLI 的会话标识（claude/codex 等），注入或捕获得到。空表示
	// 无/未捕获；持久化到 jobs.session_id（旧库经 selectCols COALESCE 成 ""）；与
	// job.JobResult.SessionID 互转，供 show/list/resume 使用。
	SessionID string
	// Channel / Client 是提交来源（provenance）：channel=cli/web/mcp/im（提交渠道），
	// client=来源主机名(CLI)/IP(HTTP)。空表示旧库/未提供（selectCols COALESCE 成 ""）；
	// 与 job.JobResult 互转，配合 CallerID 供 show/list 标识"谁/哪台/经哪渠道提交"。
	Channel string
	Client  string
	// OriginAgent / EscalateTo 是监督分层升级路由（supervisor-routing P1.1）的 owner 路由列：
	// OriginAgent=发起该 job 的主 agent agent_id（owner，L1 escalation 优先回投它），
	// EscalateTo=可选 job 级 escalate 覆盖。空表示旧库/未提供（selectCols COALESCE 成 ""）；
	// 与 job.JobResult 互转。P1.1 仅透传落库，escalate 路由改写在 P1.2。
	OriginAgent string
	EscalateTo  string
	// Role 是该 job 的角色预设名（supervisor-routing P2.2 套娃防护）：监督路由器据此识别
	// "supervisor 自身产生的 interaction"，对其永不自动答/回投 sup（防死循环），直接留 pending
	// 等人（L3）。空表示旧库/未提供（selectCols COALESCE 成 ""）；与 job.JobResult.Role 互转。
	Role string
	// PlanID 是客户端可设的归组键，把此 job 归入某个 plan。区别于引擎私有
	// WorkflowID；空表示不属任何 plan（旧库经 selectCols COALESCE 成 ""）。
	PlanID string
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
	Session string // exact session_id match when non-empty (P3, list --session)
	Plan    string // exact plan_id match when non-empty (plan-orchestration P1)
	Limit   int    // <= 0 => DefaultListLimit
	Offset  int    // skip the first Offset rows (pagination); ignored when <= 0
	Since   int64  // when > 0, keep only jobs with started_at >= Since
}

// selectCols is the shared projection for GetJob/ListJobs. COALESCE guards the
// nullable columns so a NULL (from any future writer) scans into the zero value
// instead of failing the scan into a plain string/int64.
const selectCols = `SELECT id, project_key, agent, runner, COALESCE(interactive,0), COALESCE(worker_id,''),
  status, exit_code, COALESCE(cwd,''), result_dir, COALESCE(request_json,''),
  COALESCE(error,''), started_at, COALESCE(ended_at,0), updated_at,
  COALESCE(caller_id,''), COALESCE(request_id,''),
  COALESCE(rendered_command,''), COALESCE(result_json,''),
  COALESCE(artifacts_json,''), COALESCE(diff_summary,''),
  COALESCE(source,''), COALESCE(tags_json,''),
  COALESCE(workflow_id,''), COALESCE(step_index,0),
  COALESCE(attempt,1), COALESCE(fan_index,0),
  COALESCE(session_id,''), COALESCE(channel,''), COALESCE(client,''),
  COALESCE(origin_agent,''), COALESCE(escalate_to,''),
  COALESCE(role,''), COALESCE(plan_id,'') FROM jobs`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob reads one row (in selectCols order) into a JobRecord.
func scanJob(sc rowScanner) (JobRecord, error) {
	var r JobRecord
	var interactive int
	err := sc.Scan(
		&r.ID, &r.ProjectKey, &r.Agent, &r.Runner, &interactive, &r.WorkerID,
		&r.Status, &r.ExitCode, &r.Cwd, &r.ResultDir, &r.RequestJSON,
		&r.Error, &r.StartedAt, &r.EndedAt, &r.UpdatedAt,
		&r.CallerID, &r.RequestID,
		&r.RenderedCommand, &r.ResultJSON, &r.ArtifactsJSON, &r.DiffSummary,
		&r.Source, &r.TagsJSON,
		&r.WorkflowID, &r.StepIndex, &r.Attempt, &r.FanIndex,
		&r.SessionID, &r.Channel, &r.Client,
		&r.OriginAgent, &r.EscalateTo, &r.Role, &r.PlanID,
	)
	r.Interactive = interactive != 0
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
  (id, project_key, agent, runner, interactive, worker_id, status, exit_code, cwd, result_dir,
   request_json, error, started_at, ended_at, updated_at, caller_id, request_id,
    rendered_command, result_json, artifacts_json, diff_summary, source, tags_json,
    workflow_id, step_index, attempt, fan_index, session_id, channel, client,
    origin_agent, escalate_to, role, plan_id)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(id) DO UPDATE SET
    project_key=excluded.project_key,
    agent=excluded.agent,
    runner=excluded.runner,
    interactive=excluded.interactive,
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
    attempt=excluded.attempt,
    fan_index=excluded.fan_index,
    session_id=excluded.session_id,
    channel=excluded.channel,
    client=excluded.client,
    origin_agent=excluded.origin_agent,
    escalate_to=excluded.escalate_to,
    role=excluded.role,
    plan_id=excluded.plan_id`
	// Serialise writes in-process (see Store.writeMu) so SQLite never sees two
	// concurrent writers and cannot return SQLITE_BUSY under burst.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(q,
		rec.ID, rec.ProjectKey, rec.Agent, rec.Runner, rec.Interactive, rec.WorkerID,
		rec.Status, rec.ExitCode, rec.Cwd, rec.ResultDir, rec.RequestJSON,
		rec.Error, rec.StartedAt, rec.EndedAt, rec.UpdatedAt,
		rec.CallerID, rec.RequestID,
		rec.RenderedCommand, rec.ResultJSON, rec.ArtifactsJSON, rec.DiffSummary,
		rec.Source, rec.TagsJSON,
		rec.WorkflowID, rec.StepIndex, rec.Attempt, rec.FanIndex,
		rec.SessionID, rec.Channel, rec.Client,
		rec.OriginAgent, rec.EscalateTo, rec.Role, rec.PlanID,
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

// nonTerminalJobStatuses are the live states a job can be left in by a serve that
// died / restarted mid-flight. Mirrors the job package's non-terminal set (the
// complement of terminalStatuses). Kept local so jobstore never imports job.
var nonTerminalJobStatuses = []string{"queued", "running"}

// activeJobStatuses are the states a daemon-style job passes through while alive.
// Broader than nonTerminalJobStatuses (adds pending_interaction) because the P4b
// supervisor reconciler counts a sup momentarily blocked on its own interaction as
// still "present" so it is not double-spawned.
var activeJobStatuses = []string{"queued", "running", "pending_interaction"}

// CountActiveJobsByRole returns how many jobs of the given role are currently active
// (status in activeJobStatuses). The P4b supervisor reconciler (supervisor-routing
// P4b) uses it as the SINGLE replica signal: active < desired_supervisors triggers
// re-dispatch. Counting real job rows (not in-memory / not presence) makes it
// idempotent across serve restarts and avoids double-counting a healthy sup as both
// a running job AND an online presence agent.
func (s *Store) CountActiveJobsByRole(role string) (int, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(activeJobStatuses)), ",")
	q := `SELECT COUNT(*) FROM jobs WHERE role = ? AND status IN (` + placeholders + `)`
	args := make([]any, 0, len(activeJobStatuses)+1)
	args = append(args, role)
	for _, st := range activeJobStatuses {
		args = append(args, st)
	}
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("jobstore: count active jobs by role: %w", err)
	}
	return n, nil
}

// CountJobsByStatus returns a status->count map over all jobs.
func (s *Store) CountJobsByStatus() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: count jobs by status: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("jobstore: scan jobs by status: %w", err)
		}
		out[status] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: count jobs by status rows: %w", err)
	}
	return out, nil
}

// ReconcileOrphanJobs marks every job still in a non-terminal state as failed — the
// crash-recovery backstop (mirrors ReconcileOrphanInteractions). A job left
// "queued"/"running" in the store by a previous serve instance can never reach a
// real terminal state on its own: the in-memory orchestration that drove it (the
// dispatch entry / worker sink) did not survive the restart, so even a worker that
// keeps executing has nowhere to report back. Run ONCE at serve startup, before new
// work is accepted, so the in-memory map is empty and no live job can be misclassified.
// ts stamps ended_at/updated_at; reason is recorded in the error column. Returns rows fixed.
func (s *Store) ReconcileOrphanJobs(ts int64, reason string) (int, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(nonTerminalJobStatuses)), ",")
	q := `UPDATE jobs SET status = 'failed', error = ?, ended_at = ?, updated_at = ?
  WHERE status IN (` + placeholders + `)`
	args := make([]any, 0, len(nonTerminalJobStatuses)+3)
	args = append(args, reason, ts, ts)
	for _, st := range nonTerminalJobStatuses {
		args = append(args, st)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("jobstore: reconcile orphan jobs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: reconcile orphan jobs rows: %w", err)
	}
	return int(n), nil
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
	if q.Session != "" {
		// session_id 精确等于（不同于 Tag 的 LIKE 元素匹配）：一个 session_id 唯一标识
		// 一条 agent 会话链，用于 list --session 列出某会话的所有 turn (P3)。
		where = append(where, "session_id = ?")
		args = append(args, q.Session)
	}
	if q.Plan != "" {
		where = append(where, "plan_id = ?")
		args = append(args, q.Plan)
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
