# P2 — E14 通知外发（webhook）（实施计划）

> 主纲：[`2026-06-20-job-events-notify-plan.md`](./2026-06-20-job-events-notify-plan.md) · 设计 §5.5–5.7/§6/§8。
> 关键事件触发 **webhook 外发**，durable 投递（退避重试 + 抢占 sweeper）+ URL 白名单/HMAC。依赖 P1（事件流）。**仅 webhook，MQ 排除（D6）**。

---

## P2-a NotificationConfig + event_deliveries 表 + DAO

### 落点
- `internal/config/model.go`：`ServerConfig` 加 `Notification *NotificationConfig`（yaml `notification`）；`ProjectConfig` 加 `NotifyEnabled *bool`（yaml `notify_enabled`，nil=默认开）。
- `internal/jobstore/store.go`：`schemaStmts` 加 `event_deliveries` 表 + 索引（IF NOT EXISTS）。
- `internal/jobstore/deliveries.go`（新）：DAO（入队/抢占领取/更新）。

### 步骤
**1) config**：
```go
type NotificationConfig struct {
    Webhooks   []WebhookConfig `yaml:"webhooks"`
    AllowHosts []string        `yaml:"allow_hosts"` // 出站白名单 host
    AllowHTTP  bool            `yaml:"allow_http"`  // 默认 false=仅 https（本地测试可开）
    MaxAttempts int            `yaml:"max_attempts"` // 默认 6（退避表长度）
}
type WebhookConfig struct {
    URL       string   `yaml:"url"`
    Events    []string `yaml:"events"`     // 订阅触发事件(omit=默认集 job.terminal+interaction.created)
    SecretEnv string   `yaml:"secret_env"` // HMAC 密钥 env 名(SR403,不入库)
    Projects  []string `yaml:"projects"`   // 仅这些项目(omit=全部)
}
```
**2) 表**（store.go schemaStmts，按设计 §5.6）：`event_deliveries(id PK auto, event_seq, job_id, target, status, attempts, next_retry_at, last_error, created_at, updated_at)` + `idx_deliveries_due(status, next_retry_at)`。
**3) DAO**（`jobstore/deliveries.go`）：
- `InsertDelivery(d)`（pending 入队）。
- `ClaimDueDeliveries(now, limit) ([]Delivery, error)`：**`UPDATE...WHERE status='pending' AND next_retry_at<=? ...`**（SR303 抢占，置一个 claimed 标记或直接取出待投——SQLite 无 RETURNING 老版本则先 SELECT id 再条件 UPDATE，确保单投递只被一个 sweep 处理）。
- `MarkDelivered(id)` / `MarkRetry(id, attempts, nextRetryAt, lastErr)` / `MarkFailed(id, lastErr)`。
- `ListDeliveriesByJob(jobID)`（P2-d 可见性）。

### P2-a 验收
- 单测 `internal/config`：解析含 `notification.webhooks/allow_hosts` 的 yaml 正确映射。
- 单测 `internal/jobstore`：新库含 `event_deliveries` 表；InsertDelivery→ClaimDue 取到到期件、未到期不取；MarkDelivered/Retry/Failed 状态流转正确；并发 claim 不重复领取（条件 UPDATE 语义）。

---

## P2-b 投递入队 + webhook 客户端（HMAC/超时/白名单）

### 落点
- `internal/job/events.go`（或新 `notify.go`）：`recordEvent` 落库后，按 `NotificationConfig` 匹配订阅 webhook → `InsertDelivery(pending, next_retry_at=now)`。
- `internal/notify/`（新包）或 `internal/job/`：`validateWebhookURL` + `postWebhook`（HMAC + 超时）。

### 步骤
**1) 入队**：`recordEvent` 末尾（事件已落库、拿到 seq）→ 取 `s.config().Server.Notification`，对每个 `webhook` 判 `项目匹配 && 事件 type ∈ webhook.Events(或默认集) && project.NotifyEnabled!=false` → `InsertDelivery{event_seq, job_id, target:webhook.URL, status:pending, next_retry_at:now}`。
> 入队也 best-effort（失败仅 warning，不影响 job）。需要 event 的 seq —— `InsertJobEvent` 返回 `seq`（lastInsertId），recordEvent 接住后入队。
**2) webhook 客户端**：
```go
// validateWebhookURL：仅 https(除非 AllowHTTP)；host ∈ AllowHosts；解析 IP 拒 loopback/私网保留段(SR904)；
func validateWebhookURL(raw string, cfg NotificationConfig) error
// postWebhook：POST application/json，body {event:{seq,job_id,type,detail,at}, job:{id,status,project,...摘要}}；
// header X-Gofer-Event:<type>、X-Gofer-Signature:sha256=<hmac(body,secret)>(secret 存在时)；
// ctx 超时(如 5s)、不跟 3xx；2xx→nil，否则 err。
func postWebhook(ctx, target string, body []byte, secretEnv string, cfg NotificationConfig) error
```
- secret：`os.Getenv(webhook.SecretEnv)`（不入库不日志）。HMAC-SHA256(body, secret)。

