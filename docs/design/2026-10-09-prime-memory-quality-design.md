# 新会话上下文质量：prime 与 memory 改进

> 状态：已确认（2026-10-09，§5 全部按默认），实施中（P1 见 §4.1，P1b 见 §4.2，P2 见 §4.3，P3 见 §4.4 已完成）。
> 目标：新会话读完 `gofer repo prime` 就能走上正轨——看到的是**准确、当前、有重点**的信息，而不是一堆过期交接。

## 0. 现状与问题（2026-10-09 实测）

gofer 仓库清理前的 prime 输出正好 8KB，被截断：

| 问题 | 证据 |
|---|---|
| 过期内容淹没有效内容 | 64 条记忆中 44 条是 2026-06~07 的交接 / 进度 / 部署状态（`gofer-deploy-state-260627`、`dab-*` 等），内容已完成或错误（旧路径 `tools/gofer`、旧代号 DAB）；真正长期有效的规则只有 2~3 条 |
| 排序失效 | bd → gofer tracker 迁移把所有 `updated_at` 改成同一天，prime 按「更新时间」排序等于按 key 字母序，`dab-*` 排最前；8KB 截断后有效规则反而看不到 |
| 摘要无信息量 | 非 `prime` 标签的记忆只显示第一行，而第一行常是「【★gofer 会话交接·恢复入口·更新25】」这类标题 |
| 「进行中」不可信 | `in_progress` 或「有指派人」都算进行中：认领于 07-18 的 issue 一直挂着；改回 open 后因仍有指派人继续出现在「进行中/已认领」 |
| 缺「现状」 | 当前版本、各节点版本、进行中 plan 与下一步、最近 tag / 提交——新会话最需要的信息只能靠人写进记忆，因此最先过期 |
| 没有生命周期 | 记忆不区分长期规则与一次性交接，写入后永久存在，无人清理 |

第一步已手工清理（63 条归档到工作区 `local/`，整理成 4 条规则），本设计解决「以后不再变成这样」。

## 1. 原则

**核心：当前重点 + 索引。** 开场只回答两件事——「现在该做什么」（当前重点，自动生成、永远新鲜）和「还有什么可查」（索引，一行一条、指引方向、避免遗忘）。规则全文是例外：少而必须。

1. **能自动生成的不写进记忆**：版本、节点、plan 进度、最近提交每次现取。
2. **记忆分层**：规则（长期、全文、优先）≠ 笔记（摘要）≠ 交接（短命）。
3. **宁缺毋滥**：prime 按段落分预算，宁可少列也不让过期内容挤掉规则。
4. **过期要可见**：每条带年龄，可疑的标出来，清理由管家提议、人确认。

## 2. 方案

### 2.1 记忆类型 `kind`

`Memory` 加字段 `kind`：

| kind | 用途 | prime 中 | 生命周期 |
|---|---|---|---|
| `rule` | 长期有效的约定 / 流程 / 环境事实 | 全文，最先显示 | 永久，靠「过期检测」提示复核 |
| `note`（默认） | 一般经验、背景 | 摘要一行 + 年龄 | 永久，90 天未更新标「久未更新」 |
| `handoff` | 会话交接、阶段进度 | 只列未过期的最新 3 条，摘要 | `--ttl` 默认 14 天，过期后 prime 不再显示，`memory ls` 标「已过期」 |

- CLI：`gofer memory set <key> "…" [--kind rule|note|handoff] [--ttl 14d]`；`memory ls` 显示 kind、年龄、过期标记；`memory ls --kind rule`。
- 兼容：已有的 `prime` 标签视为 `rule`（G032：读取时映射，打移除标记）；服务端全局 / 项目记忆同样加 `kind`。
- **交接首选 plan 交接说明**（已有「进行中 plan 的交接说明」段），`handoff` 记忆用于没有 plan 的零散交接。

### 2.2 两段式：摘要 + 正文

每条记忆分两段：

