# gofer 命令参考（`job` 之外）

> 主 `SKILL.md` 详讲最常用的 `gofer job`。本文补齐其余命令，**按需查阅**——AI 要用 workflow / plan / schedule / tunnel / session 等时读这里。
> 每个命令的**完整 flag** 用 `gofer <cmd> --help`（本文只给"是什么 + 常用法 + 何时用"）。
> 命令通过主机 server 执行；连接、project key、agent/runner 的通用规则见 `SKILL.md`。

## workflow（别名 `wf`）— job 链（有依赖的多步编排）

把多个 step 串成一条链，step 间可有依赖 / fan-out / join。**从文件提交**（title + `steps[]`）：

```bash
gofer workflow run <file.yaml> [-w]     # 从 yaml/json 提交; -w=轮询到终态并打印每步
gofer workflow list                     # 列 workflow(可带状态过滤)
gofer workflow show <id>                # 状态 + step 链
gofer workflow events <id>              # 生命周期事件时间线
gofer workflow cancel <id>              # 取消运行中的
gofer workflow export <id>              # 导出 spec(去密钥)可再 import, 默认 yaml(= run 格式)
```

- 文件格式：`.json` 按 json，其余按 yaml；顶层 `title` + `steps: [...]`。先 `export` 一个跑通的当模板最快。
- **何时用**：多步、有依赖/并发的编排（先 build → 再 test → 再 deploy）。**单发命令**用 `job`，别套 workflow。

## plan — job 分组计划（组织 + 跟踪，不是执行编排）

把相关 job 归到一个 plan 下、附带 todo 清单跟踪进度（**不**决定执行顺序，只做归类/看板）：

```bash
gofer plan create --title "<标题>" [--desc "<说明>"] [--plan-id <id>]
gofer plan attach <plan-id> <job-id>    # 把已有 job 挂到 plan（注意顺序：先 plan 后 job）
gofer plan list / show <id> / archive <id>
    # show 在每个 todo 下列出挂接的 job(id/状态/agent/用时, 新→旧最多 10 条)
gofer plan add-todo <id> "<待办>" [--note "<备注>"]       # 加 todo(别名 todo-add)
gofer plan set-todo <todo-id> [--status doing|done|skipped|pending] [--note "<结果>"] [--append-note "<追加一行>"]
    # 生命周期推进：--status doing 自动记开始时间, done/skipped 记完结时间;
    # 裸调用=done, --undone=pending(旧用法兼容); --note 单独用只改备注;
    # --append-note 追加一行到现有备注(与 --note 互斥, 服务端原子追加; 只追加不改状态;
    # job 的终态就是这么自动记账的: 见下「todo 联动」)
gofer plan set-status <id> <status>
```

- **workflow vs plan**：workflow = **执行**依赖链（server 按链跑）；plan = **组织** view（把散 job + todo 归一起看）。

### 范式：长任务进度跟进（人不在电脑前也能看）

在任何 AI 会话里执行**多步骤长任务**（大改造 / 迁移 / 分阶段实施）时，把 plan 当进度看板用——每步经 CLI 或 gofer MCP 工具（`gofer_create_plan` / `gofer_add_todo` / `gofer_update_todo`）汇报，web 控制台 Plan 详情页（手机可开）就是实时进度页：

```bash
# 开工: 建计划, 每个步骤一个 todo
gofer plan create --title "xxx 改造实施"
gofer plan add-todo <plan-id> "步骤1: 数据模型迁移"
gofer plan add-todo <plan-id> "步骤2: API 扩展"
# 每步开始 / 完成时:
gofer plan set-todo <todo-id> --status doing
gofer plan set-todo <todo-id> --status done --note "迁移完成, 单测绿"
# 某步决定不做:
gofer plan set-todo <todo-id> --status skipped --note "原因..."
```

要点：note 写**结果/验收一句话**（不是过程流水，过程在 job logs）；跑长命令的步骤尽量用 `gofer job run` 执行并 `--plan <id>` 或 `plan attach` 挂进来，进度页可直接点进日志。**更好的是直接 `job run --todo <todo-id>`**：状态与"交付了哪些提交"由 job 终态自动写回，不用手工 `plan set-todo`（见「todo 联动」）。

