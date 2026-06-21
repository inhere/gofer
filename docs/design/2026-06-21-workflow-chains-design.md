# Gofer 工作流（job 链）— 设计方案

> 一句话：让用户提交**一串有序的 job**（step），上一步的产出（result.json / result_dir / stdout / exit_code）**喂给下一步**，gofer 串起来自动跑——`codex 生成 → exec 跑测试 → claude 评审` 一次提交、逐步推进、全程可观察可审计。
> roadmap [`../2026-06-20-enhancements-roadmap.md`](../2026-06-20-enhancements-roadmap.md) **E7 多步/工作流**（最大"完成任务"杠杆、最大范围，独立 epic）。v1 **线性 chain + `${steps.N.xxx}` 引用**；DAG/并行（E9）后续。承接 E1 产物/E6 result.json/E13 事件/`GOFER_RESULT_DIR`（它们正是 step 间传值的载体）。bd epic 待建。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-21 | Claude | 初版：线性链模型 + 推进引擎 + `${steps.N}` 引用 + §11 决策。 |

## 1. 概览

### 1.1 背景与缺口（事实，带 file:line）
- **只能单步**：`Service.Submit(JobRequest)`（`service.go:172`）一次只起一个 job；要"生成→测试→评审"得人工把上一步产出粘到下一步提交。
- **产出已可取，但无人串**：一个 done job 的 `ResultJSON`(E6)/`ResultDir`/`ExitCode`/`ArtifactsJSON`(E1) 经 `Get(id)`（`cancel.go:12`）即可拿到；result.json 落 `<result_dir>/result.json`——**喂下一步的料齐了，缺的是编排**。
- **终态汇聚点现成**：`finish()`（`service.go:453-498`）是所有 job 到终态的唯一汇聚（刚加过 `recordEvent(terminal)` `:467`），是"推进下一步"的天然挂点。
- **可靠推进有范式**：`event_deliveries` 的 `ClaimDueDeliveries` 条件 UPDATE 抢占（`deliveries.go:159-172`，RowsAffected==1）+ `startDeliveryLoop` sweeper（`serve.go:192-230`）——工作流"幂等推进 + crash 兜底"可照搬。
- **替换有先例**：`agent.Render` 的 `{{result_dir}}` 等（`template.go:35-42`，仅 cli-agent args）；`${steps.N}` 是**跨 step** 的另一层替换。

### 1.2 目标
| 编号 | 目标 |
|---|---|
| G1 | 一次提交 **N 个有序 step**，gofer 串行自动推进（step i 成功 → 起 step i+1） |
| G2 | step 间传值：`${steps.N.result_dir/result/stdout/exit_code/status/job_id}` 注入下一步 prompt/cmd/cwd/env |
| G3 | 工作流可查询/取消，每个 step 仍是普通 job（详情/日志/事件/产物照旧），整体可观察可审计 |

### 1.3 非目标
- **不做** DAG / 并行 fan-out（一个 step 派多 agent、菱形依赖）——**E9**，v1 严格串行单活跃 step。
- **不做** 每 step 复杂失败策略（retry/continue/分支）——v1 **fail-fast**（任一 step 失败→工作流失败）。
- **不做** 循环/条件跳转（while/if-goto）。
- **不做** 跨工作流引用、人审批门（E8 相邻）。

## 2. 名词
- **工作流 (workflow)**：一个有序 step 列表 + 头信息（id/status/当前步）。提交后由引擎自动推进。
- **步 (step)**：工作流的一环 = 一个 **job 规格**（project/agent/runner/prompt|cmd/cwd/timeout/tags）+ 可含 `${steps.N}` 引用。每个 step 落地为**一个普通 job**。
- **引用 (reference)** `${steps.N.field}`：在 step 提交前，把**前序 step N** 的产出替换进本 step 的字符串字段。
- **推进 (advance)**：step i 到终态后，引擎决定（失败→终结 / 成功且有后续→起 step i+1 / 成功且末步→工作流 done）。

