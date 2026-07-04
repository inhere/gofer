# P2：worker 端到端 pty attach 实施计划

> 上游：设计 [`../../design/2026-07-04-web-pty-attach-P2-design.md`](../../design/2026-07-04-web-pty-attach-P2-design.md) **v0.3（codex round-3 终审 = GO）**；评审 [`../../review/2026-07-04-web-pty-attach-P2-codex-review.md`](../../review/2026-07-04-web-pty-attach-P2-codex-review.md)（3 轮）；P1 计划 [`P1-plan.md`](P1-plan.md)。
> P2 = **worker 真拨入 + 字节泵 + resize + 两段化取消协议 + 断连全链路**。serve 侧协议/relay/安全闸 P1 已落地。
> 铁律：G023（非交互零行为变化，全程 `Interactive`/`observer!=nil` 门控）/ G022·G024（`job` 不 import pty/ptyrelay；`internal/worker` 可 import `runner/pty`）。每 T 子阶段 `go test ./...` 绿即提交（SR1202，前缀 `feat(pty)`/`fix(pty)`，结尾 Co-Authored-By，**不 push**）。
> **执行环境（SUPMODE）**：host codex 纯编辑（Windows，只 `go build`/`go vet`，`.git` 只读）；容器（Linux）跑 `go test ./...`（含真 pty）+ 提交。派 codex：`gofer job run -f <task.md>`（agent=codex runner=local cwd=tools/gofer）。

## 任务依赖
```
T0(PtyRunner observer/输出所有权) ─┬─▶ T3(worker rendezvous+wiring) ─▶ T4(pty_pump) ─▶ T5(handleDispatch)
T1(Dispatch.PtySessionID+host填充) ─┤                                                      │
T2(registry.Done+D-P2-8 helper) ───┴─▶ T6(host 三路 wait) ◀───────────────────────────────┘
                                                              T7(e2e 矩阵 + 零回归)  ← 依赖 T0..T6
```
进度：
- [ ] T0 PtyRunner `SessionObserver` + 输出所有权（消 discard 双读）
- [ ] T1 `wsproto.Dispatch.PtySessionID` + host 填充 + `Forward` 已有字段核对
- [ ] T2 `ptyrelay.Registry.Done(jobID)` 语义 + `closedChan` + D-P2-8 锁外关 helper（Close+Prepare）
- [ ] T3 worker Client：rendezvous(`OnSessionStart`/`waitSession`) + `pendingCancel` + recvLoop URL/cancel threading + `SetObserver` wiring
- [ ] T4 `worker/pty_pump.go`：拨出 + 双向泵 + resize + selfClosing/断连判据 + `derivePtyConnectURL`
- [ ] T5 `handleDispatch`：投影 + fail-fast + 消费 pendingCancel + 启动 pump + join pumpDone
- [ ] T6 host `runner/worker.Run` interactive 三路 wait + `hostCancelGrace`
- [ ] T7 e2e 矩阵（13 项）+ 零回归清单 + `go list -deps` 环检

---

## T0 — PtyRunner `SessionObserver` + 输出所有权（消 discard 双读，D-P2-3）

**目标**：`PtyRunner.Run` 现无条件 `go io.Copy(io.Discard, sess)`（`runner/pty/runner.go:53-58`）。P2 pump 再读 = 双读吞字节。→ 加可注入 `SessionObserver`：设定时 observer 成唯一 reader（不 drain）；未设时保留 discard（serve / P0-P1 tests，G023）。

