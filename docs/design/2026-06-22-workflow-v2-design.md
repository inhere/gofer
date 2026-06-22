# Gofer 工作流 v2 设计（E7 完整化：重试/并行/子工作流/跨项目）

> 一句话：在 v1「线性 chain + `${steps.N}` 引用 + fail-fast」基础上，**additive** 加上 **per-step 重试/失败策略(E24)、并行 fan-out(E9)、子工作流嵌套+跨项目(E27)、workflow 事件流/retention、导入导出(E18)**，让工作流从"玩具"到"生产可用"。
> 设计依据 roadmap [`../2026-06-20-enhancements-roadmap.md`](../2026-06-20-enhancements-roadmap.md) §工作流 v2；承接 v1 设计 [`2026-06-21-workflow-chains-design.md`](2026-06-21-workflow-chains-design.md)（决策 D1-D12 不复述，本文新决策从 **D13** 起）。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-06-22 | inhere | 初稿：E7 尾巴 + E9 + E24 + E27 + E18 合并设计，待审核 |
| v0.2 | 2026-06-22 | inhere | §8 决策点 **D13–D23 全部按推荐定稿**（D13 线性+fan-out+子工作流不做通用DAG / D14 同step多job+fan_index / D15 join=all默认 / D16 同step新attempt+指针退回+延迟退避 / D17 on_failure fail默认 / D18 内联子工作流 / D19 parent_*触发父advance / D20 本地直读+远端共享盘 / D21 workflow_events表 / D22 独立retention / D23 全additive向后兼容）；进入 plan 阶段 |

## 2. 核心取向（先对齐，最重要）

**保留 v1 引擎骨架，v2 能力全部 additive 挂在其上——不重写成通用 DAG 引擎。**

| v1 不变量（保留） | v2 如何在其上扩展 |
|---|---|
| `current_step` 单值线性指针 | 仍指"当前 step"；fan-out 是**一个 step 对应多个 job**（指针不变，job 聚合判定） |
| `AdvanceCurrentStep(cur,cur+1)` 条件 UPDATE 幂等屏障 | **完全不动**——仍是"一个 step 绝不推进两次"的唯一真源 |
| finish 钩子 + sweeper 双触发，幂等 | 不动；子工作流终态、重试重投都复用同一推进入口 |
| StepSpec ≈ 单 JobRequest | additive 加字段（`type`/`on_failure`/`retry`/`fan_out`），**默认值 = v1 行为** |
| spec_json 自包含、推进时重建 step | 不动；子工作流定义内联进 spec_json |

**收益**：v1 工作流零改动继续跑（D23 向后兼容）；幂等/crash 恢复这套最难写对的并发逻辑**不重写**；每个 v2 能力可独立分阶段上线。
**代价**：不支持任意菱形 DAG（菱形依赖留更后）——但"线性链 + step 内 fan-out + 子工作流嵌套"已覆盖绝大多数真实场景（生成→并行评审→汇总→测试）。

## 3. 范围

**做**：① per-step 重试/失败策略(`on_failure`+`retry`) + 统一 job 级重试(E24)；② 单 step 并行 fan-out + join(E9)；③ 子工作流嵌套(workflow-as-step) + 跨项目产物澄清(E27)；④ workflow 级事件流 + retention(E7 尾巴)；⑤ 工作流导入导出 + md-per-step(E18 + E7 尾巴)。

**不做**：
- **不做** 通用菱形 DAG（任意前驱依赖图）——v2 是"线性主链 + step 内并行 + 子工作流"，菱形留 v3。
- **不做** 循环/条件跳转(while/if-goto)——失败策略仅 fail/continue/retry，无任意分支。
- **不做** 跨工作流**引用**其它工作流的中间 step 产出（子工作流是黑盒，只暴露其整体产出）。
- **不做** worker 远端跨机产物自动拉取（跨项目产物依赖共享文件系统；远端拉取通道留后续，见 D20）。
- **不做** 人审批门(E8，相邻 epic)、自动驾驶(自主化 epic)。

## 4. 已确认事项（v1 现状，复用不重造）

