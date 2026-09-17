---
name: gofer-usage
description: "Use `gofer` from inside a dev container: submit tasks to the host gofer server with `gofer job` — run a command in the HOST environment, do multi-service / integration / external-callback testing the container can't do alone, or invoke a host AI agent (codex/claude) — and understand worker config (LEGACY local projects vs POLICY server-pushed roots) enough to tell WHY a project/agent isn't runnable. Use when inside a dev container and something must run on the host (outside the container) or on a specific worker, when a workspace's CLAUDE.md points to gofer / an old codex-bridge for host tasks, or when a gofer worker/project/agent is rejected and you need to diagnose it. Covers submit (--runner server; local remains a compatibility alias), reading logs, sync vs async, agent/runner selection, project discovery, worker LEGACY/POLICY modes + roots mapping, and troubleshooting."
---

# gofer 使用：job 提交 + worker 配置

gofer = 一套「主机 server + 多台 worker」的任务执行网。你在 docker 容器里够不着主机环境 / 主机网络 / 主机上其他服务时，用 `gofer job` 把任务提交到 **主机 gofer server**，由它在 **主机**（`--runner server`；旧值 `local` 仍兼容）或某台 **worker**（`--runner <worker-id>`）执行。这取代了「写 tmp 通知外部 codex / curl host-bridge」这类旧机制。

- **server = 策略权威**：哪个 project 派给哪台 worker、允许哪些 agent、能否 exec/pty。
- **worker = 能力提供方**：这台机器有哪些目录（roots）/ 装了哪些 agent。

> 本 skill 详讲最常用的 `gofer job`。**其余命令**（`workflow`/`plan`/`schedule`/`session`/`tunnel`/`project`/`config`/`init`）见 [`references/commands.md`](references/commands.md)；**配置 gofer** 按节点角色看：纯客户端节点（容器里最常见）→ [`references/client-config.md`](references/client-config.md)；server → [`references/server-config.md`](references/server-config.md)；worker（含 LEGACY/POLICY 与 roots）→ [`references/worker-config.md`](references/worker-config.md)；加 project / 建 worker / 迁 POLICY 的分步 → [`references/setup-recipes.md`](references/setup-recipes.md)。需要时再读。
>
> 💡 执行**多步骤长任务**时，建议用 `gofer plan` + todo 做进度看板（每步 `--status doing/done --note`，web/手机实时可看）——范式见 [`references/commands.md`](references/commands.md) 的「长任务进度跟进」。**遇到需要人拍板的决策点**，用 MCP 工具 `gofer_ask_human` 阻塞提问、人在 web 作答后答案流回（超时按预案继续，不无限阻塞）——见同文「决策点问人」。

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
| `gofer job rerun <id>` | 用原请求重提（新幂等 key，**新会话**，agent 重读全部上下文） |
| `gofer job resume <id> --prompt "…"` | **续跑同一个 agent 会话**（codex `exec resume` / claude `--resume`）：job 中途失败/超时后让它带着自己的上下文继续，见 §5b |
| `gofer job worktree ls [-p] / rm <id> [--force] [--delete-branch]` | 列出/清理 `--worktree` job 留下的 git worktree（见 §5c） |

`--timeout <秒>` 有上限：`server.max_job_timeout_sec`（默认 3600）或项目 `max_timeout_sec`；超出会被 **clamp** 并在提交后打一行 `warning: --timeout … exceeds the project ceiling`，`job show` 显示生效的 `timeout:`。超 1 小时的任务：让管理员提高上限，或把任务拆成多个 job。

job 状态里的 **`recovering`** 不是失败：执行它的 worker 断线了，server 在 `job_recover_window_sec`（默认 120s）内等同一个 worker 进程重连；重连上 → 回到 `running`，日志不丢不重；窗口到期才 `failed`（error `worker lost …`）。看到 recovering 先别重派。

## 4. agent 与 runner

