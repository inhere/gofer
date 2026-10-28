# Trigger 外部触发派活设计

> issue：gofer-rztq
> 状态：0.1 草稿
> 相关：`docs/design/2026-10-11-event-hooks-design.md`（只读外发事件与 event_hooks）；`internal/notify`（generic / 钉钉 / 飞书 webhook 签名与 provider 适配）；`internal/config/ceiling.go`（调用方上限）；`internal/authz`、`docs/reference/permissions.md`（权限目录）；`internal/messenger`（会话传话与 IM 相关能力）；`internal/template`、`internal/job/template.go`（任务书模板）；`AUTO-02b` schedule trigger（现有单 schedule 外部触发）；G032、G033、G056。

## 1. 背景与问题

gofer 已有两类相邻能力，但都不能直接满足“外部系统按事件派一个受约束的 job”这一需求：

- `server.notification.webhooks` 是**出站**通知。generic / 钉钉 / 飞书适配器把 gofer 事件发送到外部，签名保护的是 gofer 发出的请求；它没有入站请求的认证、重放和幂等语义。
- `server.event_hooks` 是 server 主机上的只读命令旁路。它可以消费事件，但命令输出不能回写 gofer，也不能借此提交 job。
- `POST /v1/schedules/{id}/trigger` 是单个 schedule 的外部触发器：token 只代表该 schedule，触发时使用 schedule 创建者的 `OriginCaller` 上限；它不适合多个外部事件映射到任务书、携带受控输入，或统一审计去重。
- `internal/messenger` / `session_messaging` 解决的是已经登记的终端会话与网页之间的传话，不是通用 IM 入站网关。把 IM 消息直接当成会话回复，会绕过收件会话的权限、状态和人工确认语义。

需要一个独立的 **Trigger 模块**：一个外部请求经认证、重放检查、幂等和速率限制后，只能按配置好的项目、任务书和输入映射生成 job。外部请求不能选择 agent、runner、命令、项目或任意 prompt，也不能因为“触发器有 token”而取得一个新的 user caller 权限。

本设计重点解决：

1. 外部请求究竟以什么身份派活，以及如何继承调用方权限和 ceiling；
2. generic HTTP 与 IM provider 的签名、时间窗、重放与去重；
3. 触发风暴、重复投递和服务重启下的速率限制；
4. 默认关闭、密钥不进 API / 日志、配置写入与审计；
5. 与已有出站通知、`event_hooks`、schedule trigger、`internal/messenger` 的边界。

## 2. 目标与非目标

### 目标

- 提供一个按名称寻址的入站 trigger，支持 generic HTTP；IM 入站通过 provider 适配器复用同一派活内核。
- 每个 trigger 绑定一个固定项目和任务书模板，并只允许声明过的 payload 字段填充模板变量；请求本身不能改变执行资源。
- 在 dispatch 前完成认证、时间戳窗口、持久化幂等、触发器限流、请求大小限制和 owner ceiling 校验；重复请求返回原 job，不重复派活。
- 为每个接受、拒绝、重放、重复、限流和派活结果留下可查询审计记录，并在 job 上保留 `channel=trigger` 与来源标识。
- 默认关闭：没有显式配置和启用的 trigger 时不注册任何可用入站派活入口；没有明确 owner caller 的配置不能加载。
- HTTP、CLI、MCP 都有清楚的能力边界；权限判定仍集中在 `internal/authz`，不在 handler 里读取旧 capability / governance 开关。

### 非目标

- 不做任意脚本回调、决策 hook、同步等待 job 结果或把 job 输出自动回复到 IM。
- 不把外部请求变成一个新的 user caller，不让 trigger token 访问普通 `/v1` 路由，也不允许 trigger 代理 `config.write`、`job.manage` 等权限。
- 不替换 schedule trigger；schedule 仍是“一个已保存 JobRequest 的定时 / 单次运行”，Trigger 是“多个外部输入到固定任务书”的入口。
- 不翻译或清洗用户 payload 的业务语义；gofer 只负责边界大小、结构和模板变量转义，任务书决定 agent 如何理解内容。
- 不在第一阶段支持任意 IM 平台；先提供 generic HTTP 与一个可验证签名的 provider seam，具体 provider 按独立适配器落地。

## 3. 方案

### 3.1 配置形状

