<!-- template_id: plan; template_version: 1.2.0 -->
# X3 A1 交互式 ACP 会话 job 实施计划

> 状态：Draft 0.1 / 待人工计划批准；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-30 | Codex | 将 Approved 设计的 A1 交互式 ACP 会话 job 拆为状态与存储、ACP 多轮、超时锁配额、恢复、入口、Web、工作台和真实进程冒烟波次 |

## 目标与完成定义

`thinking_mode=RIGOROUS`；`core_objective=在同一 ACP job 内连续对话且可恢复`；`allowed_scope=Approved X3/A1`；`non_goals=B1–B3、pty/cli-agent 常驻、live 服务和部署动作`；`expansion_policy=DEFER_OR_REQUEST`；`review_budget=discovery + confirmation，只有 CORE_BLOCKING 时进入最多四轮 blocking closure，总计不超过八轮`；`stop_condition=遇到未裁决的协议/权限/生命周期冲突或完成本计划候选并提交时停止`。核心目标是在现有 ACP runner、job 生命周期、目录锁、agent 并发配额、审批和工作台积木上增加可持久化的持续会话 job。入口为 `gofer job run -a <acp-agent> --session`、`gofer job say <id> "<消息>"`、`gofer job end <id>`，并提供等价 HTTP/MCP 接口；Web 新建 job 和工作台 ACP 线程能够选择持续会话。

完成定义：会话 job 在 `running ↔ awaiting_input` 间逐轮运行，最终进入 `done`、`cancelled` 或 `failed`；`timeout_sec` 只限制单轮，`idle_timeout_sec`（默认 1800 秒）只限制等待输入，`max_session_sec` 可选且默认不限；等待输入期间仍持有目录锁和 agent 并发名额。三类新增事件、逐轮输出分隔、ACP 全量 `acp.jsonl`、say/end 权限、server/worker 恢复、工作台文本过滤、HTTP/CLI/MCP/Web 契约均有测试和证据。

必须有真实进程冒烟：使用临时 serve、临时配置、随机端口和一个可用 ACP agent；若主机没有可用 agent，在 `testutil` 放置最小假 ACP agent。一个 job 完成三轮对话，第二轮引用第一轮内容；等待输入触发空闲超时后状态为 `done`；另行验证取消和 `session/load` 恢复路径。计划候选提交不代表功能完成、部署完成或业务验收完成。

## 范围、排除项与授权

- 只修改 `tools/gofer` 独立 Git 仓的 A1 owner。候选 owner 由 T00 以当前源代码确认为准：`internal/job`/jobstore 负责状态、持久化、事件、生命周期和权限调用；`internal/runner/acp` 负责 ACP 进程与多轮协议；`internal/httpapi`、`internal/client`、`internal/commands`、`internal/mcpserver` 负责入口绑定与转发；`web` 负责 job 详情、新建表单和工作台视图；`testutil`/集成测试负责假 agent 与临时 serve 冒烟；README、skill 和设计实测记录负责文档证据。遵守 G021/G022，入口不承载业务编排。
- A1 的状态真源是 job 持久化记录与其事件/日志。`awaiting_input` 是非终态；`recovering` 只作为恢复过程中的内部状态或等价持久表示，不能被页面误报为已完成。禁止复制一套独立会话状态缓存；ACP session id、turn 序号、截止时间、锁路径和 agent 名额占用必须能从现有生命周期读取或按新增字段明确落盘。
- 会话第一轮允许有 prompt，也允许空 prompt 直接进入 `awaiting_input`。每轮 `session/prompt` 返回 stop reason 后写入 turn 结束事件并等待下一条消息；`say` 只向同一 ACP 会话发送下一轮，不新建 job；`end` 释放进程、锁和配额并进入 `done`。agent 异常退出进入 `failed`，取消进入 `cancelled`。
- `say`/`end` 复用取消 job 的调用方判定：发起者和 user caller 可操作，job caller 只能操作自己派发的会话 job。HTTP/MCP/CLI 不得绕过这一边界；错误返回沿现有错误 envelope 和状态码。
- 本期不改 pty 交互 job、不让非 ACP cli-agent 常驻、不实现跨 server 重启保留同一 agent 进程，不做 B1 目录锁等待、B2 pty 会话 id、B3 prime 精简，也不改真实配置、live server/worker、部署、推送或硬件动作。测试一律使用 `t.TempDir()`；smoke 只用临时 config、随机端口，命令显式带 `--server http://127.0.0.1:<端口>` 或 `-c <临时配置>`。
- `web/dist` 不作为前端构建输出目录；前端构建只能写仓库 `tmp` 下的临时目录。
- `host_or_non_offline_action=NOT_APPLICABLE`。本计划当前授权仅涵盖离线临时进程/测试、源码和文档修改及本地提交；push、真实服务、真实配置、部署和外部动作均不在完成适用范围。

