# N3：决策中心首页「今天」（WEB-17 + WORK-06）

> 状态：设计稿 v3，**已确认（2026-10-09，§9 各项按默认）**，进入实施。原型：[`n3-today-preview.html`](n3-today-preview.html)。
> 总规划见 [`plans/2026-10-08-next-phases-plan.md`](../plans/2026-10-08-next-phases-plan.md) §N3。

## 设计原则

**保持专注、只看重点、简明清晰、多工作多 agent 并行。** 每个取舍都按这四条检验：

1. **专注**：首页只回答两个问题——「现在要我拍板的是什么」「并行的这些活各自到哪了」。其他一律下钻（详情页 / 抽屉）或留在各自页面。
2. **重点**：队列默认只展开最重要的 5 张，其余折成一行「还有 N 张 · 专注处理」；排序先看「卡住了多少」。
3. **简明**：多余信息默认不显示，**想看时点开、跳转或进一步跟进**。卡片固定 4 行（来源与等待 / 标题 / 一句话 / 操作）；阻塞明细、管家理由、验收数据收在卡内「详情」，时间线与用量在泳道抽屉，diff / 日志跳详情页。
4. **并行**：进行中区是「泳道」——每条并行的工作 / plan 一行，一眼看完全部 agent 在干什么、谁停了、谁出问题。

v3 相对 v2 的减法：去掉「为什么找我」筛选片和批量操作（暂不做，见 §8）；早报从卡片缩成一行；阻塞说明在卡上只留「卡住 N」短标记，管家理由、验收数据与改动摘要都收进「详情」；管家建议只体现为一个「按建议：批准」主按钮；工作项里程碑卡墙改为泳道行，时间线与用量移进抽屉；「已稍后 / 已处理」降为队列底部的小链接；状态条只留 runner、今日用量、管家、版本四项。

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

工作台的「等你」只覆盖其中一部分；Dashboard 是计数墙，不能操作；并行跑着的工作 / plan / agent 要翻三个页面才拼得出全貌。默认落地页 `/dashboard` 回答不了「现在我该做什么」。

## 1. 页面结构（`/today`，成为默认落地页）

自上而下，手机单列同序：

1. **一行早报**：「自上次打开：完成 12 · 失败 1 · 新提交 9 ▸」。点开展开当天早报（`/v1/work-items/digest`，含管家点评），默认折叠。不另调模型。
2. **待我决策**：统一卡片队列（§2），默认展开前 5 张，其余一行「还有 N 张 · 专注处理」。队列为空时一行「没有等你的事 · 今天处理了 N 张」。
3. **并行中**：泳道列表（§3），头部一行汇总「8 条并行 · 5 个 agent 在跑 · 1 停滞 · 1 有风险」。
4. **状态条**（贴底）：runner、今日用量、管家、版本四项，点各项跳对应页面。正常时低对比；有异常的那一项（runner 离线、当日用量超预算阈值、管家巡检失败）变色并排最前。

顶栏「待我决策 N」：任何页面点它或按 `g` `d` 弹出右侧浮层，复用同一卡片组件，处理完不离开当前页。

Dashboard 保留（统计墙，改版见 gofer-yelm），导航「观察」组首项改为「今天」。

## 2. 待我决策

### 2.1 后端聚合 `GET /v1/today`

```jsonc
{
  "digest": { "since_last": {"jobs_done": 12, "jobs_failed": 1, "commits": 9}, "title": "…", "text": "…", "commentary": "…" },
  "decisions": [DecisionCard],
  "snoozed": 2,
  "status": { "usage_today": {...}, "steward_today": {...}, "runners": {...}, "version": "…", "alerts": ["runner w-mac-win10 离线"] },
  "generated_at": 1760000000
}
```

泳道单独一个接口 `GET /v1/today/lanes` → `{lanes:[Lane], summary:{total, agents_running, attention}}`（独立刷新，订阅 `work` / `plans` / `jobs` / `sessions`）。

