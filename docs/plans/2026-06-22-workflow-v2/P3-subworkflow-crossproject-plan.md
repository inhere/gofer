# P3 — 子工作流嵌套 + 跨项目产物（实施细化）

> 主纲：[`2026-06-22-workflow-v2-plan.md`](2026-06-22-workflow-v2-plan.md) · 设计 §5/§6.1（D18/D19/D20）。依赖 P2。
> 目标：工作流作为另一工作流的一个 step（嵌套）+ 跨项目衔接（开发→测试）。

## 核心机制
- step `type="workflow"` 时，该 step 不起 step-job，而是**提交一个子工作流**（内联 `sub_workflow` 定义，D18）。
- 子工作流加 `parent_workflow_id`/`parent_step_index`；其终态时按 parent_* **触发父 advanceWorkflow**（D19）。
- 父 advanceWorkflow 判定该 step 终态 = **子 wf 是否终态**（而非 step-job）。

## T3.1 StepSpec type/sub_workflow + parent_* 列 + 校验

**改动**：`workflow.go` StepSpec 加（design §5.1）：
```go
Type        string        `json:"type,omitempty"`        // ""/"job"(默认) | "workflow"
SubWorkflow *WorkflowSpec `json:"sub_workflow,omitempty"`// type=workflow 时内联子工作流
```
`jobstore`：`workflows` 加 `parent_workflow_id TEXT` / `parent_step_index INTEGER`（P1 已加 step_attempt/next_step_at，本阶段加 parent_*）。`Workflow` struct + selectCols 兼容。
校验（`SubmitWorkflow`）：`type=="workflow"` 必须有非空 `sub_workflow`（且其 steps 非空）；`type=="job"` 不应带 sub_workflow；**递归校验子工作流**（validateRefs/validateRetry/validateFanout + 每 step 过单 job 准入——嵌套不绕过 allowlist/exec gate，§9 安全）；限**嵌套深度**（如 ≤3 防无限/失控）。

**验收**：type=workflow 缺 sub_workflow 拒；子 wf 非法 step 拒（嵌套准入）；超深度拒；向后兼容（无 type=job）。

## T3.2 子 wf 提交 + 终态触发父

**改动**：`internal/job/workflow.go`。
- `startStepJob` 分流：`step.Type=="workflow"` → `startSubWorkflow(wf, cur, att, step.SubWorkflow)`：
```go
func (s *Service) startSubWorkflow(parent Workflow, step, att int, sub *WorkflowSpec) {
	// 提交子 wf,带 parent 绑定 + 确定性 wfID(复用 request_id 思路:parentWfID:s%d:a%d 防重复提交)
	s.SubmitWorkflowChild(*sub, parent.CallerID, parent.ID, step)
}
```
- `SubmitWorkflow` 抽出 `submitWorkflowImpl(spec, caller, parentID, parentStep)`；公开版 parent 为空，子版带 parent。子 wf id 确定性派生（防 sweeper 并发重复提交子 wf）。
- 子 wf 终态钩子：`setWorkflowDone`/`setWorkflowFailed` 末尾加——若 `wf.ParentWorkflowID != ""` → `go s.advanceWorkflow(wf.ParentWorkflowID)`（仿 finish 的 `go advanceWorkflow`，异步、幂等）。
- 父 advanceWorkflow 的 step 终态判定分流：`step.Type=="workflow"` → 查该 step 对应的子 wf 状态（按 parent_workflow_id+parent_step_index 找子 wf）是否终态，而非 stepFanJobs。子 wf done→step done；子 wf failed/cancelled→step failed（再走 on_failure）。

> sweeper 兜底天然覆盖：子 wf 终态但父 advance 漏触发时，父仍是 running，sweeper 下次扫到父→advanceWorkflow→查子 wf 已终态→推进。

## T3.3 跨项目产物澄清 + 远端警示（D20）

- **代码**：StepSpec.ProjectKey 已支持每 step 不同项目（v1 即有）。`${steps.N.result_dir}` 是绝对路径——**本地 runner 同容器文件系统下，跨项目 step 直接可读**（开发项目产物→测试项目读），无需改代码，加**集成测试**验证跨项目线性传值。
- **警示**：fan-out/跨项目 step 用 **worker 远端**执行时，result_dir 在 worker 机，跨机不可读 → `resolveRefs` 解析到远端 result_dir 时**文档警示**（README + 工作流详情提示"跨机传值用共享盘或改 inline result/stdout"）；远端产物拉取通道留后续（roadmap）。

**验收**：3-step 跨项目工作流（projA 生成→projB 读 result_dir→projC）本地传值通；远端跨机场景文档警示到位。

## T3.4 验收清单（全绿即进 P4）

- [ ] `go build ./... && go test ./internal/job ./internal/jobstore ./internal/httpapi` 绿
- [ ] 子工作流嵌套跑通：父 step=workflow→提交子 wf→子 wf 终态→父推进
- [ ] 子 wf 失败 → 父 step 按 on_failure 处理（fail/continue/retry）
- [ ] 嵌套准入：子 wf 每 step 过 allowlist/exec gate，超深度拒
- [ ] sweeper 兜底：父 advance 漏触发时下次扫到补推进
- [ ] 跨项目本地传值通；远端警示到位
- [ ] **向后兼容**：无 type 的 v1 工作流行为不变
- [ ] git 提交：`feat(workflow-v2): P3 子工作流嵌套 + 跨项目产物`
