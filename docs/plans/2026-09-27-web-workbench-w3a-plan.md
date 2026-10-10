<!-- template_id: plan; template_version: 1.2.0 -->
# web 工作台 W3a ACP 结构化流与对话视图实施计划

> 状态：Draft 0.1 / 待人工计划批准

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-27 | Codex | 初稿：把 Approved identity 0.4 的 W3 拆出 W3a，只实施 ACP prompt/message 落盘、结构化 SSE 与按 job 链渲染的对话视图；diff 与行内评审留待 W3b |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 规划声明

- `thinking_mode=RIGOROUS`
- `core_objective=在不改变现有 stdout、thread、turn 与审批权威链路的前提下，使 ACP 每轮具备可重放的 prompt/message 记录、可尾读的归一化结构流，以及按 job 链呈现的安全对话视图`
- `allowed_scope=Approved identity 0.4 的“W3 细化”中“现状”“ACP 落盘补齐”“结构化流”“对话视图”四节，以及任务书固定测试、质量门、README 和可选实测记录`
- `non_goals=改动视图、GET /v1/workbench/threads/{id}/diff、POST /review、行内评论、文件跳转、W4 worktree/看板/预览、部署、push、live gofer server/worker 与真实配置`
- `expansion_policy=DEFER_OR_REQUEST`
- `delivery_track=Full`：新增一个 authenticated SSE Interface，修改 runner 持久化合同，并跨 Go runner/streaming/httpapi 与 Vue 工作台多个 owner；候选不改数据库、Secret、部署或生产流量，全部 source 变更可按原子提交回滚，治理暴露度为低，一轮合并 Standards+Spec 评审封顶。
- `review_budget=一轮 discovery、一轮 confirmation；只有 CORE_BLOCKING、Semantic Amendment 或 Ownership Conflict 才修订候选并重新进入评审`
- `stop_condition=计划 validator PASS、精确本地提交并生成 candidate identity 后立即停止；监督者批准计划且用户续接本会话形成新的当前执行请求之前，不进入 W3A-T00`

## 目标与完成定义

目标是把 ACP 会话的“过程”从最近 200 行 stdout/stderr 日志升级为按轮次重放的结构化对话，同时保持非 ACP 会话、终端会话和底部续接输入框的既有行为不变。

完成不是“新增了 route”“页面能打开”或“单个 reducer 测试通过”。只有下列结果全部成立，才算 W3a source/runtime 完成：

1. `TestACPRunnerRecordsPromptAndMessages` 先 RED 后 GREEN：每轮 prompt 是首条业务记录，按 Unicode 字符安全截到 8000 字并带 `truncated:true`；tool/thought/plan/stop 与 2 秒静默边界能落 message；消息按顺序拼接后与 stdout.log 的正文一致；stop 前残余消息先落盘。
2. `TestACPStreamEmitsStructuredEvents` 先 RED 后 GREEN：手写 acp.jsonl 能得到稳定的 prompt/message/thought/tool/permission/plan/usage/stop 序列；连续 thought 合并；同一 tool id 首条与状态更新均发送；已批准噪声被丢弃；旧作业 stdout 兜底插在 stop 前；`tail=2` 先发带 `skipped` 的 truncated；终态以 SSE `end` 收口；job credential 的 GET 权限与现有 `/stream` 一致。
3. 运行中 ACP 作业按 `streaming.StreamPollInterval` 跟随 acp.jsonl 增长；每个归一事件有连接内单调 `seq`；单事件超过 `MaxSSEFrameBytes` 时只截文本字段并显式 `truncated:true`，不发送超限帧，也不把畸形/未知记录冒充业务事件。
4. `acpEvents.test.ts` 先 RED 后 GREEN：tool 按 `tool_call_id` 在原位置覆盖、相邻 thought 合并、truncated 保留省略信息、多个 job 严格按 thread 的 `job_ids` 顺序分轮；纯函数不依赖 Vue/DOM/网络。
5. ACP thread 的过程区默认显示最近 3 轮，可每次加载更早 3 轮；历史终态轮次读完即关，只有运行中的最新一轮保持 SSE；prompt、默认折叠 thought、可展开工具卡、路径文本、Markdown 助手消息、permission/plan/usage/stop 与轮次结束均可区分。
6. 待答 interaction 使用既有 `InteractionCard` 内联在最新轮次末尾，回答仍走现有 answer/punt API；底部 turn 仍走 `POST /v1/workbench/threads/{id}/turn`。非 ACP 的 cli/批处理仍是 LogTape 最近 200 行，PTY 与 relay 路径不变。
7. ACP 判定只复用 W1 的 `MetaAgent.type === "acp-agent"`；不按 agent 名称后缀猜测。thread 已有且继续直接复用按轮次排序的 `job_ids`，不增加平行 thread 查询、兼容字段或 localStorage 会话真源。
8. 改过的 Go 文件 `gofmt -l` 无输出；Windows/Linux build、vet、指定 Go 包、Web test/typecheck/build、diff/编码/控制字符检查均有 exit code 与原始 PASS/FAIL 行。测试、runner、streaming、HTTP、前端 reducer、对话 UI、文档各按功能点独立本地提交；最终不 push、不部署、不重启/reload live gofer、不触碰真实配置。

## 范围、排除项与授权

范围包括：

