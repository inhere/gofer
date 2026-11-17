<script setup lang="ts">
// Sessions（R13）：三类会话合一的历史页。数据全部来自工作台线程接口（R10 的 agent /
// runner 筛选与 counts），默认包含已结束；点一行进工作台对应线程。新建会话与工作台
// 重复的入口已删除（G032）：新建只留在工作台侧栏顶部。
// 终端会话特有的操作（中继开关、授权、唤醒 / 接管、转归属、删除）都在详情抽屉
// （SessionDrawer）里，行上只给入口，不丢功能。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import InfoCard from '../components/InfoCard.vue'
import SessionDrawer from '../components/SessionDrawer.vue'
import { toggleExpanded } from '../utils/cardExpand'
import { createLiveTopic } from '../utils/useLiveTopic'
import {
  attachWorkSession,
  downloadPtyRecording,
  listAgentSessions,
  listRecentPtySessions,
  listWorkItems,
} from '../api/client'
import { getMetaCached } from '../api/metaCache'
import { listWorkbenchThreads } from '../api/workbench'
import { fmtAgo, fmtDateTime } from '../api/time'
import type {
  AgentSession,
  MetaResp,
  PtySession,
  WorkItem,
  WorkbenchSourceCounts,
  WorkbenchStatus,
  WorkbenchThread,
  WorkbenchThreadsResp,
} from '../api/types'

import { runnerLabel } from '../utils/runnerDisplay'
import { acpThreadIsEnded, acpThreadJobLink, acpThreadLink } from '../utils/acpThreads'
import { canContinueThread, continueThread } from '../utils/sessionContinue'
import {
  countFor,
  historyQuery,
  historyTabs,
  type HistorySource,
} from '../utils/sessionHistory'
import { findQuery, findRowTarget } from '../utils/sessionFind'
import { findSummary, hitReasons, topicDisplay } from '../utils/sessionTopic'
import { sessionDisplayName } from '../utils/sessionMessaging'
import { t } from '../i18n'
import Tabs from '../ui/Tabs.vue'
import StatusPill from '../ui/StatusPill.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import { useProjectFilter } from '../ui/projectFilter'
import { tabFromQuery } from '../ui/tabs'

const PAGE_SIZE = 50
// 页签值用 'all'（接口不带 source），避免空串在 Tabs / URL 里的歧义。
type HistoryTab = 'all' | HistorySource
const TAB_VALUES: HistoryTab[] = ['all', 'acp', 'pty', 'relay']

const route = useRoute()
const router = useRouter()

// 项目筛选沿用 URL ?project=（设计稿 §6.4）：唯一真源。
const { project, setProject, options: projectOptions, loadOptions: loadProjectOptions } = useProjectFilter('/sessions')

const threads = ref<WorkbenchThread[]>([])
const counts = ref<WorkbenchSourceCounts | undefined>(undefined)
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')
const cursor = ref('')
const hasMore = ref(false)
const expanded = ref<Set<string>>(new Set())
const nowSec = ref(Math.floor(Date.now() / 1000))

// 筛选：agent / runner / 状态 / 搜索 + 「只看进行中」。状态与搜索都交给服务端过滤，
// 页签条数与列表因此永远出自同一份条件。
const agentFilter = ref('')
const runnerFilter = ref('')
const statusFilter = ref<WorkbenchStatus | ''>('')
const searchInput = ref('')
const activeQuery = ref('')
const onlyLive = ref(false)
const meta = ref<MetaResp>({ projects: [], agents: [], runners: [], workers: [] })

const tabItems = computed(() =>
  historyTabs(counts.value, {
    '': t('sessions.history.all'),
    acp: t('sessions.tab.acp'),
    pty: t('sessions.tab.pty'),
    relay: t('sessions.tab.relay'),
  }).map((tab) => ({
    value: tab.value === '' ? 'all' : tab.value,
    label: tab.label,
    count: tab.count,
  })),
)
const activeTab = computed<HistoryTab>(() => {
  const value = tabFromQuery(route.query, tabItems.value) as HistoryTab
  return TAB_VALUES.includes(value) ? value : 'all'
})
function sourceOf(tab: HistoryTab): HistorySource {
  return tab === 'all' ? '' : tab
}

