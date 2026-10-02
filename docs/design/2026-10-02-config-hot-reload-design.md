<!-- template_id: design; template_version: 1.1.1 -->
# server / worker 配置热重载补全（R 批）

> 状态：Approved（Draft 0.1；用户 2026-10-02 在 web 中继确认 R1–R6，W 向导下一批）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-02 | Claude | 基于现状调研的 6 项补全 |

## 背景

主机 server 跑在 Windows（计划任务后台进程），没有 SIGHUP；用户常不在 worker 机器旁（例：在 worker 上新增了 tunnel 配置后需要远程让它生效）。现有热重载能力分散、Windows 上不顺手，且有几处"看似热生效实际没生效"。

## 现状（2026-10-02 代码调研，路径相对仓库根）

- server reload 入口：unix SIGHUP（`internal/serve/serve.go` `startReloadLoop`）、`POST /v1/config/reload`（`internal/httpapi/config_write_handler.go`，需 can_admin，web「重新读取文件」）、控制台写接口（`Core.Update`）。核心在 `internal/core/core.go` `reloadLocked`：读文件、重探 agents、Rev+1、原子替换 Projects/Agents/Jobs 注册表，锁外 `flushPush` 推策略给 POLICY worker。无 `gofer serve reload` 子命令，无文件监听，Windows 无 reload 事件（`daemon.NotifyStop` 只有 stop）。
- 「需重启」分类在 `internal/config/editable.go`（`RestartRequired`）。
- 看似热生效实际启动时固化：`server.runner_probe`（prober 在 serve 启动时构建）、`server.notification` 的启用开关与 sweep 间隔（delivery loop）、`supervisor` 启用开关（supervisor loop）、`session:` 段（`SetSessionRelayPolicy` / `SetSessionInjectCommands` 只在启动时读，且不在 editable 表里，UI 无提示）。
- `server.workers` / `callers` 标记需重启，但 reload 会静默成功，hub 绑定 `workerBindings(cfg)` 与 `hubWorkerSelector.allowed` 在 `core.Build` 时固化——新增 / 删除 worker、改 token 都要重启。
- SIGHUP 读 `config.InputCfgFile`，HTTP 读 `core.WithConfigPath(opts.CfgPath)`，两者可能不一致。
- reload 响应只有 `{"status":"ok","reloaded":true}`，web 提示固定"已生效"，不区分需重启字段。
- worker：`gofer worker reload <id>`（经 server `POST /v1/workers/{id}/reload` → hub reload 帧，Windows worker 可用）、unix SIGHUP；Windows worker 本机无法 reload；web 无 worker reload 按钮。worker reload 热生效 agents / roots / guards / labels / max_concurrent / tunnel allowlist；`worker_id`、`server_link`、storage、`xfer_timeout_sec` 需重启。

## 方案

- **R1 便捷入口**：`gofer serve reload`（本机：优先发本地 reload 信号 / Windows 命名事件 `gofer-reload-<pid>`，与现有 stop 事件同机制，无需 token；可选 `--remote` 走 HTTP API 需 admin token）；`gofer worker reload --local`（同理对本机 worker 进程，读 worker 的 pid 文件）。Windows 上 serve / worker 都监听 reload 事件，映射到与 SIGHUP 相同的串行 reload 路径。
- **R2 reload 结果可读**：`Core.Reload` 返回 rev、变更的配置分区、以及**变更中需重启才生效的键**（与 editable 表 / 启动时固化清单对比新旧配置）；`POST /v1/config/reload` 与控制台写接口返回这些字段；web 设置页据此显示"已生效：…；需重启才生效：…"；`gofer serve reload` 打印同样内容。
- **R3 去掉"看似热生效"**：能低成本做成热生效的做成热生效——`notification` 启用开关 / 间隔变化时重建 delivery loop；`session:` 段在 reload 时重新应用 relay 策略与 inject 命令；`runner_probe` 变化时重建 prober；`supervisor` 启用开关同理（若改动过大则标需重启）。做不到的在 editable 表与 R2 输出里明确标需重启。`session:` 段补进 editable 分区。
- **R4 `server.workers` 热生效**：reload 时重算 hub worker 绑定与 selector：新增 worker 可立即注册；删除 worker 断开其连接（在途 job 按现有 worker 断开规则处理）；改 token 后旧 token 连接断开、新 token 可连。`callers` 若可同法热生效一并处理，否则保持需重启并在 R2 中提示。
- **R5 web worker reload 按钮**：Runners 页（worker 卡片与详情抽屉）加「重新加载配置」，调用现有 `POST /v1/workers/{id}/reload`，展示返回的 caps 摘要 / 失败原因 / 超时提示（504 不代表失败，提示稍后查看）。
- **R6 路径统一**：SIGHUP / Windows 事件 / HTTP 读取同一个配置文件路径（以 serve 启动时解析出的实际路径为准），启动日志与 reload 结果里都打印该路径。

## 测试与验收

固定测试：`TestServeReloadCommandLocal`、`TestReloadReportsRestartRequiredKeys`、`TestNotificationEnableHotReload`、`TestSessionBlockHotReload`、`TestRunnerProbeHotReload`、`TestWorkersHotReloadAddRemoveToken`、`TestReloadPathConsistent`，web Vitest（worker reload 按钮、设置页需重启提示）。Windows 事件路径需 Windows 构建下的测试或在汇报中说明验证方式。监督者容器验收：临时 serve 改配置后 `gofer serve reload` 输出；新增 worker 不重启即可连接；删除 worker 被断开；web worker reload 按钮。

## 不做

- 文件自动监听（fsnotify）。
- `server.addr` / `tls` / `storage` 等监听与存储类配置的热切换（保持需重启，R2 中明确提示）。
