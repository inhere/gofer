<!-- template_id: design; template_version: 1.1.1 -->
# plan todo 接着上一项的会话做（会话复用） 设计

> 状态：Draft 0.3（0.2 已批准：用户 2026-10-17 经 web「按推荐，批准设计」，决策 1–9 取推荐值；0.3 为实施计划评审带来的条件补充与落点修正，随计划一并请用户确认）
> issue：gofer-rnp3

- 思考模式：rigorous。
- 核心目标：plan 里连续、相关的几个 todo 可以在同一个 agent 会话里接着做，减少每项重新读代码、重新熟悉上下文的消耗；条件不满足时自动退回新会话，行为可预期、可追溯。
- scope freeze：范围以本稿「范围与非目标」为准；实施中发现超出范围的需求另开 issue。
- review budget：一轮独立评审已用（针对 0.1）；0.2 按评审修订，经作者自查。
- 停止条件：用户对本稿给出具名的批准，或明确要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-17 | Claude（监督会话） | 初稿：todo 的 `continue_from`、派发时的接续条件与退回、续接 job 的组装（新 todo 的任务书与验收、不重复注入规则）、ACP 一次性续接、worktree 继承、链长上限、实测对比方案 |
| 0.2 | 2026-10-17 | Claude（监督会话） | 按独立评审修订：续接请求以普通派发的请求为底板、只从前序取会话字段（全字段表）；prompt 由续接组装方完整渲染（模板 + 承接说明 + 验收 + 交付约定 + 时限行）后以 `RulesResolved` 提交，cli / ACP 两条路径统一，不新增 `ContextCarried`；失败转移在 todo 边界停止、从 todo 重新组装；worktree 续接以前序结束点为本项基线；前序有自动交付时不续接；续接位用条件更新原子占用；`loadSession` 只按配置判断；v1 只支持本机 runner；去掉 job 新字段改为推导；退回原因单独字段；用量汇总补缓存 token 并按真实 agent 分组；实测方案补重复次数、价格表、逐项曲线与盲审 |
| 0.3 | 2026-10-17 | Claude（监督会话） | 按实施计划评审：接续条件增加第 10 条「本项与前序的只读设置一致」（cli 续接载体会沿用源 job 的沙箱参数）；失败转移只在转移时于 todo 边界停止，`resumeJob` 还原真实 agent 的回溯不受影响；本项基线用新的 job 字段 `BaselineSHA`，`WorktreeBaseSHA` 保持分支级语义不变；ACP 一次性续接以显式参数开启 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 背景与目标

plan 的每个 todo 派发出去都是一个新 job、新会话。连续几项改的是同一块代码时，agent 每次都要重新读 AGENTS.md、重新找文件、重新理解上一项刚做完的东西。2026-10-17 用户提出：能否让相关的连续 job 复用同一个会话。

现有能力只能手工做：`gofer job resume`（cli agent 用 `--resume`，ACP 用 `session/load`）和 ACP 持续会话；plan 派发不会用到它们。

用户可见的目标：

- 给 todo 标一个「接着上一项的会话做」（`plan add-todo … --continue prev`），派发时 gofer 在前一项的 agent 会话里继续，任务说明换成这一项的。
- 条件不满足（前一项没完成、换了 agent、换了目录、链太长……）时自动开新会话，并在 todo 上写明原因。
- 能用数据说明省了多少：同一组任务，续接与新会话各跑一遍，对比 token、耗时与返工。

## 名词

- **续接（continue）**：以前一项 todo 最近一次 job 的 agent 会话为基础，开一个新 job 执行本项 todo；技术上是一次 resume，但挂在本项 todo 上、用本项的任务书。
- **前序项**：`continue_from` 指向的 todo（`prev` = 本 plan 中排序在前的上一项）。
- **会话链**：沿 `resumed_from` 回溯、同一个 agent 会话上连续的 job 序列；**链长**指链上 TodoID 发生变化的次数（即续接次数；自动续投与返工继承原 TodoID，不计）。
- **退回**：不满足续接条件时按普通方式派发（新会话），并记录原因。

