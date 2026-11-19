# 一期计划：常驻会话名册（seat）+ 主动找人

> issue：gofer-0py5（一期）；同批收 gofer-hzz3（推送同口径）、gofer-iuiq（非 Claude 标记通道）
> 设计：`docs/design/2026-11-18-resident-sessions-and-team-design.md` §4、§5（方向已批准）
> 状态：**待批**（2026-11-19 第二版：按独立评审修订，拆 1a / 1b）

## 0. 计划级决定

1. **代码与 CLI 用 `seat`，中文仍叫「常驻会话」**。代码里 `resident` 已指「ACP 持续会话 job」（`job resume --mode session`、`MailResidentAgent`）。一个 seat 是一个长期的人手位置，先后有多个会话坐上去。
2. **拆两步**。1a 先把判定做对：「等我」覆盖终端、ACP、suga 三类会话；推送进门即发且只发一次；seat 实体、CLI、charter、`seat open`。1b 再做 web：名册、弹卡、浏览器通知、定时。底部标签栏不做。
3. **「等我」只认存储事实**。ACP / suga job 会话要进「等我」，须满足三条：停在 `awaiting_input`，该轮带 `[needs-reply]` 标记，且超过宽限期。判定由 today 生成卡片，不用内存定时器。前端工作台不再把每个 `awaiting_input` 都算「需要你」，改用服务端给的结论。
4. **通知进门即发，且持久化**。每个「等我」主体在进门时记一行，通知只发一次，30 分钟未处理再提醒一次（服务端记 `reminded_at`）。桌面、webhook、Web Push 三个渠道都只从这里发。推送只带标题和类别，不带回合正文。
5. **绑定以人为主**。web 会话行「设为常驻」，或人在终端执行 `gofer seat bind`。不把绑定命令写进给 agent 的注入。只是离线的 seat 不算空位。seat 不承载会话授权。
6. **痛点 3（提醒建 plan、记得流程）的过渡办法**：在 charter 里写流程约定，T4 提供一份模板。正式的流程推进在二期。

## 1. 现状要点（代码摸底，详细位置见附录 A）

- 终端会话 `agent_sessions.session_id` 就是 CLI 自己的会话 id。Claude Code `--resume` 后 id 不变（用户确认），`/clear` 会生成新 id。同 id 重新注册会回到 running 并轮换 incarnation，会话授权随 incarnation 失效。
- 一个目录可能有多个会话：本机标记文件每目录只有一份，`resolveCurrentSession` 遇到多条时报错。
- 「等我」至少四个口径：today / inbox（宽限 + needs_reply + 监督）、工作台 attention（无宽限）、前端 `pendingAlert` toast、服务端 `NotifySessionWaiting`（回合一开就发，正文是整段回合）。Web Push 只认 job 事件。
- ACP 会话等回复只有 `session.awaiting_reply` 这个 job 事件。它由内存 `time.AfterFunc` 触发，server 重启就丢；没配 notification 时关闭；today / inbox 没有对应卡类。
- 前端 `threadGroups.ts` 把 `awaiting_input` 直接映射为 `need`。工作台已有 need / active / idle / ended 分组、按项目分组、`WorkbenchThreadPref{Title, SeenAt, Pinned}` 与服务端布局。
- `PlanResumeForIn` 是 pty 接管的预演：要求 takeover 已接线且项目 `allow_interactive`；argv 带 job 用的 `GlobalArgs`；目录是 `ExecPath`（容器视角下是容器路径）。**不能直接用来给人拼命令**。
- 会话催办 nudge 在会话 ended / handed_off 时永久结束，offline 时跳过，删会话时连带删除。**不能承载 seat 定时**。定时任务 `schedules` 有 cron、catch_up、失败暂停，暂停时进「等我」。
- 收件箱已有 `inbox_item_state`（seen / cleared），可承载弹卡的关闭状态。

## 2. 任务

