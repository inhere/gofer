# WEB-03 P2：worker 端到端 pty attach 设计细化

> 上游：主设计 [`2026-07-03-web-pty-attach-design.md`](2026-07-03-web-pty-attach-design.md) v0.8（§5 架构 / §7 时序 / §14 阶段）；P1 计划 [`../plans/web-pty-attach/P1-plan.md`](../plans/web-pty-attach/P1-plan.md)（§P2 前瞻）；评审 [`../review/2026-07-03-web-pty-attach-codex-review.md`](../review/2026-07-03-web-pty-attach-codex-review.md)（round-2 阻断5 = 取消协议）。
> 本文只细化 **P2（worker 端到端）**：serve 侧协议/relay/安全闸已 P1 落地，P2 补 **worker 真拨入 + 字节泵 + resize + 取消时序 + 断连语义**，改动面最大。
> 铁律 G023（非交互路径零行为变化）/ G022·G024（`internal/worker` 可 import `runner/pty`，但 `job` 不反向 import pty/ptyrelay；worker Client 经窄接口拿 `PtySession`）。

## 修订记录
| 版本 | 日期 | 说明 |
|---|---|---|
| v0.1 | 2026-07-04 | 初稿：6 决策 + 5 时序图 + 帧/接口改动清单，全部对真实代码核对。待 codex 评审。 |

## 0. 范围

**做**：worker 收 interactive dispatch → 本地 PtyRunner 起 pty（P1 已可）→ **eager 拨出第二条专用 pty ws** 到 serve → 双向字节泵（输出 binary / 输入 binary / resize text）→ **host 取消协议**（等 worker ack/grace 再 finish）→ 断连全链路。**e2e**（用现有 ws-worker 测试 harness + `httptest`）。

**不做**（留后阶段）：cast 加密录制 + `pty_sessions` 表（P3）；前端 `AttachTerminal.vue` + e2e 全矩阵（P4）；serve-local pty（drop-in，V1 不触发）。

## 1. 现状锚点（P1 已落地，P2 依赖）

| 组件 | 位置 | P2 依赖点 |
|---|---|---|
| serve pty ws 端点 | `httpapi/pty_connect_handler.go` | worker 按其 hello/nonce 契约拨入；校验 nonce+live instance+`callerID==WorkerID`+job in-flight+interactive+`binding.{JobID,PtySessionID}==hello.{...}` |
| remotePtySource | `httpapi/pty_source.go` | serve 眼中的字节流：`Read`只收 **binary**=pty 输出；`Write`发 **binary**=pty 输入；`Resize`发 **text** `{type:resize,cols,rows}`；`Close`发 normal-close |
| relay + 两层背压 + lease | `ptyrelay/relay.go` | recordLoop 读 source→ring+cast+viewer fan-out；source EOF/err→`Close()`（幂等 CAS）→`Done()` |
| relay 注册表 + nonce | `ptyrelay/registry.go`,`nonce.go` | host runner 侧 `Prepare(pending_worker)`/`Issue(nonce)`；端点 `Consume(nonce)`+`Open(nonce,source)` |
| browser attach ws | `httpapi/attach_handler.go` | 消费 ticket→`MarkAttached`→`AddViewer(lease)`→回放 `Scrollback()`→pump `viewer.Out()`→binary；读 `{t:i}`/`{t:r}` |
| host worker runner | `runner/worker/runner.go` | interactive 时已 `LiveInstance`+`Issue` nonce+`Prepare` relay+Dispatch 带 `RelayNonce`；**ctx.Done 立即返回**（P2 要改） |
| PtyRunner + PtySession | `runner/pty/{runner,session}.go` | worker 本地起 pty；`registry.Lookup(jobID)`；`PtySession.{Read,WriteInput,Resize,Done,ExitCode}` + 有序 teardown |
| worker Client | `worker/client.go`,`dispatch.go` | 单 hub conn + writeMu + 全 JSON；`handleDispatch` 未投影 interactive 字段（P2 补） |
| core 装配 | `core/core.go`,`commands/worker.go` | worker 与 serve 同走 `core.Build`：`Runners["pty"]=*PtyRunner`（Available 时）；Client 由 `commands/worker.go:192` 建，拿得到 `cr.Runners` |

