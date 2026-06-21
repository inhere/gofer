# P1 — E16 Prometheus /metrics（实施细化）

> 主纲：[`2026-06-21-observability-governance-plan.md`](2026-06-21-observability-governance-plan.md) · 设计 §6。
> 目标：`/metrics` 端点 + HTTP 指标 + job 生命周期指标，全内存态、job 包零 prometheus 依赖。

## T1.1 依赖 + `internal/metrics` 包骨架

**改动**：`go get github.com/prometheus/client_golang@latest`（拉 client_golang + 间接依赖）；新建 `internal/metrics/metrics.go`。

**关键骨架**：用**独立 Registry**（非全局默认，避免污染/便于测试），自带 Go runtime collector。

```go
package metrics

import (
	"net/http"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the collectors + a private registry. Nil-safe: a nil *Metrics
// makes every record method a no-op so non-serve callers/tests need no wiring.
type Metrics struct {
	reg *prometheus.Registry

	httpRequests *prometheus.CounterVec   // {method,route,status}
	httpDuration *prometheus.HistogramVec // {method,route}
	jobsSubmitted *prometheus.CounterVec  // {caller,project,agent,runner}
	jobsTerminal  *prometheus.CounterVec  // {status,caller,project}
	jobDuration   *prometheus.HistogramVec// {agent,runner,status}
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := &Metrics{reg: reg}
	m.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gofer_http_requests_total", Help: "HTTP requests by method/route/status"}, []string{"method", "route", "status"})
	m.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "gofer_http_request_duration_seconds", Help: "HTTP request duration", Buckets: prometheus.DefBuckets}, []string{"method", "route"})
	m.jobsSubmitted = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gofer_jobs_submitted_total", Help: "Jobs submitted"}, []string{"caller", "project", "agent", "runner"})
	m.jobsTerminal = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gofer_jobs_terminal_total", Help: "Jobs reaching a terminal state"}, []string{"status", "caller", "project"})
	m.jobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "gofer_job_duration_seconds", Help: "Job submit→terminal duration (incl. queue wait)", Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800}}, []string{"agent", "runner", "status"})
	m.reg.MustRegister(m.httpRequests, m.httpDuration, m.jobsSubmitted, m.jobsTerminal, m.jobDuration)
	return m
}

func (m *Metrics) Handler() http.Handler { return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}) }

// 记录方法（nil-safe）：实现 job.MetricsSink 接口（见 T1.4）。
func (m *Metrics) JobSubmitted(caller, project, agent, runner string) {
	if m == nil { return }
	m.jobsSubmitted.WithLabelValues(orEmpty(caller, "anon"), project, agent, runner).Inc()
}
func (m *Metrics) JobTerminal(status, caller, project, agent, runner string, durationSec float64) {
	if m == nil { return }
	m.jobsTerminal.WithLabelValues(status, orEmpty(caller, "anon"), project).Inc()
	m.jobDuration.WithLabelValues(agent, runner, status).Observe(durationSec)
}
func (m *Metrics) ObserveHTTP(method, route, status string, sec float64) {
	if m == nil { return }
	m.httpRequests.WithLabelValues(method, route, status).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(sec)
}
```
> `orEmpty(caller,"anon")`：空 caller（allow_empty_token 直连）落 `anon` 标签，避免空串标签。

**验收**：`go build ./internal/metrics` 绿；`go.mod` 出现 client_golang。

## T1.2 `/metrics` 端点（免认证 + 可选 token + enabled）

**改动**：`internal/httpapi/server.go`。Server 加 `metrics *metrics.Metrics` 字段；`New(...)` 增参注入（或 setter，见 T1.4 装配）。`buildRouter()` 在 `r.GET("/health",...)`(`server.go:176`) 之后、`/v1` group 之前加：

```go
// E16: Prometheus scrape endpoint. Sibling of /health (OUTSIDE the /v1
// authMiddleware group): scrapers rarely carry Bearer; intranet admission
// guards it (SR202). Optional metrics.token re-adds Bearer when set.
if s.metrics != nil && s.metricsEnabled {
	r.GET("/metrics", s.handleMetrics)
}
```

`handleMetrics`（新 handler，建议 `internal/httpapi/metrics_handler.go`）：

