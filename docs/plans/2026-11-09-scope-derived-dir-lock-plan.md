<!-- template_id: plan; template_version: 1.3.0 -->
# 目录锁由 --scope 推出 实施计划

> 状态：Draft 0.1
> issue：gofer-341t

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-09 | Claude（gofer 监督会话） | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：同一 checkout 里改不同目录的可写 job 各自声明 `--scope` 就能并行，不再互相 `waiting_dir`；不带 `--scope` 的提交行为不变；新会话照 `waiting_dir` 提示和 gofer-usage skill 就能用对。Scope freeze：只实施设计 Draft 0.1（已批准）的范围和下文“对设计的细化”。Review budget：低暴露度，一轮合并轴评审；有 `CORE_BLOCKING` 才再修一轮。停止条件：评审无 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

目标：独占 job 的锁由 `--scope` 推出（scope 决定锁落在哪，不决定要不要锁；cwd 只决定在哪运行），多路径锁原子获取，提示文案与 skill 首先推荐 `--scope`。

完成定义：T1–T8 通过；设计“测试与反向验证”各项都有证据；设计决策 D1–D8 与已定事项 Q1–Q5 逐条有对应测试或文档；`gofer-verify` 全绿；文档按 G045 同步；设计稿补实施记录。

## 范围、排除项与授权

范围：`internal/job`（推导函数、Submit 盖章、`dirLocks` 多路径原子获取、execute 锁循环、读取视图的锁来源）、`internal/commands/job.go`（帮助分类、`job show` 来源行）、`internal/i18n/lang/{en,zh-CN}/cli_job.ini`、`skills/gofer-usage/SKILL.md` 与 `references/job-advanced.md`、`docs/reference/http-api.md`、`docs/capabilities.md` 行描述、`CHANGELOG.md`「未发布」。

排除：设计“非目标”全部（常驻会话持锁时长 Q6、MCP 补字段 Q7、scope 强制执行、跨机器锁、`.git` 并发治理）；不改 schema（D5，G057 不触发）；不删除、不弃用任何选项（D7，G032）；不含发版、push、打 tag（用户要求攒一批再发）；能力清单的版本号按 G058 发版时再改，本计划只改行描述。

授权边界：批准本计划不等于授权执行，执行需要单独的当前请求。本地提交按既有默认授权；push、打 tag、发版、升级不在本计划内。

- `host_or_non_offline_action=REQUIRED`

需要的主机动作：建、删 worktree（主机 job）；suga 实施 job；`gofer-verify`。不需要真实 agent 进程，集成测试用 `testcmd` 假 agent。

## 输入与批准证据

- 设计：`docs/design/2026-11-09-scope-derived-dir-lock-design.md` Draft 0.1。用户 2026-11-09：“设计都按推荐项批准”。Q1 空前缀退回锁 cwd 并记事件；Q2 文件锁文件本身；Q3 不在 git 仓库或越出项目根不推导；Q4 `--lock`+`--worktree` 不报错、`job show` 标注；Q5 skill 首选 scope；Q6、Q7 deferred。
- 规范：G021–G024、G031、G032、G045、G046、G051–G053、G057、G058、SR1211、SR1433。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | scope → 锁路径推导、多路径锁、提交判定 | `ScopeMatch` / `globRegexp`（`scope_check.go`）、`gitTopLevel`（`worktree.go:388`）、`dirsOverlap` / `dirContains`（`dirlock.go`）、`--lock` 的 `SafeJoin` 解析（`submit.go:486-496`） | 是 | 原位扩展：推导函数与 `ScopeMatch` 同文件共用规整；`Acquire` 改收路径集合 | DIRECT_REUSE | none | 无新模块；`LockSource` 只进 `request_json`，旧 server 忽略 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 对设计的细化

代码核对发现设计没定或说法不够具体的地方，按最小方案定下，批准本计划即确认。都不改变设计的用户结果。

