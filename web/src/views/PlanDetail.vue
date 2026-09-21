<script setup lang="ts">
// Plan 详情：getPlan 填头部 + counts 进度 + jobs 表 + decisions 决策卡 + todos 清单/看板；
// 有未终态 job 或 OPEN decision 时轮询（2.5s，H4：规划期提问也要刷新）。
//  - jobs 链入 /jobs/{id}（仿 WorkflowDetail 的 step→job）。
//  - decisions：投影成 Interaction 复用 InteractionCard（OPEN→choice/question、
//    ANSWERED→answered 回显、EXPIRED→expired 只读），作答走 answerDecision 后整刷。
//  - todos：勾选(updateTodo) / 新增(addTodo，可绑 job) / 展示绑定 job / 派发(patchTodo 置
//    ready) / 编辑派发字段(assignee、template、verify、review、runner、cwd、timeout、project)。
//  - attach：把已有 job id 补挂到本 plan（attachJob）。
//  - WEB-10 看板（PlanBoard）：同一份详情数据派生五列，拖拽落点走 patchTodo（乐观更新 +
//    失败回滚），链操作走 planRun/planPause/planResume；视图选择记 localStorage。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import PlanStatusBadge from '../components/PlanStatusBadge.vue'
import StatusBadge from '../components/StatusBadge.vue'
import InteractionCard from '../components/InteractionCard.vue'
import PlanBoard from '../components/PlanBoard.vue'
import {
  addTodo, answerDecision, attachJob, getPlan, listAgents, patchTodo, planPause, planResume,
  planRun, updatePlan, updateTodo, updateTodoStatus,
} from '../api/client'
import { fmtDateTime, fmtDuration, jobDurationSec, toUnixSec } from '../api/time'
import type {
  AgentInfo, Decision, Interaction, Job, PlanDetail, PlanStatus, Todo, TodoPatch, TodoStatus,
} from '../api/types'
import { formatTokens } from '../utils/jobOutcome'
import { boardProgress } from '../utils/planBoard'
import { progressDetail, progressSegments, progressText } from '../utils/planProgress'

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
const updating = ref(false)
const statusError = ref('')

let timer: number | null = null

// plan「进行中」= 其下有 queued/running 的 job，或存在 OPEN decision（H4：
// 规划期提问时无 running job，也要保持轮询）；据此决定是否轮询（仿 WorkflowDetail.isRunning）。
const isActive = computed(() => {
  const c = plan.value?.counts
  const todos = plan.value?.todos ?? []
  const hasOpenDecision = plan.value?.decisions?.some((d) => d.state === 'OPEN') ?? false
  return (!!c && c.running + c.queued > 0) || todos.some((t) => t.status === 'doing') || hasOpenDecision
})

// 决策通道（T4）：decision → Interaction 投影（复用 InteractionCard）。
// OPEN→pending（options 非空→choice、空→question 自由文本）、ANSWERED→answered、EXPIRED→expired。
const submittingDecisions = ref<Set<string>>(new Set())

function toDecisionInteraction(d: Decision): Interaction {
  const options = (d.options ?? []).map((o) => ({ value: o }))
  if (d.state === 'ANSWERED') {
    return {
      id: d.id,
      job_id: '',
      type: options.length > 0 ? 'choice' : 'question',
      prompt: d.question,
      status: 'answered',
      answer: d.answer,
      created_at: d.asked_at,
      answered_at: d.answered_at,
      answered_by: d.answered_by,
    }
  }
  if (d.state === 'EXPIRED') {
    return {
      id: d.id,
      job_id: '',
      type: options.length > 0 ? 'choice' : 'question',
      prompt: d.question,
      status: 'expired',
      created_at: d.asked_at,
    }
  }
  return {
    id: d.id,
    job_id: '',
    type: options.length > 0 ? 'choice' : 'question',
    prompt: d.question,
    options: options.length > 0 ? options : undefined,
    status: 'pending',
    created_at: d.asked_at,
  }
}

async function onAnswerDecision(d: Decision, value: string): Promise<void> {
  if (submittingDecisions.value.has(d.id)) return
  submittingDecisions.value = new Set(submittingDecisions.value).add(d.id)
  opError.value = ''
  try {
    await answerDecision(d.id, value)
    await fetchPlan() // 整刷：卡片变只读回显，OPEN 计数/轮询门同步
  } catch (e) {
    opError.value = e instanceof Error ? e.message : String(e)
  } finally {
    const next = new Set(submittingDecisions.value)
    next.delete(d.id)
    submittingDecisions.value = next
  }
}

// plan 头部用量汇总（PLAN-02 P2）：token/成本只累加报了数字的 job，括号里是各 agent 的
// job 数（jobs 计全部挂接的 job，没报用量也算跑了）——故「—（omp 2 jobs）」= 跑过但没采
// 集到数字。没有任何 job 时返回 ""，头部整行不渲染。
const planUsageText = computed<string>(() => {
  const u = plan.value?.usage
  if (!u || u.jobs === 0) {
    return ''
  }
  const parts: string[] = []
  if (u.total_tokens > 0) {
    parts.push(`${formatTokens(u.total_tokens)} tokens`)
  }
  if (u.cost_usd > 0) {
    parts.push(`$${u.cost_usd.toFixed(4)}`)
  }
  // 多 agent 时按 job 数降序（谁在干活一眼可见），同数按 key 稳定排序。
  const byAgent = Object.entries(u.by_agent ?? {})
    .sort((a, b) => b[1].jobs - a[1].jobs || a[0].localeCompare(b[0]))
    .map(([agent, a]) => `${agent} ${a.jobs} jobs`)
  const head = parts.length > 0 ? parts.join(' / ') : '—'
  return byAgent.length > 0 ? `${head}（${byAgent.join('、')}）` : head
})

const todoSummary = computed(() => {
  const todos = plan.value?.todos ?? []
  const doing = todos.filter((t) => t.status === 'doing').length
  const skipped = todos.filter((t) => t.status === 'skipped').length
  const complete = todos.filter((t) => t.done || t.status === 'skipped').length
  const base = `完成 ${complete}/${todos.length}${skipped ? `（含跳过 ${skipped}）` : ''}`
  return doing > 0 ? `${base} · 进行中 ${doing}` : base
})

