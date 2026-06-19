# P4 — C6 远端节点可观测性：`/v1/runners`（worker 心跳态 + peer-http 主动 `/health` 探针）

> 主文档（契约真源）：[`./2026-06-19-ws-worker-c6c7-plan.md`](./2026-06-19-ws-worker-c6c7-plan.md)（§3 分期、§4 wshub `WorkerRegistry`、§10 进度）。
> 关联约束：[`../../design/architecture-overview.md`](../../design/architecture-overview.md) §9.1 C6 行、§9.2 扩展点②（Workers 仪表盘）。
> 本子文档给 P4 的可执行细节（file→改动、响应结构、关键流程、验收）。**仅计划，不含实现代码、不提交。**

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-19 | Claude | 初版：拆 C6 为「peer-http 主动探针（独立、可先落）」+「worker 心跳态（依赖 P3 注册表）」两半；定 `/v1/runners` 响应结构、探针循环生命周期（镜像 `startPruneLoop`）、状态来源映射、线程安全、测试矩阵。 |

## 2. 概览与目标

C6 的本质：**让"远端执行位置"在故障发生前就可见**。当前痛点（overview §9.1 C6）——`peer-http` 在 peer 宕机前 host 无感，故障发现滞后；worker 连接态对运维不可见。

本阶段交付**一个新只读端点 `GET /v1/runners`**（挂在已鉴权的 `/v1` 组下），一次性列出**全部已配置 runner**的类型与健康态，三类 runner 各有来源：

| runner kind | 状态来源 | 状态值 |
|---|---|---|
| `local` | 进程内恒在 | 恒 `up` |
| `worker` | `wshub.WorkerRegistry`（P3 心跳态） | `connected` / `disconnected`（未连过 = `disconnected`/`unknown`） |
| `peer-http` | **周期主动探针** `GET <base_url>/health` 的缓存结果 | `up` / `down` / `unknown`（尚无探针结果） |

**两半的依赖关系（关键）**：

- **A. peer-http 主动探针** —— **与 ws-worker 完全无关，不依赖 P0–P3**，只需 `serve` 进程 + 现有 `peer-http` runner 配置即可落地。**可早于 P3、甚至作为本计划最先交付的一块**（主文档 §3 已注明"peer-http 部分独立"）。
- **B. worker 心跳态** —— **依赖 P3** 的 `wshub.WorkerRegistry`（连接态 / `last_heartbeat` / 在飞 job / labels / worker_id 在 P3 完成后才有数据）。

> 因此 `/v1/runners` 的 handler 与探针组件可先落地（worker 段返回占位/空），P3 完成后再把 worker 段接上注册表读取——两段解耦、可分两次提交（SR1202）。

## 3. 范围

### 3.1 IN（本阶段做）

1. **`GET /v1/runners` handler**：在 `httpapi` 内新增只读处理器，列出 `cfg.Runners` 全量 + 隐含的 `local`；每行带 `name / type / status / detail`。挂入 `buildRouter` 的 `/v1` 鉴权组（与 `/v1/jobs` 同 `authMiddleware`）。
2. **peer-http 主动健康探针**：在 `commands` 内新增一个小探针组件 `peerProber`（serve core 持有），周期对每个 `peer-http` runner 发 `GET <base_url>/health`，缓存「up/down + 时间戳 + 延迟」；探针循环为 serve goroutine，干净停机（镜像 `startPruneLoop`/`startReloadLoop`），**绝不阻塞请求处理**。
3. **worker 段接线**：`/v1/runners` 读 `wshub.WorkerRegistry` 暴露的连接态（依赖 P3）。本子文档定**读取契约与字段映射**；注册表本体在 P3 落地。
4. **配置面**：探针间隔 / 超时的配置项（§6）。
5. **测试**：handler 形状/鉴权、探针 up→down 翻转、`-race`（探针缓存 + 注册表读并发）。

### 3.2 OUT（本阶段不做）

- **WP4 Workers 仪表盘前端**：`/v1/runners` 是其数据源，但**本阶段无任何前端改动**（见 §9，仅一行说明）。
- **被动健康判定 / 探测式熔断 / 自动摘除不健康 runner**：探针只**呈现**状态，不参与派发决策（不做"探针 down 就拒绝派 job"）。派发仍由 `job.Service` 现有逻辑负责；可观测性与调度解耦。
- **peer-http 探针动态增删随 SIGHUP 重建**：与现状一致——runner 实例不随 reload 重建（overview §9.1 C3 限制），探针目标集合在 serve 启动时冻结；新增/删 peer 需重启。本阶段**沿用该限制**，不引入探针热重载。
- **worker 主动探活**：worker 段只读 P3 已有的**被动心跳态**，不在 hub 侧对 worker 发额外探测（worker 是纯出站连接，心跳已是其活性真源）。

