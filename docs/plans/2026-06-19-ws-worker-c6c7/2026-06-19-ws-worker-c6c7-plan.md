# Gofer ws-remote-worker + C6/C7 实施计划（主文档）

> 主设计真源：[`../../design/2026-06-17-ws-remote-worker-design.md`](../../design/2026-06-17-ws-remote-worker-design.md)（v0.2，含评审 §17）。
> 关联加固：[`../2026-06-18-hardening-c2-c5-plan.md`](../2026-06-18-hardening-c2-c5-plan.md)（C2 多 caller token = per-worker token 身份底座；C4 SSE 流控 = ws 读侧背压模板）。
> 架构约束清单：[`../../design/architecture-overview.md`](../../design/architecture-overview.md) §9.1（C6/C7）。
> 本文是**骨架/契约 + 分期 + 进度索引**；各阶段 file:line 改动点、代码片段、验收在子文档（SR1105）。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-19 | Claude | 初版：基于 ws-worker 设计 v0.2 + C6/C7，按确认决策（coder/websocket、做到 WP3、C6 含 peer-http 探针、C7 仅 worker 多地址+退避重连）拆主+5 子文档。 |

## 2. 概览与目标

**ws-remote-worker**：让**无法被入站连接**的远端机（笔记本 / NAT 后 / 临时算力）运行 `gofer worker` **主动外拨**一条持久 WebSocket 连到中心 `serve`（hub），注册后接收 job 派发、用**自己的 `job.Service`/`local` runner 本地执行**、把日志/状态/结果/交互**经同一连接回流**。它是继 `local`/`peer-http` 之后的**第三种执行位置**，统一在 `runner.Runner` 抽象后（overview §5）。相比 `peer-http`（server→peer 拉取，需 peer 暴露 HTTP）：worker **纯出站**，NAT 友好。

**本轮目标（确认决策）**：
- WS 库 = **coder/websocket**（context 原生、纯 Go）。
- 做到 **WP3**：spike → WP1（端到端 + per-worker token + sink/有序/背压）→ WP2（运行中交互 + cancel/timeout）→ WP3（心跳/重连/worker-lost/多 worker 弹性）。**WP4（标签自动调度 + Web Workers 仪表盘）本轮缓做**。
- **C6** = `/v1/runners` 同时覆盖 **worker 心跳态**（依赖 WP3 心跳）+ **peer-http 主动 `/health` 探针**（不依赖 ws，可先落）。
- **C7** = 仅 **worker 端多 hub 地址 + 指数退避重连**；**显式排除**多 hub 共享注册表 / 跨 hub job 接管 / 选主（大工程，本轮 out-of-scope）。

## 3. 范围与分期

| 阶段 | 内容 | 依赖 | 子文档 |
|---|---|---|---|
| **P0 Spike** | coder/websocket 在 gookit/rux v2 上 `Accept`（Hijack/Flush）+ register/dispatch 回环最小验证 | — | [`P0-spike-plan.md`](./P0-spike-plan.md) |
| **P1 WP1 核心** | `wsproto` + `wshub` + `gofer worker` + register/dispatch + 日志/状态/结果镜像；**per-worker token 绑定**、**per-job sink 生命周期/有序**、**单连接背压**、`WorkerID` 字段贯穿 | P0 | [`P1-wp1-core-plan.md`](./P1-wp1-core-plan.md) |
| **P2 WP2 交互/取消** | 运行中交互跨线透传（P9 over WS，复用 `InteractionSink`）+ cancel/timeout | P1 | [`P2-wp2-interaction-cancel-plan.md`](./P2-wp2-interaction-cancel-plan.md) |
| **P3 WP3 弹性 + C7** | 心跳/读截止（半开检测）+ 断线重连 + worker-lost 在飞 job 处理 + 多 worker；**C7：worker 多地址 + 退避重连** | P1（P2 可并行） | [`P3-wp3-resilience-c7-plan.md`](./P3-wp3-resilience-c7-plan.md) |
| **P4 C6 可观测性** | `/v1/runners`：worker 心跳态（依赖 P3）+ peer-http 周期主动 `/health` 探针（独立，可早于 P3 起手） | P3（worker 部分）；peer-http 部分独立 | [`P4-c6-observability-plan.md`](./P4-c6-observability-plan.md) |

**建议顺序**：P0（硬门）→ P1 → {P2, P3 可并行} → P4。每子阶段绿灯即提交（SR1202）。WP4 不在本计划。