- ACP runner 在 `<result_dir>/artifacts/acp.jsonl` 新增 `prompt` 与 `message` 业务记录，补齐 2 秒静默落盘、边界落盘、stop 前 flush 和 `locations.path/line` 保真；stdout.log 字节行为保持不变。
- `internal/streaming` 内新增 ACP JSONL 读取、归一、tail、旧作业回退、事件大小限制、运行中 follow 与终态 end；HTTP 层只解析 job/tail、套既有认证/GET 权限并准备 SSE。
- 新增 `GET /v1/jobs/{id}/acp/stream?tail=N`；业务事件统一用 `event: acp`，JSON data 为 `{seq,kind,...}`，终止仍用现有 `event: end`。
- 前端复用 fetch + reader SSE 解析器、`MarkdownBlock`、`InteractionCard`、Workbench 5 秒 threads poll 与现有 turn/answer/punt；新增纯 reducer 与对话组件。
- README 工作台段落从“W3 尚未做”改为 W3a 对话视图用法；若 3600 秒预算允许，Approved identity 0.4 设计末尾追加只含真实证据的“W3a 实测记录”，不提升设计 revision。

明确排除：

- `GET /v1/workbench/threads/{id}/diff`、`POST /v1/workbench/threads/{id}/review`、diff 文件列表、行内评论、本地草稿、接受/回灌和编辑事件触发刷新；它们属于 W3b。
- locations 的可点击跳转、跟随 agent、编辑器集成；W3a 只显示 `path[:line]` 文本。
- 修改 workbench thread 聚合、`job_ids` 顺序、status/review/seen 语义、resume 派发、interaction 状态机或 ACP wire type；这些现有 owner 只作只读依赖。
- 新数据库表、Schema、第三方 Go/pnpm 依赖、新全局状态库、新 Markdown/SSE parser、WebSocket 或 EventSource 平行实现。
- 为旧 server/worker、缺 `job_ids`、agent 名称后缀或旧 route 增加无标记兼容分支。
- pull/rebase、push、tag、release、deploy、迁移/清理真实数据、live server/worker restart/reload、真实配置目录、远端 worker、traffic、外部消息和硬件动作。

当前请求只授权生成、验证并本地提交本计划候选；不授权实施。候选获批后仍须用户续接本会话形成新的当前执行请求。执行方式已选择 `DIRECT_CONTINUOUS`，但该选择只决定获批后的连续推进方式，不越过本次 Plan Gate。

- `host_or_non_offline_action=REQUIRED`

该值只覆盖获批实施中的本机编译、测试、`httptest`/`t.TempDir()`、Web 构建和必要的随机 loopback 临时 smoke。若增加独立 gofer CLI smoke，每条命令必须显式 `--server http://127.0.0.1:<随机端口>` 或 `-c <临时配置>`；本计划不授权 live/remote/真实配置或任何出机器动作。

## 输入与批准证据

- 唯一设计依据：[2026-09-26-web-workbench-design.md](../design/2026-09-26-web-workbench-design.md)，状态 `Approved`、identity `0.4`，当前设计基线提交 `39f8106`；本计划只消费“W3 细化”的前四节，不消费同节的“改动视图”“行内评审”或 W4。
- 当前任务书明确将本 job 命名为 `WEB-11 W3a`，固定三组测试与验证矩阵，要求测试先行、按功能点提交、候选提交后停止，并明确 `DIRECT_CONTINUOUS`。
- 用户已授权 gofer 监督者 Claude 代为批准“由已批准设计派生的实施计划”；设计 Approved、validator PASS、candidate commit、低暴露评审 PASS 或执行方式选择都不自动构成计划批准或实施授权。
- IDEV-STD：workspace `BOUND`，runtime `0.22.0`，standards revision `d2a5c03b46ab65fb50156ffadb83774cb7a82bb5`，任务 `plan+git` 的 semantic delivery 为 `FULL`，payload SHA-256 已核对；首次 mutation 前 verify 返回 `inputs_unchanged=true`。
- 跟踪项：tools Beads `tools-fkk`（`[gofer][WEB-11] W3a ACP 结构化流与对话视图计划`）；从 workspace 以 `bd -C tools ...` 访问，gofer 子仓本身没有独立 Beads DB。

workspace baseline（2026-09-27 规划时）：

- Git root：`D:/work/inhere/my-tools-dev/gofer`；branch=`main`；HEAD=`39f8106da70ec08df280d14d8002fde6805ab8b4`；`git status --short` 无输出；`git status -sb` 为 `main...origin/main [ahead 86]`。origin 仅作只读基线，本 job 禁止 push。
- 工具：`go version go1.25.10 windows/amd64`、Node `v24.15.0`、pnpm `12.5.1`；Web 已有 `vitest run`、`vue-tsc --noEmit` 和 Vite build，无需增加依赖。
- Codebase Memory 项目=`gofer`、Verify Tier 2、generation=`2026-09-05T06:59:33Z`，项目 root/branch/current HEAD 正确，但 18 条证据路径全部为 `not_tracked` 或 `metadata_changed`；`web/src/api/types.ts:1-989` 另有 `parse_partial`，不与本计划依赖的 Workbench/SSE 类型行重叠。所有物质性结论已从当前 HEAD 精确源文件回读；图仅用于确认既有 `httpapi.handleJobStream -> streaming.StreamJob -> TailFrom/writeSSE` 关系，不用于否定性或完整性结论。
- 当前 runner 只把 agent message chunk 写 stdout；thought 已有 coalescing，tool/permission/plan/usage/stop 已写 acp.jsonl；`ToolCallLocation` 已含 `path + optional line`，但 `toolCallEvent` 当前只落 path 字符串。
- 当前 `streaming` 已有 `StreamPollInterval=250ms`、`TailFrom`、`TailLinesOffset`、`writeSSE` 与默认 `MaxSSEFrameBytes=1MiB`；`handleJobStream` 已建立历史 job lookup、flushable writer、SSE headers 与 tail 上限 5000 的实现范式。
- 当前 workbench `Thread.JobIDs` 已由按 `StartedAt,ID` 排序的 records 顺序生成；`ThreadView` 仅对 latest job 执行 `streamJob(tail:200)`。W1 composer 对 ACP 的权威判定是 `MetaAgent.type === "acp-agent"`；`MarkdownBlock` 已集中执行 marked + DOMPurify。
- 已知、不得顺手修：`internal/worker` 的 `TestPolicyCacheRoundTrip` 0600 断言；8 个实施前已存在的 gofmt 不干净文件。W3a 只要求 owned changed Go 文件 gofmt clean。

