# WEB-03 浏览器 pty 交互（attach 交互式 agent）设计

> Web 里经 pty（伪终端）双向流直连**正在运行的交互式 agent CLI**（xterm.js input/output/resize），区别于结构化 `pending_interaction`（单问单答）。定位 **attach 已知交互式 agent**，**不做通用 web shell**（防后门）。
> 架构基线（Q0 已定）：`gofer serve` 跑主机 **Windows**；交互式 agent 全在 **Linux worker** 执行。→ V1 pty 建在 Linux worker、serve 纯中继；传输无关（serve 本机 pty 为 **跨平台 drop-in**：`internal/pty` unix=creack/pty、windows=vendored conpty，见 v0.7/§13）。
> pty 字节流走 worker↔serve **专用 ws 通道**（非复用 hub 控制平面）；interactive job **起即拨出**该通道（eager）。
> 经 host codex 两轮对抗式审核（`docs/review/2026-07-03-web-pty-attach-codex-review.md`）。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1–v0.4 | 2026-07-03 | Claude | local-first→worker-first(Q0)→W1 测绘→传输无关(`PtySource`)。 |
| v0.5 | 2026-07-03 | Claude | codex 一轮后：专用 pty ws（解 HOL）+ `InteractiveExecutor` 旁路 + 会话状态机 + 多闸安全 + P0 spike。 |
| **v0.6** | 2026-07-03 | Claude | **codex 二轮后大改**（5 新阻断 + 5 新高）：① 执行模型**不再旁路 Submit**，改 runner 层 `PtyRunner` + `PtySession` 注册表（复用 Submit 全部 admission/SafeJoin/env/session/result-dir，解阻断1）；② 浏览器 attach 改**短 TTL 一次性 attach ticket**（浏览器 WS 不能设 Authorization header，解阻断2）；③ pty ws **eager 起即拨**（serve 从首字节录制 + ring buffer，解阻断3 懒建矛盾）；④ dispatch 帧带 **relay nonce**（绑 worker_id+instance_id+job_id+session_id+expiry，pty ws 端点原子消费，解阻断4）；⑤ interactive **独立取消协议**（host `cancelling`→等 worker ack/grace→finish，解阻断5）；⑥ serve 侧 **relay 状态机 + 统一 CAS 关闭**；⑦ 背压**拆两层**（recorder 主链路 / viewer fan-out）；⑧ pty metadata **一等表** + retention 顺序；⑨ capability **入调度**（Submit 预解析 worker 校验）；⑩ framing 精确（binary bytes / text JSON control）。 |
| **v0.7** | 2026-07-03 | Claude | **pty 后端跨平台化**（用户输入）：`internal/pty` 抽平台无关接口，unix=`creack/pty` **升 v1.1.24（最新）**，windows=**vendored `UserExistsError/conpty` 单文件**（`conpty.go` ~350L，MIT，仅依赖 `golang.org/x/sys/windows`（已有）+ stdlib，API 全备：`IsConPtyAvailable`/`Start(cmdline,opts)`/`Resize`/`Wait(ctx)→exit`/`io.ReadWriteCloser`/`Pid`）→ **解除 v0.6「Windows serve 只能纯中继」限制**：`localPtySource` 变**跨平台 drop-in**（unix+windows），pty capability 不再 OS-gated（=构建含 pty 后端即可）。V1 仍 worker-first、serve-local 仍 drop-in（现跨平台）。注意 windows 侧 `conpty.Start` 收 commandLine 字符串（vs creack 收 `*exec.Cmd`）→ 接口内做 (command,args)→安全引用 的平台适配。 |
| **v0.8** | 2026-07-04 | Claude | **P0 spike 完成（commit `7226251`，容器 Linux，3 证明点全成立，`build/vet/test`+`-race`+`GOOS=windows build` 全绿）**，回填 6 发现：① **验证**：`PtySession`↔`jobEntry`/`finish` 解耦干净（session 注册表在 `PtyRunner` 内、`jobEntry` 不持 pty 句柄、`finish` 只经 `run.Run` 返回值观察终态、`job` 不 import 任何 pty 包）；cancel 有序 teardown 可落地（Run 阻塞穿过 wait-child 才返回，证伪「发信号即返回」）；ring/两层背压/lease 成立；conpty vendored 无编译坑。② **P1 必补**（spike gap）：`runner.Request` **加 `Interactive/Cols/Rows`**（现无该字段→初始尺寸硬编码 80×24 送不到 PtyRunner）。③ `Pty` 接口若要**分级 kill**（SIGHUP 宽限→SIGKILL）需补显式 `Signal/Kill`（当前 unix `Close()` 是 ptmx.Close+Process.Kill 原子一步）。④ `Pty.Wait(ctx)` 的 ctx 在 unix 侧近 vestigial（靠 Close→kill 让 cmd.Wait 返回），P1 复用需收窄/注意。⑤ viewer 队列深度应 **per-viewer 可配**（写租户深/只读跟随浅），非全局单值（spike 加了 `AddViewerWithQueue` 佐证）。⑥ 跨进程「host cancelling→等 worker ack/grace」仍需 P1 在 worker↔serve 帧层落地（进程内有序 close+bounded grace 已证）。 |

