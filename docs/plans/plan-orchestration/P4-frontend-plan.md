# P4 — session 续跑 + Plans 前端（实施计划，代码级）

> 主纲：[plan-orchestration-plan.md](./plan-orchestration-plan.md)
> 设计：[../../design/2026-07-09-plan-orchestration-design.md](../../design/2026-07-09-plan-orchestration-design.md) §7（session 续跑）+ §10（前端）
> 上游：[P1-data-plan.md](./P1-data-plan.md)（✅ 已 push）、[P2-mcp-aggregate-plan.md](./P2-mcp-aggregate-plan.md)（✅ 已 push）、[P3-todo-plan.md](./P3-todo-plan.md)（✅ 已 push）；本期在其上叠加 UI 与续跑归组。
> 触点均已实测定位（2026-07-10 只读探查，见各 T 的 file:line）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-10 | inhere/claude | 初稿：P4 代码级子计划（Plans 前端 + session 续跑归组 + Board plan 过滤），触点已实测 |
| v0.2 | 2026-07-10 | inhere/claude | 用户审阅拍板：D1/D2/D3 按倾向，D4-D6 不做。补 **T9.6 会话链入口**（`?session=` 后端已就绪但前端无法过滤，「继续会话」跳转后无回看整链入口 → 闭环缺口）；T1/T2 随之加 `ListJobsOpts.session` 透传。新增 D7（已定）。**无 session 的 job 的「快速重建」+ 血缘 `source_job_id` 划归 P5**（跨数据层，不入 P4） |

## 一句话

P1~P3 已把 plan/todo 三通道（HTTP/CLI/MCP）打通、后端零缺口；P4 补 **web 前端**（Plans 列表 + PlanDetail 详情 + 类型/client/路由/导航）、**session 续跑归组**（JobDetail「继续会话」入口 + 后端 resume **继承源 job 的 plan_id**）、**Board 增 plan 过滤维度**。plan 相关后端 API 全部现成，前端纯对接；续跑归组只需**后端 1 处 1 行**（resume 构造新 job 时带上 `src.PlanID`）+ 前端新增 resume UI。

## 范围

**做（P4）**

1. **web 类型/client/路由/导航**（T1~T4）：`types.ts` 加 `Plan/PlanDetail/Todo/PlanCounts/PlanStatus/PlansResp` + `Job.plan_id` + `ListJobsOpts.plan` + `ListJobsOpts.session`；`client.ts` 加 `listPlans/getPlan/createPlan/attachJob/addTodo/updateTodo/resumeJob` + `listJobs` 透传 `?plan=` / `?session=`；`router.ts` 加 `/plans` + `/plans/:id`；`App.vue`「观察」导航组加 Plans 入口。
2. **Plans.vue**（新，T5）：plan 列表（status/title/id/counts 进度条）+ 顶部内联「新建计划」表单（title + 可选 description），行点击进详情。仿 `Workflows.vue`。
3. **PlanDetail.vue**（新，T6）：plan 头部元数据 + counts 进度聚合条 + jobs 表（链入 `/jobs/:id`）+ todos 清单（勾选/新增/绑 job）+ attach-job 表单。仿 `WorkflowDetail.vue`，running 时轮询。
4. **Board plan 过滤**（T7）：`Board.vue` filter-panel 加 plan 下拉（仿 project 下拉：`listPlans` 填充 + `route.query.plan` 承载），透传到 `listJobs({plan})`。
5. **session 续跑归组**（T8~T9）：**后端** `resume.go` 构造续投 job 时带 `PlanID: src.PlanID`（归组核心，1 行）；**前端** `JobDetail.vue` 对有 `session_id` 的 job 加「继续会话」入口（prompt 输入 → `resumeJob` → 跳新 job），并在 meta 展示 `plan_id`（链入 plan 详情）；**T9.6 会话链回看**：同 `session_id` 的 job 列表（`listJobs({session})`，纯前端），补上「跳转新 job 后无法回看整链」的闭环缺口，尤其惠及**未归 plan 的独立 job**。
6. **list 计数增强**（T10，推荐）：`handleListPlans` 内联每个 plan 的 counts（当前 list 只返回 header、无 counts；列表进度条依赖它）。handler-only ~10 行。
7. **验证**（T11）：前端 `pnpm typecheck && pnpm build`（主机）；后端改动 `go test ./internal/job/... ./internal/httpapi/...` + `go build/vet`。

**不做（划归后续 / 留开关）**

- job 终态 → todo 自动勾选（P3 已明确留后续开关，不在 P4）。
- workflow 整体归入 plan（设计 §12 待确认 2，后续）。
- 续跑时前端**改选/覆盖** plan（本期只做「自动继承源 job plan_id」，设计 §7 语义；覆盖留后续）。
- pty「继续」（interactive session 的 attach 续接，设计 §7 路②）：attach 能力 P2/P3 已在，JobDetail 已有「打开终端」；本期续跑聚焦**续投新 job**（路①），不改 attach。
- todo 拖拽重排 / 删除面（P3 已界定，沿用）。
- 前端单测：web 无测试框架（`package.json` 仅 `dev/build/preview/typecheck`，无 vitest），前端以 `typecheck + build` 兜底。

## 核心约束（承接总纲 C1..C5 / G021..G024）

- **前端只调现成 API**：plan/todo 的 HTTP 面 P1~P3 已全落（`server.go:429-434` 六条路由 + resume `server.go:415`），前端不新增后端端点，除**两处后端小改**（T8 resume 继承、T10 list 计数）——二者都属"补齐既有端点的行为/字段"，非新端点。
- **snake_case 契约逐字对齐**：web 类型字段严格照后端 JSON tag（`plan_handler.go` 的 `planView/todoView/planDetail`、`PlanCounts`），照 `types.ts` 既有 `Workflow/Job` 的对齐风格。
- **G021 后端入口薄**：T8 归组逻辑落在 `internal/job`（`ResumeJob` 编排体内），httpapi resume handler 不动；T10 只在 handler 内投影，聚合查询复用现成 `PlanJobStatusCounts`/`RollupPlanCounts`（jobstore）。
- **前端范式复用**：列表仿 `Workflows.vue`（轮询 2.5s + Page Visibility + status 过滤 + 行点击）；详情仿 `WorkflowDetail.vue`（head-card + meta dl + section + running 轮询 + `watch(props.id)` 重取）；过滤仿 `Board.vue` 的 project 下拉（`route.query` 承载、可深链）。

## 已确认关键事实（探查结论）

