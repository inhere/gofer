# Cron 定时调度（AUTO-02）使用 Runbook

> 配套 design [`../design/2026-06-30-cron-schedule-design.md`](../design/2026-06-30-cron-schedule-design.md) / plan [`../plans/2026-06-30-cron-schedule-plan.md`](../plans/2026-06-30-cron-schedule-plan.md)。容器控制面 E2E 14/14 全绿。

## 是什么

serve 内置定时调度：把一份**准备好的 job 请求** + **标准 5 字段 cron 表达式**存入 `schedules` 表；serve 周期 sweeper（默认 30s）扫到期项，**先 advance 下一次、再异步提交**（条件更新抢占，重启/并发恰触发一次）。把 cron 设到空闲时段即「空闲时段执行准备好的任务」。

## CLI 用法

```bash
# 新建：每天凌晨 2 点跑一个 claude 任务（请求旗标与 `job run` 完全一致）
gofer schedule add --name nightly-report --cron "0 2 * * *" \
  -p workspace -a claude --runner local --prompt "跑夜间报告生成"

# exec 命令型任务（argv 走 `--`）
gofer schedule add --name disk-check --cron "*/30 * * * *" \
  -p workspace -a exec --runner local -- df -h

gofer schedule list                 # id/name/cron/next_run/enabled/last_job
gofer schedule show <id>            # 详情（含内嵌 request、next/last）
gofer schedule disable <id>        # 停用（sweeper 不再捞）
gofer schedule enable  <id>        # 恢复
gofer schedule run <id>            # 立即跑一次（run-now，不改 next_run_at）
gofer schedule rm <id>             # 删除
```

- cron 是**标准 5 字段**（分 时 日 月 周），如 `0 2 * * *`、`*/30 * * * *`、`0 9 * * 1`。非法表达式 / 非法 agent / 非法 project 在 `add` 时即被拒（与 `job run` 同款准入校验）。
- `--catch-up=false`：宕机错过的到期项**跳过补跑**（仅 advance）；默认 `true`（到期补跑一次）。超 `schedule.miss_grace_sec`（默认 3600s）的错过项即使 catch_up 也跳过——避免凌晨任务在白天高峰补跑。
- 触发的 job 标 `channel=cron`（审计区分定时 vs 人工），并沿用创建者 `caller_id`。

## HTTP API（`/v1/schedules`）

`POST`（create，body `{name, cron, request:{...JobRequest}, enabled?, catch_up?}`）/ `GET`（list，`?project=`）/ `GET {id}` / `DELETE {id}` / `POST {id}/enable|disable|run-now`。

## 配置（可选，`config.yaml`）

```yaml
schedule:
  sweep_interval_sec: 30    # 兜底扫描节拍（默认 30s，<< cron 1min 粒度）
  miss_grace_sec: 3600      # 错过超此秒数则跳过补跑
```

## ⚠️ 安全（SR403/805）

**勿在 schedule 内嵌密钥**：`request_json` 会**明文落 DB**。secret 走 agent `env` / K8s secret 注入（不落 request_json），schedule 只存非敏感请求。serve 日志只打 schedule id/name/cron/job_id，不打 request 内容。

## 运维要点

- 单 serve：无多 hub 重复触发问题。多实例场景靠 `AdvanceSchedule` 的条件更新（`WHERE next_run_at=旧值`）保证恰一次。
- 重启恢复：到期项在重启后 sweeper 首轮触发**一次**（不重复 stampede）；catch_up=0 或超 grace 的则跳过。
- 排障：`gofer schedule show <id>` 看 `last_job_id` → `gofer job show <last_job_id>` 看实际执行结果。