## 1. 概览

Web 里对交互式 REPL/CLI agent（claude/codex 交互模式）开真终端：键入即达 stdin、输出实时回显、尺寸同步。

- **≠ 日志 SSE**（WEB-08）：单向只读、`string(chunk)` 文本流。pty 是**双向 raw byte 流**（含 ANSI，不当 UTF-8 string）。
- **≠ pending_interaction**（WEB-09）：结构化单问单答经 HTTP/store 轮询，不碰 OS stdin。pty 直连 ptmx。

**架构一句话**：serve 派发 interactive job 时生成 **relay nonce** 随 dispatch 下发；Linux worker 起 job 即用 `PtyRunner` 挂 ptmx 运行、**并立即用 nonce 拨出专用 pty ws** 到 serve；serve 从首字节 `ptyRelay` 录制 + ring buffer；浏览器先换 **attach ticket** 再建 attach ws，relay 中继双向字节。

## 2. 名词

| 名词 | 含义 |
|---|---|
| pty / ptmx | 伪终端主从对；agent 挂 pts，worker 侧持 master fd |
| **PtyRunner** | worker 侧 runner 层的 pty 执行变体（**不旁路 `Submit`**）：`Run` 起 `pty.Start`、注册 `PtySession`、阻塞等子进程；interactive job 经**正常 admission** 后由 runner 选择命中 |
| PtySession | worker 侧一次 pty 执行的句柄 + 状态机；注册进 session 注册表（对齐 `jobEntry` 生命周期） |
| pty ws（专用） | pty 会话专属 worker→serve ws，只承载 raw 字节 + resize/exit control，per-session、**eager 起即拨**、隔离于 hub |
| relay nonce | serve 派发 interactive job 时生成的一次性凭据（绑 worker_id+instance_id+job_id+pty_session_id+expiry），worker 拨 pty ws 时提交、serve 原子消费 |
| ptyRelay | serve 侧每会话中继 + 状态机；消费 `PtySource`，recorder 主链路 + viewer fan-out（两层背压） |
| PtySource | `{Read/Write/Resize/Close}`：`remotePtySource`（worker 经专用 pty ws，V1）/ `localPtySource`（serve 本机 ptmx，unix drop-in） |
| **attach ticket** | 浏览器换取的短 TTL 一次性凭据（绑 caller/job/session/origin/lease 模式），WS 用 `?ticket=`/`Sec-WebSocket-Protocol` 提交（浏览器 WS 无法设 Authorization header） |
| interactive agent | 配置 `interactive:true` + `no-raw-cmd` + 固定 command/args，独立 `interactive.allowed_agents` |
| cast | 会话录制（asciinema 风格 raw byte），serve 集中落盘，默认加密或短 TTL |

## 3. 范围

**做（V1）**：worker `PtyRunner`+`PtySession` 状态机（Linux）；worker→serve 专用 pty ws（eager+nonce）；serve `ptyRelay` 状态机（recorder+viewer 两层背压）+ attach ticket + attach ws；多闸鉴权+Origin+lease；cast（加密/短TTL）+ pty metadata 一等表；worker pty capability 广告+调度校验；前端 xterm.js。

**drop-in（V1 不实现）**：serve 本机 pty（`localPtySource`，unix serve）。

**不做**：通用 web shell；多浏览器并发**写**（V1 独占写）；录制回放富 UI；worker 断连续同一会话（断连即终止）；**workflow/schedule 的 interactive job**（admission 默认拒，防无人值守开终端）。

