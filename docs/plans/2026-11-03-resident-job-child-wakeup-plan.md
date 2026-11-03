<!-- template_id: plan; template_version: 1.3.0 -->
# 常驻 ACP job 的子 job 回执第一期 实施计划

> 状态：Draft 0.3 / 待人工计划批准
> issue：gofer-rt3z

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-03 | Claude（gofer 监督会话） | 初稿 |
| 0.2 | 2026-11-03 | Claude（gofer 监督会话） | 按计划审核修订（5 阻塞、6 纠正）：terminal 回执确认后停用；补收件箱卡片落点；重发与限频补发由 tick 驱动，CmdID 按批次；更新暂停验收改用集成测试加真实 hold；通知证据加 webhook；手动待送行、错误分类、依赖顺序 |
| 0.3 | 2026-11-03 | Claude（gofer 监督会话） | 按确认轮修订（3 阻塞、2 纠正）：卡片取告警事件，走收件箱 terminal 卡片路径；更新暂停替代验证要断言 say，覆盖本机和 worker 重启；手动 once 行确认后也消费 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：落地设计 Draft 0.7 的第一期。Scope freeze：只做设计“范围”六项和下列任务，其余 deferred 或走 discovery 分类。Review budget：一轮计划审核加一轮确认；有阻塞项才继续，上限按严谨规划核心合同。停止条件：确认轮无 `CORE_BLOCKING`，然后停在计划 Gate。

## 目标与完成定义

目标：常驻 job 用自己的凭据派出子 job 后，子 job 有结果时 gofer 自动给它开新一轮，不需要外部参与；送不到时告诉人。

完成定义：设计“结论”列出的 9 条验收全部有真实环境证据（本机与 worker 父 job 各一遍），`gofer-verify` 全绿，文档按 G045、G058 同步，设计稿补实施记录。

## 范围、排除项与授权

范围：设计“范围”1–6。

排除：父 job 结束后自动续接（第二期）；R2、R3、R4；plan 待办派发的回执；定时 wakeup 对活 job 的行为；常驻会话阻塞 worker 升级（`gofer-xmit`）；配置入库（回执事件第一期只能写文件或用单次参数）。

授权边界：批准本计划不等于授权执行，执行需要单独的当前请求。本地提交按既有默认授权。push 和发版不在本计划内，另走既有流程。真实环境验收用临时 server 和临时 worker，不改用户的主机配置和线上 server。

- `host_or_non_offline_action=REQUIRED`

需要的主机动作只有 T12：经 `gofer job --runner local` 在主机起临时 server、worker 和本地 webhook 接收端，跑真实 claude-acp 父 job，产物只写 `tmp/`，消耗少量模型 token。

验收例外（批准本计划即接受）：“更新暂停期间提交的子 job 也有回执”不用真实更新。更新暂停的两个入口都要求已注册的 server（否则 409），为验收注册服务会改主机。改用 T9 的两条集成测试（本机放行、worker 父 job 重启后放行，都断言 say 内容），加 T12 真实 hold 走同一条 `parkJob` 路径。

## 输入与批准证据

- 设计：`docs/design/2026-11-03-resident-job-child-wakeup-design.md` Draft 0.7（提交 `dae7c4ed`）。
- 用户决定（2026-11-03）：“R1 设计 按建议”；“回执哪些事件 - 能让事件可以配置吗？2 取消“暂停空闲超时” 按建议”；“R1 拆第二期”。0.5–0.7 是复核修订，未改方向，由本计划的批准一并确认。
- 复核证据：窄范围复核和两轮确认，最后一轮（0.7）无阻塞项。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 持久化回执登记 | `job_wakeups`、`internal/job/wakeup.go` | 是 | 加 `auto_child` 列和 `await_input` 哨兵 | DIRECT_REUSE | none | 无新表 |
| CAP-02 | 给活着的会话开新一轮 | `saySession`、`SessionCommandSender` | 是 | 拆错误类型，worker 分支先占 pending | DIRECT_REUSE | none | 人工 say 共用同一路径 |
| CAP-03 | 周期扫描 | `startScheduleLoop` | 是 | 紧随 `sweepDueWakeups` 调新函数 | DIRECT_REUSE | none | 无新 goroutine |
| CAP-04 | 告警 | `internal/notify`、`internal/webpush`、`internal/inbox` | 是 | 加新事件和 kind | DIRECT_REUSE | none | 旧事件默认集合不变 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 前置检查与 fail-closed 条件

