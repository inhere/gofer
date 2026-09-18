# Runbook · 终端会话 ↔ web 消息中继（session relay）

> 设计：[`../design/2026-09-06-agent-session-relay-design.md`](../design/2026-09-06-agent-session-relay-design.md)（SESS-01）。本文是操作手册 + 排障。

## 1. 一次性装配

```bash
# 需要 gofer ≥ 0.36（含 `gofer hook` / `gofer session` / `gofer init hooks`）
gofer init hooks                      # Claude Code: 合并写 ./.claude/settings.json
gofer init hooks --agent codex        # Codex:       合并写 ./.codex/hooks.json
gofer init hooks --agent all --global # 写到 ~/.claude 与 ~/.codex，对所有项目生效
gofer init hooks --remove             # 卸载（只删 gofer 自己的条目）
```

写入的 hook 命令全部是 `gofer hook <agent>`（Stop 为 `gofer hook <agent> --wait 7140`，hook timeout 7200s），另在 Claude 的 `env` 加 `CLAUDE_CODE_STOP_HOOK_BLOCK_CAP=500`。hook 进程连 server 的方式与 `gofer job` 一致：`$GOFER_CONFIG_DIR/.env` 的 `GOFER_SERVER_ADDR/TOKEN`。

Codex 额外条件：`config.toml` 里 `[features] hooks = true`（旧版本键名 `codex_hooks`），且项目层 `.codex/` 已被 Codex trust。

## 2. 日常使用

开关是**三态**（按会话存在 server，缺省 `auto`）：

| mode | 含义 | 怎么结束等待 |
|---|---|---|
| `on` | 每次停下都在 web 等你回复（显式开关） | 终端输入（降回 `auto`）、web 回复 `/off`、`gofer session relay off` |
| `off` | 从不等；已打开的 turn 被释放 | — |
| `auto`（缺省） | server 按下面两条判据决定本次停下要不要等 | 见「判据」 |

`auto` 的两条判据（各自显式写 `0` = 关闭该判据）：

1. **键盘空闲**（`session.auto_relay_idle_sec`，默认 300s）：hook 上报的键鼠空闲 ≥ 阈值 ⇒ 布防；人一碰键鼠 ⇒ 放行。
2. **距上次人工输入**（`session.auto_relay_turn_sec`，默认 900s，R2）：**探测不到键盘时**（容器 / 无 X11，空闲值恒为 -1）改用本会话的 `last_human_at`（SessionStart 与非注入的 UserPromptSubmit 会刷新）⇒ 布防；放行靠人的动作：按 Esc，或直接在终端输入一条（`UserPromptSubmit`/`Interrupt` 事件到达即把 turn 关成 `EXPIRED` + `released_by=user_returned`）。

**监督中不布防（SUP-01 D，`session.auto_relay_skip_when_supervising`，默认开）**：你（或一个监督用的 Claude 会话）一边等 job 一边开着 `auto` 时，键盘空闲/距上次人工输入的判据会把你**锁在 web 回复框上**——而你要等的是 job 完成通知，通知就在被阻塞的那个回合后面（bd h-aii-s2v4）。所以 `auto` 在这个条件下**根本不布防**：

- 该会话的 `caller_id`（注册时的认证身份）名下还有**在跑的 job**（状态 queued/running/pending_interaction/recovering，且在 `session.supervising_window_sec`（默认 7200s）之内）⇒ `wait_reason` 为空，hook 立即放行；web 会话页在自动开关旁显示原因（`未布防：supervising N jobs`）。
- job 一进终态，自动判据立刻恢复（`needs_review` 不算监督中：那是等人的验收，不是等人离开键盘）。
- 只影响 `auto`：显式 `on` 照旧每次停下都等（那是你明确要求的）。`caller_id` 为空的会话（老会话 / 未配 token）不套用——无从判定是谁的 job。
- 关掉：`session.auto_relay_skip_when_supervising: false`。

顺带：会话的 `caller_id` 同时也是**作答权**——`say` / `deliver` / `relay set-mode` 只允许该会话 owner 的 caller（governance `require_answer_capability` 开启时 `can_answer` 也可；`caller_id` 为空的老会话放行），worker token 一律 403（h-aii-esus）。

