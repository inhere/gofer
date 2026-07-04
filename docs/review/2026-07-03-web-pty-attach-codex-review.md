# WEB-03 pty attach 设计 v0.4 — host codex 对抗式审核

> 审核方：host codex（gofer job `20260704-112138-7625070b`，`--cwd tools/gofer`，读真实代码交叉核对）。
> 被审：`docs/design/2026-07-03-web-pty-attach-design.md` v0.4。
> 结论：**需改后可进入实施计划**（方向对，3 阻断 + 4 高 + 3 中 + 遗漏项待处理）。

## 总评
worker-first 与 `PtySource` 抽象方向对，但 v0.4 低估两块硬耦合：① 现有 worker 执行链是「提交本地 job + 轮询日志文件 + 等终态」，不是直接持进程/pty 句柄；② hub 是单 WebSocket、单 reader、同步 sink 的严格顺序模型，高频 pty 字节流塞进去直接触碰 HOL/背压红线。

## 强项（判断正确处）
- worker-first 成立（避开 Windows ConPTY，契合现有 worker 本地执行模型）。
- `PtySource` 传输无关合理（serve 侧 relay 不依赖 `creack/pty`，否则 Windows serve 构建与 drop-in 被绑死）。
- pty 与 `pending_interaction` 明确分开正确（安全级别完全不同）。
- 已意识到 `wshub.readLoop` 单线程顺序 demux 与流控是最关键现实约束。

## 阻断（必须改）
1. **执行模型不是「单一改动点」**：`runner/local:52 Run`→`cmd.Run()` 无逃逸句柄；worker `dispatch.go:23` 不直接跑 runner，而是 `cl.jobs.Submit` + `streamLocalJob`(轮询本地 stdout/stderr 文件) + `jobs.Wait` + 发 Outcome/Result。pty 分支至少要动：JobRequest/Dispatch/Forward 传 interactive/size、job.Service 加 pty 会话句柄/能力接口、worker `Jobs` 接口暴露 pty read/write/resize 或新增旁路执行器、host worker runner + hub sink 加 pty 通道。→ **拆独立 ADR/plan，定义 `PtyCapableRunner`/`InteractiveExecutor` seam；别承诺 G023「一个分支自然零改写」，须列不改普通 `cmd.Run()` 的验收测试。**
2. **pty fd 生命周期与 `finish`/evict/`Wait` 未闭环，有竞态**：`finish`(execute.go:113) 终态后 evict live entry → 新 attach 查不到；relay 仍持 fd → job terminal 但浏览器仍可写；`Cancel` 只 cancel ctx 不 close master → `Wait` 卡在 ptmx read/write goroutine 或 `cmd.Wait`。→ **pty session 需自有状态机 starting/running/exiting/closed；close 顺序写死：停 input → close master/kill child(ctx) → wait child → flush output/cast → publish exit → finish → relay close；finish 不拥有 fd close 副作用；relay 只观察终态、不拥有进程生命周期。**
3. **单 hub ws 承载 pty 字节流破坏 HOL 假设，「有界缓冲+反压或丢帧」不够**：`hub.go:270 readLoop` 是连接唯一 reader、按序同步调 sink；`hub_test.go:421` 专门验证 chatty job 不饿死 quiet job（为低频 JSON 设计）。pty output 在 readLoop 写 cast/browser channel，慢浏览器/磁盘会阻塞同 worker **所有** job 的 result/cancel/answer；单 ws 上大 base64 消息造成连接级 HOL。→ **V1 二选一明确：(优)worker 为 pty 建专用 ws/stream channel、hub 只做控制平面；或复用 hub 则 readLoop 只做 O(1) 入队 per-pty bounded channel、满则按策略断开/暂停 + 两级限速(worker→serve、serve→browser)+指标+关闭语义。不留「反压或丢帧待定」。**

