# 终端 Agent 会话 ↔ Web 消息中继（Session Relay）设计

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-09-06 | inhere + claude | 初稿：Stop hook 阻塞中继 + server 侧会话注册/开关 + `gofer hook` 内置执行体 + `gofer init hooks` 一键装配；Claude Code 与 Codex CLI 双支持 |
| v0.2 | 2026-09-06 | claude | **T1-T6 已落地**（jobstore `agent_sessions` + decisions additive 列 / `internal/sessionrelay` / `/v1/sessions/*` 9 端点 / `internal/hookrelay` 执行体 + `hooks/` 嵌入模板 + JSON 合并安装 / `gofer hook`·`gofer session`·`gofer init hooks` / web Sessions 分组+抽屉+铃铛来源 / skill+runbook）。容器内 **Claude Code 真机 e2e PASS**（`claude -p`：停→CLI 作答→续跑→`/off` 放行，见 runbook §5）。落地偏差：`SetRelay(off)` 先置 idle 再过期 turn 再落开关（并发观察一致性）；`WaitTurn` 观察到 answered 时也把会话置 running；`init hooks` 用 `--output <dir>` 指定项目目录。TBD-1/2/4 见 §11 更新；Codex 真机待主机验证 |

> 关联：[`2026-07-18-session-handoff-and-pty-ux-design.md`](2026-07-18-session-handoff-and-pty-ux-design.md) Part A §11（hooks 通知中心，T7 未实施）与 Part C（决策通道，已落地 `tools-frx`）。本设计**取代 §11 的"裸终端不可远程作答"边界结论**，并吸收 §5 `adopted_sessions` 为统一的会话注册表。

---

## 1. 背景与目标

人在终端里跑一个 Claude Code / Codex 会话，任务进行到一半需要离开电脑。会话随后停下来等人处理（回合结束、抛出问题、等待确认），人不在，任务就此中断。目标：

1. 会话停下时，**最后一条需要人看的消息出现在 gofer web**（含手机浏览器）；
2. 人在 web 输入回复，**回复进入同一个终端会话**继续执行；不开 pty、不重开会话；
3. 人在终端时中继不干扰正常使用；
4. 忘了开开关也能在 web 找到活跃会话补开；
5. 一切随 gofer 二进制发布，`gofer init hooks` 一条命令装到 `.claude/` / `.codex/`，不用手写脚本。

### 1.1 关键机制（已核实）

| 能力 | Claude Code 2.1.x | Codex CLI（hooks 特性） |
|---|---|---|
| Stop hook 阻止停止并继续 | 输出 `{"decision":"block","reason":"…"}`，`reason` 成为下一条指令 | 同样的 legacy 形态 `{"decision":"block","reason":"…"}`，文档明说"不是拒绝，而是让 Codex 继续"；也可 exit 2 + stderr |
| hook 超时 | 默认 10 min，按 hook `timeout` 秒可调 | 默认 600 s，`timeout` 可调（SessionEnd 上限 3 s） |
| 连续 block 上限 | 默认 8，`CLAUDE_CODE_STOP_HOOK_BLOCK_CAP` 可调 | `stop_hook_active` 标识，上限待实测 |
| stdin 公共字段 | `session_id` `transcript_path` `cwd` `hook_event_name` `stop_hook_active` | 同名字段 + `turn_id`；Stop 还直接给 `last_assistant_message` |
| 最后一条消息来源 | 需解析 `transcript_path`（内部格式，可能变） | 直接字段，无需解析 |
| 配置位置 | `.claude/settings.json` → `hooks.<Event>[]` | `.codex/hooks.json` 或 `config.toml [hooks]`；需 `[features] hooks = true`（旧名 `codex_hooks`），项目层需 trusted |
| 可用事件 | SessionStart / UserPromptSubmit / Stop / SessionEnd / Notification(idle_prompt, permission_prompt) | SessionStart / UserPromptSubmit / Stop / SessionEnd / Interrupt（无 Notification） |

