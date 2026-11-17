<!-- template_id: plan; template_version: 1.1.0 -->
# 第 5 批：web 会话体验 P1 + P2 实施计划

> 状态：Draft 0.1 已批准（2026-11-16，用户：「5 批计划批准」）
> 设计：`docs/design/2026-11-16-web-session-experience-design.md`（已批准，Q1–Q7 = A；Q6 调整：等待输入让出目录锁提前到本批 P1，常驻与自动接回仍下一批）
> issue：gofer-59vu（让锁）；关联 gofer-xmit、gofer-geyu、gofer-byvi（P3，下一批）、gofer-ds7i（卡片绑定轮次，第 4 批 P3）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-16 | Claude（gofer 监督会话） | 初稿 |

思考模式：RIGOROUS。核心目标：设计 P1（交互补齐 + 让锁）与 P2（显示统一与入口）落地并通过真实验收。Scope freeze：只实施设计 §1、§2、§3、§4 中「冷启动」与「等待输入让锁」，以及下文「对设计的细化」；常驻、worker 会话自动接回、升级挂起、通知送达 ACP 会话属于 P3。Review budget：计划不另起评审轮，用户批准即开工；实施审查每条链一轮，有阻塞问题才返工一次。

## 目标与完成定义

- P1：ACP 会话里 agent 可调用 `gofer_ask` 向人提问，问题以卡片出现在 web 对话、「等我」与手机宽度下，点选后 agent 拿到答案继续；终端会话的 AskUserQuestion 不再以 JSON 摘要出现；新建会话可选权限档位（自动 / 问我 / 严格），只能收紧；持续会话等待输入时让出目录锁，下一轮开始前重新获取。
- P2：工作台对话、job 详情 Messages、Sessions 抽屉共用一个时间线组件（markdown、tool 折叠、注入规则折叠、卡片内嵌）；Sessions 页改为三类会话合一的历史页（搜索、按项目 / agent / runner / 状态筛选、计数一致）；新建会话默认 ACP + `claude-acp` + 带 `container` 标签的在线 worker；已结束 ACP 会话一键接着聊；`claude-acp` 模板优先 PATH 上的适配器。
- 单测与反向验证通过；web `vue-tsc` / `vitest` / `vite build` 通过；gofer-verify 四路通过；合入 main、关 issue。

## 范围、排除项与授权

范围：设计 §1 A–D、§2、§3、§4「冷启动」「等待输入让锁」；对应文档（gofer-usage references、`docs/reference/http-api.md`、runbook、CHANGELOG「未发布」、设计稿实施记录）。

排除：常驻（不按空闲结束）、worker 会话自动接回、升级挂起（gofer-xmit）、会话已死无法结束（gofer-geyu）、恢复后 secret 掩码（gofer-byvi）、job 完成通知送达 ACP 会话——P3 下一批；「交给它决定」按钮的动作（第 4 批 P3，本批卡片组件只留插槽，不接动作）；终端 pty 的 web 渲染；容器时区（只写 runbook）。

授权：批准后按功能点本地提交、建 worktree、派 sonnet 子 agent 与 suga job、跑主机 job、临时 server 与容器临时 worker。push 被拦时向用户申请；发版另行请示。

## 已确认事实（main 9059de35，补充设计「已确认事实」）

