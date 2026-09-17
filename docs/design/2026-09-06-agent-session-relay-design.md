# 终端 Agent 会话 ↔ Web 消息中继（Session Relay）设计

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-09-06 | inhere + claude | 初稿：Stop hook 阻塞中继 + server 侧会话注册/开关 + `gofer hook` 内置执行体 + `gofer init hooks` 一键装配；Claude Code 与 Codex CLI 双支持 |
| v0.2 | 2026-09-06 | claude | **T1-T6 已落地**（jobstore `agent_sessions` + decisions additive 列 / `internal/sessionrelay` / `/v1/sessions/*` 9 端点 / `internal/hookrelay` 执行体 + `hooks/` 嵌入模板 + JSON 合并安装 / `gofer hook`·`gofer session`·`gofer init hooks` / web Sessions 分组+抽屉+铃铛来源 / skill+runbook）。容器内 **Claude Code 真机 e2e PASS**（`claude -p`：停→CLI 作答→续跑→`/off` 放行，见 runbook §5）。落地偏差：`SetRelay(off)` 先置 idle 再过期 turn 再落开关（并发观察一致性）；`WaitTurn` 观察到 answered 时也把会话置 running；`init hooks` 用 `--output <dir>` 指定项目目录。TBD-1/2/4 见 §11 更新；Codex 真机待主机验证 |
| v0.3 | 2026-09-17 | inhere + claude | **R1/R2 落地**（bd h-aii-1hmo）：开关 bool → 三态 `relay_mode: auto\|on\|off`（旧 `relay=1→on` / `0→auto`；`relay` 列留作 `mode=='on'` 的镜像给旧二进制）；`auto` 由 server 判定：判据一 = 键盘空闲（`session.auto_relay_idle_sec`，旧 `server.session_auto_relay_idle_sec` 仅作别名 + warn 一次），判据二 = **距本会话上次人工输入**（`session.auto_relay_turn_sec`，新列 `last_human_at`）——专治"键盘在主机、hook 在容器里探不到"；判定结果以 `wait_reason`（mode_on / idle_probe / turn_age）回给 hook。判据二的等待无读数可探，人回来时靠 `UserPromptSubmit` / `Interrupt` 事件由 server 释放（`released_by=user_returned`），设计细节见 `../runbook/session-relay.md` |
| v0.4 | 2026-09-17 | inhere + claude | **阶段 2 定案**（bd h-aii-w934）：空闲会话（无 OPEN turn）收到 web 回复时的两条送话路径——**A. tmux 按键注入**（首选，续同一进程）与 **B. `--resume` pty 接管**（无 tmux 时兜底），见 §9.1；IM 双向作答继续不做 |
| v0.5 | 2026-09-17 | claude | 阶段 2 实施细化（任务书依据）：**hook 的 runner 登记**在 client 运行模式下不再假装 `server`（取 `GOFER_HOOK_RUNNER`/`--runner`，否则为空 → A/B 均不可用并明示原因；容器会话要可送话必须在容器内起 worker）；新增 `Deliver` 选路入口（`Say` 保持只答 turn），HTTP `POST /v1/sessions/{sid}/deliver {text, allow_takeover}`——B 只在 `allow_takeover=true` 时执行（web 二次确认）；B 用 pty job 新增的 `InitialInput`（TUI 首次输出后安静 1.5s 再写入）；`handed_off` 会话可 `release-takeover` 解除；A/B 分两个 job（P2-1/P2-2） |
| v0.6 | 2026-09-18 | claude | **P2-1 已落地**（A = tmux 注入）：`Deliver` + `Injector` 注入 seam + `POST /v1/sessions/{sid}/deliver` + `session say --deliver` + web「送入终端」+ 失败原因码（409/502/400）+ `plan_decisions.detail` 审计列 + client 模式 runner 登记修正；未做项与前置条件（exec 护栏）见 §9.1 的 P2-1 落地记录 |
| v0.7 | 2026-09-18 | claude | **P2-2 已落地**（B = `--resume` pty 接管）：`JobRequest.InitialInput`（pty 首次输出后安静 `session.takeover_input_delay_ms`，默认 1500ms，最多 10s → 写入 + `job.input_injected` 事件；worker 路径经 `wsproto.Dispatch.initial_input`，协议 v7）+ `Takeoverer` seam（`PlanTakeover`/`TakeoverSession`/`CancelTakeover`）+ `session state=handed_off`（新列 `handed_off_job_id/handed_off_at`）+ `POST /v1/sessions/{sid}/deliver {allow_takeover}` + `POST /v1/sessions/{sid}/release-takeover` + `session say --deliver --takeover` + heartbeat 响应 `notice`（hook 打 stderr）+ web 接管按钮/解除按钮；实测记录与限制见 §9.1 |

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
| `POST /v1/sessions/{sid}/say` | 答最新 OPEN turn（`Say`：只作答，`/off` 关中继） |
| `POST /v1/sessions/{sid}/deliver` | 选路送话（§9.1）：有 OPEN turn 就作答，否则 A（tmux 注入）；`{text, allow_takeover}` 且 allow_takeover=true 时才接 B（`--resume` 接管） |
| `POST /v1/sessions/{sid}/release-takeover` | 解除 B 的接管（§9.1 B）：先 cancel 接管 job，再置 `idle` 并清空 `handed_off_*` |
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

