# P1 — 工作流引擎地基（实施计划）

> 主纲：[`2026-06-21-workflow-chains-plan.md`](./2026-06-21-workflow-chains-plan.md) · 设计 §5.1–5.3/5.5/5.7/§10。
> 把"提交 N 个 step 串行自动推进"跑通：表 + 幂等推进 + API + fail-fast + 取消。**无 `${steps.N}` 引用**（P2），各 step 独立跑。**最高风险点是推进幂等**（一个 step 绝不起两次）。

---

## P1-a `workflows` 表 + jobs 加 2 列 + DAO

### 落点
- `internal/jobstore/store.go`：schemaStmts 加 `workflows` 表 + `idx_workflows_status`（IF NOT EXISTS）；`migrate()` 给 jobs 加 `workflow_id TEXT` + `step_index INTEGER`（仿 tags_json，ADD COLUMN）。jobs DDL（schemaStmts 的 CREATE TABLE）也同步加这 2 列。
- `internal/jobstore/jobs.go`：`JobRecord` 加 `WorkflowID string` + `StepIndex int`；`selectCols`(COALESCE) / `scanJob` / `UpsertJob`（列/占位/ON CONFLICT SET/参数）5 处贯通。
- `internal/job/model.go`：`JobResult` 加 `WorkflowID string json:"workflow_id,omitempty"` + `StepIndex int json:"step_index,omitempty"`；`JobRequest` 加 `WorkflowID string` + `StepIndex int`（**内部用，json/yaml tag 设 `-`**，不对外提交——由引擎设）。
- `internal/job/service.go`：`toRecord`/`fromRecord` + `Submit` 构造 JobResult 处映射这 2 字段。
- `internal/jobstore/workflows.go`（新）：`Workflow` struct + DAO。

### 步骤
**1) workflows 表**（store.go，按设计 §5.1）：
```sql
CREATE TABLE IF NOT EXISTS workflows (
  id TEXT PRIMARY KEY, title TEXT, status TEXT NOT NULL,
  current_step INTEGER NOT NULL, total_steps INTEGER NOT NULL,
  spec_json TEXT NOT NULL, caller_id TEXT, error TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_workflows_status ON workflows(status);
```
**2) jobs 加 2 列**：`migrate()` `add("workflow_id","workflow_id TEXT")` + `add("step_index","step_index INTEGER")`；jobs CREATE TABLE 同步。`JobRecord`/selectCols(`COALESCE(workflow_id,''), COALESCE(step_index,0)`)/scanJob/UpsertJob 贯通。
**3) workflows DAO**（`workflows.go`）：
```go
type Workflow struct {
    ID, Title, Status string
    CurrentStep, TotalSteps int
    SpecJSON, CallerID, Error string
    CreatedAt, UpdatedAt int64
}
func (s *Store) InsertWorkflow(w Workflow) error
func (s *Store) GetWorkflow(id string) (Workflow, bool, error)
func (s *Store) ListWorkflows(status string, limit int) ([]Workflow, error)
// AdvanceCurrentStep 抢推进权(幂等屏障,SR303): 仅 status='running' 且 current_step==from 时
// UPDATE 到 to, 返回 RowsAffected==1=抢到。并发/重复调用只有一个成功。
func (s *Store) AdvanceCurrentStep(id string, from, to int) (bool, error)
func (s *Store) SetWorkflowStatus(id, status, errMsg string) error
// ListWorkflowJobs: jobs WHERE workflow_id=? ORDER BY step_index ASC。
func (s *Store) ListWorkflowJobs(id string) ([]JobRecord, error)
```
> `AdvanceCurrentStep`：`UPDATE workflows SET current_step=?,updated_at=? WHERE id=? AND current_step=? AND status='running'`，writeMu 内，RowsAffected==1 才 true。这是推进幂等核心。

### P1-a 验收
- 单测 jobstore：新库含 workflows 表 + jobs 含 2 列(PRAGMA)；InsertWorkflow→GetWorkflow round-trip；**`AdvanceCurrentStep` 并发只 1 次成功**（N goroutine 抢 from=1→to=2，恰 1 个 true）；`from` 不匹配/`status!=running` 返 false；旧库 migrate 补 2 列、旧 job workflow_id 空。
- jobs round-trip 带 workflow_id/step_index。

---

## P1-b WorkflowSpec + SubmitWorkflow

