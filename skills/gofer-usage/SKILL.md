---
name: gofer-usage
description: "Use `gofer` from inside a dev container: submit tasks to the host gofer server with `gofer job` — run a command in the HOST environment, do multi-service / integration / external-callback testing the container can't do alone, or invoke a host AI agent (codex/claude) — and understand worker config (LEGACY local projects vs POLICY server-pushed roots) enough to tell WHY a project/agent isn't runnable. Use when inside a dev container and something must run on the host (outside the container) or on a specific worker, when a workspace's CLAUDE.md points to gofer / an old codex-bridge for host tasks, or when a gofer worker/project/agent is rejected and you need to diagnose it. Covers submit (--runner server; local remains a compatibility alias), reading logs, sync vs async, agent/runner selection, project discovery, worker LEGACY/POLICY modes + roots mapping, and troubleshooting, persistent ACP session jobs (--session / job say / job end), interactive pty jobs, worker show/projects/reload, terminal-session relay + web messages, work items (`gofer work`, the overview page, reminders, the daily digest, report / hand-over requests, the passive tidy-up and the steward), HTTPS entry, and job environment hygiene."
---

# gofer 使用：job 提交 + worker 配置

gofer = 一套「主机 server + 多台 worker」的任务执行网。你在 docker 容器里够不着主机环境 / 主机网络 / 主机上其他服务时，用 `gofer job` 把任务提交到 **主机 gofer server**，由它在 **主机**（`--runner server`；旧值 `local` 仍兼容）或某台 **worker**（`--runner <worker-id>`）执行。这取代了「写 tmp 通知外部 codex / curl host-bridge」这类旧机制。

- **server = 策略权威**：哪个 project 派给哪台 worker、允许哪些 agent、能否 exec/pty。
- **worker = 能力提供方**：这台机器有哪些目录（roots）/ 装了哪些 agent。

> 本 skill 详讲最常用的 `gofer job`。**其余命令**（`workflow`/`plan`/`schedule`/`session`/`tunnel`/`project`/`config`/`init`）见 [`references/commands.md`](references/commands.md)；**配置 gofer** 按节点角色看：纯客户端节点（容器里最常见）→ [`references/client-config.md`](references/client-config.md)；server → [`references/server-config.md`](references/server-config.md)；worker（含 LEGACY/POLICY 与 roots）→ [`references/worker-config.md`](references/worker-config.md)；加 project / 建 worker / 迁 POLICY 的分步 → [`references/setup-recipes.md`](references/setup-recipes.md)。需要时再读。
>
> 💡 执行**多步骤长任务**时，用 `gofer plan` + todo **把步骤串成链**，然后一条 `gofer plan run <plan-id>` 开工：每一项声明它等谁（`--after prev`），前一项 done/skipped 后后一项自动 `ready` → 自动出 job，跑完由 job 终态自动写回状态与交付的提交（PLAN-03；链末放一条 `--assign exec --cmd '<构建/测试命令>'` 的复核项即可让"改完自动验证"也在链上）。中间失败会**停在那一项**（plan `status=blocked` + 通知），`plan set-todo <todo> --status ready|skipped` 或 `plan resume` 继续。web/手机实时可看（Plans 页的**计划看板**：五列看板，把卡片拖到 `ready` 即派发、拖到 `done`/`skipped` 即人工标记，`doing`/`needs_review` 两列由服务端驱动、不可拖入）——完整示例见 [`references/commands.md`](references/commands.md) 的「todo 依赖链 + plan run」。**遇到需要人拍板的决策点**，用 MCP 工具 `gofer_ask_human` 阻塞提问、人在 web 作答后答案流回（超时按预案继续，不无限阻塞）——见同文「决策点问人」。

## 0. 先判断能不能用（30 秒自检）

```bash
command -v gofer            # gofer 是否在 PATH(常见 /workspace/go/bin/gofer)
gofer job list 2>&1 | head  # 能列出 job 即说明: .env 已自动加载 + 连上了主机 server
```

- `gofer` 启动时自动加载 `$GOFER_CONFIG_DIR/.env`，把 `GOFER_SERVER_ADDR` / `GOFER_SERVER_TOKEN` 注入；`gofer job` 的 `--server/--token` 默认就读这两个 env。**正常情况零配置、无需手动 source、无需传 token。**
- **纯客户端节点**：`GOFER_RUN_MODE=client` + `$GOFER_CONFIG_DIR/.env` 即可，**无需** worker.yaml / config.yaml（`gofer init client` 生成模板）；`project`/`agent` 等命令默认读 **server** 的实时视图（`project list` 即远程列表、`agent list` 列 server 的 agents）。
- 若 `gofer job list` 报连不上 / 401 → 主机 server 没起或 `.env` 不对，见 §7；这种环境就别用本 skill。

## 1. 找到本工作空间的 project key

提交必须带 `-p <project>`（容器内通常无本地 config，无法按 cwd 自动探测）。按优先级发现：

1. **本工作空间的 `CLAUDE.md`**：通常已写明 gofer 项目 key（最权威）。
2. `gofer job list` 输出的 `PROJECT` 列（看历史 job 用的哪个 key）。
3. `gofer project list`（列这台 worker 当前生效的 project；POLICY 模式读 server 下发的策略缓存；**client 模式即 server 的远程列表**，等价 `--remote`）。

> **内置 `default` 项目**：server 配置里没声明 `default` 时会自动注入一个指向默认工作空间（`~/.gofer/workspace` / `GOFER_WORKSPACE`）的 `default` 项目（`allowed_runners: [local]`，`allowed_agents` = 本机可用 agent），`project list` / Web Projects 标「内置」；编辑保存后写入配置成为声明项目，不能直接删除；配置里显式声明 `default` 则整条覆盖。`job run` 不带 `-p` 且 cwd 匹配不到项目时回落到它。
>
> project key 因工作空间而异，**不要硬编码**。该 key 必须已在**主机 server**注册（否则 `unknown project`）。

## 2. 最常用：在主机执行命令并等结果

```bash
# --runner server=主机执行（local 仍兼容）；--sync=server 等到终态返回；--cwd=项目内相对目录；--title=便于 list 辨识
gofer job run -p <project> -a exec --runner server --sync \
  --cwd <相对项目根的子目录> --title "<一句话任务名>" \
  -- bash -lc '<你的命令>'
```

返回里有 `status` 和 `exit_code`（`exit_code != 0` 即失败）。**stdout/stderr 要单独取**：

```bash
gofer job logs <job-id>
```

### 两条必守约定（最常见的坑）

**① 工作目录用 `--cwd`（相对项目根），不要在命令里 `cd` 绝对路径。**
job 在哪台机器执行，路径就按那台机器的项目根解析：同一 project 的**容器路径**与**主机路径**不同。命令里写死容器绝对路径，`--runner local` 在主机执行时目录不存在 → 报错。`--cwd` 由 gofer 按执行机的项目根安全拼接，天然跨主机/容器；默认 `.` 即项目根。

```bash
# ✗ 错：命令里 cd 容器绝对路径——runner=local 在主机跑时该路径不存在
... -- bash -lc 'cd /path/to/ws-root/<ws>/<sub>/xxx && git pull --rebase'
# ✓ 对：用 --cwd 相对项目根，命令只管业务逻辑
... --cwd <sub>/xxx -- bash -lc 'git pull --rebase 2>&1 | tail -3'
```

**② 每个任务带 `--title "<一句话>"`。**
不带 title 的 job 在 `gofer job list` 里只有 id/agent/runner，难辨识。一句话标题让任务列表可读、便于回溯。

## 3. job 子命令速查

| 命令 | 用途 |
|---|---|
| `gofer job run`（别名 `add`） | 提交任务 |
| `gofer job logs <id>` | 读 stdout/stderr |
| `gofer job show <id>` | 查状态/元数据 |
| `gofer job watch <id>` | 实时跟随状态+日志直到结束（异步任务用） |
| `gofer job list`（别名 `ls`） | 列 job（`-p` / `--tag` / `--agent` / `--runner` / `--since` 过滤） |
| `gofer job cancel <id>` | 取消运行中的 job |
| `gofer job say <id> "消息"` / `gofer job end <id>` | ACP 持续会话：同一个 job 下一轮 / 结束并释放锁（见下节） |
| `gofer job set <id> --title "…"` | 改/清空 job 标题（`--title ""` 清空）；忘了提交时带 `--title` 就用它补 |
| `gofer job rerun <id>` | 用原请求重提（新幂等 key，**新会话**，agent 重读全部上下文） |
| `gofer job resume <id> --prompt "…" [--mode session\|interactive\|batch] [--agent <同族agent>] [--env K=V]` | **续跑同一个 agent 会话**（codex `exec resume` / claude `--resume`）：job 中途失败/超时后让它带着自己的上下文继续；`--mode` 选续接形态、`--agent` 在同会话族内换 agent，见 §5b |
| `gofer job worktree ls [-p] / merge <id> [--squash] / rm <id> [--force] [--delete-branch]` | 查看、合并或清理 `--worktree` job 留下的 git worktree（见 §5c） |
| `gofer job run --read-only …` | **只读 job**：审查/分析类任务，agent 不能写文件（cli-agent 追加 `read_only_args` 沙箱参数、acp-agent `session/set_mode`）；exec agent 与没配只读模式的 agent 提交即被拒（见 §5e） |
| `gofer job run -t <模板> --var k=v …` | **用任务书模板派活**：把重复的那段约束/流程写成服务端模板，提交时只给变量（见 §5f） |
| `gofer template ls / show <name>` | 列出 / 预览模板（预览是服务端渲染好的正文，与提交时一致） |

`--timeout <秒>` 有上限：`server.max_job_timeout_sec`（默认 3600）或项目 `max_timeout_sec`；超出会被 **clamp** 并在提交后打一行 `warning: --timeout … exceeds the project ceiling`，`job show` 显示生效的 `timeout:`。超 1 小时的任务：让管理员提高上限，或把任务拆成多个 job。

### 多 agent 对比与流程模板

- 内置模板：`gofer workflow template ls|show <name> [-p <project>]` 查看；`gofer workflow run --template <name> --var k=v ...` 提交。`compare`（同一任务 × `agent_a`/`agent_b` 各在隔离 worktree 跑，停在待择优）、`plan-implement`（`planner` 规划 → 人工 review 闸 → `implementer` 在 worktree 实现）、`review-committee`（`agent_a`/`agent_b` 只读评审 → `verifier` agent 读各评审报告并对照源码逐条核实后汇总）。公共 var：`project`、`task` 必填；`runner` 留空 = 项目默认（项目允许内置 local 就用 local）。
- 择优：`gofer workflow pick <wf-id> <step> <fan> [--merge [--squash] [--cleanup-others]]`（也可 `--step/--fan`）。必须所有 fan 结束且被选 fan 成功才能选；`--merge` 紧接着把选中分支合入项目主 checkout，`--cleanup-others` 删掉其余 fan 的 worktree 与分支。单独合并：`gofer job worktree merge <job-id> [--squash] [--cleanup-others]`。
- 合并规则：只支持 server 本机 runner（远程 worker 的 job 返回 409 "only for a local runner"）；主 checkout 不能有未提交的已跟踪改动（未跟踪文件不影响）且须在命名分支，否则 409；冲突时 abort 复原并返回 409 + 冲突文件；不 push。
- **Web 对比视图与模板向导**：Workflow 详情里扇出步（`agents[]` 异构扇出 / `join: pick` 等）显示为对比视图——每路一列（agent、状态、耗时、diff 摘要与领先提交、verify、汇报尾部），可「并排展开 diff」；`join: pick` 停在待择优时每列有「选这个并合并」（二次确认弹层：merge/squash、清理其余分支；**先合并后记录选择**，冲突 409 时列出冲突文件、仓库已复原、没记录选择，可改选另一路；远程 runner 的 fan 只能「仅选择」不合并）。手机宽度一次一张卡片、可左右切换。新建 workflow 页有「从模板新建」页签（保留 YAML 页签）：选项目→模板（内置/全局/项目，显示来源）→按 `vars` 生成表单（agent / runner 下拉按项目 `allowed_agents` / `allowed_runners` 过滤）→「预览步骤」→提交。Job 详情的 worktree 区有「合并到基线」按钮（同合并接口；远程 runner 灰显并说明）。新增接口 `POST /v1/workflow-templates/{name}/render`（`{vars}` → 渲染后的 spec，不提交，用于预览）；workflow 详情的 step 行新增 `join` / `picked` 字段。
- 模板也可放项目 `.gofer/workflows/<name>.yaml` 或 `<config-dir>/workflows/`（项目优先于全局优先于内置）。细节见 references/commands.md「workflow」。

### ACP 持续会话（`--session`）

任务需要在**同一个 ACP agent 进程**里多轮对话时，先从 `gofer agent list` 选 `type=acp-agent`：

```bash
gofer job run -p <project> -a <acp-agent> --session \
  --prompt "第一轮消息" --timeout 90 --idle-timeout 1800 [--max-session 7200]
gofer job say <job-id> "下一轮消息"      # 同一 job 里再发一轮（不产生新 job）
gofer job end <job-id>                   # 结束会话并释放目录锁
```

