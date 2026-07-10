# P6 — plan 生命周期（完结/归档）+ 派生操作终态门禁（实施计划，代码级）

> 主纲：[plan-orchestration-plan.md](./plan-orchestration-plan.md)
> 设计：[../../design/2026-07-09-plan-orchestration-design.md](../../design/2026-07-09-plan-orchestration-design.md) §（状态流转图 `:106`）
> 上游：P1（`plans` 表 + `SetPlanStatus`）✅ / P4（Plans 前端）✅ / P5（rebuild + 血缘）✅
> 触点均已实测定位（2026-07-10 只读探查）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-10 | inhere/claude | 初稿：两个用户实测发现的缺口（plan 无法完结 / running job 仍显示派生按钮），决策已拍板 |

## 一句话

补两个 P1~P5 遗留的缺口：① **plan 无法完结** —— `jobstore.SetPlanStatus` 是**死代码**（全仓零调用方），HTTP/CLI/MCP/UI 都没暴露状态变更，plan 建后永远停在 `open`；② **派生操作无终态门禁** —— `ResumeJob` / `RebuildJob` 都不校验源 job 状态，对 `running` job 也放行，前端两个按钮亦无 gate。

## 背景（两个缺口怎么被发现的）

用户重建 host server 验收 P5 时提出：「之前的 plan 如何完结？」「运行中的 job，不应该显示『快速重建』和『继续会话』」。探查后确认二者都是真实缺口，且第二个的危害不止 UX。

**`resume` 一个 running job 是数据损坏风险**：续投 job 复用**同一个 `session_id`**（`resume.go:84` `SessionID: src.SessionID`）去跑 `claude --resume <sid>` / `codex exec resume <sid>`，而源 job 仍在跑、仍持有该会话 → 两个进程并发操作同一份 agent 会话状态。前端藏按钮不解决问题，`gofer job resume <running-id>` 照样触发。

**`rebuild` 一个 running job 无技术危害**（新 job 独立、fresh session），但语义可疑。

## 已确认关键事实（探查结论）

| 事实 | 位置（实测 file:line） | 对 P6 的影响 |
|---|---|---|
| `jobstore.SetPlanStatus(id, status, progress)` 已存在，但**全仓零调用方**（死代码） | `internal/jobstore/plans.go:110` | T1 只需在 HTTP 层接线，jobstore 不动 |
| `SetPlanStatus` 用 `UPDATE ... WHERE plan_id=?`，**不检查 affected rows** → 对不存在的 plan 也返回 nil | `plans.go:110-126` | **handler 必须先 `GetPlan` 判存在**（照 `handleAttachPlanJob` 的模式），否则 PATCH 不存在的 plan 会假成功 |
| `SetPlanStatus` **不校验 status 合法值** | 同上 | handler 侧白名单校验 `open/active/done/archived` |
| `progress < 0` 表示「保持原值」 | `plans.go:112-113` | API 用 `*int` 区分「未提供」与「显式设 0」；未提供 → 传 `-1` |
| plan 状态常量齐全 | `plans.go:12-15`（`PlanOpen/PlanActive/PlanDone/PlanArchived`） | 直接复用 |
| plan HTTP 路由只有 create/list/get/attach-job/add-todo，**无状态变更端点** | `internal/httpapi/server.go:433-437` | T1 新增 `PATCH /v1/plans/{id}` |
| plan CLI 子命令只有 `create/list/show/attach/add-todo/set-todo` | `internal/commands/plan.go`（`gofer plan` help 实测） | T3 新增 `set-status` + `archive` |
| `PlanDetail.vue` 只有 `pill--archived` 样式，**无归档/完结动作** | `web/src/views/PlanDetail.vue:136,656` | T5 补状态动作区 |
| **`job.IsTerminal(status)` 已导出**，判 `done/failed/cancelled/timeout` | `internal/job/cancel.go:157-169` | T2/T4 直接复用，勿另写 |
| 已有 `ErrJobTerminal`（语义**相反**：job 已终态、不能再交互） | `internal/job/interaction.go:24` | 新 sentinel 命名为 `ErrJobNotTerminal`，勿混淆 |
| `ResumeJob` 只校验 unknown / `SessionID==""` / agent 支持 / 同 runner，**不看源状态** | `internal/job/resume.go:41-58` | T2 插入终态校验 |
| `RebuildJob` 只校验 unknown / `RequestJSON==""`，**不看源状态** | `internal/job/rebuild.go:110-115` | T2 插入终态校验 |
| resume/rebuild 的 handler sentinel 映射位置 | `internal/httpapi/job_handler.go:333-336`（rebuild）、`:348-352`（resume） | T2 各加一条 `ErrJobNotTerminal` → 400 |
| 「快速重建」`v-if="job"`、「继续会话」`v-if="job.session_id"`，**均无终态 gate** | `web/src/views/JobDetail.vue:973`、`:1046` | T6 加 gate |
| `JobDetail.vue` **已有** `isTerminal()` + `isTerminalView` computed | `web/src/views/JobDetail.vue:254-256,271` | T6 直接复用，勿另写 |
| `TestJobRerunCallsRebuildEndpoint` 用 httptest mock，不涉真实 job 状态 | `internal/commands/job_test.go:266` | 该测试**不会**因 gate 而挂 |