| 字段 | 要求 | 用在哪 |
|---|---|---|
| `summary` | 一句话，≤80 字，回答「这条讲什么、什么时候该看」 | prime 索引、场景命中提示、`memory ls` |
| `content` | 完整正文，不限长 | `memory show`、规则全文、场景命中后注入 |

- 写入：`gofer memory set <key> "<正文>" --summary "<一句话>" [--tags …] [--kind …] [--when …]`。`rule` 与 `note` 正文 > 200 字时 summary 必填（CLI 报错并给出建议的首句）；`handoff` 可省。
- 老记忆没有 summary：取「第一个非标题、非空的句子」（跳过 `#`、`【…】` 开头的行与纯链接行）作为临时摘要，`memory doctor` 列出缺 summary 的条目，由管家补写建议。

### 2.3 prime：重要的全文，其余只给索引

开场只给两样东西：**最重要的规则全文**，以及**其他记忆的索引**（key · 摘要 · tags · 年龄），按 tag 分组：

```
## 记忆索引（23 条，按需 `gofer memory show <key>`）
[release] gofer-release-flow（规则，见上）
[tunnel]  tunnel-forward-tips · 远程 forward 停止与排障要点 · 12 天前
[web]     web-build-gotchas · web/dist 是 live 目录，先拷 assets 再拷 index · 3 天前
[其他]    …
```

- 哪些规则给全文：`kind=rule` 且（没有 `when`，或 `when` 命中当前场景，见 §2.9）。其余 rule 也只进索引并标「规则」。
- 索引是「让 agent 知道有什么」，配合 §2.9 的自动命中，「不遗漏」不靠 agent 自己记得去查。

### 2.3.1 分段预算

总上限仍 8KB，各段有自己的预算，超出的段内截断并写明「另有 N 条」，**不再整体从尾部截断**：

| 顺序 | 段 | 预算 | 内容 |
|---|---|---|---|
| 1 | 提交策略 + 命令提示 | 固定 | 现状不变 |
| 2 | **当前重点（自动）** | ≤ 600B | §2.4 |
| 3 | **规则** | ≤ 3KB | `kind=rule` 全文，按 key 稳定排序；超预算时列 key 并提示「规则过长，请精简」 |
| 4 | 进行中 issue | ≤ 600B | 只算 `in_progress`；>14 天无更新标「认领 N 天无更新」 |
| 5 | ready 前 N | ≤ 800B | 现状不变，加年龄 |
| 6 | 笔记 | ≤ 1.5KB | 按真实更新时间倒序，摘要 + 「N 天前」 |
| 7 | 交接 | ≤ 600B | 未过期 handoff 最新 3 条 |
| 8 | 全局 / 项目记忆 | 余量 | 同样按 kind 规则显示 |
| 9 | 进行中 plan 交接说明 | 余量 | 现状不变 |

### 2.4 「当前重点」段（自动生成，best effort，总耗时 ≤ 2s）

放在 prime 最前面，全部现取、不靠人写：

- **在做的**：`in_progress` 且 14 天内有更新的 issue；本项目 open plan 的进度与**下一个未完成 todo**；最新一条未过期 `handoff` 的摘要。
- **刚解锁的**：最近 3 天关闭的 issue 让哪些 issue 变成 ready（借鉴 bd `close --suggest-next`）。
- **未收尾的**：工作树未提交改动数、tracker 未提交改动、领先远端的提交数。
- **环境版本**：见下例（服务 / worker 版本、最近 tag）。

```
## 当前重点（自动，2026-10-09 19:50）
- 仓库：main 8d41dde5，最近 tag v0.128.2（之后 2 个提交），tracker 未提交改动 0
- 服务：server 0.128.2；worker：w-docker-claude 0.128.2 · w-kzl-desktop 0.128.2 · w-mac-win10 离线
- plan：plan-…3fc4ecd5「N3 收尾…」7/8 完成，下一步：…
```

- 仓库信息本地读 git；服务与 worker 从 `/v1/meta`、`/v1/workers` 取（沿用 prime 现有 250ms 客户端超时，失败就省略该行）；plan 取本项目 open plan 的进度和第一个未完成 todo。
- 配置 `prime.focus: true|false`（默认开），仓库可关。