| 时机 | 做法 |
|---|---|
| 要离开 | 对 agent 说"打开中继"，或自己敲 `gofer session relay on`（同目录多个会话时按提示加 `--session`） |
| 在外面 | gofer web → 会话页（`/sessions`）：等回复的会话置顶，打开抽屉看最后一条消息，底部输入框回复；铃铛里「会话」条目也可内联作答 |
| 让它停下 | web 回复 `/off`：关掉中继（mode=off），agent 正常结束回合 |
| 回到电脑 | 终端里任意输入一条（`on` → 降回 `auto`，`auto` 的等待直接释放）；或 `gofer session relay off` / `auto` |
| 忘了开 | `auto` 模式两条判据会自动布防（容器靠判据二）；要显式开就在 web 会话列表点 `on`，**下一次回合结束**生效；会话已空闲则见「无 turn 时送话」 |
| 监督 job 时不想被挡 | `auto` 已自动让路（见上面「监督中不布防」）：不用手工 `/off`，也不会把 job 完成通知挡在回合后面 |
| 会话已空闲 | web 会话抽屉的输入框（没有 OPEN turn 时变成「送入终端」）直接送话；CLI `gofer session say <id> "<回复>" --deliver`。前提：会话在 tmux 里 + 登记了执行机（见下表） |

**无 turn 时送话（阶段 2-A，tmux 注入）**：`POST /v1/sessions/{sid}/deliver {text}`。有 OPEN turn 就等价 `say`（作答）；否则 server 在该会话的 runner 上起一个内部 exec job，确认 pane 存活、前台是 agent CLI（白名单 `session.inject_commands`，默认 `claude|codex|omp|node|gemini|opencode`），再逐行 `tmux send-keys -t <pane> -l -- '<行>'` + `Enter`（文本前缀 `[gofer web 回复] `，上限 8KB，单引号转义）。成功后会话置 `running`，审计行 `plan_decisions(kind=relay, detail={"path":"tmux","job_id":…})`。

**无 turn 时送话（阶段 2-B，`--resume` pty 接管）**：会话没有可用 tmux pane（`no_tmux` / `inject_failed:pane_missing`）时，带 `allow_takeover: true` 再发一次即可让 server **起一个新进程接管**：`POST /v1/sessions/{sid}/deliver {text, allow_takeover: true}`（`allow_takeover` 缺省 false —— 接管会把会话从原终端移走，必须显式要）。server 用该会话的 `session_id` 在**同一 runner、同一项目相对目录**起一个交互 pty job（`claude --resume <sid>` / `codex resume <sid>` / `omp --resume <sid>`，argv 来自 agent 的 `SessionResumeInteractive` 模板），把 `[gofer web 回复] <文本>\r` 作为该终端的**首条输入**（pty 首次输出后安静 `session.takeover_input_delay_ms`（默认 1500ms）再写，最多等 10s；多行原样写入），会话随即置 `handed_off`（记 `handed_off_job_id`/`handed_off_at`，审计 `detail={"path":"takeover","job_id":…}`），web 跳到 `/jobs/<id>?attach=1`。前提：agent 有交互 resume 模板（内置 claude/codex/omp）、项目 `allow_interactive: true`、会话 cwd 能换算成执行机上的项目相对路径。

- **原终端会发生什么**：它的中继**停用**（`wait_reason` 恒空、`OpenTurn` 拒绝），下一次 hook 事件（Stop / 你敲字）在 stderr 打一行 `该会话已于 <时间> 在 web 接管（job <id>），继续请在 web 终端或 --resume；本终端的中继已停用`。原进程仍在跑，但两个进程写同一 CLI 会话会分叉 —— 要继续就在 web 的接管终端里说。
- **解除接管**：web 会话抽屉「解除接管」→ `POST /v1/sessions/{sid}/release-takeover`：先 cancel 接管 job，再把会话置回 `idle` 并清空 `handed_off_*`，原终端恢复中继（cancel 失败会报 502，不假装成功）。CLI 无对应子命令，用 web 或 HTTP。
- **CLI 等价面**：`gofer session say <id> "<文本>" --deliver --takeover`（`--takeover` 必须与 `--deliver` 同时用）。

失败原因（看 server 返回的 `error` 字段 / web 提示）：

