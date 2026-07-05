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

# 第四轮：P2 实施计划评审

> 工具限制：本轮会话仍未暴露 codebase-memory-mcp 的 `search_graph` 等图工具，已按项目规则降级为定点文件核对。验证命令使用项目内缓存：`.cache/go-build` / `.cache/gomod`。

## 事实性冲突（优先）

未发现“计划片段与真实签名/类型对不上，会直接 build 失败”的事实性冲突。已用当前代码自证：

- `go build ./...` 通过。
- `go vet ./...` 通过。
- `go list -deps ./internal/job | Select-String 'runner/pty|ptyrelay'` 无输出，`internal/job` 仍未依赖 pty runner / ptyrelay。

## 逐 T 核对表

| T | 判定 | 依据 |
|---|---|---|
| T0 `SessionObserver` + 输出所有权 | 可实施 | `PtyRunner` 当前只有 `reg *registry`，`Run` 当前在 `reg.add` 后无条件 `go io.Copy(io.Discard, sess)`，再 `sess.run(ctx)`（`internal/runner/pty/runner.go:30-62`）。计划加 `observer` 字段、`SetObserver`、`observer!=nil` 时交接 reader / nil 时保留 discard，和真实结构贴合。`OnSessionStart` 同步点确实在 `sess.run` 前；计划已写“observer 实现必须非阻塞”（`docs/plans/web-pty-attach/P2-plan.md:33-35`）。`LookupSession` 仅是 `PtyRunner` 私有 registry 的现有方法（`internal/runner/pty/runner.go:125-131`），`rg` 未发现外部依赖；删除/不再依赖不会影响别处。验收里“阻塞 observer 不得卡死 Run”的表述需按契约理解为 fake observer 内部自行 goroutine 化，不能要求 `Run` 容忍坏 observer。 |
| T1 `Dispatch.PtySessionID` + host 填充 | 可实施 | `Dispatch` 当前字段到 `RelayNonce` 为止，缺 `PtySessionID`（`internal/wsproto/frames.go:37-50`）。host runner 已在 interactive 下生成 `ptySessionID` 并写入 nonce/registry binding（`internal/runner/worker/runner.go:129-158`），dispatch 构造目前只传 `RelayNonce`（`:187-201`）。serve pty-connect 已校验 `binding.JobID/PtySessionID == hello.JobID/PtySessionID`（`internal/httpapi/pty_connect_handler.go:21-25`、`:70-72`）。计划字段和闭环正确。 |
| T2 `Registry.Done` + `closedChan` + `detachLocked` | 需补 | `Done` 语义表和真实 registry 状态贴合：pending entry 无 `Relay`（`internal/ptyrelay/registry.go:81-85`），open 后才 `Relay.Start()`（`:121-125`），`Relay.Done()` 已存在（`internal/ptyrelay/relay.go:255-256`）。`Close` 当前锁内 `Relay.Close()`（`internal/ptyrelay/registry.go:183-208`），`Prepare` replacement 当前也锁内 close（`:71-94`），确实需要 D-P2-8 helper。并发模型成立：锁内摘索引/标 finalized 后，`Open/Lookup/MarkAttached` 通过同一把锁看到一致状态。**但计划代码片段自相矛盾：`Prepare` 片段在解锁和锁外 close 后 `return cloneEntry(e)`（`docs/plans/web-pty-attach/P2-plan.md:158-165`），下一行又说“clone 在锁内取”（`:167`）。照抄会让返回值在解锁后可能被并发 `Close/Prepare` 改写，形成竞态/错快照。需把片段改成锁内 `ret := cloneEntry(e)`，解锁后 close oldRel，再 `return ret`。 |
| T3 rendezvous + `pendingCancel` + recvLoop URL + wiring | 可实施 | `Client` 当前有 `jobMu/jobMap`，无 session rendezvous 和 pending cancel（`internal/worker/client.go:76-112`、`:165-185`）；`recvLoop(ctx)` 当前在 dispatch 分支 `go cl.handleDispatch(ctx, d)`，cancel 未命中 mapping 时直接丢（`:301-326`）；`runSession(ctx,url)` 调 `recvLoop(ctx)`，签名改为 `recvLoop(ctx,url)` 的连锁清楚（`:240-294`）。`waitSession` 的 `go jobs.Wait(localID)` goroutine 在 session 命中后会继续等到该 job terminal 才退出，是每个 interactive job 一个有界等待，不是无限泄漏；`job.Service.Wait` 对 terminal/evicted job 立即收敛，对 live job 等 `entry.done`（`internal/job/cancel.go:76-86`）。`sessMu` 同护 `sessReady/sessWaiters/pendingCancel` 可行，粒度小。wiring 点存在：`runWorker` 当前 `core.Build` -> `worker.New` -> `worker.Serve`（`internal/commands/worker.go:183-208`），`core.Build` 注册 `*ptyrunner.PtyRunner` 到 `cr.Runners[ptyrunner.Name]`（`internal/core/core.go:78-85`），Serve 前注入 observer 可行；serve 侧不注入，nil 行为保留。 |
| T4 `pty_pump.go` | 可实施 | worker 侧 hello struct 按计划声明为 `job_id/pty_session_id/relay_nonce`，与 serve 完全一致（`internal/httpapi/pty_connect_handler.go:21-25`）。`coder/websocket.Conn` 文档说明除 `Reader`/`Read` 外方法可并发调用（mod `github.com/coder/websocket@v1.8.15/conn.go:29-31`）；计划只有一个 out goroutine 写 binary，一个 input loop 读，`conn.Close` 并发可接受。serve 的 `remotePtySource` 已用同一协议：binary 为 pty output/input，text resize（`internal/httpapi/pty_source.go:57-78`）。`derivePtyConnectURL` 用 `net/url` 直接换 path 正确，且兼容 worker config 允许 hub URL 只有根路径的现状（`internal/config/worker_config_test.go:49-78`）。断连判据包含 starting/running，贴合设计 D-P2-5（`docs/design/2026-07-04-web-pty-attach-P2-design.md:168-175`）。 |
| T5 `handleDispatch` 投影 + fail-fast + join | 可实施 | 当前 `handleDispatch` 只投影非 interactive 字段，提交后建 mapping，再 `streamLocalJob -> Wait -> Outcome -> Result`（`internal/worker/dispatch.go:23-73`）。计划加 `sessionURL` 参数、interactive 凭据 fail-fast、`Interactive/Cols/Rows` 投影、`putJobMapping` 后消费 pending cancel、`waitSession -> pumpPty -> streamLocalJob -> Wait -> join pumpDone -> Result`，和现有链路贴合。`Runner: builtinLocalRunner` 不会绕过 pty：worker 本地 Submit 在 `req.Interactive && !remote` 时会改选 `pty` runner（`internal/job/submit.go:110-121`）。join 链无环：pump 不等 `jobs.Wait`，`streamLocalJob/Wait` 不等 pump，最后只在发 Result 前 join。 |
| T6 host 三路 wait + `hostCancelGrace` | 可实施 | `relayPreparer` 当前只有 `Prepare/Close`（`internal/runner/worker/runner.go:61-64`），T2 后加 `Done` 正常。`Run` 当前所有路径立即返回，ctx 分支 cancel 后不等 worker/relay（`:207-243`）；计划只在 `f.Interactive` 下等 `Done|lostCh|grace` 或 Result 后等 `Done|grace`，非交互保持立即返回，符合 G023。defer `relayRegistry.Close` 已存在（`:160-164`），T2 后作为幂等 backstop 可行。 |
| T7 e2e + 零回归 + 环检 | 可实施 | 13 项覆盖设计 §8，且四个高风险点均有专测：尾字节 #2、Done 四边界 #5、starting 窗口 #7、unmapped cancel #8（`docs/plans/web-pty-attach/P2-plan.md:491-504`、`:512-516`）。零回归清单覆盖设计 §9 的 local/worker/cancel/interactions/outcome/worker disconnect/HOL/workflow/schedule/resume（`:506`）。环检命令方向有效；本轮用 PowerShell 等价命令已确认 `internal/job` 无 `runner/pty|ptyrelay` 依赖。 |

