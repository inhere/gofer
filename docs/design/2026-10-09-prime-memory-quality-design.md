# 新会话上下文质量：prime 与 memory 改进

> 状态：设计稿，待用户确认（2026-10-09）。
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

### 2.2 摘要取法

摘要不再取第一行，取「第一个非标题、非空的句子」（跳过 `#`、`【…】` 开头的行与纯链接行），≤80 字。写入时若内容 > 300 字且无 `summary`，CLI 提示「建议用 --summary 给一句话摘要」；`Memory` 增可选 `summary` 字段，有则优先。

### 2.3 prime 新布局与分段预算

总上限仍 8KB，各段有自己的预算，超出的段内截断并写明「另有 N 条」，**不再整体从尾部截断**：

| 顺序 | 段 | 预算 | 内容 |
|---|---|---|---|
| 1 | 提交策略 + 命令提示 | 固定 | 现状不变 |
| 2 | **现状（自动）** | ≤ 600B | §2.4 |
| 3 | **规则** | ≤ 3KB | `kind=rule` 全文，按 key 稳定排序；超预算时列 key 并提示「规则过长，请精简」 |
| 4 | 进行中 issue | ≤ 600B | 只算 `in_progress`；>14 天无更新标「认领 N 天无更新」 |
| 5 | ready 前 N | ≤ 800B | 现状不变，加年龄 |
| 6 | 笔记 | ≤ 1.5KB | 按真实更新时间倒序，摘要 + 「N 天前」 |
| 7 | 交接 | ≤ 600B | 未过期 handoff 最新 3 条 |
| 8 | 全局 / 项目记忆 | 余量 | 同样按 kind 规则显示 |
| 9 | 进行中 plan 交接说明 | 余量 | 现状不变 |

### 2.4 「现状」段（自动生成，best effort，总耗时 ≤ 1s）

```
## 现状（自动，2026-10-09 19:50）
- 仓库：main 8d41dde5，最近 tag v0.128.2（之后 2 个提交），tracker 未提交改动 0
- 服务：server 0.128.2；worker：w-docker-claude 0.128.2 · w-kzl-desktop 0.128.2 · w-mac-win10 离线
- plan：plan-…3fc4ecd5「N3 收尾…」7/8 完成，下一步：…
```

- 仓库信息本地读 git；服务与 worker 从 `/v1/meta`、`/v1/workers` 取（沿用 prime 现有 250ms 客户端超时，失败就省略该行）；plan 取本项目 open plan 的进度和第一个未完成 todo。
- 配置 `prime.status: true|false`（默认开），仓库可关。

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

## 3. 改动面

| 层 | 内容 |
|---|---|
| tracker 本地 | `Memory` 加 `kind/summary/ttl/created_at`；`memories-archive.jsonl`；prime 分段预算与新摘要；doctor |
| 同步 / 服务端 | 记忆 body 透传新字段（服务端按 body 存，无需改表）；全局 / 项目记忆加 kind |
| CLI | `memory set --kind/--ttl/--summary`、`memory ls` 列、`memory doctor`、`memory archive`、`issue update --status open` 清指派人 |
| 管家 | 巡检读 doctor，写 `memory` 建议卡（复用 T4 的建议 / 采纳机制） |
| web | 记忆列表显示 kind / 年龄 / 可疑标记；「今天」页 memory 建议卡 |
| skill | gofer-usage 记忆与 prime 章节 |

## 4. 分步

| 步 | 内容 |
|---|---|
| P1 | kind / summary / ttl 字段与 CLI；prime 分段预算、新摘要、年龄；「进行中」只看 in_progress；迁移保留时间戳 |
| P2 | 「现状」段 |
| P3 | `memory doctor` + 「⚠ 可能过期」标记 + `memory archive` |
| P4 | 管家清理建议卡 + web 显示 |

P1 价值最大、风险最小，可单独发版；P2 / P3 可并行。

## 5. 待确认

1. 三种 kind（rule / note / handoff）够不够？handoff 默认 TTL 14 天合适吗？
2. 「现状」段默认开启，含 server / worker 版本（需要连 server，失败即省略），可以吗？
3. 过期检测里「路径不存在」「提交不存在」这两条规则会有误报（历史记录里提到旧路径是正常的）。是否只对 `rule` 和 `note` 检查、`handoff` 不查？
4. 清理只做「建议 + 人确认」，不做自动归档，可以吗？
5. `issue update --status open` 默认清除指派人，可以吗？
