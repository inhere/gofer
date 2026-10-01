<!-- template_id: design; template_version: 1.1.1 -->
# 从 web 给 Claude 终端会话发消息（Y6）

> 状态：Approved（Draft 0.1；用户 2026-10-01 在 web 中继批准，传话人配额默认每 runner 2 个）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-01 | Claude | 实测三条通道后形成方案 |

## 背景与目标

用户在 web 上需要随时给某个 Claude 终端会话（在本机或容器里跑的 `claude` 交互会话）发消息，例如布置任务、要进度。现在只有会话停在回合末尾、中继在等待时，web 回复才能送达；会话正在干活或已空闲时送不进去。用户的临时做法是：在 web 开一个 pty / ACP 的 Claude 会话当"传话人"，让它用 Claude Code 的会话间消息转给目标会话，再让它列出各会话名称来辨认是谁发来的消息。目标是把这个做法做成 web 上的一个按钮。

## 已确认事实（2026-10-01 容器实测）

1. **会话身份可读**：Claude Code 为每个交互会话进程写 `~/.claude/sessions/<pid>.json`，含 `sessionId`、`name`（如 `hyy-ai-inspect-22`，其他会话用它寻址）、`messagingSocketPath`、`status`（idle / busy）、`cwd`、`version`、`peerProtocol`。这是 Claude Code 的内部文件，无公开契约，只能尽力读取。
2. **会话所在机器已知**：`agent_sessions.runner` 记录了会话在哪个 runner（如 `w-docker-claude`）。
3. **一次性传话人可用**：在同一台机器上执行 `claude -p "<用 SendMessage 把原文发给 <名称>>" --allowedTools SendMessage,ListAgents`，约 10 秒送达；目标会话**正在干活时也能收到**（实测：本会话忙时收到测试消息）。
4. **PostToolUse 注入可用**：hook 返回 `hookSpecificOutput.additionalContext`，正在干活的会话在下一次工具调用后即在本回合中收到（实测通过）。
5. **授权语义**：经会话间消息送达的内容，接收方按"另一个会话转达"对待，不视为用户本人的指令或审批；经中继回复送达的内容才是用户本人的输入。

## 方案

1. **会话身份上报与显示**
   - hook 在 SessionStart / Stop / UserPromptSubmit 时，按 session_id 找到对应的 `~/.claude/sessions/*.json`，随心跳上报 `peer_name`、`peer_status`、`peer_messaging`（是否有通信地址）。找不到或格式不识别就不报，不影响其他功能。
   - web 会话列表每行显示：名称、内部会话 id（短格式，点击复制完整值）、忙闲状态、是否可接收消息。
2. **「发消息」按钮与投递**
   - 每行一个「发消息」，输入框发出后 server 记一条消息记录（存简单文件，不加表：`<storage.root>/sessions/<session_id>.outbox.jsonl`），并选择通道：
     - **会话停在回合末尾、中继在等**：作为中继回复送达（用户本人输入）。
     - **其他状态**：在会话所在 runner 上提交一个内部 exec job，运行一次性传话人（`claude -p` + SendMessage，原文转发，消息前加 `[来自 web，<用户>]` 前缀），job 结果即投递结果。
   - web 上显示每条消息的状态：排队中 / 已送达（经哪条通道）/ 失败原因（如会话已退出、runner 离线、传话人报错）。失败时可一键重试。
   - 同一会话的消息按顺序投递；传话人 job 不占用户的 agent 并发配额（内部 job，单独小配额，默认每 runner 2 个）。
3. **授权提示**：发消息框下方说明"会话忙或空闲时经会话间消息转达，对方不会把它当作你本人的审批；需要拍板的请等它停下后在中继里回复"。
4. **PostToolUse 注入**：本期不做主通道（需要各工作区额外安装 hook）；作为后续可选项。
5. **配置**：`server.session_messaging.enabled`（默认开）、`messenger_command`（默认 `claude`，可换路径）、`messenger_timeout_sec`（默认 90）。

## 不做

- 不直接实现 Claude Code 的会话间套接字协议（内部协议，随版本变化）；只通过官方 CLI 的 SendMessage 工具。
- 不做多轮对话式的常驻传话人；消息频繁时再评估。
- 不改变中继的授权语义。

## 测试与冒烟

固定测试：`TestHookReportsPeerIdentity`（读 sessions 文件、找不到时不报）、`TestSessionMessageViaRelayWhenWaiting`、`TestSessionMessageViaMessengerJob`（假 messenger 命令）、`TestSessionMessageOrderedPerSession`、`TestSessionMessageFailureReported`。
真实冒烟：容器内两个真实 Claude 会话，web 上给忙、闲、等待中的会话各发一条，核对送达通道与状态；截图会话列表的名称与 id。

## 待确认事项

1. 传话人 job 的单独小配额默认每 runner 2 个，是否合适。

## 结论与人工计划 Gate

审批后由 codex 直接实施（排在 Y5 之后，二者都改会话列表页，顺序执行避免冲突）。
