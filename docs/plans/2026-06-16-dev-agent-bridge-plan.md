# dev-agent-bridge 实施计划

文档修订记录：

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-16 | Codex | 初版：基于 `docs/design/2026-06-16-dev-agent-bridge-design.md` 拆解实施阶段、代码结构、接口、测试与交付检查 |
| v0.2 | 2026-06-16 | Codex | 按反馈调整依赖：YAML 使用 `github.com/goccy/go-yaml`，HTTP 使用 `github.com/gookit/rux/v2`；配置增加数据交换目录，默认项目下 `tmp`；补充运行中 Agent 双向交互后续阶段 |
| v0.3 | 2026-06-16 | Claude | 审核修订：①修正 §5 包职责表 `internal/mcp` 阶段号 P7→P8；②`internal/runner` 接口标注流式 Writer 仅 local 语义；③P4 补 job_id 防碰撞（纳秒/随机后缀）与 `interactions.jsonl` 目录预留；④P0/P10 二进制跟踪表述按实际（未跟踪）简化、`.gitignore` 忽略名同步为 `/agent-bridge`；⑤P10 新增"更新根 CLAUDE.md 工具引用/鉴权头"任务；⑥安全章补监听地址默认 `0.0.0.0`+强制 token 口径；⑦P8/P9 标注运行中交互走 MCP 不引入 ACP；⑧§14 验收补根 CLAUDE.md 已更新项 |
| v0.4 | 2026-06-16 | Claude | 落实用户确认：①配置默认位置用户级 `~/.config/dev-agent-bridge/config.yaml`（§6.1 查找链已含）；②容器反向调用优先 peer bridge（P7），`docker-exec` 不进 MVP；③`StorageConfig` 增加可选 `Root` 全局 store 字段，P2 结果目录解析加 `storage.root` 分支，默认仍各项目 `tmp/`；④§15 开工前确认项更新 |
| v0.5 | 2026-06-16 | Claude | 文档随迁入独立仓库 `tools/dev-agent-bridge`；对齐 Makefile：版本变量注入 `package main`（`var Version/GitCommit/BuildDate`），§8.1 入口与 P1 改用 `commands.NewApp(Version)`，消除 `version.Version` 与 `-X main.Version` 不一致 |

> 本计划属于工具内实施计划，文档已迁入独立仓库 `tools/dev-agent-bridge/docs/plans/`（先于正式 P1 重命名完成迁移；`tools/dev-agent-bridge` 自带独立 `.git`，不归主仓库跟踪）。
> 本计划只围绕桥接工具自身实施，不默认修改任何被登记项目代码。

## 1. 目标

把现有 `tools/codex-bridge` 从单文件 Codex HTTP 包装器改造为 `dev-agent-bridge`：

- 项目目录改为 `tools/dev-agent-bridge`，正式 CLI 为 `agent-bridge`，可选短别名为 `dab`。
- CLI 解析使用 `github.com/gookit/gcli/v3`，支持 `serve`、`project`、`agent`、`job`、`mcp` 多级子命令。
- 支持 Codex、Claude、OpenCode 以及任意可配置 CLI Agent。
- 支持任意 Docker 挂载目录 / 项目，通过 `project_key` 选择，不再启动时写死单个 project。
- 支持本机 local runner，后续扩展 peer-http runner 反向调用容器内 bridge。
- 支持项目数据交换目录配置，默认项目下 `tmp`，job 结果默认写到 `<project>/tmp/dev-agent-bridge/<job_id>`。
- 移除旧 `codex-bridge` 名称、旧 `/run`、旧 `/result/{id}`、旧 `mode=codex|exec`、旧 `tmp/codex-bridge`。

## 2. 非目标

- 不兼容旧接口、旧结果目录和旧 CLI flag。
- 不实现交互式 TUI 托管和持续会话输入；首期只支持非交互异步 job。
- 不内置每个 Agent 的复杂语义解析，只负责进程执行、日志、结果和状态。
- 不把工具抽成独立仓库；等实际有多个仓库稳定复用后再做。
- 不修改被登记项目源码；只有用户提交的 job 命令或 Agent prompt 可能触达目标项目。

## 3. 当前基线

当前工具状态：

| 文件 / 能力 | 状态 | 改造动作 |
|---|---|---|
| `main.go` | 单文件实现，包含 flag、HTTP、job、执行逻辑 | 拆到 `cmd/agent-bridge` 和 `internal/*` |
| `go.mod` | module 为 `codex-bridge`，暂无 gcli 依赖 | 改 module 名，新增 `gcli/v3`、YAML 依赖 |
| `start.ps1` / `start.sh` | 启动旧二进制和旧 flag | 改为 `agent-bridge serve` |
| HTTP API | `/run`、`/result/{id}` | 替换为 `/v1/jobs` 系列 |
| Project | 启动时 `-project` 写死 | 配置文件 `projects` 注册表 |
| Agent | `mode=codex|exec` | 配置文件 `agents` 注册表 |
| Store | `<project>/tmp/codex-bridge/<job_id>` | `<project>/tmp/dev-agent-bridge/<job_id>` |

实施前先确认工作树，只暂存工具目录和 Beads 导出，不混入无关文档改动。

## 4. 依赖决策

