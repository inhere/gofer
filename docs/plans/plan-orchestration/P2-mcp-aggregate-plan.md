# P2 — MCP 工具 + 进度聚合 实施计划

> 总纲：[plan-orchestration-plan.md](./plan-orchestration-plan.md)
> 设计：[../../design/2026-07-09-plan-orchestration-design.md](../../design/2026-07-09-plan-orchestration-design.md)（草案 v0.2，归组键锁定 `plan_id`）
> 上游：[P1-data-plan.md](./P1-data-plan.md)（✅ 已完成并 push；本期在其上叠加）
> bd: h-aii-xfvc (epic)
> 触点均已实测定位（2026-07-09 只读探查，见各 T 的 file:line）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿：P2 代码级子计划（MCP 三工具 + run_job plan_id 透传 + GetPlan 进度聚合），触点已实测 |

## 一句话

在 P1 数据底座上补齐 **MCP 通道** 与 **进度聚合**：新增 `gofer_create_plan` / `gofer_attach_job` / `gofer_get_plan` 三工具，给 `gofer_run_job` 加 `plan_id` 入参（提交即归组），并让 `GetPlan`（HTTP + client + MCP）在查询期实时聚合其下 jobs 的状态计数 `{total,queued,running,done,failed}`。

## 范围

**做（P2）**

1. **进度聚合（数据层单源）**：jobstore 加 `PlanJobStatusCounts(planID)`（`SELECT status,COUNT(*) ... GROUP BY status` 原始 map）+ `PlanCounts` 结构 + `RollupPlanCounts(map)` 折叠（把 7 个 job 状态归并到 5 桶）。HTTP `GET /v1/plans/{id}` 响应加 `counts` 字段；client `Plan` 加 `Counts`；MCP `gofer_get_plan` 同样带 `counts`。
2. **MCP `gofer_run_job` 加 `plan_id`**：`runJobInput` 加 `plan_id` 字段，handler 透传到 `job.JobRequest.PlanID`（提交即归组，仿 P0 给 `runJobInput` 加 `agent_args` 的做法）。两 Backend 已整体转发 `JobRequest`，无需改 Backend。
3. **MCP 三新工具**：`Backend` 接口加 `CreatePlan / AttachJob / GetPlan`；localBackend（经 `job.Service.Meta()` 拿 `*jobstore.Store`）与 clientBackend（转发 `client.CreatePlan/AttachJob/GetPlan`）各补三实现；server.go 注册三工具 + handler。

**不做（划归 P3/P4）**

- `plan_todos` 待办（P3）。
- 前端 `Plans.vue` / `PlanDetail.vue`、进度条渲染、session 续跑 UI（P4）。
- `plans.progress`/`status` 的自动推进：C2 规定 plan 是纯归组、不推进；本期 `progress` 列保持 P1 现状（不自动写），聚合计数是**查询期实时算**，不落库（设计 §12 待确认 5 倾向）。

## 核心约束（承接总纲，本期重点）

- **C5 三入口层只做绑定/校验/转发**（G021）：`mcpserver` handler 只做入参投影 + view 投影；plan CRUD/聚合逻辑在 jobstore（数据）/ backend（后端访问）。
- **C3 jobstore 中立**：`PlanJobStatusCounts` 只吃 `planID` 字符串、返回 map，不 import job；状态桶归并在 jobstore 内以**硬编码状态串**完成（与现有 `pendingInteractionTerminalJobStatuses`（`internal/jobstore/interactions.go:170`）、`activeJobStatuses`（`internal/jobstore/jobs.go:292`）同风格，不 import job）。
- **Backend 双实现范式（照抄 RunJob/GetJob）**：`Backend` 接口方法 → localBackend 直驱 `job.Service`/`Meta()`；clientBackend 转发 `client.Client`。差异源形（jobstore.Plan vs client.Plan）→ 按 backend.go 既定约定（projects/agents/artifacts 那样）**返回 mcpserver 自有 view 类型 `planView`**，两端各自构建，形状逐字对齐。

