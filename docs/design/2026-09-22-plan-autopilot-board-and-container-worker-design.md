<!-- template_id: design; template_version: 1.1.1 -->
# 计划自驱与看板、容器 worker、pty 会话续接与短 id 设计（PLAN-03 / WEB-10 / CFG-09 / PTY-01 / XFER-02 / 小项）

> 状态：Approved 0.2 / 实施中（2026-09-22 人工批准，决策 1–6 照初稿）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-22 | Claude | 初稿：PLAN-03 todo 依赖 + 自动推进 + plan run/pause/blocked；WEB-10 计划看板；CFG-09 容器 worker 上线（含 hook runner、Linux 复核 todo）；PTY-01 交互 pty job 的会话 id 捕获与文本转录（现在 stdout 为空、无 session id、无法续接）；XFER-02 传输记录短 id；小项 AUTO-02b schedule webhook、`agent.degraded` 通知、xfer 上传前校验（h-aii-gnm3）、wakeup 时区（h-aii-tnua） |
| 0.2 | 2026-09-22 | Claude | 人工批准；分期 Q1 → Q2 → Q3 → Q4，全部 omp，测试先提交 |

## 背景与目标

v0.48.1 之后"派活 → verify → 验收"的每一步都有了，但**链条仍靠人串**：一批 9 个 job 是一个个 `--todo` 派的；看板只能看进度不能操作；我的 Linux 复核在容器手工跑；容器会话收不到 web 送话（容器没有 worker）。另外两处使用中发现的缺陷：交互 pty job 结束后 `stdout.log` 为空、`session_id` 也没有，`job resume` 续不上交互会话；`gofer tool cp` 的记录 id 是 32 位 hex，太长。

目标：**建好 todo 链，只管验收**；看板上拖一下就派发；容器 worker 让 Linux 复核成为链上一环、也让容器会话可被 web 送话；pty 会话可续接；id 变短。

## 已确认事实（代码 / 环境）

- todo 状态 `pending → ready → doing → done|skipped`，`ready`+assignee 自动派发（PLAN-02）；job 终态联动在 `internal/job/todolink.go linkTodoOutcome`（done → todo done；needs_review → note；accept 后按 done）；`plans` 有 `status/owner/project_key`，无 paused/blocked。
- 交互 pty job：`internal/runner/pty/runner.go` 明确**不**把输出接到 `req.Stdout`（设计 §11 "pty output 只入 cast/attach"）；`httpapi/local_pty_observer.go:133 sessionIDObserver` 只看**前 64KB** 输出并跑 `SessionCapture` 正则；claude/codex 的会话 id 在 TUI **退出时**打印，且 TUI 输出夹着 ANSI 序列——所以从未命中；`SessionInject`（claude `--session-id`）只在非交互构建路径追加（`submit.go:395`），交互 argv 没有注入。
- 传输 id：`internal/xfer/store.go:221 NewID()` = 16 字节随机 hex（32 位）；wakeup id 是 `wk-<8hex>`（11 位）。
- 容器：`/d/work/inhere/config/linux/gofer/` 已有 `worker.yaml`（`worker_id: w-docker-claude`，POLICY roots `D:/work/inhere → /d/work/inhere`，guards 全开，agents `claude/tty-claude/tty-demo`）、`.env`（server 地址已是 `192.168.65.254`，worker token 在 env）、`start-worker.sh`；但 `server_link.urls` 仍写 `host.docker.internal`（容器解析不了），worker 未运行（server 端 `w-docker-claude` disconnected）；容器会话 `TMUX_PANE` 未设（不在 tmux 里）。server 侧 `hyy-ai-inspect` 的 `allowed_runners` 已含 `w-docker-claude`。
- schedules：`POST /v1/schedules/{id}/run` 已有（手动触发，需 bearer）；agent 健康度按读时计算（`HealthState`），无状态转换事件。
- xfer 上传：`POST /v1/xfer` multipart 先收 `file` 流再校验路径（真机：传到 34% 才 400）；wakeup 时间渲染用了与 job id 不同的时区。

## 一、PLAN-03 todo 依赖与自动推进

### 1. 模型

- `plan_todos` 增 `after_json`（依赖的 todo id 列表，同 plan 内）、`auto INTEGER`（1 = 允许自动推进，默认 1）。
- `plans` 增 `paused INTEGER`、`blocked_todo TEXT`（链停在哪个 todo）；plan `status` 增 `blocked`（非终态，人处理后回 `open`）。
- CLI：`plan add-todo --after <todo>[,<todo>]`（`--after prev` = 上一条 todo，建链最常用）；`plan set-todo --after …`；`plan run <plan>`（把所有**无未完成依赖**、有 assignee、状态 `pending` 的 todo 置 `ready`）；`plan pause|resume <plan>`；`plan show` 显示依赖与 blocked。