| 依赖 | 用途 | 决策 |
|---|---|---|
| `github.com/gookit/gcli/v3` | CLI app、flag、多级子命令 | 必选 |
| `github.com/gookit/rux/v2` | HTTP server / router | 必选 |
| `github.com/gookit/goutil` | 文件、字符串、断言等辅助 | 可用，优先匹配本地 gookit 使用习惯 |
| `github.com/goccy/go-yaml` | YAML 配置读写 | 必选；需要保留未知字段时使用该库的 Node / AST 能力 |
| MCP SDK | stdio MCP server | P8 再选型，不进入 MVP |

配置读写优先保持简单：业务运行时解码到强类型 struct；`project add/remove` 需要写回时，使用 `github.com/goccy/go-yaml` 的 Node / AST 能力保留未知顶层字段，避免覆盖人工扩展配置。

## 5. 目标代码结构

P1 完成后目录形态：

```txt
tools/dev-agent-bridge/
|- cmd/
|  `- agent-bridge/
|     `- main.go
|- internal/
|  |- app/
|  |  `- app.go
|  |- commands/
|  |  |- serve.go
|  |  |- project.go
|  |  |- agent.go
|  |  |- job.go
|  |  `- mcp.go
|  |- config/
|  |  |- model.go
|  |  |- loader.go
|  |  `- writer.go
|  |- project/
|  |  |- registry.go
|  |  `- path.go
|  |- agent/
|  |  |- registry.go
|  |  |- adapter.go
|  |  |- template.go
|  |  `- detect.go
|  |- job/
|  |  |- model.go
|  |  |- service.go
|  |  `- cancel.go
|  |- runner/
|  |  |- runner.go
|  |  |- local/
|  |  |  `- runner.go
|  |  `- peerhttp/
|  |     `- runner.go
|  |- store/
|  |  |- store.go
|  |  `- filestore.go
|  |- httpapi/
|  |  |- server.go
|  |  |- job_handler.go
|  |  |- project_handler.go
|  |  `- agent_handler.go
|  |- mcp/
|  |  |- server.go
|  |  `- tools.go
|  `- security/
|     |- auth.go
|     `- policy.go
|- configs/
|  `- bridge.example.yaml
|- scripts/
|  |- start.ps1
|  `- start.sh
|- docs/
|  |- design/
|  `- plans/
|- README.md
`- go.mod
```

包职责边界：

| 包 | 职责 |
|---|---|
| `cmd/agent-bridge` | 创建 `gcli.App`，注册命令，处理退出码 |
| `internal/commands` | 只做 CLI 参数绑定和调用应用服务 |
| `internal/config` | 配置查找、加载、校验、写回 |
| `internal/project` | 项目注册、路径解析、目录安全 |
| `internal/agent` | Agent 注册、检测、模板渲染、命令构造 |
| `internal/job` | Job 状态机、超时、取消、调度 |
| `internal/runner` | 执行位置抽象，首期 local，后续 peer-http |
| `internal/store` | `request.json`、`stdout.log`、`stderr.log`、`result.json` 文件存储 |
| `internal/httpapi` | HTTP 路由、请求响应、认证中间件 |
| `internal/mcp` | stdio MCP server，P8 实现 |
| `internal/security` | token、项目 allowlist、exec 策略 |

## 6. 核心模型

### 6.1 配置模型

```go
type Config struct {
    Server   ServerConfig             `yaml:"server"`
    Storage  StorageConfig            `yaml:"storage"`
    Projects map[string]ProjectConfig `yaml:"projects"`
    Agents   map[string]AgentConfig   `yaml:"agents"`
    Runners  map[string]RunnerConfig  `yaml:"runners"`
}

type StorageConfig struct {
    DefaultExchangeSubdir string `yaml:"default_exchange_subdir"` // 默认 tmp
    DefaultResultSubdir   string `yaml:"default_result_subdir"`   // 默认 dev-agent-bridge
    Root                  string `yaml:"root"`                    // 可选；非空=全局 store，留空=默认各项目 tmp/
}

type ProjectConfig struct {
    HostPath          string   `yaml:"host_path"`
    ContainerPath     string   `yaml:"container_path"`
    ExchangeSubdir    string   `yaml:"exchange_subdir"`
    ResultSubdir      string   `yaml:"result_subdir"`
    DefaultAgent      string   `yaml:"default_agent"`
    AllowedAgents     []string `yaml:"allowed_agents"`
    AllowedRunners    []string `yaml:"allowed_runners"`
    AllowExec         bool     `yaml:"allow_exec"`
    MaxConcurrentJobs int      `yaml:"max_concurrent_jobs"`
}

type AgentConfig struct {
    Type        string            `yaml:"type"`
    Command     string            `yaml:"command"`
    Args        []string          `yaml:"args"`
    Env         map[string]string `yaml:"env"`
    AllowRawCmd bool              `yaml:"allow_raw_cmd"`
    Detect      DetectConfig      `yaml:"detect"`
}
```

配置查找顺序：

1. CLI `--config`。
2. 环境变量 `AGENT_BRIDGE_CONFIG`。
3. 当前目录 `.dev-agent-bridge.yaml`。
4. 用户级 `~/.config/dev-agent-bridge/config.yaml`。

目录默认值：

