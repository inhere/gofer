# Gofer job 事件时间线 + 通知外发 — 实施计划（总纲）

> 设计依据：[`../../design/2026-06-20-job-events-notify-design.md`](../../design/2026-06-20-job-events-notify-design.md)（v0.1，§10 决策全按推荐采纳）。
> bd epic：`hyy-ai-inspect-zec`（P1 `hyy-ai-inspect-bol` / P2 `hyy-ai-inspect-e31`）。本文件只保留**总纲 + 进度跟进 + 阶段简述**（SR1105）；阶段详情见子文档。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-20 | Claude | 初版：P1–P2 拆分 + 全局验收门 + 进度跟进骨架。 |

## 已采纳决策（design §10，全按推荐）

- **D1** 事件落 `job_events` **append-only** 表（`seq` autoincrement、INSERT-only，仿 interactions 第二表）。
- **D2** 事件 7 类：`job.submitted`/`job.dispatched`/`job.running`/`job.terminal`/`job.cancelled`/`interaction.created`/`interaction.answered`；detail 小、不含 secret。
- **D3** SSE 加 `pumpEvents()` 轮询发 `event` 帧，**不引 pub-sub 总线**。
- **D4** E14 投递 **durable**：`event_deliveries` 表 + 退避 sweeper（SR606 `30s→2m→5m→15m→60m`、SR303 `UPDATE...WHERE` 抢占）。
- **D5** 默认触发集 `job.terminal`(failed) + `interaction.created`，每 webhook 可配 `events` 覆盖。
- **D6** E14 **仅 webhook HTTP POST**，MQ 排除（gofer 无 amqp/独立工具）。
- **D7** webhook 安全：URL 白名单 + 拒内网/loopback + https-only + HMAC(`secret_env`) + 超时。
- **D8** 全局 `server.notification.webhooks[]` + 项目级 `notify_enabled` 开关。
- **D9** 本期带只读 `GET /v1/jobs/{id}/deliveries` + Web 投递状态。
- **D10** 一个 epic 两阶段（已建）。

## 范围与分期

| 阶段 | 子文档 | 内容 | 依赖 | 风险 |
|---|---|---|---|---|
| **P1** | [`P1-events-plan.md`](./P1-events-plan.md) | **E13 事件流**：`job_events` 表 + `recordEvent` 7 处插桩 + `GET /v1/jobs/{id}/events` + SSE `event` 帧 + Web 时间线面板 | 无 | 低-中 |
| **P2** | [`P2-notify-plan.md`](./P2-notify-plan.md) | **E14 webhook 外发**：`NotificationConfig` + `event_deliveries` 表 + 退避投递 sweeper + URL 白名单/HMAC + 投递可见性 | P1 | 中 |

**顺序**：P1 → P2。每阶段绿灯即 Git 提交（SR1202）。

## 进度跟进

- [x] **P1-a** `job_events` 表（schemaStmts）+ `InsertJobEvent`(返回 seq)/`ListJobEvents` DAO + prune 连带删
- [x] **P1-b** 中央 `recordEvent(jobID,type,detail)`（best-effort）+ 7 处生命周期插桩
- [x] **P1-c** `GET /v1/jobs/{id}/events[?since]` + SSE `pumpEvents()` 发 `event` 帧
- [x] **P1-d** Web JobDetail 事件时间线面板 + `JobEvent` 类型 + `client.listEvents`
- [ ] **P2-a** `NotificationConfig`（config）+ `event_deliveries` 表 + DAO（抢占领取/更新）
- [ ] **P2-b** 投递入队（事件落库后按订阅 webhook 入 pending）+ webhook 客户端（HMAC/超时/白名单 `validateWebhookURL`）
- [ ] **P2-c** 投递 sweeper（serve.go 挂载，退避表）+ 优雅停机
- [ ] **P2-d** 投递可见性 `GET /v1/jobs/{id}/deliveries` + Web 状态 + example/config 注释

## 全局验收门（每阶段收尾必过）

```bash
cd tools/gofer
go build ./... && go vet ./... && go test ./... && gofmt -l internal/ cmd/   # 后端
pnpm -C web build                                                            # 含前端阶段(P1-d/P2-d)
```

- **回归**：现有 job 提交/日志/SSE/list/worker/peer 全绿；两张新表 schemaStmts 自动建、旧库无该表经建表补上；无 `notification` 配置时 P2 sweeper 不启动、行为同旧。
- **真机冒烟**：local 跑一个含交互的 job → `GET /events` 全生命周期事件齐全、详情时间线展示；P2 配一个本地 webhook 接收器，验 `job.terminal` 触发投递 + 失败重试退避 + HMAC 签名头。

## 安全要点（贯穿）

- **webhook 出站是最大新面**：`validateWebhookURL`（白名单 host + 拒 loopback/内网保留段 + https-only + 不跟 3xx + 超时），仿 SR904。
- HMAC secret 走 `secret_env`（`os.Getenv`，不入库不入日志，SR403/805）；事件 detail 不含 secret。
- 投递 sweeper 抢占用 `UPDATE...WHERE status='pending' AND next_retry_at<=now`（SR303）防重复投递。
- `recordEvent` best-effort：任何失败仅 warning，绝不改 job 终态。

## 结论
E13/E14 共用生命周期转换点的中央 `recordEvent` + `job_events` append-only 表；E13=事件流 API/SSE/Web，E14=其上加 `event_deliveries` + 退避投递 sweeper（仿 `startPruneLoop`）。最大复用：interactions 第二表先例、SSE 轮询范式、prune-loop sweeper 范式、`token_env` secret 范式、SR303/606/904 既有约定。最大新面 webhook 出站。本总纲随阶段更新进度与「阶段实施结果」。

## 阶段实施结果

- **P1**（2026-06-20，commit `82fa513`+`3e8c2cb`+`b0a4e53`+`2371eff`+修复 `70cf6b0`）：`job_events` append-only 表(seq autoincrement,仿 interactions 第二表)+ `events.go` DAO(`InsertJobEvent` 返回 seq 供 P2 入队游标 / `ListJobEvents` seq ASC + since 增量)+ prune 连带删；中央 `recordEvent`(best-effort recover,8KB 上限,`eventSink` 接口供失败注入测试)+ 7 处生命周期插桩(submitted/dispatched/running/terminal/cancelled/interaction 建·答)；`GET /v1/jobs/{id}/events[?since]` + SSE `pumpEvents()`(lastEventSeq 游标 + 初始回放 + finish 前补发,并入 250ms 轮询)；Web JobDetail 事件时间线面板 + `JobEvent` 类型 + `listEvents` + SSE `event` 帧。验收门 5/5 PASS。**主控复核抓到并修复一处真实竞争**(`70cf6b0`)：`finish()` 原先 persist 终态后才记 terminal 事件,导致 keyed-on-terminal-status 的读者(waitDone/SSE 关流)在事件落库前读事件→漏 terminal 帧(`TestStreamEventFrames` flaky)+迟到 insert 撞已关闭 DB；改为 finish 顶部先 `recordEvent(terminal)` 再翻状态,x30+全量 x4+`-race` 全绿根治。
