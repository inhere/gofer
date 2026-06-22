# 实施计划：工作流 v2（E7 完整化）

> 设计依据：[`../../design/2026-06-22-workflow-v2-design.md`](../../design/2026-06-22-workflow-v2-design.md)（v0.2 决策 D13–D23 定稿）。承接已落地 v1（[`../../design/2026-06-21-workflow-chains-design.md`](../../design/2026-06-21-workflow-chains-design.md)）。
> 本文为**总纲**：开发计划总纲 + 进度跟进 + 每阶段实施结果（SR1105）。阶段详情见 `P1..P4` 子文档。

## 1. 总纲

| 阶段 | 子文档 | 目标 | 依赖 | 量级 |
|---|---|---|---|---|
| **P1** | [`P1-retry-events-plan.md`](P1-retry-events-plan.md) | per-step `on_failure`/`retry` + 统一 job 级重试(E24) + `attempt`/`next_step_at` 列 + `workflow_events` 表 + workflow retention | — | 中-大 |
| **P2** | [`P2-fanout-plan.md`](P2-fanout-plan.md) | 单 step 并行 fan-out + `join`(all/any/quorum) + `fan_index` 列 + advanceWorkflow 多 job 聚合 + 引用聚合 | P1 | 大 |
| **P3** | [`P3-subworkflow-crossproject-plan.md`](P3-subworkflow-crossproject-plan.md) | 子工作流嵌套(`type=workflow`/`sub_workflow`) + `parent_*` 列 + 子 wf 终态触发父 + 跨项目产物澄清 | P2 | 大 |
| **P4** | [`P4-usability-plan.md`](P4-usability-plan.md) | 导入导出(E18) + md-per-step + CLI/Web 展示(并行/嵌套/重试/事件) + workflow 指标 | P3 | 中 |

**核心取向（贯穿全程，不可违反）**：保留 v1 引擎骨架——`current_step` 单值指针 + `AdvanceCurrentStep` 幂等屏障 + finish/sweeper 双触发**全部不重写**；v2 能力 additive 挂载（fan-out=step 内多 job、子工作流=step 类型、重试=失败分支）；StepSpec 新字段默认值 = v1 行为（D23 向后兼容，v1 工作流零改动继续跑）。

**独立交付**：P1/P2 各自即可独立上线主要价值；P3/P4 视需要推进。

## 2. 关键设计落点（v1 引擎 → v2 改造）

| v1 挂点 | 文件:行 | v2 改造 | 阶段 |
|---|---|---|---|
| `advanceWorkflow` | `internal/job/workflow.go:155` | step 终态判定（单 job→多 job 聚合）+ 失败分支（fail-fast→on_failure 策略）+ retry 重投 + 子 wf 分支 | P1/P2/P3 |
| `AdvanceCurrentStep` 幂等屏障 | `internal/jobstore/workflows.go:150` | **不动**；retry 唯一新交互=抢权后指针退回（P1 D16） | P1 |
| `finish` 触发 | `internal/job/service.go:634` | job 级 retry 在此判定重投；子 wf 终态额外触发父 advance | P1/P3 |
| sweeper | `internal/job/workflow.go:271` `AdvanceRunningWorkflows` | **不动**；天然驱动 retry 到点重投（`next_step_at`）+ fan-out 半完成兜底 | P1 |
| `StepSpec` | `internal/job/workflow.go:19` | additive 加 `type`/`on_failure`/`retry`/`fan_out`/`join`/`sub_workflow` | P1/P2/P3 |
| `validateRefs`/`resolveRefs` | `internal/job/refs.go:44/84` | fan-out 聚合产出引用；子 wf 产出引用 | P2/P3 |
| `SubmitWorkflow` | `internal/job/workflow.go:49` | 加 retry/fan-out/子 wf 校验 + 子 wf 提交带 parent | P1/P2/P3 |
| `workflows` 表 | `internal/jobstore/store.go:141` | ALTER ADD `next_step_at`/`parent_workflow_id`/`parent_step_index` | P1/P3 |
| `jobs` 表 | `internal/jobstore/store.go:260` | ALTER ADD `fan_index`/`attempt` | P1/P2 |
| 退避重投范式 | E14 `internal/job/delivery.go` + `startDeliveryLoop`(`serve.go:211`) | **参考**：delivery 的"记 next_attempt + sweeper 到点重投"是 retry 的同款范式 | P1 |
| 事件流范式 | E13 `job_events` 表 + `recordEvent`(`events.go`) | **仿造** `workflow_events` 表 + `recordWorkflowEvent` | P1 |
| 配额 | E17 per-caller 配额 | fan-out 大批量并行 step-job 继承 caller_id，天然受配额约束 | P2 |

## 3. 前置检查（plan-checking，SR1430.2）

