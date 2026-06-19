# P2 / WP2 —— 运行中交互透传 + cancel/timeout（实施子文档）

> 主文档（契约真源）：[`./2026-06-19-ws-worker-c6c7-plan.md`](./2026-06-19-ws-worker-c6c7-plan.md)
> 主设计：[`../../design/2026-06-17-ws-remote-worker-design.md`](../../design/2026-06-17-ws-remote-worker-design.md) §8.3（交互跨线）、§8.4（取消/超时）、§9.1（帧）、§10（#3 钩子评审）。
> 依赖：**P1（WP1 核心）已落地** —— `internal/wsproto` 帧/常量、`internal/wshub`（WorkerRegistry + 入站帧 demux + Dispatch 发送 + per-job sink 注册/注销）、`internal/runner/worker.workerRunner`（已把 `log`/`status`/`result` 帧落 `req.Stdout/Stderr` 并返回 `runner.Result`）、`internal/worker` 客户端（收 `dispatch` → 本地 `job.Service.Submit` → 推帧）、`WorkerID` 贯穿。本文只补 **`interaction`/`answer`/`cancel` 三类帧的端到端打通**，不重述 P1 的连接/注册/镜像。

构建环境：`export PATH=/d/work/inhere/linux-env/sdk/gosdk/go1.25.10/bin:$PATH; cd tools/gofer`

---

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-19 | Claude | 初版：按主文档 §5 帧契约 + §4 集成钩子拆 P2。核心结论：**交互透传 100% 复用 peer-http 的 `remoteInteractionSink` 机制**（hub 侧 `workerRunner` 提供 `InteractionSink`，其 `Open` 阻塞等 hub 回程答案，与 peer-http 阻塞 `WaitAnswer` 同形），**不新增 `AnswerInteraction` 钩子**；cancel/timeout 复用现有 ctx + `classify` 语义。 |

---

## 2. 范围

**做（P2）**：
1. **运行中交互透传**（worker→hub→user→hub→worker）：worker 本地 job 触发交互 → worker 发 `interaction{action:open}` → hub 注入 host job（转 `pending_interaction`）→ 现有 HTTP/Web/MCP/SSE 交互面**零改动**呈现 → host 用户经现有 answer API 作答 → hub 发 `answer{interaction_id,answer}` → worker 投递本地 job → 续跑。
2. **cancel**：host `POST /v1/jobs/{id}/cancel`（worker job）→ ctx 取消 → `workerRunner` 发 `cancel{job_id}` → worker 本地 `job.Service.Cancel` → worker 推 `result{status:cancelled}` → hub finalize。保持现有 Cancel 语义（终态 no-op、未知 404）。
3. **timeout**：worker 端本地 `job.Service` 自带的超时（`normalizeTimeout` + `context.WithTimeout`）owns 执行超时；超时本地 finish → 推 `result{status:timeout}`。hub 侧 ctx 作为**兜底超时**（同样基于 `normalizeTimeout`），双方哪边先到哪边赢，最终态以 `result` + host ctx `classify` 收敛。

**不做（移交其他阶段）**：
- worker 断线/重连、半开检测、heartbeat、worker-lost 在飞 job 处理 → **P3**。本文仅在 §7.4 标注「pending_interaction 期间 worker 断线」的边界，详细处理 defer 到 P3。
- 标签自动调度、Web worker 仪表盘 → WP4（本轮不做）。

---

## 3. 复用点说明（为何无需改 `AnswerInteraction`）

主文档 §4 的「集成钩子」结论：**P9 交互无需新增 `AnswerInteraction` 钩子**。逐条对照已落地代码确认：

