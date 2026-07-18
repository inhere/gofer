# 会话交接到 Web（session handoff）+ PTY 多端与输入体验 设计

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-18 | inhere + agent | 初稿：外部会话领养 + 双向交接 + pty 三问题修复设计 |

> 关联 issue：tools-tsq（会话领养）、tools-it1（ESC）、tools-3xy（多端错乱）、tools-j0e（聊天输入框）。

---

# Part A · 外部会话交接到 Web（tools-tsq）

## 1. 背景

操作者在终端里进行一个 Claude Code 会话（长任务进行中），需要离开电脑，希望到 gofer web 控制台（含手机浏览器）**继续同一个会话**；回到电脑后还能**接回终端**继续。

现状拼图（已具备）：

- claude 会话文件化，`claude --resume <session-id>` 可续接同一会话（id 不变）；
- gofer agent 配置已有 `SessionResumeInteractive` 模板（交互 TUI 续接 argv）；
- web 已有 pty attach（浏览器里与交互 TUI 对话，WEB-03）；
- gofer MCP server 与 Claude Code skill 机制可做会话内入口。

**缺口**：`gofer job resume` 只认 gofer 自己跑出的 job；外部终端会话 gofer 不知道，无法从 web 侧发起续接。

## 2. 名词

| 名词 | 含义 |
|---|---|
| 外部会话 | 不经 gofer 启动的 agent CLI 会话（如终端里直接跑的 Claude Code） |
| 领养 adopt | 把外部会话的元数据登记进 gofer，使其可被 web/CLI 续接 |
| 交接 handoff | 终端 → web 的会话所有权切换；handback 反向 |
| resume job | 由领养记录发起的 interactive job（`claude --resume <sid>` 起 TUI，经 pty attach 使用） |

## 3. 总体思路（不写代码的人也能懂）

把"我在终端里聊的这个会话"当成一件可以**寄存**的东西：

1. 要走时说一声（`/handoff-web` 或让会话里的 agent 调 MCP 工具）——gofer 记下"这个项目有个 claude 会话 X 存放在 W 机器上"；
2. web 的 Sessions 页看到它，点"继续对话"——gofer 在**同一台机器**上用 `claude --resume X` 把会话拉起来，浏览器直接对话（手机也行）；
3. 回到电脑，在 web 里点"结束"（或让它退出），终端上 `claude --resume X` 又接回来了——**始终是同一个会话 X**，两边看到完整历史。

约束一句话：**同一时刻只有一端在写**。会话文件是单写者模型，交接的本义就是"这边停、那边接"。

## 4. 架构总览

```txt
终端 Claude Code 会话 (session X, 机器 W)
   │ /handoff-web skill  或  MCP tool adopt_session
   ▼
gofer serve (hub)
   ├─ POST /v1/sessions/adopt   → 落库 adopted_sessions
   ├─ web Sessions 页: 列出 adopted + "继续对话"按钮
   │      └→ Submit interactive job (SessionResumeInteractive 渲染
   │          `claude --resume X`), runner = 领养记录指定的 worker
   ▼
worker (必须 = 机器 W, 同一 $HOME/项目文件系统)
   └─ pty 起 claude TUI → serve ptyrelay → 浏览器 attach
```

执行位约束是硬的：resume 必须在**拥有该会话文件（`~/.claude`）与项目工作区**的机器上执行；领养记录必须携带目标 runner/worker，提交时不允许改道（复用 runner pin 语义）。

## 5. 数据模型

新表（serve 侧 sqlite，随 jobstore）：

```sql
adopted_sessions (
  id            TEXT PRIMARY KEY,   -- 领养记录 id
  agent         TEXT NOT NULL,      -- 'claude'（先只做 claude，字段留扩展）
  session_id    TEXT NOT NULL,      -- agent CLI 会话 id
  project_key   TEXT NOT NULL,
  runner        TEXT NOT NULL,      -- 目标 runner（含 local）
  cwd           TEXT,               -- 项目内相对 cwd
  title         TEXT,               -- 一句话描述（领养时带上，列表展示用）
  state         TEXT NOT NULL,      -- ADOPTED | WEB_ACTIVE | RELEASED
  active_job_id TEXT,               -- WEB_ACTIVE 时指向 resume job
  created_at    INTEGER, updated_at INTEGER
)
-- UNIQUE(agent, session_id)：同一会话重复 adopt 做 upsert（刷新 title/cwd/时间）
```