## 4. 改动清单（file → 改动）

> 行号为现状定位锚点，实施时以实际为准。

### 4.1 peer-http 探针（A，独立可先落）

| 文件 | 改动 |
|---|---|
| `internal/config/model.go` | `ServerConfig` 增可选探针配置块（§6）：`RunnerProbeConfig{ IntervalSeconds int; TimeoutSeconds int }`（YAML `runner_probe`）。给 `Enabled()`/默认值访问器（参照 `RetentionConfig` 风格：`ProbeInterval()` 默认 30s、`ProbeTimeout()` 默认 5s）。**纯新增字段**，未配置时按默认探针。 |
| `internal/commands/`（新增 `runner_probe.go`） | 新增 `peerProber` 组件：持有 `map[name]probeState`（`{status, checkedAt, latency, lastErr}`）+ `sync.RWMutex`；构造时从 `cfg.Runners` 过滤 `type==peer-http` 取 `{name, base_url}`；方法 `Snapshot() []ProbeResult`（供 handler 读）、`probeOnce(ctx)`（遍历目标并发探测、写缓存）。探测用独立 `http.Client{Timeout: ProbeTimeout()}` 发 `GET base_url+"/health"`，2xx => `up`，否则 `down`。**不复用 `peerhttp.Runner`/`client.Client`**（那是 job 转发用，探针只需轻量 GET `/health`）。 |
| `internal/commands/serve.go` | `runServe` 内新增 `startProbeLoop(c, prober, interval, stop)`（**完全镜像 `startPruneLoop`**：先 `probeOnce` 一次，再 `time.NewTicker` 周期；`<-stop` 退出）。`stopProbe := make(chan struct{}); defer close(stopProbe)`。prober 实例传给 `httpapi.New`（见下）。 |
| `internal/commands/assemble.go` | `Core` 增字段或在 serve 内单独构造 prober；倾向**在 `serve` 内构造**（mcp 入口不起 HTTP server，无需探针），prober 仅注入 `httpapi.New`。 |

### 4.2 `/v1/runners` 端点（A+B 汇合点）

| 文件 | 改动 |
|---|---|
| `internal/httpapi/server.go` | `Server` 增两个只读依赖：`prober runnerProber`（接口，`Snapshot() []ProbeResult`，serve 注入；nil-safe）、`workers workerRegistry`（接口，P3 注入；nil 表示 worker 段未启用）。`New(...)` 签名追加这两参（或用 functional-option 避免参数膨胀——见 §7 决策）。`buildRouter` 的 `/v1` 组内增 `r.GET("/runners", s.handleListRunners)`（紧邻 `/v1/jobs` 注册块，第 137 行附近）。 |
| `internal/httpapi/`（新增 `runner_handler.go`） | `handleListRunners`：遍历 `cfg.Runners` + 隐含 `local`，按 type 组装每行（§5 结构）；list-style 响应 `{"runners":[...]}`（**与 `handleListJobs` 的 `{"jobs":[...]}` 同形**，第 67 行）。空集合也返回非 nil 数组（`{"runners":[]}`）。 |
| `internal/httpapi/`（接口定义） | 在 httpapi 内定义**窄接口**（依赖倒置，避免 httpapi 直接 import commands/wshub 造成环）：`runnerProber{ Snapshot() []ProbeResult }`、`workerRegistry{ WorkerState(workerID string) (WorkerStatus, bool) }`。`ProbeResult`/`WorkerStatus` 是 httpapi 内的小 DTO（或放共享叶子包），由 serve 侧适配器实现。 |

### 4.3 worker 段接线（B，依赖 P3）

| 文件 | 改动 |
|---|---|
| `internal/wshub`（P3 产物） | `WorkerRegistry` 暴露只读快照方法供 handler 读：`WorkerState(workerID) (WorkerStatus, bool)` 或 `Snapshot() map[string]WorkerStatus`。`WorkerStatus{ Connected bool; LastHeartbeat int64(ms); InFlight int; Labels []string; WorkerID string }`。**线程安全**（注册表内部锁，见 §8）。本字段集**P3 定义**，本文仅声明 handler 侧读取契约——如 P3 实际字段名不同，以 P3 为准并回填本表（§10 一致性）。 |
| `internal/httpapi/runner_handler.go` | `type==worker` 行：取 `cfg.Runners[name].WorkerID`（注：`RunnerConfig` 需在 P1 已加 `worker_id` 字段——见 §10 一致性核对），向 `s.workers.WorkerState(workerID)` 查；命中且 `Connected` => `status:"connected"` + detail（heartbeat age / in-flight / labels）；未命中或断开 => `status:"disconnected"`；`s.workers==nil`（P3 未上线）=> `status:"unknown"`。 |

