# Gofer 代码分层重构设计（入口层逻辑下沉）

> 一句话：把 `commands` / `httpapi` / `mcpserver` 三个**入口层**里混着的业务/编排逻辑下沉到专用包，让入口只做「**参数绑定 + 校验 + 转发**」；**纯搬家/抽接口、零行为变化**，每步全量测试绿。
> 关联：bd 审计 `gofer-b-refactor-audit`（摸代码产出）；TODO §代码优化（B 组）。本文新决策从 **D-B1** 起。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-06-25 | inhere | 初稿：现状审计 + 目标分层 + 新包划分 + D-B1..D-B7 + 分阶段（A 类入口下沉本轮，B 类 job 拆分附录 epic），待审核 |

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

## 11. 附录 Epic（B 类，紧随本轮、独立 PR）：job 上帝文件拆分

`job/service.go`(1025) → 按职责拆 `submit.go`/`execute.go`/`persistence.go`/`concurrency.go`/`config.go`；`job/workflow.go`(1402) → 拆 `workflow_advance.go`（最高价值，~150 行状态机）/`workflow_submit.go`/`workflow_join.go`/`workflow_query.go`/`workflow_terminate.go`/`workflow_cancel.go`。**同包拆文件、调用方零改动、测试零改动**——纯按职责切分，`go test` 即背书等价。优先 `workflow_advance.go`。不并入本轮（避免 diff 纠缠）。

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
