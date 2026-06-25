# Job workflow 子包抽取实施计划（B2 类 / 包级分层）

> 修订记录：v1 / 2026-06-25 / inhere+claude / 初稿，依据 design §13

## 1. 背景 / 目标 / 依据

§11 同包拆文件已完成，但 `internal/job` 仍是 29 文件 / ~5000 行巨型包。本计划把最大最自洽的子域 **workflow（9 文件 / 25 *Service 方法 + 29 自由函数 / ~1400 行）升为子包 `internal/job/workflow`**，做依赖倒置破双向耦合。

- 依据：design `docs/design/2026-06-25-code-layering-refactor-design.md §13`（D-B8 升包判据 / D-B9 防环 / D-B10 依赖倒置 / D-B11 放宽零测试改动）。
- **不变量**：①零行为变化（逻辑逐字、断言不变，仅 receiver/accessor 改名 + wiring）；②`internal/job` **绝不** import `workflow`，反向只经 job 内定义的 `WorkflowAdvancer` 接口；③每个 WS 结束 `go build ./... && go vet ./... && go test ./...` 全绿 + `go list -deps .../internal/job | grep workflow` 为空。
- **与 §11 区别**：本计划**打破零测试改动**（类型改名 ~35 处 + 9 个 workflow 测试迁包），但保零行为变化。须独立 PR/评审。

## 2. 目标包结构与依赖方向

```
internal/job/                         宿主：job 模型 + 单 job 引擎
  service.go      +Meta()/Now()/Config()/Validate()/Metrics() 导出访问器
                  +WorkflowAdvancer 接口 + s.wf 字段 + SetWorkflow()
  retry.go        RetryPolicy + 导出 MaxAttemptsPolicy/BackoffForPolicy/RetryableExitPolicy
                  (单 job 与 step 重试共用, §3.4 留 job)
  execute.go      finish(): s.wf.Advance(wfID)  ← 唯一反向 seam
  workflow/                           新子包：链编排域
    types.go      Spec/StepSpec(.Retry *job.RetryPolicy)/Step + workflow 常量 + fanWant/joinPolicy/包装器
    engine.go     Engine{ops JobOps; meta; now; metrics} + NewEngine + JobOps 接口
    advance.go submit.go join.go query.go terminate.go cancel.go export.go parse.go refs.go
    *_test.go     迁自 job 的 11 个 workflow/refs 测试 + helper_test.go(newTestEngine)

依赖（单向无环）：
  httpapi/serve/core ─▶ internal/job/workflow ─▶ internal/job ─▶ jobstore/config/...
  internal/job ──(仅 WorkflowAdvancer 接口, 实现体 core 注入)──▶ (不 import workflow)
```

## 3. 接口契约（核心）

### 3.1 反向 seam（job 侧定义）

```go
// internal/job/service.go —— 唯一反向依赖；core 在 wiring 时注入 engine。
// nil（未装 engine / 纯单 job 部署）时 finish 不触发，等价旧「非工作流 job 不推进」。
type WorkflowAdvancer interface{ Advance(wfID string) }

func (s *Service) SetWorkflow(w WorkflowAdvancer) { s.wf = w }   // Service 加字段 wf WorkflowAdvancer
```

```go
// internal/job/execute.go finish() —— 原: go s.advanceWorkflow(snap.WorkflowID)
if s.wf != nil && snap.WorkflowID != "" {
    go s.wf.Advance(snap.WorkflowID)
}
```

### 3.2 正向能力（workflow 侧定义，Service 满足）

```go
// internal/job/workflow/engine.go
type JobOps interface {
    Submit(req job.JobRequest) (job.JobResult, error)
    Cancel(id string) error
    Validate(cfg *config.Config, req job.JobRequest, remote bool) (config.ProjectConfig, error)
    Config() *config.Config
    Meta() *jobstore.Store
    Now() time.Time
    Metrics() job.MetricsSink   // 可为 nil
}

type Engine struct {
    ops     JobOps
    meta    *jobstore.Store   // = ops.Meta()（稳定，构造时缓存）
    now     func() time.Time  // = ops.Now（方法值；测试可在包内覆写）
    metrics job.MetricsSink   // = ops.Metrics()（nil-safe）
}

func NewEngine(ops JobOps) *Engine {
    return &Engine{ops: ops, meta: ops.Meta(), now: ops.Now, metrics: ops.Metrics()}
}

func (e *Engine) Advance(wfID string) { /* 原 advanceWorkflow，实现 job.WorkflowAdvancer */ }
```