预期 owner 路径：

- 测试先行与 fake agent：新建 `internal/runner/acp/runner_test.go`、新建 `internal/streaming/acpstream_test.go`、新建 `internal/httpapi/acp_stream_test.go`、新建 `web/src/components/workbench/acpEvents.test.ts`；按测试需要扩展 `internal/acp/acptest/server.go` 与 `internal/acp/acptest/acptest.go` 的可控消息静默脚本。
- runner：`internal/runner/acp/runner.go`。`internal/acp/types.go` 只读复用，除非发现当前 `ToolCallLocation` 无法无损表达设计；若需改 wire type，属于 Semantic Amendment，先停。
- streaming/HTTP：新建 `internal/streaming/acpstream.go`、新建 `internal/httpapi/acp_stream_handler.go`，修改 `internal/httpapi/server.go`；`streaming.go`、`stream_handler.go`、`jobcredential.go` 原则上只读复用，只有 owner 内无语义的 helper 抽取才可按 Corrective 记录。
- Web 数据与 transport：新建 `web/src/components/workbench/acpEvents.ts`，修改 `web/src/api/sse.ts` 与 `web/src/api/types.ts`。
- Web 视图：新建 `web/src/components/workbench/ConversationView.vue`，修改 `ThreadView.vue` 与 `web/src/views/Workbench.vue`；`WorkbenchComposer.vue`、`MarkdownBlock.vue`、`InteractionCard.vue` 和 `web/src/api/workbench.ts` 只读复用。
- 文档：`README.md`、可选 `docs/design/2026-09-26-web-workbench-design.md`、本计划的非语义 progress 记录。

这些 expected paths 是 planning evidence，不是封闭白名单。实施发现新 path/symbol 时，在下一次 mutation 前按 Operational Discovery / Corrective / Semantic Amendment / Ownership Conflict 分类；不得通过扩大 staging、hunk staging 或重算 baseline 绕过 owner 冲突。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | ACP prompt/message 可重放落盘且 stdout 行为不变 | `runner/acp.handler`、`eventWriter`、`writeStdout`、`addThought/flushThought`、`endTurn`、`acptest` | 直接复用 eventWriter、handler mutex、stdout block 与 fake agent | 在同一 handler 增 message builder、idle timer、边界 flush 与 prompt writer；不建第二 transcript | OWNER_EXTENSION | 现有 acp.jsonl 没有 prompt/message，读取端无法还原对话 | 唯一真源仍是每 job 的 acp.jsonl + stdout；timer 在 endTurn 必须关闭，删除 W3a 时与字段/测试一并回退 |
| CAP-02 | JSONL 归一、tail、frame cap、live follow、terminal end | `StreamJob`、`TailFrom`、`TailLinesOffset`、`writeSSE`、`StreamPollInterval`、`MaxSSEFrameBytes` | 直接复用轮询节奏、增量尾读、SSE encoder 与 job terminal 判定 | 在既有 `internal/streaming` package 增 `acpstream.go`，HTTP 不承载归一逻辑 | OWNER_EXTENSION | 通用 log stream 只传字节/status，无法合并 thought、按 tool id 表达更新或过滤 ACP 噪声 | 不建新 package/reader daemon；状态只活在一次连接，文件仍是权威，连接结束即释放 |
| CAP-03 | authenticated ACP SSE route 与 job caller GET 读权限 | `handleJobStream`、`server.go` jobs routes、SEC-01 `jobCallerMayRead` | 复用同一 auth/middleware、历史 job lookup、flushable SSE response 与 `end` convention | 新 handler 只解析 id/tail并调用 streaming；route 是唯一新 HTTP seam | THIN_ADAPTER | 现有 `/stream` 的 payload 是日志帧，不能复用为结构事件而不破坏旧消费者 | 不改旧 route，不加 alias；删除 W3a 时先删前端 consumer，再删 route/handler |
| CAP-04 | 可独立测试的 tool/thought/truncated/多轮 reducer | `layoutTree.ts` 纯函数范式、Vue reactive state、现有 SSE data | 复用 TypeScript 原生 Map/数组与 Vitest | 新建无 IO 的 `acpEvents.ts`，只拥有事件归并和 round projection | MINIMAL_NEW_MODULE | 直接在 Vue component 原地改数组会让 tool 覆盖与多轮顺序不可独立验证 | 单一 consumer 为 ConversationView；无全局状态/持久化，删除 UI 时与唯一 test 一并删除 |
| CAP-05 | 对话轮次 UI 与内联审批 | `ThreadView`、`MarkdownBlock`、`InteractionCard`、Workbench poll/turn、现有 CSS tokens | Markdown、安全清洗、审批作答、thread header/footer 全部直接复用 | 新建 `ConversationView.vue` 作为薄视图；ThreadView 只做 ACP/非 ACP 分派 | THIN_ADAPTER | 把多轮加载、工具/思考卡直接塞进 424 行 ThreadView 会混合 transport、projection 与 header/footer owner | 新组件不拥有 auth/turn/interaction 状态机；W3b 可扩展消费事件但不能复制 reducer |
| CAP-06 | ACP 类型识别与有序 job 链 | W1 `MetaAgent.type`、`WorkbenchThread.job_ids`、`projectThreads` 排序、`getMeta` | 直接复用 `type === acp-agent` 与现有 `job_ids` | Workbench 页面共享 ACP agent key set给窗格；不改后端 thread contract | DIRECT_REUSE | 无 gap：当前合同已包含有序 job ids 与 agent key | 不增加 agent 名称约定、`agent_type` 平行字段或 per-pane localStorage |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| MOD-01 | CAP-04 | `web/src/components/workbench/acpEvents.ts` | `ThreadView.vue` inline arrays、`sse.ts` transport parser、`layoutTree.ts` pure helper pattern | transport parser只拆 SSE 帧，不懂 ACP tool/round 语义 | 把 reducer 放 ThreadView 会把可测试领域规则埋进组件；扩展 sse.ts 会混淆 transport 与 projection | 固定 Vitest 要求纯函数且覆盖 tool/thought/truncated/多轮 | workbench/ConversationView 唯一 owner；无 IO、无生命周期服务 | W3a 回滚时与 `acpEvents.test.ts`/ConversationView 一并删；若未来通用化必须有第二个已验证 consumer |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| Browser `EventSource` 或第三方 SSE client | CAP-02 | bearer Authorization 无法按现有方式注入；现有 `sse.ts` fetch + reader 已处理 auth、abort、分帧与 end，复用即满足验收 |
| 第三方状态/reducer package | CAP-04 | TypeScript、Vitest 与局部纯函数已覆盖归并需求；新增依赖不会减少 owner 或验证面，Web dependency 文件应保持无 diff |
| 第三方 chat/markdown package | CAP-05 | Vue、`MarkdownBlock` 与 `InteractionCard` 已覆盖安全渲染和审批卡；新增 UI 包只会形成第二组件体系 |
| 新 threads detail endpoint 或 `agent_type` 兼容字段 | CAP-06 | `job_ids` 与 MetaAgent.type 已存在；新增后端 contract 只会产生第二真源，删除测试是“不改 workbench model/service/handler” |