## 已确认的关键事实（探查结论）

| 事实 | 位置 | 对 P2 的影响 |
|---|---|---|
| job 状态取值共 7 个：`queued/running/done/failed/cancelled/timeout/pending_interaction` | `internal/job/model.go:261-270` | 折叠映射覆盖全部 7 个，`total = 5 桶之和`（无遗漏桶） |
| 已有 `CountJobsByStatus()` 用 `SELECT status,COUNT(*) FROM jobs GROUP BY status` | `internal/jobstore/jobs.go:315-338` | `PlanJobStatusCounts` **照抄**其扫描范式，仅加 `WHERE plan_id=?` |
| `JobRequest.PlanID` 已是客户端可设字段（`json:"plan_id,omitempty"`） | `internal/job/model.go:82-85` | run_job 透传直接落库，无需改 job 层 |
| `Backend` 只有 localBackend / clientBackend 两实现，**无手写 fake** | `internal/mcpserver/*_test.go`（`mockBackend` 返回真 clientBackend 套 httptest mux） | 给接口加方法只需补两真实现，测试不会因 fake 缺方法而炸 |
| `s.jobs.Meta()` 返回 `*jobstore.Store`；`s.jobs.ListJobs(job.ListOpts{Plan,Limit})` 已用于 handleGetPlan | `internal/job/service.go:204`；`internal/httpapi/plan_handler.go:80-97` | localBackend.GetPlan 复用同两调用 |
| client 已 import jobstore（`internal/client/watch.go:16`）；mcpserver **尚未** import jobstore，但 localBackend.GetPlan 需经 `Meta()` → 本期 mcpserver 新增 import jobstore（入口→数据层，G022 允许） | — | `PlanCounts` 放 jobstore 可被 httpapi/client/mcpserver 三处共享 |
| HTTP plan 路由已注册 4 条（create/list/get/attach） | `internal/httpapi/server.go:429-432` | 本期不加路由，只在既有 `handleGetPlan` 响应里加 `counts` |
| plan_id 生成范式（依赖 `job.JobIDLayout`+`job.RandomSuffix`） | `internal/httpapi/plan_handler.go:47`；`internal/job/service.go:28`、`internal/job/submit.go:431` | localBackend.CreatePlan 复刻此生成逻辑（mcpserver 已 import job） |

## 状态折叠映射（本期决策，见「待确认 D1」）

5 桶输出 `{total,queued,running,done,failed}`，7 状态归并如下（`total` = 全部行数 = 5 桶之和）：

| 桶 | 归入的 job 状态 |
|---|---|
| `queued` | `queued` |
| `running` | `running` + `pending_interaction`（等待作答，仍在飞行中） |
| `done` | `done` |
| `failed` | `failed` + `timeout` + `cancelled`（终态非成功） |
| `total` | 以上全部之和 |

---

## 任务分解（T1..T8）

> 顺序：T1（数据层）→ T2/T3（HTTP/client 聚合）→ T4（run_job 透传）→ T5/T6/T7（MCP 接口+两实现）→ T8（测试门禁）。SR1202：每个 T 完成后更新总纲 checkbox 并 Git 提交（feat(plan): 前缀）。

### T1 — jobstore 进度聚合（数据层单源）

**现状**：`internal/jobstore/plans.go` 有 CRUD（`InsertPlan/GetPlan/ListPlans/SetPlanStatus/AttachJobToPlan/TouchPlan`），无按 plan 的状态计数。`jobs.go:315-338` 已有全局 `CountJobsByStatus()` 可照抄。

**目标**：在 `internal/jobstore/plans.go` 末尾追加：

