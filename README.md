# dev-agent-bridge

把本机 / 容器内可配置的 CLI Agent（`codex` / `claude` / `opencode` / 任意命令）与任意已登记项目，桥接为一个统一的**异步 job** 控制面（HTTP + CLI）：一端提交 `{项目, agent, prompt/命令, cwd}`，bridge 在该项目的真实工作目录里异步执行，结果（状态 / 退出码 / 日志）落到项目内的结果目录，可经 HTTP 回读。

> 本工具是 `tools/codex-bridge` 的重构升级版（v0.1 MVP）。从"单一 codex + exec 直跑"演进为"多 agent 注册表 + 多项目注册表 + `/v1` 异步 job API + Bearer 认证"。迁移说明见文末 [迁移 / 切换上线 (Cutover)](#迁移--切换上线-cutover)。

## 简介

- **多项目**：通过配置登记多个项目（各自 `host_path` / `container_path` / 允许的 agent 与 runner）。
- **多 agent**：`codex` / `claude` / `opencode` 通过配置模板接入，未安装时 `agent detect` 返回 `unavailable`，**不影响 serve 启动**。内置 `exec` agent 用于直接跑命令（需项目显式放行）。
- **异步 job**：`POST /v1/jobs` 立即返回 `id`，后台 goroutine 执行；可查状态 / 拉日志 / 取消。
- **结果回传**：结果与日志写到 `<host_path>/tmp/dev-agent-bridge/<job_id>/`，容器与主机共享同一项目盘即可直接读回。
- **CLI = HTTP 封装**：`agent-bridge job ...` 子命令直接调用 server 的 `/v1/jobs`，无需手写 curl。

## 安装 / 构建

需要 Go 1.25+。

```bash
# 当前平台（产物 dist/agent-bridge）
make build
# 或直接 go build
go build -o dist/agent-bridge ./cmd/agent-bridge

# 跨平台交叉编译（linux/darwin/windows amd64+arm64，产物在 dist/）
make build-all
```

> `make build` 默认会用 `upx` 压缩产物；若本机无 upx，可改用上面的 `go build` 直接产出二进制。

## 快速开始

```bash
# 1. 登记一个项目（host-path 必须是已存在的绝对目录）
agent-bridge project add workspace \
  --host-path /d/work/inhere/hyy-ai-inspect \
  --container-path /workspace \
  --default-agent codex \
  --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner local \
  --allow-exec

# 2. 起服务（token 建议走 env，见下）
export AGENT_BRIDGE_TOKEN=dev-token
agent-bridge serve --addr 0.0.0.0:8765

# 3. 另开终端，跑一个 exec smoke job（-- 之后是要执行的命令 argv）
agent-bridge job run -p workspace -a exec --cwd . --wait -- go version
# → job <id> submitted ... job <id> finished: status=done exit_code=0

# 4. 查日志
agent-bridge job logs <id> --stream stdout
```

## 配置文件

### 查找链

`config.Load` 按以下顺序查找配置文件，命中即用（第一个存在的生效）：

1. `--config <path>` 命令行参数
2. 环境变量 `AGENT_BRIDGE_CONFIG`
3. 当前目录 `./.dev-agent-bridge.yaml`
4. `<config-dir>/config.yaml`（即下文「配置目录」，默认 `~/.config/dev-agent-bridge/config.yaml`）

完整示例见 [`configs/bridge.example.yaml`](configs/bridge.example.yaml)，拷贝到上述任一位置即可。关键片段：

```yaml
server:
  # 默认 0.0.0.0:8765，让容器能经 host.docker.internal 访问；
  # 安全性靠 强制 token + 内网准入 保证（见“安全注意事项”）。
  addr: 0.0.0.0:8765
  token_env: AGENT_BRIDGE_TOKEN   # 优先从该 env 读取 bearer token（推荐）
  # token: ""                     # 也可内联（不要把真实 token 提交进仓库）
  allow_empty_token: false        # 必须显式 true 才能无 token 启动

storage:
  default_exchange_subdir: tmp              # 项目内数据交换子目录（默认 tmp）
  default_result_subdir: dev-agent-bridge   # 交换目录下的结果子目录
  # root: /var/lib/dev-agent-bridge   # 可选：全局集中 store（仅影响日志结果目录）
  # db_path: ""                       # 可选：SQLite 元数据库路径；空则
                                      #   显式 db_path > <root>/agent-bridge.db > <config-dir>/agent-bridge.db
  # retention:                        # 可选：终态 job 保留策略（不配=不清理）
  #   max_age_days: 30                #   删除超过 N 天的终态 job（含日志目录）
  #   max_count: 5000                 #   仅保留最新 N 个终态 job
  #   prune_interval_minutes: 60      #   清理周期（默认 60 分钟）

projects:
  workspace:
    host_path: /d/work/inhere/hyy-ai-inspect
    container_path: /workspace
    default_agent: codex
    allowed_agents: [codex, claude, exec]
    allowed_runners: [local]
    allow_exec: true               # 允许 exec agent 在本项目直跑命令
    max_concurrent_jobs: 4

agents:
  # args 模板占位符：{{prompt}} {{cwd}} {{job_id}} {{result_dir}}
  codex:
    type: cli-agent
    command: codex
    args: [exec, "{{prompt}}"]
    detect: { command: codex, args: [--version] }
  exec:                            # 内置 agent，此块仅为补 detect 探针
    type: exec
    detect: { command: sh, args: [-c, "true"] }

runners:
  local: { type: local }          # 内置 runner，显式声明可选
```

### 配置目录与 `.env`

- **配置目录**：默认 `~/.config/dev-agent-bridge/`，可用环境变量 `AGENT_BRIDGE_CFG_DIR` 整体改到别处（同时影响 `config.yaml` 与全局 `.env` 的位置）。
- **`.env` 自动加载**：启动时（**先于读取任何配置**）按顺序加载两个 dotenv 文件，便于本地开发集中放置 `AGENT_BRIDGE_TOKEN` 等敏感值（基于 `goutil/envutil`）：
  1. `<config-dir>/.env`（全局）
  2. `./.env`（当前目录，项目级，**覆盖**全局同名项）
- **优先级**：已显式导出的 OS 环境变量始终高于 `.env` 文件；文件不存在直接跳过、格式错误不致命。
- `.env` 仅用于本地开发便利，**不要提交真实 token**（仓库已 `.gitignore` 忽略 `.env`，并提供 [`.env.example`](.env.example)）。

各段含义：

- **server**：监听地址、bearer token 来源（`token_env` > 内联 `token` > `--token` 覆盖）、是否允许空 token。
- **storage**：项目内交换 / 结果子目录的默认值；可选 `root` 把所有项目结果集中到一处（设了 `root` 后容器侧只能经 HTTP 回读，不再走共享盘）。job 元数据 / 索引 / 交互存于全局 SQLite 库（`db_path`，默认配置目录下 `agent-bridge.db`），日志仍是结果目录文件；内存只保留运行中的 job，终态 job 落库后驱逐。可选 `retention` 周期清理旧终态 job 及其日志。
- **projects**：每个项目的 `host_path`（主机真实路径）、`container_path`（容器挂载路径）、允许的 agent / runner、是否 `allow_exec`、最大并发。
- **agents**：每个 agent 的 `command` + `args` 模板（`type: cli-agent`），或内置 `type: exec`；`detect` 是可用性探针。
- **runners**：`local`（内置，在 bridge 进程本地执行）；`peer-http` 见 [主机访问容器 peer bridge](#主机访问容器-peer-bridgep7-规划中)（P7，规划中）。

### 结果目录规则

- 默认（未设 `storage.root`）：`<host_path>/<exchange_subdir>/<result_subdir>/<job_id>/`，即默认 `<host_path>/tmp/dev-agent-bridge/<job_id>/`。
- 设了 `storage.root`：`<root>/<project_key>/<job_id>/`，集中存放。

每个 job 目录下只保留日志文件 `stdout.log` / `stderr.log`；job 状态 / 元数据 / 索引 / 交互均在 SQLite 库（不再写 `result.json` / `jobs.jsonl` / `interactions.jsonl`），统一经 HTTP 回读。

## CLI 用法

`agent-bridge <command> [sub] [--options] [args]`。所有命令都支持 `-c, --config` 指定配置文件。

```bash
# serve：启动 HTTP 控制面
agent-bridge serve --addr 0.0.0.0:8765 --token dev-token   # 也支持 --allow-empty-token

# project：项目注册表
agent-bridge project list
agent-bridge project show workspace
agent-bridge project add workspace --host-path /abs/path --allow-agent exec --allow-exec
agent-bridge project remove workspace
agent-bridge project validate workspace

# agent：agent 注册表
agent-bridge agent list
agent-bridge agent detect                # 跑各 agent 的 detect 探针，未装报 unavailable（不退非零）
agent-bridge agent show codex

# job：通过 server 的 /v1/jobs 提交与管理（CLI 是 HTTP 封装）
agent-bridge job run -p workspace -a exec --cwd . --wait -- go version
agent-bridge job run -p workspace -a codex --prompt "总结本目录的测试失败用例"
agent-bridge job show <id>
agent-bridge job logs <id> --stream stdout      # stream: stdout|stderr
agent-bridge job cancel <id>
```

`job run` 关键参数：`-p/--project`（必填）、`-a/--agent`（必填）、`--runner`（默认 `local`）、`--cwd`（默认 `.`，限项目内相对路径）、`--prompt`（cli-agent 的提示词）、`--timeout`（秒，0=server 默认）、`--wait`（轮询到终态）、`-s/--server` 与 `--token`（覆盖 config）。

> cli-agent（claude/codex/...）用 `--prompt "..."` 传提示词；exec 类 job 用 `--` 分隔：`--` 之后的 token 被原样当成 argv（如 `-- go version`），不经 shell 重新分词。

## MCP 接入

`agent-bridge mcp` 以 **stdio MCP server** 暴露同一套控制面能力（复用 `serve` 的 `job.Service` / project / agent 注册表，不重复执行逻辑）。stdout 为 MCP 协议通道，命令启动后不向 stdout 输出任何日志。

```bash
# 启动（由 MCP 客户端拉起，通过 stdin/stdout 通信）
agent-bridge mcp --config /abs/path/to/config.yaml
```

给 Claude Code / Codex 等客户端的 MCP 配置示例（`command` 用 `agent-bridge` 二进制的绝对路径）：

```json
{
  "mcpServers": {
    "agent-bridge": {
      "command": "/abs/path/to/agent-bridge",
      "args": ["mcp", "--config", "/abs/path/to/config.yaml"]
    }
  }
}
```

暴露的 8 个 tool（input/output 字段均为 snake_case，与 HTTP API 对齐）：

| Tool | 用途 |
| --- | --- |
| `bridge_list_projects` | 列出已登记项目及其 agent/runner 允许清单 |
| `bridge_list_agents` | 列出 agent 及探测可用性（`available` / `detail`） |
| `bridge_run_job` | 在项目内提交 agent/exec job，返回初始状态（含 `id`） |
| `bridge_get_job` | 按 `id` 查询 job 当前状态 |
| `bridge_tail_log` | 拉取 job 的 stdout/stderr 日志尾部（默认上限 256KB） |
| `bridge_cancel_job` | 请求取消运行中的 job，返回当前状态 |
| `bridge_get_interactions` | 列出 job 的运行中交互（待答问题及其答复） |
| `bridge_answer_interaction` | 回答 job 的某条待答交互（`interaction_id` + `answer`），使 agent 继续 |

## 运行中交互（P9）

运行中的 agent / wrapper 可在执行途中向用户提问、等待回答后再继续：

1. agent 经 `POST /v1/jobs/{id}/interactions` 提问，job 状态置为 `pending_interaction`。
2. 用户经 `GET /v1/jobs/{id}/interactions` 看到 `pending` 交互，`POST .../answer`（body `{"answer":"..."}`）回答。
3. 答复后该 job 无其它待答交互时自动回到 `running`，agent 读到答案继续直至完成。

MCP 侧对应 `bridge_get_interactions`（读） + `bridge_answer_interaction`（答）。

## Web 控制台

`serve` 默认内置一个 Web 控制台（静态 SPA，挂在根路径，无需鉴权即可打开页面；页面内对 `/v1/*` 的请求仍需 token）。控制台静态资源**嵌入到二进制**，运行时不依赖外部文件。

```bash
# 1. 构建并嵌入前端，再编译二进制（一步到位）
make web build       # = make web（pnpm 构建 web/dist 并拷入 internal/webui/dist）+ make build（baking 进二进制）

# 2. 起服务（Web 默认开启）
agent-bridge serve --addr 0.0.0.0:8765 --token dev-token

# 3. 浏览器打开 http://<addr>/ ，在页面里粘贴 token 接入，即可看 project/agent/job、实时日志流
```

- **关闭 Web 只留 API**：`serve --no-web`，或配置 `server.web_enabled: false`；此时 `GET /` 返回 404，`/health` 与 `/v1/*` 不受影响。
- **裸 `go build`（未跑 `make web`）**：二进制仍可编译运行，`GET /` 返回一个占位页（提示运行 `make web` 重新构建），不影响 API。
- 前端开发态自行 `pnpm -C web dev`（vite 代理到 `serve` 的 `/v1`），无需每次 `make web`。

## HTTP API

HTTP server 基于 `gookit/rux`。`/health` 不鉴权；`/v1/*` 全部要求 `Authorization: Bearer <token>`。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/health` | 进程存活（不鉴权） |
| `GET` | `/v1/projects` | 项目列表 |
| `GET` | `/v1/projects/{key}` | 项目详情 |
| `GET` | `/v1/agents` | Agent 列表 + 检测状态 |
| `POST` | `/v1/jobs` | 创建异步 job |
| `GET` | `/v1/jobs/{id}` | 查询 job 状态 |
| `GET` | `/v1/jobs/{id}/logs/stdout` | 读取 stdout（尾部 256KB 限制） |
| `GET` | `/v1/jobs/{id}/logs/stderr` | 读取 stderr（尾部 256KB 限制） |
| `POST` | `/v1/jobs/{id}/cancel` | 取消 job |
| `POST` | `/v1/jobs/{id}/interactions` | 运行中 agent 发起交互（提问） |
| `GET` | `/v1/jobs/{id}/interactions` | 列出 job 的交互（`{"interactions":[...]}`） |
| `POST` | `/v1/jobs/{id}/interactions/{interaction_id}/answer` | 回答某条待答交互（body `{"answer":"..."}`） |

错误响应统一小结构（**不套**公司业务 `{status,code,message}` 信封）：

```json
{"error":"unknown project","detail":"project_key other not found"}
```

curl 示例：

```bash
TOKEN=dev-token
BASE=http://127.0.0.1:8765      # 容器内用 http://host.docker.internal:8765

# 提交 exec job（立即返回带 id 的 JobResult）
curl -s -X POST "$BASE/v1/jobs" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
        "project_key": "workspace",
        "agent": "exec",
        "runner": "local",
        "cmd": ["go", "version"],
        "cwd": ".",
        "timeout_sec": 30
      }'
# → {"id":"...","project_key":"workspace","agent":"exec","status":"queued","result_dir":"...",...}

# 查状态 / 拉日志 / 取消
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/jobs/<id>"
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/jobs/<id>/logs/stdout"
curl -s -X POST -H "Authorization: Bearer $TOKEN" "$BASE/v1/jobs/<id>/cancel"
```

`POST /v1/jobs` body 字段（snake_case）：`project_key`、`agent`、`runner`、`prompt`（cli-agent）/`cmd`（exec argv）、`cwd`、`timeout_sec`、`title`。
job 状态：`queued` / `running` / `done` / `failed` / `cancelled` / `timeout`。

## Docker 内访问主机 bridge（主要用法）

本工作空间的典型场景：**Claude 在 docker 容器内**，需要主机配合的活（调主机 `codex`、跑需要主机环境的命令、真实 MQ 联调等）经主机上跑的 bridge 转交。

```
┌─ docker 容器 (Claude) ──────────────┐        ┌──────── 主机 (host) ────────┐
│ curl host.docker.internal:8765/v1/jobs │ ──▶ │ agent-bridge serve (本程序)  │
│   Authorization: Bearer <token>     │        │   在项目真实 cwd 异步执行     │
│ 读 ./tmp/dev-agent-bridge/<id>/...  │ ◀──    │   结果写 <host_path>/tmp/... │
└─────────────────────────────────────┘ 共享盘 └──────────────────────────────┘
```

容器侧步骤：

```bash
# 1. 提交（容器经 host.docker.internal 访问主机 bridge）
curl -s -X POST http://host.docker.internal:8765/v1/jobs \
  -H "Authorization: Bearer $AGENT_BRIDGE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"project_key":"workspace","agent":"exec","runner":"local",
       "cmd":["mvn","-q","test"],
       "cwd":"java-biz-dev/hyy-service-inspect-vision","timeout_sec":1200}'
