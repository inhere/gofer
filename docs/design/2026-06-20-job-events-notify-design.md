# Gofer job 事件时间线 + 通知外发 — 设计方案

> 一句话：给每个 job 落一条 **append-only 生命周期事件流**（提交→派发→状态变更→交互→取消→终态），详情页展示"这个 job 一路发生了什么"（E13）；并在 `done/failed/pending_interaction` 等关键事件时**主动 webhook 外发**，把人/系统拉进来——别让卡住或失败的 agent 无人知（E14）。
> 合并 roadmap [`../2026-06-20-enhancements-roadmap.md`](../2026-06-20-enhancements-roadmap.md) 的 **E13 事件时间线 + E14 通知外发**（B2 批次，强内聚：**共用同一 job 生命周期事件发射点**——E13 落流、E14 在同样转换点触发投递）。承接「产出与审计」(`example-project-dhk`) 与「CLI 易用+tags」(`example-project-4a0`)。bd epic 待建。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-20 | Claude | 初版：事件模型 + 中央 recordEvent + 投递 sweeper + §10 决策。 |

## 1. 概览

### 1.1 背景与缺口（事实，带 file:line）
- **无过程留痕**：job 现只有**终态字段**（status/exit_code/ended_at），看不到"caller X 提交 → 派发 worker Y → queued→running → 交互问答 → 取消/终态"的**过程**。SSE 流（`stream_handler.go`）是 **250ms 轮询** logs/interactions/status 现态、**非持久事件流**，关掉就没了。
- **状态常量**：`internal/job/model.go:104-116`（queued/running/done/failed/cancelled/timeout/pending_interaction）。
- **转换点散落**：Submit 建 queued（`service.go:155-322`）、queued→running（`service.go:357-374`）、finish 终态（`service.go:415-441`）、cancel（`cancel.go:37-60`）、交互建/答（`interaction.go:88-148`/`:235-288`）、远端派发 Forward（`service.go:232-248`）——**无统一记录点**。
- **无主动外发**：全仓**无 webhook/callback**；出站 HTTP 仅 `client.go`（30s 超时无重试）/ `peerhttp`（调 peer）。job 卡在 `pending_interaction` 或 `failed` 时**无人知**。
- **MQ 不可行**：gofer `go.mod` 无 amqp；`golang-ext/amqp` 跨模块不可 import（无 go.work、独立 module）；gofer 是**独立本地 dev 工具**——故 E14 **只走 webhook**，MQ 留给上层平台/消费方（§10-D6）。

### 1.2 目标
| 编号 | 目标 | 轴 |
|---|---|---|
| G-E13 | job 生命周期事件 **append-only 落库** + `GET /v1/jobs/{id}/events` + 详情时间线 | 审计 |
| G-E14 | 关键事件（done/failed/pending_interaction…）**webhook 外发**，可靠投递（重试+退避） | 审计 / 协同 |

### 1.3 非目标
- **不做** MQ 外发（gofer 无 MQ 条件，留上层；§10-D6）。
- **不做** 远端执行机**内部**事件深度镜像：host 记录 host 视角的生命周期（submitted/dispatched/host 所见 status/terminal）即可；worker/peer 自身细粒度事件留后续。
- **不做** 事件驱动重构 SSE（仍轮询，E13 事件作为新一路轮询对象）。
- webhook **不做** 双向（gofer→外部单向通知）；消费方回执不纳入。

## 2. 名词
- **事件 (event)**：job 生命周期某一刻的不可变记录 `{seq, job_id, type, detail, at}`。append-only，绝不更新。
- **事件类型 (type)**：`job.submitted` / `job.dispatched` / `job.running` / `job.terminal` / `job.cancelled` / `interaction.created` / `interaction.answered`（§5.2 全集）。
- **投递 (delivery)**：一个事件向一个 webhook 目标的一次外发尝试记录（含状态/尝试数/下次重试）。
- **触发事件 (trigger)**：被某 webhook 订阅、会产生投递的事件类型子集（默认 `job.terminal`(failed) / `interaction.created`）。
- **投递 sweeper**：周期扫描"待投/到期重试"的投递并 POST 的后台 loop（仿 `startPruneLoop`）。

## 3. 范围与分期

