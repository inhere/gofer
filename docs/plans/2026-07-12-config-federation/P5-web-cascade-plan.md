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

## P5 验收总纲 — ✅ 完成（commit `070caab`；手工浏览器冒烟建议人工验证）

- [x] T5.1 `selectedWorker` + `workerAgentKeys`/`projectOptions` 能力交集 + interactive 过滤（NewJob 有开关）
- [x] T5.2 `reconvergeToWorker` 换 worker/runner 重收敛，无悬空非法选择
- [x] T5.3 未选 worker（"先选 worker" 提示态）/ worker 无能力（回落全量，**fail-safe 绝不锁死**）空态
- [x] `NewSchedule.vue` 同步（唯一分歧：无 interactive 开关，跳过该过滤，已注释说明）
- [x] `pnpm build` 绿（**主机** `pnpm build` = `vue-tsc --noEmit && vite build` **双绿**，产出 NewJob/NewSchedule chunk）
- [x] 手工浏览器冒烟：**真 Chrome(agent-browser)全 PASS** — 隔离 serve+worker+web(:8893, `--web-dir web/dist`)。A baseline `[alpha,beta]`/`[echoagent,exec]` → B runner=wrun 未选 worker 不变+"先选 worker"提示 → **C 选 worker w1: project→`[alpha]`、agent→`[exec]` 收窄且自动重收敛(非空白)** → D 切回 local 回宽、选择有效 → E interactive 开 agent→`[echoagent]`。literal `<select>` DOM 读取为证。

> **浏览器冒烟确认的端到端缺口 → ✅ 已补（`7b063d2`，用户选方案 a）**：原本 worker-only project（host 未定义）**不出现在 NewJob 下拉**——`projectOptions` 从 host `meta.projects` 出发过滤，host 没有的 project 永进不了列表，G1（可提交）与 G3（UI 级联）在 UI 层未接上。
>
> **方案 (a) 带标记实现**：`/v1/meta.projects` 并入**在线 worker** 上报的 project（`worker_only:true` + 空 allowlist；host 定义的同名 project 胜出、不标记；离线 worker 不并入）。消费方按标记处理：NewJob/NewSchedule `projectOptions` **选中 worker 后**才纳入其 worker-only project（baseline/local/labels 隐藏，因跑不了 local），切回 local 自动丢弃重收敛；NewWorkflow 一行 `filter(!p.worker_only)` 排除（workflow 不支持，行为逐字不变）。承重核查确认 `/v1/meta.projects` 仅被 NewJob/NewSchedule/NewWorkflow 消费（Cluster/Board/Projects 走别的端点），波及面可控。
>
> **浏览器复验 5/5 PASS**：`/v1/meta` 含 `wonly(worker_only:true)`；baseline `[alpha,beta]` 无 wonly；选 w1 → `[alpha,wonly]`（wonly 现身、beta 消失）；选 wonly 后切 local → wonly 丢弃重收敛 `alpha`；NewWorkflow 无 wonly。剩 `tools-2gk` 的另两项（交互式 worker-only 报错措辞 / `ListJobs(project=worker-only)` 可见性）后续。

## 实施补记（fail-safe 判断）

- **交集全 fail-safe**：worker 无 caps（离线/旧数据）/ 交集为空 → 回落全量列表，**绝不把下拉清空锁死用户**（后端 P3 会兜底校验真实组合）。
- **labels 模式不收窄**：`selectedWorker` 仅在"指定 worker"模式且已选 worker 时生效；labels 模式多 worker 能力不同，收窄到单个不成立 → 全量。
- **能力 key 源**：优先 `agent_caps`（typed），回落 `agents[]`；两者皆空=不收窄信号。
- **interactive 过滤仅 NewJob**（NewSchedule 无该开关）；亦 fail-safe（会清空则回落）。
- rebuild 预填不加重收敛（源 job 的 agent/worker 逐字保留，避免静默改用户选择）。
