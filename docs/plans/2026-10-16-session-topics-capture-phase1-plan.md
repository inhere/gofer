<!-- template_id: plan; template_version: 1.2.0 -->
# 会话主题、收集箱与旧协作功能退役（第一期） 实施计划

> 状态：Draft 0.1 / 待人工计划批准
> issue：gofer-e6sb

- 思考模式：rigorous。
- 核心目标：落地设计第一期——会话工作主题与查找、收集箱（随手记与分派：交给会话、新建 job、转 todo、转仓库 issue），并删除从未被使用的 presence/driver 与 supervisor。
- scope freeze：只做本计划「范围」列出的内容；信箱（设计第二期）、主机 agent 寻址（第三期）、工作区代理（gofer-9dbh）不在本计划内。实施中发现超出范围的需求按「实施发现」分类处理，产品行为、接口或安全范围的变化停下回到设计 / 计划 Gate。
- review budget：本计划最多两轮独立评审。
- 停止条件：用户具名批准本计划（之后另需执行请求），或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-16 | Claude（监督会话） | 初稿：退役（R1–R2）、会话主题（T1–T3）、收集箱（C1–C4）、文档与发版（D1、V） |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 目标与完成定义

目标对应设计 `docs/design/2026-10-16-session-topics-mailbox-and-inbox-design.md`（Draft 0.5，已批准）的第一期与「退役（随第一期附带）」：

1. 每个会话可设置主题与范围，未设置时有推断值（只用于展示与推荐）；会话、CLI、MCP、web 都能按主题或路径查找会话，结果带命中原因。
2. 用户在 web、CLI，agent 在 MCP 里都能随手记一条想法 / 问题 / 需求 / 记录进收集箱；收集箱条目可以交给会话、新建 job、转 plan todo、转仓库 issue，或归档、丢弃；分派结果回写到条目。
3. presence/driver 与 supervisor（规则作答、升级、AI 监督 job）及其 MCP 工具、CLI、web 页面、配置项、answerguard 全部删除；现有配置里的 `supervisor`、`presence` 段加载时忽略并提示，server 照常启动。

完成定义：

- 下文每个任务的完成标准都满足，监督者的全量验证通过（Linux `-race`、Windows 全量、vet linux / windows / darwin、web 三命令、`go generate` 后权限参考文档无差异）。
- 发版后在主机上完成「人工 Gate / 发版后验证」列出的实测。
- 设计稿末尾补「实施记录（第一期）」。

## 范围、排除项与授权

范围：

- 会话主题与范围：存储、PATCH 接口、查找与排序、推断、SessionStart 提醒、CLI、MCP、web。
- 收集箱：`captured` 状态与 `kind`、写入面收口、消费方排除、快速记录（web、CLI、MCP）、分派（session / job / todo / issue）、目标推荐、Today 汇总卡、`gofer work park --captured`。
- server 新建 tracker issue（镜像内分配 id、同步回仓库、id 冲突处理）与 web 新建 issue 入口。
- 退役：presence、supervisor、answerguard 及其接线、路由、MCP 工具、CLI、web 页面；job 请求里的 `origin_agent` / `escalate_to` 字段与交互作答的 `responder` 参数；配置项改为忽略并提示。

排除项：

- 信箱（`/v1/mail`、投递器、唤醒、Stop 送达、回复串）、`agent:` 地址、presence 旧表的删除（后续版本）、工作区代理、区分 web 与 CLI 的身份。
- 不改 web 发消息（`POST /v1/sessions/{sid}/messages`）与管家带话的语义；不改权限目录。

授权边界：

- 计划批准不授权实施；实施需要用户另发执行请求。
- 执行请求覆盖：由 suga 在主机 `inhere-tools/gofer` 主 checkout 逐项实施并本地提交；监督者审阅、验证、本地提交 tracker 变化。
- 发版（tag、push、CI、mirror 上传、主机 server / worker 升级）沿用已有长期授权（项目记忆 `gofer-release-flow`）。用户主机配置文件的清理（删除 `supervisor` 段、`roles.supervisor`、`claude-sup` agent）不在授权内，发版后提醒用户，由用户操作或另行授权。
- `host_or_non_offline_action=REQUIRED`

