<!-- template_id: plan; template_version: 1.2.0 -->
# GIT-01 未提交改动守卫（P1）实施计划

> 状态：Draft 0.1 / 待人工计划批准；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-27 | Codex | 将 Approved 0.2 的 GIT-01 拆为测试、后端、worker 协议、Web 和文档波次 |

## 目标与完成定义

`thinking_mode=RIGOROUS`。核心目标：agent job 结束时准确标出本轮新增且仍未提交的文件，并按项目策略提醒、转验收或续接一次。`allowed_scope=GIT-01 P1`；`non_goals=TRK-01、部署与真实 gofer 进程操作`；`expansion_policy=DEFER_OR_REQUEST`。评审预算为一轮发现、一轮核对；发现改变设计语义或 owner 的 CORE_BLOCKING 问题即停止并回到设计/计划 Gate。

完成须同时满足：四个指定测试覆盖真实行为，字段从本机或 worker 结果持久化到查询结果，Web 三处显示「未提交 N」及文件列表；`off|warn|review|resume`、忽略规则与嵌套仓库行为符合设计；规定的 Go/Web 检查和控制字符扫描有原始输出；各功能点本地原子提交，保留 clean gofmt。测试编译、build 或 Web 构建单独成功均不算功能验收。

## 范围、排除项与授权

- 仅改 `tools/gofer` 独立 Git 仓库。P1 不实现 TRK-01、`gofer repo`、issue/memory、迁移、同步或生产配置。
- `on_uncommitted` 默认 `warn`；项目级 `uncommitted_ignore` 是 P1 配置。设计风险段提到未来 `.gofer/tracker/config.yaml`，但 P1 与 TRK-01 独立，按本任务指定的现有项目配置 owner 落地；若实施发现必须改变该配置来源或协议语义，停止并提交人工决策。
- `host_or_non_offline_action=NOT_APPLICABLE`。允许本地文档/代码/测试提交；不 push、release、部署、迁移、重启/reload server/worker、触碰真实配置或硬件。
- 所有测试仓库、配置和 smoke 环境使用 `t.TempDir()` / 临时目录、随机端口；每条 smoke gofer 命令显式指定 `--server http://127.0.0.1:<端口>` 或 `-c <临时配置>`。

## 输入与批准证据

- 设计：[`Approved 0.2 GIT-01`](../design/2026-09-27-local-first-tracker-and-uncommitted-guard-design.md)，其中 P1、默认 `warn` 已于 2026-09-27 获人工批准。
- 本 job 任务书授权生成并本地提交本计划候选，要求提交后停止；监督者代为批准计划的授权来自本 job 说明，尚无本计划批准证据。获批后的当前续接请求才进入实施。
- 仓库约束：`AGENTS.md` 的 G021/G022 分层、G032 兼容策略；`workspace.md` 的独立 Git 根与本地原子提交规则；gofer 注入规则的测试先行、禁止 push 和运行中进程操作。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | job 基线和终态 Git 采集 | `captureBaseSHA`、`captureDiff`、`captureWorktreeDiff`、`gitdiff.go`、`worktree.go` | 复用已有 Git 命令执行与 worktree cwd | 在 `internal/job` 扩展快照和比较，独立于 `capture_diff` 开关 | OWNER_EXTENSION | 现有 diff 摘要不保存起始脏路径及内容哈希 | 不建第二套 Git 服务；快照只活在本次 job 生命周期 |
| CAP-02 | 结果、事件、策略与续接 | `JobResult`、`jobstore.JobRecord`、`recordEvent`、`needsReview`、`autoResumeEligible`、`autoResume`、`ProjectConfig` | 复用现有状态机、事件和续接预算 | 在既有 job/config/jobstore owner 增字段及策略决策 | OWNER_EXTENSION | 现有结果没有未提交字段，终态无该策略 | 不另建状态机；review 必须在原终态锁内决定 |
| CAP-03 | worker 回传与 Web 显示 | `outcomeFrame`、`wsproto.Outcome`、`applyOutcome`、Web job/Board/工作台视图、`eventMeta.ts` | 复用现有 Outcome 帧、结果 API 与徽标模式 | additive 字段透传并在现有组件显示 | OWNER_EXTENSION | 现有帧和视图均缺未提交数量/路径 | 不改协议语义、不建平行 API；按仓库惯例处理版本 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 前置检查与 fail-closed 条件