| peer-http 现状（已落地） | worker（P2 复用） |
|---|---|
| host 对 remote job 设 `runReq.Interactions = remoteInteractionSink{s, jobID}`（`internal/job/service.go:224`，分支 `remote := isPeerRunner(...)` `service.go:152`） | host 对 worker job 设**同一个** `remoteInteractionSink{s, jobID}`（仅扩 remote 分支判定，见 §5 改动 1） |
| `remoteInteractionSink.Open` 注入 host 交互（`injectInteraction` → `pending_interaction`）+ 起 goroutine 阻塞 `WaitAnswer`，答案到了写 `ch`（`internal/job/remote_interaction.go:22-44`） | **完全不改** —— `Open` 与 runner 无关，对 worker job 语义一致 |
| runner（`peerhttp.Runner`）在 `handleFrame` 里：收 peer SSE `interaction{open}` → `req.Interactions.Open(ctx, ri)` → 起 goroutine `<-ansCh` → `r.c.AnswerInteraction(peerID,...)`（HTTP POST 回 peer）（`internal/runner/peerhttp/runner.go:179-203`） | `workerRunner` 同形：收 hub demux 来的 **WS `interaction{open}`** → `req.Interactions.Open(ctx, ri)` → 起 goroutine `<-ansCh` → **发 WS `answer` 帧**（替换「HTTP POST」为「WS send」），见 §5 改动 2 |
| host 用户作答仍走现有 `s.jobs.AnswerInteraction`（`internal/httpapi/interaction_handler.go:79-94` → `internal/job/interaction.go:235`），其 `close(rec.answered)` 唤醒 `WaitAnswer` | **完全不改** —— answer API、`AnswerInteraction`、`WaitAnswer`、SSE `pumpInteractions`（`stream_handler.go:196`）全部零改动 |

**结论**：交互的「权威记录持有方 = host」（与 P9 一致）。host job 的 `interactionRec`（`injectInteraction` 写入 `entry.interactions`，`internal/job/remote_interaction.go:63-95`）是唯一权威；worker 本地 job 也有一份自己的交互记录（worker 自己的 `job.Service`），但它只是「执行侧镜像」——作答事实从 host 经 `answer` 帧回流，worker 调本地 `AnswerInteraction` 续跑。两份记录各自独立、互不耦合（设计 §6 同款双记录哲学）。

唯一的「集成」工作落在 **worker runner 一侧**（把 peer-http 的「POST 答案」换成「WS 发 answer 帧」）+ **worker 客户端一侧**（把本地 job 交互桥到 WS 帧），`internal/job/` 包**零侵入**。这正是主设计 §10 #3 评审后「比原文更轻」的兑现：原 §10 #3 设想需要在 `AnswerInteraction` 加 per-job answer 回调或 hub 订阅，实际因 `remoteInteractionSink` 通用机制已存在而**完全省掉**。

### 3.1 `interaction` 帧 `action` 取值语义（与现有 SSE 对齐）

帧 `action` 沿用现有 SSE `interactionFrame` 的同一词表（`internal/httpapi/stream_handler.go:58-61, 200-213`，由 interaction 状态派生）：

| action | 触发 | hub 处理 | 说明 |
|---|---|---|---|
| `open` | worker 本地 job 起交互（pending） | `remoteInteractionSink.Open` → `injectInteraction`（幂等：重发 open 是 no-op，`remote_interaction.go:75-79`） | **P2 必须** |
| `answered` | （可选）worker 侧已自行作答（如 worker 本地有人作答） | hub 可镜像更新 host 交互状态（best-effort）；MVP 可仅记录、不强制 | 占位，MVP 可不主动用 |
| `cancelled` | worker job 终止时仍 pending 的交互 | 随 `result` 帧统一收敛（host job finish 时 pending 交互自然失效，`WaitAnswer` 因 ctx/done 关闭返回） | 占位 |

> MVP 实质只依赖 `open`；`answered`/`cancelled` 在 P1 已按主文档「一次性定全帧字段」声明占位，P2 不强制实现其入站处理，但 hub demux 必须**容忍并安全忽略**未实现的 action（不 panic、不误判终态）。

---

## 4. 改动清单（file → 改动）

> P1 已建的包/类型为基线。下列均为 P2 增量。`internal/job/` 包**不改任何已有逻辑**（仅 §5 改动 1 的 remote 分支判定可能在 P1 已合并为 `isRemoteRunner`，若 P1 已做则本文 0 改动 job 包）。

