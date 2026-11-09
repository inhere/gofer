<!-- template_id: design; template_version: 1.2.0 -->
# 目录锁由 --scope 推出设计

> 状态：Draft 0.1 / 待人工计划批准
> issue：gofer-341t

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-09 | Claude | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：thinking_mode=RIGOROUS。
核心目标：同一 checkout 里改不同目录的可写 job 只要各自声明 `--scope` 就能并行，不再互相 `waiting_dir`；新会话照着 `waiting_dir` 提示和 gofer-usage skill 就能用对，不必自建 worktree 目录。
Scope freeze：只改「独占 job 的锁落在哪」的推导（scope → 锁路径）、多路径锁的获取方式、`waiting_dir` 提示文案、`job run` 帮助分类与 gofer-usage skill 对应 references；不改「要不要锁」的判定规则，不改库表。
非目标：常驻会话持锁时长（deferred，见待确认 Q6）；scope 的强制执行（仍只在审阅时标记越界）；MCP 补 `exclusive_dir` / `worktree` 字段；跨机器的锁；`.git` 层面的并发（index.lock）治理。
Review budget：低暴露度（SR1409），一轮合并轴评审。停止条件：合并轴评审无 `CORE_BLOCKING`，或评审结论只剩待用户裁决的核心选择，即交用户批准。

## 背景与目标

2026-11-09 用户实际遇到：同一项目两个 codex job 都在项目根提交（没带 `--cwd`），各带不同 `--scope`，第二个停在 `waiting_dir` 等第一个；第一个是常驻会话，会话不结束锁就不放。用户原话：「--scope 的也是锁定目录，cwd 变为读目录呢？现在选项很多，感觉新会话不会用了，都是自己建立的 wt dir」。

根因：锁只看 cwd（或显式 `--lock`），`--scope` 只用于提示词「交付约定」与审阅标记。两个 job 声明的改动范围不相交，锁却都落在项目根。

用户可见结果：

- 在仓库根提交、`--scope` 不相交的两个可写 job 同时运行；相交的仍按 FIFO 串行。
- 不带 `--scope` 的提交行为与现在完全相同。
- `waiting_dir` 提示首先推荐 `--scope`；skill 入口把「声明改哪里」写成并行改代码的首选做法，`--worktree` 用于同一处要并行改或需要独立提交的场景。

## 名词

- **独占 job**：`resolveDirExclusive` 判为要取目录锁的 job（默认：非 exec、非只读、非交互的 agent job）。
- **锁路径**：独占 job 取锁的路径集合；任一路径与他人已持有或排在前面的路径「重叠」（相同或祖先 / 后代）即冲突。
- **scope 基准**：scope glob 的相对基准，即 job cwd 所在 git 仓库的顶层目录。
- **固定前缀**：glob 中第一个含通配符（`*`、`?`）的段之前的所有段。

## 范围与非目标

范围：`internal/job`（推导函数、Submit 中的盖章顺序、`dirLocks` 多路径原子获取、execute 的锁循环）、`internal/commands/job.go`（帮助分类与文案）、`internal/i18n` 的 `waiting_dir_help`、`skills/gofer-usage/SKILL.md` 入口两行与 `references/job-advanced.md`「并行：--worktree 与目录锁」一节。HTTP 参考文档只补 `lock_paths` 可能由 scope 推出的一句说明。

非目标见文首声明。受影响项目：仅 gofer。

## 已确认事实与规范

锁本身：