### 2. 推进规则（都在 hub 侧 `linkTodoOutcome` 之后的 `advancePlan(planID)`）

- 触发：某 todo 变 `done`（含 accept 后）或 `skipped` → 扫同 plan 的 `pending` todo：依赖全部 `done|skipped` 且 `assignee != ""` 且 `auto=1` 且 plan 未 `paused` → 置 `ready`（随即由 PLAN-02 派发）。无 assignee 的 todo 到点只记事件 `plan.todo_unassigned`（人来补）。
- 失败：链上 todo 的 job `failed|timeout|cancelled|rejected`（且没有自动续投/转移接手）→ plan `status=blocked`、`blocked_todo`、事件 `plan.blocked {todo, job, reason}`（**进通知默认集**：它与 `job.needs_review` 一样是"需要人"的信号，只在用了链的 plan 上发生）；后续不再推进。人处理后：`plan set-todo <todo> --status ready`（重派）或 `--status skipped`（跳过）都会把 plan 回 `open` 并继续推进；`plan resume` 同。
- `needs_review`：后序等待 accept；reject 按失败处理（blocked）。
- 幂等：`advancePlan` 单条 SQL 条件更新（`status='pending' AND …`），并发终态不会重复置 ready；PLAN-02 的"无活跃 job"判定仍兜底。
- 事件：`plan.todo_advanced {todo, after}`、`plan.blocked`、`plan.completed`（全部 done/skipped 时，plan `status=done`，可订阅）。

### 3. 与容器复核的配合

链的末尾放一个 `exec@w-docker-claude` 的 todo（`--assign exec --runner w-docker-claude --cwd tools/gofer --verify 'bash -lc "go build ./... && go vet ./... && go test ./..."'`，prompt 为空 → exec 用 `--cmd`？）——**exec todo 需要 `cmd`**：`plan add-todo --cmd '…'`（argv，与 `job run -- …` 同语义）；`assignee=exec` 时派发用 `Cmd`。

## 二、WEB-10 计划看板

- `PlanDetail.vue` 顶部切换「列表 / 看板」（默认看板，记住选择）。五列：`pending / ready / doing / needs_review / done`（`skipped` 折叠进 done 列尾）。
- 卡片：标题、assignee 徽标（无则灰色"未指派"）、依赖角标（`after` 数 + 是否满足）、当前 job 芯片（状态色、verify 徽标、点开跳 job）、用量（tokens/$）、`dispatch_error` 红字。
- 拖拽（原生 HTML5 DnD，不加依赖）：`pending → ready`（= 派发，无 assignee 时弹 agent 选择）；`ready → pending`（撤回，未派发时）；任意 → `done`/`skipped`（人工标记，弹确认）；`doing` 列不可拖入。列头计数；plan 头部：进度条、`blocked` 横幅（指向 blocked todo + "重派/跳过"按钮）、`run / pause / resume` 按钮、用量汇总。
- 轮询沿用 PlanDetail 现有间隔；拖拽后乐观更新 + 失败回滚 toast。

## 三、CFG-09 容器 worker 上线

### 1. 代码侧（小）

- `worker.yaml` `server_link.urls` 支持多条已在；补 **`gofer worker doctor`**（`gofer worker` 组内）：检查 `.env` token、解析 urls 的主机、`roots.to` 目录存在、agents detect、并向 server 试连一次，打印一张表——省掉每次靠 `worker list` 猜。
- hook runner：`resolveHookRunner` 在 worker 模式已取 `worker_id`；容器里 CLI 是 client 模式 + worker 进程并存 → `.env` 加 `GOFER_HOOK_RUNNER=w-docker-claude`（P1 已支持）。
- `agents.<k>.detect` 在容器只认 `claude`（omp/codex 未装）——server 派 `omp` 到 `w-docker-claude` 会被 caps 拒绝，这是对的。

### 2. 运维侧（runbook `docs/runbook/container-worker.md`）