| # | 问题 | 决定 |
|---|---|---|
| R1 | 设计写“`resolveDirExclusive` 之后、repo 模式检查之前”调用推导，但 `req.Worktree` 在 `submit.go:447` 才解析，`workDir` 在 `:458` 才得到，推导两者都要用 | 推导放在 `workDir` 解析之后、`ResolvedLockPaths` 解析（`:486`）之前，仍在 repo 模式检查（`:497`）之前。`dirExclusive` 已在 `:337` 定下，推导不回头改它（D1）。这样 `ResolvedLockPaths`、`heldLockPaths`（`:1143`）、续接与会话恢复都直接用上推导结果 |
| R2 | remote（peer / worker runner）时本端 `workDir` 为空 | 本端不推导、不记回退事件，行为与现状相同；peer 端自己的 Submit 按同一规则推导。worker 执行侧现无目录锁，不变 |
| R3 | `LockSource` 由 Submit 盖章、调用方传入一律清空，但 hold 审批重入（`heldJobID`）和服务端续接（`ResumeSourceAgent`）的请求来自已存的 `request_json` | 清空只对外部提交生效：`heldJobID == "" && ResumeSourceAgent == ""` 时先清空。续接在 `resume.go:443` 旁同样从源 `request_json` 继承 `LockSource`；`resume --lock` 替换锁时置空。重跑（`rebuild.go`）覆盖 `LockPaths` 时置空 `LockSource`，不覆盖时随源请求继承 |
| R4 | 回退事件在 Submit 时还没有 job 行 | 与 `job.notify_dropped` 同一做法（`submit.go:1381`）：推导返回原因，行建好后记 `job.lock_scope_fallback {reason, scope}`，`reason` 取 `empty_prefix` / `invalid_glob` / `not_git` / `outside_project`。只在“独占、无显式锁、非 worktree、scope 非空”时才会产生 |
| R5 | `ScopeLockPaths(base, scope)` 只返回 `ok`，事件需要原因 | 纯函数签名定为 `ScopeLockPaths(scope []string) (prefixes []string, reason string)`，只做 glob → 仓库相对前缀（不碰文件系统）；`reason` 非空即不推导。仓库顶层与项目根的换算、越界判断放 Submit 的小助手 `scopeLockPathsForSubmit`（同包），便于纯函数做表驱动与性质测试 |
| R6 | 设计写“等待事件的 `dir` 写出全部锁路径” | `job.waiting_dir` 事件保留 `dir`（字符串，填造成等待的那条路径，web 读取不变），新增 `lock_paths`（全部锁路径）。`onWait` 每个 waiter 只回调一次 |
| R7 | Q4 的“`job show` 标注 worktree 不取锁” | `job show` 的 `dir:` 行在 worktree job 且声明了 `LockPaths` 时追加 i18n 文案“worktree 不取目录锁”；提交成败不变 |
| R8 | 读取视图怎样带来源 | `JobResult` 增加 `LockSource string`（`lock_source,omitempty`），由 `persistence.go:431` 旁从 `request_json` 填。`job show` 用它区分“来自 --scope / 来自 --lock / 来自 cwd” |

## 前置检查与 fail-closed 条件

workspace baseline：

- Git root：`inhere-tools/gofer`，分支 `main`。同一 checkout 里另有 agent 在改 `internal/project` 和其他 docs：本计划不碰 `internal/project`，提交只暂存自己的文件（SR1211）。
- 实施在 `.worktrees/341t`（分支 `feat/341t-scope-lock`），开 worktree 前先提交 tracker；worktree 在主机建。合并用 `--no-ff`，合并后删 worktree 和分支。
- expected paths 见各任务；实施发现新路径按四类记录。

fail-closed，以下任一情况停止并回到设计：

- 推导需要改变 `resolveDirExclusive` 的独占判定才能成立（违反 D1）。
- 不带 `--scope` 的现有 dirlock 测试（`dirlock_test.go`、`u5_dirlock_test.go`）需要改断言才能通过。
- 需要新增库表列或新的提交字段（违反 D5、D8）。
- 原子获取改变了现有 FIFO 或取消语义（`TestDirLockSerializesWritableAgentJobs`、`TestCancelWhileWaitingDir` 需改断言）。