| 事实 | 位置 | 对 P4 的影响 |
|---|---|---|
| resume 后端已具备（新起 job 沿用 `src.SessionID`、同 runner），但 **Submit 未带 `PlanID`** | `internal/job/resume.go:74-95` | T8 加 `PlanID: src.PlanID,` 1 行即完成归组；`src` 是 `JobResult`，`PlanID` 字段存在（`model.go:216`） |
| resume HTTP 端点已注册，body `{prompt, runner?}`，返回新 job `JobResult`（含 `plan_id`） | `internal/httpapi/job_handler.go:279-307`；路由 `server.go:415` | 前端 `resumeJob(id, prompt)` 直接对接；**无需**改 resume body/签名 |
| **web 前端目前完全无 resume 触发**（仅 meta 展示 `session_id` + title 提示 `gofer job resume`），client 无 `resumeJob` | `web/src/views/JobDetail.vue:927-929`；`web/src/api/client.ts`（grep 无 resume） | T9 新增 resume UI + client 方法（从 0 建） |
| `GET /v1/plans`（list）**只返回 header，无 counts**；`GET /v1/plans/{id}`（detail）才有 `counts/jobs/todos` | `plan_handler.go:80-91`（list）vs `:93-136`（detail） | 列表进度条需 counts → T10 给 list 内联 counts（否则列表无 counts 数据源） |
| `?plan=` list 过滤后端已全落（handler/job.ListOpts/DB+overlay 三路） | `job_handler.go:141`；`job/list.go:32-33,82,121` | Board plan 过滤纯前端：`ListJobsOpts.plan` + `listJobs` query（T2）+ Board 下拉（T7） |
| `PlanCounts` = `{total,queued,running,done,failed}` 全 int | `internal/jobstore/plans.go:151-157` | web `PlanCounts` 类型逐字对齐 |
| `planDetail` = header + `counts`(PlanCounts) + `jobs`([]JobResult) + `todos`([]todoView) | `plan_handler.go:93-98,130-135` | web `PlanDetail` 类型：header 展开 + counts + jobs(Job[]) + todos(Todo[]) |
| `todoView` = `{todo_id,plan_id,job_id?,title,done,sort?,created_at,updated_at}` | `plan_handler.go:33-42` | web `Todo` 类型逐字对齐 |
| Board project 过滤走 `route.query.project`（可深链、非 ref） + `listProjects` 填下拉 | `Board.vue:115-139,143-151` | plan 过滤照抄：`route.query.plan` + `listPlans` 填下拉 |
| `StatusBadge` 只吃 `JobStatus`（`statusColor` 仅映射 job 状态）；plan 状态 `open/active/archived` 不在其中 | `web/src/components/StatusBadge.vue:5,7`；`client.ts:628-636` | plan 状态**不复用 StatusBadge**，用本地状态 pill（自带颜色映射，见 T5） |
| App 导航 `navGroups[0]`（观察）topbar+drawer 共用同一数组 | `App.vue:39-59,98-109,148-160` | 加 1 项 `{to:'/plans',label:'Plans'}` 两处同步生效 |
| 前端无测试框架 | `web/package.json` scripts | 验证 = `pnpm typecheck && pnpm build` |

## `session 续跑归组` 结论（探查定论）

**结论：需后端 1 处 1 行 + 前端新增 resume UI（非纯前端）。**

- **归组机制 = 后端继承**（推荐落点）：`resume.go` 的 `s.Submit(JobRequest{...})`（`resume.go:74-95`）当前带了 `SessionID/Runner/Channel/OriginAgent…` 却漏了 `PlanID`。加 `PlanID: src.PlanID,` 一行 → 续投 job 天然落入源 job 的同一 plan（`src` 有 plan_id 就继承，没有就为空）。契合设计 §7「续投 job 自动继承原 job 的 `plan_id`」。
- **为何不用「纯前端传 plan_id」**：resume body 现为 `{prompt, runner?}`（`job_handler.go:283-286`），若走前端读源 job `plan_id` 再塞进 resume 请求，需给 `resumeJobReq` + `ResumeJob(...)` 签名**加一个 plan_id 入参**（改动更大、多一处可伪造入口），而后端继承只碰 1 行、语义更内聚（归组是 job 血缘的自然延续，不该交给客户端声明）。故**选后端继承**。
- **前端职责**：只提供**入口**（「继续会话」按钮 + prompt 输入 → `resumeJob(id, prompt)` → 跳转新 job）。归组由后端自动完成，前端无需感知 plan_id 传递。

## Plans/PlanDetail 复用模板的程度

- **Plans.vue ≈ Workflows.vue 90%**：列表轮询/Page Visibility/status 过滤/行点击/空态/表格样式几乎照搬；差异仅：① 列由 `step` 换 `counts 进度条`；② 顶部「创建」由跳 `/workflows/new` 改为**内联新建表单**（plan 创建轻量，只 title+desc，不值当单开路由/页面）；③ 状态 pill 用本地实现（非 StatusBadge）。
- **PlanDetail.vue ≈ WorkflowDetail.vue 70%**：head-card + meta dl + running 轮询 + `watch(props.id)` 重取 + 返回按钮全照搬；新增段：counts 进度条、jobs 表（链 `/jobs/:id`，仿 WorkflowDetail 的 step→job 链）、todos 清单（勾选/新增/绑 job，本期新写）、attach-job 表单（本期新写）。无 workflow 的 step-group/fan-out/子 wf/events 复杂度。

---

## 任务分解（T1..T11）

> 顺序：T1~T4 前端管道（类型/client/路由/导航）→ T5 Plans.vue → T6 PlanDetail.vue → T7 Board 过滤 → T8 后端 resume 归组 → T9 JobDetail resume UI → T10 后端 list 计数 → T11 验证门禁。SR1202：每个 T（或相邻小 T 合并）完成后更新总纲 checkbox 并 Git 提交（`feat(web):` / `feat(plan):` 前缀）。

---

### T1 — web `types.ts`：Plan/Todo/PlanCounts 类型 + `Job.plan_id` + `ListJobsOpts.plan`

**`web/src/api/types.ts`**：

**1.1 `Job` 加 `plan_id`**（现状 `types.ts:53-55`，`role/origin_agent/escalate_to` 处；后端 `JobResult.PlanID json:"plan_id,omitempty"`，`getJob`/resume 均回该字段）：
```ts
  role?: string
  origin_agent?: string
  escalate_to?: string
  // plan 编排（plan-orchestration）：客户端可设的归组键；归入某 plan 时非空。
  // 详情页据此展示 plan 链接；session 续跑的新 job 自动继承源 job 的 plan_id。
  plan_id?: string
}
```

**1.2 `ListJobsOpts` 加 `plan`**（现状 `types.ts:412-423`，`caller` 后）：
```ts
  caller?: string
  // plan 归组过滤（?plan=<id>，后端 P1 已落）：只列该 plan 下的 job。
  plan?: string
  // session 会话链过滤（?session=<sid>，后端早已落：job/list.go:29-31,118 + job_handler.go:140，
  // 但前端此前从未透传）。T9.6 用它列出「同一 agent 会话下的全部 job」（resume 链）。
  session?: string
  limit?: number
  offset?: number
}
```

