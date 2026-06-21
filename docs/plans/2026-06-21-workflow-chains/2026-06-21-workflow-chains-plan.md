# Gofer 工作流（job 链）— 实施计划（总纲）

> 设计依据：[`../../design/2026-06-21-workflow-chains-design.md`](../../design/2026-06-21-workflow-chains-design.md)（v0.1，§11 决策全按推荐采纳）。
> bd epic：`example-project-4dn`（P1 `example-project-6ym` / P2 `example-project-yeo` / P3 `example-project-ajw`）。本文件只保留**总纲 + 进度跟进 + 阶段简述**（SR1105）；阶段详情见子文档。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-21 | Claude | 初版：P1–P3 拆分 + 全局验收门 + 进度跟进骨架。 |

## 已采纳决策（design §11，全按推荐）

- **D1/D10** v1 **线性 chain + 严格串行单活跃 step**；DAG/并行 = E9 留后续。
- **D2** `workflows` 表(头+spec_json) + jobs 加 `workflow_id`/`step_index` 列 + **条件 UPDATE `current_step` 抢推进权**（不另建 workflow_steps 表）。
- **D3** 推进触发：`finish` 异步钩子（快）+ `startWorkflowLoop` sweeper 兜底（crash 恢复），共用 `advanceWorkflow`。
- **D4** v1 **fail-fast**（任一 step 失败 → 工作流 failed）。
- **D5** 引用 `${steps.N.result_dir|result|stdout|exit_code|status|job_id}`，N=1-based 前序；result/stdout inline 上限 32KB、大数据走 result_dir。
- **D6** 提交：JSON + yaml 工作流文件（`steps:` 列表）；md-per-step 留后续。
- **D7/D8** CancelWorkflow = 标 cancelled + 取消当前 step job；step-job 继承 `workflows.caller_id`。
- **D9** v1 不另起 workflow 事件流，靠 workflows.status + 各 step 的 E13 事件。
- **D11** v1 prune job 不动 workflows；workflow 独立清理留后续。
- **D12** 一个 epic 三阶段（已建）。

## 范围与分期

| 阶段 | 子文档 | 内容 | 依赖 | 风险 |
|---|---|---|---|---|
| **P1** | [`P1-engine-plan.md`](./P1-engine-plan.md) | **引擎地基**：`workflows` 表 + jobs 2 列 + `SubmitWorkflow` + **幂等 `advanceWorkflow`**（finish 钩子 + sweeper）+ API + 串行 fail-fast + 取消（**无引用**） | 无 | 高 |
| **P2** | [`P2-refs-plan.md`](./P2-refs-plan.md) | **`${steps.N}` 传值**：引用文法 + 解析器（前序产出替换 prompt/cmd/cwd/env）+ 提交期校验 | P1 | 中 |
| **P3** | [`P3-cli-web-plan.md`](./P3-cli-web-plan.md) | **CLI + Web**：`gofer workflow run/show/list/cancel` + Web 工作流列表/详情 | P1（P2 增强展示） | 低-中 |

**顺序**：P1 → P2 → P3。每阶段绿灯即 Git 提交（SR1202）。

## 进度跟进

- [x] **P1-a** `workflows` 表 + jobs 加 `workflow_id`/`step_index` 列（5 处贯通）+ `workflows.go` DAO（含 `AdvanceCurrentStep` 抢占）
- [x] **P1-b** `WorkflowSpec`/`StepSpec` + `SubmitWorkflow`（校验 + 建 workflow + 起 step1）
- [x] **P1-c** `advanceWorkflow`（幂等推进）+ `finish` 异步钩子 + `CancelWorkflow`
- [x] **P1-d** `startWorkflowLoop` sweeper（serve 挂载，crash 兜底）
- [x] **P1-e** API：`POST /v1/workflows` / `GET /v1/workflows{,/id}` / `POST /v1/workflows/{id}/cancel`
- [ ] **P2-a** `${steps.N.field}` 解析器（`refs.go`）+ 接入 advanceWorkflow 起 step 前替换
- [ ] **P2-b** 提交期引用校验（拒未来/自引用/越界/非法 field）+ 运行期缺产出 → fail-fast
- [ ] **P3-a** CLI `gofer workflow run <file>`(yaml)/`show`/`list`/`cancel`（+ `--watch`）
- [ ] **P3-b** Web 工作流列表/详情（step 链 → 各 job 详情）+ types/client

