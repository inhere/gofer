# TCP 隧道

TCP 隧道适合从本机临时访问位于 worker 网络内的 TCP 服务，例如设备维护或连通性检查。每条连接都经过 server 控制面和 worker 数据 WebSocket，最终授权由 worker 的白名单决定。典型用法（Modbus 设备调试、PLC 编程软件经 worker 侧网关远程在线）见设计文档 [§13](../design/2026-09-11-tcp-tunnel-design.md)。

## 配置

在 B 侧 worker 配置中启用（缺省关闭）：

```yaml
tunnel:
  allow: ["192.168.1.10:502,1217", "192.168.1.0/24:502"]   # 尽量精确到设备与端口，避免放开整网段任意端口
  max_conns: 8
  dial_timeout_sec: 5
```

条目支持 `host:port`、`CIDR:port`、任意端口 `*`，IPv6 使用 `[2001:db8::10]:502`。端口还可写逗号列表与闭区间范围，如 `192.168.1.10:502,1217,11740-11743`，同一设备的多个端口不必拆成多行；`*` 不能与列表混写。条目还可加网络前缀：`udp/192.168.1.10:1502` 放行 UDP 单播，`tcp/...` 与不写前缀等价；白名单按 network 隔离，tcp 条目不会放行同一端口的 UDP。修改后执行 `gofer worker reload <worker-id>`，或重启 worker。worker 必须支持协议 v5。

## 命令

命令组别名为 `tunnel`/`tun`（子命令 forward / check / ls / stop / save / saved / forget / presets），子命令别名为 `forward`/`fwd`、`ls`/`list`；完整参数以 `gofer tunnel --help` 为准。

```text
$ gofer tunnel check -w w-plc 192.168.1.10:502
OK w-plc -> 192.168.1.10:502 (3 ms)
$ gofer tunnel check -w w-plc 192.168.1.99:502
ERROR: tunnel: HTTP 403: target not allowed
$ gofer tunnel check -w w-plc 192.168.1.10:503
ERROR: tunnel: HTTP 502: dial tcp 192.168.1.10:503: connect: connection refused
$ gofer tunnel forward -w w-plc 1502:192.168.1.10:502
forwarding 127.0.0.1:1502 -> w-plc:192.168.1.10:502
registered as fw-1a2b3c4d
$ gofer tunnel forward -w w-plc udp/1502:192.168.1.10:1502
forwarding UDP 127.0.0.1:1502 -> w-plc:192.168.1.10:1502
registered as fw-5e6f7a8b
$ gofer tunnel ls
FORWARDERS
ID WORKER RULES HOST PID AGE CONNS UP DOWN STOP
fw-1a2b3c4d w-plc 1502 -> 192.168.1.10:502 workshop-pc 4242 3m 1 32 32 remote
CONNECTIONS
ID CALLER WORKER TARGET CLIENT AGE UP DOWN
t-3967153a246e default w-plc 192.168.1.10:502 127.0.0.1:59948 2s 32 32
```

### 多条规格：空格与逗号都可以

每条规格仍是 `[udp/][bind:]lport:host:port`，**一个参数里可以用逗号写多条**，逗号与空格可以混用；逗号两侧的空格会被去掉，空条目忽略。IPv6 目标写 `[::1]:port`，里面没有逗号，不会被拆错。

```bash
# 一条参数写完四条规则（逗号分隔，最自然的写法）
gofer tun forward -w w-hw \
  udp/21845:192.168.0.253:21845,1502:192.168.0.205:502,11217:127.0.0.1:1217,11740:192.168.0.205:11740

# 与空格分隔完全等价
gofer tun forward -w w-hw udp/21845:192.168.0.253:21845 1502:192.168.0.205:502
```

某一条写错时报错会带**位置和原文**，不必再手工二分：`spec #3 "11217:127.0.0.1:1217": invalid local port ...`。保存预设时存的是**拆开后**的规则数组。

