<!-- template_id: design; template_version: 1.1.1 -->
# 本地优先的 issue/memory 跟踪（TRK-01）与未提交改动守卫（GIT-01）

> 状态：Draft 0.1（待用户批准）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-27 | Claude | 初稿：用户要求 gofer 接管 bd 的 issue + memory（本地优先写 jsonl、连上 server 再同步），并解决"agent 改了一堆文件不提交"；IDEV-STD 适配等本功能可用后再做 |

## 背景与目标

1. **agent 不提交、改动越积越多。** 根因是 bd 往 `CLAUDE.md`/`AGENTS.md` 注入的托管块（`BEGIN BEADS INTEGRATION … profile:minimal`）写着 "Do not commit or push without clear authority"，并声称压过其他指令；gofer 的 `house-rules` 与工作空间 R003 虽写了"按功能点本地提交"，交互会话读到的仍是 bd 这段。该块由 bd 按哈希维护，手改会被覆盖。
2. **bd 实际只用了 issue 和 memory。** 用户确认其余功能（Dolt 远端同步、`refs/dolt/data` 等）都没用过。
3. **目标**：
   - 交付 gofer 自己的 issue + memory，数据**本地优先**：写仓库里的 jsonl，离线可用；连得上 server 时同步上去，供 web 查看/编辑和 job 联动；
   - 会话开场注入由 gofer 掌握，提交策略写成我们自己的口径（默认"按功能点本地提交，不 push"）；
   - 一个与说辞无关的兜底：job 结束时发现"本次新增且未提交"的改动就标出来，按项目策略提醒/转验收/续接一轮去提交。

## 范围与非目标

- 范围：GIT-01 未提交守卫；TRK-01 本地存储与 CLI（issue、memory）、`gofer prime` 与 hooks、从 bd 迁移、server 镜像与同步、`job run --issue` 联动、web Issues 页。
- 非目标：Dolt、跨仓库 git 远端同步通道（`refs/dolt/data` 一类）、epic/lease/heartbeat 等 bd 特有语义；全局（跨仓库）memory 留到后续；**IDEV-STD（inhere-dev-standards）的适配不在本批**，等本功能可用后另做。

## 已确认事实与规范

实测（2026-09-27，本工作空间 `zy-bsly-sf-dev`，bd 1.3.0）：

| 项 | 事实 |
|---|---|
| 使用面 | 20+ 仓库有 `.beads`（含若干 `tmp/` 克隆）；本仓 348 个 issue（275 closed / 43 open / 29 in_progress / 1 blocked），类型 task 166、bug 151、feature 29、epic 1、chore 1；memory 37 条 |
| issue 字段 | 必有：id、title、status、priority、issue_type、owner、created_at/by、updated_at；常用：description(341)、closed_at/close_reason(275)、started_at、assignee、notes(159)、dependencies(119)、acceptance_criteria(108)；少量：comments、design、labels；子项 id 形如 `<parent>.<n>` |
| memory | `bd memories --json` 输出 `{key: content}` |
| 接入点 | `.claude/settings.json` 的 SessionStart 跑 `bd prime --hook-json`；`git config core.hooksPath` 指向 `.beads/hooks`（本仓还是失效的旧路径 `D:\work\zy-bsly-sf-dev\.beads\hooks`）；`CLAUDE.md`/`AGENTS.md` 有带哈希的托管块 |
| 已知坑 | bd 的 jsonl 是 Dolt 的被动导出，auto-export 不保证落盘（bd 记忆 `bd-auto-export-may-not-flush`），提交前要手动 `bd export` |
| gofer 现状 | 已有 `.gofer/`（`RULES.md`、`templates/`）；plan/todo、评论层、规则注入、`gofer init hooks`（Claude/Codex）、job 结束采集 `changes.diff`（未提交改动快照，只存档不提醒） |

## 总体方案

### GIT-01 未提交改动守卫