结论：两家的 Stop hook 语义一致，**一个执行体、两份装配模板**即可覆盖。

### 1.2 硬边界（诚实说明）

- 会话**已停在空闲提示符**（没有任何 hook 进程活着）时，除向 tty 注入按键外，没有办法把消息送进去。中继只能在"下一次 Stop"生效。
- 因此 web 拨开关的生效时机 = 下一次回合结束。会话正在跑长工具时拨开，任务结束即接上（最常见的"忘开"场景，覆盖）；会话已空闲则需终端输入一次或走 §9 的 tmux 兜底。

## 2. 名词

| 名词 | 含义 |
|---|---|
| agent 会话 | 终端里直接运行的 Claude Code / Codex 会话，以其 CLI 的 `session_id` 标识 |
| relay 开关 | 会话级布尔状态，on 时 Stop hook 阻塞等待 web 回复 |
| turn | 一次中继回合：hook 上抛的最后一条 assistant 消息（out）+ 人的回复（in） |
| 执行体 | hook 配置里被调用的命令，即 `gofer hook <agent>` |

## 3. 总体架构

```txt
终端 (任意 runner 机器/容器)                    gofer serve (hub)
┌──────────────────────────────┐              ┌────────────────────────────────┐
│ Claude Code / Codex 会话      │              │ agent_sessions  会话注册表+开关 │
│  hooks → `gofer hook claude` │──register──▶ │ plan_decisions  turn(复用决策)  │
│          `gofer hook codex`  │──heartbeat─▶ │ web: Sessions 页 / 铃铛 / 卡片   │
│                              │──turn(out)─▶ │ CLI: gofer session ...          │
│  Stop hook 阻塞 ◀── 长轮询 ───│◀──answer─────│◀── 人在 web / 手机 作答、拨开关  │
│  → block+reason 继续会话      │              └────────────────────────────────┘
└──────────────────────────────┘
```

执行体与 server 的连接复用 `gofer job` 的方式：`$GOFER_CONFIG_DIR/.env` 自动加载 `GOFER_SERVER_ADDR/TOKEN`，走 `internal/client`。hook 在哪台机器跑，会话就登记为哪台 runner（`GOFER_RUN_MODE`/worker id 自报，缺省 `server`）。

## 4. 决策

| # | 决策 | 理由 |
|---|---|---|
| D1 | **执行体是 gofer 二进制本身**：`gofer hook claude|codex` 读 stdin JSON、按事件分发、输出决策 JSON。不发布 bash 脚本 | 零外部依赖（无 bash/jq，Windows 主机可用），随 gofer 升级，`init` 只需写配置；符合 G021（commands 入口只做绑定，逻辑放 `internal/hookrelay`） |
| D2 | **relay 开关存 server、按会话**；hook 每次 Stop 先查开关，off 直接退出；server 不可达直接退出 | web 可补开（问题 4）；终端体验零干扰；中继绝不能卡死终端 |
| D3 | **turn 复用 `plan_decisions`**（additive 加 `session_id`、`kind` 列），不新建消息表 | 铃铛/InteractionCard/自由文本作答/懒过期/`gofer plan answer` 全部现成；web 只需加来源标签与跳转 |
| D4 | 新表 `agent_sessions` 作统一会话注册表，字段吸收 handoff 设计 §5 的 `adopted_sessions` | 一张表同时服务中继与后续的 adopt/resume，避免两套会话记录 |
| D5 | Claude 与 Codex 共用一个执行体，差异在 payload 归一化层 | Codex 直接给 `last_assistant_message`；Claude 解析 transcript，失败降级为固定提示，不阻断 |
| D6 | UserPromptSubmit 自动 relay off（可配置 `relay.auto_off_on_prompt`，默认 true）；**注入回复触发的 UserPromptSubmit 除外**（hook 按 `[gofer web 回复]` 前缀识别并上报 `injected`） | 人在终端敲了字就是回来了；Claude Code 会为 Stop-hook 续跑再触发一次 UserPromptSubmit，靠前缀区分 |
| D7 | 回复文本带前缀 `[gofer web 回复]` 注入 | 让模型知道来源与终端输入等价；便于 transcript 审计 |
| D8 | `SessionStart` 只登记不开开关；开开关是人的动作（终端 CLI / web / 回复框指令） | 与 handoff 设计"领养是离场动作"一致，避免每个会话都进入中继 |
| D9 | 第一版不做 tmux 按键注入 | 硬边界场景占比待观察；见 §9 |

