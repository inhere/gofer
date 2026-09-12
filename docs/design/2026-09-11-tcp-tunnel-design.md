# TCP 隧道（经 worker 端口转发）设计 — TUN-01

> 让本机进程像访问本地端口一样访问 **远端 worker 所在网络** 里的 TCP 服务（典型：工控机 B 旁挂的 PLC Modbus TCP server），链路复用 worker 已有的「出站连 server」能力，B 无需任何入站端口。
> 机制沿用 WEB-03 远端 pty 隧道已验证的「控制帧下发 + 每会话一条专用数据 ws」模式（见 [`2026-07-04-web-pty-attach-P2-design.md`](./2026-07-04-web-pty-attach-P2-design.md) D-P2-1）。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-09-11 | Claude | 初版：场景、方案取舍、协议 v5、安全模型、配置、CLI、代码落点、实施拆分 T1–T4。 |
| v0.2 | 2026-09-11 | Claude | 补 §13.2 PLC IDE 经 worker 侧 Gateway 远程在线（CODESYS）、§13.3 其它 IDE/HMI 软件判断方法；§11 补 UDP 广播发现类工具的限制与替代。CLI 须可在 Windows 运行。 |
| v0.3 | 2026-09-11 | Claude | `TunnelOpen` 与 ws① 预留 `network` 字段（v1 仅 tcp，worker 对非 tcp 明确拒绝），使后续 UDP 转发为纯增量；§11 补 UDP 转发复杂度评估与「已有 VPN」时的取舍。 |
| v1.0 | 2026-09-11 | Claude | 已实现：协议 v5 `tunnel_open`、server 两个 ws 端点与 `GET /v1/tunnels`（响应 `{"tunnels":[...]}`）、worker 隧道处理（白名单热重载）、`gofer tunnel forward/check/ls`；端到端回归 `scripts/smoke/tunnel/run-smoke.sh`（11 项）。待补：Go 端到端测试与 ws② 校验关闭码的单元测试。 |
| v1.1 | 2026-09-12 | Claude | 白名单端口半段支持逗号列表与闭区间范围（`192.168.1.10:502,1217,11740-11743`），同一设备多端口写一行；`*` 不与列表混写。纯增量，老条目语义不变。 |

## 2. 背景与目标

**场景**：本机开发一个上位机服务（Go），需要连接设备的 Modbus TCP server（如 `192.168.1.10:502`）做真机调试；设备只接在另一台电脑 B 的网络上，本机不可达。B 可部署 gofer worker 连入本机 server。

**目标**：

- 本机 `127.0.0.1:1502` ⇄（server ⇄ worker B）⇄ B 拨出 `192.168.1.10:502`，**透明 TCP 字节流**，业务代码只改连接地址。
- 通用 TCP，不解析上层协议（Modbus/HTTP/SSH/数据库…都可）。
- B 侧只需在 `worker.yaml` 显式放行目标；默认关闭。

**非目标（v1 不做）**：UDP、串口（Modbus RTU，可在 B 上用串口转 TCP 工具后再走本隧道）、SOCKS 动态代理、Web 控制台 UI、TCP 半关闭语义、多 hub。

## 3. 方案取舍

| 维度 | 选项 | 结论 |
|---|---|---|
| 数据走哪条 ws | A. 复用 worker 控制 ws 多路复用（JSON 信封里 base64） | ✗ 与 job 日志/心跳抢同一条连接和单写锁，大流量会饿死心跳；base64 膨胀 33% |
| | B. **每条 TCP 连接一条专用数据 ws**（pty 同款） | ✓ 隔离、二进制帧、已有 nonce 鉴权范式可复用；Modbus 场景连接数少且长连，握手开销可忽略 |
| 监听端口开在哪 | C. server 侧开监听 | ✗ 暴露面大、多用户端口冲突、server 可能在别的机器 |
| | D. **客户端侧（CLI）开监听** | ✓ `kubectl port-forward` / `ssh -L` 心智；监听默认只绑 127.0.0.1；CLI 在容器里跑也行 |

