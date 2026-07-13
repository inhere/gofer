# worker 配置远程化 实施计划（总纲 + P1）

> 设计：[`docs/design/2026-07-13-worker-config-federation-design.md`](../design/2026-07-13-worker-config-federation-design.md)（v0.4）
> bd epic：`tools-5pq`

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-07-13 | Claude | 初版：总纲 + P1（worker 热重载 + 远程 reload）详细计划。待审 |

## 阶段总纲

| 阶段 | 内容 | 状态 |
|---|---|---|
| **P1** | worker 热重载 + 远程 reload（SIGHUP / hub 指令 / HTTP / CLI），不中断在跑 job | 📋 本文详列 |
| P2 | 内置 agent 模板注册表 + detect 上报；worker.yaml 的 `agents` 降为逃生舱 | 待规划 |
| P3 | Policy 下发（proto v3 语义）：server 按 runner 可达性推送、worker 按 roots 自筛 | 待规划 |
| P4 | 管理面（Cluster 页 accepted/rejected/degraded/detected）+ CLI | 待规划 |
| D5 | web 表单按 project 收窄 runner（独立小项，不依赖 P1-P4） | ✅ 已完成（`tools-5pq.1`） |

---

# P1：worker 热重载 + 远程 reload

## 目标与验收（先写死，避免实施跑偏）

**目标**：改完 worker 配置后，**不重启 worker 进程**就能让新配置生效并让 server 看到新能力；且能**从 server 侧远程触发**，不必登录 worker 机器执行命令。

**验收（必须逐条实测，不接受"应该可以"）**：

1. **复现今天的痛点并消除**：给容器 worker 的 `worker.yaml` 加一个 agent（如 `tty-claude`）→ 执行 `gofer worker reload w-container-example`（在 **server 侧**执行，不登录 worker）→ `GET /v1/meta` 中该 worker 的 `agent_caps` **立即出现 tty-claude**，全程 worker 进程 PID 不变。
2. **不中断在跑 job**：worker 上跑一个 ≥20s 的 exec job，期间触发 reload → 该 job 仍正常跑完（`status=done`, `exit_code=0`），日志不断流。
3. **本地 SIGHUP 等价**：`kill -HUP <worker pid>` 与远程 reload 效果一致。
4. **旧 worker 明确报错**：对 proto < 3 的 worker 发 reload → HTTP **409** + 明确文案（"worker 版本过旧，请重启"），**不能假装成功**。
5. **离线 worker**：对未连接的 worker 发 reload → HTTP **409**（不是 500，不是静默 200）。
6. **配置坏了不炸**：reload 时 worker.yaml 语法错 / 文件不存在 → **保留旧配置继续服务**，回报错误（与 server 的 `core.Reload` fail-safe 一致），worker 不退出。
7. 全量 `go test ./... -p1 -count=1` 绿；`go vet` 绿。

## 现状事实（实施前必须知道的）

- worker 的 Core 是从 `workerConfigToConfig(wc)` 转出来的普通 `config.Config`（`internal/commands/worker.go:193`）→ **可以复用 `core` 的 reload 机制**，但 `core.Reload(path)` 走的是 `config.Load(path)`（server 配置格式），**不能直接用**在 worker.yaml 上。
- worker 的能力（Labels/Projects/Agents/AgentCaps/MaxConc）在 `worker.New(worker.Config{...})` 构造时**一次性快照**（`worker.go:203-217`），Register 帧从它取值 → reload 后必须能**更新这份快照**。
- 帧分发的 `switch` 无 `default` 分支 → **未知帧类型天然被忽略**（`internal/worker/client.go:485`，envelope.go 注释也确认）。故新增帧不会打挂旧端；但 server 必须知道对端**认不认**新帧，否则 reload 会"发出去没人理"却报成功 → 用 `ProtocolVersion` 判定。
- `Register` 帧已携带 `ProtocolVersion`，且整份 Register 存进 `workerConn.meta`（`internal/wshub/registry.go`）→ server **已经知道**每个 worker 的协议版本，直接用。

## 任务分解

### T1 `core`：抽出 `ReloadWith(cfg)`

`internal/core/core.go`：把 `Reload(path)` 的后半段抽成可复用的入口，让 worker 能用**自己的**配置对象走同一套热替换。

```go
// Reload 保持不变（server 用：按路径加载 + overlay + ReloadWith）
func (c *Core) Reload(path string) error {
    ...                                  // 现有的 stat / config.Load / ApplyProjectOverlays
    return c.ReloadWith(newCfg)
}

// ReloadWith 热替换运行时配置快照。worker 用它——worker.yaml 是另一套 schema，
// 由调用方转成 *config.Config 后传入（workerConfigToConfig）。
func (c *Core) ReloadWith(newCfg *config.Config) error {
    c.Cfg = newCfg
    c.Projects.Reload(newCfg)
    c.Agents.Reload(newCfg)
    c.Jobs.Reload(newCfg)
    return nil
}
```

**验收**：`go test ./internal/core/` 绿；`Reload` 行为逐字不变（G023）。

