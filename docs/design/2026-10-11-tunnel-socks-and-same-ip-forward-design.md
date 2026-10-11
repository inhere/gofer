# 隧道：SOCKS5 动态转发 + 同 IP 同端口转发 + bind 地址预检

- issue：gofer-2m7a
- 路线图：TUN-06（SOCKS5 动态转发）、TUN-07（同 IP 同端口简写 + bind 地址预检）
- 状态：设计稿，待实施

## 背景

`gofer tun forward` 是**静态端口转发**：每条规则 `[udp/][bind:]lport:host:port` 把一个本地端口绑定到 worker 网络里的一个固定目标。worker 所在的现场网络分成多个子网、每个子网有各自的调试目标时，暴露出三个问题：

1. **worker 白名单**要逐个写目标。这一点现有能力已能解决：白名单支持 `CIDR:端口列表/区间`，按网段放开、按端口收紧即可，只需补文档（见「四」）。
2. **转发端要为每个目标分配本地端口**。十来个管理页面就要排十来个本地端口，靠预设备注记对照关系；Web 应用的重定向、绝对地址、cookie 与端口绑定，换端口后常常跳错。
3. **本地开发的服务与现场服务联调**时，服务配置里写的是 `127.0.0.1:<换过的端口>`，与现场部署的真实地址不一致，联调完还要改回去。

## 目标

- 浏览器、命令行工具按**真实目标地址**访问 worker 网络，不用分配本地端口（TUN-06）。
- 本地服务按**真实 IP + 真实端口**连接，配置与现场一致（TUN-07）。
- 绑定地址不在本机时，给出能直接照做的提示，而不是 `cannot assign requested address`（TUN-07）。
- 授权模型不变：每条连接仍经 `/v1/tunnels/connect` 鉴权（bearer、`can_tunnel`），最终放行仍由 **worker 白名单**决定。

## 非目标

- 不做三层 VPN / TUN 网卡（透明路由、广播）。需要广播发现的软件仍按 tcp-tunnel 设计 §13.2 的「worker 侧网关」方案处理。
- SOCKS 不做 BIND、UDP ASSOCIATE、用户名密码认证；不做普通 HTTP 代理（absolute-form 请求）。
- 不让 server 远程修改 worker 白名单：worker 白名单是现场一侧的安全边界，保持 worker 本地配置说了算。

## 一、TUN-06 SOCKS5 动态转发

### 1. 规格写法

新增一种规格，和现有规格同列，因此逗号列表、预设、server 托管转发、Web 启停全部直接可用：

```text
socks/[bind:]port          # 例：socks/1080、socks/127.0.0.1:1080、socks/[::1]:1080
```

```bash
gofer tun forward -w <worker> socks/1080
# 与静态规则混用
gofer tun forward -w <worker> socks/1080,1502:10.20.1.5:502
gofer tun save site-a -w <worker> socks/1080   # 存成预设，Web 上可一键启停（托管）
```

解析结果：`ForwardSpec{Network: "socks", Bind, LocalPort, Target: "*"}`。`Target` 固定为 `*`，只用于展示；真实目标按连接确定。

### 2. 协议范围

监听端口按首字节嗅探，同一个端口兼容两种客户端：

| 首字节 | 协议 | 处理 |
|---|---|---|
| `0x05` | SOCKS5 | 只接受 NO AUTH（`0x00`）；只接受 CONNECT（`0x01`）；地址类型 IPv4 / IPv6 / 域名都接受 |
| ASCII | HTTP | 只接受 `CONNECT host:port HTTP/1.1`，成功回 `200 Connection established` 后转成字节流；其他方法回 `405`，正文提示「请用 SOCKS5 或 CONNECT」 |
| 其他 | — | 直接关闭 |

- 域名目标原样交给 worker 解析，使用 worker 一侧的 DNS，这样现场内网域名也能用。注意：白名单的 CIDR 条目**只匹配 IP**，域名目标需要白名单写主机名条目，否则返回 403。文档里写明。
- 支持 HTTP CONNECT 是为了只认 HTTP 代理的工具（`HTTPS_PROXY=http://127.0.0.1:1080` 的 curl、git、Go 程序等）。

### 3. 每条连接的流程

1. 完成握手，得到 `target = host:port`。
2. 调用 `DialTarget(ctx, "tcp", target)`。它和静态规则的 Dial 是同一条路径（`client.DialTunnel` / 托管转发的 HostedDial），只是目标改成按连接传入。
3. 成功：SOCKS 回 `0x00`（BND 填 `0.0.0.0:0`），HTTP 回 `200`；然后复用 `BridgeWithOptions` 双向转发。
4. 失败：按下表映射回复码后关闭。

