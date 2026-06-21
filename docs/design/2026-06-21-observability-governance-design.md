# Gofer 可观测 + 治理设计（E16 /metrics + E17 per-caller 配额限流）

> 一句话：给 gofer 补上**时序可观测**（Prometheus `/metrics`）与**按 caller 治理**（并发配额 + 速率限流），两者共享同一套 **caller/project/agent/runner 维度**——metrics 按这些维度打标签、限流按 caller 设闸，故合并为一个 epic 实施。
> roadmap [`../2026-06-20-enhancements-roadmap.md`](../2026-06-20-enhancements-roadmap.md) **E16 指标** + **E17 per-caller 配额/限流**。承接已有 `caller_id` 审计字段、per-project 信号量、E13 事件流、周期任务范式。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-06-21 | inhere | 初稿：E16+E17 合并设计，待审核 |
| v0.2 | 2026-06-21 | inhere | §12 决策点**全部按推荐定稿**（D1 client_golang / D2 免认证+可选token / D3 GaugeFunc / D6 并发超额排队 / D7 仅写入类限流 / D8 沿用 project 热加载现状 / D9 仅 per-caller）；进入 plan 阶段 |

## 2. 名词

- **caller**：提交方身份，由 Bearer token 映射（`server.callers[]` / `server.workers` / legacy `default`）；已落 `jobs.caller_id` 审计列。
- **in-flight job**：已提交未到终态的 job，内存快照在 `Service.jobs`（`service.go:100`）。
- **并发配额**：某 caller 同时在跑的 job 上限（信号量排队语义，超额排队不拒绝）。
- **速率限流**：某 caller 单位时间提交请求数上限（令牌桶，超额 `429` 拒绝）。
- **cardinality**：Prometheus 标签维度基数；高基数标签（如 `job_id`）会爆内存，**禁用**。

## 3. 范围

**做（E16）**：Prometheus `/metrics` 端点 + HTTP 请求指标 + job 生命周期指标（提交/终态/时长/在飞/排队）+ worker 连接指标。

**做（E17）**：`CallerConfig` 扩并发/速率字段 + per-caller 并发信号量（复用现有 semaphore）+ per-caller 速率限流中间件（429）+ 热加载兼容 + per-caller metrics 标签。

**不做**：
- **不做** 拉取式以外的 push gateway / OpenTelemetry trace（仅 pull `/metrics`，trace 留后续）。
- **不做** per-agent / per-project 的速率限流（v1 速率仅按 caller；并发配额 project 维度已有，caller 维度本次补）。
- **不做** 分布式限流（单 hub 进程内 limiter；多 hub 共享配额属 C7 大版范畴）。
- **不做** 动态改并发上限即时生效——与现有 project 信号量行为一致（见 §7.3）。
- **不做** 复用 SR102 `{status,code,message}` 信封——gofer 内网工具沿用轻量 `{error,detail}`（`respond.go:7`）。

## 4. 已确认事项（地基现状，不重复造）

| 能力 | 现状挂点 | 复用方式 |
|---|---|---|
| caller 解析 | `authMiddleware`(`auth.go:27`) 存 `ctxCallerID` → `callerFromCtx(c)`(`server.go:43`) | 限流/metrics 中间件直接 `callerFromCtx` 取身份 |
| per-project 并发 | `Service.sems map[string]chan struct{}`(`service.go:104`) + `semaphore(key,limit)`(`service.go:365`) + execute `sem<-`/`defer <-sem`(`service.go:396`) | E17 caller 并发**同模式**新增 `callerSems` |
| 终态汇聚 | `finish()`(`service.go:456`)：所有 job 到终态唯一出口 | metrics 终态 counter/histogram 挂此 |
| 提交入口 | `Submit()`(`service.go:172`)，`recordEvent(submitted)`(`service.go:340`) | metrics 提交 counter + in-flight++ 挂此 |
| 周期任务范式 | `startPruneLoop/DeliveryLoop/WorkflowLoop/ProbeLoop`(`serve.go:164..317`) | 若需采样型 gauge 可仿此（但优先 §6.4 GaugeFunc 免周期） |
| 路由装配 | `buildRouter()`(`server.go:173`)，`/v1` group 挂 `authMiddleware` | `/metrics` 与 `/health` 同级**免认证**；限流中间件挂 `/v1` group |
| 配置热加载 | `Service.Reload(newCfg)`(`service.go:166`) + `startReloadLoop`(`serve.go:113`) 原子交换 | caller 配额表随 Reload 重建（§7.3） |
| caller 配置 | `CallerConfig{ID,Token,TokenEnv}`(`config/model.go:139`) **无配额字段** | E17 在此扩字段 |

## 5. 架构（挂点总览）

