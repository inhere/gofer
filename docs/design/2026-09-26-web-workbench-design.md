<!-- template_id: design; template_version: 1.1.1 -->
# web 工作台设计（WEB-11）

> 状态：Draft 0.1 / 待批准

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-26 | Claude | 初稿：以 herdr 的「工作区 → 标签页 → 窗格 → 智能体 + 状态上卷」为骨架，映射到 gofer 已有的项目 / job / pty attach / ACP / 会话中继 / 验收台；三期 W1 外壳与状态侧栏 → W2 分屏布局与新建会话 → W3 ACP 对话窗格 |

## 背景与目标

用户（2026-09-24）：「想基于现有功能做 web 工作台页面，方便我远程**开始**工作——现在还是以终端为主，web 是被动处理任务；初步想法类似现在的 agent 桌面版。」

现在的 web 是**按对象组织**的（Board 看 job、Plans 看计划、Review 看待验收、Sessions 看会话），适合"来处理一件事"，不适合"坐下来干活"：要同时盯三个 agent、在其中一个里接着说话、顺手看一眼另一个的 diff，得在五六个页面之间跳。工作台要把它改成**按工作组织**：一个工作区里并排几个 agent 窗格，哪个卡住了一眼能看到，点进去就能接着干。

参考 herdr（https://herdr.dev/zh-cn/docs/concepts/ ，终端里的 agent 编排器）：

| herdr 概念 | 含义 | gofer 对应 |
|---|---|---|
| 工作区 Workspace | 顶层容器，"每个仓库、任务或调查一个"；侧边栏汇总内部 agent 状态 | **项目**（`project_key`），可再按 plan 细分（见 §一.1） |
| 标签页 Tab | 工作区内的布局单元，分隔 agents / logs / server / review 等视图 | 工作区内的**标签页**：自定义分屏页 + 固定页（Plan 看板、验收、文件） |
| 窗格 Pane | 实际的终端进程，可左右/上下分割、重命名、读取、输入、关闭 | **窗格** = 一个会话：交互 pty job（终端窗格）、ACP 会话（对话窗格）、中继的外部终端会话（中继窗格）、只读 job 日志 |
| 智能体 Agent | 窗格里识别出的进程，五种状态 blocked / working / done / idle / unknown | gofer **本来就精确知道**状态（不用像 herdr 那样看屏幕猜）：待答交互/审批 = blocked，running = working，needs_review 或刚结束未看 = done，空闲会话 = idle |
| 状态上卷 | blocked 的 agent 让窗格、标签页、工作区都显示 blocked；working 让工作区显示活跃 | 同样上卷到窗格 → 标签页 → 工作区，侧栏徽标 |
| 会话 Session / 客户端-服务器 | 服务器持有窗格与进程，多个客户端连接；可分离 | gofer server 持有 job；浏览器只是客户端，关掉页面 job 照跑，回来重新附着（`ctrl+b q` 的分离语义天然具备） |
| 前缀键 / 鼠标原生 | `ctrl+b` 前缀 + 动作键；点击、拖动、右键菜单 | 同样：`ctrl+b` 前缀（可改）做分屏/切焦点/关闭，鼠标拖分隔条、右键窗格菜单 |

**要吸收的核心**：① 工作区 → 标签页 → 窗格三层与**状态上卷**；② 分屏布局；③ 前缀键 + 鼠标原生；④ 服务器持有、客户端随时分离重连。**不照搬的**：herdr 靠屏幕快照猜 agent 状态——gofer 有结构化事件，不需要猜。

非目标：在浏览器里做完整 IDE（代码编辑器）；多人协作同屏；替代 CLI。

## 已确认事实（现有积木）

- 交互 pty job + web 附着：`web/src/components/AttachTerminal.vue`（1011 行，xterm、移动端输入框、写权限 ticket），`POST /v1/jobs/{id}/attach-ticket` + `GET /v1/jobs/{id}/attach`（WS）；会话 id 捕获与 `job resume` 续接（PTY-01、AGT-04、F11、F-e）。
- job 实时流：`GET /v1/jobs/{id}/stream`（SSE，stdout/stderr/status 事件）。
- ACP：`acp-agent` 类型，stdout 为 assistant 文本投影、`acp.jsonl` 为结构化记录，permission 交互卡（`components/InteractionCard.vue`），`job resume` 走 `session/load`（F13 已修）；**一个 ACP job = 一轮 prompt**。
- 会话中继（SESS-01/02）：外部 Claude Code/Codex 终端会话登记到 server，web 可回复、可 `--resume` 接管（`views/Sessions.vue`、`components/SessionDrawer.vue`）。
- 事件词表与中文标签：`web/src/utils/eventMeta.ts`；轮询工具 `utils/poller.ts`（可见性/失焦暂停）。
- 待处理的人：`interaction.created`（待答交互/审批）、`job.needs_review`、`plan.blocked`、`decision`（ask_human）——这些就是"blocked"的来源。
- 验收台 `ReviewPanel.vue` / `UnifiedDiff.vue`（REV-01）、计划看板 `PlanBoard.vue`（WEB-10）、评论 `CommentThread.vue`（MCP-05）。

