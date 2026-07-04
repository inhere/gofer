# WEB-03 P2：worker 端到端 pty attach 设计细化

> 上游：主设计 [`2026-07-03-web-pty-attach-design.md`](2026-07-03-web-pty-attach-design.md) v0.8（§5 架构 / §7 时序 / §14 阶段）；P1 计划 [`../plans/web-pty-attach/P1-plan.md`](../plans/web-pty-attach/P1-plan.md)（§P2 前瞻）；评审 [`../review/2026-07-03-web-pty-attach-codex-review.md`](../review/2026-07-03-web-pty-attach-codex-review.md)（round-2 阻断5=取消协议）+ [`../review/2026-07-04-web-pty-attach-P2-codex-review.md`](../review/2026-07-04-web-pty-attach-P2-codex-review.md)（本文 v0.1/v0.2 两轮评审）。
> 本文只细化 **P2（worker 端到端）**：serve 侧协议/relay/安全闸已 P1 落地，P2 补 **worker 真拨入 + 字节泵 + resize + 取消时序 + 断连语义**，改动面最大。
> 铁律 G023（非交互路径零行为变化）/ G022·G024（`internal/worker` 可 import `runner/pty`，但 `job` 不反向 import pty/ptyrelay）。

## 修订记录
| 版本 | 日期 | 说明 |
|---|---|---|
| v0.1 | 2026-07-04 | 初稿：6 决策 + 5 时序图 + 帧/接口清单。 |
| v0.2 | 2026-07-04 | codex round-1 后大改：输出所有权倒置(`SessionObserver`)、ack 两段化(`relay.Done()`)、事件驱动 rendezvous、断连判据 state+selfClosing、per-dispatch URL、Registry.Close 锁拆分。 |
| **v0.4** | 2026-07-04 | **实施完成后据实证修订**（SUPMODE T0–T7 全绿）：修正 §8 e2e#2「尾字节」措辞——cancel teardown = `ptmx.Close()+SIGKILL`（不可捕获），child 无法在 cancel 时补发哨兵；D-P2-2/6 保护的是 **in-flight 字节不被提前关 relay 截断**（自然退出哨兵走同一 drain 机制验证），非「捕获 SIGKILL 期间临终输出」。分级 kill 留后续。其余决策实现均落地无变。 |
| **v0.3** | 2026-07-04 | **codex round-3 终审 = GO（0 阻断/0 高，可进 P2 实施计划）**。本版据 round-2 收敛（1 阻断+3 高+3 中）：① **`Done(jobID)` 语义定死**（不再列待确认）——**pending=无字节可排=返回已关 chan、host 不等**，只 open/attached 才等 relay drain（D-P2-2），解「pending/dial-fail 空等 10s grace」阻断；② 断连 Cancel 判据放宽到 **starting+running 都 Cancel**（消 observer 早于 `StateRunning` 的窗口，D-P2-5）；③ 新增 **D-P2-9 unmapped cancel pending set**（cancel 早于 `putJobMapping` 不丢，覆盖非交互）；④ D-P2-8 锁外关 IO **覆盖 `Prepare` replacement**（同 `closeLocked`）；⑤ 定死 interactive 仍走 `streamLocalJob`（仅终态检测+interactions bridge，pty 输出**不**过日志）；⑥ observer 注入时序列为实施验收项。 |

## 0. 范围

**做**：worker 收 interactive dispatch → 本地 PtyRunner 起 pty → **worker 成 pty 输出唯一 reader** + **eager 拨出第二条专用 pty ws** → 双向字节泵（输出/输入 binary、resize text）→ **两段化取消/终态协议**（worker teardown ack + serve drain ack）→ 断连全链路。**e2e**（现有 ws-worker harness + `httptest` + Linux 真 pty）。

**不做**（留后阶段）：cast 加密 + `pty_sessions` 表（P3）；前端 `AttachTerminal.vue` + browser `{t:x,code}` exit 帧 + e2e 全矩阵（P4）；serve-local pty（drop-in，V1 不触发）。

