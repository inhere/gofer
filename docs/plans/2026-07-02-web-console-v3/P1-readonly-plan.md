# Web 控制台 v3 · P1 只读观察层 实施计划

> 设计：[`../../design/2026-07-02-web-console-v3-design.md`](../../design/2026-07-02-web-console-v3-design.md)（v0.2）。预览：[`../../design/web-v3-preview.html`](../../design/web-v3-preview.html)。
> 本文只覆盖 **P1（只读观察层）**；P2（WEB-09 写层应答）另出 `P2-interaction-write-plan.md`。
> 所有代码锚点基于 2026-07-02 现状核对（子 agent 调查）。**每个子任务完成后即 commit**（SR1202）。

## 进度跟踪

- [x] **P1.1** 后端 · worker 节点透视（projects/agents 4 层串联）✅ host codex 实施 + 容器验收(build/vet/test 全绿)
- [ ] **P1.2** 后端 · `GET /v1/stats` + `/v1/jobs` 补读 `offset`
- [ ] **P1.3** 前端 · types + client 基础（所有 P1 前端的依赖底座）
- [ ] **P1.4** 前端 · WEB-06 导航/IA + EscalationBell（**只读**：计数/下拉/toast，无应答按钮）
- [ ] **P1.5** 前端 · WEB-07 Dashboard 首页
- [ ] **P1.6** 前端 · Drivers(presence) + Inbox 展示
- [ ] **P1.7** 前端 · Board 补强 + Cluster worker 透视 + Agents 作用域说明 + JobDetail rendered-cmd 前置

## 现状校正（勿重做 / 已具备）

子 agent 核对确认以下 design 里列的点**已实现**，P1 **只验证不重写**：

- **Board 末列时间 `hh:mm:ss`**：`Board.vue` `rowTime()` 已输出 `HH:MM:SS`，`col-time` 列已在。
- **JobDetail STDOUT/STDERR tab**：`LogTape.vue` 内部已实现（`activeStream` + 自动聚焦 stderr）。
- **JobDetail ANSI 色彩**：`LogTape.vue` `renderAnsi()` 已实现（先 `escapeHtml` 再拼 `<span class="ansi-*">`，`v-html` 渲染，安全）。仅当发现未覆盖的 SGR 码时扩 `applyAnsiCode`。
- **Board runner 列 + worker_id + `.remote` 高亮**、**JobDetail meta 的 channel/client** 均已渲染。

因此 WEB-08 的实际待做仅剩：**offset 分页**（完全没有）、**role/channel 行内徽标**（channel 有字段未渲染；role 需补 TS 类型）、**rendered-cmd 提出 `hasOutcomes` 门控**（running 即显）。

## 前置 / 环境

- **Go 构建/测试在容器内**：`go build ./cmd/gofer`、`go test ./...`（workspace GOWORK 模式）。
- **前端构建在主机**（容器 node_modules 是 host 符号链接农场，不能直接 build）：`gofer job run -p demo-project -a exec --runner local --cwd tools/gofer/web --timeout 600 --sync -- pnpm build`（= `vue-tsc --noEmit && vite build`）。
- **眼检**：`agent-browser`（容器全局）对 `pnpm build` 产物或 `file://` 预览截图。
- **上线生效**：主机 `make web`（embed→`internal/webui/dist`）+ `make build`/`make install` 重建二进制 + 重启 host serve（前端改动约束，v2 同款）。P1 前后端一并出后再统一部署。
- 端点 smoke：起 ephemeral serve（**不碰 live serve**）或对 live serve 只读 GET（`source /path/to/ws-root/config/linux/gofer/.env` 取 token）。

---

## P1.1 后端 · worker 节点透视（§6.6）

**目标**：把 worker 握手已上报、`workerConn.meta` 已保留的 `Projects`/`Agents` 透出到 `/v1/runners` 与 `/v1/meta.workers`。worker 侧零改动。数据源：`wsproto.Register`（`internal/wsproto/frames.go:9`，含 `Labels`/`Projects`/`Agents`）。

**4 处协同改动（照 `Labels` 现成路径加两字段）**：

1. `internal/wshub/registry.go`
   - `WorkerSnapshot` struct（:252）加字段：
     ```go
     type WorkerSnapshot struct {
         WorkerID      string
         LastHeartbeat int64
         InFlight      int
         Labels        []string
         Projects      []string // NEW — from wc.meta.Projects
         Agents        []string // NEW — from wc.meta.Agents
     }
     ```
   - `WorkerRegistry.WorkerSnapshot()`（:264）复制（与 `labels` 同款 defensive copy）：
     ```go
     projects := append([]string(nil), wc.meta.Projects...)
     agents := append([]string(nil), wc.meta.Agents...)
     return WorkerSnapshot{
         WorkerID: wc.workerID, LastHeartbeat: wc.lastHeartbeat.Load(),
         InFlight: wc.inflightCount(), Labels: labels,
         Projects: projects, Agents: agents,
     }, true
     ```