> `config` 必须经 `e.ops.Config()` **实时**取（C3 热重载读 atomic 指针）；`meta`/`metrics` 稳定可缓存；`now` 缓存方法值以便测试覆写。

### 3.3 Service 满足 JobOps（job 侧加 5 个薄导出器）

```go
// internal/job/service.go（config()/nowFn/meta/metrics 保持私有，仅加导出 wrapper）
func (s *Service) Meta() *jobstore.Store { return s.meta }
func (s *Service) Now() time.Time        { return s.nowFn() }
func (s *Service) Config() *config.Config { return s.config() }
func (s *Service) Metrics() MetricsSink  { return s.metrics }
func (s *Service) Validate(cfg *config.Config, req JobRequest, remote bool) (config.ProjectConfig, error) {
    return s.validate(cfg, req, remote)   // validate 保持私有
}
// Submit / Cancel 已是导出方法，签名天然匹配 JobOps。
```

> 决策：不另设 core 适配器——core 包访问不到 Service 私有成员，适配器仍需先导出，反成多余间接。Service 直接满足 `workflow.JobOps` 最简。

### 3.4 共享重试原语必须留在 job（复审定论，防环关键）

`RetryPolicy` 是 **`JobRequest.Retry` 的字段类型**（model.go:54），单 job 重试 `execute.go:maybeRetryJob` 真实调用 `maxAttemptsPolicy/backoffForPolicy/retryableExitPolicy(req.Retry)`。故这些**不能迁 workflow**（否则 `JobRequest.Retry *RetryPolicy` 使 job 反向依赖 workflow → 环）。处置：

- **留 job**（建议归入新文件 `internal/job/retry.go`，从 workflow.go 移出）：`RetryPolicy`(类型，名不变)、`defaultBackoffSec`(var,私有)、`maxRetryAttempts`(const,私有)；
- **导出供 workflow 调用**：`maxAttemptsPolicy→MaxAttemptsPolicy`、`backoffForPolicy→BackoffForPolicy`、`retryableExitPolicy→RetryableExitPolicy`（execute.go 同步改用导出名）；
- **迁 workflow** 的 StepSpec 包装器改调 job 导出原语：`maxAttempts(step)`→`job.MaxAttemptsPolicy(step.Retry)`、`backoffFor`→`job.BackoffForPolicy`、`retryableExit`→`job.RetryableExitPolicy`；`StepSpec.Retry` 字段类型 → `*job.RetryPolicy`。

## 4. 命名映射

**类型**：`WorkflowSpec→workflow.Spec`、`WorkflowStep→workflow.Step`、`StepSpec→workflow.StepSpec`（名同、改包）。**`RetryPolicy` 留 job、名不变**（见 §3.4），外部 `job.RetryPolicy`（1 处）不动。

**方法**：公共方法名**保持不变**以减少调用点改动（`engine.SubmitWorkflow/GetWorkflow/ListWorkflows/WorkflowSteps/CancelWorkflow/ExportWorkflow/ListWorkflowEvents/SubmitWorkflowChild`）；仅两处必改：
- `advanceWorkflow → Advance`（实现 `WorkflowAdvancer`）
- `AdvanceRunningWorkflows → AdvanceRunning`（serve 2 处调用点）

**函数体内统一 sed（receiver 与依赖访问改写）**：

| 原（job 内 `*Service`） | 新（workflow 内 `*Engine`） |
|---|---|
| `func (s *Service)` | `func (e *Engine)` |
| `s.meta` | `e.meta` |
| `s.nowFn()` / `s.nowFn` | `e.now()` / `e.now` |
| `s.metrics` | `e.metrics` |
| `s.config()` | `e.ops.Config()` |
| `s.validate(` | `e.ops.Validate(` |
| `s.Submit(` | `e.ops.Submit(` |
| `s.Cancel(` | `e.ops.Cancel(` |
| `s.<其余 workflow 方法>(` | `e.<同名>(`（resolveRefs/advanceWorkflowStep/startNextStep/... 全表见 design §13.4）|