Trigger 定义放在 `server.triggers`，与 `server.event_hooks` 一样是**文件专属配置**：配置写接口、Web 设置页和 MCP 写工具不能创建、编辑或旋转它。原因是 trigger 能够持续产生 job；让一个普通已认证调用方远程修改模板、项目或密钥映射，等价于远程扩大执行权限。`gofer serve reload` 后生效，旧快照继续服务在途请求。

建议配置形状如下，字段名是设计提案，不代表本阶段已有实现：

```yaml
server:
  triggers:
    - name: source-update
      enabled: true                         # 缺省 false，必须显式打开
      owner_caller: automation               # 必须是已声明且非 root 的 caller
      project: <project>
      template: source-update                # 固定任务书；不可由请求选择
      auth:
        kind: hmac-sha256
        secret_env: GOFER_TRIGGER_SOURCE_UPDATE
        timestamp_header: X-Gofer-Trigger-Timestamp
        signature_header: X-Gofer-Trigger-Signature
        replay_window_sec: 300
      input:
        vars:
          ref: body.ref
          delivery: header.X-Provider-Delivery
          summary: body.summary
        # 可选：IM provider 只允许这些字段作为模板输入
        message: body.message
      dedup:
        key: header.X-Provider-Delivery
        ttl_sec: 86400
      rate:
        per_minute: 10
        burst: 2
        max_inflight: 2
      limits:
        max_body_bytes: 262144
        max_var_bytes: 32768
```

约束：

- `name` 必须是短、可寻址的稳定标识；全局唯一。`enabled` 未写等于 false，而不是沿用 event hook 的“未写即开”。
- `owner_caller` 必须存在于 `server.callers`，不能是 root 管理员；加载时检查该 caller 当前能否提交 job。owner 删除、变成 root 或失去 `job.submit` 后，reload 失败或 trigger 进入不可用状态，不能静默改用另一个身份。
- `project`、`template`、`agent`、`runner`、`cmd` 等执行资源来自固定的任务书 / 显式白名单，不来自请求。模板解析在 reload 时预检，在 dispatch 时再次按当前快照校验；模板的输入变量只接受 `body.<json path>`、`header.<name>`、`message.<field>` 等有限引用，不执行表达式、脚本或任意 JSONPath。
- `secret_env` 只存环境变量名，secret 从不进入配置读视图、数据库、错误、审计或 CLI 输出。推荐 HMAC；静态 bearer / query token 不作为 generic trigger 的默认方案，因为 URL 会进入代理和访问日志。
- `dedup.key` 必须引用外部稳定事件号（例如 provider delivery id）；没有稳定事件号时只能使用调用方提供的 `Idempotency-Key`，未提供则明确表示“本请求不保证去重”，不能用时间或 IP 猜一个键。
- 所有大小、超时、限流、TTL 有服务端上限。配置超限直接 `config validate` / reload 报错；`enabled: true` 但认证信息不可解析也不能启动。

### 3.2 数据模型与派活身份

新增一个由 `internal/trigger` 持有的深模块，外部接口只接受规范化的 `TriggerRequest`：

```text
TriggerRequest {
  trigger_name, request_id, external_event_id,
  received_at, raw_body_hash,
  headers (只保留 allowlist), payload (有大小上限), source
}
```

模块内部按以下顺序处理：认证 → 时间窗 → 结构 / 大小 → 幂等 claim → trigger 限流 → 解析受控变量 → 构造固定 `JobRequest` → 调用 job admission → 写结果。任何步骤失败都不进入下一步；幂等 claim 与 job 记录需要在同一事务 / 可恢复状态机下协调，避免并发请求同时通过。

触发 job 的身份分三层保存，不能混成一个 caller：

- `JobRequest.CallerID` 为空：外部请求没有 gofer user 身份，不能拿 trigger token 去访问普通 user 路由。
- `JobRequest.OriginCaller = owner_caller`：这是 ceiling owner。job、retry、continuation、failover 和内部派生 job 都继承它；owner 的项目、agent、runner、worker、`allow_exec`、只读等上限始终生效。它与现有 schedule webhook 的“创建者作为 `OriginCaller`”规则一致。
- 新增可选的来源字段（例如 `TriggerName` / `TriggerEventID`，仅用于 job 详情、事件和审计），值是配置名和外部事件号，不是可鉴权的 caller。`Channel` 固定为 `trigger`；provider、远端 request id 等放在受限的 provenance 结构中。

