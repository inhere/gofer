# P1：协议 + relay + 安全闸 + admission（serve 侧）实施计划

> 上游：主计划 [`../2026-07-03-web-pty-attach-plan.md`](../2026-07-03-web-pty-attach-plan.md)；设计 [`../../design/2026-07-03-web-pty-attach-design.md`](../../design/2026-07-03-web-pty-attach-design.md) v0.8；评审 [`../../review/2026-07-03-web-pty-attach-codex-review.md`](../../review/2026-07-03-web-pty-attach-codex-review.md)（第三轮=本计划评审）。
> P1 = **serve 侧可独立单测的一切** + P0 回填 + **P0 seam bug 前置修复**。worker 侧真拨入/字节泵/取消时序属 P2。
> 铁律：G023（非交互路径零行为变化）/ G022·G024（`job` 不反向 import `pty`/`ptyrelay`/`wshub`，经接口 seam）。每 T 子阶段绿灯即提交（SR1202），不 push。

## 修订记录
| 版本 | 日期 | 说明 |
|---|---|---|
| v0.1 | 2026-07-04 | 初版 8 任务。 |
| **v0.2** | 2026-07-04 | **host codex 评审后改**（1 阻断+6 高+5 中）：加 **T0 前置修复**（Submit interactive 选择抢 worker runner 的 P0 bug）；T1 拆 serve-side threading（worker 投影 `handleDispatch` 明确归 **P2**）；T3 workflow 拒绝改为 **StepSpec 不映射 Interactive**（`WorkflowID!=""` 抓不到 submit-time）；拆 **T3a**（capability 投影/解析 + D4 default worker）；T4 **先落 serve 侧 `PtyRelayRegistry`/manager + dispatcher `LiveInstance` 窄接口** 再 nonce；T2 加 **AttachOrigins + loader fail-fast**（拒 `*`）；T6 固定 owner/admin/旧 job `CallerID==""` 策略；T7 只消费 ticket 不读 bearer；T8 补 **jobstore migration 全链** + 范围声明（HTTP，Web/MCP 投影单列）；补 **close-code/error mapping**。 |

## 任务依赖
```
T0(前置:Submit选择修正) ─▶ T1(serve threading) ─┬─▶ T3(admission五闸) ─┬─▶ T6(ticket) ─▶ T7(attach ws)
T2(config+origin failfast) ─────────────────────┘   T3a(capability投影/解析)┘
T1 ─▶ T4(relay registry+nonce+LiveInstance) ─▶ T5(serve pty ws端点+remoteSource)
T1 ─▶ T8(持久化 全migration链)
```
进度：
- [x] T0 前置：Submit interactive 选择修正（不抢 worker runner）
- [x] T1 serve-side 协议/请求 threading（worker 投影归 P2）
- [x] T2 config 字段 + loader fail-fast（含 AttachOrigins）
- [x] T3 admission 五闸 + 拒 exec/workflow/schedule
- [x] T3a capability 投影 + D4 default worker 解析
- [x] T4 relay registry/manager + nonce + dispatcher LiveInstance
- [x] T5 serve 专用 pty ws 端点 + remotePtySource
- [x] T6 attach ticket 端点 + store
- [x] T7 browser attach ws + Origin + viewer/lease
- [x] T8 interactive 持久化（全 migration 链）

---

## T0 — 前置修复：Submit interactive 选择不抢 worker runner（阻断）

**目标**：修 P0 spike 埋的 bug——`submit.go:119-123` 对**任何** `req.Interactive` 都换成本机 `"pty"` runner，worker remote job 会拿到无 Command/Args 的 Forward 却跑本机 pty。

