# Runbook · 终端会话 ↔ web 消息中继（session relay）

> 设计：[`../design/2026-09-06-agent-session-relay-design.md`](../design/2026-09-06-agent-session-relay-design.md)（SESS-01）。本文是操作手册 + 排障。

## 1. 一次性装配

```bash
# 需要 gofer ≥ 0.36（含 `gofer hook` / `gofer session` / `gofer init hooks`）
gofer init hooks                      # Claude Code: 合并写 ./.claude/settings.json
gofer init hooks --agent codex        # Codex:       合并写 ./.codex/hooks.json
gofer init hooks --agent omp         # omp:         写扩展 ./.omp/extensions/gofer-relay.ts
gofer init hooks --agent jcode --global # jcode:    写 ~/.jcode/config.toml 的 [hooks]（jcode 无项目级）
gofer init hooks --agent all --global # 写到 ~/.claude、~/.codex、~/.omp/agent/extensions、~/.jcode，对所有项目生效
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

**监督中不布防（SUP-01 D，`session.auto_relay_skip_when_supervising`，默认开）**：你（或一个监督用的 Claude 会话）一边等 job 一边开着 `auto` 时，键盘空闲/距上次人工输入的判据会把你**锁在 web 回复框上**——而你要等的是 job 完成通知，通知就在被阻塞的那个回合后面（bd h-aii-s2v4）。所以 `auto` 在这个条件下**根本不布防**：

- 该会话的 `caller_id`（注册时的认证身份）名下还有**在跑的 job**（状态 queued/running/pending_interaction/recovering，且在 `session.supervising_window_sec`（默认 7200s）之内）⇒ `wait_reason` 为空，hook 立即放行；web 会话页在自动开关旁显示原因（`未布防：supervising N jobs`）。
- job 一进终态，自动判据立刻恢复（`needs_review` 不算监督中：那是等人的验收，不是等人离开键盘）。
- 只影响 `auto`：显式 `on` 照旧每次停下都等（那是你明确要求的）。`caller_id` 为空的会话（老会话 / 未配 token）不套用——无从判定是谁的 job。
- 关掉：`session.auto_relay_skip_when_supervising: false`。

**子 agent 在跑也不布防（N1 §C，SESS-10）**：`gofer init hooks`（claude）还会装 `SubagentStart` / `SubagentStop`（命令同样是 `gofer hook claude`，timeout 5）。hook 把它们作为心跳事件上报（`subagent_delta` +1/-1 + `subagent_id` = Claude Code 载荷的 `agent_id`，字段缺失时宽松处理），server 在会话上维护「在跑子 agent 数」（内存，按 `agent_id` 去重；某个子 agent 超过 2 小时没有后续事件就按丢了 Stop 处理、自动归零；SessionStart/SessionEnd 清零；server 重启丢计数，退化为旧行为）。

- 在跑子 agent > 0 与「在跑 job」同级，算监督中：`auto` 不布防，`wait_reason_detail` 为 `supervising N subagents`（与 job 并存时 `supervising 2 jobs, 1 subagents`）。同样受 `session.auto_relay_skip_when_supervising` 控制，且**不要求 `caller_id`**；显式 `on` 不受影响。会话 JSON 新增 `subagent_count`。
- **已阻塞时释放**：计数归零的那个 `SubagentStop` 到达时，若该会话有 OPEN turn 且开关不是 `on`，server 把 turn 关成 `EXPIRED` + `released_by=subagent_done`，会话回 `idle`，阻塞中的 Stop hook 下一次轮询见 `expired` 即放行，主 agent 去消化子 agent 结果。显式 `on` 的 turn 不被它关闭（走自己的等待预算）。**待真机验证**：Claude Code 在 Stop hook 阻塞期间是否还会触发 SubagentStop；若不触发，本条不生效，只剩「布防前判定」+ 等待预算兜底。

**等待预算（`wait_budget_sec`）**：心跳响应（及会话 JSON）带 `wait_budget_sec`，hook 实际等待 = `min(--wait, wait_budget_sec)`：`on` 取 `session.relay_on_wait_sec`（默认 3600），auto 布防取 `session.relay_auto_wait_sec`（默认 600），`0` = server 不设上限（由 `--wait` 决定）；只在会进入等待时给，`gofer job watch` 类「只等 job 事件」的等待不受影响。旧 server 不返回该字段时 hook 行为不变。两项随配置热重载（reload hook 重设）；web 配置页目前不编辑 `session:` 块，改 `config.yaml` 即可。预算用尽 hook 放行，agent 正常停下（日志 `wait budget exhausted, released`；被封顶时日志有 `wait capped by server budget`）。

**忽略 agent 内部会话（gofer-v74l）**：hook 在上报任何事件之前检查载荷 `cwd`，位于忽略目录就**直接退出 0、无输出、不连 server**（不登记、不建工作项）。默认忽略 `~/.codex/memories`（codex 记忆整理 agent 的工作目录）；`GOFER_HOOK_IGNORE_CWDS` 追加（按系统路径分隔符 `:` / Windows `;` 分隔，支持 `~/` 前缀）。按路径前缀（目录边界）匹配，Windows 不区分大小写且 `/` `\` 等价。已存在的这类会话 / 工作项不自动清理（`work rm` 人工处理）。

**中继关闭时仍等 job 事件（Y1）**：Stop 被放行（`off`，或 `auto` 未布防/监督中）并不等于没人等 job——会话只要还有**未投递的 job watch**（PostToolUse 看到 `job X submitted` 自动登记 / `gofer session watch`；`auto`+监督中时 Stop 还会认领名下尚无人 watch 的在跑 job），hook 就**只等 job 事件**：不开 turn、不转发 web 输入，job 到终态即以 block 反馈送入（`[gofer job 完成] …`，与 turn 内一致）；所有 watch 清空或 `--wait` 上限到达才放行。`hook.log`：`relay not waiting (mode=…) but N watched job(s) pending … waiting up to …`。兜底：放行后才完成的 job，下一次 UserPromptSubmit / SessionStart 把未投递的通知作为 additionalContext 补投并标记已投递（只补一次；仅 claude / codex，omp 扩展不接收 hook 输出、jcode 只观察，所以它们不消费 watch）。放行时若仍有监督中 job 但无 watch，日志写明 `supervising N jobs but no watched job to wait for`。

顺带：会话的 `caller_id` 同时也是**作答权**——`say` / `deliver` / `relay set-mode` 只允许该会话 owner 的 caller（governance `require_answer_capability` 开启时 `can_answer` 也可；`caller_id` 为空的老会话放行），worker token 一律 403（h-aii-esus）。

| 时机 | 做法 |
|---|---|
| 要离开 | 对 agent 说"打开中继"，或自己敲 `gofer session relay on`（同目录多个会话时按提示加 `--session`） |
| 在外面 | gofer web → 会话页（`/sessions`）：等回复的会话置顶，打开抽屉看最后一条消息，底部输入框回复；铃铛里「会话」条目也可内联作答 |
| 让它停下 | web 回复 `/off`：关掉中继（mode=off），agent 正常结束回合 |
| 回到电脑 | 终端里任意输入一条（`on` → 降回 `auto`，`auto` 的等待直接释放）；或 `gofer session relay off` / `auto` |
| 忘了开 | `auto` 模式两条判据会自动布防（容器靠判据二）；要显式开就在 web 会话列表点 `on`，**下一次回合结束**生效；会话已空闲则见「无 turn 时送话」 |
| 监督 job 时不想被挡 | `auto` 已自动让路（见上面「监督中不布防」）：不用手工 `/off`，也不会把 job 完成通知挡在回合后面 |
| 会话已空闲 | web 会话抽屉的输入框（没有 OPEN turn 时变成「送入终端」）直接送话；CLI `gofer session say <id> "<回复>" --deliver`。前提：会话在 tmux 里 + 登记了执行机（见下表） |

**无 turn 时送话（阶段 2-A，tmux 注入）**：`POST /v1/sessions/{sid}/deliver {text}`。有 OPEN turn 就等价 `say`（作答）；否则 server 在该会话的 runner 上起一个内部 exec job，确认 pane 存活、前台是 agent CLI（白名单 `session.inject_commands`，默认 `claude|codex|omp|node|gemini|opencode`），再逐行 `tmux send-keys -t <pane> -l -- '<行>'` + `Enter`（文本前缀 `[gofer web 回复] `，上限 8KB，单引号转义）。成功后会话置 `running`，审计行 `plan_decisions(kind=relay, detail={"path":"tmux","job_id":…})`。

**无 turn 时送话（阶段 2-B，`--resume` pty 接管）**：会话没有可用 tmux pane（`no_tmux` / `inject_failed:pane_missing`）或**注入 job 根本没跑起来**（`inject_failed:runner_error`：runner 不可达 / 脚本自己挂了 —— 这两类 A 都没机会看 pane）时，带 `allow_takeover: true` 再发一次即可让 server **起一个新进程接管**：`POST /v1/sessions/{sid}/deliver {text, allow_takeover: true}`（`allow_takeover` 缺省 false —— 接管会把会话从原终端移走，必须显式要）。server 用该会话的 `session_id` 在**同一 runner、同一项目相对目录**起一个交互 pty job（`claude --resume <sid>` / `codex resume <sid>` / `omp --resume <sid>`，argv 来自 agent 的 `SessionResumeInteractive` 模板），把 `[gofer web 回复] <文本>\r` 作为该终端的**首条输入**（pty 首次输出后安静 `session.takeover_input_delay_ms`（默认 1500ms）再写，最多等 10s；多行原样写入），会话随即置 `handed_off`（记 `handed_off_job_id`/`handed_off_at`，审计 `detail={"path":"takeover","job_id":…}`），web 跳到 `/jobs/<id>?attach=1`。前提：agent 有交互 resume 模板（内置 claude/codex/omp）、项目 `allow_interactive: true`、会话 cwd 能换算成执行机上的项目相对路径。

- **原终端会发生什么**：它的中继**停用**（`wait_reason` 恒空、`OpenTurn` 拒绝），下一次 hook 事件（Stop / 你敲字）在 stderr 打一行 `该会话已于 <时间> 在 web 接管（job <id>），继续请在 web 终端或 --resume；本终端的中继已停用`。原进程仍在跑，但两个进程写同一 CLI 会话会分叉 —— 要继续就在 web 的接管终端里说。
- **解除接管**：`gofer session release-takeover <sid>`（web 会话抽屉「解除接管」；HTTP `POST /v1/sessions/{sid}/release-takeover`）：先 cancel 接管 job，再把会话置回 `idle` 并清空 `handed_off_*`，原终端恢复中继（cancel 失败会报 502，不假装成功）。会话未接管 → 409（陈旧请求值得知道，不吞掉）。
- **接管 job 结束时自动释放**：pty job 一到终态，server 就把会话放回 `idle` 并清空 `handed_off_*` —— 那时已经没有进程握着这个 CLI 会话，留着它只会得到一个既不能收新 turn、也没有终端可去的中继。释放不动那个已终态的 job（无可 cancel），事件 `session.takeover_released {session_id, job_id, reason: job_<status>}`（如 `job_done` / `job_failed`；不在默认通知集，要订阅就显式写进 webhook `events`）。所以「接管进程跑完了，会话却一直显示已接管」不会发生；人在 job 还在跑时手动解除仍走上面的 cancel 路径。
- **CLI 等价面**：`gofer session say <id> "<文本>" --deliver --takeover`（`--takeover` 必须与 `--deliver` 同时用）。

失败原因（看 server 返回的 `error` 字段 / web 提示）：

| 原因码 | 含义 / 处理 |
|---|---|
| `no_tmux` | 会话不在 tmux 里（web 报 409）。用 `tmux new -A -s claude` 重开会话，或带 `allow_takeover` 走 B（web 会直接给「起新进程接管并发送」按钮） |
| `no_runner` | 会话没登记执行机：容器内没起 worker，或 `.env` 里缺 `GOFER_HOOK_RUNNER=<worker-id>`（`GOFER_RUN_MODE=client` 的节点不再假装登记成 `server`） |
| `ended` | 会话已结束 |
| `inject_failed:pane_missing` | pane 没了（tmux 会话被关 / 换了 window）；带 `allow_takeover` 时 server 自动改走 B |
| `inject_failed:pane_busy:<cmd>` | pane 前台不是 agent CLI（例如 vim / shell），拒绝敲字；也不会改走 B（你在用那个终端） |
| `inject_failed:runner_error` | 执行机侧失败（runner 不可用、job 超时、project 未开 `allow_exec`、B 的接管 job 提交被拒等）；`gofer job ls --tag relay-inject` / `--tag relay-takeover` 找到那个 job 看日志。带 `allow_takeover` 时这类失败**也会**改走 B（A 没能看到 pane，起个新进程是剩下的办法）；B 自己也提交失败才会把该原因返回给调用方 |
| `no_resume_template` | B 前提不满足：该 agent 没有交互 resume 模板（内置 claude / codex / omp 有） |
| `interactive_not_allowed` | B 前提不满足：项目未开 `allow_interactive` |
| `cwd_outside_project` | B 前提不满足：会话 cwd 换算不到执行机上的项目相对路径（POLICY roots 映射后两台机路径不同，见 `docs/design/2026-09-06-agent-session-relay-design.md` §9.1 限制） |
| `handed_off:<job>` | 该会话已被 job `<job>` 接管：到那个终端继续，或先解除接管 |

CLI 等价面：`gofer session ls / show <id> / say <id> "<回复>" [--deliver [--takeover]] / release-takeover <id> / nudge … / rm <id>`（id 可用前 8 位）；`ls` 的 RELAY 列显示 `on` / `off` / `auto`，auto 且当前在等时显示 `auto·wait(i)`（键盘空闲）或 `auto·wait(t)`（距上次人工输入）；`show` 额外打印 mode 与判定依据。

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

- 会话表 `agent_sessions`：开关是 `relay_mode`（auto|on|off；`relay` 列是 pre-R1 的历史列，自 v0.48 起不读不写，仅 `migrateAgentSessions` 的一次性回填还用），加上 `idle_sec`（键盘空闲读数）、`last_human_at`（判据二的锚点）与 `handed_off_job_id`/`handed_off_at`（阶段 2-B 的接管 job）。turn 复用 `plan_decisions`（additive 列 `session_id`, `kind`, `detail`）。
- 状态：`running → idle`(Stop, 不等) / `waiting_reply`(Stop, 等) / `needs_attention`(Claude Notification) / `handed_off`(web 起了 `--resume` pty job 接管，原终端不再中继) / `ended`(SessionEnd)。`handed_off` 只由 `release-takeover`（`gofer session release-takeover <id>` / web）或**接管 job 自己到达终态时的自动释放**解除 —— 原终端的任何事件都不会把它改回去。
- `on` → 人在终端输入（UserPromptSubmit）即降回 `auto`；`auto` 的等待在人回来时直接释放（探得到就探，探不到就靠事件）。harness 产生的同名事件（注入回复带 `[gofer web 回复]` 前缀、后台任务通知 `<task-notification>`、系统提醒）hook 会上报 `injected`：不动开关、也不当作"人回来了"；`hook.log` 里能看到 `human prompt` / `harness prompt` 的判定。
- 日志：`<config-dir>/run/hook.log`（>5MB 自动清空）；每个事件一行，含 state / relay mode / wait reason。

## 3.0 Claude 会话名（peer_name）

claude hook 每次心跳都会 best-effort 读 Claude 配置目录（`$CLAUDE_CONFIG_DIR`，缺省 `~/.claude`）下的 `sessions/*.json`，按 `sessionId` 匹配，把 `name`（和 `nameSource`）作为 `peer_name` / `peer_name_source` 带给 server；会话改名后下一次心跳更新。该目录是 Claude Code 的内部未公开格式：读不到 / 解析失败 / 字段变化一律静默忽略（`hook.log` 只记 `peer identity unavailable: …`）。Web Sessions 卡片、工作项抽屉会话行、会话详情显示并可复制；`gofer session ls` 的 NAME 列、`session show` 的 `name:` 行。hook 在容器里而 Claude 配置目录不在同一台机器可见时没有名字（不报错）。

## 3.1 手机提醒（可选）

会话等在那里时想让手机响一下，配一个钉钉/飞书群机器人即可，见
[`im-notification.md`](im-notification.md)。要点：事件名 `session.waiting` 必须
显式写进 webhook 的 `events`（默认订阅集不含它），并配 `server.web_base_url`
让消息里带一条直达会话抽屉的链接。

## 3.2 终端会话用量（N2 §A，OBS-14）

hook 在 `Stop` / `SubagentStop` / `SessionEnd` 时增量读该会话的 transcript（路径取 hook payload 的 `transcript_path`），把**新增的** token 用量随心跳 `usage_delta` 上报；`PostToolUse` 不读 transcript。server 累加进会话并按日、按模型记账；web 与 `/v1/stats` 只显示 token，transcript 没有费用字段就不显示费用。

- **偏移状态**：`<config-dir>/run/hook-usage/<session_id>.json`（每个 transcript 文件一个字节偏移 + 首 256 字节指纹 + 已计消息 id 的有界窗口 4096 条；codex 另存上次累计值）。只有心跳被 hub 收下后才落盘新偏移，心跳失败下次重发同一段；同一会话两个 hook 并发时用 `.lock` 互斥，抢不到就跳过本次（下次追上）。`SessionEnd` 顺带清理 30 天未动的状态文件。
- **方言**：
  - claude：`type=assistant` 行的 `message.usage`（input / output / cache_read_input / cache_creation_input）。同一条消息按内容块拆成多行且各自带同一份 usage，按 `message.id` 去重；模型取 `message.model` 分桶。`isSidechain: true` 的行与 `<transcript 去掉 .jsonl>/subagents/*.jsonl`（Claude Code 给每个子 agent 单独写一份，行内 `isSidechain: true`）全部计入「子 agent」，其余计入「主会话」。
  - codex：`event_msg` 里 `token_count` 的 `info.total_token_usage` 是累计值，取本次新读到的**最后一条**与上次累计值之差（codex 的 input 含缓存，上报时 cached 部分挪到 cache_read）；模型取 `turn_context.model`。累计值倒退（新的 rollout）按重置处理。
  - omp：`type=message`、`message.role=assistant` 的 `message.usage`（带 `cost.total` 时保留费用），按行 `id` 去重，模型取 `message.model` / 最近的 `model_change`。
  - generic：行内带顶层 `usage` 对象则按 `runner.UsageFromObject` 读取，有 `id` 则去重。jcode 与其它方言不采集。
- **限制**：单次最多读 4 MB（含子 agent 文件），读不完的下次事件继续；超过 8 MB 的单行跳过；最后一行没写完（无换行）留到下次。transcript 被截断 / 压缩重写时从头重读，已计的消息 id 不重复计；去重窗口之外的老消息在极端情况（重写后文件 > 4096 条消息）可能重计。解析失败只写 `hook.log`（`usage: …`），不影响 hook 流程。
- **存储与接口**：`agent_sessions.usage_json`（累加列）、`session_usage_daily(day, session_id, model, project_key, agent, …)`（UTC 日 + 模型）。`GET /v1/sessions`、`GET /v1/sessions/{sid}` 返回 `usage: {main, sub, total, by_model}`；工作项视图 `usage` 是其当前会话的用量之和；`GET /v1/stats` 的 `session_usage.windows["24h"|"7d"]` 给会话数与 token（total / by_agent）。按日桶统计，窗口取整日桶，最老一端最多多出一天。
- **web**：Sessions 页展开详情 / 会话抽屉显示「主会话用量」「子 agent 用量」；工作项卡显示「会话用量」；Home「Agent 用量」卡下增加终端会话一栏。
- **计入 plan**：会话是某个 plan 的监督会话（`supervisor_session_id`）且该 plan 状态为 `open` / `blocked` 时，心跳带来的每个 `usage_delta` 在同一事务里同时记到 `plan_session_usage(plan_id, session_id, usage_json, updated_at)`（`usage_json` 与会话同形：main / sub / by_model）；`done` / `archived` 的 plan 不再累计。**一个会话同时监督多个 open / blocked plan 时，每笔 delta 只记到其中「最近有动静」的一个**（gofer-9mum，之前每个都记一遍，跨 plan 相加会重复）：动静 = `MAX(plans.updated_at, 该 plan 最新的 plan_todos.updated_at, 该 plan 最新的 jobs.updated_at)`——plan 编辑 / 状态 / 绑定、todo 变化、job 状态变化都算；同分取较新创建的 plan。活跃 plan 换了以后只有之后的 delta 跟着走，已记的不搬。查询走 `idx_plans_supervisor` / `idx_plan_todos_plan` / `idx_jobs_plan_id`，未监督任何 plan 的会话只多一次索引查询。`GET /v1/plans/{id}` 的 `usage` 增加 `session {main, sub, total, by_model, sessions}`（无归属用量时省略）与 `overall {total_tokens, cost_usd}`（jobs + 会话）；`gofer plan show` 多一行 `session:`。**只计绑定之后上报的用量**：绑定前、以及升级到本版本之前的会话用量不回填——`session_usage_daily` 是按日桶的，绑定当天的桶里混着绑定前的用量，回填会多算，所以不做。改绑到另一个会话后，旧会话已记的部分保留（`sessions` 计数为 2）。

## 3.3 会话催办（N2 §E，SESS-12）

人不在电脑前、又不想盯着会话时，可以给会话挂一个定时「催办」：到点把一句话送进会话，等同于你在 web 里点了「发消息给会话」。

```bash
gofer session nudge <sid> --when-stalled 20m -m "卡住了吗？说下进展" --until 3h   # 停滞 20 分钟才催
gofer session nudge <sid> --every 30m -m "继续，做完告诉我"                        # 固定每 30 分钟
gofer session nudge ls [<sid>] [--all]      # 列出（不给 sid = 全部；--all 含已结束）
gofer session nudge pause|resume|rm <nudge-id>
```

web：会话抽屉输入框上方的「催办」区块（列出 / 新建 / 暂停 / 恢复 / 删除）。REST：`POST/GET /v1/sessions/{sid}/nudges`、`GET /v1/nudges`、`PATCH|DELETE /v1/nudges/{id}`。

- **调度**：server 每 30s 扫描持久化表 `session_nudges`（和 schedule / wakeup 一样是 server 内部 sweeper，不依赖外部 cron）。`every`：到 `next_run_at` 就发，之后按间隔顺延（不补发错过的）。`stalled`：见下。
- **停滞判定**：会话 `running`，或 `idle` 但关联的工作项未结（状态不是 done / dropped / parked）；并且「最后进展」距今 ≥ 阈值。最后进展 = `last_seen_at`（任何 hook 心跳，含 Stop / 子 agent 起止 / 提示）、`progress_at`（进行中文本）、`usage_at`（用量有增长，N2 §A 的 `usage_delta` 触发）三者中最近的一个，并且不早于该 nudge 创建 / 上次发送的时刻——所以有进展不会催，同一次停滞每个阈值周期只催一次。`needs_attention` / `waiting_reply` 不算停滞（人在等或该人处理）。
- **送达**：与 web「发消息给会话」同一条阶梯：会话在等回复 → 直接作答该 turn；否则传话人（Claude SendMessage）；无 Claude 地址的 agent 走 deliver_command / tmux。结果记入会话 outbox（operator 为 `gofer-nudge:<nudge-id>`，在会话对话流里和普通 web 消息一起显示）。`offline` 的会话跳过且不计失败；`ended` / `handed_off` 的会话，以及到了 `--until` 的 nudge，自动变为 `ended`。
- **失败处理**：不重试轰炸。同一个 nudge 连续 3 次送达失败 → 自动 `paused`，记 `pause_reason` / `last_error`，并发通知事件 `session.nudge_paused`；成功一次失败计数清零。**该事件不在默认通知集**，要手机收到请把它写进 webhook 的 `events`（同 `session.waiting`，见 3.1）。会话恢复可达后用 `nudge resume`（或 web「恢复」）清零继续。
- **权限**：只有人（user / admin caller，且是会话属主或持有 `can_answer`）能创建与管理。worker token 与所有 job 凭证（member / leader / steward）都是 403——**管家不拍板**，不替人设定时器去打断一个正在工作的会话；steward 对 nudges 连读也不行。
- **注意**：催办文本会被目标 agent 当作「另一个会话转达的消息」（传话人路径）或终端输入（tmux 路径）读到，不是你本人的授权——别在里面放需要审批的指令。

## 4. 排障

| 现象 | 查看 | 处理 |
|---|---|---|
| web 看不到会话 | `gofer session ls`；`hook.log` 有无 `SessionStart registered` | hooks 未装（`gofer init hooks`）；或 hook 进程连不上 server（`hook.log` 出现 `register failed` → 检查 `.env`）。装好后新会话才登记，老会话在下一个事件时自动补登记 |
| 开了中继但停下时没进 web | `hook.log` 该会话最后一行 | `relay off, released` = server 说这次不等（若带 `supervising N jobs but no watched job` 则是监督中却没有可等的 watch；有 watch 时会改走 `waiting … for job events only`）（mode off，或 auto 的判据没成立：看 `gofer session show <id>` 的 relay 行）；`open turn failed ... 409` = 同一瞬间被关 |
| 容器里会话不自动布防 | `gofer session show <id>` 的 relay 行 | 空闲值恒为 `-1`（无 X11）→ 走判据二：确认 `session.auto_relay_turn_sec`（默认 15 分钟）没被写成 `0`，且 `last_human_at` 不是 0 |
| auto 判据没成立却以为会等 | `gofer session ls` 的 `auto·wait(i)` / `auto·wait(t)` | 探到键盘时以空闲值为准：人还在别的窗口打字（空闲小）就不会布防 |
| 回复后 agent 没继续 | `hook.log` 有无 `answered (...) continuing` | 有 → agent 已收到，看终端；没有 → turn 可能已过期（`gofer session show` 里 `[EXPIRED]`），重新让它停一次 |
| 子 agent 跑着，停下却被 web 卡住 / 或反过来没等 | `gofer session show <id>` 的 `wait_reason_detail`；`hook.log` 的 `subagent id=… delta=…` | 没有 `subagent` 日志 = SubagentStart/Stop 没装（`gofer init hooks` 重装）；计数卡住的最长 2 小时自愈；`auto_relay_skip_when_supervising: false` 会让子 agent 不再免布防 |
| 会话卡没有用量 / 用量偏少 | `hook.log` 的 `usage:` 行；`<config-dir>/run/hook-usage/<sid>.json` 的偏移 | 只在 Stop / SubagentStop / SessionEnd 读；hook 没带 `transcript_path` 或 transcript 在别的机器上就读不到；`usage: skipped` 后接原因；一次最多读 4 MB，大 transcript 要几次事件才追平 |
| 终端一直"hook 运行中" | 正常：这就是等待 | 想直接输入按 Esc 取消；或 web 回复 `/off` |
| Stop 后终端立刻恢复但没走中继 | `hook.log` 有 `heartbeat failed` | server 不可达，hook 按设计直接放行 |
| 忘开开关且会话已空闲 | — | 走「无 turn 时送话」：web 抽屉「送入终端」/ `session say --deliver`（需 tmux + 已登记执行机）；没有 tmux 就用「起新进程接管并发送」/ `--deliver --takeover`；否则在终端输入一次 |
| 会话显示「已接管」但我要回原终端 | `gofer session show <id>` 看 `handed_off_job_id` | `gofer session release-takeover <id>`（或 web 抽屉「解除接管」；cancel 接管 job 后会话回 idle）；接管 job 自己结束时 server 也会自动释放；或在接管终端里继续 |
| 接管后原终端敲字没反应 | 设计如此 | 原终端的 hook 会打一行 `该会话已于 … 在 web 接管`；两个进程写同一会话会分叉，继续请在接管终端 |
| 接管被拒 `session_alive` | `gofer session show <id>` 的 last_seen；agent 是否配了 `deliver_command` | 原进程还在（最近一次 hook 心跳在 `session.takeover_alive_sec`，默认 120s 内；或有 `deliver_command` 的 agent 没有报 not_running）。两个进程写同一会话会分叉，所以拒绝：请在原终端继续或用在线送话；进程确实没了（SessionEnd / 离线 / 心跳超时）才允许接管。`0` 关闭该保护 |
| 送话 `deliver_failed:<原因>` | 该会话 agent 的 `deliver_command`；`gofer job ls --tag relay-deliver` 看内部 job 的 stderr | 命令退出码非 0 且非 3；**不会**再退到 tmux（失败的命令可能已送达，再走一条路会重复送）。修好命令或看原因文本 |
| 接管报 `cwd_outside_project` | `gofer session show <id>` 的 cwd 与该项目在执行机上的根 | 容器与主机路径不一致（POLICY roots 映射），让两侧同名路径（bind mount）后再试；A 路径不受影响 |

## 5. 端到端验证（容器内自测记录 2026-09-06）

临时 serve（127.0.0.1:18765）+ `gofer init hooks -o <proj>` + `claude -p`（PATH 指向新二进制）：

1. SessionStart 登记，项目由 cwd 匹配；UserPromptSubmit 写入标题。
2. `relay on` 后 agent 停下 → `waiting_reply`，`last message = "step one done"`。
3. `session say <sid> "Now reply with exactly: step two done"` → hook 输出 block → agent 续跑，最终输出 `step two done`。
4. 第二次停下 → `say /off` → hook 放行，SessionEnd → `ended`。
5. 关着开关的 Stop 耗时 12ms；server 停机时 hook 直接退出。

6. web 侧（agent-browser）：会话页「AGENT 会话」分组显示等待回复行 + 铃铛 toast；抽屉内输入框发送 → 挂起的 hook 立即输出 `{"decision":"block","reason":"[gofer web 回复] …"}`，会话回到 running。
7. 等待超过 hook 预算（`--wait`）时 turn 过期、hook 放行、会话置 idle，开关保持 on（下一次 Stop 继续中继）。

8. 工作空间根 + 主机 serve 真机：`gofer init hooks` 与 bd 的 SessionStart hook 共存；会话按 cwd 匹配到项目、runner=容器 worker；后台任务通知触发的 UserPromptSubmit 被判为 harness、中继保持；web 抽屉作答注入、`/off` 放行，两轮全通。

Codex 真机四步已在 2026-10-05 实测通过（见 §7）。

## 6. 2026-10-04 真机补测（P 批）

### 6.1 交互 TUI 下等待期间按 Esc（容器内 tmux 驱动 Claude Code 实测）

临时 serve + `gofer init hooks -o <临时目录>` + tmux 里启动 `claude`，`session relay on` 后让它停下（Stop hook 挂起，TUI 显示 `running Stop hook`，会话 `waiting_reply`，turn `OPEN`），再 `tmux send-keys Escape`：

- 画面立即显示 `Interrupted · What should Claude do instead?`，输入框恢复可用；hook 进程被**信号**终止（用 bash 包装脚本 trap 实测，不是不可捕获的 SIGKILL）。
- **缺陷（已修）**：旧版 hook 对信号无处理，server 不知道等待已被放弃——显式 `on` 的会话一直 `waiting_reply`、turn 一直 `OPEN`，web 还能对它作答（答案无人接收，静默丢失），直到终端下一次输入才被清理（旧版 `Interrupt` 事件也只清非 `on` 的 turn）。
- **修复**：Stop hook 在等待期间捕获 SIGINT/SIGTERM/SIGHUP，上报 `Interrupt` 后退出（≤3s）；server 对 `Interrupt` 无条件关闭该会话的 OPEN turn（`released_by=interrupted`），状态回 `idle`，**开关保持 `on`**。复测：Esc 后 3s 内会话 `idle`、relay 仍 `on`、turn `EXPIRED`；`hook.log` 留有 `interrupted by the terminal while waiting, releasing the turn`。
- Esc 后继续：再输入一条（`on` 按设计降回 `auto`）→ 再 `relay on` → 下一次 Stop 照常等待；`session say` 注入 `Now reply with exactly: replied via web` 后 agent 续跑并输出，随后再次停下仍中继（`on` = 每次停下都等）；`say /off` → 放行，mode=off。
- 提示：人在终端输入后 `on` 会降回 `auto` 是设计行为；要在下一回合等待，须在**提交 prompt 之后**再 `relay on`（先开再输入会被降级）。

### 6.2 会话互转（job resume 跨 agent）真机

主机 `gofer agent ls`：有 `claude` / `claude-acp` / `codex` / `tty-*`，**没有 `codex-acp`**。

- claude → claude-acp：`claude`（cli）job 记暗号 `PINEAPPLE-42`（job `20261004-224209-6440a835`，session `a8e3a2b6-…`，回答"记住了"）；`job resume --mode batch --agent claude` 回答 `PINEAPPLE-42`（job `20261004-224344-694ca866`，同族同 agent，批处理续接通过）；`job resume --mode session --agent claude-acp`（job `20261004-224232-bdceac06`）通过了 `session/load`，在 `session/prompt` 处失败：`400 MissingSessionID … x-opencode-session`——主机 Claude 走的 API 网关要求请求带 `x-opencode-session` 头，`@zed-industries/claude-code-acp` 的请求不带，**这是主机模型网关的限制，不是会话族问题**（全新的 `claude-acp` 一次性 job `20261004-224142-277140c9` 同样报该错）。因此 claude-acp 方向只验证到「能加载 CLI 会话」，**模型回答原文未能取得**；claude-acp → claude 方向因 claude-acp 在主机上跑不起来无法造出会话，未验证。
- codex 族（**已被 §8.1 推翻**，codex-acp 现已入 codex 族）：主机没有 `codex-acp` agent，无从验证 codex-acp ↔ `codex resume`，**`builtinSessionFamilies` 保持不含 codex-acp**（如需验证：在主机装 codex-acp 适配器、在 server 配置声明该 agent 后重测；本批不擅自改 server 配置）。

- **更新（v0.104.0，同日）**：上面的 `x-opencode-session` 报错根因是 `@zed-industries/claude-code-acp` 已废弃（停在 0.16.x，自带旧 Claude Agent SDK），旧 SDK 不发新版网关要求的会话头；内置 `claude-acp` 模板改用 `@agentclientprotocol/claude-agent-acp`（0.85.x）后复测：
  - 一次性 claude-acp job `20261004-235428-b8e50604` 回答 `OK-ACP`；
  - claude → claude-acp：`job resume 20261004-224209-6440a835 --mode session --agent claude-acp` → 新 job `20261004-235447-92b08e7f` 第 1 轮回答 `PINEAPPLE-42`；
  - claude-acp → claude：`job resume 20261004-235428-b8e50604 --mode batch --agent claude` → job `20261004-235511-6709c6d3` 原样复述 `OK-ACP`。
  - 结论：claude 族（claude / claude-acp / tty-claude）双向互转真机通过（cwd 一致）。codex-acp 仍因主机未配置该 agent 未验证。

### 6.3 Codex 中继四步（当时未完成，卡在 codex 项目信任；已在 §7.1 补测通过）

- 已装配（codex 在上一棒完成，备份在 `tmp/gofer-p-backup/20261004/`）：`.codex/hooks.json` 已含 gofer 6 类事件。
- 实测：主机清掉 `GOFER_JOB*` / `CLAUDE*` 后 `codex exec`（codex-cli 0.160.0）能跑通并显示 `hook: SessionStart` / `hook: Stop`，说明 `exec` 会触发 hooks；但这些 hooks 是**别的来源**的（hook.log 没有任何 codex 记录；用 PATH 垫片替换 `gofer` 也没被调用），推断项目层 `.codex/hooks.json` 因项目未被 codex trust 而没加载（§1 的「项目层 `.codex/` 已被 trust」条件）。
- 另一个前置：在 job 外直接调 `gofer hook codex`（模拟用户真实会话，`GOFER_JOB*` 已清）连主机 server 时是 `401 missing or invalid bearer token`——主机上 hook 进程需要自己能拿到 token（`$GOFER_CONFIG_DIR/.env` 或环境变量），job 环境里的 job token 被清掉后就没有了。
- 需要用户做：在主机该工作区交互启动一次 `codex` 并信任项目（或授权给 codex 配置写入 trust），并确认主机 `~/.config/gofer/.env` 里有 `GOFER_SERVER_ADDR/TOKEN`；之后重跑 §5 四步。


## 7. 2026-10-05 真机补测（H 批）

### 7.1 Codex 中继四步（codex-cli 0.160.0，主机，项目 my-tools-dev）通过

用户已在主机交互批准项目层 hooks。在 gofer job 里模拟真实会话：先清掉 `GOFER_JOB*` / `GOFER_RESULT*` / `GOFER_MESSENGER` / `CLAUDE*`，设 `GOFER_CONFIG_DIR=D:\work\inhere\config\win-env\gofer`（`.env` 里有 server 地址与 token），`codex exec --skip-git-repo-check "<让 agent 跑一条 Start-Sleep 70 再回复 step one done>"`，会话 `01a109f6`：

1. SessionStart 登记：`gofer session ls` 立刻出现 agent=codex、项目 my-tools-dev、state=running（hook.log：`SessionStart registered`，UserPromptSubmit 写入标题）。
2. 在 agent 执行命令期间 `gofer session relay --session <sid> on`（注意：必须在**提交 prompt 之后**再开，先开会被首条人工输入降回 auto，见 §6.1）；agent 停下后 `waiting_reply`，turn 1 `OPEN`，`last message = step one done`（hook.log：`Stop turn ... open (reason=mode_on)`）。
3. `gofer session say <sid> "Now reply with exactly: step two done"` → hook 输出 block，codex 继续并输出 `step two done`，turn 1 `ANSWERED`、turn 2 `OPEN`。
4. `say /off` → `Stop answered /off, released`，codex 退出，SessionEnd → `state=ended relay=off`；job 输出 `step two done`、`EXIT=0`。

结论：codex 0.160 的 `exec` 触发 SessionStart / UserPromptSubmit / Stop / SessionEnd，payload 与 gofer 预期一致，**无需改代码**。观察到的小事：同项目有运行中的 gofer job 时，`auto` 判据会按「监督中不布防」放行（UserPromptSubmit 返回 `wait_detail=supervising 1 jobs`），要等待须显式 `relay on`。

### 7.2 omp（oh-my-pi 18.6.0）与 jcode（0.90.0）的 hook 能力

| | omp | jcode |
|---|---|---|
| hook 形态 | TypeScript 扩展（`pi.on(event, handler)`，没有 shell 命令 hook） | `config.toml` 的 `[hooks]` 里写 shell 命令（也可用 `JCODE_HOOK_<EVENT>` 环境变量），命令按 shell 风格解析但不经 shell 执行 |
| 配置位置 | 全局 `~/.omp/agent/extensions/*.ts`（`PI_CODING_AGENT_DIR`）；项目 `.omp/extensions/*.ts` 与 `.omp/hooks/{pre,post}/*.ts`（均实测加载）；`--hook <file>` 单次加载 | 仅全局 `~/.jcode/config.toml`（`JCODE_HOME`）；实测项目内 `.jcode/config.toml` 不生效 |
| 事件 | session_start、input、before_agent_start、agent_start/end、turn_start/end、tool_call/tool_result、**session_stop**、session_shutdown 等 | session_start、turn_start、turn_end、pre_tool、post_tool、session_end |
| payload | 事件对象 + `ctx`（`ctx.sessionManager.getSessionId()`、`ctx.cwd`）；`session_stop` 含 `last_assistant_message`、`session_id`、`stop_hook_active` | 环境变量 `JCODE_HOOK_EVENT/SESSION_ID/CWD/MODEL/TOOL_NAME/LAST_ASSISTANT_TEXT/...` 与同内容 JSON 的 `JCODE_HOOK_PAYLOAD`；stdin 为空 |
| 阻塞 Stop 并注入消息 | 可：`session_stop` 返回 `{decision:"block",reason}`（Claude 同款，续跑最多 8 次）；但 **handler 超时 30s**（实测 40s 的 handler 被截断并报 `handler timed out after 30000ms`），长等待不可行 | 不可：除 `pre_tool`（工具前闸门，exit 2 拦截）外全部 fire-and-forget |
| 注入消息的替代 | `pi.sendUserMessage(text,{deliverAs:"followUp"})`：agent 空闲时开新一轮（rpc 模式实测通过） | 无；只能 tmux 注入 |

实现：
- `gofer hook omp`：与 Claude/Codex 同一份 Run 流程；omp 端是一个嵌入的 TS 扩展 `gofer-relay.ts`（`hooks/omp.gofer-relay.ts`），只做事件→JSON 转发。Stop 时不阻塞 handler：起后台子进程 `gofer hook omp --wait 7140` 等 web 回复，回复到达后用 `sendUserMessage(followUp)` 送回（人在终端输入、新的 Stop、会话关闭都会取消等待）。
- `gofer hook jcode`：从 `JCODE_HOOK_*` 读事件，`turn_end` 映射为「只汇报」的 Stop（`Options` 的 observe-only 分支：更新 state/最后一条消息，**不开 turn、不等待**）。
- `gofer init hooks --agent omp|jcode|all`：omp 写/删带 `@gofer-managed-omp-extension` 标记的扩展文件（同名非 gofer 文件拒绝覆盖，`--force` 才换）；jcode 对 `[hooks]` 做逐行合并，保留其它表/注释/用户已配事件（jcode 每个事件只能一条命令，已有用户命令则提示并跳过，空串视为未配置）。

真机验证（主机临时 serve：127.0.0.1 随机端口、独立 `GOFER_CONFIG_DIR`、独立 token，验证后已停；正式 8767 server 未动）：
- omp（`omp --mode rpc`，扩展经 `gofer init hooks --agent omp -o <项目>` 安装）：SessionStart 登记（agent=omp，按 cwd 匹配项目）→ 人工 prompt 写标题/running → `relay on` 后 Stop 进入 `waiting_reply`（turn 1，last message=`first answer`）→ `say` 注入，omp 以新一轮继续并输出 `step two done`（turn 2 `OPEN`）→ `say /off` 放行 → 退出后 `ended`、`relay=off`。
- jcode（`jcode run`，用 `JCODE_HOOK_*=gofer hook jcode` 环境变量，与 `[hooks]` 等价）：SessionStart 登记（agent=jcode）、SessionEnd 后 `ended`。**（turn_* 已在 §8.3 于 TUI 下验证通过）**：`turn_start/turn_end/post_tool` 在 `jcode run` / `repl` 下没有触发 turn_*（post_tool 的 payload 已用转储脚本取到），推测这两个事件只在 TUI/server 客户端路径触发，无法在无头环境实测；`[hooks]` 写入后 jcode 实际加载 config.toml 也只验证了环境变量等价路径（`JCODE_HOME` 指向无凭据目录时 jcode 在登录检查处退出）。

### 7.3 全局安装

`gofer init hooks --agent all --global`：写 `~/.claude/settings.json`、`~/.codex/hooks.json`、`~/.omp/agent/extensions/gofer-relay.ts`、`~/.jcode/config.toml`。已有项目级安装与全局并存会让同一事件触发两次（claude 对相同命令去重；codex/omp 未验证，按重复处理）：全局装完 gofer 会扫描当前目录和 config 里登记的项目，列出这些项目并给出清理命令 `gofer init hooks --remove --agent <名> -o <项目目录>`（`--remove` 只删 gofer 自己的条目）。codex 全局 hook 同样要在主机交互模式批准一次（按内容哈希）；不在 gofer 项目列表里的工作区，会话以「无项目」登记，中继/传话可用，唤醒（resume）需要项目；job 内跑的 agent 因 `GOFER_JOB_ID` 直接放行。

## 8. 2026-10-05 真机补测（L 批）

方法：主机上用 worktree 构建的 windows 二进制起**临时 serve**（独立 `GOFER_CONFIG_DIR`、127.0.0.1 随机端口、独立 token，项目指向一个无关工作目录），验证后进程已结束；正式 8767 server 与正式 worker 未动。

### 8.1 codex-acp 换新包 + codex 族互转（通过，codex-acp 已加入 codex 族）

- 旧模板 `command: codex-acp` 在主机 PATH 上没有，detect 过不了，从未注入。`@zed-industries/codex-acp` 已废弃，内置模板改为 `npx -y @agentclientprotocol/codex-acp`（2.1.1），detect `npx -y @agentclientprotocol/codex-acp --version`（实测输出 `@agentclientprotocol/codex-acp 2.1.1`）。包的 `initialize` 声明 `loadSession: true`，会话模式为 `read-only / workspace-write / agent / agent-full-access`，所以 `acp.modes.read_only = read-only`。临时 serve 的 `agent ls` 里 `codex-acp type=acp-agent command=npx` 被注入；serve 日志 `acp runner: agent initialized ... load_session=true`。
- 互转实测（防止模型"凭记忆"蒙对，问题是**逐字复述本会话的第一条用户消息**，暗号每次不同）：
  1. codex-acp 一次性 job `20261005-130030-ecfb3fcc`（session `01a10a6f-5f29-7ac0-ab0d-985dfc1a5778`，prompt 含暗号 LYNX-118）→ `job resume --mode batch --agent codex` job `20261005-130054-38f66176`，输出原样复述 `Remember the code word LYNX-118. Reply with only: OK`。
  2. codex（cli）job `20261005-130118-e77d17b7`（session `01a10a70-1809-7930-bf1b-74da1a3883b8`，暗号 MOOSE-264）→ `job resume --mode session --agent codex-acp` job `20261005-130147-8dfda8ff`，turn 1 原样复述 `Remember the code word MOOSE-264. Reply with only: OK`。
  3. 同 agent 基线：codex-acp 续接第 1 项的会话（job `20261005-130159-5d77554e`）同样复述 LYNX-118。
  4. 存储一致：两个 codex-acp / codex 会话都落在 `~/.codex/sessions/2026/10/05/rollout-<时间>-<session id>.jsonl`，文件名里的 id 与 ACP 的 session id 完全相同——这正是 `codex resume <id>` 认的 id。
- 结论：codex-acp 与 codex / tty-codex 共用同一份磁盘会话存储，**`builtinSessionFamilies` 已加入 `"codex-acp": "codex"`**（`internal/agent/session_family.go`，测试 `TestSessionFamilyAndCompat`）。
- 踩坑记录：先前一次用"记暗号 → 问暗号"的提问方式，codex-acp 续接 A 会话却答成了另一个会话的暗号（疑似 codex 的跨会话记忆干扰），所以最终用例改为复述首条用户消息。另：`job run` 的 acp 会话型 job（`resume --mode session`）是常驻的，只会停在 `awaiting_input`，要 `job end` 才结束；`resume` 不加 `--sync`。

### 8.2 omp 扩展：取消等待时上报 Interrupt + 交互 TUI 下 web 回复续跑（通过）

驱动方式：临时 serve 里定义一个 `interactive: true` 的 tty agent（powershell 包装脚本清掉 `GOFER_JOB*`/`CLAUDE*` 变量、设好 `GOFER_CONFIG`/`GOFER_BIN` 后启动 `omp`，主机 omp 18.6.0 TUI，ConPTY），经 `POST /v1/jobs/{id}/attach-ticket` + `/attach` WebSocket 往 pty 里敲键，会话状态用 `/v1/sessions`、`session show` 读取。要让 `auto` 判据在"被 gofer job 监督"时也能布防，临时配置里设了 `session.auto_relay_idle_sec: 30` 与 `auto_relay_skip_when_supervising: false`。

- **TUI 续跑（通过）**：TUI 里敲 prompt → omp 答完 → Stop → 会话 `waiting_reply`、turn 1 `OPEN`（`reason=idle_probe`）→ `session say <sid> "Reply with exactly: step two done"` → TUI 里出现 `[gofer web 回复] Reply with exactly: step two done`，omp 开新一轮回答 `step two done`，turn 1 `ANSWERED`、turn 2 `OPEN`（hook.log：`Stop answered (33 bytes), continuing`）。
- **Interrupt 对比（旧扩展 vs 新扩展）**：同一流程，显式 `relay on`、turn 2 `OPEN` 时在 TUI 里按 Ctrl+D 退出 omp（走 `session_shutdown`，没有 `input` 事件）：
  - 旧扩展：hook.log 只有 `SessionEnd`，会话 `ended` 后 turn 2 仍是 **`[OPEN]`**（Windows 上 `kill()` 硬终止，等待进程来不及上报）。
  - 新扩展：先 `Interrupt state=idle relay=on wait=mode_on`，再 `SessionEnd`，turn 2 为 **`[EXPIRED]`**。两次复测一致。
  - 在 TUI 里输入 `/exit`（`input` 事件）时新旧都会把 turn 关掉（旧版靠"人工输入把 on 降回 auto 并放行"），差别只在 `on` 模式下非输入类取消（退出、会话关闭）。
- 服务端：`sessionrelay.Heartbeat` 对 `Interrupt` 与 agent 无关（无条件关该会话的 OPEN turn、回 idle、开关不变），`TestOmpInterruptEventSettlesTheTurn` 钉住 omp payload 的 `Interrupt` → 同一条 beat 路径。
- 实现注意：`session_stop` 里的 `cancelWaiter()` **不**发 Interrupt（紧接着要开新 turn，迟到的 Interrupt 会把新 turn 关掉）；`session_shutdown` 里先 await Interrupt 再发 `SessionEnd`，保证顺序。

### 8.3 jcode turn_* 事件（通过）

用同样的 pty 驱动在主机交互 TUI（jcode 0.90.0）里跑，hook 命令经一个转储脚本（记录 `JCODE_HOOK_*` 再转给 `gofer hook jcode`）：TUI 里敲 `Reply with exactly: jcode ok` 后 `jc-events.log` 记到 `session_start` → **`turn_start` → `turn_end`（`LAST_ASSISTANT_TEXT=jcode ok`）**；gofer 侧 hook.log：`SessionStart registered` → `UserPromptSubmit … injected`（turn_start 无 prompt 文本，按 harness 处理）→ `Stop state=idle relay=auto`（observe-only，不开 turn）→ `SessionEnd`；`session show`：agent=jcode、state=idle、**last message=`jcode ok`**。结论：turn_* 在 TUI 下会触发（H 批在 `jcode run`/repl 无头模式下没触发，推测正确），observe-only Stop 能更新最后一条消息与状态。


## 9. 通用 agent 接入（generic 方言）

目的：任何自研 agent（本节用通用名 `myagent`）不改 gofer 代码，就能走完整链路——会话登记、web 传话、用量、工作项整理、送话、会话互通。接入方实现 hook 客户端 + transcript 导出 + 一条送话命令，gofer 侧只写配置。

### 9.1 hook：`gofer hook generic --agent <key>`

```bash
# stdin 是一行 JSON（与 claude 同形）；<key> 是 gofer 配置里这个 agent 的 key，会话就以它登记，
# 续接 / 接管 / 唤醒按该 agent 的 session_resume / session_resume_interactive 模板。
echo '{"session_id":"s1","cwd":"/w/repo","transcript_path":"/home/u/.myagent/transcripts/s1.jsonl","hook_event_name":"SessionStart","source":"startup"}' \
  | gofer hook generic --agent myagent
```

`--agent` 必填（小写字母/数字/`- _ .`）；`<key>` 不在 gofer 配置里 server 也照常登记（只是续接/接管找不到模板，会报 `no_resume_template`）。`--wait N`（Stop 等待上限秒数）、`--poll`、`--runner`、`--project` 与 claude 方言相同；`GOFER_JOB_ID` 存在（agent 本身是 gofer job）时直接放行。hook 失败、超时、无 gofer 都不应影响对话（gofer 本身也不会以非 0 退出，除非调用方式错误）。

| `hook_event_name` | 何时发 | 用到的字段 | 输出 |
|---|---|---|---|
| `SessionStart` | 会话创建 / 恢复打开 | `session_id` `cwd` `transcript_path` `source` | 可能有 `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"…"}}`（补投上次错过的 `[gofer job 完成]` 通知，**只补一次**），接入方把它作为系统上下文附加到下一轮 |
| `UserPromptSubmit` | 用户提交输入 | `prompt` `injected`（可选 bool，省略=false） | 同上；`injected:true`（接入方声明这条输入是注入的：web 回复、`[gofer job 完成]` 通知、`[来自 …]` 本地送话）→ 视为 injected，**不算「人回来了」**（不关 relay、不改标题、不刷新人工输入时间）。兜底：`injected` 缺省时，`prompt` 以 `[gofer web 回复]` / `[gofer job 完成]` 开头、为空、或是 `<tag>` 开头的系统内容也判为 injected。其它方言（claude/codex）无此字段，行为不变 |
| `PostToolUse` | shell/exec 类工具完成 | `tool_name`（`shell`/`bash`/`exec`/`command`/`run_command`…）`tool_output` | 无；输出里出现 `job <id> submitted` / `gofer job watch <id>` 就登记 job watch |
| `Stop` | 一轮结束，回到等待输入 | `last_assistant_message`（本轮最终回复，**直接取自载荷，不读 transcript**）`stop_hook_active` | web 有回复时 `{"decision":"block","reason":"[gofer web 回复] <文本>"}`；否则为空（中继没开 / 等待超时 / `/off`）。可能还会因被监视 job 完成而返回 `block`，reason 是 `[gofer job 完成] …` |
| `Interrupt` | 用户在等待期间开始输入 / 取消等待 | — | 无；关闭该会话的 OPEN turn（`released_by=interrupted`），会话回 idle |
| `SessionEnd` | 退出 | — | 无 |
| `Notification` | 可选 | `notification_type` `message` | 无 |

输出格式与 claude 完全一致：补投走 `hookSpecificOutput.additionalContext`，Stop 注入走顶层 `decision`/`reason`。

**Stop 等待的建议做法（接入方）**：一轮结束、回复打印完之后，**在后台**起 `gofer hook generic --agent <key> --wait <N>`（Stop 事件），终端照常显示输入提示。后台进程返回 `block` 就把 `reason` 当作一条用户输入开新一轮（本轮 `stop_hook_active=true`）；返回空 / 出错就保持空闲。用户在终端开始输入时，**先同步发一次 `Interrupt` 事件，再结束后台等待进程**，然后按普通输入处理——顺序不能反（先杀进程再 Interrupt，server 会一直把会话当成在等）；退出时若还在等，先 `Interrupt` 再 `SessionEnd`。

### 9.2 agent 配置

```yaml
agents:
  myagent-cli:
    type: cli-agent
    command: myagent
    args: [run, --output-format, stream-json, --prompt, "{{prompt}}"]
    session_inject: [--session-id, "{{session_id}}"]
    session_resume: [run, --output-format, stream-json, --resume, "{{session_id}}", --prompt, "{{prompt}}"]
    session_resume_interactive: [chat, --resume, "{{session_id}}"]   # 送话 B（接管）/ 唤醒用
    output_format: ndjson
    ndjson_keep: [session, result, error]
    ndjson_stdout: final_text
    ndjson_stdout_path: result
    ndjson_usage_path: usage          # 结果行里的用量对象（snake_case / camelCase 都认）
    transcript_dialect: generic       # claude | codex | omp | generic
    inject_process: [myagent]         # tmux 送话的前台进程白名单（并入 session.inject_commands）
    session_family: myfam             # 与同族 agent 互相续接
    deliver_command: [send, --session, "{{session_id}}"]   # 在线送话（见 9.5）
    deliver_stdin: true               # 文本走 stdin
  myagent-acp:
    type: acp-agent
    command: myagent
    args: [acp]
    session_family: myfam
```

- **`ndjson_usage_path`**：点路径（`usage` / `result.usage`），从 ndjson 行读用量对象，经与 claude/omp/ACP 同一个读取器入 job 用量（`source: ndjson:<agent>`），所以 `job show`、统计、`agent status` 的 24h 用量都看得到；取**最后一个**带该对象的行，即使该行被 `ndjson_keep` 丢弃也读。续接 job（exec 载体）按**源 agent** 的配置采集。需要 `output_format: ndjson`；不配则行为不变。worker 上跑的 job 用 worker 自己的 agent 配置。
- **`inject_process`**：进程名（不含路径和扩展名）。与 `session.inject_commands`（缺省内置 claude/codex/omp/node/gemini/opencode）取并集，**只对登记为该 agent 的会话生效**——全局放宽会让任何会话的 pane 只要碰巧在跑这个进程名就被敲入文本。
- **`session_family`**：显式声明会话族，**覆盖**内置表（claude/claude-acp、codex/codex-acp）也可加入内置族（写 `claude`）。同族的 cli-agent 与 acp-agent 可互相续接（`job resume --agent <另一个>`；acp 用 `session/load`）。名字大小写不敏感。
- 都能在控制台 Config → Agents 表单里编辑，`gofer config validate` 校验取值（未知方言 / 路径含 `/` 的进程名 / 缺 `{{text}}` 的 deliver_command 等直接报错）。

### 9.3 transcript：`generic` 方言

接入方为每个会话追加写一个 jsonl（路径随 hook 的 `transcript_path` 登记；须是 `.jsonl`，且在 home / `GOFER_TRANSCRIPT_ROOTS` 之下）：

```json
{"v":1,"type":"user","ts":"2026-10-07T10:00:00Z","text":"…","injected":false}
{"v":1,"type":"assistant","ts":"…","text":"…"}
{"v":1,"type":"tool","ts":"…","name":"shell","summary":"go test ./... (exit 0)"}
```

user / assistant 转对话轮次，tool 转一行工具进度，`injected=true`（送话注入 / 补投上下文）的 user 行**不算人的发言**，整理器不会把它当作「人回来了」。方言由 agent 的 `transcript_dialect` 决定，其次按 agent key 前缀（claude/codex/omp），最后嗅探内容（`"v":1` + type∈user|assistant|tool 即 generic）。解析一律发生在 server 侧（worker 的 `transcript_tail` 帧只回原始字节），所以**协议不需要新字段**，旧 worker 也能用（≥ v17）。

### 9.4 送话与唤醒

- **tmux 注入（A）**：配 `inject_process`；会话要在 tmux 里且登记了执行机。
- **接管（B）**：`session_resume_interactive` 起新进程 `--resume`，首行写入 `[gofer web 回复] <文本>\r`；`allow_interactive` 与 cwd 条件同其它 agent。**保护**：原进程仍在线时拒绝（`session_alive`，见 §9.5 与 §4）。
- **唤醒**：`session_resume` 起非交互续接 job（0.118 起沿用源 agent 的 env 与输出投影）。

### 9.5 在线送话命令（deliver_command，路径 C）

会话进程还活着、但不在 tmux、也没有 OPEN turn（没在等回复）时，接管会让两个进程写同一会话。让 agent 自己负责本机进程间传话：每个进程在本机登记并开本地 socket，本机命令 `myagent send --session <sid>` 把话送进活进程；gofer 只负责**在会话登记的执行机上跑这条命令**（与 tmux 注入同一套内部 exec job，tag `relay-deliver`，按**该 agent**过准入门——不需要 `allow_exec`——并带上该 agent 的 `env`，所以数据目录之类的变量与会话进程一致）。

| 配置 | 含义 |
|---|---|
| `deliver_command` | argv 模板，**接在 agent 的 `command` 之后**（与 `session_resume` 同规则）；首元素是绝对路径时视为完整 argv。变量 `{{session_id}}`、`{{text}}`（文本已带 `[gofer web 回复] ` 前缀） |
| `deliver_stdin: true` | 文本走 stdin，argv 里**不得**含 `{{text}}`（推荐：长文本 / 特殊字符不受 argv 限制）。本机 runner 直接支持；worker 需**协议 ≥ v18**（旧 worker 会明确拒绝，不会空 stdin 执行） |

退出码约定：`0` = 已送达活进程；`3` = 会话进程不在线（not running）；其它 = 失败，stderr 是原因。

`POST /v1/sessions/{sid}/deliver` 的优先级（`gofer session say --deliver` 同）：

1. 有 OPEN turn → 作答（`path=turn`）；
2. agent 配了 `deliver_command` 且会话登记了执行机 → 跑命令：`0` → `path=command`，会话置 running，审计行 detail `path=command`；`3` → 记为 `not_running`，**继续往下**；其它 → `deliver_failed:<原因>`（HTTP 502），**直接返回**，不再走 tmux（失败的命令可能已送达，再走一条路会重复送）；
3. tmux 注入（`path=tmux`）；
4. `allow_takeover` 时接管（`path=takeover`）。

**接管存活检查**：走接管前——有 `deliver_command` 的 agent 以本次命令的 exit 3 为「原进程不在」的依据；没有的 agent（claude/codex/omp…）看心跳：最近一次 hook 心跳在 `session.takeover_alive_sec`（默认 120 秒，`0` 关闭）内就拒绝，原因 `session_alive`（HTTP 409）。`ended` / `offline` 的会话不受限。显式「唤醒」（`session resume`）同样适用该检查。

web 抽屉的普通「发送」对**没有 Claude SendMessage 地址**的会话（自研 agent、旧 Claude Code）也会走这条阶梯（命令 → tmux），失败时错误文本以原因码开头（`no_tmux: …`），抽屉据此提示「起新进程接管并发送」；提示文案：在线送达显示「已送达（在线会话）」，`session_alive` 提示「会话进程仍在线…请在原终端继续」。

原因码补充：`not_running`（仅内部，阶梯继续）、`deliver_failed:<原因>`、`session_alive`。

#### codex 会话（`codex queue`，2026-10-08 实测 codex-cli 0.161.0）

codex 自带 `codex queue --thread <会话 UUID|会话名> --message <文本>`：运行中的交互会话会立即取走并作答；会话进程已退出时仍返回成功并排队，下次 `codex resume` 才出现；会话不存在时退出码 1（输出含 `no rollout found for thread id`）。

**内置默认即可用，不再需要包装脚本**：codex agent（内置模板注入的，或你声明了 codex 但没写这两项的；按 agent 名或 command 基名匹配，同 `model_args`）默认带

```yaml
deliver_command: [queue, --thread, "{{session_id}}", --message, "{{text}}"]   # 非绝对路径首元素 = 接在 command 后
deliver_offline_match: "no rollout found"
```

`deliver_offline_match`（Go 正则，需配合 `deliver_command`）：送话命令非 0 退出且 stdout+stderr 匹配时按 exit 3（`not_running`）处理，阶梯继续走 tmux / 接管；不匹配的非 0 仍是 `deliver_failed`。判定在 server 侧（拿到 exec job 的退出码与输出后），会话在远程 worker 上也生效，无协议变化。显式写了 `deliver_command` / `deliver_stdin` 的 agent 按所写为准，不再混入内置的匹配。codex-acp 不带默认。

限制：进程已退出的会话也会得到 `0`（消息排队到下次 resume），所以 web 显示「已送达」不代表对方此刻在线；需要立即回应时以会话状态（离线 / 已结束）为准，改用唤醒。

## 10. 终端工具授权（Claude Code PermissionRequest）

装了新版 hook（重跑 `gofer init hooks`，多出 `PermissionRequest` 条目，`PostToolUse` matcher 改为全部工具）后：

- 每次终端弹授权，会话最后消息变成 `需要授权：Bash \`rm -rf node_modules\``（随后 Claude Code 自己的 "Claude needs your permission" 通知不再覆盖它）。
- 会话**正在等 web**（与 Stop 同一套判据：`on`，或 `auto` 下键盘空闲 / 久无人工输入；但**不受**「监督中不布防」约束——授权对话框本身已卡住终端，在跑 job / 子 agent 时照样上 web、照样等，子 agent 中途启动也不会把它当 `relay_off` 放掉；会话 JSON 的 `permission_wait_reason` / `permission_wait_budget_sec` 即这套判据）时，hook 开一个 `kind=permission` 的决策并长轮询：今天页出「需要授权」卡（允许 / 总是允许：<Claude 给的建议> / 拒绝 / 附原因拒绝），会话抽屉里同样可答。只有会话本人（登记时的 caller）能答；worker、job、管家凭据和 `can_answer` 都不行；作答记审计 `session.permission_answered`。
- 不在等 web（人在键盘前）：只上报，不等待，hook 立即返回。
- 任一方先答为准：web 答了 → hook 输出 decision，终端对话框关闭（显示 `Allowed/Denied by PermissionRequest hook`）；终端先答 → 选 **Yes**：同一调用的 PostToolUse（按 tool_name+tool_input 指纹匹配，即命令跑完时）把 web 卡关成 `released_by=terminal`；选 **No / Esc**：Claude Code 立即 SIGTERM 还在等的 PermissionRequest hook（约 50ms，约 1s 后 SIGKILL），hook 的信号处理先调 `…/permissions/resolve` 收卡（`released_by=terminal`，实测按键后 ~150ms 卡已关），再看 transcript 判断原因写 `hook.log`（`permission denied in the terminal: card released (1 closed)`；超时被杀则为 `hook stopped by the agent CLI (timeout / interrupt)`）；兜底仍有下一次 UserPromptSubmit / Stop / Interrupt / SessionEnd；hook 超时 / 等待预算用完 / 轮询连续失败或 404 / web 答案不可用 → 不输出，终端对话框照旧，且 hook 顺手（best effort，3s 上限）调 `…/permissions/resolve` 把 web 卡关成 `released_by=terminal`，不留一张没人消费的可答卡（`expired` / `relay_off` / 用户回到键盘的释放已在 server 侧收卡，不再调）。
- 脱敏两道：hook 先脱敏（密钥类 JSON 键整值替换、Bearer、key=value / --flag），server 存储前对摘要、参数、建议标签与 `需要授权：…` 会话消息再跑一遍 `secret.RedactString`（另含 `mysql -p<密码>`、`curl -u user:<密码>` / `--user`、URL 里的 `user:<密码>@`），老版或第三方 hook 也存不进明文密码。
- 接口：`POST /v1/sessions/{sid}/permissions`（hook 开）、`…/permissions/resolve`（hook 报终端已处理）、`…/permissions/{id}/answer`（`allow | always:<i> | deny | deny:<原因>`）；hook 用 `GET /v1/sessions/{sid}/turns/{id}` 轮询。通用 `POST /v1/decisions/{id}/answer` 对它返回 409。

### 10.1 实测记录（2026-10-09，Claude Code 2.1.295，容器内临时 `CLAUDE_CONFIG_DIR` + tmux）

| 场景 | 结果 |
|---|---|
| `-p` 非交互，hook 3s 后输出 allow | 命令执行；输入含 `tool_name/tool_input/permission_suggestions/permission_mode`，**无 tool_use_id** |
| `-p`，hook 什么都不输出 / 超时（timeout 5s，sleep 15） | 视为拒绝（`-p` 无对话框可退）；超时时 hook 进程被杀 |
| 交互 TUI，hook 25s 后输出 allow | hook 运行期间**对话框已显示**；hook 返回后对话框关闭，显示 `Allowed by PermissionRequest hook` |
| 交互，hook 运行中人在终端选 Yes | 立即执行；hook **不会被杀**，30s 后输出的 deny 被忽略 |
| 交互，hook 8s 后退出且无输出 | 对话框保持，继续等人 |
| 交互，hook 超时（5s）被杀 | 对话框保持 |
| `Notification`（permission_prompt） | 约在 PermissionRequest 之后 6s 到达，message 为 "Claude needs your permission" |
| `permission_suggestions` 样例 | `addRules`（`Bash(npm --version)`, localSettings）、`addDirectories`（session）、`setMode acceptEdits`（session） |

gofer 端到端（临时 server + 新 hook）：web `always:0`（该次建议是 addDirectories）→ 对话框关闭、命令执行（`updatedPermissions` 原样回传，之后同目录的 `rm` 仍会询问——目录授权不等于命令规则）；`deny:keep e.txt for now` → `Denied by PermissionRequest hook`，agent 转述了原因；终端按 1 → 3s 内 web 卡 `released_by=terminal`；relay off → 只上报、不建卡，通知不覆盖精确消息。

限制：Claude Code 的 `-p` 模式里没有对话框，hook 不答就是拒绝；终端选 Yes 后 web 卡要等命令跑完（PostToolUse）才消失，长命令期间卡仍可见（此时 web 作答已无效）；`总是允许` 只提供 Claude Code 给出的建议项（最多 2 个按钮）；仅 claude（及 generic 方言若发 PermissionRequest）支持。

### 10.2 终端选「No」的收卡信号（2026-10-09，Claude Code 2.1.295，gofer-kcw0）

方法同 10.1：临时 `CLAUDE_CONFIG_DIR` + 临时项目 + tmux 驱动交互 `claude`，先用一个记录全部事件（PermissionRequest / PreToolUse / PostToolUse / PostToolUseFailure / Notification / Stop / UserPromptSubmit / SubagentStop / SessionStart / SessionEnd）并捕获信号的脚本 hook，再用临时 gofer server + `gofer init hooks` 端到端。

| 观察 | 结果 |
|---|---|
| 选 4. No / Esc 后的 hook 事件 | **一个都没有**：无 PostToolUse、无 PostToolUseFailure、无 Stop（回合以 `Interrupted · What should Claude do instead?` 结束）、无新 Notification |
| 仍在运行的 PermissionRequest hook | 按键后约 50ms 收到 **SIGTERM**，约 1–1.5s 后被 SIGKILL（trap 里的循环只活到 +1.0s） |
| transcript | 按键后约 10ms 记一条 `type=user` 的 `tool_result`（`is_error:true`，内容 `The user doesn't want to proceed with this tool use…`），同一条带 `toolUseResult:"User rejected tool use"`、`toolDenialKind:"user-rejected"`；落盘约在按键后 150ms |
| 选 1. Yes | hook 不被杀；PostToolUse 在命令跑完后到达并收卡 |

结论：最早可靠的信号就是 hook 自己收到的 SIGTERM——`gofer hook claude` 的信号处理（`abortReportOnSignal` → `ReportPermissionAbort`）原本就会收卡，此前「要等下一次输入 / Stop」的说法没有实测依据。本次只补了日志：先收卡（抢在 SIGKILL 前），再在 600ms 内轮询 transcript 区分「终端拒绝」与「超时被杀」。gofer 端到端实测：No → 按键后 ~170ms 卡 `EXPIRED released_by=terminal`，hook.log `permission denied in the terminal: card released (1 closed)`；Esc → ~140ms 收卡。无需新增 hook 条目（`PostToolUseFailure` 对拒绝不触发）。
