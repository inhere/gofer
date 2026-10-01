<!-- template_id: plan; template_version: 1.2.0 -->
# 远程 worker 持续会话（Y3）实施计划

> 状态：Approved（Draft 0.2；用户 2026-10-01 在 web 中继批准）；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-01 | Codex | 按已批准 Y3 设计和当前代码核对结果编排协议、worker、server、恢复、页面及真实进程验收 |
| 0.2 | 2026-10-01 | Codex | 采纳监督者裁定：status 携带 turn_no 去重，register.inflight 携带会话状态并在重连时补记 replayed 事件 |

> 仅语义变化递增版本；纯身份、来源或格式修正沿用原版本并在 Git 记录中留痕。

## 目标与完成定义

远程 worker 上的 ACP agent 能以同一个 job 连续对话；server 的状态、事件、超时、token、取消和恢复行为与已批准设计一致。设计列出的九个固定测试和监督者追加的两项回合事件测试均先红后绿；真实进程冒烟证明三轮对话、同进程断线重连、server 重启接管、worker 重启失败并提示 resume、旧协议拒绝和停机进程清理。仅测试、编译或页面可见不单独构成完成。

## 范围、排除项与授权

- `thinking_mode=RIGOROUS`；核心目标仅为设计 Y3。范围冻结为 gofer 单仓内的 wire v13、worker 本地 ACP 会话桥、server 镜像与恢复、现有 Web 工作台和使用文档、隔离的真实进程冒烟。审阅预算：一次现状发现、一次候选核对；只有 CORE_BLOCKING 才追加收敛轮次，达到第 8 轮仍未关闭则停止。
- 排除 Y2 通知、peer-http 持续会话、worker 进程重启后自动恢复旧进程、业务仓修改、真实 server/worker reload、真实配置目录、push、发布或部署。G021 入口只绑定/校验/转发，G022 依赖保持单向；G032 不增无标记兼容分支，旧 worker 只做拒绝提示。
- 本候选提交后停在计划批准 Gate；当前请求仅授权计划文档和本地提交。后续实施须收到新的明确执行请求，使用 DIRECT_CONTINUOUS 逐波推进。真实进程冒烟只在各自临时配置目录、随机端口和临时进程内运行；不触碰生产配置或常驻进程。
- `host_or_non_offline_action=REQUIRED`

## 输入与批准证据

- [已批准设计](../design/2026-10-01-session-notify-and-remote-session-design.md) 的 Y3 节；文档状态注明用户于 2026-10-01 在 web 中继同意待确认事项 1–4。其第 3、4 项分别确认离线 `say` 返回 409、worker 进程重启本期判失败并提示 `gofer job resume`。
- 监督者裁定已写入设计 commit `f3e5067`（当前设计提交为 `f3e50672`）：会话状态不走 `job_event`；v13 `status` 状态一律带 `turn_no`，server 按 `job+turn_no+状态` 去重；`register.inflight` 带每个会话 job 的当前 `turn_no` 和状态，重连时补记漏掉事件并带 `replayed:true`。
- 本次任务明确要求只生成并提交 Y3 计划候选，提交后等待监督者审批；不把设计批准当作实施批准。
- 项目 `AGENTS.md` 的 G021/G022/G032 与本次 gofer 注入约束：仅 `apply_patch` 改文件、测试先写且先单独提交、每功能点提交、禁止 push 和常驻进程操作。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | v13 能力门槛及会话帧 | `wsproto` v12 `Dispatch`/`Status`、`runner/worker.unsupportedDispatchFields`、`wshub` 收帧 | 沿用 envelope、注册版本和拒绝提示 | 扩展原类型与现有收发 switch | OWNER_EXTENSION | v12 无 `session_cmd` 和会话字段 | 不建第二协议包；旧 worker 仅在会话提交时拒绝 |
| CAP-02 | worker 会话执行与幂等 | `worker.handleDispatch`、`job.Service.SaySession/EndSession`、本地事件观察器 | 本地 ACP 状态机及映射表可复用 | 在 worker client 桥接 `cmd_id`、按 job 生命周期清理 | THIN_ADAPTER | 现有 worker 只接 cancel/answer | 去重状态归 worker 进程，终态清除，不引入持久队列 |
| CAP-03 | host 镜像、时限、凭据 | `job.session_state`、`job.execute`、`jobstore.jobtokens`、`wshub.JobSink` | 本机回合状态机、token 延长和 status 收帧可复用 | 扩展 status sink、inflight 快照与 job/runner owner | OWNER_EXTENSION | host 只处理 `started`，远程仍启整 job timer；现有 job_event 身份不适合回合状态 | 会话状态唯一走 status；事件持久化由 server 按 turn 去重，避免第二事件源 |
| CAP-04 | 断线与重启恢复 | RECOV-01 `wshub.recovery`、`job.adopt`、`jobstore.orphanWorkerJobStatuses`、`parkCancel` | 同进程续传、实例校验、接管框架可复用 | 增加会话状态复核、inflight 比对、漏事件补记与 `end` 补投递 | OWNER_EXTENSION | `awaiting_input/pending_interaction` 未待接管，恢复强置 `running`，且没有会话状态补报 | 不建独立恢复服务；命令和回合事件按 job/实例/turn 约束并有界清理 |
| CAP-05 | 页面、文档及冒烟 | `JobDetail.vue`、工作台组件、`README.md`、现有 X3 测试夹具 | 沿用现有会话控件与临时配置测试方式 | 最小显示及隔离真实进程脚本/测试 | THIN_ADAPTER | 远程状态现不可见，真实故障路径未验收 | 不新增 CLI 组、配置系统或冒烟框架 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 前置检查与 fail-closed 条件

