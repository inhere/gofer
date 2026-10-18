<!-- template_id: plan; template_version: 1.2.0 -->
# 调用方作用范围（上限）第一期 实施计划

> 状态：Draft 0.1 / 已批准（用户 2026-10-18 经 web：「批准计划，开始实施」——同时为执行请求）
> issue：gofer-6pw0

- 思考模式：rigorous。
- 核心目标：落地设计 `docs/design/2026-10-18-caller-scope-ceilings-design.md`（Draft 0.2，已批准）的第一期（决策 9）：配置与严格解码、上限所有者持久化与继承、`submitAdmitted` 单点准入、路由「项目范围 / 读写」声明维度与覆盖测试、`canFor` 三态与只读、job 凭据读取收窄、记忆写入约束、内部作业继承、web 与文档。
- scope freeze：第二期内容（长连接复核、`/v1/ws` 主题过滤、聚合计数、禁止自审与 `job.review.others`）不在本计划；实施中发现的新需求另开 issue。
- review budget：本计划最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户具名批准本计划，或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-18 | Claude（监督会话） | 初稿：S1 配置、S2 上限所有者、S3 准入、S4a 路由声明维度（标注全部路由）、S4b 判定执行、S5 job 凭据收窄、S6 记忆 / tracker 写入、S7 内部作业、S8 web、D1 文档、V 验收 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 目标与完成定义

1. root 能在 `server.callers[].ceiling` 写上限；未知键与空上限加载失败；存在上限时本机 runner 与 exec 默认禁止；whoami 与启动日志显示上限。
2. 受限调用方直接或间接发起的提交（HTTP、job 凭据子 job、评论派发、定时、webhook、plan / todo、续投、重试、失败转移、hold 批准、update-pause 重投、内部作业）都按上限所有者经 `submitAdmitted` 单点检查；拒绝返回 `ErrCeilingDenied`（403），重投路径终止不重试。
3. 每条路由声明了「项目范围」与「读写」；覆盖测试强制；`canFor` 三态（允许 / 403 / 范围外 404）在所有带资源的路由上执行；只读调用方的写操作 403。
4. 受限调用方的 job 里的 agent，经 job 凭据读取时同样收窄到范围内。
5. 受限调用方只能写范围内项目的记忆（`global_memory` 显式允许才可写全局）。
6. 未配置上限的调用方行为与改动前一致（现有测试不改断言）。

完成定义：上述有测试覆盖并通过；Linux 全量 `-race`、Windows 全量、darwin vet、web 三命令通过；文档（含回滚 runbook）同步；发版 CI 全绿。

## 范围、排除项与授权

范围：设计 0.2 第一期（决策 9 所列）。

排除项（第二期）：长连接复核与断开、`/v1/ws` 主题按项目过滤、聚合 / 计数按范围、`job.review.others` 与禁止自审。第一期发布时文档写明这些已知限制。

授权边界：

- 计划批准不授权实施；实施需要用户另发执行请求（可与批准同一句话）。
- 执行请求覆盖：sonnet 子 agent（主 checkout，同包串行）与 suga（`--model opencode-go/deepseek-v4.1-flash`，主机 worktree，边界清楚的任务）实施并本地提交；监督者审阅、容器 `-race` 复跑、合并。
- 发版沿用已有长期授权（`gofer-release-flow`）。
- 不改用户的主机 / 容器配置；不给任何真实调用方加上限（由用户自己配置）。
- `host_or_non_offline_action=REQUIRED`（发版）。

## 输入与批准证据

