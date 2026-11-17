<!-- template_id: plan; template_version: 1.1.0 -->
# 第 4 批：自主推进 P1 + P2 实施计划

> 状态：Draft 0.1 已批准（2026-11-16，用户：「4 批计划批准」）
> 设计：`docs/design/2026-11-16-plan-driver-autonomy-design.md`（已批准，Q1–Q6 = A）
> issue：gofer-eeod（底座部分）、gofer-jaoy、gofer-n3kw；关联 gofer-5hdh、gofer-ds7i、gofer-zoww（P3 起）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-16 | Claude（gofer 监督会话） | 初稿 |

思考模式：RIGOROUS。核心目标：设计 P1（信号与底座）与 P2（推进引擎只记录模式）落地并通过真实验收。Scope freeze：只实施设计 P1、P2 与下文「对设计的细化」；决策点、决策卡、assist / auto 的执行属于 P3。Review budget：计划不另起评审轮，用户批准即开工；实施审查每条链一轮，有阻塞问题才返工一次。

## 目标与完成定义

- P1：job 可自报终态并在 CLI / web / API 可见；`gofer job diagnose` 可用；交付核验的执行改走统一一次性调用器，每日限额持久化；plan 推进收拢为一个事件入口，serve 重启后补推进。现有推进行为不变（除重启补推进这一修复）。
- P2：`plan drive on` 以 `record` 等级启用后，推进引擎对每个推进事件算出「本来会做的动作」并记录（不执行）；记录与随后实际发生的处理对比，`gofer plan drive report` 给出一致率；统一预算按 plan 计数并在记录里标出「本会暂停」。
- 单测与反向验证通过；gofer-verify 四路通过；合入 main、关 issue。

## 范围、排除项与授权

范围：设计 §1（推进引擎的状态、规则层、放权等级中的 `record`、动作白名单与风险、统一预算）、§3（自报终态的接收与展示）、§6（job 诊断）、§2 中「统一一次性调用器」与交付核验收编、§8 中管家 observe 的「只记录」思路（推进引擎自有记录表，不改管家）。

排除：决策点 agent 调用与 `decision/v1`（P3）；决策卡与「交给它决定」（P3）；`assist` / `auto` 的实际执行（P3，本批只允许设置 `record`，其余等级被拒并提示「后续版本」）；缺失终态时的续接一次（P3，本批只记录「缺失」）；记忆综合 `JobOneShot` 迁到统一调用器（P3 决策点落地时一起迁，本批不动）；leader 收编（P4）。

授权：批准后按功能点本地提交、建 worktree、派 sonnet 子 agent 与 suga job、跑主机 job 与临时 server。push 被拦时向用户申请；发版另行请示。

## 已确认事实（main a3c184d2 之后，调研记录）

- 推进只有 `advancePlan`（`job/planadvance.go:161`）一处，调用方：`RunPlan` / `ResumePlan` / `PlanTodoChanged`（同文件 :88 / :123 / :155）与 `linkTodoOutcome`（`job/todolink.go:104`，被 `execute.go:879,955`、`hold.go:768`、`review.go:170` 调用）；`maybeBlockPlan`（:231）被 `execute.go:988`、`hold.go:770`、`review.go:175` 调用；`PlanTodoChanged` 另被 `todo_completion.go:129`、`worktree.go:771`、`httpapi/plan_handler.go:1188`、`mcpserver/backend_local.go:573` 调用。**没有后台扫描，serve 启动不补推进**；唯一周期循环是 leader wake（`serve/serve.go:1019`）。
- 交付核验：`serviceChecker.RunCheck`（`job/delivery_check.go:437`）自己 `Submit` 只读无工具 job、`WaitFor`、`TailLog`；每日限额 `allowCheckToday`（:264）按进程内存计数，重启清零；续接计数在 `jobs.check_resume_count`。
- bgagent：`Guard`（`bgagent/runner.go:355`）从 `background_runs` 表读计数（持久化），硬停止写 `audit_events`；`Run`（:436）+ `Executor`（:76）；`KindJobCheck` 已声明但生产代码无人用。
- 管家 observe 记的是被拦下的原始 HTTP 写请求（`steward_observations`），没有语义化动作；推进引擎不复用这张表。
- job 内 MCP：完整面在 `mcpserver/server.go`（`addGoferTool`），leader / steward 各有收窄面（:1916、`steward.go:124`）；工具元数据 `mcptools/tools.go:59`；job 凭据写白名单 `httpapi/jobcredential.go:195`。
- 失败相关：`jobs.failure_class`（`transient` / `other` / `budget` / `caller-disabled` / `worktree-gone`）、`stop_reason`、`wrap_up`、`session_end_reason`、`auto_resume_attempt`、`check_resume_count`、`job_retries` 表；stall 以 `stalled:` 前缀的错误标记。**没有统一终止原因，也没有诊断命令。**
- 已有 `plan_decisions` 表与 `gofer plan decisions` 子命令（plan ask / answer 的问答），**新表与新命令不得用这个名字**。
- 迁移：无版本号，`schemaStmts` + `init()` append 新表 + `migrate()` 幂等加列；最新 fixture `testdata/schema-v0.214.1.sql`。
- 子包范本：`job/workflow`（`WorkflowAdvancer` 在 `job/service.go:363`，`JobOps` 在 `workflow/engine.go:14`，装配在 `core/core.go:391`）。

