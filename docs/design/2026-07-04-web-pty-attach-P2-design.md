# WEB-03 P2：worker 端到端 pty attach 设计细化

> 上游：主设计 [`2026-07-03-web-pty-attach-design.md`](2026-07-03-web-pty-attach-design.md) v0.8（§5 架构 / §7 时序 / §14 阶段）；P1 计划 [`../plans/web-pty-attach/P1-plan.md`](../plans/web-pty-attach/P1-plan.md)（§P2 前瞻）；评审 [`../review/2026-07-03-web-pty-attach-codex-review.md`](../review/2026-07-03-web-pty-attach-codex-review.md)（round-2 阻断5=取消协议）+ [`../review/2026-07-04-web-pty-attach-P2-codex-review.md`](../review/2026-07-04-web-pty-attach-P2-codex-review.md)（本文 v0.1 评审）。
> 本文只细化 **P2（worker 端到端）**：serve 侧协议/relay/安全闸已 P1 落地，P2 补 **worker 真拨入 + 字节泵 + resize + 取消时序 + 断连语义**，改动面最大。
> 铁律 G023（非交互路径零行为变化）/ G022·G024（`internal/worker` 可 import `runner/pty`，但 `job` 不反向 import pty/ptyrelay；worker 经窄接口拿 `PtySession`）。

## 修订记录
| 版本 | 日期 | 说明 |
|---|---|---|
| v0.1 | 2026-07-04 | 初稿：6 决策 + 5 时序图 + 帧/接口清单。 |
| **v0.2** | 2026-07-04 | **codex 评审后大改**（2 事实性错误 + 4 阻断 + 4 高 + 4 中）：① **输出所有权倒置**——`PtyRunner.Run` 已有 `io.Copy(io.Discard,sess)`，P2 pump 再读 = 双读竞争吞字节 → 改 `SessionObserver` 交接，worker 成唯一 reader（D-P2-3 重写）；② **ack 两段化**——`Result` 只证 worker teardown，不证 serve 读完尾字节（两连接无跨序）→ 关闭权归 relay recordLoop EOF、host 等 `relay.Done()` 再 finish（D-P2-2/6 重写）；③ **拨入改事件驱动 rendezvous**（非 2s 轮询，防本地并发槽排队击穿，D-P2-3）；④ **断连判据改 state+selfClosing 原子标志**（非 `Done()`，防自然退出误判 cancel，D-P2-5 重写）；⑤ 新增 **D-P2-7 per-dispatch URL threading**（去全局 mutable `currentURL`）+ **D-P2-8 Registry.Close 锁内翻状态/锁外关 IO**（去全局 HOL）；⑥ 补 fail-fast（缺 nonce/session-id）+ 尾字节 e2e 证明。`{t:x,code}` exit 帧移 P4。 |

## 0. 范围

**做**：worker 收 interactive dispatch → 本地 PtyRunner 起 pty → **worker 成 pty 输出唯一 reader** + **eager 拨出第二条专用 pty ws** 到 serve → 双向字节泵（输出 binary / 输入 binary / resize text）→ **两段化取消/终态协议**（worker teardown ack + serve drain ack）→ 断连全链路。**e2e**（现有 ws-worker harness + `httptest` + Linux 真 pty）。

**不做**（留后阶段）：cast 加密 + `pty_sessions` 表（P3）；前端 `AttachTerminal.vue` + browser `{t:x,code}` exit 帧 + e2e 全矩阵（P4）；serve-local pty（drop-in，V1 不触发）。

## 1. 现状锚点（P1 已落地 + 本次评审核对，附文件:行）