**触碰** `internal/runner/pty/runner.go`：
```go
// SessionObserver 在 session 注册后、Run 阻塞前【同步】回调；observer 成为 sess 输出的唯一 reader。
// 实现必须【非阻塞】(内部只投递/起 goroutine)，否则会阻塞 PtyRunner.Run→job.execute。
type SessionObserver interface {
	OnSessionStart(jobID string, sess *PtySession)
}

type PtyRunner struct {
	reg      *registry
	observer SessionObserver // nil=serve/tests → 保留默认 discard drain
}

// SetObserver 由 worker 命令在 worker.Serve 前注入（serve 侧不调 → nil）。
func (r *PtyRunner) SetObserver(o SessionObserver) { r.observer = o }

func (r *PtyRunner) Run(ctx context.Context, req runner.Request) runner.Result {
	sess, err := r.start(req)
	if err != nil {
		return runner.Result{ExitCode: -1, Err: err}
	}
	r.reg.add(req.JobID, sess)
	defer r.reg.remove(req.JobID)

	if r.observer != nil {
		// worker: observer 拿走单一 reader 所有权（起 pty ws 泵）。不再 discard。
		r.observer.OnSessionStart(req.JobID, sess)
	} else {
		// serve / 测试: 保持 pty 被排空避免 slave 阻塞（P0 spike 行为，G023）。
		go func() { _, _ = io.Copy(io.Discard, sess) }()
	}

	code, runErr := sess.run(ctx)
	return runner.Result{ExitCode: code, Err: runErr}
}
```
- 删除设想中的 `LookupSession`（不再需要，session 经 observer 直接交付）。

**验收**：
- 新单测：设 fake observer → `Run` 后 observer 收到 `(jobID, sess)`，且**无** discard goroutine（用一个只发 N 字节的 fake pty，断言 observer 读到全部 N 字节、零丢）。
- 未设 observer → 保留 discard（现有 `session_test.go` / P0 行为零回归）。
- **`OnSessionStart` 非阻塞是接口契约**（round-4 建议）：`PtyRunner.Run` **不**为坏 observer 兜底起 goroutine（否则交接时序变弱）；单测里 observer 若需耗时须自己 goroutine 化。断言正常（非阻塞）observer 下 `Run` 交接后正常进入 `sess.run`。

---

## T1 — `Dispatch.PtySessionID` + host 填充（D-P2-4 协议字段）

**目标**：serve pty-connect 端点强校验 `binding.PtySessionID == hello.PtySessionID`（`httpapi/pty_connect_handler.go:70-72`），但 `wsproto.Dispatch` 只有 `RelayNonce`。→ 补字段 + host 填充（worker 投影归 T5）。

**触碰**：
1. `internal/wsproto/frames.go:37-50` `Dispatch` 加：
```go
	PtySessionID string `json:"pty_session_id,omitempty"`
```
2. `internal/runner/worker/runner.go:188-201` 构造 `Dispatch` 处补（`ptySessionID` 变量 `:141` 已有）：
```go
	d := wsproto.Dispatch{
		// ...现有字段...
		Interactive:  f.Interactive,
		Cols:         f.Cols,
		Rows:         f.Rows,
		RelayNonce:   relayNonce,
		PtySessionID: ptySessionID, // T1: worker hello 需回显给 serve 端点校验
	}
```

**验收**：
- 单测：interactive worker dispatch 的 `Dispatch.PtySessionID` == host `Prepare/Issue` 时的 `ptySessionID`（非空）；非 interactive 为空。
- 非交互零回归。

---

## T2 — `Registry.Done(jobID)` + `closedChan` + D-P2-8 锁外关 helper（阻断1 + 高2/中1）

**目标**：① host 终态收敛需 `Done(jobID)`（**pending=无字节可排=已关 chan；open=relay drain 信号**）；② `Close`/`Prepare` replacement 现锁内 close relay/ws → 全局 HOL，拆「锁内摘 relay/移索引/标 finalized、锁外 close」。