### 规格写法一览

| 写法 | 含义 |
|---|---|
| `[bind:]lport:host:port` | 本机 `bind:lport`（bind 缺省 `127.0.0.1`）→ worker 网络里的 `host:port`（TCP） |
| `udp/[bind:]lport:host:port` | 同上，UDP 单播 |
| `[udp/]=ip:port` | **同地址转发**：在本机 `ip:port` 监听，转发到 worker 网络里的同一个 `ip:port` |
| `socks/[bind:]port` | **动态转发**：本机起一个 SOCKS5 / HTTP CONNECT 代理，目标由每条连接自己给出；只能绑环回地址 |

三种写法可以写在同一个逗号列表里，也能存进预设、由 server 托管。

### 动态转发（SOCKS5 / HTTP CONNECT）

worker 网络里的目标很多（各类管理页面、多个网段的设备）时，不必给每个目标分配本地端口：

```bash
gofer tun forward -w <worker> socks/1080
```

- 浏览器：代理插件（如 SwitchyOmega）把 worker 网段（例如 `10.20.*`）指向 SOCKS5 `127.0.0.1:1080`，其余直连；之后直接打开原始地址，如 `https://10.20.1.21:8006`。没有端口改写，页面的重定向、cookie、证书都按原地址工作。
- 命令行：`curl --socks5-hostname 127.0.0.1:1080 http://10.20.1.40:8080/`；只认 HTTP 代理的工具用 `HTTPS_PROXY=http://127.0.0.1:1080`（同一个端口也接受 HTTP CONNECT）。
- 每条连接仍按真实目标走 `/v1/tunnels/connect` 的鉴权与 **worker 白名单**；被拒时客户端收到协议自己的错误：SOCKS5 回复码 `0x02`（白名单不放行）、`0x05`（设备拒绝连接）、`0x04`（不可达）、`0x06`（等待 worker 超时）、`0x01`（worker 不在线 / 超出连接上限），HTTP CONNECT 回 403 / 502 / 503 / 504。
- 域名目标交给 worker 解析，用的是 worker 一侧的 DNS。白名单的 CIDR 条目只匹配 IP，按域名访问时白名单要写主机名条目。
- 只支持 CONNECT：不支持 SOCKS 的 BIND / UDP ASSOCIATE、用户名密码认证，也不支持普通 HTTP 代理（`GET http://…` 形式，回 405）。UDP 设备仍用 `udp/` 静态规则。
- 代理没有认证，所以**只能绑环回地址**（`127.0.0.0/8`、`::1`），写 `socks/0.0.0.0:1080` 会被拒绝。
- 浏览器对每个站点会并发多条连接，worker 默认 `max_conns: 8` 很容易打满（表现为页面部分资源加载失败、日志里有 429）。用 SOCKS 访问网页时，worker 设 `max_conns: 32` 或更高。
- `tun ls` 中显示为 `socks/1080 -> *`，CONNECTIONS 段照常列出每条连接的真实目标；`tun check -n <预设>` 跳过 socks 规则（没有固定目标可测）。
- 日志：每条连接的 `session.opened` 带真实 `target`；握手不完整或被拒记 `session.closed close_reason=handshake_failed`。

### 同地址转发（本地服务按真实地址联调）

本地开发的服务要连 worker 网络里的设备时，用同地址转发，服务配置直接写设备的真实地址，与现场部署一致：

```bash
# 一次性（需要管理员）：把设备 IP 挂到本机环回网卡
#   Linux：  sudo ip addr add 10.20.1.5/32 dev lo
#   macOS：  sudo ifconfig lo0 alias 10.20.1.5/32
#   Windows：设备管理器 → 添加过时硬件 → 网络适配器 → Microsoft KM-TEST Loopback Adapter，
#            给它配 10.20.1.5 / 255.255.255.255（一块网卡可加多个地址）
gofer tun forward -w <worker> =10.20.1.5:502,=10.20.1.5:11740
```

