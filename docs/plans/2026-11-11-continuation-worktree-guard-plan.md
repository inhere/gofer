<!-- template_id: plan; template_version: 1.3.0 -->
# 续跑与 worktree 合并清理互斥 实施计划

> 状态：Draft 0.1 / 待评审
> issue：gofer-fi7f

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-11 | Claude（gofer 监督会话） | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：续跑（九个入口）永远不在已失效的 worktree 或主 checkout 里替 worktree job 干活；worktree 上还有活着的 job 时，`worktree merge` 拒绝、清理跳过。Scope freeze：只实施设计 Draft 0.3（已批准，Q1–Q3=A）与下文“对设计的细化”。Review budget：两轴独立评审（Standards/Governance、Spec/Executability）各一轮，有 `CORE_BLOCKING` 才继续，上限按严谨核心。停止条件：两轴无开放 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

- 九个入口在源 job 是 worktree job 且 worktree 不可用（或续跑 cwd 不在其内）时一律不提交，按设计 §1 的表处理；开跑前复查失败的 job 失败类为 `worktree-gone`，不再被续跑 / 转移 / 重试。
- `MergeWorktree` 遇到在用 worktree（含被合并 job 自身仍在跑）返回 `ErrWorktreeInUse`（HTTP 409）；清理前复查命中时合并成功、清理跳过并给 warning。
- 故障转移的 job 继承源 worktree（行上有 `worktree_path`）。
- 文档同步（http-api、skill references、CHANGELOG）。
- 单测、真实验收、反向验证通过；gofer-verify 四路通过；合并、关 issue。

## 范围、排除项与授权

范围：设计 §1–§5。排除：设计“非目标”（Q3 的续跑语义、远程 worker、自动交付合并、stalled 判定、旧超长文件拆分）；`docs/capabilities.md` 随下次发版更新（不在本计划）。

外部动作适用性：host_or_non_offline_action=REQUIRED （建 / 删主机 worktree、主机临时 server 真实验收、gofer-verify 均为主机 job；push 与发版不在本计划内）。新代码在现有包 `internal/job` 内，不新增模块。

授权：本计划批准后，按功能点本地提交、建 worktree、派子 agent、跑主机 job 与临时 server。push 与发版按「攒一批再发」另行请示。

## 输入与批准证据

- 设计：`docs/design/2026-11-11-continuation-worktree-guard-design.md` Draft 0.3。用户 2026-11-11：「设计 Q1–Q3 按推荐，批准进入计划」。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 续跑继承 worktree 的判定 | `resume.go` `inheritedWorktree`（只看目录存在）、`InheritWorktree` 标记 | 部分 | 判定改为 guard，入口统一用 `WorktreeFrom` | OWNER_EXTENSION | 现有判定失效时静默回落主 checkout，且转移 / 重试不经过它 | 替换 `InheritWorktree`，不留两套标记 |
| CAP-02 | 在用 worktree 检查 | `worktreeUsers`、`ErrWorktreeInUse`（`RemoveWorktree` 已用，HTTP 已映射 409） | 是 | 合并与清理前各调用一次 | DIRECT_REUSE | none | 与 `worktree rm` 同口径 |
| CAP-03 | 不再续跑的失败分类 | `failureDecision` 的 `errCallerDisabled` 哨兵写法、`maybeRetryJob` 跳过 `FailureClassCallerDisabled` | 是 | 加一个同构的类 | OWNER_EXTENSION | none | 与 caller-disabled 并列 |
| CAP-04 | 被拒留痕 | `recordEvent` + 事件常量 | 是 | 新事件常量 | OWNER_EXTENSION | none | 不新增存储 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 对设计的细化