## 输入与批准证据

- 唯一设计依据：[Approved 交互式 ACP 会话 job 设计](../design/2026-09-30-interactive-acp-job-and-backlog-design.md)，其中 A1 与“决策”第 2、3 条已批准；本计划只实施 X3/A1，不借 B1–B3 扩大范围。
- 实施起点为 gofer 独立仓 `main` 的 `56f3b23`（tag `v0.84.0`，或监督者批准后的后继 HEAD）；计划编写时 `git status --short --untracked-files=all` 有预存 `.codebase-memory/` 未跟踪目录，不能 stage 或修改它。
- 当前可复用积木（以 T00 当前源代码复核为准）：`internal/runner/acp/runner.go` 目前执行 initialize → session/new|load → session/prompt 的单轮映射；`internal/job/execute.go` 在排队后建立 run context 并处理超时、锁和 agent 配额；`internal/job/resume.go` 与 `acp_resume_test.go` 已覆盖 session/load 的单轮续接；`internal/workbench/service.go` 的 `Turn` 对 agent 线程调用 `ResumeJob`；`internal/httpapi` 已有 job、ACP stream、interaction 和 workbench 路由；`internal/commands/job.go` 已有 run/resume/cancel；`internal/mcpserver` 已有 job backend；`web/src/views/Workbench.vue` 与 `components/workbench/*` 已有工作台对话积木。上述现状不能代替 T00 对字段和调用链的复核。
- 本 job 明确授权编写并本地提交 X3 计划候选。候选提交后停在人工计划批准 Gate；监督者批准后还需续接请求才能实施，执行方式固定为 `DIRECT_CONTINUOUS`。
- 适用约束：`workspace.md`、本仓 `AGENTS.md` 的 G021/G022/G032、注入的 `house-rules`/`gofer-repo`、IDEV-STD plan 合同。不得 push。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 会话 job 状态、turn 元数据和事件持久化 | `internal/job/model.go`、jobstore schema/迁移、现有 job 事件和结果持久化 | 复用 JobResult、状态常量、事件表、日志目录和现有查询投影 | 增加会话模式、`awaiting_input`/恢复所需字段、turn 序号/截止时间及三类 job 事件；由 job service 统一变更状态 | OWNER_EXTENSION | 当前 job 只有单轮运行终态，没有持续等待输入的非终态 | 不在 runner、HTTP 或 Web 各自保存状态；事件和持久化必须由 job 层统一写入 |
| CAP-02 | ACP agent 常驻与多轮 prompt | `internal/runner/acp/runner.go`、`internal/acp` client、现有 permission handler、`acp_resume_test.go` | 复用 initialize、session/new 或 session/load、session/prompt、stdout/stderr/acp.jsonl 映射和审批通道 | 在一个 ACP 进程生命周期内暴露可取消的 turn seam、轮次边界和等待输入信号；首轮可空 prompt，后续由 job service 驱动 | OWNER_EXTENSION | 当前 `Run` 在一个 prompt 返回后结束，无法在同一进程继续 prompt | runner 不决定 job 终态或权限；必须防止单轮 context deadline 误杀等待输入的进程 |
| CAP-03 | 单轮/空闲/会话总时限 | `internal/job/execute.go` 的 run context、stall/timeout 处理、现有配置解析 | 复用排队不计执行超时、取消 context、现有 timer/事件和结果字段 | 将单轮 deadline、awaiting idle deadline、可选 max session deadline 分层，并在状态转换处停止/重置对应计时器 | OWNER_EXTENSION | 现有 timeout 语义面向一次 job，不区分等待输入时间 | 多个 timer 可能重复终止或泄漏；必须用单一生命周期 owner 和可观测原因 |
| CAP-04 | 会话期间持续锁与 agent 并发占用 | `internal/job/dirlock.go`、`execute.go`、`concurrency.go`、现有 release 路径 | 复用同一锁 key、agent semaphore 和取消释放函数 | 将锁/配额生命周期从“单轮结束”延长到会话终态；页面投影实际持有锁路径 | OWNER_EXTENSION | 现有执行完成即释放资源，等待输入会提前释放 | 释放必须幂等；`say` 不能重新排队或取得第二份名额，恢复失败必须释放原占用 |
| CAP-05 | server 重启/worker 断线恢复 | `internal/job/recover.go`、remote worker reconcile、`ResumeJob`/session/load 测试 | 复用 job 恢复扫描、agent capability `loadSession`、source session id 和 failed 错误口径 | 为会话 job 增加 recovering 检查点；支持 load 时拉起并回到 awaiting_input，不支持时 failed 并提示 | OWNER_EXTENSION | 当前恢复主要面向单轮/续接 job，未定义常驻 job 的 awaiting_input 恢复 | 不声称恢复原进程；必须区分 session/load 不支持、agent 异常和 worker 断线，并避免重复占用锁/配额 |
| CAP-06 | HTTP/CLI/MCP 同等 say/end 契约 | `internal/httpapi` job handlers、`internal/client`、`internal/commands/job.go`、`internal/mcpserver` backend | 复用 job caller middleware、cancel endpoint、client envelope、MCP backend 转发 | 增加 `job say`、`job end` 和 HTTP/MCP 方法，统一请求模型、状态投影、权限和错误 | OWNER_EXTENSION | 当前只有 resume 新 job、cancel 终止 job，没有同 job 多轮入口 | 不能让 CLI 的本地分支绕过 server 权限；`server`/`local` runner 归一化沿现有 G043 入口处理 |
| CAP-07 | Web job 详情、新建持续会话、工作台文本流 | `web/src/views/JobDetail.vue`、新建 job 表单、`web/src/views/Workbench.vue`、`components/workbench/*`、ACP SSE | 复用现有 SSE 日志、job 详情、审批卡片和 workbench turn 状态 | 新增持续会话勾选、say/end/释放锁并结束按钮、awaiting_input 显示；工作台只投影用户消息和 agent 回复文本，并给“查看过程”链接 | OWNER_EXTENSION | 当前工作台一轮一个 job，详情没有 awaiting_input/锁路径/持续会话控件 | 工具调用/思考/审批只留 job 详情；不得在工作台复制 ACP 事件或形成第二套会话历史真源 |
| CAP-08 | 临时假 ACP agent 与真实进程冒烟 | `internal/runner/acp` 测试夹具、现有 e2e/serve 测试、ACP protocol client | 复用现有 testutil、随机端口 server 启动和协议 JSONL 解析 | 若无可用主机 ACP agent，新增最小可用 fake agent，仅支持 initialize、session/new、session/load、session/prompt 和可控退出/延迟 | OWNER_EXTENSION | 当前没有覆盖同一常驻 job 三轮、idle、cancel、restart/load 的完整进程场景 | fake agent 只用于测试；真实 agent 可用性、网络和认证不能被假 agent 冒充生产验收 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立 ACP session daemon | CAP-02 | 设计要求 job 持有 agent 进程、锁和配额；额外 daemon 会改变部署与生命周期，先以 job owner 内常驻进程验证 |
| 第二套会话消息数据库/缓存 | CAP-01 | job 事件、ACP 日志和 workbench 投影已有真源；复制消息会导致权限、顺序和恢复漂移 |
| 工作台直接连接 ACP socket | CAP-07 | 工作台必须经 job/server 权限和“查看过程”链接；直连会绕过 job 事件、审批和 caller 判定 |
| 为旧 worker 增加无标记协议回退 | CAP-05 | G032 禁止永久兼容分支；若实际需要兼容，必须明确 `// DEPRECATED(vX): remove in vY`、迁移窗口和文档记录，否则直接失败并回到 Gate |