| 组件 | 位置 | P2 依赖 / 评审要点 |
|---|---|---|
| serve pty ws 端点 | `httpapi/pty_connect_handler.go:54-98` | worker 按 hello=`{job_id,pty_session_id,relay_nonce}`(text) 拨入；校验 nonce 消费 + `callerID==WorkerID` + live instance + `binding.{JobID,PtySessionID}==hello.{...}`(`:70-72`) + job in-flight+interactive |
| remotePtySource | `httpapi/pty_source.go` | serve 眼中字节流：`Read`只收 **binary**=输出(`:44` 跳 text)；`Write`发 **binary**=输入；`Resize`发 **text** `{type:resize}`；`Close`(`:80-83`)关 ws |
| relay + 两层背压 + lease | `ptyrelay/relay.go` | `recordLoop`(`:120-138`)读 source→ring+cast+fanout；source EOF/err→`Close()`(`:235-253`)→`Done()`。**唯一关闭权**（见 D-P2-2） |
| relay 注册表 + nonce | `ptyrelay/registry.go`,`nonce.go` | `Prepare(pending_worker)`/`Issue`；`Consume`+`Open(nonce,source)`；`Close` 现**持锁调 Relay.Close**(`:183-203`, HOL 风险, D-P2-8) |
| browser attach ws | `httpapi/attach_handler.go` | ticket→`MarkAttached`→`AddViewer(lease)`→回放 `Scrollback()`→pump `viewer.Out()`；`relay.Done` 现只 close 4404(`:136-139`) |
| host worker runner | `runner/worker/runner.go` | interactive 已 `LiveInstance`+`Issue`+`Prepare`+Dispatch 带 `RelayNonce`(`:133-201`)；`ctx.Done` 现**立即返回**(`:229-242`, D-P2-6)；`resultCh`/`lostCh`(`:310-312`) |
| PtyRunner + PtySession | `runner/pty/{runner,session}.go` | **`Run` 已 `go io.Copy(io.Discard,sess)`**(`runner.go:53-58`, 事实性错误1)；`session.run`(`session.go:137-170`)`<-ps.done` 才返回；`State()`/状态常量；`Done()` 末尾才关(`:212-217`) |
| worker Client | `worker/{client,dispatch}.go` | 单 hub conn+writeMu+全 JSON；`handleDispatch`(`dispatch.go:23-73`)未投影 interactive；`recvLoop`(`client.go:301-347`) |
| core / worker 装配 | `core/core.go:78-106`,`commands/worker.go:185-204` | worker 与 serve 同走 `core.Build`：`Runners["pty"]=*PtyRunner`；Client 由 `worker.go:192` 建，可拿 `cr.Runners` |
| worker 本地 admission | `job/submit.go`,`execute.go`,`cancel.go` | `Submit` 异步起 `execute`；执行前等**项目/调用方并发槽**（排队可 >秒级，D-P2-3）；`Cancel` cancel ctx(`cancel.go:47-62`) |

**P1 已成立**：worker `handleDispatch` 强制 `Runner=local` → submit.go `req.Interactive && !remote` → 命中本机 pty runner；submit.go 已把 `Interactive/Cols/Rows` threaded 到 `runner.Request`→`PtyRunner.start`。**断点**：`handleDispatch` 构造 `JobRequest` 未带 interactive 字段（D-P2-4）。

## 2. 关键设计决策

### D-P2-1 worker 第二条专用 pty ws = 独立连接（非复用 hub conn）
hub conn 是单 conn+单 writeMu+全 `wsjson`；pty 是 binary 高频流，塞进去破坏 hub 顺序/背压（round-1 阻断3）。→ interactive job 起时 worker 另开 `websocket.Dial` 到 serve `/v1/workers/pty-connect`，独立 conn + 独立 write 锁 + binary 帧，与 hub 控制平面隔离。每 job 一条，job 结束即关。

### D-P2-2 关闭权归 relay recordLoop EOF；ack 两段化（**修阻断1**）
**核心纠正**：worker 侧「排空+关 pty ws 再发 Result」只保证 worker 本地 teardown 完成；`Result`(hub ws) 与 pty ws 尾字节**在两条独立连接上、无跨连接顺序**——host 收 Result 后若立即 `relayRegistry.Close` 会关掉 `remotePtySource` 的 ws，此时 serve `recordLoop` 可能尚未从 `conn.Read` 取出最后 binary/EOF → 截尾。
→ 定死**单一关闭权 = relay 自身 recordLoop 的 source EOF**：worker 排空后关 pty ws(FIN) → `remotePtySource.Read` 返 err → `recordLoop` 读完全部字节后 `relay.Close()` → `relay.Done()`。这是**唯一**「serve 已读完」的可信信号。
- **`Result` = worker-teardown ack**（供 exit 分类 / Outcome / 唤醒条件之一），**不**兼作 serve-drain ack。
- **`relay.Done()` = serve-drain-complete ack**（host 据此才 finish，D-P2-6）。
- host 的 defer `Close` 退化为**兜底**：仅在 grace 超时（worker/relay 卡死）才 force-close（此时截尾可接受）。
- 需 registry 暴露 `Done(jobID) <-chan struct{}`（pending 未 Open 的 relay 无字节可排、host 靠 grace 收敛）。