- job 开始时在执行机的 cwd 仓库记一份基线：`git status --porcelain=v2 -z` 的路径集合 + 已脏文件的内容哈希（`git hash-object`）。结束时再取一次，**本次新增的脏路径**（新出现的，或基线里已脏但内容哈希变了的）即"本次未提交"。
- 结果落 job：`uncommitted_files`（最多 200 个路径 + 总数）；事件 `job.uncommitted`；web 的 job 详情、Board、工作台会话头显示「未提交 N」徽标。
- 项目策略 `on_uncommitted: off | warn | review | resume`（默认 `warn`）：
  - `warn` 只标记与发事件；
  - `review` 把本应 done 的 job 转 `needs_review`；
  - `resume` 自动续接同一会话一轮（固定提示：列出文件、要求按功能点本地提交、不 push、不提交无关文件），计入 `auto_resume_max` 预算，续接后仍未提交则降级为 `review`。
- 只作用于 agent job（exec 不管）；worktree job 查它的 worktree；cwd 不是 git 仓库则跳过。嵌套仓库：额外扫描 cwd 下深度 ≤2、未被忽略的 `.git` 目录（覆盖"cwd 是项目根、代码在 `tools/gofer`"这类情况）。

### TRK-01 本地存储（真源在仓库里）

- 目录 `.gofer/tracker/`（提交进 git）：
  - `issues.jsonl`：一行一个 issue 的完整快照，**按 id 排序、字段顺序固定**，改一条只动一行，git diff 小；
  - `memories.jsonl`：一行一条 `{key, content, updated_at, by}`；
  - `config.yaml`：`prefix`（id 前缀）、`tracker_id`（init 时生成的 uuid，同步时的仓库身份）、`commit_policy`、`auto_sync`。
- `.gofer/tracker/.local/`（gitignore）：锁文件、同步状态与同步基线快照。
- **jsonl 就是真源**（不是某个数据库的导出），不存在"导出没刷盘"的问题。
- 写入：取锁 → 读全文件 → 改 → 写临时文件 → rename。锁用 `O_EXCL` 建锁文件（写 pid/主机/时间，30 秒过期可抢），因为容器与主机共享同一目录，flock 跨挂载不可靠。
- issue 字段对齐 bd 实际用到的：id、title、type、status（open / in_progress / blocked / closed）、priority（0–4）、description、design、acceptance_criteria、notes（**条目列表** `{at, by, text}`，便于合并）、assignee、owner、labels、parent、deps（`{id, type}`）、comments（条目列表）、created_at/by、updated_at、started_at、closed_at、close_reason。
- id：`<prefix>-<4 位 base36 随机>`，本地查重；子项 `<parent>.<n>`。从 bd 导入的 id 原样保留，提交信息和文档里的引用继续有效。
- 仓库发现：从 cwd 向上找最近的 `.gofer/tracker/`，与 bd 找 `.beads` 相同；`--tracker <dir>` 可显式指定。

### CLI

| 命令 | 说明（对应 bd） |
|---|---|
| `gofer issue init [--prefix]` | 建 `.gofer/tracker/` |
| `gofer issue ready` / `ls [--status --type --label --all]` / `show <id>` | `bd ready/list/show` |
| `gofer issue create -t … [--type --priority --parent --dep …]` | `bd create` |
| `gofer issue update <id> [--claim --status --title --append-notes …]` / `close <id> [--reason]` / `dep add <id> <on>` | `bd update/close/dep` |
| `gofer issue sync` | 与 server 同步（见下） |
| `gofer issue migrate --from-bd [--apply]` | 从 bd 迁移（见下），默认只打印计划 |
| `gofer memory set <key> <content>` / `ls [kw]` / `show <key>` / `rm <key>` | `bd remember/memories/recall/forget`（保留 `remember`/`forget` 别名） |
| `gofer prime [--hook-json]` | 代替 `bd prime` |

