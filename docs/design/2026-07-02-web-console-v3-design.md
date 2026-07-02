# Gofer Web 控制台 v3 设计（含 presence/inbox 展示）

> 收敛 roadmap `WEB-06/WEB-07/WEB-08/WEB-09` 一份 design 统一 IA，并把多 agent 协作 epic 的 **presence/inbox 展示**（#6/#7 尾巴）纳入。承接 v2 只读层（[`2026-06-23-web-console-v2-readonly-design.md`](2026-06-23-web-console-v2-readonly-design.md)）与已上线的 schedules 页。
> 本文遵循公司事实标准（`std-arch-rules.md`）与 gofer 项目约定（`AGENTS.md`），不重复其已规定事实。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.2 | 2026-07-02 | claude | 评审反馈：厘清 **Projects/Agents = server 作用域**；新增 **§6.6 Worker 节点透视**（worker 握手已上报 projects/agents，`WorkerStatus` 补字段暴露 + Cluster 节点面板展示，纳入 P1 只读）。§13 待确认 5 项 + worker 透视均按推荐确认。 |
| v0.1 | 2026-07-02 | claude | 初版：WEB-06 IA / WEB-07 Dashboard(+/v1/stats) / WEB-08 列表详情补强 / **Drivers(presence)+Inbox 展示** / WEB-09 全局交互通知+应答。分 P1 只读观察层 + P2 写/交互层，写层前置=身份分级决策。 |

## 1. 概览 / 背景

v2 只读层补齐了「集群拓扑 / 项目透视 / 产物预览」，但**导航与信息架构滞后于后端能力**：

- 多 agent 协作 epic（MCP-01/03/04）已全落地并 LIVE——在线 driver 名册（presence）、信箱（inbox）、监督 agent（supervisor）、角色（roles）在后端可查，**web 完全没有展示**（roadmap #6/#7）。
- sup job 混在普通 job list 里**无标记**，无法一眼区分"谁是监督 / 谁带角色"。
- 首页直接落 board，缺"整体运行态一眼观察"的 dashboard。
- job 列表无分页、无时间列；详情 running 态看不到 rendered 命令、STDOUT/STDERR 两栏过窄、无 ANSI 色彩。
- `pending_interaction`（含 sup 升级留人 `needs_human`）**只能进 JobDetail 单页看**，跨页无感知、无法就地应答。

v3 目标：**把控制台从"看板+详情"升级为"整体可观察 + 舰队/agent 透视 + 可就地交互"**，且严格按 **只读观察层（P1，便宜先行、零写风险）→ 写/交互层（P2，需身份分级）** 切分（SR1402 闭环、v2 同款安全取向）。

## 2. 名词

| 术语 | 含义 |
|---|---|
| **agent（配置）** | `config.agents` 里登记的 CLI/exec 能力（claude/codex/exec），现 `Agents.vue` 展示其可用性。**非**运行实例。 |
| **driver / presence** | 经 mcp `gofer_register` 注册到 serve 的**在线 agent 实例**（`agent_id`+name+role+project+last_seen），MCP-04。一个工作目录可多会话各一 id。 |
| **inbox** | 某 driver 的消息信箱（escalation / 派活 / 通知），只读观察经 `GET /v1/agents/{id}/inbox`（不消费、不刷心跳）。 |
| **supervisor** | role=supervisor 的 driver，自动应答低危 `pending_interaction`、高危 punt 留人（MCP-01 / `y5wt`）。 |
| **escalation** | `pending_interaction` 被 sup 判定需人工（`needs_human=1`）或升级到 owner/sup 的状态。 |
| **role（角色）** | MCP-03 角色预设；job/driver 均可带 `role`，列表据此标记。 |