### P2-b 验收
- 单测 `validateWebhookURL`：`https://hooks.example.com`(在白名单)合法；`http://...`(AllowHTTP=false)拒；`https://127.0.0.1`/内网 IP/非白名单 host 拒。
- 单测 `postWebhook`（httptest）：2xx→成功、签名头 `X-Gofer-Signature` 正确（HMAC 可复算）、非 2xx→err、超时→err。
- 单测：含 webhook 配置时 `job.terminal` 事件入队 deliveries；`notify_enabled:false` 项目不入队。

---

## P2-c 投递 sweeper（serve 挂载 + 退避）

### 落点
- `internal/commands/serve.go`：仿 `startPruneLoop(:139)` 加 `startDeliveryLoop(...)`，serve 启动序列挂载（prune loop 附近 `:92`）。
- 投递处理逻辑（`internal/notify/` 或 `internal/job/`）：`deliverDue()`。

### 步骤
**1) sweeper**（ticker，如 15s，仿 startPruneLoop 的 `time.NewTicker`+`select{stop|tick}`，启动跑一次）：
```go
// deliverDue：ClaimDueDeliveries → 逐件 postWebhook：
//   2xx → MarkDelivered；
//   非2xx/网络/超时 → attempts++，按退避表 backoff[min(attempts-1,len-1)] 置 next_retry_at，MarkRetry；
//     attempts >= MaxAttempts → MarkFailed。
var backoff = []time.Duration{30*time.Second, 2*time.Minute, 5*time.Minute, 15*time.Minute, 60*time.Minute} // SR606
```
- 仅当 `cfg.Server.Notification != nil && len(webhooks)>0` 才启动 loop（无配置不启，回归同旧）。
- 优雅停机：`stop <-chan struct{}`（仿 prune loop 的 defer close）。
**2) 装配**：serve.go 在 buildCore 后、prune loop 旁加 `go startDeliveryLoop(jobs, cfg.Server.Notification, stop)`。

### P2-c 验收
- 单测/集成：投递成功路径（fake webhook 200）→ MarkDelivered；失败路径（500/超时）→ attempts 递增 + next_retry_at 按退避表推进；超 MaxAttempts → failed。
- 单测：抢占——两个并发 sweep 不重复投同一件。
- 回归：无 notification 配置 → loop 不启动、serve 正常。

---

## P2-d 投递可见性 + example/config

### 落点
- `internal/httpapi/`：`GET /v1/jobs/{id}/deliveries`（`handleListDeliveries`，仿 events handler）。
- `web/`：JobDetail 投递状态（事件时间线旁或事件行内标注投递结果）；types/client 加 `listDeliveries`。
- `config/gofer.example.yaml`：补 `server.notification` 段（webhooks/allow_hosts/secret_env，带注释 + 安全说明）。

### 步骤
- handler：取 job(404)→`c.JSON(200, {"deliveries": ListDeliveriesByJob(id)})`。路由注册。
- Web：时间线事件行若有投递，显示徽标（delivered/retry n/failed）；或独立"投递"小列表。`api/types.ts` `Delivery` 类型 + `client.listDeliveries`。
- example：`notification` 段示例（含 `secret_env` 占位、`allow_hosts`、注释"webhook 出站白名单 + HMAC 验真"）。

### P2-d 验收
- 单测 httpapi：`GET /v1/jobs/{id}/deliveries` 返投递列表；未知 id→404。
- `pnpm -C web build` 绿；真机：详情页见投递状态。
- example 经 `config.Load` 解析不报错（接 P3 例可解析测试同款，防漂移）。

### 提交点
P2-a / P2-b / P2-c / P2-d 各绿灯分别 `git commit`；更新主纲进度全勾 + 出**完成报告**（SR1430）。**P2 触碰 serve 装配 + 出站 HTTP，需全量 `go test ./...` + 真机冒烟（本地 webhook 接收器验投递+退避+签名）**。

> 范围注记（D6）：仅 webhook，**不接 MQ**（gofer 无 amqp 条件）。投递可靠性靠 durable `event_deliveries` + 退避 sweeper（D4）。最大对外安全面是 webhook 出站（白名单+SSRF+HMAC，D7）。
