# N3：决策中心首页「今天」（WEB-17 + WORK-06）

> 状态：设计稿 v2，待用户确认（2026-10-09）。原型：[`n3-today-preview.html`](n3-today-preview.html)。
> 总规划见 [`plans/2026-10-08-next-phases-plan.md`](../plans/2026-10-08-next-phases-plan.md) §N3。
> 核心思路：**认知压缩**——打开首页先看到「待我决策」，并且知道先处理哪张（谁被卡住得最多）；工作进展压缩成里程碑和阶段链，细节逐层下钻；管家只给建议、不替人批准。
>
> v2 变更（参考调研后）：按阻塞程度排序并写明阻塞了什么；「为什么找我」筛选；稍后再看（有新动静自动回来）；「已处理」历史与撤销窗口；待验收卡先摘要后 diff、附意见重跑；顶部早报卡；进行中区加健康度、补一句、plan 阶段链；全局浮层与批量处理；状态条只在异常时显眼。参考来源见 §8。

## 0. 现状与问题

「需要人」的信号现在散在六处：

| 来源 | 现在在哪看 | 动作 |
|---|---|---|
| ACP 交互（工具审批 / 提问） | 顶栏铃铛、job 详情 | `POST /v1/jobs/{id}/interactions/{iid}/answer`、`punt` |
| OPEN decision（`gofer_ask_human`、中继会话轮次） | 铃铛、Plan 详情、Sessions | `POST /v1/decisions/{id}/answer`、`/v1/sessions/{sid}/say` |
| 待验收 job | 「待验收 N」→ `/review` | `accept` / `reject` |
| 工作项 needs_me / needs_onsite / 到期提醒 | Works 徽标 | 改状态、写日志、请求汇报 |
| 整理建议、合并建议 | Works 卡片内 | `suggestions/{field}/accept|dismiss`、`merge-suggestions/{n}/…` |
| 阻塞的 plan | Plans | `plan resume`、改 todo |

工作台的「等你」只覆盖前三类里的 job / 中继会话部分；Dashboard 是计数墙，不能操作。默认落地页 `/dashboard` 回答不了「现在我该做什么」。v1 稿把这些合成了一个队列，但仍只按「类别 + 等了多久」排，看不出**哪张卡卡住的东西最多**；处理完的卡也找不回来。

## 1. 页面结构（`/today`，成为默认落地页）

自上而下，手机单列同序：

1. **早报卡**（可折叠，折叠状态按天记在 localStorage）：当天的工作摘要（`/v1/work-items/digest`，即每天推送的早报，含管家点评），加一行「自你上次打开以来」：完成 N 个 job、失败 N、新提交 N、新进队列 N。正文里的工作项 / job 可点。不另调模型。
2. **待我决策**：统一卡片队列（§2）。头部一行「为什么找我」筛选片（全部 / 审批 / 提问 / 回复 / 验收 / 工作 / 建议 / plan，各带计数），右侧「已稍后 N」「已处理」两个入口和「专注处理」按钮。队列为空时折叠成一行「没有等你的事 · 今天处理了 N 张 · 上次清空 hh:mm」。
3. **进行中**（§3）：两个页签——「工作项」（里程碑卡，带健康度和「补一句」）与「计划」（每个运行中 / 阻塞的 plan 一行阶段链）。
4. **状态条**（贴底）：今日用量（job + 终端会话，OBS-14）、管家今日动作数、runner 水位、服务版本。**正常时整条低对比**；有异常的那一项（runner 离线、当日用量超预算阈值、管家巡检失败）变色并排到最前。点各项跳到 Dashboard / Runners / 管家。

Dashboard 保留（统计墙，后续改版见 gofer-yelm），导航「观察」组首项改为「今天」。

## 2. 待我决策

### 2.1 后端聚合 `GET /v1/today`

一次返回首页所需数据，避免前端拼 6 个接口：