### 2.5 过期检测 `gofer memory doctor`

逐条检查并给出原因，prime 中在该条后加「⚠ 可能过期」：

- handoff 已过 TTL；note 90 天未更新；
- 内容引用的**路径**在仓库 / 工作区都不存在（识别反引号或空白分隔、含 `/` 的路径样式 token）；
- 引用的**提交 hash** 不在 git 中，或引用的版本号落后当前最近 tag 两个以上 minor（只对写明「当前 / 现在 / 部署」语义的行判断，避免误报历史记录）；
- 与另一条记忆高度重复（标题 / key 前缀相同且内容相似度高）。

`doctor --json` 供管家使用；`--fix` 不提供，清理一律走 §2.6 由人确认。

### 2.6 管家提议清理

每日巡检读 `memory doctor --json`，对可疑记忆生成「今天」页的建议卡（新 kind `memory`）：「建议归档 / 合并到 X / 改为 rule」，人点「采纳」才执行。新增 `gofer memory archive <key>`：移入 `.gofer/tracker/memories-archive.jsonl`（同步、可 `memory ls --archived` 搜索、不进 prime），比直接删除更安全。

### 2.7 修正「进行中」与时间戳

- 「进行中/已认领」改为只看 `status=in_progress`；open 但有指派人的显示在 ready 里并标「@指派人」。`issue update --status open` 时清除指派人（可 `--keep-assignee`）。
- 所有迁移（bd → tracker、tracker id、未来的格式迁移）保留原 `updated_at`；`Memory` 增 `created_at`。

### 2.8 写入引导

托管块与 prime 命令提示加一句：「长期约定用 `--kind rule`（写现状不写进度）；阶段进度写 plan 交接说明或 `--kind handoff`」。

### 2.9 按场景自动出现（`when` 触发器）

特定场景才有用的记忆（发版流程、隧道排障、某个子目录的坑），平时只在索引里占一行，**命中场景时再把全文送到 agent 面前**：

```yaml
when:
  keywords: [发版, release, 升级 server, upgrade]   # 用户提问里出现（大小写不敏感）
  paths: [web/**, internal/tunnel/**]                # 会话 cwd 或本轮涉及的文件在其下
  commands: ["gofer worker upgrade", "git push"]     # agent 将要执行的命令前缀（P5）
```

- **开场**：会话 cwd 命中 `paths` 的记忆，摘要提到索引最前；`rule` 直接全文。
- **用户提问时**：gofer hook 在 `UserPromptSubmit` 已能注入额外上下文（现用于补发 job 完成通知，claude / codex / generic 都支持）。prompt 命中 `keywords` 的记忆，把全文作为附加上下文注入，前缀「[gofer 记忆 · 因“发版”命中]」。
- **执行命令前（P5，可选）**：PreToolUse 命中 `commands` 时注入，适合「执行前提醒」类记忆。
- **防干扰**：同一会话里每条记忆最多注入一次（按会话 id 记在 hook 本地状态）；单次注入总量 ≤ 2KB，超出只给摘要 + `memory show` 提示；只匹配人工输入的 prompt（injected / harness 生成的不算）。
- **好写**：`memory set --when-keywords 发版,release --when-paths 'web/**'`；管家整理时可以为常被手动查的记忆建议补 `when`。

### 2.10 借鉴 bd（beads）的几处设计

