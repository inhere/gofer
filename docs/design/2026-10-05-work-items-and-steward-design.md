<!-- template_id: design; template_version: 1.1.1 -->
# 工作项（Work Item）+ 全局总览 + 管家会话（W 批）

> 状态：Approved（一期 v0.109.0 已上线；§14 二期用户 2026-10-06 确认按建议实施，先 W2a 后 W2b）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-05 | Claude | 初稿：工作项模型、会话归属、被动整理 + 按需自汇报、搁置提醒、总览页、可换 agent 的管家 |
| 0.2 | 2026-10-06 | Claude | 一期已上线（v0.109.0）；新增 §14 二期详细设计（借鉴 Octop AgentTeams：调度不干活、异步请求账本、发言者标注、记忆压缩），待确认 |

## 1. 问题

- 同时开着多个终端会话（Claude / codex / omp / jcode，分布在主机与容器 worker），不少停在"半完成"：缺设备 / 资源、要去现场、等别人。
- gofer 目前能看到会话**在不在跑**（state、last_message、心跳、离线），看不到**做到哪、卡在哪、下一步是什么**。会话标题只是"最近一次提问"，同一工作区里多个会话难以区分。
- 这些信息只存在于各会话上下文里，会话一停就没人记得，换个 agent 接手也无从继续。

## 2. 参考

| 来源 | 借鉴点 |
|---|---|
| GTD 的 Waiting-for / Tickler | "等待"是正式状态，要写明在等什么；到期自动浮出 |
| Taskwarrior `wait:` / `until:` | 搁置到某个时间点前隐藏，到点重新出现 |
| Linear / 看板 | 按状态分栏、按项目筛选、一张卡片一件事 |
| LangGraph Agent Inbox、Orca "未读" | "需要我处理"集中在一个收件箱，不靠逐个翻会话 |
| Claude Code `/resume` 会话摘要 | 用模型从 transcript 生成一句话摘要，供人快速认出会话 |
| Paseo handoff、gofer plan handoff | 关键信息写成可版本化的交接文档，换人 / 换 agent 都能接着做 |

## 3. 现有可复用能力（2026-10-05 调研）

- **会话登记**：`agent_sessions` 表（state、relay、last_message、progress_text、turn、peer、handoff、offline）、hook 心跳、`/v1/sessions`、`sessions` 推送主题、Sessions 页、唤醒 / 接管 / 传话。
- **会话关联 job**：`session_job_watches`，hook 自动从工具输出里登记 job id。
- **持久化**：
  - `plan_handoffs`：版本化 Markdown + 乐观锁；
  - `scoped_memories`：global / project 作用域；
  - tracker issue / memory（`.gofer/tracker`，即 bd 的替代）；
  - `plan_decisions`（ask human）。
- **运行与唤醒**：持续 ACP 会话 job（say / end）、`acp.mcp_servers` 配置、wakeup（at / every / cron / event）、schedules（定时提交 job）。
- **通知与推送**：钉钉 webhook 事件框架、`/v1/ws` 主题。
- **缺口**：
  - 没有"工作"层的数据模型；
  - 会话没有主动写目标 / 阻塞 / 下一步的通道；
  - 没有摘要生成；
  - 没有会话类 MCP 工具；
  - 管家会话不能自动挂 gofer MCP；
  - 没有日报类通知。

## 4. 核心概念：工作项 ≠ 会话

**一件事可能跨多个会话**：会话结束后被唤醒、换 agent 接手、在另一台机器继续。**一个工作区下也可能同时进行多件事**。所以引入独立于会话的「工作项」：

```
工作项 (work_item) 1 ── n 会话 (agent_sessions)   当前会话 + 历史会话
        │
        ├── 日志 (work_journal)：追加式，汇报 / 人工备注 / 状态变化 / 管家整理
        └── 关联：tracker issue、plan / todo、job（经会话的 job watches 自动带出）
```

- 新会话第一次有人工提问时，**自动生成一张草稿工作项**（标题暂取提问首行，目标待补）；草稿不打扰人，只在总览的"未整理"区显示。
- 同一件事的多个会话可以**合并到一个工作项**，一个会话做了两件事可以**拆分**。管家会给出合并建议（同工作区、时间相近、内容相近），由人确认。
- 工作项就是"关键信息记录"本身：管家、任何会话、人都读写同一份数据，**换 agent、换会话都从工作项 + 日志继续**。

### 4.1 数据模型（新增表，additive）

