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
	// ReadOnly (bd h-aii-0ql3) records whether the job ran under a read-only sandbox
	// (cli-agent read_only_args / acp-agent session/set_mode). Persisted so a finished
	// job still answers "was this run allowed to write?".
	ReadOnly bool
	// RequireReview / ReviewedBy / ReviewedAt / ReviewNote are the人工验收 (GATE-01
	// S3) audit fields: whether the job was gated on a human's accept/reject, and —
	// once that decision exists — who made it, when, and why. They are persisted so a
	// reviewed job still explains itself long after its process ended.
	RequireReview bool
	ReviewedBy    string
	ReviewedAt    int64
	ReviewNote    string
	WorkerID      string // reserved for ws-worker; empty for local/peer jobs
	// WorkerInstanceID is the process nonce (wsproto.Register.InstanceID) of the
	// worker connection the job was dispatched to (RECOV-01 R4). Together with
	// WorkerID it proves WHICH worker process owns the job, so a hub starting after a
	// serve restart can ADOPT a store-held `recovering` job only when the
	// re-registering process is the very one that was running it. Empty for
	// local/peer jobs and for rows written before R4 (which are never adopted).
	WorkerInstanceID string
	Status           string
	ExitCode         int
	Cwd              string
	ResultDir        string // per-job log/artifact directory (logs stay on disk)
	RequestJSON      string // original JobRequest JSON, for re-submit/audit
	Error            string
	StartedAt        int64
	EndedAt          int64
	UpdatedAt        int64
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
	// NDJSONKept / NDJSONDropped / NDJSONTruncated 是采集期 NDJSON 投影器
	// （bd h-aii-rpky / bd h-aii-525u）的行数审计：stderr.log 写入/丢弃/截断的行数。
	// 旧行 COALESCE 成 0（文本 agent 也是 0，同义）。
	NDJSONKept      int
	NDJSONDropped   int
	NDJSONTruncated int
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
	SessionID         string
	ResumedFrom       string
	AutoResumeAttempt int
	AutoResumedBy     string
	// StopReason is the acp-agent session/prompt stopReason (ACP-01 S0). Empty for
	// every other agent type and for rows written before the column existed
	// (selectCols COALESCEs it to "").
	StopReason string
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
	// TodoID 是客户端可设的 checklist 键（SUP-01 C）：把此 job 挂到某个 plan todo 上，
	// 终态时由 hub 把结果写回该 todo 的 note/status。空表示不挂 todo（旧库 COALESCE→""）。
	// 与 job.JobResult.TodoID 互转；反查 ListJobsByTodo。
	TodoID string
	// BaseSHA / CommitsJSON 是提交采集（SUP-01 C）：BaseSHA=执行机在 job 开跑时的 HEAD
	// （worktree job 即其基线），CommitsJSON=终态时 base..HEAD 的提交列表（JSON 数组，
	// 新→旧）。非 git 仓/采集失败留空（旧库 COALESCE→""）；与 job.JobResult 互转。
	BaseSHA     string
	CommitsJSON string
	// SourceJobID 是血缘键（P5）：resume/rebuild 出的 job 指回源 job id（服务端盖章）。空=非
	// 派生（旧库 COALESCE→""）。与 job.JobResult.SourceJobID 互转；反查 ?source_job=。
	// 注意区别既有 Source 列（执行位置 worker:/peer:）。
	SourceJobID string
	// RecoveringSince is the unix time the job entered the RECOV-01 `recovering`
	// state (0 = never / not recovering). Old rows COALESCE to 0, which reads as
	// "not recovering" — exactly the pre-RECOV-01 meaning.
	RecoveringSince int64
	// TimeoutSec / RequestedTimeoutSec / TimeoutClamped 是 job 超时上限可配（bd h-aii-s9ck）
	// 的三元组：生效 deadline 秒数（0=无 deadline）、调用方请求值（0=未指定）、请求是否被上限
	// 截断。旧库/旧 job 经 selectCols COALESCE 成 0/0/false，含义是"未记录"，不会被误判成
	// "被截断"。与 job.JobResult 同名字段互转（post-mortem 据此解释 job 为何提前被杀）。
	TimeoutSec          int
	RequestedTimeoutSec int
	TimeoutClamped      bool
	// WT-01 受管 worktree：job 在 <repo top>/tmp/gofer/wt/<job-id> 的独立 worktree 中执行，
	// 提交落在分支 gofer/<job-id> 上。WorktreePath/Branch 是交付物位置，BaseSHA=基线提交，
	// HeadSHA/CommitsAhead=终态时该分支的 HEAD 与领先基线的提交数。非 worktree job 为空/0
	// （旧库经 selectCols COALESCE 成 ""/0）；与 job.JobResult 同名字段互转，供 job show /
	// web 详情 / worktree ls 使用。
	WorktreePath    string
	WorktreeBranch  string
	WorktreeBaseSHA string
	WorktreeHeadSHA string
	CommitsAhead    int
}