| bd 的做法 | 借鉴到 gofer |
|---|---|
| 语义「记忆衰减」：旧的已关闭条目压缩成摘要、原文存档，`bd restore` 可还原 | 记忆 `archive` 保留 `summary` 进**归档索引**（默认不进 prime，`memory ls --archived` 可搜），`memory restore <key>` 还原；90 天未更新的 note 可由管家建议「降为归档」 |
| 短命条目（ephemeral / wisp，TTL 后清理）+ `bd promote` 转为永久 | `handoff` 过期即退出 prime；`memory promote <key> --kind rule|note` 把一条交接里沉淀出的经验转成长期记忆（管家巡检时可建议） |
| `discovered-from` 依赖：干活中发现的新问题挂回来源 | 记忆加 `source`（来源 issue / plan / job / 会话），`memory show` 显示来源，便于判断是否仍适用；`issue create --from <id>` 记录「从哪个 issue 发现」 |
| `bd stale`：列出久未更新的 in_progress / open | 「进行中」超 14 天标停滞（§2.7）；`issue ls --stale [--days 30]` |
| `bd doctor` + 可按项静默的警告 | `memory doctor` 的检查项可在 config 里按 slug 静默（如 `prime.doctor.suppress.path-missing`），减少已知误报 |
| 记忆按作用域分：项目事实进 tracker，个人偏好留在 agent 自己的记忆里 | 明确分工：**项目事实只写 gofer memory**（所有 agent 共享、可同步）；agent 自带记忆（如 Claude 的 auto-memory）只放个人偏好与指向 gofer memory 的指针，避免同一事实两处维护、各自过期 |

## 3. 改动面

| 层 | 内容 |
|---|---|
| tracker 本地 | `Memory` 加 `kind/summary/when/ttl/created_at`；`memories-archive.jsonl`；prime 规则全文 + 索引、分段预算；doctor |
| hook | `UserPromptSubmit` 关键词命中注入全文（每会话每条一次）；P5 再做 PreToolUse 命令命中 |
| 同步 / 服务端 | 记忆 body 透传新字段（服务端按 body 存，无需改表）；全局 / 项目记忆加 kind |
| CLI | `memory set --kind/--ttl/--summary`、`memory ls` 列、`memory doctor`、`memory archive`、`issue update --status open` 清指派人 |
| 管家 | 巡检读 doctor，写 `memory` 建议卡（复用 T4 的建议 / 采纳机制） |
| web | 记忆列表显示 kind / 年龄 / 可疑标记；「今天」页 memory 建议卡 |
| skill | gofer-usage 记忆与 prime 章节 |

## 4. 分步

| 步 | 内容 |
|---|---|
| P1 ✅ | kind / summary / when / ttl 字段与 CLI；prime 规则全文 + 分组索引、分段预算、年龄；「进行中」只看 in_progress；迁移保留时间戳 |
| P1b ✅ | hook：`UserPromptSubmit` 关键词命中注入 + cwd 路径命中排前 |
| P2 ✅ | 「当前重点」段（含刚解锁、未收尾） |
| P3 ✅ | `memory doctor` + 「⚠ 可能过期」标记 + `memory archive`（含 §2.10：restore / promote / source / `issue ls --stale` / `issue create --from`；见 §4.2） |
| P4 | 管家清理建议卡（含补 summary / when 建议）+ web 显示 |
| P5 ✅ | PreToolUse 命令命中注入 |

P1 + P1b 价值最大，可单独发版；P2 / P3 可并行。实施时建 gofer plan，每步一个 todo。

### 4.1 P1 实施记录（2026-10-09，已完成）

- **数据模型**：`tracker.MemoryMeta`（`kind / summary / when{keywords,paths,commands} / expires_at / source / created_at`）以内嵌方式追加在 `Memory` 原字段之后，全部 omitempty，旧 jsonl 字节不变。TTL 不单独存储：写入时换算成绝对的 `expires_at`；没有 `expires_at` 的老 handoff 按 `updated_at + 14d` 判定。`source` 已入 schema（P3 无需改结构）。
- **同步**：server 按 body 透传，无需改表；三方合并逐字段覆盖新字段（`when` 整体比较）。
- **全局 / 项目记忆**：`scoped_memories` additive 加列 `meta_json`；只改正文 / 标签的写入（web、MCP）保留这些字段；HTTP `POST/PUT /v1/memories` 接受可选 `kind/summary/when/expires_at/source`（缺省保持）。
- **兼容**：`prime` 标签读取时映射为 `kind=rule`，代码带 `DEPRECATED(v0.129): remove in v0.132`；`agent:<name>` 全文显示保留。
- **CLI**：`memory set --summary --kind --ttl --when-keywords --when-paths --when-commands --source --tags`（`--tag` 保留）；更新时未传字段保持，`-` 清空；`memory ls --kind`；`memory show` 显示全部字段。`issue update --status open` 清指派人（`--keep-assignee`）。
- **prime**：按 §2.3.1 分段预算（现状段暂缺，`PrimeOptions.Focus` 预留在提交策略之后）；交接单独成段（§2.3.1 第 7 段），未过期最新 3 条。
- **迁移时间戳**：bd 迁移保留导出记录自带的 `updated_at / created_at`（bd 的 `memories --json` 兜底来源没有时间戳，只能用迁移时间）；tracker id 迁移本来就不改时间戳。