基线（2026-11-03）：Git root `inhere-tools/gofer`，分支 `main`，写本计划时 HEAD 为 `f94a181f`。工作树只有本会话的 `.gofer/tracker/issues.jsonl`；开 worktree 前先提交 tracker。

Fail-closed：
- 配置入库计划如果先开工，先核对 `WakeupConfig`、字段策略表是否变了，变了按 discovery 分类。
- `TestOpenUpgradesPreviousReleaseSchema` 红时不继续后面任务。
- 真实 claude-acp 或 worker 环境不可用时，T12 报 `UNVERIFIED_REAL_ENV`，不算完成。
- 发现需要改 worker 协议，属于 `Semantic Amendment`，停下回到计划 Gate。

派活：`-a suga --runner local --cwd inhere-tools/gofer --worktree --model openai-codex/gpt-6.1-terra --strict-timeout --timeout 5400 --notify`。每批一个 job，合并后再派下一批。我审查 diff、接线和反向验证，不以汇报为准。

## 波次与依赖

| 波次 | 任务 | 依赖 | 说明 |
|---|---|---|---|
| W1 | T1、T2、T3、T4 | T4 依赖 T1、T3，其余无 | 地基，单独都不改变现有用户路径（T3 的 interaction 补记、T4 的默认集合除外） |
| W2 | T5、T6、T7、T8、T9 | W1 | 主路径，顺序执行 |
| W3 | T10、T11 | W2 | 收口和文档 |
| W4 | T12 | W3 | 真实环境验收 |

## 任务

### T1 wakeup 存储

- 文件: `internal/jobstore/wakeups.go`、jobstore schema 与迁移、`internal/jobstore/*_test.go`。
- 动作: 加 `auto_child INTEGER NOT NULL DEFAULT 0`，走迁移（G057），基础 schema 不引用它；同步 `selectWakeupCols`、`scanWakeup`、`InsertWakeup`。常量 `WakeupPendingSay = "await_input"`。新增 `MarkWakeupPendingSay(id)`（不累加 `fired_count`）、`ListPendingSay()`、`ListAutoChildByParent(parentJobID)`、`ListAutoChildFinishedBefore(ts)`、`RetargetWakeupFilter(id, fromJobID, toJobID) (bool, error)`（CAS）、`CompleteWakeupSay(id, disable bool)`（清哨兵并累加 `fired_count`，`disable` 时同时停用）、`ListJobEventsByType(eventType string, sinceAt int64)`（给 T4 的卡片用）。`fired_count` 改到成功后累加，失败不回退的旧行为去掉。
- 验证: 在 `inhere-tools/gofer` 运行 `go test ./internal/jobstore -run 'Wakeup|OpenUpgrades'`；`go test ./internal/job -run WakeupFiredCount`（续接失败发生在 `fireWakeup`）。
- 完成标准: 旧库能升级；CAS 只有一方成功；续接失败的 wakeup `fired_count` 不增加。
- 依赖: 无。

### T2 say 错误与占位