### D-P2-3 输出所有权倒置：`SessionObserver` 交接（**修事实性错误1 + 阻断2/3**）
真实 `PtyRunner.Run` 注册 session 后立即 `go io.Copy(io.Discard,sess)`（P0 spike 的临时 drain）。pty master **非 broadcast**，P2 pump 再 `sess.Read` = 两 reader 随机分流吞字节。且 v0.1 用 `LookupSession`+2s 轮询定位 session，会被 worker 本地**并发槽排队**击穿（`MaxConcurrent=1` 时长任务占槽 → interactive 排队 >2s → pump 放弃 → 漏拨）。
→ **倒置**：`PtyRunner` 加可注入的 `SessionObserver`：
```go
// internal/runner/pty
type SessionObserver interface {
    // 在 session 注册后、Run 阻塞前同步回调；observer 成为 sess 输出的【唯一 reader】。
    // 必须非阻塞（内部 spawn goroutine）。observer 未设时 PtyRunner 才跑默认 discard drain。
    OnSessionStart(jobID string, sess *PtySession)
}
func (r *PtyRunner) SetObserver(o SessionObserver) // worker 侧设；serve/测试 nil→保留 discard drain(G023 tests)
```
`Run`：`reg.add` 后 `if observer!=nil { observer.OnSessionStart(jobID,sess) } else { go io.Copy(io.Discard,sess) }`。worker Client 实现 `OnSessionStart`（jobID=worker 本地 id），经 **事件驱动 rendezvous** 交给 `handleDispatch` 的 pump（无轮询）：
```txt
handleDispatch(d): res=Submit(interactiveReq); localID=res.ID; putJobMapping(d.JobID,localID)
  if interactive: sess := cl.waitSession(ctx, localID)   // 阻塞至 OnSessionStart(localID)|job terminal|ctx
                  if sess!=nil { pumpDone = go pumpPty(ctx, sessURL, sess, d.JobID, d.PtySessionID, d.RelayNonce) }
OnSessionStart(localID,sess): rendezvous 投递(有 waiter→送; 无→buffer sessReady[localID])  // 无竞态
waitSession(ctx,localID): 命中 sessReady 立返; 否则挂 waiter, select{ sess | jobs.Wait(localID)终态 | ctx.Done }
```
- **单 reader**：observer 设时无 discard drain，pump 是唯一 reader（含正常输出/cancel 尾字节/scrollback/cast 全字节可证）。
- **无轮询**：`waitSession` 事件驱动，排队多久等多久；job 终态/ctx 唤醒防悬挂 → 修阻断3。
- G024：`SessionObserver` 接口在 `runner/pty` 定义，worker 实现并注入，`job` 不 import pty；worker 已 import `ptyrunner`。`LookupSession` 不再需要（session 直接交付）。

### D-P2-4 `Dispatch` 补 `PtySessionID` + handleDispatch 投影 + fail-fast
pty_session_id 是 **serve mint**（`runner/worker/runner.go:141`），worker 不知道，但 serve 端点强校验 `binding.PtySessionID==hello.PtySessionID`。→ `wsproto.Dispatch` 加 `PtySessionID string json:"pty_session_id,omitempty"`；host `Run` 填（已有 `ptySessionID` 变量）；worker `handleDispatch` 投影 `d.{Interactive,Cols,Rows}`→JobRequest，`d.{JobID,PtySessionID,RelayNonce}`→pump hello。
- **fail-fast（中1）**：`d.Interactive && (d.RelayNonce=="" || d.PtySessionID=="")` → **不 Submit 不可 attach 的 pty**，直接回 `Result{failed}`（旧 worker/坏 dispatch 语义明确）。

