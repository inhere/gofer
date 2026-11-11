<!-- template_id: plan; template_version: 1.3.0 -->
# 第 1 批：todo 按交付完成、长 job 记录按回合查看 实施计划

> 状态：Draft 0.3 / 待批准
> issue：gofer-94yq、gofer-mmh0（含 gofer-tykh）、gofer-gqap、gofer-8hlc、gofer-juo0、gofer-ehvd、gofer-r20b、gofer-5x85

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-11 | Claude（gofer 监督会话） | 初稿 |
| 0.2 | 2026-11-11 | Claude（gofer 监督会话） | 按两轴评审修订：续跑中以行级事实 `AutoResumedBy` 判定，覆盖 merge 交付路径（Spec F1）；ACP 回合改为复用现有 `artifacts/acp.jsonl` 与 `streaming.ReadTranscript`，转录读取业务放 `internal/streaming`（Spec F4，对设计的偏离见人工 Gate）；两条 sonnet 链各用一个 worktree、共享文件指定负责方（Spec F2/F3、Gov G2）；R5 签名与条件写实（F5）；卡片只认事件不跑 git（F6）；inbox 落点写全（F7）；补「共 N 回合」提示与 merge `--help`（Gov G1、F8）；suga 任务的执行位置与串行（G6/F9）；脱敏由 job 层注入写入器、先脱敏后截断（G7）；GR104 文件逐个标注（G3）；任务补 owner、依赖与委派约束（G4）；新事件的文档与前端登记（G5）；向 suga 会话发消息的时点（G8）；`complete_on` 校验在 jobstore（G10）；反向验证补两项（G9）；验证命令补全（F10） |
| 0.3 | 2026-11-11 | Claude（gofer 监督会话） | 第 2 轮修订：`acp.jsonl` 写入时按授予的 secret 值掩码，由 job 层注入（Gov N1，SECURITY）；`turns.jsonl` 在 `captureNDJSON` 内打开、随 capture 关闭（Spec N1）；回合数经 `GET /v1/jobs/{id}` 的 `turn_count` 字段到 CLI（Spec N2 / Gov N2）；ACP 读取改为逐行扫描、取够一页即停，路径走 `SafeJoinUnder`（Spec N3）；`complete_on` 校验照 notify 做法（Spec N4）；`AutoResumedBy` 判定的适用范围（Spec N5）；超长文件清单补全（Gov N3）；suga job 的 `-p` 与 `--cwd`（Gov N4）；建 worktree 前先提交 tracker（Gov N5） |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：①`complete_on=merge` 的 plan 中，worktree 交付的 todo 在交付落地后才完成、后续 todo 才派发，所有 plan 在续跑进行中不提前完成；②`job transcript` 按回合分页读取 claude / suga / ACP job 的完整记录。Scope freeze：只实施设计 Draft 0.3（已批准，Q1–Q6=A）、下文「对设计的细化」与人工 Gate 中列出的一处偏离。Review budget：两轴独立各一轮，有 `CORE_BLOCKING` 才继续。停止条件：两轴无开放 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

- 设计 §1 表每行行为成立；`job` 模式行为不变（含 `continue_from` 与分支基线回归）；续跑进行中（含 merge 交付路径）与续跑提交失败两种情况 todo 结局正确；「待你收尾」卡片按事件与交付状态出现与消失。
- claude / suga 本机 job 写出 `turns.jsonl`（脱敏、上限）；ACP job（含远程）复用 `acp.jsonl`；`job transcript` 的 CLI / HTTP / MCP 可分页取完任意大小的回合；多回合 job 的 `job show` / `job logs` 提示回合数。
- §3 Quick 与 §4 小改完成；文档同步；单测、真实验收、反向验证、gofer-verify 四路通过；合并、关 issue。

## 范围、排除项与授权

范围：设计 §1–§4。排除：设计「非目标」；`docs/capabilities.md` 随发版更新。