- F1 只有独占 job 取锁；共享 job 既不取锁也不被挡（`internal/job/dirlock.go:12-23`）。冲突判定是重叠而非相等（`dirsOverlap`，`dirlock.go:218-232`），FIFO 且新来者与排队者重叠也要排后（`conflictsLocked`，`dirlock.go:182-192`）。路径经 `util.RealPath` 规整，对不存在的路径也能工作（`internal/util/path.go:20-30`）。
- F2 默认要不要锁由 `resolveDirExclusive` 决定（`internal/job/submit.go:1469-1485`）：`server.dir_lock` 关 → 全共享；显式 `--exclusive-dir` / `--shared-dir` 优先；声明了 `--lock` → 独占（exec 也独占）；否则 agent 类型非 exec 且非 `--read-only`、非交互才独占。peer 上解析不到的 agent 不假定独占。
- F3 锁获取在所有并发槽位之后、状态变 running 之前；worktree job 跳过（`internal/job/execute.go:125-140`，条件 `gates.exclusive && entry.wt == nil`）；`TestWorktreeJobsDoNotConflict`（`internal/job/dirlock_test.go:288`）覆盖。
- F4 锁路径 = `req.LockPaths`，空则 `[]string{req.WorkDir}`（`execute.go:151-154`），**按调用方给出的顺序逐个获取、持有前面的再等后面的**（`execute.go:157-171`）。因此 `--lock a --lock b` 与 `--lock b --lock a` 两个 job 可互相持有一半并等待对方，只能等 `dir_lock_max_wait_sec`（默认 3600s，`internal/config/model.go:1338`）超时失败。
- F5 等锁时先记 `job.waiting_dir` 事件再翻状态（`execute.go:623-640`）；`job show` 打印 `waiting_dir: holder=… lock_paths=…` 和 `cli.job.show.waiting_dir_help`（`internal/commands/job.go:2958-2966`）。现文案推荐 `--read-only`、`--lock`、`--shared-dir`、`--worktree`，未提 `--scope`（`internal/i18n/lang/zh-CN/cli_job.ini:38`、`en/cli_job.ini:40`）。
- F6 释放发生在 job 进程结束时；常驻会话（`--session`）整段生命周期（含 `awaiting_input`）持锁，skill 已写明（`skills/gofer-usage/references/job-advanced.md:98`、`files-and-wakeup.md:90`）。

选项与字段：

- F7 `job run` 相关选项：`--cwd`（`internal/commands/job.go:1484`，相对项目根）、`--worktree`（`:1487`）、`--lock`（`:1507`，「project-relative」，可重复）、`--lock-wait`（`:1508`）、`--read-only`（`:1510`）、`--exclusive-dir` / `--shared-dir`（`:1513-1514`，二者互斥在 `:2332`）、`--scope`（`:1524`，「relative to the repo root」）。
- F8 `--lock` 相对**项目根**解析（`project.SafeJoin(cfg.ExecPath(proj), rel)`，`submit.go:486-496`）；`Scope` 相对 **cwd 所在仓库顶层**（`internal/job/model.go:53-57`，`internal/jobstore/todos.go:123`）。项目根等于仓库顶层时两者一致，项目根含多个仓库或位于仓库子目录时不一致。
- F9 `--lock` 与显式 `--shared-dir` 同用被拒（`submit.go:334-336`）。`dir_lock_mode: repo` 时，cwd 下有嵌套仓库的可写 job 若未声明 `--lock` 且未显式选模式则被拒（`submit.go:497-509`）。
- F10 `LockPaths` 只存在 `request_json`（`model.go:88-91`，无独立列）；`dir_exclusive` 是列（`persistence.go:86-87`）。续接继承源 job 的 `LockPaths`（`internal/job/resume.go:443`），`resume --lock` 只能替换不能清空（`resume.go:400-413`）；会话恢复同样按 `LockPaths` 重建锁（`internal/job/session_recovery.go:99-105`）。续接**不**继承 `Scope`。
- F11 ScopeMatch 规则（`internal/job/scope_check.go:72-87`，web 同规则 `web/src/utils/scope.ts:63`）：`**` 跨段、`*` / `?` 不跨段；不含通配符的 glob 覆盖该路径及其下全部（`internal/job` = `internal/job/**`）；忽略开头 `./` 与结尾 `/`。
- F12 plan todo 有 `--scope`（`internal/commands/plan.go:154`），派发时原样带给 job（`internal/job/plandispatch.go:194`）；todo 没有锁字段。
- F13 HTTP 提交直接解码 `job.JobRequest`（`internal/httpapi/job_handler.go:36`），有 `scope`、`lock_paths`、`exclusive_dir`、`worktree`；MCP 提交工具有 `lock_paths`、`lock_wait_sec`、`read_only`、`scope`（`internal/mcpserver/server.go:765,803,838`），没有 `exclusive_dir` 与 `worktree`。
- F14 skill 入口写「多个 agent 同时改代码：每个一个 git worktree」（`skills/gofer-usage/SKILL.md:94`），`--scope` 只作为审阅用途出现（`SKILL.md:87`、`references/job-advanced.md:214`）；目录锁一节推荐 `--lock` 收窄（`job-advanced.md:277-281`）。这正是新会话倾向自建 worktree 的入口原因。