**agent**（`-a`，取决于该 project 在 server 配的 allowed_agents，常见 `exec`/`codex`/`claude`）：

- `exec`：直接跑命令，命令放 `--` 之后：`-a exec -- <cmd> <args...>`。
- `codex` / `claude` / `omp`：跑 AI agent，提示词用 `--prompt "..."` 或任务文件 `-f task.md`（YAML frontmatter + 正文）。
- **一个 agent 两种启动方式**：agent 定义的 `args` 是批处理 argv（`job run`），`interactive_args` 是 pty argv（`job run --interactive`，`[]` = 裸 TUI）。提交时若被拒：`agent "x" has no batch mode` = 该定义只写了 `interactive: true`（旧的 tty-* 写法），只能 `--interactive` 提交；`has no interactive mode` = 没写 `interactive_args`；`project "p" does not allow interactive jobs` = 项目没开 `allow_interactive`。`gofer agent list` 的 `batch/interactive` 两列就是能力位。

**runner**（`--runner`，默认 `server`；`local` 为兼容别名）：

- `server` → **主机 server** 执行（需要主机环境/多服务联调时用它）。
- `local` → **主机 server** 执行（兼容旧写法，与 `server` 等价）。
- `<worker-id>` → 对应 **worker** 执行（容器自带的活直接在 bash 跑即可，一般无需绕 worker）。

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

## 5. 同步 vs 异步

- **同步** `--sync`：server 阻塞到终态返回（默认上限 ~30s，可 `--wait-timeout <秒>`）。短任务、要立刻拿结果用它。
- **异步**（不加 `--sync`）：立即返回 job id，再 `gofer job watch <id>` 跟随 / `gofer job show <id>` 轮询。长任务（构建、联调、FFmpeg 等）用它，配 `--timeout <秒>` 限执行时长。

### 5b. job 中断了怎么续（`job resume`）

codex/claude 因供应商容量错误、网络抖动或超时把 job 干掉一半时，**不要重派整份任务书**（新会话 = 重读上下文），用 resume 让它带着自己的会话继续：

```bash
gofer job show <源 job-id> | grep session_id     # 有 session_id 才能续
gofer job resume <源 job-id> --plan <plan-id> \
  --prompt "上一次运行因 <原因> 中断。先 git status / git log --oneline -5 判断进度，只完成任务书剩余项，不要重做已提交部分；汇报格式同前。"
```

前提：源 job 已终态（done/failed/timeout/cancelled 都行）、捕获到了 `session_id`（codex 靠输出 `session id:` 捕获，claude 靠 `--session-id` 注入；omp 需在 agent 定义加 `session_capture`/`session_resume`）、agent 有 resume 模板（内置 claude/codex）、同一 runner。resume 产生一个**新 job id**，`--plan` 照常可挂。命中瞬时错误会自动续跑一次（`auto_resume_max`）。

### 5c. 并行派活用 `--worktree`

多个 agent job 在同一 checkout 里并行改代码会互相踩（`.git/index.lock` 残留、互相覆盖）。加 `--worktree` 让每个 job 在 `<仓库顶层>/tmp/gofer/wt/<job-id>` 的独立 git worktree 里跑，提交落在分支 `gofer/<job-id>`，主 checkout 不动；结束后分支保留供人合并，`gofer job worktree ls/rm` 清理。`--cwd` 仍相对项目根，会映射进 worktree 的同一子路径；嵌套仓库按 `--cwd` 所在的最近 git 顶层建。任务书里要提醒 agent：**不要 `git checkout` 别的分支、不要 push**。

### 5d. 日志与隧道诊断

