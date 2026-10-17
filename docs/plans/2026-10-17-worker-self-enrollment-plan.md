<!-- template_id: plan; template_version: 1.2.0 -->
# worker 自助注册 实施计划

> 状态：Draft 0.2 / 已批准并请求执行（用户 2026-10-17：「批准计划，开始实施」；同时确认设计 0.3 的落点修正）
> issue：gofer-l1ry

- 思考模式：rigorous。
- 核心目标：落地设计 `docs/design/2026-10-17-worker-self-enrollment-design.md`（Draft 0.2 已批准；0.3 为本计划评审带来的落点修正）：`gofer worker join` 发起申请，管理员在 web 上输入配对码后接受，worker 自动领取 token 并写好配置。
- scope freeze：只做本计划「范围」列出的内容；token 哈希化、全局 HTTP 超时、自动接受不在本计划内。实施中发现超出范围的需求另开 issue。
- review budget：本计划最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户具名批准本计划，或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-17 | Claude（监督会话） | 初稿：E1 core 迁移、E2 业务层与存储、E3 HTTP 接口与权限、E4 推送与 Today、E5 `worker join`、E6 web、D1 文档、V 验收与发版 |
| 0.2 | 2026-10-17 | Claude（监督会话） | 按独立评审修订：E1 改为 config 纯函数（core 已 import wshub、httpapi 只经 `ConfigWriter` 碰 core、现有 fake writer 测试原样可用）；E2 拆为 E2a / E2b，hub 首连兜底经 serve 扇出 presence 观察者并异步处理，`AuditSink` 在 E2 定义；路由七条；E3 列表带 `enabled`；配置字段登记 `fieldPolicies`；E4 只依赖 E2、推送为无负载失效通知、补文件；改 httpapi 的任务串行；每个任务带本任务文档（G045），D1 只做 runbook / CHANGELOG / 实施记录；补文件清单、验证命令与关闭态断言 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 目标与完成定义

1. `gofer worker join --server <addr>` 发起申请后，命令行显示配对码与倒计时；管理员在 web 上输入配对码接受后，命令自动领取 token、写好 `.env` 与 `worker.yaml`，可选启动 worker。
2. 拒绝、过期、被顶替、放弃都有明确结果；接受后未领取的登记在核对指纹后自动撤销；server 中途重启不丢申请、不留无人持有 token 的 worker。
3. 匿名接口有按来源的限流、顶替、屏蔽、请求体与字段限制；来源地址不信任转发头（除非配置可信代理）。
4. 管理接口、推送主题、Today 卡片、顶栏提醒都只对持有 `worker.manage` 的人可见；job 凭据与 worker 凭据不能操作。
5. 功能默认关闭，`server.worker_enrollment.enabled` 可热加载开关。
6. `worker add` / `worker rm` 行为不变。

完成定义：上述行为有测试覆盖并通过；Linux 全量 `-race`、Windows 全量、darwin vet、web 三命令通过；文档同步；v0.169.0（或当时的下一版）发布，CI 全绿。

## 范围、排除项与授权

范围：设计稿「范围」所列全部内容。

排除项：token 哈希化、整个 HTTP server 的读写超时、无人值守自动接受、worker 握手与协议变更、server 下发 roots。

授权边界：

- 计划批准不授权实施；实施需要用户另发执行请求（「批准计划，开始实施」一类的明确表述即可）。
- 执行请求覆盖：由监督会话的 sonnet 子 agent 在主 checkout 逐项实施并本地提交（2026-10-16 起的用户安排；若用户改回 suga，按 `-a suga` 派发）；监督者审阅、复跑测试、提交 tracker 变化。
- 发版（tag、push、CI、mirror 上传、主机 server / worker 升级）沿用已有长期授权（项目记忆 `gofer-release-flow`）。
- 不改用户的主机 / 容器配置文件；开启 `worker_enrollment` 由用户自己改配置。
- `host_or_non_offline_action=REQUIRED`（发版与主机测试）。

## 输入与批准证据

