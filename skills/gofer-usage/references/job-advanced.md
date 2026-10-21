# job 进阶用法

> SKILL.md 只讲最常用的 `gofer job run` / `logs` / `watch`。这里是其余 job 能力：续跑、持续会话、只读、模型、预算、验收、并行、故障转移、模板、日志等。完整 flag 以 `gofer job <子命令> --help` 为准。

## 目录

- [等待方式与超时](#等待方式与超时)
- [job 结束时通知提交会话（--notify）](#job-结束时通知提交会话--notify)
- [job 状态速览](#job-状态速览)
- [agent 的两种启动方式与交互 pty job](#agent-的两种启动方式与交互-pty-job)
- [ACP 持续会话（--session）](#acp-持续会话--session)
- [中断后续跑（job resume）](#中断后续跑job-resume)
- [继承旧会话开新会话（--from-session）](#继承旧会话开新会话--from-session)
- [只读 job（--read-only）](#只读-job--read-only)
- [指定模型（--model）](#指定模型--model)
- [预算熔断（--max-tokens / --max-cost / --max-turns）](#预算熔断)
- [agent 软预算（--agent-budget-tokens / --agent-budget-cost-usd）](#agent-软预算)
- [验证步骤（--verify）](#验证步骤--verify)
- [人工验收、验收标准与改动范围](#人工验收验收标准与改动范围)
- [并行：--worktree 与目录锁](#并行--worktree-与目录锁)
- [交付：合并 / 推分支 / 开 PR](#交付合并--推分支--开-pr)
- [停滞看门狗、故障转移与重试](#停滞看门狗故障转移与重试)
- [任务书模板（-t / --var）](#任务书模板-t----var)
- [提交与 diff 采集](#提交与-diff-采集)
- [工具调用审批（pending_interaction）](#工具调用审批pending_interaction)
- [日志：stdout / stderr 的分工](#日志stdout--stderr-的分工)
- [规则、skill 与角色](#规则skill-与角色)
- [脱敏、秘密扫描与删除](#脱敏秘密扫描与删除)
- [派活给主机 agent 的注意事项](#派活给主机-agent-的注意事项)
- [在 job 上留评论](#在-job-上留评论)

## 等待方式与超时

- `--sync`：server 阻塞到终态再返回；默认只等约 30 秒，`--wait-timeout <秒>` 放宽（上限 60 秒）。等不到时转异步返回、job 继续跑。返回里只有状态，输出用 `gofer job logs <id>` 取。
- `--wait`：客户端轮询到终态（适合比 60 秒长、又想阻塞等的场景）。
- 异步（默认）：立刻返回 job id，再 `gofer job watch <id>` 跟随或 `gofer job show <id>` 轮询。
- `--timeout <秒>` 限执行时长，有上限（server `max_job_timeout_sec`，默认 3600，项目可用 `max_timeout_sec` 覆盖）。超出会被截到上限并打印 `warning: --timeout … exceeds the project ceiling`，`job show` 显示生效值。超 1 小时的活：请管理员提高上限，或拆成多个 job。
- **agent 知道自己的时限**：有时限的 job 在环境里能读到 `GOFER_JOB_TIMEOUT_SEC`（生效秒数，已截到上限）与 `GOFER_JOB_DEADLINE`（RFC3339，带时区）；两者都从**真正开始执行**那一刻算起（排队等并发槽位 / 目录锁的时间不计入），排队很久的 job 也不会误以为只剩几分钟。一次性的 cli / acp agent job（非 exec）的 prompt 末尾还会多一行「本 job 时限 N 分钟，从开始执行算起（排队等锁的时间不计入）；截止时刻与环境变量 `$GOFER_JOB_DEADLINE` 一致，也可运行 `gofer job show $GOFER_JOB_ID` 查看。接近时限时先提交已完成的部分，汇报里写清未完成项」。续接 job 按它自己生效的时限算（继承源 job 的超时），持续会话（`--session`）带 `max_session` 时按它算；没带时只注入每一轮的 `GOFER_JOB_TIMEOUT_SEC`，不注入 `GOFER_JOB_DEADLINE`；完全没有时限的 job（如没显式给 `--timeout` 的交互 pty job）两个变量都不注入。`gofer job show` 的 `queued_at` / `started_at` 与「排队 X」可看出排了多久、跑了多久。
- `-f task.md`：任务文件（YAML frontmatter 给参数 + 正文即 prompt）。
- `gofer job set <id> --title "…"` 补改标题（`--title ""` 清空）。

## job 结束时通知提交会话（--notify）

```bash
gofer job run -p <p> -a claude --notify --prompt "…"          # 结束时通知「提交这个 job 的会话」
gofer job run -p <p> -a exec --notify-session <sid> -- <命令>  # 结束时通知指定会话
```

- **`--notify`**：job 到终态（done / failed / timeout / cancelled / rejected，以及停在 `needs_review` 时）经**传话人**（web「发消息」同一套）给目标会话发一条**系统通知**，不必等那个会话自己停下来才发现。
- **默认关闭**，因为每条通知都要起一次传话人进程（本机常驻进程或一次性 job）——长 job、要离开一会儿时才加。
  - 例外：在 agent 会话里提交的 `--hold` 待批 job 默认通知该会话（不论 job 的项目 / runner 与会话是否一致），`--no-notify` 关掉；见 [hold-approval.md](hold-approval.md)。
  - 这个**隐式默认**通知若被服务端拒绝（例如会话属于另一个调用方），CLI 不让整条提交失败：去掉通知、打印一行警告、再提交一次；显式给了 `--notify` / `--notify-session` 的仍按原样报错，其它错误不重试。
- 目标会话：`--notify-session <sid>` 显式指定（隐含开启）；`--notify` 用提交方——有 `--source-session-id` 就用它，否则从 `$GOFER_SESSION_ID` / `$CLAUDE_CODE_SESSION_ID` / `$CODEX_THREAD_ID` 认出当前会话（会话只是通知地址，**不按 job 的项目过滤**，会话属于别的项目也能识别）；都认不出来时**直接报错**（不会静默不通知）。提交成功后 CLI 会打印一行 `notify: job 结束时通知会话 <sid>`。
- 通知内容一行，以 `[gofer 通知] [gofer job 完成]` 开头（`[gofer 通知]` 是 gofer 系统通知的固定前缀，与用户的 `[来自 web，…]` 区分开），含 job id、标题、状态、退出码、耗时、agent 自述的收尾原因（`wrap_up=`，有则带）、`gofer job show/logs <id>` 的提示，并注明「这是 gofer 系统通知，不是用户消息」；消息来源（operator）记为 `gofer`。
- 与 `session watch` / Stop hook 的关系：送达**成功**后对应的 job watch 被消费，Stop hook 与下一次输入的补发**不会再发一次**；送达失败则保留 watch，仍由补发兜底（两边都不会漏）。同一个 job 只通知一次，server 重启也不会重发。
- **目标会话失效时**：你直接指定的 `--notify` / `--notify-session` 若指不到会话（不存在、属于别的 caller）或根本没给，**提交前就会报错**；而 **gofer 自己发起的 job**（定时任务 / plan 派发 / workflow 步 / wakeup / 自动续跑 / rerun）一律**丢弃通知、照常执行**，并在 job 时间线记一条 `job.notify_dropped` 事件（写明目标与原因）——没人过问的活不应该因为通知找不到收件人而失败。
- HTTP / MCP 同名可选字段 `notify` / `notify_session`（见 <https://github.com/inhere/gofer/blob/main/docs/reference/http-api.md>）；plan 级开关 `plan create --notify` / `plan set <plan> --notify on|off` 让该 plan 派发的 job 都通知它的主 Agent 会话（没绑定时不通知任何人，`plan show` 会提示），见 [plans-workflows.md](plans-workflows.md)。

## job 状态速览

| 状态 | 含义 |
|---|---|
| `queued` / `waiting_dir` | 排队；`waiting_dir` = 在等目录锁（`job show` 打出持有者） |
| `running` | 执行中 |
| `awaiting_input` | ACP 持续会话一轮结束，等下一条 `job say` |
| `pending_interaction` | 工具调用等人批准（见「工具调用审批」） |
| `needs_review` | 正常完成，等人验收 |
| `awaiting_approval` | `--hold` 待批，人批准才执行（见 [hold-approval.md](hold-approval.md)） |
| `paused_for_update` | 一键更新在等机器空闲期间停住的新 job；更新完成 / 失败 / 取消时自动开跑（不用重新提交、不用批准） |
| `recovering` | 执行它的 worker 断线，server 在恢复窗口内（默认 120 秒）等它重连；**不要重派** |
| `done` / `failed` / `timeout` / `cancelled` / `rejected` | 终态 |

`gofer job list --status <状态>` 过滤；`--all` 连内部传话 job 一起列。

## agent 的两种启动方式与交互 pty job

- agent 定义里 `args` 是批处理 argv（`job run`），`interactive_args` 是 pty argv（`job run --interactive`，`[]` = 裸 TUI）。`gofer agent list` 的 batch / interactive 两列就是能力位。
- 被拒时的含义：`has no batch mode` = 该定义只写了 `interactive: true`，只能 `--interactive`；`has no interactive mode` = 没写 `interactive_args`；`project "p" does not allow interactive jobs` = 项目没开 `allow_interactive`。
- `--interactive --prompt "…"`：prompt 作为 pty 的首条输入（自动补回车）；不带就是裸 TUI 等人 attach。`--cols/--rows` 设初始大小。
- 交互 job 的输出写进去 ANSI 的文本转录 `pty.txt`（默认保留尾部 4MB，`pty.transcript_max_bytes` 可调），`gofer job logs` 自动读它。
- 会话 id：带 `session_inject` 的 agent（内置 claude）提交时就知道 id；其它 agent 在退出横幅或会话文件里捕获。拿到 `session_id` 后可 `gofer job resume <id>` 重新进入。取消时 gofer 先发 agent 配的 `exit_keys` 让 TUI 打出退出横幅再收尾。相关配置见 [server-config.md](server-config.md)「agents」。

## ACP 持续会话（--session）

同一个 ACP agent 进程里多轮对话（先用 `gofer agent list` 找 `type=acp-agent`）：

```bash
gofer job run -p <project> -a <acp-agent> --session \
  --prompt "第一轮消息" --timeout 90 --idle-timeout 1800 [--max-session 7200] [--lock <子目录> | --worktree]
gofer job say <job-id> "下一轮消息"      # 同一个 job 再发一轮，不产生新 job
gofer job end <job-id>                   # 结束会话并释放目录锁
```

- 状态：一轮结束停在 `awaiting_input`，`say` 回到 `running`；`end` 或空闲到期为 `done`，`cancel` 为 `cancelled`，agent 进程异常退出为 `failed`。
- 超时：`--timeout` 限每一轮；`--idle-timeout` 限等下一条消息（0 = 1800 秒）；`--max-session` 限整个会话（0 = 不限）。`--prompt` 可省，直接等第一条 `say`。
- 占用：整个会话期间（含 `awaiting_input`）都持有目录锁并占 agent 的并发名额，尽量配 `--lock <子目录>` 或 `--worktree`，用完及时 `end`。
- 在哪跑：server 本机或较新的 worker；太旧的 worker 提交时被拒并提示升级。`--interactive` 与 `--session` 互斥，非 acp-agent 带 `--session` 被拒。server 重启 / worker 断线后用协议的会话载入恢复，agent 不支持就 `failed`，改开新会话。
- 内置 `claude-acp` / `codex-acp` 用 `npx -y @agentclientprotocol/claude-agent-acp`、`npx -y @agentclientprotocol/codex-acp` 适配器（在 config 里自定义过的，确认用的是这两个包）。codex-acp 认证读 `~/.codex/auth.json` 或 `CODEX_API_KEY` / `OPENAI_API_KEY`，`CODEX_PATH` 可指向别的 codex。
- web 工作台里 ACP 会话的输入直发同一 job；`say` / `end` 与 `cancel` 同一套权限。
- 偶尔追问又不想占锁和名额：用不带 `--session` 的 job + `job resume`（一轮一个 job）。

## 中断后续跑（job resume）

codex / claude 因供应商容量错误、网络抖动或超时中断时，**不要重派整份任务书**（新会话要重读上下文），续跑同一个会话：

```bash
gofer job show <源 job-id> | grep session_id     # 有 session_id 才能续
gofer job resume <源 job-id> \
  --prompt "上一次运行因 <原因> 中断。先 git status / git log --oneline -5 判断进度，只完成剩余项，不要重做已提交部分；汇报格式同前。"
```

- 前提：源 job 已终态、捕获到 `session_id`、agent 有续接模板（内置 claude / codex / suga）、同一 runner。产生**新 job id**。
- plan：默认**继承**源 job 的 plan（提交时的 `--plan`、plan 自动派发、`gofer plan attach` 都算）；`--plan <plan-id>` 挂到别的 plan，`--no-plan` 不挂任何 plan（同时丢弃继承的 checklist 挂接，因为 todo 属于创建它的那个 plan）。`gofer job show` 会列出 `plan:` / `todo:`。
- acp-agent 的 resume 不需要模板：新开一个持续会话并载入源会话，`--prompt` 可省（直接进 `awaiting_input` 等 `say`）。agent 不支持载入会话（或配了 `acp.load_session: false`）时报不支持。
- `--mode session|interactive|batch` 显式选续接形态：`session` = 持续 ACP（需本机或较新 worker）；`interactive` = pty（需项目 `allow_interactive` 且 agent 有交互续接模板）；`batch` = 一次性，必须带 `--prompt`。
- `--agent` 只能换**同会话族**的 agent（共用同一份会话存储）：`claude` ↔ `claude-acp`，`codex` ↔ `codex-acp`，`suga` ↔ `suga-acp`。ACP agent 没有同族 CLI 时不能续成 CLI job。
- 环境：续接按源 agent 处理输出与环境，叠加顺序（低到高）`env_files` < 源 agent 的 `env` < 源 job 的 `env` < 本次 `--env K=V`。`--env` 的值会随新 job 记录保存，**别放密钥**（密钥用 `--secret <名字>`，见 [server-config.md](server-config.md#job-密钥--secret)）。worker 上的续接只继承源 agent 的 env。
- `--model`、`--max-*` 覆盖源 job 的设置；只读、budget、model、plan 默认沿用源 job。
- `--lock <子目录>` 覆盖继承的目录锁：续接默认照抄源 job 的 `lock_paths`（没有就是锁整个 cwd），而持续会话一旦落在项目根就**锁住整个项目根**，别的子目录 job 只能排队等锁（表现为新建 job 停在 `waiting_dir`）。只想动某个子模块时 `gofer job resume <id> --lock gookit/ini` 把锁换到那个子目录（可重复）；声明锁也会同时要求独占（源 job 是 `--shared-dir` 时同样生效）。cwd 不能改：ACP 的 `session/load` 必须在同一 cwd 加载会话，要换目录请新开 job。
- 结束通知默认**继承**：源 job 用 `--notify` / `plan create --notify` 开了通知时，续跑结束也通知**同一个会话**（续跑的 request 没有源会话可依靠，所以目标会话被显式带过来）；不想被通知加 `--no-notify`。
- 命中瞬时错误时 gofer 会自动续跑一次（`auto_resume_max`）。哪些错误算瞬时、哪些 agent 默认带内置模式（claude / codex / omp / suga），见 [server-config.md](server-config.md#故障转移与健康度)。 agent 自带 `error_kind` 时以它为准：只有 `provider_transient` 续投，额度 / 被拒 / 非法响应与未知取值都不续投（额度类错误会在 job 错误里写出重置时间）。
- `job rerun <id>` = 原请求重提，**新会话**，agent 重读全部上下文。

## 继承旧会话开新会话（--from-session）

`job run --from-session <会话id>`：开一个**新**会话，继承源会话的上下文（源会话只读、不被续写）。与 `resume`（接着跑同一个会话）互补：上下文太长、想换方向但保留结论时用。

- 只对配了 `from_session_args`（含 `{{from_session}}`）的 cli-agent 有效。内置 claude 默认已带 `[--resume, "{{from_session}}", --fork-session]`（分叉源会话）；其他支持从旧会话分叉的 agent 自己配，如 `from_session_args: [--from, "{{from_session}}"]`。显式配置优先于内置。
- 新会话 id 照常由 gofer 注入或捕获；`job show` 的 `session_id:` 是新会话，`from_session:` 是源会话。
- 被拒（400）：agent 没配该片段、exec / acp-agent、`--session`、与 resume 混用。源 id 若是 gofer 记录过的会话，须同 agent 或同会话族。
- web：job 详情页（已结束且有 `session_id`）和会话抽屉有「从此会话新开」按钮。

## 只读 job（--read-only）

审查 / 分析类任务不让 agent 改文件：

```bash
gofer job run -p <project> -a codex --read-only --prompt "只做审查：列出这次改动的问题，不要修改任何文件"
```

- cli-agent 追加 agent 的 `read_only_args`（内置 codex `-s read-only`、claude `--permission-mode plan`、suga `--read-only`）；acp-agent 用 `acp.modes.read_only` 切到只读模式，agent 不支持就失败（不会在可写模式下跑完）。
- exec agent 与没配只读模式的 agent 提交即被拒，错误会点名要配哪个键。
- 只读随 job 记录（`job show` 的 `read_only: true`、列表 `[ro]`），resume 继承只读；想改成可写只能新开 job。
- 只读 job 不抢目录锁；可写 job 等锁时显示 `waiting_dir`，见下「目录锁」。

## 指定模型（--model）

`job run --model <id>`（也可写在任务书 frontmatter、`plan add-todo|set-todo --model`、web 表单）：

- cli-agent 用 agent 的 `model_args`（内置 claude `--model {{model}}`、codex `-m {{model}}`、suga `--model {{model}}`）；acp-agent 经协议选模型，agent 不支持该 id 时 job 失败并列出可选值。
- 建议写**完整模型 ID**（CLI 的短别名不一定生效）。不指定 = agent 自身默认、argv 不变。
- 没有 `model_args` 的 agent、exec agent 带 `--model` 提交即 400。resume 沿用源 job 的 model，`--model` 覆盖。

## 预算熔断

给 job 设花费上限，越线即终止：`--max-tokens 50000|50k|1.5m`、`--max-cost 2.5`（美元）、`--max-turns 20`（模型请求次数）。同名 flag 也可用于 `plan add-todo|set-todo` 与 `job resume`，任务书 frontmatter 写 `budget:`。

- 默认值层级：agent `budget:` < 项目 `budget:` < 任务书 < 请求，按维度覆盖；各维 0 / 不给 = 不限。`job show` 的 `budget:` 行显示上限与已用。
- 超限：杀进程树，job `failed`、失败类别 `budget`（不自动续投、不故障转移、不重试），事件 `job.budget_exceeded` 在默认通知集。
- 计量来源：claude stream-json、omp、配了 `ndjson_usage_path` 的 ndjson agent、acp-agent、codex。codex 只在结束时报用量、claude 只在结尾报费用，所以这两项是事后判定（job 已跑完但仍判超预算失败）。
- 用量口径：omp 每次模型请求各报一份，gofer 逐次相加；`ndjson_usage_path` 指向的对象应是本次 run 的**累计值**，以最后一次出现的为准——agent 每轮都输出累计值时，预算能在运行中途生效，只在结尾输出则只能事后判定。usage 对象里带 `tool_calls`（工具调用次数）时一并记录，`job show` / web 显示为 `tools N`。
- exec、交互 pty、文本输出的 cli-agent **显式**带预算提交即 400；只靠 agent / 项目默认值合并进来的预算对它们不生效（不报错）。
- 目标是较旧的 worker 时，带预算的提交被拒并提示升级。

### agent 软预算

`--max-*` 是 gofer 杀 job 的**硬**上限；另有一层**软**预算交给 agent 自己执行：`--agent-budget-tokens 200k`、`--agent-budget-cost-usd 2`（`plan add-todo|set-todo`、`job resume`、MCP / HTTP `budget` 里的 `agent_tokens` / `agent_cost_usd`、任务书 `budget:` 同名字段）。agent 用到这个量就自己收尾并给出汇报，job 正常结束（`done`），不被杀。

- 只对在配置里声明了 `agent_budget` 映射的 agent 有效（内置 `suga`：argv `--budget-tokens` / `--budget-cost-usd`，同时映射环境变量 `SUGA_BUDGET_TOKENS` / `SUGA_BUDGET_COST_USD`，cli 走 argv、acp-agent 走环境变量）。**没有映射的 agent 带软预算提交即 400**（`does not support budgets`），不会静默忽略；只靠 agent / 项目默认值合并进来的软预算对它们不生效（不报错）。
- 软预算要设得**低于**硬上限：硬上限到了 gofer 直接杀进程，agent 来不及软收尾。
- resume 沿用源 job 的软预算，显式 flag 按维度覆盖；目标 agent 不支持时，继承来的被丢弃，显式给的报 400。较旧（协议 < v24）的 worker 会拒收带软预算的 job。
- 用尽的判定来自 agent 自己上报的快照（acp 的 `usage_update._meta` 预算字段、stream-json 用量 / result 行的 `budget` 对象）：`job show` 打 `agent_budget: 51k/50k tokens (turn) - exhausted, agent wrapped up`，并把 `wrap_up` 标成 `budget`（预算耗尽收尾，区别于步数上限 / 时限，也区别于硬上限的 `failure_class=budget`），时间线有 `job.agent_budget_exhausted` 事件，web 详情的用量区显示「预算 已用 / 上限」和「预算耗尽收尾」徽标。agent 没报快照就没有这些（gofer 不推算）。

### agent 自己的收尾原因（`wrap_up`）

预算熔断是 **gofer** 杀 job（status=failed）；agent 自己撞上限则是 agent **自己**收尾：`suga` 的 stream-json 在步数 / 用量 / 时限到顶时 result 行带 `wrap_up=max_steps|max_tokens|timeout`，软预算用尽记为 `wrap_up=budget`（正常回答不带），旧版 suga 撞步数上限时最后一行是 `{"type":"error","error":"max steps exceeded: 100"}`，也算同一种。

- gofer 把这两种都识别为 `wrap_up`，写在 job 上：`job show` / `job review` 打一行 `wrap_up: 步数上限（agent 自己收尾，任务可能未完成）`，web 详情有「收尾原因」，`GET /v1/jobs/{id}` 带 `wrap_up` 字段。
- 这种 job 的 status **仍是 done**、汇报也在（汇报本身是真实产出），所以不看这一行就与做完的任务分不开。要续跑用 `gofer job resume <id> --prompt "…"`。
- 只识别 agent 自己说的，gofer 不推算：流里没有这个信息就不标（别的 agent 方言多数不报）。

## 验证步骤（--verify）

`job run --verify '<命令>'`：agent **正常结束**后在同一台执行机、同一 cwd / env 跑这条命令（按 shell-words 拆分，不经 shell；要 shell 就写 `bash -lc '…'`），独立超时 `--verify-timeout`（缺省项目 `verify_timeout_sec`，再缺省 600 秒）。

- 通过 → job 按原逻辑结束；失败 / 超时 → job `failed`（不触发自动续投与故障转移）；开了 `--review` 则停在 `needs_review` 留人裁决；agent 自己失败时不跑。
- 输出合并写进本 job 的 stderr（带横幅），`job show` 打 `verify: failed (exit 1, 12.3s)`。
- 需要项目 `allow_exec`；不想跑项目默认的验证用 `--no-verify`。

## 人工验收、验收标准与改动范围

```bash
gofer job run … --review                                 # 正常完成后停在 needs_review 等人验收
gofer job run … --acceptance $'- 测试通过\n- 文档已更新'  # prompt 末尾追加「## 验收标准」
gofer job run … --scope 'src/api/**,web/src/x.vue'       # 声明改动范围（可重复）
gofer job review <id> [--tail N] [--diff] [--json]   # 验收材料一屏
gofer job findings <id> [--create-issues] [-p 2] [--tag discovered]
gofer job accept <id> [--note "…"]                       # needs_review → done（只有人能做）
gofer job reject <id> --note "…" [--resume]              # → rejected；--resume 以 note 为 prompt 续投
gofer job list --status needs_review                     # 谁在等验收
```

- `reject --resume` 会新建续接 job，除验收权限 `job.review` 外还要 `job.submit`；缺 `job.submit` 返回 403，退回不会被记录。
- 开 review：`--review`、项目 `require_review: true` 或 workflow 步骤 `review:`。`needs_review` 不能 `cancel`（用 reject），也要先裁决才能 resume。MCP 只有 reject 工具，没有 accept。
- `--worktree` job 被 `reject --resume` 退回返工时，续接 job 在**同一个 worktree 和分支**上接着做，并沿用源 job 的交付设置（`--deliver` 等）与验收要求；返工后 accept 即按原方式交付整条分支，PR 标题与任务说明仍取最初的任务，不是退回理由。只限 server 本机执行的 job。
- `job review` 内容：状态、验收标准、提交、`diff --stat`、越界文件、**审阅提示**、汇报尾部（默认 60 行）、「发现但不碰」；`--diff` 追加完整 patch。web 的验收面板是同一份材料（见 [web-console.md](web-console.md)「验收台」）。
- 「审阅提示」（review hints）是服务端现算的三小节，空则整块省略：**删除热点**（该 job 的 diff 里删除 ≥10 行的文件，按删除行数取前 5）、**被删注释**（diff 删掉、同文件新增行里没有原样出现的注释行，最多 20 行，带旧文件行号）、**验证结果行**（从汇报里抽出的 `ok  <pkg>` / `FAIL` / `--- FAIL:` / `PASS` / `*_EXIT=n` / `exit=n` / `tsc=n` / `Test Files … passed` / `Tests … passed`，最多 30 行）。作用是先看哪里可疑（顺手删注释、大段删除、汇报里到底贴了哪几行验证），不是裁决依据。`--json` 打 `{id, status, review_hints}`，字段见 `docs/reference/http-api.md`。
- 验收标准随活走：非 exec 的 agent job，prompt 末尾追加「## 验收标准」并要求汇报逐条答「满足 / 未满足 / 无法验证 + 依据」；exec job 只记录、照样在面板显示。plan todo 用 `--acceptance` 或 `--acceptance-from-issue <issue-id>`（从当前仓库 tracker 读该 issue 的验收标准）。
- 交付约定：项目 `scope_discipline: auto|on|off`（默认 `auto` = plan todo 派出的、要人验收的、带验收标准或 scope 的 job；`on` = 所有非 exec 批处理 agent job；交互与 `--session` 不加）会追加「## 交付约定」：只改与任务相关的内容，范围外问题写进汇报的「## 发现但不碰」（`- <位置>：<问题>`），发现注入的记忆与实际不符用 `gofer memory flag` 上报。单次关闭 `--no-scope-discipline`。
- 越界文件只是提示，不挡 accept；已提交的文件也算，共享 checkout 里他人同期的提交可能造成误报。
- 经验候选：项目 `knowledge_capture: auto|on|off`（口径同上）开启时，「## 交付约定」还要求把可复用经验写进「## 可复用经验」。交付时记成候选，**不自动入库**：`gofer memory candidates` / `accept <id> --key k` / `reject <id>`，见 [tracker.md](tracker.md)「经验候选」。
- 裁决前自己核验（跑测试、看 diff）——汇报是 agent 自己说的。

## 并行：--worktree 与目录锁

**--worktree**：多个 agent job 在同一 checkout 里并行改代码会互相踩（`.git/index.lock` 残留、互相覆盖）。加 `--worktree` 让每个 job 在 `<仓库顶层>/tmp/gofer/wt/<job-id>` 的独立 worktree 里跑，提交落在分支 `gofer/<job-id>`，主 checkout 不动。`--cwd` 仍相对项目根；`--worktree-base <ref>` 指定基线。项目可设 `worktree_default: true`。任务书里提醒 agent：不要切分支、不要 push。

位置与分支可以按项目配置改（模板 `{name}` / `{job_id}`），单个 job 也能覆盖：

```bash
gofer job run -p <project> -a <agent> --worktree --worktree-name <名> --branch <分支名> --prompt "…"
# 项目配置（见 server-config.md「项目」）：
#   worktree:
#     dir: ".worktrees/{name}"     # 默认 tmp/gofer/wt/{job_id}
#     branch: "{name}"             # 默认 gofer/{job_id}
#     links: [web/node_modules]    # 从主 checkout 链接进 worktree 的目录
```

- `--worktree-name` 只允许字母、数字、`.`、`_`、`-`；没给时 `{name}` = job id，所以默认布局不变。
- 同名 worktree 目录或同名分支已存在时**提交失败**（不复用：两个 job 共用一个 worktree 就会抢同一个 index）。
- `--branch` 覆盖项目的 `worktree.branch` 模板。
- `links`：worktree 建好后，在执行机上从主 checkout 的同路径建链接（Windows 目录联接、其它平台符号链接），让 job 用上没人提交的依赖目录。源不存在、worktree 里已有同路径、建链接失败都只记一条事件（`job.worktree_links`）并继续执行，不会卡住 job。**实际建成的链接会记在 job 上**（`worktree_links`）；`job worktree rm` / `--cleanup` 按这份记录逐个删链接，再扫一遍 worktree 顶层与已知路径删掉残留的链接 / 联接，最后才删 worktree —— 事后把某条 `links` 从配置里删掉也不会顺着残留链接删掉主 checkout 里的内容。
- worker 上的 job 由那台 worker 建 worktree 与链接（它执行、它拥有 checkout）。

```bash
gofer job worktree ls [-p <project>]                       # 分支 / 领先提交 / 是否脏 / 是否已合并
gofer job worktree merge <job-id> [--squash] [--cleanup-others] \
      [--todo-done [--note <说明>]] [--comment <评论>] [--cleanup]
gofer job worktree rm <job-id> [--force] [--delete-branch]  # 清理；分支默认保留
```

`rm` 的边界：**正在被 job 使用**（含自动续接出来的新 job —— 它承接源 job 的同一个目录）时一律拒绝（409）并指明是哪个 job，`--force` 也不覆盖；git 已经把登记和文件清掉、只剩目录删不掉（Windows 上有进程占着）时照常删完分支并返回成功，用 `warning:` 列出残留目录；目录还被锁住什么都没能删时返回 409 说明原因，不留「目录在、分支没删」的半状态。

```bash
gofer job wait <id>... [--any] [--timeout 30m]             # 等到终态，输出状态 / 退出码 / 标题
```

合并规则：只支持 server 本机 runner（远程 worker 的返回 409）；主 checkout 不能有未提交的**已跟踪**改动且须在命名分支——但改动若只在 `.gofer/tracker/` 下则放行（其它文件会在 409 里列出文件名）；`.git/index.lock` 被占用时每秒检查、最多等 60 秒，超时 409 且不删锁；冲突时自动 abort 复原并列出冲突文件，**不做任何收尾**；从不 push。

合并成功后的收尾（都可省略，失败只给 warning，不影响已完成的合并）：

- `--todo-done [--note <说明>]`：把该 job 所属 plan todo 标 done，说明默认 `<分支> 合入 <主线短 sha>`；job 不属 todo 时忽略并提示。
- `--comment <评论>`：在 job 上留一条评论（按当前凭据身份，`@` 照现有规则处理）。
- `--cleanup`：删依赖链接 → 删 worktree → `git branch -d` 删已合并分支。

`job wait` 用来代替手写轮询：默认等到全部 job 终态，`--any` 任一终态即返回；`--timeout` 到点非零退出并列出还在跑的 job。

**目录锁**：同一工作目录（含祖先 / 子目录）上不许两个**可写 agent job** 同时跑，后来的停在 `waiting_dir`（不占执行位，可取消），`job show` 打出持有者。exec 与只读 job 默认共享。

- `--lock <子目录>`（可重复）把锁收窄到子目录——顶层目录包含多个仓库时优先这样做；项目开 `dir_lock_mode: repo` 后，cwd 下有嵌套仓库的可写 job 必须声明 `--lock` 或显式 `--shared-dir` / `--exclusive-dir`，否则提交被拒并列出可选仓库；**续接**（`job resume --lock`）同样能换锁，见上「中断后续跑」。
- `--shared-dir` 放弃独占（自担风险）；`--exclusive-dir` 强制独占（exec 也独占）。`--worktree` job 不取锁。
- `--lock-wait <秒>` 限等锁时长（0 = 不限，是否允许由 server 决定）；超时报错写出实际锁路径与持有者。串联多个长 job 时显式设值。
- agent 的 `max_concurrent` 另限单个 agent 的并发，超出停在 `queued`。

## 交付：合并 / 推分支 / 开 PR

`--worktree` job 结束后可以自动把分支交出去，按项目配置 `delivery` 或 job 的 `--deliver`：

| 方式 | 做什么 |
|---|---|
| `merge` | 合进仓库主 checkout 当前分支（同 `job worktree merge`：主 checkout 要干净、冲突就中止复原）；`push_base: true` 时再推基线分支 |
| `push` | 把分支推到 remote（分支名按 `delivery.branch` 模板，默认 `gofer/<job-id>`），不开 PR |
| `pr` | 推分支并开 PR / MR：GitHub（含企业版）、GitLab（含自托管）、Gitea / Forgejo，按 remote 地址识别平台 |

- **什么时候**：没开验收的 job 在 done 且有新提交时交付；开了验收的在 Accept 后交付（`gofer job accept <id> --no-deliver` / web 勾「本次不交付」则不交付）。没有新提交、失败或取消的 job 不交付。
- **推送许可**：`push` / `pr`（以及 merge 的 `push_base`）要求项目配置 `delivery.allow_push: true`，job 上不能临时打开；从不 force push。
- **平台 token**：只在执行交付的机器上读环境变量（GitHub `GITHUB_TOKEN` / `GH_TOKEN`，GitLab `GITLAB_TOKEN`，Gitea `GITEA_TOKEN` / `FORGEJO_TOKEN`，或 `delivery.token_env` 指定的名字）；GitHub / GitLab 没有时尝试已登录的 `gh` / `glab`。没有 token 时只推分支，结果里给出平台「新建 PR」的链接。推送用 git 自己的凭据。
- **代理**：执行交付的机器只能经代理访问平台时，配 `delivery.proxy`（推送与平台 API 都走它）；不配时推送用 git 自己的 `http.proxy`，平台 API 也跟随 git 对该 remote 的 `http.proxy`，再退到 `HTTPS_PROXY` 等环境变量。
- **PR 内容**：标题取 job 标题，正文带任务说明（提交时写的 prompt，不含 gofer 注入的规则与约定段）、验收标准、提交列表、改动统计、verify 结果、用量、验收结论和 job id；回到 gofer web 的 job 链接默认不放（web 地址常是内网，PR 可能公开），要放就开 `delivery.job_link: true`。同一分支已有打开的 PR 时直接复用。仓库公开时注意 prompt 本身别写内部信息。
- **worker 上的 job**：在那台 worker 上执行（内部 job，`--all` 才列出），那台机器要有推送凭据和平台 token。
- **预览**：`job deliver <id> --dry-run` 只打印将要用的模式、分支、目标分支、PR 标题与正文，不执行、不记录；`needs_review` 的 job 也能预览，accept 前先看一眼 PR 会写什么。
- **结果**：`job show` 的 `delivery:` 行、web job 详情的「delivery」（含 PR 链接，done 后可选方式手动交付 / 重试）；交付失败会通知（`job.delivery_failed`）。

```bash
gofer job run -p <project> -a <agent> --worktree --deliver pr --prompt "…"   # 本次覆盖项目的交付方式
gofer job deliver <job-id> [--mode merge|push|pr] [--draft|--ready] [--branch <名>] [--dry-run]   # 交付 / 重试；--dry-run 只预览，不执行
gofer job accept <job-id> --no-deliver                                         # 通过但不交付
```

配置见 [server-config.md](server-config.md#交付worktree-job)。

## 停滞看门狗、故障转移与重试

- **停滞**：非交互 job 若 N 秒内 stdout / stderr 都没有新输出，判停滞、杀进程、`failed: stalled: no output for Ns`；该错误按瞬时错误处理，自动续投 / 故障转移会接管。N = `--stall-timeout` > agent `stall_timeout_sec` > server `stall_timeout_sec`（默认 900）。exec job 默认不判停滞，交互 job 恒不判；等人作答与 verify 期间不计时。`--no-stall` 关掉。
- **故障转移**：agent 因供应商错误（rate limit、at capacity、429、stream disconnected 等）挂掉、自己也续不了时，server 用候选 agent 以原请求重提一个新 job（同一 cwd，prompt 前加「上一次由 X 执行、只做剩余部分」，标题加 `(→omp)`），继承 plan / tags / review / verify 等。候选来源 `--fallback a,b` > 项目 `agent_fallbacks` > agent `fallback_agents`，提交时冻结；`--no-fallback` 关掉。配置见 [server-config.md](server-config.md)「故障转移与健康度」。
- **重试**：`--retry <n>[:<退避秒列表>]` 失败后按退避重跑（n 含首次），`--retry-on <退出码>` 限定只在这些退出码时重跑，`--no-retry` 关掉。`gofer job retry ls <id>` 看重试链、`job retry cancel` 取消某次待重试。

## 任务书模板（-t / --var）

把每天重复的「通用约束 + 交付要求」写成**模板**（server 上的 md 文件：frontmatter 给 job 默认值 + 正文即 prompt），提交时只给模板名和变量：

```bash
gofer template ls [-p <project>]                              # 能用哪些模板
gofer template show <name> [-p <project>] --var tasks="…"     # 预览：来源 + 变量表 + 服务端渲染后的正文
gofer job run -p <project> -t impl-batch --var tasks="1. 加 foo 子命令" --prompt "补充：不要动 web/"   # --prompt 追加在正文之后
gofer job run -p <project> -t impl-batch --var task=@brief.md    # 变量值从文件读
gofer job run -p <project> -t impl-batch --var task=@- --prompt "补充"  # 从 stdin 读
```

- 变量值的 `@` 形式：`--var k=@file` 读文件内容、`--var k=@-` 读 stdin、`--var k=@@text` 表示字面量 `@text`。值在 **CLI 侧**读取（server 只收到解析后的值）；文件不存在 / 是目录 / 超过 1 MB 时报错并指明是哪个变量。

- 位置：项目的 `<项目根>/.gofer/templates/<name>.md` 优先，其次 server 的 `<config-dir>/templates/<name>.md`。模板在 **server** 那台机器上，`template show` 是问 server。worker-only 项目只能用全局目录。
- frontmatter 白名单：`desc`、`agent`、`runner`、`model`、`timeout_sec`、`tags`、`verify`、`verify_timeout_sec`、`review`、`read_only`、`worktree`、`fallback_agents`、`budget`、`vars`（`{name: {default, required, desc}}`）；其它键解析即报错。**显式 flag > 模板默认 > 项目默认**。
- 正文变量：`{{name}}` 取 `--var`（没给用 `default`，`required` 缺值 → 400 列出缺项）；内置 `{{project}}` `{{cwd}}` `{{date}}` `{{head}}`，挂 plan todo 时还有 `{{plan_title}}` `{{plan_description}}` `{{todo_title}}` `{{todo_note}}` `{{todo_id}}`；`{{include: common.md}}` 拼同目录片段（只一层）。没人给值的未声明变量原样保留并告警。
- job 记录里存渲染后的 prompt 与模板名 / 变量，重跑不再渲染。`-t` 与 `-f`、与 `--` 后的 argv 互斥。
- 写项目自己的模板：把本项目的验证命令、禁区、汇报格式写进去，放在项目的 `.gofer/templates/`。示例见 <https://github.com/inhere/gofer/tree/main/docs/examples/templates>（参考写法后按本项目改）。

## 提交与 diff 采集

- 提交采集：job 开跑时记 `base_sha`，终态时把 `base..HEAD` 的提交（最多 50 个）写进 `commits`，`job show` 与 web「提交」块可见；worker 上的 job 同样回传。
- 带 `--todo <todo-id>` 时终态自动写回该 todo：成功 → todo `done`，备注追加 `<job-id> ✓ N commits: <sha> <subject>; …`；`needs_review` → 追加「待验收」（accept 后补 done）；失败 → 状态不动，追加 `<job-id> ✗ <status>: <原因>`。
- 项目 `capture_diff: auto|on|off`（默认 auto：cli-agent 与要验收的 job 采集未提交改动与本 job 的提交，存 `changes.diff`；普通 exec 跳过）。exec 也要审计时设 `on`；超大 diff 截断时附文件清单。采集失败不影响 job 终态。

## 工具调用审批（pending_interaction）

acp-agent 的工具调用若被项目 `approval` 策略（`mode: ask|strict`）拦住，job 停在 `pending_interaction`：

```bash
gofer job interactions <id>                         # 被求批的调用与可选 optionId
gofer job answer <id> <interaction-id> <optionId>   # 作答（web 交互面板 / 「待我决策」同样可答）
```

无人作答到 `timeout_sec` 按 `on_timeout` 兜底（默认 reject）。

## 日志：stdout / stderr 的分工

- `gofer job logs <id>` 读 stdout / stderr；交互 job 读 `pty.txt`。
- 配了 `output_format: ndjson` 的 agent（claude stream-json、omp json 等）：**stdout = 最终答复，stderr = 过程事件**（逐 token 增量在采集时丢弃，单行事件过长会截断）。`job show` 的 `ndjson_kept` / `ndjson_dropped` / `ndjson_truncated` 是保留 / 丢弃 / 截断行数；agent 定义开 `ndjson_raw: true` 才另存未过滤的 `stdout.raw.log`。
- 看不到输出时先分清「agent 没输出」还是「被过滤了」：开 `ndjson_raw` 重跑一次对照。
- acp-agent：`stdout.log` 是 agent 文本，`stderr.log` 是紧凑事件行，`artifacts/acp.jsonl` 是完整结构化记录。
- `job show` 还打出 dir（独占 / 共享、等锁时的持有者）、todo、`base_sha`、commits、verify、usage（用量与成本）、xfer 计数。
- 时间按服务端本地时区渲染并带偏移。

## 规则、skill 与角色

`job run` 还有：`--rule "<强制规则>"`（注入到 prompt 顶部，可重复）、`--skill <名>`（给本 job 挂载 skill，可重复）、`--no-rules` / `--no-skills`（不注入任何绑定）、`--role <预设>`（填 agent / system prompt / project / tags）、`--system-prompt`。server 侧的规则库 / skill 库用 `gofer agent rule …` / `gofer agent skill …` 管理。细节见 `--help`。

`--prime-keywords "子系统,语言,模块"` 给注入的 tracker-prime 选材：命中的记忆 / 规则在里面注入全文、排最前并标「（相关）」，未命中的照常按 cwd 列出（预算不够时先截掉）。缺省读环境变量 `GOFER_PRIME_KEYWORDS`，显式 flag 优先。派活时建议给出任务涉及的子系统 / 语言 / 模块名（如 `"job, rules"`），理由是选材命中。详情见 [tracker.md](tracker.md)「prime：会话开场注入什么」。

## 脱敏、秘密扫描与删除

- 提交时 `job run` 对命令 / prompt / 标题 / tags 做常见秘密形态扫描，命中只在 stderr 提示位置、不阻止；`--no-secret-check` 关闭。
- `gofer job redact <id> --literal-from-stdin`（或重复 `--pattern <RE2>`）：从终态 job 的记录与结果文件里抹掉秘密，原文只从 stdin 进入，输出只有计数。只限 job owner / 管理员；远程缓存、已发通知和外部日志不在范围内。带该 `job_id` 的信件（以及收集箱条目里引用它们的摘要）一并处理。
- `gofer job secret-scan --literal-from-stdin [--pattern <RE2>] [-p <project>] [--since <dur>]`：跨终态 job 查同一秘密，输出 job id、掩码标题、位置与计数，另有一段 `mail` 列出命中的信件（id、对话串、收发显示名、状态、项目、位置与计数；不回显正文）；加 `--redact --yes` 逐个脱敏（job 与信件都算），`--vacuum` 额外压缩数据库（可能持锁）。普通 caller 只看自己的 job 与它自己收发（含自己会话 / job 收到）的信件。
- `gofer job delete <id>... --yes`：删除终态 job 的记录与结果目录，保留删除审计。

## 派活给主机 agent 的注意事项

- **主机是 Windows 时反引号会被 PowerShell 吃掉**：agent 用 PowerShell 双引号字符串写文件时，`` `t `` `` `n `` 等会变成制表符、换行，Markdown 行内代码和注释被悄悄改坏，编译测试照样通过。任务书写明：改文件用 apply_patch，不要用 PowerShell 双引号字符串（含 `@"..."@`），非用不可时只用 `@'...'@`；提交前 `git grep -nP "[\x00-\x08\x0b\x0c\x0e-\x1f]" -- <改动的文件>` 无输出。
- 主机命令用 `cmd /c "…"` 包（PowerShell 会吞 git 输出）。
- **Windows 写出的文件常是 CRLF**：与容器共用的仓库最好有 `.gitattributes`（`* text=auto eol=lf`），否则容器侧会看到大批「已修改但 diff 为空」。
- **agent 的汇报不能当验收依据**：关键结论自己 grep 源码、自己跑测试；必要时要求它贴出指定命令的原始输出。

## 在 job 上留评论

```bash
gofer job comment <job-id> "验收：全量测试通过；我补了 abc1234（修 X）；已发布。"
gofer job comment <job-id> "退回：发现 Y 回归，续接修复见 job <新 id>。"
gofer job comments <job-id>
```

- 写：结论（通过 / 退回）、自己做的修改（附提交）、发现的问题、返工的后续 job id、验收与发布结果。几行事实即可。
- 不必评：很简单的 exec / 检测类 job（构建、查版本、读配置）。实施、方案候选、返工这类实质性 job 才评。
- **不要写 `@名字`**：人的评论里出现 `@agent` / `@role` 会真的派出一个 job。在 job 内部（设了 `GOFER_JOB_ID`）发的评论记为该 agent 的发言，不会派活。
