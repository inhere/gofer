<!-- template_id: design; template_version: 1.1.1 -->
# 本地优先的 issue/memory 跟踪（TRK-01）与未提交改动守卫（GIT-01）

> 状态：Approved（文档 identity：Draft 0.3；2026-09-27 用户批准：按建议——试点仓库 hyy-app-dev，GIT-01 默认 `warn`）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.5 | 2026-10-07 | Claude | 按用户意见放宽提交策略文案：「不 push」改为「push 到远端需用户授权」（prime 提交策略与 gofer 托管块同步修改） |
| 0.4 | 2026-10-06 | Claude | X1 批次（5 个工作区从 bd 迁来前的补齐与加固）：issue/memory 命令对齐 bd 日常用法（update 全字段 + `--clear`、comment、reopen、dep rm/ls、ls 过滤排序、`-l/--label` 别名、show 展示关系、memory show 多 key）；`repo migrate --from-bd` 重写为 `internal/bdmigrate`（读实时 bd 库、防分叉、完整切换接入点、备份与校验）；prime 预算改为 issue 行优先；详见文末「X1 实测记录」。 |
| 0.3 | 2026-09-27 | Claude | 用户要求 issue/memory 加 `--tag` 便于搜索：issue 的 `labels` 统一改名 `tags`（P2 刚上线无真实数据，直接改名不留兼容），memory 增加 `tags`；`issue ls`/`memory ls` 支持 `--tag` 过滤与 `-q` 关键字；bd 的 `labels` 导入为 `tags`。随 P3 一起实施 |
| 0.2 | 2026-09-27 | Claude | 按用户意见把仓库级公共命令收进新命令组 `gofer repo`（init / prime / sync / migrate / status）；补 `repo init` 的职责（对应 `bd init`）；`issue`/`memory` 只留条目操作 |
| 0.1 | 2026-09-27 | Claude | 初稿：用户要求 gofer 接管 bd 的 issue + memory（本地优先写 jsonl、连上 server 再同步），并解决"agent 改了一堆文件不提交"；IDEV-STD 适配等本功能可用后再做 |

## 背景与目标

1. **agent 不提交、改动越积越多。** 根因是 bd 往 `CLAUDE.md`/`AGENTS.md` 注入的托管块（`BEGIN BEADS INTEGRATION … profile:minimal`）写着 "Do not commit or push without clear authority"，并声称压过其他指令；gofer 的 `house-rules` 与工作空间 R003 虽写了"按功能点本地提交"，交互会话读到的仍是 bd 这段。该块由 bd 按哈希维护，手改会被覆盖。
2. **bd 实际只用了 issue 和 memory。** 用户确认其余功能（Dolt 远端同步、`refs/dolt/data` 等）都没用过。
3. **目标**：
   - 交付 gofer 自己的 issue + memory，数据**本地优先**：写仓库里的 jsonl，离线可用；连得上 server 时同步上去，供 web 查看/编辑和 job 联动；
   - 会话开场注入由 gofer 掌握，提交策略写成我们自己的口径（默认"按功能点本地提交，不 push"）；
   - 一个与说辞无关的兜底：job 结束时发现"本次新增且未提交"的改动就标出来，按项目策略提醒/转验收/续接一轮去提交。

## 范围与非目标

- 范围：GIT-01 未提交守卫；TRK-01 本地存储与 CLI（issue、memory）、`gofer repo`（init / prime / sync / migrate / status）与 hooks、从 bd 迁移、server 镜像与同步、`job run --issue` 联动、web Issues 页。
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
  - `memories.jsonl`：一行一条 `{key, content, tags, updated_at, by}`；
  - `config.yaml`：`prefix`（id 前缀）、`tracker_id`（init 时生成的 uuid，同步时的仓库身份）、`commit_policy`、`auto_sync`。