- **work_items**：`id`、`title`、`goal`、`status`、`blocker_kind`、`blocker_text`、`next_step`、`summary`、`project_key`、`workspace`（cwd 根）、`priority`、`park_until`（时间，可空）、`park_note`（"到货后继续"之类的条件说明）、`remind_at`、`source`（auto / human / steward）、`rev`（乐观锁）、`created_at`、`updated_at`、`updated_by`、`closed_at`。
- **work_item_sessions**：`work_item_id`、`session_id`、`role`（current / past）、`attached_at`、`detached_at`。
- **work_journal**：`id`、`work_item_id`、`kind`（report / note / status / steward / link）、`text`、`by`（human:<caller> / session:<sid> / steward）、`at`。只追加，不改。
- **work_links**：`work_item_id`、`kind`（issue / plan / todo / job）、`ref`。

## 5. 状态（建议）

| 状态 | 含义 | 自动来源 |
|---|---|---|
| 进行中 active | 会话正在干活 | 当前会话 running |
| 等我 needs_me | 需要我回复 / 决策 | 当前会话 waiting_reply，或有待应答交互 / decision |
| 等资源 waiting_resource | 缺设备、账号、数据、别人的东西 | 人或管家标记 |
| 需现场 needs_onsite | 要到现场操作 | 人或管家标记 |
| 待验收 review | 做完了，等我检查 | 人或管家标记；关联 job needs_review |
| 已搁置 parked | 暂时不做，到点或条件满足再说 | 人标记，带 `park_until` / `park_note` |
| 已完成 done / 已放弃 dropped | 结束 | 人确认（管家只建议，不自动关闭） |

- 自动来源只在人**没有手动设置**时生效：人标的"等资源"不会被会话的 running 覆盖，除非人改回或会话自汇报明确说阻塞已解除。
- 会话 offline / ended 而工作项未完成时，在卡片上标"会话已离线"，提示唤醒，但不改工作项状态。

## 6. 信息怎么来：被动整理为主，自汇报按需（建议）

**不在每次停下时都让会话汇报**。原因：
- 汇报要往会话里注入一条消息，终端里会多一轮，打扰正在用的人；
- 每次都汇报耗额度，信息也大多重复。

| 时机 | 方式 | 说明 |
|---|---|---|
| 随时 | 被动数据 | hook 已有的 last_message、progress、turn、关联 job，实时显示在卡片上，零成本 |
| 会话空闲 ≥ 15 分钟、offline、ended，且自上次整理后有新活动 | **管家被动整理** | 读 transcript 尾部（只读，不打扰会话），用便宜模型提炼目标 / 进度 / 阻塞 / 下一步，写回工作项（标 source=steward，人可改） |
| 人点「搁置」或「去现场」 | **请会话写交接** | 经传话 / 中继请会话自己写一段交接（做到哪、卡在哪、回来第一步），比外部提炼准。会话不在运行时退回管家整理 |
| 人点「请它汇报」 | 按需自汇报 | 同上 |
| 每日摘要前 | 管家整理 | 只处理当天有变化的工作项 |

自汇报的通道：
- 汇报请求里带上工作项 id 与会话 id，会话通过 CLI `gofer work report <id> --goal ... --status ... --blocker ... --next ...` 写回（任何有 shell 的 agent 都能用）；
- 有 gofer MCP 的会话也可以用 `gofer_work_report` 工具；
- SessionStart prime 里加一句简短说明，告诉会话有这个命令。

## 7. 全局总览「工作」页

- 新页面（导航"工作"，手机优先）：
  - 顶部：「等我」与「到期提醒」两个计数徽标，点开即筛选；
  - 主体：按状态分栏（进行中 / 等我 / 等资源 / 需现场 / 待验收 / 已搁置），可切换为按工作区分组（解决一个工作区多个会话）；
  - "未整理"区：自动生成、还没补目标的草稿工作项。
- 卡片：标题、目标一句话、阻塞原因、下一步、最后活动时间、当前会话（agent / runner / 在线状态）、关联 issue / todo / job 小标签。
- 卡片操作：
  - 会话相关：打开会话、唤醒 / 接管、传话；
  - 汇报相关：请它汇报、写备注；
  - 状态相关：标状态、搁置（选时间或写条件）、设提醒、完成 / 放弃；
  - 整理相关：合并 / 拆分、关联 issue / 转成 todo。
- 卡片详情：工作项字段 + 日志时间线 + 所有会话（当前 / 历史）+ 关联项。
- 推送主题 `work`，沿用 `/v1/ws` 与兜底轮询。

## 8. 提醒与每日摘要