## 高
1. **`can_attach` 仿 `can_answer` 不够**（pty=shell 等价）：需 caller `can_attach` + agent `interactive:true` + 项目 allowed_agents + runner/worker pty capability + owner 策略；默认 require；**禁止 worker token/普通机器 token attach 人类终端**（防 worker 凭据反向拿 shell）。
2. **「白名单+二次校验 agent」仍可能退化成 web shell**：`AgentConfig` 无 interactive/no-shell 标记；只校验 `job.Agent∈白名单` 不能证明该 agent 不可执行任意命令。→ interactive agent 须显式声明**不可 raw cmd + 固定 command/args 模板 + 禁 `Cmd` 覆盖**；`interactive.allowed_agents` 单独列表（不复用普通 allowed_agents）；创建 interactive job 拒绝 `Agent=exec`。
3. **录制 secret 风险低估**：cast 含用户键入（可能粘 token/password）。→ V1 默认 cast **加密或短 TTL**；下载 gate `can_admin` 或 same caller/owner；明文随 retention 不作默认。
4. **浏览器 attach WS 的 Origin/CSRF 需单独设计**：worker WS `hub.go:151 InsecureSkipVerify:true`（非浏览器客户端），浏览器 attach **不能复用**。→ 严格 `OriginPatterns`/host allowlist + bearer；不支持 cookie-only 鉴权。

## 中
1. **`TypePTYData(direction)` demux/序列语义**：双向靠 direction 易反射/错向/乱序。→ 拆 `pty_output`/`pty_input` 两 type 更安全；需 `pty_session_id`(job_id 不够，防重连后旧帧写新会话)+`seq`+`source` 校验。
2. **worker-lost/重连是 job 级非 pty 会话级**：`onDisconnect` 对 in-flight job 处理、reconnect supersede，但 pty 断点/浏览器重连/cast 完整性/input 重放未定义。→ V1 明确 **worker-hub 断连即 pty session 终止、job failed/cancelled**，不声称复用 hub 重连续同一 pty；浏览器 5min 重连**只适用 browser-serve**，不适用 worker-serve。
3. **P1/P2 顺序不合理**：P1 堆了 wsproto+pty+runner+execute+worker+流控，但无 relay/attach handler 无法端到端验证。→ **先 P0 spike（pty executor + fake PtySource + relay 单测）**；P1 协议+relay API；P2 worker pty executor 端到端；P3 cast/审计；P4 前端。**流控/安全 gate 第一个可运行阶段就落，不后移。**

## 4 knob 推荐
- **流控**：反压为主、超限断会话，不静默丢帧；只在只读 fan-out 层可丢屏幕刷新，不丢 cast/主链路字节。
- **并发 attach**：V1 拒绝第二个可写；只读跟随分开做（且慢只读不能拖住主写者/cast）。
- **cast 加密/TTL**：默认短 TTL（24h 或 <retention）；敏感默认加密（key 来自 env/config、不进项目文件）；下载 `can_admin` 优先。
- **帧编码**：V1 base64-over-JSON 可接受（帧小/禁压缩/限速/测吞吐），协议预留 binary；高频全屏 TUI 尽早上 binary 或专用 WS。

## 遗漏项（二级问题）
- `interactive` 需持久化/查询/前端字段（不只 JobRequest），否则详情无法判断「打开终端」。
- **pty capability 广告**：worker register 现有 labels/projects/agents/maxConcurrent，无 `pty=true`/OS/build → serve 不能假设「worker 恒 OK」。
- resize 范围+频率校验（cols 1..500、rows 1..200，节流合并）。
- **attach 授权含 job 归属**：same caller 或 admin，否则有 `can_attach` 者可 attach 别人运行中 job。
- input 写入独占 **token/lease**，serve 侧抢占+断线释放原子化。
- **cast/terminal 按字节处理**，不能当 UTF-8 string；SSE 现有 `string(chunk)` 不能直接复用到 pty。
- 明确 pty output 与 stdout/stderr 日志关系（只写 cast？双写？escape 污染普通日志视图）。
- 端到端测试覆盖：cancel 时 child/ptmx 退出、worker disconnect、browser disconnect/reconnect、slow browser、chatty pty 不饿死 quiet job、resize fuzz、权限拒绝、recording 下载 gate。

---

# 第二轮：v0.5 复审（host codex，job 20260704-120031-ba4178c1）