### T00 只读现状核对（本候选已完成）

本次基线：Git 根 `D:/work/inhere/hyy-ai-inspect/tools/gofer`，分支 `main`，HEAD `61e71535`；`git status --short` 仅 `?? .codebase-memory/` 与本计划候选，计划文件由本任务持有，`.codebase-memory/` 归属本任务外，均保留不动。图索引 generation 为 2026-09-05，相关路径报告 `metadata_changed/not_tracked`；以下以当前源码和监督者裁定为准。实施启动时重查 HEAD、status、这些路径与所有权，任何他人 dirty 命中先停止。

| 设计「现状」事实 | 当前代码证据 | 结论与影响 |
|---|---|---|
| v12、一次性 dispatch、运行期仅 cancel/answer | `internal/wsproto/frames.go:42,360,515,619,633`；`internal/wsproto/envelope.go:20-70` | 符合。现有另有 `job_event` 等帧，不能把“只有 cancel/answer”解读为完整帧清单；v13 沿用 envelope。 |
| 旧字段能力门槛 | `internal/runner/worker/runner.go:131-173,282-288` | `unsupportedDispatchFields` 已集中判断旧字段，但没有持续会话；提交前要查询已解析 worker 协议版本，不能只在异步 `Run` 拒绝。v13 会话额外使用独立能力门槛。 |
| worker 持有目录锁/agent 名额；host 持有并发槽与信号量 | `internal/worker/dispatch.go:69-182`、`internal/job/execute.go:35-115`、`internal/wshub/registry.go:215-250` | 符合。两边现有 `Run` 生命周期到 Result；会话状态不得提前释放。 |
| host `status` 只消费 `started` | `internal/wshub/hub.go:504-519`；worker 发 `started` 在 `internal/worker/dispatch.go:477-484` | 符合。新增 v13 会话状态须一律带 `turn_no`，按 job+turn_no+状态落 host；当前页面只能看 `running`。 |
| server 重启待接管状态 | `internal/jobstore/jobs.go:750-755` | 只含 `queued/running/recovering`，漏 `awaiting_input/pending_interaction`，需要 W4。 |
| 远程 started 后整 job 超时 | `internal/job/execute.go:186-229` | 符合。会话需保留单轮 worker 限时，host 仅 `max_session_sec+60s` 兜底；未设 max 时不启整 job timer。 |
| token 提交时签发与每轮延期 | `internal/job/submit.go:525-534`、`internal/job/credential.go:125-149`、`internal/jobstore/jobtokens.go:111-117`、`internal/job/session_state.go:59-72` | 远程只按提交 timeout 签发；本机 `beginSessionTurn` 已延期。W3 沿用同一 DB token，不旋转 worker 进程持有的明文。 |
| cancel 暂存补投递 | `internal/wshub/hub.go:799-846`、`internal/wshub/recovery.go:395-417,495-535` | `parkCancel/deliverCancel` 已有，`answer` 离线只报错。W3/W4 复用有界暂存语义处理 `end`。 |
| 远程会话提交拒绝 | `internal/job/submit.go:78-110`、`internal/job/session_job_recovery_x3_test.go:144-149` | 当前拒绝所有 remote（含 peer）；Y3 仅把 worker 改为 v13 条件准入，peer 拒绝仍保留。 |