- **更正设计事实 7**：工作台对话读的不是 `/v1/jobs/{id}/events`，而是 `GET /v1/jobs/{id}/acp/stream`（`httpapi/server.go:1636` → `acp_stream_handler.go:20`），`prompt` 记录在 `streaming/acpstream.go:242` 原样转成文本事件，没有 `SplitInjectedRules`；拆注入应在这里做（与 `streaming/job_transcript.go:489`、`httpapi/job_messages.go:158` 同口径）。
- gofer MCP 注入：`goferMCPStdioServer(env, markers)`（`job/steward.go:39`）已是通用构造，管家（`steward.go:32`）与常驻收件会话（`mail_resident.go:29`）各带标记复用；注入发生在 `Submit`（`job/submit.go:1103-1112`）与会话恢复（`session_recovery.go:118`）。MCP 进程用 `GOFER_BIN`、job 凭据与 `EnvServerAddr`（`steward.go:44-64`）。**worker 上执行的 job 是否带得上 hub 地址与 worker 自己的 gofer 路径，需 T1 先实测**（job 凭据由 hub 签发经 dispatch 下发，`worker/dispatch.go:191`，`wsproto/frames.go:815`）。
- MCP 面收窄的先例：leader 按 `GOFER_LEADER_PLAN`（`mcpserver/server.go:1907-1909`）、管家按 `GOFER_STEWARD`（`mcpserver/steward.go`）。
- job 交互：`CreateInteraction`（`job/interaction.go:153`）把 job 置为 `pending_interaction`；`Interaction` 类型为 `question | choice | confirmation | permission`，选项 `[]InteractionOption`，**没有多选、没有推荐项字段**（`interaction.go:79-97`、`:137-148`）。HTTP `POST /v1/jobs/{id}/interactions` 的用户面为 `InHandler(deny, job.manage.others)`（`httpapi/authz_routes.go:376`），**不在 job 凭据写白名单里**（`httpapi/jobcredential.go:195-217`）。
- 终端 hook：`run.go:233-234` 的 PermissionRequest 无 AskUserQuestion 分支；选择题分支 `askUserQuestion()`（`hookrelay/question.go:51`）只挂在 PreToolUse（`command_memory.go:81-82`）；卡片按 `PermissionFingerprint(tool, input)` 标识（`question.go:84`）。
- ACP 模式：`acp.modes` 映射 gofer 模式到 agent 模式，现只有 `read_only`（`agent/templates.go:96-99`，`job/submit.go:1673` 设 `ReadOnlyModeID`，`runner/acp/runner.go:212` 调 `session/set_mode`）。审批档位 `off / ask / strict`（`config/model.go:2776-2790`），项目与 agent 取更严者（`model.go:2964`）；`SessionApproval` 只能由 server 设（`job/model.go:197-204`）。
- 目录锁：每台机器进程内一张表（`job/dirlock.go:12-46`），`execute.go:157` 获取、`:185-186` `defer releaseDir()`，整段执行期间持有；会话的回合回调 `OnTurnStart` / `OnAwaitInput` 在 `job/session_state.go:37-52`。gofer-usage 现写明「整个会话期间（含 `awaiting_input`）都持有目录锁」（`skills/gofer-usage/references/job-advanced.md`「持续会话」一节、`files-and-wakeup.md`），本批要改。
- 历史：工作台线程接口已合并三类来源（`workbench/filter.go:8-32`，`source=acp|pty|relay`），`Query` 有 `Project / Status / Q / Source / Live / Limit / Cursor`（`workbench/model.go:73-98`），**没有 agent / runner 筛选与分来源计数**；`SessionsAcpTab.vue` 也走它（:73）。
- 新建会话表单 `web/src/ui/NewSessionSheet.vue`（361 行）：默认 `pty` / `local` / 第一个 agent（:27、:36、:101-106）。

## 对设计的细化