与设计的差异：

1. 只有 `when.keywords` / `when.commands` 的 rule 在开场不展开全文（开场只有 cwd 可匹配），进索引并标「规则」，由 P1b / P5 命中注入。
2. 「久未更新」标记（§2.1）顺带在 P1 的索引与 `memory ls` 中实现；「⚠ 可能过期」仍属 P3。
3. 未全文显示的规则因预算溢出时，规则段写「N 条未展开（见索引），请精简规则」，对应规则进入索引。
4. `PrimeRule`（派发 job 的强制规则）本步未改，仍注入全部记忆全文。

P1b / P2 / P3 扩展点：

- P1b：`tracker.MemoryMatchesKeyword / MemoryMatchesPath / MemoryMatchesCommand`（matcher），记忆来源 `Store.ReadMemories()` 与 `client.ListScopedMemories` → `ScopedMemory.TrackerMemory()`；摘要用 `DisplayMemorySummary`，过期用 `MemoryExpired`。
- P2：`PrimeOptions.Focus`（已渲染好的段落，预算 600B），由命令层 `primeWithServerContext` 取 git / server 信息后传入 `Store.PrimeWith`。
- P3：`MemoryMeta.Source`、`MemoryStale`、`MemoryExpiresAt` 可直接复用；archive 可在 `Store.UpdateMemories` 旁新增 `memories-archive.jsonl` 读写，prime 的 `newMemoryView` 只需不读归档文件。

### 4.2 P1b 实施记录（2026-10-09，已完成）

- **入口**：`hookrelay.Run` 的 `UserPromptSubmit` 人工分支（`isHarnessInput` 为假）在 job 补发通知之后调用 `injectPromptMemories`，结果拼接到 `Result.Context`（补发通知在前，空行分隔），由 `gofer hook` 输出为 `hookSpecificOutput.additionalContext`。只对 `CatchUpAgent` 为真的方言生效（claude / codex / generic）；omp（扩展丢弃输出）与 jcode（detached）不注入。
- **候选来源**：`hookrelay.NewMemoryLoader`——`tracker.Discover(cwd)` 的本地 `memories.jsonl`（无网络）+ server 全局 / 项目记忆（`client.NewWithTimeout(..., 250ms)`，与 prime 相同的短超时；全局列表失败即跳过项目列表）。项目 key：tracker `project_key` → `--project`/`GOFER_PROJECT` → `config.ProjectForPath(cwd)`。没有 tracker 的目录仍会取 server 记忆。
- **匹配**：`tracker.MemoryMatchesKeyword`（大小写不敏感子串）；跳过 `MemoryExpired` 的 handoff、`MemoryForAgent` 不符的 `agent:<名>` 记忆、已删除的 scoped 记忆。顺序：rule 在前，再按 key，同 key 按来源（仓库 → 项目 → 全局）。
- **防干扰**：每会话已注入 id（`repo/<key>`、`global/<key>`、`project:<pk>/<key>`）记在 `<config-dir>/run/prompt-memory/<sha1(session)>.json`（原子写，多次 hook 进程共享），写入时清理 7 天未更新的其他会话文件；单次 ≤ 2KB（`DefaultPromptMemoryBudget`），装不下的给 `DisplayMemorySummary` 摘要 + `gofer memory show [--global|--project <pk>] <key>` 提示，摘要形式也算已注入。
- **开关**：tracker `prime.inject_on_prompt`（默认 true，`PrimeConfig.InjectOnPromptEnabled`）；同时尊重 `prime.memory` / `prime.scoped_memory`（分别关掉本地 / server 来源）。全局 gofer config 没有 prime 段，未新增全局开关。
- **开场（SessionStart）**：cwd 命中 `when.paths` 排前已由 P1 的 prime 完成；claude / codex 的 `gofer init hooks` / `repo init` 都会装 `gofer repo prime --hook-json` 的 SessionStart 条目，hook 本身不再重复注入。generic agent 没有安装项，由接入方自行调用 `gofer repo prime`。