外部动作适用性：host_or_non_offline_action=REQUIRED （建 / 删主机 worktree、主机 web 构建、主机临时 server 真实验收、gofer-verify 为主机 job；push 与发版不在本计划内）。

授权：本计划批准后，按功能点本地提交、建 worktree、派子 agent、跑主机 job 与临时 server。push 与发版另行请示。外部动作：T9 收尾时向 suga 会话发一条消息（gofer-ohf7，设计 §2.1 已列，用户批准设计即授权），内容只请对方写明 stream-json 事件契约，不要求改 suga 代码，不含业务名（G031）。

## 输入与批准证据

- 设计：`docs/design/2026-11-11-todo-completion-and-job-transcript-design.md` Draft 0.3。用户 2026-11-11：「设计 Q1–Q6 按推荐，批准进入计划」。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | plan 字段与接口 | `Plan` 的 `notify` 字段路径（jobstore、`plan set`、plan HTTP、mcpserver `SetPlanNotify`，`backend.go:69-79`） | 是 | 照同一路径加 `complete_on` | OWNER_EXTENSION | 无该字段 | additive 迁移，G057 升级测试 |
| CAP-02 | todo 完成判定 | `linkTodoOutcome`、`markMergeTodoDone`、`recordDelivery` | 是 | 判定抽到新文件，原处只调 | OWNER_EXTENSION | 现判定只看 job 状态 | 单一判定函数，四处调用 |
| CAP-03 | 注意力卡片 | `internal/today` 卡片生成、`internal/inbox/waiting.go`（`Signals` / `signalsOf` / `Classify`）、`config.DefaultInboxRules` | 是 | 新卡片 kind 与类别 | OWNER_EXTENSION | 无「待合并」 | 与 `review` 类同处 |
| CAP-04 | 回合记录写入（ndjson 类） | `internal/runner/ndjsonfilter`（过滤器在白名单前见全部行） | 是 | 新写入器与累积器 | OWNER_EXTENSION | 投影器丢正文、suga delta 被丢 | 行类型只在 ndjsonfilter 定义 |
| CAP-05 | ACP 回合记录 | `artifacts/acp.jsonl`（`runner/acp/runner.go:86/323/356`、`transcript.go`）、`streaming.ReadTranscript`、远程镜像 `runner/acp_mirror.go` | 是 | 直接读取，outline 在同包补工具计数 | DIRECT_REUSE | none | 不再为 ACP 另写一份 |
| CAP-06 | 安全拼路径 | `internal/store/safejoin.go` `SafeJoinUnder` | 是 | 直接调用 | DIRECT_REUSE | none | 不另写 |
| CAP-07 | 脱敏 | `internal/job/secrets.go` `secretMasker`（值在执行机上解析，`submit.go:272`）；读侧 `secret.RedactString`（`httpapi/job_messages.go`） | 是 | 抽按值替换的纯函数 `maskSecrets`，由 job 层注入 `turns.jsonl` 写入器与 ACP 的 `acp.jsonl` 写入；读侧另加模式脱敏 | OWNER_EXTENSION | `mask` 是流式方法；`acp.jsonl` 写入现无按值掩码 | secret 只允许本机 runner（`secrets.go:29-31` 拒绝远程），Mask 只在本机注入，不改 worker 协议 |
| CAP-08 | 读接口与权限 | `/logs/stdout` 的 `InHandler(permDeny, PermJobReadOthers)`、mcpserver 工具注册 | 是 | 新路由与工具 | OWNER_EXTENSION | 无转录接口 | 权限文档生成 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| ACP `Request.Turns` 写入与 handler 回合缓冲（设计 0.3 §2.1 ACP 行） | CAP-05 | 删去后核心验收仍满足：`acp.jsonl` 已有每回合提示与回复全文且远程镜像回主机，另写一份是重复存储 |

## 对设计的细化

