# 信箱第三期：`agent:` 地址与主机 agent 收件 设计

> issue: gofer-utyo ｜ 状态：0.2 已批准（2026-10-22，按推荐）｜ 2026-10-22
> 基于：`docs/design/2026-10-16-session-topics-mailbox-and-inbox-design.md`（「分期」第三期）、`docs/design/2026-10-22-mail-broadcast-design.md`

## 背景

信箱现在只能发给**已经存在**的会话：`session:` / `job:` 直接指定，`topic:` / `path:` 按主题或范围查找。
如果要找「一个能帮我处理某件事的 suga / codex」，而手上没有现成的会话，信就只能转「待指派」，由人去开会话。

第三期让地址可以写成 `agent:<名字>`：gofer 找到或启动这个 agent 的**常驻会话**，把信送进去，回信照常回到发件人。

原设计遗留的「suga 终端会话的身份与收件方式」已经在前几期解决：gofer 认得 `SUGA_SESSION_ID`，suga 会话会自己向 gofer 登记，收信走 `suga send`。第三期不再依赖它。

## 目标

1. `agent:<名字>` 能投递：有这个 agent 的常驻会话就送进去，没有就按配置启动一个。
2. 常驻会话由 gofer 管理：同一个 agent、同一个项目只保留一个，反复复用；空闲久了自动结束，达到条数或时长后轮换（与传话人、整理器同样的思路）。
3. 收件 agent 可以用 gofer 的信箱工具回信，回信挂在原对话串里。
4. 权限、成本可控：启动会话要有 `job.submit`；常驻会话数量有上限。

## 不做

- `agent:` **不会**挑选用户自己开的终端会话：终端会话属于用户，要发给它就用 `session:` 地址。
- 不支持 pty 交互 job 做常驻会话（不能程序化地送信）。
- 广播不能用 `each:agent:<名字>` 去启动新会话：广播只投递已经存在的会话（沿用第二期）。

## 设计

### D1 地址

- `agent:<名字>`：项目取发件方所在的项目（发件会话或 job 的项目；web / CLI 发件取 `-p` 或调用方默认项目）。
- `agent:<名字>@<项目>`：显式指定项目。
- `<名字>` 是信箱配置里的收件 agent 名（D2），不是任意 agent key。没有配置的名字返回 400，并列出可用的名字。

### D2 配置：哪些 agent 能收信

```yaml
mail:
  agents:
    suga:                     # 地址里的名字：agent:suga
      agent: suga-acp         # 用哪个 agent 定义起会话（须是 acp-agent，支持持续会话）
      args: [--model, command-code/deepseek-v4.1-flash]   # 可选，追加给 agent
      runner: ""              # 空 = 项目默认 runner
      idle_minutes: 120       # 空闲多久结束会话（默认 120）
      max_letters: 50         # 收满多少封后轮换（默认 50；0 = 不限）
      max_hours: 24           # 存在多久后轮换（默认 24；0 = 不限）
      prompt: ""              # 追加给常驻会话的开场说明（可选）
    codex:
      agent: codex-acp
  agents_max_resident: 6      # 全局最多同时存在的常驻会话
```

- 只支持 acp-agent：常驻会话就是一个 ACP 持续会话 job（`--session`）。送信走已经实现的「`awaiting_input` 时 `job say`」路径，不用新建投递通道。
- 开场说明由 gofer 生成：说明这是一个收信会话，信件会以 `<gofer-mail>` 标签送达；用 gofer 信箱工具回信（`gofer_session_mail_reply` 或 CLI）；之后追加配置里的 `prompt`。

### D3 解析与常驻会话

投递一封 `agent:suga` 的信件：

1. 查这个项目 `suga` 的常驻会话记录（新表 `mail_resident_agents`：名字、项目、job id、累计信件数、创建时间、状态）。
2. 记录对应的 job 仍是持续会话、没有终态 → 当作 `job:<id>` 投递（复用现有路径）。
3. 没有记录、job 已结束，或达到轮换条件 → 启动一个新的持续会话 job（带开场说明），写入记录，再投递。
   - 能续接时（agent 声明了 `loadSession`），新会话用 `job resume` 载入上一个会话，保留上下文；轮换则一律开新会话。
4. 达到 `agents_max_resident` 时不再启动新会话：信件转「待指派」，并说明原因。

启动和投递放在投递器里异步完成，发信接口不等待会话启动：信件先落为 `pending`，`to_kind=agent`。