- 设计：Draft 0.2，用户 2026-10-17 批准（原话「按推荐，批准设计；出实施计划」），决策 1–6 取推荐值，决定窗口 120 秒。
- 设计已经一轮独立评审（针对 0.1，8 条必改全部并入 0.2）。
- 关联 issue：gofer-l1ry。
- 仓库规则：AGENTS.md G011、G013、G021 / G022、G023、G024、G032、G045 / G046、G051–G054、G053、G056。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 登记 / 撤销 worker | `handleRegisterWorker` / 删除 handler（`internal/httpapi/worker_registration.go`）、`core.Update` 热加载 | 部分 | 抽成 `internal/config` 纯函数，handler 与本功能都在 `core.Update` 回调里调用 | OWNER_EXTENSION | 业务层不能 import httpapi（G022） | 两份登记逻辑会漂移 → 只保留 config 一份 |
| CAP-02 | worker 端写配置 | `runWorkerInit`、`renderWorkerConfig`、`upsertEnvFile`（`internal/commands/worker_init.go`） | 是 | join 调用「已有 token」分支 | DIRECT_REUSE | — | — |
| CAP-03 | 限时待决对象与过期清扫 | 决策表 `plan_decisions` 与 `startDecisionExpiryLoop`、job hold 的过期清扫 | 否（语义、权限、凭据都不同） | 参照其模式新建表与清扫 | MINIMAL_NEW_MODULE | 决策表无领取凭据、无配置写入副作用、不按权限过滤 | 新表与清扫自成一体，结束记录 30 天清理 |
| CAP-04 | 推送与可见性 | pushhub `globalTopics`、`wsTopicAllowed`、`wsTopicPerms`、EscalationBell、Today handler | 是 | 新主题 + handler 追加卡片 | THIN_ADAPTER | `pending` 快照不按权限过滤 | — |
| CAP-05 | 匿名限流 | `httpapi/ratelimit.go`（按调用方） | 否 | 新的按来源限流（组外路由） | MINIMAL_NEW_MODULE | 现有限流挂在鉴权组内、按调用方 id | 只服务本功能的三条路由 |
| CAP-06 | 长轮询 | 会话回合等待（25 秒上限 + 调用方循环） | 是 | 同一模式 | DIRECT_REUSE | — | — |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NEW-01 | CAP-03 | `internal/workerenroll`：申请状态机、配对码、清扫与启动对账 | 决策表 `plan_decisions`、job hold | 语义与凭据不同 | 扩展决策表会把领取凭据与配置副作用混进通用决策，且其读写面不按权限过滤 | 见 CAP-03 | 唯一负责申请生命周期；数据在 `worker_enrollments` 表 | 删除本功能时整包与表一起删，`worker add` 不受影响 |
| NEW-02 | CAP-05 | `internal/workerenroll` 内的按来源限流与屏蔽表 | `httpapi/ratelimit.go` | 现有限流按调用方 id、挂在鉴权组内 | 扩展它要让鉴权组内的中间件处理匿名来源，影响所有已认证路由 | 见 CAP-05 | 只服务本功能的匿名路由，内存表有上限与 TTL | 随 NEW-01 一起删除 |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 复用决策表 `plan_decisions` 存申请 | CAP-03 | 决策表的读写面（Today、ask_human、pending 快照）不按权限过滤，申请会泄露给无权限的人；领取凭据与配置写入也不属于决策语义 |
| 在申请行暂存明文 token | CAP-01 | 配置里已有一份；多一份明文增加泄露面且重启后要另行清理（设计 0.2） |

## 前置检查与 fail-closed 条件

workspace baseline（2026-10-17 写计划时）：

- Git root：`inhere-tools/gofer`；branch `main`；HEAD `a56e5446`（v0.168.0，发版中）。
- `git status --short`：本计划与设计稿两个文档、监督者的 tracker 文件。
- 依赖：容器 Go 1.26（`GOTOOLCHAIN=go1.26.9`）、主机 web 三命令脚本、主机 Windows 全量脚本。

每个任务开工前（监督者）：

- 上一个任务的提交已审阅、相关包测试已复跑。
- 同时并行的子 agent 只改互不重叠的目录；提交一律 pathspec。

fail-closed：

