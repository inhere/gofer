<!-- template_id: plan; template_version: 1.2.0 -->
# web 工作台 W1 会话中枢实施计划

> 状态：Draft 0.1 / 待监督者人工计划批准

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-26 | Codex | 初稿：把 Approved 0.2 的 W1 收敛为测试先行的会话聚合、续接 API、工作台前端、隔离浏览器实测与文档收口 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 规划声明

- `thinking_mode=RIGOROUS`
- `core_objective=在不重写既有 job、session relay、interaction、PTY、日志与轮询能力的前提下，交付可主动发起工作、查看注意力队列并在同一会话继续对话的 /workbench W1`
- `allowed_scope=Approved 0.2 §五 W1，以及为其固定的后端测试、workbench_thread_prefs 加法表、Web 页面与 README/设计实测记录`
- `non_goals=W2 分屏/标签/PWA/Web Push，W3 ACP 结构化对话/改动评审，W4 worktree 生命周期/看板/预览，默认登录落地页调整，以及任何 live server/worker/config 操作`
- `expansion_policy=DEFER_OR_REQUEST`
- `delivery_track=Full`；新增 REST Interface、SQLite Schema 且跨 Go/Vue 多模块。候选不接触生产数据、可按本地提交回滚、无部署/放量，治理暴露度为低，一轮合并轴评审封顶。
- `review_budget=一轮合并 Standards+Spec 评审；只有 CORE_BLOCKING 才修订候选并重审`
- `stop_condition=计划候选验证、精确本地提交与 candidate identity 生成后立即停止；未获计划批准与新的当前执行请求前不进入 T10`

## 目标与完成定义

目标是在 `/workbench` 提供 W1 会话中枢：左侧按项目分组的 thread 列表及状态上卷，顶部常驻 composer 与“等你”队列，主区显示单个 thread 并可处理 interaction、查看终端或日志、继续同一会话；同时提供 `GET/PATCH /v1/workbench/threads` 与 `POST .../turn` 三个后端接口。

完成不是“代码能编译”或“页面能打开”。只有下列结果全部成立才算 W1 完成：

1. 任务书固定的 7 个 thread/turn/prefs/security 测试先红后绿，无 `t.Skip`、无仅骨架实现。
2. thread 聚合、状态、注意力排序、过滤/项目上卷、prefs 与 turn 派发符合本计划锁定的合同，查询无 per-thread N+1。
3. composer 可用临时配置中的假交互 agent 和假 ACP agent 发起 job；ACP 能从底部输入框完成第二轮；待答 permission 可在主区作答。
4. 侧栏、主区、命令面板、最近会话切换与窄屏单面板布局经真实浏览器目视，截图与描述可复核。
5. Go/Web 质量门、Linux 交叉构建、控制字符扫描、文档更新和 Git 边界检查均有原始输出；所有功能点均有独立本地提交，未 push。

## 范围、排除项与授权

范围包括：

- `s:<session_id>` agent thread、`j:<job_id>` 一次性 job thread、`r:<sid>` relay thread 的聚合与读取。
- caller 维度的标题覆盖、已看时间与置顶偏好；唯一新增持久化表为任务书指定的 `workbench_thread_prefs`。
- `blocked > working > review > done > idle` 状态上卷、`stalled` 正交标记、项目计数及按等待时长升序的 attention queue。
- workbench 页面、其专属组件、新 API client/types、导航与路由；复用既有 `AttachTerminal`、`LogTape`、`InteractionCard`、`SessionDrawer` 会话时间线、`streamJob` 和 `createPoller`。
- README 的 Web console 工作台说明，以及 Approved 0.2 设计中的 W1 实测记录。

明确排除：

- W2 的服务端布局树、分屏/标签页、`ctrl+b`、PWA、Web Push、跨设备恢复。
- W3 的 ACP 结构化消息流、工具调用文件跟随、diff/内联评论/评审回灌。
- W4 的 worktree 开关、合并/归档、看板视角、dev server 与隧道预览。
- 新状态机、新通知通道、新前端状态库、新测试框架、decision 的 `project_key` Schema 扩展、旧 server fallback，以及默认 `/` 或登录后的落地页变更。
- push、发布、部署、服务重启/reload、真实 gofer 配置、真实 agent 凭据、真实项目写入、远端 worker、外部消息和硬件动作。

当前请求只授权生成、验证并本地提交本计划候选；不授权实施。计划批准后仍须由本会话收到新的当前执行请求，执行方式已选 `DIRECT_CONTINUOUS`。

- `host_or_non_offline_action=REQUIRED`

该值仅指实施验收必需的本机临时二进制、随机端口 server 与真实浏览器 smoke；不得把它解释为操作正在运行的 gofer、真实配置目录或远端节点。计划审批与后续执行请求覆盖上述隔离 smoke；任何 live 动作仍需另行、逐项批准。

## 输入与批准证据