- 文件: `internal/job/session_commands.go`、`internal/job/session_recovery.go`、对应测试。
- 动作: 新增可用 `errors.Is` 区分的错误：`ErrSessionTurnBusy`（一轮进行中）、`ErrSessionInputQueued`、`ErrWorkerOffline`、`ErrSessionEnding`、`ErrNoLiveSession`，都包住现有 `ErrJobNotRunning` 或 `ErrInvalidRequest`，HTTP 状态和文案不变。worker 分支改为发送前在 `entry.mu` 内占 pending 并设 `sessionCommandTurn`，发送失败回滚（`sessionEnding` 时不回滚）。新增内部函数 `resendSessionSay(id, cmdID, message string) error`：只在 pending 已占、状态仍 awaiting_input、TurnNo 没变时发送，供 T9 重发。`saySession` 内部增加可传入 cmdID 的变体，外部接口不变。CmdID 按批次稳定（同一批重发同一个，新批次新的），因为 worker 的 seen 不清理，跨批次复用会被丢弃。
- 验证: `go test ./internal/job -run 'Say|SessionCommand'`，含两路并发 say 只有一路通过、TurnNo 已推进时不重发的用例。
- 完成标准: 现有 say 接口行为不变；并发用例在改动前失败、改动后通过（反向验证）。
- 依赖: 无。

### T3 配置与事件

- 文件: `internal/config`（`WakeupConfig`、校验）、`internal/job/model.go`、wakeup 事件目录、`internal/job/remote_interaction.go`。
- 动作: `WakeupConfig.ChildEvents []string`，默认 `[job.terminal, job.needs_review]`，校验未知值报错、自动补 `job.terminal`；提供 `Config.ResolveChildEvents(override []string) ([]string, error)`，供 T6 用。新增 `EventJobChildWakeFailed = "job.child_wake_failed"`，详情 `{wakeup_id, child_job_id, reason}`。`interaction.created`、`job.awaiting_approval` 进事件目录。`injectInteraction` 非幂等分支补记 `interaction.created`，补记前查 DB 防重复。
- 验证: `go test ./internal/config -run Wakeup`；`go test ./internal/job -run 'Interaction|WakeupEvent'`。
- 完成标准: 非法配置被拒；worker 子 job 的 interaction 只记一次事件。
- 依赖: 无。

### T4 告警通知

- 文件: `internal/notify/match.go`、`internal/notify/catalog.go`、`internal/webpush/dispatch.go`、`internal/webpush/sender.go`（如需要）、`internal/config/inbox.go`、`internal/config/model.go`、`internal/inbox/waiting.go`、`internal/httpapi/inbox_handler.go`、`web/src/utils/inbox.ts`、i18n 文案、相关测试。
- 动作: 新事件进 webhook 和桌面默认集合、通知目录。webpush 加白名单和标题正文分支，`ThreadID` 留空、`Tag` 为 `child-wake:<wakeup_id>`，这样同时不走合并窗口和线程 Topic；`URL` 另设为父 job 页面。新增 action 类 inbox kind 及卡片：照 `job.failed` 的做法，在 `inbox.TerminalCards` 旁构建，经 `waitingFor` 合入；数据源是 `ListJobEventsByType("job.child_wake_failed", …)`，窗口 72 小时。收件箱 clear 隐藏，首页到期消失（首页一点就消失要新增持久 dismiss，不在范围内）。不改 `internal/today/cards.go`。`job.wakeup_failed` 的默认集合不变。
- 验证: `go test ./internal/notify ./internal/webpush ./internal/inbox ./internal/config ./internal/httpapi`；`web` 下跑类型检查。
- 完成标准: 默认配置下三个渠道都会发；默认配置下收件箱 action 列出现卡片；同线程后续推送不替换告警；断言旧事件仍不在默认集合。
- 依赖: T1、T3。

### T5 选路、待送、发送、确认

