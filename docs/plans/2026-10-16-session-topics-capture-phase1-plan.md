<!-- template_id: plan; template_version: 1.2.0 -->
# 会话主题、收集箱与旧协作功能退役（第一期） 实施计划

> 状态：Draft 0.2 / 待人工计划批准
> issue：gofer-e6sb

- 思考模式：rigorous。
- 核心目标：落地设计第一期——会话工作主题与查找、收集箱（随手记与分派：交给会话、新建 job、转 todo、转仓库 issue），并删除从未被使用的 presence/driver 与 supervisor（保留 plan leader 回合）。
- scope freeze：只做本计划「范围」列出的内容；信箱（设计第二期）、主机 agent 寻址（第三期）、工作区代理（gofer-9dbh）不在本计划内。实施中发现超出范围的需求按「实施发现」分类处理，产品行为、接口或安全范围的变化停下回到设计 / 计划 Gate。
- review budget：本计划最多两轮独立评审；本版为第一轮评审后的修订。
- 停止条件：用户具名批准本计划（之后另需执行请求），或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-16 | Claude（监督会话） | 初稿：退役（R1–R2）、会话主题（T1–T3）、收集箱（C1–C4）、文档与发版（D1、V） |
| 0.2 | 2026-10-16 | Claude（监督会话） | 按第一轮评审修订：退役按「先删消费方、再删提供方」拆为 R1a / R1b / R1c / R2，保留 plan leader（配置迁到顶层，设计 0.6）；补齐退役与收集箱遗漏的文件和代码路径；写明「忽略并提示」的实现位置；当前会话改用权限 T2.2 的 `SessionIDFromEnv` 与会话头，T1 / T2 依赖 T2.2；新增开工前实测 P0；查找排序放到 sessionrelay；收集箱拆 C1a / C1b、C2a / C2b、C4a / C4b；`to=job` 改为关联已提交的 job（设计 0.6）；分派后状态写明；issue id 冲突改在 server 接收推送时处理并加回归测试；punt 等通用交互功能保留；web 测试按仓库惯例；DEPRECATED 版本号按实际发版填写；基线更新 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 目标与完成定义

目标对应设计 `docs/design/2026-10-16-session-topics-mailbox-and-inbox-design.md` 的第一期与「退役（随第一期附带）」：

1. 每个会话可设置主题与范围，未设置时有推断值（只用于展示与推荐）；会话、CLI、MCP、web 都能按主题或路径查找会话，结果带命中原因。
2. 用户在 web、CLI，agent 在 MCP 里都能随手记一条想法 / 问题 / 需求 / 记录进收集箱；收集箱条目可以交给会话、关联新建的 job、转 plan todo、转仓库 issue，或归档、丢弃；分派结果回写到条目。
3. presence/driver 与 supervisor（规则作答、升级、AI 监督 job）及其 MCP 工具、CLI、web 页面、配置项、answerguard 全部删除；plan leader 回合照常工作，配置迁到顶层 `leader:`；现有配置里的 `supervisor`、`presence` 段加载时忽略并提示，server 照常启动。

完成定义：

- 下文每个任务的完成标准都满足，监督者的全量验证通过（Linux `-race`、Windows 全量、vet linux / windows / darwin、web 三命令、`go generate` 后权限参考文档无差异）。
- 发版后在主机上完成「人工 Gate / 发版后验证」列出的实测。
- 设计稿末尾补「实施记录（第一期）」。

## 范围、排除项与授权

范围：

- 会话主题与范围：存储、PATCH 接口、查找与排序、推断、SessionStart 提醒、CLI、MCP、web。
- 收集箱：`captured` 状态与 `kind`、写入面收口、消费方排除、快速记录（web、CLI、MCP）、分派（session / job / todo / issue）、目标推荐、Today 汇总卡、`gofer work park --captured`。
- server 新建 tracker issue（镜像内分配 id、同步回仓库、id 冲突处理）与 web 新建 issue 入口。
- 退役：presence、supervisor、answerguard 及其接线、路由、MCP 工具、CLI、web 页面与统计；job 请求的 `origin_agent` / `escalate_to` 字段与交互作答的 `responder` 参数；plan leader 配置迁到顶层 `leader:`；配置项改为忽略并提示。

排除项：

- 信箱（`/v1/mail`、投递器、唤醒、Stop 送达、回复串）、`agent:` 地址、presence 旧表的删除（后续版本）、工作区代理、区分 web 与 CLI 的身份。
- 保留不动：web 发消息与管家带话的语义；权限目录；roles 机制（含把角色 env 传给 MCP 子进程，`internal/job/submit.go:826`）；交互的 punt / `needs_human` 标记、待答交互的列表与作答工具（`gofer_list_pending_interactions`、`gofer_answer_interaction`、`gofer_punt_interaction`）；孤儿交互的对账。

授权边界：

- 计划批准不授权实施；实施需要用户另发执行请求。
- 执行请求覆盖：由 suga 在主机 `inhere-tools/gofer` 主 checkout 逐项实施并本地提交；监督者审阅、验证、本地提交 tracker 变化。
- 发版（tag、push、CI、mirror 上传、主机 server / worker 升级）沿用已有长期授权（项目记忆 `gofer-release-flow`）。用户主机配置文件的清理（删除 `supervisor` 段中除 `leader` 外的内容、`roles.supervisor`、`claude-sup` agent，以及把 `supervisor.leader` 挪到顶层）不在授权内，发版后提醒用户，由用户操作或另行授权。
- `host_or_non_offline_action=REQUIRED`