```go
// PlanCounts is the查询期实时聚合 of a plan's jobs by rolled-up status bucket
// (plan-orchestration P2). Total = queued+running+done+failed (every job status
// maps to exactly one bucket, so no rows are lost). json tags carry the wire
// shape shared by the HTTP GetPlan response, client.Plan.Counts and the MCP
// gofer_get_plan output.
type PlanCounts struct {
	Total   int `json:"total"`
	Queued  int `json:"queued"`
	Running int `json:"running"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
}

// PlanJobStatusCounts returns a raw status->count map over the jobs bound to
// planID (WHERE plan_id=? GROUP BY status). Mirrors CountJobsByStatus. An empty
// plan (no jobs) yields an empty (non-nil) map.
func (s *Store) PlanJobStatusCounts(planID string) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM jobs WHERE plan_id = ? GROUP BY status`, planID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: plan job status counts %q: %w", planID, err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("jobstore: scan plan job status %q: %w", planID, err)
		}
		out[status] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: plan job status rows %q: %w", planID, err)
	}
	return out, nil
}

// RollupPlanCounts folds a raw job status->count map into the 5-bucket PlanCounts.
// running 桶含 pending_interaction；failed 桶含 timeout/cancelled（终态非成功）。
// 状态串硬编码（与 pendingInteractionTerminalJobStatuses 同风格，保持 jobstore 不 import job）。
func RollupPlanCounts(raw map[string]int) PlanCounts {
	var c PlanCounts
	for st, n := range raw {
		c.Total += n
		switch st {
		case "queued":
			c.Queued += n
		case "running", "pending_interaction":
			c.Running += n
		case "done":
			c.Done += n
		case "failed", "timeout", "cancelled":
			c.Failed += n
		default:
			// 未知状态仍计入 total（防遗漏），不落任何桶 —— 由测试守护 5 桶=total 的前提
		}
	}
	return c
}
```

> 注：`default` 分支理论上不触达（7 状态已穷举）；保留以防未来新增状态时 total 仍准确。若要严格保证 `total==5 桶之和`，见待确认 D1。

**验收**：
- `PlanJobStatusCounts("plan-x")` 对含多状态 job 的 plan 返回正确 map；空 plan 返回非 nil 空 map。
- `RollupPlanCounts` 单测：`{queued:1,running:2,pending_interaction:1,done:3,failed:1,timeout:1,cancelled:1}` → `{Total:10,Queued:1,Running:3,Done:3,Failed:3}`。
- `go test ./internal/jobstore/...` 绿。

**测试（`internal/jobstore/plans_test.go` 追加）**：
- `TestPlanJobStatusCountsAndRollup`：建 plan-1，`UpsertJob` 多条不同 status 并 `AttachJobToPlan`（或直接建带 plan_id 的 job），断言 `PlanJobStatusCounts` map 与 `RollupPlanCounts` 5 桶；空 plan 断言空 map / 全 0 桶。参照现有 `sampleJob(id,agent,ts)` 工具（`plans_test.go:51`）——注意需给 job 设 status，若 `sampleJob` 固定某状态则改用 `UpsertJob` 自造 status。

---

### T2 — HTTP `GET /v1/plans/{id}` 加 counts

**现状**（`internal/httpapi/plan_handler.go:75-97`）：

```go
type planDetail struct {
	planView
	Jobs []job.JobResult `json:"jobs"`
}

func (s *Server) handleGetPlan(c *rux.Context) {
	id := c.Param("id")
	p, ok, err := s.jobs.Meta().GetPlan(id)
	...
	jobs, err := s.jobs.ListJobs(job.ListOpts{Plan: id, Limit: 1000})
	...
	c.JSON(http.StatusOK, planDetail{planView: toPlanView(p), Jobs: jobs})
}
```

**目标**：`planDetail` 加 `Counts`，handler 计算后填入。

```go
type planDetail struct {
	planView
	Counts jobstore.PlanCounts `json:"counts"`
	Jobs   []job.JobResult     `json:"jobs"`
}
```

`handleGetPlan` 在拿到 `jobs` 后、返回前插入：

