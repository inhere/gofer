# Cron 定时调度（AUTO-02）实施计划

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-30 | claude | 初稿，配 SUPMODE + host codex 实施 |

- design：[`../design/2026-06-30-cron-schedule-design.md`](../design/2026-06-30-cron-schedule-design.md)
- issue：`example-project-2m2s`
- 已确认默认（按推荐）：① cron 用 `github.com/robfig/cron/v3`（仅 `ParseStandard`+`Next`）② `catch_up` 默认 1 + `schedule.miss_grace_sec` 配置（超阈值跳过补跑）③ 触发 job 沿用创建者 `caller_id`，`channel=cron`。

## 执行约定（host codex 必读）

- 工作目录 `tools/gofer`（与 host 共享同一工作树）。每阶段：写代码 → `go build ./...` → `go test ./...`（相关包）→ **报告改动文件 + 测试结果**；**不要 git commit / 不要 push**（主控验收后统一提交）。
- 遵循现有分层与风格：入口只绑定/转发（G021）；依赖单向（G022）；jobstore 写经 `writeMu`；迁移用 additive `ALTER ADD`（仿 `migrateInteractions`）；表无服务前缀（与 jobs/interactions 一致）。
- 复用现有范式，**勿造新轮子**：sweeper 仿 `internal/serve/serve.go:startDeliveryLoop`；表/CRUD 仿 `internal/jobstore/interactions.go` + `store.go`；HTTP handler 仿 `internal/httpapi/interaction_handler.go`；CLI 仿 `internal/commands/job.go`。
- 关键方法/逻辑加简明注释（SR1203）；新增逻辑配单测；不降覆盖。

## 关键代码锚点（实施依据）

- `JobRequest`（`internal/job/model.go:8+`）：`ProjectKey/Agent/Runner/Prompt/Cmd/Cwd/TimeoutSec/Title/Role/SystemPrompt/Env/Tags/Channel/CallerID`。触发时 `Channel="cron"`，`CallerID`=schedule 创建者。
- 提交：`func (s *job.Service) Submit(req JobRequest) (JobResult, error)`（`internal/job/submit.go:24`）；id 生成 `genJobID()`（submit.go:323）。
- serve loop 接线：`runServe` 内 `startDeliveryLoop`(serve.go:116)/`startWorkflowLoop`(123) 旁加 `startScheduleLoop`。loop 定义仿 `startDeliveryLoop`（serve.go:517）。
- jobstore 表：`store.go` 建表数组 + `migrateInteractions`(店内迁移范式) + `tableColumns`/PRAGMA 探测。
- HTTP：路由 `internal/httpapi/server.go:~321`（interactions 路由块旁）；handler 仿 `interaction_handler.go`；`writeError`/`interactionStatus` 风格的错误映射。
- CLI：`internal/commands/job.go`（命令组 `job` 的 `run/list/show/...`）；旗标绑定复用 `job run` 的请求构建。

---

## P1 数据层（jobstore + cron next 计算）

**目标**：`schedules` 表 + `ScheduleRecord` + CRUD + cron next 计算，全单测覆盖。

**改动**：
1. `go.mod`：`go get github.com/robfig/cron/v3`（仅解析用）。
2. `internal/jobstore/store.go`：建表数组加 `schedules` DDL（见 design §3，含 `idx_sched_due`）；新增 `migrateSchedules()`（仿 `migrateInteractions`，对旧库 additive 补列——首版整表新建，迁移函数留空骨架即可，与 jobs/interactions 一致在 Open 时调用）。
3. 新文件 `internal/jobstore/schedules.go`：
   ```go
   type ScheduleRecord struct {
       ID, Name, CronExpr, RequestJSON string
       Enabled      int   // 1 启用 / 0 停用
       NextRunAt, LastRunAt int64
       LastJobID    string
       CatchUp      int   // 1 补跑一次 / 0 跳过
       ProjectKey   string
       CreatedAt, UpdatedAt int64
   }
   // CRUD（均经 writeMu 写）：
   func (s *Store) InsertSchedule(r ScheduleRecord) error
   func (s *Store) GetSchedule(id string) (ScheduleRecord, bool, error)
   func (s *Store) ListSchedules(projectFilter string, enabledOnly bool) ([]ScheduleRecord, error)
   func (s *Store) DeleteSchedule(id string) error
   func (s *Store) SetScheduleEnabled(id string, enabled int) error
   // DueSchedules: enabled=1 AND next_run_at>0 AND next_run_at<=now，按 next_run_at asc
   func (s *Store) DueSchedules(now int64) ([]ScheduleRecord, error)
   // AdvanceSchedule: 条件抢占——仅当 next_run_at 仍=oldNext 才推进（防重复触发）
   //   UPDATE ... SET next_run_at=newNext,last_run_at=now,updated_at=now
   //   WHERE id=? AND enabled=1 AND next_run_at=oldNext  → 返回 affected==1
   func (s *Store) AdvanceSchedule(id string, oldNext, newNext, now int64) (bool, error)
   func (s *Store) SetScheduleLastJob(id, jobID string) error
   ```