2. `internal/serve/probe.go`（`hubWorkerRegistry.WorkerStatus`，:26）：把 `snap.Projects`/`snap.Agents` 带进 `httpapi.WorkerStatus`。
3. `internal/httpapi/runner_handler.go`
   - `WorkerStatus` struct（:40）加 `Projects []string` / `Agents []string`（无 json tag，内部适配类型）。
   - `workerView`（:89）加 `Projects []string \`json:"projects,omitempty"\`` / `Agents []string \`json:"agents,omitempty"\``。
   - `renderWorkerStatus`（:185）填：`v.Worker.Projects = ws.Projects; v.Worker.Agents = ws.Agents`。
4. `internal/httpapi/meta_handler.go`
   - `metaWorker`（:51）加 `Projects []string \`json:"projects,omitempty"\`` / `Agents []string \`json:"agents,omitempty"\``。
   - `metaWorkers()`（:144）在 `ws.Connected` 分支填 `mw.Projects = ws.Projects; mw.Agents = ws.Agents`。

**测试**：扩现有 wshub registry 测试断言 `WorkerSnapshot` 回带 Projects/Agents；扩 runner/meta handler 测试（若有 worker fixture）断言 `worker.projects`/`worker.agents` 出现在 JSON。

**验收**：
- `go build ./cmd/gofer && go test ./internal/wshub/... ./internal/httpapi/... ./internal/serve/...` 全绿。
- ephemeral serve + 一个带 `projects`/`agents` 的 worker 注册后，`GET /v1/runners` 该 worker 的 `worker.projects`/`worker.agents` 非空；`GET /v1/meta` 对应 worker 同样。
- 无 worker / 离线 worker：字段 `omitempty` 不出现（不回退全量）。

**commit**：`feat(web-api): surface worker-reported projects/agents on /v1/runners+/v1/meta (§6.6)`

---

## P1.2 后端 · `/v1/stats` + `/v1/jobs` offset

**目标**：Dashboard 单次聚合端点 + job 列表分页所需的 offset。

**改动**：

1. `internal/jobstore/jobs.go` 新增按状态计数（当前**无** `CountJobsByStatus`，只有 `CountActiveJobsByRole`:291）：
   ```go
   // CountJobsByStatus returns a status->count map over all jobs (single GROUP BY).
   func (s *Store) CountJobsByStatus() (map[string]int, error) {
       rows, err := s.db.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
       if err != nil { return nil, err }
       defer rows.Close()
       out := make(map[string]int)
       for rows.Next() {
           var st string; var n int
           if err := rows.Scan(&st, &n); err != nil { return nil, err }
           out[st] = n
       }
       return out, rows.Err()
   }
   ```
2. `internal/httpapi/stats_handler.go`（新文件）`handleStats`：
   - jobs：`s.jobs.Meta().CountJobsByStatus()` → `by_status` + `total`（求和）。
   - schedules：`s.jobs.Meta().ListSchedules("", false)` → `total=len`，`enabled=count(Enabled==1)`（`ScheduleRecord.Enabled int`，schedules.go:20；无计数辅助，内存派生）。
   - drivers：`s.presence.List("", "")` → `online=len`，`supervisors=count(role=="supervisor")`（`presence` 可能为 nil→置 0）。
   - escalations_pending：`s.jobs.ListPendingInteractions()` 过滤 `NeedsHuman==1` 计数。
   - runners：遍历 `s.runners` + `s.workers.WorkerStatus(id)` 统计 `workers_connected/workers_total`；peers 用 prober 缓存统计 `peers_up`。
   - projects：`len(s.projects.List())`。
   - workflows：若无廉价计数则**本期置 `{running:0,total:0}` 或省略**（标注 TODO，不为它加重后端）。
   - `server_time`：毫秒（SR102）。
   - 形状对齐 design §6.2 JSON。
3. `internal/httpapi/server.go`：authed group 内注册 `r.GET("/stats", s.handleStats)`（照 `r.GET("/runners", s.handleListRunners)`:272 样板）。
4. `internal/httpapi/job_handler.go` `handleListJobs`：补读 offset（现只读 limit/since）：
   ```go
   offset, _ := strconv.Atoi(c.Query("offset"))
   // ...组装 ListQuery 时：
   q.Offset = offset // <=0 时 jobstore 自动忽略（jobs.go:390）
   ```
   `ListQuery.Offset` 已支持（jobs.go:111 + jobs_test.go:298 已覆盖分页）。

