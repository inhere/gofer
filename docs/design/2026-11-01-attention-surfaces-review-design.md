# 注意力面复盘：今天 / 工作项 / 收件箱为什么没人用，以及怎么收

> issue：gofer-cnxy
> 状态：0.2 已批准（见 §7）
> 相关：`docs/design/2026-10-09-dashboard-redesign-design.md`、`2026-10-23-ui-redesign-design.md`、`2026-10-28-inbox-triage-design.md`；外部参照 dsh-superagent-prototype 的 `docs/000-philosophy.md`、`docs/005-panel.md`（本地副本在 WP `local/dsh-superagent-prototype/`），以及 xhen 的调研 `inhere-tools/xhen/docs/research/2026-10-31-dsh-superagent-prototype.md`。

## 1. 背景

gofer 的定位是围绕「实施、交付、跟进」的控制平面，目标是**提升专注、降低干扰**。用户反馈（2026-11-01）：今天页、工作项、收件箱几乎没有点开过，信息太多、混乱、重复，有些还是错的；真正在用的只有工作台，以及手工跟进计划和任务。

dsh-superagent 的核心原则正好对着这个问题：

> Take as little human attention as possible, and give as much value as possible.

它的面板只回答三个问题（现在有没有需要我处理的？系统健康吗？某件事的来龙去脉是什么？），每个元素都要回答其中之一，否则删掉；分类要有可检查、互斥、穷尽的定义；每个概念只有一个主页；计数必须等于它链接的列表长度；健康状态不占地方；「在等人」必须来自结构化字段，不能从自由文本推断。

## 2. 现状实测（2026-11-01，生产 server，`/v1/today`、`/v1/inbox`、`/v1/work-items`）

| 面 | 内容 | 观察 |
|---|---|---|
| 今天 | 15 张决策卡：work 11、suggestion 3、relay 1、capture 1 | 多数卡片是工作项的模型摘要；有的是空话（「本段对话没有出现与该机制相关的任何讨论……信息不足，无法据此判断推进」）；有的早已完成（「三处改动都已完成并推送」）仍作为决策卡；suggestion 都是「建议把状态改为等我」 |
| 收件箱·要处理 | 22 条：work 10、job.failed 7、suggestion 3、relay 1、capture 1 | 与今天页基本是同一批，另加 7 条失败 job；这 7 条里 4 条是监督 agent 自己派的诊断命令（`cmd /c` 探路失败），没有一条需要人处理 |
| 收件箱·仅知会 | 43 条，全部 `job.terminal` | 绝大多数是 agent 派的 exec 小命令（建 worktree、查状态、删 tag），对人零价值 |
| 工作项 | 22 个，21 个 `source=auto` | 标题是会话的第一句话（「继续」「提交 + push」「列出目录文件」「简要介绍当前是个什么项目」）；16 个 `active`，其中一半 2–7 天没动；状态靠模型推断 |

同一件事在三处出现，三处的数字互相对不上（今天 15、收件箱要处理 22、工作项「需要我」1）。

## 3. 问题归类

1. **来源不可靠**：工作项从每个终端会话自动生成，标题取首条消息，状态和「卡在哪」由模型从对话推断。这正是 dsh 明确否决的「从自由文本推断在等人」。来源错了，上面的今天页、收件箱、管家、每日综合都跟着错。
2. **一个概念多个主页**：今天、收件箱、工作项三处展示同一批 work 卡；人不知道该看哪一处，也就都不看。
3. **把机器的活动当成人的事**：agent / 监督会话自己派的 job（诊断、建 worktree、提交合并）的终态与失败进了人的收件箱。它们的结果已经回到发起会话（hook 通知），人不需要再看一遍。
4. **计数与列表不一致**：违反「count equals the length of the list it links to」，削弱信任。
5. **为解释噪声再加信息**：suggestion 卡、摘要、digest、健康原因一层层叠加；dsh 的做法是收紧或暂停，而不是加解释。

真正在用的工作台、计划、任务恰好相反：对象是人显式创建或显式派发的，状态来自结构化事实（job 状态、todo 状态、会话 relay 状态），一个概念一个页面。

## 4. 方案（建议）

### 4.1 一个入口：「等我」

只有一个列表回答「现在有没有需要我处理的」，放在顶栏徽标与今天页首屏，收件箱「要处理」栏与它同源同数。条目只来自结构化事实，按下表**第一个命中**归类（互斥、穷尽），不读任何模型摘要：

| 顺序 | 类别（标签写「等谁、要什么」） | 结构化来源 |
|---|---|---|
| 1 | 待你批准 | job `awaiting_approval`（含推送 hold）、`pending_interaction` 的审批 |
| 2 | 待你回答 | `gofer_ask_human` 问题、会话 relay 等你回复（relay 结构化状态） |
| 3 | 待你验收 | job `needs_review` |
| 4 | 计划卡住 | plan `blocked`、todo `needs_review` |
| 5 | 你提交的任务失败了 | 终态失败 / 超时、**由人本人提交**（CLI / Web / MCP 用户身份，非 job 凭据、非会话 agent）、且无后续 job |
| 6 | 投递待你确认 | 留待（held）消息、退信 |

