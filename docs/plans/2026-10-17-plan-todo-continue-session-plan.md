<!-- template_id: plan; template_version: 1.2.0 -->
# plan todo 接着上一项的会话做 实施计划

> 状态：Draft 0.2 / 已批准并请求执行（用户 2026-10-17：「批准计划，开始实施」；同时确认设计 0.3）
> issue：gofer-rnp3

- 思考模式：rigorous。
- 核心目标：落地设计 `docs/design/2026-10-17-plan-todo-continue-session-design.md`（Draft 0.2 已批准；0.3 为本计划评审带来的修正）：todo 的 `continue_from`、派发时的接续判断与退回、续接请求的组装、失败转移在 todo 边界停止、worktree 基线、ACP 一次性续接、web 显示，以及实施后的实测对比。
- scope freeze：只做本计划「范围」列出的内容；fork 形态、链式交付、worker / peer 支持不在本计划内。
- review budget：本计划最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户具名批准本计划，或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-17 | Claude（监督会话） | 初稿：K1 数据与接口、K2 prompt 段落抽取（零行为变化）、K3 派发与续接请求、K4 失败转移 / worktree 基线 / 交付、K5 web、D1 文档、V 验收与发版、M 实测对比 |
| 0.2 | 2026-10-17 | Claude（监督会话） | 按独立评审修订：新增 K0 零行为提取（`resumeJob` 校验、模板渲染与元数据、prompt 段落含 knowledge capture 与生效时限）与 golden 对照测试；K1 拆为 K1a / K1b / K1c；K3 拆为 K3a / K3b / K3c，补只读一致条件、model / budget 可用性、ACP 一次性显式参数、清掉模板字段、JobRequest 全字段分类的反射测试；K4 拆为 K4a（失败转移只在转移时于 todo 边界停止，可与 K3 并行）/ K4b（新字段 `BaselineSHA`，`WorktreeBaseSHA` 不动）；补配置字段策略、事件登记、迁移与文件清单；同包任务串行；M 改用一次性克隆 + 临时项目、先试跑一条链、补选择标准、价格表与盲审导出 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 目标与完成定义

1. `plan add-todo … --continue prev|<id>` / `set-todo --continue <id> | --no-continue` 可用；HTTP、MCP、web 都能设置并显示。
2. 满足设计「接续条件」十条时，todo 的 job 在前序会话里继续，prompt 是本项完整任务书（承接说明 + 任务书 + 验收 + 交付约定 + 时限行），不重复注入规则 / prime / 工具段 / 技能清单；cli 与 ACP（一次性）两条路径都成立。
3. 任一条件不满足时普通派发，`continue_note` 写明原因；续接位原子占用；链长上限生效。
4. 续接 job 的失败转移、完成说明、review diff、scope 检查、分支交付标题都只对应本项 todo。
5. 普通 `job resume`、普通 todo 派发的行为不变。
6. 实施发布后完成一次实测对比（M），结论写进设计稿实施记录。

完成定义：上述行为有测试覆盖并通过；Linux 全量 `-race`、Windows 全量、darwin vet、web 三命令通过；文档同步；发版 CI 全绿；M 的结论已记录。

## 范围、排除项与授权

范围：设计稿「范围」所列全部内容 + 实测对比。

排除项：fork（`--from-session`）形态、链式交付、worker / peer runner、改变 `job resume` 的参数与行为。

授权边界：

- 计划批准不授权实施；实施需要用户另发执行请求（可与批准同一句话）。
- 执行请求覆盖：由监督会话的 sonnet 子 agent 在主 checkout，或 suga（`--model opencode-go/deepseek-v4.1-flash`）在主机 worktree 实施并本地提交；监督者审阅、容器 `-race` 复跑、合并。
- 发版沿用已有长期授权（`gofer-release-flow`：推送 → CI → mirror → 一键更新 → 容器 worker）。
- M 实测会在主机派出约 24 个 suga job（2 组 × 4 项 × 3 次，可选 A' 组再加 12 个）；开跑前在回复里报告预计耗时，用户不反对即执行。
- `host_or_non_offline_action=REQUIRED`（主机 job、发版、实测）。

## 输入与批准证据

