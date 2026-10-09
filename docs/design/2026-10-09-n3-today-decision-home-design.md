# N3：决策中心首页「今天」（WEB-17 + WORK-06）

> 状态：设计稿，待用户确认（2026-10-09）。原型：[`n3-today-preview.html`](n3-today-preview.html)。
> 总规划见 [`plans/2026-10-08-next-phases-plan.md`](../plans/2026-10-08-next-phases-plan.md) §N3。
> 核心思路：**认知压缩**——打开首页先看到「待我决策」，工作进展压缩成里程碑，细节逐层下钻；管家只给建议、不替人批准。

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

工作台的「等你」只覆盖前三类里的 job / 中继会话部分；Dashboard 是计数墙，不能操作。默认落地页 `/dashboard` 回答不了「现在我该做什么」。

## 1. 页面结构（`/today`，成为默认落地页）

自上而下三段，手机单列同序：

1. **待我决策**：统一卡片队列。卡上一句人话摘要 + 直接操作；有管家建议时显示建议与「按建议」按钮。队列为空时折叠成一行「没有等你的事 · 上次清空 hh:mm」。
2. **进行中的工作**：每个未结、未搁置的工作项一张卡，卡内是最近的**里程碑**短时间线（≤5 条），点卡进现有工作项抽屉（字段、完整日志、会话、关联 job / diff）。
3. **状态条**：今日用量（job + 终端会话，OBS-14）、管家今日动作数、runner 水位（在线数 / 运行中 job）、服务版本。点各项跳到 Dashboard / Runners / 管家。

顶部右侧一个「专注处理」按钮（队列 > 0 时高亮）：进入逐张处理模式（§4）。

Dashboard 保留（统计墙），导航「观察」组首项改为「今天」。

## 2. 待我决策：统一卡片

### 2.1 后端聚合 `GET /v1/today`

一次返回首页三段所需数据，避免前端拼 6 个接口：

```jsonc
{
  "decisions": [DecisionCard],
  "work": [WorkMilestoneCard],
  "status": { "usage_today": {...}, "steward_today": {...}, "runners": {...}, "version": "…" },
  "generated_at": 1760000000
}
```

`DecisionCard`：

| 字段 | 说明 |
|---|---|
| `key` | 稳定 id：`<kind>:<ref>`，前端去重 / 记住已读 |
| `kind` | `interaction` / `decision` / `relay` / `review` / `work` / `suggestion` / `merge` / `plan_blocked` |
| `urgency` | `now`（会超时：interaction / decision 带 `expires_at`）/ `blocking`（有 agent 在等：interaction、decision、relay、plan_blocked）/ `normal`（review、work、suggestion、merge） |
| `title` / `project_key` / `waiting_since` / `expires_at` | 展示与排序 |
| `summary` | 一句人话，**只取现成字段、不另调模型**：interaction=`prompt`；decision=`question`；relay=会话 `last_message`；review=job 汇报首段（≤160 字）；work=`blocker_text` 否则 `summary`；suggestion=「建议把状态改为 X：理由」；merge=建议文本；plan_blocked=阻塞 todo 的错误摘要 |
| `refs` | `job_id` / `interaction_id` / `decision_id` / `session_id` / `work_item_id` / `plan_id` / `todo_id` |
| `actions[]` | `{id, label, style, needs_text?}`，由后端按种类生成（如 interaction 的各 optionId；review 的「通过 / 退回」；work 的「回复 / 请求汇报 / 搁置」） |
| `advice` | 可空，管家建议 `{text, action_id?, by, at}`（§5） |

排序：`urgency`（now > blocking > normal），同级按 `waiting_since` 升序（等最久的在前）。

纳入规则（与工作台「等你」口径对齐、并扩展）：

- interaction：全部 `pending`（不只 needs_human）。
- decision：`state=OPEN`；`kind=relay` 归为 `relay` 卡，同会话的 waiting_reply / needs_attention 只出一张卡。
- review：`needs_review` 的 job；**exec agent 默认不纳入**（与工作台一致，开关在卡片区右上角）。
- work：`needs_me`、`needs_onsite`、提醒 / 搁置到期（`due`）。
- suggestion：待采纳的 `status_hint` 与 `goal`（其余字段建议不进首页，留在 Works 卡内，避免刷屏）；merge 建议全部纳入。
- plan_blocked：`status=blocked` 的 plan。