新增顶层命令只有 `issue`、`memory`、`prime` 三个，其余全部作为子命令。

### `gofer prime`（会话开场注入）

输出四段，总量有上限（默认 8 KiB，超出截断并说明）：

1. **提交策略**（来自 `commit_policy`，默认 `local-commit`）："按功能点本地提交是默认授权；不要 push；`.gofer/tracker/*.jsonl` 的变化随功能点一起提交；提交前 `git status` 确认没有夹带无关文件。"另有 `ask`（提交前问人）与 `none`（不提交）两档供特殊仓库使用。
2. 进行中与已认领的 issue；
3. `ready` 的前 10 条；
4. memory 全量（超限时先按更新时间保留最新的）。

`gofer init hooks` 负责把 SessionStart 装成 `gofer prime --hook-json`（Claude），Codex 走其 hooks 等价物。gofer 派出的 job：若 cwd 仓库有 tracker，把第 1、4 段并入规则注入（同一套大小上限与记名称/sha）。

### 从 bd 迁移（`gofer issue migrate --from-bd`）

默认 dry-run，打印将要做的每一步；`--apply` 才执行：

1. 读 `.beads/issues.jsonl`，按上表映射字段（notes 整段作为一条条目；bd 未知状态映射为 open 并打标签 `bd:<原状态>`），写 `issues.jsonl`；
2. 有 `bd` 可执行时用 `bd memories --json` 导出 memory，否则跳过并提示；
3. 把 `CLAUDE.md`/`AGENTS.md` 里 `BEGIN/END BEADS INTEGRATION` 托管块替换为 gofer 的短块（带 `BEGIN/END GOFER TRACKER` 标记，写明用 `gofer issue`/`gofer memory` 与提交策略）；
4. `.claude/settings.json` 的 `bd prime --hook-json` 换成 `gofer prime --hook-json`（Codex hooks 同理）；
5. `core.hooksPath` 指向 `.beads/hooks` 时 unset（失效路径同样处理），并在汇报中列出原值；
6. `.beads/` 原样保留作只读归档，不删除。

迁移可重复执行：已存在的 id 按 `updated_at` 较新者为准，不重复导入。

### server 镜像与同步

- server 新表：`tracker_repos {tracker_id, project_key, rel_path, prefix, last_sync_at}`、`tracker_issues {tracker_id, id, body_json, rev, updated_at}`、`tracker_memories {tracker_id, key, …}`。
- `gofer issue sync`：把本地与"上次同步基线"（`.local/sync-base.jsonl`）的差异推给 server，拉回 server 自上次以来的变更（web 编辑、job 联动、其他机器），**三方合并**后写本地：
  - 标量字段：只有一方改了就取那一方；两方都改了，取 `updated_at` 较新者，并在 notes 追加一条"同步冲突：<字段> 取了 <某方>，另一方为 <值>"；
  - notes、comments、deps、labels：并集。
- 何时同步：手动 `gofer issue sync`；`auto_sync: true`（默认）时在 `gofer prime` 与每次写命令之后尽力同步一次（2 秒超时，失败只提示、不影响本地写入）。server 侧改动在下一次本地命令时拉回。
- `job run --issue <id>`：开跑时 server 镜像把该 issue 置 in_progress，结束时追加 notes（状态、提交列表、未提交提示），与现在 todo 的联动方式一致；本地在下一次同步时拿到。仓库从未同步过时只给 job 打标签。
- web：Issues 页（按项目/仓库列表、筛选、详情、编辑、评论），工作台会话可链接 issue；编辑写 server 镜像，经同步回到仓库。

## 实施分期（测试先写先提交；每期我远程升级并验收）