- **状态**：一轮结束后 job 不终态，停在 **`awaiting_input`**（非终态，"等待输入"）；`say` 让它回到 `running`。`end` 或空闲到期为 `done`（`session_end_reason`），`cancel` 为 `cancelled`，agent 进程异常退出为 `failed`。
- **超时**：`--timeout` 只限**每一轮**；`--idle-timeout` 限等待下一条消息（0 = 默认 1800 秒）；`--max-session` 限整个会话（0 = 不限）。`--prompt` 可省：空 prompt 直接进入 `awaiting_input` 等第一条 `say`。
- **占用**：整个会话期间（含 `awaiting_input`）都**持有目录锁**并占 agent 的 `max_concurrent` 名额；所以尽量配 `--lock <子目录>` 或 `--worktree`，用完及时 `end`。
- **在哪跑**：server 本机 runner，或**协议 ≥ v13 的 worker**（`gofer worker show <id>` 看 `protocol: vN`）。目标 worker 协议 < v13 在提交时被拒（"不支持持续会话，请升级"）；peer 等其他远程 runner 提交即被拒（"仅支持本机 runner"）；`--interactive` 与 `--session` 互斥，非 acp-agent 带 `--session` 也被拒。本机 server 重启 / worker 断线后靠 ACP `session/load` 恢复（agent 不支持就 `failed`，改用新会话）。
- **续接 ACP job = 新开一个持续会话**：`gofer job resume <源id> [--prompt "首条消息"]` 对 acp-agent 源 job 会新建一个 job，用 `session/load` 载入源会话并进入 `awaiting_input`；`--prompt` 可省（空 prompt 直接等你 `say`），之后继续 `say` / `end`。agent 不支持 `loadSession` 或配了 `acp.load_session: false` 时报不支持。（因供应商错误触发的**自动**续投仍是一轮式。）
- **内置 `claude-acp` 用的适配器**：`npx -y @agentclientprotocol/claude-agent-acp`（旧包 `@zed-industries/claude-code-acp` 已废弃，停在 0.16.x，自带的旧 Claude Agent SDK 不发新版网关要求的会话请求头，表现为 `400 MissingSessionID … x-opencode-session`，而 claude CLI 正常）。在 config 里自定义了 claude-acp 的，改成新包名。
- **内置 `codex-acp` 用的适配器**：`npx -y @agentclientprotocol/codex-acp`（旧包 `@zed-industries/codex-acp` 已废弃；原先模板是裸 `codex-acp` 命令，主机 PATH 上没有，所以从未被注入）。detect 为 `npx -y @agentclientprotocol/codex-acp --version`，`acp.modes.read_only` = `read-only`，适配器声明 `loadSession`。自带 `@openai/codex` 依赖，可用 `CODEX_PATH` 指向别的 codex；认证读 `~/.codex/auth.json` 或 `CODEX_API_KEY`/`OPENAI_API_KEY`。
- **续接形态可选**：`job resume` 默认按源 job 决定形态（ACP→持续 ACP；cli 批处理→`--resume -p`；cli 交互→pty）；`--mode session|interactive|batch` 显式指定——一次性 ACP/批处理 job 可续成持续 ACP，也可续成 pty 终端；`session` 需 local runner 或协议 ≥ v13 的 worker，`interactive` 需项目 `allow_interactive` 且 agent 有交互续接模板，`batch` 必须带 `--prompt`。`--agent` 只允许**同会话族**（共用同一份磁盘会话存储）：`claude-acp`、`claude` 互通；`codex-acp`、`codex` 互通（内置 `tty-claude` / `tty-codex` 已移除：交互用 `job run -a claude|codex --interactive`；历史上 agent 为 tty-claude/tty-codex 的旧 job 仍可查看，`job resume` 时自动映射到 claude/codex 并保持 pty 形态，该映射是 DEPRECATED(v0.107)，v0.110 删除）（2026-10-05 主机实测：codex-acp 的会话 id 就是 `~/.codex/sessions` 里的 rollout id，双向续接都能复述原始首条消息），ACP agent 没有同族 CLI 时不能续成 CLI job（返回 400 并提示加 `--agent`）。`GET /v1/agents` 返回每个 agent 的 `session_resume` / `session_resume_interactive` / `acp_load_session` / `session_family`，Web 详情页「继续会话」据此给出灰显的「续接方式」单选。
- Web 工作台里 ACP 会话的输入直发同一 job，对话只呈现用户消息与 agent 回复；工具/思考/审批从「查看过程」进 job 详情。`say`/`end` 与 `cancel` 同一套权限（发起者可操作；job caller 只能操作自己派发的会话 job）。
- **通知长度**：`server.notification.max_text_runes` 默认 3000，可在每个 webhook 上用同名字段覆盖；0/缺省继承全局，钉钉/飞书另有 18000 UTF-8 字节安全上限。会话回复预览最多读取 64K rune，统一在 IM 渲染阶段截断。
- **Job 脱敏**：`gofer job redact <id> --literal-from-stdin` 或重复 `--pattern <RE2>` 只处理终态 job 的 owner/admin；原文不进 argv，响应只有计数和二进制跳过列表。远程缓存、已发送通知和外部日志不在范围内。
- **跨 job 秘密扫描**：`gofer job secret-scan --literal-from-stdin [--pattern <RE2>] [-p <project>] [--since <dur>]` 只从 stdin 接收原文 literal；普通 caller 只看到自己拥有的 job，管理员可按项目/时间窗跨 job 扫描。输出只有 job id、掩码后的标题、位置和计数，运行中的 job 只提示不能脱敏；不会扫描 `CLAUDE*` 文件。
- **批量脱敏**：在 `secret-scan` 上加 `--redact --yes` 会逐个复用现有 job redact，保留每个 `job.redacted` 审计，最后统一截断 WAL；`--vacuum` 只在显式指定时额外执行一次并可能持锁。远程缓存、通知和外部日志仍不处理。
- **Job 删除**：`gofer job delete <id> [<id> ...] --yes` 只删除终态 job 的持久记录和结果目录，保留不含原标题的 `job.deleted` 审计；Web 详情页会二次确认。
- **提交秘密提示**：`job run` 在最终 command/args/prompt/title/tags 上做常见形态扫描，stderr 只给位置提示、不回显、不阻止；`--no-secret-check` 关闭，服务端提交不拦截。
- 对比：偶尔追问不想占锁/名额，仍可用 `job resume` 的一轮一个 job 路径（不加 `--session` 的源 job）。
- **Web 新建会话选 runner**：工作台「新会话」表单与 Sessions 页都有 runner 下拉（按项目 `allowed_runners` 过滤；不可用项灰显并写原因，如 worker 不具备该项目 / 协议 < v13 不支持持续会话 / worker-only 项目不能用 local）；默认值仍是项目 `allowed_runners[0]`（无则本机 local），但可见可改，不再静默落到 server 本机。选了 ACP agent 而模式是一次性批处理时，会默认切到「ACP 持续会话」并提示。

job 状态里的 **`recovering`** 不是失败：执行它的 worker 断线了，server 在 `job_recover_window_sec`（默认 120s）内等同一个 worker 进程重连；重连上 → 回到 `running`，日志不丢不重；窗口到期才 `failed`（error `worker lost …`）。看到 recovering 先别重派。

## 4. agent 与 runner

**agent**（`-a`，取决于该 project 在 server 配的 allowed_agents，常见 `exec`/`codex`/`claude`）：

- `exec`：直接跑命令，命令放 `--` 之后：`-a exec -- <cmd> <args...>`。
- `codex` / `claude` / `omp`：跑 AI agent，提示词用 `--prompt "..."` 或任务文件 `-f task.md`（YAML frontmatter + 正文）。
- **一个 agent 两种启动方式**：agent 定义的 `args` 是批处理 argv（`job run`），`interactive_args` 是 pty argv（`job run --interactive`，`[]` = 裸 TUI）。`global_args` 放命令级、子命令前的选项，普通调用、手动/自动续接和工作台续聊都会复用；未配置时，gofer 只会从 `args` 中已知的续接子命令（如 `exec`）之前提取前缀，无法确认时不会猜测。提交时若被拒：`agent "x" has no batch mode` = 该定义只写了 `interactive: true`（旧的 tty-* 写法），只能 `--interactive` 提交；`has no interactive mode` = 没写 `interactive_args`；`project "p" does not allow interactive jobs` = 项目没开 `allow_interactive`。`gofer agent list` 的 `batch/interactive` 两列就是能力位。
- **交互 pty job 带提示词**：`job run --interactive --prompt "…"` 会把 prompt 作为 pty 的**首条输入**（自动补回车）写给 TUI，而不是被忽略；不带 `--prompt` 就是裸 TUI，人再 attach 输入。`--cols/--rows` 设初始终端大小，输出落 `pty.txt`（`job logs` 自动读它）。
- **交互取消与续接**：`exit_keys` 顺序发送退出输入，`exit_grace_sec` 最多等待横幅（默认 8 秒），之后仍按 `cancelled` 处理。已有 `session_inject` 是预分配配置；未注入的 agent 可用 `session_store_glob` + `session_store_id_regex` 从本机文件找候选 ID，终态横幅优先。配置形式与内置 CLI 边界见 [server-config.md](references/server-config.md)；捕获到 `session_id` 后用 `gofer job resume <id>` 重新进入（取消时 gofer 先发 `exit_keys` 让 TUI 打出退出横幅再收尾）。

**runner**（`--runner`，默认 `server`；`local` 为兼容别名）：

- `server` → **主机 server** 执行（需要主机环境/多服务联调时用它）。
- `local` → **主机 server** 执行（兼容旧写法，与 `server` 等价）。内部 / wire / DB 的规范名仍是 `local`，**Web 上所有给人看的地方统一显示 `server`**（Runners 页「Server」分组 / 卡片、runner 下拉的 `server · 本机`、job / 会话列表、拓扑图）；提交给后端的值不变。
- `<worker-id>` → 对应 **worker** 执行（容器自带的活直接在 bash 跑即可，一般无需绕 worker）。
- **两种拼写在所有入口等价**：`server` 与 `local` 指同一个本机 runner——job 提交 / 列表过滤 / resume、plan todo 的 `runner`、schedule 与 workflow（含模板渲染后、fan）的 runner、会话登记（hook 默认标签就是 `server`）与传话 / 唤醒、项目 `allowed_runners`（HTTP / CLI / MCP 都一样）。服务端在入口统一规范化并**落库为 `local`**，旧库里残留的 `server` 行读出时同样被识别为本机。`server` / `local` 是**保留名**（大小写不敏感）：自定义 runner 与 worker id 都不能叫这两个名字（`runners:` 里唯一合法的声明是 `local: {type: local}` / `server: {type: local}`；其他类型、`worker add server`、`init worker --id local`、worker.yaml 的 `worker_id`、`server.workers` 键都会被拒绝并说明原因），所以 `server` 永远就是本机，不存在“声明优先”的例外。

### 派活给主机 codex / claude：Windows 主机的两个坑

- **反引号会被 PowerShell 吃掉。** 主机是 Windows 时 agent 常用 PowerShell 写文件，而反引号是 PowerShell 的转义符：
  `` `t `` / `` `n `` / `` `r `` / `` `0 `` 会变成制表符、换行、回车、空字符。Markdown 行内代码（`` `run.pid` ``、`` `net.Listen` ``）
  和 Go 注释里的反引号会被**悄悄**改坏，编译与测试都照样通过；文件含空字符还会被 git 当成二进制。
  2026-09 实测发生过：设计文档里出现空字符、注释 `The `target` field` 被写成 `The <TAB>arget` field`。
  任务书里要写明：**改文件用 apply_patch，不要用 PowerShell 双引号字符串（含 `@"..."@`）**；非用不可时只用单引号 here-string `@'...'@`。
  并要求提交前自查 `git grep -nP "[\x00-\x08\x0b\x0c\x0e-\x1f]" -- <本次改动的文件>` 无输出，把原始输出贴进汇报。
- **Windows 上写出的文件常是 CRLF。** 与容器共用工作区的仓库最好有 `.gitattributes`（`* text=auto eol=lf`），
  否则容器侧会看到大批"已修改但 diff 为空"的假改动。

另外，**agent 的汇报不能当验收依据**：实测出现过"汇报称已按要求修改，代码其实一行没动"。
关键结论要自己 `grep` 源码、自己跑测试核对；必要时在任务书里要求它贴出指定命令的原始输出。

### 派活后如何验收（`needs_review`）

开着 review 的 job（`job run --review`，或项目 `require_review: true`）agent 正常结束后**不落 `done`**，停在 `needs_review` 等人裁决：

- **Web 验收台**：`/review` 列全部待验收 job（verify 徽标 / commits 数 / usage / 已经等了多久，等最久的在最前），行内 Accept / Reject（拒绝必写理由，可勾「自动续投」）。点行进 job 详情，页首是**验收面板**，五个页签一屏看完：**汇报**（agent 最终文本，markdown）/ **提交**（`base_sha → HEAD`）/ **Diff**（patch 就地渲染、按文件折叠，>5000 行或 >1MB 只渲染前 5000 行并可下载原文）/ **验证**（verify 结果 + stderr 里最后一段 verify 输出）/ **用量**；底部同一组 Accept / Reject。顶栏只在有待验收 job 时显示「待验收 N」徽标（v0.75 起 Review 不再是常驻菜单项），点它进 `/review`；工作台的「等你」也包含这些 job。
- **容器里（无浏览器）**：`gofer job review <id>` 打同一份材料（`--tail N` 改汇报行数，`--diff` 追加完整 patch）。

裁决前先自己核验（跑测试、看 diff），面板里的汇报仍是 agent 自己说的。

### 审阅后在 job 上留评论（留痕）

监督别人派的活（codex / claude / exec）时，job 结束并审阅完，**在这个 job 上留一条简短评论**，把结论和后续留在 job 页上，web 的 job 详情里就能看到，不用翻聊天记录：

```bash
gofer job comment <job-id> "验收：全量测试通过、web 三命令通过；我补了 abc1234（修 X）；已发 v1.2.3。"
gofer job comment <job-id> "退回：发现 Y 回归（约 40 个测试失败），续接修复见 job <新 id>。"
gofer job comments <job-id>        # 查看这条 job 的评论
```

- 写什么：结论（通过 / 退回）、自己做的修改（附 commit）、发现的问题、退回返工时的后续 job id、验收与发版结果。几行即可，只写事实。
- 哪些不用评：很简单的 exec / 检测类 job（主机构建、自更新、查版本、读配置之类）不必评论，避免记一堆没有信息量的记录；实施、计划候选、返工这类实质性 job 才评。
- **不要写 `@名字`**：用户身份的评论里出现 `@agent` / `@role` 会真的派出一个 job（MCP-05）。提到 agent 时去掉 `@`。在 job 内部（设了 `GOFER_JOB_ID`）发的评论记为该 agent 的发言，不会派活。

## 5. 同步 vs 异步

- **同步** `--sync`：server 阻塞到终态返回（默认上限 ~30s，可 `--wait-timeout <秒>`）。短任务、要立刻拿结果用它。
- **异步**（不加 `--sync`）：立即返回 job id，再 `gofer job watch <id>` 跟随 / `gofer job show <id>` 轮询。长任务（构建、联调、FFmpeg 等）用它，配 `--timeout <秒>` 限执行时长。
- **job 停在 `pending_interaction` 等批**：acp-agent 的工具调用若被项目的 `approval` 策略拦住（`mode: ask|strict`），job 会等人点头——`gofer job interactions <id>` 看被求批的工具调用与可用 optionId，`gofer job answer <id> <interaction-id> <optionId>`（或在 web 交互面板点按钮）作答；无人作答到 `timeout_sec` 就按 `on_timeout` 兜底（默认 reject）。

### 5a. `capture_diff` 与 exec job

项目配置的 `capture_diff` 接受 `auto`（默认）、`on`、`off`；历史布尔值 `true`/`false` 仍兼容。`auto` 会为 cli-agent 和 review-gated job 采集 tracked 未提交改动，保存 stat 摘要和结果目录中的 `changes.diff`，普通 `exec` job 会跳过仓库扫描以避免固定开销。需要 exec 也保留审计时设 `capture_diff: on`，完全关闭时设 `off`。采集失败、非 Git 目录和超时都会优雅降级，不影响 job 终态。

### 5b. job 中断了怎么续（`job resume`）

codex/claude 因供应商容量错误、网络抖动或超时把 job 干掉一半时，**不要重派整份任务书**（新会话 = 重读上下文），用 resume 让它带着自己的会话继续：

```bash
gofer job show <源 job-id> | grep session_id     # 有 session_id 才能续
gofer job resume <源 job-id> --plan <plan-id> \
  --prompt "上一次运行因 <原因> 中断。先 git status / git log --oneline -5 判断进度，只完成任务书剩余项，不要重做已提交部分；汇报格式同前。"
