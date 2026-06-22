# P1 — 重试/失败策略 + 事件流（实施细化）

> 主纲：[`2026-06-22-workflow-v2-plan.md`](2026-06-22-workflow-v2-plan.md) · 设计 §5/§6.1/§6.2。
> 目标：per-step `on_failure`/`retry` + workflow 事件流 + retention，保留 v1 幂等骨架。**这是 v2 地基，也是自主化 epic 前置。**

## ⭐ 幂等核心（实现细化，比 design D16 更安全，务必照此实现）

retry 引入"同一 step 多次 attempt"。用**两层幂等**保证"每个 (step, attempt) 绝不起两个 job、状态转移绝不执行两次"：

1. **每个 step-job 确定性 request_id**（复用 C5 幂等 `service.go:200`）：`stepToRequest` 设 `RequestID = fmt.Sprintf("%s:s%d:a%d", wfID, stepIndex, attempt)`（P2 再加 `:f%d` fan_index）。→ 无论多少并发 Submit，C5 唯一索引保证**该 (step,attempt) 只起一个 job**。这是起 job 幂等的根本。
2. **(current_step, step_attempt) 二元组条件 UPDATE**（替代 v1 单值 `AdvanceCurrentStep`，升级为 `AdvanceStep`）：每次状态转移（推进/重试/继续）抢权一次，只有赢家执行后续决策。

> 为何不用 design 的"指针退回 cur+1→cur"：退回与抢权的并发交互易错。二元组方案里 retry = `(cur,att)→(cur,att+1)`、推进 = `(cur,att)→(cur+1,1)`，是**对称的同一种抢权**，更清晰；叠加 request_id 幂等双保险。**design D16 据此细化**。

## T1.1 StepSpec / RetryPolicy 字段 + 校验

**改动**：`internal/job/workflow.go`。StepSpec 加（design §5.1）：
```go
OnFailure string       `json:"on_failure,omitempty"` // ""/"fail"(=v1 fail-fast) | "continue" | "retry"
Retry     *RetryPolicy `json:"retry,omitempty"`
```
```go
type RetryPolicy struct {
	MaxAttempts int   `json:"max_attempts"`           // >=1（含首次）
	BackoffSec  []int `json:"backoff_sec,omitempty"`  // 默认接 SR606 [30,120,300,900,3600]
	OnExitCodes []int `json:"on_exit_codes,omitempty"`// 空=任意非 0 重试
}
```
校验（`SubmitWorkflow` 内，validateRefs 同级，新增 `validateRetry(spec)`）：`on_failure` ∈ 已知集；`on_failure=="retry"` 必须有 `retry.max_attempts>=1`；`max_attempts` 设上限（如 ≤10 防失控）；非 retry 不应带 retry 块（warn 或拒）。

**验收**：表驱动单测——合法/非法 on_failure、缺 retry 块、max 越界、向后兼容（无字段=fail）。

## T1.2 表迁移 + DAO

**改动**：`internal/jobstore/store.go` migrate() 加 ALTER ADD（幂等，IF NOT EXISTS 模式同 v1 `:260`）：
```sql
ALTER TABLE jobs      ADD COLUMN attempt        INTEGER;  -- 1-based,默认 1;旧行 COALESCE→1
ALTER TABLE workflows ADD COLUMN step_attempt   INTEGER;  -- 当前 step 的 attempt,默认 1
ALTER TABLE workflows ADD COLUMN next_step_at   INTEGER;  -- 退避到点时间(unix),0=立即
```
> `Workflow` struct(`workflows.go:35`) + `JobRecord`(`jobs.go:33`) 加对应字段；selectCols COALESCE 成 1/0 兼容旧行。

**新/改 DAO**（`internal/jobstore/workflows.go`）：
```go
// AdvanceStep 取代 v1 AdvanceCurrentStep：二元组(step,attempt)条件 UPDATE 抢权 + 设 next_step_at。
// 只有 (current_step,step_attempt,status) 全匹配的赢家 RowsAffected==1。
func (s *Store) AdvanceStep(id string, fromStep, fromAtt, toStep, toAtt int, nextStepAt int64) (bool, error)
//   UPDATE workflows SET current_step=?, step_attempt=?, next_step_at=?, updated_at=?
//    WHERE id=? AND current_step=? AND step_attempt=? AND status='running'
```
> 保留 v1 `AdvanceCurrentStep` 作 `AdvanceStep(.., att→1, 0)` 的特例，或直接替换全部调用点（v1 仅 advanceWorkflow 一处用）。`stepJob` → 新增 `stepJobAttempt(jobs, step, att)`（按 step_index+attempt 匹配）。

**验收**：迁移幂等（重复 migrate 不报错）；AdvanceStep 并发只 1 赢家（仿 v1 幂等并发测试 `workflow_test.go`）。

## T1.3 advanceWorkflow 失败分支改造（核心）