派活约束（SR1433）：任务书写明 G021–G024（推导逻辑在 `internal/job`，`internal/commands` 只绑定与输出）、G031、idevstd 加载方式、SR1211 只暂存自己的文件；不做无关重排，不删注释；不在 export 了 `GIT_DIR`/`GIT_WORK_TREE` 的命令里跑测试；不起后台空等；job 用 `--sync`。子 agent 的完成报告只是声明，监督会话逐条核实并做反向验证。

## 波次与依赖

```text
W1  T1 推导纯函数、T2 多路径原子锁（文件不相交，可并行，各自提交）
W2  T3 Submit 盖章（依赖 T1）→ T7 集成测试与反向验证（依赖 T2、T3）
W3  T4 job show 与文案（依赖 T3）→ T5 帮助分类（同文件，排在 T4 后）
W4  T6 skill 与 http-api（依赖 T3、T4）→ T8 CHANGELOG 与能力清单（依赖 T6）
```

执行方：T1、T2、T3、T7（推导、锁、提交判定及其测试）由 sonnet 子 agent 在 worktree 内实施；T4、T5、T6、T8（文案、帮助分类、skill/文档、CHANGELOG）派 `-a suga`（gpt 模型）在同一 worktree 实施。同一 worktree 内同一时刻只有一个执行方写代码，每个任务一个提交。监督会话审查 diff、做反向验证、合并。

## 任务

命令的运行目录都是 worktree 根。每个任务提交前跑 `go build ./... && go vet ./...`。

### T1 推导纯函数 ScopeLockPaths

- 执行方: sonnet 子 agent。
- 文件: `internal/job/scope_check.go`、`internal/job/scope_check_test.go`。
- 动作:
  - 抽出 `ScopeMatch` 现有规整（去空白、去开头 `./`、去结尾 `/`）为共用函数，`ScopeMatch` 行为不变。
  - 新增 `ScopeLockPaths(scope []string) (prefixes []string, reason string)`（R5）：含 `..` 段或绝对路径判 `invalid_glob`；不含通配符锁该路径本身，不 stat（Q2）；含 `*` / `?` 取第一个通配段之前的段；任一 glob 固定前缀为空则整组 `empty_prefix`（Q1）；逐个推导后去重、去掉被其他前缀包含的项，不取公共祖先（D5）。输出用 `/` 分隔、排序稳定。
- 验证: `go test ./internal/job -run 'TestScopeMatch|TestScopeLockPaths' -count=1`。
  - `TestScopeLockPaths` 表驱动：`internal/job` → `internal/job`；`internal/job/**` → `internal/job`；`web/src/*.vue` → `web/src`；`internal/job*` → `internal`；`./a/b/` → `a/b`；`a/../b` → `invalid_glob`；`**/*.go`、`*.md` → `empty_prefix`；`[a/x/**, a/**]` → `[a]`；`[src/api/**, web/src/*.vue]` → 两项且不合并为根；`docs/x.md`（文件）→ `docs/x.md`。
  - `TestScopeLockPathsCoversScopeMatch` 性质测试：复用 `TestScopeMatch` 的 glob 集，加固定种子生成的随机相对路径集，断言 `ScopeMatch(g, f)` ⇒ `f` 等于或位于 `prefix(g)` 之内（用 `dirContains` 同一判定）。
- 反向验证: 把前缀改为“含第一个通配段且去掉通配符”（`internal/job*` → `internal/job`），性质测试必须失败（`internal/jobx/a.go` 被匹配但不在前缀内）；把多 glob 改为取公共祖先，`[src/api/**, web/src/*.vue]` 用例必须失败。
- 完成标准: 上述用例通过，`TestScopeMatch` 不改。
- 依赖: 无。

### T2 dirLocks 多路径原子获取

- 执行方: sonnet 子 agent。
- 文件: `internal/job/dirlock.go`、`internal/job/execute.go`（锁循环与 `enterWaitingDir`）、`internal/job/dirlock_test.go`。
- 动作:
  - 先写交叉顺序测试并在**未改代码**上运行，确认它在等待上限内卡住（失败），输出记入 `tmp/341t-impl/reverse.md`。
  - `Acquire(ctx, dirs []string, jobID string, onWait func(holder, dir string))`：整个集合作为一个 waiter 排队；`conflictsLocked` / `conflictsHeldLocked` 对集合内任一路径判定；授予时一次登记全部路径，`release` 一次释放全部。FIFO 不变：新来者与前面排队者任一路径重叠也排后。取消时整体出队，不持有任何路径。
  - `execute.go:151-171` 的逐个获取改为一次 `Acquire`；`enterWaitingDir` 事件按 R6 写 `dir` 与 `lock_paths`。
