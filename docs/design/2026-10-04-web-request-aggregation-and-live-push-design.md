<!-- template_id: design; template_version: 1.1.1 -->
# Web 请求聚合 + 全局推送（Q 批）

> 状态：Approved（Draft 0.1；用户 2026-10-04 确认，派 Sonnet 实施）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-04 | Claude | 详情聚合 `?include=`、浏览器 `/v1/ws` 推送、轮询降为兜底 |

## 背景

打开一个 job 详情页会发出 16 个请求（用户截图）：

- 全局组件：`EscalationBell` 每 15s 拉 `/v1/interactions`、`/v1/decisions`，约 60s 拉一次 `/v1/stats`。
- `JobDetail` 一次性并发：`/v1/agents`、`/v1/meta`、`/v1/jobs/{id}`、`/v1/jobs?session=`、`/artifacts`、`/events`、`/deliveries`、`/pty/sessions`、`/wakeups`、`/retries`、`/comments`（CommentThread）、两路日志。

除此之外，各列表页都在 2.5–5s 定时轮询：Board、Plans、Workflows、Schedules、Runners、Agents、Sessions、Dashboard、Config、Tunnels、Workbench、SessionDrawer。浏览器与服务端走 HTTP/1.1（没有 h2），每个 origin 最多 6 个连接，十几个并发请求要排队。

另有三个现成问题：

- 非终态 job 的 SSE 会重放全部 events，`/events` 请求因此重复。
- SSE 服务端每 250ms 查一次 DB，是轮询，不是事件驱动。
- 顶栏的"连接状态点"只看本地有没有 token，并不反映真实连接。

## 现有可复用基础（2026-10-04 调研）

- 统一事件源：`job.Service.AddEventObserver`（`internal/job/events.go`）。所有 `recordEvent` 都会回调它，webpush 已在用。job 快照的唯一落盘咽喉是 `Service.persist → jobstore.UpsertJob`。
- 浏览器 WS 鉴权模式：attach 用的 `POST …/attach-ticket`（Bearer）换 30s 一次性 ticket，再 `wss://…?ticket=`。库是 `coder/websocket`，Origin 校验沿用 `governance.attach_origins`。
- 前端的 `utils/poller.ts` `createPoller` 已处理页面隐藏、失焦时暂停，恢复时立即拉取。
- 缺口：
  - decisions 与 sessions 的写入不走事件总线。
  - decisions 是读时懒过期，没有主动通知。
  - interaction 的升级、需人工标记，以及 `job.recovering` 都没有事件。
  - wshub 的 worker 上下线没有订阅接口。

## 方案

### Q1 job 详情聚合

- `GET /v1/jobs/{id}?include=events,comments,deliveries,retries,wakeups,pty_sessions,artifacts,session_jobs`
  - 缺省 include 为空，现有调用方（CLI、MCP）零影响。
  - 每项独立容错：失败写入 `include_errors`，整体仍返回 200。
- 体积上限：
  - events：默认最近 200 条，返回 `last_seq` 和 `truncated`，更早的用 `?before=` 分页拉。
  - comments：上限 100。
  - artifacts：只在终态 job 内联，上限 200，并给出 `total`。运行中的 job 不内联（要扫目录）。
  - session_jobs：上限 50。
  - deliveries、retries、wakeups 数量少，全量返回。
  - pty_sessions 保留原有的 attach 权限判断：无权限时省略该字段，并在 `include_errors` 里标 `forbidden`。
- 不聚合：日志正文、diff 全文、`/request`。
- 前端：
  - 详情页改为一次聚合请求，再加日志（终态走分页，运行中走 SSE）。
  - 运行中的 job 不再单独请 `/events`（SSE 已经重放）。
  - CommentThread 接受外部传入的初值。
  - `/v1/agents`、`/v1/meta` 改为前端模块级缓存，NewJob、Workbench、Agents 共用；收到推送的 `meta` 失效通知时再刷新。
- 目标：打开详情页的请求从 16 个降到 3 个以内：聚合请求、日志或 SSE，以及首次 ws-ticket。

### Q2 浏览器全局推送 `/v1/ws`

- 鉴权：
  - 用 `POST /v1/ws-ticket`（Bearer，在 auth 组内；job 凭据、worker token 均拒绝）换取 30s 一次性 ticket，复用 attach ticket 存储。
  - `GET /v1/ws?ticket=` 注册在 auth 组外，自行校验 ticket 和 Origin。
  - 每个 caller 最多 16 条连接。
- 协议（JSON 文本帧）：
  - 客户端发：`sub` / `unsub`（topics，可带过滤条件）、`ping`。
  - 服务端发：`hello`（server_time、version）、`snap`（快照）、`evt`（增量）、`inval`（失效，客户端自己用 REST 重拉）、`resync`、`pong`。
  - 服务端每 20s 发一次 WS ping，读超时 60s。
- 主题：