## 硬问题（阻断/高）

### 高 1：T2 `Prepare` replacement 片段的 clone/解锁顺序写错

计划在 `Prepare` replacement 示例中先 `r.mu.Unlock()`，再锁外 close old relay，最后 `return cloneEntry(e)`（`docs/plans/web-pty-attach/P2-plan.md:158-165`），但紧接着又要求“clone 在锁内取”（`:167`）。真实 `Registry.Prepare` 返回的是 entry 快照（`internal/ptyrelay/registry.go:71-94`）；如果照代码块实现，`e` 在解锁后仍是 registry map 中的新 entry 指针，可能被并发 `Close(jobID)` 或下一次 `Prepare(jobID)` 标 finalized/移索引后再 clone，导致调用方拿到错误状态或竞态快照。

建议把计划片段改成明确顺序：

```go
r.mu.Lock()
oldRel := r.detachLocked(r.byJob[b.JobID], "replaced")
e := &RelayEntry{ /* pending_worker */ }
r.byJob[b.JobID] = e
// 写 bySession/byNonce
ret := cloneEntry(e)
r.mu.Unlock()
if oldRel != nil { _ = oldRel.Close() }
return ret
```

这不是设计问题，是计划片段级硬问题；不修正容易在 T2 实施时返工。

## 非阻断注意