server/worker/forwarder 都写 JSONL 文件日志（轮转、脱敏）：server `<config-dir>/run/serve.log`，worker `run/worker-<id>.log`，`tunnel forward` 默认 `run/tunnels/forward-<时间>-<pid>.log`（`--log-file`/`--log-dir` 可改，`--quiet` 只静默终端）。一次隧道在三端共用同一 `tunnel_id`（server 经 `X-Gofer-Tunnel-Id` 回传），`rg '"tunnel_id":"…"'` 三个文件即可重建全链路；`first_byte_ms`/`bytes_up|down`/`packets_up|down`/`close_reason` 能判断"慢在 relay、设备还是往返次数"（`duration_ms / packets_up` ≈ ping RTT = 协议逐包 stop-and-wait）；`GOFER_TUNNEL_TRACE=1` 逐报文记 `tunnel.datagram`。详见仓库 `docs/runbook/tcp-tunnel.md`。

**job 日志（stdout.log / stderr.log）**：ndjson agent（`omp --mode json`、`claude --output-format stream-json`）在 agent 定义里写 `output_format: ndjson` 后**stdout=最终答复、stderr=过程事件**（逐 token 增量在**采集时**就被丢掉；事件一行一个、单行 ≤2KB，超长标 `…(truncated)`；`session` 行恒留，`ndjson_keep` 调白名单，`ndjson_raw: true` 才另存未过滤的 `<result_dir>/stdout.raw.log`）；`gofer job show <id>` 的 `ndjson_kept`/`ndjson_dropped`/`ndjson_truncated` 就是保留/丢弃/截断行数，web 详情页对**事件流（stderr）**有「结构化视图」切换。**看不到输出**先分清是"agent 没输出"还是"被过滤了"：`ndjson_raw` 打开重跑一次即可对照。

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
gofer init hooks                  # 一次性: 把 hook 写进 ./.claude/settings.json (Codex: --agent codex → ./.codex/hooks.json; --agent all 两个都写)
gofer session relay auto          # 回到缺省三态(server 按判据决定; 也可以 on / off, 见下)
gofer session ls                  # 看哪些会话在等回复(waiting_reply 置顶), RELAY 列显示 on / off / auto
gofer session say <id> "<回复>"   # web 输入框的 CLI 等价; 回复 /off = 关掉中继并让会话正常停下
gofer session show <id>           # relay 行显示当前 mode + 判定依据(空闲多久 / 距上次人工输入多久)
gofer init hooks --remove         # 卸载
```

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
- **人回来即放行**：判据一开的等待，hook 每 ≤5s 重探空闲值，人一碰键鼠就放行；判据二开的等待没有可探的东西，靠**你的动作本身**——按 Esc 结束等待，或直接在终端输入一条（UserPromptSubmit / Interrupt 事件一到达，server 就把 turn 关成 `released_by=user_returned`）。**显式 `on` 的等待不受此影响**：只有终端输入 / `/off` / `relay off` 才关。
- 依赖：Windows/macOS 探得到键盘；**Linux 需要 `xprintidle`**（X11），缺失或 Wayland/无桌面时为"未知"——此时**自动走判据二**，不再退回到"只能手动拨开关"。
- web 会话列表里找到该会话也可以直接点 auto / on / off 切换，**下一次回合结束**生效（会话正在跑长任务时最常见，能接上）；已经停在空闲提示符、且人一直没离开过的会话没有 hook 在跑，仍需在终端输入一次。
- 注入的回复带前缀 `[gofer web 回复]`，与终端输入等价处理。
- 详见 [`references/commands.md`](references/commands.md) 的「session — 终端会话中继」。
- **想让手机响一下**：配个钉钉/飞书群机器人，事件订阅 `session.waiting`（不在默认集里，必须显式写），消息带直达会话的链接。配置见 gofer 仓库 `docs/runbook/im-notification.md`。

## 备注

- 本 skill 是**通用机制**说明；本工作空间的具体 project key / 可用 agent 以该工作空间 `CLAUDE.md` 为准。
- worker 配置 / 迁移见 §6 的文档链接；gofer 自身部署（serve / worker daemon / 换二进制）属运维范畴，按需查对应 gofer 文档或 bd 记忆。
