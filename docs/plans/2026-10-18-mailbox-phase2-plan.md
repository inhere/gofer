<!-- template_id: plan; template_version: 1.2.0 -->
# gofer 信箱（第二期） 实施计划

> 状态：Draft 0.1 / 待用户批准
> issue：gofer-e6sb

- 思考模式：rigorous。
- 核心目标：落地设计 `docs/design/2026-10-16-session-topics-mailbox-and-inbox-design.md`（Draft 0.7，已批准）的第二期——信箱：发件出处、持久保存、地址解析、投递（hook 注入、唤醒、Stop 送达、server 侧送达）、离线补送与期限、回复串、web 信箱页；收集箱「交给会话」改为发信件。
- scope freeze：第三期（`agent:` 地址、主机 agent 常驻会话）、工作区代理（gofer-9dbh）不在本计划；实施中发现的新需求另开 issue。
- review budget：本计划最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户具名批准本计划，或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-18 | Claude（监督会话） | 初稿：M1 存储、M2 发信与地址解析、M3 路由 / CLI / MCP、M4 hook 送达、M5 server 投递器、M6 收集箱改发信、M7 web、M8 秘密扫描与保存期、D1 文档、V 验收 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 目标与完成定义

1. 会话、调用方、job 可以向 `session:` / `job:` / `topic:` / `path:` / `caller` 地址发信；出处按设计如实标注（session / caller / job），信件与唤醒提示一律带 `<gofer-mail …>` / `<gofer-mail-wake …/>` 标签、计入 harness 输入，不走 web 消息通道。
2. 地址唯一命中才投递；歧义、不可投递（pty job）、超过送达期限（默认 24 小时）、对话串超过自动往返上限（默认 20 封）的信件转「待指派」进收集箱，列出候选。
3. 送达：普通件在收件会话下一个 SessionStart / UserPromptSubmit 注入；急件轮内经 PreToolUse 注入；空闲会话（idle / offline 且 `last_seen_at` 早于 5 分钟）经唤醒通道触发一轮（每会话 10 分钟最多 1 次）；用户离开且已有待信时 Stop 阻塞送出；suga 会话经 `suga send`；ACP 会话 job 在 `awaiting_input` 时经 `job say`。
4. 投递语义：CAS 占用（`pending → delivering`，租期 60 秒）后才送；hook 路径先确认再注入（至多一次）；server 侧路径至少一次（标签带信件 id）。
5. 回复挂在同一对话串（`gofer session mail reply` / MCP 同名工具），送回发件方；web 信箱页显示全部往来，可回复、转交、插话。
6. 收集箱「交给会话」改为生成信件，条目状态随回复回写。
7. 限额：单封 32 KB；单次注入最多 5 封、合计 8000 字；发件限速每发件方每分钟 10 封、每小时 100 封（429）；保存期 90 天。
8. 权限按设计「路由与权限」表实施（G056）；调用方上限（v0.172）同样约束信箱：受限调用方只能给范围内项目的会话 / job 发信、读信。
9. 秘密扫描与脱敏覆盖信件；收集箱条目、work journal、Today 卡片只存信件引用与摘要。

完成定义：上述有测试覆盖并通过；Linux 全量 `-race`、Windows 全量、darwin vet、web 三命令通过；文档同步（http-api、skill references、runbook、CHANGELOG）；发版 CI 全绿。

## 范围、排除项与授权

范围：设计第二期全部内容（见「分期」表第二期一行与「信箱」一节）。

排除项：第三期 `agent:` 地址与主机 agent 收件；工作区代理；「用户本人」级别出处（设计「待确认事项」第三条，暂不做）；standalone MCP 的信箱工具（返回 `errNeedsServer`）。

授权边界：