- E1 是零行为变化的抽取：`worker add` / `worker rm` 的现有测试（fake `registrationWriter`）必须不改断言全部通过。
- 任何新路由必须在 `routeDecls` 声明并重跑 `go generate ./internal/httpapi/...`；`TestEveryRouteIsDeclaredOrPending` 与 `TestAuthzReferenceIsUpToDate` 必须通过。
- 功能关闭（默认）时，现有行为与接口不得有任何变化；匿名路由不访问数据库；清扫仍处理遗留申请。
- 任何路径不得把 token 写进日志、审计、推送或 web 响应（领取接口除外）；E3 / E5 各有断言。

## 波次与依赖

| 波次 | 任务 | 依赖 |
|---|---|---|
| 一 | E1 登记 / 撤销抽成 config 纯函数 | 无 |
| 二 | E2a 存储与状态机核心；E2b 限流、清扫、对账、hub 兜底、配置段与装配 | E2a 依赖 E1；E2b 依赖 E2a |
| 三 | E3 HTTP 接口、限流与权限；E4 推送、Today 与顶栏数据 | E3 依赖 E2b；E4 依赖 E2b。两者都改 httpapi 包，**串行**（先 E3 后 E4），不在同一 checkout 并行 |
| 四 | E5 `gofer worker join`；E6 web | E5 依赖 E3；E6 依赖 E3、E4。E5（Go：commands、client）与 E6（web）文件不重叠，可并行 |
| 五 | D1 文档收尾；V 验收与发版 | 全部 |

## 任务

通用要求（每个任务）：

- 只提交自己的文件（pathspec 提交），英文 conventional message；不还原、不丢弃别人的改动；不碰 `.gofer/tracker/*`。
- 用户可见变化在**同一任务**里同步对应 skill references 与 `docs/reference/http-api.md`（G045 / G046）。
- Go 验证统一前缀 `GOTOOLCHAIN=go1.26.9 GOWORK=off`，测试前 `env -u CLAUDE_CODE_SESSION_ID -u GOFER_SESSION_ID`；`go build ./...`、`go vet ./...`（linux / windows / darwin）。
- 新组件有 `Close()`，测试里先停组件再关库（G051）；成功路径等待用 `internal/testutil/wait`，窗口在测试里调小（G052）。

### E1 登记与撤销抽成 config 纯函数（零行为变化）

- 文件：`internal/httpapi/worker_registration.go`、`internal/config/`（新文件，如 `worker_register.go` 与测试）。
- 动作：
  - 把 `handleRegisterWorker` 在 `core.Update` 回调里做的配置修改（id 正则、`CheckWorkerID`、写 `server.workers` / `runners` / 项目 `allowed_runners`、重复 id 错误）抽成 `config.RegisterWorkerInto(cfg *Config, id string, labels, projects []string, token string) error`；`newWorkerToken` 移为 `config.NewWorkerToken()`。
  - 把删除 handler 的配置修改抽成 `config.RemoveWorkerFrom(cfg *Config, id, expectFingerprint string) (removed bool, err error)`：指纹为空时与现有删除一致；非空时先核对 token 的 SHA-256，不一致返回 `removed=false` 且不改 cfg。另导出 `config.WorkerTokenFingerprint(token)`。
  - handler 改为在 `Update` 回调里调用这两个函数；断开连接、`recordConfigUpdate` 位置不变。
- 验证：`go test -race -count=1 ./internal/config/... ./internal/httpapi/...`。
- 完成标准：`worker_registration_test.go` 现有用例（fake `registrationWriter`）不改断言全部通过；config 新函数有单测（含指纹不一致不删除、保留名、重复 id、项目 `allowed_runners` 展开）。
- 依赖：无。

### E2a 存储与状态机核心

