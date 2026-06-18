# dev-agent-bridge 多 Agent 多项目桥接工具设计

文档修订记录：

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-16 | Codex | 初版：基于 `tools/codex-bridge` 现状提出通用化改造方案，覆盖命名、目录结构、多 CLI Agent、多项目、反向 Docker 交互与 MCP 可行性 |
| v0.2 | 2026-06-16 | Codex | 按反馈调整：设计归属下沉到工具内；无需兼容旧 `codex-bridge` 接口/路径；多项目范围扩展为任意 Docker 挂载目录；正式 CLI 建议改为 `agent-bridge`，短别名 `dab` |
| v0.3 | 2026-06-16 | Codex | 补充 CLI 设计：flag / 子命令解析采用 `github.com/gookit/goutil/cflag/capp`，参考 `doccli`；服务启动使用 `serve` 子命令；增加 `project` 子命令管理项目配置 |
| v0.4 | 2026-06-16 | Codex | 调整 CLI 技术选型：命令设计保持不变，改用 `github.com/gookit/gcli/v3` 支持多级子命令；不采用 `capp` |
| v0.5 | 2026-06-16 | Codex | 调整依赖选型：YAML 使用 `github.com/goccy/go-yaml`，HTTP 路由使用 `github.com/gookit/rux/v2`；配置增加项目数据交换目录，默认项目下 `tmp`；补充运行中 Agent 双向交互边界 |
| v0.6 | 2026-06-16 | Claude | 审核修订：①阶段编号收敛，详细分期以实施计划 plan §9 为准（消除本文 P1-P6 与 plan P0-P10 冲突）；②`NewApp` 归 `internal/commands/app.go`，删除 `internal/app` 包；③模板变量首期即支持 `{{prompt}}/{{cwd}}/{{job_id}}/{{result_dir}}`，与 plan P3 对齐；④`job` 子命令纳入 MVP；⑤澄清监听地址默认 `0.0.0.0`+强制 token，`127.0.0.1` 为可选收紧（消除 §13 与 §14 矛盾）；⑥新增 §12.5 MCP/ACP 选型边界；⑦补充重命名需同步更新根 `CLAUDE.md` 引用 |
| v0.7 | 2026-06-16 | Claude | 落实用户确认：①配置文件规范默认位置为用户级 `~/.config/dev-agent-bridge/config.yaml`；②容器反向调用优先 peer bridge，`docker-exec` 不进 MVP；③结果目录默认各项目 `tmp/`，新增可选 `storage.root` 全局 store 开关（§9.3 / §14）；同步 §16 待确认项收敛 |
| v0.8 | 2026-06-16 | Claude | 拆出 Web 控制台子设计 [`2026-06-16-web-console-design.md`](./2026-06-16-web-console-design.md)（serve 内嵌只读监控+实时日志+取消+运行中交互）；本文 §10 API、§12.4 运行中交互为其上游依赖，新增 list/SSE/cookie 端点在子设计中定义 |

> 文档类型：工具内设计文档。本文只围绕 `tools/codex-bridge` 这个开发辅助工具自身做设计和调整，不要求也不默认修改任何被操作项目代码。
> "给其他项目使用" 指任意被 Docker 挂载、且在本工具配置中显式登记的目录 / 项目；不限于当前 `example-project` 工作空间内的子项目。
> **子设计**：Web 控制台见 [`2026-06-16-web-console-design.md`](./2026-06-16-web-console-design.md)（实时监控/日志/交互的浏览器界面，依赖本文 §10 / §12.4）。
> **先读总览**：术语与两条正交轴（接入入口 / 执行位置）见 [`architecture-overview.md`](./architecture-overview.md)。

## 1. 背景

当前 `tools/codex-bridge` 是一个跑在主机上的 HTTP 桥，主要解决：

- Docker 容器内 Agent 无法直接访问主机侧 CLI、Host 网络、部分构建工具的问题。
- 容器内通过 `http://host.docker.internal:8765/run` 提交任务。
- 主机侧执行 `codex exec` 或任意 `exec` 命令。
- 结果通过 `<project>/tmp/codex-bridge/<id>/` 共享目录回传。

现状代码集中在单个 `main.go`，且有几个明显限制：

- 名称绑定 Codex，后续支持 Claude、OpenCode 等 CLI Agent 会产生语义偏差。
- Agent 调用逻辑写死为 `mode=codex`，其他 Agent 只能退化为 `mode=exec`。
- 启动时只能指定一个 `-project`，请求无法选择项目，难以作为多项目常驻工具。
- 目录结构未按 Go 工具常规拆分，后续加配置、任务存储、MCP、远端 runner 会让单文件失控。
- 当前方向是 Docker 内 Agent 调主机；缺少主机反向调用 Docker 内 Agent CLI 的标准入口。

当前工具仅在本项目内使用，因此重构时不需要保留旧名称、旧 HTTP 接口、旧结果目录或 wrapper。

## 2. 项目命名

推荐项目名改为 **`dev-agent-bridge`**，正式 CLI 使用 **`agent-bridge`**，高频短别名使用 **`dab`**。

理由：

- `dev` 明确它是开发/联调工具，不是生产业务服务。
- `agent` 覆盖 Codex、Claude、OpenCode 以及后续任意 CLI Agent。
- `bridge` 保留当前核心定位：跨运行环境、跨项目、跨工具触发任务。
- `agent-bridge` 作为正式命令比 `dev-agent-bridge` 更短，仍能表达用途。
- `dab` 适合手敲高频命令，但可读性不足，只建议作为 alias / symlink。

命名决策：