- 设计：Draft 0.2，用户 2026-10-17 经 web 批准（原话「按推荐，批准设计」），决策 1–9 取推荐值。
- 设计已经一轮独立评审（针对 0.1，7 条必改全部并入 0.2）。
- 关联 issue：gofer-rnp3；相关：gofer-hnqf（预算）、gofer-s2er（时限行，已发布）。
- 仓库规则：AGENTS.md G021 / G022、G023、G032、G045 / G046、G051–G054、G053、G056。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 在原会话里继续一个 job | `job/resume.go` 的 `resumeJob` 校验、`resumeCLICarrier`、ACP `session/load`、`InheritWorktree` | 部分 | 新增 `todoContinuationRequest`，复用校验与载体构造，不改 `continuationBase` | OWNER_EXTENSION | `continuationBase` 的继承面不适合 todo 派发（设计「已确认事实」） | 两条组装路径 → 只共享校验与载体函数，字段表单独维护并有逐项测试 |
| CAP-02 | prompt 段落与模板（验收、交付约定、时限行、模板渲染与元数据） | `job/prompt_sections.go` 的 `injectPromptSections`、`appendJobDeadlineLine`；`job/template.go` 的 `applyTemplate` | 部分 | 抽出纯函数供 Submit 与续接组装共用（K0） | OWNER_EXTENSION | 续接以 `RulesResolved` 提交时 Submit 不再注入 | 两处拼装漂移 → 只保留一份纯函数 |
| CAP-03 | 派发、写回、推进 | `job/plandispatch.go` 的 `dispatchTodo`、`todolink.go`、`planadvance.go` | 是 | 拆成「选择请求」与「提交收尾」两段 | OWNER_EXTENSION | — | — |
| CAP-04 | 失败转移 | `job/fallback.go` 的 `fallbackBase` / `fallBack` | 部分 | 在 todo 边界停止并从 todo 重组 | OWNER_EXTENSION | 现有回放会重做前序 todo（设计 0.2） | — |
| CAP-05 | 用量汇总 | `jobstore/plans.go` 的 `PlanUsage` | 部分 | 加缓存列、按真实 agent 归类 | OWNER_EXTENSION | 缺缓存 token；载体 agent 列为 exec | — |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 新增 `ContextCarried` 请求字段 | CAP-02 | 删掉它后由续接组装方完整渲染 prompt、以 `RulesResolved` 提交即可；新字段还要同步 hold 序列化与 worker 回流（设计 0.2） |
| job 新字段 `continued_from_todo` | CAP-01 | 删掉它后由「TodoID 与 resumed_from 指向 job 的 TodoID 不同」推导即可，省掉 schema 与序列化改动 |

## 前置检查与 fail-closed 条件

workspace baseline（2026-10-17 写计划时）：

- Git root：`inhere-tools/gofer`；branch `main`；HEAD `da545842` 之后（本计划与设计批准记录待提交）。
- 依赖：容器 Go 1.26、主机 web 三命令脚本与 Windows 全量脚本、主机 suga。

每个任务开工前（监督者）：上一任务已审阅并复跑；同时并行的实施方改动不重叠的目录；主 checkout 的提交一律 pathspec。

fail-closed：

- K0 是零行为变化的抽取：现有 prompt 注入、模板、resume 相关测试不改断言全部通过；K0 先在未改动的代码上提交 golden 对照测试（见 K0）。
- 普通 `job resume`、`reject --resume`、自动续投、失败转移（`fallback_test.go`、`session_fallback_test.go`）的现有测试不改断言全部通过；有 worktree 的普通 resume 其 `WorktreeBaseSHA` 不变。
- 没有 `continue_from` 的 todo 派发结果与改动前逐字段一致（K0 的 golden 测试在 K3 之后不改 golden 通过）。
- 任何新的 todo / job 字段都要有 additive 迁移与旧库迁移用例（plan_todos 走 `migratePlanTodos` 的 add 机制）。
- JobRequest 全字段在续接继承表中必须有分类：反射测试枚举字段，新增字段未分类即失败。
- 同一 Go 包的任务在主 checkout 里串行；并行只发生在不同目录，或一方在主机 worktree。

## 波次与依赖