- 文件：`internal/jobstore/`（`worker_enrollments` 表、迁移、读写；旧库迁移用例按 `migrate_test.go` 模式）、`internal/jobstore/change.go`（`ChangeWorkerEnrollment`）、`internal/workerenroll/`（新包）。
- 动作：
  - 表与条件更新（`pending → accepting` 占位、`accepting → accepted / pending`、`accepted → collected` 等）。
  - 服务：Request（顶替同来源旧申请；全局上限）、Poll（凭据校验、可重复领取、按指纹从当前配置读 token）、Confirm、Cancel、Accept（生成 token 与指纹 → 占位 → `Registrar.Register` → accepted + 领取截止；失败回 pending）、Reject。
  - 配对码：8 位、30 字符集、常量时间比较、输错 5 次作废、不回显；领取凭据 SHA-256、错 5 次作废。
  - 窄接口：`Registrar{ Register(id, labels, projects, token) error; Remove(id, fp) (bool, error); Disconnect(id) }`、`AuditSink{ Record(kind, enrollID, actor string) }`、配置快照读取函数；业务层不 import httpapi / core。
  - 所有审计事件（request / accept / reject / expire / collect / cancel / code_mismatch / secret_mismatch）在服务层经 `AuditSink` 记录。
- 验证：`go test -race -count=1 ./internal/workerenroll/... ./internal/jobstore/...`。
- 完成标准：状态机全路径、接受与过期竞态（条件更新可确定性复现）、可重复领取、指纹不一致不交出 token、顶替、全局上限、配对码与凭据限次的用例通过；用 fake Registrar / AuditSink。
- 依赖：E1。

### E2b 限流、清扫、对账、hub 兜底、配置段与装配

- 文件：`internal/workerenroll/`（来源限流与屏蔽表、清扫、对账）、`internal/config/`（`server.worker_enrollment` 模型、校验含 CIDR 解析、Clone）、`internal/config/editable.go`（`fieldPolicies`：`trusted_proxies` 仅随文件热加载、不可经控制台改；其余可改且热加载）、`config/gofer.example.yaml`（即 `gofer init server` 模板，经 `config/embed.go` 嵌入）、`internal/wshub/hub.go`（如需新增观察者 setter）、`internal/serve/`（装配：Registrar 适配器用 `core.Update` + config 纯函数 + `cr.Hub`；presence 观察者扇出给 livepush 与本功能；清扫启动与 `Close`；启动对账）、`skills/gofer-usage/references/server-config.md`。
- 动作：
  - 来源限流与屏蔽：内存表 4096 上限、LRU + TTL；每来源每分钟 3 次、每来源 1 条未决（顶替）、拒绝时屏蔽 N 分钟。
  - 清扫（5 秒）：pending 过期；accepted 过领取截止 → `Remove(id, fp)` 后 `Disconnect`（仅当撤销成功）；`accepting` 超 30 秒走对账；结束记录 30 天后删除。**功能关闭时清扫照常处理遗留申请**，只是不接受新申请。
  - 启动对账：设计「状态机与并发」一节。
  - hub 首连兜底：presence 观察者在 serve 扇出；回调只入队，业务层异步按 worker_id 查 accepted 申请并核对配置里的 token 指纹后置 collected。
  - 配置每次请求读当前快照，不在构造时捕获 `enabled`。
- 验证：`go test -race -count=1 ./internal/workerenroll/... ./internal/config/... ./internal/serve/... ./internal/wshub/...`。
- 完成标准：清扫与对账用例（含指纹不一致不撤销、关闭态仍撤销遗留 accepted）；hub 兜底用例（含 doctor 探测提前触发首连）；运行中切换 `enabled` 的用例；`TestEveryServerFieldHasPolicy` 与 example「no drift」测试通过；livepush 原有用例不受扇出影响。
- 依赖：E2a。

### E3 HTTP 接口、限流与权限

- 文件：`internal/httpapi/`（新 handler 文件；`server.go` 组外注册四条匿名路由；`authz_routes.go` 七条声明）、`internal/client/`（join 用的方法：Enroll、PollEnroll、ConfirmEnroll、CancelEnroll）、`docs/reference/http-api.md`、`docs/reference/permissions.md`（生成）。
- 动作：
  - 四条匿名路由：`POST /v1/worker-enroll`、`GET /v1/worker-enroll/{id}`、`POST /v1/worker-enroll/{id}/confirm`、`POST /v1/worker-enroll/{id}/cancel`；`NoUserSurface()` + `external`；请求体 8 KiB（`MaxBytesReader`）、单次读超时 10 秒（`ResponseController`）、字段校验与清洗、来源解析（连接地址；仅可信代理读转发头——**不得调用现有 `clientIP`**）、调用 E2b 的限流；不存在 id 与凭据错同一 404；功能关闭 404 + 说明；长轮询单次 ≤ 25 秒。
  - 三条管理路由：`GET /v1/worker-enrollments`（`{enabled, pending_count, items}`，不含 secret_hash / token_fp）、`POST /v1/worker-enrollments/{id}/accept`、`POST /v1/worker-enrollments/{id}/reject`；`Require(worker.manage)` + `adminUserOnly`；accept 成功后 `recordConfigUpdate`（actor 为决定人）。
  - 所有七条响应 `Cache-Control: no-store`；领取响应不返回 connect url。
