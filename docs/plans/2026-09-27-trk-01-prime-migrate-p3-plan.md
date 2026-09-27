<!-- template_id: plan; template_version: 1.2.0 -->
# TRK-01 repo prime、hooks 与 bd 迁移（P3）实施计划

> 状态：Draft 0.1 / 待人工计划批准；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-27 | Codex | 将已批准 P3 拆为红测试、prime、hooks、迁移、job 注入与离线验证波次 |

## 目标与完成定义

`thinking_mode=RIGOROUS`。核心目标：让本地 tracker 在会话开场提供有界上下文，将 repo init 的 SessionStart hooks 装到 Claude/Codex，并以默认 dry-run 的命令从 bd 数据迁入 tracker。`allowed_scope=TRK-01 P3`；`non_goals=P4 sync/server/web/issue 联动、GIT-01、真实仓库迁移与试点验收`；`expansion_policy=DEFER_OR_REQUEST`。评审预算为一轮发现、一轮核对；只有 CORE_BLOCKING 问题才追加阻断修正，设计语义冲突立即停止。

完成定义：六个固定名测试先有效呈红并独立提交，再转绿；四段 prime、8 KiB 截断、hook JSON、幂等 hooks、六步迁移、job `tracker-prime` 注入及名称/sha 记录均可观察；全仓 gofmt 无输出、Windows/Linux build、vet、指定三包测试、临时假仓库二进制 smoke 和控制字符扫描给出 exit code 与原始输出；各功能点独立本地提交。文档候选提交本身不算 P3 实施完成。

## 范围、排除项与授权

- Git 根固定为 `D:/work/inhere/hyy-ai-inspect/tools/gofer`。`internal/commands` 只作 CLI 绑定/输出；`internal/tracker` 拥有本地 prime 内容、迁移映射及仓库文件变更；`internal/hookrelay` 复用 JSON hook 合并机制；`internal/job` 复用 JOB-06① 的规则大小上限与 RuleRef 记录，保持 G021/G022 单向依赖。
- `repo init` 补步骤 4 hooks；P4 的首次 sync（步骤 5）仍不实现。`repo status` 的 hooks 改为真实检测，sync 继续明确 P4 未实现。`repo migrate --from-bd` 默认 dry-run；只在 `--apply` 改写传入仓库，`.beads/` 保留。真实仓库不得运行 `--apply`，试点 `hyy-app-dev` 后续单独安排。
- `host_or_non_offline_action=NOT_APPLICABLE`。本期授权本地测试、代码、文档和按功能点本地提交；不授权 push、部署、远端同步、真实 bd 数据迁移、重启/reload live gofer server/worker 或读写真实配置 `D:/work/inhere/config/win-env/gofer`。

## 输入与批准证据

- 唯一设计依据：[`Approved 0.2` 的“repo init 第 4 步”“gofer repo prime”“从 bd 迁移”与 P3 分期](../design/2026-09-27-local-first-tracker-and-uncommitted-guard-design.md)。设计内记载 2026-09-27 人工批准；P2 实测记录是当前实现基线，不扩大 P3。
- 本 job 授权编写和本地提交本计划候选；监督者可代批由已批准设计派生的实施计划。当前候选尚无批准；提交后停止，等待审批及续接实施请求。
- 适用约束：工作区 `workspace.md`、本仓 `AGENTS.md` 的 G021/G022/G032、gofer 注入的 house-rules/gofer-repo、IDEV-STD 0.22.1 的 plan 合同。若设计与现有协议/数据事实冲突，返回 Gate，不擅自扩围。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | prime 四段、截断与 hook JSON | `internal/tracker` 的 Store/ready/model；`bd prime --hook-json` 形状；Go `encoding/json` | Store 可直接提供 issue/memory 和 commit_policy | 在 tracker 扩展纯渲染，在 commands 包装 SessionStart JSON | OWNER_EXTENSION | P2 尚无 prime 输出 | 单一 tracker 内容口径，CLI 和 job 共用；不建第二上下文仓库 |
| CAP-02 | Claude/Codex SessionStart hooks 读写与检测 | `internal/hookrelay/install.go`、`hooks` 模板、`internal/commands/config.go` | 直接复用既有 JSON 文件路径、合并及保留其他 hook 的模式 | 扩展 hookrelay 的指定命令合并/探测；repo init 调用它 | OWNER_EXTENSION | 现有 installer 只识别 `gofer hook`，不能原样当 prime installer | 只保留一个 JSON 合并 owner，不覆盖其他 hook 或重新建配置 writer |
| CAP-03 | bd issue/memory 导入与托管块替换 | `internal/tracker` Store/UpdateIssues/UpdateMemories/managedBlock、Go JSONL/exec/git config | 复用模型、锁、原子写和托管块文本 | 在 tracker 内加 bd 格式 adapter，commands 负责 dry-run/apply 参数与输出 | OWNER_EXTENSION | 当前没有 bd 映射入口 | 不引入 Dolt/数据库真源；旧路径若必须保留按 G032 标记 |
| CAP-04 | job 的 tracker-prime 规则注入 | `internal/job/rules.go`、`config.EffectiveRulesMaxBytes`、RuleRef/persistence | JOB-06① 已有规则段、大小上限、name/sha 记录 | 在现有 resolveRules 路径加入 cwd tracker 内容 | OWNER_EXTENSION | 当前只读规则库及 `.gofer/RULES.md` | 不复制注入管道，不增加 wire 字段或底层反向依赖 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 前置检查与 fail-closed 条件

