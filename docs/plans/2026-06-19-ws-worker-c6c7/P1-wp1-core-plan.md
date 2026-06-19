# P1 · WP1 核心实施计划（端到端远程执行）

> 主文档（契约真源）：[`./2026-06-19-ws-worker-c6c7-plan.md`](./2026-06-19-ws-worker-c6c7-plan.md)（§4 包布局 / §5 帧表 / §6 配置 / §7 鉴权 / §8 WorkerID / §9 跨阶段约束）。
> 主设计：[`../../design/2026-06-17-ws-remote-worker-design.md`](../../design/2026-06-17-ws-remote-worker-design.md)（§8 流程 / §9 数据模型 / §10 模块 / §17 评审 #1/#2/#3/#8）。
> 前置硬门：[`./P0-spike-plan.md`](./P0-spike-plan.md)（coder/websocket 在 gookit/rux v2 上 `Accept`，origin/option 已验通）。
> 构建环境：`export PATH=/path/to/ws-root/linux-env/sdk/gosdk/go1.25.10/bin:$PATH; cd tools/gofer`

本子文档把主文档 P1 行（主 §3）落到可执行细节：file:line 改动点、关键代码片段、关键流程时序、测试与验收。**实施者无需再猜**。

---

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-19 | Claude | 初版：WP1 端到端远程执行；含 `wsproto`/`wshub`/`runner/worker`/`worker`/`commands/worker.go` 全量改动点 + register/dispatch/log-mirror/result 时序 + 评审 #1（token 绑定）/#2（sink 生命周期+有序）/#3（背压）/#8（worker 再校验）落地细节 + WorkerID 贯穿（无 schema 迁移）。 |

---

## 2. 范围（WP1 做 / 不做）

**做**（端到端单 worker 远程执行闭环）：
- `internal/wsproto`：envelope + **全量**帧类型常量/结构（主 §5；交互/ping 帧本期声明占位，impl 在 P2/P3）。
- `internal/wshub`：`WorkerRegistry` + `GET /v1/workers/connect` 的 `Accept` + register→registered 握手（含 **per-worker token 绑定**，评审 #1）+ **单连接单读循环**按 `job_id` demux **有序**投递到 job sink（评审 #2，绝不 goroutine-per-frame）+ `Dispatch` 下发。心跳钩子**声明但 impl 延后 P3**。
- `internal/runner/worker`：`workerRunner` 实现 `runner.Runner`；`Run` **先注册 sink 再发 dispatch**（评审 #2）→ 把 `log` 帧写入 `req.Stdout/Stderr`（与 local 同一 store 文件，HTTP `/logs`+SSE 零改动）→ 等 `result` 返回；**C4 同款 per-job 有界缓冲 + 节流/丢尾 + 截断标记**（评审 #3）。
- `internal/worker`（客户端）：连接+register → 收 `dispatch` → 调**自己的** `job.Service.Submit` 再校验（评审 #8）→ 推 `log`/`status`/`result`。
- `internal/commands/worker.go`：`gofer worker --config worker.yaml`。
- 配线：`config/model.go`（server `workers` + `worker` runner 类型 + worker.yaml 结构）/ `assemble.go buildCore`（hub 单例）/ `job/service.go`（`isRemoteRunner` 分支 + WorkerID 校验/映射）/ `job/model.go`（WorkerID 字段）/ `httpapi/server.go`（挂 WS 路由）。

**不做**（推迟到后续阶段，结构占位即可）：
- 运行中交互透传、cancel→worker、timeout 通知 worker（P2）。
- 心跳/读截止/重连/worker-lost 在飞 job 处理/多 worker 弹性、worker 端多地址退避（P3 / C7）。
- `/v1/runners` 可观测（P4 / C6）。

**显式不做（全程 out-of-scope，主 §3）**：多 hub HA / 跨 hub 接管 / seq-offset 续传重放 / WP4 标签自动调度 + Web 仪表盘。

---

## 3. 改动清单（file → 改动表）

> 路径相对 `tools/gofer/`。**新增**=新文件；**改**=既有文件改动点（file:line 为当前快照）。

### 3.1 新增包/文件

| 文件 | 类型 | 改动 |
|---|---|---|
| `internal/wsproto/envelope.go` | 新增 | `Envelope` 结构 + 帧 type 常量（全量，§4.1）+ `Marshal`/`Unmarshal`/`Decode` 助手 |
| `internal/wsproto/frames.go` | 新增 | 各帧 payload 结构：`Register`/`Registered`/`Dispatch`/`Log`/`Status`/`Result`（P1）+ `Cancel`/`Interaction`/`Answer`/`Ping`/`Pong`（占位，P2/P3）|
| `internal/wshub/registry.go` | 新增 | `WorkerRegistry`（worker_id→`*workerConn`+meta，并发安全）|
| `internal/wshub/hub.go` | 新增 | `Hub` 单例：`Accept` 入口、register 握手+token 绑定校验、单读循环 demux、`Dispatch`、sink 注册/注销 API、心跳钩子声明 |
| `internal/wshub/upgrade_writer.go` | 新增 | `wsUpgradeWriter`（P0 硬性产出）：包装 rux `c.Resp`，hijack 前立即下发 101，否则握手静默挂起。从 spike_test.go 提升（§4.2.2）|
| `internal/wshub/sink.go` | 新增 | per-job sink 接口（hub→workerRunner 解耦），背压有界缓冲实现可放此或放 runner（见 §4.3 决策）|
| `internal/runner/worker/runner.go` | 新增 | `workerRunner` 实现 `runner.Runner`；`New(name, workerID, hub)`；`Run` 生命周期 |
| `internal/worker/client.go` | 新增 | worker 客户端：dial+register、收 dispatch、推帧 |
| `internal/worker/dispatch.go` | 新增 | 收 `dispatch`→构造 `job.JobRequest`→`Submit`→把本地 job 日志/状态/结果推回 hub（镜像写出器适配 `req.Stdout`?见 §4.4）|
| `internal/commands/worker.go` | 新增 | `NewWorkerCmd()`：`gofer worker --config` |

### 3.2 既有文件改动