```go
	raw, err := s.jobs.Meta().PlanJobStatusCounts(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "plan counts failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, planDetail{
		planView: toPlanView(p),
		Counts:   jobstore.RollupPlanCounts(raw),
		Jobs:     jobs,
	})
```

> jobstore 已在本文件 import（`plan_handler.go:11`），无新 import。`counts` 恒输出（空 plan → 全 0），故用值类型非指针。

**验收**：
- `GET /v1/plans/{id}` 响应含 `counts:{total,queued,running,done,failed}`，与该 plan 下 jobs 实况一致。
- 空 plan 返回 `counts` 全 0。

**测试（`internal/httpapi/plan_handler_test.go` 追加或扩展 `TestCreateListGetPlanAndAttachJob`）**：建 plan、attach 若干不同 status 的 job（用现有 `do(...)` submit + attach，或直接构造），断言 GetPlan 的 `counts`。

---

### T3 — client `Plan` 加 `Counts`

**现状**（`internal/client/client.go:513-524`）：`Plan` 结构无 counts 字段；`GetPlan`（:554-559）`doJSON` 直接反序列化到 `Plan`。

**目标**：`Plan` 加字段（jobstore 已被 client import，见 `watch.go:16`，直接复用 `jobstore.PlanCounts` 保证形状单源）：

```go
type Plan struct {
	PlanID      string               `json:"plan_id"`
	Title       string               `json:"title,omitempty"`
	Description string               `json:"description,omitempty"`
	Status      string               `json:"status"`
	Owner       string               `json:"owner,omitempty"`
	Progress    int                  `json:"progress,omitempty"`
	CreatedAt   int64                `json:"created_at"`
	UpdatedAt   int64                `json:"updated_at"`
	Counts      *jobstore.PlanCounts `json:"counts,omitempty"`
	Jobs        []job.JobResult      `json:"jobs,omitempty"`
}
```

> 用指针 + `omitempty`：`ListPlans`（列表接口不返回 counts）时 `Counts` 为 nil，`GetPlan` 时非 nil。需在 client.go import 段加 `"github.com/inhere/gofer/internal/jobstore"`（当前 client.go 未 import，watch.go 才 import；检查 client.go import 段并补上）。

**验收**：`GetPlan` 后 `p.Counts != nil` 且字段与服务端一致；`ListPlans` 结果各项 `Counts == nil`。`go build ./...` 绿。

**CLI 顺带（可选，低优）**：`internal/commands/plan.go:154-170` 的 `printPlan` 可在 `p.Counts != nil` 时打印一行 `counts: total=.. queued=.. running=.. done=.. failed=..`。非必须，若做则一并测 `plan show` 输出。

---

### T4 — MCP `gofer_run_job` 加 `plan_id`（提交即归组）

**现状**（`internal/mcpserver/server.go:345-405`）：`runJobInput` 无 `plan_id`；`runJobHandler` 组装 `job.JobRequest` 时未带 `PlanID`。参照 P0 给 `runJobInput` 加 `AgentArgs`（:351）+ handler 透传（:384）的做法。

**目标**：
1. `runJobInput` 加字段（放在 `Title` 之后、`Role` 之前一带即可）：

```go
	// PlanID groups this job under a plan header (plan-orchestration P2, 客户端可设).
	// 透传到 job.JobRequest.PlanID —— 提交即归组，无需事后 attach。
	PlanID string `json:"plan_id,omitempty"`
```

2. `runJobHandler` 组装 `job.JobRequest{...}` 时加：

```go
		PlanID: in.PlanID,
```

3. `gofer_run_job` 工具描述（server.go:80-82）补一句 plan 归组说明（可选）：`"... Set plan_id to group the job under a plan."`

> 两 Backend（local/client）已整体转发 `job.JobRequest`（`SubmitJob`/`Submit`），`PlanID` 随之落库，**无需改 Backend**。

**验收**：`gofer_run_job` 带 `plan_id` 提交后，`gofer_get_plan`/HTTP GetPlan 能看到该 job 归入 plan；不带 `plan_id` 时行为不变。