## 一、概念与数据模型

### 1. 工作区（Workspace）

- 默认**一个项目一个工作区**（`ws = project_key`），侧栏列出用户最近用过/置顶的项目。可选"plan 工作区"：以某个 plan 为范围（窗格只放该 plan 的 job），用于一条链的专注工作。
- 工作区状态 = 其中所有窗格状态上卷（blocked > working > done > idle）；另外把该项目里**不在任何窗格中**的 blocked 事项（待答交互、needs_review、plan.blocked）也计入——不然在别处卡住的 agent 会被漏看。侧栏每个工作区一行：名称 + 状态点 + 计数（`2 working · 1 blocked`）。

### 2. 标签页（Tab）

- 每个工作区可有多个**分屏标签页**（用户自建，命名如 agents / debug），外加三个**固定标签页**：`Plan`（该项目 open plan 的看板）、`Review`（该项目待验收）、`Files`（项目文件树 + 最近 job 的 diff，复用 `FilePreview`/`UnifiedDiff`）。
- 标签页状态同样上卷（标题旁状态点）。

### 3. 窗格（Pane）

| 类型 | 内容 | 输入 | 来源 |
|---|---|---|---|
| terminal | 交互 pty job 的终端 | 键盘直通（写权限 ticket） | 现有 attach |
| chat | ACP 会话的对话流：assistant 文本、工具调用折叠行、permission 卡片内联 | 底部输入框发下一轮 | W3 新组件 |
| relay | 中继的外部终端会话：最近几轮 + 回复框 | 回复 / 接管 | 现有中继 |
| log | 任意 job 的只读日志（SSE 流），可一键"续接"成 terminal/chat | 无 | 现有 stream |

- 窗格头：agent 图标、标题（可重命名）、状态点、所属 job/会话 id、菜单（续接、取消、打开 job 详情、在新标签页打开、关闭窗格——**关闭窗格不等于停 job**，与 herdr 的分离语义一致；"停止"是单独的菜单项并二次确认）。
- 状态（gofer 直接给，不猜）：`blocked`（有待答交互/审批，或 ask_human 未答）、`working`（running / waiting_dir）、`done`（终态且用户未"看过"；needs_review 也是 done 并加标）、`idle`（会话存活但无活动 job，如 chat 窗格等你说下一句）、`gone`（job 已不存在/被清理）。

### 4. 布局与持久化

- 布局是一棵分屏树：`{split: "row"|"col", ratio: [..], children: [...]} | {pane: {kind, ref, title}}`。
- **存在 server 上**（表 `workbench_layouts {caller_id, workspace, tabs_json, updated_at}`），换浏览器/手机打开是同一个工作台；写入走 `PUT /v1/workbench/{workspace}`（乐观并发：带 `updated_at`，冲突 409 让前端重拉合并）。
- 手机/窄屏：不分屏，窗格变成可左右滑动的卡片，顶部是工作区/标签页切换。

## 二、交互

- **侧栏**（左）：工作区列表 + 状态；底部"待处理"汇总（所有工作区的 blocked 项，点一下跳到对应窗格或打开它）。
- **新建窗格**（`+` 或 `ctrl+b c`）：选 agent（按项目 `allowed_agents`，标出支持交互/ACP 的）、模式（终端 / 对话，按 agent 能力自动选默认）、cwd（项目内目录选择）、可选：挂到某个 plan todo、附加 skills/rules、初始 prompt → 提交对应 job 并在当前焦点窗格旁**分屏**打开。
- **键盘**（前缀默认 `ctrl+b`，可改）：`%`/`"` 右/下分屏、方向键切焦点、`x` 关闭窗格（分离）、`z` 最大化、`c` 新建、`n`/`p` 切标签页、`w` 工作区导航、`r` 重命名、`q` 把焦点从终端里拿出来（终端窗格聚焦时普通按键直通给 agent）。鼠标：拖分隔条调比例、拖窗格头换位置、右键窗格菜单。
- **通知**：窗格从 working → blocked/done 时，标签页标题闪烁 + 浏览器通知（用户授权后）；复用现有 `interaction.created` 等事件，不新增推送通道。
- **"看过"**：窗格获得焦点并停留 2s，或用户点"标记已看"，done → idle/清除高亮（记在布局里，按 caller）。

