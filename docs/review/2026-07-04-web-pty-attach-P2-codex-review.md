# WEB-03 P2 worker 端到端 pty attach 设计细化 - codex 对抗式评审

> 审核方：codex（读真实代码交叉核对，未改源码）。  
> 被审：`docs/design/2026-07-04-web-pty-attach-P2-design.md` v0.1。  
> 自证：`go build ./...` / `go vet ./...` 使用 workspace 内 `GOCACHE/GOMODCACHE` 通过；默认 cache 路径因沙箱无权写 `C:\Users\KZL\AppData\Local\go-build` 与 `D:\env\gopath\pkg\mod\cache` 失败。

## 总评

**不能直接进入 P2 实施计划。**方向是对的：专用 pty ws、worker 本地 `PtyRunner`、`Result` 复用为控制面终态信号、host cancel 等待 worker ack，这些都比新增 hub 字节帧更贴近现有架构。但 v0.1 仍有 4 个会直接导致尾字节截断、pty 输出丢失、拨入漏建或错误取消分类的硬问题。

关键结论：**`Result` 可以作为 "worker 本地 pty session 已 teardown" 的 ack，但不能按当前设计直接作为 "serve relay 已读完 pty ws 尾字节并关闭" 的 ack。**两条 WebSocket 无跨连接顺序保证，host 收到 Result 后的 defer `relayRegistry.Close` 可能先于 serve 的 `recordLoop` 读完 pty ws。这个点不补，round-2 的取消协议 blocker 只是从 "host 先 finish" 变成 "host 收到 result 后仍可能截尾"。

## 事实性错误

1. **设计把 worker pump 当成 `PtySession` 输出的唯一 reader，但真实 `PtyRunner.Run` 已经启动了一个 discard reader。**  
   `internal/runner/pty/runner.go:56-58` 在 session 注册后 `go io.Copy(io.Discard, sess)`，P2 的 `pumpPty` 若再 `sess.Read`，两个 goroutine 会竞争同一个 pty master。结果不是复制输出，而是随机分流：部分字节会被 discard 吞掉，serve relay/cast/browser 永远看不到。  
   影响：D-P2-2 的 "输出排空 + pty ws 关闭后再 Result" 不成立；P2 计划必须先把 `PtyRunner` 的临时 drain 改成可注入/可禁用的单 reader 所有权，不能只新增 worker pump。

2. **`Dispatch` 当前确实没有 `PtySessionID`，且 host 构造帧也没填；serve pty-connect 端点已经强校验 hello 的 pty_session_id。**  
   `internal/wsproto/frames.go:37-50` 只有 `RelayNonce`，无 `PtySessionID`；`internal/runner/worker/runner.go:188-201` Dispatch 也只填 `RelayNonce`；但 `internal/httpapi/pty_connect_handler.go:70-72` 要求 `binding.PtySessionID == hello.PtySessionID`。  
   设计 D-P2-4 已识别这个缺口，结论正确；实施计划里这必须是协议链第一步，否则 worker 端没有可提交的 session id。

## 阻断

1. **`Result` 作 ack 的跨连接强序不成立，host 仍可能截断 pty ws 尾字节。**  
   worker 侧即使做到 "out goroutine 写完 binary -> 关闭 pty ws -> handleDispatch 发送 Result"，serve 侧接收仍发生在两条独立连接上：pty ws 的最后 binary/FIN 和 hub ws 的 Result 没有全局顺序。host runner 当前在收到 Result 后立即返回，并由 defer 关闭 relay：`internal/runner/worker/runner.go:160-164`、`:210-220`。relay close 会一路关闭 source/websocket：`internal/ptyrelay/registry.go:196-203`、`internal/ptyrelay/relay.go:247-251`、`internal/httpapi/pty_source.go:80-83`。而真正读取尾字节的是并发的 `recordLoop`：`internal/ptyrelay/relay.go:120-138`。  
   反例：hub Result 先到，`Run` 返回触发 `relayRegistry.Close`，`remotePtySource.Close` 关闭 pty ws；此时 `recordLoop` 可能还没从 `conn.Read` 取出最后一个 binary frame 或 EOF，尾字节被截断。  
   修正要求：P2 不能只等 `sink.resultCh`。host interactive 分支收到 Result 后还要等待 `relay.Done()`（或 registry 暴露的 relay finalized）短 grace；或者新增 worker/serve pty 通道上的应用层 "relay-drained ack" 再让 host finish。仅复用 Result 可以保留为 "worker pty teardown ack"，但不能声称它也是 "serve relay read-complete ack"。

2. **`PtyRunner` 的 discard drain 会直接吞掉 P2 输出，必须先改输出所有权。**  
   `PtyRunner.Run` 注册 session 后立即启动 `io.Copy(io.Discard, sess)`：`internal/runner/pty/runner.go:53-58`。P2 设计的 out pump 也要调用 `sess.Read`：设计 §5 第 4 步。pty master 不是 broadcast stream，两个 reader 是竞争关系。  
   影响：正常输出、cancel 尾输出、pre-attach scrollback、cast 都可能缺字节；worker 侧 "排空" 也无法证明，因为另一个 reader 已经消费过数据。  
   修正要求：计划中新增一个明确任务：`PtyRunner` 不再生产默认 discard reader，或通过窄接口把单一 output consumer 注入 session；在没有 P2 pump 的 P0/P1 测试场景再用测试/兼容 drain。这个改动虽在 `pty` runner 内，但只影响 interactive 路径，符合 G023。