### 4.3 P2 实施记录（2026-10-09，已完成）

- **位置与开关**：`## 当前重点（自动，<本地时间>）` 由命令层 `primeFocus`（`internal/commands/repo_focus.go`）收集后经 `PrimeOptions.Focus` 放在提交策略之后；`prime.focus: false` 关闭（默认开）。
- **收集**：`tracker.Store.BuildFocus` 在 2s 的 ctx 内（挂载的 Windows 盘上冷启动 git status 约 1.1s，1s 会丢掉仓库行）并行取 git 与 server，本地 tracker 同步读；任何部分失败或超时只省略自己那几行，从不报错。 标题时间按 server 时区（`/v1/runners` 的 `server.utc_offset_sec`）显示，取不到时用本地时区并带缩写。
  - git（`tracker.CollectFocusGit`，`GitRunner` 可替换）：`git --no-optional-locks status --porcelain=v2 --branch --untracked-files=no`（分支 / HEAD / 已跟踪改动数 / tracker 文件数 / 领先上游）、`git describe --tags --long`（最近 tag 与之后提交数）、`rev-parse --show-prefix`（tracker 路径前缀）。
  - server（250ms 客户端超时）：`GET /v1/runners` 一次拿到 server 版本与各 worker 的在线状态和版本（替代设计中的 `/v1/meta` + `/v1/workers`，后者不带版本）；本项目 open plan 取最近更新的 2 个，进度用列表里的 `todo_counts`（done + skipped / total），下一个 todo 来自 `GET /v1/plans/<id>`（按 sort 第一个未完成）。没有 `project_key` 时不列 plan。
- **内容**：在做（14 天内有更新的 in_progress 最多 3、plan 最多 2、最新未过期 handoff 摘要）；刚解锁（近 3 天关闭、使 open issue 的 `blocks` 依赖全部关闭的，最多 3）；未收尾（已跟踪文件改动数，不含未跟踪文件；领先上游数，无上游不显示）；环境（仓库一行、服务一行；worker 与 server 同版本时只写「同版本」，不同的列出，离线的列名）。
- **预算**：≤600B，超出按 环境 → 未收尾 → 刚解锁 → 在做 的顺序、每类从最后一行起整行删除；只剩标题时整段省略。

与设计的差异：标题与配置键按「当前重点」命名（`prime.focus`，原稿为「现状」/`prime.status`）；未收尾只统计已跟踪文件，tracker 改动显示为文件数而非条目数（条目级差异见 `gofer repo status --changed`）。

### 4.4 P3 实施记录（2026-10-09，已完成）