- `=ip:port` 等价于 `ip:port:ip:port`；`ip` 必须是 IP 字面量（它同时是本机监听地址）。
- 启动时先检查所有规则的监听地址是否属于本机，有一条不在就整体报错、不启动任何监听，并给出上面的添加命令。环回地址、`0.0.0.0` / `::`、主机名不检查。
- 本机所在网络如果本身也在用同一网段，挂上这些地址会和真实地址冲突，这种情况只能继续用 `127.0.0.1:<端口>` 转发。

### 多网段现场的推荐配置

worker 所在网络分成多个子网、调试目标经常变化时：

1. **worker 白名单按「网段 + 端口」写**，一次写好，换目标不用再改 worker：

   ```yaml
   tunnel:
     allow:
       - "10.20.1.0/24:502,11740-11743"          # 设备网段：只放行协议端口
       - "udp/10.20.1.0/24:1740-1743"
       - "10.20.10.0/24:443,8006,8080"           # 管理页面网段
       - "127.0.0.1:1217"                        # worker 侧网关
     max_conns: 32
   ```

   不要写 `网段:*`，那等于把整个网段的任意服务都暴露出去。新网段先 `gofer tun check -w <worker> <ip>:<port>`：403 是白名单没放行，502 多半是 worker 所在机器到该网段本身不通（路由 / 防火墙），需要现场开通或在那个网段另放一台 worker。
2. **浏览器、命令行**用动态转发 `socks/1080`。
3. **本地服务联调**用同地址转发 `=ip:port`，或继续用 `127.0.0.1:<端口>` 静态转发。
4. 依赖 UDP 广播发现的软件仍按设计文档 §13.2 的「worker 侧网关」方案。

常用组合存成预设（`gofer tun save <name> -w <worker> socks/1080,=10.20.1.5:502`），web 设置 → Tunnels 可一键由 server 托管启停。

### 转发进程可见

`tun forward` 在**监听成功之后**向 hub 登记自己（`POST /v1/tunnels/forwarders`，id 形如 `fw-1a2b3c4d`，记录 caller/worker/规则/主机名/pid/启动时间），之后每 30 秒心跳续期，退出（含 Ctrl+C）时注销。超过 `server.tunnel.forwarder_ttl_sec`（默认 90 秒，即三次心跳）没有心跳就自动消失——进程被 kill、机器休眠都不会留下僵尸条目。

```yaml
server:
  tunnel:
    forwarder_ttl_sec: 90   # 0/缺省即 90；可热改（下一次读/写登记时生效）
```

`tun ls` 因此分两段：**FORWARDERS**（在线转发进程，含规则、主机/pid、运行时长、连接数与累计字节）与 **CONNECTIONS**（原来的活跃隧道）。"没有转发进程在跑"和"没人连上来"是两种不同的排查结论，现在能分开看。登记只是展示信息，**不授予任何转发能力**：真正的连接仍走 `/v1/tunnels/connect` 的鉴权与 worker 白名单。写登记只允许 user caller，且只能续/删自己登记的（job 凭证被 SEC-01 默认拒绝，worker 凭证不允许写）。

登记失败（旧 server、hub 不可达、token 不对）只 warn，不影响本地转发；心跳收到 404 说明 hub 重启或条目已过期，会自动重新登记。

### 远程停止转发进程

`tun forward` 跑在别的机器上时，删除它的登记没有用（下一次心跳 404 后它会重新登记）。要让它真正退出，用远程停止：

```text
$ gofer tunnel stop fw-1a2b3c4d
stop requested for fw-1a2b3c4d (workshop-pc pid 4242); it exits at its next heartbeat (<= 30s)
```

或在 web 控制台 设置 → Tunnels 的在线转发行点「停止」→「确认停止」，该行显示「停止中（≤30 秒内退出）」直到进程退出、条目消失。