## 前置检查与 fail-closed 条件

- 计划基线：Git root `D:/work/inhere/hyy-ai-inspect/tools/gofer`，branch `main`，HEAD `56f3b23`，tag `v0.84.0`；提交前和每个 mutation wave 前执行 `git status --short --untracked-files=all`，只 stage 本计划或当波 owner 文件。
- T00 必须读取当前 `internal/runner/acp`、`internal/acp`、`internal/job/{model,execute,resume,recover,dirlock,concurrency,events}`、jobstore schema、`internal/httpapi` job/stream/workbench handler、`internal/client`、`internal/commands/job.go`、`internal/mcpserver` job backend、workbench Web 组件和既有 ACP/resume/recovery 测试。若代码事实与设计冲突，或需要扩大 wire/权限/worker 语义，立即停止并在报告中写明冲突，不自作主张改协议。
- 每个 mutation wave 前重跑 `idev-std probe --workspace ... --require-bound`，再按当前 task/mode 做 semantic load；保存 receipt payload hash 并核验；用同一 fingerprint/scope 执行 `idev-std resolve --response verify`。任何 `SESSION_FINGERPRINT_CHANGED`、`SESSION_SCOPE_CHANGED`、dirty standards、partial 或 hash mismatch 均 fail-closed。
- 状态转换必须是单向可审计的：`running → awaiting_input → running` 可循环；`awaiting_input → done/cancelled/failed` 只能由 end、idle timeout、cancel 或 agent 异常触发。拒绝从终态返回 running；重复 say/end/cancel 必须幂等或返回现有冲突错误。
- 每轮必须有独立 context/deadline；等待输入期间不计入单轮 timeout，idle timer 在进入 awaiting_input 时启动、收到 say 时停止并在下一轮结束后重置。`max_session_sec` 若配置则从会话启动计时，默认不限；超时原因写入事件和结果。
- 所有资源释放路径（end、cancel、idle、agent failure、恢复失败、server shutdown、worker disconnect）必须幂等；等待输入期间页面显示真实解析后的锁路径。不能用“health/build/green scheduler”替代会话行为验收。
- G032 清单：新增 `awaiting_input`、事件名、请求字段按 additive schema 处理；只有确认旧路径仍被实际调用且需要过渡时才保留兼容分支，并写移除标记和版本；没有使用者的旧路径直接删除。最终报告列出新增/保留/删除项。
- 测试禁止真实配置目录 `D:\work\inhere\config\win-env\gofer`，禁止重启/reload 正在运行的 gofer server/worker；所有命令显式指定临时 `--server` 或 `-c`，前端构建使用 `pnpm build --outDir <tools/gofer/tmp/...>`。