const agentOptions = computed(() => (meta.value.agents ?? []).map((a) => a.key).sort((a, b) => a.localeCompare(b)))
const runnerOptions = computed(() => (meta.value.runners ?? []).map((r) => r.name).sort((a, b) => a.localeCompare(b)))
const statusOptions = computed<Array<{ value: WorkbenchStatus | ''; label: string }>>(() => [
  { value: '', label: t('sessions.status.all') },
  { value: 'blocked', label: t('sessions.history.status.blocked') },
  { value: 'working', label: t('sessions.history.status.working') },
  { value: 'review', label: t('sessions.history.status.review') },
  { value: 'idle', label: t('sessions.history.status.idle') },
  { value: 'done', label: t('sessions.history.status.done') },
])

// 工作项归属（W1）：卡片上显示工作项标题并可跳到「工作」页；终端录制的下载入口要用
// pty 会话的录制标记，所以另外拉一份最近 pty 会话按 session id 索引。
const workItems = ref<WorkItem[]>([])
const ptyBySession = ref<Map<string, PtySession>>(new Map())
const downloadingRecordingIds = ref<Set<string>>(new Set())
const expandedWork = ref<Record<string, string>>({})
const linkBusy = ref('')
const linkError = ref('')

const workBySession = computed(() => {
  const m = new Map<string, WorkItem>()
  for (const item of workItems.value) {
    for (const sid of item.session_ids ?? []) if (!m.has(sid)) m.set(sid, item)
  }
  return m
})
// 线程的 session id：'s:<sid>' / 'r:<sid>'；一次性 job 线程没有。
function threadSessionID(thread: WorkbenchThread): string {
  return thread.id.includes(':') ? thread.id.slice(thread.id.indexOf(':') + 1) : ''
}
function workFor(thread: WorkbenchThread): WorkItem | undefined {
  const sid = threadSessionID(thread)
  return (thread.latest_job_id ? workBySession.value.get(thread.latest_job_id) : undefined) ??
    (sid ? workBySession.value.get(sid) : undefined)
}
const linkableItems = computed(() => workItems.value.filter((item) => !['done', 'dropped'].includes(item.status)))

async function linkJobToWork(jobID: string | undefined): Promise<void> {
  const workID = jobID ? expandedWork.value[jobID] : ''
  if (!jobID || !workID) return
  linkBusy.value = jobID
  linkError.value = ''
  try {
    await attachWorkSession(workID, jobID)
    await loadWorkItems()
  } catch (e) {
    linkError.value = e instanceof Error ? e.message : String(e)
  } finally {
    linkBusy.value = ''
  }
}

async function loadWorkItems(): Promise<void> {
  try {
    workItems.value = (await listWorkItems({ limit: 500 })).items ?? []
  } catch {
    workItems.value = []
  }
}

async function loadPtySessions(): Promise<void> {
  try {
    const resp = await listRecentPtySessions(PAGE_SIZE)
    ptyBySession.value = new Map(
      (resp.sessions ?? [])
        .filter((s): s is PtySession & { session_id: string } => !!s.session_id)
        .map((s) => [s.session_id, s]),
    )
  } catch {
    // 只是附加信息（录制下载）：拉不到就不显示。
    ptyBySession.value = new Map()
  }
}

function flattenThreads(resp: WorkbenchThreadsResp): WorkbenchThread[] {
  return (resp.projects ?? []).flatMap((group) => group.threads ?? [])
}

function currentFilters() {
  return {
    project: project.value || undefined,
    agent: agentFilter.value || undefined,
    runner: runnerFilter.value || undefined,
    status: statusFilter.value,
    q: activeQuery.value,
  }
}