- 唯一设计依据：[2026-09-26-web-workbench-design.md](../design/2026-09-26-web-workbench-design.md)，状态 `Approved 0.2`，当前基线提交 `2928055`。
- 当前任务书明确限定“本 job 只做 §五 W1”，指定 T1 测试名、T2/T3 范围、验证矩阵、`DIRECT_CONTINUOUS` 执行方式，以及“计划候选提交后停下等待审批”。
- 跟踪项：workspace Beads `h-aii-4smj`（WEB-11 W1 会话中枢实施）；nested gofer Git 根本身没有独立 Beads DB。
- 计划批准可由用户已授权的 gofer 监督者 Claude 代为给出；设计批准、候选提交或 validator PASS 均不自动构成计划批准或实施授权。

workspace baseline（2026-09-26 规划时）：

- Git root：`D:/work/inhere/hyy-ai-inspect/tools/gofer`；branch=`main`；HEAD=`29280554da415a25c55987c6ea961976b3a4624c`。
- `git status --short` 无输出；计划写入前 nested worktree 干净。外层 workspace 的 IDEV-STD 临时交付与 Beads DB 不属于 gofer Git owner。
- Codebase Memory 项目=`gofer`、Verify Tier 2、generation=`2026-09-05T06:59:33Z`。覆盖检查将多条当前路径标为 `metadata_changed/not_tracked`，`web/src/api/types.ts:1-989` 为 `parse_partial`；因此所有 material claim 已用当前 HEAD 源码直读补证，图中“无记录问题”不作完整性证明。
- 已确认复用链：`job.Service.ResumeJob` 负责 cli/ACP 续接；`sessionrelay.Service.Say` 负责 relay 回复；`jobstore` 已持久化 jobs/interactions/events/plans/decisions/agent_sessions；HTTP 的 SEC-01 对新 write route 默认拒绝 job caller。
- 依赖保持现状：Go、SQLite、Vue 3、Vue Router、现有 Web API/SSE/xterm 依赖；不增加 Go module 或 pnpm package。

预期 owner 路径：

- 后端 domain/store：`internal/workbench/`（新）、`internal/jobstore/workbench.go`（新）、`internal/jobstore/workbench_test.go`（新）、`internal/jobstore/store.go`。
- HTTP：`internal/httpapi/workbench_handler.go`（新）、`internal/httpapi/workbench_test.go`（新）、`internal/httpapi/server.go`、`internal/httpapi/jobcredential.go`。
- Web：`web/src/views/Workbench.vue`（新）、`web/src/components/workbench/`（新）、`web/src/api/workbench.ts`（新）、`web/src/api/types.ts`、`web/src/components/SessionDrawer.vue`、`web/src/App.vue`、`web/src/router.ts`。
- 文档：`README.md`、`docs/design/2026-09-26-web-workbench-design.md`、本计划的非语义实施进度记录。