- 验证：`go test -race -count=1 ./internal/httpapi/... ./internal/client/...`；`go generate ./internal/httpapi/...` 后 `git diff --exit-code docs/reference/permissions.md`（生成物已提交）；`go test -count=1 -run 'TestEveryRouteIsDeclaredOrPending|TestAuthzReferenceIsUpToDate' ./internal/httpapi/`。
- 完成标准：设计「测试计划 / httpapi」各项用例通过（含伪造 `X-Forwarded-For` 不影响来源、配置可信代理时取最右不可信跳、超大 body 400、job / worker 凭据 403、关闭 404 且不访问数据库）；断言 token 不出现在日志、审计与列表响应。
- 依赖：E2b。

### E4 推送、Today 与顶栏数据

- 文件：`internal/pushhub/hub.go`（`TopicWorkerEnrollments` 常量与 `globalTopics`）、`internal/serve/livepush.go`（`ChangeWorkerEnrollment` → `Notify`）、`internal/httpapi/authz_live_conn.go`（`wsTopicAllowed` 显式分支、`wsTopicPerms`）、`internal/httpapi/today_handler.go`（持有 `worker.manage` 时追加卡片）、`internal/today/`（`worker_enroll` 卡片 kind 取值）。
- 动作：新主题为无负载失效通知；按 `worker.manage` 过滤订阅与重新声明；Today 卡片不经 Service 缓存、snooze、advice。
- 验证：`go test -race -count=1 ./internal/pushhub/... ./internal/httpapi/... ./internal/serve/... ./internal/today/...`。
- 完成标准：无权限连接订阅新主题被拒（含重新声明路径）；不带 topics 的默认订阅不包含新主题；有权限才出现 Today 卡片。
- 依赖：E2b；在 E3 提交之后开工（同包串行）。

### E5 CLI：`gofer worker join`

- 文件：`internal/commands/worker_join.go`（新，挂在 worker 命令组）、`internal/commands/flagreset_test.go`（`flagGlobals()` 登记 `workerJoinOpts`）、`skills/gofer-usage/references/commands.md`、`worker-config.md`、`setup-recipes.md`。
- 动作：设计「CLI」一节全部行为；调用 `runWorkerInit` 时照搬 `runInitDelegatedWorker`（`worker_init.go:82-96`）的「保存旧值、设全局、defer 还原」方式，`workerInitOpts.id` 设为接受时确定的 worker id，不改 `worker_init.go` 签名。
- 验证：`go test -race -count=1 -shuffle=on ./internal/commands/...`；`GOOS=windows go vet ./internal/commands/...`；`GOOS=darwin go vet ./...`。
- 完成标准：设计「测试计划 / commands」八个用例全部通过（接受、拒绝、过期、顶替、Ctrl-C 放弃、`runWorkerInit` 失败后的提示与确认、管理员改 id、已有 `worker.yaml` 不发申请，以及非回环 http 警告）；`TestFlagGlobalsRegistryComplete` 通过；测试 server + testcmd，不连真实 server。
- 依赖：E3。

### E6 web：待接受区、接受对话框与顶栏提醒