## 5. 响应结构示例（JSON）

`GET /v1/runners`（需 Bearer 鉴权，401 走 `/v1` 组现有 `authMiddleware`）：

```json
{
  "runners": [
    {
      "name": "local",
      "type": "local",
      "status": "up"
    },
    {
      "name": "peer-build-01",
      "type": "peer-http",
      "status": "up",
      "base_url": "https://peer-01.internal:8765",
      "probe": {
        "checked_at": 1750300000000,
        "latency_ms": 12,
        "error": ""
      }
    },
    {
      "name": "peer-build-02",
      "type": "peer-http",
      "status": "down",
      "base_url": "https://peer-02.internal:8765",
      "probe": {
        "checked_at": 1750300000000,
        "latency_ms": 0,
        "error": "dial tcp ...: connection refused"
      }
    },
    {
      "name": "remote-laptop",
      "type": "worker",
      "status": "connected",
      "worker_id": "laptop-01",
      "worker": {
        "last_heartbeat": 1750300003000,
        "heartbeat_age_ms": 1200,
        "in_flight": 1,
        "labels": ["macos", "gpu"]
      }
    },
    {
      "name": "remote-nas",
      "type": "worker",
      "status": "disconnected",
      "worker_id": "nas-02"
    }
  ]
}
```

字段约定：
- `server_time` 类语义统一**毫秒**时间戳（与 `handleHealth` 的 `server_time` 一致）；`checked_at` / `last_heartbeat` 均毫秒。
- `status` 枚举：`up`（local 恒、peer-http 探针 2xx）/ `down`（peer-http 探针失败）/ `connected`（worker 在线）/ `disconnected`（worker 离线/未连过）/ `unknown`（peer-http 尚无探针结果、或 worker 注册表未启用 = P3 未上线）。
- `probe.error` 仅在 `down` 时非空；**不含敏感信息**（探针只发 `/health`，无 token 入响应/日志，遵 §SR / 主文档 §11）。
- worker 段字段（`worker.*`）在 P3 落地后才填实；P3 前该段缺省（`status:"unknown"`）。

> 这是**本地开发工具的简单结构**（与 httpapi 包头注释一致：不用公司 `{status,code,message}` 信封）；错误（极少）走现有 `writeError` 小信封。

## 6. 配置面（探针）

`server` 下新增可选块（未配置 => 默认 30s 间隔 / 5s 超时；不引入新顶层 key）：

```yaml
server:
  # ...existing token/callers...
  runner_probe:
    interval_seconds: 30   # peer-http /health 探针周期，默认 30
    timeout_seconds: 5     # 单次探测超时，默认 5
runners:
  peer-build-01:
    type: peer-http
    base_url: https://peer-01.internal:8765
    token_env: GOFER_PEER01_TOKEN   # 探针只打 /health（无鉴权端点），token 不参与探针
```

- 探针目标 = `cfg.Runners` 中 `type==peer-http` 的 `{name, base_url}` 全集；无 peer-http runner 时探针循环空转/不启（参照 `RetentionConfig.Enabled()` 风格：无目标即 `startProbeLoop` 直接 return）。
- `/health` 是**每个 serve 节点都有的非鉴权端点**（`server.go:129`、`project_handler.go:11`），故探针**无需 token**；只判 HTTP 2xx。

## 7. 关键设计决策

- **D1 探针不复用 job 转发客户端**：`peerhttp.Runner`/`client.Client` 面向 `/v1/jobs` 转发与 SSE 镜像，重且带鉴权。探针只需轻量 `http.Client.Get(base_url+"/health")`，独立小组件更内聚、超时独立可控、互不影响。
- **D2 httpapi 用窄接口反依赖**：`/v1/runners` handler 不直接 import `commands.peerProber` / `wshub.WorkerRegistry`（避免包环、保持 httpapi 仅"解析参数+编码响应"的职责，见包头注释）。定义 `runnerProber` / `workerRegistry` 两个**消费者侧接口**，由 serve 注入实现。
- **D3 `New(...)` 参数膨胀**：当前 `httpapi.New` 已 6 参，再加 prober + workers 共 8 参偏多。倾向**改用 functional-option**（`New(cfg, opts...)`，`WithProber`/`WithWorkerRegistry`）或一个 `Deps` 结构，降低后续扩展成本——此为本阶段附带的小重构，须在实施前与现有所有 `New(...)` 调用点（serve、httptest）一并改。**列为待确认**（§11）。
- **D4 探针与派发解耦**：探针结果纯展示，不喂回 `job.Service` 派发决策（OUT §3.2）。保持"可观测"与"可调度"两条线独立，避免探针抖动误伤派发。