**触碰** `internal/job/submit.go:110-123`：
```go
run := s.runners[req.Runner]
if run == nil { return JobResult{}, fmt.Errorf("runner %q is not available", req.Runner) }
// WEB-03: 仅【本机执行】的 interactive job 路由本机 pty runner。worker 远端 interactive
// 保持 worker runner（forward），pty 在 worker 侧由其自己的 job.Service 选 pty（P2）。
// 同一份代码在 serve 与 worker 都跑：worker 的 handleDispatch 强制 runner=local → !remote → 命中 pty。
if req.Interactive && !remote {
    if pr := s.runners[builtinPtyRunner]; pr != nil { run = pr }
}
```
（`remote := IsRemoteRunner(cfg, req.Runner)`，`config.go:31`，含 worker + peer-http。）配套 admission（T3）拒 **peer interactive**、拒 **local interactive 且无 pty backend**。

**验收**：
- 单测（serve 侧 `runner=worker` + `Interactive=true`）：`run` 仍是 worker runner、`Forward` 正常构造、**不**被 pty 抢；`runner=local`（容器 pty 可用）+ interactive → pty runner。
- 现有测试零回归。

---

## T1 — serve-side 协议/请求 threading（P0 回填；worker 投影归 P2）

**目标**：`Interactive/Cols/Rows` 从 `JobRequest`（P0 已加）threaded 到 `runner.Request`/`Forward`/`wsproto.Dispatch` + `PtyRunner` 去硬编码。**worker 接收端 `handleDispatch` 投影明确归 P2**（本 T 不碰，故 P1 只声称「serve→frame」闭合，不声称「threading 到 worker local Submit」）。

**触碰**：
1. `internal/runner/runner.go:38-55` `Request` 加 `Interactive bool` / `Cols,Rows int`（现无，评审确认）。
2. `internal/runner/runner.go:86-99` `Forward` 加 `Interactive bool`/`Cols,Rows int`（现字段：`ProjectKey/Agent/PeerRunner/Prompt/Cmd/Cwd/TimeoutSec/WorkerID`，评审确认）。
3. `internal/job/submit.go:136` `runReq := runner.Request{JobID,WorkDir}` 补 `Interactive/Cols/Rows`（local 路径用）；`remote` 分支的 `runReq.Forward = &runner.Forward{...}` 补 `Interactive/Cols/Rows`（worker 路径用）。
4. `internal/runner/pty/runner.go:67-80` `start` 用 `req.Cols/Rows`（0 回落 80/24）替 `defaultCols/defaultRows`。
5. `internal/runner/worker/runner.go:117-126` `workerrunner.Run` 构造 `wsproto.Dispatch` 处从 `req.Forward` 补 `Interactive/Cols/Rows`（`RelayNonce` T4 填）。
6. `internal/wsproto/frames.go:35-44` `Dispatch` 加 `Interactive bool`/`Cols,Rows int`/`RelayNonce string`（均 omitempty）。
7. `internal/wshub/hub.go:418` 透传（无需改）。

**验收**：
- `PtyRunner` 单测断言初始尺寸=传入 `Cols/Rows`（如 120×40）；`Forward` 携带字段单测。
- 非交互零回归。
- **明确不含**：`internal/worker/dispatch.go handleDispatch` 的 `Interactive/Cols/Rows/RelayNonce` 投影（P2）。

---

## T2 — config 字段 + loader fail-fast（含 AttachOrigins）