> **作用域澄清（评审要点）**：`Projects`/`Agents` 页均为 **server 中央配置**作用域——`/v1/projects`·`/v1/agents` 来自 serve 的 `config`，且 Agents 可用性在 **serve 主机**探测。**worker 节点**各有自己的 projects/agents（`worker.yaml`），在 WS 握手 `wsproto.Register` 帧里**已上报**给 serve、`workerConn.meta` **已保留**，但现有 `WorkerStatus` 只暴露 labels——v3 §6.6 补其暴露。`Drivers(presence)` 是在线 mcp driver 实例，与前两者**正交**。

## 3. 范围

**含（本设计）**：

- **WEB-06** 导航 / 信息架构重构（壳层）——补 dashboard / drivers 入口，nav 分组。
- **WEB-07** Dashboard 首页——新增轻量聚合端点 `/v1/stats`。
- **WEB-08** job 列表 / 详情体验补强——分页 + 时间列 + running rendered cmd + STDOUT/STDERR tab + ANSI。
- **Drivers(presence) + Inbox 展示**——在线 driver 名册 + inbox 观察 + sup/role 标记（roadmap #6/#7；只读）。
- **WEB-09** 全局交互通知 + Web 应答——跨页 `pending_interaction` 通知 + choice/confirm 就地应答 / punt（**写层**）。
- **Worker 节点透视**——Cluster 节点面板展示各 worker **自报的 projects/agents/labels**（`WorkerStatus` 补字段暴露；只读），厘清"server 中央 vs worker 节点能力"（§6.6）。

**不含（留后续，各需独立设计）**：

- WEB-03 浏览器 pty 交互（改执行模型 + 会话审计）。
- WEB-04③ 配置查看 / 编辑写层（写回 + reload + secret 不回显）。
- CFG-03 主机侧动作（编辑器打开）。
- 完整 AUTO-01 审批门（WEB-09 只做交互应答面，审批门语义独立设计；WEB-09 为其未来 UI 面）。
- SSE 推送 inbox / pending（本设计用轮询，与现有页一致；SSE 增量为后续优化）。
- driver 主动派活 / post message（写 presence，属协作写层，非本设计）。

**分期**：

- **P1 只读观察层**（无写风险、数据源多现成）：WEB-06 IA + WEB-07 Dashboard + WEB-08 补强 + Drivers/Inbox 展示。
- **P2 写 / 交互层**：WEB-09（answer/punt），**前置=身份分级决策**（见 §12 安全 / §13 待确认）。

## 4. 已确认事项

1. 后端契约**基本齐备**，P1 仅需两处小后端改动：新增 `/v1/stats`（WEB-07）、`/v1/jobs` handler 补读 `offset`（WEB-08 分页，`jobstore.ListQuery.Offset` 已支持）。其余（presence/inbox/pending-interactions/answer/punt）端点均已上线。
2. Job view 已暴露 `role`/`origin_agent`/`escalate_to`；Interaction view 已含 `needs_human`/`escalated_at`/`answered_by` —— **sup/role 标记与 escalation 展示是纯前端**（补 `types.ts` 字段即可）。
3. 前端改动**需 `make web` 重 embed + 重建二进制**才在控制台生效（v2 同款约束）。
4. 沿用现有前端栈与风格：Vue 3 `<script setup>` + vue-router + fetch client（`api/client.ts`）+ 轮询 + Page Visibility 暂停 + `tokens.css` 视觉 token（mono/phosphor）。
5. **worker 已上报 projects/agents**：`wsproto.Register` 帧含 `Projects`/`Agents`/`Labels`，serve 端 `workerConn.meta` 完整保留；现 `WorkerStatus` 仅暴露 labels → **补 2 字段即可透出**（§6.6），无需 worker 侧改动。

## 5. 架构总览

### 5.1 信息架构 / 导航（WEB-06）

顶栏 nav 按**语义分组**（现平铺 7 项 → 分 3 组 + 首页）：

```
Gofer ▸ agent bridge
  [ Home ]                          # 新增 dashboard 首页(WEB-07)
  观察  · Board · Workflows · Schedules
  舰队  · Drivers · Agents · Runners · Cluster · Projects
                                     # Drivers 为新增(presence)
  顶栏右: + 新建 job · + 新建 cron · 🔔(escalations) · conn · theme · 登出
```

