<script setup lang="ts">
// Jobs Board：轮询 listJobs（2.5s），Page Visibility 暂停/恢复，status 过滤，行点击进详情。
// P3：列表可以被任务详情宿主（JobsView）并排嵌入（embedded），此时隐藏页面标题、只留筛选与行。
import { runnerLabel } from '../utils/runnerDisplay'
import { jobStatusOptions } from '../utils/statusText'
import { t, te } from '../i18n'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Signal from '../components/Signal.vue'
import UncommittedBadge from '../components/UncommittedBadge.vue'
import FilterChips from '../ui/FilterChips.vue'
import FilterDrawer from '../ui/FilterDrawer.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import StatusPill from '../ui/StatusPill.vue'
import { chipItems } from '../ui/filterChips'
import { setJobsSplitPreference } from '../store/jobsSplit'
import { useListReturn } from '../ui/useListReturn'
import { listJobs, listPlans, listProjects } from '../api/client'
import { createLiveTopic } from '../utils/useLiveTopic'
import { fmtDuration, jobDurationSec } from '../api/time'
import type { Job, JobStatus } from '../api/types'
import { isCountedStatus, statusCountSince, STATUS_COUNT_LIMIT } from '../utils/jobStatusCounts'

const route = useRoute()
const router = useRouter()

// 并排模式：由详情宿主页（JobsView）传入，列表只占左列。
const props = withDefaults(defineProps<{ embedded?: boolean }>(), { embedded: false })
const embedded = computed(() => props.embedded)

// 筛选写回哪个路径：并排时留在当前详情路由（右侧详情不丢），否则回列表路由。
function listPath(): string {
  return embedded.value ? route.path : '/jobs'
}

const PAGE_SIZE = 50

const jobs = ref<Job[]>([])
const countJobs = ref<Job[]>([])
const countJobsLoaded = ref(false)
const loading = ref(false)
const error = ref('')
const lastRefreshedAt = ref<number | null>(null)
const statusFilter = ref<'' | JobStatus>('')
const offset = ref(0)
// E5 检索维度：tag/agent/runner 自由输入，caller 自由输入，since 走相对快捷预设。
const tagFilter = ref('')
const agentFilter = ref('')
const runnerFilter = ref('')
const callerFilter = ref('')
const sinceFilter = ref<'' | '1h' | '24h' | '7d'>('')
const hasUploadFilter = ref(false)
const showMessengerJobs = ref(false)
const projectKeys = ref<string[]>([])
// F-b：plan 过滤是输入框（输入 plan id，后端按 q 前缀匹配），不再全量 listPlans 后渲染
// 下拉——plan 会越来越多。recentPlans 是"最近 5 个 open plan"的轻量提示，只在输入框
// 首次获焦时取一次，不进轮询。
const recentPlans = ref<Array<{ id: string; title?: string }>>([])
const recentPlansLoaded = ref(false)
const projectLoadError = ref('')

const statusOptions: Array<{ value: '' | JobStatus; label: string }> = jobStatusOptions()

// 状态 chip / pill 的文案：i18n 有 status.<值> 就用它（与 StatusPill 同一口径），
// 否则退回 statusText 的中文旧表（P5 删旧表后只剩 i18n）。
function statusLabel(value: '' | JobStatus, fallback: string): string {
  if (!value) return t('jobs.filter.all')
  const key = `status.${value}`
  return te(key) ? t(key) : fallback
}

// 初值可由 ?status= 带入（如 /review 页头的「待批准 N →」链接）；只认过滤栏里有的状态。
// 在首次 fetch（onMounted）之前定下，不触发额外的 watch 重拉。
{
  const s = route.query.status
  if (typeof s === 'string' && statusOptions.some((o) => o.value === s)) statusFilter.value = s as JobStatus
}

// since 相对预设 -> 秒偏移；请求时换算成绝对 unix 秒（started_at >= now-offset）。
const SINCE_OFFSET_SEC: Record<'1h' | '24h' | '7d', number> = {
  '1h': 3600,
  '24h': 86400,
  '7d': 604800,
}
const sinceOptions: Array<{ value: '' | '1h' | '24h' | '7d'; label: string }> = [
  { value: '', label: t('jobs.since.all') },
  { value: '1h', label: t('jobs.since.1h') },
  { value: '24h', label: t('jobs.since.24h') },
  { value: '7d', label: t('jobs.since.7d') },
]