`?since=<ts>` 由前端带上次打开时间（localStorage），服务端据此算 `since_last`；不带则按当天 0 点。水位 = 上一次访问首页的**开始时间**：页面可见满 10 秒后，离开路由 / 切走标签页 / 关页时才写入，切回来算新的一次访问；路过不改水位。

`DecisionCard`：

| 字段 | 说明 |
|---|---|
| `key` | 稳定 id：`<kind>:<ref>` |
| `kind` | `interaction` / `decision` / `relay` / `review` / `work` / `suggestion` / `merge` / `plan_blocked` |
| `urgency` | `now`（会超时）/ `blocking`（有 agent 在等）/ `normal` |
| `blocks` | `{score, text}`：阻塞了什么，一行（§2.2） |
| `title` / `project_key` / `waiting_since` / `expires_at` / `activity_at` | 展示与排序；`activity_at` 用于稍后唤醒 |
| `summary` | 一句话，**只取现成字段、不另调模型**：interaction=`prompt`；decision=`question`；relay=会话 `last_message`；review=job 汇报首段（≤120 字）；work=`blocker_text` 否则 `summary`；suggestion=「建议把状态改为 X：理由」；merge=建议文本；plan_blocked=阻塞 todo 的错误摘要 |
| `review` | 仅 review：`{commits, adds, dels, verify, digest?}` |
| `refs` | `job_id` / `interaction_id` / `decision_id` / `session_id` / `work_item_id` / `plan_id` / `todo_id` |
| `actions[]` | `{id, label, style, needs_text?}`，后端按种类生成，最多 3 个主操作；所有卡统一可「稍后」 |
| `advice` | 可空，管家建议 `{text, action_id?, digest?, by, at}`，一行（§5） |

纳入规则：

- interaction：全部 `pending`。
- decision：`state=OPEN`；`kind=relay` 归为 relay 卡，同会话只出一张。
- review：`needs_review` 的 job；**exec agent 默认不纳入**（开关在队列右上角）。
- work：`needs_me`、`needs_onsite`、提醒 / 搁置到期。
- suggestion：只纳入 `status_hint` 与 `goal`；merge 建议全部纳入。
- plan_blocked：`status=blocked` 的 plan。
- **纯信息不进队列**：job 完成 / 失败、会话汇报只体现在早报一行和泳道里。
- 同一工作项同时命中 work 与 suggestion 时合并为一张 work 卡。

### 2.2 排序：先看卡住了多少

`blocks.score` 规则固定、可解释（`blocks.text` 就是得分来源的那句话）：

| 被卡住的东西 | 计分 |
|---|---|
| plan 中依赖这张卡的后续 todo（直接 + 传递） | 每项 3 |
| 占着 agent 进程 / 会话锁 / 并发名额 | 2 + 每 10 分钟 1，封顶 8 |
| 占着 worktree / 目录锁 | 1 |
| 工作项 `needs_me` 且有在线会话 | 2 |

排序：`urgency=now` 最前；其余按 `score` 降序，同分等得久的在前。分组标题「会超时 / 卡住别人 / 可稍后」。

### 2.3 卡片骨架与操作

```
[工具审批] orders-api · 等了 6 分钟 · 12 分钟后超时 · 卡住 2
codex 请求执行命令
要在仓库根执行 go test ./... -race，需要写 tmp/。
[按建议：批准] [拒绝]                        详情 ▸  稍后
  └ 详情（点开才显示）：卡住 codex 会话 6 分钟、plan 后面 1 项 / 管家理由 / 验收数据与改动摘要
```

- 管家给了可执行建议时，对应操作变成最前面的主按钮「按建议：批准」，不再单独占一行；理由在「详情」。
- 「卡住 N」是阻塞项数的短标记，明细在「详情」。

