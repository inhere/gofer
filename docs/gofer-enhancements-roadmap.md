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
| AUTO-03 | job 级自动重试可靠版：落库 + 租约 sweeper（重启不丢）、server/agent/project/请求四级策略（默认关闭）、`job run --retry` / `job retry ls|cancel`、`job.retry_exhausted` 默认通知；工作流 step 级 retry 完整 | 0.52 | [design §二](design/2026-09-22-config-write-v11-reliable-retry-and-session-fallback-design.md) · [runbook](runbook/2026-09-22-job-retry-runbook.md) |
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
| WEB-04 ✅ | 配置管理：拓扑/节点只读 + 项目 CRUD 写层 V1 + **agents/server 写层 V1.1**（字段策略表、secret 只引用、干跑校验、显式 reload） | 0.3x–0.5x | [config write](design/2026-07-03-web-config-write-design.md) / [V1.1](design/2026-09-22-config-write-v11-reliable-retry-and-session-fallback-design.md) §一（R3 已落地；callers/roles/runners/notification 写层留 V1.2） |
| CFG-01/02/04/06 | CLI 补全 · 引导/校验 · 全局单 server + 项目瘦配置 · 节点易用性 | 0.2x | [config simplification](design/2026-06-22-config-simplification-design.md) |
| CFG-07 | `GOFER_RUN_MODE=client`（只需 .env），`gofer init client` | 0.41 | skill `references/client-config.md` |
| CFG-08 | `start.ps1 -Action upgrade`（Windows 常驻实例原地升级） | 0.40 | [selfupdate runbook](runbook/2026-07-11-windows-server-selfupdate-runbook.md) |
| XFER-01 | 文件传输：`gofer tool cp <本地> <runner>:<project>/<path>`（双向，HTTP 传本体、WS 传指令，协议 v9）、`tool xfer ls|show|rm`；`job run --upload/--collect`（起跑前放文件、结束后收回并入 artifacts）；xfer 事件进通知 | 0.47.2 | [design](design/2026-09-20-file-transfer-and-plan-dispatch-design.md) |
| JOB-11 | 同目录串行：可写 agent job 独占 cwd（`waiting_dir`，祖先/后代互斥），exec/read-only 共享，`--exclusive-dir/--shared-dir`；`agents.<k>.max_concurrent` | 0.48 | 同上 §二 |
| AUTO-05 | 输出停滞检测：`stall_timeout`（server/agent/job 三级，默认 900s，exec 关）→ 按 transient 续投/转移 | 0.48 | 同上 §三 |
| PLAN-02 | todo 指派即派发：plan `project_key`，todo `assignee/template/vars/verify/review/runner/cwd`，状态 `ready` 自动 `job run`；`plan dispatch`；plan 级用量 | 0.48 | 同上 §四 |
| JOB-09 | job wakeups：`job wakeup create <job> --kind at|every|cron|event …` → 到点/事件命中时续投（无 session 退化重跑+指令），coalesce、TTL；HTTP/MCP/web | 0.48 | 同上 §五 |
| PTY-01 | 交互 pty job：去 ANSI 转录 `pty.txt`、会话 id 头/尾/关闭时捕获、终态扫描；`job resume` 可续接 | 0.49 | design §四 |
| PLAN-03 | todo 依赖 `--after prev`、自动推进、`plan run|pause|resume`、`blocked`（进通知默认集）、exec todo `--cmd`、`plan.completed` | 0.49 | [design](design/2026-09-22-plan-autopilot-board-and-container-worker-design.md) |
| WEB-10 | 计划看板：五列拖拽，拖到 ready 即派发，blocked 横幅重派/跳过，run/pause | 0.49 | 同上 §二 |
| XFER-02 | 传输 id `xf-<8hex>`、上传前预检、时间按服务端时区渲染 | 0.49 | design §五/六 |
| CFG-09 | 容器内 worker（`w-docker-claude`）+ `GOFER_HOOK_RUNNER`、`gofer worker doctor`、容器 worker runbook | 0.49 | [design §三](design/2026-09-22-plan-autopilot-board-and-container-worker-design.md) · [runbook](runbook/container-worker.md) |
| AUTO-02b | schedule webhook 触发（`trigger_token`）；`agent.degraded/recovered` 事件 | 0.49 | 同上 §六 |
| SVC-01 | Windows 桌面会话常驻：`serve -d`/`worker -d`/`stop` 的 Windows 实现（分离进程 + 命名事件优雅停 + 前台也记 pidfile）+ `start.ps1` 登录计划任务跑在交互会话（`--runner local` 可操作 GUI），nssm 废弃 | 0.50 | [design](design/2026-09-22-windows-desktop-session-service-design.md) · [runbook §7](runbook/2026-07-11-windows-server-selfupdate-runbook.md) |
| G032 | 兼容策略：DEPRECATED 标记 + 到期删除；v0.48 已删 6 处 v0.45 标记 | 0.46–0.48 | `AGENTS.md` G032 · SUP-01「横切」 |
| F8 | 升级后前端自愈与轮询收敛：缺失 asset 404（不再回落 shell）、shell `no-cache`/asset `immutable`、旧 chunk 自动重载一次（60s 冷却）+ 顶栏「有新版本，点击刷新」、顶栏铃铛 15s/失焦暂停（`utils/poller.ts`） | 0.53.1 | [runbook §7.5](runbook/2026-07-11-windows-server-selfupdate-runbook.md) · 本文「已落地」 |
| JOB-10 | skills 绑定：`<config-dir>/skills/` 库 + `agent skill import/ls/show/rm/update/export` + server/agent/project/job 四级**并集**绑定 + `--skill/--no-skills`；派发时物化到 job 私有 `result_dir/skills/`（本机直写 / worker 走 uploads `Base=result_dir`）并在 prompt 头部列清单，**不写项目工作树**；`job.skills_mounted/skills_skipped` | 0.56 | [design](design/2026-09-23-skills-binding-and-comment-routing-design.md) §一 · [runbook](runbook/2026-09-23-skills-runbook.md) |
| MCP-05 | 评论层 + leader 路由：job/plan/todo 评论与 `@agent` 提及即派活（阶段 A，只认 user caller + 限流 + allowlist）；leader 回合（阶段 B，opt-in：成员终态唤醒、轮次封顶、人插话接管、不能 accept/reject） | 0.56 | 同上 §二 · [评论 runbook](runbook/2026-09-23-comments-runbook.md) · [leader runbook](runbook/2026-09-23-leader-round-runbook.md) |
| S1–S4 | 小项：SPA 回落支持 HEAD；`server.dir_lock`/`server.agent_health` 放行热改（`xfer` 仍需重启）；`job.session_captured` 不进事件镜像（只补注释/文档）；web 配置页补丁式编辑 `notification`（`secret_env` 只给名字、可暂停单个目标） | 0.56 | 同上 §三 + §S4 实测记录 |

