# P4：前端 AttachTerminal + JobDetail 接入 + pty_sessions 展示 + e2e 全矩阵 实施计划

> 上游：主设计 [`../../design/2026-07-03-web-pty-attach-design.md`](../../design/2026-07-03-web-pty-attach-design.md) v0.8（§5 架构 / §10 API/协议 / §11 安全 / §14 P4）；P2 设计 v0.4 + P3 设计 v0.3（均已收官 GREEN）。P0–P3 端到端 + 录制 + gate 已完成。
> P4 = **给已跑通的后端 attach/录制链路接上浏览器终端 UI**：xterm 双向流 + 「打开终端」入口 + pty_sessions 元数据展示 + 断线重连 + e2e 全矩阵。
> **范围（用户拍板 2026-07-05）= 完整**：标准写入闭环 + 只读跟随 + 自动重连(5min) + sessions 列表视图 + e2e 全矩阵。
> 铁律：G031 前端不引 CDN/外部资源（xterm 作 npm 依赖打进 `/assets/*` 自包含）；G022/G024 后端新增 seam 不破依赖方向（`ptyrelay` 保 leaf；pty_sessions 读走注入的窄 `PtySessionStore`）；G023 非交互路径零回归。每 T 容器 `go test ./...` + `pnpm -C web build`/`vue-tsc` 绿即提交（前缀 `feat(pty)`/`test(pty)`，Co-Authored-By，**不 push**）。

## 修订记录
| 版本 | 日期 | 说明 |
|---|---|---|
| v0.1 | 2026-07-05 | 初稿：10 T，含 P4 设计决策（D-P4-1..7）内联（主设计 §14 已批架构，P4 前端为主 + 薄后端 seam，设计折叠进本计划）。 |

---

## 关键设计决策（D-P4-x，plan 内定；主设计 §14 已批架构）

### D-P4-1 exit 语义 = 后端 best-effort `{t:x,code}` 帧 + 前端 close 后 refetch（用户拍板）
pty ws EOF（`relay.Done()`）与 worker `Result`（hub ws）在两条独立连接、无跨连接顺序（P2 D-P2-2）→ `relay.Done()` 触发时 job `ExitCode` 可能尚未落地。
- **后端**：attach WS 在 `select` 命中 `relay.Done()` 后，`s.jobs.Get(jobID)` 取当前 `Status/ExitCode`；job 已终态 → 发 `{t:"x",code:ExitCode}`；未终态（竞态，code 未知）→ 发 `{t:"x"}`（省 code）。随后 close。**不阻塞等待 worker Result**（不回归 P2 取消时序）。
- **前端**：收到 `{t:x}` 或 WS close（任意原因）→ `getJob()` 拉最终 `status/exit_code` 作**权威源**回填终端页脚（「进程已退出 · exit N」）。exit 帧仅作即时提示，权威态恒以 refetch 为准。

### D-P4-2 attach WS 服务端→浏览器 **文本控制帧**：`hello` + `exit`（新增，收敛 §10 K5）
现 attach WS 服务端仅发 **binary**（输出/scrollback），无任何文本控制帧（`attach_handler.go:105-118`）。P4 补两类 **text JSON** 控制帧（浏览器按 `typeof event.data` 区分 binary=输出 / string=控制）：
- `{t:"hello", write:bool, cols:int, rows:int}`：attach 成功后**首帧**（在 scrollback 前发）。`write` 来自实际是否拿到 lease（`AddViewer(true)` 成功=true；`ErrLeaseTaken` 降级 `AddViewer(false)`=false）→ 前端据此判「写入 / 只读跟随」，解决「write ticket 被静默降级前端不知情」。`cols/rows` 来自 `entry.Binding.{Cols,Rows}`（P3 D-P3-2 已加）供终端初始尺寸。
- `{t:"x", code?:int}`：D-P4-1 退出帧。