### D-P2-5 断连即终止：判据 = session state + selfClosing 原子标志（**修阻断4 TOCTOU**）
用 `PtySession.Done()` 是否已关区分「teardown 主动 EOF」vs「外部断连」有 TOCTOU：自然退出的 teardown 窗口里（close master 后、done 关前）pty ws 若因正常 FIN/抖动让 pump 读侧报错，会误判外部断 → `Cancel` → `run` 优先返 `ctx.Err()` → 自然完成被记成 cancelled。
→ 双判据：① output pump 因 `sess.Read` EOF 结束（=teardown）时置 pump 内 `selfClosing` 原子标志、并由**本端**主动 `conn.Close`；② input pump 遇 conn err 时：`if selfClosing || sess.State() ∈ {cancelling,exiting,closed}` → 良性（本端 teardown / 已在拆），**不 Cancel**；否则（`StateRunning` 且非本端主动关）→ 真·外部断 → `cl.jobs.Cancel(localID)`（防裸跑）。

### D-P2-6 host 取消/终态：三路 wait（`relay.Done()`/`lostCh`/grace）再 finish（**修阻断1 + 高4**）
`runner/worker.Run` 的 `ctx.Done()` 现发 `hub.Cancel` 后立即返回。→ **仅 interactive** 改为等 serve-drain ack 再返回：
```txt
ctx.Done(): hub.Cancel(worker,job)
  select { case <-relayRegistry.Done(jobID): /*serve 读完*/;  case <-sink.lostCh: /*worker掉线*/;  case <-time.After(hostCancelGrace): /*兜底force-close*/ }
  return ctx.Err()   // 分类仍从 ctx(cancel/timeout)
正常路径(<-sink.resultCh, worker_result): 收 Result 后同样 select{ <-Done(jobID) | <-After(grace) } 再返回
```
- 等 `relay.Done()`（非仅 `resultCh`）→ 保证 serve 读完尾字节；defer `Close` 变幂等 no-op（relay 已自关）。
- 三路建模：`lostCh`（worker 掉线，按 worker_lost 收敛 + 关 relay）；grace 超时才 force-close。
- 非 interactive 分支**字节不变**（G023）。

### D-P2-7 per-dispatch 会话 URL（去全局 mutable `currentURL`，**修高3**）
v0.1 拟在 `runSession` 存全局 `cl.currentURL`；多 hub failover 时旧 dispatch 的 pump 可能读到已切换的新 URL（nonce/relay 却绑旧 serve），且 data race。→ `recvLoop` 把当前 session URL 作**参数**传给 `handleDispatch`/`pumpPty`（每 dispatch 固定拨回派发它的那条 hub session 对应 serve）；用 `net/url` 改 path（`/v1/workers/connect`→`/v1/workers/pty-connect`），非法 path fail-fast（中2）。

### D-P2-8 serve `Registry.Close`：锁内翻状态、锁外关 IO（**修高2 HOL**）
`Registry.Close` 现持 registry 锁调 `Relay.Close()`→`src.Close()`→关 websocket；底层 close 阻塞会卡住全局 `Lookup/Open/MarkAttached/Prepare`（cancel storm 放大成全局 HOL）。→ 锁内只做：摘出 relay 引用 + 移除索引 + 标 `finalized`；锁外再 `relay.Close()`（关 source/viewers）。保持幂等。

## 3. 改动清单（精确到文件）

### 3.1 协议 / runner 接口
| 文件 | 改动 |
|---|---|
| `wsproto/frames.go` | `Dispatch` 加 `PtySessionID string`（D-P2-4） |
| `runner/pty/runner.go` | 加 `SessionObserver` 接口 + `SetObserver`；`Run` 按 observer 有无决定「交接 vs discard drain」（D-P2-3）；移除 `LookupSession` 设想 |
| `runner/pty/session.go` | 复用现有 `State()`/状态常量（D-P2-5 判据用）；无需改 |
| `runner/worker/runner.go` | Dispatch 补 `PtySessionID`；`relayPreparer` 接口加 `Done(jobID) <-chan struct{}`；`ctx.Done`+`resultCh` 分支 interactive 三路 wait（D-P2-6）；常量 `hostCancelGrace`（§6） |
| `ptyrelay/registry.go` | 加 `Done(jobID)`；`Close` 拆锁内翻状态/锁外关 IO（D-P2-8） |