- 计划批准不授权实施；实施需要用户另发执行请求（可与批准同一句话）。
- 执行请求覆盖：sonnet 子 agent（主 checkout，同包串行）与 suga（`--model opencode-go/deepseek-v4.1-flash`，主机 worktree，边界清楚的任务）实施并本地提交；监督者审阅、容器 `-race` 复跑、合并。
- 发版沿用已有长期授权（`gofer-release-flow`）；可按完成度分两个版本发（v0.173：M1–M4；v0.174：M5–M8），每次发版前全量验证。
- 不改用户的主机 / 容器配置；`mail.*` 配置默认值写在代码里，用户按需调整。
- `host_or_non_offline_action=REQUIRED`（发版、唤醒的主机实测）。

## 输入与批准证据

- 设计：Draft 0.7；0.5 由用户 2026-10-16 批准（原话「批准设计；出实施计划」），0.6 / 0.7 的修订经用户 2026-10-16 确认（「旧位置继续兼容 - 可以无需兼容；批准计划」）。第一期已随 v0.166.0 发布，实施记录见设计稿。
- 前置：权限 T2.2（会话头单一注入点）已完成并发布；`/clear` 后会话标识实测结论：CLI 只按环境变量识别当前会话（gofer-e6sb 评论，2026-10-16）。
- 关联：gofer-e6sb；gofer-9dbh（被本 issue 阻塞）；gofer-6pw0 / gofer-7798（调用方上限）；gofer-7lac（Stop 等待放行规则，已发布）。
- 仓库规则：AGENTS.md G021 / G022、G024、G032、G033、G041、G045 / G046、G051–G054、G056。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 把一段文字送进一个会话的轮次 | `internal/sessionrelay`（deliver.go 的投递阶梯：tmux / deliver_command / 传话人；message.go；notify_job.go 的 job 完成补送）、`internal/hookrelay`（`catchUp`、additionalContext 合并） | 部分 | 唤醒提示复用投递阶梯；正文经 hook 注入，扩展 `catchUp` | OWNER_EXTENSION | 现有通道送的是 web 消息（`Say`、带 web 前缀、写 outbox），语义与信件不同 | 两套注入合并 → 只在 `catchUp` 一处合并 job 通知、记忆与信件 |
| CAP-02 | 按主题 / 路径找会话 | `sessionrelay.Find`、`Suggest`（第一期） | 是 | — | DIRECT_REUSE | — | — |
| CAP-03 | 待指派进收集箱 | `internal/work/capture.go`（`captured` 条目）、dispatch.go | 部分 | 待指派信件生成 `captured` 条目并互相引用 | OWNER_EXTENSION | — | — |
| CAP-04 | 信件存储与状态机 | `messages` / `agent_presence` 表（第一期已停读，保留待删）、session watches | 否 | 新表 `mail` | MINIMAL_NEW_MODULE | 旧 messages 表的身份与会话无关、语义不同（设计「已确认事实 / presence」） | 与旧表并存 → 旧表不读不写，后续版本删表 |
| CAP-05 | 信件业务逻辑（地址解析、出处、限额、投递器） | sessionrelay（中继与会话）、work（收集箱） | 否 | 新包 `internal/mail`（业务层，依赖 jobstore、sessionrelay 的查找；被 httpapi、hookrelay 经 client 调用） | MINIMAL_NEW_MODULE | 放进 sessionrelay 会让中继服务同时承担信箱的状态机与投递器，职责混杂 | 与 sessionrelay 重复投递 → 投递只调用 sessionrelay 的阶梯，不复制 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NM-01 | CAP-04 | `mail` 表（jobstore） | messages、session watches | 旧表语义不同 | watches 只记 job id，无正文 / 串 / 状态机 | 见 CAP-04 | jobstore 唯一所有；保存期由 M8 清理 | 旧 messages 表随后续版本删除 |
| NM-02 | CAP-05 | `internal/mail` | sessionrelay、work | 无现成信件逻辑 | 并入 sessionrelay 职责混杂 | 见 CAP-05 | `mail.Service` 由 core 组装、serve 启停投递器（G051 可 Drain） | 第三期 `agent:` 地址在此扩展 |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 信件走 web 消息（`Say`） | CAP-01 | 删掉它后信件仍能经 hook 注入送达；而走 `Say` 会被当成用户输入、关闭 relay、改标题（设计决策 6 明确排除） |
| 复用旧 presence messages 表 | CAP-04 | 身份与会话无关、只能靠轮询；第一期已退役 |

