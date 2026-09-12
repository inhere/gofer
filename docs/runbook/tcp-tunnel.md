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

条目支持 `host:port`、`CIDR:port`、任意端口 `*`，IPv6 使用 `[2001:db8::10]:502`。端口还可写逗号列表与闭区间范围，如 `192.168.1.10:502,1217,11740-11743`，同一设备的多个端口不必拆成多行；`*` 不能与列表混写。修改后执行 `gofer worker reload <worker-id>`，或重启 worker。worker 必须支持协议 v5。

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
$ gofer tunnel ls
ID CALLER WORKER TARGET CLIENT AGE UP DOWN
t-3967153a246e default w-plc 192.168.1.10:502 127.0.0.1:59948 2s 32 32
```

错误码：400 参数或网络不支持；401 凭证无效；403 无权限或目标未放行；404 worker 不在线；409 worker 协议过旧（需升级到支持协议 v5 的版本）；429 超出连接上限；502 worker 拨号失败；504 等待 worker 回连超时。

## 验证与排障

容器隔离冒烟脚本位于 [`scripts/smoke/tunnel/run-smoke.sh`](../../scripts/smoke/tunnel/run-smoke.sh)，说明见同目录 README。脚本覆盖 serve、worker、echo 隔离和 11 项检查。

常见问题：协议过旧时升级 worker；目标未放行时增加 `tunnel.allow`；达到 `max_conns` 时关闭不用的转发；UDP 和广播发现不支持，参见设计 §11、§13.2。

server 审计事件包含 `tunnel_id caller worker target client_remote bytes_up bytes_down close_reason duration_ms`；worker 事件包含 `tunnel_id target error_code bytes_from_device bytes_to_device duration_ms`。
