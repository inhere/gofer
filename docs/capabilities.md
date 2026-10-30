# gofer 能力清单

> 一句话定位：把 codex / claude 等 agent 与普通命令桥接成可追踪、可验收的异步任务（job）的控制平面，提供 CLI / HTTP / MCP / Web 四种入口；自身不内置模型。
>
> **适用版本：截至 v0.203.1**（CHANGELOG 最新已发布版本，2026-10-29）。

## 维护说明

- 每次发版对照 [CHANGELOG.md](../CHANGELOG.md) 更新本文：新增能力加条目，行为变更改描述，被移除的能力删条目。
- 稳定程度以 README「稳定性速览」与 [docs/guide/stability.md](guide/stability.md) 为准；它们变化时同步更新本文各行的「稳定程度」。
- 已发布能力写在「开发中」之前的各板块；尚未发布的只放「开发中」一节，发版后再迁入对应板块。
- 命令的完整 flag 以 `gofer <cmd> -h` 为准；本文只列入口名称与文档去向。
- 文中路径均相对本文件所在的 `docs/` 目录，或以 `skills/`、`internal/` 开头的仓库根相对路径。

稳定程度含义：**稳定** = README 稳定性速览列为可依赖；**变化中** = 速览列为仍在快速变化或 stability.md 标为实验性 / 默认关闭；**实验** = 明确标注实验。速览未覆盖的子项标「未标注」。

## 整体

### 运行模式

| 模式 | 启动 | 作用 |
|---|---|---|
| server | `gofer serve`（`-d` 后台；`serve register` 注册为受管服务） | 唯一的策略与数据权威：项目与准入规则、job 存储、调度、API、Web 控制台 |
| worker | `gofer worker`（`worker init` / `worker join` 接入） | 远程执行机，主动连入 server（WebSocket），无需开入站端口 |
| client | `gofer init client`（生成 `.env`，`GOFER_RUN_MODE=client`） | 纯命令行节点，所有命令转发给 server |

### 入口

| 入口 | 说明 | 文档 |
|---|---|---|
| CLI | `gofer <group> <cmd>`，顶级命令分组见命令参考 | [skills/gofer-usage/references/commands.md](../skills/gofer-usage/references/commands.md) |
| HTTP | `/v1/*`，Bearer token 鉴权 | [reference/http-api.md](reference/http-api.md)；直接调用：`gofer tool api` |
| MCP | `gofer mcp`（stdio），工具名均以 `gofer_` 开头 | commands.md「mcp」一节 |
| Web | `serve` 内嵌 SPA（默认 `:8765`），release 包自带，`go install` 版为占位页 | [reference/web-console.md](reference/web-console.md) |

### 数据存放

| 数据 | 位置 | 说明 |
|---|---|---|
| job / plan / 会话 / 信件 / 工作项 / 设置 / 授权 | server 的数据库（jobstore） | 由 server 管理；`gofer settings`、`gofer auth` 等经 HTTP 读写 |
| issue / memory | 仓库内 `.gofer/tracker/*.jsonl`（tracker） | 随代码提交；用 `gofer repo status --changed` 看变化，不要直接 diff jsonl |
| 配置 | `<config-dir>/config.yaml`（server）、`worker.yaml`（worker）、`.env`（client） | 默认目录 `~/.config/gofer` |

## 任务执行（job）

