<!-- template_id: plan; template_version: 1.2.0 -->
# TRK-01 本地核心（P2）实施计划

> 状态：Draft 0.1 / 待人工计划批准；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-27 | Codex | 将已批准 TRK-01 P2 拆成红测试、存储核心、纯本地 CLI、文档与验证波次 |

## 目标与完成定义

`thinking_mode=RIGOROUS`。核心目标：在没有 gofer server 时，用仓库内 `.gofer/tracker/` 持久管理 issue 和 memory，并让 `repo init|status`、`issue`、`memory` 命令从仓库子目录正常工作。`allowed_scope=TRK-01 P2 本地核心`；`non_goals=P3 prime/migrate/hooks、P4 sync/server/web/job 联动、GIT-01、真实服务操作`；`expansion_policy=DEFER_OR_REQUEST`。评审预算为一轮发现、一轮核对；发现改变已批准设计的 CORE_BLOCKING 冲突即停。

完成需同时满足七个固定名测试的真实行为断言、Windows/Linux build、vet、指定包测试、全仓 gofmt 无输出、临时目录二进制 smoke、控制字符扫描、README 与设计 P2 实测记录，以及按功能点分开的本地提交。测试红提交、编译成功或命令帮助可见，单独均不算完成。

## 范围、排除项与授权

- Git 根仅为 `D:/work/inhere/hyy-ai-inspect/tools/gofer`。新增 `internal/tracker` 作为本地数据 owner；`internal/commands` 只绑定参数、校验、调用 tracker 并格式化输出，保持 G021/G022 单向依赖。设计明确准许 `repo`、`issue`、`memory` 三个核心顶层组，符合 G033 的核心资源例外。
- P2 的 `repo init` 只执行设计步骤 1–3；步骤 4 hooks 与步骤 5 首次同步留到 P3/P4，不创建有实际副作用的占位实现。`repo status` 的同步和 hooks 项明确显示“未实现（P3/P4）”。`--no-hooks` 若按已批准 CLI 表面保留，只在 P2 标示 hooks 未实施，不声称已安装。`repo prime|migrate|sync` 本期不注册可运行的子命令。
- issue/memory 纯本地，不读取 server 配置、不作网络探测或自动同步。没有 tracker 时拒绝并提示 `gofer repo init`。`repo init` 对现有 BEADS 托管块只提示 `repo migrate --from-bd`，不删除或改写。
- `host_or_non_offline_action=NOT_APPLICABLE`。允许本地计划、测试、代码、文档的原子提交；禁止 push、部署、迁移真实 bd 数据、重启/reload 运行中 gofer 进程或触碰 `D:/work/inhere/config/win-env/gofer`。

## 输入与批准证据

- 设计：[`Approved 0.2 TRK-01 本地存储与 CLI`](../design/2026-09-27-local-first-tracker-and-uncommitted-guard-design.md)，2026-09-27 的批准记录写在设计内；本期只消费 P2。
- 本 job 明确授权编写并本地提交计划候选，提交后停在审批点；监督者代批实施计划的权限写在本 job 说明，当前尚未收到本候选的批准或实施续接请求。
- 仓库约束：`AGENTS.md` G021/G022 分层、G032 兼容、G033 命令分组；工作区 `workspace.md` 的独立 Git 根、本地提交与临时产物规则；本 job 的测试先提交、LF/UTF-8 无 BOM、禁止 push 和真实服务操作规则。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 本地 issue/memory 真源、锁与原子写 | `internal/jobstore` SQLite、现有 JSONL 日志、Go `os`/`encoding/json`/`filepath` | JSONL 日志不承担可编辑仓库真源；SQLite 不满足入 Git 的行级 diff | 不能在 jobstore 内扩展而不混入 server DB 生命周期 | MINIMAL_NEW_MODULE | 需独立、离线、仓库内可提交的完整快照和跨进程文件锁 | 唯一 owner 为 `internal/tracker`，底层用 stdlib；不再建第二个缓存或服务端存储真源 |
| CAP-02 | repo/issue/memory CLI | `internal/commands/app.go`、`plan.go`、`config.go`、`flags.go`、gcli v3 | 直接复用现有 gcli app、子命令、输出与参数绑定模式 | 在 `internal/commands` 扩展三个资源组，所有数据动作转 tracker | OWNER_EXTENSION | 现有命令没有本地 tracker 入口 | 不引入第二 CLI 框架，避免读本地时回落到真实 server |
| CAP-03 | UUID、YAML 与身份 | 已安装 `github.com/google/uuid`、现有 YAML 依赖、Go `os/user`/`os/exec` | 复用现有依赖和 stdlib | 在 tracker/commands 的既有 owner 内封装最小读写与 caller 获取 | DIRECT_REUSE | 无新增依赖必要 | 不建通用身份服务；git user.name 只作本地 fallback |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| TRK | CAP-01 | `internal/tracker` | `internal/jobstore`、JSONL 日志、Go stdlib | 现有存储以 server SQLite/追加日志为 owner，不提供可编辑、稳定排序的仓库真源 | 加进 jobstore 会让离线 CLI 依赖 server 存储职责；commands 不能承载锁与数据业务 | P2 必须有跨 Windows/Linux 的本地读改写与仓库发现 | tracker 独占 `.gofer/tracker/` 数据合同；P3/P4 复用本包，不复制模型 | 若已有 owner 后续能完整覆盖 P2，合并到该 owner 并删除新包；不得并存双真源 |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 新 CLI 框架 | CAP-02 | gcli 已支持资源组与子命令，加入第二框架只会重复解析与帮助系统 |
| SQLite tracker 真源 | CAP-01 | 无法满足设计指定的仓库内按 id 排序 JSONL、单行 diff 与 Git 提交验收 |