// 窄屏时筛选面板默认收起（它一个就占满一屏），桌面宽度下从「更多筛选」抽屉打开。
// 状态与项目是 ListHeader 上的 chip，其余维度进 FilterDrawer（设计稿 §7 任务列表）。
const filtersOpen = ref(false)
const activeFilterCount = computed(() =>
  [
    statusFilter.value,
    projectFilter.value,
    planFilter.value,
    wfFilter.value,
    tagFilter.value.trim(),
    agentFilter.value.trim(),
    runnerFilter.value.trim(),
    callerFilter.value.trim(),
    sinceFilter.value,
    hasUploadFilter.value,
  ].filter(Boolean).length,
)

// 「更多筛选」抽屉里的维度数（状态与项目已经在列表头，不重复计）
const drawerFilterCount = computed(
  () =>
    [
      planFilter.value,
      wfFilter.value,
      tagFilter.value.trim(),
      agentFilter.value.trim(),
      runnerFilter.value.trim(),
      callerFilter.value.trim(),
      sinceFilter.value,
      hasUploadFilter.value,
      showMessengerJobs.value,
    ].filter(Boolean).length,
)

// 状态 chip（带计数）：值与文案仍取自 statusText / jobStatusOptions，计数只给「进行中」类
// 状态（isCountedStatus），与表头旧口径一致。
const statusChipItems = computed(() => {
  const counts: Record<string, number> = {}
  for (const opt of statusOptions) {
    if (isCountedStatus(opt.value)) counts[opt.value] = statusCounts.value[opt.value]
  }
  return chipItems(
    statusOptions.map((opt) => ({ value: opt.value, label: statusLabel(opt.value, opt.label) })),
    counts,
  )
})

const statusCounts = computed<Record<JobStatus, number>>(() => {
  const base: Record<JobStatus, number> = {
    queued: 0,
    running: 0,
    awaiting_input: 0,
    recovering: 0,
    pending_interaction: 0,
    // GATE-01 S3：needs_review 是"等人"的非终态（和待应答一样要出现在表头统计里）。
    needs_review: 0,
    done: 0,
    failed: 0,
    cancelled: 0,
    timeout: 0,
    rejected: 0,
    // JOB-11：等目录锁的非终态（表头单独一栏可过滤，计数并入 queued）。
    waiting_dir: 0,
    // gofer-9b1b：待批准是「等人」的非终态（表头单独一栏可过滤）。
    awaiting_approval: 0,
    // gofer-dzv3：更新等空闲时停住的 job（表头单独一栏可过滤；不用人管、不计入 running）。
    paused_for_update: 0,
  }
  for (const job of countJobsLoaded.value ? countJobs.value : jobs.value) {
    base[job.status] += 1
  }
  // JOB-11：waiting_dir 是 queued 的一种（不占执行位、等锁而已），计数并入 queued 组——
  // 与 /v1/stats 的 by_status 同一口径，否则"等锁"在表头看着像什么都没发生。
  base.queued += base.waiting_dir
  return base
})

const runningCount = computed(
  () =>
    jobs.value.filter(
      (job) =>
        job.status === 'running' ||
        // RECOV-01 recovering 仍是「活」job（worker 还在跑/等重连），计入 active 才不会
        // 让一个跑了几十分钟的 job 在表头看起来已经结束。
        job.status === 'recovering' ||
        job.status === 'pending_interaction' ||
        // GATE-01 S3：needs_review 也是「等人」的活信号（agent 已停，但交付物还没定论）。
        job.status === 'needs_review' ||
        // gofer-9b1b：待批准同样在等人（还没开跑，人点了批准才执行）。
        job.status === 'awaiting_approval',
    ).length,
)
const problemCount = computed(
  () =>
    jobs.value.filter((job) => job.status === 'failed' || job.status === 'timeout' || job.status === 'rejected')
      .length,
)
const pageNumber = computed(() => Math.floor(offset.value / PAGE_SIZE) + 1)
const canPrevPage = computed(() => offset.value > 0)
const canNextPage = computed(() => jobs.value.length === PAGE_SIZE)