| 文件:line | 当前 | 改动 |
|---|---|---|
| `internal/config/model.go:27-41` `ServerConfig` | 无 worker 段 | 加 `Workers map[string]WorkerAuthConfig` yaml:`workers`（§4.6）|
| `internal/config/model.go:46-50` 附近 | `CallerConfig` | 加 `WorkerAuthConfig{TokenEnv,Token,Labels}` 结构 |
| `internal/config/model.go:142-146` `RunnerConfig` | `Type/BaseURL/TokenEnv` | 加 `WorkerID string yaml:"worker_id"`（type=worker 时用）|
| `internal/config/model.go` 末尾 | — | 加 `WorkerConfig`（worker.yaml 顶层，§4.6）|
| `internal/job/model.go:7-24` `JobRequest` | 无 WorkerID | 加 `WorkerID string json:"worker_id,omitempty"` |
| `internal/job/model.go:27-55` `JobResult` | 无 WorkerID | 加 `WorkerID string json:"worker_id,omitempty"` |
| `internal/job/service.go:152` | `remote := isPeerRunner(cfg, req.Runner)` | 改 `remote := isRemoteRunner(cfg, req.Runner)`（合并 peer-http+worker）|
| `internal/job/service.go:212-237` runReq 构建 | Forward 仅 peer | worker 分支：设 `Forward`（PeerRunner=local）+ `Interactions`（P2 用，先设占位）+ 把 `req.WorkerID` 带入 entry.result（§4.5）|
| `internal/job/service.go:243-255` `entry.result` | 无 WorkerID | 加 `WorkerID: req.WorkerID` |
| `internal/job/service.go:417-435` `toRecord` | 注释 "WorkerID stays empty" | 改 `WorkerID: r.WorkerID`，删/改注释 |
| `internal/job/service.go:439-457` `fromRecord` | 无 WorkerID | 加 `WorkerID: rec.WorkerID` |
| `internal/job/service.go:483-486` `isPeerRunner` | — | 新增 `isWorkerRunner` + `isRemoteRunner`（§4.5）|
| `internal/job/service.go:497-528` `validate` | 不校验 worker_id | type=worker 时校验 `req.WorkerID` 必填且在 `cfg.Server.Workers` 内（§4.5）|
| `internal/commands/assemble.go:49-58` `buildCore` | 仅注册 peer-http | **先建 hub 单例一次**，遍历 runners：type=worker 注册 `workerrunner.New(name, rc.WorkerID, hub)`；hub 挂进 Core（§4.7）|
| `internal/commands/assemble.go:22-29` `Core` | 无 Hub 字段 | 加 `Hub *wshub.Hub`（serve 用于挂路由）|
| `internal/httpapi/server.go:82-95` `New` | 无 hub | New 增参 `hub *wshub.Hub`（或经 Core 注入；§4.8）|
| `internal/httpapi/server.go:126-167` `buildRouter` | 无 WS 路由 | `/v1/workers/connect` 挂 `s.hub.Accept`，**走 Bearer 但绕 JSON 信封/Web fallback**（§4.8）|
| `internal/commands/serve.go:100` `httpapi.New(...)` | 不传 hub | 传 `core.Hub` |
| `internal/commands/app.go:15-19` | 未注册 worker 命令 | `app.Add(NewWorkerCmd())` |
| `go.mod` | 无 websocket | 加 `github.com/coder/websocket v1.8.x`（P0 已选型）|

> **无 jobstore 迁移**：`jobs.worker_id TEXT` 列在 C1 schema 内已落地，`internal/jobstore/jobs.go` 的 `JobRecord.WorkerID`（行 38）、`selectCols COALESCE(worker_id,'')`（行 70）、`scanJob &r.WorkerID`（行 84）、`UpsertJob` INSERT+ON CONFLICT 的 `worker_id`（行 105/112/129）**全部已含**。本期**仅** job 包侧加字段 + `toRecord/fromRecord` 映射，**不动 jobstore**。

---

## 4. 设计细节与关键代码片段（sketch）

> 下面均为**示意骨架**（命名/签名为契约），实现时补全错误处理/注释。

### 4.1 `internal/wsproto`（envelope + 全量帧）

主 §5 帧表一次性定全（评审 #6：避免破坏性改协议）。envelope 用「双段解码」：先解 `type`+`job_id`，再按 type 解 payload。

```go
package wsproto

// FrameType is the envelope discriminator (主 §5).
type FrameType string

const (
    TypeRegister    FrameType = "register"    // w→s  P1
    TypeRegistered  FrameType = "registered"  // s→w  P1
    TypeDispatch    FrameType = "dispatch"    // s→w  P1
    TypeLog         FrameType = "log"         // w→s  P1
    TypeStatus      FrameType = "status"      // w→s  P1
    TypeResult      FrameType = "result"      // w→s  P1
    TypeCancel      FrameType = "cancel"      // s→w  P2（占位）
    TypeInteraction FrameType = "interaction" // w→s  P2（占位）
    TypeAnswer      FrameType = "answer"      // s→w  P2（占位）
    TypePing        FrameType = "ping"        // both P3（占位）
    TypePong        FrameType = "pong"        // both P3（占位）
)

// Envelope is the single-connection multiplexed message. Payload carries the
// type-specific body (raw JSON), demuxed by JobID on the hub.
type Envelope struct {
    Type    FrameType       `json:"type"`
    JobID   string          `json:"job_id,omitempty"`
    Payload json.RawMessage `json:"payload,omitempty"`
}

// Register (w→s, P1): worker announces identity + capability snapshot.
type Register struct {
    WorkerID      string   `json:"worker_id"`
    Labels        []string `json:"labels,omitempty"`
    Projects      []string `json:"projects,omitempty"` // 展示/可选预校验提示（评审 #8：真正校验在 worker 本地）
    Agents        []string `json:"agents,omitempty"`
    MaxConcurrent int      `json:"max_concurrent,omitempty"`
}

// Registered (s→w, P1): handshake ack.
type Registered struct {
    Accepted   bool   `json:"accepted"`
    Reason     string `json:"reason,omitempty"`
    ServerTime int64  `json:"server_time"` // 毫秒（SR102 口径，与 /v1 一致）
}

// Dispatch (s→w, P1): job assignment = JobRequest 投影（不含 worker_id，worker 本地执行）.
type Dispatch struct {
    JobID      string   `json:"job_id"`
    ProjectKey string   `json:"project_key"`
    Agent      string   `json:"agent"`
    Runner     string   `json:"runner"` // 恒为 "local"（worker 本地执行位置）
    Prompt     string   `json:"prompt,omitempty"`
    Cmd        []string `json:"cmd,omitempty"`
    Cwd        string   `json:"cwd,omitempty"`
    TimeoutSec int      `json:"timeout_sec,omitempty"`
}

// Log (w→s, P1): incremental log frame. Seq 单调递增（与 C4 SSE seq 同义，保序基准）.
type Log struct {
    JobID  string `json:"job_id"`
    Stream string `json:"stream"` // "stdout" | "stderr"
    Seq    int    `json:"seq"`
    Text   string `json:"text"`
}

// Status (w→s, P1): optional status hint; result 为权威终态.
type Status struct {
    JobID  string `json:"job_id"`
    Status string `json:"status"`
}

// Result (w→s, P1): authoritative terminal outcome.
type Result struct {
    JobID    string `json:"job_id"`
    Status   string `json:"status"`
    ExitCode int    `json:"exit_code"`
    Error    string `json:"error,omitempty"`
}

// 占位（结构定全，impl 留 P2/P3）:
type Cancel struct{ JobID string `json:"job_id"` }
type Interaction struct {
    JobID       string          `json:"job_id"`
    Action      string          `json:"action"` // open|answered|cancelled
    Interaction json.RawMessage `json:"interaction"` // 复用 job.Interaction wire（P2 解）
}
type Answer struct {
    JobID         string `json:"job_id"`
    InteractionID string `json:"interaction_id"`
    Answer        string `json:"answer"`
}
type Ping struct{ TS int64 `json:"ts"` }
type Pong struct{ TS int64 `json:"ts"` }
```