| # | 内容 | 执行方 | 依赖 | 步 |
|---|---|---|---|---|
| T0 | 真机核实：Claude 的 ACP 适配器能否 `session/load` 终端 Claude Code 写下的会话；核实 suga 终端会话是否走 `gofer hook generic`；结论写进设计 §4.3 / 本计划 T1 | 我（主机 exec job） | — | 1a |
| T1 | 「等我」判定下沉，加 ACP / suga 会话卡类与标记 | sonnet | — | 1a |
| T2 | 通知记录表：进门即发、只发一次、服务端再提醒、三渠道同口径、正文不出门 | sonnet | T1 | 1a |
| T3 | seat 数据层、服务、HTTP | sonnet | T1（Status 的判定接口） | 1a |
| T4 | seat CLI、charter 注入、续接告知、流程约定模板 | suga | T3 | 1a |
| T5 | web：名册（扩展工作台）、「设为常驻」、顶部弹卡 | suga | T1、T3 | 1b |
| T6 | 浏览器通知单一出口 | suga | T2 | 1b |
| T7 | seat 定时：schedules 加投递目标 `seat:<id>` | suga | T3 | 1b |
| T8 | 合并、verify、真机验收、文档 | 我 | 全部 | 各步末 |

1a 做完先验收（T8 前半），再开 1b。

### T1 「等我」判定下沉 + ACP / suga 卡类（sonnet）

- 把 `httpapi.waitingFor` 的组装下沉到 `internal/inbox`（例如 `WaitingSubjects(...)`）。作用域上限、语言、caller 作为参数传入，不依赖 rux。HTTP、工作台 attention、seat Status、通知记录表都调它（G021 / G022）。`relayGraceSec` 导出或下沉到同一处。
- 新卡类「ACP 会话等你」：由 today builder 从存储事实生成，条件是 session job 处于 `awaiting_input`、该轮最终输出以 `[needs-reply]` 单独成行结尾、且超过宽限期（复用 `recheckAt`）。不读内存定时器。`session.awaiting_reply` 事件保留原样，不再作为「等我」依据。
- 标记来源：托管 ACP 会话（claude / codex / suga）经现有 SystemInject 注入同一条 needs-reply 约定。解析复用 hookrelay 的 `splitNeedsReply`，抽到共用包，不复制一份。
- suga 终端会话：若 T0 确认走 `gofer hook generic`，现有 generic 方言已覆盖，只补用例；否则在本任务里补 suga 的 hook 接法（iuiq）。
- 工作台 attention 改调同一判定。前端 `threadGroups.ts` 的 `awaiting_input → need` 改为读服务端给的 `attention` 字段（前端改动小，随本任务做）。CHANGELOG 写明工作台「需要你」现在也有宽限期。
- 用例：ACP 回合带标记且超宽限 → 进；不带标记 → 不进；宽限内作答 → 不进；server 重启后同一状态仍进（无内存依赖）；工作台与 today 同一结果；suga 会话带标记进、不带不进。

### T2 通知记录表（sonnet，hzz3）

- 新表 `waiting_notices`：`caller_id`、`subject_key`（与「等我」主体 key 一致）、`entered_at`、`notified_at`、`reminded_at`、`cleared_at`。基础 schema 用 `CREATE TABLE IF NOT EXISTS`，并通过 `TestOpenUpgradesPreviousReleaseSchema`（G057）。
- 挂在现有 sweeper 上周期扫描：比较当前「等我」集合与表。新进门的主体写行并发一次通知；超过 `reminder_after`（默认 30 分钟，可配置，可关；测试里可调小）仍未处理的再发一次并记 `reminded_at`；离开「等我」的记 `cleared_at`，之后再进门算新的一行。这样重启是幂等的。
- `OpenTurn` 不再立即 `NotifySessionWaiting`。桌面通知、webhook、Web Push 三个渠道都从这里发。Web Push 接受会话主体，tag 等于 `subject_key`。
- 隐私：通知只带标题与类别（「X 在等你回答」「X 等你批准」）。不带回合正文；预览最多 80 字符，并经过脱敏，复用仓库已有的脱敏工具；没有可复用的就不带预览。
- 用例：宽限内作答不发；进门发一次且仅一次；监督中不进门，监督结束进门时发；重启后不重发也不漏发；30 分钟再提醒只一次；payload 不含正文。

### T3 seat 数据层与服务（sonnet）