4. cron 计算辅助（放 `internal/jobstore/schedules.go` 或 `internal/job/` 小工具）：
   ```go
   // NextRun 解析标准5字段 cron 并返回 after 之后的下一次 unix 秒；非法表达式返回 error。
   func NextCronRun(expr string, after time.Time) (int64, error)  // robfig cron.ParseStandard
   ```
   > 放置位置注意分层：若放 jobstore 则 jobstore 引入 robfig 依赖（可接受，jobstore 已是数据层）。亦可放独立小包 `internal/schedule/`。**择一，保持依赖单向**。

**测试**（`internal/jobstore/schedules_test.go` 等）：
- 建表/迁移：`tableHasColumn(schedules, cron_expr/next_run_at/catch_up/...)`。
- CRUD round-trip：Insert→Get→List(过滤 project/enabled)→Delete。
- `DueSchedules`：到期(next<=now)入选、未到期/disabled/next=0 不入选，按 next 升序。
- `AdvanceSchedule`：oldNext 匹配→affected=1+值更新；oldNext 不匹配(并发已改)→affected=0 不动。
- `NextCronRun`：`"*/1 * * * *"` 下一次=after 向上取整到下一分钟；`"0 2 * * *"` 取次日 02:00；非法表达式 error。

**验收门**：`go test ./internal/jobstore/...` 全绿；`go build ./...` 绿。

**commit（主控）**：`feat(schedule): P1 数据层 schedules 表 + CRUD + cron next 计算 (AUTO-02)`

---

## P2 sweeper（serve.startScheduleLoop）

**目标**：周期扫到期项，advance-then-submit，重启/并发恰一次。

**改动**：
1. `internal/serve/serve.go`：
   - 常量 `scheduleSweepInterval = 30 * time.Second`（注释：<< cron 1min 粒度）。
   - `runServe` 内（`startWorkflowLoop` 旁）：`stopSchedule := make(chan struct{}); defer close(stopSchedule); startScheduleLoop(c, cr, stopSchedule)`。
   - 纯函数（可测，不碰 *Core）：
     ```go
     // sweepSchedules 处理一轮：对每条到期 schedule 先 advance(条件抢占) 再 submit。
     // due: 当前到期列表; nextOf: 算 cron 下一次; advance: 条件推进(返回 affected==1);
     // submit: 提交并返回 jobID; setLast: 记 last_job_id; missGrace: 超此秒数且 catch_up=0 则跳过补跑。
     func sweepSchedules(now int64, due []jobstore.ScheduleRecord, missGrace int64,
        nextOf func(expr string, after int64) (int64, error),
        advance func(id string, oldNext, newNext int64) (bool, error),
        submit func(r jobstore.ScheduleRecord) (string, error),
        setLast func(id, jobID string), logf, errf func(string, ...any))
     ```
     逻辑：见 design §4.2（advance-then-submit + catch_up/missGrace 跳过 + Submit 失败已 advance 不卡死）。
   - `startScheduleLoop`：goroutine——启动先 sweep 一次（catch 重启前到期），再 `ticker`/`stop` 二路 select，每次调 `sweepSchedules`，闭包注入：`nextOf=NextCronRun`、`advance=cr.Store.AdvanceSchedule(...)`、`submit=`(反序列化 request_json→JobRequest，置 `Channel="cron"`、保留 `CallerID`，`cr.Jobs.Submit` 返回 id)、`setLast=cr.Store.SetScheduleLastJob`。`missGrace` 取 `cfg.Schedule.MissGraceSec`（默认见 P? config）。
2. `internal/config/model.go`：加 `Schedule` 段：`SweepIntervalSec int`、`MissGraceSec int`（默认在 serve 兜底：sweep<=0→30s，miss<=0→比如 3600s）。loader 无强校验（可选）。

**测试**（`internal/serve/schedule_test.go`）：纯函数 `sweepSchedules` 矩阵——
- 到期且 advance 成功→submit 调用 1 次、setLast 记录；advance 返回 false(被抢)→不 submit。
- catch_up=0 且 now-next>missGrace→跳过 submit（仅 advance）；catch_up=1→照常 submit。
- submit 返回 err→不 setLast、不 panic（已 advance）。
- 多条 due→各自独立处理。

**验收门**：`go test ./internal/serve/... ./internal/config/...` 绿；`go build ./...` 绿。

**commit**：`feat(schedule): P2 serve sweeper advance-then-submit + config (AUTO-02)`

---

## P3 HTTP API（/v1/schedules）

**目标**：CRUD + enable/disable + run-now。

**改动**：
1. `internal/httpapi/server.go` 路由块加：
   ```
   r.POST("/schedules", s.handleCreateSchedule)
   r.GET("/schedules", s.handleListSchedules)
   r.GET("/schedules/{id}", s.handleGetSchedule)
   r.DELETE("/schedules/{id}", s.handleDeleteSchedule)
   r.POST("/schedules/{id}/enable", s.handleEnableSchedule)
   r.POST("/schedules/{id}/disable", s.handleDisableSchedule)
   r.POST("/schedules/{id}/run-now", s.handleRunSchedule)
   ```
