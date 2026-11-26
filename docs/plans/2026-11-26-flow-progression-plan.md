# 二期计划：流程推进（stage）

> issue：gofer-0py5（二期）；相关 gofer-jaoy（agent 自报终态，P1 已合入）
> 设计：`docs/design/2026-11-18-resident-sessions-and-team-design.md` §6（方向已批准，Q2 / Q3 按推荐）
> 状态：**已批**（2026-11-26 用户定：命令名 `stage`、默认开启，其余按推荐；见 §4）

## 0. 计划级决定

1. **流程挂在工作项上**（设计 §6.2）。一个工作项最多有一个进行中的流程。`gofer stage start <issue>` 会找到这个 issue 已关联的工作项，没有就新建一个并关联，再把当前会话设为工作项的 current 会话。流程不挂在 seat 上：seat 是人手，工作项是事；seat 名册显示的流程步骤，经「seat 当前会话 → 该会话 current 的工作项」取得。
2. **门禁只读存储事实和 git**，不读模型正文。
   - 判不了的门禁（项目只在 worker 上、server 跑不了 git）记为「需要人确认」，进「等我」，不放行，也不报错。
   - 门禁不过时，命令返回缺什么，流程停在原步。
3. **只有显式动作才推进**：agent 执行 `gofer stage done` / `back` / `blocked`，或人在「等我」/ web 上批准。一轮结束本身不推进。
4. **放权先只做两档**：`record`（只记录本来会做什么）和 `remind`（下一步该 agent 做时，给会话送一轮「继续下一步」）。`auto` 不做。合并到 main、push、发版永远要人。一致率的统计方法与 plan 推进引擎相同（match / mismatch / no_followup，最近 20 条），只出报告，不自动升档。
5. **拆两步**。2a 是数据、门禁、CLI、每轮注入（放权只有 record）；2b 是 remind、「等我」卡、web。2a 发版后在本工作区用一段时间，看注入和门禁是否顺手，再做 2b。
6. **默认开启，可按项目关闭**：`projects.<key>.stage.enabled` 默认 true，设为 false 后该项目没有 SessionStart 提示与「没开流程就建 plan」的提醒。已开始的流程不受这个开关影响，照常注入。

## 1. 现状要点（代码摸底 2026-11-26，位置见附录 A）

- **工作项**
  - 表 `work_items` 与 `work_item_sessions`（current / past）、`work_links`（只有 issue / plan / todo / job 四种）、`work_journal`（只追加）。
  - 状态有 active / needs_me / review / parked / done 等。
  - 不能关联设计稿，也没有 flow 字段。
- **plan 推进引擎**（`internal/job/driver`）
  - 等级名是 `record` / `assist` / `auto`，目前只开放 record；动作记在 `plan_driver_actions`，一致率报告已有。
  - 引擎没有往会话送话的能力。现成的入口是 `job.SaySession`、`work.AskSession`、`sessionrelay.SendMessage`。
- **每轮注入**
  - 终端：UserPromptSubmit 的上下文按 catchUpJobs → catchUpMail → 记忆的顺序拼接；数据来自心跳返回的会话视图，seat 信息就是这样带下来的。
  - ACP：每轮有 `TurnNote`（作为前缀），目前只在本机 submit / recovery 时接线，worker 路径未核实。
- **门禁可用的数据**
  - `project.runGitRO`：在项目的 ExecPath 下只读执行 git，只适用于主机上的项目。
  - plan 的 todo 状态（skipped 也算完成）。
  - workflow 有 `template` 列，step job 记了 `base_sha`。
  - 「某提交或其后代上验证通过」没有现成查询。
- **自报终态**：job 已有 `result_decision`（pass / rework / fail / human，由 `gofer_submit_result` 写入）。
- **「等我」卡由数据源派生**：照 `SeatScheduleCards` 新写一个数据源，在 `signalsOf` 里映射分类即可。
- **命名**：没有 `flow` 命令、路由或表；`wf` 是 workflow 的别名；seat 的章程模板也叫 `flow`。

## 2. 任务