**触碰** `internal/ptyrelay/registry.go`：
```go
// 包级：pending/finalized/missing relay 无字节可排 → 立即可过的已关 chan。
var closedChan = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// Done 返回 jobID 的 serve-drain 完成信号（D-P2-2 语义表）：
//   open/attached(有 live Relay) → relay.Done()（recordLoop EOF=已排空）
//   pending_worker / finalized / missing → 已关 chan（host 不空等）
func (r *Registry) Done(jobID string) <-chan struct{} {
	if r == nil || jobID == "" {
		return closedChan
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byJob[jobID]
	if e == nil || e.State == RelayFinalized || e.Relay == nil {
		return closedChan
	}
	return e.Relay.Done()
}

// detachLocked 锁内摘出 relay 引用 + 移索引 + 标 finalized，返回待关 relay（可能 nil）。
// 供 Close 与 Prepare replacement 共用；调用方【锁外】 relay.Close()（D-P2-8, 消 HOL）。
func (r *Registry) detachLocked(e *RelayEntry, reason string) *Relay {
	if e == nil || e.State == RelayFinalized {
		return nil
	}
	rel := e.Relay
	e.State = RelayFinalized
	e.ClosedAt = r.now()
	e.CloseReason = reason
	r.removeIndexesLocked(e)
	return rel
}
```
`Close` 改为：
```go
func (r *Registry) Close(jobID, reason string) {
	if r == nil || jobID == "" {
		return
	}
	r.mu.Lock()
	rel := r.detachLocked(r.byJob[jobID], reason)
	r.mu.Unlock()
	if rel != nil {
		_ = rel.Close() // 锁外关 source/viewers/ws（幂等）
	}
}
```
`Prepare` replacement 处（`:71-94`，现为 `defer r.mu.Unlock()`）改为**显式 Unlock + 锁内取快照 + 锁外关**（评审 round-4 高1：clone **必须在锁内**，否则解锁后 `e` 被并发 `Close/Prepare` 改写成错快照）：
```go
	r.mu.Lock()
	oldRel := r.detachLocked(r.byJob[b.JobID], "replaced") // 锁内摘旧 relay（nil-safe）
	e := &RelayEntry{Binding: b, State: RelayPendingWorker, CreatedAt: r.now()}
	r.byJob[b.JobID] = e
	if b.PtySessionID != "" { r.bySession[b.PtySessionID] = e }
	if b.Nonce != "" { r.byNonce[b.Nonce] = e }
	ret := cloneEntry(e) // ★锁内取快照（解锁后 e 可能被并发改写）
	r.mu.Unlock()
	if oldRel != nil { _ = oldRel.Close() } // 锁外关旧 relay/ws（消 HOL）
	return ret
```
> `RelayClosing` 态：`detachLocked` 直接跳 finalized，不单列等待语义（round-3 建议加实现注释说明）。`Done`/`detachLocked` 均在注释点明 `RelayClosing` 的处理。

**验收**：
- `Done` 单测四边界：open relay → 返回 live `relay.Done()`（relay Close 后该 chan 关）；pending（Prepare 未 Open）→ `closedChan` 立即可读；finalized → `closedChan`；missing → `closedChan`。
- `Close`/`Prepare` replacement 锁外关：并发 `Open/Lookup/MarkAttached` 不被一个慢 `src.Close()` 阻塞（用一个 Close 阻塞 100ms 的 fake source，断言另一 job 的 `Lookup` 立即返回）。
- 幂等：重复 `Close(jobID)` 安全；`detachLocked` 对已 finalized 返回 nil。
- 现有 `registry_test.go` 零回归。

---

## T3 — worker Client：rendezvous + pendingCancel + recvLoop threading + SetObserver wiring

**目标**：Client 实现 `OnSessionStart`/`waitSession`（事件驱动交接，无轮询）；`pendingCancel`（cancel 早于 mapping 不丢，D-P2-9，覆盖非交互）；`recvLoop` 透传 session URL（D-P2-7）；worker 命令注入 observer。

