# Job Preview（job-preview）设计

> issue：gofer-ad2u
> 状态：0.2 已批准（2026-10-29，待确认 2–7 按推荐；URL 形态改为端口模式默认）
> 相关：`docs/runbook/tcp-tunnel.md`、`docs/design/2026-09-11-tcp-tunnel-design.md`、`internal/wsproto`、`internal/httpapi` job 详情与 WebSocket 鉴权、`skills/gofer-usage`（实施后按 G045 同步）

## 1. 背景与问题

job 可以在 server 本机或 worker 上运行一个 HTTP 服务（例如开发服务器、报告查看器、调试 UI），但目前只能通过日志、产物下载或人工配置 `gofer tunnel` 访问。TCP 隧道是面向 CLI 的透明字节转发：它要求调用方本地起监听端口，worker 白名单决定目标，且没有 job 生命周期绑定；直接把它暴露给 Web 控制台会带来端口管理、跨域、鉴权和资源路径问题。

job 详情页已经按 job 的可见性保护详情、日志、事件和 WebSocket attach；Web 控制台的 Bearer token 保存在浏览器 storage，并由 API client 显式放入请求头，原始 job 服务不会自动携带这个 header。若简单把一个随机端口转成公开 URL，会出现以下问题：

- URL 可被猜测、分享或从 Referer / 访问日志泄露，不能把“能打开 job 详情”与“能访问预览”混为一谈；
- HTTP 页面通常还会请求相对资源、根路径资源、重定向和 WebSocket，单纯的路径拼接或一次性文件代理不完整；
- preview 必须绑定执行该 job 的 worker 实例，而不是只绑定 worker 名，否则 worker 重连或 job 迁移后可能把请求送到错误的进程；
- job 结束后仍保留一个可用的转发会造成残留服务和权限窗口；租约到期、浏览器刷新、worker 离线都必须有确定行为；
- 现有 `wsproto` 的 `tunnel_open`（v5）只表达“worker 去拨一个 target”，没有 job 归属和本机服务端口约束，不能直接复用为 job preview 的安全协议。

## 2. 目标与非目标

### 目标

- 为运行中的 job 创建一个短期、不可猜测的浏览器 URL，访问 job 执行机上指定的本机 TCP 端口。
- 同时支持普通 HTTP/HTTPS-over-TCP 请求、重定向、相对资源和 WebSocket upgrade；不要求浏览器安装 CLI 或配置代理。
- 预览入口的授权与 job 详情一致：只有能够读取该 job 的 user-class caller 才能创建；URL 持有者只在租约有效期内获得该预览，不获得其他 `/v1` API 权限。
- 以 job 和 worker instance 为边界；job 进入任一终态时自动撤销该 job 的全部 preview 并关闭已有连接。
- 有明确的租约、最大 TTL、并发连接限制、撤销操作和审计日志；协议协商失败时给出可操作的 worker 升级提示。
- 保持 server 的单一公开入口，适配已有 `server.web_base_url`、HTTP 路由声明、`s.can` 权限决策与长连接重判定机制。

### 非目标

- 不把 preview 做成通用公网反向代理，不允许用户任意指定 worker 网络内目标；目标只能是该 job 执行机的 loopback 端口。
- 不替代 `gofer tunnel` 的 TCP/UDP/SOCKS 能力，不改变 worker `tunnel.allow` 的语义。
- 不解析或重写应用 HTML、JavaScript、CSS，不承诺修复应用写死的绝对 URL；应用应能在自身 origin 根路径运行。
- 不为预览应用自动注入 gofer Bearer token，不让预览服务访问 `/v1` 控制面；应用自身的登录、Cookie 和 CORS 由应用负责。
- 不做跨 job 共享、持久化公网域名、文件上传下载语义或多 server 联邦。

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

## 4. 分阶段

| 阶段 | 内容 | 依赖与出口 |
|---|---|---|
| P0 协议与边界 | 明确 wildcard DNS/TLS、`public_base_url` 约束、job 执行位置与终态事件 seam；补 `wsproto` v29 的帧和 capability 常量，旧 worker 仍可注册 | 待确认项 1、2；协议 round-trip 与版本拒绝测试先行 |
| P1 server 本机 MVP | `job_previews` 迁移、lease manager、loopback HTTP reverse proxy、WebSocket upgrade、控制面 REST、job 终态自动关闭；先只支持 server runner | schema 升级测试、HTTP/WS 端到端、无 token 泄露检查 |
| P2 worker runner | worker `preview_open` / callback、job id + instance 校验、127.0.0.1 拨号、worker 断线收尾；server 对 v28 及以下返回 409 | P0/P1；协议 v29 worker 与 server 的双端 e2e |
| P3 CLI/MCP/Web | job CLI preview 子命令、MCP 四工具、JobDetail 的创建 / 列表 / 撤销 / 打开按钮；references 与 HTTP API 文档 | P1/P2；Web 三命令与浏览器级 smoke |
| P4 守护与发布 | 审计、指标、清理任务、限流、运行手册、配置示例、权限参考生成；灰度开启 `enabled: false`，确认 wildcard 代理后再启用 | 全量 Go / Web / CodeQL；发布前检查 token 脱敏与旧 worker 行为 |