**测试**：新增 `stats_handler_test.go`（塞几条不同 status 的 job + 1 schedule + mock presence，断言聚合数字）；`handleListJobs` offset 传参断言（handler 层）。

**验收**：
- `go test ./internal/jobstore/... ./internal/httpapi/... ` 全绿。
- `GET /v1/stats` 返回 design §6.2 形状，`jobs.total` = 各状态之和；`schedules.enabled ≤ total`。
- `GET /v1/jobs?limit=2&offset=2` 返回第 3–4 条（与 `?limit=2` 首页不重叠）。

**commit**：`feat(web-api): add GET /v1/stats aggregate + /v1/jobs offset pagination`

---

## P1.3 前端 · types + client 基础

**目标**：一次补齐所有 P1 前端依赖的类型与 API 方法（后续任务直接用）。

**改动**：

1. `web/src/api/types.ts`
   - `Job` 补（后端 `JobResult` 已下发，`internal/job/model.go:179-186` 有 `role`/`origin_agent`/`escalate_to`）：
     ```ts
     role?: string
     origin_agent?: string
     escalate_to?: string
     ```
   - `Interaction` 补（后端 `interaction.go:60-82` 已有）：`needs_human?: number` `escalated_at?: number` `answered_by?: string`。
   - `RunnerWorker` 补：`projects?: string[]` `agents?: string[]`。`MetaWorker` 同补。
   - `ListJobsOpts` 补：`offset?: number`。
   - 新增：
     ```ts
     export interface Presence { agent_id: string; name: string; role?: string; project_key?: string; client?: string; status: string; last_seen_at: number }
     export interface PresenceResp { agents: Presence[] }
     export interface InboxMessage { id: string; from_agent: string; to_spec?: string; kind: string; body?: string; ref?: string; created_at: number }
     export interface InboxResp { messages: InboxMessage[] }
     export interface Stats { jobs: { total: number; by_status: Record<string, number> }; workflows: { running: number; total: number }; schedules: { total: number; enabled: number }; runners: { workers_connected: number; workers_total: number; peers_up: number }; drivers: { online: number; supervisors: number }; escalations_pending: number; projects: number; server_time: number }
     ```
2. `web/src/api/client.ts`
   - `listJobs`：`URLSearchParams` 增 `if (opts?.offset != null) params.set('offset', String(opts.offset))`。
   - 新增：
     ```ts
     export function getStats(): Promise<Stats> { return request<Stats>('/v1/stats') }
     export function listPresence(role?: string, project?: string): Promise<PresenceResp> {
       const p = new URLSearchParams(); if (role) p.set('role', role); if (project) p.set('project', project)
       const qs = p.toString(); return request<PresenceResp>(`/v1/agents/presence${qs ? `?${qs}` : ''}`)
     }
     export function listInbox(id: string, includeRead = true): Promise<InboxResp> {
       const qs = includeRead ? '?include_read=1' : ''
       return request<InboxResp>(`/v1/agents/${encodeURIComponent(id)}/inbox${qs}`)
     }
     ```

**验收**：`pnpm build`（主机）type-check 通过（无未用/类型错误）。

**commit**：`feat(web): types + client for stats/presence/inbox + job offset (v3 P1)`

---

## P1.4 前端 · WEB-06 导航/IA + EscalationBell（只读）

**目标**：导航分组 + 新增入口 + 只读 escalation 铃铛/toast。**P1 只读**：铃铛只显计数+下拉列表+点击跳 JobDetail，**无就地应答按钮**（应答=P2）。

**改动**：

1. `web/src/router.ts`：`/` redirect 改 `/dashboard`；新增
   ```ts
   { path: '/dashboard', name: 'dashboard', component: () => import('./views/Dashboard.vue') },
   { path: '/drivers', name: 'drivers', component: () => import('./views/Drivers.vue') },
   { path: '/drivers/:id', name: 'driver-inbox', component: () => import('./views/DriverInbox.vue'), props: true },
   ```
2. `web/src/App.vue`：
   - `navItems` 改为分组结构（观察 / 舰队）+ `Home`；模板按组渲染小标题（参考预览 `.nav .grp/.glabel`，注意分组标签给足间距，别挤成竖排——预览里的 nit）。
   - 顶栏右加 `<EscalationBell />`（在 + 新建 cron 与 conn 之间）。
   - 侧轨 `Drivers` 小节：显在线 driver 计数 + supervisor 指示灯（复用 `listPresence` 或让 EscalationBell/rail 各自轮询；rail 可只在 loadRail 时拉一次 presence 计数）。
