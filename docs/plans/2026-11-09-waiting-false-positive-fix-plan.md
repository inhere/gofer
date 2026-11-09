<!-- template_id: plan; template_version: 1.3.0 -->
# 「等我」误报修正（RV1 F1–F4）实施计划

> 状态：Draft 0.1 / 已批准（2026-11-09，用户：「F3 选 A，三份计划批准」）
> issue：gofer-cnxy

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-09 | Claude | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：「等我」里的会话 relay 回合只在 agent 明确要人回复、会话不在监督中、且打开满 5 分钟时出现，一周误报率降到 10% 以下。Scope freeze：只实施设计 0.3 §9（F1–F4 与 digest 改名）和下文“对设计的细化”。Review budget：一轮合并轴评审加一轮确认，有阻塞项才继续。停止条件：确认轮无 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

目标：监督中的会话、汇报型回合、刚打开不足 5 分钟的回合都不进「等我」；工作台把它们显示为「监督中」或「空闲」；回合关闭时间可查，供 RV2 复盘。

完成定义：T1–T11 通过；设计 §9 的每条决策都有测试或真实验证证据；反向验证全部确认；`gofer-verify` 全绿；文档按 G045 同步；设计稿补实施记录。

## 范围、排除项与授权

范围：`plan_decisions` 两列（`needs_reply`、`released_at`）；Stop hook 解析 `[needs-reply]` 标记并上报；SessionStart 提示一行；`sessionrelay.Supervision`；today 的 relay 卡过滤与 `recheck_at`；工作台线程投影；web 显示与到点重拉；digest 文案改名；对应文档与 skill。

排除：「会话等待回复」推送的口径（设计 §9.4，RV2 再定）；relay 的 Stop 停留规则（`waitDecisionGated` 行为零变化）；问句启发式；宽限期配置项；会话 `state` 的语义；`sessionrelay/service.go`、`today/cards.go` 已有的规模问题（新增逻辑放新文件，不搬旧代码，GR104）。

授权边界：批准本计划不等于授权执行，执行需要单独的当前请求。本地提交按既有默认授权；push、打 tag、发版不在本计划内（用户要求攒一批再发版）。**G057：下次发版后补 `internal/jobstore/testdata/schema-v<版本>.sql` 快照**，属于发版流程；G058 能力清单同样随发版更新。真实验证只用临时 server 和临时目录，不改用户配置和线上 server。

- `host_or_non_offline_action=REQUIRED`

需要的主机动作：`gofer-verify` 工作流；T10 在容器内起临时 server 与一次性 Claude 进程（`--model haiku`，少量 token）。脚本和记录写工作区 `tmp/rv1-fix/`。

## 输入与批准证据

- 设计：`docs/design/2026-11-01-attention-surfaces-review-design.md` 0.3 §9。用户 2026-11-09：「等我按建议」。
- 复盘报告：工作区 `tmp/rv1/report.md`（不入库）。
- 规范：G021–G024、G031、G032、G045、G046、G051–G053、G056、G057、SR1211、SR1403、SR1407、SR1433、GR104、GR109。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 监督事实 | `supervisingDetail`、`SubagentCount`、`CountActiveJobsByCaller`、`ListSessionJobWatches` | 是 | sessionrelay 内抽出 `Supervision`，`supervisingDetail` 改为调用 | DIRECT_REUSE | none | 无新模块；today / workbench 经接口取，不重复判据 |
| CAP-02 | 回合标记与关闭时间 | `migratePlanDecisions`、`OpenTurn`、`ReleaseDecision`、`lastMessage()`、`TopicReminder` 通道 | 是 | 原位扩展 | DIRECT_REUSE | none | 两列 additive schema，退回旧版无害 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| `gofer session needs-reply` 命令 / MCP 工具 | CAP-02 | 多一次工具调用且要把标记挂到下一个回合，有竞态；标记随回合到达即可（设计 §9.4） |

## 对设计的细化

代码核对后按最小方案定下，批准本计划即确认。都不改变设计的用户结果。