- `Home`（`/`，原 redirect→/board 改 redirect→/dashboard）：整体运行态。
- `Drivers`（`/drivers`，新增）：在线 driver 名册（presence）+ 点击看 inbox。
- `🔔 escalations`（顶栏铃铛，WEB-09）：跨页 `pending_interaction` 计数 + 下拉；未读 `needs_human` 高亮。
- 现有页保留；`Agents.vue`（配置态能力）与 `Drivers`（运行态实例）并列、职责区分（§2）。

窄屏沿用 v2 抽屉；nav 分组用小标题分隔。

> HTML 效果预览 [web-v3-preview.html](web-v3-preview.html)

### 5.2 前后端分层

- **前端**：新增 `Dashboard.vue` / `Drivers.vue`，改造 `Board.vue`（分页/时间列/标记）、`JobDetail.vue`（running cmd / tab / ANSI），新增全局组件 `EscalationBell.vue` + `InteractionToast.vue`（挂 `App.vue`）。API 全走 `api/client.ts` 新增方法。
- **后端**（仅 P1 两处 + P2 复用）：`GET /v1/stats`（新，聚合读）；`GET /v1/jobs` 补读 `offset`。P2 复用既有 `answer`/`punt`。**无新表**（presence/messages/interactions 表已存在）。

## 6. 模块设计

### 6.1 WEB-06 导航 / IA（壳层，纯前端）

- 改 `App.vue` nav：分组数组 `{group,label,items[]}`，渲染小标题 + 链接；顶栏加 `EscalationBell`。
- `router.ts`：`/` → `/dashboard`；新增 `/dashboard`、`/drivers`、`/drivers/:id`（inbox）。
- 侧轨（rail）保留 projects/agents，补 **drivers 计数**（在线数）与 supervisor 在线指示灯。

### 6.2 WEB-07 Dashboard 首页（+ `/v1/stats`）

**后端**：新增 `GET /v1/stats`（authed，只读聚合，勿让前端拉全量自算）：

```jsonc
{
  "jobs":   { "total": 0, "by_status": {"running":0,"done":0,"failed":0,"queued":0,"pending_interaction":0,"cancelled":0,"timeout":0} },
  "workflows": { "running": 0, "total": 0 },
  "schedules": { "total": 0, "enabled": 0 },
  "runners": { "workers_connected": 0, "workers_total": 0, "peers_up": 0 },
  "drivers": { "online": 0, "supervisors": 0 },
  "escalations_pending": 0,          // needs_human=1 的 pending interaction 数
  "projects": 0,
  "server_time": 0                    // 毫秒(SR102)
}
```

实现：`jobstore` 补 `CountJobsByStatus()` / `CountSchedules()`（复用现有计数思路，如 `CountActiveJobsByRole`）；drivers 取 `presence.List`；escalations 复用 `ListPendingInteractions` 过滤 `needs_human`。轻量、单次聚合。

**前端** `Dashboard.vue`：卡片网格——服务健康、job 状态分布（可小柱/数字块，参考 `dataviz` 简约风格但控制台优先纯数字块）、workers/peers/drivers/supervisors 在线、schedules 启用数、escalations 待处理（红点跳转铃铛）。轮询 5s、Page Visibility 暂停。

### 6.3 Drivers(presence) + Inbox 展示（重点，只读）

**Drivers.vue（`/drivers`）**——在线 driver 名册，对接 `GET /v1/agents/presence`（`?role=`/`?project=` 过滤）：

- 列：状态点（online/stale 按 `last_seen_at` 与 TTL）· name · **role 徽标**（`supervisor` 高亮/特殊色）· project · client · `agent_id`(短) · last_seen（`fmtDateTime`+相对）。
- 过滤：role 下拉（全部/supervisor/其他）、project。轮询 3s。
- 行点击 → `/drivers/:id`（inbox 观察）。
- 空态提示"无在线 driver（经 `gofer mcp --server` 注册后出现）"。

