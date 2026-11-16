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
