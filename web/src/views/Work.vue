<script setup lang="ts">
// 「工作项」页：全局总览。一张卡片 = 一件事（跨会话）。
// 布局（P6 收尾，issue gofer-hez1）：
//  - 「按状态」是一条平铺列表：不再按「待整理 / 状态列」分段，消除组间与列高不一的空洞；
//    顺序在 utils/workBoard.ts 一处定义（WORK_LANES：等我 → 进行中 → 等资源 → 草稿 → 收集箱 → 已完成）；
//  - 「按工作区」保留分组，但卡片是同一套；
//  - 状态筛选是 chip（带计数，值进 URL ?status=），到期提醒是旁边的一组标记 chip；
//  - 卡片状态用左边框颜色（状态族 token）+ 状态文字，不靠颜色单独表达。
// 手机优先（单列，chip 行在框内横向滑动），点开一条进入全宽详情抽屉（两级页面）。
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
import Button from '../ui/Button.vue'
import FilterChips from '../ui/FilterChips.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import type { FilterChipItem } from '../ui/filterChips'
import { useProjectFilter } from '../ui/projectFilter'
import { t } from '../i18n'
import { acceptMergeSuggestion, dismissMergeSuggestion, listMergeSuggestions, type MergeSuggestion } from '../api/steward'
import { mergeSuggestionText } from '../utils/steward'
import { boardListOpts, captureKindLabel, captureSource, capturedListOpts, excludeCaptured, onlyCaptured } from '../utils/capture'
import { fmtAgo } from '../api/time'
import { pruneExpanded, toggleExpanded } from '../utils/cardExpand'
import { resumeConfirmText, resumeFailText } from '../utils/sessionResume'
import { pageWriteBlock } from '../utils/pageGate'
import { useWhoami } from '../store/whoami'
import { groupByWorkspace, isClosed, matchesQuery, sortCards, unsortedItems, type WorkFilter } from '../utils/work'
import {
  CAPTURE_LANE_STATUS,
  inLane,
  isWorkLane,
  laneChipKeys,
  laneClass,
  laneCounts,
  laneFromQuery,
  laneLabelKey,
  laneQuery,
  narrowByProject,
  sortByLane,
  workCounts,
  type WorkLane,
} from '../utils/workBoard'
import type { AgentSession, WorkItem, WorkStatus } from '../api/types'

const route = useRoute()
const router = useRouter()

// 写操作（整理建议 / 合并建议的采纳 · 忽略）调用的 POST /v1/work-items/{id}/suggestions/*、
// POST /v1/work-items/merge-suggestions/{n}/* 在服务端是 Public（work item 没有 owner 模型），
// 只受只读上限约束：只读身份一律灰显（gofer-bp9c，判定见 utils/pageGate.ts）。
const { whoami } = useWhoami()
const writeBlock = computed(() => pageWriteBlock(whoami.value))

const items = ref<WorkItem[]>([])
// 收集箱（status=captured）单独取；它是平铺列表里的一条泳道
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

// 状态筛选：值进 URL 的 ?status=（收集箱的旧深链 ?tab=capture 落到 captured 泳道）。
const lane = computed<WorkLane | ''>(() => laneFromQuery(route.query))
const laneChip = computed<string>({
  get: () => lane.value,
  set: (v) => setLane(isWorkLane(v) ? v : ''),
})
function setLane(next: WorkLane | ''): void {
  if (next === lane.value) return
  void router.replace({ query: laneQuery(route.query, next) as LocationQueryRaw })
}

// 到期提醒：布尔标记，与状态筛选叠加（不是状态，所以单独一组 chip）。
const flag = ref<WorkFilter>('all')
const flagChip = computed<string>({
  get: () => (flag.value === 'all' ? '' : flag.value),
  set: (v) => {
    flag.value = v === 'due' ? 'due' : 'all'
  },
})
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
// 项目收窄先做，计数 / 列表都按收窄后的结果算（chip 上的数字与列表一致）。
const inProject = computed(() => narrowByProject(items.value, project.value))
const capturedInProject = computed(() => narrowByProject(captured.value, project.value))
const summaryCounts = computed(() => workCounts(inProject.value, unsortedItems(inProject.value)))