| 文件 | 改动 | 阶段内子任务 |
|---|---|---|
| `internal/job/service.go` | **若 P1 未合并**：把 `service.go:152` 的 `remote := isPeerRunner(cfg, req.Runner)` 扩为 `remote := isRemoteRunner(cfg, req.Runner)`（含 `worker` 类型），使 worker job 也走 `service.go:213-224` 的 `Forward + Interactions = remoteInteractionSink{s, jobID}` 模板。**若 P1 已合并 → 本文件 0 改动**。 | T1 |
| `internal/wsproto/*.go` | **P1 已定全帧**。P2 仅确认 `interaction`（w→s：`job_id, action, interaction`）、`answer`（s→w：`job_id, interaction_id, answer`）、`cancel`（s→w：`job_id`）三帧的字段/JSON tag 与主文档 §5 一致；`interaction.interaction` payload 复用 `job.Interaction` 投影（同 peer-http `peerInteractionFrame`，`peerhttp/runner.go:228-231`）。如有缺字段则补齐（应无）。 | T1 |
| `internal/wshub/*.go`（hub） | (a) 入站 demux：`interaction` 帧按 `job_id` 路由到对应 `workerRunner` 的回调/通道（沿用 P1 的 per-job sink demux 同一机制，**保序**，§9 约束 #2）。(b) 出站：新增 `Answer(workerID, jobID, interactionID, answer)` 与 `Cancel(workerID, jobID)` 发送方法（与 P1 `Dispatch` 同款单写串行，§9 约束 #2「不可每帧 goroutine」）。 | T2 |
| `internal/runner/worker/runner.go`（hub 侧 `workerRunner`） | (a) `Run` 在发 `dispatch` 前已注册 per-job sink（P1）；P2 额外把 `req.Interactions`（host 注入的 `remoteInteractionSink`）接到入站 `interaction{open}` 处理：收到 open → `req.Interactions.Open(ctx, runner.RemoteInteraction{...})` → 起 goroutine `if ans, ok := <-ansCh; ok { hub.Answer(workerID, jobID, iid, ans) }`（**逐字复用** `peerhttp/runner.go:184-202` 的结构，把 `r.c.AnswerInteraction` 换成 `hub.Answer`）。维护 `seen map[string]bool` 防重复桥接（同 `peerhttp/runner.go:127,184`）。(b) `ctx.Err() != nil`（cancel/timeout）→ `hub.Cancel(workerID, jobID)`（best-effort），然后照常等/取 `result`（同 `peerhttp/runner.go:97-99`）。 | T2 |
| `internal/worker/*.go`（worker 客户端） | (a) **交互桥出**：worker 收 `dispatch` 提交本地 job 后，需把本地 job 的 pending 交互推成 WS `interaction{action:open}`。本地 `job.Service` 的交互入口是 `CreateInteraction`（`internal/job/interaction.go:88`）由本地 agent/runner 触发；worker 客户端**对本地 job 起一个观察者**：复用本地 SSE/`GetPersistedInteractions` 或本地交互回调，监测到新 pending 交互 → 发 `interaction{open}` 帧（带本地生成的交互 id）。(b) **交互回程**：worker 收 hub 的 `answer{interaction_id, answer}` 帧 → 调本地 `localSvc.AnswerInteraction(jobID, interactionID, answer)`（`interaction.go:235`）→ 本地 job 续跑。(c) **cancel 入站**：worker 收 `cancel{job_id}` → 调本地 `localSvc.Cancel(jobID)`（`cancel.go:36`）→ 杀子进程；本地 `classify` 据 ctx.Canceled 得 `cancelled` → 本地 finish → worker 推 `result{status:cancelled}`。(d) **timeout**：本地 `job.Service` 自带 `context.WithTimeout`（`service.go:316`），超时 → 本地 `classify` 得 `timeout`（`service.go:464`）→ 推 `result{status:timeout}`，worker 无需额外计时器。 | T3 |
| `internal/runner/worker/runner_test.go`（新） | sink 桥接单测（`-race`）：mock hub 收 `interaction{open}` → 验 `req.Interactions.Open` 被调；mock host answer 经 `ansCh` → 验 `hub.Answer` 被发；channel 关闭无值（job 提前结束）→ 验**不发** answer。 | T4 |
| `internal/worker/*_test.go`（新/扩） | worker 客户端单测：收 `answer` 帧 → 验调本地 `AnswerInteraction`；收 `cancel` → 验调本地 `Cancel` 且推 `result{cancelled}`；本地超时 → 推 `result{timeout}`。 | T4 |
| `internal/wshub/*_e2e_test.go` 或 `internal/httpapi/`（e2e，复用现有 e2e 框架，参考 `internal/httpapi/e2e_interaction_test.go`） | 端到端：起 hub + 内存 worker（loopback WS）→ 提交 worker job 触发交互 → `GET /v1/jobs/{id}/interactions` 见 `pending_interaction` → `POST .../answer` → worker job 续跑至 `done`；cancel 路径；timeout 路径。 | T4 |

