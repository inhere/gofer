# 通用 sup 事件驱动按需派发 实施计划

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-30 | inhere | 初稿：push + 持久 demand 轮询兜底 + active-sup 闸 + ack-on-answer |

- 关联 issue: `hyy-ai-inspect-y5wt`
- 关联 design: [`docs/design/2026-06-29-supervisor-routing-design.md`](../design/2026-06-29-supervisor-routing-design.md) §8.3-P2 / §11（"常驻 job 长生命周期形态"待确认 → 本计划以**事件驱动**解，不再需要"常驻"）
- 关联 runbook: [`docs/runbook/2026-06-29-supervisor-daemon-runbook.md`](../runbook/2026-06-29-supervisor-daemon-runbook.md) §10（claude-sup 现行）

## 1. 背景与目标

**浪费根因**：P4b reconciler（`serve.startSupReconcileLoop`）每 60s **无条件保活** `desired_supervisors=1` 个 sup job。但 claude/codex agent **不是常驻进程**——尽管 prompt 写"持续循环 poll"，模型 poll 几次空 inbox 判断没事干，**这一 turn 秒级结束、进程退出**。于是 reconciler 在 ≤60s 内发现 `active=0` 又补派 → **即使零 escalation 也每 ~60s 烧一次 claude**。记忆里的"1h"是 job 的 timeout cap（`supReconcileJobTimeoutDefault=3600`），**不是实际运行时长**。

**目标**：改成事件驱动按需派发——
- 空闲（无 escalation）→ **零 claude 调用**；
- 来 escalation → 唤醒 **1 个** sup，清空 inbox（低危答 / 高危拒答）后退出，不再补派；
- "常驻"不是目标（模型 agent 强行 while-true 只会烧更多 token）；目标是**按需 + 可靠**。

## 2. 方案总览（已与用户对齐）

```
                       ┌───────────────────────────────────────────────┐
router.tick (现有,便宜) │ for it in pending:                            │
  每 interval 一直在跑   │   decide → 白名单 in-process 答 / 否则 escalate │
                       │   escalate 投 role-one:supervisor(只投在线)     │
                       │   若 delivered==0 (无在线 sup) ──push──┐        │
                       └───────────────────────────────────────┼────────┘
                                                                ▼
   ┌───────────────── reconciler (改造: 纯消费者 + 闸) ─────────────────┐
   │ wake(push)  OR  每 backstop_interval 轮询 durable demand:          │
   │   demand = CountSupPendingDemand()   # interactions 表持久信号      │
   │   if demand>0 AND activeSupJobs==0:  Submit 1 sup job              │
   │ 空闲: demand=0 → 不派 → 0 claude                                   │
   └───────────────────────────────────────────────────────────────────┘
```

**三条腿**（对应用户选择 push + 轮询兜底 + ack-on-answer）：

1. **Push（低延迟）**：router `escalate()` 投 sup 目标 `delivered==0`（无在线 sup）那一刻，非阻塞发 wake 信号给 reconciler；延迟 ≈0。
2. **持久 demand 轮询兜底（可靠）**：reconciler 每 `backstop_interval`（可短，默认 30s，因为只是 DB count）查 `CountSupPendingDemand()`；覆盖 push 丢失 / serve 重启。**信号取自 interactions 表（持久），不耦合 inbox（会被 prune）**。
3. **active-sup 闸**：派之前 `CountActiveJobsByRole("supervisor")==0` 才派，至多 1 个并发 sup（沿用 `desired_supervisors` 作上限）。

**关键不变式 / 为什么安全**：
- 高危拒答天然不重派：高危 interaction 被投到 sup 后 `escalated_at>0`，router 不再 re-push（dedup）；只要它被标 `needs_human`，就从 `CountSupPendingDemand()` 排除 → **不会无限重唤醒**（议程 1 的核心隐患被构造性规避）。
- 低危丢消息修复（ack-on-answer）：sup `poll_inbox(ack=false)` **peek 不 consume**；只有 `gofer_answer_interaction` 成功才把对应 inbox 消息标记 read（按 `ref=job:<id>#<itid>` 关联）。sup 中途死在"已 peek 未答" → interaction 仍 pending 且**未** `needs_human` → 仍在 demand 里 → 下次被重新唤醒处理（答复幂等：`ErrJobTerminal` 守卫）。