> 逐条核销上轮 findings + 找 v0.5 新改动引入的问题。**总评：不能直接进 plan，仍需改到 v0.6。**

## 核销上轮 findings
- **closed（12 条）**：阻断3 HOL（专用 pty ws 解）、高1 多闸、高2 no-raw-cmd/拒 exec、高3 cast 加密/TTL、高4 Origin/CSRF、拆帧/session、断连语义、P0 spike+前置、resize 校验、owner 授权、input lease、字节非 string、pty output 不双写 stdout。
- **partial（5 条）**：
  - 阻断1（执行模型 seam）：提了 `InteractiveExecutor` 但**没说清如何复用 `Submit` 的 admission/SafeJoin/agent.Build/env/session/result-dir/captureOutcomes/并发门**，别手抄 job.Service。
  - 阻断2（fd 生命周期）：状态机方向对，但 `job.Service.Cancel` 只有 CancelFunc、`finish` 终态即 `delete(s.jobs)`，**无 session registry/close-hook seam**；若 interactive 仍经 host `runner/worker.Run`，cancel 发 hub.Cancel 后立即返回、host job 先 finish/evict，保证不了 worker pty 已 close/cast 已 flush。
  - interactive 持久化字段：设计写了但 JobRequest/jobs 表还没字段，pty metadata 是新表还是扩展+retention 未定。
  - pty capability 广告：`wsproto.Register`/`WorkerSnapshot` 无 capability 字段，`selectTargetWorker` 只按 label。
  - e2e 矩阵：只写了 P4 有矩阵，未列具体项。

## 新问题（v0.5 改动引入）
**阻断**
1. **InteractiveExecutor 旁路边界不成立**：绕开 `cl.jobs.Submit` 会丢 worker 侧 validate/SafeJoin/agent.Build/env/session/result-dir/captureOutcomes。→ 拆明确 seam（`job.Service.PrepareExecution`/`ExecutionPlan`，或 job 主状态机仍管 admission/result/cancel、runner 层引 `PtyRunner/PtySession`）；验收测试覆盖普通 job 零回归 + interactive 不绕过 allowlist/SafeJoin/env/session。
2. **浏览器 WS bearer 鉴权不可直接实现**：浏览器 `WebSocket` API **不能设自定义 `Authorization` header**；v0.5 又禁 cookie-only → 前端建不了连。→ 加短 TTL 一次性 **attach ticket**（authed HTTP `POST /attach-ticket` 换 ticket，WS 用 `?ticket=`/`Sec-WebSocket-Protocol`，绑 caller/job/session/origin/lease、一次性消费）。
3. **懒建 pty ws vs serve 集中录制/pre-attach scrollback 矛盾**：无 worker→serve 通道时 serve 收不到 attach 前输出；worker 不读 master 则子进程可能因 pty buffer 满卡住。→ 二选一写死：job start 即建 pty ws（serve 从首字节录制）**或** worker 本地持 ring/cast、首 attach 把 backlog 作握手前导传 serve（定大小/溢出/加密/失败语义）。
4. **专用 pty ws 建连缺一次性绑定**：只验 worker token → 任何持该 token 的进程可声明某 job/session。→ `pty-open` 带 serve 生成的一次性 **relay nonce**（绑 worker_id+instance_id+job_id+pty_session_id+attacher+expiry），pty ws 端点校验 nonce+token+hub live instance+job in-flight/interactive、原子消费 nonce。
5. **host 侧 cancel/timeout 破坏 close 顺序**：`runner/worker.Run` 在 `ctx.Done()` 发 `hub.Cancel` 后**立即返回**，`job.execute` 随即 finish/evict，不等 worker 终态/cast flush。→ interactive job 独立取消协议：host `cancelling` → 发 cancel → 等 worker `pty-exit/result` 或 bounded grace → finish。