### 3.2 worker 侧（主体）
| 文件 | 改动 |
|---|---|
| `worker/client.go` | `Config`/`Client` 加 rendezvous 结构（`sessReady`/`sessWaiters`+mu）+ `OnSessionStart`/`waitSession`（D-P2-3）；`recvLoop` 透传 session URL（D-P2-7） |
| `worker/dispatch.go` | `handleDispatch` 收 sessionURL 参；投影 interactive 字段 + fail-fast（D-P2-4）；interactive 时 `waitSession`→`go pumpPty`；**发 Result 前 join pumpDone**（保证 worker 排空+关 ws 先于 Result） |
| `worker/pty_pump.go`（新） | 拨出 + hello + 双向泵 + resize + selfClosing/断连判据（§5、D-P2-5）；`derivePtyConnectURL`（net/url，D-P2-7） |
| `commands/worker.go` | 从 `cr.Runners[ptyrunner.Name]` 断言 `*PtyRunner`→`SetObserver(cl)`（Client 建好后注入）；nil-safe（无 pty backend 不注入，interactive admission 早拒） |

### 3.3 serve 侧（小）
| 文件 | 改动 |
|---|---|
| `httpapi/attach_handler.go` | **P2 仅需**：`relay.Done` 后统一收尾 close（现行为够；`{t:x,code}` exit 帧 = **P4**，避开高1 的 select 竞态，P2 不引入）|

> **relay 不加 exit 语义**（保 leaf，G022）；browser exit code 走 job 终态真源，属 P4 前端。

## 4. 关键流程时序

### 4.1 正常：attach → 交互 → agent 自然退出
```txt
browser        serve(host-runner + relay)        hub        worker(Client+PtyRunner)          agent
                dispatch(+nonce+ptySID) ───────────────▶ handleDispatch: 投影+fail-fast; Submit(interactive)
                                                          (排队/起 pty) PtyRunner.Run: reg.add
                                                          →observer.OnSessionStart(localID,sess)  [唯一 reader]
                                                          waitSession 命中→go pumpPty
                pty-connect ◀════════════════════════════ Dial /pty-connect(bearer); hello{job,ptySID,nonce}
                consume nonce+校验 live instance ✔
                Open(nonce,remoteSource)→relay open; recordLoop 读 source
  {t:i} ─▶ SendInput ─▶ src.Write(binary) ═════════════▶ conn.Read binary ─▶ sess.WriteInput ─▶ stdin
  ◀binary◀ viewer.Out ◀ fanout ◀ recordLoop ◀ src.Read ◀═ conn.Write binary ◀ sess.Read(pump) ◀ stdout
  {t:r} ─▶ relay.Resize ─▶ src.Resize(text) ═══════════▶ conn.Read text{resize} ─▶ sess.Resize
              agent 退出→PtySession 自然 teardown(停input→closeMaster→wait→publishExit→done)
              out pump: sess.Read=EOF→selfClosing=1→排空→conn.Close(FIN)
              recordLoop 收 EOF→relay.Close→relay.Done()          Wait 终态→(join pumpDone)→发 Result(done)
              host: <-resultCh → select{<-Done(job)已闭|grace} → 返回 → finish; defer Close=no-op
  ◀ ws close ◀ attach: relay.Done→收尾 close
```

### 4.2 host cancel / timeout（**取消协议核心**）
```txt
browser        serve host-runner + relay          hub        worker                          agent
            ctx.Done()(cancel/timeout)
            hub.Cancel(worker,job) ─────────────────────▶ recvLoop TypeCancel→jobs.Cancel(localID)
            ★interactive 三路 wait(不立即返回):                job ctx 取消→PtyRunner ctx.Done
              select{ <-Done(job) | <-lostCh | <-After(grace) }  PtySession cancelling→有序 teardown
                                                                 closeMaster(+kill)→wait(bounded)→publishExit→done
                                                                 out pump EOF→selfClosing=1→排空→conn.Close(FIN)
            recordLoop 收 EOF→relay.Close(尾字节已全入 ring/cast)→Done()
            <-Done(job) 命中 → 返回 ctx.Err()→finish            Wait 终态→(join pumpDone)→发 Result(cancelled)
            defer Close = 幂等 no-op                              (input pump 见 selfClosing→不误 Cancel, D-P2-5)
  ◀ws close◀ attach: relay.Done→收尾
```
**要点**：① 关闭权=worker FIN→recordLoop EOF（尾字节先入 ring/cast 才关）；② host 等 `relay.Done()`（serve-drain ack）而非仅 `resultCh`；③ 仅 grace 超时（worker 卡死）才 defer force-close。