| 子项 | 说明 | 主要入口 | 稳定程度 | 文档 |
|---|---|---|---|---|
| 提交与生命周期 | 提交命令或 agent 任务，状态机、日志、diff 与提交采集、用量成本、取消 / 重跑 | CLI `job run`（`add`）/ `logs` / `show` / `watch` / `list` / `cancel` / `rerun` / `set`；HTTP `/v1/jobs`；MCP `gofer_run_job` / `gofer_get_job` / `gofer_list_jobs` / `gofer_tail_log` / `gofer_cancel_job`；Web「任务」 | 稳定 | [../skills/gofer-usage/SKILL.md](../skills/gofer-usage/SKILL.md)、[guide/en/features.md](guide/en/features.md#job) |
| agent 类型 | 三类：cli（claude / codex 等）、acp、exec（运行 `--` 之后的 argv，不经 shell）；按项目 `allowed_agents` 准入 | CLI `agent list` / `show` / `detect` / `status` / `probe`；MCP `gofer_list_agents` | 稳定 | [../skills/gofer-usage/references/server-config.md](../skills/gofer-usage/references/server-config.md) |
| runner | 在 server 本机（`--runner server`，`local` 同义）或某台 worker 上执行 | CLI `job run --runner <id>`；`worker ls` | 稳定 | [../skills/gofer-usage/SKILL.md](../skills/gofer-usage/SKILL.md) |
| 同步 / 异步与超时 | `--sync` 等待终态，长任务异步 + `watch`；`--timeout` 受上限截断，`--strict-timeout` | CLI `job run --sync --wait-timeout --timeout` | 稳定 | [../skills/gofer-usage/references/job-advanced.md](../skills/gofer-usage/references/job-advanced.md) |
| worktree 隔离与交付 | `--worktree` 在独立 git worktree 与分支中运行；合并 / 推分支 / 开 PR 三种交付；目录锁 | CLI `job run --worktree`、`job worktree ls\|merge\|rm`、`job deliver`；Web 任务详情 | 稳定（worktree 与合并）；交付 push / PR 见 stability.md 实验性说明 | [runbook/parallel-jobs-with-worktree.md](runbook/parallel-jobs-with-worktree.md) |
| 只读 job | 只读审查模式 | CLI `job run --read-only` | 未标注 | job-advanced.md「只读 job」 |
| 只读参考目录 | 给 job 挂只读参考目录，提交时做路径与符号链接范围校验（v0.203.0 新增） | CLI `job run --read-dir`；项目 `read_dirs`；计划待办目录字段 | 未标注（较新） | job-advanced.md「只读参考目录」 |
| 续接 | 中断后续跑同一 agent 会话；继承旧会话开新会话；运行中追加需求 | CLI `job resume`、`job run --from-session`；`job follow-up` 相关命令；MCP `gofer_job_follow_up` | 稳定 | job-advanced.md「中断后续跑」「继承旧会话」 |
| ACP 持续会话 | `--session` 持续对话，下一轮 `job say`、结束 `job end` | CLI `job say` / `job end`；MCP `gofer_job_say` / `gofer_job_end` | 未标注 | job-advanced.md「ACP 持续会话」 |
| 交互 pty | agent 以交互 pty 方式启动，可在 Web 附着终端 | CLI `job run --interactive`（以 `--help` 为准）；Web 工作台 | 未标注 | job-advanced.md「agent 的两种启动方式与交互 pty job」 |
| 人工验收（needs_review） | `--review` 使 job 停在待验收，一屏看汇报、验收标准、提交、diff、越界文件、发现；通过或打回（可自动续跑） | CLI `job review` / `accept` / `reject --note [--resume]` / `findings [--create-issues]`；Web「待验收」`/review` | 稳定 | [guide/en/features.md](guide/en/features.md#review-and-quality-gates) |
| 验收标准与范围 | `--acceptance` / `--scope` 注入验收标准与交付约定，汇报按约定回写发现与经验 | CLI `job run --acceptance --scope` | 稳定 | job-advanced.md「人工验收、验收标准与改动范围」 |
| verify | `--verify` 在 job 结束后跑校验命令，结果进入验收面板 | CLI `job run --verify`；`workflow init verify` | 稳定 | job-advanced.md；[design/2026-10-21-verify-and-release-templates-design.md](design/2026-10-21-verify-and-release-templates-design.md) |
| 待批 job（hold） | `--hold` 提交后等人批准才执行，agent 自己不可批准 | CLI `job run --hold --hold-reason`、`job approve` / `reject`；Web 任务详情「等你批准」 | 稳定 | [../skills/gofer-usage/references/hold-approval.md](../skills/gofer-usage/references/hold-approval.md) |
| 工具调用审批 | ACP agent 的 edit / execute 等调用等人批准，只读工具放行 | CLI `job interactions` / `job answer`；MCP `gofer_get_interactions` / `gofer_answer_interaction`；Web 今天 | 未标注 | job-advanced.md「工具调用审批」；[runbook/2026-07-02-web-interaction-answer-runbook.md](runbook/2026-07-02-web-interaction-answer-runbook.md) |
| 任务书模板 | `-t` / `--var` 渲染模板，项目级或全局 | CLI `template ls` / `template show`、`job run -t` | 未标注 | job-advanced.md「任务书模板」 |
| 预算、故障转移与重试 | 预算熔断、停滞看门狗、供应商故障转移、`--retry` 重试链 | CLI `job retry ls\|cancel`、`agent status` | 未标注 | [runbook/2026-09-22-job-retry-runbook.md](runbook/2026-09-22-job-retry-runbook.md) |
| 结束通知提交会话 | 长 job 结束时经传话人通知提交方 | CLI `job run --notify` / `--notify-session` | 未标注 | job-advanced.md「job 结束时通知提交会话」 |
| 评论 | 在 job 上留痕；人的评论中 `@agent` 会真的派出 job | CLI `job comment` / `job comments`；MCP `gofer_comment` / `gofer_list_comments` | 未标注 | [runbook/2026-09-23-comments-runbook.md](runbook/2026-09-23-comments-runbook.md) |
| 凭据与 job 环境 | job 用仅作用于本 job 的凭据，默认清洗父环境中的 token | 配置 `job_env_denylist` / `job_env_allow` | 未标注 | [../skills/gofer-usage/references/operations.md](../skills/gofer-usage/references/operations.md)「job 的环境清洗」；[runbook/2026-09-23-job-credentials-runbook.md](runbook/2026-09-23-job-credentials-runbook.md) |

## 编排（plan / workflow / schedule）

| 子项 | 说明 | 主要入口 | 稳定程度 | 文档 |
|---|---|---|---|---|
| plan 与 todo | 计划与待办；todo 指派即派发；`--after` 依赖链，`plan run` 一次开工，前一项完成后自动出 job | CLI `plan create` / `list` / `show` / `add-todo` / `set-todo` / `set-status` / `run` / `pause` / `resume` / `dispatch`；MCP `gofer_create_plan` / `gofer_add_todo` / `gofer_update_todo` / `gofer_plan_run` / `gofer_dispatch_todo`；Web「计划」 | 稳定 | [../skills/gofer-usage/references/plans-workflows.md](../skills/gofer-usage/references/plans-workflows.md) |
| 接着上一项的会话 | 待办派发时在前序项的会话里继续 | CLI `plan add-todo` 派发字段；Web 待办表单开关 | 稳定（随 plan） | [design/2026-10-17-plan-todo-continue-session-design.md](design/2026-10-17-plan-todo-continue-session-design.md) |
| 方案导入与交接 | 方案末尾的 `gofer-todos` 块转成 todo 链；阶段交接；接手简报 | CLI `plan import` / `plan attach` / `plan handoff` / `plan brief`；MCP `gofer_plan_handoff` / `gofer_plan_brief` | 稳定 | plans-workflows.md「方案规则与 plan import」 |
| 主 Agent 会话（监督会话） | 计划绑定监督会话，job 结束与阻塞通知送达该会话 | CLI `plan create --supervisor-session-id` / `plan set`；MCP `gofer_set_plan_supervisor_session` | 未标注 | plans-workflows.md「主 Agent 会话与 job 来源会话」 |
| leader 回合 | 按 plan opt-in，成员终态唤醒 leader 写评论 / 接着派活，有轮次上限，leader 不能验收 | plan 配置 | 变化中（Squad / leader） | [runbook/2026-09-23-leader-round-runbook.md](runbook/2026-09-23-leader-round-runbook.md) |
| workflow | 有依赖的多步 YAML 编排，含扇出 / 汇合、子工作流、内置步骤、`if: always`、失败重跑 | CLI `workflow run` / `list` / `show` / `events` / `cancel` / `export` / `rerun`；Web「工作流」 | 变化中（stability.md 标实验） | plans-workflows.md「workflow」 |
| workflow 模板 | 模板库、变量、保存为模板；`workflow init verify\|release` 生成起步文件 | CLI `workflow template ls\|show\|save\|rm`、`workflow save-as-template`、`workflow init`；Web 模板向导 | 变化中 | plans-workflows.md「模板的查看与保存」「verify / release 起步 workflow」 |
| 多 agent 对比与择优 | 同一任务扇出给多个 agent，人工挑选并可合并 worktree | CLI `workflow pick <id> <step> <fan> [--merge]` | 变化中 | plans-workflows.md「多 agent 对比与择优」 |
| 定时任务 schedule | cron、一次性（`--delay` / `--at`）、webhook 触发；参数声明与立即运行 | CLI `schedule add` / `list` / `show` / `edit` / `enable` / `disable` / `run` / `rm` / `rotate-token`；HTTP `/v1/schedules`；Web `/schedules` | 稳定 | [runbook/2026-06-30-cron-schedule-runbook.md](runbook/2026-06-30-cron-schedule-runbook.md) |
| 计划看板 | plan 总览（分段进度条）、自动驾驶看板；详情页任务页签分页 | Web「计划」 | 变化中（部分 Web 页面） | [design/2026-09-22-plan-autopilot-board-and-container-worker-design.md](design/2026-09-22-plan-autopilot-board-and-container-worker-design.md)、web-console.md |
| 决策点问人 | agent 阻塞提问，人在 Web 作答 | MCP `gofer_ask_human`；CLI `plan ask` / `decisions` / `answer` | 稳定（随 plan） | plans-workflows.md「决策点问人」 |

## 会话与协作

| 子项 | 说明 | 主要入口 | 稳定程度 | 文档 |
|---|---|---|---|---|
| 会话登记与中继 | 用 hooks 登记终端 Claude / Codex / omp 会话；停下时在 Web 等回复，三态 on / auto / off | CLI `init hooks`、`session relay on\|auto\|off`、`session ls` / `show` / `say`；Web「会话」 | 稳定 | [runbook/session-relay.md](runbook/session-relay.md)、[../skills/gofer-usage/references/sessions-relay.md](../skills/gofer-usage/references/sessions-relay.md) |
| 传话与唤醒 | 给空闲会话送话，唤醒 / 接管已结束会话，`session watch` 盯 job，会话催办 | CLI `session say --deliver` / `resume` / `release-takeover` / `watch` / `nudge`；`job wakeup create`；MCP `gofer_wakeup_create` | 稳定 | sessions-relay.md；[../skills/gofer-usage/references/files-and-wakeup.md](../skills/gofer-usage/references/files-and-wakeup.md) |
| 会话主题与查找 | 记录会话在做什么，按关键词或路径找谁在做某事 | CLI `session topic` / `session find`；MCP `gofer_session_set_topic` / `gofer_session_find` | 未标注 | [runbook/2026-10-16-capture-box-and-session-topics-runbook.md](runbook/2026-10-16-capture-box-and-session-topics-runbook.md) |
| 等待与查看另一会话 | 等另一会话停下、看它当前显示的内容 | CLI `session wait` / `session screen`；MCP `gofer_session_wait` / `gofer_session_screen` / `gofer_session_tail` | 未标注 | sessions-relay.md「wait / screen」 |
| 信箱 mail | 会话 / job / 收集箱之间按地址（`session:` `topic:` `job:` `path:` `agent:` 等）收发信；急件、群发、退信、暂停送达、已收到 / 已处理回应 | CLI `session mail send\|ls\|conv\|read\|ack\|pause\|resume\|reply\|cancel\|redact`；HTTP `/v1/mail*`；MCP `gofer_session_mail_*`；Web `/mail` | 变化中（stability.md 标实验） | [runbook/2026-10-18-mailbox-runbook.md](runbook/2026-10-18-mailbox-runbook.md) |
| 常驻 agent 收信 | 配置 `mail.agents` 后，`agent:<名字>` 地址由常驻 ACP 会话收信并回信 | 地址 `agent:<名字>[@<项目>]` | 变化中 | sessions-relay.md「信箱」 |
| 会话提问 | 终端会话里的 AskUserQuestion 转到 Web 作答；会话间提问 | MCP `gofer_session_ask`；Web 今天 | 未标注 | [design/2026-10-12-session-question-relay-design.md](design/2026-10-12-session-question-relay-design.md) |
| Web 消息转达与附图 | 在 Web 向会话发消息，输入框可带图片 | HTTP `/v1/attachments*`；Web 工作台 / 会话抽屉 | 未标注 | [design/2026-10-19-workbench-image-attachments-design.md](design/2026-10-19-workbench-image-attachments-design.md) |
| 今天与待我决策 | 汇总等人的事（审批、提问、验收、等回复、记忆、worker 申请）为卡片队列 | Web `/today`；HTTP `/v1/today`；MCP `gofer_today_list` / `gofer_today_card` | 稳定 | [../skills/gofer-usage/references/web-console.md](../skills/gofer-usage/references/web-console.md) |
| 工作项与收集箱 | 长期工作项、随手记收集箱与分派、会话汇报进度 | CLI `work capture\|ls\|show\|new\|set\|note\|report\|...`；MCP `gofer_work_*`；Web `/work` | 稳定性速览未列（收集箱 / 管家 stability.md 标实验） | [../skills/gofer-usage/references/work-items.md](../skills/gofer-usage/references/work-items.md) |
| 管家 steward | 自动整理工作项、提示建议；默认关闭 | CLI `steward status\|start\|restart\|stop\|ask\|notes\|review\|merges` | 变化中 | work-items.md |
| 评论 | issue / plan / job 上的评论 | CLI `issue comment`、`plan comment`、`job comment` | 稳定（issue、plan） | tracker.md、plans-workflows.md |

## 任务跟踪与记忆

| 子项 | 说明 | 主要入口 | 稳定程度 | 文档 |
|---|---|---|---|---|
| issue | 仓库内 issue：优先级、类型、依赖、父子（epic）、验收标准、评论 | CLI `issue ready` / `ls` / `show` / `create` / `update --claim` / `comment` / `close` / `reopen` / `dep`；MCP `gofer_issue_list` / `gofer_issue_get`；Web Issues 页 | 稳定 | [../skills/gofer-usage/references/tracker.md](../skills/gofer-usage/references/tracker.md) |
| brief 接手简报 | 一次拿齐字段、方案评论、设计稿、相关提交与代码入口、相关 job、验证命令、适用记忆 | CLI `issue brief <id>` / `plan brief <plan>`；MCP `gofer_issue_brief` / `gofer_plan_brief` | 稳定 | tracker.md「接手：先跑 brief」 |
| memory | 记忆（rule / note / handoff），按适用路径、关键词、命令条件注入；全局 / 项目 / 仓库作用域 | CLI `memory set` / `ls` / `show` / `rm` / `flag` / `unflag` / `doctor` / `archive` / `restore` / `promote` / `sync --global`；MCP `gofer_memory_*`；Web `/memory` | 稳定 | tracker.md；[design/2026-10-12-local-global-memory-design.md](design/2026-10-12-local-global-memory-design.md) |
| 经验候选 | 交付后知识回流为候选记忆，人确认采纳或拒绝 | CLI `memory candidates` / `accept` / `reject`；MCP `gofer_memory_candidates` 等 | 未标注 | tracker.md「经验候选」 |
| 记忆体检与整理建议 | 过时检测、反馈、整理建议卡 | CLI `memory doctor` / `memory flag`；MCP `gofer_memory_findings` / `gofer_memory_suggest` / `gofer_memory_flag`；Web 今天 | 未标注 | [design/2026-10-09-prime-memory-quality-design.md](design/2026-10-09-prime-memory-quality-design.md) |
| prime 开场注入 | 会话开场注入提交策略、当前重点、ready issue、rule 记忆与记忆索引；`--prime-keywords` 提高相关记忆优先级 | CLI `repo prime [--keywords]`、`init hooks --prime-only` | 稳定 | tracker.md「prime」 |
| tracker 同步与合并 | 初始化、与 server 镜像同步、状态查看、多 worktree 合并驱动、changelog 合并驱动、从 bd 迁移 | CLI `repo init\|sync\|status\|merge-driver\|changelog-merge\|migrate` | 稳定 | tracker.md；[runbook/tracker-transfer.md](runbook/tracker-transfer.md) |
| 每日摘要 | 工作项摘要事件 `work.digest` 与 `work digest` | CLI `work digest`；事件 `work.digest` | 未标注 | work-items.md |

## 执行机与网络

| 子项 | 说明 | 主要入口 | 稳定程度 | 文档 |
|---|---|---|---|---|
| worker 登记与接入 | 管理员登记并发一次性 token，或 worker 自助申请经管理员接受 | CLI `worker add\|remove\|ls\|show\|projects`、`worker init`、`worker join`、`worker doctor`；HTTP `/v1/workers`、`/v1/worker-enroll*`；Web Runners 页「待接受」 | 稳定 | [../skills/gofer-usage/references/worker-config.md](../skills/gofer-usage/references/worker-config.md)、[../skills/gofer-usage/references/setup-recipes.md](../skills/gofer-usage/references/setup-recipes.md) |
| roots 与项目映射 | worker 的目录根（roots）与 POLICY / LEGACY 两种模式，路径映射 | 配置 `worker.yaml`；CLI `config validate worker` | 稳定 | worker-config.md；[design/worker-roots-config-reference.md](design/worker-roots-config-reference.md) |
| 容器 worker | 容器内运行 worker | CLI `worker` | 稳定（随 worker） | [runbook/container-worker.md](runbook/container-worker.md) |
| 配置重载 | worker / server 不重启重读配置 | CLI `worker reload`、`serve reload`；Web 设置「重新读取文件」 | 稳定 | [../skills/gofer-usage/references/operations.md](../skills/gofer-usage/references/operations.md)「重载配置」 |
| 自升级 | 从控制台或命令升级整个 worker 舰队，任意 OS / 架构 | CLI `worker upgrade <id>`；HTTP `POST /v1/workers/{id}/upgrade`；Web Runners | 稳定 | operations.md「远程升级 worker」；[runbook/release-and-binary-swap.md](runbook/release-and-binary-swap.md) |
| TCP 隧道 | 本机端口转发到 worker 所在网络；SOCKS5 / HTTP CONNECT 代理；同地址转发；预设；UDP | CLI `tunnel forward\|save\|saved\|forget\|check\|ls\|stop`、`tunnel presets push`；Web Tunnels | 未标注（运维类稳定项含隧道） | [runbook/tcp-tunnel.md](runbook/tcp-tunnel.md) |
| 文件传输 | 单文件在 server 与 runner 之间对拷；job 自带文件上传 / 收集；传输暂存区 | CLI `tool cp`、`tool xfer ls\|show\|rm`、`job run --upload` / `--collect` | 稳定 | [../skills/gofer-usage/references/files-and-wakeup.md](../skills/gofer-usage/references/files-and-wakeup.md) |
| 工作区附件 | Web 工作台 / 会话抽屉附图 | HTTP `/v1/attachments*` | 未标注 | web-console.md |

## 通知与事件

| 子项 | 说明 | 主要入口 | 稳定程度 | 文档 |
|---|---|---|---|---|
| IM webhook | 钉钉 / 飞书 / 通用 webhook 出站通知，签名、重试、文本长度限制 | 配置 `server.notification.webhooks`；Web 设置 → 通知（含机器人测试发送） | 未标注 | [runbook/im-notification.md](runbook/im-notification.md) |
| 出站白名单 | 出站总开关、`allow_hosts`、`allow_http` 等全局设置，可在 Web 编辑 | 配置 `server.notification`；Web 设置 → 通知 → 全局设置 | 未标注 | im-notification.md |
| 桌面通知 | server 在所在主机弹系统通知，默认订阅审批 / 提问 / 等回复等事件 | 配置 `server.notification.desktop` | 未标注 | server-config.md（桌面通知段） |
| 浏览器推送与 PWA | Web Push、可安装为应用，审批通知可直接允许 / 拒绝 | Web 设置 → 通知；HTTP `/v1/ws` 实时推送 | 未标注 | [runbook/web-push.md](runbook/web-push.md)、[runbook/https-pwa.md](runbook/https-pwa.md) |
| event hooks | 事件发生时在 server 主机跑命令，只读旁路；只能改配置文件 | 配置 `server.event_hooks`；改后 `serve reload` | 未标注 | [design/2026-10-11-event-hooks-design.md](design/2026-10-11-event-hooks-design.md)、im-notification.md「事件钩子」 |
| 决策 hook | 提交前 `job.submit` / 交付前 `job.deliver` 外部判定，只能否决或收紧 | 配置 `server.decision_hooks`；CLI `tool hook-test` | 变化中（D1 已落地，D2 / D3 未做） | [runbook/decision-hooks.md](runbook/decision-hooks.md) |

## 权限与安全

| 子项 | 说明 | 主要入口 | 稳定程度 | 文档 |
|---|---|---|---|---|
| caller 与 token | 基于 token 的调用方；worker / job / steward 各有受限凭据 | 配置 `server.callers`；CLI `auth whoami` | 稳定 | [guide/en/security.md](guide/en/security.md) |
| 权限矩阵 | 权限目录与路由所需权限；`whoami` 显示权限来源（root / config / db） | CLI `auth whoami` / `auth routes`；HTTP `/v1/auth/*`；Web 设置 → 权限 | 稳定 | [reference/permissions.md](reference/permissions.md) |
| 会话授权 | 给终端会话临时发权限，可续期（最长 24h） | CLI `auth grant renew`；Web 设置 → 权限 | 未标注 | sessions-relay.md「会话授权」 |
| 调用方上限 ceiling | 限定调用方可触及的项目、runner、worker、agent、exec、只读 | 配置 `server.callers[].ceiling`；Web 按上限过滤界面 | 变化中（已知未覆盖聚合、推送过滤等） | [runbook/2026-10-18-caller-ceiling-runbook.md](runbook/2026-10-18-caller-ceiling-runbook.md) |
| 项目准入 | 每个项目允许的 agent / runner / exec / 交互；超时上限 | CLI `project add\|list\|show\|validate`；配置 `allowed_agents` 等 | 稳定 | server-config.md |
| 秘密扫描与脱敏 | 日志与配置视图脱敏；跨 job 秘密扫描；对 job 与信件内的秘密抹除 | CLI `job redact` / `job secret-scan` / `session mail redact` | 稳定（脱敏）；其余未标注 | job-advanced.md「脱敏、秘密扫描与删除」；[design/2026-10-03-secret-sweep-design.md](design/2026-10-03-secret-sweep-design.md) |
| 隧道目标白名单 | 目标必须在 worker 的 `tunnel.allow` 内 | worker 配置 `tunnel.allow` | 同隧道 | tcp-tunnel.md |
| 无沙箱 | 现无容器沙箱 runner 与独立 OS 账号；隔离靠目录锁、worktree 与容器 worker | - | 不提供 | [guide/stability.md](guide/stability.md) |

## Web 控制台

页面清单与操作说明见 [reference/web-console.md](reference/web-console.md) 与 [../skills/gofer-usage/references/web-console.md](../skills/gofer-usage/references/web-console.md)。Web 整体：稳定性速览将「部分 Web 页面」列为仍在变化。

| 页面 | 作用 | 稳定程度 |
|---|---|---|
| 今天 `/today` | 待我决策队列、专注处理、并行泳道 | 稳定 |
| 工作台 `/workbench` | 会话中枢：ACP 对话、持续会话、交互 PTY、改动与评审，多标签分屏 | 稳定 |
| 任务 / 待验收 `/review` | job 列表与详情、验收面板、待批准 | 稳定 |
| 计划 / 工作流 / 定时 `/schedules` | plan 总览与详情、workflow 与模板、定时任务 | 计划稳定，工作流变化中 |
| 会话 | 终端会话列表与抽屉（回复、接管、唤醒、催办） | 稳定 |
| 信箱 `/mail` | 按对话显示的信件、写信、待指派 | 变化中 |
| 工作项 `/work` | 工作项与收集箱 | 变化中 |
| Issues / 记忆 `/memory` | tracker 的 issue 与记忆浏览编辑，跨项目搜索 | 稳定 |
| 统计 `/dashboard` | 任务、耗时、用量等统计 | 未标注 |
| Runners / Agents / 项目 / Tunnels | 执行机、agent 健康度与探针、项目表单、隧道 | 稳定 |
| 设置 | 配置编辑与重读 / 重启 server、通知、权限、扩展（菜单隐藏 `web.nav_hidden`） | 稳定 |

## 运维

| 子项 | 说明 | 主要入口 | 稳定程度 | 文档 |
|---|---|---|---|---|
| init | 生成 server / worker / client 配置；安装 hooks 与 skill | CLI `init [server\|worker\|client]`、`init hooks`、`init skill`；缺失 agent CLI 时给出安装提示 | 稳定 | [guide/en/getting-started.md](guide/en/getting-started.md) |
| doctor | `worker doctor` 自检；`doctor --bundle` 导出脱敏诊断包（版本、运行模式、脱敏配置、日志尾部、worker / agent 状态） | CLI `doctor --bundle [-o] [--lines]` / `doctor --print`；`worker doctor` | 稳定 | [../skills/gofer-usage/references/troubleshooting.md](../skills/gofer-usage/references/troubleshooting.md) |
| config | 查看、校验、编辑、拆分、lint 配置；运行时扩展设置存数据库 | CLI `config info\|show\|validate\|edit\|split\|lint`、`settings ls\|get\|set\|reset\|export\|import`；Web 配置管理页 | 稳定 | [runbook/config-split-and-lint.md](runbook/config-split-and-lint.md)、[runbook/2026-07-03-web-config-edit-runbook.md](runbook/2026-07-03-web-config-edit-runbook.md) |
| 配置热重载 | 见「执行机与网络」配置重载；server 也可 `SIGHUP` | CLI `serve reload`、`worker reload` | 稳定 | operations.md |
| 受管 server 服务 | Windows 登录任务 / Linux systemd 注册、启停、状态、日志、升级二进制 | CLI `serve register\|start\|stop\|restart\|status\|logs\|uninstall\|upgrade`；Web 设置「重启 server」 | 稳定 | [runbook/2026-10-08-serve-management-runbook.md](runbook/2026-10-08-serve-management-runbook.md) |
| 一键更新 | 检查并应用更新，等待空闲后升级 | CLI `serve upgrade`、`worker upgrade`（更新检查与应用的具体命令以设计稿为准） | 未标注 | [design/2026-10-13-update-apply-design.md](design/2026-10-13-update-apply-design.md)、[design/2026-10-14-update-wait-idle-design.md](design/2026-10-14-update-wait-idle-design.md) |
| 日志 | server 日志 `<config-dir>/run/serve.log`；hook 日志 `run/hook.log`；job 的 stdout / stderr 分流 | CLI `serve logs`、`job logs` | 稳定 | troubleshooting.md；job-advanced.md「日志」 |
| 直接调 API | 用同一套地址与 token 调任意 `/v1` 路由 | CLI `tool api` | 稳定 | commands.md「tool」 |
| 指标与统计 | `/metrics`、`/v1/stats`、Dashboard | HTTP `/metrics`、`/v1/stats` | 稳定 | [guide/stability.md](guide/stability.md) |
| 证书 | 本地 CA 与 HTTPS 证书（手机 PWA 需要 HTTPS） | CLI `tool cert` | 未标注 | [runbook/https-pwa.md](runbook/https-pwa.md) |

## i18n

| 子项 | 说明 | 主要入口 | 稳定程度 | 文档 |
|---|---|---|---|---|
| 生成文本语言 | `server.language` / 项目 `language` 决定注入 agent 的提示词段落、交付正文、信件提示的语言 | 配置 `server.language`、项目 `language` | 稳定 | [../skills/gofer-usage/references/server-config.md](../skills/gofer-usage/references/server-config.md) |
| CLI 语言 | `--help` 与提示按调用方语言显示：`GOFER_LANG` > 系统语言 > 缺省 | 环境变量 `GOFER_LANG` | 阶段 3（server 与 web 文本）仍在变化 | [design/2026-10-27-i18n-design.md](design/2026-10-27-i18n-design.md) |
| Web 界面 | 中文 / 英文切换 | Web 设置 | 变化中 | web-console.md |

## 开发中（未发布）

以下均为「开发中，未发布」，不属于上面的已发布能力。已合并但未发版的改动见 CHANGELOG「未发布」段。

| 能力 | 状态 | 说明 | 设计稿 |
|---|---|---|---|
| 入站触发 trigger | 开发中，未发布 | 外部系统按事件派一个受约束的 job；入站请求认证与签名，另含 GitHub 兼容 provider。与现有出站 webhook、只读 event_hooks 区别：这是入站且能提交 job | [design/2026-10-28-trigger-design.md](design/2026-10-28-trigger-design.md)（0.2 已批准） |
| job 预览 job-preview | 开发中，未发布 | 暴露 job 在 server 本机或 worker 上运行的 HTTP 服务（开发服务器、报告查看器等）；URL 形态默认端口模式，相关 tunnel 设计可参考 | [design/2026-10-28-job-preview-design.md](design/2026-10-28-job-preview-design.md)（0.2 已批准） |
| 每日记忆综合 | 开发中，未发布 | 把分散的当日结构化事实（工作项 journal、memory 体检等）综合为每日记忆；候选队列暂缓 | [design/2026-10-28-daily-memory-synthesis-design.md](design/2026-10-28-daily-memory-synthesis-design.md)（0.2 已批准） |
| 收件箱分栏 | 设计中 | 把等你回答、待验收、失败、信件、agent 回复汇到一个入口并分栏，按人记已读 / 清理状态，与 IM / 桌面推送规则一致 | [design/2026-10-28-inbox-triage-design.md](design/2026-10-28-inbox-triage-design.md)（0.1 草稿） |
| 反向链接 | 开发中，未发布 | issue / job / plan / 信件 / 评论里提到的对象 id 自动建立双向引用，详情页显示「被引用」；CLI `refs`、HTTP `/v1/refs`、MCP `gofer_refs` | — |
| 项目输出目录 | 开发中，未发布 | job 结果与附件默认从 `<项目>/tmp/` 移到 `<项目>/.gofer/jobs`、`.gofer/attachments`（由 `.gofer/.gitignore` 忽略）；显式配置不变 | — |
| worktree 默认位置 | 开发中，未发布 | `--worktree` 默认落在 `<git 顶层>/.worktrees/<name>`，并写入本地 `info/exclude`；显式 `worktree.dir` 不变，旧位置仍可列出与清理 | — |
