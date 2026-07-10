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
| **P1** | 数据底座 | `plans` 表 + CRUD；`jobs.plan_id` 客户端可设列（+索引）；`JobRequest/JobResult.PlanID`；submit 落库；list `--plan` 过滤（HTTP/CLI/client）；`POST /v1/plans` 建计划 + `POST /v1/plans/{id}/jobs` attach + `gofer plan` CLI | [P1-data-plan.md](./P1-data-plan.md) | ✅ 已完成 |
| **P2** | MCP + 进度聚合 | `gofer_create_plan` / `gofer_attach_job` / `gofer_get_plan` 工具；`gofer_run_job` 加 `plan_id` 入参（提交即归组）；`GetPlan` 实时聚合其下 jobs 状态 `{total,queued,running,done,failed}` | [P2-mcp-aggregate-plan.md](./P2-mcp-aggregate-plan.md) | ✅ 已完成 |
| **P3** | todo | `plan_todos` 表（纯待办 / 绑 `job_id` 两种）+ CRUD + HTTP/MCP/CLI；纯手动 done（不做 job 终态自动联动） | [P3-todo-plan.md](./P3-todo-plan.md) | ✅ 已完成 |
| **P4** | session 续跑 UI + Plans 前端 | JobDetail「继续会话」入口（续投同 session_id + 继承 plan_id）；**会话链回看**（同 session 的 job 列表，T9.6）；`Plans.vue` 列表 + `PlanDetail.vue`；Board 加 plan 过滤维度；后端两处小改（T8 resume 继承 plan_id / T10 list 内联 counts） | [P4-frontend-plan.md](./P4-frontend-plan.md)（v0.3 代码级，T1..T11） | ✅ 已完成（2026-07-10，用户眼检通过） |
| **P5** | job 血缘 + 快速重建 | `jobs.source_job_id` 血缘列（服务端盖章、`json:"-"` 不可伪造）；`POST /v1/jobs/{id}/rebuild`（服务端以源 request_json 为基底 + `env_set`/`env_unset`，**env 真值不进请求/响应序列化**）；`rerun` = rebuild 空 body；`GET /v1/jobs/{id}/request` 默认脱敏（关闭 `h-aii-xqe1` 的**直读裸吐**）；`?source_job=` 反查 | [P5-lineage-rebuild-plan.md](./P5-lineage-rebuild-plan.md)（v0.3 代码级，T1..T15） | ✅ 已完成（2026-07-10，对抗式复审后实施；**待用户重部署眼检**） |
| **P6** | plan 生命周期 + 派生门禁 | `PATCH /v1/plans/{id}` 手动置状态（open/active/done/archived）+ client + CLI `plan set-status`/`archive` + PlanDetail 动作区；`ResumeJob`/`RebuildJob` 对**非终态源 job** 返回 `ErrJobNotTerminal`→400，前端两个派生按钮加 `isTerminalView` gate | [P6-lifecycle-and-gate-plan.md](./P6-lifecycle-and-gate-plan.md)（v0.1 代码级，T1..T7） | ✅ 已完成（2026-07-10；**待用户重部署 host server 眼检**） |

> P1+P2 即打通「编排即归组」最小闭环（设计 §12 待确认 5 倾向）。P3/P4 增量叠加，不阻塞。
> P5 由 P4 评审衍生（无 session 的 job 无法 resume → 需「重建」→ 需血缘键），与 plan 编排正交，独立成期。
>
> ✅ **P4/P5 的 merge 冲突点已消解**：`internal/job/resume.go` 的 `s.Submit(JobRequest{...})` 现同时带 `PlanID: src.PlanID`（P4 T8）与 `SourceJobID: jobID`（P5 T4），二者并存、正交。
>
> ⚠️ **P5 的已知安全边界（用户拍板接受）**：rebuild 可覆盖执行体（`prompt`/`cmd`/`agent_args` 等）且继承源 env，故持 token 的 caller 能让新 job 把 env 打进日志再读回。`GET /request` 的脱敏防的是**意外暴露**，不防**恶意 caller 主动提取**（gofer 单信任层）。审计靠血缘列（`source_job_id` + 发起者 `caller_id`）。详见 P5 计划的 §安全声明。

## 进度跟踪

- [x] P1 数据底座（子任务见 P1-data-plan.md 的 T1..T7）
- [x] P2 MCP + 进度聚合
- [x] P3 todo（`plan_todos` 表 + CRUD + HTTP/MCP/CLI 三通道，`a02070f`）
- [x] P4 session 续跑 UI + Plans 前端（T1..T11 全绿 + 用户眼检通过 2026-07-10，**已收官**）。5 commit：T8+T10 后端 / T1-T6 前端 / T7+T9 前端 / client 契约回归修复 / T11 收尾
- [x] P5 job 血缘 + 快速重建（v0.3 定稿并实施，T1..T15 全绿）。4 commit：批1 T1-T5 数据层 / 批2 T6-T10 脱敏+rebuild / 批3 T11-T14 前端 / 测试加固。**待用户 `make web` + 重部署 server 眼检**
- [x] P6 plan 生命周期（完结/归档）+ 派生操作终态门禁（T1..T7 全绿）。2 commit：T1-T3 后端 / T4-T6 前端。**待用户重部署 host server 眼检**（当前 host 仍 P5 版，`gofer plan set-status` 尚未上线）

> SR1202：每个子阶段（T 级）完成后更新对应 checkbox 并 Git 提交，不要攒到最后。

## P3 todo（outline，P2 后细化）

- **表** `plan_todos`（新，`schemaStmts` 建表）：`todo_id TEXT PK / plan_id TEXT idx / job_id TEXT NULL / title TEXT / done INTEGER / sort INTEGER / created_at / updated_at`。两种 todo：`job_id=NULL` 纯待办；`job_id` 非空 = 绑某次 job 执行（终态可联动 `done`）。
- **jobstore CRUD**（todos.go，仿 workflows.go）：`InsertTodo` / `ListTodosByPlan` / `SetTodoDone` / `DeleteTodo`。
- **HTTP**：`POST /v1/plans/{id}/todos`（建）/ `GET /v1/plans/{id}` 详情内联 `todos` / `PATCH /v1/todos/{todo_id}`（勾选 done）。
- **MCP**：`gofer_add_todo(plan_id, title, job_id?)` / `gofer_update_todo(todo_id, done)`（设计 §8）。
- **CLI**：`gofer plan add-todo` / `gofer plan set-todo` / `gofer plan show` 渲染 todos。
- **联动**（设计 §12 待确认 3，倾向：可勾但不强制自动）：P3 只落纯手动 `done`，自动联动留后续开关。

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