```jsonc
{
  "digest": { "title": "…", "text": "…", "commentary": "…", "since_last": {"jobs_done": 12, "jobs_failed": 1, "commits": 9, "new_cards": 4} },
  "decisions": [DecisionCard],
  "snoozed": 2,
  "work": [WorkMilestoneCard],
  "plans": [PlanChainRow],
  "status": { "usage_today": {...}, "steward_today": {...}, "runners": {...}, "version": "…", "alerts": ["runner w-mac-win10 离线"] },
  "generated_at": 1760000000
}
```

`?since=<ts>` 由前端带上次打开时间（localStorage），服务端据此算 `since_last`；不带则按当天 0 点。

`DecisionCard`：

| 字段 | 说明 |
|---|---|
| `key` | 稳定 id：`<kind>:<ref>`，前端去重 / 记住已读 |
| `kind` | `interaction` / `decision` / `relay` / `review` / `work` / `suggestion` / `merge` / `plan_blocked` |
| `reason` | 「为什么找我」筛选用：`approve`（interaction）/ `ask`（decision）/ `reply`（relay）/ `review` / `work` / `suggest`（suggestion、merge）/ `plan` |
| `urgency` | `now`（会超时：interaction / decision 带 `expires_at`）/ `blocking`（有 agent 在等：interaction、decision、relay、plan_blocked）/ `normal`（review、work、suggestion、merge） |
| `blocks` | **阻塞了什么**（新）：`{score, items:[文本…]}`，后端按 §2.2 算；卡上显示为一行「卡住：plan N2 后面 3 项 · 占着 claude 会话锁 18 分钟」 |
| `title` / `project_key` / `waiting_since` / `expires_at` / `activity_at` | 展示与排序；`activity_at` 是 refs 上最近一次新事件时间，用于稍后唤醒 |
| `summary` | 一句人话，**只取现成字段、不另调模型**：interaction=`prompt`；decision=`question`；relay=会话 `last_message`；review=job 汇报首段（≤160 字）；work=`blocker_text` 否则 `summary`；suggestion=「建议把状态改为 X：理由」；merge=建议文本；plan_blocked=阻塞 todo 的错误摘要 |
| `review` | 仅 review 卡：`{commits, adds, dels, verify, digest?}`，`digest` 见 §2.5 |
| `refs` | `job_id` / `interaction_id` / `decision_id` / `session_id` / `work_item_id` / `plan_id` / `todo_id` |
| `actions[]` | `{id, label, style, needs_text?, undoable}`，由后端按种类生成（如 interaction 的各 optionId；review 的「通过 / 退回 / 附意见重跑」；work 的「回复 / 请求汇报 / 搁置」）。所有卡统一追加 `snooze` |
| `advice` | 可空，管家建议 `{text, action_id?, by, at}`（§5） |

纳入规则（与工作台「等你」口径对齐、并扩展）：

- interaction：全部 `pending`（不只 needs_human）。
- decision：`state=OPEN`；`kind=relay` 归为 `relay` 卡，同会话的 waiting_reply / needs_attention 只出一张卡。
- review：`needs_review` 的 job；**exec agent 默认不纳入**（与工作台一致，开关在卡片区右上角）。
- work：`needs_me`、`needs_onsite`、提醒 / 搁置到期（`due`）。
- suggestion：待采纳的 `status_hint` 与 `goal`（其余字段建议不进首页，留在 Works 卡内，避免刷屏）；merge 建议全部纳入。
- plan_blocked：`status=blocked` 的 plan。
- **纯信息不进队列**：job 完成 / 失败通知、会话汇报等只进早报的「自上次以来」和进行中区的时间线，不生成卡片，否则「清空」没有意义。

同一工作项同时命中 work 与 suggestion 时合并为一张 work 卡，建议作为卡内次要操作。

### 2.2 排序：先看卡住了多少

`blocks.score` 由后端按卡算，规则固定、可解释（卡上逐条列出得分来源）：

