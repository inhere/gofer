# 并行 agent job 用 `--worktree`

适用：同一项目要并行跑多个会**改动/提交代码**的 agent job（WT-01）。不适用：只读任务
（审查/问答）——它们不写 checkout，加 worktree 只是多一次检出。

## 为什么要用

多个 job 共用一个 checkout 时会互相踩：`.git/index.lock` 残留、A 的改动被 B 的
`git checkout`/`git stash` 冲掉、A 的未提交改动混进 B 的 diff。`--worktree` 让每个 job
在自己的 git worktree 里跑，提交落在自己的分支上，主 checkout 保持干净。

## 怎么提交

```bash
# 单次
gofer job run -p workspace -a codex --worktree --prompt "修 3 个 issue，逐个 commit"
# 指定基线（默认 = 当前 HEAD）
gofer job run -p workspace -a codex --worktree --worktree-base v1.2.0 --prompt "..."
# 本项目全默认开启：项目配置里写 worktree_default: true（见 config/gofer.example.yaml）
```

- md 任务文件 frontmatter 同名键：`worktree: true` / `worktree_base: v1.2.0`。
- `runner=worker` 时 worktree 由 **worker 建在它自己那台机器上**（路径按那边的项目根解析）；
  提交端已把项目默认值解析进请求，worker 不会用自己配置里的默认值覆盖你的决定。
- cwd 不是 git checkout → 提交被拒：`worktree requires a git checkout`。机器上没 git →
  `worktree requires git on PATH`（都不会"悄悄退回共享 checkout"）。

## job 里发生了什么

- 执行目录：`<仓库顶层>/tmp/gofer/wt/<job-id>`，分支 `gofer/<job-id>`（`tmp/` 已在项目中
  被忽略，主 checkout 的 `git status` 不会因此变脏）。嵌套仓库命中**最近**的顶层。
- cwd：`--cwd` 仍相对项目根，按相同子路径映射进 worktree（`--cwd sub` → `<worktree>/sub`）。
- 环境变量：`GOFER_WORKTREE`（worktree 路径）、`GOFER_WORKTREE_BRANCH`（`gofer/<job-id>`）、
  `GOFER_WORKTREE_BASE`（基线 **sha**，不是 ref 名）。agent 可据此定位自己的分支。
- 结束时：`job show <id>` / web 详情显示 `worktree` / `wt_branch` / `wt_head`（+ 领先提交数）；
  `changes.diff` 分两段 —— `=== committed (base..HEAD) ===`（分支上已提交的交付物）+
  `=== uncommitted ===`（结束时仍在工作区的残留）。

## 分支怎么合并

job 只负责"产出分支"，**不自动合并**（非目标）：

```bash
git -C <项目根> log --oneline gofer/<job-id>          # 看交付了什么
git -C <项目根> diff main..gofer/<job-id>            # 或看 changes.diff
git -C <项目根> merge --ff-only gofer/<job-id>       # 或 rebase/cherry-pick/开 PR
```

单机顺序合并多个分支时，第二个通常要 `rebase`（`--ff-only` 会因基线前移失败）：

```bash
git -C <项目根> rebase main gofer/<job-id> && git -C <项目根> merge --ff-only gofer/<job-id>
```

## 怎么清理

```bash
gofer job worktree ls                # JOB / PROJECT / BRANCH / AHEAD / DIRTY / MERGED / PATH
gofer job worktree rm <job-id>       # 移除 worktree；有未提交改动会被拒
gofer job worktree rm <job-id> --force            # 丢弃未提交改动
gofer job worktree rm <job-id> --delete-branch    # 连分支一起删（已合并后再删）
```

- `ls` 的状态是**实时**探测的：`AHEAD` = 领先基线的提交数，`DIRTY` = 有未提交改动，
  `MERGED` = 该分支的提交是否已包含在**主 checkout 当前分支**里（即"删了不会丢东西"）。
- `rm` 默认**保留分支**（分支是交付物）；`MISSING` 表示目录已不在本机（例如 job 跑在
  worker 上，或被人手删了）——此时在拥有那台机器上执行 `git worktree prune` 清理残留条目。
- HTTP 对应：`GET /v1/jobs/{id}/worktree`、`DELETE /v1/jobs/{id}/worktree?force=1&delete_branch=1`。

## 自动清理（retention）

`storage.retention` 清理 job 行时一并处理它的 worktree，判据是**已证明无损失**：

| 情形 | 处理 |
|---|---|
| 无未提交改动 且 分支已合并到基线分支 | `git worktree remove` + 删分支 |
| 有未提交改动，或分支未合并 | **保留**，日志 `job.worktree_retained` 列出路径/分支/原因 |
| 目录已不在 / 探测不到（无 git、checkout 损坏） | 保留，日志说明 |

所以"job 结束时分支还没合"的交付物不会被 retention 偷偷删掉——要么先合并，要么手工
`job worktree rm --delete-branch`。

## 常见问题

- **`worktree add` 报 already exists**：同 job id 的残留目录（极端情况：结果目录被保留但
  目录被手删一半）。手工 `rm -rf` 该目录后重投，或 `git worktree prune` 清条目。
- **一个项目要不要长期开 `worktree_default`**：项目里几乎都是并行写代码的 agent job →
  开；以只读/构建类 job 为主 → 不开（每次检出整棵树有成本）。
- **worktree 跑在 worker 上，我在本机 `rm` 报 MISSING**：正常。在 worker 那台机器上执行
  `gofer job worktree rm`（或者其 serve 的 `/v1/jobs/{id}/worktree`）。