| kind | 主操作 |
|---|---|
| interaction | 各 option（批准 / 拒绝 / …，最多 3 个）。不设「交给管家」：现有 `punt` 的语义是标 `needs_human`（留给人、管家不再接手），卡已在人面前，没有意义 |
| decision | 选项按钮，或「回复」展开输入框 |
| relay | 回复、已读（确认该会话全部未读 turn，`refs.decision_ids`） |
| review | 通过、附意见重跑（= 退回 + 自动续投）、看 diff；「退回」在 job 详情 |
| work | 回复、请求汇报、搁置 |
| suggestion / merge | 采纳、忽略 |
| plan_blocked | 继续、打开 plan |

- **回复输入框不常驻**：点「回复」才在卡内展开，保持卡片紧凑。
- **撤销窗口**：操作后卡片立刻收起，底部 toast「已批准 · 撤销 5s」，倒计时结束才调用写接口（页面卸载时立即发出，不丢；`today.action` 审计在写成功后才记）。写成功后的下一次重拉若仍返回这张卡（如「请求汇报」后工作项仍是等我），卡重新显示。`expires_at` 不足 30 秒的立即发送。
- **稍后**：1 小时 / 明早 9:00 / 等相关 job 结束。新表 `today_snooze(card_key PK, until_at, until_job_id, activity_at, created_at)`；卡的 `activity_at` 变新即提前回到队列并标「有新动静」。会超时的卡稍后仍按原超时规则兜底。
- **已处理**：队列底部小链接，打开抽屉列近 7 天首页处理记录（时间 / 卡 / 你的动作 / 当时的管家建议）。数据来自新审计事件 `today.action`，不另建表。

### 2.4 待验收卡

卡面只有汇报一句话和操作（通过 / 附意见重跑 / 看 diff）。「详情」里是硬数据（`3 个提交 · +214 / −37 · verify 通过`）和管家摘要（改动分组 / 风险，§5）。原始 diff 跳 job 详情验收面板看。

### 2.5 实时

订阅 WS 主题 `pending`、`jobs`、`work`、`sessions`、`plans`，`inval` / `snap` 防抖 1s 后重拉 `/v1/today`；断线回落 30s 轮询（沿用 `useLiveTopic`）。

## 3. 并行中（泳道）

### 3.1 一行一条并行的活

`Lane` 来自两类来源，合并成一个列表：未结且未搁置的**工作项**，和 `running` / `blocked` 的 **plan**（已挂在某工作项下的 plan 不单列，并入该工作项那一行）。

```
● N2 可见与可控     tools       —              ■■■✕□□□ Windows 测试失败   阻塞     3h12m
● Windows CI 提速   tools       claude·idle    -p 2 后全量 23 分钟         停滞     6h
● 导出接口改造      orders-api  codex·running  ■◧■□□  分页方案           有风险   1h40m
```

| 列 | 内容 |
|---|---|
| 状态点 | 进行中 / 等我 / 等资源 / 待验收 / 阻塞 的颜色 |
| 标题、项目 | |
| agent | 当前关联的活跃 job / 会话：`agent·状态`（running / awaiting_input / idle），多个时显示 `codex+claude` |
| 进度 | 有 plan：todo 节点点阵（完成 / 运行 / 待验收 / 失败 / 未到）+ 当前步骤名；无 plan：最近一条里程碑 |
| 健康度 | 正常不显示；有风险 / 停滞 / 阻塞才显示（§3.2） |
| 用时 | 用量、关联 job / diff / plan 跳转在抽屉 |

排序：阻塞 > 停滞 > 有风险 > 运行中 > 其余，同级按最近活动倒序。头部汇总一行；超过 12 行时折叠为「还有 N 条正常运行中」。点行打开抽屉：字段、**里程碑时间线**（「只看里程碑 / 全部」切换）、关联 job / diff，以及底部「补一句」输入框（= 现有「回复」：写日志并经会话送达；无在线会话时只记日志）。

### 3.2 健康度