### D-P4-3 job 详情增 `can_attach` 计算位（后端，仅详情端点）
前端只持 bearer token、**不知自己的 caller_id**，无法判 owner；也不知 relay 是否 live、caller 是否有 `can_attach` capability。→ `GET /v1/jobs/{id}` 详情端点计算并附加 `can_attach bool`：
```go
canAttach := res.Interactive && !job.IsTerminal(res.Status) &&
    s.callerMayAttach(caller, res) && s.ptyRelayLive(res.ID) // relay Open/Attached
```
- **仅详情端点**算（需 relay lookup + caller）；list 端点不算（性能 + 无 relay 上下文）。
- 经内嵌 view 结构透出（Go 嵌入字段 JSON 提升，不改 JobResult 结构、不入库）：`type jobDetailView struct{ job.JobResult; CanAttach bool `json:"can_attach"` }`。
- 前端按钮：`interactive && running` 时显示，`can_attach` 时可点；relay 未就绪（刚 dispatch，worker 未拨入）→ `can_attach=false`，按钮置灰提示「终端未就绪」，用户可 refetch 重试。

### D-P4-4 xterm 自包含打包（G031 无 CDN）
用 `@xterm/xterm`（新版 scoped 包，非废弃 `xterm`）+ `@xterm/addon-fit`，作 **pnpm 依赖**由 vite 打进 `/assets/*.js`+内联 CSS。WS 走**同源相对路径** `/v1/jobs/{id}/attach`（无外部 host）。→ 严格 CSP / 离线均可用，不违 G031。

### D-P4-5 输入 base64 + 输出 binary（对齐 P2 已落协议）
- 浏览器→服务端 **text**：`{t:"i", d:base64(utf8(input))}`（`attach_handler.go:161` 服务端 `base64.StdEncoding.DecodeString`）；`{t:"r", cols, rows}`（clamp 由服务端做，`:168-170`）。
- 服务端→浏览器 **binary**：raw pty 输出 → `term.write(Uint8Array)`；scrollback 首个 binary blob。`ws.binaryType='arraybuffer'`。

### D-P4-6 自动重连 = 5min 窗口 + 重取 ticket + scrollback 回放（K3 生命周期）
浏览器断线 5min 内**仅 browser↔serve 段**可重连（pty 未终止）。→ AttachTerminal 在 WS 非正常关闭（未收 `{t:x}` 且 job 未终态）时，指数退避内**重取 ticket + 新建 WS**；服务端 attach 时重发 `Scrollback()`（`attach_handler.go:114`）恢复屏幕。停止重连条件：收 `{t:x}` / job 终态 / 用户关终端 / 超 5min / 连续失败超上限。

### D-P4-7 pty_sessions 展示 = 详情面板 + 列表视图（只读元数据 + 录制下载）
- `GET /v1/jobs/{id}/pty/sessions`（新，owner/admin gate 同 recording）→ 返回该 job 的 pty 会话元数据行（cols/rows/bytes_in/out/started/ended/encrypted/state/有无录制）。
- JobDetail 增「终端会话」面板（有会话行才显），每行展示尺寸/字节/时长/加密标 + `recording_uri` 非空时「下载录制」按钮（走 `GET /pty/recording`，带鉴权 fetch+blob，存 `.cast`）。
- 全局 `/sessions` 列表视图（可选路由）：跨 job 的 pty 会话概览（近期）。**不做录制回放富 UI**（设计 §3 明确不做）——仅下载。

---

## 任务依赖
```
T0(job.can_attach 计算位) ─┐
T1(attach WS hello+exit 控制帧) ─┤
T2(GET /pty/sessions 端点+ListByJob) ─┴──────────────┐
                                                     │
T3(前端基建: xterm 依赖+types+client) ─▶ T4(AttachTerminal 核心) ─▶ T5(只读+自动重连)
                                                     │                       │
                                                     └────────┬──────────────┘
                                                              ▼
                                                     T6(JobDetail 接入「打开终端」) ─▶ T7(sessions 展示+录制入口+列表视图)
                                                              │
                                                              ▼
                                                       T8(e2e 全矩阵) ─▶ T9(构建/环检/零回归)
```
> 后端 T0/T1/T2 相互独立可并行；T3 前端基建独立。T4 依赖 T1（控制帧协议）+ T3。T6 依赖 T0（can_attach）+ T4/T5。T7 依赖 T2 + T6。

