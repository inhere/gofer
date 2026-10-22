# 会话压缩后重注入当前任务、终端投递不覆盖输入 设计

> issue: gofer-o53s（重注入）、gofer-k23r（安全投递）｜ 状态：0.2 已批准（2026-10-22，按推荐）｜ 2026-10-22
> 参考：WP `docs/refer/2026-10-22-openrig-analysis.md`（openrig 的 refocus 与 seat delivery）

## 一、会话压缩后重新注入当前任务

### 现状

- Claude Code / Codex 的 SessionStart 带 `source`（startup / resume / clear / compact）。gofer 登记的 SessionStart hook matcher 为空，压缩后也会运行 `gofer repo prime --hook-json`。
- prime 注入的是**仓库级**内容：提交策略、当前重点、规则、ready issue、记忆索引、交接。
- 缺的是**这个会话自己**正在做什么：压缩后 agent 只剩摘要，常常忘了自己认领的 issue、在监督的 plan、在等的 job、没回的信。

### 设计

D1 压缩时（`source=compact`，以及 `resume`），在 prime 之后追加一段「本会话当前任务」：

| 内容 | 来源 |
|---|---|
| 认领中的 issue（标题、验收标准、设计稿路径） | tracker：被本会话认领（claim 记录会话 id；没有会话 id 时按调用方 + 最近活动推断，推断的标「可能」） |
| 本会话监督的 plan（进度、下一项、阻塞） | plan 的 supervisor_session_id |
| 本会话在等的 job（id、标题、状态） | 会话的 job watch |
| 未回的信件（发件方、摘要） | 信箱：收件方是本会话、状态 delivered 且未回复 |
| 本会话的主题 | 会话 topic |

- 每项给出接手命令（`gofer issue brief <id>`、`gofer plan brief <id>`），不展开全文，总长受预算限制（复用 prime 的预算机制）。
- 没有任何当前任务时不输出这一段。
- 标签：以 `[gofer · 提示]` 开头，说明「上下文刚被压缩，以下是你压缩前在做的事」。
- `startup` / `clear` 不追加（新会话没有「之前」）。

D2 实现位置：`gofer repo prime` 增加 `--session <sid> --source <s>`（hook 已知 session_id 与 source），内容由 server 一次性返回（新接口 `GET /v1/sessions/{sid}/focus`，按 G056 登记，权限同读本会话），prime 拼接。server 不可达时跳过这一段，不影响 prime 其余部分。

## 二、终端投递不覆盖人正在输入的内容

### 现状（`internal/sessionrelay/deliver.go` 的 `injectScript`）

- 先检查 tmux pane 的前台程序是否在允许名单里，然后**逐行** `send-keys -l <行>` + `send-keys Enter`。
- 问题 1：**多行消息被拆成多次提交**——每行都回车，Claude Code 里每一行是一次单独的输入。
- 问题 2：**不检查输入框**——人正在终端里打一半，投递的文字会接在后面一起提交（openrig #48 同类事故），或在 AskUserQuestion 选项界面里按下回车替人选择（openrig #498）。

### 设计

D3 整段粘贴：改为 `tmux load-buffer -` 读入整段文字，`paste-buffer -p`（bracketed paste，TUI 把它当成一次粘贴，换行保留在同一条输入里），再发**一次** Enter。删除逐行回车。

D4 投递前检查输入区（尽力而为）：用 `tmux capture-pane -p` 取 pane 最后几行，按 agent 识别：

- 输入框里有内容 → 不投递，消息留待（D5）；
- 处于选择界面（AskUserQuestion / 权限确认等，按已知界面特征识别）→ 不投递；
- 判断不了 → 按会话的投递模式处理（D5）。

识别规则按 agent 放在配置里（`agents.<k>.inject_guard`，内置 claude / codex 的默认规则），识别失败一律视为「不确定」，不硬判定为空。

D5 会话的投递模式（新增，按会话设置，web 会话页可切换）：

| 模式 | 行为 |
|---|---|
| `auto`（默认） | 输入区确认为空才投递；不确定或有内容时改为留待 |
| `hold` | 从不往终端打字，消息一律留待 |
| `force` | 保持旧行为：不检查，直接粘贴（给明确知道终端无人使用的场景） |

- **留待**：消息不丢，等会话的下一个 UserPromptSubmit / Stop hook 注入（现有 hook 注入路径本来就有），或者人在 web 上点「现在送」。web 会话页显示「有 N 条消息等待投递」。
- 每次留待、跳过都记事件（原因：输入框有内容 / 选择界面 / 不确定 / hold 模式）。

D6 不变：hook 注入路径（UserPromptSubmit / Stop 等）本来就不打字，不受影响；只改「往终端打字」这一条路径。

## 已定（2026-10-22 用户批准，按推荐）

1. 会话投递模式默认 `auto`：只在确认输入区为空时才打字，识别不了时留待。
2. 重注入覆盖 `compact` 与 `resume`。
3. claim 没记会话 id 时，按调用方 + 最近活动推断并标「可能」。