2. 新文件 `internal/httpapi/schedule_handler.go`：
   - create body：`{name, cron, request:{...JobRequest}, enabled?, catch_up?}`。校验：`NextCronRun(cron)` 合法；`request` 经 `s.jobs` 提交前校验（project/agent/runner 准入）——**复用 job 校验路径**（如有独立 validate 则调它，否则 dry-run 思路：至少校验 project 存在 + agent∈allowed）。算首个 `next_run_at=NextCronRun(cron, now)`，`InsertSchedule`。
   - run-now：`GetSchedule`→反序列化 request→`Channel="cron"`→`s.jobs.Submit`→返回 JobResult；**不改 next_run_at**。
   - 错误映射仿 `interactionStatus`（未知 id→404、非法 cron/请求→400）。
   - 视图：`scheduleView`（snake_case，含 next_run_at/last_run_at/last_job_id/enabled，request 回显）。
3. id 生成：复用 job id 风格或 `sch-<unix>-<rand>`（store 或 handler 生成，保持唯一）。

**测试**（`internal/httpapi/schedule_test.go`，仿 `interaction_test.go` 的 `do/newTestServer`）：
- create→list→get→delete round-trip；create 非法 cron→400；create 非法 agent→400。
- enable/disable 改 enabled；run-now 产生 job 且 next_run_at 不变。

**验收门**：`go test ./internal/httpapi/...` 绿；`go build ./...` 绿。

**commit**：`feat(schedule): P3 /v1/schedules CRUD + run-now (AUTO-02)`

---

## P4 CLI（gofer schedule）

**目标**：`gofer schedule add/list/show/enable/disable/run/rm`，经 HTTP 打 serve（仿 `job` 子命令走 client）。

**改动**：
1. 新文件 `internal/commands/schedule.go`：命令组 `schedule`（别名 `sch`），子命令复用 `bindServerFlags`（连 serve）。
   - `add`：旗标 `--name --cron --catch-up` + **复用 `job run` 的请求旗标**（`-p/-a/--runner/--prompt 或位置 prompt/--cmd/--cwd/--timeout/--title/--tag/--role`）构建 `JobRequest`→POST `/v1/schedules`。
   - `list`：GET→`gookit/cliui` 表格（id/name/cron/next_run(hh:mm:ss)/enabled/last_job）。
   - `show <id>`：GET 详情（含内嵌 request、next/last）。
   - `enable|disable|run <id>`：POST 对应端点。`rm <id>`：DELETE。
2. `internal/commands/`（app 注册处）：把 `schedule` 命令挂到 app（仿 `job`/`wf` 注册）。
3. client（`internal/client/client.go`）：补 schedule 方法（CreateSchedule/ListSchedules/GetSchedule/DeleteSchedule/SetScheduleEnabled/RunSchedule），仿 `AnswerInteraction`/`ListProjects` 的 `doJSON`。

**测试**：client 方法单测（仿 client_test）；CLI 解析/构建请求的单测（如有 job run 旗标构建可复用的测试范式）。

**验收门**：`go test ./internal/commands/... ./internal/client/...` 绿；`go build ./...` 绿；`go vet ./...` 绿。

**commit**：`feat(schedule): P4 gofer schedule CLI + client (AUTO-02)`

---

## P5 收尾 + E2E 冒烟

**目标**：provenance + 文档 + 容器 E2E。

**改动**：
1. provenance：确认触发 job `channel=cron` 全链路可见（show/list 展示）；如 `job list` 需识别 cron channel 无需特殊处理（已是 channel 字段）。
2. runbook/README：`gofer schedule` 用法 + 「勿在 schedule 内嵌密钥」(SR403) 警示。
3. roadmap：AUTO-02 标 ✅ + commit。

**E2E（容器，主控做）**：ephemeral serve（仿 `tmp/ed-e2e/`）：
- `schedule add --cron "*/1 * * * *"` 内嵌一个 exec(echo) job → ≤90s sweeper 触发、`last_job_id` 落库、`next_run_at` 推进未来。
- `disable` 后 ≥2min 不再触发；`enable` 恢复。
- `run-now` 立即产 job 且 next 不变。
- 非法 cron/agent 在 add 被拒。

**commit**：`docs(schedule): P5 provenance + runbook + roadmap AUTO-02 ✅ (AUTO-02)`

---

## 进度跟踪

- [x] P1 数据层（schedules 表 + CRUD + cron next）— `959a2f2`，host codex 实施 + 容器验收全绿
- [x] P2 sweeper（startScheduleLoop + config）— `29b43e7`，host codex + 容器验收
- [x] P3 HTTP `/v1/schedules`（CRUD+run-now）— `3c2f57b`，host codex + 容器验收（校验复用 s.jobs.Validate）
- [x] P4 CLI `gofer schedule` + client — `130fc5f`，host codex(job.go DRY抽取复用) + 容器验收
- [x] P5 收尾 + 容器 E2E 冒烟 — runbook + roadmap✅，**容器 E2E 14/14 全绿**
- [x] roadmap AUTO-02 ✅