**高**
1. **serve 侧 relay 状态机缺失**：`pty-open`/worker pty ws/browser attach/job terminal 四方竞态未闭合。→ serve relay 状态机+超时 `pending_worker→open→closing→finalized`，把 hub disconnect/worker result/browser close/pty ws close 归一到同一 CAS 关闭路径。
2. **反压与只读跟随隔离混在一起**，慢浏览器仍可能卡 agent。→ 拆两层：source→recorder/ring 主链路（bounded、超限断会话）+ viewer fan-out（每 viewer 独立 bounded queue，慢只读丢刷新或断开，不阻塞 recorder）。
3. **pty session ↔ retention/prune 未定义**：cast 短 TTL 独立于 job retention → detail 指向不存在录制；或 prune job 留敏感 cast。→ pty metadata 一等表 + FK/软关联 + retention 优先级 + prune 删除顺序。
4. **pty capability 影响调度但只说广告没说 host 怎么查**：Submit 的 worker 选择在 admission 内，判断不了默认 runner 绑定的 worker 是否 pty-capable。→ 协议加 `Capabilities`，hub registry 暴露 `PtyCapable(workerID)`，Submit interactive+worker 路径必须解析最终 worker 并校验（含默认 fallback 提前解析）。
5. **专用 pty ws binary/control framing 不精确**：→ 固定协议：binary message=raw pty bytes；text JSON=control `{type:resize|exit|error|hello}`；浏览器也优先 binary，base64 只作兼容模式。

**中**：interactive 会被 workflow/schedule/rerun/resume/md/MCP 继承——需 admission 统一校验 + 默认拒 workflow/schedule interactive；cols/rows 当前尺寸应在 session metadata（JobRequest 只初始值）；`pty-close` 定义为幂等 command 带 reason+generation；cast 加密/TTL 落成可测试 knob（`cast.retention_ttl`/`cast.encryption.enabled/key_env` + 无 key fail-fast）；普通 job 零回归验收测试补具体清单（local/worker exec、cancel、pending_interaction bridge、Outcome before Result、disconnect fail、chatty/quiet HOL、workflow step、schedule run-now、resume）。

## 5 个关键修改（进 v0.6 再 P0 spike）
1. InteractiveExecutor 如何复用 Submit 的 admission/resolve/capture（非绕开重写）
2. 浏览器 attach 改短 TTL attach ticket（非普通 Bearer WS）
3. 懒建 pty ws vs serve 集中录制 取舍
4. pty ws 建连用一次性 nonce 绑 live worker instance/job/session
5. interactive cancel/terminal 改 host worker runner 等待/ack 语义

> P0 spike 目标不只是 creack/pty 能跑，而是证明：普通 worker job 不回归、interactive job admission/cancel/finish 顺序可控、attach 前输出与 cast 归属模型可验证。

---

# 第三轮：P1 实施计划评审（host codex，job 20260704-145228-b8bc677f）

> 审 P1 计划（非设计）。**总评：需改后可。** 方向和多数锚点成立，但有 1 阻断 + 6 高 + 5 中。

## 阻断
- **P0 seam 真 bug：interactive runner 选择抢掉 worker runner**。`submit.go` 现 `if req.Interactive { run = s.runners["pty"] }` 对**任何** interactive job 生效——包括 `runner=worker`（remote 分支已构造无 Command/Args/WorkDir 的 Forward），会用本机 PtyRunner 跑一个空 remote 请求。→ 修正：`req.Interactive && !remote` 才路由本机 pty；worker interactive **保持 worker runner** + 经 Forward/Dispatch 下发 Interactive/Cols/Rows/nonce；peer interactive 拒绝；local-without-pty 由 admission capability 拒。（同一份 submit 代码在 serve 与 worker 都跑：worker 侧 handleDispatch 强制 runner=local→`!remote`→命中 pty，正确；serve 侧 worker job `remote`→保持 worker runner。）