## 输入与批准证据

- 设计：`docs/design/2026-10-16-session-topics-mailbox-and-inbox-design.md`，Draft 0.5，用户 2026-10-16 原话「批准设计；出实施计划」；candidate commit `eb2b1e3a`，document_revision 0.5。批准记录提交 `2ef8bc25`。
- 两轮设计评审结论已并入设计正文。
- 关联 issue：gofer-e6sb（本功能）、gofer-9dbh（工作区代理，后续）、gofer-kd8q（权限，进行中）。
- 仓库规则：AGENTS.md G021 / G022、G032、G033、G041、G045 / G046、G051–G054、G056。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 会话主题与范围的存储、修改、查找 | `agent_sessions` 表与 `ListSessionsOpts`（`internal/jobstore/sessions.go`）、`GET /v1/sessions`、MCP `gofer_session_list` | 部分 | 原位扩展 jobstore 会话表与查询、httpapi 会话路由 | OWNER_EXTENSION | — | 无；不新建表 |
| CAP-02 | 识别当前会话 | `client.LookupCurrentAgentSession`（环境变量）、`resolveCurrentSession`（cwd，有误判）、hook stdin | 是 | 无 | DIRECT_REUSE | — | 不再新增按 cwd 识别的路径 |
| CAP-03 | 收集箱条目 | work item（`internal/jobstore/work.go`、`internal/work`）、`Unsorted` 草稿 | 部分 | 原位扩展：新增 `captured` 状态与 `kind` 列 | OWNER_EXTENSION | — | `captured` 不进看板顺序，写入面收口防止被现有自动逻辑改写 |
| CAP-04 | 分派：交给会话 | `sessionrelay.Service.SendMessage`（web 发消息） | 是 | 无 | DIRECT_REUSE | — | 第一期限制见设计分期表 |
| CAP-05 | 分派：新建 job、转 todo | `POST /v1/jobs` / `job.Service.Submit`、`work/totodo.go`、NewJob 的 `route.query` 预填 | 是 | 无 | DIRECT_REUSE | — | 无 |
| CAP-06 | server 新建 tracker issue | `jobstore` 的 tracker 镜像（`tracker.go`）、`tracker.GenerateIssueID`、版本化同步 | 部分 | 原位扩展 jobstore 镜像写入与 tracker 同步的冲突处理 | OWNER_EXTENSION | — | 与 web 修改 issue 共用同步通道 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立的收集箱表与包 | CAP-03 | 删掉它后由 work item 承担，挂会话、转 todo、合并、拆分都能直接复用；独立实体只会重复这些能力 |
| 在仓库目录跑 exec job 执行 `gofer issue create` | CAP-06 | 删掉它后由 server 在镜像里直接写入，经现有同步回到仓库；exec job 要占一个 job、依赖主机路径且仍需再同步 |

## 前置检查与 fail-closed 条件

workspace baseline（2026-10-16 写计划时）：

- Git root：`inhere-tools/gofer`；branch `main`；HEAD `2ef8bc25`。
- `git status --short`：`M .gofer/tracker/issues.jsonl`（监督会话的 tracker 记录，归监督者，随功能点提交）。
- 并行工作：权限第二批 T2.2 的 suga job（`20261016-124950-fee8c1d7`）正在同一主 checkout 运行。
- 依赖：主机 Go 1.26（xenv 按 go.mod 切换）、web 的 `node_modules`、主机 suga agent。

前置检查（每个任务开工前由监督者确认）：

- 主 checkout 上没有其他正在运行的实施 job；本计划的任务与权限第二批的任务**逐个交替派发，不并发**（同一 checkout，避免互相踩）。
- `git status --short` 只有监督者的 tracker 文件；出现其他未提交改动时停下，按 Ownership Conflict 处理。
- 上一个任务的提交已通过监督者审阅。

fail-closed：

- 配置加载在删除 `supervisor` / `presence` 结构后必须仍能读取含这两段的现有配置；任何让现有配置启动失败的改动不得提交。
- 退役删除的路由在 G056 声明表里同步删除；`TestEveryRouteIsDeclaredOrPending` 与 `TestAuthzReferenceIsUpToDate` 必须通过。
- 新路由未声明即无法构建（`wrapRoutes` 会 panic），不允许为通过构建而放宽。