## 1. 现状锚点（P1 已落地 + 两轮评审核对，附文件:行）

| 组件 | 位置 | P2 依赖 / 评审要点 |
|---|---|---|
| serve pty ws 端点 | `httpapi/pty_connect_handler.go:54-98` | worker 按 hello=`{job_id,pty_session_id,relay_nonce}`(text) 拨入；校验 nonce 消费 + `callerID==WorkerID` + live instance + `binding.{JobID,PtySessionID}==hello.{...}`(`:70-72`) + job in-flight+interactive；`defer Close(job,"pty_ws_closed")`(`:92`) |
| remotePtySource | `httpapi/pty_source.go` | serve 眼中字节流：`Read`只收 **binary**=输出(`:44` 跳 text)；`Write`发 **binary**=输入；`Resize`发 **text** `{type:resize}`；`Close`(`:80-83`)关 ws |
| relay + 两层背压 + lease | `ptyrelay/relay.go` | `recordLoop`(`:120-138`)读 source→ring+cast+fanout；source EOF/err→`Close()`(`:235-253`)→`Done()`(`:255-256`)。**唯一关闭权**（D-P2-2） |
| relay 注册表 + nonce | `ptyrelay/registry.go`,`nonce.go` | `Prepare`(`:71-94`, 替换旧 entry 时锁内 `closeLocked`)/`Open`(`:98-126`,建 Relay)/`Lookup`/`MarkAttached`/`Close`(`:183-208`,持锁调 `Relay.Close`)；**无 registry-level done**（D-P2-2 新增） |
| browser attach ws | `httpapi/attach_handler.go` | ticket→`MarkAttached`→`AddViewer(lease)`→回放 `Scrollback()`→pump `viewer.Out()`；`relay.Done` 现只 close 4404(`:136-142`) |
| host worker runner | `runner/worker/runner.go` | interactive 已 `LiveInstance`+`Issue`+`Prepare`+Dispatch 带 `RelayNonce`(`:133-201`)；`ctx.Done` 现**立即返回**(`:229-242`)；`resultCh`/`lostCh`(`:310-312`,`:390-407`) |
| PtyRunner + PtySession | `runner/pty/{runner,session}.go` | **`Run` 已 `go io.Copy(io.Discard,sess)`**(`runner.go:53-58`)；`newSession` 初始 `StateStarting`(`session.go:72-80`)，`run` 开头才 `StateRunning`(`:137-139`)，teardown 开始即 `StateExiting`(`:180-183`)，`Done` 末尾才关(`:212-217`)；`run` `<-ps.done` 才返回(`:160-169`) |
| worker Client | `worker/{client,dispatch}.go` | 单 hub conn+writeMu+全 JSON；`recvLoop` dispatch 起 goroutine、cancel inline(`client.go:301-347`)；`handleDispatch` Submit 后才 `putJobMapping`(`dispatch.go:40-44`)，未投影 interactive |
| core / worker 装配 | `core/core.go:78-106`,`commands/worker.go:183-208` | worker/serve 同走 `core.Build`：`Runners["pty"]=*PtyRunner`；worker 命令先 Build→建 Client→`worker.Serve` |
| worker 本地 admission | `job/{submit,execute,cancel}.go` | `Submit` 异步起 `execute`；执行前等**项目/调用方并发槽**（排队可 >秒级）；`Cancel` 只 cancel ctx 不等(`cancel.go:47-63`) |

**P1 已成立**：worker `handleDispatch` 强制 `Runner=local` → submit.go `req.Interactive && !remote` → 命中本机 pty runner；submit.go 已把 `Interactive/Cols/Rows` threaded 到 `runner.Request`→`PtyRunner.start`。

## 2. 关键设计决策