3. **`LookupSession` 2s 有界轮询会被 worker 本地队列击穿，导致 pty ws 漏拨。**  
   `Submit` 返回时只是启动了异步 `execute` goroutine：`internal/job/submit.go:312-320`。真正执行前还会等项目/调用方并发槽：`internal/job/execute.go:32-40`、`:49-57`。`PtySession` 只在 `PtyRunner.Run` 里 `start` 成功后注册：`internal/runner/pty/runner.go:48-54`。  
   反例：worker 本地 `MaxConcurrentJobs=1`，一个长任务占槽，interactive dispatch 先 `Submit` 成功但排队超过 2s。P2 pump 超时放弃 pty ws，relay pending 到期；几秒后 job 真启动 pty，却没有专用 ws，输出被 drain/卡住，host/browser 永远 attach 不上。  
   修正要求：不要用固定 2s 轮询作为协议语义。应提供 `PtyRunner.WaitSession(ctx, jobID)` 或 job/service/session registry 的 start notification，等待到本地 job terminal/ctx cancel/host cancel/nonce expiry，而不是短超时；或把 pump 启动移动到 `PtyRunner.Run` 拿到 session 的同一所有权点。

4. **用 `PtySession.Done()` 是否关闭来区分 teardown vs 外部断连有 TOCTOU，会把自然退出误分类为 cancelled。**  
   `Done` 在 teardown 最后才关闭：`internal/runner/pty/session.go:212-217`。但自然退出路径在 `childExited` 后仍会进入 teardown、先 close master、wait/publish，再 close done：`internal/runner/pty/session.go:150-158`、`:180-218`。如果这段窗口里 pty ws 因正常 FIN/网络抖动让 pump 的读侧报错，P2 设计会看到 `Done()` 尚未关闭并调用 `cl.jobs.Cancel(localID)`。`Cancel` 会 cancel ctx：`internal/job/cancel.go:47-62`；而 `PtySession.run` 最终优先返回 `ctx.Err()`：`internal/runner/pty/session.go:160-168`，自然完成可能被 host 记录成 cancelled。  
   修正要求：不能用 "Done 已关" 作为唯一判据。至少要区分 `StateExiting/StateClosed`（teardown 已开始）与 `StateRunning`，或让 output pump 的主动关闭路径设置原子原因，input pump 只在 session 仍 `StateRunning` 且非本端主动 close 时 Cancel。

## 高

1. **attach 侧 `{t:x,code}` 的插入点不可靠，当前 select 可能被 `pumpDone` 抢走。**  
   当前 `handleJobAttach` 在 `relay.Done()` 分支直接 close 4404：`internal/httpapi/attach_handler.go:136-139`。P2 §3.3 提议 "relay.Done 后查 job 终态发 `{t:x,code}`"，方向对，但当前结构里 `Relay.Close` 会先关闭 viewer.Out：`internal/ptyrelay/relay.go:247-251`，pump goroutine 随即 close `pumpDone`：`internal/httpapi/attach_handler.go:120-128`。`select` 同时看到 `relay.Done` 和 `pumpDone` 时可能选 `pumpDone`，函数直接返回，浏览器只看到 normal close，没有 exit frame。  
   修正要求：attach handler 需要把 "relay ended -> resolve job terminal -> 写 exit frame -> close" 做成统一收尾路径，而不是只在 `relay.Done()` case 里做。可在任一结束 case 后检查 `relay.Done()` 是否已关，或单独由 relay done goroutine 串行写 `{t:x}`，并用 `writeMu` 保护。

2. **`Registry.Close` 持 registry 锁执行 `Relay.Close`/websocket close，可能放大成全局 relay HOL。**  
   `Registry.Close` 持 `r.mu` 后调用 `closeLocked`：`internal/ptyrelay/registry.go:183-194`；`closeLocked` 在锁内调用 `e.Relay.Close()`：`:196-203`；`Relay.Close` 又会调用 `src.Close()`：`internal/ptyrelay/relay.go:232-252`，remote source 关闭 websocket：`internal/httpapi/pty_source.go:80-83`。如果底层 close 因网络/库锁/并发 Read 阻塞，整个 registry 的 `Lookup/Open/MarkAttached/Prepare` 都被卡住。  
   修正要求：P2 计划应把 registry 状态翻转和外部 IO 分离：锁内摘出 relay、移除索引、标 finalized；锁外 close source/viewers。否则 cancel storm 或异常 pty ws 会拖住其它会话 attach/open。

3. **`currentURL` 作为 mutable Client 字段会在多 hub failover 下拨错 serve，也容易引入 data race。**  
   worker reconnect loop 每次取 `url := cl.urls[idx]` 并调用 `runSession(ctx,url)`：`internal/worker/client.go:203-216`、`:240-245`；dispatch goroutine由 recvLoop 启动：`:310-315`。P2 §5 计划在 `runSession` 存 `cl.currentURL`，pump 再读它派生 `/pty-connect`。如果 hub 连接断开并切到下一个 URL，而旧 dispatch 的本地 job/pump 还在启动或排队，global `currentURL` 可能已经变成新 hub；nonce/relay 却绑定旧 serve 的 live instance。  
   修正要求：不要用全局 mutable URL。recvLoop 应把当前 session URL 作为参数传给 `handleDispatch`/`pumpPty`，让每个 dispatch 固定拨回派发它的那条 hub session 对应 serve。并用 `net/url` 改 path，不用字符串替换。

