# P3 — plan_todos 待办（实施计划，代码级）

> 主纲：[plan-orchestration-plan.md](./plan-orchestration-plan.md)
> 设计：[../../design/2026-07-09-plan-orchestration-design.md](../../design/2026-07-09-plan-orchestration-design.md) §5.3/§8/§12
> 上游：[P1-data-plan.md](./P1-data-plan.md)（✅ 已完成并 push）、[P2-mcp-aggregate-plan.md](./P2-mcp-aggregate-plan.md)（✅ 已完成并 push）；本期在其上叠加
> 触点均已实测定位（2026-07-10 只读探查，见各 T 的 file:line）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-10 | inhere/claude | 初稿：P3 代码级子计划（plan_todos 表 + CRUD + HTTP/MCP/CLI 三通道 + 详情内联 todos），触点已实测 |

## 一句话

在 P1/P2 之上补齐 **todo 待办通道**：新增 `plan_todos` 表（两种 todo：`job_id=NULL` 纯待办 / `job_id` 非空绑某次 job 执行）+ jobstore CRUD + HTTP（`POST /v1/plans/{id}/todos`、`PATCH /v1/todos/{todo_id}`、`GET /v1/plans/{id}` 详情**内联 todos**）+ MCP（`gofer_add_todo` / `gofer_update_todo`，`gofer_get_plan` 内联 todos）+ client + CLI，三通道一致。**只做纯手动勾选 `done`，不做 job 终态→todo 自动联动**（留后续开关）。

## 范围

**做（P3）**

1. **表 `plan_todos`**（新，`schemaStmts` `CREATE TABLE IF NOT EXISTS`，仿 plans 表位置 `store.go:265-278`）+ `idx_plan_todos_plan`。**新表无需 migrate() ALTER**（applySchema 幂等建表）。
2. **jobstore CRUD**（新 `internal/jobstore/todos.go`，仿 `plans.go`/`workflows.go`）：`PlanTodo` 中立结构 + `InsertTodo / ListTodosByPlan / GetTodo / SetTodoDone / DeleteTodo`。**中立不 import job**；`todo_id` 生成放入口层（httpapi + mcpserver localBackend，仿 plan_id 用 `job.JobIDLayout`+`job.RandomSuffix`）；`InsertTodo` 只收非空 id（C3）。
3. **HTTP**：`POST /v1/plans/{id}/todos`（建）+ `PATCH /v1/todos/{todo_id}`（勾/取消 `done`）；`GET /v1/plans/{id}` 详情 `planDetail` 加 `todos` 字段（像 P2 的 `counts`/`jobs` 那样内联）。
4. **client**：`Todo` 类型 + `AddTodo(planID,title,jobID)` / `UpdateTodo(todoID,done)`；`Plan` 加 `Todos`（`GetPlan` 内联）。
5. **MCP**：`gofer_add_todo(plan_id, title, job_id?)` / `gofer_update_todo(todo_id, done)`；`Backend` 加 `AddTodo/UpdateTodo`（仿 P2 `CreatePlan/AttachJob/GetPlan` 的双实现）；mcpserver 自有 `todoView`；`planView` 加 `todos`（与 HTTP 详情对齐）。
6. **CLI**（三通道一致，设计 §8.3）：`plan show` 渲染 todos；新增 `plan add-todo` / `plan set-todo` 子命令。
7. **测试**：jobstore / httpapi / client / mcpserver / commands。

**不做（划归 P4 或留开关）**

- **job 终态 → todo 自动勾选 `done`**（设计 §12 待确认 3，倾向"可勾但不强制自动"）：P3 **只落纯手动 `SetTodoDone`**，不接 sweeper/finish 钩子、不加自动联动逻辑。`plan_todos.job_id` 只是元数据绑定（供未来联动定位），本期不驱动任何状态变化。
- 前端 `Plans.vue`/`PlanDetail.vue` todos 渲染（P4）。
- todo `sort` 的拖拽重排接口（本期 `sort` 仅可创建时设、按 `sort ASC` 展示，无重排端点）。
- HTTP/MCP 的 todo 删除端点：`DeleteTodo` 落在 jobstore 层备用（+单测），本期**不暴露** HTTP/MCP/CLI 删除面（避免过度设计；后续需要再补 3 行）。

## 核心约束（承接总纲 C1..C5 / G021..G024）

- **C3 jobstore 中立**：`todos.go` **绝不** import `internal/job`；`todo_id` 生成在入口层（httpapi handler + mcpserver localBackend），`InsertTodo` 只收非空 id。`done` 用硬编码存储转换（见「done 存储决策」），不引 job。
- **C5 三入口只做绑定/校验/转发**（G021）：handler 只做入参绑定 + view 投影；建 id、CRUD、内联聚合逻辑在 jobstore(数据) / backend(后端访问)。
- **Backend 双实现范式（照抄 P2 CreatePlan/AttachJob/GetPlan）**：`Backend` 接口加 `AddTodo/UpdateTodo` → localBackend 经 `b.jobs.Meta()` 直驱 `*jobstore.Store`；clientBackend 转发 `client.AddTodo/UpdateTodo`。两端源形不同（`jobstore.PlanTodo` vs `client.Todo`）→ 返回 mcpserver 自有 `todoView`，两端各自构建、形状逐字对齐（同 planView 约定）。
- **G022 依赖方向**：mcpserver/httpapi/client 已 import jobstore（P2 起），本期不新增反向依赖；`go list -deps ./internal/jobstore/...` 须仍不含 mcpserver/job。

