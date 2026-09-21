# Runbook · 容器 worker 上线（CFG-09）

> 设计：[`../design/2026-09-22-plan-autopilot-board-and-container-worker-design.md`](../design/2026-09-22-plan-autopilot-board-and-container-worker-design.md) §三（CFG-09）。
> 目标：容器里跑一个 gofer worker（本文示例 id `w-docker-claude`），让 **Linux 复核**成为一条 plan 链上的一环，并让 **web 能送话到容器里的会话**。
> 前置：[`2026-07-15-worker-policy-migration.md`](2026-07-15-worker-policy-migration.md)（roots/POLICY 模式），[`session-relay.md`](session-relay.md)（会话中继/送话）。

## 1. 为什么要在容器里跑 worker

| 需求 | 没有容器 worker 时 | 有了之后 |
|---|---|---|
| Linux 复核（`go build ./... && go vet ./... && go test ./...`） | 只能人手工在容器里跑 | 变成 plan 链末的一个 `exec` todo，`--runner w-docker-claude` 自动跑 |
| web 送话到容器会话（tmux 路径 A） | `no_runner`：会话登记不到执行机，web 无法在容器里 `tmux send-keys` | 会话登记 `runner=w-docker-claude`，web 会话页「送入终端」直达容器 pane |
| 容器内的交互/长任务 | 只能在容器终端里敲 | `gofer job run --runner w-docker-claude` 提交、看板观看 |