- `storage.default_exchange_subdir` 默认为 `tmp`。
- `storage.default_result_subdir` 默认为 `dev-agent-bridge`。
- `project.exchange_subdir` 未设置时使用 `storage.default_exchange_subdir`。
- `project.result_subdir` 未设置时使用 `storage.default_result_subdir`。
- job 结果目录按 `storage.root` 决定（用户确认）：
  - **未设 `storage.root`（默认）**：`<project.host_path>/<exchange_subdir>/<result_subdir>/<job_id>`（容器经共享盘可读）。
  - **设 `storage.root`（全局 store）**：`<storage.root>/<project_key>/<job_id>`（集中存放；容器侧只能经 HTTP 回查，不经共享盘）。
- `exchange_subdir` 和 `result_subdir` 都必须按项目内相对路径校验，不能逃逸项目目录；`storage.root` 为主机绝对路径，转绝对后须可创建 / 可写。

### 6.2 Job 模型

```go
type JobRequest struct {
    ProjectKey string   `json:"project_key"`
    Agent      string   `json:"agent"`
    Runner     string   `json:"runner"`
    Prompt     string   `json:"prompt,omitempty"`
    Cmd        []string `json:"cmd,omitempty"`
    Cwd        string   `json:"cwd,omitempty"`
    TimeoutSec int      `json:"timeout_sec,omitempty"`
    Title      string   `json:"title,omitempty"`
}

type JobResult struct {
    ID         string `json:"id"`
    ProjectKey string `json:"project_key"`
    Agent      string `json:"agent"`
    Runner     string `json:"runner"`
    Status     string `json:"status"`
    ExitCode   int    `json:"exit_code"`
    Cwd        string `json:"cwd"`
    ResultDir  string `json:"result_dir"`
    StartedAt  int64  `json:"started_at"`
    EndedAt    int64  `json:"ended_at,omitempty"`
    Error      string `json:"error,omitempty"`
}
```

状态值：

| 状态 | 含义 |
|---|---|
| `queued` | 已创建，等待执行 |
| `running` | 子进程已启动 |
| `done` | 正常结束且退出码为 0 |
| `failed` | 进程退出码非 0 或执行错误 |
| `cancelled` | 用户取消 |
| `timeout` | 超时被终止 |

### 6.3 Runner 接口

```go
type Runner interface {
    Name() string
    Run(ctx context.Context, req runner.Request) runner.Result
}

type Request struct {
    JobID     string
    WorkDir   string
    Command   string
    Args      []string
    Env       map[string]string
    Stdout    io.Writer
    Stderr    io.Writer
}
```

接口语义说明：`Stdout` / `Stderr` 这对 `io.Writer` 是 **local runner 专用的流式直写约定**（job.Service 打开 `stdout.log` / `stderr.log` 传入，子进程实时写文件）。**peer-http / docker-exec runner 不走这对 Writer**——它们把请求转发给远端后，日志与结果以远端 `/v1/jobs` API 回查为准（见 P7）。实现时应在 Runner 接口注释里点明该差异，避免误以为所有 runner 统一流式语义。

首期只实现 local runner。peer-http runner 在 P7 实现，并复用 `/v1/jobs` 协议。

## 7. HTTP API

旧 `/run`、`/result/{id}` 不保留。新 API：
HTTP server / router 使用 `github.com/gookit/rux/v2`，handler 层只负责参数解析、认证上下文和响应编码，业务逻辑下沉到 service / registry。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/health` | 进程存活 |
| `GET` | `/v1/projects` | 项目列表 |
| `GET` | `/v1/projects/{key}` | 项目详情 |
| `GET` | `/v1/agents` | Agent 列表和检测状态 |
| `POST` | `/v1/jobs` | 创建异步任务 |
| `GET` | `/v1/jobs/{id}` | 查询任务状态 |
| `GET` | `/v1/jobs/{id}/logs/stdout` | 读取 stdout |
| `GET` | `/v1/jobs/{id}/logs/stderr` | 读取 stderr |
| `POST` | `/v1/jobs/{id}/cancel` | 取消任务 |

认证：

- 首期使用 `Authorization: Bearer <token>`。
- 兼容性不是目标，不保留旧 header 作为必需能力。
- `server.token_env` 优先；`--token` 仅用于临时覆盖。
- 空 token 必须显式 `--allow-empty-token` 或配置 `allow_empty_token: true`。

错误响应使用统一小结构，不套业务服务 `{status, code, message}`，因为这是本地开发工具，不是公司业务内网服务：

```json
{"error":"unknown project","detail":"project_key other not found"}
```

## 8. CLI 命令

### 8.1 入口

`cmd/agent-bridge/main.go` 只保留应用装配：

```go
// 版本变量由 Makefile LDFLAGS 注入到 package main（-X main.Version 等）
var Version, GitCommit, BuildDate string