## 8. 关键流程

### 8.1 peer-http 探针循环（serve goroutine，镜像 `startPruneLoop`）

```
serve 启动
  └─ 构造 peerProber(targets = peer-http runners)
  └─ startProbeLoop(prober, interval=30s, stop):
        若无 peer-http 目标 → return（不起 goroutine）
        go:
          probeOnce()                      // 启动即探一次，避免首个 interval 内全 unknown
          ticker := NewTicker(30s)
          for select:
            <-stop      → return            // serve 关闭，干净退出，无泄漏
            <-ticker.C  → probeOnce()
```

`probeOnce(ctx)`：对每个 target **并发** `GET base_url/health`（各自 `ctx` 带 5s 超时 / `errgroup` 或 `WaitGroup`）→ 写 `prober` 缓存（`mu.Lock` 整体或 per-entry）。单次探测耗时上限 = `timeout_seconds`，**与请求处理在不同 goroutine，绝不阻塞 `/v1/runners` 读**（读只取缓存快照）。

### 8.2 `/v1/runners` 读路径（请求 goroutine）

```
GET /v1/runners (Bearer 已过 authMiddleware)
  handleListRunners:
    out = []
    out += {name:"local", type:"local", status:"up"}      // 隐含恒在
    for name, rc in cfg.Runners:
      switch rc.Type:
        "peer-http": 查 prober.Snapshot()[name] → status/probe；prober 缺该名 → "unknown"
        "worker":    查 workers.WorkerState(rc.WorkerID) → connected/disconnected；workers==nil → "unknown"
        其它/未知:    status:"unknown"（前向兼容未来 runner 类型）
    JSON({"runners": out})       // 读 RWMutex.RLock 快照，O(1) 不阻塞探针写
```

### 8.3 故障翻转时序（验收锚点）

```
t0  peer-02 健康          → /v1/runners 显示 peer-build-02: up
t1  peer-02 进程被 kill
t1+ 下一次 ticker 探测 GET /health 连接拒绝 → 缓存写 down
≤ t1 + interval(30s)      → /v1/runners 显示 peer-build-02: down（探针延迟内翻转）
```

worker 段同理：P3 心跳超时把注册表标记 `disconnected` → `/v1/runners` 下次读即显示 `disconnected`。

## 9. Web 控制台（仅说明，非本阶段）

未来 **WP4 Workers 仪表盘**（overview §9.2 扩展点②）将以 `GET /v1/runners` 为唯一数据源，展示 runner 列表 / 连接态 / 心跳 age / 在飞 job / 标签。**本阶段不做任何前端改动**——只保证端点形状稳定、可被前端直接消费（list-style + 毫秒时间戳 + 明确 status 枚举）。

## 10. 与主文档的一致性核对（实施前确认）

- ✅ **范围一致**：主文档 §3 P4 行 = "worker 心跳态（依赖 P3）+ peer-http 主动探针（独立，可早于 P3 起手）"；本文 §2/§3 据此拆 A/B 两半。
- ✅ **状态来源一致**：worker 段读 `wshub.WorkerRegistry`（主文档 §4：暴露 connected/last_heartbeat/in-flight/labels）；peer-http 段为周期主动 `GET /health` 探针（主文档 §3）。
- ⚠️ **`RunnerConfig.worker_id` 字段**：主文档 §6 配置示例里 `worker` 类型 runner 带 `worker_id: laptop-01`，但**现状 `internal/config/model.go` 的 `RunnerConfig` 只有 `Type/BaseURL/TokenEnv`，无 `WorkerID`**。该字段应由 **P1**（worker runner 落地）补到 `RunnerConfig`。**P4 依赖它存在**；若 P1 未补，P4 需在 §4.3 一并补 `WorkerID string yaml:"worker_id"`。**已 flag，需与 P1 owner 对齐归属**。
- ⚠️ **worker 心跳态字段名/方法名**：本文 §4.3 假定 `WorkerState(id)`/`WorkerStatus{...}`；**P3 实际命名以 P3 子文档为准**，落地后回填本表，避免漂移。
- ✅ **handler 形状/鉴权一致**：复用 `handleListJobs` 的 `{"...":[...]}` 形 + `/v1` 组 `authMiddleware`，无新增鉴权机制。
- ✅ **生命周期模板一致**：探针循环镜像 `startPruneLoop`/`startReloadLoop`（`stop chan` + `defer close` + ticker），与 serve 既有周期 goroutine 同构。