| 波次 | 任务 | 依赖与并行 |
|---|---|---|
| 一 | K0 零行为提取与 golden（internal/job）；K1a 存储与校验（internal/jobstore） | 两者目录不同，可并行 |
| 二 | K1b 接口（commands / httpapi / mcpserver / client）；K1c 用量汇总（jobstore/plans.go）；K4a 失败转移与交付标题（job/fallback.go、branch_delivery.go） | K1b、K1c 依赖 K1a；K4a 依赖 K0；K1c 与 K1a 同包，K1a 合入后进行；K4a 与 K1b 目录不同可并行 |
| 三 | K3a 派发拆分、接续判断、占用与退回 | 依赖 K0、K1a、K4a（同在 internal/job，串行） |
| 四 | K3b 续接请求组装（cli / ACP）；K5 web | K3b 依赖 K3a；K5 依赖 K1b、K1c 的字段（冻结后可与 K3 并行，在主机 worktree） |
| 五 | K3c 链长与配置；K4b 基线与 worktree / 交付 | 依赖 K3b；同在 internal/job，串行 |
| 六 | D1 文档；V 验收与发版 | 全部 |
| 七 | M 实测对比 | V |

## 任务

通用要求（每个任务）：

- 只提交自己的文件（pathspec 提交），英文 conventional message；不还原、不丢弃别人的改动；不碰 `.gofer/tracker/*`；不删注释、不重排无关代码。
- 用户可见变化在同一任务里同步对应 skill references 与 `docs/reference/http-api.md`（G045 / G046）。
- Go 验证统一前缀 `GOTOOLCHAIN=go1.26.9 GOWORK=off`，测试前 `env -u CLAUDE_CODE_SESSION_ID -u GOFER_SESSION_ID`；`go build ./...`、`go vet ./...`（linux / windows / darwin）。主机实施（suga）不能跑 `-race`，由监督者在容器复跑。
- G051 / G052：组件先停后关库；成功路径等待用 `internal/testutil/wait`。

### K0 零行为提取与 golden 对照

- 文件：`internal/job/resume.go`、`internal/job/template.go`、`internal/job/prompt_sections.go`、`internal/job/submit.go`（只改调用方式）、新测试文件（golden 与提取函数单测）。
- 动作：
  1. **先**提交 golden 对照测试（在未改动的代码上）：对普通 todo 派发覆盖无模板、有模板 + vars、exec + cmd、runner 为空、supervisor + notify、acceptance + scope + verify + review + budget + model + prime keywords 等组合，读落库的 `request_json` 与 job 行，归一化 job id / 时间戳 / result dir 后存 golden。
  2. `resumeJob` 的校验抽成独立函数（终态、`ErrNoSession`、真实 agent 还原、agent 存在与会话族、`ErrCrossRunner`、`resumable`、model / budget 可用性），`resumeJob` 改为调用它，行为不变。
  3. 模板：`applyTemplate` 拆为 `renderTemplate`（只渲染 prompt）与 `applyTemplateMeta`（Model / Budget / Timeout / Tags / FallbackAgents / ReadOnly / Worktree / Review / Verify / VerifyTimeout），Submit 依次调用，行为不变。
  4. prompt 段落：抽出 `effectiveTimeout(cfg, req)`（`normalizeTimeout` + `jobTimeLimit` 的生效时限）与 `buildPromptSections(cfg, req, realAgent, effectiveTimeout) (text string, knowledgeCapture bool)`（验收、交付约定、knowledge capture 与时限行；用真实 agent 判断是否 exec，不看载体）；`injectPromptSections` / `appendJobDeadlineLine` 改为调用它，行为不变（包括 NoRules、无 scope 约束、项目上限截断、交互 job 等分支）。
- 验证：`go test -race -count=1 ./internal/job/...`。
- 完成标准：golden 在 K0 前后都通过；现有测试不改断言；新函数有单测。
- 依赖：无。

### K1a 存储与校验

- 文件：`internal/jobstore/todos.go`、`internal/jobstore/store.go`（`migratePlanTodos`）及测试。
- 动作：`plan_todos` 新列 `continue_from`、`continued_by`、`continue_note`（additive + 旧库迁移用例）；`continue_from` 经 `TodoPatch` 写入，`continued_by` / `continue_note` 只能经专用函数写；`ClaimTodoContinuation(pred, succ)` 条件占用与 `ReleaseTodoContinuation`；校验集中在 jobstore 一处（同一 plan、不指向自己、`continue_from` 链不成环）；设置时自动把前序加入 `after`；改 / 清 `continue_from` 或删除前序 / 本项时释放旧占用、悬空引用按「前序不存在」处理。
- 验证：`go test -race -count=1 ./internal/jobstore/...`。
- 完成标准：字段读写、校验、自动 `after`、占用并发、释放与悬空用例通过。
- 依赖：无。