后端算 `health` + `health_reason`：

- `blocked` 阻塞：plan `status=blocked`，或工作项处于「等我 / 需到现场」且有未解除 blocker（其他状态下的 blocker 只是背景说明，不标红——否则大多数泳道都会显示阻塞）；
- `stalled` 停滞：状态为进行中且超过 `work.stall_after`（默认 4h）没有日志或关联 job 活动（会话心跳不算；有 agent 在跑不判停滞）；
- `at_risk` 有风险：最近一个关联 job（按提交时间：`started_at`，远端排队中尚未开始的用 `updated_at`）失败或预算熔断；
- `ok`：其余。等资源 / 搁置的不判停滞。

### 3.3 里程碑（WORK-06）

`work_journal` 加列 `level TEXT NOT NULL DEFAULT 'detail'`（additive 迁移）。定级：

| 事件 | level |
|---|---|
| 状态变化（人、汇报或管家改的，含汇报解除阻塞后把状态交回自动推断的那一次；自动推断 / gofer 自己写的不算；只改标题 / 优先级等字段的不算） | milestone |
| 会话汇报（kind=report） | milestone |
| 关联 job 终态：成功带提交、失败、预算熔断 | milestone；其余 detail |
| decision / interaction 被**人**回答（关联到工作项时）；L0 自动作答（`auto:*`）、sup / owner 驱动作答（`agent:*`）、job 凭据作答、作答人未知的转发路径 | milestone；后几种 detail |
| 整理器流水 | detail |
| 整理器新增输出 `milestone`（≤40 字，自上次整理以来值得留一笔的事，可空） | milestone |
| 管家笔记 | 默认 detail；`gofer_work_note` 新增可选 `level` |
| 人手写日志 | milestone |

迁移时把 `report`、人写的 `note`，以及非 `system` 写的、**记录了状态变化**的 `status` 行标为 milestone，其余 detail。旧库没有「这一行是否改了状态」的列，迁移按写入格式判断：`UpdateWorkItem` 把各项改动用「；」连接，状态变化是「状态：a → b」这一段（「状态来源：」是另一个标签，不算）；另外工作项的第一行（创建）与拆分 / 合并行照写入侧口径算 milestone。无法避免的差异：字段值本身含「；状态：」的只改字段行会被误标为 milestone；旧 binary 写下的、把状态交回自动推断的那次状态变化，与现在一样按「人 / 汇报改的」算 milestone。`GET /v1/work-items/{id}/journal?level=milestone`；ItemView 增 `milestones`（最近 5 条）、`health`、`health_reason`。泳道「最近：…」取最新一条 milestone。

## 4. 专注处理

点「专注处理」进入全屏单卡：一次一张，处理后自动下一张；顶部「3 / 11」与细进度条。键盘 `j/k` 切换、`1-9` 选操作、`a` 通过 / 采纳、`r` 回复、`h` 稍后、`s` 跳过、`z` 撤销、`Esc` 退出。手机左右滑。清空后显示「本轮处理 N 张 · 按建议 M 张」并返回。不强迫清空，跳过和稍后的卡留在队列。

## 5. 管家建议（不自动批准）

- 新表 `decision_advice(card_key PK, text, action_id, digest, by, at, job_id)`；管家巡检时调用新 MCP 工具 `gofer_today_advise {card_key, text, action_id?, digest?}`；卡被处理后清理。
- 巡检提示词：review 卡写 `digest`（改动分组 / 风险，3–5 行）并给通过 / 退回建议；suggestion 卡给采纳与否；interaction 卡只在 option 明确时给建议；**decision 只补背景，不替人选**。建议文本一行（≤60 字）。
- 「按建议」= 人点击执行 `action_id`（同样走撤销窗口），审计 `today.action` 记 `advice_action_id`。管家没有直接执行这些动作的权限。
- **不做超时自动通过**：超时只按各自既有 `on_timeout` 兜底。