助手：

```go
// EncodeFrame marshals a typed payload into an Envelope's JSON bytes.
func EncodeFrame(t FrameType, jobID string, payload any) ([]byte, error)

// DecodeEnvelope unmarshals raw bytes into an Envelope (type + job_id + raw payload).
func DecodeEnvelope(b []byte) (Envelope, error)

// As decodes env.Payload into v (typed payload).
func As[T any](env Envelope) (T, error)
```

> `wsproto` **无对 job/runner 的依赖**（主 §4：与 worker 共享、纯叶子包）。交互帧 payload 用 `json.RawMessage` 持有，P2 再在 hub/worker 侧解成 `job.Interaction`，避免 wsproto 引入 job。

### 4.2 `internal/wshub`（registry + 单读循环 demux + dispatch + token 绑定）

#### 4.2.1 WorkerRegistry

```go
package wshub

// workerConn is one live worker connection + its register meta.
type workerConn struct {
    workerID string
    callerID string // 认证身份（=token 绑定的 worker_id），评审 #1
    conn     *websocket.Conn
    meta     wsproto.Register

    writeMu sync.Mutex // 串行化出站写（coder/websocket 要求单写者）

    mu    sync.Mutex
    sinks map[string]JobSink // job_id → sink（评审 #2 生命周期）
}

// WorkerRegistry maps worker_id → live conn, concurrency-safe.
type WorkerRegistry struct {
    mu    sync.RWMutex
    conns map[string]*workerConn
}

func (r *WorkerRegistry) Put(wc *workerConn) (old *workerConn)  // 同 id 重注册：返回旧 conn 供优雅关闭（约束 #5）
func (r *WorkerRegistry) Get(workerID string) (*workerConn, bool)
func (r *WorkerRegistry) Remove(workerID string, wc *workerConn) // 仅当当前是 wc 才删（避免误删替换后的新 conn）
```

#### 4.2.2 Hub + Accept + register 握手 + token 绑定（评审 #1）

```go
// Hub is the serve-process singleton: WS accept + registry + per-job demux.
type Hub struct {
    reg       *WorkerRegistry
    authorize func(token string) (callerID string, ok bool) // 注入 server 的 lookupCaller（auth.go:57）
    bindings  map[string]string // worker_id → 期望 caller/token-id（来自 cfg.Server.Workers，评审 #1）
}

// Accept upgrades GET /v1/workers/connect and runs the per-conn read loop.
// 鉴权已在路由层（httpapi）做 Bearer 校验并把 callerID 透传进来；这里只做
// worker_id↔caller 绑定校验（评审 #1）。
func (h *Hub) Accept(w http.ResponseWriter, req *http.Request, callerID string) {
    // ⚠️ P0 实证（硬性）：rux 的 *responseWriter 缓冲 WriteHeader（真正下发在链尾
    // ensureWriteHeader，而 Hijack 把它变成 no-op）→ 若把 rux 的 c.Resp 裸传给 Accept，
    // 101 握手行永不写到 socket，客户端 Dial 静默超时挂起。必须用 wsUpgradeWriter 包装：
    // 在 hijack 前立即下发 WriteHeader(101)，其余 Header/Hijack/Flush 透传 c.Resp。
    // 该适配器已在 P0 spike(internal/wshub/spike_test.go) 验通，WP1 提升为正式文件
    // internal/wshub/upgrade_writer.go。**绝不能把 c.Resp 裸传给 Accept。**
    c, err := websocket.Accept(&wsUpgradeWriter{rw: w}, req, &websocket.AcceptOptions{
        // worker 是非浏览器客户端：必须显式放开 origin，否则 Accept 拒绝（约束 #3 / 设计 §13）。
        // 注意 OriginPatterns 的 "*" 被库禁用，P0 已定用 InsecureSkipVerify。
        InsecureSkipVerify: true,
        CompressionMode:    websocket.CompressionDisabled, // P0 锁定
    })
    if err != nil { return }
    c.SetReadLimit(maxWSReadBytes) // P0 锁定：防单帧超大撑爆内存（背压另见 §4.3）
    ctx := req.Context()

    // 1) 读首帧，必须是 register
    env, err := readEnvelope(ctx, c)
    if err != nil || env.Type != wsproto.TypeRegister { _ = c.Close(...); return }
    reg, _ := wsproto.As[wsproto.Register](env)

    // 2) token↔worker 绑定（评审 #1，MVP 强制）：register.worker_id 必须等于该 token 绑定的 worker
    want, bound := h.bindings[reg.WorkerID]
    if !bound || want != callerID {
        _ = writeFrame(ctx, c, wsproto.TypeRegistered, "", wsproto.Registered{
            Accepted: false, Reason: "worker_id not bound to this token", ServerTime: nowMillis(),
        })
        _ = c.Close(websocket.StatusPolicyViolation, "binding")
        return
    }

    wc := &workerConn{workerID: reg.WorkerID, callerID: callerID, conn: c, meta: reg, sinks: map[string]JobSink{}}

    // 3) 同 worker_id 重注册：替换旧 conn（仅同 token 已由上面绑定保证；约束 #5）
    if old := h.reg.Put(wc); old != nil { old.gracefulClose("replaced") }

    // 4) registered{accepted:true}
    _ = writeFrame(ctx, c, wsproto.TypeRegistered, "", wsproto.Registered{Accepted: true, ServerTime: nowMillis()})

    // 5) 单连接单读循环（评审 #2：绝不 goroutine-per-frame）
    h.readLoop(ctx, wc)
    h.reg.Remove(reg.WorkerID, wc)
}
```