## 范围与非目标

范围：

- todo 新字段 `continue_from`（todo id 或 `prev`，客户端解析）；CLI `plan add-todo --continue`、`plan set-todo --continue / --no-continue`；HTTP 与 MCP 的 todo 写接口；web 的 todo 表单开关与 todo 卡片显示。
- 派发路径：判断接续条件，满足则组装续接请求，否则退回普通派发并记录原因；两者共用同一段提交与收尾。
- 续接请求的组装：以普通派发的请求为底板，只从前序 job 取会话相关字段；prompt 由组装方完整渲染；不重复注入会话里已有的规则、prime、工具段、技能清单。
- ACP 续接用一次性形态（跑完这一轮就结束），不进入持续会话。
- worktree：续接 job 继承前序 job 的 worktree，并以前序结束点作为本项的提交 / diff / scope 基线。
- 失败转移（fallback）在 todo 边界停止。
- 链长上限（默认 3）。
- plan 用量汇总补上缓存 token，并把 cli 续接载体的用量归到真实 agent（实测对比需要）。
- 实测对比方案与一次实测。

非目标：

- 不改变 `gofer job resume` 的现有行为与参数。
- 不做「同一个持续 ACP 进程逐项执行 todo」（一个 job 跨多个 todo）。
- 不做会话分叉（`--from-session` fork）模式；作为后续选项记录在「决策」。
- 不跨 runner、不跨 agent 会话族续接。
- 并行的 todo 不共享会话（同一前序项只允许一个后继项续接）。
- v1 只支持本机 runner（`local`）；worker 与 peer 一律退回。
- 不改变自动交付：前序 job 需要自动交付时不续接（链式交付另议）。

## 已确认事实与规范

代码现状（2026-10-17，v0.169.0）：

- 派发：`internal/job/plandispatch.go` 的 `dispatchTodo`（自动 `MaybeDispatchTodo` 与显式 `DispatchTodo` 共用）把 todo 字段拷进 `JobRequest`（agent、model、runner、cwd、timeout、budget、title、acceptance、scope、prime keywords、verify、review、template / vars），无模板时 prompt 为 `defaultTodoPrompt`；`todoActiveJob` 防止重复派发（`needs_review` 也算在跑）。
- 写回：`internal/job/todolink.go`——提交时 todo 置 doing、记 `job_id`（最近一次）；终态 done → todo done 并推进 plan，`needs_review` → 保持 doing 等验收，失败 → 记失败说明。
- `--after prev` 由 CLI 解析为 plan 的上一项（`internal/commands/plan.go`），可作为 `--continue prev` 的样板。
- 续接：`internal/job/resume.go`——前提为源 job 终态（`needs_review` 要先验收或退回）、捕获了 `session_id`、agent 同会话族（`agent.SessionCompatible`，内置 claude / claude-acp、codex / codex-acp、suga / suga-acp）、runner 相同；`continuationBase` 继承源 job 的 TodoID、cwd（`resumeCwd`，不可覆盖）、worktree（`InheritWorktree`，仅本机 runner）、Review（`ReviewFixed`）等，**不**带 Acceptance / Scope / Verify / Template；`RulesResolved=true` 跳过规则 / prime（`rules.go`）与验收 / 交付约定 / knowledge capture（`prompt_sections.go`）；工具段与时限行另被 `ResumedFrom` / `ResumeSourceAgent` 独立跳过（`tools.go`、`prompt_sections.go`）；技能与 MCP 只看 `SkillsResolved` / `MCPResolved`。
- cli agent 续接走 exec 载体（`resumeCLICarrier`）：prompt 先渲染进 argv 再设 `Agent=exec`，此后 Submit 的模板与 prompt 注入对它都不生效。ACP 续接在本机或 worker 上默认是持续会话（`continuous := autoAttempt == 0 && residentRunner`），只有自动续投是一次性；只有带 `ResumedFrom` 的 job 才 `session/load`。
- 失败转移：`fallbackBase` 沿 `ResumedFrom` 上溯到第一个非载体 job 并回放其请求（`fallback.go`）。
- worktree 继承：`inheritedWorktree` 返回源 job 的 `WorktreeBaseSHA`，执行时以它作提交 / diff 基线（`resume.go`、`execute.go`）。
- 前序终态的收尾（`linkTodoOutcome` → `advancePlan` → `MaybeDispatchTodo`）在前序 `finish()` 内同步执行，自动交付在终态钩子里异步执行（`onterminal.go`）。
- `--after prev` 只在 `add-todo` 时解析（`set-todo` 上拒绝 `prev`）。
- `PlanUsage` 按 `agent` 列分组，cli 续接载体的 agent 列是 `exec`。
- 会话 id：claude / suga 提交时注入，codex 等在终态时从输出捕获，ACP 来自 `session/new`。claude 的会话存储按 cwd 区分，suga 0.25 起拒绝在不同目录续接（gofer 已把 suga 的提示原样带进 job 错误）。
- worktree：项目 `worktree_default` 会让每个 todo 的 job 新建 worktree；两个 job 共享 worktree 的唯一路径是续接继承。
- 用量：每个 job 的 `usage_json` 有 input / output / cache_read / cache_write / total / cost / turns；codex 只有总数；ACP 的 `used` 可能是上下文占用而非本轮消耗（续接后可能是累计值，需要实测确认）。`plan show` 的汇总只有 total / input / output / cost，没有缓存 token。
- 分支交付（`branch_delivery.go`）沿 `resumed_from` 回溯到根 job 取任务名。