- Workspace baseline（计划起草时）：Git root `D:/work/inhere/hyy-ai-inspect/tools/gofer`，branch `main`，HEAD `e6a58ed`，`git status --short --untracked-files=all` 无输出；in-scope dirty/untracked 与 preserved unrelated dirty 均无。实施前重查 HEAD/status，并保护之后出现的他人改动。工作区 `tmp/idev-std/` 在独立 Git 根之外。
- 预计 owner：`internal/tracker/{repo,prime,migrate,model,store}*.go`、`internal/tracker/testdata/` 脱敏 bd fixture；`internal/commands/{repo,tracker_cli_test}*.go`；`internal/hookrelay/{install,install_test}*.go`；`internal/job/{rules,rules_test}*.go`。必要的 hook 模板/现有 hook 调用点按 T0 核查后限于同 owner 扩展。图项目 `D-work-inhere-hyy-ai-inspect` generation `2026-09-27T10:46:37Z` 排除整个 `tools/gofer` 子仓库；以上落点由当前源码读取，实施期不能据图作遗漏断言。
- 实施前重新做 IDEV-STD BOUND/semantic/fingerprint 与适用 preflight、`git status --short`、`git log -1 --oneline`，核对当前 CLI/hook/规则路径。新路径在 mutation 前按 Operational Discovery、Corrective、Semantic Amendment、Ownership Conflict 分类记录；改变接口/schema/安全/数据/验收/lifecycle 或命中他人 dirty 文件即停。
- 单测全部 `t.TempDir()`。smoke 仅用临时 Git 仓库、临时 config 与随机 localhost 端口，每条 gofer 命令显式 `-c <临时配置>` 或 `--server http://127.0.0.1:<端口>`；不让双模式命令回落到真实 server。Windows `internal/job` 慢测试须等最终退出再汇报；已知 `internal/worker/TestPolicyCacheRoundTrip` 基线不纳入修复。

## 波次与依赖

T0 核查与 preflight → T1 六项红测试独立提交 → T2 prime → T3 hooks → T4 bd 迁移 → T5 job 注入 → T6 质量门和临时 smoke。单 Agent 顺序执行，按功能点单独 conventional commit；阶段进度记录实际路径、验证命令/exit/output、commit 和 lifecycle。测试红提交不得混入实现。

## 任务

### T0 基线与实现落点核查

- 文件：只读设计指定章节、`internal/tracker/{repo,model,store,ready}.go`、`internal/commands/{repo,config,hook}.go`、`internal/hookrelay/{install,payload}.go`、`internal/job/{rules,submit}.go`、hook 模板及现有相关测试。
- 动作：核对 P2 模型与文件锁、CLI 参数惯例、SessionStart JSON 两端形状、hook 合并策略、JOB-06① 注入和 sha 记录；用脱敏字段结构确定 bd fixture，不复制真实 issue 正文；列出现有 G032 兼容路径。若 Codex hook 协议与任务书形状不一致，先报设计冲突。
- 验证：`git status --short --untracked-files=all`、`git log -1 --oneline`、当前源码定向读取；记录图索引覆盖缺口。
- 完成标准：T1–T5 owner、接口与风险明确，无未裁决语义冲突或归属冲突。
- 依赖：计划批准及续接实施请求。

### T1 六项固定测试先行，单独 red 提交