**实施状态（T4，2026-10-09 已完成）**与偏差：

- 表 `decision_advice` 按上文落地（additive）；写入口 `POST /v1/today/advice`，只放行人与管家凭据（管家读白名单加 `GET /v1/today`、写白名单加这一条；member / leader 凭据默认拒绝）。校验：卡在当前队列（含 exec 待验收）、`text` ≤60 字、`digest` ≤5 行、`action_id` 是该卡可一键执行的操作键（`answer:<value>` 或 `id`；回复类、看 diff / 打开 plan 不行），decision 卡不得带 `action_id`。人也能写（覆盖管家的），但状态条只数管家写的。
- **清理**：不在「处理后」删，而是在构建 `/v1/today`（以及给管家计数）时，把队列里没有对应卡的建议**按卡的源头**复核（交互仍 pending、decision 仍 OPEN、job 仍 needs_review、工作项仍等我 / 需现场 / 到期、建议仍 pending、plan 仍 blocked），源头不再等人才删。这样被「含 exec」开关或日后的「稍后」藏起来的卡不会误删建议。
- MCP 除 `gofer_today_advise` 外加了只读 `gofer_today_list`（复用 `GET /v1/today` 投影）与 `gofer_today_card`（单卡 + job 汇报 / diff 统计 / 提交 / verify，写 review 摘要用；管家凭据读不到原始 diff，摘要按 diff 统计、提交与汇报写）。
- 巡检提示词新增一步（只在有未建议的卡时出现）；没有变化的工作项、但「今天」有待建议的卡（review / suggestion / merge / interaction / decision）时巡检照样起会话。
- 待验收卡：建议的 `digest` 同时拷进 `review.digest`，「详情」里显示在改动数据下面（只显示一次）；其余卡的摘要跟在管家理由后。
- `today.action` 审计记录**当时的建议**（`advice_action_id` / `advice_text` / `advice_label`，前端没带时服务端从表里补）和 `via_advice`；「已处理」抽屉照做显示「按建议」，没照做显示「管家建议：X」。状态条「管家今日」的建议数取自每次写入另记的 `today.advice` 审计（建议行本身会被清理）。

## 6. 与现有页面的关系

三个页面回答三件不同的事，不互相替代：

| 页面 | 回答的问题 | 定位 |
|---|---|---|
| **今天** `/today` | 现在要我拍板什么？并行的活各自到哪了？ | 分诊与总览：看一眼、拍板、走人 |
| **工作台** `/workbench` | 要和这几个 agent 深入对话、盯着干活 | 执行面：多会话分屏、新会话、逐轮对话、diff |
| **Works** `/work` | 手上有哪些工作，怎么组织 | 管理面：状态分列、未整理、合并建议、日志、新建 |

「今天」只负责把人送到正确的地方：会话类卡片与泳道跳工作台对应会话（`?thread=`），工作项类打开 Works 的工作项抽屉（`?id=`，复用 `WorkDrawer`），plan 跳 Plans 详情，job 跳 job 详情。

**「等我」只有一个来源。** 现在的入口有三个（工作台「等你」列表、导航 `/review` 徽标、顶栏铃铛），合并为一个：

1. **共享决策队列组件**（`DecisionQueue` + `DecisionCard`）是唯一实现：`/today` 页内嵌；顶栏铃铛（`EscalationBell`）改为全局「待我决策 N」浮层，复用同一组件，任何页面（含工作台）可开，快捷键 `g d`。
2. **工作台**：去掉自己的「等你」列表（`WorkbenchAttention`），其 answer / review / reply 三种动作是新队列的子集；会话侧栏、分屏、composer 不动。浮层里点会话类卡片直接在工作台打开该会话。
3. **Works**：不动。泳道点工作项复用 `WorkDrawer`；首页只纳入「状态 / 目标」两类整理建议，其余与合并建议照旧留在 Works。
4. **验收台 `/review`**：保留为集中验收页，从导航常驻徽标中移除（计数并入「待我决策 N」）；待验收卡与「详情」里提供「看全部待验收」入口。复用其 `RejectDialog` 做「附意见重跑」。
5. **Dashboard**：退为统计页（改版见 gofer-yelm），默认落地改为 `/today`，Logo `homeTo` 同步。
6. **Board**：实为 job 列表，导航文案改为「Jobs」，避免与 Works 的状态分列混淆；路由不变。