| 能力 | v1 挂点 | v2 复用 |
|---|---|---|
| 推进引擎 | `advanceWorkflow`(`workflow.go:155`) | 扩 step 终态判定（单 job→多 job 聚合）+ 失败分支（fail-fast→策略） |
| 幂等屏障 | `AdvanceCurrentStep`(`jobstore/workflows.go:150`) | **不动** |
| 终态触发 | `finish`(`service.go:634` `go advanceWorkflow`) | 不动；子 wf 终态额外触发父 advance |
| crash 兜底 | `AdvanceRunningWorkflows` sweeper(`workflow.go:271`) | 不动；自动覆盖新能力（重试重投、fan-out 半完成） |
| 引用解析 | `validateRefs`/`resolveRefs`(`refs.go:44/84`) | fan-out 引用聚合产出；子 wf 产出引用 |
| 提交校验 | `SubmitWorkflow`(`workflow.go:49`) | 加 retry/fan-out/子 wf 校验 |
| 表结构 | `workflows`(`store.go:141`) + `jobs.workflow_id/step_index` | additive ALTER ADD 新列；新 `workflow_events` 表 |
| 配额/观测 | E16 metrics / E17 配额（本轮刚落地） | 工作流级指标 + step-job 受 caller 配额约束（自主能力天然受限） |

## 5. 数据模型（全部 additive）

### 5.1 StepSpec 扩展（`workflow.go:19`）
```go
type StepSpec struct {
    // ... v1 字段不变 ...
    Type        string       `json:"type,omitempty"`         // "job"(默认) | "workflow"
    OnFailure   string       `json:"on_failure,omitempty"`   // "fail"(默认,=v1 fail-fast) | "continue" | "retry"
    Retry       *RetryPolicy `json:"retry,omitempty"`        // on_failure=retry 时生效
    FanOut      int          `json:"fan_out,omitempty"`      // >1 时该 step 并行 N 个 job；默认 0/1=单 job
    Join        string       `json:"join,omitempty"`         // fan-out 汇聚:"all"(默认)|"any"|"quorum"
    SubWorkflow *WorkflowSpec `json:"sub_workflow,omitempty"`// type=workflow 时内联子工作流定义
}
type RetryPolicy struct {
    MaxAttempts int   `json:"max_attempts"`          // 含首次,>=1
    BackoffSec  []int `json:"backoff_sec,omitempty"` // 退避表,默认接 SR606 [30,120,300,900,3600]
    OnExitCodes []int `json:"on_exit_codes,omitempty"`// 仅这些退出码重试;空=任意非0(慎,见 D16)
}
```
> 全部 omitempty + 默认值 = v1 行为：老 spec 反序列化后 `Type=""`(当 job)、`OnFailure=""`(当 fail)、`FanOut<=1`(单 job)。

### 5.2 workflows 表新列（ALTER ADD）
```sql
ALTER TABLE workflows ADD COLUMN parent_workflow_id TEXT;   -- 子工作流:父 wf id(顶层为空)
ALTER TABLE workflows ADD COLUMN parent_step_index  INTEGER;-- 子工作流:在父中的 step 序号
```
> `current_step`/`status` 语义不变。子工作流终态时按 parent_* 触发父 advance（D19）。

### 5.3 jobs 表新列（ALTER ADD）
```sql
ALTER TABLE jobs ADD COLUMN fan_index INTEGER;  -- fan-out:同 step 内第几个并行 job(1-based;非 fan 为 0)
ALTER TABLE jobs ADD COLUMN attempt   INTEGER;  -- 重试:第几次尝试(1-based;默认 1)
```
> fan-out 的 N 个 job 共享 `step_index`、以 `fan_index` 区分；重试的 job 共享 `step_index`/`fan_index`、以 `attempt` 区分。**幂等屏障 current_step 仍单值**，只是 step 终态判定改为"聚合该 step 全部 job"。

### 5.4 新表 workflow_events（仿 job_events，E13 范式）
```sql
CREATE TABLE workflow_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, workflow_id TEXT NOT NULL, seq INTEGER NOT NULL,
  event_type TEXT NOT NULL, detail_json TEXT, created_at INTEGER NOT NULL );
```
> 事件：`workflow.submitted`/`step.started`/`step.fanout`/`step.retry`/`step.failed`/`subworkflow.started`/`workflow.terminal`。append-only，retention 连带清。

