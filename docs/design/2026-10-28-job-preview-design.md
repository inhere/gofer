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

