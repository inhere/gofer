<!-- template_id: design; template_version: 1.2.0 -->
# 第 5 批：web 会话体验（容器 claude-acp） 总设计

> 状态：Approved（Draft 0.1；用户 2026-11-16 批准：Q1–Q7 按推荐，Q6 调整为「让出目录锁」提前到本批 P1，常驻与自动接回仍下一批）
> issue：gofer-59vu（等待输入让出目录锁）、gofer-xmit（空闲常驻会话拖住 worker 升级）、gofer-geyu（会话已死时 job 无法结束）、gofer-byvi（恢复后 secret 掩码）；关联 gofer-9dbh（工作区代理）、gofer-t28g（决策 hook）、gofer-ds7i（卡片绑定轮次）
> 关联设计：第 4 批 `2026-11-16-plan-driver-autonomy-design.md`（决策卡）、`2026-10-12-session-question-relay-design.md`（gofer-02fu，终端选择题转 web）、`2026-11-10-acp-session-history-design.md`（gofer-7yrg，ACP 历史）、`2026-11-09-scope-derived-dir-lock-design.md`（Q6/Q7 deferred）
> 依据：2026-11-16 真实试用（容器 worker 上 `claude-acp` 持续会话两轮 + 无头浏览器截图）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-16 | Claude（gofer 监督会话） | 初稿：试用发现逐条核对代码，四块方案 |

## 背景与目标

用户设想过两条用 gofer 的路：一条是在终端里给监督会话下方向；另一条是不开终端，在 web 里通过 gofer 起一个**容器里的** claude ACP 会话直接用。第二条一直没用起来。用户反馈三点：历史会话入口不好找；显示体验一般（markdown 没渲染、tool 调用没合并）；交互不全（权限请求、确认、选择）。

试用确认：容器 worker 上起 `claude-acp` 已经能跑通，看得到容器环境与 `CLAUDE.md`；工作台对话视图里 markdown 渲染、tool 调用折叠为「N tool calls」。问题集中在交互、显示一致性、入口和运行底座。

目标：用户只开 web（含手机），就能在容器里起一个 claude ACP 会话持续工作：需要人的地方都是可点选的卡片；所有对话显示一致；会话从一个入口进、能找回历史并接着聊；会话不因空闲、重启或升级而中断，也不挡住同目录的其它 job。

成功标准：用户用 web 会话完成一批真实的小任务，全程不开终端；期间所有提问、确认、权限都以卡片作答，没有「回复 A 或 B」式的退化，也没有原始 JSON。

## 范围与非目标

- 范围：ACP 会话的提问 / 选择 / 确认工具与卡片；终端会话选择题的卡片修正；统一卡片组件（供第 4 批决策卡复用）；三处对话显示合一；工作台作为唯一会话入口、Sessions 页改历史；新建会话默认值与权限模式；容器适配器冷启动；等待输入让锁；常驻与自动接回；job 完成通知送达 ACP 会话。
- 非目标：「工作区代理」（gofer-9dbh）的自主判断，归第 4 批及以后；终端 pty 会话在 web 内的终端渲染改造；新增 agent 类型；改用户的 worker.yaml（只给配置步骤）。

## 已确认事实（代码，main 19a44c67；试用记录在工作区 tmp 下，截图不入库）

### 交互