## 波次与依赖

| 波次 | 任务 | 依赖 |
|---|---|---|
| 一：退役 | R1 server 侧删除；R2 MCP、CLI、web、文档侧删除 | R2 依赖 R1 |
| 二：会话主题 | T1 存储与接口；T2 CLI、MCP、hook；T3 web | T2、T3 依赖 T1 |
| 三：收集箱 | C1 状态与写入面；C2 快速记录与分派（session / job / todo）；C3 server 新建 issue；C4 web | C2 依赖 C1；C3 独立；C4 依赖 C2、C3，并用到 T1 的查找做推荐 |
| 四：收尾 | D1 文档与 CHANGELOG 汇总；V 验收与发版 | 全部 |

## 任务

所有命令的运行目录为 Git root `inhere-tools/gofer`（相对路径以它为准）。每个任务一个本地提交（必要时两个），英文 conventional 提交信息；提交信息临时文件写在 `tmp/`；不 push、不打 tag、不改 `.gofer/tracker/`、不重启 server / worker。任务通用验证：`go build ./...`、`go vet ./...`、`GOOS=linux go vet ./...`、`GOOS=darwin go vet ./...`、`gofmt -l` 无输出、对改动文件的控制字符检查无输出；改动 web 时在 `web/` 跑 `npx vue-tsc --noEmit`、`npx vitest run`、`npx vite build`。新增或删除 `/v1` 路由时跑 `go generate ./internal/httpapi/...`。

### R1 退役：server 侧删除 presence、supervisor、answerguard

- 文件：`internal/presence/`（删除）、`internal/supervisor/`（删除）、`internal/answerguard/`（删除）、`internal/core/core.go`、`internal/serve/serve.go`（`startSupervisorLoop`、reconciler、presence prune）、`internal/httpapi/server.go`、`internal/httpapi/presence_handler.go`（删除）、`internal/httpapi/authz_routes.go`、`internal/httpapi/interaction_handler.go`、`internal/job/model.go`、`internal/job/hold.go`、`internal/job/interaction.go`、`internal/job/service.go`、`internal/client/client.go`、`internal/config/model.go`、`internal/config/editable.go`、`internal/config/loader.go`、`internal/httpapi/config_handler.go`、对应测试。
- 动作：
  - 删除三个包及其在 core / serve / httpapi 的接线；删除 6 条 presence 路由（`/v1/agents/register`、`/v1/agents/presence`、`/v1/agents/{id}/inbox`、`/v1/agents/{id}/inbox/poll`、`/v1/agents/{id}/deregister`、`POST /v1/messages`）及其声明。
  - job 请求与结果去掉 `origin_agent`、`escalate_to` 字段（数据库列保留、不再读写）；交互作答去掉 `responder` 参数与 `AnswerInteractionBy`，作答一律按现有调用方规则判定。
  - 配置：删除 `SupervisorConfig` 与 presence TTL 配置的使用；loader 遇到 `supervisor`、`presence` 段时忽略并在启动日志给一条提示（标 `// DEPRECATED(v0.163): remove in v0.166`）；`editable.go` 与配置视图删除对应项。
  - `roles` 机制（E35）保留不动。
- 验证：`go test -count=1 ./internal/job/... ./internal/httpapi/... ./internal/config/... ./internal/core/... ./internal/serve/... ./internal/client/...`；新增用例：含 `supervisor:` 与 `presence:` 段的配置能加载并产生提示；`go generate ./internal/httpapi/...` 后 `docs/reference/permissions.md` 只少了这 6 条路由。
- 完成标准：`git grep -nE "internal/(presence|supervisor|answerguard)"` 在非文档代码中无结果；上述测试全绿；现有主机配置样式（含 `supervisor` 段）在测试里加载成功。
- 依赖：无（第一个任务）。

### R2 退役：MCP、CLI、web 与文档