## 三、后端

- `GET /v1/workbench/summary`：按工作区（项目）返回窗格引用对象的状态 + 未在窗格中的 blocked 事项计数（聚合 jobs / interactions / decisions / needs_review / plan.blocked），供侧栏 5s 轮询（`createPoller`）；单次查询，避免前端扇出 N 个请求。
- `GET/PUT /v1/workbench/{workspace}`：布局读写（per caller）。
- W3：`GET /v1/jobs/{id}/acp/stream`——ACP 结构化事件（assistant 文本块、工具调用开始/结束、permission 请求/结果、turn 结束）的 SSE；现在 web 只能拿到投影后的 stdout/stderr，对话窗格需要结构化的。chat 窗格的"下一轮"= 对该会话 `job resume`（`session/load` 续上下文），前端把同一会话的多轮 job 串成一条对话；**长驻多轮 ACP 会话**（一个 job 内多轮）作为后续优化，不在本设计。

## 四、实施分期（omp，测试先写先提交；每期结束我远程升级并真机验收）

| 期 | 内容 | 验收 |
|---|---|---|
| **W1 外壳与状态** | `/workbench` 路由与导航入口；侧栏工作区列表 + 上卷状态 + 待处理汇总；`GET /v1/workbench/summary`；单窗格（terminal/log/relay 三种，复用现有组件）+ 固定标签页 Plan/Review；布局表与读写接口（本期只存"打开了哪些窗格"） | 单测：`TestWorkbenchSummaryRollsUpBlocked`（交互待答 → 窗格/工作区 blocked；不在窗格里的 needs_review 计入工作区）、`TestWorkbenchLayoutPerCallerOptimisticLock`；web：typecheck+build；真机：开三个窗格（一个交互 omp、一个跑着的批处理 job 日志、一个中继会话），制造一个待答交互看侧栏变 blocked |
| **W2 分屏与新建** | 分屏树渲染与拖拽调比例、前缀键、窗格菜单（分离/停止/重命名/最大化）、新建窗格对话框（agent/模式/cwd/todo/skills/prompt）、窄屏卡片模式、"看过"语义、浏览器通知 | web：typecheck+build + 组件级测试（若引入 vitest 仅限布局树的纯函数：`splitPane`/`closePane`/`movePane`/`serialize`）；真机：键盘全流程、刷新后布局还原、手机打开同一工作台 |
| **W3 对话窗格** | `GET /v1/jobs/{id}/acp/stream`；chat 窗格组件（流式文本、工具调用折叠、permission 卡内联作答、多轮 = resume 链）；Files 标签页（文件树 + 最近 diff） | 单测：`TestACPStreamEmitsStructuredEvents`、`TestChatTurnResumesSameSession`；真机：用 omp-acp 在 chat 窗格连续三轮对话，中间触发一次审批并在卡片里放行 |

## 风险与限制

- **范围大**：三期每期都是一个完整 omp job 的量；W2 的布局交互最容易拖长，前端复杂度集中在这里。
- **终端窗格性能**：多个 xterm 同屏 + 各自 WS；超过 4 个活跃终端窗格时，非焦点窗格降级为低频刷新（只收不渲染，获焦时补画）。
- **ACP 多轮 = resume 链**：每轮一个 job，有冷启动开销（ACP 进程重启 + `session/load`）；体感比长驻会话慢，后续可做长驻多轮。
- **"blocked" 的判定只覆盖 gofer 知道的**：agent 在自己的 TUI 里卡在确认框（不经过 gofer 审批门）时，gofer 只能看到"working 但无输出"——可借 AUTO-05 的输出停滞信号标成"疑似卡住"（黄色），不等同 blocked。
- **移动端**：分屏在手机上无意义，卡片模式够用；终端输入仍依赖现有移动端输入框。

## 决策（待批准）

1. 工作区默认 = 项目（可选 plan 工作区）；状态从窗格上卷，并计入项目内不在窗格中的 blocked 事项。
2. 布局存 server、按 caller 分；换设备同一工作台。
3. 关闭窗格 = 分离（job 照跑），停止是单独动作。
4. 前缀键默认 `ctrl+b`（与 tmux/herdr 一致），可在设置里改。
5. ACP 对话多轮先用 resume 链实现，长驻多轮会话另议。
6. 分三期 W1 → W2 → W3，每期结束我远程升级主机并做真机验收。