- T0 的“`OnSessionStart` 阻塞时不得卡死 `Run`”验收必须改写为“observer 实现非阻塞”，因为计划和设计都选择同步回调；`PtyRunner.Run` 不应为坏 observer 再起 goroutine，否则 session start 的交接时序会变弱。
- T3/T5 的 stale `pendingCancel` 清理已列待办（`docs/plans/web-pty-attach/P2-plan.md:518-520`），建议落在 T3 helper 或 T5 Submit 失败分支里，不要拖到 T7。
- T6 的 `hostCancelGrace=10s` 与 stage/request timeout 偏序已列待办；实施时需要用常量和测试固化，避免上层超时先打断 drain 证明。

## 判定

**需改。**

阻断：**0**。高：**1**。

修正 T2 `Prepare` 片段的 clone/解锁顺序后，计划可据以逐 T 实施；其余 T 的触碰点、依赖序和验收覆盖与真实代码一致。

# 第五轮：P2 实现 diff 代码审查

> 审核方：codex（对抗式读真实 diff，未改源码；仅追加本文档）。  
> 被审：`git diff 88d5a51..fee4b6b`，8 个实现提交 T0-T7：`96a9951` / `3f60c44` / `f15e1ad` / `113ef5e` / `378b3ce` / `cbb18ff` / `de20f26` / `fee4b6b`。  
> 对照：`docs/design/2026-07-04-web-pty-attach-P2-design.md` v0.4，重点核对 D-P2-1..9，特别是跨连接取消顺序、rendezvous、relay 生命周期 CAS、selfClosing 判据、单 reader、G023。
> 工具限制：当前会话仍未暴露 codebase-memory-mcp 的 graph tools/resources，已按项目规则 fallback 到 `git diff/show` + 定点文件核对。

## 8 项重点核对

1. **取消协议跨连接顺序（D-P2-2/6）：通过。**  
   host 侧 interactive 正常结果分支在收到 worker `Result` 后等待 `relayRegistry.Done(jobID)`，再返回并由 defer `Close` 兜底（`internal/runner/worker/runner.go:223-238`、`:173-176`）；ctx cancel 分支先发 hub cancel，再等 `Done|lostCh|hostCancelGrace`（`:257-278`）。worker 侧 `handleDispatch` 在发 `Result` 前 join `pumpDone`（`internal/worker/dispatch.go:100-116`）；`pumpPty` out pump 读到 pty EOF 后设置 `selfClosing` 并关闭 pty ws（`internal/worker/pty_pump.go:109-125`）。serve 侧 `recordLoop` 先把 `Read` 返回的 `n>0` 写 ring/cast/fanout，再在 `err != nil` 时 `Close` relay（`internal/ptyrelay/relay.go:120-138`），所以 EOF 同帧尾字节不会被丢。`handlePtyConnect` 在 `entry.Relay.Done()` 或 request ctx 结束后返回，defer `Registry.Close` 只是幂等兜底（`internal/httpapi/pty_connect_handler.go:86-96`）。没有发现 host 先 finish 截断 in-flight 尾字节的路径。

2. **rendezvous 竞态与 goroutine 泄漏（D-P2-3）：通过。**  
   `OnSessionStart` 在发 `ch <- sess` 前已释放 `sessMu`，且 waiter chan 为 buffer 1，不会锁内阻塞（`internal/worker/client.go:227-234`）。`waitSession` 的 `jobs.Wait(localID)` goroutine 在 session 命中后仍会等到该 local job terminal 才退出（`:259-266`）；这是每个 interactive job 一个有界 goroutine，不是无限泄漏，因为 `handleDispatch` 后续 `streamLocalJob`/`jobs.Wait` 本身也等同一个 job 终态（`internal/worker/dispatch.go:93-100`）。`clearWaiter` 同时清 `sessWaiters` 和 `sessReady`，晚到 session 不会永久残留（`internal/worker/client.go:276-283`）。`sessReady`/`sessWaiters`/`pendingCancel` 统一由 `sessMu` 保护（`:248-257`、`:295-330`），一致性成立。