- 文件：`web/src/api/client.ts`、`types.ts`、`live.ts`（订阅新主题）、`web/src/api/today.ts`（`TodayCardKind`）与 lane 映射、`web/src/views/Today.vue`（卡片渲染）、`web/src/views/Runners.vue` 与新组件（待接受区、接受对话框）、顶栏提醒组件、`web/src/utils/`（纯函数）、`skills/gofer-usage/references/web-console.md`。
- 动作：设计「web」一节；据列表响应的 `enabled` 显示开启提示；入口只在 `can('worker.manage')` 时显示；手机宽度可用；沿用控制台现有外观。
- 验证：主机 web 三命令：`gofer job run -p my-tools-dev -a exec --runner local --sync --wait-timeout 900 --timeout 1200 --plan <plan-id> --title "E6 web 三命令" -- cmd /c "D:\work\inhere\my-tools-dev\tmp\codeql-alerts\web3.cmd"`，读工作区 `tmp/codeql-alerts/web-tsc.log`、`web-vitest.log`、`web-build.log`。
- 完成标准：纯函数 vitest（倒计时、配对码分段与校验、接受请求构造、冲突提示、关闭态提示）与 `?raw` 接线断言（权限门、顶栏、Runners 区、Today 卡片）通过。
- 依赖：E3、E4。

### D1 文档收尾

- 文件：`docs/runbook/2026-09-26-worker-init-runbook.md`（「自助注册」一节与排障）、README 中接入 worker 的说明（如有）、`CHANGELOG.md`、设计稿末尾「实施记录」。
- 动作：核对各任务已同步的 references 无遗漏、无冲突；CHANGELOG 汇总；实施记录写实际提交、路径与偏离（含「来源限流表在内存、重启即清空」的取舍）。
- 验证：设计稿校验器通过；人工核对 CHANGELOG 覆盖每个用户可见变化。
- 依赖：E1–E6。

### V 验收与发版（监督者）

- 全量：Linux `-race`、Windows 全量、darwin vet、web 三命令。
- 发版：按 `gofer-release-flow`（推送 → CI → mirror → 一键更新）。
- 用户实测（发版后，用户先在主机配置里开启 `server.worker_enrollment.enabled`）：在一台 worker 机器上 `gofer worker join --server …`，web 上输入配对码接受，确认 worker 自动上线；再试一次拒绝与一次超时。
- 依赖：D1。

## 回滚与恢复

- 恢复点：每个任务一个或两个提交；验证不过时 `git revert` 该提交。E1 若需回退，E2a 起全部不能保留（E1 只是抽取，回退成本低）。
- 发版后回滚：`enabled: false` 立即关闭；回退二进制时新表无人读；通过 join 接入的 worker 与 `worker add` 接入的相同，不受影响。

## 人工 Gate

- **计划批准**：用户具名批准本计划。
- **执行请求**：批准后用户发出执行请求才开始实施（可与批准同一句话）。
- **外部动作**（已有长期授权）：tag、push、CI、mirror 上传、主机 server / worker 升级。
- **配置**：开启 `worker_enrollment` 由用户自己改主机配置。
- **发版后实测**：见 V。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| 分层：登记 / 撤销抽成 config 纯函数 | E1 | 零行为变化 + 指纹用例 |
| 数据与状态机、领取两阶段、指纹核对 | E2a | 业务层用例 |
| 清扫、启动对账、hub 首连兜底、清理、配置段与热加载 | E2b | 清扫 / 对账 / 兜底用例 |
| 配对码（决策 2）、来源与限流、顶替、屏蔽（决策 5） | E2a、E2b、E3、E6 | 业务层与 httpapi 用例；web 纯函数 |
| 到期未领取撤销（决策 3） | E2b | 指纹一致 / 不一致用例 |
| 项目由人选择（决策 4） | E3、E5、E6 | join 无 `--project`；accept 请求带 projects |
| 默认关闭（决策 1）、配置热加载 | E2b、E3 | 关闭时 404 与旧行为不变用例 |
| 接口、字段限制、`no-store`、统一 404、权限 | E3 | httpapi 用例 |
| 推送与 Today 可见性、顶栏（决策 6） | E4、E6 | 订阅拒绝与卡片用例；web 接线断言 |
| `worker join` 全部行为 | E5 | commands 用例 |
| 文档同步 | 各任务、D1 | 人工核对 |

## 完成 Gate 与剩余工作

- 完成 Gate：完成定义全部满足、V 通过、用户实测完成或明确转为跟踪 issue。
- 剩余工作（另开 issue）：worker token 哈希化存储；HTTP server 全局读写超时。