## 前置检查与 fail-closed 条件

workspace baseline：`inhere-tools/gofer`，branch `main`，HEAD `2eed1dcd` 之后（v0.172.0 已发布）。

fail-closed：

- 信件与唤醒提示的标签必须纳入 `IsHarnessPrompt`（M4 首先落地并测试），否则不得开放发信路由。
- hook 注入必须「先确认、再注入」；确认失败不注入。
- 投递器在测试中必须可 Drain（G051）。
- 每条新路由按 G056 声明并 `go generate`；调用方上限的 scope / rw 声明齐全（S4a 覆盖测试）。

## 波次与依赖

| 波次 | 任务 | 依赖与并行 |
|---|---|---|
| 一 | M1 存储与状态机 | — |
| 二 | M2 发信与地址解析；M4a harness 标签 | M2 依赖 M1；M4a 独立（hookrelay），可并行 |
| 三 | M3 路由 / CLI / MCP | M2 |
| 四 | M4b hook 送达（含 Stop 送达）；M6 收集箱改发信 | M3；可并行（hookrelay vs work） |
| — | 发 v0.173（M1–M4、M6） | 全量验证 |
| 五 | M5 server 投递器（唤醒、suga、ACP、过期、补送） | M4b |
| 六 | M7 web 信箱页与 Today 卡；M8 秘密扫描、脱敏与保存期 | M3；可并行（web vs jobstore/httpapi） |
| 七 | D1 文档与 runbook；V 验收与发版 v0.174 | 全部 |

## 任务

通用要求（每个任务）：只提交自己的文件（pathspec）；不还原、不丢弃别人的改动；不碰 `.gofer/tracker/*`；不删注释、不重排无关代码；用户可见变化同步 skill references 与 http-api（G045 / G046）；Go 验证 `GOTOOLCHAIN=go1.26.9 GOWORK=off`，测试前 `env -u CLAUDE_CODE_SESSION_ID -u GOFER_SESSION_ID`；G051 / G052。

### M1 存储与状态机

- 文件：`internal/jobstore/mail.go`（新）、`store.go`（建表与迁移）、测试。
- 内容：表 `mail`（设计「信箱」列出的全部列）；状态 `pending / delivering / delivered / answered / held / expired / failed / cancelled`；方法：`InsertMail`、`ClaimMail`（CAS `pending → delivering` + 租期）、`ConfirmMail`、`ReleaseExpiredClaims`、`MarkRead`、`ListMail`（按收件 / 发件 / 串 / 状态过滤）、`PendingCount(sessionID)`、`ExpireOverdue`（→ `held` 或 `expired`）、`PruneMail`（保存期）。
- 验证：`go test -race -count=1 ./internal/jobstore/...`。
- 完成标准：状态机每条转移与非法转移用例；并发 claim 只有一个成功；租期到期回到 `pending`。

### M2 发信与地址解析（`internal/mail`）

- 文件：`internal/mail/{service,address,origin,limits}.go`（新）、`internal/core`（组装）。
- 内容：地址解析（`session:` / `job:` / `topic:` / `path:` / `caller`；复用 `sessionrelay.Find`；唯一命中才投递，歧义转 `held` 并记候选，候选按读者的 `session.read.others` 过滤由调用方传入的判定函数完成）；出处（session 头 → 核对会话存在、未结束、同一调用方；user 无会话 → caller；job 凭据 → job）；限额（单封 32 KB、限速、对话串自动往返上限）；`held` 时生成 `captured` 收集箱条目并互相引用；回复（同串、送回发件方地址）。
- 验证：`go test -race -count=1 ./internal/mail/... ./internal/core/...`。
- 完成标准：每种地址、歧义、离线会话参与解析、各限额与 429、出处判定、回复串用例。