- **doctor**：`tracker.Store.Doctor` / `DiagnoseMemories`（`internal/tracker/memory_doctor.go`），CLI `gofer memory doctor [--json]`，退出码恒 0。slug：`handoff-expired`、`note-stale`、`path-missing`、`commit-missing`、`summary-missing`、`duplicate`。路径与提交只查 rule / note（§5 第 3 条）。
  - 路径：反引号内或空白分隔、含 `/` 的 token；排除 URL、首段是域名的（Go import 路径）、含 `$ < > { } * ? [ ] = @` 的、`-` / `~` / `refs/` 开头的、`/v1` `/api` 路由与单段 `/cmd`；反引号外还须像文件（`./` `../` `/` `.` 开头、`/` 结尾或带扩展名），避免「server/worker」这类行文误报。解析基准：仓库根 + 其上 4 级目录（覆盖 `<repo>/.worktrees/<name>` 位于工作区里的情况）。
  - 提交：7–40 位小写十六进制且同时含数字和字母的词，`git cat-file --batch-check` 报 `missing` 才算；git 不可用时跳过，不误报。
  - 重复：key 首段（按 `- _ . : /` 切）相同且正文词集 Jaccard ≥ 0.6（ASCII 词 + 汉字二元组），两条都报。
  - 静默：`prime.doctor.suppress`（config）与单条 `doctor_ignore`（`MemoryMeta` 新字段，omitempty；`memory set --doctor-ignore`；同步按集合三方合并）。
- **prime「⚠ 可能过期」**：选「缓存」方案。prime 每次只做廉价检查（note-stale），`path-missing` / `commit-missing` 取 `.gofer/tracker/.local/doctor.json`（doctor 每次运行写入，记录 memories.jsonl 的 mtime + size；不一致或超过 24h 即视为过期不用），prime 本身不跑 git。只对 rule / note 标：全文规则写成 `- key（⚠ 可能过期）: …`，索引行末尾追加。
- **archive / restore**：`memories-archive.jsonl`（`ArchivedMemory` = 完整 Memory + `archived_at / archived_by / archive_reason`），同一把 tracker 锁下先写归档再写 memories.jsonl。选择**只走 git、不参与同步**：归档等于从 memories.jsonl 删除，sync 推删除标记，其他副本随之移除，归档内容随提交传播。`restore` 把 `updated_at` 置为当前时间，否则 server 上的删除标记（`deleted_at` 晚于原 `updated_at`）会在下次同步时再次删除它；`created_at` 保留原值。prime、`PrimeRule` 都只读 memories.jsonl，所以归档的不会出现。
- **promote**：`memory promote <key> --kind rule|note [--summary]`，走 `SetMemoryPatch`（清 `expires_at`、保留 source，按写入规则校验 summary）。
- **source**：`MemoryPatch.DefaultSource`——没传 `--source` 且原记录无来源时，CLI 填 `job:$GOFER_JOB_ID`，否则 `session:$GOFER_SESSION_ID`；`memory ls` 行尾「· 来源 …」（截 24 字）。
- **issue**：`issue ls --stale [--days 30]`（`IssueFilter.StaleDays`，按 updated → started → created 取最后活动时间；无 `--status` 时只看 open + in_progress；默认排序下最旧在前）；`issue create --from <id>` 复用已有的 `discovered-from` 依赖类型（`tracker.DepDiscoveredFrom`），`issue show` 多一行 `discovered-from:`。
- **job 强制规则 `PrimeRule`**：提交策略 + `kind=rule` 全文（按 key，含旧 `prime` 标签）+「其他记忆」一行索引（key ·（规则/交接）· 摘要，按更新时间倒序）；过期 handoff 与归档不注入；总长仍 ≤ `PrimeMaxBytes-64`，放不下的规则降为索引，索引放不下写「另有 N 条」。暂不按 `when.paths` 过滤（job cwd 与规则场景的匹配留给后续）。

与设计的差异：

1. 「⚠ 可能过期」不含 `summary-missing` / `duplicate`（它们不代表内容失效，只在 doctor 里报，供 P4 管家建议补写 / 合并）。
2. §2.5 的「版本号落后最近 tag 两个以上 minor」检查未做（误报面大、需要语义判断），留待需要时再加。
3. 归档文件不同步（§2.6 原写「同步」）：归档的删除本身经同步传播，归档内容走 git，避免 server 再存一份归档记录。

### 4.5 P5 实施记录（2026-10-09，已完成）