**触碰**：
1. `internal/worker/client.go` `Client` 加字段 + rendezvous：
```go
	// D-P2-3 session 交接 rendezvous（OnSessionStart 早到/晚到两序皆安全）
	sessMu      sync.Mutex
	sessReady   map[string]*ptyrunner.PtySession // localID → sess（observer 早到时 buffer）
	sessWaiters map[string]chan *ptyrunner.PtySession
	// D-P2-9 unmapped cancel（cancel 早于 putJobMapping）
	pendingCancel map[string]struct{} // remoteJobID
	ptySessions   PtySessions          // 供 pump LookupSession 兜底（可选，主路径用 rendezvous）
```
```go
// OnSessionStart 实现 ptyrunner.SessionObserver：非阻塞投递（不起 pump，pump 由 handleDispatch 起）。
func (cl *Client) OnSessionStart(localID string, sess *ptyrunner.PtySession) {
	cl.sessMu.Lock()
	if ch := cl.sessWaiters[localID]; ch != nil {
		delete(cl.sessWaiters, localID)
		cl.sessMu.Unlock()
		ch <- sess
		return
	}
	cl.sessReady[localID] = sess
	cl.sessMu.Unlock()
}

// waitSession 等 session 注册；命中 sessReady 立返，否则挂 waiter；
// 唤醒集合：session-start | jobs.Wait 终态 | ctx.Done（排队被 cancel 不悬挂）。
func (cl *Client) waitSession(ctx context.Context, localID string) *ptyrunner.PtySession {
	cl.sessMu.Lock()
	if s := cl.sessReady[localID]; s != nil {
		delete(cl.sessReady, localID)
		cl.sessMu.Unlock()
		return s
	}
	ch := make(chan *ptyrunner.PtySession, 1)
	cl.sessWaiters[localID] = ch
	cl.sessMu.Unlock()

	term := make(chan struct{})
	go func() { cl.jobs.Wait(localID); close(term) }() // 终态即醒
	select {
	case s := <-ch:
		return s
	case <-term:
		cl.clearWaiter(localID)
		return nil
	case <-ctx.Done():
		cl.clearWaiter(localID)
		return nil
	}
}
```
2. `pendingCancel` 记录/消费：
```go
// recordPendingCancel: recvLoop 收 cancel 但 mapping 未建时调用。
func (cl *Client) recordPendingCancel(remoteID string) {
	cl.sessMu.Lock()
	cl.pendingCancel[remoteID] = struct{}{}
	cl.sessMu.Unlock()
}
// takePendingCancel: handleDispatch putJobMapping 后 / Submit 失败分支调用；命中则本地 job 需立即 Cancel。
// 也用于 handleDispatch 退出时清 stale（dispatch 已处理但从未 mapping 的残留）。
func (cl *Client) takePendingCancel(remoteID string) bool {
	cl.sessMu.Lock()
	_, ok := cl.pendingCancel[remoteID]
	delete(cl.pendingCancel, remoteID)
	cl.sessMu.Unlock()
	return ok
}
```
> **stale 清理（round-4 非阻断项，落 T3/T5 不拖 T7）**：`pendingCancel[remoteID]` 只在 handleDispatch 的 mapping/Submit-fail 路径被消费；本 worker 从未被派发该 job（cancel 空投）时会残留。→ `recordPendingCancel` 里做**软上限 sweep**（超过 N 条时按插入序丢最旧，或加 remoteID→记录时刻、`recvLoop` 每轮顺带清超 TTL 项）。dispatch 到达但 Submit 失败时由 T5 的 `defer cl.takePendingCancel(d.JobID)` 清（见 T5）。
3. `recvLoop` 现签名 `recvLoop(ctx)`（`client.go:301`）→ 需要当前 session URL。`runSession(ctx,url)` 调 `recvLoop`；改 `recvLoop(ctx, url)` 并透传给 `handleDispatch`：
```go
	case wsproto.TypeDispatch:
		d, derr := wsproto.As[wsproto.Dispatch](env)
		if derr != nil { continue }
		go cl.handleDispatch(ctx, url, d)       // D-P2-7 per-dispatch URL
	case wsproto.TypeCancel:
		cf, derr := wsproto.As[wsproto.Cancel](env)
		if derr != nil { continue }
		if localID := cl.localJobID(cf.JobID); localID != "" {
			_ = cl.jobs.Cancel(localID)
		} else {
			cl.recordPendingCancel(cf.JobID)     // D-P2-9 cancel 早于 mapping
		}
```
4. `New` 初始化新 map；`Config` 加 `PtySessions PtySessions`（窄接口，见下）。
5. `internal/commands/worker.go:192-204` wiring（`SetObserver` 在 `worker.Serve` 前，中2 验收）：
```go
	cl := worker.New(worker.Config{ /* ...现有... */ }, cr.Jobs)
	if pr, ok := cr.Runners[ptyrunner.Name].(*ptyrunner.PtyRunner); ok {
		pr.SetObserver(cl) // worker: observer=Client；serve 从不调 → nil（保留 discard）
	}
	if err := worker.Serve(cl, wc); err != nil { /* ... */ }
```
> `PtySessions` 窄接口（若 pump 需 LookupSession 兜底）：`type PtySessions interface{ LookupSession(string)(*ptyrunner.PtySession,bool) }`——主路径用 rendezvous，本接口仅备用；若设计确认不需要可省。

