# 实施计划：可观测 + 治理（E16 /metrics + E17 per-caller 配额限流）

> 设计依据：[`../../design/2026-06-21-observability-governance-design.md`](../../design/2026-06-21-observability-governance-design.md)（v0.2 决策已定稿）。
> 本文为**总纲**：开发计划总纲 + 进度跟进 + 每阶段实施结果（SR1105）。阶段详情见 `P1-metrics-plan.md` / `P2-governance-plan.md`。

## 1. 总纲

| 阶段 | 子文档 | 目标 | 依赖 | 量级 |
|---|---|---|---|---|
| **P1** | [`P1-metrics-plan.md`](P1-metrics-plan.md) | E16：`/metrics` 端点 + HTTP 指标中间件 + job 提交/终态/时长 + in_flight/queued/workers GaugeFunc | — | 中 |
| **P2** | [`P2-governance-plan.md`](P2-governance-plan.md) | E17：CallerConfig 扩配额字段 + per-caller 并发信号量 + 速率限流中间件(429) + 热加载 + per-caller metrics 标签 | P1 | 中 |

**实施次序**：P1（先把"看得见"立起来，也作 P2 的验证手段）→ P2（再加"管得住"）。每阶段绿灯即 git 提交（SR1202）。

## 2. 关键设计落点（共享，两阶段共用）

| 落点 | 文件:行 | P1 用 | P2 用 |
|---|---|---|---|
| HTTP 路由装配 | `internal/httpapi/server.go:173` `buildRouter()` | `/metrics` 挂 `/health` 同级（免认证） | `rateLimitMiddleware` 追加到 `/v1` group |
| caller 解析 | `internal/httpapi/auth.go:43` `callerFromCtx` | metrics 标签取 caller | 限流键取 caller |
| 提交入口 | `internal/job/service.go:172` `Submit` / `:340` recordEvent | `JobSubmitted` Inc + in_flight | 取 callerSem 传入 execute |
| 终态汇聚 | `internal/job/service.go:456` `finish` / `:470` | `JobTerminal` Inc + `JobDuration` Observe | — |
| per-project 信号量 | `internal/job/service.go:365` `semaphore` / `:396` execute 取槽 | — | `callerSemaphore` 同模式 + execute 二次取槽 |
| 配置结构 | `internal/config/model.go:27` `ServerConfig` / `:139` `CallerConfig` | `Metrics` 段 | `Governance` 段 + CallerConfig 扩字段 |
| 热加载 | `internal/job/service.go:166` `Reload`（atomic cfg）；`serve.go:334` `startReloadLoop` | — | 限流配置统一从 `job.Service.config()` 读（天然热加载）；httpapi.Server **不在** reload 路径，故不从 Server.cfg 读 governance |
| 错误响应 | `internal/httpapi/respond.go:14` `writeError` | — | 加 `writeRateLimited`（429+Retry-After） |

**架构要点（避免实施跑偏）**：
- job 包**不依赖** prometheus：定义 `job.MetricsSink` 接口（nil-safe），由 `internal/metrics` 实现、`commands.buildCore` 注入。未注入即 no-op。
- 限流配置（rate/并发上限）**唯一真源是 `job.Service` 的 atomic cfg**（`config()` 返回最新快照）；httpapi 限流中间件通过 `job.Service` 的导出方法读，保证 SIGHUP 即时生效，**不**复制一份到 httpapi.Server。

## 3. 前置检查（plan-checking，SR1430.2）

| 检查项 | 结果 |
|---|---|
| `go build ./...` 基线 | ✅ PASS |
| `go test ./internal/job ./internal/httpapi` 基线 | ✅ PASS（job 40s / httpapi 19s） |
| GOPROXY 可达 | ✅ `proxy.golang.org,direct` |
| `github.com/prometheus/client_golang` 可拉取 | ✅ proxy 可查（实施用 `@latest`，预期 v1.2x） |
| `golang.org/x/time` 可拉取 | ✅ proxy 可查（v0.14.0） |
| 无新表/无 DB 迁移 | ✅ 全内存态 + 配置扩字段 |

**结论：前置 PASS，可进入 SUPMODE。** 无严重阻断卡点；唯一需实施期确认的小点：rux 取「路由模板」的 API（见 P1-T1.3，已备路径归一化兜底方案）。

## 4. 进度跟进

- [x] **P1 E16 指标**（详见 P1 子文档）✅ commit `a7956ea`
  - [x] T1.1 依赖 + `internal/metrics` 包骨架（collectors + Registry + Handler）
  - [x] T1.2 `/metrics` 端点（免认证 + 可选 token + enabled 开关）
  - [x] T1.3 HTTP 指标中间件（requests_total + duration，route 模板/归一化）
  - [x] T1.4 job 挂点（MetricsSink 接口 + Submit/finish 埋点 + Service.Stats()）
  - [x] T1.5 GaugeFunc 装配（in_flight/queued/workers）
  - [x] T1.6 验收 + example/docs + 测试
