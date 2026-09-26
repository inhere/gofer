# worker 接入向导（CFG-05）与默认工作空间 / 会话 id 实时落库 Runbook

> 配套 design [`../design/2026-09-25-rules-injection-and-worker-init-design.md`](../design/2026-09-25-rules-injection-and-worker-init-design.md) §二（CFG-05）、§三 F-g / F-e。
> 相关：[容器 worker 接入](./container-worker.md)、[worker 策略迁移](./2026-07-15-worker-policy-migration.md)。

## 一条命令接入：`gofer worker init`

新机器不必再手写 worker.yaml（`worker_id` / `server_link.urls` / token / roots 映射 / agents 五项全靠手抄，抄错就是"连不上"或"跑不了任何 project"）。

```bash
gofer worker init \
  --server http://192.168.65.254:8767 \   # hub 地址(http/https/ws/wss 皆可, http→ws)
  --token  <worker token> \               # 写进 <config-dir>/.env 的 GOFER_WORKER_TOKEN
  --id     w-laptop                       # 必须等于 server.workers 里的 key
```

流程与交互：

```
✓ 连接 server v0.61.0，协议 v11，可派给 w-laptop 的项目 2 个
  hyy-ai-inspect   host_path D:/work/inhere/hyy-ai-inspect
  zy-bsly-sf-dev   host_path D:/work/inhere/zy-bsly-sf-dev
使用显式 --roots 映射 1 条（跳过推断）        # 没给 --roots 时这里是"推断 + 逐条确认"
探测到 agents（已装 2 个）：
  ✓ claude       2.1.278 (Claude Code)
  ✗ codex-acp    未安装
已写入 <config-dir>/worker.yaml、<config-dir>/.env
默认工作空间 <home>/.gofer/workspace 已就绪：把它登记为 server 上的 `default` 项目…
运行 doctor：
… PASS/FAIL 表 …
启动：gofer worker -d
```

- **探测到哪些 agent 就写哪些**：只把本机 PATH 上真的装了的记进 `agents:`（装了才会写，没装的列出来提示）；没有装任何 CLI 也能生成（只剩内置 `exec`），提示里说明。
- **roots 推断**（不给 `--roots` 时）：取 server 侧 project `host_path` 的**最长公共前缀**作 `from`；`to` 依次尝试**同路径** → **盘符互转**（`D:/x` ↔ `/d/x`，容器挂载形态）→ 交互输入。逐条显示 `[✓ 存在]/[✗ 不存在]`，回车接受、`n` 跳过、直接输入即改为该路径。映射后本机不存在的 project 会单独告警（那些 project 派到本机会失败，用 `--roots` 精确指定）。
- `from` 统一写成**正斜杠**形态（`D:/work/x`）——与 server 配置里的写法一致，映射两侧都会做同样的大小写/分隔符归一。
- **非交互**：`--yes` 接受全部推断；显式 `--roots 'from=to'`（可重复）**以显式为准，跳过推断**。
- **不自动启动 worker**：启动方式因平台而异（Windows 的登录自启脚本只覆盖服务端），所以只打印 `gofer worker -d`。
- **覆盖保护**：`<config-dir>/worker.yaml` 已存在时**先报错**（在任何网络请求之前），`--force` 才覆盖，且**覆盖前备份**为 `worker.yaml.bak-<YYYYMMDD-HHMMSS>`。`.env` 是**就地更新**那一行 `GOFER_WORKER_TOKEN`，其它键原样保留（同一份 .env 里可能还有别的凭据）。
- **占用告警**：若该 id 在 server 上**已有在线 worker**，先打一行 warning —— 启动第二份会顶掉它的连接并失败其 in-flight job。
- **收尾跑 doctor**：向导用刚写的文件跑一次完整 doctor（含注册握手），**任一 FAIL 则命令非零退出**并提示重跑 `gofer worker doctor`；token 用本次 `--token` 复核，所以新机器（还没 export）也能验通。
- 两个入口**同一实现**：`gofer init worker --server …` 委托给向导（不带 `--server` 时 `init worker` 仍是"写一份示例模板"的老行为）。

服务端侧的读接口：`GET /v1/workers/{id}/assignable` —— 返回**能派给这个 worker 的 project（key + server 侧 host_path）**与 server/协议版本。判定：project 的 `allowed_runners` 里**直接写了该 worker id**，或者写了一个 `type: worker` 且 `worker_id: <id>` 的 runner 名。鉴权：**该 worker 自己的 token 或 user caller**；别的 worker token → 403。

## 默认工作空间与 `default` 项目回落（F-g）