同一工作项同时命中 work 与 suggestion 时合并为一张 work 卡，建议作为卡内次要操作。

### 2.2 卡上操作

全部复用现有写接口，前端按 `actions[].id` 调用：

| kind | 操作 |
|---|---|
| interaction | 各 option 按钮（批准 / 拒绝 / 自定义），「交给管家」=punt |
| decision | 选项按钮或文本回复 |
| relay | 文本回复（`session say`）、「已读」（ack） |
| review | 通过、退回（必填理由，可勾自动续投）、「看 diff」打开 job 详情验收面板 |
| work | 回复（写日志并经会话送达）、请求汇报、搁置到…、标记完成 |
| suggestion / merge | 采纳、忽略 |
| plan_blocked | 继续（resume）、打开 plan |

操作成功后卡片淡出；失败原地显示错误。需要更多上下文时点卡标题进对应详情页 / 抽屉。

### 2.3 实时

页面订阅 WS 主题 `pending`、`jobs`、`work`、`sessions`、`plans`，收到 `inval` / `snap` 防抖 1s 后重拉 `/v1/today`；断线回落 30s 轮询（沿用 `useLiveTopic`）。

## 3. 里程碑（WORK-06）

### 3.1 日志分级

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

### 3.2 展示

- 首页工作卡：最近 5 条 milestone（时间 + 一句话 + 状态色点），卡头是标题、状态、当前阻塞 / 下一步一行、用量。
- Works 抽屉的日志区加「只看里程碑 / 全部」切换，默认只看里程碑。
- `GET /v1/work-items/{id}/journal?level=milestone`；ItemView 增 `milestones`（最近 5 条）。

## 4. 专注处理模式

点「专注处理」进入全屏单卡视图：一次一张决策卡（含完整摘要和建议），处理后自动下一张；键盘 `j/k` 切换、`1-9` 选 option、`a` 通过 / 采纳、`r` 回复、`s` 跳过（本次会话内不再出现）、`Esc` 退出。手机上左右滑切换。队列清空显示「全部处理完」并返回首页。

## 5. 管家建议（不自动批准）

- 新表 `decision_advice(card_key PK, text, action_id, by, at, job_id)`；管家巡检时对队列里的卡调用新 MCP 工具 `gofer_today_advise {card_key, text, action_id?}` 写建议；卡消失（被处理）后建议随之清理。
- 巡检提示词加一段：对 review 卡读汇报与 diff 摘要给「建议通过 / 退回 + 理由」；对 suggestion 卡给采纳与否；对 interaction 卡只在 option 明确时给建议。**不对 decision 的业务问题替人选择**，只可补充背景。
- 卡上「按建议」= 由人点击执行 `action_id` 对应的操作，记审计 `today.advice_adopted`。管家没有任何直接执行这些动作的权限（steward 凭证仍不能 accept / answer）。
- 状态条「管家今日动作」= 当日 steward 日志条数 + 建议数 + 整理次数。

## 6. 分步实施

| 步 | 内容 | 依赖 |
|---|---|---|
| T1 | `GET /v1/today`（decisions + status）、`/today` 页决策区与状态条、默认落地页切换、导航 | — |
| T2 | WORK-06：journal `level` 迁移与定级、整理器 `milestone` 输出、`?level=` 过滤、首页工作卡与抽屉切换 | — |
| T3 | 专注处理模式 | T1 |
| T4 | 管家建议：表、MCP 工具、巡检提示词、卡上「按建议」 | T1 |

T1 / T2 可并行（后端不同文件）；每步各自 web 测试 + 后端测试；全部完成打一个版本（预计 v0.127）。gofer-usage skill 同步：首页说明、`/v1/today`、`gofer_today_advise`、journal level。

## 7. 待确认

1. 默认落地页改为 `/today`（Dashboard 保留在导航）？
2. 待验收里 exec job 默认不进首页（同工作台）？
3. 整理建议只把「状态 / 目标」两类放上首页，其余留在 Works？
4. 管家建议的范围：review / suggestion / interaction 给建议，decision 只补背景不推荐选项？
5. 「人手写日志一律 milestone」是否合适？
