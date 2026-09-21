# Gofer 增强路线（Roadmap）

> 三轴：**① 更方便使用** · **② 更好地利用各种 agent 完成任务** · **③ 始终可观察、可审计 agent 的工作**。
> 本文**只列功能点、状态、一句话与去哪看细节**；构想、权衡、落地过程一律在链接的 design / plan / runbook 里，不写在这里。
> 2026-09-18 之前的详细版（含 v1.0–v1.30 修订记录与旧 `E` 编号映射）已归档到 [`roadmap-history.md`](roadmap-history.md)。
>
> 状态：✅ 已落地 · 🚧 部分落地 · 📝 设计中 · ⏳ 待做 · ❄ 暂缓（明确不做或等条件）。编号按主题前缀：`JOB / PLAN / WF / AGT / ACP / GATE / SESS / TUN / OBS / WEB / CFG / AUTO / MCP / AI`，新条目在主题内续号。
>
> 参考项目吸收记录（WebCodex、Multica）：[`refer/reference-projects.md`](refer/reference-projects.md)。

## 一、已落地能力（按主题，一行一项）

| 编号 | 能力 | 版本 | 细节 |
|---|---|---|---|
| JOB-01/03/04 | 产物回取 · 标签/搜索 · 结构化结果 | 0.2x | [outcomes design](design/2026-06-20-job-outcomes-audit-design.md) |
| JOB-07 | agent session 捕获与 `job resume`（exec 载体） | 0.3x | [session-capture](design/2026-06-26-session-capture-design.md) |
| JOB-08 | `job run --verify`：agent 结束后同机跑验证 argv，失败→failed / review 下 needs_review | 0.46 | [SUP-01 §二](design/2026-09-18-supervision-loop-and-agent-reliability-design.md) |
| JOB-02 | 任务书模板 `.gofer/templates` + 全局，`job run -t/--var`，`template ls|show` | 0.46 | [SUP-01 §六](design/2026-09-18-supervision-loop-and-agent-reliability-design.md) · 示例 `docs/examples/templates/` |
| WT-01 | `job run --worktree`（`tmp/gofer/wt/<job>`，分支 `gofer/<job>`）+ retention | 0.42 | [recovery+worktree](design/2026-09-16-job-recovery-and-worktree-design.md) · [runbook](runbook/parallel-jobs-with-worktree.md) |
| RECOV-01 | worker 断线 → `recovering` → 同实例重连续接；serve 重启认领 | 0.42 | 同上 |
| JOB-TO | job 超时上限可配（`server.max_job_timeout_sec` / 项目 `max_timeout_sec`） | 0.41 | design 内嵌于 AGT-02 |
| AUTO-02 | 内置 cron schedules（+run-now） | 0.3x | [cron runbook](runbook/2026-06-30-cron-schedule-runbook.md) |
| AUTO-03 🚧 | job 级自动重试（进程内最小版）；工作流 step 级 retry 完整 | 0.3x | [workflow v2](design/2026-06-22-workflow-v2-design.md) |
| AUTO-RES | 供应商类错误自动续投（`transient_error_patterns` / `server.auto_resume_max`） | 0.42 | [SUP-01 事实](design/2026-09-18-supervision-loop-and-agent-reliability-design.md) |
| AGT-02 | agent 双模式（batch `args` + `interactive_args`），项目 `allow_interactive` | 0.40 | [dual-mode](design/2026-09-15-agent-dual-mode-and-project-allow-interactive-design.md) |
| AGT-03 | agent 故障转移 `fallback_agents` / `--fallback`，健康度 degraded，`agent status|probe` | 0.46 | [SUP-01 §一](design/2026-09-18-supervision-loop-and-agent-reliability-design.md) |
| ACP-01 | `acp-agent` 类型（Agent Client Protocol）：claude/codex/gemini/omp 模板、acp.jsonl、resume 走 `session/load`、`--read-only` → `set_mode` | 0.44–0.45 | [ACP/GATE](design/2026-09-17-acp-agent-and-approval-gate-design.md) |
| GATE-01 | 审批门（项目 `approval: off|ask|strict`，permission 交互卡）+ `needs_review` 人工验收（accept/reject，agent 不能 accept） | 0.44–0.45 | 同上 |
| REV-01 | 验收台：web `/review` 队列 + 详情五页签（汇报/提交/Diff/验证/用量）+ `gofer job review` | 0.47 | [review workbench](design/2026-09-18-review-workbench-and-relay-wrapup-design.md) |
| PLAN-01 | `gofer plan` + todo 看板；`job run --todo` 自动 doing/done + 提交列表；`plan set-todo --append-note` | 0.3x / 0.46 | [plan-orchestration](design/2026-07-09-plan-orchestration-design.md) · SUP-01 §三 |
| WF-01–04 | 多步工作流 v2：fan-out/join、子工作流、跨项目、导入导出 | 0.3x | [workflow v2](design/2026-06-22-workflow-v2-design.md) |
| SESS-01 | 终端会话 ↔ web 消息中继：Stop hook 阻塞、三态 `relay_mode`、监督期间不布防 | 0.41–0.46 | [session relay](design/2026-09-06-agent-session-relay-design.md) · [runbook](runbook/session-relay.md) |
| SESS-02 | 中继阶段 2：tmux 送话（A）、`--resume` pty 接管（B）、接管自动释放 | 0.45–0.47 | 同上 §9.1 |
| MCP-01–04 | 监督 agent 自动应答 · `gofer mcp` client 模式 · roles · presence/inbox | 0.3x | [multi-agent](design/2026-06-28-multi-agent-collab-design.md) · [supervisor routing](design/2026-06-29-supervisor-routing-design.md) |
| TUN-01/02 | TCP/UDP 隧道经 worker 转发，文件日志与延迟测量 | 0.38–0.40 | [tcp tunnel](design/2026-09-11-tcp-tunnel-design.md) · [logging](design/2026-09-15-tunnel-file-logging-and-udp-latency-design.md) |
| OBS-01/02/04/05/06/08 | diff 快照 · 事件时间线 · 渲染命令 · `/metrics` · per-caller 配额 · 来源追踪 | 0.2x | [observability](design/2026-06-21-observability-governance-design.md) |
| OBS-03 🚧 / OBS-07 🚧 | 事件外发 webhook；钉钉/飞书出站通知（`kind` 适配、`session.waiting` 等） | 0.39 | [im-notification](runbook/im-notification.md) |
| OBS-09 | 用量/成本：omp/claude/codex/acp 采集入 `usage_json`，`stats.usage` 24h/7d，Home 卡 | 0.46 | [SUP-01 §五](design/2026-09-18-supervision-loop-and-agent-reliability-design.md) |
| OBS-10 | NDJSON 采集投影：stdout=assistant 文本、stderr=裁剪事件，`ndjson_*` 开关 | 0.43–0.46 | README「Agent 输出」 |
| OBS-11 | 结构化文件日志（serve/worker/tunnel/daemon sidecar，lumberjack） | 0.39–0.40 | [logging design](design/2026-09-15-tunnel-file-logging-and-udp-latency-design.md) |
| WEB-01–09 | 控制台 v2/v3：产物预览、git/子仓、pty attach、拓扑、Dashboard、交互应答、导航（Plans 前置、Drivers 并入 Agents）、Server DB/Sessions 卡 | 0.2x–0.44 | [web v3](design/2026-07-02-web-console-v3-design.md) |
| WEB-04 🚧 | 配置管理：拓扑/节点只读 + 项目 CRUD 写层 V1（server/callers/agents 编辑待） | 0.3x | [config write](design/2026-07-03-web-config-write-design.md) |
| CFG-01/02/04/06 | CLI 补全 · 引导/校验 · 全局单 server + 项目瘦配置 · 节点易用性 | 0.2x | [config simplification](design/2026-06-22-config-simplification-design.md) |
| CFG-07 | `GOFER_RUN_MODE=client`（只需 .env），`gofer init client` | 0.41 | skill `references/client-config.md` |
| CFG-08 | `start.ps1 -Action upgrade`（Windows 服务原地升级） | 0.40 | [selfupdate runbook](runbook/2026-07-11-windows-server-selfupdate-runbook.md) |
| G032 | 兼容策略：DEPRECATED 标记 + 到期删除（清单见 SUP-01「横切」） | 0.46 | `AGENTS.md` G032 |

