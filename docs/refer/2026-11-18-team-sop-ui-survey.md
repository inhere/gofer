# 参考调研：Team / 共享对话 / SOP / 界面（LoopForge、Multica、LobeHub，2026-11-18）

> 参考文档，只做参考。源码阅读，未运行。版本：LoopForge `f8dcd1e`（2026-11-18）、Multica `3a181ffc8`（2026-11-18）、LobeHub 2026-11-11 快照。
> 已有笔记不重复：`reference-projects.md`（Multica 09-20、LobeHub 11-11 两节）、`2026-11-15-loopforge-notes.md`（L1–L7）、`2026-11-11-super-agent-workflow-survey.md`（①–㉗）。
> 用于设计 `docs/design/2026-11-18-resident-sessions-and-team-design.md`（gofer-0py5）。

## 0. 三个项目各自收敛成什么

| 项目 | Team 形态 | 谁决定下一步 | 给 gofer 的结论 |
|---|---|---|---|
| LoopForge | **已撤掉「角色群 / 自由协作」**（`docs/conversation-workspaces.md:54`、`internal/domain/group_plan.go` 注明 retired role group），收敛为：一个 Main Bot 主持 + @ 召唤角色子 agent（渲染成原生 CLI subagent）+ SOP 固定流程 + 人审批 | SOP 图（确定性）；主持人只能「建议」走 SOP | 多 agent 自由群聊是走过又退回的路 |
| Multica | Squad：一个 leader agent + 成员（agent 或人），leader 不实现、done 留给人；新增**项目工作流**（每列一步：处理人、指令、下一步、退回步） | 工作流 + 显式命令推进；leader 每轮必须记一次评估 | leader 协议、路由默认值、「每轮注入你在哪一步」可几乎原样借 |
| LobeHub | 旧 Chat Group（supervisor 模型每轮决定谁说话）+ 新 Channel（人与 agent 同为成员，确定性规则路由，模型只兜底） | Channel：@ 必答、跟进窗口、职责匹配；agent 回复不再触发路由 | 不学每轮用模型调度；Channel 的「公开层 + 每成员私有会话 + FIFO」是群组骨架 |

## 1. Team / 群组

- **leader 协议文本**（Multica `server/internal/handler/squad_briefing.go:34-347`）：每次 leader 领任务都注入「协议 + 花名册 + 小队指令」。协议：读任务 → @ 派活（简短、不复述）→ **必须**记一条评估 `action | no_action | failed --reason` → 派完即停；无事只记 no_action、不发言。状态权限：本队任务 leader 推到 in_review，done 留给人；被别人 @ 进来不改状态。
- **路由默认值**（Multica `comment.go:1481-1530`、`squad.go:1192-1295`）：成员不带 @ 的进展 → 唤醒 leader；工作 agent 只 @ 人也回 leader；人 @ 人不触发 agent；显式 @ 只跑被点名者；leader 不自触发；已有排队轮次就合并。
- **成员状态五档**（Multica `squad.go:618-674`）：archived / working / idle / unstable / offline；接口同时给每个成员正在跑和排队的任务。
- **Channel 路由**（LobeHub `apps/server/src/services/channel/README.md`、`ChannelRoutingService.ts`）：@、私聊、1 对 1 直接投；无 @ 时按成员自己的规则（点名、跟进窗口、职责、排除）；回 `NO_REPLY` 记沉默，连续沉默的成员不再凭猜测唤醒；agent 回复不触发路由（防互相拉起）；每次判定落库可审计。
- **上下文分层**（LobeHub）：频道是公开层；每个 agent 对每个「请求人 + 分支」保留私有会话；每次只推最近 5 条未看消息，有遗漏加一个 gap 块，更早的按需用工具读。
- **挂起 → 等齐 N 个成员 → 再唤醒**（LobeHub `agent-runtime/src/groupOrchestration/`、`AgentRuntimeService/groupMember.ts`）：带 maxRounds、成员超时。
- **主持人 + 原生子 agent**（LoopForge `internal/httpapi/assistant_subagents.go:51-137`）：角色渲染成 CLI subagent 文件，禁用主持人专属工具，按 agentId 续用；UI 有子 agent 面板。
- **产物交接**（LoopForge `internal/domain/issue_sop.go`、`dispatch.go:258-297`）：多段 SOP 用 DependsOn / Handoff 串起，派发时追加「从上一步的产物继续，不要重做」。
- **消息不丢、可解释**（Multica migration 150 / 157 / 538、`dispatch/reason.go`）：新消息合并进待跑轮次并记投递回执；运行中插话可选「现在插入 / 本轮后 / 替代 / 跳过」；发送前预览「这条会被谁怎样处理」与不会运行的原因码。