## 3. 范围与分期

| 阶段 | 内容 | 依赖 | 风险 |
|---|---|---|---|
| **P1** | **引擎地基**：`workflows` 表 + jobs 加 `workflow_id`/`step_index` 列 + 提交 API + **幂等推进**（finish 异步钩子 + sweeper 兜底）+ 串行 + fail-fast + 取消。**无引用**（各 step 独立跑） | 无 | 高 |
| **P2** | **`${steps.N}` 传值**：引用文法 + 解析器（前序产出替换进下一步 prompt/cmd/cwd/env）+ 提交期校验（拒未来/自引用、越界）+ 大数据走 result_dir | P1 | 中 |
| **P3** | **CLI + Web**：`gofer workflow run/show/list/cancel` + Web 工作流列表/详情（step 链，各链到 job 详情） | P1（P2 增强展示） | 低-中 |

> **顺序**：P1（能串起来跑）→ P2（传值，真正"上一步喂下一步"）→ P3（顺手用、看得见）。每阶段绿灯即提交（SR1202）。

## 4. 架构与关键改动

```txt
提交 workflow def(steps[]) ──▶ POST /v1/workflows ──▶ 引擎: 建 workflows 行(status=running,current_step=1)
                                                          │  起 step1 job(workflow_id,step_index=1, caller 继承)
                                                          ▼
   job(step i) 跑完 ──▶ finish()(:467 之后) ──▶ 若 job.workflow_id 非空: go advanceWorkflow(wfID)   ← 快路径
                                          (并行兜底) startWorkflowLoop sweeper 扫 running 工作流  ← crash 兜底
                                                          ▼
   advanceWorkflow(wfID)  [幂等: 条件 UPDATE current_step 抢占, RowsAffected==1 才推进]
     ├─ 前步 failed/timeout/cancelled → workflows.status=failed (fail-fast, 终止)
     ├─ 前步 done 且有后续 → 解析 ${steps.N}(P2) → Submit(step i+1 job)
     └─ 前步 done 且末步 → workflows.status=done
                                                          ▼
   GET /v1/workflows/{id}  (头 + 各 step: job_id/status)   ·  POST /v1/workflows/{id}/cancel
        └─▶ CLI workflow run/show/list/cancel · Web 工作流详情(step 链 → job 详情)
```

**改动面**：
- 中-高：P1 新 `workflows` 表 + jobs 加 2 列 + `advanceWorkflow`（幂等核心）+ finish 钩子 + sweeper + 提交/查询/取消 API。
- 中：P2 `${steps.N}` 解析器 + 提交期校验。
- 低-中：P3 CLI 命令 + Web。

## 5. 模块详设

### 5.1 数据模型（P1）
新表 `workflows`（schemaStmts IF NOT EXISTS，仿现有 4 表）：
```sql
CREATE TABLE IF NOT EXISTS workflows (
  id           TEXT PRIMARY KEY,
  title        TEXT,
  status       TEXT NOT NULL,        -- running / done / failed / cancelled
  current_step INTEGER NOT NULL,     -- 1-based；推进时条件 UPDATE 此列做幂等屏障
  total_steps  INTEGER NOT NULL,
  spec_json    TEXT NOT NULL,        -- 完整 step 定义(含引用原文)，引擎推进时读
  caller_id    TEXT,
  error        TEXT,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_workflows_status ON workflows(status);
```
jobs 表加 2 列（`migrate()` ADD COLUMN，仿 tags_json）：`workflow_id TEXT`、`step_index INTEGER`（普通 job 为空/0；step-job 标注归属）。`JobResult`/`JobRequest` 加对应字段（`WorkflowID`/`StepIndex`，`JobRequest` 的由引擎内部设、不对外提交）。
- DAO `workflows.go`：`InsertWorkflow` / `GetWorkflow` / `ListWorkflows` / **`AdvanceCurrentStep(wfID, from, to int) (bool,error)`**（`UPDATE workflows SET current_step=?,updated_at=? WHERE id=? AND current_step=? AND status='running'`，RowsAffected==1 才 true=抢到推进权，SR303）/ `SetWorkflowStatus(wfID, status, error)` / 关联 `ListWorkflowJobs(wfID)`（`jobs WHERE workflow_id=? ORDER BY step_index`）。

