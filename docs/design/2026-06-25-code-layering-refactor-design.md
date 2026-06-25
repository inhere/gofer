# Gofer 代码分层重构设计（入口层逻辑下沉）

> 一句话：把 `commands` / `httpapi` / `mcpserver` 三个**入口层**里混着的业务/编排逻辑下沉到专用包，让入口只做「**参数绑定 + 校验 + 转发**」；**纯搬家/抽接口、零行为变化**，每步全量测试绿。
> 关联：bd 审计 `gofer-b-refactor-audit`（摸代码产出）；TODO §代码优化（B 组）。本文新决策从 **D-B1** 起。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-06-25 | inhere | 初稿：现状审计 + 目标分层 + 新包划分 + D-B1..D-B7 + 分阶段（A 类入口下沉本轮，B 类 job 拆分附录 epic），待审核 |
| v0.2 | 2026-06-25 | inhere+claude | A 类(BP1-6)与 B 类(§11 job 同包拆文件)均已落地；新增 §13【B2 类：job 巨型包的包级分层 — `internal/job/workflow` 子包抽取（依赖倒置）】，回应「拆文件≠分层、job 仍是巨型包」反馈，引入 **D-B8..D-B11** |

## 2. 背景

用户痛点：`internal/commands` 里 `assemble`/`runner_probe` 等**逻辑代码**塞在命令包，commands 应只做参数绑定校验；`internal/httpapi` 同样"逻辑混在入口、散乱难维护"；应建 core/service/专用包整理。

摸代码审计（17 项，bd `gofer-b-refactor-audit`）确认问题分两类，并回答"除 commands/httpapi 外还有哪些"：
- **`mcpserver` 不是同类**（反例）：10 个 MCP handler 全薄转发、不 import jobstore、无内联逻辑——**不纳入**。
- **`job` 两个上帝文件**（`service.go` 1025 / `workflow.go` 1402）是最严重同类，但属 **B 类（巨型文件职责耦合）**，同包可拆、零测试改动 → **独立 epic（§11）**。
- **`assemble.go`/`runner_probe.go`**：根本不是命令入口（无 RunE），**放错包**。
- **`httpapi/stream_handler.go`**：httpapi 内最严重入口混逻辑。

## 3. 现状审计（浓缩，全表见 bd）

### A 类：入口层混业务/编排逻辑（本轮目标）

| 位置 | 问题 | 严重度 |
|---|---|---|
| `commands/serve.go`(404) | RunE 内直接编排 5 个后台循环 `start{Prune,Delivery,Workflow,Reload,Probe}Loop`（~200 行 goroutine+ticker+signal+ctx）| 高 |
| `commands/assemble.go`(170) | `buildCore`/`Reload`/`Core`/`hubWorkerSelector` 核心 wiring，**非命令入口却在 commands**，被 serve+mcp+worker 共用 | 高 |
| `commands/runner_probe.go`(172) | `newPeerProber`/`probeOnce` 并发健康探测+HTTP 探测，**非命令入口** | 高 |
| `httpapi/stream_handler.go`(391) | `pumpLogs`/`pumpInteractions`/`pumpEvents`+动态节流+eviction 回退（~260 行流式编排在 handler 内）| 高 |
| `commands/config.go`(473) | `validateServerConfig`/`validateWorkerConfig` 校验编排、`runConfigEdit` 编辑器探测、`runConfigShow` overlay 合并（~180 行）| 中高 |
| `commands/job.go`(655) | `resolveProjectByCwd`、`watchToTerminal`(SSE 状态机)、客户端轮询（~150 行）| 中 |
| `commands/workflow.go`(524) | `parseWorkflowFile`/`decodeWorkflowBody` 解析、`watchWorkflow` 轮询（~120 行）| 中 |
| `commands/worker.go`(203) | signal/ctx 编排（同 serve 模式）| 中 |
| `httpapi/job_handler.go`(213) | 同步提交语义 `WaitFor`+`clampWait` 在 handler 决策 | 中 |
| `httpapi/artifact_handler.go`(147) | `manifestFor`/`remoteSource`/`safeJoinUnder`(48 行路径安全) | 中 |
| `commands/workflow_md.go`(150) | `mergeStepFromMarkdown` 字段优先级合并、frontmatter 解析（域逻辑）| 中 |
| `commands/project.go`(331) | `runProjectAddInteractive` 交互循环 | 低 |