const lastRefreshedText = computed(() => {
  if (lastRefreshedAt.value == null) {
    return t('jobs.h.never')
  }
  return t('jobs.h.lastRefresh', { time: new Date(lastRefreshedAt.value).toLocaleTimeString() })
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

const planFilter = computed(() => {
  const p = route.query.plan
  return typeof p === 'string' && p ? p : undefined
})

// 工作流过滤（?wf=<wf-id>）：job 详情里的 workflow 链接与工作流详情都可以带过来。
const wfFilter = computed(() => {
  const p = route.query.wf
  return typeof p === 'string' && p ? p : undefined
})

const wfInput = ref(wfFilter.value ?? '')
watch(wfFilter, (v) => {
  wfInput.value = v ?? ''
})

function applyWfInput(): void {
  const value = wfInput.value.trim()
  if (value === (wfFilter.value ?? '')) {
    return
  }
  const nextQuery = { ...route.query }
  if (value) nextQuery.wf = value
  else delete nextQuery.wf
  void router.push({ path: listPath(), query: nextQuery })
}

const projectSelectValue = computed({
  get: () => projectFilter.value ?? '',
  set: (value: string) => {
    const nextQuery = { ...route.query }
    if (value) {
      nextQuery.project = value
    } else {
      delete nextQuery.project
    }
    void router.push({ path: listPath(), query: nextQuery })
  },
})

// plan 输入框：URL 里的 ?plan= 是"已生效"的值，输入框自己只在回车/失焦时把它写回 URL。
const planInput = ref(planFilter.value ?? '')
watch(planFilter, (v) => {
  planInput.value = v ?? ''
})

function applyPlanInput(): void {
  const value = planInput.value.trim()
  if (value === (planFilter.value ?? '')) {
    return
  }
  const nextQuery = { ...route.query }
  if (value) nextQuery.plan = value
  else delete nextQuery.plan
  void router.push({ path: listPath(), query: nextQuery })
}

function pickRecentPlan(id: string): void {
  planInput.value = id
  applyPlanInput()
}

const projectOptions = computed(() => {
  const keys = [...projectKeys.value]
  if (projectFilter.value && !keys.includes(projectFilter.value)) {
    keys.unshift(projectFilter.value)
  }
  return keys
})


async function fetchProjects(): Promise<void> {
  try {
    const resp = await listProjects()
    projectKeys.value = resp.projects ?? []
    projectLoadError.value = ''
  } catch (e) {
    projectLoadError.value = e instanceof Error ? e.message : String(e)
  }
}

// 最近 open plan 提示（F-b）：只在输入框首次获焦时请求一次（limit=5，不进轮询）；
// 失败静默——它只是提示，board 本身不依赖它。
async function loadRecentPlans(): Promise<void> {
  if (recentPlansLoaded.value) {
    return
  }
  recentPlansLoaded.value = true
  try {
    const resp = await listPlans({ status: 'open', limit: 5 })
    recentPlans.value = (resp.plans ?? []).map((p) => ({ id: p.plan_id, title: p.title }))
  } catch {
    recentPlans.value = []
  }
}

async function fetchJobs(): Promise<void> {
  loading.value = true
  try {
    const resp = await listJobs({
      status: statusFilter.value || undefined,
      project: projectFilter.value,
      plan: planFilter.value,
      workflow_id: wfFilter.value,
      tag: tagFilter.value.trim() || undefined,
      agent: agentFilter.value.trim() || undefined,
      runner: runnerFilter.value.trim() || undefined,
      caller: callerFilter.value.trim() || undefined,
      since: sinceParam(),
      limit: PAGE_SIZE,
      offset: offset.value,
      all: showMessengerJobs.value,
    })
    jobs.value = (resp.jobs ?? []).filter((job) => !hasUploadFilter.value || (job.xfer?.uploads?.length ?? 0) > 0)
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
      plan: planFilter.value,
      workflow_id: wfFilter.value,
      tag: tagFilter.value.trim() || undefined,
      agent: agentFilter.value.trim() || undefined,
      runner: runnerFilter.value.trim() || undefined,
      caller: callerFilter.value.trim() || undefined,
      since: statusCountSince(Math.floor(Date.now() / 1000), sinceParam()),
      limit: STATUS_COUNT_LIMIT,
      all: showMessengerJobs.value,
    })
    countJobs.value = resp.jobs ?? []
    countJobsLoaded.value = true
  } catch {
    countJobsLoaded.value = false
  }
}

// Q3：`jobs` 主题的失效通知（任何 job 状态变化）触发重拉当前页；WS 断开超过 15s 才由
// 30s 一次的兜底轮询接手（恢复后自动停）。
const liveJobs = createLiveTopic('jobs', {
  initial: false,
  fetch: () => {
    void fetchJobs()
    void fetchStatusCounts()
  },
})

