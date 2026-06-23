# gofer

把可配置的 **CLI Agent**（`codex` / `claude` / `opencode` / 任意命令）与已登记的**项目**，桥接为一个统一的**异步 job 控制面**：一端提交 `{项目, agent, prompt/命令, cwd}`，gofer 在该项目的真实工作目录里执行（本机 / 远端 worker / peer 容器），把状态、日志、退出码、结果统一回传，并可经 **CLI / HTTP / MCP / Web 控制台** 四种入口提交与观测。

> gofer = "跑腿取送的人"：派任务给 agent → 在目标项目目录执行 → 回传日志/结果。自带 `gofer`↔`gopher` 的 Go 双关。

## 能力总览

- **多入口控制面**：CLI（`gofer job ...`）/ HTTP（`/v1/jobs`）/ MCP（stdio tools）/ Web 控制台（含提交表单），同一套 `job.Service`。
- **多 agent**：`type: cli-agent`（模板渲染 `--prompt`，如 codex/claude/opencode）与内置 `type: exec`（原样跑 argv）。未安装的 agent 仅标 `unavailable`，不影响启动。
- **多项目**：每个项目登记 `host_path`/`container_path` + 允许的 agent/runner + `allow_exec` + 并发上限。
- **三种执行位置（runner）**：`local`（本进程）/ `peer-http`（转发到另一台 gofer）/ `worker`（WS 远端执行机）；远端的日志/交互经"镜像"机制透明回传，读路径不变。
- **WS 远端 worker + 标签调度**：worker 经 WebSocket 连入 hub，按 `worker_labels` 自动选机（或显式 `worker_id`）；实际执行机记入结果可审计。
- **同步 / 异步提交**：默认异步（立即返 `id`）；`sync` 让服务端等到终态再返回（封顶 30s/60s，超时退回异步）。
- **多种提交格式**：JSON、`-- argv`（exec）、**md+yaml**（frontmatter 定参数 + 正文即 prompt）。
- **运行中交互**：agent 可在执行途中提问 → job 置 `pending_interaction` → 用户作答 → 续跑。
- **可观测 / 可审计**：`/v1/runners` 健康名册、`/v1/jobs` 状态/日志/SSE 实时流、`caller_id`（谁提交）/`worker_id`（在哪执行）入库，retention 周期清理。
- **存储**：job 元数据/索引/交互入 SQLite（纯 Go modernc），日志为结果目录文件；内存只留在飞 job。

## 架构总览

```txt
  提交/观测入口                    控制面 (serve)                 执行位置 (runner)        agent
┌───────────────┐           ┌────────────────────────┐      ┌─ local  (本进程) ─┐
│ CLI  gofer job │──HTTP────▶│  /v1/jobs  job.Service  │─────▶│ peer-http (另一台 gofer) │──▶ cli-agent
│ HTTP /v1/*     │           │  registry: project/agent │      │ worker (WS 远端执行机) │     (codex/claude/…)
│ MCP  stdio     │           │  runners  + ws hub       │      └────────┬───────────┘      或 exec(argv)
│ Web  控制台     │           │  jobstore(SQLite)+logs   │◀────镜像日志/状态/交互────┘
└───────────────┘           └────────────────────────┘
   Authorization: Bearer <token>（/health 除外）         结果: <host_path>/tmp/gofer/<job_id>/ + DB
```

## 安装 / 构建

需要 Go 1.25+（含 Web 控制台时另需 Node + pnpm）。

```bash
make build            # 当前平台 → dist/gofer（默认 upx 压缩；无 upx 用下面的 go build）
go build -o dist/gofer ./cmd/gofer

make web build        # 构建前端并嵌入二进制（= make web 拷 dist 入 internal/webui/dist + make build）
make build-all        # 跨平台交叉编译（linux/darwin/windows × amd64/arm64）
```

## 快速开始

