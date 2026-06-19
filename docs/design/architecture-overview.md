# dev-agent-bridge 架构总览与术语

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
| v0.7 | 2026-06-18 | Claude | **项目改名 dev-agent-bridge → Gofer**：module `github.com/inhere/gofer`、binary/CLI/env(`GOFER_`)/运行时(`~/.config/gofer`/`gofer.db`/结果子目录 `gofer`) 已更名（commit `dcba644`）。**项目根目录 `tools/dev-agent-bridge` 暂冻结不动**；本文及其它历史 design/plan 文档仍按旧名 dev-agent-bridge 行文（历史快照，未回改）。 |

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
| `peer-http` | ✅ **透传**（host 把 peer SSE 的 `interaction` 帧注入自己的 job 记录 → host 转 pending_interaction；host 侧作答经 `client.AnswerInteraction` POST 回 peer 续跑。commit `1d8ed80`） |
| `worker` | 📐 设计内置双向跨线（worker 推 open → server 注入 → 用户答 → 回发 answer，见 ws 设计 §8.3） |

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
| P9 交互：peer-http 透传 | ✅（`1d8ed80`） |
| P9 交互：worker 跨线 | 📐 设计内（WP2） |
| `mcp-agent`（bridge 当 MCP 客户端；原拟称 mcp-client runner，宜作 agent type） | 🔲 规划，未设计 |
| P10 Cutover（codex-bridge 退役） | 📋 runbook 就绪，待主机侧执行 |

## 9. 扩展点与已知约束（架构回看，2026-06-18）

> 结论：整体架构**自洽、无结构性缺陷**。runner 抽象 + "镜像进 result_dir" 让远端执行/交互对所有读路径透明，对端再校验给出清晰安全边界，两条正交轴心智简单。下面是回看时识别的**已知约束**（长跑/多用户场景才显）与**扩展点**，均非阻塞，按需取舍。

### 9.1 已知约束 / 待加固

| # | 约束 | 影响 | 建议 |
|---|---|---|---|
| ~~C1~~ ✅ | ~~内存 job 表 + `jobs.jsonl` 无界增长~~ **已解决** | — | **SQLite（modernc 纯 Go）元数据/索引/交互 store 落地（SP1–SP5 全完成）**：内存仅留 live job（终态驱逐）、列表为 DB 索引查询、`retention` 保留策略周期 prune 清磁盘；日志仍文件。详见 [`2026-06-18-sqlite-store-design.md`](./2026-06-18-sqlite-store-design.md) |
| C2 | **单一 bearer token，无调用方身份/吊销粒度** | 多用户/多 worker 下无审计、无法单点吊销 | per-worker / per-caller token（hash 存储，SR201 口径）+ 提交方标识入 job |
| C3 | **配置无热加载**：`serve` 启动时加载一次；`project add` 写文件但运行中实例不感知 | 加项目/agent 需重启 serve | SIGHUP/接口热重载 registry（worker 已是动态注册，不受此限） |
| C4 | **日志无流控**：SSE 每 tick 推全部新增字节；前端 stdout/stderr 字符串无界累积 | 超大/高频输出 job 撑大内存与帧 | 日志轮转 + 前端虚拟化日志视图 + SSE 端 cap |
| C5 | **提交无幂等键**：客户端重试会产生重复 job | 网络抖动下重复执行 | 可选 `request_id` 幂等（SR607 口径，Redis/内存窗口） |
| C6 | **远端节点可观测性弱**：peer-http 在 peer 宕机前 host 不知情 | 故障发现滞后 | `/v1/runners` 健康探针；worker 心跳态上报（ws 设计已含心跳） |
| C7 | **单 hub 无 HA**：ws-worker 的 server 是其连入 worker 的 SPOF | server 重启期 worker 失联 | 多 hub / worker 多地址重连（WP3+） |

### 9.2 扩展点

- **`mcp-agent`（agent type）**：让 job 调用"本身是 MCP server"的外部能力（见 §3 备注），与 runner 正交、可与 worker 组合。
- **Web 控制台适配新架构**：① 看板/详情展示 `runner`（及未来 `worker_id`）让"在哪执行"可见；② ws-worker 落地后加 **Workers 仪表盘**（连入列表/心跳/在飞 job/标签）；③（更大）控制台内**提交表单**（选 项目/agent/runner/worker_id）。详见 [`../TODO.md`](../TODO.md)。
- **产物回取**：job 除日志外产生的文件（构建产物等）经控制台/接口下载。
- **job 标签 / 元数据 + 搜索过滤**：看板按标签/项目/agent/时间检索。
- **调度策略**：超出"每项目并发"的优先级/跨 worker 公平调度。
- **远端交互对齐**：peer-http 透传已做（§6）；worker 跨线随 WP2 落地，保持三 runner 交互体验一致。

> 注：peer-http 远端 job 的交互/日志因"镜像"机制**已透明地呈现在现有控制台**（无需改前端）——这正是架构自洽的体现；上面的控制台扩展是"让远端执行位置**可见**"，非"让它**能用**"。