## 6. 关键流程

### 6.1 advanceWorkflow 改造（核心，`workflow.go:155`）
单 job 判定 `stepJob(jobs, cur)` → **聚合该 step 全部 job**：
```
cur := wf.CurrentStep
stepJobs := jobsOfStep(jobs, cur)              // 该 step 的全部 job(含 fan_index/attempt)
if !stepTerminal(stepJobs, step.FanOut, step.Join) { return }  // 未到聚合终态,等
won := AdvanceCurrentStep(cur, cur+1); if !won { return }       // 幂等屏障(不动)
verdict := evalStep(stepJobs, step.Join)       // done / failed (按 join: all/any/quorum)
switch verdict {
case failed:
   if step.OnFailure=="retry" && attemptsLeft(stepJobs, step.Retry) {
       rollbackCurrentStep(cur)                 // 把 current_step 退回 cur(重新激活本步)
       scheduleRetry(step, cur, nextAttempt, step.Retry.BackoffSec)  // 延迟重投(走 sweeper/延迟)
   } else if step.OnFailure=="continue" {
       startNextStepOrDone(cur)                 // 跳过失败,推进下一步
   } else { SetWorkflowStatus(failed) }         // 默认 fail-fast(v1 行为)
case done:
   startNextStepOrDone(cur)                     // = v1 逻辑(resolveRefs + Submit 下一步;末步→done)
}
```
> **重试的幂等**：`AdvanceCurrentStep(cur,cur+1)` 抢权后,retry 分支要把指针**退回** cur(条件 UPDATE `cur+1→cur`,只赢家退);重投的新 attempt job 终态再触发 advance。退避用现有延迟机制(或 sweeper 到点重投)。这是 v2 唯一对幂等屏障的**新交互**,需重点测并发(D16)。
> **fan-out 起 job**：`startStep(cur)` 时若 `FanOut>1`,循环 Submit N 个(fan_index=1..N) 而非 1 个;`${steps.N}` 引用 fan-out 步的产出时按 join 语义聚合(D14)。
> **子工作流 step**：`Type=="workflow"` 时 startStep 改为 `SubmitWorkflow(step.SubWorkflow, parent=wf/cur)`;子 wf 终态(其 advanceWorkflow 末步)按 `parent_workflow_id` 触发父 advance(D19)。父侧"该 step 是否终态"= 子 wf 是否终态。

### 6.2 统一 job 级重试(E24，非工作流 job)
普通 job(`WorkflowID==""`)的 `JobRequest` 也可带 `Retry *RetryPolicy`。`finish` 失败时:若 attemptsLeft → 延迟重投(attempt+1,新 job 复用 request_json,关联原 job id 链)。**与 step 级共用同一 RetryPolicy 类型 + 同一退避/重投实现**(避免两套语义,roadmap 横切要求)。默认 nil=不重试(向后兼容)。

### 6.3 跨项目产物(D20)
`StepSpec.ProjectKey` 已支持每步不同项目。`${steps.N.result_dir}` 是**绝对路径**——本地 runner 同容器文件系统下,跨项目 step 直接可读(开发项目产物→测试项目读)。**约束**:fan-out/跨项目 step 用 worker 远端执行时,result_dir 在 worker 机,跨机不可读 → 文档明示"跨机传值用共享盘,否则改用 inline result/stdout(32KB)";远端产物拉取通道留后续。

## 7. 分阶段实施