**`wsUpgradeWriter`（P0 硬性产出，从 spike 提升）** —— `internal/wshub/upgrade_writer.go`：~30 行，包装 rux 的 `c.Resp`，使 `websocket.Accept` 的握手能下发：
```go
// wsUpgradeWriter forces rux's deferred WriteHeader to flush the 101 line
// BEFORE coder/websocket hijacks the conn (rux buffers WriteHeader; Hijack
// would otherwise turn the deferred flush into a no-op → 101 never sent).
type wsUpgradeWriter struct{ rw http.ResponseWriter }
func (u *wsUpgradeWriter) Header() http.Header { return u.rw.Header() }
func (u *wsUpgradeWriter) Write(b []byte) (int, error) { return u.rw.Write(b) }
func (u *wsUpgradeWriter) WriteHeader(code int) {
    u.rw.WriteHeader(code)
    if f, ok := u.rw.(http.Flusher); ok { f.Flush() } // 立即把 101 推到 socket
}
func (u *wsUpgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
    return u.rw.(http.Hijacker).Hijack() // 透传给 rux 的 responseWriter（已实现 Hijack）
}
func (u *wsUpgradeWriter) Flush() { if f, ok := u.rw.(http.Flusher); ok { f.Flush() } }
```
> 以 P0 spike 验通的实现为准（spike_test.go 里的版本，正/负 origin 测试 + `-race` 均过）。WP1 把它移到非 `_test.go` 文件并补单测。

#### 4.2.3 单读循环 + 按 job_id demux + 有序投递（评审 #2，核心不变量）

```go
// readLoop 是该连接唯一的读 goroutine：顺序读帧，按 job_id 投递到对应 sink。
// 不可每帧起 goroutine（否则 log/result 乱序，且与背压冲突）。
func (h *Hub) readLoop(ctx context.Context, wc *workerConn) {
    for {
        env, err := readEnvelope(ctx, wc.conn)
        if err != nil { return } // 断线/ctx 结束 → 上层 Remove；在飞 sink 由 workerRunner 侧感知（P3 才做 worker-lost；P1 阶段连接稳定）
        switch env.Type {
        case wsproto.TypeLog:
            lf, _ := wsproto.As[wsproto.Log](env)
            if sk := wc.sink(env.JobID); sk != nil { sk.WriteLog(lf.Stream, lf.Seq, lf.Text) } // 同 job 串行（同一读循环天然有序）
        case wsproto.TypeStatus:
            // P1：status 仅信息性，result 为权威；记录但不驱动终态
        case wsproto.TypeResult:
            rf, _ := wsproto.As[wsproto.Result](env)
            if sk := wc.sink(env.JobID); sk != nil { sk.Finish(rf) } // 解锁 workerRunner.Run 的等待
        case wsproto.TypeInteraction:
            // P2：交互透传（占位）
        case wsproto.TypePong:
            // P3：心跳（占位）
        }
    }
}
```

**有序保证来源**：单连接**仅一个读 goroutine**，对同一 `job_id` 的 `log…log…result` 帧按读到顺序投递；sink 内对该 job 的写也是串行（同一 goroutine 调用）。因此 `result` 永远在该 job 全部已读 `log` 之后被处理——**无需 per-frame goroutine，无需 per-job 队列**（读循环本身即串行队列）。

> ⚠️ 实现红线：背压（§4.3）只允许在 sink **内部**对**满缓冲**做节流/丢尾，**不得**把单帧投递异步化到新 goroutine——否则破坏上面的有序不变量。

#### 4.2.4 sink 注册/注销 + Dispatch

```go
// RegisterSink 由 workerRunner 在发 dispatch 之前调用（评审 #2：先注册后派发）。
func (h *Hub) RegisterSink(workerID, jobID string, sk JobSink) error {
    wc, ok := h.reg.Get(workerID)
    if !ok { return ErrWorkerOffline }
    wc.mu.Lock(); wc.sinks[jobID] = sk; wc.mu.Unlock()
    return nil
}
func (h *Hub) DeregisterSink(workerID, jobID string) { /* delete(wc.sinks, jobID) */ }

// Dispatch 发送 dispatch 帧到指定 worker（出站写需持 wc.writeMu）。
func (h *Hub) Dispatch(workerID string, d wsproto.Dispatch) error {
    wc, ok := h.reg.Get(workerID)
    if !ok { return ErrWorkerOffline }
    return wc.writeFrame(wsproto.TypeDispatch, d.JobID, d)
}

// 心跳钩子：声明，impl 延后 P3。
func (h *Hub) startHeartbeat(wc *workerConn) { /* P3: ping/pong + read deadline（设计 §17 #7）*/ }
```

### 4.3 背压（评审 #3，复用 C4 思路）— 在 sink 侧

C4 模板见 `internal/httpapi/stream_handler.go`：`maxSSEFrameBytes`（行 29）/`streamThrottleBytes`（行 34）/`log-rotated`（行 49-56）。WS 读侧把同款策略落在 **per-job sink** 的写出路径（一个话痨 job 不得卡住单连接读循环 → 否则同 worker 全部 job 一起卡）。

```go
// JobSink 把 hub 读到的 worker 帧落到 server 侧 job 的镜像写出器 + 等待终态。
type JobSink interface {
    WriteLog(stream string, seq int, text string) // 有界缓冲；满则丢尾+标记截断
    Finish(wsproto.Result)                          // 唤醒 workerRunner.Run 的等待
}

// boundedSink: workerRunner 构造，持有 req.Stdout/req.Stderr（= store.LogWriter 文件）。
type boundedSink struct {
    stdout, stderr io.Writer
    resultCh       chan wsproto.Result // buffered 1
    // 背压：每流有界（pending 字节阈值）。WriteLog 直接同步写文件（落盘快、不阻塞读循环）；
    // 仅当写出器返回阻塞/慢（极端）时启用丢尾——MVP 用「单帧 cap + 累计阈值」近似 C4：
    truncated bool
    written   int64
}

const (
    maxWSFrameBytes  = 1 << 20  // 单 log 帧 text 上限（对齐 C4 maxSSEFrameBytes）
    sinkDropTailMark = "\n[gofer: log truncated by worker back-pressure]\n"
)

func (s *boundedSink) WriteLog(stream string, _ int, text string) {
    w := s.stdout; if stream == "stderr" { w = s.stderr }
    if w == nil || text == "" { return }
    if len(text) > maxWSFrameBytes { text = text[:maxWSFrameBytes] } // cap 单帧
    _, _ = io.WriteString(w, text) // 同步写盘：复用 local runner 同款管道，HTTP/SSE 零改动
}
func (s *boundedSink) Finish(r wsproto.Result) { select { case s.resultCh <- r: default: } }
```