// 生命周期状态推进（Part C §C2）：下拉即改，doing/done 时间戳由服务端自动打。
// PLAN-02 P2：ready 是「可派发」——条目此时有 assignee 且无活跃 job，服务端立刻起 job
// （与行内「派发」按钮同一条路，故两者都在这里列全）。
const TODO_STATUSES: TodoStatus[] = ['pending', 'ready', 'doing', 'done', 'skipped']

async function onTodoStatus(t: Todo, status: TodoStatus): Promise<void> {
  if (status === t.status) return
  opError.value = ''
  try {
    const updated = await updateTodoStatus(t.todo_id, status)
    // ready 可能当场派发出 job（PLAN-02 P2）：job 链接与 jobs 列表只有整刷才拿得到；
    // 其余状态就地回填即可。
    if (status === 'ready') {
      await fetchPlan()
      return
    }
    if (plan.value) {
      plan.value.todos = plan.value.todos.map((x) =>
        x.todo_id === updated.todo_id ? updated : x,
      )
    }
  } catch (e) {
    opError.value = e instanceof Error ? e.message : String(e)
  }
}

// 耗时展示：done/skipped 用 started→done 区间；doing 用 started→now。
function todoDuration(t: Todo): string {
  const start = t.started_at ?? 0
  if (!start) return ''
  const end = t.done_at && t.done_at > 0 ? t.done_at : Math.floor(Date.now() / 1000)
  const s = Math.max(0, end - start)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(s / 3600)}h${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`
}

// —— PLAN-02 P2：todo 派发 + 派发字段编辑 ——

// agents 是「给没 assignee 的条目选 agent」的候选（GET /v1/agents）。页面本没有 agent
// 列表，故按需加载一次；拉不到就退回手填 key（能填的输入框 > 一个空下拉）。
const agents = ref<AgentInfo[]>([])
let agentsLoaded = false
async function loadAgents(): Promise<void> {
  if (agentsLoaded) return
  agentsLoaded = true
  try {
    agents.value = (await listAgents()).agents ?? []
  } catch {
    agents.value = []
  }
}

// dispatching=正在 PATCH 的 todo id；dispatchFor=正在问 agent 的 todo id。
const dispatching = ref('')
const dispatchFor = ref('')
const dispatchAgent = ref('')

// todoLiveJob：该待办最近一次 job 还在跑（非终态）——行内链接据此高亮，回答「派发出去了吗」。
const LIVE_JOB_STATUSES = new Set([
  'queued', 'running', 'pending_interaction', 'recovering', 'waiting_dir', 'needs_review',
])
function todoLiveJob(t: Todo): boolean {
  const latest = t.jobs && t.jobs.length > 0 ? t.jobs[0] : undefined
  return latest ? LIVE_JOB_STATUSES.has(latest.status) : false
}

function onDispatch(t: Todo): void {
  if (dispatching.value) return
  if (!t.assignee) {
    // 没人认领的条目：先问 agent，再和 ready 一起发。
    dispatchFor.value = dispatchFor.value === t.todo_id ? '' : t.todo_id
    dispatchAgent.value = ''
    if (dispatchFor.value) void loadAgents()
    return
  }
  void dispatchTodo(t, '')
}

function onConfirmDispatch(t: Todo): void {
  const agent = dispatchAgent.value.trim()
  if (!agent) return
  void dispatchTodo(t, agent)
}

// dispatchTodo 走 PATCH /v1/todos/{id}（PLAN-02 P2）：status=ready（+ assignee）就是派发。
// 服务端在这次写入里起 job（条目转 doing 并拿到 job_id），故随后整刷详情——job 链接与
// counts 都从服务端取，不在前端拼。
async function dispatchTodo(t: Todo, agent: string): Promise<void> {
  if (dispatching.value) return
  dispatching.value = t.todo_id
  opError.value = ''
  try {
    const patch: TodoPatch = { status: 'ready' }
    if (agent) patch.assignee = agent
    await patchTodo(t.todo_id, patch)
    dispatchFor.value = ''
    await fetchPlan()
  } catch (e) {
    opError.value = e instanceof Error ? e.message : String(e)
  } finally {
    dispatching.value = ''
  }
}

// 派发字段编辑面板（PLAN-02 P2）：一次 PATCH 描述「这个条目派发时用什么」。
// 空串是【显式清空】（assignee 解除指派、cwd 回默认），而 verify/timeout 留空 = 不改
// —— 与后端 nil=保持、空值=清空同一约定（见 TodoPatch）。
interface TodoEditForm {
  assignee: string
  project: string
  template: string
  verify: string
  review: boolean
  runner: string
  cwd: string
  timeoutSec: string
}

const editFor = ref('')
const savingEdit = ref(false)
const editForm = ref<TodoEditForm>(emptyEditForm())

function emptyEditForm(): TodoEditForm {
  return {
    assignee: '', project: '', template: '', verify: '',
    review: false, runner: '', cwd: '', timeoutSec: '',
  }
}

function toggleTodoEdit(t: Todo): void {
  if (editFor.value === t.todo_id) {
    editFor.value = ''
    return
  }
  editFor.value = t.todo_id
  editForm.value = {
    assignee: t.assignee ?? '',
    project: t.project ?? '',
    template: t.template ?? '',
    // verify 是 argv，编辑时按空格拼成一行（与 CLI --verify 的输入方式一致）。
    verify: (t.verify ?? []).join(' '),
    review: t.review === true,
    runner: t.runner ?? '',
    cwd: t.cwd ?? '',
    timeoutSec: t.timeout_sec ? String(t.timeout_sec) : '',
  }
}

async function saveTodoEdit(t: Todo): Promise<void> {
  if (savingEdit.value) return
  const f = editForm.value
  const patch: TodoPatch = {
    // 这五个字符串总是发：空串即「清空」，不发就再也改不回去。
    assignee: f.assignee.trim(),
    project: f.project.trim(),
    template: f.template.trim(),
    runner: f.runner.trim(),
    cwd: f.cwd.trim(),
    review: f.review,
  }
  // verify 一行命令 → argv（空格分词）；留空 = 不改（去掉验收步骤走 CLI/MCP 的 verify:[]）。
  const verify = f.verify.trim().split(/\s+/).filter(Boolean)
  if (verify.length > 0) {
    patch.verify = verify
  }
  const sec = Number(f.timeoutSec.trim())
  if (f.timeoutSec.trim() !== '' && Number.isFinite(sec) && sec > 0) {
    patch.timeout_sec = sec
  }
  savingEdit.value = true
  opError.value = ''
  try {
    await patchTodo(t.todo_id, patch)
    editFor.value = ''
    await fetchPlan() // 整刷：assignee 徽标 / dispatch_error 都可能因这次写入变
  } catch (e) {
    opError.value = e instanceof Error ? e.message : String(e)
  } finally {
    savingEdit.value = false
  }
}

// —— WEB-10 看板 ——

// 视图选择（默认看板）：列表/看板同一份数据，只换渲染方式；选择记 localStorage。
const VIEW_KEY = 'gofer.plans.view'
const view = ref<'board' | 'list'>(readView())
function readView(): 'board' | 'list' {
  try {
    return localStorage.getItem(VIEW_KEY) === 'list' ? 'list' : 'board'
  } catch {
    return 'board'
  }
}
function setView(next: 'board' | 'list'): void {
  view.value = next
  try {
    localStorage.setItem(VIEW_KEY, next)
  } catch {
    /* ignore storage failures */
  }
  if (next === 'board') void loadAgents() // 看板拖到 ready 时要用 agent 列表
}

// 头部进度条（done + skipped / total）：与看板列头同源，planProgress 的 job 口径不动。
const boardPct = computed(() => boardProgress(plan.value?.todos ?? []))

// 拖拽/写入进行中：暂停轮询写回（重渲染会打断拖拽，也会把乐观更新打回旧值）。
const boardDragging = ref(false)
const moving = ref<Set<string>>(new Set())
const boardBusy = computed(() => boardDragging.value || moving.value.size > 0)

function setMoving(id: string, on: boolean): void {
  const next = new Set(moving.value)
  if (on) next.add(id)
  else next.delete(id)
  moving.value = next
}

// 乐观更新：先就地改本地条目，PATCH 失败再把整条旧对象换回去（回滚 + toast）。
function patchTodoLocal(todoId: string, patch: Partial<Todo>): Todo | null {
  const prev = plan.value?.todos.find((x) => x.todo_id === todoId) ?? null
  if (plan.value && prev) {
    plan.value.todos = plan.value.todos.map((x) =>
      x.todo_id === todoId ? { ...x, ...patch } : x,
    )
  }
  return prev
}

const MOVE_TOAST: Record<TodoStatus, string> = {
  pending: '已撤回到待办',
  ready: '已派发',
  doing: '已置为进行中',
  done: '已标记完成',
  skipped: '已跳过',
}

// 看板落点：PATCH /v1/todos/{id}，status 就是落点；ready + assignee 由服务端当场起 job。
async function onBoardMove(payload: {
  todo: Todo
  status: TodoStatus
  assignee?: string
}): Promise<void> {
  const t = payload.todo
  if (moving.value.has(t.todo_id)) {
    return
  }
  const prev = patchTodoLocal(t.todo_id, {
    status: payload.status,
    ...(payload.assignee ? { assignee: payload.assignee } : {}),
  })
  setMoving(t.todo_id, true)
  opError.value = ''
  const patch: TodoPatch = { status: payload.status }
  if (payload.assignee) {
    patch.assignee = payload.assignee
  }
  try {
    await patchTodo(t.todo_id, patch)
    setMoving(t.todo_id, false)
    await fetchPlan() // 服务端真相：ready 可能当场起 job（条目转 doing 并挂上 job_id）
    const head = payload.assignee
      ? `${MOVE_TOAST[payload.status]}（${payload.assignee}）`
      : MOVE_TOAST[payload.status]
    setToast(`${head}：${t.title}`)
  } catch (e) {
    if (plan.value && prev) {
      plan.value.todos = plan.value.todos.map((x) => (x.todo_id === prev.todo_id ? prev : x))
    }
    setMoving(t.todo_id, false)
    setToast(`操作失败，已回滚：${e instanceof Error ? e.message : String(e)}`)
  }
}

function onBoardDrag(active: boolean): void {
  boardDragging.value = active
}

// plan 链操作（PLAN-03）：run=启动链（先解除 pause/block，把依赖已满足、已指派的 pending
// 条目置 ready 并派发）、pause=挂起自动推进（已在跑的 job 不取消）、resume=解除 pause/block
// 并继续推进。三者都返回 plan 头部快照，故就地合并再整刷一次拿条目/job。
const acting = ref('')
const confirmRun = ref(false)
async function onPlanAction(kind: 'run' | 'pause' | 'resume'): Promise<void> {
  if (acting.value) {
    return
  }
  acting.value = kind
  confirmRun.value = false
  opError.value = ''
  try {
    const fn = kind === 'run' ? planRun : kind === 'pause' ? planPause : planResume
    const head = await fn(props.id)
    // paused/blocked_todo 是 omitempty：服务端在 false/空 时根本不发这个键，直接 spread
    // 会留着旧值（解除挂起后横幅还在），故显式补默认。
    plan.value = {
      ...plan.value!,
      ...head,
      paused: head.paused ?? false,
      blocked_todo: head.blocked_todo ?? '',
    }
    await fetchPlan()
    setToast(kind === 'run' ? '已启动链' : kind === 'pause' ? '已挂起自动推进' : '已继续推进')
  } catch (e) {
    opError.value = e instanceof Error ? e.message : String(e)
  } finally {
    acting.value = ''
  }
}

// blocked 横幅的两个出口（PLAN-03）：重派 = 该条目回 ready、跳过 = 该条目 skipped——服务端
// 在这两种写入里都会解除 block 并继续推进链（httpapi 的 PlanTodoChanged）。
async function onReleaseBlocked(status: 'ready' | 'skipped'): Promise<void> {
  const id = plan.value?.blocked_todo
  if (!id || acting.value) {
    return
  }
  acting.value = `blocked:${status}`
  opError.value = ''
  try {
    await patchTodo(id, { status })
    await fetchPlan()
    setToast(status === 'ready' ? '已重派被阻塞的条目' : '已跳过被阻塞的条目')
  } catch (e) {
    opError.value = e instanceof Error ? e.message : String(e)
  } finally {
    acting.value = ''
  }
}

const blockedTitle = computed(() => {
  const id = plan.value?.blocked_todo
  if (!id) {
    return ''
  }
  return plan.value?.todos.find((t) => t.todo_id === id)?.title ?? id
})

// 操作结果浮层（同 ReviewQueue 的口径）：点一下关掉，8s 自动消失。
const toast = ref('')
let toastTimer: number | null = null
function setToast(text: string): void {
  toast.value = text
  if (toastTimer != null) {
    window.clearTimeout(toastTimer)
  }
  toastTimer = window.setTimeout(() => {
    toast.value = ''
    toastTimer = null
  }, 8000)
}

const canFinish = computed(() => {
  const c = plan.value?.counts
  // 「可以收尾」= 没有 queued/running 的 job，且待办为空或全部 done/skipped。仅作提示，不自动改状态（C2）。
  const todos = plan.value?.todos ?? []
  const todosDone = todos.length === 0 || todos.every((t) => t.done || t.status === 'skipped')
  return !!c && c.queued === 0 && c.running === 0 && todosDone
})

async function fetchPlan(): Promise<void> {
  // 看板拖拽/写入进行中不写回：一次重渲染会把正在拖的卡片换掉，也会把乐观更新打回旧值。
  if (boardBusy.value) {
    return
  }
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

async function setStatus(next: PlanStatus): Promise<void> {
  if (updating.value) return
  updating.value = true
  try {
    plan.value = { ...plan.value!, ...(await updatePlan(props.id, next)) }
    statusError.value = ''
  } catch (e) {
    statusError.value = e instanceof Error ? e.message : String(e)
  } finally {
    updating.value = false
  }
}

function openJob(jobId: string): void {
  void router.push(`/jobs/${encodeURIComponent(jobId)}`)
}

// 「在 Board 查看该 plan 的 job」深链（复用 T7 的 ?plan= 过滤）。
function viewInBoard(): void {
  void router.push({ path: '/board', query: { plan: props.id } })
}

// 进度条 / 进度文字 / 副信息见 utils/planProgress：completion（待办优先、无待办回落 job）；
// 旧服务端无 completion 时回落原 job counts 显示。

function shortId(id: string): string {
  return id.length > 14 ? id.slice(-14) : id
}

// todoJobRef 是该待办最近一次 job 的 id（SUP-01 C）：挂接列表（服务端新→旧）的第一条
// 优先，手工绑定的 job_id 兜底——纯待办两者都为空。
function todoJobRef(t: Todo): string {
  const latest = t.jobs && t.jobs.length > 0 ? t.jobs[0] : undefined
  return latest ? latest.id : t.job_id || ''
}

// todoJobsTitle 是待办行的悬浮明细：每一次挂接 job 的 id/状态/agent/用时。
function todoJobsTitle(t: Todo): string {
  return (t.jobs ?? [])
    .map((j) => `${j.id} ${j.status} ${j.agent || '-'} ${j.duration_sec ?? 0}s`)
    .join('\n')
}

function rowDuration(j: Job): string {
  return fmtDuration(jobDurationSec(j))
}

function rowStartTime(j: Job): string {
  const sec = toUnixSec(j.started_at)
  if (sec == null) {
    return '—'
  }
  const d = new Date(sec * 1000)
  return [
    String(d.getHours()).padStart(2, '0'),
    String(d.getMinutes()).padStart(2, '0'),
    String(d.getSeconds()).padStart(2, '0'),
  ].join(':')
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
  // 看板是默认视图：拖到 ready 要先选 agent，故进页面就备好候选列表（一次 GET /v1/agents）。
  if (view.value === 'board') void loadAgents()
  document.addEventListener('visibilitychange', onVisibility)
})
onUnmounted(() => {
  stopPolling()
  if (toastTimer != null) window.clearTimeout(toastTimer)
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="detail" :class="{ 'detail--board': view === 'board' }">
    <div class="detail-head">
      <button class="back mono" type="button" @click="router.push('/plans')">
        &larr; plans
      </button>
      <div v-if="plan" class="head-status">
        <PlanStatusBadge :status="plan.status" />
        <div class="status-actions mono">
          <button
            v-if="plan.status === 'open' || plan.status === 'active'"
            class="status-action"
            type="button"
            :disabled="updating"
            @click="setStatus('done')"
          >
            标记完成
          </button>
          <button
            v-if="plan.status === 'open' || plan.status === 'active' || plan.status === 'done'"
            class="status-action"
            type="button"
            :disabled="updating"
            @click="setStatus('archived')"
          >
            归档
          </button>
          <button
            v-if="plan.status === 'done' || plan.status === 'archived'"
            class="status-action"
            type="button"
            :disabled="updating"
            @click="setStatus('open')"
          >
            重新打开
          </button>
        </div>
        <button class="board-btn mono" type="button" @click="viewInBoard">
          在 Board 查看
        </button>
      </div>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>
    <p v-if="opError" class="error mono">{{ opError }}</p>
    <p v-if="statusError" class="error mono">{{ statusError }}</p>

    <div v-if="plan" class="head-card">
      <h1 class="plan-title">{{ plan.title || plan.plan_id }}</h1>
      <dl class="meta mono">
        <div class="meta-row">
          <dt>id</dt>
          <dd>{{ plan.plan_id }}</dd>
        </div>
        <div class="meta-row">
          <dt>status</dt>
          <dd><PlanStatusBadge :status="plan.status" /></dd>
        </div>
        <div v-if="plan.owner" class="meta-row">
          <dt>owner</dt>
          <dd>{{ plan.owner }}</dd>
        </div>
        <!-- PLAN-02 P2：该 plan 的待办派发进哪个 project（待办可自带 project 覆盖）。 -->
        <div v-if="plan.project" class="meta-row">
          <dt>project</dt>
          <dd>{{ plan.project }}</dd>
        </div>
        <div v-if="plan.description" class="meta-row">
          <dt>description</dt>
          <dd>{{ plan.description }}</dd>
        </div>
        <div v-if="plan.progress != null" class="meta-row">
          <dt>progress</dt>
          <dd>{{ plan.progress }}</dd>
        </div>
        <div class="meta-row">
          <dt>created</dt>
          <dd>{{ fmtDateTime(plan.created_at) }}</dd>
        </div>
        <div class="meta-row">
          <dt>updated</dt>
          <dd>{{ fmtDateTime(plan.updated_at) }}</dd>
        </div>
      </dl>
    </div>

    <!-- WEB-10 头部操作条：进度（done+skipped/total）+ 用量汇总 + 链操作（PLAN-03）。 -->
    <div v-if="plan" class="ops mono">
      <div class="ops-progress" :title="`完成（含跳过）${boardPct.done}/${boardPct.total}`">
        <span class="cbar" aria-hidden="true">
          <span class="seg seg--done" :style="{ width: `${boardPct.percent ?? 0}%` }"></span>
        </span>
        <span class="ops-frac">
          {{ boardPct.total === 0 ? '—' : `${boardPct.done}/${boardPct.total}` }}
        </span>
      </div>
      <!-- PLAN-02 P2：plan 级用量汇总（挂接 job 的 token/成本 + 各 agent 的 job 数）。 -->
      <span v-if="planUsageText" class="ops-usage">{{ planUsageText }}</span>
      <span class="ops-actions">
        <!-- run：启动链（先解除 pause/block），会当场把依赖已满足、已指派的条目置 ready → 二次确认。 -->
        <template v-if="confirmRun">
          <span class="ops-hint">启动链？</span>
          <button
            class="status-action"
            type="button"
            :disabled="!!acting"
            @click="onPlanAction('run')"
          >
            确认启动
          </button>
          <button class="status-action" type="button" @click="confirmRun = false">取消</button>
        </template>
        <button
          v-else-if="plan.status !== 'archived'"
          class="status-action"
          type="button"
          :disabled="!!acting"
          @click="confirmRun = true"
        >
          启动链
        </button>
        <!-- pause/resume：paused 时给「继续」；blocked 时同样给「继续」（resume 一并解除 block）。 -->
        <button
          v-if="plan.paused"
          class="status-action"
          type="button"
          :disabled="!!acting"
          @click="onPlanAction('resume')"
        >
          {{ acting === 'resume' ? '继续中…' : '继续' }}
        </button>
        <button
          v-else-if="plan.status === 'blocked'"
          class="status-action"
          type="button"
          :disabled="!!acting"
          @click="onPlanAction('resume')"
        >
          {{ acting === 'resume' ? '解除中…' : '解除阻塞' }}
        </button>
        <button
          v-else-if="plan.status !== 'done' && plan.status !== 'archived'"
          class="status-action"
          type="button"
          :disabled="!!acting"
          @click="onPlanAction('pause')"
        >
          {{ acting === 'pause' ? '挂起中…' : '挂起' }}
        </button>
      </span>
    </div>

    <!-- PLAN-03 blocked 横幅：链失败停在哪个条目 + 两个出口（重派 / 跳过都会解除 block 并继续）。 -->
    <div v-if="plan && plan.blocked_todo" class="blocked mono">
      <span class="blocked-text">
        链停在「{{ blockedTitle }}」（{{ plan.blocked_todo }}）——重派它继续跑，跳过它则往后走
      </span>
      <span class="ops-actions">
        <button
          class="status-action"
          type="button"
          :disabled="!!acting"
          @click="onReleaseBlocked('ready')"
        >
          重派
        </button>
        <button
          class="status-action"
          type="button"
          :disabled="!!acting"
          @click="onReleaseBlocked('skipped')"
        >
          跳过
        </button>
      </span>
    </div>

    <section v-if="plan" class="section">
      <h2 class="section-title mono">PROGRESS</h2>
      <div class="counts-card mono">
        <div class="count-line">
          <span class="cbar" aria-hidden="true">
            <span
              v-for="s in progressSegments(plan)"
              :key="s.cls"
              class="seg"
              :class="s.cls"
              :style="{ width: `${s.pct}%` }"
            ></span>
          </span>
          <span class="count-frac">{{ progressText(plan) }}</span>
        </div>
        <p v-if="canFinish && plan.status !== 'done'" class="finish-hint">
          job 与待办都已收尾，可标记完成
        </p>
        <div v-if="plan.completion?.basis === 'todos'" class="legend">
          <span><i class="dot dot--done"></i>done / skipped</span>
          <span><i class="dot dot--run"></i>doing</span>
        </div>
        <div v-else class="legend">
          <span><i class="dot dot--done"></i>done</span>
          <span><i class="dot dot--run"></i>running</span>
          <span><i class="dot dot--fail"></i>failed</span>
          <span><i class="dot dot--queue"></i>queued</span>
        </div>
        <p v-if="progressDetail(plan)" class="count-detail">{{ progressDetail(plan) }}</p>
      </div>
    </section>

    <section v-if="plan" class="section">
      <h2 class="section-title mono">JOBS ({{ plan.jobs.length }})</h2>
      <div class="jobs-table">
        <div class="jobs-head mono">
          <span>状态</span>
          <span>job · title / id</span>
          <span>agent</span>
          <span>runner</span>
          <span>开始</span>
          <span>耗时</span>
        </div>
        <button
          v-for="j in plan.jobs"
          :key="j.id"
          class="job-row"
          type="button"
          @click="openJob(j.id)"
        >
          <span class="job-status"><StatusBadge :status="j.status" :holder="j.waiting_on_job" /></span>
          <span class="job-main" :class="{ 'job-main--titled': j.title }">
            <span v-if="j.title" class="job-title" :title="j.title">{{ j.title }}</span>
            <span class="job-id mono" :title="j.id">{{ shortId(j.id) }}</span>
          </span>
          <span class="job-dim mono">{{ j.agent }}</span>
          <span class="job-dim mono">{{ j.runner }}</span>
          <span class="job-dim mono">{{ rowStartTime(j) }}</span>
          <span class="job-dim mono">{{ rowDuration(j) }}</span>
        </button>
        <div v-if="plan.jobs.length === 0" class="empty mono">该计划暂无 job</div>
      </div>
    </section>

    <section v-if="plan && (plan.decisions?.length ?? 0) > 0" class="section">
      <h2 class="section-title mono">DECISIONS ({{ plan.decisions!.length }})</h2>
      <InteractionCard
        v-for="d in plan.decisions"
        :key="d.id"
        :interaction="toDecisionInteraction(d)"
        :is-decision="true"
        :submitting="submittingDecisions.has(d.id)"
        @answer="onAnswerDecision(d, $event)"
      />
    </section>

    <section v-if="plan" class="section">
      <div class="section-head">
        <h2 class="section-title mono">TODOS ({{ todoSummary }})</h2>
        <!-- WEB-10 视图切换：列表/看板同一份数据；选择记 localStorage，默认看板。 -->
        <div class="view-switch mono">
          <button
            class="view-btn"
            :class="{ 'view-btn--on': view === 'board' }"
            type="button"
            @click="setView('board')"
          >
            看板
          </button>
          <button
            class="view-btn"
            :class="{ 'view-btn--on': view === 'list' }"
            type="button"
            @click="setView('list')"
          >
            列表
          </button>
        </div>
      </div>
      <!-- 看板：拖到 ready 即派发（无 assignee 先选 agent）；done/skipped 落点先确认。 -->
      <PlanBoard
        v-if="view === 'board'"
        :todos="plan.todos"
        :jobs="plan.jobs"
        :agents="agents"
        :busy-ids="moving"
        @move="onBoardMove"
        @drag-active="onBoardDrag"
      />
      <div v-else class="todos">
        <div v-for="t in plan.todos" :key="t.todo_id" class="todo-item">
          <label class="todo-row">
            <input type="checkbox" :checked="t.done" @change="onToggleTodo(t)" />
            <span class="todo-main">
              <span class="todo-title" :class="{ 'todo-title--done': t.done }">{{ t.title }}</span>
              <!-- PLAN-02 P2：认领这个条目的 agent（空 = 没人认领，故派发要先选 agent）。 -->
              <span v-if="t.assignee" class="todo-assignee mono" :title="`assignee: ${t.assignee}`">
                {{ t.assignee }}
              </span>
              <span class="todo-actions">
                <button
                  class="todo-dispatch mono"
                  type="button"
                  :disabled="dispatching === t.todo_id"
                  :title="
                    t.assignee
                      ? `派发：置 ready，服务端立刻用 ${t.assignee} 起一个 job`
                      : '派发：先选 agent（assignee），再置 ready'
                  "
                  @click.prevent="onDispatch(t)"
                >
                  {{ dispatching === t.todo_id ? '派发中…' : '派发' }}
                </button>
                <button
                  class="todo-edit-btn mono"
                  :class="{ 'todo-edit-btn--on': editFor === t.todo_id }"
                  type="button"
                  title="编辑派发字段（assignee/template/verify/review/runner/cwd/timeout/project）"
                  @click.prevent="toggleTodoEdit(t)"
                >
                  {{ editFor === t.todo_id ? '收起' : '编辑' }}
                </button>
              </span>
            </span>
            <select
              class="todo-status mono"
              :class="`todo-status--${t.status}`"
              :value="t.status"
              :title="t.started_at ? `开始 ${new Date(t.started_at * 1000).toLocaleString()}` : ''"
              @change="onTodoStatus(t, ($event.target as HTMLSelectElement).value as TodoStatus)"
            >
              <option v-for="st in TODO_STATUSES" :key="st" :value="st">{{ st }}</option>
            </select>
            <span v-if="todoDuration(t)" class="todo-duration mono" :title="t.done_at ? `完结 ${new Date(t.done_at * 1000).toLocaleString()}` : '进行中'">
              {{ todoDuration(t) }}
            </span>
            <span
              v-if="t.jobs && t.jobs.length > 1"
              class="todo-job-count mono"
              :title="todoJobsTitle(t)"
            >
              {{ t.jobs.length }} jobs
            </span>
            <button
              v-if="todoJobRef(t)"
              class="todo-job mono"
              :class="{ 'todo-job--live': todoLiveJob(t) }"
              type="button"
              :title="todoJobRef(t)"
              @click.prevent="openJob(todoJobRef(t))"
            >
              job {{ shortId(todoJobRef(t)) }} &rarr;
            </button>
          </label>
          <!-- 没人认领的条目：先选 agent，再和 status=ready 一起发（同一次 PATCH）。 -->
          <div v-if="dispatchFor === t.todo_id" class="todo-dispatch-form mono">
            <span class="dispatch-label">agent</span>
            <select v-if="agents.length > 0" v-model="dispatchAgent" class="op-input mono">
              <option v-for="a in agents" :key="a.key" :value="a.key">{{ a.key }} · {{ a.type }}</option>
            </select>
            <input
              v-else
              v-model="dispatchAgent"
              class="op-input mono"
              placeholder="agent key（如 omp）"
            />
            <button
              class="op-btn mono"
              type="button"
              :disabled="!dispatchAgent.trim() || dispatching === t.todo_id"
              @click.prevent="onConfirmDispatch(t)"
            >
              确认派发
            </button>
            <button class="op-btn mono" type="button" @click.prevent="dispatchFor = ''">取消</button>
          </div>
          <!-- PLAN-02 P2：最近一次派发没起 job 的原因（服务端写在条目上，成功后清空）。 -->
          <p v-if="t.dispatch_error" class="todo-dispatch-error mono">
            派发失败：{{ t.dispatch_error }}
          </p>
          <p v-if="t.note" class="todo-note mono">{{ t.note }}</p>
          <!-- 派发字段编辑面板：prefill 自该条目，保存走同一条 PATCH（见 saveTodoEdit）。 -->
          <div v-if="editFor === t.todo_id" class="todo-edit mono">
            <label class="edit-field">
              <span>assignee</span>
              <input v-model="editForm.assignee" class="op-input mono" placeholder="agent key" />
            </label>
            <label class="edit-field">
              <span>project</span>
              <input v-model="editForm.project" class="op-input mono" placeholder="覆盖 plan 的 project" />
            </label>
            <label class="edit-field">
              <span>template</span>
              <input v-model="editForm.template" class="op-input mono" placeholder="任务书模板（空=默认 prompt）" />
            </label>
            <label class="edit-field">
              <span>verify</span>
              <input v-model="editForm.verify" class="op-input mono" placeholder="验收命令（空格分词为 argv）" />
            </label>
            <label class="edit-field">
              <span>runner</span>
              <input v-model="editForm.runner" class="op-input mono" placeholder="local / worker:&lt;id&gt;" />
            </label>
            <label class="edit-field">
              <span>cwd</span>
              <input v-model="editForm.cwd" class="op-input mono" placeholder="项目内相对路径" />
            </label>
            <label class="edit-field">
              <span>timeout_sec</span>
              <input
                v-model="editForm.timeoutSec"
                class="op-input mono"
                inputmode="numeric"
                placeholder="秒（空=默认）"
              />
            </label>
            <label class="edit-check">
              <input v-model="editForm.review" type="checkbox" />
              <span>review（完成后要人验收）</span>
            </label>
            <div class="edit-actions">
              <button
                class="op-btn mono"
                type="button"
                :disabled="savingEdit"
                @click.prevent="saveTodoEdit(t)"
              >
                {{ savingEdit ? '保存中…' : '保存' }}
              </button>
              <button class="op-btn mono" type="button" @click.prevent="editFor = ''">取消</button>
            </div>
          </div>
        </div>
        <div v-if="plan.todos.length === 0" class="empty mono">暂无待办</div>
      </div>
      <form class="op-form mono" @submit.prevent="onAddTodo">
        <input v-model="newTodoTitle" class="op-input mono" placeholder="待办标题" />
        <input v-model="newTodoJob" class="op-input mono" placeholder="绑定 job id(可选)" />
        <button class="op-btn mono" type="submit" :disabled="!newTodoTitle.trim() || addingTodo">
          {{ addingTodo ? '新增中…' : '新增待办' }}
        </button>
      </form>
    </section>

    <section v-if="plan" class="section">
      <h2 class="section-title mono">ATTACH JOB</h2>
      <form class="op-form mono" @submit.prevent="onAttach">
        <input v-model="attachJobId" class="op-input mono" placeholder="已有 job id" />
        <button class="op-btn mono" type="submit" :disabled="!attachJobId.trim() || attaching">
          {{ attaching ? '挂载中…' : '挂到本计划' }}
        </button>
      </form>
    </section>

    <p v-else-if="!error" class="loading mono">加载中…</p>

    <!-- 看板拖拽/链操作的结果：右下角浮层，点一下关掉，8s 自动消失。 -->
    <div v-if="toast" class="pd-toast mono" role="status" @click="toast = ''">
      {{ toast }}
    </div>
  </div>
</template>

<style scoped>
.detail {
  max-width: 980px;
  margin: 0 auto;
}
/* 看板视图放宽容器：五列 × 220px 起，980px 放不下会横向滚动（见 PlanBoard 的 .board）。 */
.detail--board {
  max-width: 1400px;
}
.detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.back {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.back:hover {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.head-status {
  display: inline-flex;
  align-items: center;
  gap: 12px;
}
.status-actions {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.status-action {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 8px;
  font-size: 12px;
}
.status-action:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.status-action:disabled {
  opacity: 0.55;
  cursor: default;
}
.board-btn,
.op-btn {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.board-btn:hover,
.op-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.op-btn:disabled {
  opacity: 0.55;
  cursor: default;
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}

.head-card {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 16px;
  margin-bottom: 18px;
  background: var(--panel);
}
.plan-title {
  font-size: 18px;
  color: var(--paper);
  margin: 0 0 12px;
  word-break: break-word;
}
.meta {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 12px;
}
.meta-row {
  display: grid;
  grid-template-columns: 90px 1fr;
  gap: 10px;
}
.meta-row dt {
  color: var(--queue);
  letter-spacing: 0.04em;
}
.meta-row dd {
  margin: 0;
  color: var(--paper);
  word-break: break-all;
}

.section {
  margin-top: 18px;
}
/* WEB-10 头部操作条：进度条 + 用量 + 链操作按钮，一行放不下就折行。 */
.ops {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
  padding: 10px 14px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.ops-progress {
  display: grid;
  grid-template-columns: minmax(120px, 220px) 56px;
  align-items: center;
  gap: 10px;
}
.ops-frac {
  color: var(--paper);
  font-size: 12px;
  text-align: right;
}
.ops-usage {
  color: var(--queue);
  font-size: 12px;
  word-break: break-word;
}
.ops-actions {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-left: auto;
}
.ops-hint {
  color: var(--run);
  font-size: 12px;
}
/* blocked 横幅（PLAN-03）：链失败停在某条目——比状态徽标更该被看见，故整条描红。 */
.blocked {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 10px;
  padding: 10px 14px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  font-size: 12px;
}
.blocked-text {
  color: var(--fail);
  word-break: break-word;
}
/* 列表/看板切换：两个小按钮，选中态描 phosphor。 */
.view-switch {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.view-btn {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 12px;
}
.view-btn:hover,
.view-btn--on {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
/* 操作结果浮层：右下角，点一下关掉，8s 自动消失（与验收台同形）。 */
.pd-toast {
  position: fixed;
  right: 16px;
  bottom: 16px;
  z-index: 50;
  max-width: 460px;
  padding: 8px 12px;
  background: var(--panel);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  color: var(--paper);
  font-size: 12px;
  cursor: pointer;
  word-break: break-word;
}
.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.section-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--queue);
  text-transform: uppercase;
  margin: 0 0 10px;
}

.counts-card {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 12px 14px;
}
.count-line {
  display: grid;
  grid-template-columns: minmax(160px, 1fr) 60px;
  align-items: center;
  gap: 12px;
}
.count-frac {
  color: var(--paper);
  font-size: 13px;
  text-align: right;
}
.count-detail {
  color: var(--queue);
  font-size: 12px;
  margin: 8px 0 0;
}
.finish-hint {
  color: var(--queue);
  font-size: 12px;
  margin: 8px 0 0;
}
.legend {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 8px;
  color: var(--queue);
  font-size: 11px;
}
.legend span {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
.dot--done {
  background: var(--done);
}
.dot--run {
  background: var(--run);
}
.dot--fail {
  background: var(--fail);
}
.dot--queue {
  background: var(--queue);
}

.jobs-table,
.todos {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.jobs-head,
.job-row {
  display: grid;
  grid-template-columns: 130px minmax(180px, 1fr) 100px 100px 72px 80px;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
}
.jobs-head {
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}
.job-row {
  width: 100%;
  background: transparent;
  color: var(--paper);
  border: 0;
  border-bottom: 1px solid var(--line);
  text-align: left;
  font-size: 13px;
}
.job-row:last-child {
  border-bottom: none;
}
.job-row:hover,
.job-row:focus-visible {
  background: var(--panel);
}
.job-main {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.job-title {
  color: var(--paper);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.job-id {
  color: var(--phosphor);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.job-main:not(.job-main--titled) .job-id {
  font-size: 13px;
}
.job-dim {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.todo-item {
  border-bottom: 1px solid var(--line);
}
.todo-item:last-child {
  border-bottom: none;
}
.todo-row {
  display: grid;
  grid-template-columns: 22px minmax(160px, 1fr) auto auto auto auto;
  align-items: center;
  gap: 10px;
  padding: 9px 14px;
  font-size: 13px;
}
/* 标题列：标题 + assignee 徽标 + 行内动作（派发/编辑），动作右贴该列末尾。 */
.todo-main {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.todo-assignee {
  flex: none;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 6px;
  font-size: 11px;
}
.todo-actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
  flex: none;
}
.todo-dispatch,
.todo-edit-btn {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 12px;
}
.todo-dispatch:hover:not(:disabled),
.todo-edit-btn:hover,
.todo-edit-btn--on {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.todo-dispatch:disabled {
  opacity: 0.55;
  cursor: default;
}
/* 派发前选 agent（没 assignee 的条目）：与 note 同缩进，贴在行下。 */
.todo-dispatch-form {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 14px 9px 46px;
}
.todo-dispatch-form .op-input {
  flex: 0 1 220px;
}
.dispatch-label {
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.04em;
}
.todo-dispatch-error {
  margin: 0;
  padding: 0 14px 9px 46px;
  color: var(--fail);
  font-size: 12px;
  word-break: break-word;
}
/* 派发字段编辑面板：字段自动折行，保存/取消在末行。 */
.todo-edit {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
  gap: 8px 12px;
  padding: 0 14px 12px 46px;
}
.edit-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.04em;
}
.edit-check {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--queue);
  font-size: 12px;
}
.edit-check input {
  accent-color: var(--phosphor);
}
.edit-actions {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
/* 状态下拉：安静的行内控件，按态着色（doing=run/done=done/skipped=queue） */
.todo-status {
  background: var(--term-bg);
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 6px;
  font-size: 11px;
}
.todo-status--doing {
  color: var(--run);
  border-color: var(--run);
}
/* ready = 已布防（有 assignee，等一次派发）：与 pending 的静默区分开。 */
.todo-status--ready {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.todo-status--done {
  color: var(--done);
}
.todo-status--skipped {
  color: var(--queue);
}
.todo-duration {
  color: var(--paper);
  font-size: 11px;
}
.todo-note {
  margin: 0;
  padding: 0 14px 9px 46px;
  color: var(--queue);
  font-size: 12px;
  word-break: break-word;
  white-space: pre-line;
}
.todo-row input {
  accent-color: var(--phosphor);
}
.todo-title {
  color: var(--paper);
  word-break: break-word;
}
.todo-title--done {
  color: var(--queue);
  text-decoration: line-through;
}
.todo-job {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 12px;
}
.todo-job:hover {
  border-color: var(--phosphor);
}
/* 最近一次 job 还在跑（非终态）：链接带脉冲，行内一眼看到「派出去了、还在跑」。 */
.todo-job--live {
  color: var(--run);
  border-color: var(--run);
  animation: todo-job-live 1.6s ease-in-out infinite;
}
@keyframes todo-job-live {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.55;
  }
}
@media (prefers-reduced-motion: reduce) {
  .todo-job--live {
    animation: none;
  }
}
.op-form {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 10px;
}
.op-input {
  min-width: 0;
  flex: 1;
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 6px 8px;
  font-size: 12px;
  outline: none;
}
.op-input:focus {
  border-color: var(--phosphor);
}
.empty {
  padding: 18px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 12px;
}
.loading {
  color: var(--queue);
  font-size: 13px;
  padding: 18px 0;
}

.cbar {
  display: flex;
  width: 100%;
  height: 10px;
  overflow: hidden;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--term-bg);
}
.seg {
  height: 100%;
  min-width: 2px;
}
.seg--done {
  background: var(--done);
}
.seg--run {
  background: var(--run);
}
.seg--fail {
  background: var(--fail);
}
.seg--queue {
  background: var(--queue);
}

@media (max-width: 780px) {
  .detail-head,
  .head-status,
  .op-form {
    align-items: flex-start;
    flex-direction: column;
  }
  .jobs-head {
    display: none;
  }
  .job-row {
    grid-template-columns: 1fr;
    gap: 6px;
  }
  .todo-row {
    grid-template-columns: 22px 1fr auto;
  }
  .todo-status,
  .todo-duration,
  .todo-job-count,
  .todo-job {
    grid-column: 2 / -1;
    justify-self: start;
  }
  .todo-note,
  .todo-dispatch-form,
  .todo-dispatch-error,
  .todo-edit {
    padding-left: 14px;
  }
  .todo-dispatch-form,
  .edit-actions {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
