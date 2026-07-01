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
const countJobs = ref<Job[]>([])
const countJobsLoaded = ref(false)
const loading = ref(false)
const error = ref('')
const lastRefreshedAt = ref<number | null>(null)
const statusFilter = ref<'' | JobStatus>('')
// E5 检索维度：tag/agent/runner 自由输入，caller 自由输入，since 走相对快捷预设。
const tagFilter = ref('')
const agentFilter = ref('')
const runnerFilter = ref('')
const callerFilter = ref('')
const sinceFilter = ref<'' | '1h' | '24h' | '7d'>('')

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

// since 相对预设 -> 秒偏移；请求时换算成绝对 unix 秒（started_at >= now-offset）。
const SINCE_OFFSET_SEC: Record<'1h' | '24h' | '7d', number> = {
  '1h': 3600,
  '24h': 86400,
  '7d': 604800,
}
const sinceOptions: Array<{ value: '' | '1h' | '24h' | '7d'; label: string }> = [
  { value: '', label: '全部时间' },
  { value: '1h', label: '近 1h' },
  { value: '24h', label: '近 24h' },
  { value: '7d', label: '近 7d' },
]

const activeFilterCount = computed(() =>
  [
    statusFilter.value,
    projectFilter.value,
    tagFilter.value.trim(),
    agentFilter.value.trim(),
    runnerFilter.value.trim(),
    callerFilter.value.trim(),
    sinceFilter.value,
  ].filter(Boolean).length,
)

const statusCounts = computed<Record<JobStatus, number>>(() => {
  const base: Record<JobStatus, number> = {
    queued: 0,
    running: 0,
    pending_interaction: 0,
    done: 0,
    failed: 0,
    cancelled: 0,
    timeout: 0,
  }
  for (const job of countJobsLoaded.value ? countJobs.value : jobs.value) {
    base[job.status] += 1
  }
  return base
})

const runningCount = computed(
  () =>
    jobs.value.filter(
      (job) => job.status === 'running' || job.status === 'pending_interaction',
    ).length,
)
const problemCount = computed(
  () => jobs.value.filter((job) => job.status === 'failed' || job.status === 'timeout').length,
)

const lastRefreshedText = computed(() => {
  if (lastRefreshedAt.value == null) {
    return '尚未刷新'
  }
  return new Date(lastRefreshedAt.value).toLocaleTimeString()
})

