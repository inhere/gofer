<script setup lang="ts">
// Plans 列表：轮询 listPlans（2.5s，只刷当前页），Page Visibility 暂停/恢复，status/project/q
// 过滤 + limit/offset 分页（F-d），行点击进详情；顶部内联「新建计划」表单。
// 过滤与翻页全部落在 URL query（status/project/q/offset），刷新/分享都保持同一视图。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import PlanStatusBadge from '../components/PlanStatusBadge.vue'
import { createPlan, listPlans, listProjects } from '../api/client'
import { fmtDuration } from '../api/time'
import type { Plan, PlanStatus } from '../api/types'
import { progressDetail, progressSegments, progressText } from '../utils/planProgress'

const route = useRoute()
const router = useRouter()
const POLL_MS = 2500
// 与后端默认页大小一致（GET /v1/plans 缺省 20、上限 100）。
const PAGE_SIZE = 20

const plans = ref<Plan[]>([])
// total 是同条件下的总条数（服务端给的，不是本页条数）：翻页按钮据此判断有没有下一页。
const total = ref(0)
const loading = ref(false)
const error = ref('')

// URL query 是过滤条件的唯一来源（get 读 URL、set 写 URL），所以浏览器前进/后退、分享链接
// 都能复原同一页；q 是输入框的即时值，回车/失焦才写回 URL。
const statusFilter = computed({
  get: () => {
    const s = route.query.status
    return typeof s === 'string' ? (s as '' | PlanStatus) : ''
  },
  set: (value: '' | PlanStatus) => setFilter('status', value),
})
const projectFilter = computed(() => {
  const p = route.query.project
  return typeof p === 'string' ? p : ''
})
const qFilter = computed(() => {
  const q = route.query.q
  return typeof q === 'string' ? q : ''
})
const offset = computed(() => {
  const o = Number(route.query.offset)
  return Number.isFinite(o) && o > 0 ? Math.floor(o) : 0
})

const qInput = ref(qFilter.value)
watch(qFilter, (v) => {
  qInput.value = v
})

// pushQuery 合并写回 URL：空值删除该键（不留下 ?status= 这种空参数）。
function pushQuery(patch: Record<string, string | undefined>): void {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries({ ...route.query, ...patch })) {
    if (typeof v === 'string' && v) {
      next[k] = v
    }
  }
  void router.push({ path: '/plans', query: next })
}

// 过滤条件变化一律回到第 1 页（否则换了过滤还在 offset=40 会看到空页）。
function setFilter(key: 'status' | 'project', value: string): void {
  pushQuery({ [key]: value || undefined, offset: undefined })
}

function applyQueryInput(): void {
  const v = qInput.value.trim()
  if (v !== qFilter.value) {
    pushQuery({ q: v || undefined, offset: undefined })
  }
}

const projectKeys = ref<string[]>([])
const projectOptions = computed(() => {
  const keys = [...projectKeys.value]
  if (projectFilter.value && !keys.includes(projectFilter.value)) {
    keys.unshift(projectFilter.value)
  }
  return keys
})

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

const pageFrom = computed(() => (plans.value.length === 0 ? 0 : offset.value + 1))
const pageTo = computed(() => offset.value + plans.value.length)
const hasPrev = computed(() => offset.value > 0)
const hasNext = computed(() => pageTo.value < total.value)
const hasFilters = computed(() => Boolean(statusFilter.value || projectFilter.value || qFilter.value))

function prevPage(): void {
  const prev = offset.value - PAGE_SIZE
  pushQuery({ offset: prev > 0 ? String(prev) : undefined })
}

function nextPage(): void {
  if (hasNext.value) {
    pushQuery({ offset: String(offset.value + PAGE_SIZE) })
  }
}

let timer: number | null = null