每阶段保持可回滚：P1 的 server 本机 preview 不改变现有 tunnel；P2 只对明确创建 preview 的请求做协议门槛；P3 若前端未完成仍可用 REST/CLI；P4 才把配置示例标成启用。

## 5. 测试与守护

### 协议与 worker

- `internal/wsproto`：`PreviewOpen` JSON 字段、未知附加字段、v28 不支持 / v29 支持、`CurrentProtocolVersion` 与 `MinProtocolVersion` 分离；不提高注册 floor。
- worker 单测：非运行 job、错误 worker instance、非 loopback 目标、非法端口、nonce 重放、job 已终态、worker reload / disconnect 都必须拒绝或关闭；确认没有读取 `tunnel.allow` 造成语义串线。
- server-worker e2e：HTTP 200、302/304、二进制 body、keep-alive、多资源并发、应用 WebSocket echo、端口未监听、worker 离线、旧协议 409、job 结束自动断开、租约过期断开。

### HTTP / authz / 数据库

- token 熵和存储：数据库与日志中没有明文 token；随机 token、错误 host、改一个字符、过期 lease、已撤销 lease 和错误 Origin 均不可区分地返回 404。
- 访问矩阵：job owner、持有 `job.read.others` 的 caller、无权限 caller、worker token、job credential、受 ceiling 限制的 caller；控制面按现有 owner-or-admin 规则，数据面只能持有有效 lease。
- 路由守护：在 `internal/httpapi/authz_routes.go` 登记每个新 `/v1` 路由；数据面 route 明确 `NoUserSurface` / `External`。运行 `go generate ./internal/httpapi/...`，`docs/reference/permissions.md` 必须与声明一致。
- schema：把发布前一版的建表 SQL 放入 `internal/jobstore/testdata/schema-v*.sql`，通过 `TestOpenUpgradesPreviousReleaseSchema`；基础 schema 不能引用迁移才新增的列。迁移失败不留下半个 lease。
- 生命周期：用 `jobs.Wait(id)` 等待终态收尾，不以 status 先变终态代替连接和数据库清理；测试按 G051 先停 proxy/worker 再清理 TempDir，等待使用 `internal/testutil/wait`。

### Web / CLI / MCP

- Web API client 不把 preview URL 当 API URL 加 Bearer；打开新标签页不把 token 写入 `localStorage`，控制台退出不会误删 preview Cookie。
- 浏览器级 smoke 覆盖相对资源、根路径资源、302、Cookie、WebSocket、刷新、过期和 job 终态；CSP / `Referrer-Policy` 与预览应用的 Cookie 隔离有断言。
- CLI/MCP 对 400/404/409/410 的结构化错误稳定；MCP 结果不把 token 写入 prompt、持久化消息或 debug 日志；`--open` 只在主机侧动作。
- Go 包测试先跑 `internal/wsproto`、`internal/jobstore`、`internal/httpapi` 相关包，再跑 `go test ./...`；Web 按约定跑 `npx vue-tsc --noEmit && npx vitest run && npx vite build`。

## 6. 风险

- **wildcard DNS/TLS 不可用**：子域名方案需要部署配套，若 server 只暴露单一证书，功能会无法启用。不能退回路径前缀而假装兼容；应提供反向代理示例并在配置校验中给出修复提示。
- **预览应用本身不可信**：它能服务任意 HTML/JS，可能诱导用户输入敏感信息或发起外部请求。子域名隔离控制台 token，但不能替用户判断应用内容；默认只允许主动创建、短 TTL、HTTPS 和 no-store。
- **URL 泄露即访问泄露**：高熵和短租约降低风险但不能撤回已复制的 URL 以外的截图 / 缓存。控制面不自动发送 URL，日志脱敏，提供显式 revoke，并在 Web 上显示“这是 bearer URL”。
- **反向代理协议边界**：HTTP/1.1 upgrade、压缩、流式 body、连接背压和半关闭可能有实现细节。初版只支持 HTTP/1.1 与标准 WebSocket，不承诺 HTTP/2 CONNECT；所有方向设置上限并用超时避免 goroutine 泄漏。
- **job 重启 / worker 重连**：同一个 job id 可能出现新执行实例。lease 绑定 instance 且旧 instance 丢失即关闭，牺牲自动恢复换取不把请求送到错误进程；用户可重新创建。
- **权限扩大争议**：复用 `job.read.others` 最容易理解，但 job preview 比日志更接近实时应用。若安全评审要求最小授权，增加 `job.preview.others` 会带来 catalog、seed、session grant 和 references 的完整迁移成本，应在实现前定案。
- **数据库清理与终态竞态**：创建恰逢 job 结束、renew 恰逢 revoke、连接恰逢 expiry 需要同一事务 / generation 检查；否则可能出现“响应成功但马上不可用”的边界。API 应返回最终状态，客户端按状态处理而不是假定成功即长存。