- `.gofer/tracker/.local/`（gitignore）：锁文件、同步状态与同步基线快照。
- **jsonl 就是真源**（不是某个数据库的导出），不存在"导出没刷盘"的问题。
- 写入：取锁 → 读全文件 → 改 → 写临时文件 → rename。锁用 `O_EXCL` 建锁文件（写 pid/主机/时间，30 秒过期可抢），因为容器与主机共享同一目录，flock 跨挂载不可靠。
- issue 字段对齐 bd 实际用到的：id、title、type、status（open / in_progress / blocked / closed）、priority（0–4）、description、design、acceptance_criteria、notes（**条目列表** `{at, by, text}`，便于合并）、assignee、owner、tags（bd 的 labels）、parent、deps（`{id, type}`）、comments（条目列表）、created_at/by、updated_at、started_at、closed_at、close_reason。
- id：`<prefix>-<4 位 base36 随机>`，本地查重；子项 `<parent>.<n>`。从 bd 导入的 id 原样保留，提交信息和文档里的引用继续有效。
- 仓库发现：从 cwd 向上找最近的 `.gofer/tracker/`，与 bd 找 `.beads` 相同；`--tracker <dir>` 可显式指定。

### CLI

仓库级的公共动作放新命令组 **`gofer repo`**（管当前仓库的 `.gofer/`：tracker、`RULES.md`、`templates/`、hooks）；`issue`/`memory` 只做条目操作。新增顶层命令共三个：`repo`、`issue`、`memory`。

| 命令 | 说明（对应 bd） |
|---|---|
| `gofer repo init [--prefix P] [--no-hooks] [--no-agents-md]` | 对应 `bd init`，幂等，见下 |
| `gofer repo prime [--hook-json]` | 会话开场注入，对应 `bd prime` |
| `gofer repo sync` | 与 server 同步 issue/memory |
| `gofer repo migrate --from-bd [--apply]` | 从 bd 迁移，默认只打印计划；隐含 `repo init` |
| `gofer repo status` | 自检：tracker 路径与计数、待同步变更与上次同步时间、hooks 是否装好、托管块是否在、提交策略；对应 `bd doctor` 里我们用得到的部分 |
| `gofer issue ready` / `ls [--status --type --tag T… -q 关键字 --all]` / `show <id>` | `bd ready/list/show`；`--tag` 可重复，多个取交集；`-q` 匹配标题与描述 |
| `gofer issue create -t … [--type --priority --parent --dep … --tag a,b]` | `bd create` |
| `gofer issue update <id> [--claim --status --title --append-notes --tag a,b --untag c …]` / `close <id> [--reason]` / `dep add <id> <on>` | `bd update/close/dep` |
| `gofer memory set <key> <content> [--tag a,b]` / `ls [kw] [--tag T…]` / `show <key>` / `rm <key>` | `bd remember/memories/recall/forget`（保留 `remember`/`forget` 别名） |

`gofer repo init` 做的事（每步已完成就跳过，可重复执行）：

1. 建 `.gofer/tracker/`：`config.yaml`（`prefix` 默认取目录名；`tracker_id` 生成 uuid；`commit_policy: local-commit`；`auto_sync: true`）、空的 `issues.jsonl`/`memories.jsonl`；
2. 在 `.gofer/.gitignore` 加 `tracker/.local/`；
3. 在 `AGENTS.md`/`CLAUDE.md` 写入 gofer 托管块（`BEGIN/END GOFER TRACKER` 标记：用 `gofer issue`/`gofer memory`、提交策略一句话、开场会自动注入）；已有 bd 托管块时提示改用 `repo migrate --from-bd`，不自行删除；
4. 装 SessionStart hook 为 `gofer repo prime --hook-json`（复用 `gofer init hooks` 的写法，Claude/Codex 都装）；
5. 连得上 server 时顺带做第一次 `repo sync`，登记仓库。

没有 tracker 的仓库里执行 `gofer issue`/`gofer memory` 会报错并提示先 `gofer repo init`，不自动创建，避免到处散落 `.gofer/tracker/`。`gofer init hooks` 保留，只负责 hooks 这一步。

### `gofer repo prime`（会话开场注入）

输出四段，总量有上限（默认 8 KiB，超出截断并说明）：

1. **提交策略**（来自 `commit_policy`，默认 `local-commit`）："按功能点本地提交是默认授权；不要 push；`.gofer/tracker/*.jsonl` 的变化随功能点一起提交；提交前 `git status` 确认没有夹带无关文件；提交 tracker 前用 `gofer repo status --changed` 核对 issue/memory 相对 HEAD 的变化。"另有 `ask`（提交前问人）与 `none`（不提交）两档供特殊仓库使用。
2. 进行中与已认领的 issue；
3. `ready` 的前 10 条；
4. memory 全量（超限时先按更新时间保留最新的）。