func main() {
    app := commands.NewApp(Version)
    app.Run(nil)
}
```

`internal/commands/app.go`：

```go
func NewApp(version string) *gcli.App {
    app := gcli.NewApp()
    app.Name = "agent-bridge"
    app.Desc = "Bridge local and container CLI agents across allowed projects."
    app.Version = version
    app.Add(NewServeCmd())
    app.Add(NewProjectCmd())
    app.Add(NewAgentCmd())
    app.Add(NewJobCmd())
    app.Add(NewMcpCmd())
    return app
}
```

### 8.2 命令清单

| 命令 | MVP | 说明 |
|---|---|---|
| `agent-bridge serve` | 是 | 启动 HTTP server |
| `agent-bridge project list` | 是 | 列出配置项目 |
| `agent-bridge project show <key>` | 是 | 查看项目详情 |
| `agent-bridge project add <key>` | 是 | 写入项目配置 |
| `agent-bridge project remove <key>` | 是 | 删除项目配置 |
| `agent-bridge project validate <key>` | 是 | 校验路径、Agent、runner |
| `agent-bridge agent list` | 是 | 列出 Agent |
| `agent-bridge agent detect` | 是 | 执行 detect 命令 |
| `agent-bridge agent show <key>` | 是 | 查看 Agent 配置 |
| `agent-bridge job run` | 是 | 通过 HTTP 提交任务 |
| `agent-bridge job show <id>` | 是 | 查询任务 |
| `agent-bridge job logs <id>` | 是 | 查看日志 |
| `agent-bridge job cancel <id>` | 是 | 取消任务 |
| `agent-bridge mcp` | 否 | P7 stdio MCP server |

`project`、`agent`、`job` 必须使用 `Command.Subs`，不使用 `capp`。

### 8.3 project add 参数

```bash
agent-bridge project add hyy-ai-inspect \
  --host-path D:/work/inhere/hyy-ai-inspect \
  --container-path /workspace \
  --exchange-subdir tmp \
  --result-subdir dev-agent-bridge \
  --default-agent codex \
  --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner local \
  --allow-exec \
  --force
