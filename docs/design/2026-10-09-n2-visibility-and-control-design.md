# N2：可见与可控（v0.124–0.126）

> 状态：已确认分期（2026-10-08），实施中。总规划见 [`plans/2026-10-08-next-phases-plan.md`](../plans/2026-10-08-next-phases-plan.md)。
> 本文只定合同与取舍；实测记录在各节末尾追加。
> 分两波：第一波 A（OBS-14）、B（GATE-02）、C（TRK-05）、D（提交慢 gofer-5foz）并行；第二波 E（SESS-12 催办），依赖 A 的会话活动数据、与 A 同改会话中继代码。

## A. 终端会话用量（OBS-14）

- **采集点**：hook 侧，在 `Stop`、`SubagentStop`、`SessionEnd` 事件时增量读该会话 transcript（PostToolUse 不读，避免高频 IO）。偏移记在本机 `<config-dir>/run/hook-usage/<session_id>.json`（文件路径 + 字节偏移 + 已计消息 id 的有界集合），hook 进程短命，状态必须落盘；文件被截断或换路径时从头计并按消息 id 去重。
- **方言**：
  - claude：assistant 行的 `message.usage`（input / output / cache_read / cache_creation）。同一条消息会按内容块拆成多行且重复携带 usage，**按 `message.id` 去重**。`isSidechain: true` 的行计入子 agent；会话目录下 `subagents/*.jsonl`（若存在）全部计入子 agent。模型取 `message.model`，按模型分桶。
  - codex：rollout 里 `token_count` 事件携带累计值，取最后一条累计值与上次上报值之差。
  - generic：transcript 行若带 `usage` 对象则按 `runner.UsageFromObject` 读取；没有则不采集。
  - 其它方言暂不采集。
- **上报**：心跳新增 `usage_delta: {main: Usage, sub: Usage, by_model: {model: Usage}}`（`Usage` 与 job 用量同结构，snake_case）。server 累加进会话（additive 列 `agent_sessions.usage_json`），并按日聚合到 `session_usage_daily(day, session_id, project, agent, model, …)` 供统计。
- **展示**：会话卡 / 抽屉显示主会话与子 agent 两行用量；工作项卡显示所属会话之和；Home 用量卡新增「终端会话 24h / 7d」。成本：transcript 无费用字段时只显示 token，不臆造价格。
- **限制**：依赖各 agent 的私有 transcript 格式，解析失败只记 debug 日志、不影响 hook；server 重启不丢（已落库）。

## B. 预算熔断（GATE-02，job 级）

- `JobRequest.Budget {max_tokens, max_cost_usd, max_turns}`（均可选，0 = 不限）；默认值层级：agent `budget` < 项目 `budget` < 请求。`job run --max-tokens / --max-cost / --max-turns`、任务书 frontmatter `budget`、plan todo、MCP、web 新建表单。
- **计量**：沿用现有流式用量来源——claude stream-json 每条 assistant 消息的 usage（按 message.id 去重累加）、codex stderr 的 token_count、ACP `usage_update`、generic `ndjson_usage_path`。`max_tokens` 比较 input+output+cache 合计（与 job 用量的 total 口径一致）；`max_cost_usd` 只在来源提供费用时生效；`max_turns` = 模型请求次数（assistant 消息数 / ACP 回合数）。
- **超限**：立即按取消路径终止整棵进程树，job `failed`，`failure_class=budget`，错误文本写明哪个上限、实际值；事件 `job.budget_exceeded`（进默认通知集）；不触发自动续投 / 转移（budget 不是 transient）。
- 远程 worker：判定在执行侧（worker 本地 job.Service），协议只需随派发带 budget（可选字段，沿用 v19 之后的新版本号，按 G032 写清门槛：旧 worker 收到带 budget 的 job 在提交时被拒，不静默忽略）。
- **落地口径（2026-10-09，gofer-n33p）**：①`config.Budget` 是共享类型（请求 / agent / 项目 / 任务书 / todo / worker 帧），wire 上是 `wsproto.Budget`（协议 v20，`BudgetMinProtocolVersion`）；②计量器 `runner.BudgetMeter` 由 job.Service 在执行侧建立，喂数点：ndjson 投影（claude 按 `message.id` 去重取高水位、omp `message_end`、`ndjson_usage_path`）、acp 处理器（`usage_update` 高水位 + 每个 prompt 回合一次 `AddTurn`）、codex stderr 尾部（只在结束时可得，事后判）；claude 费用只在末尾 `result` 行，也是事后判；③「超过」才算超限（`max_turns=N` 允许第 N 次请求完成）；④超限错误以 `budget exceeded: ` 开头，host 按文本前缀归类 `failure_class=budget`（远程 worker 只回传文本），`job.budget_exceeded` 在 finish 时统一补记；⑤不可计量的 agent（exec / pty / 文本 cli-agent）显式带预算 400，默认值层级合并来的预算对其静默不生效；⑥带预算（含默认值合并来的）提交给协议 < v20 的 worker 在提交时 400，派发期 `unsupportedDispatchFields` 再兜底；⑦resume 继承源 budget、按维度覆盖，计量从 0 重新开始；`maybeRetryJob` 对 budget 失败不重试（workflow 步骤级 retry 本期未处理）。
- 会话级预算（终端会话）本期只做告警，依赖 A，放到第二波评估。