## 8.4 手机提醒（OBS-07a，已落地）

会话进入 `waiting_reply` 时经既有 webhook 队列推一条 IM 消息（钉钉/飞书群机器人），
消息带一条直达该会话抽屉的链接。事件名 `session.waiting`，**不在默认订阅集里**，
需在 webhook 的 `events` 显式订阅。实现要点见 [`../runbook/im-notification.md`](../runbook/im-notification.md)：
`OpenTurn` 经 `sessionrelay.Notifier` 接口回调（`job.Service` 注入，relay 不感知通知与配置），
投递复用 `event_deliveries` 的预渲染路径（`body`/`event_type` 两列 additive）。
另有 `session.attention`：会话进入 `needs_attention`（Claude 的权限确认等终端内对话框）时推送，
按内容去重（同一提示反复弹只推一次），且**只在中继开着时**推。它**不能**在 web 上作答——
那是 Claude Code 自己的对话框，不是 relay turn——通知的作用是叫人回终端。

IM 自定义机器人只能收不能发回，**回复仍在 web**；IM 内直接作答属 OBS-07c，未做。

## 9. 兜底与后续

### 9.1 阶段 2：向空闲会话送话（A tmux 注入 + B resume 接管）

阶段 1（Stop 阻塞 + 三态 + 空闲回退）只能在**回合结束那一刻**决定要不要等。盖不住的场景：短回合结束时人还在（未布防），随后人离开，几小时后想从 web 接着说——此时会话停在提示符，没有任何 hook 进程活着，回复无处注入。阶段 2 给"没有 OPEN turn 的会话"两条送话路径，web「会话」页对这类会话的输入框改为「发送到终端」并按可用性自动选路：

**A. tmux 按键注入（首选）**

- 前提：会话跑在 tmux 里。hook 已在 SessionStart/heartbeat 登记 `TMUX_PANE`（`agent_sessions.tmux_pane`）；容器/主机的 shell 入口建议默认 `tmux new -A -s claude` 起 Claude Code / Codex，让所有会话天然可注入。
- 流程：web/CLI `session say <id> "<回复>"` → server 发现该会话无 OPEN turn 且 `tmux_pane` 非空 → 向该会话所在 runner（`agent_sessions.runner`，容器会话即 `w-docker-claude`）派一个内部 exec job：`tmux send-keys -t <pane> -l -- "<回复>"` 再 `tmux send-keys -t <pane> Enter`（两次调用，`-l` 字面量防止按键名解释；回复先经 `[gofer web 回复]` 前缀，hook 的 UserPromptSubmit 据此识别为 injected，不改 relay_mode）。
- 校验与回执：注入前 `tmux display -p -t <pane> '#{pane_current_command}'` 确认 pane 仍存活且前台是 agent 进程（claude/codex/node）；否则回退 B。注入成功后会话状态置 `running`，web 显示"已送入终端 ✓"；失败给出原因（pane 不存在 / runner 离线）。
- 安全：只允许向**本 caller 自己登记的会话**送话；内容长度上限 8KB；多行回复按行 `send-keys -l` + `Enter`（Claude Code 支持粘贴多行）。审计进 `session_turns`（kind=inject_tmux）。