4. **host cancel 等待只写了 `resultCh/grace`，但遗漏 `lostCh` 与 relay close 的组合状态。**  
   sink 目前有独立 `resultCh` 和 `lostCh`：`internal/runner/worker/runner.go:310-312`；hub disconnect 会调用 `OnDisconnect`：`internal/wshub/hub.go:349-361`。P2 D-P2-6 只说 cancel 后等 `sink.resultCh` 或 `time.After(hostCancelGrace)`。如果 cancel 后 hub 断了但 pty ws 尚未及时 EOF，host 会等满 grace；如果 Result 到了但 relay 未 Done，又触发阻断 1 的截尾。  
   修正要求：interactive cancel wait 应显式建模三路：`resultCh`、`lostCh`、`relay.Done()`。Result 后等 relay；lost 后按 worker_lost/ctx 分类策略收敛并关闭 relay；grace 超时才 force close。

## 中

1. **`PtySessionID` 补字段后还要补滚动升级/坏帧策略。**  
   旧 worker 或未完成投影的 `handleDispatch` 会拿不到 `PtySessionID`。serve pty-connect 已校验 session id：`internal/httpapi/pty_connect_handler.go:70-72`。计划应要求 worker 在 `Interactive=true` 但 `RelayNonce/PtySessionID` 任一为空时直接 fail 本地 dispatch 并回 Result，而不是启动普通 pty 或等待 pending relay 到期。

2. **`derivePtyConnectURL` 需要按 URL path 明确规则，不要只写 `/connect -> /pty-connect`。**  
   文档和示例主路径是 `/v1/workers/connect`：`config/worker.example.yaml:19-20`，但 worker config 的 URL 列表是外部输入，当前 `wsDialURLs` 只是 copy，不标准化路径：`internal/commands/worker.go:270-278`。计划应写清：只接受/规范化 hub connect URL，pty path 固定为同 scheme/host 的 `/v1/workers/pty-connect`；非法 path fail fast。

3. **P2 要明确 interactive 不再 tail stdout/stderr 作为主输出，但 interactions 轮询是否保留。**  
   当前 worker dispatch 总是 `streamLocalJob` 再 `Wait`：`internal/worker/dispatch.go:46-52`；而 pty runner 明确不写 stdout.log：`internal/runner/pty/runner.go:44-48`。P2 应写清 interactive 路径是否仍调用 `streamLocalJob` 仅用于 pending_interaction bridge，还是拆成 `pumpInteractions` ticker，避免后续实现者误以为 pty output 还会从日志回传。

4. **测试矩阵缺一个 "queued interactive" 场景。**  
   设计 §8 有 "session 注册竞态"，但没有覆盖本地并发槽排队超过 lookup 上限的情况。应新增：worker 项目并发=1，先占槽，再 dispatch interactive，确认 pty ws 在真正 session start 后仍能拨入。

## 重点质疑清单核销

- **D-P2-2 复用 Result 作 ack：部分成立，但不能按 v0.1 进入 plan。**  
  成立部分：worker 本地 job 的 `Result` 确实晚于 `PtySession.run` 返回；`run` 等 `<-ps.done` 后才返回：`internal/runner/pty/session.go:160-169`，`PtyRunner.Run` 随后返回：`internal/runner/pty/runner.go:60-61`，`handleDispatch` 在 `jobs.Wait` 后才发 Result：`internal/worker/dispatch.go:51-72`。  
  不成立部分：这只能证明 worker 本地 teardown，不证明 serve relay 已读完 pty ws；见阻断 1。另有 discard reader 竞争，见阻断 2。

- **§6 双 grace 偏序：`hostCancelGrace > defaultGrace + RTT` 只覆盖 worker teardown，不覆盖 serve relay drain。**  
  `defaultGrace=5s` 在 `internal/runner/pty/session.go:43-45`。拟 8s 对正常 child reap 大概率够，但 host grace 还必须包括 "Result 后等待 relay.Done" 的短窗口，或者独立 relay drain grace。否则即使 worker 5s 内完成，host 仍可能在 relay 未读完时关闭 source。

- **断连即终止（D-P2-5）：目标正确，判据不可靠。**  
  "pty ws 外部断连 -> Cancel local job" 是必要的，防裸跑；但 `Done()` 未关不等于还在正常 running。自然退出 teardown 窗口会误触发 cancel，见阻断 4。需要状态/原因级判据。

- **G023 零回归：可做到，但计划必须更硬地标门控。**  
  当前非交互路径没有被 P1 改坏：`handleDispatch` 仍未投影 interactive 字段：`internal/worker/dispatch.go:24-32`，host `ctx.Done` 仍立即返回：`internal/runner/worker/runner.go:229-242`。P2 改动必须保持：host cancel 等待、pty pump、session lookup、relay close wait 全部只在 `f.Interactive/d.Interactive/req.Interactive` 下启用；`PtyRunner` 输出所有权改动只影响 interactive runner。

## 遗漏项