规范：G021 / G022（入口只转发）、G032（additive 迁移）、G045 / G046（skill references 通用写法）、G051–G054（测试）、G053（CLI flag 登记）、G056（路由声明；本功能不新增路由，只扩展 todo 字段）。

## 总体方案

1. 建 todo 时声明 `continue_from`（`--continue prev` 或 `--continue <todo-id>`）。声明续接即隐含依赖：前序项不在 `after` 里时自动加入。
2. 派发本项时，检查接续条件（见「关键流程 / 接续条件」）。全部满足就组装续接 job；任何一条不满足就按普通方式派发，并在 todo 上记一条说明「未接续：<原因>，已开新会话」，同时写 job 事件。
3. 续接 job：在前序 job 的会话里继续，cwd 与 worktree 沿用前序 job，提交 / diff 基线从前序结束点算起；prompt 是本项 todo 的任务书，前面加一行承接说明，后面带本项的验收、交付约定与时限行；verify、scope、review 等取本项 todo；规则、prime、工具段、技能清单不再注入（会话里已有）。
4. ACP agent 用一次性续接：`session/load` 后跑完这一轮即结束，结果按普通 job 写回 todo。
5. 链长达到上限（默认 3）时，本项开新会话（原因写明「已连续接续 N 次」），之后的项从这个新会话起算。

一期完成，不拆分。

## 架构

### 数据

- `plan_todos` 新列：`continue_from TEXT`（前序 todo id）、`continued_by TEXT`（已续接本项会话的后继 todo id，用于原子占用）、`continue_note TEXT`（最近一次派发「未接续」的原因，与 `dispatch_error` / `note` 分开）。additive 迁移。
- job 不加新字段：续接 job 的 TodoID 与其 `resumed_from` 指向的 job 的 TodoID 不同，即可识别为续接；链长按此推导。
- 事件：`job.continued`（前序 todo、前序 job、链长）、`job.continue_fallback`（todo、原因），登记到事件说明与 skill references。

### 派发（internal/job/plandispatch.go）

