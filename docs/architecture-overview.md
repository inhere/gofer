# dev-gofer 架构总览与术语

> **先读这篇看清全貌**。本文是概念地图 + 术语表，把分散在各设计文档里的机制串成一张图；**机制细节不在此重复**，按链接跳转（SR1130/SR1131）。
> 详见：主设计 [`2026-06-16-dev-agent-bridge-design.md`](./2026-06-16-dev-agent-bridge-design.md)、Web 控制台 [`2026-06-16-web-console-design.md`](./2026-06-16-web-console-design.md)、WS 远端 Worker [`2026-06-17-ws-remote-worker-design.md`](./2026-06-17-ws-remote-worker-design.md)。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-17 | Claude | 初版：两条正交轴（接入入口 / 执行位置）、术语表、共同机制（镜像 + 对端再校验）、实现状态矩阵。 |
| v0.2 | 2026-06-17 | Claude | `mcp-client runner` → `mcp-agent` 改名并归入 **agent type** 维度；术语表/MCP 两个方向/状态矩阵同步（commit `7c7b470`）。 |
| v0.3 | 2026-06-18 | Claude | peer-http P9 交互透传落地，§6 + 状态矩阵更新为 ✅（commit `dcbcadc`/`1d8ed80`）。 |
| v0.4 | 2026-06-18 | Claude | 新增 §9「扩展点与已知约束」（整体架构回看）。 |
| v0.5 | 2026-06-18 | Claude | C1 方向定为 SQLite（modernc 纯 Go），§9.1 C1 行链接 [`2026-06-18-sqlite-store-design.md`](./2026-06-18-sqlite-store-design.md)。 |
| v0.6 | 2026-06-18 | Claude | C1 **已解决**：SQLite store SP1–SP5 全落地（内存仅留 live、DB 索引列表、retention prune），§9.1 C1 行标记完成。 |
| v0.7 | 2026-06-18 | Claude | **项目改名 dev-gofer → Gofer**：module `github.com/inhere/gofer`、binary/CLI/env(`GOFER_`)/运行时(`~/.config/gofer`/`gofer.db`/结果子目录 `gofer`) 已更名（commit `dcba644`），**根目录亦改名 `tools/gofer`**。本文及其它历史 design/plan 文档仍按旧名 dev-gofer 行文（记录 codex-bridge→dev-gofer 迁移史实，历史快照未回改）。 |
| v0.8 | 2026-06-19 | Claude | **C2–C5 已解决**：加固 Phase A/B/C 全落地并独立验收（多 caller 身份 + 配置热加载 + 日志流控 + 提交幂等，9 commit `8200654`..`79906eb`），§9.1 对应四行标记完成。**C6/C7 仍待**（属 ws-worker 工作流）。详见 [`../plans/2026-06-18-hardening-c2-c5-plan.md`](../plans/2026-06-18-hardening-c2-c5-plan.md)。 |
| v0.9 | 2026-06-19 | Claude | **ws-worker（WP1–WP3）+ C6 落地，C7 最小版**：远端 worker 端到端执行 + 运行中交互透传 + 心跳/重连/worker-lost/多 worker 弹性全部实现并逐阶段独立验收；C6 `/v1/runners` 可观测（worker 心跳态 + peer-http 主动探针）已解决；C7 经 worker 多地址退避重连最小缓解（多 hub HA 仍 out-of-scope）。§5/§6/§8 worker 行 + §9.1 C6/C7 行同步更新。WP4（标签自动调度 + Web Workers 仪表盘）缓做。详见 [`../plans/2026-06-19-ws-worker-c6c7/`](../plans/2026-06-19-ws-worker-c6c7/)。 |
| v0.10 | 2026-06-19 | Claude | **WP4 Workers 仪表盘前端落地**（commit `84655cf`）：Web 控制台新增 `/runners` 视图，按真实运行器分类（Workers/Peers/Local）分组名册，签名元素「心跳脉冲」(`Heartbeat.vue` 扩展 `Signal` 波形语言，connected 脉冲/stale 减速/断连 flatline)，4s 轮询 + 1s 本地 tick 实时年龄，消费 C6 `/v1/runners`。§5/§8 worker 行、§9.1 C6 行、§9.2 扩展点同步。**WP4 标签自动调度仍缓做**。容器内未做真机视觉确认（typecheck/build/embed 绿）。 |
| v0.11 | 2026-06-29 | inhere | 新增 **§10「多 agent 协作维度」**：`driver agent` vs `job agent` 核心区分、`presence` 在线名册、interaction→**分层升级路由(L0–L3)**；术语表补对应词。细节链 [`multi-agent-collab`](./design/2026-06-28-multi-agent-collab-design.md) + [`supervisor-routing`](./design/2026-06-29-supervisor-routing-design.md)。 |

