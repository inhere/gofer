# Gofer 架构加固 C2–C5 实施计划

> 对应 [`design/architecture-overview.md`](../design/architecture-overview.md) §9.1 的 C2–C5。本计划给出可直接落地的改动点（file:line）、关键代码片段与验收检查。C1 已完成（SQLite store），本计划在其上增量。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-18 | Claude | 初版：C2 调用方身份/吊销、C3 配置热加载、C4 日志流控、C5 提交幂等。基于现状代码勘察。 |

## 2. 范围与分期

| 阶段 | 内容 | 价值 | 依赖 |
|---|---|---|---|
| **Phase A** | C2 + C5：提交链路加固（调用方身份 + 提交幂等）—— 二者都改 `JobRequest/JobResult/JobRecord` + jobs 表，合并一次 schema 迁移 | 审计/多租户隔离 + 重投去重 | C1 |
| **Phase B** | C3：配置热加载（SIGHUP → 重新 Load + 原子替换注册表 cfg） | 加项目/agent 不重启 | 无 |
| **Phase C** | C4：日志流控（SSE 帧 cap + 节流 + 日志轮转 + 前端 buffer 上限） | 超大/高频输出 job 不撑爆内存/帧 | 无 |

**建议顺序**：A → B → C（A 价值最高且最小；B/C 独立可并行/按需）。每阶段绿灯即提交（SR1202）。

## 3. 公共前置：jobs 表增列的轻量迁移

C2、C5 都要给 `jobs` 表加列（`caller_id`、`request_id`）。当前 `jobstore` 用 `CREATE TABLE IF NOT EXISTS`，对**已存在**的库不会加列。SQLite 无 `ADD COLUMN IF NOT EXISTS`，需按 `table_info` 探测后 `ALTER TABLE ADD COLUMN`。

`internal/jobstore/store.go` `applySchema()` 后追加 `migrate()`：

```go
// migrate adds columns introduced after the initial schema (additive only).
// SQLite has no ADD COLUMN IF NOT EXISTS, so probe pragma table_info first.
func (s *Store) migrate() error {
    cols, err := s.tableColumns("jobs")
    if err != nil { return err }
    add := func(col, ddl string) error {
        if _, ok := cols[col]; ok { return nil }
        _, e := s.db.Exec("ALTER TABLE jobs ADD COLUMN " + ddl)
        return e
    }
    if err := add("caller_id", "caller_id TEXT"); err != nil { return err }      // C2
    if err := add("request_id", "request_id TEXT"); err != nil { return err }    // C5
    return nil
}

func (s *Store) tableColumns(table string) (map[string]struct{}, error) {
    rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
    if err != nil { return nil, err }
    defer rows.Close()
    out := map[string]struct{}{}
    for rows.Next() {
        var cid int; var name, typ string; var notnull, pk int; var dflt any
        if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil { return nil, err }
        out[name] = struct{}{}
    }
    return out, rows.Err()
}
```

`request_id` 唯一性用**部分索引**（NULL/'' 不参与），随 schema 建：
```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_request_id ON jobs(request_id) WHERE request_id <> '';
```
> 决策：列保持可空、写入空串表示"无 request_id"；部分唯一索引只约束非空值，故未带 request_id 的 job 不互相冲突。

**验收**：旧库（仅 C1 列）启动后自动补 `caller_id`/`request_id` 列 + 索引；新库一次建全。单测：用只含 C1 列的库文件跑 `Open` 后 `PRAGMA table_info` 含新列。

---

## 4. Phase A — C2 调用方身份 / 吊销 + C5 提交幂等

### 4.1 现状（勘察结论）

- 鉴权中间件 `Server.authMiddleware` @ `internal/httpapi/auth.go:24`，token **明文 `!=` 比对**（`auth.go:36`，非常时间）；单一 token（`config.ServerConfig.Token/TokenEnv`）。
- 受保护：全部 `/v1/*`（`server.go:77-94`）；豁免：`/health`、静态 web fallback。
- `JobRequest/JobResult`（`job/model.go`）、`JobRecord`（`jobstore/jobs.go`）**无调用方字段、无 request_id**。
- 提交链路：`job_handler.go:handleCreateJob` → `service.go:Submit`（`service.go:119`）→ `genJobID`（时间戳+随机，仅文件系统去重）。
- 无 Redis；唯一去重手段是 SQLite 主键（job id）。

### 4.2 C2 设计决策