| 期 | 内容 | 验收 |
|---|---|---|
| **P1 GIT-01** | 基线/终态比对、嵌套仓库扫描、`uncommitted_files` 字段与事件、`on_uncommitted` 四档、web 徽标 | `TestUncommittedDetectsNewDirtyOnly`、`TestUncommittedNestedRepo`、`TestUncommittedPolicyReviewAndResume`；真机：让 agent 改文件不提交，看到徽标并按 `resume` 续接后提交 |
| **P2 TRK-01 本地核心** | 目录与格式、锁、id、issue/memory 全部 CLI、仓库发现 | `TestTrackerRoundTripStableOrder`、`TestTrackerLockContention`、`TestIssueReadyRespectsDeps`、`TestMemoryCRUD`；无 server 时所有命令可用 |
| **P3 prime + 迁移** | `gofer prime`（含 `--hook-json`）、`init hooks` 接入、`issue migrate --from-bd` 六步 | `TestPrimeSectionsAndCap`、`TestMigrateFromBdFixture`（用本仓 jsonl 的脱敏样本）、`TestMigrateStripsBeadsBlock`；试点仓库迁移后开新会话，看到 gofer 的提交策略 |
| **P4 同步 + 联动 + web** | server 表、sync 三方合并、`job run --issue`、Issues 页 | `TestSyncThreeWayMerge`（含两边改同一字段）、`TestSyncOfflineThenCatchUp`、`TestJobIssueLinkAppendsNotes`；真机：离线建/改 issue，恢复后同步，web 改了再拉回本地 |

P1 与 P2–P4 相互独立，可以并行排；P3 完成即可在试点仓库离线使用，P4 补上共享。

## 风险与限制

- **跨 clone 的 git 合并冲突**：两个 clone 各自改同一 issue 并各自提交，git 层会在该行冲突。容器与主机共用同一目录，实际很少发生；主通道是 server 同步。后续可加 git merge driver。
- **锁在共享挂载上的可靠性**：`O_EXCL` 建文件在 drvfs/9p 上基本可靠，但不是强保证；写入都是整文件原子替换，最坏情况是后写覆盖先写，需要靠同步基线发现。实现时要在容器+主机双侧并发写做一次压测。
- **过渡期两套并存**：IDEV-STD 仍会调用 bd；试点仓库迁移后若 IDEV-STD 继续往 bd 写，两边会分叉。试点应选不走 IDEV-STD 的仓库，或迁移后暂停该仓的 IDEV-STD 流程。
- **GIT-01 的误报**：agent 有意留下的未提交文件（如本地配置）也会被标出；`warn` 为默认档就是为此，项目可在 `.gofer/tracker/config.yaml` 列 `uncommitted_ignore` 路径模式。

## 决策（待批准）

1. gofer 接管 issue + memory，真源是仓库内 `.gofer/tracker/*.jsonl`（提交进 git），server 只是镜像。
2. 默认提交策略 `local-commit`：按功能点本地提交是默认授权，不 push。
3. GIT-01 默认 `warn`，项目可改 `review`/`resume`。
4. 新增顶层命令 `issue`、`memory`、`prime`。
5. 迁移默认 dry-run；`.beads/` 保留为归档不删除。
6. IDEV-STD 适配推迟到本功能可用后。

## 待确认事项

1. 试点仓库选哪个：建议选一个不走 IDEV-STD 流程的仓库；若选本工作空间，迁移后需要先停用 IDEV-STD 的 Beads 跟踪。
2. GIT-01 的默认档是 `warn` 还是直接 `resume`（后者会自动多跑一轮 agent，消耗额度）。

## 结论与人工计划 Gate

批准后按 P1 → P4 派发实施（P1 可与 P2 并行），每期测试先行、我在容器验证并远程升级。

## 参考

- bd（beads）：`bd prime` / `bd memories --json` / `.beads/issues.jsonl`；本仓 bd 记忆 `bd-auto-export-may-not-flush`
- gofer：`docs/design/2026-09-25-rules-injection-and-worker-init-design.md`（规则注入与大小上限）、`docs/design/2026-07-09-plan-orchestration-design.md`（todo 与 job 联动）
