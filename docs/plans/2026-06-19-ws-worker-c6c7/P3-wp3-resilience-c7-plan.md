# P3 — WP3 弹性（心跳/重连/worker-lost/多 worker）+ C7（worker 多地址 + 退避重连）实施计划

> 主文档（契约真源）：[`./2026-06-19-ws-worker-c6c7-plan.md`](./2026-06-19-ws-worker-c6c7-plan.md)
> 主设计：[`../../design/2026-06-17-ws-remote-worker-design.md`](../../design/2026-06-17-ws-remote-worker-design.md)（§8.1 注册/心跳、§8.5 断线/worker-lost MVP、§13 内置重连、§15 待确认、§17 评审 #7 半开读超时）。
> 本文按 SR1105 给可执行细节（file→改动 / 默认参数 / 关键流程 / 验收）；契约（包/帧/配置/鉴权/数据模型/跨阶段约束）以主文档 §4–§9 为准，不重复。

**依赖**：P1（WP1 核心：`wsproto`/`wshub`/`internal/worker`/`workerRunner` 已落地，主文档 §4）。P2（交互/取消）可并行——P3 不依赖 P2，但 cancel 帧已在 P2 落地，P3 复用其发送通道做优雅关闭。

**构建环境**：`export PATH=/path/to/ws-root/linux-env/sdk/gosdk/go1.25.10/bin:$PATH; cd tools/gofer`

---

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-19 | Claude | 初版：WP3 弹性 + C7。定心跳/半开检测（评审 #7，含数值默认）、worker-lost MVP（在飞 job 置 failed，fail-fast 而非挂起窗口）、worker 端退避重连 + 多地址轮换（C7）、多 worker 路由与 at-capacity 拒绝、goroutine 生命周期。显式排除：多 hub 共享注册表 / 跨 hub 接管 / 选主 / seq-offset 续传重放（主文档 §3 OUT、设计 §8.5 推迟）。 |

---

## 2. 范围

### 2.1 本阶段做（WP3 + C7）

1. **心跳 + 半开检测（hub & worker，评审 #7）**：周期 `ping`/`pong`（`ts`），hub 侧对每条 worker 连接设**显式 WS 读截止（read deadline）**，静默死连（half-open TCP，无 FIN）在读截止窗口内被检出 → 标记离线。hub 每帧刷新 `last_heartbeat`（喂 C6/P4）。
2. **worker-lost 在飞 job 处理（hub，设计 §8.5 MVP）**：连接断开（含半开检出）时，该 worker 名下所有**在飞 server 端 job**经既有 finish 路径置 `failed`（error `worker disconnected`）。**fail-fast**，不实现"挂起等重连"窗口。
3. **断线重连（worker 端）+ C7 多地址**：指数退避 + 抖动重连循环；`server_link.urls` 可列**多个 hub 地址**（或单个 VIP），失败时轮换地址；重连成功后**重新 `register`**（重建身份）。
4. **多 worker**：多个不同 `worker_id` 同时在线，派发按 `worker_id` 路由；per-worker `max_concurrent` 并发上限——**at-capacity 拒绝派发并回明确错误**（队列化为后续）。同 `worker_id` 重注册替换旧连接（优雅关闭），**仅当同一 token 认证**时允许（主文档 §7）。
5. **优雅关闭**：hub/worker 的心跳循环、读循环、重连循环在 serve/worker 关闭时干净停止（镜像 `serve.go` 的 stop-channel + `signal.Stop` 模式），无 goroutine/fd 泄漏。

### 2.2 C7 边界与 Scope OUT（显式，主文档 §3）

C7 **仅** = worker 端"多 hub 地址 + 指数退避重连"。**显式排除**（本轮不做）：

- 多 hub **共享注册表 / 同步**（多个 hub 实例看到同一份 worker 注册表）；
- **跨 hub job 接管 / hand-off**（worker 从 hub-A 漂移到 hub-B 后，在飞 job 在 hub-B 续跑）；
- **leader 选举 / HA 协调**。