**验收**：
- rendezvous 单测：OnSessionStart **先于** waitSession（走 sessReady buffer）/ **后于** waitSession（走 waiter chan）两序都拿到 sess；job 终态先到 → waitSession 返 nil 不悬挂；ctx 取消 → 返 nil。
- pendingCancel 单测：先 `recordPendingCancel(r)` 再 `takePendingCancel(r)`==true 且清空；未记录 →false；并发安全（`-race`）。
- wiring 单测/断言：`SetObserver` 在 `worker.Serve` 前；serve 路径 observer 恒 nil（core.Build 不设）。
- `recvLoop(ctx,url)` 透传：非交互 dispatch 零回归（现有 worker e2e 绿）。

---

## T4 — `worker/pty_pump.go`：拨出 + 双向泵 + resize + 断连判据（D-P2-1/5/7）

**目标**：worker 第二条独立 pty ws；out=唯一 reader；in=输入+resize；selfClosing+state 判据（含 starting）。

**触碰** 新 `internal/worker/pty_pump.go`：
```go
// pumpPty 拨出第二条专用 pty ws 并双向泵字节。返回 pumpDone（handleDispatch join 后才发 Result）。
// sessionURL = 派发此 dispatch 的 hub 会话 URL（D-P2-7 per-dispatch，非全局）。
func (cl *Client) pumpPty(ctx context.Context, sessionURL, localID, remoteJobID, ptySessionID, nonce string, sess *ptyrunner.PtySession) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		url, err := derivePtyConnectURL(sessionURL) // net/url: path→/v1/workers/pty-connect
		if err != nil { _ = cl.jobs.Cancel(localID); return }

		header := http.Header{}
		if cl.token != "" { header.Set("Authorization", "Bearer "+cl.token) }
		conn, _, derr := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
		if derr != nil { _ = cl.jobs.Cancel(localID); return } // 断连即终止（host Done(pending)=已关→不空等）
		defer conn.Close(websocket.StatusNormalClosure, "pty pump end")

		if err := wsjson.Write(ctx, conn, ptyConnectHello{JobID: remoteJobID, PtySessionID: ptySessionID, RelayNonce: nonce}); err != nil {
			_ = cl.jobs.Cancel(localID); return
		}

		var selfClosing atomic.Bool
		// out: 唯一 reader（PtyRunner 已因 observer 关掉 discard）
		outDone := make(chan struct{})
		go func() {
			defer close(outDone)
			buf := make([]byte, 32*1024)
			for {
				n, rerr := sess.Read(buf)
				if n > 0 { if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil { break } }
				if rerr != nil { selfClosing.Store(true); break } // sess EOF=teardown
			}
			_ = conn.Close(websocket.StatusNormalClosure, "pty eof") // 主动关→触发 serve recordLoop EOF
		}()
		// in: 输入 binary → WriteInput；resize text → Resize
		cl.ptyInputLoop(ctx, conn, sess)
		// in 结束若非本端 teardown 且 session 未在拆 → 外部断连即终止（含 starting，D-P2-5）
		if !selfClosing.Load() {
			switch sess.State() {
			case ptyrunner.StateCancelling, ptyrunner.StateExiting, ptyrunner.StateClosed:
				// 本端 teardown / 已在拆 → 不误 Cancel
			default: // starting / running
				_ = cl.jobs.Cancel(localID)
			}
		}
		<-outDone
	}()
	return done
}

func (cl *Client) ptyInputLoop(ctx context.Context, conn *websocket.Conn, sess *ptyrunner.PtySession) {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil { return }
		switch typ {
		case websocket.MessageBinary:
			_, _ = sess.WriteInput(data)
		case websocket.MessageText:
			var ctrl struct{ Type string; Cols, Rows int }
			if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" {
				c, r := clampSize(ctrl.Cols, ctrl.Rows) // 1..500 / 1..200
				_ = sess.Resize(c, r)
			}
		}
	}
}

// derivePtyConnectURL 用 net/url 换 path 为 /v1/workers/pty-connect（同 scheme/host）；非法 fail-fast。
func derivePtyConnectURL(hubURL string) (string, error) {
	u, err := url.Parse(hubURL)
	if err != nil || u.Host == "" { return "", fmt.Errorf("pty pump: bad hub url %q", hubURL) }
	u.Path = "/v1/workers/pty-connect"
	return u.String(), nil
}
```
> `ptyConnectHello` 需与 serve 端点 `httpapi/pty_connect_handler.go:21-25` 的字段 json tag 完全一致（`job_id`/`pty_session_id`/`relay_nonce`）；在 worker 侧声明同形 struct。`ptyrunner.State*` 常量需保持导出（现已导出 `session.go:24-30`）。