1. `worker.yaml`：`server_link.urls: [ws://192.168.65.254:8767/v1/workers/connect]`；`labels: [linux, container, go]`；`agents` 补 `exec` 显式段（默认内置即可，不必）。
2. 启动：`gofer worker -d`（daemon，pid/log 在 `run/`），容器入口脚本或 `start-worker.sh` 更新；`gofer worker doctor` 自检；server 侧 `gofer worker list` 看到 `w-docker-claude connected`。
3. `.env`：`GOFER_HOOK_RUNNER=w-docker-claude`；会话用 `tmux new -A -s claude` 起 Claude Code（`TMUX_PANE` 才会登记）；`gofer init hooks` 重装一次让 hook 带上 runner。
4. 验收：① `gofer job run -p hyy-ai-inspect -a exec --runner w-docker-claude --cwd tools/gofer -- go version`；② web 会话页对容器会话「送入终端」成功（tmux 路径）；③ 一条 plan 链末尾的 Linux 复核 todo 自动跑完。

## 四、PTY-01 交互 pty job 的会话续接

- **文本转录**：pty 输出经 relay 的 `OutputObserver` 同时写一份**去 ANSI** 的文本到 `<result_dir>/pty.txt`（CSI/OSC/私有序列剥掉，`\r` 折叠，尾部环形上限 `pty.transcript_max_bytes` 默认 4MB，超出保留尾部）；`job logs`/web 日志页对交互 job 显示 `pty.txt`（stdout 页签）；cast 录制不变。worker 上的 pty 也经 hub 的 relay（pump）→ 同一处写，无协议改动。
- **会话 id 捕获**：观察器改为**头 64KB + 尾 64KB 环**，在**进程结束**（relay 关闭）时再对去 ANSI 的尾部跑一次 `SessionCapture`；命中 → `SetSessionID`。终态 `captureOutcomes` 对交互 job 也扫 `pty.txt`（复用 `captureSessionID`）。
- **注入**：交互构建路径也追加 `SessionInject`（claude `--session-id <uuid>` 在 TUI 模式同样有效）——`agent.Build` 的 `Interactive` 分支加上；codex 无注入模板则靠退出输出捕获（内置 `SessionCapture` 正则对 TUI 退出文案的实际形态用真机采样校正，如 `session id: <uuid>` / `Resume this session with: codex resume <uuid>`，两种都匹配）。
- **续接**：有了 `session_id`，现有 `job resume` 的交互路径（`SessionResumeInteractive`）与中继 B 的接管都可用；web 会话/job 页对交互 job 显示"续接"按钮（已有 resume 入口的复用）。
- 验收：`tty-claude` 跑一个交互 job → 退出 → `job show` 有 `session_id`、`pty.txt` 有末尾总结；`job resume <id>` 起新交互 job 且 claude 认得上下文。

## 五、XFER-02 传输记录短 id

- `xfer.NewID()` → `xf-<8 hex>`（11 位，与 `wk-` 同风格；碰撞概率 2^-32/条，表有唯一键，冲突重生成）；HTTP 路径参数、CLI 输出、事件 `xfer:<id>` 作用域同步；已有长 id 的旧行照常可读（不迁移）。

## 六、小项

- **AUTO-02b**：`POST /v1/schedules/{id}/trigger`（外部 webhook 入口）：schedule 增 `trigger_token`（创建时生成，`schedule show` 可见，`schedule rotate-token`），请求带 `?token=`（或 `X-Gofer-Trigger-Token`），不需要 bearer；触发 = 现有 run-now 语义 + 事件 `schedule.triggered {source: webhook}`；`schedule create --webhook` 开启。
- **`agent.degraded` 通知**：健康度状态转换在 job 终态时计算（该 agent 上一次状态缓存在内存），`healthy|unknown → degraded` 记 `agent.degraded {agent, transient_fail, window_sec}`，`degraded → healthy` 记 `agent.recovered`；进事件表（scope `agent:<key>`）与 webhook（可订阅，不进默认集）。
- **h-aii-gnm3**：`POST /v1/xfer` multipart 先读 `meta` part 做全部校验（runner 在线/协议、project、路径、大小上限）再收 `file`；CLI 先 `POST /v1/xfer/precheck`（同 meta）再上传。
- **h-aii-tnua**：CLI/web 的 wakeup、schedule 时间统一按服务端本地时区渲染并带 `+08:00` 后缀（job id 已是服务端本地）；`GET /v1/...` 返回 unix 秒不变。
- `acp.log_thoughts` 保持默认 true（已合并成一行，不再淹没）。

## 横切