- 设计：Draft 0.2，用户 2026-10-18 经 web 批准（原话「按推荐，批准设计」），决策 1–9 取推荐值。
- 设计已经一轮独立评审（针对 0.1，8 条必改全部并入 0.2）。
- 关联：gofer-6pw0；gofer-dc9l（reject 续接需 `job.submit`，已修）；权限设计 2026-10-14。
- 仓库规则：AGENTS.md G021 / G022、G032、G045 / G046、G051–G054、G053、G056。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 每调用方配置 | `server.callers[]`（`internal/config/model.go`）、`CallerConcurrencyLimit` | 部分 | 在 callers 下加 `ceiling` 子树与纯函数 `CallerCeiling(id)` | OWNER_EXTENSION | 无现成的上限字段 | — |
| CAP-02 | 提交准入单点 | `submitAdmitted`、`validate(cfg, …)`（`internal/job`） | 部分 | 在 validate 内加上限检查 | OWNER_EXTENSION | 现有准入不看调用方 | — |
| CAP-03 | 路由声明与覆盖测试 | `routeDecls`、`wrapRoutes`、`TestEveryRouteIsDeclaredOrPending`（`internal/httpapi/authz_routes.go`） | 部分 | 声明增加两个维度 | OWNER_EXTENSION | 声明没有项目与读写维度 | 两套声明漂移 → 只在 routeDecls 一处 |
| CAP-04 | 资源归属判定 | `ownerAccess` 家族、`s.can`（`authz_job_rules.go`、`authz_resolve.go`） | 部分 | 新增 `canFor` 三态，ownerAccess 改经它 | OWNER_EXTENSION | 无资源项目维度 | — |
| CAP-05 | 上限所有者 | `CallerID`（混用命名空间） | 否 | 新列 `origin_caller` 与继承规则 | OWNER_EXTENSION | `CallerID` 无法可靠解析（设计 0.2） | 两个字段并存 → `CallerID` 保留展示 / 现有语义，判定只用 `origin_caller` |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 给 `Submit` / `Validate` 加 `Admission` 参数 | CAP-02 | 删掉它后在 `submitAdmitted` / `validate` 内按 cfg 快照读上限即可，所有入口自然覆盖；加参数要改约 20 个调用点且仍会漏掉重投路径（设计 0.2） |
| 注入 `CeilingResolver` | CAP-02 | 同上：job 层已读 cfg 快照（并发限制先例），不需要注入 |

## 前置检查与 fail-closed 条件

workspace baseline：`inhere-tools/gofer`，branch `main`，HEAD `97e67410` 之后（本计划与设计批准记录待提交）。

fail-closed：

- 未配置 `ceiling` 的调用方，所有现有测试不改断言通过（每个任务都要跑相关包全量）。
- S4a 必须先于 S4b：所有路由标注完成且覆盖测试通过后，才开始执行判定。
- 任何新增 / 修改路由按 G056 重跑 `go generate ./internal/httpapi/...`。
- 上限所有者解析不到时，对受限调用方的派生链拒绝；系统作业免检清单在代码里显式登记并有测试。

## 波次与依赖

| 波次 | 任务 | 依赖与并行 |
|---|---|---|
| 一 | S1 配置；S4a 路由声明维度（标注全部路由，暂不执行） | S1 在 internal/config，S4a 在 internal/httpapi，可并行 |
| 二 | S2 上限所有者；S3 准入 | S2 依赖 S1；S3 依赖 S1、S2（同在 internal/job，串行） |
| 三 | S4b 判定执行（canFor、只读、过滤） | S4a、S1 |
| 四 | S5 job 凭据收窄；S6 记忆 / tracker 写入；S7 内部作业 | S4b；S5 / S6 / S7 都改 httpapi，串行 |
| 五 | S8 web | S1（whoami 字段）；可与第四波并行（主机 worktree） |
| 六 | D1 文档与 runbook；V 验收与发版 | 全部 |

## 任务

通用要求（每个任务）：只提交自己的文件（pathspec）；不还原、不丢弃别人的改动；不碰 `.gofer/tracker/*`；不删注释、不重排无关代码；用户可见变化同步 skill references 与 http-api（G045 / G046）；Go 验证 `GOTOOLCHAIN=go1.26.9 GOWORK=off`，测试前 `env -u CLAUDE_CODE_SESSION_ID -u GOFER_SESSION_ID`；G051 / G052。

### S1 配置与严格解码

- 文件：`internal/config/model.go`（`CallerCeiling` 类型、`ServerConfig.CallerCeiling(id)`）、`internal/config/loader.go`（严格解码 `ceiling` 子树、空上限报错、给 root 写上限报错——复用 `authz.Derive` 的根推导口径）、`internal/config/editable.go`（`server.callers.*.ceiling` 仅文件、热加载）、`internal/configlint/known.go`、`config/gofer.example.yaml`（注释示例）、`internal/httpapi/auth_handler.go`（whoami 返回本人 `ceiling`）、`internal/serve`（启动日志「已生效 N 个受限调用方」）。
- 字段：`projects`、`runners`（含 `local` / `server`）、`workers`、`agents`（glob 列表）、`allow_exec`、`global_memory`、`read_only`（`*bool`）；存在上限时 runner 默认不含本机、`allow_exec` 默认 false；显式允许本机 runner 时加载警告（经 configlint / 日志）。
- 验证：`go test -race -count=1 ./internal/config/... ./internal/configlint/... ./internal/httpapi/... ./internal/serve/...`。
- 完成标准：严格解码（未知键、拼写错误、空上限）、默认值、root 冲突、热加载、whoami 字段用例通过。

