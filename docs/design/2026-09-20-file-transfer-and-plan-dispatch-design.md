<!-- template_id: design; template_version: 1.1.1 -->
# 文件传输与「计划即派发」设计（XFER-01 / JOB-11 / AUTO-05 / PLAN-02 / JOB-09）

> 状态：Draft 0.1 / 待人工批准

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-20 | Claude | 初稿：XFER-01 客户端↔server↔worker 文件传输（`gofer tool cp`、`job run --upload/--collect`）；JOB-11 同 cwd 串行锁 + per-agent 并发；AUTO-05 输出停滞检测；PLAN-02 todo 指派即派发 + plan 级用量；JOB-09 job wakeups（事件/定时 → 续投）。新增 CLI 约定：小工具命令进 `gofer tool` 组 |

## 背景与目标

- 通过 worker 操作节点机（如 `w-hw-windows11`）时经常要传文件，现在只能 base64 塞进 job 日志再解出来：慢、有大小上限、污染日志。→ **XFER-01**。
- 同一个 checkout 上并发派两个 agent job 会互相踩工作树，本周只能人工串行。→ **JOB-11**。
- agent 卡死（供应商流挂起、CLI 等待不存在的交互）只能等 job 超时，续投/转移都触发不了。→ **AUTO-05**。
- plan 的 todo 已能挂 job（`--todo`），但派发仍靠人敲命令；Multica 的"指派即派发"模型更顺手。→ **PLAN-02**。
- "等 verify 出结果 / 等人回复 / 每小时看一眼"现在靠常驻 hook 阻塞或人盯；Multica 的 wakeups（登记订阅后结束运行、输入到达再跑一次）更省。→ **JOB-09**。

参考项目对比见 [`../refer/reference-projects.md`](../refer/reference-projects.md)；路线图条目见 [`../gofer-enhancements-roadmap.md`](../gofer-enhancements-roadmap.md)。

## 已确认事实（代码）

- 路径安全：`project.SafeJoin(execRoot, cwd)`（`internal/project/path.go`）把项目内相对路径拼到执行机项目根之下并拒绝逃逸；`cfg.ExecPath(proj)` 给 server 本机项目根；POLICY worker 用 `roots` 把 server 逻辑 `host_path` 映射成本机路径。
- worker 的 token 在 HTTP 层是 `callerKindWorker`（`httpapi/server.go:416`），已有端点按 kind 区分（attach ticket、review 403）——worker 可以用同一 token 调 server HTTP。
- WS 协议 v8（`wsproto.CurrentProtocolVersion`），能力常量 + `Supports*()` + 派发前拒绝（SUP-01 P2，G032）；`Status` 帧 `{job_id, status}` 是自由字符串。
- `job.Service.execute()` 先取项目信号量 `sem` 与 caller 信号量 `callerSem`（`concurrency.go`），期间 job 保持 `queued`；再 `run.Run(ctx, req)`；job 环境已注入 `GOFER_JOB_ID / GOFER_CWD / GOFER_RESULT_DIR`（`outcomes.go:399`）。
- 终态链路：`finish()` → `failure_class` / 自动续投 / 故障转移（`transientHit` 同时看 stderr 尾部与 `snap.Error`）→ `OnTerminal` 钩子；`recordEvent()` 持久化后有 E14 webhook 入队与 SUP-01 P2 的事件观察者出站。
- schedules：`jobstore.ScheduleRecord{ScheduleType, CronExpr, RequestJSON, NextRunAt, …}`，`serve.startScheduleLoop` 每 `schedule.sweep_interval_sec` 扫 `DueSchedules(now)`，条件抢占 `AdvanceSchedule` 后 `Submit`。
- plan：`Plan{PlanID, Title, Description, Status, Owner}`（**无 project_key**）；todo 状态 `pending|doing|done|skipped`（`jobstore.ValidTodoStatus`），`PlanTodo.JobID` = 最近一次 job，`jobs.todo_id` 反查全部；`--todo` 联动与 note 追加（SUP-01 C）；模板渲染 `internal/template`（SUP-01 F）。
- artifacts：`ArtifactItem{Name,Size,Mtime}` 清单随 `Outcome` 回传，**文件本体留执行机**；`GET /v1/jobs/{id}/artifacts/{name}` 只能下载 server 本机 result dir 里的文件。
- CLI 顶级命令组：job / plan / session / agent / worker / template / schedule / wf / tun / project / config / init / hook / mcp / serve / presence / stop。

## 〇、CLI 约定：`gofer tool` 组（G033）