## 5. 数据模型（serve sqlite，随 jobstore）

```sql
agent_sessions (
  session_id    TEXT PRIMARY KEY,   -- agent CLI 会话 id（claude uuid / codex id）
  agent         TEXT NOT NULL,      -- claude | codex
  project_key   TEXT,               -- 可空：hook 按 cwd 反查 project，查不到留空
  runner        TEXT NOT NULL,      -- 登记方所在 runner（server | <worker-id>）
  cwd           TEXT,               -- 绝对 cwd（登记方视角）
  title         TEXT,               -- <cwd 末级目录>: <首条用户提示前 40 字>
  transcript    TEXT,               -- transcript_path（备用：resume / 观察）
  tmux_pane     TEXT,               -- 可空，§9 兜底用
  state         TEXT NOT NULL,      -- running | idle | waiting_reply | needs_attention | ended
  relay         INTEGER NOT NULL DEFAULT 0,
  turn_no       INTEGER NOT NULL DEFAULT 0,
  last_message  TEXT,               -- 最近一次 Stop 的 assistant 文本（截 4KB）
  last_event    TEXT,               -- 最近 hook 事件名
  last_seen_at  INTEGER NOT NULL,
  started_at    INTEGER NOT NULL,
  ended_at      INTEGER
);
-- plan_decisions additive：
ALTER TABLE plan_decisions ADD COLUMN session_id TEXT;   -- relay turn 归属会话
ALTER TABLE plan_decisions ADD COLUMN kind TEXT;         -- NULL/ask | relay
CREATE INDEX idx_decisions_session ON plan_decisions(session_id, asked_at);
```

状态机：

```txt
running ──Stop(relay off)──▶ idle ──UserPromptSubmit──▶ running
running ──Stop(relay on)───▶ waiting_reply ──answer──▶ running
running ──Notification(permission/idle)──▶ needs_attention（Claude 独有，仅展示）
* ──SessionEnd──▶ ended        * ──last_seen 超 `relay.stale_after`(默认 30m)──▶ 列表标灰(不改状态)
```

## 6. 接口

### 6.1 serve API（`/v1`，既有 token 鉴权）

| API | 说明 |
|---|---|
| `POST /v1/sessions` | upsert 登记（SessionStart / 首次任何事件） |
| `POST /v1/sessions/{sid}/heartbeat` | `{event, state?, last_message?, title?}`；返回当前 `relay`（Stop 用它一次拿到开关） |
| `GET /v1/sessions?project=&state=&agent=` | 列表 |
| `GET /v1/sessions/{sid}` | 详情 + 最近 N 个 turn |
| `POST /v1/sessions/{sid}/relay` | `{relay: true|false}` |
| `POST /v1/sessions/{sid}/turns` | hook 上抛 out：server 创建 `plan_decisions(kind=relay, session_id, question=last_message, options=NULL, timeout=hook 超时-60s)`，置 `waiting_reply`，返回 decision id |
| `GET /v1/sessions/{sid}/turns/{id}?wait=25` | 长轮询：answered / expired / relay 被关 三者之一即返回；否则最多挂 `wait` 秒 |
| `POST /v1/decisions/{id}/answer` | **复用**既有作答端点（web/CLI 已有） |
| `DELETE /v1/sessions/{sid}` | 移除登记 |

作答成功时 server 顺带置 `state=running`；`relay` 被关时正在等待的 turn 置 EXPIRED（answer 留空），hook 立即退出。

### 6.2 CLI

