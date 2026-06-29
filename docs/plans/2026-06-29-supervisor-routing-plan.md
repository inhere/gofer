# Gofer 监督应答 · 分层升级路由 实施计划

> 配套设计：`docs/design/2026-06-29-supervisor-routing-design.md`（L0→L1→L2→L3 分层升级路由）。
> 现状基线（实测）：基础设施约 70% 就绪——escalation→inbox 投递、`gofer_answer_interaction`/
> `gofer_list_pending_interactions`/`gofer_register`/`gofer_poll_inbox`、`max_rounds_per_job` 熔断、
> 内置规则 answerer(L0)、escalate 投递(L3) 均已实现。本计划补 **L1 owner-first 路由 + 三道安全闸 + L2 server 托管**。

## 总纲 / 阶段划分

| 阶段 | 主题 | 性质 | 依赖 |
|---|---|---|---|
| **P0** | mcp 工具前缀 `bridge_*` → `gofer_*` | 横切改名(独立) | — |
| **P1** | owner-first 路由地基（origin_agent 透传 + escalate 改写） | MVP 核心 | — |
| **P2** | owner 超时兜底 + 套娃防护 | MVP 核心 | P1 |
| **P3** | 派生作答白名单约束 + 审计区分 | MVP 安全闸 | P1 |
| **P4** | 通用 sup agent server 托管 | P4a=MVP(文档) / P4b=完整(reconciler) | P1-P3 |
| **P5** | 最小只读 inbox 端点（可选，调试可见） | 增强 | P1 |

**MVP 收口** = P0 + P1 + P2 + P3 + P4a；`escalated_at` 落表并入 P1/P2（折中档，疑问2 定）。P4b(reconciler)、`rounds` 落表为完整版/后续。
每阶段独立可验、自带测试，完成即 commit（SR1202）。

---

## P0 mcp 工具前缀 bridge_* → gofer_*（横切改名，独立先行/独立提交）

**目标**：15 个 mcp 工具统一前缀 `bridge_` → `gofer_`，与项目名对齐。横切、与监督逻辑无关，**独立 commit**。

- 锚点 `internal/mcpserver/server.go:42-121`：逐个 `mcp.AddTool` 的 `Name` 改前缀（纯前缀替换，15 个）：`list_projects` / `list_agents` / `run_job` / `get_job` / `tail_log` / `cancel_job` / `get_interactions` / `answer_interaction` / `get_artifacts` / `get_result` / `register` / `poll_inbox` / `post_message` / `list_presence` / `list_pending_interactions`。
- **兼容策略**：gofer 是内部工具（G031）、agent 动态发现工具名（不硬编码）→ **直接改名、不做双注册**（YAGNI）；若顾虑正在跑的会话，可选临时双注册旧名标 deprecated、一版后删（默认不做）。
- **同步全仓引用**（`grep -rn 'bridge_' internal/ docs/`）：测试 `mcpserver/server_test.go`；文档 `architecture-overview.md`（§3/§4 "8 个 bridge_* tool" 顺带订正为 15 个 `gofer_*`）、`2026-06-28-multi-agent-collab-design.md`、本 design/plan、`docs/runbook/*`；P4a sup prompt 模板。
- **验收**：mcp list tools 全 `gofer_*`；`grep -rn 'bridge_' internal/` 仅余无关词；`go test ./...` 绿。

> 本 plan 后续阶段（P1+）及验收命令中工具名以 `gofer_*` 为准；个别处若仍现 `bridge_*` 旧名，按前缀映射替换。

---

## P1 owner-first 路由地基

**目标**：job 记录"发起它的主 agent"，escalation 由一刀切改为 `owner → job覆盖 → 全局sup` 有序路由。

### P1.0 mcp 进程级自注册 + 自动注入 origin_agent（实测定论，免显式传参）

> 实测：gofer mcp = **stdio per-进程**（`internal/mcpserver/server.go:129` `Serve` → `Run(ctx, &mcp.StdioTransport{})`），每个 agent 会话拉起独立 mcp 子进程；MCP handler 拿不到连接身份（go-sdk session 钩子 handler 层不可用），故不靠 SDK session，改 **mcp 进程级自注册**。