// 过滤条件变化 -> 立即刷新（含 E5 的 tag/agent/runner/caller/since）
watch(
  [
    statusFilter,
    projectFilter,
    planFilter,
    wfFilter,
    tagFilter,
    agentFilter,
    runnerFilter,
    callerFilter,
    sinceFilter,
    hasUploadFilter,
    showMessengerJobs,
  ],
  () => {
    offset.value = 0
    void fetchJobs()
  },
)

watch([projectFilter, planFilter, wfFilter, tagFilter, agentFilter, runnerFilter, callerFilter, sinceFilter, showMessengerJobs], () => {
  void fetchStatusCounts()
})

function shortId(id: string): string {
  return id.length > 8 ? id.slice(-8) : id
}

function rowDuration(job: Job): string {
  return fmtDuration(jobDurationSec(job))
}

// 目录锁徽标的路径：优先 lock_paths（多路径），否则 cwd。
function lockPathOf(job: Job): string {
  return job.lock_paths?.length ? job.lock_paths.join(', ') : (job.cwd ?? '')
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
  // 带上当前筛选：并排时列表还在，单栏时详情页的返回链接也能回到同一个筛选。
  void router.push({ path: `/jobs/${encodeURIComponent(job.id)}`, query: { ...route.query } })
}

function refreshNow(): void {
  void fetchJobs()
}

function prevPage(): void {
  if (!canPrevPage.value) {
    return
  }
  offset.value = Math.max(0, offset.value - PAGE_SIZE)
  void fetchJobs()
}

function nextPage(): void {
  if (!canNextPage.value) {
    return
  }
  offset.value += PAGE_SIZE
  void fetchJobs()
}

function clearFilters(): void {
  statusFilter.value = ''
  tagFilter.value = ''
  agentFilter.value = ''
  runnerFilter.value = ''
  callerFilter.value = ''
  sinceFilter.value = ''
  hasUploadFilter.value = false
  showMessengerJobs.value = false
  if (projectFilter.value || planFilter.value) {
    void router.push({ path: listPath() })
  }
}

onMounted(() => {
  void fetchProjects()
  void fetchJobs()
  void fetchStatusCounts()
  liveJobs.start()
})

// 详情页的 BackLink 靠这份记录回到当前筛选 + 滚动位置（设计稿 §16）。
useListReturn('jobs', '/jobs')

onUnmounted(() => {
  liveJobs.stop()
})
</script>