**Scope OUT（显式）**：多 hub HA / 共享注册表 / 跨 hub job 接管 / leader 选举（C7 大版）；标签自动调度、Web Workers 仪表盘（WP4）；断线后 job seq-offset 续传重放（设计 §8.5 明确推迟，worker-lost MVP 走"在飞 job 置 failed"或"挂起等重连"二选一，见 P3）。

## 4. 架构与包布局

```
hub 侧（serve 进程内，单例）          worker 侧（gofer worker 进程）
┌─────────────────────────┐         ┌──────────────────────────┐
│ httpapi /v1/workers/connect (WS) │ │ internal/worker          │
│ internal/wshub          │◄══ws══►│  connect/register/reconnect│
│   WorkerRegistry        │         │  recv dispatch→job.Service │
│   per-job demux/dispatch│         │  push log/status/result/  │
│   heartbeat             │         │  interaction; heartbeat    │
│ internal/runner/worker  │         │  多地址+退避(C7)           │
│   workerRunner(Runner)  │         │ internal/commands/worker.go│
└─────────────────────────┘         └──────────────────────────┘
        共享 internal/wsproto（envelope + type 常量）
```

**新增包**：
- `internal/wsproto` —— 消息 envelope + 帧类型常量（hub 与 worker 共享，无其它依赖）。
- `internal/wshub` —— hub 侧：`WorkerRegistry`（worker_id→conn+meta）、WS `Accept` 入口、按 `job_id` demux 入站帧、`Dispatch/Cancel/Answer` 发送、心跳。**serve 进程单例**。
- `internal/runner/worker` —— `workerRunner` 实现 `runner.Runner`：`Run()` 经 hub 发 `dispatch`、把入站 `log` 帧写进 `req.Stdout/Stderr`（镜像）、等 `result` 返回 `runner.Result`；ctx 取消 → 发 `cancel`。**由 hub 句柄构造**。
- `internal/worker` —— worker 客户端：连接/注册/重连循环、收 `dispatch`→调本地 `job.Service.Submit`（再校验）、推帧、心跳、多地址+退避（C7）。
- `internal/commands/worker.go` —— `gofer worker --config worker.yaml` 命令。

**集成钩子（现状，file:line）**：
- `runner.Runner` 接口 `internal/runner/runner.go:13-18`；`Request`（37-54，含 `Stdout/Stderr io.Writer` 镜像 sink、`Forward *Forward` 48/85-93、`Interactions InteractionSink` 53/78-80）。`workerRunner` 插入方式同 `peerhttp`。
- 派发分支：`job/service.go:152` `remote := isPeerRunner(cfg, req.Runner)`；`isPeerRunner` `483-486`。新增 `isWorkerRunner`/合并 `isRemoteRunner` 分支（worker job 同样跳过本地 agent/cwd 解析、设 `Forward`，且需带 `worker_id`）。`runReq.Forward`+`runReq.Interactions` 模板在 `service.go:213-224`。
- runner 构造：`commands/assemble.go:49-58` `buildCore` 循环 `cfg.Runners` 注册 `peer-http`。**hub 单例先建一次**，再让 `worker` 类型 runner 引用它（与 peer-http 每条配置一个不同）。
- 路由：`httpapi/server.go:126-167` `buildRouter`，`/v1/workers/connect` + `/v1/runners` 挂入。
- P9 交互**无需新增 `AnswerInteraction` 钩子**：peer-http 已用通用 `remoteInteractionSink.Open`（`job/remote_interaction.go:22-44`）阻塞 `WaitAnswer` + runner 转发答案（`peerhttp/runner.go:192-201`）；`service.go:224` 已对 remote job 设 `runReq.Interactions = remoteInteractionSink{...}`。workerRunner 复用同机制，仅把"POST 答案"换成"WS 发 `answer`"。设计 §10 #3 因此比原文更轻。

## 5. 协议契约（`internal/wsproto`）

**单连接多路复用，按 `job_id` demux**。Envelope（建议）：`{"type": <string>, "job_id": <string?>, ...payload}`。WP1 **一次性把全部帧类型与字段定全**（评审 #6：避免后续破坏性改协议），未用字段（交互等）先声明占位。