| 项 | 名称 | 说明 |
|---|---|---|
| 项目 / 目录名 | `dev-agent-bridge` | 工具项目正式名称，表达开发态 Agent 桥接定位 |
| Go module | `dev-agent-bridge` 或内部域名模块名 | 后续独立仓库化时再按仓库路径调整 |
| 正式 CLI | `agent-bridge` | 默认安装的二进制名 |
| 短别名 | `dab` | 可选 alias / symlink，便于高频手敲 |
| 旧名称 | 不保留 | 当前没有外部使用方，无需兼容 `codex-bridge` |

落地建议：

- 目录从 `tools/codex-bridge` 重命名为 `tools/dev-agent-bridge`。
- 二进制名使用 `agent-bridge`。
- 可额外生成 `dab` 作为同一二进制的短别名。
- 数据交换目录默认为项目下 `tmp`，结果目录从 `tmp/codex-bridge` 改为 `tmp/dev-agent-bridge`。
- 旧 `/run`、`/result/{id}`、`tmp/codex-bridge` 不保留。

命令形态：

```bash
agent-bridge serve --config ~/.config/dev-agent-bridge/config.yaml
agent-bridge project list
agent-bridge project add example-project --host-path D:/work/inhere/example-project --container-path /workspace
agent-bridge agent list
agent-bridge job run --project example-project --agent codex --cwd tools/dev-agent-bridge "检查当前设计"
agent-bridge mcp --config ~/.config/dev-agent-bridge/config.yaml

dab job run -p other-mounted-project -a claude --cwd . "运行测试并总结失败点"
```

## 3. 总体思路

将当前工具从"Codex HTTP 包装器"升级为"本地开发任务路由器"。

核心抽象分三层：

```txt
Client(容器/主机/Agent/MCP)
  -> HTTP API / MCP Tools
  -> Job Router
  -> Runner(local | peer-http | docker-exec)
  -> Adapter(codex | claude | opencode | exec | custom)
```

关键原则：

- **项目先选择，再执行**：请求必须带 `project_key`，服务端按配置解析 host 路径、容器路径和结果目录。
- **Agent 配置化**：不要在代码里写死 Codex/Claude/OpenCode 参数，通过配置声明命令模板。
- **执行不走 shell 字符串拼接**：命令必须保持 argv 数组，避免转义和注入问题。
- **异步任务模型保留**：提交任务立即返回 `job_id`，日志和结果落文件，HTTP 只做控制面。
- **本地 runner 与远端 runner 一致**：主机执行、Docker 内执行、其他机器执行都走相同 Job/Result 协议。
- **只改工具自身**：本工具的重构不触碰被登记项目的源码；只有用户提交的具体 job 命令 / Agent 任务才可能在目标项目内产生变更。

## 4. 范围

本次增强设计覆盖：

- 项目重命名与 CLI 命名。
- Go 代码结构规范化。
- 多 CLI Agent 支持。
- 任意挂载项目配置与请求选择。
- 主机到 Docker 的反向调用方案。
- MCP 作为可选入口的边界与实现建议。
- API 和配置草案。

不覆盖：

- 具体实现代码。
- CLI Agent 的交互式 TUI 托管。
- 生产环境部署。
- Agent 输出语义解析和自动评审质量判断。
- 被操作项目的业务代码调整。

## 5. 架构设计

### 5.1 运行拓扑

正向：Docker 内 Agent 调主机工具。

```txt
Docker container
  curl / MCP client
    -> host.docker.internal:8765
      -> dev-agent-bridge(host)
        -> local runner(host)
          -> codex / claude / opencode / exec
        -> write host project tmp
  read shared tmp result
```

反向：主机调 Docker 内 Agent 或容器内命令。

推荐支持两种方式：

```txt
方案 A: peer bridge
host dev-agent-bridge
  -> peer-http runner
    -> dev-agent-bridge(container)
      -> local runner(container)
        -> claude / opencode / exec

方案 B: docker-exec runner
host dev-agent-bridge
  -> docker exec -i <container> <agent command>
```

推荐优先实现 **方案 A peer bridge**，原因是协议统一、日志/结果模型一致、容器内路径解析更可靠。`docker-exec` 适合作为没有容器内常驻服务时的兜底。

### 5.2 组件职责

| 组件 | 职责 |
|---|---|
| HTTP API | 接收任务、查询结果、取消任务、读取日志、列出项目和 Agent |
| MCP Server | 可选入口，把桥接能力暴露为 MCP tools |
| Project Registry | 管理项目 key、host 路径、container 路径、结果目录、安全策略 |
| Agent Registry | 管理 Agent 定义、命令模板、能力、默认参数 |
| Job Service | 生成 job_id、保存 request/result、管理状态和超时 |
| Runner | 负责在哪里执行：本机、peer HTTP、docker exec |
| Adapter | 负责如何调用某个 CLI Agent：Codex、Claude、OpenCode、自定义 |
| File Store | 管理 request.json、stdout.log、stderr.log、result.json 的原子写入 |

## 6. 代码结构

建议结构：