| 原因码 | 含义 / 处理 |
|---|---|
| `no_tmux` | 会话不在 tmux 里（web 报 409）。用 `tmux new -A -s claude` 重开会话，或带 `allow_takeover` 走 B（web 会直接给「起新进程接管并发送」按钮） |
| `no_runner` | 会话没登记执行机：容器内没起 worker，或 `.env` 里缺 `GOFER_HOOK_RUNNER=<worker-id>`（`GOFER_RUN_MODE=client` 的节点不再假装登记成 `server`） |
| `ended` | 会话已结束 |
| `inject_failed:pane_missing` | pane 没了（tmux 会话被关 / 换了 window）；带 `allow_takeover` 时 server 自动改走 B |
| `inject_failed:pane_busy:<cmd>` | pane 前台不是 agent CLI（例如 vim / shell），拒绝敲字；也不会改走 B（你在用那个终端） |
| `inject_failed:runner_error` | 执行机侧失败（runner 不可用、job 超时、project 未开 `allow_exec`、B 的接管 job 提交被拒等）；`gofer job ls --tag relay-inject` / `--tag relay-takeover` 找到那个 job 看日志 |
| `no_resume_template` | B 前提不满足：该 agent 没有交互 resume 模板（内置 claude / codex / omp 有） |
| `interactive_not_allowed` | B 前提不满足：项目未开 `allow_interactive` |
| `cwd_outside_project` | B 前提不满足：会话 cwd 换算不到执行机上的项目相对路径（POLICY roots 映射后两台机路径不同，见 `docs/design/2026-09-06-agent-session-relay-design.md` §9.1 限制） |
| `handed_off:<job>` | 该会话已被 job `<job>` 接管：到那个终端继续，或先解除接管 |

CLI 等价面：`gofer session ls / show <id> / say <id> "<回复>" [--deliver [--takeover]] / rm <id>`（id 可用前 8 位）；`ls` 的 RELAY 列显示 `on` / `off` / `auto`，auto 且当前在等时显示 `auto·wait(i)`（键盘空闲）或 `auto·wait(t)`（距上次人工输入）；`show` 额外打印 mode 与判定依据。

## 3. 运行机制速览

```
Stop hook → gofer hook <agent>
  → POST /v1/sessions/{sid}/heartbeat {event:Stop, last_message, idle_sec}  # 不等: 到此结束(<300ms)
       返回 wait_reason = mode_on | idle_probe | turn_age | ""(不等)
  → 等待: POST /v1/sessions/{sid}/turns → plan_decisions(kind=relay, OPEN)
  → GET /v1/sessions/{sid}/turns/{id}?wait=25 长轮询, 直到 answered / expired / relay_off
  → answered: stdout {"decision":"block","reason":"[gofer web 回复] <文本>"} → agent 续跑
      · idle_probe 的等待: 每 ≤5s 补一次 POST …/turns/{id}/release {idle_sec}(人回来即放行)
      · turn_age 的等待: 不探测; 人回来时的事件(UserPromptSubmit/Interrupt)由 server 直接关 turn
```

- 会话表 `agent_sessions`：开关是 `relay_mode`（auto|on|off；`relay` 列保留为 `relay_mode=='on'` 的镜像，给旧二进制读），加上 `idle_sec`（键盘空闲读数）、`last_human_at`（判据二的锚点）与 `handed_off_job_id`/`handed_off_at`（阶段 2-B 的接管 job）。turn 复用 `plan_decisions`（additive 列 `session_id`, `kind`, `detail`）。
- 状态：`running → idle`(Stop, 不等) / `waiting_reply`(Stop, 等) / `needs_attention`(Claude Notification) / `handed_off`(web 起了 `--resume` pty job 接管，原终端不再中继) / `ended`(SessionEnd)。`handed_off` 只由 `release-takeover` 解除 —— 原终端的任何事件都不会把它改回去。
- `on` → 人在终端输入（UserPromptSubmit）即降回 `auto`；`auto` 的等待在人回来时直接释放（探得到就探，探不到就靠事件）。harness 产生的同名事件（注入回复带 `[gofer web 回复]` 前缀、后台任务通知 `<task-notification>`、系统提醒）hook 会上报 `injected`：不动开关、也不当作"人回来了"；`hook.log` 里能看到 `human prompt` / `harness prompt` 的判定。
- 日志：`<config-dir>/run/hook.log`（>5MB 自动清空）；每个事件一行，含 state / relay mode / wait reason。

## 3.1 手机提醒（可选）

会话等在那里时想让手机响一下，配一个钉钉/飞书群机器人即可，见
[`im-notification.md`](im-notification.md)。要点：事件名 `session.waiting` 必须
显式写进 webhook 的 `events`（默认订阅集不含它），并配 `server.web_base_url`
让消息里带一条直达会话抽屉的链接。