这些 expected paths 是 planning evidence，不是封闭白名单。实施发现新路径时，在下一次 mutation 前按 Operational Discovery / Corrective / Semantic Amendment / Ownership Conflict 分类。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 跨 jobs、relay、interaction、prefs 的 thread 聚合与 turn 选路 | `internal/job`、`internal/jobstore`、`internal/sessionrelay`、`internal/httpapi` | 直接复用 `ResumeJob`、`Say`、现有记录与状态；不复制执行/中继逻辑 | 新建窄 `internal/workbench` domain service，HTTP 只绑定/鉴权/转发 | MINIMAL_NEW_MODULE | 现有 owner 都只负责单域；把聚合塞进 `httpapi` 违反 G021，把 relay 塞进 `job` 会破坏 G022 | 模块只拥有 workbench projection/dispatch；底层生命周期继续由 job/sessionrelay/jobstore 独占 |
| CAP-02 | caller prefs 与无 N+1 的 bounded snapshot | `jobstore.schemaStmts`、`ListJobs`、sessions/interactions/events/plans/decisions bulk readers | 复用同一 SQLite metadata DB、writeMu、unix 秒口径和现有表 | jobstore owner 扩展一个加法表、upsert/list prefs 与一个固定查询数 snapshot | OWNER_EXTENSION | `ListJobs(Session=...)` 可查单链，但逐 thread 调用会形成 N+1，且没有 prefs 表 | 不新增第二数据库/缓存；表可由旧 binary 忽略，删除 workbench 时可单独清理 |
| CAP-03 | 三个 workbench HTTP 接口与 SEC-01 权限 | `buildRouter`、caller context、job credential default-deny、现有 error envelope | 复用 rux/auth/error/status 映射与 caller id | 新 handler + Server 窄 service 字段；job route literal/action 只为清晰错误，不加入 write allowlist | OWNER_EXTENSION | 当前没有 workbench 路由 | 不创建兼容 route；GET 沿用已授权读面，PATCH/turn 仅 user caller |
| CAP-04 | composer 的项目/agent 能力联动与提交 | `getMeta`、`submitJob`、`listPlans/getPlan`、`NewJob.vue` 级联规则 | 复用现有 API、`allowed_agents`、agent `type/interactive/batch` 与 `SubmitJobReq` | workbench composer 只实现 W1 字段和能力标签 | OWNER_EXTENSION | NewJob 是独立全量页面，不能直接嵌入常驻顶栏 | 不复制后端准入；前端过滤只作提示，后端仍是最终裁决者 |
| CAP-05 | 单 thread 的终端、日志、审批和 relay 时间线 | `AttachTerminal`、`LogTape`/`NdjsonTimeline`、`InteractionCard`、`SessionDrawer`、`streamJob` | 原组件和 SSE 直接复用 | `SessionDrawer` 增加 embedded/workbench-turn 适配；新 ThreadPane 仅组合既有组件 | THIN_ADAPTER | 现有组件分散在 JobDetail/Sessions drawer，没有 thread 容器 | 不创建第二套终端、日志解析、interaction card 或 relay renderer |
| CAP-06 | 5s 轮询、快捷键、MRU 与窄屏 | `createPoller`、Vue Router、App 导航、现有响应式 token | 轮询生命周期与路由直接复用 | workbench view 持有页面内 MRU/command palette/单面板状态 | OWNER_EXTENSION | 当前无工作台级键盘调度 | W1 状态仅内存；不提前创建 W2 layout store |
| CAP-07 | 隔离真实浏览器验收 | `gofer-testcmd pty-echo`、`gofer-testcmd acp-fake`、temp config、现有 Web embed | 两个假 agent 与现有 server/browser 直接复用 | 只增加临时 smoke fixture/config，不进正式配置 | DIRECT_REUSE | 无缺口 | 禁止 fallback 到 live server；每条 gofer 命令显式 `-c` 或 `--server` |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| MOD-01 | CAP-01 | `internal/workbench` | `internal/job`、`internal/sessionrelay`、`internal/httpapi`、`internal/core` | 任一单域均不能同时拥有 job chain、relay thread、attention projection 与 dispatch | `httpapi` owner extension 会承载业务聚合；`job` extension 会反向依赖 relay；`core` 是组装 owner，不应承载 projection 规则 | W1 三个 endpoint 与 Web 都需要同一 thread/status/attention 真源 | 唯一 owner 为 `internal/workbench`; 生命周期跟随 workbench API；通过窄 store/job/relay interfaces 调用既有能力 | 若工作台撤销，先移除 HTTP/Web consumer，再删除 module；不得把 projection 复制回 handler |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 新前端 store/router/test framework | CAP-04 | 删除后 Vue refs/computed、现有 Router 与 typecheck/build 已满足 W1；本期没有需要全局持久布局的状态 |
| 新 WebSocket 或第二条 thread SSE | CAP-05 | 删除后 5s `createPoller` + 当前 job 的既有 SSE 已满足刷新；W1 不改流协议 |
| HTTP fallback 到 `/jobs` + `/sessions` 拼装 | CAP-03 | 删除后新 API 是唯一合同；fallback 会形成 G032 未标记兼容分支并掩盖版本不匹配 |
| decision `project_key` 新列 | CAP-02 | 带 `plan_id` 的项目内 decision 可经 `plans.project_key` 归属；全局 decision 没有可信项目，删除该候选可避免伪造归属和扩大 Schema |

## 前置检查与 fail-closed 条件

1. 计划实施前核验批准对象的 document revision、candidate commit、subject path；候选有语义变化则先生成新 revision 并重新审批。
2. 每个 mutating stage 前复核 IDEV-STD fingerprint；核验 Git root=`tools/gofer`、branch/HEAD/status 与 owner，发现用户/其他 Agent dirty 冲突立即停止。
3. T10 前记录 `go version`、`pnpm --version`、当前 focused Go tests 与 `pnpm typecheck` baseline；不得把既有失败算作 W1 失败，也不得顺手修复。
4. 所有 Go 测试数据库、result/config 路径使用 `t.TempDir()`；不得读取或写入 `D:/work/inhere/config/win-env/gofer`。
5. smoke 必须清除子进程继承的真实 gofer 地址/token/config 环境，使用 repo `tmp/` 下独立目录、临时 DB/storage、随机监听端口；启动前断言配置路径与 live 配置不相等。
6. 新增 Interface/Schema/状态、改变 thread id/seen/turn 语义、需要第三方依赖、触碰 W2-W4 或 live lifecycle 动作，均为 Semantic Amendment，停止并回到 design/plan Gate。
7. 仅对本次改过的 Go 文件运行并要求 `gofmt -l` 无输出；仓库已知 8 个历史 gofmt 脏文件与 `internal/worker.TestPolicyCacheRoundTrip` 的 Windows 0600 基线不在本计划修复范围。

## W1 接口与状态锁定

### Thread identity 与窗口

