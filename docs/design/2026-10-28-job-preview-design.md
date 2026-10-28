# Job Preview（job-preview）设计

> issue：gofer-ad2u
> 状态：0.1 草稿
> 相关：`docs/runbook/tcp-tunnel.md`、`docs/design/2026-09-11-tcp-tunnel-design.md`、`internal/wsproto`、`internal/httpapi` job 详情与 WebSocket 鉴权、`skills/gofer-usage`（实施后按 G045 同步）

## 1. 背景与问题

job 可以在 server 本机或 worker 上运行一个 HTTP 服务（例如开发服务器、报告查看器、调试 UI），但目前只能通过日志、产物下载或人工配置 `gofer tunnel` 访问。TCP 隧道是面向 CLI 的透明字节转发：它要求调用方本地起监听端口，worker 白名单决定目标，且没有 job 生命周期绑定；直接把它暴露给 Web 控制台会带来端口管理、跨域、鉴权和资源路径问题。

job 详情页已经按 job 的可见性保护详情、日志、事件和 WebSocket attach；Web 控制台的 Bearer token 保存在浏览器 storage，并由 API client 显式放入请求头，原始 job 服务不会自动携带这个 header。若简单把一个随机端口转成公开 URL，会出现以下问题：

- URL 可被猜测、分享或从 Referer / 访问日志泄露，不能把“能打开 job 详情”与“能访问预览”混为一谈；
- HTTP 页面通常还会请求相对资源、根路径资源、重定向和 WebSocket，单纯的路径拼接或一次性文件代理不完整；
- preview 必须绑定执行该 job 的 worker 实例，而不是只绑定 worker 名，否则 worker 重连或 job 迁移后可能把请求送到错误的进程；
- job 结束后仍保留一个可用的转发会造成残留服务和权限窗口；租约到期、浏览器刷新、worker 离线都必须有确定行为；
- 现有 `wsproto` 的 `tunnel_open`（v5）只表达“worker 去拨一个 target”，没有 job 归属和本机服务端口约束，不能直接复用为 job preview 的安全协议。

## 3. 方案

### 3.1 总体选择：以 opaque subdomain 为 canonical URL

推荐使用 **opaque subdomain**，不使用路径前缀作为 canonical 方案：

```text
https://<lease-token>.preview.example.test/
```

`server.preview.public_base_url` 配置为带 `{token}` 占位符的 origin（例如 `https://{token}.preview.example.test`），由部署的 DNS / 证书提供通配符解析。token 使用至少 32 字节随机数的 base64url 表示，只在 URL 中出现，不使用可递增的 job id 或 preview id。

选择子域名而不是 `/preview/<token>/` 有三点理由：

1. 预览应用拥有自己的 `/`，普通相对路径、根路径资源、`Location: /…` 和客户端构造的绝对 WebSocket URL 不需要改写；
2. 预览 origin 与控制台 origin 隔离，预览应用不能读取控制台的 localStorage / sessionStorage，也不能自然地把请求发到控制面 API；不会把 gofer Bearer token 或控制台 Cookie 送入不可信的 job 服务；
3. WebSocket handshake、应用 Cookie、CORS 和安全策略以一个稳定 origin 工作，代理只需做标准 HTTP reverse proxy 和字节级 WebSocket relay。

路径前缀不作为兼容别名：它虽然不要求 wildcard DNS，但会使根路径资源和应用重定向失真，HTML 重写又无法覆盖任意 JavaScript 生成的 URL。没有可用 wildcard DNS / TLS 的部署应显式配置一个反向代理，将 `*.preview.<host>` 转发到 gofer，而不是静默退化为不安全的路径代理。`public_base_url` 未配置或不满足占位符校验时，preview 功能关闭并在创建接口返回明确错误。