- **需要一个 serve relay read-complete 信号。** 可以是 host 等 `relay.Done()`，也可以是 pty ws 上 serve 发 "drained/closed" 控制帧给 worker，再由 worker 发 Result；但必须明确跨连接顺序来源。
- **需要 session start notification，而不是固定短轮询。** 这同时解决排队、慢 pty start、以及 worker 本地 admission 迟滞。
- **需要定义 pty pump 的单 reader 所有权。** `PtyRunner`、worker Client、测试 drain 三者不能同时读 `PtySession`。
- **需要明确 pty ws close reason/state。** 至少区分 worker 主动 close、serve relay close、external conn err、本地 teardown started，供 D-P2-5 判定。
- **需要覆盖旧 worker/坏 dispatch 的失败语义。** `interactive=true` 但缺 `relay_nonce/pty_session_id` 应 fail fast，避免启动不可 attach 的 pty。
- **需要 P2 e2e 增加 tail-byte 证明。** 测试里让 child 在收到 cancel 后输出 sentinel，再退出；断言 browser/relay ring 收到 sentinel 后 host job 才 finish。

# 第二轮：v0.2 复审

> 审核方：codex（读真实代码交叉核对，未改源码）。
> 被审：`docs/design/2026-07-04-web-pty-attach-P2-design.md` v0.2。
> 自证：`go build ./...` / `go vet ./...` 使用 workspace 内 `.cache/go-build`、`.cache/gomod` 通过。
> 工具限制：本轮会话未暴露 codebase-memory-mcp 的 `search_graph` 等图工具，已按项目规则退回定点 `rg`/文件核对。

## 事实性冲突（优先）

1. **`Done(jobID)` 的 pending/finalized 语义不能列为 "待确认/非阻断"。**
   v0.2 D-P2-6 已把 host 终态收敛建立在 `relayPreparer.Done(jobID)` 上：cancel 分支等 `Done|lostCh|grace`，Result 分支等 `Done|grace`（设计:81-90）；但 §10 又把 "pending（未 Open）relay 的 `Done(jobID)` 语义" 列为待确认（设计:235-236）。真实 registry 目前没有 `Done(jobID)`，pending entry 也没有 `Relay`：`internal/ptyrelay/registry.go:81-85`；`Relay.Done()` 只在 `Open` 后才存在：`internal/ptyrelay/registry.go:121-125`、`internal/ptyrelay/relay.go:255-256`。这是协议核心，不是 plan 内可延后的细节。

2. **`SessionObserver` 同步回调点早于 `PtySession.run` 把状态置为 running。**
   v0.2 要求 `Run` 在 `reg.add` 后同步 `OnSessionStart`，observer 内部再起 pump（设计:54-66）。真实 `PtyRunner.Run` 当前也是 `reg.add` 后才 `sess.run(ctx)`：`internal/runner/pty/runner.go:53-60`；而 `StateRunning` 在 `PtySession.run` 开头才设置：`internal/runner/pty/session.go:137-139`。因此实现若严格按 "只有 `StateRunning` 且非 selfClosing 才 Cancel"（设计:77-80、187-195），存在 observer 已启动 pump 但 session 仍是 `StateStarting` 的短窗口。这个窗口遇到 pty ws 外部断连时可能不 Cancel 本地 job。

## 上轮 findings 核销表