- 验证: `go test ./internal/job -run 'DirLock|WaitingDir|Worktree|LockPaths|RepoMode' -count=1`，并 `-race` 跑 `./internal/job -run 'DirLock|LockPaths'`。
  - `TestDirLockCrossOrderNoDeadlock`：两个 exec job 分别 `--lock a --lock b` 与 `--lock b --lock a`（假 agent 各运行一小段），`dir_lock_max_wait_sec` 设为几秒；断言两个都成功结束，没有 `dir_lock` 超时失败。
  - `TestDirLockMultiPathAtomic`：A 持 `[a]`；B 要 `[a,b]` 进入 `waiting_dir` 时 `b` 未被占（C 要 `[b]` 且排在 B 后，按 FIFO 等 B）；A 结束后 B 一次拿全。
  - `TestDirLockMultiPathCancelHoldsNothing`：B 等待中取消，之后 C 要 `[b]` 立即运行。
  - 现有 `TestDirLockSerializesWritableAgentJobs`、`TestDirLockAncestorDescendantConflict`、`TestCancelWhileWaitingDir`、`TestDeclaredLockPathsAllowSiblingJobs` 不改断言。
- 反向验证: 交叉顺序测试在改动前失败（上文），改动后通过；把 `Acquire` 改回逐个持有再等，必须再次失败。
- 完成标准: 显式多 `--lock` 不再出现持有并等待（D6）；现有测试不改断言全部通过。
- 依赖: 无。

### T3 Submit 盖章 LockPaths / LockSource

- 执行方: sonnet 子 agent。
- 文件: `internal/job/model.go`（`JobRequest.LockSource`、`JobResult.LockSource`、事件常量 `EventJobLockScopeFallback`）、`internal/job/submit.go`、`internal/job/resume.go`、`internal/job/rebuild.go`、`internal/job/persistence.go`、新增 `internal/job/scope_lock_test.go`。
- 动作:
  - 外部提交先清空 `LockSource`（R3）。
  - 按 R1 位置：`!remote && dirExclusive && len(LockPaths)==0 && !Worktree && len(Scope)>0` 时调用 `scopeLockPathsForSubmit(ctx, projectRoot, workDir, scope)`：`gitTopLevel(workDir)` 失败 → `not_git`（Q3）；`ScopeLockPaths` 返回原因照用；前缀拼到仓库顶层后用 `filepath.Rel` 换成项目根相对路径，任一路径越出项目根 → `outside_project`（Q3）。成功则 `LockPaths = 结果`、`LockSource = "scope"`；失败不动 `LockPaths`（锁 cwd），按 R4 在行建好后记事件。
  - 显式 `--lock` 优先，不取并集（D2）；显式 `--shared-dir` 时 `dirExclusive=false` 不推导、不报错（D3）；worktree 不推导（D4）；只读、exec、交互仍共享（D1，`resolveDirExclusive` 不改，不读 scope）。
  - repo 模式检查（`:497`）在推导之后执行，推出的 `LockPaths` 非空即视同已声明（设计判定顺序）。
  - 续接、重跑按 R3 继承或置空 `LockSource`；读取视图按 R8 填 `LockSource`。
