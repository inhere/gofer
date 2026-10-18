# 信箱运维 Runbook

> issue：gofer-e6sb。设计：`docs/design/2026-10-16-session-topics-mailbox-and-inbox-design.md`；计划：`docs/plans/2026-10-18-mailbox-phase2-plan.md`。适用 v0.174.0 起（发信 / hook 送达自 v0.173.0）。

## 1. 数据在哪

- 信件在 server 元数据库的 `mail` 表（每封一行：发件出处、原始地址、解析后的收件方、状态、正文、租期、送达期限、关联的收集箱条目）。
- 状态：`pending`（待送）→ `delivering`（被 hook 或投递器占用，带租期）→ `delivered` → `answered`；旁路 `held`（待指派，同时生成一条收集箱条目）、`expired`、`failed`、`cancelled`。
- 待指派的信件在收集箱里有对应条目：候选会话、改投、新建 job、丢弃都在条目上操作；web 信箱页的待指派信件有「在收集箱处理」链接。

## 2. 送达路径（排查顺序）

| 收件方 | 路径 | 看哪里 |
|---|---|---|
| Claude Code / codex 会话 | 下一轮开始（SessionStart / UserPromptSubmit）注入；急件在下一个配了 PreToolUse 的工具前注入（每会话约 30 秒检查一次，失败后退避 2 分钟）；人离开且已有待信时 Stop 送出 | 主机或容器的 `<config-dir>/run/hook.log`（`urgent mail:`、`stop: delivering`） |
| 空闲会话（idle / offline 超过 `mail.wake_idle_sec`） | server 投递器发唤醒提示（每会话每 `mail.wake_interval_sec` 最多一次），会话醒来后按上一行注入 | server 日志 `serve.log` 里的 mail deliverer 行 |
| suga 会话 | 投递器经 `suga send` 直送正文；退出码 3 = 不在线，信件留在 pending，2 分钟后再试 | `serve.log` |
| ACP 会话 job | job 处于 `awaiting_input` 时投递器经 `job say` 一次送一封 | job 详情的会话记录 |
| pty 会话 job | 不支持，信件直接转待指派 | 收集箱 |

常见问题：

- **信件一直 pending**：收件会话没有新一轮、也没被唤醒。看会话状态（`gofer session ls`）：`running` / `waiting_reply` 不会被唤醒；`idle` / `offline` 要等 `last_seen_at` 超过唤醒阈值。超过 `mail.deliver_by_sec`（默认 24 小时）后转待指派。
- **急件没在一轮里注入**：PreToolUse 只在 hooks 配了的工具前触发（`gofer init hooks` 生成的是 shell 命令前）；并且每会话约 30 秒才检查一次。检查过程不阻塞工具调用：超时（300ms）后放弃，信件下一轮再送。
- **地址有歧义**：`topic:` / `path:` 命中多个或零个会话时不投递，转待指派并列出候选。

## 3. 配置

server 配置的 `mail:` 块（热重载生效）：`tick_sec`（默认 30）、`wake_idle_sec`（300）、`wake_interval_sec`（600）、`deliver_by_sec`（86400）、`claim_lease_sec`（60，hook 取件与投递器占用共用）。保存期：`storage.mail_retention_days`（默认 90；负数 = 永久保留），只删终态信件，pending / delivering / held 不删。

## 4. 敏感信息

- `gofer job secret-scan` 的报告含信件段（非 admin 只看自己发出或收到的）；`--redact` 同时处理命中的信件。
- 单封脱敏：`gofer session mail redact <id>`（literal / pattern 从 stdin 或参数读，与 job redact 相同；原文不进 argv）。会同步改写收集箱条目日志里的回复摘要。
- 信件脱敏目前没有审计事件（job 脱敏记在 job 事件里）。

## 5. 回滚

- 回到 v0.173 之前的版本：旧版本不读 `mail` 表，会忽略 `mail:` 配置块与 `storage.mail_retention_days`（不影响启动）。信件留在库里不动，不会丢；重新升级后，未过期的 pending 信件照常送达，过期的由投递器转待指派。
- 回到 v0.173.x：没有 server 投递器（不唤醒、不送 suga / ACP 会话 job），没有 web 信箱页与信件脱敏；hook 送达与 `gofer session mail` 照常可用。
- 不需要回滚前的数据迁移；如果不希望回滚期间积压的信件在重新升级后被送达，回滚前用 `gofer session mail ls --state pending` 查看并逐封 `cancel`。