- 文件：`internal/mcpserver/server.go`、`backend.go`、`backend_client.go`、`backend_local.go`（删除 `gofer_register` / `gofer_poll_inbox` / `gofer_post_message` / `gofer_list_presence` 与 `selfRegister`、`responder` 透传）、`internal/mcpserver/*_test.go`、`internal/commands/presence.go`（删除）及命令注册、`web/src/views/DriverInbox.vue`（删除）、`web/src/views/Agents.vue`（去掉 presence 页签）、`web/src/views/Config.vue`（去掉 supervisor 区块）、`web/src/router.ts`、`web/src/api/client.ts`、`web/src/api/types.ts`、`skills/gofer-usage/`（删除 presence、supervisor、driver 相关说明）、`docs/reference/http-api.md`、`config/*.example.yaml`、`CHANGELOG.md`。
- 动作：删除上述 MCP 工具、CLI、web 页面与 API 客户端；skill 与 http-api 删去对应段落；示例配置删去 `supervisor` 段；CHANGELOG「未发布 / 移除」写明删除内容、旧配置段被忽略、交互待答由人在 Today 与推送作答。
- 验证：`go test -count=1 ./internal/mcpserver/... ./internal/commands/...`；web 三命令；`git grep -niE "presence|gofer_poll_inbox|DriverInbox|supervisor" -- skills web/src internal/mcpserver internal/commands` 只剩与本次删除说明无关的合法用法（逐条列在汇报里）。
- 完成标准：MCP 工具清单、`gofer --help`、web 路由里都不再有 presence / supervisor；skill 无残留说法。
- 依赖：R1。

### T1 会话主题：存储与接口

- 文件：`internal/jobstore/sessions.go`、`internal/jobstore/store.go`（迁移）、`internal/jobstore/sessions_topic_test.go`（新）、`internal/httpapi/session_handler.go`、`internal/httpapi/authz_routes.go`、`internal/httpapi/session_topic_test.go`（新）、`docs/reference/http-api.md`、`docs/reference/permissions.md`（生成）。
- 动作：
  - `agent_sessions` 增加 `topic`、`scope_json`、`topic_source`、`topic_at`（additive）；会话视图返回 `topic`、`scope`、`topic_source`，主题为空时返回推断值并标 `inferred`（按 `last_cwd` 相对项目根与 `peer_name` 生成，不落库）。
  - `PATCH /v1/sessions/{sid}`：只改 topic 与 scope；owner 本人可改，他人的需 `session.manage.others`（InHandler deny）；job 凭据与 worker 拒绝；scope 每项须为项目根相对路径（拒绝绝对路径与 `..`）。设置后 `topic_source` 按调用方式记 `self`（带会话标识）或 `human`。
  - `GET /v1/sessions` 增加 `q`、`path`：只在可投递会话（`running`、`idle`、`waiting_reply`、`needs_attention`、`offline`）里匹配；`path` 规则为 scope 等于或是查询路径的祖先算强命中，查询路径是 scope 的祖先算弱命中；关键词匹配主题、`peer_name`、项目；排序 scope 强命中 > 主题 > `peer_name` > 项目，同等时在线优先；返回每条的命中原因；`offline` 标「可能不在线」；推断值参与排序时标注。读他人会话沿用 `session.read.others`（filter）。
- 验证：`go test -count=1 ./internal/jobstore/... ./internal/httpapi/...`；反向用例与调用断言覆盖 PATCH；排序与匹配方向的表驱动用例；`go generate` 后权限参考文档多出 PATCH 一行。
- 完成标准：上述用例通过；旧库迁移用例通过；推断值不落库（用例断言）。
- 依赖：R2。

### T2 会话主题：CLI、MCP 与 hook

