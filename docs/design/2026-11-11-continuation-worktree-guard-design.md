<!-- template_id: design; template_version: 1.2.0 -->
# 续跑与 worktree 合并清理互斥设计

> 状态：Draft 0.3 / 待批准
> issue：gofer-fi7f

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-11 | Claude | 初稿：事故复盘、代码核查与修复方案 |
| 0.2 | 2026-11-11 | Claude | 按两轴评审修订：补故障转移入口（F1）与全部八个入口的被拒处理（F3/S1）；新增 `WorktreeFrom` 统一继承；合并不排除自身（F2）；开跑前复查位置与非 transient（F4）；可用判定改为分支 ref 存在、不要求 HEAD（F5）、RealPath 比较（S6）；worktree 在项目根外单独 reason（F6）；测试钩子（F7）；hold 与残余竞态说明（F8/F9）；新文件规避 GR104（S4）；事件常量（S5）；入口映射与同步文件清单（S2/S3）；交付合并列为非目标（S7） |
| 0.3 | 2026-11-11 | Claude | 第 2 轮评审修订：重试是第九条路径——新增失败类 `worktree-gone`，不自动续跑 / 不转移 / 不重试，续跑的重试也带 `WorktreeFrom`（R1/C1）；转移的 `WorktreeFrom` 取失败 job 自身 id，带交付的 job 转移维持现状（C2）；事件改名 `job.continuation_skipped{entry, reason}`（N3）；测试钩子复位与不并行（N2）；hold 事实表述更正（N4）；同步清单补故障转移一段与第三条 CHANGELOG（N1） |

需要用户：答复 Q1–Q3（或接受全部推荐项），并明确批准进入计划编写。批准设计不等于授权实施。

## 背景与目标

2026-11-11 事故（另一会话反馈）：一个 `--worktree` 的 agent job 提交完代码后 900 秒无输出，被判 stalled（failed），server 随即自动续跑；几乎同时监督方执行 `job worktree merge --todo-done --cleanup`。合并成功；清理时续跑进程占着目录，`git worktree remove` 在 Windows 上报 Permission denied，但 worktree 登记与分支已删，目录里没有 `.git`。续跑进程在里面执行 git，会向上落到主仓库的 main。该次只跑了 `git status/log`、`go build/test`，main 未被改动，但续跑 job 一直在跑，目录也删不掉。本会话 11-10 也见过同类：suga job 写完改动后被判 stalled，监督方已审阅提交，自动续跑又在同一 worktree 跑了 7 分钟。

目标：**续跑（自动、手动、待办接续）永远不在一个已失效的 worktree 里、也不在主 checkout 里替一个 worktree job 干活；worktree 还有活着的 job 时，合并与清理不动它。**

## 范围与非目标

范围：一切「接着某个 job 干活」的提交（下表全部入口）在源 job 是 worktree job 时的 worktree 检查；开跑前复查；`job worktree merge`（含 `--cleanup`）对在用 worktree 的处理；对应文档同步。

非目标：

- 是否应当自动续跑「已提交、且被监督方接手」的 job（Q3）。
- 远程 worker 上的 worktree（合并本来就不支持远程；`submit.go` 在 remote 时本就不继承 worktree，检查只在本机执行，远程保持现状）。
- 自动交付的合并（`internal/delivery/execute.go:109` 直接 `MergeBranch`，发生在 job 结束时，与续跑并发窗口极小），另立 issue。
- stalled 判定本身；不拆分已超长的旧文件（G023，另立 issue）。

## 已确认事实（代码，候选 92f943b3）