```go
func (s *Server) handleMetrics(c *rux.Context) {
	if s.metricsToken != "" { // 可选鉴权
		got, ok := bearerToken(c.Req.Header.Get("Authorization"))
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.metricsToken)) != 1 {
			writeError(c, http.StatusUnauthorized, "unauthorized", "invalid metrics token"); c.Abort(); return
		}
	}
	s.metrics.Handler().ServeHTTP(c.Resp, c.Req)
}
```
> `metricsEnabled`/`metricsToken` 由 `New` 从 `serverCfg.Metrics`（T2.1 加，P1 阶段先读 nil-safe：未配则 `enabled=true,token=""`）解析。P1 可先硬编码 `enabled=true,token=""`，T2.1 接通配置——**或** P1 直接加最小 `MetricsConfig`（推荐，少回改）。

**验收**：`curl -s localhost:<port>/metrics | head` 见 `# HELP gofer_...` 与 `go_goroutines`；带错误 token（配了 token 时）返 401。

## T1.3 HTTP 指标中间件

**改动**：`internal/httpapi/server.go` 加 `metricsMiddleware`，挂到 `/v1` group（与 auth 同 group，**在 auth 之前或之后均可**，metrics 不依赖 caller_id）：

```go
r.Group("/v1", func() { /* ...routes... */ }, s.metricsMiddleware, s.authMiddleware)
```

```go
func (s *Server) metricsMiddleware(c *rux.Context) {
	if s.metrics == nil { c.Next(); return }
	start := s.nowFn() // Server 加 nowFn（默认 time.Now，测试可注入）；或直接 time.Now()
	c.Next()
	route := routeTemplate(c)          // 见下
	status := strconv.Itoa(c.Resp.Status()) // rux ResponseWriter 暴露 Status()；若无则包装记录
	s.metrics.ObserveHTTP(c.Req.Method, route, status, time.Since(start).Seconds())
}
```

**route 取值（cardinality 关键，设计 §6.5）**——优先用 rux 路由模板，兜底归一化：
```go
// 优先：rux 是否在 ctx 暴露匹配的路由模板（实施时确认 rux.Context API，
// 如 c.Params 的路由 pattern / c.Get 某 key）。能取则用 "/v1/jobs/{id}"。
// 兜底 normalizeRoute：把疑似 ID 段（uuid/数字/长 hex）折叠为 {id}，杜绝高基数。
func normalizeRoute(p string) string { /* split '/'，ID 段→"{id}"，重组 */ }
```
> ⚠️ 绝不用原始 path 作标签（`/v1/jobs/abc123` 会爆基数）。实施期**必须**验证 route 标签是有限模板集（见 T1.6 验收）。

**验收**：scrape 后 `gofer_http_requests_total` 的 `route` 标签只出现有限模板（`/v1/jobs`、`/v1/jobs/{id}` 等），无具体 id。

## T1.4 job 挂点（MetricsSink 接口 + 埋点 + Stats）

**改动 A**：`internal/job/service.go` 定义接口（job 包不 import metrics）：
```go
// MetricsSink receives job lifecycle counters. nil-safe at call sites.
type MetricsSink interface {
	JobSubmitted(caller, project, agent, runner string)
	JobTerminal(status, caller, project, agent, runner string, durationSec float64)
}
```
Service 加字段 `metrics MetricsSink`；`NewService` 末参或新 `func (s *Service) SetMetrics(m MetricsSink)` 注入。调用点判 `if s.metrics != nil`。

**改动 B**：`Submit`，在 `recordEvent(EventJobSubmitted...)`(`service.go:340`) 之后：
```go
if s.metrics != nil {
	s.metrics.JobSubmitted(req.CallerID, req.ProjectKey, req.Agent, req.Runner)
}
```

**改动 C**：`finish`(`service.go:456`)，在 `entry.mu.Unlock()`(`:484`) 之后、用 snap：
```go
if s.metrics != nil {
	dur := float64(snap.EndedAt - snap.StartedAt) // 端到端时长(含排队)，设计 §6.3 注明语义
	s.metrics.JobTerminal(status, snap.CallerID, snap.ProjectKey, snap.Agent, snap.Runner, dur)
}
```
> 确认 `JobResult`(snap) 含 `CallerID/ProjectKey/Agent/Runner/StartedAt/EndedAt`；若某字段缺（如 remote 无 agent），传空串即可（标签为空不影响）。

