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
| JOB-02 | 任务书模板 `.gofer/templates` + 全局，`job run -t/--var`，`template ls\|show` | 0.46 | [SUP-01 §六](design/2026-09-18-supervision-loop-and-agent-reliability-design.md) · 示例 `docs/examples/templates/` |
| WT-01 | `job run --worktree`（`tmp/gofer/wt/<job>`，分支 `gofer/<job>`）+ retention | 0.42 | [recovery+worktree](design/2026-09-16-job-recovery-and-worktree-design.md) · [runbook](runbook/parallel-jobs-with-worktree.md) |
| RECOV-01 | worker 断线 → `recovering` → 同实例重连续接；serve 重启认领 | 0.42 | 同上 |
| JOB-TO | job 超时上限可配（`server.max_job_timeout_sec` / 项目 `max_timeout_sec`） | 0.41 | design 内嵌于 AGT-02 |
| AUTO-02 | 内置 cron schedules（+run-now） | 0.3x | [cron runbook](runbook/2026-06-30-cron-schedule-runbook.md) |
| AUTO-03 | job 级自动重试可靠版：落库 + 租约 sweeper（重启不丢）、server/agent/project/请求四级策略（默认关闭）、`job run --retry` / `job retry ls\|cancel`、`job.retry_exhausted` 默认通知；工作流 step 级 retry 完整 | 0.52 | [design §二](design/2026-09-22-config-write-v11-reliable-retry-and-session-fallback-design.md) · [runbook](runbook/2026-09-22-job-retry-runbook.md) |
| AUTO-RES | 供应商类错误自动续投（`transient_error_patterns` / `server.auto_resume_max`） | 0.42 | [SUP-01 事实](design/2026-09-18-supervision-loop-and-agent-reliability-design.md) |
| AGT-02 | agent 双模式（batch `args` + `interactive_args`），项目 `allow_interactive` | 0.40 | [dual-mode](design/2026-09-15-agent-dual-mode-and-project-allow-interactive-design.md) |
| AGT-03 | agent 故障转移 `fallback_agents` / `--fallback`，健康度 degraded，`agent status\|probe` | 0.46 | [SUP-01 §一](design/2026-09-18-supervision-loop-and-agent-reliability-design.md) |
| ACP-01 | `acp-agent` 类型（Agent Client Protocol）：claude/codex/gemini/omp 模板、acp.jsonl、resume 走 `session/load`、`--read-only` → `set_mode` | 0.44–0.45 | [ACP/GATE](design/2026-09-17-acp-agent-and-approval-gate-design.md) |
| GATE-01 | 审批门（项目 `approval: off\|ask\|strict`，permission 交互卡）+ `needs_review` 人工验收（accept/reject，agent 不能 accept） | 0.44–0.45 | 同上 |
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
| CFG-08 | `start.ps1 -Action upgrade`（Windows 常驻实例原地升级） | 0.40 | [selfupdate runbook](runbook/2026-07-11-windows-server-selfupdate-runbook.md)；被 SVC-05 取代 |
| XFER-01 | 文件传输：`gofer tool cp <本地> <runner>:<project>/<path>`（双向，HTTP 传本体、WS 传指令，协议 v9）、`tool xfer ls\|show\|rm`；`job run --upload/--collect`（起跑前放文件、结束后收回并入 artifacts）；xfer 事件进通知 | 0.47.2 | [design](design/2026-09-20-file-transfer-and-plan-dispatch-design.md) |
| JOB-11 | 同目录串行：可写 agent job 独占 cwd（`waiting_dir`，祖先/后代互斥），exec/read-only 共享，`--exclusive-dir/--shared-dir`；`agents.<k>.max_concurrent` | 0.48 | 同上 §二 |
| AUTO-05 | 输出停滞检测：`stall_timeout`（server/agent/job 三级，默认 900s，exec 关）→ 按 transient 续投/转移 | 0.48 | 同上 §三 |
| PLAN-02 | todo 指派即派发：plan `project_key`，todo `assignee/template/vars/verify/review/runner/cwd`，状态 `ready` 自动 `job run`；`plan dispatch`；plan 级用量 | 0.48 | 同上 §四 |
| JOB-09 | job wakeups：`job wakeup create <job> --kind at\|every\|cron\|event …` → 到点/事件命中时续投（无 session 退化重跑+指令），coalesce、TTL；HTTP/MCP/web | 0.48 | 同上 §五 |
| PTY-01 | 交互 pty job：去 ANSI 转录 `pty.txt`、会话 id 头/尾/关闭时捕获、终态扫描；`job resume` 可续接 | 0.49 | design §四 |
| PLAN-03 | todo 依赖 `--after prev`、自动推进、`plan run\|pause\|resume`、`blocked`（进通知默认集）、exec todo `--cmd`、`plan.completed` | 0.49 | [design](design/2026-09-22-plan-autopilot-board-and-container-worker-design.md) |
| WEB-10 | 计划看板：五列拖拽，拖到 ready 即派发，blocked 横幅重派/跳过，run/pause | 0.49 | 同上 §二 |
| XFER-02 | 传输 id `xf-<8hex>`、上传前预检、时间按服务端时区渲染 | 0.49 | design §五/六 |
| CFG-09 | 容器内 worker（`w-docker-claude`）+ `GOFER_HOOK_RUNNER`、`gofer worker doctor`、容器 worker runbook | 0.49 | [design §三](design/2026-09-22-plan-autopilot-board-and-container-worker-design.md) · [runbook](runbook/container-worker.md) |
| AUTO-02b | schedule webhook 触发（`trigger_token`）；`agent.degraded/recovered` 事件 | 0.49 | 同上 §六 |
| SVC-01 | Windows 桌面会话常驻：`serve -d`/`worker -d`/`stop` 的 Windows 实现（分离进程 + 命名事件优雅停 + 前台也记 pidfile）+ `start.ps1` 登录计划任务跑在交互会话（`--runner local` 可操作 GUI），nssm 废弃 | 0.50 | [design](design/2026-09-22-windows-desktop-session-service-design.md) · [runbook §7](runbook/2026-07-11-windows-server-selfupdate-runbook.md)；被 SVC-05 取代 |
| G032 | 兼容策略：DEPRECATED 标记 + 到期删除；v0.48 已删 6 处 v0.45 标记 | 0.46–0.48 | `AGENTS.md` G032 · SUP-01「横切」 |
| F8 | 升级后前端自愈与轮询收敛：缺失 asset 404（不再回落 shell）、shell `no-cache`/asset `immutable`、旧 chunk 自动重载一次（60s 冷却）+ 顶栏「有新版本，点击刷新」、顶栏铃铛 15s/失焦暂停（`utils/poller.ts`） | 0.53.1 | [runbook §7.5](runbook/2026-07-11-windows-server-selfupdate-runbook.md) · 本文「已落地」 |
| F15 | plan 状态去重：删掉与 `open` 重复、无人设置的 `active`（老库打开时迁移为 `open`；`PATCH`/`plan set-status` 传 `active` → 400 提示用 `open`；web 筛选/徽标/动作同步收窄） | 0.60.5 | `AGENTS.md` G032 · 无独立设计 |
| WEB-12 | 设置页左侧二级菜单（配置管理 / Tunnels / 关于），`/config` 重定向 | 0.60.2 | [design](design/2026-09-24-settings-hub-and-tunnel-visibility-design.md) §一 |
| TUN-03 | 隧道在 web 可见：`tun forward` 向 hub 登记与心跳、在线转发列表（归并活跃连接）；预设存 server（CLI 优先、`tun presets push`）、web 编辑 | 0.60.2 | [design](design/2026-09-24-settings-hub-and-tunnel-visibility-design.md) §二 |
| TUN-04 | `tun forward/save` 的 spec 接受逗号分隔（与空格混用） | 0.60.2 | [design](design/2026-09-24-settings-hub-and-tunnel-visibility-design.md) §三 |
| WEB-11 | **web 工作台**（W1–W3）：会话为一等公民（同 session 的 job 链 / 中继会话）、按项目分组与状态上卷、「等你」队列、composer 发起与续接、命令面板；分屏/标签页布局存 server、`ctrl+b` 前缀、手机布局；PWA + Web Push（通知内审批，需 HTTPS）；ACP 结构化对话视图；会话改动视图 + 行内评审回灌同一会话 | 0.62–0.66 | [design](design/2026-09-26-web-workbench-design.md) §W1–W3 实测记录 |
| JOB-10 | skills 绑定：`<config-dir>/skills/` 库 + `agent skill import/ls/show/rm/update/export` + server/agent/project/job 四级**并集**绑定 + `--skill/--no-skills`；派发时物化到 job 私有 `result_dir/skills/`（本机直写 / worker 走 uploads `Base=result_dir`）并在 prompt 头部列清单，**不写项目工作树**；`job.skills_mounted/skills_skipped` | 0.54 | [design](design/2026-09-23-skills-binding-and-comment-routing-design.md) §一 · [runbook](runbook/2026-09-23-skills-runbook.md) |
| MCP-05 | 评论层 + leader 路由：job/plan/todo 评论与 `@agent` 提及即派活（阶段 A，只认 user caller + 限流 + allowlist）；leader 回合（阶段 B，opt-in：成员终态唤醒、轮次封顶、人插话接管、不能 accept/reject） | 0.55–0.56 | 同上 §二 · [评论 runbook](runbook/2026-09-23-comments-runbook.md) · [leader runbook](runbook/2026-09-23-leader-round-runbook.md) |
| S1–S4 | 小项：SPA 回落支持 HEAD；`server.dir_lock`/`server.agent_health` 放行热改（`xfer` 仍需重启）；`job.session_captured` 不进事件镜像（只补注释/文档）；web 配置页补丁式编辑 `notification`（`secret_env` 只给名字、可暂停单个目标） | 0.57 | 同上 §三 + §S4 实测记录 |
| WEB-04③ | 配置写层 V1.1：agents/server 在 web 编辑（字段可编辑/需重启标记、secret 只引用、干跑校验、显式 reload） | 0.53 | [design](design/2026-09-22-config-write-v11-reliable-retry-and-session-fallback-design.md) §一 + R3 实测记录 |
| AGT-04 | 会话捕获兜底：未配 `session_capture` 的 cli-agent 用通用正则（非 uuid id 也认）+ resume 模板兜底，新增 agent 免配即可续接 | 0.51 | [design](design/2026-09-22-config-write-v11-reliable-retry-and-session-fallback-design.md) §三（jcode 实测） |
| SEC-01 | job 作用域凭证：执行时签发 job token（终态吊销），job/verify 环境去掉 server/worker token，server 按凭证判定身份与权限（member/leader 两档），`as_job` 废弃，协议 v11；F10 补漏（job 环境去 `GOFER_CONFIG_DIR`、job 内 CLI 不读 .env token、PATH 前置运行它的 gofer） | 0.58 / 0.60.1 | [design](design/2026-09-23-job-credentials-and-leader-opt-in-design.md) §一 |
| LEAD-02 | leader 逐 plan 开启（默认关，全局 enabled 仅总闸）、leader 动作改 CLI（凭证强制权限）、config 视图补 leader、plan 页事件区 | 0.59 | [design](design/2026-09-23-job-credentials-and-leader-opt-in-design.md) §二 |
| F-a/b/c | worker 模式 skill CLI 回落 HTTP（bd h-aii-uzvc）、Board plan 过滤改输入框、导航「技能」→「Skills」；Plans 分页与状态/项目/关键字过滤 | 0.60.0 | [design](design/2026-09-23-job-credentials-and-leader-opt-in-design.md) §三 |
| ACP-02 | 真机端到端：omp-acp / jcode-acp 首轮+续接；ACP 取消/超时杀整棵进程树（F12）、session/load 无 sessionId 兜底（F13）、ACP agent 可继承 `~/.claude/settings.json` env（F14） | 0.60.3–0.60.4 | [ACP 设计](design/2026-09-17-acp-agent-and-approval-gate-design.md) · [v0.60.2 设计 §四](design/2026-09-24-settings-hub-and-tunnel-visibility-design.md) |
| JOB-06① | 强制规则：server 规则库（`agent rule`、web 设置页 Rules）、四级并集 + 仓库 `.gofer/RULES.md`，派发时注入 prompt 顶部、job 记名称与 sha | 0.61 | [design](design/2026-09-25-rules-injection-and-worker-init-design.md) §一 |
| CFG-05 | `gofer worker init` 向导：拉 server 可派项目、推断并校验 roots、探测 agent、写 worker.yaml/.env、跑 doctor；新增 `GET /v1/workers/{id}/assignable` | 0.61 | [design](design/2026-09-25-rules-injection-and-worker-init-design.md) §二 |
| F-e/F-f/F-g | 文本类 cli-agent 会话 id 实时落库（F11 遗留）；`job run --env`；init 默认工作空间 `~/.gofer/workspace` + `default` 项目 | 0.60.2 / 0.61 | [design](design/2026-09-25-rules-injection-and-worker-init-design.md) §三 |
| F16 | 工作台 review 口径收窄（仅 needs_review / 未看的失败 / 有改动未看）、首访已看基线、全部标记已看 | 0.62.1 | [design](design/2026-09-26-web-workbench-design.md) §F16 |
| B01 | 续接"续接出来的作业"：沿 `ResumedFrom` 链回溯原始 agent（不再依赖 `OriginAgent`） | 0.62 / 0.65 | [design](design/2026-09-26-web-workbench-design.md) §W3b 实测记录 |
| WEB-13 | 手机端列表紧凑化：plan 详情 jobs、Board、Plans 行压成 2–3 行；Board 筛选与新建计划表单窄屏默认收起 | 0.66.2 | 无独立设计 |
| GIT-01 | job 未提交改动守卫：开跑/结束比对，标出本轮新增未提交文件；`on_uncommitted: off\|warn\|review\|resume`；web「未提交 N」徽标 | 0.67 | [design](design/2026-09-27-local-first-tracker-and-uncommitted-guard-design.md) §GIT-01 |
| TRK-01 | 仓库本地优先 issue/memory（真源 `.gofer/tracker/*.jsonl`）：`repo init\|prime\|sync\|migrate\|status` + `issue`/`memory`；bd 迁移；SessionStart 注入 prime | 0.68–0.69 | 同上 §TRK-01 |
| TRK-01 P4 | tracker server 镜像：三方合并同步、`job run --issue`、web Issues 页 | 0.74 | 同上 · [plan](plans/2026-09-29-trk-01-sync-link-web-p4-plan.md) |
| TRK-01 X1 | issue/memory 对齐 bd 日常用法；`repo migrate --from-bd` 加固；prime 预算 issue 优先 | 0.115 | 同上「X1 实测记录」 |
| TRK-02 | 全局 / 项目作用域记忆（存 server，`memory --global/--project`，`agent:<名>` 标签注入） | 0.81 | [design](design/2026-09-29-memory-scope-plan-tags-log-perf-design.md) §M1 |
| TRK-03 | prime 精简：issue 条数上限、memory 仅 `prime` 标签全文、`prime:` 配置 | 0.83 | [design](design/2026-09-30-interactive-acp-job-and-backlog-design.md) §B3 |
| TRK-04 | Issues 页父子树 / 依赖、分页多选批量操作；`issue close/update` 多 id | 0.116–0.117 | [runbook](runbook/session-relay.md) §3.0 |
| JOB-12 | 续接载体 job 记录 / 显示原 agent（`resume_agent`） | 0.70 | [design](design/2026-09-27-resume-display-plan-handoff-tun-web-hook-watch-design.md) §JOB-12 |
| JOB-11b | 目录锁细化：`--lock <path>`、`dir_lock_mode: repo`、waiting_dir 提示 | 0.75–0.76 | [design](design/2026-09-29-web-ui-polish-and-backlog-design.md) §U5 |
| JOB-13 | 排队时间不计入超时（远程 job 从 worker 开跑计时） | 0.80 | [design](design/2026-09-29-memory-scope-plan-tags-log-perf-design.md) §M6 |
| JOB-14 | `--lock-wait` 按 job 覆盖等锁上限；报错显示实际锁路径 | 0.83 | [design](design/2026-09-30-interactive-acp-job-and-backlog-design.md) §B1 |
| JOB-15 | 传话 job 在 Board / 列表默认隐藏 | 0.96 | [design](design/2026-10-03-worker-remote-upgrade-design.md) §U3 |
| JOB-16 | `job resume --mode --agent`（续接形态与同族 agent）；工作台新建会话可选 runner | 0.99 | [plan](plans/2026-10-03-f-batch-mobile-feedback-plan.md) |
| PLAN-04 | plan 版本化交接说明（CLI/HTTP/MCP/web），prime 注入进行中 plan 交接 | 0.71 / 0.75 | [design](design/2026-09-27-resume-display-plan-handoff-tun-web-hook-watch-design.md) §PLAN-04 |
| PLAN-05 | plan 标签与按标签过滤；Plans 状态多选 | 0.79 | [design](design/2026-09-29-memory-scope-plan-tags-log-perf-design.md) §M2/M3 |
| TUN-05 | web 按预设启停 server 托管隧道，预设 `autostart` | 0.72 | [plan](plans/2026-09-29-tun-05-hosted-forward-plan.md) |
| PTY-02 | pty 取消先发 `exit_keys` 优雅退出以捕获会话 id；`session_store_glob` | 0.84 | [design](design/2026-09-30-interactive-acp-job-and-backlog-design.md) §B2 |
| ACP-03 | ACP 持续会话 job：`job run --session`、`awaiting_input`、`job say/end`、重启 session/load 恢复 | 0.85 | [plan](plans/2026-09-30-x3-interactive-acp-session-job-plan.md) |
| ACP-04 | 远程 worker 持续会话（协议 v13 `session_cmd`） | 0.87 | [plan](plans/2026-10-01-remote-worker-session-plan.md) |
| SESS-03 | Stop 等待盯住本会话派出的 job，完成即放行并注入；`gofer session watch` | 0.73 | [plan](plans/2026-09-29-sess-03-session-job-watch-plan.md) |
| SESS-04 | web 给 Claude 终端会话发消息（等回复走中继，否则经传话人 SendMessage） | 0.86 | [design](design/2026-10-01-web-message-to-agent-session-design.md) |
| SESS-05/06 | 会话体验打磨；server 本机与 worker 侧常驻传话人（协议 v14） | 0.90–0.92 | [design](design/2026-10-02-session-ux-polish-tls-messenger-design.md) · [messenger](design/2026-10-02-worker-resident-messenger-design.md) |
| SESS-07 | 传话人可见（状态 / 投递历史 / ListAgents，协议 v16）；会话唤醒 `session resume` | 0.100 | [plan](plans/2026-10-04-m-batch-messenger-session-visibility-plan.md) |
| SESS-08 | omp / jcode hook 适配；`init hooks --global` | 0.105 | [runbook](runbook/session-relay.md) §7 |
| SESS-09 | plan job 只绑定经认证的显式来源会话（Z1/Z2）；注入标记 `injected` 与 job 完成前缀视为非人工输入 | 0.121 | 无独立设计 |
| OBS-12 | 持续会话提醒 `session.awaiting_reply`（Web Push / IM） | 0.86–0.87 | [design](design/2026-10-01-session-notify-and-remote-session-design.md) §Y2 |
| OBS-07e | IM 正文长度可配 `max_text_runes` | 0.97 | [design](design/2026-10-03-notify-length-job-redact-design.md) §V1 |
| SEC-03/04 | `job redact` / `job delete`；`job secret-scan` 跨 job 查找与批量脱敏；提交时秘密形态警告 | 0.97–0.98 | [design](design/2026-10-03-notify-length-job-redact-design.md) · [secret sweep](design/2026-10-03-secret-sweep-design.md) |
| WEB-11b | 以会话为中心的工作台（只放三类会话、简洁新建、ACP 会话列表） | 0.88–0.89 | [design](design/2026-10-02-sessions-centric-workbench-design.md) |
| WEB-14/15 | web 打磨（交接卡、Issues 页重做、待验收徽标、Skills 入设置）；日志合批渲染与分块回放 | 0.75–0.99 | [design](design/2026-09-29-web-ui-polish-and-backlog-design.md) |
| WEB-16 | job 详情 `?include=` 聚合；单连接 `/v1/ws` 主题推送，轮询降为兜底 | 0.104 | [design](design/2026-10-04-web-request-aggregation-and-live-push-design.md) |
| WF-05 | 多 agent 对比：异构扇出 + 每路 worktree、`join: pick`、`job worktree merge`、模板库；web 对比视图与模板向导 | 0.99–0.103 | [design](design/2026-10-03-multi-agent-compare-and-flow-templates-design.md) |
| CFG-10 | 命令帮助去掉计划编号（扫描测试防回归） | 0.79 | [design](design/2026-09-29-memory-scope-plan-tags-log-perf-design.md) §M4 |
| CFG-11 | HTTPS 入口 `server.tls` + `gofer tool cert` | 0.90 | [runbook](runbook/https-pwa.md) |
| CFG-12 | 配置热重载补全：`serve reload` / `worker reload --local`、需重启键清单、workers 热生效 | 0.94 | [design](design/2026-10-02-config-hot-reload-design.md) |
| CFG-13 | `worker add` / `POST /v1/workers` 登记与 onboarding | 0.95 | 无独立设计 |
| CFG-14 | 本机 runner `server`/`local` 统一规范化并设为保留名；唤醒用会话原始目录 | 0.102–0.103 | [plan](plans/2026-10-04-m78-runner-normalize-session-cwd-plan.md) |
| CFG-15 | 未声明时注入内置 `default` 项目（G044） | 0.111 | `AGENTS.md` G044 |
| SVC-02 | 远程升级 worker `gofer worker upgrade`（协议 v15，排空交接、失败回滚） | 0.96–0.98 | [design](design/2026-10-03-worker-remote-upgrade-design.md) |
| SVC-03/04 | Windows job object 允许 daemon 脱离；后台子进程不弹控制台 | 0.98 / 0.112 | [plan](plans/2026-10-03-x-batch-plan.md) |
| SVC-05 | 原生受管 server：`serve register/start/stop/restart/status/logs/upgrade`（Windows 登录任务 + supervisor、Linux systemd），升级交独立执行者、结果持久化、支持 UPX 候选；正式实例已切换（v0.121.0 起） | 0.120–0.121.1 | [design](design/2026-10-07-serve-management-and-repository-migration-design.md) · [runbook](runbook/2026-10-08-serve-management-runbook.md) |
| AGT-05 | 通用 agent 接入：`gofer hook generic`、`transcript_dialect`、`ndjson_usage_path`、`inject_process`、`session_family`、`deliver_command`（协议 v18） | 0.119 | [runbook](runbook/session-relay.md) §9 |
| L 批 | SSE 事件驱动；codex-acp 换新包入 codex 族；omp Interrupt；jcode turn 事件 | 0.106–0.107 | [plan](plans/2026-10-05-l-batch-leftovers-plan.md) |
| WORK-01 | 工作项（W1）：自动草稿、状态映射、搁置 / 提醒、每日摘要、Works 页、`gofer work` CLI/MCP/REST | 0.109 | [design](design/2026-10-05-work-items-and-steward-design.md) |
| WORK-02 | 发言者标注、请求账本（请它汇报 / 交接）、被动整理（读 transcript 尾部，协议 v17）（W2a） | 0.110 | 同上 §14.8 |
| WORK-03 | 管家 steward（W2b）：专用凭据白名单、自动注入 gofer MCP、笔记、每日巡检、「问管家」 | 0.113 | 同上 §14.9 |
| WORK-04 | Works「等我」徽标、工作项转 todo、ACP/pty 会话关联（W3）；decision→等我、带话、issue 只读 MCP、完成回写、`work.needs_me` 通知（X2） | 0.114–0.115 | 同上 §15–16 |
| WORK-05 | `gofer work rm` / `DELETE /v1/work-items/{id}` / web 删除已结束工作项 | 0.122 | 无独立设计 |