不在表里的都不进入「等我」。计数 = 列表长度，有测试断言。

### 4.2 工作项：从自动生成改为显式

- 停止「每个终端会话自动生成一个工作项」。工作项只来自：人在 Web / CLI 显式创建、plan 绑定、或会话用 `gofer work report` 明确声明（结构化字段，不是模型推断）。
- 模型推断的状态、摘要、「卡在哪」不再驱动任何提醒；可保留为工作项详情里的折叠参考，或整体关闭（待确认 2）。
- 现有 21 个 `auto` 工作项提供一次性「归档全部自动生成项」。

### 4.3 今天页收缩

今天页 = 「等我」列表 + 进行中的计划（plan 进度条，链到计划页）+ 正在跑的会话 / job 数（链到工作台）。去掉 digest、suggestion、capture 卡；收集箱只在有未处理条目时在顶栏显示一个计数入口。

### 4.4 收件箱「仅知会」降噪

- 默认不收 agent / 会话 / job 凭据派出的 job 终态；只收人本人提交的 job 的完成（失败已在「等我」）。
- 若收件箱只剩「等我」一栏，可以考虑收件箱与今天页合并为一个页面（待确认 3）。

### 4.5 暂停扩面

在 4.1–4.4 落地并用一周之前，不再新增注意力面：

- DC3「后台 agent 管理页」先不做 Web 页，只做只读接口（或整体推迟）。
- MEM3 的「开场引用」默认关闭（每日综合本身已默认关闭）。
- 管家（steward）的主动整理与建议默认只记录（dsh 的 `observe`：记下「本来会做什么」，不打扰人），确认有价值后再开。

## 5. 衡量

- 一周内「等我」条目的误报率（点开发现不需要处理的比例）目标 < 10%。
- 「等我」为空时，今天页首屏不出现任何需要阅读的文字（健康状态不占地方）。
- 用户主观：是否愿意把今天页 / 「等我」当作每天第一眼。

## 6. 待确认项

1. **4.1 的判定表**是否覆盖你实际需要被叫的场景？是否还有漏的（例如定时任务失败、worker 掉线）？推荐：worker 掉线只在影响到你提交的任务时才出现，不单列。
2. **工作项的模型推断**：推荐保留为详情里的折叠参考、不驱动任何提醒；另一个选项是整体关闭模型推断与管家整理。
3. **收件箱是否与今天页合并**：推荐先不合并，收件箱只保留「等我」同源的一栏 + 人本人提交任务的完成记录；用一周后再看。
4. **4.5 的暂停**：推荐 DC3 只做只读接口、MEM3 开场引用默认关闭、管家主动行为改为只记录。
5. **实施顺序**：推荐先做 4.1（等我判定表 + 一致性测试）和 4.4（agent 派的 job 不进收件箱），这两项改动小、效果最直接；再做 4.2 / 4.3。

## 7. 已定事项（2026-11-01）

- §6 的 1–5 全部按推荐：worker 掉线不单列；工作项的模型推断保留为详情里的折叠参考、不驱动提醒；收件箱暂不与今天页合并；DC3 只做只读接口、MEM3 开场引用默认关闭、管家主动行为改为只记录；先做 4.1（「等我」判定表 + 一致性测试）与 4.4（agent / 会话 / job 凭据派出的 job 不进收件箱），再做 4.2 / 4.3。

## 实施记录

- **AT1「等我」判定表**：判定与列表在 `internal/inbox/waiting.go`（`Classify` 纯函数 + `Waiting` 折叠 + `TerminalCards`），留在 inbox 包而不新建包：它只在已有的 today 卡与 jobstore 之上加一层归类，不增加反向依赖（G024：域不独立）。来源事实：job 状态 / 待处理交互类型 / 会话 relay 的 open turn / plan blocked / held 信件，卡上新增结构化 `subtype`。`GET /v1/today` 新增 `waiting`，收件箱「要处理」栏由同一函数（httpapi `waitingFor`）生成，`total_unread` 即其未读数；web 顶栏徽标取 `total_unread`（收件箱不可用时回落为列表长度）。被移出的 work / suggestion / merge / memory / capture 卡不再在首屏出现，`decisions` 旧字段保留给 AT3 取舍。「todo needs_review」在数据里就是其 job 的 `needs_review`，与第 3 类同源，同一 job 取第 3 类。退信不单列（只判 held）。
- **AT2 收件箱降噪**：「人本人提交」的判据在 `job.HumanSubmitted`：channel 为 cli / web / mcp，且无 `submitted_by_job:` 标签（job 凭据）、无 `submitted_by_session:` 标签（带 `X-Gofer-Session-Id` 的用户请求，由服务端在提交时新盖章）、无 `source_session_id`、请求里无 `notify_session`、非自动续接 / failover 接管；空 channel 视为非人。已知局限：agent 在没有会话环境变量的环境里用用户 token 提交，与人无法区分。