> C7 的"多地址"语义是 worker **客户端容灾**：地址列表互为**冗余入口**（同一 hub 的多 VIP，或多个**各自独立**的 hub）。worker 任一时刻只连一个 hub；切换 hub 即"换一个控制面"，**不携带旧 hub 的在飞 job**（旧 hub 已按 §2.1#2 把它们 fail 掉）。这与"跨 hub 接管"无关。

- **seq-offset 续传重放**（设计 §8.5"恢复(后续)"）：本轮**不做**。worker-lost 一律 fail-fast（见 §6.3 决策）。

---

## 3. 改动清单（file → 改动）

> P1 已建包：`internal/wsproto`（帧）、`internal/wshub`（hub + WorkerRegistry）、`internal/worker`（客户端）、`internal/runner/worker`（workerRunner）、`internal/commands/worker.go`（命令）。P3 在其上加弹性能力，不新建包。

### 3.1 hub 侧（`internal/wshub`）

| file | 改动 |
|---|---|
| `internal/wshub/registry.go`（P1）| `WorkerRegistry` 项加 `lastHeartbeat int64`（原子，秒）、`maxConcurrent int`、`inflight map[jobID]struct{}`（在飞 job 集合，dispatch 时加、result/cancel/disconnect 时删）；新增 `TouchHeartbeat(workerID)`、`Inflight(workerID) []jobID`、`TryReserve(workerID) (ok bool)`（at-capacity 判定，原子占位）、`Release(workerID, jobID)`。同 `worker_id` 重注册替换：`Put` 检出旧 conn → 先 `gracefulClose(old)` 再装新（仅当 token 相同，token 校验在 Accept 入口已过，见 §3.1 hub.go）。`-race` 安全：注册表 `sync.Mutex` + 原子计数。 |
| `internal/wshub/hub.go`（P1）| **读循环**：在每次 `conn.Read` 前设 `SetReadDeadline(now + readDeadline)`（coder/websocket 用 `Read(ctx)` + `context.WithTimeout`/`conn.SetReadLimit`，读循环每轮重建带 deadline 的 ctx）；读到任意帧（含 `pong`）→ `registry.TouchHeartbeat`。读超时/EOF/错误 → 退出读循环 → 调 `onDisconnect(workerID)`。**心跳发送循环**：每 worker 一个 goroutine，按 `pingInterval` 发 `ping{ts}`；收到 `ping` 回 `pong{ts}`（对称）。**onDisconnect**：从 registry 摘 worker；对 `Inflight(workerID)` 每个 jobID 调用注入的 worker-lost 回调（§3.2）。所有 per-worker goroutine 用 per-conn `done` channel + 父 stop 联动关闭。 |
| `internal/wshub/dispatch.go`（P1）| `Dispatch(workerID, frame)`：先 `registry.TryReserve(workerID)`，**false → 返回 `ErrWorkerAtCapacity`**（明确错误，不阻塞、不排队）；worker 不在线 → `ErrWorkerOffline`。reserve 成功后记 `inflight`，发 `dispatch` 帧。`result`/`cancel` 入站或 disconnect → `Release`。 |
| `internal/wshub/config.go`（新增或并入 hub.go）| 心跳/读截止参数（§5），从 serve 配置读，缺省用默认常量。 |

### 3.2 workerRunner 侧（`internal/runner/worker`）

| file | 改动 |
|---|---|
| `internal/runner/worker/runner.go`（P1）| `workerRunner.Run` 的等待路径需感知 worker-lost：当前 P1 是"发 dispatch → 阻塞等 `result` 帧/ctx.Done"。P3 增第三个唤醒源——**hub 的 disconnect 信号**：hub 在 `onDisconnect` 时关闭该 job 的"lost" channel（per-job，dispatch 时注册），`Run` 的 `select` 命中 lost → 返回 `runner.Result{ExitCode: -1, Err: errWorkerDisconnected}`。`classify`（`job/service.go:461`）已把 `res.Err != nil && ctx 无 deadline/cancel` 映射为 `StatusFailed`——**无需改 classify**，error 文案 `worker disconnected` 透传到 `jobs.error`。 |