## 输入与批准证据

- 设计：`docs/design/2026-10-16-session-topics-mailbox-and-inbox-design.md`。Draft 0.5 由用户 2026-10-16 批准（原话「批准设计；出实施计划」，candidate commit `eb2b1e3a`，document_revision 0.5，批准记录 `2ef8bc25`）。Draft 0.6 含实施计划评审发现的三处修订（leader 配置迁移、`to=job` 改为关联、issue id 冲突处理位置），**须与本计划一并获得用户确认**。
- 设计两轮评审结论已并入设计正文；本计划第一轮评审结论已并入本版。
- 关联 issue：gofer-e6sb（本功能）、gofer-9dbh（工作区代理，后续）、gofer-kd8q（权限，T2.2 进行中）、gofer-qzv5（送达等待缺陷，排在本计划之前）。
- 仓库规则：AGENTS.md G002、G021 / G022、G024、G032、G033、G041、G045 / G046、G051–G054、G056。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 会话主题与范围的存储、修改、查找 | `agent_sessions` 表与 `ListSessionsOpts`（`internal/jobstore/sessions.go`）、`sessionrelay` 的会话列表、`GET /v1/sessions`、MCP `gofer_session_list` | 部分 | 原位扩展 jobstore 会话表、sessionrelay 查询与 httpapi 会话路由 | OWNER_EXTENSION | — | 无；不新建表 |
| CAP-02 | 识别当前会话 | 权限 T2.2 的 `SessionIDFromEnv` 与 `X-Gofer-Session-Id` 会话头；`LookupCurrentAgentSession`（取第一个活着的候选）；`resolveCurrentSession`（按 cwd，有误判） | 是 | 无 | DIRECT_REUSE | — | 只用 T2.2 的单一注入点，不新增识别路径 |
| CAP-03 | 收集箱条目 | work item（`internal/jobstore/work.go`、`internal/work`）、`Unsorted` 草稿 | 部分 | 原位扩展：新增 `captured` 状态与 `kind` 列 | OWNER_EXTENSION | — | `captured` 不进看板顺序；写入面与消费方逐一收口 |
| CAP-04 | 分派：交给会话 | `sessionrelay.Service.SendMessage`（web 发消息） | 是 | 无 | DIRECT_REUSE | — | 第一期限制见设计分期表 |
| CAP-05 | 分派：新建 job、转 todo，job 结束回写 | `POST /v1/jobs`、NewJob 的 `route.query` 预填、`work/totodo.go`、`job.Service.OnJobEnded`（`internal/job/onended.go`） | 是 | 在 core 组装处把 work 的回写注册为 job 结束回调（G022：work 不依赖 job） | DIRECT_REUSE | — | 无 |
| CAP-06 | server 新建 tracker issue 与冲突处理 | `jobstore` 的 tracker 镜像（`tracker.go`、`SyncTrackerIssue`）、`tracker.GenerateIssueID`、仓库三方合并同步（`internal/tracker/sync.go`、`sync_http.go`） | 部分 | 原位扩展 jobstore 镜像写入与 server 接收推送的冲突处理 | OWNER_EXTENSION | — | 与 web 修改 issue 共用同步通道 |
| CAP-07 | 配置旧段「忽略并提示」 | `RejectRemovedKeys`（`internal/config/include.go`，按片段探测原始 map）、非严格解码（`loader.go:377-385`） | 部分 | 原位扩展：同形的 warnings 探测 | OWNER_EXTENSION | — | 带 `DEPRECATED` 标记，三个版本后删除 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立的收集箱表与包 | CAP-03 | 删掉它后由 work item 承担，挂会话、转 todo、合并、拆分都能直接复用；独立实体只会重复这些能力 |
| 在仓库目录跑 exec job 执行 `gofer issue create` | CAP-06 | 删掉它后由 server 在镜像里直接写入，经现有同步回到仓库；exec job 要占一个 job、依赖主机路径且仍需再同步 |
| 分派 `to=job` 由 work 包代为提交 job | CAP-05 | 删掉它后由 NewJob 照常 `POST /v1/jobs` 再关联；代为提交要在 work 包重写提交校验与盖章，并引入 work → job 依赖 |

## 前置检查与 fail-closed 条件

workspace baseline（2026-10-16 修订计划时）：

- Git root：`inhere-tools/gofer`；branch `main`；HEAD `b24330ac`。
- `git status --short`：`M .gofer/tracker/issues.jsonl`（监督会话，随功能点提交）；另有权限 T2.2 的 suga job（`20261016-124950-fee8c1d7`）正在改的约 25 个文件，归该 job，提交前不得触碰。
- 依赖：主机 Go 1.26、web 的 `node_modules`、主机 suga agent。

排期前置：

- 本计划的实施排在权限 T2.2 提交与验收、以及 gofer-qzv5 修复之后；T1、T2 依赖 T2.2 提交的 `SessionIDFromEnv` 与会话头。
- 本计划任务与权限第二批其余任务（T2.3、T2.4）在同一 checkout **逐个交替派发，不并发**。