| # | 问题 | 定案 |
|---|---|---|
| R1 | `WorktreeFrom` 取代 `InheritWorktree` | `JobRequest` 删 `InheritWorktree`，加 `WorktreeFrom string \`json:"-" yaml:"-"\``（注释：源 job id，由服务端入口设置，不来自客户端）。设置点：`resume.go:509`（`src.WorktreeBranch != ""` 时设 `src.ID`）、`todo_continue.go:340`（同理设 `predJob.ID`）、`fallback.go:295` 附近（`snap.WorktreeBranch != ""` 时设 `snap.ID`）、`retry_sweep.go:78` 的 `if src, ok` 块内（`src.WorktreeBranch != "" && !req.Worktree` 时设 `rec.SourceJobID`）。`heldInternals`（`hold.go:155/185/221`）：新增 `WorktreeFrom string \`json:"worktree_from,omitempty"\``，capture / restore 各一行；**升级前已 hold 的续跑**只有 `inherit_worktree:true`，restore 时 `h.InheritWorktree && r.WorktreeFrom == ""` 则 `r.WorktreeFrom = r.ResumedFrom`，字段与这一分支打 `// DEPRECATED(v0.214): remove in v0.217`（G032）。 |
| R2 | 提交时分支 | `submit.go:660` 条件改为 `!remote && req.WorktreeFrom != ""`，分支体为 `wt, err = s.continuationWorktree(req.WorktreeFrom, workDir)`；`err != nil` 时按该分支前面已有的出错收尾（`dropDir()` 后 return，同 `mapCwd` 出错写法）。`continuationWorktree`（新文件）：取源 job；源不存在或 `WorktreeBranch == ""` → `(nil, nil)`（普通 job，现状）；否则 `worktreeUsable` 不通过 → `ErrContinuationWorktreeGone`（reason `worktree_gone`）；可用但 `workDir` 不在其内 → 同一错误（reason `worktree_outside_project`）；通过 → 返回 `worktreeRef`（字段同 `inheritedWorktree`）。`inheritedWorktree` 删除（唯一调用点被取代）。错误带 reason 用 `fmt.Errorf("%w: %s (%s)", ErrContinuationWorktreeGone, reason, path)`，另导出 `ContinuationSkipReason(err) string` 供事件取 reason。 |
| R3 | `worktreeUsable(ctx, path, branch)` | 三条按设计 §1：`os.Stat` 是目录；`git -C path rev-parse --show-toplevel` 输出与 `path` 经 `util.RealPath` + `filepath.Clean` 后相等（Windows 用 `strings.EqualFold`）；`git -C path rev-parse --verify --quiet refs/heads/<branch>` 退出码 0。git 调用复用 `internal/job` 现有的 git 执行 helper（`worktree.go` 内已有的封装，参数数组），超时同其他 worktree git 调用。**设计验收第 145 行「分支被切换时拒绝」与 §1「不要求 HEAD」矛盾，以 §1 为准**：验收改为「分支 ref 被删除时拒绝」+「detached HEAD 仍可续跑」。 |
| R4 | 开跑前复查 | `execute.go` `recheckOwners` 块（`:196`）之后加一行：`if err := s.recheckContinuationWorktree(entry); err != nil { finish(StatusFailed, err) … return }`（照该块已有的失败收尾写法）。函数在新文件：`entry.wt != nil && !entry.wt.Temp && entry.req.Forward == nil` 时调 `worktreeUsable`。 |
| R5 | 失败类 | `model.go` 加 `FailureClassWorktreeGone = "worktree-gone"`（与 `FailureClassCallerDisabled` 并列，常量放 `model.go` 保持同处）；`fallback.go:71` 之后照写 `strings.Contains(snap.Error, ErrContinuationWorktreeGone.Error())` → `failureDecision{Class: FailureClassWorktreeGone}`；`execute.go:1164` 的条件改为 `snap.FailureClass == FailureClassCallerDisabled \|\| snap.FailureClass == FailureClassWorktreeGone`。 |
| R6 | 事件与被拒处理 | 新文件内 `EventJobContinuationSkipped = "job.continuation_skipped"`、`func (s *Service) recordContinuationSkipped(srcID, entry string, err error)`（payload `{entry, reason, worktree_path}`，再 `slog.Warn`）。调用点（只在 `errors.Is(err, ErrContinuationWorktreeGone)` 时）：`autoResume`（entry `auto`）、`resumeUncommitted`（`uncommitted`）、`followup.go:167`（`followup`，原先丢弃错误的地方）、`fallBack`（`fallback`）、重试清扫（`retry`：`Submit` 被拒时照该函数已有的 `CancelRetry` + `EventJobRetryCancelled` + receipts 收尾，再记本事件）。交付核验续接、唤醒、待办接续、手动 resume 走现有错误路径（设计 §1 表），不另记事件。 |
| R7 | 合并与清理 | `MergeWorktree`（`worktree.go:670`）在取到 `st` 之后、真正合并之前：`if users := s.worktreeUsers(st.Path); len(users) > 0 { return MergeResult{}, fmt.Errorf("%w: %s: %s", ErrWorktreeInUse, st.Path, strings.Join(users, ", ")) }`（文案与 `:864` 一致；提示「等待或 gofer job cancel <id>」放 CLI / http 文档，不改错误常量）。`cleanupMergedWorktree`（`:763`）开头：`if beforeMergeCleanup != nil { beforeMergeCleanup() }`，然后再查一次，命中则不删，向 `MergeResult.Warnings`（`worktree.go:662`，「合并成功后跳过的收尾步骤」，已是 wire 字段）追加一条说明在用的 job id，`Removed` 保持 false；不新增字段。 |
| R8 | 入口层映射 | `internal/httpapi/job_handler.go` `submitStatus` 加 `errors.Is(err, job.ErrContinuationWorktreeGone)` → 409；核实 `resumeStatus`（`:675`）与 todo continue 的 handler 都落到它（否则同样加一行）。CLI / MCP 只透传。 |
| R9 | 新文件与超长文件 | 新文件 `internal/job/continuation_guard.go` 承载 R2–R6 的函数与常量；`worktree.go` / `submit.go` / `execute.go` 只加调用行（各 ≤ 6 行）。测试放新文件 `internal/job/continuation_guard_test.go`（必要时 `_windows_test.go`）。 |