引用规范：G021–G024（推导逻辑放 `internal/job`，入口层只绑定与转发）、G031、G032（不新增兼容层，不弃用仍在用的选项）、G045 / G046（skill 同步、对外文案通用）、G057（本设计不改 schema）。

## 总体方案

一句话：**scope 决定锁落在哪，不决定要不要锁；cwd 只决定在哪运行。**

1. 推导：独占 job 在没有显式 `--lock`、不是 worktree job 时，若 `Scope` 非空，则把每个 glob 推成一个锁路径，盖章到 `LockPaths`。其余情况与现在完全相同。
2. 推导规则（函数 `ScopeLockPaths(base string, scope []string) (paths []string, ok bool)`，与 `ScopeMatch` 同文件）：
   - 规整：去空白、去开头 `./`、去结尾 `/`；含 `..` 段的 glob 判无效（`ok=false`）。
   - 不含通配符：锁该路径本身（文件或目录都可，不 stat）。与 ScopeMatch「覆盖该路径及其下」一致，且锁按路径包含判定，文件路径同样适用。
   - 含通配符：锁固定前缀（`internal/job/**` → `internal/job`；`web/src/*.vue` → `web/src`；`internal/job*` → `internal`）。
   - 固定前缀为空（`**/*.go`、`*.md`）：整组不推导（`ok=false`），回到现行为（锁 cwd），见 Q1。
   - 多个 glob：逐个推导、去重、去掉被其他路径包含的项；**不**取公共祖先（否则又退回锁根目录）。
   - 不变式：对任意文件 f，`ScopeMatch(g, f)` 为真 ⇒ f 位于 `prefix(g)` 之内。测试以此作性质断言。
3. 基准：scope 基准是 cwd 所在仓库顶层（沿用现有 `gitTopLevel`，`internal/job/worktree.go:385`）。推出的绝对路径换算成相对项目根的路径写入 `LockPaths`，从而续接、会话恢复、`job show` 无需改动即生效。cwd 不在 git 仓库内，或推出路径不在项目根之内时，不推导、回到现行为（Q3）。
4. 多路径锁改为原子获取：`dirLocks.Acquire` 接收路径集合，作为一个 waiter 排队，集合内任一路径冲突即整体等待，授予时一次拿全。消除 F4 的持有并等待（显式多 `--lock` 同样受益）。
5. 选项收敛（不删除，G032）：`--scope`、`--worktree`、`--read-only` 是常用入口；`--lock`、`--exclusive-dir`、`--shared-dir`、`--lock-wait` 移到帮助的进阶分类，skill 入口不再出现，只在 references 进阶段落说明。
6. 文案：`waiting_dir_help` 改为先推荐 `--scope`，并打印推导结果来源；skill 入口改写并行改代码的建议。

## 架构

代码组织（G021–G024）：

| 位置 | 变化 |
|---|---|
| `internal/job/scope_check.go` | 新增纯函数 `ScopeLockPaths`，与 `ScopeMatch` 共用规整逻辑 |
| `internal/job/submit.go` | `resolveDirExclusive` 之后、repo 模式检查之前，调用推导并盖章 `LockPaths` 与 `LockSource` |
| `internal/job/dirlock.go` | `Acquire` 改为路径集合、单 waiter 原子授予 |
| `internal/job/execute.go` | 锁循环改为一次 `Acquire`；等待事件的 `dir` 写出全部锁路径 |
| `internal/commands/job.go` | 只改帮助分类与 `job show` 输出一行来源，不放推导逻辑 |
| `internal/i18n/lang/*/cli_job.ini` | `waiting_dir_help` 新文案 |
| `skills/gofer-usage/` | `SKILL.md` 入口两行、`references/job-advanced.md` 目录锁一节 |