| 被卡住的东西 | 计分 |
|---|---|
| plan 中依赖这张卡的后续 todo（直接 + 传递） | 每项 3 |
| 占着 agent 进程 / 会话锁 / `max_concurrent` 名额（interaction、持续会话 relay） | 2 + 每 10 分钟 1，封顶 8 |
| 占着 worktree / 目录锁 | 1 |
| 工作项状态为 `needs_me` 且有关联在线会话 | 2 |
| review：同一 plan 后续 todo 在等这次验收 | 按第一行计 |

排序：`urgency=now` 永远在最前（会超时的先处理）；其余按 `blocks.score` 降序，同分按 `waiting_since` 升序（等最久的在前）。分组标题改为「会超时 / 卡住别人 / 可稍后」——`score>0` 的卡归入「卡住别人」，不再只看种类。

### 2.3 卡上操作、撤销窗口与「已处理」

全部复用现有写接口，前端按 `actions[].id` 调用：

| kind | 操作 |
|---|---|
| interaction | 各 option 按钮（批准 / 拒绝 / 自定义），「交给管家」=punt |
| decision | 选项按钮或文本回复 |
| relay | 文本回复（`session say`）、「已读」（ack） |
| review | 通过；退回（必填理由）；**附意见重跑**（= 退回 + 自动续投，理由即续投 prompt）；「看 diff」打开 job 详情验收面板 |
| work | 回复（写日志并经会话送达）、请求汇报、搁置到…、标记完成 |
| suggestion / merge | 采纳、忽略 |
| plan_blocked | 继续（resume）、打开 plan |
| 全部 | 稍后（§2.4） |

- **撤销窗口**：点下操作后卡片立刻淡出，底部 toast「已通过 · 撤销 5s」，**倒计时结束才真正调用写接口**；点撤销则卡片原样回来、什么都没发生。例外：`expires_at` 不足 30 秒的 interaction 立即发送（toast 无撤销）。这样不需要后端支持「撤销已批准」。
- **已处理**：队列头部「已处理」打开右侧抽屉，列近 7 天在首页处理过的卡：时间、卡标题、你的动作、当时的管家建议（是否按建议）、跳转详情。数据来自审计日志（新增 `today.action` 审计事件，字段 `card_key / action_id / advice_action_id`），不另建表。
- 操作失败时卡片回到原位并显示错误。需要更多上下文时点卡标题进对应详情页 / 抽屉。

### 2.4 稍后再看

- 卡上「稍后」菜单：1 小时 / 今晚 20:00 / 明早 9:00 / **等相关 job 结束**（卡有 `job_id` 且 job 未终态时可选）。
- 新表 `today_snooze(card_key PK, until_at, until_job_id, activity_at, created_at)`。`/v1/today` 过滤掉生效中的稍后卡，返回 `snoozed` 计数；「已稍后 N」点开可提前取消。
- **有新动静自动回来**：卡的 `activity_at` 晚于稍后时记录的 `activity_at`（如会话又发来消息、interaction 被重新请求、job 失败）即视为唤醒，卡回到队列并带「有新动静」标记。卡消失（被别处处理）时清理对应行。
- `urgency=now` 的卡可以稍后，但超时规则不变（到点照常按 `on_timeout` 兜底），菜单里写明「X 分钟后会按 拒绝 兜底」。

### 2.5 待验收卡：先摘要后 diff

- 卡上一行硬数据：`3 个提交 · +214 / −37 · verify 通过 · 用量 1.1M tok`。
- 有管家巡检时，`review.digest` = 管家写的结构化摘要：改动分组（每组一句话 + 涉及文件数）、风险点、测试情况，3–6 行；卡上默认显示，原始 diff 不在卡上展开，「看 diff」进 job 详情验收面板。
- 「附意见重跑」见 §2.3。

### 2.6 实时、全局浮层与批量