导航两组：

- **工作**（原「观察」）：**今天**（默认）· 工作台 · Works · Plans · Jobs · Sessions · Issues · Dashboard。
- **配置**（原「舰队」）：Agents · Runners · Projects · Workflows · Schedules——这些都是「配好后很少再看」的对象；Workflows / Schedules 平时极少打开，从主组移到这里。路由不变。
- 顶栏「新建 cron」按钮去掉（入口在 Schedules 页内已有 / 补一个），只保留「新建 job」。

## 7. 分步实施

| 步 | 内容 | 依赖 |
|---|---|---|
| T1 | `GET /v1/today`（digest 一行 + decisions 含 `blocks` + status）、`/today` 页决策区（前 5 张 + 专注入口）与状态条、撤销窗口、`today.action` 审计与「已处理」、默认落地页；共享 `DecisionQueue` 组件、顶栏铃铛改全局浮层、工作台去掉「等你」列表、`/review` 徽标并入、导航调整（§6） | — |
| T2 | 泳道：`lanes` 聚合（工作项 + plan、agent 状态、健康度）、行与抽屉；WORK-06 journal `level`、整理器 `milestone`、`?level=` | — |
| T3 | 专注处理模式、稍后（`today_snooze` 与唤醒） | T1 |
| T4 | 管家建议：表、MCP 工具（含 review `digest`）、巡检提示词、「按建议」 | T1 |

T1 / T2 可并行；全部完成打 v0.127（T3 / T4 量大时拆到 v0.128）。gofer-usage skill 同步：首页说明、`/v1/today`、稍后与撤销、`gofer_today_advise`、journal level、健康度配置。

## 8. 暂不做

- 「为什么找我」筛选片：分组 + 前 5 张已够聚焦，筛选片只增加噪音。卡片多到需要筛选时再加。
- 批量操作：真正需要批量的场景（一堆整理建议）先在 Works 页处理。
- 验收面板逐文件「已看」进度：太细，偏离首页「拍板」的重心。
- 首页内嵌 diff、完整时间线：一律下钻。

## 9. 确认项（2026-10-09：全部按默认）

1. 默认落地页改为 `/today`（Dashboard 保留在导航）？
2. 待验收里 exec job 默认不进首页？
3. 整理建议只把「状态 / 目标」两类放上首页？
4. 管家建议范围：review / suggestion / interaction 给建议，decision 只补背景？
5. 人手写日志一律 milestone？
6. 撤销窗口 5 秒、到点才提交？
7. 停滞阈值默认 4 小时？
8. 阻塞计分权重（每个后续 todo 3 分、占锁每 10 分钟 1 分）？
9. 队列默认展开 5 张、泳道超过 12 行折叠，这两个数合适吗？

## 10. 参考

| 来源 | 借鉴到 |
|---|---|
| LangChain Agent Inbox | 动作由数据声明（`actions[]`） |
| GitHub Copilot Mission Control | 全局浮层、运行中「补一句」 |
| Linear Inbox / Snooze | `j/k` / `h`、有新动静自动唤醒 |
| Superhuman | 处理后自动下一张、撤销窗口；不强迫清空 |
| Devin Review | 待验收先给改动分组摘要 |
| kandev pipeline | 泳道里的 todo 点阵 |
| Vibe Kanban / Conductor | 附意见重跑、多 agent 并行一览 |
| PagerDuty | 超时只兜底不放行、只在异常时显眼 |