### B 类：业务层巨型文件 / 混职责（不在本轮，见 §11 / 留后续）

| 位置 | 问题 | 处置 |
|---|---|---|
| `job/service.go`(1025) / `job/workflow.go`(1402) | 上帝文件，10/14+ 职责挤一起（同包其它已拆好） | **附录 epic §11**（同包拆文件，零测试改动）|
| `client/client.go`(555) | SSE 流消费混进 API client | 次要，可随 client/watch 顺带 |
| `wshub/hub.go`(470) | 容量/掉线失败策略混进连接管理 | **留 WP4**（依赖未来多 worker 调度）|

**已确认 clean（不动）**：`mcpserver`、`jobstore`（纯 DAO）、`worker`、`runner/*`、`notify`、`project`、`agent`、`config{loader,writer,overlay}`、多数 httpapi handler。

## 4. 核心取向

- **零行为变化（最高原则，D-B1）**：本轮全部是「搬家 / 抽接口 / 拆文件」，**不改任何业务逻辑**。验证靠现有测试全绿 + 关键命令冒烟，不新增行为。
- **A/B 分期**：本轮只做 **A 类入口下沉**（对齐用户原始目标）；**B 类 job 上帝文件拆分**作为紧随的独立 epic（§11）——A 类跨包搬家+改调用方需谨慎，B 类同包拆文件零行为变化，混一个 PR 会让"搬家"与"拆分"的 diff 纠缠、难评审。
- **测试是安全网**：每个阶段搬完即跑**全量 `go test ./...`** 确认绿（gofer 有 1143 测试）；行为等价由测试背书。

## 5. 目标分层（重构后）

```
入口层(只做 绑定/校验/转发):  commands/  httpapi/  mcpserver/
                                  │         │          │
                                  ▼         ▼          ▼
编排层(新):              internal/core   internal/serve   internal/streaming
  core   = 运行时组装(buildCore/Core/Reload, serve+mcp+worker 共用)
  serve  = serve 进程编排(Start + 5 个后台循环)
  streaming = job 流式(pump logs/events/interactions + 节流/eviction)
                                  │
业务层:                      internal/job (Service + 扩 SubmitSync/GetArtifactManifest)
                                  │
数据/能力层:    jobstore / project / agent / runner(+probe) / wshub / notify / store / config
```

新增/调整包职责：
| 包 | 职责 | 吸收来源 |
|---|---|---|
| **`internal/core`**（新）| 运行时组装：`Core`/`buildCore`/`Reload`/`hubWorkerSelector` | `commands/assemble.go` |
| **`internal/serve`**（新）| serve 进程编排：`Start(cfg,opts)` + 5 个 `start*Loop` | `commands/serve.go` 主体 |
| **`internal/streaming`**（新）| job 流式输出：`StreamJob(...)` + pump*/节流/eviction | `httpapi/stream_handler.go` 逻辑 |
| `internal/runner`（扩）| 健康探针 `probe.go` | `commands/runner_probe.go` |
| `internal/job`（扩接口）| `SubmitSync(req,maxWait)`、`GetArtifactManifest(id)` | `httpapi/job_handler.go`、`artifact_handler.go` |
| `internal/client`（扩）| `watch.go` 统一 watch/poll 状态机 | `commands/job.go`、`workflow.go` 的 watch |
| `internal/store`（扩）| `safeJoinUnder` 路径安全（独立可测）| `httpapi/artifact_handler.go` |
| `internal/job`（域逻辑）| workflow 文件解析 `mergeStepFromMarkdown`/`parseWorkflowFile` | `commands/workflow_md.go`、`workflow.go` |
| `internal/config`（扩）| `validate.go`：server/worker 配置校验编排 | `commands/config.go` |

## 6. 范围

**做**（A 类入口下沉）：core / serve / streaming 三新包 + runner/probe 归位 + job.Service 扩 2 接口 + client/watch + 解析下沉 + config validate 下沉 + httpapi handler 瘦身 + safeJoinUnder→store。收尾后三入口层只剩绑定/校验/转发。