# → {"id":"...","result_dir":".../tmp/dev-agent-bridge/<id>",...}

# 2. 经共享项目盘直接回读日志（容器与主机看到同一份 tmp/；状态/元数据见 HTTP）
tail -n 50 tmp/dev-agent-bridge/<id>/stdout.log
# 或不依赖共享盘，经 HTTP 回读（状态/元数据只在此处，已不写 result.json）：
curl -s -H "Authorization: Bearer $AGENT_BRIDGE_TOKEN" \
  http://host.docker.internal:8765/v1/jobs/<id>
```

> `cwd` 必须是项目内相对路径（不接受主机绝对路径，防 `../` 越界）。

## 主机访问容器 peer bridge（P7，规划中）

反向形态：bridge 跑在主机，把某个 runner 指向**容器内运行的另一台 peer bridge**，由容器侧执行（需要容器环境的活）。MVP **未实现**，但配置结构已预留：

```yaml
runners:
  docker-peer:
    type: peer-http
    base_url: http://127.0.0.1:8766      # 容器 peer bridge 地址
    token_env: CONTAINER_BRIDGE_TOKEN
```

提交时指定 `"runner": "docker-peer"` 即转发到 peer。**注意：`peer-http` runner 在 MVP 阶段未落地**，仅结构预留，落地见计划 P7。

## Agent 示例

`args` 模板占位符：`{{prompt}}` / `{{cwd}}` / `{{job_id}}` / `{{result_dir}}`，逐元素替换且保持 argv 数组（`{{prompt}}` 始终是**一个** argv 元素，含空格 / 引号也不会被 shell 重新分词——全程不经 shell）。各 CLI 的非交互参数以你本机安装的版本为准，下面是常见形态：

```yaml
agents:
  codex:                      # codex exec "<prompt>"
    type: cli-agent
    command: codex
    args: [exec, "{{prompt}}"]
    detect: { command: codex, args: [--version] }
  claude:                     # claude -p "<prompt>"（-p 打印模式，非交互）
    type: cli-agent
    command: claude
    args: ["-p", "{{prompt}}"]
    detect: { command: claude, args: [--version] }
  opencode:                   # opencode run "<prompt>"
    type: cli-agent
    command: opencode
    args: [run, "{{prompt}}"]
    detect: { command: opencode, args: [--version] }