**1.3 plan 一族类型**（文件末尾新增，紧邻 workflow 类型块之后 `types.ts:713` 附近）：
```ts
// plan 编排（plan-orchestration，design §5/§10）。归组容器：把陆续产生的独立 job
// 归到一个计划 + 跟进 todo。与 workflow（静态串行引擎）正交。字段严格对齐
// internal/httpapi/plan_handler.go 的 planView/todoView/planDetail 及 jobstore.PlanCounts。
export type PlanStatus = 'open' | 'active' | 'done' | 'archived'

// plan 下 jobs 的实时状态聚合（查询期算，detail 恒有；list 经 T10 内联）。
export interface PlanCounts {
  total: number
  queued: number
  running: number
  done: number
  failed: number
}

// plan 待办项（P3）。job_id 为空=纯待办；非空=绑某次 job 执行（元数据，done 纯手动）。
export interface Todo {
  todo_id: string
  plan_id: string
  // 后端 omitempty
  job_id?: string
  title: string
  done: boolean
  // 后端 omitempty
  sort?: number
  // Unix 秒
  created_at: number
  updated_at: number
}

// plan 头部（list/create/attach 返回）。counts 在 detail 恒有、list 经 T10 内联（故 optional）。
export interface Plan {
  plan_id: string
  // 后端 omitempty
  title?: string
  description?: string
  status: PlanStatus
  owner?: string
  progress?: number
  // Unix 秒
  created_at: number
  updated_at: number
  // list（T10 后）与 detail 均带；老 list 响应缺省时为 undefined，渲染做兜底
  counts?: PlanCounts
}

// plan 详情（GET /v1/plans/{id}）：头部 + counts + 其下 jobs + todos。
export interface PlanDetail extends Plan {
  counts: PlanCounts
  jobs: Job[]
  todos: Todo[]
}

export interface PlansResp {
  plans: Plan[]
}
```

**验收**：`pnpm typecheck` 绿（新类型无 TS 报错）。

---

### T2 — web `client.ts`：plan 端点 + `resumeJob` + `listJobs` 透传 `?plan=` / `?session=`

**`web/src/api/client.ts`**：

**2.1 import 类型补全**（现状 `client.ts:6-42` 的 `import type {...}`）加 `Plan, PlanDetail, PlansResp, Todo`（`PlanStatus` 若函数签名需要一并加）。

**2.2 `listJobs` 透传 plan**（现状 `client.ts:259-262`，`caller` 拼装后）：
```ts
  if (opts?.caller) {
    params.set('caller', opts.caller)
  }
  if (opts?.plan) {
    params.set('plan', opts.plan)
  }
  if (opts?.session) {
    params.set('session', opts.session)
  }
```

> `?session=` 后端三路（handler / `job.ListOpts` / DB+overlay）**早已就绪**，仅前端从未透传；T9.6 的会话链列表依赖它。无后端改动。

**2.3 plan API 一族**（文件内 workflow 段之后、`statusColor` 之前新增；仿 `listWorkflows/getWorkflow/submitWorkflow`）：
```ts
// plan 编排（plan-orchestration）。list 仅头部（+counts，T10）；detail 内联 counts/jobs/todos。
export function listPlans(status?: PlanStatus): Promise<PlansResp> {
  const qs = status ? `?status=${encodeURIComponent(status)}` : ''
  return request<PlansResp>(`/v1/plans${qs}`)
}

export function getPlan(id: string): Promise<PlanDetail> {
  return request<PlanDetail>(`/v1/plans/${encodeURIComponent(id)}`)
}

// 建计划（plan_id 缺省服务端生成；owner 由服务端盖 caller）。
export function createPlan(req: {
  title?: string
  description?: string
  plan_id?: string
}): Promise<Plan> {
  return request<Plan>('/v1/plans', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
}

// 补挂已有 job 到 plan（提交即归组走 POST /v1/jobs 的 plan_id；此为事后补挂）。返回 plan 头部。
export function attachJob(planId: string, jobId: string): Promise<Plan> {
  return request<Plan>(`/v1/plans/${encodeURIComponent(planId)}/jobs`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ job_id: jobId }),
  })
}

// 在 plan 下建 todo（纯待办或绑 job）。
export function addTodo(
  planId: string,
  title: string,
  jobId?: string,
): Promise<Todo> {
  return request<Todo>(`/v1/plans/${encodeURIComponent(planId)}/todos`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title, job_id: jobId ?? '' }),
  })
}

// 勾/取消 todo 的 done（PATCH /v1/todos/{id}）。
export function updateTodo(todoId: string, done: boolean): Promise<Todo> {
  return request<Todo>(`/v1/todos/${encodeURIComponent(todoId)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ done }),
  })
}
```

**2.4 `resumeJob`**（session 续跑；仿 `cancelJob`，POST /v1/jobs/{id}/resume，返回新 job 的 `Job` 快照）：
```ts
// session 续跑（session-capture P2）：续投新 job 续接源 job 的底层 agent 会话。
// body {prompt}；后端同 runner、自动继承源 job 的 plan_id（归入同 plan 血缘，P4/T8）。
// 返回新 job（其 session_id 链回源会话，plan_id 继承源 job）。前端据此跳转新 job 详情。
export function resumeJob(id: string, prompt: string): Promise<Job> {
  return request<Job>(`/v1/jobs/${encodeURIComponent(id)}/resume`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  })
}
```

> `request<T>`（`client.ts:89`）对 PATCH 与 POST 同路（透传 `method`）；错误体 `{error,detail}` 已统一转 `ApiError`（含 resume 400/404）。

**验收**：`pnpm typecheck` 绿；无未用 import。

---

### T3 — `router.ts`：`/plans` + `/plans/:id` 路由

**`web/src/router.ts`**（workflows 路由块 `router.ts:23-34` 之后新增，紧随其后保持"观察"域聚拢）：
```ts
  { path: '/plans', name: 'plans', component: () => import('./views/Plans.vue') },
  {
    path: '/plans/:id',
    name: 'plan-detail',
    component: () => import('./views/PlanDetail.vue'),
    props: true,
  },
```

> 无 `/plans/new`（创建走 Plans.vue 内联表单，见 T5）。路由默认受全局守卫（`router.ts:61-69`，无 token 跳 /access）。

**验收**：`pnpm build` 绿（懒加载组件路径存在，即 T5/T6 文件已建）。

---

### T4 — `App.vue`：「观察」导航组加 Plans 入口

**`web/src/App.vue`**，`navGroups[0]`（观察）items（现状 `App.vue:41-48`，Workflows 后）：
```ts
      { to: '/board', label: 'Board' },
      { to: '/sessions', label: 'Sessions' },
      { to: '/workflows', label: 'Workflows' },
      { to: '/plans', label: 'Plans' },
      { to: '/schedules', label: 'Schedules' },
```

> topbar（`App.vue:98-109`）与窄屏 drawer（`App.vue:148-160`）都 `v-for` 同一 `navGroups`，一处改动两处生效，无需再动模板。

**验收**：`pnpm build` 绿；顶栏/抽屉「观察」组出现 Plans 链接，active 高亮随路由。

---

### T5 — `Plans.vue`（新）：plan 列表 + 内联新建

**新建 `web/src/views/Plans.vue`**（仿 `Workflows.vue`：轮询 2.5s + Page Visibility + status 过滤 + 行点击 + 空态；差异见「复用程度」）。骨架：

```vue
<script setup lang="ts">
// Plans 列表：轮询 listPlans（2.5s），Page Visibility 暂停/恢复，status 过滤，
// 行点击进详情；顶部内联「新建计划」表单（title + 可选 description）。仿 Workflows.vue。
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { createPlan, listPlans } from '../api/client'
import type { Plan, PlanCounts, PlanStatus } from '../api/types'

const router = useRouter()
const POLL_MS = 2500

const plans = ref<Plan[]>([])
const loading = ref(false)
const error = ref('')
const statusFilter = ref<'' | PlanStatus>('')