### 5.2 提交（P1）
- `WorkflowSpec`：`{title, steps:[]StepSpec}`；`StepSpec`：`{name?, project_key, agent, runner, prompt?, cmd?, cwd?, timeout_sec?, tags?}`（= 单 job 规格子集；**不含** caller/sync/request_id）。
- `Service.SubmitWorkflow(spec, callerID) (Workflow, error)`：校验（≥1 step；每 step project/agent/runner 合法，复用 `validate`；P2 再校验引用）→ 建 `workflows` 行（running/current_step=1/spec_json）→ 起 step1：用 step1 规格构 `JobRequest`（`WorkflowID=wfID, StepIndex=1, CallerID=callerID`）调 `Submit` → 返回 workflow 快照。
- 提交格式：**JSON**（`POST /v1/workflows`）+ **yaml 工作流文件**（CLI 解析 `steps:` 列表，P3）。md-per-step 后续。

### 5.3 推进引擎（P1，最关键）
**触发**：① 快路径——`finish()` 末尾（recordEvent terminal 后）若 `job.WorkflowID!=""` 起 `go s.advanceWorkflow(job.WorkflowID)`（异步，绝不阻塞 finish/不改 entry.done 时序）；② 兜底——`startWorkflowLoop`（仿 startPruneLoop，ticker 如 30s）扫 `status='running'` 工作流，对其当前 step 的 job 已终态却未推进的补推（crash 恢复，SR304 兜底）。两者走同一 `advanceWorkflow`。
**`advanceWorkflow(wfID)`（幂等）**：
1. 取 `workflows`（非 running → return）；取当前 step 的 job（`ListWorkflowJobs` 找 step_index==current_step 的 job）；job 未终态 → return（还没跑完）。
2. **抢推进权**：`AdvanceCurrentStep(wfID, cur, cur+1)`——RowsAffected==0（别人已推进/状态变了）→ return（幂等保证单次推进）。
3. 抢到后按前 step 终态：
   - `failed/timeout/cancelled` → `SetWorkflowStatus(failed)` + 记 `workflow.failed` 事件，终止（fail-fast，D4）。
   - `done` 且 `cur < total` → 解析 step(cur+1) 的 `${steps.N}`（P2；P1 无引用直接用）→ 构 `JobRequest(WorkflowID,StepIndex=cur+1,CallerID=wf.CallerID)` → `Submit`。Submit 失败 → `SetWorkflowStatus(failed)`。
   - `done` 且 `cur == total` → `SetWorkflowStatus(done)`。
> caller_id：step-job 继承 `workflows.caller_id`（非 HTTP 上下文，调用方显式设，recon ④）。advance 全程 best-effort 包裹日志，单 step 提交失败只让该工作流 failed、不影响别的。

### 5.4 `${steps.N}` 传值（P2）
- **文法**：`${steps.<N>.<field>}`，N=1-based 前序 step 序号，field ∈ `result_dir | result | stdout | exit_code | status | job_id`。
  - `result_dir`：前步 `ResultDir`（路径；**大数据首选**——下一步读该目录文件，配合 `GOFER_RESULT_DIR` 同款）。
  - `result`：前步 `ResultJSON` 原文（result.json，inline，**上限如 32KB**，超则报错提示改用 result_dir）。
  - `stdout`：前步 stdout tail（上限如 32KB）。
  - `exit_code`/`status`/`job_id`：标量。