### D-P2-1 worker 第二条专用 pty ws = 独立连接
hub conn 单 conn+单 writeMu+全 JSON；pty 是 binary 高频流。→ interactive job 起时 worker 另开 `websocket.Dial` 到 `/v1/workers/pty-connect`，独立 conn+独立 write 锁+binary 帧，与 hub 隔离。每 job 一条，结束即关。

### D-P2-2 关闭权归 relay recordLoop EOF；ack 两段化 + **`Done(jobID)` 语义定死**（修阻断1）
`Result`(hub ws) 与 pty ws 尾字节在两条独立连接、无跨连接顺序 → host 收 Result 后若立即关 relay 会截尾。
→ **单一关闭权 = relay recordLoop 的 source EOF**：worker 排空后关 pty ws(FIN) → `remotePtySource.Read` err → `recordLoop` 读完全部字节后 `relay.Close()`→`relay.Done()`。这是唯一「serve 已读完」信号。
- `Result` = **worker-teardown ack**（供 exit 分类/Outcome/唤醒条件），**不**兼作 serve-drain ack。
- `relay.Done()` = **serve-drain-complete ack**（host 据此才 finish，D-P2-6）。
- host defer `Close` 退化为**兜底**：仅 grace 超时（open relay 卡死）才 force-close。

**新增 registry-level `Done(jobID) <-chan struct{}`（协议核心，非待确认）**。核心洞察：**pending = 尚无 source、无字节可排 → host 无需等**。语义表：

| 目标 relay 状态 | `Done(jobID)` 返回 | host 行为 |
|---|---|---|
| open / attached（有 live `Relay`+recordLoop） | `e.Relay.Done()`（recordLoop EOF=已排空） | 等它（真 drain），随后 defer Close 幂等 no-op |
| **pending_worker**（无 `Relay`：worker 未拨入/dial 失败/nonce 校验失败） | **已关闭的 chan（包级 sentinel）** | **立即**继续 → force-close pending relay，**不空等 grace** |
| finalized / missing | 已关闭的 chan | 立即继续 |

实现：`Registry.Done` 锁内取 entry：`e==nil || e.State==Finalized || e.Relay==nil → return closedChan`；否则 `return e.Relay.Done()`。→ pending/dial-fail 立即收敛（消阻断1 + 高3 的「10s 空等」）；`hostCancelGrace` 只兜「open relay 但 recordLoop 卡死」。

### D-P2-3 输出所有权倒置：`SessionObserver` 交接 + 事件驱动 rendezvous
真实 `PtyRunner.Run` 注册 session 后 `go io.Copy(io.Discard,sess)`；pty master 非 broadcast，P2 pump 再读=双读吞字节。且 `LookupSession`+固定 2s 轮询会被 worker 本地**并发槽排队**击穿。
→ `PtyRunner` 加可注入 `SessionObserver`：
```go
// internal/runner/pty
type SessionObserver interface {
    // 在 session 注册后、Run 阻塞前【同步】回调；observer 成为 sess 输出的唯一 reader。
    // 必须非阻塞（内部只做投递/起 goroutine）。observer==nil 时 PtyRunner 才跑默认 discard drain。
    OnSessionStart(jobID string, sess *PtySession)
}
func (r *PtyRunner) SetObserver(o SessionObserver) // worker 设；serve/测试 nil→保留 discard(G023 tests)
```
`Run`：`reg.add` 后 `if observer!=nil { observer.OnSessionStart(jobID,sess) } else { go io.Copy(io.Discard,sess) }`。worker Client 实现 `OnSessionStart`（jobID=worker 本地 id），经**事件驱动 rendezvous** 交给 `handleDispatch` 的 pump（无轮询、无竞态）：
```txt
OnSessionStart(localID,sess): 有 waiter→送; 无→buffer sessReady[localID]   // 早到/晚到两序皆安全
waitSession(ctx,localID): 命中 sessReady 立返; 否则挂 waiter, select{ sess | jobs.Wait(localID)终态 | ctx.Done }
```
- **单 reader**：observer 设时无 discard drain，pump 是唯一 reader（正常/cancel 尾/scrollback/cast 全字节可证）。
- **无轮询**：排队多久等多久；job 终态/ctx 唤醒防悬挂。
- **注入时序（验收项，中2）**：worker 命令 `core.Build`→建 `Client`→`ptyRunner.SetObserver(cl)`→`worker.Serve`；**必须在 Serve 前注入**；serve 侧 observer 恒 nil（保留 discard，不回归）。测试证明「首个 dispatch 前 observer 已生效」。
- G024：接口在 `runner/pty` 定义、worker 注入，`job` 不 import pty。`LookupSession` 不再需要。