- 文件：`internal/tracker/{prime,migrate}_test.go`、`internal/tracker/testdata/bd-issues.jsonl`、`internal/commands/{repo,tracker_cli_test}*.go`、`internal/hookrelay/*_test.go`、`internal/job/rules_test.go`（按真实测试入口分布）。
- 动作：写 `TestPrimeSectionsAndCap`（四段顺序、三档策略、8 KiB、保留最新 memory、issue 截断说明、无 tracker hook 空上下文 exit 0）；`TestPrimeHookJSONShape`（`hookSpecificOutput.additionalContext`）；`TestRepoInitInstallsHooks`（Claude/Codex、一次/重复/`--no-hooks`、保留其他 hook）；`TestMigrateFromBdFixture`（字段、子项、deps、未知状态标签、notes、时间、幂等和 dry-run 零写入）；`TestMigrateStripsBeadsBlock`（两个托管块、Claude/Codex hook 替换、临时 Git 的 hooksPath unset）；`TestJobInjectsTrackerPrime`（cwd tracker、策略+memory、8 KiB 规则预算、`tracker-prime` 名称/sha）。断言实际行为，不写 `t.Skip`。
- 验证：在指定三包运行固定测试名 `go test ... -run 'Test(PrimeSectionsAndCap|PrimeHookJSONShape|RepoInitInstallsHooks|MigrateFromBdFixture|MigrateStripsBeadsBlock|JobInjectsTrackerPrime)$' -count=1`；记录 exit code 与原始失败，红因缺 P3 实现而非夹具/编译笔误。提交前 `git status --short`、核对 staged exact paths。
- 完成标准：六项有有效 red 证据，仅测试与脱敏 fixture 进入独立 `test(tracker): cover prime hooks migration and job injection` 本地提交。
- 依赖：T0。

### T2 prime 内容及 hook JSON

- 文件：`internal/tracker/prime.go`、`internal/commands/repo.go` 与 T1 对应测试。
- 动作：从 config/issue/memory 生成四段，策略文案按 `local-commit|ask|none`；进行中/已认领 issue、ready 前十、memory 全量，按更新时间优先保留最新 memory、截断 issue 并标记，总输出至多 8 KiB。`--hook-json` 仅包 Claude SessionStart `hookSpecificOutput.additionalContext`；无 tracker 返回空上下文 exit 0。保持 hook 模式无 server 依赖；普通 prime 的 P4 auto_sync 不在本期伪装实现。
- 验证：`TestPrimeSectionsAndCap`、`TestPrimeHookJSONShape` 转绿；临时 tracker 读命令的字节上限及 JSON 解析断言。
- 完成标准：四段及空 tracker hook 行为可观察，独立 `feat(tracker): add bounded repository prime` 提交。
- 依赖：T1。

### T3 repo init hooks 与真实 status

- 文件：`internal/hookrelay/install.go`、相应测试、`internal/commands/repo.go`、`internal/tracker/repo.go`；如既有模板是必要 owner，限现有 `hooks/` 模板文件。
- 动作：复用 hookrelay 对 `.claude/settings.json` 和 `.codex/hooks.json` 的 JSON 合并方式，追加 SessionStart `gofer repo prime --hook-json`，重复安装不重复，保留已有其他 hook 与其他事件；`--no-hooks` 跳过。status 从两份配置真实检测安装状态，不再报告 P3 未实现；P4 sync 状态保持待实现。避免 `gofer init hooks` 的旧会话 hook 被新安装误删。
- 验证：`TestRepoInitInstallsHooks` 转绿，临时 repo init 两次后两份 JSON 均只有一条目标命令，其他 hook 字节语义保留；`repo status` 显示检测结果。
- 完成标准：Claude/Codex hooks 安装、幂等、跳过和 status 可观察，独立 `feat(tracker): install repository prime hooks` 提交。
- 依赖：T2。

### T4 从 bd dry-run 与 apply

- 文件：`internal/tracker/migrate.go`、`internal/commands/repo.go`、T1 fixture/测试；复用 `internal/tracker/repo.go` 的 gofer 托管块。
- 动作：按六步逐项打印计划；apply 时隐含 repo init，逐行只读取 `_type:issue`，映射 id/title/status/priority/type/正文/design/验收/owner/assignee/labels/parent/子项/deps/comments/时间/close_reason；notes 整段转单条目，未知状态转 open 并加 `bd:<原状态>`。有 bd 时读取 `bd memories --json` 的 key/content，无可执行文件则提示跳过；按 id 与 `updated_at` 较新者合并，不重复。替换 AGENTS/CLAUDE 中 BEADS 块为同一 gofer 托管块，更新 Claude/Codex 中仅 bd prime hook，只有指向 `.beads/hooks` 的 `core.hooksPath` 才 unset 并打印原值，原样保留 `.beads/`。dry-run 不创建 tracker、配置或任何文件；apply 汇总 issue/memory 数、文件及 hooksPath 原值。
- 验证：`TestMigrateFromBdFixture`、`TestMigrateStripsBeadsBlock` 转绿；临时 Git 仓库 dry-run 前后文件快照一致，apply 两次记录数稳定，非 bd hook 保留。
- 完成标准：迁移六步和幂等行为可观察，独立 `feat(tracker): migrate repository data from bd` 提交；G032 对保留旧读取路径标 `// DEPRECATED(v0.68): remove in v0.71` 或证实不留旧兼容分支，并在报告列明。
- 依赖：T3。

### T5 job 注入 tracker-prime

