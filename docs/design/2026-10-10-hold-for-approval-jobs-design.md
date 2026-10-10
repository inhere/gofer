# 待批 job（`--hold` / `awaiting_approval`）设计

> issue: gofer-9b1b · 日期: 2026-10-10 · 状态: 已定，待实施

## 背景

用户在外只能用手机 web 时，agent 要做的对外 / 不可逆操作（典型是 `git push`）常被 agent 自身的权限检查拦下。让用户在手机上手敲长命令不现实，放宽 agent 的权限规则又过宽。需要「agent 提交、人在 web 点一下才执行」的 job。

## 目标与非目标

- 目标：`gofer job run --hold …` 提交后入库为 `awaiting_approval`，不派发、不占名额与目录锁；人在 web（手机宽度）看到完整命令后一键批准或拒绝；超时自动取消；server 重启不丢。
- 非目标（v1）：plan todo 自带 hold 属性、`--session` / `--interactive` / workflow 步骤的 hold、Web Push 里的批准按钮、强制某些命令必须 hold。

## 状态与语义

- 新状态 `awaiting_approval`：非终态，`IsFinished=false`（job 还会跑）。与 `queued` 的区别是**内存表 `s.jobs` 里没有条目**，纯数据库态。
- 流转（全部用数据库 CAS `UPDATE … WHERE id=? AND status='awaiting_approval'`，互斥且幂等）：
  - 批准 → `queued` → 正常执行（同一 job id）。
  - 拒绝 → `cancelled`（error `hold rejected by <who>[: <reason>]`）。
  - 超时 → `cancelled`（error `hold expired`）。
  - 提交者 `cancel` → `cancelled`。
- 事件：`job.awaiting_approval`、`job.hold_approved`、`job.hold_rejected`、`job.hold_expired`。用 `hold_` 前缀，避免和验收拒绝的状态 `rejected` / `job.reviewed{verdict:rejected}` 混淆（偏离 issue 原文的 `job.approved` / `job.rejected`，理由同此）。
- 不加入：`nonTerminalJobStatuses`（启动会判 failed）、`orphanWorkerJobStatuses`、升级 drain 计数、`supervisedJobStatuses`、保留期清理的 `terminalStatuses`。

## 存储

- jobs 表加列 `hold_expires_at INTEGER`、`hold_json TEXT`（`{reason, timeout_sec, origin, digest, prompt_preview, decision, decided_by, decided_at, note}`，同 `fallback_json` 模式）；部分索引 `idx_jobs_hold_due ON jobs(hold_expires_at) WHERE status='awaiting_approval'`。
- `jobstore.DecideHold(id, to, holdJSON, ts) (bool, error)`、`ListDueHolds(now, limit)`。

## 提交与批准（`internal/job`）

- 拦截点：`submitAdmitted` 的 `tm.mark("admit")` 之后、worktree / 凭据 / entry / `execute` 之前。此时准入校验全做完，没有副作用。
- `submitAdmitted` 开头保存原始请求快照 `rawJSON`；hold 阶段跳过 `stageSkills`（worker 暂存区会过期，批准时再暂存）。
- hold 分支：建 job 目录、persist 一行 `awaiting_approval`（`RequestJSON=rawJSON`）、登记会话 watch、记 `job.submitted` + `job.awaiting_approval`、`linkTodoSubmit`，直接返回。
- 批准 `ApproveJob(id, by, note)`：取升级 permit（排空中返回 503）→ CAS 到 `queued` → 记 `job.hold_approved` → 反序列化原始请求、设内部 `heldJobID=id` 再走 `submitAdmitted`：复用 job id 与目录、跳过 request_id 去重与重复的 `job.submitted`，其余（解析、凭据、worktree、`execute`、worker `Forward`）一行不变。启动失败则 CAS 到 `failed`（`approved but could not start: …`）并走终态收尾。
- **所批即所跑**：hold 时对 `{project, agent, runner, cwd, cmd, prompt_preview, read_only, worktree, env 键值}` 算 sha256 存 `digest`；批准重入时重算，不一致返回 409 `ErrHoldDrift`（「请求在等待批准期间变了，请重新提交」）。
- **hold 跟着请求走**：批准后 `request_json` 仍带 `hold=true`，`rebuild`、自动重试、schedule、`resume` 都会重新进入待批，堵住「把批过的 push job rebuild 一次就免批再跑」。
- 终态收尾抽 `finishUnrun(snap)`：`linkTodoOutcome`、`maybeBlockPlan`、`notifyTerminalHooks`、workflow `Advance`（同 `review.go` 尾部）。
- `SubmitSync` 遇待批直接返回 async（HTTP 202）；`Cancel` 遇待批走 CAS 而不是 no-op。
- worker / peer runner：hold 只发生在 hub，批准后照常 `Forward`，worker 无感知，**不改协议**；worker 凭据批准时才铸。

