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

