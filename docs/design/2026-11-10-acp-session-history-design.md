<!-- template_id: design; template_version: 1.2.0 -->
# ACP 持续会话历史与继续设计

> 状态：Draft 0.2 / 待批准
> issue：gofer-7yrg

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-10 | Claude | 初稿：现状核查（含线上浏览器实测）与方案 |
| 0.2 | 2026-11-10 | Claude | 按两轴评审修订：工作台深链能打开时间窗外的旧线程（F6，复用置顶附带机制）；筛选改为 `source=acp`，排除终端 PTY 会话（F7）；「显示已结束」改服务端 `live=1`（F8）；项目筛选走服务端（F10）；ACP 页签抽组件控制行数（C2）；同步文件与测试（C1/C3）；验收可观察化（F9） |

需要用户：答复 Q1–Q3（或接受全部推荐项），并明确批准进入计划编写。批准设计不等于授权实施。

## 背景与目标

用户反馈：web 上看不到 ACP 持续会话（`job run --session`）的历史记录，结束或离开后无法重新进入、接着继续。

目标：从「会话」页能找到全部 ACP 持续会话（含已结束的），一个会话一张卡；点开即可回看完整对话，并在同一处继续发言。

## 已确认事实（2026-11-10 线上实测 + 代码）

后端能力已齐，问题出在「会话」页：

- **会话页的 ACP 页签取数有缺陷**：`web/src/views/Sessions.vue:565-578` 调 `listJobs({ limit: 100 })` 取全部类型最近 100 个 job，再在前端筛 `job.session`。job 多的时候 ACP 会话 job 全在 100 条之外，线上实测页签显示「ACP sessions 0」，勾「Show ended」也是 0；而工作台接口同期能列出十几个 ACP 会话线程。
- 该页签一张卡对应一个 job，不是一个会话：同一会话续接多次会出现多张卡；卡上唯一操作是「打开 job」。
- **工作台已能回看与继续**：
  - `GET /v1/workbench/threads` 把带 `session_state_json` 的 job 按 `session_id` 归为一个线程 `s:<sid>`，整条续接链在同一线程（`internal/workbench/service.go:377-421`），`resumable` 表示可继续，分页 + 游标，首页默认只看 7 天（`service.go:139-200`）。
  - 线上实测：打开一个已结束的 suga-acp 会话，对话完整显示，底部输入框可发送。
  - 发送走 `Turn`（`service.go:665-697`）：有 `awaiting_input` 的行就 say，否则对最新一行（`ListJobs` 按 `started_at DESC`）调 `ResumeJob`；续接沿用 `session_id`，ACP 侧用 `session/load`（`internal/acp/client.go:255-283`）。
  - 工作台支持 `?thread=<id>` 深链（`web/src/views/Workbench.vue:549`）。
- job 详情页对会话 job 也有「消息」页签、「会话链」、「Resume conversation」。
- 「会话」菜单（`/sessions`）另外两个页签是 hook 注册的终端 agent 会话与 pty 终端会话，与本 issue 无关。
- 未确认：`session/load` 时 agent 回放的历史是否会被写进新 job 的输出造成重复（`internal/runner/acp/runner.go` 未见屏蔽回放的标志）。列入验收，若重复则另立 issue，不在本次修。

## 范围与非目标

范围：会话页 ACP 页签改为按会话列出、取数正确、可分页；卡片能直达工作台继续；工作台接口加线程类型筛选。

非目标：

- 不改续接、`session/load` 与持续会话的生命周期。
- 不在会话页内嵌对话视图（工作台已有）。
- 一次性 ACP 续接（不带 `session_state`）本来就不进线程，不改。

## 总体方案

### 1. 工作台接口：两个筛选参数

`GET /v1/workbench/threads` 新增：

- `source=acp|pty|relay`（`workbench.Query.Source`）。线程只有两类 job 来源（`service.go:388-393`）：交互式 PTY job 与常驻 ACP job；另有 relay 会话。`acp` = job 来源且 `!Interactive`；`pty` = job 来源且 `Interactive`；`relay` = `Kind == relay`。不能用现有的 `kind=agent`：它只看 `SessionID` 非空（`service.go:426-429`），补录到 session id 的 PTY job 也算 agent。
- `live=1`（`Query.Live`）：只要 `RawStatus` 不是终态的线程。

两者都在 `listPage` 的 `match`（`service.go:211`，活跃 / 置顶附带的线程也先经过它）和 `buildResponse` 的同一处过滤里判断，不写第三份；判断函数放 `model.go` 或新文件（`service.go` 已 1137 行，GR104）。非法值 400，校验方式同现有 `status`（`internal/httpapi/workbench_handler.go:139-148`）。路由已声明，不涉及 G056。

### 2. 工作台深链能打开时间窗外的线程

现状：工作台首页只取 7 天内 + 活跃 + 置顶的线程（`web/src/components/workbench/threadPaging.ts:23`），`?thread=<id>` 只在已加载的线程里找（`Workbench.vue:581`），找不到就一直等待，没有按 id 取单条线程的接口。从会话页点一个更早的会话会打不开。

方案：复用「置顶线程随首页附带、不受时间窗限制」的现成机制（`listPage` 把置顶 id 传给 `LoadWorkbenchPage` 的 `PinnedIDs`）。

- 接口新增 `include=<thread id>`（`Query.Include`，单个，格式按现有线程 id 校验）：仅在首页（无 cursor）并入本次请求的 `PinnedIDs`，照常过 `match`（含 `Keep` 可见性），不写入用户的置顶偏好。
- 工作台首页请求在路由带 `?thread=` 时附带 `include`；路由的 `thread` 变成一个未加载的 id 时，重新加载首页一次，加载完成后按现有逻辑定位。