```

写入规则：

- `host_path` 转绝对路径，必须存在且是目录。
- `container_path` 可空，只做基本格式校验。
- `exchange_subdir` 默认 `tmp`。
- `result_subdir` 默认 `dev-agent-bridge`，相对 `exchange_subdir` 解析。
- 同 key 默认拒绝覆盖，`--force` 才覆盖。
- 写回当前加载配置；若不存在配置文件，创建用户级配置。

## 9. 实施阶段

### P0：准备与安全边界

目标：确保重构范围清晰，避免混入无关改动。

任务：

- 检查工作树，记录已有无关 dirty / ahead 状态。
- 只在 `tools/codex-bridge` 或重命名后的 `tools/dev-agent-bridge` 内改动。
- 二进制产物：经核实当前 `codex-bridge` / `codex-bridge.exe` **未被 Git 跟踪**（已在 `.gitignore`），无需移除；P1 重命名后只需把 `.gitignore` 忽略名同步为 `/agent-bridge`、`/agent-bridge.exe`（并可保留 `/dab`）。
- 在 Beads issue 中记录实施入口。

验收：

```bash
git status --short --branch
bd show hyy-ai-inspect-799
```

交付：无代码变化或只提交计划文档。

### P1：目录重命名与 gcli 骨架

目标：完成项目命名、Go 目录结构和 CLI skeleton。

任务：

- 将 `tools/codex-bridge` 重命名为 `tools/dev-agent-bridge`。
- `go.mod` module 改为 `dev-agent-bridge`，新增 `github.com/gookit/gcli/v3`、`github.com/gookit/rux/v2`、`github.com/goccy/go-yaml`。
- 新建 `cmd/agent-bridge/main.go`。
  - **版本变量与 Makefile 对齐**：`Makefile` 的 `LDFLAGS` 注入 `-X main.Version` / `-X main.GitCommit` / `-X main.BuildDate`，故须在 `package main`（`cmd/agent-bridge/main.go`）声明 `var Version, GitCommit, BuildDate string`，并把 `Version` 传入 `commands.NewApp(Version)`（统一 §8.1 示例里 `version.Version` 的写法，避免出现空版本号）。
- 新建 `internal/commands`，注册 `serve/project/agent/job/mcp` 命令。
- `serve` 暂时可以只打印配置加载结果或启动空 HTTP server。
- 更新 README、`start.ps1`、`start.sh` 中的命令名和目录名。
- 移除旧 `main.go` 中的顶层 `flag` 解析入口。

验收：

```bash
cd tools/dev-agent-bridge
go mod tidy
go test ./...
go run ./cmd/agent-bridge --help
go run ./cmd/agent-bridge serve --help
go run ./cmd/agent-bridge project --help
go run ./cmd/agent-bridge project add --help
```

提交建议：

```bash
git add tools/dev-agent-bridge
git commit -m "refactor(agent-bridge): add gcli command skeleton"
```

### P2：配置加载与 Project Registry

目标：服务不再依赖启动时写死的单项目路径。

任务：

- 实现 `internal/config`：查找、加载、默认值、校验。
- 新增 `configs/bridge.example.yaml`。
- 实现 `internal/project.Registry`。
- 实现 `project.SafeJoin(project, cwd)`：
  - 只接受相对 `cwd`。
  - clean 后不能逃逸 `host_path`。
  - 空 `cwd` 视为 `"."`。
- 实现项目交换目录解析：
  - `exchange_subdir` 默认 `tmp`。
  - `result_subdir` 默认 `dev-agent-bridge`。
  - job 结果目录按 `storage.root` 分支：未设=`<host_path>/<exchange_subdir>/<result_subdir>/<job_id>`（默认）；已设=`<storage.root>/<project_key>/<job_id>`（全局 store）。
  - `project validate` 需要校验交换目录和结果目录可创建 / 可写（含全局 store 时的 `storage.root`）。
- 实现 `project list/show/add/remove/validate`。
- `project add/remove` 使用 `github.com/goccy/go-yaml` 写回 YAML，并保留未知顶层字段。

关键测试：

- `cwd="."` 成功。
- `cwd="tools/dev-agent-bridge"` 成功。
- `cwd=".."` 失败。
- `cwd="../other"` 失败。
- Windows 盘符绝对路径作为 `cwd` 失败。
- 未登记 `project_key` 失败。

验收：

```bash
cd tools/dev-agent-bridge
go test ./internal/config ./internal/project ./internal/commands
go run ./cmd/agent-bridge project add self --config tmp/bridge-test.yaml --host-path ..\.. --allow-agent exec --allow-runner local --allow-exec
go run ./cmd/agent-bridge project list --config tmp/bridge-test.yaml
go run ./cmd/agent-bridge project validate self --config tmp/bridge-test.yaml
```

提交建议：

```bash
git add tools/dev-agent-bridge
git commit -m "feat(agent-bridge): add project registry config"
```

### P3：Agent Registry 与命令模板

目标：Codex、Claude、OpenCode、自定义 Agent 通过配置接入。

任务：

- 实现 `internal/agent.Registry`。
- 实现 Agent 类型：
  - `cli-agent`：用 `command + args` 模板渲染 prompt。
  - `exec`：只允许 `JobRequest.Cmd`。
- 实现模板渲染，首期支持：
  - `{{prompt}}`
  - `{{cwd}}`
  - `{{job_id}}`
  - `{{result_dir}}`
- 实现 `agent detect`，运行 detect 命令，返回 available/version/error。
- 实现项目级 `allowed_agents` 校验。
- 默认示例配置包含 `codex`、`claude`、`opencode`、`exec`，但单测不要求本机一定安装这些 CLI。

关键测试：

- `codex` 模板渲染保留 argv 数组，不拼 shell 字符串。
- `prompt` 为空且 Agent 类型为 `cli-agent` 时失败。
- `exec` Agent 不允许没有 `cmd`。
- 项目未允许的 agent 失败。
- detect 命令不存在时返回 unavailable，不让整个服务启动失败。

验收：

```bash
cd tools/dev-agent-bridge
go test ./internal/agent ./internal/security ./...
go run ./cmd/agent-bridge agent list --config configs/bridge.example.yaml
go run ./cmd/agent-bridge agent detect --config configs/bridge.example.yaml
```

提交建议：

```bash
git add tools/dev-agent-bridge
git commit -m "feat(agent-bridge): add configurable agent registry"
```

### P4：Job Service、File Store、Local Runner

目标：恢复并升级异步任务执行能力。

任务：

- 实现 `internal/store.FileStore`：
  - `request.json`
  - `stdout.log`
  - `stderr.log`
  - `result.json`
  - 原子写 result：先写临时文件，再 rename。
  - 预留 `interactions.jsonl` 在同一 `<result_dir>/<job_id>/` 目录下（P9 才写入，P4 只约定路径常量与目录布局，便于后续接口零破坏接入）。
- 实现 `internal/job.Service`：
  - 创建 job id：**不能只用 `时间(秒)-seq%1000`**。当前实现进程重启后 `seq` 归零，同一秒重启前后的两个 job 会撞 id 并覆盖彼此的 `result_dir`。改为追加纳秒/短随机后缀（如 `20060102-150405-<nano后6位>` 或 `-<4位随机>`），保证跨重启唯一；创建前若目录已存在则视为冲突重试。
  - 保存 request。
  - goroutine 异步执行。
  - 管理状态、超时、取消。
  - 并发安全 map 或轻量 registry。
- 实现 `internal/runner/local`：
  - `exec.CommandContext`。
  - `Dir` 使用 safeJoin 后的 host cwd。
  - env 只来自进程环境 + Agent 配置 env。
  - stdout/stderr 直接写文件。
- 实现项目级 `allow_exec`。
- 实现 `timeout_sec` 默认值和上限。

关键测试：

- `exec` 执行 `go version` 成功。
- 命令退出码非 0 时 job 为 `failed`。
- 超时 job 为 `timeout`。
- cancel job 为 `cancelled`。
- stdout/stderr 文件可读。
- result 原子写入，不出现半截 JSON。

验收：

```bash
cd tools/dev-agent-bridge
go test ./internal/job ./internal/store ./internal/runner/local ./...
go run ./cmd/agent-bridge job run --config tmp/bridge-test.yaml --project self --agent exec -- go version
```

提交建议：

```bash
git add tools/dev-agent-bridge
git commit -m "feat(agent-bridge): add async local job runner"
```

### P5：HTTP API 与 serve

目标：提供稳定 HTTP 控制面，替换旧 `/run`。

任务：

- 实现 `internal/httpapi.Server`。
- HTTP router 使用 `github.com/gookit/rux/v2`。
- 实现 token middleware。
- 实现 `/health`。
- 实现 `/v1/projects`、`/v1/projects/{key}`。
- 实现 `/v1/agents`。
- 实现 `/v1/jobs`、`/v1/jobs/{id}`、logs、cancel。
- `serve` 命令加载配置并启动 HTTP server。
- 日志读取默认限制大小，例如最多返回最后 256KB，后续再加流式 tail。

手工验收：

```bash
cd tools/dev-agent-bridge
go run ./cmd/agent-bridge serve --config tmp/bridge-test.yaml --addr 127.0.0.1:8765 --token dev-token
```

另开终端：

```bash
curl -H "Authorization: Bearer dev-token" http://127.0.0.1:8765/health
curl -H "Authorization: Bearer dev-token" http://127.0.0.1:8765/v1/projects
curl -H "Authorization: Bearer dev-token" http://127.0.0.1:8765/v1/agents
curl -H "Authorization: Bearer dev-token" -H "Content-Type: application/json" ^
  -d "{\"project_key\":\"self\",\"agent\":\"exec\",\"runner\":\"local\",\"cmd\":[\"go\",\"version\"],\"cwd\":\".\",\"timeout_sec\":30}" ^
  http://127.0.0.1:8765/v1/jobs