## 二、待做 / 候选（下一批从这里选）

| 编号 | 功能 | 价值 | 大小 | 状态 | 来源 / 细节 |
|---|---|---|---|---|---|
| **JOB-11** | **同 cwd 串行锁**：目标同一项目目录（非 worktree）的 job 排队（新状态 `waiting_dir`），避免两个 agent 踩同一 checkout；顺带 `agents.<k>.max_concurrent` | 高 | 小 | 📝 设计中 | [design](design/2026-09-20-file-transfer-and-plan-dispatch-design.md) · Multica `waiting_local_directory`，[refer](refer/reference-projects.md) |
| **JOB-09** | **job wakeups**：agent（或人）在 job 上登记 事件订阅 / 定时器，job 结束后输入到达时自动 `job resume` 续投（等 verify、等人回复、每小时巡检），不占会话/hook | 高 | 中 | 📝 设计中 | [design](design/2026-09-20-file-transfer-and-plan-dispatch-design.md) · Multica wakeups；建立在 resume + schedules + 事件流上 |
| **PLAN-02** | **todo 指派即派发**：todo 增 `assignee`(agent)/`template`/`verify`/`review`，状态到 `todo` 自动 `job run -t … --todo`；同一 todo 可多次 run / 换 agent；plan 级用量汇总 | 高 | 中 | 📝 设计中 | [design](design/2026-09-20-file-transfer-and-plan-dispatch-design.md) · Multica "assign an issue"；依赖 JOB-02/PLAN-01 |
| **XFER-01** | **文件传输**：客户端 ↔ server ↔ worker 双向传文件（scp 式 `gofer tool cp ./x.bin w-hw-windows11:<project>/tmp/x.bin` / 反向拉回），worker 经既有 WS 收指令、经 HTTP + worker token 上传/下载；限项目根内（POLICY roots 映射）、大小上限、sha256、审计事件；`job run --upload local:dest` / `--collect <path>` 让 job 前置输入与产物随 job 传；`--runner local` 时直接落 server 主机 | 高 | 中 | 📝 设计中 | [design](design/2026-09-20-file-transfer-and-plan-dispatch-design.md)；用户 2026-09-20：现在靠 base64 塞进 job 日志传文件 |
| **AUTO-05** | **输出停滞检测**：运行中 N 分钟无 stdout/stderr 增长 → 判 hung，kill 后按 transient 走续投/转移 | 中 | 小 | 📝 设计中 | [design](design/2026-09-20-file-transfer-and-plan-dispatch-design.md) · Multica "codex stalled output" |
| JOB-10 | skills 绑定：项目/agent 级 skill 目录，派发时挂载（`.claude/skills` / AGENTS.md 引用）或注入，`gofer skill import <dir|zip|url>` | 中 | 中 | ⏳ | Multica skills；接 roles/模板 |
| AUTO-02b | schedule 增 webhook 触发（`POST /v1/schedules/{id}/trigger` + 签名） | 中 | 小 | ⏳ | Multica autopilots |
| MCP-05 | leader 路由：plan/job 评论 `@agent` 触发 job；leader 回合决定下一步/升级/转验收 | 中 | 中-大 | ⏳ | Multica squads；依赖 PLAN-02 + 评论触发 |
| AUTO-03 | job 级重试可靠版：持久化退避、退出码白名单、opt-in 幂等 | 中 | 中 | 🚧 | [roadmap-history](roadmap-history.md) AUTO-03 |
| CFG-05 | worker 配置向导 `gofer worker init`（拉 server projects → roots 映射） | 中 | 中 | ⏳ | 容器 worker 上线前做 |
| CFG-09 | 容器内 worker + `GOFER_HOOK_RUNNER`：让容器会话可被 web 送话、verify/tmux 真机 e2e | 中 | 中 | ❄ 用户定时机 | [session relay v0.5](design/2026-09-06-agent-session-relay-design.md) |
| WEB-04③ | 配置写层 V1.1：server/callers/agents 在 web 编辑（热重载盲区 + secret 策略） | 中 | 中 | 🚧 | [config write](design/2026-07-03-web-config-write-design.md) |
| ACP-02 | 真 claude-acp / codex-acp 端到端验收（鉴权、供应商稳定后） | 中 | — | ❄ 等条件 | [ACP S0 实测](design/2026-09-17-acp-agent-and-approval-gate-design.md) |
| JOB-06 | 上下文/secret/规则注入（per-job env 已有；规则文件挂载待） | 中 | 中 | 🚧 | roadmap-history JOB-06 |
| JOB-05 | mcp-agent 类型（job 调用"本身是 MCP server"的能力） | 低 | 中 | ⏳ | roadmap-history JOB-05 |
| AUTO-04 | 事件 hook 插件（只读旁路先行） | 低 | 大 | ⏳ | roadmap-history AUTO-04 |
| OBS-07(b)(c) | IM 入站提交 / 交互应答 | 中 | 大 | ❄ 用户暂不做 | [im-notification](runbook/im-notification.md) |
| CFG-03 | 主机侧动作（编辑器打开等） | 低 | 低 | ⏳ | roadmap-history CFG-03 |
| AI-01/02 | 内置 AI 助手 / usage skill 完善 | 低 | 大/低 | ⏳ | roadmap-history AI-01 |
| G032-v0.48 | 到期删除 6 处 DEPRECATED(v0.45)（relay 镜像列、HTTP relay bool、旧配置键别名、interactive_allowed_agents 一次性读取） | — | 小 | ✅ v0.48 | SUP-01「横切」清单；删除记录见设计「v0.48 到期删除记录」 |

## 三、建议下一批

**「文件传输 + 计划即派发」批（XFER-01 + JOB-11 + AUTO-05 + PLAN-02 + JOB-09）**：XFER-01 是当前最直接的痛点（worker 节点机上传/拿回文件现在靠 base64 走日志），先做；再把并发安全（同 cwd 串行、停滞检测）补上，再让 plan 的 todo 直接指派给 agent 自动派发、job 能登记 wakeup 等条件到达后续投——这四项合起来就是 Multica 的核心工作模型，且全部建立在已有的 plan/todo、模板、verify、resume、schedules 之上。JOB-10 skills 与 AUTO-02b 视余量并入。

## 四、维护约定

- 新条目：在「待做」加一行（编号、一句话、价值/大小、来源）；进入设计后把链接换成 design 文档；落地后移到「已落地」并只留一行。
- 细节不进本文：构想与权衡写 design，实施过程写 plan / 实测记录，操作步骤写 runbook。
- 参考项目对比写 [`refer/reference-projects.md`](refer/reference-projects.md)，本文只留条目编号。
