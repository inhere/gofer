# P1 数据底座（实施计划，代码级）

> 主纲：[plan-orchestration-plan.md](./plan-orchestration-plan.md) ｜ 设计：[docs/design/2026-07-09-plan-orchestration-design.md](../../design/2026-07-09-plan-orchestration-design.md) §5/§11
> 触点均已实测定位（2026-07-09 只读探查）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿：代码级任务分解 T1..T7 |

## 范围

数据底座 + 三通道最小闭环：
- `jobs` 表加 `plan_id` **客户端可设**列（+索引）；`JobRequest/JobResult.PlanID`；submit 落库；list `--plan` 过滤（jobstore / job.Service / HTTP / client / CLI 逐层打通）。
- `plans` 表 + jobstore CRUD（Insert/Get/List/UpdateStatus/AttachJob）。
- `POST /v1/plans`（建计划）+ `GET /v1/plans` + `GET /v1/plans/{id}`（P1 返回 header + 其下 job 列表，聚合计数留 P2）+ `POST /v1/plans/{id}/jobs`（attach 已有 job）。
- `gofer plan` CLI（create / list / show / attach）+ client 方法。

**不含**（后续期）：MCP plan 工具面（P2）、`GetPlan` 状态聚合计数（P2）、`plan_todos`（P3）、前端 / session 续跑 UI（P4）、workflow 整体归入 plan（设计 §12 待确认 2，后续）。

## 关键约束回顾（总纲 C1..C5）

- `plan_id` 客户端可设（`json`+`yaml`，**非** `"-"`）；jobstore 不 import job（id 生成放 httpapi）；加列走 `migrate() add`；索引放 `migrate()`（ALTER 之后）；入口层只透传，httpapi 经 `s.jobs.Meta()` 拿 `*jobstore.Store`。

---

## T1. jobstore：`jobs` 表加 `plan_id` 列 + 索引

### T1.1 CREATE TABLE 加列（新库）+ migrate ALTER（旧库）

**现状（`internal/jobstore/store.go:84-89`，`jobs` 建表尾部）**：
```go
  session_id       TEXT,
  channel          TEXT,
  client           TEXT,
  origin_agent     TEXT,
  escalate_to      TEXT,
  role             TEXT
)`,
```
**改为**（在 `role` 后加 `plan_id`；注意保持列在建表内，新库直接有该列）：
```go
  origin_agent     TEXT,
  escalate_to      TEXT,
  role             TEXT,
  plan_id          TEXT
)`,
```

**现状（`store.go:412-414`，migrate 末尾 role 的 add 之后、`migrateWorkflows()` 之前）**：
```go
	if err := add("role", "role TEXT"); err != nil { // job 角色预设（套娃判定）
		return err
	}
	if err := s.migrateWorkflows(); err != nil {
```
**在 `add("role",...)` 之后插入**（旧库 ALTER；`plan` 归组键，客户端可设，区别引擎私有 workflow_id）：
```go
	// plan 编排（plan-orchestration P1）：客户端可设的归组键，把陆续产生的独立 job
	// 归到一个计划（区别于引擎私有 workflow_id）。旧库 ALTER ADD，旧行 selectCols COALESCE→""。
	if err := add("plan_id", "plan_id TEXT"); err != nil {
		return err
	}
```

### T1.2 plan_id 索引（migrate 内，ALTER 之后）

**现状（`store.go:426-430`，request_id 部分唯一索引，migrate 末尾）**：
```go
	if _, err := s.db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_request_id ON jobs(request_id) WHERE request_id <> ''`,
	); err != nil {
		return fmt.Errorf("jobstore: migrate request_id index: %w", err)
	}
	return nil
```
**在 `return nil` 前插入**（普通索引，非唯一——一个 plan 下多个 job；`plan_id` 列此时已存在，故放 migrate 而非 schemaStmts，见 C4）：
```go
	// plan_id 归组过滤索引（list --plan）。列在上面 ALTER 后已存在，故索引建在此处
	// （不放 schemaStmts：applySchema 先于 migrate，旧库彼时无 plan_id 列）。IF NOT EXISTS 幂等。
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_jobs_plan_id ON jobs(plan_id)`,
	); err != nil {
		return fmt.Errorf("jobstore: migrate plan_id index: %w", err)
	}
```