## 已拍板决策（2026-07-10 用户确认）

- **D1 plan 完结 = 方案 A：手动状态变更、全通道**。新增 `PATCH /v1/plans/{id}` + client + CLI + PlanDetail 按钮。**保持 C2「plan 是纯归组，不推进」**——状态由人手动置，系统**不自动改**。UI 依据 counts 提示「其下 job 已全部终态，可标记完成」，但不代劳。
  - 被否决：自动流转（attach 首个 job → active；全终态 → done）。理由：违反 C2，且需在 attach / job 终态回写点插钩子，耦合度明显上升。
- **D2 派生操作门禁 = 前后端都 gate `resume` 与 `rebuild`**（比"仅 gate resume"更严，用户明确选择）。
  - ⚠️ **有意的行为变更（回归）**：`gofer job rerun <running-id>` 与 `POST /v1/jobs/{running-id}/rebuild` 此后返回 **400**。旧 `rerun` 从不看源状态。用户已知悉并接受——rerun 一个还在跑的 job 通常是误操作。
  - 想续接**还在跑**的 interactive job，正确入口是既有的「打开终端」(attach)，不是 resume。

## 核心约束

- **G021 入口薄**：终态校验落 `internal/job`（`ResumeJob`/`RebuildJob` 体内）；handler 只做 sentinel → 状态码映射。plan 状态白名单校验属入参校验，落 handler。
- **复用既有原语**：后端用 `job.IsTerminal`（`cancel.go:169`），前端用 `isTerminalView`（`JobDetail.vue:271`）。**不要重新实现终态判断。**
- **`SetPlanStatus` 不动**（jobstore 层已正确），只接线。
- 本仓是独立通用工具库（`AGENTS.md` G031）：代码/注释/测试数据禁止出现业务领域信息。

---

## 任务分解（T1..T7）

### T1 —（后端）`PATCH /v1/plans/{id}`：plan 状态变更端点

**`internal/httpapi/plan_handler.go`** 新增：

```go
// updatePlanReq is the PATCH /v1/plans/{id} body (P6): move a plan along its
// lifecycle. status is required; progress is optional (nil = keep current).
// 系统不自动推进 plan 状态（C2：plan 是纯归组），全部由调用方显式置。
type updatePlanReq struct {
	Status   string `json:"status"`
	Progress *int   `json:"progress,omitempty"`
}

// validPlanStatus 白名单：jobstore.SetPlanStatus 不校验取值，必须在入口挡住。
func validPlanStatus(s string) bool {
	switch s {
	case jobstore.PlanOpen, jobstore.PlanActive, jobstore.PlanDone, jobstore.PlanArchived:
		return true
	}
	return false
}

func (s *Server) handleUpdatePlan(c *rux.Context) {
	id := c.Param("id")
	var body updatePlanReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	status := strings.TrimSpace(body.Status)
	if !validPlanStatus(status) {
		writeError(c, http.StatusBadRequest, "invalid status",
			"status must be one of open/active/done/archived")
		return
	}
	// SetPlanStatus 用裸 UPDATE、不看 affected rows：不存在的 plan 会「假成功」。
	// 故先 GetPlan 判存在（同 handleAttachPlanJob 的前置模式）。
	if _, ok, err := s.jobs.Meta().GetPlan(id); err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+id)
		return
	}
	progress := -1 // <0 = 保持原 progress（plans.go:112）
	if body.Progress != nil {
		progress = *body.Progress
	}
	if err := s.jobs.Meta().SetPlanStatus(id, status, progress); err != nil {
		writeError(c, http.StatusInternalServerError, "update plan failed", err.Error())
		return
	}
	p, ok, err := s.jobs.Meta().GetPlan(id)
	if err != nil || !ok {
		writeError(c, http.StatusInternalServerError, "reload plan failed", "")
		return
	}
	c.JSON(http.StatusOK, toPlanView(p))
}
```

