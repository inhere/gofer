# 运维：worker、受管 server、HTTPS、job 环境

> 管理员 / 运维向命令。AI agent 日常一般只用到「看 worker」；其余在用户要求时照做。完整 flag 以 `gofer worker|serve <子命令> --help` 为准。配置字段见 [server-config.md](server-config.md) / [worker-config.md](worker-config.md)，分步配方见 [setup-recipes.md](setup-recipes.md)。

## 目录

- [看 worker](#看-worker)
- [登记 / 移除 worker](#登记--移除-worker)
- [重载配置（不重启）](#重载配置不重启)
- [远程升级 worker](#远程升级-worker)
- [受管 server 服务](#受管-server-服务)
- [重启 / 升级 server 前](#重启--升级-server-前)
- [HTTPS 入口（手机 PWA）](#https-入口手机-pwa)
- [job 的环境清洗](#job-的环境清洗)
- [默认工作空间](#默认工作空间)

## 看 worker

```bash
gofer worker ls                       # server 上登记的 worker
gofer worker show <id>                # 连接状态、版本、在跑 job 数、projects / agents、传话进程状态、默认工作空间、策略状态、被拒 / 降级项、最近升级
gofer worker projects <id>            # 该 worker 实际生效的 project 清单（排查「project 不在该 worker」先看它）
gofer worker doctor                   # 在 worker 机器本机自检：配置、地址解析与连通、token、roots、agent 是否装了、注册握手
```

## 登记 / 移除 worker

```bash
gofer worker add <id> [--labels l] [--project key]   # 管理员：登记并输出一次性 worker token 与下一步命令
gofer worker remove <id>                              # 移除 runner、项目允许列表，并断开在线连接
gofer init worker --server <addr> --admin-token <管理员 token> --id <id> --project <key> --yes   # 在 worker 机器上一步接入
gofer worker stop [<id>]                              # 停本机后台 worker
```

- 向导（`gofer worker init` / `gofer init worker --server …`）向 server 要项目、推断 roots、探测 agent、写 worker.yaml、跑 doctor。管理员 token 只用于本次登记、不写盘；server 返回的 worker token 写进 `<config-dir>/.env`，worker.yaml 只保留 `token_env`。已有 token 时可省略 `--admin-token`。
- web Runners 页「添加 worker」完成同一操作。
- `server` / `local` 是保留名，不能用作 worker id 或自定义 runner 名。

## 重载配置（不重启）

```bash
gofer worker reload <id> [--reason "新增隧道白名单"] [--timeout 秒]   # 远程：经 server 让已连接的 worker 重读配置（Windows 也行，需管理员）
gofer worker reload --local [<id>] [--timeout 秒]                       # 本机：信号 / Windows 命名事件，等待回执文件
gofer serve reload -c <config> [--timeout 秒]                           # 本机 server 重读配置
```

- worker 热生效：agents / roots / guards / labels / max_concurrent / 隧道白名单；`worker_id`、`server_link`、storage、`xfer_timeout_sec` 要重启。结果：成功（打印新能力摘要）/ worker 的拒绝原因 / `offline` / 版本太旧（需升级重启）/ 超时（可能已应用，稍后 `worker show`）。
- server 也可 Unix `SIGHUP` 或 web 设置页「重新读取文件」。`server.workers` 与 type=worker 的 runner 增删改热生效；`server.addr`、token、tls、callers、session_messaging、agent_fallback、xfer 等改动列在 `restart_required` 里，需要重启。
- 主机新装了某个 agent CLI，想让探测可见：重载对应的 server 或 worker 一次。

## 远程升级 worker

```bash
gofer worker upgrade <id> [--file <worker 二进制>] [--force] [--drain-timeout 秒] [--no-wait] [--timeout 秒]   # 需管理员
```

- 不带 `--file` 时用 server 自己的可执行文件（要求同 os / arch）；带 `--file` 时先上传到 server 暂存，worker 下载后校验 sha256 并试跑 `--version`。
- worker 先排空（不再接新 job，在途 job 做完，默认最多等 600 秒，超时放弃升级并恢复接单；`--force` 不等），再切换二进制、拉起新进程；新进程注册成功后旧进程退出，60 秒内没注册则还原旧二进制，记为 `rolled_back`。
- CLI 默认等最终结果，打印「已升级到 vX（耗时）」或「已回滚：原因」，失败时退出码非 0。升级期间向该 worker 提交 job 会失败（`worker is being upgraded`）。
- 太旧的 worker 不支持远程升级，会提示手动升级一次。`gofer worker show <id>` 与 web Runners 卡片显示最近的升级记录。旧二进制保留为 `<exe>.old` 供手动回退。

## 受管 server 服务

把 server 登记成系统原生服务（Linux systemd、Windows 计划任务），由系统负责开机启动与崩溃重启：

```bash
gofer serve register -c <config.yaml> [--name gofer-serve] [--exe <binary>] [--work-dir <dir>] [--start]
gofer serve start|stop|restart|status|uninstall [--name <name>]
gofer serve status --json                 # 原生入口、受管进程身份、端口归属、健康、运行版本
gofer serve logs [--lines N] [--follow] [--journal]
gofer serve upgrade --binary <预构建二进制> [--name <name>] [--no-wait]
gofer serve upgrade status <upgrade_id> [--json]
```

- 登记默认不启动，`--start` 登记后启动并核对进程、端口与健康。
- Linux：默认 `--scope system`，必须显式 `--run-as <user>`；`--scope user` 用当前账号的 user manager。不会自动 sudo、开 linger 或提权。
- Windows：用当前交互账号的登录计划任务；`--elevated`（需已提权）与 `--adopt`（接管已验证的旧任务）仅 Windows 可用。受管进程不保留 cmd 窗口，日志用 `gofer serve logs` 看。
- 受管 server 始终以配置文件路径启动，pid 与默认日志在配置文件所在目录的 `run/`。具名实例（`--name`）用于隔离测试或迁移，要分开配置目录和端口。
- `uninstall` 停止并移除原生入口，保留程序、配置和数据。没有受管登记的实例仍可用 `gofer serve stop`（pidfile）。
- `serve upgrade`：校验预构建二进制，由独立的升级执行者在 server 排空后切换、核对健康，失败时恢复旧程序；默认等最终结果，`upgrade status` 读跨重启的回执（执行者已不在且非终态时显示 `interrupted`，不代表成功）。候选可以是 UPX 压缩包。
  - 从 job 里发起时：命令本身必须是 exec job 的进程（`-a exec -- <gofer 绝对路径> serve upgrade …`，不要包在 shell 脚本里，否则排空会等发起 job 自己结束直至超时）；job 不继承 `GOFER_CONFIG_DIR`，用 `--env GOFER_CONFIG_DIR=<config-dir>` 传入。
- 平台细节见 <https://github.com/inhere/gofer/blob/main/docs/runbook/serve-managed-internals.md>。

## 重启 / 升级 server 前

- **server 本机 runner 上有 job 在跑时不要直接重启**：本机 job 是 server 的子进程，重启后它们被判 `failed: orphaned: serve restarted…`（只有 worker 上的 job 能经 `recovering` 等新 server 接管）。先 `gofer job list --status running` 确认。
- 优雅停机时 server 先停止接新 job、取消本机执行的 job 并杀掉其进程树，再关库。
- CLI、server、web 要升级到同一版本（CLI 比 server 旧会缺命令 / 字段）。
- 用 `serve upgrade`（受管）或 `worker upgrade`（远程 worker），比手工换二进制安全。

## HTTPS 入口（手机 PWA）

```bash
gofer tool cert [--out-dir <dir>] --hosts <DNS 或 IP,…>   # 生成本地 CA + 服务器证书（ca.crt / ca.key / server.crt / server.key；已有 CA 则复用）
```

- server 配置加 `server.tls: {addr, cert_file, key_file}`：在原 HTTP 监听之外**另开**一个 HTTPS 监听（同路由同鉴权；CLI / worker 继续走 HTTP），改它需重启。路径可写 `{config_dir}/certs/server.crt`。
- 访问用的 IP / 主机名必须在 `--hosts` 里。Android 把 `ca.crt` 装成「CA 证书」后用 `https://<server-ip>:<port>` 打开，「安装应用」即得独立窗口 PWA 与 Web Push。
- 证书与私钥别入库、别进日志、别贴进汇报。完整步骤见 <https://github.com/inhere/gofer/blob/main/docs/runbook/https-pwa.md>、<https://github.com/inhere/gofer/blob/main/docs/runbook/web-push.md>。

## job 的环境清洗

gofer 起的 job / 子进程默认剔除：`GOFER_TOKEN` / `GOFER_SERVER_TOKEN` / `GOFER_WORKER_TOKEN`、`GOFER_CONFIG_DIR`（否则 job 里的 gofer 会读到 server 的 `.env` 里的操作员 token），以及 Claude Code 会话标记（`CLAUDECODE`、`CLAUDE_CODE_ENTRYPOINT`、`CLAUDE_CODE_SESSION_ID` 等——job 里的 claude 不会误当成上级会话的子进程）。用户自己的设置类变量（如 `CLAUDE_CODE_MAX_RETRIES`）照常传。

- 因此 job 里的 `gofer` 用 gofer 注入的 `GOFER_JOB_TOKEN`（只作用于本 job，不是用户身份），**不要指望从父环境继承配置目录或用户 token**。
- 额外剔除清单：server `job_env_denylist`；放行：项目 `job_env_allow`。
- `--env K=V` 的值会随 job 记录保存，别放密钥。

## 默认工作空间

每台机器有一个默认工作空间：`GOFER_WORKSPACE`，缺省 `~/.gofer/workspace`（`gofer init server` / `init worker` 会创建，serve 与 worker 启动时不存在也会自动创建）。server 配置没声明 `default` 项目时会自动注入一个指向它的内置 `default` 项目（列表标「内置」，不能删；在 web 里编辑保存后成为声明项目；显式声明 `default` 则整条覆盖）。`job run` 不带 `-p` 且 cwd 匹配不到项目时回落到它。web Runners 卡片的「工作目录」区显示它。