| 上轮 finding | 判定 | 核销依据 |
|---|---|---|
| 事实性错误1 / 阻断2：`io.Copy(io.Discard,sess)` 双读 | **已解决（设计层）** | v0.2 改成 `SessionObserver` 输出所有权倒置：observer 设定时不跑默认 discard，worker pump 成唯一 reader（设计:49-71、104-115）。真实风险锚点仍在现代码 `internal/runner/pty/runner.go:53-58`，P2 任务必须先改这里。同步回调本身不会阻塞 `sess.run` 的前提是 `OnSessionStart` 内部只做非阻塞投递/起 goroutine；v0.2 已写 "必须非阻塞"（设计:54-59）。 |
| 事实性错误2 / 中1：`Dispatch` 缺 `PtySessionID` | **已解决（设计层）** | v0.2 明确 `wsproto.Dispatch` 加 `PtySessionID`，host 填 `ptySessionID`，worker hello 透传，并对缺 nonce/session id fail-fast（设计:73-75、104、107、114）。真实代码当前仍缺字段：`internal/wsproto/frames.go:37-50`；host 当前只填 `RelayNonce`：`internal/runner/worker/runner.go:188-201`；serve 已强校验 hello：`internal/httpapi/pty_connect_handler.go:70-72`。设计链路已闭合。 |
| 阻断1：`Result` 跨连接不证明 serve 读完尾字节 | **部分解决** | v0.2 的 "Result=worker teardown ack，`relay.Done()`=serve drain ack" 方向正确（设计:41-47），并要求 host 在 Result/cancel 后等 Done（设计:81-90）。但 `Done(jobID)` 对 pending / 已 finalized / missing entry 的返回语义未定（设计:235-236），会让 worker 从未拨入、dial 失败、nonce 消费后校验失败等路径只能空等满 `hostCancelGrace`，或实现者随意返回导致截尾。 |
| 阻断3：2s 轮询被并发槽击穿 | **部分解决** | v0.2 的 rendezvous 设计覆盖 OnSessionStart 早到/晚到两序：`sessReady` buffer + waiter 集合，`waitSession` 等 session-start / `jobs.Wait` 终态 / ctx（设计:61-70）。这解决固定 2s 轮询问题，也把 queued interactive 测试补入矩阵（设计:224）。仍未覆盖 cancel frame 早于 jobMap 建立的竞态：当前 dispatch 异步起 goroutine `go cl.handleDispatch(ctx,d)`，cancel inline 只在 `localJobID` 已存在时生效：`internal/worker/client.go:310-324`；mapping 只有 Submit 返回后才写：`internal/worker/dispatch.go:40-44`。v0.2 未要求 "unmapped cancel pending set"，所以 "排队中被 cancel 不悬挂/不裸跑" 仍不完整。 |
| 阻断4：用 `Done()` 判据 TOCTOU 误判 cancel | **部分解决** | v0.2 改为 selfClosing + `sess.State()`，不再只看 `Done()`（设计:77-80）。这修掉自然退出 teardown 窗口的主要误判，因为真实状态在 teardown 开始即变 `StateExiting`：`internal/runner/pty/session.go:180-183`，`Done` 则到最后才关：`internal/runner/pty/session.go:212-217`。剩余缺口是上面的 `StateStarting` observer 窗口：`StateRunning` 晚于 observer 回调。 |
| 高2：`Registry.Close` 持锁关 IO 造成 HOL | **部分解决** | v0.2 要求 Close 锁内摘索引/标 finalized、锁外 `relay.Close()`（设计:96-97、108），能解决当前 `Close` 持锁调用 `Relay.Close` 的问题：`internal/ptyrelay/registry.go:183-203`。但同类 HOL 还存在于 `Prepare` 替换旧 entry：`Prepare` 在锁内 `closeLocked(old,"replaced")`：`internal/ptyrelay/registry.go:75-80`。P2 计划应把 "摘出待关 relay、锁外 close" 做成 registry 通用 helper，覆盖 Close 和 Prepare replacement。 |
| 高3：全局 `currentURL` failover 拨错 | **已解决（设计层）** | v0.2 改为 recvLoop 把当前 session URL 作为参数传给 `handleDispatch` / `pumpPty`，每个 dispatch 固定拨回派发它的 hub session 所属 serve，并用 `net/url` 改 path（设计:93-94、113-115）。这与真实多 URL reconnect 形态匹配：`internal/worker/client.go:203-216`、`internal/worker/client.go:240-245`、`internal/worker/client.go:310-315`。 |
| 高4：cancel wait 漏 `lostCh` / relay 组合 | **部分解决** | v0.2 明确三路 wait：`relay.Done()` / `lostCh` / grace，并且 Result 后也等 Done（设计:81-90）。这与真实 `boundedSink` 的两个独立唤醒源匹配：`internal/runner/worker/runner.go:310-312`、`internal/runner/worker/runner.go:390-407`。剩余问题仍是 `Done(jobID)` 语义未定；如果 pending 只能等 grace，高4 的 "lost/relay 组合" 仍会在未拨入场景拖慢。 |
| 高1：attach `{t:x,code}` select 竞态 | **已解决（范围收敛可接受）** | v0.2 把 browser exit frame 移到 P4，P2 只保留 `relay.Done` 后关闭（设计:11、121-123）。这避免在 P2 引入上一轮指出的 select 竞态。P2 不提供 exit code 会留下 browser 端 "知道断了但不知道退出码" 的观测缺口，但这是当前行为延续：`internal/httpapi/attach_handler.go:136-142`，可接受为 P4 范围。 |
| 中2：URL 规范化 fail-fast | **已解决（设计层）** | v0.2 明确 `derivePtyConnectURL` 使用 `net/url`，同 scheme/host 固定 path `/v1/workers/pty-connect`，非法 path fail-fast（设计:93-94、187）。 |
| 中3：interactive 是否仍 `streamLocalJob` | **未解决** | v0.2 §10 仍把 "interactive 路径是否仍走 `streamLocalJob` 仅为 pending_interaction bridge，还是拆 `pumpInteractions` ticker" 列为待确认（设计:239）。真实代码当前 `handleDispatch` 总是 `streamLocalJob` 后 `Wait`：`internal/worker/dispatch.go:46-52`；pty runner 不写 stdout.log：`internal/runner/pty/runner.go:44-48`。这个选择会影响 P2 的 goroutine join 链和测试断言，应在 v0.3 写定。 |
| 中4：queued interactive 测试 | **已解决（设计层）** | v0.2 测试矩阵新增 worker 并发=1、长任务占槽、interactive 排队后仍成功拨入（设计:224）。 |

## v0.2 新问题

### 阻断

1. **`relayPreparer.Done(jobID)` 必须在设计里给出完整语义，否则 P2 取消/终态协议仍不可实现。**
   当前 `Registry` 只有 `Prepare/Open/Lookup/MarkAttached/Close`，没有 registry-level done：`internal/ptyrelay/registry.go:50-57`、`internal/ptyrelay/registry.go:181-208`。如果 `Done(jobID)` 对 pending relay 返回 relay.Done，则没有 relay；如果返回永不关闭的 chan，host 在 worker dial 失败/从未拨入时只能等满 10s；如果对 finalized/missing 返回 nil chan，host 可能永久阻塞。最小修改：v0.3 明确定义并实现 registry-level done 语义，例如 `Prepare` 创建 entry-level done；`Open` 后 relay EOF/`Registry.Close` 都最终 close entry done；`Done(jobID)` 对 finalized/missing 返回已关闭 chan；对 pending（尚无 source、无字节可 drain）在 host 已决定收敛时不能强迫等待满 grace，至少要定义 "pending 无可排字节，Done 可立即闭合并由后续 Close 使 nonce/open 失败" 的行为。对应 e2e 必须覆盖 worker 从未拨入、dial 失败、nonce 消费后校验失败、relay 已先 finalized 后 host 再 Done 这四类。