**P1 已成立的调度事实**（无需 P2 改）：worker `handleDispatch` 强制 `Runner=local` → submit.go `req.Interactive && !remote` → 命中本机 pty runner；submit.go 已把 `Interactive/Cols/Rows` threaded 到 `runner.Request`→`PtyRunner.start`。**唯一断点**：`handleDispatch` 构造 `JobRequest` 时没带 `Interactive/Cols/Rows`（下方 D-P2-4 修）。

## 2. 关键设计决策

### D-P2-1 worker 第二条专用 pty ws = 独立连接（非复用 hub conn）
worker Client 现为**单 conn + 单 writeMu + 全 `wsjson`（JSON）**（`client.go:95-96`）。pty 字节流是 binary + 高频，塞进 hub conn 会破坏 hub 的顺序/背压（codex round-1 阻断3 的原因）。→ interactive job 起时，worker **另开一条 `websocket.Dial` 到 serve 的 `/v1/workers/pty-connect`**，自带独立 `*websocket.Conn` + 独立 write 锁，**binary 帧**收发，与 hub 控制平面完全隔离。每个 interactive job 一条，job 结束即关。

### D-P2-2 pty-exit ack = 复用 hub `Result` 帧（**不新增 wsproto 帧**）
codex round-2 阻断5 要求：host cancel → 等 worker「pty-exit **或 result**」再 finish。关键事实链（已核对）：worker 本地 interactive job 的终态**必然晚于** `PtySession` 有序 teardown——`handleDispatch` 在 `cl.jobs.Wait(localID)` 返回终态后才发 `Result`；而 `Wait` 返回 ⇐ `PtyRunner.Run` 返回 ⇐ `PtySession.run` 走完 `<-ps.done`（publish-exit）。**故现有 `Result` 帧就是合法的 pty-exit ack**，无需新增帧。
- 再加一条 worker 侧强序（D-P2-6）：`handleDispatch` 对 interactive job **先 join pty 泵 goroutine（输出排空 + pty ws 关闭）再发 Result** → `Result` 语义升级为「pty 全排空 + 已 teardown」。
- 决策留给 codex：**是否接受"复用 Result 而非新帧"**。倾向接受（少一个协议面 + 与 round-2 建议一致）。

### D-P2-3 worker-client → PtySession 取字节 seam
Client 只持 `jobs Jobs`（`client.go:93`），拿不到 `PtySession`。→ `PtyRunner` 加导出方法 `LookupSession(jobID)(*PtySession,bool)`（转发内部 `registry.Lookup`）；`commands/worker.go` 从 `cr.Runners[ptyrunner.Name].(*ptyrunner.PtyRunner)` 取出，经 worker `Config` 注入 Client 一个窄接口 `ptySessions interface{ LookupSession(string)(*ptyrunner.PtySession,bool) }`。`internal/worker` 已 import `ptyrunner`（`Available()`），不违 G024（job 仍不 import pty）。
- **注册时机竞态**：`PtySession` 在 `PtyRunner.Run`（job 执行 goroutine）内 `reg.add`，`handleDispatch` 的 `Submit` 是异步返回、此刻 session 可能未注册。→ worker pty 泵起手**有界轮询** `LookupSession(localID)`（如 20ms×N，上限 2s）；超时未见即放弃 pty ws（job 仍按普通流程跑/失败，relay 端 `pending_worker` 到期由 registry sweep）。