## 二、待做 / 候选（下一批从这里选）

| 编号 | 功能 | 价值 | 大小 | 状态 | 来源 / 细节 |
|---|---|---|---|---|---|
| WEB-04③ | 配置写层 V1.1：agents/server 在 web 编辑（字段可编辑/需重启标记、secret 只引用、干跑校验、显式 reload） | 高 | 中 | ✅ 0.5x（R3 落地） | [design](design/2026-09-22-config-write-v11-reliable-retry-and-session-fallback-design.md) §一 + R3 实测记录 |
| AUTO-03 | job 重试可靠版：`job_retries` 落库 + 租约 sweeper（重启不丢）、server/agent/project/job 四级策略、`--retry`、`retry_exhausted` 进默认通知 | 高 | 中 | ✅ 0.5x（R2 落地） | 同上 §二 |
| AGT-04 | 会话捕获兜底：未配 `session_capture` 的 cli-agent 用通用正则（非 uuid id 也认）+ resume 模板兜底，新增 agent 免配即可续接 | 中 | 小 | ✅ 0.5x（R1 落地） | 同上 §三（jcode 实测） |
| SEC-01 | job 作用域凭证：给 leader（及将来其他受控 job）发**只能做允许动作**的 token，并把 server bearer token 从 job 环境里去掉 —— 否则"leader 不能 accept/reject"只是调用方自报身份（`as_job`），带 server token 的 agent 直接 curl 仍能 accept（见 [leader runbook](runbook/2026-09-23-leader-round-runbook.md)「局限」） | 高 | 中 | ⏳ | MCP-05 阶段 B 遗留（S4 记录） |
| CFG-05 | worker 配置向导 `gofer worker init`（拉 server projects → roots 映射） | 中 | 中 | ⏳ | 容器 worker 已手工上线（CFG-09），向导仍缺 |
| ACP-02 | 真 claude-acp / codex-acp 端到端验收（鉴权、供应商稳定后） | 中 | — | ❄ 等条件 | [ACP S0 实测](design/2026-09-17-acp-agent-and-approval-gate-design.md) |
| JOB-06 | 上下文/secret/规则注入（per-job env 已有；规则文件挂载待） | 中 | 中 | 🚧 | roadmap-history JOB-06 |
| JOB-05 | mcp-agent 类型（job 调用"本身是 MCP server"的能力） | 低 | 中 | ⏳ | roadmap-history JOB-05 |
| AUTO-04 | 事件 hook 插件（只读旁路先行） | 低 | 大 | ⏳ | roadmap-history AUTO-04 |
| OBS-07(b)(c) | IM 入站提交 / 交互应答 | 中 | 大 | ❄ 用户暂不做 | [im-notification](runbook/im-notification.md) |
| CFG-03 | 主机侧动作（编辑器打开等） | 低 | 低 | ⏳ | roadmap-history CFG-03 |
| AI-01/02 | 内置 AI 助手 / usage skill 完善 | 低 | 大/低 | ⏳ | roadmap-history AI-01 |
| G032-v0.48 | 到期删除 6 处 DEPRECATED(v0.45)（relay 镜像列、HTTP relay bool、旧配置键别名、interactive_allowed_agents 一次性读取） | — | 小 | ✅ v0.48 | SUP-01「横切」清单；删除记录见设计「v0.48 到期删除记录」 |