### D4 回信

- 收件 agent 通过 gofer MCP 的信箱工具回信。持续会话 job 本来就带 job 凭据，能读写与本 job 相关的信件（第一期的权限表）。
- 回信挂在原对话串里，送回发件方，与其他信件一致。
- 收件 agent 没有回信，只在会话里输出文字时，不自动转成回信（避免把思考过程当成回复）。web 的信件对话里可以链接到该会话 job 查看。

### D5 权限与成本

- 发到 `agent:` 的信件需要 `job.submit`（可能启动新 job），按发件调用方检查；job / steward 凭据发 `agent:` 信件需要它所在项目允许（沿用 job 提交的权限检查）。
- 启动的会话 job 的发起者记为发件调用方（`OriginCaller`），按发件人自己的额度与上限受限，不借用别人的权限。
- 常驻会话的空闲结束、轮换、启动失败都记事件；web 信箱页可以看到当前的常驻会话（名字、项目、job、信件数、空闲时间），并能手动结束。

### D6 接口

- 地址解析：`agent:` 从「not supported yet」改为按 D3 解析。
- HTTP：`GET /v1/mail/agents` 列出可用的收件 agent 名与当前常驻会话；`POST /v1/mail/agents/{name}/end?project=…` 手动结束。按 G056 登记，前者 `Require(mail.read)` 类，后者需要 `job.manage` 类权限（按现有目录选最接近的）。
- CLI：`gofer session mail agents`（列出），`gofer session mail send --to agent:suga "…"`。
- MCP：发信工具的地址说明里加上 `agent:`。

### D7 收件会话单独的审批策略（gofer-81b4，2026-10-22 追加）

- 痛点：收件常驻会话本来只按项目级 `approval` 审批（项目为 off 时全部自动放行），给项目开 ask 会影响该项目所有 ACP 会话。
- 方案：`mail.agents.<名字>.approval`，结构与项目 `approval` 一致、校验同一套（`validateApprovalAt`），未写 = 沿用项目策略。
- 生效：启动常驻会话时由 `mail.ResidentSpec.Approval` → `JobRequest.SessionApproval` 带进 job，`acpRequest` 用 `Config.EffectiveApprovalWith(项目, agent, override)` 算策略——**替代**项目策略，只有 agent 自己的 `acp.permission_policy` 还能继续收紧。
- 外部不可设：`session_approval` 是 server 内部字段，HTTP 提交带它直接 400（拒绝而非静默忽略，不然调用方会以为自己的门生效了）；MCP / CLI 没有对应字段。
- 持久：随 `request_json` 保存，`continuationBase` 继承给续接 job，server 重启恢复与 `job resume` 都仍按它审批；续接出的新常驻会话由 `mail.agents` 配置重新带入。

## 已定（2026-10-22 用户批准，按推荐）

1. `agent:` 只投递 gofer 管理的常驻会话，不挑用户的终端会话（发给终端会话用 `session:`）。
2. 常驻会话重启时续接上一次的上下文（agent 支持 `loadSession` 时）；轮换时开新会话。
3. 默认项目取发件方所在的项目，跨项目写 `agent:<名字>@<项目>`。

## 实施记录（2026-10-22）

