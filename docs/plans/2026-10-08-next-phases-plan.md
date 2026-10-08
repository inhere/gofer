# 下几期开发规划（N1–N4，2026-10-08）

> 状态：草案，待用户确认分期后逐期出 design / plan。
> 输入：用户问题与想法清单（2026-10-08）、一份外部 AI 讨论记录（下文「外部建议」）、未结 issue、本轮 suag 复审中暴露的 gofer 问题。
> 现状依据：v0.122.0 代码与 [roadmap](../gofer-enhancements-roadmap.md) 已落地表（本次已补齐 v0.67–v0.122）。

## 1. 用户痛点（排序依据）

1. **和 agent 之间的通信不顺**：主 agent 卡在 Stop 等待里收不到子 agent 完成通知；codex 会话收不到传话；想在 plan 页直接跟主 agent 说话。
2. **多项工作并行时注意力分散**：Workbench / Works / 管家各自一块，每天打开没有「我现在要决定什么」的单一入口。
3. **看不见花了多少**：主 agent 与其子 agent 的用量没有数据；也没有超限保护。
4. **已有功能静默失效**：开了管家与每日摘要却从没收到（0 订阅被静默丢弃）。

## 2. 用户清单逐条结论

| # | 条目 | 现状 | 结论 / 归属 |
|---|---|---|---|
| 1 | codex 会话间 `codex queue --thread` 传话 | codex 有该子命令；gofer 送话阶梯已支持 `deliver_command`，codex agent 未配置 | 配置 + 实测即可（SESS-11，N1）；需包一层把「会话不存在」映射为退出码 3 |
| 2 | 定时回传 / 催办停滞的主 agent | job 场景有 wakeup / session watch；终端会话无定时送话、无停滞判定 | SESS-12（N2）；过渡办法：schedule 派 exec job 执行 `gofer session say --deliver` |
| 3 | 主 agent 与子 agent 的用量 | 只有 job 级 `usage_json`；终端会话无采集；transcript 里有 `message.usage` | OBS-14（N2），GATE-02 预算熔断依赖它 |
| 4 | 插件机制 | webhook 外发、rules、skills 已有；事件插件未做 | AUTO-04 先做只读 `kind: exec`（N4） |
| 5 | Issues 页同步按钮 | `repo sync` 只能在仓库目录跑；server 无主动入口 | TRK-05（N2） |
| 6 | runner 声明可用工具 | 缺失（worker 注册只报 labels / agents） | CFG-16（N4）；过渡：项目 / agent 级 rule 手写 |
| 7 | 指定模型 | 无 `--model`；可用 `--agent-arg` / `--env` 绕 | AGT-06（N1） |
| 8 | plan 绑定主 agent 会话、web 直接通讯 | 单会话绑定已有（`supervisor_session_id`），web 未展示、无发送入口 | PLAN-06（N1，小）；多会话 PLAN-07（N3） |
| B1 | 管家开着但 9 点没收到报告 | 摘要已生成，webhook 显式 `events` 未含 `work.digest`，0 订阅静默丢弃且标记当天已发 | 配置立即可修；代码侧 OBS-13（N1） |
| B2 | 内置子 agent 开发时 Stop hook 长时间卡住 | SUP-01 D 只计 gofer job；auto 按「距上次人工输入 15 分钟」布防，等待 7140s | SESS-10（N1）：子 agent 感知 + 等待预算下发；单纯缩短等待只是减轻 |

## 3. 外部建议评估

外部建议基于「gofer 只是命令网关」的假设，不少点 gofer 已具备，需要打折看：

| 建议 | gofer 现状 | 评估 |
|---|---|---|
| 不可变沙箱编排 | worktree、同目录锁、容器 worker、只读 job、job 作用域凭证 | 个人 / 小团队场景收益有限、成本大；留 SBX-01 远期 |
| MCP 中央网关 | `gofer mcp` 服务端、管家自动注入 gofer MCP、suag 支持 `--mcp-config` | 「server 托管 MCP 注册表并按项目注入」有价值（MCP-06，N4）；不做请求级代理 |
| Git / PR 闭环 | worktree、`job worktree merge`、对比择优、verify、提交捕获 | 只缺「推分支 / 开 PR」；push 需授权的约定下优先级低（GIT-02，N4） |
| 多端审批 | 钉钉 / 飞书出站、Web Push 通知内审批、PWA | IM 入站用户已明确暂不做；不新增 |
| Token 熔断 | 用量采集已覆盖 job | 合理，补 GATE-02（N2），先 job 后会话 |
| 协议垫片 Shim | ACP、ndjson 投影、generic hook + transcript 方言 | 已具备，不做 |
| 上层调度管家 | 管家 steward（巡检、带话、汇报请求、笔记） | 方向一致；「替人自动批低风险决策」风险高，N3 只做「建议 + 一键采纳」，不自动批准 |
| 里程碑时间线 / 决策中心首页 | Works 卡片 + 日志、工作台、「等我」徽标 | **与痛点 2 最贴合**，作为 N3 主题（WEB-17 / WORK-06） |

采纳的核心思路：**认知压缩**——首页只放「待我决策」，工作进展压缩成里程碑，细节可逐层下钻；管家负责压缩与建议，不越权替人决策。

## 4. 分期

### N1 卡点与通信（v0.123–0.124，小而痛）