**测试（`internal/mcpserver/server_test.go`）**：localBackend 路径提交带 `plan_id` 的 run_job，`GetJob` 或经 store 断言 `plan_id` 落库；或经 `gofer_get_plan`（T5 就绪后）断言 jobs 含之。

---

### T5 — MCP：`Backend` 接口 + `planView` + 三工具注册/handler

**现状**：
- `Backend` 接口（`internal/mcpserver/backend.go:17-46`）无 plan 方法。
- `server.go` 无 plan view / 工具；工具注册集中在 `newServer`（:69-154），最后一个是 `gofer_list_pending_interactions`（:149-152）。

**目标 5.1 — 接口加三方法**（`backend.go`，方法签名参照 RunJob/GetJob 返回域/视图 + error 的范式；plan 因两端源形不同，返回 mcpserver 自有 `planView`）：

```go
	// Plan grouping (plan-orchestration P2). 与 projects/agents/artifacts 同：两端源形
	// 不同（jobstore.Plan vs client.Plan），故 Backend 返回 mcpserver view 类型 planView，
	// local/client 各自构建、形状逐字对齐。
	CreatePlan(title, description string) (planView, error)
	AttachJob(planID, jobID string) (planView, error)
	GetPlan(planID string) (planView, error)
```

**目标 5.2 — `planView` 类型**（server.go，紧邻 jobView 定义处 :225-272 之后新增一节；含 counts + jobs 便于与 HTTP planDetail 对齐）：

```go
// planView is the snake_case projection returned by the plan tools. Mirrors the
// HTTP planDetail shape (header + counts + jobs) so MCP and HTTP callers see the
// same plan structure. create/attach return header (+empty jobs, zero counts);
// get returns the full detail.
type planView struct {
	PlanID      string              `json:"plan_id"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Status      string              `json:"status"`
	Owner       string              `json:"owner,omitempty"`
	Progress    int                 `json:"progress,omitempty"`
	CreatedAt   int64               `json:"created_at"`
	UpdatedAt   int64               `json:"updated_at"`
	Counts      jobstore.PlanCounts `json:"counts"`
	Jobs        []jobView           `json:"jobs"`
}
```

> server.go / backend*.go 需新增 import `"github.com/inhere/gofer/internal/jobstore"`（入口→数据层，G022 允许；client 侧 planView 由 clientBackend 组装，见 T7）。

**目标 5.3 — 工具入参 + handler**（server.go 末尾新增一节，仿 getJobHandler/postMessageHandler 范式）：

```go
// --- gofer_create_plan / gofer_attach_job / gofer_get_plan -----------------

type createPlanToolInput struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

func createPlanHandler(b Backend) mcp.ToolHandlerFor[createPlanToolInput, planView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in createPlanToolInput) (*mcp.CallToolResult, planView, error) {
		pv, err := b.CreatePlan(in.Title, in.Description)
		if err != nil {
			return nil, planView{}, err
		}
		return nil, pv, nil
	}
}

type attachJobToolInput struct {
	PlanID string `json:"plan_id"`
	JobID  string `json:"job_id"`
}

func attachJobHandler(b Backend) mcp.ToolHandlerFor[attachJobToolInput, planView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in attachJobToolInput) (*mcp.CallToolResult, planView, error) {
		pv, err := b.AttachJob(in.PlanID, in.JobID)
		if err != nil {
			return nil, planView{}, err
		}
		return nil, pv, nil
	}
}

type getPlanToolInput struct {
	PlanID string `json:"plan_id"`
}