- schema（additive）：`plan_todos.after_json/auto/cmd_json`、`plans.paused/blocked_todo`、`schedules.trigger_token`；`xfers.id` 形态变化不迁移。
- 事件：`plan.todo_advanced|todo_unassigned|blocked|completed`、`schedule.triggered`、`agent.degraded|recovered`；**默认通知集加 `plan.blocked`**。
- 协议：无变更（pty 转录在 hub 侧 relay 写）。
- G032：无新增兼容分支。
- CLI 分组（G033）：`worker doctor` 在 `worker` 组；无新顶级命令。

## 实施分期与验收（每期一个 omp job，测试先提交）

| 期 | 内容 | 验收 |
|---|---|---|
| Q1 | PTY-01（转录 + 尾部捕获 + 交互注入）+ XFER-02 短 id + h-aii-gnm3 + h-aii-tnua | 真机：`tty-claude` 交互 job 退出后 `session_id` 有、`pty.txt` 有；`job resume` 续上；`tool cp` id 11 位、逃逸在上传前拒绝 |
| Q2 | PLAN-03（依赖/推进/blocked/run-pause、exec todo `--cmd`）+ AUTO-02b + `agent.degraded` | 三条链式 todo 自动跑完；中间失败 → blocked + 通知；重派后继续；webhook 触发 schedule |
| Q3 | WEB-10 看板 | 桩数据下五列/拖拽/派发/blocked 横幅；`vue-tsc`/build |
| Q4 | CFG-09：`worker doctor` + runbook + 容器实际上线（监督者在容器操作）| 容器 worker connected；web 送话到容器会话；链末 Linux 复核 todo 自动跑 |

## Q1 实测记录（2026-09-22 实施）

- **PTY-01 与代码事实的一处出入**：`internal/job/submit.go` 的 `SessionInject` 追加**本来就无条件**（只判 `ac.SessionInject` 非空），交互 job 一直会注入 `--session-id`——设计 §四「只在非批处理路径追加」的表述与代码不符。据此**没有再加分支**，而是补 `TestInteractiveBuildInjectsSessionID`（`tty-claude` 形状：`interactive_args` + `no_raw_cmd`）把这条路径钉住。`no_raw_cmd` 在代码里只出现在 `config.AgentConfig` 字段与内置模板中，**没有任何校验读取它**（`git grep -n NoRawCmd` 仅见模型/模板），所以 gofer 自身注入 argv 不存在被它拒绝的问题。
- **转录与环形上限**：`internal/ptyrelay/transcript.go` 的 `Transcript` 是 append-only 写盘 + 环形尾部保留：文件超过上限（默认 4MB）时按 `max/4` 的节流做一次重写压缩（文件峰值 ≤ 1.25×上限，重写放大 ≤ 4×）；`\r\n`→`\n`、孤立 `\r`→`\n`、CSI/OSC/charset/两字节序列剥离，**状态跨 Write 保留**（4KB pty 读会切开转义序列）。首次 flush 不早于构造后 2s / 64KB，静默会话不留空 `pty.txt`。
- **会话 id 捕获**：`httpapi.ptySessionCapture` 去 ANSI 后维护头 64KB + 尾 64KB 窗口，逐 chunk 扫；relay `WithCloseHook` 在 recorder 停止后（尾部已完整）再扫一次；终态 `captureSession` 对 `Interactive` job 兜底扫 `pty.txt`（非交互 job 不读转录）。
- **codex TUI 退出文案：未采样**。主机 codex-cli 0.155.1 在 pty 里可启动，但 TUI 先弹「Do you trust the contents of this directory?」信任确认才建会话，非交互环境下拿不到真实退出横幅（探针见提交说明，探针文件已删）。因此按设计 §四列出的两种形态实现：`session id: <uuid>` 与 `… codex resume <uuid>`，正则共用**单个捕获组**（`CaptureSessionIDBytes` 只取第 1 组）并收紧为 UUID 形态，避免把噪声行吞成 id。真机采样后若文案不同，只需改 `builtinSessionDefaults["codex"].SessionCapture` 一处。
- **XFER-02 id 形态**：`NewID()` → `xf-<8hex>`；`Manager.stage()` 对 `Store.Create`/`InsertXfer` 的碰撞重试 3 次。**没有任何地方解析 id 形态**（`git grep -n "0-9a-f]{32}"` 全仓无命中），所以「旧 32 位 hex 行照常可读」是无条件成立的，未加兼容分支。
- **h-aii-gnm3 的真实缺口**：multipart 早已要求 `meta` 先于 `file` 且在读 file 前校验路径/大小，但**不检查 worker 是否在线**——注册了却掉线的 worker 会收下整包再在 dispatch 阶段失败（真机「传到 34% 才 400」）。现在 `validateXferTarget` 增加「在线 + 协议 ≥ `wsproto.FileXferMinProtocolVersion`(9)」（离线 409 / 协议旧 409），`POST /v1/xfer/precheck` 复用同一个 `validateXferMeta`，CLI 在**哈希之前**预检。
- **h-aii-tnua**：`GET /v1/stats` 增 `server_tz_offset_sec`（每次请求现算，DST 变更无需重启）；CLI 一个 `fmtServerTime`/`fmtServerClock`（`internal/commands/timefmt.go`）被 `formatStarted`/`probeTime`/`formatScheduleTime`/`formatScheduleListTime` 共用，偏移每进程解析一次（拿不到→本地 + ` (local)`）；web 在 `api/time.ts` 一处按同偏移渲染（`getStats()` 顺带写入偏移）。线上时间字段仍是 Unix 秒。

