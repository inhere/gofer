# 工作项（gofer work）与管家（steward）

> 同时开着多个终端会话时，gofer 看得到「会话在不在跑」，看不到「做到哪、卡在哪、下一步是什么」。**工作项**就是这层记录：一张卡 = 一件事，跨会话（会话结束后被唤醒、换 agent 接手、换机器继续都挂在同一张卡上）。web「Works」页（`/work`，手机优先）是总览。管家是可选的常驻 agent，帮你整理这些卡。完整 flag 以 `gofer work --help` / `gofer steward --help` 为准。

## 目录

- [什么时候会碰到工作项](#什么时候会碰到工作项)
- [被要求汇报 / 写交接时](#被要求汇报--写交接时)
- [状态与自动映射](#状态与自动映射)
- [命令](#命令)
- [请求账本与被动整理](#请求账本与被动整理)
- [里程碑、健康度与「今天」泳道](#里程碑健康度与今天泳道)
- [提醒、摘要与通知](#提醒摘要与通知)
- [管家（steward）](#管家steward)
- [MCP 工具](#mcp-工具)

## 什么时候会碰到工作项

- 新会话**第一次有人工提问**时自动建一张草稿（标题取提问首行，进「未整理」区）；补上目标或点「已整理」后进入看板。
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
gofer work show <id>                       # 字段（含来源）+ 会话 + 关联 + 日志 + 建议 + 请求；id 可用唯一前缀
gofer work new "标题" [--goal ..] [--project ..] [--session <sid>]...
gofer work set <id> [--title|--goal|--status|--blocker|--blocker-kind|--next|--summary|--project|--workspace|--priority N] [--auto] [--sorted] [--rev N]   # 值给 - 清空
gofer work note <id> "备注"
gofer work park <id> [--until 2d] [--note "到货后继续"]
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
- **被动整理**：会话空闲 / 等回复满 `work.summarize_idle_min`（15）分钟、或离线 / 结束，且有新活动时，server 起一个一次性、只读、无工具的整理 job（agent 默认 `claude`，便宜模型；项目依次取 `work.summarizer_project` → 工作项自己的项目 → 内置 `default` 项目）。输入是会话记录尾部，输出目标 / 进展 / 卡点 / 下一步 / 状态建议 / 里程碑。整理 job 带内部标签，job 列表默认隐藏（`--all` 可见）。
- **写回规则**：只填空字段或覆盖上次也是整理器写的字段；人或会话写过的字段不覆盖，改存为「整理建议」，卡上一键采纳 / 忽略；状态建议只有采纳才改状态。
- **成本控制**：每会话两次整理最少间隔 `work.summarize_min_interval_min`（30），每日自动整理上限 `work.summarize_daily_limit`（50，-1 不限）；手动「整理」不受限。没有可用整理器 agent（需 cli-agent 且有只读模式）时自动整理关闭，设置页说明原因。
- 设置：web 设置 →「工作项」页（管理员），对应配置的 `work:` 块，热生效。

## 里程碑、健康度与「今天」泳道

- **里程碑**：状态变化、会话汇报、人手写的备注、关联 job 的有意义结束（成功且有提交 / 失败 / 预算熔断 / 验收结论）、关联决策被人回答、整理器给出的里程碑；其余是流水。Works 抽屉时间线默认「只看里程碑」。管家 / agent 写备注可带级别（`gofer_work_note` 的 `level: milestone|detail`）。
- **健康度**：`blocked`（关联 plan 阻塞，或等我 / 需现场且写了卡点）> `stalled`（进行中、没有 agent 在跑、超过 `work.stall_after`（默认 `4h`）没有日志或 job 活动）> `at_risk`（最近一个关联 job 失败或预算熔断）> `ok`。
- **泳道**：web「今天」页的「并行中」区，一行一件事（工作项或未挂到工作项的 plan）：在跑的 agent、进度点、健康度、耗时与用量；阻塞 > 停滞 > 有风险 > 有 agent 在跑排序。见 [web-console.md](web-console.md)。

## 提醒、摘要与通知

- **搁置 / 提醒**：`work park --until`、`work remind`；server 每 30 秒扫描，到点发一次 `work.remind` 事件，卡片进「到期提醒」。
- **每日摘要** `work.digest`：默认每天 09:00（`work.digest_time`，`work.digest_enabled: false` 关闭）：等我 / 等资源 / 需现场 / 待验收计数、搁置超 7 天、昨日有进展的列表，带链接。`gofer work digest` 预览，`--send` 立即发。开了管家时附管家点评。
- **「等我」通知** `work.needs_me`：卡进入等我时发一次，默认关（`work.needs_me_notify`），同一张卡最小间隔 `work.needs_me_throttle_min`（30 分钟）。
- `work.remind` 与 `work.digest` 在默认订阅集里。没有任何 webhook 订阅时，`gofer steward status` 与 `gofer config validate` 会给出提示。通知配置见 <https://github.com/inhere/gofer/blob/main/docs/runbook/im-notification.md>。

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

- 配置（`steward:` 块，web 设置 →「工作项」页的「管家」区）：`enabled`、`agent`（任意已装的 acp-agent）、`project`（默认内置 `default`）、`review_time`（每日巡检，默认 08:50）、`idle_end_min`（空闲多久结束会话，默认 30）、`review_max_items`（默认 20）、`event_wake`（会话离线 / 到期 / 草稿多时是否唤醒，默认 false）、`event_throttle_min`（默认 30）。
- 会话可抛弃：关键信息都在工作项、日志、请求和管家笔记里，重启 / 换 agent 靠首条消息里的摘要接着做。
- 每日巡检只处理自上次巡检后有变化的项与还没看过的「今天」卡；没变化就不起会话。巡检末尾写一段点评附在当天的摘要里。
- 问管家：Works 页右下角「问管家」面板（手机可用），或 `gofer steward ask`。
- 权限提醒：管家的 gofer 凭据只能读写工作项相关数据、提建议；server 无 token 运行时这层限制不生效。管家 agent 自带的 shell / 文件工具不受 gofer 凭据约束。

## MCP 工具

- 工作项：`gofer_work_list` / `gofer_work_get` / `gofer_work_update`（描述性字段，不含状态）/ `gofer_work_note` / `gofer_work_report` / `gofer_work_requests` / `gofer_work_request_report` / `gofer_work_summarize`；只读会话：`gofer_session_list` / `gofer_session_get`；只读 issue：`gofer_issue_list` / `gofer_issue_get`。采纳建议没有 MCP 工具（那是人的决定）。
- 带话 `gofer_session_ask`：给一个**在线**会话捎一句话（终端会话走中继或传话；ACP / pty 会话 job 等同 `job say`）。会话不在线或送达失败直接报错，不排队；送达后记在工作项日志里。
- `--project` 收窄的 MCP 只看 / 改本项目的工作项，且没有 `gofer_session_ask`。