> 关键：worker-lost → failed 的"接线"落在 workerRunner.Run 的返回值上，复用 `execute`→`finish` 既有终态路径（`service.go:358-361`），**不新增 job 包的 finish 入口**。hub 只需把 disconnect 广播给受影响的在飞 job 的 lost channel。

### 3.3 worker 客户端（`internal/worker`）

| file | 改动 |
|---|---|
| `internal/worker/client.go`（P1）| **重连循环**（C7 核心）：`Run(ctx)` 外层 for 循环——尝试连接 → 注册 → 进入收发循环；收发循环退出（连接断）→ 退避后重试。地址轮换：维护 `urls` 索引，每次连接失败 `idx = (idx+1) % len(urls)`；连接成功重置退避。退避：full jitter 指数（§5）。worker 端**也设读截止 + 发/回心跳**（与 hub 对称），检出 hub 半开。ctx 取消（worker 进程关闭）→ 退出所有循环，优雅关闭连接（close code 1001 going-away）。 |
| `internal/worker/heartbeat.go`（新增）| worker 侧心跳 goroutine：按 `pingInterval` 发 `ping`、回 `pong`、读截止刷新。与收发循环共享 per-conn done。 |
| `internal/worker/backoff.go`（新增）| `nextBackoff(attempt)` full-jitter 计算（§5），纯函数，便于 `-race`/单测。 |

### 3.4 配置面（`internal/config`）

| file | 改动 |
|---|---|
| `internal/config`（worker 配置结构，P1 已建 `server_link`）| `ServerLink.Reconnect` 结构补全：`InitialBackoffMs`、`MaxBackoffMs`、`PingIntervalSec`、`ReadDeadlineSec`（缺省见 §5）。`server_link.urls []string`（P1 已定义）多地址。hub 侧 serve 配置可选 `workers.<id>` 不变（P1）；心跳/读截止参数可放 `server.ws_heartbeat`（可选，缺省常量）。 |

### 3.5 装配与生命周期（`internal/commands`）

| file | 改动 |
|---|---|
| `internal/commands/serve.go`（`runServe`）| hub 单例（P1 在 `buildCore` 建）需要随 serve 优雅关闭。新增 `stopHub := make(chan struct{}); defer close(stopHub)`，把 `stopHub` 传给 hub，hub 据此停所有 per-worker goroutine（心跳/读循环）并优雅关闭所有连接。**镜像 `startPruneLoop`/`startReloadLoop` 的 stop-channel 模式**（`serve.go:89-98`）。 |
| `internal/commands/worker.go`（P1）| worker 命令的信号处理：`signal.Notify(SIGINT/SIGTERM)` → cancel worker `ctx` → `client.Run` 退出重连/收发/心跳循环，优雅关闭。镜像 serve 的 `signal.Stop(sig)` defer，避免信号 goroutine 泄漏。 |

---

## 4. 默认参数表

> 全部可配置；下表为缺省常量（配置缺省时生效）。命名 `*Default` 常量集中放 `internal/wshub`（hub 侧）与 `internal/worker`（worker 侧），便于单测覆盖。

### 4.1 心跳 / 读截止

| 参数 | 默认 | 配置键（worker.yaml `server_link.reconnect` / serve `server.ws_heartbeat`） | 说明 |
|---|---|---|---|
| ping interval | **15s** | `ping_interval_sec` | hub 与 worker 对称发 ping 的周期 |
| read deadline | **45s**（≈3× ping） | `read_deadline_sec` | 单次 `conn.Read` 的截止；超时即判半开/断线。容忍 2 次丢 ping/pong |
| pong 容忍 | 隐含在 read deadline | — | 任意入站帧（含业务帧）都刷新心跳，非仅 pong；空闲连接靠 ping 维持 |