`repo init`（或 `gofer init hooks`）把 SessionStart 装成 `gofer repo prime --hook-json`（Claude），Codex 走其 hooks 等价物。gofer 派出的 job：若 cwd 仓库有 tracker，把第 1、4 段并入规则注入（同一套大小上限与记名称/sha）。

### 从 bd 迁移（`gofer repo migrate --from-bd`）

默认 dry-run，打印将要做的每一步；`--apply` 才执行：

1. 读 `.beads/issues.jsonl`，按上表映射字段（`labels`→`tags`；notes 整段作为一条条目；bd 未知状态映射为 open 并打标签 `bd:<原状态>`），写 `issues.jsonl`；
2. 有 `bd` 可执行时用 `bd memories --json` 导出 memory，否则跳过并提示；
3. 把 `CLAUDE.md`/`AGENTS.md` 里 `BEGIN/END BEADS INTEGRATION` 托管块替换为 gofer 的短块（带 `BEGIN/END GOFER TRACKER` 标记，写明用 `gofer issue`/`gofer memory` 与提交策略）；
4. `.claude/settings.json` 的 `bd prime --hook-json` 换成 `gofer repo prime --hook-json`（Codex hooks 同理）；
5. `core.hooksPath` 指向 `.beads/hooks` 时 unset（失效路径同样处理），并在汇报中列出原值；
6. `.beads/` 原样保留作只读归档，不删除。

迁移可重复执行：已存在的 id 按 `updated_at` 较新者为准，不重复导入。

### server 镜像与同步

- server 新表：`tracker_repos {tracker_id, project_key, rel_path, prefix, last_sync_at}`、`tracker_issues {tracker_id, id, body_json, rev, updated_at}`、`tracker_memories {tracker_id, key, …}`。
- `gofer repo sync`：把本地与"上次同步基线"（`.local/sync-base.jsonl`）的差异推给 server，拉回 server 自上次以来的变更（web 编辑、job 联动、其他机器），**三方合并**后写本地：
  - 标量字段：只有一方改了就取那一方；两方都改了，取 `updated_at` 较新者，并在 notes 追加一条"同步冲突：<字段> 取了 <某方>，另一方为 <值>"；
  - notes、comments、deps、tags：并集。
- 何时同步：手动 `gofer repo sync`；`auto_sync: true`（默认）时在 `gofer repo prime` 与每次写命令之后尽力同步一次（2 秒超时，失败只提示、不影响本地写入）。server 侧改动在下一次本地命令时拉回。
- `job run --issue <id>`：开跑时 server 镜像把该 issue 置 in_progress，结束时追加 notes（状态、提交列表、未提交提示），与现在 todo 的联动方式一致；本地在下一次同步时拿到。仓库从未同步过时只给 job 打标签。
- web：Issues 页（按项目/仓库列表、筛选、详情、编辑、评论），工作台会话可链接 issue；编辑写 server 镜像，经同步回到仓库。

## 实施分期（测试先写先提交；每期我远程升级并验收）

| 期 | 内容 | 验收 |
|---|---|---|
| **P1 GIT-01** | 基线/终态比对、嵌套仓库扫描、`uncommitted_files` 字段与事件、`on_uncommitted` 四档、web 徽标 | `TestUncommittedDetectsNewDirtyOnly`、`TestUncommittedNestedRepo`、`TestUncommittedPolicyReviewAndResume`；真机：让 agent 改文件不提交，看到徽标并按 `resume` 续接后提交 |
| **P2 TRK-01 本地核心** | 目录与格式、锁、id、`repo init`/`repo status`、issue/memory 全部 CLI、仓库发现 | `TestTrackerRoundTripStableOrder`、`TestTrackerLockContention`、`TestIssueReadyRespectsDeps`、`TestMemoryCRUD`；无 server 时所有命令可用 |
| **P3 prime + 迁移** | `gofer repo prime`（含 `--hook-json`）、hooks 接入、`repo migrate --from-bd` 六步 | `TestPrimeSectionsAndCap`、`TestMigrateFromBdFixture`（用本仓 jsonl 的脱敏样本）、`TestMigrateStripsBeadsBlock`；试点仓库迁移后开新会话，看到 gofer 的提交策略 |
| **P4 同步 + 联动 + web** | server 表、`repo sync` 三方合并、`job run --issue`、Issues 页 | `TestSyncThreeWayMerge`（含两边改同一字段）、`TestSyncOfflineThenCatchUp`、`TestJobIssueLinkAppendsNotes`；真机：离线建/改 issue，恢复后同步，web 改了再拉回本地 |