## 二、待做 / 候选（下一批从这里选）

> 2026-10-08 重排：并入用户问题清单、一份外部讨论中与现状对得上的部分、未结 issue 与本轮复审遗留。评估与分期见 [`plans/2026-10-08-next-phases-plan.md`](plans/2026-10-08-next-phases-plan.md)。期号 N1–N4 对应该计划。

| 编号 | 功能 | 价值 | 大小 | 期 | 状态 | 来源 / 细节 |
|---|---|---|---|---|---|---|
| SESS-10 | Stop 等待感知会话内子 agent / 后台任务（SubagentStart/Stop hook 计入 SUP-01 D，子 agent 结束即放行）；server 按 relay 模式下发等待预算（auto 默认 10 分钟兜底） | 高 | 中 | N1 | ✅ 0.123 | idea BUG-2 |
| OBS-13 | 每日摘要 / 提醒 0 订阅可见：无 webhook 订阅 `work.digest`/`work.remind` 时告警（日志、`steward status`、`config validate`），0 订阅不记当天已发 | 高 | 小 | N1 | ✅ 0.123 | idea BUG-1 |
| AGT-06 | 指定模型：agent `model_args` + `job run --model`（plan todo / web 表单 / MCP 同步） | 高 | 小 | N1 | ✅ 0.123 | idea #7 |
| SESS-11 | codex 会话送话：codex agent 配 `deliver_command`（`codex queue --thread`），退出码对齐 0/3，实测后进 runbook | 中 | 小 | N1 | ✅ 0.123 | idea #1 |
| PLAN-06 | plan 页显示 / 编辑绑定的主 agent 会话，并可直接给它发消息（如「写交接说明」） | 中 | 小 | N1 | ✅ 0.123 | idea #8（多会话绑定见 PLAN-07） |
| OBS-14 | 终端会话用量：hook 增量读 transcript `message.usage`，按主会话 / 子 agent（sidechain）拆分，会话与工作项展示 | 高 | 中 | N2 | ✅ 0.124 | idea #3 |
| SESS-12 | 会话催办（nudge）：按间隔或「N 分钟无进展且有未完成项」给终端会话送话，复用传话阶梯 | 中 | 中 | N2 | ✅ 0.125 | idea #2 |
| GATE-02 | 预算熔断：job / 会话级 `max_tokens`、`max_cost_usd`、`max_turns`，超限终止并标记、通知 | 中 | 中 | N2 | ✅ 0.124 | next-sug §五（用量采集已具备） |
| TRK-05 | web Issues 页「同步」：server 在仓库所在 runner 派 `gofer repo sync` 并回显结果 | 中 | 中 | N2 | ✅ 0.124 | idea #5 |
| WEB-17 | 以决策为中心的首页「今天」：待我决策队列（交互 / decision / 待验收 / 等我）、工作项里程碑墙（日志压缩成时间线，可下钻）、管家与用量条 | 高 | 大 | N3 | 📝 | next-sug 首页设计 + 用户痛点；与 WEB-11 W4 合并考虑 |
| WORK-06 | 工作项里程碑时间线：日志事件分级（里程碑 / 细节），卡片只显示里程碑，抽屉下钻到日志与 diff | 中 | 中 | N3 | ⏳ | next-sug「下钻时间线」 |
| PLAN-07 | plan 绑定多个会话（主 / 接手历史），派发与校验按集合 | 低 | 中 | N3 | ⏳ | idea #8 |
| CFG-16 | runner 声明可用外部工具（worker.yaml `tools`），随注册上报、派发时注入 prompt，job 环境加 `GOFER_WORKER_ID` | 中 | 中 | N4 | ⏳ | idea #6 |
| AUTO-04 | 事件插件：先做只读旁路（webhook `kind: exec`，事件 JSON 走 stdin），再评估决策 hook | 中 | 中→大 | N4 | ⏳ | idea #4 · roadmap-history AUTO-04 |
| MCP-06 | server 托管 MCP 注册表：按项目 / job 注入到 ACP 会话与 cli-agent（`--mcp-config`），凭据留在 server | 中 | 中 | N4 | ⏳ | next-sug §二（现有：steward 自动注入 gofer MCP） |
| GIT-02 | 交付闭环：worktree job 完成后可选推分支 / 开 PR（gh），PR 描述带用量与验证结果；push 仍需显式授权 | 低 | 中 | N4 | ⏳ | next-sug §三（worktree/merge 已具备） |
| SBX-01 | 容器沙箱 runner：job 在一次性容器内执行（挂载 worktree、默认断网 / 白名单），结束回收 | 中 | 大 | 远期 | ❄ | next-sug §一；当前 dir lock + worktree + 容器 worker 已覆盖个人场景 |
| WEB-11 W4 | 工作台并行隔离与预览（worktree 开关、`dev_command` + 隧道预览） | 中 | 大 | — | ❄ | 并入 WEB-17 评估 |
| SEC-02 | agent 以独立 OS 账号运行 | 中 | 大 | 远期 | ⏳ | SEC-01 限制 |
| JOB-06② | 密钥引用 `job run --secret` | 中 | 中 | — | ❄ | [design](design/2026-09-25-rules-injection-and-worker-init-design.md) §一 |
| ACP-02b | claude-acp 端到端（等上游端点） | 中 | 小 | — | ❄ | ACP 设计 |
| OBS-07(b)(c) | IM 入站提交 / 交互应答 | 中 | 大 | — | ❄ 用户暂不做 | [im-notification](runbook/im-notification.md) |
| WF-06 | workflow `join: judge` 裁判、按 hunk 合并 | 低 | 中 | — | ⏳ | multi-agent-compare 设计「未做」 |
| CFG-05b / JOB-05 / CFG-03 / AI-01/02 | worker init 提示 guards；mcp-agent 类型；主机侧动作；内置 AI 助手（已由管家部分覆盖） | 低 | — | — | ⏳ | roadmap-history |