```txt
tools/dev-agent-bridge/
|- cmd/
|  `- agent-bridge/
|     `- main.go
|- internal/
|  |- commands/
|  |  |- app.go
|  |  |- serve.go
|  |  |- project.go
|  |  |- agent.go
|  |  |- job.go
|  |  `- mcp.go
|  |- config/
|  |  |- config.go
|  |  `- loader.go
|  |- httpapi/
|  |  |- server.go
|  |  |- job_handler.go
|  |  |- project_handler.go
|  |  `- agent_handler.go
|  |- mcp/
|  |  |- server.go
|  |  `- tools.go
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
|  |  |- peerhttp/
|  |  |  `- runner.go
|  |  `- dockerexec/
|  |     `- runner.go
|  |- store/
|  |  |- store.go
|  |  `- filestore.go
|  `- security/
|     |- auth.go
|     `- policy.go
|- configs/
|  `- bridge.example.yaml
|- scripts/
|  |- start.ps1
|  `- start.sh
|- README.md
`- go.mod
```

拆分依据：

- `cmd/agent-bridge/main.go` 只调用 `commands.NewApp(version)` 并 `app.Run(nil)`，不含装配逻辑。
- `internal/commands/app.go` 负责创建 `gcli.App`、设置名称 / 版本 / 描述并注册各子命令；不再单设 `internal/app` 包。
- `internal/commands` 其余每个文件暴露一个 `*gcli.Command` 构造函数；一级命令通过 `app.Add(...)` 注册，多级命令通过 `Command.Subs` 注册。
- `internal/httpapi` 不直接拼命令，只调用 `job.Service`。
- `internal/agent` 只负责命令模板与 Agent 能力。
- `internal/runner` 只负责执行位置。
- `internal/project` 集中做路径安全校验，避免多处重复实现 `safeJoin`。

CLI 依赖约定：

- flag / 子命令解析使用 `github.com/gookit/gcli/v3`。
- 选择 `gcli` 而不是 `goutil/cflag/capp` 的原因是：`agent-bridge project add/list/show`、`job logs/cancel` 需要多级子命令，`capp` 不适合该结构。
- 命令参数在 `gcli.Command.Config` 中绑定，使用 `StrOpt` / `BoolOpt` / `IntOpt` / `VarOpt`，位置参数用 `AddArg`。
- CLI 错误优先使用 `c.NewErrf(...)` 或返回普通 `error`。
- YAML 配置读写使用 `github.com/goccy/go-yaml`。
- HTTP server / 路由使用 `github.com/gookit/rux/v2`。
- 通用文件、字符串、JSON 辅助优先使用 `github.com/gookit/goutil` 系列包；不足时再封装标准库。
- 单元测试断言使用 `github.com/gookit/goutil/x/assert`。

CLI 入口形态参考 `D:\work\inhere\my-tools-dev\gookit2\gcli`：

```go
// internal/commands/app.go，与各子命令同包，构造函数无需 commands. 前缀
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

`serve` 命令示例：

```go
func NewServeCmd() *gcli.Command {
    return &gcli.Command{
        Name:    "serve",
        Aliases: []string{"s"},
        Desc:    "start HTTP bridge server",
        Config: func(c *gcli.Command) {
            c.StrOpt(&serveOpts.Config, "config", "c", "", "config file path")
            c.StrOpt(&serveOpts.Addr, "addr", "", "", "listen address")
            c.StrOpt(&serveOpts.Token, "token", "", "", "override auth token")
            c.BoolOpt(&serveOpts.AllowEmptyToken, "allow-empty-token", "", false, "allow empty token")
        },
        Func: handleServe,
    }
}
```

`project` 多级子命令示例：

```go
func NewProjectCmd() *gcli.Command {
    return &gcli.Command{
        Name:    "project",
        Aliases: []string{"p"},
        Desc:    "manage allowed project mappings",
        Subs: []*gcli.Command{
            NewProjectListCmd(),
            NewProjectShowCmd(),
            NewProjectAddCmd(),
            NewProjectRemoveCmd(),
            NewProjectValidateCmd(),
        },
    }
}

func NewProjectAddCmd() *gcli.Command {
    return &gcli.Command{
        Name: "add",
        Desc: "add a project mapping",
        Config: func(c *gcli.Command) {
            c.AddArg("key", "project key", true)
            c.StrOpt(&projectAddOpts.HostPath, "host-path", "", "", "host project path")
            c.StrOpt(&projectAddOpts.ContainerPath, "container-path", "", "", "container mount path")
            c.StrOpt(&projectAddOpts.DefaultAgent, "default-agent", "", "", "default agent")
            c.VarOpt(&projectAddOpts.AllowedAgents, "allow-agent", "", "allowed agent, repeatable")
            c.VarOpt(&projectAddOpts.AllowedRunners, "allow-runner", "", "allowed runner, repeatable")
            c.BoolOpt(&projectAddOpts.AllowExec, "allow-exec", "", false, "allow raw exec jobs")
            c.BoolOpt(&projectAddOpts.Force, "force", "f", false, "overwrite existing project")
        },
        Func: handleProjectAdd,
    }
}
```

## 7. 多 Agent 支持

### 7.1 Agent 模型

把当前 `mode=codex` 改成：

```json
{
  "project_key": "example-project",
  "agent": "codex",
  "kind": "agent",
  "prompt": "在当前项目执行测试并总结结果",
  "cwd": "java-biz-dev/hyy-service-inspect-vision",
  "timeout_sec": 1200
}
```

直接命令执行作为内置 Agent：

```json
{
  "project_key": "example-project",
  "agent": "exec",
  "kind": "exec",
  "cmd": ["mvn", "clean", "test"],
  "cwd": "java-biz-dev/hyy-service-inspect-vision"
}
```

请求模型直接采用 `project_key + agent + runner`，不再保留旧 `mode=codex|exec` 字段。

### 7.2 配置化 Agent

不在代码里固化 `claude` / `opencode` 的具体参数。不同 CLI 的非交互参数差异较大，应通过配置模板描述。

示例：

```yaml
agents:
  codex:
    type: cli-agent
    command: codex
    args:
      - "-s"
      - "danger-full-access"
      - "-a"
      - "never"
      - "exec"
      - "{{prompt}}"
    detect:
      command: codex
      args: ["--version"]

  claude:
    type: cli-agent
    command: claude
    args:
      - "{{prompt}}"
    env:
      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1"
    detect:
      command: claude
      args: ["--version"]

  opencode:
    type: cli-agent
    command: opencode
    args:
      - "{{prompt}}"
    detect:
      command: opencode
      args: ["--version"]

  exec:
    type: exec
    allow_raw_cmd: true
```

