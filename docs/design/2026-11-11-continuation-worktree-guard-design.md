<!-- template_id: design; template_version: 1.2.0 -->
# 续跑与 worktree 合并清理互斥设计

> 状态：Draft 0.1 / 待批准
> issue：gofer-fi7f

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-11 | Claude | 初稿：事故复盘、代码核查与修复方案 |

需要用户：答复 Q1–Q3（或接受全部推荐项），并明确批准进入计划编写。批准设计不等于授权实施。

## 背景与目标

2026-11-11 事故（另一会话反馈）：一个 `--worktree` 的 agent job 提交完代码后 900 秒无输出，被判 stalled（failed），server 随即自动续跑；几乎同时监督方执行 `job worktree merge --todo-done --cleanup`。合并成功；清理时续跑进程占着目录，`git worktree remove` 在 Windows 上报 Permission denied，但 worktree 登记与分支已删，目录里没有 `.git`。续跑进程在里面执行 git，会向上落到主仓库的 main。该次只跑了 `git status/log`、`go build/test`，main 未被改动，但续跑 job 一直在跑，目录也删不掉。本会话 11-10 也见过同类：suga job 写完改动后被判 stalled，监督方已审阅提交，自动续跑又在同一 worktree 跑了 7 分钟。

目标：**续跑（自动、手动、待办接续）永远不在一个已失效的 worktree 里、也不在主 checkout 里替一个 worktree job 干活；worktree 还有活着的 job 时，合并与清理不动它。**

## 范围与非目标

范围：续跑接手 worktree 的提交时检查与开跑前复查；`job worktree merge`（含 `--cleanup`）对在用 worktree 的处理；手动 `job resume`、自动续跑、待办接续三条入口。

非目标：

- 是否应当自动续跑「已提交、且被监督方接手」的 job（Q3）。
- 远程 worker 上的 worktree（合并本来就不支持远程；续跑的 worktree 检查只在本机执行，远程保持现状）。
- stalled 判定本身。

## 已确认事实（代码）

- 自动续跑在 job 失败时立即提交：`execute.go:928` → `autoResume`（`execute.go:1113`）→ `resumeJob`。资格只看开关、会话 id、次数与 agent 能否续接（`autoResumeEligible`，`execute.go:1100`），不看 worktree。
- 续跑的 cwd：`resumeCwd`（`resume.go:766`）——源 job 有 `WorktreePath` 时取源 job 的 worktree 路径，否则取源 job 提交时的相对 cwd，也就是**主 checkout**。
- 续跑是否接手 worktree：`InheritWorktree = src.WorktreePath != ""`（`resume.go:509`，待办接续同理 `todo_continue.go:340`）；提交时 `inheritedWorktree`（`resume.go:793`）只检查目录存在，不检查 git 登记与分支；返回 nil 时续跑**照常当普通 job 提交**（`submit.go:660-665`）。
  - 结果一：worktree 已被清理（`cleanupMergedWorktree` 清空了源 job 的 `WorktreePath`，`worktree.go:763`）→ `InheritWorktree=false`、cwd 回落到主 checkout → 续跑在主 checkout 里改代码。这正是记忆里「合并清理后不 resume」事故的成因。
  - 结果二：登记已删但目录还在（事故现场）→ 续跑落在无 `.git` 的目录，git 向上找到主仓库。
- `RemoveWorktree`（`job worktree rm`）已有「有活着的 job 在用就拒绝，`--force` 也不行」（`worktree.go:854`，`worktreeUsers` 查库里非终态且路径相同的 job，`worktree.go:1026`）；`MergeWorktree` 的 `--cleanup`（`cleanupMergedWorktree`）**没有**这道检查，合并本身也没有。
- 源 job 清理后仍保留 `WorktreeBranch` / `WorktreeBaseSHA`（「branch/base/head stay for audit」，`worktree.go:845` 注释），可据此判断「这个 job 曾经在 worktree 里跑」。

## 总体方案

### 1. 续跑只在活着的 worktree 里跑

