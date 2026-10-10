# gofer

[English](README.md) · 中文 · [更新日志](CHANGELOG.md)

**gofer 是一个自托管的控制平面：把 Claude Code、Codex、ACP agent 或普通命令，作为可追踪、可验收的 job，在你自己的机器上、在真实的项目目录里运行。**

你在主力机器上跑一个 gofer server，按需在其他机器或容器里跑 worker。每一件工作——一条命令、一次 agent 提问、一段持续的 ACP 对话、多步计划中的一步——都是一个绑定到已登记项目和 runner 的 *job*，带有日志、diff、提交、token 用量和验收状态。人和 agent 通过 CLI、HTTP API、MCP 和 Web 控制台访问同一个控制平面；仓库本地的 tracker（issue、记忆、会话开场 prime、接手包）让每个新的 agent 会话拿到上一个会话留下的上下文。

它面向一个开发者、或互相信任的小团队，在几台机器上同时驱动多个编程 agent。它**不是** agent 或大模型本身，不是托管服务，不是 CI/CD 系统或沙箱，也不是多租户平台：访问基于 token，job 以执行它的 gofer 进程的权限运行。

核心能力：

- **哪里有代码就在哪里跑**——在 server 本机或远程 worker 上执行（WebSocket 连接、按标签路由、server 下发项目策略），配合 git worktree 隔离、同目录锁、断线恢复、文件传输和 TCP/UDP 隧道。
- **用同一种方式驱动任何 agent**——`cli-agent`（claude、codex、omp……）、`acp-agent`（Agent Client Protocol）或 `exec`；批处理、交互 pty 或 ACP 持续会话；续接、故障转移到其他 agent、按 job 指定模型与预算上限。
- **编排多步工作**——带依赖链的 plan / todo、支持扇出与多 agent 对比的 workflow、cron 定时任务与 job 唤醒。
- **人始终把关**——验证步骤、验收标准、改动范围检查、`needs_review` 的接受 / 驳回、ACP 工具审批门、终端会话中继到网页和手机，以及以决策为中心的「今天」首页。
- **为 agent 协作共享上下文**——仓库本地 issue 与记忆、会话开场 prime 与接手包、从交付的 job 中提炼经验、工作项与可选的管家 agent。
- **一切可观察、可审计**——事件时间线、diff 与提交、用量与成本、统计看板、IM / webhook 通知、job 作用域凭证与脱敏工具。

> gofer 意为「跑腿的人」：把任务交给 agent，在目标项目里执行，再把日志和结果带回来。`gofer` / `gopher` 的谐音是有意为之。

## 目录