- **D1 地址**：`internal/mail/address.go` 解析 `agent:<名字>[@<项目>]`（`Address.Project`，最后一个 `@` 分隔）；非 `each:` 的 `agent:` 走 `residentAgent` 解析，未配置的名字是 `mail.ErrUnknownAgent`（入口层 400，错误里列出 `mail.agents` 的名字）；`each:agent:<名字>` 仍是「只投递已存在会话」的广播选择器。
- **D2 配置**：`internal/config/mail.go` 增加 `MailAgentConfig`（`agent` / `args` / `runner` / `idle_minutes` / `max_letters` / `max_hours` / `prompt`，后两者用指针区分「未写 = 默认」与「0 = 不限」）与 `agents_max_resident`，加载时校验名字字符、必填 `agent`、非负值；`config/gofer.example.yaml` 有注释示例。开场说明由 `mail.OpeningPrompt` 生成（收信会话、`<gofer-mail>` 标签、用信箱工具回信、空闲时长）再追加配置的 `prompt`。
- **D3 常驻会话**：additive 表 `mail_resident_agents`（name / project / job_id / session_id / letters / created_at / ended_at / status / last_error，主键 name+project），存储层 `internal/jobstore/mail_resident.go`。投递器 `internal/mail/resident_deliver.go` 每个 sweep 处理待投递的 `to_kind=agent` 信件：活着的会话 job 在 `awaiting_input` 时按 `job say` 路径投递（claim → say → confirm）并累加信件数；达到轮换条件先结束再开新会话（不续接）；job 已终态则标记行 ended，能续接时带 `ResumeSessionID` + `ResumeFromJobID`（`ResumedFrom` + `SessionID` = 执行侧的 loadSession）；达到 `agents_max_resident` 转待指派；启动失败记 failed 并在约 2 分钟后重试。会话 job 由 `internal/serve/mail_resident.go` 的适配器提交（`Session: true`、`IdleTimeoutSec=idle_minutes`、`ExclusiveDir=false` 避免长期占用项目目录锁、`OriginCaller` = 发件调用方）。空闲结束靠会话自身 idle-timeout，投递器在下个 sweep 看到终态即记事件。
- **D4 回信**：沿用第一期权限表——常驻会话 job 用自身 job 凭据通过 `gofer_session_mail_reply` / `gofer session mail reply <id>` 回信，挂原对话串；会话里的普通输出不转回信。
- **D5 权限与事件**：`internal/httpapi/mail_handler.go` 的 `mailAuthorize` 对 `MailKindAgent` 目标要求 `job.submit`（job 凭据直接放行，它自己的提交已证明）；事件走 `AppendAuditEvent("mail_resident", <名字@项目>, detail)` 并写 slog。同时按调用方 `ceiling` 检查目标项目（`jobstore.SplitResidentKey(t.ID)` 取项目，`ceilingAllowsProject` 不通过则 `mailForbidden` 说明项目超出上限）——常驻会话要在那个项目里跑，过不了就不能等到启动时才被拒（那样信件会挂到过期）；地址里没有项目（发件方也没给）时无法判定，交给后续检查。
- **D6 接口**：`GET /v1/mail/agents`、`POST /v1/mail/agents/{name}/end?project=`（`internal/httpapi/mail_agents.go`，G056 声明已登记并重跑 `go generate ./internal/httpapi/...`）；CLI `gofer session mail agents [--end <名字>] [--project <项目>]`；MCP 发信工具地址说明加 `agent:`。
- **测试**：`internal/mail/resident_test.go`（地址解析含默认项目 / `@项目` / 未配置名字、首封启动、第二封复用、`max_letters` 轮换、终态续接、上限转待指派、启动失败与重试窗口、回信同串、手动结束与列表）用假 `ResidentHost`；`internal/mail/mail_test.go` 的 `agent:` 未支持用例改为「未配置名字」用例，`broadcast_test.go` 的解析期望同步。
- **web**：常驻 agent 列表放在独立组件 `web/src/components/mail/ResidentAgents.vue`（名字 / 项目 / job 链接 / 信件数与待投递 / 空闲 / 状态 / 结束按钮），`Mail.vue` 只多一行导入与一个 `<MailResidentAgents />` 挂载；API client 加 `listMailAgents` / `endMailAgent`，types 加 `MailAgentsResp` 等；接线用 `web/src/views/mailResidentAgents.test.ts` 钉住。
- **已完成**：D1–D6 全部落地。
- **D7 收件会话单独的审批策略（gofer-81b4）**：`config.MailAgentConfig.Approval`（`*ApprovalConfig`）+ `validateApprovalAt` 复用项目 approval 的校验；`mail.ResidentAgentSpec/ResidentSpec.Approval` 一路带到 `serve.mailResidentHost.StartSession` 填 `job.JobRequest.SessionApproval`，`job.acpRequest` 改走 `config.EffectiveApprovalWith`。`internal/serve/mail_resident.go` 的 `cloneApproval` 让会话快照不被后续 reload 改写。HTTP 入口在 `handleCreateJob` 解码后拒绝 `session_approval`（400）。测试：`internal/config/mail_agents_test.go`（解析 / 校验）、`internal/config/approval_test.go`（覆盖替代项目策略、agent 仍只收紧）、`internal/job/session_approval_test.go`（ask 产生 interaction、读类自动放行、覆盖替代 ask 项目、续接后仍生效）、`internal/httpapi/session_approval_test.go`（外部带字段 400）、`internal/serve/mail_resident_test.go`（配置映射与快照隔离）。
