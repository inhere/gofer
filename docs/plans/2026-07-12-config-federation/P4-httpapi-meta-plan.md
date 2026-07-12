# P4 — 能力明细上 API（/v1/meta cascade + /v1/runners 可观测）

> 主纲: [../2026-07-12-config-federation-plan.md](../2026-07-12-config-federation-plan.md) ｜ bd: h-aii-xu64.10 ｜ 依赖: P1（数据）/ P2（snapshot）
> 目标: 把 worker typed 能力(type/interactive)与节点信息暴露给前端。**扩展现有端点、不新增**。
> **校正（dossier）**：web 级联真源是 **`/v1/meta`**（非 `/v1/runners`）——`metaWorker` 已带 projects/agents，`metaAgent` 缺 `interactive`。故 cascade 主改 `meta_handler.go`；`/v1/runners` 只补可观测明细。

## T4.1 serve→httpapi 快照适配加字段

**文件**: `internal/serve/probe.go`（`hubWorkerRegistry.WorkerStatus`，`:18-34`），`internal/httpapi/runner_handler.go`（`WorkerStatus` 结构 `:40-47`）

- `httpapi.WorkerStatus` 加 `AgentCaps []AgentBrief`（httpapi 本地类型，避免 import wshub/wsproto）+ 节点信息 `OS/Arch/GoferVersion/StartedAt`。
- `serve/probe.go` 适配层把 `wshub.WorkerSnapshot.AgentCaps/OS/Arch/...` 转成 httpapi 本地类型填入。
- httpapi 定义本地 `type AgentBrief struct { Key, Type string; Interactive bool }`（json `key/type/interactive`）。

## T4.2 /v1/meta：metaAgent.interactive + metaWorker typed agents

**文件**: `internal/httpapi/meta_handler.go`

- `metaAgent`（`:35-38`）加 `Interactive bool json:"interactive,omitempty"`；填充源=server 全局 `cfg.Agents[k].Interactive`（local 视角 agent 明细）。
- `metaWorker`（`:51-57`）：现有 `Projects []string`+`Agents []string` **保留**；加 `AgentCaps []agentBriefView json:"agent_caps,omitempty"`（typed，来自 `WorkerStatus.AgentCaps`）+ 节点信息可选。
- `metaWorkers`（`:136-156`）填充：把 `WorkerStatus.AgentCaps` 映射进 `metaWorker.AgentCaps`。

**验收 T4.2**: `/v1/meta` 契约单测——`metaAgent` 含 `interactive`；某在线 worker 的 `metaWorker.agent_caps` 带 `{key,type,interactive}`；离线 worker 不带（或 connected=false）。

## T4.3 /v1/runners：workerView typed + 节点信息

**文件**: `internal/httpapi/runner_handler.go`（`workerView` `:91-98`，`renderWorkerStatus` `:177-198`）

- `workerView` 加 `AgentCaps []agentBriefView json:"agent_caps,omitempty"` + `OS/Arch/GoferVersion/StartedAt`（可观测面板/Cluster 视图）。现有 `Agents []string` 保留。
- `renderWorkerStatus` 从 `WorkerStatus` 填 `AgentCaps`/节点信息。
- **local 合成**：`handleListRunners`（`:105-133`）里隐式 local 行——为其补能力明细（projects=keys(cfg.Projects)、agents 由 cfg.Agents typed 合成），使前端对 local runner 也能级联。放 `runnerView` 上一个可选 `Capabilities`（或复用 worker 字段），保证 local 与 worker 视图一致。

**验收 T4.3**: `/v1/runners` 契约单测——worker 行带 typed agent_caps + 节点信息；local 行带合成能力。

## T4.4 web 类型对齐

**文件**: `web/src/api/types.ts`（`MetaAgent`/`MetaWorker` 等 `:558-591`）

- `MetaAgent` 加 `interactive?: boolean`。
- `MetaWorker` 加 `agent_caps?: {key:string; type?:string; interactive?:boolean}[]`（projects/agents 已有）。
- （若 P5 也用 /v1/runners，则同步 runner 类型；否则仅 meta。）

**验收 T4.4**: `pnpm -C web build` / `tsc` 绿（类型对齐，无 runtime 改动）。

## P4 验收总纲 — ✅ 完成（commit `bdec35b`；web build 待 P5 主机跑）

- [x] T4.1 serve/httpapi 快照适配层带 AgentCaps/节点信息（`serve/probe.go` `briefsFromSnapshot`，httpapi 仍不 import wshub/wsproto）
- [x] T4.2 /v1/meta metaAgent.interactive + metaWorker.agent_caps + 契约单测绿
- [x] T4.3 /v1/runners workerView typed + 节点信息 + local 合成 + 契约单测绿
- [~] T4.4 web 类型对齐（types.ts 加 AgentBrief/RunnerCapabilities/interactive/agent_caps）；`pnpm build` **容器内跑不了**（node_modules 缺 tsc/vue-tsc）→ 由 **P5 走主机** `gofer job -a exec --cwd tools/gofer/web -- pnpm install && pnpm build` 验证
- [x] `go test ./internal/httpapi/... ./internal/serve/... -count=1` 绿（全量套件亦绿）

## 实施补记 + P5 契约

- **无新增分层违规**：`httpapi/server.go` 早已直接 import `wshub`（P4 前既存）；P4 的 handler 文件（runner/meta）**0** 直接 wshub/wsproto 引用，经 `serve/probe.go` 适配层桥接。`localAgentCaps` 读**resolved registry**（`s.agents.List()`，含内置 exec、真类型），与 P1 一致——保证 local 能力不漏报 exec。
- **单一导出 `AgentBrief` 类型**（而非计划的 `AgentBrief`+`agentBriewView` 两份）：`WorkerStatus` 导出且由 serve 包构造 `AgentCaps`，元素类型必须导出，故统一用一个带 JSON tag 的导出类型。
- **`capabilities.{projects,agent_caps}` 统一挂 local + worker 两类 runner 行**（peer-http 不带），P5 一个字段跨类型读级联。

**P5 契约（数据已就绪）**：
- cascade 主源仍 `/v1/meta`：local runner → 交集全局 `projects` + `agents[]`(现带 `type`/`interactive`)；worker runner → 该 `workers[]` 条目的 `projects` + `agent_caps`(typed)。
- `/v1/meta`: `agents[]{key,type,interactive?}`、`workers[]{...,agent_caps?[{key,type?,interactive?}],connected}`。
- `/v1/runners`: 每行可选 `capabilities{projects?,agent_caps?}`（local 合成 + 在线 worker）；worker 行 `worker.{os,arch,gofer_version,started_at,agent_caps}` 节点信息。
- 内置 `exec` 恒在 local 集合、`type:"exec"`。`interactive` omitempty（缺省即 false）。离线 worker 不带 caps。所有新字段加法式、可选。