### 范式：决策点问人（gofer_ask_human）

大计划跑到**需要人拍板的分叉**时，agent 经 MCP 工具 `gofer_ask_human` **阻塞提问**；人在 web 铃铛 / Plan 详情页作答，答案从工具返回值流回，会话原地继续（设计 §C3）：

```
# agent 会话里(MCP 工具, 阻塞等待):
gofer_ask_human{plan_id, title, question, options: ["方案A","方案B"], timeout_sec: 1800}
# → 返回 {state:"answered", answer, answered_by} 或 {state:"expired"}

# 人这一侧(CLI 等价面, 不阻塞; web 更常用):
gofer plan ask --plan <id> --title "..." --question "..." [--option A --option B] [--timeout 30m]
gofer plan decisions --state OPEN
gofer plan answer <decision-id> --answer "方案A"
```

要点：

- **超时兜底在 agent 侧**：收到 `{state:"expired"}` 后按预案继续（执行推荐项），或把该步 todo 置 `skipped` + note 说明后跳过——**不无限阻塞、不原地重问**。
- `timeout_sec` 缺省 1800s（clamp `[2s, 24h]`）。按**宿主客户端的 tool 调用超时上限**设定：宿主若先杀调用，decision 留 OPEN、到期自动 EXPIRED，通道本身无错。
- **决策点串行提问是范式建议**（宿主客户端可能串行执行 tool call），**不是** MCP 连接限制——go-sdk 服务端并发执行 tool call，ask 阻塞不排队其他调用。

## job 的"续"与"验收"：`resume` / `worktree` / `accept|reject`

```bash
gofer job resume <源id> --prompt "…" [--runner <同源>]   # 续跑源 job 的 agent 会话(新 job id); 源 job 须终态且有 session_id
gofer job accept <id> [--note "…"]                       # 人工验收通过: needs_review → done
gofer job reject <id> --note "…" [--resume]              # 人工验收拒绝: needs_review → rejected(终态); --resume 以 note 为 prompt 续投
gofer job run … --review                                 # 让这个 job 正常完成后停在 needs_review 等人验收
gofer job list --status needs_review                     # 谁在等人验收
gofer job worktree ls [-p <project>]                     # 列 --worktree job 留下的 worktree: 分支/领先提交/是否脏/是否已合并
gofer job worktree rm <job-id> [--force] [--delete-branch]   # 移除 worktree(脏且无 --force 拒绝); 分支默认保留
gofer job run … --worktree [--worktree-base <ref>]       # 在 <顶层>/tmp/gofer/wt/<job-id> 的 worktree 里跑, 分支 gofer/<job-id>
gofer job run … --todo <todo-id>                         # 为某个 plan todo 跑这个 job(见下「todo 联动」)
gofer job run … --verify 'go test ./...' [--verify-timeout 900]   # agent 正常结束后在同一个 cwd/env 跑这条验收命令; 非 0 退出 → job failed
gofer job run … --no-verify                              # 关掉项目默认的 verify(见下「验证步骤」)
gofer job show <id>                                      # 打印 todo / base_sha / commits(这次交付了哪些提交) / verify(验收结果)
```