### 4.3 pty ws 外部断连（serve 崩/网络断，job 仍在跑）→ 断连即终止
```txt
serve                          worker pumpPty                          agent
 (serve 重启/idle-kill)
 pty ws 断 ────────────────▶ in/out pump conn err
                             判据: !selfClosing && sess.State()==Running ⇒ 真·外部断
                             → cl.jobs.Cancel(localID) ─────────────▶ 正常 teardown→退出→Result(failed/cancelled)
 (relay 端: remoteSource 随 conn 断→recordLoop EOF→relay.Close→Done)
```

### 4.4 worker↔hub 断连（P1 既有，P2 验证）
```txt
host: boundedSink.OnDisconnect→lostCh；interactive 三路 wait 命中 lostCh→按 worker_lost 收敛+关 relay→job failed
worker 整体掉线: 第二条 pty ws 同断→serve recordLoop EOF→relay.Close(幂等)
```

### 4.5 browser 断连（会话存活，K3；P2 验证 worker 不受影响）
```txt
browser 断→attach ws 关→viewer.Close→relay/pty/worker job 全不受影响；重连(新 ticket)→AddViewer→回放 Scrollback
```

## 5. worker 拨出连接管理（`pty_pump.go`）
```txt
pumpPty(ctx, sessionURL, sess, remoteJobID, ptySessionID, nonce) (pumpDone chan):
 1. url := derivePtyConnectURL(sessionURL)  // net/url: path→/v1/workers/pty-connect; 非法 fail-fast
    conn := websocket.Dial(ctx, url, bearer=cl.token)   // worker 可设 Authorization header
    dial 失败 → cl.jobs.Cancel(localID)(断连即终止); close(pumpDone); return
 2. wsjson.Write(conn, hello{job_id:remoteJobID, pty_session_id:ptySessionID, relay_nonce:nonce})
    // serve 校验失败以 close-code 关 conn → 下面 read/write err → 断连分支
 3. out goroutine(唯一 reader): for{ n,err:=sess.Read(buf); if n>0 conn.Write(binary); if err{ selfClosing=1; break } }
                                 结束(sess EOF=teardown)→conn.Close(FIN)  // 触发 serve recordLoop EOF
    in  goroutine: for{ typ,data,err:=conn.Read(); if err break;
                        if binary: sess.WriteInput(data);
                        if text: 解析{type:resize}→sess.Resize(clamp cols1..500/rows1..200) }
 4. 收敛: 等 out 结束(排空毕);  若 in 先因 conn err 结束 且 !selfClosing && sess.State()==Running: cl.jobs.Cancel(localID)
    close(pumpDone)  // handleDispatch join 后才发 Result
```
- **单 reader**：out goroutine 是 sess 唯一 reader（PtyRunner 已因 observer 关掉 discard）。
- **起手 pty buffer**：eager 下 serve relay pending、端点已 up，dial ~1 RTT；交互 agent 启动不 flood，dial 期间 sess 暂不读可接受（plan 内若需可加本地 staging）。
- **写并发**：pty ws 独立 write 锁；out 是唯一 binary writer，hello 起 goroutine 前串行写。

## 6. 三个超时的关系
| 超时 | 位置 | 值 | 作用 |
|---|---|---|---|
| `PtySession.defaultGrace` | `session.go:45` | 5s | teardown 内等被 kill 子进程 reap 上限 |
| `hostCancelGrace`（新） | `runner/worker.go` | ~10s | host 等 **`relay.Done()`（serve 读完尾字节）** 上限；超时才 force-close |
| `waitSession`（新，无固定值） | `worker/client.go` | 事件驱动 | 等 session start，靠 job 终态/ctx 唤醒，非超时 |
> 约束 `hostCancelGrace ≥ PtySession.defaultGrace + RTT + serve drain`（覆盖 worker teardown **且** serve recordLoop 读尾），拟 10s；stage/请求超时 ≥ 该值。

