# dev-agent-bridge 架构总览与术语

> **先读这篇看清全貌**。本文是概念地图 + 术语表，把分散在各设计文档里的机制串成一张图；**机制细节不在此重复**，按链接跳转（SR1130/SR1131）。
> 详见：主设计 [`2026-06-16-dev-agent-bridge-design.md`](./2026-06-16-dev-agent-bridge-design.md)、Web 控制台 [`2026-06-16-web-console-design.md`](./2026-06-16-web-console-design.md)、WS 远端 Worker [`2026-06-17-ws-remote-worker-design.md`](./2026-06-17-ws-remote-worker-design.md)。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-17 | Claude | 初版：两条正交轴（接入入口 / 执行位置）、术语表、共同机制（镜像 + 对端再校验）、实现状态矩阵。 |

## 2. 核心心智模型：两条正交的轴

dev-agent-bridge 的一切围绕一个异步 **job**（`{项目, agent, prompt/命令, cwd}` → 状态/退出码/日志/结果）。理解整体只需抓住**两条互相独立的轴**：

- **接入入口轴 —— 别人怎么跟 bridge 对话**（提交/查询 job 的入口）
- **执行位置轴 —— job 实际在哪台机器上跑**（由 job 的 `runner` 决定）

两轴**正交**：任一入口都可触发任一执行位置。例：一个 MCP agent 经 **MCP 入口** 提交一个 **在 worker 上执行** 的 job。

```txt
接入入口轴：  [MCP agent] ───MCP(stdio)──┐
              [Web 控制台] ──HTTP/SSE────┤
              [CLI: job 子命令] ─HTTP────┤
              [curl/外部] ───HTTP /v1────┤
                                         ▼
                                 agent-bridge (serve)
                                 job.Service │ 按 runner 选执行位置
              ┌──────────────────────────┼──────────────────────────┐
执行位置轴：  local(本进程)        peer-http(对等 peer)        worker(ws 远端机)
              本机执行            HTTP 转发(server→peer)       WS 派发(worker→server 拨出)
```

> 记住：`local / peer-http / worker` 是 **runner 名**（执行位置轴）；`HTTP / Web / CLI / MCP` 是 **入口**（接入轴）。两者不在一个维度，别混。

> 还有一个更细的相关维度 —— **agent type（怎么调用目标能力）**：`cli-agent`（跑 CLI）/ `exec`（跑裸命令）/ 未来 `mcp-agent`（调外部 MCP server 的 tool）。它与 runner（在哪跑）**正交**：同一个 agent type 可在任意 runner 上执行。下文 `mcp-agent`（原拟称 "mcp-client runner"）即属此维度，不是 runner。

## 3. 术语表

| 术语 | 含义 | 所在轴 |
|---|---|---|
| **serve / server** | 跑 `agent-bridge serve` 的节点：持控制面（HTTP/Web/MCP）+ `job.Service` + WorkerHub | 中枢 |
| **runner** | job 的执行位置抽象（`local`/`peer-http`/`worker`），对入口透明 | 执行位置 |
| **local** | 在 bridge 本进程执行 | 执行位置 |
| **peer** | peer-http 里**对等的另一台 `serve` 节点**——它本身就是 server，"peer" 强调**平级**（两端同类，仅某 job 一端转发一端执行） | 执行位置 |
| **worker** | ws-worker 里**精简的远端执行体**：拨出连入 server、无 HTTP 面、纯执行。与 peer 是**主从**而非对等 | 执行位置 |
| **Hub / WorkerRegistry** | server 侧维护已连入 worker（worker_id→conn）的组件 | 执行位置 |
| **dispatch / forward** | 把 job 交给远端：peer-http 叫 forward（HTTP 转发），ws 叫 dispatch（WS 派发） | 执行位置 |
| **mirror（镜像）** | 远端的日志/结果落地到 **server 自己的 `result_dir`**，作为 server 侧读取真源（见 §5） | 执行位置 |
| **HTTP `/v1`** | 控制面 REST-ish 入口（curl/CLI/Web 都走它） | 接入 |
| **Web 控制台** | `serve` 内嵌的浏览器界面（看板/实时日志/取消/交互） | 接入 |
| **CLI（`job` 子命令）** | `agent-bridge job ...`，是 HTTP `/v1` 的薄封装 | 接入 |
| **MCP Server** | `agent-bridge mcp`，stdio MCP server，把 bridge 暴露成 8 个 `bridge_*` tool 给 MCP agent 用 | 接入 |
| **mcp-agent**（原拟称 mcp-client runner）| **未做**：bridge 反过来当 MCP **客户端**，把 job 翻成对一个"本身是 MCP server 的能力"的 **tool 调用**。是**协议适配**，宜建模为 **agent type**（非 runner），与 worker **正交、可组合** | agent 类型(规划) |