- `resume` vs `rerun`：`rerun` 是同一请求重提（新会话）；`resume` 是让 codex/claude 用 `exec resume <sid>` / `--resume <sid>` 接着上次会话跑，prompt 只说"从哪继续"。**acp-agent 的 resume 走协议 `session/load`，不需要 `session_resume` 模板**（也不需要注入/捕获模板）；agent 没声明 `loadSession`（或配了 `acp.load_session: false`）时 resume 直接报不支持，不会偷偷开新会话。
- **只读 job**：`job run --read-only`（审查/分析类任务，agent 不能写文件）——cli-agent 追加 `read_only_args`（内置 codex `-s read-only`、claude `--permission-mode plan`），acp-agent 用 `acp.modes.read_only` 映射到 agent 的 mode id（prompt 前 `session/set_mode`）；exec agent 与没配只读模式的 agent 提交即被拒。resume 继承只读（同一 job 链内不能升级为可写）。
- **人工验收（`needs_review`）**：`job run --review`（或项目 `require_review: true`，或 workflow 步骤 `review:`）的 job，agent **正常完成**后停在非终态 `needs_review`，等人 `job accept`（→done）或 `job reject --note …`（→终态 `rejected`，workflow 按失败聚合、不会被自动重试/续投）。**只有人能 accept**：worker token 打 HTTP `POST /v1/jobs/{id}/accept|reject` 一律 403；MCP 只有 `gofer_reject_job`，没有 accept 工具。`job cancel` 对 `needs_review` 返回 409（改用 reject），`job resume` 也要求先把验收做完。
- 断线恢复：worker 断线时 job 进 `recovering`（`job list --status recovering`），窗口内同进程重连即恢复；serve 重启也一样。**recovering 不要重派。**
- **todo 联动（`--todo`，SUP-01 C）**：`job run --todo <todo-id>` 把"跑一次活"和 checklist 上的那一项绑起来——`plan_id` 缺省从 todo 反查（显式 `--plan` 与 todo 所属 plan 不一致直接 400），提交成功后该项转 `doing` 并指向这个 job；终态时自动写回：`done` → 该项 `done` 且备注追加一行 `<job-id> ✓ N commits: <sha> <subject>; …`（最多 8 条，超出 `+N`；无提交写 `no commits`）、`needs_review` → 追加 `<job-id> 待验收`（accept 后再补 done 行）、失败/超时/取消/拒绝 → 状态不动、追加 `<job-id> ✗ <status>: <原因前 120 字>`。**失败只影响备注，不影响 job 本身**；`reject --resume` / 自动续投的新 job 继承 `todo_id`，整条链的每一轮都追加到同一项上。
- **提交采集**：job 开跑时在执行机记 `base_sha`（cwd 不是 git 仓则空；worktree job 用其基线），终态时 `git log base..HEAD`（上限 50，新→旧）写进 `commits`——`job show` 列出、web 详情页「提交」块可一键复制 sha、worker 上跑的 job 经 Outcome 回传后 host 行同样有。它独立于 `capture_diff` 开关，采集失败留空、不影响 job。
- **验证步骤（`--verify`，SUP-01 P2）**：agent 汇报不当验收——`job run --verify '<argv>'`（shell-words 拆成 argv，**不经 shell**；要 shell 就写 `bash -lc '…'`）在 agent **正常结束（exit 0）**后于**同一台执行机、同一 cwd/env**跑这条命令，独立超时 `--verify-timeout`（缺省项目 `verify_timeout_sec`，再缺省 600s）。结果：`passed` → job 按原逻辑；`failed`/`timeout` → job `failed`（exit_code 取验证退出码，timeout 为 -1）且**不是** transient（不触发自动续投/故障转移）；开了 `--review` 则停 `needs_review` 留人裁决；agent 自己失败/取消/超时 → `skipped`（不跑）。stdout+stderr 合并写进本 job 的 stderr 日志并夹两条横幅，web 详情页「验证」块可点击跳到输出，`job show` 打印 `verify: failed (exit 1, 12.3s)`。**需要项目 `allow_exec`**（argv 来自提交者，与 exec 同一信任面）；不想跑项目默认值就 `--no-verify`。**worker/peer 上跑的 job 由执行机跑验证**，结果经 Outcome 回传（不会在 server 上重跑）；协议 < v8 的 worker 会被**直接拒绝**（提示升级，不再静默忽略）。
- **worker 侧事件镜像（SUP-01 G）**：worker 上跑的 job 的审批（`job.permission_requested|answered|timed_out`）与验证（`job.verify_started|finished`）事件现在会**镜像到 hub 的 job 事件表**（detail 带 `origin: worker:<id>`），所以 `job watch`/webhook 订阅这些事件对远端 job 同样生效。重复帧在 hub 侧按 `(job_id, type, ts, interaction_id)` 去重。

## tunnel（别名 `tun`）— 经 worker 的 TCP/UDP 端口转发

```bash
gofer tunnel forward -w <worker> [udp/][bind:]lport:host:port …   # 本机端口 → worker 所在网络的目标; 可多条
gofer tunnel forward --name <preset>                              # 用 tunnel save 存的预设
gofer tunnel save <name> -w <worker> <spec…> [--note …] / saved / forget <name>
gofer tunnel check -w <worker> [udp/]host:port                    # 只验 worker 能否建到目标的 socket(UDP 不代表设备会应答)
gofer tunnel ls                                                   # 活动隧道(id/caller/worker/target/bytes)
```