**`internal/httpapi/server.go`**（现状 `:433-437` 的 plan 路由块内，`GET /plans/{id}` 之后）：
```go
		r.PATCH("/plans/{id}", s.handleUpdatePlan)
```

**验收**：`go test ./internal/httpapi/...` 绿；扩展 `plan_handler_test.go`：PATCH 合法 status → 200 且响应 `status` 已变；非法 status → 400；不存在的 plan → **404**（压住"假成功"）；`progress` 省略时原值不变、给出时被更新。

---

### T2 —（后端）`ErrJobNotTerminal`：resume / rebuild 的终态门禁

**`internal/job/resume.go`** 的 var 块（`ErrNoSession` 旁）新增：
```go
	// ErrJobNotTerminal marks a derive attempt (resume / rebuild) whose SOURCE job is
	// still queued/running. Resuming a live job would hand the SAME session_id to a
	// second process while the source still holds it (concurrent writes to one agent
	// session). Rebuild of a live job is technically harmless but rejected too, for a
	// consistent rule (P6/D2 — this intentionally changes `job rerun <running-id>`,
	// which historically ignored the source status, to a 400).
	ErrJobNotTerminal = errors.New("source job is not in a terminal state")
```

**`internal/job/resume.go`** 的 `ResumeJob`（现状 `:41-47`，`s.Get` 之后、`SessionID` 校验**之前**）：
```go
	if !IsTerminal(src.Status) {
		return JobResult{}, fmt.Errorf("%w: %q is %s", ErrJobNotTerminal, jobID, src.Status)
	}
```
> 放在 `SessionID` 校验之前：一个 `queued` job 尚未捕获 session，旧逻辑会报 `ErrNoSession`（误导）；现在报更准确的 `ErrJobNotTerminal`。

**`internal/job/rebuild.go`** 的 `RebuildJob`（现状 `:110-115`，`s.Get` 之后、`RequestJSON` 校验**之前**）：
```go
	if !IsTerminal(src.Status) {
		return JobResult{}, fmt.Errorf("%w: %q is %s", ErrJobNotTerminal, jobID, src.Status)
	}
```

**`internal/httpapi/job_handler.go`**：rebuild 的 sentinel 映射（现状 `:333-336`）与 resume 的（现状 `:348-352`）**各加一条**：
```go
	if errors.Is(err, job.ErrJobNotTerminal) {
		writeError(c, http.StatusBadRequest, "source job not terminal", err.Error())
		return
	}
```

**验收**：`go build ./... && go vet ./...` 绿；
- `internal/job`：源 job `running` → `ResumeJob` 返回 `ErrJobNotTerminal`（**不是** `ErrNoSession`）；`RebuildJob` 同；源 job 终态 → 二者仍照常成功。
- `internal/httpapi`：`POST /jobs/{running-id}/rebuild` → 400；`POST /jobs/{running-id}/resume` → 400。

---

### T3 —（后端）client `UpdatePlan` + CLI `plan set-status` / `plan archive`

**`internal/client/client.go`** —— **逐字仿 `UpdateTodo`（`:616-624`），它就是本仓的 PATCH 模板**（`doJSON` 签名是 `(method, path string, body io.Reader, out any)`，`:822`）：
```go
// UpdatePlan moves a plan along its lifecycle (PATCH /v1/plans/{id}, P6). status must be
// one of open/active/done/archived. A nil progress keeps the plan's current progress.
func (c *Client) UpdatePlan(planID, status string, progress *int) (Plan, error) {
	payload := map[string]any{"status": status}
	if progress != nil {
		payload["progress"] = *progress
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Plan{}, fmt.Errorf("encode update plan: %w", err)
	}
	var p Plan
	err = c.doJSON(http.MethodPatch, "/v1/plans/"+url.PathEscape(planID), bytes.NewReader(body), &p)
	return p, err
}
```
> `r.PATCH` 在本仓已有先例（`server.go:438` 的 `/todos/{todo_id}`），rux 支持，无需额外适配。