```bash
# 1. 登记项目（host-path 必须是已存在的绝对目录）
gofer project add workspace \
  --host-path /d/work/inhere/hyy-ai-inspect --container-path /workspace \
  --default-agent codex --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner local --allow-exec

# 2. 起服务（token 走 env）
export GOFER_TOKEN=dev-token
gofer serve --addr 0.0.0.0:8765

# 3. 跑一个 exec job（-- 之后为命令 argv）；--sync 让服务端等到终态再返回
gofer job run -p workspace -a exec --sync -- go version
# → status=done exit_code=0

# 4. 跑一个 cli-agent job；查日志
gofer job run -p workspace -a codex --prompt "总结本目录的测试失败用例" --wait
gofer job logs <id> --stream stdout

# 5. 浏览器打开 http://<addr>/ ，粘贴 token 接入，看板/详情/实时日志/新建 job
```

## 核心概念

- **project**：一个可执行任务的真实目录。`host_path`（主机路径）/`container_path`（容器路径）/`allowed_agents`/`allowed_runners`/`allow_exec`/`max_concurrent_jobs`。
- **agent**：怎么执行。`cli-agent` 用 `command`+`args` 模板渲染（占位符 `{{prompt}}`/`{{cwd}}`/`{{job_id}}`/`{{result_dir}}`，逐元素替换、不过 shell）；`exec` 原样跑请求里的 `cmd` argv（需项目 `allow_exec`）。
- **runner**：在哪执行。`local`（内置，本进程子进程）/ `peer-http`（转发到另一台 gofer）/ `worker`（WS 连入的远端执行机）。
- **job 生命周期**：`queued → running → done|failed|cancelled|timeout`；运行中提问时 `running → pending_interaction → running`。

## 提交 job 的几种方式

同一个 `JobRequest`，四种入口、两种时序：

```bash
# CLI —— cli-agent 用 --prompt；exec 用 -- argv
gofer job run -p workspace -a codex --prompt "审查改动并给风险点"
gofer job run -p workspace -a exec  --sync -- mvn -q test     # --sync：服务端等终态

# md+yaml 文件 —— frontmatter 定参数，正文即 prompt（仅 cli-agent）
gofer job run -f task.md
```

```markdown
<!-- task.md -->
---
project_key: workspace
agent: codex
runner: worker
worker_labels: [gpu]      # 或 worker_id: w-01
sync: false
---
在 scripts/ 下生成批量巡检脚本，读取 config.yaml 的门店列表……（正文即 prompt）
```

- **HTTP**：`POST /v1/jobs`（JSON）。加 `"sync": true` 或 `?wait=1` 走同步（命中终态 `200`+完整结果；超服务端上限 `202`+`X-Gofer-Async:1`+id）。md 提交用 `Content-Type: text/markdown`。
- **Web 控制台**：顶栏「+ 新建 job」表单，选 项目/agent/runner（worker 可显式选 id 或填 labels）、勾 sync，提交后跳详情。

## 远端执行（peer-http / ws-worker / 标签调度）

远端 job 的日志、状态、运行中交互都经"镜像"透明回传到本地 job，**看板/详情/日志读路径无需任何改动**。

```yaml
# 方式 A：peer-http —— 转发给另一台 gofer（如容器内的 peer）
runners:
  docker-peer: { type: peer-http, base_url: http://127.0.0.1:8766, token_env: PEER_TOKEN }

# 方式 B：ws-worker —— 远端执行机主动连入本 hub
server:
  workers:                         # 在册 worker（worker_id ↔ token 绑定 + 调度标签）
    w-gpu: { token_env: WTOK_GPU, labels: [gpu, linux] }
    w-cpu: { token_env: WTOK_CPU, labels: [cpu, linux] }
runners:
  worker: { type: worker }         # worker 类型 runner（动态按 worker_id 派发）
```

worker 侧用独立配置连入并本地执行（`gofer worker --config worker.yaml`）：

```yaml
worker_id: w-gpu
server_link: { urls: [ws://hub:8765/v1/workers/connect], token_env: WTOK_GPU }
labels: [gpu, linux]
projects: { workspace: { host_path: /abs, container_path: /abs, allowed_agents: [exec], allow_exec: true } }
```