- forward 的日志：默认 `<config-dir>/run/tunnels/forward-<时间>-<pid>.log`（`--log-file`/`--log-dir` 改；`--quiet` 只静默终端）；事件带 `tunnel_id`（与 server/worker 日志同一个）、`session_id`（UDP=本地来源地址）、`dial_ms`、`first_byte_ms`、`bytes_up/down`、`packets_up/down`（UDP）、`close_reason`。`GOFER_TUNNEL_TRACE=1` 逐报文记 `tunnel.datagram`（`dir/len/gap_ms`）。
- 目标必须在 worker 的 `tunnels.allow` 白名单内；判读"慢在哪"见仓库 `docs/runbook/tcp-tunnel.md`。

## session（别名 `sess`）— 终端会话中继（web ↔ 终端）

终端里的 Claude Code / Codex 会话经 hooks 登记到 server；会话的 **relay 开关**打开时，Stop hook 把 agent 最后一条消息发成一个 turn 并阻塞等待，人在 web「会话」页 / 铃铛 / CLI 作答，答案经 `decision: block` 注入同一会话继续（设计 SESS-01）。

```bash
gofer init hooks [--agent claude|codex|all] [--global] [--remove] [--force]
#   合并写 ./.claude/settings.json 与/或 ./.codex/hooks.json(--global 写 ~/); 幂等、只增删 `gofer hook` 自己的条目
#   Codex 另需 config.toml [features] hooks = true(旧名 codex_hooks), 且项目 .codex/ 需 trust
gofer session ls [-p <project>] [--state waiting_reply] [--all]   # 列会话(waiting_reply/needs_attention 置顶)
gofer session show <id>                 # 详情 + 最近 turn(id 可用前 8 位)
gofer session relay auto|on|off [--session <id>]  # 省略 --session: 按当前目录反查(歧义时列出候选); auto = 缺省
gofer session say <id> "<回复>"         # 答最新 OPEN turn; "/off" = 关中继放行
gofer session say <id> "<回复>" --deliver   # 选路: 有 OPEN turn 就当作答, 否则敲进该会话的 tmux pane(§9.1 A)
gofer session say <id> "<回复>" --deliver --takeover   # 没有 tmux 时起新进程 `--resume` 接管该会话, 这条消息作首条输入(§9.1 B)
gofer session rm <id>                   # 移除登记(turn 保留)
gofer hook claude|codex [--wait N]      # hook 执行体(由 hooks 配置调用, 人不直接用); 日志 <config-dir>/run/hook.log
```

要点：

- 开关在 server、按会话、**三态**（`agent_sessions.relay_mode`）：`on` 每次停下都等；`off` 从不等（已打开的 turn 释放）；`auto`（缺省）由 server 判定。hook 每次 Stop 先问 server（heartbeat 返回 `wait_reason`），不等则零阻塞，server 不可达也直接放行，**永不卡死终端**。
- **自动布防（`auto`，两条判据；`0` 分别关闭）**：
  1. **键盘空闲**（`session.auto_relay_idle_sec`，默认 300s）：hook 每次 Stop / Notification(idle_prompt) 上报"键鼠空闲秒数"（Windows `GetLastInputInfo`、macOS `ioreg HIDIdleTime`、Linux `xprintidle`；取不到或超 ~200ms = 未知）。空闲 ≥ 阈值 ⇒ 停下就发 turn 并在 web 等回复（`wait_reason=idle_probe`，列表显示 `auto (idle 12m)`）。
  2. **距上次人工输入**（`session.auto_relay_turn_sec`，默认 900s，R2）：**探测不到键盘时**（容器/无 X11，`idle_sec` 恒为 -1）改用本会话的 `last_human_at`（SessionStart、以及非注入的 UserPromptSubmit 会刷新），距今 ≥ 阈值 ⇒ 同样布防（`wait_reason=turn_age`，列表显示 `auto (no input 22m)`）。这条是为"键盘在主机、hook 在容器里"的场景准备的。
  - 探测成功时以判据一为准，不会再用判据二猜；`last_human_at = 0`（没见过人工输入）不构成布防理由。
