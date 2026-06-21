<script setup lang="ts">
// Workflows 列表（job 链）：轮询 listWorkflows（2.5s），Page Visibility 暂停/恢复，
// status 过滤，行点击进详情。仿 Board.vue 结构。
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import StatusBadge from '../components/StatusBadge.vue'
import { listWorkflows } from '../api/client'
import { fmtDuration } from '../api/time'
import type { Workflow, WorkflowStatus } from '../api/types'

const router = useRouter()

const POLL_MS = 2500

const workflows = ref<Workflow[]>([])
const loading = ref(false)
const error = ref('')
const statusFilter = ref<'' | WorkflowStatus>('')

const statusOptions: Array<{ value: '' | WorkflowStatus; label: string }> = [
  { value: '', label: '全部' },
  { value: 'running', label: 'running' },
  { value: 'done', label: 'done' },
  { value: 'failed', label: 'failed' },
  { value: 'cancelled', label: 'cancelled' },
]

let timer: number | null = null

async function fetchWorkflows(): Promise<void> {
  loading.value = true
  try {
    const resp = await listWorkflows(statusFilter.value || undefined)
    workflows.value = resp.workflows ?? []
    error.value = ''
  } catch (e) {
    // 401 已由 client 处理（跳转登录）；其余显示错误条
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function startPolling(): void {
  stopPolling()
  if (document.hidden) {
    return
  }
  timer = window.setInterval(() => {
    void fetchWorkflows()
  }, POLL_MS)
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
    void fetchWorkflows()
    startPolling()
  }
}

watch(statusFilter, () => {
  void fetchWorkflows()
})

function shortId(id: string): string {
  return id.length > 10 ? id.slice(-10) : id
}

// 工作流耗时：created_at -> updated_at（运行中按 now）。
function rowDuration(wf: Workflow): string {
  const end = wf.status === 'running' ? Math.floor(Date.now() / 1000) : wf.updated_at
  return fmtDuration(Math.max(0, end - wf.created_at))
}

function openWorkflow(wf: Workflow): void {
  void router.push(`/workflows/${encodeURIComponent(wf.id)}`)
}

onMounted(() => {
  void fetchWorkflows()
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
      <h1 class="title mono">WORKFLOWS</h1>
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

    <p v-if="error" class="error mono">{{ error }}</p>

    <div class="table">
      <div class="thead mono">
        <span class="col-status">状态</span>
        <span class="col-wf">workflow · title / id</span>
        <span class="col-step">step</span>
        <span class="col-dur">耗时</span>
      </div>

      <div
        v-for="wf in workflows"
        :key="wf.id"
        class="trow"
        role="button"
        tabindex="0"
        @click="openWorkflow(wf)"
        @keydown.enter="openWorkflow(wf)"
      >
        <span class="col-status"><StatusBadge :status="wf.status" /></span>
        <span class="col-wf" :class="{ 'col-wf--titled': wf.title }">
          <span v-if="wf.title" class="wf-title" :title="wf.title">{{ wf.title }}</span>
          <span class="wf-id mono" :title="wf.id">{{ shortId(wf.id) }}</span>
        </span>
        <span class="col-step mono">{{ wf.current_step }}/{{ wf.total_steps }}</span>
        <span class="col-dur mono">{{ rowDuration(wf) }}</span>
      </div>

      <div v-if="workflows.length === 0 && !error" class="empty mono">
        暂无 workflow
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
.filter-select {
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
}
.filter-select:focus {
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
  grid-template-columns: 124px minmax(200px, 1fr) 90px 120px;
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
.col-wf {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.wf-title {
  color: var(--paper);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-id {
  color: var(--phosphor);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-wf:not(.col-wf--titled) .wf-id {
  font-size: 13px;
}
.col-step {
  color: var(--queue);
}
.col-dur {
  color: var(--queue);
}
.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 13px;
}
</style>