| # | 内容 | 执行方 | 依赖 | 步 |
|---|---|---|---|---|
| F0 | 真机核实：worker 上的 ACP 会话能不能拿到 TurnNote；suga 会话走哪条注入路径；本工作区的验证模板名 | 我 | — | 2a |
| F1 | 数据层、服务、门禁求值、HTTP | sonnet | F0 | 2a |
| F2 | CLI `gofer stage`，以及建 plan / 派 job 时的提醒 | suga | F1 | 2a |
| F3 | 每轮注入（终端 + ACP）与 SessionStart 提示 | suga | F1 | 2a |
| F4 | remind 档、「等我」流程卡、一致率报告 | sonnet | 2a 发版 | 2b |
| F5 | web：工作项流程条、会话与名册显示步骤、就地批准 | suga | F4 | 2b |
| F6 | 合并、verify、真机验收、文档 | 我 | 各步 | 各步末 |

### F1 数据层与门禁（sonnet）

- **表**
  - `work_stages`：work_item_id（主键）、template、steps_json（开始时把模板快照下来，改模板不影响进行中的流程）、step、step_entered_at、level、status（active / blocked / done / dropped）、blocked_reason、doing_done_sha（doing 完成那一刻主仓库的 HEAD）、created_at、updated_at。
  - 步骤变化与门禁结果写进 `work_journal`，kind 为 `stage`，不另建事件表。
  - `work_links` 加一种类型 `doc`（设计稿或计划文档的仓库相对路径，带 role：design / plan）。
- **内置模板**（设计 §6.1）
  - `feature`：design → review → plan → doing → verify → done
  - `small`：doing → verify → done
- **门禁**（`internal/stage` 包求值；依赖方向是 stage → jobstore / project / tracker，不反向）

  | 步 | 放行条件 |
  |---|---|
  | design | 存在 role=design 的 doc 链接，该文件已在主分支提交（`git log -1 -- <path>` 非空），前 20 行含工作项所关联的 issue id |
  | review | 人在「等我」或 web 上批准；或者有关联到工作项的评审 job，其 `result_decision=pass`。评审轮次上限默认 2（取自模板），超过后只能由人批准 |
  | plan | 工作项关联了 plan，且 plan 至少有一条 todo；模板要求计划文档时，role=plan 的 doc 也须已提交 |
  | doing | 关联 plan 的 todo 全部 done 或 skipped；放行时记下 doing_done_sha |
  | verify | 有一次 workflow 运行，template 在 `projects.<key>.stage.verify_templates` 中（默认 `[verify]`），状态为 done，开始时间晚于 doing 完成时间，且其 base_sha 等于 doing_done_sha 或是它的后代（用 `merge-base --is-ancestor`） |
  | done | 无门禁，不改 issue 状态（关 issue 留给人或 agent 自己） |

  每条门禁返回 `{ok, missing[], needs_human}`，missing 的写法要能直接告诉 agent 下一步做什么。
- **服务**：Start、Done（求值当前步门禁，过了就进下一步）、Back（回到上一步，必须带理由）、Blocked（标阻塞并带理由，再执行 done 时解除）、Approve（只有人能调）、Drop、Status。
- **HTTP**：`/v1/work-items/{id}/stage`（GET / POST start / POST done|back|blocked|approve|drop），在 `authz_routes.go` 中声明，approve 只认人的凭据。跑 httpapi 全包。
- **用例**：每条门禁的过与不过、判不了时 needs_human、模板快照、back / blocked 的往返、并发 done 只推进一次。

### F2 CLI（suga）

- 命令：`gofer stage start <issue> [--size feature|small] [--work <id>]`、`status`、`done`、`back --reason`、`blocked --reason`、`ls`（列出本项目进行中的流程）。默认作用于当前会话 current 的工作项；也可以用 `--work` 指定。
- 加入 G033 顶级命令白名单；命令结构照 `internal/commands` 的现有写法。
- 提醒：在开启了 stage 的项目里，当前会话没有进行中的流程时，执行 `gofer plan create` 或派发实施 job 会在输出末尾提示一次「实现类工作先 `gofer stage start`」。同一会话只提示一次，不拦截。
- seat 章程模板名 `flow` 不变（命令叫 `stage`，不再冲突）。

### F3 注入（suga）

- 终端：会话视图带上 `stage`（工作项 id、当前步、门禁缺项摘要，限 300 字节）。hookrelay 在 UserPromptSubmit 的上下文末尾追加一行流程提醒：当前步、完成后执行的命令、`back` / `blocked` 的用法。
- ACP：通过 TurnNote 带同样一行。worker 路径若拿不到，按 F0 的结论补上接线或写进遗留。
- SessionStart：在开启了 stage 的项目里，给没有流程的会话一句「实现类工作先开流程」。seat 会话把这句放进 `seat.Section`，其他会话照 `topicReminder` 的写法加。
- 用例：有流程和无流程的两种注入、长度上限、未开启的项目不提示。