- 验证: `go test ./internal/job -run 'ScopeLock|LockPaths|RepoMode|Resume' -count=1`，`scope_lock_test.go` 断言提交后的 `request_json`：
  - `TestScopeLockStampsLockPaths`：仓库根提交、`--scope 'src/api/**,web/src/*.vue'` → `lock_paths=[src/api, web/src]`、`lock_source=scope`。
  - `TestScopeLockRelativeToProjectRoot`：项目根是仓库子目录或 cwd 在嵌套仓库时换算正确；越出项目根 → 无 `lock_paths`、事件 `reason=outside_project`。
  - `TestScopeLockFallbackEvents`：`**/*.go` → 事件 `empty_prefix`；非 git 目录 → `not_git`；`a/../b` → `invalid_glob`；三者都不改 `lock_paths`。
  - `TestScopeLockExplicitLockWins`（D2）、`TestScopeLockSharedDirNoLock`（D3，提交成功、`dir_exclusive=false`、无事件）、`TestScopeLockWorktreeNoLock`（D4）、`TestScopeLockReadOnlyAndExecStayShared`（D1）。
  - `TestScopeLockCallerLockSourceCleared`：HTTP 形态请求带 `lock_source=scope` 和显式 `lock_paths` → 落库 `lock_source` 为空。
  - `TestRepoModeAcceptsScopeDerivedLock`：`dir_lock_mode: repo`、cwd 下有嵌套仓库、只带 `--scope` → 提交成功。
  - `TestResumeInheritsScopeDerivedLock`：续接继承 `lock_paths` 与 `lock_source`；`resume --lock x` 后 `lock_source` 为空。
- 反向验证: 删掉 repo 模式前的推导调用，`TestRepoModeAcceptsScopeDerivedLock` 必须失败；去掉清空，`TestScopeLockCallerLockSourceCleared` 必须失败。
- 完成标准: D1–D5 各有用例；`resolveDirExclusive` 无改动（`git diff` 核对）。
- 依赖: T1。

### T4 job show / waiting_dir 输出来源与 i18n 文案

- 执行方: suga（gpt）。
- 文件: `internal/commands/job.go`（`job show` 段，约 `:2958-2966`）、`internal/i18n/lang/en/cli_job.ini`、`internal/i18n/lang/zh-CN/cli_job.ini`、对应 `_test.go`。
- 动作:
  - `waiting_dir:` 行追加来源：`lock_source=scope` →“来自 --scope”；`lock_paths` 非空且无来源 →“来自 --lock”；空 →“来自 cwd”。来源文字走 i18n 键。
  - `waiting_dir_help` 改为先推荐 `--scope`（设计“关键流程”示意）：同一仓库改不同目录用 `--scope '<glob>,…'` 收窄锁；只读加 `--read-only`；需要独立分支或同一处并行改用 `--worktree`。不再列 `--lock` / `--shared-dir`。
  - R7：worktree job 声明了 `LockPaths` 时，`dir:` 行追加“worktree 不取目录锁”（Q4）。
  - 文案通用，不含本仓库约定（G046）。
- 验证: `go test ./internal/i18n -run CatalogKeysMatch -count=1`、`go test ./internal/commands -run 'JobShow' -count=1`；新增 `TestJobShowWaitingDirLockSource`（三种来源各一条，断言输出含对应文案）与 `TestJobShowWorktreeLockIgnoredNote`；`go test ./internal/testutil/cjkscan -count=1`。
- 完成标准: 中英文键齐全，`job show` 能看出锁来源。
- 依赖: T3。

### T5 job run 帮助分类

- 执行方: suga（gpt）。
- 文件: `internal/commands/job.go`（`job run` 选项声明，约 `:1484-1525`）、对应 `_test.go`。
- 动作: `--lock`、`--lock-wait`、`--exclusive-dir`、`--shared-dir` 移到新分类 `Directory lock (advanced)`；`--scope` 帮助改为说明“同时把目录锁收窄到这些路径（独占 job）”；`--cwd` 帮助写明只决定运行目录。不删除、不改名任何选项（D7）。只改分类与帮助文字，不放推导逻辑（G022）。
- 验证: `go test ./internal/commands -count=1`；新增 `TestJobRunLockOptionsInAdvancedCategory` 断言四个选项的 `Category`、`--scope` 仍在 `Execution`；`go run ./cmd/gofer job run --help` 人工看分类顺序。
- 完成标准: 帮助里常用入口是 `--scope` / `--worktree` / `--read-only`。
- 依赖: T4（同文件，顺序提交）。

### T6 gofer-usage skill 与 http-api