- 文件：`internal/client/`（会话 topic / find 方法）、`internal/commands/session.go`（`topic`、`find` 子命令）、`internal/mcpserver/work.go`（`gofer_session_set_topic`、`gofer_session_find`）、`internal/mcpserver/backend*.go`、`internal/hookrelay/run.go`（SessionStart 提醒）、对应测试、`skills/gofer-usage/references/sessions-relay.md`、`skills/gofer-usage/references/commands.md`。
- 动作：
  - `gofer session topic "<主题>" [--scope <路径>]… [--session <id>]`、`gofer session find <关键词|路径>`；不带 `--session` 时只用 `LookupCurrentAgentSession` 识别当前会话，不唯一或识别不到时报错要求 `--session`；不按 cwd、不读标记文件。
  - MCP 两个工具同上；standalone 模式返回需要 server 的错误。server 拒绝以已结束会话的名义设置主题（覆盖 `/clear` 后环境变量仍指向旧会话的情况）。
  - SessionStart：主题为空或仍是推断值时，在 `additionalContext` 里注入一行提示（如何用一句话设定主题），与现有的补送与记忆注入合并，不覆盖。
- 验证：`go test -count=1 ./internal/client/... ./internal/commands/... ./internal/mcpserver/... ./internal/hookrelay/...`；用例覆盖：环境变量多候选报错、已结束会话被拒、SessionStart 合并注入。
- 完成标准：用例通过；skill 写明用法与「当前会话只按环境变量识别」。
- 依赖：T1。

### T3 会话主题：web

- 文件：`web/src/views/Sessions.vue`、会话卡片与详情组件、`web/src/api/client.ts`、`web/src/api/types.ts`、新增测试、`skills/gofer-usage/references/web-console.md`。
- 动作：会话卡片与详情显示主题与范围（推断值灰显并标「推断」）；可就地编辑；会话页加查找框（调 `q` / `path`，显示命中原因与「可能不在线」）。
- 验证：web 三命令；vitest 覆盖显示、编辑提交与查找结果渲染。
- 完成标准：测试通过；web-console 参考文档更新。
- 依赖：T1。

### C1 收集箱：状态与写入面

- 文件：`internal/jobstore/work.go`、`internal/jobstore/store.go`（迁移）、`internal/work/service.go`、`internal/work/steward.go`、`internal/work/summarize.go`、`internal/httpapi/work_handler.go`、`internal/mcpserver/work.go`、`internal/mcpserver/steward.go`、`internal/today/cards.go`、对应测试。
- 动作：
  - `work_items` 增加 `kind`；新增状态 `captured`，单独定义与校验，不加入 `WorkStatuses` 看板顺序。
  - 写入面：`work.Service.Report` 的显式 status 与摘要器 status_hint 拒绝 `captured` 且不改动 `captured` 条目的状态；`StewardUpdate` 同上；`PATCH /v1/work-items/{id}` 对 job 凭据拒绝改 `captured` 条目，user 类调用方改出 `captured` 视同归档；`SetWorkItemAutoStatus` 不改写（收集箱条目 `status_source=human`）；摘要器 prompt 与 MCP 工具描述的状态枚举不列 `captured`。
  - 消费方：`Summary.Open` 计数、health 与到期提醒、合并建议排除 `captured`；Today 不为单条出卡片，改为「收集箱 N 条待处理」汇总卡。
  - `gofer work park --captured`：把全部收集箱条目转为 `parked` 并写 journal（回滚辅助）。
- 验证：`go test -count=1 ./internal/jobstore/... ./internal/work/... ./internal/httpapi/... ./internal/mcpserver/... ./internal/today/...`；每条写入路径一个「不能改 captured」用例；消费方排除用例；`park --captured` 用例。
- 完成标准：用例通过；看板顺序未变（用例断言 `WorkStatuses` 不含 `captured`）。
- 依赖：R2（避免与退役改动同文件冲突）。

### C2 收集箱：快速记录与分派（会话、job、todo）