首个请求以 token 识别租约并设置仅限该 host 的 `HttpOnly`、`SameSite=Strict` Cookie；后续 HTTP 与 WebSocket 请求只依赖该 Cookie。代理将 gofer 的 Cookie 从上游请求剥离，token 永不传给 job 服务；预览 URL 是短期 bearer，复制 URL 即复制访问权，但不会扩大到控制台或其他 job。

### 3.2 配置形状

在全局配置增加 server 侧配置，默认关闭：

```yaml
server:
  preview:
    enabled: true
    public_base_url: "https://{token}.preview.example.test"
    default_ttl_sec: 1800       # 缺省 30 分钟
    max_ttl_sec: 28800          # 最长 8 小时
    idle_timeout_sec: 900       # 仅对无活动连接回收；0 = 不按 idle 回收
    max_connections: 16         # 每个 lease 的并发浏览器连接
```

- `public_base_url` 必须是绝对 `http`/`https` origin，host 中恰好有一个 `{token}`，不能把 token 放在 path、query 或 userinfo；生产环境推荐 HTTPS。
- `enabled: false`、空值或校验失败时不启动监听以外的 preview 逻辑；配置校验在加载和写接口统一执行。
- `ttl_sec` 由请求指定时夹在 `1..max_ttl_sec`；省略取 default。server 只延长到当前时间加 TTL，不允许 renew 超过 max。
- 配置是 server 全局能力，不写进项目瘦配置或 worker.yaml；worker 只需升级到支持 preview 的协议版本。

### 3.3 数据模型与生命周期

新增 `job_previews` 表（字段名示意）：

```sql
CREATE TABLE job_previews (
  id TEXT PRIMARY KEY,                 -- 内部 id，可展示但不作为访问凭证
  token_hash BLOB NOT NULL UNIQUE,     -- 原 token 永不落库
  job_id TEXT NOT NULL,
  caller_id TEXT NOT NULL,
  worker_id TEXT NOT NULL,
  worker_instance_id TEXT NOT NULL,
  port INTEGER NOT NULL,
  state TEXT NOT NULL,                 -- active | expired | closed | job_ended | worker_lost
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_used_at INTEGER,
  closed_at INTEGER,
  close_reason TEXT,
  connection_count INTEGER NOT NULL DEFAULT 0
);
```

创建时读取 job 的执行位置与 worker instance；本机 runner 记录 server 自己的执行 endpoint，worker runner 记录 worker id 和注册时的 instance id。只接受 `1..65535` 的端口，不接受 host、CIDR、Unix socket 或任意 worker 网络目标。`job_id + port` 可有多个 lease，但受配置的连接数和未来可加的 job 级配额限制。

访问流程：

1. 控制面 API 校验 caller 能读 job、job 仍在运行、preview 已启用、端口和 TTL 合法，生成高熵 token，只存 hash，返回一次完整 URL；
2. 浏览器请求 wildcard host。server 按 host 中的 token hash 查 lease，检查 `active`、未到期、job 仍未终态、worker instance 仍匹配；失败统一返回 404（不透露 job / lease 是否存在）；
3. 首次通过后设置 host-only preview Cookie，随后每个 HTTP 请求由 reverse proxy 以该 lease 建立到执行机 `127.0.0.1:<port>` 的专用数据连接；WebSocket upgrade 走同一 lease 的长连接 relay；
4. 到期由后台清理与每次请求的 lazy check 双重保证。到期后 HTTP 返回 410 或统一 404（推荐外部统一 404），已有连接取消并以 WebSocket 4408 关闭；
5. job 服务发出终态事件时，`PreviewManager.CloseJob(jobID)` 原子地把 lease 标为 `job_ended`，取消该 job 的全部代理上下文，再清理连接；不能依赖浏览器主动刷新；
6. worker instance 断开时关闭其 preview 连接并将 lease 标为 `worker_lost`，不自动绑定到新 instance，避免旧 job 请求误落到重连后的不同执行环境；job 尚未终态时用户可重新创建 lease。

