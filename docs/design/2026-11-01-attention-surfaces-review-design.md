# 注意力面复盘：今天 / 工作项 / 收件箱为什么没人用，以及怎么收

> issue：gofer-cnxy
> 状态：0.3 —— F1 / F2 / F4 与 digest 改名已批准（2026-11-09，用户：「等我按建议」）；F3 的具体约定（末行 `[needs-reply]` 标记）是本稿新提出的，待用户确认
> 相关：`docs/design/2026-10-09-dashboard-redesign-design.md`、`2026-10-23-ui-redesign-design.md`、`2026-10-28-inbox-triage-design.md`；外部参照 dsh-superagent-prototype 的 `docs/000-philosophy.md`、`docs/005-panel.md`（本地副本在 WP `local/dsh-superagent-prototype/`），以及 xhen 的调研 `inhere-tools/xhen/docs/research/2026-10-31-dsh-superagent-prototype.md`。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-01 | Claude | 初稿（补记） |
| 0.2 | 2026-11-01 | Claude | §6 全部按推荐定案（§7）；§8 记忆整理三层（补记） |
| 0.3 | 2026-11-09 | Claude | RV1 复盘误报约 25%，新增 §9：监督中的会话不进「等我」（F1）、relay 回合满 5 分钟才进（F2）、只有带「需要回复」标记的回合归第 2 类（F3）、记录回合关闭时间（F4）；digest 的「等我 N」改名 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

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
- RV1（2026-11-09）未达标，修正与再次衡量见 §9。

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

## 8. 记忆整理（2026-11-01 已定）

管家的「模型记忆建议 → 人采纳」队列（`gofer_memory_suggest`、`/v1/memory-suggestions*`、巡检的「仓库记忆整理」步骤）在 AT1 之后已无人可见，改为按证据强弱三层处理，不新增注意力面：

1. **确凿的自动归档**（不问、不推送、可 `restore`、写审计并在 `memory doctor` 列出）：过期超过 7 天的 handoff；引用的路径与提交全部不存在的笔记（note）。**rule 永不自动归档。**
2. **需判断的攒着等人拉**：被 agent flag 的 rule、重复、长期未用——不进「等我」、不推送、不在每日摘要加计数；集中在现有记忆页 `/memory` 的「待整理」区与 `gofer memory doctor`，每条三选一：保留（unflag）/ 改写（set）/ 归档。
3. **使用中顺手修**：注入时被 flag 的记忆已带「⚠ 需复核」；skill 写明 agent 确认过时即 `memory set` 改写或 `memory archive`，不必等人。

删除管家的模型记忆建议队列及其 MCP 工具、HTTP 路由与巡检步骤。

- **MH1 记忆整理三层**：
  - **自动归档**：`internal/tracker/memory_autoarchive.go`（`Store.AutoArchiveMemories`，判定 `autoArchiveReason` 复用 doctor 的 `memoryPathRefs` / `memoryCommitRefs` / `pathExistsUnder` / `gitCommitsMissing`）。规则：handoff 过期超过 7 天；note 至少引用一个路径，且全部路径与提交都不存在（只引用提交的不动：浅克隆会把老提交也报成缺失）；rule 永不；7 天内改过的不动（刚 `restore` 的因此有一周宽限，不会被立刻再归档）；`doctor_ignore` / `prime.doctor.suppress` 静默了对应检查的不动；没有仓库检出的存储（全局记忆）不判 note。决定在快照上做，归档时在锁内复核记忆仍在且 `updated_at` 未变。归档条目 `archived_by=auto-archive` + 原因；`Store.Doctor` 的 `auto_archived` 列出近 30 天仍在归档文件里的条目。
  - **触发时机**：`gofer repo sync` 推送之前（`commands/repo.go` 只转发一次调用）。理由：它在仓库检出里跑，路径 / 提交检查才有意义；归档产生的墓碑在同一次同步里到达 server 与其它 clone；不放在写后自动同步（`tryAutoSync`）里，否则 `restore` 一个过期 handoff 会被立刻再归档；不放在 prime（读热路径，不改数据、不跑 git）。开关 `prime.doctor.auto_archive`，默认开（设计 §8 已批准，且可 `restore`）。
  - **待整理**：web 记忆页 `?view=tidy`。列表由已取回的记忆 + doctor 现算（`utils/memories.ts` 的 `tidyItems`；server 项目 / 全局记忆没有 doctor，按自带 `flags` 补 `flagged`），不新增读接口。保留 = 清除标记（仓库记忆另把其它发现写进 `doctor_ignore`）；改写 = 打开详情抽屉；归档只有仓库记忆。新增写接口 `POST /v1/tracker/memories/{id}/archive`、`DELETE /v1/tracker/memories/{id}/flag`（`memory.write`，人凭据；已登记 `routeDecls`，permissions.md 已重生成）。
  - **删除**：`gofer_memory_suggest`、`/v1/memory-suggestions*`、巡检「仓库记忆整理」步骤与 `steward_memory` 模板、`today.MemoryHygiene` / 建议存取 / 采纳逻辑、web 的建议卡文案；`memory_suggestions` 表按 G057 保留不删。