## C. web Issues 页同步（TRK-05）

- `POST /v1/tracker/repos/{tracker_id}/sync`：server 找到该仓库最近一次同步来源（runner + 仓库路径），没有来源时用 `project_key` + `rel_path` 在项目默认 runner 上解析；派一个内部 exec job（tag `tracker-sync`，Board 默认隐藏），在仓库目录执行 `gofer repo sync`，job 内经 job 凭证调用 `/v1/tracker/sync`（确认 member 凭证白名单放行该路由，仅限本仓库）。返回 job id；web 轮询该 job 与仓库 `last_sync_at`。
- Issues 页仓库选择旁加「同步」按钮，显示进行中 / 结果（成功时间、失败原因）。
- 只允许人（user / admin）触发；job / steward 凭证 403。

**短 id（2026-10-09）**：`tracker_id` 由 UUID 改为 `tracker-<10 位小写十六进制>`（`tracker.NewTrackerID`，crypto/rand）。旧仓库在 `gofer repo sync` 时迁移：新 id = `tracker.ShortTrackerID(旧)`（`tracker-` + sha256(旧) 前 10 hex，确定性，所有克隆一致），先调 `POST /v1/tracker/repos/{old}/rename`（单事务改 tracker_repos/issues/memories；幂等，新旧并存 409，`new_tracker_id` 必须等于派生值；人凭证或关联旧 id 的 job 凭证），成功再改写 config.yaml；失败沿用旧 id 同步。同步 job 凭证绑定旧 id，因此 `/v1/tracker/sync` 的范围检查同时接受 `bound` 与 `ShortTrackerID(bound)`。迁移代码标 `DEPRECATED(v0.126): remove in v0.129`（G032）。

## D. 提交慢（gofer-5foz）

- 先测量：在 `Submit` 各阶段打耗时（校验、项目解析、目录锁判定、GIT-01 基线快照、落库、派发），超过阈值记 warn。
- 预期修复方向：GIT-01 的「开跑前未提交改动快照」若在提交路径同步执行，移到 job 启动时（或异步 + 超时），大工作区不阻塞提交；`git status` 加 `--untracked-files=no` 或限定 pathspec 等选项按需取舍，保持 GIT-01 语义。
- 验收：在含大量嵌套仓库的工作区根提交 exec job，提交返回 < 2s；GIT-01 行为不变（未提交改动检测结果一致）。

## E. 会话催办（SESS-12，第二波）

- `gofer session nudge <sid> (--every 30m | --when-stalled 20m) -m "…" [--until …]`，REST / web 会话抽屉；复用 schedule sweeper 与送话阶梯（中继 → 传话 → 送话）。
- 停滞 = 会话 `running` 且距最后进展（心跳 progress / Stop / 用量增长，来自 A）超过阈值，且关联工作项未结。
- 送达失败不重试轰炸：同一 nudge 连续失败 3 次自动暂停并通知。

## 验收总表

| 项 | 验收 |
|---|---|
| A | 真实 claude 会话（含子 agent）Stop 后会话卡显示主 / 子用量且与 transcript 手算一致；重复事件不重复计；codex 会话有用量 |
| B | 低 `--max-tokens` 的真实 claude job 被终止为 `failure_class=budget` 并通知；不设预算行为不变；远程 worker 生效 |
| C | web 点同步后仓库 `last_sync_at` 更新、新 issue 出现 |
| D | 大工作区根提交 < 2s，GIT-01 结果不变 |
| E | 停滞会话按阈值收到催办，失败 3 次暂停 |
