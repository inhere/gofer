<!-- template_id: plan; template_version: 1.2.0 -->
# worktree 派活与收尾 实施计划

> 状态：Draft 0.1 / 已批准并请求执行（用户 2026-10-21：「批准计划，开始实施」）
> issue：gofer-7b8e

- 思考模式：rigorous。
- 核心目标：落地 `docs/design/2026-10-21-worktree-dispatch-and-landing-design.md`（决策 1–5 取推荐值）。
- scope freeze：只做设计「方案」四节。
- review budget：本计划最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户具名批准本计划，或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-21 | Claude（监督会话） | 初稿：W1 worktree 位置 / 分支 / 链接与 `--var @file`；W2 合并收尾与 `job wait`；串行 |

## 目标与完成定义

1. 项目配置 `worktree.dir` / `branch` / `links`；`job run --worktree --worktree-name <名> [--branch <分支>]`；链接随 worktree 创建与清理，清理不跟随链接删除目标。
2. `--var key=@file`。
3. `job worktree merge` 与 HTTP `POST /v1/jobs/{id}/worktree/merge`：只有 `.gofer/tracker/` 有未提交改动时允许合并；`.git/index.lock` 最多等 60 秒；`--todo-done [--note]`、`--comment`、`--cleanup`。
4. `gofer job wait <id>... [--any] [--timeout]`。
5. 文档同步。

## 已确认事实（代码核对）

- worktree 建立与状态在 `internal/job/worktree.go`；合并路由 `POST /v1/jobs/{id}/worktree/merge`（`internal/httpapi/server.go`，声明 `InHandler(permDeny, job.manage.others)` + `Resource("job")`），交付 `delivery` / `--deliver merge` 复用同一合并逻辑。
- `--var k=v` 在 `internal/commands/job.go`（`jobRunTemplateVars`）。
- 项目配置在 `internal/config/model.go`（已有 `WorktreeDefault`）。

## 波次与依赖

两项都改 `internal/job` 与 `internal/commands`，**串行**：W1 合入后再派 W2。

## 任务

通用要求：只提交自己的文件，英文 conventional message；不删注释、不重排无关代码；用户可见变化同步 skill references（job-advanced 的 worktree 一节、server-config 的项目配置、plans-workflows）与 http-api（G045 / G046，对外产物写通用用法，不写本仓库自己的目录约定）；G051–G053；主机实施不能 `-race`，监督者容器复跑。

### W1 worktree 位置、分支、依赖链接与 `--var @file`

- `config`：项目 `worktree: {dir, branch, links}`（`dir` / `branch` 支持 `{name}`、`{job_id}`；默认即现状 `tmp/gofer/wt/{job_id}`、`gofer/{job_id}`）；`config validate` 校验模板与 links 为项目内相对路径、不含 `..`。
- `job run --worktree-name <名>`（未给时 `{name}` = job id）与 `--branch <分支名>`（覆盖模板）；同名 worktree 目录或分支已存在时提交失败并说明。
- links：建 worktree 后对每个条目在 worktree 内创建指向主 checkout 同路径的链接（Windows 目录联接、Unix 符号链接）；源不存在时跳过并记一条事件。
- 清理（`job worktree rm` 与后续 `--cleanup`）：先删链接本身（不跟随），再删 worktree；有测试证明主 checkout 的目标目录完好。
- `--var key=@file`：读文件内容作为变量值（`@-` 读 stdin；`@@` 开头表示字面量 `@`），大小上限与 prompt 一致。
- 测试：模板展开、默认不变（回归）、同名拒绝、links 创建与安全清理（Windows 联接与 Unix 符号链接各一）、`@file` / `@-` / `@@`。

### W2 合并收尾与 `job wait`

- 合并前检查：主 checkout 的未提交已跟踪改动只在 `.gofer/tracker/` 下时允许继续（其他文件仍 409）；有未跟踪文件不影响（现状）。
- `.git/index.lock`：存在时每秒检查，最多 60 秒；超时返回 409 并说明「锁仍被占用」，不删锁。
- `--todo-done [--note]`：合并成功后把 job 所属 todo 标为 done，说明默认 `<分支> 合入 <主线短 sha>`；job 不属于 todo 时忽略并提示。`--comment`：在 job 上留评论（评论文本里的 `@` 不触发派活，沿用现有规则）。`--cleanup`：删链接、删 worktree、删已合并分支（`-d`）。HTTP 同名字段。
- `gofer job wait <id>... [--any] [--timeout <dur>]`：用现有的 job 状态读取 / 推送，等到全部（或任一）终态；输出每个 job 的状态、退出码与标题；超时非零退出。
- 测试：tracker 脏可合 / 其他脏 409、锁等待成功与超时（锁文件仍在）、三个收尾参数各自与组合、冲突复原后不做收尾、`job wait` 全部 / 任一 / 超时。

### V 验收（监督者）

- 容器 Linux 全量 `-race -timeout 30m`、darwin / windows vet；Windows 全量；用新命令实际派一项小任务并收尾一次（自用验证）。

## 回滚与恢复

- 新配置项与参数都是 additive，默认值即现状；`git revert` 即可回退。

## 人工 Gate

- **计划批准与执行请求**：用户具名批准本计划并要求开始实施。
- **外部动作**（已有长期授权）：tag、push、CI、mirror、一键更新。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| 一 位置、分支、链接 | W1 | 模板与链接用例 |
| 二 规则模板（`--var @file`） | W1 | `@file` 用例 |
| 三 合并收尾 | W2 | 合并用例 |
| 四 `job wait` | W2 | wait 用例 |
