# 一期计划：常驻会话名册（seat）+ 主动找人

> issue：gofer-0py5（一期）；同批收 gofer-hzz3（推送同口径）、gofer-iuiq（非 Claude 标记通道）
> 设计：`docs/design/2026-11-18-resident-sessions-and-team-design.md` §4、§5（方向已批准）
> 状态：**待批**（2026-11-19）

## 0. 先定的三点（计划级，与设计的差异）

1. **代码与 CLI 用 `seat`（席位），中文仍叫「常驻会话」**。代码里 `resident` 已大量指「ACP 持续会话 job」（`job resume --mode session`、resident parent wake、`MailResidentAgent`），再用会混淆。一个 seat 是一个长期的人手位置，先后有多个会话坐上去。
2. **底部会话标签栏只在宽屏（≥1100）出现**：手机档已有底部导航；快捷跳转并入现有命令面板（Ctrl K），不新占 `Mod+K`。
3. **ACP 会话停在 `awaiting_input` 不进「等我」**，沿用现有「会话等回复提醒」（延时后的 `session.awaiting_reply`）作为进「等我」的信号，否则每轮结束都会进。

## 1. 现状要点（代码摸底，详细行号见本计划附录 A）

- 终端会话 `agent_sessions.session_id` 就是 CLI 自己的会话 id；Claude Code `--resume` 后不变（用户确认）；同 id 重新注册会回到 running 并轮换 incarnation。
- 一个目录可能有多个会话：本机标记文件每目录只有一份，`resolveCurrentSession` 多条时报错 → 绑定必须显式确认，不按目录猜。
- SessionStart 有两条注入链：`gofer repo prime --hook-json`（8 KiB 预算，已按 compact / resume 判断时机，charter 放这里）与 hookrelay（`needsReplyReminder(topicReminder(catchUp(...)))`，`CatchUpAgent` 只放行 claude / codex / generic）。
- 「等我」至少四个口径：today / inbox（宽限 + needs_reply + 监督）、工作台 attention（无宽限）、前端 `pendingAlert` 右下角 toast（任何未确认 decision）、服务端 `NotifySessionWaiting`（回合一开就发）。Web Push 只认 job 事件，终端会话进不去；前端没有任何 `new Notification` / 失焦通知。
- 现成可复用：`sessionrelay.PlanResumeForIn`（resume 的 argv 与目录）、`MailResidentAgent`（按名字记住一个 ACP 会话并重启的先例）、会话催办 nudge（定时送话、失败自动暂停）、`today` 的 `gateRelayTurns` / `Snooze`。

## 2. 任务

| # | 内容 | 执行方 | 依赖 |
|---|---|---|---|
| T0 | 真机核实：Claude 的 ACP 适配器能否 `session/load` 终端 Claude Code 写下的会话（同一 cwd、同一会话 id）；结论写进设计 §4.3 | 我（主机 exec job） | — |
| T1 | seat 数据层与服务 + HTTP | sonnet | — |
| T2 | seat CLI、charter 注入、SessionStart 绑定提示 | suga | T1 |
| T3 | 「等我」单一来源与推送同口径（hzz3） | sonnet | — |
| T4 | web：名册侧栏、顶部弹卡、失焦浏览器通知、再提醒、宽屏标签栏 | suga | T1、T3 |
| T5 | 非 Claude 会话的「需要回复」标记通道（iuiq） | suga | T3 |
| T6 | seat 定时（一·补）：到点向当前会话送固定话术 | suga | T1、T2 |
| T7 | 合并、verify、真机验收、文档 | 我 | 全部 |

### T1 seat 数据层与服务（sonnet）

- 表 `seats`：`id`、`caller_id`（归属）、`name`（同 caller 内唯一）、`project_key`、`cwd`、`purpose`、`charter`（正文或仓库相对路径，二选一）、`kind`（`terminal` | `acp`）、`agent`、`current_ref`（终端会话 id 或 ACP job id）、`pinned`、`ord`、`archived_at`、时间戳；表 `seat_bindings`（seat_id、ref、bound_at、unbound_at、reason）。基础 schema `CREATE TABLE IF NOT EXISTS`，过 `TestOpenUpgradesPreviousReleaseSchema`（G057）。
- 服务（新包 `internal/seat`，G022：依赖 jobstore / sessionrelay 接口，不反向 import）：
  - 增删改、排序、归档；`Bind(seat, ref)`（同一 ref 只能坐一个 seat，换绑记历史）、`Unbind`。
  - **自动续绑**：同一会话 id 重新注册（resume）保持绑定，什么都不用做；会话 ended 不解绑（下次 resume 回来仍是它）。
  - **绑定候选**：给定 (caller, cwd)，返回该目录下 `current_ref` 为空、或当前会话已 ended / offline 的 seat。
  - **打开计划**：`OpenPlan(seat)` → 终端类复用 `PlanResumeForIn` 得到 argv 与绝对目录，拼成一条可在终端执行的命令（`cd <abs> && <agent> --resume <id>`，按 OS 给 sh / PowerShell 两种写法）；当前会话不可续时返回「新开」命令 + 说明；acp 类返回工作台线程 id。
  - **状态**：`Status(seat)` = 当前会话状态 + 是否在「等我」里（调 T3 的单一判定）+ 未读；名册列表一次批量算。