- **判定「源 job 是 worktree job」**改为 `src.WorktreeBranch != ""`（清理后仍保留），不再只看 `WorktreePath`。
- **活着的 worktree**：路径是目录；`git -C <path> rev-parse --show-toplevel` 解析后等于该路径；`git -C <path> symbolic-ref --short HEAD` 等于记录的分支。新 helper `worktreeAlive(ctx, wt)` 放 `worktree.go` 附近。
- **提交时**：本机续跑（手动 resume、自动续跑、待办接续）在源 job 是 worktree job 时，worktree 不活着就**拒绝提交**，返回新的类型错误 `ErrContinuationWorktreeGone`（HTTP 409），说明「源 job 的 worktree 已被删除或不再是原分支，续跑不会改到主 checkout；需要时另起新 job」。不再回落到主 checkout 或无 `.git` 的目录。
- **开跑前复查**：接手了 worktree 的续跑在真正启动 agent 前（离开排队、拿到目录锁之后）再判一次 `worktreeAlive`；不活着就以同一错误失败，不启动进程。覆盖「排队期间 worktree 被清理」。
- **自动续跑**被拒时：不提交续跑，源 job 照常记 `job.terminal`（失败照常进「等我」），并在源 job 上记事件 `job.auto_resume_skipped`（reason `worktree_gone`），说明为什么没续跑。

### 2. 有活着的 job 时不合并、不清理

- `MergeWorktree` 在合并前查 `worktreeUsers`（排除被合并的 job 自身）：有非终态 job 在用这个 worktree 就**拒绝合并**（Q1），返回现有 `ErrWorktreeInUse`（409），列出 job id 与状态，提示「等它结束或 `gofer job cancel <id>` 后再合并」。
- `cleanupMergedWorktree` 在删除前再查一次（防止合并期间刚提交的续跑）：有就跳过清理，结果里给 warning（合并已成功，不回滚）。

### 3. 不加进程内锁

提交时检查 + 开跑前复查 + 清理前复查三道检查已覆盖已知顺序：续跑先提交 → 合并被拒；清理先完成 → 续跑提交被拒；续跑在排队时被清理 → 开跑前被拒。剩下「清理前复查之后、删除完成之前恰好有续跑开跑」的窗口在毫秒级，且开跑前复查会看到半删状态而失败。不引入跨 Submit / Merge 的互斥锁（SR1410：局部检查已足够）。

## 架构

全部在 `internal/job`：`resume.go`（判定改用 `WorktreeBranch`）、`submit.go`（提交时拒绝）、执行启动处（复查）、`worktree.go`（`worktreeAlive`、合并与清理前检查）、`todo_continue.go`（同一判定）、`execute.go`（自动续跑被拒的事件）。入口层只做错误到 HTTP 状态的映射（409）。不改 schema、协议与权限。

## 关键流程

1. job A（worktree W，分支 B）被判 stalled → 自动续跑：W 活着 → 续跑 A′ 提交（库里有非终态行，路径 W）。
2. 监督方 `merge A --cleanup`：`worktreeUsers(W)` 含 A′ → 409，提示等 A′ 结束或取消 A′。
3. 反过来：监督方先合并清理 W → A 再被自动续跑：`worktreeAlive(W)` 为假 → 不续跑，A 记 `job.auto_resume_skipped`；手动 `job resume A` → 409。
4. A′ 已提交、在排队时 W 被（其他途径）删掉 → A′ 开跑前复查失败，不启动 agent。

## 安全、数据、运维与回滚

- 修复的是「续跑在主 checkout 或无 `.git` 目录里改代码」的数据风险；新增的只有拒绝路径，不新增写操作。
- 行为变化（写进 CHANGELOG 与 skill）：worktree 已删除的 job 不能再续跑（以前会在主 checkout 里续跑）；worktree 上还有 job 在跑时 `worktree merge` 返回 409。
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
  - 自动续跑遇到失效 worktree：不提交续跑，源 job 有 `job.auto_resume_skipped` 事件。
  - 待办接续遇到失效 worktree：拒绝。
  - 续跑提交后、开跑前删掉 worktree：续跑失败且未启动 agent 进程。
  - worktree 上有排队 / 运行中的续跑时 `MergeWorktree` 返回 `ErrWorktreeInUse`，分支未合并；清理前复查命中时合并成功、清理跳过并有 warning。
- 真实验收（临时 server，exec agent 足以模拟）：造一个 worktree job → 手动 resume 让它在 worktree 里 `sleep` → `worktree merge --cleanup` 得 409；取消后合并清理成功 → 再 resume 得 409，主 checkout 无改动。
- 反向验证：去掉提交时检查 → 「清理后 resume」用例失败；去掉合并前检查 → 「在用时合并」用例失败。

## 结论与人工计划 Gate

核心是把「续跑回落到主 checkout」这条隐式路径改成显式拒绝，并让合并在 worktree 仍被使用时停下；不加锁、不改 schema。等用户答复 Q1–Q3 并批准后写计划。