<template>
  <div class="board" :class="{ 'board--embedded': embedded }">
    <div class="board-head">
      <div v-if="!embedded">
        <h1 class="title mono">{{ t('jobs.h.title') }}</h1>
        <p class="subtitle mono">
          {{ t('jobs.h.summary', { n: jobs.length, running: runningCount, problems: problemCount }) }}<span
            v-if="activeFilterCount"
          >{{ t('jobs.h.summaryFilters', { n: activeFilterCount }) }}</span>
        </p>
      </div>
      <!-- 并排时页面标题已由详情页头担任，列表只留一行计数。 -->
      <p v-else class="subtitle mono" data-test="jobs-pane-summary">
        {{ t('jobs.h.summary', { n: jobs.length, running: runningCount, problems: problemCount }) }}<span
          v-if="activeFilterCount"
        >{{ t('jobs.h.summaryFilters', { n: activeFilterCount }) }}</span>
      </p>
      <div class="head-actions mono">
        <button
          v-if="embedded"
          class="head-btn"
          type="button"
          data-test="jobs-split-toggle"
          :title="t('jobs.h.singleTitle')"
          @click="setJobsSplitPreference('single')"
        >
          {{ t('jobs.h.single') }}
        </button>
        <span class="last-refresh">{{ lastRefreshedText }}</span>
        <span class="poll-hint" :class="{ 'poll-hint--on': loading }">●</span>
        <button class="head-btn" type="button" :disabled="loading" @click="refreshNow">
          {{ loading ? t('jobs.h.refreshing') : t('jobs.h.refresh') }}
        </button>
        <button
          class="head-btn"
          type="button"
          :disabled="activeFilterCount === 0"
          @click="clearFilters"
        >
          {{ t('jobs.h.clear') }}
        </button>
      </div>
    </div>

    <!-- 列表头（设计稿 §7）：项目 chip + 状态 chip；其余维度进「筛选」抽屉。 -->
    <div class="filter-bar mono">
      <ProjectFilter
        v-model="projectSelectValue"
        :options="projectOptions"
        :loading="!projectKeys.length && !projectLoadError"
        :error="projectLoadError"
      />
      <FilterChips
        :items="statusChipItems"
        :model-value="statusFilter"
        :label="t('jobs.filter.status')"
        @update:model-value="(v) => (statusFilter = v as '' | JobStatus)"
      />
      <FilterDrawer
        v-model:open="filtersOpen"
        :active-count="drawerFilterCount"
        :clearable="activeFilterCount > 0"
        @clear="clearFilters"
      >
        <div class="controls">
        <span v-if="planFilter" class="proj-chip">{{ t('jobs.filter.planChip', { id: planFilter }) }}</span>
        <label class="filter filter-check"><input v-model="hasUploadFilter" type="checkbox" class="filter-checkbox" /><span class="filter-label">{{ t('jobs.filter.uploads') }}</span></label>
        <label class="filter filter-check"><input v-model="showMessengerJobs" type="checkbox" class="filter-checkbox" /><span class="filter-label">{{ t('jobs.filter.messenger') }}</span></label>
        <label class="filter">
          <span class="filter-label">{{ t('jobs.filter.plan') }}</span>
          <input
            v-model="planInput"
            class="filter-input filter-input--plan mono"
            :placeholder="t('jobs.filter.planPh')"
            spellcheck="false"
            @focus="loadRecentPlans"
            @keydown.enter.prevent="applyPlanInput"
            @blur="applyPlanInput"
          />
        </label>
        <label class="filter">
          <span class="filter-label">{{ t('jobs.filter.wf') }}</span>
          <input
            v-model="wfInput"
            class="filter-input filter-input--plan mono"
            :placeholder="t('jobs.filter.wfPh')"
            spellcheck="false"
            data-test="wf-filter"
            @keydown.enter.prevent="applyWfInput"
            @blur="applyWfInput"
          />
        </label>
        <label class="filter">
          <span class="filter-label">{{ t('jobs.filter.tags') }}</span>
          <input
            v-model="tagFilter"
            class="filter-input mono"
            :placeholder="t('jobs.filter.tagsPh')"
            spellcheck="false"
          />
        </label>
        <label class="filter">
          <span class="filter-label">{{ t('jobs.filter.agent') }}</span>
          <input
            v-model="agentFilter"
            class="filter-input mono"
            :placeholder="t('jobs.filter.agent')"
            spellcheck="false"
          />
        </label>
        <label class="filter">
          <span class="filter-label">{{ t('jobs.filter.runner') }}</span>
          <input
            v-model="runnerFilter"
            class="filter-input mono"
            :placeholder="t('jobs.filter.runner')"
            spellcheck="false"
          />
        </label>
        <label class="filter">
          <span class="filter-label">{{ t('jobs.filter.caller') }}</span>
          <input
            v-model="callerFilter"
            class="filter-input mono"
            :placeholder="t('jobs.filter.callerPh')"
            spellcheck="false"
          />
        </label>
        <label class="filter">
          <span class="filter-label">{{ t('jobs.filter.since') }}</span>
          <select v-model="sinceFilter" class="filter-select mono">
            <option v-for="opt in sinceOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </option>
          </select>
        </label>
        </div>

        <!-- F-b：plan 输入框下方的轻量提示（最近 5 个 open plan，点一下填入），不是下拉。 -->
        <div v-if="recentPlans.length" class="recent-plans mono">
          <span class="recent-label">{{ t('jobs.filter.recent') }}</span>
          <button
            v-for="p in recentPlans"
            :key="p.id"
            type="button"
            class="recent-plan"
            :title="p.title ? `${p.title} · ${p.id}` : p.id"
            @click="pickRecentPlan(p.id)"
          >
            {{ p.id }}
          </button>
        </div>
      </FilterDrawer>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div class="table">
      <div class="thead mono">
        <span class="col-status">{{ t('jobs.col.status') }}</span>
        <span class="col-job">{{ t('jobs.col.job') }}</span>
        <span class="col-proj">{{ t('jobs.col.project') }}</span>
        <span class="col-agent">{{ t('jobs.col.agent') }}</span>
        <span class="col-runner">{{ t('jobs.col.runner') }}</span>
        <span class="col-signal">{{ t('jobs.col.signal') }}</span>
        <span class="col-time">{{ t('jobs.col.time') }}</span>
      </div>

      <div
        v-for="job in jobs"
        :key="job.id"
        class="trow"
      >
        <span class="col-status"><StatusPill :status="job.status" /></span>
        <span
          class="col-job"
          :class="{ 'col-job--titled': job.title }"
          role="button"
          tabindex="0"
          :aria-label="t('jobs.row.open', { id: job.id })"
          :title="job.title || job.id"
          data-test="jobs-row"
          @click="openJob(job)"
          @keydown.enter="openJob(job)"
          @keydown.space.prevent="openJob(job)"
        >
          <span v-if="job.title" class="job-title">{{ job.title }}</span>
          <span class="job-id mono">{{ shortId(job.id) }}</span>
          <span v-if="job.tags && job.tags.length" class="job-tags">
            <span v-for="tag in job.tags" :key="tag" class="tag-chip mono" :title="tag">{{ tag }}</span>
          </span>
          <span v-if="job.role || job.channel || job.read_only || job.verify?.status === 'failed' || job.uncommitted_count" class="job-badges">
            <UncommittedBadge :count="job.uncommitted_count" :files="job.uncommitted_files" />
            <span
              v-if="job.verify?.status === 'failed'"
              class="job-badge job-badge--verify mono"
              :title="t('jobs.row.badgeVerifyTitle', { cmd: (job.verify.command ?? []).join(' '), code: job.verify.exit_code })"
            >{{ t('jobs.row.badgeVerify') }}</span>
            <span
              v-if="job.read_only"
              class="job-badge job-badge--ro mono"
              :title="t('jobs.row.badgeReadOnlyTitle')"
            >{{ t('jobs.row.badgeReadOnly') }}</span>
            <span
              v-if="job.role"
              class="job-badge mono"
              :title="t('jobs.row.badgeRoleTitle', { role: job.role })"
            >{{ job.role }}</span>
            <span
              v-if="job.channel"
              class="job-badge job-badge--chan mono"
              :title="t('jobs.row.badgeChannelTitle', { channel: job.channel })"
            >{{ job.channel }}</span>
          </span>
          <span v-if="job.xfer?.uploads?.length" class="job-badge job-badge--xfer mono" :title="t('jobs.row.badgeAttachmentsTitle')">{{ t('jobs.row.badgeAttachments', { n: job.xfer.uploads.length }) }}</span>
          <span v-if="job.status === 'waiting_dir'" class="job-badge job-badge--waiting mono" :title="t('jobs.row.badgeLockTitle', { path: lockPathOf(job), holder: job.waiting_on_job || t('jobs.row.badgeLockHolder') })">{{ t('jobs.row.badgeLock', { path: lockPathOf(job), holder: job.waiting_on_job || t('jobs.row.badgeLockHolder') }) }}</span>
        </span>
        <span class="col-proj mono">{{ job.project_key }}</span>
        <span class="col-agent mono" :title="job.resume_agent ? t('jobs.row.resumeTitle') : undefined">{{ job.resume_agent ? `${job.resume_agent} ↻` : job.agent }}</span>
        <span class="col-runner mono" :class="{ remote: job.runner !== 'local' }">
          <span class="runner-name" :title="job.runner">{{ runnerLabel(job.runner) }}</span>
          <span v-if="job.worker_id" class="runner-worker" :title="t('jobs.row.workerTitle', { id: job.worker_id })">{{ job.worker_id }}</span>
        </span>
        <span class="col-signal">
          <Signal :status="job.status" :duration-sec="jobDurationSec(job)" />
          <span class="run-dur mono">{{ rowDuration(job) }}</span>
        </span>
        <span class="col-time mono">{{ rowTime(job) }}</span>
      </div>

      <div v-if="jobs.length === 0 && !error" class="empty mono">
        {{ t('jobs.empty') }}
      </div>
    </div>

    <div class="pager mono">
      <button class="pager-btn" type="button" :disabled="!canPrevPage || loading" @click="prevPage">
        {{ t('jobs.page.prev') }}
      </button>
      <span class="pager-page">{{ t('jobs.page.at', { n: pageNumber }) }}</span>
      <button class="pager-btn" type="button" :disabled="!canNextPage || loading" @click="nextPage">
        {{ t('jobs.page.next') }}
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
  gap: 16px;
  margin-bottom: 12px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--ink);
  margin: 0;
}
.subtitle {
  color: var(--ink-3);
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
  color: var(--ink-3);
}
.head-btn {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 10px;
  font-size: 12px;
}
.head-btn:hover:not(:disabled) {
  color: var(--accent);
  border-color: var(--accent);
}
.head-btn:disabled {
  color: var(--ink-3);
  cursor: default;
  opacity: 0.65;
}
.filter-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s2);
  margin-bottom: 14px;
}
.controls {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  font-size: 12px;
}
.proj-chip {
  color: var(--accent);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 2px 8px;
}
.filter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--ink-3);
}
.filter-check {
  min-height: 28px;
}
.filter-checkbox {
  accent-color: var(--accent);
}
.filter-label {
  font-size: 11px;
  letter-spacing: 0.06em;
}
.filter-select {
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
}
.filter-select:focus {
  border-color: var(--accent);
}
.filter-input {
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
  width: 92px;
}
.filter-input:focus {
  border-color: var(--accent);
}
.filter-input::placeholder {
  color: var(--ink-3);
}
/* plan 过滤输入框（F-b）：plan id 比 tag/agent 长得多，给宽一点。 */
.filter-input--plan {
  width: 210px;
}
/* 输入框下方的"最近 open plan"提示（点一下填入），不是下拉。 */
.recent-plans {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 11px;
}
.recent-label {
  color: var(--ink-3);
}
.recent-plan {
  background: none;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--accent);
  font-family: inherit;
  font-size: 11px;
  padding: 1px 6px;
}
.recent-plan:hover {
  border-color: var(--accent);
}
.poll-hint {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-hint--on {
  color: var(--accent);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}

.table {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  overflow: visible;
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
  background: var(--surface);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--ink-3);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  font-size: 13px;
  outline: none;
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--surface);
}
/* job cell: title (primary) stacked over short-id (secondary). When a job has no
   title only the short-id shows, so the row reads the same as before. */