说明：

- 上面 `claude` 和 `opencode` 的参数仅作为占位示例，真实参数以本机 CLI 版本为准。
- 模板变量首期即支持 `{{prompt}}`、`{{cwd}}`、`{{job_id}}`、`{{result_dir}}`（与 plan P3 一致）；未用到的变量在渲染时按空串或跳过处理，后续再按需扩展。
- Agent 检测只判断命令是否存在和版本是否可读，不代表任务一定能成功。

### 7.3 任意 CLI Agent

支持"任意存在的 CLI Agent"的关键不是为每个 Agent 写 Go 代码，而是提供 `custom` 模板：

```yaml
agents:
  my-agent:
    type: cli-agent
    command: my-agent
    args: ["run", "--cwd", "{{cwd}}", "--prompt", "{{prompt}}"]
```

只要该 Agent 能以非交互方式接收 prompt 并输出到 stdout/stderr，就可以接入。

对于必须交互式 TUI 的 Agent，首期不承诺支持。后续可增加 pseudo-terminal runner，但它会显著增加取消、日志、输入续写和跨平台复杂度。

## 8. CLI 子命令设计

CLI 使用 `github.com/gookit/gcli/v3`，整体风格参考 `D:\work\inhere\my-tools-dev\gookit2\gcli`：

- `cmd/agent-bridge/main.go` 只创建 `gcli.App` 并注册命令。
- `internal/commands/*.go` 定义命令和 flag。
- 多级子命令用 `Command.Subs` 表达，例如 `project -> add/list/show/remove/validate`、`job -> run/show/logs/cancel`。
- 每个命令的参数在 `Config func(c *gcli.Command)` 中绑定。
- 实际业务逻辑放到 `internal/project`、`internal/agent`、`internal/job` 等包，命令层不写业务逻辑。
- 每个命令保留短 alias，但正式文档优先使用完整命令。

### 8.1 serve

`serve` 负责启动 HTTP bridge server，是默认常驻入口。

```bash
agent-bridge serve --config ~/.config/dev-agent-bridge/config.yaml
agent-bridge serve -c configs/bridge.yaml --addr 0.0.0.0:8765
dab s -c configs/bridge.yaml
```

建议参数：

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-c, --config` | 自动查找 | 配置文件路径 |
| `--addr` | 配置文件 `server.addr` | 覆盖监听地址 |
| `--token` | 空 | 覆盖 token；优先建议使用 `token_env` |
| `--allow-empty-token` | `false` | 明确允许空 token，仅本机可信环境使用 |

配置查找顺序（命中即止）：

1. `--config` 指定路径。
2. 环境变量 `AGENT_BRIDGE_CONFIG`。
3. 当前目录 `.dev-agent-bridge.yaml`。
4. 用户级 `~/.config/dev-agent-bridge/config.yaml`。

**规范默认位置为第 4 项用户级 `~/.config/dev-agent-bridge/config.yaml`**（用户确认）：前 3 项为按需覆盖手段；当四级均未命中时，`project add` 自动创建用户级配置（见 §8.2）。

### 8.2 project

`project` 子命令用于查看和维护配置中的项目登记。它只修改工具配置，不修改目标项目代码。

命令建议：

```bash
agent-bridge project list
agent-bridge project show example-project
agent-bridge project add example-project \
  --host-path D:/work/inhere/example-project \
  --container-path /workspace \
  --default-agent codex \
  --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner local --allow-runner docker-peer
agent-bridge project remove example-project
```

短 alias：

```bash
dab p list
dab p add project-a --host-path D:/work/other/project-a --container-path /workspace/project-a
```

建议子命令：

| 命令 | 用途 |
|---|---|
| `project list` | 列出所有已登记项目 key、host path、container path、默认 Agent |
| `project show <key>` | 查看单个项目完整配置 |
| `project add <key>` | 新增或覆盖项目配置 |
| `project remove <key>` | 删除项目配置 |
| `project validate <key>` | 校验路径存在、交换目录 / 结果目录可写、允许的 Agent / runner 存在 |

`project add` 参数：

| 参数 | 必填 | 说明 |
|---|---|---|
| `<key>` | 是 | 项目 key，建议 lowercase-kebab |
| `--host-path` | 是 | 主机侧项目路径 |
| `--container-path` | 否 | 容器内挂载路径；没有容器映射时可空 |
| `--exchange-subdir` | 否 | 项目内数据交换目录，默认 `tmp` |
| `--result-subdir` | 否 | 相对交换目录的结果目录，默认 `dev-agent-bridge` |
| `--default-agent` | 否 | 默认 Agent |
| `--allow-agent` | 否，可重复 | 项目允许的 Agent |
| `--allow-runner` | 否，可重复 | 项目允许的 runner |
| `--allow-exec` | 否 | 是否允许原始命令执行 |
| `--max-concurrent-jobs` | 否 | 项目级并发限制 |

写入规则：

- 默认写回当前使用的配置文件。
- 如果配置文件不存在，创建用户级 `~/.config/dev-agent-bridge/config.yaml`。
- 写入前保留未知顶层字段，避免破坏人工扩展配置。
- `host_path` 写入前转绝对路径并做存在性校验。
- `container_path` 只做格式校验，不强制当前主机可访问。
- `exchange_subdir` 和 `result_subdir` 必须是项目内相对路径，不能逃逸项目目录。
- 同名 project 默认拒绝覆盖；可用 `--force` 覆盖。

### 8.3 agent

`agent` 子命令用于查看配置中的 Agent 以及本机检测状态。

```bash
agent-bridge agent list
agent-bridge agent detect
agent-bridge agent show codex
```

首期只读，不负责编辑 Agent 配置。Agent 模板比项目配置更容易写错，建议先通过配置文件手工维护，后续再加 `agent add`。

### 8.4 job

`job` 子命令是 HTTP API 的本地 CLI 包装，便于主机侧直接提交任务。

```bash
agent-bridge job run --project example-project --agent codex --cwd . "检查当前设计"
agent-bridge job show 20260616-153000-001
agent-bridge job logs 20260616-153000-001 --stream stdout
agent-bridge job cancel 20260616-153000-001
```

`job` 子命令纳入 MVP（plan P6），是 HTTP API 的本地 CLI 包装，便于主机侧无需 curl 即可自测与提交任务；HTTP `/v1/jobs`（plan P5）先于 `job` CLI 落地。`run` 命名固定为 `job run` 子命令，不占用顶层 `run`，避免与其他能力冲突。

### 8.5 mcp

`mcp` 作为独立子命令运行 stdio MCP server：

```bash
agent-bridge mcp --config ~/.config/dev-agent-bridge/config.yaml
```

MCP 命令不启动 HTTP server，内部直接复用同一套 config / project / job service。

## 9. 多项目支持

这里的"多项目"不是当前 workspace 内的子项目概念，而是任意被 Docker 挂载并在配置中显式登记的目录。工具只根据 `project_key` 找到 host 路径、容器路径和安全策略，不关心目标目录是否属于当前 Git 仓库。

```txt
任意 host 目录
  + 可选 container 目录映射
  + allowed_agents / allowed_runners / result_dir 策略
  -> 一个 project_key