核对出的恢复风险及裁定处理：`job.outcomes.clearRecovering` 将所有 job 恢复成 `running`（`internal/job/outcomes.go:653-676`），`job.adopt` 依赖持久行重建；因此重连时以 worker `register.inflight` 的会话 `turn_no/status` 复核并恢复等待态。会话状态不进入现有 `job_event` 镜像；server 按 job+turn_no+状态去重，发现持久事件缺口时补记 `replayed:true`。既有普通 `job_event` 队列丢失仍只影响其原有白名单事件，不得用它承载会话回合状态。

实施 preflight：核验设计仍 Approved、计划候选身份与批准范围、独立当前执行请求和 `DIRECT_CONTINUOUS`；`git status --short` 分类 dirty；`bd prime`/关联 issue 状态；Go/Node 与真实 ACP agent 可执行文件及临时目录权限。任一核心依赖、身份、协议行为、临时进程隔离或源文件归属不明即停在相应 Gate；不得接触 `D:/work/inhere/config/win-env/gofer` 或 reload 常驻服务。

## 波次与依赖

| 波次 | 任务 | 依赖 | 固定红/绿测试与波次出口 |
|---|---|---|---|
| W1 协议 v13 | T01 | T00/人工计划批准/当前执行请求 | `TestRemoteSessionDispatchRejectedForOldWorker`；旧 v12 明确拒绝、普通 job 不回退破坏 |
| W2 worker | T02 | W1 | `TestRemoteSessionSayEndRoundTrip`；同一进程跨三轮且重复 `cmd_id` 不重复执行 |
| W3 server | T03 | W2 | `TestRemoteSessionStatusMirrorsTurns`、`TestRemoteSessionTurnEventsDedupByTurnNo`、`TestRemoteSessionSkipsHostWholeJobTimeout`、`TestRemoteSessionTokenExtendedPerTurn`、`TestRemoteSessionSayRejectedWhileWorkerOffline`、`TestRemoteSessionEndParkedAndRedelivered` |
| W4 恢复 | T04 | W3 | `TestRemoteSessionAdoptedAfterServerRestart`、`TestRemoteSessionTurnEventsReplayedOnReconnect`、`TestRemoteSessionFailsOnWorkerRestart`；断线状态复核及补投递 |
| W5 页面和文档 | T05 | W4 | 现有前端类型/构建和 API/页面定向验收 |
| W6 真实进程 | T06 | W5 | 临时 serve/worker/ACP 七项场景证据与无残留进程 |

每波严格先用 `apply_patch` 添加有效断言，运行定向测试取得预期红，**仅提交测试**（`test(y3): ...`）；再实现至绿并按协议、worker、server、恢复、页面各功能点提交（`feat(y3): ...`）。不得用 `t.Skip` 或空骨架。每波记录 commit、原始 exit code/输出及下一恢复点；前波未绿不进入后波。Windows 上 `internal/job`、`internal/httpapi` 整包测试允许后台运行，但必须等待最终结果。

## 任务

### T01 协议与旧 worker 准入（W1）