```

提交示例：

```bash
agent-bridge job run -p workspace -a codex    --prompt "在本目录跑测试并总结失败用例"
agent-bridge job run -p workspace -a claude   --prompt "审查本目录的改动并给出风险点"
agent-bridge job run -p workspace -a opencode --prompt "解释本目录的整体结构"
```

> 未安装某 agent 时，`agent detect` / `GET /v1/agents` 对它返回 `unavailable` 并附错误，但**不影响 serve 启动**，也不影响其它 agent。

## 安全注意事项

- **监听地址默认 `0.0.0.0:8765`**：这是为了让容器经 `host.docker.internal` 反向可达；安全性**靠强制 token + 内网准入**保证，而非默认绑回环。纯主机本地自用可显式收紧为 `127.0.0.1:8765`。
- **强制 token**：默认不允许空 token；`serve` 在无 token 时**拒绝启动**。要无鉴权启动必须显式 `--allow-empty-token`（或配置 `allow_empty_token: true`），仅限可信本地环境。token 来源优先级：`server.token_env` > 内联 `server.token` > `--token` 覆盖。
- **exec 双重放行**：`exec` 类 job 必须同时满足「agent 是 `exec`」且「项目 `allow_exec: true`」，且 agent / project / runner 都必须在配置 allowlist 内。
- **cwd 限项目内**：不接受请求传主机绝对路径作为 cwd；统一在项目内做 `safeJoin` 校验，防 `../` 越界。
- **不拼 shell**：所有命令保持 argv 数组，不经 shell 重新分词。
- **token 不入日志**：token / 密钥不写日志、不写响应体。
- **日志限制**：日志接口只返回尾部 256KB；完整日志经项目内结果目录文件查看。

## 迁移 / 切换上线 (Cutover)

> 当前主机上**仍在运行旧 `codex-bridge`**（用 `X-Bridge-Token` 头、旧 `/run` + `/result/{id}` 接口）。**在主机 bridge 切到新 `agent-bridge serve` 二进制并验证之前，请勿改动根 `CLAUDE.md`，也不要删除 `tools/codex-bridge`**——否则文档会与仍存活的服务契约不符。下面是届时由人工在主机侧执行的精确清单。

### A. 重启主机 bridge 为新二进制

```bash
# 主机侧
cd /d/work/inhere/hyy-ai-inspect/tools/dev-agent-bridge
make build                       # 产出 dist/agent-bridge（或 go build -o dist/agent-bridge ./cmd/agent-bridge）

