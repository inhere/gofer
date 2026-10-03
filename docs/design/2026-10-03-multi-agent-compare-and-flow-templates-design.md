<!-- template_id: design; template_version: 1.1.1 -->
# 多 agent 对比择优 + 预置流程模板（Z 批）

> 状态：Approved（Draft 0.1；用户 2026-10-03 在 web 中继确认，分两期：一期 Z1–Z3，二期 Z4）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-03 | Claude | 借鉴 Orca 扇出对比、Paseo handoff / committee、magpie 对抗评审 |

## 背景

同类工具调研（`docs/refer/2026-10-03-agent-orchestrators-survey.md`）中价值最高的两项：同一任务交给多个 agent 各做一份再择优；以及"规划→实现""多评审+验证者"这类多 agent 流程一键复用。gofer 已有的基础：

- workflow：串行步骤、步内 `fan_out` + `join: all|any|quorum`、`${steps.N.field}` 传值、重试 / continue、`review` 人工闸、子工作流（`internal/job/workflow`）。
- job worktree：`--worktree` 建独立分支 `gofer/<job-id>`，结束时生成 `changes.diff`，web ReviewPanel 看 diff、accept / reject；`gofer job worktree ls/rm`，有"已合并"探测。
- job 级任务书模板（`gofer template`，`{{var}}`）。

## 缺口（2026-10-03 代码梳理）

1. 扇出的 N 个 job 共用同一个 agent；步骤不能开 worktree、不能引用任务书模板。
2. 没有把某个 worktree 分支合回基线的能力，只能人工在主 checkout 合并。
3. 没有 workflow 级模板与入参变量。
4. 只能按序号引用步骤输出；扇出结果只能取第一个 fan 的 stdout，汇总步拿不到全部评审意见。
5. web 上扇出只是并排的 job 链接，没有对比视图和"选这个"按钮。

## 方案

### Z1 异构扇出 + worktree（workflow 步骤字段）

- 步骤新增 `agents: [claude, codex, ...]`：扇出数等于列表长度，第 K 个 fan 用第 K 个 agent（与 `fan_out` 互斥）；可选 `fan: [{agent, runner}]` 逐个覆盖。
- 步骤新增并透传 `worktree` / `worktree_base` / `template` / `vars` / `read_only` / `verify`；多 agent 写代码的扇出强制 worktree，每个 fan 独立分支，互不抢目录锁。
- 引用扩展：按步骤名引用 `${steps.<name>.field}`；新增字段 `worktree_branch`、`diff`（changes.diff 路径）、`diff_summary`；聚合引用 `${steps.<name>.all.stdout}`（各 fan 输出带 agent 标题拼接，超过内联上限自动改为写文件传路径）。

### Z2 择优合并

- `join: pick`：扇出步全部结束后停在"待择优"，人工选择后推进；下游可引用 `${steps.<name>.picked.*}`。
- 合并接口：`POST /v1/jobs/{id}/worktree/merge`（策略 `merge --no-ff` / `squash`；冲突返回 409 和冲突文件列表，不留半合并状态）；选中后可一并清理其余 fan 的 worktree 与分支。CLI：`gofer job worktree merge <id> [--squash] [--cleanup-others]`、`gofer wf pick <wf-id> <step> <fan>`。
- 可选 `join: judge`：再派一个裁判 agent，输入各 fan 的 diff 与汇报，输出选中编号和理由，人工确认后合并（默认不自动合并）。

### Z3 workflow 模板库

- 位置：`<project>/.gofer/workflows/*.yaml`（优先）、`<config-dir>/workflows/`，以及内置模板（embed）。
- spec 增加 `vars:`（default / required / desc，复用 `internal/template.Var`），步骤中用 `${vars.x}`。
- 接口：`GET /v1/workflow-templates[/{name}]`；`POST /v1/workflows` 支持 `template` + `vars`；CLI `gofer wf template ls/show`、`gofer wf run --template <name> --var k=v`。
- 内置三个模板：
  - `compare`：同一任务 × 多个 agent（worktree）→ `join: pick`。
  - `plan-implement`：claude 规划 → 人工确认（review 闸）→ codex 按规划实现（worktree）→ 可选验证步。
  - `review-committee`：多个 agent 各自评审同一改动 → 验证者 agent 读真实代码逐条确认、过滤误报 → 输出汇总报告。

### Z4 web

- WorkflowDetail 扇出步改为对比视图：每个 fan 一列（agent、状态、耗时、diff 摘要、领先提交数、verify 结果），可并排展开 diff，每列「选这个并合并」。
- NewWorkflow 增加「从模板新建」：选模板 → 变量表单 → 提交；保留 YAML 编辑。
- JobDetail 的 worktree 区块增加「合并到基线」按钮（同 Z2 接口）。

## 不做（本批）

- 按 hunk 勾选合并（依赖 Z2 稳定后再看）。
- 条件执行 `when:` / 依赖 DAG（plan 已有 DAG；需要时另立）。
- 自动合并（一律人工确认）。

## 分期建议

- 第一期：Z1 + Z2（CLI / API）+ Z3（含三个内置模板）。
- 第二期：Z4 web 对比视图与模板向导。

## 测试与验收

固定测试：`TestWorkflowAgentsFanOutUsesEachAgent`、`TestWorkflowStepWorktreePassThrough`、`TestStepRefByNameAndAll`、`TestJoinPickWaitsForSelection`、`TestWorktreeMergeConflictReturns409`、`TestWorkflowTemplateVars`、`TestBuiltinWorkflowTemplatesValidate`。监督者真实验收：临时 serve 上用两个 exec "agent"（各写不同文件内容）跑 `compare` 模板 → 对比 → pick 一个合并到临时仓库主分支、另一个被清理；`review-committee` 用假 agent 跑通汇总步拿到全部评审输出。