## 已确认关键事实（探查结论）

| 事实 | 位置 | 对 P3 的影响 |
|---|---|---|
| plans 表建在 schemaStmts 末尾（IF NOT EXISTS，注释明示"新表无需 migrate ALTER"） | `internal/jobstore/store.go:265-278` | plan_todos 照此**紧随其后**追加，同风格 |
| plans CRUD 范式（`selectPlanCols`/`scanPlan`/Insert/Get/List/SetStatus/Attach/Touch） | `internal/jobstore/plans.go:31-148` | todos.go **逐一照搬** |
| `rowScanner`（`jobs.go:133`）、`s.unixNow()`（`workflows.go:14`）、`DefaultListLimit`（`store.go:33`）现成复用 | — | 不重复定义 |
| 布尔列 jobstore 用 **`int`**（`Enabled int`/`CatchUp int`，存 0/1，`enabled=1`） | `internal/jobstore/schedules.go:21,25,93` | done 存储用 INTEGER 0/1；见「done 存储决策」 |
| plan_id 生成范式 `"plan-"+time.Now().Format(job.JobIDLayout)+"-"+job.RandomSuffix()` | httpapi `plan_handler.go:47`；mcpserver `backend_local.go:161-163` | todo_id 生成 `"todo-"+...` 同款，两处各一份 |
| HTTP 详情已内联 `counts`+`jobs`（`planDetail`） | `internal/httpapi/plan_handler.go:75-107` | 加 `Todos []todoView` 字段，handler 多一次 `ListTodosByPlan` |
| MCP `planView` 已内联 `counts`+`jobs`，`planHeaderView` 置空 jobs | `internal/mcpserver/server.go:293-323` | 加 `Todos []todoView`，`planHeaderView` 置 `make([]todoView,0)`，GetPlan 填 |
| plan 路由块 4 条 | `internal/httpapi/server.go:429-432` | 其后加 2 条 todo 路由；`r.PATCH` 存在（rux `internal/core/router.go:192`） |
| Backend 只有 local/client 两实现，无手写 fake（测试 mockBackend 套 httptest mux） | `internal/mcpserver/backend_client_test.go:15-31` | 接口加方法只需补两真实现 |
| plan 工具注册块 3 条 | `internal/mcpserver/server.go:156-169` | 其后加 2 条 todo 工具注册 |
| client `Plan`/CRUD + `doJSON`（POST/GET 现成，PATCH 传 `http.MethodPatch`） | `internal/client/client.go:514-572` | 加 `Todo` 类型 + `AddTodo/UpdateTodo`，用 `doJSON` |
| CLI `plan` 命令已注册（`app.go:37`），有 show/attach 子命令与 `printPlan`/`printPlanJobs` | `internal/commands/plan.go:24-181`；`app.go:37` | show 加 todos 渲染 + 2 个子命令 |
| jobView 带 `PlanID` 字段（供内联 jobs 展示） | `internal/mcpserver/server.go:278` | 无需改 |

## `done` 存储决策（本期）

- **存储**：`plan_todos.done INTEGER NOT NULL DEFAULT 0`（0=未完成 / 1=已完成），对齐 jobstore 既有布尔列 `enabled` 的 0/1 约定（`schedules.go:93`）。
- **中立结构 / 视图 / 线格**：`jobstore.PlanTodo.Done` 及三处 view（httpapi/mcpserver `todoView`、client `Todo`）统一用 **`bool`**（MCP `gofer_update_todo(done)` 与设计 §5.3 均为 bool，bool 是 todo 的自然语义）。
- **转换收敛在 jobstore 单点**：写入（`InsertTodo`/`SetTodoDone`）时 `bool→0/1`，读取（`scanTodo`）时 `SELECT COALESCE(done,0)` 扫进 int 中间量再 `Done = n != 0`。视图层不做任何 int↔bool 转换（`PlanTodo.Done` 已是 bool），避免多点转换漂移。
- **偏离说明**：这里的中立结构用 `bool` 而非沿用 `Enabled int` 的 int 惯例——理由：done 是二元完成态，bool 语义更贴切且与 MCP/设计契约一致；把唯一的存储转换隔离在 jobstore 内一层，反而比"int 贯穿 + 3 处 view 转 bool"更少转换点。

---

## 任务分解（T1..T9）

> 顺序：T1（建表）→ T2（jobstore CRUD）→ T3（HTTP）→ T4（client）→ T5/T6/T7（MCP 接口+两实现）→ T8（CLI）→ T9（测试门禁）。SR1202：每个 T 完成后更新总纲 checkbox 并 Git 提交（`feat(plan):` 前缀）。

### T1 — jobstore：`plan_todos` 表 + 索引

**现状**（`internal/jobstore/store.go:265-279`，schemaStmts 尾部 plans 表 + 索引，后接 `}` 收尾）：
```go
	`CREATE TABLE IF NOT EXISTS plans (
  plan_id      TEXT PRIMARY KEY,
  ...
)`,
	`CREATE INDEX IF NOT EXISTS idx_plans_status ON plans(status)`,
}
```