| 阶段 | 内容 | 依赖 | 风险 |
|---|---|---|---|
| **P1** | **E13 事件流**：`job_events` 表 + 中央 `recordEvent` 插桩 + `GET /v1/jobs/{id}/events` + SSE `event` 帧 + Web 时间线面板 | 无 | 低-中 |
| **P2** | **E14 webhook 外发**：`NotificationConfig` + `event_deliveries` 表 + 投递 sweeper（退避）+ URL 白名单/HMAC 签名 + 状态可见 | P1 | 中 |

> **顺序**：P1（建事件流地基）→ P2（消费事件做投递）。每阶段绿灯即提交（SR1202）。local-only（webhook 是 gofer 主动出站）。

## 4. 架构与关键改动

统一收敛到 **生命周期转换点 → recordEvent → job_events 表**，E14 在其上加投递：

```txt
Submit / queued→running / finish / cancel / interaction 建·答 / 远端 dispatch
        │  (各转换点插一行 recordEvent，best-effort 不影响 job 终态)
        ▼
   recordEvent(jobID, type, detail) ──INSERT──▶ job_events(seq,job_id,type,detail_json,at)   ← 新表(append-only)
        │                                              │
        │                                  ┌───────────┴── GET /v1/jobs/{id}/events  (E13)
        │                                  ├───────────── SSE pumpEvents() 轮询 → `event` 帧 (E13)
        │                                  └───────────── Web JobDetail「事件时间线」面板 (E13)
        ▼ (E14, P2)
   匹配 NotificationConfig.trigger 的事件 ──▶ 为每个订阅 webhook 入 event_deliveries(pending)  ← 新表
        ▼
   投递 sweeper(ticker,仿 startPruneLoop) ── claim 到期投递 ──▶ POST webhook(HMAC签名,超时,白名单)
        │   2xx → delivered；非2xx/网络 → 退避重试(30s→2m→5m→15m→60m)；超上限 → failed
        └── 详情/状态暴露投递结果
```

**改动面**：
- 低-中：P1 加 `job_events` 表（schemaStmts，仿 interactions）+ `recordEvent` + 5~7 处插桩 + events handler + SSE `pumpEvents` + Web 面板。
- 中：P2 加 `NotificationConfig`（config）+ `event_deliveries` 表 + 投递 sweeper（serve.go 挂载，仿 `startPruneLoop:139`）+ webhook 客户端（HMAC/超时/白名单）+ 投递状态暴露。

## 5. 模块详设

### 5.1 中央事件记录（P1 公共地基）
`internal/job/` 加 `recordEvent(jobID, eventType string, detail any)`：marshal detail（小，截断上限如 8KB）→ `s.meta.InsertJobEvent(...)`（INSERT-only）。**best-effort**：失败仅 warning，绝不改 job 终态（呼应 captureOutcomes 既有铁律）。在**非 HTTP 上下文**（execute 协程）调用，无需 HeaderUtil（gofer 非公司服务，SR509 不适用）。

### 5.2 事件类型与插桩点（P1）
| 事件 type | 插桩点（file:line） | detail（小 JSON） |
|---|---|---|
| `job.submitted` | `service.go` Submit 持久化 queued 后(:300 附近) | `{project,agent,runner,caller_id,tags}` |
| `job.dispatched` | `service.go` 远端 Forward 准备后(:232-248) | `{runner,worker_id}`（仅 remote） |
| `job.running` | `service.go` queued→running(:368-371) | `{}` |
| `job.terminal` | `service.go` finish 设终态(:415-424) | `{status,exit_code,error?}` |
| `job.cancelled` | `cancel.go` cancel 发信号(:56-58) | `{was_terminal}` |
| `interaction.created` | `interaction.go` CreateInteraction(:129-144) | `{interaction_id,type,prompt}` |
| `interaction.answered` | `interaction.go` AnswerInteraction(:261-279) | `{interaction_id,answer}` |
> `job.terminal` 覆盖 done/failed/timeout（detail.status 区分）；cancelled 经 `job.cancelled` + 随后 `job.terminal(status=cancelled)`。每个 detail 不含 secret（SR403）。

