# dev-agent-bridge WS 远端 Worker 设计

> 子设计文档。主设计 [`2026-06-16-dev-agent-bridge-design.md`](./2026-06-16-dev-agent-bridge-design.md) 已定的事实（CLI、配置、Job 模型、`/v1` API、store、P9 交互、安全口径）不再重复，按编号引用。
> 关联：主设计 §5（架构）、§9.3（结果目录）、§11（反向/Docker 交互）、§12.4（运行中交互）；实施计划 §P7（peer-http runner）。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-17 | Claude | 初版：WS 远端 Worker（执行机连入中心 server）。已确认 4 项核心决策（Worker 执行机 / 流式推送+server 镜像 / 显式 worker_id 路由 / 与 peer-http 并存）。 |
| v0.2 | 2026-06-18 | Claude | 评审修订（详见 §17）：worker_id↔token MVP 即绑定（安全缺口）、per-job 日志 sink 生命周期+保序、单连背压/HOL、worker_id 入 `JobResult`、§8.2 持久化更正为 SQLite(jobstore)、WP1 增 ws/rux `Accept` spike + 半开读超时。**注**：本文成于 gofer 改名前，命令现为 `gofer worker`、持久化已是 `jobstore`（非 result.json/jobs.jsonl）。 |

## 2. 背景与目标

现状两种执行位置：① `local` runner 在 bridge 进程本地跑；② `peer-http` runner（P7）由 server **主动 HTTP 拉**到另一台已暴露 HTTP 的 peer bridge。两者都要求**目标可被入站访问**。

很多远端机器（开发者笔记本、NAT/防火墙后的机器、临时算力）**无法暴露入站端口**，但**能拨出**到一个已知的中心 server。本设计让这类机器以 **Worker（执行机）** 角色，用一条 **持久 WebSocket** 主动连入中心 server，注册自己的 agent/项目，接收 server 派发的 job 在**本地执行**，并把日志/状态/结果/交互**经同一条连接回传**。

目标：在不要求 worker 暴露入站端口的前提下，把远端机器的 `codex`/`claude`/`exec` 能力纳入中心 server 的统一控制面（HTTP/Web/MCP），用户体验与本地 job 一致。

## 3. 名词

| 术语 | 含义 |
|---|---|
| Server / Hub | 跑 `serve` 的中心节点，持有控制面（HTTP/Web/MCP）与 WorkerRegistry |
| Worker | 跑 `agent-bridge worker` 的远端执行机，拨出 WS 连入 server，本地执行 job |
| Dispatch | server 经 WS 把一个 job 派发给指定 worker |
| Mirror（镜像）| server 把 worker 回传的日志/结果落地到**自己的 result_dir**，作为 server 侧读取真源 |
| worker_id | worker 的稳定标识，job 路由键 |

## 4. 已确认事项（本次拍板）

1. **角色 = Worker 执行机**：中心 server 协调，远端连入执行；输出在 worker 本地产生。
2. **日志真源 = 流式推送 + server 镜像**：worker 边跑边推日志帧，server 落地镜像到自己 `result_dir`；**server 侧 HTTP/Web/MCP/SSE 读路径完全不变**，worker 离线也能查历史。
3. **路由 = 显式 worker_id**：提交 job 时指定目标 worker_id。
4. **与 peer-http 并存**：ws-worker（worker 拨出，NAT 友好）与 peer-http（server 拉，需入站）作为两种远端机制共存。

## 5. 范围

**做**：worker 连入/注册、显式 worker_id 派发、日志/状态/结果镜像、运行中交互跨线、取消、心跳与断线处理、worker 侧本地执行复用现有 job.Service。

**不做（本期）**：能力/标签自动调度、worker 集群 HA、worker 端独立 HTTP 面、超大日志的流控优化（仅留 TODO）、Web 控制台的 worker 仪表盘（后续）。

## 6. 总体思路（一句话）

> **Worker 就是一台"反向接入"的 bridge**：它内部仍用现有的 `job.Service`/`local` runner/store 执行 job（与单机模式零差异），只是把"接收任务"和"回传输出"从 HTTP 入站换成了一条**拨出的持久 WS**。Server 侧则把 worker 当成一种新的 `runner`：派发 = 发 WS 消息，"执行结果" = 把 worker 推回的帧**写进 server 自己的日志文件**——于是 server 现有的所有读路径（HTTP 拉日志、SSE 实时流、Web 控制台、MCP `bridge_tail_log`）**一行不改**就能用。