新增的**小工具类命令**一律放在 `gofer tool <name>` 下（本设计的 `gofer tool cp`、`gofer tool xfer …`），不再新增顶级命令；核心资源名词（job / plan / session / agent / worker / template / schedule / wf / tun / config）保持各自的顶级组。写进 `AGENTS.md` 作为 **G033**。

## 一、XFER-01 文件传输

### 1. 形态

```bash
gofer tool cp ./firmware.bin  w-hw-windows11:zy-bsly-hw-win11/tmp/in/firmware.bin   # 推到 worker
gofer tool cp w-hw-windows11:zy-bsly-hw-win11/tmp/out/report.csv  ./report.csv       # 从 worker 拿回
gofer tool cp ./x.tar  server:zy-bsly-sf-dev/tmp/x.tar                                # 目标是 server 主机（local 别名同）
gofer tool xfer ls|show <id>|rm <id>                                                  # 暂存区管理
```

- 远端写法 `<runner>:<project>/<项目内相对路径>`；`runner` 是 `server`（`local` 别名）或 worker id。路径按**执行机**的项目根解析（`SafeJoin`，POLICY worker 经 roots 映射），**只允许项目根内**，与 job `--cwd` 同一边界；目标目录不存在时创建（仅项目根内）；目标已存在需 `--force` 覆盖。
- v1 只传**单文件**；多文件/目录先打包（文档给 `tar`/`Compress-Archive` 示例）。不做断点续传、不做 worker↔worker 直传。
- 大小上限 `server.xfer.max_bytes`（默认 256MB，项目可覆盖 `xfer_max_bytes`）；sha256 全程校验；进度在 CLI 打印（按 1MB 块）。

### 2. 通道与状态机

**文件本体走 HTTP，WS 只传指令**（不改 WS 帧的流量特性）：

```
推：client --PUT /v1/xfer (multipart)--> server 暂存 xfer/<id>  --WS file_xfer{op:put}--> worker --GET /v1/xfer/<id>/content--> 写入项目路径 --WS file_xfer_result-->
拉：client --POST /v1/xfer {op:get, runner, project, path}--> server --WS file_xfer{op:get}--> worker 读文件 --PUT /v1/xfer/<id>/content--> server 暂存 --client GET /v1/xfer/<id>/content-->
```

- `xfers` 表：`id, op(put|get), runner, project_key, path, size, sha256, state(staged|dispatched|done|failed|expired), error, caller_id, created_at, finished_at, expires_at, job_id?`。
- 暂存目录 `<storage root 或 config dir>/xfer/<id>`；`state=done` 且已被取走、或到期（`xfer.ttl_sec` 默认 86400）由既有 retention 循环清理；`xfer rm` 立即删。
- `runner=server`：server 进程自己做文件读写（`ExecPath(proj)` + `SafeJoin`），不经 WS。
- worker 侧：`file_xfer` 帧到达 → 校验项目/路径 → HTTP 上传/下载（worker token，`internal/client`）→ 写盘到临时名再原子 rename → 回 `file_xfer_result{ok, size, sha256, error}`；单个 xfer 有超时（默认 10 分钟）；worker 离线/断线 → server 标 `failed: worker offline`（不排队等待，v1 简单化）。
- 协议 **v9**：`file_xfer` / `file_xfer_result` 帧 + Dispatch 的 `uploads/collect`（见 §3）；hub 对 <v9 的 worker 拒绝并提示升级（G032，不做静默降级）。
- 安全：调用方必须是 `user` caller（worker caller 只能作为执行端上传/下载**自己被指派的** xfer id）；每次传输记事件 `xfer.put|xfer.get {id, runner, project, path, size, sha256, by}`（进事件流与 IM 可订阅）；不传 `.env`/密钥的判断留给人（文档提醒），server 不做内容审查。

### 3. 与 job 集成（同一机制复用）

- `job run --upload ./a.bin:tmp/in/a.bin`（可多次）：CLI 先把文件 `PUT /v1/xfer` 进暂存区，`JobRequest.Uploads []{xfer_id, dest}`；执行机在 agent 起跑**前**按 §2 拉取到 `cwd` 相对的 `dest`（失败 → job `failed: upload x failed`，不起 agent）。
- `job run --collect tmp/out/*.csv`（glob，可多次）：job 结束（无论成败）后执行机匹配文件 → 上传到 server 暂存 → server 落到该 job 的 `<result_dir>/artifacts/collected/<相对路径>` 并并入 artifacts 清单，因此 `GET /v1/jobs/{id}/artifacts/{name}` 与 web 产物预览直接可用；单文件上限同 xfer，总量上限 `xfer.collect_max_bytes`（默认 1GB），超出的跳过并在 `collect` 结果里列出。
- `JobResult.Xfer {uploads:[{dest,size,ok}], collected:[{name,size}], skipped:[…]}` 持久化 `jobs.xfer_json`；web JobDetail 「文件」块；`job show` 一行。
- worker 路径：Dispatch 增 `uploads/collect`（v9），worker 本地 job.Service 执行；`--runner server` 直接本机复制。