```bash
gofer hook claude|codex                  # 执行体（供 hooks 配置调用，人不直接用）
gofer init hooks [--agent claude|codex|all] [--global] [--force] [--remove]
gofer session ls [-p <project>] [--state waiting_reply]
gofer session show <sid>
gofer session relay on|off [--session <sid>]   # 省略 --session：按 cwd 反查当前活跃会话，多个则列出让选
gofer session say <sid> "<回复>"        # web 输入框的 CLI 等价（= plan answer 最近 OPEN turn）
gofer session rm <sid>
```

`gofer session relay` 不带 `--session` 的解析规则：server 按 `cwd` 前缀 + `state != ended` + `last_seen` 最近 取一个；歧义时列出候选并要求 `--session`。SessionStart 时执行体也会把 sid 写到 `<config-dir>/run/sessions/<cwd-hash>` 作本地兜底。

### 6.3 `gofer hook` 事件映射

| 事件 | Claude | Codex | 执行体动作 | 阻塞 |
|---|---|---|---|---|
| SessionStart | ✓ | ✓(`source`) | upsert 登记；写本地 sid 兜底文件 | 否 |
| UserPromptSubmit | ✓ | ✓(`prompt`) | 首次补 title；heartbeat state=running；按 D6 自动 relay off | 否 |
| Stop | ✓ | ✓ | §7 主流程 | relay on 时 |
| SessionEnd | ✓ | ✓(≤3 s) | state=ended | 否 |
| Notification | ✓ | ✗ | `idle_prompt`→idle 提示；`permission_prompt`→needs_attention | 否 |
| Interrupt | ✗ | ✓(≤3 s) | heartbeat state=idle | 否 |

未知事件一律 exit 0 且不输出，保证升级 CLI 新增事件时无害。

### 6.4 `gofer init hooks` 装配行为

- Claude：读取目标 `settings.json`（项目 `.claude/settings.json`，`--global` 为 `~/.claude/settings.json`），**合并**写入以下条目；识别自有条目的依据是 `command` 以 `gofer hook` 开头，重复执行幂等，`--remove` 只删自有条目，其余用户 hook 原样保留；同时在 `env` 合并 `CLAUDE_CODE_STOP_HOOK_BLOCK_CAP=500`。

```json
{
  "hooks": {
    "SessionStart":     [{ "matcher": "", "hooks": [{ "type": "command", "command": "gofer hook claude", "timeout": 10 }] }],
    "UserPromptSubmit": [{ "matcher": "", "hooks": [{ "type": "command", "command": "gofer hook claude", "timeout": 5 }] }],
    "Stop":             [{ "matcher": "", "hooks": [{ "type": "command", "command": "gofer hook claude", "timeout": 7200 }] }],
    "SessionEnd":       [{ "matcher": "", "hooks": [{ "type": "command", "command": "gofer hook claude", "timeout": 5 }] }],
    "Notification":     [{ "matcher": "idle_prompt|permission_prompt", "hooks": [{ "type": "command", "command": "gofer hook claude", "timeout": 5 }] }]
  }
}
```

- Codex：合并写 `.codex/hooks.json`（`--global` 为 `~/.codex/hooks.json`），事件 SessionStart / UserPromptSubmit / Stop / SessionEnd / Interrupt，命令 `gofer hook codex`；完成后打印两条提示：`config.toml` 需 `[features] hooks = true`（旧版本键名 `codex_hooks`），项目层 `.codex/` 需在 Codex 里 trust 过。
- 模板嵌入方式沿用 `skills/embed.go` 的做法：`hooks/` 目录下放 `claude.settings.json` / `codex.hooks.json` 两份模板，`//go:embed`，init 只做 JSON 合并，不做字符串拼接。
- `gofer init skill` 顺带更新 gofer-usage skill（§8.3）。

## 7. Stop 主流程（执行体内部）