## 前置检查与 fail-closed 条件

- Workspace baseline（起草时）：Git root `D:/work/inhere/hyy-ai-inspect/tools/gofer`，branch `main`，HEAD `83a75d4469b22ccbc1ea6b3293abf512ccfbe97b`，`git status --short` 无输出；in-scope dirty/untracked 与 preserved unrelated dirty 均为无。实施前重新核对。工作区 `tmp/idev-std/` 不属于本 Git 根。
- 图索引项目 `gofer` 的 generation 为 `2026-09-05T06:59:33Z`；`internal/commands/{app,config,flags,plan}.go` 已 `metadata_changed`，设计文档不在索引内，`internal/tracker` 尚不存在。已用当前源码核实 app 注册、gcli 模式、`bindConfigFlag` 和现有 UUID 依赖；实施落点以最新源码再定向核查，不把旧图当成当前代码证据。
- 预计 owner：`internal/tracker/{model,store,lock,id,discovery,ready,config}.go` 及对应 `*_test.go`；`internal/commands/{app,repo,issue,memory}.go` 及 CLI 测试；`README.md`、设计文档的 P2 实测记录。实际文件可按职责合并/拆分；新增路径在 mutation 前依计划合同分类，不以本列表屏蔽合法同 owner 文件。
- P2 执行前重新完成 IDEV-STD BOUND、semantic load、fingerprint Gate 与适用 preflight；preflight 的 context usage `unknown` 按 0.22.1 当前合同判断，不自行跳过。核对设计、`git status --short`、Go 版本与命令现状。若发现 schema/接口语义、设计验收、数据安全、owner 或授权冲突，fail closed，不在实现中自改协议。
- smoke 使用临时仓库、临时配置、随机端口；所有 gofer 命令都显式给 `-c <临时配置>`（如需 server 参数则显式随机 localhost 地址），绝不依赖默认 server。单测使用 `t.TempDir()`；不碰真实 gofer 配置或进程。

## 波次与依赖

T00 当前源码与基线核查 → T1 七项红测试单独提交 → T2 tracker 本地核心 → T3 三组 CLI → T4 README/设计实测与最终验证。按功能点逐波 conventional commit，测试先独立 red 提交；T2 和 T3 不与文档混交。各波结束记录状态、实际路径、验证 exit code/原始输出与 commit，实施发现按 Operational Discovery、Corrective、Semantic Amendment、Ownership Conflict 分类；语义变更与归属冲突立即停止。

## 任务

### T00 定向核查

- 文件：只读 `internal/commands/{app,plan,config,flags}.go`、相关 CLI 测试、`go.mod`、`README.md` 与设计。
- 动作：核对 gcli 子命令/flags 及命令测试夹具、现有 Go 依赖、CLI 输出惯例、`--tracker` 可绑定层级；记录 Git HEAD/dirty 与 G032 旧路径。确认 `internal/tracker` 不存在，判定任何现有等价 owner。
- 验证：图索引及 `check_index_coverage` 后对 stale/missing 路径读当前源码；`git status --short`、`git log -1 --oneline`。
- 完成标准：T1–T3 的具体 owner 与测试入口明确，且无未裁决的语义冲突或他人 dirty 文件。
- 依赖：计划批准及当前实施续接请求。

