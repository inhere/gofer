# 终端会话交给 web：hooks、中继、送话、唤醒

> 人离开电脑时，让**这个终端会话**停下来等的那条消息进 gofer web「会话」页，人在 web（手机也行）回复，回复注入**同一个**会话继续跑。靠 agent CLI 的 hook 实现，不开 pty、不重开会话。完整 flag 以 `gofer session --help` / `gofer init hooks --help` 为准。

## 目录

- [安装 hooks](#安装-hooks)
- [常用命令](#常用命令)
- [三态开关 on / off / auto](#三态开关-on--off--auto)
- [自动布防的两条判据](#自动布防的两条判据)
- [人回来即放行](#人回来即放行)
- [会话进行中预览、用量与授权上 web](#会话进行中预览用量与授权上-web)
- [给不在等回复的会话送话](#给不在等回复的会话送话)
- [唤醒 / 接管会话](#唤醒--接管会话)
- [web 发消息（转达）与会话回复](#web-发消息转达与会话回复)
- [session watch：job 跑完时被叫醒](#session-watchjob-跑完时被叫醒)
- [会话催办（nudge）](#会话催办nudge)
- [自研 agent 接入（generic）](#自研-agent-接入generic)
- [其它约定](#其它约定)

## 安装 hooks

```bash
gofer init hooks                         # 写 ./.claude/settings.json（默认 --agent claude）
gofer init hooks --agent codex|omp|jcode|all   # all = 四个都写
gofer init hooks --agent all --global    # 用户级一次装好，所有工作区生效
gofer init hooks --remove [--agent X] [-o <项目目录>]
gofer init hooks --prime-only --global --agent all   # 只装记忆注入（见 tracker.md）
```

| agent | 配置落点 | web 回复注入 |
|---|---|---|
| claude | `.claude/settings.json`（`--global` → `~/.claude`） | 是 |
| codex | `.codex/hooks.json`（`--global` → `~/.codex`；codex 要在交互模式批准一次，`config.toml` 需开 hooks 功能，项目 `.codex/` 需 trust） | 是 |
| omp | 扩展 `.omp/extensions/gofer-relay.ts`（`--global` → `~/.omp/agent/extensions/`） | 是（回复作为新一轮消息送回） |
| jcode | `~/.jcode/config.toml` 的 `[hooks]`（无项目级，须 `--global` 或 `-o <JCODE_HOME>`） | 否，只观察；要传话用 tmux 送话 |
| generic（自研） | 由 agent 自己调 `gofer hook generic --agent <key>` | 是 |

- 幂等，只增删 gofer 自己的条目。jcode 每个事件只能配一条命令，已有用户命令时不覆盖并提示；omp 扩展文件带 gofer 标记，同名的非 gofer 文件拒绝覆盖（`--force` 才换）。
- 全局安装后会扫描当前目录与已登记项目，列出已有的**项目级** gofer hooks（会与用户级重复触发）并给出清理命令。不在项目列表里的目录，会话以「无项目」登记：中继 / 传话可用，唤醒需要项目。
- 老版本装过的 hooks 重跑一次 `gofer init hooks` 即可获得新条目（子 agent、进行中预览、授权上 web、命令前记忆注入等）。

## 常用命令

```bash
gofer session relay on|auto|off [--session <id>]   # 省略 --session 按当前目录反查
gofer session ls [-p <p>] [--state …] [--all]      # waiting_reply 置顶；RELAY 列 on/off/auto；NAME 列是 Claude Code 会话名
gofer session show <id>                            # 详情 + 最近 turn；relay 行显示 mode 与判定依据
gofer session say <id> "<回复>"                    # = web 输入框：答最新的等待 turn；"/off" = 关中继并放行
gofer session say <id> "<回复>" --deliver          # 没有 turn 在等也送（见「送话」）
gofer session say <id> "<回复>" --deliver --takeover   # 没有 tmux 时起新进程接管（见「唤醒 / 接管」）
gofer session resume <id> [--input "首条消息"] [--plan]
gofer session release-takeover <id>
gofer session watch <job-id> [--session <id>]
gofer session nudge <id> --when-stalled 20m -m "进展如何？"
gofer session rm <id>
```

**约定**：用户说「打开中继 / 我要离开了 / 交给 web」→ 执行 `gofer session relay on`，然后正常结束回合；之后每次回合结束都在 web 等回复，直到 web 回复 `/off`、终端有人输入、或 `relay off`。

## 三态开关 on / off / auto

按会话存在 server，缺省 `auto`：

| mode | 含义 |
|---|---|
| `on` | 每次停下都在 web 等回复；终端有人工输入会自动回到 `auto`（`session show` 多一行 `note:`，web 抽屉显示说明）；web 回复 `/off` 或 `relay off` 关掉 |
| `off` | 从不等；已打开的等待被释放 |
| `auto` | server 按下面两条判据决定这次停下要不要等 |

- hook 每次 Stop 先问 server，不等则零阻塞；server 不可达也直接放行，**永不卡死终端**。
- web 会话列表里也能点切换，**下一次回合结束**生效；已停在空闲提示符、且人从未离开过的会话没有 hook 在跑，需在终端输入一次。
- 等待上限由 server 下发：`on` → `session.relay_on_wait_sec`（默认 3600），`auto` → `session.relay_auto_wait_sec`（默认 600），`0` = 不设上限。

## 自动布防的两条判据

忘了开也不要紧，`auto` 下 server 自动布防：

1. **键盘空闲**（`session.auto_relay_idle_sec`，默认 300，写 0 关）：hook 上报键鼠空闲秒数，人离开超过阈值就等 web 回复（列表显示 `auto (idle 12m)`）。Windows / macOS 探得到键盘；Linux 需要 `xprintidle`（X11），Wayland / 无桌面 / 容器里为「未知」。
2. **距上次人工输入**（`session.auto_relay_turn_sec`，默认 900，写 0 关）：探测不到键盘时（容器里的 hook 测不到主机键盘），看会话里人最后一次输入距今多久，到阈值同样布防（列表显示 `auto (no input 22m)`）。

不布防的情况：你名下还有 job 在跑、或会话里有子 agent 在跑（视同「监督中」，受 `session.auto_relay_skip_when_supervising` 控制；显式 `on` 不受影响）。最后一个子 agent 结束时，已阻塞的 auto 等待会被释放。

## 人回来即放行

- 判据一开的等待：hook 每 ≤5 秒重探空闲值，人一碰键鼠就放行。
- 判据二开的等待：按 Esc 结束等待，或直接在终端输入一条，server 就把等待关掉。
- 显式 `on` 的等待不被终端输入关闭（输入只把 mode 降回 `auto`）；但**按 Esc** 会关掉这一轮等待、会话回 idle、开关保持 `on`（下次停下照常中继）。
- web 注入的回复带前缀 `[gofer web 回复]`，与终端输入等价处理，但不会被当成「人回来了」。

## 会话进行中预览、用量与授权上 web

- **进行中预览**：PostToolUse hook 读会话最新的 assistant 文字，以「进行中」更新会话列表、抽屉和工作台；`session.progress_interval_sec` 默认 30 秒，0 关闭。
- **会话用量**：hook 在 Stop / SubagentStop / SessionEnd 增量读会话记录，上报 token（主会话与子 agent 分开、按模型）；Sessions 详情、会话抽屉、工作项卡、首页用量卡可见。只有 token，来源无费用就不显示费用。jcode 不采集。
- **终端授权上 web**（claude）：终端弹工具授权时，会话最后消息显示「需要授权：Bash `…`」；会话正在等 web（同 on / auto 判据）时，「今天」页与会话抽屉出「需要授权」卡，可允许 / 总是允许 / 拒绝 / 附原因拒绝，与终端对话框先答者生效。只有会话本人能答；`session say` 不会答它。
- **会话名**：claude hook 上报 Claude Code 自己的会话名，web 与 `session ls` 的 NAME 列、`session show` 的 `name:` 行显示，可一键复制。
- **离线**：Claude Code 进程被直接杀掉时没有结束事件；会话超过 `session.offline_after_sec`（默认 1800，0 关闭）无心跳就标 `offline`（灰色徽标），收到新心跳恢复。`offline` 与 `ended` 一样可以唤醒。

## 给不在等回复的会话送话

会话没有等待中的 turn（在干活或已空闲）时，`session say --deliver`（web 抽屉的「送入终端」）按顺序尝试：

1. **agent 的送话命令**（`deliver_command`）：在会话登记的执行机上跑这条命令，退出码 `0` = 已送达，`3` = 进程不在线（继续往下），其它 = 失败（不再退到 tmux，避免重复送）。codex agent 内置默认用 `codex queue` 排队消息，已退出的会话下次 resume 时收到。
2. **tmux 注入**：确认 pane 存在、前台是 agent CLI（默认白名单 `claude|codex|omp|node|gemini|opencode`，`session.inject_commands` 可配），把 `[gofer web 回复] …` 逐行敲进去（上限 8KB）。
   - 前提：会话跑在 tmux 里（建议 `tmux new -A -s claude`）且登记了执行机。**容器里的会话**要在容器内起一个 gofer worker，并在容器 `.env` 配 `GOFER_HOOK_RUNNER=<worker-id>`；纯客户端节点不会登记执行机。
   - 注入是一个 exec job，项目需 `allow_exec: true`（worker 侧 `guards.allow_exec` 也不能收紧）。
3. 前两步都不行时，`--takeover` 起新进程接管（见下）。

失败原因：`no_runner`（未登记执行机）、`no_tmux`（不在 tmux）、`ended`、`deliver_failed:<原因>`、`inject_failed:pane_missing`（pane 没了）、`inject_failed:pane_busy:<cmd>`（前台是别的程序，如 vim）、`inject_failed:runner_error`。

## 唤醒 / 接管会话

- **接管**（`--deliver --takeover`，web「起新进程接管并发送」+ 二次确认）：在同一 runner、同一项目相对目录起一个交互 pty job（`claude --resume <sid>` / `codex resume <sid>` / `omp --resume <sid>`），把消息作为首条输入，web 跳到该 job 的终端继续聊。
- **唤醒**（`gofer session resume <id> [--input …]`，web Sessions 每行与会话抽屉的「唤醒 / 接管」按钮）：同一种 `--resume` pty job，**已结束的会话也能唤醒**。`--plan` 只问能不能、在哪个目录起，不起进程。不能唤醒时按钮灰显并写原因。
- 前提：agent 有交互续接模板（内置 claude / codex / omp）、项目 `allow_interactive: true`、会话目录能换算成执行机上的项目相对路径；否则报 `no_resume_template` / `interactive_not_allowed` / `cwd_outside_project`。
- 唤醒目录：优先用会话记录文件验证出的原始启动目录，其次登记的 cwd，再次项目根。
- **原进程还在线就拒绝**（`session_alive`）：最近一次心跳在 `session.takeover_alive_sec`（默认 120 秒，0 关）内算在线，避免两个进程写同一会话分叉。会话显示未结束但你确定终端已关时，先关掉原终端再接管。
- 接管后会话为 `handed_off`，原终端的中继停用（hook 在 stderr 提示）；继续请在接管终端里。想回原终端：web「解除接管」或 `gofer session release-takeover <id>`（先取消接管 job）。接管 job 结束时自动释放；本来已结束的会话回到 `ended`。
- `gofer job ls --tag relay-takeover` 列出接管 job。

## web 发消息（转达）与会话回复

- 会话不在等回复时，web 的「发消息」经**传话人**把话转给目标会话（在会话所在 runner 上用 `claude -p … --allowedTools SendMessage,ListAgents` 转发；server 本机与较新的 worker 用常驻传话进程）。需要目标是有会话间通信能力的 Claude Code 会话。
- **接收方看到的是「另一个会话转达的消息」**（前缀 `[来自 web，<用户>]`），不是用户本人的指令或审批：需要批准 / 拍板的事，等它停下后在中继里回复或终端输入，不要把转达当授权。
- **作为目标会话的 agent**：收到 `[来自 web，…]` 的转达，直接用 SendMessage 回给 `from` 地址，原话会出现在 web 对话里；也可以正常在终端回答。前提是装了 `gofer init hooks` 的 PostToolUse 条目（缺省就装）。
- web 对话流里显示送达状态（排队 / 已送达 / 失败可重试）。Runners 页每台 runner 的卡片显示传话人状态（未启动 / 空闲 / 处理中），抽屉里有最近投递、错误输出尾部与「列出可见会话」。配置 `server.session_messaging` 见 [server-config.md](server-config.md)。
- 「无需回复」：会话停在等回复的 turn 上时，web 可标已读（可撤销）。这不是回答、也不结束等待，只是从「待我决策」里消掉；要放行用 `/off` 或 `relay off`。

## session watch：job 跑完时被叫醒

`gofer session watch <job-id>`（省略 `--session` 按当前目录解析）把当前会话登记为盯这个 job；会话停下时，job 终态通知注入回终端。

- 即使中继关闭，只要还有未结束的 watch，Stop 也会只等 job 事件（不转发 web 输入）；错过的通知在下一次输入 / 会话开始时补投一次（claude / codex；omp 靠 Stop，jcode 只观察）。
- 你 `gofer job run` / `watch` 后 PostToolUse hook 会自动登记；Stop 还会自动认领「来源会话就是本会话、且 caller / 项目 / runner / cwd 都匹配」的在途 job。其它 job 用 `session watch` 显式关联。

## 会话催办（nudge）

```bash
gofer session nudge <id> --when-stalled 20m -m "进展如何？" [--until 2h]   # 运行中（或已停下但工作项未结）且 20 分钟无进展就发
gofer session nudge <id> --every 30m -m "…"                                # 定时发（最小间隔 1m）
gofer session nudge ls [<id>] [--all]
gofer session nudge rm|pause|resume <nudge-id>
```

- 送达走与 web「发消息」同一条路径；连续 3 次送达失败自动 `paused`（可订阅事件 `session.nudge_paused`），`resume` 清零后继续。会话结束 / 被接管 / 到 `--until` 时自动结束；`offline` 的会话跳过。
- 只有人（会话属主或有回答权限）能建、改、删；job 凭据与管家不行。web 会话抽屉有「催办」小区块。

## 自研 agent 接入（generic）

- hook：agent 自己调 `gofer hook generic --agent <gofer agent key>`，stdin 与 claude 同形 JSON；事件 SessionStart、UserPromptSubmit（可带 `"injected": true` 表示注入输入，不算人工输入）、PreToolUse（带 `tool_name` 与 `tool_input.command`，输出命令记忆）、PostToolUse、Stop、Interrupt、SessionEnd、Notification。Stop 阻塞，输出同 claude；最后一条消息取载荷的 `last_assistant_message`。
- agent 配置可加：`ndjson_usage_path`（用量）、`transcript_dialect: generic`（会话记录方言）、`inject_process`（tmux 送话前台进程名）、`session_family`（与同族 agent 互相续接）、`deliver_command` / `deliver_stdin` / `deliver_offline_match`（在线送话）。配置样例见 [server-config.md](server-config.md)「agents」与 <https://github.com/inhere/gofer/blob/main/docs/runbook/session-relay.md>。

## 其它约定

- **在 gofer job 里不触发中继**：环境里有 `GOFER_JOB_ID`（你本身是 gofer 派出的 job）时 hook 直接放行。
- **忽略 agent 内部会话**：cwd 在 `~/.codex/memories` 或 `GOFER_HOOK_IGNORE_CWDS` 列出的目录时 hook 静默退出。
- **会话身份**：会话的重复登记与心跳要求同一个认证 caller；worker token 只能操作属于该 worker 的会话。
- **让手机响一下**：配钉钉 / 飞书群机器人并显式订阅 `session.waiting`（不在默认集），消息带直达会话的链接。见 <https://github.com/inhere/gofer/blob/main/docs/runbook/im-notification.md>。
- 更多细节：<https://github.com/inhere/gofer/blob/main/docs/runbook/session-relay.md>。