## 三、建议下一批

v0.49.0 已落地 PLAN-03 / WEB-10 / CFG-09 / PTY-01 / XFER-02 / AUTO-02b；SVC-01（Windows 桌面会话常驻，含 `start.ps1` 登录计划任务）已在 v0.50 落地（正式切换 = 桌面管理员窗口卸旧 nssm 服务）。下一批已出设计（待批准）：WEB-04③ 配置写层 V1.1 + AUTO-03 可靠重试 + AGT-04 会话捕获兜底。WEB-04③/AUTO-03/AGT-04 已于 v0.51–v0.53 落地（另见 v0.53.1 的 web 修复）。JOB-10 skills 绑定 + MCP-05 评论/leader 路由 + 小项 S1–S4 已于 v0.56 落地（含 S4 收尾：SPA HEAD、`dir_lock`/`agent_health` 热改、notification 补丁式编辑、leader 不唤醒已完成的 plan、worker job 的 `job.skills_mounted` 回执）。下一批候选：SEC-01 job 作用域凭证、ACP-02 真机验收、CFG-05 worker init 向导、JOB-06 规则文件挂载。

## 四、维护约定

- 新条目：在「待做」加一行（编号、一句话、价值/大小、来源）；进入设计后把链接换成 design 文档；落地后移到「已落地」并只留一行。
- 细节不进本文：构想与权衡写 design，实施过程写 plan / 实测记录，操作步骤写 runbook。
- 参考项目对比写 [`refer/reference-projects.md`](refer/reference-projects.md)，本文只留条目编号。