- Workspace baseline（计划起草时）：Git root `D:/work/inhere/hyy-ai-inspect/tools/gofer`，branch `main`，HEAD `e40d4a68f7ae106caf8a342396ef89dd5aae082e`，`git status --short --untracked-files=all` 无输出；in-scope dirty/untracked 与 preserved unrelated dirty 均为无。实施前重取快照，任何不明归属改动先停。
- 预计 owner：`internal/job/{execute,outcomes,gitdiff,worktree,model,persistence,events}.go`、`internal/jobstore/jobs.go` 及迁移文件、`internal/config/{model,loader,editable}.go`、`internal/worker/dispatch.go`、`internal/wsproto/frames.go`、相关测试、`web/src/api/types.ts`、job 详情/Board/工作台现有视图及 `web/src/utils/eventMeta.ts`、`README.md`、本设计的 P1 实测记录。具体 Web 组件与数据库迁移文件由实施前定向发现确认，不以此列表排除必要的同 owner 路径。
- 图索引 generation 为 `2026-09-05T06:59:33Z`，上述多数源码 `metadata_changed`、部分 `not_tracked`，`web/src/api/types.ts` 1–989 为 partial；实施必须以当前源码复核。当前源码已核实 `captureBaseSHA` 位于 `execute.go:187`，`captureWorktreeDiff` 位于 `worktree.go`，Outcome 帧在 `frames.go:535`，结果持久化另经过 `jobstore/jobs.go`。
- 不修改真实 `D:/work/inhere/config/win-env/gofer`；不启动/重启运行中服务。发现设计与当前代码冲突、需要扩协议语义、影响 G021/G022 或不明 owner 时 fail closed。

## 波次与依赖

T00 定向核查 → T1 四项红测试并单独提交 → T2 本机后端与持久化 → T3 worker/协议 → T4 Web → T5 README 与设计 P1 实测记录。每波验证并按功能点单独 conventional commit；前一波未形成可复核结果不进入下一波。实施发现新路径按 Operational Discovery、Corrective、Semantic Amendment、Ownership Conflict 分类记录；后两类中的语义变更或归属冲突停止。

## 任务

### T00 基线与落点核查

- 文件：只读 `internal/job/`、`internal/jobstore/`、`internal/config/`、`internal/worker/`、`internal/wsproto/`、`web/src/`、现有 migration 与测试文件。
- 动作：核对 job 类型与 Git cwd、起始/终态时序、worker 结果回传、存储列及 Web 三个具体视图；记录当前 `git status --short`、`git log -1`、配置/协议版本约定和 G032 旧路径。只做相关最小核查，不跑全仓基线。
- 验证：定向源码读与现有相关测试名检索；记录确切路径、符号及发现分类。
- 完成标准：T1–T4 的具体 owner、依赖和当前脏改动归属明确，无未裁决的语义冲突。
- 依赖：本计划获批及新的当前执行请求。

### T1 测试先行并提交红测试

- 文件：`internal/job/*_test.go`、`internal/worker/*_test.go`、`internal/wsproto/*_test.go`，必要时 `internal/config/*_test.go`、`internal/jobstore/*_test.go`；不写占位 `t.Skip`。
- 动作：写固定名 `TestUncommittedDetectsNewDirtyOnly`（基线脏 A、未跟踪 B；改 A、建 C、提交 D；仅 A/C；忽略文件与 exec 跳过）、`TestUncommittedNestedRepo`（cwd 相对路径）、`TestUncommittedPolicyReviewAndResume`（review 锁内终态、一次续接固定 prompt、预算、再次未提交降 review、ignore、无 session）、`TestUncommittedWorkerFrameRoundTrip`（帧与落库）；所有 Git fixture 使用 `t.TempDir()`。
- 验证：只运行四个目标测试，保存 exit code 与失败行；`git status --short`、精确 staged path 核对。
- 完成标准：测试因尚未实现的 GIT-01 行为而红，非语法/环境错误；独立 `test(job): cover uncommitted guard behavior` 本地提交。
- 依赖：T00。

### T2 本机检测、策略与持久化

- 文件：`internal/job/{execute,outcomes,gitdiff,worktree,model,persistence,events}.go`、`internal/jobstore/jobs.go` 与其 schema/migration、`internal/config/{model,loader,editable}.go`，及 T1 相关测试。
- 动作：agent 开始时记录 `git status --porcelain=v2 -z` 脏路径及 `git hash-object` 内容哈希；结束时同口径比对，只计新增脏路径或基线脏但内容变更者。处理已跟踪、未跟踪（`--exclude-standard`）、rename/删除、空格/特殊字符路径；扫描 cwd 下深度 ≤2、未被外层忽略且非 `node_modules`/`tmp`/`vendor` 的嵌套 `.git`，worktree job 使用 worktree。过滤 `uncommitted_ignore` glob，结果保存总数和最多 200 个稳定排序相对路径，事件 `job.uncommitted` detail 含 count 与前 20 路径。配置 `off|warn|review|resume` 默认 `warn` 并登记可编辑策略；review 与 resume 决策接入既有终态锁、`auto_resume_max` 和同会话续接。固定 prompt 必须包含前 20 路径；无 session 或续接后仍有未提交转 review。
- 验证：T1 本机测试由红转绿；配置字段策略测试、jobstore round trip 和相关 `internal/job` focused 测试。
- 完成标准：本机 agent 的检测、事件、策略、查询结果持久化可观察；exec/非 Git cwd 不产生未提交检测；独立后端功能提交。
- 依赖：T1。

