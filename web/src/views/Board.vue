<script setup lang="ts">
// Jobs Board：轮询 listJobs（2.5s），Page Visibility 暂停/恢复，status 过滤，行点击进详情。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import StatusBadge from '../components/StatusBadge.vue'
import Signal from '../components/Signal.vue'
import { listJobs } from '../api/client'
import { fmtDuration, jobDurationSec } from '../api/time'
import type { Job, JobStatus } from '../api/types'

const route = useRoute()
const router = useRouter()

const POLL_MS = 2500

const jobs = ref<Job[]>([])
const loading = ref(false)
const error = ref('')
const statusFilter = ref<'' | JobStatus>('')

const statusOptions: Array<{ value: '' | JobStatus; label: string }> = [
  { value: '', label: '全部' },
  { value: 'queued', label: 'queued' },
  { value: 'running', label: 'running' },
  { value: 'pending_interaction', label: '⚠ 待应答' },
  { value: 'done', label: 'done' },
  { value: 'failed', label: 'failed' },
  { value: 'cancelled', label: 'cancelled' },
  { value: 'timeout', label: 'timeout' },
]

const projectFilter = computed(() => {
  const p = route.query.project
  return typeof p === 'string' && p ? p : undefined
})

let timer: number | null = null

async function fetchJobs(): Promise<void> {
  loading.value = true
  try {
    const resp = await listJobs({
      status: statusFilter.value || undefined,
      project: projectFilter.value,
    })
    jobs.value = resp.jobs ?? []
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
  // 仅页面可见时轮询
  if (document.hidden) {
    return
  }
  timer = window.setInterval(() => {
    void fetchJobs()
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
    void fetchJobs()
    startPolling()
  }
}

// 过滤条件变化 -> 立即刷新
watch([statusFilter, projectFilter], () => {
  void fetchJobs()
})

function shortId(id: string): string {
  return id.length > 8 ? id.slice(-8) : id
}

function rowDuration(job: Job): string {
  return fmtDuration(jobDurationSec(job))
}

function openJob(job: Job): void {
  void router.push(`/jobs/${encodeURIComponent(job.id)}`)
}

onMounted(() => {
  void fetchJobs()
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
      <h1 class="title mono">JOBS BOARD</h1>
      <div class="controls mono">
        <span v-if="projectFilter" class="proj-chip">project: {{ projectFilter }}</span>
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
        <span class="col-job">job · title / id</span>
        <span class="col-proj">project</span>
        <span class="col-agent">agent</span>
        <span class="col-runner">runner</span>
        <span class="col-signal">信号 / 耗时</span>
      </div>

      <div
        v-for="job in jobs"
        :key="job.id"
        class="trow"
        role="button"
        tabindex="0"
        @click="openJob(job)"
        @keydown.enter="openJob(job)"
      >
        <span class="col-status"><StatusBadge :status="job.status" /></span>
        <span class="col-job" :class="{ 'col-job--titled': job.title }">
          <span v-if="job.title" class="job-title" :title="job.title">{{ job.title }}</span>
          <span class="job-id mono" :title="job.id">{{ shortId(job.id) }}</span>
        </span>
        <span class="col-proj mono">{{ job.project_key }}</span>
        <span class="col-agent mono">{{ job.agent }}</span>
        <span class="col-runner mono" :class="{ remote: job.runner !== 'local' }">
          <span class="runner-name" :title="job.runner">{{ job.runner }}</span>
          <span v-if="job.worker_id" class="runner-worker" :title="`worker_id: ${job.worker_id}`">{{ job.worker_id }}</span>
        </span>
        <span class="col-signal">
          <Signal :status="job.status" :duration-sec="jobDurationSec(job)" />
          <span v-if="job.status === 'running'" class="run-dur mono">{{ rowDuration(job) }}</span>
        </span>
      </div>

      <div v-if="jobs.length === 0 && !error" class="empty mono">
        暂无 job
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
.proj-chip {
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 8px;
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
  grid-template-columns: 124px minmax(160px, 1fr) 140px 120px 110px 180px;
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
/* job cell: title (primary) stacked over short-id (secondary). When a job has no
   title only the short-id shows, so the row reads the same as before. */
.col-job {
  display: flex;
  flex-direction: column;
  gap: 1px;
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
/* Without a title the id is the primary (full-size) label, as before. */
.col-job:not(.col-job--titled) .job-id {
  font-size: 13px;
}
.col-proj {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-agent {
  color: var(--queue);
}
/* runner cell: runner name (primary) stacked over worker_id (secondary) when a
   runner=worker job ran on a specific worker, so "where it ran" is visible. */
.col-runner {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  color: var(--queue);
}
.runner-name,
.runner-worker {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.runner-worker {
  font-size: 10px;
  color: var(--queue);
}
/* Remote runners (peer-http / worker) stand out so "where it ran" is visible. */
.col-runner.remote .runner-name {
  color: var(--phosphor);
}
.col-signal {
  display: inline-flex;
  align-items: center;
  gap: 10px;
}
.run-dur {
  font-size: 11px;
  color: var(--run);
}
.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 13px;
}
</style>