// ListQuery filters/bounds a ListJobs query. A zero value lists every project's
// jobs (no status filter), newest first, capped at DefaultListLimit.
type ListQuery struct {
	Project   string // exact project_key match when non-empty
	Status    string // exact status match when non-empty
	Caller    string // exact caller_id match when non-empty (C2)
	Tag       string // tags_json contains this tag element when non-empty (E5)
	Agent     string // exact agent match when non-empty (E5)
	Runner    string // exact runner match when non-empty (E5)
	Session   string // exact session_id match when non-empty (P3, list --session)
	Plan      string // exact plan_id match when non-empty (plan-orchestration P1)
	SourceJob string // exact source_job_id match when non-empty (P5, list ?source_job=)
	Limit     int    // <= 0 => DefaultListLimit
	Offset    int    // skip the first Offset rows (pagination); ignored when <= 0
	Since     int64  // when > 0, keep only jobs with started_at >= Since
}

// WorktreeRecord is the WT-01 projection of one job's managed worktree: the row
// fields the retention sweep and `job worktree ls` need, without pulling a whole
// JobRecord. Empty Path means the job has no managed worktree.
type WorktreeRecord struct {
	JobID      string
	ProjectKey string
	Path       string
	Branch     string
	BaseSHA    string
}

// ListWorktrees returns every job row that records a managed worktree (WT-01),
// newest first. The retention sweep snapshots these BEFORE pruning so it can tell
// which worktrees lost their row (PruneJobs returns result dirs, not ids) and clean
// up the merged ones.
func (s *Store) ListWorktrees() ([]WorktreeRecord, error) {
	rows, err := s.db.Query(`SELECT id, project_key, COALESCE(worktree_path,''),
  COALESCE(worktree_branch,''), COALESCE(worktree_base_sha,'')
  FROM jobs WHERE COALESCE(worktree_path,'') <> '' ORDER BY started_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list worktrees: %w", err)
	}
	defer rows.Close()
	var out []WorktreeRecord
	for rows.Next() {
		var r WorktreeRecord
		if err := rows.Scan(&r.JobID, &r.ProjectKey, &r.Path, &r.Branch, &r.BaseSHA); err != nil {
			return nil, fmt.Errorf("jobstore: scan worktree: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// selectCols is the shared projection for GetJob/ListJobs. COALESCE guards the
// nullable columns so a NULL (from any future writer) scans into the zero value
// instead of failing the scan into a plain string/int64.
const selectCols = `SELECT id, project_key, agent, runner, COALESCE(interactive,0), COALESCE(worker_id,''),
  COALESCE(worker_instance_id,''),
  status, exit_code, COALESCE(cwd,''), result_dir, COALESCE(request_json,''),
  COALESCE(error,''), started_at, COALESCE(ended_at,0), updated_at,
  COALESCE(caller_id,''), COALESCE(request_id,''),
  COALESCE(rendered_command,''), COALESCE(result_json,''),
  COALESCE(artifacts_json,''), COALESCE(diff_summary,''),
  COALESCE(ndjson_kept,0), COALESCE(ndjson_dropped,0), COALESCE(ndjson_truncated,0),
  COALESCE(source,''), COALESCE(tags_json,''),
  COALESCE(workflow_id,''), COALESCE(step_index,0),
	COALESCE(attempt,1), COALESCE(fan_index,0),
	COALESCE(session_id,''), COALESCE(stop_reason,''), COALESCE(resumed_from,''), COALESCE(auto_resume_attempt,0), COALESCE(auto_resumed_by,''), COALESCE(channel,''), COALESCE(client,''),
	COALESCE(origin_agent,''), COALESCE(escalate_to,''),
  COALESCE(role,''), COALESCE(plan_id,''), COALESCE(source_job_id,''),
  COALESCE(todo_id,''), COALESCE(base_sha,''), COALESCE(commits_json,''),
  COALESCE(timeout_sec,0), COALESCE(requested_timeout_sec,0), COALESCE(timeout_clamped,0),
  COALESCE(recovering_since,0),
  COALESCE(worktree_path,''), COALESCE(worktree_branch,''), COALESCE(worktree_base_sha,''),
  COALESCE(worktree_head_sha,''), COALESCE(commits_ahead,0), COALESCE(read_only,0),
  COALESCE(require_review,0), COALESCE(reviewed_by,''), COALESCE(reviewed_at,0), COALESCE(review_note,'') FROM jobs`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob reads one row (in selectCols order) into a JobRecord.
func scanJob(sc rowScanner) (JobRecord, error) {
	var r JobRecord
	var interactive, timeoutClamped, readOnly, requireReview int
	err := sc.Scan(
		&r.ID, &r.ProjectKey, &r.Agent, &r.Runner, &interactive, &r.WorkerID,
		&r.WorkerInstanceID,
		&r.Status, &r.ExitCode, &r.Cwd, &r.ResultDir, &r.RequestJSON,
		&r.Error, &r.StartedAt, &r.EndedAt, &r.UpdatedAt,
		&r.CallerID, &r.RequestID,
		&r.RenderedCommand, &r.ResultJSON, &r.ArtifactsJSON, &r.DiffSummary,
		&r.NDJSONKept, &r.NDJSONDropped, &r.NDJSONTruncated,
		&r.Source, &r.TagsJSON,
		&r.WorkflowID, &r.StepIndex, &r.Attempt, &r.FanIndex,
		&r.SessionID, &r.StopReason, &r.ResumedFrom, &r.AutoResumeAttempt, &r.AutoResumedBy, &r.Channel, &r.Client,
		&r.OriginAgent, &r.EscalateTo, &r.Role, &r.PlanID, &r.SourceJobID,
		&r.TodoID, &r.BaseSHA, &r.CommitsJSON,
		&r.TimeoutSec, &r.RequestedTimeoutSec, &timeoutClamped,
		&r.RecoveringSince,
		&r.WorktreePath, &r.WorktreeBranch, &r.WorktreeBaseSHA,
		&r.WorktreeHeadSHA, &r.CommitsAhead, &readOnly,
		&requireReview, &r.ReviewedBy, &r.ReviewedAt, &r.ReviewNote,
	)
	r.Interactive = interactive != 0
	r.TimeoutClamped = timeoutClamped != 0
	r.ReadOnly = readOnly != 0
	r.RequireReview = requireReview != 0
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
  (id, project_key, agent, runner, interactive, worker_id, worker_instance_id, status, exit_code, cwd, result_dir,
   request_json, error, started_at, ended_at, updated_at, caller_id, request_id,
	    rendered_command, result_json, artifacts_json, diff_summary, ndjson_kept, ndjson_dropped, ndjson_truncated, source, tags_json,
	    workflow_id, step_index, attempt, fan_index, session_id, stop_reason, resumed_from, auto_resume_attempt, auto_resumed_by, channel, client,
	    origin_agent, escalate_to, role, plan_id, source_job_id,
	    todo_id, base_sha, commits_json,
	    timeout_sec, requested_timeout_sec, timeout_clamped, recovering_since,
	    worktree_path, worktree_branch, worktree_base_sha, worktree_head_sha, commits_ahead, read_only,
	    require_review, reviewed_by, reviewed_at, review_note)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(id) DO UPDATE SET
    project_key=excluded.project_key,
    agent=excluded.agent,
    runner=excluded.runner,
    interactive=excluded.interactive,
    worker_id=excluded.worker_id,
    worker_instance_id=excluded.worker_instance_id,
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
    ndjson_kept=excluded.ndjson_kept,
    ndjson_dropped=excluded.ndjson_dropped,
    ndjson_truncated=excluded.ndjson_truncated,
    source=excluded.source,
    tags_json=excluded.tags_json,
    workflow_id=excluded.workflow_id,
    step_index=excluded.step_index,
    attempt=excluded.attempt,
    fan_index=excluded.fan_index,
	    session_id=excluded.session_id,
    stop_reason=excluded.stop_reason,
    resumed_from=excluded.resumed_from,
    auto_resume_attempt=excluded.auto_resume_attempt,
    auto_resumed_by=excluded.auto_resumed_by,
    channel=excluded.channel,
    client=excluded.client,
	    origin_agent=excluded.origin_agent,
	    escalate_to=excluded.escalate_to,
	    role=excluded.role,
    plan_id=excluded.plan_id,
    source_job_id=excluded.source_job_id,
    todo_id=excluded.todo_id,
    base_sha=excluded.base_sha,
    commits_json=excluded.commits_json,
    timeout_sec=excluded.timeout_sec,
    requested_timeout_sec=excluded.requested_timeout_sec,
    timeout_clamped=excluded.timeout_clamped,
    recovering_since=excluded.recovering_since,
    worktree_path=excluded.worktree_path,
    worktree_branch=excluded.worktree_branch,
    worktree_base_sha=excluded.worktree_base_sha,
    worktree_head_sha=excluded.worktree_head_sha,
    commits_ahead=excluded.commits_ahead,
    read_only=excluded.read_only,
    require_review=excluded.require_review,
    reviewed_by=excluded.reviewed_by,
    reviewed_at=excluded.reviewed_at,
    review_note=excluded.review_note`
	// Serialise writes in-process (see Store.writeMu) so SQLite never sees two
	// concurrent writers and cannot return SQLITE_BUSY under burst.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(q,
		rec.ID, rec.ProjectKey, rec.Agent, rec.Runner, rec.Interactive, rec.WorkerID,
		rec.WorkerInstanceID,
		rec.Status, rec.ExitCode, rec.Cwd, rec.ResultDir, rec.RequestJSON,
		rec.Error, rec.StartedAt, rec.EndedAt, rec.UpdatedAt,
		rec.CallerID, rec.RequestID,
		rec.RenderedCommand, rec.ResultJSON, rec.ArtifactsJSON, rec.DiffSummary,
		rec.NDJSONKept, rec.NDJSONDropped, rec.NDJSONTruncated,
		rec.Source, rec.TagsJSON,
		rec.WorkflowID, rec.StepIndex, rec.Attempt, rec.FanIndex,
		rec.SessionID, rec.StopReason, rec.ResumedFrom, rec.AutoResumeAttempt, rec.AutoResumedBy, rec.Channel, rec.Client,
		rec.OriginAgent, rec.EscalateTo, rec.Role, rec.PlanID, rec.SourceJobID,
		rec.TodoID, rec.BaseSHA, rec.CommitsJSON,
		rec.TimeoutSec, rec.RequestedTimeoutSec, rec.TimeoutClamped,
		rec.RecoveringSince,
		rec.WorktreePath, rec.WorktreeBranch, rec.WorktreeBaseSHA,
		rec.WorktreeHeadSHA, rec.CommitsAhead, rec.ReadOnly,
		rec.RequireReview, rec.ReviewedBy, rec.ReviewedAt, rec.ReviewNote,
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

// nonTerminalJobStatuses are the live states a LOCAL job can be left in by a serve
// that died / restarted mid-flight — exactly the ones ReconcileOrphanJobs fails
// outright, because no in-process orchestration survived to ever finish them. It
// deliberately excludes `recovering`: only a WORKER job ever enters that state, and
// such jobs are held (not failed) via orphanWorkerJobStatuses instead. Kept local so
// jobstore never imports job.
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

// supervisedJobStatuses are the states that mean "this caller has work in
// flight": every live state (activeJobStatuses) plus `recovering`, a worker job
// held while its worker reconnects — the work is not finished, only paused. A
// job parked in `needs_review` is deliberately EXCLUDED: the agent is done, the
// human is reviewing a delivery, not supervising a run (SUP-01 D).
var supervisedJobStatuses = []string{"queued", "running", "pending_interaction", "recovering"}

// CountActiveJobsByCaller counts the caller's jobs that are still in flight and
// were submitted at or after `since` (unix seconds; 0 = no lower bound). It is
// the SUP-01 D supervision signal: the relay refuses to auto-arm a session whose
// caller is demonstrably watching live work, because an armed Stop would block
// the hook (and the job's completion notice) until the human came back. An empty
// callerID counts nothing — there is no one to attribute the work to.
func (s *Store) CountActiveJobsByCaller(callerID string, since int64) (int, error) {
	if callerID == "" {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(supervisedJobStatuses)), ",")
	q := `SELECT COUNT(*) FROM jobs WHERE caller_id = ? AND started_at >= ? AND status IN (` + placeholders + `)`
	args := make([]any, 0, len(supervisedJobStatuses)+2)
	args = append(args, callerID, since)
	for _, st := range supervisedJobStatuses {
		args = append(args, st)
	}
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("jobstore: count active jobs by caller: %w", err)
	}
	return n, nil
}