| # | 问题 | 决定 |
|---|---|---|
| R1 | `Supervision` 的类型放哪 | `sessionrelay.Supervision{Jobs, Subagents, Watches int}` 加 `Any() bool`。today、workbench 现已 import sessionrelay（方向不变）；today 在 `Deps` 加 `Supervisor interface{ Supervision(jobstore.AgentSession) sessionrelay.Supervision }`，workbench 对已有 `relay` 做同签名接口断言 |
| R2 | `Supervision` 查询失败 | 该项计 0 并 `slog.Warn`（GR109），与 `supervisingDetail` 现有口径一致；Watches 查询失败同样处理 |
| R3 | 多个开放回合 | `OpenTurn` 已保证一个会话只有一个开放回合；`relay()` 仍按现有分组，只保留满足四条件的回合，全不满足则不出卡 |
| R4 | `recheck_at` 的范围 | 只算「其余条件都满足、仅被宽限挡住」的回合，取 `asked_at + 300` 的最小值；被监督或无标记挡住的回合不参与（它们的变化有 sessions / jobs 事件） |
| R5 | 标记解析的边界 | 取最后一个非空行，`strings.TrimSpace` 后与 `[needs-reply]` 全等；大小写敏感；正文中间出现不算；剥掉该行后正文为空时用现有的「无消息」文案 |
| R6 | 截断与标记 | 先在完整消息上判断并剥掉标记，再按 `MaxMessage` 截断（现有 `lastMessage()` 先截断，会把末行截掉） |
| R7 | 工作台线程状态 | 开放回合中有 `needs_reply` 且不在监督 → 现状（blocked + attention）；在监督 → `StatusWorking` 加 `supervising` 字段，不出 attention；否则 → `StatusIdle`。`state=waiting_reply` 但无开放回合的旧分支保持现状 |

## 前置检查与 fail-closed 条件

workspace baseline：

- Git root：`inhere-tools/gofer`，分支 `main`，基线为提交本计划与设计 0.3 的 commit。同一 checkout 有其他会话的未提交改动（`internal/project`、其他设计稿），提交时只暂存本任务文件（SR1211）。
- 实施在 `.worktrees/rv1-fix`（分支 `fix/rv1-waiting`，主机建），合并用 `--no-ff`，合并后删 worktree 与分支。开 worktree 前先提交 tracker。
- expected paths 见各任务；实施发现新路径按四类记录。

fail-closed，以下任一情况停止并回到设计：

- T10 发现 Claude Code 的 Stop 输入里 `last_assistant_message` 不保留末行原文（被改写、去掉方括号或截断），且 transcript 尾部同样读不到。
- 实施需要改变 Stop 的停留行为或会话 `state` 才能达到验收。
- 计数与列表无法同源（需要第二条判定路径）。

派活约束（SR1433）：任务书写明 G021–G024、G031、G057，idevstd 加载方式，SR1211 只暂存自己的文件；不做无关重排、不删注释；不在 export 了 `GIT_DIR`/`GIT_WORK_TREE` 的命令里跑测试；不起后台空等，job 用 `--sync`。子 agent 的完成报告只是声明，监督会话逐条核实（先看 `git diff --stat` 的删除行）并做反向验证。

## 波次与依赖

```text
W1  T1 存储（sonnet）→ T2 hook 协议（sonnet）
W1  T3 监督事实（sonnet，与 T1/T2 并行）
W2  T4 等我判定（sonnet，依赖 T1 T3）→ T5 工作台投影（sonnet，依赖 T1 T3）
W3  T6 web（suga）、T7 文案（suga）、T8 文档与 skill（suga）、T9 补充测试（suga）
W4  T10 真实验证（监督会话）→ T11 反向验证与收尾（监督会话）
```

执行方：核心任务（判定逻辑、库表迁移、hook 协议字段、并发与状态）由 sonnet 子 agent 实施；展示、文案、文档、补充测试由 suga（`openai-codex/gpt-6.1-terra`，慢时 `gpt-6-sol`）实施；T10、T11 是验收，由监督会话做。同一 worktree 顺序实施，每个任务一个提交。

## 任务

命令的运行目录都是 Git root 或它的 worktree。每个 Go 任务提交前跑 `go build ./... && go vet ./...`。

### T1 存储：`needs_reply`、`released_at`