## 波次与依赖

T00 实际落点核查 → W1 状态/存储/事件红测 → W2 ACP runner 多轮与 say/end 红测 → W3 单轮/空闲/总时限、锁与配额 → W4 server/worker 恢复 → W5 HTTP/CLI/MCP → W6 Web job 详情与新建 → W7 工作台持续会话文本视图 → W8 文档、质量门和真实进程冒烟。每波先写固定测试使目标行为有效呈红，再实现转绿；按功能点分别 conventional commit；单 Agent 顺序执行。

## 任务

### T00 基线与实际落点核查（只读）

- 文件：Approved 设计；本计划“输入与批准证据”列出的 Go、Web、testutil 文件及其测试。
- 动作：确认当前 job status/terminal 判定、JobResult/事件持久化、ACP client 是否支持复用同一连接、permission handler 如何跨轮、目录锁和 agent semaphore 的 acquire/release 边界、远程 worker start/recover 事件、HTTP/MCP caller 传递、Web SSE/ACP 文本投影和前端构建脚本。记录可复用函数、真实行号、潜在循环依赖及 G032 兼容点。
- 验证：`git log -1 --oneline --decorate`、`git status --short --untracked-files=all`、定向 `rg`/源码读取、graph 覆盖检查；完成标准是 W1–W8 的字段流、owner、调用方向和测试夹具均能指向当前代码。
- 依赖：计划批准和后续明确实施请求；本波不改文件。

### W1 状态、存储与事件先行