| # | 问题 | 定案 |
|---|---|---|
| R1 | `turns.jsonl` 打开点（Spec N1） | 只有 ndjson 类（claude / suga）写。照 `stdout.raw.log` 旁路的做法（`ndjson.go:87-97`），在 `captureNDJSON` 内调用 `openTurnsWriter(entry, jobID)`（新文件 `internal/job/turns_writer.go`，仅本机 runner 且 agent 登记了回合方言时打开 `store.TurnsFile`，否则 nil），经 `ndjsonfilter.Options.Turns` 传入，由 `ndjsonCapture.Close` 关闭（`execute.go:555` 先关 stdout、`:561` 再 `recordNDJSONCapture`，最后一回合能写完）。T3 不改 `execute.go` |
| R2 | 脱敏与截断（P2、G7） | 写入器在 `ndjsonfilter` 内负责截断与编码；job 层注入 `Mask func(string) string`（包装从 `secretMasker` 抽出的纯函数 `maskSecrets(text, values)`，`secrets.go` 原方法改为调用它，行为不变）。累积器按回合缓冲原文（缓冲上限 1MB，超出后丢弃中段只留首尾并标 `truncated`），回合结束时**先 Mask、再截断到 256KB、再编码**。`stdout.raw.log` 不脱敏是现有问题，T9 另立 issue。**ACP（Gov N1）**：`runner.Request` 加 `Mask func(string) string`，job 层在组装 ACP 请求处注入同一 `maskSecrets`：`submit.go:985`（`runReq.ACP = acpRequest(...)` 旁一行，值来自 `:272` 已解析的 `secretValues`）与 `session_recovery.go:106`（恢复时 `req.Secrets` 非空则用 `resolveSecrets` 重新解析后注入，解析失败则不恢复该会话、按现有恢复失败处理）。secret 只允许本机 runner（`secrets.go:29-31`），远程 job 没有授予的 secret，不改 worker 协议；`runner/acp/transcript.go` 写 `prompt` / `message` / 工具摘要的文本字段在 JSON 编码前调用它（`runner/acp/runner.go` 只加传参行）。修复前已写下的旧 `acp.jsonl` 不回写，读取时只有模式脱敏（与现有 messages 页同口径），在人工 Gate 披露 |
| R3 | 游标偏移（P3①） | `offset` 为回合正文内字节偏移，切片前向前对齐到 UTF-8 字符边界（`utf8.RuneStart`），`next.offset` 为对齐后位置 |
| R4 | 合并成功但 todo 写失败（P3②、F6） | `markMergeTodoDone` 写库失败时除 warning 外记事件 `todo.complete_failed{todo_id, job_id, err}`。卡片判定只读该事件：有则显示「已合并，待标记完成」（动作 `plan set-todo done`），不跑 git |
| R5 | 续跑判定（F1、F5、N1） | `linkTodoOutcome(snap JobResult, resumePending bool)`：`execute.go:871` 传 `guardDecision == "resume"`，`review.go:170`、`hold.go:763` 传 false。`todoCompletion` 另读行级事实 `snap.AutoResumedBy != ""` 视为续跑中、不完成（覆盖 `recordDelivery` 与 accept 路径，F1）。续跑提交失败：`execute.go:939-944` 分支内加 `if guardDecision == "resume" && !autoResumed { s.linkTodoOutcome(snap, false) }`，与该分支对 `dec.AutoResume` / `Fallback` 的既有处理并列。适用范围（Spec N5）：`AutoResumedBy` 判定只用于 `linkTodoOutcome` 与 `recordDelivery`，**不用于** `markMergeTodoDone`（merge 模式下人工合并即完成，被合并的常是原 job 的 worktree）；续跑结束后再对原 job 手工 `job deliver`，todo 由续跑 job 的结局完成，原 job 不再完成它 |
| R6 | 回合方言登记 | `builtinNDJSONDef` 加 `turns: "claude" | "suga" | ""`；`NDJSONProjectorFor` 旁新增 `NDJSONTurnsFor(agent)`；空值不写 |
| R7 | 卡片与类别（F7） | today 卡片 kind `todo_awaiting_merge`；inbox：`Signals` 加字段、`signalsOf` 加分支、类别 `awaiting_merge` 在 `Classify` 中排在 `review` 之后、`plan_stuck` 之前，i18n 词条 `inbox.waiting.awaiting_merge`；`config.DefaultInboxRules`（`internal/config/model.go:753`）默认栏「要处理」 |
| R8 | 转录读取业务的位置（F4、Spec N3） | 业务入口 `streaming.JobTranscript(jobs, id, opts)`，新文件 `internal/streaming/job_transcript.go`：ndjson 类读 `turns.jsonl`；ACP 在同包复用 `acpLineReader` / `acpNormalizer` 写逐行扫描的读取函数——按 `prompt` 事件切回合（与 `ReadTranscript` 同口径），统计工具调用，取够一页即停，单次扫描字节有上限，不整份读入；路径经 `SafeJoinUnder`；其他回退 stdout。读侧对用户文本做 `SplitInjectedRules` 与 `secret.RedactString`，与 `job_messages.go` 同口径。T4 跑 `go list -deps` 验环 |
| R9 | 回合数提示（G1、F8、Spec N2） | server 侧计算：`GET /v1/jobs/{id}`（仅单个 job，不含列表）响应加可选字段 `turn_count`，httpapi 调 `streaming.TurnCount` 填写（只数行、有上限），写入 http-api 文档；CLI `job show` / `job logs` 读该字段打印提示，`commands/job.go` 只加打印行 |
| R10 | `complete_on` 校验（G10、Spec N4） | 照 notify 的做法：新增 `jobstore.ValidPlanCompleteOn`；三个入口先检查（`commands/plan.go:820`、`plan_handler.go:337/680`、`mcpserver/server.go:1029` 附近）并返回各自的 400；jobstore 写入时再兜底检查（`plans.go:113/139/165`） |