// 平铺列表的数据源：看板条目 + 收集箱条目（一条泳道一张卡）。
const allRows = computed<WorkItem[]>(() => [...inProject.value, ...capturedInProject.value])
const counts = computed(() => laneCounts(allRows.value))
const visibleCount = computed(() => allRows.value.filter((it) => !isClosed(it.status) && it.status !== CAPTURE_LANE_STATUS).length)

// 状态 chip：全部 + 各泳道（已结束的两条只在「显示已完成」时出现），都带计数。
const laneChips = computed<FilterChipItem[]>(() => [
  { value: '', label: t('work.f.all'), count: visibleCount.value },
  ...laneChipKeys(showClosed.value).map((l) => ({ value: l, label: t(laneLabelKey(l)), count: counts.value[l] })),
])
const flagChips = computed<FilterChipItem[]>(() => [
  { value: 'due', label: t('work.f.due'), count: allRows.value.filter((it) => it.due && !isClosed(it.status)).length },
])

// 平铺列表（按状态）：先泳道顺序，泳道内按最近活动倒序。
const flatRows = computed(() =>
  sortByLane(
    allRows.value.filter(
      (it) =>
        (showClosed.value || !isClosed(it.status)) &&
        inLane(it, lane.value) &&
        (flag.value !== 'due' || it.due) &&
        matchesQuery(it, q.value),
    ),
  ),
)

// 按工作区：保留分组（同一套卡片、同样的筛选）。收集箱条目只在平铺视图里占一条泳道。
const workspaceRows = computed(() =>
  sortCards(
    inProject.value.filter(
      (it) => (showClosed.value || !isClosed(it.status)) && inLane(it, lane.value) && (flag.value !== 'due' || it.due) && matchesQuery(it, q.value),
    ),
  ),
)
const groups = computed(() => groupByWorkspace(workspaceRows.value))

const filtering = computed(() => lane.value !== '' || flag.value !== 'all' || q.value.trim() !== '')
const emptyText = computed(() =>
  allRows.value.some((it) => !isClosed(it.status)) ? t('work.triage.noMatch') : t('work.triage.empty'),
)
// 当前泳道的说明（草稿 / 收集箱这两种「还没进看板」的条目要讲清怎么处理）
const laneHint = computed(() => {
  if (lane.value === 'draft') return t('work.triage.draftsHint')
  if (lane.value === CAPTURE_LANE_STATUS) return t('work.triage.captureHint')
  return ''
})

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