把 `dispatchTodo` 拆成「选择请求」与「提交与收尾」两段：

```
req := normalTodoRequest(plan, todo, by)          // 现有组装逻辑原样抽出
if todo.ContinueFrom != "" {
    if creq, reason := s.todoContinuationRequest(plan, todo, req); reason == "" {
        req = creq                                   // 续接
    } else {
        record continue_note = reason; event job.continue_fallback
    }
}
// 现有的提交与收尾原样共用：ErrSourceSessionElsewhere 重试、AddSessionJobWatch、
// EventPlanTodoDispatched、SetTodoDispatchError("") / todoDispatchFailed
```

续接请求提交时若返回续接类错误（`ErrNoSession`、`ErrCrossRunner`、`ErrResumeUnsupported`、`ErrJobNotTerminal`），释放续接位、记原因，改用普通请求提交一次，不写 `dispatch_error`。

### 续接请求的组装：`todoContinuationRequest`

新函数，不改 `continuationBase` 与 `job resume` 的行为。以普通派发的请求为底板（字段、通知、Channel=plan、SourceSessionID、调用方都与普通派发相同），只从前序 job 取会话相关字段：

| 类别 | 字段 | 取值 |
|---|---|---|
| 会话 | SessionID、ResumedFrom、SourceJobID | 前序 job |
| 位置 | Cwd（`resumeCwd`）、worktree（`InheritWorktree`）、Runner | 前序 job |
| 基线 | 提交 / diff / scope 基线 | 新字段 `BaselineSHA` = 前序 job 的结束点（`WorktreeHeadSHA`，无 worktree 时为前序结束时的 HEAD）；`WorktreeBaseSHA` 不变（仍是分支基线，供合并、清理、状态使用） |
| 模型 | Model | 本项 todo（或其模板）有值则用之，否则前序 job |
| 任务 | TodoID、PlanID、Title、Acceptance、Scope、Verify、Review、TimeoutSec、Budget、Tags、ReadOnly | 本项 todo（含模板元数据），与普通派发相同 |
| 不继承 | Hold、Fallback 冻结计划、Deliver*、LockPaths、Secrets、EnvFiles、" (resumed)" 标题后缀、`ReviewFixed` | 按普通派发 |
| MCP / 技能 | MCP 挂载 | 前序 job 已解析的 MCP（保持会话一致）；`SkillsResolved=true`，不再插技能清单 |

prompt 由组装方完整渲染，然后以 `RulesResolved=true` 提交（Submit 不再追加任何段落），cli 与 ACP 两条路径一致：

1. 按本项 todo 的模板（`todoTemplateBuiltins(本项 TodoID)` + vars）渲染，或用 `defaultTodoPrompt`；模板元数据（Model / Budget / Timeout / Review / Verify / Tags / ReadOnly）按普通派发应用，模板里的 Worktree 忽略（沿用前序）、FallbackAgents 见「失败转移」。
2. 前面加承接说明（与现有 prompt 注入段同语言）：「本任务接着上一项（<前序 todo 标题>）在同一个会话里继续。上一项已完成；下面是新的任务说明，以它为准。」
3. 后面加本项的验收、交付约定与时限行——复用 `prompt_sections.go` 的拼装函数（抽成纯函数供两处调用），时限按本项 job 的时限计算。
4. 不加规则、tracker-prime、工具段、技能清单（会话里已有）。

cli agent：把渲染好的全文作为 prompt 交给 `resumeCLICarrier`；ACP agent：见下。

### ACP 一次性续接

给续接组装开放一次性形态（`resumeACPSession` 增加显式的一次性参数）：ACP 续接请求设 `Session=false`、`ResumedFrom=<前序 job>`（Submit 据此填 `LoadSessionID`），`session/load` 后执行一轮即结束，与自动续投相同的路径。`loadSession` 支持与否只能按配置判断（`acp.load_session` 未关闭）；agent 运行时拒绝时 job 失败并带原因（决策 4）。