- 文件/owner：`internal/wsproto/{frames,envelope}.go` 及定向测试；`internal/job/submit.go`、`internal/runner/worker/runner.go` 及定向测试；按需要只在既有 worker 能力查询接口补 seam。
- 动作：增加 `SessionJobMinProtocolVersion=13`、`SupportsSessionJob`，给 `Dispatch` 增 `session/idle_timeout_sec/max_session_sec`，新增 `session_cmd{job_id,cmd_id,action,prompt}`；扩展 `Status` 的会话字段，所有会话状态都必须带 `turn_no`。扩展 `InflightJob`/register 快照，使会话 job 附带当前 `turn_no` 与状态。保留 v12 注册兼容，但会话提交在已解析目标 worker `<13` 或版本未知时同步拒绝并提示升级。peer-http 仍拒绝，非会话 dispatch 仍可落旧 worker。同步修正固定 v12 的协议测试断言。
- 验证：先红后绿运行 `go test ./internal/wsproto ./internal/job ./internal/runner/worker -run 'TestRemoteSessionDispatchRejectedForOldWorker|TestProtocolVersionSplit' -count=1`；再 `go test ./internal/wsproto ./internal/runner/worker -count=1`。检查 JSON round trip、status/inflight 的 `turn_no` 必填语义、旧 worker 拒绝发生在持久 job 行创建前。
- 完成标准：测试展示 v13 字段、帧编码和能力边界；v12 会话提交收到升级说明且无新 job，普通 job 保持原行为。
- 依赖：T00、人工计划批准与新的执行请求。

### T02 worker 本地会话桥（W2）

- 文件/owner：`internal/worker/{client,dispatch}.go`、`internal/worker/*session*_test.go`；只调用 `internal/job/session_commands.go` 现有 `SaySession/EndSession`，不把编排下沉到入口层。
- 动作：将 dispatch 会话字段投影到 worker 本地 `JobRequest`；复用本地 ACP runner 和 host→local job id 映射。按 `(worker instance, job_id, cmd_id)` 幂等处理 `say/end`，只在本地操作成功后记已处理，终态清理；`say` 仅在 awaiting_input 入队，`end` 可在 running/awaiting_input 入队。上报 `turn_started/awaiting_input/session_ending` 的真实本地状态、轮次和 idle deadline；保持日志流和终态 Result 现有路径。
- 验证：先红后绿 `go test ./internal/worker -run '^TestRemoteSessionSayEndRoundTrip$' -count=1`；再 `go test ./internal/worker -run 'TestRemoteSession|TestOutcomeFrameSessionID' -count=1`。
- 完成标准：一个 worker 本地 job 和 agent 进程跨轮存活；重复 cmd 不重复投喂，非法状态拒绝且不假报成功；终态清理映射与去重记录。
- 依赖：T01。

### T03 server 状态、时限、token 与离线命令（W3）

- 文件/owner：`internal/wshub/{hub,recovery}.go`、`internal/runner/worker/runner.go`、`internal/job/{execute,session_state,session_commands,submit,adopt}.go`、`internal/jobstore/jobtokens.go`、`internal/httpapi/session_job_handler.go` 及对应包定向测试。入口只做错误到 HTTP 409 的映射。
- 动作：server 的 live/adopted session sink 按已授权 job/worker/实例接收 status，更新 `running/awaiting_input/session_ending`、轮次与 idle deadline；会话状态不写入 `job_event`。以 `job+turn_no+状态` 做幂等键，每个状态事件只持久化一次；同一轮重复 status 不产生重复事件，状态变化产生对应 `job.turn_started/turn_ended/awaiting_input` 事件并保留 turn_no。跳过 host 整 job timer；仅已设 `max_session_sec` 时以 worker 上限加 60 秒兜底。每轮开始延长原 token 过期时间，失败显式留日志与测试。`say` 先核 server 状态及在线 worker，离线返回 409 且不排队；`end` 和 `cancel` 在离线/写失败时暂存，重连后补投递。整会话不释放已有项目/调用方/agent 信号量或 worker 并发槽。
- 验证：先红后绿 `go test ./internal/job ./internal/httpapi ./internal/wshub ./internal/runner/worker -run 'TestRemoteSessionStatusMirrorsTurns|TestRemoteSessionTurnEventsDedupByTurnNo|TestRemoteSessionSkipsHostWholeJobTimeout|TestRemoteSessionTokenExtendedPerTurn|TestRemoteSessionSayRejectedWhileWorkerOffline|TestRemoteSessionEndParkedAndRedelivered' -count=1`；再受影响包整包测试（可后台，等待退出）。断言 409、无排队 `say`、相同 turn_no/status 只一条事件、max 上限、token 未旋转及整会话占槽。
- 完成标准：host 页面可读到真实轮次/等待状态，事件不因同秒不同回合而丢失，也不因重发而重复；远程会话不被 `timeout_sec` 当整 job 杀死；离线行为与两项用户批准一致。
- 依赖：T02。