1. 终端会话的选择题已有转 web 链路（gofer-02fu）：PreToolUse 遇到 `AskUserQuestion` 走 `askUserQuestion()`（`internal/hookrelay/question.go:51`），人不在终端前时开带 `questions` 的卡片（`question.go:86-89`）；人在时只上报「等你回答」消息、不开卡（`question.go:75-78`）。
2. 同一工具的 **PermissionRequest 事件没有 AskUserQuestion 分支**：`run.go:233-234` 一律进 `permissionRequest()`（`permission.go:413`），摘要由通用 `PermissionView` 生成（`permission.go:151-176`）——没有 command / path / url / query 字段时，摘要就是整个输入的 JSON。试用截图里终端会话「最后一条消息」显示的「Permission needed: AskUserQuestion {"questio…」与随后「Expired / unanswered」即此路径（推断：PreToolUse 判定人在终端、未开卡，随后 PermissionRequest 走了通用权限卡）。
3. ACP 会话没有提问工具：`claude-agent-acp` 不暴露 `AskUserQuestion`（试用第 2 轮 agent 自述，退化为正文「回复 A 或 B」）；gofer 只给**管家** job 注入自己的 MCP（`internal/job/steward.go:69-86`，`applyStewardRun`），普通 ACP 会话的 `mcpServers` 只来自 agent 配置 `acp.mcp_servers`（`internal/runner/acp/runner.go:167-190`）。
4. 已有三套「要人回答」的机制：
   - job 交互（P9）：`question / choice / confirmation`，`job.CreateInteraction`（`internal/job/interaction.go:153`）、HTTP `POST /v1/jobs/{id}/interactions`（`internal/httpapi/server.go:1917`）；ACP 审批闸门也用它开卡（`internal/job/approval.go:26`）；web 由 `InteractionCard.vue` 渲染，已用于工作台对话、job 详情、plan 详情，并支持 decision 投影（`isDecision`）。
   - 终端会话权限卡（含 `questions`）：`SessionPermissionPrompt.vue`。
   - plan 决策：MCP `gofer_ask_human`（`internal/mcpserver/server.go:300`、`:1943`），阻塞轮询直到作答或过期（`server.go:1793-1830`）。
5. ACP 权限分两层：gofer 审批闸门 `approval.mode` 为 `off / ask / strict`，默认 `off` 全部自动放行（`internal/config/model.go:2776-2790`）；agent 自身的会话模式（试用会话以 `auto` 启动，可选 `default / acceptEdits / plan / auto / bypassPermissions`，见 acp.jsonl 首行）。新建会话表单没有任何一层的选项（`web/src/ui/NewSessionSheet.vue`，字段见文件头注释 :3-4）。`SessionApproval` 只能由 server 设置，入口层拒收（`internal/job/model.go:197-204`）。
6. 终端 hook 在 gofer job 内不生效（`internal/commands/hook.go:312`，按 `GOFER_JOB_ID` 判定），所以 ACP 里的 claude 不会被重复注册成终端会话。

### 显示

7. 三处对话显示各写一套：
   - 工作台 `ConversationView.vue`：assistant 用 `MarkdownBlock` 渲染（:322），tool 调用分组折叠（:353）；**用户气泡显示 prompt 事件原文**（:310-313），数据来自 `/v1/jobs/{id}/events`（`web/src/api/client.ts:1830`）读 acp.jsonl，未经 `job.SplitInjectedRules`——注入的规则全文显示为「你」的第一条消息（手机截图占满一屏）。
   - job 详情 Messages：`<pre>` 纯文本、不渲染 markdown（`web/src/views/JobDetail.vue:2553`），但注入规则已拆出、折叠在「查看注入内容」里（:2555-2558；服务端 `internal/httpapi/job_messages.go:158`、`:202`）。
   - Sessions 抽屉（终端会话）：agent 消息 marked + DOMPurify 渲染（`web/src/components/SessionDrawer.vue:261-272`、:1329），人的消息 `<pre>`（:1347）；终端会话本来就拿不到 tool 调用。
8. 服务端已有统一拆分函数 `job.SplitInjectedRules`（`internal/job/rules.go:361`），messages 与 transcript（`internal/streaming/job_transcript.go:489`）在用，events 接口没用。

### 入口与历史

9. gofer-7yrg 已做（`2026-11-10-acp-session-history-design.md` 实施记录）：工作台接口 `source / live / include` 筛选与深链打开时间窗外线程；Sessions 页 ACP 页签（`SessionsAcpTab.vue`）按线程出卡、服务端分页 / 项目 / live，「打开会话」进工作台。
10. ACP 页签计数 0 / 1 不一致的原因：计数只由 ACP 页签组件挂载后上报（`web/src/views/Sessions.vue:231`、`:271-273`、`:897-909`，`v-else-if="active === 'acp'"`），不在该页签时一直是初值 0。
11. 新建会话表单默认类型 `pty`、runner `local`、agent 取列表第一个（`NewSessionSheet.vue:27`、`:36`、`:101-106`）；ACP 类型下 runner 只放出 local 与 worker（:71-72）。
12. 已结束会话接着聊：job 详情的「继续会话」仍是旧式「Follow-up request」表单（试用截图）；工作台线程对已结束会话的续接入口以 gofer-7yrg 的深链为准，未见一键 `session/load`（计划阶段实测确认）。

### 运行底座

13. 内置模板 `claude-acp` = `npx -y @agentclientprotocol/claude-agent-acp`（`internal/agent/templates.go:82-100`）；模板在 PATH 能找到命令时注入、运维显式声明的同名 agent 整条覆盖（`templates.go` 文件头注释）。容器 worker.yaml 未声明 `claude-acp`，靠模板（npx 在 PATH）。试用首启约 2.5 分钟（npx 下载）。
14. 目录锁：可写 agent job 默认独占（`internal/job/submit.go:1505-1532`，`!ReadOnly && !Interactive`）；ACP 持续会话不是 interactive，所以独占；独占覆盖**祖先与后代目录**（`internal/job/model.go:797-803`）。试用会话 cwd 为项目根（工作区根），等待输入期间会让工作区下所有可写 agent job 排队。让锁已在 gofer-59vu 登记（`2026-11-09-scope-derived-dir-lock-design.md` Q6 deferred）。
15. 空闲上限：会话 `IdleTimeoutSec` 为 0 时取 1800（`internal/job/submit.go:299-301`），`--max-session` 默认不限。
16. 自动接回：serve 重启时只接回**本机**会话（`ClaimLocalSessionsForRecovery` → `resumeLocalSession`，`internal/job/interaction.go:380-399`、`session_recovery.go:40`）；worker 上的会话在 worker 重启 / 升级后没有对应的自动接回路径（未找到；gofer-xmit 记录了空闲常驻会话拖住 worker 升级排空）。
17. job 完成通知：`NotifyJobEnded` 的目标是 hook 注册的 agent 会话，经 messenger `SendMessage` 投递（`internal/sessionrelay/notify_job.go:155-190`、`message.go:108-120`）；job 内的会话 id 环境变量被剔除（`internal/job/credential.go:94-98`）。ACP 会话 job 既不是 agent 会话表里的目标，里面提交的 job 也没有可通知的来源会话——**目前不支持**送达 ACP 会话。
18. 容器时区：试用中 `date` 输出 PST，与 +08:00 不一致（环境项，不在 gofer 内修）。

## 总体方案

### 1. 交互补齐（最优先）

- **A. ACP 会话的提问工具**：所有 ACP 会话 job 注入一个收窄的 gofer MCP（沿用 `applyStewardRun` 的注入方式，工具集另列），只含 `gofer_ask`：参数 `question`、`options[{label, description}]`、`multi`、`allow_free_text`、`timeout_sec`；实现为对**本 job** `CreateInteraction(choice|question|confirmation)`，阻塞到作答 / 过期后返回 `{state, answer}`，过期时 agent 用自己的推荐项继续（与 `gofer_ask_human` 同口径）。凭据用该 job 自己的 job 凭据，只能对本 job 开交互。worker 上的会话经现有远端交互注入（`remote_interaction.go`）回到 server。
  - 会话首轮提示里加一句「需要人选择 / 确认时用 gofer_ask」（随注入规则一起，折叠显示）。
- **B. 终端会话选择题修正**：`permissionRequest()` 遇到 `AskUserQuestion` 改走选择题分支：人不在时开带 `questions` 的卡片（与 PreToolUse 同一张，按 fingerprint 去重），人在时只报「等你回答：<问题>」，不再生成 JSON 摘要。`PermissionView` 对 `AskUserQuestion` 输出问题文本作摘要，作为兜底。
- **C. 建会话选权限**：新建会话增加「权限」一项，三档：`自动`（gofer `off` + agent `auto`，现状）/ `问我`（gofer `ask`：低风险种类自动，其余出卡）/ `严格`（gofer `strict`）。实现上由 server 按选项设置 `SessionApproval`（保持「入口层不能直接传」的约束：入口只传档位名，server 映射并受项目上限约束，只能收紧不能放宽）。
- **D. 统一卡片**：以 `InteractionCard.vue` 为唯一卡片组件，扩展：多问题、多选、自由文本补充、推荐项标注与理由、「交给它决定」（第 4 批）、过期 / 已答只读态。`SessionPermissionPrompt.vue` 的选择题与权限卡改为复用它（数据适配，不改服务端契约）。第 4 批 `driver.decision` 卡直接用同一组件（它已有 decision 投影）。
- 卡片出现位置：所在对话里内嵌 + 「等我」页（现有 attention 类）+ 推送。作答后对话里留只读记录。

### 2. 显示统一

- 抽一个 `ConversationTimeline` 组件，工作台对话、job 详情 Messages、Sessions 抽屉都用它：
  - 人的消息与 agent 消息都按 markdown 渲染（DOMPurify 净化，复用 `MarkdownBlock`）；
  - tool 调用按轮折叠成「N tool calls」，展开看每个调用的标题、参数摘要与结果（终端会话没有 tool 数据，自然不显示）；
  - 注入内容折叠成一行「gofer 规则 · N 条」，点开看全文；
  - 交互卡片内嵌在时间线对应位置。
- 服务端 events 接口对 `prompt` 事件做 `SplitInjectedRules`，拆出的部分放 `injected` 字段（与 messages 接口同口径）；前端不自行解析规则格式。
- job 详情的旧式「Follow-up request」表单对 ACP 会话改为与工作台一致的输入框 + 「接着聊」。

### 3. 入口与历史

- **工作台是唯一的会话入口**：侧栏按「需要我 / 进行中 / 历史」分组（现有分组保留），顶部「＋新建会话」。
- **Sessions 页改为历史**：一个列表含三类会话（终端 / ACP / pty），支持关键词搜索（标题、主题、最后消息）、按项目 / agent / runner / 状态筛选，默认含已结束；各页签计数由服务端统一返回（修事实 10）。点任一行进工作台对应线程。
- **已结束会话一键接着聊**：ACP 会话行与工作台线程上「接着聊」= 新开持续会话 job、`session/load` 原会话后停在等待输入（S 批 S2 的语义），不必填续接指令。
- **新建会话默认值**：类型 ACP、agent `claude-acp`、runner 优先选带 `container` 标签且在线、支持该 agent 的 worker（没有则回落 local）；项目记住上次选择。表单其余项收进「更多」。

### 4. 运行底座

- **冷启动**：模板改为优先用 PATH 上的 `claude-agent-acp`（容器预装后即用），找不到再回落 `npx -y …`；同时在 runbook 写用户步骤：容器里 `npm i -g @agentclientprotocol/claude-agent-acp`（gofer 不改用户 worker.yaml）。worker 能力上报带上适配器来源（PATH / npx），新建会话时提示「首次启动需下载」。
- **等待输入让锁**（gofer-59vu）：持续会话进入 `awaiting_input` 时释放目录锁，下一轮开始前重新获取；获取不到时这一轮排队，会话状态显示「等目录锁：被 <job> 占用」。风险与语义见下「安全」。
- **常驻**：新建会话可选「常驻」：`idle_timeout` 不限（仍受项目上限）、不计入 worker 升级排空的 in_flight（gofer-xmit：升级时先把空闲常驻会话挂起，升级后 `session/load` 接回）。
- **自动接回**：worker 重启 / 断线恢复后，对该 worker 上非终态的持续会话按本机路径同样 `session/load` 接回；接回失败才判失败（并修 gofer-geyu 的「会话已死无法结束」、gofer-byvi 的 secret 掩码）。
- **通知送达 ACP 会话**：ACP 会话 job 在 sessions 层注册一个可投递地址（`job:<id>`），`--notify` 的来源会话解析到它；投递 = 在会话等待输入时作为下一条 gofer 系统消息送入（`[gofer · job 完成] …`），会话在跑时排队到本轮结束。job 内提交的 job 以 job 凭据识别来源 job。
- 容器时区：只在 runbook 记一条（容器设 `TZ`），不改代码。

## 架构与分工

| 块 | 落点 | 实施 |
|---|---|---|
| `gofer_ask` MCP 注入与 job 内鉴权、远端交互回传 | `internal/job`（注入，复用 steward 注入模式）、`internal/mcpserver`（工具）、`internal/job/remote_interaction.go` | **sonnet**（协议 / 鉴权） |
| 终端 PermissionRequest 的选择题分支 | `internal/hookrelay` | **sonnet**（hook 并发与卡片去重） |
| 权限档位 → `SessionApproval` 映射与上限 | `internal/job/submit.go`、`internal/config` | **sonnet**（鉴权） |
| 等待输入让锁 / 重新获取 | `internal/job`（dir lock） | **sonnet**（并发） |
| worker 会话接回、升级挂起、通知送达 ACP 会话 | `internal/job`、`internal/worker`、`internal/sessionrelay` | **sonnet**（协议 / 并发） |
| events 接口拆注入规则、各页签计数接口、历史搜索筛选接口 | `internal/httpapi`、`internal/workbench` | suga |
| 统一卡片、统一时间线、Sessions 历史页、新建会话表单、job 详情输入框、文案 i18n | `web/src` | suga |
| 模板优先 PATH、能力上报适配器来源、runbook | `internal/agent`、`docs/runbook` | suga |

suga 一律 `opencode-go/deepseek-v4.1-flash`、`--notify`。

## 分阶段

| 阶段 | 内容 | 退出条件 |
|---|---|---|
| P1 交互 | `gofer_ask` 注入与卡片；终端选择题修正；统一卡片组件；建会话权限档位 | 试用脚本中提问 / 选择 / 确认 / 权限请求全部以卡片作答，终端选择题不再出 JSON |
| P2 显示与入口 | 统一时间线；注入规则折叠；Sessions 历史页；新建会话默认值；一键接着聊；计数修正 | 三处显示一致；从历史找到 30 天前会话并接着聊 |
| P3 底座 | 冷启动；让锁；常驻；worker 接回与升级挂起；通知送达 ACP 会话 | 会话跨 worker 重启 / 升级存活；同目录 job 不再被空闲会话挡住；`--notify` 送达 ACP 会话 |

建议本批做 P1 + P2，P3 视进度并入或下一批（见 Q6）。每阶段单独计划；与第 4 批并行，web 卡片组件以本批为准，第 4 批 P3 决策卡直接复用。

## 安全、数据、运维与回滚

- `gofer_ask` 只能对**本 job** 开交互，凭据即 job 凭据；工具集只含这一项，不带任何写 plan / issue / 配置的能力。
- 权限档位只能收紧：入口只传档位名，server 映射时与项目 `approval` 取更严者；`SessionApproval` 仍不接受调用方直接传值。
- 让锁的风险：会话在等待期间有别的 job 改了同一目录，下一轮 agent 看到的是变化后的文件——这正是用户期望的「协作」，但要在会话里提示「等待期间目录有变化：<job 列表>」；需要独占的会话可在建会话时选「保持锁」。
- 自动接回失败一律判失败并出卡，不静默新开会话。
- 统一组件替换旧组件时一次切换、删除旧路径（G032），不并存。
- 回滚：各项均可按配置 / 版本回退；新增字段为加法迁移。

## 待确认事项

| 编号 | 问题 | 选项 | 推荐 |
|---|---|---|---|
| Q1 | ACP 会话的提问工具形态 | A：gofer 注入收窄 MCP `gofer_ask`，落为 job 交互卡；B：复用 `gofer_ask_human`（plan 决策，卡在 plan / 铃铛，不在对话里） | **A**，卡片出现在对话里 |
| Q2 | 新建会话的默认权限档位 | A：`自动`（现状）；B：`问我`；C：`严格` | **A**，档位可选；试点后再看是否改默认 |
| Q3 | 等待输入时的目录锁 | A：默认让锁，可选「保持锁」；B：默认保持，可选让锁 | **A**，空闲会话不该挡住整棵工作区 |
| Q4 | Sessions 页的定位 | A：改为三类会话合一的历史页，入口只在工作台；B：保留现有三页签，只补搜索 | **A** |
| Q5 | 新建会话默认 runner | A：带 `container` 标签的在线 worker 优先，回落 local；B：记住上次选择，首次为 local | **A**，并记住上次项目 |
| Q6 | 本批范围 | A：P1 + P2，P3 下一批；B：P1–P3 一起 | **A**，与第 4 批并行时先保交互与显示；P3 中「让锁」若计划阶段评估改动小可提前 |
| Q7 | 冷启动的处理 | A：模板优先 PATH 上的 `claude-agent-acp` + runbook 预装步骤；B：只写 runbook | **A** |

## 验收（总设计层面，阶段计划细化）

- P1：容器 `claude-acp` 会话中 `gofer_ask` 单选 / 多选 / 自由文本 / 过期各一次，卡片在对话、「等我」、手机宽度均可作答；终端会话 relay=on 与 auto 两种状态下各触发一次 AskUserQuestion，web 不出现 JSON；`问我` 档位下触发一次权限卡。反向：去掉 job 归属校验 → 跨 job 开交互用例失败；去掉档位上限取严 → 放宽用例失败。
- P2：同一 ACP 会话在工作台、job 详情、Sessions 抽屉显示一致（markdown、tool 折叠、规则折叠）；历史页搜到已结束会话并一键接着聊；各页签计数与列表一致。
- 每阶段走 gofer-verify 四路 + 真实环境验收（主机临时 server + 容器 worker）。

## 结论与人工计划 Gate

把「web 里起容器 claude ACP 会话」补成可以替代终端的第二条路：先补交互（提问 / 选择 / 权限都是卡片，卡片组件与第 4 批共用），再统一显示与入口，最后补运行底座。等用户答复 Q1–Q7 并批准后写 P1 + P2 的计划。

## 实施记录（P1 + P2）

本批按 [`../plans/2026-11-16-web-session-experience-plan.md`](../plans/2026-11-16-web-session-experience-plan.md) 实施，已交付：

- **交互**：`gofer_ask` 工具（持续会话的收窄 MCP，交互落在本 job）；交互字段 `multi` / `allow_other` / `recommended` 与选项 `description`；job 凭据对本 job 开 / 读交互（`permission` 类型拒绝）；权限档位 `permission_tier` 三档映射；终端 `AskUserQuestion` 的 `PermissionRequest` 分支；web 统一交互卡片。
- **让锁**：会话进入 `awaiting_input` 释放目录锁、下一轮前重新获取，`keep_dir_lock` 保持旧行为；等锁超时不发送并在对话里标「未发送」；下一轮带「目录变化」注入说明。
- **显示与入口**：`ConversationTimeline` 统一工作台 / job 详情消息 / 会话抽屉；ACP 流 `prompt` 拆出 `injected`、新增 `turn_unsent`；线程接口加 `agent` / `runner` 与 `counts=1`；Sessions 页改为历史页；工作台「＋新建会话」默认 ACP + `claude-acp` + `container` worker；已结束会话「接着聊」；`claude-acp` 模板 PATH 优先。

与设计 / 计划的偏离（实施时定下的，已写进代码与计划）：

| # | 设计写法 | 实际 |
|---|---|---|
| 1 | §1A「所有 ACP 会话 job 注入 `gofer_ask`」 | 只注入持续会话（`--session`）：单轮 / 批处理 job 无人值守，提问只会把它挂到超时（计划 R1）。管家、收件会话在自己的收窄面里同样注册；leader 轮次**不**注册（无人值守，计划决策有自己的 `gofer_ask_human`） |
| 2 | 过期由工具按截止时间判定并取消 | 两层：交互带 `expires_at`，**服务端到点 cancel**（job 不会一直被挂着）；工具端只留 10 秒宽限兜底，判到期即返回 `expired` |
| 3 | 档位「设置 `SessionApproval`」 | **不写** `SessionApproval`：档位存入 `JobRequest.permission_tier`，在取有效审批策略时与项目 / agent 策略取更严者（入口仍不能直接传 `session_approval`） |
| 4 | — | 新增两个 wire 字段：`dispatch.permission_tier`（需协议 **v31**，旧 worker 被拒而不是降级）与 `dispatch.keep_dir_lock`（**不抬版本**：旧 worker 忽略即默认让锁，正好是新默认） |
| 5 | §4「获取不到时这一轮排队」 | 排到等锁上限就**不发送**：会话回 `awaiting_input`，记 `job.dir_wait_timeout {unsent:true}` 并在 ACP 流回 `turn_unsent`，对话里可重发；不判失败 |
| 6 | §3「Sessions 页是历史页，入口只在工作台」 | 终端会话的回复 / 送入终端 / 接管等操作保留在**详情抽屉**；历史页只做「打开 / 详情 / 接着聊」 |

遗留（P3，下一批）：常驻会话（不按空闲结束、不计入升级排空的 in_flight）、worker 会话自动接回与升级挂起、job 完成通知送达到 ACP 会话的下一批（`job:<id>` 可投递地址）。