1. **常时间比对**（独立安全修复，先做）：`auth.go:36` 改 `crypto/subtle.ConstantTimeCompare`。
2. **多 token + 调用方标识**：`ServerConfig` 新增 `Callers []CallerConfig`（`id` + `token`/`token_env`）；保留 `Token`（视作 id=`"default"` 的调用方，向后兼容）。中间件命中后把 `caller_id` 写入请求上下文，handler 取出注入 `JobRequest.CallerID`。
3. **吊销**：MVP 用**配置即真源** + 热加载（C3）——从 config 删掉某 caller 并 SIGHUP 即吊销，无需独立吊销表。文档注明"吊销 = 改配置 + reload"。
4. **caller_id 入 job**：`JobRequest/JobResult/JobRecord` 加 `CallerID`，落 `jobs.caller_id` 列；`ListJobs` 支持按 caller 过滤（`ListQuery.Caller`）。

```go
// config/model.go
type CallerConfig struct {
    ID       string `yaml:"id"`
    Token    string `yaml:"token"`
    TokenEnv string `yaml:"token_env"`
}
// ServerConfig 增: Callers []CallerConfig `yaml:"callers"`
```

中间件改造（`auth.go`）——构建 `map[token]callerID`（启动时一次，token 空跳过），常时间比对命中取 caller：
```go
// Server 持有: callers map[string]string  // token -> caller_id  (启动时由 cfg 构建)
func (s *Server) authMiddleware(c *rux.Context) {
    tok := bearerToken(c)                          // 现有提取逻辑
    caller, ok := s.lookupCaller(tok)              // 常时间遍历比对
    if !ok { writeError(c, 401, ...); c.Abort(); return }
    c.Set(ctxCallerID, caller)                     // rux 上下文传递(需确认 c.Set/Get;否则用 *http.Request ctx)
    c.Next()
}
// lookupCaller: 对每个已知 token 做 subtle.ConstantTimeCompare, 命中返回其 caller_id
```
> **待确认**：`gookit/rux` 的 `Context` 是否有 `Set/Get`（值传递）。若无，则用 `c.Req = c.Req.WithContext(context.WithValue(...))`，handler 从 `c.Req.Context()` 取。WP 实施时先验证（1 行 spike）。

handler 注入（`job_handler.go:handleCreateJob`）：
```go
req.CallerID = callerFromCtx(c)   // 覆盖客户端传入, 防伪
res, err := s.jobs.Submit(req)
```

### 4.3 C5 设计决策

- `JobRequest` 加 `RequestID string json:"request_id,omitempty"`（客户端可选提供，建议 UUID）。
- **DB 唯一键**为准（持久、跨重启；无 Redis 不引入）：`jobs.request_id` 部分唯一索引（§3）。
- `Submit` 前置查重：有 `request_id` 且已存在 → 直接返回既有 job（幂等），不新建。
- 并发同 request_id：靠唯一索引兜底——`UpsertJob` 插入撞唯一索引则回退查既有返回（见下）。

`jobstore` 新增：
```go
func (s *Store) GetJobByRequestID(reqID string) (JobRecord, bool, error) // WHERE request_id=? (reqID==""→false)
```
`service.go:Submit` 开头（验证后、建目录前）：
```go
if req.RequestID != "" {
    if rec, ok, err := s.meta.GetJobByRequestID(req.RequestID); err != nil {
        return JobResult{}, err
    } else if ok {
        return fromRecord(rec), nil   // 幂等命中, 复用既有 job
    }
}
```
> 竞态：两个并发同 request_id 都未命中前置查 → 都建 job → 第二个 `UpsertJob` 撞部分唯一索引报错。处理：`UpsertJob` 对 request_id 唯一冲突识别（SQLite `ErrConstraint`）后，调用方 `Submit` 回退 `GetJobByRequestID` 返回先到者，并清理自己刚建的空目录。MVP 也可接受"第二个返回冲突错误由客户端重试"——**决策取前者（对用户透明幂等）**，在 §4.5 验收覆盖。

### 4.4 改动清单（Phase A）