- **解析点**：`advanceWorkflow` 起 step i+1 前，对该 step 规格的字符串字段（prompt、cmd 各元素、cwd、env 值）做替换：取 `${steps.N.field}` → `Get(stepN.jobID)` 拿对应产出 → 替换。逐字段替换（不 shell 重切，安全，仿 Render）。
- **校验**（SubmitWorkflow 提交期）：引用的 N **必须 < 当前 step**（拒自引用/未来引用）、N≥1、field 合法；否则 400。运行期 N 步缺产出（如 result.json 不存在引用 result）→ 该 step 提交失败→工作流 failed（带清晰 error）。
- 与 `{{}}` 正交：`${}`（工作流引擎，跨 step）先解析 → 再常规 Submit（agent.Render 的 `{{}}` 在 job 内）。

### 5.5 取消（P1）
- `Service.CancelWorkflow(wfID)`：`SetWorkflowStatus(cancelled)`（阻止后续推进——advanceWorkflow 见非 running 即停）+ 取消当前 running step 的 job（`Cancel(curJobID)`）。幂等（已终态工作流 no-op）。

### 5.6 事件/审计（P1，复用 E13）
- 每个 step-job 自带完整 E13 事件流（submitted→running→terminal）。工作流级状态在 `workflows`（status/current_step）。v1 **不另起 workflow 事件流**；详情页按 step 链聚合展示各 job 状态即可（D9）。可选记 `workflow.*` 事件留后续。

### 5.7 API（P1/P3）
| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/workflows` | 提交工作流（JSON `{title,steps[]}`）→ 工作流快照 |
| GET | `/v1/workflows/{id}` | 头 + 各 step（step_index/name/job_id/status） |
| GET | `/v1/workflows` | 列表（按 status/项目过滤，仿 jobs list） |
| POST | `/v1/workflows/{id}/cancel` | 取消 |
- handler 风格仿 `handleGetJob`（取→404 writeError→c.JSON）；路由进 `/v1` authed 组（`server.go:186-231`）。

## 6. 数据模型小结
新表 `workflows`（§5.1）+ jobs 加 `workflow_id`/`step_index` 两列（additive migrate）。step 定义存 `workflows.spec_json`（含引用原文，推进时按需解析）。retention：prune 时一并清 workflow（或保留 workflows 直至其所有 step-job 被 prune——§11-D 决定；v1 简单：prune job 不动 workflows，加独立 workflow retention 留后续）。

## 7. 安全
- 每 step 仍过 `validate`（project/agent/runner 白名单、exec gate），**工作流不绕过单 job 的任何准入**。
- `${steps.N.result/stdout}` inline 有上限（防大注入/DB 膨胀）；result/stdout 可能含敏感内容——文档提示（与单 job result.json 同责，SR403 agent 侧）。引用只在同工作流内、同 caller。
- 取消/查询在 `/v1` 鉴权内；工作流 caller_id 继承提交者。
- 推进抢占用条件 UPDATE（SR303）防重复起 step（**关键正确性**：一个 step 绝不被起两次）。

## 8. 部署
无新部署面：一张 additive 表 + 两列 + serve 多挂一个 sweeper goroutine（仿 prune/delivery）+ 既有 HTTP。

## 9. 关键流程（codex→exec→claude 示例）
```yaml
title: 生成-测试-评审
steps:
  - name: gen     # step1
    project_key: my-proj
    agent: codex
    prompt: 在 ${{cwd}} 实现 X，把改动写到 result_dir
  - name: test    # step2：用 step1 的产出目录跑测试
    project_key: my-proj
    agent: exec
    cmd: [bash, -c, "cd ${steps.1.result_dir} && go test ./..."]
  - name: review  # step3：把 step2 结果喂给 claude 评审
    project_key: my-proj
    agent: claude
    prompt: "测试结果(exit=${steps.2.exit_code}):\n${steps.2.stdout}\n请评审 ${steps.1.result_dir} 的改动"