**不做**：
- **B 类 job 上帝文件拆分**（§11 独立 epic）。
- `wshub/hub.go` 容量/故障策略外移（留 WP4）。
- **任何业务逻辑/行为改动**（纯结构调整，D-B1）。
- 不动已 clean 的包。

## 7. 决策点

- **D-B1 零行为变化**：本轮不改任何逻辑分支/语义；搬家后函数体逐字保留，仅调包/调用方。每阶段全量 `go test ./...` 绿 = 验收硬门。
- **D-B2 新包划分**：`internal/core`（组装）/ `internal/serve`（serve 编排）/ `internal/streaming`（流式）三新包 + 既有包扩展（runner/probe、client/watch、store、config/validate、job 域解析）。
- **D-B3 依赖方向（防环）**：入口层 → 编排层(core/serve/streaming) → job → 数据层；core 被 commands(serve/mcp/worker) 调、core 调 job 等。**job 及以下绝不反向依赖 core/serve/streaming/commands/httpapi**。新包建好后 `go build`/`go vet` 即暴露环。
- **D-B4 worker 编排归位**：`commands/worker.go` 的 signal/ctx 编排移到 `internal/worker`（已存在包）的 `Run(ctx,...)`；worker 命令 RunE 仅调它。
- **D-B5 job.Service 扩接口而非搬逻辑到入口**：`SubmitSync`/`GetArtifactManifest` 把 httpapi handler 里的同步等待/清单解析语义收进 Service（mcpserver 若有对应也复用），handler 变薄转发。
- **D-B6 B 类独立 epic**：job 上帝文件拆分（§11）零行为变化、零测试改动，A 类收尾后单独推进，不并本轮。
- **D-B7 分阶段独立提交**：每个新包/归位一个阶段、独立 commit（SR1202），每步可单独回滚、单独评审。

## 8. 重构项方案（落点）

> 通则：被搬函数**函数体逐字保留**，仅改包名/导出性/调用方 import。每阶段后 `go build ./... && go vet ./... && go test ./...` 必须全绿。

1. **`internal/core`**：移 `assemble.go` 的 `Core`/`buildCore`/`Reload`/`Close`/`hubWorkerSelector`/`workerBindings`/`hubWorkerRegistry`。commands 的 serve/mcp/worker 改 `core.Build(cfg)` / `core.Reload(...)`。（`buildCore` 现是包内私有，导出为 `core.Build`。）
2. **`internal/serve`**：移 `serve.go` 的 `runServe` 主体 + 5 个 `start*Loop` + `resolveToken`/`proberOrNil`/`workerCounts` 等 helper 为 `serve.Start(c, cfg, opts)`。`commands/serve.go` 仅留 flag 绑定 + 调 `serve.Start`。
3. **`internal/runner/probe.go`**：移 `runner_probe.go` 的 `peerProber`/`newPeerProber`/`probeOnce`/`probeTarget`。serve 的 `startProbeLoop` 调 `runner` 包。
4. **`internal/streaming`**：移 `stream_handler.go` 的 `pumpLogs`/`pumpInteractions`/`pumpEvents`/节流/eviction 为 `streaming.StreamJob(ctx, w, jobs, id, opts)`。`httpapi/stream_handler.go` 仅解析请求 + 调它。
5. **`job.Service` 扩接口**：`SubmitSync(req, maxWait)`（收 `job_handler.go` 的 `WaitFor`+`clampWait`）；`GetArtifactManifest(id)`（收 `artifact_handler.go` 的 `manifestFor`/`remoteSource`）。handler 变薄。`safeJoinUnder` → `internal/store`（导出 + 单测）。
6. **`internal/client/watch.go`**：统一 `commands/job.go` 的 `watchToTerminal` + `workflow.go` 的 `watchWorkflow` 为 client 侧 `WatchJob`/`WatchWorkflow`（消除重复 SSE/轮询状态机）。顺带把 `client/client.go` 的 SSE 消费抽到 `watch.go`（B 类次要项搭车，零行为变化）。
7. **域解析下沉**：`workflow_md.go` 的 `mergeStepFromMarkdown` + `workflow.go` 的 `parseWorkflowFile`/`decodeWorkflowBody` → `internal/job`（与 workflow 业务同包）。命令仅读文件 + 调解析。
8. **`internal/config/validate.go`**：移 `config.go` 的 `validateServerConfig`/`validateWorkerConfig`/`validateProjects` 编排。`runConfigEdit` 的编辑器探测抽 helper（留 commands 或 internal/cli）。
9. **`resolveProjectByCwd`**（job.go）→ `internal/project` 或 `internal/client`（cwd→project 反查是通用工具）。