### T2 `wsproto`：proto v3 + 两个新帧

`internal/wsproto/envelope.go` / `frames.go`：

```go
const ProtocolVersion = 3  // v2 → v3：新增 reload 指令 + 运行期能力更新

const (
    TypeReload FrameType = "reload" // s→w：重读本地配置、重报能力
    TypeCaps   FrameType = "caps"   // w→s：运行期能力更新（reload 后，不断连）
)

// Reload：server → worker。
type Reload struct {
    Reason string `json:"reason,omitempty"` // 谁/为什么触发，进 worker 日志便于追溯
}

// Caps：worker → server。Register 的能力子集，在**已建立的连接上**更新，
// 避免为了换个 agent 就断连重注册（断连会让 server 看到 worker 掉线、并可能丢派发）。
type Caps struct {
    Labels    []string     `json:"labels,omitempty"`
    Projects  []string     `json:"projects,omitempty"`
    Agents    []string     `json:"agents,omitempty"`
    AgentCaps []AgentBrief `json:"agent_caps,omitempty"`
    MaxConc   int          `json:"max_concurrent,omitempty"`
    Err       string       `json:"err,omitempty"` // reload 失败：回报原因，能力保持不变
}
```

**验收**：`internal/wsproto` 单测覆盖 encode/decode 两个新帧；旧端收到新帧被忽略（已有行为，补一条断言）。

### T3 `worker`：可更新的能力快照 + reload 执行

`internal/worker/client.go`：

```go
// Client 的能力字段改为受锁保护的快照（当前是构造时定死）
type Client struct {
    ...
    capsMu sync.RWMutex
    caps   wsproto.Caps  // Register 与 TypeCaps 都从这里取
    // reload 由 commands 层注入：worker.yaml 的解析/转换是 commands 的职责（G021）
    reloadFn func() (wsproto.Caps, error)
}

// UpdateCaps 替换能力快照，并在当前连接上广播一帧 caps（无连接时静默——
// 下次 Register 会带上最新值，不需要额外补偿）。
func (cl *Client) UpdateCaps(c wsproto.Caps) {
    cl.capsMu.Lock()
    cl.caps = c
    cl.capsMu.Unlock()
    _ = cl.writeFrame(wsproto.TypeCaps, "", c) // 失败不致命：重连时 Register 携带最新快照
}

// 读循环新增：
case wsproto.TypeReload:
    go cl.handleReload(env)   // 独立 goroutine：reload 可能读盘/跑 detect，不能阻塞读循环
```

```go
func (cl *Client) handleReload(env wsproto.Envelope) {
    var rl wsproto.Reload
    _ = json.Unmarshal(env.Data, &rl)
    slog.Info("worker reload requested", "reason", rl.Reason)
    caps, err := cl.reloadFn()
    if err != nil {
        // fail-safe：保留旧配置继续服务，把失败回报给 server（验收 6）
        slog.Error("worker reload failed, keeping old config", "err", err)
        _ = cl.writeFrame(wsproto.TypeCaps, "", wsproto.Caps{Err: err.Error()})
        return
    }
    cl.UpdateCaps(caps)
}
```

`internal/commands/worker.go`：注入 reload 闭包——**它是唯一知道 worker.yaml 怎么读、怎么转 Config 的地方**：

```go
reloadFn := func() (wsproto.Caps, error) {
    nwc, err := loadWorkerConfig(workerOpts.config)  // 现有加载器（commands/worker.go:241）
    if err != nil {
        return wsproto.Caps{}, err                    // 旧配置原样保留（Core 未动）
    }
    ncfg := workerConfigToConfig(nwc)
    if err := cr.ReloadWith(ncfg); err != nil {           // T1
        return wsproto.Caps{}, err
    }
    return wsproto.Caps{
        Labels:    nwc.Labels,
        Projects:  mapKeys(nwc.Projects),
        Agents:    agentKeys(ncfg),      // 与 Register 同源：所报即所受（现有 resolvedAgentKeys）
        AgentCaps: agentBriefs(ncfg),
        MaxConc:   nwc.MaxConcurrent,
    }, nil
}
```

**关键不变量**：能力快照与 Core 的配置**同源同刻**——先 `ReloadWith` 再从**同一份** `ncfg` 算 caps，绝不能一个用新的一个用旧的（否则 server 以为 worker 能跑某 agent、worker 的第二道 validate 却拒绝——就是今天 `tty-claude` 那个错误的翻版）。

`internal/worker/serve.go`：SIGHUP → 同一个 `reloadFn`（与远程 reload **走同一条路径**，不允许两套实现）。

```go
signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
signal.Notify(hup, syscall.SIGHUP)   // 新增；Windows 上 SIGHUP 不可用 → 构建标签隔离
```

**验收**：单测——`UpdateCaps` 后 Register 携带新值；`reloadFn` 失败时 Core 配置不变且发出带 `Err` 的 caps 帧。

### T4 `wshub`：接收 caps + 下发 reload

`internal/wshub/hub.go` 读循环加分支；`registry.go` 加原地更新：