| # | 问题 | 定案 |
|---|---|---|
| R1 | `gofer_ask` 的注入范围 | 只对 `acp-agent` 的持续会话 job（`--session`）注入（单轮 / 批处理 job 不注入：无人值守时提问会把 job 挂住等到超时），标记 `GOFER_ASK=1`；管家 / 收件会话 / leader 已有自己的 gofer MCP 时不重复注入，改为在它们的收窄面里同样注册 `gofer_ask`。agent 配置里已有同名 `gofer` MCP 时以 gofer 注入的为准（同 `applyStewardRun`）。没签发凭据时不注入（与现状一致） |
| R2 | `gofer_ask` 的工具面与契约 | 带 `GOFER_ASK` 且无其它标记时，MCP 只注册 `gofer_ask` 一个工具。输入 `{question, options?[{label, description?}], multi?, allow_other?, recommended?, timeout_sec?}`；无 options = 自由文本；`timeout_sec` 默认 1800、上限 86400。实现：`POST /v1/jobs/{本 job}/interactions`（choice / question）后轮询 `GET` 直到 answered / cancelled / 过期，返回 `{state: answered\|expired\|cancelled, answer}`；过期由工具按截止时间判定并取消该交互。工具说明写明「过期时用你的推荐项继续」 |
| R3 | 凭据路由 | `POST /v1/jobs/*/interactions` 与 `GET /v1/jobs/*/interactions` 加入 job 凭据白名单，**只允许本 job**（与 `/jobs/*/say`、`/jobs/*/wakeups` 同一「own」判定）；`type` 只允许 `question / choice / confirmation`，`permission` 拒绝 |
| R4 | 交互数据扩展 | `Interaction` / `InteractionInput` 加可选字段 `multi bool`、`allow_other bool`、`recommended string`（JSON 加法，存于现有交互记录，不改表）；多选答案为选中 label 的 JSON 数组字符串；答案校验：单选必须是某个 label 或（`allow_other` 时）非空文本 |
| R5 | worker 上的会话 | MCP 进程运行在 worker，经 hub 地址直连 hub（job 凭据由 hub 签发）。T1 先实测；若 worker 侧缺 hub 地址或 gofer 路径，由 worker 用自己 `server_link` 的地址与自身可执行文件补齐，**不另走 ws 帧**。实测不通且需要协议改动 → fail-closed 回报 |
| R6 | 终端选择题分支 | `permissionRequest()` 开头：`r.p.ToolName == AskUserQuestionTool` 时转 `askUserQuestion()` 的同一逻辑（人在只上报「等你回答：<问题>」，人不在开带 `questions` 的卡片并等待）；同一调用 PreToolUse 已开卡时按 fingerprint 复用，不开第二张。`PermissionView` 对 AskUserQuestion 的摘要改为首个问题文本（兜底） |
| R7 | 权限档位 | 提交请求加 `permission_tier: auto\|ask\|strict`（空 = auto，仅 acp-agent 有效）。server 映射：`auto` → 不改；`ask` → `SessionApproval{mode: ask}`，并把 agent 模式设为 `acp.modes.ask`；`strict` → `SessionApproval{mode: strict}` + `acp.modes.ask`。与项目 `approval` 取更严者；只能收紧。`claude-acp` 模板加 `modes.ask: default`（agent 没配 `modes.ask` 时只设 gofer 闸门，并在 job 事件里记一条说明）。CLI `job run --permission-tier`，HTTP / MCP 提交同字段；`SessionApproval` 仍拒收调用方直接传值 |
| R8 | 等待输入让锁 | 会话 job 默认在 `OnAwaitInput` 释放目录锁，`OnTurnStart` 前重新 `Acquire`（可被取消、不持 `entry.mu` 等待）；等待期间 job 保持 `awaiting_input`，记事件 `job.dir_wait`（持有者、目录），回合计时从拿到锁开始。提交可带 `keep_dir_lock: true`（CLI `--keep-dir-lock`，web「保持目录锁」勾选）保持现状。拿回锁后若等待期间有其它 job 持有过该目录，下一轮 prompt 前加一段注入说明（列出 job id 与标题，按注入规则格式，web 折叠显示）。`end` / `cancel` 在等锁时立即生效 |
| R9 | 注入规则拆分 | `streaming/acpstream.go` 的 `prompt` 事件调用 `job.SplitInjectedRules`，正文进 `text`，注入部分进新字段 `injected`（与 messages 接口同名）；R8 的等待说明同样以注入格式拼接，自然被拆出 |
| R10 | 历史接口 | 工作台线程接口加 `agent`、`runner` 筛选与 `counts=1`（按 `source` 返回 `{acp, pty, relay, all}` 计数，与列表同一可见性过滤）；`Q` 搜索覆盖标题、主题、最后消息（核实现状，缺的补上）。不新增路由 |
| R11 | 统一卡片 | `InteractionCard.vue` 扩展：多选、「其他」自由文本、推荐项标注、过期 / 已答只读态；保留 `isDecision` 投影与一个空的「交给它决定」插槽（第 4 批 P3 接动作）。`SessionPermissionPrompt.vue` 的选择题与权限两种卡改为把会话权限卡数据适配成 `Interaction` 交给 `InteractionCard` 渲染，作答仍走原会话权限接口；适配后删除旧渲染代码（G032） |
| R12 | 统一时间线 | 新组件 `ConversationTimeline.vue`（输入：消息、tool 调用组、交互、注入），工作台 `ConversationView`、job 详情 Messages、`SessionDrawer` 都改用它；人与 agent 消息都走 `MarkdownBlock`；注入显示为一行「gofer 规则 · 展开」。旧的三处渲染代码删除 |
| R13 | 入口与新建 | Sessions 页改为历史列表（R10 接口），默认含已结束，点行进工作台线程；工作台侧栏顶部「＋新建会话」。`NewSessionSheet` 默认 `acp`、`claude-acp`（项目准入里有时）、带 `container` 标签的在线且支持该 agent 的 worker（无则 local），记住上次项目；新增「权限」（R7）与「保持目录锁」（R8）两项，其余收进「更多」 |
| R14 | 接着聊 | 已结束 ACP 会话（工作台线程、历史行、job 详情）的「接着聊」调用现有续接（新开持续会话、`session/load` 后停在等待输入，S 批 S2 语义）；T7 开工先核实现有续接接口与入口，job 详情旧「Follow-up request」表单对 ACP 会话改为与工作台一致的输入框 |
| R15 | 冷启动 | `claude-acp` 模板：PATH 上有 `claude-agent-acp` 时 `Command: claude-agent-acp`、无参数，否则保持 `npx -y …`；注入判定与 G044「内置」标记照旧。runbook 写容器预装命令与 `TZ` 设置 |