### 失败转移（fallback）

- 失败转移回溯时遇到 TodoID 变化即停止，不回放前序 todo 的请求；这个停止只用于失败转移，`resumeJob` 还原真实 agent 的回溯照旧（否则续接 job 的自动续投与人工 resume 会失败）。
- 续接 job 触发失败转移时，从本项 todo 重新组装普通请求（换成 fallback agent、新会话），而不是回放续接请求。
- 自动续投不受影响（它以续接 job 自身为源，TodoID 就是本项）。

### worktree 与交付

- 续接 job 继承前序 worktree（本机 runner；目录必须仍存在）。
- 本项的提交 / diff / scope 基线为前序结束点，`todoDoneLine`、review diff、scope 检查都只看本项的改动。
- 前序 job 需要自动交付（job 带 `Deliver` 或项目 `delivery` 不为 none）时**不续接**，退回原因「前序需要自动交付」（避免交付合并、删除 worktree 与续接竞态）。链式交付另议（决策 6）。
- 链断开退回新会话、而前序有未合并的 worktree 分支时：新 worktree 以前序分支为基（`WorktreeBase` 指向前序分支），使后续项能看到前面的提交；todo 的 `continue_note` 写明。
- 分支交付回溯任务名时遇到 TodoID 变化即停，每个 todo 用自己的标题。

### 续接位的原子占用

同一前序项只能被一个后继项续接：派发时以条件更新 `UPDATE plan_todos SET continued_by=? WHERE id=<前序> AND continued_by=''` 占用，影响 0 行即「已被续接」退回。提交失败时释放；续接 job 后来失败**不释放**（会话已被推进），要重做需对本项 `--no-continue` 或把前序重置。

### 链长

沿 `resumed_from` 回溯，统计 TodoID 发生变化的次数（自动续投、返工、人工 resume 继承原 TodoID，不计）。达到上限（项目配置 `continue_max_chain`，默认 3，0 = 不限，上限 10；登记到配置校验与文档）时本项退回新会话，原因「已连续接续 N 次，开新会话以控制上下文长度」。

### 接口

- CLI：`gofer plan add-todo <plan> "<标题>" … --continue prev|<todo-id>`（`prev` 在客户端解析为 plan 的上一项，plan 为空时报错）；`gofer plan set-todo <todo> --continue <todo-id> | --no-continue`（`set-todo` 只接受 todo id，与 `--after` 一致）。服务端校验：同一 plan、不能指向自己、不能成环；设置 `continue_from` 时自动加入 `after`；前序被删除时续接退回并记原因。新 flag 登记 G053。
- HTTP：todo 的创建 / 修改接口增加 `continue_from`；todo 视图返回 `continue_from`、`continued_by`、`continue_note`。无新路由。
- MCP：todo 写工具增加 `continue_from`。
- web：todo 表单「接着上一项的会话做」开关（选择前序项）；todo 卡片显示「接续自 <标题>」或「未接续：<原因>」。

### 用量汇总

`PlanUsage` 增加 cache_read / cache_write，按 agent 分组时 cli 续接载体按其真实 agent（`resume_agent`）归类；`plan show`、HTTP、web 同步。job 列表按 plan 过滤时的 JSON 若尚未带 usage 则补上，供实测脚本读取。

## 关键流程

### 接续条件（全部满足才续接）

前序 job 取前序 todo 名下**最近一个终态为 done、且捕获了 `session_id` 的 job**（经过自动续投或返工的取最新者；cli 载体按 `fallbackBase` 的方式还原真实 agent）。条件尽量复用 `resumeJob` 的现有校验（终态、`ErrNoSession`、`ErrCrossRunner`、`SessionCompatible`、`resumable`），不另写一套：