**B. `--resume` pty 接管（兜底）**

- 前提：会话无 tmux（典型：Windows 主机的 Windows Terminal）且 agent 有 `SessionResumeInteractive` 模板（内置 claude/codex/omp）、项目 `allow_interactive: true`。
- 流程：server 用该会话的 `session_id` 在同一 cwd 起一个 **交互 pty job**（`claude --resume <sid>` / `codex resume <sid>` / `omp --resume <sid>`），把回复作为首条输入写入 pty；web 跳转到该 job 的 attach 终端继续对话。原终端进程仍在，但会话已在另一进程延续——原终端人回来时，会话页与 hook（下一次 UserPromptSubmit）提示"该会话已在 web 接管于 <时间>，继续请在 web 或 `--resume`"。
- 限制：Claude Code 同一会话被两个进程写入会分叉，所以接管后标记原终端会话为 `handed_off`，其后续 Stop 不再开 turn。

**选路**：有 OPEN turn → 注入 turn（阶段 1）；否则 `tmux_pane` 有效 → A；否则满足 B 前提 → B（web 二次确认"将起新进程接管"）；都不行 → 明确提示"该会话无法远程送话：请在终端运行于 tmux，或开启项目 allow_interactive"。

**实施拆分**：P2-1 A（server 选路 + 内部 exec job + pane 校验 + 回执 + 审计 + web 输入框，`internal/sessionrelay`/`httpapi`/`web`）；P2-2 B（复用 pty job 与 attach，`handed_off` 状态与提示）；P2-3 skill/README/runbook（tmux 入口建议、两条路径的适用条件）。测试：A 用假 runner 断言派发的 argv 与 pane 校验；B 用现有 interactive e2e 基础设施。

**v0.5 细化（实施依据）**：

- **runner 登记**：hook 登记的 `runner` 决定 A/B 的内部 job 派到哪台机器。`resolveHookRunner` 在 server 模式登记 `server`、worker 模式登记 worker id；**client 模式**（容器里只做客户端）取 `GOFER_HOOK_RUNNER` / `gofer hook --runner`，都没有则登记空串，server 对 `runner=''` 的会话明确回"无法远程送话：会话未登记执行机"。因此容器会话要能被 web 送话，容器内必须起一个 gofer worker 并把 `GOFER_HOOK_RUNNER` 指向它；Windows 主机终端（无 tmux）的会话只能走 B。
- **入口分离**：`Say` 保持"只答 OPEN turn"的旧语义；新增 `Deliver(sid, text, by, allowTakeover)` 做选路 turn → A → B，HTTP `POST /v1/sessions/{sid}/deliver {text, allow_takeover}`，CLI `session say --deliver [--takeover]`。B 只在 `allow_takeover=true` 时执行（web 先二次确认），否则在 A 不可用处以 409 `no_tmux` 停下。失败原因码：`no_runner | no_tmux | ended | handed_off:<job> | inject_failed:<pane_missing|pane_busy:<cmd>|runner_error> | no_resume_template | interactive_not_allowed | cwd_outside_project`。
- **A 的注入 job**：一条 `sh -c` 脚本：`tmux display -p -t <pane> '#{pane_current_command}'` 存活与前台白名单（`claude|codex|omp|node|gemini|opencode`，可配 `session.inject_commands`）校验 → 逐行 `send-keys -t <pane> -l -- <行>` + `Enter`；文本带 `[gofer web 回复] ` 前缀、≤8KB、shell 单引号转义；hook 的 `UserPromptSubmit` 见该前缀即按 injected 处理（不翻 relay_mode、不更新 `last_human_at`）。
- **B 的首条输入**：pty job 新增内部字段 `InitialInput`（不入 request_json；worker 路径经 Dispatch 下发）：子进程首次输出后 `session.takeover_input_delay_ms`（默认 1500）内无新输出即写入，最多等 10s；记 `job.input_injected` 事件。会话置 `handed_off`（新列 `handed_off_job_id/handed_off_at`），`OpenTurn` 对其拒绝，hook 通过 heartbeat 响应的 `notice` 在原终端打一行提示；`POST /v1/sessions/{sid}/release-takeover` 解除（接管 job 在跑则先 cancel）。
- **cwd 换算**：B 需要把会话的绝对 cwd 换算成项目相对路径：runner=server 用 `cfg.ExecPath(proj)` 前缀；worker runner 用 server 侧 `host_path` 前缀（POLICY roots 映射后本机路径不同者换算失败 → `cwd_outside_project`，已知限制）。