> **MVP 背压口径（与 C4 对齐、显式 TODO 标注）**：单 log 帧 text cap 到 `maxWSFrameBytes`；写盘是同步且快（FileStore append），单读循环不会长时间阻塞。**更强的「per-job 异步有界缓冲 + 满则节流」**留作 P3 强化（设计 §15 「超大/高频日志背压」TODO）。**WP1 验收只需证明**：话痨 job 不会导致同连接其他 job 的帧停滞、不会无界内存增长（见 §6 背压测试）。
> 决策记录：sink 写盘同步化 + 单帧 cap 是 WP1 选定口径（避免引入与评审 #2 有序冲突的异步缓冲）；若验收发现单 job 大量小帧拖慢读循环，再上 P3 的有界 channel。

### 4.4 `internal/worker`（客户端：连接/注册/收 dispatch/推帧）

worker = 「反向接入的 bridge」：内部仍用自己的 `job.Service`/`local` runner（设计 §6）。把「server 把 worker log 推回」反过来——worker 跑本地 job，用一个**把 store 写出转成 WS log 帧**的适配器。

```go
package worker

type Client struct {
    cfg  *config.WorkerConfig
    core *commands.Core // worker 本地装配（projects/agents/runners=local/job.Service）
    conn *websocket.Conn
    writeMu sync.Mutex
}

// Run dials 单 URL（P1 多地址保留给 C7/P3）、register、进入收 dispatch 循环。
func (cl *Client) Run(ctx context.Context) error {
    c, _, err := websocket.Dial(ctx, cl.cfg.ServerLink.URL(), &websocket.DialOptions{
        HTTPHeader: http.Header{"Authorization": {"Bearer " + cl.token()}},
    })
    if err != nil { return err }
    cl.conn = c
    // register
    _ = cl.writeFrame(ctx, wsproto.TypeRegister, "", wsproto.Register{
        WorkerID: cl.cfg.WorkerID, Labels: cl.cfg.Labels,
        Projects: keys(cl.cfg.Projects), Agents: keys(cl.cfg.Agents),
        MaxConcurrent: cl.cfg.MaxConcurrent,
    })
    env, _ := readEnvelope(ctx, c)
    if reg, _ := wsproto.As[wsproto.Registered](env); !reg.Accepted {
        return fmt.Errorf("register rejected: %s", reg.Reason)
    }
    // 收 dispatch 循环（单读 goroutine）
    for {
        env, err := readEnvelope(ctx, c)
        if err != nil { return err } // P1：直接退出（重连留 P3/C7）
        if env.Type == wsproto.TypeDispatch {
            d, _ := wsproto.As[wsproto.Dispatch](env)
            go cl.handleDispatch(ctx, d) // worker 侧多 job 并行，每 job 一个执行 goroutine（与 hub 单读不同）
        }
        // cancel/answer/ping → P2/P3 占位
    }
}
```

```go
// handleDispatch 调本地 job.Service.Submit（评审 #8：用 worker 自己的配置再校验 project/agent/exec/SafeJoin），
// 然后把该 job 的日志/状态/结果推回 hub。
func (cl *Client) handleDispatch(ctx context.Context, d wsproto.Dispatch) {
    res, err := cl.core.Jobs.Submit(job.JobRequest{
        ProjectKey: d.ProjectKey, Agent: d.Agent, Runner: builtinLocalRunner, // 恒 local
        Prompt: d.Prompt, Cmd: d.Cmd, Cwd: d.Cwd, TimeoutSec: d.TimeoutSec,
        // 注意：用 d.JobID 作为 server 侧 job_id 回传键；worker 本地 job 另有自己的 id
    })
    if err != nil {
        // 评审 #8：本地校验失败 → 直接推 result{failed}，无需新帧（设计 §17 #8）
        _ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{JobID: d.JobID, Status: "failed", ExitCode: -1, Error: err.Error()})
        return
    }
    localID := res.ID
    // 用 worker 的 SSE/tail 读路径把本地 job 输出转成 log 帧推回（见下）
    cl.streamLocalJob(ctx, localID, d.JobID)
    // 终态
    final, _ := cl.core.Jobs.Wait(localID)
    _ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{
        JobID: d.JobID, Status: final.Status, ExitCode: final.ExitCode, Error: final.Error,
    })
}
```

**worker 侧日志转发实现选项**（实现者择一，推荐 A）：
- **A（推荐，零侵入）**：worker 复用自己的 `job.Service.Wait` + 增量读本地 `store` 日志文件（同 `tailFrom` 思路，`stream_handler.go:309`），边读边推 `log` 帧。worker 内部即一个 mini SSE 消费者，但**进程内直读文件**（不经 HTTP）。
- **B**：给 `job.Service.Submit` 暴露一个可选 log-tap 回调（侵入 job 包）——**不选**，避免污染既有逻辑。

> `streamLocalJob` 用 A：轮询本地 `<result_dir>/<localID>/stdout.log|stderr.log` 增量字节（`tailFrom` 同款），生成单调 `seq`，推 `log` 帧；job 终态后停。worker 侧也天然保留自己的本地记录（设计 §6「客户端也有任务记录」）。

### 4.5 `internal/runner/worker`（workerRunner）+ job 服务分支

#### 4.5.1 workerRunner.Run 生命周期（评审 #2：先注册 sink 后 dispatch）

```go
package worker // import path internal/runner/worker（包名避免与 internal/worker 冲突：用 workerrunner 别名）

type Runner struct {
    name     string
    workerID string
    hub      *wshub.Hub
}

func New(name, workerID string, hub *wshub.Hub) *Runner { return &Runner{name, workerID, hub} }
func (r *Runner) Name() string { return r.name }

func (r *Runner) Run(ctx context.Context, req runner.Request) runner.Result {
    if req.Forward == nil { return runner.Result{ExitCode: -1, Err: errors.New("worker runner requires forward request")} }

    sink := &boundedSink{stdout: req.Stdout, stderr: req.Stderr, resultCh: make(chan wsproto.Result, 1)}

    // (a) 先注册 sink（评审 #2：否则首批 log 帧抢在 sink 前丢）
    if err := r.hub.RegisterSink(r.workerID, req.JobID, sink); err != nil {
        return runner.Result{ExitCode: -1, Err: err} // worker 离线
    }
    defer r.hub.DeregisterSink(r.workerID, req.JobID) // (e) 终态/错误注销

    // (b) 发 dispatch
    d := wsproto.Dispatch{
        JobID: req.JobID, ProjectKey: req.Forward.ProjectKey, Agent: req.Forward.Agent,
        Runner: "local", Prompt: req.Forward.Prompt, Cmd: req.Forward.Cmd,
        Cwd: req.Forward.Cwd, TimeoutSec: req.Forward.TimeoutSec,
    }
    if err := r.hub.Dispatch(r.workerID, d); err != nil {
        return runner.Result{ExitCode: -1, Err: err}
    }

    // (c)(d) hub 读循环把 log 帧写进 sink（=req.Stdout/Stderr）；这里等 result
    select {
    case res := <-sink.resultCh:
        return runner.Result{ExitCode: res.ExitCode, Err: errFromResult(res)}
    case <-ctx.Done():
        // P1：ctx 取消/超时 → 直接返回（cancel→worker 帧留 P2）。job 服务据 ctx 分类 timeout/cancelled。
        return runner.Result{ExitCode: -1, Err: ctx.Err()}
    }
}
```

