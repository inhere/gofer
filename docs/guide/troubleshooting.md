# 常见问题

每条按 **现象 → 原因 → 处理** 排。先看现象对得上哪条，命令都是在 client / server 机器上直接能跑的。更细的 agent 侧排障（recovering、waiting_dir、预算超限等）见 [skill 的 troubleshooting](../../skills/gofer-usage/references/troubleshooting.md)。

## 连不上 server

**现象**：`gofer job list` / `gofer project list` 报 `connection refused`、`no such host`、一直转圈。

**原因**：CLI 不知道 server 在哪，或者那台机器连不到它。客户端凭据来自 `<config-dir>/.env`（`GOFER_SERVER_ADDR` / `GOFER_SERVER_TOKEN`）以及环境变量，最常错的是地址。

**处理**：

```bash
gofer config info            # 看解析出的配置路径、关键 ENV（含 server 地址来源）
curl -s http://<server>:8765/health    # 从这台机器直连一下
```

- server 没起：在 server 机器上 `gofer serve status`（受管服务）或看 `<config-dir>/run/serve.log` 尾部。
- 从别的机器 / 容器提交：先 `gofer init client`，把 `GOFER_SERVER_ADDR` 写成 `http://<server>:8765`（容器里可能是 `http://host.docker.internal:8765`）。
- **在 gofer 配置目录里执行 gofer 会连本地 `127.0.0.1`**：当前目录（或上级）有 `config.yaml` 时会被当成本地 server 配置。`cd` 回工作目录再执行。
- 地址写对了但还是不通：端口没放开 / 绑到了 `127.0.0.1`（改 `server.addr`）、防火墙、反向代理没放行 WebSocket。

## 401 / 403

**现象**：`401 unauthorized`，或 `403` + 某个权限名。

**原因**：401 是 token 不对或没带上（`.env` 没被读到、用了另一台 server 的 token、`callers[]` 里删了这个人）。403 是 token 有效但**没有某个权限**（或超出了调用方上限）。

**处理**：

```bash
gofer auth whoami     # 我是谁、持有哪些权限（以及每项从哪来）
gofer auth routes     # 这条路由要什么权限
```

- 401：核对 `<config-dir>/.env` 的 `GOFER_SERVER_TOKEN`；临时用 `--token` / `--server` 覆盖试一次，能通就是 `.env` 的问题。改了 server 的 `callers` 后要 `gofer serve reload`。
- 403：缺的权限由根管理员发（Web「设置 → 权限」，或配置里给这个 caller），页面会写明缺哪个权限名。
- 看起来“东西不存在”（404）而不是 403：可能是调用方上限把它挡在项目范围外，服务端按不存在处理。上限由管理员在配置文件里写，Web「设置 → 权限」页顶部有你自己那份的摘要（接口字段是 `GET /v1/auth/whoami` 的 `ceiling`）。

## job 一直是 queued

**现象**：提交成功，状态停在 `queued`，没有日志。

**原因**：排队等三样东西之一——① 该调用方或该项目的**并发槽**满了；② 目标是某个**不在线的 worker**；③ 机器在**排空升级**中（server 在等空闲）。

**处理**：

```bash
gofer job show <id>                 # 状态与最后的说明
gofer job list -p <project>         # 这个项目在跑几个
gofer worker ls                     # 目标 worker 在线吗
gofer auth whoami                   # 我是谁、持有哪些权限（配额上限看配置里的 callers / governance）
```

- 并发满：等前面的结束，或让管理员调 `server.governance` / 项目并发上限。
- worker 不在线：在 worker 机器上 `gofer worker -d` 起回来（见下一条）；job 会继续等，不需要重新提交。
- 排在升级中：`paused_for_update`，更新完成后自动开跑，**不要重派**。
- 状态是 `waiting_dir` 而不是 `queued`：那是同目录锁——同一目录已有可写 job 在跑。等它、用 `--lock <子目录>` 收窄、或给这个 job 加 `--worktree`。