- `gofer init server` 生成配置时创建 **`~/.gofer/workspace`**（Windows：`%USERPROFILE%\.gofer\workspace`）并登记为项目 **`default`**（`host_path` 指向它、`allowed_agents` 取本机探测到的 agent、`allowed_runners: [local]`）。路径可用 `--workspace <dir>` 或 `GOFER_WORKSPACE` 改。
- **已有 `default` 项目就不覆盖**：目标配置里已经有 `projects.default` 时，沿用**它自己的 host_path**（不新建、不搬家）；目录已存在则原地复用（里面的东西不动）。
- **`job run` 的项目解析顺序**：`-p/--project` > 当前目录匹配到的 project > **`default`** > 报错。回落到 `default` 时在 **stderr** 打一行
  `note: current directory matches no project; using the default project "default" (<path>)`
  （放 stderr 是为了不污染脚本解析的 stdout）。带 `--role` / `--template` 时**不回落**——那两条路径的项目由服务端填，硬塞 `default` 会把它盖掉。
- **worker 侧**：向导也创建 `~/.gofer/workspace`，但 worker 是 POLICY 模式（项目由 server 下发），所以它只建目录并提示"到 server 上把该目录登记为 `default`、`allowed_runners` 含本 worker 的 runner"。server 上登记之后，worker 的 roots 推断会把那个路径一起纳入。

用途：临时、不属于任何仓库的活（"帮我看看这段日志"）有个安全落脚处，不必先建项目。

## 会话 id 什么时候落库（F-e / F11）

一个 job 的 `session_id` 决定了 `gofer job resume` 能不能续接，所以它要在**跑着的时候**就进库（serve 重启会把只存在于内存里的 id 丢掉，行会被 ReconcileOrphanJobs 判 failed 且 resume 报"无 session"）。四条路径，都走同一个窄更新 `jobstore.SetJobSessionID`（只写 `session_id` 一列，**首次写入即胜**，绝不覆盖已有值）：

| 路径 | 触发 | 事件 `job.session_captured` 的 `source` |
|---|---|---|
| 注入（claude 等 `session_inject`） | 提交时就知道，直接带上 | —（不走捕获） |
| ndjson 结构化流 | agent 自己吐的 session 行（`--output-format stream-json` / `--mode json`），过滤器一读到就写 | `ndjson` |
| **文本流（F-e）** | **非 ndjson** 的 cli-agent：stdout/stderr 里出现可匹配的 `session_capture` 文本（codex 的 `session id: <uuid>` 等）时立即写 | `stream` |
| pty 交互 | TUI 输出（去 ANSI 的头/尾窗口），注册握手后的实时会话 | `pty` |
| 终态补扫 | job 结束时扫 `stdout.log` / `stderr.log`（交互 job 再扫 pty transcript），再兜底读 `<result_dir>/session_id` 文件 | `stdout`/`stderr`/`pty`/`file` |
| 重启补扫 | serve 重启后 ReconcileOrphanJobs **翻终态之前**重扫同一批日志，让失败的行仍可 resume | `orphan_scan` |

- 文本流的观察器与 pty 的**同思路**：去掉 ANSI、保留"前 64KB 头窗口 + 滚动 64KB 尾窗口"，且**每次只扫新增字节（加 1KB 重叠防止匹配被写调用切断）**——1MB 输出不会被反复整段重扫；`exec`、acp、ndjson agent 不挂（分别没有 prompt/文本 id、已有结构化捕获），交互 job 也不挂（pty 那条路负责）。
- `by` 字段告诉你这个 id 来自 **agent 自己的 `session_capture`**（`agent_config`）还是 gofer 的**通用兜底正则**（`fallback`）——后者意味着"该给这个 agent 写条 `session_capture` 了"。
- 事件 `job.session_captured {agent, by, source}`，`source` 即上表；`job show` 的 `session:` 行与 `resume` 都用同一个值。

## 排障

- **`worker init` 连不上**：`--server` 要是**本执行机**能访问的地址（容器里写 `host.docker.internal` 解析不了，用宿主 IP，如 `192.168.65.254`）；先用 `gofer worker doctor --worker-config <file>` 看 url/token 两行。
- **`assignable` 返回 0 个 project**：server 上没有任何 project 的 `allowed_runners` 指向这个 worker。给项目加上 worker 的 runner 名（或直接写 worker id），再重跑向导。
- **roots 全"不存在"**：server 侧 `host_path` 与本机盘符/挂载点不一致，用 `--roots 'D:/work/x=/mnt/work/x'` 显式给（`from` 是 server 视角，`to` 是本机路径）。多棵不相关树就多给几条。
- **`job run` 没带 `-p` 却报 `--project/-p is required`**：配置里没有 `default` 项目（`gofer init server` 才会生成），或本次带了 `--role`/`--template`（设计上不回落）。
- **`job show` 里 `session_id` 为空**：看时间线有没有 `job.session_captured`；没有则是该 agent 的 `session_capture` 正则没匹配上——`agent.SessionCapture` 可手写（见 [CLI agent 接入](./2026-09-22-cli-agent-onboarding-runbook.md)）。