## 9. 风险与验证

- **import 环**（主要风险）：core/serve/streaming 新包必须单向依赖 job 及以下；新建后 `go build`/`go vet` 立即暴露环（D-B3）。若 job 包某处反向需要（不应有），停下重审边界。
- **零行为变化保证**：函数体逐字搬；每阶段全量 `go test ./...`（1143 测试）绿；关键命令冒烟（`serve` 起停、`job run`、`config validate/info`、stream/SSE、worker 连接）。
- **测试归属**：搬走的逻辑若有专属测试，测试随之迁移到新包；保持覆盖不降。
- **分阶段可回滚**：每阶段独立 commit，出问题单独 revert。

## 10. 收益

- 三入口层（commands/httpapi/mcpserver）只做绑定/校验/转发，新人一眼看清"命令做什么"。
- 编排逻辑（serve 循环、组装、流式）集中可测、可复用（serve/mcp/worker 共用 core）。
- 为后续（B 类 job 拆分、WP4 调度、E28 mcp HTTP-client）腾出清晰边界。

## 11. 附录 Epic（B 类，紧随本轮、独立 PR）：job 上帝文件拆分 ✅ 已完成

`job/service.go`(1025) → 按职责拆 `submit.go`/`execute.go`/`persistence.go`/`concurrency.go`/`config.go`；`job/workflow.go`(1402) → 拆 `workflow_advance.go`（最高价值，~150 行状态机）/`workflow_submit.go`/`workflow_join.go`/`workflow_query.go`/`workflow_terminate.go`/`workflow_cancel.go`。**同包拆文件、调用方零改动、测试零改动**——纯按职责切分，`go test` 即背书等价。优先 `workflow_advance.go`。不并入本轮（避免 diff 纠缠）。

> **已完成（2026-06-25）**：11 步逐文件拆+全量 test 绿+独立 commit，service.go→235、workflow.go→225，函数 44/30 守恒，等价性校验通过（见 `docs/plans/2026-06-25-job-split-plan.md §7`）。
> **遗留**：拆文件解决了「单文件可读性」，但 `internal/job` 仍是 **29 文件/~5000 行的巨型包**（单 job 引擎 + workflow 引擎 + interaction + delivery + events + outcomes + refs 混在同一包），**包级分层未动** → 见 §13。

## 12. 结论 + 分阶段（plan 预告）

全部**零行为变化**、测试背书。建议阶段（每阶段独立 commit + 全量 test 绿）：
- **BP1** `internal/core`（assemble 移出）
- **BP2** `internal/serve`（runServe + startLoop 移出）+ worker 编排归位（internal/worker）
- **BP3** `internal/runner/probe.go`（runner_probe 归位）
- **BP4** `internal/streaming`（stream_handler 下沉）
- **BP5** `job.Service` 扩 `SubmitSync`/`GetArtifactManifest` + httpapi handler 瘦身 + `safeJoinUnder`→store
- **BP6** `client/watch.go` + 域解析下沉（workflow 解析）+ `config/validate.go` + `resolveProjectByCwd` 归位

> BP1→BP2 有依赖（serve 用 core）；BP3/BP4/BP5/BP6 相对独立，可并行或顺序。审核通过后出 `plans/2026-06-25-code-layering-refactor/`（或单文件 plan）。
>
> **附录 epic（job 拆分）**单独出 plan，A 类收尾后推进。

## 13. 附录 Epic（B2 类，独立 PR）：job 巨型包的包级分层 —— `internal/job/workflow` 子包抽取 ✅ 已完成