> `errFromResult` 仿 `peerhttp.errFromStatus`（`peerhttp/runner.go:251`）：done→nil；failed/timeout/cancelled→error。host 侧仍用自己 ctx 分类（`classify` `service.go:461`），与 peer-http 一致。

#### 4.5.2 job 服务分支（service.go）

```go
// isWorkerRunner / isRemoteRunner（service.go:483 附近）
func isWorkerRunner(cfg *config.Config, name string) bool {
    rc, ok := cfg.Runners[name]; return ok && rc.Type == "worker"
}
func isRemoteRunner(cfg *config.Config, name string) bool {
    return isPeerRunner(cfg, name) || isWorkerRunner(cfg, name)
}
```

`Submit`（service.go:152）改 `remote := isRemoteRunner(cfg, req.Runner)`。两类 remote 共用「跳过本地 agent/cwd 解析 + 设 Forward」逻辑（service.go:175/213-224 已具备）。

`validate`（service.go:497）末尾追加 worker 专属校验：

```go
// worker runner 必须带已知的 worker_id（评审 #1：worker_id 即 caller 身份的一部分）
if isWorkerRunner(cfg, req.Runner) {
    if req.WorkerID == "" {
        return config.ProjectConfig{}, fmt.Errorf("%w: worker_id is required for worker runner", ErrInvalidRequest)
    }
    if _, ok := cfg.Server.Workers[req.WorkerID]; !ok {
        return config.ProjectConfig{}, fmt.Errorf("%w: unknown worker_id %q", ErrInvalidRequest, req.WorkerID)
    }
}
```

`entry.result`（service.go:243）加 `WorkerID: req.WorkerID`；`toRecord`（417）/`fromRecord`（439）加 `WorkerID` 映射（删除 416 行 "WorkerID stays empty" 注释）。

> ⚠️ **约定**：`workerRunner` 的 `workerID` 既来自 runner 配置 `rc.WorkerID`（service 不知道具体 worker），也由 `req.WorkerID` 校验。两者必须一致——**WP1 让 runner 自己持有配置 `worker_id`（一个 worker runner = 一个 worker）**，`req.WorkerID` 仅用于 validate + 落库展示。若需「一个 runner 名指向动态 worker_id」是 WP4 调度范畴，本期不做。

### 4.6 配置面（config/model.go）

```go
// ServerConfig 加（model.go:36 后）:
Workers map[string]WorkerAuthConfig `yaml:"workers"` // per-worker token 绑定（§7 / 评审 #1）

// WorkerAuthConfig: server 侧登记一个合法 worker 身份。
type WorkerAuthConfig struct {
    Token    string   `yaml:"token"`     // 字面 token（少用）
    TokenEnv string   `yaml:"token_env"` // 推荐：从 env 读，token 不入仓
    Labels   []string `yaml:"labels"`    // 展示/校验提示（WP4 才自动调度）
}

// RunnerConfig 加（model.go:146）:
WorkerID string `yaml:"worker_id"` // type=worker：该 runner 指向的 worker

// worker.yaml 顶层（新结构）:
type WorkerConfig struct {
    WorkerID      string                          `yaml:"worker_id"`
    ServerLink    WorkerServerLink                `yaml:"server_link"`
    Projects      map[string]ProjectConfig        `yaml:"projects"` // worker 本地 host_path
    Agents        map[string]AgentConfig          `yaml:"agents"`
    Runners       map[string]RunnerConfig         `yaml:"runners"`  // 通常仅 local
    MaxConcurrent int                             `yaml:"max_concurrent"`
    Labels        []string                        `yaml:"labels"`
    Storage       StorageConfig                   `yaml:"storage"` // worker 本地 result_dir/db
}
type WorkerServerLink struct {
    URLs      []string `yaml:"urls"`      // 多地址（C7 用）；P1 用 URLs[0]
    TokenEnv  string   `yaml:"token_env"`
    Token     string   `yaml:"token"`
    Reconnect ReconnectConfig `yaml:"reconnect"` // C7 占位（P1 不消费）
}
type ReconnectConfig struct {
    InitialMS int     `yaml:"initial_ms"`
    MaxMS     int     `yaml:"max_ms"`
    Jitter    float64 `yaml:"jitter"`
}
```

server 端 token→worker 绑定表（hub.bindings）由 buildCore 从 `cfg.Server.Workers` 解析：每个 `WorkerAuthConfig` 解析出 token（同 `buildCallers` 逻辑，server.go:102），把该 token 也加入 `callers`（caller_id = worker_id），并记 `bindings[worker_id]=worker_id`。这样 `lookupCaller`（auth.go:57）返回的 callerID 就是 worker_id，hub.Accept 里直接比对 `callerID == reg.WorkerID`。

> **复用 C2 底座（评审 #1）**：worker token 与 caller token 共用同一「token→身份」比对（`crypto/subtle`，auth.go:60）。worker_id 即一种 caller_id。

### 4.7 装配（assemble.go buildCore）

```go
// buildCore（assemble.go:46）改：
runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}

// 1) hub 单例先建一次（与 peer-http 每条配置一个不同！）
hub := wshub.New(/* authorize=lookupCaller 注入, bindings from cfg.Server.Workers */)

for name, rc := range cfg.Runners {
    switch rc.Type {
    case "peer-http":
        runners[name] = peerhttprunner.New(name, rc.BaseURL, tokenFromEnv(rc.TokenEnv))
    case "worker":
        runners[name] = workerrunner.New(name, rc.WorkerID, hub) // 引用同一 hub 单例
    }
}
// Core 加 Hub 字段
return &Core{..., Hub: hub}, nil
```

> 注意 buildCore 也被 `mcp` 命令复用（assemble.go 注释）。mcp 进程没有 HTTP 路由挂 hub，但 hub 单例存在无害（无 worker 连入即空闲）；worker runner 在 mcp 下 Dispatch 会因 worker 离线返回错误，符合预期。

### 4.8 路由挂载（httpapi/server.go）— Bearer 鉴权但绕 JSON 信封

