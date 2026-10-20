<!-- template_id: design; template_version: 1.1.1 -->
# worktree 派活与收尾 设计

> 状态：Draft 0.1 / 已批准（用户 2026-10-21：「按推荐，继续」——决策 1–5 均取推荐值）
> issue：gofer-7b8e

- 思考模式：rigorous。
- 核心目标：一个监督会话把一项开发任务派给 agent、等它做完、审过后合回主线，整个过程中的机械步骤都由 gofer 完成；监督者只负责写任务书、审 diff、决定合不合。
- scope freeze：只扩展现有的 `job run --worktree`、`job worktree merge/rm` 与任务书模板；不新增「task」之类的新资源名词（用户 2026-10-20 认可）。
- review budget：本稿最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户对本稿给出具名的批准，或明确要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-21 | Claude（监督会话） | 初稿：worktree 目录与分支命名、依赖链接、规则模板、合并收尾（todo / 评论 / 清理 / 锁重试 / 允许 tracker 脏）、结束通知 |

## 背景：监督者现在每次手工做的事

2026-10-19 至 10-20 四批开发里，每派一项任务都重复：

1. 在主机建 `.worktrees/<名>` 与分支，给 `web/node_modules` 建目录链接；
2. todo 标 doing；任务书末尾追加同一段规则（不 push、apply_patch、控制字符检查、提交署名、验证命令、汇报格式）；
3. 提交 job，写轮询脚本等结束；
4. 审阅后合并：遇到 `.git/index.lock` 等待重试；CHANGELOG 冲突手工合并（另由 gofer-yf4p 解决）；
5. todo 标 done 并写说明；在 job 上留评论；
6. 删 worktree 与分支（先删依赖链接）。

## 现有能力（2026-10-21 核对）

- `job run --worktree [--worktree-base <ref>]`：在 `<仓库顶层>/tmp/gofer/wt/<job-id>` 建 worktree，分支 `gofer/<job-id>`；项目可设 `worktree_default: true`。
- `job worktree ls / merge <id> [--squash] [--cleanup-others] / rm <id> [--force] [--delete-branch]`；交付设置 `delivery` / `--deliver merge` 可在 job 结束后自动合并。
- 合并要求主 checkout 没有未提交的已跟踪改动，否则 409——但 `.gofer/tracker/*.jsonl` 在监督过程中几乎总是有改动（issue / plan 记录），所以现在实际用不上，只能手工合。
- 任务书模板：`job run -t <模板> --var k=v`，模板放在项目 `.gofer/templates/` 或全局。
- `job run --plan --todo` 已把 job 挂到 todo 并回写结果；`--notify` 在 job 结束时把结果送进提交它的会话。

## 方案

### 一、worktree 的位置、分支名与依赖链接（项目配置）

```yaml
projects:
  <project>:
    worktree:
      dir: ".worktrees/{name}"        # 默认 tmp/gofer/wt/{job_id}（现状）；{name} 来自 --worktree-name，没给时用 job id
      branch: "{name}"                # 默认 gofer/{job_id}（现状）
      links: [web/node_modules]       # 建 worktree 后，从主 checkout 链接过来的目录（Windows 目录联接 / Unix 符号链接）
```

- `job run --worktree --worktree-name <名> [--branch <分支名>]`：按项目配置的模板建；`--branch` 覆盖分支名。同名 worktree 已存在时报错（不复用，避免两个 job 抢同一目录）。
- 链接只在建 worktree 时创建，清理 worktree 时先删链接再删目录（不会顺着链接删掉主 checkout 的内容——这是现在手工清理时最怕出错的一步）。

### 二、固定规则做成项目任务书模板

- 不新增机制：把那段规则写成项目模板（如 `.gofer/templates/dev-task.md`），用 `{{task}}` 收任务正文，`job run -t dev-task --var task=@brief.md`。
- 补一个小能力：`--var key=@file` 从文件读变量值（现在只能内联）。

### 三、收尾：`job worktree merge` 扩展

```
gofer job worktree merge <job-id> [--squash] [--message <m>]
      [--todo-done [--note <说明>]] [--comment <评论>] [--cleanup]
```