状态机：

```txt
ADOPTED ──继续对话(起 resume job)──▶ WEB_ACTIVE ──job 终态──▶ RELEASED
   ▲                                                            │
   └────────── 再次领养(upsert, 或直接再点继续) ◀───────────────┘
```

RELEASED ≠ 删除：记录保留，web 可再次"继续对话"，终端也可 `--resume` 接回（见 §7）。

## 6. 接口与入口

### 6.1 serve API（`/v1`，走既有 token 鉴权）

| API | 说明 |
|---|---|
| `POST /v1/sessions/adopt` | body: agent/session_id/project_key/runner/cwd/title；upsert，返回记录 |
| `GET /v1/sessions/adopted` | 列表（含 state/active_job_id） |
| `POST /v1/sessions/adopted/{id}/open` | 起 resume job（见 §7 防双开检查），返回 job_id |
| `DELETE /v1/sessions/adopted/{id}` | 移除登记（不动会话文件） |

### 6.2 CLI

```bash
gofer session adopt --agent claude --session-id <sid> -p <project> [--runner <r>] [--cwd .] [--title "..."]
gofer session list            # 列 adopted
gofer session open <id>       # 等价 web 的"继续对话"（终端场景一般不用）
```

### 6.3 会话内入口（两条，先做 ①）

① **skill `/handoff-web`**（人主动说"我要走了"，语义最贴）：

- 获取当前 session id：优先 env（若 Claude Code 暴露）；兜底取
  `~/.claude/projects/<cwd-slug>/` 下 mtime 最新的 `*.jsonl` 文件名（= session id）。**待验证两条途径的可靠性（TBD-1）**。
- 调 `gofer session adopt ...`（CLI 已在 PATH、token 自动加载）；
- 输出提示："已交接，web Sessions 页可继续；此终端请勿再输入。"

② **MCP tool `adopt_session`**（gofer MCP server 加一个 tool，供会话里的 agent 直接调）——参数同 CLI；实现即包一层 §6.1 API。

> 不做 SessionStart hook 自动登记：每个会话都注册太吵，且"领养"语义应是人的离场动作。

### 6.4 web

- Sessions 页新增 "Adopted" 分组：title / project / runner / state / 更新时间；
- 按钮：`继续对话`（→ open → 跳转 job 详情 pty attach）、`移除`；
- WEB_ACTIVE 的记录显示"进行中 → 打开终端"直接跳 active_job_id。

## 7. 关键流程

### 7.1 终端 → web（交接）

```txt
人: /handoff-web
skill: 取 sid → gofer session adopt(... runner=w-X) → 提示终端停手
人(手机/web): Sessions → 继续对话
serve: 检查 state≠WEB_ACTIVE 且无 RUNNING 的 active_job → Submit interactive job
       (agent=会话对应 tty agent, SessionResumeInteractive 渲染 --resume sid,
        runner=记录里的 w-X, 不可改道)
worker W: pty 起 claude --resume sid → 浏览器对话(继承完整上下文)
serve: state=WEB_ACTIVE, active_job_id=job
```

### 7.2 web → 终端（接回，回答"还能交接回来吗"：能）

```txt
人(web): 结束会话(job 详情里停止 job / TUI 内退出)
serve: job 终态 → state=RELEASED（jobstore 终态钩子里顺带更新）
人(终端): claude --resume <sid>          ← 同一会话 id，历史完整
         （或 gofer session list 找到 id 后照提示 resume）
```

要点：**session id 全程不变**，"接回"不需要 gofer 做任何事，只要 web 侧 job 已结束（单写者约束满足）即可。skill/文档要把"先结束 web 端再接回"讲清楚。