```txt
                       ┌─────────────────── serve HTTP (rux) ───────────────────┐
  GET /metrics ───────▶│  metricsHandler (promhttp)  ← 免认证，与 /health 同级    │
                       │                                                          │
  POST /v1/jobs ──────▶│ authMiddleware → rateLimitMiddleware(E17) → handler      │
   Bearer<token>       │   (caller_id)      ↑429 超速率        │                  │
                       │                  per-caller token-bucket                 │
                       └──────────────────────────────────┬───────────────────────┘
                                                           ▼
                          ┌──────────────── job.Service ──────────────┐
                          │ Submit(): metrics.submitted++  in_flight++ │
                          │   ├ projectSem (已有)   ┐                  │
                          │   └ callerSem  (E17 新) ┘ 满则排队 queued   │
                          │ finish(): metrics.terminal{status}++       │
                          │           duration.Observe  in_flight--    │
                          └──────────────┬─────────────────────────────┘
                                         ▼  scrape 时即时读
                   GaugeFunc: in_flight / queued / workers_connected (免周期维护)
```

## 6. E16 — Prometheus /metrics 设计

### 6.1 库选型（决策 D1）
采用 **`github.com/prometheus/client_golang`**（事实标准、`promhttp` 现成、自带 Go runtime collector）。引入间接依赖较多但纯 Go、内网工具可接受。**不**手写 expfmt（维护成本高、易错）。

### 6.2 端点与认证（决策 D2）
- `GET /metrics`：注册在 `buildRouter()`(`server.go:173`) 中，**与 `/health` 同级、不挂 `authMiddleware`**——Prometheus 抓取端难配 Bearer，内网准入由网关兜底（同 SR202 思路）。
- 可配 `metrics.enabled`（默认 `true`）+ 可选 `metrics.token`（设了则要求 `Authorization: Bearer`，给有鉴权抓取需求的环境）。

### 6.3 指标集
| 指标 | 类型 | 标签 | 挂点 |
|---|---|---|---|
| `gofer_http_requests_total` | Counter | `method,route,status` | HTTP 中间件（`route`=路由模板非原始 path） |
| `gofer_http_request_duration_seconds` | Histogram | `method,route` | HTTP 中间件 |
| `gofer_jobs_submitted_total` | Counter | `caller,project,agent,runner` | `Submit()`(`service.go:340` 附近) |
| `gofer_jobs_terminal_total` | Counter | `status,caller,project` | `finish()`(`service.go:470` 附近) |
| `gofer_job_duration_seconds` | Histogram | `agent,runner,status` | `finish()`（running→terminal 时长） |
| `gofer_jobs_in_flight` | GaugeFunc | `—`（或 by project） | scrape 时读 `len(s.jobs)`（§6.4） |
| `gofer_jobs_queued` | GaugeFunc | `—` | scrape 时读等信号量计数（§6.4） |
| `gofer_workers_connected` | GaugeFunc | `—` | scrape 时读 `workerRegistry` |
| `gofer_worker_in_flight` | GaugeFunc | `worker_id` | 复用 `workerRegistry` in_flight |
| Go runtime（goroutines/GC/mem） | — | — | client_golang 默认 collector |

### 6.4 Gauge 用 GaugeFunc 在 scrape 时即时读（决策 D3）
in_flight / queued / workers 这类**当前值**用 `prometheus.NewGaugeFunc`（或自定义 Collector）在抓取回调里即时读内存，**不维护增减一致性、不需 startMetricsLoop**。读 `len(s.jobs)` 等需走 `Service.mu`——回调里短临界区快照即可。

### 6.5 cardinality 守则（决策 D4）
- **禁用** `job_id` / `request_id` / 原始 URL path 作标签。
- `route` 用 rux 路由模板（`/v1/jobs/{id}` 而非展开值）。
- `caller/project/agent/runner` 均为配置登记的有限集，安全。
- secret/token **绝不**进 metrics（SR403）。

## 7. E17 — per-caller 配额 / 限流设计

### 7.1 配置扩展（CallerConfig，决策 D5）
```go
type CallerConfig struct {
    ID       string
    Token    string
    TokenEnv string
    // E17 新增（0/空 = 不限，回退全局默认）
    MaxConcurrentJobs int     `yaml:"max_concurrent_jobs"` // 该 caller 同时在跑上限（信号量排队）
    RateLimit         float64 `yaml:"rate_limit"`          // 每秒请求数（令牌桶速率）
    RateBurst         int     `yaml:"rate_burst"`          // 桶容量（突发），默认 = ceil(RateLimit) 或 1
}
```
全局兜底放 `server.governance`：`default_caller_max_concurrent` / `default_rate_limit` / `default_rate_burst`（caller 未单配时生效；均 0=不限，保持向后兼容）。

### 7.2 并发配额：复用信号量（决策 D6）
- `Service` 新增 `callerSems map[string]chan struct{}`，仿 `semaphore()`(`service.go:365`) 新增 `callerSemaphore(callerID, limit)`。
- `execute()`(`service.go:383`) 在取 project 信号量**之后**再取 caller 信号量，两者都 `defer <-sem` 释放。
- **超额语义 = 排队**（job 停 `queued` 等槽，与 project 现有行为一致），**不**拒绝——并发配额是"削峰"，不是"拒服务"。