## 前置检查与 fail-closed 条件

workspace baseline：主机建三个 worktree：`.worktrees/b1`（分支 `feat/b1`，集成）、`.worktrees/b1-a`（`feat/b1-a`，§1 链）、`.worktrees/b1-b`（`feat/b1-b`，§2 链），均基于当前 main，`.git` 改相对 gitdir，`web/node_modules` 链接主 checkout；Go 命令带 `GOWORK=off`，`GOCACHE` / `TMPDIR` 在仓库之外。

fail-closed（出现即停，回报监督会话）：

- 基线 `go test ./internal/job ./internal/jobstore ./internal/runner/... ./internal/today ./internal/inbox ./internal/streaming -count=1` 在未改动时失败。
- 需要改 worker 协议、改 `todo_done` wire 语义、或新增设计未列的权限。

## 波次与依赖

```
W0  T0 前置（Claude）
W1  [b1-a] T1 完成时机（sonnet A）       ‖ [b1-b] T3 回合写入（sonnet B）
W2  [b1-a] T2 收尾卡片 → T6 小改（sonnet A，串行） ‖ [b1-b] T4 转录读取（sonnet B）
W3  合并 b1-a → b1、b1-b → b1（Claude，`--no-ff`，解决共享文件冲突，b1 上全量测试）
W4  [b1] T7 Quick 文案 → T5 web → T8 文档（suga，依次）
W5  T9 真实验收、反向验证、全量与收尾（Claude）
```

共享文件负责方（两条链在各自 worktree 改，W3 合并时由 Claude 解决冲突）：`internal/mcpserver/server.go` 与 `backend*.go`、`internal/client/client.go`——plan 字段归 T1，转录工具归 T4；`internal/commands/job.go`——ehvd 显示归 T6，回合数提示归 T4；`internal/i18n/lang`——各任务只加自己的词条；`internal/httpapi`——plan / r20b 归 A 链，transcript 路由归 B 链；`execute.go`——只有 T1（R5）改；`submit.go`——ACP Mask 注入一行归 T3，r20b 归 T6；`session_recovery.go` 归 T3。