进度（Wave A 后端 seams 全绿·2026-07-05·codex 实施+容器验收）：
- [x] T0 后端：job 详情 `can_attach` 计算位 — `a17702f`
- [x] T1 后端：attach WS `{t:hello,write,cols,rows}` 首帧 + `{t:x,code?}` best-effort 退出帧 — `c490307`
- [x] T2 后端：`GET /v1/jobs/{id}/pty/sessions` + jobstore `ListPtySessionsByJob` + 窄接口拓宽 — `e8b02b3`
- [x] T3 前端基建：`@xterm/xterm`+`addon-fit` 依赖 + api/types(Job.interactive/can_attach + PtySession) + api/client(attachTicket/downloadRecording/listPtySessions) + api/attach.ts 纯函数 — `(Wave B `386551e`, host pnpm build 绿)`
- [x] T4 前端：`AttachTerminal.vue` 核心（xterm+fit+ticket+WS 双向泵+resize+scrollback+hello/exit/close） — `25d32e8`
- [x] T5 前端：AttachTerminal 只读模式 + 断线自动重连（5min 窗口） — `25d32e8`
- [ ] T6 前端：JobDetail「打开终端」入口（interactive+can_attach）+ 终端 drawer + 退出 refetch
- [ ] T7 前端：pty_sessions 元数据面板 + 录制下载入口 + `/sessions` 列表视图
- [ ] T8 e2e 全矩阵（后端 Go e2e + 前端帧解析/base64 单测）
- [ ] T9 构建/环检/零回归（make web + vue-tsc + bundle 自包含 + GOOS=windows + 无 CDN 核对）

---

## T0 — 后端：job 详情 `can_attach` 计算位（D-P4-3）

**触碰** `internal/httpapi/job_handler.go`（详情端点，现 `c.JSON(http.StatusOK, res)` 约 `:160`）：
1. 新增内嵌 view + 计算：
```go
type jobDetailView struct {
	job.JobResult
	CanAttach bool `json:"can_attach"`
}
// handleJobDetail 内, 取到 res 后:
caller := callerFromCtx(c)
view := jobDetailView{JobResult: res, CanAttach: s.canAttachNow(caller, res)}
c.JSON(http.StatusOK, view)
```
2. `internal/httpapi/attach_ticket_handler.go` 抽出可复用判据 `canAttachNow(caller, res) bool`（= `res.Interactive && !job.IsTerminal(res.Status) && callerMayAttach(caller,res) && relay live`）。relay live 判定复用 `attach_ticket_handler.go:43-47` 的 `ptyRelays.Lookup` + `State ∈ {RelayOpen,RelayAttached}`；`s.ptyRelays==nil` → false。attach-ticket handler 改调此 helper（去重，行为不变）。

**验收**：
- job_handler 单测：interactive+running+owner+relay live → `can_attach:true`；非 owner → false；终态 → false；relay 未 Open（仅 pending / nil）→ false；非 interactive job → 响应无 `can_attach:true`（false）。
- **零回归**：现有 job 详情字段全在（内嵌提升，`interactive`/`caller_id`/`exit_code`/... 不变）；list 端点响应**不含** `can_attach`（未改）。
- attach-ticket 现有测试全绿（helper 抽取行为等价）。

---

## T1 — 后端：attach WS `hello` 首帧 + `{t:x,code?}` 退出帧（D-P4-1/2）

**触碰** `internal/httpapi/attach_handler.go`：
1. 服务端→浏览器 **text** 控制帧写入器（与 `writeBinary` 并列，共用 `writeMu`）：
```go
writeControl := func(v any) error {
	b, _ := json.Marshal(v)
	writeMu.Lock(); defer writeMu.Unlock()
	return conn.Write(ctx, websocket.MessageText, b)
}
```
2. `AddViewer` 后、`Scrollback` 前发 **hello**（`write` = 实际是否持 lease）：
```go
wantLease := binding.Mode != "read"
viewer, err := relay.AddViewer(wantLease)
gotLease := wantLease && err == nil
if errors.Is(err, ptyrelay.ErrLeaseTaken) { viewer, err = relay.AddViewer(false); gotLease = false }
// ... err 处理不变 ...
_ = writeControl(map[string]any{"t":"hello","write":gotLease,"cols":entry.Binding.Cols,"rows":entry.Binding.Rows})
```
3. `select` 命中 `relay.Done()` 分支：best-effort exit 帧（D-P4-1，不阻塞）：
```go
case <-relay.Done():
	if res, ok := s.jobs.Get(binding.JobID); ok && job.IsTerminal(res.Status) {
		_ = writeControl(map[string]any{"t":"x","code":res.ExitCode})
	} else {
		_ = writeControl(map[string]any{"t":"x"}) // code 未知(跨连接竞态), 前端 refetch 为准
	}
	closeWS(websocket.StatusNormalClosure, "session ended")
```
   （原 `closeWS(attachCloseNotFound,...)` 改为发帧 + 正常关；其余 `pumpDone/readDone/ctx.Done` 分支不变）
