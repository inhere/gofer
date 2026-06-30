# Cron 定时调度（AUTO-02）设计 + 实施计划

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-30 | claude | 初稿：内嵌请求 + 标准 cron + server 集中 sweeper |

- roadmap：**AUTO-02**（原 E23 定时任务/内置 cron）。关联 WF-03（定时跑工作流，v2）、AUTO-03（重试）、OBS-08（provenance channel）。
- 已确认（用户 2026-06-30）：**标准 5 字段 cron 表达式** + **schedule 内嵌完整 job 请求** + 先 design/plan 再实施。

## 1. 概览

serve 内置定时调度：把一份**准备好的 `JobRequest`** + **cron 表达式**存入 `schedules` 表；serve 一个周期 sweeper（仿 `startDeliveryLoop`）扫到期项，**先 advance 下一次、再异步 `Submit`**（条件更新抢占，重启/并发不重复触发）。用户把 cron 设到空闲时段（如 `0 2 * * *`）即"空闲时段执行准备好的任务"。

## 2. 范围

- **MVP 做**：内嵌单个 `JobRequest`；标准 5 字段 cron；server 集中触发（方案①，单 serve 无多 hub 去重问题）；enable/disable；run-now（立即跑一次，不影响 next）；list/show/rm。
- **不做（v2）**：内嵌 workflow spec（先 job）；负载感知触发；错过补偿回填多次；多 hub 去重；web UI；schedule 更新（MVP 用 rm+add）。

## 3. 数据模型：`schedules` 表

gofer 自有库、无服务前缀（与 jobs/interactions 一致）。`internal/jobstore/store.go` 加 DDL + `migrateSchedules`（仿 `migrateInteractions`）。

```sql
CREATE TABLE IF NOT EXISTS schedules (
  id           TEXT NOT NULL,     -- 生成 id（如 sch-<ts>-<rand> 或复用 job id 生成器）
  name         TEXT NOT NULL,     -- 人类可读名（便于 CLI 引用/日志）
  cron_expr    TEXT NOT NULL,     -- 标准 5 字段 cron
  request_json TEXT NOT NULL,     -- 内嵌的 JobRequest（marshalled）
  enabled      INTEGER NOT NULL,  -- 1 启用 / 2 停用（SR-风格从 1 起；这里 1/0 即可，0=停用）
  next_run_at  INTEGER NOT NULL,  -- 下次触发 unix 秒（add/每次 fire 后由 cron 算）
  last_run_at  INTEGER,           -- 最近触发时刻
  last_job_id  TEXT,              -- 最近触发产生的 job id（审计/排障）
  catch_up     INTEGER,           -- 1=错过的到期项补跑一次(默认) / 0=只 advance 不补跑
  project_key  TEXT,              -- 冗余，便于列表过滤/准入预检（真值也在 request_json）
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (id)
);
CREATE INDEX IF NOT EXISTS idx_sched_due ON schedules(enabled, next_run_at);
```

`ScheduleRecord` struct + CRUD 放新文件 `internal/jobstore/schedules.go`。

## 4. 关键流程

### 4.1 add（CLI/HTTP → store）
1. 校验 `cron_expr`：`cron.ParseStandard(expr)`（robfig/cron/v3），失败拒。
2. 校验内嵌 `JobRequest`：经现有提交前校验（project 存在 / agent ∈ allowed_agents / runner ∈ allowed_runners）——**复用 `job.Service` 的校验路径**，add 时就拒非法请求，不留到触发才失败。
3. `next_run_at = schedule.Next(now)`（严格未来的下一次）→ insert（enabled 默认 1）。

### 4.2 sweeper（`serve.startScheduleLoop`，默认 30s tick）
30s << cron 最小粒度 1min，保证分钟级 cron 在 ~30s 内被捞起。每 tick：

```
due = SELECT * FROM schedules WHERE enabled=1 AND next_run_at>0 AND next_run_at<=:now
for s in due:
    newNext = ParseStandard(s.cron_expr).Next(now)        # 下一次（未来）
    # 条件抢占(SR303 风格): next_run_at 仍是读到的旧值才更新 → 防多 tick/重启/并发重复
    affected = UPDATE schedules SET next_run_at=newNext, last_run_at=now, updated_at=now
               WHERE id=s.id AND enabled=1 AND next_run_at=s.next_run_at
    if affected != 1: continue                            # 已被本/他实例抢走
    # 错过补偿：到期太久(serve 宕过) 且 catch_up=0 → 跳过本次(已 advance)，不提交
    if s.catch_up==0 and (now - s.next_run_at) > missGraceSec: 
        log "skip missed run"; continue
    jobID, err = jobs.Submit(requestFrom(s.request_json))  # 异步触发；channel=cron
    if err==nil: UPDATE schedules SET last_job_id=jobID WHERE id=s.id   # best-effort
    else: log err                                         # 已 advance，下次按 cron 再来，不卡死
```