func getPlanHandler(b Backend) mcp.ToolHandlerFor[getPlanToolInput, planView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in getPlanToolInput) (*mcp.CallToolResult, planView, error) {
		pv, err := b.GetPlan(in.PlanID)
		if err != nil {
			return nil, planView{}, err
		}
		return nil, pv, nil
	}
}
```

**目标 5.4 — 注册**（`newServer`，在 `listPendingInteractions`（server.go:152）之后、`return s`（:154）之前）：

```go
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_create_plan",
		Description: "Create a lightweight plan (grouping header) and return it. Use plan_id from the result to attach jobs or submit jobs with plan_id set.",
	}, createPlanHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_attach_job",
		Description: "Attach an existing job to a plan by id. Returns the plan header.",
	}, attachJobHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_get_plan",
		Description: "Get a plan with its jobs and a live status roll-up {total,queued,running,done,failed}.",
	}, getPlanHandler(b))
```

**验收**：`go build ./...` 绿（接口新增方法后必须两 Backend 都实现，见 T6/T7 否则不编译）；MCP schema 含三新工具（snake_case 入参）。

---

### T6 — MCP localBackend 三实现 + `newPlanID`

**现状**（`internal/mcpserver/backend_local.go`）：localBackend 直驱 `b.jobs`（`*job.Service`）；无 plan 方法。`job.Service.Meta()` 返回 `*jobstore.Store`（`internal/job/service.go:204`）。

**目标 6.1 — plan_id 生成 helper**（复刻 `plan_handler.go:47`，mcpserver 已 import job；新增 import `time`）：

```go
// newPlanID mirrors the httpapi handler's plan id 生成 (plan-<ts>-<rand>). 本地模式
// 无中央 serve 生成 id，故 localBackend 自造；client 模式让服务端生成（传空）。
func newPlanID() string {
	return "plan-" + time.Now().Format(job.JobIDLayout) + "-" + job.RandomSuffix()
}
```

**目标 6.2 — 三方法**（backend_local.go 末尾）：

```go
// --- plan grouping (local 直驱 jobstore via Meta) ---------------------------

func (b *localBackend) CreatePlan(title, description string) (planView, error) {
	now := time.Now().Unix()
	p := jobstore.Plan{
		PlanID: newPlanID(), Title: title, Description: description,
		Status: jobstore.PlanOpen, // owner 留空：standalone 无 HTTP caller（同 RegisterAgent 本地）
		CreatedAt: now, UpdatedAt: now,
	}
	if err := b.jobs.Meta().InsertPlan(p); err != nil {
		return planView{}, err
	}
	return planHeaderView(p), nil
}

func (b *localBackend) AttachJob(planID, jobID string) (planView, error) {
	st := b.jobs.Meta()
	p, ok, err := st.GetPlan(planID)
	if err != nil {
		return planView{}, err
	}
	if !ok {
		return planView{}, fmt.Errorf("unknown plan %q", planID)
	}
	attached, err := st.AttachJobToPlan(jobID, planID)
	if err != nil {
		return planView{}, err
	}
	if !attached {
		return planView{}, fmt.Errorf("unknown job %q", jobID)
	}
	_ = st.TouchPlan(planID)
	p, _, _ = st.GetPlan(planID)
	return planHeaderView(p), nil
}