### M3 路由、CLI、MCP

- 文件：`internal/httpapi/mail_handler.go`（新）、`authz_routes.go`（routeDecls：权限按设计表，scope / rw 维度；受限调用方按收件 / 发件会话或 job 的项目判定）、`internal/client/mail.go`、`internal/commands/session.go`（`gofer session mail send|ls|read|reply|cancel`，G033）、`internal/mcpserver`（同名工具；standalone 返回 `errNeedsServer`）。
- 路由：`POST /v1/mail`、`GET /v1/mail`、`GET /v1/mail/{id}`、`POST /v1/mail/{id}/reply|ack|cancel|readdress`。
- 验证：`go test -race -count=1 ./internal/httpapi/... ./internal/client/... ./internal/commands/... ./internal/mcpserver/...`；`go generate ./internal/httpapi/...`。
- 完成标准：设计权限表每格一条用例（含 job 凭据只能发给本 job 来源会话 / plan 主会话 / caller）；受限调用方范围外 404；CLI 输出用例。

### M4a harness 标签

- 文件：`internal/hookrelay/run.go`（`IsHarnessPrompt` 纳入 `<gofer-mail` 与 `<gofer-mail-wake`）、测试。
- 完成标准：带标签的输入不关闭 relay、不改标题、不触发自动草稿。

### M4b hook 送达

- 文件：`internal/hookrelay/run.go`（SessionStart / UserPromptSubmit / PreToolUse 取件：先 claim 再确认再注入；与 job 通知、记忆合并进 `additionalContext`；单次 5 封 / 8000 字，超出只注入标题行并提示 `gofer session mail read <id>`）、Stop（心跳回应带 `mail_pending`；只在已有待信且按 relay 规则判定离开时阻塞送出，独立于 `stop_job_wait`；server 原子关闭本轮 turn 并占用信件，与 `CompleteSessionWatchTurn` 同形）、`internal/httpapi/session_handler.go`（取件与原子完成接口）、`internal/sessionrelay`（`mail_pending` 计数）。
- 验证：`go test -race -count=1 ./internal/hookrelay/... ./internal/sessionrelay/... ./internal/httpapi/...`。
- 完成标准：至多一次（确认失败不注入）；合并与限长；急件 PreToolUse；Stop 阻塞条件的真值表；与 web 回复竞争时只有一方成功。

### M5 server 投递器

- 文件：`internal/mail/deliverer.go`（新）、`internal/serve`（启停）。
- 内容：周期检查（唤醒阈值、每会话唤醒频率、合并）；唤醒提示经 sessionrelay 投递阶梯 / `codex queue`（不走 `Say`、不写 outbox、不加 web 前缀、对唤醒提示的回复丢弃）；suga 会话经 `suga send` 送正文（返回 3 = 不在线）；ACP 会话 job 在 `awaiting_input` 时 `job say`（一次一封）；pty job 转 `held`；送达期限到期转 `held`（或 `expired`）；同一会话恢复（重新登记 / 心跳）时补送。
- 验证：`go test -race -count=1 ./internal/mail/... ./internal/serve/... ./internal/sessionrelay/...`；主机实测（监督者）：Claude Code 空闲会话被唤醒并读到信件。
- 完成标准：各收件方类型的送达用例；唤醒频率与合并；至少一次的重复识别；可 Drain。

### M6 收集箱「交给会话」改发信

- 文件：`internal/work/dispatch.go`、`internal/httpapi/work_dispatch_handler.go`、web 分派对话框文案（如需）。
- 内容：分派到会话 = 发一封信（出处为分派者，`capture_item_id` 关联）；条目状态随信件 `delivered` / `answered` 回写；回复内容只存引用与摘要。
- 完成标准：分派、送达、回复各一条回写用例；旧的 web 消息分派路径删除（G032）。