3. **relay 生命周期 CAS（D-P2-2/8）：通过。**  
   `Prepare` 在锁内 detach old entry、写新 entry、锁内 clone 快照，然后解锁后 close old relay，修掉第四轮指出的错快照风险（`internal/ptyrelay/registry.go:79-102`）。`Close` 锁内 detach、锁外 `Relay.Close()`，避免 registry HOL（`:194-203`）。`detachLocked` 直接将 entry 标 finalized 并移除 job/session/nonce 索引（`:212-221`），并发 `Open/Lookup/MarkAttached/Done` 通过同一把锁看到 pending/open/finalized/missing 的一致状态（`:107-134`、`:138-180`、`:234-244`）。`Done` 对 missing/finalized/pending(`Relay==nil`) 返回已关闭 sentinel，对 open/attached 返回 live `Relay.Done()`（`:234-244`）；`Relay.Close()` 自身用 `closed` CAS 和 `done` chan 保证幂等（`internal/ptyrelay/relay.go:235-256`）。

4. **selfClosing/state 断连判据（D-P2-5）：通过。**  
   out pump 在 `sess.Read` 返回错误时 `selfClosing.Store(true)`，in loop 结束后用 `selfClosing.Load()` 判定是否本端 teardown；跨 goroutine 用 atomic 建立可见性（`internal/worker/pty_pump.go:101-140`）。如果 state 已 `cancelling/exiting/closed`，不误发 cancel；默认分支覆盖 `starting/running`，因此 observer 早于 `StateRunning` 的 starting 窗口发生外部断连也会 cancel（`:133-139`）。`PtySession` 自然/取消 teardown 一开始进入 `StateExiting`，最终 `StateClosed` 后关 done（`internal/runner/pty/session.go:176-217`），排除自然退出 teardown 被误判为外部断连的主要窗口。

5. **单 reader（D-P2-3）：通过。**  
   `PtyRunner.Run` 在 `observer != nil` 时只同步调用 `OnSessionStart`，明确不启动 discard drain（`internal/runner/pty/runner.go:71-77`）；worker 命令在 `worker.Serve` 前把 Client 注入为 observer（`internal/commands/worker.go:205-213`）。`observer == nil` 分支保留原 `io.Copy(io.Discard, sess)` drain（`internal/runner/pty/runner.go:77-80`），serve/tests 的非 worker 路径维持原行为。

6. **G023 零回归：通过。**  
   host 侧新增 drain wait 只在 `f.Interactive` 下执行（`internal/runner/worker/runner.go:233-238`、`:272-278`）；非 interactive dispatch 字段仅多带零值 `Interactive/Cols/Rows/RelayNonce/PtySessionID`，原 result/lost/ctx select 结构未改变（`:201-218`、`:223-278`）。worker `handleDispatch` 只有 interactive 时 fail-fast、wait session、pump pty；非 interactive 仍 `Submit -> put mapping -> streamLocalJob -> Wait -> Outcome -> Result`，新增 `pendingCancel` 只补早到 cancel 的既有语义（`internal/worker/dispatch.go:27-124`）。`PtyRunner` 输出所有权只在 observer 注入时改变，nil observer 保留原 discard（`internal/runner/pty/runner.go:71-80`）。

7. **pty ws framing/单 writer：通过。**  
   worker hello 字段名与 serve 端 `ptyConnectHello` 一致（`internal/worker/pty_pump.go:35-40`、`internal/httpapi/pty_connect_handler.go:21-25`），serve 校验 job/session/nonce/worker instance（`internal/httpapi/pty_connect_handler.go:60-72`）。hello 在 out goroutine 启动前串行写，之后只有 out goroutine写 binary；in loop只读 binary/text（`internal/worker/pty_pump.go:92-128`、`:151-167`）。serve `remotePtySource.Write/Resize` 用 `wmu` 串行写 binary/text，`Read` 是唯一 reader 并跳过非 binary 输出帧（`internal/httpapi/pty_source.go:26-78`）。符合 coder/websocket 单 reader/单 writer 使用约束。