- 执行方: suga（gpt）。
- 文件: `skills/gofer-usage/SKILL.md`、`skills/gofer-usage/references/job-advanced.md`、`docs/reference/http-api.md`。
- 动作:
  - `SKILL.md` 入口两行：`:94` 改为“多个 agent 同时改同一 checkout 的不同目录：各自带 `--scope`，锁随之收窄、可并行；同一处要并行改或需要独立提交，用 `--worktree`”；`:87` 补一句 scope 也收窄目录锁。
  - `job-advanced.md`「并行：--worktree 与目录锁」一节：首选 `--scope`；“声明多大就锁多大”（R1 风险）；同一 checkout 并行提交可能撞 `index.lock`，需要并行提交用 `--worktree`（R2 风险，Q5）；空前缀、非 git 目录退回锁 cwd 并有 `job.lock_scope_fallback` 事件；`--lock` / `--shared-dir` / `--exclusive-dir` 作为进阶段落保留；`--lock`+`--worktree` 时锁不生效。
  - `http-api.md` 的 `lock_paths` 说明补一句：独占 job 未给 `lock_paths` 时由 `scope` 推出，读取视图的 `lock_source=scope` 标明来源（D8，不加提交字段）。
  - G045 / G046：通用视角，不写个人环境与本仓库约定。
- 验证: `sh scripts/check-private-names.sh`（worktree 里设 `PRIVATE_NAMES_FILE=<主 checkout 绝对路径>/.private-names`）；人工核对文档与 T3/T4 实现一致；`grep -n 'scope' skills/gofer-usage/SKILL.md` 能看到入口两行。
- 完成标准: skill 入口不再把 worktree 写成并行改代码的唯一做法；进阶选项只在 references 出现。
- 依赖: T3、T4。

### T7 集成测试与反向验证

- 执行方: sonnet 子 agent。
- 文件: `internal/job/scope_lock_integration_test.go`（新）、`tmp/341t-impl/reverse.md`（不入库）。
- 动作: 按 G051–G053 用真实 Service、临时 git 仓库和 `testcmd` 假 agent 跑提交到执行全链路。
- 验证: `go test ./internal/job -run 'ScopeLockIntegration|DirLock|LockPaths|RepoMode' -count=1` 与 `-race`：
  - `TestScopeLockIntegrationDisjointRunConcurrently`：仓库根两个可写 agent job，`--scope 'src/api/**'` 与 `--scope 'web/src/*.vue'`，断言两者同时处于 `running`。
  - `TestScopeLockIntegrationOverlapWaits`：再提交 `--scope 'src/**'`，断言 `waiting_dir`、`holder` 为第一个 job、事件含 `lock_paths`。
  - `TestScopeLockIntegrationNoScopeUnchanged`：不带 scope 的两个 job 仍串行；现有 `dirlock_test.go`、`u5_dirlock_test.go` 不改仍通过。
  - `TestScopeLockIntegrationEmptyPrefixFallsBack`：`**/*.go` 的 job 锁 cwd，与另一带 scope 的 job 冲突，事件 `job.lock_scope_fallback` 存在。
  - `TestScopeLockIntegrationResumeKeepsLock`：续接 job 与源 job 同样的锁行为。
- 反向验证（临时改动，不提交，结果写 `reverse.md`）：去掉 Submit 中的推导调用，`DisjointRunConcurrently` 必须失败（第二个停在 `waiting_dir`）；`ScopeLockPaths` 恒返回 `empty_prefix`，同一用例必须失败；T1、T2、T3 各自列出的反向验证复核一遍。
- 完成标准: 设计“测试与反向验证”全部有证据。
- 依赖: T2、T3。

### T8 CHANGELOG 与能力清单

- 执行方: suga（gpt）。
- 文件: `CHANGELOG.md`、`docs/capabilities.md`。
- 动作: `## [未发布]` 增加“变更：独占 job 的目录锁由 `--scope` 推出，scope 不相交的 job 可在同一 checkout 并行；多路径锁改为原子获取，交叉顺序的 `--lock` 不再互等；`waiting_dir` 提示与帮助首选 `--scope`（gofer-341t）”。能力清单只改“worktree 隔离与交付”“验收标准与范围”两行的描述，说明 scope 收窄目录锁；版本号不动（G058）。
- 验证: `sh scripts/check-private-names.sh`；`git diff --stat` 只含这两个文件。
- 完成标准: 用户可见变化已记入「未发布」。
- 依赖: T6。