func (b *localBackend) GetPlan(planID string) (planView, error) {
	st := b.jobs.Meta()
	p, ok, err := st.GetPlan(planID)
	if err != nil {
		return planView{}, err
	}
	if !ok {
		return planView{}, fmt.Errorf("unknown plan %q", planID)
	}
	jobs, err := b.jobs.ListJobs(job.ListOpts{Plan: planID, Limit: 1000})
	if err != nil {
		return planView{}, err
	}
	raw, err := st.PlanJobStatusCounts(planID)
	if err != nil {
		return planView{}, err
	}
	pv := planHeaderView(p)
	pv.Counts = jobstore.RollupPlanCounts(raw)
	pv.Jobs = make([]jobView, 0, len(jobs))
	for _, j := range jobs {
		pv.Jobs = append(pv.Jobs, toJobView(j))
	}
	return pv, nil
}
```

**目标 6.3 — `planHeaderView` 投影 helper**（放 server.go 紧邻 planView 定义，供 local/client 复用；header-only，jobs 置非 nil 空片、counts 零值）：

```go
// planHeaderView projects a jobstore.Plan header onto planView (jobs empty,
// counts zero). GetPlan overwrites Counts/Jobs; create/attach return header-only.
func planHeaderView(p jobstore.Plan) planView {
	return planView{
		PlanID: p.PlanID, Title: p.Title, Description: p.Description,
		Status: p.Status, Owner: p.Owner, Progress: p.Progress,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		Jobs: make([]jobView, 0),
	}
}
```

**验收**：
- localBackend 实现全部 3 方法（接口满足，编译过）。
- CreatePlan 返回带生成 id 的 header；AttachJob 对未知 plan/job 返回 error；GetPlan 返回 header + counts + jobs。
- `go build ./... && go vet ./...` 绿。

---

### T7 — MCP clientBackend 三实现

**现状**（`internal/mcpserver/backend_client.go`）：clientBackend 转发 `b.cli`（`*client.Client`）；client 侧 `CreatePlan/AttachJob/GetPlan` 已具备（`internal/client/client.go:526-570`），T3 后 `client.Plan` 含 `Counts`。

**目标**（backend_client.go 末尾，转发 + 把 `client.Plan` 映射为 `planView`）：

```go
// --- plan grouping (client 转发中央 serve) -----------------------------------

func (b *clientBackend) CreatePlan(title, description string) (planView, error) {
	// 传空 plan_id：让中央 serve 生成 id + owner（与 local 自造 id 分工）。
	p, err := b.cli.CreatePlan("", title, description)
	if err != nil {
		return planView{}, err
	}
	return clientPlanToView(p), nil
}

func (b *clientBackend) AttachJob(planID, jobID string) (planView, error) {
	p, err := b.cli.AttachJob(planID, jobID)
	if err != nil {
		return planView{}, err
	}
	return clientPlanToView(p), nil
}

func (b *clientBackend) GetPlan(planID string) (planView, error) {
	p, err := b.cli.GetPlan(planID)
	if err != nil {
		return planView{}, err
	}
	return clientPlanToView(p), nil
}