- 表 `seats`：`id`、`caller_id`、`project_key`、`name`、`rel_cwd`（项目相对目录）、`purpose`、`charter`（正文或仓库相对路径）、`charter_by`（`user` / `session:<sid>`）、`kind`（`terminal` / `acp`）、`agent`、`current_ref`、`archived_at`、时间戳。唯一索引 `(caller_id, project_key, name) WHERE archived_at IS NULL`；`current_ref` 上部分唯一索引，保证同一会话只坐一个 seat。另有表 `seat_bindings`（历史）和 `seat_charter_history`。
- 排序和置顶不放在 seat 上，复用 `WorkbenchLayout` / thread pref；未读取当前线程的 `SeenAt`。
- 服务（新包 `internal/seat`，依赖 jobstore / sessionrelay 与 T1 判定的接口，不反向 import）：
  - 增删改、归档；`Bind` / `Unbind` 在事务内完成换绑并记历史；两个会话并发 bind 时结果确定。
  - 自动续绑：同 id 重新注册保持绑定；会话 ended 不解绑；会话被删时 `current_ref` 置空并记历史，不留悬空引用。
  - 绑定候选：同一项目内、`rel_cwd` 为前缀匹配（兼容子目录，兼容主机 / 容器两种写法），且 seat 无 current，或 current 已 ended 超过 7 天。只是 offline 的不算。
  - `OpenPlan`：新写一个只做渲染的函数（例如 `agent.InteractiveResumeArgv`）。它不带 `GlobalArgs`，不走 takeover 前置检查；目录用 `project.HostPath` 加 `rel_cwd`。返回命令本身，并写明在哪台机器上执行，给出 sh / PowerShell 两种写法。当前会话不可续时返回「新开」命令。acp 类返回工作台线程 id；当前 job 已结束时，用原 session id 重启。重启逻辑从 MailResident 抽出共用，不复制。
  - 新开时的续接告知：返回给 T4 / 重启路径一段注入文本，内容是 charter、最近的 `work report` 或 plan handoff 摘要，加上「这是新会话」。
  - Status：当前会话状态，加 T1 判定的是否在「等我」，加未读。名册列表批量计算。
- HTTP（G056：登记 `routeDecls` 与 catalog，`go generate` 更新 permissions.md）：`GET/POST /v1/seats`、`GET/PATCH/DELETE /v1/seats/{id}`、`POST /v1/seats/{id}/bind|unbind`、`GET /v1/seats/{id}/open-plan`。列表按作用域上限过滤（`InHandler(filter)`）。读写他人的 seat 需要新权限 `seat.manage.others`（风险中，不可授予会话）。请求来自会话主体时，修改 charter / purpose 记 `charter_by=session:<sid>` 并留历史，web 上标出。字段写进 `docs/reference/http-api.md`。
- 待核实（本任务第一步）：终端会话里的 CLI 调用以什么主体到达 server，即用户 token 还是会话凭证。结论决定上一条的判定方式。
- 会话注册视图 `sessionView` 加 `seat`（名字与 charter 摘要），只在已绑定时出现。不再下发绑定候选给 agent。
- 用例：唯一名（含跨项目同名、归档后重名）；换绑历史与并发 bind；resume 同 id 绑定不变；删会话后不悬空；候选不含 offline；open-plan 不含 GlobalArgs、目录是主机路径；acp 已结束时重启；作用域过滤；会话主体改 charter 有记录；schema 升级。

### T4 seat CLI 与注入（suga）

- `gofer seat ls|add|show|set|rm|bind|unbind|open`。这是与 job / session 同级的顶级资源名词，要同步更新 AGENTS.md G033 的名词清单。`bind` 不带 ref 时取当前会话；`open <name>` 打印 open-plan 的命令；跨项目时用 `name@project`。CLI 照 `internal/commands` 的结构写：入口薄，渲染放在业务包。
- charter 注入：只走 hookrelay SessionStart 一条链（`sessionView` 已带 seat），在 startup / resume / compact 时追加一节「常驻会话：<名字>（<用途>）」加 charter 正文，按预算截断。seat 段的渲染放在 `internal/seat`。只装了 prime hook、没装 relay hook 时，`seat bind` 提示去装。
- 续接告知：seat 的 current 换成新会话（新开或 `/clear`）后，第一个 SessionStart 注入 T3 的续接告知文本。
- 流程约定模板：`docs/` 下一份 charter 示例，写明 design → review → plan（`gofer plan create`）→ doing → verify 的约定，`seat add --charter-template flow` 可直接套用。
- skill references（G045）：`sessions-relay.md` 加 seat 一节；`commands.md` 补命令。
- 用例：CLI 真实分发测试；注入片段（startup / resume / compact）；续接告知只出现一次；未绑定时不注入任何 seat 内容。