## 验证

- 每个任务：上文验证命令；`go build ./... && go vet ./...`。
- 全量：`go test ./... -count=1`；`go test ./internal/job -race -count=1`；`gofer-verify` 工作流（四路）通过。
- 审查：每个提交看 `git diff --stat` 查无关删除与重排；核对 `resolveDirExclusive` 与 schema 无改动；逐条对照设计 D1–D8 核实执行方的声明。
- 主 checkout 里 `sh scripts/check-private-names.sh` 通过。

## 回滚与恢复

- 恢复点：每个任务一个提交，worktree 隔离；合并前 main 不受影响。合并后退回用 `git revert -m 1 <merge>`。
- 运行时回滚：退回旧版即恢复“锁 cwd”；已盖章 `LockPaths` 的历史续接按显式锁处理，语义仍正确；`lock_source` 旧版忽略。

## 人工 Gate

1. 批准本计划。
2. 当前执行请求（可与批准同一句）。
3. push、打 tag、发版：不在本计划内，攒批后按发版流程另行处理。

实施过程本身没有其他人工 Gate，除非出现 Semantic Amendment 或 fail-closed。

## 可追溯性

| 设计决策 / 已定事项 | 任务 | 验证 |
|---|---|---|
| D1 scope 不改独占判定 | T3 | `TestScopeLockReadOnlyAndExecStayShared`，`resolveDirExclusive` 无 diff |
| D2 显式 `--lock` 优先 | T3 | `TestScopeLockExplicitLockWins` |
| D3 `--scope`+`--shared-dir` 不报错不取锁 | T3 | `TestScopeLockSharedDirNoLock` |
| D4 / Q4 worktree 不取锁、`job show` 标注 | T3 T4 | `TestScopeLockWorktreeNoLock`、`TestJobShowWorktreeLockIgnoredNote` |
| D5 盖章进 `LockPaths`、不加列、不取公共祖先 | T1 T3 | `TestScopeLockPaths`、`TestScopeLockStampsLockPaths` |
| D6 多路径原子获取 | T2 | `TestDirLockCrossOrderNoDeadlock`（改前失败） |
| D7 不删选项、只调分类 | T5 T6 | `TestJobRunLockOptionsInAdvancedCategory` |
| D8 不加提交字段、HTTP 文档一句 | T3 T6 | `TestScopeLockCallerLockSourceCleared`、文档核对 |
| Q1 空前缀退回锁 cwd 并记事件 | T1 T3 T7 | `TestScopeLockFallbackEvents`、`...EmptyPrefixFallsBack` |
| Q2 文件锁文件本身 | T1 | `TestScopeLockPaths` 文件用例 |
| Q3 非 git / 越出项目根不推导 | T3 | `TestScopeLockFallbackEvents`、`TestScopeLockRelativeToProjectRoot` |
| Q5 skill 首选 scope、写明 `index.lock` | T6 | 文档核对 |
| repo 模式视同已声明 | T3 | `TestRepoModeAcceptsScopeDerivedLock` |
| 续接继承 | T3 T7 | `TestResumeInheritsScopeDerivedLock` |
| 不相交并行（核心目标） | T7 | `TestScopeLockIntegrationDisjointRunConcurrently` + 反向验证 |
| 提示文案与 i18n | T4 | `CatalogKeysMatch`、`TestJobShowWaitingDirLockSource` |

## 完成 Gate 与剩余工作

完成 Gate：T1–T8 通过；反向验证写入 `tmp/341t-impl/reverse.md`；`gofer-verify` 全绿；合并回 main（`--no-ff`）并删 worktree 和分支；设计稿补实施记录；计划进入 COMPLETE 时写不超过 8 行的完成总结；关 `gofer-341t`。

剩余工作（不在本计划内）：攒批发版时 push 并更新能力清单版本号（G058）；Q6 常驻会话等待输入时让出锁、Q7 MCP 补 `worktree` 字段，另开 issue。