## 4. 排障

| 现象 | 查看 | 处理 |
|---|---|---|
| web 看不到会话 | `gofer session ls`；`hook.log` 有无 `SessionStart registered` | hooks 未装（`gofer init hooks`）；或 hook 进程连不上 server（`hook.log` 出现 `register failed` → 检查 `.env`）。装好后新会话才登记，老会话在下一个事件时自动补登记 |
| 开了中继但停下时没进 web | `hook.log` 该会话最后一行 | `relay off, released` = server 说这次不等（mode off，或 auto 的判据没成立：看 `gofer session show <id>` 的 relay 行）；`open turn failed ... 409` = 同一瞬间被关 |
| 容器里会话不自动布防 | `gofer session show <id>` 的 relay 行 | 空闲值恒为 `-1`（无 X11）→ 走判据二：确认 `session.auto_relay_turn_sec`（默认 15 分钟）没被写成 `0`，且 `last_human_at` 不是 0 |
| auto 判据没成立却以为会等 | `gofer session ls` 的 `auto·wait(i)` / `auto·wait(t)` | 探到键盘时以空闲值为准：人还在别的窗口打字（空闲小）就不会布防 |
| 回复后 agent 没继续 | `hook.log` 有无 `answered (...) continuing` | 有 → agent 已收到，看终端；没有 → turn 可能已过期（`gofer session show` 里 `[EXPIRED]`），重新让它停一次 |
| 终端一直"hook 运行中" | 正常：这就是等待 | 想直接输入按 Esc 取消；或 web 回复 `/off` |
| Stop 后终端立刻恢复但没走中继 | `hook.log` 有 `heartbeat failed` | server 不可达，hook 按设计直接放行 |
| 忘开开关且会话已空闲 | — | 走「无 turn 时送话」：web 抽屉「送入终端」/ `session say --deliver`（需 tmux + 已登记执行机）；没有 tmux 就用「起新进程接管并发送」/ `--deliver --takeover`；否则在终端输入一次 |
| 会话显示「已接管」但我要回原终端 | `gofer session show <id>` 看 `handed_off_job_id` | web 抽屉「解除接管」（cancel 接管 job 后会话回 idle）；或在接管终端里继续 |
| 接管后原终端敲字没反应 | 设计如此 | 原终端的 hook 会打一行 `该会话已于 … 在 web 接管`；两个进程写同一会话会分叉，继续请在接管终端 |
| 接管报 `cwd_outside_project` | `gofer session show <id>` 的 cwd 与该项目在执行机上的根 | 容器与主机路径不一致（POLICY roots 映射），让两侧同名路径（bind mount）后再试；A 路径不受影响 |

## 5. 端到端验证（容器内自测记录 2026-09-06）

临时 serve（127.0.0.1:18765）+ `gofer init hooks -o <proj>` + `claude -p`（PATH 指向新二进制）：

1. SessionStart 登记，项目由 cwd 匹配；UserPromptSubmit 写入标题。
2. `relay on` 后 agent 停下 → `waiting_reply`，`last message = "step one done"`。
3. `session say <sid> "Now reply with exactly: step two done"` → hook 输出 block → agent 续跑，最终输出 `step two done`。
4. 第二次停下 → `say /off` → hook 放行，SessionEnd → `ended`。
5. 关着开关的 Stop 耗时 12ms；server 停机时 hook 直接退出。

6. web 侧（agent-browser）：会话页「AGENT 会话」分组显示等待回复行 + 铃铛 toast；抽屉内输入框发送 → 挂起的 hook 立即输出 `{"decision":"block","reason":"[gofer web 回复] …"}`，会话回到 running。
7. 等待超过 hook 预算（`--wait`）时 turn 过期、hook 放行、会话置 idle，开关保持 on（下一次 Stop 继续中继）。

8. 工作空间根 + 主机 serve（nssm）真机：`gofer init hooks` 与 bd 的 SessionStart hook 共存；会话按 cwd 匹配到项目、runner=容器 worker；后台任务通知触发的 UserPromptSubmit 被判为 harness、中继保持；web 抽屉作答注入、`/off` 放行，两轮全通。

未覆盖：Codex 真机（容器内无 codex），待主机上按 §1 装配后跑同样四步；交互 TUI 下等待期间按 Esc 的表现。