### T5 web 名册与弹卡（suga，1b）

- 名册：在工作台侧栏现有分组上加「常驻会话」分区，按工作区分组。终端会话也进入工作台（复用 `r:<sid>` 线程）。排序和置顶用现有布局，打开或回复不改变顺序。每行显示名字、用途、状态点（回复中 / 等我 / 失败 / 空闲 / 离线）、未读点。行菜单：在终端继续（复制 open-plan 命令并显示执行机器）、打开线程、编辑（显示 `charter_by` 与历史）、归档。
- 「设为常驻」：工作台与会话页的会话行菜单。会话 id 变了、而目录对得上某个 seat 时，行上显示「是否设为 X」，人点了才绑。
- 顶部弹卡：App 级组件，只弹会话主体的 answer / approval 类「等我」项，失败 job、token 将过期等不弹。一次一件，其余显示 +N。按钮：作答（短回复直接送）/ 批准 / 打开 / 稍后（现有 snooze）。关闭写入 `inbox_item_state` 的 seen，多端同步。替换现有 `pendingAlert` toast。
- 传输：WS 加一个「等我变化」事件（只带 key 与类别），客户端收到后拉取，不轮询完整 today。
- i18n 中英；用例照 `threadGroups.test.ts` / `appShellRender.test.ts` / `*Wiring.test.ts`：顺序稳定、弹卡队列与 +N、关闭后另一客户端不再弹、非会话项不弹。

### T6 浏览器通知单一出口（suga，1b）

- 已订阅 Web Push 的浏览器只收服务端推送（T2），前端不再自己发。未订阅时，页面隐藏才发，且只由一个标签页发：用 BroadcastChannel 选主，tag 等于 `subject_key`，点击深链。
- 非安全上下文（局域网 http）下没有 SW 和 Notification，降级为页面标题计数，并在设置里说明原因。
- 用例：两个隐藏标签页只发一条；已订阅时前端不发；非安全上下文走降级。

### T7 seat 定时（suga，1b）

- `schedules` 加投递目标列（空表示现有行为，即提交 job；`seat:<id>` 表示投递到 seat），并复用 cron、catch_up、失败暂停。到点时解析 seat 的 `current_ref`：终端会话在线就 `SendMessage`；ACP 走 `job say`，已结束则按 T3 重启后再送；终端会话不在线就生成一张「去打开 X」卡，不替人起终端。nudge 不改。
- CLI：`gofer seat schedule <name> --cron ... --text ...`，内部建 schedule。
- 用例：到点送话；终端离线进「等我」；acp 已结束时重启再送；会话换绑后定时跟着 seat 走。

### T8 验收（我）

- 1a 验收：
  - 主机上一个 Claude Code 终端会话：`seat add` → `bind` → 关掉 → `seat open` 用一条命令续上（同 id，命令里没有 GlobalArgs）→ resume / compact 后 charter 出现。
  - 回合标 `[needs-reply]`，满宽限期进「等我」并推一次；重启 server 后不重推；阈值调小后验证再提醒只发一次。
  - 一个托管 ACP seat 带标记的回合进「等我」，不带标记的不进。
  - 推送内容里没有正文。
- 1b 验收：
  - 弹卡、关闭后另一浏览器不再弹；两个标签页加一个手机只收到一条通知。
  - seat 定时到点送达；离线时进「等我」。
- 截图存 `tmp/acp-trial/png/s9-*.png`。gofer-verify 全绿；CHANGELOG、capabilities（G058）、skill references、http-api.md。

## 3. 反向验证