### 高

1. **`StateStarting` 窗口会削弱 "pty ws 外部断连即终止"。**
   v0.2 的 observer 在 `Run` 注册 session 后、`sess.run` 前触发（设计:54-66），真实状态也是 `newSession` 初始 starting：`internal/runner/pty/session.go:72-80`，到 `run` 才置 running：`internal/runner/pty/session.go:137-139`。若 pump 已 dial 成功但连接马上异常，按 "只有 `StateRunning` 才 Cancel" 的规则可能跳过 cancel，留下本地 pty 继续跑。最小修改二选一：把 `StateRunning` 迁移提前到 observer 触发前；或把断连 Cancel 判据改为 "非 selfClosing 且 state 不在 cancelling/exiting/closed 即 cancel"，即 starting/running 都视为需要终止。补一个人工断开 pty ws 于 observer 后、run running 前的单测/集成钩子。

2. **unmapped cancel race 仍会让排队中的 interactive job 脱离 host cancel。**
   真实 recvLoop 里 dispatch 是 goroutine，cancel 是 inline 控制帧：`internal/worker/client.go:310-324`；现有 `handleDispatch` 只有 Submit 返回后才 `putJobMapping`：`internal/worker/dispatch.go:40-44`。v0.2 虽然让 `waitSession` 等 job terminal/ctx，但没有处理 cancel frame 早于 mapping 的情况。host 取消后如果 worker 尚未完成 Submit/mapping，cancel 被丢弃；之后本地 job 仍可能排队、启动、observer 拨入失败或裸跑，host 侧则按 cancelled/grace 收敛并 deregister sink。最小修改：worker Client 增加 `pendingCancel[remoteJobID]`，cancel 未命中 mapping 时记录；`putJobMapping` 后立即消费并 `jobs.Cancel(localID)`，再进入 `waitSession`。这个修复最好同时覆盖非 interactive worker dispatch，避免远端取消竞态继续存在。

3. **`hostCancelGrace` 不能承担 pending/dial-fail 的正常收敛路径。**
   v0.2 拟 `hostCancelGrace≈10s`，约束覆盖 `PtySession.defaultGrace(5s)+RTT+serve drain`（设计:208-210），这作为异常兜底合理；但如果 pending relay 的 Done 语义不闭合，它会变成 worker 未拨入、dial 失败、旧 worker fail-fast 等常见失败路径的正常等待时间。这样 cancel 响应会被固定拉长到 10s，且与 stage/request timeout 的关系变脆。最小修改：先按阻断1 定义 pending/missing/finalized Done，使 10s 只用于 "worker/relay 卡死"；再在计划里明确 stage/request timeout 必须大于 `hostCancelGrace`，否则上层超时会抢先打断 drain 证明。

### 中

1. **D-P2-8 的锁外关 IO 范围需要覆盖 `Prepare` replacement，不只是 `Close`。**
   当前 `Prepare` 替换同 job 旧 relay 时仍在 registry 锁内 close：`internal/ptyrelay/registry.go:75-80`。v0.2 只点名 `Registry.Close`（设计:96-97、108）。虽然 replacement 不是高频主路径，但它与 Close 使用同一个 `closeLocked`，实现者如果只改 `Close`，HOL 仍残留在重派/替换路径。

2. **observer 注入时序需要写成实施验收项。**
   当前 worker 命令先 `core.Build`，再创建 `Client`，再 `worker.Serve`：`internal/commands/worker.go:183-208`；`core.Build` 同时用于 serve/worker，并在 pty 可用时注册 `PtyRunner`：`internal/core/core.go:78-85`。v0.2 在文件清单里说 `commands/worker.go` 对 `cr.Runners["pty"]` 断言后 `SetObserver(cl)`（设计:116），方向可行；但 P2 plan 应写硬：必须在 `worker.Serve` 前注入，serve 侧 observer 恒 nil 以保留 discard drain，且用测试证明 worker 第一条 dispatch 前 observer 已生效。

3. **interactive 的 `streamLocalJob` 策略仍未落定。**
   如果继续调用 `streamLocalJob`，它对 pty output 没有主输出意义，只负责 interactions 轮询；如果拆成 `pumpInteractions`，`handleDispatch` 的 join 链更清楚。该选择目前仍在 §10 待确认（设计:239），但它影响 "发 Result 前 join pumpDone" 的实现位置和测试矩阵。

## join 链核对

v0.2 建议的主链无环，前提是 `OnSessionStart` 非阻塞且 `pumpPty` 不等待 `jobs.Wait`：

```txt
handleDispatch
  -> Submit -> putJobMapping -> waitSession
  -> start pumpPty goroutine
  -> streamLocalJob / jobs.Wait
       -> execute waits runner.Run
       -> PtyRunner.Run waits sess.run
       -> sess.run teardown closes master, then closes ps.done
  -> join pumpDone
       -> pumpDone waits sess.Read EOF / conn close; it does not block sess.run
  -> send Outcome/Result
```

取消路径也无固有环：host cancel frame 调 `jobs.Cancel`，`Cancel` 只 cancel ctx 不等待：`internal/job/cancel.go:47-63`；`sess.run` 收 ctx 后 teardown；out pump 读 EOF 后关 pty ws；serve `recordLoop` EOF 后 `relay.Done`。真正需要补的是 unmapped cancel race 和 `Done(jobID)` pending 语义。

## 收敛判定