| 文件 | 改动 |
|---|---|
| `internal/config/model.go` | `ServerConfig.Callers []CallerConfig` + 类型；保留 `Token` 兼容 |
| `internal/httpapi/auth.go` | 常时间比对；`token→caller` 查找；上下文写 caller_id |
| `internal/httpapi/server.go` | 构建 callers map（启动）；`ctxCallerID` 常量；`callerFromCtx` |
| `internal/httpapi/job_handler.go` | 提交前注入 `req.CallerID` |
| `internal/job/model.go` | `JobRequest`/`JobResult` 加 `CallerID`、`JobRequest` 加 `RequestID` |
| `internal/job/service.go` | `Submit` 前置幂等查；`toRecord/fromRecord` 映射 CallerID/RequestID；唯一冲突回退 |
| `internal/jobstore/jobs.go` | `JobRecord` 加 `CallerID/RequestID`；`UpsertJob` 含新列 + 冲突识别；`GetJobByRequestID`；`ListQuery.Caller` 过滤；`selectCols` 含新列 |
| `internal/jobstore/store.go` | §3 migrate + 部分唯一索引 |

### 4.5 测试与验收（Phase A）

- `jobstore`：①迁移补列（旧库）②`GetJobByRequestID` 往返/空串③唯一索引：同 request_id 第二次 `UpsertJob`（不同 id）报冲突④`ListQuery.Caller` 过滤。
- `job`：①带 `RequestID` 二次 `Submit` 返回**同一 job id**（幂等）②不带 request_id 两次 → 两个 job③`CallerID` 贯穿 persist 入库④并发同 request_id（N goroutine）只产生 1 个 job。
- `httpapi`：①未知 token→401②caller A token→job.caller_id=A③`subtle` 比对（构造等长 token 验证仍拒）。
- 验收：`go test ./... -count=1` 全绿 + `-race` 并发幂等用例零竞争。

---

## 5. Phase B — C3 配置热加载

### 5.1 现状

- `serve.go:runServe`（`serve.go:47`）一次 `Load` → `buildCore`（`assemble.go:46`）装配 project/agent registry + runner map + job.Service；**无信号处理**（grep 无 SIGHUP）。
- `project.Registry`/`agent.Registry` 持 `*config.Config`（启动快照，只读）；`job.Service.cfg` 同。`Add/Remove` 只 `config.Save` 写文件，不改运行内存。
- 在飞 job 的 validate 已过、持有自己的 store，不依赖后续 cfg。

### 5.2 设计决策

- **SIGHUP 触发**重新 `Load` → 原子替换各 registry 的 cfg 指针（不重启、不动在飞 job、不动 jobstore）。
- registry 的 `cfg *config.Config` 改 `atomic.Pointer[config.Config]`；读路径 `cfg.Load()`。`job.Service` 的 cfg 同理或经 registry 取。
- `validate()` 入口**一次性快照** cfg，整个校验用该快照，避免遍历中被替换出现不一致。
- 失败安全：reload 时新配置 `Load` 校验失败 → 保留旧配置、记日志、不替换。

### 5.3 改动清单

| 文件 | 改动 |
|---|---|
| `internal/project/registry.go` | `cfg *config.Config` → `atomic.Pointer[config.Config]`；`Config()/Get` 原子读；`Reload(newCfg)` 原子写 |
| `internal/agent/registry.go` | 同上 |
| `internal/job/service.go` | `cfg` 经原子读或加 `cfg()` 取快照；`validate` 开头 `cfg := s.cfg()` 快照 |
| `internal/commands/assemble.go` | 加 `(*Core).Reload() error`：`config.Load` → 各 registry `Reload` → 替换 Service cfg |
| `internal/commands/serve.go` | `runServe` 起 `signal.Notify(SIGHUP)` goroutine：收到则 `core.Reload()`，并发 select 与 HTTP 停止；优雅退出 |

```go
// serve.go (草图)
sig := make(chan os.Signal, 1)
signal.Notify(sig, syscall.SIGHUP)
go func() {
    for range sig {
        if err := core.Reload(); err != nil { log.Printf("gofer: reload failed, keep old config: %v", err) }
        else { log.Printf("gofer: config reloaded") }
    }
}()
```

### 5.4 测试与验收

- `project/agent registry`：`Reload` 后 `Get` 返回新项目/agent；并发 `Get` 与 `Reload` 无竞争（`-race`）。
- `job.Service`：reload 增删 project 后 `Submit` 对新 project 成功、对已删 project 报 unknown；**在飞 job 不受影响**（reload 中跑完）。
- 端到端（手验/脚本）：serve 运行中改 config 加一个 project → `kill -HUP <pid>` → `GET /v1/projects` 含新项目；日志打印 "config reloaded"。
- 验收：`-race` 全绿；reload 失败时保留旧配置（构造坏配置验证不崩、不替换）。