**改为**（在 `idx_plans_status` 之后、`}` 之前插入 plan_todos 建表 + 索引）：
```go
	`CREATE INDEX IF NOT EXISTS idx_plans_status ON plans(status)`,
	// plan_todos is the plan-orchestration todo list (design §5.3). Two kinds of
	// todo share one table: a pure checklist item (job_id NULL) and a job-bound
	// item (job_id -> a specific job run, for future done-linkage). It always
	// belongs to a plan (plan_id). done is 0/1 (jobstore 布尔列惯例，如 schedules.enabled).
	// sort orders the list (ASC). IF NOT EXISTS like every table here — a fresh
	// new table needs no migrate() ALTER (P3 只加新表，不改 jobs 列).
	`CREATE TABLE IF NOT EXISTS plan_todos (
  todo_id      TEXT PRIMARY KEY,
  plan_id      TEXT NOT NULL,
  job_id       TEXT,
  title        TEXT,
  done         INTEGER NOT NULL DEFAULT 0,
  sort         INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_plan_todos_plan ON plan_todos(plan_id)`,
}
```

**验收**：`go test ./internal/jobstore/...` 绿；新增断言 `tableExists(t,s,"plan_todos")` + `indexExists(t,s,"idx_plan_todos_plan")`（仿 `plans_test.go:11-18` 的 `TestFreshOpenHasPlansTableAndPlanIDColumn`）。旧库 Open（`TestMigrateAddsPlanSupportToOldDB`，`plans_test.go`）也应自动有此表（applySchema 幂等）——顺带在该测试加一条 `tableExists(...,"plan_todos")` 断言。

---

### T2 — jobstore：CRUD（新文件 `internal/jobstore/todos.go`）

**新建 `internal/jobstore/todos.go`**（仿 `plans.go`；**不 import job**）：
```go
package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// PlanTodo is a plan-orchestration todo item (design §5.3). It is neutral (no
// internal/job import) so job/http layers drive it without an import cycle.
// JobID "" == a pure checklist item; non-empty binds it to a specific job run
// (metadata only in P3 — done stays手动, no auto-linkage). Done is exposed as a
// bool; storage is the 0/1 INTEGER column (conversion isolated here).
type PlanTodo struct {
	TodoID    string
	PlanID    string
	JobID     string
	Title     string
	Done      bool
	Sort      int
	CreatedAt int64
	UpdatedAt int64
}

// selectTodoCols COALESCE-guards nullable job_id/title/done/sort so a NULL/absent
// column scans into the zero value instead of failing (mirrors selectPlanCols).
const selectTodoCols = `SELECT todo_id, plan_id, COALESCE(job_id,''),
  COALESCE(title,''), COALESCE(done,0), COALESCE(sort,0), created_at, updated_at
  FROM plan_todos`

// scanTodo reads one row (in selectTodoCols order). done is scanned as int then
// folded to bool (the single storage<->bool conversion point, C3).
func scanTodo(sc rowScanner) (PlanTodo, error) {
	var (
		t    PlanTodo
		done int
	)
	err := sc.Scan(&t.TodoID, &t.PlanID, &t.JobID, &t.Title, &done, &t.Sort,
		&t.CreatedAt, &t.UpdatedAt)
	t.Done = done != 0
	return t, err
}

// InsertTodo persists a new todo. The caller generates a non-empty todo_id
// (jobstore stays job-import-free, C3); plan_id must be non-empty too. job_id ""
// is stored as NULL (scans back "" via COALESCE) so a pure todo is a clean NULL.
func (s *Store) InsertTodo(t PlanTodo) error {
	if t.TodoID == "" {
		return errors.New("jobstore: InsertTodo: empty todo id")
	}
	if t.PlanID == "" {
		return errors.New("jobstore: InsertTodo: empty plan id")
	}
	var jobID any // NULL for a pure todo (design §5.3)
	if t.JobID != "" {
		jobID = t.JobID
	}
	done := 0
	if t.Done {
		done = 1
	}
	const q = `INSERT INTO plan_todos
  (todo_id, plan_id, job_id, title, done, sort, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, t.TodoID, t.PlanID, jobID, t.Title, done, t.Sort,
		t.CreatedAt, t.UpdatedAt); err != nil {
		return fmt.Errorf("jobstore: insert todo %q: %w", t.TodoID, err)
	}
	return nil
}