- **显式路由**：提交 `{"runner":"worker","worker_id":"w-gpu"}`。
- **标签自动调度**：提交 `{"runner":"worker","worker_labels":["gpu"]}` → 在**已连接且标签全包含**的 worker 里按 `in_flight↑ → 心跳新鲜↑` 选一台；无合格候选返 `503`。
- 实际落机的 `worker_id` 记入 `JobResult`，看板 runner 列与详情 meta 均可见。
- worker 多 hub 地址 + 全抖动退避重连（hub 重启=短暂中断而非永久失联）。

### 配置一个 worker（init → 校验 → 启动）

```bash
gofer init worker -c worker.yaml              # 生成 worker 配置模板（含对齐注释）
# 编辑 worker.yaml（见下「三处对齐」）后自查，重点看 token 是否可解析 / host_path 是否存在：
gofer config validate worker -c worker.yaml
gofer worker -c worker.yaml                   # 启动；进程日志直接看连接成败
```

**三处对齐**（同一个 worker_id，缺一则连不上或 Web 看不到）：

| 位置 | 字段 | 必须等于 |
|---|---|---|
| 服务端 `server.workers` | 段的 KEY（鉴权/绑定） | worker 端 `worker_id` |
| 服务端 `runners.<name>` | `worker_id`（名册/派发） | 同一个 worker_id —— **缺这段=连上了但 `/v1/runners`、Web 看不到** |
| worker 端 `server_link` | `token` / `token_env` | `server.workers.<worker_id>` 的同一 token |

> 要在 Web「Runners」看到某台 worker 为 `connected`，必须声明一个**带 `worker_id` 的具名 worker runner**（如 `w-gpu: {type: worker, worker_id: w-gpu}`）；不带 `worker_id` 的通用 `worker` runner 只用于标签动态派发，不对应具体一台。

**派发要点**：
- **提交端**项目 `allowed_runners` 要含那个 **worker-runner 的名字**（如 `w-gpu`，不是字面量 `worker`）才能派给它；**worker 端**项目 `allowed_runners` 含 `local`（worker 收到后用本地 local runner 真正执行）。
- job 带的 `project` key 必须**两边配置都有**（worker 用自己的配置再解析一次同名项目，对不上会以「未知项目」拒掉）。

**连接日志**：worker / serve 在关键点输出结构化日志（`worker registered with hub` / `hub accepted worker` / `registration rejected … reason=…`），出问题一眼定位。`GOFER_LOG_LEVEL=debug|info|warn|error` 调详细度（默认 `info`，写 stderr）。

## 运行中交互

1. 运行中的 agent 经 `POST /v1/jobs/{id}/interactions` 提问 → job 置 `pending_interaction`。
2. 用户 `GET /v1/jobs/{id}/interactions` 看到待答项 → `POST .../answer`（`{"answer":"..."}`）。
3. 无其它待答项时 job 自动回到 `running`，agent 读到答案续跑。MCP 侧对应 `bridge_get_interactions` / `bridge_answer_interaction`。

## 观测与审计

- **执行位置可见**：`GET /v1/runners` 列每个 runner 健康态（local 恒 up；peer-http 周期主动探针 up/down；worker 心跳态 connected/disconnected + 在飞数 + 标签 + 心跳年龄）。Web `/runners` 仪表盘消费它。
- **job 状态/日志**：`GET /v1/jobs`（按 project/status/caller 过滤）、`/v1/jobs/{id}`、`/logs/{stdout,stderr}`（尾部 256KB）、`/stream`（SSE 实时日志+状态+交互）。
- **审计字段**：`caller_id`（谁提交，由 token 解析、服务端覆盖防伪）+ `worker_id`（在哪执行）随 job 入库、随响应回显、可过滤。
- **留存**：`storage.retention` 周期清理超期/超量的终态 job 及其日志目录。

## 配置参考

### 查找链（命中即用）

1. `--config <path>` → 2. 环境变量 `GOFER_CONFIG` → 3. `./.gofer.local.yaml` → `./.gofer.yaml` → 4. `<config-dir>/config.yaml`（默认 `~/.config/gofer/config.yaml`，可用 `GOFER_CFG_DIR` 改）。