- 有非空 `session_id` 的 job 按 `s:<session_id>` 分组；一次性无 session job 为 `j:<job_id>`；relay registry session 为 `r:<sid>`。
- 默认 `since=now-7d`（Unix 秒）；所有非终态 job 无条件纳入。若一个 session 中任一 job 命中窗口/非终态，snapshot 必须取回该 session 的完整 job 链，保证首轮标题与 turns 不丢失。
- relay 同理纳入窗口内或未 ended 的 session。`since` 非法返回 400，不静默改成另一个窗口。
- 新 ACP job 在 session id 尚未捕获时可短暂显示 `j:<job_id>`；响应携带 `job_ids/latest_job_id`，前端以提交 job id 在后续 poll 中重定位到 `s:` thread，保持选中态。prefs 读取以 canonical id 优先，并允许首 job 的 provisional `j:` prefs 作为同链 fallback，避免捕获 session 后立即丢标题/已看/置顶；不增加永久 alias 表。

### Status、stalled 与 attention

- thread status 严格按 `blocked > working > review > done > idle` 决定；`stalled` 是独立布尔值，不创造第六状态。
- 任一链上 active job 有 pending interaction，或 relay 为 `waiting_reply/needs_attention`，即 `blocked`；waiting time 取最早尚待处理项的创建/询问时间。
- 最新 job 为 `queued/running/waiting_dir/recovering` 时为 `working`；任务书点名的 `running/waiting_dir` 必须有回归测试。
- 最新 job 为 `needs_review`，或为 terminal 且 `seen_at < terminal_wait_at`，即 `review`；terminal 且已看为 `done`。`seen=true` 只使 terminal review 变 done，不越过真正的 `needs_review` 人工验收。
- relay 存活且无等待项为 `idle`；ended relay 不进入默认窗口，显式 since 命中时以 `done` 展示并保留 raw state。
- `stalled=true` 仅当链上仍为 running 的 job 已有 `job.stalled` 事件；历史已结束 stall 不污染后续正常 turn。
- attention queue 每个 blocked/review thread 一项，按 `waiting_since ASC, thread_id ASC`；action 为 `answer|review|reply`，并带 thread id 及 interaction/job/session 的精确定位字段。
- 项目上卷使用相同优先级与计数。`plans.status=blocked` 和 OPEN、非 relay、且能经 `plan_id -> plans.project_key` 确定归属的 decision 计入 orphan attention；无可信项目的 global decision 不伪造归属。needs_review job 本身仍属于 `s:`/`j:` thread，不重复计数。

### HTTP contract

- `GET /v1/workbench/threads?project=&status=&q=&since=` 返回 `{projects, attention, total, since}`；project 内含 rollup status/counts 和排序后的 threads。`q` 对 title/thread id/agent/cwd 做不区分 ASCII 大小写的 literal substring；`project/status/q` 过滤后再生成响应计数，orphan attention 仅在所属 project 仍命中时加入。
- thread 至少返回：id/kind、status/stalled/raw_status、title、project/agent/runner/cwd、interactive、pinned/seen_at、started/updated/waiting time、turns、usage 合计、job ids/latest job、pending interaction 定位与 relay 摘要。title 取首轮显式 title，否则取首轮 prompt；按 Unicode rune 截前 30 字；caller title override 优先。
- `PATCH /v1/workbench/threads/{id}` 接受可选 `{title, seen, pinned}`；`seen=true` 写当前 Unix 秒，`seen=false` 清零，空 title 清除 caller override；未知/非法 thread 为 404/400。表固定为 `workbench_thread_prefs(caller_id, thread_id, title, seen_at, pinned)`，主键 `(caller_id, thread_id)`。
- `POST /v1/workbench/threads/{id}/turn {text}`：`s:` 找链上最新 job 并调用 `ResumeJob(prompt=text)`；`r:` 调 `sessionrelay.Say`；`j:` 返回 409 和“该一次性批处理没有 session，无法续接”的可操作提示。成功至少返回新 `job_id` 或 relay decision id。
- GET 对任一已认证 caller 可读；PATCH/turn 仅 user caller。SEC-01 保持 default-deny：不得把 workbench write route 加入 job allowlist，`TestJobCallerCannotTurn` 固定 403。
- snapshot 是一个 store/service 调用，内部使用固定数量 bulk SQL/CTE；禁止 thread 循环内调用 `ListJobs(Session)`、`ListInteractions(job)`、`ListJobEvents(job)` 或 prefs query。

## 波次与依赖

| 波次 | 内容 | 依赖 | 预期提交 |
|---|---|---|---|
| W0 | T00 计划批准、基线与 owner 复核 | 本候选获批 + 新的当前执行请求 | 无代码提交 |
| W1 | T10 固定名 red tests | W0 | `test(web-11): specify workbench W1 contracts` |
| W2 | T20 store snapshot/prefs；T21 domain；T22 HTTP/security | W1 red 证据 | `feat(web-11): add workbench thread model`；`feat(web-11): expose workbench thread APIs` |
| W3 | T30 Web API、侧栏、attention、composer | W2 green | `feat(web-11): add workbench thread launcher` |
| W4 | T31 主区、续接、快捷键与窄屏 | W3 | `feat(web-11): add workbench conversation view` |
| W5 | T40 文档；T41 全门；T42 隔离浏览器 smoke；T43 收口 | W4 | `docs(web-11): document workbench W1`；必要时仅 owner 内 corrective commit |