**零开发替代**（功能上线前可先用）：B 上 `ssh -R`（需本机开 sshd）/ chisel / frp；只需一次性读几个寄存器时，可 `gofer job run --runner worker --worker-id <B> -a exec -- <modbus 命令行工具>` 在 B 上直接执行。

## 4. 总体链路

```txt
 本机(或容器)                          server (gofer serve)                        电脑 B (gofer worker)          设备网段
┌──────────────┐  TCP  ┌─────────────────┐ ws① /v1/tunnels/connect ┌──────────────┐ ws② /v1/workers/tunnel-connect ┌──────────┐ TCP ┌──────────┐
│ 上位机服务    ├──────►│ gofer tunnel     ├────────────────────────►│ 拼接 ①⇄②     │◄───────────────────────────────┤ 隧道处理  ├────►│ PLC :502 │
│ →127.0.0.1:1502│      │ forward (监听)   │  Bearer(user caller)    │ (splice)     │  Bearer(worker)+一次性 nonce     │ 白名单+拨号│     └──────────┘
└──────────────┘       └─────────────────┘                          └──────┬───────┘                                 └────▲─────┘
                                                                           │ 控制 ws（已有）: tunnel_open{tunnel_id,target,nonce}  │
                                                                           └──────────────────────────────────────────────────────┘
```

一条本地 TCP 连接的生命周期：

1. CLI accept 本地连接 → 拨 ws① `GET /v1/tunnels/connect?worker=<id>&target=<host:port>`（Bearer）。
2. server 校验 caller → 查 worker 在线且协议 ≥ v5 → 生成 `tunnel_id` + 一次性 nonce（绑定 worker_id + instance_id + tunnel_id，TTL 15s）→ 经控制 ws 发 `tunnel_open` → **挂起等待 rendezvous（15s）**，此时**尚未**接受 ws① 的升级。
3. worker 收到 `tunnel_open` → 白名单/并发上限检查 → `net.DialTimeout(target, 5s)` → 无论成败都拨 ws② 并先发 hello（成功 `error` 为空；失败带 `error_code`）。
4. server 收 ws② hello：校验 nonce/实例/tunnel_id → 若 hello 带错误，把错误映射成 HTTP 状态**拒绝 ws① 升级**；否则接受 ws① 升级，开始 ①⇄② 拼接。
5. 任一端断开（本地连接关闭 / PLC 断开 / 任一 ws 断 / 保活失败）→ 整条链路拆除，server 打审计日志。

> 「先 rendezvous 后升级 ws①」是有意的：所有失败（目标未放行、拨号失败、worker 离线/过旧、超时）都以 **HTTP 状态码 + 一行文本** 返回给 CLI，错误清晰且好测；升级成功即代表到设备的 TCP 已连通。

## 5. 协议

### 5.1 控制帧（wsproto，协议 v5）

- 新增 `TypeTunnelOpen FrameType = "tunnel_open"`（s→w），payload：

  ```go
  type TunnelOpen struct {
      TunnelID   string `json:"tunnel_id"`
      Target     string `json:"target"`      // host:port，已由 server 做语法校验
      RelayNonce string `json:"relay_nonce"` // 一次性，worker 在 ws② hello 中回传
      Network    string `json:"network,omitempty"` // 空=tcp；预留 "udp"（v0.3），v1 worker 对非 tcp 回 bad_target
  }
  ```

- `CurrentProtocolVersion` 4 → **5**；新增 `TunnelMinProtocolVersion = 5` 与 `SupportsTunnel(proto int) bool`（同 `SupportsReload`/`SupportsPolicy` 写法）。`MinProtocolVersion` **不动**（仍为 2，不能踢掉老 worker）。
- **不新增 w→s 帧、不改 register/Caps**：worker 的成败经 ws② hello 回报；server 只凭注册时的 `ProtocolVersion` 判断能否下发。老 worker 读循环本就忽略未知帧，但 server 不会给它发。

### 5.2 客户端 ws①：`GET /v1/tunnels/connect`

- 注册在 `/v1` 鉴权组**之外**（与 `/v1/workers/pty-connect` 一样：ws 升级失败要裸状态码），handler 自行 Bearer 鉴权。
- 查询参数：`worker`（必填，worker_id）、`target`（必填，`host:port`，`net.SplitHostPort` 可解析、端口 1–65535、host 非空）、`network`（可选，缺省 `tcp`；v1 只接受 tcp，其它值 400——为 UDP 预留）。
- 升级前失败 → 裸 HTTP 状态 + `text/plain` 一行原因：