每个任务开工前（监督者）：

- 主 checkout 上没有其他正在运行的实施 job。
- `git status --short` 只有监督者的 tracker 文件；出现其他未提交改动时停下，按 Ownership Conflict 处理。
- 上一个任务的提交已通过监督者审阅。

fail-closed：

- 任何时刻配置加载都必须能读取含 `supervisor`（含 `leader`）与 `presence` 段的现有配置；让现有配置启动失败、或让 leader 回合失效的改动不得提交。
- 每个退役任务单独提交后 `go build ./...` 与相关包测试必须通过（先删消费方、再删提供方）。
- 退役删除的路由在 G056 声明表里同步删除；`TestEveryRouteIsDeclaredOrPending` 与 `TestAuthzReferenceIsUpToDate` 必须通过；新路由不允许为通过构建而放宽声明。

## 波次与依赖

| 波次 | 任务 | 依赖 |
|---|---|---|
| 零：实测 | P0 `/clear` 后会话标识实测 | 无 |
| 一：退役 | R1a 消费方（MCP、CLI、client）；R1b presence 服务端；R1c supervisor、answerguard、配置与 leader 迁移；R2 web 与文档 | R1a → R1b → R1c → R2 |
| 二：会话主题 | T1 存储与接口；T2 CLI、MCP、hook；T3 web | T1 依赖 R2 与权限 T2.2；T2 依赖 T1、P0；T3 依赖 T1 |
| 三：收集箱 | C1a 状态与 jobstore；C1b work 包与消费方；C2a 快速记录与推荐；C2b 分派；C3 server 新建 issue；C4a web 收集箱；C4b web 分派 | C1a → C1b → C2a → C2b；C2a 还依赖 T1（推荐复用查找）；C3 依赖 C1a；C4a 依赖 C2a；C4b 依赖 C2b、C3 |
| 四：收尾 | D1 文档与 CHANGELOG 汇总；V 验收与发版 | 全部 |

## 任务

所有命令的运行目录为 Git root `inhere-tools/gofer`。每个任务一个本地提交（必要时两个），英文 conventional 提交信息，提交信息临时文件写在 `tmp/`；不 push、不打 tag、不改 `.gofer/tracker/`、不重启 server / worker。

任务通用验证：`go build ./...`、`go vet ./...`、`GOOS=linux go vet ./...`、`GOOS=darwin go vet ./...`、`gofmt -l` 无输出、对改动文件的控制字符检查无输出；改动 web 时在 `web/` 跑 `npx vue-tsc --noEmit`、`npx vitest run`、`npx vite build`（仓库没有 jsdom / test-utils：逻辑抽成纯函数单测，组件接线用 `?raw` 源码断言）；新增或删除 `/v1` 路由时跑 `go generate ./internal/httpapi/...`。`DEPRECATED` 标记的版本号按实际发版版本填写（`DEPRECATED(v<发版版本>): remove in v<+3>`），并按 G032 在 `docs/reference` 或 runbook 记一笔。

### P0 `/clear` 后会话标识实测（监督者 + 用户）

- 文件：无代码改动；结果记进 gofer-e6sb 评论与本计划进度。
- 动作：在一个 Claude Code 会话里先后执行 `! echo $CLAUDE_CODE_SESSION_ID` 与 `/clear`，再执行一次 `! echo $CLAUDE_CODE_SESSION_ID`，对照 `gofer session ls` 的会话 id；若该会话配置了 gofer MCP，监督者在容器里读 MCP 进程的 `/proc/<pid>/environ` 对照。
- 验证：两次 id 与 `gofer session ls` 的对照记录。
- 完成标准：确定 Bash 环境与 MCP 环境在 `/clear` 后是否仍指向旧会话。若指向旧会话，T2 按设计已批准的推荐做法改为「hook 记录 Claude Code 进程 pid，CLI / MCP 按父进程 pid 找最新会话，环境变量作后备」；若不指向旧会话，T2 只用环境变量。两种做法都在设计范围内，不需要回设计 Gate。
- 依赖：无。

### R1a 退役：删除消费方（MCP、CLI、client）

- 文件：`internal/mcpserver/server.go`、`backend.go`、`backend_client.go`、`backend_local.go`（删除 `gofer_register` / `gofer_poll_inbox` / `gofer_post_message` / `gofer_list_presence`、`selfRegister`、`envAgentRole` 的读取、作答的 `responder` 透传）、`internal/mcpserver` 下相关测试（`presence_e2e_test.go` 删除；`answer_gate_e2e_test.go`、`plandispatch_test.go`、`review_test.go`、`server_test.go` 调整）、`internal/commands/presence.go`（删除）、`internal/commands/app.go`、`internal/commands/mcp.go`（`cr.Presence`）、`internal/commands/flagreset_test.go`、`internal/client/client.go`（presence 与带 responder 的作答方法）、`internal/agent/mcpenv.go`（只改注释里 supervisor 的示例）、`internal/testutil/testcmd/main.go`（若引用被删符号）。
- 动作：删除上述 MCP 工具、CLI 命令与 client 方法；作答改为不带 responder 的现有方法；保留 `gofer_list_pending_interactions`、`gofer_answer_interaction`、`gofer_punt_interaction`。
- 验证：`go test -count=1 ./internal/mcpserver/... ./internal/commands/... ./internal/client/...`；`go build ./...`。
- 完成标准：MCP 工具清单与 `gofer --help` 里不再有 presence 相关项；构建与测试通过（服务端 presence 仍在，下一任务删除）。
- 依赖：P0 不阻塞本任务；排期前置满足。