### 落点 `internal/job/workflow.go`（新）
```go
type StepSpec struct {
    Name       string   `json:"name,omitempty" yaml:"name,omitempty"`
    ProjectKey string   `json:"project_key" yaml:"project_key"`
    Agent      string   `json:"agent" yaml:"agent"`
    Runner     string   `json:"runner" yaml:"runner"`
    Prompt     string   `json:"prompt,omitempty" yaml:"prompt,omitempty"`
    Cmd        []string `json:"cmd,omitempty" yaml:"cmd,omitempty"`
    Cwd        string   `json:"cwd,omitempty" yaml:"cwd,omitempty"`
    TimeoutSec int      `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
    Tags       []string `json:"tags,omitempty" yaml:"tags,omitempty"`
}
type WorkflowSpec struct {
    Title string     `json:"title,omitempty" yaml:"title,omitempty"`
    Steps []StepSpec `json:"steps" yaml:"steps"`
}
// SubmitWorkflow 校验 spec → 建 workflows 行(running,current_step=1) → 起 step1 job。
func (s *Service) SubmitWorkflow(spec WorkflowSpec, callerID string) (jobstore.Workflow, error)
```
步骤：① 校验 `len(Steps)>=1`；逐 step 复用 `validate`（project/agent/runner 合法、exec gate）——构一个 `JobRequest{ProjectKey,Agent,Runner,Cmd,...}` 调既有校验逻辑（P1 不校验引用，P2 加）。② 生成 wfID（仿 jobID 生成）；`spec_json=marshal(spec)`；`InsertWorkflow(running, current_step=1, total=len)`。③ `stepToRequest(spec.Steps[0], wfID, 1, callerID)` → `Submit`；Submit 失败 → `SetWorkflowStatus(failed)` 并返回 err。④ 返回 workflow 快照。
- `stepToRequest(step, wfID, idx, caller)`：StepSpec → `JobRequest{...step 字段, WorkflowID:wfID, StepIndex:idx, CallerID:caller}`。

### P1-b 验收
- 单测 job：`SubmitWorkflow` 3-step → workflows 行 running/current_step=1/total=3，step1 job 已起（`ListWorkflowJobs` 含 step_index=1 的 job）；空 steps→err；非法 project/agent/runner→err（提交期拒）。

---

## P1-c advanceWorkflow（幂等推进）+ finish 钩子 + CancelWorkflow

### 落点 `internal/job/workflow.go` + `service.go` + `cancel.go`
**1) advanceWorkflow**（幂等核心）：
```go
// advanceWorkflow 在工作流当前 step 的 job 到终态后推进。幂等: 多次/并发调用(finish 钩子+
// sweeper)经 AdvanceCurrentStep 抢占, 只有一个真正推进。best-effort 包裹日志。
func (s *Service) advanceWorkflow(wfID string) {
    wf, ok, _ := s.meta.GetWorkflow(wfID); if !ok || wf.Status != "running" { return }
    jobs, _ := s.meta.ListWorkflowJobs(wfID)
    cur := stepJob(jobs, wf.CurrentStep)        // 找 step_index==current_step 的 job
    if cur == nil || !isTerminal(cur.Status) { return } // 还没起/没跑完
    if ok, _ := s.meta.AdvanceCurrentStep(wfID, wf.CurrentStep, wf.CurrentStep+1); !ok { return } // 抢推进权
    switch cur.Status {
    case StatusFailed, StatusTimeout, StatusCancelled:
        s.meta.SetWorkflowStatus(wfID, "failed", "step "+itoa(wf.CurrentStep)+" "+cur.Status)
    case StatusDone:
        if wf.CurrentStep >= wf.TotalSteps { s.meta.SetWorkflowStatus(wfID, "done", ""); return }
        var spec WorkflowSpec; json.Unmarshal([]byte(wf.SpecJSON), &spec)
        next := spec.Steps[wf.CurrentStep] // 0-based: 下一步是 index current_step(已是 cur+1 的 0-based)
        // P2: resolveRefs(&next, jobs) 把 ${steps.N} 替换 ; P1 直接用
        req := stepToRequest(next, wfID, wf.CurrentStep+1, wf.CallerID)
        if _, err := s.Submit(req); err != nil { s.meta.SetWorkflowStatus(wfID, "failed", "submit step: "+err.Error()) }
    }
}
```
> 注意 step 序号与 spec.Steps 下标：current_step 是 1-based，spec.Steps[current_step] 是"下一步"（current_step 抢占后语义上已指向下一步，实现时仔细对齐——建议 advance 内先读 `wf.CurrentStep`(=刚完成的步)，AdvanceCurrentStep(cur,cur+1) 抢占，再起 `spec.Steps[cur]`(0-based 第 cur+1 步)）。**实现者务必加注释并单测覆盖序号对齐**。
**2) finish 钩子**（`service.go` finish 末尾，recordEvent terminal 之后）：
```go
// 工作流推进(E7): step-job 到终态 → 异步推进所属工作流(绝不阻塞 finish/不改 entry.done 时序)。
if entry 的 result.WorkflowID != "" { go s.advanceWorkflow(workflowID) }
```
> WorkflowID 从 entry.result 取（在 finish 内已有 snap）。异步 `go`，best-effort。
**3) CancelWorkflow**（`cancel.go` 或 workflow.go）：
```go
// CancelWorkflow 标 cancelled(阻止后续推进) + 取消当前 running step 的 job。幂等。
func (s *Service) CancelWorkflow(wfID string) error
```
取 workflow（非 running→no-op 返回）；`SetWorkflowStatus(cancelled)`；找当前 step job→`Cancel(jobID)`（已终态则 no-op）。

### P1-c 验收
- 单测 job（**幂等是重点**）：3-step echo 工作流跑完 → 各 step 顺序 done、工作流 done、step 序号对（step1→2→3）。
- **并发幂等**：手动并发多次调 `advanceWorkflow(wfID)`（模拟 finish 钩子 + sweeper 同时触发）→ 下一 step 只被起一次（`ListWorkflowJobs` 该 step_index 恰 1 个 job）。`-count=20`+`-race`。
- fail-fast：step2 失败（exit≠0）→ step3 不起、工作流 failed。
- 取消：running 工作流 CancelWorkflow → 当前 step 取消、工作流 cancelled、后续不起。

---

## P1-d startWorkflowLoop sweeper（crash 兜底）

### 落点 `internal/commands/serve.go`（仿 startPruneLoop/startDeliveryLoop）
```go
// 扫 status='running' 工作流, 对其当前 step 已终态但未推进的补推(crash 恢复, SR304)。
func startWorkflowLoop(c, jobs *job.Service, stop <-chan struct{}) // ticker 如 30s, 启动跑一次
```
- `Service.AdvanceRunningWorkflows(ctx)`：`ListWorkflows("running", N)` → 逐个 `advanceWorkflow(wfID)`（幂等，与 finish 钩子叠加安全）。
- serve 启动序列挂载（prune/delivery loop 旁，`serve.go:92/99` 附近）；**始终启动**（工作流是核心能力，非可选配置）或仅当有 workflows——简单起见始终挂（开销低）。stop chan 优雅停机。

### P1-d 验收
- 单测：构造一个 running 工作流、其当前 step job 已 done 但未触发钩子（模拟 crash）→ `AdvanceRunningWorkflows` 把它推进。
- 回归：无 running 工作流时 sweep 空转无副作用。

---

## P1-e API

### 落点 `internal/httpapi/workflow_handler.go`（新）+ `server.go`
- `POST /v1/workflows`：`handleCreateWorkflow`——`BindJSON(&WorkflowSpec)`；`callerID=callerFromCtx(c)`；`SubmitWorkflow(spec, callerID)`；err→`writeError(submitStatus(err))`；ok→`c.JSON(200, wf)`。
- `GET /v1/workflows/{id}`：`handleGetWorkflow`——`GetWorkflow(id)`(404)；附 `ListWorkflowJobs` 的 step 摘要 `[{step_index,name,job_id,status}]`；`c.JSON`。
- `GET /v1/workflows`：`handleListWorkflows`——`?status=` 过滤；`c.JSON({workflows:[...]})`。
- `POST /v1/workflows/{id}/cancel`：`handleCancelWorkflow`——`CancelWorkflow(id)`(404 if unknown)；返回更新后快照。
- `server.go` `/v1` authed 组加 4 路由（仿 jobs 块 `:186-231`）。

### P1-e 验收
- 单测 httpapi：`POST /v1/workflows` 起工作流(200+快照)；`GET /v1/workflows/{id}` 含 step 列表；list 过滤；`cancel` 标 cancelled；未知 id→404；非法 spec→400。

### 提交点（SR1202）
P1-a / P1-b / P1-c / P1-d / P1-e 各绿灯分别 `git commit`；更新主纲进度 + 实施结果一行。**P1-c 触碰 finish + 并发推进，必须 `-count`+`-race` 验幂等不 flaky**。
