# 传话回复直达 web（gofer-6er0）

> 状态：实施中（2026-10-10）。issue：gofer-6er0。
> 目标：web 经传话人发给终端会话的消息，目标会话的回复**原文**直接出现在 web 该会话的对话里，标明来自会话本身；传话人不再转述。

## 0. 现状与根因（2026-10-10 实测）

链路：web「发消息」→ `sessionrelay.SendMessage` → 传话人（`claude -p … --allowedTools SendMessage,ListAgents`，常驻 stream-json 进程或一次性 job）用 SendMessage 把 `[来自 web，<用户>] <原文>` 发给目标会话。

1. **目标会话看到的是"另一个会话发来的消息"**：Claude Code 把它注入成 `<cross-session-message from="uds:/tmp/cc-socks/<pid>.sock" from-name="<传话人名>">…`（transcript 里是 `type=user`、`origin={kind:"peer", from, name, body}` 的条目，回合中途到达时是 `attachment.origin`）。模型自然用 SendMessage 回给 `from` 地址——也就是传话人。实测 transcript：`SendMessage{to:"uds:/tmp/cc-socks/4014788.sock", message:"<回复原文>", summary:…}`。
2. **gofer 没有任何一条路把这条回复带回 web**：web 会话对话（SessionDrawer 时间线）只有两类条目——中继 turn（Stop hook 的最后一条消息 + 人的回复）和 outbox（web→会话的消息）。回复只进了传话人的模型上下文。
3. **传话人"转述"**：常驻传话人收到回复后会自己开一个回合（stream-json 输出：新的 `system/init` → assistant 文本 → `result`，前后由 `command_lifecycle started/completed` 包住），模型把回复改写成"xxx 回复说……"。这个**不请自来的 result** 被 `messenger.process.read` 塞进只有 1 个缓冲的 `events`，**被下一次投递当成自己的结果读走**：下一条 web 消息的传话 job 输出变成上一条回复的转述（用户在 web 上看到的"转述"就是它），之后每次投递都错位一拍；连续两条不请自来的 result 还会让读协程阻塞在 `events <-`。一次性传话人回完"已发送"就退出，目标的回复发到一个已关闭的地址。

结论：缺的是"回复 → web 对话"的通道；传话人的错位是同一现象的副作用，也必须修。

## 1. 方案选择

| 方案 | 要目标模型配合 | 原文 | 覆盖通道 | 评价 |
|---|---|---|---|---|
| (a) 传话人原样转回 | 否 | 要读传话人 transcript 才是原文 | 只有常驻传话人（一次性的已退出）；worker 上的要新增协议帧 | 作兜底 |
| (b) 目标会话的 gofer hook 截获 SendMessage | 否 | 是（`tool_input.message`） | 全部（与传话人通道无关，hook 直接 HTTP 报 server） | **主通道** |
| (c) 在消息里教目标用 `gofer session say`/MCP 回复 | 是 | 是 | 全部 | 依赖模型听话、需要命令权限，不选 |

**主通道 (b)**：目标会话已经装了 `PostToolUse`（matcher `""`，`gofer init hooks` 缺省安装，用于进行中预览）。hook 看到 `tool_name=SendMessage` 时：

1. 取 `tool_input.to`（缺省 `recipient`）与字符串 `tool_input.message`；
2. 在本会话 transcript 尾部（4MB）找一条**来自该地址的 web 消息**：非 assistant 条目里 `origin.kind=peer`、`origin.from==to`、`origin.body` 以 `[来自 web` 开头（顶层或 `attachment.origin`；格式变了时退回"同一行同时出现 `from=\"<to>\"` 与 `[来自 web`"）；
3. 找到就 `POST /v1/sessions/{sid}/replies {text, to}`（鉴权同心跳：调用方必须是会话 owner）。

找不到（回给别的会话、普通的会话间协作）就什么都不做。只截获 SendMessage 成功后的 `PostToolUse`。

**server**：回复作为一条 `direction=reply` 的记录写进该会话已有的 outbox（`sessions/<sid>.outbox.jsonl`），带 `source=session|messenger`、`peer`（发往的地址）、`reply_to`（该会话最近一条经传话人送达的 web 消息 id）。web 现有的 outbox 分页、推送、时间线合并全部复用。同一会话 10 分钟内同文的回复只记一条（两条通道都到时去重；hook 后到时把来源升级为 `session`）。

**web**：时间线里 `direction=reply` 的条目画成会话一侧的气泡，标「<会话名> · 回复」，正文按 Markdown 渲染。

**传话人（兜底 + 不再转述）**：

- 读协程识别不请自来的回合：`command_lifecycle started…completed` 之间的 `result`，以及没有请求在等时到达的 `result`，一律不进 `events`，不再错位；
- 这样的回合是"有会话回消息给传话人"：读传话人自己的 transcript（`~/.claude/projects/<编码 cwd>/<session_id>.jsonl`，init 帧给出 session_id 与 cwd）尾部最后一条 `origin.kind=peer` 的条目，得到原文与对方会话名；记入传话人投递历史（`op=reply`，Runners 抽屉可见，worker 随已有的 messenger_detail 上报，无需协议升级）；
- server 本机的常驻传话人再把原文按 `source=messenger` 交给 sessionrelay（按会话名匹配本机会话），与 hook 通道去重——目标的 hook 太旧 / 没装 PostToolUse 时 web 仍能看到原文；
- 传话 prompt 加一句"对方的回复会由 gofer 直接显示在 web；收到对方会话的消息时只回复‘已收到’，不要转述"，省掉转述的 token。

## 2. 不做

- 不改送达路径（仍是 SendMessage 传话；中继 turn / deliver 阶梯不变）。
- 不升级 worker 协议：worker 上的传话人兜底只记投递历史，不把原文送回 server（需要时另开 issue）。
- 不截获失败的 SendMessage（一次性传话人已退出时目标的回复本来就送不到；Claude Code 对失败的工具调用不触发 PostToolUse）。

## 3. 验收

- e2e 测试：web 发消息 → 假传话人送达 → 目标 hook 以真实形状的 transcript + PostToolUse(SendMessage) 运行 → `GET /v1/sessions/{sid}/outbox` 里出现 `direction=reply`、原文一致、`source=session`。
- 回归测试：常驻传话人收到不请自来的回合后，下一次投递拿到的是自己的结果（修复前失败）。
- 真机：容器内两个真实 Claude 会话（一个装 gofer hook），web 发一条，目标用 SendMessage 回复，web 对话里出现原文气泡，传话 job 输出仍是"已发送"。