## 对设计的细化

| # | 问题 | 定案 |
|---|---|---|
| R1 | 推进事件入口 | `job` 包内 `PlanEvent{PlanID, TodoID, JobID, Kind}` 与 `s.emitPlanEvent(ev)`，采用**预写 + 同步执行**：先插入 `plan_events` 行，再在调用方原处同步执行推进（按 plan 加锁串行，同一 plan 同时只有一个处理者），成功后删行。上面列出的 `advancePlan` / `maybeBlockPlan` 调用点全部改走它，时序与现在相同（不改成异步，现有用例的「完成后立即推进」断言保持成立）；进程在推进中途退出时残留的行由 R2 重放 |
| R2 | 补推进 | serve 启动后与每 60 秒扫描：`plan_events` 残留行重放；另对每个 open、未暂停、非 blocked 的 plan 投递一次 `Kind=sweep`。处理必须幂等：`SetTodoReadyIfPending` 与 `MaybeDispatchTodo` 已有状态判断，T1 补用例证明重复事件不重复派发 |
| R3 | driver 子包 | `internal/job/driver`：`job` 经 `PlanDriver` 接口（`OnPlanEvent(ctx, PlanEventView)`）回调，driver 经 `HostOps`（读 plan / todo / job、写记录）取宿主能力；装配在 `core`，模式同 `job/workflow`。driver 关闭的 plan 不调用 driver，现有推进照旧 |
| R4 | driver 状态 | 表 `plan_drivers(plan_id PK, level, budget_json, spent_json, enabled_at, updated_at)`；`level` 只接受 `record`（本批），`assist` / `auto` 返回 400「后续版本支持」。`gofer plan drive on <plan> [--level record] [--max-jobs N --max-rework N --max-hours N]`、`off`、`status`；HTTP `PUT/DELETE/GET /v1/plans/{id}/driver`（G056 登记，权限沿用 plan 写 / 读） |
| R5 | 动作记录 | 表 `plan_driver_actions(id, plan_id, todo_id, job_id, event_kind, action, params_json, risk, rule, would_pause, outcome, matched_at, created_at)`；`action` 枚举：`dispatch_todo` / `complete_todo` / `resume_with_notes` / `retry` / `block_plan` / `escalate`（规则无法判断，P3 起成为决策点）/ `pause_budget`；`risk` = `low` / `medium` / `redline`，取自代码内固定表 |
| R6 | 规则层（record 下只算不做） | 输入：事件 + plan / todo / job 当前状态 + 自报终态 + 核验结论 + 诊断。规则按设计 §1 四条；自报 `human` → `escalate`；自报 `fail` 而 exit 0 → `escalate`（P3 再改为失败处理）；自报 `rework` → `resume_with_notes`（受返工预算）；缺失自报 → 照退出码与核验；`transient` 失败 → `retry`；其余失败 → `escalate`。每条记录带命中的 `rule` 名 |
| R7 | 一致率 | 记录后观察实际发生的处理：同一 todo 在 24 小时内的下一次状态变化或新 job（派发、完成、续接、重试、跳过、blocked）。动作与之对应 → `outcome=match`，不同 → `mismatch`，24 小时无变化 → `no_followup`。`gofer plan drive report <plan>` 按 action 与 rule 汇总 match / mismatch / no_followup 与一致率；设计 Q5 的「连续 20 次 ≥ 90%」在报告里按 action 给出「是否达到建议升级条件」，本批只显示不升级 |
| R8 | 统一预算 | 每个推进事件按 plan 计数已用 job 数（plan 内 todo 派出的 job，含续接与重试）、返工轮数（`resume_with_notes` + 交付核验续接）、时长（启用至今）；超过即记录 `would_pause=true` 的 `pause_budget` 动作。record 下不暂停 |
| R9 | 自报终态 | MCP 工具 `gofer_submit_result {decision: pass\|rework\|fail\|human, reason, action_required?, findings?[]}`：完整面与 leader 面都注册（steward 面不注册）；落 `jobs` 新列 `result_decision`、`result_reason`、`result_json`、`result_attempt`；同一 job 同一 attempt 第二次提交返回错误。HTTP `POST /v1/jobs/{id}/result`（`JobCred` 只允许本 job，加入 `jobWriteAllowlist`）。CLI `gofer job show` / web job 详情显示。prime 注入一行通用说明（G046，中英各一份按现有注入语言） |
| R10 | 终止原因与诊断 | `job.TerminationReason(job) string` 纯函数：由 `status` / `failure_class` / `stop_reason` / `wrap_up` / `session_end_reason` / `stalled:` 前缀 / 取消来源推导，枚举 `ok` / `agent_error` / `transient` / `stalled` / `timeout` / `budget` / `cancelled` / `worktree_gone` / `caller_disabled` / `session_ended` / `unknown`，不加列。`gofer job diagnose <id>`：终止原因、续跑链（`resumed_from` / `auto_resumed_by` / `fell_back_*` / `job_retries`）首错、阶段时间线（排队、开始、首个输出、最后输出、结束，取自现有事件与日志修改时间）、证据 / 未知 / 下一步建议（确定性文案）。HTTP `GET /v1/jobs/{id}/diagnosis` |
| R11 | 统一一次性调用器 | 新包 `internal/oneshot`：`Invoker.Run(ctx, Request{Kind, Agent, Prompt, Input any, OutputSchema, Limits}) (Result, error)`，内部用 bgagent `Guard` + `Run`，执行器是只读无工具 job（复用 `checkAgentArgs` 的 argv 规则，迁入本包）。交付核验改用它（`KindJobCheck`），每日限额改由 `background_runs` 持久化计数；`DeliveryChecker` 接口与现有行为（续接次数、needs_review 转向、文案）不变 |
| R12 | 记录可见 | web plan 详情加「推进记录」页签（driver 状态、预算、动作列表与 outcome、一致率汇总）；plan 列表对启用 driver 的 plan 显示等级徽标；job 详情显示自报终态与「诊断」按钮（调用 R10 接口） |