- **人回来即放行**：判据一的等待，hook 每轮（≤5s）重探空闲值并报告给 server，空闲 < 阈值 ⇒ turn 关成 `EXPIRED` + `released_by=user_returned`。判据二的等待没有可探的读数，靠人的动作本身：按 Esc（hook 被杀，等价放行）或在终端输入一条 —— `UserPromptSubmit` / `Interrupt` 事件到达时 server 把该会话的 OPEN turn 关成 `EXPIRED` + `released_by=user_returned`，`hook.log` 里该轮随即 `turn expired, released`。**显式 `on` 的等待不受此影响**——它只由终端输入（UserPromptSubmit 把 mode 降回 `auto`）、web `/off`、或 `session relay off` 结束。
- `UserPromptSubmit`（人在终端输入）在 `on` 下把 mode 降回 `auto`（"人回到键盘就交还给自动判据"）；web 注入的回复虽也触发该事件，但带 `[gofer web 回复]` 前缀，hook 上报 injected，不会动开关，也不会被当成"人回来了"。
- Stop hook 等待期间终端显示 hook 运行中；人回到电脑想直接输入可按 Esc 取消。
- 硬边界：会话已停在空闲提示符、且人从未离开过的场景没有 hook 进程活着，web 拨开开关要等下一次 Stop；需终端输入一次（人离开过则由自动布防覆盖）。
- **无 OPEN turn 时送话（阶段 2-A，tmux 注入）**：`POST /v1/sessions/{sid}/deliver {text}`（CLI 是 `session say --deliver`，web 是抽屉里变成「送入终端」的输入框）先看有没有 OPEN turn —— 有就等价 `say` 作答（`path=turn`）；否则派一个内部 exec job 到该会话的 runner：`tmux display -p -t <pane> '#{pane_current_command}'` 确认 pane 存活且前台是 agent CLI（白名单默认 `claude|codex|omp|node|gemini|opencode`，`session.inject_commands` 可配），再逐行 `tmux send-keys -t <pane> -l -- '<行>'` + `Enter`；文本前缀 `[gofer web 回复] `、上限 8KB、pane 与文本都按 shell 单引号转义。成功：`{path:"tmux", job_id, decision_id}`，会话置 running，审计行 `plan_decisions(kind=relay, detail={"path":"tmux","job_id":…})`。
  - 失败码（HTTP 状态）：`no_runner` / `no_tmux` / `ended` → 409（原因码写在错误信封的 `error` 字段，如 `deliver failed: no_tmux`）；`inject_failed:pane_missing|pane_busy:<cmd>|runner_error` → 502；空文本 / 超 8KB → 400。**worker token 不能送话**（403）。
  - 前置条件：会话跑在 tmux 里 + 登记了执行机（容器里要在容器内起 worker 并配 `GOFER_HOOK_RUNNER=<worker-id>`，纯客户端节点不再假装登记成 `server`）；注入 job 是 exec 类型，project 需 `allow_exec: true`。
