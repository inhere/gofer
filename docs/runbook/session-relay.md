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

| 时机 | 做法 |
|---|---|
| 要离开 | 对 agent 说"打开中继"，或自己敲 `gofer session relay on`（同目录多个会话时按提示加 `--session`） |
| 在外面 | gofer web → 会话页（`/sessions`）：等回复的会话置顶，打开抽屉看最后一条消息，底部输入框回复；铃铛里「会话」条目也可内联作答 |
| 让它停下 | web 回复 `/off`：关掉中继（mode=off），agent 正常结束回合 |
| 回到电脑 | 终端里任意输入一条（`on` → 降回 `auto`，`auto` 的等待直接释放）；或 `gofer session relay off` / `auto` |
| 忘了开 | `auto` 模式两条判据会自动布防（容器靠判据二）；要显式开就在 web 会话列表点 `on`，**下一次回合结束**生效；会话已空闲则需终端输入一次 |

CLI 等价面：`gofer session ls / show <id> / say <id> "<回复>" / rm <id>`（id 可用前 8 位）；`ls` 的 RELAY 列显示 `on` / `off` / `auto`，auto 且当前在等时显示 `auto·wait(i)`（键盘空闲）或 `auto·wait(t)`（距上次人工输入）；`show` 额外打印 mode 与判定依据。

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

- 会话表 `agent_sessions`：开关是 `relay_mode`（auto|on|off；`relay` 列保留为 `relay_mode=='on'` 的镜像，给旧二进制读），加上 `idle_sec`（键盘空闲读数）与 `last_human_at`（判据二的锚点）。turn 复用 `plan_decisions`（additive 列 `session_id`, `kind`）。
- 状态：`running → idle`(Stop, 不等) / `waiting_reply`(Stop, 等) / `needs_attention`(Claude Notification) / `ended`(SessionEnd)。
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
| 忘开开关且会话已空闲 | — | 硬边界：终端输入一次；或后续启用 tmux 兜底 |

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