1. `continue_from` 指向同一 plan 中仍存在的 todo，且其状态为 done（需要验收的已被接受）。显式 `plan dispatch` 同样要求这一条。
2. 存在上述前序 job（读终态快照的 `session_id`）。
3. 本项 assignee 与前序 job 的真实 agent 同会话族；exec 不能续接。
4. runner 相同，且为本机 runner（比较前经 `config.NormalizeRunnerName`）。
5. 本项 todo 没有设置 cwd，或与前序 job 的 `resumeCwd` 相同。
6. 前序 job 用了 worktree 时目录仍存在；前序 job 不需要自动交付。
7. 续接位占用成功（见「续接位的原子占用」）。
8. 链长未达上限。
9. 配置层支持续接：ACP agent 未关闭 `acp.load_session`；cli agent 有续接模板；本项指定的 model / budget 在该 agent 上可用（`model_args` / 预算支持）。
10. 本项与前序 job 的只读设置一致（cli 续接载体沿用源 job 的沙箱参数）。

任何一条不满足：普通派发，`continue_note` 记原因，事件 `job.continue_fallback`。plan 暂停或阻塞时不评估（与现有派发一致）。

### 续接后的结果

- 续接 job 成功 / 失败 / 待验收的写回与普通 job 完全相同（挂在本项 todo）。
- 续接 job 失败且属于临时错误时，自动续投照常生效（在同一会话上，不计入链长）。
- 续接 job 在 agent 侧被拒绝（例如会话已被清理、`loadSession` 不支持、目录不一致）时，job 失败并带原因；todo 的失败说明里提示可以 `--no-continue` 后重新派发。不在同一次派发里自动重试新会话（避免一次派发产生两个 job）。
- 失败转移按「失败转移（fallback）」一节，从本项 todo 重新组装。
- workbench 按会话聚合：一个会话线程跨多个 todo 是预期行为，线程标题取根 job；卡片上显示每轮对应的 todo。

### 默认数值

| 项 | 默认 | 说明 |
|---|---|---|
| 链长上限 `continue_max_chain` | 3 | 项目级配置；0 = 不限；最大 10 |
| 续接对前序 todo 状态的要求 | done（含已验收） | needs_review 未验收不续接 |

## 实测对比方案

目的：在决定是否推荐续接之前，得到真实的节省数据。

- **任务组**：从仓库里选一组 4 项、串行、改同一子系统的真实小任务，写好 4 份任务书；同一基线提交；同一 agent 与模型（cli 形态的 suga + opencode-go/deepseek-v4.1-flash）。
- **A 组（新会话）**：4 个 todo 普通派发；第 2–4 项的新 worktree 以前一项的分支为基（与 B 组代码状态一致）。
- **B 组（续接）**：4 个 todo，第 2–4 项 `--continue prev`，共享一个 worktree。
- **可选 A' 组**：新会话但同样跳过规则 / prime / 工具段注入，用来区分「复用上下文」与「少注入」两部分收益；不跑时至少报告被跳过注入的文本字节数。
- **重复次数**：每组 3 次，报中位数与区间（LLM 不确定性）。
- **记录**：每个 job 的 input / output / cache_read / cache_write / total、wall time、`tool_calls`（`turns` 只在设置预算时有值）；成本按显式价格表重算（cache_read 用折扣价），不依赖 `cost_usd`；逐项记录 cache_read 曲线，作为链长上限的依据；按 todo 汇总，避免 cli 载体的 `exec` agent 列干扰。
- **质量**：测试是否通过、监督审阅是否退回、返工次数——判定规则事先写定，审阅者不知道 job 属于哪一组。
- **限定**：实测用 cli suga（stream-json 的 usage 是本轮值）；ACP 的 `used` 可能是累计上下文，若同时测 suga-acp，只比较成本与耗时；结论写明适用范围。codex 只有总 token，不参与本次对比。
- **结论落点**：写进设计稿「实施记录」，据此决定是否在 skill 里推荐、是否调整链长上限默认值。

