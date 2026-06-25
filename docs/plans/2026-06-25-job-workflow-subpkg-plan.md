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
  execute.go      finish(): s.wf.Advance(wfID)  ← 唯一反向 seam
  workflow/                           新子包：链编排域
    types.go      Spec/StepSpec/Step/RetryPolicy + 常量 + 策略纯函数
    engine.go     Engine{ops JobOps; meta; now; metrics} + NewEngine + JobOps 接口
    advance.go submit.go join.go query.go terminate.go cancel.go export.go parse.go refs.go
    *_test.go     迁自 job 的 9 个 workflow 测试 + helper_test.go(newTestEngine)

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

## 4. 命名映射

**类型**（去 stutter）：`WorkflowSpec→workflow.Spec`、`WorkflowStep→workflow.Step`；`StepSpec`/`RetryPolicy` 名不变（`workflow.StepSpec`/`workflow.RetryPolicy`）。

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

## 5. WS1：Service 暴露 JobOps 访问器（包内，零行为变化）

**改动**：仅 `internal/job/service.go` 加 §3.3 的 5 个导出 wrapper + 加 `wf WorkflowAdvancer` 字段 + `SetWorkflow` + `WorkflowAdvancer` 接口定义。**不动 finish、不建包、不迁代码**。

**自检**：加一个临时编译断言（WS2b 删除或转入 workflow 包）：
```go
// service_test.go (package job) —— 证明 Service 形状满足将来的 JobOps
var _ interface {
    Submit(JobRequest) (JobResult, error); Cancel(string) error
    Validate(*config.Config, JobRequest, bool) (config.ProjectConfig, error)
    Config() *config.Config; Meta() *jobstore.Store; Now() time.Time; Metrics() MetricsSink
} = (*Service)(nil)
```

**验收 WS1**：`go build/vet ./... + go test ./...` 全绿；diff 仅 service.go + 1 测试断言；零行为变化。独立 commit。

## 6. WS2a：types 下移到 workflow 包（纯类型搬迁，无 Engine）

1. 新建 `internal/job/workflow/types.go`（`package workflow`，**不 import job**）：迁入 `Spec`(原 WorkflowSpec)、`StepSpec`、`Step`(原 WorkflowStep)、`RetryPolicy` + 全部 workflow 常量（onFailure*/join*/stepType*/maxFanOut/maxRetryAttempts/maxWorkflowDepth/defaultBackoffSec/sweeperWorkflowScan）+ 策略纯函数（fanWant/joinPolicy/maxAttempts/backoffFor/retryableExit/maxAttemptsPolicy/backoffForPolicy/retryableExitPolicy）。types 内 `WorkflowSpec→Spec`、`WorkflowStep→Step` 改名。
2. 从 `internal/job/workflow.go` 删除上述声明（该文件其余 workflow 代码此刻仍在 job 内，临时改引用 `workflow.Spec/Step/...` → **job 临时 import workflow**；因 workflow 仅类型无 job import，**不成环**）。
3. **外部改名**（sed，~35 处）：`job.WorkflowSpec→workflow.Spec`、`job.WorkflowStep→workflow.Step`、`job.StepSpec→workflow.StepSpec`、`job.RetryPolicy→workflow.RetryPolicy`，并加 import。命中文件：`httpapi/workflow_handler.go`、`commands/workflow*.go`、`client/*.go`、`mcpserver/*`（按子代理清单逐个）。
   > 注意：`jobstore.Store.GetWorkflow/ListWorkflows` 与 Service 同名但是 DAO，**不在改名范围**。
4. `go build/vet ./... + go test ./...` 全绿。

**验收 WS2a**：类型已在 workflow 包；外部全改名；`go list -deps .../internal/job/workflow` **不含** job（纯类型包）。独立 commit。

## 7. WS2b：Engine 抽取 + wiring + finish hook + 测试迁移（主体）

> 体量大；可用 §11 的 `extract.py` 思路先按块搬出，再批量 sed 改 receiver/accessor。建议子步内顺序做、最后一次性 build/test。

### 7.1 迁移 workflow 引擎代码 → `internal/job/workflow`
- 把 job 的 `workflow_advance/submit/join/query/terminate/cancel/export/parse.go` + `refs.go`（resolveRefs 及其 workflow 专属内容）迁入 workflow 包，按 §4 sed 改写；`workflow.go` 残留（若有共享）并入 `engine.go`/`types.go`。
- 新建 `engine.go`：`Engine`/`NewEngine`/`JobOps`（§3.2）；`advanceWorkflow→Advance`、`AdvanceRunningWorkflows→AdvanceRunning`。
- 迁出后 `internal/job` 不再有任何 workflow.* 引用 → **job 去掉对 workflow 的 import**（WS2a 的临时 import 在此消除）。workflow 单向 import job（取 `job.JobRequest/JobResult/MetricsSink`）。
- `stepToRequest` 构造 `job.JobRequest{WorkflowID,StepIndex,Attempt,FanIndex,CallerID,...}`（均为导出字段，已确认，无需额外导出）。