## 前置检查与 fail-closed 条件

1. 监督者先对本候选做低暴露的一轮合并 Standards+Spec review，并明确批准 revision 0.1；用户再续接本会话形成当前执行请求。二者缺一即停。
2. 执行开始按 `inhere-run-supmode` 重新加载 `implement+test+git` 语义资源，核对 approved candidate tuple、`DIRECT_CONTINUOUS`、session fingerprint、branch/HEAD/status 与 Beads；每个 mutation stage 前过 fingerprint Gate。
3. `git status --short` 必须把 owner 内 dirty/untracked 判为本 job 可拥有；任何用户/其他 Agent 修改命中 expected path，分类为 Ownership Conflict 并停止。无关 dirty 只保留，不暂存、不格式化。
4. 禁止读取/修改 `D:/work/inhere/config/win-env/gofer`；禁止 restart/reload live gofer server/worker。所有测试用 `t.TempDir()`；任何独立 smoke 只用临时配置和随机 loopback 端口，并逐条显式指定 `--server` 或 `-c`。
5. T10 必须先形成真实 RED，失败原因只能是 W3a 缺失 symbol/route/behavior；编译环境、fixture 或既有失败先修正测试本身，不把假 RED 提交为 contract。
6. runner 的 stdout 字节序列、ACP wire decoder、thread/job_ids、turn/answer/punt、非 ACP LogTape/PTY/relay 是冻结面。发现必须改变任一外部 Interface、审批状态机或 W3b contract，分类为 Semantic Amendment 并停止。
7. `internal/streaming` 不 import `httpapi`；`httpapi` 不承载 normalize/follow 业务；新增依赖后以 build/vet/必要时 `go list -deps` 验证 G021/G022。
8. 文本只以 apply_patch 修改，保持 LF、UTF-8 无 BOM；不运行会写文件的 `gofmt -w`，只用 `gofmt -d/-l` 检查并通过 apply_patch 修正。
9. 若指定 package test 出现任务前已知之外的失败，保留原始输出并 focused 对照；只修 owned regression。`TestPolicyCacheRoundTrip` 与既有 8 个 gofmt 文件不得顺手修改。

## 波次与依赖

| 波次 | 任务 | 依赖 | 产出/恢复点 |
|---|---|---|---|
| W0 | W3A-T00 | approved candidate + 当前执行请求 | 只读 preflight 与执行基线 |
| W1 | W3A-T10 | T00 | 固定 contract tests 的唯一 RED commit |
| W2 | W3A-T20 | T10 | runner prompt/message GREEN commit |
| W3 | W3A-T21 → W3A-T22 | T20 | streaming normalizer/follow commit；HTTP route/security commit |
| W4 | W3A-T30 → W3A-T31 | T22 | Web reducer GREEN commit；对话视图 commit |
| W5 | W3A-T40 → W3A-T41 → W3A-T42 | T31 | 全质量门；README/可选实测记录；最终 Git/Beads 交付 |

单 Agent 按依赖顺序执行，不需要 transfer registry。若后续监督者改为多 Agent 并行，必须在 mutation 前冻结重复 owner（尤其 `ThreadView.vue`、`types.ts`、`server.go`）的 transfer chain。

## 任务

### W3A-T00 复核候选、执行授权与 workspace baseline

