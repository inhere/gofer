# job 重试（AUTO-03 R2 可靠版）使用 Runbook

> 配套 design [`../design/2026-09-22-config-write-v11-reliable-retry-and-session-fallback-design.md`](../design/2026-09-22-config-write-v11-reliable-retry-and-session-fallback-design.md) §二。**默认关闭**：不改配置时行为与以前完全一致。

## 是什么

失败的 job 由 **serve 自己**按策略重投，**进程重启不丢**：

- 判定"该重试"发生在 job 终态那一刻（`finish`），但它只做一件事——往 `job_retries` 表写一行 `pending`（`next_run_at = now + 退避`）。**同一事务随终态落库**，所以"终态已写、重试没排上"不可能发生。
- serve 的 retry sweeper（15s 一轮）用**租约**领到期行（`ClaimDueRetries`，每轮最多 20 条、租约 60s），逐条提交成**新 job**（attempt+1、`request_id` 为空、异步、自动带 `retry` 与 `retry_of:<源 job>` 标签），成功则行置 `done` 并记 `job.retry_started`。
- 提交失败的行**不丢**：释放租约、`next_run_at` 推到下一档退避，最多晚一轮。
- 预算用尽（最后一次尝试仍失败）记 `job.retry_exhausted`——这条**进通知默认集**，是"没人会再跑它了"的信号。

## 配置：四级就近覆盖（推荐先只在单个 job 上试）

| 层 | 键 | 说明 |
|---|---|---|
| 1（最优先） | `job run --retry <n>[:<b1,b2,...>]` / `JobRequest.retry` | 单个 job 的策略；`--no-retry` = 显式关闭（`max_attempts: 1`），压过下面所有层 |
| 2 | `projects.<key>.retry` | 该项目所有 job 的默认 |
| 3 | `agents.<key>.retry` | 跑该 agent 的 job 的默认 |
| 4 | `server.retry` | 全局默认 |

**就近层整体替换，不逐字段合并**：某一层写了 `retry` 就用它整条（不会出现"上一层的 max_attempts + 下一层的退避表"这种半个策略）。四层都空 = 关闭。`max_attempts: 1` 等价于关闭，且**不会被更外层改回来**——项目想对某个项目关掉全站重试，就写 `max_attempts: 1`。

推荐值（先小后大）：

```yaml
server:
  retry:                     # 全局默认；不写就是关闭
    max_attempts: 3          # 含首次，即最多 2 次重试
    backoff_sec: [60, 300]   # 第 1 次失败等 60s，第 2 次等 300s；表尾复用
    on_exit_codes: []        # 空 = 任意非零退出都重试；建议按需收窄，如 [1, 2]
```

```bash
# 单个 job 先验证（推荐的第一步）
gofer job run -p workspace -a claude --retry 3:60,300 --prompt "..."
gofer job run -p workspace -a exec --retry 2:5 -- bash -lc 'exit 7'
```

- 重试**沿用原请求**（同 agent / prompt / cwd / timeout / session 等），只是 `attempt+1`。
- 每次尝试都是**新 job**（新 id），`retry_of:<源 job>` 指向失败的那个，`gofer job list --tag retry` 能一次筛出所有重投。
- 重试的请求里**固化了排程时的策略**，所以链条中途改配置不会改变已在队列里的那条重试的上限/退避。

## 与 auto_resume / stall / fallback 的分工（重要，避免叠加）

| 失败类型 | 谁接手 | 会不会再排重试 |
|---|---|---|
| 供应商类错误（`transient_error_patterns` 命中） | `auto_resume`（同会话续投）→ 不行则 `fallback_agents` 转移 | **不会** |
| 输出停滞（AUTO-05 `stall_timeout`） | 按 transient 处理：同上 | **不会** |
| `cancelled` / `timeout` | 无人接手 | **不会** |
| `needs_review` / `rejected` | 人在闭环里 | **不会** |
| **agent 真的跑完并失败**（`failed` + 退出码可重试） | 本节的 durable retry | **会** |

一句话：**transient 家族（续投/停滞/转移）优先，它接手了就不排重试**；只有"真失败"进 `job_retries`。

## 看与管

```bash
gofer job show <job>                 # 有待发重试时多一行：retry: attempt 2/3, next at 2026-09-22 14:05:00 +08:00
gofer job retry ls <job>             # 该 job 的重试链（id/attempt/state/reason/next_run_at/new_job_id）
gofer job retry cancel <retry-id>    # 取消一条待发重试（sweeper 不再投它）
gofer job list --tag retry           # 所有被重投出来的 job
```

- HTTP：`GET /v1/jobs/{id}/retries` 列出重试链，`DELETE /v1/retries/{rid}` 取消一条。
- web：job 详情顶部出现「重试 2/3 · 下次 14:05」提示条。
- 事件（job 时间线）：`job.retry_scheduled {retry_id, attempt, next_run_at, reason}`（在**源 job** 上）、`job.retry_started {retry_id, new_job_id}`（在**源 job** 上）、`job.retry_exhausted {attempts}`（在**最后一次尝试的那个 job** 上）。

## 通知

`job.retry_exhausted` 已加入 webhook 的**默认触发集**（与 `job.terminal` / `interaction.created` / `job.needs_review` / `plan.blocked` 同级）：重试用光了就是要人看。IM 文案一行：`job <id> · project <p> · agent <a> · 3 attempts · exit 7`，附 job 链接。

`job.retry_scheduled` / `job.retry_started` **不在默认集**（"还有下一次"不是呼叫人的信号）；想订阅就在 webhook 的 `events` 里显式列出。

## 运维要点

- **重启语义**：`pending` 行在库里，serve 起来后 sweeper **首轮就投**（这就是"可靠版"的全部含义）；崩在"已领未投"之间最多晚一个租约（60s），不会丢。
- **多实例**：领取是逐行条件 UPDATE（`state=claimed + lease_until`），同一行不会被两个 sweeper 投两次；租约到期前不会重复领。
- **放量风险**：server/agent 级开太松 + 必然失败的 job = 反复真的跑 agent（消耗额度）。缓解：默认关闭、优先 `on_exit_codes`、`job.retry_exhausted` 默认通知；**先在单个 job 上用 `--retry` 验证，再考虑项目/agent/server 级默认**。
- **保留期**：重试行随源 job 一起被 retention 清理（`PruneJobs` 同事务删 `job_retries`）。
- **排障**：
  - 重试没发生 → `gofer job show <job>` 看有没有 `retry:` 行；没有就查策略（四级哪层生效：`config validate` + `job show` 的 request）、退出码是否在 `on_exit_codes` 里、失败是否被判成 transient（`failure_class: transient` 就不进重试）。
  - 有行但一直不投 → serve 是否在跑（sweeper 15s 一轮）；`gofer job retry ls <job>` 看 `state`（`claimed` 卡住 = 上一轮崩了，等租约过期后自动重投）。
  - 不想让它再投 → `gofer job retry cancel <retry-id>`。