## 二、JOB-11 同 cwd 串行锁 + per-agent 并发

- **谁互斥**：只有**可写的 agent job**（cli-agent / acp-agent 且非 `--read-only`、非交互）默认取**独占锁**；exec job 与 `--read-only` job 默认**共享**（不取锁、不被挡——`git log`、`go test` 这类巡检不该排队）。exec job 想独占可 `job run --exclusive-dir`；agent job 想放弃可 `--shared-dir`（自担风险）。
- **锁的键**：执行机上的绝对 `WorkDir`；**祖先/后代目录视为同一把锁**（`proj/` 与 `proj/sub/` 互斥）；worktree job 的目录独立，天然不冲突。
- **在哪**：`job.Service.execute()` 取完项目/caller 信号量之后、`run.Run` 之前，进程内 `dirLocks`（按路径的等待队列，FIFO）；等待期间 job 状态 **`waiting_dir`**（新非终态，统计/inflight 视同 `queued`，`IsFinished=false`），事件 `job.waiting_dir {holder_job}`，取消仍生效。worker 上跑的 job 由 worker 自己的 `job.Service` 加锁，状态经 `Status` 帧上报（字符串，额外协议不需要）。
- **per-agent 并发**：`agents.<key>.max_concurrent`（默认 0=不限）→ `Submit` 增 agent 信号量（与项目/caller 同机制），超限保持 `queued`。
- 配置：`server.dir_lock: true`（默认开；关掉即全部共享）。
- web/CLI：Board 状态芯片增 `waiting_dir`（灰蓝）+ 悬停显示 holder；`job show` 打印 `waiting_dir: holder=<id>`。

## 三、AUTO-05 输出停滞检测

- 判定：运行中的 job（非交互）若 **N 秒内 stdout/stderr 都没有新增字节、ACP 也没有任何 `session/update`** → 视为停滞。N = `job run --stall-timeout` > `agents.<key>.stall_timeout_sec` > `server.stall_timeout_sec`（默认 **900**，agent job 生效；exec job 默认 0=关，因为构建/测试常常长时间无输出）。
- 动作：kill 进程（复用取消路径），job `failed`，`error = "stalled: no output for 900s"`，事件 `job.stalled {silent_sec}`；`failure_class=transient`（内置 transient 模式加 `^stalled:`），于是**自动续投 → 故障转移**链正常接管。
- 实现：job 的 stdout/stderr writer 外包一层记录 `lastOutputAt`；ACP runner 的 `OnJobEvent`/update 也 bump；`execute()` 内 30s 一次的看门狗。worker 上由 worker 的 `job.Service` 执行（它拥有进程），hub 收到 failed + error 文本后按现有规则续投/转移。
- 不误伤：`pending_interaction`（等人作答）期间不计时；verify 阶段不计时（有自己的超时）。

## 四、PLAN-02 todo 指派即派发 + plan 级用量

### 1. 模型

- `plans` 增 `project_key`（`plan create --project`；缺省时派发要求 todo 自带 `--project`）。
- `plan_todos` 增：`assignee`（agent key）、`project_key`（覆盖 plan）、`template`、`vars_json`、`verify_json`、`review INTEGER`、`runner`、`cwd`、`timeout_sec`、`dispatch_note`；状态枚举增 **`ready`**（在 `pending` 与 `doing` 之间：`pending`=backlog 不触发，`ready`=可派发）。
- 规则：**`status=ready` 且 `assignee` 非空 且没有活跃 job（非终态、非 needs_review）→ 立即派发**。两个条件哪个后满足都触发（`set-todo --status ready` 或 `set-todo --assign omp`）。派发 = `Submit(JobRequest{ProjectKey, Agent: assignee, Template/Vars 或默认 prompt, Verify, Review, Runner, Cwd, TimeoutSec, TodoID, PlanID, Channel:"plan", CallerID: 触发者})`，SUP-01 C 的联动随即把 todo 置 `doing`、结束时 done/note。
- 默认 prompt（无模板时）：`# <plan.title>\n\n<plan.description>\n\n## 本任务\n<todo.title>\n\n<todo.note>`；有模板时内置变量增 `{{plan_title}} {{plan_description}} {{todo_title}} {{todo_note}} {{todo_id}}`。
- 再跑：todo 处于 `doing/done` 时再 `--status ready` = 再来一次（前一个 job 必须终态或 needs_review 已裁决）；换 agent = `--assign` 另一个 + `--status ready`。
- 失败/拒绝：job `failed` → todo 保持 `doing` + note（C 已有），**不**自动重派（人或 wakeup 决定）；`reject --resume` 的续投自然继承 todo。
- 兜底：`gofer plan dispatch <todo>` 显式派发（忽略状态判定，仅要求 assignee 与无活跃 job）。server 重启**不**自动补派 ready 的 todo。