```

### 9.1 项目注册

启动时不再只有一个 `-project`，而是加载项目注册表：

```yaml
server:
  addr: 0.0.0.0:8765
  token_env: BRIDGE_TOKEN

storage:
  default_exchange_subdir: tmp
  default_result_subdir: dev-agent-bridge

projects:
  example-project:
    host_path: D:/work/inhere/example-project
    container_path: /workspace
    exchange_subdir: tmp
    result_subdir: dev-agent-bridge
    default_agent: codex
    allowed_agents: [codex, claude, opencode, exec]
    allow_exec: true

  other-mounted-project:
    host_path: D:/work/other/project-a
    container_path: /workspace/project-a
    exchange_subdir: tmp
    result_subdir: dev-agent-bridge
    default_agent: codex
    allowed_agents: [codex, claude, exec]
    allow_exec: true

  temp-debug-repo:
    host_path: E:/tmp/debug-repo
    container_path: /mnt/debug-repo
    exchange_subdir: tmp
    result_subdir: dev-agent-bridge
    default_agent: opencode
    allowed_agents: [opencode, exec]
    allow_exec: true
```

### 9.2 请求路径解析

请求必须传 `project_key`：

```json
{
  "project_key": "other-mounted-project",
  "agent": "codex",
  "cwd": ".",
  "prompt": "检查当前项目的健康检查接口"
}
```

路径规则：

- `cwd` 只能是项目内相对路径。
- 不接受请求直接传 host 绝对路径。
- `project.host_path + cwd` 必须经过统一 `safeJoin`。
- 如果任务由 peer runner 转发到容器内，则使用 `project.container_path + cwd`。

### 9.3 数据交换目录与结果目录

每个项目都有一个数据交换目录，用于容器、主机和不同 Agent 之间交换 job 请求、日志、结果、附件和后续交互数据。默认交换目录是项目下 `tmp`：

```txt
<project.host_path>/tmp/
```

结果目录默认是交换目录下的 `dev-agent-bridge/<job_id>`：

```txt
<project.host_path>/tmp/dev-agent-bridge/<job_id>/
```

好处：

- 容器和主机可继续通过项目共享盘交换结果。
- 不同项目互不污染。
- 工具不需要在目标项目内落额外代码。
- 后续运行中交互也可以复用同一个 exchange 根，例如 `<exchange_dir>/dev-agent-bridge/<job_id>/interactions.jsonl`。

全局 store 作为**首期可配置开关**（默认关闭，用户确认）：

```yaml
storage:
  root: D:/work/.dev-agent-bridge/jobs   # 可选；留空=默认写各项目 tmp/
```

解析规则：

- 未设 `storage.root`（默认）：结果目录 `<project.host_path>/<exchange_subdir>/<result_subdir>/<job_id>`，容器经共享盘可直接读 `result.json` / 日志，满足正向用例。
- 设 `storage.root`：结果目录 `<storage.root>/<project_key>/<job_id>`，集中存放、跨项目统一清理。

权衡（务必在 README 点明）：全局 store 不在项目共享盘，**容器侧无法经共享盘读结果**，只能走 HTTP `/v1/jobs/{id}` 回查。因此全局 store 仅推荐用于纯主机侧或 peer runner 场景；需要容器经共享盘直读结果时保持默认项目 `tmp/`。

## 10. HTTP API 草案

建议 API 使用 `/v1` 前缀；旧 `/run`、`/result/{id}` 不保留。
HTTP server / router 使用 `github.com/gookit/rux/v2`，处理函数内部只做参数解析、认证上下文和响应编码，业务逻辑仍调用 `job.Service`、`project.Registry`、`agent.Registry`。

| 方法 | 路径 | 用途 |
|---|---|---|
| `GET` | `/health` | 进程健康 |
| `GET` | `/v1/projects` | 列出可用项目 |
| `GET` | `/v1/agents` | 列出可用 Agent 和检测状态 |
| `POST` | `/v1/jobs` | 创建异步任务 |
| `GET` | `/v1/jobs/{id}` | 查询任务状态 |
| `GET` | `/v1/jobs/{id}/logs/stdout` | 读取 stdout |
| `GET` | `/v1/jobs/{id}/logs/stderr` | 读取 stderr |
| `POST` | `/v1/jobs/{id}/cancel` | 取消任务 |

任务请求：

```json
{
  "project_key": "example-project",
  "agent": "codex",
  "runner": "local",
  "prompt": "运行相关测试并总结失败点",
  "cmd": null,
  "cwd": "tools/dev-agent-bridge",
  "timeout_sec": 900,
  "title": "bridge self test"
}
```

任务结果：

```json
{
  "id": "20260616-153000-001",
  "project_key": "example-project",
  "agent": "codex",
  "runner": "local",
  "status": "done",
  "exit_code": 0,
  "cwd": "D:/work/inhere/example-project/tools/dev-agent-bridge",
  "result_dir": "tmp/dev-agent-bridge/20260616-153000-001",
  "started_at": 1781585400,
  "ended_at": 1781585520
}
```

## 11. 反向和 Docker 内 Agent CLI 交互

结论：可以做，但不建议把它理解成"主机直接控制容器内 Agent 会话"。更稳的抽象是 **主机向容器内 runner 提交异步任务**。

### 11.1 方式 A：容器内也运行 dev-agent-bridge

容器内启动：

```txt
agent-bridge serve --addr 0.0.0.0:8766 --config /workspace/.bridge/container.yaml
```

主机桥配置 peer：

```yaml
runners:
  docker-claude:
    type: peer-http
    base_url: http://localhost:8766
    token_env: CONTAINER_BRIDGE_TOKEN
