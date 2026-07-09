# gofer plan 编排设计（草案 v0.1）

> bd: h-aii-xfvc (epic) ｜ 来源：2026-07-09 使用摩擦审计（iss-0709 §新想法 + §优化增强 + §讨论点3）
> 状态：**草案待审**。定稿后拆 epic 子任务与实施计划。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿：plan 一等实体 + todo + session 续跑 + MCP，待审 |

## 1. 背景与问题

iss-0709 里有 4 处需求，指向**同一件事**——「把一个完整计划里动态派生的多个 job 关联起来、可跟进」：

- §新想法：todo 任务列表，可分配到 job 上跟进进度，也可以是普通待办。
- §优化增强：有 session_id 的一次性 job，想用**同会话**继续下一轮（job 续投 或 pty 继续）。
- §讨论点3：主控编排调度 host codex 做 plan 子任务开发，但 gofer 记录里**这几个 job 关联不起来**；要不要接 MCP？串起来是不是也建一个 job 记录？

**现状（已核实代码）**：
- gofer **唯一**的多 job 串联器是 `internal/job/workflow` 引擎，但它只能编排**提交时就一次性声明好、由引擎串行推进**的 step（`workflow/types.go:110 Spec{Steps[]}`、`workflow/submit.go:23 SubmitWorkflow`）。它**无法**把"跑着跑着才决定派生、且各自独立提交"的 job 事后归到一组——**正是主控派单 host codex 的形态**。
- job 级**没有客户端可设的归组键**：`workflow_id` 是引擎私有、`model.go:92-97` 里 json/yaml tag `"-"` **禁止客户端伪造**；只有 `origin_agent`（谁派的，`model.go:128-134`）和 `session_id`（CLI 会话，`model.go:111-116`）两个**弱关联**。
- `session_id`：resume 会**新起一个 job 但沿用同一 session_id**（`httpapi/job_handler.go:291`），是"同一对话血缘"的**事实弱链接**，但语义是 CLI 会话续接，不是计划编排。
- MCP（`internal/mcpserver/server.go:69-152`，16 工具）能提交/查**单个** job、agent 间派单互答，但**对 workflow/plan 零暴露**。
- **todo 实体完全空白**（grep 只有代码注释 `// TODO`）。

## 2. 名词（关键：三者正交，别混）

| 概念 | 是什么 | 谁驱动 | 现状 |
|---|---|---|---|
| **workflow** | 提交时**静态声明**的串行 job 链（step 依赖/传值/fan-out/子工作流） | gofer 引擎自动推进 | 已有 |
| **plan（本设计新增）** | **动态归组**容器：把陆续产生的独立 job 挂进来 + 跟进进度 | 外部（主控/人）挂载 | 空白 |
| **session** | 底层 agent CLI（claude/codex）的会话标识，用于 resume 续接 | agent CLI | 已有（缺 UI 入口） |

一句话：**workflow = 静态声明的自动串行执行；plan = 动态归组 + 进度跟进；session = 单条对话血缘。** 一个 plan 里**可以**同时含若干独立 job、若干 workflow、若干 session 血缘。

## 3. 目标 / 非目标

**目标**
- G1 提供 **plan 一等实体**：动态把已产生的独立 job 归到一个计划，查询"这个计划下所有 job + 状态"。
- G2 **进度聚合**：plan 自动汇总其下 job 的状态（total/running/done/failed）+ 可选人工进度。
- G3 **todo**：计划下可挂 todo 项（可绑定某 job，也可纯待办），跟进 checkbox 进度。
- G4 **session 续跑打通**：UI 上对有 session_id 的 job 一键"继续"（新起 job 续投 或 pty 继续），并自然归入同一 plan/血缘。
- G5 **MCP 暴露**：主控 agent 能经 MCP 建计划 / 挂 job / 查计划，实现"编排即归组"。

**非目标**
- 不改 workflow 引擎语义（plan 不替代 workflow；二者共存）。
- 不做甘特图/依赖图等重编排（plan 是归组+跟进，不是 DAG 调度）。
- 不引入外部 DB（延续 jobstore 的加表/迁移范式）。

## 4. 方案总览

**最小落点二选一（已评估）**：

| 方案 | 做法 | 评价 |
|---|---|---|
| **(a) jobs 加客户端可设列 `plan_id`（推荐）** | 像 `session_id`/`tags` 那样加列+索引+过滤（参照 `jobstore/store.go:334-390` 加列范式、`jobs.go:399` session 过滤）；新增 `plans` 表存 plan 元数据/进度 | 与 workflow 引擎**解耦**（纯归组不推进）；改动小、语义清晰 |
| (b) 放宽 `workflow_id` 允许外部把已存在 job 挂到"容器工作流" | 复用 `workflows` 表 | 要改 `model.go:92-97` 故意禁写的契约 = **安全面变更**，且把"归组"和"引擎推进"耦合，语义混淆 |

→ **选 (a)**：`plan` 是独立于 workflow 的轻量归组层。

## 5. 数据模型

沿用 jobstore 加表/迁移范式（`jobstore/store.go` 的 `add(...)` 加列、建索引）。

### 5.1 `plans` 表（新）
```txt
plan_id      text PK        -- 如 plan-20260709-xxxx
title        text
description  text
status       text           -- open / active / done / archived
owner        text           -- 创建者 agent_id / 人
progress     int            -- 0..100 可选人工进度
created_at / updated_at  int
```