| 状态 | 条件 |
|---|---|
| 400 | 参数缺失/`target` 语法非法；worker 回 `bad_target` |
| 401 | 无/未知 token |
| 403 | caller 是 worker token（只允许 user caller）；开启 `require_tunnel_capability` 而 caller 无 `can_tunnel`；worker 回 `disabled` / `not_allowed` |
| 404 | worker 不在线 |
| 409 | worker 协议 < v5（提示升级 worker） |
| 429 | worker 回 `limit`（超出 worker 并发隧道上限） |
| 502 | 下发控制帧失败；worker 回 `dial_failed`（原因带上 worker 侧拨号错误文本） |
| 503 | server 未装配 hub |
| 504 | rendezvous 超时（15s 内 worker 没回 ws②） |

### 5.3 worker 数据 ws②：`GET /v1/workers/tunnel-connect`

- 同样在鉴权组外、自行 Bearer 鉴权；**caller_id 必须等于 nonce 绑定的 worker_id，且该 worker 当前 live instance_id 等于绑定值**（与 `handlePtyConnect` 同一套校验）。
- 第一条消息为文本 JSON hello（读超时 10s）：

  ```go
  type tunnelConnectHello struct {
      TunnelID   string `json:"tunnel_id"`
      RelayNonce string `json:"relay_nonce"`
      ErrorCode  string `json:"error_code,omitempty"` // disabled|not_allowed|limit|dial_failed|bad_target
      Error      string `json:"error,omitempty"`      // 人读原因
  }
  ```

- nonce 无效/过期 → close 4401；实例不符 → 4409；tunnel_id 与绑定不符或等待方已不在（客户端已放弃）→ 4404。

### 5.4 数据面

- **每个 ws binary 消息 = 一段原始 TCP 字节**；发送端每段 ≤ 32 KiB，接收端 `SetReadLimit(64 KiB)`。text 消息保留（v1 收到即忽略）。关闭 ws 压缩。
- **不支持 TCP 半关闭**：任一方向读到 EOF/错误即整条拆除（Modbus/绝大多数请求-响应协议不受影响，写进限制说明）。
- server 拼接两条 ws 时双向逐消息转发（阻塞写即天然背压），并累计上/下行字节。
- **保活**：server 对 ws①、ws② 每 30s `Ping`（超时 10s），失败即拆除——用于识别 B 断网等半开连接。CLI/worker 两端只要在 `Read` 就会自动回 pong，无需额外代码。
- 本地/设备侧 TCP 用 Go 默认（`TCP_NODELAY` 开），对 Modbus 这类小包请求-响应友好。

## 6. 安全模型

与架构总览「执行授权留在对端」同一不变量：**能连到哪由 worker 本地配置说了算，server 逼不了 worker 拨白名单外的地址。**

1. **worker 白名单，默认拒绝**：`tunnel.allow` 为空/缺省 = 本 worker 不提供隧道（hello 回 `disabled`）。条目语法：
   - `host:port` —— 精确匹配（host 为 IP 或主机名，主机名大小写不敏感；IPv6 写 `[addr]:port`）。
   - `CIDR:port` —— 如 `192.168.1.0/24:502`，**只匹配 IP 字面量目标**（不为匹配 CIDR 去解析主机名，避免 DNS 带来的意外放行）。
   - 端口可写 `*` 表示任意端口，如 `192.168.1.10:*`。
   - 端口也可写逗号列表与闭区间范围，如 `192.168.1.10:502,1217,11740-11743`（一台设备的多个端口写一行）。`*` 不与列表混写，否则条目实际放开多大一眼看不出来。
   - 非法条目在 worker 加载配置时报错（启动失败 / reload 被拒，沿用现有 reload 失败语义）。
   - worker 拨号使用请求里的**原样 target**；白名单每次打开时读**当前生效配置**（`gofer worker reload` 后即生效）。
