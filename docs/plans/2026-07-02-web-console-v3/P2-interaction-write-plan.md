# Web 控制台 v3 · P2 交互写层（WEB-09）实施计划

> 设计：[`../../design/2026-07-02-web-console-v3-design.md`](../../design/2026-07-02-web-console-v3-design.md)（§6.5 / §11 / §13）。P1（只读观察层）已全落地+LIVE，见 [`P1-readonly-plan.md`](P1-readonly-plan.md)。
> 目标：把 P1 的**只读** escalation 通知升级为**可就地应答**，实现 MCP-01「升级人 → Web 应答」面 + 通用交互应答。**写层**，前置=身份分级（§13 已按推荐确认：caller_id 归属 + 独立 operator token + 可选 `can_answer` 能力位）。
> 每子任务完成即 commit（SR1202）。SUPMODE + host codex 实施、容器验收。

## 现状校正（P2 摸底，勿重复造）

- **JobDetail 已有按 job 应答 UI**（P9 既有）：`answerInteraction()` + 待应答/已应答卡片（JobDetail.vue:12/171/718）。**per-job 文本/选项应答已可用**。
- 后端 `POST /v1/jobs/{id}/interactions/{iid}/answer`（`handleAnswerInteraction`→`AnswerInteractionBy(id,iid,answer,responder)`）与 `.../punt`（`handlePuntInteraction`）**端点已存在**。
- **client 有 `answerInteraction`，无 `puntInteraction`**。
- **审计缺口**：`answerInteractionReq.Responder` 是**客户端传入**（原为监督 driver 的 agent_id + 触发派生作答闸 answerguard）。web 人工应答若走 Responder 会①可伪造、②误触 agent 闸。需服务端盖 `answered_by = 认证 caller_id` 且**不触发 agent 派生作答闸**。
- `CallerConfig`（config/model.go:294）有 ID/Token/TokenEnv/配额字段，**加 `CanAnswer bool` 干净**。

因此 P2 真正待做：① 全局 **EscalationBell 内联应答**（免跳详情）② **punt**（client+wire）③ **审计归属**（answered_by=caller_id，不触 agent 闸）④ **可选 can_answer 闸**（operator token）。

## 前置 / 环境

同 P1：Go 容器 `go build/test`；前端主机 `gofer job ... pnpm build`；agent-browser 眼检；上线主机 `make install` + 重启 serve。已 LIVE 的 serve 需重建部署才生效。

## 进度跟踪

- [ ] **P2.1** 后端 · 应答审计归属（answered_by=caller_id，不触 agent 派生作答闸）+ punt 归属
- [ ] **P2.2** 后端 · `can_answer` 能力位 + 可选闸（opt-in，默认放行保持兼容）
- [ ] **P2.3** 前端 · client `puntInteraction` + EscalationBell 内联应答/punt（choice/confirm 就地，question 引导详情）
- [ ] **P2.4** 前端 · JobDetail 加 punt 按钮 + 展示 answered_by
- [ ] **P2.5** 文档 · runbook operator token 配置 + 安全说明（SR201-204 对齐）

---

## P2.1 后端 · 应答审计归属（不触 agent 闸）

**目标**：web 人工应答/ punt 的 `answered_by` 记为**认证 caller_id**（服务端盖，防伪），且**不触发** agent 派生作答闸（answerguard 是给监督 driver 自动答用的）。

**先核对**（codex 必做，决定实现形态）：读 `internal/job/interaction.go:363 AnswerInteractionBy` + `internal/answerguard`，确认派生作答闸的触发条件——是「responder 非空即触发」，还是「responder 命中已注册 agent 才触发」。
- 若**仅命中注册 agent 才触发**：web 传 `responder=caller_id`（非 agent）天然跳过闸，最简——handler 在 `req.Responder==""` 时用 `callerFromCtx(c)` 兜底即可。
- 若**非空即触发**：需新增不走闸的人工路径，如 `AnswerInteractionByHuman(jobID,iid,answer,callerID)`（设 `answered_by=callerID`，跳过 guard），handler 据来源选择。

**改动**（按核对结论择一，倾向最小改动）：
- `handleAnswerInteraction`：当请求未带 responder（web 人工）→ 以 `callerFromCtx(c)` 作 `answered_by`，且不触 agent 闸。保留现有带 responder（MCP/agent）路径不变。
- `handlePuntInteraction`：punt 也记录发起者 caller_id（若 `MarkInteractionNeedsHuman` 有 by 维度则填；无则至少事件/日志留痕）。
- 保持向后兼容：MCP/agent 带 responder 的行为**零变化**。

**测试**：`internal/httpapi` 或 `internal/job` 加断言——web 路径（无 responder，认证 caller=X）应答后 `answered_by==X` 且未触发 guard；agent 路径（带 responder）行为不变。

**验收**：`go build ./... && go test ./internal/job/... ./internal/httpapi/...` 绿；构造带认证 caller 的 answer 请求，`GET .../interactions` 回该 interaction `answered_by=caller_id`。

**commit**：`feat(interaction): stamp answered_by from caller_id on human web answers (P2.1)`

---

## P2.2 后端 · `can_answer` 能力位 + 可选闸

**目标**：可选地把「谁能替 agent 应答/放行」收敛到带能力的 operator token（防只读/CI token 误答）。**opt-in**：默认不开、保持 P1 兼容。

