# plan 编排 实施计划（总纲）

> 设计：[docs/design/2026-07-09-plan-orchestration-design.md](../../design/2026-07-09-plan-orchestration-design.md)（草案 v0.2，归组键锁定 `plan_id`）
> bd: h-aii-xfvc (epic)
> 触点均已实测定位（2026-07-09 只读探查，见各 P 子计划的 file:line）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿：分期总纲 + P1 代码级子计划 |

## 一句话

plan = 独立于 workflow 引擎的**轻量动态归组层**：`plans` 表存计划元数据，`jobs.plan_id`（**客户端可设**列，区别于引擎私有 `workflow_id`）把陆续产生的独立 job 归到一个计划，三通道（HTTP / CLI / MCP）暴露「建计划 → 挂 job → 查计划（含进度聚合）」。

## 核心约束（贯穿各期，实施须遵守）

- **C1 归组键 = `plan_id`，客户端可设**：`JobRequest.PlanID` 用 `json:"plan_id" yaml:"plan_id"`（**非** `"-"`），与 `WorkflowID`/`StepIndex`/`Attempt`/`FanIndex` 的引擎私有 `"-"` 语义**相反**——这是 plan 与 workflow 的本质区别（设计 §4）。
- **C2 与引擎解耦**：plan 是纯归组，**不推进**、**不占 job 记录**；`plans` 行独立，job 只多一个 `plan_id` 归属列。不改 workflow 引擎语义。
- **C3 jobstore 保持中立**：`internal/jobstore` **绝不 import `internal/job`**（包注释硬约束）。故 `plan_id` 生成（依赖 `job.JobIDLayout`/`job.RandomSuffix`）**不在 jobstore**，放能 import job 的入口层（httpapi）；`jobstore.InsertPlan` 要求调用方传非空 id（仿 `InsertWorkflow`）。
- **C4 加列/加表复用 jobstore 迁移范式**：新列走 `store.go` 的 `migrate() add(...)` ALTER（旧库自动补全 + `selectCols` COALESCE）；新表走 `schemaStmts` 的 `CREATE TABLE IF NOT EXISTS`（幂等 Open）。索引引用新列时放在 `migrate()`（ALTER 之后），不放 `schemaStmts`（`applySchema` 先于 `migrate`，旧库该列尚不存在）。
- **C5 三入口层只做绑定/校验/转发**（G021）：plan 编排/CRUD 逻辑放 jobstore（数据）/ httpapi handler（薄）；`commands`/`mcpserver` 只透传。httpapi 经 `s.jobs.Meta()`（返回 `*jobstore.Store`）访问 plan CRUD，无需新增 `New(...)` 参数。

## 分期

| 期 | 主题 | 范围 | 子计划 | 状态 |
|---|---|---|---|---|
| **P1** | 数据底座 | `plans` 表 + CRUD；`jobs.plan_id` 客户端可设列（+索引）；`JobRequest/JobResult.PlanID`；submit 落库；list `--plan` 过滤（HTTP/CLI/client）；`POST /v1/plans` 建计划 + `POST /v1/plans/{id}/jobs` attach + `gofer plan` CLI | [P1-data-plan.md](./P1-data-plan.md) | ⬜ 未开始 |
| **P2** | MCP + 进度聚合 | `gofer_create_plan` / `gofer_attach_job` / `gofer_get_plan` 工具；`gofer_run_job` 加 `plan_id` 入参（提交即归组）；`GetPlan` 实时聚合其下 jobs 状态 `{total,queued,running,done,failed}` | P2-mcp-aggregate-plan.md（P1 定稿后拆） | ⬜ 未开始 |
| **P3** | todo | `plan_todos` 表（纯待办 / 绑 `job_id` 两种）+ CRUD + HTTP/MCP；job 终态可选联动 done | P3-todo-plan.md（outline，见下） | ⬜ 未开始 |
| **P4** | session 续跑 UI + Plans 前端 | JobDetail「继续会话」入口（续投同 session_id + 继承 plan_id）；`Plans.vue` 列表 + `PlanDetail.vue`；Board 加 plan 过滤维度 | P4-frontend-plan.md（outline，见下） | ⬜ 未开始 |