> 注：`project add` CLI 改的是文件；运行实例仍需 SIGHUP 才感知（或后续做 `add` 后自动通知，本期不做）。

---

## 6. Phase C — C4 日志流控

### 6.1 现状

- SSE `stream_handler.go:handleJobStream`（`stream_handler.go:46`）每 250ms（`streamPollInterval`）`tailFrom` 增量读日志 → 推 `log` 帧；`tailFrom`（`stream_handler.go:234`）**一次读尽新增字节，无上限**。
- `/v1/jobs/{id}/logs` 端点有 256KB 尾部上限（`job_handler.go:maxLogTailBytes`），但 **SSE 路径不受其约束**。
- 日志文件无轮转；磁盘清理靠 retention prune（按时间/计数，C1 SP5）。
- 前端 `web/src/api/sse.ts:streamJob` 累积 buffer，无大小上限（由消费组件决定）。

### 6.2 设计决策

- **SSE 帧字节 cap + 分片**：单帧 ≤ `maxSSEFrameBytes`（1MB），`tailFrom` 的大块按 cap 切多帧（seq 递增，前端按序拼接）。
- **动态节流**：单轮新增超阈值（如 10MB）则下次 tick 延长（250ms→500ms），避免高频大帧。
- **单 job 日志轮转**：`LogWriter`/写入侧超 `maxPerJobLogBytes`（如 500MB）滚动到 `.1`（或截断 + 标记），防单 job 日志文件无界。
- **前端 buffer 上限**：消费侧（sse.ts / 详情组件）保留最近 N MB，超出丢最早（虚拟化展示）。
- **轮转与 tailFrom 协调**：轮转产生 gap，需在 SSE 推一个 `log-rotated` 提示帧或重置 offset；MVP 简化为"轮转后 offset 归零 + 推一条 rotated 标记"，前端清屏续读。

### 6.3 改动清单

| 文件 | 改动 |
|---|---|
| `internal/httpapi/stream_handler.go` | `maxSSEFrameBytes` 常量；`pumpLogs` 大块分片循环；`ticker.Reset` 动态节流；轮转标记帧 |
| `internal/store/filestore.go` | `LogWriter` 写入超 `maxPerJobLogBytes` 轮转；（可选）暴露当前 size 供 stream 侧判断 gap |
| `web/src/api/sse.ts` + 日志展示组件 | buffer 字节上限 + 丢弃最早；rotated 帧清屏 |

```go
// stream_handler.go pumpLogs 分片(草图)
for len(chunk) > maxSSEFrameBytes {
    if err := writeSSE(w, flusher, "log", logFrame{Stream: st, Seq: seq, Text: string(chunk[:maxSSEFrameBytes])}); err != nil { return err }
    chunk = chunk[maxSSEFrameBytes:]; seq++
}
if len(chunk) > 0 { writeSSE(...); seq++ }
```

### 6.4 测试与验收

- `httpapi/stream`：①>1MB 增量被拆成多帧、seq 连续、拼接还原原文②高速写入触发节流（间隔变长，不丢字节）③轮转后推 rotated 标记 + offset 重置、续读不串。
- `store/filestore`：`LogWriter` 超阈值轮转（产生 `.1`、新文件从 0 写）。
- 前端（手验）：灌大日志，内存稳定（buffer 上限生效）、UI 不卡死。
- 验收：`go test ./... -count=1` 全绿；大日志 job 端到端内存不无界（serve RSS 观测）。

---

## 7. 跨阶段注意

- **schema 兼容**：Phase A 用 §3 additive 迁移；不破坏 C1 既有库。
- **向后兼容**：未传 `request_id`/`caller_id`（旧客户端、`Token` 单 token）行为不变。
- **与 ws-worker 协同**：C2 的 per-caller token 机制是 ws-worker **per-worker token**（设计 §9.2/§11）的同款底座——建议 C2 落地时把 token→身份抽象做成 worker 也能复用（worker_id 即一种 caller_id）。
- **测试规约**：单测用 `github.com/gookit/goutil/x/assert`；并发用例补 `-race`（容器已装 gcc）。

## 8. 结论

C2/C5 合并为 Phase A（一次 schema 迁移，价值最高、最小）；C3、C4 独立。均为增量、向后兼容、不破坏 C1。建议 A→B→C，每阶段绿灯即提交。关键风险点：rux 上下文传 caller（待 1 行 spike 确认）、C5 并发幂等的唯一冲突回退、C4 轮转与 SSE offset 协调——均在各阶段验收覆盖。