```

主机请求：

```json
{
  "project_key": "example-project",
  "runner": "docker-claude",
  "agent": "claude",
  "prompt": "在容器内运行 mvn test 并总结",
  "cwd": "java-biz-dev/hyy-service-inspect-vision"
}
```

优点：

- 主机和容器使用同一套 Job API。
- 容器内路径、环境变量、工具链由容器内桥自己解析。
- 日志和取消模型一致。

缺点：

- 容器内需要多起一个小服务。
- 需要处理端口映射或 Docker 网络访问。

### 11.2 方式 B：主机使用 docker exec

配置：

```yaml
runners:
  dev-container:
    type: docker-exec
    container: example-project-dev
    workdir_prefix: /workspace
    allowed_agents: [claude, exec]
```

优点：

- 不需要容器内常驻 bridge。
- 对临时容器更方便。

缺点：

- 依赖主机 Docker CLI。
- Windows/PowerShell/TTY/编码问题更多。
- 取消任务和日志流要自己处理 `docker exec` 子进程。

建议：首期实现 peer-http，docker-exec 放 P4 之后。

## 12. MCP 可行性

### 12.1 MCP 适合解决什么

MCP 适合作为 **Agent 调用桥接工具的标准入口**。也就是把 dev-agent-bridge 暴露成 MCP server，提供 tools：

| MCP tool | 对应能力 |
|---|---|
| `bridge_list_projects` | 列出项目 |
| `bridge_list_agents` | 列出 Agent |
| `bridge_run_job` | 提交异步任务 |
| `bridge_get_job` | 查询任务 |
| `bridge_tail_log` | 读取日志尾部 |
| `bridge_cancel_job` | 取消任务 |

这样 MCP-capable 的 Agent 可以不手写 curl，直接调用工具。

### 12.2 MCP 不适合直接承诺什么

MCP 不是所有 CLI Agent 的通用远程控制协议。Codex、Claude、OpenCode CLI 是否能作为 MCP server 暴露、是否支持非交互任务、是否支持继续会话，取决于各自实现。

因此本设计中 MCP 的边界是：

- dev-agent-bridge 可以作为 MCP server，对外暴露"提交任务/查日志/查结果"工具。
- dev-agent-bridge 不假设 Codex/Claude/OpenCode 本身都是 MCP server。
- 如果某个容器内 Agent 只提供 CLI，就仍通过 Agent Adapter 调 CLI。
- 如果某个容器内能力本身就是 MCP server，后续可新增 `mcp-client runner` 转发工具调用，但不作为首期目标。

### 12.3 MCP 运行形态

建议支持两种：

```txt
agent-bridge serve --config bridge.yaml
agent-bridge mcp --config bridge.yaml
```

- `serve` 提供 HTTP API。
- `mcp` 提供 stdio MCP server，给 Claude Desktop、Claude Code 等支持 MCP 的客户端挂载。

后续如果需要远程 MCP，可再评估 Streamable HTTP MCP，但首期 stdio 足够。

### 12.4 运行中 Agent 双向交互

问题：是否可以通过 `serve` 和正在运行的 Agent 进行双向交互，例如 Agent 执行中遇到问题，需要询问用户或让用户选择。

结论：可以设计成桥接能力，但不能假设普通 CLI Agent 天然支持。需要把 job 模型从"提交后等待完成"扩展为"可挂起、可提问、可回答、可继续"。

推荐抽象：

```txt
running job
  -> agent emits question / choice request
  -> bridge records interaction and sets status=pending_interaction
  -> client reads pending interaction via HTTP/MCP
  -> user answers via HTTP/MCP
  -> bridge resumes job or starts follow-up turn