P1 与 P2–P4 相互独立，可以并行排；P3 完成即可在试点仓库离线使用，P4 补上共享。

## 风险与限制

- **跨 clone 的 git 合并冲突**：两个 clone 各自改同一 issue 并各自提交，git 层会在该行冲突。容器与主机共用同一目录，实际很少发生；主通道是 server 同步。后续可加 git merge driver。
- **锁在共享挂载上的可靠性**：`O_EXCL` 建文件在 drvfs/9p 上基本可靠，但不是强保证；写入都是整文件原子替换，最坏情况是后写覆盖先写，需要靠同步基线发现。实现时要在容器+主机双侧并发写做一次压测。
- **过渡期两套并存**：IDEV-STD 仍会调用 bd；试点仓库迁移后若 IDEV-STD 继续往 bd 写，两边会分叉。试点应选不走 IDEV-STD 的仓库，或迁移后暂停该仓的 IDEV-STD 流程。
- **GIT-01 的误报**：agent 有意留下的未提交文件（如本地配置）也会被标出；`warn` 为默认档就是为此，项目可在 `.gofer/tracker/config.yaml` 列 `uncommitted_ignore` 路径模式。

## 决策（已批准 2026-09-27）

1. gofer 接管 issue + memory，真源是仓库内 `.gofer/tracker/*.jsonl`（提交进 git），server 只是镜像。
2. 默认提交策略 `local-commit`：按功能点本地提交是默认授权，不 push。
3. GIT-01 默认 `warn`，项目可改 `review`/`resume`。
4. 新增顶层命令 `repo`、`issue`、`memory`；仓库级公共动作（init / prime / sync / migrate / status）归 `gofer repo`。
5. 迁移默认 dry-run；`.beads/` 保留为归档不删除。
6. IDEV-STD 适配推迟到本功能可用后。
7. 试点仓库 `hyy-app-dev`（74 个 issue，唯一不走 IDEV-STD 的 bd 仓库）。

## 待确认事项

无（原两项已按建议确认：试点 hyy-app-dev；GIT-01 默认 `warn`）。

## 结论与人工计划 Gate

批准后按 P1 → P4 派发实施（P1 可与 P2 并行），每期测试先行、我在容器验证并远程升级。

## 参考

- bd（beads）：`bd prime` / `bd memories --json` / `.beads/issues.jsonl`；本仓 bd 记忆 `bd-auto-export-may-not-flush`
- gofer：`docs/design/2026-09-25-rules-injection-and-worker-init-design.md`（规则注入与大小上限）、`docs/design/2026-07-09-plan-orchestration-design.md`（todo 与 job 联动）

## P1 实测记录（2026-09-27）

本节只记录 GIT-01 P1 的本地实现与验证结果，不改变上面的 Approved 0.2 决策。TRK-01 尚未实施。实际项目配置落在现有 `projects.<key>`，`uncommitted_ignore` 不依赖尚未建立的 `.gofer/tracker/config.yaml`。

- 本机 agent job 在开始/结束时比较 Git porcelain v2 脏路径与 `hash-object` 内容；被忽略文件、未变化的基线脏文件和已提交文件不计。嵌套仓库深度 ≤2，worktree 使用自身目录；结果保留完整 `uncommitted_count`、排序后的前 200 个 `uncommitted_files`，事件 `job.uncommitted` 带前 20 个路径。`off|warn|review|resume` 默认 `warn`，`uncommitted_ignore` 支持含 `**` 的路径 glob。
- `review` 在 `finish` 原有状态锁内转 `needs_review`；`resume` 复用同会话和 `auto_resume_max` 预算。无 session 或不能续接转 review；自动续接后仍脏转 review。worker 端只检测并经 v12 的 additive 结果帧回报，hub 负责状态决策；自动续接轮次随 dispatch 透传，避免 worker 把上轮遗留脏文件当成本轮无变化而漏报。
- `TestUncommittedDetectsNewDirtyOnly`、`TestUncommittedNestedRepo`、`TestUncommittedPolicyReviewAndResume`、`TestUncommittedWorkerFrameRoundTrip` 先以缺实现的编译错误呈红，再随实现转绿；另有真实 `job.Service` 的 review/resume/worker-only/落库测试。Web 的 job 详情、Board 行、工作台会话头提供可展开文件列表的徽标，事件中文标签为「未提交改动」。