## 2. 流程（SOP）与自动推进

- **Multica 项目工作流**（`server/internal/issueworkflow/issueworkflow.go:160-166, 302, 346-398, 762-902`）：步骤 = {状态, 处理人, 指令, 下一步, 退回步}；进入某步自动指派并起一轮；每轮注入 `[Project workflow]` 块：全部步骤与处理人（标 ← current）、当前步指令、三条动作（完成 → 下一步 / 要改 → 退回 / 做不了 → blocked + 原因）；**一轮结束不自动推进，必须显式执行动作**；无处理人的步骤只是进度标记，由同一人做完整段（曾出现 agent 做完前半就停）；编辑器预览注入文本。注入块放在每轮用户消息尾部，不破坏提示缓存（`daemon/prompt.go:53-80`）。
- **LoopForge devflow**（`local/skills/devflow/config/workflow.json`、`scripts/workflow_state.py:653-780`、`scripts/validate_plan.py`）：路由表按规模分流（small：直接做 → 汇总；medium/large：需求 → 设计 → 实现 → 评审 → 测试 → 沉淀 → 汇总）；完成某阶段时硬校验：必需产物缺失或自开始以来没改动就拒绝；设计阶段必须带合格的执行计划（任务有状态与目标文件、验证段有可执行命令、风险与回滚）；评审失败把实现重置回 pending；同一阶段失败 2 次 → blocked；门禁阶段需确认才继续。缺点：状态靠模型自己调脚本维护。
- **LoopForge SOP 引擎**（`internal/domain/workflow.go`）：节点 agent / script / approval / 子 SOP；门禁 command / manual / mr，失败 loop / reject / fail / human；run 启动冻结流程快照；审批可附「人工补充上下文」写进下一轮工作区（`internal/engine/human_context.go`）；打回重置目标节点及下游。内置「通用后端交付」：需求定型 → 方案设计 → 实现 → 独立评审 → 测试 → 人工验收（打回到实现）→ 发起 MR → 交付汇总，每步声明必需产物。
- **流程建议卡片**（LoopForge `internal/httpapi/assistant_proposals.go:227-540`）：主持人判断该走流程时只能「建议」{reason, timing}；timing=start 显示 3 分钟倒计时，无人处理就自己做；timing=end 先做完再建议；拒绝一次本会话不再提。用户可设「主动走流程」开关与 ≤4000 字自定义规则，每轮注入。
- **LobeHub 卡点**（`packages/types/src/task/index.ts` CheckpointConfig、`services/taskLifecycle/index.ts`）：阶段前 / 后暂停、agent 主动请求暂停；阶段结束依次 handoff 摘要 → review → checkpoint。Plan / Todo 每轮重新注入（`context-engine/src/providers/PlanInjector.ts`、`TodoInjector.ts`）。
- **带到期的唤醒**（Multica migration 675、`handler/chat_wakeup.go`）：会话登记 condition / at / check（无事静默）后结束本轮，`expires_at` 必填（默认 24h、最长 7 天），到期再跑一轮汇报；每会话 ≤ 10 条；输入框上方提示条显示正在等什么。
- **阶段屏障**（Multica migration 123、`service/issue_wakeup_system.go`）：同一阶段子任务全部关闭时唤醒父负责人放开下一阶段。

## 3. 常驻 / 固定用途会话

- **定时任务 + 固定会话**（LoopForge 11-17 新增，`internal/domain/sync.go:88-99`、`schedule_execution.go:85-97`、`design.md:181-189`）：每次新开或「始终同一会话」，保留 N 个；每次执行一条记录、能「打开会话」；列表行显示「同一会话」。直接对应周报、周分析、PR 巡检。
- **固定用途 = 角色 + 开场提示 / 快捷动作**（Multica `quick_action`、agent `conversation_starters`）：预设「找谁 + 说什么」，按使用次数排序。
- **规范文档注入规则**（LobeHub `packages/agent-templates/src/types.ts`）：每个文档配注入位置（system 前 / 追加 / 首条用户消息前 / 按需）与规则（总是、关键词、时段）。固定格式周报模板、PR 审查清单就是「总是注入」的文档。
- **续接告知**（Multica `daemon/prompt.go:18-51`、`handler/daemon.go:3593-3617`）：按身份续接原 CLI 会话；续不上就新开并注入连续性提示，不假装记得。
- **会话关联任务**（LoopForge `conversation_sop.go`）：会话里提到的需求成主需求，会话里派发的任务反向关联；之后每轮上下文列出其流程状态与分支。