| 帧 type | 方向 | 关键字段 | 阶段 |
|---|---|---|---|
| `register` | w→s | worker_id, labels[], projects[], agents[], max_concurrent | P1 |
| `registered` | s→w | accepted(bool), reason?, server_time | P1 |
| `dispatch` | s→w | job_id, project_key, agent, runner(=local), prompt, cmd[], cwd, timeout_sec | P1 |
| `log` | w→s | job_id, stream(stdout\|stderr), seq, text | P1 |
| `status` | w→s | job_id, status（可选；result 为权威） | P1 |
| `result` | w→s | job_id, status, exit_code, error | P1 |
| `cancel` | s→w | job_id | P2 |
| `interaction` | w→s | job_id, action(open\|answered\|cancelled), interaction | P2 |
| `answer` | s→w | job_id, interaction_id, answer | P2 |
| `ping` / `pong` | both | ts | P3 |

> `seq` 在 `log` 帧内单调递增（与 C4 SSE seq 同义），hub 写镜像时据此保序。

## 6. 配置面

**Server（serve）侧** —— 新增 `workers` 映射（per-worker token 强制，§7）：
```yaml
server:
  # ...existing token/callers...
workers:
  laptop-01:
    token_env: GOFER_WORKER_LAPTOP01_TOKEN   # 必填，per-worker（§7 绑定）
    labels: [macos, gpu]                      # 展示/校验提示（WP4 才自动调度）
runners:
  remote-laptop:
    type: worker          # 新 runner 类型
    worker_id: laptop-01  # 该 runner 指向的 worker
```

**Worker（`gofer worker --config worker.yaml`）侧**：
```yaml
worker_id: laptop-01
server_link:
  urls: [wss://hub-a.internal/v1/workers/connect, wss://hub-b.internal/...]  # 多地址(C7)
  token_env: GOFER_WORKER_TOKEN
  reconnect: {初始/最大退避、抖动}   # C7，默认见 P3
projects: { ... }   # worker 本地 project/agent（与 serve 同构，本地执行用）
agents:   { ... }
max_concurrent: 4
labels: [macos, gpu]
```

## 7. 身份与鉴权（C2 → per-worker token）

- 复用 C2：WS 升级握手带 `Authorization: Bearer <token>`，经 `crypto/subtle` 常时间比对（`auth.go:57-65` `lookupCaller`）。
- **绑定规则（评审 #1，MVP 强制）**：token → 一个 worker 身份；`register.worker_id` **必须等于** 该 token 绑定的 worker_id，否则 `registered{accepted:false}` 拒绝。即 **per-worker token 强制**，杜绝"任何 server-token 持有者冒充 worker 劫持其 job"。
- `worker_id` 即一种 `caller_id`：派发产生的 job 落 `jobs.caller_id`/`worker_id`（§8）。
- WS 端点鉴权走 `/v1` 同款 Bearer，但**绕开 JSON 错误信封/Web fallback**：401 需以 WS 握手拒绝形态返回（见 P1）。
- token 不入日志（§11/SR）。

## 8. 数据模型（WorkerID 贯穿）

**勘误（重要）**：设计 §9.3 称"`JobRecord` 已预留 `worker_id` 列"——经核实 **jobstore 层已完整支持**：`jobs.worker_id TEXT` 列在 C1 schema 内（`store.go` schemaStmts）、`JobRecord.WorkerID`/`selectCols COALESCE(worker_id,'')`/`scanJob &r.WorkerID`/`UpsertJob` INSERT 均已含。**因此本轮无需 schema 迁移**。仅缺 job 包侧：
- `internal/job/model.go`：`JobRequest` 加 `WorkerID string json:"worker_id,omitempty"`；`JobResult` 加 `WorkerID string json:"worker_id,omitempty"`。
- `internal/job/service.go`：`toRecord`/`fromRecord` 映射 `WorkerID`（当前 `toRecord` 注释"WorkerID stays empty"需改）；Submit 对 `runner==worker` 校验 `worker_id` 必填且在 `workers` 配置内。

## 9. 跨阶段约束（评审强制，贯穿各阶段）

1. **per-job 日志 sink 生命周期/有序（评审 #2）**：`workerRunner.Run` **先注册** per-job `{stdout,stderr}` writer sink **再发 `dispatch`**（否则首批 `log` 帧抢在 sink 前丢）；`result`/`cancel`/`error` 时注销。hub **按 job 有序**写帧（单读循环或 per-job 串行队列——**绝不 goroutine-per-frame**，否则与 `result` 乱序）。
2. **单连接 HOL/背压（评审 #3）**：一个话痨 job 不得卡死同 worker 连接上所有 job。per-job 有界缓冲 + 节流/丢尾 + 截断标记，**复用已落地的 C4**（`stream_handler.go` 的 `maxSSEFrameBytes`/`streamThrottleBytes`/`log-rotated` 同款策略）于 WS 读侧。
3. **安全（设计 §13）**：生产 `wss://`（或反代终结 TLS）；`coder/websocket.Accept` **默认做 origin 校验**——worker 是非浏览器客户端，需显式配置 `InsecureSkipVerify`/`OriginPatterns`，否则 Accept 拒绝。
4. **再校验（评审 #8）**：worker 收 `dispatch` 后用**自己的**配置 `job.Service.Submit` 再校验 project/agent/exec 门；hub 侧 register 的 `agents[]`/`projects[]` 仅作展示/可选预校验提示。
5. **同 worker_id 重注册（设计 §8.1/§15）**：仅当**同一 token** 认证时允许替换（旧连接优雅关闭）；与 §7 绑定规则一致。