机制：hub 够不着那台机器，只能把登记标记为 `stop_requested`（`POST /v1/tunnels/forwarders/{id}/stop` → 202）；转发进程下一次心跳收到 **410 Gone**（`stop requested`），hub 同时删除条目，进程打印 `stop requested from the console (gofer tunnel stop / web); exiting`、停止所有监听并以退出码 0 结束，**不会**重新登记。所以停止最多延迟一个心跳周期（30 秒）；进程若在此之前已死，条目照常按 TTL 过期。

- **版本要求**：新版 `tun forward` 登记时带 `caps: ["stop"]`，只有带这项能力的进程能被远程停止（`tun ls` 的 STOP 列为 `remote`）。旧版进程（列为 `ctrl+c-only`）不认识 410、会当作告警继续跑，所以 hub 直接拒绝：409「该转发进程版本过旧，不支持远程停止，请在 <host> 上 Ctrl+C」，web 上「停止」按钮置灰并给出同样提示。升级那台机器上的 gofer 并重启 forward 后即可远程停止。server 也必须是支持该接口的版本（旧 server 对 `/stop` 返回 404）。
- **权限**：登记该转发的 caller 本人，或带 `can_admin` 的 caller（与删除 job 同一条 owner-or-admin 规则）；其他 caller 403；worker 凭证 403；job 凭证被 SEC-01 默认拒绝（`job credential may not stop a tunnel forwarder`）。web 控制台用的是哪个 caller 的 token，就按那个 caller 判断——用同一 token 登录的 web 可以停掉 CLI 用同一 token 起的转发。
- server 托管（hosted）的转发不走这条路，仍用预设行里的「停止」（`DELETE /v1/tunnels/hosted/{name}`）；对它调用 `/stop` 返回 409。
- `tun ls` 的 STOP 列：`remote`（可远程停止）、`ctrl+c-only`（旧版，只能本机 Ctrl+C）、`hosted`（server 托管）、`stopping`（已请求停止，等心跳）。`GET /v1/tunnels/forwarders` 每条带 `caps`、`stop_requested`、`stop_requested_at`。

`tunnel forward` 支持 `--log-file <path>` 或 `--log-dir <dir>`（二选一）；目录模式生成唯一的 `forward-<YYYYmmdd-HHMMSS>-<pid>.log`。未指定时写入 `<config-dir>/run/tunnels/`。`--quiet` 仅关闭终端输出，文件日志仍保留；显式路径失败会使命令报错，默认路径失败则警告后降级为 stderr。

日志为 JSONL，可用同一 `tunnel_id` 关联 server、worker、forwarder：`rg '"tunnel_id":"t-..."' <config-dir>/run/tunnels/*.log`。`tunnel_id` 由 server 分配，随 connect 的 101 响应头 `X-Gofer-Tunnel-Id` 回传给 forwarder（旧 server 无此头时字段为空，其余不受影响）。

forwarder 事件（`component=forward`，每条都带 `session_id`；UDP 的 session_id 是本地来源 host:port，TCP 是本地连接 host:port）：`forward.started`（local/target/network）→ `session.opened` → `tunnel.opened`（`dial_ms`）→ `tunnel.first_up`/`tunnel.first_down`（`first_byte_ms`，自 dial 开始计，每 session 各一次）→ `session.closed`（`close_reason`、`bytes_up`、`bytes_down`、`duration_ms`）。`close_reason` 取值：`client_closed`（本地客户端挂断）、`worker_closed`（隧道远端挂断）、`idle`（UDP 空闲回收）、`ctx_done`（forwarder 退出）、`dial_failed`（隧道没建起来，带 `error`）、`write_failed`；超出 UDP session 上限的新来源记 `session.rejected`（`dropped_max_sessions`）。判断"慢在哪一段"：`dial_ms` 大 = rendezvous/worker 慢；`first_up` 正常而 `first_down` 迟迟不来或没有 = 设备侧无响应。UDP 的 `session.closed` 另带 `packets_up`/`packets_down`（worker 端 `tunnel.closed` 同样有），`duration_ms / packets_up` 就是平均每包周转：若它接近主机↔worker 的 ping RTT，说明协议是逐包 stop-and-wait、慢在往返次数×链路 RTT，而不是 gofer；若远大于 RTT，慢在设备响应。要看逐包分布，运行 forwarder 时设 `GOFER_TUNNEL_TRACE=1`，每个报文记一条 `tunnel.datagram`（`dir`、`len`、`gap_ms`=与同方向上一报文的间隔），默认关闭、关闭时无开销；只记长度与时间，不记内容。