### T3 worker 与协议字段贯通

- 文件：`internal/worker/dispatch.go`、`internal/wsproto/frames.go`、`internal/job/outcomes.go` 的 worker outcome 接收段，相关 worker/wsproto/job 测试；必要的现有协议版本常量文件。
- 动作：把数量及前 200 路径作为 additive 可选字段随结果帧传回，host 按既有 DiffSummary 路径落库；不改旧字段含义。检查仓库协议版本惯例；G032 需要保留的旧路径加 `// DEPRECATED(vX): remove in vY`，无使用者的旧分支删除并列清单。
- 验证：`TestUncommittedWorkerFrameRoundTrip` 从 worker 构帧到 host 持久化转绿；相关 wsproto/worker 测试。
- 完成标准：本机与远端 worker 查询结果一致，老字段行为保持；独立协议/worker 功能提交。
- 依赖：T2。

### T4 Web 徽标与事件

- 文件：`web/src/api/types.ts`、T00 确认的 job 详情、Board 行和工作台会话头组件、`web/src/utils/eventMeta.ts`，及现有对应测试。
- 动作：三处显示「未提交 N」徽标，点击可看文件路径及总数；`job.uncommitted` 显示中文「未提交改动」。复用现有结果和事件数据流。
- 验证：相关 Web 测试、`pnpm typecheck`、`pnpm build`；人工检视三处含 `count>200` 的计数与列表截断表达。
- 完成标准：三个入口和事件可观察，独立 Web 提交。
- 依赖：T3。

### T5 文档与最终核验

- 文件：`README.md`、`docs/design/2026-09-27-local-first-tracker-and-uncommitted-guard-design.md`（只追加「P1 实测记录」），必要的既有 progress 记录。
- 动作：记录 P1 配置、行为、验证边界与真实证据；列 G032 保留标记和删除项。执行全仓 `gofmt -l`（必须无输出）、Windows/Linux build、`go vet ./...`、指定五包 `go test ./internal/job/ ./internal/worker/ ./internal/wsproto/ ./internal/config/ ./internal/httpapi/ -count=1`、Web `pnpm test && pnpm typecheck && pnpm build`、控制字符扫描。Windows 慢包可后台跑，但最终报告等待退出码与 PASS/FAIL 行。已知 `TestPolicyCacheRoundTrip` 0600 基线失败只记录，不顺带修。
- 验证：每条命令同时记 exit code 和原始输出；`git diff --check`、`git status --short`、`git log`、精确 staged path 核对。
- 完成标准：README 与 P1 实测记录准确，全部要求的结果与限制可复核；独立 docs 提交，最终报告给各任务状态和 hash、原始输出、G032 清单、未完事项。
- 依赖：T4。

## 回滚与恢复

每波提交是恢复点；失败时保留已验证提交和原始输出，按首个失败边界修复，不重置或覆盖他人改动。提交前始终 `git status --short`，只暂存本波 owner 文件并核对 `git diff --cached --name-only`。可逆回滚仅针对本任务精确提交制定 `git revert` 候选，执行前核对后续依赖与 dirty 归属；不推送。progress 另记任务状态、实际路径、验证命令/结果、commit、lifecycle 状态，不用修改批准版计划承载进度。

## 人工 Gate

本候选提交后停在计划批准 Gate。监督者可依据本 job 明示的代批授权批准；随后仍需明确续接执行请求。若实施需改变项目配置来源、wire 语义、设计验收、真实配置或服务状态，返回相应人工 Gate。任何 push、release、部署、迁移或真实进程操作均不在本计划授权内。

## 可追溯性

| Approved GIT-01 要求 / 本 job 验收 | 任务 | 可观察验证 |
|---|---|---|
| 本次新增脏路径、hash 变化、忽略、嵌套与 worktree、exec 排除 | T1、T2 | `TestUncommittedDetectsNewDirtyOnly`、`TestUncommittedNestedRepo` |
| 结果前 200/总数、事件前 20、项目策略与固定续接 prompt | T1、T2 | `TestUncommittedPolicyReviewAndResume`、jobstore round trip、事件断言 |
| worker Outcome additive 回传并落库 | T1、T3 | `TestUncommittedWorkerFrameRoundTrip` |
| job 详情、Board、工作台徽标和中文事件标签 | T4 | Web 测试、typecheck/build、三处人工检视 |
| G021/G022/G032 与构建质量、文档实测 | T00、T3、T5 | Go build/vet/test、gofmt、控制字符扫描、README/设计记录 |

## 完成 Gate 与剩余工作

P1 完成条件是所有波次代码、文档、测试和本地提交证据闭合，且无未裁决 CORE_BLOCKING 问题。P2–P4 TRK-01、真实 server/worker 升级、真机 agent 端到端验收均另行安排；最终只报告已获得的证据，不把本地测试说成远端运行验收。