### 5.3 数据模型 — job_events（P1）
新表（`jobstore/store.go` schemaStmts，仿 `interactions` 第二张表先例 `:84-95`）：
```sql
CREATE TABLE IF NOT EXISTS job_events (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,  -- 全局单调递增，天然有序
  job_id     TEXT    NOT NULL,
  type       TEXT    NOT NULL,
  detail_json TEXT,
  at         INTEGER NOT NULL                    -- 事件时间(unix 秒)
);
CREATE INDEX IF NOT EXISTS idx_job_events_job ON job_events(job_id, seq);
```
- DAO：`InsertJobEvent(e)` (INSERT only) + `ListJobEvents(jobID, sinceSeq)` (按 seq ASC)。**append-only，无 upsert**（区别于 jobs/interactions 的快照覆盖）。retention：prune job 时一并删其 events（`prune.go` 扩一句 DELETE）。

### 5.4 E13 API + SSE + Web（P1）
- `GET /v1/jobs/{id}/events[?since=<seq>]`：返回 `{events:[{seq,type,detail,at}]}`（仿 `handleListArtifacts`/`serveLog` 取 job 风格，404 if !ok）。
- **SSE**：`stream_handler.go` 加 `pumpEvents()`（与 `pumpLogs`/`pumpInteractions` 平行 250ms 轮询），按 `sinceSeq` 增量发 `event:event` 帧 `{seq,type,detail,at}`；初始回放现有事件。
- **Web**：`JobDetail.vue` 在 interactions(:506-538) 与 outcomes(:541) 之间加「事件时间线」面板（按 seq 升序，每事件一行：图标+type+关键 detail+相对时间）；SSE `event` 帧 append；`api/types.ts` 加 `JobEvent` 类型 + `client.ts` `listEvents(id)`。

### 5.5 E14 通知配置（P2）
`ServerConfig` 加 `Notification`（全局），`ProjectConfig` 加 `notify_enabled *bool`（项目级开关，nil=默认开）：
```yaml
server:
  notification:
    webhooks:
      - url: https://hooks.example.com/gofer        # 出站目标（白名单校验）
        events: [job.terminal, interaction.created]  # 订阅的触发事件(omit=默认集)
        secret_env: GOFER_WEBHOOK_SECRET             # HMAC 密钥的 env 名(SR403,不入库)
        projects: [proj-a]                            # 可选：仅这些项目(omit=全部)
    allow_hosts: [hooks.example.com]                  # URL 白名单(host)，默认拒内网/loopback
    retry: { max_attempts: 6 }                        # 退避表固定 30s→2m→5m→15m→60m
```
- secret 走 `secret_env`（`os.Getenv`，仿 `token_env` 先例 `model.go:30/88`），**不入库不入日志**。

### 5.6 E14 投递模型 + sweeper（P2）
**入队**：`recordEvent` 后（或事件落库后由投递入队步），对每个**订阅该 type 且项目匹配**的 webhook，插一行 `event_deliveries(pending)`：
```sql
CREATE TABLE IF NOT EXISTS event_deliveries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_seq INTEGER NOT NULL,      -- 关联 job_events.seq
  job_id TEXT NOT NULL,
  target TEXT NOT NULL,            -- webhook url
  status TEXT NOT NULL,            -- pending / delivered / failed
  attempts INTEGER NOT NULL DEFAULT 0,
  next_retry_at INTEGER NOT NULL,  -- 到期可投时间(unix 秒)
  last_error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_deliveries_due ON event_deliveries(status, next_retry_at);
```
**sweeper**（`serve.go` 仿 `startPruneLoop:139` 挂 ticker，如 15s）：条件领取 `status='pending' AND next_retry_at<=now`（**`UPDATE...WHERE` 抢占式**，呼应 SR303，避免多次重复投递）→ POST webhook → `2xx` 置 `delivered`；非 2xx/网络/超时 → `attempts++`、按退避表（**30s→2m→5m→15m→60m**，SR606 同款）置 `next_retry_at`，超 `max_attempts` 置 `failed`。投递子进程带超时（如 5s）。
- **webhook 请求**：`POST application/json`，body `{event:{seq,job_id,type,detail,at}, job:{id,status,project,...摘要}}`；header `X-Gofer-Event: <type>`、`X-Gofer-Signature: sha256=<hmac(body, secret)>`（secret 存在时）。

### 5.7 E14 安全（P2，最大新面）
- **URL 白名单 + SSRF**：webhook host 必须在 `allow_hosts`；默认**拒 loopback/内网保留段**（仿 SR904 口径）；仅 `https`（可配 `allow_http` 放开本地测试）；投递超时硬上限。复用 `safeJoinUnder` 之外另写 `validateWebhookURL`（这是 E14 最大对外出站面）。
- HMAC 签名让消费方验真；secret 仅 env、不入库不回显。