### S4a 路由声明维度（标注，不执行）

- 文件：`internal/httpapi/authz_routes.go`（声明结构增加 `scope`：`PathProject(param)` / `Resource(kind)` / `BodyProject` / `Filter` / `NoProject`；`rw`：`Mutating()` / `ReadSafe()`，GET 默认读、非 GET 默认写，覆盖必须写明）、覆盖测试（每条路由两维齐全，否则失败）。
- 动作：逐条标注全部路由（包括 `Public()` 与组外路由），初始 `ReadSafe` 集合含 `POST /v1/ws-ticket`、个人偏好写入、读语义 POST；数据面连接（隧道、attach）标 `Mutating`。本任务只写声明与覆盖测试，不改判定行为。
- 验证：`go test -race -count=1 ./internal/httpapi/...`；`go generate ./internal/httpapi/...`（若参考文档要显示新维度）。
- 完成标准：覆盖测试通过；行为零变化（现有测试不改断言）。

### S2 上限所有者持久化与继承

- 文件：`internal/jobstore`（jobs `origin_caller` 列，additive + 旧库迁移用例；plans / schedules / todos 的创建者字段按需新增）、`internal/job`（提交时确定并写入；子 job、续投、重试、失败转移、hold 批准、update-pause 重投、续接载体、todo 派发、leader 回合、评论派发的继承规则——设计「上限所有者」一节逐条实现）、`internal/httpapi`（入口层：用户请求 = 本人；job 凭据 = 所属 job 的 `origin_caller`；定时到点 / webhook = 定时创建者）。
- 动作：评论派发的判定不再用 author 名；系统作业免检清单（升级、清理等）在代码里登记；解析不到时对受限链失败关闭。
- 验证：`go test -race -count=1 ./internal/jobstore/... ./internal/job/... ./internal/httpapi/...`。
- 完成标准：设计「测试计划 / 上限所有者」每条派生路径一条用例；源 job 已清理时失败关闭。

### S3 准入单点

- 文件：`internal/job/config.go`（`validate` 内上限检查，任何副作用之前）、`internal/job/submit.go`（`submitAdmitted`）、`ErrCeilingDenied`（job 包导出）与 HTTP 状态映射（403）、重投路径（`retry_sweep.go` 用 `CancelRetry` 终止并记事件；自动续投、失败转移、wakeup、todo 派发写 `dispatch_error`、定时运行记失败并通知）。
- 检查：项目、runner（`NormalizeRunnerName`，本机需显式允许）、路由后的 worker、agent（`gateAgentOf(req)`，resume 载体按真实 agent）、exec（`Agent=exec`、`Cmd`、`Verify` argv、hooks；与 runner 是否远程无关）。
- 验证：`go test -race -count=1 ./internal/job/... ./internal/httpapi/...`。
- 完成标准：每维度用例；`ErrCeilingDenied` 终止重试；未受限调用方不受影响。

### S4b 判定执行

- 文件：`internal/httpapi/authz_resolve.go`（`decision` 带上限；`canFor` 三态）、`authz_job_rules.go`（`ownerAccess` 家族改经 `canFor`）、`authz_routes.go`（`authzGate` 读写维度 + 只读 403；`PathProject` 在 gate 判定）、各 `BodyProject` handler 绑定后调用 `s.requireProjectScope`（测试断言调用发生）、`Filter` 型列表按范围过滤、无项目资源对受限调用方视为范围外。
- 验证：`go test -race -count=1 ./internal/httpapi/... ./internal/authz/...`。
- 完成标准：表驱动覆盖每种资源类型的三态与状态码；受限调用方对所有路由的反向矩阵；只读放行 `ReadSafe`、拒绝其它写；未受限调用方不变。

### S5 job 凭据读取收窄