- 锚点 `internal/commands/mcp.go`（Serve 入口）/ `internal/mcpserver`：进程启动时（或首次 run_job 前 lazy）自动 `b.RegisterAgent(name=<唯一>, role)` 拿 agent_id，缓存进程内存（backend 字段，仿 `mcpHostname()` 单例）。
- `runJobHandler`（`server.go:282`）注入 `OriginAgent = 缓存 agent_id`（仿现 `Channel="mcp"`/`Client`）；**显式入参 `origin_agent` 优先**（覆盖自注册值）。
- **name 取 `mcp-<hostHash>-<pid>`**：`hostHash` = hostname 经简单 hash（crc32/fnv 取 hex 前 6–8 位），避免长/含特殊字符/有歧义的 hostname 直接入名干扰；`pid` 保证同机多会话不幂等到同一 agent_id（presence.Register 幂等键 `(name, caller_id)`，`presence/service.go:210`；串身份则 escalation 投错会话）。常驻 supervisor 可经 flag 指定**固定 name** 复用身份。
- 进程退出 deregister（stdio onClose）或靠 presence 90s TTL 自然过期。
- 非 MCP 入口（CLI/web/HTTP）派的 job 无 driver 身份 → origin_agent 空 → 其 interaction 直接 L0→L2→L3（无 owner），符合设计。

**验收**：不传 origin_agent 直接 `gofer_run_job` → `gofer_get_job` 返回的 origin_agent = 本会话 mcp agent_id；两个独立 mcp 进程派的 job origin_agent 不同（不串）。

### P1.1 jobs 表 + JobRequest 加 origin_agent / escalate_to

- 锚点 `internal/jobstore/store.go:60`（jobs DDL，与 channel/client 同区）：
  ```sql
  origin_agent TEXT,   -- 发起该 job 的主 agent agent_id（owner，L1 路由）
  escalate_to  TEXT,   -- 可选 job 级 escalate 覆盖
  ```
  > 新库走 `CREATE TABLE`；旧库需 `ALTER TABLE jobs ADD COLUMN`（确认 store 是否有迁移钩子，无则补 idempotent ALTER）。
- 锚点 `internal/job/*.go` `JobRequest`：加 `OriginAgent string` / `EscalateTo string`，落库 insert/scan 同步（grep 现有 `Channel`/`Client` 落库点逐一对齐）。
- 锚点 `internal/mcpserver/server.go:267` `runJobInput`：加 `OriginAgent string json:"origin_agent,omitempty"` / `EscalateTo string json:"escalate_to,omitempty"`，`runJobHandler`(283) 透传（仿 Channel/Client 注入位置）。
- **顺带给 `interactions` 加列 `escalated_at INTEGER`**（dedup 标记 + 超时计时共用，供 P1.2 落表 dedup / P2.1 超时判定；提前到此一并加列免返工）。

**验收**：`gofer_run_job(origin_agent="agt_x")` 后 `gofer_get_job` 能取回 origin_agent；DB 行有值。

### P1.2 escalate 改写为 owner-first（核心）

- 锚点 `internal/supervisor/service.go:228` `escalate`：现 `s.presence.Post(systemFrom, s.policy.EscalateTo, ...)` 改为按序解析候选并逐个投递，首个 `delivered>0` 即停：
  ```
  job := s.jobs.Get(it.JobID)                    // 需 JobOps 暴露 Get（确认接口已有/补）
  var targets []string
  if job.OriginAgent != "" { targets = append(targets, job.OriginAgent) }  // L1 裸 agent_id 直投(store-and-forward,无 agent: 前缀)
  if job.EscalateTo != ""  { targets = append(targets, job.EscalateTo) }            // job 级覆盖
  targets = append(targets, s.policy.EscalateTo)                                    // L2 全局 sup
  for _, to := range targets {
      if n, _ := s.presence.Post(systemFrom, to, escalateKind, it.Prompt, ref); n > 0 { delivered=true; break }
  }
  ```
- `supervisor.JobOps` 接口若无 `Get(jobID)` 需补（G022：supervisor 经接口取 job，不反向 import）。
- 默认值：`config/model.go` + `serve.go:329` 维持读 `sc.EscalateTo`；**config 默认值改 `role-one:supervisor`**（取一，防多 sup 重复抢答；锚点 `supervisor.DefaultEscalateTo`，service.go:98 现为 `role:supervisor`）。