- **允许 tracker 脏**：主 checkout 的未提交改动如果只在 `.gofer/tracker/` 下，照常合并（git 合并不碰这些文件时没有风险；碰到时由 tracker 合并驱动处理）；其他已跟踪文件有改动仍然 409。
- **锁重试**：遇到 `.git/index.lock` 时最多等待 60 秒（每秒检查一次）再合并，绝不删除锁文件。
- **合并成功后**，按参数依次做：`--todo-done` 把 job 所属 todo 标为 done 并写说明（默认说明：`<分支> 合入 <主线短 sha>`）；`--comment` 在 job 上留评论；`--cleanup` 删依赖链接、删 worktree、删分支（`-d`，只删已合并的分支）。
- 冲突：与现状一样中止复原并返回冲突文件；CHANGELOG 的「未发布」段由 gofer-yf4p 的合并驱动自动处理。
- 远程 worker 上的 worktree 与现状一样不支持合并（409）。

### 四、等待与通知

- 派活时带 `--notify`（现有）：job 结束时把结果送进监督会话，监督者不必轮询。
- 补一个 `gofer job wait <id>... [--any] [--timeout]`：等到这些 job 都结束（或任意一个结束）后返回，输出每个 job 的状态与退出码；给脚本和不在会话里的场景用，替代手写轮询循环。

## 范围与非目标

- 范围：上面四节；skill references（job-advanced 的 worktree 一节、plans-workflows）与 http-api。
- 非目标：远程 worker 上的 worktree 合并；自动审阅；新的资源名词。

## 测试计划

- 目录与分支模板、同名拒绝、依赖链接的创建与清理（清理不跟随链接删除目标）。
- `--var key=@file`。
- merge：tracker 脏可合、其他文件脏 409、index.lock 等待后成功 / 超时失败且锁仍在、`--todo-done` / `--comment` / `--cleanup` 各自与组合、冲突复原。
- `job wait`：全部结束、`--any`、超时。

## 决策

以下为本稿的提案，待用户确认：

1. **扩展现有命令，不新增名词**（用户已认可方向）。
2. **worktree 位置与分支名由项目配置模板决定**，默认保持现状（`tmp/gofer/wt/{job_id}`、`gofer/{job_id}`），不影响已有用法。
3. **合并时允许只有 `.gofer/tracker/` 下有未提交改动**；其他文件仍然要求干净。
4. **index.lock 等待上限 60 秒**，不删锁。
5. **新增 `gofer job wait`**，与 `--notify` 并存。

## 结论与人工计划 Gate

本稿经用户具名批准后写实施计划；设计批准不授权实施。

## 实施记录

计划 `docs/plans/2026-10-21-worktree-dispatch-and-landing-plan.md`（用户 2026-10-21 批准），随 v0.180.0 发布。

| 任务 | 合并 | 要点 |
|---|---|---|
| W1 位置 / 分支 / 链接、`--var @file` | a33bb3f5 | 项目 `worktree.dir` / `branch` / `links`，`--worktree-name` / `--branch`；链接建在 worktree 建好之后、job 执行之前，建立失败只记事件；`@file` 上限 1 MiB |
| W2 合并收尾、`job wait` | 70d5ab11 | 清理按 job 上记录的「实际建立的链接」逐个先删，删目录前再扫一遍链接 / 联接兜底；合并允许只有 `.gofer/tracker/` 脏；`index.lock` 最多等 60 s；`--todo-done` / `--note` / `--comment` / `--cleanup`；`gofer job wait`（轮询） |
| 补测 | d8a96544 | `--todo-done` 成功与冲突路径（服务层与 HTTP） |

与设计的出入：

- W1 最初按清理时的项目配置推导要删的链接；审阅发现配置变更后可能顺着目录联接误删主 checkout 的目标，W2 改为记录实际建立的链接并加扫描兜底。
- 按保留期自动清理（prune）的路径没有读取记录的链接，依靠删除前的扫描兜底。
- worker 上建立的链接记录在该执行机自己的 job 行，不经 wire 回报主机（清理只在执行机发生）。
- `gofer job wait` 用轮询现有读取接口实现，没有用推送。

