# 同目录串行锁（JOB-11）：等待、执行超时与取消

适用：可写 agent job（cli-agent / acp-agent，非 `--read-only`、非交互）落在**同一个目录**上；
或排在任何水位（项目 / caller / agent 信号量、目录锁）后面的 job **迟迟不动**、**没跑就 timeout**。

## 一句话口径

**job 的执行超时从"开始运行"（拿到目录锁、状态翻 `running`）那一刻才起算；排队不是执行。
等锁另有独立上限 `server.dir_lock_max_wait_sec`（默认 3600s，0 = 不限）。**

## 一个 job 的一生（排队相关）

```
submit → queued ──(项目/caller/agent 信号量)──→ queued
        └─(独占目录锁被占)─→ waiting_dir  holder=<谁堵着我>
        └─(拿到锁)────────→ running ──(自己的 timeout 从这一刻起算)──→ 终态
```

- **`waiting_dir` 只受独立上限约束**：超过 `server.dir_lock_max_wait_sec` → 终态 `failed`，
  `error = dir lock wait exceeded <N>s (holder "<job>", dir <path>)`，事件
  `job.dir_wait_timeout {holder_job, dir, wait_sec}`。它**从未运行**，所以这不是"跑挂了"。
- **信号量排队（`queued`）没有自己的上限**：它只等释放或被取消（这是"排队"的本义）。要么
  `gofer job cancel <id>`，要么让占位者结束。
- **取消在排队期同样立即生效**：`waiting_dir` / `queued` 的 job 被 cancel 就是 `cancelled`，
  且不会影响持锁者（`TestCancelWhileWaitingDir`）。
- 关掉整套锁：`server.dir_lock: false`（全部共享）；单个 job 用 `--shared-dir` /
  `--exclusive-dir` 覆盖。

## 常见排查

| 症状 | 看什么 | 处理 |
|---|---|---|
| job 停在 `waiting_dir` | `gofer job show <id>`（`waiting_dir: holder=<其他 job>`）、事件 `job.waiting_dir`、web Board 的 ⏳ 等目录芯片 | 看 holder 在干什么；不想等就 cancel；holder 卡死优先查它 |
| `failed: dir lock wait exceeded` | 事件 `job.dir_wait_timeout` 里的 `holder_job` | 明确是排队超限，不是执行失败；要么调大上限，要么修 holder |
| job 秒变 `timeout`、`error=job timed out` | 是否**真的跑过**（`stdout.log`/`stderr.log` 有没有输出）、`job.running` 事件时间 | 排队不算超时（v0.60.2 起）；若仍如此，看 `job.dir_wait_timeout`/`waiting_dir` 事件确认它到底等了多久 |
| holder 结束后队列才动 | `db` 里 holder 的终态时间 vs 下一个 job 的 `job.running` 时间 | 正常，FIFO 交接 |

## 相关配置

- `server.dir_lock`：整套锁的开关（默认开，热改）。
- `server.dir_lock_max_wait_sec`：**等锁上限**（默认 3600s，0 = 不限，热改，下一次提交生效；
  `null` = 未设即默认，"显式 0" = 不限，两者不是一回事）。
- `agents.<key>.max_concurrent`：单 agent 并发上限，超出者 `queued`。
- 这些策略在**提交时解析一次**并随请求下发（`exclusive_dir` / `dir_wait_max_sec` 走
  `Forward` / `Dispatch`），所以 `--runner worker` 的 job 用的也是 hub 的决定。

## 相关代码与测试

- 锁与等待：`internal/job/dirlock.go`（FIFO、祖先/后代同一把锁、取消出队）。
- 执行/等待的 ctx 划分与上限：`internal/job/execute.go`（`execGates.dirWait`）。
- 测试：`internal/job/dirwait_test.go`（`TestDirLockWaitDoesNotConsumeTimeout` /
  `TestDirLockWaitHasItsOwnCap`）、`internal/job/dirlock_test.go`（串行、取消、worktree 不取锁）。
- 缺陷背景（F12，2026-09-24 真机）：见 `docs/design/2026-09-24-settings-hub-and-tunnel-visibility-design.md`
  §四与 `docs/design/2026-09-17-acp-agent-and-approval-gate-design.md` 文末。