> P1+P2 即打通「编排即归组」最小闭环（设计 §12 待确认 5 倾向）。P3/P4 增量叠加，不阻塞。

## 进度跟踪

- [ ] P1 数据底座（子任务见 P1-data-plan.md 的 T1..T7）
- [ ] P2 MCP + 进度聚合
- [ ] P3 todo
- [ ] P4 session 续跑 UI + Plans 前端

> SR1202：每个子阶段（T 级）完成后更新对应 checkbox 并 Git 提交，不要攒到最后。

## P3 todo（outline，P2 后细化）

- **表** `plan_todos`（新，`schemaStmts` 建表）：`todo_id TEXT PK / plan_id TEXT idx / job_id TEXT NULL / title TEXT / done INTEGER / sort INTEGER / created_at / updated_at`。两种 todo：`job_id=NULL` 纯待办；`job_id` 非空 = 绑某次 job 执行（终态可联动 `done`）。
- **jobstore CRUD**（todos.go，仿 workflows.go）：`InsertTodo` / `ListTodosByPlan` / `SetTodoDone` / `DeleteTodo`。
- **HTTP**：`POST /v1/plans/{id}/todos`（建）/ `GET /v1/plans/{id}/todos`（`GetPlan` 详情内联）/ `PATCH /v1/todos/{todo_id}`（勾选 done）。
- **MCP**：`gofer_add_todo(plan_id, title, job_id?)` / `gofer_update_todo(todo_id, done)`（设计 §8）。
- **联动**（设计 §12 待确认 3，倾向：可勾但不强制自动）：job 终态 done 时，若有绑定 todo，提供接口勾选但默认不自动。P3 先落纯手动，自动联动留开关。

## P4 session 续跑 UI + Plans 前端（outline）

- **session 续跑**（设计 §7，后端已具备，`job_handler.go:291` resume 沿用同 session_id）：`JobDetail.vue` 对有 `session_id` 的 job 加「继续会话」→ 续投新 job（同 session_id + 继承源 job 的 `plan_id` → 归入同 plan 血缘）。归组随 C1 的 `plan_id` 客户端可设自然打通。
- **Plans 前端**：`web/src/views/Plans.vue`（列表：id/title/status/进度聚合条）+ `PlanDetail.vue`（plan 元数据 + jobs 表 + todos）。`web/src/api/types.ts` 加 Plan/Todo 类型；`web/src/api/` 加 plan 端点封装。
- **Board 过滤**：Board 列表加 `plan` 过滤维度（复用 `?plan=` list 查询，P1 已落）。

## 关键引用（P1 精确触点速查）

| 用途 | 文件:行 |
|---|---|
| jobs 加列范式（migrate add + selectCols COALESCE） | `internal/jobstore/store.go:329-432`；`internal/jobstore/jobs.go:119-233` |
| plans 表参照（建表/CRUD/条件更新） | `internal/jobstore/store.go:151-163`；`internal/jobstore/workflows.go:97-241` |
| list 过滤范式（session where 拼装） | `internal/jobstore/jobs.go:396-401`；`internal/job/list.go:72-122` |
| JobRequest 客户端可设 vs 引擎私有 `"-"` | `internal/job/model.go:92-116`（session_id/plan_id 参照 vs workflow_id） |
| submit 落库 / 持久化互转 | `internal/job/submit.go:231-265`；`internal/job/persistence.go:22-135` |
| HTTP workflow handler + 路由参照 | `internal/httpapi/workflow_handler.go`；`internal/httpapi/server.go:419-426` |
| CLI workflow 命令参照 | `internal/commands/workflow.go`；`internal/commands/job.go:177-191`（list flags） |
| client workflow 方法参照 | `internal/client/client.go:465-539` |
| MCP 工具注册 / run_job 入参 | `internal/mcpserver/server.go:69-155,346-403`；`internal/mcpserver/backend.go:17-46` |
| 核心 wiring（store/engine 装配） | `internal/core/core.go:107-135` |