### R1b 退役：presence 服务端

- 文件：`internal/presence/`（删除）、`internal/core/core.go`、`internal/serve/serve.go`（presence prune）、`internal/httpapi/server.go`、`internal/httpapi/presence_handler.go` 与 `presence_handler_test.go`（删除）、`internal/httpapi/authz_routes.go`（删除 6 条声明：`/v1/agents/register`、`/v1/agents/presence`、`/v1/agents/{id}/inbox`、`/v1/agents/{id}/inbox/poll`、`/v1/agents/{id}/deregister`、`POST /v1/messages`）、`internal/httpapi/authz_inhandler_test.go`、`authz_routes_test.go`、`internal/httpapi/stats_handler.go` 与 `stats_handler_test.go`（删除 `drivers` 字段）、`internal/jobstore/presence.go`、`internal/jobstore/messages.go` 与其测试（删除 DAO；`agent_presence` 与 `messages` 表保留不读，后续版本删表）、`docs/reference/permissions.md`（生成）。
- 动作：删除 presence 服务、路由与统计字段；jobstore 去掉 DAO 与建表以外的引用。
- 验证：`go test -count=1 ./internal/httpapi/... ./internal/jobstore/... ./internal/core/... ./internal/serve/...`；`go generate ./internal/httpapi/...` 后权限参考文档只少这 6 条路由。
- 完成标准：`git grep -n "internal/presence"` 在代码中无结果；测试通过。
- 依赖：R1a。

### R1c 退役：supervisor、answerguard、job 字段与配置（含 leader 迁移）

- 文件：`internal/supervisor/`（删除）、`internal/answerguard/`（删除）、`internal/serve/serve.go`（删除 `startSupervisorLoop`、reconciler 与 `defaultSupReconcilePrompt` 一套；保留孤儿交互对账）、`internal/core/core.go`、`internal/job/model.go`、`internal/job/persistence.go`、`internal/job/resume.go`、`internal/job/submit.go`（`origin_agent` 盖章）、`internal/job/hold.go`、`internal/job/interaction.go`（`AnswerInteractionBy`、answerGuard）、`internal/job/service.go`、`internal/jobstore/jobs.go`（列清单中不再读写 `origin_agent` / `escalate_to`，列保留）、`internal/jobstore/interactions.go`（`CountSupPendingDemand`、`escalated_at` 的读写）、`CountActiveJobsByRole` 所在文件、`internal/today/cards.go:769` 与 `internal/workbench/service.go:578`（不再展示 OriginAgent）、`internal/httpapi/interaction_handler.go`（作答去掉 responder）、`internal/httpapi/stats_handler.go`（删除 `escalations_pending`）、`internal/config/model.go`、`internal/config/loader.go`、`internal/config/include.go`、`internal/config/editable.go`、`internal/config/writer.go`（保存时不误删旧段）、`internal/httpapi/config_handler.go`、对应测试。
- 动作：
  - 删除 supervisor 与 answerguard 及其接线；job 请求与结果去掉 `origin_agent`、`escalate_to`；作答一律按现有调用方规则判定。
  - leader：新增顶层 `leader:`（字段与原 `supervisor.leader` 相同），`LeaderConfig()` 先读顶层、再读 `supervisor.leader`（`// DEPRECATED(v<发版版本>): remove in v<+3>`）；两处都写时以顶层为准并提示；`editable.go` 与配置视图改为顶层键。
  - 「忽略并提示」：在 `decodeConfig` 对每个配置片段（与 `RejectRemovedKeys` 同一位置、同一形态）探测 `supervisor` 下除 `leader` 外的子键与 `presence` 段，产生提示；serve 启动、`config validate`、SIGHUP 与 `PUT /v1/config` 重载都输出；`writer.go` 保存配置时原样保留这些段，不删除、不报错。
- 验证：`go test -count=1 ./internal/job/... ./internal/jobstore/... ./internal/config/... ./internal/httpapi/... ./internal/serve/... ./internal/core/... ./internal/today/... ./internal/workbench/...`；新增用例：
  - 含 `supervisor:`（含 `leader`）与 `presence:` 段的配置能加载，产生提示，leader 照常生效；
  - 顶层 `leader:` 生效；两处都写时以顶层为准并提示；
  - 配置写在 include 片段里时同样提示；web 保存配置后旧段仍在；
  - leader 回合的现有测试全部通过。
- 完成标准：`git grep -nE "internal/(supervisor|answerguard)|AnswerInteractionBy|OriginAgent|EscalateTo"` 在代码中无结果（数据库列名的 SQL 常量除外，逐条列在汇报里）；测试通过。
- 依赖：R1b。

### R2 退役：web 与文档