// ListJobsByTodo returns the jobs attached to one plan todo (jobs.todo_id),
// newest first, capped at limit (<= 0 means DefaultListLimit). It is what
// `plan show` / GET /v1/plans/{id} list under a todo: a todo can be carried by
// several runs (retries, re-runs, continuations), and the most recent one is the
// interesting one.
func (s *Store) ListJobsByTodo(todoID string, limit int) ([]JobRecord, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	rows, err := s.db.Query(selectCols+` WHERE todo_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`, todoID, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list jobs by todo: %w", err)
	}
	defer rows.Close()
	out := make([]JobRecord, 0)
	for rows.Next() {
		rec, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan job by todo: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list jobs by todo rows: %w", err)
	}
	return out, nil
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

// orphanWorkerJobStatuses are the states a WORKER job can be left in by a serve
// process that died / restarted mid-flight: the two live states, plus `recovering`
// itself — a row held by an earlier run of the recovery window whose serve then
// died too. Those are re-held (window re-armed by the caller) rather than left
// stranded, so the re-armed window still ends them.
var orphanWorkerJobStatuses = []string{"queued", "running", "recovering"}

// ReconcileOrphanJobs resolves every job left non-terminal by a previous serve
// instance — the crash-recovery backstop (mirrors ReconcileOrphanInteractions).
// Run ONCE at serve startup, before new work is accepted, so the in-memory job map
// is empty and no live job can be misclassified. Two outcomes (RECOV-01):
//
//   - worker job: held in `recovering` with recovering_since=ts and an explanatory
//     error. A worker job is a row whose worker_id is non-empty OR whose runner is
//     one of workerRunners (the worker-type runner names the caller resolved from
//     config). The runner half is load-bearing: a runner=worker job whose worker_id
//     was empty on the request (the D4 "runner's configured default worker" fallback,
//     resolved at dispatch) used to be misclassified as a local job and failed at
//     once. The worker process may still be RUNNING the job (its job.Service is
//     process-scoped, so a worker-side job survives a serve restart), and serve arms
//     the recovery window so the same process can reconnect and be ADOPTED (R4: the
//     hub rebuilds a sink for a store-held recovering row whose worker_instance_id
//     matches the re-registering process). Rows with no recorded instance are still
//     held for the window and then failed (they can never be adopted).
//   - every other non-terminal job (local runner / peer-http): failed, as before —
//     its in-process state really is gone.
//
// ts stamps updated_at (+ ended_at on the failed rows); reason is recorded in the
// error column (the recovering rows prefix it and append the worker id). Returns the
// total rows touched (held + failed), so the caller's startup log reports both.
func (s *Store) ReconcileOrphanJobs(ts int64, reason string, workerRunners []string) (int, error) {
	// 1) Hold worker jobs in `recovering` for the (re-armed) recovery window. The
	// worker predicate is (worker_id <> '' OR runner IN workerRunners); the runner
	// placeholders are built only when the caller resolved any, so an empty list
	// degrades to exactly the column test.
	heldPlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(orphanWorkerJobStatuses)), ",")
	workerPred := "worker_id <> ''"
	heldArgs := make([]any, 0, len(orphanWorkerJobStatuses)+len(workerRunners)+3)
	heldArgs = append(heldArgs, ts, "recovering: "+reason+" — awaiting worker ", ts)
	heldArgs = append(heldArgs, workerRunnersToAny(workerRunners)...)
	if len(workerRunners) > 0 {
		workerPred += " OR runner IN (" + strings.TrimSuffix(strings.Repeat("?,", len(workerRunners)), ",") + ")"
	}
	heldQ := `UPDATE jobs SET status = 'recovering', recovering_since = ?, error = ? || worker_id, updated_at = ?
  WHERE (` + workerPred + `) AND status IN (` + heldPlaceholders + `)`
	for _, st := range orphanWorkerJobStatuses {
		heldArgs = append(heldArgs, st)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	heldRes, err := s.db.Exec(heldQ, heldArgs...)
	if err != nil {
		return 0, fmt.Errorf("jobstore: hold orphan worker jobs: %w", err)
	}
	held, err := heldRes.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: hold orphan worker jobs rows: %w", err)
	}

	// 2) Fail every remaining non-terminal job (local/peer: no state survived the
	// restart). A worker row held in step 1 is now `recovering`, which is NOT in
	// nonTerminalJobStatuses, so it is not touched again here.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(nonTerminalJobStatuses)), ",")
	q := `UPDATE jobs SET status = 'failed', error = ?, ended_at = ?, updated_at = ?
  WHERE status IN (` + placeholders + `)`
	args := make([]any, 0, len(nonTerminalJobStatuses)+3)
	args = append(args, reason, ts, ts)
	for _, st := range nonTerminalJobStatuses {
		args = append(args, st)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("jobstore: reconcile orphan jobs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: reconcile orphan jobs rows: %w", err)
	}
	return int(held) + int(n), nil
}