| 主题 | 方式 | 触发源 |
|---|---|---|
| `stats` | 快照；服务端合并节流，最快 2s 一次，有订阅者才计算 | job persist、interaction/decision 变化、schedule 变化 |
| `pending` | 快照（待应答 interactions + OPEN 且未 ack 的 decisions，即铃铛数据） | `interaction.*` 事件、decisions 写入 hook、interaction 升级 hook、decisions 定时过期扫描 |
| `jobs` | `inval`（附变化的 job id 与 status）；列表页收到后用现有 REST 重拉当前页，不在前端合并增量（避免分页、过滤错位） | persist 咽喉 |
| `job:<id>` | 增量：status、event、interaction、comment | observer 按 job 过滤 + persist |
| `sessions` | `inval`，心跳不推，状态与轮次变化才推，节流 2s | jobstore session 写入 hook（排除 Touch） |
| `runners` | `inval` | wshub 上下线、升级记录变化 |
| `meta` | `inval` | `config.updated`、配置热重载、`agent.degraded/recovered` |
| `plans` / `workflows` / `schedules` | `inval` | `plan.*`、`workflow.*`、`step.*`、`schedule.*` 事件 |

- 服务端结构：
  - 新包 `internal/pushhub`：订阅表 + 每个连接一个有界发送队列（64）+ 节流器。
  - observer、persist hook 里只做非阻塞入队；队列满时丢弃增量，并给该连接发 `resync`。
  - 绝不阻塞 `recordEvent` 和 `persist`。
- 补事件缺口：
  - decisions：Insert、Answer、Ack、Unack、Release、Expire 写入后发 `pending` 通知；新增一个定时扫描，让到期的 decision 主动过期并推送。
  - interaction：升级、需人工标记后发 `pending` 通知。
  - persist 咽喉：job 状态变化（包括 recovering）后发 `jobs`、`job:<id>`、`stats` 通知。
  - wshub：worker 上下线发 `runners` 通知。
- 连上或重连：先发 `hello`，再对每个已订阅主题发 `snap` 或 `inval`。本期不做按 rev 补发：重连就直接拉快照，数据量小、实现可靠。`job:<id>` 用 `since_seq` 精确补发。

### Q3 前端接入与兜底

- `api/live.ts`：单连接管理。
  - 换 ticket、建连、订阅、心跳。
  - 断线指数退避重连：1s 起，上限 30s，加抖动。
  - 401 交给现有的 `triggerUnauthorized` 处理。
- `useLiveTopic(topic, fetcher, opts)`：
  - WS 正常时只用推送数据。
  - WS 断开超过 15s，切回 `createPoller` 低频兜底轮询（30s）；WS 恢复后停掉轮询，并立即拉一次快照对齐。
- 页面隐藏超过 5 分钟主动断开 WS，回到前台再重连并拉快照（与 `createPoller` 现有语义一致）。
- 迁移范围：
  - EscalationBell（stats、pending）、顶栏待验收数、新版本提示（改由 `hello.version` 驱动）。
  - JobDetail（`job:<id>`）、Board、ReviewQueue、Dashboard、Runners、Plans、PlanDetail、Workflows、WorkflowDetail、Schedules、Sessions、SessionDrawer、Workbench、Agents。
  - 各页原有的定时轮询全部降级为断线兜底。
  - ThreadChangesView 补上页面可见性暂停。
- 顶栏状态点改为反映真实 WS 状态：已连接、重连中、已断开（兜底轮询中）。
- 多标签页：每个标签页一条连接。本期不做 SharedWorker 或 BroadcastChannel，原因是 sessionStorage 的 token 按标签页隔离，Safari 和 Android 对 SharedWorker 支持也不稳。

### Q4（可选，视工作量放二期）SSE 去 DB 轮询

`job:<id>` 主题接管 status、event、interaction 之后，`/v1/jobs/{id}/stream` 只保留日志帧，去掉每 250ms 一次的 DB 查询。

## 不做

- 启用 h2（Go 标准库不支持 h2 上的 WebSocket，单独议题）。
- 日志正文进 WS。
- 多 server 实例的 HA 广播。

## 风险

- 反向代理：如果部署前面还有 nginx 一类代理，需放行 Upgrade 头、关闭缓冲，并让空闲超时大于 60s。gofer 自带的 TLS 监听（8768）可以直接用 wss。Vite 开发代理要加 `ws: true`。
- 背压：observer 必须非阻塞。stats 计算较重（有两个各 200ms 的预算），必须合并节流。
- 权限：推送内容不带按 caller 计算的字段（如 `can_attach`、`can_delete`），这类字段由 REST 的 include 提供。

## 测试与验收

- 固定测试：
  - `TestJobDetailInclude`（各项内容、上限、单项失败容错、pty 权限）
  - `TestWSTicketRejectsJobAndWorkerCallers`
  - `TestPushHubNonBlockingAndResync`（队列满时触发 resync，且不阻塞 recordEvent）
  - `TestStatsCoalesced`
  - `TestDecisionChangesPublishPending`
  - `TestDecisionExpirySweepPublishes`
  - `TestPersistPublishesJobs`
  - `TestRunnersInvalOnWorkerConnect`
  - Web Vitest：live 连接、重连、兜底切换；`useLiveTopic`；详情页单请求。
- 真实验收：在临时 serve 上用 agent-browser 打开详情页，统计请求数，目标是不超过 3 个加日志。停留 2 分钟，统计每分钟请求数，目标是只有 ws 帧、没有定时 REST 请求。杀掉 WS 或重启 serve 后，15s 内切到兜底轮询，恢复后自动重连并停止轮询。在 Board 页提交 job，列表 2s 内刷新。铃铛在 interaction 创建、回答时 2s 内变化。