## 分工

按用户要求，sonnet 只做最核心：`gofer_ask` 的注入、MCP 面收窄与 job 凭据路由（R1–R5，鉴权 / 协议）、终端 hook 的 AskUserQuestion 分支（R6，hook 并发与卡片去重）、权限档位映射（R7，鉴权）、等待输入让锁（R8，并发）。其余（R9–R15 与文档）派 suga，模型 `opencode-go/deepseek-v4.1-flash`，一律 `--notify`。

## 前置与 fail-closed 条件

- T0（Claude）：主机建 worktree `.worktrees/b5`（`feat/b5`，集成）、`.worktrees/b5-a`（`feat/b5-a`）、`.worktrees/b5-b`（`feat/b5-b`）、`.worktrees/b5-c`（`feat/b5-c`），基于当时的 main，相对 gitdir，`web/node_modules` 链接主 checkout；先提交 tracker。
- Go 命令：`GOWORK=off`，`GOCACHE` 在 `tmp/_cache/<名>`，`TMPDIR=/tmp/gofer-gotmp/<名>`。
- fail-closed（出现即停，回报）：基线相关包测试在未改动时失败；worker 上 `gofer_ask` 需要新增协议帧（R5）；权限档位出现任何「放宽」路径；让锁需要改动锁表的冲突语义（祖先 / 后代重叠规则）；现有会话 / 审批 / 交互用例需要改断言（说明行为变了，除 R6、R8 明确改变的行为外）。

## 与第 4 批并行的共享文件

第 4 批（自主推进 P1 + P2）同期在 `.worktrees/b4*` 实施，它新增 MCP 工具 `gofer_submit_result`（完整面与 leader 面）、`POST /v1/jobs/{id}/result`（job 凭据白名单）、`GET /v1/jobs/{id}/diagnosis`、`/v1/plans/{id}/driver`，web job 详情显示自报终态与「诊断」按钮。共享点与约定：