- HTTP（G056 登记 `routeDecls` 与 catalog，`go generate` 更新 permissions.md）：`GET/POST /v1/seats`、`GET/PATCH/DELETE /v1/seats/{id}`、`POST /v1/seats/{id}/bind`、`POST /v1/seats/{id}/unbind`、`GET /v1/seats/{id}/open-plan`、`POST /v1/seats/order`。权限：本人的 seat 不需额外权限；读写他人的 seat 用新权限 `seat.manage.others`（风险中、不可授予会话）。
- 会话注册视图 `sessionView` 加 `seat`（已绑定的名字与 charter 摘要）与 `seat_candidates`，供 T2 的 hook 提示用。
- 用例：增删改与唯一名；换绑历史；resume 同 id 绑定不变；候选只含空位与已结束；open-plan 的两种命令；schema 升级用例；路由声明覆盖。

### T2 seat CLI 与注入（suga）

- `gofer seat ls|add|show|set|rm|bind|unbind|open|order`（顶级资源名词，G033 例外理由：与 job / session 同级）。`bind` 不带 ref 时取当前会话（`AgentSessionEnvKeys` / `resolveCurrentSession`）；`open <name>` 打印 open-plan 的命令，`--exec` 直接在当前终端执行（仅本机 server 视角）。
- charter 注入：`repo prime --hook-json` 在 startup / resume / compact 时，若当前会话坐在某个 seat 上，追加一节「常驻会话：<名字>（<用途>）」+ charter 正文（按 prime 预算截断，路径型 charter 读仓库文件）。
- 绑定提示：hookrelay SessionStart 在 `seat_candidates` 非空且当前会话未绑定时追加一行：「本目录有常驻会话 X（用途）空着；若这就是它，执行 `gofer seat bind X`」。只提示，不自动绑。
- skill references（G045）：`sessions-relay.md` 加 seat 一节；`commands.md` 补命令。
- 用例：CLI 分发测试（真实 gcli 分发，照 `review-check-structure-vs-siblings`）、prime 注入片段、提示只在有候选且未绑定时出现。

### T3 「等我」单一来源（sonnet）

- 把 `httpapi.waitingFor` 的组装抽到编排层（不依赖 rux.Context），返回 `inbox.Waiting` 同结构；HTTP、名册状态、推送复核都调它（G021）。
- 终端会话推送（hzz3）：`OpenTurn` 不再立即 `NotifySessionWaiting`；改为回合打开满宽限期（`relayGraceSec`）时，若仍满足进门条件（needs_reply、未监督、未 ack）才发；ack / 作答 / 被监督则取消。重启后按未关闭回合重建定时。
- Web Push 接 `session.waiting`（放宽 `webpush.dispatch` 对 job 事件的前提，payload 带 `/workbench?thread=r:<sid>`）。
- 工作台 attention 改用同一判定（补上宽限期）；前端 `pendingAlert` 的数据源改为「等我」新增项（T4 用）。
- 用例：宽限期内作答不推送；满宽限仍在等则推送一次；监督中不推送；重启后定时重建；工作台与 today 同一结果。反向验证前三条。

### T4 web（suga）

- **名册侧栏**：工作台侧栏顶部「常驻会话」分区，按工作区分组、组内按 `pinned` 再 `ord`，**打开或回复不改变顺序**；每行名字、用途、状态点（回复中 / 等我 / 失败 / 空闲 / 离线）、未读点；行菜单：在终端继续（复制 open-plan 命令）、打开线程、编辑、归档；拖动排序调 `/v1/seats/order`。分区顶部「需要我 N」汇总。
- **顶部弹卡**：App 级全局组件，「等我」出现新项且该会话不在当前屏时，顶部中间弹一张卡（一次一件，其余 +N），按钮：作答（短回复直接送）/ 批准 / 打开 / 稍后；关掉只隐藏。替换现有右下角 `pendingAlert` toast（同一数据源，避免两处弹）。
- **失焦浏览器通知**：`document.visibilityState === 'hidden'` 时，新「等我」项经已注册的 Service Worker `showNotification`（tag = 条目 id 去重，点击 `focusOrOpen` 深链）；未授权时在设置 → 通知里引导授权。可见时不发系统通知。
- **再提醒**：一项超过 30 分钟未处理再弹 / 通知一次（每项最多一次，设置可关），「稍后」= 现有 snooze。
- **宽屏标签栏**：≥1100 时底部一条常驻会话标签（只列 pinned 的 seat），转圈 = 回复中、彩点 = 等我 / 未读；命令面板加「跳到常驻会话 <名字>」。
- i18n 中英；用例照 `threadGroups.test.ts` / `appShellRender.test.ts` / `*Wiring.test.ts` 写法：排序稳定、弹卡队列与 +N、隐藏时走通知分支、再提醒只一次。