## 任务

### T00 复核 candidate、基线与执行边界

- 文件: 无业务文件；只读本计划、设计、AGENTS、Git/Beads/IDEV-STD 状态。
- 动作: 核验批准 candidate tuple、`DIRECT_CONTINUOUS` 当前执行请求、Git 根/branch/HEAD/dirty owner；记录 Go/pnpm 与 focused test baseline；确认 live gofer 进程和真实配置只作为禁止触碰目标，不读取 secret 内容。
- 验证: `git status --short`；`go version`；`cd web && pnpm --version && pnpm typecheck`；现有相关 Go tests。命令同时记录 exit code 与原始 PASS/FAIL 行。
- 完成标准: approved candidate 与执行请求均明确，工作树可安全分阶段提交，基线失败已分离；否则停止。
- 依赖: 监督者计划批准 + 用户续接本会话实施。

### T10 先提交可编译的 red contract tests

- 文件: `internal/httpapi/workbench_test.go`（新）；如表级断言需要复用，可同时新增 `internal/jobstore/workbench_test.go`，但不得创建 production stub。
- 动作: 用 `httptest`、`t.TempDir()` 与现有 fake runner/session helper 写固定名测试：`TestThreadsGroupJobsBySession`、`TestThreadsStatusPrecedence`、`TestThreadsAttentionQueueOrder`、`TestThreadsFilterAndRollup`、`TestTurnDispatchesByThreadKind`、`TestThreadPatchRenameAndSeen`、`TestJobCallerCannotTurn`。测试以 HTTP JSON/状态码观察合同；时间戳显式固定，避免 sleep 排序。
- 验证: `go test ./internal/httpapi -run 'TestThreads(GroupJobsBySession|StatusPrecedence|AttentionQueueOrder|FilterAndRollup)|TestTurnDispatchesByThreadKind|TestThreadPatchRenameAndSeen|TestJobCallerCannotTurn' -count=1` 必须因 endpoint/行为缺失而 FAIL，测试本身必须编译；贴原始 FAIL 行。`rg -n 't\.Skip'` 对新测试无命中。
- 完成标准: 七个测试逐一覆盖任务书断言；失败边界只指向未实现 W1；精确暂存测试文件并提交 `test(web-11): specify workbench W1 contracts`。
- 依赖: T00。

### T20 扩展 jobstore snapshot 与 thread prefs

- 文件: `internal/jobstore/store.go`、`internal/jobstore/workbench.go`（新）、`internal/jobstore/workbench_test.go`（新/扩展）。
- 动作: 在 `schemaStmts` 增加任务书指定 prefs 表与必要索引；实现 caller prefs upsert/get-bulk。实现 bounded snapshot：CTE 先选窗口内或非终态 thread key，再取其完整 jobs；批量联接 pending interactions、`job.stalled` events、relay sessions/open relay decisions、blocked plans/open plan decisions和 caller prefs。可以使用固定数量查询，但查询数不得随 thread/job 数增长。
- 验证: fresh DB 和模拟旧 DB `Open` 均出现表；prefs roundtrip/隔离/clear；窗口包含完整链；query 结果不重复计数；`go test ./internal/jobstore -run 'Workbench|Migrate' -count=1`。
- 完成标准: store 返回中性 snapshot，不 import `internal/job`/`sessionrelay`，SQLite 写继续走 `writeMu`，无 destructive migration。
- 依赖: T10。

### T21 建立唯一 workbench domain owner

- 文件: `internal/workbench/model.go`、`internal/workbench/service.go`、`internal/workbench/service_test.go`（均新）。
- 动作: 通过窄 Store/Jobs/Relay interfaces 消费 T20 snapshot 与既有 `ResumeJob`/`Say`；实现 thread id 解析、完整链分组、首轮标题、usage 合计、状态/`stalled`、filters、项目 rollup、attention 排序、prefs 合并和 turn 选路。`j:` 不做“猜 session”兼容；provisional prefs fallback 仅属于同一首 job→session canonicalization。
- 验证: domain table tests 覆盖 Unicode 30 字、状态优先级、等待排序、session 完整链、prefs caller 隔离、`s/r/j` dispatch 与错误映射；`go test ./internal/workbench -count=1`。
- 完成标准: 所有 projection 语义只有一个 owner；module 不 import `httpapi/core/serve`，不复制 job 或 relay 状态机。
- 依赖: T20。

### T22 暴露 HTTP API 并固定权限