```

自动测试：

- `httptest` 覆盖认证失败。
- 未知 project 返回 400/404。
- 未知 agent 返回 400。
- logs 限制返回长度。
- cancel 已完成 job 返回稳定错误或 no-op，行为在测试中固定。

提交建议：

```bash
git add tools/dev-agent-bridge
git commit -m "feat(agent-bridge): expose v1 job http api"
```

### P6：Job CLI 包装

目标：主机侧也可以不用 curl，直接用 `agent-bridge job` 操作服务。

任务：

- 实现 HTTP client。
- `job run` 支持：
  - `--project/-p`
  - `--agent/-a`
  - `--runner`
  - `--cwd`
  - `--timeout`
  - prompt 参数
  - `--` 后 raw command 传给 exec agent
- `job show` 查询状态。
- `job logs` 读取 stdout/stderr。
- `job cancel` 取消任务。
- `--server` 或 config 中 `server.addr` 决定请求地址。
- token 从 config/env 获取，不要求命令行明文传。

验收：

```bash
cd tools/dev-agent-bridge
go test ./internal/commands ./...
go run ./cmd/agent-bridge job run --config tmp/bridge-test.yaml -p self -a exec -- go version
go run ./cmd/agent-bridge job show --config tmp/bridge-test.yaml <job_id>
go run ./cmd/agent-bridge job logs --config tmp/bridge-test.yaml <job_id> --stream stdout
```

提交建议：

```bash
git add tools/dev-agent-bridge
git commit -m "feat(agent-bridge): add job cli commands"
```

### P7：Peer HTTP Runner

目标：支持主机反向把任务提交到容器内 bridge。

任务：

- 实现 `runner/peerhttp`。
- 配置支持：

```yaml
runners:
  docker-peer:
    type: peer-http
    base_url: http://127.0.0.1:8766
    token_env: CONTAINER_BRIDGE_TOKEN
