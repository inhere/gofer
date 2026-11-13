<!-- template_id: plan; template_version: 1.2.0 -->
# 第 2 批：定时与巡检少打扰 实施计划

> 状态：Draft 0.1 已批准（2026-11-13，用户：「计划 0.1 批准」）
> 设计：`docs/design/2026-11-12-schedule-health-and-quiet-checks-design.md` Draft 0.2（已批准，Q1–Q8=A）
> issue：gofer-ss3b、gofer-vqoe、gofer-2a86、gofer-4ymk、gofer-qm3l、gofer-fgwc、gofer-oby3、gofer-j01b

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-13 | Claude（gofer 监督会话） | 初稿 |

思考模式：RIGOROUS。核心目标：设计 §1–§7 落地并通过真实验收。Scope freeze：只实施设计 0.2 与下文「对设计的细化」。Review budget：按「治理与改动成比例」，计划不另起评审轮，用户批准即开工；实施审查每条链一轮，有阻塞问题才返工一次。

## 目标与完成定义

- 设计「验收」各条单测通过；真实验收五个场景通过；反向验证四项按预期失败；gofer-verify 四路通过；合入 main、关 issue。

## 范围、排除项与授权

范围：设计 §1–§7。排除：设计「非目标」；`docs/capabilities.md` 随发版更新。

授权：批准后按功能点本地提交、建 worktree、派子 agent 与 suga job、跑主机 job 与临时 server。push 被拦时向用户申请；发版另行请示。

## 对设计的细化

| # | 问题 | 定案 |
|---|---|---|
| R1 | 终态钩子 | 新文件 `internal/job/schedule_health.go` 的 `scheduleOutcome(snap)`，在 `linkTodoOutcome` 的四个调用点旁各加一行（`execute.go:871`、`:946`、`hold.go:763`、`review.go:170`）。T1 先列出 `finish` 中不经这四处的终态返回（关机回收、worker 丢失、恢复等），逐个补调用或在汇报中说明为何不需要 |
| R2 | 计数 SQL | jobstore 新函数 `IncScheduleRun(id) (count, max int, err)`、`IncScheduleFailure(id) (failures, max int, err)`、`ResetScheduleFailures(id)`，单条 `UPDATE … RETURNING`；暂停用 `PauseSchedule(id, reason)`，条件 `enabled=1`（只暂停一次、只发一次事件） |
| R3 | 叠跑查询 | jobstore `ScheduleHasActiveJob(scheduleID) bool`，状态集合写成常量并有测试；用 `schedule_id` 索引 |
| R4 | 扫描与检查池 | job 层 `ScheduleDispatcher`（新文件 `internal/job/schedule_dispatch.go`）：`Fire(record, trigger)` 做叠跑判定 → 前置检查（池，并发 4，同一 schedule 单飞）→ 提交；serve 的 `submitDueSchedule` 改为调用它（serve 只留调用行），立即运行与 webhook 的 handler 也经它判叠跑。`sweepSchedules` 的 CAS 推进与 `once` 路径不变 |
| R5 | 检查环境 | 以 server 进程环境为底，剥离全部 `GOFER_*` 与配置中所有 `*_env` 引用的变量名（server token、worker token 等），再叠加 schedule 参数；`exec.CommandContext` 直接执行 argv，不经 shell；stdout / stderr 各限 4KB 尾部 |
| R6 | 「等我」卡片 | today 卡片 kind `schedule.paused`，key `schedule.paused:<id>`，只在 `paused_reason=failures` 且仍停用时出现；inbox 类 `failed`；动作 `enable` / `open`；启用后消失。通知目录登记 `schedule.paused` 默认开 |
| R7 | 上下文段 | `JobRequest.ScheduleContext *ScheduleContext \`json:"-"\``；T3 找到 prompt 拼接处（与续跑提示同处）在前面加段，模板 `internal/i18n/prompts/{zh-CN,en}/schedule-context.md.tmpl`；上一轮回复末尾优先取 `turns.jsonl` 最后一回合，否则 stdout 尾部；掩码用本 job 的授予值 |
| R8 | 退出码 | 若 `internal/commands` 已有带退出码的错误类型则复用，否则新增一个并在入口统一处理；不在命令内 `os.Exit` |
| R9 | worker 互顶接缝 | 照 `Hub.SetPresenceObserver` 的模式加 `SetReplaceObserver(fn)`，在 `Put` 返回旧连接且 `instance_id` 不同的分支调用；serve 接线处写日志与事件（scope `worker:<id>`，照 `ConfigEventScope` 的做法）；`worker show` 显示最近一次被顶替（若需新读接口，按 G056 登记） |
| R10 | `gone` | `WorktreeStatus` 新状态值 `gone`；T6 列出 HTTP / CLI / Web 消费处并同步（Web 归 T8） |
| R11 | 新建 / 旧行默认 | 迁移给已有行写 `overlap=allow`、`max_failures=0`、`context=off`；创建接口未传时取 `skip` / 3 / `on` |