## 2. 核心心智模型：两条正交的轴

dev-gofer 的一切围绕一个异步 **job**（`{项目, agent, prompt/命令, cwd}` → 状态/退出码/日志/结果）。理解整体只需抓住**两条互相独立的轴**：

- **接入入口轴 —— 别人怎么跟 bridge 对话**（提交/查询 job 的入口）
- **执行位置轴 —— job 实际在哪台机器上跑**（由 job 的 `runner` 决定）

两轴**正交**：任一入口都可触发任一执行位置。例：一个 MCP agent 经 **MCP 入口** 提交一个 **在 worker 上执行** 的 job。

```txt
接入入口轴：  [MCP agent] ───MCP(stdio)──┐
              [Web 控制台] ──HTTP/SSE────┤
              [CLI: job 子命令] ─HTTP────┤
              [curl/外部] ───HTTP /v1────┤
                                         ▼
                                 gofer (serve)
                                 job.Service │ 按 runner 选执行位置
              ┌──────────────────────────┼──────────────────────────┐
执行位置轴：  local(本进程)        peer-http(对等 peer)        worker(ws 远端机)
              本机执行            HTTP 转发(server→peer)       WS 派发(worker→server 拨出)
```

> 记住：`local / peer-http / worker` 是 **runner 名**（执行位置轴）；`HTTP / Web / CLI / MCP` 是 **入口**（接入轴）。两者不在一个维度，别混。

> 还有一个更细的相关维度 —— **agent type（怎么调用目标能力）**：`cli-agent`（跑 CLI）/ `exec`（跑裸命令）/ 未来 `mcp-agent`（调外部 MCP server 的 tool）。它与 runner（在哪跑）**正交**：同一个 agent type 可在任意 runner 上执行。下文 `mcp-agent`（原拟称 "mcp-client runner"）即属此维度，不是 runner。

> 以上是**单 job 视角**（一个 job 怎么提交、在哪跑）。当**多个长在线 agent 互相协作**（派活 / 提问 / 监督应答）时，引入**第三个维度——协作主体 `driver agent`**，见 **§10**。

## 3. 术语表

| 术语 | 含义 | 所在轴 |
|---|---|---|
| **serve / server** | 跑 `gofer serve` 的节点：持控制面（HTTP/Web/MCP）+ `job.Service` + WorkerHub | 中枢 |
| **runner** | job 的执行位置抽象（`local`/`peer-http`/`worker`），对入口透明 | 执行位置 |
| **local** | 在 bridge 本进程执行 | 执行位置 |
| **peer** | peer-http 里**对等的另一台 `serve` 节点**——它本身就是 server，"peer" 强调**平级**（两端同类，仅某 job 一端转发一端执行） | 执行位置 |
| **worker** | ws-worker 里**精简的远端执行体**：拨出连入 server、无 HTTP 面、纯执行。与 peer 是**主从**而非对等 | 执行位置 |
| **Hub / WorkerRegistry** | server 侧维护已连入 worker（worker_id→conn）的组件 | 执行位置 |
| **dispatch / forward** | 把 job 交给远端：peer-http 叫 forward（HTTP 转发），ws 叫 dispatch（WS 派发） | 执行位置 |
| **mirror（镜像）** | 远端的日志/结果落地到 **server 自己的 `result_dir`**，作为 server 侧读取真源（见 §5） | 执行位置 |
| **HTTP `/v1`** | 控制面 REST-ish 入口（curl/CLI/Web 都走它） | 接入 |
| **Web 控制台** | `serve` 内嵌的浏览器界面（看板/实时日志/取消/交互） | 接入 |
| **CLI（`job` 子命令）** | `gofer job ...`，是 HTTP `/v1` 的薄封装 | 接入 |
| **MCP Server** | `gofer mcp`，stdio MCP server，把 gofer 暴露成 15 个 `gofer_*` tool 给 MCP agent 用 | 接入 |
| **mcp-agent**（原拟称 mcp-client runner）| **未做**：bridge 反过来当 MCP **客户端**，把 job 翻成对一个"本身是 MCP server 的能力"的 **tool 调用**。是**协议适配**，宜建模为 **agent type**（非 runner），与 worker **正交、可组合** | agent 类型(规划) |
| **driver agent** | **长在线、用 MCP 主动驱动 gofer 的协作主体**（你的 Claude Code 会话、常驻 supervisor）。登记在 `agent_presence`，经 `agent_id` 寻址。详见 §10 | 协作主体 |
| **job agent** | 被派去跑**一次性 job** 的执行体（`gofer job run` 拉起的 claude/codex），执行完即终；不进 presence，经 `job_id` 标识。详见 §10 | 执行(被驱动) |
| **presence** | **在线 driver agent 名册**（`agent_presence` 表）：90s 心跳 TTL 判 online/offline，使 `messages` 可寻址（`role:`/`agent_id`）。详见 §10 | 协作主体 |
| **interaction / escalation** | job 执行中产生的待答提问（`interactions` 表）/ 答不了时经 `messages` 信箱升级给上层应答者的消息。监督的**分层升级路由**见 §10.3 | 协作 |