## 8. 已定事项（2026-10-29）

- **待确认 1 改定：默认端口模式，子域名为可选。** 部署常见为局域网 IP + 端口访问，没有泛域名 DNS / TLS，因此：
  - 默认：server 在配置的端口池（如 `preview.port_range: 18100-18199`）里为每个预览分配一个端口，地址形如 `http://<server 主机>:<端口>/`，打开时由 server 生成并在 Web 上展示；根路径、相对资源、重定向与 WebSocket 原样工作。端口池同时是并发预览上限，租约结束端口回收。
  - 隔离：localStorage 等按 origin（含端口）隔离，控制台的 token 不会被预览页读到；cookie 不按端口隔离，server 转发时剥掉 gofer 自己的 cookie 与 `Authorization` 头，并且预览响应里设置的同名 cookie 不得覆盖控制台 cookie（转发响应时丢弃与 gofer cookie 同名的 `Set-Cookie`）。
  - 鉴权：预览端口上的第一次访问带一次性 token（由 Web「打开预览」生成），server 校验后在该端口 origin 下种一个仅本预览有效的 cookie；不可猜、可撤销、随租约失效。
  - 配置了 `preview.base_domain` 时改用 §3.1 的 opaque subdomain 方案，其余语义一致。
- 待确认 2–7 按推荐：server 本机 runner 与 worker 同一 API、P1 先支持本机；复用 `job.read.others`；租约默认 30 分钟、上限 8 小时、闲置 15 分钟回收；只拨执行机 `127.0.0.1:<port>`；server 关闭数据连接并以 lease 状态为权威；MCP 只在 create 的单次结果里返回完整 URL。
- 实施按 §4 分阶段拆成独立任务（本机端口模式 → worker 协议 v29 → Web / CLI / MCP），每个任务限时 45 分钟。

## 7. 待确认项

1. **公开 URL 形态：推荐 opaque subdomain，接受部署 wildcard DNS/TLS。**
   - 推荐理由：任意应用的根路径、相对资源、重定向和 WebSocket 都能原样工作；与控制台 storage 和 Cookie 隔离，安全边界最清楚。
   - 若部署无法提供 wildcard，推荐在前置反向代理补齐，而不是实现 HTML/JS 重写的路径前缀兼容层。请确认这项部署前提是否可接受。

2. **是否允许 server 本机 runner：推荐 P1 先支持，并与 worker 使用同一 API。**
   - 推荐理由：可先在无 worker 的环境验证 lease、代理、鉴权和 Web 交互，降低协议调试耦合；本机直接 dial job 执行 namespace 的 loopback。
   - 若只允许 worker，P1 可缩小，但会失去最短验证路径且使配置 / UI 与本机 job 不一致。

3. **权限：推荐初版复用 `job.read.others`，不新增 `job.preview.others`。**
   - 推荐理由：用户要求“能看 job 的人”，现有 job 访问规则已有 owner / others / ceiling / job credential 语义，避免新增一套容易漂移的权限矩阵；preview 仍受显式 create、TTL 和 revoke 约束。
   - 若预览经常承载比日志更敏感的管理 UI，再增加独立权限，并默认不授予 session grant。

4. **租约默认值：推荐 30 分钟默认、8 小时上限、闲置 15 分钟回收。**
   - 推荐理由：覆盖一次开发调试，又避免 URL 长期有效；idle 只回收连接不提前删除 lease，用户可在未到期时重新打开。
   - 若存在长时间人工演示场景，可提高配置上限，但不建议无限期 lease。

5. **预览服务的监听约定：推荐 worker 只拨 `127.0.0.1:<port>`，不接受用户 host。**
   - 推荐理由：job 端口由执行机本地进程拥有，避免把 preview 变成 worker 网络扫描 / 任意白名单代理；需要暴露其他机器服务时继续使用 `gofer tunnel`。
   - 若容器网络 namespace 与 worker agent 不同，需在 P0 明确共享 network namespace 或提供执行器侧 loopback bridge，否则不在 preview 中静默放宽目标。

6. **protocol v29 的关闭语义：推荐 server 关闭每条数据连接并以 lease 状态为权威，不新增复杂的 worker close RPC。**
   - 推荐理由：job 终态和租约撤销都由 server 观察，关闭现有 relay 即可；worker 只需拒绝新的 `preview_open`，协议面更小。
   - 若要在 worker 侧主动回收所有同 job 连接，可后续加可选 `preview_close`，但不能依赖它保证安全。

7. **MCP 是否返回完整 URL：推荐返回，但仅在 create 的单次工具结果中返回。**
   - 推荐理由：MCP agent 可以把 URL 交给人或调用方；list / 日志 / prompt 不重复暴露 secret，降低持久化泄露面。
   - 若部署策略禁止 agent 接触 bearer URL，则 MCP create 应只返回 preview id，由人通过 Web/CLI 创建，需同步修改 MCP 工具的产品定位。
