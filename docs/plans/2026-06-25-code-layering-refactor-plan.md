# Gofer 代码分层重构实施计划（B 组 · 入口逻辑下沉）

> 对应 design [`../design/2026-06-25-code-layering-refactor-design.md`](../design/2026-06-25-code-layering-refactor-design.md)（D-B1..D-B7）。
> **铁律（D-B1）**：纯搬家/抽接口/拆文件，**零行为变化**——被搬函数体逐字保留，仅改包/导出性/调用方 import。每阶段验收硬门 = `go build ./... && go vet ./... && go test ./...` 全绿（防 import 环 + 行为等价）+ 关键命令冒烟。

## 1. 总纲

| 阶段 | 目标 | 依赖 | 工作量 |
|---|---|---|---|
| **BP1** | `internal/core`：组装层（assemble.go 移出 commands）| — | 中 |
| **BP2** | `internal/serve`：serve 进程编排 + worker 编排归位 | BP1 | 中 |
| **BP3** | `internal/runner/probe.go`：健康探针归位 | (BP2 用) | 小 |
| **BP4** | `internal/streaming`：stream_handler 流式下沉 | — | 中 |
| **BP5** | `job.Service` 扩 `SubmitSync`/`GetArtifactManifest` + handler 瘦身 + `safeJoinUnder`→store | — | 中 |
| **BP6** | `client/watch.go` + 域解析下沉 + `config/validate.go` + `resolveProjectByCwd` 归位 | — | 中 |

> BP1→BP2 有序（serve 用 core）；BP3/BP4/BP5/BP6 相对独立，可顺序或并行。每阶段独立 commit（D-B7）。

## 2. 关键原则

- **零行为变化**：函数体不动，只搬位置 + 调包名 + 改调用方 import。**不重构逻辑、不改分支/语义**。
- **测试是安全网**：每阶段后全量 `go test ./...`（含 job 47s/httpapi 22s）必须绿——行为等价由测试背书。专属测试随逻辑迁移到新包（覆盖不降）。
- **防 import 环（D-B3）**：core/serve/streaming **单向依赖** job 及以下；`go build`/`go vet` 立即暴露环。job 及以下**绝不**反向依赖 core/serve/streaming/commands/httpapi。
- **导出性**：私有符号（如 `buildCore`）跨包需导出（`core.Build`）；仅在原包内用的 helper 尽量保持最小导出面。

## 3. 前置检查

- [ ] 工作区干净；`go build ./... && go vet ./... && go test ./...` 基线全绿（19 包）。
- [ ] codebase 索引可用（`d-work-...-tools-gofer`），辅助 `search_code`/`query_graph` 找调用方。
- [ ] 确认 A 组已全提交（`-c` 收敛/serve 打印/config edit·info/p add -i）——B 组在其上。

## 4. 进度跟进

- [ ] **BP1** internal/core（assemble 移出）
- [ ] **BP2** internal/serve（runServe+startLoop）+ worker 编排归位
- [ ] **BP3** internal/runner/probe.go
- [ ] **BP4** internal/streaming（stream_handler 下沉）
- [x] **BP5** job.Service 扩接口 + handler 瘦身 + safeJoinUnder→store
- [x] **BP6** client/watch + 域解析 + config validate(留 commands, 避环) + resolveProjectByCwd→project

---

## BP1：internal/core（组装层）

**搬移**（`internal/commands/assemble.go` → 新 `internal/core/core.go`，函数体逐字）：
| 符号 | 现 | 去向 |
|---|---|---|
| `Core` 结构 | assemble.go:25 | `core.Core` |
| `buildCore` | assemble.go:52 | `core.Build`（导出）|
| `(*Core) Reload` | assemble.go:146 | `core.(*Core).Reload` |
| `(*Core) Close` | assemble.go:40 | 同 |
| `hubWorkerSelector` + `Candidates` | assemble.go:91 | core |
| `workerBindings` | assemble.go:119 | core |
| `hubWorkerRegistry`（在 serve.go）| serve.go:137 | core（worker 观测适配器）|

**调用方改**：`commands/serve.go`/`mcp.go`/`worker.go` 里 `buildCore(cfg)` → `core.Build(cfg)`；`core.Close`/`core.Reload` 同步。`startReloadLoop` 调 `core.Reload`（BP2 一起移；BP1 暂留 commands 调 core）。

**验收**：`go build/vet/test ./...` 全绿；assemble.go 删空或仅留命令无关；无 import 环（core 不依赖 commands）。

---

## BP2：internal/serve + worker 编排归位