## 前置与 fail-closed 条件

- 主机建三个 worktree：`.worktrees/b2`（`feat/b2`，集成）、`.worktrees/b2-a`（`feat/b2-a`，A 链）、`.worktrees/b2-b`（`feat/b2-b`，B 链），基于当前 main，`.git` 改相对 gitdir，`web/node_modules` 链接主 checkout。
- Go 命令：`GOWORK=off`，`GOCACHE` 在 `tmp/_cache/<名>`，`TMPDIR=/tmp/gofer-gotmp/<名>`（容器本地，须在任何 git 仓库之外；不得放在 `/d/work/inhere` 下）。
- fail-closed（出现即停，回报）：基线 `go test ./internal/job ./internal/jobstore ./internal/serve ./internal/wshub ./internal/commands -count=1` 未改动时失败；需要改 worker 协议，或需要设计未列的权限。

## 波次与依赖

```
W0  T0 前置（Claude）
W1  [b2-a] T1 运行健康 → T2 前置检查 → T3 跨轮上下文（sonnet A，串行）
    ‖ [b2-b] T4 serve status → T5 worker 互顶 → T6 残留目录 → T7 掩码（sonnet B，串行）
W2  审查两条链；合并 b2-a、b2-b → b2（Claude，--no-ff）；b2 上相关包测试
W3  [b2] T8 web → T9 文档（suga，依次）
W4  T10 真实验收、反向验证、gofer-verify、合并收尾（Claude）
```

共享文件负责方：`internal/job/model.go` 事件常量——schedule 事件归 A、`worker.replaced` 归 B；`internal/notify/catalog.go` 同；`internal/httpapi/authz_routes.go` 与 authz catalog 只有 A 改；`internal/serve/serve.go`——A 改调度调用行、B 改 hub 接线行；`internal/i18n/lang` 各加自己的词条。

委派约束（写入任务书）：G021–G024 分层；G031 / G046；超过 1000 行的文件只加调用行或字段，新逻辑进新文件；G051–G053 测试规则；G056 / G057；只 `git add` 本任务文件，提交前查 `git diff --cached --stat`；不删注释、不做无关重排；不在导出了 `GIT_DIR` 的 shell 里跑测试；不起空等的后台任务。

## 任务

### T0 前置（Claude）
建 gofer plan 与 T1–T10 todo；提交 tracker；建 worktree；基线测试；写两份 sonnet 任务书与 suga 任务书。

### T1 运行健康（sonnet A，b2-a）
- 设计 §1、R1–R3、R6、R11：jobstore 迁移与函数、`schedule_id`、叠跑判定、计数、暂停与事件、今天卡片、CLI / HTTP 字段、`schedule list/show/events` 显示。
- 验证：设计 §1 单测全部；`go test ./internal/jobstore ./internal/job ./internal/serve ./internal/httpapi ./internal/commands ./internal/today ./internal/inbox -count=1`。