### T5 非 Claude 标记通道（suga，iuiq）

- 核实 suga 的 hook 输出能否承载 additionalContext（suga v0.56 有 `hooks.commands`）；能则把 suga 加进 `CatchUpAgent` 并让 `[needs-reply]` 解析覆盖 suga 的 Stop 文本；不能则给 suga / ACP 会话一个等价的显式标记（如结束回合时的结构化字段），不做正文启发式。
- 用例：suga 会话带标记的回合进「等我」，不带的不进。

### T6 seat 定时（suga，一·补）

- seat 加可选 `schedule`（cron + 话术）。终端类：生成 / 更新一条会话催办（nudge 加 `cron` kind，沿用失败 3 次自动暂停）；当前会话不在线时进「等我」提示「去打开 X」，不替人起终端。acp 类：到点 `job say`；会话已结束则按 `MailResidentAgent` 的方式重启同一会话后再送。
- 用例：到点送话、离线进「等我」、acp 结束后重启再送。

### T7 验收（我）

- 真机：主机上一个 Claude Code 终端会话 `seat add` + `bind` → 关掉 → `seat open` 一条命令续上（同 id）→ charter 在 resume / compact 后出现 → 回合标 `[needs-reply]` 满 5 分钟进「等我」、弹卡、页面隐藏时系统通知、作答后消失 → 30 分钟再提醒一次。截图存 `tmp/acp-trial/png/s9-*.png`。
- gofer-verify 全绿；CHANGELOG、capabilities（G058）、skill references。

## 3. 反向验证

- T1：去掉「resume 同 id 保持绑定」→ 用例失败；去掉候选的「已结束」条件 → 候选用例失败。
- T3：去掉宽限期复核 → 「宽限期内作答不推送」失败；去掉监督判断 → 「监督中不推送」失败。
- T4：去掉排序稳定（按活动时间）→ 排序用例失败。

## 4. 不在一期

流程推进（二期）、Team（三期）、ACP 原地迁入终端会话（取决于 T0，结论可行则二期前单独做）、多人共享 seat。

## 附录 A 代码位置（摸底，2026-11-19）

- 会话表 `internal/jobstore/store.go:525`、迁移 `:2425–2507`；模型 `internal/jobstore/sessions.go:290–396`；upsert `:615`、复活轮换 `:705–745`。
- 登记：`internal/hookrelay/run.go:457/485/502`；`internal/httpapi/session_handler.go:568/900`、`sessionView :238`；`internal/sessionrelay/service.go:492/615/642`。
- 注入：`internal/commands/repo.go:86–136/303/463`、`internal/tracker/prime.go:15`；`internal/hookrelay/run.go:206–226/889/922/935`、`needs_reply.go:17/24/39`、`prompt_memory.go:34/109`。
- resume：`internal/sessionrelay/resume.go:16/48/73/114`、`deliver.go:1010`；`internal/agent/registry.go:237/286`。
- 「等我」：`internal/today/cards.go:37/236/393`、`relay_gate.go:8/18`、`snooze.go`；`internal/httpapi/inbox_handler.go:118`；`internal/inbox/waiting.go:65/105/160`；工作台 attention `internal/workbench/service.go:600–633`。
- 推送：`internal/sessionrelay/service.go:977`；`internal/job/notify_dispatch.go:59/79/247`；`internal/webpush/dispatch.go:55/306`；`web/public/sw.js`；`web/src/ui/NeedsMeButton.vue:73–80`、`utils/pendingAlert.ts`。
- 布局：`web/src/App.vue`、`web/src/ui/AppShell.vue:173–383`、`utils/shell.ts`；`web/src/components/workbench/WorkbenchSidebar.vue`、`threadGroups.ts`。
- 先例：`internal/jobstore/mail_resident.go`；`internal/sessionrelay/nudge.go:66/210/254`。
