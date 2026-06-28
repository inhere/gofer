# 多 agent 协作 实施计划（总纲）

> design：[`../design/2026-06-28-multi-agent-collab-design.md`](../design/2026-06-28-multi-agent-collab-design.md)（v0.2，含对抗复审 7 处收紧）
> bd：epic `hyy-ai-inspect-hyxz` · P1=`y2jg`(E36) · P2=`fl46`(E35) · P3=`axma`(E25)
> 本文只留**总纲 + 进度 + 跨文档引用**；各阶段细节见子 plan（SR1105）。

## 1. 范围与节奏

在已通的 **L1 通道（E28 ✅）** 上叠加三层，每 P 自带 jobstore/Service/httpapi/client/mcp/CLI 一条龙、独立可验：

| 阶段 | 层 | 子 plan | bd | 依赖 |
|---|---|---|---|---|
| **P1** | L2 身份/信箱(E36) | [`multi-agent-collab/P1-presence-mailbox-plan.md`](multi-agent-collab/P1-presence-mailbox-plan.md) | y2jg | E28✅ |
| **P2** | L3 角色(E35) | [`multi-agent-collab/P2-roles-plan.md`](multi-agent-collab/P2-roles-plan.md) | fl46 | 独立（与 P1 并行可） |
| **P3** | L4 监督应答(E25) | [`multi-agent-collab/P3-supervisor-answerer-plan.md`](multi-agent-collab/P3-supervisor-answerer-plan.md) | axma | P1（升级走信箱） |

> P2 不依赖 P1，可与 P1 并行；P3 的"升级人"经 P1 的信箱投递，故排 P1 之后。建议顺序 **P1 → P2 → P3**（design §13）。

## 2. 全局铁律（贯穿三阶段）

- **G023 零行为变化抽取**：搬迁/抽包逐字、每步 `go test ./...` 绿背书。
- **G022 依赖单向**：新包 `internal/presence`、`internal/supervisor` → `job`/`jobstore`（接口/DAO），**绝不反向**；`mcpserver`→`client` 无环。
- **SR1202**：每个子步骤（下表 checkbox 粒度）完成即 `go test ./...` + 1 次 git commit，不攒大堆。
- **additive 迁移**：新表进 `jobstore` `schemaStmts`（`CREATE TABLE IF NOT EXISTS`，照 `store.go:59-171`），新列走 `migrate()` `add()`（照 `store.go:236-316`）；旧库幂等。
- **provenance/审计/配额复用**：E34 channel/client、E13 事件、E17 配额沿用既有钩子，不另起。
- **部署**：纯 Go（无前端）→ `go build`；容器 `worker stop→装→worker -d`；host 用户自建（[[gofer-build-install]]）。Web 显示 presence/inbox 不在本 epic（留 Web v2 写层）。

## 3. 进度跟踪

### P1 L2 身份/信箱（E36）✅ 完成（2026-06-28）
- [x] P1.1 jobstore：`agent_presence`+`messages` 两表 + DAO（presence.go/messages.go）+ 单测 — `05492dc`
- [x] P1.2 `internal/presence` Service：register/poll/post/listPresence + role 投递 fan-out + TTL 懒判定 + prune + 单测 — `ba458f4`
- [x] P1.3 httpapi：5 端点（register/presence/inbox poll/messages/deregister）+ handler + agent_token 校验 + 单测 — `aab40fc`
- [x] P1.4 client 5 方法 + Backend 接口扩 4 方法（两实现）+ mcp 4 工具 + core/ServeLocal 线缆 — `92ab5d7`
- [x] P1.5 CLI `gofer presence`（ls/send/inbox，非 `gofer agent`，见下注）+ serve prune sweeper + 双 agent mcp 工具 E2E — `ceaa475`/`b73f281`

> **P1.5 自动决策（SR1401 二级修正）**：原计划 CLI 名 `gofer agent ls`，但 `gofer agent` 已存在（= job-agent 定义 claude/codex/exec 的检视命令），且其已有 `list` 子命令。复用会**混淆 driver/job 二分 + 子命令冲突**。故新建顶层 `gofer presence`（别名 `driver`）承载 presence(driver agent)，与 `gofer agent`(job agent) 分离。inbox 的 agent_token 标志用 `--agent-token`（避与 bearer `--token` 冲突）。
> **E2E 形式**：用进程内双 client + 真实 `bridge_*` 工具(SDK transport) 做确定性等价 E2E（`TestPresenceToolsE2E`），并在容器内起真 serve 用 curl + `gofer presence` CLI 冒烟（register/list 不漏 token/post/poll 消费/403/role fan-out/prune sweeper 全过）。host serve 重建 + 容器二进制换装见“部署”待办。

### P2 L3 角色（E35）
- [ ] P2.1 config：`RoleConfig`+`Roles` 段 + `AgentConfig.SystemInject` + claude/codex 内置 system_inject 默认
- [ ] P2.2 agent：`Vars.SystemPrompt` + `replacements` + 单测
- [ ] P2.3 job：`JobRequest.Role/SystemPrompt` + submit role 解析 + system_inject 追加（类比 SessionInject）+ **resume 重施 system_inject** + 单测
- [ ] P2.4 CLI `job run --role` + mcp `bridge_run_job` role 字段 + 单测 + 冒烟

### P3 L4 监督应答（E25）
- [ ] P3.1 jobstore：`ListPendingInteractions`（JOIN jobs 过滤终态）+ 单测
- [ ] P3.2 job：finish/cancel **对账残留 pending→cancelled**（修既有缺口，`InteractionCancelled` 赋值）+ 启动 sweeper 兜底 + 单测
- [ ] P3.3 `internal/supervisor`：分层 answerer（白名单 choice+options 自动答 / confirmation+question 升级）+ poller + 升级经信箱 + 审计/配额钩子 + 单测
- [ ] P3.4 httpapi `GET /v1/interactions?status=pending` + client 方法 + mcp `bridge_list_pending_interactions`
- [ ] P3.5 config `supervisor:` 段 + serve 线缆 + 部署 + E2E（choice 自动答 / confirmation 升级 inbox）

## 4. 实施模式（SUPMODE 建议）

有本计划即可进监督模式（SR1430.1）。前置检查：容器 worker 在线、host serve 可达、`go test ./...` 基线绿。每 P 末做真机冒烟/E2E（多 agent 协作语义靠双 mcp client 验，参考 E28 plan E2E 手法）。

## 5. 阶段实施结果（完成后回填）

| 阶段 | commit | 验收 | 备注 |
|---|---|---|---|
| P1 | 05492dc→b73f281(6 提交) | go test 27 包绿 + go vet 净 + 无 import 环；容器真 serve curl/CLI E2E 全过 | CLI 改 `gofer presence`(非 agent)；mcp 工具 14 个(10+4)；待部署(host serve 重建+容器二进制换装) |
| P2 | | | |
| P3 | | | |