4. import 补 `internal/job`（`job.IsTerminal`）。

**验收**：
- attach_handler 单测（复用 P2 attach 骨架 + fake relay）：attach 成功首帧=`{t:hello,write:true,cols,rows}`；第二个 write viewer（lease 被占）→ `{t:hello,write:false}`（只读降级）；relay.Done 且 job 终态 → 收 `{t:x,code:N}` 后 WS 正常关；relay.Done 但 job 未终态 → 收 `{t:x}`（无 code）。
- 输出仍走 binary（hello/exit 不污染 binary 流）；scrollback 顺序 = hello 之后。
- `-race` 绿；P2 attach 现有测试零回归（新增帧不改 input/resize/断连语义）。

---

## T2 — 后端：`GET /v1/jobs/{id}/pty/sessions` + jobstore `ListPtySessionsByJob`（D-P4-7）

**触碰**：
1. `internal/jobstore/pty_sessions.go`：新增 `ListPtySessionsByJob(jobID string) ([]PtySessionRecord, error)`（按 `started_at DESC` 查该 job 全部会话，复用 `scanPtySession`）。
2. `internal/httpapi/server.go` 窄接口 `PtySessionStore`（`:87+`）加一法：
```go
ListPtySessionsByJob(jobID string) ([]jobstore.PtySessionRecord, error)
```
   （`*jobstore.Store` 自动满足；`ptySessions==nil` 时 handler 返回空列表，nil-safe）
3. 新 `internal/httpapi/pty_sessions_handler.go` + 路由（authed `/v1` 组，`server.go:400` recording 附近）：`r.GET("/jobs/{id}/pty/sessions", s.handlePtySessions)`：
```go
caller := callerFromCtx(c); res, ok := s.jobs.Get(id)
if !ok { 404 }
if !s.callerMayAttach(caller, res) { 403 }        // 同 recording gate: owner 或 admin
if s.ptySessions == nil { c.JSON(200, {"sessions":[]}); return }
rows, _ := s.ptySessions.ListPtySessionsByJob(id)
// map 到 view（PtySessionRecord 无 JSON tag → 显式 view，隐藏 InstanceID/Owner 等敏感/无用字段；
//           recording 有无用 has_recording bool 表达, 不回显 recording_uri 绝对路径）
c.JSON(200, map[string]any{"sessions": toPtySessionViews(rows)})
```
   view 字段：`pty_session_id/state/cols/rows/bytes_in/bytes_out/encrypted(1是2否→bool)/started_at/ended_at/has_recording(recording_uri!="")`。

**验收**：
- jobstore 单测：`ListPtySessionsByJob` 返回多会话按 started_at 降序；无会话→空切片非 nil。
- httpapi 单测：owner→200+会话数组（字段正确、`has_recording` 反映 uri 空/非空、不泄露绝对路径/InstanceID）；非 owner 非 admin→403；未知 job→404；`ptySessions==nil`→200 空数组。
- `-race` 绿；零回归（新端点，不动既有）。

---

## T3 — 前端基建：xterm 依赖 + types + client（D-P4-4/5）