> **已完成（2026-06-25）**：WS1/WS2.0/WS2/WS3 全绿落地，637 测试不减、无环。实施与偏差见 `docs/plans/2026-06-25-job-workflow-subpkg-plan.md §11`（`JobOps` 实际含 `Get/TailLog/Wait`；`maxRetryAttempts` 留 workflow 私有）。模式沉淀为 `CLAUDE.md G024`。

### 13.1 问题：拆文件 ≠ 分层

§11 把两个上帝文件按职责拆成多文件，**单文件可读性**解决了，但 `internal/job` 仍是 **29 文件 / ~5000 行的巨型包**：单 job 引擎、workflow 链引擎、interaction、delivery、events、outcomes、refs 全挤在同一包里，const/struct/func 混杂。包级耦合未动 —— 入口看到的仍是一个无边界的大 `job`。本节把**最大、最自洽**的子域 `workflow`（9 文件 / 25 方法 + 29 自由函数 / ~1400 行）升为子包 `internal/job/workflow`，作为包级分层的旗舰。

### 13.2 升包判据（D-B8）

**拆文件改善阅读，升包改善边界——只有当一个子域满足以下全部，才升包：**
1. **域自洽**：有完整的「类型 + 状态机 + 生命周期」（workflow 有 Spec/Step + advance 状态机 + submit→done/failed/cancel 生命周期）；
2. **反向 seam 够窄**：宿主对它的反向依赖少到能用一个窄接口/回调倒置（workflow 的反向依赖**只有 1 处**：`execute.go:finish()` 的 `advanceWorkflow`）；
3. **正向依赖可接口化**：它对宿主的依赖能收敛成一个能力接口（`JobOps`，~7 项）；
4. **收益 > 代价**：包瘦身显著 + 子域可独立测试，盖过类型改名/测试迁移成本。

不满足（尤其 seam 太宽）的子域**不升包**，留在 job 内按文件聚合即可（见 §13.8）。

### 13.3 目标包结构与依赖方向

```
internal/job/                  ← 宿主：job 模型(JobRequest/JobResult) + 单 job 引擎
  ├─ (定义) WorkflowAdvancer 接口   ← 唯一反向 seam，finish() 经它驱动链推进
  └─ workflow/               ← 新子包：链编排域
       ├─ types.go           Spec / StepSpec / Step / RetryPolicy + 常量
       ├─ engine.go          Engine{ops JobOps} + 25 方法(原 *Service→*Engine)
       ├─ advance/submit/join/query/terminate/cancel/parse/export/refs.go
       └─ (定义) JobOps 接口     ← 向宿主索取的能力(~7 项)

依赖方向（单向，无环）：
  httpapi/commands/serve ──▶ internal/job/workflow ──▶ internal/job ──▶ jobstore/config/...
                              （workflow 仅 import job 取 JobRequest/JobResult 类型）
  internal/job ──(仅接口)──▶ WorkflowAdvancer   ← 实现体由 core 注入，job 不 import workflow
```

**防环关键（D-B9）**：`internal/job` **绝不** import `workflow`；反向调用只通过 job 内定义的 `WorkflowAdvancer` 接口（实现体在 wiring 时注入）。`workflow` 单向 import `job`（取 `JobRequest`/`JobResult` 类型）。`go list -deps` 验。

### 13.4 双向耦合的依赖倒置（核心，D-B10）

**反向 seam（job → workflow，仅 1 处）** —— job 侧定义接口、Service 持有、`finish` 经它回调：

```go
// internal/job —— 唯一反向依赖，由 core 在 wiring 时注入 engine；nil 时不触发(纯单 job 部署)
type WorkflowAdvancer interface{ Advance(wfID string) }
func (s *Service) SetWorkflow(w WorkflowAdvancer) { s.wf = w }
// execute.go finish(): if s.wf != nil && snap.WorkflowID != "" { go s.wf.Advance(snap.WorkflowID) }
```
> sweeper 的 `AdvanceRunningWorkflows(ctx)` 不进此接口——它由 serve 循环**直接**调 `engine.AdvanceRunning(ctx)`（serve 可同时 import job 与 workflow）。

**正向能力（workflow → job，~7 项）** —— workflow 侧定义 `JobOps`，Service 经薄适配器满足：

