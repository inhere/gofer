# 工作项（gofer work）与管家（steward）

> 同时开着多个终端会话时，gofer 看得到「会话在不在跑」，看不到「做到哪、卡在哪、下一步是什么」。**工作项**就是这层记录：一张卡 = 一件事，跨会话（会话结束后被唤醒、换 agent 接手、换机器继续都挂在同一张卡上）。web「工作项」页（`/work`，手机优先）是总览。管家是可选的常驻 agent，帮你整理这些卡。完整 flag 以 `gofer work --help` / `gofer steward --help` 为准。

## 目录

- [什么时候会碰到工作项](#什么时候会碰到工作项)
- [被要求汇报 / 写交接时](#被要求汇报--写交接时)
- [状态与自动映射](#状态与自动映射)
- [命令](#命令)
- [请求账本与被动整理](#请求账本与被动整理)
- [降噪（短命会话与排除名单）](#降噪短命会话与排除名单)
- [里程碑、健康度与「今天」泳道](#里程碑健康度与今天泳道)
- [提醒、摘要与通知](#提醒摘要与通知)
- [管家（steward）](#管家steward)
- [MCP 工具](#mcp-工具)

## 什么时候会碰到工作项

- 新会话第 `work.draft.min_prompts` 次人工提问（默认 2，第 1 次多半只是试一下）时自动建一张草稿（标题取触发它的那次提问首行，进 web「工作项」页的「待整理」泳道）；补上目标或点「已整理」后进入看板。没有项目的会话、命中排除名单的会话不建（见 [降噪](#降噪短命会话与排除名单)）；已经建出来的草稿不受影响。
- web「请它汇报」「请它写交接」，或人把卡搁置 / 标「需现场」时，你的会话会收到一段固定请求（含工作项 id 和请求 id）——照下一节回复。
- 不想用可以不管：不影响 job / plan / issue 的使用。

## 被要求汇报 / 写交接时

```bash
gofer work report <id> --request <请求id> --goal "这件事为了什么" --status needs_onsite \
  --blocker "缺现场设备" --next "到货后跑回归" --summary "接口已写完，待联调"
#   --status: active|needs_me|waiting_resource|needs_onsite|review|parked（active = 阻塞已解除；不需要的字段省略）
#   --request <id> = 回填请求账本；MCP gofer_work_report 的 request 参数同义
#   --session <sid>（或环境变量 GOFER_SESSION_ID）= 发言者；job 凭据也能 report
```

SessionStart prime 里也有这行提示。

## 状态与自动映射

- 8 个状态：`active` 进行中、`needs_me` 等我、`waiting_resource` 等资源、`needs_onsite` 需现场、`review` 待验收、`parked` 已搁置、`done` 已完成、`dropped` 已放弃。
- 会话自动映射：运行中 → `active`；会话等回复 / 关联 job 有待应答交互 / 关联 plan 有待回答的决策 → `needs_me`；关联 job 待验收 → `review`。会话离线 / 结束只在卡片标「会话已离线」，不改状态。
- **人手动设置的状态优先**（`work set --status` / web 标状态），之后会话怎么跑都不覆盖，直到 `work set --auto` 交还，或会话汇报 `--status active`（阻塞解除）。`done` / `dropped` 永远不从汇报采纳（完成由人确认）。
- 完成回写只建议：关联的 todo / issue 全部完成时，不改卡的状态，而是留一条「待验收 / 完成」建议，人在卡上「采纳」才生效。
- 日志只追加：汇报、备注、状态 / 字段变更、关联、合并、整理记录；每条标明发言者（人 / 会话 / 整理器 / 管家）与级别（里程碑 / 流水）。改字段带版本号，过期会被拒（409）。

## 命令

```bash
gofer work ls [--status needs_me,review] [--project p] [--workspace dir] [--query q] [--unsorted] [--due] [--all] [--json]
#   列：ID STATUS SESS SEEN FLAGS TITLE；FLAGS: U=未整理草稿 D=提醒/搁置到期 O=会话已离线
gofer work capture "<内容>" [--kind idea|issue|request|note] [--project p]   # 收集箱快速记录（status captured，kind 默认 note），打印 id；用 `gofer work ls --status captured` 看；来源会话取环境里的会话 id，job 凭据下记来源 job
gofer work dispatch <id> --to session|job|todo|issue|archive|drop [--session sid] [--job jid] [--plan pid] [--tracker tid] [--title t]   # 分派收集箱条目（只对 captured）：session 给对方发一封信（出处同 `session mail send`，条目记信件 id；信件送达 / 被回复时把「已送达」与回复摘要写进条目日志，送不出去时条目留在收集箱）、job 关联已提交的 job（结束时终态写进日志）、todo 同 to-todo、issue 在 --tracker 仓库的 server 镜像里新建 issue（--title 缺省用条目标题）、archive 结案(done)、drop 丢弃；成功后条目 active（archive/drop 除外）；他人的会话/job/plan 需 session.reply.others / job.read.others / plan.manage.others

> 转仓库 issue（`--to issue --tracker <id>`，web 收集箱的「转 issue」同此）：在该仓库的 server 镜像里新建 issue（kind 为 issue 记 bug，其余记 task）、关联条目并改为 `parked`；issue 带「待同步」标记，下次 `gofer repo sync` 才进仓库。仓库还没同步过（没有 id 前缀）时 409。**描述总是带收集原文**：`--title` 改短了标题也不会丢——条目标题原文、与标题不同的 goal、文本里的附图 / 附件引用都进描述，外加一行来源（谁收集、何时、来自哪个会话 / job / 收集箱 / 信箱、项目）和末行 `captured as work item <id>`；所以标题改写只影响标题，原文始终可在 issue 里查。
gofer work show <id>                       # 字段（含来源）+ 会话 + 关联 + 日志 + 建议 + 请求；id 可用唯一前缀
gofer work new "标题" [--goal ..] [--project ..] [--session <sid>]...
gofer work set <id> [--title|--goal|--status|--blocker|--blocker-kind|--next|--summary|--project|--workspace|--priority N] [--auto] [--sorted] [--rev N]   # 值给 - 清空
gofer work note <id> "备注"
gofer work park <id> [--until 2d] [--note "到货后继续"]
gofer work park --captured [--until 2d] [--note ..]   # 回滚辅助：把收集箱（captured）里的全部条目转为 parked，各写一条「从收集箱归档」日志；与 <id> 二选一
gofer work remind <id> <时间> | --clear
gofer work link <id> --issue X | --plan X | --todo X | --job X | --session <sid> | --acp <job-id> [--rm]
gofer work to-todo <id> [--plan <plan-id> | --new-plan "标题"]   # 转成 plan todo 并回链；只能转一次
gofer work merge <id> <src...>             # 多张并入一张（源项 dropped，列表默认隐藏）
gofer work split <id> "新标题" [--session <sid>]... [--keep]      # --keep = 一个会话做了两件事
gofer work summarize <id>                  # 立即整理（别名 tidy）
gofer work accept|dismiss <id> <field>     # 采纳 / 忽略整理建议（goal|blocker|blocker_kind|next|summary|status_hint）
gofer work requests [<id>] [--all]
gofer work digest [--send]
gofer work rm <id>... [--yes]              # 永久删除 done / dropped 的项（不带 --yes 只列出）
gofer work rm --status dropped|done [--dry-run] [--yes]
```

- 时间写法：`2h` `90m` `3d` `1w` `tomorrow`（次日 09:00）、`YYYY-MM-DD`、`"YYYY-MM-DD HH:MM"`（本地时间）、RFC3339、unix 秒。
- `--acp <job-id>`：ACP 持续会话 / 终端 pty job 也能挂到工作项上（它们不参与「请它汇报」）。
- 删除只允许 `done` / `dropped` 的项，是人的决定：没有 MCP 工具，job / 管家凭据不能删。

## 请求账本与被动整理

- **请求**：`report`（汇报）/ `handoff`（交接）/ `summarize`（整理）；状态 `pending` → `sent` → `answered`，或 `failed` / `expired`。会话在运行时经中继或传话送达；不在运行或送达失败时直接转为整理。默认 30 分钟（`work.request_timeout_min`）未回 → `expired` 并自动整理。
- **自动交接**：人把状态设为 `parked` 或 `needs_onsite` 时，对运行中的会话自动发交接请求（`work.auto_handoff: false` 关闭）。
- **被动整理**：会话空闲 / 等回复满 `work.summarize_idle_min`（15）分钟、或离线 / 结束，且有新活动时，server 起一个一次性、只读、无工具的整理 job（agent 默认 `claude`，便宜模型；项目依次取 `work.summarizer_project` → 工作项自己的项目 → 内置 `default` 项目）。输入是会话记录尾部，输出目标 / 进展 / 卡点 / 下一步 / 状态建议 / 里程碑。整理 job 带内部标签，job 列表默认隐藏（`--all` 可见）。短命会话直接跳过，不花整理额度（见 [降噪](#降噪短命会话与排除名单)）。
- **整理器自己只有一个会话**：每个项目保留一个整理器会话并续用，不再每整理一件就新开一个 agent 会话——第一次整理照旧由 job 层给 agent 分配会话（agent 的 `session_inject`，如 `suga run --session-id` / `claude --session-id`），gofer 记下这次 job 的会话 id；之后每次整理续在这个会话里（agent 的 `session_resume`，即 `job resume` 那套，`suga --resume` / `claude --resume` / `codex exec resume`）。续用时提示开头会说明「这是新的一件工作，忽略之前的工作项」，每次提示仍自带完整上下文、仍只输出 JSON。累计整理 `work.summarizer_session_max_runs`（20）次或会话存在超过 `work.summarizer_session_max_age`（24h）后自动换新会话；续接失败（会话不存在 / agent 报错）时回退成新会话并记一条 `work.summarizer_session_reset` 事件，整理本身不失败。没有 `session_resume` 的 agent 维持旧行为（每次新会话）。
- **整理器会话是内部会话**：`gofer session ls`、web 工作台的会话列表与会话搜索默认不显示整理器产生的会话；`--all`（或接口的 `all=1` / `internal=1`）才列出来。旧版本产生的整理器会话只要能认出来（带整理 job 的会话 id，或在整理 job 运行期间登记的空会话）也一并隐藏。
- **写回规则**：只填空字段或覆盖上次也是整理器写的字段；人或会话写过的字段不覆盖，改存为「整理建议」，卡上一键采纳 / 忽略；状态建议只有采纳才改状态。
- **成本控制**：每会话两次整理最少间隔 `work.summarize_min_interval_min`（30），每日自动整理上限 `work.summarize_daily_limit`（50，-1 不限）；手动「整理」不受限。没有可用整理器 agent（需 cli-agent 且有只读模式）时自动整理关闭，设置页说明原因。整理 job 不注入 rules（agent / 项目的规则只用于正式任务）。
- **换整理器 agent**：`work.summarizer_agent` 填任一 cli-agent，它要有只读模式——claude / codex 内置，其他 agent 在定义里配 `read_only_args`（如某个 CLI 的「禁用全部工具」开关），否则整理器不可用。prompt 走 agent 自己 `args` 里的 `{{prompt}}`，`work.summarizer_args` 只是追加在后面的参数（便宜模型、不留会话、关思考之类），不需要写占位符；默认参数只适用于 claude，换 agent 时自己写。
- 设置：web 设置 →「工作项」页（管理员），对应配置的 `work:` 块，热生效。

## 降噪（短命会话与排除名单）

测试 / 试一试的会话不再变成工作项、也不进整理与管家。阈值与名单都是扩展设置（web 设置 →「扩展设置」，或 `gofer settings set <键> <值>`），改了立即生效：

| 键 | 默认 | 作用 |
|---|---|---|
| `work.draft.min_prompts` | 2 | 同一会话第几次人工提问才建草稿（1 = 第一次就建，回到旧行为） |
| `work.draft.exclude_projects` | 空 | 不建草稿的项目 key 名单（精确匹配） |
| `work.draft.exclude_cwds` | 空 | 不建草稿的目录名单（会话 cwd 以其中一项为前缀即命中；Windows 下不分大小写、`/` 与 `\` 等价） |
| `work.draft.exclude_title_patterns` | 空 | 不建草稿的标题正则 |
| `work.short_session.max_minutes` | 10 | 短命会话的时长阈值 |
| `work.short_session.max_prompts` | 3 | 短命会话的人工提问次数阈值 |

- **无项目不建**：会话不属于任何项目（hook 没报 `project_key`）时，提问不建草稿。
- **短命会话**：已结束或离线，且存活时间短于 `work.short_session.max_minutes`、人工提问少于 `work.short_session.max_prompts` 的会话，整理器跳过（在整理记录里留一条 `skipped` 和原因，不占每日额度），管家巡检、首条摘要里的未结列表、会话离线 / 结束事件也都跳过。两个阈值任一设为 0 即关闭该判据。
- 阈值只影响之后的判断：已经存在的草稿、已经发生的整理不受影响。
- 读设置失败（数据库错误）时退回默认值并在日志里记一条，整理与巡检不停摆。

## 改整理器与管家的提示词

三段提示词都能改，各有两个扩展设置键（web 设置 →「扩展设置」，或 `gofer settings set <键> <值>`），改了下一次整理 / 巡检即生效，不用重启：

| 提示词 | 整段替换（空 = 用代码默认） | 追加 |
|---|---|---|
| 管家角色（会话第一条说明） | `prompt.steward.role` | `prompt.steward.role.extra` |
| 管家巡检指令（每日 / 手动 / 事件三种都用） | `prompt.steward.review` | `prompt.steward.review.extra` |
| 整理器（一次整理 job 的提示词） | `prompt.work.summarizer` | `prompt.work.summarizer.extra` |

- **整段替换 vs 追加**：替换键非空就整段用它代替代码默认；`extra` 非空就追加在（默认或替换后的）提示词之后，前面加一行 `## 使用者补充` 分隔。推荐用 `extra` 写偏好——它跟着默认提示词一起升级；要完全控制才用替换。
- **追加也能替换不到的东西**：整理器要求的 JSON 输出字段说明永远由代码追加在最末尾，替换键改不掉它（否则解析会坏）；管家巡检的动态材料（本次要处理的工作项清单、待处理事件、`「今天」待决策卡`、记忆整理步骤）也仍由代码拼接，替换的只是指令正文，编号会自动接在后面。
- **默认提示词的取舍**：整理器只记录有实质进展的会话，试用 / 测试 / 演示性质的操作不算进展、不给建议，材料不足就写「信息不足」；管家巡检同样不给测试性事项提建议，优先真正卡住、等人、到期的事，建议要具体到下一步动作。
- **看实际生效的提示词**：整理器 job 的详情里有它收到的 prompt；管家会话的记录里有 prime 与每轮巡检的原文。
- 单条提示词上限 32 KiB；读设置失败时退回代码默认并记一条日志。

## 里程碑、健康度与「今天」泳道

- **里程碑**：状态变化、会话汇报、人手写的备注、关联 job 的有意义结束（成功且有提交 / 失败 / 预算熔断 / 验收结论）、关联决策被人回答、整理器给出的里程碑；其余是流水。Works 抽屉时间线默认「只看里程碑」。管家 / agent 写备注可带级别（`gofer_work_note` 的 `level: milestone|detail`）。
- **健康度**：`blocked`（关联 plan 阻塞，或等我 / 需现场且写了卡点）> `stalled`（进行中、没有 agent 在跑、超过 `work.stall_after`（默认 `4h`）没有日志或 job 活动）> `at_risk`（最近一个关联 job 失败或预算熔断）> `ok`。
- **泳道**：web「今天」页的「并行中」区，一行一件事（工作项或未挂到工作项的 plan）：在跑的 agent、进度点、健康度、耗时与用量；阻塞 > 停滞 > 有风险 > 有 agent 在跑排序。见 [web-console.md](web-console.md)。

## 提醒、摘要与通知

- **收集箱条目（`captured`）**：不在按状态的列表里占普通状态的位置，只在「收集箱」泳道（web 工作项页的状态 chip 可只看它）；汇报 / 整理器 / 管家 / job 凭据都不能把条目改成或改出该状态，也不改动它的状态；日报、整理扫描、健康检查、到期提醒、合并建议、Today 泳道都不含它，Today 只出一张「收集箱 N 条待处理」汇总卡。人通过 `PATCH` 把状态改出 `captured` 视同归档（写日志）。
- **搁置 / 提醒**：`work park --until`、`work remind`；server 每 30 秒扫描，到点发一次 `work.remind` 事件，卡片进「到期提醒」。
- **每日摘要** `work.digest`：默认每天 09:00（`work.digest_time`，`work.digest_enabled: false` 关闭）：等我 / 等资源 / 需现场 / 待验收计数、搁置超 7 天、昨日有进展的列表，带链接。`gofer work digest` 预览，`--send` 立即发。开了管家时附管家点评。
- **「等我」通知** `work.needs_me`：卡进入等我时发一次，默认关（`work.needs_me_notify`），同一张卡最小间隔 `work.needs_me_throttle_min`（30 分钟）。
- `work.remind` 与 `work.digest` 在默认订阅集里。没有任何 webhook 订阅时，`gofer steward status` 与 `gofer config validate` 会给出提示（`work.digest_enabled` 不写时不提示，显式写 `true` 或开了管家才提示）。通知配置见 <https://github.com/inhere/gofer/blob/main/docs/runbook/im-notification.md>。

## 管家（steward）

管家是一个**常驻的持续 ACP 会话 job**（标签 `steward`），只做**调度和整理**：读工作项 / 会话 / 请求，记备注、设提醒、提合并建议、请会话汇报、触发整理、给「今天」的卡写建议、提记忆整理建议、维护自己的笔记。**不干活、不拍板**：不能把卡设成 `done` / `dropped`，不能提交执行类 job、改配置、合并或删除；它的建议要人点「采纳 / 按建议」才执行，也不做超时自动通过。默认**关闭**（要花模型额度）。

```bash
gofer steward status [--json]          # 开关 / agent / 会话状态 / 笔记 / 最近巡检
gofer steward start | restart | stop   # 按需启动 / 重建（不丢信息）/ 结束会话
gofer steward ask "我手上还有什么没完成？" [--no-wait] [--timeout 180]
gofer steward notes [--version N | --history | --edit | --set-file <f|->]
gofer steward review [--force]         # 立即巡检（没变化的项不起会话，--force 强制）
gofer steward merges | merge-accept <n> | merge-dismiss <n>
```

- 配置（`steward:` 块，web 设置 →「工作项」页的「管家」区）：`enabled`、`agent`（任意已装的 acp-agent）、`project`（默认内置 `default`）、`review_time`（每日巡检，默认 08:50）、`idle_end_min`（空闲多久结束会话，默认 30）、`review_max_items`（默认 20）、`event_wake`（会话离线 / 到期 / 草稿多时是否唤醒，默认 false）、`event_throttle_min`（默认 30）、`approval`（会话自己的工具审批策略，见下条）。
- **管家会话自己的审批策略** `steward.approval`（结构与项目 `approval` 一样）：缺省 = `mode: ask` 并自动放行 gofer 只读 / 只提建议的工具**与管家本职的写工具**（`gofer_work_update` / `gofer_work_note` / `gofer_work_remind` / `gofer_work_request_report` / `gofer_work_summarize` / `gofer_steward_notes` / `gofer_session_ask`），所以记备注、设提醒、请会话汇报不会停下来等人；写文件 / 执行命令 / 其他 MCP server 的工具仍然问人，`mode: strict` 则一律问人。它**只**替代管家这一条会话的策略（以 job 的 `session_approval` 随 job 保存，`job resume`、会话续接、重启恢复都照旧），项目里其他 ACP 会话不受影响；只写 `mode` 时保留上面的默认工具名单，显式写 `auto_allow_tools` 才替换它。审批记录里这类自动放行的 `reason` 是 `gofer_steward_tool`。
- 会话可抛弃：关键信息都在工作项、日志、请求和管家笔记里，重启 / 换 agent 靠首条消息里的摘要接着做。
- 每日巡检只处理自上次巡检后有变化的项与还没看过的「今天」卡；没变化就不起会话。巡检末尾写一段点评附在当天的摘要里。短命会话的项既不巡检、也不进首条摘要的未结列表、也不产生会话离线 / 结束事件（见 [降噪](#降噪短命会话与排除名单)）。
- 问管家：Works 页右下角「问管家」面板（手机可用），或 `gofer steward ask`。
- 权限提醒：管家的 gofer 凭据只能读写工作项相关数据、提建议；server 无 token 运行时这层限制不生效。管家 agent 自带的 shell / 文件工具不受 gofer 凭据约束。

## MCP 工具

- 工作项：`gofer_work_list` / `gofer_work_get` / `gofer_work_update`（描述性字段，不含状态）/ `gofer_work_note` / `gofer_work_report` / `gofer_work_requests` / `gofer_work_request_report` / `gofer_work_summarize` / `gofer_work_capture`（`text` 必填，`kind` idea|issue|request|note 默认 note，`project` 可选；只写入收集箱，不派活）；只读会话：`gofer_session_list` / `gofer_session_get`；只读 issue：`gofer_issue_list` / `gofer_issue_get`。采纳建议没有 MCP 工具（那是人的决定）。
- 带话 `gofer_session_ask`：给一个**在线**会话捎一句话（终端会话走中继或传话；ACP / pty 会话 job 等同 `job say`）。会话不在线或送达失败直接报错，不排队；送达后记在工作项日志里。
- `--project` 收窄的 MCP 只看 / 改本项目的工作项，且没有 `gofer_session_ask`。
