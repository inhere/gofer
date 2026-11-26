# LoopForge 调研：可借鉴项（2026-11-15）

> 参考文档，只做参考。来源：公开仓库 Tencent/LoopForge（MIT，Go，2026-07 创建）源码阅读，未运行；公开版由私有上游导出，私有部分行为未核实。已排除 `2026-11-11-super-agent-workflow-survey.md` 里已有的 ①–㉗。

## 定位

自托管团队平台：围绕多种编码 CLI 提供角色、Docker 沙箱、SOP（Agent / Script / Human gate / Sub-SOP / Preview 节点）、人工门禁与交付；另有模型驱动的 Main Bot 对话。架构：`internal/engine`（SOP 图、租约、沙箱、交付）、`internal/repo`（MySQL）、`cmd/agentflow-runner`、`cmd/agentflow-gate-mcp`。

## 可借鉴（按单人多 job 的有用程度）

| # | 机制 | LoopForge 出处 | gofer 现状 | 落点 / 规模 |
|---|---|---|---|---|
| L1 | agent 自报结构化终态：单工具 MCP `submit_gate_result`（decision = pass / rework / fail / human，reason 必填，human 带 actionRequired，按节点与 attempt 排他写入）；缺终态时续同一会话只要求「提交、不要重做」，补一次，仍无则转人工 | `internal/gatemcp/server.go`、`internal/engine/gate_submit.go`、`node_execute.go:815-845` | 终态只看退出码；有 `--verify`、交付核验、`gofer_ask_human`，agent 无法声明「做不了 / 需要人决定」 | gofer：MCP 工具 + job 字段 + 路由（human 进「等我」，fail 即使 exit 0 也判失败）；小到中 |
| L2 | workflow 有上限的打回返工：`rejectTo` / `maxReworks` / `maxAttempts`；验证门禁失败可 loop / reject / fail / human；人工 reject 可打回任意上游节点；「仍然放行」只适用于确定性失败 | `internal/domain/workflow.go`、`docs/api/orchestration.md` | `on_failure` 只有 fail / continue / retry；`wf rerun --from` 另起新 workflow | gofer workflow 引擎，依赖 L1；中到大 |
| L3 | 执行诊断档案：每次尝试的 pid、信号、输出行数、首末输出时间、阶段时间线；保留最早 stderr 与 firstError；终止原因枚举；确定性分析器输出 evidence / unknowns / nextSteps 并脱敏 | `docs/proposals/cli-execution-diagnostics.md`、`internal/domain/assistant_diagnostic_analysis.go` | 有 error / exit_code / stop_reason / log head；无统一终止枚举，续跑链不保留首错 | gofer：`gofer job diagnose <id>`；中 |
| L4 | 动作绑定轮次：按钮动作带 `{status, attempt, reworkCount}`，状态变了即拒绝 | `internal/engine/bot_control.go`、`internal/httpapi/bot_interactions.go` | 单 job 风险小；做 L2 后同一步多轮时需要 | gofer；小 |
| L5 | 终态 run 复盘：确定性构建事实（节点耗时、错误汇总、同一工具重复调用计数）→ 模型给白名单动作建议（改节点、加门禁、追加说明）→ 人挑选后应用到模板 | `internal/runeval/facts.go`、`internal/runeval/apply.go` | 调研 ⑲ 面向规范；这里面向 workflow 模板与 verify 门禁 | gofer 建事实与应用补丁，判断派 suga job；中。「工具重复次数」可单独作打转信号 |
| L6 | 准备阶段命令的停滞看门狗（无输出 N 秒即杀、错误留最后 20 行） | `internal/procwatch` | job 级看门狗已有；worktree 创建、fetch 等准备步骤是否覆盖未核实 | gofer；小 |
| L7 | 模板声明依赖并预检（`requires`：agent、runner 在线、secret） | `internal/domain/workflow.go` `WorkflowRequirement` | 无 | gofer workflow；小，优先级低 |

建议顺序：L1 → L3 → L2 + L4 → L5；L6、L7 作附带 issue。