### 5.2 `jobs` 表加列（新，客户端可设）
```txt
plan_id      text  NULL, index   -- 客户端可设的归组键（区别于引擎私有 workflow_id）
```
- 与 `session_id`/`workflow_id` 并列；list 加 `--plan <id>` 过滤（参照 `jobstore/jobs.go:399` session 过滤的 where 拼装）。
- `plan_id` **允许客户端设置**（与 `workflow_id` 的 `"-"` 语义相反）——这是二者的本质区别。

### 5.3 `plan_todos` 表（新）
```txt
todo_id      text PK
plan_id      text  index
job_id       text  NULL       -- 可绑定某 job（进度随 job 状态联动）；NULL=纯待办
title        text
done         bool
sort         int
created_at / updated_at  int
```
- 复用 `messages`(kind=task) 底座可选，但 todo 需要 done/进度语义，独立表更清晰。

## 6. plan 生命周期与进度聚合

```txt
create plan(open) → attach job(s) / add todo(s) → 进度自动聚合(active) → 全部终态 → done → archived
```
- **进度聚合**：查询 plan 时实时聚合其下 jobs 状态 `{total, queued, running, done, failed}`；`progress` 字段供人工覆盖/补充语义。
- 复用 jobstore 现有 job 终态事件（sweeper/finish 钩子）触发 plan.updated_at 刷新；不引入新调度。

## 7. session 续跑打通（G4）

后端 **已具备**（resume 新起 job 沿用同 session_id，`job_handler.go:291`），缺的只是**入口 + 归组**：
- **UI**：JobDetail 页对有 `session_id` 的 job 增"继续会话"操作 → 走两条路之一：
  - ① 续投新 job（同 session_id）+ 可带 plan_id → 归入同 plan。
  - ② pty 继续（如是 interactive session）→ attach 现有 pty 能力（P2/P3 已实现 attach）。
- **归组**：续投 job 自动继承原 job 的 `plan_id`（若有），使"一次会话里多轮"天然归到同一 plan 血缘。
- 对应 iss-0709「使用中触发 job 产生了有价值输出，但没法继续」的诉求。

## 8. MCP 暴露（G5）

`internal/mcpserver/server.go` 新增 plan 工具面（与现有 `gofer_run_job` 并列）：
- `gofer_create_plan(title, description)` → plan_id
- `gofer_attach_job(plan_id, job_id)` / 或 `gofer_run_job` 增可选 `plan_id` 参数（提交即归组，**更顺**）
- `gofer_get_plan(plan_id)` → plan 元数据 + jobs 聚合 + todos
- `gofer_add_todo(plan_id, title, job_id?)` / `gofer_update_todo(todo_id, done)`

→ 回答用户三问：
1. "要不要接 MCP"：**要**，主控编排时经 MCP 建计划并把每个派生 job 带上 `plan_id`，即"编排即归组"。
2. "串起来是不是也建一个 job 记录"：**不是**。plan 是独立轻量实体（`plans` 行），**不占 job 记录**；job 只是多一个 `plan_id` 归属。
3. HTTP/CLI 同步暴露（`POST /v1/plans` 等），保持三通道一致（现 workflow 仅 HTTP+CLI，plan 补齐 MCP）。

## 9. 关键流程（主控派单 host codex → plan 归组）

```txt
主控(claude) ──MCP gofer_create_plan("重构X")──▶ server: plans 行(plan-A, open)
   │
   ├─ MCP gofer_run_job(codex, prompt=子任务1, plan_id=plan-A) ──▶ job-1 (plan_id=plan-A)
   ├─ MCP gofer_run_job(codex, prompt=子任务2, plan_id=plan-A) ──▶ job-2 (plan_id=plan-A)
   └─ gofer_add_todo(plan-A, "验收 E2E", job_id=job-2)
        │
   查询 gofer_get_plan(plan-A) ──▶ {jobs:[job-1 done, job-2 running], todos:[...], progress}
```
→ 现在 web `Plans` 视图 / MCP 都能看到"这个计划下的所有 job 与进度"，解决"关联不起来"。

## 10. UI（后续，非本轮）
- 新增 `Plans.vue` 列表 + `PlanDetail.vue`（plan 元数据 + jobs 表 + todos）。
- Board 增 `plan` 过滤维度。
- 均属 plan epic 后续子任务，本轮 UI 重构 A+B 不含。

## 11. 分期建议（供拆 epic 子任务）
- **P1 数据底座**：plans 表 + jobs.plan_id 列 + list 过滤 + `POST /v1/plans` / attach（CLI）。
- **P2 MCP + 进度聚合**：MCP 工具面 + get_plan 聚合。
- **P3 todo**：plan_todos 表 + CRUD + MCP/HTTP。
- **P4 session 续跑 UI + Plans 前端**。

## 12. 待确认

1. 归组键命名：`plan_id`（本文）确认？还是 `group_id`（更泛化，未来可承载非 plan 的分组）？
2. `gofer_run_job` 直接加 `plan_id` 参数（提交即归组）vs 单独 `attach_job`——是否两者都要？
3. plan 与 workflow 是否需要交叉引用（一个 workflow 整体归入某 plan）——`workflows` 表加 `plan_id`？（倾向：需要，workflow 也是 plan 的一员）
4. todo 绑定 job 时的进度联动语义：job 终态 done 是否自动勾选 todo？（倾向：job done→todo 可勾但不强制自动）
5. 是否需要 plan 级权限/owner 校验（谁能 attach/archive）。
6. 分期是否按 §11，先做 P1+P2 打通"编排即归组"最小闭环即可验证价值？