**触碰**：
1. `model.go:187-203` `GovernanceConfig` 加 `RequireAttachCapability bool` + `AttachOrigins []string yaml:"attach_origins"`（attach ws 的 Origin allowlist）。
2. `model.go:300-316` `CallerConfig` 加 `CanAttach bool`；`:346-357` 后加 `CallerCanAttach`（照抄 `CallerCanAdmin`）。
3. `model.go:510-538` `AgentConfig` 加 `Interactive bool` + `NoRawCmd bool`（区别于 exec 的 `AllowRawCmd`）。
4. `model.go:481-501` `ProjectConfig` 加 `InteractiveAllowedAgents []string`（扁平，独立于 `AllowedAgents`）。
5. `model.go:405-415` `StorageConfig` 加 `Cast CastConfig`：
```go
type CastConfig struct {
    RetentionTTLHours int                  `yaml:"retention_ttl_hours"` // 默认 24；0=用默认；上限常量 castMaxTTLHours=168(7d)
    Encryption        CastEncryptionConfig `yaml:"encryption"`
}
type CastEncryptionConfig struct { Enabled bool `yaml:"enabled"`; KeyEnv string `yaml:"key_env"` }
```
6. `loader.go:302-313` 后加 fail-fast：
   - `RequireAttachCapability` 开但无 `can_attach` caller → 报错（照抄 answer/admin 段）。
   - **AttachOrigins 校验**：`RequireAttachCapability`/attach 启用时逐条拒空白/`"*"`/非法 `path.Match` pattern（`coder/websocket` 语义：不许 `*`）。
   - **cast 校验**：`Encryption.Enabled && KeyEnv==""` → 报错；`KeyEnv` 只校验**名非空**（value 在 serve start 时取，缺失时 serve 启动期再报，文档写明）；`RetentionTTLHours > castMaxTTLHours` → 报错。
7. `config_handler.go:38-44` + `:213-214` `governanceView` 补 `RequireAttachCapability` 脱敏视图。

**验收**：config 测试 + 新用例（require_attach 无 caller / cast 加密无 key / AttachOrigins 含 `*` / TTL 超上限 → 均 `Load` 报错）；`GET /v1/config` 含 `require_attach_capability`。

---

## T3 — admission 五闸 + 拒 exec/workflow/schedule

**目标**：interactive 准入红线落 `validate` 唯一 choke-point（评审确认 plain/workflow/schedule 三入口共用）。

**触碰** `internal/job/config.go:44-102` `validate`，`req.Interactive` 时追加：
```go
if req.Interactive {
    if remote && !isWorkerRunner(cfg, req.Runner) { return fmt.Errorf("interactive not supported on peer runner") } // 拒 peer
    ag := cfg.Agents[req.Agent]
    if !ag.Interactive { return Err... } // 闸②
    if !contains(proj.InteractiveAllowedAgents, req.Agent) { return Err... } // 闸③ 独立白名单
    if isExecType(ag) || !ag.NoRawCmd { return Err... } // 禁 web shell
    if len(req.Cmd) > 0 { return Err... } // 禁 Cmd 覆盖
    // 闸④ capability：见 T3a（selectTargetWorker 内解析+校验）
    // local interactive 且无 pty backend：由 T3a 的 capability 检查覆盖（serve 无 "pty" runner→拒）
}
```
**workflow 拒绝（评审修正）**：不靠 `req.WorkflowID != ""`（submit-time `wfID==""` 抓不到）。改为：
- `internal/job/workflow/types.go` 的 `StepSpec` **不新增 Interactive 字段**、`stepToRequest`（`workflow/submit.go:193-218`）**不映射** interactive（结构上无法从 step 请求 interactive）；
- 额外在 workflow submit 入口对「step 请求里带 interactive」防御性拒绝（若未来 StepSpec 加了字段）。
**schedule**：create（`validateScheduleRequest`→`jobs.Validate`）与 run-now（`jobs.Submit`）**双路径**都经 `validate`，测试覆盖两者。

**验收**：`internal/job` 单测各拒绝路径（非 interactive agent / 不在 interactive_allowed_agents / exec / Cmd 非空 / peer runner / schedule create+run-now interactive）+ 正常过；现有零回归。

---

## T3a — capability 投影 + D4 default worker 解析（闸④）

**目标**：admission-time 解析最终 worker 并校验 pty capability（含 D4 无标签 fallback），供 T4 生成 nonce。