### 3. 会话页 ACP 页签

- 取数改为 `GET /v1/workbench/threads?source=acp&limit=50&since=1`（`since=1` 表示不加时间窗，`List` 与 `listPage` 两条路径都成立），「加载更多」用 `next_cursor`；项目筛选作为 `project` 参数传给服务端，不在前端过滤（否则分页变稀）。
- 一张卡 = 一个会话线程：标题、agent、项目、runner、轮数、最近活动时间、状态（运行中 / 等待输入 / 已结束）。
- 「显示已结束」开关沿用页面现有的 `showEnded`：不勾时请求带 `live=1`，勾上不带（Q2）。页签计数 = 已加载的卡片数（有更多时显示「N+」）。
- 卡片主按钮「打开会话」→ `/workbench?thread=s:<sid>`，在那里回看与继续；次按钮「最近的 job」→ job 详情。
- 原有的工作项关联（`workForJob` / 关联选择）改用线程的最新 job id，行为不变。
- ACP 页签（取数、卡片、加载更多）抽成独立组件（如 `web/src/components/SessionsAcpTab.vue`）：`Sessions.vue` 已 1546 行，改动后行数只减不增（GR104）。现有 `web/src/views/__tests__/sessionsAcpList.test.ts` 按新取数改写：按线程出卡、`next_cursor` 追加、「打开会话」链接到 `/workbench?thread=s:<sid>`。

### 4. 文档同步（G045 / G058）

- `docs/reference/http-api.md`：`GET /v1/workbench/threads` 的 `source`、`live`、`include` 参数与 400，说明它们和 project / status / q 一样在切页前过滤。
- `skills/gofer-usage/references/web-console.md`：会话页 ACP 页签按会话列出、「打开会话」进工作台继续。
- `docs/capabilities.md`：会话 / 工作台相关行随发版更新；`CHANGELOG.md` 新增条目。

## 架构

入口 `httpapi` 解析 `source` / `live` / `include` → `workbench.Query` → `workbench` 包内过滤（`include` 并入 `PinnedIDs`）。web：`api/workbench.ts` 的 query 类型、`threadPaging.ts` 首页参数、`Workbench.vue` 深链重载、新 ACP 页签组件与 `Sessions.vue` 的接入。G021 / G022 方向不变，不改 schema。

## 关键流程

1. 用户进「会话」→「ACP 会话」页签 → `GET /v1/workbench/threads?source=acp&limit=50&since=1[&live=1][&project=]`，按会话出卡片；「加载更多」带 `cursor`。
2. 点「打开会话」→ `/workbench?thread=s:<sid>` → 首页请求带 `include=s:<sid>`，该线程不论多旧都随首页返回 → 工作台定位并显示整条续接链的对话。
3. 在输入框发言 → `POST /v1/workbench/threads/{id}/turn`：会话还在等输入就直接送达；已结束就续接（新 job，沿用 `session_id`，ACP `session/load`），回复出现在同一线程。

## 安全、数据、运维与回滚

- 只读改动；线程可见性仍走工作台现有的 `Keep`（调用方归属 / 上限）与 handler 的 `filterWorkbenchThreads`；`include` 只是让指定线程绕过时间窗，仍过同一可见性过滤，看不到的线程照样看不到。
- 回滚：前端回到旧取数，后端参数不传即无影响。

## 决策

| 编号 | 问题 | 选项 | 推荐 |
|---|---|---|---|
| Q1 | 数据源 | A：复用工作台线程接口加 `source` 筛选；B：`/v1/jobs` 加「持续会话」筛选，前端按 `session_id` 归组 | **A**。工作台已按会话归组、带可继续标记、分页与可见性过滤，B 要在前端重做一遍归组且分页按 job 切会切断会话 |
| Q2 | 已结束会话默认是否显示 | A：沿用页面「显示已结束」开关（默认不勾）；B：ACP 页签默认显示全部 | **A**。与另外两个页签行为一致；取数修好后勾上即可看到全部历史 |
| Q3 | 在哪里回看与继续 | A：跳工作台对应线程；B：会话页内嵌对话与输入框 | **A**。工作台已实现回看、继续、附图与审批交互，内嵌等于复制一套 |

## 待确认事项

- 上表 Q1–Q3，等用户答复。

## 验收

- 线上数据（只读）：ACP 页签勾「显示已结束」并加载完全部页后，卡片数等于 `GET /v1/workbench/threads?source=acp&since=1` 各页 `threads` 之和，不再是 0；同一会话续接多次只有一张卡；终端 PTY 会话不出现。
- 深链：从 ACP 页签打开一个最近活动早于 7 天、未置顶的已结束会话，工作台能定位并显示对话，可继续发言；该线程没有因此变成置顶。
- 临时 server：起一个 ACP 持续会话（`--shared-dir`），说两轮后 `job end`；在会话页找到它 → 打开会话 → 看到两轮对话 → 发一条消息 → 生成续接 job、回复出现在同一线程。检查续接 job 的输出是否重复了前两轮（只记录，见「未确认」）。
- `source=bad`、`live=x`、格式非法的 `include` 返回 400；`source=relay` 只返回 relay 线程；`include` 指向调用方不可见的线程时不返回它。
- 单测：`source` / `live` 过滤（含活跃 / 置顶附带的线程）、`include` 只在首页生效且不改偏好；web 组件测试见上。

## 结论与人工计划 Gate

后端只加三个查询参数，主要是修正会话页的取数与卡片组织，并把「继续」导向已有的工作台。等用户答复 Q1–Q3 并批准后写计划。
