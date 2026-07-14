# worker 配置远程化 实施计划（总纲 + P1）

> 设计：[`docs/design/2026-07-13-worker-config-federation-design.md`](../design/2026-07-13-worker-config-federation-design.md)（v0.5）
> bd epic：`tools-5pq`

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-07-13 | Claude | 初版：总纲 + P1 详细计划。待审 |
| v0.2 | 2026-07-14 | Claude | **对抗式审查（host codex）后重写 P1**。原方案有 5 处会翻车：版本闸直接提到 3 会踢掉所有 v2 worker；reload 无 request id 导致 HTTP 202 表达不了失败（验收 6 不可实现）；`go handleReload` 并发无序会让旧配置覆盖新配置；`Core.ReloadWith` 不是跨组件原子切换（并发 Submit 可用旧配置过校验、用新 agent 命令执行）；hub 按 workerID 原地改 `meta` 有 data race、旧连接可污染新连接、且 `meta.MaxConcurrent` 改了不影响真正做准入的 `maxConcurrent`。伪代码里的 `writeFrame` 签名、`ReloadWith` 返回 error 的语义也与真实代码不符，一并更正。 |

## 阶段总纲

| 阶段 | 内容 | 状态 |
|---|---|---|
| **P1** | worker 热重载 + 远程 reload（SIGHUP / hub 指令 / HTTP / CLI），不中断在跑 job | 📋 本文详列 |
| P2 | 内置 agent 模板注册表 + detect 上报；worker.yaml 的 `agents` 降为逃生舱 | 待规划 |
| P3 | Policy 下发（proto v3 语义）：server 按 runner 可达性推送、worker 按 roots 自筛 + D6 投影 | 待规划 |
| P4 | 管理面（Cluster 页 accepted/rejected/degraded/detected）+ CLI | 待规划 |
| — | pin=硬授权（D4′ 的前提） | ✅ 已完成（`1e69ff5`，bd `tools-blt`） |
| — | web 表单按 project 收窄 runner | ✅ 已完成（`c2c7bd4`，bd `tools-5pq.1`） |

---

# P1：worker 热重载 + 远程 reload

## 目标

改完 worker 配置后，**不重启 worker 进程**就能让新配置生效并让 server 看到新能力；且能**从 server 侧远程触发**，不必登录 worker 机器。

## 验收（先写死；每条都要能指出"怎么证明它真的成立"）

1. **消除今天的痛点**：给容器 worker 的 `worker.yaml` 加一个 agent → 在 **server 侧**执行 `gofer worker reload w-container-example` → `/v1/meta` 中该 worker 的 `agent_caps` 出现新 agent，且 **worker 进程 PID 不变**（用 `pgrep` 前后对比证明，不看日志自述）。
2. **不中断在跑 job**：worker 上跑一个 ≥20s 的 exec job，中途 reload → 该 job 仍 `done` / `exit_code=0`，日志不断流。
3. **本地 SIGHUP 等价**：`kill -HUP <worker pid>` 与远程 reload 走**同一条执行路径**（代码上是同一个 executor，不是两份实现），效果一致。
4. **坏配置不炸且能同步报错**：worker.yaml 语法错 → 保留旧配置继续服务、worker 不退出，且 **HTTP 请求本身返回失败**（不是 202 之后石沉大海）。→ 这条要求 reload 有 request id 与回执（见 T2/T5）。
5. **旧 worker 明确报错**：proto < 3 的 worker 收到 reload 请求 → HTTP **409** + "worker 版本过旧，请重启"；且该 worker **仍能正常连接与工作**（不是被版本闸踢掉）。
6. **离线 worker**：未连接 → HTTP **409**（不是 500、不是静默 200）。
7. **并发 reload 定序**：连续触发两次 reload（A 慢 B 快），最终生效的必须是 **B**（后一次），不能被 A 覆盖回去。用一个人为拖慢的 reload 钩子在单测里构造。
8. **配置切换原子性**（`-race`）：并发循环 `Submit` + `reload`，断言每个 Submit **要么完全看到旧配置、要么完全看到新配置**，不能出现"用旧策略过校验、用新 agent 命令执行"的混合态。
9. **hub 侧无竞态**（`go test -race ./internal/wshub/...`）：能力更新与 `WorkerSnapshot` 并发无 race；旧连接迟到的 caps **不得**覆盖新连接；`max_concurrent` 更新后**真正的准入上限**跟着变（不是只改了展示字段）。
10. 全量 `go test ./... -p1 -count=1` 绿；`go vet` 绿。

## 现状事实（实施前必须知道的；已逐条对代码核实）