**触碰**：
1. `internal/wsproto/frames.go:9-22` `Register` 加 `PtyCapable bool`/`OS string`。
2. `internal/worker/client.go:255-262` set `PtyCapable: ptyrunner.Available(), OS: runtime.GOOS`。
3. `internal/wshub/registry.go:252-259` `WorkerSnapshot` 加 `PtyCapable bool`（+ `InstanceID string`，供 T4 LiveInstance，注意 defensive copy 不泄露 conn）；`:266-284` 填充自 `wc.meta`。
4. `internal/job/selector.go:11-18` `WorkerCandidate` 加 `PtyCapable bool`；commands 层 hub 适配器填充（仿 Labels）。
5. `internal/job/selector.go:37-58` `selectWorker`：`req.Interactive` 时在 `hasAllLabels` 旁加 `w.PtyCapable` 过滤。
6. **D4 default worker 解析（评审重点）**：`internal/job/config.go:115-132` `selectTargetWorker` 无标签 `return nil` 分支——`req.Interactive` 时不能留白：需把 worker runner 的 default worker_id 解析出来（扩 `WorkerSelector` seam 支持「按 runner 取 default 候选 + capability」，或在此显式解析 `JobRequest.WorkerID`），并校验其 `PtyCapable`。**旧 worker 未上报 PtyCapable（false）→ interactive 拒**。

**验收**：单测（fake selector）：interactive 选中 worker 无 capability → 拒（含 D4 default 路径）；capable → 过；旧 worker(PtyCapable=false) → 拒。

---

## T4 — relay registry/manager + nonce + dispatcher LiveInstance

**目标**：serve 侧共享的 relay 生命周期管理 + 一次性 nonce（T5/T7 共同依赖）。**先 registry 后 nonce**（评审：只改 `ptyrelay.Relay` 本体不够）。

**触碰**：
1. 新 serve 侧 `PtyRelayRegistry`/manager（挂 `httpapi.Server` 或 `internal/ptyrelay`）：`job/pty_session_id → relay` 索引 + 状态 `pending_worker→open→attached→closing→finalized` + pending nonce binding + open source 绑定 + attach lookup + terminal cleanup（统一 CAS 关闭）。P0 的 `ptyrelay.Relay`(只有 closed/started) 归入其管理。
2. 新 `internal/ptyrelay/nonce.go`：`NonceStore{Issue(NonceBinding)→token(crypto/rand); Consume(nonce)→(binding,ok) 原子}`；`NonceBinding{WorkerID,InstanceID,JobID,PtySessionID,Expiry}`。
3. **dispatcher LiveInstance 窄接口（评审修正）**：`internal/runner/worker` 的 `dispatcher` 接口（现 `RegisterSink/DeregisterSink/Dispatch/Answer/Cancel`）加 `LiveInstance(workerID)(string,bool)`，由 `*wshub.Hub` 实现（读 registry 的 `instanceID`）+ 测试 fake 实现。**不让 runner/worker 直接碰 registry**。
4. 生成点 `internal/runner/worker/runner.go:117-129` 构造 Dispatch 前：`req.Forward.Interactive` 时解析最终 workerID（含 D4 default）→ `LiveInstance` 取 instance → `Issue` nonce + registry 预建 relay(`pending_worker`) → 填 `Dispatch.RelayNonce`。

**验收**：nonce 单测（Issue→Consume 一次成功/二次 false/过期 false）；registry 状态迁移单测；生成点单测（fake dispatcher.LiveInstance）：interactive dispatch 带非空 nonce + registry 有 pending relay；非 interactive 无 nonce。

---

## T5 — serve 专用 pty ws 端点 + remotePtySource

**触碰**：
1. `server.go:269-271` 旁（**/v1 外**，仿 `/v1/workers/connect`）`r.GET("/v1/workers/pty-connect", s.handlePtyConnect)`。
2. 新 `pty_connect_handler.go`（仿 `handleWorkerConnect:408-424`）：bearer→`lookupCaller`；`websocket.Accept`（`InsecureSkipVerify:true` OK——worker 非浏览器）；首帧 `{job_id,pty_session_id,relay_nonce}`；`nonceStore.Consume`→校验 `WorkerID/InstanceID` 对上 `hub.LiveInstance`、job in-flight+interactive、relay 处 `pending_worker`；通过→构造 `remotePtySource` 绑 relay(`open`)，否则 **固定 close-code** 拒（见下 close-code 表）。
3. `internal/ptyrelay/source.go` 新 `remotePtySource` 实现 `PtySource`（Read/Write=binary，Resize=text JSON `{type:"resize",cols,rows}`，K5）。