## 4. 「需要我」与主动通知

- **状态分桶**（LobeHub `packages/utils/src/client/topic.ts` resolveStatusBucket）：等人 / 失败 / 完成未读合成一个「需要你」桶置顶，定时单列，收藏最上；头像角标未读红、运行中黄；done 后变未读，正在看的不标。
- **顶部弹卡**（LobeHub `src/features/GlobalApprovalNotification/`）：不在当前屏的会话需要人时，顶部中间弹一张卡，一次一件（其余 +N），可就地批准 / 拒绝或跳转；关掉只隐藏，收件箱保留。
- **系统通知**（Multica `packages/core/platform/system-notification.ts`、LobeHub `desktopNotification.ts`）：只在页面失焦时弹，按条目去重，点击深链到会话；进行中状态不弹。
- **发送前复核**（LoopForge `internal/notification/service.go:169-215`）：默认 actionable 策略，发之前再查一次是否仍在等，已结束就不发。
- **角标必须可清除**（Multica `layout/app-rail.tsx:66-112`）；收件箱分级：「agent 在等你」不可静音，点名与普通评论分开静音，状态变更默认静音；延后遇到点名、失败、子任务完成提前回来；E 归档并打开下一条（Multica `inbox/registry.go:33-98`、`snooze.go`）。
- **会话内提醒**（LoopForge `chatbot/src/RunAlerts.jsx`）：在消息流里列本会话关联任务的失败与待人工节点；「不再提醒」按 运行 + 节点 + 轮次 + 状态 记，下一轮进入等待会重新提醒。
- **agent 主动找人**（LobeHub `packages/builtin-tool-brief/`）：createBrief（decision / result / insight × urgent / normal / info，按钮 resolve / comment / link）、requestCheckpoint；brief 只是通知，卡点状态存在任务上。

## 5. 界面

- **三栏**（LoopForge chatbot）：图标栏 + 会话侧栏（304px，可拖）+ 对话 + 右抽屉（440px，「产物 / SOP」两页签）。
- **会话侧栏规则**（LoopForge `chatbot/src/session-sections.js`、`session-history.js`）：置顶组；其余按项目**固定顺序**分组，打开或回复不让分组上移；每组显示前 6 条并补上当前会话；隐藏没人说过话的已结束运行会话；搜索标题 / 流程名 / 项目；行尾三态（回复中转圈、失败感叹号、离开期间完成的未读蓝点，已读服务端存）；状态标签只在需要人时出现。
- **Team 面板**（Multica `layout/panels/team-panel.tsx:239-288`）：需要关注 / 正在工作 / 全部 / 小队与成员 / 资源，每区 5 条可展开。
- **底部会话标签栏**（Multica `layout/bottom-bar.tsx`、`chat/components/chat-bar.tsx`）：每个会话一个标签，转圈 = 回复中、彩点 = 未读，关掉的标签有新消息自动回来；快捷键打开上次的会话。
- **快速切换**（LobeHub）：Mod+K 命令面板、Ctrl+数字切换、收藏任意路径、托盘列出置顶 / 最近 / 等人的会话、把会话拖进对话区并排打开。
- **群组界面**（LobeHub `src/features/Channels/`）：头部成员头像带状态点；输入框上方「谁在处理」；从公开回复跳到产出它的私有会话；只用四种色调（工作中蓝、需注意琥珀、出错红、已结束灰）。
- **排队消息托盘**（LoopForge `QueuedMessages.jsx`）：回复中发的消息进托盘，可引导 / 编辑 / 删除，切换会话仍保留。

## 6. 不借鉴

- 每轮用模型决定谁发言（LobeHub supervisor）、Channel 的模型路由与概率阈值。
- 多人项目成员 / 职位 / RBAC、多工作区可见性（SaaS 取向；LoopForge Room / Space 公开版 UI 也未接入）。
- 模型生成的通知摘要；定时任务以完全权限执行。
- 流程状态靠模型自己调脚本维护（LoopForge devflow）——gofer 把状态放在服务端判定。