**advance-then-submit** 是关键：先把 next 推进到未来再提交，即使 Submit 失败也不会卡在同一时刻反复触发；条件更新保证恰好一次（重启/双实例安全）。

### 4.3 run-now
`POST /schedules/{id}/run-now` → 直接 `Submit(request)`，**不动 next_run_at**（手动补跑/测试）。

## 5. API（`/v1/schedules`，仿 interaction/job handler）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/schedules` | 创建：`{name, cron, request:{...JobRequest}, enabled?, catch_up?}` |
| GET | `/v1/schedules` | 列表（可选 `?project=`/`?enabled=`） |
| GET | `/v1/schedules/{id}` | 详情 |
| DELETE | `/v1/schedules/{id}` | 删除 |
| POST | `/v1/schedules/{id}/enable` \| `/disable` | 启停（disable 后 sweeper 不捞） |
| POST | `/v1/schedules/{id}/run-now` | 立即跑一次（不改 next） |

## 6. CLI（`gofer schedule`，别名 `sch`）

复用 `job run` 的请求构建旗标（project/agent/prompt 或 cmd/runner/tags/cwd…），只是末端**存为 schedule 而非提交**：

```
gofer schedule add --name nightly-report --cron "0 2 * * *" \
   -p workspace -a claude --runner local "跑夜间报告生成"
gofer schedule list                # 表格：id/name/cron/next_run/enabled/last_job
gofer schedule show <id>           # 含内嵌 request + next/last
gofer schedule enable|disable <id>
gofer schedule run <id>            # run-now
gofer schedule rm <id>
```

## 7. 安全

- 触发的 job 走正常 `Submit` → 受现有准入（allowed_agents/runners、project）约束；add 时预校验（§4.1.2）。
- **provenance**：触发的 job 标 `channel=cron`（扩 OBS-08/E34 的 channel 维度）+ caller=创建者或固定 `scheduler` caller，审计可区分"定时触发 vs 人工"。
- **secret（SR403/805）**：`request_json` 若内嵌 env/secret 会**明文落 DB**。MVP 文档警示"勿在 schedule 内嵌密钥"；secret 走 agent env / K8s secret 注入，schedule 只存非敏感请求。**不入日志**（log 只打 id/name/cron/job_id）。

## 8. 待确认

1. **依赖**：引入 `github.com/robfig/cron/v3`（仅用 `ParseStandard`+`Next` 算时间，不用其 Scheduler）。倾向用库（标准、稳）而非自写 5 字段解析。← **倾向用库**。
2. **错过补偿默认**：`catch_up` 默认 1（到期即补跑一次→advance）还是默认按 `missGraceSec` 跳过？倾向 **catch_up=1 默认 + 提供 missGraceSec 配置**（宕机超阈值则跳，避免凌晨任务在白天高峰补跑，呼应"空闲时段"意图）。
3. **caller**：触发 job 的 caller 用创建者 caller 还是固定 `scheduler`？倾向**记录创建者 caller_id 进 schedule，触发时沿用**（审计更准）。

## 9. 实施计划（每阶段独立 commit + 测试，可 SUPMODE）

- **P1 数据层**：`schedules` 表 DDL + `migrateSchedules` + `ScheduleRecord` + CRUD（Insert/Get/List/Delete/SetEnabled/UpdateAfterFire）+ `cron.ParseStandard` 包装算 next + 单测（建表/迁移/CRUD/next 计算/due 查询）。
- **P2 sweeper**：`serve.startScheduleLoop`（advance-then-submit 条件更新）+ 纯函数 `dueDecision`（可测）+ 接 `cr.Jobs.Submit` + 单测（到期触发/未到期不触发/条件抢占恰一次/catch_up 跳过/Submit 失败已 advance）。
- **P3 HTTP**：`/v1/schedules` CRUD + enable/disable/run-now handler + 路由 + 测试（含 add 校验 cron/请求拒非法）。
- **P4 CLI**：`gofer schedule add/list/show/enable/disable/run/rm`，复用 `job run` 旗标绑定 + 测试。
- **P5 收尾**：provenance `channel=cron` + go.mod 加 robfig/cron/v3 + runbook 用法 + 容器 E2E 冒烟（add 一条 1 分钟 cron → 等 sweeper 触发 → 验 job 产生 + next advance + disable 后不再触发）。

## 10. 验收（E2E 冒烟）

- add `*/1 * * * *`（每分钟）的 schedule（内嵌一个 echo/exec job）→ ≤90s 内 sweeper 触发一次、`last_job_id` 落库、`next_run_at` 推进到未来。
- `disable` 后 ≥2 分钟不再触发；`enable` 后恢复。
- `run-now` 立即产生 job 且 `next_run_at` 不变。
- 非法 cron / 非法 agent 在 add 时被拒（不留到触发）。
- 重启 serve：到期项按 catch_up 策略触发恰一次（不重复 stampede）。