> 选 45s（3×）而非 2×：避免 GC/调度抖动下的误判离线（误判 → 误把在飞 job fail，代价高于晚 15s 检出）。读截止 ≥ 2× ping 是硬约束（否则正常心跳也会误超时）。

### 4.2 重连退避（worker 端，C7）

| 参数 | 默认 | 配置键（`server_link.reconnect`） | 说明 |
|---|---|---|---|
| initial backoff | **1s** | `initial_backoff_ms`（1000） | 首次重连前等待基值 |
| max backoff | **30s** | `max_backoff_ms`（30000） | 退避上限（封顶） |
| 策略 | **full jitter** | —（固定算法） | `sleep = rand(0, min(max, initial * 2^attempt))`；连接成功后 `attempt` 归零 |
| 地址轮换 | 每次失败 `idx=(idx+1)%len(urls)` | —（由 `urls` 长度决定） | 多地址间轮转，单地址则原地重试 |

> full jitter（AWS 退避范式）优于纯指数：多 worker 同时被一个 hub 重启踢下线时，full jitter 摊平重连惊群（thundering herd）。max 30s 兜住"hub 长时间不可用"时的重试频率。

---

## 5. 关键流程

### 5.1 心跳 + 半开检测（hub 侧）

```txt
hub per-worker:
  [心跳循环]  every pingInterval(15s): send ping{ts=now}
  [读循环]    loop:
                ctx = WithTimeout(parent, readDeadline=45s)
                fr, err = conn.Read(ctx)
                if err (timeout / EOF / closed):  break -> onDisconnect(workerID)
                registry.TouchHeartbeat(workerID)        # 刷新 last_heartbeat(喂 C6)
                switch fr.type:
                   ping -> send pong{ts=fr.ts}
                   pong -> (仅刷新心跳, 已在上一行做)
                   log/status/result/interaction -> 既有 demux(P1/P2)
  [onDisconnect] registry.remove(workerID); 关闭 per-conn done(停心跳循环);
                 for jobID in registry.Inflight(workerID): close(lostCh[jobID])  # 触发 §5.3
```

半开（TCP 静默死，无 FIN）：心跳循环仍在 `send ping`（写可能也阻塞/失败），但**决定性检出**来自读循环——45s 内收不到任何帧（包括对端 pong）→ `conn.Read` 返回 deadline 超时 → 走 onDisconnect。**这是评审 #7 的核心：没有显式读截止，半开连接永远卡在 `Read` 上检测不到。**

worker 侧对称：自己也设读截止 + 发 ping/回 pong，检出 hub 半开 → 退出收发循环 → 进重连（§5.2）。

### 5.2 断线重连 + 多地址（worker 侧，C7）

```txt
worker client.Run(ctx):
  attempt = 0; idx = 0
  for ctx not done:
     url = urls[idx]
     conn, err = dial(url, Bearer token)
     if err:
        idx = (idx+1) % len(urls)                 # 轮换地址
        sleep( nextBackoff(attempt) )             # full jitter, cap 30s
        attempt++
        continue
     # 连上：注册重建身份
     send register{worker_id, labels, projects, agents, max_concurrent}
     reg = recv registered
     if !reg.accepted:                            # token/worker_id 绑定不符(§7)
        log + sleep(backoff) + idx++ + continue   # 不会自愈, 但仍重试(配置可能被改对)
     attempt = 0                                  # 成功, 退避归零
     启动 [心跳循环] + [收发循环]                  # per-conn done 联动
     <收发循环阻塞, 直到 conn 断 / ctx 取消>
     关闭 per-conn(停心跳)
     # 回到 for 顶: ctx 未取消则继续重连
  # ctx 取消: 优雅关闭(close 1001), 返回
```

**验收场景（bad-then-good）**：`urls: [wss://bad.invalid/..., wss://good-hub/...]`。dial bad 失败 → 轮到 good → 连上注册。hub 重启（good 短暂不可用）→ 收发循环断 → 退避重试（轮换：bad 失败→good，good 起来后连上）→ re-register。一次 transient 重启**不**永久断 worker。