## 5. WS1：job 侧立 seam（包内，零行为变化，独立 commit）

**改动**（仅 `internal/job`，不建包、不迁 workflow 代码）：
1. `service.go`：加 §3.3 的 5 个导出 wrapper（Meta/Now/Config/Metrics/Validate）+ `wf WorkflowAdvancer` 字段 + `SetWorkflow` + `WorkflowAdvancer` 接口定义。**暂不动 finish**（advanceWorkflow 仍在 job，finish 仍直调；WS2 才切到 s.wf.Advance）。
2. 新建 `internal/job/retry.go`：从 workflow.go 移入 `RetryPolicy`、`defaultBackoffSec`、`maxRetryAttempts`、`maxAttemptsPolicy/backoffForPolicy/retryableExitPolicy`，并把后三者**导出**为 `MaxAttemptsPolicy/BackoffForPolicy/RetryableExitPolicy`；`execute.go` + workflow.go 内调用点同步改导出名。
3. `service_test.go` 加编译断言：
```go
var _ interface {
    Submit(JobRequest)(JobResult,error); Cancel(string)error
    Validate(*config.Config,JobRequest,bool)(config.ProjectConfig,error)
    Config()*config.Config; Meta()*jobstore.Store; Now()time.Time; Metrics()MetricsSink
} = (*Service)(nil)
```

**验收 WS1**：`go build/vet ./... + go test ./...` 全绿；零行为变化（仅加导出器/接口/改 retry 调用名）。独立 commit。

## 6. WS2：workflow 子包原子抽取（生产代码 + 测试同一 commit）

> **必须原子**：一旦 `SubmitWorkflow` 移到 `Engine`、`WorkflowSpec→workflow.Spec`，仍留 `package job` 的 workflow 测试即编译失败；且「先迁类型后迁引擎」会在中间态形成 job⇄workflow 环（StepSpec.Retry→*job.RetryPolicy）。故生产搬迁 + 外部改名 + wiring + finish hook + 测试迁移**一次做完、绿了再 commit**。用 §11 `extract.py` 思路按块搬出，再批量 sed 改 receiver/accessor。

### 6.0 先导出 job 私有符号（workflow 真实引用，复审已枚举）

WS2 第一步（仍在 package job，零行为变化）：把以下 5 个 job 私有符号 sed 改导出名（含定义 + 全部调用点），`go build ./... + go test ./...` 绿后再开始搬迁：

| 私有 | 导出 | 定义文件 | workflow 用处 |
|---|---|---|---|
| `jobIDLayout` | `JobIDLayout` | service.go | genWorkflowID |
| `randomSuffix` | `RandomSuffix` | submit.go | genWorkflowID |
| `maxEventDetailBytes` | `MaxEventDetailBytes` | events.go | recordWorkflowEvent |
| `titleFromRequestJSON` | `TitleFromRequestJSON` | persistence.go | WorkflowSteps |
| `isRemoteRunner` | `IsRemoteRunner` | config.go | submitWorkflowImpl/stepToRequest |

迁包后这些在 workflow 内写成 `job.JobIDLayout` 等。**已导出、仅需加 `job.` 限定**的：`StatusDone`/`StatusFailed`、`JobRequest`/`JobResult`/`MetricsSink`/`RetryPolicy`、`ErrInvalidRequest`/`ErrUnknownProject`/`ErrNoEligibleWorker`、`MaxAttemptsPolicy`/`BackoffForPolicy`/`RetryableExitPolicy`。`finish` 在 workflow 中全是注释引用（无反向调用，安全）。

### 6.1 建 workflow 包 + 迁引擎
- 新建 `internal/job/workflow/`（`package workflow`，import job）：
  - `types.go`：`Spec`(原 WorkflowSpec)/`StepSpec`/`Step`(原 WorkflowStep)（`StepSpec.Retry *job.RetryPolicy`）+ workflow 专属常量（onFailure*/join*/stepType*/maxFanOut/maxWorkflowDepth/sweeperWorkflowScan）+ 纯函数 `fanWant`/`joinPolicy` + StepSpec 包装器 `maxAttempts/backoffFor/retryableExit`（改调 `job.MaxAttemptsPolicy` 等，§3.4）。
  - `engine.go`：`Engine`/`NewEngine`/`JobOps`（§3.2）。
  - 迁 job 的 `workflow_advance/submit/join/query/terminate/cancel/export/parse.go` + **`refs.go` 整文件**（validateRefs/resolveRefs/resolveString/resolveRef/fanJobsOfStep/pickFanJob，已确认无外部调用者），按 §4 sed 改 receiver/accessor；`advanceWorkflow→Advance`（实现 `WorkflowAdvancer`）、`AdvanceRunningWorkflows→AdvanceRunning`，其余公共方法名不变。
  - `stepToRequest` 构造 `job.JobRequest{WorkflowID,StepIndex,Attempt,FanIndex,CallerID,...}`（均导出字段，已确认）。