async function fetchPlans(): Promise<void> {
  loading.value = true
  try {
    const resp = await listPlans({
      status: statusFilter.value || undefined,
      project: projectFilter.value || undefined,
      q: qFilter.value || undefined,
      limit: PAGE_SIZE,
      offset: offset.value,
    })
    plans.value = resp.plans ?? []
    total.value = resp.total ?? plans.value.length
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// 项目下拉的数据源（项目数量少，取一次即可；失败静默，不阻塞 plan 列表）。
async function fetchProjects(): Promise<void> {
  try {
    const resp = await listProjects()
    projectKeys.value = resp.projects ?? []
  } catch {
    projectKeys.value = []
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

// 进度条 / 进度文字 / 副信息见 utils/planProgress：completion（待办优先、无待办回落 job）；
// 旧服务端无 completion 时回落原 job counts 显示。

function shortId(id: string): string {
  return id.length > 14 ? id.slice(-14) : id
}

function rowAge(p: Plan): string {
  const base = p.updated_at || p.created_at
  if (!base) {
    return '—'
  }
  return fmtDuration(Math.max(0, Math.floor(Date.now() / 1000) - base))
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

// 过滤/翻页变化 -> 立即刷新（轮询只重复当前页，不重置 offset）。
watch([statusFilter, projectFilter, qFilter, offset], () => void fetchPlans())

onMounted(() => {
  void fetchPlans()
  void fetchProjects()
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})
onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="board">
    <div class="board-head">
      <h1 class="title mono">PLANS</h1>
      <div class="controls mono">
        <label class="filter">
          <span class="filter-label">status</span>
          <select v-model="statusFilter" class="filter-select mono">
            <option v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </option>
          </select>
        </label>
        <label class="filter">
          <span class="filter-label">project</span>
          <select v-model="projectFilter" class="filter-select mono">
            <option value="">全部</option>
            <option v-for="key in projectOptions" :key="key" :value="key">{{ key }}</option>
          </select>
        </label>
        <label class="filter">
          <span class="filter-label">q</span>
          <input
            v-model="qInput"
            class="filter-input mono"
            placeholder="plan id 前缀 / 标题"
            spellcheck="false"
            @keydown.enter.prevent="applyQueryInput"
            @blur="applyQueryInput"
          />
        </label>
        <span class="poll-hint" :class="{ 'poll-hint--on': loading }">●</span>
      </div>
    </div>

    <form class="create-row mono" @submit.prevent="onCreate">
      <label class="create-field">
        <span>title</span>
        <input v-model="newTitle" class="create-input mono" placeholder="计划标题" />
      </label>
      <label class="create-field create-field--desc">
        <span>description</span>
        <input v-model="newDesc" class="create-input mono" placeholder="描述(可选)" />
      </label>
      <button class="create-btn mono" type="submit" :disabled="!newTitle.trim() || creating">
        {{ creating ? '创建中…' : '新建计划' }}
      </button>
    </form>

    <p v-if="createError" class="error mono">{{ createError }}</p>
    <p v-if="error" class="error mono">{{ error }}</p>

    <div class="table">
      <div class="thead mono">
        <span class="col-status">状态</span>
        <span class="col-plan">plan · title / id</span>
        <span class="col-project">project</span>
        <span class="col-counts">进度</span>
        <span class="col-updated">更新</span>
      </div>

      <div
        v-for="p in plans"
        :key="p.plan_id"
        class="trow"
        role="button"
        tabindex="0"
        @click="openPlan(p)"
        @keydown.enter="openPlan(p)"
      >
        <span class="col-status"><PlanStatusBadge :status="p.status" /></span>
        <span class="col-plan" :class="{ 'col-plan--titled': p.title }">
          <span v-if="p.title" class="plan-title" :title="p.title">{{ p.title }}</span>
          <span class="plan-id mono" :title="p.plan_id">{{ shortId(p.plan_id) }}</span>
        </span>
        <!-- PLAN-02 P2：该 plan 的待办派发进哪个 project（空 = 未指定，派发时要求待办自带）。 -->
        <span class="col-project mono" :title="p.project || '未指定 project'">
          {{ p.project || '—' }}
        </span>
        <span class="col-counts mono">
          <span class="count-line">
            <span class="cbar" aria-hidden="true">
              <span
                v-for="s in progressSegments(p)"
                :key="s.cls"
                class="seg"
                :class="s.cls"
                :style="{ width: `${s.pct}%` }"
              ></span>
            </span>
            <span class="count-frac">{{ progressText(p) }}</span>
          </span>
          <span v-if="progressDetail(p)" class="count-detail">
            {{ progressDetail(p) }}
          </span>
        </span>
        <span class="col-updated mono">{{ rowAge(p) }}</span>
      </div>

      <div v-if="plans.length === 0 && !error" class="empty mono">
        <p>{{ hasFilters ? '没有匹配的 plan' : '暂无 plan' }}</p>
      </div>
    </div>

    <!-- F-d 分页：当前页/总条数都由服务端给，上一页/下一页写回 URL（offset）。 -->
    <div v-if="total > 0" class="pager mono">
      <button class="page-btn" type="button" :disabled="!hasPrev || loading" @click="prevPage">
        ← 上一页
      </button>
      <span class="page-info">第 {{ pageFrom }}–{{ pageTo }} 条 / 共 {{ total }} 条</span>
      <button class="page-btn" type="button" :disabled="!hasNext || loading" @click="nextPage">
        下一页 →
      </button>
    </div>
  </div>
</template>

<style scoped>
.board {
  max-width: 1100px;
  margin: 0 auto;
}
.board-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.controls {
  display: flex;
  align-items: center;
  gap: 14px;
  font-size: 12px;
}
.filter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--queue);
}
.filter-label {
  font-size: 11px;
  letter-spacing: 0.06em;
}
.filter-select,
.filter-input,
.create-input {
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
}
.filter-input {
  min-width: 180px;
}
.filter-select:focus,
.filter-input:focus,
.create-input:focus {
  border-color: var(--phosphor);
}
.poll-hint {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-hint--on {
  color: var(--phosphor);
}

.create-row {
  display: flex;
  align-items: flex-end;
  gap: 10px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px;
  margin-bottom: 12px;
  background: color-mix(in srgb, var(--panel) 72%, transparent);
}
.create-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 180px;
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.04em;
}
.create-field--desc {
  flex: 1;
  min-width: 220px;
}
.create-input {
  width: 100%;
  min-height: 28px;
}
.create-btn {
  background: var(--phosphor);
  color: var(--ink);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 5px 10px;
  font-size: 12px;
  font-weight: 600;
  min-height: 28px;
}
.create-btn:hover:not(:disabled) {
  opacity: 0.9;
}
.create-btn:disabled {
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

.table {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.thead,
.trow {
  display: grid;
  grid-template-columns: 124px minmax(200px, 1fr) 120px minmax(260px, 360px) 90px;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
}
.thead {
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--queue);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  cursor: pointer;
  font-size: 13px;
  outline: none;
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--panel);
}
.trow:focus-visible {
  background: var(--panel);
  box-shadow: inset 2px 0 0 var(--phosphor);
}
.col-plan {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.plan-title {
  color: var(--paper);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.plan-id {
  color: var(--phosphor);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-plan:not(.col-plan--titled) .plan-id {
  font-size: 13px;
}
/* project 列（PLAN-02 P2）：plan 的待办派发进哪个 project；空渲染 —。 */
.col-project {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-counts {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.count-line {
  display: grid;
  grid-template-columns: minmax(120px, 1fr) 48px;
  align-items: center;
  gap: 10px;
}
.count-frac {
  color: var(--paper);
  font-size: 12px;
  text-align: right;
}
.count-detail {
  color: var(--queue);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-updated {
  color: var(--queue);
}
.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 13px;
}
.empty p {
  margin: 0;
}

/* F-d 分页条：与表格同宽，居中放"第 a–b 条 / 共 N 条"，两侧是翻页按钮。 */
.pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 14px;
  margin-top: 10px;
  font-size: 12px;
  color: var(--queue);
}
.page-btn {
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.page-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.page-btn:disabled {
  opacity: 0.45;
  cursor: default;
}
.page-info {
  letter-spacing: 0.04em;
}

.cbar {
  display: flex;
  width: 100%;
  height: 8px;
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

@media (max-width: 760px) {
  .board-head {
    align-items: flex-start;
    flex-direction: column;
    gap: 10px;
  }
  .controls,
  .create-row {
    flex-wrap: wrap;
  }
  .create-field,
  .create-field--desc {
    flex: 1 1 220px;
  }
  .thead {
    display: none;
  }
  .trow {
    grid-template-columns: 1fr;
    gap: 8px;
  }
}
</style>