> serve 本机 pty（`localPtySource`）不在 V1 但**设计内建为跨平台 drop-in**（unix creack/pty + windows vendored conpty，v0.7）；当前 serve=Windows + agent 在 worker 场景 V1 不触发它。

## 4. 已确认（决策）

| 编号 | 决策 | 结论 |
|---|---|---|
| Q0 | serve OS + agent 位置 | serve=Windows / agent=Linux worker |
| D1 | 执行模型 | 复用 job + `interactive:true`；worker 经 **runner 层 `PtyRunner`**（不旁路 `Submit`，admission 全复用） |
| D2 | 首期范围 | worker pty 为 V1；serve-local drop-in |
| D3 | 审计录制 | V1 即录 cast + 元数据；默认加密或短 TTL |
| D4 | attach 准入 | 严格白名单（`interactive:true`+no-raw-cmd）+ 五闸 + owner |
| K1 | pty 传输 | 专用 worker→serve pty ws（hub 不承载字节流） |
| **K6** | **pty ws 建连时机** | **eager：interactive job 起即拨**（serve 从首字节录制 + ring buffer，首 attach 回放尾部）；relay nonce 随 dispatch 下发 |
| K2 | 流控 | 反压为主、超限断会话；**拆两层**（recorder 主链路 bounded / viewer 各自 bounded，慢只读丢刷新不阻塞 recorder） |
| K3 | 并发 attach | 拒绝第二个可写（独占 lease）；只读跟随分开 |
| K4 | cast | 默认短 TTL（24h/<retention）+ 敏感加密（`cast.encryption.key_env`）；**无 key + 超阈值 TTL → serve fail-fast**；下载 `can_admin` 优先 |
| K5 | framing | 专用通道：binary message=raw pty bytes；text JSON=control `{type:resize\|exit\|error\|hello}`；浏览器优先 binary、base64 仅兼容 |

**生命周期**：pty 会话随 agent 退出终止；serve/worker idle 15min + 最大 2h；**worker↔serve 断连即会话终止 + job failed**；浏览器断线 5min 可重连**仅 browser↔serve 段**（pty 未终止时）。

## 5. 架构

```txt
┌─ Browser ─────┐              ┌─────────────── serve ───────────────┐   hub ws(控制)   ┌── Linux worker ────────┐
│ 1. POST attach│──ticket req─▶│ POST /v1/jobs/{id}/attach-ticket     │                  │                        │
│    -ticket    │◀──ticket─────│  (authed: bearer+can_attach+owner)   │                  │                        │
│ 2. WS ?ticket=│──────────────▶ GET /v1/jobs/{id}/attach             │                  │                        │
│ AttachTerminal│  (Origin +   │  ├ 消费 ticket + lease               │                  │                        │
│  xterm(binary)│   ticket)    │  └ 绑 ptyRelay(session)              │                  │ PtyRunner:             │
│  in/out/resize│◀════════════▶│ ptyRelay(状态机) ⇄ PtySource ────────┼◀═ 专用 pty ws ══▶│  pty.Start→ptmx        │
└───────────────┘              │  ├ recorder 主链路→cast(加密)+ring   │  (worker eager   │  PtySession 状态机     │
                               │  └ viewer fan-out(各自 bounded)      │   拨出, nonce)   │  ptmx.Read/Write/Setsize│
   serve 派发 interactive job  │ dispatch(+relay nonce) ──────────────┼─────────────────▶│ (job start 即拨 pty ws)│
   → 生成 nonce + 预建 relay    │ cancel(interactive: 等 ack/grace) ───┼─────────────────▶│ 取消协议               │
   (pending_worker)            └──────────────────────────────────────┘                  └────────────────────────┘
```

**执行模型（解阻断1，不旁路 Submit）**：interactive job 经 `handleDispatch → cl.jobs.Submit` **正常 admission**（validate/SafeJoin/agent.Build/env/session/result-dir/captureOutcomes/并发门全复用）；`job.execute` 选 runner 时，`interactive && unix` → 命中 **`PtyRunner`**（而非 `local.Runner`）。`PtyRunner.Run` 用 `pty.Start(cmd)` 起 agent、把 `PtySession`（master fd + 状态机）注册进 **session 注册表**（与 `jobEntry` 并列、对齐 `entry.cancel`），阻塞等子进程退出。**普通 job 走原 `local.Runner` 零改动**（G023，验收测试见 §14）。