```go
// server.go: Server 加 hub 字段；New 增参 hub（serve.go:100 传 core.Hub）。
// buildRouter（server.go:131 /v1 group 内或同级）加：
r.GET("/v1/workers/connect", func(c *rux.Context) {
    // 复用 Bearer 解析（auth.go:bearerToken）+ lookupCaller，但失败以 WS 握手拒绝形态返回 401，
    // 不走 writeError 的 {error,detail} JSON 信封、不触发 Web fallback（§7）。
    got, ok := bearerToken(c.Req.Header.Get("Authorization"))
    callerID, matched := "", false
    if ok { callerID, matched = s.lookupCaller(got) }
    if !matched {
        c.Resp.WriteHeader(http.StatusUnauthorized) // 裸 401，无 JSON body（WS 握手前拒绝）
        return
    }
    s.hub.Accept(c.Resp, c.Req, callerID) // 进入 WS 升级
})
```

> ⚠️ 该路由**不能**放在 `authMiddleware` 守护的 `/v1` group 内：authMiddleware 在升级前会 `c.Next()`/`writeError`。**单独注册**该路由（group 外或 group 内但不复用 group middleware），自行做 Bearer 校验。关于 rux 包装 ResponseWriter 与 `Accept` 的握手：P0 实证 rux 的 `c.Resp` 缓冲 `WriteHeader` → **必须经 `wsUpgradeWriter` 包装**（见 §4.2.2），不能裸传 `c.Resp`，否则 101 握手不下发、`Dial` 静默挂起。具体以 P0 spike 验通的写法为准。
> allow_empty_token 场景：若 server 无 token 配置且 allowEmptyToken，worker connect 允许 callerID=""，但此时 `bindings` 为空 → hub.Accept 会因绑定校验失败拒绝。**结论**：worker 接入**强制要求**配置 `server.workers`（评审 #1），即便 allow_empty_token 也不放行无绑定 worker。

---

## 5. 关键流程时序

### 5.1 register / token 绑定（评审 #1）

```txt
worker                                         hub(serve)
  │ WS GET /v1/workers/connect (Bearer <tok>) ─▶
  │                                  路由层 bearerToken+lookupCaller → callerID
  │                                  callerID 无匹配 → 裸 401（WS 握手拒绝），结束
  │                                  匹配 → websocket.Accept(InsecureSkipVerify/origin)
  │ register{worker_id:w1, labels, projects[], agents[], max_concurrent} ─▶
  │                                  绑定校验：bindings[w1]==callerID ?
  │                                    否 → registered{accepted:false, reason} → Close ; 结束
  │                                    是 → reg.Put(w1, conn)（旧 conn 优雅关闭，约束 #5）
  │ ◀─ registered{accepted:true, server_time}
  │ ⟂ 持久连接，进入收 dispatch / 单读循环
```

### 5.2 dispatch + 日志镜像 + result（核心，评审 #2 顺序）

```txt
用户 ─POST /v1/jobs {runner:"worker", worker_id:"w1", project_key, agent, prompt|cmd}─▶ serve
serve.job.Submit: validate（worker_id 必填+已知）→ remote 分支 → 建 job(queued)+job_id+result_dir
serve.execute: 打开 stdout/stderr LogWriter（store 文件）→ req.Stdout/Stderr 注入 → run.Run(ctx, req)
workerRunner.Run:
   (a) hub.RegisterSink(w1, job_id, sink{stdout,stderr,resultCh})   ← 先注册（评审 #2）
   (b) hub.Dispatch(w1, dispatch{job_id, project_key, agent, runner:local, ...})
worker: 收 dispatch → core.Jobs.Submit(local 再校验, 评审 #8) → 本地跑 codex/claude/exec
worker: streamLocalJob 增量读本地日志 → log{job_id, stream, seq, text} ──▶ hub
hub.readLoop: 单读 → demux by job_id → sink.WriteLog → io.WriteString(req.Stdout/Stderr)
            = <result_dir>/<job_id>/stdout.log  → serve 的 /logs + /stream(SSE) + Web + MCP 零改动
worker: 本地终态 → result{job_id, status, exit_code, error} ──▶ hub
hub.readLoop: demux → sink.Finish(result) → 唤醒 workerRunner.Run 的 select
workerRunner.Run: 返回 runner.Result{ExitCode, Err}（(e) defer DeregisterSink）
serve.execute: classify(ctx, res) → finish → jobstore.UpsertJob(终态, worker_id=w1 落库)
```

**有序不变量**（评审 #2）：同一 `job_id` 的 `log…log…result` 由 hub **单读循环**按到达顺序处理；sink 注册先于 dispatch，故 worker 即使立刻推首帧也不会丢。

### 5.3 worker 本地校验失败（评审 #8）

```txt
worker 收 dispatch → core.Jobs.Submit 校验失败（agent 未放行/exec 门/SafeJoin 越界）
worker → result{job_id, status:"failed", exit_code:-1, error:"..."}（无需新帧类型）
hub → sink.Finish → workerRunner.Run 返回 failed → serve finish 为 failed
```

---

## 6. 测试与验收

> 测试就近放各包 `_test.go`；e2e smoke 放 `internal/worker/e2e_test.go` 或 `internal/commands/`。`-race` 必跑（hub demux + sink 并发）。

### 6.1 单元测试（按包）

**`internal/wsproto`**：
- `TestEncodeDecodeRoundTrip`：每个 P1 帧（register/registered/dispatch/log/status/result）Encode→DecodeEnvelope→As 还原一致。
- `TestDecodeUnknownType`：未知 type 不 panic，返回原始 envelope（前向兼容）。
- `TestServerTimeMillis`：`Registered.ServerTime` 为毫秒（SR102）。

**`internal/wshub`**（`-race`）：
- `TestRegistryPutGetRemove`：并发 Put/Get/Remove 无 data race；同 id 重 Put 返回旧 conn。
- `TestRegisterTokenBindingMismatch`：register.worker_id ≠ callerID 绑定 → `registered{accepted:false}` 且连接关闭（评审 #1）。
- `TestRegisterAccepted`：绑定一致 → accepted:true，registry 含该 worker。
- `TestReadLoopOrdering`（评审 #2 核心）：构造 fake conn 注入交错 `log(seq1) log(seq2) result` → 断言 sink 收到顺序与注入一致、`Finish` 在所有 `WriteLog` 之后。
- `TestSinkRegisteredBeforeDispatch`：模拟「先注册 sink，再有 log 帧」不丢首帧；反向（未注册先来帧）丢弃但不 panic。
- `TestDispatchOfflineWorker`：Dispatch 到未注册 worker → `ErrWorkerOffline`。
- `TestBackPressureChattyJob`（评审 #3，`-race`）：一个 job 持续高频/大 log 帧，断言（1）同连接另一 job 的帧仍被及时投递（不饿死）；（2）单帧 text 被 cap 到 `maxWSFrameBytes`、截断标记存在；（3）内存有界（不缓存全部历史）。

