# Job 上帝文件拆分实施计划（B 类 Epic）

> 修订记录：v1 / 2026-06-25 / inhere+claude / 初稿，固定函数→文件映射

## 1. 背景与目标

A 类入口下沉（BP1-6）已完成（收尾 commit `96a0ff3`，全量 test 绿）。本任务是 A 类附录 Epic：把 `internal/job` 下两个上帝文件按职责**同包拆文件**。

- 依据：design `docs/design/2026-06-25-code-layering-refactor-design.md` §11；plan `docs/plans/2026-06-25-code-layering-refactor-plan.md` §6。
- **铁律（G023）**：函数体**逐字搬迁**，仅改包内文件归属；调用方零改动、测试零改动；每步 `go build ./... && go vet ./internal/job/ && go test ./...` 全绿。
- 范围：`internal/job/service.go`(1025L) + `internal/job/workflow.go`(1402L)。`mcpserver` 等反例不动；`client/hub` 留 WP4。

## 2. 拆分原则

- **核心文件保留声明**：`service.go` / `workflow.go` 保留为「核心文件」，承载该域全部 `const`/`var`/`type` 声明 + 最核心的构造/访问方法（service）或共享纯函数（workflow）。
- **行为外迁**：业务行为函数按职责迁出到新文件，包内可见性不变（包级符号跨文件可见），无需调整可见性。
- **import 按文件子集**：每个新文件仅 import 自身用到的包，由 `go build` 报错收敛。

## 3. workflow.go(1402L) → 拆分映射

保留 `workflow.go`（核心）：全部 const（`sweeperWorkflowScan`/`onFailure*`/`maxRetryAttempts`/`join*`/`maxFanOut`/`stepType*`/`maxWorkflowDepth`/`defaultBackoffSec`）、全部 type（`StepSpec`/`RetryPolicy`/`WorkflowSpec`/`WorkflowStep`）、共享纯函数策略助手（`fanWant`/`joinPolicy`/`maxAttempts`/`backoffFor`/`retryableExit`/`maxAttemptsPolicy`/`backoffForPolicy`/`retryableExitPolicy`）。

| 新文件 | 迁入函数 |
|---|---|
| `workflow_advance.go`（优先，状态机） | `advanceWorkflow` / `advanceWorkflowStep` / `startNextStep` / `startStepJob` / `submitStepFan` / `startSubWorkflow` / `AdvanceRunningWorkflows` / `scheduleRetryAdvance` |
| `workflow_submit.go` | `SubmitWorkflow` / `SubmitWorkflowChild` / `childWorkflowID` / `submitWorkflowImpl` / `stepToRequest` / `validateRetry` / `validateFanout` / `validateSubworkflow` / `genWorkflowID` |
| `workflow_join.go` | `stepJob` / `stepJobAttempt` / `stepFanJobs` / `fanCounts` / `fanTerminal` / `fanVerdict` / `fanFailStatus` / `fanFailExitCode` / `cancelInflightFans` |
| `workflow_query.go` | `GetWorkflow` / `ListWorkflows` / `WorkflowSteps` / `recordWorkflowEvent` / `ListWorkflowEvents` |
| `workflow_terminate.go` | `setWorkflowDone` / `recordWorkflowTerminalMetric` / `triggerParentAdvance` / `setWorkflowFailed` |
| `workflow_cancel.go` | `CancelWorkflow` |

## 4. service.go(1025L) → 拆分映射

保留 `service.go`（核心）：全部 const（timeout/jobID*/builtinLocalRunner）、全部 var（`ErrUnknownProject` 等）、全部 type（`MetricsSink`/`ServiceStats`/`Service`/`jobEntry`）、核心方法 `SetMetrics`/`Stats`/`NewService`/`config`/`Reload`/`(*jobEntry).snapshot`。

| 新文件 | 迁入函数 |
|---|---|
| `submit.go` | `Submit` / `createJobDir` / `genJobID` / `randomSuffix` / `normalizeTimeout` |
| `execute.go` | `execute` / `finish` / `maybeRetryJob` / `classify` |
| `persistence.go` | `persist` / `toRecord` / `marshalTags` / `unmarshalTags` / `fromRecord` / `titleFromRequestJSON` |
| `concurrency.go` | `semaphore` / `callerSemaphore` / `CallerRate` |
| `config.go` | `validate` / `selectTargetWorker` / `checkRunnerAllowed` / `isPeerRunner` / `isWorkerRunner` / `isRemoteRunner` |

> 判断点：`validate`/`selectTargetWorker`/`checkRunnerAllowed` 归 `config.go`（config 准入语义），`submit.go` 专注 Submit 编排 + jobID/dir/timeout 机制。

## 5. 执行顺序（每步 = 1 commit，全量 test 绿）

workflow 先（按 handoff 优先 advance）：
1. `workflow_advance.go`
2. `workflow_submit.go`
3. `workflow_join.go`
4. `workflow_query.go`
5. `workflow_terminate.go`
6. `workflow_cancel.go`

service 后：
7. `submit.go`
8. `execute.go`
9. `persistence.go`
10. `concurrency.go`
11. `config.go`

每步操作：新建文件（`package job` + import 子集 + 逐字搬入函数）→ 从原文件删除同名函数 → `gofmt -w` 两文件 → `go build ./... && go vet ./internal/job/ && go test ./...` 全绿 → `git commit`。

## 6. 验收

- 每个 commit 后 `go test ./...` 全 24 包绿（基线已确认全绿）。
- 末步用 `git diff 96a0ff3 -- internal/job/ | grep -E '^[+-]'` 抽查：增删行应仅为「函数在文件间搬迁 + 各文件 package/import 头」，无业务逻辑行变更。
- 函数总数不变；`grep -c '^func' internal/job/*.go` 求和拆分前后一致。

## 7. 实施结果（2026-06-25 完成）

全部 11 步完成，每步 `go build/vet ./... + go test ./...` 全绿、独立 commit（P1=`25b3c76` … P11=`acc08bc`）。

- **workflow.go**：1402 → 225（核心仅 types/consts/策略纯函数）；迁出 advance(454)/submit(350)/join(171)/query(99)/terminate(74)/cancel(62)。函数总数 44 守恒。
- **service.go**：1025 → 235（核心仅 const/var/type + NewService/Stats/SetMetrics/config/Reload/snapshot）；迁出 submit(280)/execute(247)/persistence(128)/concurrency(45)/config(130)。函数总数 30 守恒。
- **等价性校验**（剥离 package/import/空行后代码+注释行多重集 vs 基线 `96a0ff3`）：
  - workflow：1310=1310 **字节级完全一致**。
  - service：923=923 一致，**唯一差异**为 gofmt 把 `NewService` 结构体字面量 5 个字段对齐列由窄改宽——基线该处本就非 gofmt-clean（前5/后5字段对齐不一致），gofmt 规整为统一列；纯空格、零语义，`gofmt -l` 现为空。
- 工具：确定性提取脚本（scratchpad `extract.py`，逐字节搬块 + 限定符探测 import + `go build` 兜底裁剪），非手工 Edit，杜绝逐字漂移。
- 范围外（未动）：`workflow_export.go`/`workflow_parse.go`（既有独立文件）、`mcpserver`、`client/hub`（留 WP4）。
- gofer 子仓本地无 remote，提交仅在本地。
