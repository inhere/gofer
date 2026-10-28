# 权限参考

> 生成文件：由 `go generate ./internal/httpapi/...` 从服务端代码写出，请勿手工编辑；
> 改了权限目录或路由声明就重新运行这条命令。

gofer 的**权限**描述一个**用户类调用方**（人的 CLI、web 控制台、直接连 server 的 agent 会话）能做什么。
worker 用自己的令牌、job 用自己的凭据，两者都不走这套权限，各自受自己的白名单约束（见路由表的「其他凭据」列）。

## 怎么判定

- **根管理员**持有全部权限，不受任何开关限制：`server.callers` 里标了 `can_admin: true` 的调用方；
  一个调用方都没声明的部署里，唯一的 `default` 就是根管理员。用 `allow_empty_token` 且没有 callers 的部署没有根管理员。
- 其他用户类调用方的有效权限 = 「来源」为「配置」的那些（每次加载配置重算，只读）
  ∪ 数据库里的授权：发给全体用户类调用方的（首次启用时把「来源」为「种入」的那些发给它们），
  以及发给这个调用方自己的；`gofer auth whoami` 显示当前持有哪些、每项从哪来。
- 授权存的是**模式**（`tunnel.*`、`job.*.others`、`*`）：任何模式都匹配不到 `auth.manage`；通配符不会命中
  「危险」级权限——危险级只能字面写出（例如 `job.submit`），这样以后目录新增危险级权限时，旧的通配符不会悄悄扩大。
- **过滤型**权限（例如 `job.read.others`、`session.read.others`）缺少时不报错，只返回调用方自己的数据；
  其余权限缺少时返回 `403`，响应里指出缺的是哪一项。

## 权限目录

| 权限 | 覆盖 | 风险 | 授予会话 | 可限定范围 | 来源 |
|---|---|---|---|---|---|
| `job.read.others` | 读他人的 job、workflow、workbench 线程、interaction 列表（含 `GET /v1/interactions`）、wakeup / retry、SSE 流；过滤型 | 中 | 是 | 是 | 种入 |
| `job.submit` | 一切产生 job 的入口：jobs、workflows、schedules 创建 / run-now / enable、plan run / resume / PATCH 开启 leader、todo dispatch 与 PATCH（改为 ready 或改 assignee 即派发）、resume / rebuild、`reject?resume=true`、会话 resume、deliver（允许接管时起 pty job）、wakeups 创建与修改、agent probe、评论 @ 派发、workbench agent 线程。这些入口只在「这次请求会产生 job」时另需本权限（InHandler） | 危险 | 否 | 是 | 种入 |
| `job.cancel.others` | 取消他人 job、workflow、retry | 中 | 是 | 是 | 种入 |
| `job.manage.others` | 他人 job 的 resume / rebuild / say / end / title / worktree 删除与合并 / deliver / workflow pick / 注入 interaction，以及对**会话 job**（ACP / pty）说话：session-ask / report-request / workbench 线程中目标为会话 job 的（产生 job 的另需 `job.submit`） | 高 | 否 | 是 | 种入 |
| `job.delete.others` | 删除、脱敏他人 job | 高 | 否 | 是 | 仅根 |
| `job.attach` | attach 自己的交互 job、看自己的 pty 录像与会话 | 中 | 是 | 是 | 配置 |
| `job.attach.others` | attach 他人 job、他人录像；全局 pty 会话列表（过滤型） | 高 | 否 | 是 | 仅根 |
| `job.review` | 验收、拒绝、批准 hold（只有人签收） | 中 | 否 | 是 | 配置 |
| `repo.push` | 推送分支与 tag：提交含 `uses: gofer/push` 步骤的 workflow（分支推 `refs/heads/`、tag 推 `refs/tags/`，永不强推，推后 `ls-remote` 核对）；另需项目 `delivery.allow_push` | 中 | 否 | 是 | 仅根 |
| `job.review.others` | 审阅（accept / reject / approve）**他人提交的** job；自己的 job 仍只需 `job.review` | 中 | 否 | 是 | 种入 |
| `job.wake.others` | 唤醒他人 job | 中 | 是 | 是 | 配置 |
| `interaction.answer` | 回答 job 的工具审批与提问、punt、非中继类决策、推送通知按钮 | 中 | 是 | 是 | 配置 |
| `session.read.others` | 他人会话的列表 / 详情 / 消息 / tail / turn / nudge / 收件箱（过滤型；outbox 不在此列） | 中 | 是 | 否 | 种入 |
| `session.reply.others` | 向他人**终端会话**说话或读其待发内容：say / messages / ack / deliver / relay / outbox / 中继类决策回答 / workbench 终端会话线程 / 目标为终端会话的 session-ask 与 report-request | 高 | 是 | 否 | 配置 |
| `session.manage.others` | 删除他人会话、开 / 释放 turn、release-takeover | 中 | 否 | 否 | 种入 |
| `session.permission.answer` | 回答**他人**终端的工具权限提示（会话 owner 自己不需要） | 高 | 否 | 否 | 仅根 |
| `session.transfer` | 转移会话归属 | 高 | 否 | 否 | 仅根 |
| `plan.manage.others` | 他人 plan 的修改 / run / pause / resume / attach / todo 增改与派发 / handoff | 中 | 否 | 是 | 种入 |
| `plan.supervise.others` | 给他人 plan 绑定 supervisor，或绑定他人会话 | 中 | 否 | 是 | 仅根 |
| `schedule.manage.others` | 他人 schedule 的删除 / 启停 / 轮换 token / run-now（run-now 另需 `job.submit`） | 中 | 否 | 是 | 种入 |
| `steward.manage` | steward start / stop / restart / ask / review，以及 notes 写入 | 高 | 否 | 否 | 种入 |
| `memory.write` | 写、删 tracker 记忆与 scoped 记忆（会注入所有 job 的 prompt），采纳记忆建议 | 危险 | 否 | 是 | 种入 |
| `tracker.sync` | 推快照、repo sync | 低 | 否 | 是 | 种入 |
| `tracker.delete` | 删除 tracker 镜像 | 高 | 否 | 是 | 配置 |
| `tunnel.open` | 开隧道连接 | 中 | 是 | 第二期 | 配置 |
| `tunnel.hosted` | 托管转发启停、写或删预设、导入预设、autostart（另需 `tunnel.open`） | 高 | 否 | 否 | 种入 |
| `tunnel.manage.others` | 停他人的转发与托管转发（隧道与转发的列表为 Public，保持现状） | 高 | 否 | 否 | 仅根 |
| `xfer.write` | 创建传输（在执行主机读写文件） | 危险 | 否 | 是 | 种入 |
| `xfer.manage.others` | 读取、下载、删除他人的传输 | 高 | 否 | 是 | 仅根 |
| `secret.scan.all` | 跨调用方秘密扫描与脱敏（过滤型） | 高 | 否 | 否 | 仅根 |
| `update.check` | 触发检查新版本（`GET /v1/update` 为 Public） | 低 | 是 | 否 | 种入 |
| `update.apply` | 一键更新执行与取消；带 workers 时另需 `worker.upgrade` | 高 | 是 | 否 | 配置 |
| `worker.upgrade` | 用 server 自身二进制升级 worker | 高 | 是 | 否 | 配置 |
| `worker.upgrade.upload` | 上传二进制；`source=staged` 也需要 | 危险 | 否 | 否 | 配置 |
| `worker.manage` | 注册、删除、重载 worker | 危险 | 否 | 否 | 配置 |
| `config.read` | `GET /v1/config`、MCP 注册表读取（`/meta`、`/agents`、`/runners` 为 Public） | 低 | 是 | 否 | 种入 |
| `mcp.test` | 测试 MCP server 连接（会在 server 上起进程） | 中 | 否 | 否 | 种入 |
| `settings.read` | 读取扩展设置（`GET /v1/settings`、单个键、导出）：键表、默认值、当前值、说明 | 低 | 是 | 是 | 种入 |
| `settings.write` | 写扩展设置（PUT / DELETE 单个键、导入）：提示词、阈值与排除名单；每次写入记审计 | 中 | 否 | 是 | 仅根 |
| `config.write` | 写配置（含 steward / work）、校验、重载 | 危险 | 否 | 否 | 配置 |
| `project.write` | 项目增删改 | 危险 | 否 | 否 | 配置 |
| `rules.write` | 规则库写入 | 危险 | 否 | 否 | 配置 |
| `skills.write` | skills 导入、更新、删除 | 危险 | 否 | 否 | 配置 |
| `auth.manage` | 管理授权（仅根管理员，任何授权都匹配不到） | 根 | 否 | 否 | 仅根 |