委派约束（写入每份任务书）：G021–G024 分层、G031、GR104（下列已超 1000 行的文件只加调用行或字段：`internal/job/{worktree,execute,submit,model}.go`、`internal/config/model.go`、`internal/commands/{job,plan}.go`、`internal/httpapi/{plan_handler,authz_routes}.go`、`internal/mcpserver/server.go`、`internal/client/client.go`、`internal/runner/acp/runner.go`）、GR109、G051–G053、SR1211（只 `git add` 本任务文件，提交前查 `git diff --cached --stat`，禁 stash / reset）；不删注释、不无关重排；不在导出 GIT_DIR / GIT_WORK_TREE 的环境跑测试；命令前台同步执行、不起空等后台任务；suga 任务禁用 web_fetch，允许改 go.mod / go.sum。复核：看 `diff --stat` 删除行，对照设计逐条核实，T9 反向验证。

## 任务

### T0 前置

- 执行方：Claude。依赖：无。路径：主机 `.worktrees/b1*`。动作：建 gofer plan 并把 T1–T9 建成 todo（job 挂到对应 todo）；先提交 tracker；建三个 worktree；基线测试；写两份 sonnet 任务书（R1–R10、共享文件负责方、委派约束）。完成标准：基线绿。

### T1 完成时机

- 执行方：sonnet A，worktree b1-a。依赖：T0。
- 文件：`internal/jobstore/plans.go` 与迁移、`internal/jobstore/testdata` 升级测试；`internal/job/todo_completion.go`（新）、`todolink.go`、`branch_delivery.go`（`recordDelivery` 一处）、`worktree.go`（`markMergeTodoDone` 与 `worktree rm` 各一处调用）、`execute.go`（R5）、`review.go`、`hold.go`（调用签名）；`internal/commands/plan.go`、`internal/commands/job.go`（worktree merge `--help` 文案）、`internal/httpapi/plan_handler.go`、`internal/mcpserver`（plan 工具与 backend）、`internal/client`。
- 动作：设计 §1 全部，R4、R5、R10。
- 验证（用例名写进汇报）：设计 §1 表每行；merge 模式 + 交付 merge + 守卫判 resume 不完成（F1）；续跑提交失败后完成或记原因（R5）；`job` 模式 `continue_from` / 分支基线回归；R4 组合；`complete_on` 非法值 400；迁移升级（G057）。`go test ./internal/jobstore ./internal/job ./internal/httpapi ./internal/commands ./internal/mcpserver ./internal/client -count=1`。

### T2 收尾卡片

- 执行方：sonnet A，b1-a。依赖：T1。
- 文件：`internal/today`（新卡片生成，新文件）、`internal/inbox/waiting.go`、`internal/config/model.go`（`DefaultInboxRules` 一行）、`internal/i18n/lang` 词条。
- 动作：R7 与设计 §1 卡片条件（按 job 行交付状态与 R4 事件，不读 note、不跑 git）。
- 验证：卡片出现 / pending 不出 / 合并后消失 / 已丢弃 / 「已合并，待标记完成」；`go test ./internal/today ./internal/inbox ./internal/config -count=1`。

### T3 回合写入

- 执行方：sonnet B，worktree b1-b。依赖：T0。
- 文件：`internal/runner/ndjsonfilter`（`TurnRecord`、写入器、累积器，新文件）、`internal/agent/registry.go`（R6）、`internal/job/turns_writer.go`（新）、`internal/job/ndjson.go`（R1 调用行）、`internal/job/secrets.go`（R2 抽函数）、`internal/store`（常量）、`internal/runner/runner.go`（`Request.Mask`）、`internal/runner/acp/transcript.go`（编码前掩码）与 `runner.go`（传参行）、`internal/job/submit.go:985` 与 `internal/job/session_recovery.go:106` 各一处注入（R2）。
- 动作：R1、R2（含 ACP 写入掩码）、R6；claude 与 suga 两种方言。
- 验证：claude、suga（含 `retry{restart:true}` 只截最近一段）写入；未登记方言不写；256KB 截断；注入 secret 后只出现掩码（含跨两条 delta、跨 256KB 边界）；ACP job 回显授予的 secret 时 `acp.jsonl` 只出现掩码；`go test ./internal/runner/... ./internal/job ./internal/agent ./internal/store -count=1`。