## 高
1. **T1 漏 worker 接收端**：`internal/worker/dispatch.go handleDispatch` 把 Dispatch→本地 JobRequest，现不传 Interactive/Cols/Rows。→ 明确归 P2（worker 投影），P1 不得声称已 threading 到 worker Submit。
2. **T3 拒 workflow 表述错**：workflow submit-time 预校验 `wfID==""`，`req.WorkflowID != ""` 抓不到"来自 workflow"。→ StepSpec 不暴露/不映射 Interactive，或 workflow submit 额外拒 step interactive；schedule 覆盖 create+run-now 双测。
3. **T4 nonce 拿 instance_id 不可达**：`workerrunner.Run` 只依赖 `dispatcher` 接口（无 WorkerSnapshot/LiveInstance），WorkerSnapshot 不暴露 InstanceID。→ 给 dispatcher 加窄接口 `LiveInstance(workerID)(string,bool)` 由 Hub 实现 + fake；生成 nonce 前解析最终 workerID（含 D4 fallback）。
4. **T4 预建 ptyRelay(pending_worker) 无 registry**：`ptyrelay.Relay` 现 `New(src)` 立即绑定、只有 closed/started、无 serve 侧 session map，Server 无 relay store。→ P1 补 serve 侧 `PtyRelayRegistry`/manager（job/session→relay 状态、pending nonce、open source、attach lookup、terminal cleanup），T4/T5/T7 共同依赖。
5. **T5/T7 认证模型写清**：T7 attach ws（/v1 外）**只消费一次性 ticket、不读 bearer**，校验 ticket 绑 job/session/caller/origin；T5 worker pty-connect 仍 bearer + InsecureSkipVerify（非浏览器 OK）。
6. **T7 Origin `"*"` 禁止应 loader fail-fast** 非 handler 临时处理：config 加载拒空白/`*`/非法 `path.Match`；handler 用 OriginPatterns 不设 InsecureSkipVerify（同 host/无 Origin 库默认放行，测试覆盖）。

## 中
- T3 D4 fallback 非"return nil 旁加过滤"：`selectTargetWorker` 无 labels 直接 return nil、worker runner 运行时才用 `r.workerID`，admission 时 job 包拿不到 default worker_id。→ 显式注入 default worker 到解析阶段，或扩 `WorkerSelector` seam。**独立成 T3a**。
- T6 owner 闸 + admin fallback + require_attach 关系固定：require 开→需 can_attach 或 admin；owner=`job.CallerID==caller || can_admin`；**旧 job `CallerID==""` 只允许 admin**（防历史 allow_empty_token job 被任意 attach）。
- T8 漂移到 MCP/Web：MCP `jobView/toJobView`、Web `types.ts Job`/`SubmitJobReq` 均无 interactive。→ P1 声明范围（HTTP only），Web/MCP 投影单列（P4/独立）。
- T2 `StorageConfig.Cast` 校验不完整：定 TTL 上限常量/单位、KeyEnv 校验 env 名非空还是 value 必存、与运行环境关系。
- jobstore migration 全链：`schemaStmts`/`migrate`/`JobRecord`/`selectCols`/`scanJob`/`UpsertJob`/`toRecord`/`fromRecord`（T8 只写 list/detail 不够）。

## 切分建议（已采纳入 v0.2）
T0 前置修复(Submit 选择) → T1 拆 serve-side threading（worker 投影归 P2）→ T2 含 Origin fail-fast → T3 admission + **T3a capability 投影/解析** → T4 先 relay registry/manager 再 nonce（+dispatcher LiveInstance）→ T5/T6/T7 → T8 全 migration 链 + 范围声明。

## 遗漏项（补入各 T）
worker/dispatch.go 投影(P2)；Submit 选择修正(T0)；relay registry/manager(T4)；dispatcher 接口扩+fake(T4)；WorkerSnapshot/LiveInstance instance id(defensive copy)；D4 default worker capability/nonce(T3a/T4)；旧 worker 未报 PtyCapable 的 interactive admission 策略；jobstore migration 全链(T8)；Web/MCP 投影(范围声明)；attach ticket 旧 job CallerID=="" 策略(T6)；**close code/error mapping 固定**(T5/T7)；`go list -deps` 加 `runner` 不 import `job`、`ptyrelay` 仍 leaf 检查。

## 可行性
P1 不做端到端、`httptest.Server`+`coder/websocket.Dial` 模拟 worker pty-connect/浏览器 attach **可行**（现有 hub 测试已这么用）。真 worker 第二条 pty ws 拨入/字节泵/取消时序留 P2。