> **建议子任务顺序**：T1（帧/分支确认）→ T2（hub 出站 + runner 桥接，cancel 同批）→ T3（worker 客户端三向）→ T4（测试 + `-race`）。每子任务绿灯即提交（SR1202）。

---

## 5. 关键流程

### 5.1 交互透传时序（worker → hub → user → hub → worker）

```txt
worker.localJob          worker.client         hub(workerRunner)        host.job.Service        user(HTTP/Web/MCP)
   │ agent 触发交互          │                      │                        │                       │
   │ CreateInteraction ─────▶│ (本地 pending)         │                        │                       │
   │ (本地 job→pending)      │                       │                        │                       │
   │                        │── interaction{open, id, interaction} ──WS──▶│                        │                       │
   │                        │                       │ demux by job_id        │                       │
   │                        │                       │ req.Interactions.Open──▶│ injectInteraction      │
   │                        │                       │   (起 goroutine 等 ansCh)│  (host job→pending_interaction) │
   │                        │                       │                        │── persist + SSE ───────▶ GET /interactions
   │                        │                       │                        │                       │  见 pending_interaction
   │                        │                       │                        │                       │  POST /interactions/{iid}/answer
   │                        │                       │                        │◀── AnswerInteraction ──┤
   │                        │                       │                        │  close(rec.answered)   │
   │                        │                       │ ◀── WaitAnswer 返回 ────│  (host job→running)    │
   │                        │                       │ ansCh<-answer          │                        │
   │                        │◀── answer{iid, answer} ─WS──┤ hub.Answer(...)   │                        │
   │◀── AnswerInteraction ──┤ localSvc.AnswerInteraction  │                  │                        │
   │ (本地 job→running 续跑) │                       │                        │                       │
   │ ...继续执行至结束        │── result{status:done} ─WS──▶│ workerRunner.Run 返回 ─▶ finish(done)    │
```

要点：
- **权威交互记录 = host**（`host.job.Service.entry.interactions`）；worker 本地交互是执行侧镜像。
- host 侧 `injectInteraction`/`AnswerInteraction`/`WaitAnswer`/SSE **零改动**——这是复用 peer-http 机制的直接收益。
- 交互 id 由 **worker 本地生成**（本地 `CreateInteraction` 产出），随 `open` 帧上行；host 沿用同一 id；`answer` 帧按该 id 回程（与设计 §8.3 末「id 由 worker 生成」一致）。
- 幂等：worker 可能重发 `open`（重连场景，P3），hub→`injectInteraction` 对重复 id no-op（`remote_interaction.go:75-79`），runner `seen` 防重复转发。

### 5.2 cancel 时序

```txt
user        host.httpapi            host.job.Service        hub(workerRunner)        worker.client       worker.localJob
 │ POST /v1/jobs/{id}/cancel │                              │                        │                   │
 │──────────▶│ handleCancelJob ──▶ Cancel(id)               │                        │                   │
 │           │                    entry.cancel()  (ctx→Canceled)                     │                   │
 │           │                              │ run.Run 的 ctx.Done() 触发 ─▶│         │                   │
 │           │                              │              hub.Cancel(workerID,jobID)─WS─▶│              │
 │           │                              │                        │     localSvc.Cancel(jobID)──▶│ 杀子进程
 │           │                              │                        │                   │ 本地 classify→cancelled
 │           │                              │                        │◀── result{status:cancelled} ─WS─┤
 │           │                              │ workerRunner.Run 返回 runner.Result ◀──────┤              │
 │           │                              │ classify(ctx=Canceled, res) → StatusCancelled            │
 │           │                              │ finish(cancelled)        │                 │              │
 │◀─ 200 {snapshot} ─┤ Get(id)             │                          │                 │              │
```

要点（映射现有 Cancel 语义，`internal/job/cancel.go:36-59`）：
- **已终态 worker job**：host `Cancel` 是 stable no-op（返回 nil；entry 已 evict 则查 meta 命中 → no-op），HTTP 返回 200 + 当前快照。**不**向 worker 发 `cancel`（job 已 finish，`workerRunner.Run` 已返回，无 ctx 可取消）。
- **未知 job id**：host `Cancel` 返回 `unknown job` → `handleCancelJob` 映射 404（`job_handler.go:108-117`，零改动）。
- host→worker 的 `cancel` 帧是 **best-effort**：即使丢失，host ctx 已 Canceled，`classify` 仍会把 host job 收敛为 `cancelled`；worker 侧由 P3 的 worker-lost / ctx 兜底回收（见 §7.4）。