### T4 转录读取

- 执行方：sonnet B，b1-b。依赖：T3。
- 文件：`internal/client`（含 `JobResult.TurnCount`）、`internal/streaming/job_transcript.go`（新，R8、R9）、`internal/commands/job_transcript.go`（新）、`internal/commands/job.go`（R9 调用行）、`internal/httpapi`（handler 与 `routeDecls`）、`internal/mcpserver`（`gofer_get_job_transcript` 与 backend）、`internal/client`、`docs/reference/permissions.md`（生成）。
- 验证：参数互斥 400、上下界、search 限长；大于 64KB 回合经游标取完（含多字节字符边界）；ACP 复用 `acp.jsonl`（含远程镜像目录）；回退分页；`SafeJoinUnder` 越界拒绝；他人 job 与日志同样拒绝；`turn_count` 只在单个 job 响应里出现、多回合 CLI 有提示、单回合无；ACP 逐行读取取够一页即停（大文件用例）；CLI 真实分发与 `--out`；新 `*Opts` 登记 flag 快照；MCP 工具；`go generate ./internal/httpapi/...` 后 `TestAuthzReferenceIsUpToDate` 通过。`go test ./internal/streaming ./internal/httpapi ./internal/commands ./internal/mcpserver ./internal/client -count=1`。

### T6 小改 §4 与 juo0

- 执行方：sonnet A，b1-a。依赖：T2。
- 文件：`internal/job/fallback.go`（`:32-49` 返回被跳过候选、`:328`）、`internal/job/todo_continue.go`（`:237-240` juo0）、`internal/job/rebuild.go`（`RebuildJob` 带 `WorktreeFrom`）、`internal/job/submit.go` 或其校验处（r20b 校验顺序）、`internal/commands/job.go`（`job show` 显示 ehvd 事件，调用行）、相关测试。
- 验证：ehvd 事件写入且 `job show` 可见；r20b 400；5x85 rerun 409；juo0 口径与续跑守卫一致；`go test ./internal/job ./internal/httpapi ./internal/commands -count=1`。

### T7 Quick 文案

- 执行方：suga（gpt），`gofer job run -p my-tools-dev -a suga --runner local --cwd inhere-tools/gofer/.worktrees/b1 --plan <本计划 plan>`（派发前用一个只读 exec job 核对 cwd 落在 b1）。T5、T8 同。依赖：W3。
- 文件：`internal/i18n/prompts/zh-CN|en/{auto-resume,uncommitted-resume,delivery-check-resume}.md.tmpl`、`internal/job/fallback.go` 的 `fallbackPrompt`（gqap）；`internal/brief/refocus.go:174`、`refocus_test.go`、`prime_refocus_test.go`、`internal/brief/testdata/refocus.txt`、`skills/gofer-usage/references/tracker.md`（8hlc）。
- 验证：`go test ./internal/i18n/... ./internal/brief ./internal/job -run 'Prompt|Refocus|Fallback' -count=1`；单独提交并回报 hash。

### T5 web

- 执行方：suga（gpt），b1。依赖：T7（同一 worktree 串行）；后端字段 `complete_on`（T1）、卡片 kind `todo_awaiting_merge` 与类别 `awaiting_merge`（T2）、事件 `job.fallback_skipped`（T6）与 `todo.complete_failed`（R4）。
- 文件：计划页 `complete_on` 显示与设置、今天 / 收件箱新卡片的文案与动作、`web/src/utils/eventMeta.ts` 两个新事件、`web/src/i18n`。
- 验证：主机 `npx vue-tsc --noEmit && npx vitest run && npx vite build`；单独提交并回报 hash。