## 分工

按用户要求，sonnet 只做出错代价最高的部分：推进事件入口与补推进（并发、幂等，T1）、规则层与预算（T5）、自报终态的凭据路由与 MCP（鉴权，T2）。其余派 suga，模型 `opencode-go/deepseek-v4.1-flash`。

## 前置与 fail-closed 条件

- T0（Claude）：主机建 worktree `.worktrees/b4`（`feat/b4`，集成）、`.worktrees/b4-a`、`.worktrees/b4-b`、`.worktrees/b4-c`，基于当时的 main，相对 gitdir，`web/node_modules` 链接主 checkout；先提交 tracker。
- Go 命令：`GOWORK=off`，`GOCACHE` 在 `tmp/_cache/<名>`，`TMPDIR=/tmp/gofer-gotmp/<名>`。
- fail-closed（出现即停，回报）：基线相关包测试在未改动时失败；事件化后任何现有 plan 推进用例需要改断言（说明行为变了）；record 等级需要执行任何动作；需要设计未列的权限。

## 与第 5 批并行的共享文件

第 5 批（web 会话体验）同期在另外的 worktree 实施。共享点：`mcptools/tools.go` 与 `mcpserver/server.go`（两批各加工具）、`internal/i18n/lang`、web 的 job 详情。约定：各自只追加，不重排；后合并的一方在合并前 rebase 并跑两侧用例。第 4 批 P1 + P2 不新增「等我」卡片。

## 波次与依赖

