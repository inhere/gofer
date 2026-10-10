# 排障

> 先按现象查表；需要看日志时看「日志在哪」。worker 相关的判断先分清它是 LEGACY 还是 POLICY（见 [worker-config.md](worker-config.md)「模式」）。

## 目录

- [现象 → 处理](#现象--处理)
- [project 不在 worker 上](#project-不在-worker-上)
- [日志在哪](#日志在哪)
- [隧道慢 / 不通](#隧道慢--不通)

## 现象 → 处理

| 现象 | 处理 |
|---|---|
| `gofer job list` 连不上 / 401 | 主机 server 没起，或 `$GOFER_CONFIG_DIR/.env` 的 `GOFER_SERVER_ADDR` / `GOFER_SERVER_TOKEN` 与 server 不一致。`gofer config info` 看解析结果。 |
| 在某个目录执行 gofer 连到了 127.0.0.1 | 当前目录（或上级）有 gofer 的 `config.yaml`，被当成本地 server 配置；cd 回工作区再提交。 |
| `unknown project "xxx"` | project 没在**主机 server** 注册；用 `gofer project list` 确认正确 key。 |
| 不带 `-p` 却跑进了 `default` 项目 | cwd 匹配不到任何项目时回落到内置 `default`（默认工作空间）；显式带 `-p`。 |
| `unknown agent` | 该 agent 没在这台机器（server 或 worker）定义 / 安装；换已装的 agent，或去那台机器装上后重载。 |
| `agent "x" is interactive-only` / `has no batch mode` | agent 定义误加了 `interactive: true`（= 仅 pty）。批处理 + pty 两用应写 `args` + `interactive_args`，删掉 `interactive: true` 后重载 server。 |
| `has no interactive mode` | agent 没写 `interactive_args`。 |
| `project "x" does not allow interactive jobs` | 项目没开 `allow_interactive: true`（config 或 web 项目表单）。 |
| 提交后 `warning: --timeout … exceeds the project ceiling` | 超时被截到上限（默认 3600 秒）。拆 job，或请管理员改 `server.max_job_timeout_sec` / 项目 `max_timeout_sec`。 |
| `--sync` 返回了但看不到输出 | `--sync` 只回状态；输出用 `gofer job logs <id>`。默认只等约 30 秒，长任务加 `--wait-timeout` 或改异步 + `job watch`。 |
| job 状态 `recovering` | worker 断线，server 在恢复窗口内等它重连；**不要重派**，等它回到 running 或 `failed (worker lost)`。 |
| job 停在 `waiting_dir` | 目录被另一个可写 job 占着（`job show` 打出持有者）。等它、`--lock <子目录>` 收窄、`--worktree`，或只读任务加 `--read-only`。 |
| job 停在 `pending_interaction` | 工具调用等人批准：`gofer job interactions <id>` / `job answer`，或 web 作答。 |
| job 中途失败（`at capacity`、`rate limit` 等） | `gofer job resume <id> --prompt "…"` 续跑同一会话，不要重派整份任务书（见 [job-advanced.md](job-advanced.md)「续跑」）。 |
| `failed: stalled: no output for Ns` | 输出停滞被看门狗杀掉；会自动续投 / 故障转移。确实会长时间无输出的活加 `--stall-timeout` 或 `--no-stall`。 |
| `budget exceeded: …` | 超出预算上限；调大 `--max-*` 或拆小任务。 |
| `worker … is being upgraded` | 该 worker 正在远程升级（排空中），稍后再提交或换 runner。 |
| `gofer project list` 报 `worker.yaml: no such file` | 纯客户端节点却设了 `GOFER_RUN_MODE=worker`；改为 `client`（见 [client-config.md](client-config.md)）。 |
| `run mode is client: …` | client 模式下执行了 `serve` / `worker` / `project add` 等本机命令；到 server / worker 那台机器上执行。 |
| 送话失败 `no_runner` / `no_tmux` / `pane_busy` | 见 [sessions-relay.md](sessions-relay.md)「送话」。 |
| agent 改出的文件混进制表符 / 空字符，Markdown 行内代码或注释被吃掉 | Windows 主机上 agent 用 PowerShell 双引号字符串写文件，反引号被当成转义符。任务书要求用 apply_patch，提交前 `git grep -nP "[\x00-\x08\x0b\x0c\x0e-\x1f]"` 自查。 |
| 容器里建的 worktree 在主机上 git 报错 | worktree 的 `.git` 文件指向容器路径。要在主机跑依赖 git 的 job，就在主机建 worktree 或在主 checkout 里跑。 |
| 子 agent 在 worktree 里 `issue brief <新 id>` 找不到 | worktree 里的 tracker 是建分支时的副本；先提交 tracker 再开 worktree。 |
| 看不到 ndjson agent 的输出 | stdout 只留最终答复、过程事件在 stderr；开 agent 的 `ndjson_raw: true` 重跑对照。 |
| 偶发失败 | 先单独重跑确认；连续出现或「单跑也挂」的不要当偶发，查根因。 |

## project 不在 worker 上

`gofer worker projects <id>` 看该 worker **实际生效**的 project 清单，再分模式：

- **LEGACY**（worker.yaml 有 `projects:`、没有 `roots:`）：该 project 要在 worker.yaml 里也定义（`allowed_runners: [local]`）。
- **POLICY**（有 `roots:`）：project 来自 server，检查 ① server 侧该 project 的 `allowed_runners` 是否含这台 worker 的 runner 名；② worker 的 `roots` 是否覆盖该 project 的 `host_path`（不覆盖 → `path_outside_roots` 被拒）；③ worker 是否已应用策略（重连窗口内会短暂 `policy_pending`）。
- 自检：worker 机器上 `gofer config validate worker` + `gofer project list`（列映射后的本机路径）；`gofer worker doctor` 还查连通性、agent 是否真装了、注册握手。
- worker 连不上 / Web 看不到：`worker_id` 三处没对齐（见 [worker-config.md](worker-config.md)「worker_id 三处对齐」）。

## 日志在哪

- server：`<config-dir>/run/serve.log`；worker：`<config-dir>/run/worker-<id>.log`（JSONL，轮转、敏感键脱敏）。用 `-c` 指定配置启动时，`run/` 跟随该配置所在目录。
- 后台运行（`-d`）另有 `.out.log`（如 `run/worker-<id>.out.log`），只承接 panic 和非结构化输出；worker 远程升级后新进程继续追加它。排障时与结构化 `.log` 分开看。
- 受管服务：`gofer serve logs [--lines N] [--follow]`（Linux `--journal` 读 systemd 日志）。
- hook：`<config-dir>/run/hook.log`（中继、记忆注入命中都记在这里）。
- worker 日志按 `event` 字段筛：`worker.registered` / `worker.reconnecting` / `worker.policy_applied` / `worker.job_started|finished|rejected` / `worker.job_recovering|resumed` / `tunnel.*`。
- Windows 上 `-d` 后台进程启动的子进程不会弹出 cmd 窗口。

## 隧道慢 / 不通

- `gofer tunnel check -w <worker> [udp/]host:port` 先验 worker 能否建到目标的连接（UDP 通不代表设备会应答）；目标必须在 worker 的 `tunnel.allow` 白名单内。
- forward 日志默认 `<config-dir>/run/tunnels/forward-<时间>-<pid>.log`（`--log-file` / `--log-dir` 改，`--quiet` 只静默终端）。一次隧道在 forward、server、worker 三端共用同一个 `tunnel_id`，按它搜三份日志即可重建全链路。
- 判断慢在哪：看 `first_byte_ms`、`bytes_up|down`、`packets_up|down`、`close_reason`；`duration_ms / packets_up` 接近 ping RTT 说明是协议逐包往返。`GOFER_TUNNEL_TRACE=1` 逐报文记录。
- 详见 <https://github.com/inhere/gofer/blob/main/docs/runbook/tcp-tunnel.md>。