- **无 OPEN turn 且没有 tmux 时送话（阶段 2-B，`--resume` pty 接管）**：`POST /v1/sessions/{sid}/deliver {text, allow_takeover: true}`（CLI `session say --deliver --takeover`，web 是 A 报 `no_tmux` / `pane_missing` 后出现的「起新进程接管并发送」+ 二次确认）。server 在**同一 runner、同一项目相对目录**起一个交互 pty job：argv = agent 的交互 resume 模板（`claude --resume <sid>` / `codex resume <sid>` / `omp --resume <sid>`，经 `PlanTakeover` 由宿主解析），`InitialInput = "[gofer web 回复] <文本>\r"` 由 pty 在**首次输出后安静 `session.takeover_input_delay_ms`（默认 1500ms，最多等 10s）**再写入子进程 stdin 并记 `job.input_injected` 事件（worker 路径经 `wsproto.Dispatch.initial_input`，协议 v7）。成功：`{path:"takeover", job_id, decision_id}`，会话置 `handed_off`（`handed_off_job_id`/`handed_off_at`），审计 `detail={"path":"takeover","job_id":…}`，通知事件 `session.handed_off`（不在默认集）。
  - `allow_takeover` 缺省 false：不带它时 server 停在 A 的 409 `no_tmux`（接管会把会话从原终端移走，必须显式要）。
  - 前提与失败码（409）：`no_resume_template`（agent 无交互 resume 模板）、`interactive_not_allowed`（项目未开 `allow_interactive`）、`cwd_outside_project`（cwd 换算不到执行机上的项目相对路径，POLICY roots 映射的已知限制）、`handed_off:<job>`（已被接管）；派发失败 → 502 `inject_failed:runner_error`。接管 job 是 exec 载体但按**源 agent** 过访问门（与 job resume 同一豁免），不需要 `allow_exec`。
  - 接管后原终端：`wait_reason` 恒空、`OpenTurn` 拒绝（头一次 Stop 起就以 `ErrRelayOff` 放行），心跳响应带 `notice`，hook 在 `UserPromptSubmit` / `Stop` 把它打到 **stderr**（"该会话已于 <时间> 在 web 接管（job <id>）…本终端的中继已停用"）。原终端的 Stop / UserPromptSubmit / Notification / SessionEnd 都不会把会话从 `handed_off` 改回去。
  - **解除接管**：`POST /v1/sessions/{sid}/release-takeover`（web 抽屉「解除接管」；无 CLI 子命令）→ 先 cancel 接管 job，再置 `idle` 并清空接管标记；cancel 失败返回 502（不会假装成功）。会话未接管时 409。
  - 监控：`gofer job ls --tag relay-takeover` / `gofer job show <id>`（`job.input_injected` 记录首条输入的字节数与安静窗口）。
- turn 复用决策通道：铃铛里「会话」标签条目可直接内联作答；`gofer plan decisions --state OPEN` 也能看到（kind=relay；被"人回来"关掉的 turn 是 EXPIRED + `released_by=user_returned`）。

## schedule（别名 `sch`）— 定时 job

```bash
gofer schedule add <...job请求...>      # 从一个 job 请求建定时计划
gofer schedule list / show <id>
gofer schedule enable <id> / disable <id>
gofer schedule run <id>                 # 立即跑一次(不等下次触发)
gofer schedule rm <id>
```

## project（别名 `p` / `proj`）

```bash
gofer project list [--remote]           # 不带=本地(按 GOFER_RUN_MODE 读 config.yaml/worker.yaml); --remote=server 实时 project(client 模式默认即 --remote)
gofer project show <key>                # project 详情(client 模式来自 server 的 /v1/meta)
gofer project validate <key>            # 校验路径/agent/runner(别名 check)
gofer project add / remove <key>        # 注册 / 移除(client 模式拒绝: 需本地配置)
```

- POLICY 模式 worker 上 `gofer project list` 读 server 下发的策略缓存，列**映射后的本机路径**（见 `SKILL.md` §6）。

## config（别名 `cfg`）

```bash
gofer config info                       # 解析出的 config 路径 + 关键 ENV + 关键设置
gofer config show <project>             # 某 project overlay 合并后的有效 config
gofer config validate server|worker     # 校验 config(别名 check); worker 按模式给判据 + 校验 roots
gofer config edit                       # 用 $VISUAL/$EDITOR 打开解析出的 config
```

## init — 脚手架

```bash
gofer init [server|worker|client]       # 从内置 example 模板生成 config(默认 server); client 生成 <config-dir>/.env(GOFER_RUN_MODE=client 的纯客户端节点, 无 config.yaml/worker.yaml)
gofer init -g worker                    # 写到用户全局 config 目录(client 默认就写全局 config dir 的 .env)
gofer init [-g] skill                   # 装 gofer-usage skill: 默认写 ./.claude/skills 和 ./.agents/skills 两处; -g 写全局 ~/.claude+~/.agents; -o <dir> 单目标
```

`gofer init hooks [--agent claude|codex|all] [--global] [--remove]`：安装/卸载会话中继 hooks（见上文 session 节）。

## 运维向（AI 一般不直接用，了解即可）

- `serve` / `worker` / `presence`：起 server / worker、看在线状态。worker 配置见 [`worker-config.md`](worker-config.md)、server 配置见 [`server-config.md`](server-config.md)、加 project / 建 worker / 迁移分步见 [`setup-recipes.md`](setup-recipes.md)。
- `agent` / `mcp`：agent 定义探测、MCP 接入。