- Owner：`internal/job`、jobstore/schema、事件查询和定向测试。
- 固定红测：`TestSessionJobStateMachineTransitions`、`TestSessionJobPersistsAwaitingInputAndTurnMetadata`、`TestSessionJobEmitsTurnLifecycleEvents`。
- 目标：新增持续会话标识、turn 序号、ACP session id、awaiting_input 和恢复检查点的最小持久化；固定事件 `job.turn_started`、`job.turn_ended`、`job.awaiting_input`，轮次分隔和终态原因可查询；旧单轮 job 行为不变。明确 JSON/YAML/数据库字段和迁移是否 additive。
- 验证命令：`go test ./internal/job/... ./internal/jobstore/... -run 'TestSessionJob(StateMachineTransitions|PersistsAwaitingInputAndTurnMetadata|EmitsTurnLifecycleEvents)' -count=1`；按规则记录 exit code、原始 PASS/FAIL 行和 `gofmt -l` 输出。测试必须先有效失败，再实现转绿。
- 完成标准/提交：红测单独提交 `test(job): cover interactive session state and events`；实现转绿后提交 `feat(job): persist interactive session lifecycle`。若 schema 不能 additive 迁移，停在 design/plan Gate。
- 依赖：T00。

### W2 ACP runner 多轮与 say/end

- Owner：`internal/runner/acp`、`internal/acp`、job service 的 runner seam 和 ACP 测试夹具。
- 固定红测：`TestACPSessionRunnerKeepsProcessAcrossTurns`、`TestACPSessionRunnerSeparatesTurnOutput`、`TestSessionJobSayAndEndDriveSameSession`。
- 目标：在一个 ACP 进程内完成 initialize 后首轮 session/new 或 session/load，轮次返回后进入 awaiting_input，say 再发 session/prompt，end 终止并关闭进程；stdout 保留 agent 文本，stderr 保留事件行，`acp.jsonl` 保留全量协议；每轮写明确分隔且审批卡片仍走既有通道。空 prompt 可直接等待。
- 验证命令：`go test ./internal/runner/acp ./internal/job -run 'Test(ACPSessionRunnerKeepsProcessAcrossTurns|ACPSessionRunnerSeparatesTurnOutput|SessionJobSayAndEndDriveSameSession)' -count=1`；补充现有 `go test ./internal/job -run 'ACP|Resume' -count=1` 回归。固定红测必须证明目标缺失而非夹具错误。
- 完成标准/提交：红测提交 `test(acp): cover resident multi-turn protocol`；转绿后提交 `feat(acp): keep session jobs across prompt turns`。不能把 say 实现为 ResumeJob 新建 job；若 ACP client 无法安全复用，停在 Gate 并报告协议事实。
- 依赖：W1。

### W3 单轮/空闲/总时限、目录锁与 agent 配额

- Owner：`internal/job/execute.go`、`dirlock.go`、`concurrency.go`、配置模型、页面投影字段和测试。
- 固定红测：`TestSessionJobTimeoutAppliesPerTurn`、`TestSessionJobIdleTimeoutEndsAwaitingInput`、`TestSessionJobHoldsLockAndAgentSlotWhileAwaitingInput`、`TestSessionJobMaxSessionTimeout`。
- 目标：单轮 timeout 只包住 session/prompt；idle 默认 1800 秒且只在 awaiting_input 计时；可选 max_session_sec 默认不限；等待期间锁和 agent `max_concurrent` 持有，页面能读到解析后的锁路径；end/cancel/timeout/failure 的 release 幂等，取消后不残留 goroutine/slot/lock。
- 验证命令：`go test ./internal/job -run 'TestSessionJob(TimeoutAppliesPerTurn|IdleTimeoutEndsAwaitingInput|HoldsLockAndAgentSlotWhileAwaitingInput|MaxSessionTimeout)' -count=1`；`go test ./internal/job -run 'DirLock|Concurrency|Cancel|Timeout' -count=1`；记录 `gofmt -l` 无输出。使用短测试时长，不把默认 1800 秒真实等待写入测试。
- 完成标准/提交：红测提交 `test(job): cover session deadlines and held resources`；转绿后提交 `feat(job): enforce session timeout and resource lifetime`。若现有 server 超时字段无法表达单轮/idle 分层，先修改设计并等待批准。
- 依赖：W2。

### W4 server 重启、worker 断线与 session/load 恢复