## 安全、数据、运维与回滚

- 续接不改变权限：续接 job 的提交身份与普通派发相同（plan 的调用方），会话所有权不变。
- 数据：新列 additive；旧版本忽略 `continue_from`，按普通派发。
- 回滚：去掉 todo 的 `continue_from`（`--no-continue`）即恢复普通派发；回退二进制无影响。
- 风险：上下文变长导致单轮成本上升与质量下降 → 链长上限 + 实测；前一项的错误思路被带入下一项 → 只在前序项 done（含已验收）后续接。

## 测试计划

遵守 G051–G054。

- 派发：满足全部条件时提交的是续接 job（`resumed_from` 指向前序 job、本项 TodoID、本项 prompt / 验收 / verify / review、普通派发的通知与调用方）；十条条件逐条不满足时退回并记 `continue_note`；续接类提交错误退回普通派发；并行后继竞态下只有一个续接（条件更新）；链长上限（推导计数）。
- 继承面：hold、fallback 冻结计划、交付、锁路径、标题后缀不被继承（全字段表逐项断言）。
- 失败转移：续接 job 触发 fallback 时，接管 job 挂在本项 todo、用本项任务书，前序 todo 不受影响。
- 基线：续接 job 的完成说明、review diff、scope 检查只含本项改动。
- 交付：前序需要自动交付时不续接。
- prompt：cli 与 ACP 两条路径的续接 prompt 都含承接说明、本项任务书、验收、交付约定、时限行，不含规则 / prime / 工具段 / 技能清单；普通 `job resume` 行为不变（现有测试不改断言）。
- ACP：续接为一次性，跑完即终态，不进入 `awaiting_input`（acptest）。
- worktree：继承前序 worktree；worktree 已删除时退回。
- 分支交付：每个 todo 用自己的标题。
- CLI / HTTP / MCP：`continue_from` 的写入、`prev` 解析、plan 为空报错、`--no-continue` 清除。
- web：表单与卡片的纯函数与接线断言。

## 文档

- `skills/gofer-usage/references/plans-workflows.md`：新增「接着上一项的会话做」小节（何时用、条件、退回、链长、与 worktree 的关系）。
- `docs/reference/http-api.md`：todo 字段。
- CHANGELOG。

## 决策

1. **续接形态**：推荐 **resume（同一会话继续）**。备选：fork（`--from-session`，从前一项的会话分叉出新会话），上下文同样带过来但不会越叠越长；agent 支持度不一。建议先做 resume，fork 视实测结果再加。
2. **链长上限默认值**：推荐 **3**（最多 4 项共用一个会话），实测后再调。
3. **前序项必须 done 才续接**：推荐**是**（含已验收；前序是返工 job 时取最新 done 的那个）。
4. **续接被 agent 拒绝时**：推荐**本次失败、不自动改派新会话**；备选：自动改派一次新会话。
5. **默认开启与否**：推荐**默认关闭、按 todo 显式开启**。
6. **前序需要自动交付时**：推荐 v1 **不续接**（退回并说明）；备选：把交付推迟到链尾（链式交付，另议）。
7. **续接位的释放**：推荐**提交失败时释放、job 失败不释放**（会话已被推进）。
8. **退回新会话时的 worktree 起点**：推荐**以前序分支为基**，让后续项看到前面的提交。
9. **runner 范围**：推荐 v1 **只支持本机 runner**，worker / peer 退回。

## 待确认事项

- 已确认（用户 2026-10-17「按推荐」）：决策 1–9 均取推荐值。
- 实测任务组选哪一组（建议实施后从 gofer 自身的小 issue 里挑 4 个相关项）。

## 结论与人工计划 Gate

本稿经用户批准后出实施计划（建议拆为：数据与 CLI / HTTP / MCP；派发与续接组装；ACP 一次性续接与 worktree / 交付；web；实测与文档）。

## 实施记录

（实施后补充。）