### T1 七项测试先行并提交 red

- 文件：`internal/tracker/*_test.go`、`internal/commands/{repo,issue,memory}*_test.go`（按测试职责命名）；不写 `t.Skip` 占位。
- 动作：写 `TestTrackerRoundTripStableOrder`（乱序输入、固定字段序、改一条只动一行、重写字节不变）、`TestTrackerLockContention`（两个 goroutine 各 50 次 RMW、100 条 notes、不丢写、30 秒过期锁可抢且内容可读）、`TestIssueReadyRespectsDeps`（A blocks B、关闭后 ready、priority→created_at）、`TestIssueIDGeneration`（prefix/四位 base36/冲突重试/子项递增）、`TestMemoryCRUD`（set/ls keyword/show/rm 与 remember/forget 别名）、`TestRepoInitIdempotent`（两次相同、托管块一次、BEADS 保留并提示、gitignore 不重复）、`TestTrackerDiscovery`（上溯最近 tracker 与显式覆盖）。CLI 测试经真实 gcli 命令入口，所有文件夹用 `t.TempDir()`。
- 验证：`go test ./internal/tracker/ ./internal/commands/ -run 'Test(TrackerRoundTripStableOrder|TrackerLockContention|IssueReadyRespectsDeps|IssueIDGeneration|MemoryCRUD|RepoInitIdempotent|TrackerDiscovery)$' -count=1`；记录 exit code 与失败行，确认红因缺 P2 行为而非测试语法、环境或夹具错误。
- 完成标准：七项有有效 red 证据，`git status --short` 和 staged paths 核对后仅测试文件进入独立 `test(tracker): cover local tracker and CLI` 本地提交。
- 依赖：T00。

### T2 `internal/tracker` 本地核心

- 文件：`internal/tracker/{model,store,lock,id,discovery,ready,config}.go`（可按职责合并）及 T1 tracker 测试。
- 动作：定义设计字段模型与固定 JSON 字段序；JSONL 按 id/key 排序，UTF-8、LF、末尾换行，未改记录保持单行稳定。写入全程持有 `.gofer/tracker/.local/lock`，用 `O_CREATE|O_EXCL`、pid/host/时间、30 秒过期抢锁，锁归属核验避免误删；同目录临时文件加 flush/close 后原子替换，Windows rename 覆盖现有目标走受控策略，错误不丢原文件。实现仓库发现/显式目录、config、随机 base36 ID 与冲突重试、子项下一序号、ready 依赖过滤/排序、memory CRUD；输入校验和错误反馈由本包统一处理。
- 验证：T1 的 tracker 测试由红转绿；锁争用/过期与 Windows 文件替换路径在指定包测试中真实执行；`go test ./internal/tracker/ -count=1`。
- 完成标准：独立本地存储与并发写行为可观察，G021/G022 不倒依赖；单独 `feat(tracker): add local issue and memory store` 提交。
- 依赖：T1。

### T3 纯本地 `repo`、`issue`、`memory` CLI

- 文件：`internal/commands/app.go`、`internal/commands/{repo,issue,memory}.go` 及 T1 CLI 测试。
- 动作：注册顶层三个资源组，仅 `repo init|status`、`issue ready|ls|show|create|update|close|dep add`、`memory set|ls|show|rm` 与 `remember|forget` 别名。绑定每组/适用子命令的 `--tracker` 与 `--json`，不触发全局 server/config 回落。`repo init` 建 config、空 JSONL、`.gofer/.gitignore` 和 gofer 托管块；默认 prefix 取目录名，UUID 只在首次 init 生成；只在现有 AGENTS/CLAUDE 文件写块，两者均无则创建 AGENTS；保留 BEADS 块并提示迁移。`repo status` 报路径、状态计数、memory 数、提交策略、块存在性和 P3/P4 未实现项。issue flags 对齐设计表；`update --claim` 写 in_progress、assignee（`GOFER_CALLER`→git user.name→OS 用户）、started_at，`--append-notes` 追加 `{at,by,text}`，`close --reason`；人读输出与 `--json` 对应，缺 tracker 明确报 init 提示。
- 验证：T1 CLI 测试转绿；`go test ./internal/commands/ -count=1`，临时目录中用构建二进制按本 job 指定序列跑 init→issue create/update/dep/close/ready→memory set/ls/rm→repo status，并核对 JSON 输出和无 tracker 错误。每条 smoke 命令显式 `-c <临时配置>`。
- 完成标准：全 CLI 脱离 server 可用，幂等与发现行为可观察；按 `repo` 与 `issue/memory` 功能点分别提交 `feat(tracker): add repository commands`、`feat(tracker): add local issue and memory commands`。
- 依赖：T2。