/* 整个 job 单元格可点：点击/键盘(Enter/Space)均进入 job 详情。 */
.col-job {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  cursor: pointer;
  border-radius: 3px;
}
.col-job:hover .job-title,
.col-job:hover .job-id {
  color: var(--accent);
  text-decoration: underline;
}
.col-job:focus-visible {
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 45%, transparent);
  outline: none;
}
.job-title {
  color: var(--ink);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.job-id {
  color: var(--accent);
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
.job-badges {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 2px;
}
.tag-chip {
  font-size: 10px;
  color: var(--accent);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 0 5px;
  line-height: 15px;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.job-badge {
  font-size: 10px;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 0 5px;
  line-height: 15px;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.job-badge--chan {
  color: var(--ink);
}
.job-badge--ro {
  color: var(--warn, var(--accent));
  border-color: var(--warn, var(--accent));
}
/* 验证失败徽标（SUP-01 P2）：一行 failed 里最容易被忽略的失败原因——agent 干完了但验收没过。 */
.job-badge--verify {
  color: var(--fail);
  border-color: var(--fail);
}
.job-badge--xfer {
  color: var(--accent);
  border-color: var(--accent);
}
.job-badge--waiting {
  color: var(--run);
  border-color: var(--run);
}
.col-proj {
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-agent {
  color: var(--ink-3);
}
/* runner cell: runner name (primary) stacked over worker_id (secondary) when a
   runner=worker job ran on a specific worker, so "where it ran" is visible. */
.col-runner {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  color: var(--ink-3);
}
.runner-name,
.runner-worker {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.runner-worker {
  font-size: 10px;
  color: var(--ink-3);
}
/* Remote runners (peer-http / worker) stand out so "where it ran" is visible. */
.col-runner.remote .runner-name {
  color: var(--accent);
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
  color: var(--ink-3);
  text-align: right;
  white-space: nowrap;
}
.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--ink-3);
  font-size: 13px;
}
.pager {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 10px;
  font-size: 12px;
  color: var(--ink-3);
}
.pager-btn {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 10px;
  font-size: 12px;
}
.pager-btn:hover:not(:disabled) {
  color: var(--accent);
  border-color: var(--accent);
}
.pager-btn:disabled {
  color: var(--ink-3);
  cursor: default;
  opacity: 0.65;
}
.pager-page {
  min-width: 64px;
  text-align: center;
}

.filter-toggle {
  display: none;
}

/* 手机档（断点与 utils/mobileLayout.ts 的 MOBILE_MAX_WIDTH = 900 一致）：列头隐藏，一行
   任务改成两行卡片。 */
@media (max-width: 900px) {
  .filter-toggle {
    display: inline-flex;
  }
  .filter-panel:not(.filter-panel--open) {
    display: none;
  }
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
  .pager {
    justify-content: space-between;
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
  /* 窄屏一条 job 压成两行：标题/id + 状态 / project · agent · runner · 耗时 · 时间 */
  .trow {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 2px 0;
    padding: 8px 10px;
  }
  .col-job {
    order: 1;
    flex: 1 0 calc(100% - 96px);
    min-width: 0;
  }
  .col-status {
    order: 2;
    flex: 0 0 96px;
    display: flex;
    justify-content: flex-end;
  }
  .col-proj,
  .col-agent,
  .col-runner,
  .col-signal,
  .col-time {
    order: 3;
    flex: none;
    font-size: 11px;
    text-align: left;
  }
  .col-proj {
    max-width: 40%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .col-agent::before,
  .col-runner::before,
  .col-signal::before,
  .col-time::before {
    content: ' · ';
    white-space: pre;
    color: var(--ink-3);
  }
  .col-signal,
  .col-runner {
    display: inline-flex;
    flex-direction: row;
    align-items: center;
    gap: 4px;
  }
  .job-tags {
    margin-top: 2px;
  }
}

/* 并排模式（设计稿 §16）：列表只占左列（520–700px），七列表格会横向溢出，
   所以和手机一样用两行卡片式行（先标题行 + 状态，再把项目/agent/runner/耗时/时间缩成一行）。 */
.board--embedded .thead {
  display: none;
}
.board--embedded .table {
  overflow-x: visible;
}
.board--embedded .trow {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 2px 0;
  padding: 8px 10px;
}
.board--embedded .col-job {
  order: 1;
  display: flex;
  flex-direction: row;
  flex-wrap: nowrap;
  align-items: baseline;
  gap: 6px;
  flex: 1 0 calc(100% - 96px);
  min-width: 0;
  overflow: hidden;
}
.board--embedded .col-job .job-title {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.board--embedded .col-job .job-id,
.board--embedded .col-job .job-badges {
  flex: none;
  white-space: nowrap;
}
.board--embedded .col-job .job-tags {
  display: none;
}
.board--embedded .col-status {
  order: 2;
  flex: 0 0 96px;
  display: flex;
  justify-content: flex-end;
}
.board--embedded .col-proj,
.board--embedded .col-agent,
.board--embedded .col-runner,
.board--embedded .col-signal,
.board--embedded .col-time {
  order: 3;
  flex: none;
  font-size: 11px;
  text-align: left;
}
.board--embedded .col-proj {
  max-width: 40%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.board--embedded .col-agent::before,
.board--embedded .col-runner::before,
.board--embedded .col-signal::before,
.board--embedded .col-time::before {
  content: ' · ';
  white-space: pre;
  color: var(--ink-3);
}
.board--embedded .col-signal,
.board--embedded .col-runner {
  display: inline-flex;
  flex-direction: row;
  align-items: center;
  gap: 4px;
}

@media (max-width: 760px) {
  .board-head {
    margin-bottom: 8px;
  }
  .head-actions {
    flex-wrap: nowrap;
  }
  .head-actions .head-btn {
    white-space: nowrap;
  }
  .filter-panel {
    gap: 8px;
    padding: 8px;
  }
  .status-tabs {
    flex-wrap: nowrap;
    overflow-x: auto;
    padding-bottom: 2px;
  }
  .status-tab {
    flex: none;
  }
  .trow {
    align-items: baseline;
  }
  /* 标题行收成一行：标题省略号截断、id 紧随、重要徽标保留；普通标签在详情页看。
     （内联排布会溢出到右侧状态列，造成 id 与状态重叠。） */
  .col-job {
    display: flex;
    flex-direction: row;
    flex-wrap: nowrap;
    align-items: baseline;
    gap: 6px;
    min-width: 0;
    overflow: hidden;
  }
  .col-job .job-title {
    flex: 0 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .col-job .job-id,
  .col-job .job-badges {
    flex: none;
    white-space: nowrap;
  }
  .col-job .job-tags {
    display: none;
  }
  .job-badges {
    margin: 0;
  }
  .job-badges .job-badge {
    display: inline-block;
    margin: 0 3px 0 0;
  }
}
</style>