- [为什么用 gofer](#为什么用-gofer) · [架构](#架构) · [安装](#安装) · [快速开始](#快速开始)
- [核心概念](#核心概念) · [功能总览](#功能总览) · [配置](#配置)
- [文档](#文档) · [开发](#开发) · [更新日志](#更新日志) · [许可](#许可)

## 为什么用 gofer

真正用编程 agent 干活时，很快会遇到同样几类问题：

| 问题 | gofer 的做法 |
|---|---|
| 想用的 agent 在另一台机器上（主机与容器、Windows 与 Linux、GPU 机器） | 一个 server、多个 worker；在任何地方提交，job 在项目和 agent 所在的地方执行 |
| agent 的运行记录淹没在终端滚动里 | 每次运行都是一个 job：状态、日志、diff、提交、用量、事件时间线，存在 SQLite |
| 「agent 说做完了」不等于验收通过 | 验证命令、验收标准、范围检查，以及只有人能接受的 `needs_review` 关口 |
| 多个 agent 并行时互相踩同一份工作区 | 目录锁、`--worktree` 隔离与显式合并回主工作区 |
| 长任务需要多个步骤、多个 agent | 带依赖 todo 的 plan、可扇出 / 择优的 workflow、定时任务与唤醒 |
| 人一离开，会话就停在那里等你 | 会话中继、Web Push 与「今天」决策队列，可以在浏览器或手机上作答 |
| 每个新 agent 会话都从零开始 | 仓库 tracker：issue、记忆、`repo prime`、`issue brief` / `plan brief`、经验候选 |

## 架构

```txt
 客户端                          控制平面                           执行
┌──────────────────┐        ┌──────────────────────────┐      ┌──────────────────────────┐
│ CLI   gofer …    │─HTTP──▶│ gofer serve              │─────▶│ server（内置本机 runner） │──▶ agent
│ MCP   gofer mcp  │        │  /v1 API · Web 控制台    │      ├──────────────────────────┤    cli-agent: claude, codex, omp…
│ Web   控制台     │        │  项目 · agent            │◀─WS─▶│ worker（远程，主动连入）  │──▶ acp-agent: *-acp
│ hooks claude/    │        │  job · plan · workflow   │      ├──────────────────────────┤    exec: 任意 argv
│   codex/omp/…    │        │  会话 · tracker          │─HTTP▶│ peer-http（另一台 gofer） │
└──────────────────┘        │  SQLite + 结果目录       │      └──────────────────────────┘
                            └──────────────────────────┘
 /v1/* 需要 Authorization: Bearer <token>        日志、状态、交互与产出都会镜像回 server
```

- **server**（`gofer serve`）：唯一的真源——项目注册表与准入规则、job 存储、调度、Web 控制台、MCP / HTTP API。一台机器一个 server。
- **worker**（`gofer worker`）：远程执行机，经 WebSocket 主动连入 server，上报自己有哪些 agent 和目录，在本机执行 job。POLICY 模式下项目集合由 server 下发，worker 只做路径前缀映射（`roots`），并可用 `guards` 收紧。
- **agent**：真正干活的程序。gofer 不内置模型，只负责启动和监督已有的 CLI。
- **客户端**：一切提交或观察工作的一方——CLI（容器里可用纯客户端模式）、MCP 客户端、浏览器，以及登记终端会话的 agent hooks。

更完整的概念地图见 [`docs/architecture-overview.md`](docs/architecture-overview.md)。

## 安装

需要 Go 1.25+；构建 Web 控制台时还需要 Node.js 与 pnpm。

```bash
make web build          # 构建并内嵌 Web 控制台，生成 dist/gofer（默认用 upx 压缩）
make install            # 同上，再把二进制复制到 $GOPATH/bin
go build -o dist/gofer ./cmd/gofer   # 只含 API/CLI；控制台显示占位页
make build-all          # 交叉编译 linux / darwin / windows × amd64 / arm64
```

## 快速开始

**1. Server**（拥有项目目录的那台机器）：

```bash
gofer init server --global          # 写出 <config-dir>/config.yaml（默认 ~/.config/gofer）
$EDITOR ~/.config/gofer/config.yaml # 检查一遍，删掉用不到的示例项目
export GOFER_TOKEN=change-me        # 示例配置从 GOFER_TOKEN 读取 bearer token
gofer project add demo --host-path /abs/path/to/demo \
  --default-agent codex --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner server --allow-exec
gofer config validate
gofer serve                         # 或：gofer serve -d（后台），gofer serve register（受管服务）
```

浏览器打开 `http://<server>:8765/`，粘贴 token 即可使用 Web 控制台。

**2. 客户端**（只提交工作的容器或其他机器——不需要 YAML）：

```bash
gofer init client                   # 写出 <config-dir>/.env：GOFER_SERVER_ADDR / GOFER_SERVER_TOKEN / GOFER_RUN_MODE=client
gofer project list                  # 客户端模式下列出 server 上的项目
```

**3. Worker**（可选，负责执行 job 的机器）：

```bash
# 在 server 上（管理员 token）：登记 worker 并打印一次性 token
gofer worker add w-01 --project demo
# 在 worker 机器上：写 worker.yaml 与 .env、推断 roots、探测 agent 并跑 doctor 自检
gofer worker init --server http://<server>:8765 --id w-01 --token <一次性 token>
gofer worker -d
```

**4. 第一批 job：**

```bash
gofer job run -p demo -a exec --sync -- go version                      # 一条命令；--sync 等到出结果
gofer job run -p demo -a codex --prompt "总结这个目录里失败的测试" --wait
gofer job run -p demo -a claude --runner w-01 --worktree --review --prompt "修复问题 X"
gofer job list
gofer job watch <id>                                                     # 实时状态与日志
gofer job logs <id> --stderr --tail -n 50
gofer job review <id>                                                    # 一屏看完验收材料
gofer job accept <id>                                                    # 或：gofer job reject <id> --note "…" [--resume]
```

**5. 可选集成：**

```bash
gofer init hooks --agent all --global   # 登记 Claude Code / Codex / omp / jcode 会话（会话中继 + 记忆注入）
gofer init skill --global               # 为 agent 安装 gofer-usage skill
```

## 核心概念

| 概念 | 含义 |
|---|---|
| **project（项目）** | 允许执行工作的已登记目录：`host_path`（可选 `container_path`）、允许的 agent 与 runner、`allow_exec`、`allow_interactive`、并发与超时上限、默认验证步骤、验收与范围策略。未声明时内置一个指向默认工作空间（`~/.gofer/workspace` 或 `GOFER_WORKSPACE`）的 `default` 项目。 |
| **agent** | 怎么执行：`cli-agent`（带 `{{prompt}}`、`{{cwd}}` 等占位符的 argv 模板，批处理用 `args`，可选 `interactive_args` 用于 pty）、`acp-agent`（经 stdio 的 ACP server）或 `exec`（原样执行请求里的 argv）。本机 `PATH` 上装了的 CLI 会自动注入内置模板。 |
| **runner** | 在哪执行：`server`（server 本机的内置 runner；落库的规范名是 `local`）、某个 `worker`，或一台 `peer-http` gofer。 |
| **job** | 一个工作单元：项目 + agent + prompt 或 argv + cwd。生命周期 `queued → running → done / failed / cancelled / timeout`，另有 `waiting_dir`、`pending_interaction`、`awaiting_input`、`recovering`、`needs_review → rejected`。结果在项目的 `tmp/gofer/<job-id>/` 与数据库里。 |
| **workflow** | 步骤链（`gofer workflow run file.yaml`），支持 `${steps.N.*}` 引用、重试、扇出 / 汇合、子工作流和内置模板（`compare`、`plan-implement`、`review-committee`）。 |
| **plan / todo** | plan 把 job 归组，todo 是它的清单。有指派人且状态为 `ready` 的 todo 会自动派发；`--after` 把 todo 串成链，`gofer plan run` 推动整条链。 |
| **session（会话）** | 一段 agent 对话：ACP 持续会话 job（`job run --session`、`job say`、`job end`）、pty job，或经 hooks 登记的终端会话（中继、唤醒、催办）。 |
| **work item（工作项）** | 跨会话「你正在做的一件事」一张卡（`gofer work`），带状态、提醒、每日摘要和可选的管家。 |
| **tracker** | 仓库里的 `.gofer/tracker/`：issue 与记忆以 JSONL 随代码提交，镜像到 server，由 `gofer repo prime` 注入新会话。 |

## 功能总览

完整的使用参考是 [gofer-usage skill](skills/gofer-usage/SKILL.md) 及其 [命令参考](skills/gofer-usage/references/commands.md)；flag 以 `gofer <command> -h` 为准。

### Job

- 提交方式：CLI 参数、带 YAML frontmatter 的 markdown 任务文件（`-f task.md`），或 server 渲染的任务书模板（`-t <name> --var k=v`、`gofer template ls|show`）。
- 同步或异步（`--sync`、`--wait`），`--read-only`、`--model`、`--max-tokens / --max-cost / --max-turns`、`--timeout`、`--retry`、`--fallback`、`--env`、`--upload` / `--collect`。
- 隔离：同目录锁（`--lock`、`--shared-dir`、`--exclusive-dir`、`--lock-wait`），托管 worktree（`--worktree`、`gofer job worktree ls|merge|rm`）。
- 续接：`job resume`（同一 agent 会话，`--mode`，会话族内用 `--agent` 换 agent）、`job rerun`、`--from-session`、供应商类错误自动续投、故障转移到备选 agent。
- 交互：`--interactive` pty job 可在浏览器 attach；`--session` 开 ACP 持续会话。
- 唤醒：`gofer job wakeup create` 按定时或事件续接一个已结束的 job。

### 验收与质量关口

- `--verify '<cmd>'` 在 agent 结束后于执行机上跑检查；`--review` 让 job 停在 `needs_review`；只有人能 `job accept`。
- `--hold [--hold-reason "…"]` 让 job 在**执行前**停在 `awaiting_approval`——用于 agent 被自身权限拦下的对外 / 不可逆操作（如 `git push`）。人在 job 页（手机可用）一键批准或 `gofer job approve <id>` 后才执行；拒绝或超时即取消、命令不跑。agent 不能批准自己提交的待批 job。
- `--acceptance` 验收标准与 `--scope` 改动范围进入 prompt 和验收面板；范围外的发现可用 `gofer job findings [--create-issues]` 转成 issue。
- 未提交改动守卫（`on_uncommitted`）、ACP 工具调用审批门（项目 `approval`）、强制规则（`gofer agent rule`）与 skill 绑定（`gofer agent skill`）。

### 编排

- plan 与 todo：`gofer plan create|add-todo|set-todo|run|pause|resume|dispatch|import`，plan 交接说明，决策（`plan ask`、MCP `gofer_ask_human`），Web 看板视图。
- workflow：`gofer workflow run|show|pick|template`，多 agent 对比与择优合并。
- 定时任务：`gofer schedule add|run|enable|disable`，支持 cron 或一次性定时，可选 webhook 触发。
- 评论中 `@agent` 提及即派发 job，可按 plan 开启 leader 回合。

### 会话与人在回路

- 终端会话中继：`gofer init hooks` 之后，停下的 Claude Code / Codex / omp 会话可以在网页上等待回复（`gofer session relay auto|on|off`、`session say`），也可以被重新唤醒（`session resume`）或定时催办（`session nudge`）。
- Claude Code 权限确认镜像到网页作答；工作台里的 ACP 与 pty 会话；借助可选的 HTTPS 监听（`server.tls`、`gofer tool cert`）安装 PWA 并接收 Web Push。
- 「今天」首页：一个队列列出所有等你决定的事（交互、plan 决策、待验收、工作项），并行泳道、延后与专注模式。
- 工作项与管家：`gofer work …` 跟踪你跨会话在做的事；可选的管家（`gofer steward …`）以受限凭据帮你整理。

### 仓库 tracker 与 agent 记忆

- `gofer repo init|status|prime|sync|migrate`、`gofer issue …`、`gofer memory …`——issue 与记忆存在 `.gofer/tracker/*.jsonl`，另有存在 server 上的全局与项目记忆。
- `repo prime`（安装为 SessionStart hook）给新会话当前重点、进行中的 issue、规则和记忆索引；`gofer issue brief` / `gofer plan brief` 一条命令交接任务。
- `memory flag` 上报过时记忆，`memory doctor` 体检，`memory candidates|accept|reject` 把交付 job 中的可复用经验转成记忆。

### Worker、网络与运维

- Worker：`gofer worker add|init|doctor|show|projects|reload|upgrade|remove`，LEGACY 与 POLICY 两种配置，断线恢复。
- 隧道：`gofer tunnel forward|check|ls|save|stop` 在 worker 白名单内经 worker 转发 TCP/UDP；预设存在 server，也可由 server 托管转发。
- 文件：`gofer tool cp <src> <runner>:<project>/<path>` 与 `gofer tool xfer`。
- 受管 server：`gofer serve register|start|stop|restart|status|logs|upgrade|uninstall`（Windows 登录任务或 Linux systemd 单元），`gofer serve reload` 热重载配置。

### 观测与安全

- 事件时间线、diff 与提交、每个 job 的用量与成本、统计看板（`/dashboard`）、Prometheus `/metrics`、带轮转与脱敏的 JSONL 文件日志。
- 通过 webhook 及钉钉 / 飞书适配器发送通知（`server.notification`）。
- job 作用域凭证、带可选能力位的 caller token、`gofer job redact`、`job delete`、`job secret-scan`。

### 接入入口

- **CLI**——命令按 `gofer -h` 分组。
- **HTTP**——`/v1/*` 需要 `Authorization: Bearer <token>`；`/health` 与 Prometheus `/metrics` 不在其内（`/metrics` 可单独配 token）。
- **MCP**——`gofer mcp` 是 stdio MCP server，默认转发到 `GOFER_SERVER_ADDR` 指向的 server（`--standalone` 进程内执行，`--project` 限定项目）：

  ```json
  { "mcpServers": { "gofer": { "command": "/abs/path/to/gofer", "args": ["mcp"] } } }
  ```

- **Web 控制台**——内嵌在 `gofer serve` 中（`--no-web` 关闭）：今天、Dashboard、工作台、Board、验收台、Plans、工作项、Sessions、Issues、Workflows、Schedules、Agents、Runners、Projects 与设置。

## 配置

配置查找顺序：`-c/--config` → `GOFER_CONFIG` → `./.gofer.local.yaml` / `./.gofer.yaml` → `<config-dir>/config.yaml`（`<config-dir>` 默认 `~/.config/gofer`，可用 `GOFER_CONFIG_DIR` 覆盖）。项目目录可以放一个只含偏好的瘦配置 `.gofer.project.yaml`。`<config-dir>/.env` 与 `./.env` 会自动加载；不要提交真实 token。

| `GOFER_RUN_MODE` | 本地配置 | 用途 |
|---|---|---|
| `server`（默认） | 上面的查找链 | `gofer serve`、本机管理 |
| `worker` | `<config-dir>/worker.yaml` | 连入 server 并执行派发来的 job |
| `client` | 只有 `<config-dir>/.env` | 提交与观察；project 与 agent 命令读取 server |

- 示例：[`config/gofer.example.yaml`](config/gofer.example.yaml) 与 [`config/worker.example.yaml`](config/worker.example.yaml)。
- 参考：[server](skills/gofer-usage/references/server-config.md) · [worker](skills/gofer-usage/references/worker-config.md) · [客户端](skills/gofer-usage/references/client-config.md) · [配置步骤](skills/gofer-usage/references/setup-recipes.md)。
- 查看与检查：`gofer config info`、`gofer config show <project>`、`gofer config validate [server|worker]`、`gofer worker doctor`。
- 安全基线：没有 token 时 server 拒绝启动（除非 `--allow-empty-token`）；不需要远程访问时监听 `127.0.0.1`；exec job 需要项目开启 `allow_exec`；argv 从不经过 shell；job 的 cwd 被限制在项目目录内。

## 文档

| 位置 | 内容 |
|---|---|
| [`skills/gofer-usage/`](skills/gofer-usage/SKILL.md) | 面向用户与 agent 的使用指南，随每个用户可见改动同步更新 |
| [`docs/runbook/`](docs/runbook/) | 操作手册：会话中继、容器 worker、隧道、HTTPS/PWA、Web Push、IM 通知、受管 server、worktree、重试…… |
| [`docs/reference/`](docs/reference/) | HTTP API 端点总览与 Web 控制台细节（工作台布局、快捷键、实时推送） |
| [`docs/design/`](docs/design/) | 各功能的设计记录 |
| [`docs/gofer-enhancements-roadmap.md`](docs/gofer-enhancements-roadmap.md) | 路线图：按编号列出已落地功能与下一批候选（[历史档案](docs/roadmap-history.md)） |
| [`docs/examples/templates/`](docs/examples/templates/) | 示例模板（通用，可按项目改） |

设计文档与 runbook 大多用中文撰写。

## 开发（给 gofer 贡献者）

```bash
go build ./... && go vet ./...
go test ./...                                   # 全量；发版前用 -race -count=1
cd web && npx vue-tsc --noEmit && npx vitest run && npx vite build
GOOS=darwin go vet ./...                        # 改动平台相关代码之后
```

- 项目规则（分层、CLI 约定、兼容策略、公共工具）见 [`AGENTS.md`](AGENTS.md)。要点：入口层（`commands`、`httpapi`、`mcpserver`）只做绑定和转发；依赖单向；新的小工具命令放在 `gofer tool` 下；临时兼容代码带 `DEPRECATED(vX): remove in vY` 标记。
- 用户可见的改动在同一批提交里更新 [`skills/gofer-usage/`](skills/gofer-usage/)，并在 [`CHANGELOG.md`](CHANGELOG.md) 的「未发布」下加一行。
- 并行 worktree、设计稿命名与发版流程等仓库自身约定见 [`AGENTS.md`](AGENTS.md)。

## 更新日志

见 [`CHANGELOG.md`](CHANGELOG.md)：v0.100.0 起逐版本记录，更早的历史按里程碑压缩。

## 许可

[MIT](LICENSE)