### 关键区分：peer vs worker
| | **peer**（peer-http） | **worker**（ws-worker） |
|---|---|---|
| 关系 | 对等（两台都是完整 serve） | 主从（server 中枢 / worker 执行体） |
| 有 HTTP 面 | 有 | 无（纯出站） |
| 自知身份 | 不自知（只是普通 server 收提交） | 明确知道连入了 server |

### 关键区分：MCP 的两个方向
- **`gofer mcp`（已做）**：bridge = MCP **server**，agent 调 bridge（接入入口）。
- **`mcp-agent`（未做，原拟称 mcp-client runner）**：bridge = MCP **client**，把 job 翻成对外部 MCP server 的 **tool 调用**。宜建模为 **agent type**（"怎么调"），与 runner（"在哪跑"）正交——可在 `local`/`worker` 任意 runner 上执行；与 worker 是**组合**关系（worker 管到哪台机、mcp-agent 管用什么协议调目标），互不替代。

## 4. 接入入口（接入轴）

| 入口 | 形态 | 给谁用 | 状态 |
|---|---|---|---|
| HTTP `/v1` | REST-ish + Bearer | 容器/主机/外部 curl | ✅ |
| Web 控制台 | 内嵌 SPA，根路径，SSE 实时 | 开发者浏览器 | ✅（含 P9 交互卡 web-P2） |
| CLI `job` | HTTP 薄封装 | 命令行 | ✅ |
| MCP Server `gofer mcp` | stdio，15 个 `gofer_*` tool | MCP agent（Claude Code/Codex/...） | ✅（P8） |

> 入口只决定"怎么提交/查询"；提交后的执行由 `runner` 决定（§5）。

## 5. 执行位置（runner 轴）

| runner | 连接方向 | 谁需可入站 | NAT 友好 | 对端形态 | 状态 |
|---|---|---|---|---|---|
| `local` | 进程内 | — | — | 本机 | ✅ |
| `peer-http` | **server → peer**（HTTP） | **peer 需暴露 HTTP** | ✗ | 完整 serve 节点 | ✅（P7） |
| `worker` | **worker → server**（WS 拨出，持久多路复用） | **无人**（worker 纯出站） | ✓ | 精简执行体，无 HTTP 面 | ✅（WP1–WP3 + WP4 Workers 仪表盘；WP4 标签自动调度缓做）|

### 共同机制一：镜像（"server 怎么读远端输出"）
`local`/`peer-http`/`worker` **共享同一个巧思**：远端日志都经 runner 的 `Stdout/Stderr` 写进 **server 自己的 `<result_dir>/<job_id>/*.log`**——于是 server 的 HTTP 拉日志 / SSE 流 / Web / MCP `gofer_tail_log` **读路径全部不变**。