**搬移**（`internal/commands/serve.go` 主体 → 新 `internal/serve/serve.go`）：
- `runServe` 主体 → `serve.Start(c *gcli.Command, cfg *config.Config, opts Opts)`（opts 收 addr/token/allowEmptyTok/noWeb）。
- `startPruneLoop`/`startDeliveryLoop`/`startWorkflowLoop`/`startReloadLoop`/`startProbeLoop` → serve 包。
- `resolveToken`/`proberOrNil`/`workerCounts` 等 helper → serve 包。
- **`commands/serve.go` 仅留**：flag 绑定（含 `bindConfigFlag`）+ `serve.Start(...)` 调用 + serve 打印配置路径（A 组 P2，可留命令层或入 serve.Start）。

**worker 归位**（`commands/worker.go` 的 signal/ctx 编排 → `internal/worker`）：
- worker.go:86-97 的 signal/ctx 起停 → `worker.Run(ctx, cfg, ...)`（worker 包已存在）。
- `commands/worker.go` 仅 flag 绑定（`--worker-config`）+ 调 `worker.Run`。

**依赖**：serve → core（BP1）→ job…。验证无环。

**验收**：`go build/vet/test ./...` 全绿；**冒烟**：`gofer serve --allow-empty-token`（timeout 起一瞬看 5 个 loop 日志 + config 路径打印 + 优雅停）；`gofer worker --worker-config X`（到 worker_id 校验）。

---

## BP3：internal/runner/probe.go

**搬移**（`internal/commands/runner_probe.go` → `internal/runner/probe.go`）：
- `peerProber`/`newPeerProber`/`probeOnce`/`probeTarget`（+ 相关类型）逐字搬。
- 注意 httpapi 的 `runnerProber`/`ProbeResult` 接口：runner/probe 实现它（接口留 httpapi，runner 实现，依赖方向 runner→httpapi? 不可！）。**核对**：若 `ProbeResult` 是 httpapi 定义、prober 需实现，改为在 runner 包定义 `ProbeResult` 或用中性类型，httpapi/serve 引用 runner 的——避免 runner→httpapi 反向依赖。serve 的 `proberOrNil`（BP2 已入 serve）适配。

**调用方**：`internal/serve` 的 `startProbeLoop`/`newPeerProber` → `runner.NewPeerProber`。

**验收**：`go build/vet/test ./...` 全绿；无 runner→httpapi 反向环。

---

## BP4：internal/streaming（流式下沉）

**搬移**（`internal/httpapi/stream_handler.go` 逻辑 → 新 `internal/streaming/streaming.go`）：
- `pumpLogs`/`pumpInteractions`/`pumpEvents` + 动态节流 + eviction 回退 + offset/游标跟踪 → `streaming.StreamJob(ctx, w io.Writer, jobs *job.Service, id string, opts StreamOpts)`。
- **`httpapi/stream_handler.go` 仅留**：解析请求（id/from/stream 参数）+ SSE 头 + 调 `streaming.StreamJob` + 错误映射。瘦到 ~130 行。

**依赖**：streaming → job.Service（读日志/事件/交互的既有方法）。验证无环。

**验收**：`go build/vet/test ./...` 全绿；**冒烟**：起 serve 提交一个 job，`GET /v1/jobs/{id}/stream`（curl）收到 log/status/end 帧（含历史回放 + 实时跟随 + 轮转）。

---

## BP5：job.Service 扩接口 + handler 瘦身 + safeJoinUnder→store

**job.Service 扩**（`internal/job/`，新方法或并入既有文件，逻辑收自 handler）：
- `SubmitSync(req, maxWait)`：收 `httpapi/job_handler.go` 的 `WaitFor`+`clampWait` 同步等待语义（默认/上限钳制 + async 回退判定）。
- `GetArtifactManifest(id)`：收 `httpapi/artifact_handler.go` 的 `manifestFor`/`remoteSource`（清单解析 + 远端源定位）。

**safeJoinUnder → store**：`httpapi/artifact_handler.go:safeJoinUnder`（48 行路径安全）→ `internal/store`（导出 + 独立单测）。

**handler 瘦身**：`job_handler.go` 同步提交 → 调 `SubmitSync`；`artifact_handler.go` → 调 `GetArtifactManifest` + `store.SafeJoinUnder`。mcpserver 若有对应（`resolveArtifacts`）可复用 `GetArtifactManifest`（消除注释标注的"故意重复"，可选）。

**验收**：`go build/vet/test ./...` 全绿；**冒烟**：`POST /v1/jobs sync`（同步等终态）；`GET /v1/jobs/{id}/artifacts[/{name}]`（清单+下载+remote 409）。`store` 新增 safeJoinUnder 单测（路径穿越拒绝）。

---

## BP6：client/watch + 域解析下沉 + config/validate + resolveProjectByCwd