### D-P2-4 `wsproto.Dispatch` 增补 `PtySessionID` + `handleDispatch` 投影
serve 端点强校验 `binding.PtySessionID == hello.PtySessionID`，但 pty_session_id 是 **serve 侧 mint**（`runner/worker/runner.go:141 newPtySessionID`），worker 不知道。→ `Dispatch` 加 `PtySessionID string json:"pty_session_id,omitempty"`；host `runner/worker.Run` 填入（已有 `ptySessionID` 变量）；worker `handleDispatch` 把 `d.{Interactive,Cols,Rows}` 投影进本地 `JobRequest`，把 `d.{JobID,PtySessionID,RelayNonce}` 传给 pty 泵作 hello。

### D-P2-5 pty ws 断连即会话终止
主设计"生命周期"：`worker↔serve 断连即会话终止 + job failed`。pty ws（第二条）**非 teardown 引起**的断开（serve 崩/网络断/idle killed）时，worker 泵检测到 conn err 且 `PtySession` 仍 running → 调 `cl.jobs.Cancel(localID)` 终止本地 job（→ 走正常 teardown → Result=failed/cancelled）。反向：teardown 引起的 EOF（master 关）是正常关闭，不触发 cancel。用「谁先动」区分：`PtySession.Done()` 已关 = teardown 主动；否则 = 外部断连。

### D-P2-6 host 取消协议：interactive 等 ack/grace 再 finish
`runner/worker.Run` 的 `ctx.Done()` 分支现**发 hub.Cancel 后立即 return**（`runner.go:229-242`），host job 随即 finish + defer 关 relay → 早于 worker teardown/flush（round-2 阻断5）。→ **仅 interactive**：发 `hub.Cancel` 后 `select{ case <-sink.resultCh: /*ack*/; case <-time.After(hostCancelGrace): /*超时兜底*/ }` 再 return `ctx.Err()`（保持 cancel/timeout 分类）。sink 的 `DeregisterSink` 是 defer、wait 期间仍在册 → 能收到 worker 的 Result；relay 的 `Close` 也是 defer、退化为幂等 backstop（真正关闭由 worker pty ws FIN→recordLoop EOF 触发，见 §3.2）。非 interactive 分支**字节不变**（G023）。

## 3. 改动清单（精确到文件）

### 3.1 协议 / 接口
| 文件 | 改动 |
|---|---|
| `internal/wsproto/frames.go` | `Dispatch` 加 `PtySessionID string json:"pty_session_id,omitempty"`（D-P2-4） |
| `internal/runner/pty/runner.go` | `PtyRunner` 加导出 `LookupSession(jobID)(*PtySession,bool)` = `r.reg.Lookup`（D-P2-3） |
| `internal/runner/worker/runner.go` | Dispatch 构造补 `PtySessionID: ptySessionID`；`ctx.Done()` 分支 interactive 加等 ack/grace（D-P2-6）；新增常量 `hostCancelGrace`（略大于 `PtySession.defaultGrace`，见 §6） |

### 3.2 worker 侧（改动主体）
| 文件 | 改动 |
|---|---|
| `internal/worker/client.go` | `Config`+`Client` 加 `ptySessions` 窄接口与当前 hub 会话 URL（`currentURL`，`runSession` 记录）；`New` 存字段 |
| `internal/worker/dispatch.go` | `handleDispatch` 投影 `d.{Interactive,Cols,Rows}`→JobRequest；interactive 时 `go cl.pumpPty(ctx, localID, d.JobID, d.PtySessionID, d.RelayNonce)` 拿 done chan；**发 Result 前 join done chan**（D-P2-6 强序） |
| `internal/worker/pty_pump.go`（新） | 拨出 + hello + 双向泵 + resize + 断连处理（§5 详述） |
| `internal/commands/worker.go` | 从 `cr.Runners[ptyrunner.Name]` 断言取 `*PtyRunner`，注入 `worker.Config.PtySessions`；nil-safe（无 pty backend 时不注入，interactive 在 admission 早被拒） |