**`internal/commands/plan.go`**（仿 `attach` `:71` / `set-todo` `:95` 的注册块）新增两个子命令：
- `set-status <plan-id> <status>` —— 主命令
- `archive <plan-id>` —— 便捷别名，等价 `set-status <id> archived`

两者都 `bindConfigFlag(c)` + `bindServerFlags(c)`；成功后打印 `plan <id> -> <status>`。

**验收**：`go test ./internal/client/... ./internal/commands/...` 绿；扩展测试：`UpdatePlan` 往返（PATCH 打到正确路径与 body）；CLI `plan set-status` / `plan archive` 已注册进 `plan` 子命令组（仿 `job_subcmd_test.go:17` 的注册断言）。

---

### T4 —（前端类型/client）`updatePlan` + `PlanStatus` 收敛

**`web/src/api/types.ts`**：`PlanStatus` 已存在（P4 T1）。无需新类型。

**`web/src/api/client.ts`**（plan 一族之后）：
```ts
// plan 生命周期（P6）：手动置状态。系统不自动推进（C2）。
export function updatePlan(id: string, status: PlanStatus, progress?: number): Promise<Plan> {
  return request<Plan>(`/v1/plans/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(progress == null ? { status } : { status, progress }),
  })
}
```

**验收**：`pnpm build` 绿。

---

### T5 —（前端）`PlanDetail.vue`：状态动作区 + 「可完结」提示

在 head 区（`(active)` pill 旁，现状 `:136` 的 `statusClass`）加动作按钮，按当前状态给出可用动作：

| 当前 status | 可用动作 |
|---|---|
| `open` / `active` | 「标记完成」→ `done`；「归档」→ `archived` |
| `done` | 「归档」→ `archived`；「重新打开」→ `open` |
| `archived` | 「重新打开」→ `open` |

```ts
const canFinish = computed(() => {
  const c = plan.value?.counts
  // 「其下 job 已全部终态」= 有 job 且没有 queued/running。仅作提示，不自动改状态（C2）。
  return !!c && c.total > 0 && c.queued === 0 && c.running === 0
})

async function setStatus(next: PlanStatus): Promise<void> {
  if (updating.value) return
  updating.value = true
  try {
    plan.value = { ...plan.value!, ...(await updatePlan(props.id, next)) }
    statusError.value = ''
  } catch (e) {
    statusError.value = e instanceof Error ? e.message : String(e)
  } finally {
    updating.value = false
  }
}
```

`canFinish && plan.status !== 'done'` 时，在进度条下方渲染一行提示：`其下 job 已全部终态，可标记完成`（弱化文案，非阻断）。

> **注意**：`updatePlan` 返回的是 `planView`（header），**不含** `counts/jobs/todos`。故用展开合并（`{...plan.value, ...resp}`）保留详情字段，或直接重新 `getPlan(props.id)`。二选一，勿整体覆盖 `plan.value`（会把 jobs/todos 清空）。

**验收**：`pnpm build` 绿；运行期：open 的 plan 点「标记完成」→ pill 变 done、jobs/todos 仍在（不被清空）；点「归档」→ archived；archived 可「重新打开」。

---

### T6 —（前端）`JobDetail.vue`：两个派生按钮加终态 gate

**「快速重建」**（现状 `:973` `v-if="job"`）：
```vue
          v-if="job && isTerminalView"
```

**「继续会话」**（现状 `:1046` `v-if="job.session_id"`）：
```vue
      <div v-if="job.session_id && isTerminalView" class="meta-item">