## worker 离线

**现象**：Web 的 Runners 页那张卡变灰 / 心跳停了；派过去的 job 一直排队或失败 `worker lost`。

**原因**：worker 进程没在跑；`worker_id` 三处没对齐（`server.workers` 键、worker 端 `worker_id`、`runners.<name>.worker_id`）；token 不匹配；hub 地址写错。

**处理**：

```bash
# worker 机器上
gofer worker doctor                 # 配置 / hub 地址 / token / roots / agent / 注册握手，一次全查
gofer worker                        # 前台跑，直接看报错；或 gofer worker -d 后台 + 看日志
# server 机器上
gofer worker ls
gofer worker show <worker-id>       # 协议版本、能力、策略状态
gofer worker projects <worker-id>   # 它在哪些项目里能干活
```

- 日志：worker 机器上 `<config-dir>/run/worker-<id>.log`（JSONL，按 `event` 字段筛 `worker.reconnecting` / `worker.policy_applied` / `worker.job_rejected`）。
- 连上了但项目不在列表里：`roots` 没覆盖该项目路径（被拒 `path_outside_roots`）、server 侧该项目 `allowed_runners` 没含这台 worker 的 runner 名，或策略还没下发（`policy_pending`，几秒后重试）。
- worker 重启后旧 job 状态：`recovering` 是 server 在等它重连（默认 120 秒窗口），**不要重派**；超时或换进程才判 `failed (worker lost)`。

## agent 未检测到

**现象**：`unknown agent "claude"`、`agent is not available`，或 Web Agents 页里看不到你装的那个 CLI。

**原因**：agent 得在那台**执行机**上（server 或对应 worker）存在。内置模板是**启动时探测 PATH** 注入的（装了才注入），所以刚装完 CLI 要重启 / 重载；或者你声明的 agent 名和提交时用的不一致；或者项目没放行这个 agent。

**处理**：

```bash
# 在执行那台机器上
gofer agent detect              # 跑各 agent 的探测命令
gofer agent ls                  # 配置里有哪些（内置的标「内置」）
gofer agent status              # 可用性与最近 job 的健康度
gofer agent probe <agent>       # 真跑一次探针
# server：改完配置让运行中的 server 重载（内置模板要重启才会重新探测 PATH）
gofer serve reload
```

- 新装的 CLI 没出现：重启 server / worker（内置模板只在启动探测 PATH 时注入）。
- 提交报 `agent "x" is not allowed in project`：项目 `allowed_agents` 加上它（`gofer project add --allow-agent …` 或 Web Projects 页）。
- 报 `interactive-only` / `has no batch mode`：agent 定义里误加了 `interactive: true`，应写 `args` + `interactive_args`。
- 报 `project "x" does not allow interactive jobs`：项目没开 `allow_interactive: true`。

## `config validate` 报错

**现象**：`gofer config validate` 非 0 退出，或 server 起不来并打印配置错误。

**常见原因与处理**：

| 报错 | 处理 |
|---|---|
| 项目路径不是绝对路径 / 在运行 gofer 的系统上不存在 | `host_path` / `container_path` 必须是**运行它的那台机器**上的绝对路径。在 Linux 上写 `D:/projects/demo` 会被拒；容器与主机路径不同就分别写 `container_path` |
| 项目 / agent / runner 名重复或被保留 | `server` / `local` 是保留名，不能当 worker id 或自定义 runner 名 |
| `ceiling` 子键拼错 / `ceiling: {}` | 上限按已知键严格解码，拼错直接报错；空对象也报错 |
| 没有 token | 配 `server.token_env`（推荐）或 `server.token`；只在本地试验时可以 `--allow-empty-token` |
| `work.digest` 无订阅提示 | 没配通知 webhook 却显式开了每日摘要；配一个 webhook 或关掉 `work.digest_enabled` |