**P2-1 落地记录（2026-09-18，A = tmux 注入）**

- 落点：`sessionrelay.Deliver(ctx, sid, text, by)` 做选路（`Say` 仍是"只答 OPEN turn"）；注入 job 经 `sessionrelay.Injector` 接口注入（`httpapi.sessionInjector` 适配 `job.Service.SubmitSync`，relay 不 import job —— G022）。HTTP `POST /v1/sessions/{sid}/deliver {text}` → `{path, job_id?, decision_id?}`；`gofer session say --deliver`；web 抽屉对无 OPEN turn 的会话把输入框切成「送入终端」。
- 注入脚本：一条 `sh -c`（Windows 执行机上没有 tmux ⇒ 直接失败，报 `runner_error`）。先 `tmux display -p -t '<pane>' '#{pane_current_command}'` 判存活（exit 3 / stdout `pane_missing`）与前台白名单（默认 `claude|codex|omp|node|gemini|opencode`，`session.inject_commands` 可配，exit 4 / stdout `pane_busy:<cmd>`），再逐行 `send-keys -t '<pane>' -l -- '<行>'` + `Enter`；pane 与文本都是单引号 shell 词（`'` → `'\''`），`$`/反引号/换行都不执行。文本 = `[gofer web 回复] ` + 回复，上限 8KB（超出 400）。
- 选路/失败码实测：`no_runner`（runner 登记为空或服务端没接 job service）、`no_tmux`（无 pane）、`ended` → HTTP 409；`inject_failed:<pane_missing|pane_busy:<cmd>|runner_error>` → 502；超长/空文本 → 400（`ErrInvalidInput`）。失败时**不改会话状态、不写审计行**。原因码放在错误信封的 `error` 字段（`deliver failed: no_tmux`），web 读作 `ApiError.code` —— 不新增 wire 字段就有机器可读的原因，客户端不必解析 detail 文案。
- 成功回执：会话 `state=running`；审计行 `plan_decisions(kind='relay', state=answered, answered_by=by, answer=文本)`，`question` 写明"会话未在等待、已送入终端"（web 时间线渲染成一条无 agent 消息的 turn），`detail` 存 `{"path":"tmux","job_id":"<注入 job>"}`。为此给 `plan_decisions` 加了一列 `detail`（additive 迁移，沿用 `released_by` 的做法）：这是"最省事且可查询"的落法 —— 塞进 `question` 会把审计信息混进对话语义，加列则 SQL/接口都能直接查，且 `task` 查询无需 LIKE 文案。
- **client 模式 runner 登记修正**：`resolveHookRunner` 在 `GOFER_RUN_MODE=client` 下不再登记 `server`，改取 `--runner`/`GOFER_HOOK_RUNNER`，都没有则登记空串。于是"容器里跑的会话"要么在容器内起 worker 并把 `GOFER_HOOK_RUNNER` 指向它（A 可用），要么登记为空并在 web 收到明确原因（A/B 都不可用）。`gofer init hooks` 生成的命令不变。
- **hook 侧**：`SessionStart`/heartbeat 早已登记 `TMUX_PANE`；`UserPromptSubmit` 的 `[gofer web 回复]` 前缀判定本来就不依赖是否存在 OPEN turn，故 tmux 注入的提示词天然按 injected 处理（不翻 relay_mode、不更新 `last_human_at`）。本次只补测试固化（`TestInjectedPromptWithoutTurnIsNotHuman`）+ 一条双包常量一致性测试（`sessionrelay.InjectPrefix` == `hookrelay.ReplyPrefix`）。
- 等待预算：注入 job 的 deadline 是 30s，但 HTTP 侧的 sync 等待只到 **25s**（`injectWaitSec`，低于 CLI 客户端 30s 的 HTTP 超时、也低于 job 自己的 deadline），超时按 `inject_failed:runner_error` 返回并带 job id —— job 自己会跑完，`gofer job show` 是真相，避免"客户端先超时、服务端后返回"的假失败。
- **已知前置条件（未在本阶段解决）**：注入 job 走 `exec` agent，因此受项目 `allow_exec` 与 worker `guards.allow_exec` 约束；项目没开 `allow_exec` 时会以 `inject_failed:runner_error`（"exec agent … not allowed"）失败。这是既有安全护栏，未为本路径开例外；需要的项目请显式 `allow_exec: true`。同类的还有：`project_key` 为空 / 本项目不认的会话，注入 job 提交即被拒，同样报 `inject_failed:runner_error`（词表里没有"没项目"这一档，直接让 job 提交的错误原话讲清楚比硬套一个码好）。
- **未做/待办**（P2-2 落地后更新）：`allow_takeover` 字段与 `handed_off` 的拒绝分支已随 P2-2 落地（见下）；`Detail` 目前只在响应里透出，web 未单独渲染；真机 tmux e2e（容器内 worker + tmux 会话 → web 送入终端 → agent 继续）待有 tmux 的环境验证，本次验证是单测级的（argv/转义/选路/状态/审计/HTTP 码）。

