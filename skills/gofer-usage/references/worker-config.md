# worker.yaml 配置参考（含 roots 全场景）

> 配一台 worker 节点。字段跟操作系统无关。全部 `<占位符>`（`D:/work/x`、`/host/projects/x`、`builder-1` 等），按实际替换。
> 脚手架：`gofer worker init`（一步接入向导）或 `gofer init worker`。校验：`gofer config validate worker`（按模式给判据 + 校验 roots）；`gofer worker doctor`（再查连通、agent、注册握手）。
> 运维命令（show / projects / reload / upgrade）见 [operations.md](operations.md)。热生效：agents / roots / guards / labels / max_concurrent / 隧道白名单；`worker_id`、`server_link`、storage、`xfer_timeout_sec` 要重启。部分能力（ACP 持续会话、web 转达的常驻传话进程、读取会话记录、在线送话走 stdin、模型 / 预算 / 从旧会话分叉）需要较新的 worker；旧 worker 会被明确拒绝或降级，`gofer worker show <id>` 可看版本。

## 目录

- [结构总览](#结构总览)
- [模式：LEGACY vs POLICY](#模式legacy-vs-policy)
- [roots（POLICY 核心）](#rootspolicy-核心)
- [guards（本地收紧）](#guards本地收紧)
- [worker_id 三处对齐](#worker_id-三处对齐)
- [agents（本机 agent 定义）](#agents本机-agent-定义)
- [projects（仅 LEGACY）](#projects仅-legacy)
- [log / storage（可选）](#log--storage可选)
- [容器 worker](#容器-worker)
- [常见坑](#常见坑)

## 结构总览

```yaml
worker_id: builder-1                      # ★ 必须与 server 对齐(见「worker_id 三处对齐」)
server_link:
  urls: [ws://<server-host>:8765/v1/workers/connect]   # 连哪个 server(容器里写宿主 IP)
  token_env: GOFER_WORKER_TOKEN           # token 从 env 读, 不写进文件
labels: [linux, gpu]                      # 展示 / 按标签选 worker
max_concurrent: 4                         # 本机同时在跑上限
# xfer_timeout_sec: 600                   # 文件传输单次超时

# —— 二选一决定模式 ——
roots: [...]                              # 有 roots ⇒ POLICY(server 下发 project)
projects: {...}                           # 无 roots + 有 projects ⇒ LEGACY(本机定义)

guards: {...}                             # 本地能力收紧(可选)
agents: {...}                             # 本机 agent 定义
tunnel: { allow: [...] }                  # 隧道目标白名单
storage: {...}                            # 结果落盘(可选)
```

## 模式：LEGACY vs POLICY

| 模式 | worker.yaml | project 来源 |
|---|---|---|
| **LEGACY** | 有 `projects:`、**无** `roots:` | worker **本机这份文件** |
| **POLICY** | 有 `roots:` | **server 下发**（本机 `projects:` 被忽略，有则告警） |
| EMPTY | 两者都无 | 无（doctor 判 FAIL） |

🔴 **全有或全无**：POLICY 下 project 集合**完全**由 server 下发（完整替换，**不与本地 projects 合并**）。一台 worker 要么整台 LEGACY、要么整台 POLICY。想让某 project 只在某台跑，用下面的场景 E（server 侧指定），不是在 worker.yaml 保留它。

## roots（POLICY 核心）

`roots` = 把 **server 下发的逻辑路径** 映射到 **本机真实路径** 的前缀规则表。

- `from` = **server 眼里的路径**（server config 里该 project 的 `host_path` 前缀）。
- `to` = **这台机器上的真实路径**。
- project 的本机目录 = 用**最长命中**的 root 把 `from` 前缀换成 `to`。

```text
host_path = D:/work/x/my-service   +   root {from: D:/work/x, to: /host/projects/x}
⟶ 本机目录 = /host/projects/x/my-service
```

### 配置场景速查

| 场景 | 何时 | roots |
|---|---|---|
| **A 恒等** | worker 与 server 同机 / 同布局 | `{from: D:/work/x, to: D:/work/x}` |
| **B 跨盘 / 跨路径** | 项目在别的盘 / 目录 | `{from: D:/work/x, to: E:/projects}` |
| **C Linux / 容器** | server 逻辑是 Windows 风格、worker 是 Linux | `{from: D:/work/x, to: /host/projects/x}` |
| **D 末段不一致** | project 末段名与本机不同 | 通配根 + 更长 `from` 的例外（见下） |
| **E worker 独有** | 某 project 只此机跑 | server 侧 `allowed_runners: [本机 runner]` + 本机对应 root |
| **F 纯本地** | project 不想上 server | 别加 roots，保持 LEGACY |

**场景 D（更具体的 root 覆盖）**：

```yaml
roots:
  - { from: D:/work/x,        to: /host/projects/x }        # 通配根
  - { from: D:/work/x/proj-a, to: /host/projects/proj-b }   # 例外: from 更长 → 命中它(子路径一起映射)
```

**场景 E（worker 独有）**：server 定义 project + `allowed_runners: [w-a]` 只推给 w-a；w-a 的 worker.yaml 加覆盖其 `host_path` 的 root（逻辑路径可直接用本机真实路径 → 恒等）。

> ⚠️ 恒等（`from == to`）**也必须写**——roots 是进 POLICY 的开关，且 host_path 要过映射校验。

### 映射规则

1. **最长 `from` 优先**（场景 D 靠它）。
2. **边界对齐**：`/a/b` 不匹配 `/a/bc`。
3. **归一化**：`\` → `/`、去尾斜杠；Windows 盘符大小写不敏感，Linux 敏感。
4. **不许逃出**：`..` / symlink 逃出 `to` 一律拒。
5. **映射不到 = 拒**：该 project 被拒（`path_outside_roots`），不进配置（绝不落到进程当前目录）。

### roots 是「能力」、只在本机配

加 root = 扩大该机可执行范围，**故意**要求机器访问权、不能远程改。worker = 能力提供方（roots / guards）；server = 策略权威（谁派给谁、允许哪些 agent）。所以把 project 加到已有 root 下 = server 改一行 + 重载，worker 零改动。

## guards（本地收紧）

```yaml
guards:
  allow_exec: true          # false = 本机拒所有 exec job
  allow_interactive: true   # false = 本机拒所有 pty / 交互 job
```

- 缺省（字段未写）= 不额外收紧；迁移时建议显式声明（doctor 未设会 WARN）。
- 只减不增：server 说 `allow_exec: false`，guards 写 `true` 也没用。

## worker_id 三处对齐

一个 worker 真正连上并可派发，**同一个 `worker_id`** 必须三处一致：

1. **server** 的 `server.workers` 段的 **KEY**（并配 token）。
2. **worker** 的 `worker_id`（本文件）。
3. **server** 的 `runners.<name>.worker_id`（名册 / 派发用）。

且 worker 端 token（`token_env` 解析出的）必须 = server 侧该 worker 绑定的 token。

- 缺 `server.workers` 段 → 注册被拒（日志 `worker_id not bound to this token`）。
- 缺 `runners` 段 → 连上了但 web Runners 看不到、不能被 `--runner <name>` 派发。
- `server` / `local` 是保留名，不能用作 `worker_id`。

## agents（本机 agent 定义）

POLICY 下允许哪些 agent 由 server 下发，但 **agent 怎么执行**由本机 `agents:` 定义（加上内置模板的探测）：

```yaml
agents:
  claude:
    type: cli-agent
    command: claude
    args: ["-p", "--output-format", "stream-json", "--verbose", "{{prompt}}"]  # {{prompt}}/{{cwd}}/{{job_id}}/{{result_dir}}
    output_format: ndjson    # 按事件流读：stdout=最终答复、stderr=过程事件，job 才有用量、才能设预算；内置 claude 模板已默认如此
    interactive_args: []     # 同一个 claude 也能 pty 交互(job run -a claude --interactive，空 = 裸 TUI)
  # exec 是内置, 无需定义
```

- server 白名单里有、但本机没定义 / 没装的 agent（如容器没装 codex）→ 提交时报 `unknown agent`。
- 字段与 server 端 agent 定义相同，见 [server-config.md](server-config.md)「agents」。

## projects（仅 LEGACY）

LEGACY 模式的本机 project 定义（POLICY 下被忽略）：

```yaml
projects:
  app-a:
    host_path: /host/projects/app-a   # 本机执行目录(worker 视角)
    allowed_agents: [exec, claude]
    allow_interactive: true            # pty / 交互 job 的项目开关
    allowed_runners: [local]           # worker 内部用 local 真执行
    allow_exec: true
    default_agent: exec
```

## log / storage（可选）

- 日志：默认 `<config-dir>/run/worker-<worker_id>.log`（JSONL，按 `max_size_mb` / `max_age_days` / `max_backups` 轮转，敏感键脱敏）；`log.file` / `log.dir` 显式指定时打不开即启动失败。`-d` 后台模式另有 `run/worker-<id>.out.log` 承接 panic 等非结构化输出。排障时按 `event` 字段筛，见 [troubleshooting.md](troubleshooting.md)「日志在哪」。

```yaml
storage:
  default_exchange_subdir: tmp      # 交换目录(默认 tmp)
  default_result_subdir: gofer      # 结果子目录(默认 gofer, 落项目 <cwd>/tmp/gofer)
  # root: <某挂载路径>              # 设了则结果落 <root>/<project>/<job>, 容器只能经 HTTP 取回
```

## 容器 worker

容器里跑 worker（Linux 复核、让容器里的会话能被 web 送话）时有三处与主机不同：

```yaml
server_link:
  urls: [ws://<server-ip>:8765/v1/workers/connect]   # ★ 宿主 IP；Linux 容器可能解析不了 host.docker.internal
roots:
  - { from: D:/work/x, to: /d/work/x }               # bind mount 的整段前缀映射；to 必须存在且可读
```

- `.env`（与 worker.yaml 同目录）：`GOFER_WORKER_TOKEN`（worker 连 server）、`GOFER_SERVER_ADDR`（容器里 CLI / hook 连 server）、`GOFER_HOOK_RUNNER=<worker_id>`（会话登记到本 worker，web 才能送话到容器里的 tmux pane；缺了报 `no_runner`）。会话要在 `tmux new -A -s claude` 里起才有 pane。
- 自检：`gofer worker doctor`（`--json` / `--timeout 5s` / `--connect=false`）；本机已有该 worker 在跑时会跳过注册探测（探测会顶掉在跑的连接）。
- 完整操作手册见 <https://github.com/inhere/gofer/blob/main/docs/runbook/container-worker.md>。

## 常见坑

| 坑 | 现象 | 处理 |
|---|---|---|
| 忘写恒等 root | 以为进了 POLICY，其实 roots 空 → 仍 LEGACY | 恒等也要写 `{from: X, to: X}` |
| `from` / `to` 写反 | project 全被拒 / 映射到怪路径 | `from` = server 逻辑路径、`to` = 本机 |
| 独有 project 想混本地 | 加了 roots 又想留某本地 project | 不行（全有或全无）→ 场景 E 上 server |
| worker_id 没三处对齐 | 注册被拒 / web 看不到 | 见「worker_id 三处对齐」 |
| Linux worker 的 host_path 写成 Windows 路径 | LEGACY 遗留，job 落到错误目录 | 迁到 roots 顺带修 |

---

- LEGACY → POLICY 迁移（含回滚 + 路径核对表）见 [setup-recipes.md](setup-recipes.md)「配方 3」。
- server 侧怎么配 project / runner 让它能派到本 worker 见 [server-config.md](server-config.md)。