## 7. 安全（P2 增量）
- pty ws 拨出用 **worker token**（bearer header）；serve `callerID==binding.WorkerID` → worker token 只能拨自己被派发的 job（nonce 绑 worker_id+instance_id）。
- 复用 P1 五闸/nonce 原子消费/instance 校验，不放宽。
- fail-fast（D-P2-4）防「interactive=true 但缺凭据 → 起不可 attach 的裸 pty」。
- 断连即终止（D-P2-5）防「pty ws 断了 agent 还在 worker 裸跑无人管」。

## 8. e2e 测试矩阵（P2 子集，现有 harness + Linux 真 pty）
两条真 `websocket.Dial`（hub + pty-connect）+ `httptest.Server`：
1. 正常：dispatch→observer 交接→pty ws 建立→输入 echo→输出回传 viewer→resize 生效。
2. **尾字节证明**（遗漏项）：child 收 cancel 后先输出 sentinel 再退出；断言 **browser/relay ring 收到 sentinel 后 host job 才 finish**（验 D-P2-2 关闭权 + D-P2-6 等 Done）。
3. cancel：host cancel→worker teardown→relay 读完→Done→host 返回 cancelled；input pump 不误 Cancel（验 D-P2-5）。
4. timeout：worker 不及时 ack→走 `hostCancelGrace` force-close 兜底。
5. **queued interactive**（阻断3）：worker 并发=1，长任务占槽，再 dispatch interactive→`waitSession` 等到 session start 后仍成功拨入。
6. pty ws 外部断：断第二条 conn（sess 仍 Running）→worker Cancel 本地 job→failed。
7. worker 掉线：断 hub conn→lostCh→job failed + relay close（两条同断）。
8. browser 断/重连：viewer 掉→worker 无感→重连回放 Scrollback。
9. **单 reader 证明**（事实性错误1）：observer 设时 PtyRunner 无 discard drain，输出零丢；observer 未设时 discard drain 保留（G023 tests）。
10. chatty pty 不饿死 quiet job（专用 ws 隔离）。

## 9. 零回归红线（G023，CI 常驻）
非 interactive 全路径字节不变：local exec / worker exec / worker cancel / pending_interaction bridge / Outcome-before-Result / worker disconnect fail / chatty-quiet hub HOL(`hub_test`) / workflow step / schedule run-now / resume。所有 P2 改动以 `d.Interactive`/`f.Interactive`/`req.Interactive`/`observer!=nil` 门控。
`go list -deps`：`job` 仍不 import `pty`/`ptyrelay`；`internal/worker` 可 import `runner/pty`（已有）；`ptyrelay` 仍 leaf。

## 10. 待确认（plan 内细化，非阻断）
- `hostCancelGrace` 具体值（拟 10s）与 stage/请求超时关系；pending（未 Open）relay 的 `Done(jobID)` 语义（返回未关 chan 靠 grace，还是立即关）。
- `waitSession` 唤醒集合确认：session-start | `jobs.Wait` 终态 | ctx（worker 关停 / hub session 掉）——排队被 cancel 时不悬挂。
- dial 期间 sess 暂不读的 pty buffer 上限实测；是否需本地 staging。
- interactive 路径是否仍走 `streamLocalJob`（仅为 pending_interaction bridge，pty 输出**不**从日志回传）还是拆独立 `pumpInteractions` ticker（中3）。
- 第二条 pty ws 是否加应用层 ping（半开检测）还是靠 job 终态/hub 断连兜底（倾向后者，P2 不加）。

---
> 评审关注点（v0.2）：**D-P2-2 关闭权归 relay EOF + host 等 `relay.Done()`** 是否真的消掉尾字节截断（`Done(jobID)` 对 pending/已 finalized relay 的边界）；**D-P2-3 observer rendezvous** 的无竞态性（OnSessionStart 早于/晚于 waitSession 两序）+ 单 reader 起手 pty buffer；**D-P2-5 selfClosing+state 双判据** 是否还有窗口误判；**D-P2-8 锁外关 IO** 与 `Open/MarkAttached` 并发的可见性。