```

建议新增能力：

| 能力 | 说明 |
|---|---|
| `GET /v1/jobs/{id}/interactions` | 读取交互事件列表 |
| `POST /v1/jobs/{id}/interactions/{interaction_id}/answer` | 回答问题或选择项 |
| MCP `bridge_get_interactions` | MCP 客户端读取待处理交互 |
| MCP `bridge_answer_interaction` | MCP 客户端提交回答 |
| 状态 `pending_interaction` | job 等待外部输入 |

底层实现分三档：

| 档位 | 能力 | 适用场景 |
|---|---|---|
| A：显式协议 | Agent 通过 MCP tool / stdout JSON 明确发出 `ask_user` 事件 | 最稳，适合自定义 Agent 或 wrapper |
| B：stdin/PTY 会话 | bridge 持有子进程 stdin 或 PTY，回答时写回进程 | 可支持交互式 CLI，但跨平台复杂 |
| C：续跑式 follow-up | 当前 job 结束或挂起后，用原 prompt + answer 创建下一轮 job | 适合不支持会话续写的 CLI Agent |

首期不建议把双向交互放进 MVP。MVP 仍然以异步非交互 job 为底座；但接口和存储可以预留 `interactions.jsonl`，后续优先落地 A 档。原因是 A 档不依赖 TTY，HTTP、MCP、peer-http runner 都能复用；B 档会引入 Windows PTY、取消、输入回显、编码和安全边界问题。

如果 Agent 是通过 MCP 接入 bridge 的客户端，最自然的交互方式是：Agent 调用 `bridge_run_job` 后轮询 `bridge_get_job` / `bridge_get_interactions`；当 bridge 返回 `pending_interaction`，由当前 Agent 把问题呈现给用户，再调用 `bridge_answer_interaction`。这属于"Agent 与 bridge 双向协调"，不是 bridge 直接控制 Agent 的内部会话。

### 12.5 MCP vs ACP：运行中交互选型边界

常见疑问：运行中双向交互到底用 MCP 还是必须用 ACP（Agent Client Protocol）。结论：**本工具用 MCP 即可，不引入 ACP**。两者解决的不是同一层问题：

| 协议 | 解决的层面 | 在本工具中的角色 |
|---|---|---|
| MCP | Agent ↔ 工具 / 数据源（client 调 server 的 tools） | bridge 作为 MCP server，对外暴露 `bridge_run_job` / `bridge_get_interactions` / `bridge_answer_interaction` 等 tool；运行中交互靠"client 轮询 pending interaction + answer" |
| ACP（Zed Agent Client Protocol） | 编辑器 / 客户端 ↔ Agent 的**会话托管**，原生支持 Agent 运行中实时请求权限 / 输入 / 确认的流式会话 | 仅当需要"像 IDE 托管 Agent 那样把其提问实时流给客户端、客户端实时回答"时才需要；要求 bridge 变成 ACP 会话宿主，且被托管 Agent 自身必须讲 ACP |

把"跟发起方 Agent 交互"拆成两个方向看：

- **方向 A（推荐，进 MVP 的目标形态）**：发起方 Agent 作为 MCP client 主动调 bridge。提交 `bridge_run_job` → 轮询 `bridge_get_job` → 命中 `pending_interaction` → Agent 呈现给用户 → 用户答 → 调 `bridge_answer_interaction` → bridge 续跑。纯 MCP，零 ACP，契合本工具异步 job 模型。
- **方向 B（被触发 Agent 半途反推问题）**：MCP 在**单次 tool call 生命周期内**有 `elicitation`（server 在工具返回前请求结构化输入）和 `sampling`（server 反向借 client 推理）。但异步 job 提交即返回 `job_id`，半途提问已不在该次 call 内，`elicitation` 够不着，仍退回方向 A 轮询。只有把 `bridge_run_job` 设计成**同步阻塞的短任务 tool** 时才能用 `elicitation` 原地反问——长任务同步阻塞会卡死会话并超时，与"长任务不卡 HTTP"定位冲突，故只作短任务 / 确认类的**可选增强**，不作主路径。

因此本设计的协议口径固定为：**HTTP/File job 协议为稳定底座；MCP 作为可选入口（含运行中交互的轮询 + answer）；ACP / PTY 那类"实时会话托管"留作后续 B 档单独评估，不进 MVP。**

## 13. 安全设计

该工具能在主机或容器内执行命令，默认必须按高风险工具处理。

基础要求：

- 默认必须配置 token；空 token 只允许显式 `--allow-empty-token`。
- `project_key` 必须来自 allowlist。
- `agent` 必须在项目的 `allowed_agents` 内。
- `cwd` 必须限制在项目目录内。
- `exec` 模式必须可按项目关闭。
- 不允许请求传任意环境变量覆盖，首期只允许配置文件里的 env。
- 日志中不打印 token、完整 secret、AK/SK。
- `/v1/jobs/{id}/logs` 默认限制返回大小，防止超大日志撑爆响应。

增强项：

- 监听地址默认 `0.0.0.0:8765`：本工具核心用例是容器经 `host.docker.internal` 反向调主机 bridge，在 Docker Desktop / WSL2 下绑回环（`127.0.0.1`）会导致容器连不上，故**默认开放 + 强制 token** 而非默认绑回环。仅当确无容器访问需求（纯主机本地自用）时，才显式收紧为 `127.0.0.1`。本条与 §14 配置示例、§8.1 默认值一致。
- 支持 IP allowlist，例如只允许 Docker 网段和本机。
- 支持命令 allowlist，限制 `exec` 只能跑 `mvn`、`go`、`npm` 等。
- 支持 job 并发限制，避免多个 Agent 同时重负载构建。

## 14. 配置文件草案

```yaml
server:
  addr: 0.0.0.0:8765
  token_env: BRIDGE_TOKEN
  allow_empty_token: false

storage:
  default_exchange_subdir: tmp
  default_result_subdir: dev-agent-bridge
  # root: D:/work/.dev-agent-bridge/jobs   # 可选；设置后切换为全局 store(<root>/<project_key>/<job_id>)，留空=默认各项目 tmp/

projects:
  example-project:
    host_path: D:/work/inhere/example-project
    container_path: /workspace
    exchange_subdir: tmp
    result_subdir: dev-agent-bridge
    default_agent: codex
    allowed_agents: [codex, claude, opencode, exec]
    allow_exec: true
    max_concurrent_jobs: 2