这正是"server 怎么读 worker 的输出"的答案：**不直接读 worker，而是 worker 把帧推回来、server 写进本地镜像，再走老的读路径**。worker 本地也留一份自己的记录（`客户端也有任务记录`），用于 worker 自身排障/离线；server 镜像是 server 侧用户查询的真源——两份记录各自独立、互不耦合。

## 7. 架构总览

```txt
        ┌────────────────────── 中心 Server (serve) ──────────────────────┐
用户 ──▶ │ HTTP /v1 + Web 控制台 + MCP                                       │
HTTP/MCP │   │ 提交 {runner:"worker", worker_id:"w1", agent, cmd/prompt}     │
         │   ▼                                                              │
         │ job.Service ──▶ workerRunner.Run(req)  ──┐                       │
         │   ▲  (镜像写 result_dir/<job>/*.log)      │                       │
         │   │                                      ▼                       │
         │ store/SSE/MCP 读路径不变 ◀── WorkerHub (WorkerRegistry: w1→conn)  │
         └──────────────────────────────▲──┬────────────────────────────────┘
                          register/log/  │  │  dispatch/cancel/answer
                          status/result/ │  │  (server→worker)
                          interaction    │  ▼   持久 WS（worker 拨出）
        ┌──────────────────────── 远端 Worker (agent-bridge worker) ────────┐
        │  ws client ──▶ 本地 job.Service ──▶ local runner ──▶ codex/claude/exec│
        │                 (worker 自己的 store / 自己的 allowlist 校验)        │
        └──────────────────────────────────────────────────────────────────┘
```

## 8. 关键流程

### 8.1 连入与注册

```txt
worker                                   server(hub)
  │ WS upgrade GET /v1/workers/connect (Authorization: Bearer) ─▶
  │                                       auth ok → websocket.Accept
  │ register{worker_id, labels, projects[], agents[], max_concurrent} ─▶
  │                                       WorkerRegistry.put(worker_id, conn, meta)
  │ ◀─ registered{accepted:true, server_time}
  │ ⟂ 持久连接：周期 ping/pong 心跳
```

- 同一 worker_id 重连：替换旧 conn（旧的优雅关闭）。
- register 上报的 projects/agents 仅供 server 展示与校验提示；**真正的执行校验在 worker 侧**（见 §12）。

### 8.2 派发 + 日志镜像（核心）

```txt
用户 ─HTTP POST /v1/jobs {project_key,agent,runner:"worker",worker_id:"w1",cmd|prompt}─▶ server
server: 校验 w1 在线、worker 在 allowed_runners → 建 job 记录(queued)+分配 job_id
server.workerRunner.Run(req): hub.dispatch(w1, {job_id, project_key, agent, prompt/cmd, cwd, timeout})
worker: 收 dispatch → 本地 job.Service.Submit（worker 重新校验 allowlist/exec/SafeJoin）
worker: 边跑边推 log{job_id, stream, seq, text}
server.hub: 按 job_id 解复用 → 把 text 写入 req.Stdout/req.Stderr
            （= <result_dir>/<job_id>/stdout.log）→ server SSE/HTTP/Web/MCP 读镜像，零改动
worker: 结束推 result{job_id, status, exit_code, error}
server.workerRunner.Run 返回 runner.Result → job.Service.finish → jobstore.UpsertJob(终态)  // C1 后：DB 真源，非 result.json/jobs.jsonl
```

要点：**workerRunner 把入站日志帧写进 `req.Stdout/req.Stderr`**——这对 Writer 正是 store 打开的日志文件（local runner 同款管道）。镜像因此天然复用现有 store 与全部读路径，无需新读接口。

### 8.3 运行中交互跨线（P9 over WS）

```txt
worker job 触发交互 → worker 推 interaction{job_id, action:"open", interaction}
server.hub → server.job.Service：把该交互注入 server 端 job 记录（job 转 pending_interaction）
   ⇒ server SSE 推 interaction 事件、Web 弹交互卡、MCP bridge_get_interactions 可见
用户经 HTTP/Web/MCP 作答 → server.AnswerInteraction → 触发 hub 回调
server.hub → 发 answer{job_id, interaction_id, answer} 给 worker
worker.job.Service.AnswerInteraction(answer) → worker job 回 running 续跑
```

- 唯一需要的小钩子：server 的 `AnswerInteraction` 对**远端 job** 需通知 hub 回发 answer 帧（见 §10 模块）。
- 交互 id 由 worker 生成并随 open 帧带上，server 镜像沿用同一 id，作答按该 id 回程。