- `peer-http`：server **主动拉** peer 的 SSE，把 `log` 帧写进本地文件。
- `worker`：worker **主动推** `log` 帧，server hub 写进本地文件。
- 两者都末尾取**权威终态**（peer-http `GetJob` / ws `result` 帧）。

> 远端侧也各留一份自己的记录（peer/worker 本机 `result_dir`），但 **server 镜像是 server 侧用户查询的真源**，两份独立、互不耦合。

### 共同机制二：执行授权留在对端（安全不变量）
转发/派发来的 job 一律走 **对端自己的** `job.Service.validate`（项目/agent allowlist + exec 门禁 + `SafeJoin` cwd 越界校验）。**中枢节点无法逼对端跑未放行的 agent 或逃出项目目录**——执行授权始终在执行方本地。

## 6. P9 运行中交互在各执行位置的状态

| runner | 交互（P9）是否透传到 server 侧入口 |
|---|---|
| `local` | ✅ 原生 |
| `peer-http` | ✅ **透传**（host 把 peer SSE 的 `interaction` 帧注入自己的 job 记录 → host 转 pending_interaction；host 侧作答经 `client.AnswerInteraction` POST 回 peer 续跑。commit `1d8ed80`） |
| `worker` | ✅ **跨线透传**（worker 推 `interaction{open}` → server 注入 host job → 用户答 → 回发 `answer` 帧续跑，WP2 commit `5adefc2`/`f727995`） |

## 7. 关联文档

- 主设计（CLI/配置/Job 模型/`/v1` API/store/P9 协议/安全）：[`2026-06-16-dev-agent-bridge-design.md`](./2026-06-16-dev-agent-bridge-design.md)
- Web 控制台（监控/实时日志/取消/交互卡）：[`2026-06-16-web-console-design.md`](./2026-06-16-web-console-design.md)
- WS 远端 Worker：[`2026-06-17-ws-remote-worker-design.md`](./2026-06-17-ws-remote-worker-design.md)
- 实施计划：[`../plans/2026-06-16-dev-agent-bridge-plan.md`](../plans/2026-06-16-dev-agent-bridge-plan.md) / [`../plans/2026-06-16-web-console-plan.md`](../plans/2026-06-16-web-console-plan.md)
- 上线切换：[`../2026-06-17-p10-cutover-runbook.md`](../2026-06-17-p10-cutover-runbook.md)
- 待办：[`../TODO.md`](../TODO.md)

## 8. 实现状态矩阵（截至 2026-06-17）

| 能力 | 状态 |
|---|---|
| 接入：HTTP `/v1` / CLI / Web 控制台 / MCP Server（15 tool） | ✅ |
| 执行：`local` / `peer-http`（P7） | ✅ |
| 执行：`worker`（ws 远端机） | ✅（WP1–WP3：端到端+交互+弹性/C7 + WP4 Workers 仪表盘；WP4 标签自动调度缓做） |
| P9 运行中交互：local / Web(web-P2) / MCP | ✅ |
| P9 交互：peer-http 透传 | ✅（`1d8ed80`） |
| P9 交互：worker 跨线 | ✅（WP2 `5adefc2`/`f727995`） |
| `mcp-agent`（bridge 当 MCP 客户端；原拟称 mcp-client runner，宜作 agent type） | 🔲 规划，未设计 |
| P10 Cutover（codex-bridge 退役） | 📋 runbook 就绪，待主机侧执行 |

## 9. 扩展点与已知约束（架构回看，2026-06-18）

> 结论：整体架构**自洽、无结构性缺陷**。runner 抽象 + "镜像进 result_dir" 让远端执行/交互对所有读路径透明，对端再校验给出清晰安全边界，两条正交轴心智简单。下面是回看时识别的**已知约束**（长跑/多用户场景才显）与**扩展点**，均非阻塞，按需取舍。

### 9.1 已知约束 / 待加固