错误码：400 参数或网络不支持；401 凭证无效；403 无权限或目标未放行；404 worker 不在线；409 worker 协议过旧（需升级到支持协议 v5 的版本）；429 超出连接上限；502 worker 拨号失败；504 等待 worker 回连超时。

## 验证与排障

## 日志与诊断

日志默认使用 JSON Lines 文件 sink（stderr 保持现有 text 格式）。server 写入
`<config-dir>/run/serve.log`，worker 写入
`<config-dir>/run/worker-<worker_id>.log`；`tunnel forward` 写入
`<config-dir>/run/tunnels/forward-<YYYYmmdd-HHMMSS>-<pid>.log`。Windows
daemon 不支持后台化，serve/worker 均前台运行，上述文件 sink 是唯一持久日志；Unix
后台进程的 `.out.log`（如 `serve.out.log`、`worker-<id>.out.log`）仅追加接收 panic
或非 slog 输出，不轮转。显式 `--log-file`、`--log-dir` 或配置路径打开失败会使启动失败；
隐式默认路径失败时警告并降级到 stderr。

每行常见字段包括 `time`、`level`、`msg`、`component`、`event`、`operation_id`
（一次进程运行的 run id）、`tunnel_id`、`session_id`、`worker_id`、`target`、
`rendezvous_ms`、`first_byte_ms`、`bytes_up`、`bytes_down`、`close_reason`。
用同一个隧道 ID 可重建三端链路：

```bash
rg '"tunnel_id":"t-abc123"' ~/.config/gofer/run/serve.log ~/.config/gofer/run/worker-*.log ~/.config/gofer/run/tunnels/*.log
```

`rendezvous_ms` 或 `dial_ms` 偏大表示 server 等待 worker/拨号阶段慢；有
`first_up` 但迟迟没有 `first_down`，通常是设备侧无响应。`bytes_up` 持续增加而
`bytes_down` 为零也指向设备回包路径；两者都为零则连接尚未真正传输。`close_reason`
结合 `duration_ms` 可区分客户端主动关闭、worker 关闭、UDP idle 回收和拨号失败。

UDP 缓冲池可用环境变量 `GOFER_UDP_BUFFER_POOL` 调整；设为 `0` 可回退到默认分配路径。
逐报文 trace 默认关闭，显式设置 `GOFER_UDP_TRACE=1` 后才记录方向、长度和耗时，
不会记录 payload、token、Authorization 或完整查询串。`--quiet` 只关闭终端进度输出，
不会关闭文件或 stderr 日志。

脱敏样本（每端 2 行）：

```jsonl
{"component":"server","event":"tunnel.requested","tunnel_id":"t-abc123","worker_id":"w-plc"}
{"component":"server","event":"tunnel.worker_connected","tunnel_id":"t-abc123","rendezvous_ms":4}
{"component":"worker","event":"tunnel.opened","tunnel_id":"t-abc123","target":"192.168.1.10:502"}
{"component":"worker","event":"tunnel.first_down","tunnel_id":"t-abc123","first_byte_ms":12,"bytes_down":64}
{"component":"forward","event":"tunnel.first_up","tunnel_id":"t-abc123","session_id":"127.0.0.1:59948","first_byte_ms":3}
{"component":"forward","event":"session.closed","tunnel_id":"t-abc123","bytes_up":32,"bytes_down":64,"close_reason":"client_closed"}
```