// clientPlanToView maps a client.Plan onto planView, byte-for-byte shape
// compatible with localBackend (non-nil empty Jobs, zero Counts when absent).
func clientPlanToView(p client.Plan) planView {
	pv := planView{
		PlanID: p.PlanID, Title: p.Title, Description: p.Description,
		Status: p.Status, Owner: p.Owner, Progress: p.Progress,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		Jobs: make([]jobView, 0, len(p.Jobs)),
	}
	if p.Counts != nil {
		pv.Counts = *p.Counts
	}
	for _, j := range p.Jobs {
		pv.Jobs = append(pv.Jobs, toJobView(j))
	}
	return pv
}
```

> backend_client.go 已 import client/job；`planView.Counts` 是 `jobstore.PlanCounts`，而 `p.Counts` 是 `*jobstore.PlanCounts`（T3），类型一致，直接解引用。

**验收**：clientBackend 实现全部 3 方法；client 模式 `gofer_get_plan` 输出与 local 模式形状一致（非 nil 空 Jobs、counts 桶名一致）。`go build ./...` 绿。

---

### T8 — 测试与门禁

**MCP 测试（`internal/mcpserver/server_test.go` + `backend_client_test.go`）**：
- localBackend：`create_plan` → 拿 plan_id → `run_job(plan_id=...)` → `get_plan` 断言 jobs 含该 job 且 `counts.total==1`、桶正确；`attach_job` 对既有 job 归组后 `get_plan` 断言；未知 plan/job → 工具 error。
- clientBackend（`mockBackend` 套 httptest mux）：mux 注册 `/v1/plans`、`/v1/plans/{id}`、`/v1/plans/{id}/jobs` stub 返回带 `counts` 的 JSON，断言 `clientPlanToView` 映射正确（尤其 `counts` 解引用、空 Jobs 非 nil）。参照 backend_client_test.go 现有 `mockBackend`（:14-31）用法。

**HTTP/jobstore/client 测试**：T1/T2/T3 各自的单测（见对应 T）。

**门禁**：
- `go build ./...`
- `go vet ./...`
- `go test ./internal/jobstore/... ./internal/httpapi/... ./internal/client/... ./internal/mcpserver/...`
- 全量 `go test ./...` 绿（G023 覆盖不降）。

**运行期冒烟（可选，非阻塞）**：`gofer serve` 起本地 → MCP `gofer_create_plan`/`gofer_run_job(plan_id)`/`gofer_get_plan` 走一遍，确认 counts 实时随 job 状态变化。

---

## 测试清单汇总

| 层 | 文件 | 用例要点 |
|---|---|---|
| jobstore | `plans_test.go` | `PlanJobStatusCounts` map；`RollupPlanCounts` 7→5 桶折叠；空 plan |
| httpapi | `plan_handler_test.go` | GetPlan 响应含 `counts`，与 jobs 实况一致；空 plan 全 0 |
| client | `client_test.go`（或 plan 相关） | GetPlan `Counts != nil`；ListPlans `Counts == nil` |
| mcpserver | `server_test.go` | local：create→run_job(plan_id)→get_plan 计数；attach；未知 id error；run_job plan_id 落库 |
| mcpserver | `backend_client_test.go` | client：三方法转发 + `clientPlanToView` 映射（counts 解引用、空 Jobs 非 nil） |

## 风险 & 注意

- **R1 折叠映射语义**（cancelled/timeout 归 failed、pending_interaction 归 running）是产品决策，非纯技术。若前端进度条需区分「取消/超时」，需扩桶——见待确认 D1。单源在 `jobstore.RollupPlanCounts`，改动只此一处。
- **R2 import 方向**：mcpserver 新增 import jobstore（入口→数据层，G022 允许，httpapi 已如此）。务必 `go list -deps ./internal/jobstore/...` 确认 jobstore **未反向** import mcpserver/job（保持 C3 中立）。
- **R3 counts 值 vs 指针**：HTTP planDetail.Counts / MCP planView.Counts 用**值**（恒输出，空 plan 全 0）；client.Plan.Counts 用**指针+omitempty**（ListPlans 不带 counts 时为 nil）。三处语义要一致：clientBackend 映射时对 nil 做零值兜底。
- **R4 owner 差异**：local CreatePlan 的 owner 留空（无 HTTP caller，同 RegisterAgent 本地语义）；client 模式 owner 由服务端 `callerFromCtx` 填。这是既有 standalone vs client 的固有差异，不修正。
- **R5 大 plan 的 jobs 上限**：GetPlan jobs 固定 `Limit:1000`（沿用 P1 handleGetPlan）。counts 是独立 GROUP BY 全量统计，**不受** 1000 截断影响（total 准确，即使 jobs 列表被截断）。这是 counts 用聚合查询而非 `len(jobs)` 的关键原因，测试需覆盖「jobs>可见但 counts 全量」的一致性（可选，低优）。

## 待确认

- **D1 折叠桶定义**：确认 `{total,queued,running,done,failed}` 5 桶足够，且 `pending_interaction→running`、`timeout+cancelled→failed` 的归并可接受？若前端要独立展示 pending/cancelled，需在 `PlanCounts` 扩字段（如 `pending_interaction`、`cancelled`）。**倾向**：先落 5 桶（本计划），P4 前端若需要再扩，改动仅 `RollupPlanCounts` + 三处结构。
- **D2 CLI `plan show` 是否打印 counts**（T3 顺带项）：默认做（一行摘要）还是留到 P4？**倾向**：顺带做，成本极低。
- **D3 gofer_run_job 描述文案**是否要强调 plan_id 归组，避免与 attach 语义混淆？**倾向**：描述补一句即可（T4 已含）。
