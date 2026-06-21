# P3 — 工作流 CLI + Web（实施计划）

> 主纲：[`2026-06-21-workflow-chains-plan.md`](./2026-06-21-workflow-chains-plan.md) · 设计 §5.7/§10。
> 让工作流顺手用、看得见：`gofer workflow run/show/list/cancel` + Web 工作流列表/详情。依赖 P1（P2 增强展示）。

---

## P3-a CLI `gofer workflow`

### 落点
- `internal/client/client.go`：`SubmitWorkflow(spec)` / `GetWorkflow(id)` / `ListWorkflows(status)` / `CancelWorkflow(id)`（仿 ListJobs/GetJob，调 `/v1/workflows*`）。
- `internal/commands/workflow.go`（新）：`NewWorkflowCmd()`，子命令 run/show/list/cancel；`app.go` 注册 `app.Add(NewWorkflowCmd())`。

### 步骤
- **`workflow run <file>`**：读 yaml 工作流文件(`steps:` 列表，`yaml.Unmarshal` 到 `job.WorkflowSpec`)→`cli.SubmitWorkflow(spec)`→打印 wfID + step 概览；`--watch` 则轮询/SSE 跟进（v1 可简单：循环 `GetWorkflow` 直到终态，打印各 step 状态变化）。
- **`workflow show <id>`**：`cli.GetWorkflow(id)`→表格(step_index/name/job_id/status + 工作流 status)。
- **`workflow list`**：`--status` 过滤→表格。
- **`workflow cancel <id>`**：`cli.CancelWorkflow(id)`→打印结果。
- yaml 文件格式同设计 §9 示例（title + steps[]）。

### P3-a 验收
- 单测 `internal/commands`：workflow 子命令含 run/show/list/cancel（注册 + 选项绑定）；`run` 解析 yaml 文件→WorkflowSpec 正确。
- 真机：`gofer workflow run wf.yaml` 起工作流、`show`/`list` 正确、`cancel` 生效。

---

## P3-b Web 工作流列表 + 详情

### 落点
- `web/src/api/types.ts`：`Workflow { id, title, status, current_step, total_steps, caller_id, created_at, steps: WorkflowStep[] }`、`WorkflowStep { step_index, name, job_id, status }`。
- `web/src/api/client.ts`：`listWorkflows`/`getWorkflow`/`cancelWorkflow`。
- `web/src/views/`：`Workflows.vue`(列表) + `WorkflowDetail.vue`(头 + step 链，每 step 链到对应 job 详情 `/jobs/{job_id}`)；路由 + 导航入口。

### 步骤
- 列表：仿现有 Board/Jobs 列表风格，status 过滤，行链到详情。
- 详情：工作流头(status/current_step/total) + step 序列（每 step：序号/name/状态徽标/→ job 详情链接）；running 工作流轮询刷新（仿 Board 2.5s）；取消按钮。
- 路由注册 + 顶部导航加"Workflows"入口。

### P3-b 验收
- `pnpm -C web build` 绿（含 vue-tsc）。
- 真机：Web 工作流列表展示、点入详情见 step 链、各 step 链到 job 详情、取消可用。

### 提交点
P3-a / P3-b 各绿灯分别 `git commit`；更新主纲进度全勾 + 出**完成报告**（SR1430）。

> 范围注记：CLI `--watch` v1 用轮询 `GetWorkflow`（工作流级 SSE 留后续）；Web 详情轮询刷新（仿 Board）。