容器隔离冒烟脚本位于 [`scripts/smoke/tunnel/run-smoke.sh`](../../scripts/smoke/tunnel/run-smoke.sh)，说明见同目录 README。脚本覆盖 serve、worker、echo 隔离和 11 项检查。

`tunnel check` 只验证 worker 拨号与授权，不会建立 UDP 监听，也不代表业务协议端到端可用。UDP 转发仅支持固定目标的单播；监听端按来源地址建立会话，空闲 60 秒回收，最多 32 个来源。

设备**从另一个端口回包**是可以的：worker 侧用非连接 socket，回包只要源 IP 是目标设备就转发（端口不限），其它主机的数据报忽略。早期版本用 connected socket，这类回包会被静默丢弃，现象是隧道建立后一直等不到响应、最终超时——若遇到这个现象，先确认 worker 已升级到含该修复的版本。

常见问题：协议过旧时升级 worker；目标未放行时增加 `tunnel.allow`；达到 `max_conns` 时关闭不用的转发；广播和组播发现仍不支持。

server 审计事件包含 `tunnel_id caller worker target client_remote bytes_up bytes_down close_reason duration_ms`；worker 事件包含 `tunnel_id target error_code bytes_from_device bytes_to_device duration_ms`。

server 侧拒绝统一记 `tunnel.rejected`：client connect 一侧带 `status`（HTTP 状态）与 `error_code`（`http_rejected`，或 worker 回的 Hello 错误码如 `target_not_allowed`）；worker 回连一侧带 `side=worker` 与 `error_code`（`invalid_nonce` / `worker_mismatch` / `instance_mismatch` / `tunnel_mismatch` / `rendezvous_gone` / `worker_unauthorized` / `hello_read_failed`），并附 `close_code`（4401/4409/4404）。同一 `tunnel_id` 下先看 worker 侧的拒绝原因，client 侧的 504 只是其结果。

## 转发预设

转发规格较长时可存成具名预设，之后用 `--name`（`-n`）复用：

```bash
gofer tun save hw-win11 -w w-lab --note "现场 HMI + PLC" \
      udp/21845:192.168.0.200:21845,1502:192.168.0.100:502   # 逗号/空格都可以
gofer tun saved              # 列 server 上的预设（tun ls 是转发进程与活跃隧道，不要混）
gofer tun forward -n hw-win11
gofer tun check -n hw-win11   # 逐个检查预设里每条规格的设备地址
gofer tun forget hw-win11
```

一条预设可存多条规格（存的是**拆开后**的数组），一次全开。`save` 时每条规格都会在两端校验，非法规格不落库；同名预设需 `--force` 才覆盖（server 回 409，CLI 直接报错）。

### 预设存在 server 上（v0.60.2 起）

预设不再只属于"执行 `save` 的那台机器"，而是存在 **server 的 `tunnel_presets` 表**里，因此：

- 任何能连到 hub 的机器 `tun saved` 看到同一份预设，换机器不必重存一遍；web 的「设置 → Tunnels」页也编辑同一份数据。
- `tun forward -n` / `tun check -n` 只查 server：server 上没有同名预设、或 hub 连不上，都直接报错，不再回落本机 `<config-dir>/tunnels.yaml`。
- `tun save` 只写 server；连不上 server 时报错，不再写本地副本。`tun saved` 只列 server 的预设。
- `tun forget <name>` 只删 server 上的预设，不存在时按错误报告。
- 本机旧 `tunnels.yaml` 里的预设不再被 CLI 读取；要迁移上去，用 web「设置 → Tunnels」页的「导入本机预设」（读 server 所在机器的 `tunnels.yaml`），或用 `tun save` 重新保存。`gofer tun presets push` 已移除。

`forward`/`check` 显式给出的 worker 或规格优先于预设，便于临时改端口而不必先改预设。