**需改到 v0.3，暂不能直接进入 P2 实施计划。**

剩余数量：**阻断 1，高 3，中 3**。

最小必要修改清单：

1. 在设计中定死并测试 `relayPreparer.Done(jobID)` 对 open、pending、finalized、missing 的返回语义；pending/dial-fail 不能靠 10s grace 正常收敛。
2. 修正 `SessionObserver` 与 `PtySession.StateRunning` 的时序/判据：starting 断连也必须 Cancel，或先置 running 再 observer。
3. worker Client 增加 unmapped cancel pending 机制，保证 cancel 早于 `putJobMapping` 时不丢。
4. D-P2-8 锁外关 IO 覆盖 `Prepare` replacement。
5. 写定 interactive 是否保留 `streamLocalJob`，若保留仅作为 interactions bridge；pty output 不从 stdout/stderr log 回传。
6. 把 observer 注入时序（Client 创建后、`worker.Serve` 前；serve nil 保留 discard）列入实施任务和测试。

# 第三轮：v0.3 终审

> 审核方：codex（读真实代码交叉核对，未改源码）。
> 被审：`docs/design/2026-07-04-web-pty-attach-P2-design.md` v0.3。
> 自证：`go build ./...` / `go vet ./...` 使用 workspace 内 `.cache/go-build`、`.cache/gomod` 通过。
> 工具限制：本轮会话未暴露 codebase-memory-mcp 的 `search_graph` 等图工具，已按项目规则退回定点文件核对。

## 事实性冲突（优先）

未发现会推翻 v0.3 的事实性冲突。v0.3 引用的现状锚点与真实代码一致：`RelayState` 当前含 `pending_worker/open/attached/closing/finalized`（`internal/ptyrelay/registry.go:12-18`），`PtyRunner.Run` 当前仍有默认 discard drain（`internal/runner/pty/runner.go:53-58`），`PtySession` 的 `starting→running→exiting→closed` 时序与设计描述一致（`internal/runner/pty/session.go:72-80`、`:137-139`、`:180-183`、`:212-217`），worker cancel 当前确实可能早于 mapping 丢失（`internal/worker/client.go:301-347`、`internal/worker/dispatch.go:40-44`）。这些都是 P2 计划要改的现状，不是 v0.3 的新增矛盾。

## 六项核销表

| round-2 必改项 | 判定 | 核销依据 |
|---|---|---|
| 1. `Done(jobID)` 语义定死 | **已解决** | v0.3 把 `Done(jobID)` 从待确认提升为协议核心，定义 `open/attached` 返回 `e.Relay.Done()`，`pending_worker/finalized/missing` 返回包级已关 `closedChan`，并给出实现判据 `e==nil || e.State==Finalized || e.Relay==nil → closedChan`（设计:49-57）。`RelayClosing` 没在表格中单列，但按实现判据落在两种安全结果之一：若 helper 已按 D-P2-8 锁内摘索引/标 finalized，则外部 `Done(jobID)` 只能看到 missing/finalized 并立即返回 closed；若实现保留短暂 `closing` 且 `Relay!=nil`，则返回 live relay 的 `Done()`，等同 open/attached drain 档。pending 返回 closed 后与 worker 恰好 `Open` 的竞态也可收敛：`Open` 与 `Close` 均受 registry 锁串行化（现状锁点 `internal/ptyrelay/registry.go:98-126`、`:183-208`）；要么 host 先移除 nonce 使 `Open` 失败，要么 worker 先 Open，随后 host Close 关闭 relay/ws，worker 侧断连判据再 Cancel，本地 job 不会裸跑。`Prepare` replacement 的旧 entry done 一致性由 D-P2-8 的“锁内摘旧 relay/移索引/标 finalized，锁外 close”覆盖（设计:102-103、117）；按 jobID 查询不会永久等旧 entry。 |
| 2. starting 窗口断连也 Cancel | **已解决** | v0.3 明确 observer 早于 `StateRunning` 的窗口存在，并把断连判据改为 `!selfClosing && state∉{cancelling,exiting,closed}` 即 starting/running 都 Cancel（设计:85-87、202-207）。这与真实状态迁移吻合：starting 在 `newSession` 建立（`internal/runner/pty/session.go:72-80`），running 到 `run` 开头才设置（`:137-139`），teardown 一开始就进入 exiting（`:180-183`），done 最后才 close（`:212-217`）。反向误判方面，只要“本端 teardown 已开始”，state 已经是 cancelling 或 exiting，因此 input pump 即使早于 out pump 设置 `selfClosing` 看到 conn err，也不会误发 Cancel。`job.Service.Cancel` 对 terminal 是 no-op、对 live job 只 cancel ctx 不等待（`internal/job/cancel.go:47-63`），多发 Cancel 的运行时后果受控；v0.3 还把 cancel 路径测试列入矩阵（设计:230、234）。 |
| 3. unmapped cancel pending set | **已解决** | v0.3 新增 D-P2-9：`recvLoop` cancel 未命中 mapping 时写入 `pendingCancel[remoteID]`，`handleDispatch` 在 `putJobMapping` 后立即消费并 `jobs.Cancel(localID)`，且覆盖非交互 dispatch（设计:105-107、122-123、189-192、235）。这正对真实竞态：dispatch goroutine 与 inline cancel 之间存在先后差（`internal/worker/client.go:301-347`），mapping 现状只在 Submit 后写入（`internal/worker/dispatch.go:40-44`）。二次竞态也闭合：消费后 mapping 仍保留到 `dropJobMapping`，后续 cancel 会走正常 `localJobID→Cancel`；消费前 cancel 由同一 `pendingCancel` 锁保护。Submit 失败未建立 mapping 时的 stale pending 清理，v0.3 已在 §10 作为 plan 内 TTL/生命周期清理项列出（设计:250）；这会造成小型状态清理任务，但不影响取消协议可实现性，也不是阻断/高风险。 |
| 4. 锁外关 IO 覆盖 Prepare replacement | **已解决** | v0.3 明确 D-P2-8 覆盖 `Close` 与 `Prepare` replacement，抽 helper 在锁内只做摘 relay、移索引、标 finalized，锁外 `relay.Close()`（设计:102-103、117）。这补上了 v0.2 只容易改 `Close` 的缺口。与真实并发关系匹配：当前 `Prepare` 仍在锁内 close 旧 entry（`internal/ptyrelay/registry.go:71-94`），当前 `Close/closeLocked` 也在锁内 close relay/ws（`:183-208`）。helper 化后 `Open/MarkAttached/Lookup` 都通过同一 registry 锁看到一致状态：Close 先摘索引则 Open/Lookup 失败，Open 先成功则 Close 拿到 live relay 并锁外关闭，幂等性由 `Relay.Close()` 保证（`internal/ptyrelay/relay.go:232-256`）。 |
| 5. interactive 仍走 `streamLocalJob`，pty 输出不过日志 | **已解决** | v0.3 写定 interactive 保留 `streamLocalJob`，但只用于终态检测和 interactions bridge，pty 输出不从 stdout/stderr log 回传（设计:12、122-123、243）。这与真实代码一致：`handleDispatch` 现状总是 `streamLocalJob→Wait→Result`（`internal/worker/dispatch.go:46-72`），`PtyRunner` 明确不写 stdout.log（`internal/runner/pty/runner.go:44-48`）。v0.3 的最终 join 链为 `Submit→putJobMapping→消费 pendingCancel→waitSession→go pumpPty→streamLocalJob→Wait→join pumpDone→Result`（设计:138-148、202-207），无环：`pumpPty` 只等 session output/conn，`streamLocalJob/Wait` 只等 job terminal，`PtySession.run` 不等待 worker Result。 |
| 6. observer 注入时序验收项 | **已解决** | v0.3 把 worker wiring 写成硬验收：`core.Build` 后创建 Client，随后 `ptyRunner.SetObserver(cl)`，最后才 `worker.Serve`；serve 侧 observer 恒 nil 保留 discard（设计:78、115、125）。真实装配顺序支持这个插入点：worker 命令当前 `core.Build`→`worker.New`→`worker.Serve`（`internal/commands/worker.go:183-208`），`core.Build` 在 pty 可用时把 `*PtyRunner` 注册到 `cr.Runners[ptyrunner.Name]`（`internal/core/core.go:78-85`）。因此断言取 `*PtyRunner` 并在 Serve 前注入可行；nil-safe 也已写入文件清单。 |