## 6. 数据模型
两张新表（均 schemaStmts IF NOT EXISTS，无破坏迁移）：`job_events`（append-only，§5.3）、`event_deliveries`（投递状态机，§5.6）。jobs 表**不加列**。retention 扩展：prune job 时连带删其 `job_events` + `event_deliveries`。

## 7. API
| 方法 | 路径 | 变更 | 说明 |
|---|---|---|---|
| GET | `/v1/jobs/{id}/events` | 新 | `?since=<seq>` 增量；事件时间线数据 |
| GET | `/v1/jobs/{id}` | 不变 | 不内联 events（量大，走专门端点 + SSE） |
| (SSE) | `/v1/jobs/{id}/stream` | 改 | 加 `event:event` 帧（与 log/status/interaction 并行） |
- （可选）`GET /v1/jobs/{id}/deliveries`：投递状态查询（E14 可见性）；§10-D 决定是否本期带。

## 8. 安全
- webhook 出站是本设计**最大新面**：白名单 host + 拒内网/loopback + https-only + 超时 + 不跟 3xx（仿 SR904）。
- secret（HMAC）走 `secret_env`、不入库不入日志（SR403/805）；事件 detail 不含 secret（仅 id/状态/prompt 摘要）。
- events / deliveries 在 `/v1` 鉴权内；投递 sweeper 抢占用 `UPDATE...WHERE`（SR303）防重复投递。

## 9. 部署
无新部署面：两张 additive 表 + serve 内多挂一个 sweeper goroutine（仿 prune/probe/reload loop）+ 既有 HTTP。webhook 配置经 config + env secret。

## 10. 待确认事项（决策点，附推荐）
- **D1（事件持久化）**：新 `job_events` append-only 表（autoincrement seq、INSERT-only）——认可？（推荐：是，仿 interactions 第二表先例）
- **D2（事件全集）**：§5.2 七类（submitted/dispatched/running/terminal/cancelled/interaction.created/answered）——够用？（推荐：是；detail 小、不含 secret）
- **D3（SSE 集成）**：`stream_handler` 加 `pumpEvents()` 轮询发 `event` 帧，**不引 pub-sub 总线**（推荐：是，与现有轮询一致）
- **D4（E14 投递可靠性）**：**durable `event_deliveries` 表 + 退避 sweeper**（仿 startPruneLoop，SR606 退避、SR303 抢占）vs 纯 best-effort fire-and-forget？（推荐：durable——"别让失败无人知"要求可靠投递，sweeper 成本低）
- **D5（E14 触发集）**：默认 `job.terminal(failed)` + `interaction.created`（最需人关注）；每 webhook 可配 `events` 覆盖（推荐：是）
- **D6（E14 传输）**：**仅 webhook HTTP POST**，MQ 超出 gofer 范围（无 amqp/跨模块/独立工具）——MQ 留上层（推荐：是，硬约束）
- **D7（E14 安全）**：webhook **URL 白名单 + 拒内网/loopback + https-only + HMAC(secret_env) + 超时**（推荐：是，最大新面）
- **D8（config 落点）**：全局 `server.notification.webhooks[]` + 项目级 `notify_enabled` 开关（推荐：是）
- **D9（投递可见性）**：本期是否带 `GET /v1/jobs/{id}/deliveries` + Web 投递状态（推荐：带只读查询，低成本，便于排障）
- **D10（bd）**：一个 epic 两阶段（E13→E14）（推荐：是）

## 11. 结论
E13/E14 共用**生命周期转换点的中央 `recordEvent`** + `job_events` append-only 表；E13 = 事件流 API/SSE/Web 时间线，E14 = 在其上加 `event_deliveries` + 退避投递 sweeper（仿现成 `startPruneLoop`）。最大复用：interactions 第二表先例、SSE 轮询范式、prune-loop sweeper 范式、`token_env` secret 范式、SR303/SR606/SR904 既有约定。最大新面是 webhook 出站（白名单+SSRF+HMAC）。MQ 经事实判定**排除**（gofer 独立无 amqp 条件）。

**下一步**：审核（重点过 §10 决策，尤其 D4 投递可靠性 / D6 仅 webhook / D7 安全）→ 通过后出分阶段 `plan`（P1–P2，细到表/钩子/handler/sweeper/Web 与验收），再按 SUPMODE 实施。