### K1b 接口

- 文件：`internal/commands/plan.go`、`internal/commands/flagreset_test.go`（如新增全局）、`internal/httpapi/plan_handler.go`、`internal/mcpserver/`（todo 工具）、`internal/client/`、`docs/reference/http-api.md`、`skills/gofer-usage/references/plans-workflows.md`（字段说明）。
- 动作：CLI `add-todo --continue prev|<id>`（`prev` 客户端解析，plan 为空报错）、`set-todo --continue <id> | --no-continue`（不接受 `prev`）；HTTP / MCP 写 `continue_from`；todo 视图与 `plan show` 显示 `continue_from` / `continued_by` / `continue_note`；`plan import` 本期不支持 `continue_from`（文档写明）。
- 验证：`go test -race -count=1 ./internal/httpapi/... ./internal/mcpserver/... ./internal/client/...`；`go test -race -count=1 -shuffle=on ./internal/commands/...`。
- 完成标准：三个入口写入与校验错误映射一致；`TestFlagGlobalsRegistryComplete` 通过。
- 依赖：K1a。

### K1c 用量汇总

- 文件：`internal/jobstore/plans.go`（`PlanUsage`）、`internal/httpapi/plan_handler.go`（用量视图）、`internal/commands/plan.go`（`plan show` 用量行）、`docs/reference/http-api.md`。
- 动作：增加 cache_read / cache_write；按 agent 分组时 cli 载体按 `resume_agent` 归类；按 plan 列 job 的 JSON 带 usage（若尚未带）。
- 验证：`go test -race -count=1 ./internal/jobstore/... ./internal/httpapi/...`。
- 完成标准：缓存列与载体归类用例通过。
- 依赖：K1a（同包串行）。

### K4a 失败转移与交付标题

- 文件：`internal/job/fallback.go`、`internal/job/branch_delivery.go` 及测试。
- 动作：新增 `fallbackBaseWithin(snap, stopAtTodo bool)`，只有 `fallBack` 传 true（遇 TodoID 变化停止）；`resumeJob` 等其他调用方行为不变。续接 job 触发失败转移时从本项 todo 重组普通请求（fallback agent、新会话）——提供 `rebuildTodoRequest` 钩子，K3a 接上普通请求的组装。分支交付回溯任务名时遇 TodoID 变化即停。
- 验证：`go test -race -count=1 ./internal/job/...`。
- 完成标准：`fallback_test.go`、`session_fallback_test.go` 不改断言通过；新增用例：续接 job 的失败转移挂在本项 todo；续接 job 的自动续投与人工 resume 成功（真实 agent 还原不受影响）。
- 依赖：K0。

### K3a 派发拆分、接续判断、占用与退回

- 文件：`internal/job/plandispatch.go`、新文件 `internal/job/todo_continue.go` 与测试。
- 动作：
  - `dispatchTodo` 拆为 `normalTodoRequest` 与共用的提交 / 收尾段（`ErrSourceSessionElsewhere` 重试不提前释放占用、job watch、`EventPlanTodoDispatched`、`dispatch_error` 处理原样保留）。
  - 接续条件十条（设计 0.3），逐条编号、逐条用例；前序 job 选择规则（最近一个 done 且有 `session_id`，不受 `ListJobsByTodo` 条数限制影响——多次失败重试与返工后仍选对）。
  - 占用：判断通过后 `ClaimTodoContinuation`；续接类提交错误先释放、记 `continue_note`、改用普通请求提交；`continue_note` 区分「已被本项占用」与「已被其他项占用」。
  - 事件 `job.continued` / `job.continue_fallback`（登记到事件说明）。
  - 本任务内组装函数先返回「尚未支持」，所有 todo 都按普通请求派发（golden 仍通过），K3b 接上组装。
  - 两条触发路径都测：自动链（前序 `finish` 内的 `advancePlan`）与验收通过后（`review.go`）。
- 验证：`go test -race -count=1 ./internal/job/... ./internal/httpapi/...`。
- 完成标准：golden 不改通过；条件、占用、退回、事件用例通过；差分用例：设了 `continue_from` 但条件不满足的 todo，其 `request_json` 与未设置时相同，仅多 `continue_note`。
- 依赖：K0、K1a、K4a。