## Q2 实测记录（2026-09-22 实施）

- **`plan run` 不理会 `auto`**：设计 §一.1 说 `plan run` 把「无未完成依赖、有 assignee、状态 pending」的 todo 置 ready，没提 `auto`。实现按字面走——`--no-auto` 是给**链**用的（链自动推进时跳过这一项），人工 `plan run` 是显式要求开工，所以根节点照样起。`advancePlan` 则**只**碰 `after` 非空的项（根节点永远不会被链自己启动），空 `after` = 根 = 必须 `plan run` 或人工置 ready，这正是「加一条 todo 不会自己跑」的落点。
- **`plan.todo_unassigned` 不要求 `auto`**：设计 §一.2 把「无 assignee 到点记事件」与「auto=1 才置 ready」分开写。实现按此：依赖满足但没人指派 → 记事件（无论 auto），因为「这一项到点了但没人干」是给人看的信号，而 auto=0 的项本来也不会被链启动。
- **blocked 的判定位置**：设计说「该 job 没有 `auto_resumed_by`/`fell_back_to`」，但 `finish()` 里 `linkTodoOutcome` 跑在续投/转移**之前**，那时标记还没写。所以 block 判定拆成独立的 `maybeBlockPlan(snap)`，在 `finish()` 的接管尝试**之后**（读 DB 行拿标记）、以及验收裁决路径（reject 之后）各调一次；`linkTodoOutcome` 只负责 note 与 done→advance。接管尝试失败而回落到迟到的 `job.terminal` 时也会 block（那时标记确实为空）。
- **exec todo**：`assignee=exec` 时派发用 `todo.Cmd` 且**不**设默认 prompt；无 `--cmd` 直接拒绝（`dispatch_error: exec todo needs --cmd`），不起一个空 argv 的 job。
- **`agent.degraded` 的 last_error**：`classify()` 对「非 0 退出且无 error」的失败返回 `Error == ""`，所以 `last_error` 在 `snap.Error` 为空时退化成状态词（`failed`），不会出现空字段。
- **健康度恢复的时间粒度**：`ok_since_transient` 只数 `ended_at` **严格大于**最后一次 transient 失败的 `ended_at` 的成功（都是 Unix 秒）。同一秒内「失败 → 成功」不会立刻算恢复，要等下一次成功。这是 SUP-01 P3 聚合本来的性质（本次未改），真机表现为恢复通知晚一条 job；测试里用一个 1.1s 的等待跨过秒边界。
- **`plan.blocked` 的 IM 渲染**：plan 作用域事件没有 job，`buildDeliveryBody` 里 job 摘要会退化成一个假 job；因此新增 `notify.PlanMessage`，IM 渠道对 `plan.*` 事件渲染「todo · job · reason」并把链接指到 `/plans/{id}`（scope `plan:<id>` 解析出 id）。
- **webhook 限速**：10s/计划的窗口是**进程内** map（`Server.scheduleTriggerAt`），重启只丢窗口、不会漏跑；限速在 token 校验**之后**判定，无凭据的探测拿 401 而不是 429。

## 决策（已批准 2026-09-22）

1. `plan.blocked` 进通知默认集（其余新事件不进）。
2. 看板用原生 DnD，不引入拖拽库；`doing` 列不可拖入。
3. pty 转录默认开启（4MB 尾部上限），去 ANSI 后落 `pty.txt`；cast 录制仍按现有开关。
4. 交互 claude 注入 `--session-id`（与批处理一致）；codex 靠退出输出捕获。
5. 传输 id `xf-<8hex>`；wakeup/webhook 等后续新 id 一律 `<2字母>-<8hex>`。
6. webhook 触发用 schedule 自己的 `trigger_token`，不复用 caller bearer。