### 2. 面

- CLI：`plan add-todo/set-todo` 增 `--assign --project --template --var --verify --review --runner --cwd --timeout`；`plan dispatch <todo>`；`plan show` 显示 assignee 与派发状态。
- HTTP/MCP：todo 写接口同字段；`gofer_update_todo` 增 `assignee/status=ready`；新工具 `gofer_dispatch_todo`。
- web PlanDetail：todo 行显示 assignee/agent 徽标 + 「派发」按钮（= 置 ready），派发中显示 job 链接；plan 头部用量汇总（tokens / $，来自挂接 job 的 `usage_json`）。
- 事件：`plan.todo_dispatched {todo_id, job_id, agent}`、`plan.todo_dispatch_failed {todo_id, error}`（进事件流；后者进通知默认集？**不进**，默认集不变）。

## 五、JOB-09 job wakeups

### 1. 语义

agent（或人）在一个 job 上登记 **事件订阅** 或 **定时器**，job 正常结束；条件到达时 gofer 自动起一次**续投**（`job resume`，有 session 则续同一会话；无 session 或 agent 不可续 → 用原请求 + 指令重跑），把登记时写好的 `instruction` 作为提示词。没有常驻进程；"事情是否办完"仍由续投的 agent 自己读当前状态判断。

```bash
gofer job wakeup create <job> --kind at    --after 10m            -m "检查 CI 结果并汇报"
gofer job wakeup create <job> --kind every --every 1h             -m "巡检一次 tmp/gofer 下的新失败 job"
gofer job wakeup create <job> --kind cron  --cron '0 9 * * 1-5' --tz Asia/Shanghai -f instr.md
gofer job wakeup create <job> --kind event --event job.terminal --job-id <other> [--status done,failed] -m "对方结束了，合并结果"
gofer job wakeup create <job> --kind event --event job.reviewed  -m "验收有结论了，按 note 处理"
gofer job wakeup list|show|disable|enable <…>
```

- `--mode once`（默认，触发一次即消费）/ `continuous`（`every/cron` 默认 continuous；event 可选）。
- 事件目录（v1）：`job.terminal`（可 `--status` 过滤）、`job.verify_finished`、`job.needs_review`、`job.reviewed`、`job.fell_back`、`interaction.answered`（默认监听自己的 job）、`session.takeover_released`；`--job-id` 指定源 job（缺省 = 自己）。
- 触发 → 续投 job 带 tag `wakeup:<id>`、事件 `job.wakeup_fired {wakeup_id, kind, continuation_job}`；**一个 wakeup 同一时刻只允许一个未终态续投**，期间再触发只计数不派发（`coalesced_count`），避免每小时叠一堆。
- 定时器：`every` 从创建/启用时起算，`cron` 取下一次未来时刻，**不补发**错过的周期（server 停机期间的 tick 合并为一次）；`at` 到点后消费。
- 有效期：默认 7 天（`wakeup.ttl_sec`），到期自动 disable；目标 job 被删除/清理时级联删除。
- 权限：创建者需有权 `job resume` 该 job（同一 caller 或 `can_answer`）；续投的 `CallerID` = 创建者。agent 在 job 里用 `GOFER_JOB_ID` 指自己（MCP 工具或 CLI 均可，CLI 需 client 配置在执行机上）。

### 2. 实现