**改动 D**：`Service.Stats()` 供 GaugeFunc 读在飞态：
```go
type ServiceStats struct{ InFlight, Queued, Running int }
func (s *Service) Stats() ServiceStats {
	s.mu.Lock(); defer s.mu.Unlock()
	st := ServiceStats{InFlight: len(s.jobs)}
	for _, e := range s.jobs {
		e.mu.Lock(); switch e.result.Status {
		case StatusQueued: st.Queued++
		case StatusRunning: st.Running++
		}; e.mu.Unlock()
	}
	return st
}
```
> 临界区短、live job 量小（终态即驱逐，C1）。注意锁序：先 s.mu 再 entry.mu，与既有代码一致避免死锁——实施期核对无反向持锁路径。

**验收**：`go build ./internal/job` 绿；提交 1 个 job 后 `Stats().InFlight` 变化。

## T1.5 GaugeFunc 装配（in_flight/queued/workers）

**改动**：`internal/metrics` 加注册函数（GaugeFunc 在 scrape 时即时读，免周期 loop，设计 §6.4）：
```go
func (m *Metrics) RegisterRuntimeGauges(stats func() (inflight, queued, running int), workers func() (connected, inFlight int)) {
	if m == nil { return }
	m.reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gofer_jobs_in_flight", Help: "Live jobs (queued+running+pending)"}, func() float64 { i, _, _ := stats(); return float64(i) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gofer_jobs_queued", Help: "Jobs waiting for a concurrency slot"}, func() float64 { _, q, _ := stats(); return float64(q) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gofer_workers_connected", Help: "Connected ws-workers"}, func() float64 { c, _ := workers(); return float64(c) }),
	)
}
```
装配点 `internal/commands/serve.go`（buildCore 之后、起 server 前）：
```go
m := metrics.New()
core.Jobs.SetMetrics(m)
m.RegisterRuntimeGauges(
	func() (int, int, int) { st := core.Jobs.Stats(); return st.InFlight, st.Queued, st.Running },
	func() (int, int) { /* 读 hub/workerRegistry：connected 数 + 总 in_flight；hub 为 nil 时返 0,0 */ },
)
// 传 m 给 httpapi.New(...)
```
> workers 读：复用 `handleListRunners`(`runner_handler.go:112`) 背后的 `workerRegistry`；若该接口未直接暴露计数，加一个轻量 `Count()`／遍历快照。hub-less（测试）返 0。

**验收**：scrape 见 `gofer_jobs_in_flight`/`gofer_jobs_queued`/`gofer_workers_connected`；起 1 个 worker 后 connected=1。

## T1.6 验收 + example/docs + 测试

- **集成测试** `internal/httpapi/*_test.go`：httptest 起 server（注入 metrics）→ 提交 N 个 exec job → 等终态 → GET `/metrics` 断言 `gofer_jobs_submitted_total` ≥ N、`gofer_jobs_terminal_total{status="done"}` 出现。
- **cardinality 守卫测试**：断言 scrape 输出中 `route=` 标签集合 ⊆ 已知模板白名单（grep 无具体 job id）。
- **example**：`config/*.example.yaml` 加（P1 最小，与 T2.1 合并完整）：
  ```yaml
  server:
    metrics:
      enabled: true
      token: ""   # 空=免认证抓取
  ```
- **README/docs**：补 `/metrics` 抓取说明 + 指标清单（可并入 P2 收尾）。

**P1 阶段验收清单（全绿才进 P2）**：
- [ ] `go build ./... && go test ./internal/metrics ./internal/job ./internal/httpapi` 绿
- [ ] `curl /metrics` 见 http/jobs/gauge/go_* 全部指标族
- [ ] 提交 job 后 submitted/terminal/duration 计数正确
- [ ] route 标签为有限模板集（无高基数 id）
- [ ] metrics.token 配置时 401 拦截、空时免认证
- [ ] git 提交（SR1202）：`feat(metrics): E16 P1 /metrics + http/job 指标 + GaugeFunc`