### 5.3 worker-lost 在飞 job → failed（时序）

```txt
hub.onDisconnect(w1)
  └─ for jobID in Inflight(w1):           # dispatch 时登记的在飞集合
        close(lostCh[jobID])              # 唤醒对应 workerRunner.Run

workerRunner.Run (per job, 阻塞在 select):
  select {
    case res := <-resultCh[jobID]:        # 正常: worker 推回 result
    case <-ctx.Done():                    # 超时/取消(P2)
    case <-lostCh[jobID]:                 # ★ worker-lost
        return runner.Result{ExitCode:-1, Err: errors.New("worker disconnected")}
  }

job.Service.execute (service.go:358-361):
  res = run.Run(...)                      # 拿到上面的 Result
  status,code,err = classify(ctx,res)     # ctx 无 deadline/cancel -> StatusFailed (service.go:470-471)
  s.finish(entry, jobID, StatusFailed, -1, "worker disconnected")  # 终态入 DB, 既有路径
```

**接线点确认**：`classify`（`service.go:461`）对 `res.Err != nil` 且 ctx 无 deadline/cancel 的分支返回 `StatusFailed, res.ExitCode, res.Err`（`service.go:470-471`）——worker-lost 的 `Err` 直接成为 job `error` 文案。**无需改 classify、无需新增 finish 入口**，仅 workerRunner.Run 多一个 `lostCh` 唤醒源 + hub 在 onDisconnect 广播之。

### 5.4 多 worker 路由 + at-capacity 拒绝

```txt
Submit(runner=worker, worker_id=w1)  ->  workerRunner.Run  ->  hub.Dispatch(w1, frame)
   hub.Dispatch:
     if !registry.online(w1):           return ErrWorkerOffline   -> Run 立即返回 Result{Err: offline}
     if !registry.TryReserve(w1):       return ErrWorkerAtCapacity -> Run 立即返回 Result{Err: at capacity}
     registry.inflight[w1].add(jobID); send dispatch
```

- **路由隔离**：`worker_id` 是路由键，w1 的 dispatch 只发往 w1 的 conn；w2 收不到 w1 的帧（registry 按 id 取 conn）。
- **at-capacity = 拒绝（MVP 决策）**：`TryReserve` 在 `inflight 计数 >= max_concurrent` 时返回 false → `ErrWorkerAtCapacity`。job 直接 `failed`（error 明确）。
  - **为何拒绝而非排队**：hub 侧排队需要队列存储 + 公平调度 + 超时治理 + worker 上线/下线时的队列再分配——属 WP4 调度器范畴。MVP 让调用方（或上层）感知"容量满"并自行重试/换 worker，语义简单、无隐藏积压。**队列化记为后续（WP4）。**
  - worker 本地仍有自己的 per-project 并发闸（`semaphore`，`service.go:284`）作二道防线；hub 的 `max_concurrent` 是前置快速拒绝。

### 5.5 同 worker_id 重注册替换（主文档 §7）

```txt
w1(老连接在线) ；w1'(同 worker_id 新连入)
  Accept 入口: Bearer token 校验 + token<->worker_id 绑定校验(§7, P1已落)  ── 必须同 token 才放行
  registry.Put(w1', conn'):
     若已存在 w1 老 conn -> gracefulClose(old, close-code 1000) ; 装 w1'->conn'
     老 conn 的读循环因关闭退出 -> onDisconnect(w1)? 
        ★ 注意: 替换不应 fail 掉刚被新连接接管的在飞 job。
        实现: Put 在替换时把老 conn 标记 "superseded",老读循环退出走 onDisconnect 时
              检查 registry 当前 conn != 老 conn -> 跳过 worker-lost(不 fail job)。
              (在飞 job 的 lostCh 仍挂着, 由新连接的 result/断线决定其命运)
```

> 这是替换语义与 worker-lost 语义的**唯一交叉点**，实现时必须区分"被取代的旧连接"与"真正掉线"——否则正常重连会误杀在飞 job。单测须覆盖（§7 用例 5）。