### 7.3 防双开（单写者保障，尽力而为）

- open 时若 state=WEB_ACTIVE 且 active_job 仍 RUNNING → 拒绝（提示先结束）；
- adopt 时无法可靠检测"终端端是否真的停手"——以提示与约定为主（TBD-2：后续可探测会话文件 mtime 活跃度做告警）；
- 终端接回前 web job 未结束 → claude 自身表现为两个进程写同一会话文件，后写覆盖——文档明示风险。

## 8. 待确认事项

| # | 事项 |
|---|---|
| TBD-1 | 会话内获取自身 session id 的可靠途径（env vs 最新 transcript 文件）需实测 |
| TBD-2 | 是否要做"终端端仍活跃"探测（会话 jsonl mtime 心跳）辅助防双开 |
| TBD-3 | resume job 的 agent 选择：领养记录带 agent key（如 tty-claude）还是 serve 按 agent 类型自动挑 interactive 定义？初稿：领养时显式带，缺省 tty-claude |
| TBD-4 | 手机端长文本体验依赖 Part B 的聊天输入框先落地 |

## 9. 实施拆分（建议顺序）

1. **T1** serve：adopted_sessions 表 + adopt/list/open/delete API + open 的防双开（含单测）
2. **T2** CLI：`gofer session adopt/list/open`
3. **T3** web：Sessions 页 Adopted 分组 + 按钮
4. **T4** skill `/handoff-web`（含 sid 获取验证）+ MCP tool `adopt_session`
5. **T5** e2e：容器内真实交接一轮（终端 → web → 终端），写 runbook

---

# Part B · PTY 三问题修复设计（tools-it1 / tools-3xy / tools-j0e）

## B1. ESC 按键不起作用（tools-it1）

**现状核查**：前端已有三层 ESC 处理（document capture / host capture / xterm custom handler 统一走 `consumeShortcut` → 发送 `\x1b`），且历史上已修过三轮（7062d2a/e578a0b/5e4e4ac）。关键事实：**live 前端从 07-14 起一直是旧构建，今天(07-18)才随部署更新**（tools-yz3）——用户遇到的 ESC 问题很可能来自旧构建。

**处置**：

1. 先在 live 实测（桌面物理 ESC + 手机"发送键"菜单的 Esc）；
2. 若仍复现：按下述假设排查并修——
   - H1 页面上层有更早注册的 document capture keydown（抽屉/弹层关闭）截胡：改为 AttachTerminal 挂载时以 `{capture:true}` 且**首位**注册（或在 JobDetail 层对 terminal-active 状态豁免 ESC）；
   - H2 焦点不在终端时 `isTerminalActive` 为假：点击终端区外操作按钮后 ESC 失效——`sendKeyAction` 已回焦，物理键路径补同样回焦；
   - H3 write 未授予时 `sendInput` 静默丢弃：给出"只读中，输入被忽略"的一次性提示（顺带解决所有按键"没反应"的困惑）。

## B2. 多端 attach 显示错乱（tools-3xy）——已定位根因

**根因（代码实锤，三处叠加）**：

| # | 位置 | 问题 |
|---|---|---|
| R1 | `attach_handler.go` hello 帧 | `cols/rows` 取 `entry.Binding.Cols/Rows` = **提交时的初始尺寸**，pty 后来被 resize 过则 hello 是错的——后进客户端一上来就按错误尺寸渲染 |
| R2 | serve 无 resize 广播 | pty 尺寸变化只有发起端知道；其他 viewer 的 xterm 停在旧尺寸 → TUI 按新宽度重绘、旧宽度视图必然错乱，且**无任何恢复手段**（正是"之后 web 端也错乱"） |
| R3 | `readAttachFrames` 的 `"r"` 分支 | **不校验写租约**，任何 viewer 发 resize 都会改 pty（前端虽有 writeGranted 门，服务端裸奔）；手机小屏一旦拿到/抢到写租约，`fit()` 立即把 pty 压成手机尺寸 |