「覆盖」是这项权限管到哪些入口与数据。风险：「危险」= 持有者能在 server 或 worker 上执行任意代码，
或读到根管理员的 token；「高」= 影响他人工作、读写敏感内容，或改所有 agent 的输入；「中」「低」依次递减；
「根」只有根管理员持有，任何授权（含通配符）都匹配不到它。

来源：「种入」= 首次启用时发给全体用户类调用方（保持升级前的行为，以后目录新增这类权限时按版本补发）；
「配置」= 由 `server.governance` 的 `require_answer_capability` / `require_attach_capability` /
`require_tunnel_capability` / `require_admin_capability` 与 `server.callers` 的 `can_answer` / `can_attach` /
`can_tunnel` 能力位推出，只读；「仅根」= 只属于根管理员，与开关无关。

「授予会话」= 这项权限能不能授予一个终端会话（会话授权只在该会话内生效）；「可限定范围」= 以后能不能限定到某个
项目 / agent / runner，现阶段授权一律作用于全部范围。

## 路由与权限

下表是全部路由声明。`声明` 是面向**用户类调用方**的判定方式，`需要的权限` 是这条路由用到的权限名。
少数路由只在对应功能启用时才注册（例如指标、worker 的 WebSocket 握手、presence 收件箱、web 控制台的静态资源）。

- `Require`：路由门直接判定，缺少列出的任一权限就是 `403`（响应里指出缺哪一项）。
- `InHandler(deny)`：接口按资源归属自己判定——操作他人（或无主）资源时才需要列出的权限，缺了就是 `403`。
- `InHandler(filter)`：同上，但缺权限时不报错，只返回调用方自己的数据。
- `Public`：任何用户类调用方都可以调用，不查权限。
- `NoUserSurface`：没有面向用户类调用方的入口，只能由「其他凭据」列的凭据调用。

`其他凭据` 列：

- `job`：job 自己的凭据（job 内的 agent 回读日志、回传结果、写评论等），由 job 凭据白名单按路由放行，与用户权限无关。
- `worker`：worker 用自己的令牌调用的路由（握手、上报、上传与下载内容等）。
- `自身凭据`：这条路由不走用户权限，自己判断凭据——一次性 ticket、schedule 的触发 token、metrics token、
  push 动作 token，或完全公开的探活。
- `静态`：web 控制台的单页与静态资源。

`项目范围` 与 `读写` 两列只是标注（调用方范围上限的判定尚未读取它们）：

- `PathProject(参数)`：项目键在路径参数里；`Resource(种类)`：项目来自被访问的记录；`BodyProject`：项目在请求体里，由接口绑定后检查；
  `Filter`：列表或聚合，结果须按范围过滤；`NoProject`：与项目无关（服务器全局的管理、元信息、本人偏好）。
- `读写`：默认 GET / HEAD 为读，其余为写；括号里的 `ReadSafe` / `Mutating` 是显式覆盖（只读调用方可调的非 GET，或能写入的 GET 数据面连接）。