### 5.3 cancel/timeout 归属（host vs worker）

| 维度 | 归属方 | 机制 |
|---|---|---|
| **执行超时**（杀进程） | **worker owns** | worker 本地 `job.Service.execute` 的 `context.WithTimeout(normalizeTimeout(req.TimeoutSec))`（`service.go:285,316`）；超时本地 `classify`→`timeout`（`service.go:464`）→ 推 `result{status:timeout}`。 |
| **host 侧超时** | hub **兜底（backstop）** | host `workerRunner` 在 host job 的 ctx（同样 `normalizeTimeout`）；正常情况下两侧用同一 `timeout_sec`（`dispatch` 帧携带，主文档 §5），worker 先到先 finish。host ctx 兜底防 worker 静默（result 永不回）。哪边先到：worker `result{timeout}` 先到 → host `classify` 时 ctx 多半也 DeadlineExceeded → 仍 `timeout`，一致；host ctx 先到（worker 卡死）→ host `classify`→`timeout` + 发 `cancel` 通知 worker 收尾。 |
| **最终态收敛** | **host**（`classify`，`service.go:360,461`） | host `classify(ctx, res)` 优先看 ctx（DeadlineExceeded→timeout / Canceled→cancelled），其次看 `res.Err`/`ExitCode`。worker 推回的 `result.status` 经 `workerRunner.Run` 转 `runner.Result{ExitCode, Err}`（参考 `peerhttp.errFromStatus` `peerhttp/runner.go:251-269` 同款映射），host `classify` 再统一裁决——**与 peer-http 完全一致**，cancel/timeout 分类不在 runner 内决定。 |

> 一致性提示：worker 与 host 必须用**同一 `timeout_sec`**（`dispatch` 帧透传 host `req.TimeoutSec`），否则两侧超时窗口错位会导致「host 判 timeout 但 worker 仍在跑」的窗口拉长。主文档 §5 `dispatch` 已含 `timeout_sec`，P1 应已透传；P2 e2e 用例需覆盖「worker 超时先到」与「host ctx 兜底先到」两种。

---

## 6. 测试与验收

构建/测试环境：`export PATH=/d/work/inhere/linux-env/sdk/gosdk/go1.25.10/bin:$PATH; cd tools/gofer`

### 6.1 单元测试

| 用例 | 文件 | 断言 |
|---|---|---|
| sink 桥接：open→Open 调用 | `internal/runner/worker/runner_test.go` | 入站 `interaction{open}` → `req.Interactions.Open` 被调一次（mock sink 记录） |
| sink 桥接：answer 回程 | 同上 | `ansCh<-answer` → mock hub 收到 `answer{iid,answer}` |
| sink 桥接：job 提前结束 | 同上 | `ansCh` 关闭无值（ctx done）→ **不发** `answer`（防止向已死 job 回程） |
| sink 桥接：重复 open 幂等 | 同上 | 同一 id 二次 open → `Open` 仅生效一次（`seen` 去重） |
| worker：收 answer | `internal/worker/interaction_test.go` | 调本地 `AnswerInteraction(jobID,iid,ans)` |
| worker：收 cancel | `internal/worker/cancel_test.go` | 调本地 `Cancel(jobID)` 且随后推 `result{status:cancelled}` |
| worker：本地超时 | 同上 | 长 job 超时 → 推 `result{status:timeout}` |

### 6.2 e2e（loopback hub+worker，参考 `internal/httpapi/e2e_interaction_test.go`）

1. **交互透传闭环**：worker job 触发交互 → `GET /v1/jobs/{id}/interactions` 返回该交互且 job `status=pending_interaction` → `POST /v1/jobs/{id}/interactions/{iid}/answer` → worker job 续跑 → `Wait`/轮询至 `status=done`。
2. **cancel 在飞 worker job**：提交长 worker job → `POST /v1/jobs/{id}/cancel` → 终态 `cancelled`；对 **已 done** 的 worker job 再 cancel → 200 + stable no-op（终态不变）；对未知 id cancel → 404。
3. **timeout**：提交超 `timeout_sec` 的 worker job → 终态 `timeout`，`classify` 分类正确（不是 `failed`/`cancelled`）。

### 6.3 并发/竞态