## 9. RV1 复盘与修正（2026-11-09 已定）

用户 2026-11-09：「等我按建议」——F1–F4 全做，digest 的「等我 N」一并处理。

### 9.1 复盘结论

一周（11-02 ～ 11-09，生产 v0.212.1）进入过「等我」的 222 条，主估计误报 55 条，**误报率约 25%**（下限 5.9%，上限 43.7%），目标 < 10% 未达到。误报**全部**来自第 2 类里的会话 relay 回合（174 条中误报 55、可疑 42）；第 1 类 job hold 42 条、终端权限弹窗 6 条零误报，第 3～6 类本周 0 条。根因三条：会话在等子 agent / 被盯 job 时回合仍算「待你回答」；汇报型回合与提问型不分；没有宽限期。报告与原始数据在工作区 `tmp/rv1/report.md`（不入库，含业务名）。

### 9.2 F1 监督中的会话不进「等我」

- **判据（核实后）**：`sessionrelay.supervisingDetail` 现有两条事实——会话内在跑的子 agent（`SubagentCount`，内存计数，按 SubagentStart/Stop 维护）与会话调用方的活跃 job（`CountActiveJobsByCaller(caller_id, now - supervising_window)`）；它不看 job watch，且受 `session.auto_relay_skip_when_supervising` 开关控制。F1 用三条事实：上述两条 + 会话的 job watch（`session_job_watches` 有行：被盯 job 结束后 hook 会以 `watched_job` 收回回合，人无需回答）。任一成立即「监督中」。
- **取法**：sessionrelay 新增 `Supervision(a AgentSession) Supervision{Jobs, Subagents, Watches int}`，只读事实、**不受开关与 relay 模式影响**（开关管 Stop 停不停，不管列表）。`supervisingDetail` 改为复用它的前两项，Stop 行为零变化（不把 watch 加进 Stop 门槛）。today 在 `Deps` 加接口 `Supervisor`（与 `StewardStatus` 同模式），由 httpapi 装配时注入 `s.relay`；workbench 按 `RelayMessenger` 的做法对已有 `Relay` 做接口断言取得。today / workbench 现已 import sessionrelay 取纯函数与常量（方向不变，sessionrelay 不 import 二者），本次不新增对 `*sessionrelay.Service` 的直接依赖（G022/G024）。接口为 nil 或查询失败按「不在监督」处理（同 `supervisingDetail` 的失败口径，记 Warn）。
- **现算**：每次构建「等我」（`today.decisions()` → `relay()`）与工作台投影时对有开放回合的会话现算，不在 Stop 时落库。回合打开后才开始的监督（之后派的 job、起的子 agent）在下一次构建即生效；监督结束后同理立即回到 F2/F3 的判断。
- **显示**：「等我」不出该会话的 relay 卡；工作台线程显示「监督中」（带 job / 子 agent / 被盯 job 数），不进工作台的 attention。

### 9.3 F2 宽限期

- relay 回合打开满 5 分钟才进「等我」。**用常量**（today 包 `relayGraceSec = 300`），不加配置项：没有调参需求，RV2 再看。
- 起点是回合的 `asked_at`（`OpenTurn` 会先关掉同会话的旧回合，一个会话同时只有一个开放回合）。
- 过滤发生在卡片生成处，「等我」列表、`total_unread`、顶栏徽标同源同数（§4.1）。宽限到点不产生存储变更事件，`/v1/today` 与 `/v1/inbox` 返回 `recheck_at`（最早一个被宽限挡住的回合的到点时刻，0 = 无），web 到点重拉一次。

### 9.4 F3 只有「需要回复」的回合归第 2 类