| # | 约束 | 影响 | 建议 |
|---|---|---|---|
| ~~C1~~ ✅ | ~~内存 job 表 + `jobs.jsonl` 无界增长~~ **已解决** | — | **SQLite（modernc 纯 Go）元数据/索引/交互 store 落地（SP1–SP5 全完成）**：内存仅留 live job（终态驱逐）、列表为 DB 索引查询、`retention` 保留策略周期 prune 清磁盘；日志仍文件。详见 [`2026-06-18-sqlite-store-design.md`](./2026-06-18-sqlite-store-design.md) |
| ~~C2~~ ✅ | ~~单一 bearer token，无调用方身份/吊销粒度~~ **已解决** | — | **多 caller token（id + token/token_env）+ `crypto/subtle` 常时间比对 + caller_id 入 job/库（handler 防伪覆盖）**；吊销 = 改配置 + SIGHUP 热加载（见 C3）。详见 [`2026-06-18-hardening-c2-c5-plan.md`](../plans/2026-06-18-hardening-c2-c5-plan.md) §4（commit `8200654`/`61fd149`/`07065ce`） |
| ~~C3~~ ✅ | ~~配置无热加载~~ **已解决** | — | **SIGHUP 热重载**：重新 `config.Load` + `atomic.Pointer` 原子替换 registry/service cfg，失败安全保留旧配置（含被删配置文件守卫）。限制：runner 实例不随 reload 重建、retention 启停门/间隔冻结在启动（需重启）。详见计划 §5（commit `17dbfb8`/`289d138`） |
| ~~C4~~ ✅ | ~~日志无流控~~ **已解决** | — | **日志轮转（LogWriter 超阈值滚 `.1`）+ SSE 帧 cap/分片(seq 连续)/动态节流/rotated 标记 + 前端 buffer 窗口（丢最旧）**。详见计划 §6（commit `7fa6080`/`614db2b`/`f7e5f47`） |
| ~~C5~~ ✅ | ~~提交无幂等键~~ **已解决** | — | **`request_id` 幂等**：jobs 部分唯一索引 + Submit 前置查重命中复用 + 并发唯一冲突回退返先到者（清理自建空目录）。详见计划 §4（commit `8200654`/`07065ce`） |
| ~~C6~~ ✅ | ~~远端节点可观测性弱：peer-http 在 peer 宕机前 host 不知情~~ **已解决** | — | **`GET /v1/runners`**（authed list-style）：local 恒 up；peer-http 周期主动 `GET /health` 探针缓存（up/down/unknown，30s 间隔/5s 超时，serve goroutine 镜像 prune-loop 干净停机、只读缓存不阻塞请求）；worker 行读 hub 心跳快照（connected/disconnected/unknown + heartbeat-age/in-flight/labels）。**Web Workers 仪表盘前端已落地**（`/runners` 视图：舰队名册分组 + 心跳脉冲签名元素 + 实时年龄轮询，commit `84655cf`）。详见 [`../plans/2026-06-19-ws-worker-c6c7/P4-c6-observability-plan.md`](../plans/2026-06-19-ws-worker-c6c7/P4-c6-observability-plan.md)（commit `ae6a630`/`3c28aa9`/`6da44cd`/`0db128b`） |
| ~~C7~~ 🟡 | ~~单 hub 无 HA：server 重启期 worker 失联~~ **最小版已解决** | — | **worker 端多 hub 地址 + 全抖动退避重连**（`server_link.urls` 轮换、初始 1s→max 30s、注册成功即重置）：hub 重启=短暂中断而非永久失联，所述影响已缓解。**仍显式 out-of-scope（本轮不做）**：多 hub 共享注册表 / 跨 hub job 接管 / 选主。详见 [WP3 计划](../plans/2026-06-19-ws-worker-c6c7/P3-wp3-resilience-c7-plan.md)（commit `a43a694`）|

### 9.2 扩展点

- **`mcp-agent`（agent type）**：让 job 调用"本身是 MCP server"的外部能力（见 §3 备注），与 runner 正交、可与 worker 组合。
- **Web 控制台适配新架构**：① 看板/详情展示 `runner`（及未来 `worker_id`）让"在哪执行"可见；② **Workers 仪表盘已落地**（`/runners` 视图：Workers/Peers/Local 名册 + 心跳脉冲 + 在飞/标签 + 实时年龄，commit `84655cf`）；③（更大）控制台内**提交表单**（选 项目/agent/runner/worker_id）仍待。详见 [`../TODO.md`](../TODO.md)。
- **产物回取**：job 除日志外产生的文件（构建产物等）经控制台/接口下载。
- **job 标签 / 元数据 + 搜索过滤**：看板按标签/项目/agent/时间检索。
- **调度策略**：超出"每项目并发"的优先级/跨 worker 公平调度。
- **远端交互对齐**：peer-http 透传已做（§6）；worker 跨线随 WP2 落地，保持三 runner 交互体验一致。