- 文件：`internal/work/`（capture 与 dispatch 服务）、`internal/httpapi/work_handler.go`、`internal/httpapi/authz_routes.go`、`internal/client/work.go`、`internal/commands/work.go`、`internal/mcpserver/work.go`、对应测试、`docs/reference/http-api.md`、`docs/reference/permissions.md`（生成）、`skills/gofer-usage/references/`（work 相关）。
- 动作：
  - `POST /v1/work-items/capture`（Public；job 凭据可用并记来源 job；worker 拒绝）：建 `captured` 条目，记 `kind`、来源（human / agent，agent 带识别到的会话）。CLI `gofer work capture "<内容>" [--kind …] [--project …]`；MCP `gofer_work_capture`。
  - `GET /v1/work-items/{id}/dispatch-targets`：按条目文本与会话主题、scope、`peer_name`、项目匹配，返回前 3 个候选及命中原因（复用 T1 的查找）；读他人会话按 `session.read.others` 过滤。
  - `POST /v1/work-items/{id}/dispatch`（`InHandler(permDeny, session.reply.others, job.submit, plan.manage.others)`，job 凭据与 worker 拒绝）：
    - `to=session`：经 `SendMessage` 发出（与 web 发消息同等信任），条目记「已送出」与会话 id；失败时条目保持 `captured` 并返回原因。
    - `to=job`：请求体与 `POST /v1/jobs` 相同，提交后条目关联 job；job 结束时把结果写进条目日志。
    - `to=todo`：沿用 to-todo。
  - 分派成功后条目离开 `captured`（交给会话、新建 job 进入 `active`，转 todo 按 to-todo 现状）。
- 验证：`go test -count=1 ./internal/work/... ./internal/httpapi/... ./internal/commands/... ./internal/mcpserver/...`；反向用例与调用断言覆盖 dispatch 三个权限分支；job 结束回写用例；`go generate` 后权限参考文档多出 3 条路由。
- 完成标准：用例通过；skill 与 http-api 更新。
- 依赖：C1、T1。

### C3 server 新建 tracker issue

- 文件：`internal/jobstore/tracker.go`、`internal/tracker/`（同步冲突处理）、`internal/httpapi/tracker_handler.go`、`internal/httpapi/authz_routes.go`、`internal/work/`（`to=issue` 分派）、对应测试、`docs/reference/http-api.md`、`docs/reference/permissions.md`（生成）。
- 动作：
  - `POST /v1/tracker/repos/{tracker_id}/issues`（Public，与现有 tracker issue 写路由一致；job 凭据按现有 SEC-01 tracker 写口径；worker 拒绝）：用镜像里已有 issue 与 `tracker_repos.prefix` 调 `tracker.GenerateIssueID` 分配 id，写入镜像（新 rev），返回 issue；仓库没有前缀时返回 409 并说明。
  - 同步冲突：仓库推上来的本地新 issue 与 server 新建的同 id 而创建时间、标题不同时，保留仓库一侧，server 一侧换新 id 并在 issue 备注里记下旧 id；有用例。
  - 分派 `to=issue`：用条目内容建 issue，条目关联 issue id，web 标「待同步」直到镜像显示已被仓库同步。
- 验证：`go test -count=1 ./internal/jobstore/... ./internal/tracker/... ./internal/httpapi/... ./internal/work/...`；用例：分配 id 带正确前缀且不与镜像已有 id 重复；无前缀 409；冲突重新分配；仓库 `gofer tracker sync` 后本地出现该 issue（用 tracker 包现有同步测试夹具）。
- 完成标准：用例通过；http-api 更新。
- 依赖：C1（共用条目关联字段）。

### C4 收集箱：web

- 文件：`web/src/views/Work.vue`、`web/src/components/WorkDrawer.vue`、顶栏组件（随手记入口）、Today 相关视图、`web/src/views/Issues.vue`（新建 issue）、`web/src/views/NewJob.vue`（从条目预填并回传关联）、`web/src/api/client.ts`、`web/src/api/types.ts`、新增测试、`skills/gofer-usage/references/web-console.md`。
- 动作：「待整理」区两个页签（草稿 / 收集箱）；顶栏「随手记」（手机宽度可用）；条目抽屉里的分派对话框（推荐候选、交给会话、新建 job、转 todo、转 issue、归档、丢弃）；新建 job 从条目预填 prompt、项目、目录，提交后关联条目；Today 汇总卡；Issues 页新建 issue。
- 验证：web 三命令；vitest 覆盖页签、随手记提交、分派对话框各分支、NewJob 预填与关联、Today 汇总卡。
- 完成标准：测试通过；web-console 参考文档更新。
- 依赖：C2、C3。

### D1 文档与 CHANGELOG 汇总