| 隧道结果 | SOCKS5 REP | HTTP CONNECT |
|---|---|---|
| 403 目标未放行 / 无权限 | `0x02` connection not allowed by ruleset | 403 |
| 404 worker 不在线 | `0x01` general failure | 502 |
| 429 超出 worker 连接上限 | `0x01` | 503 |
| 502 worker 拨号失败（refused） | `0x05` connection refused | 502 |
| 502 worker 拨号失败（其他，如 no route / timeout） | `0x04` host unreachable | 502 |
| 504 等待 worker 回连超时 | `0x06` TTL expired | 504 |
| 其他 | `0x01` | 502 |

### 4. 事件与日志

沿用现有 forwarder 事件表。`session.opened` 增加 `target` 属性，值为本条连接的真实目标；握手失败记 `session.closed close_reason=handshake_failed`（新增原因值）。`tunnel_id` 关联 server、worker 两端日志的方式不变。

### 5. 登记与展示

- 登记（`POST /v1/tunnels/forwarders`）里 spec 的 `network` 为 `socks`，`target` 为 `*`。`tun ls` 的 RULES 列显示 `socks 127.0.0.1:1080`；Web 设置 → Tunnels 同样显示。
- CONNECTIONS 段不变：每条真实连接一行，`TARGET` 为真实目标。
- `tun check` / 预设检查遇到 socks 规格时跳过（没有固定目标可测）。

### 6. 安全

- **只允许环回 bind**（`127.0.0.0/8`、`::1`，缺省 `127.0.0.1`）。SOCKS 没有认证，绑到局域网地址就成了一个开放代理，可以访问 worker 白名单里的所有目标，所以非环回 bind 直接报错。静态规则维持现状。
- 托管转发（server 主机上起 SOCKS）同样只允许环回。
- 放行范围仍由 worker 白名单决定，SOCKS 不扩大可达范围，只是省掉了逐个目标写规则。

### 7. 连接数

浏览器对每个站点都会并发好几条连接，worker 默认 `tunnel.max_conns: 8` 很容易打满（429 → SOCKS `0x01`，页面部分资源加载失败）。处理方式：

- 文档建议：用 SOCKS 访问网页时，worker 设 `max_conns: 32` 或更高。
- 不改默认值，默认值保护的是只开了少量静态转发的 worker。
- 429 的 `session.closed` 带 `error` 写明 `worker tunnel connection limit reached (max_conns)`，便于排查。

### 8. 实现位置

- `internal/tunnel/socks.go`：握手解析（SOCKS5 / HTTP CONNECT）、回复码映射，纯函数，便于表驱动测试。
- `Forwarder` 增加 `DialTarget func(ctx, network, target string) (DialResult, error)`。`Spec.Network == "socks"` 时 `Run` 走 `runSOCKS`：accept、握手、`DialTarget`、Bridge。静态规则仍用现有的 `Dial`。
- `HostedDial` 签名改成按目标拨号，托管 SOCKS 与托管静态规则共用。
- 回复码映射依赖 `client.TunnelError.Status`；托管路径从 server 内部拨号结果取同样的状态。

## 二、TUN-07 同 IP 同端口简写

### 1. 写法

```text
[udp/]=host:port           # 例：=10.20.1.5:502、udp/=10.20.1.5:1740、=[fd00::5]:502
```

等价于 `host:port:host:port`：本地监听 `host:port`，转发到 worker 网络里的 `host:port`。`host` 必须是 IP 字面量，因为要作为本机的 bind 地址。

### 2. 用法：本机环回网卡挂上目标 IP

```bash
# 一次性（需要管理员）：把目标 IP 挂到本机环回网卡
#   Windows：设备管理器 → 添加过时硬件 → 网络适配器 → Microsoft KM-TEST Loopback Adapter，
#            给它配 10.20.1.5 / 255.255.255.255（可加多个地址）
#   Linux：  sudo ip addr add 10.20.1.5/32 dev lo
#   macOS：  sudo ifconfig lo0 alias 10.20.1.5/32
gofer tun forward -w <worker> =10.20.1.5:502,=10.20.1.5:11740
```

本地服务的配置直接写 `10.20.1.5:502`，与现场部署一致。

限制（写进文档）：本地网络如果本身也在用同一网段，挂上这些地址会和真实地址冲突。这种情况只能继续用 `127.0.0.1:<端口>` 转发。

## 三、bind 地址预检

- `tun forward`（CLI）和托管转发启动时，**在开始任何监听之前**统一检查所有规格的 bind 地址，避免出现「前几条已经在监听、后面一条失败」的半启动状态。
- 下列地址不检查：bind 为 `127.0.0.0/8`、`::1`、`0.0.0.0`、`::`，或为主机名。
- 其他 IP 如果不在 `net.InterfaceAddrs()` 里，直接报错：

  ```text
  spec #2 "=10.20.1.5:502": bind address 10.20.1.5 is not assigned to this machine;
  add it to a loopback adapter first (Linux: sudo ip addr add 10.20.1.5/32 dev lo; Windows: KM-TEST Loopback Adapter; macOS: sudo ifconfig lo0 alias 10.20.1.5/32)
  ```

