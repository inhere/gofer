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

- [x] **P1 重试/失败策略 + 事件流**（详见 P1 子文档）✅ commit `7c470b8`
  - [x] T1.1 StepSpec/RetryPolicy 字段 + 校验（validateRetry）
  - [x] T1.2 表迁移 `attempt`/`step_attempt`/`next_step_at`（+顺带 P2 `fan_index`/P3 `parent_*`）+ AdvanceStep 二元组 DAO
  - [x] T1.3 advanceWorkflow 失败分支：fail/continue/retry + **(step,attempt) 二元组抢权** + next_step_at 退避（sweeper backstop + 即时 timer）
  - [x] T1.4 统一 job 级重试（finish 失败重投最小版，共用 RetryPolicy）
  - [x] T1.5 workflow_events 表 + recordWorkflowEvent + 插桩 + `GET /v1/workflows/{id}/events`
  - [x] T1.6 workflow retention（独立 max_age + 连带清 step-jobs/events）
  - [x] T1.7 验收（并发硬测 PASS + -race 无竞态）+ 测试
- [x] **P2 fan-out 并行**（详见 P2 子文档）✅ commit `4492871`
  - [x] T2.1 StepSpec `fan_out`/`join` + validateFanout + `fan_index` Go 字段
  - [x] T2.2 submitStepFan 起 N 并行 job（request_id 加 `:fF`）+ advanceWorkflow stepFanJobs 聚合判定
  - [x] T2.3 join 语义（all/any/quorum + 永不悬挂）+ 引用聚合（`${steps.N.result_dir}` 多目录 + `${steps.N.fK}`）
  - [x] T2.4 验收（并发硬测 PASS + -race）+ 测试
- [x] **P3 子工作流 + 跨项目**（详见 P3 子文档）✅ commit `dc71b06`
  - [x] T3.1 StepSpec `type=workflow`/`sub_workflow` + `parent_*` Go 字段 + 递归校验(深度≤3/fan×wf 互斥)
  - [x] T3.2 子 wf 提交(确定性 id) + 终态 triggerParentAdvance + advanceWorkflowStep 按子 wf 终态判定
  - [x] T3.3 跨项目本地 result_dir 直读(集成测试) + 远端跨机 README 警示
  - [x] T3.4 验收（并发硬测 PASS + -race）+ 测试
- [x] **P4 易用**（详见 P4 子文档）✅ commit `92cc669`
  - [x] T4.1 导入导出 `GET /v1/workflows/{id}/export` + CLI export（secret 启发式剥离 + 递归子 wf）
  - [x] T4.2 md-per-step 提交（`File` 字段 json:"-" 不过线）
  - [x] T4.3 CLI events 子命令 + show ATT/FAN 列 + Web 详情分组展示 + workflow 指标（terminal/duration）
  - [x] T4.4 验收（后端 go test 验 + 前端仅 build 验，渲染需目视）

## 5. 实施结果（完成后回填）

### P1 ✅（commit `7c470b8`）
- **改动**：16 文件（11 改 5 新）——`workflow.go`(StepSpec on_failure/retry + advanceWorkflow 三分支重写 + startStepJob/事件)、`jobstore`(AdvanceStep 二元组 DAO + workflow_events 表 + 迁移 attempt/step_attempt/next_step_at + 顺带 P2 fan_index/P3 parent_*)、`service.go`(job 级重试最小版)、`prune.go`(workflow retention 连带清)、`workflow_handler.go`(events API)、4 测试文件。
- **幂等核心**：`(step,attempt)` 二元组条件 UPDATE 抢权（AdvanceStep）+ 确定性 request_id `wfID:sN:aA` 复用 C5 → 双层防重复起 job。
- **关键决策**：退避 sweeper backstop + 即时 timer 优化；事件顺序前移（先记事件再翻状态，修竞态，仿 v1 finish）；job 级重试进程内 timer 最小版（可靠版留后续）；顺带加 P2/P3 列减少迁移。
- **验收**：build/vet 绿；`go test` 三包绿（job 46s/jobstore 18s/httpapi 25s）；**⭐并发硬测两层 PASS**（job 32 并发 hammer + store 32 协程抢权，只起一个 att+1 job、状态只转移一次）；**-race 无竞态**；**D23 v1 回归通过**（无新字段 spec 行为不变）；workflow_events 时间线/retention 连带清验证。

### P2 ✅（commit `4492871`）
- **改动**：6 生产文件 + 4 测试——`workflow.go`(StepSpec FanOut/Join + validateFanout + submitStepFan + advanceWorkflow 聚合判定 stepFanJobs/fanTerminal/fanVerdict/cancelInflightFans)、`jobstore/jobs.go`(FanIndex 字段)、`refs.go`(`${steps.N.fK}` 选择器 + result_dir 聚合)、model/service(FanIndex 透传)。
- **关键决策**：fan request_id `wfID:sN:aA:fF`（fanIndex==0 时退化为 P1 键，**D23 不变**）；join all/any/quorum **全 terminal 必可决永不悬挂**；any/quorum done 后 `cancelInflightFans` 释放配额；引用聚合=成功 fan 的 result_dir 换行连接 + `.fK` 选择器。
- **验收**：build/vet 绿；三包 `-race` 全绿（job 56s/jobstore 21s/httpapi 28s）；**并发硬测 PASS**（`TestFanOutConcurrentAdvanceOnce` 32 并发只推进一次，-race -count=3 稳定）；join 三态 + fan-out×retry + 引用聚合 + **D23/P1 回归**全通过。