| 本地验证 | 结果与边界 |
|---|---|
| 全仓 `gofmt -l` | exit 0，输出为空（`gofmt_count=0`） |
| Windows/Linux `go build ./cmd/gofer` | 均 exit 0，产物在仓库 `tmp/_cache/main/build/`；Go 同时提示不可写的外部模块 stat cache，不影响本次退出码 |
| `go vet ./...` | exit 0，输出为空 |
| 指定五包 `go test ... -count=1` | 隔离 `TMP`、`GOFER_CONFIG_DIR` 并以 `GIT_CEILING_DIRECTORIES` 保持非 Git 夹具语义后，`internal/job`、`internal/worker`、`internal/wsproto`、`internal/config`、`internal/httpapi` 全部 `ok`，exit 0 |
| Web 测试、typecheck、build | 直接调用已安装工具入口：Vitest 3 文件/14 测试通过，`vue-tsc --noEmit` exit 0，Vite 构建 236 modules、exit 0。`pnpm` 包装命令在本机 Linux 依赖目录上运行 esbuild postinstall 时 EPERM；因此未得到字面 `pnpm test && pnpm typecheck && pnpm build` 的成功记录 |

未操作 live gofer server/worker、真实配置目录或远端设备；没有部署、push 或真机 agent/Web 验收。G032 本期未新增兼容分支；协议 v12 只增加可选字段，旧字段语义和最低注册版本未改变。

## P2 实测记录（2026-09-27）

本节记录 TRK-01 P2 的本地实现与验证，不改变 Approved 0.2 决策。计划候选为 `2781034`（Draft 0.1），执行方式 `DIRECT_CONTINUOUS`；IDEV-STD 0.22.1 preflight 经记录项修正后 `exit 0 / {"ok":true,"errors":[]}`。T1 七项固定测试先因 tracker 类型与 `repo` 命令缺失呈红，独立提交 `f8d9866`；核心存储、repo、issue/memory 分别提交 `982c86d`、`e33e798`、`afbd11d`。

- `.gofer/tracker/issues.jsonl` 与 `memories.jsonl` 是本地真源，按 id/key 排序并固定 JSON 字段序；写命令持 `O_EXCL` 文件锁，保存 pid/host/时间，30 秒过期可抢，同目录临时文件替换。Windows 并发测试初次出现锁文件短暂共享占用，追加有界重试后 `TestTrackerLockContention -count=10` 得到 `ok`，修正提交 `37810bb`；Windows 目标文件短暂占用时也有界重试 rename，保留旧文件直到替换成功（`b990de4`）。跨容器与主机同时写的现场压测尚未做。
- `repo init|status`、`issue ready|ls|show|create|update|close|dep add`、`memory set|ls|show|rm` 和 memory 别名均为纯本地命令。`--tracker` 可覆盖向上发现，`--json` 供条目命令及 status 使用。`repo init` 幂等；BEADS 块保留且仅提示迁移。`repo status` 的 hooks/sync 明示“未实现（P3/P4）”；prime、migrate、sync、真实 hooks 和 server 连接未实现。
- 随机四位 base36 ID 的冲突重试和 `<parent>.<n>` 序号通过测试；`ready` 仅列 open 且其 `blocks` 依赖已 closed 的 issue，按 priority、created_at 排序。`--claim` 置 in_progress/assignee/started_at，`--append-notes` 添加结构化条目。额外 CLI 测试验证这些操作和无 tracker 提示，提交 `9a6eec7`；ID 确定性冲突测试提交 `fd685c5`。

| 本地验证 | 原始结果与边界 |
|---|---|
| 全仓 `gofmt -l`（901 个 Go 文件） | `gofmt_exit=0`，无文件名输出 |
| Windows/Linux `go build ./cmd/gofer` | `build_windows_exit=0`、`build_linux_exit=0`；产物在仓库 `tmp/_cache/main/build/`。Go 对外部模块 stat cache 报 `Access is denied` 提示，但命令退出码均为 0 |
| `go vet ./...` | `vet_exit=0`，无诊断行 |
| `go test ./internal/tracker/ ./internal/commands/ -count=1` | 初跑 Windows 锁共享冲突导致 tracker FAIL；最终复跑：`ok github.com/inhere/gofer/internal/tracker 1.998s`、`ok github.com/inhere/gofer/internal/commands 21.536s`，`test_exit=0` |
| 二进制临时目录 smoke | 每条命令显式 `-c <临时配置>`，依次完成 init→两条 issue create→claim/notes→dep add→close→ready→memory set/ls/rm→status，各步 `exit=0`；最终 `issues.open=1`、`issues.closed=1`、`memories=0`，hooks/sync 显示未实现 |