## 前置检查与 fail-closed 条件

workspace baseline：主机建 worktree `.worktrees/fi7f`（分支 `fix/fi7f-continuation-guard`，基于当前 main），`.git` 改相对 gitdir；`web/node_modules` 链接主 checkout（本计划不改 web，链接只为构建嵌入）；Go 命令带 `GOWORK=off`，`GOCACHE` / `TMPDIR` 在任何 git 仓库之外。

fail-closed（出现即停，回报监督会话）：

- 基线 `go test ./internal/job -run 'Worktree|Resume|Fallback|Retry|TodoContinue' -count=1` 在未改动时失败。
- 发现需要改 schema、wire 字段、权限声明，或第十个「接着干」入口。

## 波次与依赖

```
W0  T0 前置核对（Claude）
W1  T1 续跑守卫与九个入口（sonnet 子 agent）
W2  T2 合并 / 清理在用检查（sonnet 子 agent，同一 agent 接着做）‖ T3 文档（suga，gpt；依赖 T1 的错误文案与事件名）
W3  T4 真实验收（Claude）→ T5 反向验证、全量与收尾（Claude）
```

## 任务

### T0 前置核对

- 执行方：Claude。
- 动作：建 worktree；跑基线测试；确认 R3 复用的 git helper 名称与 `cleanupMergedWorktree` 把 warning 交回 `MergeWorktree` 的方式，写进任务书。
- 完成标准：基线绿，任务书定稿。

### T1 续跑守卫与九个入口

- 执行方：sonnet 子 agent（状态机与数据安全）。
- 文件：`internal/job/continuation_guard.go`（新）与 `continuation_guard_test.go`（新）；`model.go`、`hold.go`、`resume.go`、`todo_continue.go`、`fallback.go`、`retry_sweep.go`、`submit.go`、`execute.go`、`uncommitted.go`、`followup.go`；`internal/httpapi/job_handler.go` 及其测试。
- 动作：R1–R6、R8、R9。任务书写明 G021–G024、GR104、GR109、G051–G053、SR1211；禁删注释、禁无关重排；不起空等后台任务，测试用 `--sync` 式前台命令。
- 验证（真实 git 仓库与 worktree）：
  - 清理后手动 resume → `ErrContinuationWorktreeGone`，库里无新 job，主 checkout `git status` 干净。
  - 登记已删、目录仍在 → 拒绝；分支 ref 被删 → 拒绝；detached HEAD → 可续跑并继承。
  - worktree 在项目根外 → reason `worktree_outside_project`。
  - 自动续跑 / 未提交续投 / follow-up / 故障转移遇失效 worktree → 不提交，源 job 有对应 entry 的 `job.continuation_skipped`。
  - 交付核验续接被拒 → 走 park；待办接续被拒 → 返回错误。
  - 故障转移在 worktree 可用时继承（新 job 行 `worktree_path` 为源路径）。
  - 续跑 / 转移 job 带重试策略、失败后清理 worktree → 重试被拒、记 `entry:retry`；首次 `--worktree` job 的重试仍新建 worktree（现状不变）。
  - 提交后开跑前删 worktree（`max_concurrent=1` + 占槽 job 压住）→ 失败类 `worktree-gone`，agent 未启动，不排重试、不续跑。
  - 升级前 hold 的续跑（`heldInternals` 只有 `inherit_worktree:true`）批准后仍继承 worktree。
  - httpapi：resume 被拒 409。
  - 提交前 `go build ./... && go vet ./...`；`go test ./internal/job ./internal/httpapi -count=1`。