- 表 `job_wakeups {id, job_id, kind, at, every_sec, cron_expr, timezone, event_types_json, filter_job_id, filter_status_json, mode, instruction, enabled, revision, next_run_at, last_fired_at, fired_count, coalesced_count, continuation_job_id, created_by, created_at, expires_at}`。
- 定时：并入 `serve.startScheduleLoop` 的同一次扫描（`DueWakeups(now)` + 条件抢占 `AdvanceWakeup`），复用 `NextCronRun`。
- 事件：`job.Service.recordEvent` 持久化后调 `wakeups.OnEvent(jobID, type, detail)`（新 seam，与 SUP-01 P2 的观察者并列）；匹配 `event_types ∋ type && (filter_job_id == "" ? jobID == wakeup.job_id : jobID == filter_job_id) && status 过滤`。
- 续投：`ResumeJob(job, instruction)`；`ErrNoSession/ErrResumeUnsupported` → `RebuildJob` 并把 `instruction` 追加到 prompt 末尾（exec job：原 argv 重跑，instruction 只进事件）。
- 面：CLI `gofer job wakeup …`；HTTP `/v1/jobs/{id}/wakeups[…]`；MCP `gofer_wakeup_create|list|disable`；web JobDetail 「唤醒」块（列表 + 触发历史 + 开关）；`job show` 一行摘要（`wakeups: 2 enabled`）。

## 横切

- **协议 v9**：`file_xfer` / `file_xfer_result` 帧，Dispatch 增 `uploads / collect / exclusive_dir / stall_timeout_sec`；`Status` 帧值新增 `waiting_dir`。<v9 worker：xfer/upload/collect 派发前拒绝（G032）。
- **schema（additive）**：新表 `xfers`、`job_wakeups`；`jobs` 增 `xfer_json`；`plans` 增 `project_key`；`plan_todos` 增 assignee 等 9 列 + 状态值 `ready`；job 状态值 `waiting_dir`。
- **事件**：`xfer.put|get`、`job.waiting_dir`、`job.stalled`、`plan.todo_dispatched|todo_dispatch_failed`、`job.wakeup_fired`；默认通知集不变。
- **配置**：`server.xfer{max_bytes, ttl_sec, collect_max_bytes}`、`server.dir_lock`、`server.stall_timeout_sec`、`agents.<k>.{max_concurrent, stall_timeout_sec}`、`wakeup.ttl_sec`。
- **G032**：本批不新增兼容分支；到期的 6 处 DEPRECATED(v0.45) 在 v0.48 单独清理（不并入本批）。
- **AGENTS.md**：G033 `gofer tool` 组约定。

## 实施分期与验收（每期一个 omp job，测试先提交）

| 期 | 内容 | 验收 |
|---|---|---|
| X1 | XFER-01 核心：`xfers` 表 + 暂存区 + HTTP 上传/下载 + `file_xfer` 帧（v9）+ worker 执行端 + `runner=server` 路径 + `gofer tool cp` / `tool xfer` + 审计 + retention + G033 | 真机：容器 `tool cp` 一个 5MB 文件到 `w-kzl-desktop:<project>/tmp/`，sha256 一致；反向拉回一致；>max 被拒；路径逃逸被拒；worker 离线 → failed |
| X2 | XFER-01 job 集成：`--upload` / `--collect` + `xfer_json` + artifacts 并入 + web「文件」块 + docs/skill | worker job 起跑前文件就位；结束后 `collect` 的文件可从 web 下载 |
| P1 | JOB-11（dir lock + `waiting_dir` + per-agent 并发）+ AUTO-05（停滞检测 → transient） | 同 cwd 两个 agent job 第二个 `waiting_dir` 后接力；exec 不被挡；停滞 job 在 N 秒后 failed 且触发续投 |
| P2 | PLAN-02（plan project、todo 字段、`ready`、派发器、`plan dispatch`、web、MCP、plan 用量） | `set-todo --assign omp --status ready` 自动出 job 并联动到 done；无模板默认 prompt 含 plan/todo 文本 |
| P3 | JOB-09（表、定时并入 sweeper、事件匹配、续投/重跑、CLI/HTTP/MCP/web、docs） | `--kind at --after 1m` 到点续投；`--kind event --event job.terminal --job-id B` 在 B 结束后续投 A；coalesce 生效 |

## 决策（待批准）

1. `gofer tool cp` 的远端写法 `<runner>:<project>/<相对路径>`，只允许项目根内；v1 单文件、无断点续传。
2. 文件本体走 HTTP + worker token，WS 只传指令；协议 v9，老 worker 直接拒绝。
3. JOB-11 只有可写 agent job 默认独占；exec / read-only 共享；`--exclusive-dir` / `--shared-dir` 可反转。
4. AUTO-05 默认 900s，只对 agent job 生效；停滞按 transient 处理（会续投/转移）。
5. PLAN-02 新增 todo 状态 `ready`；`pending` 不触发；server 重启不自动补派。
6. JOB-09 同一 wakeup 同时只允许一个未终态续投（coalesce）；默认 7 天过期；无 session 时退化为重跑 + 追加指令。