### 5.6 优雅关闭（goroutine 生命周期）

| 进程 | 触发 | 停止链 |
|---|---|---|
| serve（hub）| `runServe` 返回（`defer close(stopHub)`，镜像 `serve.go:89-98`）| hub 收 stopHub → 关所有 per-conn done → 心跳循环/读循环退出 → 各 conn 优雅关闭（close 1001）→ 无 goroutine/fd 泄漏 |
| worker | SIGINT/SIGTERM → cancel ctx（`worker.go`，`signal.Stop` defer 镜像 `serve.go:168`）| ctx 取消 → 重连循环/收发循环/心跳循环退出 → 优雅关闭连接 |

---

## 6. 设计决策记录（自动决策，SR1430 留痕）

- **6.1 read deadline = 3× ping（45s）**：见 §4.1。误判离线代价（误 fail 在飞 job）> 晚检出代价，取宽松倍率。
- **6.2 full jitter 退避**：摊平多 worker 对单 hub 重启的重连惊群。
- **6.3 worker-lost = fail-fast（不实现挂起窗口）**：设计 §15 的"挂起等重连窗口"**本轮不做**。理由：(1) 无 seq-offset 续传重放（设计 §8.5"恢复"明确推迟、主文档 §3 OUT），重连后无法把已 fail 的 job 无缝续上；(2) 挂起窗口需要 job 进入新的 `suspended` 准态 + 窗口计时 + 重连后的 re-attach 协议，属 WP4/后续工程；(3) fail-fast 语义清晰、用户可见、可重投（重新 Submit）。**挂起窗口记为已文档化的后续选项，非本轮交付。**
- **6.4 at-capacity = 拒绝**：见 §5.4。
- **6.5 心跳帧任意入站帧均刷新**：不强制只认 pong，业务帧（log/result）天然证明连接活，减少空闲误判。

---

## 7. 测试与验收

> 跑测：`export PATH=/path/to/ws-root/linux-env/sdk/gosdk/go1.25.10/bin:$PATH; cd tools/gofer && go test ./internal/wshub/... ./internal/worker/... ./internal/runner/worker/... -race`

### 7.1 验收清单（对齐主文档与任务验收）

| # | 验收项 | 方法 | 通过判据 |
|---|---|---|---|
| 1 | **半开检测** | 用一对内存/管道 conn 模拟 worker，连上、dispatch 一个长 job 后**停止一切发送/回 pong**（模拟 TCP 静默死）| 在 ≤ read deadline（45s，测试用短值如 200ms 注入）窗口内 hub 标记该 worker 离线；其在飞 job → `failed`，error 含 `worker disconnected` |
| 2 | **重连 + 多地址（bad-then-good）** | `urls=[bad.invalid, 测试 hub]`；启动 worker | worker dial bad 失败 → 轮到 good → 连上 + re-register（registry 出现该 worker）|
| 3 | **transient hub 重启** | worker 连上后关闭测试 hub，短暂后重启同地址 | worker 退避重试不退出；hub 回来后 re-register；连接恢复（**不**永久断）|
| 4 | **worker disconnect mid-job → failed** | dispatch 后直接 close worker conn | hub onDisconnect → 在飞 job `finish(StatusFailed, "worker disconnected")`；DB 终态行可查 |
| 5 | **同 worker_id 重注册替换** | w1 在线（有在飞 job）→ 同 token 的 w1' 连入 | 旧 conn 优雅关闭、registry 指向 w1'；**被取代的旧连接退出不误 fail** 已接管的在飞 job（§5.5）；异 token 的 w1' → `registered{accepted:false}` 拒绝 |
| 6 | **多 worker 路由隔离 + at-capacity** | 起 w1、w2；向 w1 派发 N 个 | w2 收不到 w1 的 dispatch；w1 `max_concurrent` 满后第 N+1 个派发被 `ErrWorkerAtCapacity` 拒绝、job `failed` 且 error 明确 |
| 7 | **优雅关闭无泄漏** | serve/worker 启动→关闭；`goleak`（或 `runtime.NumGoroutine` 前后比对）+ fd 计数 | 关闭后心跳/读/重连 goroutine 全部退出，无残留 goroutine/fd |
| 8 | **`-race`** | registry + 心跳 + 重连并发路径全量 `-race` | 无数据竞争告警 |

