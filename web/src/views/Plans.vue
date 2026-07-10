<script setup lang="ts">
// Plans 列表：轮询 listPlans（2.5s），Page Visibility 暂停/恢复，status 过滤，
// 行点击进详情；顶部内联「新建计划」表单（title + 可选 description）。仿 Workflows.vue。
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { createPlan, listPlans } from '../api/client'
import { fmtDuration } from '../api/time'
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

function countsDetail(c?: PlanCounts): string {
  if (!c || c.total <= 0) {
    return ''
  }
  return `done ${c.done} · running ${c.running} · failed ${c.failed} · queued ${c.queued}`
}

// plan 状态 → 颜色 class（不复用 StatusBadge：其 statusColor 仅认 JobStatus）。
function statusClass(s: PlanStatus): string {
  return `pill pill--${s}` // open/active/done/archived 见 <style>
}

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
        <span class="col-status"><span :class="statusClass(p.status)">{{ p.status }}</span></span>
        <span class="col-plan" :class="{ 'col-plan--titled': p.title }">
          <span v-if="p.title" class="plan-title" :title="p.title">{{ p.title }}</span>
          <span class="plan-id mono" :title="p.plan_id">{{ shortId(p.plan_id) }}</span>
        </span>
        <span class="col-counts mono">
          <span class="count-line">
            <span class="cbar" aria-hidden="true">
              <span
                v-for="s in segments(p.counts)"
                :key="s.cls"
                class="seg"
                :class="s.cls"
                :style="{ width: `${s.pct}%` }"
              ></span>
            </span>
            <span class="count-frac">{{ countsText(p.counts) }}</span>
          </span>
          <span v-if="countsDetail(p.counts)" class="count-detail">
            {{ countsDetail(p.counts) }}
          </span>
        </span>
        <span class="col-updated mono">{{ rowAge(p) }}</span>
      </div>

      <div v-if="plans.length === 0 && !error" class="empty mono">
        <p>暂无 plan</p>
      </div>
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
.create-input {
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
}
.filter-select:focus,
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
  grid-template-columns: 124px minmax(200px, 1fr) minmax(260px, 360px) 90px;
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

.pill {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 74px;
  border: 1px solid currentColor;
  border-radius: var(--radius);
  padding: 2px 8px;
  font-size: 11px;
  letter-spacing: 0.04em;
}
.pill--open {
  color: var(--queue);
}
.pill--active {
  color: var(--phosphor);
}
.pill--done {
  color: var(--done);
}
.pill--archived {
  color: var(--queue);
  opacity: 0.55;
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