- 自动续跑在 job 失败时立即提交：`execute.go:928` → `autoResume`（`execute.go:1113`）→ `resumeJob`；资格（`autoResumeEligible`，`execute.go:1100`）不看 worktree。
- 「接着干」的入口共九处，前六处经 `resumeJob` → `Submit`，待办接续、故障转移与失败重试直接组请求 `Submit`：

  | 入口 | 位置 | 失败时现状 |
  |---|---|---|
  | 手动 `job resume` / HTTP resume | `resume.go` `ResumeJob` | 错误返回调用方 |
  | 自动续跑（transient） | `execute.go:1113` `autoResume` | 返回 false，调用方记 `job.terminal` |
  | 未提交改动续投 | `uncommitted.go:317` `resumeUncommitted` | 返回 false |
  | 交付核验续接 | `delivery_check.go:498` | `parkAfterFailedCheckResume` |
  | follow-up / `reject --resume` | `followup.go:167` | 释放 follow-up，错误丢弃 |
  | 唤醒续接 | `wakeup.go:661` | 非 ErrNoSession 的错误返回，唤醒记失败 |
  | 待办接续 | `todo_continue.go:330-340` | 派发失败返回 |
  | **故障转移（换 agent）** | `fallback.go:250` `fallBack` | 直接 `Submit` |
| **失败重试** | `execute.go:1157` `maybeRetryJob` → 重试清扫 | 从 `request_json` 重建请求再 `Submit`；内部字段不在 JSON 里 |

- 续跑的 cwd：`resumeCwd`（`resume.go:766`）——源 job 有 `WorktreePath` 且它在项目根下时取 worktree，否则取源 job 提交时的相对 cwd，即**主 checkout**。
- 续跑是否接手 worktree：`InheritWorktree = src.WorktreePath != ""`（`resume.go:509`、`todo_continue.go:340`）；提交时 `inheritedWorktree`（`resume.go:793`）只检查目录存在；返回 nil 时**照常当普通 job 提交**（`submit.go:660-665`，该分支还要求 `ResumedFrom` 非空）。
  - worktree 已被清理（`cleanupMergedWorktree` / `RemoveWorktree` 清空 `WorktreePath`，`worktree.go:771`、`:889`）→ 续跑回落主 checkout。即「合并清理后不 resume」事故成因。
  - 登记已删但目录还在（本次现场）→ 续跑落在无 `.git` 的目录，git 向上找到主仓库。
  - **故障转移**（`fallback.go:295-301`）取 `resumeCwd`、清空 `ResumedFrom`、不设 `InheritWorktree`：worktree 已删时同样回落主 checkout；worktree 还在时以普通 job 在 worktree 目录里跑，行上没有 `worktree_path`，`worktreeUsers` 看不到它。
- 继承了 worktree 的续跑提交即落库（`submit.go` 先 persist 后 `go s.execute`），行上 `worktree_path` 为源 worktree；`ListWorktrees`（`jobstore/jobs.go:307`）取 `worktree_path<>''` 的行，`worktreeUsers`（`worktree.go:1026`）过滤 `IsFinished`（终态或 `needs_review`，`cancel.go:259`）。
- `RemoveWorktree`（`worktree.go:854`）已有在用检查；`MergeWorktree`（`:670`）与 `cleanupMergedWorktree`（`:763`）没有；`MergeWorktree` 也不检查被合并 job 本身是否还在跑。
- 清理后源 job 保留 `WorktreeBranch` / `WorktreeBaseSHA`（`worktree.go:843-847` 注释）。`ref:` 临时 checkout 不设 Branch（`worktree.go:178`），所以只有真正的 worktree job 带 `WorktreeBranch`，不会误判。
- 继承 worktree 的续跑不拿目录锁（`execute.go:140` 条件 `entry.wt == nil`）；开跑前的最后一道复判是 `recheckOwners`（`execute.go:188-198`）。

## 总体方案

新代码集中在新文件 `internal/job/continuation_guard.go`（`worktree.go` 1221 行、`submit.go` 1842 行、`execute.go` 1290 行均已超 GR104 上限，只加调用行）：错误 `ErrContinuationWorktreeGone`、失败类 `FailureClassWorktreeGone = "worktree-gone"`、判定函数、事件常量 `EventJobContinuationSkipped = "job.continuation_skipped"`（payload `{entry, reason, worktree_path}`，entry 取 `auto` / `uncommitted` / `followup` / `fallback` / `retry`）与记录函数。

### 1. 统一的继承判定与提交时拒绝