## 三、建议下一批

按 [`plans/2026-10-08-next-phases-plan.md`](plans/2026-10-08-next-phases-plan.md)：

- **N1 卡点与通信（v0.123–0.124）**：SESS-10、OBS-13、AGT-06、SESS-11、PLAN-06 + 小修（SVC-05 收尾删旧脚本、`work ls --all` 与 `rm --status` 计数不一致、`work.deleted` 审计落表）。
- **N2 可见与可控（v0.125–0.127）**：OBS-14 会话用量 → GATE-02 预算熔断 → SESS-12 催办 → TRK-05 Issues 同步。
- **N3 决策中心首页（v0.128+）**：WEB-17 + WORK-06（先出设计稿与原型，人工 gate 后实施），PLAN-07 视需要。
- **N4 扩展与生态**：CFG-16、AUTO-04（只读）、MCP-06、GIT-02；SBX-01、SEC-02 远期。

## 四、维护约定

- 新条目：在「待做」加一行（编号、一句话、价值/大小、来源）；进入设计后把链接换成 design 文档；落地后移到「已落地」并只留一行。
- 细节不进本文：构想与权衡写 design，实施过程写 plan / 实测记录，操作步骤写 runbook。
- 参考项目对比写 [`refer/reference-projects.md`](refer/reference-projects.md)，本文只留条目编号。