```bash
gofer config info                    # 解析到了哪个文件、哪些 ENV 生效
gofer config show <project>          # 某个项目合并后的有效配置
gofer config lint                    # 哪些键可以删（含 extends 建议）
gofer config validate [server|worker]
```

改完配置让运行中的 server 生效：`gofer serve reload`（有些键需要重启，比如 runner 实例、retention 启停；错误信息里会说明）。

## Windows 上的路径与服务

**现象**：配置校验说路径不存在；`serve -d` 起来又立刻退出；命令在 Windows 主机上输出乱码或报“找不到命令”。

**原因**：Windows 的路径写法、服务运行身份、命令解释器都与 Linux 不同。

**处理**：

- **路径**：一律用绝对路径，`D:\work\...` 或 `D:/work/...` 都行，但**必须是运行 gofer 那台机器上的路径**。不要把 Linux 路径写进 Windows 的配置（反之亦然）。
- **后台 / 服务**：`gofer serve -d` 是分离进程（日志 `<config-dir>\run\serve.log`）；要开机自启、能自更新，用受管服务 `gofer serve register --start`（登录计划任务）或 Linux 的 systemd 单元。`gofer serve -d` 会等后台进程就绪再返回，提前退出会报错并附日志尾部。
- **看状态与日志**：`gofer serve status`、`gofer serve logs --lines 100 [--follow]`。
- **命令包装**：Windows 上让 job 跑批处理命令用 `cmd /c "…"`，不要用 PowerShell 双引号字符串拼命令（反引号会被当转义符吃掉，输出也容易被吞）。
- **编码**：job 输出在 Web 上按 UTF-8 显示；Windows 老程序输出的 GBK 文本会显示成问号，尽量让命令自己输出 UTF-8（`chcp 65001` 或程序选项）。

## 升级与回滚

**现象**：想升级到新版本，或者升级后有问题要退回去。

**升级**：

- **Web 一键更新**（受管 server）：设置 → 关于 → 检查更新 → 更新。server 会先排空（在跑的 job 跑完，新提交的 job 停在 `paused_for_update`），再换二进制重启。
- **手动换二进制**：`gofer serve upgrade --binary <新二进制路径>`（受管 server，带排空与失败回滚）；worker 端用 `gofer worker upgrade <worker-id>`（同 os/arch 用 server 自己的二进制，其他平台从下载源 `server.update` 取该平台的发布包；也可 `--file` 指定；`--source` / `--version` 可选；排空、交接、失败回滚）。`--no-wait` 只等到“接受”就返回。
- **看结果**：`gofer serve upgrade status`、`gofer serve status`、worker 机器的 `<config-dir>/run/worker-<id>.log`。

**回滚**：

- 受管 server：把**旧的二进制文件**留着，再 `gofer serve upgrade --binary <旧二进制>` 换回去；升级执行器在失败时会自动回滚到旧二进制。
- 自己起的 `-d` / 前台进程：停掉（`gofer serve stop` 或 Ctrl+C），换回旧二进制再启动。数据库是 SQLite，向前兼容（新版本写过的库旧版本能打开，但新功能的数据旧版本会忽略）。
- worker：`gofer worker upgrade <worker-id> --file <旧二进制>`。
- 配置是纯文本，改坏了直接改回；升级不会改你的配置文件。

## 还能看哪里

- 日志：server `<config-dir>/run/serve.log`、worker `<config-dir>/run/worker-<id>.log`、hook `<config-dir>/run/hook.log`（都是 JSONL）。
- 某个 job 的一切：Web 里 job 详情页（日志 / 事件 / 提交 / 用量 / 评论），或 `gofer job show <id>`、`gofer job logs <id>`、`gofer job review <id>`。
- 运维步骤：[`docs/runbook/`](../runbook/)：会话中继、容器 worker、隧道、HTTPS / PWA、Web Push、IM 通知、受管 server、worktree、重试……