**pty ws（K6 eager + nonce，解阻断3/4）**：serve 派发 interactive job 时生成 relay nonce（含入 dispatch 帧）并预建 `ptyRelay`（`pending_worker`）。worker 起 job 即拨出专用 pty ws，握手提交 nonce + worker token；serve 端点校验 nonce + token + hub live instance + job in-flight/interactive、**原子消费 nonce** → relay `open`，从首字节录制 + ring buffer。

**serve relay 状态机（解新高1）**：`pending_worker → open → attached → closing → finalized`（带超时）。hub worker disconnect / worker result / browser close / pty ws close / cancel 五个关闭源**归一到同一 CAS 关闭路径**，幂等。

**取消协议（解阻断5）**：interactive job cancel → host job `cancelling` → 发 hub cancel → **等 worker `pty-exit`/result 或 bounded grace**（不像现有 `runner/worker.Run` 立即返回）→ 再 `finish`；close 顺序：停 input → close master/kill child → wait child → flush cast → publish exit → 关 pty ws → finish。`finish` **不拥有 fd close 副作用**；relay 只观察终态。

## 6. 模块 / 关键机制

| 模块 | 职责 | 触碰 |
|---|---|---|
| `internal/pty`（新，平台抽象） | 接口 `Pty{io.ReadWriteCloser; Resize(cols,rows); Wait(ctx)→exit}` + `Start(Spec{Command,Args,Env,Dir,Cols,Rows})`；`pty_unix.go`=creack/pty(v1.1.24)（`pty.Start(exec.Cmd)`），`pty_windows.go`=vendored conpty（(command,args)→安全引用的 commandLine + `ConPtyDimensions/WorkDir/Env` opts）；`IsAvailable()` 特性探测 | 新包（unix+windows 两实现） |
| `internal/pty/conpty`（vendored） | `UserExistsError/conpty` 单文件 `conpty.go`（MIT，保留 LICENSE+attribution），供 windows 实现 | 拷入维护 |
| `internal/runner/pty`（新，unix）`PtyRunner` | runner 层 pty 变体：`pty.Start` + 注册 `PtySession` + 阻塞等子进程；**与 `local.Runner` 并列，`job.execute` 按 interactive 选择** | 新 runner + `execute` 选择点 |
| `PtySession` + session 注册表（worker/job 侧） | master fd + 状态机 `starting→running→exiting→closed` + close 顺序；对齐 `entry.cancel`；`finish` 不 close fd | 新，`job.Service` 加 seam |
| `internal/job` admission | `JobRequest.Interactive`+`cols/rows`（**持久化+查询+详情**）；**admission 统一校验**（非仅 HTTP handler）：pty-capable 目标（预解析 worker+capability）、`agent∈interactive.allowed_agents`、no-raw-cmd、**拒 workflow/schedule/exec interactive** | 加字段+校验 |
| `internal/job` cancel/finish | interactive **独立取消协议**（cancelling+等 ack/grace）；`finish` 不 close fd；经接口 seam（`PtyCapableRunner`，job 不反向 import pty，G024） | 改语义+接口 |
| `internal/wsproto` | dispatch 帧加 `relay_nonce`（interactive）；`Register` 加 `Capabilities{pty,os}`；专用 pty ws 协议（binary bytes + text JSON control，K5）；`pty-close` 幂等 command（reason+generation） | 加字段/协议 |
| `internal/wshub` | `WorkerSnapshot` 加 capability；`PtyCapable(workerID)` 供 Submit 预解析；worker disconnect 归入 relay CAS 关闭 | 加字段/方法 |
| serve 专用 pty ws 端点（新，worker→serve） | 接 worker 拨出流；**校验+原子消费 relay nonce** + token + live instance；绑 `ptyRelay` | 新 handler/路由 |
| `internal/ptyrelay`（serve，新） | `ptyRelay` **状态机** + `PtySource`(remote/local) + **recorder 主链路**(bounded, 超限断会话) + **viewer fan-out**(各自 bounded, 慢只读丢刷新/断开, 不阻塞 recorder) + input lease + ring buffer + cast + idle/max + 统一 CAS 关闭 | 新包 |
| `internal/httpapi` attach | `POST /v1/jobs/{id}/attach-ticket`（authed，签发短 TTL 一次性 ticket）+ `GET .../attach` ws（**消费 ticket** + Origin allowlist + lease）；`GET .../pty/recording`（gate） | 新文件+路由 |
| `internal/config` | `RequireAttachCapability`+caller `CanAttach`+agent `Interactive`/`NoRawCmd`/固定模板+`interactive.allowed_agents`+`cast.retention_ttl`/`cast.encryption.enabled/key_env`（无 key+超阈值 fail-fast）+loader 三段式 | 加字段 |
| pty metadata（jobstore） | **一等表 `pty_sessions`**（session_id/job_id/worker_id/caller/owner/state/cols/rows/recording_uri/bytes/时间）；FK/软关联 jobs；retention 优先级 + prune 删 cast 顺序 | 新表 |
| `web/src` | `AttachTerminal.vue`（xterm binary + fit + ticket 换取 + ws）、JobDetail「打开终端」（interactive+can_attach+owner）、路由 | 新组件 |