## 不采用

- 模型参与平台流程（Main Bot 委派、需求澄清、AI 通知摘要）：gofer 只做确定性规则。
- 无人值守放宽权限（AI 定时任务固定完全权限、无超时）。
- 挂 Docker socket 的沙箱（文档自认可控制宿主）。
- SaaS 与产品面：多账号成员、可视化 SOP 设计器、模板市场、IM 按钮、浏览器工具、预览托管、计费报表。
- LoopForge Local：靠模型自觉读写状态文件；gofer 状态在服务端。

## 2026-11-26 复查（f8dcd1e..45ed0064，106 提交，v0.4.1 / v0.4.2；SOP 改名 LOOP）

| # | 机制 | LoopForge 出处 | gofer 落点 | 推荐 |
|---|---|---|---|---|
| L8 | **等人期间释放并发占位**：failed / waiting（含待审批）空闲 1 小时后归档目录、释放并发配额让排队 run 晋升；批准 / 重试时重新排队，目录按需还原；释放带 revision 乐观锁 | `internal/engine/workspace_cleanup.go`、`internal/repo/run_admission.go` | job 卡在人工门禁 / awaiting_input 时，执行槽与目录锁拆开处理 | 高 |
| L9 | **agent 提议写、人确认后才执行**：冻结草稿（随机 id、参数哈希、30 分钟有效），确认时重新校验权限与参数；加锁把 pending 改 executing，重复点击只回当前状态；执行中崩溃显示「中断待核对」，不自动重放 | `docs/im-channels.md` §8.3、`/api/bot-drafts/{id}/confirm` | 「等我」卡上的批准动作；Team 里 agent 发起的写操作 | 高 |
| L10 | **run 启动时钉住资产 hash + 资产版本只追加**（角色 prompt / 规则正文） | `internal/evolution/trial.go`、`/api/runs/{id}/asset-pins` | job 记录注入的 rules / skill 版本 hash，便于复盘「当时用的是哪版规范」 | 高 |
| L11 | 资产进化的确定性部分：Beta-Binomial 分诊（7 天半衰期、P≥0.9 才值得改）、holdout 样本不给改进器看、钉选回归样本、灰度分组 `fnv32(runID|proposalID)%100`、0/1/2 打分判退化 | `internal/evolution/triage.go`、`replay.go`、`trial.go` | 规范 / skill / 模板改动的效果评估；改进器本身是模型判断，不进平台 | 中 |
| L12 | 终态写入失败的兜底：节点失败终态写出错仍走 Reject，只有租约丢失才跳过；死锁重试 6 次；轮询只读进度不读产物正文 | `internal/engine/node_outcome.go` | 回归用例：终态写失败不得让 job 停在 running | 中 |
| L13 | 通知送达语义：发送前先落发送 key 与代数，崩溃时记 `send_unknown`「送达待核对」，人工确认或重发 | `internal/notification/service.go` | 出站通知 / 推送 | 中 |
| L14 | runner 自动分配：五层兼容过滤 → 按运行数、占用数排序 → 桶锁内原子占位 → 全满进弹性队列；绑定后子任务、重试冻结在同机；离线宽限起点取 `max(入队时间, 最近离线时间)` | `internal/engine/runner_auto_assign.go` | 多 worker 选机 | 中 |
| L15 | MCP 工具调用超过 1 秒切 SSE，每 15 秒发保活注释，防审批等待被空闲超时断开 | `internal/mcpserver/tool_response.go` | gofer MCP 走 stdio，不经 HTTP 代理；`gofer_ask_human` 默认阻塞 1800s，是否撞 agent CLI 的工具超时待核实 | 低 |
| L16 | 分支名校验 `ValidGitBranchName` 与默认命名 `feature/requirement-N-YYYYMMDD-HHMM` | `internal/domain/work_branch.go` | worktree 分支名校验 | 低 |

不学：改进器（模型写候选正文）、AI 分支命名、AI 辅助审批、IM 渠道连接层、privileged Docker 沙箱、SaaS runner 管理面。
