# N1：卡点与通信（v0.123–0.124）

> 状态：已确认分期（2026-10-08），实施中。总规划见 [`plans/2026-10-08-next-phases-plan.md`](../plans/2026-10-08-next-phases-plan.md)。
> 本文只定合同与取舍；实测记录在各节末尾追加。

## A. 摘要 / 提醒 0 订阅可见（OBS-13，gofer-ribu）+ 工作项小修（gofer-gmg6、gofer-eb2k）

- 发送 `work.digest` / `work.remind` / `work.needs_me` 时，匹配到 0 个 webhook：记 `slog.Warn("work.notify_no_subscriber", event=…)`；**摘要 0 订阅时不写 `digest_last_date`**（订阅补上后当天仍会发）。成功发送记一条 info（目标数）。
- `gofer steward status` 与 `GET /v1/steward`：digest 开启（或管家开启）但没有任何 webhook 会匹配 `work.digest` 时，给出一行警告。
- `gofer config validate`：同一判断给 warning（不是 error）。
- gmg6：`work ls --all` 与 `work rm --status` 对 dropped 的口径统一（被合并源项是否列出要一致，`--all` 应包含被合并源项或明确说明并提供开关）。
- eb2k：`work.deleted` 审计不再写进 `job_events`；落到工作项自己的审计（如新增 `work_audit` 表或通用 `audit_events`），带 `kind`、`target_id`、`actor`、`at`，不含标题。旧数据一次性迁移（additive，G032 标记）。

**实施记录（A）**：
- OBS-13：`NotifyWork` 统一记 `work.notify_no_subscriber`(warn) / `work.notify_sent`(info)；摘要 0 订阅不写 `digest_last_date`，重试间隔 30 分钟（避免每 30s tick 刷警告）；判断共用 `notify.DigestNoSubscriberWarning`（steward status / `GET /v1/steward` 的 `warnings` / `config validate` 的 `[WARN]`）。
- gmg6：选 “`--all` 同时包含被合并源项并标注「已并入 X」”，与 `rm --status` 预览同口径；不加额外开关。
- eb2k：新增通用 `audit_events` 表（kind / target_id / actor / at / detail_json）；旧 `job_events.work.deleted` 在 Open 时一次性迁入（`work_kv` 标记防重复扫描；`DEPRECATED(v0.123): remove in v0.126`）。

## B. 指定模型（AGT-06，gofer-e71i）

- agent 配置新增 `model_args`（argv 片段，含 `{{model}}`），渲染时插在 `{{prompt}}` 所在参数之前；未配置时，内置 claude / codex 模板给默认值（claude `--model {{model}}`，codex `-m {{model}}`），acp-agent 走 `session/set_model`（若协议支持；不支持则报错说明）。
- `JobRequest.Model`；`job run --model`、任务书 frontmatter `model`、plan todo `--model`、web 新建 job / 会话表单、MCP `gofer_run_job.model`。
- 续接（resume）沿用源 job 的 model，`--model` 可覆盖。
- job 记录与详情显示 model；未指定 = 空（agent 自身默认）。
- 不指定 `--model` 时行为完全不变。

**落地记录（gofer-e71i）**

- 渲染：`agent.WithModelArgs` 把 `model_args` 插在首个含 `{{prompt}}` 的模板参数之前，无此参数（交互 / 交互 resume 模板）追加末尾，均在 `--agent-arg` 与 `read_only_args` 之前；`global_args` 先于整个模板，resume 的 `GlobalArgs` 前缀不变。内置：claude `--model {{model}}`、codex `-m {{model}}`（`claude --help` 实测 `--model <model>`；codex 落在 `exec` 之后）。按 agent key、其次 command 基名补默认（同 `read_only_args`），显式 `model_args` 优先；config 校验：仅 cli-agent、须含 `{{model}}`、不得含 `{{prompt}}`。
- ACP：自研 client 新增 `session/set_config_option`（category `model` 的 select 项）与 `session/set_model`（`models` 块），会话建立后、首轮 prompt 前调用；agent 未暴露或不含该 id 则 job failed 并列出可选值。
- 存储：`model` 进 `request_json`，`JobResult.model` 从请求派生，不加 jobs 列；plan todo 新增 additive 列 `plan_todos.model`。
- worker：Dispatch/Forward 增加 `model`，协议 v19（`ModelMinProtocolVersion`），< v19 的 worker 提交即拒（静默跑默认模型会说谎）。
- 续接：继承源 job 的 model，`--model`（HTTP resume body `model`）覆盖；继承的 model 目标 agent 不支持时丢弃，显式给的 400。rebuild 继承并可改。
- 取舍：模型值只做语法校验（不以 `-` 开头、无空白 / 控制字符、≤200B），不维护模型白名单；resume 不提供“清回 agent 默认”。

## C. Stop 等待感知子 agent（SESS-10，gofer-4q5i）+ agent 内部会话忽略（gofer-v74l）

