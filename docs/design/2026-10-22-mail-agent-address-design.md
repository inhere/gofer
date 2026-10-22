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

## 已定（2026-10-22 用户批准，按推荐）

1. `agent:` 只投递 gofer 管理的常驻会话，不挑用户的终端会话（发给终端会话用 `session:`）。
2. 常驻会话重启时续接上一次的上下文（agent 支持 `loadSession` 时）；轮换时开新会话。
3. 默认项目取发件方所在的项目，跨项目写 `agent:<名字>@<项目>`。
