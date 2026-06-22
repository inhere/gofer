# P2 — 单 step 并行 fan-out + join（实施细化）

> 主纲：[`2026-06-22-workflow-v2-plan.md`](2026-06-22-workflow-v2-plan.md) · 设计 §5/§6.1（D14/D15）。依赖 P1。
> 目标：一个 step 并行派 N 个 job，按 join 策略聚合再推进。**保留 current_step 单值指针**（fan-out 是 step 内多 job，不破幂等屏障）。

## 核心机制
- fan-out 的 N 个 job 共享 `(step_index, attempt)`、以 **`fan_index`(1..N)** 区分。
- step-job 确定性 request_id 扩为 `wfID:s%d:a%d:f%d`（加 fan_index）→ C5 幂等保证每个 (step,attempt,fan) 只起一个 job（P1 ⭐ 幂等核心延续）。
- `current_step`/`step_attempt` 单值不变；step 终态判定从"单 curJob"改为"该 (step,attempt) 的全部 fan job 按 join 聚合"。

## T2.1 StepSpec fan_out/join + 校验 + fan_index 列

**改动**：`workflow.go` StepSpec 加（design §5.1）：
```go
FanOut int    `json:"fan_out,omitempty"` // >1 该 step 并行 N job;默认 0/1=单 job(=v1)
Join   string `json:"join,omitempty"`    // "all"(默认)|"any"|"quorum"
```
`jobstore`：`jobs` 加 `fan_index INTEGER`（P1 已加 attempt，本阶段加 fan_index；或 P1 一并加）。`JobRecord` + selectCols 兼容（旧行=0）。
校验（`validateRetry` 同级 `validateFanout`）：`fan_out>=0`、设上限（如 ≤32 防失控 + 接 E17 配额）、`join` ∈ 已知集、`fan_out<=1` 时不应带 join。

**验收**：表驱动——fan_out 越界、join 非法、向后兼容（无字段=单 job）。

## T2.2 startStep 起 N 并行 job + advanceWorkflow 多 job 聚合

**改动**：`internal/job/workflow.go`。
- `startStepJob` → 支持 fan：`FanOut>1` 时循环 Submit N 个（fan_index=1..N，各自确定性 request_id），否则单个（fan_index=0/1）。N 个 job 继承同 caller_id → **受 E17 per-caller 配额约束**（并发自动排队，不打爆）。
- advanceWorkflow 的 step 终态判定改造：
```go
fanJobs := stepFanJobs(jobs, cur, att)        // 该 (step,attempt) 的全部 fan job
want := max(1, step.FanOut)
if !fanTerminal(fanJobs, want, step.Join) { return }  // 未达 join 聚合终态,等
// 抢权(AdvanceStep 不变)后,按 join 评决 step 整体 done/failed
verdict := fanVerdict(fanJobs, want, step.Join)
```
- `fanTerminal(fanJobs, want, join)`：判定是否已可决（all/quorum 需足够多终态；any 任一 done 即可，其余在跑也算可决——可选取消其余 in-flight fan job 省资源）。
- `fanVerdict` → done/failed，再走 P1 的 on_failure 分支（fail/continue/retry）。**retry 重试整个 step**（attempt+1 重起全部 N 个 fan，简化；只重试失败 fan 留后续）。

## T2.3 join 语义 + 引用聚合

- **join 判定**（`fanVerdict`）：`all`=全 done→done/任一失败→failed；`any`=任一 done→done（其余 best-effort cancel）；`quorum`=过半 done→done。
- **引用聚合**（`refs.go` `resolveOne`）：当被引用 step N 是 fan-out，`${steps.N.result_dir}` 默认返回**全部成功 fan 的 result_dir 换行连接**；新增 `${steps.N.fK.result_dir}` 语法指定第 K 个 fan（K=fan_index）。inline result/stdout 聚合超 32KB 仍报错引导用 result_dir（P1 上限不变）。validateRefs 兼容 fan 语法。

**验收**：fan_out=3/join=all→1 个失败则 step failed；join=any→1 个 done 即推进；join=quorum→2/3 done 推进；`${steps.N.result_dir}` 返回多目录、`${steps.N.f2...}` 取指定 fan。

## T2.4 验收清单（全绿即进 P3）

- [ ] `go build ./... && go test ./internal/job ./internal/jobstore ./internal/httpapi` 绿
- [ ] fan_out=N 并行起 N job（fan_index 1..N），request_id 各异、C5 幂等不重复
- [ ] join all/any/quorum 判定正确；幂等屏障（current_step/attempt）不破（并发硬测）
- [ ] fan-out + retry 组合：失败 step 整体重试 attempt+1 重起 N fan
- [ ] fan-out step-job 受 E17 caller 配额约束（并发排队，不超额打爆）
- [ ] 引用聚合 `${steps.N.result_dir}` / `${steps.N.fK...}` 正确
- [ ] **向后兼容**：无 fan_out 的 v1 工作流行为不变
- [ ] git 提交：`feat(workflow-v2): P2 单 step 并行 fan-out + join(all/any/quorum)`
