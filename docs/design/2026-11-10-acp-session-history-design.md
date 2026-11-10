<!-- template_id: design; template_version: 1.2.0 -->
# ACP 持续会话历史与继续设计

> 状态：Draft 0.1 / 待批准
> issue：gofer-7yrg

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-10 | Claude | 初稿：现状核查（含线上浏览器实测）与方案 |

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

### 1. 工作台接口加 `kind` 筛选

`GET /v1/workbench/threads` 新增 `kind=agent|job|relay`（`workbench.Query.Kind`）。在分页填充的过滤链里与 project / status / q 一起判断，「活跃与置顶线程随首页附带」的那部分也要过同一筛选。非法值 400。路由已声明，不涉及 G056。

### 2. 会话页 ACP 页签

- 取数改为 `GET /v1/workbench/threads?kind=agent&limit=50&since=1`（`since=1` 表示不加时间窗），「加载更多」用 `next_cursor`。
- 一张卡 = 一个会话线程：标题、agent、项目、runner、轮数、最近活动时间、状态（运行中 / 等待输入 / 已结束）。
- 「显示已结束」开关沿用页面现有的 `showEnded`：不勾只显示未结束的，勾上显示全部（Q2）。页签计数随之。
- 卡片主按钮「打开会话」→ `/workbench?thread=s:<sid>`，在那里回看与继续；次按钮「最近的 job」→ job 详情。
- 原有的工作项关联（`workForJob` / 关联选择）改用线程的最新 job id，行为不变。

## 架构

入口 `httpapi` 解析 `kind` → `workbench.Query.Kind` → `workbench` 包内过滤。web 只改 `Sessions.vue` 与 API 客户端的 query 类型。G021 / G022 方向不变，不改 schema。

## 关键流程

1. 用户进「会话」→「ACP 会话」页签 → `GET /v1/workbench/threads?kind=agent&limit=50&since=1`，按会话出卡片；「加载更多」带 `cursor`。
2. 点「打开会话」→ `/workbench?thread=s:<sid>` → 工作台显示整条续接链的对话。
3. 在输入框发言 → `POST /v1/workbench/threads/{id}/turn`：会话还在等输入就直接送达；已结束就续接（新 job，沿用 `session_id`，ACP `session/load`），回复出现在同一线程。

## 安全、数据、运维与回滚

- 只读改动；线程可见性仍走工作台现有的 `Keep`（调用方归属 / 上限）。
- 回滚：前端回到旧取数，后端参数不传即无影响。

## 决策

| 编号 | 问题 | 选项 | 推荐 |
|---|---|---|---|
| Q1 | 数据源 | A：复用工作台线程接口加 `kind` 筛选；B：`/v1/jobs` 加「持续会话」筛选，前端按 `session_id` 归组 | **A**。工作台已按会话归组、带可继续标记、分页与可见性过滤，B 要在前端重做一遍归组且分页按 job 切会切断会话 |
| Q2 | 已结束会话默认是否显示 | A：沿用页面「显示已结束」开关（默认不勾）；B：ACP 页签默认显示全部 | **A**。与另外两个页签行为一致；取数修好后勾上即可看到全部历史 |
| Q3 | 在哪里回看与继续 | A：跳工作台对应线程；B：会话页内嵌对话与输入框 | **A**。工作台已实现回看、继续、附图与审批交互，内嵌等于复制一套 |

## 待确认事项

- 上表 Q1–Q3，等用户答复。

## 验收

- 线上数据（只读）：ACP 页签勾「显示已结束」后列出的会话数与 `GET /v1/workbench/threads?kind=agent&since=1` 一致，不再是 0；同一会话续接多次只有一张卡。
- 临时 server：起一个 ACP 持续会话（`--shared-dir`），说两轮后 `job end`；在会话页找到它 → 打开会话 → 看到两轮对话 → 发一条消息 → 生成续接 job、回复出现在同一线程。检查续接 job 的输出是否重复了前两轮（只记录，见「未确认」）。
- `kind=bad` 返回 400；`kind=relay` 只返回 relay 线程。

## 结论与人工计划 Gate

后端只加一个筛选参数，主要是修正会话页的取数与卡片组织，并把「继续」导向已有的工作台。等用户答复 Q1–Q3 并批准后写计划。