| 项 | 要点 | 验收 |
|---|---|---|
| SESS-10 Stop 等待感知子 agent | `init hooks` 增装 SubagentStart / SubagentStop（claude）；server 按会话计数在跑子 agent，计入 SUP-01 D（有子 agent 在跑时 auto 不布防）；已阻塞时 SubagentStop 到达即释放 turn。心跳响应下发 `wait_budget_sec`（on / auto / 原因分别配置，auto 默认 600），hook 取与 `--wait` 的较小值 | 子 agent 运行中 Stop 不阻塞；子 agent 完成后 ≤5s 放行；auto 兜底 10 分钟；on 语义与文档一致。需先实测 SubagentStop 能否在 Stop 阻塞期间并发触发 |
| OBS-13 摘要 0 订阅可见 | 发送 targets=0 记 warn；`steward status` / `config validate` 提示「已开 digest 但无订阅」；0 订阅不写当天标记 | 去掉订阅后日志与 status 有提示；补订阅当天仍能发出 |
| AGT-06 指定模型 | agent 配置 `model_args`（如 `["--model","{{model}}"]`），插在 `{{prompt}}` 前；`job run --model`、plan todo、web 新建表单、MCP 参数 | claude / codex 各一条实测，job 详情显示所用模型 |
| SESS-11 codex 送话 | codex agent 加 `deliver_command`（包装脚本把找不到 thread 映射为 3）；runbook 记录 | 两个真实 codex 会话互送消息；会话不存在时走下一阶梯 |
| PLAN-06 plan ↔ 主会话 | PlanDetail 显示 / 修改绑定会话，复用会话发消息入口（中继 / 传话 / 送话阶梯） | web 发「请写交接说明」能到达主会话 |
| 小修 | SVC-05 收尾删除 `start.ps1` / `win-supervisor.ps1` / `win-selfupdate.ps1` 及旧 runbook 指向；`work ls --all` 与 `rm --status` 计数不一致；`work.deleted` 审计改记独立事件表或带类型标记 | 各自测试 |

N1 跟踪 issue：gofer-4q5i（SESS-10）、gofer-ribu（OBS-13）、gofer-e71i（AGT-06）、gofer-ueib（SESS-11）、gofer-6ztx（PLAN-06）、gofer-syix（旧脚本清理）、gofer-gmg6（work 计数不一致）、gofer-eb2k（删除审计落表）。

### N2 可见与可控（v0.125–0.127）

| 项 | 要点 |
|---|---|
| OBS-14 会话用量 | Stop / SessionEnd hook 增量读 transcript（记偏移），按 `isSidechain` / 子 agent transcript 拆主会话与子 agent，上报 `session_usage`；会话卡、工作项、Home 卡显示 24h 用量；方言按 agent 区分（claude / codex / generic） |
| GATE-02 预算熔断 | job 请求与 agent / 项目默认 `budget: {max_tokens, max_cost_usd, max_turns}`；ndjson / ACP 流式累计超限即终止，状态 `failed` + `failure_class=budget`、通知；会话级只告警不杀 |
| SESS-12 催办 | `gofer session nudge <sid> --every 30m|--when-stalled 20m -m "…"`，复用 schedule sweeper 与送话阶梯；停滞 = 最后进展时间超阈值且关联工作项未结 |
| TRK-05 Issues 同步 | `POST /v1/tracker/repos/{id}/sync` → 在仓库所属 runner 派 exec job 跑 `gofer repo sync`；Issues 页按钮 + `last_sync_at` |

### N3 决策中心首页（v0.128+，先设计后实施）

- **WEB-17「今天」页**（替代无 Home 的现状，成为默认落地页）：
  1. 待我决策队列：pending interaction、OPEN decision、needs_review、needs_me 工作项，统一卡片，卡上直接操作（批准 / 拒绝 / 采纳 / 回复）；每张卡一句人话摘要（优先用汇报 / 整理器的 summary 字段，不另调模型）。
  2. 工作项里程碑墙：每个进行中工作项一列短时间线，点击下钻到抽屉（字段、日志、会话、diff）。
  3. 底部状态条：今日用量（OBS-14）、管家今日动作数、runner 水位。
- **WORK-06 里程碑**：工作项日志加 `level`（milestone / detail），状态变化、汇报、job 终态、decision 为 milestone；整理器产出时标注。
- 管家扩展：巡检时给待决策卡写「建议」（不自动批准），人一键采纳。
- 交付方式：先出 design + HTML 原型（`docs/design/web-v3-preview.html` 同类），用户确认后实施；与 WEB-11 W4 一并评估。

### N4 扩展与生态

CFG-16 runner 工具清单 → AUTO-04 只读事件插件（`kind: exec`）→ MCP-06 托管 MCP 注册表 → GIT-02 推分支 / PR；SBX-01 容器沙箱、SEC-02 独立 OS 账号为远期。

## 5. 未结 issue 归属

| issue | 处理 |
|---|---|
| h-aii-hnr3 CodeQL allocation-size-overflow（进行中） | 继续，N1 期内收尾 |
| h-aii-pq8a 全量负载下偶发失败的 job 测试 | N1 小修 |
| h-aii-3cro Windows CI 提速 | N2 期间穿插 |
| tools-tsq 外部会话领养（进行中） | 核对与 SESS-07 唤醒 / 接管的重叠，未覆盖部分并入 N2 |
| h-aii-4smj WEB-11 W1 会话中枢（blocked） | W1–W3 已落地，核对后关闭或并入 WEB-17 |
| gofer-a8ad 升级 w-mac-win10 | 运维事项，worker 上线后 `worker upgrade` |

## 6. 立即可做（不等分期）

- 钉钉 webhook 的 `events` 加 `work.digest`（可加 `work.remind`、`work.needs_me`），恢复每日摘要；当天可 `gofer work digest --send` 补发。
- 指定模型的过渡写法：claude 用 `--env ANTHROPIC_MODEL=<model>` 或 `--agent-arg=--model --agent-arg=<model>`。