### T04 断线、server 重启与 worker 重启（W4）

- 文件/owner：`internal/jobstore/jobs.go`、`internal/job/{adopt,outcomes,interaction}.go`、`internal/wshub/recovery.go`、`internal/worker/{client,dispatch}.go`、`internal/serve/recovery.go` 及现有恢复测试文件。
- 动作：将 worker 的 `awaiting_input/pending_interaction` 纳入 serve 启动待接管；保存并在接管后复核真实会话状态，避免 `clearRecovering` 的 `running` 覆盖等待态。沿 RECOV-01 按 stdout/stderr 偏移续传，同一实例重连时在 `register.inflight` 重新报告每个会话 job 的当前 `turn_no/status`。server 将该快照与已记录状态比较，漏掉的回合事件补记并带 `replayed:true`；相同 job+turn_no+状态不重复写入。`end/cancel` 按当前暂存窗口补投递，保持 cmd_id 幂等。新 instance 判失败，错误明确“worker 重启，会话已结束；可用 gofer job resume”，不自动拉起旧 agent。恢复窗口到期、无法证明同一实例、状态/事件不一致均 fail closed；保护终态不复活。
- 验证：先红后绿 `go test ./internal/job ./internal/jobstore ./internal/wshub ./internal/worker -run 'TestRemoteSessionAdoptedAfterServerRestart|TestRemoteSessionTurnEventsReplayedOnReconnect|TestRemoteSessionFailsOnWorkerRestart' -count=1`；增加同进程断线等待态、`end` 重投、inflight turn_no/status 比对、`replayed:true` 和事件去重断言；再跑受影响包整包测试。
- 完成标准：恢复后 job 仍可 `say` 且轮次连续，`pending_interaction` 不悬挂；漏掉的回合事件恰好补记一次且带 replayed 标记；worker 重启是单一失败终态并有 resume 提示；无重复回合事件、重复命令或泄漏槽位。
- 依赖：T03。

### T05 页面和使用文档（W5）

- 文件/owner：`web/src/views/{JobDetail,NewJob}.vue`、必要时 `web/src/components/workbench/{WorkbenchComposer,ConversationView}.vue`、`web/src/api/types.ts`、`README.md`。这些是预期路径；若实际无需改动则记录复用证据，不造 diff。
- 动作：沿用既有会话按钮和 API，显示远程 `awaiting_input`、轮次、结束中、worker 离线 409、重启失败与 resume 引导；提交表单对旧 worker 显示升级原因。README 说明 v13 门槛、离线 `say/end` 与恢复边界，不写业务信息或真实配置路径。
- 验证：`npm --prefix web run typecheck`（若 package scripts 实际名称不同，先读 `web/package.json` 并用其现有等价命令）；`npm --prefix web run build`；相关页面/API 测试，手动核对三种状态与 409 文案。
- 完成标准：页面与 server 行一致，错误可操作，构建退出码 0，使用文档无错误的自动恢复承诺。
- 依赖：T04。

### T06 隔离的真实进程冒烟（W6）