**验收**：单测（`httptest.Server`+`websocket.Dial` 模拟 worker）：有效 nonce+匹配 instance→`open`+双向字节通；nonce 重放→拒(close-code)；instance 不匹配（模拟重启）→拒；非 interactive/已终态/无 pending relay→拒；framing binary/resize 断言。

---

## T6 — attach ticket 端点 + store

**触碰**：
1. `server.go` **/v1 group 内**（需 bearer）`r.POST("/jobs/{id}/attach-ticket", s.handleAttachTicket)`。
2. 新 `attach_ticket_handler.go`：`caller := callerFromCtx`；取 job（`JobResult`，`CallerID` 评审确认存在）；`s.callerMayAttach(caller, job)`；签发短 TTL（`attachTicketTTL=30s`）一次性 ticket（`crypto/rand`，绑 `caller/job_id/pty_session_id/origin/lease_mode`）存 store。
3. `callerMayAttach(caller string, job JobResult) bool`（评审固定语义）：
```go
if s.cfg != nil && s.cfg.Governance.RequireAttachCapability && !s.cfg.CallerCanAttach(caller) && !s.cfg.CallerCanAdmin(caller) { return false }
// owner 闸：旧 job CallerID=="" 只允许 admin（防历史 allow_empty_token job 被任意 attach）
if job.CallerID == "" { return s.cfg != nil && s.cfg.CallerCanAdmin(caller) }
return job.CallerID == caller || (s.cfg != nil && s.cfg.CallerCanAdmin(caller))
```

**验收**：单测（can_attach+自己 job→发；无 can_attach(require 开)非 admin→403；attach 别人 job 非 admin→403；admin→发；旧 job CallerID=""非 admin→403；ticket 一次性+TTL 过期）。

---

## T7 — browser attach ws + Origin + viewer/lease

**触碰**：
1. `server.go:269-271` 旁（**/v1 外**，WS）`r.GET("/v1/jobs/{id}/attach", s.handleJobAttach)`。
2. 新 `attach_handler.go`：**不读 bearer**（浏览器 WS 无 Authorization header）——读 `?ticket=`→ticket store 消费+校验（绑 job/session/caller/origin）；`websocket.Accept(w,req,&websocket.AcceptOptions{OriginPatterns: s.cfg.Governance.AttachOrigins, CompressionMode: Disabled})`（**无 InsecureSkipVerify**；allowlist 已在 T2 loader fail-fast 保证合法）；经 registry 找 relay 绑 viewer；写请求抢 `input lease`（P0 语义，第二写者只读跟随）；帧 K5：client→server `{t:i,d}`/`{t:r,cols,rows}`，server→client `{t:o,d}`/`{t:x,code}`（优先 binary）。

**验收**：单测（`websocket.Dial` 带合法/非法 ticket + Origin header）：合法→attach+viewer+写者拿 lease；无/过期/已消费 ticket→拒(close-code)；非 allowlist Origin→`Accept` 拒；第二写者→只读(`ErrLeaseTaken`)；与 T5 fake worker source 联动（source 产出→viewer 收；输入(持 lease)→达 source）。

---

## T8 — interactive 持久化（全 migration 链）+ 范围声明

**范围声明**：P1 只做 **HTTP** 列表/详情可见。**Web `types.ts`/详情展示、MCP `jobView` 投影单列**（随 P4 前端或独立小任务），P1 不做。

**触碰**（评审补全全链）：
1. `internal/jobstore/jobs.go`：`schemaStmts`/`migrate()` 加 `interactive` 列（迁移幂等）；`JobRecord` 加字段；`selectCols`/`scanJob`/`UpsertJob` 同步。
2. `internal/job/persistence.go` `toRecord`/`fromRecord` 补 `Interactive`。
3. `internal/job/model.go` `JobResult` 加 `Interactive bool`。
4. `internal/job/list.go` + `httpapi` 详情 view 暴露 `interactive`。