### 7.3 速率限流：HTTP 中间件令牌桶（决策 D7）
- 新增 `rateLimitMiddleware`，挂在 `/v1` group `authMiddleware` **之后**（已有 caller_id）。
- 用 `golang.org/x/time/rate`（轻量、标准库扩展，go.mod 已有 `x/sync`，加 `x/time`）；`map[callerID]*rate.Limiter`，`limiter.Allow()` 判定。
- 超额 → `429 Too Many Requests` + `Retry-After` 头 + `{error:"rate limited",detail:...}`（`respond.go` 加 `writeRateLimited`）。
- **速率仅限提交类写入**（`POST /v1/jobs`、`/v1/workflows`、`/answer` 等）；只读（list/get/SSE/events）不限，避免误伤观测。

### 7.4 热加载兼容（决策 D8）
- `Reload()` 时：速率 limiters **按新配置重建**（map 整体换，无状态损失——令牌桶重置可接受）。
- caller 并发信号量 `callerSems`：**懒创建后容量固定**，热加载改 `MaxConcurrentJobs` 对已建 sem **不即时生效**（下次该 caller 无活跃时重建 / 重启生效）——**与现有 project 信号量行为一致**（`service.go:365` 同限制），不引入新差异。文档明示此为已知限制。

### 7.5 与 E16 的接合
per-caller 的 `gofer_jobs_submitted_total{caller}` / `429` 计数（`gofer_http_requests_total{status="429"}`）即治理可观测面——**E17 的限流效果直接由 E16 指标体现**，无需额外埋点。

## 8. 数据模型

**无新表**。仅扩 `CallerConfig` 配置结构（§7.1）+ 进程内 `callerSems` / rate limiter map。metrics 全部内存态（client_golang registry），不落库。

## 9. 配置示例（example 增量）

```yaml
server:
  governance:                      # 全局兜底（caller 未单配时生效；0=不限）
    default_caller_max_concurrent: 4
    default_rate_limit: 5          # 每秒 5 次提交
    default_rate_burst: 10
  metrics:
    enabled: true
    token: ""                      # 空=免认证抓取；设了则需 Bearer
  callers:
    - id: ci-bot
      token_env: GOFER_TOKEN_CI
      max_concurrent_jobs: 8       # 覆盖全局
      rate_limit: 20
      rate_burst: 40
```

## 10. 安全

- `/metrics` 默认免认证但仅暴露聚合计数（无 prompt/secret/token）；敏感环境用 `metrics.token`。
- 限流键取**服务端解析的 caller_id**（非客户端供给），防伪造。
- SR403：secret/token 不入 metrics、不入限流日志。

## 11. 分阶段实施（plan 阶段细化）

| 阶段 | 内容 | 依赖 | 验收要点 |
|---|---|---|---|
| **P1** | E16 地基：引入 client_golang + `/metrics` 端点 + HTTP 中间件指标 + job 提交/终态/时长 counter+histogram + in_flight/queued/workers GaugeFunc | — | `curl /metrics` 见全部指标；跑 N 个 job 后 `submitted/terminal/duration` 数值正确；高基数标签零（grep 无 job_id 标签）；`/metrics` 免认证可抓 |
| **P2** | E17 治理：CallerConfig 扩字段 + `callerSems` 并发信号量 + `rateLimitMiddleware`(429) + 热加载重建 limiters + example/config-validate 补段 + per-caller metrics 标签贯通 | P1 | 超并发→排队(queued 不拒)；超速率→429+Retry-After；只读不被限；热加载改 rate_limit 即时生效；`gofer_http_requests_total{status="429"}` 计数可见 |

> 顺序：P1 先把"看得见"立起来（也为 P2 限流提供验证手段）→ P2 再加"管得住"。每阶段绿灯即提交（SR1202）。

## 12. 待确认决策点（审核重点）

- **D1 库**：client_golang（推荐）vs 手写 expfmt。→ 推荐 client_golang。
- **D2 /metrics 认证**：默认免认证 + 可选 token（推荐）vs 强制 token。
- **D3 gauge**：GaugeFunc scrape 时即时读（推荐，免周期）vs 实时增减维护。
- **D6 并发超额**：排队 queued（推荐，与 project 一致）vs 立即 429 拒绝。
- **D7 速率范围**：仅写入类限流（推荐）vs 全部 `/v1`。
- **D8 并发上限热加载**：沿用 project 现状"不即时生效"（推荐，零新差异）vs 强制重建 sem（有损在飞计数）。
- **D9 限流粒度**：v1 仅 per-caller（推荐）；per-agent/per-project 速率留后续。

## 13. 结论

E16/E17 共享 caller 维度、复用现有信号量与中间件链与周期范式，**无新表、改动集中在 `httpapi`(中间件+端点) + `job/service.go`(挂点) + `config/model.go`(扩字段)**。建议按 §11 P1→P2 两阶段实施。审核通过 §12 决策点后出 plan。