**验收**：
- owner 在线 → escalation 落 owner inbox（`gofer_poll_inbox` 取到 `kind=escalation`、`ref=job:..`）。
- owner 离线、有在线 sup → 落 sup（role-one 取一，多 sup 只 1 个收到）。
- owner 离线、无 sup → `delivered=0`，interaction 留 pending（L3 由人捞）。
- **dedup 落表**：escalate 时写 `interactions.escalated_at`（替代内存 `escalated` map），避免多 tick / 跨重启重复投（接 P2.1）。
- 单测：`TestEscalateOwnerFirst`（owner 在线优先 / 离线 fallback / 都无回 0）。

---

## P2 owner 超时兜底 + 套娃防护

**目标**：owner 在线但不答（会话式 agent 已结束）时自动降级；通用 sup 不自我死循环。

### P2.1 owner 超时 → 跳 L1 投 L2

- `SupervisorConfig` 加 `OwnerAnswerTimeoutSec int`（默认 300，`config/model.go:54`；`Policy` + `serve.go:325` 透传）。
- **超时计时读落表的 `escalated_at`（折中档，疑问2 定）**：用 P1.1 加的 `interactions.escalated_at` 列（P1.2 escalate 已落）。`ListPendingInteractions` 已 JOIN jobs，**扩返回 `escalated_at` 即可、不加新查询**；`tick` 对仍 pending 的 interaction：
  ```
  if escalated_at>0 && now-escalated_at > policy.OwnerAnswerTimeoutSec {
      // 跳过 L1：escalate 时 targets 去掉 owner 直投，只投 job.EscalateTo + policy.EscalateTo
  }
  ```
  > 收益：跨 serve 重启不重复投 escalation、owner 超时窗口连续；副带 interaction 升级历史可查（喂 P5/web）。`rounds`(jobID 计数) 仍留内存（重置仅多升几轮、偏安全，留完整版）。

**验收**：owner 注册但不答，>timeout 后 escalation 改投通用 sup；**serve 重启后超时计时不重置**（escalated_at 来自 DB）；单测 `TestOwnerTimeoutFallback`（mock 时钟）。

### P2.2 套娃防护（通用 sup non-interactive）

- 路由器识别"来源 job 自身是 supervisor"的 interaction → **永不自动答/不回投 sup**，直接保留 pending 等人（L3）。判定来源：job.Role=="supervisor" 或 origin 为 sup agent。
- 文档：通用 sup job 必须以 non-interactive 起（codex `approval_policy=never` / 只读），写入 P4a 启动模板。

**验收**：构造一个 role=supervisor 的 job 产生 interaction → 不进自动答、不投 supervisor inbox；单测 `TestSupervisorJobNeverAutoAnswered`。

---

## P3 派生作答白名单约束 + 审计区分

**目标**：堵"`gofer_answer_interaction` 不过白名单"的安全缺口；留痕谁答的。

### P3.1 L2 派生作答过白名单

- 现状：`gofer_answer_interaction`→`b.AnswerInteraction` 直接落答，无校验。
- 改造：在 answer 链路加**来源分级校验**——
  - owner（L1，answered_by=agent:owner）应答：放行（等同人，持上下文）。
  - 通用 sup（L2，role=supervisor 的 driver）应答：过 `allow_prompt_regex` + type 限定（仅 choice+options）+ 角色许可；不合规 → 拒绝 answer、强制保留 pending 升级人。
  - 区分来源：answer 请求需带/可推出"应答者 agent_id + role"。确认 `AnswerInteraction` 签名是否能拿到 caller 身份；不能则扩参（answered_by）。

**验收**：sup 尝试答一个 confirmation/不在白名单的 choice → 被拒、interaction 仍 pending；owner 答同一个 → 成功。单测 `TestDerivedAnswerWhitelistGate`。

### P3.2 审计 answered_by

- interaction answer 记 `answered_by`：`auto:<policy>`（L0）/ `agent:<id>`（L1 owner / L2 sup）/ `human`（L3）。
- 落 `interactions` 表或 job_events（E13）；`gofer_get_interactions` 返回可见。