## 10. 进度跟踪

> 各阶段完成后在此勾选 + 回填提交哈希（SR1201/SR1202）。

- [x] **P0 Spike** —— coder/websocket + rux Accept 回环验证（硬门）
- [x] **P1 WP1 核心** —— 端到端远程执行 + token 绑定 + sink/有序/背压 + WorkerID 贯穿（`wsproto`/`wshub`/`runner/worker`/`worker`/`commands/worker.go`；全套 `-race` 通过；wsUpgradeWriter 已提升为 `internal/wshub/upgrade_writer.go`；无 jobstore 迁移）
- [x] **P2 WP2** —— 交互透传 + cancel/timeout（提交 `5adefc2` hub demux+Answer/Cancel + runner 交互桥 / `f727995` worker 客户端三向 + cancel/answer / `138a438` e2e 交互闭环+cancel+timeout；交互 100% 复用 `remoteInteractionSink`，`internal/job/` 零改动；全套 `-race` 通过）
- [x] **P3 WP3 + C7** —— 心跳/重连/worker-lost/多 worker + worker 多地址退避（提交 `3537826` hub 心跳+读截止+worker-lost+at-capacity / `a43a694` worker 重连退避+多地址+心跳+优雅关闭；新增 hub_p3_test/backoff_test/reconnect_test + runner worker-lost/at-capacity 用例 + 全栈断线→failed e2e；心跳 ping 15s/读截止 45s、退避 1s→30s full jitter；worker-lost = fail-fast MVP；registry 暴露 `LastHeartbeat`/`IsOnline` 喂 C6/P4；全套 `-race -count=2` 通过。设计 §8.5"恢复"与 §15"挂起窗口"按 §6.3 收敛为本轮不做）
- [x] **P4 C6** —— /v1/runners（worker 心跳态 + peer-http 主动探针）（提交 `ae6a630` wshub WorkerSnapshot 访问器 / `3c28aa9` config runner_probe 块 / `6da44cd` httpapi GET /v1/runners 端点（窄接口 runnerProber/workerRegistry，local-first 排序，鉴权复用 /v1 组）/ `0db128b` commands peerProber 探针循环 + hubWorkerRegistry 适配器 + serve 接线（startProbeLoop 镜像 startPruneLoop，stop 取消 ctx 干净退出）；peer-http 2xx=up 否则 down+error、缓存只读不阻塞请求、无 peer 时探针不起；worker 行读 hub.WorkerSnapshot 秒→毫秒；`go build && vet && gofmt && test ./... -count=1` 全绿，`-race` commands/wshub/httpapi 通过。WP4 前端不做）
- [x] 文档收尾 —— architecture-overview §9.1 C6 ✅ / C7 🟡(最小版)、§5/§6/§8 worker 行 → ✅、修订记录 v0.9、本主文档进度回填

## 11. 子文档索引

- [`P0-spike-plan.md`](./P0-spike-plan.md)
- [`P1-wp1-core-plan.md`](./P1-wp1-core-plan.md)
- [`P2-wp2-interaction-cancel-plan.md`](./P2-wp2-interaction-cancel-plan.md)
- [`P3-wp3-resilience-c7-plan.md`](./P3-wp3-resilience-c7-plan.md)
- [`P4-c6-observability-plan.md`](./P4-c6-observability-plan.md)

## 12. 结论

ws-worker 主体（到 WP3）+ C6（worker 心跳 + peer-http 探针）+ C7（worker 多地址退避重连）合为一份计划，主文档定契约（包/帧/配置/鉴权/数据模型/跨阶段约束），5 个子文档给可执行细节。关键风险点：rux 上的 WS Accept（P0 硬门先验）、单连接背压与日志有序（评审 #2/#3，复用 C4）、per-worker token 绑定（评审 #1，WP1 强制）。WP4 与多 hub HA 显式排除。审核确认后进 SUPMODE 实施。