- worker 的 Core 是从 `workerConfigToConfig(wc)` 转出来的普通 `config.Config`（`internal/commands/worker.go:193`）→ 可复用 core 的热替换，但 `core.Reload(path)` 走 `config.Load(path)`（server 配置格式），**不能直接用**在 worker.yaml 上。
- worker 的能力在 `worker.New(worker.Config{...})` 构造时**一次性快照**（`commands/worker.go:203-217`），Register 帧从它取值 → reload 后必须能更新这份快照。
- **`Register` 只能是连接后的第一帧**（`wshub/hub.go:167-183`）；运行期再发 Register **没有处理分支**。故运行期更新能力必须用**新帧**，术语上叫"重新上报能力"，**不是** re-register。
- **版本闸是单一整数**：`hub.go:204-218` 直接 `reg.ProtocolVersion < wsproto.ProtocolVersion` 就拒绝连接。**把常量提到 3 = 所有 v2 worker 下次重连即被拒**（滚动升级期间一次抖动就回不来）。必须先拆 Min/Current。
- **`writeFrame` 带 ctx**：`func (cl *Client) writeFrame(ctx context.Context, t FrameType, jobID string, payload any) error`（`worker/client.go:567`）。异步 reload 用哪个 ctx / 写超时是必须设计的，不是省个参数的事。
- **`Reload` 三兄弟无返回值**：`project.Registry.Reload` / `agent.Registry.Reload` / `job.Service.Reload` 都是 `atomic.Store`，**没有 error**。所以 `ReloadWith` 返回 error 只可能来自**前置校验**——"应用阶段失败保留旧配置"这句话必须靠"先构造好新 cfg、构造失败就不进入应用阶段"来兑现，而不是靠应用阶段回滚。
- **Core 的四次 Store 不是原子快照**：`core.go:256-259` 依次 Store `Cfg / Projects / Agents / Jobs`；而 `job.Service.Submit` 取了 `cfg := s.config()` 后，**仍会去读 agent registry 自己的指针**（`submit.go:166-202` 的 `s.agents.BuildWithOptions` / `s.agents.Get`）。→ reload 窗口内一个 Submit 可能用旧 cfg 过校验、用新 agent 定义构建执行。**这是既有问题**（server 的 SIGHUP reload 同样中招），P1 必须一并解决，否则 worker 端 reload 会把它放大。
- **已在跑的 job 是安全的**：`runReq` 在起 goroutine 之前就算好了（`submit.go:136-181`）。真正的危险窗口是"正在受理的 Submit 与 reload 并发"。
- **hub registry 的 `maxConcurrent` 是独立字段**：`wc.meta`（展示）与 `wc.maxConcurrent`（`tryReserve` 真正用来准入，`registry.go:105-135`）**是两份**。只改 meta = 界面骗人。
- **`WorkerSnapshot` 在释放 registry 锁之后才读 `wc.meta`**（`registry.go:277-302`）→ 若 caps 更新在 registry 锁下原地改 `wc.meta`，就是 data race。
- **同一 worker 重连后 registry 会把 ID 指向新连接**，旧连接的 read loop 可能仍在收尾 → 按 workerID 更新能力会让**旧进程的迟到 caps 覆盖新连接**。
- ❌ **~~`httpapi` 没有 import `wshub`~~ —— 这条"核实"是错的（v0.2 写错，T5 实施时推翻）**。`httpapi/server.go` 自 `0c93951` 起就有 `hub *wshub.Hub` 字段，G022 边界**本来就是破的**；codex 当初的判断是对的，是复核只搜了函数调用、漏看结构体字段。T5（`d936c70`）已把它收窄成 `workerHub` 窄接口（只要 `Accept`+`LiveInstance`），`errors.Is/As` 收敛到 `serve` 侧适配器一处，现在 `go list -deps ./internal/httpapi | grep wshub` 为空。**教训："已核实"三个字必须附上核实命令，否则下一个人会继续信它。**

## 任务分解

### T0 版本闸拆分（**第一个做**，否则后面每一步都在制造不兼容）

`internal/wsproto/frames.go`：

```go
const (
    MinProtocolVersion     = 2 // 允许注册的最低版本（兼容下限）
    CurrentProtocolVersion = 3 // 本端实现的版本（功能协商）
)
```

`internal/wshub/hub.go`：注册闸改为 `reg.ProtocolVersion < wsproto.MinProtocolVersion` 才拒；能力（reload/caps）**按对端上报版本**分别闸控。

**滚动升级矩阵**（必须写进单测/文档，不能只在脑子里）：

| 组合 | 期望 |
|---|---|
| v3 server + v2 worker | 可连接、旧语义工作；reload 请求 → 409「版本过旧」 |
| v3 server + v3 worker | 全功能 |
| v2 server + v3 worker | 可连接（v3 worker 上报 3，v2 server 的闸是 `< 2` 不成立…**须实测确认 v2 server 不会因未知帧崩**）|
| server 先升 / worker 先升 | 均不掉线 |