### D-P2-4 `Dispatch` 补 `PtySessionID` + handleDispatch 投影 + fail-fast
pty_session_id 是 serve mint，worker 不知，但 serve 端点强校验。→ `wsproto.Dispatch` 加 `PtySessionID string`；host `Run` 填（已有 `ptySessionID`）；worker `handleDispatch` 投影 `d.{Interactive,Cols,Rows}`→JobRequest，`d.{JobID,PtySessionID,RelayNonce}`→pump hello。
- **fail-fast**：`d.Interactive && (d.RelayNonce=="" || d.PtySessionID=="")` → 不 Submit 不可 attach 的 pty，回 `Result{failed}`。

### D-P2-5 断连即终止：判据 = **selfClosing 原子标志 + state 排除集**（修阻断4 + 高1 starting 窗口）
observer 在 `reg.add` 后、`sess.run` 置 `StateRunning` 前触发 → 有 `StateStarting` 窗口（pump 已 dial 但 state 仍 starting）。若此窗口 pty ws 外断而判据要求「仅 StateRunning 才 Cancel」，会漏 Cancel、本地 pty 裸跑。
→ 判据放宽：input pump 遇 conn err 时——`if selfClosing || sess.State() ∈ {cancelling, exiting, closed}` → 良性（本端 teardown / 已在拆），不 Cancel；**否则（starting 或 running，且非本端主动关）→ Cancel**（`cl.jobs.Cancel(localID)`）。即 **starting+running 都视为需终止**。selfClosing 由 out pump 因 `sess.Read` EOF 结束时置（=本端 teardown 主动关，不误 Cancel）。

### D-P2-6 host 取消/终态：三路 wait 再 finish（依赖 D-P2-2 的 `Done` 语义）
```txt
ctx.Done(cancel/timeout): hub.Cancel(worker,job)
  select { <-relayRegistry.Done(job) | <-sink.lostCh | <-time.After(hostCancelGrace) }; return ctx.Err()
正常(<-sink.resultCh=worker_result): select { <-relayRegistry.Done(job) | <-time.After(hostCancelGrace) }; 返回
```
- `Done(job)`：open→等真 drain；pending/finalized/missing→已关 chan 立即过（D-P2-2）→ 无未拨入场景的空等。
- `lostCh`：worker 掉线→按 worker_lost 收敛+关 relay。grace 只兜 open-stuck。
- 非 interactive 分支**字节不变**（G023）。

### D-P2-7 per-dispatch 会话 URL（去全局 `currentURL`）
`recvLoop` 把当前 session URL 作**参数**传 `handleDispatch`/`pumpPty`（每 dispatch 固定拨回派发它的 serve）；`net/url` 改 path（`/v1/workers/connect`→`/v1/workers/pty-connect`），非法 fail-fast。

### D-P2-8 serve `Registry`：摘 relay 锁内、关 IO 锁外（**覆盖 Close + Prepare replacement**，修高2+中1）
`Close` 与 `Prepare`(替换旧 entry) 都经 `closeLocked`→锁内 `Relay.Close()`→关 websocket，阻塞会卡全局 `Lookup/Open/MarkAttached`。→ 抽 helper：锁内只做「摘出 relay 引用 + 移索引 + 标 finalized」，**返回待关 relay**；锁外 `relay.Close()`。`Close` 与 `Prepare` replacement 都走此 helper。保持幂等。

