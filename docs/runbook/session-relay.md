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

| 时机 | 做法 |
|---|---|
| 要离开 | 对 agent 说"打开中继"，或自己敲 `gofer session relay on`（同目录多个会话时按提示加 `--session`） |
| 在外面 | gofer web → 会话页（`/sessions`）：等回复的会话置顶，打开抽屉看最后一条消息，底部输入框回复；铃铛里「会话」条目也可内联作答 |
| 让它停下 | web 回复 `/off`：关中继，agent 正常结束回合 |
| 回到电脑 | 终端里任意输入一条即自动关中继；或 `gofer session relay off` |
| 忘了开 | web 会话列表拨开开关 → 下一次回合结束生效；会话已空闲则需终端输入一次 |

CLI 等价面：`gofer session ls / show <id> / say <id> "<回复>" / rm <id>`（id 可用前 8 位）。

## 3. 运行机制速览

```
Stop hook → gofer hook <agent>
  → POST /v1/sessions/{sid}/heartbeat {event:Stop, last_message}   # 关着开关: 到此结束(<300ms)
  → relay on: POST /v1/sessions/{sid}/turns → plan_decisions(kind=relay, OPEN)
  → GET /v1/sessions/{sid}/turns/{id}?wait=25 长轮询, 直到 answered / expired / relay_off
  → answered: stdout {"decision":"block","reason":"[gofer web 回复] <文本>"} → agent 续跑
```

- 会话表 `agent_sessions`；turn 复用 `plan_decisions`（additive 列 `session_id`, `kind`）。
- 状态：`running → idle`(Stop, 关) / `waiting_reply`(Stop, 开) / `needs_attention`(Claude Notification) / `ended`(SessionEnd)。
- 日志：`<config-dir>/run/hook.log`（>5MB 自动清空）；每个事件一行，含 state / relay。

## 4. 排障

| 现象 | 查看 | 处理 |
|---|---|---|
| web 看不到会话 | `gofer session ls`；`hook.log` 有无 `SessionStart registered` | hooks 未装（`gofer init hooks`）；或 hook 进程连不上 server（`hook.log` 出现 `register failed` → 检查 `.env`）。装好后新会话才登记，老会话在下一个事件时自动补登记 |
| 开了中继但停下时没进 web | `hook.log` 该会话最后一行 | `relay off, released` = 开关在 Stop 前被关了（多半是终端里输入过一条 → 自动关）；`open turn failed ... 409` = 同一瞬间被关 |
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

未覆盖：Codex 真机（容器内无 codex），待主机上按 §1 装配后跑同样四步；交互 TUI 下等待期间按 Esc 的表现。