**client/watch.go**（新）：统一 watch/poll 状态机——
- 收 `commands/job.go` 的 `watchToTerminal` + `workflow.go` 的 `watchWorkflow` → `client.WatchJob`/`WatchWorkflow`。
- 顺带把 `client/client.go` 的 SSE 流消费（L395-483）抽到 `watch.go`（B 类次要项搭车，零行为变化）。
- 命令 job/workflow 的 watch 调 client 侧。

**域解析下沉**（→ `internal/job`，与 workflow 业务同包）：
- `commands/workflow_md.go` 的 `mergeStepFromMarkdown` + frontmatter 解析。
- `commands/workflow.go` 的 `parseWorkflowFile`/`decodeWorkflowBody`。
- 命令仅读文件 + 调解析。

**config/validate.go**（→ `internal/config`）：
- `commands/config.go` 的 `validateServerConfig`/`validateWorkerConfig`/`validateProjects` 编排。
- `runConfigEdit` 编辑器探测抽 helper（留 commands 或 internal/cli）。

**resolveProjectByCwd**（`commands/job.go`）→ `internal/project`（cwd→project 反查通用工具）；命令调 `project.ResolveByCwd`。

**验收**：`go build/vet/test ./...` 全绿；**冒烟**：`gofer job watch <id>`（实时跟到终态 + exit code）；`gofer wf run <file>`（md+yaml 解析）；`gofer config validate`（server/worker）；项目目录内 `gofer job run`（cwd 自动识别）。

---

## 5. 完成判定

- BP1-6 各阶段验收 PASS；最终 `go build/vet ./...` + 全量 `go test ./...`（19 包）绿。
- **行为等价**：全程零逻辑改动，测试全绿背书；关键命令冒烟（serve 起停/job run·sync·watch/stream SSE/artifact/config validate·info/worker/cwd 识别）。
- 三入口层（commands/httpapi/mcpserver）只剩绑定/校验/转发；编排集中到 core/serve/streaming。
- 无 import 环；专属测试随逻辑迁移、覆盖不降。
- 各阶段独立 commit（D-B7）。

## 6. 附录 Epic（B 类，独立 plan，A 类收尾后）

`job/service.go`(1025)/`workflow.go`(1402) 上帝文件**同包拆文件**（零行为变化、零测试改动）：
- `service.go` → `submit.go`/`execute.go`/`persistence.go`/`concurrency.go`/`config.go`
- `workflow.go` → `workflow_advance.go`(优先,~150 行状态机)/`workflow_submit.go`/`workflow_join.go`/`workflow_query.go`/`workflow_terminate.go`/`workflow_cancel.go`
不并入本轮（避免与 A 类"搬家" diff 纠缠）；单独出 plan。

## 7. 实施结果（完成后回填）

> BP1-6 commit + 关键决策（尤 import 环处理、接口归属）+ 验收/冒烟 + 遗留。

**BP6**（client/watch + 域解析下沉 + config validate/cwd 归位）：
- `client/watch.go`（新）：`OpenStream`/`openStream`/`StreamJob` 从 client.go 平移 + 新增 `WatchJob`/`WatchWorkflow`（回调式 handlers，状态机入 client，打印 + exit-code 映射留 commands）。`watchToTerminal`/`watchWorkflow` 改调 client 侧。
- 域解析 → `internal/job/workflow_parse.go`：`ParseWorkflowFile`(导出) + `decodeWorkflowBody`/`expandStepMarkdown`/`parseStepMarkdown`/`mergeStepFromMarkdown`/`splitWorkflowFrontmatter`(包内)。`commands/workflow_md.go` 删除；`commands/workflow.go` 仅调 `job.ParseWorkflowFile`。
- `resolveProjectByCwd` → `internal/project/resolve.go` 的 `ResolveByCwd`（cwd→project，project 用 config.Config 正向）。
- **config validate 去向 = 保留 commands**（关键决策）：`validateServerConfig`/`validateWorkerConfig`/`validateProjects` 深度依赖命令层（`gcli.Command` 打印、`errorx.Failf`+`configExitErr`、`ccolor`、`loadRegistry`/`loadWorkerConfig`），真正的校验逻辑早已在 `project.Registry.Validate`；移到 `internal/project` 会把 gcli/errorx/ccolor 拉进域包（分层倒退），且 plan 已指出"→ internal/config"会成 `config→project` 反向环。`runConfigEdit` 的编辑器探测抽为 `resolveEditor()` helper（留 commands）。
- 测试随迁：cwd 5 测 → `internal/project/resolve_test.go`；workflow 解析/md 8 测 → `internal/job/workflow_parse_test.go`。
- 验收：`go build`/`go vet` 无环；全量 `go test ./...`(24 包)全绿；冒烟 config validate(OK/越权FAIL exit=2)、wf run md+yaml 解析 OK、job run 无 -p cwd 自动识别 OK。