- 文件/owner：只在 `tmp/` 放临时配置、构建产物与原始日志；若需要可重复脚本，归 `internal/worker` 现有测试夹具或 `scripts/` 单个测试脚本，先证明复用不足，不引入新 runner。冒烟证据不入 Git。
- 动作：构建当前 server/worker 和基线 v12 worker 二进制到 `tmp/`；两进程用各自独立临时配置目录，两个 `GOFER_CONFIG_DIR` 分别指向对应目录，临时 serve 监听 `127.0.0.1:0` 后读取实际随机端口。所有 gofer 命令显式使用 `-c <临时配置>` 或 `--server http://127.0.0.1:<实际端口>`；worker 同时显式给 `--worker-config <临时worker.yaml>`。真实 ACP agent：同 job 三轮对话；断开并恢复 worker 连接后继续；重启临时 serve 后接管并继续；重启临时 worker 后 job 失败且提示 resume；基线 v12 worker 注册后提交 session 被拒并提示升级。最后停止全部临时进程，核验 agent 子进程无残留，记录 PID、exit code、job 事件和日志路径。
- 验证：`go build ./...`、`go vet ./...`、`go test ./internal/wsproto ./internal/worker ./internal/wshub ./internal/job ./internal/jobstore ./internal/httpapi -count=1`、`gofmt -l` 对所有 tracked Go 文件无输出；真实进程记录含每轮同 job/会话标识及恢复前后事件序列。禁止调用常驻 gofer 的 `serve stop`/`worker stop` 或 reload。
- 完成标准：上述全部场景有原始证据且临时端口、配置与 PID 可追溯；无残留 agent 进程。真实 ACP agent 不可用时保留实施状态为未完成，明确缺口。
- 依赖：T05。

## 回滚与恢复

每波先测试 commit 后实现 commit；失败时停在该波，保留已验证提交与原始输出，不重写他人 dirty。回滚只针对本波 owned commit，由人工确认具体目标后操作；不使用 `git reset --hard`、跨仓清理或真实服务回滚。恢复时重核 HEAD、status、规范指纹、已批准候选、当前执行授权与临时进程状态，再从首个未绿测试继续。新路径按 Operational Discovery / Corrective / Semantic Amendment / Ownership Conflict 分类；只有语义、所有权或授权变化才返回评审 Gate。

## 人工 Gate

1. 本候选经监督者批准，并收到单独的“按已批准 Y3 计划以 DIRECT_CONTINUOUS 实施”当前请求，才能执行 W1–W6。
2. 若定向测试证实：会话状态/事件无法复用既有帧保证验收、`end` 暂存需改变持久化语义、或 worker/session 状态需要新外部协议，停止并提交具体差异给设计/计划 Gate；不得在实现中自行扩大范围。
3. 真实 Secret、push、发布、部署、迁移、设备/流量或外部消息分别需覆盖目标的明确外部动作批准；本计划未授权这些动作。本地隔离冒烟不使用真实配置或常驻进程。

## 可追溯性

| 已批准 Y3 验收/决策 | 任务 | 直接验证 |
|---|---|---|
| v13 会话字段/命令/旧 worker 拒绝 | T01、T02 | `TestRemoteSessionDispatchRejectedForOldWorker`、`TestRemoteSessionSayEndRoundTrip`、v12 真实进程拒绝 |
| 回合/等待态与事件一致、按 turn_no 去重和重连补报 | T02、T03、T04、T05 | `TestRemoteSessionStatusMirrorsTurns`、`TestRemoteSessionTurnEventsDedupByTurnNo`、`TestRemoteSessionTurnEventsReplayedOnReconnect`、断线状态复核、页面三轮 |
| host 不按整 job timeout、token 每轮延长、槽位整会话占用 | T03 | `TestRemoteSessionSkipsHostWholeJobTimeout`、`TestRemoteSessionTokenExtendedPerTurn`、容量断言 |
| 离线 say=409；end/cancel 补投递 | T03、T04 | `TestRemoteSessionSayRejectedWhileWorkerOffline`、`TestRemoteSessionEndParkedAndRedelivered` |
| serve 接管、worker 重启失败及 resume | T04、T06 | `TestRemoteSessionAdoptedAfterServerRestart`、`TestRemoteSessionFailsOnWorkerRestart`、真实重启冒烟 |
| 真实 ACP 三轮及无进程泄漏 | T06 | 临时 serve/worker 原始日志、PID 核验 |

## 完成 Gate 与剩余工作

九个设计固定测试加两项监督者裁定测试先红后绿、受影响包/构建/vet/前端检查成功、真实进程七项证据齐全、`gofmt -l` 及控制字符扫描无异常、G032 清单和各功能点 commit/`git status --short` 核对后，才能记录实施完成；部署与外部验收另行授权。任何未绿、未跑或环境缺口以实际状态列出并留在计划进度，不把计划候选自身标为实施完成。