function clearFilters(): void {
  flag.value = 'all'
  q.value = ''
  setLane('')
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
          {{ summaryCounts.open > 0 ? t('work.h.summary', { n: summaryCounts.open, needs: summaryCounts.needs_me }) : t('work.h.summaryEmpty') }}
        </p>
      </div>
      <div class="head-actions">
        <div class="seg" role="group" :aria-label="t('work.h.view')">
          <button type="button" class="seg-btn" :class="{ on: view === 'status' }" :aria-pressed="view === 'status'" data-test="view-status" @click="view = 'status'">{{ t('work.h.viewStatus') }}</button>
          <button type="button" class="seg-btn" :class="{ on: view === 'workspace' }" :aria-pressed="view === 'workspace'" data-test="view-workspace" @click="view = 'workspace'">{{ t('work.h.viewWorkspace') }}</button>
        </div>
        <label class="check"><input v-model="showClosed" type="checkbox" /> {{ t('work.h.showClosed') }}</label>
        <Button size="sm" :disabled="loading" data-test="refresh" @click="load()">{{ loading ? t('work.h.refreshing') : t('act.refresh') }}</Button>
        <Button variant="primary" size="sm" data-test="new-toggle" @click="createOpen = !createOpen">{{ createOpen ? t('work.c.collapse') : t('work.c.open') }}</Button>
      </div>
    </header>

    <div class="filter-row">
      <ProjectFilter :model-value="project" :options="projectOptions" :error="loadError" @update:model-value="setProject" />
      <FilterChips v-model="laneChip" :items="laneChips" :label="t('work.f.status')" />
      <FilterChips v-model="flagChip" :items="flagChips" :label="t('work.f.flags')" />
      <input v-model="q" class="search" type="search" :placeholder="t('work.f.search')" :aria-label="t('work.f.search')" data-test="search" />
      <Button v-if="filtering" variant="ghost" size="sm" data-test="clear-filters" @click="clearFilters">{{ t('work.f.clear') }}</Button>
    </div>

    <section v-if="createOpen" class="create" data-test="create-form">
      <input v-model="newTitle" class="search" type="text" :placeholder="t('work.c.title')" :aria-label="t('work.c.title')" data-test="new-title" @keydown.enter="create" />
      <input v-model="newGoal" class="search" type="text" :placeholder="t('work.c.goal')" :aria-label="t('work.c.goal')" />
      <Button variant="primary" size="sm" :disabled="creating || !newTitle.trim()" data-test="new-submit" @click="create">{{ t('work.c.submit') }}</Button>
    </section>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="actionError" class="error" data-test="action-error">{{ actionError }}</p>

    <!-- 管家的合并建议：只是建议，由人确认 -->
    <section v-if="mergeSuggestions.length" class="merge-block" data-test="merge-suggestions">
      <h2 class="block-title">{{ t('work.merge.title') }} <span class="group-count">{{ mergeSuggestions.length }}</span></h2>
      <div v-for="sg in mergeSuggestions" :key="sg.id" class="merge-row" data-test="merge-suggestion">
        <span class="merge-text">{{ mergeSuggestionText(sg, titleOf) }}</span>
        <Button variant="primary" size="sm" data-test="merge-accept" :disabled="!!writeBlock" :title="writeBlock || undefined" @click="resolveMerge(sg, true)">{{ t('work.merge.accept') }}</Button>
        <Button size="sm" data-test="merge-dismiss" :disabled="!!writeBlock" :title="writeBlock || undefined" @click="resolveMerge(sg, false)">{{ t('work.merge.dismiss') }}</Button>
      </div>
    </section>

    <p v-if="laneHint" class="lane-hint" data-test="lane-hint">{{ laneHint }}</p>

    <!-- 按状态：一条平铺列表（泳道顺序在 utils/workBoard.ts）。
         收集箱条目也在同一条列表里（同一张卡样式、同一套左边框颜色）。 -->
    <div v-if="view === 'status'" class="work-flat" data-test="status-view">
      <template v-for="it in flatRows" :key="it.id">
        <button
          v-if="(it.status as string) === CAPTURE_LANE_STATUS"
          type="button"
          class="capture-card"
          :class="[laneClass(CAPTURE_LANE_STATUS), { 'capture-card--active': openId === it.id }]"
          data-test="capture-row"
          @click="openItem(it.id)"
        >
          <span class="capture-badges">
            <span class="capture-kind" :data-kind="it.kind || 'note'" data-test="capture-kind">{{ captureKindLabel(it.kind) }}</span>
            <span class="capture-lane" data-test="capture-lane">{{ t(laneLabelKey(CAPTURE_LANE_STATUS)) }}</span>
          </span>
          <span class="capture-text">{{ it.title || it.goal }}</span>
          <span class="capture-meta">
            <span v-if="it.project_key" class="capture-proj">{{ it.project_key }}</span>
            <span v-if="captureSource(it)" class="capture-src" data-test="capture-source">{{ captureSource(it) }}</span>
            <span class="capture-time" :title="String(it.created_at)">{{ fmtAgo(it.created_at, nowSec) }}</span>
          </span>
        </button>
        <WorkCard
          v-else
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
      </template>
      <p v-if="!flatRows.length" class="empty" data-test="flat-empty">{{ emptyText }}</p>
    </div>

    <!-- 按工作区分组（同一套卡片、同样的筛选） -->
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
      <p v-if="!groups.length" class="empty" data-test="workspace-empty">{{ emptyText }}</p>
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
.work-page {
  max-width: 1480px;
  margin: 0 auto;
  /* 右下角「问管家」浮层按钮（fixed，约 40px 高 + 16px 底距）不盖住最后一张卡的操作按钮。 */
  padding-bottom: 64px;
}
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
/* 筛选：项目 chip、状态 chip、标记 chip、搜索在同一排（窄屏换行，chip 行自己横向滑动）。 */
.filter-row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); margin-bottom: var(--s3); }
.search { flex: 1 1 200px; min-width: 0; min-height: 32px; padding: 4px var(--s2); font: inherit; font-size: var(--fs-sm); color: var(--ink); background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-ctl); }
.search::placeholder { color: var(--ink-3); }
.create { display: flex; flex-wrap: wrap; gap: var(--s2); margin-bottom: var(--s3); padding: var(--s2); background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-panel); }
.error { color: var(--fail); font-size: var(--fs-sm); border: 1px solid var(--fail); border-radius: var(--r-ctl); padding: 8px 10px; margin: 0 0 var(--s3); word-break: break-word; }
.merge-block { margin-bottom: var(--s4); }
.block-title { display: flex; align-items: center; gap: var(--s2); margin: 0 0 var(--s2); font-size: var(--fs-md); font-weight: 600; color: var(--ink); }
.group-count { border: 1px solid var(--line); border-radius: 999px; padding: 0 7px; font-size: var(--fs-xs); font-weight: 400; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.merge-row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); margin-bottom: var(--s2); padding: 8px 10px; background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-panel); }
.merge-text { flex: 1 1 260px; min-width: 0; font-size: var(--fs-sm); color: var(--ink); word-break: break-word; }
.lane-hint { margin: 0 0 var(--s3); font-size: var(--fs-xs); color: var(--ink-3); }
/* 平铺：与卡片网格同一套列宽（卡片宽度一致、按行对齐），不再是每段一张网格。 */
.work-flat { display: grid; grid-template-columns: repeat(auto-fill, minmax(330px, 1fr)); gap: var(--s2); align-items: start; }
.group { margin-bottom: var(--s4); }
.group-title { display: flex; align-items: center; gap: var(--s2); margin: 0 0 var(--s2); font-size: var(--fs-md); font-weight: 600; color: var(--ink); }
.empty { grid-column: 1 / -1; border: 1px solid var(--line); border-radius: var(--r-panel); color: var(--ink-3); font-size: var(--fs-sm); padding: 28px 14px; text-align: center; }
/* 收集箱条目：与工作项卡片同一外观（InfoCard 的 .icard 外壳 + 状态族左边框）。 */
.capture-card {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  column-gap: 10px;
  row-gap: 4px;
  align-items: baseline;
  width: 100%;
  padding: 10px 12px;
  font: inherit;
  text-align: left;
  color: var(--ink);
  background: var(--surface);
  border: 1px solid var(--line);
  border-left: 4px solid var(--c, var(--line));
  border-radius: var(--r-panel);
  cursor: pointer;
}
.capture-card:hover, .capture-card--active { border-color: var(--accent); border-left-color: var(--c, var(--accent)); }
.capture-card:focus-visible { outline: 2px solid var(--focus); outline-offset: 1px; }
.capture-kind { font-size: var(--fs-xs); color: var(--accent); border: 1px solid var(--line); border-radius: 999px; padding: 0 8px; white-space: nowrap; }
/* 收集箱卡片也要看得到状态文字：左边框颜色之外再给一条文字（颜色不是唯一信息）。 */
.capture-badges { display: inline-flex; align-items: center; gap: 6px; }
.capture-lane { font-size: var(--fs-xs); color: var(--ink-3); white-space: nowrap; }
.capture-kind[data-kind='issue'] { color: var(--fail); }
.capture-kind[data-kind='request'] { color: var(--run); }
.capture-kind[data-kind='note'] { color: var(--ink-3); }
.capture-text { font-size: var(--fs-sm); line-height: 1.5; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 3; line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
.capture-meta { grid-column: 2; display: flex; flex-wrap: wrap; gap: 4px 12px; font-size: var(--fs-xs); color: var(--ink-3); }
/* 手机：单列（§4.3 冻结断点 760）；chip 行由 FilterChips 自己横向滑动。 */
@media (max-width: 760px) {
  .work-flat { grid-template-columns: minmax(0, 1fr); gap: var(--s1); }
  .head-actions { width: 100%; }
  .filter-row .search { flex: 1 1 100%; min-height: 40px; }
  .seg-btn { min-height: 40px; }
}
</style>