## 3. 改动点（concrete）

> 文件均在 `tools/gofer/`。每个子阶段独立 commit（SR1202）。

### C1 数据层：demand 查询 + needs_human 标记
`internal/jobstore/interactions.go` + `store.go`：
- `store.go`：interactions 表加列 `needs_human INTEGER`（ALTER ADD，旧行 COALESCE→0），仿 `escalated_at` 迁移写法（`store.go:441` 附近）。
- 新增 `CountSupPendingDemand() (int, error)`：
  ```sql
  SELECT COUNT(*) FROM interactions i JOIN jobs j ON j.id=i.job_id
   WHERE i.state='pending' AND COALESCE(i.needs_human,0)=0
     AND COALESCE(j.role,'') <> 'supervisor'   -- 套娃防护: sup 自身 interaction 不算 demand
  ```
  > sup-bound 的精确判定（owner 在线窗口内的暂不算）较重；**首版按上式粗口径**（owner-pending 也计入），代价仅"偶尔多醒一次 sup 发现 owner 已答→空转退出"，有界且罕见。精确化留 §6 子决策。
- 新增 `MarkInteractionNeedsHuman(jobID, itID string) error`：targeted UPDATE，仿 `MarkInteractionEscalated`（`interactions.go:110`）。

### C2 reconciler：无条件保活 → demand 闸 + wake 消费
`internal/serve/serve.go`（`startSupReconcileLoop` / `reconcileSupervisors` ~L372-449）：
- `reconcileSupervisors` 决策从「`active<desired` 无条件补满」改为：
  ```go
  // demand-gated: 仅当有待处理 sup demand 且当前无活跃 sup 才派 1 个
  if active, _ := countActive(); active > 0 { return }      // active-sup 闸
  if demand, _ := countDemand(); demand <= 0 { return }     // 空闲零成本
  submit()                                                  // 至多派 1
  ```
- loop 增加 wake channel：`select { case <-stop / <-ticker.C / <-wake }` 三路；`wake` 来自 C3 router。`ticker` 周期改用 `backstop_interval`（兜底，可短）。
- 纯函数 `reconcileSupervisors(desired, countActive, countDemand, submit, logf, errf)` 保持可单测（沿用 `reconcile_test.go` 风格）。

### C3 router→reconciler push
`internal/supervisor/service.go` + `serve.go`：
- `supervisor.Service` 加一个可选 `wake func()`（注入；nil 安全）。`escalate()` 在 sup 目标候选 `delivered==0`（即"想投通用 sup 但无在线 sup"）时调 `s.wake()`（非阻塞）。
- `serve.go` 用 `make(chan struct{}, 1)` 连接 `startSupervisorLoop`（生产 wake）与 `startSupReconcileLoop`（消费 wake）；`wake=func(){ select{ case ch<-struct{}{}: default: } }`（满即丢，reconciler 自身闸幂等）。

### C4 ack-on-answer + 高危拒答闭环
`internal/mcpserver/server.go`（+ backend）：
- `gofer_answer_interaction` 成功后，按 `ref="job:"+jobID+"#"+itID` 把 sup inbox 里匹配的 escalation 消息标记 read（新增 backend 能力 `MarkMessageReadByRef` 或复用现有 MarkRead）。
- 新增 `gofer_punt_interaction(job_id, interaction_id)`：sup 对高危/拿不准的显式"留给人"——调 `MarkInteractionNeedsHuman` + 标记对应 inbox 消息 read。**不** answer（interaction 仍 pending 待人）。
- sup mission prompt（`serve.go:defaultSupReconcilePrompt` + roles.supervisor.system_prompt）改为：`poll_inbox(ack=false)` → 逐条：能答→`gofer_answer_interaction`；高危/拿不准→`gofer_punt_interaction`；inbox 空→退出。