2. **只允许 user caller**：worker token 调 ws① 一律 403。
3. **可选能力位**：`server.governance.require_tunnel_capability: true` 时，caller 须 `can_tunnel: true`；与 `require_attach_capability`/`can_attach` 完全同构（含 loader 的「开了开关却没有任何 caller 有能力位 → 配置报错防锁死」校验）。
4. **ws② 鉴权**：worker token + 一次性 nonce（绑定 worker_id/instance_id/tunnel_id，15s 过期，Consume 即删）。
5. **资源上限**：worker `tunnel.max_conns`（默认 8）限制并发隧道；server 侧 rendezvous 挂起有 15s 上限。
6. **审计**：server 在打开/关闭时 `slog.Info` 记录 `tunnel_id, caller, worker, target, client_remote, duration_ms, bytes_up, bytes_down, close_reason`；worker 同样记录打开/关闭与拨号失败。token/nonce 不入日志。
7. CLI 本地监听默认只绑 `127.0.0.1`；需要让局域网其他机器用时显式写 `0.0.0.0:1502:...`。

## 7. 配置

worker.yaml（电脑 B）：

```yaml
tunnel:
  allow:                   # 必填才开启；为空 = 不提供隧道
    - "192.168.1.10:502,1217,11740-11743"   # 端口支持列表与范围
    - "192.168.1.0/24:502"
  max_conns: 8             # 并发隧道上限，<=0 取默认 8
  dial_timeout_sec: 5      # 拨目标超时，<=0 取默认 5
```

server config（可选收紧）：

```yaml
server:
  governance:
    require_tunnel_capability: true
  callers:
    - id: dev
      token_env: GOFER_DEV_TOKEN
      can_tunnel: true
```

## 8. CLI

```bash
# 本地 1502 → B 网络里的 192.168.1.10:502（可一次写多条）
gofer tunnel forward -w w-plc 1502:192.168.1.10:502 1503:192.168.1.11:502
gofer tunnel forward -w w-plc 0.0.0.0:1502:192.168.1.10:502   # 显式绑所有网卡

# 一次性连通性自检：开一条隧道立刻关，打印耗时；失败退出码非 0
gofer tunnel check -w w-plc 192.168.1.10:502

# 列出 server 上当前活跃隧道
gofer tunnel ls
```

- 命令组 `tunnel`（别名 `tun`），子命令 `forward`（别名 `fwd`）/ `check` / `ls`；沿用 `-c/--config`、`-s/--server`、`--token`（`bindConfigFlag` + 现有 `newClient` 解析地址与 token，G011）。
- forward 规格 `[bind:]lport:host:port`，host 为 IPv6 时写 `[addr]`；`lport=0` 表示随机端口（启动时打印实际端口）；bind 缺省 `127.0.0.1`。
- 每条本地连接：打印打开（耗时）/关闭（时长、上下行字节）；打开失败打印 server 返回的状态与原因并关闭该本地连接（上位机的 Modbus 客户端会按自身策略重连）。`Ctrl+C` 关闭监听并拆掉所有隧道。
- `GET /v1/tunnels`（鉴权组内 JSON，list 风格同 `/v1/runners`）：`id, caller_id, worker_id, target, client_remote, started_at, bytes_up, bytes_down`。

## 9. 代码落点与分层（G021/G022/G024）