```go
// internal/job/workflow
type JobOps interface {
    Submit(req job.JobRequest) (job.JobResult, error)            // 起 step-job
    Validate(cfg *config.Config, req job.JobRequest, remote bool) (config.ProjectConfig, error)
    Cancel(jobID string) error                                  // 取消在飞 fan
    Meta() *jobstore.Store                                      // 工作流行读写
    Config() *config.Config
    Now() time.Time
    WorkflowTerminal(status string, durationSec float64)        // 复用 MetricsSink 同名
}
type Engine struct{ ops JobOps /* +内部状态 */ }
func (e *Engine) Advance(wfID string) { /* 原 advanceWorkflow */ }   // 实现 job.WorkflowAdvancer
```

**wiring（internal/core assemble）**：
```go
svc := job.NewService(...)
eng := workflow.NewEngine(jobOps{svc})   // jobOps 适配器把 svc 的 Submit/validate/Cancel/meta/... 暴露给 JobOps
svc.SetWorkflow(eng)                       // 装反向 hook
// serve 循环改调 eng.AdvanceRunning(ctx)；httpapi workflow_handler 改持有 eng
```
> `validate`/`meta`/`config`/`nowFn` 现为 job 内私有：升包时给出**导出 accessor 或 core 内适配器**（不改语义）。`resolveRefs`(refs.go) 是 workflow 域（${steps.N.field} 解析），随域迁入子包。

### 13.5 迁移清单与外部影响面

| 项 | 动作 | 量 |
|---|---|---|
| 公共类型 `WorkflowSpec/StepSpec/WorkflowStep/RetryPolicy` | → `workflow.{Spec,StepSpec,Step,RetryPolicy}`，外部引用改名 | 外部 ~40 处（httpapi 25+12+2+1 / commands / mcpserver），机械 sed |
| 25 个 `(s *Service)` workflow 方法 | receiver 改 `(e *Engine)`，函数体逐字 | 25 |
| 29 个自由函数 + refs.go | 随域迁入子包 | 29+ |
| `finish` 反向调用 | 改经 `s.wf.Advance` | 1 |
| 公共方法调用点（`SubmitWorkflow`/`GetWorkflow`/...） | 调用方改持 `engine`（httpapi handler / serve / core 注入） | 中 |
| `workflow_*_test.go` | 迁到 `package workflow`，以 `JobOps`（真 Service 适配器或 fake）构造 Engine；**断言不变** | 测试迁移（非零改动）|

### 13.6 与 §11 的本质区别（D-B11：放宽「零测试改动」、保留「零行为变化」）

§11 是同包拆文件 → 零行为变化**且**零测试改动。本节是包级重构：**逻辑逐字、断言不变（零行为变化保留）**，但类型改名 + 测试迁包 + wiring 倒置 → **打破「零测试改动」**。这是 B2 与 B 的分界，须单独 PR、单独评审。收益：`job` 包瘦身 ~1400 行 / 边界清晰；workflow 可用 fake `JobOps` 独立快测（不起全 Service）；对齐 `internal/runner/{local,peerhttp,worker}` 子包先例。

### 13.7 风险与验证

- **import 环**（首要）：靠 D-B9（job 不 import workflow，反向仅接口）；每步 `go build`/`go vet`/`go list -deps github.com/inhere/gofer/internal/job | grep workflow`（应为空）验。
- **零行为变化**：函数体逐字；全量 `go test ./...`（现 1143 测试，迁移后总数不降）绿 + 冒烟（workflow 提交/推进/重试/fan-join/子工作流/取消、sweeper 兜底、parent-advance）。
- **nil-engine 安全**：`s.wf==nil`（纯单 job 部署/未装 engine）时 `finish` 不触发 advance，等价旧「非工作流 job 不触发」。

### 13.8 分期（plan 预告）

> 实施计划：`docs/plans/2026-06-25-job-workflow-subpkg-plan.md`（WS1 暴露 JobOps 访问器 → WS2a types 下移 → WS2b Engine 抽取+wiring+测试迁移 → WS3 收尾）。
> 实施细节修正：因 core 包访问不到 Service 私有成员，**改为在 Service 上加 5 个薄导出访问器（Meta/Now/Config/Validate/Metrics）让其直接满足 `JobOps`**，不另设 core 适配器。