- 兜底：监听本身失败且错误为 `EADDRNOTAVAIL` / Windows `WSAEADDRNOTAVAIL (10049)` 时，追加同样的提示。

## 四、文档

- `docs/runbook/tcp-tunnel.md` 新增「多网段现场」一节：
  - 白名单按 `网段CIDR:端口列表` 写，不写 `网段:*`；
  - 浏览器、命令行工具用 SOCKS（浏览器代理插件按地址规则分流）；
  - 本地服务联调用同 IP 同端口转发；
  - 需要广播发现的软件仍用 worker 侧网关；
  - SOCKS 访问网页时调大 `max_conns`。
- gofer-usage skill 对应的 references 同步：tunnel 命令与 worker 配置（G045）。
- `docs/reference/http-api.md`：forwarder spec 的 `network` 增加 `socks`。

## 五、兼容

- 旧 server 收到 `network: socks` 的登记时只用于展示，不影响转发。
- 旧 CLI 读到含 `socks/` 或 `=` 规格的 server 预设时会报 `spec #N` 解析错。按 G032 不做兼容，CHANGELOG 注明「使用新规格的预设需要新版 CLI」。
- worker 端无改动、无协议门槛：SOCKS 的每条连接对 worker 来说就是一次普通的 TCP 隧道。

## 六、测试

- `socks_test.go`（表驱动）：
  - SOCKS5 握手：方法协商、IPv4 / IPv6 / 域名、非 CONNECT 命令被拒、无认证以外的方法被拒、截断报文；
  - HTTP CONNECT：正常、非 CONNECT 方法回 405；
  - 回复码映射表逐项覆盖。
- forwarder e2e（参考现有 `forwarder_test.go`，假 Dial）：
  - SOCKS 客户端用 `golang.org/x/net/proxy` 或手写握手，连到 echo 目标后双向收发，事件里 `target` 正确；
  - 403 / 429 / 502 分别得到正确的 REP。
- spec 解析：`socks/…` 各种 bind 写法、非环回 bind 报错；`=host:port` / `udp/=…` / IPv6 / 主机名报错。
- bind 预检：不在本机的 IP 报错且不启动任何监听；环回地址和 `0.0.0.0` 放行。
- 托管转发：SOCKS 预设可启停，登记里显示 `socks`。
- 真机：容器 worker 上放行一个网段，本机起 `socks/1080`，用 `curl --socks5-hostname` 和 `curl -x http://`（CONNECT）各访问一个目标，`tun ls` 能看到对应连接。

## 七、实施分期

1. T1 spec：`socks/` 与 `=` 解析、bind 预检（含测试）。
2. T2 SOCKS 握手与回复码（`socks.go`，纯函数 + 测试）。
3. T3 forwarder `runSOCKS` + CLI / 托管接入 + 登记展示（`tun ls`、Web Tunnels 页）。
4. T4 文档：runbook、skill references、http-api、CHANGELOG。
5. T5 验证：Linux race、Windows 全量、web 三命令、真机。然后发版。

## 实施记录（2026-10-11）

- 代码：
  - `internal/tunnel/spec.go`：解析 `socks/` 与 `=` 规则。
  - `internal/tunnel/bindcheck*.go`：`CheckBindAddrs`；监听失败为 `EADDRNOTAVAIL`（Windows 10049）时追加提示。
  - `internal/tunnel/socks.go`：握手、回复码映射、`bufferedConn`。
  - `internal/tunnel/forwarder.go`：`DialTarget`、`handleSOCKS`，静态与动态规则共用 `bridgeTarget`。
  - `HostedDial` 改为按目标拨号。
  - CLI：`tun forward` 预检并接入 `DialTarget`；`tun check -n` 跳过 socks 规则；`tun ls` 显示 `socks/<port> -> *`。
  - 登记接口接受 `network: socks`；Web Tunnels 页显示「按连接选目标」。
- 与设计的偏差：
  - 托管转发的 bind 预检放在 `HostedForwarderManager.Start` 里，CLI 与 server 两端都会检查。
  - socks 转发归并连接时，算入同一 caller、同一 worker 的所有连接。这和两个登记共用同一个键时的处理一样，会多计，`forwarderCarries` 的注释里写明了。
  - 域名目标不做特殊处理，交给 worker 解析。
- 测试：
  - `spec_socks_test.go`、`bindcheck_test.go`（含真实监听 TEST-NET 地址的提示）、`socks_test.go`（握手表、回复码映射、SOCKS5 / CONNECT 端到端、拒绝回复与事件）；
  - httpapi `tunnel_socks_test.go`（托管 socks、非环回预设 400、登记解析）；
  - commands `presetCheckTargets`、client `Display`。