- **AT3 工作项改显式 + 今天页收缩**：
  - **自动生成的入口与改法**：唯一的创建点是 `work.Service.OnHumanPrompt` → `jobstore.EnsureDraftWorkItemForSession`（relay 的 `UserPromptSubmit` / 心跳经 `WorkHook` 调用）。后者连同 `draftExclusion`、`pathHasPrefix`、排除名单的读取一起删除；`OnHumanPrompt` 只保留人工提问计数（短命会话判据在读 `HumanPrompts`）。`work.draft.min_prompts / exclude_*` 四个设置没有消费者了，按 G032 保留键并标 `DEPRECATED(v0.208): remove in v0.211`（说明文字注明「已不起作用」，免得旧设置导入报错）。
  - **`work report` 的新语义**：`<id>` 可省略；`POST /v1/work-items/report`（`work.Service.ReportBySession`，入口层只绑定与转发）按会话找它已有的未结束工作项汇报，没有就由首次汇报创建：标题取显式 `--title`，缺省取 `--goal` 第一行，都没有 400 且不建；来源 `report`（新常量 `WorkOriginReport`），不带 `unsorted`。显式 id 的 `POST /{id}/report`（job 凭据可用）与 MCP `gofer_work_report` 不变。
  - **模型推断**：整理器仍写目标 / 卡点 / 下一步 / 摘要与建议，但不再驱动提醒——「等我」早已不读它（AT1）；`today` 不再建 `suggestion` 卡（删 `builder.suggestions`、`Card.Suggestions`、`KindSuggestion` 与相关目录文案）；`notifyNeedsMe` 的正文跳过整理器写的字段（`isSummarizerOwned`）；每日摘要不再附管家点评（删 `Digest.Commentary` 与 `StewardReviewComment` 的使用，点评只留在巡检记录里）。工作项详情里建议折进「模型参考」（默认收起），带整理器来源的字段上方加一句参考说明。
  - **管家只记录**：新设置 `steward.act_proactively`（默认 false）。`steward.Service` 记录进行中的巡检的 trigger / id，`Observing()` = 巡检在途 ∧ trigger 为 daily / event ∧ 开关关。HTTP 层的 job 凭据中间件在管家凭据通过白名单后，对「有后果的写」（`stewardObservedWrites`：PATCH 工作项、日志、请汇报、整理、合并建议、带话、今天建议、记忆建议、发信）调 `Observe` 记成一行 `steward_observations`（新表，保留最近 500 条）并回 202；管家自己的笔记 / 点评与对自己信件的回复不拦。`gofer steward observations` 与 `GET /v1/steward/observations` 查看。手动巡检、提问与人的写不经这条路。
  - **归档接口**：`jobstore.ListAutoArchivableWorkItems`（`source=auto`、未结束、非收集箱、未合并、`status_source<>human`、日志与字段来源无 `human` 发言）+ `work.Service.ArchiveAuto`（逐项带 rev 做 `dropped`，写日志与 `work.archived_auto` 审计，`dry_run` 只列出，幂等）；`POST /v1/work-items/archive-auto`，路由声明 `Public` + `Filter`、仅人凭据、受项目范围限制；CLI `gofer work archive-auto [--yes|--dry-run]`；Web 工作项页「归档自动生成的（N）」先 dry-run 再确认。
  - **今天页新形状**：`/v1/today` 新增 `running {sessions, jobs}`（`today.Service.running`）；`work-items` 的 `summary` 新增 `captured`（顶栏「收集箱 N」入口的数据源）。web 今天页 = 等我队列 + `TodayPlans`（读 `GET /v1/plans?status=open`，进度走 `planProgress` 唯一口径）+ 一行正在跑；删 digest / 一行摘要 / 筛选 / 并行中泳道 / 刚处理过，状态条只在有告警时出现，等我为空时只有一句空态。
  - **`decisions` 的去留**：保留，不标 DEPRECATED。web 不再读（`waitingCards` 去掉对它的回落），但 MCP `gofer_today_list|card|advise` 与管家对「今天」卡的建议、收件箱的 `waitingFor` 仍以它为输入；等这两处改读 `waiting` 后再按 G032 标记并删除。`internal/today` 的 `TodayLanes` 组件与 `/v1/today/lanes` 暂留（页面已不挂载），留待后续清理。
- **CL1 / CL2 遗留清理**：`/v1/today` 的 `decisions` 字段已删除（按 G032 不留兼容层）。`internal/today` 仍用同一条结构化卡管线，但只剩 interaction / decision / permission / relay / review / approval / plan_blocked / mail_held 八种由存储行直接产生的卡，`Response.Cards`（`json:"-"`）只给 HTTP 层折进 `inbox.Waiting`；工作项、合并建议、记忆整理、收集箱与 worker 申请不再建卡。MCP `gofer_today_list|card|advise` 与管家的今天建议改读 `waiting`（`job.failed` 卡只读），inbox 的 `waitingFor` 以同一份队列加 `inbox.TerminalCards` 组装。`TodayLanes` / `TodayRecentHandled` 组件、`/v1/today/lanes` 路由与 `today.LanesBuilder` 一并删除。遗留：记忆整理建议（`gofer_memory_suggest` 与管家记忆体检步骤）已无任何人看得到的界面，只能走 `/v1/memory-suggestions*` 接口，待决定删除还是补页面。