```go
// registry.go
func (r *WorkerRegistry) UpdateCaps(workerID string, c wsproto.Caps) bool {
    r.mu.Lock(); defer r.mu.Unlock()
    wc, ok := r.byID[workerID]
    if !ok { return false }
    if c.Err != "" {                       // reload 失败：不动能力，只记一条最近错误供 UI 展示
        wc.lastCapsErr = c.Err
        return true
    }
    wc.meta.Labels, wc.meta.Projects = c.Labels, c.Projects
    wc.meta.Agents, wc.meta.AgentCaps = c.Agents, c.AgentCaps
    if c.MaxConc > 0 { wc.meta.MaxConcurrent = c.MaxConc }
    wc.lastCapsErr = ""
    return true
}

// hub.go：下发 reload（proto 闸——旧 worker 不认这帧，必须显式拒绝而不是假装成功）
func (h *Hub) SendReload(workerID, reason string) error {
    wc, ok := h.reg.conn(workerID)
    if !ok { return ErrWorkerOffline }
    if wc.meta.ProtocolVersion < 3 { return ErrWorkerTooOld }
    return wc.write(wsproto.TypeReload, "", wsproto.Reload{Reason: reason})
}
```

**验收**：单测——caps 帧更新 registry 后 `WorkerSnapshot` 立刻反映新 agents；对 proto=2 的 conn 调 `SendReload` 返回 `ErrWorkerTooOld`。

### T5 `httpapi` + CLI：远程触发入口

`httpapi` 不能 import `wshub`（G022）→ 扩现有 `workerRegistry` 适配接口加一个方法：

```go
type workerRegistry interface {
    ...
    ReloadWorker(workerID, reason string) error   // serve/probe.go 里桥到 hub.SendReload
}

// POST /v1/workers/{id}/reload
func (s *Server) handleWorkerReload(c *rux.Context) {
    id := c.Param("id")
    if _, ok := s.workerConfigs()[id]; !ok { c.JSON(404, ...); return }   // 未登记
    switch err := s.workers.ReloadWorker(id, callerOf(c)); {
    case errors.Is(err, ErrWorkerOffline): c.JSON(409, "worker 未连接")
    case errors.Is(err, ErrWorkerTooOld):  c.JSON(409, "worker 协议过旧(<v3)，请重启该 worker")
    case err != nil:                        c.JSON(500, err)
    default:                                c.JSON(202, {"status":"reload requested"})
    }
}
```

CLI：`gofer worker reload <worker-id>` → 打上面这个端点（**在 server 侧执行**，这正是"不登录 worker 机器"的意义）。

**验收**：httpapi 单测覆盖 404 / 409(离线) / 409(过旧) / 202；CLI 手测。

### T6 e2e 真机冒烟（隔离栈，不碰 live）

```txt
1. 起隔离 serve(:18799) + worker(w-smoke)，worker.yaml 只有 agents: [claude]
2. 提交一个 exec sleep 25 的 job 到 w-smoke（验收 2 的对照）
3. job 跑到一半：编辑 worker.yaml 加 tty-demo → `gofer worker reload w-smoke`
4. 断言：
   a. /v1/meta 的 w-smoke.agent_caps 立刻含 tty-demo（进程 PID 不变 → `pgrep` 对比）
   b. 步骤 2 的 job 仍 done / exit_code=0（reload 没打断它）
   c. 立刻提交一个 tty-demo 的交互 job → 被受理（不再需要重启 worker）
5. 反例：把 worker.yaml 改成语法错的 → reload → 409/200+Err，worker 仍在跑、旧能力不变
```

## 风险与对策

| 风险 | 对策 |
|---|---|
| 在跑的 job 持有旧配置引用，reload 后行为诡异 | job 在**受理时**就已解析出 argv/cwd；reload 只换 registry 快照。**验收 2 强制实测**，不靠推理 |
| reload 后能力变小，而 server 刚按旧能力派了 job | worker 第二道 validate 会拒 → job 明确 failed（不是静默跑错）。可接受，且这正是第二道闸存在的意义 |
| 能力快照与 Core 配置不同源（先算 caps 后 reload） | T3 的「同源同刻」不变量 + 单测钉死 |
| Windows 无 SIGHUP | 构建标签隔离；Windows 上只走远程 reload（本来也是主路径） |
| 新帧打挂旧端 | 帧分发本就忽略未知类型（已验证）；server 侧再加 proto 闸显式拒绝 |

## 提交节奏（SR1202）

T1 → T2 → T3 → T4 → T5 各自单独 commit（每步 `go test ./...` 绿），T6 冒烟通过后收尾 commit + 更新本文进度。

## 进度跟进

- [ ] T1 core.ReloadWith
- [ ] T2 wsproto proto v3 + Reload/Caps 帧
- [ ] T3 worker：能力快照可更新 + reloadFn（SIGHUP 与远程同路径）
- [ ] T4 wshub：接收 caps / 下发 reload（proto 闸）
- [ ] T5 httpapi + CLI 入口
- [ ] T6 e2e 冒烟（含"不中断在跑 job"与"坏配置不炸"两条反例）