### 3.3 serve 侧（小）
| 文件 | 改动 |
|---|---|
| `httpapi/attach_handler.go` | relay `Done()` 后有界轮询 `s.jobs.Get(jobID)` 至终态，向 browser 发 `{t:x,code}`（exit code 复用 job 终态，不动 relay leaf）；超时发 `{t:x,code:-1}` |
| `httpapi/pty_source.go` | （可选）`remotePtySource.Read` 遇 text control 时按需忽略（现已 `continue` 跳过 text，天然兼容；无 exit-control 依赖，见 §4.2 说明） |

> **relay 不加 exit 语义**：exit code 走 job 终态真源（attach handler 侧查），保持 `ptyrelay` 为 stdlib leaf（G022）。

## 4. 关键流程时序

### 4.1 正常：attach → 交互 → agent 自然退出
```txt
browser        serve(relay/attach)         hub        worker(Client+PtyRunner)        agent
                dispatch(+nonce+ptySID) ───────────▶ handleDispatch: Submit(interactive)
                                                      PtyRunner.Run→pty.Start (session=running)
                                                      go pumpPty: LookupSession(轮询命中)
  (P1: attach-ticket + attach ws 已建, viewer 等 relay open)
                pty-connect ◀════════════════════════ Dial /v1/workers/pty-connect (bearer)
                consume nonce+校验 live instance ✔     hello{job_id,pty_session_id,nonce}
                Open(nonce,remoteSource)→relay open
                recordLoop 读 source
  键入{t:i} ─▶ viewer.SendInput ─▶ src.Write(binary) ═════▶ conn.Read binary ─▶ sess.WriteInput ─▶ stdin
  ◀ binary ◀ viewer.Out ◀ fanout ◀ recordLoop ◀ src.Read(binary) ◀═ conn.Write binary ◀ sess.Read ◀ stdout
  {t:r} ─▶ relay.Resize ─▶ src.Resize(text ctrl) ═════════▶ conn.Read text{resize} ─▶ sess.Resize
              agent 退出 → PtySession 自然 teardown: 停input→closeMaster→wait→publishExit→done
              worker 输出泵 sess.Read=EOF → 排空 → 关 pty ws(FIN)
              recordLoop 收 EOF → relay.Close → relay.Done         handleDispatch: Wait 终态
  ◀{t:x,code}◀ attach: relay.Done→查 job 终态 code               (join pump done) → 发 Result(done)
                                       Result ─────────▶(不是这条, 是 w→s)  host runner: <-resultCh → 正常返回
```

### 4.2 host cancel / timeout（**取消协议核心**）
```txt
browser       serve host-runner + relay        hub        worker                         agent
 (交互中, relay attached)
            ctx.Done()(cancel/timeout)
            hub.Cancel(worker,job) ───────────────────▶ recvLoop TypeCancel → jobs.Cancel(localID)
            ★interactive: 不立即返回, 等 ack:                job ctx 取消 → PtyRunner ctx.Done
              select{<-resultCh; <-After(grace)}            PtySession: cancelling→有序 teardown
                                                            停input→closeMaster(+kill)→wait(bounded)→publishExit→done
                                                            输出泵 sess.Read=EOF→排空→关 pty ws(FIN)
            recordLoop 收 EOF→relay.Close(全字节已入 ring/cast)
  ◀{t:x}◀── relay.Done→attach 查 job 终态                     handleDispatch: Wait 终态
            <-resultCh(=pty-exit ack) ◀───── Result ◀───────  (join pump done)→发 Result(cancelled)
            host runner 返回 ctx.Err()→job finish
            defer relay.Close = 幂等 no-op(已由 EOF 关)
```
**要点**：① relay 真正关闭由 **worker pty ws FIN**（recordLoop EOF）驱动，保证 ring/cast 收全字节后才关 → host 的 defer `Close` 是 backstop；② host 只在 **grace 超时**（worker 卡死/掉线）才让 defer force-close（此时截断可接受）；③ 无新帧——Result 兼作 ack。