**Inbox 观察（`/drivers/:id`）**——对接 `GET /v1/agents/{id}/inbox?include_read=1`（只读，不消费不刷心跳）：

- 顶部 driver 摘要（name/role/project/last_seen）。
- 消息列表：kind 徽标（escalation/assign/notify…）· from_agent · body（可展开）· ref（若指向 job/interaction 则链接跳转）· created_at。`include_read` 开关切「仅未读/全部」。
- **纯观察**：不提供 poll/ack/post（那是 driver 自己或写层的事）。

**sup/role 标记（#7，前端）**：`Board.vue` 行与 `JobDetail.vue` 头部，用 job view 的 `role` 字段渲染徽标（`role=supervisor` 特殊标记），`origin_agent`/`escalate_to` 在详情展示"归属 owner / 升级目标"。

**#6 presence 可见性**：Drivers 页即满足；另在 Dashboard 显示 online/supervisors 计数。

### 6.4 WEB-08 job 列表 / 详情补强

**列表 `Board.vue`**：

- **分页**：底部 `上一页/下一页` + 页码，`listJobs({limit, offset})`；后端 handler 补读 `?offset`（jobstore 已支持）。默认 `limit=50`。保留现有 status/project 过滤。
- **时间列**：末列加提交/结束时间 `hh:mm:ss`（`fmtDateTime`）。
- **role/channel 标记**：行内小徽标（sup/role、channel=cron/web/mcp/cli，复用 OBS-08 provenance）。

**详情 `JobDetail.vue`**：

- **running 即显 rendered cmd**（#5 / 补 OBS-04 gap）：running 态即拉 `GET /v1/jobs/{id}/request` 渲染"实际执行命令"，不等终态。
- **STDOUT/STDERR tab 切换**：两栏改 tab（默认 stdout），解决窄栏；复用现有 `LogTape` / SSE。
- **ANSI 色彩渲染**：日志经 ANSI→HTML（引入轻量 `ansi_up` 或自写最小 SGR 解析 + DOMPurify sanitize，与 v2 `FilePreview` 同款防 XSS）。

### 6.5 WEB-09 全局交互通知 + Web 应答（写层，P2）

**通知（读）**：全局 `EscalationBell.vue`（挂 `App.vue`）轮询 `GET /v1/interactions?status=pending`（跨活跃 job）：

- 铃铛显 pending 数；`needs_human=1`（sup 已 punt 留人）红点高亮、置顶。
- 下拉列出：job 短 id + prompt 首行 + type + escalated/needs_human 标记 → 点击进 JobDetail 或就地应答。
- 新出现的 `needs_human` 触发右下角 `InteractionToast.vue`（可关闭、点击跳转）。

**应答（写）**：choice/confirmation 就地回复（question 引导进 JobDetail 输入）：

- `POST /v1/jobs/{id}/interactions/{iid}/answer`（已存在，`answerInteraction`）→ 记 `answered_by`=当前 caller_id。
- `POST /v1/jobs/{id}/interactions/{iid}/punt`（已存在）→ 留人（web 侧一般不 punt，保留入口给"我也拿不准"）。
- 应答后本地移除 + 刷新计数。

**前置（硬约束）**：写操作必须先定**身份分级**（§12）。P1 只做通知/展示（读），应答按钮在 P2 且身份决策落地后启用。

### 6.6 Worker 节点透视（projects/agents，只读，P1）

厘清作用域（§2 澄清）并补齐"舰队/agent 透视"：Projects/Agents 页是 **server 中央真源**；worker 节点各有自己的能力，本节把 worker **握手已上报**的 projects/agents 透出到 UI。