### M7 web 信箱页与 Today 卡

- 文件：`web/src/views/Mail.vue`（新）、路由与导航、`web/src/utils/mail.ts`、SessionDrawer 的信件入口、Today 的「信件待指派」卡（与 `internal/today` 的卡片类型）。
- 内容：按对话串显示往来（信件与 web 消息、管家带话并排）；回复、转交（readdress）、插话；待指派信件可改投 / 新建 job / 丢弃。
- 验证：web 三命令；`go test` 覆盖 today 卡片。

### M8 秘密扫描、脱敏与保存期

- 文件：`internal/job` / `internal/httpapi` 的 secret-scan 报告新增 mail 段（非 admin 按 `from_caller` / `to_caller` 过滤）；按信件 id 的脱敏（同步改写收集箱 / journal / Today 摘要）；随 job 脱敏一并处理带 `job_id` 的信件；保存期清理（90 天）接入现有 prune。
- 完成标准：扫描、脱敏、摘要改写、清理用例。

### D1 文档与 runbook

- `docs/reference/http-api.md`（mail 路由与字段）、`docs/reference/permissions.md`（generate）、`skills/gofer-usage/references/sessions-relay.md`（信箱：地址、送达、回复、限额；通用写法）、`skills/gofer-usage/SKILL.md` 入口一行（如有必要）、`docs/runbook/`（信箱运维：查待指派、清理、回滚——新表被旧版本忽略，回滚前把待送信件转待指派）、CHANGELOG、设计稿第二期实施记录。

### V 验收与发版

- 全量：Linux `-race`、Windows 全量、darwin vet、web 三命令。
- 主机实测（监督者）：会话之间按主题传话往返一次；空闲会话唤醒；Stop 送达；收集箱分派到会话后收到回复。
- 发版：按 `gofer-release-flow`。

## 回滚与恢复

- 每个任务一个或两个提交；M4b / M5 回退不影响 M1–M3（信件仍可经 `mail ls/read` 读取）。
- 发版后回滚：旧版本忽略 `mail` 表；回滚前运行一次「把 pending 信件转待指派」（D1 runbook 给出命令）。

## 人工 Gate

- **计划批准与执行请求**：用户具名批准本计划并要求开始实施。
- **外部动作**（已有长期授权）：tag、push、CI、mirror、一键更新与 worker 升级。
- **主机实测**：唤醒与 Stop 送达需要真实 Claude Code / codex 会话，由监督者在主机进行，结果记进 issue。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| 信件表与状态机、投递语义 | M1、M4b、M5 | 状态机与并发用例 |
| 地址写法与歧义不猜（决策 5） | M2 | 地址用例 |
| 出处与信任级别、信件不是用户输入（决策 6） | M2、M4a | 出处与 harness 标签用例 |
| 送达规则表、唤醒通道（决策 4） | M4b、M5 | 送达用例 + 主机实测 |
| 路由与权限表、读信沿用 outbox 口径（决策 7） | M3 | 权限矩阵 |
| 收集箱分派改发信件 | M6 | 回写用例 |
| web 信箱页、Today 汇总卡 | M7 | web 三命令 |
| 敏感信息、保存期 | M8 | 扫描与清理用例 |
| 默认数值（决策 10） | M1–M5 | 各限额用例 |
| 调用方上限对信箱的约束（v0.172 起） | M3 | 受限调用方用例 |

## 完成 Gate 与剩余工作

- 完成 Gate：完成定义全部满足；V 通过；设计稿第二期实施记录写入。
- 剩余工作：第三期（`agent:` 地址与主机 agent 收件，需与 suga 维护会话确认身份与收件方式）；工作区代理 gofer-9dbh。