| 文件 / 区域 | 第 4 批 | 第 5 批 | 约定 |
|---|---|---|---|
| `mcptools/tools.go`、`mcpserver/server.go` | `gofer_submit_result` | `gofer_ask` 与 `GOFER_ASK` 收窄面 | 各自追加，不重排；收窄面判断各写一个函数 |
| `httpapi/jobcredential.go` 白名单 | `/jobs/*/result` | `/jobs/*/interactions`（POST / GET） | 各加一行，own 判定共用现有函数 |
| `httpapi/authz_routes.go` | 新路由 | 无新路由 | — |
| `job/execute.go` | T1 把推进调用改走事件入口 | R8 只改 `releaseDir` 的持有方式 | b5-b 开工前读第 4 批 T1 的改动范围；后合并方 rebase 后跑 `job` 包全量 |
| `job/submit.go` | — | R1 注入、R7 档位 | — |
| `internal/i18n` | 新文案 | 新文案 | 各自追加键 |
| web `JobDetail.vue` | 自报终态、「诊断」按钮（信息区） | Messages 页签改用时间线、ACP 续接输入框 | 第 4 批不动 Messages 与续接区；第 5 批不动信息区 |
| web `InteractionCard.vue` | 第 4 批 P3 才用 | R11 扩展 | 本批定稿，第 4 批 P3 复用 |
| `jobstore` | 新表与列 | 无 schema 改动 | — |

后合并的一方在合并前 rebase 到对方已合入的 main，并跑两侧用例。

## 波次与依赖

```
W0  T0 前置（Claude）
W1  [b5-a] T1 gofer_ask 注入、MCP 收窄与凭据路由 → T3 权限档位（sonnet A，串行）
    ‖ [b5-b] T2 终端 AskUserQuestion 分支 → T4 等待输入让锁（sonnet B，串行）
    ‖ [b5-c] T5 服务端：注入拆分、历史接口、模板冷启动（suga）
W2  审查三条链；合并 → b5（Claude，--no-ff）；b5 上相关包测试
W3  [b5] T6 web：统一卡片与统一时间线（suga）→ T7 web：历史页、新建会话、接着聊、job 详情输入框（suga）
W4  [b5] T8 文档（suga）
W5  T9 真实验收、反向验证、gofer-verify、合并收尾（Claude）
```

共享文件负责方：`job/interaction.go` 的字段扩展（R4）归 T1；`claude-acp` 模板的 `modes.ask`（R7）归 T3、`Command` 选择（R15）归 T5，同一文件分两处，T5 合并时以 T3 为准 rebase；`httpapi/jobcredential.go` 归 T1。

## 任务

| 任务 | 执行方 | 内容 | 验收 |
|---|---|---|---|
| T0 | Claude | worktree、tracker 提交 | — |
| T1 | sonnet A | R1–R5 | 本 job 凭据可开交互、他 job 被拒、`permission` 类型被拒；`GOFER_ASK` 面只有 `gofer_ask`；管家 / 收件 / leader 面也有 `gofer_ask`；单选、多选、其他、自由文本、过期、取消各一条用例；worker 路径实测结论写入任务回报 |
| T2 | sonnet B | R6 | PermissionRequest 的 AskUserQuestion：人在只上报、人不在开卡并回填答案；与 PreToolUse 同一调用只有一张卡；摘要不含 JSON |
| T3 | sonnet A | R7 | 三档映射；项目为 `strict` 时请求 `ask` 仍为 `strict`；调用方直接传 `session_approval` 仍被拒；agent 无 `modes.ask` 时只设闸门并记事件；CLI / HTTP / MCP 三入口 |
| T4 | sonnet B | R8 | 会话等待输入时同目录另一个可写 job 立即开始；下一轮在其结束后才开始，期间状态 `awaiting_input` 并有 `job.dir_wait`；`keep_dir_lock` 时保持现状；等锁时 `end` / `cancel` 立即生效；下一轮带等待说明；现有锁用例不改断言 |
| T5 | suga | R9、R10、R15 | ACP 流的 `prompt` 事件拆出 `injected`；线程接口 agent / runner 筛选与分来源计数与列表一致（表驱动）；模板 PATH 优先与回落（假 PATH 用例）；G056 无新路由 |
| T6 | suga | R11、R12 | `vue-tsc`、`vitest`、`vite build` 通过（主机跑）；旧渲染代码删除（`git diff --stat` 删除行核对）；三处入口快照 / 组件测试 |
| T7 | suga | R13、R14 | 同上；计数在任意筛选下与列表一致；新建会话默认值用例；接着聊调用现有续接接口 |
| T8 | suga | 文档：gofer-usage references（job-advanced 持续会话一节的锁与权限档位、files-and-wakeup、MCP 工具列表加 `gofer_ask`）、`docs/reference/http-api.md`（交互字段、线程接口参数、提交字段、ACP 流 `injected`）、runbook（容器预装适配器、`TZ`）、CHANGELOG「未发布」、设计稿实施记录 | 删除行审查、业务名检查（G031 / G046） |
| T9 | Claude | 真实验收、反向验证、`git diff --stat main...feat/b5`、私有名检查、gofer-verify、主机合并、合并确认后清理、关 issue、遗留另立 issue | — |