完整示例见 [`config/gofer.example.yaml`](config/gofer.example.yaml)。关键段：

```yaml
server:
  addr: 0.0.0.0:8765          # 默认对容器可达；安全靠强制 token + 内网准入
  token_env: GOFER_TOKEN      # bearer token 来源：token_env > 内联 token > --token
  allow_empty_token: false    # 必须显式 true 才能无 token 启动
  # callers:                  # 可选：多调用方身份（每个 token → caller_id，入 job 审计）
  #   - { id: docker, token_env: DOCKER_CALLER_TOKEN }
  # workers:                  # 可选：在册 worker（worker_id↔token 绑定 + 调度标签）
  #   w-gpu: { token_env: WTOK_GPU, labels: [gpu, linux] }
  # web_enabled: true         # 内置 Web 控制台开关（默认开；serve --no-web 关）

storage:
  default_exchange_subdir: tmp
  default_result_subdir: gofer
  # root: /var/lib/gofer       # 可选：集中 store（设后容器侧只能经 HTTP 回读）
  # db_path: ""                # SQLite 元数据库；空则 <root>/gofer.db > <config-dir>/gofer.db
  # retention: { max_age_days: 30, max_count: 5000, prune_interval_minutes: 60 }

projects:
  workspace:
    host_path: /d/work/inhere/hyy-ai-inspect
    container_path: /workspace
    default_agent: codex
    allowed_agents: [codex, claude, exec]
    allowed_runners: [local, worker]     # 含 worker 才能远端派发
    allow_exec: true
    max_concurrent_jobs: 4

agents:                                  # 占位符：{{prompt}} {{cwd}} {{job_id}} {{result_dir}}
  codex:  { type: cli-agent, command: codex, args: [exec, "{{prompt}}"], detect: { command: codex, args: [--version] } }
  claude: { type: cli-agent, command: claude, args: ["-p", "{{prompt}}"], detect: { command: claude, args: [--version] } }
  exec:   { type: exec, detect: { command: sh, args: [-c, "true"] } }

runners:
  local: { type: local }
  # docker-peer: { type: peer-http, base_url: http://127.0.0.1:8766, token_env: PEER_TOKEN }
  # worker: { type: worker }             # WS 远端执行机（标签调度，见上）
```

- **`.env` 自动加载**：启动最早期按序加载 `<config-dir>/.env`（全局）→ `./.env`（当前目录，覆盖全局）；已导出的 OS env 始终最高优先；文件不存在跳过。仅本地便利，**不要提交真实 token**（`.gitignore` 已忽略 `.env`，见 [`.env.example`](.env.example)）。
- **结果目录**：默认 `<host_path>/tmp/gofer/<job_id>/`（设 `storage.root` 则 `<root>/<project_key>/<job_id>/`）。每 job 目录只留 `stdout.log`/`stderr.log`；状态/元数据/交互在 SQLite，经 HTTP 回读。

## CLI 参考

`gofer <command> [sub] [--options]`，均支持 `-c/--config`。

```bash
gofer init    [server|worker] [-c path]    # 生成配置模板（默认 server→.gofer.yaml；worker→worker.yaml）
gofer serve   --addr 0.0.0.0:8765 [--token …] [--allow-empty-token] [--no-web]
gofer worker  --config worker.yaml         # 作为 WS 远端执行机连入 hub
gofer config  validate [server|worker] [-c path]   # 校验配置（默认 server；worker 查 token/host_path 等）
gofer project list | show <k> | add <k> … | remove <k> | validate <k>
gofer agent   list | detect | show <k>     # detect 未装报 unavailable，不退非零
gofer job     run … | list … | show <id> | watch <id> | logs <id> --stream … | rerun <id> | cancel <id>
gofer workflow run <file.yaml> [--watch] | show <id> | list | cancel <id>   # 多步 job 链（上一步产出喂下一步；支持 step 重试/失败策略、并行 fan-out、子工作流嵌套 type=workflow）
gofer mcp     --config <path>              # stdio MCP server
gofer completion bash|zsh                  # 补全脚本
```