- 文件：`web/src/views/DriverInbox.vue`（删除）、`web/src/views/Agents.vue`（去掉 presence 页签）、`web/src/views/Config.vue`（supervisor 区块改为顶层 leader 区块）、`web/src/components/DashboardSystem.vue`（去掉 drivers / escalations 指标）、`web/src/components/EscalationBell.vue`（删除）及引用处、`web/src/router.ts`、`web/src/api/client.ts`、`web/src/api/types.ts`、新增或调整测试、`skills/gofer-usage/`（删除 presence、driver、supervisor 说明，leader 配置改为顶层）、`docs/reference/http-api.md`、`docs/reference/web-console.md`、`docs/runbook/2026-06-29-supervisor-daemon-runbook.md`（文首标「已退役」并指向本计划）、`config/*.example.yaml`、`CHANGELOG.md`。
- 动作：删除上述页面与组件；配置页 leader 区块改为顶层键；skill 与参考文档删去对应说法；CHANGELOG「未发布 / 移除」写明删除内容、旧配置段被忽略、leader 配置的新位置、交互待答由人在 Today 与推送作答。
- 验证：web 三命令；`git grep -nE "DriverInbox|EscalationBell|gofer_poll_inbox|gofer_post_message|gofer_register|agent_presence|claude-sup|role-one:supervisor"` 在 `web/src`、`skills`、`docs/reference` 中无结果。
- 完成标准：web 路由与配置页不再出现 presence / supervisor；skill 无残留说法。
- 依赖：R1c。

### T1 会话主题：存储、查找与接口

- 文件：`internal/jobstore/sessions.go`、`internal/jobstore/store.go`（迁移）、`internal/jobstore/sessions_topic_test.go`（新）、`internal/sessionrelay/`（查找、排序与推断）、`internal/httpapi/session_handler.go`、`internal/httpapi/authz_routes.go`、`internal/httpapi/session_topic_test.go`（新）、`docs/reference/http-api.md`、`docs/reference/permissions.md`（生成）。
- 动作：
  - `agent_sessions` 增加 `topic`、`scope_json`、`topic_source`、`topic_at`（additive）。
  - 会话视图返回 `topic`、`scope`、`topic_source`；主题为空时返回推断值并标 `inferred`（不落库）：按会话所在执行机的项目根（G002：会话 cwd 是哪台机器的路径，就用该项目在那一侧的根，host_path 或 container_path 中能作为前缀的那个）求 `last_cwd` 的相对路径，加上 `peer_name`。
  - `PATCH /v1/sessions/{sid}`：只改 topic 与 scope；owner 本人可改，他人的需 `session.manage.others`（InHandler deny）；job 凭据与 worker 拒绝；scope 每项须为项目根相对路径（拒绝绝对路径与 `..`）；请求带会话头且等于目标会话时 `topic_source=self`，否则 `human`；已结束的会话拒绝。
  - 查找：在 sessionrelay 里完成匹配与排序后再截断数量（`session_handler.go:566-583` 现为先截断后过滤，排序放 handler 会丢结果）；只在可投递会话（`running`、`idle`、`waiting_reply`、`needs_attention`、`offline`）里匹配；`path` 规则为 scope 等于或是查询路径的祖先算强命中，查询路径是 scope 的祖先算弱命中；关键词匹配主题、`peer_name`、项目；排序 scope 强命中 > 主题 > `peer_name` > 项目，同等时在线优先；返回命中原因与来源；`offline` 标「可能不在线」。读他人会话沿用 `session.read.others`（filter，在排序前按调用方过滤）。
- 验证：`go test -count=1 ./internal/jobstore/... ./internal/sessionrelay/... ./internal/httpapi/...`；PATCH 的反向用例与调用断言；匹配方向与排序的表驱动用例；「先过滤后截断」用例（他人会话多于上限时自己的结果不丢）；推断值不落库用例；`go generate` 后权限参考文档多出 PATCH 一行。
- 完成标准：上述用例通过；旧库迁移用例通过。
- 依赖：R2；权限 T2.2 已提交（会话头）。

### T2 会话主题：CLI、MCP 与 hook

- 文件：`internal/client/`（topic / find 方法）、`internal/commands/session.go`（`topic`、`find` 子命令）、`internal/mcpserver/work.go`（`gofer_session_set_topic`、`gofer_session_find`）、`internal/mcpserver/backend*.go`、`internal/hookrelay/run.go`（SessionStart 提醒）、`internal/sessionrelay/` 与 `internal/httpapi/session_handler.go`（登记与心跳的回应带上 topic，供 hook 判断是否提醒）、对应测试、`skills/gofer-usage/references/sessions-relay.md`、`skills/gofer-usage/references/commands.md`。
- 动作：
  - `gofer session topic "<主题>" [--scope <路径>]… [--session <id>]`、`gofer session find <关键词|路径>`；当前会话按 P0 结论识别（`SessionIDFromEnv`，必要时加 pid 方式），识别不到或不唯一时报错要求 `--session`；不按 cwd、不读标记文件。
  - MCP 两个工具同上；standalone 模式返回需要 server 的错误。
  - SessionStart：登记回应里主题为空或为推断值时，在 `additionalContext` 注入一行提示，与现有补送、记忆注入合并，不覆盖。
- 验证：`go test -count=1 ./internal/client/... ./internal/commands/... ./internal/mcpserver/... ./internal/hookrelay/... ./internal/sessionrelay/...`；用例覆盖：多候选报错、已结束会话被拒、SessionStart 合并注入、登记回应带 topic；新增 CLI flag 登记到 commands 的 flag 注册表（G053）。
- 完成标准：用例通过；skill 写明用法与当前会话的识别方式。
- 依赖：T1、P0。