```
引擎：起 gen→done→起 test（替换 `${steps.1.result_dir}`）→done→起 review（替换 step2 stdout/exit）→done→工作流 done。任一步失败即 fail-fast。

## 10. 模块/文件落点（实现指引）
- `internal/jobstore/`：`store.go`(workflows 表 + jobs 2 列 migrate) + `workflows.go`(DAO)。
- `internal/job/`：`workflow.go`（`WorkflowSpec`/`StepSpec`/`SubmitWorkflow`/`advanceWorkflow`/`CancelWorkflow`）+ `refs.go`（P2 `${steps.N}` 解析）+ `service.go` finish 钩子 + `model.go`(JobResult/Request 加 WorkflowID/StepIndex)。
- `internal/commands/serve.go`：`startWorkflowLoop` 挂载。
- `internal/httpapi/`：`workflow_handler.go` + `server.go` 路由。
- `internal/commands/`：`workflow.go`（CLI，P3）。
- `web/`：workflows 列表/详情视图 + api（P3）。

## 11. 待确认事项（决策点，附推荐）
- **D1（模型）**：v1 **线性 chain**，DAG/并行留 E9（推荐：是）
- **D2（持久化）**：`workflows` 表(头+spec_json) + jobs 加 `workflow_id`/`step_index` 列 + **条件 UPDATE current_step 做推进幂等**（不另建 workflow_steps 表）（推荐：是，最省且幂等）
- **D3（推进触发）**：finish 异步钩子（快）+ sweeper 兜底（crash 恢复），共用 `advanceWorkflow`（推荐：是）
- **D4（失败策略）**：v1 **fail-fast**（任一 step 失败→工作流 failed）；per-step on_failure/retry 留后续（推荐：是）
- **D5（引用文法）**：`${steps.N.result_dir|result|stdout|exit_code|status|job_id}`，N=1-based 前序；result/stdout inline 上限 32KB、大数据走 result_dir（推荐：是）
- **D6（提交格式）**：JSON + yaml 工作流文件（`steps:` 列表）；md-per-step 留后续（推荐：是）
- **D7（取消语义）**：CancelWorkflow = 标 cancelled（阻后续）+ 取消当前 step job（推荐：是）
- **D8（caller 继承）**：engine 起的 step-job 继承 workflow.caller_id（推荐：是）
- **D9（工作流事件）**：v1 不另起 workflow 事件流，靠 workflows.status + 各 step job 的 E13 事件；workflow 级事件留后续（推荐：是）
- **D10（并发度）**：v1 工作流内**严格串行单活跃 step**；并行/fan-out = E9（推荐：是）
- **D11（retention）**：v1 prune job 不动 workflows（避免悬挂引用），workflow 独立清理留后续（推荐：是，简单）
- **D12（bd）**：一个 epic 三阶段（P1 引擎 / P2 引用 / P3 CLI+Web）（推荐：是）

## 12. 结论
v1 工作流 = `workflows` 表（头+spec）+ jobs 加 2 列 + **幂等推进引擎**（finish 异步钩子 + sweeper 兜底，条件 UPDATE 抢推进权）+ `${steps.N}` 跨 step 替换。每个 step 仍是普通 job，复用全部既有能力（产出/事件/日志/取消/产物）。最大复用：`finish` 终态汇聚、`ClaimDueDeliveries` 抢占范式、`startDeliveryLoop` sweeper 范式、`Get(id)` 产出读取、`agent.Render` 替换思路、jobstore 加表/加列模板。最大正确性点是**推进幂等**（一个 step 绝不起两次），最大范围是引用解析与端到端编排。

**下一步**：审核（重点过 §11 决策，尤其 D2 持久化/幂等、D4 fail-fast、D5 引用文法）→ 通过后出分阶段 `plan`（P1–P3，细到表/DAO/引擎函数/handler/CLI/Web 与验收），再按 SUPMODE 实施。
