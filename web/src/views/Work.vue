<script setup lang="ts">
// 「工作项」页（UI 重设计 P4）：全局总览。一张卡片 = 一件事（跨会话），按状态分栏或按工作区分组，
// 顶部计数 chip（全部 / 等我 / 到期 / 未整理）可直接筛选，旁边是项目 chip（§6.4，值进 URL）。
// 手机优先（单列），点开一条进入全宽详情抽屉（两级页面）。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter, type LocationQueryRaw } from 'vue-router'
import { createLiveTopic } from '../utils/useLiveTopic'
import {
  acceptWorkSuggestion,
  ApiError,
  createWorkItem,
  dismissWorkSuggestion,
  listAgentSessions,
  listWorkItems,
  patchWorkItem,
  resumeSession,
  summarizeWorkItem,
} from '../api/client'
import SessionDrawer from '../components/SessionDrawer.vue'
import StewardPanel from '../components/StewardPanel.vue'
import WorkCard from '../components/WorkCard.vue'
import WorkDrawer from '../components/WorkDrawer.vue'
import FilterChips from '../ui/FilterChips.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import StatusPill from '../ui/StatusPill.vue'
import { useProjectFilter } from '../ui/projectFilter'
import { chipItems } from '../ui/filterChips'
import { t } from '../i18n'
import { acceptMergeSuggestion, dismissMergeSuggestion, listMergeSuggestions, type MergeSuggestion } from '../api/steward'
import { mergeSuggestionText } from '../utils/steward'
import {
  boardListOpts,
  captureKindLabel,
  captureSource,
  capturedListOpts,
  excludeCaptured,
  onlyCaptured,
  triageQuery,
  triageTabFromQuery,
  type TriageTab,
} from '../utils/capture'
import { fmtAgo } from '../api/time'
import { pruneExpanded, toggleExpanded } from '../utils/cardExpand'
import { resumeConfirmText, resumeFailText } from '../utils/sessionResume'
import { pageWriteBlock } from '../utils/pageGate'
import { useWhoami } from '../store/whoami'
import {
  groupByStatus,
  groupByWorkspace,
  sortCards,
  unsortedItems,
  visibleItems,
  type WorkFilter,
} from '../utils/work'
import { narrowByProject, workCounts } from '../utils/workBoard'
import type { AgentSession, WorkItem, WorkStatus } from '../api/types'

const route = useRoute()
const router = useRouter()

// 写操作（整理建议 / 合并建议的采纳 · 忽略）调用的 POST /v1/work-items/{id}/suggestions/*、
// POST /v1/work-items/merge-suggestions/{n}/* 在服务端是 Public（work item 没有 owner 模型），
// 只受只读上限约束：只读身份一律灰显（gofer-bp9c，判定见 utils/pageGate.ts）。
const { whoami } = useWhoami()
const writeBlock = computed(() => pageWriteBlock(whoami.value))

const items = ref<WorkItem[]>([])
// 收集箱（status=captured）单独取，永远不进看板 / 分组 / 计数
const captured = ref<WorkItem[]>([])
const sessions = ref<AgentSession[]>([])
const loading = ref(false)
const error = ref('')
const actionError = ref('')
const busyIds = ref<Set<string>>(new Set())
const nowSec = ref(Math.floor(Date.now() / 1000))

const VIEW_KEY = 'gofer.work.view'
function readView(): 'status' | 'workspace' {
  try {
    return localStorage.getItem(VIEW_KEY) === 'workspace' ? 'workspace' : 'status'
  } catch {
    return 'status'
  }
}
const view = ref<'status' | 'workspace'>(readView())
watch(view, (v) => {
  try {
    localStorage.setItem(VIEW_KEY, v)
  } catch {
    // 记不住视图偏好不影响使用
  }
})

// 项目 chip：值进 URL 的 ?project=（唯一真源，可分享、后退可复原），本页在本地收窄
// 任务列表 / 收集箱两份数据（一次最多取这么多条，与迁移前同一口径）。
const { project, setProject, options: projectOptions, loadOptions, loadError } = useProjectFilter('/work')

const filter = ref<WorkFilter>('all')
const q = ref('')
const showClosed = ref(false)
const expanded = ref<Set<string>>(new Set())