- **源 job 是 worktree job**：`src.WorktreeBranch != ""`（清理后仍保留）。
- **worktree 可用**（`worktreeUsable`）：① 目录存在；② `git -C <path> rev-parse --show-toplevel` 与记录路径经 `util.RealPath` + `filepath.Clean` 后相等（Windows 不分大小写）；③ 记录的分支 ref 仍存在（`git -C <path> rev-parse --verify refs/heads/<branch>`）。**不要求 HEAD 等于该分支**：agent 停在 rebase / cherry-pick 冲突（detached HEAD）或自行建了分支时仍可续跑。git 调用一律参数数组。
- **提交时**（`submit.go:660` 分支改为调用 guard）：请求带「worktree 来源 job」时——
  - 来源是 worktree job 且 worktree 可用、续跑 cwd 在其内 → 继承（现状）；
  - 来源是 worktree job 但 worktree 不可用 → 返回 `ErrContinuationWorktreeGone`（reason `worktree_gone`），不提交；
  - worktree 可用但续跑 cwd 不在其内（项目根是仓库子目录，worktree 在根外，`resume.go:780` 回落）→ 同一错误，reason `worktree_outside_project`。
- **来源字段**：新增内部字段 `JobRequest.WorktreeFrom`（`json:"-"`）；hold 期间随 `heldInternals`（`hold.go:136-176`）保存——那里是带 json tag 的字段，capture / restore 各加一行，与 `InheritWorktree` 相同。`resumeJob` 与待办接续设为源 job id；**故障转移设为失败 job 自身的 id（`snap.ID`）**，与它的 `resumeCwd(snap)` 同源（不依赖 `ResumedFrom`），从而转移 job 也继承 worktree、行上记录路径、能被 `worktreeUsers` 看到。`InheritWorktree` 判定改为 `src.WorktreeBranch != ""`。
- **重试**：重试清扫重建请求时，若失败 job 的行上有 `WorktreeBranch` 且它本身是续跑 / 转移（`ResumedFrom` 或 `WorktreeFrom` 来源），把 `WorktreeFrom` 设为失败 job 的 id，让重试同样经过检查；首次以 `--worktree` 提交的 job 重试仍新建 worktree（现状）。
- **带交付的 job 故障转移**：维持现状（`config.go:76` 要求交付需 worktree 或续跑来源，转移请求两者都没有，被拒），本次不改。
- **各入口被拒后的结果**：

  | 入口 | 被拒后 |
  |---|---|
  | 手动 resume | HTTP 409 / CLI 报错，文案说明不会改到主 checkout、需要时另起 job |
  | 自动续跑 / 未提交改动续投 | 不续跑；源 job 记 `job.continuation_skipped`，调用方照常记 `job.terminal`，失败照常进「等我」 |
  | 交付核验续接 | 走现有 `parkAfterFailedCheckResume`，原因文案带 reason |
  | follow-up | follow-up 释放回待处理（现状），另记 `job.continuation_skipped{entry:followup}` 并 Warn 日志（不再无声丢弃） |
  | 唤醒续接 | 唤醒记失败（现状路径），错误文本带 reason |
  | 待办接续 | 派发失败返回（现状路径） |
  | 故障转移 | 不转移；源 job 记 `job.continuation_skipped{entry:fallback}`，照常 terminal |
| 失败重试 | 不重试；源 job 记 `job.continuation_skipped{entry:retry}`，失败 job 保持 failed |

### 2. 开跑前复查

`execute.go` 在所有并发槽 / 目录锁之后、`recheckOwners` 旁边（`runCtx` 之前）加一行调用：`entry.wt != nil && !entry.wt.Temp && req.Forward == nil` 时再判 `worktreeUsable`；不可用则 `finish(StatusFailed)`，错误为 `ErrContinuationWorktreeGone`，不启动 agent。

**失败类**：`failureDecision`（`fallback.go:67`）开头照 `errCallerDisabled` 的写法按错误文本哨兵识别，提前返回 `FailureClassWorktreeGone`（不判 transient、不续跑、不转移）；`maybeRetryJob`（`execute.go:1157`）照 `FailureClassCallerDisabled` 跳过该类。这样复查失败不会经任何路径再跑一次。