- 文件: 只读 `docs/plans/2026-09-27-web-workbench-w3a-plan.md`、Approved 0.4 design、`AGENTS.md`、Git/Beads/IDEV-STD 状态；不改 source。
- 动作: 核对 candidate 四元组、监督者批准原话、用户续接执行请求和 `DIRECT_CONTINUOUS`；重新加载实施语义；确认 branch/HEAD、owner dirty、当前工具版本、真实配置禁区和 live 进程禁区。将任何新 path 先按 discovery 分类。
- 验证: `git branch --show-current`、`git rev-parse HEAD`、`git status --short`、`git log -5 --oneline`、`bd -C tools show tools-fkk`；运行既有 focused baseline `go test ./internal/runner/acp/ ./internal/streaming/ ./internal/httpapi/ -count=1` 与 `cd web && pnpm test && pnpm typecheck && pnpm build`，同时记录 exit code 和原始 PASS/FAIL 行。
- 完成标准: plan approved + execution current，fingerprint/preflight PASS，owner 无冲突，baseline 失败已独立归类；否则停止。
- 依赖: Plan Gate + Execution Gate。

### W3A-T10 先提交固定名 RED contract tests

- 文件: 新建 `internal/runner/acp/runner_test.go`、`internal/streaming/acpstream_test.go`、`internal/httpapi/acp_stream_test.go`、`web/src/components/workbench/acpEvents.test.ts`；按需扩展 `internal/acp/acptest/server.go`、`acptest.go`，只增加可控的 message-boundary/idle 脚本选项。
- 动作: 新增精确测试名 `TestACPRunnerRecordsPromptAndMessages`：通过 `acptest` 真进程跑一轮，过滤 session/set_mode 元数据后断言首条业务记录是 8000-rune prompt + truncated；断言 tool/thought/plan/stop/可缩短的 idle timer 各能触发 flush，stop 前有残余 message，按 block 拼接后与 stdout.log 相同；不得 `t.Skip`。
- 动作: 新增精确测试名 `TestACPStreamEmitsStructuredEvents`：在 `t.TempDir()` 手写含 token thought、同 id tool pending→completed、permission、plan、usage、noise、stop 的 acp.jsonl 与 stdout；从公开 HTTP route 读取并断言 kind/seq/tail/truncated/frame cap/end；分别用 user token与 job credential访问 `/stream`、`/acp/stream`，证明 GET 权限同形。
- 动作: 新增 `acpEvents.test.ts`，锁定 tool merge 保留首次位置并用 defined fields 覆盖、thought 相邻合并、truncated skipped、job_ids 多轮顺序和加载窗口。测试只 import 尚不存在的 pure module，不通过复制实现自绿。
- 验证: `go test ./internal/runner/acp/ -run '^TestACPRunnerRecordsPromptAndMessages$' -count=1`、`go test ./internal/streaming/ ./internal/httpapi/ -run 'ACPStream' -count=1`、`cd web && pnpm test -- acpEvents.test.ts` 必须因 W3a 缺失 FAIL；`rg -n 't\.Skip' <owned-test-files>` 原生 exit 1 且无输出。
- 完成标准: 三组固定 contract 被测试运行器发现，RED 只指向缺失行为；提交前 `git status --short`、精确 add 与 `git diff --cached --name-only` 仅含测试/fixture owner，创建单独 `test(web-11): pin W3a ACP conversation contracts` commit。
- 依赖: T00。

### W3A-T20 实现 runner prompt/message 落盘

- 文件: `internal/runner/acp/runner.go`；消费 T10 的 runner/acptest fixtures，不扩 ACP wire schema。
- 动作: 在 `client.Prompt` 前写 prompt；以 rune 边界截到 8000 字，只有实际截断才写 `truncated:true`。session/set_mode 可继续作为元数据，但 prompt 必须是首条业务记录。
- 动作: 在 handler 同一 mutex owner 下新增 message builder 与默认 2 秒 idle timer；agent chunk 仍逐块原样写 stdout，同时合并进 builder；thought、tool_call/update、plan、stop/endTurn 边界同步取消 timer 并 flush。timer callback 与 endTurn/Close 竞争必须幂等，eventWriter 关闭后不得有迟到写。
- 动作: 为连续超大输出增加 1 MiB 级安全分段 flush，按 UTF-8 边界拆成连续 message records，不丢文本；调整 eventWriter text-record line guard，使 8000 字 prompt 与受控 message 不会退化为只有 `{t,truncated:<bytes>}` 的占位。其他 raw/plan 防护仍保留。
- 动作: `toolCallEvent` 原样持久化 `locations:[{path,line?}]`；不改 `ApprovalToolCall` 现有字符串 locations 或 ACP decoder。stop 只有在残余 message/thought 已落盘后写出；stdout newline/block 逻辑保持字节兼容。
- 验证: runner 固定测试 GREEN；现有 `go test ./internal/runner/acp/ -count=1`、`go test ./internal/job/ -run 'ACP(Output|Permission|Usage|ReadOnly)' -count=1`；对 owned Go 文件 `gofmt -l` exit 0 且无输出。
- 完成标准: prompt/message 可从 acp.jsonl 重放，stdout 回归逐字节相同，timer 无泄漏/竞态表现，locations 保留 line；创建独立 `feat(web-11): persist ACP prompts and messages` commit。
- 依赖: T10。

### W3A-T21 实现 streaming ACP 归一、tail 与 follow