- 执行方: sonnet 子 agent。
- 文件: `internal/jobstore/store.go`（`migratePlanDecisions`）、`internal/jobstore/decisions.go`、`internal/jobstore/decisions_released_test.go`（新）。
- 动作:
  - `migratePlanDecisions` 用 `add` 加 `needs_reply INTEGER NOT NULL DEFAULT 0`、`released_at INTEGER`；基础 schema 不加引用它们的索引或视图（G057）。
  - `PlanDecision` 加 `NeedsReply bool`（`json:"needs_reply,omitempty"`）、`ReleasedAt int64`（`json:"released_at,omitempty"`）；select 列与 scan 用 `COALESCE`。`InsertDecision` 写 `needs_reply`。
  - `ReleaseDecision`、`ExpireSessionDecisions` 写 `released_at = unixNow()`；`expireDueLocked` 写 `released_at = asked_at + timeout_sec`。
- 验收: `go test ./internal/jobstore -run 'ReleasedAt|NeedsReply|OpenUpgradesPreviousReleaseSchema' -count=1`。用例 `TestDecisionReleasedAt`：三条关闭路径各断言 `released_at`（到期路径等于 `asked_at+timeout_sec`，不是扫描时刻）；作答路径 `released_at=0`；`TestDecisionNeedsReplyRoundTrip`：插入后读回为 true，旧行读作 false。旧库升级测试通过。
- 反向验证（T11 执行）: `expireDueLocked` 不写 `released_at` → `TestDecisionReleasedAt` 失败。
- 依赖: 无。

### T2 hook 协议：标记解析与上报

- 执行方: sonnet 子 agent。
- 文件: `internal/hookrelay/needs_reply.go`（新）、`internal/hookrelay/run.go`、`internal/client/client.go`（`OpenSessionTurn`）、`internal/httpapi/session_handler.go`（开回合请求）、`internal/sessionrelay/service.go`（`OpenTurn` 签名）、调用 `OpenTurn` 的测试假实现、`internal/hookrelay/needs_reply_test.go`（新）、`internal/sessionrelay/*_test.go`。
- 动作:
  - `splitNeedsReply(msg string) (body string, needs bool)`（R5）；`stop()` 在截断前调用（R6），开回合时带 `needs_reply`。
  - `client.OpenSessionTurn` 增加 `needsReply bool`，请求体 `needs_reply`（省略即 false，G032 不加兼容分支）；HTTP 请求结构接收并透传；`OpenTurn(sid, body, timeoutSec, needsReply)` 写入 `PlanDecision.NeedsReply`。
  - SessionStart 注入一行 `NeedsReplyReminder`（同 `TopicReminder`：`msgprefix.Hint`、仅 `CatchUpAgent`、追加在已有内容之后），英文、通用措辞：需要对方回答才能继续时，最后一行单独写 `[needs-reply]`；汇报完成不写。
- 验收: `go test ./internal/hookrelay ./internal/sessionrelay ./internal/httpapi -run 'NeedsReply|OpenTurn|Stop' -count=1`。表驱动 `TestSplitNeedsReply`：末行标记、末行带空白、标记后有空行、正文中间出现、大小写不同、只有标记、超长消息末行标记（截断后仍判 true）。`TestStopReportsNeedsReply`：假 server 断言请求体 `needs_reply=true` 且 `body` 不含标记；无标记时请求体无该字段或为 false。`TestOpenTurnNeedsReply` 经真实路由断言入库。`TestSessionStartNeedsReplyReminder` 断言 Claude 方言有这一行、ObserveOnly 方言没有。
- 反向验证: `splitNeedsReply` 恒返回 false → `TestStopReportsNeedsReply` 失败；先截断后判断 → 超长用例失败。
- 依赖: T1。

### T3 监督事实：`Supervision`