列表接口只返回 id、job、port、状态、创建 / 到期时间和 close_reason，不返回 token 或可重新构造的 URL。撤销后的 token 永久失效；访问日志、审计日志、错误消息均对 Host/token 脱敏。

### 3.4 HTTP、CLI 与 MCP 接口

控制面接口均在 `/v1` 鉴权组内，使用已有 Bearer 鉴权和 job 资源访问规则：

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/v1/jobs/{id}/previews` | body `{port, ttl_sec?}`，创建 lease，返回 `{id, url, port, expires_at, worker_id}`；job 非运行态返回 409 |
| `GET` | `/v1/jobs/{id}/previews` | 列出调用方可见的该 job 的 lease，不返回 secret URL |
| `DELETE` | `/v1/jobs/{id}/previews/{preview_id}` | 撤销 lease；幂等，返回最终状态 |
| `POST` | `/v1/jobs/{id}/previews/{preview_id}/renew` | 仅 lease 创建者或拥有相应管理权限者续租，body `{ttl_sec}` |

浏览器数据面不是 `/v1` API：wildcard host 的 `GET /`、任意子路径与 WebSocket upgrade 都进入 preview proxy。该路由不接受 Bearer 作为替代授权，不能通过 job id 猜测入口；无效 host/token、已到期、job 已结束统一 404。应用响应中的 `Set-Cookie` 只允许 host-only cookie，代理拒绝 `Domain` 指向父域的 cookie，避免预览应用影响控制台或其他 preview。

HTTP proxy 转发原始 method、path、query、request/response body 和应用状态码，设置 `X-Forwarded-Host`、`X-Forwarded-Proto`、`X-Forwarded-For`，并将 `Host` 设为执行端 loopback 地址。去掉 `gofer_preview`、`Authorization` 等控制面凭据。`Upgrade: websocket` 请求不经 HTTP body 缓冲，校验 Origin 为该 preview origin 后直接进入字节 relay；关闭任一端即释放该 lease 的连接计数。

CLI 放在 job 资源组下（不新增顶级小工具命令）：

```text
gofer job preview <job-id> --port 3000 [--ttl 30m] [--open]
gofer job preview ls <job-id>
gofer job preview revoke <job-id> <preview-id>
```

`create` 只在终端打印一次完整 URL；`ls` 默认隐藏 token，仅显示 host 的截断形式和到期时间。`--open` 是主机侧动作，使用 host URL，不把 URL 写入 job prompt 或日志。

MCP 增加与 HTTP 对齐的 `gofer_job_preview_create`、`gofer_job_preview_list`、`gofer_job_preview_revoke`、`gofer_job_preview_renew`，返回结构化字段，create 的 URL 仅放在工具结果中。MCP caller 的语言与错误文案遵循现有 mcpserver 语言入口；MCP 工具不替浏览器访问 preview，不把 token 注入 agent 环境。

### 3.5 worker 连接与帧协议

不复用现有 generic `tunnel_open`：它的 `TunnelOpen{target}` 与 `tunnel.allow` 适合 user CLI 的任意白名单目标，无法证明 target 属于某个 job。新增 `wsproto` 能力（当前协议 v28，建议 v29）：

```go
type PreviewOpen struct {
    PreviewID  string `json:"preview_id"`
    JobID      string `json:"job_id"`
    Port       int    `json:"port"`
    RelayNonce string `json:"relay_nonce"`
}
```

server 通过控制 WebSocket 下发 `preview_open`；worker 仅在本地仍有该 `JobID` 的运行实例时接受，固定拨 `127.0.0.1:Port`，不解析请求方传来的 host。worker 通过专用 `/v1/workers/preview-connect` 回连，第一帧 hello 携带 preview id、job id 和一次性 nonce；server 校验 worker id、instance id、job id、nonce 与过期时间后才把浏览器连接接入。可以复用 `internal/tunnel` 的 rendezvous / splice 实现，但 preview 的 allowlist、job 绑定和生命周期是独立状态机。

新增 `PreviewMinProtocolVersion = 29` 与 `SupportsPreview`。worker runner 在创建 preview 时若协议低于 v29，返回 409，错误明确写出“需要协议 v29”；不能像可选日志字段一样静默忽略。server 本机 runner 不需要 wsproto，但仍走相同的 lease、反向代理和 job 终态关闭接口。该协议只新增帧和端点，不提高 `MinProtocolVersion`，老 worker 的普通 job 行为不变。

### 3.6 权限与安全

- 创建、列出、续租和撤销先解析 job，再复用 `mayReadJob` 的 owner-or-`job.read.others` 规则；因此“能看 job 的人”能看到并创建 preview。撤销他人的 lease 仍需 job owner 或现有 `job.manage.others`，创建者可撤销自己的 lease。若安全评审认为预览应用暴露面高，再增加独立的 `job.preview.others`，但初版不复制一套权限矩阵。
- 在 `internal/httpapi/authz_routes.go` 登记四个控制面路由为 job `Resource`，使用 `InHandler(deny/filter)`；在 `internal/authz/catalog.go` 不新增权限时无需 catalog 项。数据面 wildcard host 标为 `NoUserSurface` / `External`，由 handler 做 lease token 校验，不进入普通 `/v1` caller 鉴权。生成 `docs/reference/permissions.md`，保持路由声明完整。
- token 用 CSPRNG 生成、数据库只存 hash、只在 create 响应返回；URL 不是授权范围的替代品。禁止把 token 放在 query，避免常见代理 / Referer 日志泄露；响应加 `Referrer-Policy: no-referrer`、`Cache-Control: no-store`。
- preview origin 与控制台 origin 分离，默认不设跨域头、不允许预览应用设置父域 Cookie；控制面拒绝由 preview host 发起的 `/v1` 访问。WebSocket 只接受同一 preview origin 的 Origin，仍以 lease Cookie 为最终授权。
- 所有 lease 创建、续租、撤销、过期、job 终态关闭、worker instance 丢失和拒绝原因记审计事件：job id、preview id、caller、worker、port、状态和 reason，不记录 token、Cookie、完整 URL。
- 每 lease 限制并发连接、请求头大小、WebSocket frame 大小与空闲时长；代理不缓存响应，不允许 CONNECT、任意方法转发到 server 自身，不把 loopback 解析交给用户输入。应用能否访问公网由执行机网络策略决定，不由 preview 放宽。

### 3.7 与现有功能的关系

- **job 详情 / Web**：详情接口继续只返回 `previews` 的非 secret 摘要；JobDetail 页面在 job 运行中显示“Preview”创建与撤销操作，打开新标签页。页面自身 API 仍使用 `/v1` Bearer；preview 页面不是控制台 SPA 路由。
- **WebSocket attach / browser push**：attach 继续使用已有一次性 ticket，不能把 ws-ticket 当 preview 凭据；preview 使用独立 lease Cookie / worker 数据连接，撤销和长连接关闭机制可复用 `registerLiveConn` 的重判定思想但不共享 ticket store。
- **TCP tunnel**：generic tunnel 继续受 `tunnel.allow` 和 `tunnel.open` 管理，preview 不读取该 allowlist，也不把 job 服务注册成普通 tunnel。两者可共用 `internal/tunnel` 的二进制桥接、rendezvous 和字节统计代码。
- **job 终态与 worker 协议**：复用 job service 的终态事件和 worker 注册的 instance id；不在 HTTP handler 里轮询 job 状态，也不把 job 终态当作已完成清理的充分条件，关闭动作必须等待代理连接收尾。
- **通知 / MCP / CLI**：通知只可链接到控制台 job 详情，不把 bearer preview URL 写入通知、系统消息、agent prompt 或持久化评论；用户主动 create 后才得到 URL。