## 7. 关键流程（时序）

```txt
用户    Web           serve(admission+relay)           hub      worker(PtyRunner)          agent
 │ 提交 interactive:true
 │─▶ POST /v1/jobs ─▶ admission: 校验+预解析worker+capability+拒exec/workflow
 │                    生成 relay nonce + 预建 ptyRelay(pending_worker)
 │                    dispatch(+nonce) ─────────────▶ 收派发: Submit(全admission) → PtyRunner
 │                                                    pty.Start→ptmx(session=running)
 │                                                    eager 拨出 pty ws(提交 nonce+token)
 │                    pty ws 端点: 校验+原子消费 nonce+token+live instance ✔ → relay open
 │                    从首字节 recorder→cast+ring
 │ 点「打开终端」
 │─▶ POST attach-ticket (bearer+can_attach+owner) ─▶ 签发短TTL一次性 ticket
 │─▶ WS attach ?ticket= (Origin allowlist) ─────────▶ 消费 ticket + 抢 input lease → relay attached
 │                    首 attach 回放 ring 尾部
 │ 键入 ─▶ input ─▶ relay(持lease) ═ pty ws(binary) ═▶ ptmx.Write ─▶ stdin
 │            ◀── viewer fan-out ◀═ pty ws(binary) ◀═ ptmx.Read ─ stdout  (recorder 主链路→cast)
 │ resize ─▶ ═ pty ws(json ctrl) ═▶ Setsize(校验+节流); session metadata 更新 current size
 │       agent 退出/cancel(cancelling+等ack/grace) → 状态机 exiting: 停input→close master→wait→flush cast→exit→关 ws→finish
 │ ◀── attach ws close(exit)                        relay: 五关闭源归一 CAS → finalized
```

## 8. 数据模型

- **JobRequest**：`interactive bool`、`cols/rows int`（**初始**值，默认 80×24）。admission 校验 pty-capable + interactive 白名单 + no-raw-cmd + 拒 exec/workflow/schedule。`interactive` 落库 + 列表/详情可见。
- **`pty_sessions`（一等表）**：`pty_session_id`(PK)/`job_id`(FK)/`worker_id`/`instance_id`/`caller_id(attacher)`/`owner`/`state`/`cols/rows`(**current**)/`recording_uri`/`bytes_in/out`/`started_at`/`ended_at`。retention 优先级明确、prune 时先删 cast 再删行。
- **cast**：serve 侧 `<result_dir>/<job_id>/pty.cast`（asciinema v2，raw byte）；`cast.retention_ttl`（默认 24h/<retention）+ `cast.encryption`（key from `key_env`）；无 key + 超阈值 TTL → serve fail-fast。`recording_uri` 失效时 detail 明示。

## 9. 中间件 / 存储

无新 MQ/Redis。ptmx（worker）/PtySession/ptyRelay/pty ws 均**进程内 live-only**，重启即断（与 interaction 对已驱逐 job 409 一致）。`pty_sessions` 元数据入 jobstore SQLite；cast 入 serve 文件（加密 + retention prune）。

## 10. API / 协议