**验收**：`jobstore`/`job` 单测（interactive job 落库后 `Get`/`List` 返 `Interactive=true`；非=false；旧库 migrate 幂等）；现有持久化/list 零回归。

---

## close-code / error mapping（T5/T7 固定，评审补）
| 拒绝原因 | WS close status | 备注 |
|---|---|---|
| nonce 无效/重放/过期 | `4401`（app policy）| pty-connect |
| instance 不匹配（worker 重启）| `4409` | pty-connect |
| job 非 interactive/已终态/无 pending relay | `4404` | pty-connect |
| ticket 无效/过期/已消费 | `4401` | attach |
| Origin 不允许 | 库层 `Accept` 直接 403（未升级）| attach |
| lease 被占（第二写者）| 不断连，降级只读 | attach |
> 具体码值 plan 落地时对齐现有 close-code 约定；关键是**固定且单测可断言**。

## P1 验收总门
- `go build`/`vet`/`test ./...`（Linux）全绿；`GOOS=windows go build ./...` 绿；三新包 `-race` 绿。
- `go list -deps`：`job` 不 import `pty`/`ptyrelay`/`wshub`；**`runner` 不 import `job`**；`ptyrelay` 仍 stdlib/leaf。
- **零回归清单**（G023 逐条）：local exec / worker exec / worker cancel / pending_interaction bridge / Outcome-before-Result / worker disconnect fail / chatty-quiet hub HOL(`hub_test`) / workflow step / schedule run-now / resume。
- 新单测覆盖：T0 选择、T1 threading、T2 fail-fast(含 origin/cast)、T3 五闸+schedule 双路径、T3a capability(含 D4/旧 worker)、T4 nonce/registry/LiveInstance、T5 端点 nonce/instance/close-code、T6 ticket 授权/一次性/旧 job、T7 origin/ticket/lease。
- **P1 不做端到端**（无真 worker 拨入，`httptest`+`websocket.Dial` 模拟）；端到端留 P2。

## P2 前瞻（非 P1）
worker 第二条 pty ws 打破单连接假设（`Client` 单 conn+writeMu+全 JSON）；`handleDispatch` 投影 Interactive/Cols/Rows/RelayNonce；worker-client→`ptyrunner.PtySession` 取字节 seam（`Client` 只持 `jobs Jobs`）；取消协议三处收敛（`recvLoop` TypeCancel / `workerrunner.Run` ctx.Done / `job.finish` 时序）单独一节画时序 + ack 帧。

---

## P1 完成记录（2026-07-04）
- **10 任务全绿**：T0 `4123243` / T1 `ca75956` / T2 `30238e7` / T3 `f0e88c7` / T3a `a6ecd56` / T4 `129dc1e` / T5 `f2d0a91` / T6 `eecd884` / T7 `90fb259` / T8 `3816fd1`。
- **代码审查**（host codex 对抗式，读真实 diff）：核心结构核查无问题（nonce 原子/背压/lease/Origin/五闸/T8 对齐/G024/P1-P2 边界）；挖出 2 阻断+2 高+2 中。
- **审查修复** `ac4e807`：F1 relay 三终态统一 Close / F2 attach-ticket 校验 live interactive relay+绑 session / F3 禁 worker token attach / F4 registry Close 移除全索引 / F5 resize clamp / F6 store 过期 sweep。中3 cast key env 留 P3。
- **验收**：全量 `go test ./...` ALL GREEN + `GOOS=windows build` OK + G024 无环。**未 push**（独立仓，按约定不 push）。
- **P2 前瞻**（未做）：worker 第二条 pty ws 拨出 + `handleDispatch` 投影 + worker-client→ptyrunner seam + 取消协议三处收敛。