- **核实**：Claude Code 的 Stop hook 输入里只有 `session_id`、`transcript_path`、`cwd`、`stop_hook_active`、`last_assistant_message` 等（`hookrelay/payload.go`），没有「在问人」的字段。AskUserQuestion 是阻塞型工具调用，回合结束（Stop）时不可能悬而未答：中继在等时它已经走 PreToolUse 变成「问题」权限卡（第 2 类，现状不变），人在终端时直接在终端答。`gofer_ask_human` 产生 `KindDecision`，已是第 2 类、不变。所以 relay 回合缺的是「agent 明确要人回复」这一事实。
- **判据（决策）**：agent 在最后一条消息的**末行单独写协议标记 `[needs-reply]`**（去首尾空白后整行逐字匹配，ASCII，不随语言变）。Stop hook 从 `last_assistant_message`（缺省时用 transcript 尾部，与现有 `lastMessage()` 同源）判断，剥掉该行后把正文与布尔字段 `needs_reply` 一起交给 `POST /v1/sessions/{sid}/turns`。agent 怎么知道：SessionStart 注入一行提示（与 `TopicReminder` 同通道，仅 `CatchUpAgent` 方言），gofer-usage skill 的会话中继说明同步。
- **为什么不是问句启发式**：§4.1 原则是「不读任何模型摘要」，最后一条消息是模型原文而非摘要，但按「是否以问句结尾」归类仍是从自由文本推断在等人（dsh 明确否决）；RV1 用同样启发式只能给出 5.9%～43.7% 的宽区间，且汇报结尾的客套问句（「需要我继续吗？」）正是误报来源。标记是 agent 的**显式声明**（同 `gofer work report`），整行精确匹配，不做语义判断。也不用新 CLI / MCP 调用：要多一次工具调用，还得把标记挂到「下一个」回合上，有竞态；标记随它描述的回合一起到达，天然原子。
- **存储**：`plan_decisions` 加 `needs_reply INTEGER NOT NULL DEFAULT 0`，经 `migratePlanDecisions` 的 ALTER 添加，基础 schema 里不建引用它的索引（G057）；下次发版后补 `testdata/schema-v<版本>.sql` 快照。
- **无标记的回合**：不进「等我」；工作台线程显示「空闲」（监督中则显示「监督中」），回复框照常可用，Stop 照常停住等 web 回复——中继行为不变，只改「是否叫人」。会话自身的 `state`（`waiting_reply`）保持原义不改。
- **旧 hook（G032）**：请求不带 `needs_reply` 读作 false，不加兼容分支。hook 与 server 是同一个 gofer 二进制，随发版一起升级；未升级的 worker 机器上，其会话的提问在升级前不进「等我」（仍在工作台可见），发版说明写明。
- **推送不在本次**：`OpenTurn` 的「会话等待回复」推送仍按现状发，RV2 再看是否改成同口径。

### 9.5 F4 记录回合关闭时间

`plan_decisions` 加 `released_at INTEGER`（ALTER，旧行 NULL 读作 0）：`ReleaseDecision` 与 `ExpireSessionDecisions` 写当前时间，`expireDueLocked` 写 `asked_at + timeout_sec`（真实到期时刻，而不是扫描时刻）；作答仍记 `answered_at`。决策读接口多返回 `released_at`（additive）。

### 9.6 digest 的「等我 N」

核实：`work.Service` 摘要用 `work.digest.counts`，`{needs_me}` 是工作项 `needs_me` 状态数，与「等我」列表无关。最小改动是**改名**，只改 `work.ini` 两种语言的这一条文案（zh「工作项·需要我 {needs_me}」，en「work items needing me {needs_me}」），不改代码与字段；去掉要动摘要构建与测试，改动更大。

### 9.7 判定顺序与衡量

- 会话 relay 回合进「等我」第 2 类须同时满足：未读（`acked_at = 0`）∧ `needs_reply` ∧ 不在监督中 ∧ 打开满 5 分钟。§4.1 表格第 2 类的「relay 结构化状态」即指这四条。
- **RV2**：F1–F4 随下一批发版上线后满 7 天复盘一次，方法同 RV1，`released_at` 补上关闭时间盲区。除误报率（目标 < 10%）外加一项**漏报观察**：无标记回合中人在打开后 30 分钟内于 web 作答的条数（说明 agent 该标没标），用于决定是否加强 SessionStart 提示。