3. `web/src/components/EscalationBell.vue`（新，只读）：
   - 轮询 `listPendingInteractions()`（复用现有 `getInteractions`? 不——用 `GET /v1/interactions?status=pending`，client 现有 `listPendingInteractions`？确认：`api/client.ts` 有跨 job pending 的方法则复用，否则补一个 `listPendingInteractions()`→`/v1/interactions?status=pending`）。**补 client 方法**（若无）：
     ```ts
     export function listPendingInteractions(): Promise<{ interactions: Interaction[] }> {
       return request<{ interactions: Interaction[] }>('/v1/interactions?status=pending')
     }
     ```
   - 徽标显 `interactions.length`；`needs_human===1` 计数红色高亮。
   - 下拉列出每条：job 短 id + prompt 首行 + type + `needs_human/escalated` 标记；点击 `router.push('/jobs/'+job_id)`。
   - 新出现的 `needs_human` → `InteractionToast.vue`（右下角，可关闭、点击跳转）。用一个 `seen` Set 去重避免每轮重弹。
   - 轮询 5s + Page Visibility 暂停。
4. `web/src/components/InteractionToast.vue`（新）：受控展示单条 needs_human 提示。

**验收**：`pnpm build` 通过；agent-browser 眼检：nav 分组显示正常、`/` 落 `/dashboard`、铃铛计数随 pending 变化、点击下拉项跳 JobDetail、console 0 报错。

**commit**：`feat(web): v3 nav/IA groups + read-only escalation bell/toast (WEB-06)`

---

## P1.5 前端 · WEB-07 Dashboard 首页

**目标**：`Dashboard.vue` 消费 `getStats()`，纯数字块卡片（§13 推荐）。

**改动**：`web/src/views/Dashboard.vue`（新）——参考预览 `pv-1-dashboard`：
- `onMounted` + 5s 轮询（Page Visibility 暂停）拉 `getStats()`。
- 卡片网格（`.grid` 4 列，窄屏降列）：服务健康（conn/serve）· Drivers 在线（含 supervisors）· Runners（workers_connected/total + peers_up）· Escalations 待处理（`escalations_pending`，>0 红 + 点击跳铃铛/pending）· Jobs 状态分布（`by_status` 数字块行，复用 `statusColor`）· Schedules（enabled/total）· Projects。
- 视觉 token 复用 `tokens.css`；数字块用 `.mono`。
- 空/加载/错误态。

**验收**：`pnpm build` 通过；眼检卡片数字与 `/v1/stats` 一致、状态色正确、`escalations_pending>0` 高亮；轮询点亮。

**commit**：`feat(web): v3 dashboard home consuming /v1/stats (WEB-07)`

---

## P1.6 前端 · Drivers(presence) + Inbox 展示（§6.3 重点）

**目标**：在线 driver 名册 + inbox 只读观察。

**改动**：

1. `web/src/views/Drivers.vue`（新）——参考预览 `pv-3-drivers`：
   - 轮询 3s `listPresence(role, project)`；表列：状态点（online/stale 按 `last_seen_at` 与 TTL，本地用 30s 阈值近似）· name · **role 徽标**（`supervisor` 特殊色，复用预览 `.badge.sup`）· project_key · client · `agent_id`(短) · last_seen（`fmtDateTime` + 相对）。
   - 过滤：role 下拉（全部/supervisor/其他）+ project；行点击 `router.push('/drivers/'+agent_id)`。
   - 空态："无在线 driver（经 `gofer mcp --server` 注册后出现）"。
2. `web/src/views/DriverInbox.vue`（新，props `id`）——参考预览 `pv-4-inbox`：
   - 顶部 driver 摘要（从 `listPresence` 找该 id，或直接展示 id/role）。
   - `listInbox(id, includeRead)`；`include_read` 开关（默认含已读）。
   - 消息列表：kind 徽标（escalation/assign/notify…）· from_agent · body(可展开) · ref（指向 job/interaction 则 `<a>` 跳转）· created_at。
   - **纯观察**：无 poll/ack/post 按钮。
3. `sup/role 标记（#7）`已在 P1.7 的 Board 行 + JobDetail 头处理（此处 Drivers 页已满足 presence 可见性 #6）。

**验收**：`pnpm build` 通过；眼检 Drivers 列表渲染、supervisor 高亮、点击进 inbox、inbox 只读无写按钮、ref 跳转正确、console 0 报错。（live serve 无在线 driver 时展示空态也算通过。）

**commit**：`feat(web): drivers presence registry + read-only inbox (v3 §6.3)`