- 文件: `internal/httpapi/workbench_handler.go`（新）、`internal/httpapi/server.go`、`internal/httpapi/jobcredential.go`、`internal/httpapi/workbench_test.go`。
- 动作: Server 在现有 jobs/relay/store 就绪后构造 workbench service；注册 GET/PATCH/turn；handler 只解析 query/body、取得 caller、执行 user/owner Gate、转发并映射标准 error envelope。为 route collapse 增加 `workbench/threads/turn` literal 和拒绝动作文案，但绝不加入 `jobWriteAllowlist`。relay turn 复用现有 session owner/admin 检查。
- 验证: T10 七项从红变绿；补非法 since/id/body、worker/job write 403、unknown 404、one-shot 409 和无 service 503；`go test ./internal/httpapi ./internal/jobstore ./internal/workbench -count=1`。
- 完成标准: 三端点合同与 SEC-01 固定；handler 无 thread 聚合业务；精确提交 backend domain/store 与 HTTP 两个功能点。
- 依赖: T20、T21。

### T30 实现 Web 数据层、侧栏、attention 与 composer

- 文件: `web/src/api/types.ts`、`web/src/api/workbench.ts`（新）、`web/src/views/Workbench.vue`（新）、`web/src/components/workbench/WorkbenchSidebar.vue`、`WorkbenchAttention.vue`、`WorkbenchComposer.vue`（新）、`web/src/App.vue`、`web/src/router.ts`。
- 动作: 定义精确 thread/project/attention/prefs/turn types 与 API；Workbench 用 `createPoller(loadThreads, 5000)`，卸载时 stop。侧栏按项目折叠，显示 rollup/count/status/title/agent/相对时间/usage/pin，`/` 聚焦搜索。attention 点击 thread 并带 action anchor。composer 复用 `getMeta`、project `allowed_agents`、agent `type/interactive/batch`、`listPlans/getPlan` 和 `submitJob`，提供 project/agent 能力标签、模式、可选 plan todo、cwd、多行 prompt、Ctrl/Cmd+Enter；提交后按 job id 重定位 thread 并选中。
- 验证: `cd web && pnpm typecheck`；浏览器 API error 可见而不清空已有列表；poller 在 blur/hidden 停止、focus 恢复立即刷新。
- 完成标准: `/workbench` 可发起 W1 三种模式并看到新 thread；导航在“观察”组首位，`/` 默认 redirect 仍为 `/dashboard`。
- 依赖: T22。

### T31 实现单 thread 主区、续接、键盘与窄屏

- 文件: `web/src/components/workbench/WorkbenchThreadPane.vue`、`WorkbenchCommandPalette.vue`（新）、`web/src/components/SessionDrawer.vue`、`web/src/views/Workbench.vue`；如拆分只允许同目录内职责明确的子组件并记录 Operational Discovery。
- 动作: 主区 header 提供可重命名标题、status/stalled、agent、project/cwd、job turns、usage、二次确认停止、打开最新 job。内容按 latest job：interactive→`AttachTerminal`，ACP/批处理→`streamJob` + `LogTape`（W1 不创建结构化 chat），relay→`SessionDrawer` embedded 模式复用时间线；pending interactions 在内容上方用 `InteractionCard` 内联作答。非终端底部统一调 workbench turn，`j:` 显示 409 原因并禁用。
- 动作: 选中 terminal review 后 PATCH seen；支持 pin。`ctrl+k` 面板实现跳 thread/新会话/切 project/当前停止、续接、详情；`ctrl+tab`/`ctrl+shift+tab` 只维护本页 MRU；`esc` 从终端取回焦点；输入控件内不劫持 `/` 或快捷键。窄屏一次只呈现 sidebar/main，以返回按钮切换；composer 与 attention 在两态都可达。
- 验证: `cd web && pnpm typecheck && pnpm build`；SSE status/log/interaction 更新后触发一次 threads reconciliation；组件卸载关闭 SSE、poller 与全局 listener。
- 完成标准: 选中 thread 能观察、作答、停止、打开详情和继续同一 session；快捷键与窄屏不依赖 W2 layout state。
- 依赖: T30。

### T40 更新用户文档与 W1 实测记录

- 文件: `README.md`、`docs/design/2026-09-26-web-workbench-design.md`、本计划实施进度段。
- 动作: README Web console 增加“工作台”：能力、thread 定义/三种 id、状态与快捷键。设计追加“W1 实测记录”，只写提交、测试、isolated smoke、截图描述和限制；这是 progress/provenance，不改变 Approved 0.2 的设计语义，不递增 revision、不重开 review。计划 progress 同样只记录 task/commit/validation/lifecycle 状态。
- 验证: 链接检查、IDEV-STD document validator、`git diff --check`。
- 完成标准: 用户可据 README 使用 W1；文档不宣称 W2-W4、部署或 live 验收完成。
- 依赖: T31、T42 的实际证据；可先起草，最终数据只能在 smoke 后填写。

### T41 执行代码与文档质量门