### D-P2-9 worker unmapped cancel pending set（修高2 cancel race）
`recvLoop` dispatch 是 goroutine、cancel inline，`putJobMapping` 只在 Submit 返回后 → host 若在 worker 建 mapping 前 cancel，cancel 帧被丢（`localJobID` 返 ""→no-op），本地 job 仍会排队/起 pty/裸跑。
→ worker Client 加 `pendingCancel map[remoteJobID]struct{}`（+mu）：`recvLoop` TypeCancel 未命中 mapping 时记入；`handleDispatch` 在 `putJobMapping` 后**立即消费** `pendingCancel[d.JobID]`，命中则 `jobs.Cancel(localID)` 再进 `waitSession`。**覆盖非交互 dispatch**（既有远端取消竞态一并修）。

## 3. 改动清单（精确到文件）

### 3.1 协议 / runner / relay 接口
| 文件 | 改动 |
|---|---|
| `wsproto/frames.go` | `Dispatch` 加 `PtySessionID string`（D-P2-4） |
| `runner/pty/runner.go` | 加 `SessionObserver`+`SetObserver`；`Run` 按 observer 有无「交接 vs discard」（D-P2-3） |
| `runner/worker/runner.go` | Dispatch 补 `PtySessionID`；`relayPreparer` 接口加 `Done(jobID) <-chan struct{}`；`ctx.Done`+`resultCh` 分支 interactive 三路 wait（D-P2-6）；常量 `hostCancelGrace` |
| `ptyrelay/registry.go` | 加 `Done(jobID)`（D-P2-2 语义表）；抽「锁内摘 relay/锁外关」helper，`Close`+`Prepare` replacement 共用（D-P2-8）；包级 `closedChan` sentinel |

### 3.2 worker 侧（主体）
| 文件 | 改动 |
|---|---|
| `worker/client.go` | rendezvous(`sessReady`/`sessWaiters`+mu)+`OnSessionStart`/`waitSession`（D-P2-3）；`pendingCancel`+mu（D-P2-9）；`recvLoop` 透传 session URL（D-P2-7）+ 未命中 cancel 记 pending |
| `worker/dispatch.go` | `handleDispatch` 收 sessionURL 参；投影 interactive+fail-fast（D-P2-4）；`putJobMapping` 后消费 pendingCancel（D-P2-9）；interactive `waitSession`→`go pumpPty`；**发 Result 前 join pumpDone**；interactive **仍走 `streamLocalJob`**（仅终态检测+interactions bridge，pty 输出不过日志，中3 定案） |
| `worker/pty_pump.go`（新） | 拨出+hello+双向泵+resize+selfClosing/断连判据（§5、D-P2-5）；`derivePtyConnectURL`（net/url，D-P2-7） |
| `commands/worker.go` | 断言 `cr.Runners[ptyrunner.Name].(*PtyRunner)`→`SetObserver(cl)`（Serve 前）；nil-safe |

### 3.3 serve 侧（小）
| 文件 | 改动 |
|---|---|
| `httpapi/attach_handler.go` | P2 仅需现行 `relay.Done`→close；`{t:x,code}` exit 帧 = **P4**（避 select 竞态） |

## 4. 关键流程时序