### T1.3 JobRecord 字段 + selectCols + scanJob + UpsertJob

**`internal/jobstore/jobs.go`**：

(a) `JobRecord` struct（`jobs.go:95-99`，`Role` 字段后）加：
```go
	// PlanID 是客户端可设的归组键（plan-orchestration P1）：把此 job 归入某个 plan。
	// 区别于引擎私有 WorkflowID——PlanID 允许客户端设置。空表示不属任何 plan（旧库/普通 job
	// 经 selectCols COALESCE 成 ""）。持久化到 jobs.plan_id；与 job.JobResult.PlanID 互转。
	PlanID string
```

(b) `selectCols`（`jobs.go:119-130`）末尾 `COALESCE(role,'')` 后追加 `COALESCE(plan_id,'')`：
```go
  COALESCE(origin_agent,''), COALESCE(escalate_to,''),
  COALESCE(role,''), COALESCE(plan_id,'') FROM jobs`
```

(c) `scanJob`（`jobs.go:141-151`）Scan 目标末尾 `&r.Role` 后加 `&r.PlanID`：
```go
		&r.OriginAgent, &r.EscalateTo, &r.Role, &r.PlanID,
```

(d) `UpsertJob` INSERT（`jobs.go:168-207`）：
- 列名清单加 `plan_id`（`role` 后）；
- VALUES 占位符 `(?,?,...)` **加一个 `?`**（当前 33 个 → 34 个）；
- `ON CONFLICT DO UPDATE SET` 末尾加 `plan_id=excluded.plan_id`。

(e) `UpsertJob` Exec 参数（`jobs.go:212-222`）末尾 `rec.Role` 后加 `rec.PlanID`：
```go
		rec.OriginAgent, rec.EscalateTo, rec.Role, rec.PlanID,
```

> ⚠️ 占位符个数：改后 INSERT 列数 = VALUES `?` 数 = Exec 参数个数，三者必须一致（当前 33，加 plan_id 后 34）。实施后数一遍。

### T1.4 ListQuery.Plan + ListJobs where

**`internal/jobstore/jobs.go`**：

(a) `ListQuery`（`jobs.go:103-114`）`Session` 后加：
```go
	Plan    string // exact plan_id match when non-empty (plan-orchestration P1, list --plan)
```

(b) `ListJobs` where 拼装（`jobs.go:396-401`，仿 session 精确等于块，紧随其后）：
```go
	if q.Plan != "" {
		// plan_id 精确等于（仿 Session）：列出某个 plan 归组下的所有 job（list --plan）。
		where = append(where, "plan_id = ?")
		args = append(args, q.Plan)
	}
```

**验收**：`go test ./internal/jobstore/...` 绿；新增一条 store 单测：Upsert 两个 job（`plan_id="plan-x"` / `""`）→ `ListJobs{Plan:"plan-x"}` 只回前者；`GetJob` 回读 `PlanID` 正确。

---

## T2. jobstore：`plans` 表 + CRUD（新文件 `internal/jobstore/plans.go`）

### T2.1 建表（`store.go` schemaStmts）

**现状（`store.go:246-263`，pty_sessions 表 + 索引，schemaStmts 末尾）** 之后、`}` 收尾前插入：
```go
	// plans is the plan-orchestration grouping header (plan-orchestration P1, design
	// §5.1). One row per plan: a lightweight DYNAMIC grouping container (区别于 workflows
	// 的静态串行推进) that job rows join via jobs.plan_id. status: open/active/done/archived
	// (SR301 从有意义值起). owner 是创建者（caller_id / agent_id）. progress 0..100 是可选
	// 人工进度（jobs 状态聚合在查询期实时算，见 P2，不落此列）. Times unix seconds. IF NOT
	// EXISTS like every table here (idempotent Open) — 新表无需 migrate() ALTER。
	`CREATE TABLE IF NOT EXISTS plans (
  plan_id      TEXT PRIMARY KEY,
  title        TEXT,
  description  TEXT,
  status       TEXT NOT NULL,
  owner        TEXT,
  progress     INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_plans_status ON plans(status)`,