- Owner：job recover/reconcile、worker 路径、ACP capability 检查、恢复测试。
- 固定红测：`TestSessionJobRecoversWithSessionLoad`、`TestSessionJobFailsWhenSessionLoadUnsupported`、`TestSessionJobReleasesResourcesAfterRecoveryFailure`。
- 目标：server 重启或 worker 断线把会话 job 置为 recovering；支持 `session/load` 时重新拉起 agent、load 原 session id 并回到 awaiting_input；不支持时进入 failed，错误提示说明需要新会话；恢复期间不重复占用锁/配额，失败所有资源释放。
- 验证命令：`go test ./internal/job ./internal/worker -run 'TestSessionJob(RecoversWithSessionLoad|FailsWhenSessionLoadUnsupported|ReleasesResourcesAfterRecoveryFailure)' -count=1`；回归 `go test ./internal/job -run 'ACP|Recover|Orphan|Resume' -count=1`。不得重启真实 worker；只用测试 worker/临时 config。
- 完成标准/提交：红测提交 `test(job): cover interactive session recovery`；转绿后提交 `feat(job): recover interactive ACP sessions`。若 worker 协议不提供足够 checkpoint，停在 Gate，不新增无标记旧 worker 回退。
- 依赖：W3。

### W5 HTTP、CLI、MCP 入口与权限

- Owner：`internal/httpapi`、`internal/client`、`internal/commands`、`internal/mcpserver`。
- 固定红测：`TestSessionJobHTTPSTurnAndEnd`、`TestSessionJobCLISayAndEnd`、`TestSessionJobMCPSayAndEnd`、`TestSessionJobSayEndCallerScope`。
- 目标：提供 HTTP/MCP 等价的 say/end 接口和 CLI `job say`/`job end`；CLI 默认显式 `--server` 或临时 `-c`；所有入口共用请求模型、状态码、caller 规则、未知/终态/非会话错误。`job run -a <acp-agent> --session` 提交持续会话并打印可用于 say/end 的 job id；不改变已有 resume 语义。
- 验证命令：`go test ./internal/httpapi ./internal/client ./internal/commands ./internal/mcpserver -run 'TestSessionJob(HTTPSTurnAndEnd|CLISayAndEnd|MCPSayAndEnd|SayEndCallerScope)' -count=1`；`go test ./internal/httpapi ./internal/commands ./internal/mcpserver -run 'Caller|Cancel|Resume|Job' -count=1`。每条 smoke/CLI 命令带临时 `--server http://127.0.0.1:<port>` 或 `-c <temp config>`。
- 完成标准/提交：红测提交 `test(api): cover interactive session command contracts`；转绿后提交 `feat(api): expose interactive session say and end`。HTTP/MCP 若无法保持等价，停在 Gate，不在入口层塞业务逻辑。
- 依赖：W4。

### W6 Web job 详情与新建持续会话

- Owner：job 详情、ACP 日志过程链接、新建 job 表单/API 类型与组件测试。
- 固定红测：`TestJobDetailShowsAwaitingInputAndLockPath`、`TestNewJobSessionModeSubmitsSessionFlag`、`TestJobDetailEndReleasesLock`。
- 目标：新建 job 勾选“持续会话”；详情页显示 running/awaiting_input/recovering/终态、当前轮次、持有锁路径、say 输入、结束会话和“释放锁并结束”；工具调用/思考/审批仍在详情过程区，工作台不重复展开。前端 API 与服务端字段一致。
- 验证命令：`cd web && pnpm test`、`pnpm typecheck`、`pnpm build --outDir ..\\tmp\\web-build-x3-w6`；Go 侧 `go test ./internal/httpapi -run 'TestJobDetail|TestSession' -count=1`。构建目录必须在 `tools/gofer/tmp/`，不得写 `web/dist`。
- 完成标准/提交：红测提交 `test(web): cover session job detail controls`；转绿后提交 `feat(web): add persistent session job controls`。若现有 Web 测试工具链在 Windows 失败，保留原始输出并使用仓库内等效 Win32 命令，不掩盖失败。
- 依赖：W5。

### W7 工作台持续会话文本视图