- 执行方: sonnet 子 agent。
- 文件: `internal/sessionrelay/supervision.go`（新）、`internal/sessionrelay/service.go`（`supervisingDetail` 改调用）、`internal/sessionrelay/supervision_test.go`（新）。
- 动作: `func (s *Service) Supervision(a jobstore.AgentSession) Supervision`：`Subagents = SubagentCount`；`CallerID` 非空时 `Jobs = CountActiveJobsByCaller(caller, window)`；`Watches = len(ListSessionJobWatches)`；不读 `SkipWhenSupervising` 与 `RelayMode`；失败按 R2。`supervisingDetail` 保留开关判断，只用 `Jobs`、`Subagents` 拼原有文案，行为与输出逐字不变。
- 验收: `go test ./internal/sessionrelay -run 'Supervision|Supervising|WaitReason' -count=1`。`TestSupervisionCountsWatches`（只有 watch 时 `Any()=true`）；`TestSupervisionIgnoresSwitch`（开关关、`relay_mode=off` 时仍报事实）；`TestSupervisingDetailIgnoresWatches`（characterization：只有 watch 时 `WaitReason` 与改前相同，`detail` 为空）；现有 `supervisingDetail` 用例全部不改即通过。
- 反向验证: `Supervision` 漏掉 Watches → `TestSupervisionCountsWatches` 失败；`supervisingDetail` 改为用 `Any()` → `TestSupervisingDetailIgnoresWatches` 失败。
- 依赖: 无。

### T4 「等我」判定：F1 + F2 + F3

- 执行方: sonnet 子 agent。
- 文件: `internal/today/today.go`（`Deps.Supervisor`、`Response.RecheckAt`）、`internal/today/relay_gate.go`（新：`relayGraceSec`、`relayTurnQualifies`）、`internal/today/cards.go`（`decisions()` / `relay()` 调用）、`internal/httpapi/server.go`（装配 `Supervisor: s.relay`）、`internal/httpapi/inbox_handler.go`（`recheck_at`）、`internal/today/relay_gate_test.go`（新）、`internal/httpapi/attention_rv1_test.go`（新）。
- 动作:
  - 常量 `relayGraceSec = 300`；`relay()` 只保留 `NeedsReply ∧ now-asked_at ≥ 300 ∧ !Supervision.Any()` 的回合（`acked_at` 现有过滤不变）；全不满足不出卡。会话查一次 `GetAgentSession` 后调 `Supervisor`（nil 视为不在监督）。
  - builder 记录 R4 的 `recheck_at`；`/v1/today` 与 `/v1/inbox` 响应加 `recheck_at`（`omitempty`）。
  - `inbox/waiting.go` 不改：卡只在条件满足时产生，`Classify` 保持纯函数。
- 验收: `go test ./internal/today ./internal/inbox ./internal/httpapi -run 'RelayGate|Waiting|Attention|Inbox|Today' -count=1`。`TestRelayGateSupervisedExcluded`（假 Supervisor 分别报 jobs / subagents / watches）；`TestRelayGateSupervisionStartsAfterOpen`（回合先满足后，Supervisor 再变 true，下一次构建即消失；再变 false 即回来）；`TestRelayGateGrace`（299s 不在、300s 在、`recheck_at = asked_at+300`）；`TestRelayGateNeedsReplyOnly`（无标记不出卡）。HTTP 层 `TestWaitingCountEqualsListRV1`：构造四种回合 + 一个 job hold，断言 `/v1/today` 的 `waiting` 长度、`/v1/inbox` 「要处理」长度与 `total_unread` 三者相等，且只含合格回合与 hold。
- 反向验证: 去掉监督条件、宽限改 0、去掉 `NeedsReply` 条件，各使对应用例失败；`/v1/inbox` 改用另一条过滤 → `TestWaitingCountEqualsListRV1` 失败。
- 依赖: T1、T3。

### T5 工作台投影：「监督中」/「空闲」

- 执行方: sonnet 子 agent。
- 文件: `internal/workbench/service.go`（`projectRelayThread` 调用处）、`internal/workbench/model.go`（`Thread.Supervising *Supervising`，`json:"supervising,omitempty"`，三个计数）、`internal/workbench/relay_supervision_test.go`（新）。
- 动作: 按 R7 投影；监督事实经 `relay.(interface{ Supervision(...) })` 断言取得，断言失败视为不在监督。`Summary()` 保留 `supervising`（列表需要显示）。
- 验收: `go test ./internal/workbench -count=1`。`TestProjectRelayThreadSupervising`：有标记 + 监督 → `working` 且无 attention、`supervising` 计数正确；无标记不监督 → `idle` 且无 attention；有标记不监督 → `blocked` 且有 attention（现状）；无开放回合但 `state=waiting_reply` → 现状。
- 反向验证: 去掉监督分支 → 第一个子用例失败。
- 依赖: T1、T3。

### T6 web：显示「监督中」与到点重拉