### T3 会话主题：web

- 文件：`web/src/views/Sessions.vue`、会话卡片与详情组件、`web/src/utils/`（主题显示与查找结果的纯函数）、`web/src/api/client.ts`、`web/src/api/types.ts`、新增测试、`skills/gofer-usage/references/web-console.md`。
- 动作：会话卡片与详情显示主题与范围（推断值灰显并标「推断」）；可就地编辑；会话页加查找框（显示命中原因与「可能不在线」）。
- 验证：web 三命令；纯函数单测覆盖显示规则与查找结果整理，`?raw` 断言覆盖编辑与查找的接线。
- 完成标准：测试通过；web-console 参考文档更新。
- 依赖：T1。

### C1a 收集箱：状态与 jobstore

- 文件：`internal/jobstore/work.go`、`internal/jobstore/store.go`（迁移）、对应测试。
- 动作：
  - `work_items` 增加 `kind`、`capture_source_session`（来源会话存成字段，不用 `AttachWorkSession`，以免被 `SyncAll` 与摘要器接管）、`capture_source_job`。
  - 新增状态 `captured`，单独定义与校验，不加入 `WorkStatuses`；`ListWorkItems` 允许按 `captured` 过滤，默认列表不含它；`UpdateWorkItem` 只允许收集箱入口改入改出（以参数显式区分）；`MergeWorkItems`、`SplitWorkItem` 拒绝涉及 `captured` 条目；`WorkCounts`、`ListDueWorkItems` 排除 `captured`。
- 验证：`go test -count=1 ./internal/jobstore/...`；每个函数一个 `captured` 用例；断言 `WorkStatuses` 不含 `captured`；迁移用例。
- 完成标准：用例通过。
- 依赖：R2。

### C1b 收集箱：work 包、Today 与 CLI 回滚辅助

- 文件：`internal/work/service.go`（`Report` 显式 status、`SyncAll`、`maybeDigest`、`scanSummarize`）、`internal/work/steward.go`（`StewardUpdate`）、`internal/work/summarize.go`（prompt 枚举与 :755 采纳 status_hint）、`internal/work/x2.go`、`internal/work/health.go`、`internal/httpapi/work_handler.go`（PATCH 对 job 凭据拒绝改 `captured` 条目）、`internal/mcpserver/work.go:174`、`internal/mcpserver/steward.go:128,164`（状态枚举）、`internal/today/cards.go`（汇总卡）、`internal/today/lanes.go:145`（排除）、`internal/commands/work.go`（`park --captured`）、对应测试。
- 动作：
  - 汇报、摘要器、管家与 job 凭据都不得把条目改成或改出 `captured`，也不改动 `captured` 条目的状态；日报、摘要扫描、健康检查、到期提醒、合并建议、Today 泳道排除 `captured`；Today 增加「收集箱 N 条待处理」汇总卡。
  - `gofer work park [<id>] [--captured]`：`<id>` 与 `--captured` 二选一；`--captured` 把全部收集箱条目转为 `parked` 并写 journal；flag 登记到 G053 的注册表。
- 验证：`go test -count=1 ./internal/work/... ./internal/httpapi/... ./internal/mcpserver/... ./internal/today/... ./internal/commands/...`；每条写入路径一个「不能改 captured」用例；每个消费方一个排除用例；`park --captured` 用例与参数互斥用例。
- 完成标准：用例通过。
- 依赖：C1a。

### C2a 收集箱：快速记录与目标推荐

- 文件：`internal/work/`（capture、推荐）、`internal/httpapi/work_handler.go`、`internal/httpapi/authz_routes.go`、`internal/client/work.go`、`internal/commands/work.go`（`capture`）、`internal/mcpserver/work.go`（`gofer_work_capture`）、对应测试、`docs/reference/http-api.md`、`docs/reference/permissions.md`（生成）、`skills/gofer-usage/references/`（work 相关）。
- 动作：
  - `POST /v1/work-items/capture`（Public；job 凭据可用并记来源 job；worker 拒绝）：建 `captured` 条目，记 `kind`（只允许 idea / issue / request / note）与来源会话（会话头）。CLI `gofer work capture "<内容>" [--kind …] [--project …]`；MCP `gofer_work_capture`。
  - `GET /v1/work-items/{id}/dispatch-targets`（`InHandler(permFilter, session.read.others)`；job 凭据与 worker 拒绝）：复用 T1 的查找，按条目文本与会话主题、scope、`peer_name`、项目匹配，返回前 3 个候选与命中原因。设计未单列此路由，属「目标推荐」的实现细节。
- 验证：`go test -count=1 ./internal/work/... ./internal/httpapi/... ./internal/commands/... ./internal/mcpserver/...`；反向用例与调用断言；`kind` 校验用例；推荐数量与过滤用例；`go generate` 后权限参考文档多出 2 条路由。
- 完成标准：用例通过；skill 与 http-api 更新。
- 依赖：C1b、T1。

### C2b 收集箱：分派（会话、job、todo）与回写