**`internal/runner/worker`**：
- `TestRunNilForward`：Forward=nil → ExitCode -1。
- `TestRunSinkLifecycle`（用 fake hub）：断言调用顺序 RegisterSink→Dispatch→(等)→DeregisterSink（评审 #2）。
- `TestRunResultMapping`：result{done/failed/timeout} → runner.Result 正确（errFromResult）。
- `TestRunWorkerOffline`：RegisterSink 返回离线 → ExitCode -1, Err。
- `TestRunCtxCancel`：ctx 取消时 select 走 ctx.Done 分支返回（P1：不发 cancel 帧）。

**`internal/worker`**：
- `TestHandleDispatchSubmitsLocal`（用真 job.Service + local exec agent）：dispatch echo 命令 → 本地执行 → 推回 log+result。
- `TestHandleDispatchValidateFail`（评审 #8）：dispatch 一个 worker 未放行的 agent → 推 `result{failed}`，不 panic、不执行。
- `TestRegisterRejected`：registered{accepted:false} → Run 返回错误。

**`internal/job`**：
- `TestSubmitWorkerRequiresWorkerID`：runner=worker 且 worker_id 空 → ErrInvalidRequest。
- `TestSubmitUnknownWorkerID`：worker_id 不在 cfg.Server.Workers → ErrInvalidRequest。
- `TestWorkerIDRoundTrip`：Submit→toRecord→UpsertJob→fromRecord 后 WorkerID 保持（落库 + 读回）。
- `TestIsRemoteRunner`：worker/peer-http 均 true，local false。

**`internal/config`**：
- `TestDecodeServerWorkers` / `TestDecodeWorkerConfig`：yaml 解析 `workers`、`runners.*.worker_id`、worker.yaml 顶层结构无报错、字段就位。

**`internal/commands`**：
- `TestBuildCoreWorkerRunner`：含一个 `type: worker` runner 的 cfg → Core.Runners 含该 runner、Core.Hub 非 nil、hub 为**单例**（多个 worker runner 共享同一 hub 指针）。
- `TestServeMountsWorkerConnectRoute`：buildRouter 后 `/v1/workers/connect` 路由存在；无 Bearer → 裸 401（非 JSON 信封）。

### 6.2 e2e smoke（一条，文档化）

`internal/worker/e2e_test.go` 之 `TestE2ERemoteExecution`（`-race`）：
1. 起内嵌 serve（`httptest.Server` + 真 Core + hub）+ 配 `server.workers.w1{token}` + runner `remote-w1{type:worker, worker_id:w1}` + 一个 project（allowed_runners=[remote-w1]）。
2. 起 `worker.Client`（同进程 goroutine）dial→register w1（带正确 token）。
3. 经 HTTP `POST /v1/jobs {runner:remote-w1, worker_id:w1, agent:exec, cmd:["echo","hi"]}`。
4. 断言：
   - `GET /v1/jobs/{id}/logs/stdout` 含 `hi`（镜像生效，读路径零改动）。
   - `GET /v1/jobs/{id}/stream`（SSE）收到 `log`(hi) + 终态 `status` + `end`。
   - `GET /v1/jobs/{id}` 终态 `done`、`exit_code:0`、**`worker_id:"w1"`**。
   - jobstore 行 `worker_id="w1"` 已持久化。
5. 负路径子用例：错误 token dial → 401；正确 token 但 register.worker_id=w2（绑定不符）→ `registered{accepted:false}`。

### 6.3 WP1 验收门（gate，对应主 §3 + 任务验收）

- [x] 端到端：serve + worker，`runner=worker` 的 job 在 worker 执行，日志镜像到 hub（HTTP `/logs` **且** SSE `/stream` 双验），status/result 正确，`jobs.worker_id` 持久化。（`internal/worker/e2e_test.go::TestE2ERemoteExecution`）
- [x] per-worker token 绑定：错 token / worker_id 不符 → register 拒绝（评审 #1）。（`TestE2EWrongTokenRejected` / `TestE2EWorkerIDBindingMismatch` / `wshub.TestRegisterTokenBindingMismatch`）
- [x] sink-before-dispatch：首帧不丢（评审 #2）。（`runner/worker.TestRunSinkLifecycle` + `wshub.TestReadLoopOrdering` 先注册后投递）
- [x] 有序投递：交错 log/result 不乱序（评审 #2，`-race`）。（`wshub.TestReadLoopOrdering`，`-race` 通过）
- [x] 背压：话痨 job 不卡连接 / 不无界内存（评审 #3）。（`wshub.TestBackPressureChattyJob` + `runner/worker.TestBoundedSinkTruncates`）
- [x] worker 本地再校验（评审 #8）：未放行 agent → failed，不执行。（`worker.TestHandleDispatchValidateFail`）
- [x] `go build ./...` + `go test ./... -race` 全绿；`go vet ./...` 干净。
- [x] 子阶段绿灯即提交（SR1202）；完成回填主文档 §10 进度 + 提交哈希。

---

## 7. 实施顺序建议（子阶段，每段绿灯即提交 SR1202）

1. `go.mod` 加 coder/websocket + `wsproto` 全量帧 + 单测 → 提交。
2. `config/model.go` 字段 + `job/model.go` WorkerID + `service.go` 映射/校验/分支 + jobstore 回归（无迁移）→ 提交。
3. `wshub`（registry + Accept + 绑定 + 单读循环 + dispatch + sink）+ `-race` 单测 → 提交。
4. `runner/worker`（workerRunner 生命周期 + 背压 sink）+ 单测 → 提交。
5. `internal/worker` 客户端 + `commands/worker.go` + app 注册 + 配线（buildCore hub 单例 / server.go 路由 / serve.go 传 hub）→ 提交。
6. e2e smoke + 验收门全跑 + 主文档进度回填 → 提交。

---

## 8. 与主文档/设计的一致性备注（仅 flag，未改主文档）

实施时需留意以下两处需对齐（非本子文档可单方改）：
- **主 §5 dispatch 帧字段含 `runner(=local)`**：worker 侧恒用 `local`，本计划已照此（§4.1/§4.4）。一致。
- **runner 名↔worker_id 绑定粒度**：主 §6 配置示例是「一个 worker runner 配一个 `worker_id`」（静态）。本计划据此让 workerRunner 持有配置 `worker_id`，`req.WorkerID` 仅用于 validate/落库（§4.5.2 约定）。设计 §8/§9 的「提交时指定 worker_id」语义在此模型下＝「选哪个 worker-runner」，二者等价无冲突，但实施者勿误以为「一个 runner 可动态路由任意 worker_id」（那是 WP4）。
