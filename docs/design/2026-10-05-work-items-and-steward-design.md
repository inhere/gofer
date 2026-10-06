<!-- template_id: design; template_version: 1.1.1 -->
# 工作项（Work Item）+ 全局总览 + 管家会话（W 批）

> 状态：Approved（一期 v0.109.0 已上线；§14 二期 W2a、W2b 已实现；W3（v0.114.0）已实现，见 §15；X2 补缺口批已实现，见 §16）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-05 | Claude | 初稿：工作项模型、会话归属、被动整理 + 按需自汇报、搁置提醒、总览页、可换 agent 的管家 |
| 0.2 | 2026-10-06 | Claude | 一期已上线（v0.109.0）；新增 §14 二期详细设计（借鉴 Octop AgentTeams：调度不干活、异步请求账本、发言者标注、记忆压缩），待确认 |
| 0.3 | 2026-10-06 | Claude | W2a 已实现（分支 w2a-batch）：发言者标注、请求账本、被动整理（含 worker transcript_tail v17）；W2b（管家）未做 |
| 0.4 | 2026-10-06 | Claude | W2b 已实现（分支 w2b-batch）：管家（steward 凭据、MCP 注入、prime、笔记、巡检、事件、问管家面板）；见 §14.9 |
| 0.5 | 2026-10-06 | Claude | 补 W3（v0.114.0）小节 §15；修正 §9 / §14 与实现不符的描述（整理器配置键、巡检由 steward Tick 调度）；追加 X2 小节 §16 |

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
  - 定时：每日摘要前的整理。**实现**：由 steward 服务自己的 `Tick` 调度（到 `review_time` 起巡检会话），没有用 wakeup / schedule；
  - 事件：会话进入 offline / ended、工作项到期、出现新的草稿工作项（批量，节流 30 分钟）。
- **成本控制**：
  - 默认关闭，配置后启用；
  - 被动整理用 `work.summarizer_agent` / `work.summarizer_args` / `work.summarizer_project` 指定的便宜模型（一次性只读 job，与管家 agent 无关；不开管家也能用）；
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
- 巡检：每日 `review_time` 由 steward `Tick` 唤醒管家（非 wakeup），处理当天有变化的工作项（触发整理、检查到期、提出合并建议、更新笔记），结果写入日志，并给每日摘要附一段点评。
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

### 14.8 W2a 实现要点（2026-10-06）

- 整理 job 打内部标签 `work-summarizer`（与传话 `session-messenger` 同属 `jobstore.InternalJobTags`，列表 / Board 默认隐藏）。
- 实现与设计的取舍：整理输入 = transcript 尾部（claude / codex / omp 三种 jsonl 方言解析器）或降级材料；写回规则 + `work_suggestions`（采纳后按人写的算）；请求账本重启后由扫描器从表推进；worker 尾部接口是协议 v17 的 `transcript_tail` 帧，worker 自己再校验路径（绝对 `.jsonl` 常规文件，须在 home / `GOFER_TRANSCRIPT_ROOTS` 下），大小上限 512KB。
- 管家（W2b）只复用这里的账本与读写接口，`gofer_work_request_report` / `gofer_work_summarize` / `gofer_work_requests` 已按白名单思路实现。

### 14.9 W2b 实现要点（2026-10-06）

- **边界在服务端**：新增 job 凭据种类 `steward`（`jobstore.JobCredentialSteward`），`jobCredentialMiddleware` 对它**读写都默认拒绝**，只放行 `stewardReadAllow` / `stewardWriteAllow` 两张表（见 `internal/httpapi/jobcredential.go`）；处理函数再做目标级规则（`work.StewardUpdate`：不能 done / dropped，人手动设的状态优先，不能碰 `status_source`；合并只记建议 `work_merge_suggestions`，人在工作页 / `gofer steward merge-accept` 采纳才执行）。MCP 工具白名单（`GOFER_STEWARD=1` → `registerStewardTools`）只是让工具列表诚实，真正的边界是凭据。
- **标记与注入**：`JobRequest.Steward`（`json:"-"`，服务端盖章）→ `steward_jobs` 表（常驻会话重启恢复时据此恢复凭据种类与 MCP）；`applyStewardRun` 在 `session/new` 的 `mcpServers` 里注入本机 gofer 二进制（`gofer mcp`）与显式 env（`GOFER_JOB_TOKEN` / `GOFER_SERVER_ADDR` / `GOFER_JOB_ID` / `GOFER_STEWARD`）。管家会话 `ExclusiveDir=false`、`Channel=steward`、标签 `steward`、`IdleTimeoutSec = steward.idle_end_min*60`。
- **取舍**：管家 agent 自己的内置工具（如 claude-acp 的 shell / 文件读写）**不受 gofer 凭据约束**——凭据只管它对 gofer 的调用；prompt 要求它只用 gofer MCP。（W2b 时 issue 只读没有 MCP 工具，X2 §16 已补 `gofer_issue_list|get`。）`gofer_list_jobs` 是新增的只读工具。
- **服务**：`internal/steward`（生命周期 / ask 排队 / prime / 笔记 / 巡检 / 事件）经 `SessionHost` 接口取 job 能力；笔记复用 `plan_handoffs`（命名空间 `steward:notes`，16KB 硬上限，8KB 软上限由巡检提示压缩）；状态在 `work_kv`；`steward_events` / `steward_reviews` / `work_merge_suggestions` 三张新表。巡检只处理自上次巡检结束后有变化的未结项（用存储的 `last_activity_at`，会话心跳不算），无变化不起会话；`work.digest` 在当天巡检点评就绪时附「管家点评」。
- **无鉴权部署**：server 以 `allow_empty_token` 运行时 authMiddleware 不校验 bearer，所有 job 凭据（member / leader / steward）都不会被识别为 job，白名单只剩 MCP 工具层（真机验收发现，主机 / 容器验收都用了带 token 的临时 serve）。