### 7.2 job 侧 finish hook
- 删 `advanceWorkflow`（已迁走）；`execute.go finish()` 改 §3.1 的 `s.wf.Advance(...)`。
- 删 WS1 的临时编译断言（或迁为 workflow 包内 `var _ JobOps = (*job.Service)(nil)`）。

### 7.3 wiring
- `internal/core/core.go:82` 构造 Service 后：
  ```go
  svc := job.NewService(...)
  eng := workflow.NewEngine(svc)   // Service 满足 JobOps
  svc.SetWorkflow(eng)
  ```
  把 `eng` 存入 Core，供 httpapi/serve 取用。
- `internal/serve/serve.go:291,299` `startWorkflowLoop`：`jobSvc.AdvanceRunningWorkflows(ctx)` → `eng.AdvanceRunning(ctx)`。
- `internal/httpapi/workflow_handler.go` 6 个 handler：持有 `eng`，`svc.SubmitWorkflow/GetWorkflow/...` → `eng.SubmitWorkflow/...`（Server 结构体注入 engine）。

### 7.4 测试迁移（关键劳动）
- 9 个 workflow 测试文件迁到 `internal/job/workflow`，`package workflow`（内部测试，可直接访问 Engine 私有与包内 lowercase 函数 validateFanout/validateRetry/stepToRequest/validateSubworkflow）。
- 新建 `helper_test.go`：`newTestEngine(t, root) (*Engine, *job.Service)`，**用导出 API 重建** `newTestServiceWithDB` 的等价 setup（`job.NewService` + `jobstore.Open` + `localrunner.New` + 同样的 self/noexec 项目 config），再 `svc.SetWorkflow(eng)` 接好 finish→Advance 闭环，返回 eng+svc。
- `waitWorkflow`/`waitForRetryJob`（原在 workflow 测试文件内）随迁，`s.meta`→`eng.meta`/`svc.Meta()`；时钟覆写 `s.nowFn=`→`eng.now=`（同包字段）。
- 直接调用改写：`s.advanceWorkflow`→`eng.Advance`、`s.AdvanceRunningWorkflows`→`eng.AdvanceRunning`、`s.SubmitWorkflow`→`eng.SubmitWorkflow`、`s.meta`→`eng.meta`。
- 若某 workflow 测试还依赖 `waitForStatus`（service_test.go, 单 job）：在 helper_test.go 复制一份等价实现。

**验收 WS2b**：`go build/vet ./... + go test ./...`（总测试数不降）全绿；`go list -deps github.com/inhere/gofer/internal/job | grep workflow` **为空**（无环）；workflow 包 `go test ./internal/job/workflow/` 单独绿。独立 commit（可拆 7.1+7.2 一提、7.3+7.4 一提，若中途能编过）。

## 8. WS3：收尾 / 验环 / 文档 / 冒烟

- `go list -deps` 双向验环；`grep -rn 'job\.WorkflowSpec\|job\.WorkflowStep' internal/` 应为空（全部改名干净）。
- 冒烟（真机）：workflow 提交→推进→重试→fan/join→子工作流→取消、sweeper 兜底、parent-advance、纯单 job（未装 engine 路径 `s.wf==nil` 不触发）。
- 更新 `tools/gofer/CLAUDE.md`：G02x 增「D-B8 子域升包判据 + workflow 子包已落地」一行。
- 回填本 plan §11 + design §13 + bd `gofer-b-refactor-audit`。

## 9. 风险与回滚

| 风险 | 缓解 |
|---|---|
| import 环 | D-B9：job 不 import workflow；WS2a/WS2b 后各跑 `go list -deps` 验；WS2a 临时 import 必须在 WS2b 消除 |
| 测试迁移丢覆盖 | 集成测试经 `newTestEngine` 维持真实 execute→finish→Advance 闭环；总测试数不降为硬门 |
| finish nil-engine | `s.wf==nil` 时不触发，等价旧语义；单 job 测试（不装 engine）覆盖该路径 |
| 热重载语义 | config 经 `e.ops.Config()` 实时取，不缓存 |
| 大 commit 难评审 | 分 WS1/WS2a/WS2b/WS3；WS2b 内再分 7.1-7.2 / 7.3-7.4 |

每个 WS 独立 commit，出问题单独 revert。

## 10. 验收总表

- [ ] WS1：5 导出器 + WorkflowAdvancer/SetWorkflow；编译断言；test 绿
- [ ] WS2a：types 迁 workflow 包；外部 ~35 改名；workflow 包无 job 依赖；test 绿
- [ ] WS2b：Engine 抽取 + finish hook + core/serve/httpapi wiring + 9 测试迁包；`go list -deps` 无环；test 绿（数不降）
- [ ] WS3：验环 + 冒烟 + CLAUDE.md/文档/记忆回填

## 11. 实施结果（完成后回填）