```txt
1. 解析 stdin：sid, cwd, transcript_path, stop_hook_active, last_assistant_message(codex)
2. LAST = codex ? last_assistant_message : parseTranscriptLast(transcript_path)
   - 解析失败 → LAST = "(无法读取最后一条消息，请查看终端)"，继续
3. resp = POST /sessions/{sid}/heartbeat {event:Stop, state:idle, last_message:LAST}
   - 失败(网络/401) → exit 0，不输出          ← 永不阻塞终端
4. if !resp.relay → exit 0
5. turn = POST /sessions/{sid}/turns {body:LAST}   → server 置 waiting_reply、推铃铛
6. deadline = now + hookTimeout - 60s（hookTimeout 由 init 写入，执行体经 flag `--budget` 或 env 获知，缺省 540s）
   loop until deadline:
     r = GET /sessions/{sid}/turns/{turn.id}?wait=25
     switch r.state:
       answered:
         body = r.answer
         if body == "/off":  POST relay=false; exit 0          ← 放行，会话正常停下
         print {"decision":"block","reason":"[gofer web 回复] "+body}; exit 0
       expired | relay_off: exit 0
       open: continue
7. deadline 到 → heartbeat note="relay 等待超时"; exit 0
```

约束：

- 一轮 Stop 只注入一条回复；人连发多条时后续的留在下一轮（长轮询首轮即命中）。
- `stop_hook_active=true` 不特殊处理（连续 block 正是所需），靠开关与 block cap 兜底。
- 输出只在 stdout 打一行 JSON；日志写 `<config-dir>/run/hook.log`（按大小轮转），不写 stderr（Claude 会把 stderr 显示给用户）。
- 执行体单次运行时长受 hook `timeout` 硬约束，超时被宿主杀掉等价于 exit 0，turn 由 server 懒过期。

## 8. Web 与 skill

### 8.1 Web

- `Sessions` 页新增「Agent 会话」分组（现有 pty 会话保持）：列 `title / agent / project / runner / state 徽章 / relay 开关 / last_seen`，`waiting_reply` 置顶。
- 会话详情抽屉：头部一行摘要（sid/agent/project/runner/turn/last_seen），点击展开完整元数据（默认收起，状态存 localStorage，把纵向空间留给消息）；turn 时间线（out 灰、**markdown 渲染**（marked + DOMPurify.sanitize，长消息按高度裁剪而非截断源码） / in 蓝，含时间与 answered_by）、底部输入框（提交即 `POST /decisions/{id}/answer` 到最近 OPEN turn；无 OPEN turn 时禁用并提示"会话未在等待"）、右上 relay 开关、元数据（cwd / transcript / sid 复制）。
- `EscalationBell`：`kind=relay` 的 OPEN decision 以来源标签「会话」展示，点击跳会话抽屉而非 PlanDetail；沿用决策通道 v0.2 已做的联合类型 `{source, …}` 扩一个 `source:'relay'`。
- 前端改动需 `make web` 重 embed。

### 8.2 安全

- 回复原样进入模型上下文，等同终端输入：能进 web 的人就能驱动会话。沿用既有 token 鉴权；多人环境后续加会话 owner（登记时的 caller）校验。
- `last_message` 可能含敏感内容，只对鉴权用户可见；`transcript` 只存路径不读内容。

### 8.3 skill（gofer-usage）新增段落

```txt
## 离开电脑：把会话交给 web 接着聊
- 一次性装配：`gofer init hooks`（Claude）/ `gofer init hooks --agent codex`
- 要走时：对 agent 说"打开中继"，它执行 `gofer session relay on`；之后每次停下来等你，消息都在 web「会话」里，直接回复即可
- 回来后：终端里任意输入一条即自动关闭；或 web 回复 `/off`；或 `gofer session relay off`
- 忘了开：web 会话列表找到它拨开开关，会话下一次停下时生效；已空闲的会话需终端输入一次
```

## 9. 兜底与后续