- 页面订阅 WS 主题 `pending`、`jobs`、`work`、`sessions`、`plans`，收到 `inval` / `snap` 防抖 1s 后重拉 `/v1/today`；断线回落 30s 轮询（沿用 `useLiveTopic`）。
- **全局浮层**：顶栏铃铛改为「待我决策 N」，任何页面点它或按 `g` `d` 弹出右侧浮层，复用同一卡片组件和撤销窗口，处理完不离开当前页。
- **批量**：同一 `reason` 下可多选（复选框只在悬停 / 选择模式出现），批量栏只提供该类的低风险动作：建议「全部采纳 / 全部忽略」、exec 待验收「全部通过」、只读工具审批「全部批准」。批量同样走撤销窗口，逐条执行、逐条报失败。

## 3. 进行中

### 3.1 工作项（WORK-06 里程碑）

`work_journal` 加列 `level TEXT NOT NULL DEFAULT 'detail'`（additive 迁移），取值 `milestone` / `detail`。写入时按规则定级：

| 事件 | level |
|---|---|
| 状态变化（人或汇报改的；自动推断的不算） | milestone |
| 会话汇报（kind=report） | milestone |
| 关联 job 终态：成功带提交、失败、预算熔断 | milestone；其余 job 终态 detail |
| decision / interaction 被回答（关联到工作项时） | milestone |
| 整理器「整理完成 / 失败」流水 | detail |
| 整理器产出的 `milestone` 字段（新增，见下） | milestone |
| 管家笔记 | 默认 detail；`gofer_work_note` 新增可选 `level`，管家可标 milestone |
| 人手写日志 | milestone |

整理器提示词加一个可选输出 `milestone`：自上次整理以来「值得在时间线上留一笔」的一句话（≤40 字），没有就留空；有则写一条 `kind=steward, level=milestone` 日志。旧数据不回填：迁移时把 `report` / `status` / 人写的 `note` 直接标为 milestone，其余 detail。

工作卡：

- 卡头：状态、**健康度**、用量；标题；「卡在 / 下一步」一行；最近 5 条 milestone 时间线。
- **健康度**（后端算，`health` + `health_reason`）：
  - `stalled` 停滞：状态为 active 且超过 `work.stall_after`（默认 4h）没有任何日志 / 关联 job 活动；
  - `at_risk` 有风险：最近一个关联 job 失败或预算熔断、或有未解除的 blocker 超过 24h；
  - `ok` 正常：其余。状态为 waiting / parked 的不判停滞。
- **补一句**：卡底一个输入框，回车 = 现有「回复」（写日志并经会话送达）；没有在线会话时灰显并提示「无在线会话，只记日志」。
- 「里程碑 / 全部」切换保留；点卡进现有工作项抽屉（字段、完整日志、会话、关联 job / diff），抽屉日志默认只看里程碑。
- `GET /v1/work-items/{id}/journal?level=milestone`；ItemView 增 `milestones`（最近 5 条）、`health`、`health_reason`。

### 3.2 计划（阶段链）

每个 `running` / `blocked` 的 plan 一行：标题、项目、已用时，后面是 todo 节点链（按依赖拓扑序，横向，超过 8 个时折叠中间已完成的为「✓×N」）。节点样式：完成打勾、运行中高亮、`needs_review` 黄、失败红、未到虚线。点节点进 todo 对应 job，点行进 plan 详情。数据 `PlanChainRow{plan_id, title, status, started_at, nodes:[{todo_id, title, status, job_id}]}`。手机上节点链横向滚动。

## 4. 专注处理模式

点「专注处理」进入全屏单卡视图：一次一张决策卡（含完整摘要、阻塞说明和建议），处理后自动下一张；顶部进度「3 / 11」和细进度条。键盘 `j/k` 切换、`1-9` 选 option、`a` 通过 / 采纳、`r` 回复、`h` 稍后、`s` 跳过（本次会话内不再出现）、`z` 撤销上一个（撤销窗口内）、`Esc` 退出。手机上左右滑切换。

队列清空显示结束页：「全部处理完 · 本轮处理 N 张 · 按建议 M 张 · 用时 x 分」，然后返回首页。**不强迫清空**：随时退出，跳过和稍后的卡都留着。

## 5. 管家建议（不自动批准）