**触碰**：
1. `web/package.json` 加 `@xterm/xterm` + `@xterm/addon-fit`（dependencies）；`pnpm -C web install` 更新 lockfile。
2. `web/src/api/types.ts`：
   - `Job` 加 `interactive?: boolean`（后端 omitempty）+ `can_attach?: boolean`（详情端点计算，list 无）+ `caller_id?: string`。
   - 新 `PtySession`：`{pty_session_id, state, cols, rows, bytes_in, bytes_out, encrypted, started_at, ended_at?, has_recording}` + `PtySessionsResp{sessions: PtySession[]}`。
   - 新 attach 控制帧类型：`AttachServerFrame = {t:'hello',write,cols,rows} | {t:'x',code?}`；`AttachClientFrame = {t:'i',d} | {t:'r',cols,rows}`。
3. `web/src/api/client.ts`：
   - `requestAttachTicket(id, mode:'write'|'read'): Promise<{ticket:string; expires_in:number}>`（`POST /v1/jobs/{id}/attach-ticket?mode=`，带 Bearer）。
   - `downloadPtyRecording(id): Promise<void>`（`GET /v1/jobs/{id}/pty/recording`，fetch+blob+authHeaders，存 `<id>.cast`，仿 `downloadArtifact` `:347`）。
   - `listPtySessions(id): Promise<PtySessionsResp>`（`request<...>('/v1/jobs/{id}/pty/sessions')`）。
4. 新 `web/src/api/attach.ts`（纯逻辑，便于单测）：
   - `encodeInput(s: string): string` = `btoa` over utf8（`new TextEncoder()` → binary string → base64）。
   - `buildAttachWsUrl(id, ticket): string`（`wss?`+`location.host`+相对 path+`?ticket=`）。
   - `parseServerFrame(data: string): AttachServerFrame | null`（JSON.parse + 判 `t`）。

**验收**：
- `pnpm -C web build` 绿（= `vue-tsc --noEmit && vite build`，xterm 打进 bundle，类型无错）。
- **无 CDN 核对**：`grep -rE "cdn|unpkg|jsdelivr|https?://[a-z]" web/dist/assets/*.js` 无外部资源引用（xterm 内联）。
- `attach.ts` **纯函数 + 显式类型签名**（`encodeInput`/`parseServerFrame`/`buildAttachWsUrl`），正确性由 T8 后端 e2e（真跑 base64/帧协议）+ 眼检背书；**前端无 vitest 框架**（现状），单测非硬门（见 T8 决策）。

> 注（现状核实）：`web` **无前端测试框架**（无 vitest / 无 `*.test.ts`），`build` script 已含 `vue-tsc --noEmit`。故 P4 前端验证 = **类型检查 + 后端 e2e 跑真协议 + agent-browser 眼检**，不新引测试栈（除非 T8 决定 bootstrap 最小 vitest，见待办）。

---

## T4 — 前端：`AttachTerminal.vue` 核心（D-P4-2/5）

**新组件** `web/src/components/AttachTerminal.vue`。props：`{ jobId: string; mode?: 'write'|'read' }`；emit：`exit(code?:number)` / `closed()` / `error(msg)`。

**核心逻辑**：
```txt
onMounted:
  term = new Terminal({convertEol:false, fontFamily:var(--font-mono), theme:{...tokens}})
  fit = new FitAddon(); term.loadAddon(fit); term.open(el); fit.fit()
  await connect()

connect():
  { ticket } = await requestAttachTicket(jobId, mode)   // 带 Bearer
  ws = new WebSocket(buildAttachWsUrl(jobId, ticket)); ws.binaryType='arraybuffer'
  ws.onmessage(ev):
    if ev.data instanceof ArrayBuffer: term.write(new Uint8Array(ev.data))   // 输出/scrollback
    else: f = parseServerFrame(ev.data)                                       // 文本控制
          f.t==='hello' → writeGranted=f.write; 若 !f.write 显示「只读跟随」; term.resize(f.cols,f.rows)
          f.t==='x'     → gotExit=true; emit('exit', f.code); (停重连, 见 T5)
  ws.onclose  → handleClose()   // T5 判重连 or 终结
  ws.onerror  → emit('error', ...)

  term.onData(s):  if writeGranted: ws.send(JSON.stringify({t:'i', d:encodeInput(s)}))
  term.onResize({cols,rows}): if writeGranted: ws.send(JSON.stringify({t:'r', cols, rows}))
  window resize → fit.fit()（防抖）→ 触发 term.onResize 发 {t:r}

onUnmounted: ws?.close(); term.dispose(); 解绑 resize
```
- **binary/text 区分**：`ev.data instanceof ArrayBuffer` = 输出；`string` = 控制帧（D-P4-5）。
- **只读**：`writeGranted=false` 时不发 `{t:i}`/`{t:r}`，`term.options.disableStdin=true` + 顶部只读横幅。
- **退出页脚**：`gotExit` 或 close → 显示「进程已退出」（exit code 由 T6 refetch 权威回填）。
- 主题：读 `tokens.css` 变量映射 xterm theme（`--term-bg`/`--paper`/`--phosphor` 等），明暗主题一致。