| 位置 | 内容 |
|---|---|
| `internal/wsproto` | `TypeTunnelOpen`、`TunnelOpen`、`TunnelMinProtocolVersion`/`SupportsTunnel`、`CurrentProtocolVersion=5`（同步修正钉死 4 的测试） |
| `internal/config` | `WorkerConfig.Tunnel`（`WorkerTunnelConfig{Allow, MaxConns, DialTimeoutSec}`）+ 校验；`CallerConfig.CanTunnel`；`Governance.RequireTunnelCapability` + loader 防锁死校验；`ServerConfig.CallerCanTunnel` |
| `internal/tunnel`（**新叶子包**，仅 stdlib + `coder/websocket`） | 白名单解析/匹配；forward 规格解析；`Bridge(ctx, ws, net.Conn)`（CLI 与 worker 共用的 ws⇄TCP 双向泵，返回字节统计）；`Splice(ctx, a, b *websocket.Conn)`（server 用，含保活 ping 与字节统计）；server 侧 rendezvous 注册表（tunnel_id/nonce 发放、一次性消费、投递 ws② 并等待拼接结束、超时）；活跃隧道表（供 `/v1/tunnels`）。满足 G024：域自洽、对外只暴露小接口 |
| `internal/wshub` | `Hub.OpenTunnel(workerID string, t wsproto.TunnelOpen) error`：离线 → `ErrWorkerOffline`；`!SupportsTunnel(proto)` → 新 `ErrTunnelUnsupported`；复用 `writeFrame`。**不占** job 并发槽位 |
| `internal/httpapi` | `/v1/tunnels/connect`、`/v1/workers/tunnel-connect`（均鉴权组外）、`GET /v1/tunnels`；rendezvous 注册表由 `Server` 自持（`New` 内创建，不新增外部装配点） |
| `internal/worker` | `recvLoop` 增 `case wsproto.TypeTunnelOpen` → `go cl.handleTunnelOpen(...)`（不进 `dispatchWG`，ctx 取消时全部拆除）；白名单从当前生效配置读（reload 生效）；ws② 地址按 `derivePtyConnectURL` 同法派生 |
| `internal/client` | `DialTunnel(ctx, workerID, target) (*websocket.Conn, error)`（http(s)→ws(s)、Bearer、把失败响应的状态码+正文变成可读错误）；`ListTunnels()` |
| `internal/commands` | `tunnel.go`：只做 flag 绑定/校验并转调 `internal/tunnel` 的 forwarder（监听循环不放 commands，G021） |

## 10. 兼容性

- 协议只加不改：`MinProtocolVersion` 不动；老 worker（v2–v4）继续可用，只是 `tunnel` 请求回 409。新 worker 连老 server：永远收不到 `tunnel_open`，行为不变。
- 不改 register / Caps / Policy 帧，不占 job 槽位，不影响现有 dispatch/pty 路径。
- worker.yaml 不写 `tunnel` 段 = 与今天逐字相同（默认关闭）。

## 11. 已知限制与后续

- 每条 TCP 连接一次 ws 握手 + rendezvous（通常几十 ms）；对「频繁短连接」协议不友好——后续可在 ws① 上做 yamux 多路复用。
- 不支持半关闭；不支持 UDP。依赖 **UDP 广播发现**的工具（PLC IDE 网络扫描、部分 HMI 组态软件搜索设备）不能直接穿过隧道：优先把其网关/代理放到 worker 侧、本机只转发网关的 TCP 端口（见 §13.2）；确需 UDP 时再做 UDP 转发（TUN-02 候选）或用三层 VPN（全协议透明，但配置与暴露面更大）。
- **UDP 转发（TUN-02 候选）评估**：数据面已是按消息转发，UDP 数据报 1:1 映射为 ws 消息，server 拼接无需改动；需增加：`network` 字段启用（v5 已预留，届时无需再升协议版本）、白名单区分 udp、worker 端 connected UDP 收发、CLI 端 UDP 监听按来源地址建会话 + 空闲超时（UDP 无断开信号）。约一个实施任务（~300 行代码 + ~300 行测试）。只覆盖**单播且目标固定**的 UDP；广播/组播发现、报文内嵌 IP 地址的协议（端口映射后地址不符）仍不适用。
- **与现有 VPN 的关系**：若各机器已在同一 VPN 中，可在 VPN 中把设备网段路由到 worker 所在机（VPN 服务端路由 + 该机 IP 转发/NAT），则 TCP/UDP 单播原生可达（广播仍需 L2 桥接）。本隧道的优势是：无需改网络配置、按白名单细粒度放行、各现场设备网段重叠也无冲突（目标在 worker 本地拨号）、无 VPN 的现场同样可用。
- 设备端并发连接数通常很小（部分 PLC 仅 1–4 个 Modbus TCP 连接）：隧道是 1:1 透传，**本地开几条连接就占设备几条**，调试时注意别和现场上位机抢连接。
- 后续可选：`gofer tunnel socks`（SOCKS5 动态目标，仍受 worker 白名单约束）、Web 控制台展示活跃隧道、metrics 计数、server 侧 per-caller 隧道配额。