### K3b 续接请求组装（cli / ACP）

- 文件：`internal/job/todo_continue.go`、`internal/job/resume.go`（`resumeACPSession` 增加显式一次性参数，默认行为不变）、测试（含 `internal/acp/acptest` 的 ACP 用例）。
- 动作：
  - 按设计全字段表组装；JobRequest 全字段分类的反射测试。
  - prompt：用 K0 的 `renderTemplate` + `applyTemplateMeta`（Worktree 忽略）+ 承接说明 + `buildPromptSections`（真实 agent、生效时限）渲染全文；保存 `KnowledgeCapture`；清掉 Template / TemplateVars；以 `RulesResolved=true`、`SkillsResolved=true` 提交；MCP 沿用前序已解析值。
  - cli：全文交给 `resumeCLICarrier`（沿用源 job 的只读沙箱参数——由条件 10 保证一致；model / budget 经 K0 的可用性校验）。
  - ACP：一次性参数开启、`ResumedFrom` 指向前序 job，保留 `ResumeSourceAgent` 与 `AllowsLoadSession` 检查。
- 验证：`go test -race -count=1 ./internal/job/... ./internal/acp/... ./internal/runner/...`。
- 完成标准：设计「测试计划 / 派发、继承面、prompt、ACP」用例通过；ACP 续接跑完即终态、不进入 `awaiting_input`。
- 依赖：K3a。

### K3c 链长与配置

- 文件：`internal/job/todo_continue.go`、`internal/config/model.go`（项目 `continue_max_chain`）、`internal/config/loader.go`（校验 0–10）、`internal/config/editable.go`（`fieldPolicies` 登记 `projects.*.continue_max_chain`）、`config/gofer.example.yaml`、`skills/gofer-usage/references/server-config.md`。
- 动作：沿 `resumed_from` 数 TodoID 变化次数；默认 3，0 不限，上限 10；达到上限退回并写明。
- 验证：`go test -race -count=1 ./internal/job/... ./internal/config/...`。
- 完成标准：链长推导（自动续投与返工不计）与边界用例、`TestEveryServerFieldHasPolicy` 与 example 一致性测试通过。
- 依赖：K3b。

### K4b 基线、worktree 与交付

- 文件：`internal/job/model.go`（`JobRequest.BaselineSHA`，带 json tag）、`internal/job/execute.go`（`captureBaseSHA` 优先用 `BaselineSHA`）、`internal/job/hold.go`（序列化往返）、`internal/job/todo_continue.go`、`internal/job/worktree.go`（退回时以前序分支为基）。
- 动作：续接请求设 `BaselineSHA` = 前序结束点，`WorktreeBaseSHA` 不动；`todoDoneLine`、review diff、scope 检查随之只看本项；前序需要自动交付时不续接（条件 6）；链断开退回、前序有未合并 worktree 分支时新 worktree 以前序分支为基并写入 `continue_note`。
- 验证：`go test -race -count=1 ./internal/job/...`。
- 完成标准：基线用例（含前序遗留未提交改动的情形）、hold 往返、自动交付排除、退回 worktree 起点用例通过；有 worktree 的普通 resume 与 `reject --resume` 的 `WorktreeBaseSHA` 不变。
- 依赖：K3c。

### K5 web

- 文件：`web/src/api/types.ts`、`web/src/api/client.ts`、`web/src/components/PlanBoard.vue`（卡片与用量）、`web/src/views/PlanDetail.vue`、`web/src/views/Plans.vue`（todo 表单所在组件以实施时确认为准）、`web/src/utils/`（纯函数）、`skills/gofer-usage/references/web-console.md`。
- 动作：todo 表单「接着上一项的会话做」（选择前序项，默认上一项，可清除）；todo 卡片显示「接续自 <标题>」或「未接续：<原因>」；plan 用量显示缓存 token。
- 验证：web 三命令（主机 worktree 内在 `web` 目录执行 `npx vue-tsc --noEmit`、`npx vitest run`、`npx vite build --outDir ../tmp/web-dist-check-continue --emptyOutDir`；主 checkout 用工作区 `tmp/codeql-alerts/web3.cmd`）。
- 完成标准：纯函数 vitest 与 `?raw` 接线断言通过。
- 依赖：K1b、K1c。