// workerRunnersToAny widens a runner-name list into the []any the driver binds.
func workerRunnersToAny(names []string) []any {
	out := make([]any, 0, len(names))
	for _, n := range names {
		out = append(out, n)
	}
	return out
}

// ListRecoveringJobs returns every job the store holds in `recovering` for workerID
// (all workers when workerID is empty). It is the RECOV-01 R4 adoption source: after
// a serve restart the new hub has no in-memory recovery set, so the rows are the only
// record of what a reconnecting worker process may still be running.
func (s *Store) ListRecoveringJobs(workerID string) ([]JobRecord, error) {
	q := selectCols + " WHERE status = 'recovering'"
	var args []any
	if workerID != "" {
		q += " AND worker_id = ?"
		args = append(args, workerID)
	}
	q += " ORDER BY id"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list recovering jobs: %w", err)
	}
	defer rows.Close()

	var out []JobRecord
	for rows.Next() {
		rec, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan recovering job row: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list recovering jobs rows: %w", err)
	}
	return out, nil
}

// FailRecoveringJob fails ONE store-held recovering job: the worker process
// reconnected but did not prove it still runs this job (absent from its inflight
// report, or the row belongs to a different process instance), so RECOV-01 R4 ends it
// right away instead of leaving it held for a window that can no longer adopt it.
// The `status = 'recovering'` guard makes it a no-op for a row that has meanwhile
// become terminal (or was adopted). reason goes in the error column; ts stamps
// ended_at/updated_at. Returns rows affected (0 or 1).
func (s *Store) FailRecoveringJob(id string, ts int64, reason string) (int, error) {
	q := `UPDATE jobs SET status = 'failed', error = ?, ended_at = ?, updated_at = ?
  WHERE id = ? AND status = 'recovering'`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(q, reason, ts, ts, id)
	if err != nil {
		return 0, fmt.Errorf("jobstore: fail recovering job %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: fail recovering job %q rows: %w", id, err)
	}
	return int(n), nil
}