`job run` 关键参数：`-p/--project`、`-a/--agent`（必填）、`--runner`（默认 local）、`--cwd`（默认 `.`，限项目内）、`--prompt`（cli-agent）、`-- argv`（exec）、`-f/--file`（md+yaml）、`--sync` + `--wait-timeout`（同步等待）、`--wait`（客户端轮询到终态）、`--worker-id` / `--worker-labels`（worker 路由）、`--tags`、`--timeout`、`--title`、`-s/--server`、`--token`。

> ⚠️ **工作流跨项目 / 跨机传值（`${steps.N.result_dir}`）**：`result_dir` 是**绝对路径**。各 step 可指向不同项目（开发项目产物→测试项目读），只要这些 step 都在**同一文件系统**（本机 / 同容器 local runner）上执行，下一步即可直接读取上一步的 `result_dir`，无需拷贝。**但**当某 step 用 **worker 远端 / peer 跨机**执行时，`result_dir` 在那台机器上，跨机**不可直接读**——此时改用 `${steps.N.result}`（inline result.json，≤32KB）/ `${steps.N.stdout}` 传值，或将产物落到**共享盘**。远端产物自动拉取通道留后续。

> `GOFER_LOG_LEVEL=debug|info|warn|error`（默认 `info`，写 stderr）调结构化日志详细度——worker/serve 的连接生命周期在此输出。

## 推荐部署（单机）

一台机器只起一个 server，项目映射收敛到全局单文件：

```bash
export GOFER_CONFIG=~/.config/gofer/config.yaml   # 写进 shell profile
gofer init server --global                         # 生成全局骨架
# 编辑：填 server.token_env / agents / runners
gofer project add siv --host-path /abs/SIV --container-path /work/SIV
gofer serve                                        # 一个进程
# 任意目录:
gofer job run -p siv -a claude "..."               # CLI 连 serve
```

`GOFER_CONFIG` 优先于当前目录的 `.gofer.yaml`，任意目录命令都走全局。
项目专属偏好放项目目录 `.gofer.project.yaml`（瘦配置，后续阶段落地）。

## MCP 接入

`gofer mcp` 以 **stdio MCP server** 暴露同一套控制面（复用 serve 的 `job.Service`/注册表）。stdout 为协议通道，不输出日志。

```json
{ "mcpServers": { "gofer": { "command": "/abs/path/to/gofer", "args": ["mcp", "--config", "/abs/path/to/config.yaml"] } } }
```

暴露 8 个 tool（字段 snake_case，与 HTTP 对齐）：`bridge_list_projects` / `bridge_list_agents` / `bridge_run_job` / `bridge_get_job` / `bridge_tail_log` / `bridge_cancel_job` / `bridge_get_interactions` / `bridge_answer_interaction`。

## Web 控制台

`serve` 默认内置静态 SPA（挂根路径，页面打开免鉴权，页内 `/v1/*` 仍需 token），资源**嵌入二进制**。`make web build` 构建并烘入；裸 `go build`（未跑 `make web`）显示占位页、不影响 API。开发态 `pnpm -C web dev`（vite 代理 `/v1`）。提供深/浅双主题（跟随系统 + 持久化）。看板/详情/实时日志/Workers 仪表盘/新建 job 表单。关 Web：`serve --no-web` 或 `server.web_enabled: false`。

## HTTP API