```

- host bridge 收到 runner=`docker-peer` 的 job 后：
  - 校验 project 和 agent 权限。
  - 将 request 转发给 peer `/v1/jobs`。
  - 保存本地 proxy result，包含 peer job id。
  - 查询 / logs / cancel 可转发到 peer。
- 首期不解决双向文件同步；结果以 peer 返回和 peer 日志为准。

验收：

- 用两个本地端口启动两个 bridge，第二个模拟容器 peer。
- 主 bridge 提交 runner=`docker-peer`，任务在 peer bridge 执行。
- cancel 能转发。

提交建议：

```bash
git add tools/dev-agent-bridge
git commit -m "feat(agent-bridge): add peer http runner"
```

### P8：MCP Server

目标：让支持 MCP 的 Agent 可直接调用桥接能力。

任务：

- 选择 Go MCP SDK。
- 实现 `agent-bridge mcp --config ...`。
- 暴露 tools：
  - `bridge_list_projects`
  - `bridge_list_agents`
  - `bridge_run_job`
  - `bridge_get_job`
  - `bridge_tail_log`
  - `bridge_cancel_job`
- MCP 内部复用 `job.Service`，不复制执行逻辑。
- README 增加 Claude / Codex MCP 配置示例。

验收：

- stdio MCP server 能被 MCP inspector 或实际客户端列出 tools。
- `bridge_run_job` 能提交 exec smoke job。
- tool schema 字段使用 snake_case，与 HTTP API 保持一致。

提交建议：

```bash
git add tools/dev-agent-bridge
git commit -m "feat(agent-bridge): add mcp tools"
```

### P9：运行中 Agent 双向交互

目标：支持 Agent 执行中提出问题或选项，由用户通过 HTTP/MCP 回答后继续处理。

约束：

- 不进入 MVP，等 P1-P8 稳定后实施。
- 优先实现显式协议，不优先做 PTY。
- **协议口径：运行中交互走 MCP（方向 A：client 轮询 `pending_interaction` + `bridge_answer_interaction`），不引入 ACP**；ACP / PTY 实时会话托管留作后续 B 档单独评估（见 design §12.5）。
- 普通 CLI Agent 不保证原地继续；如果底层 Agent 不支持 stdin/会话恢复，只能用 follow-up job 表达下一轮。

任务：

- Job 状态新增 `pending_interaction`。
- Store 增加 `interactions.jsonl`。
- 交互事件模型：

```go
type Interaction struct {
    ID        string              `json:"id"`
    JobID     string              `json:"job_id"`
    Type      string              `json:"type"` // question | choice | confirmation
    Prompt    string              `json:"prompt"`
    Options   []InteractionOption `json:"options,omitempty"`
    Status    string              `json:"status"` // pending | answered | cancelled
    Answer    string              `json:"answer,omitempty"`
    CreatedAt int64               `json:"created_at"`
    AnsweredAt int64              `json:"answered_at,omitempty"`
}
```

- HTTP API 增加：
  - `GET /v1/jobs/{id}/interactions`
  - `POST /v1/jobs/{id}/interactions/{interaction_id}/answer`
- MCP tools 增加：
  - `bridge_get_interactions`
  - `bridge_answer_interaction`
- Agent 发起交互的首期方式：
  - 自定义 wrapper 或 MCP client 调用 bridge tool 写入 interaction。
  - stdout JSON marker 可作为后续兼容方式，但必须显式启用，避免误解析普通日志。
- 回答后的恢复策略：
  - A 档：写入运行中进程 stdin 或 wrapper channel。
  - C 档：基于原 request、历史日志摘要和 answer 创建 follow-up job。
  - B 档 PTY 单独评估，不和首期交互混做。

验收：

- 一个测试 wrapper job 能写入 `question` interaction。
- `GET interactions` 返回 pending 事件。
- `answer` 后事件状态变为 answered。
- wrapper 能读取 answer 并继续完成 job。
- MCP tool 能完成同样流程。

提交建议：

```bash
git add tools/dev-agent-bridge
git commit -m "feat(agent-bridge): add job interactions"
```

### P10：文档、脚本与清理

目标：交付可被当前工作空间和其他挂载项目使用的工具。

任务：

- README 重写：
  - 安装 / 构建。
  - 配置文件。
  - Docker 内访问主机 bridge。
  - 主机访问容器 peer bridge。
  - Codex / Claude / OpenCode 示例。
  - 安全注意事项。
- 更新 `scripts/start.ps1`、`scripts/start.sh`。
- **更新根 `CLAUDE.md` 工具引用（重命名的强依赖项，不可遗漏）**：
  - 路径 `tools/codex-bridge/README.md` → `tools/dev-agent-bridge/README.md`。
  - `http://host.docker.internal:8765` 端点说明保持或按实际更新。
  - 鉴权头从旧 `X-Bridge-Token` 改为 `Authorization: Bearer <token>`，并核对 `access token: tok-for-docker-dev` 的表述是否仍准确。
  - 经核实：全工作空间内仅根 `CLAUDE.md`（L16-18）引用了 `codex-bridge`，无其他文件需改。
- 旧二进制和旧结果目录策略：
  - 二进制未被 Git 跟踪（见 P0），仅同步 `.gitignore` 忽略名为 `/agent-bridge`、`/agent-bridge.exe`。
  - 不自动删除各项目历史 `tmp/codex-bridge`，只在 README 中说明旧目录废弃。
- 补充 changelog 或迁移说明。

整体验收：

```bash
cd tools/dev-agent-bridge
go test ./...
go run ./cmd/agent-bridge --help
go run ./cmd/agent-bridge serve --help
go run ./cmd/agent-bridge project --help
go run ./cmd/agent-bridge agent --help
go run ./cmd/agent-bridge job --help
go run ./cmd/agent-bridge project validate self --config tmp/bridge-test.yaml
```

提交建议：

```bash
git add tools/dev-agent-bridge
git commit -m "docs(agent-bridge): update usage and migration docs"
```

## 10. 测试策略

### 10.1 单元测试

| 包 | 必测点 |
|---|---|
| `internal/config` | 查找顺序、默认值、缺失配置、go-yaml 写回 |
| `internal/project` | `safeJoin`、路径逃逸、Windows path、交换目录、项目校验 |
| `internal/agent` | 模板渲染、detect、exec/cli-agent 参数校验 |
| `internal/job` | 状态流转、timeout、cancel、并发安全 |
| `internal/store` | 目录创建、日志读取、result 原子写 |
| `internal/runner/local` | 成功、失败、超时、env、cwd |
| `internal/httpapi` | 认证、参数校验、job API、日志限制 |
| `internal/commands` | gcli 参数绑定、必填参数、子命令 help |

### 10.2 集成测试

优先使用 exec agent，避免依赖本机必须安装 Codex/Claude/OpenCode：

```json
{
  "project_key": "self",
  "agent": "exec",
  "runner": "local",
  "cmd": ["go", "version"],
  "cwd": ".",
  "timeout_sec": 30
}
```

Codex/Claude/OpenCode 只做 detect smoke：

- 命令存在：返回 available。
- 命令不存在：返回 unavailable，不失败启动。

### 10.3 手工验收矩阵

| 场景 | 命令 / 操作 | 期望 |
|---|---|---|
| 无 token 启动 | `serve` 无 token 且未 allow empty | 拒绝启动 |
| 本机 token 启动 | `serve --token dev-token` | 成功监听 |
| 未授权请求 | 不带 Authorization 调 `/v1/projects` | 401 |
| 项目登记 | `project add/list/show/validate` | 配置写入且可校验 |
| exec job | `job run -a exec -- go version` | job done |
| cwd 逃逸 | cwd=`..` | 请求失败 |
| 未允许 agent | 项目未配置该 agent | 请求失败 |
| 超时 | `timeout_sec=1` 执行长命令 | job timeout |
| 取消 | running job cancel | job cancelled |
| peer runner | 主 bridge 转发到 peer | peer 执行成功 |