- Owner：`internal/workbench` API/model、`web/src/views/Workbench.vue`、`components/workbench/*`。
- 固定红测：`TestWorkbenchContinuousACPReusesSessionJob`、`TestWorkbenchContinuousACPProjectsTextOnly`、`TestWorkbenchContinuousACPShowsProcessLink`。
- 目标：ACP 线程增加“持续会话”模式；会话 job 存活时输入直接 say，不再新开 job；保留原“一轮一个 job”续聊。工作台对话流只显示用户消息与 agent 回复文本，过滤工具调用、思考、审批等中间过程，并提供“查看过程”跳转 job 详情；会话结束/失败后按既有模式处理。
- 验证命令：`go test ./internal/workbench ./internal/httpapi -run 'TestWorkbenchContinuousACP(ReusesSessionJob|ProjectsTextOnly|ShowsProcessLink)' -count=1`；`cd web && pnpm test && pnpm typecheck && pnpm build --outDir ..\\tmp\\web-build-x3-w7`。不得用 `t.Skip` 占位。
- 完成标准/提交：红测提交 `test(workbench): cover continuous ACP conversation projection`；转绿后提交 `feat(workbench): reuse persistent ACP session in conversations`。若文本投影无法从现有事件稳定区分 agent 回复与工具/思考，停在 Gate，不把原始 ACP 流直接展示到工作台。
- 依赖：W6。

### W8 文档、质量门与真实进程冒烟

- Owner：README/skill/设计实测记录、`testutil` fake agent（仅在需要时）、集成测试和验证产物。
- 固定红测/冒烟测试：`TestInteractiveACPSessionThreeTurns`、`TestInteractiveACPSessionIdleTimeout`、`TestInteractiveACPSessionCancel`、`TestInteractiveACPSessionRecovery`。
- 真实进程冒烟步骤：
  1. 在 `tools/gofer/tmp/x3-smoke-<id>/` 创建临时 config、项目目录和随机 HTTP 端口；以 `gofer serve -c <temp config>` 启动临时 serve，临时配置将 server 固定到随机 `127.0.0.1:<port>`，不得使用真实配置路径。
  2. 选择一个真实可用的 ACP agent；若主机无可用认证/网络，则使用 `testutil` 最小假 ACP agent，并在证据中明确是假 agent。通过 `gofer job run --server http://127.0.0.1:<port> -a <agent> --session` 创建同一个持续会话 job，记录 job id、agent session id、锁路径和端口。
  3. 发送第一轮消息写入唯一标识，第二轮通过 `gofer job say --server http://127.0.0.1:<port> <id> "请引用第一轮的唯一标识"`，断言 agent 回复含第一轮标识；发送第三轮并断言仍为同一 job/session，`job.turn_started`/`job.turn_ended`/`job.awaiting_input` 顺序完整，stdout/stderr/acp.jsonl 有轮次边界。
  4. 用临时短 `idle_timeout_sec` 等待 `awaiting_input → done`，断言原因是 idle timeout、锁和 agent 名额释放；不要等待默认 1800 秒。
  5. 另起一个会话 job 验证 `cancel`/`end`，断言状态分别为 `cancelled`/`done`、资源释放且 say 不再接受；模拟 server 重启或 worker 断线只针对临时进程，验证支持 `session/load` 的恢复回到 awaiting_input，不支持时为 failed。
  6. 复核工作台只出现用户消息和 agent 回复文本，“查看过程”可跳详情；保存截图/日志到 `tools/gofer/tmp/`，不把临时配置或凭据 stage。
- 验证命令：`go test ./internal/job ./internal/runner/acp ./internal/httpapi ./internal/mcpserver ./internal/workbench -run 'TestInteractiveACPSession(ThreeTurns|IdleTimeout|Cancel|Recovery)' -count=1`（测试函数写在实际 owner 包中）；Windows/Linux `go build` 输出到 `tools/gofer/tmp/`；`go vet ./...`；tracked Go 文件 `gofmt -l`；`git diff --check`；控制字符扫描；Web `pnpm test`、`pnpm typecheck`、`pnpm build --outDir <tools/gofer/tmp/...>`。所有命令必须等待结束并同时记录 exit code 与原始 PASS/FAIL 行。
- 文档：README/README.zh-CN.md（若该仓当前有双语文件，以实际文档结构为准）、`skills/gofer-usage/SKILL.md` 和 Approved 设计的实测记录写明持续会话入口、单轮/idle/max 时限、锁/配额、权限、恢复和真实/假 agent 边界；不要把 smoke 绿灯写成生产部署或业务验收。
- 完成标准/提交：冒烟和质量门全部有原始证据后，按功能点提交 `test(e2e): smoke persistent ACP session lifecycle`、`docs(acp): document interactive session job`（若文档可独立）；最终汇报列出每个 commit、G032 清单、未完成项和人工决策点。
- 依赖：W2–W7。