- `remind_at` / `park_until` 到点：server 定时扫描，发钉钉 `work.remind`，并把卡片顶到"到期提醒"。
- 每日摘要 `work.digest`（默认 09:00，可配；也可关闭）：等我 N、等资源 N、需现场 N、搁置超过 7 天 N、昨日有进展的工作项列表。
  - 摘要正文由数据库**确定性生成**，不依赖模型；
  - 管家开启时可附一段点评（例如"这三件都在等同一批设备"）。

## 9. 管家（Steward）

- **它是谁**：一个常驻的持续 ACP 会话 job，跑在 server 本机的默认工作空间。
- **可换**：agent 由配置 `steward.agent` 指定，可选任何 acp-agent（claude-acp、codex-acp、omp-acp、gemini-acp……），**用户可随时在设置页切换**。
- **无状态设计**：
  - 管家自己不保存关键信息，一切都在工作项、日志和「管家笔记」里。管家笔记是一份版本化 Markdown，复用 plan_handoff 的存储与乐观锁，记录长期偏好和约定，例如"zy-bsly 现场一般周三去"。
  - 每次启动或切换 agent，用 prime 注入：管家笔记 + 未结工作项的简表 + 最近 24 小时的日志。因此换 agent、重启都能接着做。
- **能力**（gofer 自动给管家会话注入 gofer MCP，带收窄的白名单，类似 leader 模式）：
  - 读：列出 / 查看工作项、会话（含 last_message、progress、transcript 尾部）、关联 job、issue；
  - 写：更新工作项字段、写日志、合并 / 拆分建议、设提醒；
  - 转达：通过传话请某个会话汇报，或带话给它（"资源到了，可以继续"）；
  - 不能：提交执行类 job、改配置、删数据。
- **触发**：
  - 你在网页或手机找它说话：工作页右下角「问管家」面板，就是这个持续会话；
  - 定时：每日摘要前的整理，用 wakeup / schedule 实现；
  - 事件：会话进入 offline / ended、工作项到期、出现新的草稿工作项（批量，节流 30 分钟）。
- **成本控制**：
  - 默认关闭，配置后启用；
  - 被动整理用 `steward.summary_agent` 指定的便宜模型，可以和管家用不同 agent，也可以用一次性 job；
  - 只处理有变化的工作项，每次运行设上限。

## 10. 接口一览

- REST：
  - 工作项：`/v1/work-items`（CRUD + 筛选）、`/{id}/journal`、`/{id}/sessions`（attach / detach）、`/{id}/report-request`、`/{id}/merge`；
  - 管家：`/v1/steward`（状态、启停、切换 agent）、`/v1/steward/notes`。
- CLI：`gofer work ls|show|new|set|note|park|remind|report|link|merge`。`work` 是新的核心资源名词，按 G033 作为顶级命令组，实施时把它补进 AGENTS.md G033 的核心名词清单。
- MCP：
  - 工作项：`gofer_work_list/get/update/note/report`；
  - 会话（只读）：`gofer_session_list/get`；
  - 转达：`gofer_session_ask`；
  - 管家笔记：`gofer_steward_notes`。
- 推送：`work` 主题；钉钉事件：`work.remind`、`work.digest`、`work.needs_me`（可选）。

## 11. 分期

| 期 | 内容 | 价值 |
|---|---|---|
| 一期 | 数据模型；会话自动建草稿工作项；状态自动映射 + 手工编辑；搁置 / 提醒；日志；工作页（含按工作区分组、合并 / 拆分）；CLI / MCP / REST；`work.remind` 与确定性 `work.digest` | 不依赖模型就能把"遗漏、忘记进度"解决大半 |
| 二期 | 管家被动整理（transcript 尾部提炼）；「请它汇报 / 写交接」；会话 prime 说明；管家配置与 gofer MCP 自动注入；「问管家」面板 | 卡片内容自动补全，信息更准 |
| 三期 | 管家定时巡检与点评；合并 / 关联建议；工作项与 issue / todo 双向联动；周报 | 管理层的智能化 |

## 12. 待确认（附建议）

1. **状态集**：建议采用第 5 节的 8 个状态（含已放弃）。
2. **自汇报时机**：建议第 6 节的"被动整理为主，搁置 / 去现场时请会话写交接，其余按需"，不在每次停下时汇报。
3. **管家 agent**：设置项可选，默认 claude-acp；被动整理单独可配一个便宜模型。关键信息全部落库，换 agent 不丢上下文。
4. **工作项粒度**：建议默认一个会话一张草稿卡，人或管家确认后合并；不强制先建工作项再开会话。
5. **每日摘要**：建议默认 09:00 推钉钉，可改时间或关闭。

