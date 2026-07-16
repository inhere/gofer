# worker.yaml 配置参考（含 roots 全场景）

> 配一台 worker 节点。字段跟操作系统无关。全部 `<占位符>`（`D:/work/x`、`/host/projects/x`、`builder-1` 等），按实际替换。
> 校验：`gofer config validate worker`（按模式给判据 + 校验 roots）。脚手架：`gofer init worker`。

## 1. 结构总览

```yaml
worker_id: builder-1                      # ★ 必须与 server 对齐(见 §5)
server_link:
  urls: [ws://<server-host>:8765/v1/workers/connect]   # 连哪个 server(容器常用 host.docker.internal)
  token_env: GOFER_WORKER_TOKEN           # token 从 env 读, 不写进文件
labels: [linux, gpu]                      # 展示/调度提示
max_concurrent: 4                         # 本机同时在跑上限

# —— 二选一决定模式(见 §2) ——
roots: [...]                              # 有 roots ⇒ POLICY(server 下发 project)
projects: {...}                          # 无 roots + 有 projects ⇒ LEGACY(本机定义)

guards: {...}                             # 本地能力收紧(可选, 见 §4)
agents: {...}                            # 本机 agent 定义(cli-agent 命令, 见 §6)
storage: {...}                           # 结果落盘(可选)
```

## 2. 模式：LEGACY vs POLICY

| 模式 | worker.yaml | project 来源 |
|---|---|---|
| **LEGACY** | 有 `projects:`、**无** `roots:` | worker **本机这份文件** |
| **POLICY** | 有 `roots:` | **server 下发**（本机 `projects:` 被忽略，有则告警） |
| EMPTY | 两者都无 | 无（doctor 判 FAIL） |

🔴 **全有或全无**：POLICY 下 project 集合**完全**由 server 下发（完整快照替换，**不与本地 projects 合并**）。一台 worker 要么整台 LEGACY、要么整台 POLICY。想让某 project 只在某台跑，用 §3 场景 E（server 侧 pin），不是在 worker.yaml 保留它。

## 3. roots（POLICY 核心）

`roots` = 把 **server 下发的逻辑路径** 映射到 **本机真实路径** 的前缀规则表。

- `from` = **server 眼里的路径**（server config 里该 project 的 `host_path` 前缀）。
- `to`   = **这台机器上的真实路径**。
- project cwd = `MapRoot(host_path)` = 用**最长命中**的 root 把 `from` 前缀换成 `to`。

```
host_path = D:/work/x/my-service   +   root {from: D:/work/x, to: /host/projects/x}
⟶ cwd = /host/projects/x/my-service
```

### 配置场景速查（OS 无关）

| 场景 | 何时 | roots |
|---|---|---|
| **A 恒等** | worker 与 server 同机/同布局(如 Windows host worker) | `{from: D:/work/x, to: D:/work/x}` |
| **B 跨盘/跨路径** | 项目在别的盘/目录 | `{from: D:/work/x, to: E:/projects}` |
| **C Linux/容器** | server 逻辑是 Windows 风格、worker 是 Linux | `{from: D:/work/x, to: /host/projects/x}` |
| **D 末段不一致** | project 末段名与本机不同(H3) | 通配根 + 更长 from 例外(见下) |
| **E worker 独有** | 某 project 只此机跑 | server 侧 `allowed_runners: [本机runner]` + 本机对应 root |
| **F 纯本地** | project 不想上 server | 别加 roots, 保持 LEGACY |

**场景 D（末段不一致，更具体 root 覆盖）**：
```yaml
roots:
  - { from: D:/work/x,        to: /host/projects/x }        # 通配根
  - { from: D:/work/x/proj-a, to: /host/projects/proj-b }   # 例外: from 更长 → 命中它(子路径一起映对)
```

**场景 E（worker 独有）**：server 定义 project + `allowed_runners: [w-a]` 只推给 w-a；w-a 的 worker.yaml 加覆盖其 `host_path` 的 root（逻辑路径可直接用本机真实路径 → 恒等）。

> ⚠️ 恒等（`from==to`）**也必须写**——roots 是进 POLICY 的开关，且 host_path 要过 MapRoot 校验。

### 映射规则（MapRoot）