- 文件: 全部 owned changed files；验证产物写 `tmp/web11-w1/`，不提交临时产物。
- 动作: 对改过的 Go 文件 `gofmt -w` 后用同一精确清单跑 `gofmt -l`；依次 build/vet/test/Web gate。Windows 上慢包可后台执行，但必须等待最终 exit code 和输出后再汇报。
- 验证: `go build ./...`；PowerShell 子进程设置 `GOOS=linux` 后 `go build ./...` 并恢复环境；`go vet ./...`；`go test ./internal/workbench/ ./internal/httpapi/ ./internal/jobstore/ ./internal/job/ -count=1`；`cd web && pnpm typecheck && pnpm build`；`git diff --check`；对 changed text 运行 `rg -n --pcre2 '[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]'`。
- 完成标准: build/vet/test/typecheck/build/diff-check exit code=0；`gofmt -l` exit code=0 且无输出；控制字符 `rg` 的原生 exit code=1 且无输出表示无匹配，exit code=0 表示发现控制字符并失败。测试报告保留原始 PASS/FAIL 行。任何 FAIL 均先定位新增/基线归属，不能只写“通过”。
- 依赖: T22、T31、T40。

### T42 用临时 server 和真实浏览器完成 W1 smoke

- 文件: `tmp/web11-w1/<run-id>/` 下的临时 config、DB、logs、截图；正式配置零改动。
- 动作: 构建 `gofer` 与 `gofer-testcmd` 到该临时目录；配置一个以 `pty-echo` 为 `interactive_args` 的假交互 cli-agent、一个 `acp-fake` ACP agent（脚本化 permission）和独立假 project。动态选择空闲 loopback 端口，隐藏窗口启动临时 serve，保存准确 PID，结束时只终止该 PID。
- 动作: 子进程环境显式清除 `GOFER_SERVER_ADDR/GOFER_TOKEN/GOFER_CONFIG/GOFER_CONFIG_DIR` 等真实 gofer 入口。每条 gofer CLI 都显式 `-c <temp-config>` 或 `--server http://127.0.0.1:<port>`；任何未显式绑定的双模式命令禁止执行。
- 验证: 浏览器依次完成：composer 发起假交互 agent 并 attach；发起假 ACP；fake ACP 制造 permission，“等你”出现、点击定位并在主区作答；ACP 完成后用底部输入发送第二轮并确认同一 `s:` thread turns 增长；验证重命名/seen/pin/停止确认/详情；验证 `ctrl+k`、正反向 `ctrl+tab`、`/`、终端 `esc`；窄屏确认 sidebar/main 返回切换且 composer/attention 可达。每个关键画面截图并在报告用可观察内容描述，不只写“目视通过”。
- 完成标准: 所有场景只命中随机端口/temp DB/temp project；临时进程已按 PID退出；未重启/reload live server/worker，未读取或修改真实配置。
- 依赖: T41 build/typecheck/test 通过。

### T43 原子提交、跟踪与最终交付检查

- 文件: 每个功能点的 exact owner files、Beads `h-aii-4smj` progress；不修改其他 dirty 文件。
- 动作: 每次提交前 `git status --short`，精确 add，核对 `git diff --cached --name-only`，使用 conventional commit；不得 hunk-stage 绕过 ownership。最终记录各任务状态/一句说明/hash、原始验证输出、截图描述、`git log --oneline`、`git status --short`、G032 清单、未完成/人工决策。
- 验证: `git log --oneline <baseline>..HEAD`；`git status --short`；若有 upstream，仅用只读状态说明，不 pull/rebase/push。
- 完成标准: 所有 owned 变更已分功能点本地提交，nested worktree 无未归属变更，Beads 记录与 Git 一致；严格不 push。
- 依赖: T40、T41、T42。

## 回滚与恢复

- 提交序列保持 test contract、store/domain、HTTP、Web launcher、Web conversation、docs 六类原子边界。回滚使用针对性 `git revert <commit>` 方案，不使用 `reset --hard`、`checkout --` 或 `clean`。
- SQLite 变更仅为 `CREATE TABLE IF NOT EXISTS workbench_thread_prefs`；旧 binary 会忽略该表。代码回滚不删表、不丢 caller prefs；如未来确认废弃，另立数据清理授权。
- Web 若失败，可先回滚 Workbench 导航/route 与两个 Web 功能提交，后端新 GET/write routes 不会被旧前端调用；不得增加长期兼容 fallback。
- smoke 恢复点为 temp config、随机端口、PID 与截图目录。只停止已记录的临时 PID；目标不匹配即停止，不做递归广域清理。
- 遇到 BLOCKED/PAUSED/FAILED_TERMINAL 时，交付统一 6 行中断卡：状态、已完成、当前/已验证、未完成、首个失败边界/影响、唯一下一步；详细输出只给 `tmp/web11-w1/` 路径。

## 人工 Gate

1. 上游设计 Gate：`Approved 0.2` 已满足，只覆盖 §五 W1。
2. 当前 Plan Gate：本文件为 `Draft 0.1` 候选，提交后必须停止；由已授权监督者给出明确批准，例如“批准该 W1 计划，按 DIRECT_CONTINUOUS 连续实施”。
3. Execution Gate：计划批准后，仍需用户续接本会话形成新的当前执行请求；仅批准计划或选择 DIRECT_CONTINUOUS 不会自动开始实施。
4. Host action Gate：批准计划并请求执行后，只允许 T42 的本机临时 server/test binary/真实浏览器；push、release、deploy、live restart/reload、真实 config、远端 worker、traffic、外部消息和硬件动作仍未授权。
5. Semantic Amendment Gate：新增/改变协议、thread id/status/seen 语义、Schema 超出 prefs 表、第三方依赖、W2-W4 或真实环境验收时停止并回到 design/plan review。