### F4 remind、「等我」卡、报告（sonnet，2b）

- 放权等级用 `gofer stage level record|remind` 设置。两档都在步骤变化时把「本来会做的动作」写进 journal。remind 档另外在下一步属于 agent 时，用系统前缀 `[gofer · 流程]` 给工作项的 current 会话送一轮「继续下一步：<步骤>」。会话离线时，复用 seat 定时补发的那套队列。
- 「等我」卡：review 等人批准（归 approval 类，动作为批准或附意见退回）、门禁判不了需要人确认、verify 失败（归 attention 类）。卡片从 `work_stages` 派生，推送沿用一期通知那条路径。
- 报告：`gofer stage report` 输出一致率，口径同 plan 推进引擎。

### F5 web（suga，2b）

- 工作项详情页加步骤条：当前步、缺项、journal 时间线，以及批准 / 退回按钮。
- 工作台的会话线程头和 seat 名册行显示当前步。
- 弹卡对 review 卡可以就地批准。

### F6 验收（我）

- 2a：在本工作区，用一个真实的小修复走一遍 `small` 流程，再拿一个中等功能走 `feature` 的 design → plan 两步。核对：门禁缺项的提示、终端注入、ACP 注入、没开流程就建 plan 时的提醒。
- 2b：remind 档自动送「继续」、离线补发、review 卡批准、verify 失败卡、跨浏览器弹卡。

## 3. 反向验证

- 每条门禁都要有「故意不满足 → 不放行，且缺项写对」的用例。
- 把 verify 门禁的 ancestor 判断改为恒真，验证用例会失败；破坏后须能编译。
- 注入用例断言提醒行在上下文的末尾，且未开启的项目没有。

## 4. 决策（2026-11-26 用户定）

- **Q1 命令名**：`gofer stage`（用户：convention 太长不好记，取 stage / pipeline；选 stage——pipeline 易与 workflow / CI 混淆）。代码包 `internal/stage`、表 `work_stages`、路由 `/v1/work-items/{id}/stage`；设计稿里的 `gofer flow` 按此读。seat 章程模板 `flow` 不改名。G033 顶级白名单加 `stage`。
- **Q2 开启方式**：默认开启，项目可设 `stage.enabled: false` 关闭。
- **Q3**：保留「设计稿前 20 行含 issue id」。
- **Q4**：按项目配置 `verify_templates`（默认 `[verify]`），本工作区 gofer 项目填 `gofer-verify`。
- **Q5**：拆 2a / 2b，2a 发版试用后再做 2b。

## 5. 不在二期

- `auto` 档（自动派验证）；任意 DAG 模板；工作区自定义模板（2b 之后再看是否需要）。
- Team（三期）。
- 按 gofer-jaoy P3 改 job 结局（「缺终态时续会话补交」）。

## 附录 A 代码位置（摸底，2026-11-26）

- 工作项：`internal/jobstore/store.go:986-1095`（表），`internal/jobstore/work.go:17-35,104-109`（状态与 link 类型），`internal/work/explicit.go`，`internal/commands/work.go:54-281`，`internal/httpapi/server.go:1903-1940`。
- plan 推进引擎：`internal/job/driver/{engine,observe,report,types}.go`，`internal/job/plandriver_state.go:27-96`，`internal/jobstore/plan_driver_actions.go`。
- 终端注入：`internal/hookrelay/run.go:212-227,895-936`，`mail_inject.go:152`，`prompt_memory.go:34,109`；seat 视图 `internal/httpapi/seat_handler.go:211`。
- ACP TurnNote：`internal/runner/runner.go:327`，`internal/runner/acp/runner.go:345`，`internal/job/session_state.go:82-88`，`submit.go:1248`，`session_recovery.go:132`。
- 门禁数据：`internal/project/browse.go:103,128`，`internal/jobstore/todos.go:442`，`plans.go:629,658`，`internal/jobstore/workflows.go:276-298`，`internal/job/worktree.go:165-178,1122`。
- 自报终态：`internal/job/selfresult.go`，`internal/httpapi/job_result_handler.go`。
- 「等我」：`internal/today/today.go:22-77`，`internal/inbox/waiting.go:15-158`，`internal/inbox/subjects.go:57-82`，`internal/inbox/seat_schedule.go:34`。
- SessionStart：`internal/hookrelay/run.go:212,923`，`internal/seat/inject.go:51`，`internal/seat/charter.go:11-13`。