**验收**：单测覆盖矩阵前两行 + 「proto=2 的 conn 调 SendReload 返回 ErrWorkerTooOld」。

### T1 配置快照原子化（修既有竞态，`-race` 兜底）

问题：`Submit` 取了 cfg 快照后又去读 agent registry 的独立指针。

修法（择一，实施时以能通过 T1 验收为准）：

- **首选**：让 `Submit` 全程只用它一开始取到的 `cfg` —— 把 `s.agents.Get(k)` / `BuildWithOptions(...)` 换成基于该 cfg 的解析（`agent.ResolveAgent(cfg, key)` 已存在，Build 侧需补一个接受 cfg 的入口）。
- 备选：Core 持有一个**不可变快照对象** `{cfg, projects, agents}`，Reload 时整体换指针，Submit 取一次引用。

`core.ReloadWith(cfg)`：抽出供 worker 复用（server 的 `Reload(path)` = 加载 + overlay + `ReloadWith`）。**注意**：它的 error 只可能来自前置校验；文档/注释不得暗示"应用阶段可回滚"。

**验收（T1）**：新增 `-race` 测试——N 个 goroutine 循环 Submit，1 个 goroutine 循环 ReloadWith（两份差异明显的 cfg），断言每次 Submit 的 (校验用的策略, 构建用的 agent 定义) 同属一版。

### T2 wsproto：reload 是**有回执的 RPC**，不是"发出去就算"

```go
// s→w：请求重载。RequestID 是回执关联的唯一凭据。
type Reload struct {
    RequestID string `json:"request_id"`
    Reason    string `json:"reason,omitempty"`
}

// w→s：**回执**（RPC response，一一对应某次 Reload）
type ReloadResult struct {
    RequestID string `json:"request_id"`
    OK        bool   `json:"ok"`
    Err       string `json:"err,omitempty"` // 失败原因（坏配置等），旧配置保持不变
    Caps      *Caps  `json:"caps,omitempty"` // 成功时携带新能力
}

// w→s：**主动广播**能力（SIGHUP 触发的 reload 没有对应的 HTTP 请求）
type Caps struct {
    Labels    []string
    Projects  []string
    Agents    []string
    AgentCaps []AgentBrief
    MaxConc   int
}
```

**关键**：广播（Caps）与回执（ReloadResult）**分开**——不能让一个无关联的 Caps 同时充当状态广播和 RPC 响应，否则 SIGHUP、心跳重连、并发 reload 的 Caps 会互相冒充回执。

### T3 worker：**串行** reload executor（SIGHUP 与远程同路）

```go
// 单 goroutine 消费，天然定序：后到的 reload 一定后应用（验收 7）。
type reloadReq struct{ requestID, reason string } // requestID=="" → 本地 SIGHUP，无回执

func (cl *Client) reloadLoop(ctx context.Context) {
    for req := range cl.reloadCh {          // 串行；不再 `go handleReload(...)`
        caps, err := cl.reloadFn()          // 读盘 + 转 config + Core.ReloadWith + 重算能力
        switch {
        case req.requestID != "":           // 远程：回执
            _ = cl.writeFrame(ctx, wsproto.TypeReloadResult, "", wsproto.ReloadResult{
                RequestID: req.requestID, OK: err == nil, Err: errStr(err), Caps: capsOrNil(caps, err),
            })
        case err == nil:                    // 本地 SIGHUP：广播新能力
            cl.updateCaps(caps)             // 存快照 + 发 TypeCaps
        default:
            slog.Error("worker reload failed, keeping old config", "err", err)
        }
    }
}
```

- 读循环收到 `TypeReload` **只做入队**（不阻塞读循环、也不起新 goroutine）。
- 队列满时（极端）丢弃并回执 busy，不允许无界堆积。
- `reloadFn` 由 `commands` 注入（只有它知道怎么读 worker.yaml、怎么转 `config.Config`）：**先构造好新 cfg，构造失败就直接返回 err，绝不进入应用阶段**（这才是"坏配置保留旧配置"的真实兑现方式）。
- **不变量**：`ReloadWith(ncfg)` 与"从 ncfg 算能力"必须用**同一份** ncfg（所报即所受）。

### T4 wshub：按**连接**更新能力，且改到真正生效的字段