- 文件: `internal/job/wakeup_session.go`（新）、`internal/job/wakeup.go`、`internal/job/session_state.go`、`internal/job/session_recovery.go`、`internal/i18n/prompts/<lang>/`、测试。
- 动作: 按设计“总体方案”的表选路，只拦 event 类。路由用 `IsFinished` 判父 job 已结束。每个父 job 一把回执锁（`sync.Map`，顺序固定为回执锁在前、`entry.mu` 在后），覆盖读待送、合并、发送、记批次。回执模板只含子 job id、状态、退出码、查看命令，前缀 `msgprefix.Wakeup`；手动 wakeup 的 instruction 附在模板后；需要输入类写明“你不能代答，请转告人”。写入待送后再读一次父 job 状态，已是 awaiting_input 就立刻投递。`ErrSessionTurnBusy`、`ErrSessionInputQueued`、`ErrWorkerOffline`、`ErrUpgradeDraining`、未分类的发送错误都保持待送；`ErrSessionEnding` 按父 job 结束处理；`ErrNoLiveSession` 时 recovering 保持待送、needs_review 按父 job 结束处理。父 job 结束处理函数 `endParentChildWakes(parentJobID, reason)` 在这里创建，T7 只负责把它挂到各路径。发送放后台，终态和待验收事件在 observer 内同步取 entry，等 `entry.done` 后再读状态。确认条件：回到 awaiting_input 且 TurnNo 超过发送值，只清这一批，子 job 状态已变就保留哨兵；确认的是 `job.terminal` 回执，或该行 `Mode` 是 once（手动 event wakeup 默认如此）时，调 `CompleteWakeupSay(id, true)` 消费该行。限频：同一子 job 需要输入类 1 分钟一条，限频内写哨兵。重发和限频补发由 T9 的 tick 驱动。`awaitSessionInput`、`applyRemoteSessionState` 共用待送检查。日志 `wakeup.say_pending`、`wakeup.say_sent`，不含正文。
- 验证: `go test ./internal/job -run 'WakeupSession'`，覆盖：awaiting_input 直发、进行中待送、多子 job 合并、already queued、离线、升级排空、手动 instruction 附加、terminal 回执确认后停用、手动 once 行确认后不再触发、确认后不重发、这一轮中途死掉不算送达、标题不入 say、定时 wakeup 走旧路径。
- 完成标准: 上述用例通过；手动登记且目标已结束的 wakeup 行为与基线一致。
- 依赖: T1、T2、T3。

### T6 自动登记

- 文件: `internal/job/`（新 helper、`submit.go`、`hold.go`）、`internal/httpapi/job_handler.go`、`internal/httpapi/jobcredential.go`、`internal/commands/job.go`、MCP 对应入口。
- 动作: helper `registerChildWakeup(parentJobID string, child JobResult, req JobRequest) error` 在持久化后、记事件前调用，普通路径和 `parkJob` 各一处，`req.heldJobID == ""` 防重复，retry、rebuild 不登记。父 job id 经 `json:"-"` 字段由 handler 从 `jobCallerFromCtx` 传入。`submitGrant` 下沉到 job 层。同步等待判断由 handler 传入（`?wait=1` 不进 `req.Sync`）；`parkJob` 路径一律登记。新增 `job run --no-parent-wake`、`--parent-wake-events`，HTTP 字段 `no_parent_wake`、`parent_wake_events`；只对 job 凭据提交生效，其他调用方传了就在 httpapi handler 拒绝（不放进 `Submit`：hold 放行、更新放行和重试都会重入 `Submit`，那时父 id 为空，放进去会让放行失败）。continuous，`CreatedBy` 用父 job 的 CallerID，TTL 取 `max(wakeup.ttl_sec, 子 job 时限 + 排队余量)`。CLI 经 `app.Run` 分发测试（GR110）。
- 验证: `go test ./internal/job ./internal/httpapi ./internal/commands -run 'ChildWake|ParentWake'`。
- 完成标准: 常驻 job 凭据提交产生一行；请求体伪造父 job 无效；hold 放行不重复；非常驻 job 和普通用户提交不产生行；未获 can_submit 的提交仍是 403。
- 依赖: T3、T5。

### T7 父 job 结束处理

- 文件: `internal/job/execute.go`、`internal/job/wakeup_session.go`、测试。
- 动作: 在 terminal、被接管、needs_review、`parkAfterFailedCheckResume` 四条路径调用 T5 的 `endParentChildWakes`：停用该父 job 名下的自动行，对尚未回执的行各记一条 `job.child_wake_failed`，无行时不记；手动 event wakeup 的待送行也停用，记已有的 `job.wakeup_failed`（新告警事件只属于 `auto_child` 路径）。之后人工裁决再记 terminal 时不重复告警。不复活。
- 验证: `go test ./internal/job -run 'ChildWakeParentEnd'`，四条路径各一例，含裁决后不重复。
- 完成标准: 四条路径都告警且只告警一次；父 job 不被 resume。
- 依赖: T5、T6。