### 7.2 单元 / 集成测试要点

- `internal/worker/backoff_test.go`：`nextBackoff` full-jitter 边界（attempt 0 / 大 attempt 封顶 max / 落在 `[0, cap]`）、attempt 归零。纯函数，确定性可测（注入 rand 源）。
- `internal/wshub/registry_test.go`：`TryReserve`/`Release` 并发 `-race`（多 goroutine 抢占同 worker 的容量名额，计数不越界）；`Put` 替换语义（旧 conn superseded 标记，不误触 worker-lost）；`Inflight` 在 dispatch/result/disconnect 下的增删一致。
- `internal/wshub/hub_test.go`：半开检测（注入短 read deadline + 哑 worker conn 停发）；onDisconnect 广播 lostCh → workerRunner.Run 返回 failed。用内存 conn pair（`net.Pipe` 之上跑 coder/websocket，或 hub 接口注入假 conn）避免真实网络。
- `internal/runner/worker/runner_test.go`：`Run` 三唤醒源（result / ctx.Done / lostCh）各自的返回 `Result`，确认 lostCh 命中 → `Err="worker disconnected"`、经 `classify` 为 `StatusFailed`（可直接断言 classify 输出，复用 `service.go:461`）。
- 集成（`internal/worker` + `internal/wshub` 跨包，可放 `internal/wshub` 的 `_test` 用真 listener 起最小 hub）：场景 2/3/4/5/6 的端到端。

### 7.3 与主文档一致性检查（实施前 self-check）

- 帧类型仅用主文档 §5 已声明的 `ping`/`pong`/`dispatch`/`result`/`cancel`/`register`/`registered`——**P3 不新增帧类型**（worker-lost 是 hub 内部信号，不上线）。✔ 与评审 #6"WP1 一次定全帧类型"一致。
- worker-lost 复用既有 finish/classify 路径（`service.go:358-361/461`），不新增 job 包终态入口。✔ 与设计 §10"唯一侵入点"一致。
- 多地址语义不触碰多 hub 共享注册表/接管/选主。✔ 主文档 §3 OUT。

---

## 8. 与主文档/设计的不一致（仅标记，需实施前确认）

1. **设计 §8.5 vs 本文 §6.3（无实质冲突，已收敛）**：设计 §8.5"恢复(后续)：worker 重连 re-register，log 帧带 seq/offset，hub 从上次镜像偏移续写；worker 进程存活则 job 不中断"——这是**未来选项**，本轮 fail-fast。设计 §15 待确认项"是否需要挂起等重连窗口"在此**收敛为：不做，记为后续**。主文档 §3 OUT 已含"断线后 job seq-offset 续传重放（设计 §8.5 明确推迟）"，故本文与主文档一致，仅设计正文 §8.5 未显式标"后续"为推迟——**建议设计 §8.5 注一句"恢复路径 WP3 不实现，见 P3 §6.3"**（文档收尾时回填，非阻塞）。

2. **at-capacity 行为主文档未明定**：主文档 §6 配置面有 `max_concurrent`，但未规定 hub 在容量满时"拒绝 vs 排队"。本文 §5.4 **决策为拒绝（`ErrWorkerAtCapacity`）**，队列化记为 WP4。**需用户/审核确认此决策**（任务说明已推荐 reject-with-error，本文采纳）。

3. **心跳/读截止参数主文档未给数值**：主文档 §5 仅列 `ping/pong` 帧、§6 `reconnect:{初始/最大退避、抖动}` 占位"默认见 P3"。本文 §4 给定全部数值（ping 15s / read deadline 45s / backoff 1s→30s full jitter）——**填充而非冲突**，符合主文档"默认见 P3"的留空意图。