```go
// 传入具体的 *workerConn（或连接 generation），不是只按 workerID 查当前连接 ——
// 否则旧连接迟到的 caps 会覆盖新连接（验收 9）。
func (r *WorkerRegistry) UpdateCaps(wc *workerConn, c wsproto.Caps) {
    wc.mu.Lock()                 // 与 WorkerSnapshot 读 meta 用同一把锁（消除 race）
    defer wc.mu.Unlock()
    if r.current(wc.workerID) != wc { return } // 已被新连接取代 → 丢弃
    wc.meta.Labels, wc.meta.Projects = c.Labels, c.Projects
    wc.meta.Agents, wc.meta.AgentCaps = c.Agents, c.AgentCaps
    if c.MaxConc > 0 {
        wc.meta.MaxConcurrent = c.MaxConc
        wc.maxConcurrent = c.MaxConc          // ★ 真正做准入的字段（registry.go:105-135）
    }
}
```

同时把 `WorkerSnapshot` 读 `wc.meta` 的部分纳入 `wc.mu`（或改成读不可变 atomic 快照）。

`SendReload(workerID, requestID, reason)`：离线 → `ErrWorkerOffline`；`meta.ProtocolVersion < 3` → `ErrWorkerTooOld`。

### T5 httpapi + CLI：同步语义（限时等回执）

```txt
POST /v1/workers/{id}/reload
  → 生成 request_id，经 hub 下发 Reload
  → 等待该 request_id 的 ReloadResult，最长 N 秒（默认 10s，可配）
  → OK        → 200 {applied:true, caps:{...}}
  → Err       → 409 {applied:false, error:"<worker 的失败原因>"}   ← 验收 4 靠这条
  → 超时      → 504 {applied:false, error:"worker 未在 Ns 内回执"}
  → 离线/过旧 → 409（各自文案）；未登记 → 404
```

CLI `gofer worker reload <id>`：打这个端点，把 worker 的失败原因**原样打给用户**（别吞）。

httpapi 不 import wshub（已核实边界成立）→ 经 `serve` 侧适配器接口暴露 `ReloadWorker(id, reason) (Caps, error)`。

### T6 e2e 真机冒烟（隔离栈，不碰 live）

```txt
1. 起隔离 serve(:18799) + worker，worker.yaml 只有 agents:[claude]
2. 提交 exec sleep 25 的 job（验收 2 的对照）
3. 中途改 worker.yaml 加 tty-demo → gofer worker reload
4. 断言：
   a. /v1/meta 的 agent_caps 立刻含 tty-demo；worker PID 不变（pgrep 前后对比）
   b. 步骤 2 的 job 仍 done / exit_code=0
   c. 立刻提交 tty-demo 交互 job → 被受理（不必重启 worker）
5. 反例 A（坏配置）：worker.yaml 改成语法错 → reload → **HTTP 409 + 具体原因**，worker 仍在跑、能力不变
6. 反例 B（旧 worker）：用 proto=2 的旧 worker 二进制连上 → 能正常干活；对它 reload → 409「版本过旧」
```

## 风险与对策

| 风险 | 对策 |
|---|---|
| 版本闸提版本踢掉存量 worker | T0 先做 Min/Current 拆分；矩阵进单测 |
| 并发/乱序 reload | T3 串行 executor（SIGHUP 与远程同队列） |
| reload 与 Submit 并发看到混合配置 | T1 快照原子化 + `-race` 测试（验收 8） |
| 旧连接迟到 caps 污染新连接 | T4 按 `*workerConn` 更新 + current 校验 |
| `max_concurrent` 假更新 | T4 同步写 `wc.maxConcurrent`（准入真字段） |
| 坏配置导致 worker 半死 | 先构造后应用；构造失败不进应用阶段 |
| 在跑 job 受影响 | 已在跑的 job 用的是提交时算好的 `runReq`；验收 2 实测兜底 |
| Windows 无 SIGHUP | 构建标签隔离；Windows 只走远程 reload（本就是主路径） |

## 提交节奏（SR1202）

T0 → T1 → T2 → T3 → T4 → T5 每步单独 commit（每步 `go test ./... -p1` 绿），T6 冒烟通过后收尾。

## 进度跟进

- [x] T0 版本闸拆分（Min/Current）+ 滚动升级矩阵单测 — `faee920`
- [x] T1 配置快照原子化（含 `-race` 并发 Submit×Reload 测试）+ `core.ReloadWith` — `11eca78`
- [x] T2 wsproto：`Reload{RequestID}` / `ReloadResult` / `Caps`（广播与回执分离）— `df58c2c`
- [x] T3 worker：串行 reload executor（SIGHUP 与远程同路径）+ 先构造后应用 — `5ee375b`
- [x] T4 wshub：按连接更新能力（消 race、防旧连接污染、同步 `maxConcurrent`）— `58ecc7e`
- [x] T5 httpapi + CLI：同步回执语义（200/409/504）— `d936c70`
- [ ] T6 e2e 冒烟（含坏配置 409、旧 worker 409、在跑 job 不中断三条反例）