### T2 前置检查（sonnet A，b2-a）
- 设计 §2、R4、R5：`ScheduleDispatcher`、检查池、结局、指纹、权限 `schedule.precheck`（routeDecls、catalog、`go generate ./internal/httpapi/...`）。
- 验证：设计 §2 单测全部；同上测试包加 `./internal/authz`。

### T3 跨轮上下文（sonnet A，b2-a）
- 设计 §3、R7：环境变量、模板变量、上下文段（中 / 英）、`context` 字段。
- 验证：设计 §3 单测全部；`go test ./internal/job ./internal/i18n/... -count=1`。

### T4 serve status（sonnet B，b2-b）
- 设计 §4、R8。验证：五种退出码、`--json`、doctor 不变；`go test ./internal/commands -run 'Serve|Doctor' -count=1` 后全包。

### T5 worker 互顶（sonnet B，b2-b）
- 设计 §5、R9。验证：设计 §5 单测；`go test ./internal/wshub ./internal/worker ./internal/serve -count=1`。

### T6 残留目录（sonnet B，b2-b）
- 设计 §6、R10。验证：设计 §6 单测；`go test ./internal/job -run 'Worktree|Retire|Prune' -count=1` 后全包。

### T7 掩码（sonnet B，b2-b）
- 设计 §7。验证：设计 §7 单测（跨截断点、跨两次写入）；`go test ./internal/job ./internal/runner/... -count=1`。

### T8 web（suga，b2）
- 定时任务列表 / 详情 / 编辑：`overlap`、`max_runs`、`max_failures`、`context`、前置检查（argv、超时、无变化跳过；无权限时只读）、运行次数与暂停原因；事件标签 `schedule.run_skipped`、`schedule.paused`、`worker.replaced`；今天卡片 `schedule.paused` 文案与动作；worktree `gone` 显示。
- 验证：主机 `npx vue-tsc --noEmit && npx vitest run && npx vite build`。

### T9 文档（suga，b2）
- 设计「验收」文档清单全部与 `CHANGELOG.md`（含「行为变化」：新建 schedule 的三个默认值）。G046：不写 issue id、本机路径。
- 验证：私有名检查；我审删除行。

### T10 真实验收、反向验证与收尾（Claude）
- 真实验收：设计「验收」中的五个场景，记录 `tmp/b2-impl/acceptance.md`。
- 反向验证（副本，先确认能编译）：去掉叠跑判定、去掉掩码、去掉失败清零、去掉环境剥离，对应用例必须失败；记录 `tmp/b2-impl/reverse.md`。
- 收尾：`git diff --stat main...feat/b2` 查无关删除；私有名检查；gofer-verify 四路；在主机合并到 main（`--no-ff`），确认合并成功后删 worktree 与分支；设计稿实施记录；关 issue；遗留另立 issue。

## 回滚与恢复

单独回退合并提交；新增列与 `schedule_id` 对旧版本无害。

## 人工 Gate

本计划批准后开始 T0。push 与发版另行请示。

## 可追溯性

| 设计项 | 任务 | 验证 |
|---|---|---|
| §1 运行健康 | T1、T8 | T1 用例、T10 验收与反向 |
| §2 前置检查 | T2、T8 | T2 用例、T10 验收与反向 |
| §3 跨轮上下文 | T3 | T3 用例、T10 验收 |
| §4 serve status | T4 | T4 用例、T10 验收 |
| §5 worker 互顶 | T5、T8 | T5 用例 |
| §6 残留目录 | T6、T8 | T6 用例 |
| §7 掩码 | T7 | T7 用例、T10 反向 |
| 文档 | T9 | 私有名检查 |

## 完成 Gate

T10 完成即本计划完成；发版另行进行。