**验收**：
- 单测（`httptest.Server` 起 serve pty-connect + fake `PtySession`）：hello 正确 → serve `Open`；sess 输出 → conn 收 binary；conn 发 binary → `sess.WriteInput`；conn 发 text `{type:resize}` → `sess.Resize(clamp)`。
- selfClosing：sess EOF → out 主动关 conn、in 结束不 Cancel；外部断 conn（sess running/starting）→ Cancel(localID)（验 starting 窗口，D-P2-5）。
- `derivePtyConnectURL`：`ws://h:9090/v1/workers/connect`→`.../pty-connect`；非法 URL fail-fast → dial 前 Cancel。
- dial 失败 → Cancel(localID)（断连即终止）。

---

## T5 — `handleDispatch`：投影 + fail-fast + 消费 pendingCancel + 启动 pump + join（D-P2-4/9）

**目标**：worker 收 dispatch 投影 interactive 字段、fail-fast、消费 pendingCancel、interactive 时 `waitSession`→`pumpPty`、**发 Result 前 join pumpDone**。

**触碰** `internal/worker/dispatch.go`：
```go
func (cl *Client) handleDispatch(ctx context.Context, sessionURL string, d wsproto.Dispatch) {
	// stale pendingCancel 清理（round-4）：无论走哪条返回路径，退出时确保该 remoteID 不残留。
	// mapping 建立后的正常消费在下方；此 defer 兜住「fail-fast/Submit 失败/从未 mapping」路径。
	defer cl.takePendingCancel(d.JobID)
	// fail-fast：interactive 但缺凭据 → 不起不可 attach 的裸 pty（D-P2-4）
	if d.Interactive && (d.RelayNonce == "" || d.PtySessionID == "") {
		_ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{
			JobID: d.JobID, Status: job.StatusFailed, ExitCode: -1, Error: "interactive dispatch missing relay credentials",
		})
		return
	}
	res, err := cl.jobs.Submit(job.JobRequest{
		ProjectKey: d.ProjectKey, Agent: d.Agent, Runner: builtinLocalRunner,
		Prompt: d.Prompt, Cmd: d.Cmd, Cwd: d.Cwd, TimeoutSec: d.TimeoutSec,
		Interactive: d.Interactive, Cols: d.Cols, Rows: d.Rows, // T5 投影
	})
	if err != nil { /* ...现有 result{failed}... */ return }

	localID := res.ID
	cl.putJobMapping(d.JobID, localID)
	defer cl.dropJobMapping(d.JobID)
	// D-P2-9：cancel 早于 mapping → 立即 Cancel（覆盖非交互）
	if cl.takePendingCancel(d.JobID) { _ = cl.jobs.Cancel(localID) }

	var pumpDone <-chan struct{}
	if d.Interactive {
		if sess := cl.waitSession(ctx, localID); sess != nil {
			pumpDone = cl.pumpPty(ctx, sessionURL, localID, d.JobID, d.PtySessionID, d.RelayNonce, sess)
		}
	}

	cl.streamLocalJob(ctx, localID, res.ResultDir, d.JobID) // 终态检测 + interactions bridge（pty 输出不过日志）
	final, ok := cl.jobs.Wait(localID)
	if !ok { /* ...现有 result{failed}... */ return }

	if pumpDone != nil { <-pumpDone } // 发 Result 前 join：worker 已排空+关 pty ws（→ serve recordLoop EOF）

	if o, send := outcomeFrame(d.JobID, final); send {
		_ = cl.writeFrame(ctx, wsproto.TypeOutcome, d.JobID, o)
	}
	_ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{
		JobID: d.JobID, Status: final.Status, ExitCode: final.ExitCode, Error: final.Error,
	})
}
```
> `streamLocalJob` 对 interactive 无害：PtyRunner 不写 stdout.log（空 tail），`pumpInteractions` 对无结构化交互的 pty job 是 no-op；保留仅为终态检测统一（中3 定案）。