# 登记工作空间项目（一次性，若尚未登记）
./dist/agent-bridge project add workspace \
  --host-path /d/work/inhere/hyy-ai-inspect \
  --container-path /workspace \
  --default-agent codex \
  --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner local --allow-exec

# 停掉旧 codex-bridge 进程后，用新二进制起服务（端口保持 8765）
export AGENT_BRIDGE_TOKEN=tok-for-docker-dev
./dist/agent-bridge serve --addr 0.0.0.0:8765
```

### B. 验证（容器侧冒烟）

```bash
curl -s http://host.docker.internal:8765/health
curl -s -X POST http://host.docker.internal:8765/v1/jobs \
  -H "Authorization: Bearer tok-for-docker-dev" \
  -H "Content-Type: application/json" \
  -d '{"project_key":"workspace","agent":"exec","runner":"local","cmd":["go","version"],"cwd":".","timeout_sec":30}'
# 然后 GET /v1/jobs/<id> 应到 status=done
```

### C. 验证通过后，更新根 `CLAUDE.md`（逐行精确改动）

根 `CLAUDE.md` 当前 L16-18：

```text
  - access token: tok-for-docker-dev
  - docker access: http://host.docker.internal:8765
  - 详细使用文档查看 tools/codex-bridge/README.md
```

改为：

```text
  - access token: tok-for-docker-dev                  ← 不变（核对仍为此值即可）
  - docker access: http://host.docker.internal:8765   ← 端点不变
  - 详细使用文档查看 tools/dev-agent-bridge/README.md  ← 路径 codex-bridge → dev-agent-bridge