提交不能只靠 `OriginCaller` 字段“自觉”遵守权限。Trigger dispatcher 必须调用共享的 admission seam：以 owner caller 的有效权限与 ceiling 进行项目 / 模板展开 / runner / agent / exec 校验，再进入 `job.Service.Submit`。owner 在 reload 之后被收回 `job.submit` 或 ceiling 不再允许固定请求时，触发失败并记审计，不降级成 root 或 server 本机。

建议新增持久表 `trigger_receipts`：

```text
(trigger_name, external_event_id) UNIQUE,
request_id, body_hash, source, received_at,
status = claimed | submitted | duplicate | rejected | failed,
job_id, last_error, expires_at
```

同一 `(trigger_name, external_event_id)` 再次到达：body hash 相同则返回第一次结果（`duplicate: true`，带原 `job_id`）；hash 不同则 `409`，说明外部 id 被复用。过期记录由清理任务删除；删除前不能重新解释为新事件。认证失败的请求不写原文，只写计数 / 摘要审计。

### 3.3 HTTP 接口

#### 外部调用

- `POST /v1/triggers/{name}/invoke`：generic HTTP 入站。路由不进入 bearer auth 组，声明为 `NoUserSurface` + `External("trigger secret")`；trigger 名称、body、header 均受大小限制。
- body 必须是 JSON（provider 适配器可以先解析自己的格式，再生成同一规范化请求）。认证签名覆盖**原始 body 字节**和时间戳，例如 `HMAC-SHA256(secret, timestamp + "\\n" + raw_body)`，请求头为 `sha256=<hex>`；签名用 constant-time 比较。时间戳偏离窗口直接拒绝，不能仅靠去重替代时间窗。
- 接受后返回 `202`：`{trigger, request_id, job_id, duplicate}`。重复且已有 job 时返回 `200` 或 `202` 均可，但必须明确 `duplicate=true`；建议 `200` 表示已知结果，便于上游停止重试。认证失败 `401`，时间窗 / 签名格式错误 `401`，限流 `429` 并给 `Retry-After`，body / 变量 / 固定请求不合法 `400`，同 id 不同 body `409`，owner ceiling 或权限失效 `403`，服务暂不可用 `503`。
- 不在响应中回显 payload、secret、完整签名或模板 prompt；错误只返回稳定 code 与可操作 detail。未知 trigger 可返回统一 `404`，不要用错误时间 / 错误体暴露是否存在 enabled trigger。

#### 管理与观测

- `GET /v1/triggers`、`GET /v1/triggers/{name}`：只返回 name、enabled、owner、project、template、provider、限流 / 去重策略和最近状态；secret 只显示 `configured: true` / env key，不显示值。建议新增 `trigger.read` 权限，或在权限评审确认后复用 `config.read`，不可由 job credential 访问。
- 不提供 HTTP 写 trigger、导出 secret 或远程 rotate。文件修改后用 `serve reload`；CLI 的 rotate 只能作为本机配置文件工具，且必须明确写回哪个配置文件、备份并重新 validate，第一阶段可不提供。
- `GET /v1/triggers/{name}/deliveries`（或统一事件审计查询）只显示事件 id 的 hash / 截断值、状态、job id、时间和失败 code，不显示原始 payload。

所有新 `/v1` 路由必须加入 `internal/httpapi/authz_routes.go` 的 `routeDecls`，并按 G056 生成 `docs/reference/permissions.md`。外部 invoke 的 `NoUserSurface` 只代表它走自己的 secret，不代表 handler 可以读取 caller 配置位绕过 dispatcher 的 owner admission。

### 3.4 CLI 与 MCP

CLI 只做小工具，放在 `gofer tool trigger`（遵守 G033）：

- `gofer tool trigger ls|show <name>`：通过已认证 server 读取脱敏元数据；没有 `trigger.read` 时拒绝。
- `gofer tool trigger invoke <name> --body <file> --id <external-id>`：用于运维自测，调用 `/invoke`，secret 从显式环境变量 / stdin 读取，不把它作为命令参数回显；不提供 `add` / `edit` 的 server API。
- `gofer config validate` 增加 trigger 的静态校验、owner 权限 / ceiling 预检和“配置存在但 disabled”的提示。

MCP 不是第三种外部认证通道：