## 12. 实施拆分（host codex 实施，主控审核/验收/提交）

| 任务 | 范围 | 验收 |
|---|---|---|
| T1 基础层 | §9 的 wsproto + config + `internal/tunnel` 全部（allowlist/spec/Bridge/Splice/rendezvous/活跃表）+ 单测 | `go build ./... && go vet ./... && go test ./internal/wsproto/... ./internal/config/... ./internal/tunnel/...` 绿；全量 `go test ./...` 绿 |
| T2 server 侧 | `Hub.OpenTunnel` + httpapi 三个端点 + 审计日志 + 单测（401/403/404/409/504、worker token 被拒、能力位） | 同上 + 新测覆盖 §5.2 表中可在单测构造的状态 |
| T3 worker + CLI | worker `handleTunnelOpen`、`internal/client` 两个方法、`gofer tunnel forward/check/ls`；**端到端测试**：真 hub+httpapi+worker+本地 TCP echo：放行目标往返字节一致、未放行 403、拨号失败 502、超限 429、老协议 409 | 全量绿；容器内起独立 serve（非 live 端口）+ worker + echo 手工冒烟 `forward`/`check`/`ls` |
| T4 文档 | worker 示例配置 `tunnel` 段、README/用法、roadmap 行、架构总览提一句 | 文档与实现一致 |

端到端回归 = `scripts/smoke/tunnel/run-smoke.sh`（Go 版 `TestTunnelE2E*` 另行跟进）。

## 13. 使用示例

### 13.1 Modbus 真机调试

1. 电脑 B：`worker.yaml` 加 `tunnel.allow: ["192.168.1.10:502"]`，然后 `gofer worker reload <B 的 worker_id>`（或重启 worker）。B 的 gofer 需是支持协议 v5 的新版本。
2. 本机：`gofer tunnel check -w <B> 192.168.1.10:502` 确认连通。
3. 本机：`gofer tunnel forward -w <B> 1502:192.168.1.10:502`（保持运行）。
4. 上位机服务把 Modbus 地址配成 `127.0.0.1:1502`；超时建议 ≥ 1–2s（多了一段 B↔server 的网络往返）。

### 13.2 PLC 编程 IDE 远程在线（以 CODESYS V3 为例）

CODESYS IDE 不直连 PLC，而是经 **Gateway**（TCP 1217）通信；Gateway 再用 UDP 1740–1743（网络扫描为广播）或 TCP 11740 连 PLC。广播/UDP 过不了本隧道，所以**把 Gateway 放在 worker 侧**：

1. 电脑 B 运行 CODESYS Gateway；`worker.yaml`：`tunnel.allow: ["127.0.0.1:1217"]`。
2. IDE 所在的 Windows 主机：`gofer tunnel forward -w <B> 11217:127.0.0.1:1217`（避开本机自带 Gateway 占用的 1217）。
3. IDE 通讯设置 → 添加网关 `127.0.0.1:11217` → 扫描网络（扫描发生在 B 的网段）→ 登录/下载/在线监视。

备选：PLC 固件开启了 TCP 块驱动时可只转发 `11740` 直连，依赖固件，不如 Gateway 方案稳。

### 13.3 其它 IDE / HMI 组态软件的判断方法

- 能手动填设备 IP、且下载/在线走**纯 TCP** → 可用：逐端口转发，IDE 目标 IP 填 `127.0.0.1`（端口写死时本地端口用同号）。
- 依赖 **UDP 广播搜索**或 UDP 传输 → v1 不支持：优先找「worker 侧网关/代理」形态（同 13.2），否则需 UDP 转发（后续）或三层 VPN。
- 确认方式：在与设备同网段的机器上用该软件操作一次，同时用 Wireshark / `netstat -ano` 观察协议与端口。
- 软件拒绝 `127.0.0.1` 或校验同网段时：本机加一块环回网卡（Windows KM-TEST Loopback Adapter）配成设备网段地址，forward 的 bind 绑到该地址。
- 远程下载/在线修改会驱动真实设备，操作前确保现场知情。