## 15. W3（v0.114.0，2026-10-06）

- **导航与「等我」徽标**：顶栏菜单叫 Works（原「工作」），没有 Home 菜单项（点 Logo 回首页）；Works 菜单旁显示「等我」计数徽标，数据来自 `web/src/store/workNeedsMe.ts`（`GET /v1/work-items` 的 `summary.needs_me`，随 `work` 推送主题刷新）。
- **工作项转 plan todo**：`internal/work/totodo.go`，`POST /v1/work-items/{id}/to-todo {plan_id?, new_plan_title?}`、`gofer work to-todo`。todo 标题取工作项标题、描述取目标 + 下一步 + 来源；工作项记 `todo` 与 `plan` 两条关联；只能转一次（再转 409，响应体带已有 todo / plan）；job 凭据与管家不可调用。
- **ACP / pty job 会话关联工作项**：`work_item_sessions` 的 session id 除终端中继会话外，也可以是 ACP 持续会话 / 终端（pty）job 的 job id（`work link --acp <job-id>`，`SessionBrief.kind=job`，web 的「打开」去 `/jobs/<id>`）；这类会话不参与「请它汇报」。
- **omp 解析修正**：被动整理的 omp transcript 解析方言修正（见 §14.8 的三种 jsonl 方言）。
- 不在本批：plan decision → needs_me、管家带话 / issue 只读、完成回写、`work.needs_me` 通知——留给 X2（§16）。

## 16. X2：补齐设计缺口（2026-10-06）

对照 §5 / §8 / §10 审计后补的六项（additive，无 schema 变更）：

1. **plan decision → needs_me**（§5）：`work.derivedStatus` 新增 `planWait` 入参。工作项的 `plan` 关联、或关联 job 所属的 plan（`JobState.PlanID`），只要有 OPEN 且非 relay 的 `plan_decisions`，就派生为 `needs_me`（排在 job 待应答交互同级、`review` 之前）。decision 写入 / 应答已经触发 `ChangeDecision` → `MarkDirty`，所以 0.3s 去抖后重同步；应答后回到会话映射。
2. **管家带话 `gofer_session_ask`**（§9 / §10）：`work.Service.AskSession`。终端中继会话走与 web 传话相同的 `relay.SendMessage`（`workMessenger`），ACP / pty 持续会话 job 走 `job.SaySession`（`SessionSayer` seam）；会话不在线（offline / ended / handed_off、job 已结束）、送达失败一律返回明确错误（409），**不排队、不静默丢**；送达后写工作项日志（发言者 = 调用者，管家为 `steward(<agent>)`，对会话加前缀 `[gofer 管家带话]`）。REST `POST /v1/session-ask`；steward 白名单放行该写路由，普通 member / leader 凭据仍被默认拒绝；`--project` 收窄的 MCP 不提供该工具。
3. **管家 issue 只读 MCP**（§10 / §14.4）：`gofer_issue_list` / `gofer_issue_get` 读 server 端 tracker 镜像（`GET /v1/issues`、`GET /v1/issues/{id}`，按 project / 仓库 / 状态 / 标签 / 关键字过滤，同 id 多仓库要带 `tracker_id`）；只读，白名单放行这两条 GET；巡检 prime 与角色提示里提示使用。同时修正 §14.9「issue 只读没有现成 MCP 工具」的旧说法。
4. **完成回写**（§11 三期「与 issue / todo 双向联动」的第一步）：每次同步扫描（含 `ChangePlan` 触发）检查工作项关联的 todo / issue；全部关联完成时**不改状态**（人优先），而是写一条 `status_hint` 整理建议（只有 todo → `review`，含已关闭 issue → `done`）+ 一行 `system` 日志；已有同值 pending 建议不重复写、人忽略过的同值不再提。`AcceptSuggestion` 放宽为接受任何合法状态（采纳是人的决定；整理器自己从不产生终态 hint）。Web 抽屉 / 卡片的建议区原样可用。
5. **`work.needs_me` 通知**（§8 / §10「可选」）：工作项进入 needs_me（自动同步、手动 `Update`、会话汇报、管家标记）时发事件；`work.needs_me_notify`（默认 **false**）开关 + `work.needs_me_throttle_min`（默认 30，按工作项节流，记在 `work_kv` `needs_me_notified:<id>`）；事件常量 `notify.EventWorkNeedsMe`，进默认触发集（没写 `events` 的 webhook 会匹配，但总开关默认关所以不会多出流量）。`/settings/work` 页「等我通知」区可配。
6. **文档 / skill**：本节、`skills/gofer-usage/SKILL.md`（工作项状态映射、等我通知、完成回写、带话、issue 只读、管家白名单）、`docs/runbook/im-notification.md`。

验收（临时 serve + 假数据）：decision 使工作项变「等我」；todo 完成 / issue closed 后出现建议（采纳前状态不变）；设置页开关；`/v1/issues` 过滤；`/v1/session-ask` 对离线 / 未知会话返回 409 / 404。