| 检查项 | 结果 |
|---|---|
| `go build ./...` 基线 | ✅ PASS（本会话多次验证） |
| v1 工作流测试基线 `go test -run Workflow\|Refs ./internal/job ./internal/httpapi` | ✅ PASS（已核验） |
| v1 已完整落地（plan 9/0，真机冒烟 PASS） | ✅ 已确认（commit 链 `99afb5e`..`7a5c7f2`） |
| 无破坏性迁移（全 ALTER ADD + omitempty 字段） | ✅ D23 向后兼容硬约束 |
| 退避/事件流有现成范式可仿（delivery/job_events） | ✅ E14/E13 已落地 |

**结论：前置 PASS。** 风险点（实施期重点）：**D16 retry 与幂等屏障的并发交互**（抢权后指针退回 `cur+1→cur`）——P1 必须硬测并发不双投。

## 4. 进度跟进

- [x] **P1 重试/失败策略 + 事件流**（详见 P1 子文档）✅ commit `<P1>`
  - [x] T1.1 StepSpec/RetryPolicy 字段 + 校验（validateRetry）
  - [x] T1.2 表迁移 `attempt`/`step_attempt`/`next_step_at`（+顺带 P2 `fan_index`/P3 `parent_*`）+ AdvanceStep 二元组 DAO
  - [x] T1.3 advanceWorkflow 失败分支：fail/continue/retry + **(step,attempt) 二元组抢权** + next_step_at 退避（sweeper backstop + 即时 timer）
  - [x] T1.4 统一 job 级重试（finish 失败重投最小版，共用 RetryPolicy）
  - [x] T1.5 workflow_events 表 + recordWorkflowEvent + 插桩 + `GET /v1/workflows/{id}/events`
  - [x] T1.6 workflow retention（独立 max_age + 连带清 step-jobs/events）
  - [x] T1.7 验收（并发硬测 PASS + -race 无竞态）+ 测试
- [ ] **P2 fan-out 并行**（详见 P2 子文档）
  - [ ] T2.1 StepSpec `fan_out`/`join` + 校验 + `fan_index` 列
  - [ ] T2.2 startStep 起 N 并行 job + advanceWorkflow 多 job 聚合判定
  - [ ] T2.3 join 语义（all/any/quorum）+ 引用聚合
  - [ ] T2.4 验收 + 测试
- [ ] **P3 子工作流 + 跨项目**（详见 P3 子文档）
  - [ ] T3.1 StepSpec `type=workflow`/`sub_workflow` + `parent_*` 列 + 校验
  - [ ] T3.2 子 wf 提交 + 终态触发父 advance
  - [ ] T3.3 跨项目产物澄清 + 远端警示
  - [ ] T3.4 验收 + 测试
- [ ] **P4 易用**（详见 P4 子文档）
  - [ ] T4.1 导入导出 spec_json（CLI/API，secret 剥离）
  - [ ] T4.2 md-per-step 提交
  - [ ] T4.3 CLI/Web 展示（fan-out/嵌套/重试 attempt/事件）+ workflow 指标
  - [ ] T4.4 验收 + 测试

## 5. 实施结果（完成后回填）

### P1 ✅（commit `<P1>`）
- **改动**：16 文件（11 改 5 新）——`workflow.go`(StepSpec on_failure/retry + advanceWorkflow 三分支重写 + startStepJob/事件)、`jobstore`(AdvanceStep 二元组 DAO + workflow_events 表 + 迁移 attempt/step_attempt/next_step_at + 顺带 P2 fan_index/P3 parent_*)、`service.go`(job 级重试最小版)、`prune.go`(workflow retention 连带清)、`workflow_handler.go`(events API)、4 测试文件。
- **幂等核心**：`(step,attempt)` 二元组条件 UPDATE 抢权（AdvanceStep）+ 确定性 request_id `wfID:sN:aA` 复用 C5 → 双层防重复起 job。
- **关键决策**：退避 sweeper backstop + 即时 timer 优化；事件顺序前移（先记事件再翻状态，修竞态，仿 v1 finish）；job 级重试进程内 timer 最小版（可靠版留后续）；顺带加 P2/P3 列减少迁移。
- **验收**：build/vet 绿；`go test` 三包绿（job 46s/jobstore 18s/httpapi 25s）；**⭐并发硬测两层 PASS**（job 32 并发 hammer + store 32 协程抢权，只起一个 att+1 job、状态只转移一次）；**-race 无竞态**；**D23 v1 回归通过**（无新字段 spec 行为不变）；workflow_events 时间线/retention 连带清验证。

### P2 / P3 / P4
> 待回填。

## 6. 完成判定

- `go build ./...` + `go test ./...` 全绿
- **D23 向后兼容**：v1 工作流 spec（无新字段）零改动跑通（回归测试）
- per-step on_failure fail/continue/retry 走对；retry 退避重投**并发不双投**（硬测）
- fan-out N job 并行 + join 判定正确；幂等屏障不破
- 子工作流嵌套终态回传父；跨项目本地传值通
- 导入导出复现；Web 展示并行/嵌套/重试/事件
- 各阶段独立提交；最终按会话完成协议处理