**改动**：`internal/job/workflow.go:155` advanceWorkflow。改造后骨架：
```go
func (s *Service) advanceWorkflow(wfID string) {
	wf, ok, _ := s.meta.GetWorkflow(wfID)
	if !ok || wf.Status != running { return }
	if wf.NextStepAt > s.nowFn().Unix() { return } // 退避未到,sweeper 下次再来

	jobs, _ := s.meta.ListWorkflowJobs(wfID)
	cur, att := wf.CurrentStep, wf.StepAttempt
	curJob := stepJobAttempt(jobs, cur, att)
	if curJob == nil {
		// 抢权后/退避到点但本 attempt job 未起(crash 或 retry 等待) → 起它(request_id 幂等兜底)
		s.startStepJob(wf, cur, att, jobs); return
	}
	if !isTerminal(curJob.Status) { return }

	spec := decodeSpec(wf.SpecJSON)
	step := spec.Steps[cur-1]
	switch curJob.Status {
	case StatusDone:
		if won,_ := s.meta.AdvanceStep(wfID, cur,att, cur+1,1, 0); !won { return }
		if cur >= wf.TotalSteps { s.setWorkflowDone(wfID); return }
		s.startStepJob(wf, cur+1, 1, jobs) // resolveRefs + Submit(下一步)
	default: // failed/timeout/cancelled
		if step.OnFailure == "retry" && att < maxAttempts(step) && retryableExit(step, curJob) {
			backoff := backoffFor(step, att)               // SR606 退避表索引 att
			next := s.nowFn().Unix() + int64(backoff)
			if won,_ := s.meta.AdvanceStep(wfID, cur,att, cur,att+1, next); !won { return }
			s.recordWorkflowEvent(wfID, "step.retry", ...) // 不立即起;到点 sweeper 起 att+1 job
		} else if step.OnFailure == "continue" {
			if won,_ := s.meta.AdvanceStep(wfID, cur,att, cur+1,1, 0); !won { return }
			s.recordWorkflowEvent(wfID, "step.skipped", ...)
			s.startStepJob(wf, cur+1, 1, jobs)
		} else { // fail(默认 fail-fast,=v1)
			s.setWorkflowFailed(wfID, fmt.Sprintf("step %d %s", cur, curJob.Status))
		}
	}
}
```
- `startStepJob(wf, step, att, priorJobs)`：抽出 v1 的 resolveRefs + stepToRequest + Submit；**stepToRequest 设确定性 request_id**（⭐ 幂等核心 1）。
- `maxAttempts(step)` = `step.Retry.MaxAttempts`；`backoffFor` 索引退避表（越界取末值）；`retryableExit` 查 `OnExitCodes`。
- sweeper（`AdvanceRunningWorkflows` 不动）天然驱动：retry 设 next_step_at 后，下次 tick（≤30s）到点 → curJob(att+1) 不存在 → startStepJob 起新 attempt。
- `setWorkflowFailed/Done` 复用 v1 `SetWorkflowStatus` + recordWorkflowEvent。

**验收**（含**并发硬测**）：
- on_failure=fail → 失败即工作流 failed（v1 回归不变）。
- on_failure=retry/max=3 → 失败后退避重投，第 3 次仍失败→工作流 failed；**并发调 advanceWorkflow（模拟 finish+sweeper 同时）只起一个 att+1 job**（request_id + AdvanceStep 双保险硬测）。
- on_failure=continue → 失败跳过进下一步。
- 退避时间随 attempt 递增（注入 nowFn 验证 next_step_at）。

## T1.4 统一 job 级重试（E24，最小版）

**改动**：`internal/job/service.go` finish。普通 job（`WorkflowID==""`）的 JobRequest 加 `Retry *RetryPolicy`；finish 失败且 attemptsLeft 时延迟重投：
```go
if snap.WorkflowID == "" && snap.Retry != nil && attemptLeft(snap) && retryableExit(snap) {
	// 最小版:进程内延迟重投(time.AfterFunc),复用 request_json,attempt+1,新 job 关联原 id 链。
	// 共用 RetryPolicy/backoffFor/retryableExit(与 step 级同一套,roadmap 横切要求)。
}
```
> **范围注记**：P1 job 级用进程内延迟（重启丢失，容忍度高）；**可靠版（sweeper 驱动 `next_retry_at`）留后续**，或引导用户用"单 step 工作流 + retry"获得可靠重试。主力价值在 step 级（T1.3）。plan 明示此取舍。

**验收**：单 job 带 retry 失败后自动重投 attempt+1；不带 retry 行为不变（向后兼容）。

## T1.5 workflow_events 表 + 事件流（仿 E13 job_events）

**改动**：`internal/jobstore/store.go` 加表（design §5.4）；`internal/job/` 加 `recordWorkflowEvent(wfID, type, detail)`（仿 `recordEvent` `events.go`）。插桩点：`workflow.submitted`（SubmitWorkflow）、`step.started`（startStepJob）、`step.retry`/`step.skipped`（advanceWorkflow 分支）、`workflow.terminal`（setWorkflowDone/Failed）、`workflow.cancelled`（CancelWorkflow）。
- API：`GET /v1/workflows/{id}/events`（仿 `handleListEvents`）。
- retention：prune 连带删（T1.6）。

**验收**：跑一个含 retry 的工作流，events 时间线含 submitted→step.started→step.retry→...→terminal，顺序/seq 正确。

## T1.6 workflow retention（独立 max_age + 连带清）

**改动**：`internal/job/prune.go` + `jobstore/prune.go`。config `RetentionConfig` 加 `WorkflowMaxAgeDays`（或复用 job 策略）；prune 时清终态 workflow（done/failed/cancelled）超龄者 + **连带删其 step-jobs + workflow_events**（避免悬挂）。复用现有 prune sweeper（`startPruneLoop` `serve.go:164`）。

**验收**：超龄终态 workflow 被清，其 step-jobs/events 一并删；running workflow 不清；无悬挂引用。

## P1 阶段验收清单（全绿即进 P2）

- [ ] `go build ./... && go test ./internal/job ./internal/jobstore ./internal/httpapi` 绿
- [ ] **D23 向后兼容**：v1 spec（无 on_failure/retry）跑通，行为与改造前一致（回归）
- [ ] on_failure fail/continue/retry 三态走对；retry 退避递增
- [ ] **并发硬测**：finish+sweeper 同时推进，retry 只起一个 att+1 job（request_id + AdvanceStep 双保险）
- [ ] workflow_events 时间线完整；retention 连带清无悬挂
- [ ] git 提交（SR1202）：`feat(workflow-v2): P1 per-step 重试/失败策略 + workflow 事件流 + retention`