agents:
  codex:
    type: cli-agent
    command: codex
    args: ["-s", "danger-full-access", "-a", "never", "exec", "{{prompt}}"]
    detect:
      command: codex
      args: ["--version"]

  claude:
    type: cli-agent
    command: claude
    args: ["{{prompt}}"]
    detect:
      command: claude
      args: ["--version"]

  opencode:
    type: cli-agent
    command: opencode
    args: ["{{prompt}}"]
    detect:
      command: opencode
      args: ["--version"]

  exec:
    type: exec
    allow_raw_cmd: true

runners:
  local:
    type: local

  docker-peer:
    type: peer-http
    base_url: http://localhost:8766
    token_env: CONTAINER_BRIDGE_TOKEN
```

## 15. 实施里程碑（概览）

> 本节只给里程碑级概览统一对齐目标；**详细阶段编号、任务拆分与验收以实施计划 `docs/plans/2026-06-16-dev-agent-bridge-plan.md` §9 为唯一权威**（plan 采用 P0–P10 更细粒度分期）。早期 v0.1 草案曾用本文 P1–P6 编号，已废弃，避免与 plan 双轨歧义。

三大基础抽象 + 增强能力对应到 plan 阶段：

| 里程碑 | 内容 | 对应 plan 阶段 |
|---|---|---|
| M0 准备 | 范围边界、工作树、二进制忽略 | P0 |
| M1 骨架 | 重命名 `tools/dev-agent-bridge`、`agent-bridge` 二进制、gcli 多级子命令骨架、引入 gcli/v3 + rux/v2 + goccy/go-yaml | P1 |
| M2 Project Registry | 配置加载、`project_key`、任意 host/container 映射、交换目录、`project` 子命令 | P2 |
| M3 Agent Registry | `agents` 配置、模板渲染、检测、`agent` 子命令；统一走 `agent` 字段不保留 `mode` | P3 |
| M4 Job/Store/Runner | 异步 job、文件存储、local runner | P4 |
| M5 HTTP API | `/v1/jobs` 系列、`serve` | P5 |
| M6 Job CLI | `job run/show/logs/cancel` 本地包装 | P6 |
| M7 Peer Runner | peer-http 反向调用容器内 bridge | P7 |
| M8 MCP Server | `agent-bridge mcp` 暴露 `bridge_*` tools | P8 |
| M9 运行中交互 | `interactions.jsonl` + 轮询/answer（方向 A，纯 MCP） | P9 |
| M10 文档清理 | README、scripts、根 `CLAUDE.md` 引用、迁移说明 | P10 |

> 注意：Project Registry 先于 Agent Registry（M2→M3），因为 agent 的 `allowed_agents` 校验依赖已加载的 project 配置。

后续独立复用（抽成独立 repo / 内部通用工具包）不在本次范围内：当前阶段保持只改工具自身，不改任何被登记项目代码；待实际有多个仓库稳定复用后再单独评估。

## 16. 待确认事项

- Codex、Claude、OpenCode 在本机实际可用的非交互命令参数分别是什么。
- 是否需要支持交互式会话继续输入；如果需要，要单独设计 session/pty，不应塞进首期异步 job。
- MCP 首期只做 stdio server 是否足够。

已明确（v0.6）：

- 运行中双向交互走 MCP（方向 A：client 轮询 `pending_interaction` + `bridge_answer_interaction`），**不引入 ACP**；ACP / PTY 实时会话托管留作后续 B 档单独评估（见 §12.5）。
- 目录从 `tools/codex-bridge` 重命名为 `tools/dev-agent-bridge` 后，必须同步更新根 `CLAUDE.md` 中的工具路径、access token 与鉴权头说明（旧 `X-Bridge-Token` → `Authorization: Bearer`），否则容器侧调用说明失效（plan P10）。

已明确（v0.7，用户确认）：

- **配置文件规范默认位置为用户级 `~/.config/dev-agent-bridge/config.yaml`**：`serve` 与 `project add` 在未显式 `--config` 且无现有配置时，默认读写该路径；§8.1 的四级查找链作为覆盖手段保留（`--config` > `AGENT_BRIDGE_CONFIG` > 当前目录 `.dev-agent-bridge.yaml` > 用户级）。`configs/bridge.example.yaml` 仅作示例模板，非运行配置。
- **容器反向调用优先采用 peer bridge（方案 A）**：peer-http runner 为首选并进 plan P7；`docker-exec` 仅作无常驻 bridge 时的后续兜底，不在 MVP（与 §5.1、§11 一致）。
- **结果目录默认写各项目 `tmp/`，全局 store 作为可配置项（默认关闭）**：未设 `storage.root` 时结果落 `<project.host_path>/<exchange_subdir>/<result_subdir>/<job_id>`（容器经共享盘可读，正向用例前提）；设 `storage.root` 时切换为全局 store `<storage.root>/<project_key>/<job_id>`。权衡：全局 store 不在项目共享盘，容器侧无法经共享盘直接读结果，仅适合纯主机 / peer runner 场景（详见 §9.3）。

## 17. 结论

建议将当前工具直接重命名为 `dev-agent-bridge`，正式 CLI 使用 `agent-bridge`，定位为跨主机/容器/任意挂载项目的本地 CLI Agent 任务桥。

首期重点不是为每个 Agent 写死适配，而是完成三个基础抽象：

- Project Registry：解决多项目。
- Agent Registry：解决 Codex/Claude/OpenCode/任意 CLI Agent。
- Runner：解决主机执行、容器执行和后续远端执行。

MCP 可以作为可选入口加入，但不应替代 HTTP Job API。HTTP/File job 协议继续作为稳定底座，MCP 只负责让支持 MCP 的 Agent 更自然地调用这些能力。