### 3. 有活着的 job 时不合并、不清理

- `MergeWorktree` 合并前查 `worktreeUsers(path)`，**不排除被合并的 job 自身**（它若已结束含 needs_review 本就被过滤；若还在跑正是要拦的）：非空就拒绝（Q1），返回现有 `ErrWorktreeInUse`（`worktree_handler.go:111` 已映射 409），列出 job id 与状态，提示等待或 `gofer job cancel <id>`。
- `cleanupMergedWorktree` 删除前再查一次：命中则跳过清理、结果给 warning（合并不回滚）。为测试这一分支，在两次检查之间留一个包内测试钩子（`var beforeMergeCleanup func()`，生产为 nil，调用前判 nil）；用到它的测试照 `outcomes_test.go:215-217` 的写法保存旧值并 `t.Cleanup` 复原，且不加 `t.Parallel()`（G053）。

### 4. 残余与不做的事

- 不加跨 Submit / Merge 的锁（SR1410）。已知残余：清理前复查之后、删除完成之前恰好有续跑开跑且 agent 已启动——窗口在毫秒级，接受。
- 等待审批的续跑（hold）在批准前的行（`awaiting_approval`，`hold.go:499`）上没有 `worktree_path`，合并前检查看不到它；批准时经 `submitAdmitted` 重新提交（`hold.go:623`），若被提交时检查拒绝，该行以 failed 结束（`failApprovedHold`），结果安全。

### 5. 入口层映射与文档同步

- `ErrContinuationWorktreeGone` 在 `internal/httpapi` 的 `submitStatus` 映射 409（resume 经 `resumeStatus` 落到它，待办派发同样得到 409）；MCP / CLI 只透传错误文本。
- 文档：`docs/reference/http-api.md`（worktree/merge 段 409 原因与 cleanup 跳过 warning；Job resume 错误列表加 409；todo `continue_from` 段）；`skills/gofer-usage/references/job-advanced.md`（「中断后续跑」：worktree 已删除时不能续跑，自动续跑条目加 `job.continuation_skipped`；合并规则：有 job 在用时 merge 返回 409；「停滞看门狗、故障转移与重试」一段：转移的 job 继承源 worktree，worktree 失效时不转移、不重试）；`skills/gofer-usage/references/plans-workflows.md`（待办接续）；`CHANGELOG.md`「未发布」变更三条（续跑拒绝、merge 409、故障转移继承 worktree）。SKILL.md 无需更新。

## 架构

全部在 `internal/job`：新文件 `continuation_guard.go`；`resume.go` / `todo_continue.go` / `fallback.go` 设 `WorktreeFrom`；`submit.go:660` 与 `execute.go` 开跑前各一行调用；`worktree.go` 合并与清理前各一次检查；`execute.go` / `uncommitted.go` / `followup.go` 的失败分支记事件。入口层只做错误到 409 的映射。不改 schema、协议与权限。新写入（事件、persist）不得 `_ =` 丢错（GR109）。

## 关键流程

1. job A（worktree W，分支 B）被判 stalled → 自动续跑：W 可用 → 续跑 A′ 提交并落库（路径 W）。
2. 监督方 `merge A --cleanup`：`worktreeUsers(W)` 含 A′ → 409，提示等 A′ 结束或取消 A′。
3. 反过来：先合并清理 W → A 被自动续跑：W 不可用 → 不续跑，A 记 `job.continuation_skipped`；手动 `job resume A` → 409；A 的故障转移同样不提交。
4. A′ 已提交、排队时 W 被删 → A′ 开跑前复查失败（失败类 `worktree-gone`），不启动 agent，也不会被重试 / 续跑 / 转移。

## 安全、数据、运维与回滚