- 不新增一个让 agent 代持 trigger secret 的派活工具；agent 已有的 job submit 继续用自己的 job credential / session 规则，不能切换成 trigger owner。
- 可在 operator MCP 会话中提供只读 `gofer_trigger_list` / `gofer_trigger_show`，复用 `trigger.read`，结果与 HTTP 脱敏视图一致；job credential 不列出 trigger 元数据，除非现有路由白名单明确允许。
- 若未来某个 agent 确实要触发外部流程，应由其 caller 权限直接提交 job 或走已授权的 schedule，而不是把 trigger 做成隐式提权工具。

### 3.5 权限与安全

权限模型采用“**trigger secret 认证来源，owner caller 决定权限上限**”：

1. secret 只证明请求能调用某个 trigger，不证明它是一个 gofer 用户，也不授予 `auth.manage`、`config.write`、`session.*` 等能力。
2. owner caller 必须是非 root caller；dispatch 计算其有效权限与 ceiling。固定任务书的 project / agent / runner / exec / read-only 约束取交集，任一不满足即拒绝。所有 retry / continuation 继续继承同一 `OriginCaller`。
3. 新权限目录建议加入 `trigger.read`（中风险、不可授予 session、可配置范围）；不新增 `trigger.invoke` 给 user，因为外部 invoke 是 `External` 路由，认证在 trigger 模块完成。配置写仍属于文件操作 / 既有管理员运维流程，不用新增一个可远程授予的写权限。
4. owner caller 的 `rate_limit` / `rate_burst` / `max_concurrent_jobs` 与 trigger 自己的 per-trigger 限制同时生效，取更严格者；触发器不能通过配置自己的 rate 值提高 owner 上限。

安全细节：

- HMAC 用原始 body + 时间戳，拒绝旧时间戳，签名和 secret 比较 constant-time；不接受重定向、不在 query 中放 secret。provider adapter 必须先验证厂商签名，再转入同一 seam。
- body、headers、变量和模板渲染结果都有上限；payload 作为不可信数据注入模板时使用明确分隔 / 转义，不能成为模板语法或命令行参数。外部值不能填 `cmd`、`runner`、`agent`、`project` 或任意环境变量。
- 去重 claim 必须先于 `Submit`，并在数据库唯一键上兜底；服务重启、双请求、provider 重试不能产生两个 job。claim 后 Submit 崩溃时由可恢复 reconciler 按 receipt 状态补交或标记失败，不能无记录重跑。
- 限流分为 per-trigger token bucket、owner caller 的全局速率 / 并发和全局 body / in-flight 上限；拒绝要可观测但不记录 secret / 原文。第一阶段可采用 server 进程内 bucket（与 schedule trigger 的 10 秒门槛同类），幂等必须持久化；多实例部署若出现需求再把 bucket 状态移到共享 store。
- 审计事件至少包括 `trigger.received`、`trigger.auth_failed`、`trigger.replay_rejected`、`trigger.duplicate`、`trigger.rate_limited`、`trigger.submitted`、`trigger.rejected`，字段为 trigger 名、provider、外部 id hash、request id、owner caller、project、job id、结果 code 和远端地址的脱敏摘要。原始 body 不进日志；是否保存受控 payload 摘要需另行确认。

### 3.6 与现有功能的关系

| 现有功能 | 关系与不变点 |
|---|---|
| `server.notification.webhooks` | 仍是出站通知；不复用其 outbound URL / `allow_hosts` 作为入站认证。可把 trigger job 的终态通过现有通知发给人。 |
| `server.event_hooks` | 仍是只读命令旁路；不能用 event hook 代替 trigger，也不能让 trigger 调 event hook 回写。两者均文件专属，但执行方向相反。 |
| schedule webhook | 保持 `/v1/schedules/{id}/trigger` 与既有 token、schedule owner ceiling、10 秒内存门槛；Trigger 不改变 schedule 的语义。未来可让 schedule 的入站事件由同一个签名 / receipt 库实现，但不改现有 endpoint。 |
| `internal/messenger` / `session_messaging` | IM 入站先归一化为 trigger payload，只能创建固定的一次性 job；不得直接写 session mailbox、回答 interaction 或调用 `deliver`。会话回复仍走现有 session / mail 权限。 |
| `internal/template` / `gofer template` | Trigger 复用任务书解析与变量规则，但模板名来自文件配置；外部数据只能填声明变量。模板渲染后的 request 再走 owner admission，避免绕过既有校验。 |
| authz / caller ceilings | 新的 HTTP 管理读路由与 MCP 工具走 `trigger.read`；外部 invoke 不走 user authz，但 dispatch 必须使用 owner 的有效权限与 ceiling。 |