```

### T2.2 CRUD（新 `internal/jobstore/plans.go`，仿 `workflows.go`）

```go
package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// Plan status values (plan-orchestration P1, design §6). open=刚建、active=有 job 在跑、
// done=其下 job 全终态、archived=归档。SR301 从有意义值起（不使用空/0）。
const (
	PlanOpen     = "open"
	PlanActive   = "active"
	PlanDone     = "done"
	PlanArchived = "archived"
)

// Plan is the SQLite-persisted plan grouping header (design §5.1). Neutral struct
// (no internal/job import, C3) so the job/http layers drive it without an import cycle.
// Progress is an OPTIONAL 人工进度 (0..100); the jobs 状态聚合 is computed at query time
// (P2), not stored here.
type Plan struct {
	PlanID      string
	Title       string
	Description string
	Status      string
	Owner       string
	Progress    int
	CreatedAt   int64
	UpdatedAt   int64
}

// selectPlanCols COALESCE-guards the nullable text columns so a NULL scans into ""
// instead of failing (mirrors selectWorkflowCols).
const selectPlanCols = `SELECT plan_id, COALESCE(title,''), COALESCE(description,''),
  status, COALESCE(owner,''), COALESCE(progress,0), created_at, updated_at FROM plans`

func scanPlan(sc rowScanner) (Plan, error) {
	var p Plan
	err := sc.Scan(&p.PlanID, &p.Title, &p.Description, &p.Status, &p.Owner,
		&p.Progress, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// InsertPlan persists a new plan header. plan_id must be non-empty (the caller
// generates it — jobstore stays job-import-free, C3). created_at/updated_at are
// taken as given (clock-free store). Runs under writeMu like every writer.
func (s *Store) InsertPlan(p Plan) error {
	if p.PlanID == "" {
		return errors.New("jobstore: InsertPlan: empty plan id")
	}
	if p.Status == "" {
		p.Status = PlanOpen
	}
	const q = `INSERT INTO plans
  (plan_id, title, description, status, owner, progress, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, p.PlanID, p.Title, p.Description, p.Status, p.Owner,
		p.Progress, p.CreatedAt, p.UpdatedAt); err != nil {
		return fmt.Errorf("jobstore: insert plan %q: %w", p.PlanID, err)
	}
	return nil
}

// GetPlan returns the plan by id; bool false (nil err) when absent (仿 GetWorkflow).
func (s *Store) GetPlan(id string) (Plan, bool, error) {
	p, err := scanPlan(s.db.QueryRow(selectPlanCols+" WHERE plan_id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, false, nil
	}
	if err != nil {
		return Plan{}, false, fmt.Errorf("jobstore: get plan %q: %w", id, err)
	}
	return p, true, nil
}

// ListPlans returns plans, optionally filtered by status (exact when non-empty),
// newest first, capped at limit (<=0 => DefaultListLimit). 仿 ListWorkflows。
func (s *Store) ListPlans(status string, limit int) ([]Plan, error) {
	query := selectPlanCols
	var args []any
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC, plan_id DESC"
	if limit <= 0 {
		limit = DefaultListLimit
	}
	query += " LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list plans: %w", err)
	}
	defer rows.Close()
	out := make([]Plan, 0)
	for rows.Next() {
		p, scanErr := scanPlan(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan plan row: %w", scanErr)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPlanStatus moves a plan to status (+optional progress) and stamps updated_at
// (仿 SetWorkflowStatus). progress<0 保持原值不变。
func (s *Store) SetPlanStatus(id, status string, progress int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var err error
	if progress < 0 {
		_, err = s.db.Exec(`UPDATE plans SET status=?, updated_at=? WHERE plan_id=?`,
			status, s.unixNow(), id)
	} else {
		_, err = s.db.Exec(`UPDATE plans SET status=?, progress=?, updated_at=? WHERE plan_id=?`,
			status, progress, s.unixNow(), id)
	}
	if err != nil {
		return fmt.Errorf("jobstore: set plan %q status %s: %w", id, status, err)
	}
	return nil
}

// AttachJobToPlan binds an existing job to a plan by setting jobs.plan_id
// (conditional on the job existing). Returns true only when a row changed (job
// found); false (nil err) for an unknown job id. 不改 jobs.updated_at（那是 job
// 生命周期时间戳，归组是元数据挂载，不应扰动排序/保留）；改 plan.updated_at 由调用方另议。
// Runs under writeMu.
func (s *Store) AttachJobToPlan(jobID, planID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE jobs SET plan_id = ? WHERE id = ?`, planID, jobID)
	if err != nil {
		return false, fmt.Errorf("jobstore: attach job %q to plan %q: %w", jobID, planID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// TouchPlan bumps a plan's updated_at (membership/进度变化后). Best-effort helper
// used by attach; a no-op for an unknown id.
func (s *Store) TouchPlan(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE plans SET updated_at=? WHERE plan_id=?`, s.unixNow(), id); err != nil {
		return fmt.Errorf("jobstore: touch plan %q: %w", id, err)
	}
	return nil
}
```

> `s.unixNow()`（`workflows.go:14`）、`rowScanner`（`jobs.go:133`）、`DefaultListLimit`（`store.go:33`）复用现成，不重复定义。

**验收**：`go test ./internal/jobstore/...` 绿；新增 `plans_test.go`：Insert→Get 回读；ListPlans 过滤 status；AttachJobToPlan 对已存在 job 返回 true 且 `ListJobs{Plan}` 能查到、对未知 id 返回 false；SetPlanStatus 改 status/progress。

---

## T3. job 层：`JobRequest.PlanID`（客户端可设）+ `JobResult.PlanID` + 互转 + submit

### T3.1 JobRequest 加字段（**客户端可设**，C1 核心）

**`internal/job/model.go`**，在 `RequestID`（`model.go:78`）或 `Tags`（`model.go:82`）附近加（与 `SessionID` 同为客户端可设、用真实 json+yaml tag，**区别于** `WorkflowID` 的 `"-"`）：
```go
	// PlanID is the CLIENT-SETTABLE grouping key (plan-orchestration P1, design §4):
	// it joins this job to a plan (plans.plan_id). UNLIKE WorkflowID/StepIndex (engine-
	// private, json/yaml "-"), PlanID uses real json+yaml tags so a client (HTTP body /
	// md frontmatter / CLI --plan / MCP plan_id) MAY set it — 这是 plan 与 workflow 的本质
	// 区别（plan=动态归组，非引擎推进）。Empty == not in any plan. Persisted to jobs.plan_id.
	PlanID string `json:"plan_id,omitempty" yaml:"plan_id,omitempty"`
```

### T3.2 JobResult 加字段

**`internal/job/model.go`**，`JobResult` 里（`SessionID`，`model.go:246` 附近）加：
```go
	// PlanID 是此 job 所属 plan 的归组键（plan-orchestration P1）。持久化 jobs.plan_id，
	// 供 show/list（?plan=）回显。空=不属任何 plan（omitempty）。
	PlanID string `json:"plan_id,omitempty"`
```

### T3.3 toRecord / fromRecord 互转

**`internal/job/persistence.go`**：
- `toRecord`（`persistence.go:22-63`）末尾 `Role: r.Role` 后加 `PlanID: r.PlanID,`
- `fromRecord`（`persistence.go:92-134`）末尾 `Role: rec.Role` 后加 `PlanID: rec.PlanID,`

### T3.4 submit 落库

**`internal/job/submit.go`**，`entry.result` 初始化（`submit.go:231-265`）里 `Role: req.Role` 后加：
```go
			// plan 编排（plan-orchestration P1）：客户端可设的归组键，入口透传在 req 上；
			// 普通 job / 未归组为空。落 jobs.plan_id，供 list --plan / plan 详情回查。
			PlanID: req.PlanID,
```

> resume 路径（`resume.go:84` 附近构造新 req）如需让续投 job 继承源 job 的 plan_id，属 P4（session 续跑归组）——P1 不动 resume，避免越界。

**验收**：`go test ./internal/job/...` 绿；新增/扩展 submit 测试：提交带 `PlanID:"plan-x"` 的 job → `GetJob` 回读 `PlanID=="plan-x"`；`ListJobs{Plan:"plan-x"}` 命中。

---

## T4. list `--plan` 过滤（job.Service / HTTP / client / CLI 逐层）

### T4.1 job.ListOpts + ListJobs

**`internal/job/list.go`**：
- `ListOpts`（`list.go:16-38`）`Session`（line 31）后加：
```go
	// Plan, when non-empty, keeps only jobs whose plan_id matches exactly
	// (plan-orchestration P1, list --plan)。
	Plan string
```
- `ListJobs` DB 查询映射（`list.go:72-82`）`Session: opts.Session,` 后加 `Plan: opts.Plan,`
- **内存 overlay 过滤**（`list.go:96-122`，与 DB 逐维一致，C：未落终态的 live job 也须过滤）在 session 块（`list.go:115-117`）后加：
```go
		if opts.Plan != "" && snap.PlanID != opts.Plan {
			continue
		}
```

### T4.2 HTTP list handler

**`internal/httpapi/job_handler.go`**，`handleListJobs`（`job_handler.go:133-144`）`Session: c.Query("session"),` 后加：
```go
		Plan:    c.Query("plan"),
```

### T4.3 client.ListJobs 透传 query

**`internal/client/client.go`**，`ListJobs`（`client.go:146-167` 区，session query 拼装 `client.go:166-167` 之后）加：
```go
	if opts.Plan != "" {
		q.Set("plan", opts.Plan)
	}
```

### T4.4 CLI `job list --plan`

**`internal/commands/job.go`**：
- `jobListOpts`（`job.go:76-91`）加 `plan string`
- flag 绑定（`job.go:189` session flag 后）：
```go
					c.StrOpt(&jobListOpts.plan, "plan", "", "", "filter by plan id (exact match; lists a plan's jobs)")
```
- `runJobList` 映射（`job.go:579-589`）`Session: jobListOpts.session,` 后加 `Plan: jobListOpts.plan,`

**验收**：`go test ./internal/job/... ./internal/httpapi/... ./internal/commands/...` 绿；冒烟：`gofer job list --plan plan-x` 只列该 plan 的 job。

---

## T5. HTTP：plan 端点（新 `internal/httpapi/plan_handler.go`）

> httpapi 经 `s.jobs.Meta()`（`service.go:204` 返回 `*jobstore.Store`）访问 plan CRUD；plan_id 生成用 `job.JobIDLayout`+`job.RandomSuffix`（httpapi 已 import job）。caller 盖章仿 `handleCreateWorkflow`（`workflow_handler.go:40` `callerFromCtx(c)`）。

### T5.1 handler 文件

```go
package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// planView is the snake_case create/list/get response header (仿 workflowSummary)。
type planView struct {
	PlanID      string `json:"plan_id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Owner       string `json:"owner,omitempty"`
	Progress    int    `json:"progress,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

func toPlanView(p jobstore.Plan) planView {
	return planView{
		PlanID: p.PlanID, Title: p.Title, Description: p.Description,
		Status: p.Status, Owner: p.Owner, Progress: p.Progress,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// createPlanReq is the POST /v1/plans body. plan_id 可选（客户端可设，C1）；
// 缺省时服务端生成 plan-<time>-<rand>。owner 由 caller 盖章（防伪，仿 workflow）。
type createPlanReq struct {
	PlanID      string `json:"plan_id,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// handleCreatePlan 建一个 plan（open）。plan_id 客户端可设，缺省服务端生成；owner=caller。
func (s *Server) handleCreatePlan(c *rux.Context) {
	var body createPlanReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	planID := strings.TrimSpace(body.PlanID)
	if planID == "" {
		planID = "plan-" + time.Now().Format(job.JobIDLayout) + "-" + job.RandomSuffix()
	}
	now := time.Now().Unix()
	p := jobstore.Plan{
		PlanID: planID, Title: body.Title, Description: body.Description,
		Status: jobstore.PlanOpen, Owner: callerFromCtx(c),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.jobs.Meta().InsertPlan(p); err != nil {
		writeError(c, http.StatusInternalServerError, "create plan failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toPlanView(p))
}

// handleListPlans 列 plan（?status= 过滤）。空结果为非 nil 数组 {"plans":[]}。
func (s *Server) handleListPlans(c *rux.Context) {
	list, err := s.jobs.Meta().ListPlans(c.Query("status"), 0)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list plans failed", err.Error())
		return
	}
	out := make([]planView, 0, len(list))
	for _, p := range list {
		out = append(out, toPlanView(p))
	}
	c.JSON(http.StatusOK, map[string]any{"plans": out})
}

// planDetail 是 GET /v1/plans/{id} 响应：plan header + 其下 job 列表（P1）。
// jobs 状态聚合计数 {total,queued,running,done,failed} 留 P2。
type planDetail struct {
	planView
	Jobs []job.JobResult `json:"jobs"`
}

// handleGetPlan 返回 plan header + 其下所有 job（按 plan_id 过滤）。未知 id → 404。
func (s *Server) handleGetPlan(c *rux.Context) {
	id := c.Param("id")
	p, ok, err := s.jobs.Meta().GetPlan(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+id)
		return
	}
	jobs, err := s.jobs.ListJobs(job.ListOpts{Plan: id, Limit: 1000})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list plan jobs failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, planDetail{planView: toPlanView(p), Jobs: jobs})
}

// attachJobReq 是 POST /v1/plans/{id}/jobs 的 body：把已有 job 挂到 plan。
type attachJobReq struct {
	JobID string `json:"job_id"`
}

// handleAttachPlanJob 把已有 job 挂到 plan（补挂已存在 job；提交即归组走 job.plan_id）。
// 未知 plan → 404；未知 job → 404；成功返回 plan header。
func (s *Server) handleAttachPlanJob(c *rux.Context) {
	id := c.Param("id")
	var body attachJobReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if strings.TrimSpace(body.JobID) == "" {
		writeError(c, http.StatusBadRequest, "job_id required", "attach requires a job_id")
		return
	}
	if _, ok, err := s.jobs.Meta().GetPlan(id); err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+id)
		return
	}
	ok, err := s.jobs.Meta().AttachJobToPlan(body.JobID, id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "attach job failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+body.JobID)
		return
	}
	_ = s.jobs.Meta().TouchPlan(id) // best-effort 更新 plan.updated_at（成员变化）
	p, _, _ := s.jobs.Meta().GetPlan(id)
	c.JSON(http.StatusOK, toPlanView(p))
}
```

> ⚠️ 校验 `writeError` / `callerFromCtx` / `s.jobs` / `c.BindJSON` / `c.Param` / `c.Query` 均为现有（`workflow_handler.go` + `job_handler.go` 已用）。`job.RandomSuffix`（`submit.go:430`）、`job.JobIDLayout`（`service.go:28`）已导出。

### T5.2 路由注册

**`internal/httpapi/server.go`**，workflow 路由块（`server.go:419-426`）之后加：
```go
		// plan 编排（plan-orchestration P1）：建计划 / 列表 / 详情（含其下 job）/ 挂 job。
		// 归组容器，与 workflow 引擎解耦（纯归组不推进）。attach 补挂已有 job；提交即归组
		// 走 job 的 plan_id 字段（POST /v1/jobs body 直接带）。
		r.POST("/plans", s.handleCreatePlan)
		r.GET("/plans", s.handleListPlans)
		r.GET("/plans/{id}", s.handleGetPlan)
		r.POST("/plans/{id}/jobs", s.handleAttachPlanJob)
```

> 该路由块在受鉴权保护的 `/v1` group 内（与 workflows 同组）。POST /plans 是否计入限流 `isSubmitPath`（`server.go:467`）：P1 不纳入（plan 建计划是轻量 DB 写，非 job 提交）；如需再议。

**验收**：`go test ./internal/httpapi/...` 绿；新增 `plan_handler_test.go`：POST /v1/plans → 200 带 plan_id + owner=caller；GET /v1/plans/{id} 404 未知；attach 已有 job 后 GET 详情 jobs 含之；attach 未知 job → 404。

---

## T6. client + CLI `gofer plan`

### T6.1 client 方法（`internal/client/client.go`，仿 workflow 段 `client.go:465-539`）

```go
// Plan is the client-side view of a plan header (plan-orchestration P1). Detail
// (GetPlan) inlines its jobs; list/create return the header only (Jobs nil there).
type Plan struct {
	PlanID      string           `json:"plan_id"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Status      string           `json:"status"`
	Owner       string           `json:"owner,omitempty"`
	Progress    int              `json:"progress,omitempty"`
	CreatedAt   int64            `json:"created_at"`
	UpdatedAt   int64            `json:"updated_at"`
	Jobs        []job.JobResult  `json:"jobs,omitempty"`
}

// CreatePlan POSTs /v1/plans and returns the created header. plan_id 可选（空=服务端生成）。
func (c *Client) CreatePlan(planID, title, description string) (Plan, error) {
	body := map[string]string{"plan_id": planID, "title": title, "description": description}
	buf, err := json.Marshal(body)
	if err != nil {
		return Plan{}, fmt.Errorf("encode create plan: %w", err)
	}
	var p Plan
	// doJSON / postJSON：仿 SubmitWorkflow 的现有 helper（实施时对齐 client 内既有 POST helper 名）。
	if err := c.postJSON("/v1/plans", buf, &p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// ListPlans GET /v1/plans?status= 。
func (c *Client) ListPlans(status string) ([]Plan, error) {
	path := "/v1/plans"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	var out struct{ Plans []Plan `json:"plans"` }
	if err := c.getJSON(path, &out); err != nil {
		return nil, err
	}
	return out.Plans, nil
}

// GetPlan GET /v1/plans/{id}（含其下 jobs）。
func (c *Client) GetPlan(id string) (Plan, error) {
	var p Plan
	if err := c.getJSON("/v1/plans/"+id, &p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// AttachJob POSTs /v1/plans/{id}/jobs to bind an existing job.
func (c *Client) AttachJob(planID, jobID string) (Plan, error) {
	buf, _ := json.Marshal(map[string]string{"job_id": jobID})
	var p Plan
	if err := c.postJSON("/v1/plans/"+planID+"/jobs", buf, &p); err != nil {
		return Plan{}, err
	}
	return p, nil
}
```

> ⚠️ 实施时对齐 client 内既有请求 helper 的**真实方法名/签名**（`SubmitWorkflow`（`client.go:468`）、`GetWorkflow`（`client.go:480`）、`ListWorkflows`（`client.go:488`）分别用的是什么 doRequest/getJSON/postJSON 封装——照抄那一套，勿臆造 `postJSON/getJSON`）。`url` import 若未有需补。

### T6.2 CLI 命令（新 `internal/commands/plan.go`，仿 `workflow.go`）

`NewPlanCmd()`：`plan`（别名可留空或 `pl`），子命令：
- `create`：flags `--title` / `--desc` / `--plan-id`（可选，空=服务端生成）→ `cli.CreatePlan(...)`，打印 `plan <id> created: status=open`。
- `list`（别名 `ls`）：`--status` → `cli.ListPlans`，固定宽表 `PLAN ID / STATUS / TITLE / CREATED`（复用 `truncate`/`formatStarted`，`workflow.go:281-304`）。
- `show`：arg `<id>` → `cli.GetPlan`，打印 header + 其下 job 表（`STEP` 换成 `JOB ID / STATUS / AGENT`）。
- `attach`：arg `<plan-id>` + arg `<job-id>`（或 `--job`）→ `cli.AttachJob`，打印 `job <j> attached to plan <p>`。

每个子命令 `Config` 内调 `bindConfigFlag(c)` + `bindServerFlags(c)`（G011，仿 `workflow.go:57-62`）；`newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)` 建 client。

**注册**：`internal/commands/app.go:36`（`app.Add(NewWorkflowCmd())`）后加一行 `app.Add(NewPlanCmd())`。

**验收**：`go build ./...` 绿；`go test ./internal/commands/... ./internal/client/...` 绿；冒烟：`gofer plan create --title X` → 得 plan_id；`gofer job run -p self -a exec --plan <id> -- echo hi`（依赖 T3 的 `--plan` run flag，见 T6.3）；`gofer plan show <id>` 列出该 job。

### T6.3 `gofer job run --plan`（提交即归组，客户端可设）

**`internal/commands/job.go`**：
- `jobRunOpts`（`job.go:25-46` 区）加 `plan string`
- flag 绑定（`job.go:129` tags flag 附近）：
```go
					c.StrOpt(&jobRunOpts.plan, "plan", "", "", "attach the job to a plan (grouping key; client-settable)")
```
- `buildJobRunRequest`（`job.go:390-428` 区，赋值处）加 `PlanID: jobRunOpts.plan,`

> 这是「提交即归组」的 CLI 侧：run job 时直接带 `plan_id`，无需事后 attach（设计 §8 更顺）。`schedule add` 复用 `buildJobRunRequest` → 自动继承。

**验收**：`gofer job run ... --plan plan-x` → `GetJob` 回读 `plan_id=="plan-x"`。

---

## T7. 测试与文档收尾

### T7.1 测试清单

- **jobstore**（`jobs_test`/新 `plans_test`）：plan_id 加列回读 + `ListJobs{Plan}` 过滤（T1.4 验收）；plans CRUD + AttachJobToPlan（T2 验收）。
- **job**：submit 带 PlanID 落库回读 + list `Plan` 过滤（DB + 内存 overlay 两路，构造一个未落终态的 live job 验证 overlay 分支，`list.go:96-122`）。
- **httpapi**（新 `plan_handler_test.go`）：create（含 caller owner 盖章 + 服务端生成 id + 客户端指定 id 两路）/ list / get（404 + 含 jobs）/ attach（成功 + 未知 plan 404 + 未知 job 404）。
- **client**：CreatePlan/ListPlans/GetPlan/AttachJob 往返（起 httptest server 或复用现有 client 测试骨架）。
- **commands**（若有 commands 层测试）：`--plan` 流到 `JobRequest.PlanID`；`plan list/show` 渲染。

### T7.2 总验收

- [ ] `go build ./...` + `go vet ./...` 绿。
- [ ] `go test ./...` 全绿（容器 Linux）。
- [ ] 冒烟（local，容器/主机）端到端：
  - `gofer plan create --title "重构X"` → 得 `plan-...`。
  - `gofer job run -p self -a exec --plan <id> -- echo hi` → job 落库带 plan_id。
  - `gofer plan show <id>` → 列出该 job；`gofer job list --plan <id>` → 命中。
  - `gofer plan attach <id> <另一个已有 jobID>` → 该 job 归入 plan，再 `plan show` 可见。
- [ ] 旧库兼容：对一个 P1 前的旧 `jobstore.db` 执行 Open → migrate 自动 ALTER 加 `plan_id` + 建 `plans` 表，旧 job 读出 `plan_id==""` 无报错。
- [ ] 更新总纲进度 checkbox（P1 各 T）+ 按 SR1202 分子阶段提交。

## 风险

- **占位符错位**（T1.3d）：UpsertJob 的列数 / VALUES `?` 数 / Exec 实参数三处必须同步 +1，漏一处即 panic/错列。改后逐一核对（当前 33→34）。
- **内存 overlay 漏过滤**（T4.1）：list 的 DB 路加了 `plan_id` where 但 overlay 未加 → 未落终态的 live job 会绕过 `--plan` 过滤（session 已踩过同款坑，`list.go:98` 注释明示"逐维一致"）。务必同步加 overlay 分支。
- **client helper 名臆造**（T6.1）：client.go 的 POST/GET 封装名以**实际代码**为准（照抄 SubmitWorkflow/GetWorkflow 那套），不要照本文的 `postJSON/getJSON` 占位名硬写。
- **jobstore import 环**（C3）：plan_id 生成绝不能塞进 jobstore（会诱导 import job）。已放 httpapi 生成、jobstore.InsertPlan 只收非空 id。
- **attach 与 updated_at**（T2.2）：AttachJobToPlan 故意不动 `jobs.updated_at`（保 job 生命周期排序/保留语义），只 TouchPlan bump 计划侧时间；勿顺手改 job 的 updated_at。
- **限流归类**（T5.2）：POST /v1/plans 是否计入 `isSubmitPath` 限流，P1 定为不计入（轻量 DB 写）；若后续 plan 建量大再评估。