// 内联新建
const newTitle = ref('')
const newDesc = ref('')
const creating = ref(false)
const createError = ref('')

const statusOptions: Array<{ value: '' | PlanStatus; label: string }> = [
  { value: '', label: '全部' },
  { value: 'open', label: 'open' },
  { value: 'active', label: 'active' },
  { value: 'done', label: 'done' },
  { value: 'archived', label: 'archived' },
]

let timer: number | null = null

async function fetchPlans(): Promise<void> {
  loading.value = true
  try {
    const resp = await listPlans(statusFilter.value || undefined)
    plans.value = resp.plans ?? []
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function onCreate(): Promise<void> {
  if (!newTitle.value.trim() || creating.value) {
    return
  }
  creating.value = true
  try {
    const p = await createPlan({
      title: newTitle.value.trim(),
      description: newDesc.value.trim() || undefined,
    })
    newTitle.value = ''
    newDesc.value = ''
    createError.value = ''
    void router.push(`/plans/${encodeURIComponent(p.plan_id)}`)
  } catch (e) {
    createError.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}

// counts 进度条分段（done/running/failed/queued 占比）。counts 缺省（老 list）→ 空条。
function segments(c?: PlanCounts): Array<{ cls: string; pct: number }> {
  if (!c || c.total <= 0) {
    return []
  }
  const pct = (n: number) => (n / c.total) * 100
  return [
    { cls: 'seg--done', pct: pct(c.done) },
    { cls: 'seg--run', pct: pct(c.running) },
    { cls: 'seg--fail', pct: pct(c.failed) },
    { cls: 'seg--queue', pct: pct(c.queued) },
  ].filter((s) => s.pct > 0)
}

function countsText(c?: PlanCounts): string {
  if (!c) {
    return '—'
  }
  return `${c.done}/${c.total}`
}

// plan 状态 → 颜色 class（不复用 StatusBadge：其 statusColor 仅认 JobStatus）。
function statusClass(s: PlanStatus): string {
  return `pill pill--${s}` // open/active/done/archived 见 <style>
}

function shortId(id: string): string {
  return id.length > 14 ? id.slice(-14) : id
}

function openPlan(p: Plan): void {
  void router.push(`/plans/${encodeURIComponent(p.plan_id)}`)
}

// 轮询/可见性：与 Workflows.vue 逐字同构（startPolling/stopPolling/onVisibility）。
function startPolling(): void {
  stopPolling()
  if (document.hidden) return
  timer = window.setInterval(() => void fetchPlans(), POLL_MS)
}
function stopPolling(): void {
  if (timer != null) {
    window.clearInterval(timer)
    timer = null
  }
}
function onVisibility(): void {
  if (document.hidden) {
    stopPolling()
  } else {
    void fetchPlans()
    startPolling()
  }
}

watch(statusFilter, () => void fetchPlans())

onMounted(() => {
  void fetchPlans()
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})
onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>
```

**模板**（仿 Workflows：board-head + 内联新建行 + status 过滤 + 表格）关键点：
- `.board-head`：标题 `PLANS` + controls（status 下拉 + poll-hint），照 Workflows。
- **新建行**（board-head 下方）：`<input v-model="newTitle" placeholder="计划标题">` + `<input v-model="newDesc" placeholder="描述(可选)">` + `<button @click="onCreate" :disabled="!newTitle.trim() || creating">新建计划</button>`；`createError` 错误条。
- 表格 thead：`状态 / plan · title / id / 进度 / 更新`；trow：`statusClass` pill + title/shortId 两行（仿 `.col-wf`）+ counts 进度条（`<div class="cbar"><span v-for="s in segments(p.counts)" :class="s.cls" :style="{width: s.pct + '%'}"/></div>` + `countsText(p.counts)`）+ `fmtDuration`/时间。行 `@click="openPlan(p)"`。
- 空态：`暂无 plan`（仿 Workflows 空态，去掉跳转链接或指向新建输入框聚焦）。

**样式**：复用 Workflows.vue 的 `.board/.board-head/.title/.controls/.filter*/.table/.thead/.trow/.col-*/.empty/.error` 几乎全部；新增：
- `.pill`（状态徽标）：`pill--open`(queue灰) / `pill--active`(phosphor绿) / `pill--done`(done色) / `pill--archived`(压暗)。
- `.cbar`（进度条容器，flex + 细高 + `--line` 底）；`.seg--done`(--done)/`.seg--run`(--run)/`.seg--fail`(--fail)/`.seg--queue`(--queue)。
- 新建行 `.create-row`（flex + gap，input 复用 filter-input 风格）。

**验收**：`pnpm typecheck && pnpm build` 绿；页面进 `/plans` 列出 plan（含状态 pill + 进度条）；新建表单 title 必填、提交后跳详情；status 过滤生效；running/queued 有 job 的 plan 进度条随轮询刷新。

---

### T6 — `PlanDetail.vue`（新）：元数据 + counts + jobs + todos + attach

**新建 `web/src/views/PlanDetail.vue`**（仿 `WorkflowDetail.vue`：head-card + meta dl + running 轮询 + `watch(props.id)` 重取 + 返回按钮）。骨架：

```vue
<script setup lang="ts">
// Plan 详情：getPlan 填头部 + counts 进度 + jobs 表 + todos 清单；有未终态 job 时轮询（2.5s）。
//  - jobs 链入 /jobs/{id}（仿 WorkflowDetail 的 step→job）。
//  - todos：勾选(updateTodo) / 新增(addTodo，可绑 job) / 展示绑定 job。
//  - attach：把已有 job id 补挂到本 plan（attachJob）。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import StatusBadge from '../components/StatusBadge.vue'
import { addTodo, attachJob, getPlan, updateTodo } from '../api/client'
import { fmtDuration } from '../api/time'
import type { PlanDetail, Todo } from '../api/types'

const props = defineProps<{ id: string }>()
const router = useRouter()
const POLL_MS = 2500

const plan = ref<PlanDetail | null>(null)
const error = ref('')

// 操作态
const newTodoTitle = ref('')
const newTodoJob = ref('')
const addingTodo = ref(false)
const attachJobId = ref('')
const attaching = ref(false)
const opError = ref('')

let timer: number | null = null

// plan「进行中」= 其下有 queued/running 的 job；据此决定是否轮询（仿 WorkflowDetail.isRunning）。
const isActive = computed(() => {
  const c = plan.value?.counts
  return !!c && c.running + c.queued > 0
})

async function fetchPlan(): Promise<void> {
  try {
    plan.value = await getPlan(props.id)
    error.value = ''
    if (!isActive.value) stopPolling()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function onToggleTodo(t: Todo): Promise<void> {
  opError.value = ''
  try {
    const updated = await updateTodo(t.todo_id, !t.done)
    // 就地回填（避免整刷）
    if (plan.value) {
      plan.value.todos = plan.value.todos.map((x) =>
        x.todo_id === updated.todo_id ? updated : x,
      )
    }
  } catch (e) {
    opError.value = e instanceof Error ? e.message : String(e)
  }
}

async function onAddTodo(): Promise<void> {
  if (!newTodoTitle.value.trim() || addingTodo.value) return
  addingTodo.value = true
  opError.value = ''
  try {
    await addTodo(props.id, newTodoTitle.value.trim(), newTodoJob.value.trim() || undefined)
    newTodoTitle.value = ''
    newTodoJob.value = ''
    await fetchPlan() // 重取拿到新 todo（也刷新 counts/updated_at）
  } catch (e) {
    opError.value = e instanceof Error ? e.message : String(e)
  } finally {
    addingTodo.value = false
  }
}

async function onAttach(): Promise<void> {
  if (!attachJobId.value.trim() || attaching.value) return
  attaching.value = true
  opError.value = ''
  try {
    await attachJob(props.id, attachJobId.value.trim())
    attachJobId.value = ''
    await fetchPlan() // 重取拿到新挂 job + counts
  } catch (e) {
    opError.value = e instanceof Error ? e.message : String(e)
  } finally {
    attaching.value = false
  }
}

function openJob(jobId: string): void {
  void router.push(`/jobs/${encodeURIComponent(jobId)}`)
}

// 「在 Board 查看该 plan 的 job」深链（复用 T7 的 ?plan= 过滤）。
function viewInBoard(): void {
  void router.push({ path: '/board', query: { plan: props.id } })
}

// 轮询/可见性 + watch(props.id) 重取：与 WorkflowDetail.vue 同构。
function startPolling(): void {
  stopPolling()
  if (document.hidden) return
  timer = window.setInterval(() => {
    if (isActive.value) void fetchPlan()
    else stopPolling()
  }, POLL_MS)
}
function stopPolling(): void {
  if (timer != null) {
    window.clearInterval(timer)
    timer = null
  }
}
function onVisibility(): void {
  if (document.hidden) stopPolling()
  else if (isActive.value) {
    void fetchPlan()
    startPolling()
  }
}

watch(
  () => props.id,
  () => {
    stopPolling()
    plan.value = null
    void fetchPlan().then(() => {
      if (isActive.value) startPolling()
    })
  },
)

onMounted(() => {
  void fetchPlan().then(() => {
    if (isActive.value) startPolling()
  })
  document.addEventListener('visibilitychange', onVisibility)
})
onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>
```

**模板**（仿 WorkflowDetail：detail-head 返回按钮 + head-card meta dl + sections）关键点：
- `.detail-head`：`← plans` 返回（`router.push('/plans')`）+ 右侧 plan 状态 pill（本地实现，同 T5 statusClass）+ 「在 Board 查看」按钮（`viewInBoard`）。
- `.head-card`：`plan.title || plan.plan_id` 标题；meta dl 行：`id / status / owner / progress / created / updated`（`fmtTime` 可仿 WorkflowDetail 的时间；或用 `new Date(x*1000).toLocaleString()`）。
- **counts 进度段**：`<section>` COUNTS：进度条（同 T5 segments）+ 文案 `total/queued/running/done/failed`（逐桶数字）。
- **jobs 段**：`<section>` JOBS：表 `状态 / job id / agent / runner`；`v-for="j in plan.jobs"`，`<StatusBadge :status="j.status"/>`（这里是 **job** 状态，复用 StatusBadge 正确）+ shortId(j.id) 链 `openJob(j.id)`；空态「该计划暂无 job」。
- **todos 段**：`<section>` TODOS：`v-for="t in plan.todos"` 行：`<input type="checkbox" :checked="t.done" @change="onToggleTodo(t)">` + `t.title` + 绑定 job 徽标（`t.job_id` 非空时 `job={shortId} →`，点击 `openJob(t.job_id)`）；下方新增行：`<input v-model="newTodoTitle" placeholder="待办标题">` + `<input v-model="newTodoJob" placeholder="绑定 job id(可选)">` + `<button @click="onAddTodo">新增待办</button>`；空态「暂无待办」。
- **attach 段**：`<input v-model="attachJobId" placeholder="已有 job id">` + `<button @click="onAttach">挂到本计划</button>`。
- `opError` 统一错误条（todo/attach 操作失败）。

**样式**：复用 WorkflowDetail 的 `.detail/.detail-head/.back/.head-card/.wf-title/.meta/.meta-row/.chain-title/.error/.loading` 等；jobs 表复用 `.step*`/`.step-link` 或简化为通用 `.table/.trow`；新增 `.pill--*`（同 T5）、`.cbar`/`.seg--*`（同 T5）、`.todo-row`（checkbox + title + job 徽标）、`.op-form`（新增/attach 输入行）。

**验收**：`pnpm typecheck && pnpm build` 绿；`/plans/:id` 显示 plan 元数据 + counts 进度 + jobs 表（可点入 job）+ todos（可勾选/新增/绑 job）+ attach（挂已有 job 后刷新出现）；有 running/queued job 时轮询、进入全终态停轮询；跨 plan 切换（若从别处链入）`watch(props.id)` 重取。

---

### T7 — `Board.vue`：filter-panel 加 plan 过滤维度

**`web/src/views/Board.vue`**（仿现有 project 下拉；plan 走 `route.query.plan` 承载，可深链——PlanDetail 的「在 Board 查看」即用此）：

**7.1 import + 数据源**（现状 `Board.vue:7`）：
```ts
import { listJobs, listPlans, listProjects } from '../api/client'
```
`projectKeys` 附近（`Board.vue:31-32`）加：
```ts
const planKeys = ref<Array<{ id: string; title?: string }>>([])
```

**7.2 planFilter / planSelectValue / planOptions**（仿 project，`Board.vue:115-139` 之后）：
```ts
const planFilter = computed(() => {
  const p = route.query.plan
  return typeof p === 'string' && p ? p : undefined
})
const planSelectValue = computed({
  get: () => planFilter.value ?? '',
  set: (value: string) => {
    const nextQuery = { ...route.query }
    if (value) nextQuery.plan = value
    else delete nextQuery.plan
    void router.push({ path: '/board', query: nextQuery })
  },
})
```

**7.3 fetchPlans + onMounted**（仿 `fetchProjects`，`Board.vue:143-151`）：
```ts
async function fetchPlans(): Promise<void> {
  try {
    const resp = await listPlans()
    planKeys.value = (resp.plans ?? []).map((p) => ({ id: p.plan_id, title: p.title }))
  } catch {
    // plan 下拉失败不阻塞 board（静默）
  }
}
```
`onMounted`（`Board.vue:300-306`）加 `void fetchPlans()`。

**7.4 fetchJobs / fetchStatusCounts 带 plan**（现状 `Board.vue:156-166`、`180-187`）：两处 `listJobs({...})` opts 加 `plan: planFilter.value,`。

**7.5 activeFilterCount / watch / clearFilters 纳入 plan**：
- `activeFilterCount`（`Board.vue:58-68`）数组加 `planFilter.value`。
- 两个 `watch`（`Board.vue:223-241`）依赖数组加 `planFilter`。
- `clearFilters`（`Board.vue:288-298`）：若 `planFilter.value` 存在，清 plan——因 project/plan 都在 query，统一 `router.push({ path: '/board' })` 即可清掉两者（现有 clearFilters 已对 project 这么做，plan 同理，无需额外分支；确认 push 空 query 会同时抹掉 project+plan）。

**7.6 模板下拉**（filter-panel 的 `.controls` 内，project 下拉 `Board.vue:358-370` 之后）：
```vue
        <label class="filter">
          <span class="filter-label">plan</span>
          <select v-model="planSelectValue" class="filter-select mono">
            <option value="">全部</option>
            <option v-for="p in planKeys" :key="p.id" :value="p.id">
              {{ p.title || p.id }}
            </option>
          </select>
        </label>
```
（可选）project chip 旁加 plan chip（`Board.vue:357`）：`<span v-if="planFilter" class="proj-chip">plan: {{ planFilter }}</span>`。

**验收**：`pnpm typecheck && pnpm build` 绿；Board filter 出现 plan 下拉；选中某 plan → 列表只剩该 plan 的 job、URL 带 `?plan=`；PlanDetail「在 Board 查看」跳转后下拉已预选；清空过滤同时清掉 project+plan。

---

### T8 —（后端）`resume.go`：续投 job 继承源 job 的 `plan_id`（归组核心）

**`internal/job/resume.go`**，`ResumeJob` 的 `s.Submit(JobRequest{...})`（现状 `resume.go:74-95`，在 `EscalateTo: src.EscalateTo,` 之后、`})` 之前追加）：
```go
		OriginAgent: src.OriginAgent,
		EscalateTo:  src.EscalateTo,
		// 续跑归组（plan-orchestration P4，design §7）：续投 job 继承源 job 的 plan_id，
		// 使"一次会话里多轮续接"天然归入同一 plan 血缘（源 job 未归组时为空）。plan_id 是
		// 客户端可设的归组键（区别引擎私有 workflow_id），这里由后端从源 job 继承而非客户端声明。
		PlanID: src.PlanID,
	})
```

> `src` 是 `JobResult`（`resume.go:41` `s.Get(jobID)`），`PlanID` 字段存在（`model.go:216`）；`JobRequest.PlanID`（`model.go:85`）为客户端可设、submit 落库（P1 T3.4 已接 `submit.go`）。故续投 job 落库即带 plan_id，`GET /v1/plans/{id}` 与 `?plan=` 立即可见。

**验收**：`go build ./... && go vet ./...` 绿；新增/扩展 `internal/job/resume_test.go`（或就近测试）：源 job 带 `PlanID="plan-x"` + `SessionID` → `ResumeJob` 返回的新 job `PlanID=="plan-x"`；源 job 无 plan_id → 新 job plan_id 为空（不回填、不报错）。

---

### T9 —（前端）`JobDetail.vue`：「继续会话」resume 入口 + `plan_id` meta 链接 + 会话链回看

**`web/src/views/JobDetail.vue`**：

**9.1 import router + resumeJob**（现状 `JobDetail.vue:6` 仅 `useRoute`；`client` import 块 `:15-30`）：
```ts
import { useRoute, useRouter } from 'vue-router'
```
client import 加 `resumeJob`（`:15-30` 的 `{...}` 内）。`useRouter`：`const router = useRouter()`（`route` 声明 `:50` 旁）。

**9.2 resume 状态 + 动作**（近 `doCancel` `:457-467`）：
```ts
// session 续跑：仅对有 session_id 的 job 可用。续投新 job（同会话、继承 plan_id），跳新 job。
const showResumeForm = ref(false)
const resumePrompt = ref('')
const resuming = ref(false)
const resumeError = ref('')

async function doResume(): Promise<void> {
  if (resuming.value) return
  resuming.value = true
  resumeError.value = ''
  try {
    const newJob = await resumeJob(props.id, resumePrompt.value)
    resumePrompt.value = ''
    showResumeForm.value = false
    void router.push(`/jobs/${encodeURIComponent(newJob.id)}`)
  } catch (e) {
    resumeError.value = e instanceof Error ? e.message : String(e)
  } finally {
    resuming.value = false
  }
}
```

> resume 允许空 prompt（后端 `resumeJobReq.Prompt` 可空，`job_handler.go:282`）；失败（400 无会话/不支持/跨 runner，404 未知）走 `ApiError` → `resumeError` 展示。

**9.3 session_id meta 加「继续会话」按钮 + 内联表单**（现状 `JobDetail.vue:927-930`）：
```vue
      <div v-if="job.session_id" class="meta-item">
        <span class="meta-k mono">session_id</span>
        <span class="meta-v mono" :title="job.session_id">{{ job.session_id }}</span>
        <button class="resume-btn mono" type="button" @click="showResumeForm = !showResumeForm">
          {{ showResumeForm ? '收起' : '继续会话' }}
        </button>
      </div>
      <div v-if="job.session_id && showResumeForm" class="resume-form">
        <textarea
          v-model="resumePrompt"
          class="resume-input mono"
          rows="3"
          placeholder="续接指令（可留空，让 agent 直接继续）"
        ></textarea>
        <div class="resume-actions">
          <button class="resume-go mono" type="button" :disabled="resuming" @click="doResume">
            {{ resuming ? '续投中…' : '续投新 job' }}
          </button>
          <span v-if="resumeError" class="resume-err mono">{{ resumeError }}</span>
        </div>
      </div>
```

**9.4 plan_id meta 链接**（现状 `JobDetail.vue:921-923` channel 处附近新增；归组可视化，让 job → plan 可回跳）：
```vue
      <div v-if="job.plan_id" class="meta-item">
        <span class="meta-k mono">plan</span>
        <RouterLink class="meta-v mono" :to="`/plans/${encodeURIComponent(job.plan_id)}`">
          {{ job.plan_id }}
        </RouterLink>
      </div>
```

> `RouterLink` 在 JobDetail 模板已用（`:869` `← board`）；`job.plan_id` 由 T1 类型 + 后端 `plan_id,omitempty` 提供。

**9.5 样式**：新增 `.resume-btn`（小幽光按钮，仿 `.terminal-open`/`.reconnect`）、`.resume-form`/`.resume-input`（textarea，仿页面既有输入风格）、`.resume-actions`/`.resume-go`/`.resume-err`（错误红字，仿 `.stream-err`）。

**9.6 会话链回看（闭环缺口，v0.2 追加）**

> **为什么必须做**：9.3 的「继续会话」跳转到新 job 后，用户**没有任何入口回看整条 resume 链**。已归 plan 的 job 还能从 PlanDetail 的 jobs 表看到兄弟 job；**未归 plan 的独立 job 则彻底断线** —— 而独立 job 恰恰是 resume 的主力场景。数据层关联本就存在（resume 复用 `src.SessionID`，`resume.go:84`；且 `session_id` 非空是 resume 的硬前置，`resume.go:45` `ErrNoSession`），只是前端从未透传 `?session=`。本节把它接上。**纯前端，无后端改动**（T1/T2 已备好 `ListJobsOpts.session`）。

**9.6.1 拉取同会话 job**（近 9.2 的 resume 状态声明；`listJobs` 加入 client import）：
```ts
// 会话链：同 session_id 的全部 job（含本 job）。resume 复用源 job 的 session_id，
// 故一条 resume 链共享同一 sid。后端无 parent_job_id，链内顺序按 started_at 升序还原。
const sessionJobs = ref<Job[]>([])
const sessionJobsOpen = ref(false)

async function loadSessionJobs(): Promise<void> {
  const sid = job.value?.session_id
  if (!sid) return
  try {
    const resp = await listJobs({ session: sid, limit: 50 })
    sessionJobs.value = [...resp.jobs].sort((a, b) => a.started_at - b.started_at)
  } catch {
    sessionJobs.value = []  // 链表是增强信息，拉取失败静默降级，不打断详情页
  }
}
```

在既有的 job 载入成功处（`watch(props.id)` / 首次 `loadJob` 之后）调用 `void loadSessionJobs()`；`doResume` 成功跳转新 job 后由新页自身的载入触发，无需手动刷新。

> **排序依据（已核验）**：`Job` **没有 `created_at` 字段**（`types.ts` 的 `Job` 仅 `started_at: number` / `ended_at?`）；`started_at` 在 **submit 时**即盖章（`internal/job/submit.go:242` `StartedAt: now`，非"运行开始"语义），故 queued 的新 job 也有非零值且等于提交时刻 —— 用它升序排即还原提交顺序。**勿写 `created_at`（typecheck 会挂）**。
>
> 全仓无 `parent_job_id`/`source_job_id`（P5 才补），故链内父子关系只能按提交时刻近似还原。单链场景准确；同一 job 分叉出多条续跑时无法区分分支 —— 这是**已知局限**，P5 落 `source_job_id` 后可精确成树。UI 文案因此用「会话内 job」而非「续跑链」，不承诺父子精度。

**9.6.2 session_id meta 行展示链长 + 可展开列表**（接 9.3 的 `v-if="job.session_id"` 块之后）：
```vue
      <div v-if="job.session_id && sessionJobs.length > 1" class="meta-item">
        <span class="meta-k mono">会话内 job</span>
        <button class="chain-toggle mono" type="button" @click="sessionJobsOpen = !sessionJobsOpen">
          共 {{ sessionJobs.length }} 个{{ sessionJobsOpen ? ' ▾' : ' ▸' }}
        </button>
      </div>
      <ol v-if="sessionJobsOpen" class="chain-list">
        <li v-for="sj in sessionJobs" :key="sj.id" class="chain-item">
          <RouterLink
            v-if="sj.id !== props.id"
            class="chain-link mono"
            :to="`/jobs/${encodeURIComponent(sj.id)}`"
          >{{ sj.id }}</RouterLink>
          <span v-else class="chain-self mono">{{ sj.id }}（当前）</span>
          <StatusBadge :status="sj.status" />
        </li>
      </ol>
```

> 阈值 `> 1`：单 job 会话（从未 resume 过）不显示该行，避免噪声。`StatusBadge` 吃 `JobStatus`，此处合法（区别于 plan 状态，见 T5）。`limit: 50` 足够覆盖任何真实 resume 链；超出不分页（同 D6 判断）。

**9.6.3 样式**：`.chain-toggle`（幽光小按钮，复用 `.resume-btn` 视觉）、`.chain-list`（去 marker 的 `ol`，缩进对齐 meta-v）、`.chain-item`（flex，id + badge 同行）、`.chain-self`（当前项弱化、不可点）。

**验收**：`pnpm typecheck && pnpm build` 绿；有 `session_id` 的 job 详情出现「继续会话」→ 展开 prompt → 「续投新 job」跳转到新 job；新 job 详情的 `plan` meta 显示继承的 plan_id（源 job 归过组时）并可回跳 plan；resume 失败（如源 job 无会话）显示错误、不跳转。**会话链**：续投后新 job 详情显示「会话内 job 共 2 个」，展开可见源 job 并点击回跳；源 job 详情同样显示 2 个（双向可达）；从未 resume 的 job **不显示**该行；**未归 plan 的独立 job 亦可经此回看全链**（本节的核心目的）。

---

### T10 —（后端，推荐）`handleListPlans`：list 内联 counts（列表进度条数据源）

> **动机**：`GET /v1/plans`（list）当前只回 `planView`、**无 counts**（`plan_handler.go:80-91`），而 T5 列表进度条需要 counts。给 list 内联 counts（handler-only，复用 detail 已用的 `PlanJobStatusCounts`+`RollupPlanCounts`），避免前端 N+1 逐个 getPlan。plans 属管理面、量小，每 plan 一次索引化 `COUNT(*) GROUP BY status` 可接受。

**`internal/httpapi/plan_handler.go`**：

**10.1 list item 结构**（`planView` 之后新增）：
```go
// planListItem 是 list 响应项：header + 其下 jobs 的 counts（列表进度条数据源，P4/T10）。
// detail（planDetail）另含 jobs/todos；list 仅带 counts 保持轻量。
type planListItem struct {
	planView
	Counts jobstore.PlanCounts `json:"counts"`
}
```

**10.2 `handleListPlans` 内联 counts**（现状 `plan_handler.go:80-91`）：
```go
func (s *Server) handleListPlans(c *rux.Context) {
	list, err := s.jobs.Meta().ListPlans(c.Query("status"), 0)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list plans failed", err.Error())
		return
	}
	out := make([]planListItem, 0, len(list))
	for _, p := range list {
		raw, cErr := s.jobs.Meta().PlanJobStatusCounts(p.PlanID)
		if cErr != nil {
			writeError(c, http.StatusInternalServerError, "plan counts failed", cErr.Error())
			return
		}
		out = append(out, planListItem{
			planView: toPlanView(p),
			Counts:   jobstore.RollupPlanCounts(raw),
		})
	}
	c.JSON(http.StatusOK, map[string]any{"plans": out})
}
```

> `PlanJobStatusCounts`（`plans.go:161`）/ `RollupPlanCounts`（`plans.go:188`）现成；web `Plan.counts` 已 optional（T1），前后端兼容。

**验收**：`go test ./internal/httpapi/...` 绿；扩展 `plan_handler_test.go`：list 响应每项含 `counts`（建 plan + attach 2 job 后 `counts.total==2`）。前端 Plans 列表进度条据此渲染。

> **偏离说明**：这是 P4 里第 2 处后端小改（第 1 处是 T8）。若要严格"P4 纯前端 + 仅 T8 后端"，可**降级**：列表进度条改用 `plan.progress`（人工字段）或不显示 counts、仅在 detail 显示——见「待确认 D2」。**倾向**：做 T10（列表有 counts 才达成设计 §10「列表：进度聚合条 counts」）。

---

### T11 — 测试与验证门禁

**11.1 前端（主机跑，容器内无 node 工具链按 workspace 约定走 gofer job/主机）**
```bash
# 主机 web 目录：gofer job run -p example-project -a exec --runner local --sync -- \
#   bash -lc 'cd <gofer>/web && pnpm typecheck && pnpm build'
pnpm typecheck   # vue-tsc --noEmit：新类型/组件零 TS 报错
pnpm build       # vue-tsc + vite build：路由懒加载组件解析、产物构建通过
```
- [ ] `pnpm typecheck` 绿（T1~T7、T9 前端全部）。
- [ ] `pnpm build` 绿（`/plans`、`/plans/:id` 懒加载路径存在，即 Plans.vue/PlanDetail.vue 已建）。

**11.2 后端（容器内）**
- [ ] `go build ./... && go vet ./...` 绿。
- [ ] `go test ./internal/job/... ./internal/httpapi/...` 绿（T8 resume 继承 + T10 list counts）。
- [ ] 全量 `go test ./...` 绿（G023 覆盖不降）。

**11.3 运行期冒烟（可选，非阻塞；`gofer serve` + 浏览器）**
- 建 plan（Plans 新建）→ `gofer job run --plan <id> ...` 或 attach 已有 job → PlanDetail 见 job + counts 刷新。
- 新增 todo（纯 + 绑 job）+ 勾选 → 刷新 done。
- 有 session_id 的 job → 「继续会话」续投 → 新 job 详情 `plan` meta 显示继承 plan（源 job 先归过组）。
- Board 选 plan 下拉 → 列表过滤；PlanDetail「在 Board 查看」深链预选。

**11.4 收尾**
- [ ] 更新总纲 `plan-orchestration-plan.md` 的 P4 checkbox（`- [ ] P4 …` → `- [x]`）+ 分子阶段提交（SR1202）。
- [ ] 若 T10 落地，主纲「P4 outline / 分期表」补一句「list 内联 counts」。

---

## 测试清单汇总

| 层 | 文件 | 用例要点 |
|---|---|---|
| 后端 job | `internal/job/resume_test.go`（新/扩展） | 源 job 带 plan_id+session → resume 新 job 继承 plan_id；源无 plan_id → 新 job plan_id 空、不报错 |
| 后端 httpapi | `internal/httpapi/plan_handler_test.go`（扩展） | list 响应每项含 `counts`（attach N job 后 `total==N`、桶计数正确） |
| 前端 | —（无测试框架） | `pnpm typecheck && pnpm build` 兜底；运行期冒烟见 11.3 |

核心不变量：
- **归组闭环**：续投 job 的 `plan_id` == 源 job 的 `plan_id`（后端继承，非客户端声明）。
- **契约逐字**：web `Plan/PlanDetail/Todo/PlanCounts` 字段与 `plan_handler.go` 的 view/`PlanCounts` 一一对齐；`Job.plan_id`、`ListJobsOpts.plan`、`ListJobsOpts.session` 与后端 tag/query 对齐。
- **列表 counts 数据源**：Plans 列表进度条依赖 T10（list 内联 counts）或降级方案（D2）；二选一，不能既要进度条又不给数据源。
- **会话链双向可达**（T9.6）：源 job 与续投 job 互相在「会话内 job」列表中可见并可点击跳转；该列表**不依赖 plan**（独立 job 亦可用）。排序字段是 `started_at`（`Job` 无 `created_at`）。

## 风险

- **R1 list 无 counts 数据源**（T5×T10）：Plans 列表要 counts 进度条，但 list 端点原本无 counts。必须落 T10（后端内联）或按 D2 降级；`Plan.counts` 已设 optional，前端渲染做 `segments(undefined)→空条` 兜底，避免 T10 未做时崩。**勿**在前端 N+1 逐个 getPlan 撑列表进度条（管理面也不该）。
- **R2 plan 状态误用 StatusBadge**（T5/T6）：`StatusBadge`/`statusColor` 只认 `JobStatus`，plan 的 `open/active/archived` 传入会落默认灰、语义丢失。plan 状态一律用本地 `.pill--*`；**只有 PlanDetail 的 jobs 表内**（那是 job 状态）才用 StatusBadge。
- **R3 resume 归组只在后端**（T8）：若漏 T8（只做前端 resume UI），续投 job 不带 plan_id、归组断链——「继续会话」能跑但 plan 里看不到续投 job。T8 是归组核心，**必须**与 T9 同期落。
- **R4 resume UI 从零**（T9）：web 此前无任何 resume 触发，需新建 client 方法 + UI + `useRouter`（JobDetail 原只 `useRoute`）。注意 resume 允许空 prompt、失败 4xx 要展示不跳转。
- **R5 Board 清空过滤连带 plan**（T7.5）：project+plan 都在 `route.query`，`clearFilters` 现有 `router.push({path:'/board'})` 会一并清掉；实施后核对空 query 确实抹掉 `?plan=`（勿只清 project 留 plan）。
- **R6 PlanDetail 轮询判据**（T6）：`isActive = running+queued>0`；若 counts 缺省（异常）恒 false → 不轮询（可接受，静态展示）。进入全终态须停轮询（仿 WorkflowDetail），避免详情页常驻轮询。
- **R7 契约漂移**（T1）：后端若后续调 view 字段名，前端类型需同步；本期以当前 `plan_handler.go` 为准逐字抄，评审时对照一遍 JSON tag。

## 待确认

> **v0.2 拍板结论（2026-07-10 用户确认）**：D1 ✅ 后端继承 / D2 ✅ 做 T10 / D3 ✅ 内联表单 / D4 ✅ 不做 / D5 ✅ 不做 / D6 ✅ 不做 / **D7 ✅ 做（新增 T9.6）**。以下保留原始权衡记录备查。

- **D7 会话链回看入口（v0.2 新增，已定：做）**：9.3 的「继续会话」跳转新 job 后无回看整链入口；已归 plan 的 job 尚可从 PlanDetail jobs 表看到兄弟，**未归 plan 的独立 job 彻底断线**（而这正是 resume 主力场景）。数据层关联本就存在（同 `session_id`，`?session=` 后端三路已就绪），仅前端未透传。→ **T9.6**，纯前端三处小改（`ListJobsOpts.session` + `listJobs` 透传 + JobDetail 展开列表），无后端改动。
  - **未纳入本期**：无 `session_id` 的 job（exec 类一次性命令）的「快速重建」按钮 —— 它没有会话可续，需新增血缘字段 `source_job_id` + 脱敏读端点 `GET /v1/jobs/{id}/request`，跨 jobstore/job/httpapi/client/web 约 8 文件。**已划归 P5**（见 `P5-lineage-rebuild-plan.md`），不入 P4。

- **D1 续跑归组方向**：确认走**后端继承**（T8，`resume.go` 加 `PlanID: src.PlanID`）而非前端传参改 resume body/签名。**倾向**：后端继承（改动最小、语义内聚、不新增可伪造入口）。
- **D2 list counts 是否做 T10**：列表进度条需要 counts，而 list 端点原无 counts。**倾向**：做 T10（handler-only 内联，达成设计 §10「列表进度聚合条 counts」）。降级备选：列表不显 counts、仅 status + 人工 `progress`，counts 只在 detail——若要严格"P4 仅 T8 一处后端改动"则选降级。
- **D3 plan 创建入口形态**：Plans.vue **内联新建表单**（title+desc） vs 单开 `/plans/new` 页（仿 NewWorkflow）。**倾向**：内联（plan 创建轻量，无需整页）。
- **D4 pty「继续」路②**：设计 §7 提到 interactive session 可走 attach 续接（P2/P3 attach 已在，JobDetail 有「打开终端」）。本期续跑只做**续投新 job**（路①）。**倾向**：路②不在 P4（attach 已能用，无新增诉求）。
- **D5 续跑是否允许改 plan**：本期续投 job **自动继承**源 plan、前端不提供改选。**倾向**：不做（设计 §7 即"自动继承"）；后续若需"续投时挪到别的 plan"再加。
- **D6 Board plan 下拉规模**：`listPlans` 全量填下拉，plan 多时下拉会长。**倾向**：管理面 plan 量可控，暂不分页/搜索；后续多了再加输入过滤（仿 tag 输入）。
