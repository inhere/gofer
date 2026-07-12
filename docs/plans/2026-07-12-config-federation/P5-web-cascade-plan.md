# P5 — web 级联（选 runner → 按 worker 能力 + interactive 过滤）

> 主纲: [../2026-07-12-config-federation-plan.md](../2026-07-12-config-federation-plan.md) ｜ bd: h-aii-xu64.10 ｜ 依赖: P4
> 目标: G3 级联——创建 job/schedule 时，选定 worker runner 后 project/agent 下拉**只列该 worker 真有的**；interactive 场景只列 interactive-capable agent。**纯前端**（数据 P4 已给）。

## 现状（dossier）

`NewJob.vue`/`NewSchedule.vue` 结构一致，单一数据源 `getMeta()`→`/v1/meta`。现有级联仅按 `MetaProject.allowed_agents/allowed_runners` 交集（`agentOptions`/`runnerOptions` computed），**未**用所选 worker 的能力。worker 选择器（`connectedWorkers`）已存在但与 agent/project 下拉**无联动**。

## T5.1 selectedWorker + 能力交集

**文件**: `web/src/views/NewJob.vue`（`:116-168` 附近 computed），`NewSchedule.vue` 同构

1. 新增 computed：当前选中 worker（worker runner 时）：
```ts
const selectedWorker = computed<MetaWorker | undefined>(() =>
  isWorkerRunner.value ? workers.value.find((w) => w.id === workerId.value) : undefined,
)
```
2. `agentOptions` 叠加 worker 能力交集（在现有 allowed_agents 交集之后）：
```ts
const agentOptions = computed<MetaAgent[]>(() => {
  let list = agents.value
  const allowed = selectedProject.value?.allowed_agents ?? []
  if (allowed.length > 0) { const s = new Set(allowed); list = list.filter((a) => s.has(a.key)) }
  // 联邦(G3): worker runner 选定 worker 后，只列该 worker 上报的 agent。
  const w = selectedWorker.value
  if (w?.agents && w.agents.length > 0) { const s = new Set(w.agents); list = list.filter((a) => s.has(a.key)) }
  // interactive 场景(若表单有 interactive 开关)只列 interactive-capable。
  if (interactive.value) list = list.filter((a) => a.interactive)
  return list
})
```
3. `projectOptions`（若无则新增）叠加 worker projects 交集——worker runner 选定 worker 后 project 下拉只列该 worker 上报 projects：
```ts
const projectOptions = computed<MetaProject[]>(() => {
  const w = selectedWorker.value
  if (w?.projects && w.projects.length > 0) { const s = new Set(w.projects); return projects.value.filter((p) => s.has(p.key)) }
  return projects.value
})
```
4. project 下拉 `v-for` 由 `projects` 改 `projectOptions`（`:442-449`）。

## T5.2 重收敛（换 worker/runner 时修正已选值）

**文件**: 同上（`selectProject`/新增 `onWorkerChange`）

- worker 选择器 `<select @change>` 触发时（新增 `onWorkerChange`）：若当前 `agentKey`/`projectKey` 不在新 worker 能力内，回落到该 worker 首个可用项（复用 `selectProject` 的收敛模式）。
- 保证：切换 worker → project/agent 自动收敛到合法值，不留悬空选择（提交前无非法组合）。

## T5.3 交互与空态

- worker runner 但**未选 worker** → project/agent 下拉给出提示态（"先选 worker"）或用全量（提交时 host 端 selector 兜底）。二选一，倾向前者（明确）。
- worker **离线/无能力上报**（旧数据）→ 交集为空时不要把下拉清空到不可选；回落全量 + 轻提示（避免旧 worker 场景卡死表单）。

## P5 验收总纲

- [ ] T5.1 selectedWorker + agent/project 能力交集 + interactive 过滤
- [ ] T5.2 换 worker/runner 重收敛，无悬空非法选择
- [ ] T5.3 未选 worker / worker 无能力 的空态处理
- [ ] `NewSchedule.vue` 同步（同构改动）
- [ ] `pnpm -C web build` 绿（主机 `gofer job -a exec --cwd tools/gofer/web -- pnpm build`）
- [ ] 手工冒烟：选某 worker → project/agent 下拉收窄到该 worker 能力；换 worker → 下拉随动收敛