**验收**：
- 投影单测：interactive dispatch → 本地 `JobRequest.Interactive/Cols/Rows` 命中 pty runner（submit.go `Interactive && !remote`）。
- fail-fast：`Interactive=true` 且缺 nonce/sessionID → 直接 `Result{failed}`，不 Submit。
- pendingCancel：先 `recordPendingCancel(d.JobID)` 再 handleDispatch → 本地 job 被 Cancel（覆盖非交互 dispatch）。
- join：pumpDone 未关时不发 Result；用 fake pump 阻塞断言 Result 时序在 pumpDone 之后。
- 非交互零回归（现有 dispatch 测试绿）。

---

## T6 — host `runner/worker.Run`：interactive 三路 wait + `hostCancelGrace`（D-P2-6）

**目标**：host cancel/终态收敛为「等 `relay.Done()`（serve-drain ack）| `lostCh` | grace」再 finish，不再 Result/ctx.Done 立即返回截尾。仅 interactive；非交互字节不变。

**触碰**：
1. `internal/runner/worker/runner.go` `relayPreparer` 接口加 `Done`：
```go
type relayPreparer interface {
	Prepare(ptyrelay.RelayBinding) *ptyrelay.RelayEntry
	Done(jobID string) <-chan struct{} // T2 registry 实现
	Close(jobID, reason string)
}
```
2. `Run` 的 select（`:209-243`）interactive 分支改（非交互保持原样）：
```go
const hostCancelGrace = 10 * time.Second // 仅兜 open-stuck；pending/dial-fail 由 Done=已关 chan 立即过

	select {
	case res := <-sink.resultCh:
		relayCloseReason = "worker_result"
		if f.Interactive { // 等 serve 读完尾字节再返回（关闭权归 relay recordLoop EOF）
			select {
			case <-r.relayRegistry.Done(req.JobID):
			case <-time.After(hostCancelGrace):
			}
		}
		return runner.Result{ ExitCode: res.ExitCode, Err: errFromResult(res), Outcome: outcomeFrom(sink.takeOutcome(), workerID) }
	case err := <-sink.lostCh:
		relayCloseReason = "worker_lost"
		return runner.Result{ExitCode: -1, Err: err}
	case <-ctx.Done():
		relayCloseReason = map[bool]string{true: "ctx_timeout", false: "cancelled"}[errors.Is(ctx.Err(), context.DeadlineExceeded)]
		_ = r.hub.Cancel(workerID, req.JobID)
		if f.Interactive { // 三路 wait：serve 读完 | worker 掉线 | grace 兜底
			select {
			case <-r.relayRegistry.Done(req.JobID):
			case <-sink.lostCh:
			case <-time.After(hostCancelGrace):
			}
		}
		return runner.Result{ExitCode: -1, Err: ctx.Err()}
	}
```
> defer `relayRegistry.Close(req.JobID, relayCloseReason)`（`:160-164`）不变，退化为幂等 backstop（open 已由 recordLoop EOF 自关；pending 由此 force-close）。`hostCancelGrace` 与 stage/request timeout 偏序：文档要求 stage/request timeout > `hostCancelGrace`（否则上层抢先打断 drain 证明，见 §待办）。