```
以及其下的内联 resume 表单（`v-if="job.session_id && showResumeForm"`）同步加 `isTerminalView`，否则表单可能在 job 转回非终态时残留。

> `isTerminalView` 是既有 computed（`:271`），**不要另写终态判断**。
> 运行中的 interactive job 想续接 → 用既有的「打开终端」(attach)，那个按钮本就只在 running 时出现。

**验收**：`pnpm build` 绿；运行期：running job 详情**不显示**「快速重建」「继续会话」；job 转终态后（轮询刷新）两个按钮出现。

---

### T7 — 测试与验证门禁

**7.1 后端（容器内）**
- [x] `go build ./... && go vet ./...` 绿（容器 Linux）。
- [x] 全量 `go test ./... -p 1 -count=1` 绿（34 包，禁缓存）。
- [x] 新用例逐个 `-run -v -count=1` 确认**真实执行**：`TestResumeJobRejectsRunningSourceBeforeNoSession`（断 `ErrJobNotTerminal` 而非 `ErrNoSession`）/ `TestRebuildJobRejectsRunningSource` / `TestUpdatePlan*` / `TestResume|RebuildRunningJobRejected`。

**7.2 前端（主机）**
- [x] `pnpm build` 绿（= `vue-tsc --noEmit && vite build`），主控经 `gofer job -a exec` 在主机独立复跑。

**7.3 运行期冒烟（用户眼检）**
- plan：`gofer plan set-status <id> done` → `gofer plan show <id>` 见 done；PlanDetail 上「标记完成」「归档」「重新打开」可用且 jobs/todos 不被清空。
- job：running job 详情无「快速重建」「继续会话」；`gofer job rerun <running-id>` → **400**（有意变更）；对终态 job 一切照旧。

> **主控实施中修正（v0.1 未列）**：codex 在 `handleResumeJob`/`handleRebuildJob` 里各加了提前 return 的 `ErrJobNotTerminal` 分支，与既有 `resumeStatus`/`rebuildStatus` 映射**冗余**且分叉了 error 文案。已删冗余、统一走 status 函数（error=类别、detail=具体原因，与 no-session/cross-runner 一致）；两个 HTTP 测试的断言相应从 `body.Error` 改为 `body.Detail`（`Contains "not in a terminal state"`）。`TestResume|RebuildStatusMapping` 独立锁住 sentinel→400，删分支不失覆盖。

## 测试清单汇总

| 层 | 文件 | 用例要点 |
|---|---|---|
| httpapi（plan） | `plan_handler_test.go`（扩展） | PATCH 合法 status → 200 且状态已变；非法 status → 400；**未知 plan → 404**（压"假成功"）；progress 省略保持原值 / 给出则更新 |
| httpapi（gate） | `job_handler` 相关测试（扩展/新） | `POST /jobs/{running}/rebuild` → 400；`POST /jobs/{running}/resume` → 400 |
| job | `resume_test.go` / `rebuild_test.go`（扩展） | 源 running → `ErrJobNotTerminal`（**非** `ErrNoSession`）；源终态 → 照常成功 |
| client | `client_e2e_test.go`（扩展） | `UpdatePlan` 往返：PATCH 路径 + body + 返回新 status |
| commands | `plan` 子命令注册测试 | `set-status` / `archive` 已注册（仿 `job_subcmd_test.go:17`） |
| 前端 | —（无测试框架） | `pnpm build` 兜底 + 7.3 冒烟 |

**核心不变量**
- **plan 状态只由显式调用改变**：系统不在 attach / job 终态回写等路径自动推进（C2）。`canFinish` 只提示、不代劳。
- **PATCH 不存在的 plan → 404**，不得因 `SetPlanStatus` 裸 UPDATE 而假成功。
- **status 白名单在入口挡住**（jobstore 不校验取值）。
- **派生操作要求源 job 终态**：`ResumeJob` / `RebuildJob` 对非终态源返回 `ErrJobNotTerminal` → HTTP 400。
- **终态判断复用既有原语**：后端 `job.IsTerminal`，前端 `isTerminalView`。全仓不得出现第二份终态列表。
- **`PlanDetail` 的 `updatePlan` 响应合并不得清空 `jobs`/`todos`**（PATCH 只回 header）。

## 风险

- **R1（有意回归，用户已接受）**：`gofer job rerun <running-id>` 与 `POST /jobs/{running}/rebuild` 由「成功起新 job」变为 **400**。旧行为从不看源状态。已在 T2 的 sentinel 注释与本节写明。
- **R2**：`PlanDetail` 若用 `plan.value = await updatePlan(...)` 整体覆盖，会把 `counts/jobs/todos` 清空（PATCH 只回 `planView`）。T5 已给出合并写法。
- **R3**：`ErrJobTerminal`（既有，交互域）与 `ErrJobNotTerminal`（新）名字仅差一个 `Not`，易混。前者=已终态不能交互，后者=未终态不能派生。注释已互相点名。
- **R4**：`archive` 作为 CLI 便捷命令，与 `set-status <id> archived` 语义重复。保留二者（前者常用），但实现上后者是唯一真源，`archive` 只是转调。
- **R5**：`queued` job 现在报 `ErrJobNotTerminal` 而非 `ErrNoSession`。若有下游依赖 `ErrNoSession` 判断 queued，需一并核查（探查未发现此类依赖）。

## 待确认

无。D1 / D2 均已由用户拍板（见「已拍板决策」）。