## 13. 测试与验收（一期）

- 固定测试：
  - 新会话自动建草稿工作项；
  - 状态自动映射，以及人工状态优先；
  - 搁置 / 提醒扫描发 `work.remind`；
  - 摘要内容确定性；
  - 合并 / 拆分后会话归属与日志正确；
  - 乐观锁冲突；
  - MCP / CLI 读写；
  - `work` 推送。
- 真实验收：在临时 serve 上用两个假会话（同一工作区）+ 一个离线会话，在工作页上完成"标需现场 → 搁置到明天 → 到点提醒 → 唤醒会话继续 → 完成"的全流程。手机宽度截图。

## 14. 二期详细设计（v0.2，2026-10-06）

> 一期已于 v0.109.0 上线。本节细化二期：被动整理、请会话汇报、管家。补充参考：TencentCloud/Octop（自托管多用户多 agent 助手平台，其 AgentTeams 的"主持人只调度、异步派活、job 账本、发言者标注、记忆压缩"思路）、agent-deck / agent-session-manager（跨机器的会话管理控制台）。

### 14.1 设计原则（借鉴 Octop AgentTeams）

1. **管家只调度和整理，不干活、不拍板**：它读、整理、提醒、请会话汇报、给你建议，**不**替你决定状态终局（完成/放弃）、不提交执行类 job、不改配置。
2. **派活异步 + 回调**：管家发出的每个请求（请会话汇报、请整理某张卡）都异步执行、完成后回调写回，管家不阻塞等待。
3. **请求账本，重启不丢**：所有在途请求记在 `work_requests` 表（见 14.3），server 重启、管家换 agent 后都能看到"哪些还在等、等了多久"，超时自动标记失败并在卡片上提示。
4. **改写而非转发**：管家给会话的汇报请求由模板生成，带工作项 id、当前已知目标/阻塞、要求的格式和 `gofer work report` 用法；不把你的原话直接甩给会话（你的原话进日志）。
5. **发言者标注**：工作项字段与日志的每条变更都带来源 `by`（human:<caller> / session:<sid>(<agent>) / steward(<agent>) / summarizer(<agent>)），卡片上显示"这条信息谁写的、何时"，便于判断可信度。
6. **关键信息全部落库**：管家自己的上下文可随时丢弃；换 agent 后靠管家笔记 + 工作项 + 日志 + 请求账本继续。

### 14.2 被动整理（Summarizer）

- 触发（任一满足，且该会话自上次整理后有新活动）：会话 idle ≥ `work.summarize_idle_min`（默认 15 分钟）、进入 offline / ended、每日摘要前、你在卡片点「整理」。
- 输入：会话 transcript 尾部（最多 N 轮 / 32KB，按 agent 解析 claude / codex / omp 的会话文件；读不到时退回 last_message + progress + 日志）、该工作项现有字段。transcript 在会话所在 runner 上：server 本机直接读，worker 上的经 worker 读取接口取尾部（只读，大小受限）。
- 执行：一次性 job（`work.summarizer_agent`，默认可设成便宜模型的 cli-agent，如 `claude` + 小模型参数），只读、无工具；输出固定 JSON（goal / progress / blocker_kind / blocker / next / status_hint / confidence）。
- 写回规则：只填空字段或覆盖 `source=summarizer` 的旧值；**不覆盖人或会话自己写的字段**（显示为"整理建议"供你一键采纳）；状态只给 `status_hint`，不直接改状态（人工优先原则不变）。
- 成本控制：每会话最小间隔 30 分钟、每日上限（`work.summarize_daily_limit`，默认 50 次）、只整理有变化的。

### 14.3 请会话汇报（Report Request）

- 一期已有按钮（只对在运行的会话）。二期改为经请求账本：`work_requests`（id、work_item_id、session_id、kind=report|handoff、state=pending|sent|answered|failed|expired、sent_at、answered_at、deadline、by、error）。
- 会话在运行：经现有通道送达（等回复中走中继注入；否则走传话）；会话用 `gofer work report --request <id>` 回填，账本置 answered。
- 会话不在运行：自动转为被动整理（14.2），账本标 `kind=summarize`。
- 「搁置」「需现场」时默认发 `handoff` 请求：请会话写交接（做到哪、卡在哪、回来第一步），写入工作项 summary/next 并进日志。
- 超时（默认 30 分钟）未回：标 expired，卡片提示"会话未回应，已改为整理"并触发被动整理。