### 4.1 正常：attach → 交互 → agent 自然退出
```txt
browser        serve(host-runner + relay)        hub        worker(Client+PtyRunner)          agent
                dispatch(+nonce+ptySID) ───────────────▶ handleDispatch: 投影+fail-fast; Submit; putJobMapping
                                                          消费 pendingCancel(无); (排队/起 pty) Run: reg.add
                                                          →observer.OnSessionStart(localID,sess) [唯一 reader]
                                                          waitSession 命中→go pumpPty
                pty-connect ◀════════════════════════════ Dial(bearer); hello{job,ptySID,nonce}
                consume nonce+校验 ✔; Open(nonce,remoteSource)→relay open; recordLoop 读
  {t:i} ─▶ SendInput ─▶ src.Write(binary) ═════════════▶ conn.Read binary ─▶ sess.WriteInput ─▶ stdin
  ◀binary◀ viewer.Out ◀ fanout ◀ recordLoop ◀ src.Read ◀═ conn.Write binary ◀ sess.Read(pump) ◀ stdout
  {t:r} ─▶ relay.Resize ─▶ src.Resize(text) ═══════════▶ conn.Read text{resize} ─▶ sess.Resize
              agent 退出→PtySession 自然 teardown(StateExiting→closeMaster→wait→publishExit→done)
              out pump sess.Read=EOF→selfClosing=1→排空→conn.Close(FIN)
              recordLoop 收 EOF→relay.Close→relay.Done()          streamLocalJob 检测终态; Wait; join pumpDone
              host: <-resultCh → <-Done(job)(已闭) → 返回 → finish; defer Close=no-op   → 发 Result(done)
  ◀ ws close ◀ attach: relay.Done→close
```

### 4.2 host cancel / timeout（**取消协议核心**）
```txt
browser        serve host-runner + relay          hub        worker                          agent
            ctx.Done()
            hub.Cancel(worker,job) ─────────────────────▶ recvLoop TypeCancel→jobs.Cancel(localID)
            ★三路 wait(不立即返回):                            (若 cancel 早于 mapping→pendingCancel, 见 4.6)
              select{ <-Done(job) | <-lostCh | <-After(grace) } job ctx 取消→PtyRunner ctx.Done→PtySession
                                                                cancelling→teardown→publishExit→done
                                                                out pump EOF→selfClosing=1→排空→conn.Close(FIN)
            recordLoop 收 EOF→relay.Close(尾字节全入)→Done()      Wait; join pumpDone
            <-Done(job) 命中 → 返回 ctx.Err()→finish            (input pump 见 selfClosing→不误 Cancel)
            defer Close=幂等 no-op                                → 发 Result(cancelled)
```
关闭权=worker FIN→recordLoop EOF；host 等 `relay.Done()`；仅 grace 超时才 force-close。

### 4.3 pty ws 外部断连（serve 崩/网络断，job 仍在跑）→ 断连即终止
```txt
serve                          worker pumpPty                          agent
 (serve 重启/idle-kill / dial 失败 / nonce 校验失败)
 pty ws 断/拒 ─────────────▶ in/out pump conn err
                            判据: !selfClosing && state∉{cancelling,exiting,closed}(含 starting) ⇒ 外部断
                            → cl.jobs.Cancel(localID) ─────────────▶ teardown→退出→Result(failed/cancelled)
 (relay pending 未 Open: host Done(job)=已关 chan→立即收敛; 已 Open: recordLoop EOF→Close→Done)
```

### 4.4 worker↔hub 断连（P1 既有，P2 验证）
```txt
host: OnDisconnect→lostCh; interactive 三路 wait 命中 lostCh→worker_lost 收敛+关 relay→job failed
worker 整体掉线: 第二条 pty ws 同断→serve recordLoop EOF→relay.Close(幂等)
```

### 4.5 browser 断连（会话存活，K3；P2 验证 worker 不受影响）
```txt
browser 断→attach ws 关→viewer.Close→relay/pty/worker job 不受影响；重连(新 ticket)→AddViewer→回放 Scrollback
```

### 4.6 cancel 早于 mapping（D-P2-9）
```txt
host cancel ─── hub.Cancel ──▶ recvLoop TypeCancel: localJobID("")→未命中→pendingCancel[remoteID]=1
worker handleDispatch: Submit→putJobMapping→消费 pendingCancel 命中→jobs.Cancel(localID)→teardown（不裸跑/不悬挂）
```