// GetTodo returns a todo by id. ok is false (nil err) when absent (仿 GetPlan).
func (s *Store) GetTodo(id string) (PlanTodo, bool, error) {
	t, err := scanTodo(s.db.QueryRow(selectTodoCols+" WHERE todo_id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return PlanTodo{}, false, nil
	}
	if err != nil {
		return PlanTodo{}, false, fmt.Errorf("jobstore: get todo %q: %w", id, err)
	}
	return t, true, nil
}

// ListTodosByPlan returns a plan's todos ordered by sort ASC then created_at ASC
// (stable). An empty plan yields a non-nil empty slice.
func (s *Store) ListTodosByPlan(planID string) ([]PlanTodo, error) {
	rows, err := s.db.Query(
		selectTodoCols+" WHERE plan_id = ? ORDER BY sort ASC, created_at ASC, todo_id ASC",
		planID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list todos of plan %q: %w", planID, err)
	}
	defer rows.Close()
	out := make([]PlanTodo, 0)
	for rows.Next() {
		t, scanErr := scanTodo(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan todo row: %w", scanErr)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list todos of plan %q rows: %w", planID, err)
	}
	return out, nil
}

// SetTodoDone sets a todo's done flag and stamps updated_at. It returns false
// (nil err) when the todo id is unknown (仿 AttachJobToPlan 的 RowsAffected==1).
// 纯手动勾选 —— P3 不接 job 终态自动联动（留后续开关）。
func (s *Store) SetTodoDone(todoID string, done bool) (bool, error) {
	d := 0
	if done {
		d = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE plan_todos SET done=?, updated_at=? WHERE todo_id=?`,
		d, s.unixNow(), todoID)
	if err != nil {
		return false, fmt.Errorf("jobstore: set todo %q done: %w", todoID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteTodo removes a todo. It returns false (nil err) for an unknown id. Kept
// for完整性 (P3 outline CRUD); no HTTP/MCP surface is exposed this期.
func (s *Store) DeleteTodo(todoID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM plan_todos WHERE todo_id=?`, todoID)
	if err != nil {
		return false, fmt.Errorf("jobstore: delete todo %q: %w", todoID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
```

**验收**：`go test ./internal/jobstore/...` 绿；新增 `todos_test.go`（或并入 plans_test.go）：Insert（纯待办 job_id="" 存 NULL，回读 ""；绑 job 回读 job_id）→ Get 回读；`ListTodosByPlan` 只回该 plan、按 sort 排序；`SetTodoDone(true/false)` 改 done + 未知 id 返回 false；`DeleteTodo` 删除 + 未知 id false；`InsertTodo` 空 todo_id/空 plan_id 报错。

---

### T3 — HTTP：add-todo / update-todo 端点 + 详情内联 todos

**`internal/httpapi/plan_handler.go`**：

**3.1 `todoView` + 投影**（文件内新增，紧邻 `planView`）：
```go
type todoView struct {
	TodoID    string `json:"todo_id"`
	PlanID    string `json:"plan_id"`
	JobID     string `json:"job_id,omitempty"`
	Title     string `json:"title"`
	Done      bool   `json:"done"`
	Sort      int    `json:"sort,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func toTodoView(t jobstore.PlanTodo) todoView {
	return todoView{
		TodoID: t.TodoID, PlanID: t.PlanID, JobID: t.JobID, Title: t.Title,
		Done: t.Done, Sort: t.Sort, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}
```

**3.2 `planDetail` 加 `Todos`**（现状 `plan_handler.go:75-79`）：
```go
type planDetail struct {
	planView
	Counts jobstore.PlanCounts `json:"counts"`
	Jobs   []job.JobResult     `json:"jobs"`
	Todos  []todoView          `json:"todos"`
}
```

**3.3 `handleGetPlan` 内联 todos**（现状 `plan_handler.go:97-106`，拿到 `raw` counts 后、`c.JSON` 前插入 list + map）：
```go
	todos, err := s.jobs.Meta().ListTodosByPlan(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list plan todos failed", err.Error())
		return
	}
	todoViews := make([]todoView, 0, len(todos))
	for _, t := range todos {
		todoViews = append(todoViews, toTodoView(t))
	}
	c.JSON(http.StatusOK, planDetail{
		planView: toPlanView(p),
		Counts:   jobstore.RollupPlanCounts(raw),
		Jobs:     jobs,
		Todos:    todoViews,
	})
```

**3.4 add-todo handler**（文件末尾新增；plan 存在校验 404，todo_id 服务端生成，job_id 宽松不校验存在性）：
```go
type addTodoReq struct {
	Title string `json:"title"`
	JobID string `json:"job_id,omitempty"`
	Sort  int    `json:"sort,omitempty"`
}

// handleAddPlanTodo 在 plan 下建一个 todo（纯待办或绑 job）。plan 不存在→404；
// title 必填。job_id 宽松存储（不校验该 job 是否存在，todo 与 job 解耦）。
func (s *Server) handleAddPlanTodo(c *rux.Context) {
	id := c.Param("id")
	var body addTodoReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeError(c, http.StatusBadRequest, "title required", "a todo requires a title")
		return
	}
	if _, ok, err := s.jobs.Meta().GetPlan(id); err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+id)
		return
	}
	now := time.Now().Unix()
	t := jobstore.PlanTodo{
		TodoID:    "todo-" + time.Now().Format(job.JobIDLayout) + "-" + job.RandomSuffix(),
		PlanID:    id,
		JobID:     strings.TrimSpace(body.JobID),
		Title:     body.Title,
		Sort:      body.Sort,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.jobs.Meta().InsertTodo(t); err != nil {
		writeError(c, http.StatusInternalServerError, "add todo failed", err.Error())
		return
	}
	_ = s.jobs.Meta().TouchPlan(id) // best-effort：成员变化 bump plan.updated_at
	c.JSON(http.StatusOK, toTodoView(t))
}
```

**3.5 update-todo handler**（PATCH 勾/取消 done；未知 id→404；返回更新后 todo）：
```go
type updateTodoReq struct {
	Done bool `json:"done"`
}

// handleUpdateTodo 勾/取消 todo 的 done（body {done:true|false}）。未知 id→404。
func (s *Server) handleUpdateTodo(c *rux.Context) {
	tid := c.Param("todo_id")
	var body updateTodoReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	ok, err := s.jobs.Meta().SetTodoDone(tid, body.Done)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "update todo failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown todo", "no todo with id "+tid)
		return
	}
	t, _, _ := s.jobs.Meta().GetTodo(tid)
	c.JSON(http.StatusOK, toTodoView(t))
}
```

**3.6 路由**（`internal/httpapi/server.go`，plan 路由块 `server.go:429-432` 之后）：
```go
		// plan todo 待办（P3）：建 todo（纯待办/绑 job）+ 勾选 done。详情 GET /plans/{id}
		// 内联 todos。纯手动，不做 job 终态自动联动（留后续）。
		r.POST("/plans/{id}/todos", s.handleAddPlanTodo)
		r.PATCH("/todos/{todo_id}", s.handleUpdateTodo)
```

> `time`/`strings`/`job`/`jobstore`/`writeError`/`c.BindJSON`/`c.Param` 均本文件已用（`plan_handler.go:1-12`）；无新 import。`r.PATCH` 存在（rux `internal/core/router.go:192`）。POST /plans/{id}/todos 是轻量 DB 写，**不计入** `isSubmitPath` 限流（同 POST /plans，P1 T5.2 决策）。

**验收**：`go test ./internal/httpapi/...` 绿；扩展 `plan_handler_test.go`：POST todo（纯待办 + 绑 job）→ 200 带 todo_id；plan 不存在→404；title 缺失→400；PATCH done→200 且 `done=true`，未知 todo→404；GET /plans/{id} 详情 `todos` 数组含之、`done` 状态正确。

---

### T4 — client：`Todo` 类型 + `AddTodo` / `UpdateTodo` + `Plan.Todos`

**`internal/client/client.go`**：

**4.1 `Plan` 加 `Todos`**（现状 `client.go:514-526`，`Jobs` 字段后）：
```go
	Jobs        []job.JobResult      `json:"jobs,omitempty"`
	Todos       []Todo               `json:"todos,omitempty"`
}

// Todo is the client-side view of a plan todo item (P3). JobID "" == pure todo.
type Todo struct {
	TodoID    string `json:"todo_id"`
	PlanID    string `json:"plan_id"`
	JobID     string `json:"job_id,omitempty"`
	Title     string `json:"title"`
	Done      bool   `json:"done"`
	Sort      int    `json:"sort,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}
```

**4.2 方法**（plan 段末尾，`AttachJob`（`client.go:564-572`）之后）：
```go
// AddTodo POSTs /v1/plans/{id}/todos to create a todo (pure or job-bound).
func (c *Client) AddTodo(planID, title, jobID string) (Todo, error) {
	body, err := json.Marshal(map[string]any{"title": title, "job_id": jobID})
	if err != nil {
		return Todo{}, fmt.Errorf("encode add todo: %w", err)
	}
	var t Todo
	err = c.doJSON(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/todos", bytes.NewReader(body), &t)
	return t, err
}

// UpdateTodo PATCHes /v1/todos/{todo_id} to set the done flag.
func (c *Client) UpdateTodo(todoID string, done bool) (Todo, error) {
	body, err := json.Marshal(map[string]bool{"done": done})
	if err != nil {
		return Todo{}, fmt.Errorf("encode update todo: %w", err)
	}
	var t Todo
	err = c.doJSON(http.MethodPatch, "/v1/todos/"+url.PathEscape(todoID), bytes.NewReader(body), &t)
	return t, err
}
```

> `json`/`bytes`/`url`/`http`/`doJSON` 均已用；无新 import。`doJSON` 对 PATCH 与 POST 同路（`method` 参数）。

**验收**：`go build ./...` 绿；client 测试（起 httptest server 或复用现有骨架）：`AddTodo` 往返回读 todo_id/title/job_id；`UpdateTodo(done=true)` 回读 done；`GetPlan` 后 `Todos` 内联。

---

### T5 — MCP：Backend 接口 + `todoView` + `planView.Todos` + 工具注册/handler

**5.1 接口加两方法**（`internal/mcpserver/backend.go`，plan 三方法 `backend.go:42-44` 之后）：
```go
	// Plan todos (plan-orchestration P3). 两端源形不同（jobstore.PlanTodo vs
	// client.Todo），故返回 mcpserver view 类型 todoView（同 planView 约定）。
	// AddTodo: jobID "" == 纯待办；UpdateTodo: 纯手动勾选，不接 job 终态自动联动。
	AddTodo(planID, title, jobID string) (todoView, error)
	UpdateTodo(todoID string, done bool) (todoView, error)
```

**5.2 `todoView` + `planView.Todos`**（`internal/mcpserver/server.go`）：

`planView`（现状 `server.go:295-306`）`Jobs` 后加 `Todos`：
```go
	Jobs        []jobView           `json:"jobs"`
	Todos       []todoView          `json:"todos"`
}

// todoView is the snake_case projection returned by the todo tools and inlined
// in gofer_get_plan (mirrors the HTTP todoView shape).
type todoView struct {
	TodoID    string `json:"todo_id"`
	PlanID    string `json:"plan_id"`
	JobID     string `json:"job_id,omitempty"`
	Title     string `json:"title"`
	Done      bool   `json:"done"`
	Sort      int    `json:"sort,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func toTodoView(t jobstore.PlanTodo) todoView {
	return todoView{
		TodoID: t.TodoID, PlanID: t.PlanID, JobID: t.JobID, Title: t.Title,
		Done: t.Done, Sort: t.Sort, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}
```

`planHeaderView`（现状 `server.go:311-323`）置空 Todos（同 Jobs 处理）：
```go
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		Jobs:        make([]jobView, 0),
		Todos:       make([]todoView, 0),
	}
```

> `jobstore.PlanTodo` 供 local 端 `toTodoView` 使用；mcpserver 已 import jobstore（P2）。

**5.3 工具入参 + handler**（`server.go` plan 工具区 `server.go:462-506` 之后新增一节，仿 createPlanHandler）：
```go
// --- gofer_add_todo / gofer_update_todo ------------------------------------

type addTodoToolInput struct {
	PlanID string `json:"plan_id"`
	Title  string `json:"title"`
	JobID  string `json:"job_id,omitempty"`
}

func addTodoHandler(b Backend) mcp.ToolHandlerFor[addTodoToolInput, todoView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in addTodoToolInput) (*mcp.CallToolResult, todoView, error) {
		tv, err := b.AddTodo(in.PlanID, in.Title, in.JobID)
		if err != nil {
			return nil, todoView{}, err
		}
		return nil, tv, nil
	}
}

type updateTodoToolInput struct {
	TodoID string `json:"todo_id"`
	Done   bool   `json:"done"`
}

func updateTodoHandler(b Backend) mcp.ToolHandlerFor[updateTodoToolInput, todoView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in updateTodoToolInput) (*mcp.CallToolResult, todoView, error) {
		tv, err := b.UpdateTodo(in.TodoID, in.Done)
		if err != nil {
			return nil, todoView{}, err
		}
		return nil, tv, nil
	}
}
```

**5.4 注册**（`newServer`，plan 工具注册 `server.go:166-169` `gofer_get_plan` 之后、`return s` 之前）：
```go
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_add_todo",
		Description: "Add a todo to a plan. Omit job_id for a plain checklist item, or set it to bind the todo to a specific job run.",
	}, addTodoHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_update_todo",
		Description: "Mark a todo done (or undone) by todo_id. Returns the updated todo.",
	}, updateTodoHandler(b))
```

**验收**：`go build ./...` 绿（接口加方法后两 Backend 必须都实现，见 T6/T7）；MCP schema 含 `gofer_add_todo`/`gofer_update_todo`；`gofer_get_plan` 输出含 `todos`。

---

### T6 — MCP：localBackend 两实现 + `newTodoID`

**`internal/mcpserver/backend_local.go`**（plan 段末尾，`GetPlan`（`backend_local.go:202-226`）之后）：

**6.1 todo_id 生成**（复刻 `newPlanID`（`backend_local.go:161-163`）；`time`/`job` 已 import）：
```go
func newTodoID() string {
	return "todo-" + time.Now().Format(job.JobIDLayout) + "-" + job.RandomSuffix()
}
```

**6.2 两方法**：
```go
func (b *localBackend) AddTodo(planID, title, jobID string) (todoView, error) {
	st := b.jobs.Meta()
	if _, ok, err := st.GetPlan(planID); err != nil {
		return todoView{}, err
	} else if !ok {
		return todoView{}, fmt.Errorf("unknown plan %q", planID)
	}
	now := time.Now().Unix()
	t := jobstore.PlanTodo{
		TodoID: newTodoID(), PlanID: planID, JobID: jobID, Title: title,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.InsertTodo(t); err != nil {
		return todoView{}, err
	}
	_ = st.TouchPlan(planID)
	return toTodoView(t), nil
}

func (b *localBackend) UpdateTodo(todoID string, done bool) (todoView, error) {
	st := b.jobs.Meta()
	ok, err := st.SetTodoDone(todoID, done)
	if err != nil {
		return todoView{}, err
	}
	if !ok {
		return todoView{}, fmt.Errorf("unknown todo %q", todoID)
	}
	t, _, err := st.GetTodo(todoID)
	if err != nil {
		return todoView{}, err
	}
	return toTodoView(t), nil
}
```

**6.3 GetPlan 内联 todos**（现状 `backend_local.go:202-226`，`pv.Jobs` 填充后、`return pv` 前插入）：
```go
	todos, err := st.ListTodosByPlan(planID)
	if err != nil {
		return planView{}, err
	}
	pv.Todos = make([]todoView, 0, len(todos))
	for _, t := range todos {
		pv.Todos = append(pv.Todos, toTodoView(t))
	}
	return pv, nil
```

> `st := b.jobs.Meta()` 在 GetPlan 起始已声明（`backend_local.go:203`），复用同一变量。

**验收**：localBackend 满足接口（编译过）；AddTodo 对未知 plan 报错、绑 job 存 job_id；UpdateTodo 对未知 todo 报错；GetPlan 内联 todos。`go build ./... && go vet ./...` 绿。

---

### T7 — MCP：clientBackend 两实现 + `clientPlanToView` 内联 todos

**`internal/mcpserver/backend_client.go`**（plan 段 `clientPlanToView`（`backend_client.go:175-194`）之后 / 之内）：

**7.1 两方法**：
```go
func (b *clientBackend) AddTodo(planID, title, jobID string) (todoView, error) {
	t, err := b.cli.AddTodo(planID, title, jobID)
	if err != nil {
		return todoView{}, err
	}
	return clientTodoToView(t), nil
}

func (b *clientBackend) UpdateTodo(todoID string, done bool) (todoView, error) {
	t, err := b.cli.UpdateTodo(todoID, done)
	if err != nil {
		return todoView{}, err
	}
	return clientTodoToView(t), nil
}

func clientTodoToView(t client.Todo) todoView {
	return todoView{
		TodoID: t.TodoID, PlanID: t.PlanID, JobID: t.JobID, Title: t.Title,
		Done: t.Done, Sort: t.Sort, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}
```

**7.2 `clientPlanToView` 内联 todos**（现状 `backend_client.go:175-194`，`for _, j := range p.Jobs` 之后、`return pv` 前插入）：
```go
	pv.Todos = make([]todoView, 0, len(p.Todos))
	for _, t := range p.Todos {
		pv.Todos = append(pv.Todos, clientTodoToView(t))
	}
	return pv
```

> `client`/`job` 已 import；`client.Todo` 由 T4 提供。

**验收**：clientBackend 满足接口；client 模式 `gofer_get_plan` 的 `todos` 与 local 形状一致（非 nil 空片、字段逐字对齐）。`go build ./...` 绿。

---

### T8 — CLI：`plan show` 渲染 todos + `add-todo` / `set-todo` 子命令

> 三通道一致（设计 §8.3：HTTP/CLI 同步暴露）。`client.Plan.Todos` 已由 T4 内联，`plan show` 天然可渲染。

**`internal/commands/plan.go`**：

**8.1 opts + 子命令**（`NewPlanCmd` 的 `Subs` 里，`attach` 之后追加两项；顶部加 opts 变量）：
```go
var planAddTodoOpts = struct {
	job string
}{}

var planSetTodoOpts = struct {
	undone bool
}{}
```
```go
			{
				Name:    "add-todo",
				Aliases: []string{"todo-add"},
				Desc:    "Add a todo to a plan (omit --job for a plain checklist item)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("plan-id", "plan id", true)
					c.AddArg("title", "todo title", true)
					c.StrOpt(&planAddTodoOpts.job, "job", "", "", "bind the todo to a job id (optional)")
				},
				Func: runPlanAddTodo,
			},
			{
				Name:    "set-todo",
				Aliases: []string{"todo-done"},
				Desc:    "Mark a todo done (or --undone)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("todo-id", "todo id", true)
					c.BoolOpt(&planSetTodoOpts.undone, "undone", "", false, "mark the todo NOT done instead")
				},
				Func: runPlanSetTodo,
			},
```

**8.2 Func**（文件末尾新增）：
```go
func runPlanAddTodo(c *gcli.Command, _ []string) error {
	planID, title := argValue(c, "plan-id"), argValue(c, "title")
	if planID == "" || title == "" {
		return fmt.Errorf("plan add-todo requires <plan-id> and <title>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	t, err := cli.AddTodo(planID, title, planAddTodoOpts.job)
	if err != nil {
		return err
	}
	c.Printf("todo %s added to plan %s\n", t.TodoID, planID)
	return nil
}

func runPlanSetTodo(c *gcli.Command, _ []string) error {
	todoID := argValue(c, "todo-id")
	if todoID == "" {
		return fmt.Errorf("plan set-todo requires a <todo-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	t, err := cli.UpdateTodo(todoID, !planSetTodoOpts.undone)
	if err != nil {
		return err
	}
	c.Printf("todo %s done=%t\n", t.TodoID, t.Done)
	return nil
}
```

**8.3 `plan show` 渲染 todos**（`runPlanShow`（`plan.go:111-127`）里 `printPlanJobs` 之后加 `printPlanTodos(c, p.Todos)`；新增 helper）：
```go
func printPlanTodos(c *gcli.Command, todos []client.Todo) {
	c.Println("todos:")
	if len(todos) == 0 {
		c.Println("  (no todos yet)")
		return
	}
	for _, t := range todos {
		box := "[ ]"
		if t.Done {
			box = "[x]"
		}
		bind := ""
		if t.JobID != "" {
			bind = "  (job=" + t.JobID + ")"
		}
		c.Printf("  %s %-26s %s%s\n", box, t.TodoID, t.Title, bind)
	}
}
```

> `client`/`fmt`/`config`/`gcli`/`argValue` 已 import（`plan.go:1-11,145`）；`c.BoolOpt` 是 gcli 标准。

**验收**：`go build ./...` 绿；`go test ./internal/commands/...` 绿；冒烟：`gofer plan add-todo <plan> "验收 E2E" --job <jobID>` → 得 todo_id；`gofer plan set-todo <todo>` → done=true；`gofer plan show <plan>` → todos 段带 `[x]/[ ]`。

---

### T9 — 测试与门禁

**9.1 测试清单**

| 层 | 文件 | 用例要点 |
|---|---|---|
| jobstore | `todos_test.go`（或并入 `plans_test.go`） | 建表/索引存在（T1）；Insert 纯待办(NULL)/绑 job；Get/ListTodosByPlan 排序+过滤；SetTodoDone true/false + 未知 id false；DeleteTodo；空 id/空 plan_id 报错 |
| httpapi | `plan_handler_test.go` | POST todo（纯/绑 job）；plan 404；title 400；PATCH done→true + 未知 404；GET 详情 `todos` 内联 |
| client | `client_test.go` | AddTodo/UpdateTodo 往返；GetPlan `Todos` 内联 |
| mcpserver | `server_test.go` | local：create_plan→add_todo(job_id)→update_todo→get_plan 断言 todos+done；未知 plan/todo→工具 error |
| mcpserver | `backend_client_test.go` | client：mux stub `/v1/plans/{id}/todos`、`/v1/todos/{todo_id}`、`/v1/plans/{id}` 带 todos → 断言 `clientTodoToView`/内联映射 |
| commands | `plan_test.go` | `add-todo`/`set-todo` 流到 client；`plan show` 渲染 todos（若有 commands 层测试骨架） |

> mcpserver 测试参照 `TestPlanToolsRoundTrip`（`server_test.go:317`）+ 工具清单断言 `expectedTools` map（`server_test.go:126-129`，加 `"gofer_add_todo":false`/`"gofer_update_todo":false`）。jobstore 用 `sampleJob`/`withStatus`/`openTest`/`tableExists`/`indexExists`（`jobs_test.go:15,25,448`；`workflows_test.go:234`；`migrate_test.go:24`）。httpapi 用 `newTestServer`/`do`/`decode`/`testToken`（`plan_handler_test.go`）。

**9.2 门禁**
- [ ] `go build ./...` + `go vet ./...` 绿。
- [ ] `go test ./internal/jobstore/... ./internal/httpapi/... ./internal/client/... ./internal/mcpserver/... ./internal/commands/...` 绿。
- [ ] 全量 `go test ./...` 绿（G023 覆盖不降）。
- [ ] `go list -deps ./internal/jobstore/...` 确认 jobstore **未** import job/mcpserver（C3 中立守恒）。
- [ ] 更新总纲 `plan-orchestration-plan.md` 的 P3 checkbox + 各 T 分子阶段提交（SR1202）。

**9.3 运行期冒烟（可选，非阻塞）**
- `gofer serve` 起本地 → HTTP：`POST /v1/plans` → `POST /v1/plans/{id}/todos`（纯 + 绑 job）→ `PATCH /v1/todos/{id}` → `GET /v1/plans/{id}` 见 todos+done。
- MCP：`gofer_create_plan` → `gofer_add_todo` → `gofer_update_todo` → `gofer_get_plan` 内联 todos。
- CLI：`gofer plan add-todo` / `set-todo` / `show`。

---

## 测试清单汇总

见 T9.1 表。核心不变量：
- **两种 todo**：纯待办 `job_id=""`（存 NULL，回读 ""）；绑 job `job_id=<jobID>`（宽松存储，不校验 job 存在）。
- **纯手动 done**：`SetTodoDone` 是唯一改 done 的路径；**无** job 终态自动联动（本期）。
- **详情内联对齐**：HTTP `planDetail.todos`、MCP `planView.todos`、client `Plan.Todos` 三处形状逐字一致（`todo_id/plan_id/job_id/title/done/sort/created_at/updated_at`）。

## 风险

- **R1 done 存储 bool↔int 漂移**（T2）：转换只允许在 jobstore 三点（`scanTodo` 读、`InsertTodo`/`SetTodoDone` 写）；view 层一律直接用 `PlanTodo.Done`（bool），**勿**在 view 里再 int↔bool，否则双重转换出错。单测覆盖 `done=false` 回读（防"零值=未设置"误判）。
- **R2 job_id NULL vs ""**（T2）：纯待办 `JobID=""` 必须存 NULL（`InsertTodo` 用 `any` nil），`selectTodoCols` COALESCE 回 ""。若误存空串本身无害（COALESCE 一致），但保持 NULL 语义与设计 §5.3 对齐。
- **R3 接口加方法即断编译**（T5）：`Backend` 加 `AddTodo/UpdateTodo` 后，localBackend + clientBackend **必须**同批实现，否则 `go build` 红——T5/T6/T7 同一提交内完成。
- **R4 内联三处不同步**（T3/T5/T6/T7）：详情 todos 在 HTTP handler / mcpserver planHeaderView+GetPlan / clientPlanToView 四处填充，漏一处则某通道 todos 恒空。以"planHeaderView 置空片 + GetPlan 覆盖"的既有 jobs 范式为模板逐处对齐。
- **R5 job 终态联动是产品决策**：本期显式不做（留开关）。若后续要自动勾选，单源应落在 job finish/sweeper 钩子调 `SetTodoDone`——届时需先建 `todo.job_id → todo` 反查（或 `ListTodosByJob`），不在 P3 范围。
- **R6 PATCH 支持**：已核 rux `r.PATCH` 存在（`internal/core/router.go:192`）；binding 支持 PATCH body（`pkg/binding/binding.go:39`）。若测试用 `do(t,s,http.MethodPatch,...)` 需确认测试 helper 透传 method（`do` 已按 method 参数化）。

## 待确认

- **D1 job_id 存在性校验**：`add_todo` 的 `job_id` 本期**宽松不校验**（todo 与 job 解耦，允许先建 todo 后有 job / 引用别处 job）。若产品要求 job_id 必须指向已存在 job，则在 handler 加 `GetJob` 校验（404）——**倾向**：宽松，绑定仅作元数据。
- **D2 done 语义与联动**：确认 P3 只做纯手动 `done`（设计 §12 待确认 3 倾向"可勾但不强制自动"）。job 终态自动联动留 P4/后续开关。
- **D3 CLI 命名**：`add-todo`/`set-todo`（别名 `todo-add`/`todo-done`）扁平挂在 `plan` 下（gcli 子命令扁平），未做 `plan todo <sub>` 嵌套二级。若偏好嵌套，需改 Subs 结构——**倾向**：扁平，成本低、与现有 create/list/show/attach 一致。
- **D4 删除面**：`DeleteTodo` 仅落 jobstore（备用 + 单测），本期不暴露 HTTP/MCP/CLI。若需要删除操作，补 `DELETE /v1/todos/{id}` + `gofer_delete_todo` + `plan del-todo`（各 3~5 行）——**倾向**：P3 不做，按需再加。