**改动**：
- `internal/config/model.go` `CallerConfig` 加 `CanAnswer bool \`yaml:"can_answer"\``。
- 闸开关：`ServerConfig.Governance` 加 `RequireAnswerCapability bool \`yaml:"require_answer_capability"\``（默认 false）。加载校验：为 true 时至少一个 caller `can_answer:true`，否则 fail-fast 提示（避免锁死无人能答）。
- `handleAnswerInteraction` / `handlePuntInteraction`：当 `RequireAnswerCapability==true` 且当前 caller 无 `can_answer` → `403`（`writeError` "answer not permitted for this caller"）。需由 caller_id 反查 CallerConfig.CanAnswer（config 已有 caller 列表；加一个 `ServerConfig.CallerCanAnswer(callerID) bool` 辅助，仿 `CallerConcurrencyLimit`）。
- 默认（flag=false）：任何认证 caller 可答（= P1 后行为），零破坏。

**测试**：flag=false 放行；flag=true 时 can_answer=false 的 caller 403、can_answer=true 的 caller 放行。

**验收**：`go test` 绿；两种配置手测（ephemeral serve）行为符合。

**commit**：`feat(config): optional can_answer capability gate for interaction answer/punt (P2.2)`

> 备注：这是**可选安全控制**；若本期只要「归属」不要「闸」，P2.2 可延后，P2.1+P2.3+P2.4 已闭合应答闭环。

---

## P2.3 前端 · client punt + EscalationBell 内联应答

**目标**：从全局铃铛下拉就地应答，免跳详情。

**改动**：
- `web/src/api/client.ts` 加：
  ```ts
  export function puntInteraction(id: string, iid: string): Promise<Interaction> {
    return request<Interaction>(
      `/v1/jobs/${encodeURIComponent(id)}/interactions/${encodeURIComponent(iid)}/punt`,
      { method: 'POST' },
    )
  }
  ```
  （`answerInteraction` 已存在；web 应答**不传 responder**，让服务端盖 caller_id。）
- `web/src/components/EscalationBell.vue`（P1 只读 → 加写）：下拉每条按 `type`：
  - `choice`：渲染 `options` 为按钮，点击 `answerInteraction(job_id, id, option.value)`。
  - `confirmation`：确认 / 拒绝 两按钮（值按后端约定，如 "yes"/"no" 或 options）。
  - `question`：**不就地输入**，保留「进 JobDetail 作答」链接（文本输入在详情更合适）。
  - 每条加 **punt** 小按钮（`puntInteraction` → 留人；needs_human 已 punt 的隐藏 punt）。
  - 应答/punt 成功 → 本地移除该条 + 刷新计数；失败（含 **403 无 can_answer**）→ 该条显示错误文案，不崩下拉。
  - 提交中禁用按钮防重复。

**验收**：`pnpm build` 绿；眼检：choice/confirm 就地应答后 job 续跑、该条消失、计数回落；question 跳详情；403 时提示。

**commit**：`feat(web): inline answer/punt from escalation bell (WEB-09, P2.3)`

---

## P2.4 前端 · JobDetail punt + answered_by 展示

**目标**：详情页应答能力补齐（已有 answer，加 punt + 归属展示）。

**改动**：
- `web/src/views/JobDetail.vue`：待应答卡片加 **punt** 按钮（`puntInteraction`）；已应答卡片展示 `answered_by`（P1.3 类型已加该字段）+ `needs_human` 标记。
- 复用现有 `interactionErrors` 错误展示；punt/answer 403 同样优雅提示。

**验收**：`pnpm build` 绿；眼检：详情 punt 生效、已应答显示 answered_by。

**commit**：`feat(web): jobdetail punt button + answered_by display (P2.4)`

---

## P2.5 文档 · operator token + 安全说明

- `docs/runbook/` 加一节：配置**独立 operator caller token**（`callers:` 加一条 `id: web-op, token_env: ..., can_answer: true`）+（可选）`governance.require_answer_capability: true` 开闸；说明 answered_by 审计语义、与 SR201-204 的关系（web 写操作经认证 caller，审计可追溯到人）。
- roadmap WEB-09 标 ✅（P2 完成后）。

**commit**：`docs(runbook): operator token setup for web interaction answering (P2.5)`

---

## 统一验收 & 部署

1. 后端容器 `go build ./... && go test ./...` 全绿。
2. 前端主机 `pnpm build` 绿。
3. agent-browser 眼检写闭环：造一个 `pending_interaction`（如 sup punt 或直接起带交互的 job）→ 铃铛就地应答 → job 续跑 → 计数回落 → answered_by 正确；403 闸（若开）行为正确。
4. 主机 `make install` + 重启 serve 部署。
5. 回填：本 plan checkbox + roadmap WEB-09 ✅ + `bd remember`。

## 安全要点（写层，SR201-204）

- 所有写端点在 authed `/v1` group（bearer→caller_id），**非匿名**。
- answered_by 服务端盖 caller_id（防伪），不采信客户端 responder 做人工归属。
- `can_answer` 闸为纵深防御（opt-in）：把「替 agent 决策/放行」限定到 operator token。
- 不引入 web 会话/用户体系（§13 反面，暂不做）；不做 pty/审批门（各自独立设计）。