- 文件：`internal/httpapi/authz_resolve.go`（`can` 对 job 凭据不再一律放行，按所属 job 的 `origin_caller` 上限做项目范围检查）、`jobcredential.go`（读路由经 `canFor`）。
- 验证：`go test -race -count=1 ./internal/httpapi/...`。
- 完成标准：受限调用方 job 的 agent 读范围外 job / 日志 / 项目文件 / work item / 记忆 / tracker 得 404；SEC-01 写白名单不变；未受限调用方的 job 凭据不变。

### S6 记忆与 tracker 写入

- 文件：记忆写入 handler（`scope_key` 必须为范围内项目；全局记忆需 `global_memory`）、tracker 写入（按仓库所属项目）。
- 验证：`go test -race -count=1 ./internal/httpapi/... ./internal/jobstore/...`。
- 完成标准：范围外 / 全局写入被拒，范围内正常。

### S7 内部作业

- 文件：会话中继（messenger / inject / takeover）、交付、tracker repo sync、摘要器、管家的触发路由与提交处。
- 动作：触发路由先按资源项目判定（S4b 的声明）；内部作业的 `origin_caller` 继承触发者；免检清单测试断言不可被受限路径触发。
- 验证：`go test -race -count=1 ./internal/httpapi/... ./internal/sessionrelay/... ./internal/job/... ./internal/work/...`。
- 完成标准：受限调用方触发范围外的中继 / 交付 / sync / 摘要被拒；范围内的内部作业带上其上限。

### S8 web

- 文件：`web/src/api/types.ts`（whoami 的 `ceiling`）、`web/src/store/whoami.ts`、提交表单（NewJob / 新会话 / workflow 模板）的项目 / runner / agent 下拉按上限过滤、只读时隐藏写操作（`permissionGate` 等）、`web/src/utils/`（纯函数）、`skills/gofer-usage/references/web-console.md`。
- 验证：web 三命令。
- 完成标准：纯函数 vitest + `?raw` 接线断言。

### D1 文档与 runbook

- 文件：`docs/reference/http-api.md`（whoami `ceiling`、403 / 404 语义、`ErrCeilingDenied` 原因文本）、`docs/reference/permissions.md`（`go generate`）、`skills/gofer-usage/references/server-config.md`（`ceiling`、默认更严、威胁模型摘要、第一期已知限制）、runbook（给 agent / CI 发受限 token；**回滚前先撤销或轮换受限 token**）、CHANGELOG、设计稿实施记录。

### V 验收与发版（监督者）

- 全量：Linux `-race`、Windows 全量、darwin vet、web 三命令。
- 发版：按 `gofer-release-flow`。
- 用户实测（发版后，由用户在主机配置里给一个测试 token 加上限）：提交范围外项目被拒、范围内正常、只读 token 只能读。

## 回滚与恢复

- 恢复点：每个任务一个或两个提交；S4a 是纯标注，回退成本低；S2 / S3 回退时 S4b 起一并回退。
- 发版后回滚：先撤销或轮换受限 token（旧版本会忽略 `ceiling`），再回退二进制；新列被旧版本忽略。

## 人工 Gate

- **计划批准与执行请求**：用户具名批准本计划并要求开始实施。
- **外部动作**（已有长期授权）：tag、push、CI、mirror、一键更新与 worker 升级。
- **配置**：给真实调用方加上限由用户自己做。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| 配置、严格解码、默认更严（决策 2、3） | S1 | 配置用例 |
| 上限所有者持久化与继承、失败关闭（决策 4、8） | S2 | 每条派生路径用例 |
| 准入单点、exec 全覆盖、永久拒绝终止 | S3 | 准入与重投用例 |
| 路由项目范围与读写声明 | S4a | 覆盖测试 |
| canFor 三态、只读、范围外 404（决策 7） | S4b | 反向矩阵 |
| job 凭据收窄（决策 5） | S5 | job 凭据用例 |
| 记忆 / tracker 写入（威胁模型 3） | S6 | 写入用例 |
| 内部作业继承与免检清单 | S7 | 内部作业用例 |
| web 过滤与只读 | S8 | vitest |
| 回滚 runbook（威胁模型 4） | D1 | 人工核对 |
| 分期（决策 9） | 全部 | 第二期另立计划 |

## 完成 Gate 与剩余工作

- 完成 Gate：完成定义全部满足；V 通过。
- 剩余工作（第二期，另立计划）：长连接复核、`/v1/ws` 主题过滤、聚合计数、`job.review.others` 与禁止自审。
