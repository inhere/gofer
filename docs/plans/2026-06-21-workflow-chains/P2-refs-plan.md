# P2 — `${steps.N}` 跨 step 传值（实施计划）

> 主纲：[`2026-06-21-workflow-chains-plan.md`](./2026-06-21-workflow-chains-plan.md) · 设计 §5.4/§7/§9。
> 让"上一步产出喂下一步"真正成立：引用文法 + 解析器（前序产出替换进下一步 prompt/cmd/cwd/env）+ 校验。依赖 P1。

---

## P2-a `${steps.N.field}` 解析器 + 接入推进

### 落点 `internal/job/refs.go`（新）+ `workflow.go`（advanceWorkflow 接入）
**文法**：`${steps.<N>.<field>}`，N=1-based 前序 step 序号，field ∈ `result_dir | result | stdout | exit_code | status | job_id`。
```go
const maxRefInlineBytes = 32 * 1024
// resolveRefs 把 step 规格里所有 ${steps.N.field} 替换为前序 step 的产出。
// priorJobs: 该工作流已完成的 step-job(按 step_index)。逐字段(prompt/cmd各元素/cwd)替换,
// 不 shell 重切(仿 agent.Render)。缺产出/超上限 → 返回 error(运行期 → 该 step 提交失败 → 工作流 failed)。
func (s *Service) resolveRefs(step *StepSpec, priorJobs []jobstore.JobRecord) error
// resolveOne(ref) 取 ${steps.N.field} 的值: 
//   result_dir → prior.ResultDir; result → 读 prior 的 ResultJSON(≤32KB,否则 err 提示用 result_dir);
//   stdout → 读 prior stdout tail(≤32KB); exit_code/status/job_id → 标量。
```
- 取前序 job 产出复用 `Get`/`ResultJSON`/`ResultDir`/`ExitCode`/`Status` + stdout 经 store ReadLogTail（recon ③ 的最短路径）。
- **接入**：`advanceWorkflow` 起下一 step 前（P1 留的注释点）：`if err := s.resolveRefs(&next, jobs); err != nil { SetWorkflowStatus(failed, ...); return }`。

### 实现注意（正则/扫描）
- 用正则 `\$\{steps\.(\d+)\.(\w+)\}` 全局匹配替换每个字符串字段；N/field 校验在替换时再确认（提交期已校验静态部分）。
- 替换值原样插入（不转义）——逐字段、不进 shell（exec argv 逐元素；cli-agent prompt 是单字符串）。

### P2-a 验收
- 单测 `resolveRefs`：`${steps.1.result_dir}` → 前步 ResultDir；`${steps.1.exit_code}`/`status`/`job_id` → 标量；`${steps.2.stdout}` → 前步 stdout；`${steps.1.result}` → result.json 原文；一个字段多引用、prompt+cmd 混合替换正确。
- 上限：result/stdout 超 32KB → err（提示 result_dir）。
- 缺产出：引用 `result` 但前步无 result.json → err。

---

## P2-b 提交期引用校验 + 端到端

### 落点 `internal/job/workflow.go`（SubmitWorkflow 内）
- 提交期静态校验（不需运行）：扫每 step 的字符串字段的 `${steps.N.field}`——
  - **N 必须 < 当前 step 序号**（拒自引用、未来引用）；N≥1；field ∈ 合法集；否则 `ErrInvalidRequest`（400）。
  - step1 不得含任何 `${steps.N}`（无前序）。
```go
// validateRefs 在 SubmitWorkflow 校验阶段调用: 逐 step 扫引用, N<本step序号 && field合法。
func validateRefs(spec WorkflowSpec) error
```
- 端到端：advanceWorkflow 接 resolveRefs 后，真实链路 `gen→test(用 ${steps.1.result_dir})→review(用 ${steps.2.stdout/exit_code})` 跑通。

### P2-b 验收
- 单测：合法引用 spec 提交通过；`${steps.2.x}` 出现在 step2（自引用）→400；`${steps.3.x}` 在 step2（未来）→400；`${steps.1.bogus}`（非法 field）→400；step1 含引用→400。
- 集成（job 包，真实 exec）：3-step 工作流 step2 cmd 用 `${steps.1.result_dir}`、step3 prompt 用 `${steps.2.exit_code}` → 替换后各 step 实跑、值正确（断言 step2/3 的 rendered_command/request 含替换后的真实路径/码）。
- 运行期缺产出：step 引用 `${steps.1.result}` 但 step1 没写 result.json → step 提交失败 → 工作流 failed（带清晰 error）。

### 提交点
P2-a / P2-b 各绿灯分别 `git commit`；更新主纲进度 + 实施结果一行。

> 范围注记：`${}`(工作流跨 step) 与 `{{}}`(agent.Render job 内) 正交——`${}` 先在 advanceWorkflow 解析、再常规 Submit（job 内 `{{}}` 照旧）。大数据走 `result_dir`（路径），`result`/`stdout` inline 仅小结果。
