# gofer

[English](README.md) · 中文

把可配置的 **CLI Agent**（`codex` / `claude` / `omp` / `opencode` / 任意命令）与已登记的**项目**，桥接为一个统一的**异步 job 控制面**：一端提交 `{项目, agent, prompt/命令, cwd}`，gofer 在该项目的真实工作目录里执行（本机 / 远端 worker / peer），把状态、日志、退出码、结果统一回传，并可经 **CLI / HTTP / MCP / Web 控制台** 四种入口提交与观测。

> gofer = "跑腿取送的人"：派任务给 agent → 在目标项目目录执行 → 回传日志/结果。自带 `gofer`↔`gopher` 的 Go 双关。

## 目录

- [能力总览](#能力总览) · [架构](#架构) · [安装 / 构建](#安装--构建) · [快速开始](#快速开始)
- [核心概念](#核心概念) · [提交 job](#提交-job) · [并行：--worktree](#并行job用---worktree) · [中断续跑：resume](#中断续跑job-resume)
- [远端执行与 worker](#远端执行与-worker) · [断线恢复](#断线恢复recovering) · [隧道](#隧道tunnel)
- [人机协作](#人机协作交互plan会话中继) · [日志与观测](#日志与观测)
- [配置参考](#配置参考) · [CLI 参考](#cli-参考) · [MCP](#mcp-接入) · [Web 控制台](#web-控制台) · [HTTP API](#http-api)
- [部署](#部署) · [安全](#安全注意事项) · [历史](#历史)

## 能力总览

- **多入口控制面**：CLI（`gofer job …`）/ HTTP（`/v1/*`）/ MCP（stdio，22 个 `gofer_*` tool）/ Web 控制台（看板、详情、实时日志、Runners、Plans、会话、新建 job），同一套 `job.Service`。
- **多 agent，一个 key 两种模式**：`type: cli-agent` 用模板渲染（`args` = 批处理 argv，`interactive_args` = pty argv），`type: exec` 原样跑 argv。未安装的 agent 只标 `unavailable`。
- **多项目、按项目治理**：`host_path`/`container_path`、允许的 agent/runner、`allow_exec`、`allow_interactive`、并发上限、超时上限、默认 worktree。
- **三种执行位置（runner）**：`local`（本进程）/ `peer-http`（转发给另一台 gofer）/ `worker`（WS 远端执行机，标签调度）；远端的日志、状态、交互经"镜像"透明回传。
- **断线恢复**：worker 连接抖动或 serve 重启时，在飞 job 进入 `recovering`，同一 worker 进程在窗口内重连即续传日志、补发结果，不再一断就 failed。
- **受管 worktree**：`--worktree` 让每个 job 在自己的 git worktree 里跑，并行 agent 互不干扰。
- **续跑**：`job resume` 让 codex/claude 带着自己的会话上下文接着上次中断的地方继续。
- **隧道**：`gofer tunnel` 经 worker 做受白名单约束的 TCP/UDP 端口转发（如容器 → 车间 PLC/HMI），三端日志用同一 `tunnel_id` 关联并带分段时延。
- **人机协作**：运行中提问（`pending_interaction`）、`plan` + todo 进度看板、`ask_human` 阻塞决策、终端会话中继（人离开电脑时自动布防，web/手机回复注入原会话）。
- **codex 挂了自动转 omp**：agent 因**供应商错误**（`at capacity` / `stream disconnected` / sandbox 没起来…）挂掉、且它自己也没法续时，server 把**同一份活**交给下一个候选 agent（一个普通 job）：agent 上写 `fallback_agents: [omp]`，或项目上按 agent 覆盖 `agent_fallbacks: {codex: [omp]}`；单个 job 可用 `job run --fallback omp` 覆盖、`--no-fallback` 关掉。接管的 job 继承 worktree、plan/todo、verify、caller，prompt 会说明"上一次由谁执行、只做剩余部分、别重做已提交的工作"；源 job 记 `job.fell_back` 并留下 `fell_back_to`（不再报一条马上被接管的终态失败）。健康度按 agent 聚合（每个 failed job 都记 `failure_class`）：`gofer agent status` 看谁 degraded、`gofer agent probe <key>` 提交一个真的"只回一行 OK"的 job 立刻验活，web 的 Agents 页有徽标和探针按钮；`server.agent_fallback.pre_dispatch: true`（默认关）还会在**提交时**就把 degraded 的 agent 换成候选。
- **验收不靠 agent 自述**：agent 汇报≠验收——`job run --verify 'go test ./...'`（或项目 `verify:` 默认值）让验收命令在**干活那台机器**上、紧跟 agent 之后、用同一个 cwd/env 跑；非 0 退出即 job `failed`（开着 review 则停 `needs_review`），输出带横幅落进该 job 的 stderr 日志；worker 上跑的 job 由执行机验、结果经 Outcome 回传，审批与验证事件也会镜像到 hub，通知与审计同样看得到。
- **定时与编排**：`schedule` 定时 job，`workflow` 多步依赖链（fan-out / join / 重试）。
- **可观测 / 可审计**：JSONL 文件日志（轮转、脱敏）、`/v1/runners` 健康名册、SSE 实时流、`caller_id`/`worker_id` 入库、retention 周期清理；SQLite（纯 Go）存元数据。
- **Windows 友好**：nssm 服务化脚本（`scripts/start.ps1`，含一键 `upgrade`）、ConPTY 交互会话。

## 架构

```txt
  提交/观测入口                    控制面 (serve)                 执行位置 (runner)        agent
┌───────────────┐           ┌────────────────────────┐      ┌─ local  (本进程) ─┐
│ CLI  gofer job │──HTTP────▶│  /v1/*   job.Service    │─────▶│ peer-http (另一台 gofer) │──▶ cli-agent
│ HTTP /v1/*     │           │  registry: project/agent │      │ worker (WS 远端执行机) │     (codex/claude/…)
│ MCP  stdio     │           │  runners + ws hub        │      └────────┬───────────┘      或 exec(argv)
│ Web  控制台     │           │  jobstore(SQLite)+logs   │◀────镜像日志/状态/交互────┘
└───────────────┘           └────────────────────────┘
   Authorization: Bearer <token>（/health 除外）         结果: <host_path>/tmp/gofer/<job_id>/ + DB
```

## 安装 / 构建

需要 Go 1.25+（含 Web 控制台时另需 Node + pnpm）。

```bash
make build            # 当前平台 → dist/gofer（默认 upx 压缩；无 upx 用下面的 go build）
go build -o dist/gofer ./cmd/gofer
make web build        # 构建前端并嵌入二进制
make build-all        # linux/darwin/windows × amd64/arm64
```

## 快速开始

```bash
# 1. 登记项目（host-path 必须是已存在的绝对目录）
gofer project add workspace \
  --host-path /work/projects/workspace --container-path /workspace \
  --default-agent codex --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner local --allow-exec

# 2. 起服务（token 走 env）
export GOFER_TOKEN=dev-token
gofer serve --addr 0.0.0.0:8765

# 3. exec job（-- 之后为命令 argv）；--sync 让服务端等到终态再返回
gofer job run -p workspace -a exec --sync -- go version
# → status=done exit_code=0

# 4. cli-agent job；查日志
gofer job run -p workspace -a codex --prompt "总结本目录的测试失败用例" --wait
gofer job logs <id> --stream stdout

# 5. 浏览器打开 http://<addr>/ ，粘贴 token 接入
```

只当客户端用（比如容器里给 AI agent 用）时不需要任何 yaml：

```bash
gofer init client      # 生成 <config-dir>/.env：GOFER_SERVER_ADDR / GOFER_SERVER_TOKEN / GOFER_RUN_MODE=client
gofer job list         # 填好地址与 token 即可
```

## 核心概念

- **project**：一个可执行任务的真实目录。字段：`host_path`（主机路径）/ `container_path`（容器路径）/ `default_agent` / `allowed_agents` / `agent_fallbacks`（项目级故障转移候选，按挂掉的 agent 给有序列表）/ `allowed_runners` / `allow_exec` / `allow_interactive`（pty/交互 job 的项目级开关，默认关，是项目侧**唯一**的交互闸）/ `max_concurrent_jobs` / `max_timeout_sec` / `worktree_default` / `verify` + `verify_timeout_sec`（项目默认验证步骤及其独立超时）。
- **agent**：怎么执行。`cli-agent` 用 `command` + `args` 模板渲染（占位符 `{{prompt}}` `{{cwd}}` `{{job_id}}` `{{result_dir}}`，逐元素替换、不过 shell）；再写 `interactive_args` 即**一个 key 同时支持批处理与 pty**（`[]` = 裸 TUI 启动；不得含 `{{prompt}}`）。`exec` 原样跑请求里的 `cmd` argv（需项目 `allow_exec`）。
- **runner**：在哪执行。`local`（本进程子进程）/ `peer-http`（转发到另一台 gofer）/ `worker`（WS 连入的远端执行机）。
- **job 生命周期**：`queued → running → done | failed | cancelled | timeout`；运行中提问 `running → pending_interaction → running`；执行它的 worker 断线 `running → recovering → running | failed`。

## 提交 job

同一个 `JobRequest`，四种入口、两种时序：

```bash
# CLI —— cli-agent 用 --prompt；exec 用 -- argv
gofer job run -p workspace -a codex --prompt "审查改动并给风险点"
gofer job run -p workspace -a exec  --sync -- mvn -q test     # --sync：服务端等终态
gofer job run -f task.md                                      # md+yaml 文件：frontmatter 定参数，正文即 prompt
gofer job run -p workspace -a codex --review --prompt "重构解析器"  # 交付物需人验收
gofer job accept <job-id> [--note "看着不错"]                  # needs_review → done
gofer job reject <job-id> --note "拒绝理由" [--resume]          # → rejected；--resume 以理由为 prompt 续投
```

```markdown
<!-- task.md -->
---
project_key: workspace
agent: codex
runner: worker
worker_labels: [gpu]      # 或 worker_id: w-01
worktree: true
---
在 scripts/ 下生成批处理脚本，读取 config.yaml 的任务列表……（正文即 prompt）
```

- **HTTP**：`POST /v1/jobs`（JSON 或 `text/markdown`）。`"sync": true` 或 `?wait=1` 走同步（终态 `200` + 完整结果；超服务端上限 `202` + `X-Gofer-Async: 1` + id）。
- **MCP**：`gofer_run_job` 等（见 [MCP](#mcp-接入)）。
- **Web**：顶栏「+ 新建 job」。
- **同步 / 异步**：默认异步（立即返 `id`，`job watch <id>` 跟随）；`--sync` 服务端等到终态（默认上限 30s，`--wait-timeout` 可调，超时退回异步）。
- **超时上限**：`--timeout` 超过 `server.max_job_timeout_sec`（默认 3600）或项目 `max_timeout_sec` 会被 **clamp**，且**不会静默**：CLI 打 `warning: --timeout <请求>s exceeds the project ceiling (<上限>s)…`，响应带 `requested_timeout_sec` / `timeout_clamped`。

### 并行 job：用 `--worktree`

多个 job 在同一个 checkout 里并行改代码会互相踩（`.git/index.lock` 残留、互相覆盖）。`--worktree` 让 job 在**自己的 git worktree** 里跑：

```bash
gofer job run -p workspace -a codex --worktree [--worktree-base v1.2.0] --prompt "修 3 个 issue，逐个 commit"
gofer job worktree ls [-p workspace]                        # 分支 / 领先提交数 / 是否脏 / 是否已合并
gofer job worktree rm <job-id> [--force] [--delete-branch]  # 脏且无 --force 拒绝；分支默认保留
```

- 位置 `<仓库顶层>/tmp/gofer/wt/<job-id>`，分支 `gofer/<job-id>`（基线默认当前 HEAD）；`--cwd sub` 映射为 `<worktree>/sub`；嵌套仓库按 cwd 所在的最近 git 顶层建；worktree 建在**执行机**上。
- 环境变量 `GOFER_WORKTREE` / `GOFER_WORKTREE_BRANCH` / `GOFER_WORKTREE_BASE`；结果记 `worktree_path` / `worktree_branch` / `worktree_base_sha` / `worktree_head_sha` / `commits_ahead`；`changes.diff` 分 `committed (base..HEAD)` 与 `uncommitted` 两段。
- 结束**默认保留**（分支上的提交就是交付物）；retention 只自动移除"无未提交改动且分支已合并"的 worktree。项目级 `worktree_default: true` 让所有 job 默认开启。详见 `docs/runbook/parallel-jobs-with-worktree.md`。

### 中断续跑：`job resume`

agent 因供应商容量错误、网络抖动或超时把 job 干掉一半时，不必重派整份任务（新会话 = 重读全部上下文）：

```bash
gofer job resume <源 job-id> --prompt "上一次运行因 <原因> 中断。先看 git status/log 判断进度，只完成剩余项，不要重做已提交部分。"
```

前提：源 job 已终态、捕获到了 `session_id`（`job show` 可见；codex 靠输出捕获，claude 靠 `--session-id` 注入）、agent 有 resume 模板（内置 claude/codex；其他 agent 用 `session_capture` / `session_resume` 配）、同一 runner。`rerun` 则是同请求重提（新会话）。命中配置的瞬时错误模式时 server 会自动续跑一次（`server.auto_resume_max`，设为 `0` 关闭）。（`reject --resume` 走的是同一条续投路径，只是由验收人的理由触发，而不是瞬时错误。）

### 人工验收：`needs_review` 与 `job accept` / `job reject`

"agent 跑完了"不等于"交付被接受"。带 `job run --review`（或项目级 `require_review: true`，或 workflow 步骤的 `review: true|false` 覆盖）的 job，agent **正常完成**后不会落 `done`，而是停在**非终态**的 `needs_review`，等人裁决：

```bash
gofer job accept <job-id> [--note "合格"]                    # → done（记 job.reviewed{accepted} + job.terminal{done}）
gofer job reject <job-id> --note "测试还是红的"               # → rejected（终态；理由必填）
gofer job reject <job-id> --note "测试还是红的" --resume       # 同时以该理由为 prompt 续投一个新 job
gofer job list --status needs_review                         # 还有哪些等人验收
```

- 失败（`failed`/`cancelled`/`timeout`）**不**进验收，只有正常完成才进。`rejected` 是终态：workflow 步骤按失败聚合，**不会**被自动重试/自动续投——只有人的 `--resume` 才继续这份工作。
- **只有人能 accept**：`POST /v1/jobs/{id}/accept|reject` 对 worker token 一律 403（开 `governance.require_answer_capability` 时还需 `can_answer`）；MCP 只提供 `gofer_reject_job`，**故意没有** accept 工具。验收入口是 web job 页的验收卡、CLI 或 HTTP。
- `job cancel` 对 `needs_review` 返回 409——已经没东西可取消，请用 `reject`；`job resume` 同样要求先裁决。
- 审计字段 `require_review` / `reviewed_by` / `reviewed_at` / `review_note` 落库，`job show` 与 web 页可见。`job.needs_review` 是**IM 通知默认事件**（与 `job.terminal` 同列），`job.reviewed` 需显式订阅。

## 远端执行与 worker

远端 job 的日志、状态、运行中交互都经"镜像"透明回传到本地 job，读路径不变。

```yaml
# 方式 A：peer-http —— 转发给另一台 gofer
runners:
  docker-peer: { type: peer-http, base_url: http://127.0.0.1:8766, token_env: PEER_TOKEN }

# 方式 B：ws-worker —— 远端执行机主动连入本 hub
server:
  workers:                         # 在册 worker（worker_id ↔ token 绑定 + 调度标签）
    w-gpu: { token_env: WTOK_GPU, labels: [gpu, linux] }
runners:
  w-gpu:  { type: worker, worker_id: w-gpu }   # 具名 worker runner（名册可见、可显式派发）
  worker: { type: worker }                     # 通用 worker runner（按标签动态派发）
```

worker 侧用独立配置连入并本地执行：

```bash
gofer init worker -o worker.yaml             # 模板
gofer -c worker.yaml config validate worker  # 自查 token / host_path / roots
gofer worker --worker-config worker.yaml     # 启动
```

```yaml
worker_id: w-gpu
server_link: { urls: [ws://hub:8765/v1/workers/connect], token_env: WTOK_GPU }
labels: [gpu, linux]
roots:                                   # POLICY 模式：项目由 server 下发，本机只映射路径前缀
  - { from: D:/work, to: /srv/work }
guards: { allow_exec: true, allow_interactive: true }   # 本机只减不增
```

- **路由**：显式 `{"runner":"w-gpu"}` / `--worker-id`，或 `{"runner":"worker","worker_labels":["gpu"]}` 在已连接且标签全包含的 worker 里按 `in_flight↑ → 心跳新鲜↑` 选机；无候选 `503`。落机的 `worker_id` 记入结果。
- **三处对齐**：`server.workers.<id>` 的 key、`runners.<name>.worker_id`、worker 端 `worker_id` 必须是同一个；worker 的 token 必须等于 `server.workers.<id>` 的 token。
- **LEGACY vs POLICY**：worker.yaml 有 `roots` 即 POLICY（project 集合由 server 下发，加项目零改动 worker）；只有 `projects` 则 LEGACY（本机自己定义）。`gofer config validate worker` / `gofer project list` 自检。
- worker 多 hub 地址 + 全抖动退避重连；`POST /v1/workers/{id}/reload` 让 worker 热重读配置。

### 断线恢复（recovering）

worker 连接抖动（WSL/Docker/VPN 瞬断）时，在飞的 job **不再立刻 `failed`**，而是进入 **`recovering`**（看板黄色徽标，`job list --status recovering`），等**同一个 worker 进程**在 `server.job_recover_window_sec`（默认 120，`0` = 关闭即旧行为）内重连：

- 重连时 worker 带上在飞清单与已发送的日志偏移；server 回给它已落盘偏移，日志**不丢不重**；断线期间跑完的结果重连后补发；`job cancel` 在断线期间下达的取消重连后送达。
- 窗口超时、或重连上来的是新进程（`instance_id` 变了）→ `failed`，error `worker lost …`。
- **serve 重启同样适用**：新 serve 把遗留的非终态 worker job 重新计入窗口，worker 重连即被**收养**（job 回到 `running`，日志接着原文件写）。
- 通知只认终态：recovering→running 不打扰，recovering→failed 才投递。

## 隧道（tunnel）

经 worker 把本机端口转发到 worker 所在网络的目标（如容器/办公机 → 车间 PLC、HMI），目标受 worker 的 `tunnels.allow` 白名单约束，server 只做中继：

```bash
gofer tunnel forward -w w-plc 1502:192.168.1.10:502 udp/21845:192.168.1.20:21845   # TCP + UDP
gofer tunnel save hmi -w w-plc udp/21845:192.168.1.20:21845 && gofer tunnel forward --name hmi
gofer tunnel check -w w-plc udp/192.168.1.20:21845   # 只证明 worker 能建 socket，不证明设备会应答
gofer tunnel ls
```

三端（forwarder / server / worker）日志用同一 `tunnel_id` 关联，带 `dial_ms`、`first_byte_ms`、`bytes_up|down`、`packets_up|down`（UDP）、`close_reason`；`GOFER_TUNNEL_TRACE=1` 逐报文记录。怎么判断"慢在 relay、设备还是往返次数"见 [`docs/runbook/tcp-tunnel.md`](docs/runbook/tcp-tunnel.md)。

## 人机协作：交互、plan、会话中继

- **运行中交互**：agent 经 `POST /v1/jobs/{id}/interactions` 提问 → job 置 `pending_interaction` → 人 `POST …/answer` → 续跑；MCP 对应 `gofer_get_interactions` / `gofer_answer_interaction`；web 与 IM 通知（钉钉/飞书 webhook，`server.notification`）。
- **审批门（acp-agent）**：项目 `approval` 段决定 ACP agent 能无人值守做到哪一步——`mode: off`（默认）照旧自动放行，`ask` 放行 `auto_allow_kinds`（read/search/think/fetch）之外的求批，`strict` 全部求批。待批请求落成 `type=permission` 的交互（被求批的工具调用 + agent 自己的 ACP 选项 + `timeout_sec` 倒计时），在 web job 页点按钮、`gofer job answer <id> <interaction-id> <optionId>` 或 MCP `gofer_answer_interaction` 作答；无人作答则按 `on_timeout` 兜底（默认 `reject`，可配 `allow`）。agent 侧只能经 `agents.<key>.acp.permission_policy` **收紧**项目策略；IM 只发通知（`job.permission_requested`，带 web 链接），不支持在 IM 里作答。每次决定都进 job 事件（`job.permission_requested|answered|timed_out`）与 `<result_dir>/artifacts/acp.jsonl` 审计。
- **plan 进度看板**：`gofer plan create/add-todo/set-todo`，job 用 `--plan <id>` 挂上；web Plan 页（手机可开）就是实时进度页。**决策点问人**：MCP `gofer_ask_human` 阻塞提问，人在 web 作答后答案流回 agent（超时按预案继续）。
- **终端会话中继**：`gofer init hooks` 装 Stop/UserPromptSubmit 等 hook 后，Claude Code / Codex 会话停下时最后一条消息可发到 web「会话」页等回复，回复注入**同一个**会话继续（`gofer session relay auto|on|off`、`gofer session say`；会话归**注册它的 caller** 所有，且该 caller 名下还有在跑的 job 时 `auto` **刻意不布防**——否则 job 完成通知会堵在你自己的 Stop 后面，`session.auto_relay_skip_when_supervising`）。开关**三态**：`on` 每次停下都等，`off` 从不等，`auto`（缺省）交给 server 判——离开键盘超过 `session.auto_relay_idle_sec`（默认 300，`0` 关）自动布防，人一碰键盘即放行；**容器里探测不到键盘**（无 X11，`xprintidle` 不可用）时改看 `session.auto_relay_turn_sec`（默认 900，`0` 关）——距本会话人最后一次输入多久，人下次输入或按 Esc 即放行。会话只是**空闲**（没有 turn 在等）时，会话抽屉的输入框变成「送入终端」（CLI 是 `gofer session say --deliver`）：server 起一个内部 exec job 把文本敲进该会话的 **tmux** pane，于是已经停下的会话也能从 web 接着聊——前提是会话跑在 tmux 里、且登记了执行机（容器会话要在容器内起 gofer worker 并把 `GOFER_HOOK_RUNNER` 指向它）。

## 日志与观测

- **文件日志**：server `<config-dir>/run/serve.log`、worker `run/worker-<id>.log`、`tunnel forward` `run/tunnels/forward-<时间>-<pid>.log`（`--log-file` / `--log-dir`，`--quiet` 只静默终端）。JSON Lines，按 `log.max_size_mb` / `max_age_days` / `max_backups` 轮转，`token`/`authorization`/`password`/`secret` 键脱敏；显式路径打不开则启动失败，默认路径打不开只 warn。unix `-d` 后台模式另有 `run/*.out.log` 旁路承接 panic 等非 slog 输出；Windows 前台/服务进程的文件日志就是唯一持久日志。
- **事件**：每行带 `event`（`server.*` / `worker.*` / `tunnel.*` / `job.*`）、`operation_id`（进程一次运行）、`job_id` / `worker_id` / `tunnel_id`；`GOFER_LOG_LEVEL=debug|info|warn|error` 调详细度（stderr 与文件共用）。
- **Agent 输出**：结构化输出的 agent（`omp --mode json`、`claude --output-format stream-json`）逐行输出 JSON 事件；配 `output_format: ndjson` 后 gofer 在**采集时**把这条流**投影**成两路——`stdout.log` 只放 agent 的最终答复（omp 取最后一条 assistant 消息、claude 取 `result` 文本），`stderr.log` 放紧凑事件（一行一个、单行上限 2KB，超长字段标 `…(truncated)`）并丢掉逐 token 增量——体积小 10~30 倍、形同 codex，web 详情按时间线渲染事件流（`ndjson_keep` 决定哪些事件进投影器，`ndjson_events_to` / `ndjson_stdout` / `ndjson_stdout_path` / `ndjson_fields` 调落点与字段，`ndjson_raw` 另存未投影的 `stdout.raw.log`；计数落在 `ndjson_kept` / `ndjson_dropped` / `ndjson_truncated`）。
- **API**：`GET /v1/runners` 健康名册；`GET /v1/jobs` 过滤、`/v1/jobs/{id}/stream` SSE 实时日志 + 状态 + 交互、`/logs/{stdout,stderr}` 尾部 256KB、`/diff`、`/artifacts`、`/events`；`GET /v1/metrics`。
- **审计**：`caller_id`（谁提交，由 token 解析、服务端覆盖防伪）+ `worker_id` / `worker_instance_id`（在哪执行）随 job 入库；`storage.retention` 周期清理超期/超量的终态 job。

## 配置参考

### 查找链与运行模式

1. `--config <path>` → 2. `GOFER_CONFIG` → 3. `./.gofer.local.yaml` → `./.gofer.yaml` → 4. `<config-dir>/config.yaml`（默认 `~/.config/gofer`，`GOFER_CONFIG_DIR` 可改）。

| `GOFER_RUN_MODE` | 本地配置 | 用途 |
|---|---|---|
| `server`（默认） | 上面的查找链 | `gofer serve`；本机跑 job |
| `worker` | `<config-dir>/worker.yaml` | 连入 hub、执行派发的 job |
| `client` | **无**（只有 `<config-dir>/.env`） | 纯客户端：`project list` / `agent list` 默认读 server；`serve` / `worker` / `project add` 等明确拒绝 |

`.env` 自动加载：`<config-dir>/.env`（全局）→ `./.env`（当前目录覆盖）；已导出的 OS env 最高优先；**不要提交真实 token**。

### server 关键段

完整示例见 [`config/gofer.example.yaml`](config/gofer.example.yaml)。

```yaml
server:
  addr: 0.0.0.0:8765            # 对容器可达；安全靠强制 token + 内网准入
  token_env: GOFER_TOKEN        # token 来源：token_env > 内联 token > --token
  allow_empty_token: false
  # max_job_timeout_sec: 3600   # job --timeout 上限；项目 max_timeout_sec 可覆盖
  # job_recover_window_sec: 120 # 断线恢复窗口；0 = 关
  # session_auto_relay_idle_sec: 300   # 【已迁移】session.auto_relay_idle_sec 的别名
  # workers: { w-gpu: { token_env: WTOK_GPU, labels: [gpu] } }
  # callers: [ { id: docker, token_env: DOCKER_CALLER_TOKEN } ]
session:                             # 终端会话中继: 自动布防的两条判据(0 = 关闭该判据)
  # auto_relay_idle_sec: 300         # 键盘空闲 >= 阈值 → 会话停下时在 web 等回复
  # auto_relay_turn_sec: 900         # 探测不到键盘(容器)? 改看距上次人工输入多久
  # auto_relay_skip_when_supervising: true   # 该会话的 caller 还有在跑的 job 时不自动布防(SUP-01 D)
  # supervising_window_sec: 7200     # 上面这条判据回看多久内提交的 job
  # inject_commands: [claude, codex, omp, node, gemini, opencode]  # 允许被 web 送话的前台命令白名单
  # takeover_input_delay_ms: 1500    # 没有 tmux 时: 接管进程首次输出后安静多久再写首条输入(§9.1 B)
log:
  max_size_mb: 50
  max_age_days: 14
  max_backups: 10
storage:
  default_exchange_subdir: tmp
  default_result_subdir: gofer
  # root: /var/lib/gofer
  # retention: { max_age_days: 30, max_count: 5000, prune_interval_minutes: 60 }
projects:
  my-project:
    host_path: /work/projects/my-project
    container_path: /work/my-project
    default_agent: codex
    allowed_agents: [codex, claude, exec]
    allowed_runners: [local, w-gpu]
    allow_exec: true
    allow_interactive: true
    max_concurrent_jobs: 4
    # max_timeout_sec: 7200
    # worktree_default: true
agents:                            # 占位符：{{prompt}} {{cwd}} {{job_id}} {{result_dir}}
  codex:  { type: cli-agent, command: codex,  args: [exec, "{{prompt}}"], interactive_args: [], detect: { command: codex,  args: [--version] } }
  claude: { type: cli-agent, command: claude, args: ["-p", "{{prompt}}"], interactive_args: [], detect: { command: claude, args: [--version] } }
  exec:   { type: exec }
runners:
  local: { type: local }
```

- agent 的四种组合：只有 `args` = 仅批处理；`args` + `interactive_args` = 双模；旧写法 `interactive: true`（args 即 pty argv）= 仅交互；`interactive: true` 且 `args` 含 `{{prompt}}` = **配置错误，加载即拒**。
- 结果目录：默认 `<host_path>/tmp/gofer/<job_id>/`（设 `storage.root` 则 `<root>/<project_key>/<job_id>/`），只放 `stdout.log` / `stderr.log` / `changes.diff` / 产物；状态与元数据在 SQLite。

## CLI 参考

全局 `-c/--config` 是 app 级 flag，须置于子命令之前：`gofer -c <path> <command> …`。

```bash
gofer init     [server|worker|client] [-o path]     # 配置模板；init hooks 装会话中继 hook；init skill 装 gofer-usage skill
gofer serve    --addr 0.0.0.0:8765 [--no-web] [-d]  # -d 后台（unix）
gofer worker   --worker-config worker.yaml [-d]
gofer config   info | show <project> | validate [server|worker] | edit
gofer project  list [--remote] | show <k> | add <k> … | remove <k> | validate <k>
gofer agent    list [--local] | detect | show <k>
gofer job      run … | list … | show <id> | watch <id> | logs <id> --stream … | cancel <id> | rerun <id> | resume <id> --prompt … | worktree ls|rm
gofer plan     create | list | show <id> | add-todo | set-todo | set-status | attach | ask | decisions | answer
gofer workflow run <file.yaml> [-w] | list | show <id> | events <id> | cancel <id> | export <id>
gofer schedule add … | list | show | enable | disable | run <id> | rm <id>
gofer session  ls | show <id> | relay auto|on|off | say <id> "…" | rm <id>
gofer tunnel   forward | check | ls | save | saved | forget
gofer mcp      [--standalone]                        # stdio MCP server
```

`job run` 关键参数：`-p/--project`、`-a/--agent`、`--runner`（默认 `server`；`local` 兼容别名；worker/peer 填 runner 名）、`--cwd`（相对项目根）、`--prompt` / `-- argv` / `-f task.md`、`--sync` + `--wait-timeout`、`--wait`、`--worker-id` / `--worker-labels`、`--interactive` + `--cols`/`--rows`（需项目 `allow_interactive` 且 agent 有 `interactive_args`）、`--worktree` + `--worktree-base`、`--plan`、`--tags`、`--timeout`、`--title`、`-s/--server`、`--token`。

> 工作流跨机传值：`${steps.N.result_dir}` 是执行机上的绝对路径，只在同一文件系统内可直接读；跨 worker/peer 用 `${steps.N.result}`（inline result.json ≤32KB）/ `${steps.N.stdout}` 或共享盘。

## MCP 接入

`gofer mcp` 以 **stdio MCP server** 暴露同一套控制面（默认连 `GOFER_SERVER_ADDR` 的 server；`--standalone` 在本进程执行）。stdout 为协议通道，不输出日志。

```json
{ "mcpServers": { "gofer": { "command": "/abs/path/to/gofer", "args": ["mcp"], "env": { "GOFER_CONFIG_DIR": "/abs/config/dir" } } } }
```

tool（snake_case，与 HTTP 对齐）：`gofer_list_projects` `gofer_list_agents` `gofer_run_job` `gofer_get_job` `gofer_tail_log` `gofer_get_result` `gofer_get_artifacts` `gofer_cancel_job` `gofer_attach_job` `gofer_get_interactions` `gofer_list_pending_interactions` `gofer_answer_interaction` `gofer_punt_interaction` `gofer_create_plan` `gofer_get_plan` `gofer_add_todo` `gofer_update_todo` `gofer_ask_human` `gofer_register` `gofer_list_presence` `gofer_post_message` `gofer_poll_inbox`。

## Web 控制台

`serve` 内置静态 SPA（页面免鉴权，页内 `/v1/*` 需 token），资源嵌入二进制：`make web build`；裸 `go build` 显示占位页不影响 API。页面：Home 看板（服务健康、drivers/runners、需人工介入、job 状态分布、schedules、projects，外加两块元数据库卡片——**Server DB**：db + WAL 文件大小、page 几何、行数最多的若干张表；**Sessions**：按状态与中继三态的总数、待回复的 relay turn 数，整卡可点进 Sessions）/ job 详情（实时日志、diff、产物、pty attach）/ Plans（todo、决策）/ 会话（中继开关、`auto (idle Xm)`）/ Workflows / Schedules / Agents（已配置 agent 及其 detect 状态，下方同页列出在线 driver presence，点行进 `/agents/presence/:id` 收件箱；旧 `/drivers`、`/drivers/:id` 链接自动重定向到这里）/ Runners / 项目（含「允许交互 job」）/ 新建 job。左侧导航「观察」组为 Board、Plans、Sessions、Workflows、Schedules，「舰队」组为 Agents、Runners、Projects（Drivers 不再是独立菜单项）。关 Web：`serve --no-web` 或 `server.web_enabled: false`。

## HTTP API

`/health` 不鉴权；`/v1/*` 要求 `Authorization: Bearer <token>`。错误体 `{"error":"…","detail":"…"}`。

| 组 | 主要端点 |
|---|---|
| 项目 / agent / 名册 | `GET/POST /v1/projects`、`GET/PUT/DELETE /v1/projects/{key}`、`GET /v1/agents`、`GET /v1/runners`、`GET /v1/meta`、`GET /v1/metrics` |
| job | `POST/GET /v1/jobs`、`GET /v1/jobs/{id}`、`/logs/{stdout,stderr}`、`/stream`(SSE)、`/events`、`/diff`、`/artifacts`、`POST …/cancel`、`POST …/resume`、`GET/DELETE …/worktree`、`POST …/attach-ticket`、`GET …/pty/sessions` |
| 交互 | `POST/GET /v1/jobs/{id}/interactions`、`POST …/{iid}/answer`、`POST …/{iid}/punt`、`GET /v1/interactions` |
| plan / 决策 | `POST/GET /v1/plans`、`GET /v1/plans/{id}`、`POST …/todos`、`POST …/jobs`、`POST/GET /v1/decisions`、`POST /v1/decisions/{id}/answer` |
| 会话中继 | `GET/POST /v1/sessions`、`POST /v1/sessions/{sid}/heartbeat`、`…/relay`、`…/say`、`…/deliver`、`…/release-takeover`、`…/turns` |
| workflow / schedule | `POST/GET /v1/workflows`、`…/{id}/cancel`、`…/events`、`…/export`；`POST/GET /v1/schedules`、`…/enable`、`…/disable`、`…/run-now` |
| worker / 隧道 | `GET /v1/workers/connect`（WS）、`/v1/workers/pty-connect`、`POST /v1/workers/{id}/reload`、`GET /v1/tunnels`、`/v1/tunnels/connect`、`/v1/workers/tunnel-connect` |

`POST /v1/jobs` body（snake_case）：`project_key`、`agent`、`runner`、`prompt` / `cmd`、`cwd`、`timeout_sec`、`title`、`worker_id` / `worker_labels`、`interactive`、`worktree` / `worktree_base`、`plan_id`、`tags`、`sync` / `wait_timeout_sec`、`request_id`（幂等键）。

## 部署

**单机（Linux/macOS）**：

```bash
export GOFER_CONFIG=~/.config/gofer/config.yaml
gofer init server --global && $EDITOR ~/.config/gofer/config.yaml
gofer project add demo-api --host-path /abs/demo-api --container-path /work/demo-api
gofer serve -d                                     # 后台；日志 <config-dir>/run/serve.log
```

**Windows 服务（nssm）**：`scripts/start.ps1`（管理员 pwsh）：

```powershell
pwsh -File scripts\start.ps1 -ConfigDir 'D:/path/to/gofer' -Account '.\you'   # 安装并启动（以你的账号跑，job 才有你的 PATH/git 身份）
pwsh -File scripts\start.ps1 -Action upgrade [-Web]   # 先 make build（服务不停）→ stop → 换 serve-run\gofer.exe → start；旧 exe 留 .prev
pwsh -File scripts\start.ps1 -Action status|logs|restart|stop|remove
```

**容器 ↔ 主机**：容器内只当客户端（`gofer init client`，`GOFER_SERVER_ADDR=http://host.docker.internal:8765`），主机跑 server（和/或 worker）；job 的 `--cwd` 按执行机的项目根解析，命令里不要写死容器路径。

## 安全注意事项

- **监听 `0.0.0.0:8765`** 是为了容器可达；安全靠**强制 token + 内网准入**。纯本地自用可收紧 `127.0.0.1`。
- **强制 token**：默认无 token 拒绝启动；空 token 须显式 `--allow-empty-token`。多 caller token 常时间比对，`caller_id` 入库防伪。
- **worker 绑定**：`worker_id` 与其 token 绑定；POLICY 模式下项目由 server 下发，worker 用 `roots` + `guards` 只减不增。
- **执行边界**：exec 需项目与请求双重放行；cwd 限项目内（safeJoin 防 `../`）；不拼 shell（argv 数组）；`--worktree` 只在 git 顶层的 `tmp/gofer/wt` 内；隧道目标受 worker 白名单约束。
- **日志**：token / Authorization / nonce / payload 不落盘；日志接口仅尾部 256KB。

## 历史

代号沿革：`codex-bridge`（单 codex + exec 直跑）→ `dev-agent-bridge`（多 agent/项目注册表 + `/v1` 异步 job）→ **`gofer`**（+ ws worker / 标签调度 / 同步与 md 提交 / Web / MCP / SQLite / plan 与决策 / 会话中继 / 隧道 / 断线恢复 / worktree）。

> 设计与实施计划见 [`docs/`](docs/)（`design/` 设计、`plans/` 实施计划、`runbook/` 运维手册）。