| 方法 | 路径 | 声明 | 需要的权限 | 其他凭据 | 项目范围 | 读写 |
|---|---|---|---|---|---|---|
| `GET` | `/*` | `NoUserSurface` | — | `静态` | `NoProject` | 读 |
| `GET` | `/health` | `NoUserSurface` | — | `自身凭据` | `NoProject` | 读 |
| `GET` | `/metrics` | `NoUserSurface` | — | `自身凭据` | `NoProject` | 读 |
| `GET` | `/v1/agents` | `Public` | — | — | `NoProject` | 读 |
| `POST` | `/v1/agents/{id}/probe` | `Require` | `job.submit` | — | `NoProject` | 写 |
| `POST` | `/v1/attachments` | `Public` | — | — | `NoProject` | 读（ReadSafe） |
| `DELETE` | `/v1/attachments/{id}` | `Public` | — | — | `Resource(attachment)` | 读（ReadSafe） |
| `GET` | `/v1/attachments/{id}` | `Public` | — | — | `Resource(attachment)` | 读 |
| `GET` | `/v1/auth/audit` | `Require` | `auth.manage` | — | `NoProject` | 读 |
| `GET` | `/v1/auth/catalog` | `Public` | — | — | `NoProject` | 读 |
| `GET` | `/v1/auth/expand` | `Require` | `auth.manage` | — | `NoProject` | 读 |
| `GET` | `/v1/auth/grants` | `Require` | `auth.manage` | — | `NoProject` | 读 |
| `POST` | `/v1/auth/grants` | `Require` | `auth.manage` | — | `NoProject` | 写 |
| `DELETE` | `/v1/auth/grants/{id}` | `Require` | `auth.manage` | — | `NoProject` | 写 |
| `POST` | `/v1/auth/grants/{id}/activate` | `Require` | `auth.manage` | — | `NoProject` | 写 |
| `POST` | `/v1/auth/grants/{id}/renew` | `Require` | `auth.manage` | — | `NoProject` | 写 |
| `GET` | `/v1/auth/routes` | `Public` | — | — | `NoProject` | 读 |
| `GET` | `/v1/auth/whoami` | `Public` | — | — | `NoProject` | 读 |
| `GET` | `/v1/config` | `Require` | `config.read` | — | `NoProject` | 读 |
| `DELETE` | `/v1/config/agents/{key}` | `Require` | `config.write` | — | `NoProject` | 写 |
| `PUT` | `/v1/config/agents/{key}` | `Require` | `config.write` | — | `NoProject` | 写 |
| `POST` | `/v1/config/reload` | `Require` | `config.write` | — | `NoProject` | 写 |
| `PUT` | `/v1/config/server` | `Require` | `config.write` | — | `NoProject` | 写 |
| `PUT` | `/v1/config/steward` | `Require` | `config.write` | — | `NoProject` | 写 |
| `POST` | `/v1/config/validate` | `Require` | `config.write` | — | `NoProject` | 写 |
| `PUT` | `/v1/config/work` | `Require` | `config.write` | — | `NoProject` | 写 |
| `GET` | `/v1/decisions` | `Public` | — | — | `Filter` | 读 |
| `POST` | `/v1/decisions` | `Public` | — | `job` | `Resource(plan)` | 写 |
| `GET` | `/v1/decisions/{id}` | `Public` | — | — | `Resource(decision)` | 读 |
| `POST` | `/v1/decisions/{id}/answer` | `InHandler(deny)` | `interaction.answer`、`session.reply.others` | — | `Resource(decision)` | 写 |
| `GET` | `/v1/interactions` | `InHandler(filter)` | `job.read.others` | — | `Filter` | 读 |
| `GET` | `/v1/issues` | `Public` | — | `job` | `Filter` | 读 |
| `GET` | `/v1/issues/{id}` | `Public` | — | `job` | `Resource(issue)` | 读 |
| `GET` | `/v1/jobs` | `InHandler(filter)` | `job.read.others` | `job` | `Filter` | 读 |
| `POST` | `/v1/jobs` | `Require` | `job.submit` | `job` | `BodyProject` | 写 |
| `POST` | `/v1/jobs/secret-scan` | `InHandler(filter)` | `secret.scan.all` | — | `BodyProject` | 写 |
| `DELETE` | `/v1/jobs/{id}` | `InHandler(deny)` | `job.delete.others` | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}` | `InHandler(deny)` | `job.read.others` | `job` | `Resource(job)` | 读 |
| `PATCH` | `/v1/jobs/{id}` | `InHandler(deny)` | `job.manage.others` | `job` | `Resource(job)` | 写 |
| `POST` | `/v1/jobs/{id}/accept` | `InHandler(deny)` | `job.review`、`job.review.others` | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/acp/stream` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/approve` | `InHandler(deny)` | `job.review`、`job.review.others` | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/artifacts` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `GET` | `/v1/jobs/{id}/artifacts/{name}` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `GET` | `/v1/jobs/{id}/attach` | `NoUserSurface` | — | `自身凭据` | `Resource(job)` | 写（Mutating） |
| `POST` | `/v1/jobs/{id}/attach-ticket` | `InHandler(deny)` | `job.attach.others`、`job.attach` | — | `Resource(job)` | 写 |
| `POST` | `/v1/jobs/{id}/cancel` | `InHandler(deny)` | `job.cancel.others` | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/comments` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/comments` | `InHandler(deny)` | `job.submit` | `job` | `Resource(job)` | 写 |
| `POST` | `/v1/jobs/{id}/deliver` | `InHandler(deny)` | `job.manage.others` | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/deliveries` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `GET` | `/v1/jobs/{id}/diff` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/end` | `InHandler(deny)` | `job.manage.others` | `job` | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/events` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/findings/issue` | `Public` | — | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/follow-ups` | `InHandler(deny)` | `job.manage.others`、`job.submit` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/follow-ups` | `InHandler(deny)` | `job.manage.others`、`job.submit` | — | `Resource(job)` | 写 |
| `DELETE` | `/v1/jobs/{id}/follow-ups/{follow_up_id}` | `InHandler(deny)` | `job.manage.others`、`job.submit` | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/interactions` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/interactions` | `InHandler(deny)` | `job.manage.others` | — | `Resource(job)` | 写 |
| `POST` | `/v1/jobs/{id}/interactions/{interaction_id}/answer` | `Require` | `interaction.answer` | — | `Resource(job)` | 写 |
| `POST` | `/v1/jobs/{id}/interactions/{interaction_id}/punt` | `Require` | `interaction.answer` | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/logs/stderr` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `GET` | `/v1/jobs/{id}/logs/stdout` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `GET` | `/v1/jobs/{id}/pty/recording` | `InHandler(deny)` | `job.attach.others`、`job.attach` | — | `Resource(job)` | 读 |
| `GET` | `/v1/jobs/{id}/pty/sessions` | `InHandler(deny)` | `job.attach.others`、`job.attach` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/rebuild` | `InHandler(deny)` | `job.manage.others`、`job.submit` | — | `Resource(job)` | 写 |
| `POST` | `/v1/jobs/{id}/redact` | `InHandler(deny)` | `job.delete.others` | — | `Resource(job)` | 写 |
| `POST` | `/v1/jobs/{id}/reject` | `InHandler(deny)` | `job.review`、`job.review.others`、`job.submit` | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/request` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/resume` | `InHandler(deny)` | `job.manage.others`、`job.submit` | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/retries` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `GET` | `/v1/jobs/{id}/review-hints` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/say` | `InHandler(deny)` | `job.manage.others` | `job` | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/stream` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `GET` | `/v1/jobs/{id}/wakeups` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/wakeups` | `InHandler(deny)` | `job.wake.others`、`job.submit` | `job` | `Resource(job)` | 写 |
| `DELETE` | `/v1/jobs/{id}/worktree` | `InHandler(deny)` | `job.manage.others` | — | `Resource(job)` | 写 |
| `GET` | `/v1/jobs/{id}/worktree` | `InHandler(deny)` | `job.read.others` | — | `Resource(job)` | 读 |
| `POST` | `/v1/jobs/{id}/worktree/merge` | `InHandler(deny)` | `job.manage.others` | — | `Resource(job)` | 写 |
| `GET` | `/v1/mail` | `InHandler(filter)` | `session.reply.others`、`job.read.others` | `job` | `Filter` | 读 |
| `POST` | `/v1/mail` | `InHandler(deny)` | `session.reply.others`、`job.manage.others` | `job` | `BodyProject` | 写 |
| `GET` | `/v1/mail/agents` | `InHandler(filter)` | `session.reply.others`、`job.read.others` | — | `Filter` | 读 |
| `POST` | `/v1/mail/agents/{name}/end` | `InHandler(deny)` | `session.reply.others`、`job.manage.others` | — | `Resource(mail)` | 写 |
| `GET` | `/v1/mail/conversations` | `InHandler(filter)` | `session.reply.others`、`job.read.others` | `job` | `Filter` | 读 |
| `GET` | `/v1/mail/{id}` | `InHandler(deny)` | `session.reply.others`、`job.read.others` | `job` | `Resource(mail)` | 读 |
| `POST` | `/v1/mail/{id}/ack` | `InHandler(deny)` | `session.reply.others`、`job.manage.others` | `job` | `Resource(mail)` | 读（ReadSafe） |
| `POST` | `/v1/mail/{id}/cancel` | `InHandler(deny)` | `session.reply.others`、`job.manage.others` | `job` | `Resource(mail)` | 写 |
| `POST` | `/v1/mail/{id}/readdress` | `InHandler(deny)` | `session.reply.others`、`job.manage.others` | — | `Resource(mail)` | 写 |
| `POST` | `/v1/mail/{id}/redact` | `InHandler(deny)` | `job.delete.others` | — | `Resource(mail)` | 写 |
| `POST` | `/v1/mail/{id}/reply` | `InHandler(deny)` | `session.reply.others`、`job.manage.others` | `job` | `Resource(mail)` | 写 |
| `GET` | `/v1/mcp-servers` | `Require` | `config.read` | — | `NoProject` | 读 |
| `GET` | `/v1/mcp-servers/{name}` | `Require` | `config.read` | — | `NoProject` | 读 |
| `POST` | `/v1/mcp-servers/{name}/test` | `Require` | `mcp.test` | — | `NoProject` | 写 |
| `GET` | `/v1/memories` | `Public` | — | — | `Filter` | 读 |
| `POST` | `/v1/memories` | `Require` | `memory.write` | — | `BodyProject` | 写 |
| `DELETE` | `/v1/memories/{scope}/{scope_key}/{key}` | `Require` | `memory.write` | — | `Resource(memory)` | 写 |
| `GET` | `/v1/memories/{scope}/{scope_key}/{key}` | `Public` | — | — | `Resource(memory)` | 读 |
| `PUT` | `/v1/memories/{scope}/{scope_key}/{key}` | `Require` | `memory.write` | — | `Resource(memory)` | 写 |
| `DELETE` | `/v1/memories/{scope}/{scope_key}/{key}/flag` | `Require` | `memory.write` | — | `Resource(memory)` | 写 |
| `POST` | `/v1/memories/{scope}/{scope_key}/{key}/flag` | `Public` | — | `job` | `Resource(memory)` | 写 |
| `GET` | `/v1/memory-candidates` | `Public` | — | — | `Filter` | 读 |
| `POST` | `/v1/memory-candidates/{n}/accept` | `Require` | `memory.write` | — | `Resource(memory_candidate)` | 写 |
| `POST` | `/v1/memory-candidates/{n}/reject` | `Public` | — | — | `Resource(memory_candidate)` | 写 |
| `GET` | `/v1/memory-findings` | `Public` | — | `job` | `Filter` | 读 |
| `GET` | `/v1/memory-suggestions` | `Public` | — | `job` | `Filter` | 读 |
| `POST` | `/v1/memory-suggestions` | `Public` | — | `job` | `BodyProject` | 写 |
| `POST` | `/v1/memory-suggestions/{n}/adopt` | `Require` | `memory.write` | — | `Resource(memory_suggestion)` | 写 |
| `POST` | `/v1/memory-suggestions/{n}/dismiss` | `Public` | — | — | `Resource(memory_suggestion)` | 写 |
| `GET` | `/v1/memory-syntheses` | `Public` | — | `job` | `Filter` | 读 |
| `GET` | `/v1/memory-syntheses/{id}` | `Public` | — | `job` | `Filter` | 读 |
| `GET` | `/v1/meta` | `Public` | — | — | `NoProject` | 读 |
| `GET` | `/v1/notify/events` | `Require` | `config.read` | — | `NoProject` | 读 |
| `POST` | `/v1/notify/test` | `Require` | `config.write` | — | `NoProject` | 写 |
| `GET` | `/v1/nudges` | `InHandler(filter)` | `session.read.others` | — | `Filter` | 读 |
| `DELETE` | `/v1/nudges/{id}` | `InHandler(deny)` | `session.reply.others` | — | `Resource(nudge)` | 写 |
| `PATCH` | `/v1/nudges/{id}` | `InHandler(deny)` | `session.reply.others` | — | `Resource(nudge)` | 写 |
| `GET` | `/v1/plans` | `Public` | — | — | `Filter` | 读 |
| `POST` | `/v1/plans` | `Public` | — | — | `BodyProject` | 写 |
| `GET` | `/v1/plans/{id}` | `Public` | — | — | `Resource(plan)` | 读 |
| `PATCH` | `/v1/plans/{id}` | `InHandler(deny)` | `plan.manage.others`、`plan.supervise.others`、`job.submit` | — | `Resource(plan)` | 写 |
| `GET` | `/v1/plans/{id}/comments` | `Public` | — | — | `Resource(plan)` | 读 |
| `POST` | `/v1/plans/{id}/comments` | `InHandler(deny)` | `job.submit` | `job` | `Resource(plan)` | 写 |
| `GET` | `/v1/plans/{id}/events` | `Public` | — | — | `Resource(plan)` | 读 |
| `GET` | `/v1/plans/{id}/handoff` | `Public` | — | — | `Resource(plan)` | 读 |
| `PUT` | `/v1/plans/{id}/handoff` | `InHandler(deny)` | `plan.manage.others` | `job` | `Resource(plan)` | 写 |
| `GET` | `/v1/plans/{id}/handoff/history` | `Public` | — | — | `Resource(plan)` | 读 |
| `POST` | `/v1/plans/{id}/jobs` | `InHandler(deny)` | `plan.manage.others` | — | `Resource(plan)` | 写 |
| `POST` | `/v1/plans/{id}/pause` | `InHandler(deny)` | `plan.manage.others` | — | `Resource(plan)` | 写 |
| `POST` | `/v1/plans/{id}/resume` | `InHandler(deny)` | `plan.manage.others`、`job.submit` | — | `Resource(plan)` | 写 |
| `POST` | `/v1/plans/{id}/run` | `InHandler(deny)` | `plan.manage.others`、`job.submit` | — | `Resource(plan)` | 写 |
| `POST` | `/v1/plans/{id}/todos` | `InHandler(deny)` | `plan.manage.others` | — | `Resource(plan)` | 写 |
| `GET` | `/v1/plans/{id}/todos/{todo_id}/comments` | `Public` | — | — | `Resource(plan)` | 读 |
| `POST` | `/v1/plans/{id}/todos/{todo_id}/comments` | `InHandler(deny)` | `job.submit` | `job` | `Resource(plan)` | 写 |
| `GET` | `/v1/projects` | `Public` | — | — | `Filter` | 读 |
| `POST` | `/v1/projects` | `Require` | `project.write` | — | `BodyProject` | 写 |
| `DELETE` | `/v1/projects/{key}` | `Require` | `project.write` | — | `PathProject(key)` | 写 |
| `GET` | `/v1/projects/{key}` | `Public` | — | — | `PathProject(key)` | 读 |
| `PUT` | `/v1/projects/{key}` | `Require` | `project.write` | — | `PathProject(key)` | 写 |
| `GET` | `/v1/projects/{key}/file` | `Public` | — | — | `PathProject(key)` | 读 |
| `GET` | `/v1/projects/{key}/git` | `Public` | — | — | `PathProject(key)` | 读 |
| `GET` | `/v1/projects/{key}/repos` | `Public` | — | — | `PathProject(key)` | 读 |
| `GET` | `/v1/projects/{key}/templates` | `Public` | — | — | `PathProject(key)` | 读 |
| `GET` | `/v1/projects/{key}/templates/{name}` | `Public` | — | — | `PathProject(key)` | 读 |
| `GET` | `/v1/pty/sessions` | `InHandler(filter)` | `job.attach.others` | — | `Filter` | 读 |
| `POST` | `/v1/push/actions` | `NoUserSurface` | — | `自身凭据` | `NoProject` | 写 |
| `DELETE` | `/v1/push/subscriptions` | `Public` | — | — | `NoProject` | 读（ReadSafe） |
| `GET` | `/v1/push/subscriptions` | `Public` | — | — | `NoProject` | 读 |
| `POST` | `/v1/push/subscriptions` | `Public` | — | — | `NoProject` | 读（ReadSafe） |
| `POST` | `/v1/push/test` | `Public` | — | — | `NoProject` | 写 |
| `GET` | `/v1/push/vapid-public-key` | `Public` | — | — | `NoProject` | 读 |
| `DELETE` | `/v1/retries/{rid}` | `InHandler(deny)` | `job.cancel.others` | — | `Resource(retry)` | 写 |
| `GET` | `/v1/rules` | `Public` | — | — | `NoProject` | 读 |
| `DELETE` | `/v1/rules/{name}` | `Require` | `rules.write` | — | `NoProject` | 写 |
| `GET` | `/v1/rules/{name}` | `Public` | — | — | `NoProject` | 读 |
| `PUT` | `/v1/rules/{name}` | `Require` | `rules.write` | — | `NoProject` | 写 |
| `GET` | `/v1/runners` | `Public` | — | — | `NoProject` | 读 |
| `GET` | `/v1/runners/{name}/messenger/agents` | `Public` | — | — | `NoProject` | 读 |
| `GET` | `/v1/schedules` | `Public` | — | — | `Filter` | 读 |
| `POST` | `/v1/schedules` | `Require` | `job.submit` | — | `BodyProject` | 写 |
| `DELETE` | `/v1/schedules/{id}` | `InHandler(deny)` | `schedule.manage.others` | — | `Resource(schedule)` | 写 |
| `GET` | `/v1/schedules/{id}` | `Public` | — | — | `Resource(schedule)` | 读 |
| `PATCH` | `/v1/schedules/{id}` | `InHandler(deny)` | `schedule.manage.others`、`job.submit` | — | `Resource(schedule)` | 写 |
| `POST` | `/v1/schedules/{id}/disable` | `InHandler(deny)` | `schedule.manage.others` | — | `Resource(schedule)` | 写 |
| `POST` | `/v1/schedules/{id}/enable` | `InHandler(deny)` | `schedule.manage.others`、`job.submit` | — | `Resource(schedule)` | 写 |
| `GET` | `/v1/schedules/{id}/events` | `Public` | — | — | `Resource(schedule)` | 读 |
| `POST` | `/v1/schedules/{id}/rotate-token` | `InHandler(deny)` | `schedule.manage.others` | — | `Resource(schedule)` | 写 |
| `POST` | `/v1/schedules/{id}/run-now` | `InHandler(deny)` | `schedule.manage.others`、`job.submit` | — | `Resource(schedule)` | 写 |
| `POST` | `/v1/schedules/{id}/trigger` | `NoUserSurface` | — | `自身凭据` | `Resource(schedule)` | 写 |
| `DELETE` | `/v1/server/restart` | `Require` | `config.write` | — | `NoProject` | 写 |
| `GET` | `/v1/server/restart` | `Require` | `config.read` | — | `NoProject` | 读 |
| `POST` | `/v1/server/restart` | `Require` | `config.write` | — | `NoProject` | 写 |
| `POST` | `/v1/session-ask` | `InHandler(deny)` | `session.reply.others`、`job.manage.others` | `job` | `Resource(session)` | 写 |
| `GET` | `/v1/sessions` | `InHandler(filter)` | `session.read.others` | `job` | `Filter` | 读 |
| `POST` | `/v1/sessions` | `Public` | — | — | `BodyProject` | 写 |
| `DELETE` | `/v1/sessions/{sid}` | `InHandler(deny)` | `session.manage.others` | — | `Resource(session)` | 写 |
| `GET` | `/v1/sessions/{sid}` | `InHandler(deny)` | `session.read.others` | `job` | `Resource(session)` | 读 |
| `PATCH` | `/v1/sessions/{sid}` | `InHandler(deny)` | `session.manage.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/deliver` | `InHandler(deny)` | `session.reply.others`、`job.submit` | — | `Resource(session)` | 写 |
| `GET` | `/v1/sessions/{sid}/focus` | `InHandler(deny)` | `session.read.others` | — | `Resource(session)` | 读 |
| `POST` | `/v1/sessions/{sid}/heartbeat` | `Public` | — | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/held/send` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/inject-mode` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/mail/claim` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/mail/confirm` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/mail/pause` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/mail/resume` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `GET` | `/v1/sessions/{sid}/messages` | `InHandler(deny)` | `session.read.others` | — | `Resource(session)` | 读 |
| `POST` | `/v1/sessions/{sid}/messages` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `GET` | `/v1/sessions/{sid}/nudges` | `InHandler(deny)` | `session.read.others` | — | `Resource(session)` | 读 |
| `POST` | `/v1/sessions/{sid}/nudges` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `GET` | `/v1/sessions/{sid}/outbox` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 读 |
| `POST` | `/v1/sessions/{sid}/owner` | `Require` | `session.transfer` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/permissions` | `Public` | — | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/permissions/resolve` | `Public` | — | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/permissions/{id}/answer` | `InHandler(deny)` | `session.permission.answer` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/relay` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/release-takeover` | `InHandler(deny)` | `session.manage.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/replies` | `Public` | — | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/resume` | `InHandler(deny)` | `session.reply.others`、`job.submit` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/say` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `GET` | `/v1/sessions/{sid}/screen` | `InHandler(deny)` | `session.read.others` | — | `Resource(session)` | 读 |
| `GET` | `/v1/sessions/{sid}/tail` | `InHandler(deny)` | `session.read.others` | `job` | `Resource(session)` | 读 |
| `GET` | `/v1/sessions/{sid}/takeover-plan` | `InHandler(deny)` | `session.read.others` | — | `Resource(session)` | 读 |
| `POST` | `/v1/sessions/{sid}/turns` | `InHandler(deny)` | `session.manage.others` | — | `Resource(session)` | 写 |
| `GET` | `/v1/sessions/{sid}/turns/{id}` | `InHandler(deny)` | `session.read.others` | — | `Resource(session)` | 读 |
| `DELETE` | `/v1/sessions/{sid}/turns/{id}/ack` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/turns/{id}/ack` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/turns/{id}/complete-mail` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/turns/{id}/complete-watches` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/turns/{id}/release` | `InHandler(deny)` | `session.manage.others` | — | `Resource(session)` | 写 |
| `GET` | `/v1/sessions/{sid}/wait` | `InHandler(deny)` | `session.read.others` | — | `Resource(session)` | 读 |
| `GET` | `/v1/sessions/{sid}/watches` | `InHandler(deny)` | `session.read.others` | — | `Resource(session)` | 读 |
| `POST` | `/v1/sessions/{sid}/watches` | `InHandler(deny)` | `session.reply.others` | `job` | `Resource(session)` | 写 |
| `POST` | `/v1/sessions/{sid}/watches/complete` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `DELETE` | `/v1/sessions/{sid}/watches/{job_id}` | `InHandler(deny)` | `session.reply.others` | — | `Resource(session)` | 写 |
| `GET` | `/v1/settings` | `Require` | `settings.read` | — | `NoProject` | 读 |
| `GET` | `/v1/settings/export` | `Require` | `settings.read` | — | `NoProject` | 读 |
| `POST` | `/v1/settings/import` | `Require` | `settings.write` | — | `NoProject` | 写 |
| `DELETE` | `/v1/settings/{key}` | `Require` | `settings.write` | — | `NoProject` | 写 |
| `GET` | `/v1/settings/{key}` | `Require` | `settings.read` | — | `NoProject` | 读 |
| `PUT` | `/v1/settings/{key}` | `Require` | `settings.write` | — | `NoProject` | 写 |
| `GET` | `/v1/skills` | `Public` | — | — | `NoProject` | 读 |
| `POST` | `/v1/skills/import` | `Require` | `skills.write` | — | `NoProject` | 写 |
| `DELETE` | `/v1/skills/{name}` | `Require` | `skills.write` | — | `NoProject` | 写 |
| `GET` | `/v1/skills/{name}` | `Public` | — | — | `NoProject` | 读 |
| `GET` | `/v1/skills/{name}/export` | `Public` | — | — | `NoProject` | 读 |
| `POST` | `/v1/skills/{name}/update` | `Require` | `skills.write` | — | `NoProject` | 写 |
| `GET` | `/v1/stats` | `Public` | — | — | `Filter` | 读 |
| `POST` | `/v1/stats/backfill` | `Public` | — | — | `NoProject` | 写 |
| `GET` | `/v1/stats/overview` | `Public` | — | — | `Filter` | 读 |
| `GET` | `/v1/steward` | `Public` | — | `job` | `NoProject` | 读 |
| `POST` | `/v1/steward/ask` | `Require` | `steward.manage` | — | `NoProject` | 写 |
| `GET` | `/v1/steward/notes` | `Public` | — | `job` | `NoProject` | 读 |
| `PUT` | `/v1/steward/notes` | `Require` | `steward.manage` | `job` | `NoProject` | 写 |
| `POST` | `/v1/steward/restart` | `Require` | `steward.manage` | — | `NoProject` | 写 |
| `POST` | `/v1/steward/review` | `Require` | `steward.manage` | — | `NoProject` | 写 |
| `POST` | `/v1/steward/review-summary` | `Require` | `steward.manage` | `job` | `NoProject` | 写 |
| `POST` | `/v1/steward/start` | `Require` | `steward.manage` | — | `NoProject` | 写 |
| `POST` | `/v1/steward/stop` | `Require` | `steward.manage` | — | `NoProject` | 写 |
| `GET` | `/v1/today` | `Public` | — | `job` | `Filter` | 读 |
| `POST` | `/v1/today/actions` | `Public` | — | — | `Resource(today_card)` | 写 |
| `POST` | `/v1/today/advice` | `Public` | — | `job` | `Resource(today_card)` | 写 |
| `GET` | `/v1/today/handled` | `Public` | — | — | `Filter` | 读 |
| `GET` | `/v1/today/lanes` | `Public` | — | — | `Filter` | 读 |
| `POST` | `/v1/today/snooze` | `Public` | — | — | `NoProject` | 读（ReadSafe） |
| `DELETE` | `/v1/today/snooze/{key}` | `Public` | — | — | `NoProject` | 读（ReadSafe） |
| `GET` | `/v1/today/snoozed` | `Public` | — | — | `NoProject` | 读 |
| `PATCH` | `/v1/todos/{todo_id}` | `InHandler(deny)` | `plan.manage.others`、`job.submit` | `job` | `Resource(todo)` | 写 |
| `GET` | `/v1/todos/{todo_id}/comments` | `Public` | — | — | `Resource(todo)` | 读 |
| `POST` | `/v1/todos/{todo_id}/comments` | `InHandler(deny)` | `job.submit` | `job` | `Resource(todo)` | 写 |
| `POST` | `/v1/todos/{todo_id}/dispatch` | `InHandler(deny)` | `plan.manage.others`、`job.submit` | — | `Resource(todo)` | 写 |
| `GET` | `/v1/tracker/issues` | `Public` | — | — | `Resource(tracker_repo)` | 读 |
| `POST` | `/v1/tracker/issues/batch` | `Public` | — | — | `Resource(tracker_repo)` | 写 |
| `GET` | `/v1/tracker/issues/{id}` | `Public` | — | — | `Resource(tracker_repo)` | 读 |
| `PUT` | `/v1/tracker/issues/{id}` | `Public` | — | — | `Resource(tracker_repo)` | 写 |
| `POST` | `/v1/tracker/issues/{id}/comments` | `Public` | — | — | `Resource(tracker_repo)` | 写 |
| `GET` | `/v1/tracker/memories` | `Public` | — | — | `Resource(tracker_repo)` | 读 |
| `DELETE` | `/v1/tracker/memories/{id}` | `Require` | `memory.write` | — | `Resource(tracker_repo)` | 写 |
| `PUT` | `/v1/tracker/memories/{id}` | `Require` | `memory.write` | — | `Resource(tracker_repo)` | 写 |
| `GET` | `/v1/tracker/repos` | `Public` | — | — | `Filter` | 读 |
| `DELETE` | `/v1/tracker/repos/{tracker_id}` | `Require` | `tracker.delete` | — | `Resource(tracker_repo)` | 写 |
| `GET` | `/v1/tracker/repos/{tracker_id}/clients` | `Public` | — | — | `Resource(tracker_repo)` | 读 |
| `POST` | `/v1/tracker/repos/{tracker_id}/issues` | `Public` | — | — | `Resource(tracker_repo)` | 写 |
| `POST` | `/v1/tracker/repos/{tracker_id}/rename` | `Require` | `tracker.sync` | `job` | `Resource(tracker_repo)` | 写 |
| `POST` | `/v1/tracker/repos/{tracker_id}/sync` | `Require` | `tracker.sync` | — | `Resource(tracker_repo)` | 写 |
| `POST` | `/v1/tracker/sync` | `Require` | `tracker.sync` | `job` | `Resource(tracker_repo)` | 写 |
| `GET` | `/v1/tunnels` | `Public` | — | — | `NoProject` | 读 |
| `GET` | `/v1/tunnels/connect` | `InHandler(deny)` | `tunnel.open` | `自身凭据` | `NoProject` | 写（Mutating） |
| `GET` | `/v1/tunnels/forwarders` | `Public` | — | — | `NoProject` | 读 |
| `POST` | `/v1/tunnels/forwarders` | `Public` | — | — | `NoProject` | 写 |
| `DELETE` | `/v1/tunnels/forwarders/{id}` | `Public` | — | — | `NoProject` | 写 |
| `PUT` | `/v1/tunnels/forwarders/{id}` | `Public` | — | — | `NoProject` | 写 |
| `POST` | `/v1/tunnels/forwarders/{id}/stop` | `InHandler(deny)` | `tunnel.manage.others` | — | `NoProject` | 写 |
| `DELETE` | `/v1/tunnels/hosted/{name}` | `InHandler(deny)` | `tunnel.hosted`、`tunnel.open`、`tunnel.manage.others` | — | `NoProject` | 写 |
| `POST` | `/v1/tunnels/hosted/{name}` | `Require` | `tunnel.hosted`、`tunnel.open` | — | `NoProject` | 写 |
| `GET` | `/v1/tunnels/local-presets` | `Public` | — | — | `NoProject` | 读 |
| `POST` | `/v1/tunnels/local-presets/{name}` | `Require` | `tunnel.hosted`、`tunnel.open` | — | `NoProject` | 写 |
| `GET` | `/v1/tunnels/presets` | `Public` | — | — | `NoProject` | 读 |
| `DELETE` | `/v1/tunnels/presets/{name}` | `Require` | `tunnel.hosted`、`tunnel.open` | — | `NoProject` | 写 |
| `GET` | `/v1/tunnels/presets/{name}` | `Public` | — | — | `NoProject` | 读 |
| `PUT` | `/v1/tunnels/presets/{name}` | `Require` | `tunnel.hosted`、`tunnel.open` | — | `NoProject` | 写 |
| `GET` | `/v1/update` | `Public` | — | — | `NoProject` | 读 |
| `DELETE` | `/v1/update/apply` | `Require` | `update.apply` | — | `NoProject` | 写 |
| `POST` | `/v1/update/apply` | `InHandler(deny)` | `update.apply`、`worker.upgrade` | — | `NoProject` | 写 |
| `POST` | `/v1/update/check` | `Require` | `update.check` | — | `NoProject` | 写 |
| `DELETE` | `/v1/wakeups/{wid}` | `InHandler(deny)` | `job.wake.others` | — | `Resource(wakeup)` | 写 |
| `GET` | `/v1/wakeups/{wid}` | `InHandler(deny)` | `job.read.others` | — | `Resource(wakeup)` | 读 |
| `PATCH` | `/v1/wakeups/{wid}` | `InHandler(deny)` | `job.wake.others`、`job.submit` | — | `Resource(wakeup)` | 写 |
| `GET` | `/v1/work-items` | `Public` | — | `job` | `Filter` | 读 |
| `POST` | `/v1/work-items` | `Public` | — | — | `BodyProject` | 写 |
| `POST` | `/v1/work-items/capture` | `Public` | — | `job` | `BodyProject` | 写 |
| `POST` | `/v1/work-items/delete` | `Public` | — | — | `Resource(work_item)` | 写 |
| `GET` | `/v1/work-items/digest` | `Public` | — | — | `Filter` | 读 |
| `POST` | `/v1/work-items/digest` | `Public` | — | — | `Filter` | 写 |
| `GET` | `/v1/work-items/merge-suggestions` | `Public` | — | `job` | `Filter` | 读 |
| `POST` | `/v1/work-items/merge-suggestions/{n}/accept` | `Public` | — | — | `Resource(merge_suggestion)` | 写 |
| `POST` | `/v1/work-items/merge-suggestions/{n}/dismiss` | `Public` | — | — | `Resource(merge_suggestion)` | 写 |
| `GET` | `/v1/work-items/requests` | `Public` | — | `job` | `Filter` | 读 |
| `GET` | `/v1/work-items/summarizer` | `Public` | — | — | `NoProject` | 读 |
| `DELETE` | `/v1/work-items/{id}` | `Public` | — | — | `Resource(work_item)` | 写 |
| `GET` | `/v1/work-items/{id}` | `Public` | — | `job` | `Resource(work_item)` | 读 |
| `PATCH` | `/v1/work-items/{id}` | `Public` | — | `job` | `Resource(work_item)` | 写 |
| `POST` | `/v1/work-items/{id}/dispatch` | `InHandler(deny)` | `session.reply.others`、`job.read.others`、`plan.manage.others` | — | `Resource(work_item)` | 写 |
| `GET` | `/v1/work-items/{id}/dispatch-targets` | `InHandler(filter)` | `session.read.others` | — | `Resource(work_item)` | 读 |
| `GET` | `/v1/work-items/{id}/journal` | `Public` | — | `job` | `Resource(work_item)` | 读 |
| `POST` | `/v1/work-items/{id}/journal` | `Public` | — | `job` | `Resource(work_item)` | 写 |
| `DELETE` | `/v1/work-items/{id}/links` | `Public` | — | — | `Resource(work_item)` | 写 |
| `GET` | `/v1/work-items/{id}/links` | `Public` | — | `job` | `Resource(work_item)` | 读 |
| `POST` | `/v1/work-items/{id}/links` | `Public` | — | — | `Resource(work_item)` | 写 |
| `POST` | `/v1/work-items/{id}/merge` | `Public` | — | — | `Resource(work_item)` | 写 |
| `POST` | `/v1/work-items/{id}/merge-suggestions` | `Public` | — | `job` | `Resource(work_item)` | 写 |
| `POST` | `/v1/work-items/{id}/report` | `Public` | — | `job` | `Resource(work_item)` | 写 |
| `POST` | `/v1/work-items/{id}/report-request` | `InHandler(deny)` | `session.reply.others` | `job` | `Resource(work_item)` | 写 |
| `GET` | `/v1/work-items/{id}/requests` | `Public` | — | `job` | `Resource(work_item)` | 读 |
| `GET` | `/v1/work-items/{id}/sessions` | `Public` | — | `job` | `Resource(work_item)` | 读 |
| `POST` | `/v1/work-items/{id}/sessions` | `Public` | — | — | `Resource(work_item)` | 写 |
| `DELETE` | `/v1/work-items/{id}/sessions/{sid}` | `Public` | — | — | `Resource(work_item)` | 写 |
| `POST` | `/v1/work-items/{id}/split` | `Public` | — | — | `Resource(work_item)` | 写 |
| `POST` | `/v1/work-items/{id}/suggestions/{field}/accept` | `Public` | — | — | `Resource(work_item)` | 写 |
| `POST` | `/v1/work-items/{id}/suggestions/{field}/dismiss` | `Public` | — | — | `Resource(work_item)` | 写 |
| `POST` | `/v1/work-items/{id}/summarize` | `Public` | — | `job` | `Resource(work_item)` | 写 |
| `POST` | `/v1/work-items/{id}/to-todo` | `InHandler(deny)` | `plan.manage.others` | — | `Resource(work_item)` | 写 |
| `GET` | `/v1/workbench/layout` | `Public` | — | — | `NoProject` | 读 |
| `PUT` | `/v1/workbench/layout` | `Public` | — | — | `NoProject` | 读（ReadSafe） |
| `GET` | `/v1/workbench/threads` | `InHandler(filter)` | `job.read.others` | — | `Filter` | 读 |
| `POST` | `/v1/workbench/threads/seen-all` | `Public` | — | — | `NoProject` | 读（ReadSafe） |
| `PATCH` | `/v1/workbench/threads/{id}` | `Public` | — | — | `Resource(workbench_thread)` | 读（ReadSafe） |
| `GET` | `/v1/workbench/threads/{id}/diff` | `InHandler(deny)` | `job.read.others` | — | `Resource(workbench_thread)` | 读 |
| `POST` | `/v1/workbench/threads/{id}/review` | `InHandler(deny)` | `job.manage.others` | — | `Resource(workbench_thread)` | 写 |
| `POST` | `/v1/workbench/threads/{id}/turn` | `InHandler(deny)` | `job.manage.others`、`session.reply.others`、`job.submit` | — | `Resource(workbench_thread)` | 写 |
| `POST` | `/v1/worker-enroll` | `NoUserSurface` | — | `自身凭据` | `NoProject` | 写 |
| `GET` | `/v1/worker-enroll/{id}` | `NoUserSurface` | — | `自身凭据` | `NoProject` | 读 |
| `POST` | `/v1/worker-enroll/{id}/cancel` | `NoUserSurface` | — | `自身凭据` | `NoProject` | 写 |
| `POST` | `/v1/worker-enroll/{id}/confirm` | `NoUserSurface` | — | `自身凭据` | `NoProject` | 写 |
| `GET` | `/v1/worker-enrollments` | `Require` | `worker.manage` | — | `NoProject` | 读 |
| `POST` | `/v1/worker-enrollments/{id}/accept` | `Require` | `worker.manage` | — | `NoProject` | 写 |
| `POST` | `/v1/worker-enrollments/{id}/reject` | `Require` | `worker.manage` | — | `NoProject` | 写 |
| `POST` | `/v1/workers` | `Require` | `worker.manage` | — | `NoProject` | 写 |
| `GET` | `/v1/workers/connect` | `NoUserSurface` | — | `worker` | `NoProject` | 写（Mutating） |
| `GET` | `/v1/workers/pty-connect` | `NoUserSurface` | — | `worker` | `NoProject` | 写（Mutating） |
| `GET` | `/v1/workers/tunnel-connect` | `NoUserSurface` | — | `worker` | `NoProject` | 写（Mutating） |
| `DELETE` | `/v1/workers/{id}` | `Require` | `worker.manage` | — | `Resource(worker)` | 写 |
| `GET` | `/v1/workers/{id}` | `Public` | — | — | `Resource(worker)` | 读 |
| `GET` | `/v1/workers/{id}/assignable` | `Public` | — | `worker` | `Resource(worker)` | 读 |
| `GET` | `/v1/workers/{id}/projects` | `Public` | — | — | `Resource(worker)` | 读 |
| `POST` | `/v1/workers/{id}/reload` | `Require` | `worker.manage` | — | `Resource(worker)` | 写 |
| `POST` | `/v1/workers/{id}/upgrade` | `InHandler(deny)` | `worker.upgrade`、`worker.upgrade.upload` | — | `Resource(worker)` | 写 |
| `GET` | `/v1/workers/{id}/upgrade/file` | `NoUserSurface` | — | `worker` | `Resource(worker)` | 读 |
| `PUT` | `/v1/workers/{id}/upgrade/file` | `Require` | `worker.upgrade.upload` | — | `Resource(worker)` | 写 |
| `GET` | `/v1/workflow-templates` | `Public` | — | — | `Filter` | 读 |
| `DELETE` | `/v1/workflow-templates/{name}` | `Require` | `config.write` | — | `BodyProject` | 写 |
| `GET` | `/v1/workflow-templates/{name}` | `Public` | — | — | `Filter` | 读 |
| `PUT` | `/v1/workflow-templates/{name}` | `Require` | `config.write` | — | `BodyProject` | 写 |
| `POST` | `/v1/workflow-templates/{name}/render` | `Public` | — | — | `BodyProject` | 读（ReadSafe） |
| `GET` | `/v1/workflow-templates/{name}/source` | `Public` | — | — | `BodyProject` | 读 |
| `GET` | `/v1/workflows` | `InHandler(filter)` | `job.read.others` | — | `Filter` | 读 |
| `POST` | `/v1/workflows` | `Require` | `job.submit` | — | `BodyProject` | 写 |
| `GET` | `/v1/workflows/{id}` | `InHandler(deny)` | `job.read.others` | — | `Resource(workflow)` | 读 |
| `POST` | `/v1/workflows/{id}/cancel` | `InHandler(deny)` | `job.cancel.others` | — | `Resource(workflow)` | 写 |
| `GET` | `/v1/workflows/{id}/events` | `InHandler(deny)` | `job.read.others` | — | `Resource(workflow)` | 读 |
| `GET` | `/v1/workflows/{id}/export` | `InHandler(deny)` | `job.read.others` | — | `Resource(workflow)` | 读 |
| `POST` | `/v1/workflows/{id}/pick` | `InHandler(deny)` | `job.manage.others`、`job.submit` | — | `Resource(workflow)` | 写 |
| `POST` | `/v1/workflows/{id}/rerun` | `InHandler(deny)` | `job.manage.others`、`job.submit` | — | `Resource(workflow)` | 写 |
| `POST` | `/v1/workflows/{id}/save-as-template` | `Require` | `config.write` | — | `Resource(workflow)` | 写 |
| `GET` | `/v1/ws` | `NoUserSurface` | — | `自身凭据` | `NoProject` | 读 |
| `POST` | `/v1/ws-ticket` | `Public` | — | — | `NoProject` | 读（ReadSafe） |
| `GET` | `/v1/xfer` | `InHandler(filter)` | `xfer.manage.others` | — | `Filter` | 读 |
| `POST` | `/v1/xfer` | `Require` | `xfer.write` | — | `BodyProject` | 写 |
| `POST` | `/v1/xfer/precheck` | `Require` | `xfer.write` | — | `BodyProject` | 写 |
| `DELETE` | `/v1/xfer/{id}` | `InHandler(deny)` | `xfer.manage.others` | — | `Resource(xfer)` | 写 |
| `GET` | `/v1/xfer/{id}` | `InHandler(deny)` | `xfer.manage.others` | — | `Resource(xfer)` | 读 |
| `GET` | `/v1/xfer/{id}/content` | `InHandler(deny)` | `xfer.manage.others` | `worker` | `Resource(xfer)` | 读 |
| `PUT` | `/v1/xfer/{id}/content` | `NoUserSurface` | — | `worker` | `Resource(xfer)` | 写 |