**验收**：
- `vue-tsc` 无错；组件挂载不报错（jsdom 或手测）。
- 手测（T9 联调）：写入 echo 回显、resize 生效、退出显示页脚。
- 单测（可选，逻辑已抽 attach.ts）：帧分发 switch 覆盖 hello/exit/binary。

---

## T5 — 前端：AttachTerminal 只读模式 + 断线自动重连（D-P4-6）

**触碰** `AttachTerminal.vue`：
1. 只读模式已在 T4 骨架（`writeGranted` + 横幅）；补：`mode==='read'` 时 ticket 请求即 `mode=read`（不抢 lease）；写模式被服务端降级（hello `write:false`）→ 横幅「他人正在操作，已只读跟随」+ 「抢占写入」按钮（重连 mode=write 试抢 lease）。
2. 自动重连状态机：
```txt
handleClose():
  if gotExit || jobTerminal || userClosed: emit('closed'); return   // 终结, 不重连
  if Date.now() - firstConnectAt > 5*60_000: 显示「会话超时(5min)」+「手动重连」; return
  if reconnectAttempts >= MAX(=6): 显示「重连失败」+「手动重连」; return
  reconnectAttempts++; 退避 sleep(min(1000*2^n, 15000)); await connect()  // 重取 ticket + 新 WS
  // 服务端 attach 重发 Scrollback → 屏幕恢复
onExit/onUserClose: 置标志停重连
```
   - `jobTerminal` 由父组件（T6）经 prop/事件告知（父在 SSE status 终态时通知），或组件内不判、仅靠 `{t:x}`+5min+MAX 兜底（倾向后者，减耦合）。
   - 重连成功清 `reconnectAttempts`。手动「重连」按钮：重置计数 + firstConnectAt 窗口 + connect。

**验收**：
- 手测（T9）：serve 重启 / 网络抖动 → 自动重连 + scrollback 回放屏幕续上；5min 后停；用户关闭不重连；写 lease 被占→只读横幅 + 抢占按钮。
- 逻辑单测（退避/停止条件可抽纯函数 `nextReconnectDelay(n)` / `shouldReconnect(state)` 便于测）。

---

## T6 — 前端：JobDetail「打开终端」入口 + 终端 drawer + 退出 refetch（D-P4-1/3）

**触碰** `web/src/views/JobDetail.vue`：
1. 头部 `head-right`（`:761`）加按钮（`showCancel` 附近）：
```vue
<button v-if="canOpenTerminal" class="term-btn mono" @click="openTerminal">打开终端</button>
```
   `const canOpenTerminal = computed(() => !!job.value?.interactive && status.value==='running' && !!job.value?.can_attach)`；
   `job.value?.interactive && running && !can_attach` → 置灰 + title「终端未就绪，稍后重试」（可点「刷新」refetch getJob）。
2. 终端 drawer（复用现有 `.drawer-overlay`/`.drawer-panel` 样式，`:1073`）挂 `<AttachTerminal :job-id="props.id" :mode="termMode" @exit="onTermExit" @closed="closeTerminal" />`。
3. 读/写切换：drawer 头部「写入 / 只读跟随」toggle（改 `termMode` 重挂组件）。
4. **退出 refetch**（D-P4-1 权威源）：
```ts
async function onTermExit(code?: number) {
	termExitCode.value = code ?? null
	try { job.value = { ...(job.value??{}), ...(await getJob(props.id)) } as Job } catch {}
	// 终端页脚显示权威 status/exit_code
}
```
5. drawer 打开时暂不影响既有 SSE 日志跟随（终端与日志并存；pty 输出**不**入 stdout.log，设计 §11，故两者不重复）。