### 4.3 pty ws 断连（worker↔serve 第二通道断，job 仍在跑）→ 断连即终止
```txt
serve                         worker pumpPty                         agent
  (serve 崩/网络断/idle-kill)
  pty ws 断 ────────────────▶ conn.Read/Write err
                              PtySession.Done() 未关 ⇒ 外部断连(非 teardown)
                              → cl.jobs.Cancel(localID)   ─────────▶ 正常 teardown → 退出
                              → Result(failed/cancelled) 经 hub 回 host
  (relay 端: remoteSource 已随 conn 断 → recordLoop EOF → relay.Close)
```

### 4.4 worker↔hub 断连（P1 既有机制，P2 只验证）
```txt
host runner boundedSink.OnDisconnect→lostCh→Run 返回 worker-lost→job failed→defer relay.Close
worker 进程若整体掉线: 第二条 pty ws 同时断→serve recordLoop EOF→relay.Close(幂等)
```

### 4.5 browser 断连（会话存活，K3；P2 只验证 worker 不受影响）
```txt
browser 断→attach ws 关→viewer.Close(defer)→relay/pty/worker job 全不受影响(继续跑)
重连(新 ticket)→AddViewer→回放 Scrollback→继续。worker 侧无感。
```

## 5. worker 拨出连接管理（`pty_pump.go` 细节）

```txt
pumpPty(ctx, localID, remoteJobID, ptySessionID, nonce) (done chan):
 1. sess := 有界轮询 ptySessions.LookupSession(localID)  // D-P2-3 竞态, 上限~2s
    若未命中 → close(done); return  // 不拨 pty ws, relay pending_worker 由 registry 到期清
 2. url := derivePtyConnectURL(cl.currentURL)  // hub 会话 URL 的 /connect → /pty-connect, 同 host
    conn := websocket.Dial(ctx, url, bearer=cl.token)   // worker 可设 Authorization header
    若 dial 失败 → cl.jobs.Cancel(localID)(断连即终止); close(done); return
 3. wsjson.Write(conn, hello{job_id:remoteJobID, pty_session_id:ptySessionID, relay_nonce:nonce})
    // serve 校验失败会以 close-code 关 conn → 下面 read/write err → 走断连分支
 4. 两 goroutine:
    out: for{ n,err:=sess.Read(buf); if n>0 conn.Write(binary,buf[:n]); if err break }  // EOF=teardown
    in:  for{ typ,data,err:=conn.Read(); if err break;
              if binary: sess.WriteInput(data);
              if text: 解析{type:resize,cols,rows}→sess.Resize(clamp) }  // 其他 text 忽略
 5. 收敛: 等 out 结束(pty EOF=排空完毕) → conn.Close(normal)  // 主动关, 触发 serve recordLoop EOF
    若 in 先因 conn err 结束 且 !sess.Done(): cl.jobs.Cancel(localID)  // D-P2-5 断连即终止
    close(done)  // handleDispatch join 此 chan 后才发 Result(D-P2-6)
```

- **URL 派生**：Client 在 `runSession(ctx,url)` 存 `cl.currentURL=url`；pty ws 用同一 serve（同 host+scheme），仅换 path。多 hub 地址失效切换期间新 interactive job 极少，按当前会话 URL 即可。
- **写并发**：pty ws 自带独立 write 锁（与 hub writeMu 无关）；out goroutine 是唯一 binary writer，hello 在起 goroutine 前串行写，无竞争。
- **resize clamp**：worker 侧再夹一次 cols 1..500/rows 1..200（serve attach 已夹一次，纵深防御）。