## 超时

- 配置 `server.hold.default_timeout_sec`（默认 86400）、`max_timeout_sec`（默认 604800）；`--hold-timeout` 为 0 取默认，负数或超上限返回 400（不截断）。`hold_expires_at` 在提交时定死。
- 持久化扫描 `startHoldExpiryLoop`：启动扫一次，之后每 30 秒，照 `startRetryLoop`。重启后自然恢复，停机期间过期的在启动时转 `cancelled`。

## 权限

- 路由 `POST /v1/jobs/{id}/approve`；`reject` 复用 `/reject`，服务端按状态分派（待批时理由可选，`--resume` 返回 400）。两者走 `humanReviewer`（worker token 403，开 `governance.require_answer_capability` 时要求 `can_answer`）。
- job 凭据（`GOFER_JOB_TOKEN`，含 leader / steward）一律 403：`approve` 不进 job allowlist。
- **诚实边界**：容器 agent 通常与人共用同一个用户 token，服务端分不清。真正隔离要给 agent 配独立 token 并开 capability 闸门（文档写成推荐配置）。默认配置下的护栏：CLI `job approve` 在 agent 会话环境（`GOFER_SESSION_ID` / `CLAUDE_CODE_SESSION_ID` / `CODEX_THREAD_ID` / `CODEX_SESSION_ID` / `GOFER_JOB_TOKEN`）里拒绝且不留绕过开关；MCP 不提供 approve 工具，`gofer_reject_job` 对待批 job 报错并提示用 cancel；skill 明令 agent 不得自批。护栏不是安全边界。
- `hold_json.origin` 记提交来源（`job:<id>` / `agent-session:<sid>` / channel），面板显示为「提交者」。

## 组合

| 组合 | v1 |
|---|---|
| `--review` | 允许叠加：执行完照常 `needs_review` |
| `--plan` / `--todo` | 允许：待批时 todo 为 doing；拒绝 / 超时经 `maybeBlockPlan` 停住 plan |
| `--sync` | 强制异步，打印说明 |
| `--wait` | 允许，等待窗口 = hold 超时 + job 超时，待批期间轮询放宽到 ≥5s |
| `--session` / `--interactive` / workflow 步骤 | 拒绝（400，写明原因） |
| `--worktree` / `--verify` / `--retry` / `--lock` / `-f` / request_id | 允许，批准后才解析 |

## 通知与回流

- `job.awaiting_approval` 进 `notify.DefaultTriggerEvents`，立即渲染 `ApprovalMessage`（标题「待批准：<title>」、理由 + 命令前几行 + 过期时间、链接 `/jobs/<id>`「去批准」）；Web Push 同 `needs_review` 分支，urgency high。决定类事件不进默认集。
- 回到提交会话不需新链路：终态经现有会话 job watch + Stop hook 推回。小增量：watch 视图加 `error`，`cancelled` 通知附 `reason=hold rejected…` / `hold expired`。
- CLI 提交后打印 `job <id> submitted: status=awaiting_approval …` 与 `awaiting approval: <web>/jobs/<id>`（不含 "finished"，免被 hook 误判结束）。

## Web（Vue 3）

- 类型 / `StatusBadge`「⏸ 待批准」/ Signal / 颜色 / eventMeta。
- `ApprovalPanel.vue` 放 JobDetail 页首：理由、完整命令（exec 显示 argv；agent 显示 prompt_preview，默认展开，`pre-wrap` 不横滚）、项目 / cwd / runner（`runnerLabel`）/ agent、提交者、提交时间与过期倒计时；≤640px 按钮 sticky 底部、触控 ≥44px；批准一键，拒绝展开可选理由。
- 顶栏「等你」：`today` 加 `approvals` 卡（kind `approval`，截止 = 过期时间，不进 advice 一键建议）；看板加「待批准」过滤与计数。/review 与工作台不列入，/review 页头放「待批准 N →」链接。

## 实施拆分

1. **WP1** 后端状态、存储与服务层核心（job / jobstore）。
2. **WP2** 超时扫描与配置（config / serve）——依赖 WP1。
3. **WP3** API、CLI、MCP——依赖 WP1。
4. **WP4** 通知、回流与今日卡——依赖 WP1。
5. **WP5** Web——依赖 WP3、WP4。
6. **WP6** 文档：gofer-usage skill（★ 速查：「对外 / 不可逆操作被拦时用 `--hold` 提交，请用户在 web 批准；不要自己 approve」）、references、CHANGELOG、README。

端到端验收：`--hold` 提交 `exec -- sh -c 'date > probe'` → probe 不存在 → web 手机宽度批准 → probe 出现、done；另一个待批 job 在 server 重启后仍待批，拒绝后 `cancelled` 且未执行；job token 调 approve 得 403。