```
W0  T0 前置（Claude）
W1  [b4-a] T1 推进事件入口与补推进（sonnet A）
    ‖ [b4-b] T2 自报终态 → T3 统一一次性调用器与核验收编（sonnet B，串行）
    ‖ [b4-c] T4 终止原因与 job diagnose → T6 driver 状态表与 plan drive 命令（suga，串行）
W2  审查三条链；合并 → b4（Claude，--no-ff）；b4 上相关包测试
W3  [b4] T5 driver 子包：规则层、动作记录、一致率、预算（sonnet A）
W4  [b4] T7 web（suga）→ T8 文档（suga）
W5  T9 真实验收、反向验证、gofer-verify、合并收尾（Claude）
```

共享文件负责方：`jobstore` 迁移各加各的表与列、迁移函数分文件；`httpapi/authz_routes.go`——B 加 `/jobs/{id}/result`，C 加 `/jobs/{id}/diagnosis` 与 `/plans/{id}/driver`；`internal/job/service.go` 注入点——A 加事件与 `PlanDriver` 接口，T5 装配；`core/core.go`——T5 装配 driver、T3 装配 invoker。

## 任务

| 任务 | 执行方 | 内容 | 验收 |
|---|---|---|---|
| T0 | Claude | worktree、tracker 提交 | — |
| T1 | sonnet A | R1、R2 | 现有 plan 推进用例不改断言全部通过；重复事件不重复派发；serve 重启（测试里重建 Service）后残留事件与 sweep 推进到下一项；同一 plan 并发事件串行 |
| T2 | sonnet B | R9 | 只允许本 job 凭据；同 attempt 第二次被拒；leader 面可用、steward 面没有；`job show` 显示 |
| T3 | sonnet B | R11 | 交付核验现有用例不改断言通过；重建 Service 后每日计数仍在；硬停止写审计 |
| T4 | suga | R10 | 各终止原因各一条用例（表驱动）；续跑链首错；CLI 与 HTTP |
| T5 | sonnet A | R3、R5–R8 | record 下任何事件都不改变 plan / todo / job 状态（专门用例：开 driver 与不开 driver 跑同一序列，最终状态逐字段相同）；每条规则一条用例；一致率三种 outcome；预算超限只记录 |
| T6 | suga | R4 | `assist` / `auto` 被拒；预算参数解析；G056 登记与 `go generate` |
| T7 | suga | R12 | `vue-tsc`、`vitest`、`vite build` 通过（主机跑） |
| T8 | suga | 文档：gofer-usage references（plan、job）、`docs/reference/http-api.md`、CHANGELOG「未发布」、设计稿实施记录 | 删除行审查、业务名检查 |
| T9 | Claude | 真实验收、反向验证、`git diff --stat main...feat/b4`、私有名检查、gofer-verify、主机合并、合并确认后清理、关 issue、遗留另立 issue | — |

## 真实验收（T9）

主机临时 server（独立配置目录与端口），一个三项串行 plan（todo 由 suga job 执行，任务很小）：

1. `plan drive on`（record）→ `plan run`；第一项完成后核对动作记录有 `complete_todo` 与 `dispatch_todo`，且 plan 实际推进与未开 driver 时相同。
2. 第二项运行中停掉临时 server、等该 job 在 worker 上结束、再启动：60 秒内第三项被派发（补推进）。
3. 第三项让 agent 调用 `gofer_submit_result decision=human`：job 详情显示自报终态，动作记录为 `escalate`。
4. 一个人为失败的 job：`gofer job diagnose` 给出终止原因、时间线与下一步。
5. 交付核验：开启核验跑一项，重启 server 后 `background_runs` 计数仍在。
6. `gofer plan drive report` 输出一致率；web「推进记录」页签与 CLI 一致。

## 反向验证

去掉启动扫描 → 重启补推进用例失败；去掉 attempt 去重 → 二次提交用例失败；让 record 执行一次派发 → 「开与不开最终状态相同」用例失败；核验每日计数改回内存 → 重启后计数用例失败；去掉按 plan 串行 → 并发用例失败。破坏须能编译（筛 `[build failed]`）。

## 风险与回退

- 事件化改动推进主路径：T1 以「现有用例不改断言」作为闸门，并在 b4 上跑 job 包全量 race。
- 重复派发：处理幂等 + 按 plan 串行，反向验证覆盖。
- 记录表增长：`plan_driver_actions` 按 plan 保留，plan 归档时删除；`plan_events` 处理后即删。
- 回退：新表与新列旧版本忽略；driver 默认关闭；事件入口是行为等价改造，回退版本即回到同步推进。