### T8 文档

- 执行方：suga（gpt），b1。依赖：T5。
- 文件：设计验收「文档同步」所列全部文件、`CHANGELOG.md`；另加 `todo.complete_failed` 事件登记（http-api 事件清单）、「已合并，待标记完成」卡片状态（web-console、plans-workflows）、`GET /v1/jobs/{id}` 的 `turn_count` 字段（http-api，注明只在单个 job 响应中出现）、CHANGELOG 记 ACP 记录写入时按授予 secret 值掩码（修复前的旧记录不回写）。G046：不写 issue id、本机路径。
- 验证：私有名检查；我审删除行。

### T9 真实验收、反向验证、全量与收尾

- 执行方：Claude。依赖：T8。
- 真实验收（主机临时 server，临时 git 仓库）：`complete_on=merge` plan 两个 worktree todo 串联，第一个完成后第二个不派发，「等我」出现「待你收尾」，`worktree merge` 后派发第二个；一个两回合 suga job 与一个 ACP job 的 `job transcript --outline` / `--turns` / `--out`，`job show` 有回合提示。记录 `tmp/b1-impl/acceptance.md`。
- 反向（副本，不提交；先确认破坏后的副本能编译）：去掉 §1 判定；去掉 `AutoResumedBy` 判定；去掉 suga delta 累积；去掉 R5 补调；去掉 `turns.jsonl` 脱敏；去掉 ACP 写入掩码；去掉 UTF-8 对齐；去掉 `SafeJoinUnder`——对应用例必须失败。记录 `tmp/b1-impl/reverse.md`。
- 收尾：`git diff --stat main...feat/b1` 查无关删除；全分支私有名检查；gofer-verify 四路；`feat/b1` 合并到 main（`--no-ff`）；删三个 worktree 与分支；设计稿实施记录（含 ACP 复用的偏离）；计划 COMPLETE 总结；关 issue；另立 issue：`stdout.raw.log` 不脱敏（R2）；向 suga 会话发送 gofer-ohf7 的请求。

## 回滚与恢复

单独回退合并提交；plans 表 `complete_on` 列保留无害；结果目录多出的 `turns.jsonl` 无害。

## 人工 Gate

- 本计划批准后开始 T0。
- 随本计划一并请用户确认的、对已批准设计的偏离：设计 §2.1 写 ACP 由 runner 另写 `turns.jsonl`；代码里 ACP 已有含全文的 `artifacts/acp.jsonl`（远程镜像回主机），本计划改为直接复用，转录读取业务因此放在 `internal/streaming`（设计写 `job.Service.Transcript`）。用户可见结果相同，且远程 ACP job 也能读。为补齐复用带来的脱敏缺口，本计划同时给 `acp.jsonl` 的写入加上按授予 secret 值的掩码（也修复现有 messages 页的同一缺口）；**修复前已产生的旧 ACP 记录不回写**，读取时只有模式脱敏，与现有 messages 页一致。
- 发版：攒批另行请示。

## 可追溯性

| 设计项 | 任务 | 验证 |
|---|---|---|
| §1 完成时机、续跑修正、merge 即完成、rm、远程 | T1 | T1 用例、T9 验收与反向 |
| §1 待你收尾卡片 | T2、T5 | T2 用例、T9 验收 |
| §2.1 回合写入与脱敏（ndjson 类）、ACP 复用 | T3、T4 | T3 / T4 用例、T9 反向 |
| §2.2 读取接口与回合提示 | T4 | T4 用例、T9 验收 |
| §3 Quick | T6（juo0）、T7 | focused test |
| §4 小改 | T6、T5（事件显示）、T8 | focused test、文档 |
| 文档同步 | T8 | 私有名检查 |

## 完成 Gate 与剩余工作

T9 完成即本计划完成；发版另行进行。