### T2 合并 / 清理在用检查

- 执行方：sonnet 子 agent（与 T1 同一 agent，T1 提交后接着做，单独提交）。
- 文件：`internal/job/worktree.go`（调用行与钩子变量）、`continuation_guard_test.go` 或 `worktree_tododone_test.go` 追加用例。
- 动作：R7。
- 验证：worktree 上有排队 / 运行中的续跑时 `MergeWorktree` → `ErrWorktreeInUse`，分支未合并；被合并 job 自身仍在跑 → 同样拒绝；钩子里起一个续跑 → 合并成功、清理跳过、有 warning、目录与登记仍在；钩子测试保存旧值并 `t.Cleanup` 复原、不 `t.Parallel()`。`go test ./internal/job ./internal/httpapi -count=1`。

### T3 文档

- 执行方：suga（gpt）。
- 文件：`docs/reference/http-api.md`、`skills/gofer-usage/references/job-advanced.md`、`skills/gofer-usage/references/plans-workflows.md`、`CHANGELOG.md`「未发布」（三条）。
- 动作：按设计 §5。G046：不写 issue id、本机路径。任务书禁止 web_fetch、禁删无关段落与标题。
- 验证：私有名检查（`PRIVATE_NAMES_FILE` 指向主 checkout 的 `.private-names`）；我审 `git diff --stat` 删除行。

### T4 真实验收

- 执行方：Claude。
- 环境：worktree 交叉编译的 Windows 二进制，主机临时 server（临时端口与配置目录，长时 exec job 运行，用完 cancel），测试仓库为临时 git 仓库（不碰业务项目）。
- 动作：提交 `--worktree` 的 exec job → 手动 resume 让续跑在 worktree 里 `sleep` → `worktree merge --cleanup` 得 409；cancel 续跑后合并清理成功 → 再 resume 得 409，主 checkout `git status` 干净、无新 job。记录写 `tmp/fi7f-impl/acceptance.md`。

### T5 反向验证、全量与收尾

- 执行方：Claude。
- 反向（临时副本，不提交）：去掉提交时 guard → 「清理后 resume」用例必须失败；去掉合并前检查 → 「在用时合并」用例必须失败；去掉开跑前复查 → 「排队期间删 worktree」用例必须失败；去掉 `maybeRetryJob` 跳过 → 「worktree-gone 不重试」用例必须失败。记录 `tmp/fi7f-impl/reverse.md`。
- `git diff --stat main...` 查无关删除；gofer-verify 四路通过；合并（`--no-ff`）；删 worktree 与分支；设计稿补实施记录；计划 COMPLETE 写不超过 8 行总结；关 `gofer-fi7f`，另立 issue：Q3（已提交的 stalled job 是否续跑）、自动交付合并的在用检查。

## 回滚与恢复

单独回退合并提交；无 schema、配置、wire 协议变化（`heldInternals` 新增的是库内 hold JSON 的可选字段，回退后旧代码忽略它，只会丢失该次 hold 续跑的 worktree 继承，并按旧逻辑处理）。

## 人工 Gate

- 本计划批准后开始 T0。
- 发版：与其他已完成项攒批，另行请示。

## 可追溯性

| 设计项 | 任务 | 验证 |
|---|---|---|
| §1 继承判定、提交时拒绝、`WorktreeFrom`、九个入口 | T1 | T1 用例、T4、T5 反向 |
| §2 开跑前复查与失败类 | T1 | T1 排队用例、T5 反向 |
| §3 合并 / 清理在用检查 | T2 | T2 用例、T4、T5 反向 |
| §4 残余（hold） | T1 | T1 hold 升级用例 |
| §5 入口映射与文档 | T1（409）、T3 | T1 httpapi 用例、T3 私有名检查 |

## 完成 Gate 与剩余工作

T5 完成即本计划完成；发版另行进行。