async function loadHistory(reset = true): Promise<void> {
  if (reset) {
    loading.value = true
  } else {
    loadingMore.value = true
  }
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const resp = await listWorkbenchThreads(
      {
        ...historyQuery(currentFilters(), sourceOf(activeTab.value), {
          limit: PAGE_SIZE,
          cursor: reset ? undefined : cursor.value,
          // counts 与 source / 分页无关：第一页（含切页签、改筛选）都带上，条数才不会掉成 0；
          // 追加更早的一页时不必重算。
          counts: reset,
        }),
        live: onlyLive.value || undefined,
      },
    )
    const rows = flattenThreads(resp)
    threads.value = reset
      ? rows
      : [...threads.value, ...rows.filter((row) => !threads.value.some((old) => old.id === row.id))]
    counts.value = resp.counts ?? counts.value
    cursor.value = resp.next_cursor ?? ''
    hasMore.value = !!resp.has_more && !!resp.next_cursor
    expanded.value = new Set([...expanded.value].filter((id) => threads.value.some((row) => row.id === id)))
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

function runSearch(): void {
  // 「按主题 / 路径找」打开时搜索走会话查找接口（gofer session find 同一套口径），
  // 关掉就走历史列表的服务端过滤。
  if (findMode.value) {
    void loadFind()
    return
  }
  activeQuery.value = searchInput.value
  void loadHistory()
}

function clearSearch(): void {
  searchInput.value = ''
  runSearch()
}

function reload(): void {
  if (findMode.value) {
    void loadFind()
    return
  }
  void loadHistory()
}

// ---------------- 按主题 / 路径找（R13 保留旧页的查找） ----------------
//
// 旧会话页的查找：关键词（主题 / 会话名 / 项目）或路径（如 internal/job）打 GET /v1/sessions?q=|path=，
// 结果按服务端相关度排序、带命中原因；点一行打开该会话。合一历史页按新结构接回：入口是
// 搜索框旁的开关，结果是一行一条（不是旧的整页卡片），终端会话仍进详情抽屉。
const findMode = ref(false)
const findRows = ref<AgentSession[]>([])
const findLoading = ref(false)
const findError = ref('')
// 已生效的查找词：摘要文案用它，输入框继续改不影响上一轮结果的说明。
const findText = ref('')

async function loadFind(): Promise<void> {
  const query = findQuery(searchInput.value, PAGE_SIZE, project.value)
  findText.value = query ? searchInput.value : ''
  if (!query) {
    findRows.value = []
    findError.value = ''
    return
  }
  findLoading.value = true
  try {
    findRows.value = (await listAgentSessions(query)).sessions ?? []
    findError.value = ''
  } catch (e) {
    findRows.value = []
    findError.value = e instanceof Error ? e.message : String(e)
  } finally {
    findLoading.value = false
  }
}

// 点一行：终端会话（relay）开详情抽屉；ACP / pty 有工作台线程的进那个线程。
function openFound(row: AgentSession): void {
  const target = findRowTarget(row.session_id, threads.value)
  if (target.kind === 'thread') {
    void router.push(target.link)
    return
  }
  openDrawer(target.sid)
}

watch([project, agentFilter, runnerFilter, statusFilter, onlyLive], () => { void reload() })
watch(activeTab, () => { void loadHistory() })
// 开关切换：打开时若已经有输入就立即按查找跑一轮，关掉时回到历史列表（列表可能已过期）。
watch(findMode, (on) => {
  if (!on) {
    void loadHistory()
    return
  }
  if (searchInput.value.trim()) void loadFind()
})

// ---------------- 行上的动作 ----------------

// 打开：进工作台对应线程（三类会话同一个入口）。
function openThread(thread: WorkbenchThread): void {
  void router.push(acpThreadLink(thread.id))
}

// 中继会话（hook 登记、可接管 / 唤醒的终端会话）的详情抽屉：中继开关、授权、唤醒 /
// 接管、转归属、主题、删除都在里面。
const openSid = ref(typeof route.query.sid === 'string' ? route.query.sid : '')
function openDrawer(sid: string): void {
  openSid.value = sid
  if (route.query.sid !== sid) void router.replace({ query: { ...route.query, sid } })
}
function closeDrawer(): void {
  openSid.value = ''
  if (route.query.sid) {
    const query = { ...route.query }
    delete query.sid
    void router.replace({ query })
  }
}
watch(() => route.query.sid, (value) => { openSid.value = typeof value === 'string' ? value : '' })

// 终端会话（pty）：接回终端 + 录制下载。
function isInteractive(thread: WorkbenchThread): boolean {
  return !!thread.interactive
}
function ptyOf(thread: WorkbenchThread): PtySession | undefined {
  const sid = threadSessionID(thread)
  return sid ? ptyBySession.value.get(sid) : undefined
}
function attachLink(thread: WorkbenchThread): string {
  return `/jobs/${encodeURIComponent(thread.latest_job_id ?? '')}?attach=1`
}
async function downloadRecording(thread: WorkbenchThread): Promise<void> {
  const pty = ptyOf(thread)
  const jobID = thread.latest_job_id
  if (!pty?.has_recording || !jobID || downloadingRecordingIds.value.has(pty.pty_session_id)) return
  downloadingRecordingIds.value = new Set(downloadingRecordingIds.value).add(pty.pty_session_id)
  try {
    await downloadPtyRecording(jobID)
  } finally {
    const next = new Set(downloadingRecordingIds.value)
    next.delete(pty.pty_session_id)
    downloadingRecordingIds.value = next
  }
}

// 接着聊（R14）：已结束的 ACP 会话走现有续接接口（新开持续会话、session/load 后停在等待输入）。
const continuingIDs = ref<Set<string>>(new Set())
const continueErrors = ref<Map<string, string>>(new Map())
async function continueEnded(thread: WorkbenchThread): Promise<void> {
  if (continuingIDs.value.has(thread.id)) return
  continuingIDs.value = new Set(continuingIDs.value).add(thread.id)
  const errors = new Map(continueErrors.value)
  errors.delete(thread.id)
  continueErrors.value = errors
  try {
    const job = await continueThread(thread)
    continueErrors.value = new Map(continueErrors.value)
    if (job.session_id) {
      void router.push(acpThreadLink(`s:${job.session_id}`))
    } else {
      void router.push(`/jobs/${encodeURIComponent(job.id)}`)
    }
  } catch (e) {
    continueErrors.value = new Map(continueErrors.value).set(
      thread.id,
      e instanceof Error ? e.message : String(e),
    )
  } finally {
    const busy = new Set(continuingIDs.value)
    busy.delete(thread.id)
    continuingIDs.value = busy
  }
}

function continueLabel(thread: WorkbenchThread): string {
  return continuingIDs.value.has(thread.id) ? t('sessions.history.continuing') : t('sessions.history.continue')
}

function toneOf(thread: WorkbenchThread): 'live' | 'hot' | 'off' {
  if (acpThreadIsEnded(thread)) return 'off'
  if (thread.status === 'blocked') return 'hot'
  return 'live'
}
function isEnded(thread: WorkbenchThread): boolean {
  return acpThreadIsEnded(thread)
}
function showTime(sec: number): string {
  return fmtDateTime(sec)
}
function countLabel(): string {
  const n = countFor(counts.value, sourceOf(activeTab.value))
  return n == null ? '' : String(n)
}

const liveThreads = createLiveTopic('jobs', { initial: false, fetch: () => loadHistory() })
const liveSessions = createLiveTopic('sessions', { initial: false, fetch: () => loadHistory() })
const liveWork = createLiveTopic('work', { initial: false, fetch: () => loadWorkItems() })

onMounted(() => {
  void loadProjectOptions()
  void loadWorkItems()
  void loadPtySessions()
  void getMetaCached().then((value) => { meta.value = value }).catch(() => {})
  void loadHistory()
  liveThreads.start()
  liveSessions.start()
  liveWork.start()
})

onUnmounted(() => {
  liveThreads.stop()
  liveSessions.stop()
  liveWork.stop()
})
</script>

<template>
  <div class="board page">
    <header class="board-head">
      <div class="head-main">
        <h1 class="title">{{ t('sessions.title') }}</h1>
        <p class="head-summary">{{ t('sessions.history.summary', { n: counts ? counts.all : threads.length }) }}</p>
      </div>
      <div class="head-filters">
        <ProjectFilter :model-value="project" :options="projectOptions" @update:model-value="setProject" />
      </div>
    </header>

    <Tabs :tabs="tabItems" fallback="all" :label="t('sessions.tabsLabel')">
      <template #default>
        <section class="group" data-test="history-list">
          <form class="find-box" data-test="find-box" role="search" @submit.prevent="runSearch">
            <input
              v-model="searchInput"
              class="find-input"
              type="search"
              data-test="find-input"
              :placeholder="findMode ? t('sessions.find.placeholder') : t('sessions.history.searchPlaceholder')"
              :aria-label="findMode ? t('sessions.find.label') : t('sessions.history.searchPlaceholder')"
            />
            <button class="act" type="submit" data-test="find-submit">{{ t('sessions.find.submit') }}</button>
            <label class="check find-mode" :title="t('sessions.history.findHint')">
              <input v-model="findMode" type="checkbox" data-test="find-mode" />
              {{ t('sessions.history.findByTopic') }}
            </label>
            <button v-if="activeQuery || searchInput" class="act" type="button" data-test="find-clear" @click="clearSearch">{{ t('sessions.find.clear') }}</button>
          </form>

          <div v-if="!findMode" class="filter-row">
            <label class="filter-field">
              <span>agent</span>
              <select v-model="agentFilter" data-test="history-agent">
                <option value="">{{ t('sessions.history.any') }}</option>
                <option v-for="key in agentOptions" :key="key" :value="key">{{ key }}</option>
              </select>
            </label>
            <label class="filter-field">
              <span>runner</span>
              <select v-model="runnerFilter" data-test="history-runner">
                <option value="">{{ t('sessions.history.any') }}</option>
                <option v-for="key in runnerOptions" :key="key" :value="key">{{ runnerLabel(key) }}</option>
              </select>
            </label>
            <label class="filter-field">
              <span>{{ t('sessions.filter.status') }}</span>
              <select v-model="statusFilter" data-test="history-status">
                <option v-for="item in statusOptions" :key="item.value || 'all'" :value="item.value">{{ item.label }}</option>
              </select>
            </label>
            <label class="check">
              <input v-model="onlyLive" type="checkbox" data-test="history-live" />
              {{ t('sessions.history.onlyLive') }}
            </label>
            <span class="grow"></span>
            <span v-if="countLabel()" class="group-count" data-test="history-count">{{ countLabel() }}</span>
            <button class="act" type="button" :disabled="loading" data-test="history-refresh" @click="reload">
              {{ loading ? t('sessions.refreshing') : t('sessions.refresh') }}
            </button>
          </div>

          <p v-if="error" class="error mono">{{ error }}</p>
          <p v-if="linkError" class="error mono" data-test="link-error">{{ linkError }}</p>

          <div v-if="findMode" class="find-results" data-test="find-results">
            <div class="find-head">
              <span v-if="findText.trim()" class="find-note" data-test="find-summary">{{ findSummary(findRows.length, findText) }}</span>
              <span v-else class="find-note" data-test="find-hint">{{ t('sessions.history.findHint') }}</span>
              <button class="act" type="button" :disabled="findLoading" data-test="find-refresh" @click="loadFind">
                {{ findLoading ? t('sessions.refreshing') : t('sessions.refresh') }}
              </button>
            </div>
            <p v-if="findError" class="error mono" data-test="find-error">{{ findError }}</p>
            <div v-if="findRows.length > 0" class="find-list">
              <button
                v-for="row in findRows"
                :key="row.session_id"
                class="find-row"
                type="button"
                data-test="find-row"
                :title="row.session_id"
                @click="openFound(row)"
              >
                <span class="find-name mono" data-test="find-name">{{ sessionDisplayName(row) }}</span>
                <span
                  v-if="topicDisplay(row).text"
                  class="find-topic mono"
                  :class="{ 'find-topic--inferred': topicDisplay(row).inferred }"
                  :title="topicDisplay(row).title || t('sessions.card.inferredTitle')"
                  data-test="find-topic"
                >{{ topicDisplay(row).text }}<em v-if="topicDisplay(row).inferred" class="find-topic-tag">{{ t('sessions.card.inferred') }}</em></span>
                <span v-for="(reason, i) in hitReasons(row.hits)" :key="i" class="find-reason mono" data-test="hit-reason">{{ reason }}</span>
                <span v-if="row.maybe_offline" class="find-reason mono" data-test="find-offline">{{ t('sessions.card.maybeOffline') }}</span>
                <span class="find-where mono">{{ row.agent }} · {{ row.project_key || '—' }}</span>
              </button>
            </div>
            <div v-else-if="!findLoading && !findError" class="empty" data-test="find-empty">{{ t('sessions.history.findEmpty') }}</div>
          </div>

          <div v-if="!findMode && threads.length > 0" class="icard-grid" data-test="history-cards">
            <InfoCard
              v-for="thread in threads"
              :key="thread.id"
              :tone="toneOf(thread)"
              :dim="isEnded(thread)"
              :expanded="expanded.has(thread.id)"
              :card-id="thread.id"
              @toggle="expanded = toggleExpanded(expanded, thread.id)"
            >
              <template #title>
                <RouterLink class="thread-title mono" :to="acpThreadLink(thread.id)" data-test="history-title">{{ thread.title || thread.id }}</RouterLink>
              </template>
              <template #badges>
                <StatusPill :status="thread.status" />
              </template>
              <template #meta>
                <span class="mono">{{ thread.agent || '—' }}</span>
                <span class="mono">{{ thread.project_key || '—' }}</span>
                <span class="mono">{{ runnerLabel(thread.runner) || '—' }}</span>
                <span class="mono" :title="thread.cwd">{{ thread.cwd || '—' }}</span>
                <span v-if="thread.turns" class="mono">{{ t('sessions.card.turns') }} {{ thread.turns }}</span>
                <span class="mono" :title="showTime(thread.updated_at)">{{ fmtAgo(thread.updated_at, nowSec) }}</span>
                <RouterLink
                  v-if="workFor(thread)"
                  class="icard-chip mono"
                  data-test="history-work-link"
                  :to="`/work?id=${encodeURIComponent(workFor(thread)!.id)}`"
                  :title="t('sessions.card.workItemTitle', { title: workFor(thread)!.title })"
                >{{ t('sessions.card.workItem', { title: workFor(thread)!.title }) }}</RouterLink>
              </template>
              <template #actions>
                <button class="icard-btn icard-btn--primary mono" type="button" data-test="history-open" @click="openThread(thread)">
                  {{ t('sessions.history.open') }}
                </button>
                <button
                  v-if="canContinueThread(thread)"
                  class="icard-btn mono"
                  type="button"
                  data-test="history-continue"
                  :disabled="continuingIDs.has(thread.id)"
                  :title="continueErrors.get(thread.id) || t('sessions.history.continueTitle')"
                  @click="continueEnded(thread)"
                >{{ continueLabel(thread) }}</button>
                <RouterLink v-if="isInteractive(thread)" class="icard-btn mono" :to="attachLink(thread)" data-test="history-attach">
                  {{ t('sessions.card.openTerminal') }}
                </RouterLink>
                <button
                  v-if="thread.kind === 'relay'"
                  class="icard-btn mono"
                  type="button"
                  data-test="history-detail"
                  @click="openDrawer(threadSessionID(thread))"
                >{{ t('sessions.history.detail') }}</button>
                <RouterLink v-else-if="thread.latest_job_id" class="icard-btn mono" :to="thread.latest_job_id ? acpThreadJobLink(thread.latest_job_id) : '#'" data-test="history-job">
                  {{ t('sessions.card.job') }}
                </RouterLink>
              </template>
              <template #details>
                <dl class="icard-kv mono">
                  <dt>{{ t('sessions.history.thread') }}</dt>
                  <dd :title="thread.id">{{ thread.id }}</dd>
                  <dt>{{ t('sessions.card.job') }}</dt>
                  <dd>
                    <RouterLink v-if="thread.latest_job_id" :to="acpThreadJobLink(thread.latest_job_id)">{{ thread.latest_job_id }}</RouterLink>
                    <template v-else>—</template>
                  </dd>
                  <dt>{{ t('sessions.card.started') }}</dt><dd>{{ showTime(thread.started_at) }}</dd>
                  <dt>{{ t('sessions.card.lastReply') }}</dt><dd>{{ showTime(thread.updated_at) }}</dd>
                  <template v-if="thread.job_ids?.length">
                    <dt>{{ t('sessions.history.turns') }}</dt>
                    <dd class="turn-links">
                      <RouterLink v-for="id in thread.job_ids" :key="id" :to="acpThreadJobLink(id)">{{ id }}</RouterLink>
                    </dd>
                  </template>
                  <template v-if="isInteractive(thread) && ptyOf(thread)">
                    <dt>{{ t('sessions.card.recording') }}</dt>
                    <dd>
                      <template v-if="ptyOf(thread)!.has_recording">
                        <button
                          class="icard-btn mono"
                          type="button"
                          :disabled="downloadingRecordingIds.has(ptyOf(thread)!.pty_session_id)"
                          data-test="history-recording"
                          @click="downloadRecording(thread)"
                        >{{ downloadingRecordingIds.has(ptyOf(thread)!.pty_session_id) ? t('sessions.card.downloading') : t('sessions.card.downloadRecording') }}</button>
                      </template>
                      <template v-else>{{ t('sessions.card.notRecorded') }}</template>
                    </dd>
                  </template>
                  <template v-if="thread.latest_job_id && !workFor(thread) && linkableItems.length">
                    <dt>{{ t('sessions.card.linkWork') }}</dt>
                    <dd class="link-pick">
                      <select v-model="expandedWork[thread.latest_job_id]" class="mono" data-test="history-link-pick">
                        <option value="">{{ t('sessions.card.pickWork') }}</option>
                        <option v-for="item in linkableItems" :key="item.id" :value="item.id">{{ item.title }}</option>
                      </select>
                      <button
                        class="icard-btn mono"
                        type="button"
                        :disabled="!expandedWork[thread.latest_job_id] || linkBusy === thread.latest_job_id"
                        data-test="history-link-btn"
                        @click="linkJobToWork(thread.latest_job_id)"
                      >{{ t('sessions.card.link') }}</button>
                    </dd>
                  </template>
                </dl>
              </template>
            </InfoCard>
          </div>

          <button v-if="!findMode && hasMore" class="act" type="button" :disabled="loadingMore" data-test="history-load-more" @click="loadHistory(false)">
            {{ loadingMore ? t('sessions.acp.loadingMore') : t('sessions.acp.loadMore') }}
          </button>
          <div v-else-if="!findMode && !loading && !error && threads.length === 0" class="empty">{{ t('sessions.history.empty') }}</div>
        </section>
      </template>
    </Tabs>

    <SessionDrawer
      v-if="openSid"
      :sid="openSid"
      @close="closeDrawer"
      @changed="loadHistory()"
      @deleted="loadHistory()"
    />
  </div>
</template>

<style scoped>
.board { min-width: 0; }
.board-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 14px;
}
.head-main { min-width: 0; }
.title { font-size: 16px; letter-spacing: 0.08em; color: var(--ink); margin: 0; }
.head-summary { margin: 2px 0 0; color: var(--ink-3); font-size: var(--fs-sm); }
.head-filters { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; }
.group { margin-bottom: 26px; }
.find-box { display: flex; gap: 8px; margin: 0 0 10px; }
.find-input {
  flex: 1; min-width: 0; padding: 5px 10px; border: 1px solid var(--line); border-radius: var(--r-chip);
  background: var(--sunken); color: var(--ink); font-size: 12px;
}
.find-input:focus { outline: none; border-color: var(--accent); }
/* 按主题 / 路径找：开关 + 一行一条的查找结果（命中原因用小胶囊）。 */
.find-mode { flex: 0 0 auto; white-space: nowrap; }
.find-results { display: flex; flex-direction: column; gap: 10px; margin: 0 0 12px; }
.find-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.find-note { margin: 0; color: var(--ink-3); font-size: 12px; }
.find-list { display: flex; flex-direction: column; gap: 6px; }
.find-row {
  display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap;
  width: 100%; text-align: left; cursor: pointer;
  padding: 8px 10px;
  background: var(--sunken); color: var(--ink);
  border: 1px solid var(--line); border-radius: var(--r-chip);
  font-size: 12px;
}
.find-row:hover { border-color: var(--accent); }
.find-name { color: var(--ink); overflow-wrap: anywhere; }
.find-topic { color: var(--ink-2); overflow-wrap: anywhere; }
.find-topic--inferred { color: var(--ink-3); font-style: italic; }
.find-topic-tag { margin-left: 4px; font-size: 10px; font-style: normal; color: var(--ink-3); }
.find-reason {
  border: 1px solid var(--line); border-radius: 9px; padding: 0 7px;
  font-size: 10px; color: var(--ink-3);
}
.find-where { margin-left: auto; color: var(--ink-3); }
.filter-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin: 0 0 10px;
  color: var(--ink-3);
  font-size: 12px;
}
.filter-field { display: inline-flex; align-items: center; gap: 5px; }
.filter-field select {
  min-width: 0;
  padding: 3px 6px;
  background: var(--surface-2);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  font: inherit;
  font-size: 12px;
}
.check { display: inline-flex; align-items: center; gap: 5px; cursor: pointer; font-size: 11px; color: var(--ink-3); user-select: none; }
.check input { accent-color: var(--accent); margin: 0; }
.grow { flex: 1; }
.group-count { border: 1px solid var(--line); border-radius: 9px; padding: 0 7px; font-size: 10px; color: var(--ink-3); }
.thread-title { color: var(--ink); text-decoration: none; overflow-wrap: anywhere; }
.thread-title:hover { color: var(--accent); }
.turn-links { display: flex; gap: 8px; flex-wrap: wrap; }
.link-pick { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
.link-pick select { max-width: 100%; min-width: 0; }
.act {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 10px;
  font-size: 11px;
  text-decoration: none;
  cursor: pointer;
}
.act:hover:not(:disabled) { border-color: var(--accent); color: var(--accent); }
.act:disabled { opacity: 0.45; cursor: default; }
.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}
.empty {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--ink-3);
  font-size: 13px;
  padding: 28px 14px;
  text-align: center;
}
@media (max-width: 760px) {
  .find-input { font-size: 16px; }
  .find-where { margin-left: 0; }
  .board-head { flex-wrap: wrap; gap: 8px; }
  .filter-row { gap: 8px; }
}
</style>