它不是给容器当通用执行机用的：容器只装 `claude` / `python3`，所以 `agents:` 只声明这几个；server 派 `omp`/`codex` 过来会被 caps 拒（**这是对的**，别为了"看起来全能"去声明没装的 CLI——[doctor](#3-启动--停止--自检) 会判 FAIL）。

## 2. 一次性配置

### 2.1 worker.yaml（关键段）

```yaml
worker_id: w-docker-claude                     # ★ 必须与 server.workers 的 key 一致(见 §2.3)
server_link:
  urls: [ws://192.168.65.254:8767/v1/workers/connect]   # ★ 宿主 IP，不是 host.docker.internal(见 §6)
  token_env: GOFER_WORKER_TOKEN                # token 从 .env / env 读，不写进文件
labels: [claude, container]
max_concurrent: 4
roots:                                          # 有 roots ⇒ POLICY(project 由 server 下发)
  - { from: D:/work/inhere, to: /d/work/inhere }
guards:
  allow_exec: true                              # 显式声明姿态(别靠缺省，doctor 未设会 WARN)
  allow_interactive: true
agents:                                         # 只声明容器里真有的
  claude: { type: cli-agent, command: claude, args: ["-p", "--output-format", "stream-json", "--verbose", "{{prompt}}"] }
  tty-claude: { type: cli-agent, command: claude, interactive: true, no_raw_cmd: true }
  # exec 是内置 agent，无需声明
```

三个要点：

- **`server_link.urls` 写宿主 IP**。Docker Desktop 在 **Linux 容器里解析不了 `host.docker.internal`**（`LookupHost` 直接 no such host）；Windows 侧 serve 监听 `0.0.0.0:8767` 时容器用 `192.168.65.254`（Docker Desktop 的宿主网关地址）能连上。换机器/换 Docker 版本后地址会变，用 `gofer worker doctor` 验。
- **roots 是"宿主机看到的路径 → 容器里的路径"**。容器里项目落在 bind mount 上，所以通常整段前缀映射（`D:/work/inhere → /d/work/inhere`）。`to` 目录必须存在且可读。
- **guards 显式写**。缺省 = 不额外收紧（exec/交互全放行），迁移后建议把姿态写在文件里。

### 2.2 .env（`<config-dir>/.env`，与 worker.yaml 同目录）

```bash
GOFER_WORKER_TOKEN=<server.workers.w-docker-claude.token>   # worker 连 hub 的凭据
GOFER_SERVER_ADDR=http://192.168.65.254:8767                # 容器里 CLI 提交 job / gofer hook 连 hub
GOFER_HOOK_RUNNER=w-docker-claude                           # 会话登记到本 worker(送话路径 A 的前提)
```

- `.env` 由 `gofer` 每次启动时加载（`<config-dir>/.env`，导出的 OS env 优先），所以 `gofer worker` / `gofer job` / `gofer hook` 三个进程都吃这份文件。
- `GOFER_RUN_MODE=client` 的节点上 `GOFER_HOOK_RUNNER` **必须**写：否则 `resolveHookRunner` 返回空，server 拒绝向该会话送话（`no_runner`）——见 [`session-relay.md`](session-relay.md) §2 的失败原因表。
- token 不进 git（`.gitignore` 忽略 `.env`）。

### 2.3 server 侧（三处对齐）

```yaml
server:
  workers:
    w-docker-claude: { token: "<上面的 GOFER_WORKER_TOKEN>" }   # ① key = worker_id；token 与容器一致
runners:
  w-docker-claude: { type: worker, worker_id: w-docker-claude } # ② 具名 runner，可显式 --runner 派发
projects:
  hyy-ai-inspect: { allowed_runners: [server, w-docker-claude] } # ③ 项目要允许这个 runner
```

改完在 server 侧 `gofer worker list`（或 Web 的 runners 面板）确认能看到 `w-docker-claude`。缺 ①→ 注册被拒 `worker_id not bound to this token`；缺 ②→ 连上了但不能 `--runner w-docker-claude`；缺 ③→ 提交报 `runner ... is not allowed in project`。

## 3. 启动 / 停止 / 自检

```bash
# 启动（后台；pid/log 都在 <config-dir>/run/）
gofer worker -d                       # 或 gofer worker -d --worker-config <config-dir>/worker.yaml
gofer worker doctor                   # 起飞前自检，见下
gofer worker stop                     # 停（单个 worker 时 id 可省）
gofer worker stop w-docker-claude     # 有多个 worker 时显式给 id
```

- 日志：`<config-dir>/run/worker-<id>.log`（JSONL，按 `event` 字段筛 `worker.registered` / `worker.reconnecting` / `worker.job_started|finished`），另有 `worker-<id>.out.log` 承接 panic 等非 slog 输出。
- pid：`<config-dir>/run/worker-<id>.pid`。
- 启动后在 **server 侧**核对（doctor 只证明本机能连上，真正连没连以 hub 为准）：

  ```bash
  gofer worker list        # → w-docker-claude  status=connected  labels=claude,container
  ```

  仍显示 `disconnected` 时：worker 日志的 `worker.registered` / `rejected` 行 + 本文件 §6。

### `gofer worker doctor`（CFG-09）

```bash
gofer worker doctor                                   # 表格；任一 FAIL → 退出码 1，只 WARN → 0
gofer worker doctor --json                            # 机器可读（一条 JSON 文档在 stdout）
gofer worker doctor --connect=false                   # 跳过注册握手（只做 TCP 可达）
gofer worker doctor --timeout 5s                      # 每步超时（host 解析 / TCP / 注册）
gofer worker doctor --worker-config <path>            # 默认 <config-dir>/worker.yaml
```

检查项（每行 `PASS|WARN|FAIL`）：

| check | 内容 |
|---|---|
| `config` / `worker_id` | worker.yaml 可读、`worker_id` 非空 |
| `url` | scheme 是 ws/wss（http(s) 给 WARN）、主机能解析、TCP 可达（解析失败时提示容器内改用宿主 IP） |
| `token` | `token_env` 指向的变量非空（**值不打印**）；只写了明文 `token` 时同样 PASS，但会提示改用 `token_env`（密钥别入库） |
| `mode` / `roots[i]` | POLICY（roots 数 + 当前生效 project 数，读自 policy 缓存）/ LEGACY（提示迁移）/ EMPTY（FAIL）；每条 root 的 `to` 存在且可读、`from` 不重复 |
| `guards` / `max_concurrent` | 姿态摘要；guards 未显式声明给 WARN，`max_concurrent` 未设给 WARN |
| `agent.<k>` | 每个声明/内置 agent 的 detect 结果（available / version / error）；声明了却没装 = FAIL |
| `connect` | 用 token 向 hub 走一次**真实注册握手**，报 `accepted=true protocol=N`；被拒时原样打印服务端原因 |

**安全联锁（重要）**：注册同一个 `worker_id` 会**顶掉** hub 上它在跑的连接（`registry.Put` + gracefulClose 会失败那条连接上的 in-flight job）。所以 doctor 发现**本机**有该 worker_id 的 `worker -d` 在跑时，会跳过注册探测并给一行 WARN（附 pid 与日志路径）；要真试连就先 `gofer worker stop`。跨机场景（在 A 机诊断 B 机的 worker）本机探测不到，**请到 worker 所在的那台机器上跑 doctor**。

## 4. 会话侧：让容器会话能被 web 送话

1. 会话必须在 **tmux** 里起（`TMUX_PANE` 才会随 SessionStart 登记，送话路径 A 靠它定位 pane）：

   ```bash
   tmux new -A -s claude        # 容器里起 Claude Code 的常规入口
   claude                        # 在这个 tmux 会话里跑
   ```

2. `.env` 里 `GOFER_HOOK_RUNNER=w-docker-claude`（§2.2）。
3. hooks 装好：`gofer init hooks`（Claude Code 写 `./.claude/settings.json`，`--global` 写 `~/.claude`）。hook 命令形如 `gofer hook claude --wait 7140`，**不带** runner —— runner 由 hook 进程读 `.env` 的 `GOFER_HOOK_RUNNER` 决定，所以改完 `.env` 后**新起的会话**才登记新 runner（已在跑的会话登记的是旧值）。

送话：web 会话页 → 抽屉输入框（没有 OPEN turn 时是「送入终端」）→ server 在 `runner=w-docker-claude` 上起内部 exec job，确认 pane 存活 + 前台是 agent CLI 后 `tmux send-keys`。CLI 等价面：

```bash
gofer session say <sid> "<文本>" --deliver          # 有 OPEN turn = 作答；没有 = 送进终端(路径 A)
gofer session say <sid> "<文本>" --deliver --takeover   # pane 没了时改走 --resume 接管(路径 B)
```

## 5. 验收清单

```bash
# ① exec job 真在容器里跑（Linux 复核的最小闭环）
gofer job run -p hyy-ai-inspect -a exec --runner w-docker-claude --cwd tools/gofer -- go version

# ② web 会话页对容器会话「送入终端」成功（tmux 路径）→ 会话页看到新消息、pane 里出现 `[gofer web 回复] …`
#    （失败原因看 session-relay.md 的表：no_runner / no_tmux / pane_busy …）

# ③ 链末 Linux 复核 todo：挂在计划链最后一项，前一项 done 后自动派发到容器
gofer plan add-todo <plan> "Linux 复核" --after prev --assign exec --runner w-docker-claude \
  --cwd tools/gofer --cmd 'bash -lc "go build ./... && go vet ./... && go test ./..."'
gofer plan run <plan>             # 根节点要人起；之后由依赖自动推进
```

`--assign exec` 的 todo **必须有 `--cmd`**（argv 语义，与 `gofer job run -- …` 一致），否则派发时报 `exec todo needs --cmd`。

## 6. 常见故障

| 现象 | 根因 | 处理 |
|---|---|---|
| `worker doctor` 的 `url` 行 FAIL：`host "host.docker.internal" 解析失败` | Linux 容器解析不了 Docker Desktop 的宿主名 | `server_link.urls` 改宿主 IP（Docker Desktop 通常 `192.168.65.254`），`gofer worker doctor` 复验 |
| 启动后一直 `worker.reconnecting`，server 无记录 | 端口没通（serve 只监听 `127.0.0.1`）/ 宿主防火墙 | serve 监听 `0.0.0.0:<port>`；容器里 `nc -vz 192.168.65.254 8767` |
| 注册被拒 `worker_id not bound to this token` | `server.workers` 缺该 key，或 token 不一致 | §2.3 三处对齐；改完 server 侧 reload/重启 |
| `401`（upgrade 就被拒） | `GOFER_WORKER_TOKEN` 没导出到 worker 进程 | 确认 `.env` 与 worker 同目录、变量名与 `token_env` 一致（`worker doctor` 的 `token` 行会指出） |
| `worker doctor` 的 `roots[i]` FAIL：`to 目录不存在` | 容器里没挂到那个路径，或路径写成了宿主风格 | bind mount 挂上，或改 `to`（`gofer config validate worker` 同样会报） |
| `connect` FAIL：`worker 协议版本过旧(vN)` | 容器里的 gofer 二进制比 server 老 | 容器内换新二进制（`gofer -V` 与 server 对照） |
| 送话报 `no_runner` | 会话登记时没有 `GOFER_HOOK_RUNNER`（或改完 `.env` 没重开会话） | §4；`gofer session show <sid>` 看 runner 字段 |
| 送话报 `no_tmux` | 会话不在 tmux 里（`TMUX_PANE` 未登记） | `tmux new -A -s claude` 重开；或带 `--takeover` 走路径 B |
| doctor 打印 `WARN connect 本机已有 worker … 在运行` | 设计如此：注册探测会顶掉在跑的连接 | 要试连先 `gofer worker stop`；平时看 worker 日志的 `worker.registered`/`rejected` |

## 7. 参考

- 字段与模式（LEGACY/POLICY、roots 全场景、guards、agents）：skill `gofer-usage` → `references/worker-config.md`。
- 迁移与回滚、路径核对表：[`2026-07-15-worker-policy-migration.md`](2026-07-15-worker-policy-migration.md)。
- 会话中继 / 送话 / 接管：[`session-relay.md`](session-relay.md)。
- 配置自检（JSON schema 级）：`gofer config validate worker`；机器级自检（本文件 §3）：`gofer worker doctor`。