### D1 文档

- 文件：`skills/gofer-usage/references/plans-workflows.md`（「接着上一项的会话做」小节：何时用、条件、退回、链长、worktree 与交付）、`docs/reference/http-api.md`、`CHANGELOG.md`、设计稿「实施记录」。
- 验证：设计稿校验器通过；人工核对 CHANGELOG。
- 依赖：K0–K5。

### V 验收与发版（监督者）

- 全量：Linux `-race`、Windows 全量、darwin vet、web 三命令。
- 发版：按 `gofer-release-flow`。
- 依赖：D1。

### M 实测对比（监督者）

- **环境**：每组一个一次性克隆（工作区 `tmp/` 下）+ 一个临时 gofer 项目（指向该克隆、关闭 `worktree_default`）；A / B 各自的 4 项在同一目录里串行执行，代码状态自然衔接；跑完删除克隆、临时项目与实测 plan / todo。
- **任务组**：按标准挑选——同一子系统、可单独验证、预计耗时相近、没有外部依赖；任务书与质量判定规则在开跑前发给用户过目。
- **分组**：A 组普通派发（`--after prev`）；B 组 `--continue prev`；可选 A' 组（新会话 + 同样跳过规则 / prime / 工具段注入）。每组 3 次：A / B 共 24 个 job（含 A' 共 36 个）。开跑前确认 suga 的 `max_concurrent` 与主机负载，避免排队影响耗时可比性。
- **试点**：先跑 1 条 B 组链（4 个 job），确认 suga stream-json 的 usage 是本轮值、cache_read 与 `tool_calls` 有值，再跑全量。
- **数据**：脚本按 todo 汇总每个 job 的 input / output / cache_read / cache_write、wall time、`tool_calls`；价格表写在脚本里并注明来源与日期，cache_read 用折扣价；逐项记录 cache_read 曲线。
- **质量**：脚本导出每项的 diff 与测试结果，剥离 job id、标题、todo 说明、`continue_note`、分支名并打乱编号，由监督者按事先写定的规则盲审。
- **结论**：写进设计稿「实施记录」，据此更新 skill 推荐与默认链长上限（如需要，另行提交）。
- 依赖：V。

## 回滚与恢复

- 恢复点：每个任务一个或两个提交，验证不过时 `git revert`；K0 只是抽取，回退成本低；K3a 起回退时其后的 K3 / K4b 一并回退。
- 发版后回滚：对 todo `--no-continue` 即恢复普通派发；回退二进制时新列无人读。

## 人工 Gate

- **计划批准与执行请求**：用户具名批准本计划并要求开始实施（可同一句话）。
- **M 开跑前**：在回复里列出任务组与预计 job 数 / 耗时。
- **外部动作**（已有长期授权）：tag、push、CI、mirror、一键更新与 worker 升级。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| 数据（三列）与接口（CLI / HTTP / MCP） | K1a、K1b | 字段、校验、占用用例 |
| 续接位原子占用（决策 7） | K1a、K3a | 并发用例 |
| 接续条件十条、退回与原因 | K3a | 逐条用例 |
| 续接请求全字段表、prompt 完整渲染（cli / ACP） | K0、K3b | 继承面、反射分类与 prompt 用例 |
| ACP 一次性续接 | K3b | acptest 用例 |
| 链长上限（决策 2） | K3c | 推导计数与配置用例 |
| 失败转移在 todo 边界停止 | K4a | fallback 用例 |
| worktree 基线、交付（决策 6）、退回 worktree 起点（决策 8） | K4b | 基线 / 交付 / worktree 用例 |
| v1 仅本机 runner（决策 9）、只读一致（条件 10） | K3a | 条件用例 |
| 默认关闭（决策 5）、被拒即失败（决策 4） | K0、K3a、K3b | golden 对照；拒绝用例 |
| 用量汇总补缓存与归类 | K1c | PlanUsage 用例 |
| web 显示 | K5 | vitest |
| 文档 | D1、各任务 | 人工核对 |
| 实测对比 | M | 结论记录 |

## 完成 Gate 与剩余工作

- 完成 Gate：完成定义全部满足；M 的结论已写入设计稿。
- 剩余工作（另开 issue）：fork 形态、链式交付、worker / peer 支持（视实测结论）。