`JobRequest` 新增可选字段 `LockSource string`（`json:"lock_source,omitempty"`，取值 `scope` 或空），只进 `request_json` 与读取视图，用于显示「锁由 scope 推出」；由 Submit 盖章，调用方传入的值一律先清空，不构成新的提交入口（与 D8 一致）。不加列（G057 不触发）。同工作区参照：锁路径解析沿用现有 `--lock` 的 `SafeJoin` 路径（`submit.go:486-496`），推导只是给它产出输入。

判定顺序（Submit 内）：

```text
explicit --lock + --shared-dir  → 报错（现状）
resolveDirExclusive              → 要不要锁（不读 scope）
独占 && LockPaths 空 && !Worktree && Scope 非空
    → ScopeLockPaths(gitTopLevel(workDir), Scope)
        ok  → LockPaths = 相对项目根的推导结果，LockSource = "scope"
        !ok → 不变（锁 cwd），记 job.lock_scope_fallback 事件（原因）
repo 模式嵌套仓库检查             → 推出的 LockPaths 视同已声明
SafeJoin 解析 ResolvedLockPaths   → 现状
```

## 关键流程

两个 job 在仓库根、scope 不相交：

```text
A: --scope 'src/api/**'      → 锁 <repo>/src/api      → running
B: --scope 'web/src/*.vue'   → 锁 <repo>/web/src      → 不重叠 → running
C: --scope 'src/**'          → 锁 <repo>/src          → 与 A 重叠 → waiting_dir（holder=A）
```

`waiting_dir` 时 `job show` 示意（对外文案通用，不含任何项目约定）：

```text
waiting_dir: holder=<job-id> lock_paths=<路径>（来自 --scope | 来自 cwd）
提示：在同一仓库改不同目录时，用 --scope '<glob>,…' 声明要改的路径，锁随之收窄；
      只读任务加 --read-only；需要独立分支或同一处并行改，用 --worktree。
```

续接：源 job 盖章的 `LockPaths` 照常继承；`resume --lock` 仍可替换（F10）。

## 安全、数据、运维与回滚

- 风险 R1：scope 是建议性的，agent 写到 scope 外不受锁保护，可能与另一 job 冲突。缓解：「交付约定」本就要求只改范围内，审阅标记越界文件（F11）；skill 写明「声明多大就锁多大」。
- 风险 R2：同一 checkout 内并行的 job 共享 `.git`，同时 `git commit` 可能撞 `index.lock`。这在用户手动 `--lock` 收窄时已存在；skill 写明需要并行提交时用 `--worktree`（Q5）。
- 风险 R3：锁变窄后，原来被串行掩盖的并发写入会显现。只影响带 scope 且 scope 不相交的 job，属于预期变化。
- 数据：不改 schema；`lock_source` 是 `request_json` 可选字段，旧 server 读到忽略。
- 运维：`server.dir_lock: false` 依旧整体关闭；无新配置项。
- 回滚：退回上一版本即恢复「锁 cwd」；已盖章 `LockPaths` 的历史续接按显式锁处理，语义仍正确。

## 决策