- 文件：`CHANGELOG.md`、`skills/gofer-usage/SKILL.md`（仅当入口层有变化，如「★ 开发流程速查」加一句随手记与会话主题）、`skills/gofer-usage/references/`、`docs/reference/http-api.md`、设计稿末尾「实施记录（第一期）」。
- 动作：核对各任务的文档改动无遗漏、无冲突；CHANGELOG 按新增 / 变更 / 移除整理；设计稿追加实施记录（实际路径、偏离与原因），不改已批准正文。
- 验证：`python3 <idev-std>/validate_document.py --kind design <设计稿>` 通过；人工核对 CHANGELOG 覆盖每个任务的用户可见变化。
- 完成标准：文档与代码一致；skill 无「presence / supervisor / driver」残留说法。
- 依赖：R1–C4。

### V 验收与发版（监督者）

- 动作：审阅全部提交；全量验证（导出副本上的 Linux `-race`、主机干净 worktree 的 Windows 全量、vet 三平台、web 三命令、`go generate` 新鲜度）；按 `gofer-release-flow` 发版并上传 mirror；用户一键更新；发版后验证（见「人工 Gate」）；在 job 与 issue 上留评论。
- 完成标准：全量验证零失败；发版后验证通过；gofer-e6sb 记录第一期完成。
- 依赖：D1。

## 回滚与恢复

- 恢复点：每个任务一个提交；任一任务验证不过时 `git revert` 该提交即可，后续任务未开始。
- 发版后回滚：回到上一版二进制。新增的列被旧版本忽略；`captured` 条目在旧版本显示为未知状态，回滚前执行 `gofer work park --captured`；server 新建、尚未同步的 issue 留在镜像里，旧版本照常同步。退役删除的功能本就无人使用，回滚后旧版本恢复它们，不影响数据。
- 配置：退役后旧配置段只被忽略，不需要为回滚改配置。

## 人工 Gate

- **计划批准**：本计划 Draft 版本需用户具名批准。
- **执行请求**：批准后另需用户发出执行请求，才派 suga 实施。
- **外部动作**（已有长期授权覆盖）：tag、push、CI、mirror 上传、主机 server / worker 升级。
- **发版后验证**（用户参与）：
  - web：给一个会话设主题，在会话页按主题与路径查找；
  - 随手记一条，分派给会话、新建 job、转 todo、转 issue 各一次，确认回写；
  - `gofer tracker sync` 后仓库里出现 server 新建的 issue；
  - 在一个 Claude Code 会话里 `/clear` 后调用 MCP `gofer_session_set_topic`，确认被拒而不是写到旧会话；
  - 启动日志出现 `supervisor` 段被忽略的提示，之后提醒用户清理主机配置（不在本计划授权内）。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| 决策 1：主题做成会话字段、推断不参与投递 | T1、T2、T3 | T1 推断不落库用例；T2 环境变量识别用例 |
| 「会话主题与查找」匹配方向、可投递会话、命中原因 | T1、T3 | T1 表驱动用例；T3 vitest |
| 决策 2：收集箱复用 work item、页签不进看板、写入面收口 | C1、C4 | C1 写入面与消费方用例；C4 vitest |
| 收集箱分派（会话、job、todo）与状态回写 | C2、C4 | C2 反向用例与回写用例 |
| 决策 9：转仓库 issue 由 server 新建 | C3、C4 | C3 分配、冲突、同步用例 |
| 决策 3：presence/driver 与 supervisor 退役 | R1、R2 | R1 配置兼容用例与 grep；R2 grep 与 web 测试 |
| 决策 10：默认数值（第一期涉及：推荐 3 个候选） | C2 | C2 推荐数量用例 |
| 当前会话只按环境变量识别（安全） | T2 | T2 用例 |
| G056 路由声明 | R1、T1、C2、C3 | 覆盖测试与生成文档新鲜度 |
| G045 / G046 文档同步 | 各任务、D1 | D1 人工核对 |

## 完成 Gate 与剩余工作

- 完成：V 的完成标准全部满足后，在 gofer-e6sb 记录第一期完成，plan 状态改为完成。
- 剩余工作：设计第二期（信箱）在权限 T2.2 完成后另出实施计划；第三期与工作区代理（gofer-9dbh）另行规划；presence 旧表在后续版本删除（另开 issue）。