const openId = ref(typeof route.query.id === 'string' ? route.query.id : '')
const openSid = ref('')

const mergeSuggestions = ref<MergeSuggestion[]>([])
function titleOf(id: string): string {
  return items.value.find((i) => i.id === id)?.title ?? id
}
async function resolveMerge(sg: MergeSuggestion, accept: boolean): Promise<void> {
  actionError.value = ''
  try {
    await (accept ? acceptMergeSuggestion(sg.id) : dismissMergeSuggestion(sg.id))
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  }
  await load({ silent: true })
}

const sessionMap = computed<Record<string, AgentSession>>(() => Object.fromEntries(sessions.value.map((s) => [s.session_id, s])))

// ---------------- 数据 ----------------
async function load(opts?: { silent?: boolean }): Promise<void> {
  if (!opts?.silent) loading.value = true
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const [w, s, cap] = await Promise.all([
      listWorkItems(boardListOpts(showClosed.value)),
      listAgentSessions({ all: true, limit: 300 }).catch(() => ({ sessions: [] as AgentSession[] })),
      listWorkItems(capturedListOpts()).catch(() => ({ items: [] as WorkItem[] })),
    ])
    items.value = excludeCaptured(w.items ?? [])
    captured.value = onlyCaptured(cap.items ?? [])
    mergeSuggestions.value = await listMergeSuggestions().catch(() => [] as MergeSuggestion[])
    sessions.value = s.sessions ?? []
    expanded.value = pruneExpanded(expanded.value, items.value.map((i) => i.id))
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const liveWork = createLiveTopic('work', { initial: false, fetch: () => load({ silent: true }) })
// 会话状态 / 名称变化也影响卡片上的会话信息
const liveSessions = createLiveTopic('sessions', { initial: false, fetch: () => load({ silent: true }) })

watch(showClosed, () => void load())

// ---------------- 视图 ----------------
// 项目收窄先做，计数 / 分栏 / 未整理区都按收窄后的结果算（chip 上的数字与列表一致）。
const inProject = computed(() => narrowByProject(items.value, project.value))
const capturedInProject = computed(() => narrowByProject(captured.value, project.value))
const counts = computed(() => workCounts(inProject.value, unsortedItems(inProject.value)))
const chipItemsList = computed(() => [
  ...chipItems([{ value: '', label: t('work.f.all') }], { '': counts.value.open }),
  {
    value: 'needs_me',
    label: t('work.f.needsMe'),
    count: counts.value.needs_me,
  },
  { value: 'due', label: t('work.f.due'), count: counts.value.due },
  { value: 'unsorted', label: t('work.f.unsorted'), count: counts.value.unsorted },
])
// FilterChips 的单选口径：空串 = 不筛（'' ↔ 'all'）。
const chipValue = computed<string>({
  get: () => (filter.value === 'all' ? '' : filter.value),
  set: (v) => {
    filter.value = v === '' ? 'all' : (v as WorkFilter)
  },
})

const unsorted = computed(() => sortCards(unsortedItems(inProject.value)))

const shown = computed(() => visibleItems(inProject.value, { filter: filter.value, q: q.value, showClosed: showClosed.value }))
const filtering = computed(() => filter.value !== 'all')
const columns = computed(() =>
  groupByStatus(shown.value, { showClosed: showClosed.value, includeUnsorted: filtering.value || !!q.value.trim() }),
)
const groups = computed(() => groupByWorkspace(shown.value))
// 未整理区只在不筛选、不搜索时单独列出（筛选时它们直接参与结果）
const triageTab = computed<TriageTab>(() => triageTabFromQuery(route.query.tab))
const capturedCount = computed(() => capturedInProject.value.length)
// 「待整理」区：草稿 / 收集箱两个页签；有草稿或收集箱条目、或直接带 ?tab=capture 进来时显示
const showTriageArea = computed(
  () => !filtering.value && !q.value.trim() && (unsorted.value.length > 0 || capturedCount.value > 0 || triageTab.value === 'capture'),
)
function setTriageTab(next: TriageTab): void {
  void router.replace({ query: triageQuery(route.query, next) as LocationQueryRaw })
}

function toggleCard(id: string): void {
  expanded.value = toggleExpanded(expanded.value, id)
}

// ---------------- 抽屉 ----------------
function openItem(id: string): void {
  openId.value = id
  if (route.query.id !== id) void router.replace({ query: { ...route.query, id } })
}
function closeItem(): void {
  openId.value = ''
  if (route.query.id) {
    const rest = { ...route.query }
    delete rest.id
    void router.replace({ query: rest })
  }
}
watch(
  () => route.query.id,
  (v) => {
    openId.value = typeof v === 'string' ? v : ''
  },
)

function openSession(sid: string): void {
  // ACP 持续会话 / 终端 job 会话关联的是 job id：去 job 页，不开终端中继会话抽屉。
  if (items.value.some((i) => i.sessions.some((s) => s.session_id === sid && s.kind === 'job'))) {
    void router.push(`/jobs/${encodeURIComponent(sid)}`)
    return
  }
  openSid.value = sid
}

// ---------------- 操作 ----------------
function setBusy(id: string, on: boolean): void {
  const next = new Set(busyIds.value)
  if (on) next.add(id)
  else next.delete(id)
  busyIds.value = next
}

async function setStatus(it: WorkItem, status: WorkStatus): Promise<void> {
  if (busyIds.value.has(it.id)) return
  if (status === 'dropped' && !window.confirm(t('work.confirm.drop', { title: it.title }))) return
  setBusy(it.id, true)
  actionError.value = ''
  try {
    await patchWorkItem(it.id, { status, rev: it.rev })
    await load({ silent: true })
  } catch (e) {
    actionError.value =
      e instanceof ApiError && e.status === 409 ? t('work.err.conflict', { title: it.title }) : e instanceof Error ? e.message : String(e)
    await load({ silent: true })
  } finally {
    setBusy(it.id, false)
  }
}

// 整理：后台一次性只读 job；结果经 work 推送回来（卡片上出现整理建议 / 字段被填上）。
async function summarize(it: WorkItem): Promise<void> {
  if (busyIds.value.has(it.id)) return
  setBusy(it.id, true)
  actionError.value = ''
  try {
    await summarizeWorkItem(it.id)
    await load({ silent: true })
  } catch (e) {
    actionError.value = e instanceof ApiError ? (e.detail ? `${e.message}：${e.detail}` : e.message) : e instanceof Error ? e.message : String(e)
  } finally {
    setBusy(it.id, false)
  }
}

async function suggestion(it: WorkItem, field: string, accept: boolean): Promise<void> {
  if (busyIds.value.has(it.id)) return
  setBusy(it.id, true)
  actionError.value = ''
  try {
    await (accept ? acceptWorkSuggestion(it.id, field) : dismissWorkSuggestion(it.id, field))
    await load({ silent: true })
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
    await load({ silent: true })
  } finally {
    setBusy(it.id, false)
  }
}

async function wake(sid: string): Promise<void> {
  const s = sessionMap.value[sid]
  if (!s?.can_resume || busyIds.value.has(sid)) return
  if (!window.confirm(resumeConfirmText(s))) return
  setBusy(sid, true)
  actionError.value = ''
  try {
    const res = await resumeSession(sid)
    if (res.job_id) await router.push(`/jobs/${encodeURIComponent(res.job_id)}?attach=1`)
    else await load({ silent: true })
  } catch (e) {
    actionError.value = t('work.err.wake', { reason: resumeFailText(e) })
  } finally {
    setBusy(sid, false)
  }
}

// 新建
const createOpen = ref(false)
const newTitle = ref('')
const newGoal = ref('')
const creating = ref(false)
async function create(): Promise<void> {
  const title = newTitle.value.trim()
  if (!title || creating.value) return
  creating.value = true
  actionError.value = ''
  try {
    const d = await createWorkItem({ title, goal: newGoal.value.trim() || undefined })
    newTitle.value = ''
    newGoal.value = ''
    createOpen.value = false
    await load({ silent: true })
    openItem(d.id)
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}

onMounted(() => {
  void load()
  void loadOptions()
  liveWork.start()
  liveSessions.start()
})
onUnmounted(() => {
  liveWork.stop()
  liveSessions.stop()
})
</script>

<template>
  <div class="work-page">
    <header class="page-head">
      <div class="head-text">
        <h1 class="page-title">{{ t('work.h.title') }}</h1>
        <p class="page-sub" data-test="work-summary">
          {{ counts.open > 0 ? t('work.h.summary', { n: counts.open, needs: counts.needs_me }) : t('work.h.summaryEmpty') }}
        </p>
      </div>
      <div class="head-actions">
        <div class="seg" role="group" :aria-label="t('work.h.view')">
          <button type="button" class="seg-btn" :class="{ on: view === 'status' }" :aria-pressed="view === 'status'" data-test="view-status" @click="view = 'status'">{{ t('work.h.viewStatus') }}</button>
          <button type="button" class="seg-btn" :class="{ on: view === 'workspace' }" :aria-pressed="view === 'workspace'" data-test="view-workspace" @click="view = 'workspace'">{{ t('work.h.viewWorkspace') }}</button>
        </div>
        <label class="check"><input v-model="showClosed" type="checkbox" /> {{ t('work.h.showClosed') }}</label>
        <button class="btn" type="button" :disabled="loading" data-test="refresh" @click="load()">{{ loading ? t('work.h.refreshing') : t('act.refresh') }}</button>
        <button class="btn btn--primary" type="button" data-test="new-toggle" @click="createOpen = !createOpen">{{ createOpen ? t('work.c.collapse') : t('work.c.open') }}</button>
      </div>
    </header>

    <div class="filter-row">
      <ProjectFilter :model-value="project" :options="projectOptions" :error="loadError" @update:model-value="setProject" />
      <FilterChips v-model="chipValue" :items="chipItemsList" :label="t('work.f.chips')" />
      <input v-model="q" class="search" type="search" :placeholder="t('work.f.search')" :aria-label="t('work.f.search')" data-test="search" />
      <button v-if="filter !== 'all' || q.trim()" type="button" class="link-btn" data-test="clear-filters" @click="filter = 'all'; q = ''">{{ t('work.f.clear') }}</button>
    </div>

    <section v-if="createOpen" class="create" data-test="create-form">
      <input v-model="newTitle" class="search" type="text" :placeholder="t('work.c.title')" :aria-label="t('work.c.title')" data-test="new-title" @keydown.enter="create" />
      <input v-model="newGoal" class="search" type="text" :placeholder="t('work.c.goal')" :aria-label="t('work.c.goal')" />
      <button class="btn btn--primary" type="button" :disabled="creating || !newTitle.trim()" data-test="new-submit" @click="create">{{ t('work.c.submit') }}</button>
    </section>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="actionError" class="error" data-test="action-error">{{ actionError }}</p>

    <!-- 管家的合并建议：只是建议，由人确认 -->
    <section v-if="mergeSuggestions.length" class="group" data-test="merge-suggestions">
      <h2 class="group-title">{{ t('work.merge.title') }} <span class="group-count">{{ mergeSuggestions.length }}</span></h2>
      <div v-for="sg in mergeSuggestions" :key="sg.id" class="merge-row" data-test="merge-suggestion">
        <span class="merge-text">{{ mergeSuggestionText(sg, titleOf) }}</span>
        <button class="btn btn--primary" type="button" data-test="merge-accept" :disabled="!!writeBlock" :title="writeBlock || undefined" @click="resolveMerge(sg, true)">{{ t('work.merge.accept') }}</button>
        <button class="btn" type="button" data-test="merge-dismiss" :disabled="!!writeBlock" :title="writeBlock || undefined" @click="resolveMerge(sg, false)">{{ t('work.merge.dismiss') }}</button>
      </div>
    </section>

    <!-- 未整理：自动生成、还没补目标的草稿，不打扰人，只在这里集中 -->
    <section v-if="showTriageArea" class="group" data-test="triage-area">
      <h2 class="group-title">{{ t('work.triage.title') }}</h2>
      <div class="triage-tabs" role="tablist" :aria-label="t('work.triage.title')">
        <button type="button" role="tab" class="triage-tab" :class="{ on: triageTab === 'drafts' }" :aria-selected="triageTab === 'drafts'" data-test="tab-drafts" @click="setTriageTab('drafts')">
          {{ t('work.triage.drafts') }} <span class="group-count">{{ unsorted.length }}</span>
        </button>
        <button type="button" role="tab" class="triage-tab" :class="{ on: triageTab === 'capture' }" :aria-selected="triageTab === 'capture'" data-test="tab-capture" @click="setTriageTab('capture')">
          {{ t('work.triage.capture') }} <span class="group-count" :class="{ 'group-count--hot': capturedCount > 0 }">{{ capturedCount }}</span>
        </button>
      </div>
      <div v-if="triageTab === 'capture'" class="capture-list" data-test="capture-area">
        <p class="group-hint">{{ t('work.triage.captureHint') }}</p>
        <ul v-if="capturedInProject.length" class="capture-rows">
          <li v-for="it in capturedInProject" :key="it.id">
            <button type="button" class="capture-row" :class="{ 'capture-row--active': openId === it.id }" data-test="capture-row" @click="openItem(it.id)">
              <span class="capture-kind" :data-kind="it.kind || 'note'" data-test="capture-kind">{{ captureKindLabel(it.kind) }}</span>
              <span class="capture-text">{{ it.title || it.goal }}</span>
              <span class="capture-meta">
                <span v-if="it.project_key" class="capture-proj">{{ it.project_key }}</span>
                <span v-if="captureSource(it)" class="capture-src" data-test="capture-source">{{ captureSource(it) }}</span>
                <span class="capture-time" :title="String(it.created_at)">{{ fmtAgo(it.created_at, nowSec) }}</span>
              </span>
            </button>
          </li>
        </ul>
        <div v-else class="empty" data-test="capture-empty">{{ t('work.triage.emptyCapture') }}</div>
      </div>
      <template v-else>
        <p class="group-hint">{{ t('work.triage.draftsHint') }}</p>
        <div v-if="unsorted.length" class="icard-grid">
          <WorkCard
            v-for="it in unsorted"
            :key="it.id"
            :item="it"
            :expanded="expanded.has(it.id)"
            :active="openId === it.id"
            :sessions="sessionMap"
            :busy="busyIds.has(it.id)"
            :now-sec="nowSec"
            :write-block="writeBlock"
            @toggle="toggleCard(it.id)"
            @open="openItem(it.id)"
            @open-session="openSession"
            @wake="wake"
            @set-status="(st) => setStatus(it, st)"
            @summarize="summarize(it)"
            @accept-suggestion="(f) => suggestion(it, f, true)"
            @dismiss-suggestion="(f) => suggestion(it, f, false)"
          />
        </div>
        <div v-else class="empty" data-test="drafts-empty">{{ t('work.triage.emptyDrafts') }}</div>
      </template>
    </section>

    <!-- 筛选 / 搜索：平铺结果 -->
    <section v-if="filtering" class="group" data-test="filtered-area">
      <h2 class="group-title">{{ t('work.triage.filtered') }} <span class="group-count">{{ shown.length }}</span></h2>
      <div v-if="shown.length" class="icard-grid">
        <WorkCard
          v-for="it in sortCards(shown)"
          :key="it.id"
          :item="it"
          :expanded="expanded.has(it.id)"
          :active="openId === it.id"
          :sessions="sessionMap"
          :busy="busyIds.has(it.id)"
          :now-sec="nowSec"
          :write-block="writeBlock"
          @toggle="toggleCard(it.id)"
          @open="openItem(it.id)"
          @open-session="openSession"
          @wake="wake"
          @set-status="(st) => setStatus(it, st)"
          @summarize="summarize(it)"
          @accept-suggestion="(f) => suggestion(it, f, true)"
          @dismiss-suggestion="(f) => suggestion(it, f, false)"
        />
      </div>
      <div v-else class="empty">{{ t('work.triage.noMatch') }}</div>
    </section>

    <!-- 按状态分栏 -->
    <div v-else-if="view === 'status'" class="work-cols" data-test="status-view">
      <section v-for="col in columns" :key="col.status" class="work-col" :class="{ 'work-col--empty': !col.items.length }" :data-status="col.status">
        <h2 class="col-title">
          <StatusPill :status="col.status" />
          <span class="group-count">{{ col.items.length }}</span>
        </h2>
        <div class="col-cards">
          <WorkCard
            v-for="it in col.items"
            :key="it.id"
            :item="it"
            :expanded="expanded.has(it.id)"
            :active="openId === it.id"
            :sessions="sessionMap"
            :busy="busyIds.has(it.id)"
            :now-sec="nowSec"
            :write-block="writeBlock"
            @toggle="toggleCard(it.id)"
            @open="openItem(it.id)"
            @open-session="openSession"
            @wake="wake"
            @set-status="(st) => setStatus(it, st)"
            @summarize="summarize(it)"
            @accept-suggestion="(f) => suggestion(it, f, true)"
            @dismiss-suggestion="(f) => suggestion(it, f, false)"
          />
          <p v-if="!col.items.length" class="col-empty">{{ t('work.triage.emptyColumn') }}</p>
        </div>
      </section>
    </div>

    <!-- 按工作区分组 -->
    <div v-else class="work-groups" data-test="workspace-view">
      <section v-for="g in groups" :key="g.key" class="group">
        <h2 class="group-title" :title="g.key">{{ g.label }} <span class="group-count">{{ g.items.length }}</span></h2>
        <div class="icard-grid">
          <WorkCard
            v-for="it in g.items"
            :key="it.id"
            :item="it"
            :expanded="expanded.has(it.id)"
            :active="openId === it.id"
            :sessions="sessionMap"
            :busy="busyIds.has(it.id)"
            :now-sec="nowSec"
            :write-block="writeBlock"
            @toggle="toggleCard(it.id)"
            @open="openItem(it.id)"
            @open-session="openSession"
            @wake="wake"
            @set-status="(st) => setStatus(it, st)"
            @summarize="summarize(it)"
            @accept-suggestion="(f) => suggestion(it, f, true)"
            @dismiss-suggestion="(f) => suggestion(it, f, false)"
          />
        </div>
      </section>
      <div v-if="!groups.length" class="empty">{{ t('work.triage.emptyGroup') }}</div>
    </div>

    <div v-if="!loading && !error && inProject.length === 0" class="empty" data-test="empty">
      {{ t('work.triage.empty') }}
    </div>

    <WorkDrawer
      v-if="openId"
      :id="openId"
      :sessions="sessions"
      :items="items"
      @close="closeItem"
      @changed="load({ silent: true })"
      @open-session="openSession"
      @wake="wake"
      @open-item="openItem"
    />
    <StewardPanel />
    <SessionDrawer v-if="openSid" :sid="openSid" @close="openSid = ''" @changed="load({ silent: true })" @deleted="openSid = ''" />
  </div>
</template>

<style scoped>
.work-page { max-width: 1480px; margin: 0 auto; }
.page-head { display: flex; flex-wrap: wrap; align-items: flex-start; justify-content: space-between; gap: var(--s2); margin-bottom: var(--s3); }
.head-text { min-width: 0; }
.page-title { margin: 0; font-size: var(--fs-xl); font-weight: 600; color: var(--ink); }
.page-sub { margin: 2px 0 0; font-size: var(--fs-sm); color: var(--ink-3); }
.head-actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); font-size: var(--fs-sm); color: var(--ink-3); }
.seg { display: inline-flex; border: 1px solid var(--line); border-radius: var(--r-ctl); overflow: hidden; }
.seg-btn { padding: 4px 10px; font: inherit; font-size: var(--fs-xs); color: var(--ink-3); background: transparent; border: 0; border-right: 1px solid var(--line); cursor: pointer; }
.seg-btn:last-child { border-right: 0; }
.seg-btn.on { color: var(--accent); background: var(--accent-soft); }
.check { display: inline-flex; align-items: center; gap: 5px; font-size: var(--fs-xs); color: var(--ink-3); cursor: pointer; }
.check input { accent-color: var(--accent); margin: 0; }
.filter-row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); margin-bottom: var(--s3); }
.search { flex: 1 1 200px; min-width: 0; min-height: 32px; padding: 4px var(--s2); font: inherit; font-size: var(--fs-sm); color: var(--ink); background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-ctl); }
.search::placeholder { color: var(--ink-3); }
.link-btn { background: none; border: 0; padding: 0 4px; font: inherit; font-size: var(--fs-xs); color: var(--accent); cursor: pointer; text-decoration: underline; }
.create { display: flex; flex-wrap: wrap; gap: var(--s2); margin-bottom: var(--s3); padding: var(--s2); background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-panel); }
.error { color: var(--fail); font-size: var(--fs-sm); border: 1px solid var(--fail); border-radius: var(--r-ctl); padding: 8px 10px; margin: 0 0 var(--s3); word-break: break-word; }
.group { margin-bottom: var(--s5); }
.group-title { display: flex; align-items: center; gap: var(--s2); margin: 0 0 var(--s2); font-size: var(--fs-md); font-weight: 600; color: var(--ink); }
.group-count { border: 1px solid var(--line); border-radius: 999px; padding: 0 7px; font-size: var(--fs-xs); font-weight: 400; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.group-count--hot { border-color: var(--need); color: var(--need); }
.merge-row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); margin-bottom: var(--s2); padding: 8px 10px; background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-panel); }
.merge-text { flex: 1 1 260px; min-width: 0; font-size: var(--fs-sm); color: var(--ink); word-break: break-word; }
.triage-tabs { display: flex; gap: 6px; margin: 0 0 var(--s2); border-bottom: 1px solid var(--line); }
.triage-tab { display: inline-flex; align-items: center; gap: 6px; padding: 7px 14px; margin-bottom: -1px; font: inherit; font-size: var(--fs-sm); color: var(--ink-3); background: transparent; border: 1px solid transparent; border-bottom: 0; border-radius: var(--r-ctl) var(--r-ctl) 0 0; cursor: pointer; }
.triage-tab.on { color: var(--ink); background: var(--surface); border-color: var(--line); }
.triage-tab:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
.group-hint { margin: 0 0 var(--s2); font-size: var(--fs-xs); color: var(--ink-3); }
.capture-rows { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.capture-row { display: grid; grid-template-columns: auto minmax(0, 1fr); column-gap: 10px; row-gap: 4px; align-items: baseline; width: 100%; text-align: left; padding: 9px 12px; color: var(--ink); background: var(--surface); border: 1px solid var(--line); border-left: 3px solid var(--ink-3); border-radius: var(--r-panel); font: inherit; cursor: pointer; }
.capture-row:hover, .capture-row--active { border-color: var(--accent); }
.capture-row:focus-visible { outline: 2px solid var(--focus); outline-offset: 1px; }
.capture-kind { font-size: var(--fs-xs); color: var(--accent); border: 1px solid var(--line); border-radius: 999px; padding: 0 8px; white-space: nowrap; }
.capture-kind[data-kind='issue'] { color: var(--fail); }
.capture-kind[data-kind='request'] { color: var(--run); }
.capture-kind[data-kind='note'] { color: var(--ink-3); }
.capture-text { font-size: var(--fs-sm); line-height: 1.5; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 3; line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
.capture-meta { grid-column: 2; display: flex; flex-wrap: wrap; gap: 4px 12px; font-size: var(--fs-xs); color: var(--ink-3); }
.work-cols { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: var(--s3); align-items: start; }
.work-col { min-width: 0; }
.col-title { display: flex; align-items: center; gap: var(--s2); margin: 0 0 var(--s2); font-size: var(--fs-sm); }
.col-cards { display: flex; flex-direction: column; gap: var(--s2); }
.col-empty { margin: 0; padding: 14px 0; text-align: center; color: var(--ink-3); font-size: var(--fs-sm); border: 1px dashed var(--line); border-radius: var(--r-panel); }
.empty { border: 1px solid var(--line); border-radius: var(--r-panel); color: var(--ink-3); font-size: var(--fs-sm); padding: 28px 14px; text-align: center; }
/* 手机：单列；空的列不占位，免得滑很久才到有内容的列；chip 行在框内横向滑动（FilterChips 自己处理）。 */
@media (max-width: 760px) {
  .work-cols { grid-template-columns: minmax(0, 1fr); gap: var(--s1); }
  .work-col--empty { display: none; }
  .head-actions { width: 100%; }
  .filter-row .search { flex: 1 1 100%; min-height: 40px; }
  .seg-btn { min-height: 40px; }
}
</style>