### T8 子 job 接管与重试

- 文件: `internal/job/execute.go`、`internal/job/fallback.go`、`internal/job/retry_sweep.go`、`internal/httpapi/retry_handler.go`（只在需要时）、测试。
- 动作: 接管和重试提交成功时调 `RetargetWakeupFilter`，CAS 后补查新 job 状态。回执前查待执行重试：有就不发不停用。重试取消四处（解不开、无 owner、权限拒绝、`CancelRetry` API）触发补发；可重试的提交失败不补发。核验续跑提交失败转 needs_review 的情况按设计保留。
- 验证: `go test ./internal/job -run 'ChildWakeRetarget|ChildWakeRetry'`，含新 job 在 CAS 前已结束。
- 完成标准: 回执指向最终 job；每种取消都有回执；退避重试期间不发。
- 依赖: T5、T6。

### T9 扫描与对账

- 文件: `internal/job/wakeup_session.go`、`internal/serve/serve.go`、`internal/job/wakeup.go`、`internal/job/session_recovery.go`、测试。
- 动作: `run()` 里紧随 `sweepDueWakeups` 调 `SweepPendingSays(now)`，启动时在 `ReconcileOrphanJobs` 之后再调一次。对账只看 `auto_child=1`、启用、子 job `IsTerminal`、`ended_at` 超过宽限期（常量 2 分钟）的行；已停用或已回执的不再处理；有重试行或接管标记走 T8，重试行 done 且有 `new_job_id` 时补 CAS。tick 同时驱动：worker 父 job 发送超过 60 秒未确认的批次调 `resendSessionSay`；限频到期的哨兵补发。自动行 TTL 到期记 `job.child_wake_failed`。`applyRemoteSessionState` 看到新 turn 时续期 worker 父 job 令牌（先按读代码实现，T12 实测）。
- 验证: `go test ./internal/job ./internal/serve -run 'PendingSay|ChildWakeReconcile'`，覆盖 `FailRecoveringJobs` 改库后的补发、宽限期内不发、等验收不重复发、已回执不再补发、重启后投递、worker 丢帧后重发、限频到期补发；更新暂停两条：本机父 job（暂停→子 job parked→`ResumeStartsForUpdate`→父 job 收到 say）；worker 父 job（照 `TestUpdatePausedSurvivesRestart` 建第二个 Service，对账、放行后送达）。都断言 say 内容。
- 完成标准: 上述用例通过；同一行在 tick 和事件路径并发时只发一次。
- 依赖: T7、T8。

### T10 提示词与展示

- 文件: `internal/job/prompt_sections.go`、`internal/i18n/prompts/<lang>/`、`protocol.go`、`internal/commands` 的 wakeup 命令、`internal/httpapi` wakeup handler、`internal/mcpserver`、权限检查。
- 动作: 有提交权限的常驻 job 注入提示：派完结束本轮、设够长 `--idle-timeout`、父用 `--shared-dir`、可写子 job 用 `--worktree`、并发槽位留余量；空 prompt 不注入；各语言一份（G046）。`wakeup ls`、HTTP、MCP 把 `await_input` 显示为待送。核实手动 event wakeup 走 say 时不能用 `job.wake.others` 绕过 `job.manage.others`，改了权限就跑 `go generate ./internal/httpapi/...`（G056）。
- 验证: `go test ./internal/job ./internal/commands ./internal/httpapi ./internal/mcpserver -run 'Wakeup|PromptSection'`。
- 完成标准: 无提交权限或空 prompt 时提示不出现；三个入口都不把哨兵显示成 job id；越权用例被拒。
- 依赖: T9。

### T11 文档