- **入口**：`hookrelay.Run` 新增 `PreToolUse` 分支 → `runner.preToolUse`（`internal/hookrelay/command_memory.go`）。不调 hub（无 register / 心跳），只做记忆注入；`gofer hook` 在命令层走独立的 `runPreToolUseHook`：没有 server 配置也能用（只取仓库本地记忆），输出 `hookSpecificOutput.additionalContext`（`hookEventName=PreToolUse`）。
- **方言**：claude（Claude Code PreToolUse 支持 `additionalContext`）、codex（官方 hooks 文档：PreToolUse 可返回 `hookSpecificOutput.additionalContext` 作为模型可见上下文，`tool_input.command`）、generic；omp / jcode 不注入（`CatchUpAgent` 为假）。shell 工具名复用 PostToolUse 的 `isShellTool`（`bash/shell/shell_command/exec_command/command/exec/run_command`，大小写不敏感）。命令取 `tool_input.command`（字符串，或 argv 数组按空格拼接），`tool_input.cmd` 兜底。
- **匹配**：`hookrelay.NormalizeCommand` 去掉首尾空白、`bash -lc` / `sh -c` 外壳及引号、开头 `env`、`NAME=value` 赋值、`cd <dir> &&` / `cd <dir>;`（循环直到不变），然后 `tracker.MemoryMatchesCommand` 前缀匹配（区分大小写，不按词边界）。不拆其余链式命令。
- **复用 P1b**：候选来源 `NewCommandMemoryLoader`（与 `NewMemoryLoader` 同一实现，开关换成 tracker `prime.inject_on_command`，默认 true）；过滤 / 排序（`matchMemories`）、2KB 预算渲染、每会话已注入集合（同一个 `<config-dir>/run/prompt-memory/<sha1(session)>.json`）全部共用，所以提问与命令两条路径对同一条记忆合计只注入一次。前缀「[gofer 记忆 · 执行 “<命中前缀>” 前]」。
- **不拖慢命令**：整条路径硬上限 300ms（`DefaultCommandMemoryDeadline`，命令层从进程启动起算，剩余不足时至少给 20ms）；加载 + 匹配 + 渲染在 goroutine 里做，超时即什么都不输出，且**只在按时返回时才记已注入**（超时的那次不吞掉记忆）。server 记忆列表每次 100ms 超时（全局失败即跳过项目）。任何错误都静默（只写 hook 日志）。
- **安装**：claude / codex 的嵌入模板加 PreToolUse 条目（claude `Bash`，codex `Bash|shell|shell_command|exec_command`，`gofer hook <agent>`，timeout 5s），`gofer init hooks` 按模板合并，天然幂等；`gofer init hooks --prime-only` 与 `gofer repo init` 通过 `hookrelay.InstallCommandMemory` 在没有 gofer PreToolUse 条目时补一条。`--remove --prime-only` 调 `RemoveCommandMemory`：只有在没装会话中继 hooks 时才删这条（否则它属于中继安装）。老用户重跑一次 `gofer init hooks`（或 `--prime-only` / `repo init`）即可获得。
- **与设计的差异**：无。全局 gofer config 仍没有 prime 段，开关只在仓库 tracker 配置。

## 5. 待确认

1. 三种 kind（rule / note / handoff）够不够？handoff 默认 TTL 14 天合适吗？
2. 「现状」段默认开启，含 server / worker 版本（需要连 server，失败即省略），可以吗？
3. 过期检测里「路径不存在」「提交不存在」这两条规则会有误报（历史记录里提到旧路径是正常的）。是否只对 `rule` 和 `note` 检查、`handoff` 不查？
4. 清理只做「建议 + 人确认」，不做自动归档，可以吗？
5. `issue update --status open` 默认清除指派人，可以吗？
6. 两段式里 `rule` / `note` 正文 > 200 字时强制要求 summary，可以吗？
7. 关键词命中注入全文：每会话每条最多一次、单次 ≤ 2KB，这个节奏合适吗？PreToolUse 命令命中放到 P5 再做，可以吗？
8. 借鉴 bd 的几项（§2.10）里，`source` 来源、`archive/restore`、`promote` 放进 P3 一起做，可以吗？