基于 `gookit/rux`。`/health` 不鉴权；`/v1/*` 全部要求 `Authorization: Bearer <token>`。错误体 `{"error":"…","detail":"…"}`。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/health` | 进程存活（不鉴权） |
| GET | `/v1/projects` · `/v1/projects/{key}` | 项目列表 / 详情 |
| GET | `/v1/agents` | agent 列表 + 检测状态 |
| GET | `/v1/runners` | 各 runner 健康名册（local/peer-http 探针/worker 心跳） |
| GET | `/v1/meta` | 提交表单选项聚合（projects/agents/runners/workers） |
| POST | `/v1/jobs` | 创建 job（JSON 或 `text/markdown`；`sync`/`?wait=1` 同步） |
| GET | `/v1/jobs` · `/v1/jobs/{id}` | job 列表（project/status/caller 过滤）/ 详情 |
| GET | `/v1/jobs/{id}/logs/{stdout,stderr}` | 日志尾部（256KB） |
| GET | `/v1/jobs/{id}/stream` | SSE 实时日志 + 状态 + 交互 |
| POST | `/v1/jobs/{id}/cancel` | 取消 |
| POST/GET | `/v1/jobs/{id}/interactions` | 运行中交互：发起（提问）/ 列出 |
| POST | `/v1/jobs/{id}/interactions/{iid}/answer` | 回答（`{"answer":"…"}`） |
| GET | `/v1/workers/connect` | worker WS 升级入口（裸 Bearer，失败裸 401） |

`POST /v1/jobs` body（snake_case）：`project_key`、`agent`、`runner`、`prompt`(cli-agent)/`cmd`(exec argv)、`cwd`、`timeout_sec`、`title`、`worker_id`/`worker_labels`(worker)、`sync`/`wait_timeout_sec`、`request_id`(幂等键)。

## 安全注意事项

- **监听 `0.0.0.0:8765`**：为让容器经 `host.docker.internal` 可达；安全靠**强制 token + 内网准入**，非默认绑回环。纯本地自用可收紧 `127.0.0.1`。
- **强制 token**：默认无 token 拒绝启动；空 token 须显式 `--allow-empty-token`。多 caller token 用 `crypto/subtle` 常时间比对，`caller_id` 入库防伪。
- **worker 绑定**：`worker_id` 必须与其 token 绑定（per-worker token MVP 强制，`allow_empty_token` 不豁免）。
- **exec 双重放行** + **cwd 限项目内 safeJoin**（防 `../` 越界）+ **不拼 shell**（argv 数组）+ **token/密钥不入日志** + **日志接口仅尾部 256KB**。

## Docker 容器 ↔ 主机（本工作空间主要用法）

Claude 在容器内，需要主机配合的活（调主机 `codex`、跑需要主机环境的命令、真实 MQ 联调等）经主机上的 gofer 转交：

```bash
# 容器内提交（经 host.docker.internal）
curl -s -X POST http://host.docker.internal:8765/v1/jobs \
  -H "Authorization: Bearer $GOFER_TOKEN" -H "Content-Type: application/json" \
  -d '{"project_key":"workspace","agent":"exec","runner":"local",
       "cmd":["mvn","-q","test"],"cwd":"java-biz-dev/hyy-service-inspect-vision","timeout_sec":1200}'
# 经共享盘读日志 / 或经 HTTP 读状态
tail -n 50 tmp/gofer/<id>/stdout.log
curl -s -H "Authorization: Bearer $GOFER_TOKEN" http://host.docker.internal:8765/v1/jobs/<id>
```

## 迁移 / 历史

> ⚠️ **主机侧仍在运行旧 `codex-bridge`**（`X-Bridge-Token` 头、旧 `/run` + `/result/{id}`）。在主机 bridge 切到 `gofer serve` 并验证通过之前，**勿改根 `CLAUDE.md`、勿删 `tools/codex-bridge`**。切换清单（重启为新二进制 → 容器侧冒烟 → 改根 CLAUDE.md 的文档路径与鉴权头 → 退役旧工具）见 [`docs/2026-06-17-p10-cutover-runbook.md`](docs/2026-06-17-p10-cutover-runbook.md)。

代号沿革：`codex-bridge`（单 codex+exec 直跑）→ `dev-agent-bridge`（多 agent/项目注册表 + `/v1` 异步 job）→ **`gofer`**（+ ws 远端 worker / 标签调度 / 同步与 md 提交 / Web 控制台 / MCP / SQLite 存储）。设计与实施计划见 [`docs/`](docs/)（`design/` 设计、`plans/` 实施计划、`TODO.md` 待办与路线）。