## 5. worker 拨出连接管理（`pty_pump.go`）
```txt
pumpPty(ctx, sessionURL, sess, remoteJobID, ptySessionID, nonce) (pumpDone chan):
 1. url := derivePtyConnectURL(sessionURL)   // net/url: path→/v1/workers/pty-connect; 非法 fail-fast
    conn := websocket.Dial(ctx, url, bearer=cl.token)   // worker 可设 Authorization header
    dial 失败 → cl.jobs.Cancel(localID); close(pumpDone); return   // 断连即终止; host Done(pending)=已关→不空等
 2. wsjson.Write(conn, hello{job_id:remoteJobID, pty_session_id:ptySessionID, relay_nonce:nonce})
 3. out(唯一 reader): for{ n,err:=sess.Read(buf); if n>0 conn.Write(binary); if err{ selfClosing=1; break } }
                      结束→conn.Close(FIN)   // 触发 serve recordLoop EOF
    in : for{ typ,data,err:=conn.Read(); if err break;
              if binary: sess.WriteInput(data);  if text: {type:resize}→sess.Resize(clamp 1..500/1..200) }
 4. 收敛: 等 out 结束; 若 in 先因 conn err 结束 且 !selfClosing && sess.State()∉{cancelling,exiting,closed}:
          cl.jobs.Cancel(localID)   // D-P2-5(含 starting)
    close(pumpDone)   // handleDispatch join 后才发 Result
```
- 起手 pty buffer：eager 下 serve relay pending、端点 up，dial ~1 RTT；交互 agent 启动不 flood，dial 期间 sess 暂不读可接受（plan 内若需可本地 staging）。
- 写并发：pty ws 独立 write 锁；out 是唯一 binary writer，hello 起 goroutine 前串行写。

## 6. 三个超时的关系
| 超时 | 位置 | 值 | 作用 |
|---|---|---|---|
| `PtySession.defaultGrace` | `session.go:45` | 5s | teardown 内等被 kill 子进程 reap |
| `hostCancelGrace`（新） | `runner/worker.go` | ~10s | **仅兜 open relay 卡死**（等 `relay.Done()` 上限）；pending/dial-fail 由 `Done`=已关 chan 立即收敛，不占此时间 |
| `waitSession` | `worker/client.go` | 事件驱动 | 等 session start，靠 job 终态/ctx 唤醒，非超时 |
> `hostCancelGrace ≥ defaultGrace + RTT + serve drain`（拟 10s）；**stage/请求超时须 > `hostCancelGrace`**（否则上层超时抢先打断 drain 证明，SR510/SR705 对齐，plan 落定）。

## 7. 安全（P2 增量）
- pty ws 拨出用 worker token（bearer header）；serve `callerID==binding.WorkerID`+nonce 绑 worker/instance → 只能拨自己被派发的 job。
- 复用 P1 五闸/nonce 原子消费/instance 校验，不放宽。
- fail-fast（D-P2-4）+ 断连即终止（D-P2-5）+ unmapped cancel（D-P2-9）防裸跑/脱管。