### P3 ✅（commit `dc71b06`）
- **改动**：`workflow.go`(StepSpec type/sub_workflow + validateSubworkflow 递归校验 + submitWorkflowImpl/SubmitWorkflowChild + 确定性 childWorkflowID + advanceWorkflowStep + triggerParentAdvance)、`jobstore/workflows.go`(ParentWorkflowID/ParentStepIndex 字段 + FindChildWorkflow)、`model.go`(subworkflow.started 事件)、README(跨机警示)、3 测试文件。
- **关键决策**：子 wf 确定性 id `<parent>:sub:sN:aA`(attempt-keyed,防并发重复 + retry 重跑独立);父终态判定复用 AdvanceStep 二元组抢权(幂等不变);递归准入(子 wf 每 leaf 过 validate);取消传播。
- **主控独立验证**（非转述子 agent）：① 文件真在工作树;② `go build ./...` 绿;③ **`go test ./...` 全量无过滤 exit 0**(主控亲跑);④ **读并发测试代码确认非空壳**——`TestSubWorkflowConcurrentParentAdvanceOnce` 真起 32 goroutine + atomic 计数 + 硬断言"父 step2 只起一次/子 wf 无重复";`-race` 三包绿。
- **遗留**：子 wf 在 Web 详情视图展示属 P4;远端产物拉取通道仍后续(仅文档警示);子 wf retry 重跑整条(only-failed 留后续)。

### P4 ✅（commit `92cc669`）
- **后端**：`workflow_export.go`(secret 启发式剥离 + ExportWorkflow)、`workflow_handler.go`(export API)、`metrics.go`(gofer_workflows_terminal_total/duration 经 MetricsSink)、`commands/workflow.go`+`workflow_md.go`(JSON/md-per-step 导入 + export/events 子命令 + show ATT/FAN 列)、`client.go`(ExportWorkflow/ListWorkflowEvents)。
- **前端**：`WorkflowDetail.vue` 按 step_index 分组(fan-out/retry 徽标)+ 子 wf 链接 + events 时间线。
- **主控独立验证**：① 文件真在;② build 绿;③ **`go test ./...` 全量无过滤 exit 0**(主控亲跑);④ 读测试确认非空壳(secret 泄漏会 `t.Fatalf`、workflow 指标 done Inc 一次硬断言);`-race` 绿;**`pnpm -C web build` 过(vue-tsc)**。
- **⚠️ 验证边界(诚实)**：后端全部 go test 验过;**前端展示逻辑仅编译验证、未浏览器目视——需用户眼检**(分组渲染/子 wf 链接/事件时间线/徽标)。
- **遗留**：工作流模板库(save/--template,plan 标可选)未做;secret 剥离是启发式非保证;子 wf retry 重跑整条。

## 7. SUPMODE 完成总结

P1-P4 **全部真实落地并提交**,每阶段由主控按四步独立验证(git 文件真在 + build + **全量 go test 无过滤亲跑** + 读测试代码确认非空壳),**不转述子 agent 的 PASS**。

| 阶段 | commit | 主控验证 |
|---|---|---|
| P1 retry/事件流/retention | `7c470b8` | 全量 test + 并发硬测读码(32 goroutine 只起一次) + -race |
| P2 fan-out/join | `4492871` | 同上 |
| P3 子工作流/跨项目 | `dc71b06` | 同上 + 并发硬测(父只推进一次) |
| P4 导入导出/md/展示/指标 | `92cc669` | 后端全量 test + 前端 pnpm build(渲染需目视) |

**前端眼检已完成（commit `ac6fa2f` 修复 + 真实浏览器验证）**：
- 浏览器眼检**发现并修复 2 个 go test 全绿也藏住的真实 bug**：① 子工作流 step 在详情 STEP CHAIN 整行不可见(后端 WorkflowSteps 漏 workflow 型步)；② 点"子工作流 →"链接不跳转(前端缺 watch(props.id))。均已修 + 回归测试(实证修复前 FAIL/后 PASS)+ 浏览器复验(截图 `tmp/wfv2-*.png`)。
- 后续关键点真机端到端复验**无新 bug**：retry attempt 历史(UI retry 徽标+a1/a2 两行 / CLI show ATT 列 / events step.retry)、导入导出+secret 剥离(API `X-Gofer-Redacted`+`***REDACTED***` 零泄漏 / CLI export)、workflow 指标(`gofer_workflows_terminal_total{status}` 计数对)、CLI show/events/export 子命令。
**过程教训**：曾出现一轮子 agent 报告被误当真转述(假 P2 commit + 编造 P3),经全量 git/test 复核纠正;此后确立"主控亲验四步 + 关键功能真机/浏览器眼检"铁律——眼检确实抓到了测试藏住的 bug。

## 6. 完成判定

- `go build ./...` + `go test ./...` 全绿
- **D23 向后兼容**：v1 工作流 spec（无新字段）零改动跑通（回归测试）
- per-step on_failure fail/continue/retry 走对；retry 退避重投**并发不双投**（硬测）
- fan-out N job 并行 + join 判定正确；幂等屏障不破
- 子工作流嵌套终态回传父；跨项目本地传值通
- 导入导出复现；Web 展示并行/嵌套/重试/事件
- 各阶段独立提交；最终按会话完成协议处理