**验收**：
- 手测（T9）：interactive+running+owner job 详情页显示「打开终端」→ 点开 drawer 终端可交互 → agent 退出后页脚显示 exit code（refetch 权威）→ 关闭 drawer 日志/头部不受影响。
- 非 interactive / 非 owner（can_attach:false）→ 无按钮 / 置灰。
- `vue-tsc` 无错。

---

## T7 — 前端：pty_sessions 元数据面板 + 录制下载 + `/sessions` 列表视图（D-P4-7）

**触碰**：
1. `JobDetail.vue`：新「终端会话」面板（仿 deliveries/timeline 面板风格 `:937`），`onMounted` 调 `listPtySessions(props.id)`（有会话行才显）：每行 `尺寸 cols×rows · 输入 X / 输出 Y 字节 · 时长 · 加密标(🔒)` + `has_recording` → 「下载录制」按钮（`downloadPtyRecording(props.id)`，loading 态防重复）。
2. 新视图 `web/src/views/Sessions.vue` + 路由 `web/src/router.ts` 加 `{path:'/sessions', name:'sessions', component: () => import('./views/Sessions.vue')}`（authed，守卫已覆盖）。列表：近期 pty 会话概览（跨 job），每行链到 `/jobs/:id`。数据源：可加 `GET /v1/pty/sessions?limit=`（可选后端小端点）或复用 job 列表过滤 interactive → 逐 job 拉（V1 倾向前者，若后端端点未做则本视图先展示「按 job 查看」引导，标注 follow-up）。
3. 主导航（`App.vue` 若有导航栏）加「Sessions」入口。

**验收**：
- 手测（T9）：录制启用的 interactive job 结束后详情页「终端会话」面板显示行 + 「下载录制」得回 `.cast`（明文/加密两路后端已处理）；未录制→无下载按钮；`/sessions` 列表可达并链回详情。
- 加密录制下载：浏览器得到解密后 asciicast（后端 D-P3-7 流式解密）。
- `vue-tsc` 无错。

> 注：若 `/sessions` 跨 job 列表需专用后端端点（超出 D-P4-7 单 job 范围），本 T 只做「单 job 面板 + 路由骨架」，跨 job 聚合端点作 follow-up issue（避免范围蔓延）。

---

## T8 — e2e 全矩阵（设计主档 §14 e2e 矩阵 P4）

**后端 Go e2e**（复用 P2/P3 attach + pty_connect harness + Linux 真 pty + `httptest`）：
1. **hello 帧**：attach → 首帧 `{t:hello,write:true,cols,rows}`；第二 write viewer → `{t:hello,write:false}`（lease 降级）。
2. **exit 帧**：agent 自然退出 → 收 `{t:x,code:0}`（job 终态）后 WS 正常关；cancel → job 终态 → `{t:x,code:N}`。
3. **can_attach**：详情端点 interactive+running+owner+relay-open → `can_attach:true`；终态/非 owner/relay-pending → false。
4. **五闸拒绝路径**：非 interactive job 请 ticket→409；非 owner 非 admin→403；worker token 请 ticket→403（`attach_ticket_handler.go:18`）；terminal job→409；relay 未 live→409。
5. **ticket 过期/重放**：ticket TTL(30s) 过期→WS 401；已消费 ticket 二次用→401（`attach_handler.go:40` 一次性 Consume）。
6. **Origin 校验**：ticket Origin 与 WS Origin 不符→401（`:50`）。
7. **resize fuzz**：越界 cols/rows（0/501/-1）不 crash、被 clamp 拒（`:168`）；合法生效。
8. **slow browser**：慢 viewer 队列满→丢刷新/断该 viewer，不阻塞 recorder（P2 K2，复用 relay 背压测试断言 recorder 不饿死）。
9. **录制下载 gate**：owner→200 内容；非 owner→403；未录/uri 空→404；加密→解密正确、header 认证失败→4xx。
10. **pty/sessions 端点**：owner→会话数组；非 owner→403。