本期没有改动 live gofer server/worker、真实配置、server 数据、bd 数据或远端；没有 push/部署/迁移。G032 本期没有新增旧路径兼容分支，也没有删除旧兼容路径。P3/P4 及主机+容器共享目录的双侧压测仍属后续范围。

## 架构（P2 实测）

本节仅补记本地实现落点，不改变 Approved 0.2 的目标架构。`internal/commands` 注册 `repo`、`issue`、`memory` 三组 gcli 入口，参数绑定和输出留在入口层；`internal/tracker` 拥有仓库发现、模型、JSONL、锁、ID、ready 和条目操作。依赖方向为命令入口 → tracker → Go 文件系统/JSON/YAML 能力；P2 没有 server 或 job 数据通路。

## 关键流程（P2 实测）

本节仅补记本地命令的实测流程。`repo init` 在当前仓库建 tracker 文件并写托管块；随后 issue/memory 写命令取得 `.local/lock`，读取对应 JSONL，执行条目变更，按稳定顺序将完整快照写入同目录临时文件并替换原文件，最后释放锁。`issue ready` 读取 issue 快照并按 `blocks` 的关闭状态过滤、排序。CLI smoke 在无 server 的临时目录中走通该流程；SessionStart 注入、迁移与同步仍待 P3/P4。

## P4 实测记录（2026-09-29）

本轮完成 tracker mirror 的基础 HTTP sync、三方合并、memory server tombstone、job `--issue` 联动参数和 Web Issues 基础页面。临时 httptest server、临时 SQLite jobstore 与临时 tracker 的固定测试通过；仓库 JSONL 仍为本地真源，server 保存镜像和 memory tombstone。`repo sync` 默认复用 CLI client 的配置/env 地址和 token，`--server` 仅覆盖地址；auto sync 失败不阻塞本地写入。完整 cursor 增量、未归属仓库登记和 Web 全量验收仍需后续质量波次。

收尾波次 smoke/质量记录：

```text
go test ./internal/httpapi/ -run 'TestSyncThreeWayMerge|TestSyncOfflineThenCatchUp|TestSyncMemoryTombstone|TestJobIssueLinkAppendsNotes' -count=1
ok github.com/inhere/gofer/internal/httpapi 1.025s

go test ./internal/tracker/ -run TestSyncSecondRequestContainsOnlyDelta -count=1
ok github.com/inhere/gofer/internal/tracker 0.056s

node_modules/.bin/vue-tsc --noEmit
exit=0
```

G032 清单：本轮没有新增未标记兼容分支；tracker mirror 字段、cursor、tombstone 和 Web API 均为 additive P4 路径，没有保留无调用方的旧兼容入口。

job issue 联动收尾修正：联动已从 HTTP handler 的异步等待协程下沉到 job Service 生命周期钩子，与 todo 回写同序。开跑钩子在 job 真正进入 running 后执行，终态 note 在终态快照持久化前写入 mirror；因此 MCP、本地后端、todo 派发、resume、retry/fallback 都复用同一条生命周期路径，提交被拒时不会改变 issue。`TestJobIssueLinkAppendsNotes -count=20` 与拒绝提交测试均通过。

真实进程 smoke（2026-09-29，临时 config、临时 repo、随机临时目录；未触碰 live server/worker）：

```text
go build -o tmp/gofer-smoke2.exe ./cmd/gofer
gofer serve -c <temp>/config.yaml
server.ready ... addr=0.0.0.0:8765 ... token auth enabled
gofer repo sync -c <temp>/config.yaml   # 不传 --server，GOFER_SERVER_ADDR/TOKEN 环境解析
sync: tracker_id=456f69f7-c04b-4619-88f6-c875023fa187 server=http://127.0.0.1:8765
curl /v1/tracker/repos
{"repos":[{"tracker_id":"456f69f7-c04b-4619-88f6-c875023fa187","project_key":"","rel_path":"","prefix":"repo","last_sync_at":0,"sync_summary":""}]}
```