## 回滚与恢复

每个波次的红测和实现均为独立本地恢复点。每次提交前执行 `git status --short` 与 `git diff --cached --name-only`，只 stage 当前 owner 文件；不得吸收 `.codebase-memory/` 或其他 unrelated dirty work。失败时记录首个失败命令、exit code、原始输出、当前 HEAD 和已验证边界；只对本计划准确 commit 评估 revert。发现协议、权限、状态语义、worker 恢复或工作台展示需要超出 Approved 设计时停止并返回 design/plan Gate，不以改计划文字掩盖语义变化。

## 人工 Gate 与决策点

- 本候选提交后停在人工计划批准 Gate；批准后仍需监督者续接明确实施请求，执行方式为 `DIRECT_CONTINUOUS`。
- 本计划没有新增业务决策点。以下情况必须回到人工 Gate：T00 发现当前 ACP/worker 协议不能支持同一进程多轮或 session/load；需要改变 say/end 权限、旧 wire 语义、状态机终态、工作台过滤规则；无法用 additive schema 表达迁移；真实 ACP agent 不可用且需要将假 agent 作为唯一验收依据；需要重启 live server/worker、修改真实配置、部署、push 或现场动作。
- 假 ACP agent 只在主机没有可用真实 agent 时作为测试夹具，不等价于真实 agent 认证、网络、工具、审批或生产兼容性；真实 agent 冒烟可用性由实施阶段重新确认并记录。

## 可追溯性

| Approved A1 验收 | 任务 | 可观察验证 |
|---|---|---|
| `running ↔ awaiting_input → done/cancelled/failed` 与三类新事件 | W1、W2 | `TestSessionJobStateMachineTransitions`、`TestSessionJobEmitsTurnLifecycleEvents`、事件顺序和终态断言 |
| 同一 ACP 进程多轮、stdout/stderr/acp.jsonl、轮次分隔 | W2、W8 | `TestACPSessionRunnerKeepsProcessAcrossTurns`、`TestInteractiveACPSessionThreeTurns`、临时日志/协议证据 |
| 单轮 timeout、idle timeout 默认 1800、可选 max_session_sec | W3、W8 | 四个 deadline 固定测试、短 idle smoke、原因字段和资源释放断言 |
| 等待输入持续持有锁与 agent 并发名额，页面显示锁路径 | W3、W6 | `TestSessionJobHoldsLockAndAgentSlotWhileAwaitingInput`、详情页测试、锁路径原始输出 |
| server/worker 恢复与 session/load，不支持时 failed | W4、W8 | 三个恢复固定测试、临时 serve/worker recovery smoke |
| say/end、HTTP/MCP/CLI 等价和调用方判定 | W5 | `TestSessionJobHTTPSTurnAndEnd`、`TestSessionJobCLISayAndEnd`、`TestSessionJobMCPSayAndEnd`、权限测试 |
| Web 新建/详情与释放锁结束 | W6 | `TestNewJobSessionModeSubmitsSessionFlag`、`TestJobDetailShowsAwaitingInputAndLockPath` |
| 工作台只显示用户消息和 agent 文本，过程链接跳详情，持续会话复用 job | W7 | 三个 workbench 固定测试、Web 测试和截图 |
| 文档、质量门、真实进程三轮/idle/cancel/recovery | W8 | 原始命令输出、控制字符扫描、临时日志截图、commit/status/log 记录 |

## 完成 Gate 与剩余工作

X3 只有在 W1–W8 的固定测试均从有效 red 转为 green，状态/存储/事件、ACP 多轮、时限、锁/配额、恢复、HTTP/CLI/MCP、Web、工作台、文档和真实进程冒烟都有证据，且每个功能点均已本地 conventional commit 后，才可标记实施完成。计划候选 commit 只代表计划已提交，不代表任何代码、测试、构建、部署、推送或业务验收完成；本期不得 push。