- 执行方: suga（gpt）。
- 文件: `web/src/api/types.ts`、工作台线程列表与详情组件（`web/src/components/workbench/` 下读 `thread.status` 的组件）、`web/src/store/inbox.ts`、`web/src/store/today.ts`、`web/src/i18n/pages/workbench.ts`（或现有工作台文案文件）、对应 `*.test.ts`。
- 动作: 线程有 `supervising` 时显示「监督中 · N 个 job · N 个子 agent · N 个被盯 job」（为 0 的项不显示）；`idle` 照常显示「空闲」，回复框不变。today / inbox 的 store 在响应带 `recheck_at` 时设一个定时器到点重拉（新响应覆盖旧定时器，页面卸载清除）。
- 验收: `cd web && npx vitest run` 相关用例：带 `supervising` 的线程渲染出「监督中」；`recheck_at` 到点触发一次 `refreshInbox`（假时钟）；`npx vue-tsc --noEmit` 通过。
- 依赖: T4、T5。

### T7 文案：digest 改名与提示

- 执行方: suga（gpt）。
- 文件: `internal/i18n/lang/zh-CN/work.ini`、`internal/i18n/lang/en/work.ini`（`[digest] counts`）、引用旧文案的测试（`internal/commands/work_test.go`、`internal/httpapi/*_test.go` 中断言 digest 文本的用例）。
- 动作: zh 改为「工作项·需要我 {needs_me} · 等资源 … 」，en 改为「work items needing me {needs_me} · …」，其余三项不变；不改代码。
- 验收: `go test ./internal/i18n -run CatalogKeysMatch -count=1`、`go test ./internal/work ./internal/today ./internal/httpapi ./internal/commands -run 'Digest|Today|Work' -count=1`；`grep -n '^counts' internal/i18n/lang/*/work.ini` 不再含「等我」/「needs me {」开头。
- 依赖: 无。

### T8 文档与 skill

- 执行方: suga（gpt）。
- 文件: `skills/gofer-usage/references/sessions-relay.md`、`docs/reference/http-api.md`。
- 动作: sessions-relay 写明：需要人回复时最后一行单独写 `[needs-reply]`，否则回合不进「等我」（仍可在工作台回复）；监督中的会话不进「等我」；回合满 5 分钟才进。按 G046 通用视角，不写本仓库细节。http-api 写开回合请求的可选字段 `needs_reply`、决策的 `needs_reply` / `released_at`、`/v1/today` 与 `/v1/inbox` 的 `recheck_at`、工作台线程的 `supervising`。
- 验收: `sh scripts/check-private-names.sh`（worktree 里用 `PRIVATE_NAMES_FILE=<主 checkout 绝对路径>/.private-names`）通过；人工核对与代码一致。汇报列出改动文件（G045）。
- 依赖: T2、T4、T5。

### T9 补充测试

- 执行方: suga（gpt）。
- 文件: `internal/httpapi/attention_rv1_test.go`、`internal/hookrelay/needs_reply_test.go`（追加，不改已有断言）。
- 动作: 补端到端用例：hook Stop（假 transcript，`last_assistant_message` 为空）→ 经真实路由开回合 → 5 分钟后（注入时钟）出现在 `/v1/today.waiting`；同会话再加一个 watch 后从列表消失、工作台变「监督中」；`ack` 后消失。
- 验收: `go test ./internal/httpapi ./internal/hookrelay -run 'RV1|NeedsReply' -count=1 -shuffle=on` 通过；计时遵守 G052（不用固定 sleep）。
- 依赖: T4、T5。

### T10 真实验证：Claude Code Stop 携带标记（监督会话）

- 执行方: 监督会话。
- 文件: 只写工作区 `tmp/rv1-fix/`（脚本、日志、`real-env.md`）。
- 动作: 容器内用 worktree 构建产物起临时 server（临时端口、配置目录、库）；hook 目录 `.env` 指向它；启动前清除 `GOFER_JOB_ID`、`GOFER_JOB_TOKEN`、`GOFER_SERVER_ADDR`、`GOFER_SERVER_TOKEN`、`GOFER_CONFIG`、`CLAUDECODE`、`CLAUDE_CODE_*`。预检：临时 server 有这条会话，线上 server 没有。用 `claude -p --model haiku --allowedTools 'Bash(sleep:*)'` 起会话，提示词先 `sleep 20`（期间 `gofer session relay on --session <sid>`），再以 `[needs-reply]` 单独一行结束；断言开放回合 `needs_reply=true`、正文无标记、`last_assistant_message` 原文末行即标记（hook 日志）。经 API 回复后让它以不带标记的汇报结束，断言第二个回合 `needs_reply=false`；最后 `relay off` 放行。只按自己记录的 pid 停进程，收尾看临时 server 日志。
- 验收: `tmp/rv1-fix/real-env.md` 记录 Claude Code 版本、命令、两个回合的 `needs_reply` 与 hook 日志片段。做不到时写 `UNVERIFIED_REAL_ENV: Claude Code Stop 输入保留末行标记（复现：<命令>）`，并由 T2 的 transcript 回退用例兜底；触发 fail-closed 第 1 条则停。
- 依赖: T2。

### T11 反向验证、全量与收尾（监督会话）

- 执行方: 监督会话。
- 文件: 设计稿「实施记录」、本计划状态、`.gofer/tracker/*`。
- 动作:
  - 反向验证（临时副本里改，不提交）：执行 T1–T5 列出的每一项，确认对应测试失败。
  - `git diff --stat` 查无关删除；逐条对照设计 §9 核实子 agent 与 suga 的声明。
  - `gofer-verify` 工作流（四路）通过；Windows 全量随发版前流程。
  - 合并回 main（`--no-ff`），删 worktree 与分支；设计稿补实施记录（含 RV2 时间点：下一批发版上线后满 7 天）；计划进入 COMPLETE 时写不超过 8 行的完成总结。
- 验收: `tmp/rv1-fix/reverse.md` 每项写改法与失败的测试名；`gofer wf show <id>` 全绿；主 checkout 里 `sh scripts/check-private-names.sh` 通过；`git log` 核对提交只含本任务文件。
- 依赖: T1–T10。

## 回滚与恢复

- 恢复点：每个任务一个提交，worktree 隔离；合并前 main 不受影响。合并后退回用 `git revert -m 1 <merge>`。
- 运行时回滚：退回旧版后两列无人读写，读写都写明列名；旧版的「等我」回到 RV1 口径。
- T10 触发 fail-closed 时不合并，回到设计。临时 server 与目录用完即删，只留 `tmp/rv1-fix/` 的记录。

## 人工 Gate

1. 批准本计划。
2. 当前执行请求（可与批准同一句）。
3. push、打 tag、发版：不在本计划内，攒批后按发版流程处理（含 G057 快照、G058 能力清单）。

实施过程本身没有其他人工 Gate，除非出现 Semantic Amendment 或 fail-closed。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| §9.2 F1 判据与现算 | T3 T4 | `TestSupervisionCountsWatches`、`TestRelayGateSupervisionStartsAfterOpen` |
| §9.2 Stop 行为零变化 | T3 | `TestSupervisingDetailIgnoresWatches`，现有用例 |
| §9.2 工作台「监督中」 | T5 T6 | `TestProjectRelayThreadSupervising`，web 用例 |
| §9.3 F2 宽限与同口径 | T4 T6 | `TestRelayGateGrace`、`TestWaitingCountEqualsListRV1`、`recheck_at` 用例 |
| §9.4 F3 标记与上报 | T2 T4 | `TestSplitNeedsReply`、`TestStopReportsNeedsReply`、`TestRelayGateNeedsReplyOnly` |
| §9.4 真实 hook | T10 | `real-env.md` |
| §9.4 G057 | T1 | `TestOpenUpgradesPreviousReleaseSchema`；发版后补快照 |
| §9.5 F4 | T1 | `TestDecisionReleasedAt` |
| §9.6 digest 改名 | T7 | T7 验收 |
| 反向验证 | T11 | `reverse.md` |

## 完成 Gate 与剩余工作

完成 Gate：T1–T11 通过；`gofer-verify` 全绿；T10 有证据或按规定披露；设计稿补实施记录；issue 评论记录结果。

剩余工作（不在本计划内）：随下一批发版 push；发版后补 `schema-v*` 快照（G057）与能力清单（G058）；上线满 7 天后做 RV2（误报率与漏报观察，设计 §9.7）；推送口径按 RV2 再定。