- [x] **P2 E17 治理**（详见 P2 子文档）✅ commit `a228d8b`
  - [x] T2.1 config 扩展（Governance 段 + CallerConfig 三字段 + Metrics 段）
  - [x] T2.2 caller 并发信号量（callerSems + CallerConcurrencyLimit + execute 接入）
  - [x] T2.3 速率限流中间件（CallerRateLimit + limiters + 429）
  - [x] T2.4 热加载验证（rate 即时生效 / 并发沿用 project 语义）
  - [x] T2.5 example + config validate 补段 + per-caller metrics 标签贯通
  - [x] T2.6 验收 + 测试

## 5. 实施结果（完成后回填）

### P1 ✅（commit `a7956ea`）
- **新增**：`internal/metrics/`（独立 Registry + Go/Process collector + 5 collector vec + nil-safe sink + Handler + RegisterRuntimeGauges）；`httpapi/metrics_handler.go`（端点+中间件+route 归一化）；metrics/job/httpapi 三处测试。
- **修改**：`config/model.go`(MetricsConfig)、`job/service.go`(MetricsSink 接口+埋点+Stats)、`httpapi/server.go`(SetMetrics+端点+中间件)、`commands/serve.go`+`runner_probe.go`(装配+worker 计数)、example。
- **关键决策**：route 用 rux `c.Route().Path()`（有限模板 `/v1/jobs/:id`，低基数），`normalizeRoute` 仅 404 兜底；metrics 用 setter 注入（不动 9 处 New 调用站）；duration=EndedAt-StartedAt（端到端含排队）。
- **验收**：build/vet/test 全绿（job 51s/httpapi 19s/metrics 0.008s）；**job 包零 prometheus 依赖**（主控 `go list -deps` 复核 PASS）；真机 serve smoke 确认 submitted/terminal/duration/http 指标 + route 无 id 泄漏。
- **遗留**：离线环境未跑完整 `go mod tidy`（缺 test-only 依赖 cache，不影响功能）；有网时规整 go.sum test-graph 条目。

### P2 ✅（commit `a228d8b`）
- **新增**：`httpapi/ratelimit.go`（rateLimitMiddleware/isSubmitPath/limiterFor）；config/job/httpapi 三处治理测试。
- **修改**：`config/model.go`(CallerConfig 三字段+GovernanceConfig+CallerConcurrencyLimit/CallerRate helper)、`config/loader.go`(validate 校验 >=0)、`job/service.go`(callerSems+callerSemaphore+execute 双信号量+CallerRate 暴露)、`httpapi/server.go`(limiters map+中间件链 metrics→auth→rateLimit)、`respond.go`(writeRateLimited)、example、防漂移测试。
- **关键决策**：限流配置真源=`s.jobs.CallerRate()`（job.Service atomic cfg，SIGHUP 即时），**不复制进 Server**；rux 中间件顺序经读源码+实测双重确认（Group 末参按序前置，auth 早于 rateLimit）；并发超额=排队不拒（与 project 一致）、仅速率超限 429；limiter `SetLimit/SetBurst` 动态同步热加载；example governance 段保持注释（避免给 legacy `default` caller 误加限流，守 §7.1 向后兼容，二级判断 SR1401）。
- **验收**：build/vet/test 全绿；**真机冒烟**（实际 binary serve）：POST 连发 req1 进 handler/req2-6 **429**（burst=1）、GET 只读 5×200 **不被限**、`gofer_http_requests_total{POST,/v1/jobs,status="429"} 5` 计入且 route 无 id 泄漏；合法 job 提交后 `gofer_jobs_submitted_total{...} 1`+`gofer_jobs_in_flight 1` 闭环。
- **已知限制**：并发上限热加载不即时（callerSems 容量首建冻结，同 project sems，§7.4/D8 明示）；仅速率限流即时热加载。

## 6. 完成判定（SUPMODE 收尾）

- `go build ./...` + `go test ./...` 全绿
- `curl -s localhost:<port>/metrics` 见全部指标族，无 `job_id`/`request_id` 高基数标签
- 超并发→排队(queued 不拒)；超速率→429+Retry-After；只读端点不被限
- SIGHUP 改 `rate_limit` 即时生效
- example 配置含 `governance`/`metrics` 段且 `config validate` 通过
- 两阶段各自提交；最终按 CLAUDE.md 会话完成协议 push
