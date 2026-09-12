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

错误码：400 参数或网络不支持；401 凭证无效；403 无权限或目标未放行；404 worker 不在线；409 worker 协议过旧（需升级到支持协议 v5 的版本）；429 超出连接上限；502 worker 拨号失败；504 等待 worker 回连超时。

## 验证与排障

容器隔离冒烟脚本位于 [`scripts/smoke/tunnel/run-smoke.sh`](../../scripts/smoke/tunnel/run-smoke.sh)，说明见同目录 README。脚本覆盖 serve、worker、echo 隔离和 11 项检查。

`tunnel check` 只验证 worker 拨号与授权，不会建立 UDP 监听，也不代表业务协议端到端可用。UDP 转发仅支持固定目标的单播；监听端按来源地址建立会话，空闲 60 秒回收，最多 32 个来源。

常见问题：协议过旧时升级 worker；目标未放行时增加 `tunnel.allow`；达到 `max_conns` 时关闭不用的转发；广播和组播发现仍不支持。

server 审计事件包含 `tunnel_id caller worker target client_remote bytes_up bytes_down close_reason duration_ms`；worker 事件包含 `tunnel_id target error_code bytes_from_device bytes_to_device duration_ms`。

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