- **后端（小改）**：`WorkerStatus`（`runner_handler.go`）加 `Projects []string` + `Agents []string`，从 `workerConn.meta.Projects/Agents` 填（数据已在内存）；`/v1/runners` 的 workerView + `/v1/meta.workers` 一并暴露（复用现有 labels 透出路径）。**worker 侧零改动**（已在 `wsproto.Register` 发送）。
- **前端**：`Cluster.vue` 节点面板（WEB-04 已有）点击 worker 时，除心跳/in-flight/labels 外，增列该 worker 的 **projects / agents**；`Agents.vue` 顶部加一句作用域说明"以下为 serve 主机 agents；worker 节点 agents 见 Cluster"。
- **边界**：只展示 worker **自报**名单（非在 worker 上实时探测可用性）；不画"项目→节点"依赖边（D2，与 v2 一致）；stale/离线 worker 不展示其能力。

## 7. 关键流程

### 7.1 escalation 通知 → Web 应答（WEB-09）

```
sup 判定高危/无白名单 → gofer_punt_interaction → interaction.needs_human=1 (LIVE 已实证)
        │
web EscalationBell 轮询 /v1/interactions?status=pending  ──▶ 发现 needs_human=1
        │                                                     铃铛红点 + Toast 弹出
操作者点击 choice/confirm 就地应答 ──▶ POST .../answer (answered_by=caller_id)
        │
job 续跑；下轮轮询该 interaction 消失（status=answered），计数回落
```

### 7.2 presence + inbox 观察（只读）

```
driver 经 gofer mcp --server 注册 ──▶ agent_presence 表
web Drivers.vue 轮询 /v1/agents/presence ──▶ 在线名册(含 supervisor)
点击某 driver ──▶ GET /v1/agents/{id}/inbox?include_read=1 (不消费/不刷心跳) ──▶ 观察 escalation backlog
```

## 8. 数据模型

**无新表**。P1 涉及的表均已存在：`jobs` / `job_interactions` / `agent_presence` / `messages` / `schedules` / `workflows`。

前端 `api/types.ts` 增量：

- `Job` 补 `role? / origin_agent? / escalate_to?`（后端 view 已有，omitempty）。
- `Interaction` 补 `needs_human? / escalated_at? / answered_by?`（后端已有）。
- 新增 `Presence`(=Agent)、`InboxMessage`(=Message)、`Stats` 类型（对齐 §6.2/§6.3 JSON）。
- `ListJobsOpts` 补 `offset?`。
- `RunnerWorker` / `MetaWorker` 补 `projects? / agents?`（§6.6，worker 自报名单）。

## 9. 后端改动清单（最小）

| 改动 | 位置 | 说明 |
|---|---|---|
| 新增 `GET /v1/stats` | `httpapi` + `server.go` 注册 | authed 只读聚合（§6.2）。handler 组装 jobstore 计数 + presence.List + pending 过滤。 |
| `GET /v1/jobs` 补读 `offset` | `job_handler.go` `handleListJobs` | `strconv.Atoi(c.Query("offset"))` → `ListQuery.Offset`（jobstore 已支持，仅 handler 没读）。 |
| jobstore 计数辅助 | `jobstore/jobs.go` `schedules.go` | `CountJobsByStatus()`/`CountSchedules()`（若无现成），供 `/v1/stats`。 |
| `WorkerStatus` 加 `Projects`/`Agents` | `runner_handler.go` + wshub 透出 `wc.meta` | 从 `workerConn.meta` 填，`/v1/runners`+`/v1/meta.workers` 暴露（§6.6）。worker 侧零改动。 |

其余端点（presence/inbox/pending-interactions/answer/punt）**零改动**，前端直接对接。

## 10. API 契约（前端对接）

| 方法 | 路径 | 用途 | 状态 |
|---|---|---|---|
| GET | `/v1/stats` | Dashboard 聚合 | **新增（P1）** |
| GET | `/v1/jobs?limit=&offset=&status=&project=` | 列表分页 | **补 offset（P1）** |
| GET | `/v1/jobs/{id}/request` | running rendered cmd | 已存在 |
| GET | `/v1/agents/presence?role=&project=` | 在线 driver 名册 | 已存在 |
| GET | `/v1/agents/{id}/inbox?include_read=1` | inbox 只读观察 | 已存在 |
| GET | `/v1/interactions?status=pending` | 跨 job 待处理交互 | 已存在 |
| GET | `/v1/runners` | worker/peer/local 名册 | **worker 补 projects/agents 字段（P1，§6.6）** |
| GET | `/v1/meta` | 表单选项聚合 | **worker 补 projects/agents（P1，§6.6）** |
| POST | `/v1/jobs/{id}/interactions/{iid}/answer` | Web 应答（写，P2） | 已存在 |
| POST | `/v1/jobs/{id}/interactions/{iid}/punt` | 留人（写，P2） | 已存在 |