### 关键区分：peer vs worker
| | **peer**（peer-http） | **worker**（ws-worker） |
|---|---|---|
| 关系 | 对等（两台都是完整 serve） | 主从（server 中枢 / worker 执行体） |
| 有 HTTP 面 | 有 | 无（纯出站） |
| 自知身份 | 不自知（只是普通 server 收提交） | 明确知道连入了 server |

### 关键区分：MCP 的两个方向
- **`agent-bridge mcp`（已做）**：bridge = MCP **server**，agent 调 bridge（接入入口）。
- **`mcp-agent`（未做，原拟称 mcp-client runner）**：bridge = MCP **client**，把 job 翻成对外部 MCP server 的 **tool 调用**。宜建模为 **agent type**（"怎么调"），与 runner（"在哪跑"）正交——可在 `local`/`worker` 任意 runner 上执行；与 worker 是**组合**关系（worker 管到哪台机、mcp-agent 管用什么协议调目标），互不替代。

## 4. 接入入口（接入轴）

| 入口 | 形态 | 给谁用 | 状态 |
|---|---|---|---|
| HTTP `/v1` | REST-ish + Bearer | 容器/主机/外部 curl | ✅ |
| Web 控制台 | 内嵌 SPA，根路径，SSE 实时 | 开发者浏览器 | ✅（含 P9 交互卡 web-P2） |
| CLI `job` | HTTP 薄封装 | 命令行 | ✅ |
| MCP Server `agent-bridge mcp` | stdio，8 个 `bridge_*` tool | MCP agent（Claude Code/Codex/...） | ✅（P8） |

> 入口只决定"怎么提交/查询"；提交后的执行由 `runner` 决定（§5）。

## 5. 执行位置（runner 轴）

| runner | 连接方向 | 谁需可入站 | NAT 友好 | 对端形态 | 状态 |
|---|---|---|---|---|---|
| `local` | 进程内 | — | — | 本机 | ✅ |
| `peer-http` | **server → peer**（HTTP） | **peer 需暴露 HTTP** | ✗ | 完整 serve 节点 | ✅（P7） |
| `worker` | **worker → server**（WS 拨出，持久多路复用） | **无人**（worker 纯出站） | ✓ | 精简执行体，无 HTTP 面 | 📐 设计完成，未实现 |

### 共同机制一：镜像（"server 怎么读远端输出"）
`local`/`peer-http`/`worker` **共享同一个巧思**：远端日志都经 runner 的 `Stdout/Stderr` 写进 **server 自己的 `<result_dir>/<job_id>/*.log`**——于是 server 的 HTTP 拉日志 / SSE 流 / Web / MCP `bridge_tail_log` **读路径全部不变**。

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
| `worker` | ✅ 设计内置双向跨线（worker 推 open → server 注入 → 用户答 → 回发 answer，见 ws 设计 §8.3） |
| `peer-http` | ⚠️ **当前不透传**（只镜像 log/status，不处理 peer 的 `interaction` 帧）→ 见 [`../TODO.md`](../TODO.md)「peer-http 补 P9 交互透传」 |

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
| 接入：HTTP `/v1` / CLI / Web 控制台 / MCP Server（8 tool） | ✅ |
| 执行：`local` / `peer-http`（P7） | ✅ |
| 执行：`worker`（ws 远端机） | 📐 设计完成，待实施（WP1→WP4） |
| P9 运行中交互：local / Web(web-P2) / MCP | ✅ |
| P9 交互：worker 跨线 | 📐 设计内（WP2） |
| P9 交互：peer-http 透传 | 🔲 TODO |
| `mcp-agent`（bridge 当 MCP 客户端；原拟称 mcp-client runner，宜作 agent type） | 🔲 规划，未设计 |
| P10 Cutover（codex-bridge 退役） | 📋 runbook 就绪，待主机侧执行 |