- 文件：`internal/work/`（dispatch 与回写）、`internal/core/core.go`（把回写注册为 `job.Service.OnJobEnded` 回调）、`internal/httpapi/work_handler.go`、`internal/httpapi/authz_routes.go`、对应测试、`docs/reference/http-api.md`、`docs/reference/permissions.md`（生成）。
- 动作：`POST /v1/work-items/{id}/dispatch`（`InHandler(permDeny, session.reply.others, job.read.others, plan.manage.others)`；job 凭据与 worker 拒绝）：
  - `to=session`：经 `SendMessage` 发出（与 web 发消息同等信任），条目记会话 id 与「已送出」，状态改为 `active`；失败时条目保持 `captured` 并返回原因。
  - `to=job`：请求带已提交的 `job_id`，关联到条目（关联他人的 job 需 `job.read.others`），状态改为 `active`；job 结束时经回调把终态与摘要写进条目日志。
  - `to=todo`：沿用 to-todo，条目关联 todo，状态改为 `active`。
- 验证：`go test -count=1 ./internal/work/... ./internal/httpapi/... ./internal/core/...`；三个分支的反向用例与调用断言；job 结束回写用例；分派失败保持 `captured` 用例；`go generate` 后权限参考文档多出 1 条路由。
- 完成标准：用例通过；http-api 更新。
- 依赖：C2a。

### C3 server 新建 tracker issue 与 id 冲突处理

- 文件：`internal/jobstore/tracker.go`（新建、冲突时换 id 与更新关联）、`internal/httpapi/tracker_handler.go`、`internal/httpapi/tracker_repo_sync_handler.go`（接收推送）、`internal/tracker/sync_http.go`（如需配合）、`internal/httpapi/authz_routes.go`、`internal/work/`（`to=issue`）、对应测试、`docs/reference/http-api.md`、`docs/reference/permissions.md`（生成）。
- 动作：
  - `POST /v1/tracker/repos/{tracker_id}/issues`（Public，与现有 tracker issue 写路由一致；job 凭据按现有 SEC-01 tracker 写口径；worker 拒绝）：用镜像已有 issue 与 `tracker_repos.prefix` 调 `tracker.GenerateIssueID` 分配 id，写入镜像并标记为「server 新建、未被拉取」；仓库没有前缀时返回 409。
  - 冲突：server 接收仓库推送时，若推送的是新 issue（基线版本为 0）且同 id 的是「server 新建、未被拉取」的记录，在同一事务里给 server 那条换新 id、更新 `work_links` 中的关联，再接受推送；换 id 写进 issue 备注与条目日志。
  - 分派 `to=issue`：用条目内容建 issue，条目关联 issue id，状态改为 `parked`；web 标「待同步」直到该记录被仓库拉取。
- 验证：`go test -count=1 ./internal/jobstore/... ./internal/tracker/... ./internal/httpapi/... ./internal/work/...`；用例：
  - 分配的 id 带正确前缀且不与镜像已有 id 重复；无前缀 409；
  - **修复前失败**：先写一个用例证明现状会把同 id 的两条 issue 合成一条，修复后该用例改为断言「换 id、两条都保留」；
  - 用 tracker 包现有同步测试夹具验证仓库同步后本地出现 server 新建的 issue。
- 完成标准：用例通过；http-api 更新。
- 依赖：C1a。

### C4a 收集箱：web 记录与浏览

- 文件：`web/src/views/Work.vue`、`web/src/views/Board.vue`（排除 `captured`）、顶栏组件（随手记入口）、Today 视图与 store、`web/src/utils/`（纯函数）、`web/src/api/client.ts`、`web/src/api/types.ts`、新增测试、`skills/gofer-usage/references/web-console.md`。
- 动作：「待整理」区两个页签（草稿 / 收集箱）；顶栏「随手记」（手机宽度可用，选 kind）；看板不显示 `captured`；Today 汇总卡跳转到收集箱。
- 验证：web 三命令；纯函数单测与 `?raw` 断言覆盖页签、随手记提交、看板排除、汇总卡跳转。
- 完成标准：测试通过；web-console 参考文档更新。
- 依赖：C2a。

### C4b 收集箱：web 分派、NewJob 关联与新建 issue

- 文件：`web/src/components/WorkDrawer.vue`（分派对话框）、`web/src/views/NewJob.vue`（从条目预填、提交后调用 `dispatch{to:job, job_id}`）、`web/src/views/Issues.vue`（新建 issue）、`web/src/utils/`、`web/src/api/client.ts`、`web/src/api/types.ts`、新增测试、`skills/gofer-usage/references/web-console.md`。
- 动作：分派对话框（推荐候选、交给会话、新建 job、转 todo、转 issue、归档、丢弃）；NewJob 预填 prompt、项目、目录并在提交成功后关联条目；Issues 页新建 issue；「待同步」标记。
- 验证：web 三命令；纯函数单测与 `?raw` 断言覆盖各分派分支、NewJob 预填与关联、Issues 新建。
- 完成标准：测试通过；web-console 参考文档更新。
- 依赖：C2b、C3。

### D1 文档与 CHANGELOG 汇总