- 新表 `decision_advice(card_key PK, text, action_id, digest, by, at, job_id)`；管家巡检时对队列里的卡调用新 MCP 工具 `gofer_today_advise {card_key, text, action_id?, digest?}` 写建议；卡消失（被处理）后建议随之清理。
- 巡检提示词加一段：对 review 卡读汇报与 diff，写 `digest`（改动分组 / 风险 / 测试，§2.5）并给「建议通过 / 退回 + 理由」；对 suggestion 卡给采纳与否；对 interaction 卡只在 option 明确时给建议。**不对 decision 的业务问题替人选择**，只可补充背景。
- 卡上「按建议」= 由人点击执行 `action_id` 对应的操作（同样走撤销窗口），审计 `today.action` 记 `advice_action_id`；与建议不同的操作在「已处理」里标出。管家没有任何直接执行这些动作的权限（steward 凭证仍不能 accept / answer）。
- **不做超时自动通过**：超时只按各自既有的 `on_timeout`（默认拒绝）兜底，首页不新增任何自动放行。
- 状态条「管家今日动作」= 当日 steward 日志条数 + 建议数 + 整理次数。

## 6. 分步实施

| 步 | 内容 | 依赖 |
|---|---|---|
| T1 | `GET /v1/today`（digest + decisions 含 `blocks`/`reason` + status）、`/today` 页早报卡 / 决策区 / 筛选 / 状态条、撤销窗口、`today.action` 审计与「已处理」抽屉、默认落地页切换、导航 | — |
| T2 | WORK-06：journal `level` 迁移与定级、整理器 `milestone` 输出、`?level=` 过滤、健康度、工作卡（补一句）与抽屉切换；计划阶段链 `plans` | — |
| T3 | 专注处理模式、稍后（`today_snooze` 与唤醒）、全局浮层、批量 | T1 |
| T4 | 管家建议：表、MCP 工具（含 review `digest`）、巡检提示词、卡上「按建议」 | T1 |

T1 / T2 可并行（后端不同文件）；每步各自 web 测试 + 后端测试；全部完成打一个版本（预计 v0.127，T3/T4 量大时可拆到 v0.128）。gofer-usage skill 同步：首页说明、`/v1/today`、稍后与撤销、`gofer_today_advise`、journal level、健康度配置。

## 7. 待确认

1. 默认落地页改为 `/today`（Dashboard 保留在导航）？
2. 待验收里 exec job 默认不进首页（同工作台）？
3. 整理建议只把「状态 / 目标」两类放上首页，其余留在 Works？
4. 管家建议的范围：review / suggestion / interaction 给建议，decision 只补背景不推荐选项？
5. 「人手写日志一律 milestone」是否合适？
6. 撤销窗口 5 秒、到点才发送，可以吗？（代价：点完 5 秒内关页面，操作会在页面卸载时立即发出，不会丢）
7. 停滞阈值默认 4 小时，可以吗？
8. 阻塞计分规则（§2.2）是否合理，尤其「每个后续 todo 3 分」与「占锁每 10 分钟 1 分」的权重？

## 8. 参考

| 来源 | 借鉴到 |
|---|---|
| LangChain Agent Inbox | 动作由数据声明（`actions[]`） |
| GitHub 通知收件箱（Done / 按原因过滤 / 批量） | 「已处理」、「为什么找我」、批量 |
| GitHub Copilot Mission Control | 全局浮层、运行中「补一句」 |
| Linear Inbox / Snooze / Pulse | `j/k` / `h`、有新动静自动唤醒、早报卡、健康度 |
| Superhuman | 处理后自动下一张、撤销、结束页；不强迫清空 |
| Devin Review | 待验收先给改动分组摘要 |
| kandev（pipeline） | 计划阶段链（其验收弹窗的逐文件「已看」进度不采用：首页只管拍板，细到文件会喧宾夺主） |
| Vibe Kanban / Conductor | 附意见重跑 |
| PagerDuty | 超时只兜底不自动放行、只在异常时显眼 |