- **WS1（包内立缝，最低风险）**：job 内引入 `WorkflowAdvancer` 间接（`s.wf` 字段或 `wfAdvance func` 默认指向 `advanceWorkflow`）+ 暴露 `JobOps` 所需 accessor；`finish` 改经缝。仍全在 job，行为/测试近乎不变 —— 先把 seam 验通。
- **WS2（抽包，主体）**：建 `internal/job/workflow`，迁类型(改名)+自由函数+25 方法(→Engine)+refs；定义 `JobOps`、Engine 实现 `WorkflowAdvancer`；core 装配 + serve/httpapi 改持 engine；测试迁包接 `JobOps`。体量大可再分 WS2a(类型+wiring 立骨架) / WS2b(方法体迁移)。
- **WS3（收尾）**：删 job 内 workflow 残留符号；`go list -deps` 验无环；更新 `CLAUDE.md` G021-G023 增「子域升包判据(D-B8)」。

### 13.9 其余子域：候选与判据（本轮不做）

按 D-B8 判据逐个评估，**seam 够窄才升包**，否则留 job 内按文件聚合：

| 子域 | 文件 | 评估 | 处置 |
|---|---|---|---|
| **interaction** | interaction.go / remote_interaction.go | **已评估(2026-06-25)：不抽包**。状态 `entry.interactions` 挂 jobEntry、与 `entry.result` 共用 `entry.mu`，且直接翻转 job 状态 `pending_interaction↔running`（`StatusPendingInteraction` 是 job 核心状态）、靠 `entry.done` 唤醒——与单 job 引擎共享可变内存+锁+done 信号，反向 seam 宽（判据②不过），与 workflow(状态在 DB、仅 1 处 finish→advance)categorically 不同 | 留 job 包（文件级聚合已足够）|
| **delivery（+events）** | delivery.go / events.go | **已评估(2026-06-25)：defer**。状态在 DB、delivery.go 0 碰 jobEntry、sweeper(DeliverDue)窄——不被①③否决；但①enqueue 触发(`enqueueDeliveries`/`deliverySink`)在 events.go，须与 events 捆绑抽 `notify`；②`recordEvent` 横切 **8 处**生命周期(submit/execute/finish/cancel/interaction)，反向 seam 宽；③`eventSink`/`deliverySink` 已是接口、半解耦收益已拿到。判据②偏宽+④收益不抵 → 留 job，待 notify 子系统膨胀再议 | 留 job（defer）|
| **outcomes** | outcomes.go | **已评估(2026-06-25)：不抽**。`captureOutcomes(entry,…)` 在 execute.go 路径内写 `entry.result.*`(26 次 jobEntry/`entry.mu`)，是单 job 执行的产出采集环节，与 jobEntry/execute 不可分（同 interaction 类，判据①②不过）；自由函数 goferJobEnv/ScanArtifacts 等是 job 工具 | 留 job |
| **refs** | refs.go | workflow 专属 | **随 workflow 升包一并迁入**（见 §13.4）|

> 原则：**先把最大最干净的 workflow 升包、跑通依赖倒置模式，再以同一模式按需复制**；不一次性铺开全包拆解（决策面过大、PR 过重）。

**子域升包三类判例（2026-06-25 评估沉淀，后续按此速判）**：
1. **抽**（workflow）：状态在 DB + 反向 seam 窄（单点 hook）+ 不碰 jobEntry → 依赖倒置干净升包。
2. **不抽·jobEntry 内嵌**（interaction / outcomes）：状态/逻辑挂 `jobEntry`、直接改 job 状态或 `entry.result`、共用 `entry.mu` → 判据②死，是单 job 进程内生命周期的一环，必须同包。
3. **不抽·横切但浅**（delivery+events）：不碰 jobEntry、状态在 DB、可接口化，但反向 seam 横切多点（`recordEvent` 8 处）+ 已用 sink 半解耦 + 收益中等 → defer，待子系统膨胀再议。

> 速判口诀：先看**状态是否独立于 jobEntry**（否则直接归第 2 类不抽）；再看**反向 seam 是单点 hook 还是横切多点**（横切则归第 3 类 defer）；两关都过且收益够大才抽。