- 删除 job 内 `workflow*.go`(9) + `refs.go`；删 workflow.go 后 job 不再引用 workflow → **job 不 import workflow**（workflow 单向 import job）。

### 6.2 job 侧 finish hook + 收尾
- `execute.go finish()`：`go s.advanceWorkflow(...)` → §3.1 的 `if s.wf!=nil && snap.WorkflowID!="" { go s.wf.Advance(...) }`。
- WS1 编译断言移到 workflow 包：`var _ JobOps = (*job.Service)(nil)`。

### 6.3 外部改名（~34 处，sed）
`job.WorkflowSpec→workflow.Spec`、`job.WorkflowStep→workflow.Step`、`job.StepSpec→workflow.StepSpec`（加 import）。命中：`httpapi/workflow_handler.go`、`commands/workflow*.go`、`client/*.go`、`mcpserver/*`。
> `job.RetryPolicy` 不改（留 job）。`jobstore.Store.GetWorkflow/ListWorkflows` 是 DAO 同名、**不改**。

### 6.4 wiring
- `internal/core/core.go:82` 构造 Service 后：`eng := workflow.NewEngine(svc); svc.SetWorkflow(eng)`；`eng` 存入 Core 供 httpapi/serve 取。
- `internal/serve/serve.go:291,299`：`jobSvc.AdvanceRunningWorkflows(ctx)` → `eng.AdvanceRunning(ctx)`。
- `internal/httpapi/workflow_handler.go` 6 handler：Server 注入 `eng`，`svc.SubmitWorkflow/GetWorkflow/...` → `eng.*`。

### 6.5 测试迁移（11 文件，关键劳动）
- 迁到 `internal/job/workflow`、`package workflow`（内部测试，直接访问 Engine 私有 + 包内 lowercase `validateFanout/validateRetry/stepToRequest/validateSubworkflow/resolveRefs/validateRefs`）：
  `workflow_test.go / workflow_fanout_test.go / workflow_retry_test.go / workflow_subworkflow_test.go / workflow_crossproject_test.go / workflow_validate_test.go / workflow_subworkflow_validate_test.go / workflow_export_test.go / workflow_parse_test.go / refs_test.go / refs_fanout_test.go`。
- 新建 `helper_test.go`：`newTestEngine(t, root) (*Engine, *job.Service)` 用导出 API 重建 `newTestServiceWithDB` 等价 setup（`job.NewService` + `jobstore.Open` + `localrunner.New` + self/noexec config）+ `svc.SetWorkflow(eng)` 接好 finish→Advance 闭环；如需 `waitForStatus` 复制等价实现。
- 改写：`s.SubmitWorkflow`→`eng.SubmitWorkflow`、`s.advanceWorkflow`→`eng.Advance`、`s.AdvanceRunningWorkflows`→`eng.AdvanceRunning`、`s.resolveRefs`→`eng.resolveRefs`、`s.meta`→`eng.meta`、`s.nowFn=`→`eng.now=`（同包字段，时钟注入）；`WorkflowSpec/StepSpec`→`Spec/StepSpec`、`RetryPolicy`→`job.RetryPolicy`。

**验收 WS2**：`go build/vet ./... + go test ./...`（总测试数不降）全绿；`go list -deps github.com/inhere/gofer/internal/job | grep workflow` **为空**；`go test ./internal/job/workflow/` 单独绿。一次 commit（可在 6.1-6.4 编过后先本地 build，6.5 绿了再统一提交）。