- 修复的是「续跑在主 checkout 或无 `.git` 目录里改代码」的数据风险；新增的只有拒绝路径与一个事件，不新增写操作。
- 行为变化：worktree 已删除的 job 不能再续跑 / 转移（以前在主 checkout 里继续）；worktree 上还有未结束的 job（含被合并 job 自身仍在跑）时 `worktree merge` 返回 409；故障转移的 job 现在继承源 worktree（行上有 `worktree_path`）。
- 安全核对：鉴权不变（merge 仍要 manage 凭据，resume 权限不变，无新路由，G056 不适用）；git 参数来自库内路径与分支，以参数数组调用；409 列出的他人 job id 与现有 `worktree rm` 一致。
- 回滚：回退合并提交，无数据迁移。

## 决策

| 编号 | 问题 | 选项 | 推荐 |
|---|---|---|---|
| Q1 | worktree 上有活着的 job 时执行 `worktree merge` | A：整个合并拒绝（409，列出 job，提示等待或取消）；B：照常合并，只跳过 `--cleanup` 并给 warning | **A**。续跑还在往分支上提交，此时合并会漏掉之后的提交，合并结果与最终分支不一致；和 `worktree rm` 的规则一致 |
| Q2 | 源 job 的 worktree 已失效时的续跑（手动 / 自动 / 待办接续） | A：一律拒绝，不回落；B：保持现状（回落到主 checkout） | **A**。回落到主 checkout 等于让 agent 在主线上干分支的活，正是本次与「合并清理后不 resume」事故的根因 |
| Q3 | 是否顺带改「已提交的 stalled job 不自动续跑」 | A：本次不改，另立 issue 讨论；B：本次一并改 | **A**。是否续跑涉及 stalled 的语义（可能真没做完），与本次的安全边界无关，单独讨论 |

## 待确认事项

- 上表 Q1–Q3，等用户答复。

## 验收

- 单测（`internal/job`，真实 git 仓库与 worktree）：
  - worktree 被 `worktree merge --cleanup` 删除后，手动 resume 返回 `ErrContinuationWorktreeGone`，库里没有新 job，主 checkout 无改动。
  - 登记已删、目录仍在（模拟 Windows 删不掉目录）时同样拒绝。
  - 分支被切换（HEAD 不是原分支）时拒绝。
  - 自动续跑遇到失效 worktree：不提交续跑，源 job 有 `job.continuation_skipped{entry:auto}` 事件。
  - 待办接续遇到失效 worktree：拒绝。
  - 续跑提交后、开跑前删掉 worktree：续跑失败且未启动 agent 进程。
  - worktree 上有排队 / 运行中的续跑时 `MergeWorktree` 返回 `ErrWorktreeInUse`，分支未合并；清理前复查命中时合并成功、清理跳过并有 warning。
- 补充单测：故障转移在 worktree 已删时不提交、在 worktree 可用时继承（行上有 worktree_path，`WorktreeFrom` 为失败 job 自身）；复查失败的 job 失败类为 `worktree-gone` 且不排重试、不续跑；带重试策略的续跑在 worktree 清理后重试被拒；未提交续投被拒记事件；交付核验续接被拒走 park；源 job 自身仍在跑时 `MergeWorktree` 得 `ErrWorktreeInUse`；worktree 在项目根外时 reason 为 `worktree_outside_project`；detached HEAD（模拟 rebase 中）仍可续跑；清理前复查命中（测试钩子）时合并成功、清理跳过有 warning。排队期间删 worktree 用 `max_concurrent=1` + 占槽 job 压住续跑；等待用 `wait.For` / `jobs.Wait`，阈值远大于用例耗时（G051–G052）；Windows「目录删不掉但登记已删」复用 `worktree_remove_windows_test.go` 的做法。
- 真实验收（临时 server，exec agent 足以模拟）：造一个 worktree job → 手动 resume 让它在 worktree 里 `sleep` → `worktree merge --cleanup` 得 409；取消后合并清理成功 → 再 resume 得 409，主 checkout 无改动。
- 反向验证：去掉提交时检查 → 「清理后 resume」用例失败；去掉合并前检查 → 「在用时合并」用例失败。

## 结论与人工计划 Gate

核心是把八个「接着干」入口回落到主 checkout 的隐式路径统一改成显式拒绝（含故障转移），并让合并在 worktree 仍被使用时停下；不加锁、不改 schema。等用户答复 Q1–Q3 并批准后写计划。