8. **边界/错误路径：通过。**  
   worker 对 bad URL、dial fail、hello write fail 都 cancel local job 并 close `pumpDone`（`internal/worker/pty_pump.go:73-99`）。serve nonce 校验失败、instance mismatch、binding mismatch、job not live 均关闭 pty ws；job not live 同时 close relay（`internal/httpapi/pty_connect_handler.go:60-83`）。`waitSession` nil 路径会跳过 pump，后续仍等 local job terminal 并发 Result（`internal/worker/dispatch.go:86-100`）；host 侧 pending/missing relay 的 `Done` 是已关 chan，不会空等 grace（`internal/ptyrelay/registry.go:234-244`）。早到 cancel 先记 `pendingCancel`，mapping 建立后立即消费并 cancel，且 defer 清理 stale 记录（`internal/worker/client.go:473-480`、`internal/worker/dispatch.go:27-33`、`:71-79`）。

## 硬问题（阻断/高）

未发现 P2 实现 diff 内会导致 race、死锁、goroutine 泄漏、尾字节截断、跨连接顺序破坏或 G023 行为回归的硬问题。

阻断：**0**。高：**0**。

## 非阻断注意

1. **Windows 测试环境仍有若干非 P2 包 / e2e 清理脆弱性。**  
   `go test ./...` 本轮失败集中在 Windows 临时目录文件句柄清理、跨平台 shell/权限假设和既有包：`internal/client`、`internal/commands`、`internal/job`、`internal/job/workflow`、`internal/store`、`internal/worker`。其中 `internal/worker` 的 P2 e2e 用例出现 `TempDir RemoveAll cleanup: ... stderr.log: The process cannot access the file because it is being used by another process`，更像 Windows 文件句柄释放/测试环境问题；定向非 e2e P2 包 `go test ./internal/ptyrelay ./internal/runner/pty ./internal/runner/worker` 通过。该问题不改变本轮代码审查判定，但后续若要把 P2 e2e 纳入 Windows CI，需要单独治理句柄关闭/测试清理。

2. **`Client.writeFrame` 仍写当前 `cl.conn`，不是 dispatch 到达时的连接。**  
   这一点不是 P2 新增问题：本轮只把 `sessionURL` 线程化给 pty-connect，控制帧写入仍沿用既有 `cl.conn` 字段（`internal/worker/client.go:397`、`:465`、`:513-516`）。在同一 worker 进程 reconnect 且 job 跨连接继续执行的语义下，这可能是有意设计；但如果未来要严格按“派发连接”归还 Result/Outcome，需要另起 WP/C7 议题。P2 pty attach 的跨连接尾字节顺序不依赖这个点，因为 host 的收敛锚是 serve relay `Done`，不是 worker Result 单独排序。

3. **`hostCancelGrace` 仍需与上层 stage/request timeout 维持偏序。**  
   常量已经落为 10s（`internal/runner/worker/runner.go:37-43`），实现正确；后续配置/文档应继续保证上层 timeout 大于该 grace，否则上层取消可能抢先结束 drain 证明。

## 自证结果

- `git diff --check 88d5a51..fee4b6b`：通过。
- `go build ./...`（`GOCACHE=.cache/go-build`，`GOMODCACHE=.cache/gomod`）：通过。
- `go vet ./...`（同上）：通过。
- `go test ./internal/ptyrelay ./internal/runner/pty ./internal/runner/worker`：通过。
- `go test ./...`：失败，原因见“非阻断注意 1”，未定位到 P2 diff 的硬 bug。
- `go test ./internal/worker -run 'Test(Pty|E2E|Rendezvous|HandleDispatch|Pending|Derive|Clamp|Pump|WaitSession|OnSession|Interactive|Unmapped|Starting|Relay|Done|Cancel)' -count=1`：失败，命中 Windows `TempDir RemoveAll cleanup` 文件句柄问题（P2 e2e cancel/timeout/worker-disconnect 用例）；非 e2e P2 核心包已通过。
- `go test -race ...`：未能执行；第一次失败为 `-race requires cgo`，设置 `CGO_ENABLED=1` 后失败为 `C compiler "gcc" not found`。本机当前缺 race 所需 C toolchain。

## 收官判定

**GREEN（可收官，进入 P3/P4）。**

本轮真实 diff 未发现阻断/高问题；P2 的核心并发/时序风险点（Result vs relay Done 跨连接顺序、rendezvous、pending cancel、relay detach/Done、selfClosing/state 判据、单 reader、pump join）均已按 v0.4 设计落地。剩余事项属于 Windows 测试环境稳定性和后续 reconnect 语义治理，不阻塞 P2 收官。