- **tmux 按键注入（可选，阶段 3）**：会话跑在 tmux 中时登记带 `tmux_pane`；web 对 idle 会话回复时，server 派一个 `exec` job 到该会话的 runner 执行 `tmux send-keys -t <pane> -l "<回复>"` + `Enter`。这是向现有终端敲字，不是重开会话，能补上 §1.2 的空白格。
- **PermissionRequest 远程放行**（handoff 设计 TBD-5）：同一执行体可加 `PermissionRequest` 事件，长轮询 web 决策；仍按原判断"等中继跑顺再评估"。
- **与 handoff Part A 的衔接**：`agent_sessions` 即 adopt 记录，后续 `session open`（web 起 `--resume` 接管）直接在这张表上做，接管前置的"不活跃判定"可直接读 `state`。

## 10. 实施拆分

| # | 内容 | 落点 |
|---|---|---|
| T1 | jobstore：`agent_sessions` 表 + decisions additive 两列 + 迁移 + 单测 | `internal/jobstore` |
| T2 | `internal/hookrelay`：payload 归一化（claude/codex）、transcript 解析、Stop 循环、日志；`internal/client` 加 sessions 方法 | 新包，不 import 入口层（G022） |
| T3 | httpapi：§6.1 端点 + turns 长轮询 + relay 关联过期；单测覆盖 answered / expired / relay_off 三分支 | `internal/httpapi/session_handler.go` |
| T4 | commands：`gofer hook`、`gofer session`、`gofer init hooks`（嵌入模板 + JSON 合并 + `--remove`） | `internal/commands`，`hooks/embed.go` |
| T5 | web：Sessions 分组 + 抽屉 + 铃铛来源 | `web/src` |
| T6 | skill 段落 + runbook；真机 e2e：Claude 与 Codex 各一轮（终端停 → web 答 → 终端续；`/off`；web 补开；server 停机不卡终端；连续 10 轮不触发 cap） | `skills/gofer-usage`，`docs/runbook` |
| T7 | （可选）tmux 兜底 | — |

验收基线：关着开关时 Stop hook 耗时 < 300 ms；server 不可达时 < 3 s 退出；开着开关 web 作答后终端 3 s 内继续。

## 11. 待确认

| # | 事项 | 状态 |
|---|---|---|
| TBD-1 | Claude Code hook `timeout` 设 7200 是否被接受 | e2e 中 Stop 条目 timeout 7200 被接受（长时等待上限仍待观察） |
| TBD-2 | Stop hook 等待期间用户按 Esc 的行为 | 未在交互 TUI 实测（e2e 为 `-p`）；hook 被杀等价 exit 0，turn 由 server 懒过期，不会误 block |
| TBD-3 | Codex 连续 block 上限 / `codex exec` 是否触发 hooks | 待主机 Codex 真机 |
| TBD-4 | Claude transcript assistant 文本路径 | Claude Code 2.1.263 实测为 `type=assistant` + `message.content[].text`；解析器同时接受 string content 与 `message.role`；失败降级为固定提示 |
| TBD-5 | 与其他工具共管 `.claude/settings.json` 的合并 | 已实现：只增删 `command` 以 `gofer hook` 开头的条目，其余原样保留（与 bd 的 SessionStart hook 共存已验证）；`json.Marshal` 会按 key 重排序，语义不变 |
| 新-1 | **harness 产生的 `UserPromptSubmit`**（主机真机 e2e 发现）：Stop-hook 续跑的注入回复、后台任务完成通知（`<task-notification>`）等都会触发该事件，D6 自动关中继会把中继关掉、下一轮直接放行 | 已修：执行体只把"像人敲的" prompt 当作回到键盘；空 prompt、以 `[gofer web 回复]` 开头、以 XML 标签开头或含 `<task-notification>`/`<system-reminder>` 的一律上报 `injected=true`，server 不自动关。主机真机 e2e 二次验证 PASS |
| 新-2 | 脚本化「会话启动即开开关」时序：SessionStart 后立刻 relay on，随后到达的首个 UserPromptSubmit 会把它关掉 | 真人使用中开关总在首个 prompt 之后打开，不受影响；脚本需等 title 出现后再开 |