本次真实进程验证了 serve 编排后的 `/v1/tracker/*` 路由可用、mirror repo 可登记，以及 repo sync 不传 `--server` 时可通过环境中的 server 地址和 token 连接。完整 HTTP 编辑、再次 sync 拉回和无变化二次 sync 仍由固定 httptest 测试覆盖。

P4 server mirror 返工记录：镜像表增加 tracker 级全局 `changed_seq`，cursor 按该序号拉取，逐条 `rev` 只负责乐观并发；Web issue/memory 编辑支持部分字段更新和 `expected_rev`，冲突返回 409，server 为 Web 编辑盖 UTC `updated_at/updated_by`。评论契约统一为 `{"text":"..."}`，Web client、handler 和测试一致。sync 推送保留客户端 body 内的 `updated_at` 作为字段修改时间，server 表列时间用于接收记录；Web 编辑才由 server 盖字段时间。新增真实进程脚本：`scripts/smoke/tracker/run-smoke.sh`；Windows 主机不执行 Bash smoke，容器/Linux 运行。

memory 拉回修正：三方合并先按 `local==base` 取 remote，再单独应用 tombstone 胜负；不得用 tombstone 辅助合并结果覆盖普通字段合并结果。游标只在 issues/memories/base 全部成功写盘后写入 sync-status；失败路径保留旧 cursor。新增 `TestSyncPullsWebMemoryEdit` 覆盖真实 HTTP Web memory 编辑后下一次 sync 拉回。


## X1 实测记录（2026-10-06）

### 命令补齐（对齐 bd 日常用法）

- `issue update` 增加 `--type --priority/-p --description/-d --design --acceptance --assignee/-a --owner --parent`；清空字段用 `--clear description,design,acceptance,assignee,owner,parent,close-reason`（gcli 无法区分"没传"与"传了空串"，故用独立开关；priority 0 是合法值，用指针区分）。`--parent` 校验存在、非自身、非自己的后代；状态离开 closed 时清 `closed_at/close_reason`，`--status closed` 补 `closed_at`。
- 新增 `issue comment <id> text...`、`issue reopen <id> [--reason]`（reason 记为评论）、`issue dep rm|ls`、`dep add --type`（blocks 成环拒绝；parent-child 只能用 `--parent`）；`issue show` 支持多 id，显示 notes / comments / parent / children / blocked-by / blocks / depends-on（`--json` 额外带 `relations`）。
- `issue ls` 增加 `--assignee --priority --sort id|priority|created|updated -r -n`；`-l/--label` 作为 `--tag` 的别名（create / update / ls 一致；这是对 0.3 "不留 labels 兼容" 的有意回退——bd 用户与多 label 工作区的习惯写法）。`issue create` 标题可作位置参数，并补 `--design --acceptance -a`。
- `memory ls <kw>` 大小写不敏感地匹配 key + 内容；`memory show` 接受多个 key（缺的 key 报错，已找到的仍打印；单 key JSON 仍是对象，多 key 是数组），`recall` / `memories` 别名。
- server 镜像：Issue 新增 `external_ref` / `spec_id`（bd 迁移带过来），同步三方合并补 `parent / external_ref / spec_id / closed_at`；tags 与 deps 由"并集"改为三方集合合并，修复 `--untag` / `dep rm` 被另一端复活；comments 仍取并集。

### 迁移加固（`internal/bdmigrate`，替换 `tracker.MigrateFromBD`）

真实库演练暴露的问题与处理（演练只在副本上做，见下）：