// 把相对 since 预设换算为绝对 unix 秒；空表示不过滤。
function sinceParam(): number | undefined {
  if (!sinceFilter.value) {
    return undefined
  }
  const off = SINCE_OFFSET_SEC[sinceFilter.value]
  return Math.floor(Date.now() / 1000) - off
}

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
      tag: tagFilter.value.trim() || undefined,
      agent: agentFilter.value.trim() || undefined,
      runner: runnerFilter.value.trim() || undefined,
      caller: callerFilter.value.trim() || undefined,
      since: sinceParam(),
    })
    jobs.value = resp.jobs ?? []
    error.value = ''
    lastRefreshedAt.value = Date.now()
  } catch (e) {
    // 401 已由 client 处理（跳转登录）；其余显示错误条
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function fetchStatusCounts(): Promise<void> {
  try {
    const resp = await listJobs({
      project: projectFilter.value,
      tag: tagFilter.value.trim() || undefined,
      agent: agentFilter.value.trim() || undefined,
      runner: runnerFilter.value.trim() || undefined,
      caller: callerFilter.value.trim() || undefined,
      since: sinceParam(),
    })
    countJobs.value = resp.jobs ?? []
    countJobsLoaded.value = true
  } catch {
    countJobsLoaded.value = false
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

// 过滤条件变化 -> 立即刷新（含 E5 的 tag/agent/runner/caller/since）
watch(
  [
    statusFilter,
    projectFilter,
    tagFilter,
    agentFilter,
    runnerFilter,
    callerFilter,
    sinceFilter,
  ],
  () => {
    void fetchJobs()
  },
)

watch([projectFilter, tagFilter, agentFilter, runnerFilter, callerFilter, sinceFilter], () => {
  void fetchStatusCounts()
})

function shortId(id: string): string {
  return id.length > 8 ? id.slice(-8) : id
}

function rowDuration(job: Job): string {
  return fmtDuration(jobDurationSec(job))
}

function rowTime(job: Job): string {
  const ts = job.started_at || job.ended_at
  if (!ts) {
    return '—'
  }
  const d = new Date(ts * 1000)
  return [
    String(d.getHours()).padStart(2, '0'),
    String(d.getMinutes()).padStart(2, '0'),
    String(d.getSeconds()).padStart(2, '0'),
  ].join(':')
}

function openJob(job: Job): void {
  void router.push(`/jobs/${encodeURIComponent(job.id)}`)
}

function refreshNow(): void {
  void fetchJobs()
}

function clearFilters(): void {
  statusFilter.value = ''
  tagFilter.value = ''
  agentFilter.value = ''
  runnerFilter.value = ''
  callerFilter.value = ''
  sinceFilter.value = ''
  if (projectFilter.value) {
    void router.push({ path: '/board' })
  }
}

onMounted(() => {
  void fetchJobs()
  void fetchStatusCounts()
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
      <div>
        <h1 class="title mono">JOBS BOARD</h1>
        <p class="subtitle mono">
          {{ jobs.length }} jobs · {{ runningCount }} active · {{ problemCount }} problem
          <span v-if="activeFilterCount"> · {{ activeFilterCount }} filters</span>
        </p>
      </div>
      <div class="head-actions mono">
        <span class="last-refresh">刷新 {{ lastRefreshedText }}</span>
        <span class="poll-hint" :class="{ 'poll-hint--on': loading }">●</span>
        <button class="head-btn" type="button" :disabled="loading" @click="refreshNow">
          {{ loading ? '刷新中' : '刷新' }}
        </button>
        <button
          class="head-btn"
          type="button"
          :disabled="activeFilterCount === 0"
          @click="clearFilters"
        >
          清空过滤
        </button>
      </div>
    </div>

    <div class="filter-panel mono">
      <div class="status-tabs" aria-label="状态过滤">
        <button
          v-for="opt in statusOptions"
          :key="opt.value"
          type="button"
          class="status-tab"
          :class="{ 'status-tab--active': statusFilter === opt.value }"
          @click="statusFilter = opt.value"
        >
          <span>{{ opt.label }}</span>
          <span v-if="opt.value" class="status-count">{{ statusCounts[opt.value] }}</span>
        </button>
      </div>

      <div class="controls">
        <span v-if="projectFilter" class="proj-chip">project: {{ projectFilter }}</span>
        <label class="filter">
          <span class="filter-label">tag</span>
          <input
            v-model="tagFilter"
            class="filter-input mono"
            placeholder="标签"
            spellcheck="false"
          />
        </label>
        <label class="filter">
          <span class="filter-label">agent</span>
          <input
            v-model="agentFilter"
            class="filter-input mono"
            placeholder="agent"
            spellcheck="false"
          />
        </label>
        <label class="filter">
          <span class="filter-label">runner</span>
          <input
            v-model="runnerFilter"
            class="filter-input mono"
            placeholder="runner"
            spellcheck="false"
          />
        </label>
        <label class="filter">
          <span class="filter-label">caller</span>
          <input
            v-model="callerFilter"
            class="filter-input mono"
            placeholder="caller"
            spellcheck="false"
          />
        </label>
        <label class="filter">
          <span class="filter-label">since</span>
          <select v-model="sinceFilter" class="filter-select mono">
            <option v-for="opt in sinceOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </option>
          </select>
        </label>
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
        <span class="col-time">时间</span>
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
          <span v-if="job.tags && job.tags.length" class="job-tags">
            <span v-for="t in job.tags" :key="t" class="tag-chip mono" :title="t">{{ t }}</span>
          </span>
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
        <span class="col-time mono">{{ rowTime(job) }}</span>
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
  gap: 16px;
  margin-bottom: 12px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.subtitle {
  color: var(--queue);
  font-size: 12px;
  margin: 6px 0 0;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  flex: none;
}
.last-refresh {
  color: var(--queue);
}
.head-btn {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.head-btn:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.head-btn:disabled {
  color: var(--queue);
  cursor: default;
  opacity: 0.65;
}
.filter-panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 10px;
  margin-bottom: 14px;
}
.status-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.status-tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 9px;
  font-size: 12px;
  min-height: 28px;
}
.status-tab:hover {
  color: var(--paper);
  border-color: var(--queue);
}
.status-tab--active {
  color: var(--phosphor);
  border-color: var(--phosphor);
  background: var(--ink);
}
.status-count {
  color: var(--paper);
  font-size: 11px;
  min-width: 16px;
  text-align: center;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 4px;
  line-height: 16px;
}
.controls {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
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
.filter-input {
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
  width: 92px;
}
.filter-input:focus {
  border-color: var(--phosphor);
}
.filter-input::placeholder {
  color: var(--queue);
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
  grid-template-columns: 124px minmax(160px, 1fr) 140px 120px 110px 160px 78px;
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
/* tag 徽标：行内小标签，无 tags 时整块不渲染（v-if），不挤占无标签行的布局。 */
.job-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 2px;
}
.tag-chip {
  font-size: 10px;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 5px;
  line-height: 15px;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
.col-time {
  color: var(--queue);
  text-align: right;
  white-space: nowrap;
}
.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 13px;
}

@media (max-width: 768px) {
  .board-head {
    align-items: flex-start;
    flex-direction: column;
  }
  .head-actions {
    width: 100%;
    flex-wrap: wrap;
  }
  .last-refresh {
    flex: 1;
  }
  .status-tab {
    flex: 1 1 calc(50% - 6px);
    justify-content: space-between;
  }
  .filter {
    flex: 1 1 130px;
    align-items: flex-start;
    flex-direction: column;
    gap: 4px;
  }
  .filter-input,
  .filter-select {
    width: 100%;
    box-sizing: border-box;
  }
  .table {
    overflow-x: visible;
  }
  .thead {
    display: none;
  }
  .trow {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 6px 10px;
    padding: 12px;
  }
  .col-status {
    grid-column: 2;
    grid-row: 1;
    justify-self: end;
  }
  .col-job {
    grid-column: 1;
    grid-row: 1;
  }
  .col-proj,
  .col-agent,
  .col-runner,
  .col-signal,
  .col-time {
    grid-column: 1 / -1;
  }
  .col-proj::before {
    content: 'project ';
    color: var(--queue);
  }
  .col-agent::before {
    content: 'agent ';
    color: var(--queue);
  }
  .col-runner::before {
    content: 'runner ';
    color: var(--queue);
  }
  .col-time {
    text-align: left;
  }
  .col-time::before {
    content: 'time ';
    color: var(--queue);
  }
}
</style>