**验收 WS2b**：`go build/vet ./... + go test ./...`（总测试数不降）全绿；`go list -deps github.com/inhere/gofer/internal/job | grep workflow` **为空**（无环）；workflow 包 `go test ./internal/job/workflow/` 单独绿。独立 commit（可拆 7.1+7.2 一提、7.3+7.4 一提，若中途能编过）。

## 8. WS3：收尾 / 验环 / 文档 / 冒烟

- `go list -deps` 双向验环；`grep -rn 'job\.WorkflowSpec\|job\.WorkflowStep' internal/` 应为空（全部改名干净）。
- 冒烟（真机）：workflow 提交→推进→重试→fan/join→子工作流→取消、sweeper 兜底、parent-advance、纯单 job（未装 engine 路径 `s.wf==nil` 不触发）。
- 更新 `tools/gofer/CLAUDE.md`：G02x 增「D-B8 子域升包判据 + workflow 子包已落地」一行。
- 回填本 plan §11 + design §13 + bd `gofer-b-refactor-audit`。

## 9. 风险与回滚

| 风险 | 缓解 |
|---|---|
| import 环 | D-B9：job 不 import workflow；共享 RetryPolicy 留 job（§3.4）；WS2 原子搬迁无中间环；后跑 `go list -deps` 验 |
| 测试迁移丢覆盖 | 集成测试经 `newTestEngine` 维持真实 execute→finish→Advance 闭环；总测试数不降为硬门 |
| finish nil-engine | `s.wf==nil` 时不触发，等价旧语义；单 job 测试（不装 engine）覆盖该路径 |
| 热重载语义 | config 经 `e.ops.Config()` 实时取，不缓存 |
| WS2 大 commit | 不可避（包抽取原子性）；先把 WS1 安全缝拆出独立 commit；WS2 内 6.1-6.4 先本地 build 过、6.5 测试绿再统一提交，出问题整体 revert |

每个 WS 独立 commit，出问题单独 revert。

## 10. 验收总表

- [ ] WS1：5 导出器 + WorkflowAdvancer/SetWorkflow + retry.go(导出 *Policy)；编译断言；test 绿
- [ ] WS2：workflow 子包原子抽取（types/engine/refs + 外部~34改名 + finish hook + core/serve/httpapi wiring + 11 测试迁包）；`go list -deps` 无环；test 绿（数不降）；workflow 包单独绿
- [ ] WS3：验环 + 冒烟 + CLAUDE.md/文档/记忆回填

## 11. 实施结果（2026-06-25 完成）

全部完成，全量 `go test ./...` 绿（637 测试不减）、`go list -deps job|grep workflow` 为空（无环）。commits：WS1=`d0e0b61` / WS2.0=`c86608b` / WS2=`1ec559b` / WS3=(本提交)。

- **WS1**：service.go 加 WorkflowAdvancer/SetWorkflow/5 访问器；retry.go 留共享重试原语+导出 *Policy。
- **WS2.0**：导出 5 个 job 私有符号（JobIDLayout/RandomSuffix/MaxEventDetailBytes/TitleFromRequestJSON/IsRemoteRunner）。
- **WS2**：10 源 + 11 测试迁 `internal/job/workflow`（12 生产文件含 engine.go / 13 测试文件）；execute.go finish 经 `s.wf.Advance`；core/serve/httpapi wiring（httpapi.New 增 `wf *workflow.Engine` 参数，涟漪到 runner/worker/core 的 httpapi.New 测试调用点）；外部 `job.WorkflowSpec/StepSpec/WorkflowStep`→`workflow.Spec/StepSpec/Step` 改名。
- **WS3**：maxRetryAttempts 归位 workflow 私有（撤回 WS1 误置）；CLAUDE.md 增 G024；二进制构建+运行验证。

**与计划的偏差（均零行为变化，已审核）**：
1. `JobOps` 比预期多 3 个方法 `Get/TailLog/Wait`——`refs.go` 的 `${steps.N.result|stdout}` 解析真实调用 `s.Get/s.TailLog`（refs.go 未在初版 s.<X> 枚举内），测试用 `Wait` 排空；均为既有导出 Service 方法。
2. 额外把 `metrics_test.go` 的 2 个 workflow 指标测试拆到 workflow 包（保总测试数）。
3. 等价性背书：10 源 + 11 测试文件「控制流/断言 token 计数」逐一与基线 `c86608b` md5 一致 + 637 测试全绿。