## 全局验收门（每阶段收尾必过）

```bash
cd tools/gofer
go build ./... && go vet ./... && go test ./... -count=1 && gofmt -l internal/ cmd/   # 后端
pnpm -C web build                                                                       # 含前端阶段(P3-b)
```

- **回归**：现有单 job 提交/日志/SSE/events/deliveries/worker/peer 全绿；`workflow_id`/`step_index` 列 additive、旧 job 为空；非工作流路径完全不受影响（`finish` 钩子仅在 `WorkflowID!=""` 时触发）。
- **并发正确性（P1 重点）**：`advanceWorkflow` 幂等——**一个 step 绝不被起两次**（finish 钩子与 sweeper 并发、重复触发都只推进一次）；相关测试 `-count` 复跑 + `-race` 确认不 flaky。
- **真机冒烟**：local 跑一个 3-step 工作流（exec echo 链）→ 各 step 顺序 done、工作流 done；中间 step 失败 → 后续不起、工作流 failed；取消 → 当前 step 取消 + 后续不起。P2 后验 `${steps.N.result_dir/stdout/exit_code}` 真实替换。

## 安全要点（贯穿）

- 每 step 仍过 `validate`（project/agent/runner 白名单、exec gate），工作流不绕过单 job 准入。
- **推进幂等**（条件 UPDATE `current_step`，SR303）是最大正确性点：一个 step 绝不起两次。
- `${steps.N.result/stdout}` inline 上限 32KB（防大注入/DB 膨胀）；逐字段替换不 shell 重切（仿 Render）。
- 取消/查询在 `/v1` 鉴权内；step-job 继承 workflow caller_id。

## 结论
v1 工作流 = `workflows` 表 + jobs 2 列 + 幂等推进引擎（finish 异步钩子 + sweeper，条件 UPDATE 抢推进权）+ `${steps.N}` 跨 step 替换。每 step 仍是普通 job，复用产出/事件/日志/取消/产物。最大复用：`finish` 终态汇聚、`ClaimDueDeliveries` 抢占范式、`startDeliveryLoop` sweeper 范式、`Get(id)` 产出读取、jobstore 加表/加列模板。最大正确性点是推进幂等。本总纲随阶段更新进度与「阶段实施结果」。

## 阶段实施结果

- **P1**（2026-06-21，commit `99afb5e`+`91abe0e`+`f47aa7e`+`232de03`+`918e332`）：`workflows` 表 + jobs 加 `workflow_id`/`step_index` 列(5 处贯通)+ `workflows.go` DAO(`AdvanceCurrentStep` 条件 UPDATE 抢占,仿 ClaimDueDeliveries);`WorkflowSpec`/`StepSpec`/`SubmitWorkflow`(校验复用 validate + 建 workflow + 起 step1,caller 继承);`advanceWorkflow` **幂等推进**(读 cur→找 step job→终态?→AdvanceCurrentStep 抢推进权 RowsAffected==1 才继续→fail-fast/起下一步/done;末步也走一次抢权,故 done 后 current_step=Total+1)+ `finish` 异步钩子(`go advanceWorkflow` 仅 WorkflowID!="",不阻塞/不改 entry.done 时序)+ `CancelWorkflow`(标 cancelled+取消当前 step,幂等);`startWorkflowLoop` sweeper(crash 兜底)+`AdvanceRunningWorkflows`;`POST/GET/cancel /v1/workflows` API。验收门全 PASS。**主控独立硬压测**(汲取 E13 教训):job 包 x6 + jobstore x6 + httpapi x4 + 幂等 `-race -count=20` + **三包 `-race` 全绿,无 flaky 无 DATA RACE**——异步推进 goroutine 在压测下不导致失败(`database is closed` 仅 best-effort 迟到事件日志噪音,断言不依赖,与既有 job 包一致)。P2 `resolveRefs` 接入点已注释,本期各 step 独立跑(无引用)。