### T4 文档、质量门与报告

- 文件：`README.md`、`docs/design/2026-09-27-local-first-tracker-and-uncommitted-guard-design.md`（仅追加 P2 实测记录）。
- 动作：README 新增“本地 issue / memory”一节，记录 init、离线命令、数据路径与 P3/P4 暂缺项；设计附上 P2 实测输出和边界，不改 Approved 0.2 决策。列 G032 本期新增的 `// DEPRECATED(vX): remove in vY` 旧路径及直接删除项；无则写“无”。
- 验证：全仓 `gofmt -l` 必须 exit 0 且无输出；`GOOS=windows` 与 `GOOS=linux` 的 `go build ./cmd/gofer` 产物进仓库 `tmp/`；`go vet ./...`；`go test ./internal/tracker/ ./internal/commands/ -count=1`；T3 的完整二进制 smoke；控制字符扫描（排除允许的 tab/LF/CR 及二进制/临时产物）；`git diff --check`。逐命令保留 exit code 和原始 gofmt/build/vet/test PASS/FAIL/smoke/扫描输出；慢测试等最终退出后汇报。
- 完成标准：README 与设计记录和实测一致；无未裁决失败，精确 staged paths 核对后独立 `docs(tracker): document local issue and memory CLI` 提交；最终 `git log`、`git status --short`、G032 清单与未完成项一并报告。
- 依赖：T3。

## 回滚与恢复

每个功能点提交即恢复点。每次提交前执行 `git status --short`，只暂存本波 owner 路径并复核 `git diff --cached --name-only`；遇到他人 dirty 工作停下确认归属。失败保留已验证提交和原始输出，先定位首个失败边界；如需撤回，针对本任务准确 commit 提出 `git revert` 路径并核对后续依赖，不使用清理/重置覆盖他人改动。progress 独立记波次状态、实际文件、验证结果、commit 与 lifecycle，不通过改批准版计划记录进度。

## 人工 Gate

本候选提交后停在人工计划批准 Gate。监督者可按本 job 的代批授权给出批准；此后仍要有明确续接执行请求，执行方式为 DIRECT_CONTINUOUS。实施发现协议/设计语义、数据合同、风险、验收或 owner 变动时返回设计/计划 Gate；真实配置、服务、远端动作不属于 P2。

## 可追溯性

| Approved TRK-01 / 本 job 验收 | 任务 | 可观察验证 |
|---|---|---|
| JSONL 稳定排序、字段序、单行 diff 与原子写 | T1、T2 | `TestTrackerRoundTripStableOrder`、目标文件字节和 diff |
| O_EXCL 锁、过期抢占与并发 100 次写 | T1、T2 | `TestTrackerLockContention` |
| ready 的 blocks 依赖及 priority/时间排序；随机/子项 ID | T1、T2 | `TestIssueReadyRespectsDeps`、`TestIssueIDGeneration` |
| 仓库发现与 `--tracker`，无 tracker 不自动创建 | T1–T3 | `TestTrackerDiscovery`、临时目录 smoke |
| init 幂等、托管块与 BEADS 保留、status 的 P3/P4 边界 | T1、T3 | `TestRepoInitIdempotent`、`repo status` smoke |
| issue 操作、claim/notes/close/dep、memory CRUD/别名与 JSON | T1、T3 | `TestMemoryCRUD`、CLI 包测试、二进制 smoke |
| G021/G022/G032、离线构建质量和文档 | T00、T2–T4 | build/vet/gofmt/test/控制字符输出、README 与 P2 实测记录 |

## 完成 Gate 与剩余工作

P2 的完成以七项测试、离线二进制 smoke、质量门、文档和本地功能点提交的证据闭合为准；不把本地验证说成生产或远端验收。P3 的 prime/migrate/hooks 与 P4 的 sync/server/web/job 联动继续按 Approved 0.2 独立安排；任何未完成或因已知基线失败受限的检查，最终报告写明原始输出、影响与下一步，不以 `t.Skip` 或骨架替代。