> 注：peer-http 远端 job 的交互/日志因"镜像"机制**已透明地呈现在现有控制台**（无需改前端）——这正是架构自洽的体现；上面的控制台扩展是"让远端执行位置**可见**"，非"让它**能用**"。

## 10. 多 agent 协作维度：driver agent / presence / 监督路由（2026-06-29）

> **第三个维度**。§2 两条轴是"单个 job 怎么提交、在哪跑"；本节是"**多个长在线 agent 如何互相协作**"——对应 E28 通道 + E36 身份信箱 + E35 角色 + E25 监督应答。机制细节不在此重复，详见 [`2026-06-28-multi-agent-collab-design.md`](./design/2026-06-28-multi-agent-collab-design.md) 与 [`2026-06-29-supervisor-routing-design.md`](./design/2026-06-29-supervisor-routing-design.md)。

### 10.1 driver agent vs job agent（最易混的核心区分）

两者都是"agent"，但**角色相反**：driver **主动驱动** gofer，job agent **被 gofer 驱动**去执行。

| | **job agent** | **driver agent** |
|---|---|---|
| 是什么 | 被派去跑**一次性 job** 的执行体 | **长在线**、主动驱动 gofer 的协作主体 |
| 生命周期 | 单次执行，完成即终 | 长会话，持续在线（靠心跳维持） |
| 谁发起 | 被 driver / CLI / web 提交而产生 | **自己 `gofer_register` 上线** |
| 典型例子 | `gofer job run` 拉起的 claude/codex 跑个任务 | 你这个 Claude Code 会话、常驻 supervisor agent |
| 登记在哪 | `jobs` 表（经 `job_id` 标识） | `agent_presence` 表（经 `agent_id` 寻址，名册可见） |
| 经哪个入口 | 由入口轴任一入口提交（§4） | **MCP 入口**（§3 的 "MCP agent" 即 driver agent 的典型形态） |
| 关键能力 | 执行；卡住时产生 **interaction 提问** | 提交 job、收发 `messages` 信箱、答别人的 interaction |

> 一个进程可**两栖**：一个 driver agent（如编排者）既在线收发消息，也能 `gofer_run_job` 把活派成 job agent 去跑。

### 10.2 presence（在线名册）

**presence = 在线 driver agent 的名册/在线状态登记簿**（术语源自 IM 系统，如 XMPP/Slack 的"在线状态"）。`agent_presence` 表为每个在线 driver 记 `agent_id`（公开寻址地址）/ `agent_token`（私有句柄，poll/deregister 校验）/ `role` / `last_seen_at`，靠 **90s 心跳 TTL** 判 online/offline。

它存在的**唯一目的是让消息可寻址**：`post_message(to="role:supervisor")` → presence 把它解析成当前在线的具体 `agent_id` → 投到该 agent 的 inbox（`messages` 表）。没有 presence，agent 之间无法互相发现。详见 collab design §9。

### 10.3 提问的分层升级路由（监督应答）

job agent 卡住产生 **interaction**（提问）时，"谁来答"按**上下文能力分层、答不了就向上兜底**：

```
L0 内置规则器(正则白名单)   上下文无关·低危 choice
L1 owner = 发起该 job 的 driver  有完整上下文 → 准确答（主路径）
L2 通用 sup（server 托管 driver）  上下文无关·中低危 LLM 兜底
L3 人(IM/web/CLI)            终极兜底
```

关键心智：**正确答案落在哪一层取决于问题是否依赖前因后果**——业务相关的提问只有 owner（持 plan 上下文的主 driver）能答对，通用 sup 只配答上下文无关的通用决策。完整模型、数据结构与安全闸见 [`2026-06-29-supervisor-routing-design.md`](./design/2026-06-29-supervisor-routing-design.md)。
