# P2 — E17 per-caller 配额 / 限流（实施细化）

> 主纲：[`2026-06-21-observability-governance-plan.md`](2026-06-21-observability-governance-plan.md) · 设计 §7。依赖 P1。
> 目标：CallerConfig 扩配额字段 + per-caller 并发信号量（排队语义）+ 速率限流中间件（429）+ 热加载即时生效。

## T2.1 config 扩展

**改动**：`internal/config/model.go`。

CallerConfig(`:139`) 扩三字段：
```go
type CallerConfig struct {
	ID       string `yaml:"id"`
	Token    string `yaml:"token"`
	TokenEnv string `yaml:"token_env"`
	// E17（0/空=不限，回退 governance 全局默认）
	MaxConcurrentJobs int     `yaml:"max_concurrent_jobs"` // 同时在跑上限(信号量排队)
	RateLimit         float64 `yaml:"rate_limit"`          // 每秒提交请求数(令牌桶速率)
	RateBurst         int     `yaml:"rate_burst"`          // 桶容量(突发)，<=0 时取 max(1, ceil(RateLimit))
}
```

ServerConfig(`:27`) 加两段：
```go
	Governance GovernanceConfig `yaml:"governance"`
	Metrics    MetricsConfig    `yaml:"metrics"`   // 若 P1 已加则复用
```
```go
// GovernanceConfig 全局兜底；caller 未单配时生效。全 0 = 不限(向后兼容)。
type GovernanceConfig struct {
	DefaultCallerMaxConcurrent int     `yaml:"default_caller_max_concurrent"`
	DefaultRateLimit           float64 `yaml:"default_rate_limit"`
	DefaultRateBurst           int     `yaml:"default_rate_burst"`
}
// MetricsConfig E16 端点策略。Enabled 指针默认 true。
type MetricsConfig struct {
	Enabled *bool  `yaml:"enabled"`
	Token   string `yaml:"token"`
}
func (m MetricsConfig) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }
```

**helper（放 config 或 job 包，建议 config 作纯函数便于单测）**：
```go
// CallerConcurrencyLimit / CallerRate 解析某 caller 的有效配额：caller 单配 > 0 优先，
// 否则 governance 默认，否则 0(不限)。callerID=="" 时直接用 governance 默认。
func (sc *ServerConfig) CallerConcurrencyLimit(callerID string) int { /* 查 Callers→ID 匹配；缺则 Governance.DefaultCallerMaxConcurrent */ }
func (sc *ServerConfig) CallerRate(callerID string) (rps float64, burst int) { /* 同上；burst<=0 → max(1, ceil(rps)) */ }
```

**验收**：`go test ./internal/config` 加表驱动单测覆盖 caller 单配/回退默认/全不限三态。

## T2.2 caller 并发信号量（排队语义，复用 semaphore 模式）

**改动**：`internal/job/service.go`。

Service 加（与 `sems` 并列，复用 `s.mu`）：
```go
callerSems map[string]chan struct{} // per-caller 并发信号量；NewService 初始化为 map{}
```
`callerSemaphore`（仿 `semaphore`(`:365`)）：
```go
func (s *Service) callerSemaphore(callerID string, limit int) chan struct{} {
	if callerID == "" || limit <= 0 { return nil }
	s.mu.Lock(); defer s.mu.Unlock()
	if sem, ok := s.callerSems[callerID]; ok { return sem }
	sem := make(chan struct{}, limit); s.callerSems[callerID] = sem; return sem
}
```

`Submit`(`:356`)：算 caller sem，与 project sem 一起传入 execute：
```go
cfg := s.config() // 已有快照
projSem := s.semaphore(req.ProjectKey, proj.MaxConcurrentJobs)
callerSem := s.callerSemaphore(req.CallerID, cfg.Server.CallerConcurrencyLimit(req.CallerID))
go s.execute(entry, run, projSem, callerSem, runReq, timeout)
```

`execute`(`:383`) 签名加 `callerSem chan struct{}`；在取 project 槽(`:396-405`)**之后**再取 caller 槽（同 select+cancel-abort 模式，同样 `defer <-callerSem` 释放）：
```go
if callerSem != nil {
	select {
	case callerSem <- struct{}{}:
		defer func() { <-callerSem }()
	case <-ctx.Done():
		status, code, runErr := classify(ctx, runner.Result{ExitCode: -1})
		s.finish(entry, req.JobID, status, code, runErr); return
	}
}
```
> 超额 = job 停 `queued` 等槽（与 project 一致），**不拒绝**（设计 D6）。两层信号量顺序：project→caller，释放 defer 逆序自动。

**验收**：`internal/job` 测试——caller 配额=1 时并发提交 2 个长 job，断言第 2 个停 `queued` 直到第 1 个终态；caller 配额=0 时不 gating。

## T2.3 速率限流中间件（429，配置从 job.Service 读 = 热加载）

**改动 A**：`internal/job/service.go` 暴露 rate 读（统一真源，SIGHUP 即时）：
```go
func (s *Service) CallerRate(callerID string) (rps float64, burst int) {
	return s.config().Server.CallerRate(callerID)
}
```

**改动 B**：`internal/httpapi/server.go`。Server 加：
```go
limiters map[string]*rate.Limiter // per-caller；guarded by limMu
limMu    sync.Mutex
```
依赖：`golang.org/x/time/rate`（`go get golang.org/x/time/rate`）。