### 8.4 取消 / 超时

- server 端 ctx 取消/超时（沿用 `normalizeTimeout`）→ workerRunner 发 `cancel{job_id}` → worker 本地 `job.Service.Cancel` → worker 杀子进程、推 result(status=cancelled)。
- server 侧若先判定超时，直接 finish 为 timeout，并发 cancel 通知 worker 收尾。

### 8.5 断线与恢复

- 心跳丢失 → server 标记 worker 离线、从 WorkerRegistry 摘除。
- 在飞 job 策略（**MVP**）：worker 断线 → 其在飞 server job 置 `failed`（error: worker disconnected）。
- 恢复（**后续，WP3 不实现**，见 [P3 §6.3](../plans/2026-06-19-ws-worker-c6c7/P3-wp3-resilience-c7-plan.md#6-设计决策记录自动决策sr1430-留痕)）：worker 重连 re-register，log 帧带 seq/offset，hub 从上次镜像偏移续写；worker 进程存活则 job 不中断。WP3 一律 fail-fast（§15"挂起等重连窗口"收敛为不做）。

## 9. 数据模型

### 9.1 WS 消息信封

统一 JSON，单连多路复用（一条 WS 承载该 worker 所有 job 的帧），按 `job_id` 解复用：

```jsonc
{ "type": "<msg-type>", "job_id": "<id|empty>", /* ...payload by type... */ }
```

| 方向 | type | payload 关键字段 | 说明 |
|---|---|---|---|
| w→s | `register` | worker_id, labels[], projects[], agents[], max_concurrent | 连入注册 |
| s→w | `registered` | accepted, reason?, server_time | 注册应答 |
| s→w | `dispatch` | job_id, project_key, agent, runner(local), prompt, cmd[], cwd, timeout_sec | 派发任务（payload 即一份 JobRequest 投影） |
| w→s | `log` | job_id, stream(stdout\|stderr), seq, text | 增量日志帧 |
| w→s | `status` | job_id, status | 状态变更（可选，最终以 result 为准） |
| w→s | `interaction` | job_id, action(open\|answered\|cancelled), interaction | 交互事件（沿用 §8.3 中 Interaction 结构） |
| s→w | `answer` | job_id, interaction_id, answer | 用户作答回程 |
| w→s | `result` | job_id, status, exit_code, error | 终态结果 |
| s→w | `cancel` | job_id | 请求取消 |
| 双向 | `ping`/`pong` | ts | 心跳 |

> payload 内尽量复用既有 JSON 结构（JobRequest / JobResult / Interaction），减少协议面。

### 9.2 配置增量

**Server**（沿用现有 config，新增可选段）：
```yaml
server:
  addr: 0.0.0.0:8765
  token_env: AGENT_BRIDGE_TOKEN
workers:                 # 可选；不配则任何持 server token 的连接皆可注册（MVP）
  w1:
    token_env: W1_TOKEN  # 每 worker 独立 token（增强；MVP 可复用 server token）
    labels: [gpu, host-mac]
```

**Worker**（= 一份普通 bridge config + server 指针）：
```yaml
server_link:
  url: wss://central.example:8765/v1/workers/connect
  worker_id: w1
  token_env: W1_TOKEN
projects:                # worker 本地项目（host_path 是 worker 本机路径）
  laptop:
    host_path: /Users/me/proj
    allowed_agents: [codex, claude, exec]
    allow_exec: true
agents: { codex: {...}, claude: {...}, exec: {...} }
runners: { local: { type: local } }
```

### 9.3 JobRequest 增量

新增可选字段 `worker_id`（当 `runner == "worker"` 时必填）：
```go
type JobRequest struct {
    ... // 现有字段不变
    WorkerID string `json:"worker_id,omitempty"`
}
```

`JobResult` 也带上 `worker_id`，控制台/详情才能显示"在哪台执行"（`JobRecord` 在 C1 已预留 `worker_id` 列，仅需 `toRecord/fromRecord` 映射 + DTO 透出）：
```go
type JobResult struct {
    ... // 现有字段不变
    WorkerID string `json:"worker_id,omitempty"`
}
```

## 10. 模块与代码结构

| 包 / 文件 | 角色 |
|---|---|
| `internal/wsproto` | WS 消息信封与 type 常量（server/worker 共享）；payload 复用 job 包结构 |
| `internal/wshub`（server）| `websocket.Accept` 升级处理 `GET /v1/workers/connect`；`WorkerRegistry`（worker_id→conn+meta）；按 job_id 解复用入站帧；`Dispatch/Cancel/Answer` 发送；心跳 |
| `internal/runner/worker`（server）| `workerRunner` 实现 `runner.Runner`：`Run()` 经 hub 发 dispatch、把入站 log 帧写 `req.Stdout/Stderr`、等 result 返回 `runner.Result`；ctx 取消→发 cancel |
| `internal/worker`（client）| 连接/注册/重连循环；收 dispatch→本地 `job.Service.Submit`；把本地 job 的日志/状态/交互/结果推回；心跳 |
| `internal/commands/worker.go` | 新命令 `agent-bridge worker --config worker.yaml`（拨出连入模式）|
| serve 装配（`buildCore`/serve）| 构建 `WorkerHub`，注册名为 `worker` 的 runner（由 hub 支撑）；挂 `/v1/workers/connect` 路由 |

**唯一侵入既有代码的点**：
1. `JobRequest` 加 `worker_id` 字段（§9.3）。
2. 注册一个 `worker` runner + 一条 WS 路由（装配层）。
3. P9 交互回程钩子：`job.Service` 对远端 job 在 `AnswerInteraction` 后通知 hub 回发 answer（最小钩子，如 per-job answer 回调；或 hub 订阅）。其余 job.Service/store/SSE/Web/MCP **零改动**。

依赖：`github.com/coder/websocket`（v1.8.15，现代、context 友好、纯 Go 无 cgo，配合 net/http `Accept` 可在 rux 上用）。

## 11. 安全

- **worker 拨出、无入站端口**：worker 侧无 SSRF/入站攻击面；只需出站可达 server。
- **WS 鉴权**：升级请求带 `Authorization: Bearer`（MVP 复用 server token；增强为 per-worker token，DB/config 存 hash，SR201 口径，便于吊销）。
- **worker 重新校验（关键不变量）**：派发来的 job 一律走 **worker 自己的** `job.Service.validate`（项目/agent allowlist + exec 放行 + `SafeJoin` cwd 越界校验）。**被攻陷或恶意的 server 无法让 worker 跑未放行的 agent 或逃出项目目录**——执行授权始终由 worker 本地掌握。
- **token / 密钥不入日志**；日志尾部上限沿用 256KB（镜像后由 server 既有逻辑约束）。
- server 镜像落 `result_dir`，默认 private（沿用 §9.3 主设计）。

## 12. 与现有机制的关系

| 机制 | 连接方向 | 适用 | 本设计取舍 |
|---|---|---|---|
| `local` runner | 进程内 | bridge 本机执行 | 不变 |
| `peer-http` runner（P7）| server→peer（HTTP，需 peer 暴露入站）| peer 可被入站访问 | **并存保留** |
| `worker`（本设计）| worker→server（WS 拨出，NAT 友好）| 远端不可入站 | 新增 |

三者都通过 `runner` 抽象接入 `job.Service`，对 HTTP/Web/MCP 调用方透明。

## 13. 部署

- Server：与现有 `serve` 同进程，多挂一个 WS 路由 + WorkerHub，无需独立 Pod（SR1001）。
- Worker：远端机器跑 `agent-bridge worker --config worker.yaml`，常驻拨出；建议配 systemd/计划任务保活 + 断线自动重连（客户端内置）。
- 连接走 `wss://`（生产）；server 在反代后终止 TLS 亦可。

## 14. 实施分期（建议）

| 阶段 | 内容 | 必须 |
|---|---|---|
| WP1 | `wsproto` + `wshub` + `worker` 命令 + register/dispatch + 日志/状态/结果镜像（单 worker、显式 worker_id、无交互）→ 端到端远端执行 | 必须 |
| WP2 | 运行中交互跨线（§8.3，P9 over WS）+ 取消/超时 | 必须 |
| WP3 | 韧性：心跳、断线/重连续传、worker-lost 在飞 job 处理；per-worker token；多 worker | 推荐 |
| WP4 | 能力/标签自动调度；Web 控制台 worker 仪表盘 | 可延后 |

> MVP = WP1 + WP2。协议信封、配置结构在 WP1 即按全量预留，避免后续破坏。

## 15. 待确认

- 在飞 job 的 worker-lost 策略：MVP `failed`，是否需要"挂起等重连"窗口？
- ~~per-worker token vs 复用 server token 的落地时机（MVP 可先复用）~~ → **评审 §17-#1 收敛**：MVP 即需 worker_id↔token 绑定（防冒名顶替），不再"先复用任意 token"。
- 超大/高频日志经 WS 的背压与分片策略（先不做，留 TODO）。
- worker 是否需要可选的本地只读 HTTP（自查），还是纯出站（倾向纯出站）。
- 多 worker 同 worker_id 抢注的处理（倾向后者替换前者）。

## 16. 结论

以"Worker = 反向接入的 bridge + server 把 worker 当一种 runner"为核心，复用既有 `job.Service`/`store`/SSE/Web/MCP/P9 几乎零改动即可把 NAT 后的远端算力纳入统一控制面。唯一新面是一条持久 WS 与其消息协议；执行授权始终留在 worker 本地，安全边界清晰。建议按 WP1→WP2 推进 MVP，**开工前先落实 §17 评审修订（尤其 #1/#2/#3/#6）**。

## 17. 评审修订（v0.2，2026-06-18）

对 v0.1 的评审结论。下列条目**修订/补充上文对应章节**，冲突处以本节为准。

### 🔴 需处理（影响协议/安全，实施前落实）

- **#1 worker_id ↔ token 绑定（MVP 即需，修订 §8.1/§9.2/§11/§15）**：v0.1 的"不配 `workers` 则任何持 server token 的连接可注册任意 worker_id" + "同 worker_id 重连替换旧 conn" ⇒ 任何持 server token 者可**冒名顶替**合法 worker、劫持其 job。**修订**：register 必须校验 worker_id 与其凭据绑定——MVP 即用 per-worker token（`workers.<id>.token_env`），或至少"worker_id 替换须同 token"。`worker_id` 视作一种 `caller_id`，与 C2 的 per-caller token 共用同一"token→身份"底座（见 [`plans/2026-06-18-hardening-c2-c5-plan.md`](../plans/2026-06-18-hardening-c2-c5-plan.md) §7）。

- **#2 per-job 日志 sink 生命周期 + 帧保序（补 §8.2/§10）**：一条 WS 多路复用该 worker 全部 job，hub 按 job_id 解复用写各自 `req.Stdout/Stderr`。**规约**：`workerRunner.Run` **在发 `dispatch` 之前**就向 hub 注册本 job 的 writer sink（map[job_id]→{stdout,stderr}），收到 `result`/`cancel`/`error` 才注销；避免首批 `log` 帧早于 sink 注册而丢失。hub 须**按 job 保序**写入（同一连接的入站帧顺序处理，或每 job 串行队列；**不可每帧起 goroutine**，否则乱序 + 与 `result` 抢序）。

- **#3 单连接 HOL 阻塞 / 背压（补 §8.2/§11/§15）**：镜像写盘慢于 worker 产日志时，单 WS 读循环阻塞 → **同 worker 上所有 job 一起卡**（一个话痨 job 拖垮其他）。即便 MVP 也需：每 job 有界 buffer + 满则节流/丢尾 + 标记截断；与 [C4 日志流控](../plans/2026-06-18-hardening-c2-c5-plan.md#6-phase-c--c4-日志流控)对齐设计。

### 🟡 应修正（事实/一致性，已就地改）

- **#4 §8.2 持久化更正**：finish 已是 `jobstore.UpsertJob`（C1），非 `result.json + jobs.jsonl`（已就地改）。
- **#5 `worker_id` 入 `JobResult`**：控制台/详情显示"在哪台执行"；`JobRecord` 已预留列（已在 §9.3 补 DTO 字段）。

### 🟢 规约澄清 / 前置

- **#6 WP1 前置 spike**：确认 `github.com/coder/websocket` 能在 `gookit/rux` v2 上 `Accept`（拿到原始 `http.ResponseWriter/Request` 做 hijack）。1 个最小验证，避免 WP1 中途受阻。
- **#7 半开连接检测（补 §8.5）**：除心跳外须显式设 WS **read deadline**，否则 TCP 静默断（无 FIN）检测不到；coder/websocket ping/pong + 读超时为标准做法。
- **#8 派发被拒路径（补 §8.2）**：worker 本地 `validate` 失败（如该 agent 在 worker 未放行）→ 直接推 `result{status:failed, error}`，server 据此 finish，无需新帧类型。
- **#9 server 侧 `JobResult.Cwd` 对远端 job 为信息性**：真实执行目录在 worker 本地（worker 用自己 `host_path` SafeJoin），server 记录仅供展示。

> 落实顺序：实施 WP1 前先做 #6 spike + 把 #1/#2/#3 写进 §8.2/§9.2/§11 的实现细节；#4/#5 已改。