| 通道 | 端点/帧 | 说明 |
|---|---|---|
| HTTP | `POST /v1/jobs` | `interactive:true`+`cols/rows`；admission 校验（pty-capable+白名单+no-raw-cmd+拒 exec/workflow/schedule）否则 400 |
| HTTP | `POST /v1/jobs/{id}/attach-ticket` | authed（bearer+can_attach+owner）→ 短 TTL 一次性 ticket（绑 caller/job/session/origin/lease 模式） |
| **WS（browser→serve）** | `GET /v1/jobs/{id}/attach?ticket=` | **消费 ticket**（不用 Authorization header）+ Origin allowlist（禁 `InsecureSkipVerify`/cookie-only）+ 抢 lease；帧 `{t:i,d}`/`{t:r,cols,rows}`→server、`{t:o,d}`/`{t:x,code}`→client（优先 binary） |
| **WS（worker→serve，专用）** | pty 会话流端点 | 握手带 `job_id+pty_session_id+relay_nonce`+worker token；serve 校验+原子消费 nonce+live instance；binary bytes + text JSON control（K5） |
| hub 控制帧 | dispatch(+`relay_nonce`) / cancel / `pty-close`(幂等,reason+generation) | 不承载字节流 |
| HTTP | `GET /v1/jobs/{id}/pty/recording` | `can_admin` 优先或 same owner/caller + 审计事件 |

## 11. 安全（**重点**）

- **五闸鉴权**：① caller `can_attach`（默认 require，fail-fast 三段式）② agent `interactive:true` ③ 项目 `interactive.allowed_agents`（**独立**于普通 allowed_agents）④ worker/runner **pty capability**（Submit **预解析最终 worker + 校验**，含默认 fallback，不留到 runner.Run 才 fail）⑤ **owner 授权**（attacher=submitter/caller 或 admin，否则拒——防 attach 别人 job）。
- **attach ticket（解阻断2）**：浏览器 WS 不能设 Authorization header → authed HTTP 换短 TTL 一次性 ticket（绑 caller/job/session/origin/lease），WS `?ticket=`/`Sec-WebSocket-Protocol` 提交、一次性消费；query token 短 TTL 一次性以限日志泄露面。
- **relay nonce（解阻断4）**：pty ws 建连非仅 worker token；nonce 绑 worker_id+instance_id+job_id+session_id+expiry，serve 原子消费 + 校验 hub live instance + job in-flight/interactive。
- **禁 web shell**：interactive agent 必须 no-raw-cmd + 固定 command/args + 禁 `Cmd` 覆盖；**拒 `Agent=exec`**；**拒 workflow/schedule interactive**（admission 层，防无人值守开终端）。
- **禁 worker token attach 人类终端**：attach ws 只接受人类 caller 的 ticket，不接受 worker token。
- **Origin/CSRF**：严格 `OriginPatterns`/host allowlist；不复用 worker ws `InsecureSkipVerify:true`；不支持 cookie-only。
- **input lease（独占写）**：原子 lease，抢占/断线释放原子；K3 拒第二可写，只读跟随不持 lease。
- **流控两层（K2，解新高2）**：`source→recorder/ring` 主链路 bounded、超限**断会话**（不丢主链路/cast 字节）；`viewer fan-out` 每 viewer 独立 bounded queue，慢只读丢屏幕刷新或断开、**不阻塞 recorder**；写 lease viewer 超限断该浏览器不阻塞 recorder。专用通道隔离 → 慢会话不影响 hub 控制平面/其他 job。
- **cast（K4）**：默认加密或短 TTL；无 key+超阈值 fail-fast；下载 `can_admin` 优先。
- **resize**：范围 cols 1..500 / rows 1..200 + 频率节流合并。
- **字节流**：全程 raw bytes（现 SSE `string(chunk)` 不复用）。
- **日志边界**：pty output **只入 cast/attach**，不双写 stdout.log（避免 ANSI 污染日志/SSE 视图）；detail 明示「交互会话见录制」。

## 12. 待确认（plan 内细化，非阻断）

- ring buffer 大小/溢出策略（eager 下 serve 侧 ring；attach 回放尾部行数）。
- cast 加密算法 / key 轮换细节。
- 只读跟随最大 viewer 数。
- `PtySession` 与 sweeper/retention 的具体交互（interactive job 无文件轮询产物，需确认 sweeper 不误判）。
- attach ticket TTL 具体值（拟 30s）与并发签发上限。

## 13. 部署