另有前端一处：R4 只读端仍跑 FitAddon（手机弹出软键盘触发 window resize → fit → 本地 xterm 改小），本地视图与 pty 脱钩。

**修复设计（尺寸单一真源 = pty，serve 广播，客户端跟随）**：

1. **relay 记录当前尺寸**：`Relay` 增加 `cols/rows`（Resize 成功后更新，New 时用 Binding 初始值）+ `SizeListener` 回调（或由 attach handler 层维护，见实现取舍）；
2. **serve 广播 `{"t":"r","cols":..,"rows":..}`**（新增服务端→客户端控制帧）：任何成功的 `relay.Resize` 后推给**所有** viewer；hello 改为携带 relay 当前尺寸（修 R1）；
3. **服务端 resize 校验租约**（修 R3）：`"r"` 帧仅写租约 viewer 生效，非写者忽略（不断连，容忍旧前端）；
4. **前端跟随语义**（修 R4）：
   - 收到 `r` 帧 → `term.resize(cols, rows)`（写者忽略自己回声即可，简单起见全体执行，幂等）；
   - **只读端禁用 FitAddon 对 xterm 的重排**：window resize 时不 `fit()`，改为**自适应字号**——按容器宽度/pty cols 计算 fontSize（下限 8px），放不下时容器 `overflow-x:auto` 横向滚动；
   - 写者保持现状：fit() → onResize → 发 `r` → pty resize → 广播 → 其他端跟随；
   - 写租约易主（抢占重连）后新写者首次 fit 即触发一次全局同步。
5. **说明**：resize 后 TUI 经 SIGWINCH 自行重绘当前屏；scrollback 里旧宽度历史不清除（保留可读性，错乱只限历史区，当前屏正确）。

协议兼容：新增 `r` 服务端帧对旧前端是未知 text 帧——旧前端 `parseServerFrame` 未识别会忽略（需核对其实现容错，若 strict 则跟随本次前端一起发布，风险窗口可接受）。

## B3. 聊天式输入框（tools-j0e）

终端下方加一个可折叠输入区（默认展开，记忆折叠态）：

```txt
┌ 终端 (xterm) ────────────────────────────┐
│ ...                                      │
├──────────────────────────────────────────┤
│ [多行 textarea：长文本编辑/粘贴/检查]     ⏎│
│ 发送=写入 pty；Ctrl/Cmd+Enter 快捷发送     │
└──────────────────────────────────────────┘
```

- **发送语义**：文本经 bracketed paste 包裹写入（复用现有 `pasteText` 逻辑：`\x1b[200~…\x1b[201~`，换行归一为 `\r`），**随后单独补一个 `\r`** 提交——TUI（claude）把多行当一次输入而非逐行执行；
- 提供"仅置入不提交"开关（不补 `\r`，写进 TUI 输入区供再编辑）；
- 只读态禁用（复用 writeGranted）；发送后清空 textarea、焦点留在输入框（连续对话习惯）；
- 手机端主要输入面：软键盘对 textarea 友好（规避 xterm 直输的 IME/组合键问题），配合 B2 的字号自适应，手机可用性达标。

## B4. 实施与验收

| 项 | 改动面 | 验收 |
|---|---|---|
| B2 后端 | ptyrelay(尺寸+广播 seam)、attach_handler(hello/`r` 帧/租约校验) + 单测 | 双客户端单测：A 写 B 读，A resize → B 收到 `r` 帧；读者发 `r` 不生效；hello 尺寸=当前值 |
| B2 前端 | AttachTerminal.vue（r 帧、只读字号自适应） | 手机+桌面同开不再互相打坏；软键盘弹出不破坏视图 |
| B3 前端 | AttachTerminal.vue 输入区 | 长文本一次发送、TUI 单次收到并提交 |
| B1 | 视实测 | live 桌面 ESC / 手机菜单 Esc 生效 |

发布顺序：后端先（旧前端兼容 `r` 帧忽略），前端随后（同天可一起发）。