- 文件: `skills/gofer-usage/references/`（wakeup、job run、can_submit）、`docs/reference/http-api.md`、`docs/capabilities.md`、`CHANGELOG.md`、设计稿末尾。
- 动作: 写回执用法、`wakeup.child_events`、两个新参数、父 job 要设长 `--idle-timeout`、回退前确认没有待送。CHANGELOG 写两处全局变化（worker interaction 通知、新告警事件默认开启）。设计稿补实施记录，issue 的 `--design` 指向它。
- 验证: 在 `inhere-tools/gofer` 运行 `sh scripts/check-private-names.sh`；`idevstd script validate_document --kind design docs/design/2026-11-03-resident-job-child-wakeup-design.md`。
- 完成标准: 文档和 `--help` 一致；对外文档没有 issue id 和本机信息。
- 依赖: T10。

### T12 真实环境验收

- 文件: `tmp/gofer-wt-check/`（脚本和证据，不入库）。
- 动作: 用当前构建起临时 server 和临时 worker，逐条跑设计 9 条验收，本机与 worker 父 job 各一遍，用真实 claude-acp（“更新暂停”一条按“范围”里的例外）。桌面和 webpush 的告警由 T4 测试覆盖，T12 只实测 webhook，这点要在证据里写明。子 job 和父 job 都加 `--shared-dir` 或 `--lock <子目录>`，用完 `job end`。临时配置接本地 webhook 接收端。记录 job id、事件、webhook 投递记录、say 内容；核实日志和 say 里没有标题、输出、token；实测 worker 令牌续期。
- 验证: 每条验收一条证据（job id 和事件时间线）写入 `tmp/gofer-wt-check/`，我逐条核对，不用子 agent 的汇报。
- 完成标准: 9 条全部通过，或明确列出 `UNVERIFIED_REAL_ENV` 并停在完成 Gate。
- 依赖: T11。

## 回滚与恢复

每批一个 worktree、一次合并，出问题回退合并提交；合并后未发版不影响线上。Schema 只加一列。发版后回退：遗留待送会丢，旧版本还会把 `await_input` 显示成 job id，所以降级前先确认没有待送（T11 写入发版说明）。恢复点是每批合并后的 `main`。

## 人工 Gate

1. 计划批准（本次）。
2. 当前执行请求：批准后另说一句开始。
3. 发版：tag、升级已长期授权，push 仍走既有审批。

## 可追溯性

| 设计项或验收 | 任务 | 验证 |
|---|---|---|
| 范围 1：活着的常驻 job 走 say | T2、T5 | T5 用例；T12 |
| 范围 2：自动登记 | T6 | T6 用例；T12 |
| 范围 3：失败不显示成成功 | T1、T7 | T1、T7 用例；T12 |
| 范围 4：提示词 | T10 | T10 用例；T12 |
| 范围 5：接管和重试 | T8、T9 | T8、T9 用例；T12 |
| 范围 6：事件可配置 | T3、T6 | T3、T6 用例；T12 |
| 告警送达人 | T4、T7 | T4、T7 用例；T12 |
| 验收：派子 job 后自动开新一轮 | T5、T6 | T12 |
| 验收：一轮进行中待送、合并 | T5 | T12 |
| 验收：父 job 先结束有告警、不复活 | T4、T7 | T12 |
| 验收：重试、换 agent、取消补发 | T8 | T12 |
| 验收：更新暂停期间提交 | T6、T9 | T9 两条集成测试加 T12 真实 hold（已列例外） |
| 验收：可写子 job | T10 | T12 |
| 验收：待送期间重启 | T9 | T12 |
| 验收：多行标题不影响 say | T5 | T5 用例；T12 |
| 验收：可选事件与限频 | T3、T5 | T12 |

## 完成 Gate 与剩余工作

完成条件：T1–T12 通过；对合并后的 `main` 跑 `gofer-verify` 全绿；`gofer issue close gofer-rt3z`。进入 `COMPLETE` 时在 progress 写不超过 8 行的完成总结。

剩余工作：第二期（超时后 resume）、`gofer-xmit`、R2、R3、R4。