- pty 后端**跨平台**：unix 依赖 `github.com/creack/pty` **v1.1.24**（当前 go.sum 仅 v1.1.9 传递项，提为直接依赖并升级）；windows **vendored** `UserExistsError/conpty` 单文件 `conpty.go`（MIT，拷入 `internal/pty/conpty/`、保留 LICENSE，便于自维护修问题）——仅依赖 `golang.org/x/sys/windows`（已有）+ stdlib，**不引第三方 module**。`internal/pty` 平台文件（`pty_unix.go`/`pty_windows.go`）各编各的；`internal/runner/pty` 的 `PtyRunner` 走接口无 OS 分支。
- pty capability **不再 OS-gated**：任何含 pty 后端的构建（unix 或 windows）即 pty-capable；serve 本机 pty（`localPtySource`）成**跨平台 drop-in**，Windows serve 亦可（Win10 1809+，conpty `IsConPtyAvailable` 运行期探测）。
- serve（当前 Windows）relay/ticket/pty ws 端点/录制跨平台无碍。V1 走 worker(Linux) 路径，serve-local drop-in 不触发。
- **依赖方向（G022/G024）**：`internal/pty`/`internal/runner/pty`/`internal/ptyrelay` 为数据/编排层，`job` 经接口 seam（`PtyCapableRunner`/`PtySession`，仿 `WorkflowAdvancer`/`JobOps` 倒置）不反向 import；新增后 `go build`/`vet`/`list -deps` 验环。
- 前端 `xterm`+`xterm-addon-fit`；`make web` 重 embed + 重建二进制。

## 14. 结论 + 阶段

三大改造点：① **`PtyRunner` runner 层变体**（复用 Submit admission，普通 job 零回归）② **专用 pty ws（eager+nonce）+ 双侧状态机 + 统一 CAS 关闭 + 独立取消协议** ③ **五闸安全 + attach ticket + 两层背压**。

**阶段**（P0 spike 目标：证明**普通 job 不回归 + interactive admission/cancel/finish 顺序可控 + attach 前输出/cast 归属可验证**）：
- **P0 spike**：`internal/pty` ptmx + `PtyRunner` 骨架 + `PtySession` 状态机 + **fake `PtySource`** + `ptyRelay` 状态机/两层背压/lease 单测；**跑通「普通 worker job 零行为变化」+「interactive cancel 走 cancelling→ack→finish 顺序」+「pre-attach 输出入 ring→首 attach 回放」三个证明点**（不接真浏览器/真 worker 网络）。
- **P1 协议+relay+安全闸**：dispatch nonce + 专用 pty ws 端点（nonce 原子消费）+ `ptyRelay` remote source + attach-ticket + attach ws（Origin+lease）+ admission 五闸校验 + capability 预解析 + config 字段。
  - **P0 回填必做**：`runner.Request` 加 `Interactive/Cols/Rows`（把初始尺寸 threaded 到 `PtyRunner`，去掉 spike 的 80×24 硬编码）；viewer 队列深度 **per-viewer 可配**（写租户深/只读浅）；跨进程 cancel「host cancelling→等 worker ack/grace」在 worker↔serve 帧层落地（进程内有序 close 已由 P0 证）；如需分级 kill 给 `Pty` 补 `Signal/Kill`。
- **P2 worker 端到端**：`PtyRunner` 接真 ptmx + eager 拨 pty ws + capability 广告 + 取消协议；input/output/resize/cancel/断连全链路。→ **细化见 [`2026-07-04-web-pty-attach-P2-design.md`](2026-07-04-web-pty-attach-P2-design.md)**（6 决策 + 5 时序图 + 帧/接口清单）。
- **P3 cast + 审计**：加密录制 + `pty_sessions` 表 + retention/prune 顺序 + `/pty/recording` gate。
- **P4 前端**：`AttachTerminal.vue`（xterm binary + ticket）+ JobDetail + **e2e 矩阵**。

**普通 job 零回归验收清单**（G023，P0/CI 常驻）：local exec、worker exec、worker cancel、pending_interaction bridge、Outcome-before-Result、worker disconnect fail、chatty/quiet hub HOL（`hub_test`）、workflow step、schedule run-now、resume/session capture。
**e2e 矩阵**（P4）：cancel 时 child/ptmx 退出、worker disconnect、browser disconnect/reconnect(5min)、slow browser、chatty pty 不饿死 quiet、resize fuzz、五闸各拒绝路径、ticket 过期/重放、nonce 重放、录制下载 gate、cast 加密/无 key fail-fast。

> plan 前 knob 全拍板（§4 K1-K6 + D1-D4）；§12 为 plan 内细化，不阻断。