> 除上述 2 处 ⚠️（均为"P4 依赖上游阶段产物的字段/命名，需对齐"，非设计冲突），未发现与主文档的不一致。

## 11. 待确认事项

1. **D3 `httpapi.New` 重构**：是否本阶段把 `New(...)` 改 functional-option / `Deps` 结构？（推荐做，避免 8 参；但牵动 serve + httptest 所有调用点。）
2. **探针并发上限**：peer-http 数量多时是否限并发（如信号量）？MVP 默认全并发（数量通常很小，<10）。
3. **`unknown` vs 省略**：P3 未上线时 worker 行返回 `status:"unknown"` 还是干脆不列 worker 行？本文取**列出 + unknown**（前端能看到"配置了但注册表未启用"），待确认是否接受。

## 12. 测试与验收

### 12.1 验收标准（对应主文档 P4 Acceptance）

- [ ] `GET /v1/runners` 列出全部已配置 runner（含隐含 `local`），type 正确；**需鉴权**（无/错 token → 401，与 `/v1/jobs` 同）。
- [ ] **peer-http 行**：探针 2xx → `up`；kill 掉 peer → **≤ 一个探针间隔内**翻转为 `down`（§8.3）；`down` 行带 `probe.error`。
- [ ] **worker 行**（P3 后）：在线 worker 显示 `connected` + heartbeat-age / in-flight / labels / worker_id；离线显示 `disconnected`；P3 前显示 `unknown`。
- [ ] 探针循环**不阻塞**其它请求处理（探测在独立 goroutine，handler 只读缓存快照）；serve 关闭时探针 goroutine **干净退出，无泄漏**（`<-stop` 返回）。

### 12.2 测试矩阵

| 用例 | 类型 | 要点 |
|---|---|---|
| handler 形状 | httptest | `{"runners":[...]}` 非 nil 数组；local 恒 `up`；空 peer/worker 配置时仅 local 行 |
| handler 鉴权 | httptest | 无 Bearer → 401；正确 token → 200（复用 `/v1` 组现有 auth 测试夹具） |
| peer-http up | 单测 | 探针对 `httptest.Server` 暴露 `/health` 返 200 → `Snapshot()[name].status==up`、latency>0 |
| peer-http up→down | 单测 | 起一个 fake `/health` server，`probeOnce` 得 `up`；关闭它（或指向已关端口）再 `probeOnce` → `down` + error 非空 |
| 探针循环停机 | 单测 | `startProbeLoop` + `close(stop)` 后 goroutine 退出（`goleak` 或显式 wait，确认无泄漏） |
| 无目标短路 | 单测 | 无 peer-http runner 时 `startProbeLoop` 不起 goroutine（不 panic、`Snapshot()` 空） |
| `-race` | 竞态 | `probeOnce`(写缓存) 与 `Snapshot()`(handler 读) 并发 `go test -race`；worker 段与注册表读并发（P3 接入后）无数据竞争 |
| worker 段（P3 后） | httptest + 假注册表 | 注入 fake `workerRegistry`：connected → `connected` 行；缺失 → `disconnected`；注入 nil → `unknown` |

### 12.3 构建/测试环境

```bash
export PATH=/path/to/ws-root/linux-env/sdk/gosdk/go1.25.10/bin:$PATH
cd tools/gofer
go build ./...
go test ./internal/httpapi/... ./internal/commands/... -race
```

## 13. 结论

C6 拆为两半：**peer-http 主动 `/health` 探针**（独立、可作本计划最先落地的一块，serve goroutine 镜像 `startPruneLoop`、缓存翻转、不阻塞请求）+ **worker 心跳态**（依赖 P3 `WorkerRegistry`，handler 经窄接口只读）。汇合于新只读端点 `GET /v1/runners`（`/v1` 鉴权、list-style 响应），为未来 WP4 Workers 仪表盘提供唯一数据源。两 ⚠️ 项（`RunnerConfig.worker_id` 由 P1 补、worker 状态字段名以 P3 为准）需实施前对齐。审核确认后并入 SUPMODE 实施。