- D1 scope 只决定锁的位置，不改变 `resolveDirExclusive` 的独占判定（exec、只读、交互带 scope 仍共享）。理由：两个维度分开，兼容最好；要让 exec 独占仍用 `--exclusive-dir` / `--lock`。
- D2 显式 `--lock` 优先于 scope 推导，scope 只用于审阅；不取并集。
- D3 `--scope` + 显式 `--shared-dir`：不报错、不取锁（scope 仍有审阅意义）。与 `--lock` + `--shared-dir` 报错不同，因为 scope 不是锁声明。
- D4 `--worktree` + `--scope`：不取锁（现状，F3），scope 仍用于审阅。
- D5 推导结果盖章进 `LockPaths`，不新增列；多个 glob 分别加锁、不取公共祖先。
- D6 多路径锁原子获取，替换逐个获取。
- D7 不删除、不弃用任何现有选项；只调整帮助分类与 skill 入口（G032：这些选项仍有在用场景，如常驻父 job 用 `--shared-dir`）。
- D8 HTTP / MCP 不加新提交字段；`scope` 字段已在两处存在，行为随服务端变化。HTTP 参考文档补一句。

## 待确认事项

- Q1 固定前缀为空（`**/*.go`）时：A 整组回到锁 cwd（现行为）并记事件；B 锁 scope 基准（仓库根）；C 拒绝提交。**推荐 A**：不改变已有提交的锁范围，B 在 cwd 为子目录时会让锁变宽，C 会让原本只用于审阅的 scope 拒单。
- Q2 不含通配符的 glob 指向文件时：A 锁文件路径本身；B 锁其所在目录。**推荐 A**：与 ScopeMatch 语义一致，更窄，路径重叠判定对文件同样成立。
- Q3 cwd 不在 git 仓库、或推出路径落在项目根之外时：A 不推导、锁 cwd；B 以项目根为基准推导。**推荐 A**：scope 的现有定义以仓库顶层为基准，非 git 目录本就无法做审阅比对。
- Q4 `--lock` + `--worktree`（锁被静默忽略）是否改为报错。**推荐保持静默**并在 `job show` 标注「worktree 不取锁」，避免改变现有提交的成败；如用户希望更严格可改为报错。
- Q5 skill 是否把「scope 并行」写成默认首选、`--worktree` 只用于需要独立分支或同一处并行改。**推荐是**，并在同一段写明 R2（并行提交撞 `index.lock`）。
- Q6 常驻会话长时间持锁（`awaiting_input` 期间也持锁）是否本期处理。**推荐 deferred**：本设计已让带 scope 的会话只锁 scope；「等待输入时让出锁」涉及会话状态机与恢复，另开 issue。
- Q7 MCP 提交工具是否补 `worktree` 字段，让 MCP 客户端也有隔离手段。**推荐 deferred**（不在核心目标内）。

## 测试与反向验证

- 推导单测（表驱动）：字面路径、`**`、`*.ext`、段内通配、`./` 与结尾 `/`、`..` 无效、空前缀、多 glob 去重与包含消除。性质测试：随机文件集上 `ScopeMatch(g,f)` ⇒ `dirContains(prefix(g), f)`，与 `scope_check_test` 共享用例。
- 锁单测：原子多路径获取；`[a,b]` 与 `[b,a]` 两 job 不死锁（修改前该用例应在等待上限内卡住，作为反向验证）；FIFO 与取消语义保持。
- Submit 集成（G051–G053）：仓库根、scope 不相交的两个可写 agent job 同时 running；相交时第二个 `waiting_dir`；不带 scope 与现状一致（现有 dirlock 测试全部不改仍通过）；`--lock` 优先；`--shared-dir` 不取锁；worktree 不取锁；续接继承推导出的 `LockPaths`；`dir_lock_mode: repo` 下 scope 视同已声明；空前缀回退并有事件。
- 反向验证：去掉 Submit 中的推导调用，「scope 不相交并行」用例必须失败（第二个 job 停在 `waiting_dir`）。
- 文案：i18n 中英文键齐全；skill 文本不含个人环境与本仓库约定（G046），提交前私有名自查（G031）。

## 结论与人工计划 Gate

本稿给出「scope 推出锁位置、cwd 只管运行目录」的方案：不改 schema、不删选项、不带 scope 的提交行为不变，同时修掉多路径锁持有并等待的问题，并把 skill 入口改成首先推荐 `--scope`。Q1–Q5 的推荐项需用户确认，Q6、Q7 建议 deferred。经一轮合并轴评审且用户明确批准后再写实施计划；批准不授权实施。