## 额外硬问题核对

- `hostCancelGrace` 现在只兜 open-stuck，pending/dial-fail 不再走 10s 正常路径：`Done(jobID)` 对 pending 返回已关 chan（设计:49-57、217），Result/cancel 后等待 `Done|lostCh|grace`（设计:90-94）。open relay 但 `recordLoop` 不 EOF 时，host 会等到 grace；这是预期兜底，不是由慢浏览器造成的正常延迟。真实 `recordLoop` 的 viewer fanout 非阻塞，慢 viewer 不会阻塞 recorder（`internal/ptyrelay/relay.go:117-145`）；当前 cast sink 只是可选同步 writer，代码库内尚未接入慢外部持久化（`internal/ptyrelay/relay.go:58`、`:127-133`）。
- join 链无死锁：v0.3 要求 observer 非阻塞（设计:65-71），`pumpPty` 不等待 `jobs.Wait`，`streamLocalJob/Wait` 不等待 `pumpDone`，`pumpDone` 只等 pty Read EOF/conn close（设计:202-207）。取消路径中 `jobs.Cancel` 只 cancel ctx、不等待 job（`internal/job/cancel.go:47-63`），不会和 host/worker Result 形成等待环。
- 非交互共享路径未被无门控改动污染。v0.3 的 pty pump、waitSession、relay Done 等均以 `Interactive` 或 `observer!=nil` 门控，唯一覆盖非交互的是 D-P2-9 的 pendingCancel，语义是补发既有远端 cancel，不改变正常非交互字节流（设计:243）。这符合 G023。

## 剩余硬问题

无阻断/高问题。

低风险计划项仍需在 P2 实施计划里落任务，但不影响进入计划拆分：

1. `pendingCancel` 对 dispatch 永不到达或 Submit 失败的残留，需要按 v0.3 §10 落 TTL 或生命周期清理（设计:250）。
2. `hostCancelGrace` 具体值与 stage/request timeout 偏序，需要按 v0.3 §6/§10 固化常量和测试（设计:217-219、249）。
3. `RelayClosing` 建议在实现注释或测试里显式覆盖，避免后续实现者误以为它是独立等待语义；当前 D-P2-2 + D-P2-8 的组合已足够可实现。

## 收敛判定

**GO（可进 P2 实施计划）。**

剩余阻断/高数量：**0**。

v0.3 已可据以拆 T 出 P2 实施计划。