**P2-2 落地记录（2026-09-18，B = `--resume` pty 接管）**

- 落点：`sessionrelay.Deliver(ctx, sid, text, by, allowTakeover)` 在 A 之后接 B，**只在 A 报 `no_tmux` 或 `inject_failed:pane_missing`**（pane 注册为空 / pane 已消失）且 `allowTakeover=true` 时执行；其余失败码（`ended`、`handed_off:<job>`、`no_runner`、`inject_failed:pane_busy`、`runner_error`）直接原样返回 —— B 对它们同样无解，报真实原因比再试一次有用。B 的规划与派发经新的 `sessionrelay.Takeoverer` seam（`PlanTakeover` / `TakeoverSession` / `CancelTakeover`），宿主实现仍是 `httpapi.sessionInjector`（A/B 同一个适配器、同一个 `job.Service`，relay 不 import job/config/agent —— G022）。`PlanTakeover` 由宿主回答三件事：交互 resume argv（`[AgentConfig.Command] + Render(SessionResumeInteractive,{session_id})`，agent 无该模板则为空）、项目 `allow_interactive`、以及**该 runner 视角的项目根**（server runner 用 `cfg.ExecPath(proj)`，worker runner 用 server 侧 `host_path`）。
- 前提校验与原因码（**都在派发前**，`ErrUndeliverable` envelope）：`no_runner`（runner 空 / 未接 job service）、`no_resume_template`（agent 非 cli-agent 或无交互 resume 模板）、`interactive_not_allowed`（项目未开 `allow_interactive`）、`cwd_outside_project`（会话 cwd 无法用上述项目根前缀换算成相对路径）、`ended`、`handed_off:<job>`。派发本身失败（提交被拒 / runner 不在）沿用 `inject_failed:runner_error`（HTTP 502）—— 词表里没有第二档"派发失败"，新增码会让客户端多一个分支而语义相同。
- 接管 job：`Submit(JobRequest{Agent: exec, Cmd: <argv>, Runner: <会话登记的执行机>, Cwd: <相对>, Interactive: true, Cols/Rows: 120x40, TimeoutSec: 3600, SessionID: sid, ResumeSourceAgent: <会话的 agent>, InitialInput: "[gofer web 回复] " + 文本 + "\r", CallerID: by, Tags: [relay-takeover]})`。`ResumeSourceAgent` 让 exec 载体按**源 agent** 过访问门（与 job resume 同一豁免）；`TimeoutSec`/`Cols`/`Rows` 仍受项目/服务器上限收紧。job 标签 `relay-takeover`（`gofer job ls --tag relay-takeover`）。
- **首条输入**：`JobRequest.InitialInput`（`json:"-"`，不入 `request_json`）→ `runner.Request.InitialInput/InitialInputQuietMs` → pty runner 在子进程**首次输出**后等 `session.takeover_input_delay_ms`（默认 1500ms；`EffectiveSessionTakeoverInputDelayMs`）内无新输出再 `WriteInput`，最多等 10s（`defaultInitialInputMaxWait`，测试可注入）。安静判定**挂在 `PtySession.Read` 的输出时钟上**（唯一读者仍归 observer/relay 所有，注入方不读、不抢字节），写入不经过 shell、原样落 stdin（多行文本一次性写入 + 末尾 `\r`）。写入后记 job 事件 `job.input_injected {bytes,written,quiet_ms,error?}`。worker 路径经 `wsproto.Dispatch.initial_input`/`initial_input_quiet_ms`（协议 v7，`SupportsInitialInput` 只警告不失败：pty 在 worker 上，hub 无法代它敲字；安静窗口随 dispatch 下发，避免 worker 用自己的配置重derive）。
- 状态与提示：成功即 `agent_sessions.state=handed_off` + `handed_off_job_id` + `handed_off_at`（additive 迁移）；`DeliverResult{Path:"takeover", JobID}`；审计行 `plan_decisions(kind='relay', detail={"path":"takeover","job_id":…})`；通知事件 `session.handed_off`（**不进默认集**，与 `session.waiting` 同族，链到 `/jobs/<id>?attach=1`）。`WaitReason` 对 `handed_off` 恒返回 ""，`OpenTurn` 另有一道 `ErrRelayOff` 兜底 —— 原终端的 Stop 因此只放行、不开 turn；心跳/登记响应新增 `notice`（`该会话已于 <时间> 在 web 接管（job <id>），继续请在 web 终端或 --resume；本终端的中继已停用`），hook 在 `UserPromptSubmit`/`Stop` 收到就打到 stderr（只此一处用 stderr：决策走 stdout、日志走 `hook.log`）。
- 幂等与并发：`jobstore.TouchAgentSession` 对 `state='handed_off'` 加了守卫 —— 原终端还会继续上报 Stop / UserPromptSubmit / Notification / SessionEnd（`state=idle|running|ended`），这些 beat **都不能把会话从接管手里拿走**（`state=CASE WHEN state='handed_off' THEN state ELSE ? END`，`ended_at` 同规则）；只有 `POST /v1/sessions/{sid}/release-takeover` 能解除。
- 解除接管：`sessionrelay.ReleaseTakeover(ctx, sid)` **先 cancel 接管 job 再改状态**（`store.ReleaseSessionHandedOff` → `state=idle` + 清空两列）；cancel 失败会**报错而不是假装成功**（一个仍在跑的进程握着会话时解除，等于让两个进程写同一 CLI 会话）。宿主侧 `CancelTakeover` 把"job 已终态/已不存在"视为成功（`job.Cancel` 对终态是幂等 no-op，`ErrJobNotRunning`（needs_review）被吞掉）。
- **`allow_takeover` 默认关**：HTTP `POST /v1/sessions/{sid}/deliver {text, allow_takeover}`（`omitempty`，旧客户端字节不变）—— false 时到 B 前停在 409 `no_tmux`；web 端在收到 `no_tmux`/`pane_missing` 后显示「起新进程接管并发送」，点开是二次确认（"将用 `<agent> --resume` 起一个新进程接管该会话，原终端将不能继续"），确认后才带 `allow_takeover:true` 重发，成功后 `router.push('/jobs/<id>?attach=1')`。`handed_off` 时抽屉输入框禁用，改为「已接管 → job 链接」+「解除接管」按钮。CLI：`gofer session say <id> "<文本>" --deliver --takeover`（`--takeover` 不带 `--deliver` 直接报错，不做静默忽略）。
- 实测（本机 Windows 11 + gofer 单测；`go test ./...` 全绿、`vue-tsc --noEmit` 与 `pnpm build` 通过）：pty 侧两例（安静后写入：子进程先打 banner 后回显，断言 banner 早于回显且只写一次；不安静的终端：子进程每 20ms 输出，断言写入发生时它仍在输出且只写一次）；relay 侧覆盖派发契约（argv/Runner/Cwd 相对/InitialInput 前缀/120x40/3600/ResumeSourceAgent）、`allow_takeover` 开关、四种前提原因、`handed_off` 后 `OpenTurn` 拒绝与四种 beat 不改状态、解除接管（含 cancel 与二次解除 409）；HTTP 侧覆盖 409→`allow_takeover`→200 takeover + `handed_off` 投影 + `notice`、以及 release-takeover 的 409/200/取消 job；`PlanTakeover` 的 argv/开关/两种 runner 的根；CLI 的 `--takeover` 出网与拒绝；hook 的 notice 透出。
- **真链路 smoke（2026-09-18，本机）**：临时 `serve`（独立 config 目录/端口，`-c` + `GOFER_CONFIG_DIR` 指向 TempDir，未碰真实配置）+ 临时 project + 把假 agent 的 `command` 指向仓库内 `testcmd`（`session_resume_interactive: ["pty-echo","BANNER"]`，即真 argv 为 `testcmd.exe pty-echo BANNER`）：`POST /v1/sessions` 登记 → `deliver {text}` 得 409 `no_tmux`（无 `allow_takeover`）→ `deliver {text, allow_takeover:true}` 得 200 `{path:"takeover", job_id, decision_id}`；该 job 真在 ConPTY 里起了子进程（`interactive:true`、`120×40`、`3600s`、title `relay takeover → sid-smok`、状态 `running` 并挂在终端会话表里），事件序列出现 `job.input_injected`，会话变 `handed_off`（`handed_off_job_id`/`handed_off_at` 与 `notice` 均正确）；web 控制台（真实构建产物）渲染出「已接管 → job 链接」+「解除接管」，点「解除接管」后会话回 `idle` 且 `ReleaseTakeover` 走到了 cancel。另在浏览器里驱动了 A 失败后的接管入口：`no_tmux` 后出现「起新进程接管并发送」，二次确认块逐字显示设计文案（"将用 `claude --resume` 起一个新进程接管该会话，原终端将不能继续"），「取消」不产生任何 job。
- **限制（已知）**：
  1. **POLICY roots 路径换算**：worker session 的 `cwd` 是**登记方视角**的绝对路径（容器内 `/work/x`），而 server 只知道该 worker 项目的 `host_path`（如 `D:/work/x`）。两者对不上时 B 报 `cwd_outside_project`，A/B 都不可用 —— 这是设计承认的限制，不是 bug；要在容器里用 B，请让容器与主机路径一致（bind mount 同名路径）或在容器内起 worker 并让 roots 映射后路径一致。
  2. **Windows 主机没有 tmux**：主机上的会话永远走 B；B 依赖 ConPTY（`internal/pty` windows 后端，Win10 1809+），`gofer serve`/worker 不可用时 A/B 都不可用。
  3. **B 的会话分叉是设计选择**：接管后原终端进程仍在，两个进程写同一 CLI session 会分叉，故原终端被标记 `handed_off` 且中继停用；人若坚持在原终端继续，需要先「解除接管」（并接受已分叉的上下文）。
  4. **首条输入是"盲写"**：只按"首次输出后安静"判定 TUI 是否就绪，agent CLI 若在 10s 内仍不稳定地重绘，文本可能在 prompt 未就绪时到达（有 10s 兜底；真实 claude/codex TUI 实测在 1.5s 内已安静）。
  5. **未做**：接管 job 结束（超时/退出）时不会自动把会话置回 idle（仍显示 `handed_off`，人需点「解除接管」）；`session release-takeover` 无 CLI 子命令（web/HTTP 有）；**真实** claude/codex/omp 的 `--resume` 行为未实测（本机没有可续的 CLI 会话；上面的 smoke 用仓库内 `testcmd` 假 agent 跑通了"起进程 → 写首条输入 → 状态/UI/解除"的全链路，真 CLI 的 TUI 就绪时序仍待真机验证）。

### 9.2 其他


- **tmux 按键注入（原阶段 3 备忘，已并入 §9.1 A）**：会话跑在 tmux 中时登记带 `tmux_pane`；web 对 idle 会话回复时，server 派一个 `exec` job 到该会话的 runner 执行 `tmux send-keys -t <pane> -l "<回复>"` + `Enter`。这是向现有终端敲字，不是重开会话，能补上 §1.2 的空白格。
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