- `go test -race ./internal/runner/worker/... ./internal/worker/... ./internal/wshub/...` —— 重点覆盖交互桥接的 goroutine（`Open` 内的 `WaitAnswer` goroutine + runner 内的 `<-ansCh` goroutine + hub demux 写帧），确认无数据竞争、无 `ansCh`/`answered` channel 误用。
- hub 入站帧**按 job 保序**（主文档 §9 约束 #2）：`interaction` 帧不得与同 job 的 `log`/`result` 帧乱序；e2e 断言 `result` 后不再有该 job 的 `interaction` 注入（注入 terminal job 由 `injectInteraction` 拦截，`remote_interaction.go:70-74`）。

### 6.4 验收清单（对齐主文档 §3 P2 行）

- [ ] worker job 触发交互在 hub 显示 `pending_interaction`（现有 GET interactions API 不改）
- [ ] hub 作答经 WS 回流，worker job 续跑至完成
- [ ] cancel worker job → `cancelled`；已终态 worker job cancel = stable no-op；未知 id = 404
- [ ] 长 worker job 超时 → `timeout`（分类正确）
- [ ] `-race` 通过（交互桥接并发）
- [ ] `go vet ./...` + `go build ./...` 绿灯
- [ ] 主文档 §10 进度勾选 P2 + 回填提交哈希（SR1201/SR1202）

---

## 7. 风险与边界

1. **worker 本地交互观察方式**（§4 worker 改动 a）：worker 客户端如何感知本地 job 起了交互，取决于本地 `job.Service` 暴露的交互观察接口。优先复用本地 SSE `pumpInteractions`（`stream_handler.go:196`）或 `GetPersistedInteractions` 轮询；若 P1 未在 worker 侧建本地读路径，T3 需先确认本地交互可被客户端观测（最小：worker 进程内直接订阅本地 `job.Service`，无需起本地 HTTP）。**实施前 T3 第一步先核实此接口**。
2. **保序铁律**：hub 对 `interaction` 帧的 demux 必须与 `log`/`result` 同一**单读循环/per-job 串行队列**（主文档 §9 #2「不可每帧 goroutine」），否则 `result` 可能抢在 `interaction{open}` 前到达、host job 已 finish 导致 `injectInteraction` 被拒（`ErrJobTerminal`）。`Open` 内部的 `WaitAnswer` goroutine 是**可以**异步的（它只等答案、不影响帧顺序）。
3. **answer 帧目标 worker 离线**：host 作答时若 worker 已断（P3 范畴），`hub.Answer` 发送失败 → host 交互已 answered（host 记录权威），但 worker job 收不到回程 → worker 本地 job 由其本地 ctx/超时收尾。host 侧此时应已通过 P3 的 worker-lost 把在飞 job 置 `failed`。**P2 不实现，§7.4 标注**。
4. **pending_interaction 期间 worker 断线（边界，详细处理移交 P3）**：worker job 处于本地 pending、host job 处于 `pending_interaction` 时 worker 断线 → host 的 `remoteInteractionSink.Open` goroutine 仍阻塞 `WaitAnswer`（不会泄漏：host ctx 取消或 job finish 时 `WaitAnswer` 经 ctx.Done 返回、`ch` 关闭无值，`remote_interaction.go:36-42`）。host job 最终态由 P3 worker-lost 策略（置 `failed`，设计 §8.5 MVP）裁决。**P2 仅保证不泄漏 goroutine / 不死锁；终态裁决归 P3。**

---

## 8. 与主文档一致性核对

- 帧契约（§5）：`cancel`(s→w, job_id)、`interaction`(w→s, job_id/action/interaction)、`answer`(s→w, job_id/interaction_id/answer) —— 本文一致。
- 「无需改 `AnswerInteraction`」（§4 集成钩子末条 + 设计 §10 #3）—— 本文 §3 逐条验证落实。
- cancel/timeout 复用现有 ctx + `classify`（设计 §8.4）—— 本文 §5.2/§5.3 一致。
- worker-lost / 重连移交 P3（主文档 §3 分期、设计 §8.5）—— 本文 §2/§7.4 一致。

**未发现与主文档冲突。** 一处需 P1 落地后回填确认：主文档 §4 提到 remote 分支「新增 `isWorkerRunner`/合并 `isRemoteRunner`」——本文 §4 T1 按「P1 若已合并则 job 包 0 改动」处理，P2 起手时核对 P1 实际实现即可，非冲突。