- 文件：`CHANGELOG.md`、`skills/gofer-usage/SKILL.md`（仅入口层有变化时）、`skills/gofer-usage/references/`、`docs/reference/http-api.md`、`docs/runbook/`（回滚：`gofer work park --captured`；leader 配置迁移说明）、设计稿末尾「实施记录（第一期）」。
- 动作：核对各任务文档无遗漏、无冲突；CHANGELOG 按新增 / 变更 / 移除整理；runbook 写回滚与配置迁移；设计稿追加实施记录（实际路径、偏离与原因），不改已批准正文。
- 验证：`python3 <idev-std>/validate_document.py --kind design <设计稿>` 通过；人工核对 CHANGELOG 覆盖每个任务的用户可见变化。
- 完成标准：文档与代码一致；skill 无 presence / driver / supervisor 守护进程残留说法。
- 依赖：R1a–C4b。

### V 验收与发版（监督者）

- 动作：审阅全部提交；全量验证（导出副本上的 Linux `-race`、主机干净 worktree 的 Windows 全量、vet 三平台、web 三命令、`go generate` 新鲜度）；按 `gofer-release-flow` 发版并上传 mirror；用户一键更新；发版后验证（见「人工 Gate」）；在 job 与 issue 上留评论。
- 完成标准：全量验证零失败；发版后验证通过；gofer-e6sb 记录第一期完成。
- 依赖：D1。

## 回滚与恢复

- 恢复点：每个任务一个提交；任一任务验证不过时 `git revert` 该提交，后续任务未开始。退役按「消费方 → 提供方」顺序，回退时按相反顺序。
- 发版后回滚：回到上一版二进制。新增列被旧版本忽略；`captured` 条目在旧版本显示为未知状态，回滚前执行 `gofer work park --captured`；server 新建、尚未同步的 issue 留在镜像里，旧版本照常同步。旧版本恢复 presence / supervisor（本就无人使用）；leader 若已改写到顶层 `leader:`，旧版本读不到，需临时把配置挪回 `supervisor.leader`（runbook 写明）。
- 配置：退役后旧配置段只被忽略，回滚前无需改配置（leader 例外，见上）。

## 人工 Gate

- **设计 0.6 修订与计划批准**：用户确认设计 0.6 的三处修订，并具名批准本计划 Draft 版本。
- **执行请求**：批准后另需用户发出执行请求，才派 suga 实施。
- **P0 实测**：需要用户在一个 Claude Code 会话里执行 `/clear` 与两次 `echo`（几分钟）。
- **外部动作**（已有长期授权覆盖）：tag、push、CI、mirror 上传、主机 server / worker 升级。
- **发版后验证**（用户参与）：
  - web：给一个会话设主题，在会话页按主题与路径查找；
  - 随手记一条，分派给会话、新建 job、转 todo、转 issue 各一次，确认回写；
  - `gofer tracker sync` 后仓库里出现 server 新建的 issue；
  - plan leader 回合照常（若启用）；启动日志出现 `supervisor` 段被忽略的提示；之后提醒用户清理主机配置（不在本计划授权内）。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| 决策 1：主题做成会话字段、推断不参与投递 | T1、T2、T3 | T1 推断不落库用例；T2 识别用例 |
| 「会话主题与查找」匹配方向、可投递会话、命中原因、`offline` 标注 | T1、T3 | T1 表驱动与先过滤后截断用例；T3 单测 |
| 推断值标注（G002 路径视角） | T1、T3 | T1 推断用例；T3 显示单测 |
| SessionStart 提醒 | T2 | T2 合并注入用例 |
| 当前会话识别（安全；`/clear`） | P0、T2 | P0 实测记录；T2 用例 |
| 决策 2：收集箱复用 work item、页签不进看板、写入面收口、`kind` | C1a、C1b、C4a | C1a / C1b 写入面与消费方用例；C4a 单测 |
| Today 汇总卡 | C1b、C4a | C1b 用例；C4a 单测 |
| 收集箱快速记录与推荐（决策 10：前 3 个候选） | C2a、C4a | C2a 用例 |
| 收集箱分派（会话、job 关联、todo）与回写（设计 0.6） | C2b、C4b | C2b 反向用例与回写用例 |
| 决策 9：转仓库 issue 由 server 新建；id 冲突处理（设计 0.6） | C3、C4b | C3 分配、修复前失败、同步用例 |
| 决策 3：presence/driver 与 supervisor 退役 | R1a、R1b、R1c、R2 | 各任务 grep 与测试 |
| plan leader 配置迁移（设计 0.6） | R1c、R2、D1 | R1c leader 用例 |
| 配置旧段忽略并提示（G032） | R1c | R1c 配置兼容用例 |
| 回滚辅助 `park --captured` 与 runbook | C1b、D1 | C1b 用例；D1 人工核对 |
| G056 路由声明 | R1b、T1、C2a、C2b、C3 | 覆盖测试与生成文档新鲜度 |
| G045 / G046 文档同步 | 各任务、D1 | D1 人工核对 |

## 完成 Gate 与剩余工作

- 完成：V 的完成标准全部满足后，在 gofer-e6sb 记录第一期完成，plan 状态改为完成。
- 剩余工作：设计第二期（信箱）在权限 T2.2 完成后另出实施计划；第三期与工作区代理（gofer-9dbh）另行规划；presence 与 messages 旧表在后续版本删除（另开 issue）；`supervisor.leader` 旧位置的兼容读取按 DEPRECATED 标记到期删除。