## 11. 安全

- **只读边界（P1）**：Drivers/Inbox/Dashboard/list 全部 GET，无写；inbox 走 `ListInbox`（不消费、不刷心跳），不泄露 `agent_token`（`presence.Agent` 无 token 字段）。沿用 v2 只读取向、零新风险。
- **WEB-09 身份分级（P2 前置硬约束）**：现 bearer token → `caller_id`（`auth.go` 支持多 caller）。写操作（替 agent 应答 / 放行）**必须可追溯到人**：
  - **推荐（务实）**：不新建 RBAC；应答归属当前 `caller_id`（已记 `answered_by`），运维为 web 操作者配**独立 caller token**（区别于共享/CI token），使 `answered_by` 有意义；可选给 caller 加 `can_answer` 能力位控制写。
  - **反面（重、暂不做）**：完整 web 登录 + 用户体系 + 每操作审批。
  - 未定不启用 P2 写按钮（P1 只读可先上）。
- **XSS**：ANSI→HTML 与 inbox body 渲染经 DOMPurify（v2 `FilePreview` 同款）。
- **secret**：本设计不展示配置 secret（SR403/805 不涉及）。

## 12. 部署

前端改动，遵循 v2 约束：主机 `make web`（pnpm build + embed 到 `internal/webui/dist`，已 gitignore）+ `make build`/`make install` 重建二进制 + 重启 host serve 才生效。容器内 node_modules 为 host 符号链接农场，构建走 `gofer job --runner local` 主机跑。P1 后端两处小改随二进制一起出。

## 13. 待确认

> **2026-07-02：以下 5 项均按推荐确认**（用户"按推荐确认"），worker 节点透视亦确认纳入 P1（§6.6）。保留原文留决策痕迹，可进 plan。

1. **WEB-09 身份分级取向**（§11）：采用"复用 caller_id + 独立 operator token + 可选 `can_answer` 能力位"务实方案？还是本期只做 P1 只读、WEB-09 应答留到鉴权体系另议？（**推荐前者，且 P1 可独立先上**）
2. **Drivers 是否独立 nav 项** vs 并入 `Agents.vue` 分 tab？（推荐独立项：配置态 vs 运行态职责不同）
3. **Dashboard job 状态分布**：纯数字块 vs 迷你柱状图？（推荐先纯数字块，控制台风格一致、零依赖）
4. **ANSI 渲染**：引入 `ansi_up` 依赖 vs 自写最小 SGR 解析？（推荐 `ansi_up`，成熟、体积小；经 DOMPurify）
5. `/v1/stats` 命名与形状是否 OK，或并入扩展 `/v1/meta`？（推荐独立 `/v1/stats`，语义清晰、不撑大 meta）

## 14. 结论

v3 用**一份设计**收敛 WEB-06/07/08/09 + presence/inbox 展示，绝大部分是**只读快赢**（P1：IA 重构 + Dashboard + 列表详情补强 + Drivers/Inbox），后端仅两处小改（`/v1/stats` + `offset`），无新表；唯一写层 WEB-09 隔离到 P2 且以**身份分级**为前置。落地后：整体运行态一眼可观察、多 agent 协作（presence/inbox/supervisor/roles）在 web 可见、escalation 可就地应答——补齐 roadmap 三轴里"可观察 + 可交互"的 web 面。

确认本设计（尤其 §13 待确认项）后，再出 P1/P2 实施计划（SR1107，`plan` 文档）。