- 文件: 新建 `internal/streaming/acpstream.go`、消费 `acpstream_test.go`；原则上不改 `streaming.go`，直接复用同 package 的 `TailFrom`、`writeSSE`、常量与 job status helper。
- 动作: 定义最小 `ACPStreamOpts{TailEvents}` 与 JSON event shape。解析完整 newline-delimited JSON，未知/畸形/空行安全跳过；映射 prompt/message/thought/tool/permission/plan/usage_update/stop，丢弃 session、available_commands_update、session_info_update、mode/current_mode_update/set_mode 与不在批准 kind 集合的记录。
- 动作: 连续 thought 合并；每条 tool 记录都输出 kind=tool 且保留同一 `tool_call_id`，首条携 title/kind/raw_input、后续只携存在的更新字段，raw_input 安全截到 2000 字，locations 保留 path/line。usage_update 映射为 usage，不重新解释计费真源。
- 动作: 初始 snapshot 全量归一后，在没有任何 message record 时读取同 job 的 stdout.log，作为 stop 前最后一条 message；该旧作业路径标 `// DEPRECATED(v0.65): remove in v0.68`。旧逐 token thought 合并路径同样标记，移除条件为 pre-v0.65 artifact 已超出 retention。
- 动作: tail 在归一和 fallback 后计算；省略时先发 kind=truncated + skipped，再发最后 N 条。seq 是连接内单调序号。文本事件序列化超过 `MaxSSEFrameBytes` 时以 UTF-8/rune 安全方式缩短 text，并写 `truncated:true`，重新 marshal 验证帧不超限。
- 动作: 运行中从已消费 byte offset 轮询 `TailFrom`，保留半行直到下一次增长；每轮读取后查询 job status，终态前做最后 drain/flush，然后 `event:end`。缺失文件在 live 时继续等待，historical terminal job 则可仅 stdout fallback 后结束。
- 验证: streaming focused tests GREEN；增加 malformed line、partial line/live append、oversize Unicode 与 terminal drain 子测试；`go test ./internal/streaming/ -count=1`、`go vet ./internal/streaming/`。
- 完成标准: normalizer/follow 完全位于 streaming，连接状态有界且结束释放，旧 fallback/legacy merge 有 G032 标记；创建独立 `feat(web-11): stream normalized ACP events` commit。
- 依赖: T20。

### W3A-T22 暴露 ACP SSE route 并锁定读权限

- 文件: 新建 `internal/httpapi/acp_stream_handler.go`、修改 `internal/httpapi/server.go`、消费 `internal/httpapi/acp_stream_test.go`；`stream_handler.go`/`jobcredential.go` 默认只读。
- 动作: 注册 `GET /v1/jobs/{id}/acp/stream`。handler 按现有 `handleJobStream` 解析 live/历史 job、404、http.Flusher、SSE headers/open comment；`tail` 只接受正整数并 cap 5000，然后调用 streaming。不得在 handler 解析 JSONL、合并 thought 或读 stdout。
- 动作: route 留在同一 authenticated `/v1` group；GET 自动经过 `jobCallerMayRead`，不新增 write allowlist 或 owner 特例。SSE 正常业务帧为 `event: acp`，终态为 `event: end`，401/404/不支持 flush 的错误在 body commit 前返回。
- 验证: `TestACPStreamEmitsStructuredEvents` 全部 GREEN；`go test ./internal/httpapi/ -run '^TestACPStreamEmitsStructuredEvents$' -count=1`；`go test ./internal/streaming/ ./internal/httpapi/ -count=1`；`go vet ./internal/httpapi/ ./internal/streaming/`。
- 完成标准: endpoint、tail、terminal end 与 user/job caller权限从公开 seam 可观察；G021/G022 无业务倒灌/依赖环；创建独立 `feat(web-11): expose ACP event stream` commit。
- 依赖: T21。

### W3A-T30 实现 Web ACP 事件纯函数

- 文件: 新建 `web/src/components/workbench/acpEvents.ts`，消费 `acpEvents.test.ts`；按需在 `web/src/api/types.ts` 仅增加 `acp` SSE event type，ACP domain types留在新模块。
- 动作: 定义 discriminated ACP event、display entry、round state 与 immutable reducer；tool id 首次出现决定排序位置，后续只覆盖事件中存在的字段；相邻 thought 合并并保留 truncated；truncated gap 累加 skipped；prompt/message/permission/plan/usage/stop 保序。
- 动作: 以 thread `job_ids` 为唯一 round 顺序，构造 `{jobId,events,ended}`；未知 job 的事件不创建幽灵轮次。窗口 helper 默认最后 3 轮，每次向前扩 3，边界稳定。
- 验证: `cd web && pnpm test -- acpEvents.test.ts` GREEN，随后 `pnpm test`；TypeScript 无 `any` 逃逸掉 discriminated union 的核心分支。
- 完成标准: 固定 Web 测试全绿、函数无 Vue/DOM/network/localStorage 依赖；创建独立 `feat(web-11): reduce ACP conversation events` commit。
- 依赖: T22。

### W3A-T31 实现 ConversationView 并接入 ACP thread

- 文件: 新建 `web/src/components/workbench/ConversationView.vue`；修改 `web/src/components/workbench/ThreadView.vue`、`web/src/views/Workbench.vue`、`web/src/api/sse.ts`、`web/src/api/types.ts`；只读复用 `MarkdownBlock.vue`、`InteractionCard.vue`、`WorkbenchComposer.vue`。
- 动作: 从现有 fetch-reader 抽取/复用 authenticated SSE consume core，新增 `streamACPJob(id,{tail,signal,onEvent})` 指向 `/acp/stream`；不得复制 parser/auth/401/abort/end 循环或改旧 `streamJob` 行为。
- 动作: Workbench 页面一次加载 MetaAgent，将 `type === "acp-agent"` 的 key set放入已有 view context；ThreadView 只在 kind=agent 且 agent key命中时选择 ConversationView。meta 失败时保留当前日志视图并显示可恢复错误，不以名称后缀猜测。
- 动作: ConversationView 接受有序 job ids、job status摘要、pending interactions和 focused状态。初始并发加载最后3轮；“加载更早”每次扩3轮；terminal历史流收到 end即关闭；只有最新 job 处于运行态时保留 AbortController/SSE，thread或窗口变化时精确 abort旧连接。
- 动作: 用户 prompt、助手 message 用气泡分侧；message 交给 `MarkdownBlock`；thought 用默认折叠 details；tool卡显示 title/kind/status、可展开 raw_input与 `path[:line]`，路径不可点击；permission/plan/usage/stop和 truncated gap有明确只读样式。本轮 pending `InteractionCard` 内联在最新轮次末尾并向 ThreadView emit answer/punt。
- 动作: ThreadView 的 header、stop、job详情、seen timer和底部 turn composer保留；ACP 分支不再启动通用 `streamJob(tail:200)`，非 ACP 日志/PTY/relay路径逐字保持。ACP end后触发现有 context refresh，使 canTurn/status收敛。
- 验证: `cd web && pnpm test && pnpm typecheck && pnpm build`；静态检查 ConversationView只 import既有 Markdown/InteractionCard，不直接 `v-html`；focused手工组件测试覆盖1轮/3轮/4+轮、running latest、pending approval、meta失败与 non-ACP LogTape回归。
- 完成标准: 按轮对话、load earlier、latest-only live、inline审批与底部续接全部可观察，W3b交互没有混入；创建独立 `feat(web-11): render ACP conversation rounds` commit。
- 依赖: T30。

