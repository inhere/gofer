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

命令组别名为 `tunnel`/`tun`，子命令别名为 `forward`/`fwd`、`ls`/`list`；完整参数以 `gofer tunnel --help` 为准。

```text
$ gofer tunnel check -w w-plc 192.168.1.10:502
OK w-plc -> 192.168.1.10:502 (3 ms)
$ gofer tunnel check -w w-plc 192.168.1.99:502
ERROR: tunnel: HTTP 403: target not allowed
$ gofer tunnel check -w w-plc 192.168.1.10:503
ERROR: tunnel: HTTP 502: dial tcp 192.168.1.10:503: connect: connection refused
$ gofer tunnel forward -w w-plc 1502:192.168.1.10:502
forwarding 127.0.0.1:1502 -> w-plc:192.168.1.10:502
$ gofer tunnel forward -w w-plc udp/1502:192.168.1.10:1502
forwarding UDP 127.0.0.1:1502 -> w-plc:192.168.1.10:1502
$ gofer tunnel ls
ID CALLER WORKER TARGET CLIENT AGE UP DOWN
t-3967153a246e default w-plc 192.168.1.10:502 127.0.0.1:59948 2s 32 32
```

`tunnel forward` 支持 `--log-file <path>` 或 `--log-dir <dir>`（二选一）；目录模式生成唯一的 `forward-<YYYYmmdd-HHMMSS>-<pid>.log`。未指定时写入 `<config-dir>/run/tunnels/`。`--quiet` 仅关闭终端输出，文件日志仍保留；显式路径失败会使命令报错，默认路径失败则警告后降级为 stderr。

日志为 JSONL，可用同一 `tunnel_id` 关联 server、worker、forwarder：`rg '"tunnel_id":"t-..."' <config-dir>/run/tunnels/*.log`。`tunnel_id` 由 server 分配，随 connect 的 101 响应头 `X-Gofer-Tunnel-Id` 回传给 forwarder（旧 server 无此头时字段为空，其余不受影响）。

forwarder 事件（`component=forward`，每条都带 `session_id`；UDP 的 session_id 是本地来源 host:port，TCP 是本地连接 host:port）：`forward.started`（local/target/network）→ `session.opened` → `tunnel.opened`（`dial_ms`）→ `tunnel.first_up`/`tunnel.first_down`（`first_byte_ms`，自 dial 开始计，每 session 各一次）→ `session.closed`（`close_reason`、`bytes_up`、`bytes_down`、`duration_ms`）。`close_reason` 取值：`client_closed`（本地客户端挂断）、`worker_closed`（隧道远端挂断）、`idle`（UDP 空闲回收）、`ctx_done`（forwarder 退出）、`dial_failed`（隧道没建起来，带 `error`）、`write_failed`；超出 UDP session 上限的新来源记 `session.rejected`（`dropped_max_sessions`）。判断"慢在哪一段"：`dial_ms` 大 = rendezvous/worker 慢；`first_up` 正常而 `first_down` 迟迟不来或没有 = 设备侧无响应。

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
gofer tunnel save hw-win11 -w w-hw-windows11 --note "现场 HMI + PLC" \
      udp/21845:192.168.0.200:21845 1502:192.168.0.100:502
gofer tunnel saved            # 列出预设（tunnel ls 是活跃隧道，不要混）
gofer tunnel forward -n hw-win11
gofer tunnel check -n hw-win11   # 逐个检查预设里每条规格的设备地址
gofer tunnel forget hw-win11
```

一条预设可存多条规格，一次全开。预设保存在用户级 `<config-dir>/tunnels.yaml`（与 `worker.yaml` 同级，认 `GOFER_CONFIG_DIR`），文件权限 0600。`save` 时每条规格都会校验，非法规格不写入；同名预设需 `--force` 才覆盖。`forward`/`check` 显式给出的 worker 或规格优先于预设，便于临时改端口而不必先改预设。