## 可追溯性

| 目标/验收 | 设计/任务书来源 | 任务 | 验证 |
|---|---|---|---|
| session job 链、一次性 job、relay thread 与首轮标题 | 设计 §二；T1 Group | T10,T20,T21,T22 | `TestThreadsGroupJobsBySession`、完整链 snapshot test |
| 状态优先级、seen 与 stalled | 设计 §二/§三.2；T1 Status/Patch | T10,T20,T21,T22,T31 | `TestThreadsStatusPrecedence`、`TestThreadPatchRenameAndSeen`、SSE/poll smoke |
| “等你”排序、跳转与项目上卷 | 设计 §三.2；T1 Queue/Filter | T10,T20,T21,T30 | `TestThreadsAttentionQueueOrder`、`TestThreadsFilterAndRollup`、permission smoke |
| acp/cli resume、relay say、one-shot 409 | 设计 §三.3/§四；T1 Turn | T10,T21,T22,T31 | `TestTurnDispatchesByThreadKind`、ACP 第二轮 smoke |
| per-caller rename/seen/pin 与 SEC-01 | 设计决策 5；T1 Patch/Security | T10,T20,T22,T31 | prefs roundtrip、`TestThreadPatchRenameAndSeen`、`TestJobCallerCannotTurn` |
| composer 主动发起与能力标注 | 设计 §三.1；T2 Web | T30,T42 | typecheck/build；假 interactive + ACP browser smoke |
| 主区终端/日志/relay/审批/停止/详情 | 设计 §三.2/3/6/7；T2 Web | T31,T42 | existing component integration；browser smoke/screenshots |
| 5s 轮询、命令面板、MRU、窄屏 | 设计 §三.6/8；T2 Web | T30,T31,T42 | poller lifecycle、keyboard/narrow browser smoke |
| 文档、质量、隔离与无 live 副作用 | T3 与验证矩阵 | T40,T41,T42,T43 | validator、build/vet/tests、control scan、PID/config/port evidence |

## 完成 Gate 与剩余工作

W1 只有在 T10–T43 全部完成、七个固定测试与完整验证矩阵均有最终结果、真实浏览器场景和截图描述齐全、所有功能点已原子本地提交、`git status --short` 无未归属变更且确认未 push 时才能标记完成。green scheduler/build/health 或局部单测都不能替代该完成 Gate。

G032 预期清单：

- 新增兼容分支：`NONE`。
- 新增 `// DEPRECATED(vX): remove in vY`：`NONE`。
- 删除的旧路径：`NONE`（W1 不改现有 route/field）。
- additive schema：仅 `workbench_thread_prefs`；这是当前功能真源，不是兼容层。
- 若实施发现需要旧 server fallback、永久 alias、wire 字段容忍或其他兼容分支，必须停止并作为 Semantic Amendment 报告，不能静默加入。

剩余但明确不属于 W1：W2 布局/PWA/Web Push、W3 ACP 结构化对话与评审、W4 worktree/看板/预览，以及登录默认落地页是否改为 Workbench；这些只能在 W1 上线试用反馈后另行设计/计划/授权。

## 实施进度（不改变 Draft 0.1 计划语义）

| 任务 | 状态 | 提交/证据 |
|---|---|---|
| T00 | PASS | SUPMODE preflight；实施前 baseline 已分类 |
| T10 | PASS | `af0d136`，固定 7 项测试先 RED |
| T20 | PASS | `f4b018d`、`0bc1891` |
| T21 | PASS | `ef96efc` |
| T22 | PASS | `c4745f8`，固定 HTTP/SEC-01 tests GREEN |
| T30 | PASS | `22f214e`，Vue typecheck PASS |
| T31 | SOURCE_PASS | `7c8ab03`；Vue typecheck/Vite build PASS，真实浏览器视觉见 T42 |
| B01（用户追加） | PASS | `3cd0b4c`；`TestResumeOfResumeUsesOriginAgent` 先 RED 后 GREEN |
| T41 | PARTIAL | build/vet/Web PASS；四包中三包 PASS，`internal/job` 两项实施前 baseline FAIL |
| T42 | PARTIAL | 隔离 runtime API smoke PASS；三种浏览器 provider 均无法启动，visual/screenshot NOT_RUN |
| T43 | IN_PROGRESS | 待文档提交、最终状态/日志/控制字符与 lifecycle 检查 |

以上是 progress/provenance，不改变批准 candidate `1b21a18 / revision 0.1`，也不重开计划审批。Browser Host Gate 与两项非 W1 baseline 未关闭前，不把 W1 标记为完整外部验收。
