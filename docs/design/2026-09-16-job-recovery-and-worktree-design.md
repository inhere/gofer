<!-- template_id: design; template_version: 1.1.1 -->
# worker 断线恢复（RECOV-01）与受管 worktree 执行（WT-01）设计

> 状态：Approved 0.1 / 实施中（2026-09-16 人工拍板；对标 WebCodex 的 `recovering` 与 task worktree）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-16 | Claude | 初稿：断线→recovering→同实例重连续接/补发；`job run --worktree` 在 `tmp/gofer/wt/<job-id>` 的 git worktree 中执行 |

## 一、RECOV-01 worker 断线时 in-flight job 进入 recovering

### 背景与已确认事实

- server：`internal/wshub/hub.go` 读循环返回后 `onDisconnect` 对该连接的 in-flight job 调 `sink.OnDisconnect(errWorkerDisconnected)` → host job 立即 `failed`（`TestWorkerDisconnectMidJobFailsJob`）。唯一豁免是"新注册先于旧连接断开"的竞态：`reg.Put` 对**同 instance_id** 的旧连接标记 superseded（`hub.go:349-357`）。
- worker：`writeFrame` 永远写**当前** `cl.conn`（`client.go:740-758`，注释里已标 TODO h-aii-wag4），断线期间返回 `not connected`；`streamLocalJob` 在 writeFrame 失败后**仍推进文件偏移**（`dispatch.go:236-246`），该段日志永久丢失；dispatch goroutine 跑在 recvLoop 的 ctx 下（`client.go:644-648`），连接一断 `streamLocalJob` 就退出、Result 发到死连接后被 `_ =` 吞掉。本地 job 本身由 worker 的 `job.Service` 持有，进程不退就继续跑。
- serve 重启：`ReconcileOrphanJobs` 把所有非终态 job 直接 failed。
- 后果：WS 经 WSL/Docker/VPN 抖动一次，跑了几十分钟的 codex job 在 host 侧 failed，而 worker 侧其实跑完了。

### 目标

1. 同一 worker **进程**（instance_id 不变）在有界窗口内重连，in-flight job 不失败：日志续传、终态 Result 补发、状态回到 running。
2. 窗口超时、或重连的是新进程（instance_id 变化）→ 按现状 failed，`error_code=worker_lost`。
3. 期间 web/CLI 能看到 `recovering`，通知系统不把 recovering 当失败。
4. 老 worker 对新 server、新 worker 对老 server 都不崩：新字段全部可选。

### 方案

**状态**：job 状态枚举**尾部追加** `recovering`（不改既有值）。进入条件：worker 连接断开且该 job 未终态。退出：同实例重连 → `running`；窗口超时或新实例注册 → `failed`（`worker_lost`）；期间收到 cancel → 记录 cancel 意图，重连后下发。

**server（wshub + job service）**

- `onDisconnect` 不再对 sink 调 `OnDisconnect`，改为 `sink.Suspend(reason)`：job 置 `recovering`，`recovering_since`/`worker_id` 入库，启动 per-worker 恢复定时器 `worker.recover_window_sec`（默认 120，配置 `server.job_recover_window_sec`，0=关闭即旧行为）。superseded 连接（同实例竞态）保持现状不进 recovering。
- `Register` 帧新增可选 `inflight: [{job_id, status, stdout_off, stderr_off, seq}]`（worker 视角的远端 job_id 与已成功发送的日志偏移）。server 收到同 instance_id 的 Register：
  - 对每个 recovering job：在 `inflight` 中且 status 非终态 → `Resume()` 回 `running`，回 ack 里带 `resume: [{job_id, stdout_off, stderr_off}]`（server 已落盘的偏移，worker 据此重放）；在 `inflight` 中且已终态 → 等 worker 紧接着补发的 Result；不在 `inflight` 中 → 立即 failed（`worker_lost`，"worker no longer tracks job"）。
  - 旧 worker 不带 `inflight` 字段（nil）→ 视为"无法证实"，沿用恢复定时器：窗口内收到该 job 的任何 Log/Result 帧即回 running，否则超时 failed。
- 新 instance_id 注册（进程重启）→ 立即 failed 该 worker 的全部 recovering job（与现状 z8ow 语义一致）。
- 定时器超时 → `sink.OnDisconnect(errWorkerLost)` 走现有 classify/finish；`error_code=worker_lost`，`close_reason` 写 recovering 持续时长。
- `ReconcileOrphanJobs`（serve 重启）：worker job 不再直接 failed，而是置 `recovering` 并启动同一窗口；worker 重连时按上面的 `inflight` 协议恢复。local runner job 仍按现状 failed（进程内状态确实丢了）。

**worker**