## 8. e2e 测试矩阵（P2 子集，现有 harness + Linux 真 pty）
两条真 `websocket.Dial`（hub + pty-connect）+ `httptest.Server`：
1. 正常：dispatch→observer 交接→pty ws→输入 echo→输出回传 viewer→resize 生效。
2. **尾字节证明**：用**自然退出**的 child（进程终态前输出 sentinel 再退出）验证 relay drain——断言 browser/relay ring 收 sentinel **后** host job 才 finish（验 D-P2-2 关闭权 + D-P2-6 等 Done）。**注（实现实证 v0.4）**：cancel 路径 teardown = `ptmx.Close()+SIGKILL`（不可捕获，见 `pty_unix.go` `unixPty.Close`），child **无法**在被 cancel 时补发哨兵；故此测试用自然退出走**同一** relay drain 机制。D-P2-2/6 保护的是「worker 已从 pty 读出、经 pty ws 传输中/已传的 **in-flight 字节**不被 host 提前关 relay 截断」，**非**「捕获 child 在 SIGKILL 期间的临终输出」。若需 cancel 时 child 优雅收尾输出，须分级 kill（SIGHUP 宽限→SIGKILL），留后续（设计主档 v0.8 finding③）。
3. cancel：host cancel→worker teardown→relay 读完→Done→host cancelled；input pump 不误 Cancel（验 D-P2-5）。
4. timeout：open relay 不 EOF→走 `hostCancelGrace` force-close 兜底。
5. **`Done(jobID)` 四边界**（阻断1）：① worker 从未拨入（relay 恒 pending）→host 立即收敛；② dial 失败→worker Cancel 本地 job + host 不空等 grace；③ nonce 消费后校验失败（instance 不匹配）→端点拒+worker Cancel；④ relay 已先 finalized 后 host 再 `Done`→已关 chan。
6. **queued interactive**：worker 并发=1，长任务占槽，再 dispatch interactive→`waitSession` 等到 session start 后仍成功拨入。
7. **starting 窗口断连**（高1）：observer 已起 pump、`sess.run` 未置 running 时断 pty ws→仍 Cancel 本地 job（钩子在 observer 后、run running 前注入断开）。
8. **unmapped cancel**（高2/D-P2-9）：host cancel 早于 `putJobMapping`→pendingCancel 命中→本地 job 被 Cancel，不裸跑（覆盖非交互）。
9. pty ws 外部断：断第二条 conn（sess running）→worker Cancel→failed。
10. worker 掉线：断 hub conn→lostCh→job failed + relay close。
11. browser 断/重连：viewer 掉→worker 无感→重连回放 Scrollback。
12. **单 reader 证明**：observer 设时 PtyRunner 无 discard drain、输出零丢；未设时 discard 保留（G023 tests）。
13. chatty pty 不饿死 quiet job（专用 ws 隔离）。

## 9. 零回归红线（G023，CI 常驻）
非 interactive 全路径字节不变：local exec / worker exec / worker cancel / pending_interaction bridge / Outcome-before-Result / worker disconnect fail / chatty-quiet hub HOL(`hub_test`) / workflow step / schedule run-now / resume。所有 P2 改动以 `d.Interactive`/`f.Interactive`/`req.Interactive`/`observer!=nil` 门控（D-P2-9 的 pendingCancel 覆盖非交互但语义是「补发被丢的 cancel」，不改正常路径字节）。
`go list -deps`：`job` 仍不 import `pty`/`ptyrelay`；`internal/worker` 可 import `runner/pty`（已有）；`ptyrelay` 仍 leaf。

## 10. 待确认（plan 内细化，非阻断）
- dial 期间 sess 暂不读的 pty buffer 上限实测；是否需本地 staging。
- 第二条 pty ws 是否加应用层 ping（半开检测）还是靠 job 终态/hub 断连兜底（倾向后者，P2 不加）。
- `hostCancelGrace` 具体值（拟 10s）与 stage/request timeout 的偏序在 plan 内定常量。
- `pendingCancel` 的 TTL/清理（dispatch 从未到达时残留）——拟随 mapping 生命周期或短 TTL sweep。

---
> 评审关注点（v0.3）：**D-P2-2 `Done(jobID)` 语义表**是否覆盖全部边界（尤其 pending→已关 chan 立即收敛、与 `Prepare` replacement 时旧 entry 的 done 一致性）；**D-P2-5 判据放宽到 starting** 是否引入新的「本端 teardown 早期被误判外部断」窗口；**D-P2-9 pendingCancel** 与 `dropJobMapping`/dispatch 生命周期的清理；**D-P2-8 helper** 锁外关 IO 与并发 `Open` 的可见性。