## 6. 两个 grace 的关系
| grace | 位置 | 值 | 作用 |
|---|---|---|---|
| `PtySession.defaultGrace` | worker `session.go:45` | 5s | teardown 内等被 kill 的子进程被 reap 的上限 |
| `hostCancelGrace`（新） | host `runner/worker.go` | ~8s（> 5s + 网络往返裕量） | host 等 worker `Result` ack 的上限；超时才 force-finish |
> 约束 `hostCancelGrace > PtySession.defaultGrace + 单程 RTT 裕量`，保证正常情况下 worker 先 teardown 完发 Result、host 才不会误走超时兜底。plan 内定常量（拟 8s），并让 stage/请求超时 ≥ 该值。

## 7. 安全（P2 增量）
- pty ws 拨出用 **worker token**（bearer header，worker 非浏览器，可设 header）；serve 端点已 `callerID==binding.WorkerID` 校验 → worker token 只能拨自己被派发的 job（nonce 绑 worker_id+instance_id）。
- 复用 P1 五闸/nonce 原子消费/instance 校验，P2 不放宽。
- browser exit 走 job 终态查（真源），不引入 relay→browser 的新可信面。
- 断连即终止（D-P2-5）防「pty ws 断了但 agent 还在 worker 上裸跑无人管」。

## 8. e2e 测试矩阵（P2 子集，用现有 harness）
用 `hub_test` 式内存 harness + `httptest.Server` + 真 `websocket.Dial`（两条：hub + pty-connect）+ Linux 真 pty（容器）：
1. 正常：dispatch→pty ws 建立→输入 echo→输出回传 browser viewer→resize 生效。
2. cancel：host cancel→worker teardown→Result ack→job cancelled→relay 收全尾字节后 close→browser `{t:x}`。
3. timeout：同 2，但走 `hostCancelGrace` 兜底路径（模拟 worker 不 ack）。
4. pty ws 断连：断第二条 conn→worker Cancel 本地 job→job failed（断连即终止）。
5. worker 掉线：断 hub conn→worker-lost→job failed + relay close（两条同时断）。
6. browser 断/重连：viewer 掉→worker 无感→重连回放 Scrollback。
7. session 注册竞态：Submit 后 session 未即注册→有界轮询命中→拨入成功。
8. chatty pty 不饿死 quiet job（专用 ws 隔离验证：pty 高频不影响 hub 其他 job 的 result/cancel）。

## 9. 零回归红线（G023，CI 常驻）
非 interactive 全路径字节不变：local exec / worker exec / worker cancel / pending_interaction bridge / Outcome-before-Result / worker disconnect fail / chatty-quiet hub HOL(`hub_test`) / workflow step / schedule run-now / resume。所有 P2 改动以 `d.Interactive` / `f.Interactive` / `req.Interactive` 门控，普通 dispatch 走原 `handleDispatch`/原 `ctx.Done` 分支。
`go list -deps`：`job` 仍不 import `pty`/`ptyrelay`；`internal/worker` 可 import `runner/pty`（已有）；`ptyrelay` 仍 leaf。

## 10. 待确认（plan 内细化，非阻断）
- `hostCancelGrace` 具体值（拟 8s）与 stage/请求超时的关系确认。
- pty ws dial 失败/hello 被拒时，除 Cancel 本地 job 外是否需要给 host 一个更明确的失败原因（现走通用 failed）。
- session 注册轮询上限（拟 2s）与 PtyRunner 起 pty 的实际耗时对齐（真机测）。
- browser exit code 的有界轮询窗口（拟 2s）——relay.Done 与 job 终态的时间差实测。
- 是否给 pty ws 加应用层 ping（第二条 conn 的半开检测），还是靠 job 终态/hub 断连兜底（倾向后者，P2 不加）。

---
> 评审关注点（请 codex 重点核对）：**D-P2-2 复用 Result 作 ack 是否成立**（worker 终态严格晚于 teardown 的推理链）；**§4.2 relay 关闭权归 worker FIN、host 仅 backstop** 的 CAS 幂等是否有竞态；**D-P2-5 断连即终止的 teardown/外部断连区分**（靠 `PtySession.Done()` 是否可靠）；**§6 两 grace 的偏序约束**。