| 发现 | 处理 |
|---|---|
| `.beads/issues.jsonl` 落后于 Dolt 库：5 个库里 3 个落后（26 / 3 / 1 条），memory 根本不在 jsonl 里（`export-state.json` 的 `memories: 0` 不可信） | 优先 `bd --readonly export --include-memories`，jsonl 仅作后备，dry-run 报告差异 |
| bd 库 schema 落后于二进制时 `--readonly` 直接拒绝（`schema version mismatch`） | 识别该错误后以 `BD_IGNORE_SCHEMA_SKEW=1` 重试并记入备注；全程不执行任何 bd 写命令 |
| bd 的 parent-child 关系只在 `dependencies` 里（旧代码只靠点号 id 推断，hyy 有 13 个无点号子项），且可能多父 | 取 parent-child 依赖为 `parent`（多父优先点号 id 指向的那个，其余作为 dep 保留） |
| 导入后新建 issue 的 id 前缀会变成目录名（实际 id 前缀为 `h-aii` / `wtools` / `my-tools` 等） | 按 id 推断主前缀写入 tracker 配置 |
| `AGENTS.md` 里还有第二个托管块 `BEGIN BEADS CODEX SETUP`（旧代码不处理） | 两种块都按字节精确删除，块外逐字不变，gofer 块原地替换第一个块 |
| codex 的 hook 是 `bd codex-hook <事件>`（4 个事件），claude 有 `bd prime`、`bd prime --hook-json`、`bd prime --no-memories --hook-json`；旧实现用 `map` 重序列化会打乱键序 | 新增保序 JSON 编辑：SessionStart 的 bd 项原地换成 `gofer repo prime --hook-json --agent <名>`，其它事件的 bd 项删除（gofer prime 输出是 SessionStart 形态，PreCompact 不适用；SessionStart 压缩后会再触发），其余 hook 与缩进原样 |
| bd 空跑也会改写 dolt 的 journal/manifest，且生成 `.beads.gate.lock` | 防分叉检查先于任何 bd 调用，且不把 dolt 目录的写入当活动信号；自己生成的空 gate lock 用后清除 |
| `hyy-ai-inspect/tools` 这类嵌套目录里 `git config core.hooksPath` 读到的是外层仓库的值 | 仅当目录就是 git 顶层才处理 `core.hooksPath`；并检查 `.beads/hooks` 是否只有 bd 自己的脚本、`.git/hooks` 是否有 bd shim |
| 73 条 memory 在 prime 里把 in-progress / ready 挤出 8KiB 预算 | prime 预算改为 issue 行优先、memory 取剩余；`memory_summary_limit` 截掉的条数提示"另有 N 条"；迁移对 >20 条 memory 的仓库写 `memory_summary_limit: 15`；固定头部加一行命令提示 |

防分叉实测：bd 运行时 `embeddeddolt.gate.lock` 与 `noms/LOCK` 的 flock 被持有、`/proc` 可见 bd 进程，dry-run 能在 bd 运行中检出（也会在 `--apply` 时拒绝）；`--force` 覆盖。迁移不动 `.beads/`（校验 `issues.jsonl` 的 sha256 前后一致），改写前备份到 `.gofer/tracker/.local/migrate-backup/<时间戳>/`。

### 真实数据演练结果（只读副本）

`.beads/` 与 CLAUDE.md / AGENTS.md / `.claude` / `.codex` 复制到 `tmp/mig-drill/<ws>/`（gitignored），副本里 `git init` 后 dry-run 与 `--apply`；独立脚本逐条核对 id 集合、全部标量字段、labels↔tags、notes、comments（作者/文本/时间）、deps（含 parent-child↔parent）与 memory 内容，均无差异（仅有意不带走 claim lease/heartbeat 与依赖边的 created_at/by）。

| 工作区 | issue 前→后 | memory 前→后 | jsonl 落后于库 |
|---|---|---|---|
| ai-agent-dev | 37 → 37 | 2 → 2 | 是（11 vs 37） |
| my-tools-dev | 40 → 40 | 3 → 3 | 否 |
| hyy-ai-inspect | 696 → 696 | 73 → 73 | 是（693 vs 696） |
| zy-bsly-sf-dev | 387 → 387 | 40 → 40 | 否 |
| work-tools-dev | 962 → 962 | 59 → 59 | 是（961 vs 962） |
| hyy-ai-inspect/tools（.beads 无 jsonl） | 259 → 259 | 63 → 63 | 无 jsonl，只能走 bd export |

### prime 对比（bd prime vs gofer repo prime）

bd prime 是静态工作流说明（规则、命令清单、同步与收尾协议）加记忆（未覆盖时全文，hyy 曾 ~100KB，靠 `.beads/PRIME.md` 覆盖为"按需召回"）；gofer prime 是 ≤8KiB 的动态上下文：提交策略、work 汇报提示、命令提示、进行中/已认领 issue（≤10）、ready 前 10、memory 摘要（`memory_summary_limit`，其余提示 `gofer memory ls <关键字>`）、server 全局/项目记忆与 plan 交接（连得上时）。静态的"全部用 gofer issue、不另建 markdown TODO、命令速查"移到 AGENTS.md / CLAUDE.md 的 gofer 托管块；dolt 同步 / bd 收尾协议不再适用（提交策略由 `commit_policy` 一句话表达）。