| 阶段 | 内容 | 依赖 | 验收要点 |
|---|---|---|---|
| **P1 重试/失败策略 + 事件流** | StepSpec `on_failure`/`retry` + 统一 job 级重试(E24) + `attempt` 列 + `workflow_events` 表 + retention | — | step 失败按 fail/continue/retry 走对;retry 退避重投且幂等(并发不双投);job 级重试同语义;workflow_events 可查 |
| **P2 fan-out 并行** | StepSpec `fan_out`/`join` + `fan_index` 列 + advanceWorkflow 多 job 聚合 + 引用聚合 | P1 | 一 step 并行 N job,join=all/any/quorum 判定正确;current_step 幂等不破;`${steps.N}` 聚合产出 |
| **P3 子工作流 + 跨项目** | StepSpec `type=workflow`/`sub_workflow` + `parent_*` 列 + 子 wf 终态触发父 + 跨项目产物澄清 | P2 | 子工作流嵌套跑通+终态回传父;跨项目线性传值(本地)验证;远端跨机文档警示 |
| **P4 易用** | E18 导入导出(spec_json dump/load) + md-per-step 提交 + CLI/Web 展示(并行/嵌套/重试/事件) | P3 | 导出再导入复现;Web 详情显示 fan-out/子 wf/重试 attempt;CLI 子命令完整 |

> 顺序：P1 失败策略是地基(也是自主化 epic 的前置)→ P2 并行(最大新能力)→ P3 嵌套(最复杂)→ P4 顺手。每阶段绿灯即提交(SR1202)。**P1/P2 可独立交付价值**,P3/P4 视需要推进。

## 8. 待确认决策点（审核重点，附推荐）

- **D13 并行模型**：线性链 + step 内 fan-out + 子工作流（推荐）vs 通用 DAG。→ 推荐前者（保 v1 骨架，覆盖主场景）。
- **D14 fan-out 数据模型**：同 step_index 多 job + `fan_index` 区分（推荐）vs 交叉表 workflows_steps。→ 推荐前者（current_step 单值不破，改动小）。
- **D15 fan-out join**：`all`(默认)/`any`/`quorum` 可配（推荐）。引用聚合:`${steps.N.result_dir}` 取全部 fan job 的目录列表/或主 job。→ 推荐 join=all 默认,引用聚合语义在 P2 细化。
- **D16 重试模型**：同 step 起新 attempt job(保留历史可审计)+ 指针退回重激活 + 延迟退避（推荐）。⚠️ **幂等坑**:退回 current_step 与抢推进权的并发交互必须重点测;`on_exit_codes` 默认仅非 0 重试,改文件的 agent 慎重试(默认 nil=不重试)。
- **D17 失败策略**：`on_failure` ∈ fail(默认 fail-fast)/continue/retry;retry 耗尽落 fail（推荐）。
- **D18 子工作流定义**：内联 `sub_workflow`(spec 自包含,推荐) vs 引用命名工作流(留后续,接 E18 模板库)。
- **D19 子工作流推进**：子 wf 加 `parent_workflow_id`/`parent_step_index`,终态时触发父 advance(推荐);父 step 终态判定=子 wf 终态。
- **D20 跨项目产物**：本地同文件系统绝对路径直读(推荐);worker 跨机用共享盘/inline,远端拉取留后续。
- **D21 workflow 事件流**：新 `workflow_events` 表,仿 job_events(推荐做,审计价值)。
- **D22 workflow retention**：独立 `max_age` + prune 连带 step-jobs(推荐) vs 跟随 job prune。
- **D23 向后兼容**：StepSpec 新字段全 additive 默认 = v1 行为;表 ALTER ADD;v1 工作流零改动(推荐,硬约束)。

## 9. 安全 / 观测

- step-job 全部经单 job 准入(`validate`)——子工作流的每个 step **同样**逐个过准入(SubmitWorkflow 已有范式),嵌套不绕过 allowlist/exec gate。
- 工作流的所有 step-job 继承 `caller_id` → 受 **E17 per-caller 配额**约束(并发/速率),fan-out 大批量并行天然受限,不会打爆。
- 工作流级指标(E16 延伸):`gofer_workflows_terminal_total{status}` + `gofer_workflow_duration_seconds`(P4 顺带)。
- secret 不入 spec_json 导出(E18 导出时剥离,SR403)。

## 10. 结论

v2 **保留 v1 幂等推进骨架不重写**,所有能力 additive 挂载,改动集中在 `internal/job/workflow.go`(推进分支) + `refs.go`(聚合引用) + `jobstore`(列+事件表) + `httpapi`/`commands`(展示)。建议按 §7 P1→P4 推进,P1/P2 即可独立交付主要价值。审核通过 §8 决策点(尤其 **D13 并行模型 / D16 重试幂等**)后出分阶段 plan。