---

## P1.7 前端 · Board 补强 + Cluster worker + Agents 说明 + JobDetail rendered-cmd

**目标**：把 WEB-08 真缺项 + worker 透视前端 + 小触点一并落。

**改动**：

1. `web/src/views/Board.vue`
   - **offset 分页**：新增 `offset = ref(0)` + 常量 `PAGE_SIZE = 50`；`fetchJobs()` 传 `{ ...filters, limit: PAGE_SIZE, offset: offset.value }`；底部翻页条：
     - 上一页 `offset>0` 可点 → `offset -= PAGE_SIZE`；下一页 `jobs.length === PAGE_SIZE` 可点（**末页推断**：取到条数 < PAGE_SIZE 即末页，`JobsResp` 无 total）→ `offset += PAGE_SIZE`；页码显 `offset/PAGE_SIZE + 1`。
     - 过滤器变化时 `offset` 归零（加进现有 watch）。
   - **role/channel 行内徽标**：在 `.col-job`（title/id 那格）追加：`job.role` → `.badge.role`（`supervisor` → `.badge.sup`）；`job.channel` → `.badge.chan`（cron/web/mcp/cli）。复用预览徽标类。（时间列已有，不动。）
2. `web/src/views/Cluster.vue`（worker 面板，:380-394 段）：在 labels `.chips` 后补 worker 的 projects/agents（复用 local 块的 `.proj-list` + `.chips` 样式）：
   ```vue
   <div v-if="selectedRunner.worker?.projects?.length" class="chips">
     <span class="chip mono" v-for="p in selectedRunner.worker.projects" :key="p">{{ p }}</span>
   </div>
   <div v-if="selectedRunner.worker?.agents?.length" class="chips">
     <span class="chip mono" v-for="a in selectedRunner.worker.agents" :key="a">{{ a }}</span>
   </div>
   ```
   （数据由 P1.1 后端 + P1.3 类型提供。）
3. `web/src/views/Agents.vue`：`.head` 后插作用域说明段：
   ```vue
   <p class="scope-note mono">以下为 <b>serve 主机</b> 配置的 agents 及其可用性；worker 节点各自的 agents 见 <RouterLink to="/cluster">Cluster</RouterLink>。</p>
   ```
4. `web/src/views/JobDetail.vue`：把渲染命令块**提出 `hasOutcomes` 门控**，独立 `v-if="renderedCommand"`，使 running 态只要 `job.rendered_command` 有值就显示（不再等其它 outcome 内容）。
   - **前置核对**：确认后端在 running（dispatch）时是否已 persist `job.rendered_command`。查 `internal/job/` dispatch/persist 路径：
     - 若 running 已下发 → 纯前端 hoist，完成。
     - 若仅终态下发 → 记为 P1 尾巴小改：dispatch 时 persist rendered_command（或前端 running 态改拉 `/v1/jobs/{id}/request` 展示"原始请求"作为近似）。**先核对再决定**，不盲改。

**验收**：`pnpm build` 通过；眼检：
- Board 翻页前后不重叠、末页下一页禁用、过滤后回第 1 页；行内 role(sup)/channel 徽标正确。
- Cluster 点 worker 节点显示其 projects/agents（有 worker 时）。
- Agents 顶部说明可见、Cluster 链接可跳。
- JobDetail running 态即见 rendered 命令（依核对结果）。

**commit**：`feat(web): board pagination + role/channel badges, cluster worker caps, agents scope note, jobdetail running cmd (WEB-08/§6.6)`

---

## 统一验收 & 部署

1. **后端**（容器）：`go build ./cmd/gofer && go test ./...` 全绿 + `go vet ./...`。
2. **前端**（主机 via gofer job）：`pnpm build`（vue-tsc 类型检查 + vite build）exit 0，新 chunk（Dashboard/Drivers/DriverInbox/EscalationBell）产出。
3. **端到端眼检**：agent-browser 逐屏（dashboard/board/drivers/inbox/jobdetail/cluster），console 0 报错；对照预览。
4. **部署**：主机 `make install`（= make web embed + make build）+ 重启 host serve；或提交 `gofer job` 到主机执行。
5. **回填**：P1 全绿后更新本 plan checkbox（SR1201）+ roadmap（WEB-06/07/08 标 🚧→✅ 只读部分）+ `bd remember` 交接。

## P2 预告（不在本计划）

WEB-09 写层：EscalationBell 下拉/JobDetail 就地 choice/confirm 应答 + punt，`answered_by=caller_id`；前置 = 身份分级（operator token + 可选 `can_answer` 能力位，§11/§13）。另出 `P2-interaction-write-plan.md`。