- **子 agent 计数**：`gofer init hooks`（claude）增装 `SubagentStart` / `SubagentStop`（命令同 `gofer hook claude`）。hookrelay 把这两个事件上报为心跳的 `subagent_delta`（+1/-1，带 agent_id 去重）；server 在会话上维护「在跑子 agent 数」（带过期：最长 2 小时无事件归零，防止丢 Stop 事件导致永久不布防）。
- **布防判定**：SUP-01 D 的「监督中」条件增加「该会话在跑子 agent > 0」（与在跑 gofer job 同级），auto 模式不布防，直接放行 Stop。
- **已阻塞时**：收到 SubagentStop 且计数归零（或任意 SubagentStop，取简单稳妥者并在实测后确定），server 把该会话 OPEN turn 关成 `released_by=subagent_done`，hook 下一次轮询放行。**前提需实测**：Claude Code 在 Stop hook 阻塞期间是否会触发 SubagentStop；若不会，退化为仅靠「布防前判定」+ 等待预算。
- **等待预算**：心跳响应新增 `wait_budget_sec`（server 按原因给：on → `session.relay_on_wait_sec` 默认 3600；auto 布防 → `session.relay_auto_wait_sec` 默认 600；job watch 不受影响）；hook 取 `min(--wait, wait_budget_sec)`。旧 server 不返回该字段时 hook 行为不变。
- **内部会话忽略（v74l）**：hook 在上报前检查 cwd：位于忽略目录（默认 `~/.codex/memories`；`GOFER_HOOK_IGNORE_CWDS` 环境变量或 server 下发列表扩展，用路径前缀匹配、大小写按平台）即直接退出 0，不登记、不建工作项。已存在的这类会话 / 工作项不自动清理（用 `work rm` 人工处理）。

## D. plan 页与主会话（PLAN-06，gofer-6ztx）+ reload 误报（gofer-1zg9）+ 旧脚本清理（gofer-syix）

- PlanDetail 显示绑定会话（名称、状态、最后心跳、打开会话抽屉），可修改 / 清除（复用 PATCH plan `supervisor_session_id`）；提供「发给主 agent」输入框，复用 web 给会话发消息的同一入口（中继 → 传话 → 送话阶梯），常用快捷语「请写交接说明并更新 plan handoff」。
- reload 结果关联：CLI 写入请求 nonce（或记录发信号时间），server 写结果文件时带回 nonce / `reloaded_at`，CLI 以此判断新结果，不再依赖 rev 递增。Windows 命名事件与 Unix SIGHUP 两条路径一致。
- 删除 `scripts/start.ps1`、`win-supervisor.ps1`、`win-selfupdate.ps1`、`win-selftest.ps1`、`win-tasktest.ps1` 中仅服务旧自更新的部分（逐个确认无其它引用）；旧 runbook 顶部标注「已由受管服务取代」并指向新 runbook；roadmap CFG-08 / SVC-01 标「被 SVC-05 取代」。

## E. codex 会话送话（SESS-11，gofer-ueib）

- 主机 codex agent 配置 `deliver_command`，经一个小包装（PowerShell 或 gofer 内置 `gofer tool codex-queue`）调用 `codex queue --thread {{session_id}} --message {{text}}`，把「会话不存在」映射为退出码 3。
- 先用两个真实 codex 会话实测：会话 id 是否等于 `--thread` 接受的 UUID、离线时 queue 的行为。结论写入 `runbook/session-relay.md`。

### 实施记录（E）

- 不做包装脚本 / `gofer tool codex-queue`：新增 agent 配置项 `deliver_offline_match`（Go 正则，需 `deliver_command`；加载校验可编译）。非 0 退出且 stdout+stderr 匹配 → 按 exit 3（`not_running`）处理。判定在 server 侧 `sessionrelay.deliverCommand`（`CommandPlan.OfflineMatch`，由 `Server.deliverPlan` 编译），远程 worker 的送话 job 同样先回到 server 再判，**无协议字段、无版本变化**。
- 内置默认：`config.BuiltinDeliver("codex")` = `deliver_command: [queue, --thread, {{session_id}}, --message, {{text}}]` + `deliver_offline_match: "no rollout found"`，由 `agent.applyDeliverDefaults` 填充（与 `model_args` 同一继承规则：按 agent 名、再按 command 基名；显式声明了 `deliver_command` / `deliver_stdin` 则整套用显式的）。codex-acp 不加。
- web 配置页 agent 表单加 `deliver_offline_match`（`editable.go` 白名单 + 写接口 + 读接口）。
- 已知限制：已退出的会话 `codex queue` 也 exit 0 并排队到下次 resume。

## 验收总表

| 项 | 验收 |
|---|---|
| A | 去掉订阅时日志 / status / validate 有提示且不标记当天；补订阅后当天可发；计数口径一致；审计不再进 job_events |
| B | `--model` 在 claude / codex 各一条真实 job 生效；todo / web / MCP 可传；不传时 argv 不变 |
| C | 子 agent 在跑时 Stop 不阻塞；auto 等待 ≤ 预算；`~/.codex/memories` 会话不登记 |
| D | web 可改绑定会话并发消息到达；reload 不再误报；旧脚本删除后文档无断链 |
| E | 两个 codex 会话互送成功，会话不存在走下一阶梯 |