`rateLimitMiddleware`（挂 `/v1` group，**在 authMiddleware 之后**——需 caller_id）：
```go
func (s *Server) rateLimitMiddleware(c *rux.Context) {
	if !isSubmitPath(c) { c.Next(); return } // 仅写入类提交端点限流(设计 D7)
	caller := callerFromCtx(c)
	rps, burst := s.jobs.CallerRate(caller)
	if rps <= 0 { c.Next(); return }
	lim := s.limiterFor(caller, rps, burst) // 取/建 + SetLimit/SetBurst 同步最新
	if !lim.Allow() {
		c.Resp.Header().Set("Retry-After", "1")
		writeRateLimited(c, caller); c.Abort(); return
	}
	c.Next()
}

// isSubmitPath: 仅 POST /v1/jobs、POST /v1/workflows（提交）。
// 注意排除 /v1/jobs/{id}/cancel、/answer 等子操作——用 path 精确等于判定。
func isSubmitPath(c *rux.Context) bool {
	if c.Req.Method != http.MethodPost { return false }
	p := c.Req.URL.Path
	return p == "/v1/jobs" || p == "/v1/workflows"
}

func (s *Server) limiterFor(caller string, rps float64, burst int) *rate.Limiter {
	if burst <= 0 { burst = int(math.Ceil(rps)); if burst < 1 { burst = 1 } }
	s.limMu.Lock(); defer s.limMu.Unlock()
	lim, ok := s.limiters[caller]
	if !ok { lim = rate.NewLimiter(rate.Limit(rps), burst); s.limiters[caller] = lim; return lim }
	// 热加载即时生效：配置变了就动态调（无状态损失）
	if float64(lim.Limit()) != rps { lim.SetLimit(rate.Limit(rps)) }
	if lim.Burst() != burst { lim.SetBurst(burst) }
	return lim
}
```

group 注册改为（确认 rux 多中间件执行顺序：先 metrics、再 auth、再 rateLimit）：
```go
}, s.metricsMiddleware, s.authMiddleware, s.rateLimitMiddleware)
```
> ⚠️ 实施期**务必**验证 rux `Group(path, fn, mw...)` 的中间件执行顺序使 authMiddleware 早于 rateLimitMiddleware（后者依赖前者写入的 caller_id）。若 rux 顺序相反，改为按 rux 语义排列或在单路由上挂。

**改动 C**：`internal/httpapi/respond.go` 加：
```go
func writeRateLimited(c *rux.Context, caller string) {
	writeError(c, http.StatusTooManyRequests, "rate limited", "caller "+caller+" exceeded its submit rate; retry shortly")
}
```

**验收**：caller rate=1/s burst=1 时，连发 5 个 `POST /v1/jobs` → 首个 200/202、其余 429 带 `Retry-After`；`GET /v1/jobs`（只读）连发不被限。

## T2.4 热加载验证

- **速率**：改 example 的 `rate_limit` → `kill -HUP <pid>` → `gofer: config reloaded` → 立即生效（`limiterFor` 每次读 `CallerRate` 走 atomic cfg + `SetLimit`）。**无需** Server 在 reload 路径。
- **并发**：`callerSems` 懒建后容量固定，改 `max_concurrent_jobs` 对已建 sem 不即时生效（下次该 caller 无活跃重建/重启）——**与 project `sems` 行为一致**（`service.go:365`），在 design §7.4 / 代码注释明示，非缺陷。

**验收**：SIGHUP 改 rate 后限流阈值随之变化（测试可直接改 cfg + 调 `limiterFor` 断言 `Limit()` 更新）。

## T2.5 example + config validate + per-caller metrics 标签

- **example**（`config/*.example.yaml`）补完整段：
  ```yaml
  server:
    governance:
      default_caller_max_concurrent: 4
      default_rate_limit: 5
      default_rate_burst: 10
    metrics:
      enabled: true
      token: ""
    callers:
      - id: ci-bot
        token_env: GOFER_TOKEN_CI
        max_concurrent_jobs: 8
        rate_limit: 20
        rate_burst: 40
  ```
- **config validate**（`gofer config validate`，`internal/commands`）：校验 `rate_limit>=0`、`rate_burst>=0`、`max_concurrent_jobs>=0`；caller 配 rate 但全局/单配缺 burst 时给出默认提示（非错误）。复用既有 embed example 防漂移测试（`config validate` 对 example 通过）。
- **per-caller metrics 标签**：P1 `gofer_jobs_submitted_total{caller}` 与 `gofer_http_requests_total{status="429"}` 已天然覆盖治理可观测面（设计 §7.5），**无需新埋点**——仅在验收里确认 429 计数可见。

**验收**：`gofer config validate -c <example>` 通过；scrape 见 `gofer_http_requests_total{...,status="429"}` 在触发限流后 >0。

## P2 阶段验收清单（全绿即收尾）

- [ ] `go build ./... && go test ./internal/config ./internal/job ./internal/httpapi` 绿
- [ ] caller 并发=1：第 2 个并发 job 排队 `queued`，不拒
- [ ] caller rate 超限：429 + `Retry-After`；只读端点不被限
- [ ] SIGHUP 改 `rate_limit` 即时生效
- [ ] example 含 governance/metrics/callers 完整段，`config validate` 通过
- [ ] `gofer_http_requests_total{status="429"}` 触发后可见
- [ ] git 提交（SR1202）：`feat(governance): E17 P2 per-caller 并发配额 + 速率限流(429) + 热加载`
- [ ] 回填主纲 §5 实施结果 + 勾选进度