### C5 config 语义
`internal/config/model.go` + loader：
- `desired_supervisors` 语义注释更新为「**并发 sup 上限**（事件驱动下默认 1）」，>0 启用事件驱动 reconciler。
- 复用 `reconcile_interval_sec` 作 `backstop_interval`（注释更新：现在是 demand 轮询兜底，非保活节拍；可调小如 30s）。
- 不新增破坏性字段；live config（`D:/work/inhere/config/win-env/gofer/config.yaml`）保持 `desired_supervisors=1` 即自动获得新行为。

## 4. 验收标准

- **空闲零成本**：serve 启用、无 pending interaction、放置 ≥3 个 backstop 周期 → **零** sup job 被派（`gofer job list -role supervisor` 为空 / serve 日志无 dispatch）。
- **按需唤醒**：制造一个 escalate-to-sup 的 pending interaction → ≤ push 延迟（~秒级）内派出 **恰好 1 个** sup job；sup 答复后 interaction 变 answered；sup job 自然终态；**不再补派**。
- **高危不重派**：制造高危 interaction → sup `punt` 后标 `needs_human`；后续 ≥3 周期 **不重复**派 sup；interaction 留 pending 待人。
- **低危不丢**：sup peek 后强杀（模拟中途死）→ interaction 仍 pending（未 needs_human）→ 下个周期重新派 sup 并答复成功。
- **并发上限**：连续多次 escalation → 任意时刻 active sup job ≤1。
- `go test ./...` 全绿；新增 `CountSupPendingDemand` / `reconcileSupervisors`(demand 闸) / wake 路由 单测。

## 5. 测试与联调

- **单测**：jobstore demand count + needs_human；serve `reconcileSupervisors` 纯函数（demand×active 矩阵）；supervisor escalate 触发 wake。
- **E2E（容器 ephemeral serve，不碰 live）**：复刻 runbook §10 起一个临时 serve（独立 `storage.db_path` 便于清理），脚本验上面 5 条验收。**HOST 真机**（`/d/env/gopath/bin/gofer.exe`）经 `gofer job run -p hyy-ai-inspect -a exec --runner local --sync` 重跑空闲零成本 + 按需唤醒两条（Windows 路径 / path_view:host）。
- live 切换：改动合入后，live serve 重启即生效（Windows 不支持 `serve -d`，需用户终端重启）。

## 6. 待确认子决策（实施中遇到再定）

1. **demand 口径精确度**：首版粗口径（owner-pending 也计入 demand，可能偶发多醒一次空转 sup）。是否首版就做精确（仅 owner 不在线 / owner 超时的才计 sup demand）？倾向**先粗后精**（粗口径有界且安全）。
2. **needs_human 落地形态**：新增列 vs 复用现有状态。倾向**新增 `needs_human` 列**（语义清晰、不污染 escalated_at 时钟）。
3. **backstop 是否保留**：push 已覆盖绝大多数；backstop 轮询是否仅作"重启后首次"一次性扫描而非常驻周期？倾向**保留低频常驻**（30s，纯 DB count 极廉价，换重启/丢 push 的鲁棒性）。
4. **runbook 收敛**：§1-9 codex sup 旧法是否在本次一并标注"已被事件驱动取代"。倾向本计划完成后单独一个 doc commit 收敛。

## 7. 进度跟踪

- [x] C1 数据层：`needs_human` 列 + `CountSupPendingDemand` + `MarkInteractionNeedsHuman` + 单测 — `6cae6ab`
- [x] C2 reconciler：demand 闸 + wake 三路 select + 纯函数单测 — `97dec61`
- [x] C3 router push：`Service.wake` 注入 + escalate 触发 + serve channel 连接 + 单测 — `97dec61`
- [x] C4 demand精度 + `gofer_punt_interaction`(全栈) + sup mission(peek+punt) — `6f193c1`
  - 注：mark-read-by-ref 未做（peek+幂等 answer/punt 已正确，列为可选优化）；新增 C4a demand 精度（排除 owner-window 内的 owner-pending，避免常态空转，原 §6.1 粗口径升级为精确口径）
- [ ] C5 config 语义注释 + loader
- [ ] E2E 容器 5 条验收 + HOST 真机 2 条
- [ ] runbook §10 更新 + §1-9 收敛标注
- [ ] live serve 重启切换 + 生产冒烟