```

前提：源 job 已终态（done/failed/timeout/cancelled 都行）、捕获到了 `session_id`（codex 靠输出 `session id:` 捕获，claude 靠 `--session-id` 注入；omp 需在 agent 定义加 `session_capture`/`session_resume`）、agent 有 resume 模板（内置 claude/codex）、同一 runner。**acp-agent 不需要 resume 模板**：它的 resume 是新开一个 acp-agent **持续会话** job、用协议 `session/load` 载入源会话，`--prompt` 可省（直接进 `awaiting_input` 等 `job say`），见 §3「ACP 持续会话」（agent 不支持 `loadSession` 或配了 `acp.load_session: false` 时直接报不支持）。resume 产生一个**新 job id**，`--plan` 照常可挂。命中瞬时错误会自动续跑一次（`auto_resume_max`）。

**cli-agent 的 resume 与首轮一致（环境 + 输出）**：续接 job 虽以 `exec` 载体运行（`job show` 里 agent 记为 `exec`、`resume_agent` 为源 agent、`resumed_from` 指向源 job），但执行时按**源 agent** 处理：① 输出——沿用源 agent 的 `output_format` / `ndjson_keep` / `ndjson_stdout` / `ndjson_stdout_path` 等，stdout.log 是最终答复、stderr.log 是过滤后的事件，`ndjson_kept/dropped`、`session_id` 与首轮同形，不再是原始 NDJSON；② 环境——进程环境按低到高叠加：`env_files`（源 job 声明的文件路径，续接请求里只记路径）< 源 agent 配置的 `env`（HOME、模型变量等，执行时从**当前** agent 配置解析）< 源 job（含更早的续接链）请求里的 `env` < 本次续接显式给的 `--env K=V`（REST `env`，会随新 job 的 request_json 保存，别放密钥）。继承来的值只在执行时注入子进程，**不会写进续接 job 的 request_json**（只留 `resumed_from` / `env_files` 引用）；job 详情里 env 值照旧脱敏。续接在 worker 上执行时同样按 worker 自己配置里的源 agent 取 env 与输出设置；job 级 env 本来就不随派发传给 worker，所以 worker 上的续接只继承源 agent 的 env。

### 5c. 并行派活用 `--worktree`

多个 agent job 在同一 checkout 里并行改代码会互相踩（`.git/index.lock` 残留、互相覆盖）。加 `--worktree` 让每个 job 在 `<仓库顶层>/tmp/gofer/wt/<job-id>` 的独立 git worktree 里跑，提交落在分支 `gofer/<job-id>`，主 checkout 不动；结束后分支保留供人合并，`gofer job worktree ls/rm` 清理。`--cwd` 仍相对项目根，会映射进 worktree 的同一子路径；嵌套仓库按 `--cwd` 所在的最近 git 顶层建。任务书里要提醒 agent：**不要 `git checkout` 别的分支、不要 push**。

### 5d. 日志与隧道诊断

server/worker/forwarder 都写 JSONL 文件日志（轮转、脱敏）：server `<config-dir>/run/serve.log`，worker `run/worker-<id>.log`，`tunnel forward` 默认 `run/tunnels/forward-<时间>-<pid>.log`（`--log-file`/`--log-dir` 可改，`--quiet` 只静默终端）。daemon/worker 另有 `run/worker-<id>.out.log`（serve 为对应 `.out.log`），只承接 panic 和非 slog 的 stdout/stderr；worker 升级交接后新进程继续追加该文件，排障时把它与结构化 `.log` 分开看。`worker -d` 的显式后台进程会脱离 local job 的 Windows job object，提交它的 job 结束后仍可在线；普通 job 的子孙没有显式脱离时仍会随取消/超时一起终止。Windows 上后台运行（`-d`）的进程没有控制台，它启动的非交互子进程（agent 探测、git、job 进程、ACP agent）统一带 `CREATE_NO_WINDOW`，不会逐个弹出 cmd 窗口；交互 pty 与 `-d` 自身的 detached 启动不受影响。一次隧道在三端共用同一 `tunnel_id`（server 经 `X-Gofer-Tunnel-Id` 回传），`rg '"tunnel_id":"…"'` 三个文件即可重建全链路；`first_byte_ms`/`bytes_up|down`/`packets_up|down`/`close_reason` 能判断"慢在 relay、设备还是往返次数"（`duration_ms / packets_up` ≈ ping RTT = 协议逐包 stop-and-wait）；`GOFER_TUNNEL_TRACE=1` 逐报文记 `tunnel.datagram`。详见仓库 `docs/runbook/tcp-tunnel.md`。

**job 日志（stdout.log / stderr.log）**：ndjson agent（`omp --mode json`、`claude --output-format stream-json`）在 agent 定义里写 `output_format: ndjson` 后**stdout=最终答复、stderr=过程事件**（逐 token 增量在**采集时**就被丢掉；事件一行一个、单行 ≤2KB，超长标 `…(truncated)`；`session` 行恒留，`ndjson_keep` 调白名单，`ndjson_raw: true` 才另存未过滤的 `<result_dir>/stdout.raw.log`）；`gofer job show <id>` 的 `ndjson_kept`/`ndjson_dropped`/`ndjson_truncated` 就是保留/丢弃/截断行数，web 详情页对**事件流（stderr）**有「结构化视图」切换。**运行中看日志的成本**：SSE `GET /v1/jobs/{id}/stream` 支持 `?tail=N`（两路各从最后 N 行起，上限 5000）、`?from=<字节>`（stdout 续传）、`?stderr_from=<字节>`（stderr 续传）；每个 `log` 帧带 `off`（该帧结束处的绝对字节偏移），单帧 ≤256KB（大日志分块回放，不再一次读整个文件）。Web 详情页运行中首次连接只拉最后 500 行，断线重连按 `off` 续传，「加载更早」放大 tail 重连（上限 5000 行）；stderr 缓冲 256KB、stdout 2MiB；日志面板只渲染末尾 5000 行（手机 1000 行）并分帧插入，切 stderr 不再卡顿。

**看不到输出**先分清是"agent 没输出"还是"被过滤了"：`ndjson_raw` 打开重跑一次即可对照。

### 5e. 只读 job（`--read-only`）

审查/分析类任务不想让 agent 动手改文件时，`job run --read-only`（MCP 的 `gofer_run_job` 用 `read_only: true`）：

只读任务务必显式带 `--read-only`。如果 job 显示 `waiting_dir`，说明目录被另一个可写 job 占用；顶层目录派活时用 `--lock <子项目>` 收窄范围，也可以继续等待，或改用 `--shared-dir` 放弃独占、`--worktree` 使用隔离目录。

顶层目录包含多个仓库时，优先为每个 job 显式声明 `--lock <子项目>`。项目显式开启 `dir_lock_mode: repo` 后，cwd 下存在嵌套仓库的可写 job 必须声明一个或多个 `--lock`，或明确使用 `--shared-dir` / `--exclusive-dir`；否则提交会列出可选仓库并拒绝。只读、interactive、worktree job 不受此准入限制；没有嵌套仓库时沿用 cwd 锁行为。

`--lock-wait <秒>` 按 job 限定等目录锁的时长（0 = 不限，是否允许由 server 的 `dir_lock_allow_unbounded_wait` 决定；默认上限 `dir_lock_max_wait_sec`）。等锁超时的报错会写出**实际锁路径与持有者**，不是 cwd。串联多个长 job 时给它显式设值，别撞 server 默认。

```bash
gofer job run -p <project> -a codex --read-only --prompt "只做审查：列出这次改动的问题，不要修改任何文件"
```

- **cli-agent**：追加该 agent 的 `read_only_args`（内置 codex `-s read-only`、claude `--permission-mode plan`；omp 无对应开关，须自己配 `agents.<key>.read_only_args`）——沙箱是 CLI 自己的，不是 gofer 的口头约定。
- **acp-agent**：`acp.modes.read_only` 映射到 agent 的 mode id，在 prompt 前 `session/set_mode`；agent 的 `availableModes` 不含该 id 就**失败**（不会在可写模式下跑完还自称只读）。
- **exec agent 一律拒绝**（argv 由调用方写死，gofer 无法约束）；没配只读模式的 agent 提交即 400，错误会点名要配哪个键。
- 只读随 job 落库（`job show` 打 `read_only: true`、list 打 `[ro]`、web 列表/详情有徽章），**resume 继承只读**——想把只读改成可写只能新开 job。

### 5f. 任务书模板（`-t` / `--var`，SUP-01 P5）

每天都复制同一段"通用约束 + 交付要求"时，把它写成**模板**：模板是 server 上的一份 md 文件
（YAML frontmatter 给 job 默认值 + 正文即 prompt），提交时只传模板名和变量值。

```bash
gofer template ls [-p <project>]                 # 这份 project 能用哪些模板(项目目录优先, 再是全局目录)
gofer template show <name> [-p <project>] --var tasks="…"   # 预览: 来源路径 + 变量表 + 服务端渲染后的正文
gofer job run -p <project> -t impl-batch --var tasks="1. 加 foo 子命令" --var base=main   --prompt "补充：不要动 web/"                     # --prompt 会追加在模板正文之后
```

- **模板放哪**：项目的 `<host_path>/.gofer/templates/<name>.md` 优先，其次是 **server** 的
  `<config-dir>/templates/<name>.md`（`GOFER_CONFIG_DIR`，默认 `~/.config/gofer/templates/`）。
  同名时项目副本赢；项目目录在 server 上不可读（worker-only 项目）时只有全局目录可用。
  **模板文件在 server 那台机器上**，`gofer template show` 因此是去问 server（不是读你本地磁盘）。
- **frontmatter 能写什么**（白名单，别的键解析即报错）：`agent`、`runner`、`timeout_sec`、
  `tags`、`verify`、`verify_timeout_sec`、`review`、`read_only`、`worktree`、`fallback_agents`、
  `desc`，以及变量声明 `vars: {name: {default, required, desc}}`。**显式旗标 > 模板默认 > 项目默认**：
  模板只填你没给的那些（`--timeout 60` 压过模板的 `timeout_sec`）。
- **正文变量**：`{{name}}` 用 `--var` 的值（没给就用 `default`；`required: true` 又没给值 → 提交报
  400 并列出缺哪个）。内置 `{{project}}`/`{{cwd}}`/`{{date}}`/`{{head}}`（`head` 是该项目的
  `git rev-parse --short HEAD`，不是 git 仓就空 + 告警），以及**只对挂 todo 的提交可解析**的
  `{{plan_title}}`/`{{plan_description}}`/`{{todo_title}}`/`{{todo_note}}`/`{{todo_id}}`（PLAN-02）。
  `{{include: common.md}}` 从**同一目录**
  拼一份片段（只一层：被 include 的文件不再展开 include）。没声明的 `{{x}}` 若没人给值就原样保留并告警。
- **示例**：仓库 `docs/examples/templates/{common.md,impl-batch.md}` 就是一对（后者 include 前者），
  拷到 `<config-dir>/templates/` 即可用：`cp docs/examples/templates/*.md ~/.config/gofer/templates/`。
  示例正文用 `{{plan_title}}/{{todo_title}}/{{todo_note}}`，即**给 plan todo 派发用**的任务书
  （`plan set-todo <id> --status ready --template impl-batch`）；直接 `job run -t impl-batch --var tasks="…"`
  时这几个内置变量渲染成空 + 告警，任务正文由 `--var tasks` 给。
- **审计**：job 的 `request_json` 存的是**渲染后的 prompt**，同时留着模板名与变量值——重跑（`job rerun`/
  重建）照那份 prompt 跑，**不会**再渲染一次。

## 6. worker 配置模式：LEGACY vs POLICY（P3 起）
一台 worker 有两种模式，决定它能跑哪些 project——排障（§7）前先分清它是哪种：

| 模式 | worker.yaml | project 来源 |
|---|---|---|
| **LEGACY** | 有 `projects:`、**无** `roots:` | worker **本机这份文件**里的 project |
| **POLICY** | 有 `roots:` | **server 下发**（本机 `projects:` 段被忽略，有则告警） |

- POLICY 下 project 集合**完全**由 server 下发；worker 用 `roots` 把 server 的逻辑 `host_path` 映射成本机真实目录：`{ from: <server逻辑前缀>, to: <本机前缀> }`，**最长前缀优先**。加 project 到已有 root 下 = server 改一行 + reload，worker 零改动。
- 自检：`gofer config validate worker`（按模式给判据 + 校验 roots：`to` 目录存在、`from` 不重复、重叠提示）；`gofer project list`（列当前生效 project 及**映射后的本机路径**）。
- 配置场景（恒等 / 跨盘 / Windows / worker 独有 project / 保持 LEGACY 等，OS 无关）见 [`references/worker-config.md`](references/worker-config.md)；LEGACY→POLICY 迁移分步（含回滚 + 路径核对表）见 [`references/setup-recipes.md`](references/setup-recipes.md) 配方 3。

## 7. 排障

| 现象 | 处理 |
|---|---|
| `unknown project "xxx"` | project 没在**主机 server** 注册；用 §1 确认正确 key。 |
| worker 执行报 project 不在该 worker | 先分清 worker 模式（§6）：**LEGACY** → 该 project 要在 worker.yaml 也定义（`allowed_runners: [local]`）。**POLICY** → project 来自 server，检查：① server 侧该 project 的 `allowed_runners` 是否含这台 worker 的 runner；② worker 的 `roots` 是否覆盖该 project 的 `host_path`（不覆盖 → `path_outside_roots` 被拒）；③ worker 是否已应用策略（重连窗口内会短暂 `policy_pending`）。`gofer config validate worker` + `gofer project list` 自检。 |
| agent 报 `unknown agent` | 该 agent 未在这台 worker 装 / 定义（如容器没装 codex）；换已装的 agent，或到该 worker 装上。 |
| 连不上 / 401 | 主机 `gofer server` 未起，或 `$GOFER_CONFIG_DIR/.env` 的 `GOFER_SERVER_ADDR/TOKEN` 与 server 不一致。 |
| 有 job 但看不到输出 | `--sync` 只回状态；输出用 `gofer job logs <id>`。 |
| `agent "codex" is interactive-only` / `has no batch mode` | 主机 agent 定义里误加了 `interactive: true`（旧语义=仅 pty）。批处理 + pty 两用应写 `args` + `interactive_args`，删掉 `interactive: true`，重启 serve。 |
| `project "x" does not allow interactive jobs (allow_interactive)` | 项目未开 `allow_interactive: true`（server config.yaml 或 web 项目表单复选框）。 |
| 提交后 stderr 出现 `warning: --timeout … exceeds the project ceiling` | 超时被 clamp 到上限（默认 3600s）。要么拆 job，要么让管理员改 `server.max_job_timeout_sec` / 项目 `max_timeout_sec`。 |
| job 状态 `recovering` | worker 断线，server 在恢复窗口内等它重连；**不要重派**，等它回到 running 或 failed(worker lost) 再处理。 |
| job 中途失败（供应商 `at capacity` 等） | `gofer job resume <id> --prompt "…"` 续跑同一会话（§5b），不要重派整份任务书。 |
| `gofer project list` 报 `worker.yaml: no such file` | 这是纯客户端节点却设了 `GOFER_RUN_MODE=worker`。改为 `GOFER_RUN_MODE=client`（见 `references/client-config.md`）。 |
| agent 改出的文件里混进制表符 / 空字符 / 回车，Markdown 行内代码或注释被吃掉 | Windows 主机上 agent 用 PowerShell 双引号字符串写文件，反引号被当成转义符（见 §4）。任务书要求改文件用 apply_patch，并在提交前用 `git grep -nP "[\x00-\x08\x0b\x0c\x0e-\x1f]"` 自查。 |

## 8. 离开电脑：把终端会话交给 web 接着聊（session relay）

人要离开时，让**这个终端会话**停下来等人的那条消息进 gofer web「会话」页，人在 web（手机也行）回复，回复直接注入**同一个**会话继续跑。不开 pty、不重开会话；靠 Claude Code / Codex 的 Stop hook 阻塞等待实现。

```bash
gofer init hooks                  # 一次性: 把 hook 写进 ./.claude/settings.json (--agent codex → ./.codex/hooks.json; omp → ./.omp/extensions/gofer-relay.ts; jcode 无项目级, 须 --global; --agent all = 四个都写)
gofer init hooks --agent all --global   # 用户级一次装好, 所有工作区生效(见下「全局安装」)
gofer session relay auto          # 回到缺省三态(server 按判据决定; 也可以 on / off, 见下)
gofer session ls                  # 看哪些会话在等回复(waiting_reply 置顶), RELAY 列显示 on / off / auto；NAME 列是 Claude Code 自己的会话名（`peer_name`，其它 Claude 会话用它作 SendMessage 地址，没有为 `-`）
gofer session say <id> "<回复>"   # web 输入框的 CLI 等价(= 答最新 OPEN turn); 回复 /off = 关掉中继并让会话正常停下
gofer session say <id> "<回复>" --deliver   # 无 turn 在等也送: 消息直接敲进该会话的 tmux 终端(§9.1 A)
gofer session say <id> "<回复>" --deliver --takeover   # 没有 tmux 时: 起一个新进程 `--resume` 接管该会话并把这条消息作首条输入(§9.1 B)
gofer session show <id>           # relay 行显示当前 mode + 判定依据(空闲多久 / 距上次人工输入多久)
gofer session watch <job-id>      # 登记当前会话等待 job 终态；Stop 等待时完成通知会注入回终端
gofer init hooks --remove         # 卸载
```

**各 agent 的 hook 能力**（`gofer init hooks --agent <名>`；会话登记/状态/最后一条消息/进行中预览都可见，差别在能不能「停下等 web 回复并注入」）：

| agent | 配置落点 | 登记/状态 | web 回复注入（Stop 阻塞） |
|---|---|---|---|
| claude | `.claude/settings.json`（`--global` → `~/.claude`） | 是 | 是（`decision:block`） |
| codex | `.codex/hooks.json`（`--global` → `~/.codex`；codex 按内容哈希要求在交互模式批准一次） | 是 | 是（已真机四步通过） |
| omp | TS 扩展 `.omp/extensions/gofer-relay.ts`（`--global` → `~/.omp/agent/extensions/`；`PI_CODING_AGENT_DIR` 可改） | 是 | 是：扩展只转发事件给 `gofer hook omp`；omp 的 handler 上限 30s，所以等待在后台子进程，web 回复用 `sendUserMessage(followUp)` 作为新一轮消息送回 |
| generic（自研 agent） | 由接入方的 agent 自己调 `gofer hook generic --agent <gofer agent key>`（stdin 与 claude 同形 JSON，事件 SessionStart/UserPromptSubmit/PostToolUse/Stop/Interrupt/SessionEnd/Notification）；没有 `init hooks` 安装项 | 是 | 是（Stop 阻塞，输出同 claude）；最后一条消息只取载荷的 `last_assistant_message`，不读 transcript |
| jcode | `~/.jcode/config.toml` 的 `[hooks]`（`JCODE_HOME` 可改；无项目级，`-o <dir>` = 一个 JCODE_HOME 目录） | 是（hook 为 fire-and-forget） | **否**（jcode 除 pre_tool 外的 hook 都是后台分离执行，只能观察；要传话用 tmux 的 `session say --deliver`） |

omp 扩展取消等待（人在终端输入、会话关闭）时，会先自己上报一次 `Interrupt` 再结束后台等待进程（Windows 上结束进程是硬终止，等待进程来不及自己上报），所以显式 `on` 的 OPEN turn 会立即被关成 `released_by=interrupted`，不再等到超时。jcode 的 turn_start/turn_end 在 TUI 下会触发（`jcode run`/repl 无头模式不触发），gofer 据此更新「最后一条消息」和空闲状态（只观察、不等待）。

jcode 的 `[hooks]` 每个事件只能配一条命令：该事件已有用户命令时 gofer 不覆盖并给出提示；空串视为未配置。omp 扩展文件带 `@gofer-managed-omp-extension` 标记，同名的非 gofer 文件拒绝覆盖（`--force` 才换）。

**全局安装**（让所有工作区生效，不必逐项目装）：`gofer init hooks --agent all --global`。写 `~/.claude/settings.json`、`~/.codex/hooks.json`、`~/.omp/agent/extensions/gofer-relay.ts`、`~/.jcode/config.toml`；全局装完会扫描当前目录与 config 里登记的项目，列出已有**项目级** gofer hooks（同一事件会在用户级 + 项目级各触发一次，claude 对相同命令会去重，其余按重复处理），并给出清理命令 `gofer init hooks --remove --agent <名> -o <项目目录>`（同一目录以 `D:/x` 与 `D:\x`、或仅大小写不同的写法出现时只列一次；Windows 下大小写不敏感）。codex 的全局 hook 同样要在交互模式批准一次。不在 gofer 项目列表里的目录，会话以「无项目」登记，中继/传话可用，唤醒（resume）需要项目。gofer job 里跑的 agent 因 `GOFER_JOB_ID` 直接放行，不受影响。

开关是**三态**（按会话存在 server，缺省 `auto`）：

| mode | 含义 |
|---|---|
| `on` | 每次停下都在 web 等你回复（今天的显式开关）；终端有人输入、web 回复 `/off`、或 `relay off` 才关掉 |
| `off` | 从不等；已经打开的 turn 会被释放 |
| `auto` | server 按判据决定本次停下要不要等（下面两条） |

约定：

- 用户说「打开中继 / 我要离开了 / 交给 web」→ 执行 `gofer session relay on`，然后正常结束回合即可；之后每次回合结束都会在 web 等回复，直到 web 回复 `/off`、终端有人输入、或 `relay off`。
- **忘了开也不要紧（自动布防）**：`auto` 模式下 server 按两条判据自动布防，**不用拨开关**：
  1. **键盘空闲**（`session.auto_relay_idle_sec`，默认 300 = 5 分钟，写 `0` 关）：hook 每次 Stop 上报"键鼠已空闲多少秒"，人离开超过阈值就等 web 回复（web 列表显示 `auto (idle 12m)`）。
  2. **距上次人工输入**（`session.auto_relay_turn_sec`，默认 900 = 15 分钟，写 `0` 关）：**容器里的 hook 测不到主机键盘**（Linux 无 X11 → 空闲值恒为"未知"，这条正是为你这种场景准备的），于是改看这个会话里人最后一次输入（SessionStart / 非注入的 UserPromptSubmit）距今多久，到了阈值同样自动布防（web 列表显示 `auto (no input 22m)`）。
- **进行中预览**：执行 `gofer init hooks` 后，Claude/Codex 的 PostToolUse hook 会从会话转录读取最新 assistant 文字，以「进行中」心跳更新 Sessions 列表、抽屉和工作台线程；Stop 上报最终回复时会清空进行中预览。`session.progress_interval_sec` 默认 30 秒，设为 `0` 关闭。每个工作区至少执行一次 `gofer init hooks` 才会安装该 PostToolUse 条目。
- **人回来即放行**：判据一开的等待，hook 每 ≤5s 重探空闲值，人一碰键鼠就放行；判据二开的等待没有可探的东西，靠**你的动作本身**——按 Esc 结束等待，或直接在终端输入一条（UserPromptSubmit / Interrupt 事件一到达，server 就把 turn 关成 `released_by=user_returned`）。**显式 `on` 的等待**不被"终端输入"关闭（输入只把 mode 降回 `auto`，turn 靠 `/off` / `relay off` 结束）；但**按 Esc 中止等待中的 Stop hook** 例外：hook 被信号杀掉前会上报 `Interrupt`，server 把该 turn 关成 `released_by=interrupted`、会话回 `idle`、**开关保持 `on`**（下一次 Stop 照常中继；web 不再显示一个没人听的"等回复"）。
- 依赖：Windows/macOS 探得到键盘；**Linux 需要 `xprintidle`**（X11），缺失或 Wayland/无桌面时为"未知"——此时**自动走判据二**，不再退回到"只能手动拨开关"。
- web 会话列表里找到该会话也可以直接点 auto / on / off 切换，**下一次回合结束**生效（会话正在跑长任务时最常见，能接上）；已经停在空闲提示符、且人一直没离开过的会话没有 hook 在跑，仍需在终端输入一次。
- **已经空闲的会话也能送话（tmux 注入，阶段 2-A）**：没有 OPEN turn 时，web 会话抽屉的输入框变成「送入终端」，`--deliver` 是它的 CLI 等价。server 会在该会话登记的执行机上起一个内部 exec job，先确认 pane 还在、前台是 agent CLI（白名单默认 `claude|codex|omp|node|gemini|opencode`，`session.inject_commands` 可配），再把 `[gofer web 回复] …` 逐行 `tmux send-keys -l` 敲进去（文本按行拆、单引号转义，上限 8KB）。成功即"已送入终端 ✓"，会话状态回到 running，并记一条带注入 job id 的审计行。
  - **前提**：会话必须跑在 tmux 里（容器/主机入口建议 `tmux new -A -s claude`），且登记了执行机。
  - **容器里跑的会话要能被送话**：容器内必须起一个 gofer worker，并把 `GOFER_HOOK_RUNNER=<worker-id>` 配进该容器的 `.env`（hook 用 `--runner` 的值）；否则会话登记的 runner 为空，server 只能回"未登记执行机"。纯客户端节点（`GOFER_RUN_MODE=client`）不会再假装登记成 `server`。
  - 送不进去时按原因给话：`deliver_failed:<原因>`（agent 的送话命令失败）、`session_alive`（接管被拒，原进程还在线）、`no_tmux`（不在 tmux 中 —— 见下条走 B）、`no_runner`（未登记执行机）、`ended`、`inject_failed:pane_missing|pane_busy:<cmd>|runner_error`（pane 没了（会自动改走 B）/ 前台是别的程序如 vim / 执行机出错）。
  - 注入 job 是 exec 类型，因此该 project 需要 `allow_exec: true`（worker 侧还要 `guards.allow_exec` 不收紧），否则会以 `inject_failed:runner_error` 失败。
- **没有 tmux 时：起新进程接管（`--resume` pty 接管，阶段 2-B）**：会话不在 tmux（典型：Windows 主机的 Windows Terminal，或 pane 已消失）时，web 抽屉会给出「起新进程接管并发送」按钮（点开二次确认："将用 `<agent> --resume` 起一个新进程接管该会话，原终端将不能继续"），CLI 等价面是 `--deliver --takeover`。server 用该会话的 `session_id` 在同一 runner、同一项目相对目录起一个**交互 pty job**（`claude --resume <sid>` / `codex resume <sid>` / `omp --resume <sid>`），把 `[gofer web 回复] <文本>` 作为它的**首条输入**（pty 首次输出后安静 `session.takeover_input_delay_ms`，默认 1500ms，最多等 10s 再写），web 自动跳到该 job 的终端（`?attach=1`）继续聊。
  - **前提**：agent 有交互 resume 模板（内置 claude / codex / omp）、项目 `allow_interactive: true`、会话 cwd 能换算成执行机上的项目相对路径；否则回 `no_resume_template` / `interactive_not_allowed` / `cwd_outside_project`。接管 job 是 exec 载体但按**源 agent** 过访问门，不需要 `allow_exec`。
  - **接管后原终端会被停用**：会话置 `handed_off`（web 显示「已接管 → job 链接」），原终端的 Stop 只放行、不再开 turn，hook 会在 stderr 打一行"该会话已于 <时间> 在 web 接管（job <id>）…本终端的中继已停用"。两个进程写同一 CLI 会话会分叉，所以继续请在接管终端里。
  - **想回原终端**：web 抽屉「解除接管」（`POST /v1/sessions/{sid}/release-takeover`）：先 cancel 接管 job，再把会话置回 `idle`、清空接管标记，原终端恢复中继。
  - 监控：`gofer job ls --tag relay-takeover` 列出所有接管 job，`gofer job show <id>` 看事件（含 `job.input_injected`：首条输入何时写入、写了多少字节）。
- **Claude 会话名（`peer_name` / `peer_name_source`）**：claude hook（跑在 Claude 所在机器上）每次心跳 best-effort 读 `$CLAUDE_CONFIG_DIR`（缺省 `~/.claude`）下 `sessions/*.json`，按 `sessionId` 匹配取 `name` / `nameSource` 随会话上报（可选字段；读不到 / 格式变了就忽略；这是 Claude Code 内部未公开格式）。改名在下次心跳更新。Web Sessions 卡片、工作项抽屉的会话行、会话详情都显示该名字并可一键复制；`session ls` 有 NAME 列、`session show` 有 `name:` 行。老 hook 不上报则不显示。
- **会话无心跳自动标离线**：Claude Code 进程被直接杀掉时不会发 SessionEnd，会话会永远显示「执行中」。server 每 60s 扫一次：非 `ended` / `handed_off` 的会话，`last_seen_at` 距今超过 `session.offline_after_sec`（默认 1800 = 30 分钟，`0` 关闭，热重载生效）就标成 `offline`（灰色「离线」徽标 + 最后心跳时间；`gofer session ls` 的状态列显示 `offline`；Sessions 列表默认就显示它，不在「显示已结束」里）。有 OPEN 的中继 turn（Stop hook 在长等待、期间没有心跳）的会话不会被误判——静默时长从该 turn 的截止时刻起算。离线会话收到任何新的 register / heartbeat 就恢复为 running（带状态的事件按事件定）。`offline` 同 `ended` 一样可以唤醒（`takeover-plan` 的 `message` 会写「原进程可能已退出」）。
- **唤醒会话（含已结束的）**：终端关了、会话变成 `ended` 之后，想接着聊就「唤醒」：web 的 Sessions 每一行（勾「显示已结束」可见已结束的）和会话抽屉顶部（工作台里嵌入的抽屉也有）常驻一个「唤醒 / 接管」按钮，**不用先发送失败**；CLI 是 `gofer session resume <id> [--input "首条消息"] [--plan]`（`--plan` 只问能不能、怎么起，不起进程）。它起的是和上面「接管」同一种 `--resume` 交互 pty job（同样要求 agent 有交互 resume 模板、项目 `allow_interactive: true`、cwd 能换算成项目相对路径），成功后 web 跳到新 job 的终端（`/jobs/<id>?attach=1`）。不能唤醒时按钮灰显，悬停显示中文原因（来自 `GET /v1/sessions/{sid}/takeover-plan`，会话列表每行也带 `can_resume` / `resume_reason` / `resume_message`）。会话仍显示未结束时（终端可能还开着）也能「接管」，但要先关掉原终端——两个进程写同一个会话会互相覆盖。**唤醒目录的选择规则**（`claude --resume` 按会话**启动时**的目录找会话文件，而登记 cwd 可能是会话中途登记的临时 worktree、早已删除）：① 先用会话登记的 `transcript` 路径所在目录名做验证——把「登记 cwd 及其各级父目录、项目根」逐个按 Claude 的规则编码（非字母数字一律变 `-`，所以只验证、不解码），编码后等于该目录名且在执行机上存在的那个就是原始启动目录；② 否则用登记 cwd（存在时）；③ 再不行回落到项目根（执行机视角）；登记 cwd 根本不在项目里且无法验证时仍回 `cwd_outside_project`。执行机是 worker 时 server 无法远程判断目录是否存在，只按字符串推断（说明里会写「未核对该目录是否存在」）。`takeover-plan` 返回 `cwd`（项目相对）/ `cwd_abs`（执行机绝对路径）/ `cwd_source`（`transcript|registered|project_root`）/ `cwd_reason`（一句中文依据，不再重复路径）；`message` 精简为「一句结论（含目录）+ 一句依据」，`gofer session resume --plan` 打印 `message` 与 `cwd_abs`，不再另印 `cwd_reason`。另外 hook 的每次心跳都会带当前 cwd，服务端记为 `last_cwd`（会话行 `last_cwd`，抽屉显示「当前目录」），**仅用于显示**，不改变登记 cwd，也不参与唤醒目录决策。唤醒后会话是 `handed_off`；接管 job 结束或「解除接管」后，**本来已结束的会话回到 `ended`**（不是 `idle`）。
- **接管前先看进程还在不在（`session_alive`）**：走接管（含显式「唤醒」）前，原进程若仍在线就拒绝，避免两个进程写同一会话分叉：没有 `deliver_command` 的 agent（claude/codex/omp…）看心跳——最近一次 hook 心跳在 `session.takeover_alive_sec`（默认 120 秒，`0` 关闭）内算在线，原因码 `session_alive`（409）；`ended` / `offline` 不受限。
- **在线送话命令 `deliver_command`（路径 C）**：agent 配了 `deliver_command`（+ 可选 `deliver_stdin: true`，worker 需协议 ≥ v18）时，没有 OPEN turn 的送话先在会话登记的执行机上跑这条命令（内部 exec job，tag `relay-deliver`，按该 agent 过准入门并带其 env；变量 `{{session_id}}` `{{text}}`，文本带 `[gofer web 回复] ` 前缀）。退出码 `0` = 已送达（`path=command`）、`3` = 进程不在线（`not_running`，继续 tmux → 接管，且此时接管不受心跳保护限制）、其它 = `deliver_failed:<stderr>`（502，**不再**退 tmux，避免重复送）。web 普通「发送」对没有 Claude SendMessage 地址的会话也走这条阶梯。
- **自研 agent 完整接入（generic）**：hook 用 `gofer hook generic --agent <key>`；agent 配置加 `ndjson_usage_path`（用量，点路径如 `usage`，入 job 用量与统计，源 `ndjson:<agent>`）、`transcript_dialect: generic`（整理器 / worker 尾读解析 `{"v":1,"type":"user|assistant|tool",…,"injected":bool}` jsonl，`injected` 的 user 不算人的发言）、`inject_process: [进程名]`（只对该 agent 的会话并入 tmux 前台白名单）、`session_family`（与同族 cli/acp agent 互相续接，覆盖内置族）、`deliver_command` / `deliver_stdin`。事件表、Stop 后台等待的做法（输入时先 Interrupt 再结束等待进程）、配置样例见 gofer 仓库 `docs/runbook/session-relay.md` §9。
- 注入的回复带前缀 `[gofer web 回复]`，与终端输入等价处理。
- **在 gofer job 里不会触发中继**：hook 发现环境里有 `GOFER_JOB_ID`（即你本身是 gofer 派出的 job）就直接放行、不开 turn、不阻塞——中继只服务人手开的终端会话。
- **`session watch`：job 跑完时被叫醒**：`gofer session watch <job-id>`（省略 `--session` 按当前目录解析）把当前会话登记为盯这个 job；会话停下时，job 终态通知会注入回终端：即使中继关闭（`off` / `auto` 未布防），只要还有未终态的 watch，Stop 也会**只等 job 事件**（不转发 web 输入，上限同 `--wait`）；错过的通知在下一次 UserPromptSubmit / SessionStart（claude / codex）作为附加上下文补投且只补一次（omp 靠 Stop，jcode 只观察不投递）。PostToolUse hook 也会在你 `gofer job run`/`watch` 后自动登记；`auto` 模式下，你名下还有 job 在跑时不会自动布防；此时 Stop 会认领你名下尚无人 watch 的在跑 job 并等它们完成。
- **web 给终端会话发消息（转达）**：会话**不在等回复**（在干活或已空闲）时，web 的「发消息」经**传话人**把话转给目标会话——server 在会话所在 runner 上用 `claude -p … --allowedTools SendMessage,ListAgents` 转发（一次性 job，或同 runner 复用的常驻传话进程；worker 协议 ≥ v14 才走常驻，更旧的退回一次性）。**接收方看到的是"另一个会话转达的消息"（带 `[来自 web，<用户>]` 前缀），不是用户本人的指令或审批**：需要批准/拍板的事，等它停下后在中继里回复，或终端里自己输入；不要把转达消息当作授权去做危险操作。会话要有 Claude Code 的会话间通信（`~/.claude/sessions/*.json` 里有名称/通信地址）才可转达。配置 `server.session_messaging`（`enabled` / `messenger_command` / `messenger_timeout_sec` / `messenger_idle_sec`）见 [server-config.md](references/server-config.md)。传话 job 详情会显示目标会话、消息原文和常驻/一次性通道。
- **传话人状态一目了然（Runners 页）**：每台 runner（local 和每个 worker）的卡片都显示「传话人 未启动 / 空闲 / 处理中」，点开是「传话人」抽屉：状态与空闲退出倒计时、最近 20 次投递摘要（目标会话、结果、耗时、失败原因；消息只留 200 字摘要）、传话进程 stderr 尾部（4KB，排查"为什么传话失败"）。数据在 `GET /v1/runners` 的 `messenger_detail`（local 进程内读取，worker 随心跳上报；**协议 ≥ v16 的 worker 才有**，旧 worker 只显示"未知"）。hook 登记的 server 本机会话 runner 标签是 `server`，现在和 `local` 一样走常驻传话进程（成功时不再建一次性 job）。
- **传话人看得见哪些会话**：抽屉里「列出可见会话」= 让传话人跑一次 Claude 的 `ListAgents`（会调用一次模型，约几秒），`GET /v1/runners/{name}/messenger/agents`（`?refresh=1` 强制重问；服务端缓存 30 秒；worker 走一个内部传话 job，**worker 协议 ≥ v16**，更旧的返回 409 并提示升级）。返回 `{runner, fetched_at, cached, self, agents:[{name, short_id, kind, status, started}], raw_output}`；ListAgents 的文本里只有名称/短 id/状态/相对启动时间，**没有目录**，所以表格里的目录取自 gofer 已登记的同名会话，并标「已登记 / 未登记」（未登记的会话不能经 gofer 传话）。
- **「无需回复」**：会话停在等回复的 turn 上时，web 可点「无需回复」把该 turn 标成已读（`POST /v1/sessions/{sid}/turns/{id}/ack`，可撤销）。这**不是回答、也不结束等待**：turn 仍 OPEN、Stop hook 继续阻塞，只是从铃铛/工作台「等你」里消掉；之后照样可以 `session say` 作答。想让会话放行用 `/off` 或 `relay off`。
- 详见 [`references/commands.md`](references/commands.md) 的「session — 终端会话中继」。
- **想让手机响一下**：配个钉钉/飞书群机器人，事件订阅 `session.waiting`（不在默认集里，必须显式写），消息带直达会话的链接。配置见 gofer 仓库 `docs/runbook/im-notification.md`。

## 9. 传文件：`gofer tool cp`（不要再 base64 塞进日志）

要把一个文件搬到 worker 所在机器（或 server 本机）时，**不要**再 `base64` 进 job 日志再解出来——慢、有大小上限、还污染日志。用：

```bash
gofer tool cp ./firmware.bin w-plc:shop-floor/tmp/in/firmware.bin   # 推到 worker 项目目录
gofer tool cp w-plc:shop-floor/tmp/out/report.csv ./report.csv      # 从 worker 拉回
gofer tool cp ./x.tar server:build/tmp/x.tar                        # 目标是 server 本机（local 同义）
gofer tool cp ./x ./y --force                                       # 目标已存在必须 --force
gofer tool xfer ls [--state staged]        # 暂存区：在传/传过什么
gofer tool xfer show <id>                  # 单条详情（含失败原因）
gofer tool xfer rm <id>                    # 立即删掉暂存文件 + 记录
```

更省事的是让 **job 自己带着文件跑**（不用先 `cp` 到执行机）：

- `gofer job run … --upload ./a.bin:tmp/in/a.bin`（可重复）：提交前把本地文件暂存到 server，执行机在 agent **开跑前**放到 `<dest>`（按 job 的 cwd 解析）；这一步失败即 job `failed`，agent 不启动。
- `gofer job run … --collect 'tmp/out/*.csv'`（可重复）：job **结束后**（失败也收、verify 之后）按 glob 在 job 的 cwd 里收集，回传落进该 job 的 artifacts `collected/<项目根内相对路径>`，web 详情页与 `GET /v1/jobs/{id}/artifacts/collected/...` 直接可看可下。
- 限额：单文件 ≤ `server.xfer.max_bytes`（默认 256MB），单个 job 收集总量 ≤ `server.xfer.collect_max_bytes`（默认 1GB）；超出的文件跳过并列进该 job 的 `xfer.skipped`。

约定（踩坑点）：

- 远端写法就是 `<runner>:<project>/<项目内相对路径>`，`runner` 填 worker id 或 `server`（`local` 等价）。**路径按执行机解析、必须在项目根内**（与 `job run --cwd` 同一边界）；目标目录不存在会自动创建（项目根内）。
- **v1 只传单文件、不续传**。传目录先打包：`tar czf x.tgz dir/` 或 Windows `Compress-Archive -Path dir -DestinationPath x.zip`，传过去再解。
- 单文件上限 `server.xfer.max_bytes`（默认 256MB，超了报 `too large`）；**推送**本地算 sha256 并打印进度，**拉取**落本地前先写临时名再 rename，两端都校验 sha256。
- 失败原因**原样**打印、退出码 1：`exists`（加 `--force`）、`path escapes project`、`worker offline`（不排队，重跑即可）、`too large`；worker 侧单次传输超时默认 600s（`xfer_timeout_sec`）。
- **别传 `.env` / 私钥 / token**：gofer 不做内容审查，判断在人。
- 更懒的替代：文件就在某个 worker 的项目目录里、且 job 的输出够小时，与其传文件，不如让 job 把内容 `cat` 进 stdout（≤ 32KB 的 `result.json` 会随结果回传）。

## 10. 让 agent 自己登记等待：`gofer job wakeup create $GOFER_JOB_ID …`

"等 verify 出结果 / 等人回复 / 每小时看一眼"不必让 job 常驻轮询。**登记一条唤醒（wakeup）再结束**：条件到达时 gofer 自动起一次**续投**（有 session 就续同一会话，没有就用原请求 + 指令重跑），把登记时写好的 `instruction` 当作提示词。没有常驻进程——"事情办完没有"由续投的 agent 自己读当前状态判断。

```bash
# agent 在自己的 job 里（GOFER_JOB_ID 就是自己），到点再叫醒我
gofer job wakeup create "$GOFER_JOB_ID" --kind at --after 10m -m "检查 CI 结果并汇报"
gofer job wakeup create "$GOFER_JOB_ID" --kind every --every 1h -m "巡检一次 tmp 下的新失败 job"
gofer job wakeup create "$GOFER_JOB_ID" --kind cron --cron '0 9 * * 1-5' --tz Asia/Shanghai -f instr.md
gofer job wakeup create "$GOFER_JOB_ID" --kind event --event job.terminal --job-id "$OTHER" --status done -m "对方结束了，合并结果"
gofer job wakeup create "$GOFER_JOB_ID" --kind event --event interaction.answered -m "人回复了，按答复继续"

gofer job wakeup list "$GOFER_JOB_ID"        # 还有几条在等、触发了几次
gofer job wakeup show|disable|enable|rm <wid>
```

要点（都是设计取舍，别绕）：

- **`--event` 缺省监听自己**（`--job-id` 指定别的 job）。事件目录只有这 8 个：`job.terminal`（可 `--status done,failed,…` 过滤）、`job.verify_finished`、`job.needs_review`、`job.reviewed`、`job.fell_back`、`job.stalled`、`interaction.answered`、`session.takeover_released`；写错类型直接 400。
- **同一 wakeup 同一时刻只允许一个未终态续投**：期间再触发只累加 `coalesced_count`（`job show` 一行 `wakeups:` 与 web「唤醒」块都看得到），避免每小时叠一堆。前一个续投结束（终态）后，下一次触发才会再起一个。
- **`--mode once`（at/event 默认）触发一次即消费**；`every/cron` 默认 `continuous`。定时器**从创建/启用时起算、不补发**错过的周期（server 停机期间漏掉的 tick 合并成一次）。
- **默认 7 天过期**（`wakeup.ttl_sec`），到期自动停用并记 `job.wakeup_expired`；目标 job 被 retention 清理时唤醒级联删除。
- 无 session 的 job（exec、agent 没有 resume 模板）走**重跑**：原 argv/请求重放，`instruction` 追加到 prompt 末尾（exec job 只进事件）。
- 权限同 `job resume`：只能给**自己提交的 job**（或持有 `can_answer` 的 caller）登记；worker token 调 HTTP 一律 403。
- 事件 `job.wakeup_fired {wakeup_id, kind, reason, continuation_job}` 记在**目标 job** 上（另有 `job.wakeup_coalesced` / `job.wakeup_expired` / `job.wakeup_failed`）；续投 job 带 tag `wakeup:<wid>`。默认通知集**不变**，要 IM 提醒就显式订阅这几个事件。
- MCP 里同样可用：`gofer_wakeup_create` / `gofer_wakeup_list` / `gofer_wakeup_disable`（`job_id` 填自己的 `$GOFER_JOB_ID`）。web job 详情页的「唤醒」块可看列表 / 开关 / 触发历史 / 直接新建。

## 11. 运维与环境小抄（agent 常踩的点）

- **看 worker**：`gofer worker ls`（列表）→ `gofer worker show <id>`（连接状态、`gofer` 版本、`protocol: vN`、在跑 job 数、projects / agents、传话进程状态 `messenger`（`stopped|idle|busy`，取心跳里的实时值）、默认工作空间 `workspace`（不存在会提示）、`policy_rev/applied_rev/policy_pending`、被拒/降级项）→ `gofer worker projects <id>`（该 worker **实际生效**的 project 清单，排查"project 不在该 worker"先看它）。
- **runner 的工作目录**：每台机器有一个「默认工作空间」——`GOFER_WORKSPACE` 环境变量，缺省 `~/.gofer/workspace`（`gofer init server` / `init worker` 会创建它；`serve` 与 `worker` 启动时若发现它不存在也会自动 `MkdirAll` 并记一条 info 日志 `workspace.created`——升级后的老 worker 不用再手动建，创建失败只告警不阻止启动；`config.WorkspaceDir` / `config.EnsureWorkspaceDir`）。Runners 卡片的「工作目录」折叠区第一行显示它（不存在标红；正常情况下重启后即被自动创建，只有目录无法创建时才需把 `GOFER_WORKSPACE` 指向别处），其下是 worker 的 roots 映射（server 路径 → 本机路径，目录不存在标红）和各项目在该 runner 上的解析路径（解析不了的项目带 `policy_rejected` 原因标红）。只列**允许该 runner 运行**的项目（与 job 准入同一判断 `project.AllowsLocalRunner`：`allowed_runners` 为空或含 `local`/`server`）；server 本机行里只给其他 worker 跑的项目不再逐个检查（它们的 `host_path` 是别的机器的路径），折叠成灰色一行「另有 N 个项目仅在其他 worker 运行」（`dirs.other_projects`）。Runners 页的 worker 心跳时间由 server 对 `runners` 推送主题节流推送（每个 worker 至多 10s 一次）刷新，页面本地每秒重算「多久之前」，超过 2 个 ping 间隔（30s）显示 no heartbeat。数据在 `GET /v1/runners` 与 `GET /v1/workers/{id}` 的 `dirs`（local 由 server 解析，worker 随心跳上报，协议 ≥ v16）。传话人进程的工作目录兜底顺序：会话自己的 cwd → 本机默认工作空间（存在时）→ 家目录。
- **远程或本机重载 worker**：远程用 `gofer worker reload <id> [--reason "新增 tunnel 白名单"] [--timeout 秒]`（别名 `rl`），本机用 `gofer worker reload --local [<id>] [-c <server-config>] [--timeout 秒]`。本机路径通过 PID 对应的 Unix SIGHUP 或 Windows 命名事件触发，并等待 `run/worker-<id>.reload.json`；结果字段为 `rev`、`path`、`changed`、`restart_required`、可选 `error`，默认等待 10 秒。远程路径不重启、Windows worker 同样可用；热生效：agents / roots / guards / labels / max_concurrent / tunnel 白名单；`worker_id`、`server_link`、storage、`xfer_timeout_sec` 要重启。需 `can_admin`（远程）。
- **从零登记 worker**：管理员用 `gofer worker add <id> [--labels label] [--project key]` 调用 `POST /v1/workers`，输出一次性 worker token 和下一步 `gofer init worker --server … --id … --token … --yes` 命令；删除用 `gofer worker remove <id>`，会移除 worker runner、项目 allowlist 并断开在线连接。也可在 Runners 页面「添加 worker」完成同一操作。
- **向导登记路径**：`gofer init worker --server <addr> --admin-token <管理员 token> --id <id> --project <key> --yes` 只把管理员 token 用于本次登记，不写入磁盘；向导把 server 返回的 worker token 写入 `.env`，worker.yaml 只保留 `token_env`，随后执行 doctor。已有 token 时仍可省略 `--admin-token` 走离线回退。
- **server 配置重载**：`gofer serve reload -c <config> [--timeout 秒]` 通过 PID 对应的 Unix SIGHUP 或 Windows 命名事件触发，并等待 `<配置目录>/run/serve.reload.json`；结果字段为 `rev`、`path`、`changed`、`restart_required`、可选 `error`。也可用 Unix SIGHUP 或任何平台 `POST /v1/config/reload`（web 设置页「重新读取文件」，需 `can_admin`）。本机 worker 用 `gofer worker reload --local [<id>]`，等待 `run/worker-<id>.reload.json`。`server.workers` 与 type=worker 的 `runners` 增删/改 token 会热生效；`server.callers` 仍在 server 启动时构造，变更继续列在 `restart_required`；peer-http 等启动级 runner 类型也列在 `restart_required`。
- **HTTPS 入口（手机 PWA）**：`server.tls: {addr, cert_file, key_file}` 在原 HTTP 监听之外**另开**一个 HTTPS 监听（同路由同鉴权；HTTP 不变，CLI/worker 继续走 HTTP；改它需重启）。`gofer tool cert [--out-dir <dir>] --hosts <DNS或IP,…>` 生成本地 CA + 服务器证书（`ca.crt/ca.key/server.crt/server.key`，再跑复用已有 CA）；Android 装 `ca.crt`（设置 → 安全 → 加密与凭据 → 安装证书 → CA 证书）后用 `https://<IP>:<port>` 打开并「安装应用」即得独立窗口 PWA / Web Push。证书与私钥别入库、别进日志、别贴进汇报。完整步骤见仓库 `docs/runbook/https-pwa.md`。
- **job 环境清洗**：gofer 起的 job/子进程**默认**剔除 `GOFER_TOKEN` / `GOFER_SERVER_TOKEN` / `GOFER_WORKER_TOKEN`、**`GOFER_CONFIG_DIR`**（不继承，否则下一个 `gofer` 会读到 server 的 `.env` 里的操作员 token）以及 Claude Code 会话标记（`CLAUDECODE`、`CLAUDE_CODE_ENTRYPOINT`、`CLAUDE_CODE_SESSION_ID`、`CLAUDE_CODE_MESSAGING_*`、`CLAUDE_PID` 等）——于是 job 里的 claude 不会误当成上级会话的子进程。用户自己的设置类变量（如 `CLAUDE_CODE_MAX_RETRIES`）照常传。因此 job 里的 `gofer` 用 gofer 注入的 `GOFER_JOB_TOKEN`（作用域仅限本 job，不是用户身份），**不要指望从父环境继承配置目录/用户 token**；要额外清单用 `server.job_env_denylist`，要放行用项目的 `job_env_allow`。`--env K=V` 的值会存进 `request_json`，别放密钥。
- **发版/换二进制**：从**打了 tag 的提交**构建（`make` 的版本号取自 `git describe --tags`，脏工作树或 tag 之后的提交会带 `-dirty` / `-N-g<hash>` 后缀）；CLI（容器）、主机 server、web 前端要同步升级到同一版本（CLI 比 server 旧会缺命令/字段）。**主机 server 上有本机 runner 的 job 在跑时不要重启它**：本机 job 是 server 的子进程，重启会让它们被判 `failed: orphaned: serve restarted…`（只有 worker 上的 job 能经 `recovering` 等重连）；先 `gofer job list --status running` 看，没有再换。
- 后台运行日志：server `<config-dir>/run/serve.log`；用 `-c` 指定配置文件启的临时 serve，`run/` 跟随该配置所在目录，不会写进正式运行目录。

## 12. Web 控制台的请求与推送（`include` 聚合 + `/v1/ws`）

写脚本/agent 调 HTTP 时用得上的两件事：

- **`GET /v1/jobs/{id}?include=a,b,c`**：一次取回详情页要的附属数据，缺省 `include` 为空，等于普通 job 快照（CLI/MCP 不受影响）。可选项：`events`（最近 200 条升序，带 `events_last_seq` / `events_truncated`，更早的用 `&before=<seq>`）、`comments`（最新 100 条 + `comments_total`）、`deliveries`、`retries`、`wakeups`、`pty_sessions`（无 attach 权限时省略并在 `include_errors.pty_sessions` 写 `forbidden`）、`artifacts`（**仅终态 job** 内联，最多 200 + `artifacts_total`）、`session_jobs`（同 session 的 job，最多 50）。每项独立容错：失败项写进 `include_errors`，整体仍 200；未知 include 名是 400。日志正文、diff、`/request` 不在其中。
- **浏览器推送 `/v1/ws`**（web 用，脚本一般不需要）：`POST /v1/ws-ticket`（Bearer；**job 凭据和 worker token 返回 403**）换 30s 一次性 ticket，再 `GET /v1/ws?ticket=…`（auth 组外；Origin 须与取 ticket 时一致，并遵守 `governance.attach_origins`；每个 caller 最多 16 条连接，超出 429）。JSON 文本帧：客户端 `sub|unsub`（`topics`，`since_seq` 给 `job:<id>` 断线补发）/ `ping`；服务端 `hello`（`server_time`、`version`）、`snap`（快照）、`evt`（增量）、`inval`（失效，客户端自己 REST 重拉）、`resync`（队列满丢过增量，重订并重拉）、`pong`、`error`。服务端每 20s WS ping，60s 无响应断开。主题：`stats`（快照，合并节流 ≥2s，仅有订阅者时计算）、`pending`（快照 = 待应答 interaction + OPEN decision，铃铛数据）、`jobs`（inval，带变化的 job id/status）、`job:<id>`（status / event / interaction 增量）、`sessions`（inval，心跳不推）、`runners` / `meta` / `plans` / `workflows` / `schedules` / `work`（inval）。
- **web 的兜底行为**：WS 断开超过 15s，各页面切回 30s 一次的低频轮询；恢复后自动停轮询并拉一次快照；页面隐藏超过 5 分钟主动断开，回到前台重连。顶栏状态点反映真实连接（已连接 / 重连中 / 已断开·兜底轮询）；`hello.version` 变化触发「有新版本」提示。反向代理需放行 `Upgrade` 头、关缓冲、空闲超时 > 60s；Vite 开发代理已加 `/v1/ws` 的 `ws: true`（开发源要加进 `governance.attach_origins`）。
- `GET /v1/agents`、`GET /v1/meta` 在前端有模块级缓存，由 `meta` 主题的 inval 失效。
## 13. 工作项（`gofer work`）：一件事 ≠ 一个会话

同时开着多个终端会话时，gofer 看得到"会话在不在跑"，看不到"做到哪、卡在哪、下一步是什么"。**工作项**（work item）就是这层记录：一张卡 = 一件事，**跨会话**（会话结束后被唤醒、换 agent 接手、换机器继续，都挂在同一个工作项上），一个工作区下也可以同时有多件事。web「工作」页（`/work`，手机优先）是它的总览。

- **自动草稿**：新会话**第一次有人工提问**时自动建一张草稿工作项（`source=auto`，标题取提问首行，进「未整理」区，不打扰人）；补上目标（`work report --goal` / 手填）或点「已整理」后进入看板。同一会话不会重复建；合并 / 拆分后也不会被自动草稿冲掉。
- **状态 8 个**：`active` 进行中、`needs_me` 等我、`waiting_resource` 等资源、`needs_onsite` 需现场、`review` 待验收、`parked` 已搁置、`done` 已完成、`dropped` 已放弃。会话**自动映射**：running → active；waiting_reply / needs_attention / 关联 job 有待应答交互 / **工作项关联的 plan（`work link --plan`）或关联 job 所属 plan 有待应答的 decision（`gofer_ask_human`）** → needs_me；关联 job（会话 `session watch` 的 job + `work link --job`）`needs_review` → review。会话 offline / ended **只在卡片标「会话已离线」**，不改状态。同步时机：hook 心跳（Stop / 提问）、会话 / job / 决策 / 交互的写入约 0.3s 去抖后同步，另有每 30s 的兜底扫描。
- **人手动设置的状态优先**（`status_source=human`，`work set --status` / web 标状态）：之后会话再怎么跑也不覆盖，直到人 `work set --auto` 交还，或会话自汇报 `--status active`（"阻塞已解除"）解除。会话自汇报的其它状态记 `status_source=report`；`done` / `dropped` 永远不从汇报采纳（完成由人确认）。
- **搁置 / 提醒**：`work park <id> --until 2d --note "到货后继续"`、`work remind <id> tomorrow`（时间写法：`2h` `3d` `1w` `tomorrow` `2026-10-08` `"2026-10-08 09:30"` RFC3339）。server 每 30s 扫描，到点发**一次**钉钉事件 `work.remind`（`reminded_at` 去重）；卡片进「到期提醒」，人清除提醒 / 改状态后离开。
- **每日摘要** `work.digest`：默认每天 `09:00`（`work.digest_time`，`work.digest_enabled: false` 关闭；错过时间窗 6 小时不补发）。正文由数据库**确定性生成**：等我 / 等资源 / 需现场 / 待验收计数、搁置超 7 天、昨日有进展的列表，带 `/work?id=` 链接。`gofer work digest` 预览，`--send` 立即发一次。`work.remind` 与 `work.digest` 在默认订阅集里（见 docs/runbook/im-notification.md）。
- **「等我」通知** `work.needs_me`：工作项**进入** needs_me（自动映射或手动 / 汇报 / 管家标记都算）时发一次；`work.needs_me_notify`（**默认 false**，web 设置 →「工作项」页「等我通知」）总开关，`work.needs_me_throttle_min`（默认 30）是**同一工作项**的最小间隔；开了之后没写 `events` 的 webhook 也会收到，写了 `events` 的要自己加 `work.needs_me`。
- **完成回写（只建议，不改状态）**：工作项关联的 todo 变 done、关联 issue（server tracker 镜像里）变 closed，且**所有**关联 todo / issue 都完成时，不动工作项状态（人优先），而是留一条 `status_hint` 整理建议（只有 todo → 「待验收」，含已关闭 issue → 「完成」）并写一行日志（`system`）；卡片 / 抽屉建议区「采纳」即按人写的状态生效（采纳 `done` 也行），「忽略」后不再重复提同一个值。
- **日志只追加**（`work_journal`）：汇报、备注、状态 / 字段变更、关联、合并来源（`origin_item`）、整理记录（`steward` 类）。所有字段变更自动写一条；改字段带 `rev` 乐观锁（过期 409）。
- **发言者标注**：日志的 `by` 与字段来源统一为 `human:<caller>` / `session:<sid>(<agent>)` / `steward(<agent>)` / `summarizer(<agent>)`（job 凭据 `job:<id>`，gofer 自己的记账 `system`）；`goal` / `blocker` / `next` / `summary` 各自记 `by` + `at`（详情里的 `field_sources`），卡片和详情显示"谁写的、何时"，时间线按发言者着色。
- **合并 / 拆分**：`work merge <id> <src...>` 把多个工作项并入 `<id>`（会话、关联项并过来，日志搬过来并标来源，原项 `dropped` + `merged_into`，列表默认隐藏）；`work split <id> "新标题" --session <sid> [--keep]` 拆出新项（`--keep` = 一个会话做了两件事，同时留在两边）。

```bash
gofer work ls [--status needs_me,review] [--project p] [--workspace dir] [--unsorted] [--due] [--all] [--json]
gofer work show <id>                         # 字段 + 会话（当前/历史）+ 关联 + 日志；id 可用唯一前缀
gofer work new "标题" [--goal ..] [--project ..] [--session <sid>]...
gofer work set <id> [--title|--goal|--status|--blocker|--blocker-kind|--next|--summary|--project|--workspace|--priority N] [--auto] [--sorted] [--rev N]   # 值给 `-` = 清空
gofer work note <id> "备注"
gofer work park <id> [--until 2d] [--note "条件"]   |   gofer work remind <id> <时间> | --clear
gofer work link <id> --issue X | --plan X | --todo X | --job X | --session <sid> | --acp <job-id> [--rm]
gofer work to-todo <id> [--plan <plan-id> | --new-plan "标题"]   # 工作项转 plan todo
gofer work merge <id> <src...>   |   gofer work split <id> "标题" [--session <sid>]... [--keep]
gofer work digest [--send]
```

**会话被要求汇报 / 写交接时**（web「请它汇报」「请它写交接」，或搁置 / 标「需现场」自动发）会收到一段固定请求，里面有工作项 id 和**请求 id**，用：

```bash
gofer work report <id> --request <请求id> --goal "这件事为了什么" --status needs_onsite --blocker "缺现场设备" --next "到货后跑回归" --summary "接口已写完，待联调"
#   --status: active|needs_me|waiting_resource|needs_onsite|review|parked（active = 阻塞已解除；不需要的字段省略）
#   --request <id> = 回填请求账本（置 answered；超时已 expired 的也算迟到的回答）；MCP `gofer_work_report` 的 `request` 参数同义
#   --session <sid>（或环境变量 GOFER_SESSION_ID）= 发言者（日志作者）；job 凭据也能 report（只能 report，其它写操作 403）
```
SessionStart prime 里有一行同样的提示。

### 请求账本与被动整理（W2a）

- **请求账本 `work_requests`**：`kind` = `report`（汇报）/ `handoff`（交接）/ `summarize`（整理）；`state` = `pending` → `sent` → `answered`，或 `failed` / `expired`。`gofer work requests [<id>] [--all]` 看在途（`--all` 含已结束）。会话**运行中**（running / idle / waiting_reply / needs_attention）：等回复时经**中继**注入，否则走**传话**；会话**不在运行**（offline / ended / handed_off）或送达失败：直接转成 `summarize` 请求并整理。deadline 默认 30 分钟（`work.request_timeout_min`），到点未回 → `expired`、日志记"会话未回应，已改为整理"、自动触发整理。账本在表里，**server 重启后扫描器接着推进超时**。卡片显示在途请求与最近一次结果。
- **自动交接**：**人**把状态设为 `parked`（`work park` / web 搁置）或 `needs_onsite` 时，对运行中的会话自动发 `handoff` 请求（`work.auto_handoff: false` 关闭）；会话自己 `report --status needs_onsite` 不触发。
- **被动整理**：会话 `idle` / `waiting_reply` 满 `work.summarize_idle_min`（15）分钟、或 `offline` / `ended`，且**自上次整理后有新活动**，server 起一个**一次性、只读（`--read-only`）、无工具**的整理 job（agent = `work.summarizer_agent`，默认 `claude`，参数默认 `--model haiku --tools "" --no-session-persistence`（便宜模型、不带工具、不留会话文件），`work.summarizer_args` 可改；项目按序取：`work.summarizer_project`（非空）→ 工作项自己的项目（须允许整理器 agent 与本机 runner）→ **`default` 项目**（`~/.gofer/workspace`，配置没声明时 server 内置，设置页 `/settings/work` 留空时显示解析后的项目与目录）；job 带内部标签 `work-summarizer`，Jobs 列表 / Board 默认隐藏，`--all` 可见）。输入 = 会话 transcript 尾部（claude `~/.claude/projects/*.jsonl`、codex rollout、omp 会话文件、自研 agent 的 generic jsonl（agent 配 `transcript_dialect: generic`，方言解析在 server 侧，旧 worker 也行），最多约 128KB / 40 轮 / 32KB 文本；本机会话直接读，worker 上的会话经协议 v17 的 `transcript_tail` 只读帧取，路径只能是该会话**登记的** `.jsonl` 且须在 home（或 `GOFER_TRANSCRIPT_ROOTS`）下、大小有上限；**旧 worker（< v17）读不到就降级为 last_message + progress + 日志**，不报错）。输出固定 JSON（goal / progress / blocker_kind / blocker / next / status_hint / confidence），解析失败重试一次再记失败。
- **写回规则**：只填**空**字段，或覆盖**上次也是整理器写的**字段；**人或会话写过的字段不覆盖**，改存为"**整理建议**"——卡片上一键**采纳**（按你自己写的算，之后整理器只能再提建议）/ **忽略**（同一条不会再提）；`status_hint` 只是建议，采纳才改状态（按人手动设置）。整理器写的字段在详情里标 `整理器 (claude)`。
- **成本控制**：每会话两次整理最少间隔 `work.summarize_min_interval_min`（30）、每日自动整理上限 `work.summarize_daily_limit`（50，`-1` 不限）、只整理有新活动的会话；手动「整理」不受间隔和每日上限限制。没有可用整理器 agent（没装 / 不是 cli-agent / 没有只读模式——claude、codex 内置，其它 agent 要配 `read_only_args`）时自动整理自动关闭，设置页与 `GET /v1/work-items/summarizer` 说明原因。
- **命令**：`gofer work summarize <id>`（别名 `tidy`，后台整理，返回请求 id）、`gofer work accept <id> <field>` / `gofer work dismiss <id> <field>`（field = goal | blocker | blocker_kind | next | summary | status_hint）、`gofer work requests [<id>] [--all]`；`work show` 同时列出每个字段的来源、待采纳建议和请求。
- **设置**：web 设置 →「工作项」页（`/settings/work`，需 `can_admin`）改整理器 agent / 参数 / 项目、自动整理开关、空闲 / 间隔 / 每日上限、请求超时、自动交接、每日摘要；对应 `work:` 配置块（见 `config/gofer.example.yaml`），写入走 `PUT /v1/config/work`（部分更新，热生效）。

- **REST**：`GET|POST /v1/work-items`（筛选 `status` `project` `workspace` `unsorted` `session` `q` `closed` `due`；返回 `{items, summary:{needs_me,due,open}}`）、`GET|PATCH /v1/work-items/{id}`（PATCH 带 `rev`，409 体里有 `current`）、`GET|POST /{id}/journal`、`GET|POST /{id}/sessions` + `DELETE /{id}/sessions/{sid}`、`GET|POST|DELETE /{id}/links`、`POST /{id}/merge {sources}`、`POST /{id}/split {title,goal,session_ids,keep_sessions}`、`POST /{id}/report`（可带 `request_id`）、`POST /{id}/report-request {session_id?, kind?: report|handoff}`（返回每个会话的 `request_id` / `kind` / `state`，不在运行的会话 `kind` 会变成 `summarize`）、`GET /{id}/requests?active=1` 与 `GET /v1/work-items/requests`（账本）、`POST /{id}/summarize`（立即整理，返回账本请求）、`POST /{id}/suggestions/{field}/accept|dismiss`、`GET /v1/work-items/summarizer`（整理器状态 + 有效设置）、`PUT /v1/config/work`、`GET|POST /v1/work-items/digest`。工作项详情 / 卡片多出 `field_sources`、`requests`、`suggestions`。worker token 一律 403；job 凭据只读 + `report`（整理 / 采纳 / 请求都是人的操作）。
- **MCP**：`gofer_work_list` / `gofer_work_get` / `gofer_work_update`（描述性字段，**不含 status**）/ `gofer_work_note` / `gofer_work_report`，以及只读 `gofer_session_list` / `gofer_session_get`；W2a 起还有 `gofer_work_requests`（只读账本）、`gofer_work_request_report`（走账本请会话汇报 / 写交接）、`gofer_work_summarize`（立即整理），后两个需要连着运行中的 server（本地后端没有会话中继 / 整理器会直接拒绝）；采纳建议不提供 MCP 工具（那是人的决定）。X2 起还有 **`gofer_session_ask`**（带话，见下）与只读 **`gofer_issue_list` / `gofer_issue_get`**（读 server 的 tracker 镜像）；`--project` 收窄的 MCP 没有 `gofer_session_ask`，`gofer_issue_list` 被固定在本项目。`--project` 收窄的 MCP 只看 / 改本项目的工作项；**leader 白名单不含**这些工具。 管家会话（`GOFER_STEWARD=1`）的 MCP 是另一份**窄白名单**，见下文「管家」。
- **带话 `gofer_session_ask`**（REST `POST /v1/session-ask {session_id, text, work_id?}`）：给一个**在线**会话捎一句话（"资源到了，可以继续"）。`session_id` 是终端中继会话 id（走 web 传话同一通道：等回复中的会话走中继回答，其余走传话人）**或 ACP / pty 持续会话的 job id**（等同 `job say`）。会话不在线（offline / ended / handed_off、job 已结束或没有活着的会话）、送达失败 → **409 明确报错，不排队、不静默丢**；找不到 404，空文本 400。送达后在会话所属工作项（或 `work_id` 指定的）日志里记「已向会话 xxxx 带话：…」，发言者 = 调用者（管家为 `steward(<agent>)`，对会话显示前缀 `[gofer 管家带话]`）。人（含 MCP）调终端会话要有该会话的回答权限；普通 member / leader job 凭据一律 403（默认拒绝）。
- **转为 plan todo**：`gofer work to-todo <id> [--plan <plan-id> | --new-plan "标题"]`（不给 `--plan` 就新建 plan，标题缺省取工作项标题；REST `POST /v1/work-items/{id}/to-todo {plan_id?, new_plan_title?}`，响应 `{todo_id, plan_id, plan_created, item}`）。todo 标题取工作项标题，描述取目标 + 下一步 + 来源工作项 id；工作项上同时记 `todo` 与 `plan` 两条关联（web 点 plan 可跳转）。**只能转一次**：已有 todo 关联再转 → 409（响应体带已有的 `todo_id` / `plan_id`）；job 凭据 / 管家不可调用。web 详情抽屉「关联项」区有「转为 todo」按钮（选已有 plan 或新建），转过后变成「已转为 todo」并给出「打开 plan」。
- **ACP / 终端 job 会话也能挂工作项**：`work_item_sessions` 里的 session id 除终端中继会话 id 外，还可以是 **ACP 持续会话 / 终端（pty）job 的 job id**（`gofer work link <id> --acp <job-id>`，`POST /v1/work-items/{id}/sessions`）；工作项详情里这类会话的 `kind` = `job`，web 的「打开」去 `/jobs/<id>`，不开终端中继抽屉；它们不参与「请它汇报」（会话行会说明原因）。Sessions 页的 ACP 持续会话卡与终端会话卡显示所属工作项（点击进「工作」页并打开它），没有关联时卡片详情里有下拉可直接关联。
- **推送**：`/v1/ws` 主题 `work`（inval）。web「工作」页订阅它（外加 `sessions`），断线兜底轮询同其它页。
- **导航**：顶栏菜单叫 **Works**（原「工作」），没有 Home 菜单项，点左上角 Logo「Gofer」回首页；连接状态圆点在 Logo 右侧（原 tooltip 含义不变）；Works 菜单项上有「等我」数量徽标（`/v1/work-items` 的 `summary.needs_me`，随 `work` 主题推送刷新，断线按兜底轮询，0 不显示）。
- **web**：「工作」页顶部「等我 / 到期提醒 / 未整理」三个计数徽标（点一下筛选），按状态分栏 ↔ 按工作区分组可切换；卡片默认只显示关键信息，点「详情 ▾」展开；详情抽屉里改字段、标状态、搁置、设提醒、完成 / 放弃、合并 / 拆分、关联、写备注、看日志；W2a 起卡片 / 详情标出每个字段"谁写的、何时"、列出整理建议（采纳 / 忽略）和在途请求状态，有「整理」「请它汇报」「请它写交接」按钮，日志时间线按发言者着色。Sessions 页的卡片也显示所属工作项并可跳转。


### 管家（Steward，W2b）

管家是一个**常驻的持续 ACP 会话 job**（标签 `steward`，Sessions 页与工作页可见），只做**调度和整理**：读工作项 / 会话 / 请求账本，记备注、设提醒、提合并建议、请会话汇报、触发整理、维护自己的笔记；**不干活、不拍板**——不能把工作项设成 `done` / `dropped`，不能提交执行类 job、改配置、合并或删除。默认**关闭**（要花模型额度）。

- **配置**（`steward:` 块，web 设置 →「工作项」页的「管家」区，管理员）：`enabled`（默认 false）、`agent`（任意已安装 acp-agent，随时可切；切换会结束当前管家会话，下次需要时按 prime 用新 agent 重建）、`project`（默认内置 `default` 项目；`ExclusiveDir` 关闭，不占目录锁）、`review_time`（每日巡检，默认摘要时间前 10 分钟即 `08:50`）、`idle_end_min`（空闲多久结束会话，默认 30）、`review_max_items`（每次巡检最多处理几项，默认 20）、`event_wake`（会话离线 / 到期 / 草稿多时是否唤醒管家，默认 false）、`event_throttle_min`（事件唤醒节流，默认 30）。`PUT /v1/config/steward`（部分更新，热生效）。
- **运行形态**：按需启动（有人问、到巡检时间、事件唤醒）、空闲自动结束、server 记录当前管家 job id（`work_kv`）；状态 = 未启动 / 运行中（一轮进行中）/ 空闲。**会话可抛弃**：关键信息全在工作项、日志、请求账本和「管家笔记」里，重启 / 换 agent 靠 **prime**（首条消息）接着做：角色说明 + 笔记 + 未结工作项简表（按 等我 > 到期 > 需现场/等资源 > 其他 截断）+ 最近 24h 日志 + 在途请求账本，总长 ≤ 24KB。
- **自动注入 gofer MCP + 专用凭据**：server 给管家 job 签发 **`steward` 种类的 job 凭据**（与 member / leader 并列），并往它的 ACP `session/new` 注入 `gofer mcp`（server 本机 gofer 二进制 + `GOFER_JOB_TOKEN` / `GOFER_SERVER_ADDR` / `GOFER_JOB_ID` / `GOFER_STEWARD=1`），不用写 `acp.mcp_servers`。**服务端强制**：凭据对读和写都是**默认拒绝**，只放行：读 `GET /v1/work-items*`（含 journal / sessions / links / requests / merge-suggestions）、`/v1/sessions`、`/v1/sessions/{id}`、`/v1/sessions/{id}/tail`、`/v1/jobs`、`/v1/jobs/{id}`（只读状态，没有日志 / request / 产物）、`/v1/steward`、`/v1/steward/notes`、`/v1/issues*`；写 `POST /v1/session-ask`、`PATCH /v1/work-items/{id}`（状态不得为 done / dropped，人手动设的状态优先，不能碰 `status_source`）、`POST /{id}/journal`（记为 steward 日志）、`POST /{id}/report-request`、`POST /{id}/summarize`、`POST /{id}/merge-suggestions`、`PUT /v1/steward/notes`、`POST /v1/steward/review-summary`。其它一律 403（提交 job、`/v1/config*`、`/v1/steward/ask|start|stop|review`、merge / split、删除、accept / reject……）。管家 MCP 工具与之对应：读 `gofer_work_list|get|requests`、`gofer_session_list|get|tail`（只读 transcript 尾部，≤256KB）、`gofer_list_jobs`、`gofer_get_job`、`gofer_issue_list|get`；写 `gofer_work_update`（描述类字段 + 非终态 status）、`gofer_work_note`、`gofer_work_remind`（`at` / `after_sec` / `clear`）、`gofer_work_merge_suggest`（只记建议，人在工作页「采纳 / 忽略」，采纳才真正合并）、`gofer_work_request_report`、`gofer_work_summarize`、`gofer_session_ask`（带话）、`gofer_steward_notes`（`get|history|set|review_summary`）。读 issue：`GET /v1/issues`（`project` `tracker_id` `repo`（rel_path）`status` `type` `tag`（可重复）`q` `limit` 默认 50 最大 200，返回 `{issues,total,truncated}`）与 `GET /v1/issues/{id}?tracker_id=`（同 id 出现在多个仓库时要带 `tracker_id`，否则 409 列出候选）——只读 server 端镜像，对应 `gofer_issue_list` / `gofer_issue_get`；带话 `POST /v1/session-ask` 对应 `gofer_session_ask`。 **注意**：server 以 `allow_empty_token`（无鉴权）运行时，请求根本不校验 bearer，job 凭据（含 steward）形同虚设，白名单只剩 MCP 工具层；要让服务端强制，必须给 server 配 token。管家 agent 自带的工具（如 claude-acp 的 shell / 文件读写）也不受 gofer 凭据约束，只受 prompt 约束。
- **发言者**：管家写的字段 / 日志标 `steward(<agent>)`。
- **管家笔记**：版本化 Markdown，复用 plan_handoff 的存储与乐观锁（`plan_handoffs` 里保留命名空间 `steward:notes`，上限 16KB，版本只增）；`GET|PUT /v1/steward/notes`（PUT 带 `version`，过期 409 + `current`；`?version=N` 看历史，`?history=1` 列版本）；超过 8KB 时下次巡检要求管家重写精简版，旧版本保留。web 的「笔记」抽屉可查看 / 编辑 / 看历史。
- **每日巡检**：每天 `review_time` 唤醒，只处理**自上次巡检后有变化**的未结工作项（含到期项，最多 `review_max_items` 个）；没有变化就不起会话。管家做：触发整理 / 请汇报、检查到期、提合并建议、更新 / 压缩笔记，最后用 `gofer_steward_notes` 的 `review_summary` 写一段点评，**附在当天的 `work.digest` 里**（未启用管家则无）。结果记在 `steward_reviews`（`gofer steward status` 看最近一次）。
- **事件**（会话 offline / ended、到期、草稿 ≥5 个）默认只**记入**（`steward_events`，下次巡检一并处理）；`event_wake: true` 才会批量唤醒，且两次唤醒至少隔 `event_throttle_min`。
- **问管家**：工作页右下角「问管家」面板（手机可用）：未启动时首条消息自动启动管家，回复沿用 ACP 事件流（`/v1/jobs/{id}/acp/stream`）；有三个快捷问题。`POST /v1/steward/ask {text}` 返回 `job_id`；管家正忙超过 90s 返回 409。
- **REST**：`GET /v1/steward`（状态 + 有效设置）、`POST /v1/steward/start|stop|restart|ask|review`、`GET|PUT /v1/steward/notes`、`POST /v1/steward/review-summary`、`PUT /v1/config/steward`、`GET /v1/work-items/merge-suggestions`、`POST /v1/work-items/{id}/merge-suggestions {source_id, reason}`、`POST /v1/work-items/merge-suggestions/{n}/accept|dismiss`、`GET /v1/sessions/{sid}/tail?bytes=`。除 `GET /v1/steward*` 外，启停 / 提问 / 设置 / 采纳都拒绝一切 job 凭据。
- **CLI**：

```bash
gofer steward status [--json]                  # 开关 / agent / 会话状态 / 笔记 / 最近巡检
gofer steward start | restart | stop           # 按需启动 / 重建（不丢信息）/ 结束会话
gofer steward ask "我手上还有什么没完成？" [--no-wait] [--timeout 180]   # 未启动会自动启动；默认等回复并打印
gofer steward notes [--version N | --history | --edit | --set-file <f|->]  # 笔记；--edit 用 $EDITOR 存新版本
gofer steward review [--force]                 # 立即巡检（没变化的项不起会话，--force 强制）
gofer steward merges | merge-accept <n> | merge-dismiss <n>   # 管家的合并建议（由人确认）
```


## 备注

- 本 skill 是**通用机制**说明；本工作空间的具体 project key / 可用 agent 以该工作空间 `CLAUDE.md` 为准。
- worker 配置 / 迁移见 §6 的文档链接；gofer 自身部署（serve / worker daemon / 换二进制）属运维范畴，按需查对应 gofer 文档或 bd 记忆。
## Repository tracker

One-time offline splits between explicitly named tracker directories use `go run ./scripts/tracker-transfer`; see [references/tracker-transfer.md](references/tracker-transfer.md) for review and import steps. The tool does not sync or remove source records.

Use `gofer repo init` to create `.gofer/tracker/`. The JSONL files are the repository source of truth and remain writable offline. `gofer repo sync` uses the configured client server and token; `--server` only overrides the endpoint. Manual `repo sync` waits up to `--timeout` (default 1m) — the first sync of a large tracker can take seconds; the automatic sync after write commands keeps a 2s best-effort timeout and only warns. Use `gofer job run --issue ID [--tracker-id ID]` to link a job run to a tracker issue. `repo init`（以及 `repo migrate --from-bd --apply`）会打印匹配到的 gofer 项目 key 与依据（最长路径前缀）；当前目录是嵌套在同项目内另一个 git 仓库里的独立仓库时额外提示可注册单独项目并改 `.gofer/tracker/config.yaml` 的 `project_key`；本地配置没有 projects 的客户端机器（如容器）会再向 server 查询项目列表（`/v1/meta` 的 `host_path` / `container_path`，两种路径视角都比对，取最长前缀）来匹配，连不上 server 或仍没匹配到才提示如何填写。The Web Issues console is `/issues`：分页（每页 50/100/200，记在 localStorage；树形按「根」分页，父子同页）、每行复选框 + 表头「全选本页」（含半选）、选中跨页保留；勾父项不会自动勾子项，按住 Shift 点复选框或点父项上的「连同子项」才连同子项一起选；有选中时出现批量栏（批量关闭可填统一的关闭原因 / 改状态 open·in_progress·blocked / 加标签），确认后逐条执行，个别失败逐条列出、成功的照常生效，变更在下次 `gofer repo sync` 拉回本地 jsonl。接口：`POST /v1/tracker/issues/batch` body `{tracker_id, ids[], set:{status?, close_reason?, add_tags?}}`，返回 `{results:[{id,ok,error?}], ok, failed}`（`close_reason` 只能配 `status: closed`；`ids` 最多 2000）。列表默认「树形」（子 issue 缩进挂在父 issue 下，可折叠，父行显示「N/M 已关闭」；筛选后父项不在结果里的子项平铺并标「父：…」可点击；「平铺」切换记在浏览器 localStorage），抽屉的「关系」区显示父 / 子 issue、blocked-by / blocks（未关闭的阻塞项高亮）和 related 等其它依赖。

Server-scoped memories use `gofer memory set|ls|show|rm --global` or
`--project PROJECT_KEY` (the flags are mutually exclusive); local tracker
memories remain the default when neither is present. `memory ls` accepts a
keyword and repeatable `--tag` filters. An `agent:<name>` tag limits prime
injection to that agent; memories without an `agent:` tag are shared. Session
start hooks call `gofer repo prime --hook-json --agent claude` or
`--agent codex`. Prime includes global and cwd-resolved project memories without
requiring a tracker, caps the complete context at 8 KiB, and silently omits
server sections when the server is unavailable. Scoped memory CLI operations
report connection errors instead of using an offline cache.

For a short session start, tag repository memories `prime` to include their
full text. Global/project memories need `prime` or a matching `agent:<name>`
tag for full text; other visible entries show an 80-character first-line
summary and a `gofer memory show <key>` pointer. A different agent tag excludes
the entry. Prime lists 10 active/claimed issues and 10 ready issues by default.
Use the optional `.gofer/tracker/config.yaml` `prime:` block to turn
`issues`, `ready`, `memory`, `scoped_memory`, or `handoff` on/off and to set
`issues_limit`, `ready_limit`, or `memory_summary_limit`; omitted limits keep
10/10/all summaries. `repo status` reports local `prime_bytes` and
`prime_truncated`; server additions still share the 8 KiB cap. Under the cap the
in-progress and ready rows are kept first and memory summaries get the rest;
summaries cut by `memory_summary_limit` are counted ("另有 N 条记忆未列出") with a
pointer to `gofer memory ls <关键字>`. The fixed prime header also carries a
one-line command hint.

### issue / memory 日常命令（对照 bd）

| bd | gofer |
|---|---|
| `bd ready` / `bd list` / `bd show <id>` | `gofer issue ready` / `gofer issue ls` / `gofer issue show <id>...`（show 含 notes、comments、parent / children、blocked-by / blocks、depends-on） |
| `bd create "t" -p 1 -d … -l a,b -a me` | `gofer issue create "t" -p 1 -d … -l a,b -a me`（标题可位置参数或 `-t`；另有 `--type --design --acceptance --owner --parent --dep`） |
| `bd update <id> --claim` | `gofer issue update <id> --claim` |
| `bd update <id> --priority/--description/--design/--acceptance/--assignee/--owner/--type/--parent` | 同名 flag（`-p -d -a` 短写）；**清空**某字段用 `--clear description,design,acceptance,assignee,owner,parent,close-reason`（空字符串不会清空） |
| `bd comment <id> "text"` | `gofer issue comment <id> text...` |
| `bd close <id> --reason` / `bd reopen <id>` | `gofer issue close <id>... --reason …`（可一次关多个 id，个别失败不影响其它，最后非零退出并列出失败项；`issue update` 也接受多个 id，同一补丁作用于每个） / `gofer issue reopen <id> [--reason …]`（清 closed_at / close_reason，reason 记为评论） |
| `bd dep add` / `bd dep rm` | `gofer issue dep add <id> <on> [--type blocks\|related\|relates-to\|discovered-from\|supersedes]` / `dep rm <id> <on> [--type]` / `dep ls <id>`（只有 blocks 影响 ready；blocks 成环会被拒；父子关系用 `--parent`） |
| `bd list -l proj01 --assignee x --priority 1` | `gofer issue ls -l proj01 --assignee x --priority 1`；`-l/--label` 是 `--tag` 的别名（create / update / ls 一致，ls 多个标签取交集）；`--sort id\|priority\|created\|updated`、`-r` 反序、`-n` 限条数 |
| `bd remember / memories / recall / forget` | `gofer memory set / ls [关键字] / show <key>... / rm`（`recall` 是 `show` 的别名，`memories` 是 `ls` 的别名；关键字大小写不敏感，匹配 key 与内容；`show` 可一次给多个 key，缺的 key 报错但仍打印找到的） |

一个工作区里用 label 区分子项目：`gofer issue create "…" -l proj01`，`gofer issue ls -l proj01`。issue 的 `--json` 输出不变；`show --json` 额外带 `relations`（单 id 为对象，多 id 为数组）。同步（`repo sync`）对 tags / deps 做三方合并，所以 `--untag`、`dep rm` 不会被另一端复活；comments 取并集。

### 从 bd 迁移：`gofer repo migrate --from-bd`

默认 dry-run，只读；`--apply` 才写，`--force` 在 bd 看起来仍在使用时强行继续，`--json` 输出结构化报告。步骤：

1. **读数据**：优先 `bd --readonly export --include-memories`（实时 Dolt 库，含 memory；库 schema 落后于 bd 二进制时自动以 `BD_IGNORE_SCHEMA_SKEW=1` 重试）；失败才退回 `.beads/issues.jsonl`（再用 `bd memories --json` 补 memory）。dry-run 报告两者条数差异（jsonl 常落后于库）。bd 只会以 `--readonly` 调用；bd 顺手生成的空 `.beads.gate.lock` 会被清掉。
2. **导入**：issue 全字段（type / priority / description / design / acceptance / assignee / owner / labels→tags / deps / comments / notes / close_reason / external_ref / spec_id）；parent-child 依赖变成 `parent`；issue id 前缀按 bd id 推断写进 tracker 配置；memory 导入（已存在的 key 保留）；记忆超过 20 条时写 `prime.memory_summary_limit: 15`。不带走的：bd 的 claim lease / heartbeat 与依赖边的 created_at / created_by。
3. **防分叉**：apply 前检查 `.beads` 下被占用的锁文件、bd / dolt 进程、最近 5 分钟的写入（dolt 目录本身和 `*.gate.lock` 的 mtime 都不算——只读 bd 命令也会刷新它们，所以 dry-run 之后 apply 不会被自己拒绝；gofer 自己起的 bd 进程（含它派生的子进程，由环境变量标记）不算「bd 在用」，且 bd 调用返回前会等其遗留子进程退出；锁是否被真实持有仍会检查）、未过期的 claim lease；任一命中即拒绝，`--force` 才继续。
4. **切换接入点**：`AGENTS.md` / `CLAUDE.md` 里的 `BEADS INTEGRATION` 与 `BEADS CODEX SETUP` 块换成 gofer 块（块外内容逐字不变，原地替换）；`.claude/settings.json` / `.codex/hooks.json` 里的 `bd prime…` / `bd codex-hook …` 换成 `gofer repo prime --hook-json --agent <名>`（SessionStart 原地替换，其它事件如 PreCompact 的 bd 项直接删除，键序和缩进保持）；`core.hooksPath` 指向 `.beads/hooks` 且其中只有 bd 自己的脚本、且当前目录就是 git 顶层时才 unset，否则保留并说明原因。`.beads/` 保留不删。
5. **人工清单**：`CLAUDE.md` / `AGENTS.md` / `workspace.md`（及其 `@` 引用）里其余提到 bd 的行、`.claude/settings.local.json` 的 `Bash(bd …)` 许可、bd 的 skill 目录，只列出不改。
6. **安全网**：改写前把受影响文件备份到 `.gofer/tracker/.local/migrate-backup/<时间戳>/`；apply 结束后重读 tracker 与计划逐条比对（issue / memory 条数与内容、`.beads/issues.jsonl` 未被改动），不一致则报错。重复执行幂等。

### 只装“记忆注入”（每台电脑一次）

只需要在会话开场注入全局/项目记忆时，推荐使用幂等命令：

```bash
gofer init hooks --prime-only --global --agent claude
gofer init hooks --prime-only --global --agent codex
```

用 `--agent all` 可同时安装两条；`gofer init hooks --remove --prime-only --global --agent all` 只移除记忆注入条目，不会移除会话中继 hooks。命令重复执行不会重复写入，目标文件是 `~/.claude/settings.json` 和/或 `~/.codex/hooks.json`。

如果 CLI 尚未在 PATH 中，手动 JSON 追加可以作为备选：在对应文件的 `hooks.SessionStart` 中加入 `gofer repo prime --hook-json --agent claude`（Codex 使用 `--agent codex`）。CLI 需要通过 `$GOFER_CONFIG_DIR/.env` 连接 server；执行 `gofer repo prime --agent claude` 可验证。给某个 agent 专用的记忆打 `agent:<名>` 标签，例如 `gofer memory set --global --tag agent:claude <key> "<内容>"`。
### 远程升级 worker（协议 ≥ v15）

`gofer worker upgrade <id> [--file <worker 二进制>] [--force] [--drain-timeout 秒] [--no-wait] [--timeout 秒]`（需 `can_admin`）。不带 `--file` 时 server 用自己的可执行文件，且要求 worker 与 server 同 os/arch，否则报错并提示改用 `--file`；带 `--file` 时 CLI 先把二进制上传到 server 暂存（server 自己算 sha256，不信任客户端），worker 再用自己的 token 下载、校验 sha256/大小、试跑 `--version`。校验通过后 server 即返回（HTTP 202）：worker 进入排空（server 不再给它派新 job，在途 job 做完为止，默认最多等 10 分钟，超时放弃升级并恢复接单；`--force` 不等待，在途 job 按 worker 重启处理），然后切换二进制、拉起新进程；新进程注册成功后旧进程退出，60 秒内没注册（或新进程崩溃）则杀掉新进程、还原旧二进制、恢复接单，结果记为 `rolled_back`。CLI 默认等待最终结果，打印「已升级到 vX（耗时）」或「已回滚：原因」，失败/回滚时退出码非 0；`--no-wait` 只等 worker 接受新二进制。协议 < v15 的 worker 保持在线，但会被拒并提示"该 worker 版本过旧，需要手动升级一次"。API `GET /v1/workers/{id}` 保留最近 10 条 `upgrade_history`，`gofer worker show <id>` 显示最近 3 条，Web Runners 卡片默认显示最近一次并可展开历史；离线 worker 的升级按钮会显示“worker 离线”。结果也在 `gofer worker show <id>` 的 `last_upgrade` 与 Web Runners 页（worker 卡片的「升级」按钮，仅 server 二进制、同平台、v15+ 可用）显示；升级期间 `draining: true`，此时向该 worker 提交新 job 会失败（"worker is being upgraded"），标签选 worker 时自动跳过它。升级交接后的 worker stdout/stderr 追加到 `run/worker-<id>.out.log`，结构化运行事件仍在 `run/worker-<id>.log`。