1. **最长 `from` 优先**（场景 D 靠它）。
2. **边界对齐**：`/a/b` 不匹配 `/a/bc`。
3. **归一化**：`\`→`/`、去尾斜杠；Windows 盘符大小写不敏感，Linux 敏感。
4. **containment**：`..` / symlink 逃出 `to` 一律拒。
5. **映射不到 = 拒**：`Applied.Rejected{path_outside_roots}`，该 project 不进配置（绝不落进程 CWD）。

### roots 是「能力」、只在本机配

加 root = 扩大该机可执行范围，**故意**要求机器访问权、**不能远程改**。worker=能力提供方（roots/guards）；server=策略权威（谁派给谁、允许哪些 agent）。所以加 project 到已有 root 下 = server 一行 + reload，worker 零改动。

## 4. guards（本地收紧，只减不增）

```yaml
guards:
  allow_exec: true          # false = 本机拒所有 exec job
  allow_interactive: true   # false = 本机拒所有 pty/交互 job
```

- 缺省（字段未写）= **不额外收紧**（与升级前一致）；迁移时**建议显式声明**（doctor 未设会 WARN）。
- server 说 `allow_exec:false`，guards 写 `true` 也没用——server 说了不准就不准。

## 5. 🔴 worker_id 三处对齐（否则连不上/看不到）

一个 worker 真正连上并可派发，**同一个 `worker_id`** 必须三处一致：

1. **server** 的 `server.workers` 段的 **KEY**（并配 token）。
2. **worker** 的 `worker_id`（本文件）。
3. **server** 的 `runners.<name>.worker_id`（给名册/派发用）。

且 worker 端 `server_link.token`（token_env 解析出的）必须 = server 侧该 worker 绑定的 token。
- 缺 `server.workers` 段 → 注册被拒（日志 `worker_id not bound to this token`）。
- 缺 `runners` 段 → 连上了但 `/v1/runners`（Web）看不到、不能被 `runner: <name>` 派发。

## 6. agents（本机 agent 定义 = 逃生舱）

POLICY 下白名单（允许哪些 agent）由 server 下发，但 **agent 怎么执行**由本机 `agents:` 定义（+ 内置模板 detect）：

```yaml
agents:
  claude:
    type: cli-agent
    command: claude
    args: ["-p", "--output-format", "stream-json", "--verbose", "{{prompt}}"]  # {{prompt}}/{{cwd}}/{{job_id}}/{{result_dir}}
  tty-claude:
    type: cli-agent
    command: claude
    interactive: true        # pty 交互(浏览器 attach)
    no_raw_cmd: true         # 命令固定不可被请求覆盖(pty admission 硬要求)
  # exec 是内置, 无需定义
```

- server 白名单里有、但本机没定义/没装的 agent（如容器没 codex）→ 提交时报 `unknown agent`（非回归：本来也用不了）。

## 7. projects（仅 LEGACY 用）

LEGACY 模式的本机 project 定义（POLICY 下被忽略）：

```yaml
projects:
  my-project:
    host_path: /host/projects/my-project   # 本机执行目录(worker 视角)
    allowed_agents: [exec, claude]
    interactive_allowed_agents: [tty-claude]  # pty 白名单(worker 第二道 validate)
    allowed_runners: [local]                  # worker 内部用 local 真执行
    allow_exec: true
    default_agent: exec
```

## 8. storage（可选）

```yaml
storage:
  default_exchange_subdir: tmp      # 交换目录(默认 tmp)
  default_result_subdir: gofer      # 结果子目录(默认 gofer, 落项目 <cwd>/tmp/gofer)
  # root: <某挂载路径>              # 设了则结果落 <root>/<project>/<job>, 容器只能经 HTTP 取回
```

## 9. 常见坑

| 坑 | 现象 | 处理 |
|---|---|---|
| 忘写 identity root | 以为进了 POLICY，其实 roots 空 → 仍 LEGACY | 恒等也要写 `{from:X,to:X}` |
| `from`/`to` 写反 | project 全 Rejected / 映到怪路径 | `from`=server 逻辑、`to`=本机 |
| 独有 project 想混本地 | 加了 roots 又想留某本地 project | 不行(全有或全无) → 场景 E 上 server |
| worker_id 没三处对齐 | 注册被拒 / Web 看不到 | 见 §5 |
| Linux worker 的 host_path 误写 Windows 路径 | LEGACY 遗留，job 落进程 CWD | 迁到 roots 顺带修 |

---

- 迁移（LEGACY→POLICY，含回滚 + 路径核对表）见 `setup-recipes.md` 的迁移配方 + 命令 `commands.md`。
- server 侧怎么配 project/runner 让它能派到本 worker 见 `server-config.md`。