```

并在该处补一行鉴权头说明（旧 `X-Bridge-Token` → 新 `Authorization: Bearer <token>`），例如：

```text
  - 鉴权：请求头 Authorization: Bearer <access token>（旧 X-Bridge-Token 已废弃）
```

> 经核实：全工作空间内仅根 `CLAUDE.md`（L16-18）引用了 `codex-bridge`，无其它文件需改。

### D. 退役旧工具

- 验证稳定后，删除 `tools/codex-bridge` 目录。
- 各项目历史 `tmp/codex-bridge/` 结果目录**不自动删除**，作为历史归档，可由人工按需清理。
- 旧接口 `/run`、`/result/{id}`，旧参数 `mode=codex|exec`，旧鉴权头 `X-Bridge-Token` 均**已废弃**；新接口为 `/v1/jobs` + `Authorization: Bearer <token>`。

## 变更记录

### SQLite 存储后端（C1，2026-06-18）

- job 元数据 / 索引 / 交互迁入全局 SQLite 库（modernc 纯 Go，`db_path`）；日志仍是结果目录文件。
- 内存只保留运行中的 job，终态落库后驱逐 → 根治长跑 server 内存 / 索引文件无界增长。
- 列表为 DB 索引查询（项目 / 状态 / 分页）；新增 `storage.retention` 周期清理旧终态 job 及日志。
- **停写**：`result.json` / `jobs.jsonl` / `interactions.jsonl` / `request.json`（后者入 DB `request_json` 列）。

### v0.1（MVP）

从 `codex-bridge` 重构为 `dev-agent-bridge`：

- 新 CLI `agent-bridge`：`serve` / `project` / `agent` / `job`（`mcp` 占位）。
- 配置驱动的**项目注册表**（多 host path）与**多 agent 注册表**（codex / claude / opencode / 内置 exec）。
- 新 `/v1` HTTP API（projects / agents / jobs），**异步 job**（提交 / 查状态 / 拉日志 / 取消 / 超时）。
- 鉴权改为 `Authorization: Bearer <token>`；默认强制 token，空 token 须显式放行。
- 结果目录改为 `tmp/dev-agent-bridge/<job_id>`（可选全局 `storage.root`）。
- **已废弃**：旧 `/run`、`/result/{id}` 接口；`mode=codex|exec` 参数；`X-Bridge-Token` 头。
- **规划中（MVP 未落地）**：`peer-http` runner（P7，结构已预留）、`mcp` server（P8）、运行中 agent 双向交互（P9）。

详细设计与实施计划见 [`docs/`](docs/)。