### W3A-T40 执行完整代码质量门

- 文件: 全部 owned changed source/tests；原始输出保存到仓库 `tmp/web11-w3a/<run-id>/`（ignored，不提交），不得写系统临时目录。
- 动作: 对 owned Go 精确清单运行 `gofmt -l`/`gofmt -d`；不使用 `gofmt -w`，任何格式修正通过 apply_patch。Windows `internal/httpapi` 慢测可后台运行，但最终报告前必须等待每个进程并同时检查 exit code/输出。
- 验证: `go build ./...`；隔离子进程设置 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` 后 `go build ./...`；`go vet ./...`；`go test ./internal/runner/acp/ ./internal/streaming/ ./internal/httpapi/ -count=1`；`cd web && pnpm test && pnpm typecheck && pnpm build`。
- 验证: `git diff --check`；owned text 控制字符 `rg --pcre2 '[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]'` 原生 exit 1/无输出；逐文件验证 CRLF=0、UTF-8 BOM=0；`git diff --exit-code -- go.mod go.sum web/package.json web/pnpm-lock.yaml` 无依赖 diff。
- 完成标准: gofmt/build/vet/tests/Web/diff/编码检查按命令同时满足期望 exit code 与原始输出；任何 FAIL 有 focused对照和如实边界，不用 known baseline掩盖 W3a regression。
- 依赖: T31。

### W3A-T41 更新 README、可选实测记录与计划 progress

- 文件: `README.md`、可选 `docs/design/2026-09-26-web-workbench-design.md`、本计划的非语义 progress；只消费 T20–T40 的真实 commit/test输出。
- 动作: README 工作台段落说明 ACP conversation、最近3轮/加载更早、结构事件/审批/续接，并把“W3 尚未做”收窄为“W3b diff/行内评审尚未做”；HTTP表增加 `/jobs/{id}/acp/stream`。
- 动作: 时间允许时在 Approved identity 0.4 设计追加“W3a 实测记录”，只列真实 commits、固定测试、build/vet/Web结果、G032兼容项和未执行边界；这是 identity/provenance记录，不改决策、不提升 revision。若预算不足，先保证代码/测试/README提交并把设计记录明确列为 `NOT_DONE`，不得伪称完成。
- 动作: 计划只记录 task/status/actual paths/validation/commit/lifecycle progress，不改变 approved candidate的任务合同；纯 candidate identity修正不升 0.1。
- 验证: IDEV-STD runtime 的 `validate_document.py --kind plan`、`--kind design`（仅设计有改动时）；README/设计/计划相对链接检查；`git diff --check` 与 LF/BOM/控制字符检查。
- 完成标准: README与实际行为一致；可选记录若存在只写已验证事实；独立 `docs(web-11): document W3a conversation view` commit。
- 依赖: T40。

### W3A-T42 原子提交、Beads 与最终交付检查

- 文件: 每个功能点 exact owner files、tools Beads `tools-fkk`；不修改其他 dirty 文件。
- 动作: 每次 commit 前运行 `git status --short`，精确 add，核对 `git diff --cached --name-only`；使用 conventional commit，禁止 hunk-stage绕过 ownership。最终更新 Beads状态/notes，记录任务状态、一句话说明、commit hash、原始 gofmt/build/vet/test/Web/控制字符行、Git与G032清单。
- 验证: `git log --oneline <implementation-baseline>..HEAD`、`git status --short`、`git status -sb`、`bd -C tools show tools-fkk`。只读报告 origin ahead/behind；不得 pull/rebase/push。
- 完成标准: owned变更按功能点本地提交，nested worktree无未归属修改，Beads与Git一致，明确未 push/未部署/未操作live/真实配置。若可选设计记录未做，最终单列而不阻断已完成的source/runtime状态。
- 依赖: T41。

## 回滚与恢复

- commit序列固定为 RED tests → runner persistence → streaming normalization → HTTP route → Web reducer → conversation UI → docs。回滚使用针对性 `git revert <commit>` 方案，不使用 `reset --hard`、`checkout --` 或 `clean`。
- consumer-first 回滚：先把 ThreadView恢复为 LogTape并删除 ConversationView/reducer consumer，再删 `/acp/stream` route/handler与 streaming owner，最后撤 runner新记录。旧 acp.jsonl 里的 additive prompt/message 行会被旧代码忽略，无迁移/清理动作。
- runner回滚不改 stdout.log；timer/message builder只存在进程内。若在运行作业中回滚 binary，需要 live restart，未获本计划授权，必须另走外部动作 Gate。
- endpoint没有数据库/配置/schema生命周期；未部署时 source revert即可。任何已部署场景不由本计划推断，需另立 release/deploy授权。
- 恢复点是每个 atomic commit、测试输出目录和 Beads notes；临时测试只使用 `t.TempDir()`/随机端口，只停止记录的临时 PID，目标身份不匹配即停。
- 非 SUPMODE 终态为 BLOCKED/PAUSED/FAILED_TERMINAL 时使用统一6行中断卡：状态、已完成、当前/已验证、未完成、首个失败边界/影响、唯一下一步；详细输出只引用 `tmp/web11-w3a/`。

## 人工 Gate

1. Design Gate：Approved identity 0.4 已满足，只覆盖 W3a scope freeze；不授权 W3b、W4、部署或 live操作。
2. Plan Review/Approval Gate：本文件是 Draft 0.1 candidate，提交后必须停止。低暴露候选需监督者完成一轮合并 Standards+Spec review；可使用最小批准语句：“批准该 W3a 计划，按 DIRECT_CONTINUOUS 连续实施”。
3. Execution Gate：计划批准后仍需用户续接本会话形成新的当前执行请求；candidate/review PASS/执行方式选择都不自动开始实现。
4. Host action Gate：计划批准并请求执行后，只允许本计划具名的本机 build/test、`t.TempDir()`、随机 loopback与可选临时 smoke；push、release、deploy、live restart/reload、真实 config/data、远端 worker、traffic、外部消息和硬件动作仍未授权。
5. Secret Gate：W3a 不需要真实 Secret；一旦测试或 smoke 提出 bearer/VAPID/真实配置内容，停止并改用测试 token/临时配置，不能扩大读取权限。
6. Semantic Amendment/Ownership Conflict Gate：改变 ACP wire/schema、thread/turn/interaction语义、增加 W3b、第三方依赖、无标记兼容或命中他人 dirty owner时立即停止，返回监督者/用户决策。

## 可追溯性

| 目标/验收 | 设计/任务书来源 | 任务 | 验证 |
|---|---|---|---|
| prompt 8000字/truncated/首条业务记录 | ACP落盘补齐；T1/T2-1 | T10,T20 | `TestACPRunnerRecordsPromptAndMessages` |
| message边界、2秒idle、stdout不变、stop残余 | ACP落盘补齐；T1/T2-1 | T10,T20 | 同固定测试 + runner package回归 |
| thought merge、tool更新、locations、noise过滤 | 结构化流；T1/T2-2 | T10,T21,T22 | `TestACPStreamEmitsStructuredEvents`、streaming子测试 |
| stdout旧作业兜底与G032 | 现状/ACP落盘补齐；G032 | T21,T42 | 固定HTTP测试、源码DEPRECATED扫描、最终清单 |
| tail/truncated/frame cap/seq | 结构化流；T1/T2-2 | T10,T21,T22 | tail=2与oversize子测试 |
| live follow与terminal end | 结构化流；T1/T2-2 | T21,T22 | partial append/terminal drain测试、HTTP end断言 |
| job caller读权限同 `/stream` | T1/T2-2 | T10,T22 | user/job token HTTP subtests |
| tool/thought/truncated/多轮纯函数 | T1 Web固定测试；T2-3 | T10,T30 | `acpEvents.test.ts` |
| 最近3轮/加载更早/latest-only SSE | 对话视图；T2-3 | T30,T31 | reducer窗口测试、typecheck/build、组件手工证据 |
| markdown、工具卡、路径文本、inline审批 | 对话视图；T2-3 | T31 | MarkdownBlock/InteractionCard复用检查、Web build |
| ACP按MetaAgent判定；非ACP/turn保持 | 对话视图；W1现状 | T31 | source断言、non-ACP回归、Web build |
| README/实测边界、质量、隔离与无push | 文档/验证/house-rules | T40,T41,T42 | validators、原始命令输出、Git/Beads/lifecycle证据 |

## 完成 Gate 与剩余工作

W3a 只有在 T10–T42 达到各自完成标准、三组固定 contract从RED转GREEN、runner/stdout与结构流合同闭合、ConversationView按 job链运行、Windows/Linux build/vet/指定测试/Web门和编码扫描均有最终结果、所有 owned变更按功能点本地提交、Beads同步且 `git status --short` 无未归属变更时，才能报告 source/runtime实施完成。局部单测、SSE 200、页面渲染、typecheck或 build单独成功都不能替代该 Gate。

G032 预期清单：

- 仍需保留的旧路径一：无 `message` 记录的 pre-v0.65 ACP artifact回退整个 stdout.log，必须标 `// DEPRECATED(v0.65): remove in v0.68`；只有 artifact retention已排除旧记录后才删除。
- 仍需保留的旧路径二：pre-v0.65逐 token thought在读取端合并，必须标同一 removal版本；新runner已coalesce，v0.68评估删除legacy专用分支。
- 新增无标记兼容分支：`NONE`。
- 删除的旧路径：`NONE`（本 job仍需读取现存旧artifact）；若实施发现没人使用的旧alias/route，先作为Operational Discovery记录并另行删除，不在W3a顺手扩围。
- additive HTTP：仅 `/v1/jobs/{id}/acp/stream` 当前正式route，不是旧route alias；additive JSONL `prompt/message` 是当前artifact合同，不是兼容字段。
- `job_ids` 与 MetaAgent.type 直接复用；不新增缺字段fallback、名字猜测、旧server/worker容忍、双parser或localStorage真源。

明确留待后续：W3b会话diff、live/captured来源、行内评论上下文合成与review endpoint、编辑事件刷新/文件跳转；W4 worktree/预览；push/release/deploy/live升级；真实浏览器/手机/远端验收。它们不因W3a完成、设计已批准或 `DIRECT_CONTINUOUS` 获得实施授权。