## 11. 安全检查

实施中每个阶段都要保持这些约束：

- 不接受请求传 host 绝对路径作为 cwd。
- 不拼接 shell 字符串；所有命令保持 argv。
- 不把 token、AK/SK、appSecret 写入日志。
- 默认不允许空 token。
- 监听地址默认 `0.0.0.0:8765`（容器经 `host.docker.internal` 反向可达的前提），安全性靠**强制 token + 内网准入**保证，而非默认绑回环；纯主机本地自用可显式收紧 `127.0.0.1`（与 design §13 一致）。
- `exec` 必须同时满足 agent 是 `exec` 且项目 `allow_exec=true`。
- 项目、Agent、runner 都必须来自配置 allowlist。
- 日志接口限制返回大小。
- job 并发要有基础限制，至少项目级配置字段预留；MVP 可先串行或全局限制。

## 12. 风险与处理

| 风险 | 表现 | 处理 |
|---|---|---|
| Windows 路径逃逸判断不严 | `D:\x`、`..\x` 绕过项目目录 | `filepath.Abs` + `Rel` + 前缀校验，补 Windows 单测 |
| gcli 多级命令参数绑定误用 | 子命令 help 或参数解析异常 | 参考 `gcli/_examples/multilevel`，命令包写小测试 |
| 子进程取消不完整 | Windows 上 cancel 后残留进程 | 先用 `CommandContext`，必要时后续补进程组处理 |
| YAML 写回覆盖人工配置 | `project add` 丢字段 | 使用 `github.com/goccy/go-yaml` 的 Node / AST 写回能力，单测覆盖未知字段保留 |
| 交换目录配置错误 | job 结果、日志、交互文件写到项目外 | `exchange_subdir` 和 `result_subdir` 都走项目内 safeJoin 校验 |
| Agent CLI 参数变化 | Claude/OpenCode 非交互参数不可用 | 通过配置模板暴露，不在 Go 代码写死 |
| 日志过大 | HTTP 响应占用过高 | 默认 tail 限制，完整日志通过文件查看 |
| peer runner 结果同步复杂 | 主机和容器 result_dir 不一致 | P7 明确 proxy 语义，首期以 peer API 查询为准 |

## 13. 交付顺序

建议按阶段提交，不一次性堆大提交：

| 阶段 | 交付物 | 是否必须 |
|---|---|---|
| P1 | 目录重命名 + gcli skeleton | 必须 |
| P2 | Config + Project Registry | 必须 |
| P3 | Agent Registry | 必须 |
| P4 | Job + Store + Local Runner | 必须 |
| P5 | HTTP API + serve | 必须 |
| P6 | Job CLI | 必须 |
| P7 | Peer HTTP Runner | 推荐 |
| P8 | MCP Server | 可延后 |
| P9 | 运行中 Agent 双向交互 | 可延后 |
| P10 | README / scripts / 清理 | 必须 |

MVP 完成标准：P1-P6 + P10 完成。P7/P8/P9 可作为后续增强，但接口命名、配置结构和交换目录要在 MVP 中预留，避免再破坏配置。

## 14. 最终验收标准

MVP 结束时必须满足：

- `tools/dev-agent-bridge` 下无旧 `codex-bridge` 命名残留，除迁移说明外。
- `go test ./...` 通过。
- `agent-bridge serve --help`、`project --help`、`agent --help`、`job --help` 可用。
- 配置文件可登记至少两个不同 host path 项目。
- HTTP `/v1/jobs` 可以在指定项目 cwd 执行 exec smoke job。
- `codex`、`claude`、`opencode` 都能作为配置项出现；未安装时 detect 返回 unavailable 而不是启动失败。
- 旧 `/run` 和 `/result/{id}` 不存在。
- 新结果目录为 `tmp/dev-agent-bridge`。
- 项目数据交换目录默认是 `tmp`，可通过 `exchange_subdir` 配置覆盖。
- README 说明 Docker 内如何通过 `host.docker.internal` 调用主机 bridge。
- README 说明 peer-http 反向调用容器 bridge 的推荐形态。
- 根 `CLAUDE.md` 工具引用已同步更新（路径 `tools/dev-agent-bridge/README.md`、鉴权头 `Authorization: Bearer`），全工作空间无残留 `tools/codex-bridge` 引用。

## 15. 开工前确认

已确认（用户）：

- 配置文件规范默认位置：用户级 `~/.config/dev-agent-bridge/config.yaml`。
- 容器反向调用优先 peer bridge（P7）；`docker-exec` 不进 MVP。
- 结果目录默认各项目 `tmp/`；全局 store 作可选开关 `storage.root`，默认关闭。
- 运行中交互走 MCP（不引入 ACP）；重命名后同步更新根 `CLAUDE.md`（见 P10）。

后续进入代码实施前，仍需人工确认两点：

1. 是否直接重命名目录为 `tools/dev-agent-bridge`，并删除旧 `tools/codex-bridge` 目录。
2. 是否同意 MVP 先交付 P1-P6 + P10，P7 peer-http、P8 MCP 和 P9 运行中双向交互可在结构预留后分开落地。
