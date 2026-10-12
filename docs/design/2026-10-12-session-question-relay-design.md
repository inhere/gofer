# 终端会话的提问转 web，web 消息标明发送方

- issue：gofer-02fu
- 状态：实施中（2026-10-12）

## 问题

1. 终端里的 Claude Code 调 AskUserQuestion（选择题）时，终端停在选择框上。人不在终端前时，web 看不到这个提问，也无法作答；会话一直卡着。
2. 从 web 发给会话的消息，经「传话人」（gofer 启动的 `claude -p`）用 Claude Code 的跨会话消息送达。传话人没有名字，Claude Code 按工作目录给它起名（如 `gofer-f1`），收件的 agent 看到的是「另一个会话发来的消息」，会误以为不是用户本人。

## 实测（Claude Code 2.1.296）

PreToolUse hook（matcher `AskUserQuestion`）输出

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow",
 "updatedInput":{"questions":[…原样…],"answers":{"<question 文本>":"<选中的 label>"}}}}
```

Claude Code 不弹选择框，直接记为「User answered Claude's questions: … → 蓝」，agent 拿到答案继续。hook 输入的 `tool_input.questions[]` 每项有 `question / header / options[{label, description}] / multiSelect`。`claude -p` 模式没有这个工具。

## 方案

### 提问转 web

复用终端权限请求转 web 的整条链路（`internal/hookrelay/permission.go`、`internal/sessionrelay/permission.go`）：

- **hook**：PreToolUse 遇到 `AskUserQuestion`（claude）时上报心跳（带键盘 idle），读 server 下发的 `permission_wait_reason`——与权限请求同一套「人不在终端前」判定（relay `on`，或 `auto` 下 idle / 距上次输入超过阈值）。
  - **人在**：不开卡，只把会话标成需要关注、消息「等你回答：<问题>」，立即返回，终端照常弹框。
  - **人不在**：开一张权限类卡片，detail 带 `questions`；长轮询等待；web 作答后输出上面的 `allow + updatedInput.answers`；超时、server 不可达、人回到终端（idle 布防时）一律不输出，交回终端弹框，并关掉卡片。
- **server**：权限卡的 detail 增加可选 `questions`；有 `questions` 的卡片只接受答案 `answers:<JSON 对象>`（每个问题都要有非空答案：选项 label、多选时逗号分隔的多个 label，或自由文本），卡片标题为「有问题要你回答」。其余（开卡条件、取代旧卡、终端处理后关卡、审计、会话状态）沿用权限卡。
- **web**：会话抽屉里的权限卡片检测到 `questions` 时渲染成问题表单：每题一组选项（单选 / 多选），外加「其他」自由填写；提交即作答。
- **安装**：Claude 的 hooks 模板在 PreToolUse 下新增一条 `matcher: AskUserQuestion`、`gofer hook claude --wait 7140`、`timeout: 7200`（与 PermissionRequest 一样长；Bash 那条仍是 5 秒）。安装是「去掉 gofer 条目再按模板重加」，升级后重跑 `gofer init hooks` 即得新条目。
- **首页与通知**：「今天」页的卡片标「等你回答」，只有一道单选题时每个选项是一个按钮，否则点开会话作答；通知铃标题为「会话在等你回答」。

### web 消息标明发送方

传话人启动时加 `--name <名字>`（Claude Code 2.x 的 `-n/--name`），名字为 `gofer-web`，收件方看到的发送方即为它；消息前缀保持「[来自 web，<操作者>]」，操作者仍在前缀里。

## 不做

- codex 等其他 agent 的提问转发（它们没有同类工具与 hook）。
- 人在终端前时也从 web 作答（hook 运行期间终端无法作答，二者只能择一）。