**前端验证**（无 vitest 框架，见 T3 注）：`attach.ts` + 重连纯函数（`encodeInput`/`parseServerFrame`/`buildAttachWsUrl`/`nextReconnectDelay`/`shouldReconnect`）正确性由 **①`vue-tsc` 类型检查 ②后端 e2e#1/#2 真跑 base64+帧协议（服务端解码 = 前端 encodeInput 逆运算，端到端等价背书） ③眼检**共同背书。**决策**：默认不引 vitest（项目零前端测试栈，避免为 5 个纯函数引测试基建）；若实施时判定值得，bootstrap 最小 vitest（+`web/package.json` `test` script）单独一小步，不阻断主线。

**联调（gofer job --runner local 主机跑真链路）**：起 serve+worker，提交 interactive job，agent-browser 眼检打开终端全流程（输入/输出/resize/退出/重连/只读/下载录制）。

**验收**：以上后端 e2e 全绿（Linux 真 pty + `-race`）；前端 `vue-tsc` 绿；主机联调眼检截图存 `tmp/pty-p4/eyecheck/`。

---

## T9 — 构建 / 环检 / 零回归

1. `make web`（`pnpm install --frozen-lockfile` + `pnpm build` + embed `internal/webui/dist`）绿；`vue-tsc --noEmit` 无类型错。
2. **bundle 自包含**（G031）：`web/dist/assets` 无外部 host 引用（xterm 内联）；WS 同源相对路径。
3. 后端全量 `go build`/`go vet`/`go test ./...`（Linux 真 pty）+ `-race`（httpapi/jobstore/ptyrelay）+ `GOOS=windows build` 绿。
4. **环检**（`go list -deps`）：`ptyrelay` 仍 leaf（未动）；`job` 不 import pty/ptyrelay/castrec（未动）；新增 httpapi 端点不引入反向依赖。
5. **零回归**：非交互 job 详情/list 字段不变（`can_attach` 仅详情端点新增字段）；attach WS 新增 hello/exit 帧不改 P2 input/resize/断连/取消时序（P2 全量 attach/pty_connect 测试绿）。
6. 更新主设计 §14 P4 勾选 + 本 plan 进度 checkbox + P4 实施结果小结。

---

## P4 验收总门
- `make web` + `vue-tsc` 绿；bundle 自包含无 CDN；WS 同源相对路径。
- 后端 `go build`/`vet`/`test ./...`（真 pty）+ `-race` + `GOOS=windows build` 绿；`go list -deps` 环检 PASS（ptyrelay leaf、job 不 import pty*）。
- **功能闭环**：interactive+running+owner → 「打开终端」→ xterm 输入/输出/resize/退出全通；只读跟随 + 写 lease 降级横幅；断线 5min 自动重连 + scrollback 回放；pty_sessions 元数据面板 + 录制下载（明文/加密）；`/sessions` 列表。
- **安全 e2e 全矩阵**：五闸各拒绝路径 + ticket 过期/重放 + Origin + resize fuzz + slow browser + 录制 gate 授权 全过。
- **P2/P3 零回归**：attach/pty_connect/录制全量绿；非交互路径字节不变。

## 待办（plan 内低风险 / 实施时定）
- xterm theme 与 tokens.css 明暗主题映射的具体色值（实施时对齐 `tokens.css`）。
- 自动重连退避常量（拟 1s→15s 上限，MAX=6，窗口 5min）与 `jobTerminal` 是否经父组件下传（倾向组件内 `{t:x}`+窗口+MAX 兜底，减耦合）。
- `/sessions` 跨 job 聚合是否需专用后端端点（超 D-P4-7 单 job 范围 → 若需另起 follow-up issue，T7 只做单 job 面板 + 路由骨架）。
- 前端测试框架：**已核实 web 无 vitest / 零前端测试**（build 仅 `vue-tsc`）。默认沿用「类型检查 + 后端 e2e 跑真协议 + 眼检」，不为 P4 引测试栈；如需 bootstrap 最小 vitest 覆盖 attach.ts 纯函数，作独立小步。
- `AttachOrigins` 部署配置核对（`Governance.AttachOrigins`，attach WS Origin allowlist 需含控制台域名，运维项）。