## 真实验收（T9）

主机临时 server（独立配置目录与端口）+ 容器里一个临时 worker（独立 worker 配置，经本地转发连临时 server，标签含 `container`），浏览器用无头 chromium 截图核对：

1. 新建会话（web 表单）：默认即 ACP + `claude-acp` + 容器临时 worker；容器预装适配器后首轮在 30 秒内就绪。
2. 让会话里的 claude 调用 `gofer_ask`（单选带推荐项一次、多选一次）：卡片出现在工作台对话与「等我」，手机宽度可点选；作答后 agent 回复中引用了所选项。
3. 终端会话：容器里在 tmux 起一个注册到临时 server 的 claude 终端会话（临时目录、hook 指向临时 server，不在 gofer job 内），relay 设为 on，让它调用 AskUserQuestion：web 显示为选项卡，点选后终端里 agent 拿到答案；会话「最后一条消息」不含 JSON。
4. 让锁：会话停在等待输入时，向同一工作区提交一个可写 agent job（同一 worker）：立即开始、不排队；随后给会话发消息，下一轮在该 job 结束后开始，并带「等待期间目录有变化」说明（折叠显示）。
5. 注入规则：会话首条消息在工作台、job 详情、手机宽度下都只显示正文，注入为一行可展开。
6. 权限档位「问我」：新建会话后让 agent 写文件，出权限卡；作答后继续。
7. 历史页：结束会话后在 Sessions 历史页搜到它、各计数一致，「接着聊」后会话停在等待输入并保留上下文。

## 反向验证

去掉 job 凭据的 own 判定 → 跨 job 开交互用例失败；档位映射改为不取更严者 → 「项目 strict 请求 ask」用例失败；去掉等待输入释放锁 → 「同目录 job 立即开始」用例失败；去掉 PermissionRequest 的 AskUserQuestion 分支 → 「摘要不含 JSON / 只一张卡」用例失败；去掉 ACP 流的注入拆分 → `injected` 用例失败。破坏须能编译（筛 `[build failed]`）。

## 风险与回退

- 让锁后同一目录并发修改：默认让锁是用户已定的取舍；下一轮带等待说明，需要独占的会话用「保持目录锁」。等锁不得持 `entry.mu`、必须可取消，T4 以 race 跑 `job` 包全量。
- `gofer_ask` 阻塞时长：工具受 `timeout_sec` 与 agent 自身工具调用超时双重约束；过期返回让 agent 按推荐项继续，不会永久阻塞。交互期间 job 为 `pending_interaction`，回合计时不计入（现有规则）。
- 统一组件替换三处渲染：一次切换、删除旧代码；T6 以三处入口的组件测试兜底，真实验收 2、5 覆盖。
- 与第 4 批冲突：见「共享文件」表；后合并方 rebase 并跑两侧用例。
- 回退：交互新字段为 JSON 加法，旧版本忽略；`permission_tier` 缺省即现状；让锁可用 `keep_dir_lock` 逐会话关闭；无 schema 改动。
