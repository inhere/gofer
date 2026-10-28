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