- dispatch goroutine 改用**job 生命周期 ctx**（由 cancel 帧或进程退出取消），不再随连接 ctx 结束；`streamLocalJob` 只有在 `writeFrame` 成功后才推进偏移，失败则下个 tick 重试（自然形成断线期间的缓冲与重放）。
- 维护 `inflight` 表（远端 job_id → 本地 id、已确认偏移、seq、终态 Result 缓存）；Register 时随帧发送；收到 ack 的 `resume` 偏移时把本地偏移**回退**到 server 值再续传（server 是真源，避免重复）；断线期间已终态的 job 在重连后**先重发 Result**（幂等：server 对已终态 job 的重复 Result 丢弃）。
- `worker.job_recovering`/`worker.job_resumed` 事件进日志。

**协议兼容**：`Register.inflight`、ack `resume`、Log 帧无变化；旧 server 忽略未知字段，旧 worker 不发即走定时器路径。`protocol_version` 不 bump。

**web/CLI**：状态徽标 `recovering`（黄），详情显示 `recovering_since` 与窗口；`job list --status recovering`；IM 通知把 recovering→running 视为无事件、recovering→failed 才通知。

### 验收

- 单测（wshub/worker/job）：断线后 job 为 recovering；同实例 2s 内重连 → running 且日志无缺无重；断线期间 job 完成 → 重连后 Result 补发、host 终态与 worker 一致；窗口超时 → failed(worker_lost)；新实例注册 → 立即 failed；旧 worker（无 inflight）窗口内有帧 → running；cancel 在 recovering 期间 → 重连后下发并终态 cancelled。
- e2e：现有 `internal/worker` e2e 基础设施上"kill 连接不 kill 进程"的场景。
- `job_recover_window_sec: 0` 时全部旧测试行为不变。

## 二、WT-01 `job run --worktree`：在受管 git worktree 中执行

### 背景

多个 agent job 在同一 checkout 并行编辑/提交互相干扰（本周三次 `.git/index.lock` 残留）。WebCodex 默认每个 task 一个受管 worktree，值得照抄；gofer 的 `CaptureDiff`（`changes.diff`）与结果目录机制可以直接复用。

### 方案

- 请求：`JobRequest.Worktree bool`（`--worktree`），可选 `WorktreeBase string`（`--worktree-base <ref>`，默认当前 HEAD）。项目级 `worktree_default: true` 可让某项目默认开启。
- 定位仓库：以 job 解析后的 cwd 执行 `git -C <cwd> rev-parse --show-toplevel` 得到 `<top>`（嵌套仓库如 `hyy-ai-inspect/tools/gofer` 自然命中最近的顶层）；不是 git 仓库 → 拒绝 `worktree requires a git checkout`。
- 创建：`git -C <top> worktree add --detach <top>/tmp/gofer/wt/<job-id> <base>` 然后 `git -C <wt> switch -c gofer/<job-id>`；job 的实际 cwd = `<wt>/<cwd 相对 top 的子路径>`；环境变量 `GOFER_WORKTREE=<wt>`、`GOFER_WORKTREE_BRANCH=gofer/<job-id>`、`GOFER_WORKTREE_BASE=<base sha>`。`tmp/` 在各项目已被忽略（结果目录同一位置），worktree 目录随之被忽略。
- 结束：默认**保留**（分支上的提交是交付物）；`JobResult` 记 `worktree_path`、`worktree_branch`、`base_sha`、`head_sha`、`commits_ahead`；`changes.diff` 采集改为 `git diff <base>..HEAD` + 未提交改动两段。
- 清理：`gofer job worktree ls [-p]` / `gofer job worktree rm <job-id> [--force]`（`git worktree remove` + 可选删分支）；retention 清理 job 时若其 worktree 无未提交改动且分支已合并则一并移除，否则保留并在日志里列出。
- 与 `--cwd`：`--cwd` 仍相对项目根，映射进 worktree；与 `--interactive`：允许（pty 会话 cwd 在 worktree）；与 `exec`：允许。
- worker 侧：worktree 创建在执行机上（runner=worker 时由 worker 创建），路径按该机项目根解析；`git` 缺失 → 拒绝并给明确错误。

### 验收

- 单测：非 git 目录拒绝；嵌套仓库定位到最近顶层；cwd 子路径映射；两个并发 `--worktree` job 各自提交互不干扰且主 checkout 无改动；`changes.diff` 含分支提交；`job worktree rm` 清理；retention 对已合并/未合并分支的两种处理。
- 文档：README 提交章节 + `docs/runbook` 一段"并行 agent job 用 --worktree"。

## 非目标

- 不做 PR 创建/合并（保留分支交给人或后续 job）；不做跨机 worktree 同步；不改 pty 协议。

## 决策

- recovering 窗口默认 120s，server 配置，0 关闭；日志真源在 server 落盘偏移，worker 回退重放。
- worktree 放 `<top>/tmp/gofer/wt/<job-id>`，分支名 `gofer/<job-id>`，默认保留。