// CountRecoveringJobs returns how many jobs are currently held in `recovering`. It is
// the RECOV-01 R4 check behind serve's one-shot startup window: once every held row
// has been ADOPTED (back to `running`) there is nothing left for the window to end, so
// the timer is cancelled instead of failing adopted work.
func (s *Store) CountRecoveringJobs() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status = 'recovering'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("jobstore: count recovering jobs: %w", err)
	}
	return n, nil
}

// FailRecoveringJobs fails every job still held in `recovering`: its reconnect
// window elapsed (or no window was armed at all) without the worker coming back, so
// the job can never reach a real terminal state. Called by serve when the RECOV-01
// window armed by ReconcileOrphanJobs expires. ts stamps ended_at/updated_at; reason
// (which should name the cause, e.g. "worker lost") goes in the error column so
// CLI/web show WHY the job ended. Returns rows failed.
func (s *Store) FailRecoveringJobs(ts int64, reason string) (int, error) {
	q := `UPDATE jobs SET status = 'failed', error = ?, ended_at = ?, updated_at = ?
  WHERE status = 'recovering'`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(q, reason, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("jobstore: fail recovering jobs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: fail recovering jobs rows: %w", err)
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
	if q.SourceJob != "" {
		where = append(where, "source_job_id = ?")
		args = append(args, q.SourceJob)
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
