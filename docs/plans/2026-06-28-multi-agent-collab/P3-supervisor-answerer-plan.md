# P3 · L4 监督应答（E25）实施计划 ✅ 完成（2026-06-28）

> 主纲 [`../2026-06-28-multi-agent-collab-plan.md`](../2026-06-28-multi-agent-collab-plan.md) · design §8.3-8.4/§11/D3-D4 · bd `example-project-axma`

> **完成记录**：commits `b8c390c`(P3.1+P3.2)→`e99de63`(P3.3)→`2807654`(P3.4)→`bd338d0`(P3.5)。go test 28 包绿+vet 净+无 import 环。
> - 既有缺口修复(复审#4)：`finish()` 同终态临界区把残留 pending interaction 翻 cancelled→落库→close(answered) 唤醒 WaitAnswer(返回 cancelled 快照,不再悬挂);`InteractionCancelled` 终于生效。启动 `ReconcileOrphanInteractions` 兜底崩溃残留。`ListPendingInteractions` JOIN jobs 排除终态 job 僵尸 pending。
> - supervisor 包(G022 经 JobOps/PresenceOps 接口反向消费,deps 仅 job 链)：decide 收窄(仅 choice+options+AutoAnswer+白名单命中→自动答首个 option;余皆 escalate);escalate 经信箱 role:supervisor(dedup,ref=job:<id>#<iid>);ErrJobTerminal 跳过;MaxRounds 熔断。**诚实**：空 allow_prompt_regex=啥都不自动答(opt-in)。
> - **真机 E2E(容器 serve,supervisor.enabled)全过**：①auto-answer(choice "deploy to staging"+options→自动答 yes) ②escalate(confirmation→留 pending+投 sup-bot inbox,ref 正确) ③reconcile(cancel job→confirmation 翻 cancelled,跨 job pending 清零无僵尸)。
> - WaitAnswer 对 cancelled 返回 (cancelled, nil)(不新增 sentinel,调用方查 Status);config `*SupervisorConfig`(omitempty,nil=关) + writer managedTopKeys 加 supervisor。

> 目标：分层 answerer 作答 job 的 `pending_interaction`——**白名单 choice+options 自动答低危**，confirmation/无options/自由文本 question/高危/超轮次 **升级人**（经 P1 信箱）。补跨 job 列 pending 端点；**顺带修既有缺口**（job 终态不对账 pending interaction）。依赖 **P1**（升级走信箱）。

## 锚点（file:line）

- interaction：`job/interaction.go`(Interaction 43-53 / 常量 56-67 / CreateInteraction 89-161 / AnswerInteraction 248-310 / ErrJobTerminal) · jobstore `interactions.go`(selectInterCols 32-34 / ListInteractions 85-104)
- finish/cancel：`job/execute.go:112-182`(finish，**现不碰 interactions**) · `job/cancel.go:37-64` · entry.interactions（`interaction.go:72-76` interactionRec）
- Service 注入：`job/service.go:85-156`(struct) `168/189`(SetWorkflow/SetMetrics 样板) · meta=`*jobstore.Store`
- 事件/配额：E13 `recordEvent`（execute.go 内调用样板）· E17 配额（job.Service 现有）
- 路由/client/mcp：见 P1 锚点同款

---

## P3.1 jobstore：ListPendingInteractions（JOIN jobs 过滤终态）

**`jobstore/interactions.go` 加方法**（复审 #4：只列**活跃 job** 的 pending）：
```go
// 终态集合（与 job 包 isTerminal 对齐）：done/failed/cancelled/timeout
func (s *Store) ListPendingInteractions() ([]InteractionRecord, error) {
    const q = selectInterCols + ` AS i_unused` // 用显式列；改写为 JOIN
    rows, err := s.db.Query(`SELECT i.id, i.job_id, i.type, i.prompt, COALESCE(i.options_json,''),
        i.status, COALESCE(i.answer,''), i.created_at, COALESCE(i.answered_at,0)
        FROM interactions i JOIN jobs j ON i.job_id = j.id
        WHERE i.status = 'pending'
          AND j.status NOT IN ('done','failed','cancelled','timeout')
        ORDER BY i.created_at ASC`)
    // scan 照 scanInteraction:37-44 → []InteractionRecord
}
```
> 终态字符串以 `job/model.go` 的 Status 常量为准（落地时核对 isTerminal 集合，勿写错）。

- [x] 单测：造 1 个活跃 job(pending) + 1 个终态 job(残留 pending) → 只回前者。
- [x] commit `feat(supervisor): jobstore ListPendingInteractions(JOIN jobs 过滤终态)`

---

## P3.2 job：finish/cancel 对账残留 pending（修既有缺口）

**复审 #4 既有 bug**：job 终态时 `entry.interactions` 里的 pending 永不翻态，SQLite 留僵尸 pending；`InteractionCancelled` 常量从未被赋值。

**`job/execute.go` finish()**（112-182，persist 前插入对账）：
```go
// 终态对账：把残留 pending interaction 翻为 cancelled（防僵尸 pending；唤醒等待者）
entry.mu.Lock()
for _, rec := range entry.interactions {       // entry.interactions: []*interactionRec
    if rec.data.Status == InteractionPending {
        rec.data.Status = InteractionCancelled
        rec.data.AnsweredAt = s.nowFn().Unix()
        // 持久化该 interaction（照 AnswerInteraction 的 UpsertInteraction 落库手法）
        s.persistInteraction(jobID, rec.data)   // 复用现有持久化路径
        if rec.answered != nil { close(rec.answered) }  // 唤醒阻塞的 WaitAnswer（返回 cancelled）
    }
}
entry.mu.Unlock()
```
> 实现时复用 interaction.go 内既有的持久化/关 channel 内部 helper（避免重复逻辑、保 mu 语义一致）；`WaitAnswer`(314-344) 对 cancelled 应返回明确状态（核对其 select 分支，必要时让它识别 cancelled 返回 ErrInteractionCancelled 而非永久阻塞）。

**`job/cancel.go`**：cancel 触发 ctx.Cancel → execute classify 走 finish → 上面对账覆盖（cancel 本身不必单独改，确认链路即可）。

**启动 sweeper 兜底**（复审 #4：进程崩溃残留）：serve 启动时一次性 `UPDATE interactions SET status='cancelled' WHERE status='pending' AND job_id IN (SELECT id FROM jobs WHERE status IN (终态))`（jobstore 加 `ReconcileOrphanInteractions()`），日志条数。

- [x] 单测：job 跑完时有 pending → finish 后该 interaction=cancelled + WaitAnswer 返回（不悬挂）；ListPendingInteractions 不再回它。启动 ReconcileOrphan 清崩溃残留。
- [x] `go test ./internal/job/... ./internal/jobstore/...` 绿 → commit `fix(interaction): job 终态对账残留 pending→cancelled + 启动兜底(既有缺口)`

---

## P3.3 internal/supervisor：分层 answerer

**新包 `internal/supervisor`**（消费 `job.Service`，G022 单向；serve 启动它）：

```go
type Service struct {
    jobs     JobOps          // 接口: ListPendingInteractions/GetInteractions/AnswerInteraction（job.Service 实现）
    presence PresenceOps     // 接口: Post(...)（presence.Service 实现，升级投信箱）
    policy   Policy
    nowFn    func() time.Time
}
// 接口在 supervisor 包定义（依赖倒置，照 job WorkflowAdvancer 模式），job/presence 不 import supervisor

type Policy struct {
    Enabled        bool
    Interval       time.Duration       // poller 周期（默认 5s）
    AutoAnswer     bool                // 总开关：自动答 choice
    EscalateTo     string              // 升级收件: "role:supervisor" | 具体 agent_id（默认升级人）
    MaxRoundsPerJob int                // 超轮次升级（防套娃，默认 3）
    // 白名单：仅 choice + 带 options 才考虑自动答；可选 prompt regex 限定
    AllowPromptRegex []string
}

func (s *Service) Run(ctx context.Context)  // poller 循环：每 Interval 调 tick()
func (s *Service) tick(ctx context.Context) // ListPendingInteractions → 逐条 decide+act
```

**决策逻辑 decide(it Interaction)**（design §8.3，诚实收窄）：
```go
switch {
case it.Type == InteractionConfirmation:           return escalate   // E8：审批门一律升级
case it.Type == InteractionTypeQuestion:           return escalate   // 自由文本不自动编答
case it.Type == InteractionTypeChoice && len(it.Options)==0: return escalate  // 无 options 无法枚举
case roundCount[it.JobID] >= MaxRoundsPerJob:       return escalate   // 超轮次
case !matchWhitelist(it):                           return escalate
default: /* choice + 有 options + 命中白名单 */       return autoAnswer // 选默认/指定 option.value
}
```
- **autoAnswer**：`jobs.AnswerInteraction(it.JobID, it.ID, chosen.Value)`；遇 `ErrJobTerminal`(僵尸) **静默跳过**（复审 #4）；成功记 E13 审计 `answered_by=auto:<policy>` + 计 E17 配额。
- **escalate**：`presence.Post(from="system", to=EscalateTo, kind="escalation", body=prompt, ref="job:<id>#<iid>")` → driver poll inbox 取到 → 人/agent `bridge_answer_interaction` 作答。**避免重复升级**：同一 interaction 只投一次（记已升级 set，或 message ref 去重）。
- **去重/幂等**：tick 每轮可能重复见同一 pending（未答完前），需对"已 autoAnswer 失败"/"已 escalate"做记忆，避免刷屏。

**可选高级（§8.4，不作 MVP 默认，本 P 仅留接口位）**：派生监督 driver agent 作答——即把 escalate 的收件人设为一个常驻 `role:supervisor` 的 driver claude，它读 escalation 后自行 reason+answer。无需新执行模型（复用 P1 信箱 + 既有 answer 工具）；熔断参数（轮次/配额）即上面 Policy，plan 落地钉死默认值。

**审计/配额钩子**：复用 job.Service 既有 E13 `recordEvent`（answer 事件标 `actor=auto|human`）+ E17（supervisor 作答计入 caller 配额）；**人可暂停**：`Policy.Enabled=false`（config 改 + SIGHUP，或运行时开关）。

- [x] 单测（mock JobOps/PresenceOps）：confirmation→escalate；question→escalate；choice 无 options→escalate；choice+options+白名单→autoAnswer 选默认；超轮次→escalate；ErrJobTerminal→跳过；重复 tick 不重复升级。
- [x] `go test ./internal/supervisor/...` 绿、无环 → commit `feat(supervisor): 分层 answerer(白名单自动答+升级人) + poller`

---

## P3.4 httpapi + client + mcp：跨 job 列 pending

**`httpapi`**：`GET /v1/interactions`（buildRouter Group `/v1` 内）：
```go
r.GET("/interactions", s.handleListPendingInteractions)  // ?status=pending（MVP 仅支持 pending）
```
handler：`status := c.Query("status")`（非 pending→400 或仅支持 pending）→ `s.jobs.ListPendingInteractions()` → `map[string]any{"interactions":list}`（每条含 job_id）。

**`client`**：`func (c *Client) ListPendingInteractions() ([]job.Interaction, error)`（GET `/v1/interactions?status=pending`，照 GetInteractions 466-479）。

**`mcpserver`**：Backend 加 `ListPendingInteractions() ([]interactionView, error)`（两实现：local 调 job.Service、client 调 cli）+ 工具 `bridge_list_pending_interactions`（给可选监督 driver agent 用，design §8.4）：
```go
mcp.AddTool(s, &mcp.Tool{Name:"bridge_list_pending_interactions", Description:"List pending interactions across active jobs (for a supervisor agent to discover questions awaiting an answer)."}, listPendingHandler(b))
```

- [x] 单测：端点回活跃 pending；client/backend 往返。`go test ./...` 绿。
- [x] commit `feat(supervisor): GET /v1/interactions?status=pending + client + mcp bridge_list_pending_interactions`

---

## P3.5 config + serve 线缆 + E2E

**`config/model.go`** 加 `Supervisor SupervisorConfig`（enable/interval/escalate_to/max_rounds/auto_answer/allow_prompt_regex）；ApplyDefaults 给默认（Enabled=false 默认关，保守）。

**serve 线缆**：serve 启动时若 `cfg.Supervisor.Enabled` → 构造 `supervisor.Service`(jobs=core.Jobs, presence=core.Presence, policy=from cfg) → `go sup.Run(ctx)`；优雅停机随 serve ctx 取消。

- [x] **部署**：`go build`；容器换装；host 自建。
- [x] **E2E（分层 answerer 语义）**：
  1. config 开 `supervisor.enabled=true, auto_answer=true, escalate_to="role:supervisor"`；起 serve。
  2. **自动答路径**：派一个 job（exec 或 agent）运行中创建 `choice` interaction（带 options，命中白名单）→ 观察 supervisor 自动答（选默认 option）→ job 续跑 → E13 事件标 auto。
  3. **升级路径**：job 创建 `confirmation`（或自由文本 question）→ supervisor 不自动答 → 一个注册了 `role:supervisor` 的 driver(claude#S) `bridge_poll_inbox` 取到 escalation(ref=job#iid) → `bridge_answer_interaction` 作答 → job 续跑。
  4. **僵尸/对账**：job 带 pending 时被 cancel → interaction 翻 cancelled、不再出现在 list pending。
  5. **接管**：config `supervisor.enabled=false` + SIGHUP → poller 停、pending 全留人工。
- [x] 回填主纲 + commit `feat(supervisor): config 段 + serve 线缆 + E2E`。bd `axma` close。

## 验收总清单（P3 Done 标准）

- 跨 job pending **只列活跃 job**；僵尸 pending 被对账（既有缺口已修，`InteractionCancelled` 生效）。
- 分层 answerer：choice+options+白名单→自动答；confirmation/question/无options/超轮次→升级信箱；ErrJobTerminal 跳过；不重复升级。
- E8 以"拒答 confirmation"落地；审计标 AI/人；配额计入；`enabled=false` 可一键接管。
- `go test ./...` 全绿、无环；E2E 自动答 + 升级两路径 PASS。