**验收**：四类来源应答后 `answered_by` 各异、审计可查；单测断言取值。

---

## P4 通用 sup agent server 托管

### P4a（MVP，文档化 daemon job）

- 写 `docs/runbook/` 一节：通用 sup 启动方式 + prompt 模板：
  ```
  gofer job run -p <proj> -a codex --role supervisor --runner <node> \
    --timeout 0  \
    -- <prompt: 你是 supervisor，循环 gofer_poll_inbox，对 kind=escalation 的消息做
        通用低危判断后 gofer_answer_interaction；拿不准/高危的不要猜，留给人。non-interactive>
  ```
- 确认 `--timeout 0`（或等价"长生命周期"）现有支持；不支持则记入 §依赖、P4b 解决。
- config `roles.supervisor`（system_prompt + 默认 non-interactive）样例（沿 E35）。

**验收**：手动起一个 sup daemon job → 制造一个 owner 离线的 escalation → sup poll 取到 → answer → 原 job 继续。**端到端真机过**（host serve）。

### P4b（完整版，serve reconciler — 你要的"server 启动·任意节点"）

- `SupervisorConfig` 加 `DesiredSupervisors int` + 目标 runner/agent。
- serve 内新增 sup reconciler loop（仿 `startSupervisorLoop`，`serve.go:320`）：周期核对在线 role=supervisor driver 数 < desired → 经 `cr.Jobs.Submit` 自构造 sup job 投 dispatch；job 退出/sup 掉线 → 重派（desired=1 类比 Deployment）。
- 依赖 §11 待确认：job 长生命周期形态。

**验收**：config `desired_supervisors=1` → serve 启动后自动在指定节点拉起 sup job；kill 之 → 自动重派。

---

## P5 最小只读 inbox 端点（可选）

- 新增 `GET /v1/agents/{id}/inbox`（只读、不消费、不刷心跳；区别于现 POST `/inbox/poll`）：列出该 agent 未读/全部消息，供调试观察 escalation 堆积。
- 锚点 `internal/httpapi/presence_handler.go` + presence service 加 `ListInbox(readonly)`。

**验收**：`curl GET /v1/agents/<id>/inbox` 返回消息列表、不改 read 态、不刷 last_seen。

---

## 跨阶段依赖 / 风险待确认

- ~~owner agent_id 透传~~ **已实测定论**（P1.0）：mcp = stdio per-进程，handler 拿不到连接身份 → 用 **mcp 进程级自注册 + handler 自动注入 origin_agent**（显式入参覆盖），主 agent 零感知。注意 name per-进程唯一防串身份。
- **job 长生命周期**（P4b）：现有 job timeout 语义 vs 常驻 sup —— P4a 用 `--timeout 0` 探，P4b reconciler 周期重派兜底。
- **状态持久化**：`escalated_at` 已落表（P1.1 加列 / P1.2 写 / P2.1 读，折中档）→ 跨重启 dedup + 超时连续；仅 `rounds`(jobID 计数) 留内存（重启重置=多升几轮、偏安全，留完整版落表）。

## 进度跟进

- [ ] P0 mcp 工具 `bridge_*` → `gofer_*`（15 个 + 全仓引用 + 文档订正）
- [ ] P1.0 mcp 进程自注册 + 自动注入 origin_agent（name=`mcp-<hostHash>-<pid>`）
- [x] P1.1 jobs/JobRequest/runJobInput 加 origin_agent/escalate_to + interactions.escalated_at
- [x] P1.2 escalate owner-first 路由 + 默认 role-one:supervisor + dedup 落表
- [ ] P2.1 owner 超时兜底
- [ ] P2.2 套娃防护（sup non-interactive）
- [ ] P3.1 派生作答白名单约束
- [ ] P3.2 审计 answered_by
- [ ] P4a 通用 sup daemon job 文档 + 端到端真机过
- [ ] P4b serve sup reconciler（完整版/后续）
- [ ] P5 只读 inbox 端点（可选）

> P1 前置「owner agent_id 透传」已定论（P1.0 自注册）；P0/P1 可直接开工。