- T1：去掉「带标记」条件 → 「不带标记不进」失败；改回内存定时 → 重启用例失败。
- T2：去掉 `notified_at` 判断 → 「只发一次」失败；把正文放回 payload → 隐私用例失败。
- T3：`OpenPlan` 带上 GlobalArgs → 命令用例失败；候选包含 offline → 候选用例失败。
- T6：去掉选主 → 「两个标签页只发一条」失败。

## 4. 不在一期

- 底部会话标签栏；
- 流程推进（二期，痛点 3 的正式方案）；
- Team（三期）；
- ACP 原地迁入终端会话（取决于 T0，若结论可行，在二期前单独做）；
- 多人共享 seat。

## 附录 A 代码位置（摸底，2026-11-19）

- 会话表 `internal/jobstore/store.go:525`、迁移 `:2425–2507`；模型 `internal/jobstore/sessions.go:290–396`；upsert `:615`、复活轮换 `:705–745`。
- 登记：`internal/hookrelay/run.go:457/485/502`；`internal/httpapi/session_handler.go:568/900`、`sessionView :238`；`internal/sessionrelay/service.go:492/615/642`。
- 注入：`internal/commands/repo.go:86–136/303/463`、`internal/tracker/prime.go:15`；`internal/hookrelay/run.go:206–226/889/922/935`、`needs_reply.go:17/24/39`、`prompt_memory.go:34/109`。
- resume：`internal/sessionrelay/resume.go:16/48/73/114`、`deliver.go:1010`；`internal/agent/registry.go:237/286`。
- 「等我」：`internal/today/cards.go:37/236/393`、`relay_gate.go:8/18`、`snooze.go`；`internal/httpapi/inbox_handler.go:118`；`internal/inbox/waiting.go:65/105/160`；工作台 attention `internal/workbench/service.go:600–633`。
- 推送：`internal/sessionrelay/service.go:977`；`internal/job/notify_dispatch.go:59/79/247`；`internal/webpush/dispatch.go:55/306`；`web/public/sw.js`；`web/src/ui/NeedsMeButton.vue:73–80`、`utils/pendingAlert.ts`。
- 布局：`web/src/App.vue`、`web/src/ui/AppShell.vue:173–383`、`utils/shell.ts`；`web/src/components/workbench/WorkbenchSidebar.vue`、`threadGroups.ts`。
- 先例：`internal/jobstore/mail_resident.go`；`internal/sessionrelay/nudge.go:66/210/254`。
- 第二版补充：`internal/job/session_notify.go:24–79`（awaiting_reply 内存定时）；`internal/job/notify_dispatch.go:247–259`（`NotifySessionWaiting` 带正文）；`internal/sessionrelay/resume.go:73–98`（takeover 前置检查）；`internal/sessionrelay/nudge.go:210–240`、`internal/jobstore/sessions.go:1337`（nudge 随会话结束 / 删除）；`internal/jobstore/workbench.go:14`（`WorkbenchThreadPref`）；`web/src/components/workbench/threadGroups.ts:12`（`awaiting_input → need`）；`internal/jobstore/inbox_state.go`（seen / cleared）；`internal/jobstore/store.go:346`（schedules）；`internal/jobstore/mail_resident.go:48`（`name@project`）；`internal/hookrelay/run.go:935/1058`（generic 方言与 `splitNeedsReply`）；`internal/webpush/dispatch.go:82`（tag）。

## 附录 B T0 结论（2026-11-19）

- ACP 加载终端会话：可以。主机上 `claude -p` 建的会话，用现有的 `gofer job resume <job> --agent claude-acp --mode session` 在同一目录续上，模型答出了上一轮记住的内容。终端 seat 迁到托管 ACP 可直接走这条路，不需要新机制。
- suga 终端会话走 `gofer hook generic`（suga 文档 `docs/usage/gofer-personal-agent.md`）。generic 方言已在 `CatchUpAgent` 里，SessionStart 会注入 needs-reply 约定；suga 的 Stop payload 带 `last_assistant_message`，`splitNeedsReply` 会解析它。所以终端 suga 已覆盖。iuiq 实际缺的是 ACP / suga **job 会话**，T1 第 2–4 步已经覆盖。
