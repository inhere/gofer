<script setup lang="ts">
// Plan 详情：getPlan 填头部 + counts 进度 + jobs 表 + todos 清单；有未终态 job 时轮询（2.5s）。
//  - jobs 链入 /jobs/{id}（仿 WorkflowDetail 的 step→job）。
//  - todos：勾选(updateTodo) / 新增(addTodo，可绑 job) / 展示绑定 job。
//  - attach：把已有 job id 补挂到本 plan（attachJob）。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import PlanStatusBadge from '../components/PlanStatusBadge.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { addTodo, attachJob, getPlan, updatePlan, updateTodo, updateTodoStatus } from '../api/client'
import { fmtDateTime, fmtDuration, jobDurationSec, toUnixSec } from '../api/time'
import type { Job, PlanCounts, PlanDetail, PlanStatus, Todo, TodoStatus } from '../api/types'

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

// plan「进行中」= 其下有 queued/running 的 job；据此决定是否轮询（仿 WorkflowDetail.isRunning）。
const isActive = computed(() => {
  const c = plan.value?.counts
  return !!c && c.running + c.queued > 0
})

const todoSummary = computed(() => {
  const todos = plan.value?.todos ?? []
  const doing = todos.filter((t) => t.status === 'doing').length
  const base = `${todos.filter((t) => t.done).length}/${todos.length}`
  return doing > 0 ? `${base} · ${doing} doing` : base
})

// 生命周期状态推进（Part C §C2）：下拉即改，doing/done 时间戳由服务端自动打。
const TODO_STATUSES: TodoStatus[] = ['pending', 'doing', 'done', 'skipped']

async function onTodoStatus(t: Todo, status: TodoStatus): Promise<void> {
  if (status === t.status) return
  opError.value = ''
  try {
    const updated = await updateTodoStatus(t.todo_id, status)
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

const canFinish = computed(() => {
  const c = plan.value?.counts
  // 「其下 job 已全部终态」= 有 job 且没有 queued/running。仅作提示，不自动改状态（C2）。
  return !!c && c.total > 0 && c.queued === 0 && c.running === 0
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

function countsDetail(c?: PlanCounts): string {
  if (!c || c.total <= 0) {
    return ''
  }
  return `done ${c.done} · running ${c.running} · failed ${c.failed} · queued ${c.queued}`
}

function shortId(id: string): string {
  return id.length > 14 ? id.slice(-14) : id
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
  document.addEventListener('visibilitychange', onVisibility)
})
onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="detail">
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

    <section v-if="plan" class="section">
      <h2 class="section-title mono">COUNTS</h2>
      <div class="counts-card mono">
        <div class="count-line">
          <span class="cbar" aria-hidden="true">
            <span
              v-for="s in segments(plan.counts)"
              :key="s.cls"
              class="seg"
              :class="s.cls"
              :style="{ width: `${s.pct}%` }"
            ></span>
          </span>
          <span class="count-frac">{{ countsText(plan.counts) }}</span>
        </div>
        <p v-if="canFinish && plan.status !== 'done'" class="finish-hint">
          其下 job 已全部终态，可标记完成
        </p>
        <div class="legend">
          <span><i class="dot dot--done"></i>done</span>
          <span><i class="dot dot--run"></i>running</span>
          <span><i class="dot dot--fail"></i>failed</span>
          <span><i class="dot dot--queue"></i>queued</span>
        </div>
        <p v-if="countsDetail(plan.counts)" class="count-detail">{{ countsDetail(plan.counts) }}</p>
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
          <span class="job-status"><StatusBadge :status="j.status" /></span>
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

    <section v-if="plan" class="section">
      <div class="section-head">
        <h2 class="section-title mono">TODOS ({{ todoSummary }})</h2>
      </div>
      <div class="todos">
        <div v-for="t in plan.todos" :key="t.todo_id" class="todo-item">
          <label class="todo-row">
            <input type="checkbox" :checked="t.done" @change="onToggleTodo(t)" />
            <span class="todo-title" :class="{ 'todo-title--done': t.done }">{{ t.title }}</span>
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
            <button
              v-if="t.job_id"
              class="todo-job mono"
              type="button"
              :title="t.job_id"
              @click.prevent="openJob(t.job_id)"
            >
              job {{ shortId(t.job_id) }} &rarr;
            </button>
          </label>
          <p v-if="t.note" class="todo-note mono">{{ t.note }}</p>
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
  </div>
</template>

<style scoped>
.detail {
  max-width: 980px;
  margin: 0 auto;
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
  grid-template-columns: 22px minmax(160px, 1fr) auto auto auto;
  align-items: center;
  gap: 10px;
  padding: 9px 14px;
  font-size: 13px;
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
  .todo-job {
    grid-column: 2 / -1;
    justify-self: start;
  }
  .todo-note {
    padding-left: 14px;
  }
}
</style>