### 14.4 管家（Steward）

- 配置：`steward.enabled`（默认 false）、`steward.agent`（任意 acp-agent，设置页可选、可随时切换）、`steward.project`（默认 server 的默认工作空间项目）、`steward.review_time`（每日巡检，默认摘要前 10 分钟）。
- 运行形态：常驻持续 ACP 会话 job（标签 `steward`，在 Sessions 与工作页可见）；空闲超时自动结束，下次需要时按 prime 重建——**会话本身是可抛弃的**。
- 自动注入 gofer MCP：server 为管家 job 自动配置 gofer 的 stdio MCP（不需要用户手写 `acp.mcp_servers`），工具白名单：
  - 读：`gofer_work_list/get`、`gofer_session_list/get`、`gofer_session_tail`（只读 transcript 尾部，大小受限）、`gofer_list_jobs/get_job`（只读）、`gofer_issue_list/get`（只读）。
  - 写：`gofer_work_update`（不含终态）、`gofer_work_note`、`gofer_work_remind`、`gofer_work_merge_suggest`（只产生建议，需人确认）、`gofer_work_request_report`（走账本）、`gofer_steward_notes`（读写管家笔记）。
  - 无：提交执行 job、改配置、删除。
- Prime（每次启动/切换 agent 注入）：管家笔记 + 未结工作项简表（每项一行：状态/标题/阻塞/下一步/最后活动/在途请求）+ 最近 24 小时日志摘要 + 在途请求账本。超长时按优先级截断（等我 > 到期 > 需现场/等资源 > 其他）。
- 管家笔记：版本化 Markdown（复用 plan_handoff 存储与乐观锁），记长期约定与偏好；**定期压缩**（借鉴 Octop `/memory slim`）：超过 8KB 时管家在巡检中重写为精简版，旧版本保留可回看。
- 交互：工作页右下角「问管家」面板（复用工作台的 ACP 会话 UI）；手机可用。常用快捷问题："我手上还有什么没完成？""今天去现场要带什么/做什么？""把等资源的整理成清单"。
- 巡检：每日 `review_time` 由 wakeup 唤醒管家，处理当天有变化的工作项（触发整理、检查到期、提出合并建议、更新笔记），结果写入日志，并给每日摘要附一段点评。
- 时间线视图（借鉴 Octop 群聊式时间线，可选）：卡片详情的日志按发言者着色，人 / 会话 / 管家 / 整理器一眼可分。

### 14.5 二期分批

| 批 | 内容 |
|---|---|
| W2a | 发言者标注（字段与日志 `by` 规范化 + 卡片显示）；请求账本 + 请会话汇报/写交接（运行中走中继/传话，回填 `--request`，超时转整理）；被动整理（本机 transcript 解析 + worker 只读尾部接口 + 一次性 job + 写回规则 + 成本控制）；"整理建议"一键采纳 UI |
| W2b | 管家：配置与设置页、常驻 ACP job 与 prime、gofer MCP 自动注入与白名单、新增 MCP 工具、管家笔记与压缩、「问管家」面板、每日巡检与摘要点评 |

### 14.6 二期待确认（附建议）

1. **整理用的模型**：建议默认用你已有的 `claude` cli-agent 加一个便宜模型参数（如 haiku 级），可在设置里改；不开管家也能用被动整理。
2. **被动整理默认开启？** 建议开启（只在空闲 ≥15 分钟/离线/结束且有新活动时，每会话 30 分钟最多一次、每天 50 次上限）。
3. **搁置/需现场时自动请会话写交接？** 建议开启（会话在运行时才发；不在运行转整理）。
4. **管家默认关闭**，你在设置页选好 agent 后启用；先做 W2a 再做 W2b。

### 14.7 测试与验收（二期）

- W2a：transcript 尾部解析（claude/codex/omp 各一份 fixture）；整理写回不覆盖人/会话字段；请求账本状态机（sent→answered / expired→转整理）；`--request` 回填；成本上限；worker 只读尾部接口鉴权与大小上限。真实验收：临时 serve + 假会话 transcript，空闲触发整理，卡片出现整理建议并采纳；运行中会话收到汇报请求并回填。
- W2b：MCP 白名单（不能提交 job/改配置）；prime 截断优先级；切换 agent 后管家能复述在途请求与笔记要点；笔记压缩保留旧版本；巡检只处理有变化项。真实验收：主机临时 serve 上用 claude-acp 与 codex-acp 各当一次管家，问"我手上还有什么没完成"得到与工作页一致的答案。