**验收**：
- 单测（fake dispatcher + fake relayRegistry）：interactive 正常 Result → 等 `Done()` 关后才返回；`Done()` 为 pending(已关 chan) → 立即返回（不等 grace）。
- interactive cancel：ctx.Done → 发 Cancel → 等 `Done()` → 返回 ctx.Err()；`Done()` 卡住 → grace 后返回。
- lostCh：cancel 后 worker 掉线 → lostCh 命中收敛。
- 非交互：Result/cancel 路径**立即返回**（字节不变，G023）。

---

## T7 — e2e 矩阵 + 零回归 + 环检

**目标**：端到端验证（现有 ws-worker harness + `httptest` + Linux 真 pty），覆盖设计 §8 全 13 项。

**触碰**：新 `internal/worker/pty_e2e_test.go`（或复用 `hub_test` harness）+ 必要的 fake pty child（一个读 stdin echo stdout、收信号退出的小程序/脚本）。

**e2e 清单**（逐项断言）：
1. 正常 attach：dispatch→observer 交接→pty ws→输入 echo→输出回传 viewer→resize 生效。
2. **尾字节证明**：child 收 cancel 先输出 sentinel 再退出 → browser/relay ring 收到 sentinel **后** host job 才 finish（验 D-P2-2 + D-P2-6）。
3. cancel：host cancel→teardown→relay 读完→Done→cancelled；input pump 不误 Cancel。
4. timeout：open relay 不 EOF→走 `hostCancelGrace` 兜底。
5. **`Done` 四边界**：worker 从未拨入 / dial 失败 / nonce 校验失败(instance 不匹配) / relay 先 finalized 后 host Done——host 均立即收敛不空等 grace。
6. queued interactive：worker 并发=1，长任务占槽，interactive 排队 → `waitSession` 等到 session start 后仍成功拨入。
7. **starting 窗口断连**：observer 已起 pump、`sess.run` 未置 running 时断 pty ws → 仍 Cancel 本地 job。
8. **unmapped cancel**：host cancel 早于 `putJobMapping` → pendingCancel 命中 → 本地 job Cancel（覆盖非交互）。
9. pty ws 外部断（sess running）→ worker Cancel → failed。
10. worker 掉线 → lostCh → job failed + relay close。
11. browser 断/重连 → viewer 掉 → worker 无感 → 重连回放 Scrollback。
12. **单 reader 证明**：observer 设时无 discard、输出零丢；未设时 discard 保留。
13. chatty pty 不饿死 quiet job（专用 ws 隔离）。

**零回归清单（G023，逐条）**：local exec / worker exec / worker cancel / pending_interaction bridge / Outcome-before-Result / worker disconnect fail / chatty-quiet hub HOL(`hub_test`) / workflow step / schedule run-now / resume。

**环检**：`go list -deps ./internal/job | grep -E 'runner/pty|ptyrelay'` 为空；`internal/worker` 可含 `runner/pty`；`ptyrelay` 仍 leaf。`go vet ./...` + 三新增/改包 `-race` 绿；`GOOS=windows go build ./...` 绿。

---

## P2 验收总门
- `go build`/`vet`/`test ./...`（Linux 真 pty）全绿；`GOOS=windows go build ./...` 绿；`-race` 绿。
- `go list -deps` 环检通过（job 不 import pty/ptyrelay；ptyrelay leaf）。
- 13 项 e2e + 零回归清单全绿。
- 尾字节证明（e2e#2）+ Done 四边界（e2e#5）+ starting 窗口（e2e#7）+ unmapped cancel（e2e#8）—— 四个 codex round 定的高风险点各有专测。

## 待办（plan 内落任务，评审 round-3 低风险项）
- `pendingCancel` 对 dispatch 永不到达 / Submit 失败的 stale 残留：随 `dropJobMapping` 清或短 TTL sweep（T5/T3 落）。
- `hostCancelGrace`=10s 常量 + **stage/request timeout > `hostCancelGrace`** 偏序断言（T6 落 + 文档）。
- `RelayClosing` 态在 `Done`/`detachLocked` 加注释说明「不单列等待语义」（T2 落）。
- dial 期间 sess 暂不读的 pty buffer 上限实测；必要时本地 staging（T4 观察）。