- 文件：`internal/job/rules.go`、`internal/job/rules_test.go`；若需跨包纯渲染只调用 `internal/tracker/prime.go`，不将 job 逻辑搬入 tracker。
- 动作：在现有 resolveRules 中按 job cwd 发现 tracker，将“提交策略 + memory”作为 `tracker-prime` 规则体并入原规则段，复用同一 `rules_max_bytes` 拒绝行为、RuleRef SHA256、request 持久化和事件；无 tracker 时行为不变。首次/rerun 重新读取，已有远端注入段或 resume 按现有合同不重复注入。
- 验证：`TestJobInjectsTrackerPrime` 转绿；`go test ./internal/job/ -count=1` 等完整结果，确认无 tracker、规则上限、sha 和去重路径。
- 完成标准：job prompt 与规则记录可观察，独立 `feat(job): inject repository tracker prime` 提交。
- 依赖：T4。

### T6 质量门、离线 smoke 与交付

- 文件：本计划之外只允许与 P3 实际变化相符的 README 或设计 P3 实测记录；实施时先核对是否确有必要，文档另作 `docs(tracker): document prime and migration` 提交。
- 动作：全仓 `gofmt -l` 必须 exit 0 且无输出；Windows/Linux `go build ./cmd/gofer` 产物入仓库 `tmp/`；`go vet ./...`；`go test ./internal/tracker/ ./internal/commands/ ./internal/job/ -count=1`。二进制 smoke 在临时仓库造 `.beads/issues.jsonl`、CLAUDE 托管块、Claude bd hook 和 `core.hooksPath`，按 dry-run → apply → status → prime → prime --hook-json 跑，每条显式 `-c <临时配置>`；另做控制字符扫描与 `git diff --check`。输出记录 exit code、gofmt/build/vet 原文、测试 PASS/FAIL 行、smoke、扫描、git log/status。若命令 exit 0 但输出含失败，也按失败调查。
- 验证：上述全部命令真实运行并等慢包结果；核对 `.beads/` 未改、真实配置和 live 进程未动、G032 新增标记/删除清单。
- 完成标准：所有 P3 验收闭合且无未裁决失败；精确 staged paths 核对后完成独立文档提交，报告各任务状态、说明、commit hash、原始输出与未完成/人工决策点。
- 依赖：T5。

## 回滚与恢复

每个功能点的本地 commit 是恢复点。每次提交前 `git status --short`，只 stage 本波 owner，复查 `git diff --cached --name-only`。遇到他人 dirty 路径或需改协议/数据语义即停，不以全量暂存掩盖。失败保留有效红测试/已验证功能提交和首个失败原始输出；撤销时只对本任务的准确 commit 提出 `git revert` 并核对依赖。进度独立记实际文件、验证、commit 与 lifecycle，纯进度不改候选计划版本。

## 人工 Gate

本计划提交后停在人工计划批准 Gate。监督者按本 job 的代批授权批准后，还需在本会话续接明确的当前实施请求；方式已定为 DIRECT_CONTINUOUS。真实仓库 `migrate --apply`、试点、push、部署或 live 服务动作均是另一个目标的外部动作 Gate，本候选不批准这些动作。实施发现 design/plan 语义变化返回相应 Gate。

## 可追溯性

| Approved 0.2 / 本 job 验收 | 任务 | 可观察验证 |
|---|---|---|
| prime 四段、三档策略、ready 前十、8 KiB 与无 tracker hook | T1、T2 | `TestPrimeSectionsAndCap`、`TestPrimeHookJSONShape`、smoke 两种 prime |
| repo init 第 4 步、Claude/Codex hooks、幂等及真实 status | T1、T3 | `TestRepoInitInstallsHooks`、临时 JSON/status |
| bd issue/memory 字段、未知状态、子项/deps、较新覆盖 | T1、T4 | `TestMigrateFromBdFixture`、两次 apply 比对 |
| 托管块/hook 替换、hooksPath 清理、`.beads/` 保留 | T1、T4 | `TestMigrateStripsBeadsBlock`、临时 Git smoke |
| job 规则注入、大小上限和名称/sha | T1、T5 | `TestJobInjectsTrackerPrime`、job 包测试 |
| G021/G022/G032 与离线质量/原始证据 | T0、T2–T6 | build/vet/gofmt/三包测试/控制字符输出与 Git 记录 |

## 完成 Gate 与剩余工作

P3 仅在六项测试、离线 smoke、质量门、G032 清单和原子本地提交都有证据后标为完成；已知失败如实标边界，不用 `t.Skip` 或骨架替代。P4 的 sync/server/web/issue 联动、真实试点迁移与新会话验收、IDEV-STD 适配是后续独立工作；本期不能将本地 fake repo 的成功写成试点或生产验收。
