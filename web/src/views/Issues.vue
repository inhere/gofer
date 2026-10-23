<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import IssueBatchBar from '../components/IssueBatchBar.vue'
import IssueDrawer from '../components/IssueDrawer.vue'
import {
  batchTrackerIssues,
  createTrackerIssue,
  commentTrackerIssue,
  getTrackerIssue,
  listTrackerIssues,
  listTrackerRepos,
  updateTrackerIssue,
} from '../api/client'
import type { TrackerBatchSet, TrackerIssue, TrackerIssueView, TrackerRepo } from '../api/types'
import { buildIssueTree, type IssueNode } from '../utils/issueTree'
import { ISSUE_STATUS_VALUES, issueStatusText } from '../utils/statusText'
import { clampPage, descendantIds, normalizePageSize, PAGE_SIZES, pageCheckState, pageWindow, paginateEntries,
  summarizeBatch, toggleId, togglePage, toggleWithDescendants, type BatchSummary } from '../utils/issuePaging'
import {
  buildNewIssueRequest, ISSUE_PRIORITIES, ISSUE_TYPES, issueRepoLabel, issueRepos,
} from '../utils/dispatch'
import { useRepoSync } from '../utils/useRepoSync'
import { pageWriteBlock } from '../utils/pageGate'
import { useWhoami } from '../store/whoami'
import { fmtTrackerTime, trackerIssueMatches } from '../utils/trackerView'
import { t, te } from '../i18n'
import Tabs from '../ui/Tabs.vue'
import FilterChips from '../ui/FilterChips.vue'
import StatusPill from '../ui/StatusPill.vue'
import TrackerRepoPath from '../ui/TrackerRepoPath.vue'
import TrackerRepoPicker from '../ui/TrackerRepoPicker.vue'
import type { FilterChipItem } from '../ui/filterChips'
import type { TabItem } from '../ui/tabs'

type IssueRow = TrackerIssue & { data: TrackerIssue['body'] }

const route = useRoute()
const router = useRouter()

// issue 的写入口（新建 / 保存 / 评论）调用的 POST /v1/tracker/repos/{id}/issues、
// PUT /v1/tracker/issues/{id}、POST /v1/tracker/issues/{id}/comments 在服务端是 Public
// （design §1：tracker issue 的读写不要权限），只受只读上限约束：只读身份一律灰显
// （gofer-bp9c，判定见 utils/pageGate.ts）。
const { whoami } = useWhoami()
const writeBlock = computed(() => pageWriteBlock(whoami.value))
const repos = ref<TrackerRepo[]>([])
const trackerId = ref('')
const issues = ref<IssueRow[]>([])
const selected = ref<TrackerIssueView | null>(null)
const selectedRow = ref<IssueRow | null>(null)
const loading = ref(false)
const error = ref('')
const query = ref('')
const typeFilter = ref('')
const tagFilter = ref('')
const statuses = ref(['open', 'in_progress', 'blocked'])
const VIEW_KEY = 'gofer.issues.view'
function loadViewMode(): 'tree' | 'flat' {
  try { return localStorage.getItem(VIEW_KEY) === 'flat' ? 'flat' : 'tree' } catch { return 'tree' }
}
const viewMode = ref<'tree' | 'flat'>(loadViewMode())
function setViewMode(mode: 'tree' | 'flat'): void {
  viewMode.value = mode
  try { localStorage.setItem(VIEW_KEY, mode) } catch { /* 无存储时只在本次生效 */ }
}
const expandOverrides = ref(new Map<string, boolean>())
function toggleExpand(id: string, current: boolean): void {
  const next = new Map(expandOverrides.value)
  next.set(id, !current)
  expandOverrides.value = next
}
const saving = ref(false)

// 页签：Issues 是本站内容；记忆已独立成 /memory（tab.to 由 Tabs 组件直接跳过去，不写 ?tab=）。
const tabItems = computed<TabItem[]>(() => [
  { value: 'issues', label: t('issues.tab.issues') },
  { value: 'memories', label: t('issues.tab.memories'), to: '/memory' },
])

// 分页（前端）：列表接口本就整仓返回（过滤/树形也依赖全量），~1000 条下切页成本可忽略。
const PAGE_SIZE_KEY = 'gofer.issues.pagesize'
function loadPageSize(): number {
  try { return normalizePageSize(localStorage.getItem(PAGE_SIZE_KEY)) } catch { return normalizePageSize(null) }
}
const pageSize = ref(loadPageSize())
const page = ref(1)
function setPageSize(value: unknown): void {
  pageSize.value = normalizePageSize(value)
  try { localStorage.setItem(PAGE_SIZE_KEY, String(pageSize.value)) } catch { /* 无存储时只在本次生效 */ }
}
const tableEl = ref<HTMLElement | null>(null)
function gotoPage(p: number): void {
  page.value = clampPage(p, pageCount.value)
  tableEl.value?.scrollIntoView?.({ block: 'start' })
}

// 多选：跨页保留（Set 只存 id）。
const selectedIds = ref(new Set<string>())
const batchRunning = ref(false)
const batchResult = ref<BatchSummary | null>(null)

// 新建 issue：在 server 镜像里建，仓库下次 gofer repo sync 时拉到本地；只列有 id 前缀的仓库。
const newOpen = ref(false)
const newBusy = ref(false)
const newError = ref('')
const newMsg = ref('')
const newForm = ref({ trackerId: '', title: '', description: '', priority: '2', type: 'task' })
const createRepos = computed(() => issueRepos(repos.value))
function openNewIssue(): void {
  newOpen.value = !newOpen.value
  newError.value = ''
  newMsg.value = ''
  const ids = createRepos.value.map((r) => r.tracker_id)
  if (!ids.includes(newForm.value.trackerId)) newForm.value.trackerId = ids.includes(trackerId.value) ? trackerId.value : (ids[0] ?? '')
}
async function submitNewIssue(): Promise<void> {
  if (newBusy.value) return
  const built = buildNewIssueRequest(newForm.value, repos.value)
  if (!built.req) { newError.value = built.error; return }
  newBusy.value = true
  newError.value = ''
  try {
    const r = await createTrackerIssue(newForm.value.trackerId, built.req)
    newMsg.value = t('issues.newForm.created', { id: r.issue?.id ?? 'issue' })
    newForm.value.title = ''
    newForm.value.description = ''
    if (trackerId.value === newForm.value.trackerId) await load()
  } catch (e) {
    newError.value = e instanceof Error ? (('detail' in e && (e as { detail?: string }).detail) || e.message) : String(e)
  } finally {
    newBusy.value = false
  }
}

const repo = computed(() => repos.value.find((item) => item.tracker_id === trackerId.value))
const filteredIssues = computed(() => issues.value.filter((row) => trackerIssueMatches(row.data, row.id, statuses.value, typeFilter.value, tagFilter.value, query.value)))
const issueNodes = computed<IssueNode[]>(() => issues.value.map((r) => ({ id: r.id, title: r.data.title, status: r.data.status, parent: r.data.parent, deps: r.data.deps })))
const issueById = computed(() => new Map(issues.value.map((r) => [r.id, r])))
const treeRows = computed(() => buildIssueTree(issueNodes.value, new Set(filteredIssues.value.map((r) => r.id)), expandOverrides.value, viewMode.value === 'flat')
  .map((e) => ({ e, row: issueById.value.get(e.id)! })))
function parentLabel(id: string): string {
  const p = issueById.value.get(id)
  return p ? `${id} ${p.data.title ?? ''}`.trim() : id
}
function openById(id: string): void {
  const row = issueById.value.get(id)
  if (row) void openIssue(row)
}
const pages = computed(() => paginateEntries(treeRows.value, pageSize.value, (x) => x.e.depth))
const pageCount = computed(() => Math.max(1, pages.value.length))
const currentPage = computed(() => clampPage(page.value, pageCount.value))
const pageRows = computed(() => pages.value[currentPage.value - 1] ?? [])
const pageIds = computed(() => pageRows.value.map((r) => r.row.id))
const headState = computed(() => pageCheckState(selectedIds.value, pageIds.value))
const visibleIds = computed(() => new Set(filteredIssues.value.map((r) => r.id)))
const selectedList = computed(() => [...selectedIds.value])
// 筛选/视图/每页条数变化 => 回到第 1 页
watch([query, typeFilter, tagFilter, statuses, viewMode, trackerId, pageSize], () => { page.value = 1 })

// 状态 chip：值即接口状态值，带计数；多选，空集合表示不按状态过滤。
const statusItems = computed<FilterChipItem[]>(() => {
  const counts = new Map<string, number>()
  for (const row of issues.value) {
    const status = row.data.status || 'open'
    counts.set(status, (counts.get(status) ?? 0) + 1)
  }
  return ISSUE_STATUS_VALUES.map((status) => ({
    value: status,
    label: te(`status.${status}`) ? t(`status.${status}`) : issueStatusText(status),
    count: counts.get(status) ?? 0,
  }))
})

function onCheck(id: string, ev: MouseEvent): void {
  if (ev.shiftKey) selectFamily(id)
  else selectedIds.value = toggleId(selectedIds.value, id)
}
function selectFamily(id: string): void {
  selectedIds.value = toggleWithDescendants(selectedIds.value, id, descendantIds(id, issueNodes.value, visibleIds.value))
}
function clearSelection(): void { selectedIds.value = new Set() }
async function runBatch(set: TrackerBatchSet): Promise<void> {
  if (batchRunning.value || selectedIds.value.size === 0) return
  batchRunning.value = true
  error.value = ''
  try {
    const resp = await batchTrackerIssues(trackerId.value, selectedList.value, set)
    batchResult.value = summarizeBatch(resp.results ?? [])
  } catch (e) {
    batchResult.value = null
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    batchRunning.value = false
  }
  clearSelection()
  await load()
}

async function loadRepos(): Promise<void> {
  const result = await listTrackerRepos()
  repos.value = result.repos ?? []
  const recent = [...repos.value].sort((a, b) => b.last_sync_at - a.last_sync_at)[0]
  if (!trackerId.value || !repos.value.some((item) => item.tracker_id === trackerId.value)) {
    trackerId.value = recent?.tracker_id ?? ''
  }
}

async function load(): Promise<void> {
  if (!trackerId.value) return
  loading.value = true
  error.value = ''
  try {
    const issueResult = await listTrackerIssues(trackerId.value)
    issues.value = (issueResult.issues ?? []).map((item) => ({ ...item, data: item.body }))
    const present = new Set(issues.value.map((r) => r.id))
    if ([...selectedIds.value].some((id) => !present.has(id))) selectedIds.value = new Set([...selectedIds.value].filter((id) => present.has(id)))
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// TRK-05：服务端派发 `gofer repo sync`，轮询其 job，结束后刷新仓库与列表（接线在 utils/useRepoSync）。
const { syncing, syncRepo: runSync } = useRepoSync()
const syncMsg = ref<{ ok: boolean; text: string; jobId: string } | null>(null)
async function syncRepo(): Promise<void> {
  if (!trackerId.value) return
  syncMsg.value = null
  const out = await runSync(trackerId.value)
  if (!out) return
  if (out.ok) {
    await loadRepos()
    await load()
    syncMsg.value = { ok: true, text: t('issues.syncDone', { time: fmtTrackerTime(repo.value?.last_sync_at) }), jobId: out.jobId }
  } else {
    syncMsg.value = { ok: false, text: t('issues.syncFailed', { err: out.message }), jobId: out.jobId }
  }
}

async function changeRepo(): Promise<void> {
  syncMsg.value = null
  clearSelection()
  batchResult.value = null
  selected.value = null
  await load()
}

// repo 选择：仓库视为项目，但下拉要按「项目 / 目录」区分（P6，issue gofer-hez1）。
function pickRepo(value: string): void {
  if (!value || value === trackerId.value) return
  trackerId.value = value
  void changeRepo()
}

async function openIssue(row: IssueRow): Promise<void> {
  selectedRow.value = row
  selected.value = await getTrackerIssue(trackerId.value, row.id)
  void router.replace({ query: { ...route.query, issue: row.id } })
}

function closeDrawer(): void {
  selected.value = null
  selectedRow.value = null
  const query = { ...route.query }
  delete query.issue
  void router.replace({ query })
}

async function saveIssue(value: { title: string; status: string; priority: number; description: string }): Promise<void> {
  if (!selectedRow.value || saving.value) return
  saving.value = true
  try {
    await updateTrackerIssue(trackerId.value, selectedRow.value.id, { ...value, expected_rev: selectedRow.value.rev })
    await load()
    const refreshed = issues.value.find((item) => item.id === selectedRow.value?.id)
    if (refreshed) await openIssue(refreshed)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

async function addComment(text: string): Promise<void> {
  if (!selectedRow.value) return
  try {
    await commentTrackerIssue(trackerId.value, selectedRow.value.id, text)
    await openIssue(selectedRow.value)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(async () => {
  try {
    const requestedTracker = typeof route.query.tracker === 'string' ? route.query.tracker : ''
    if (requestedTracker) trackerId.value = requestedTracker
    await loadRepos()
    await load()
    const issueId = String(route.query.issue || '')
    const row = issues.value.find((item) => item.id === issueId)
    if (row) await openIssue(row)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})
</script>

<template>
  <main class="page" data-test="issues-page">
    <header class="page-head">
      <div class="head-main">
        <h1 class="title">{{ t('issues.title') }}</h1>
        <p class="sub">
          {{ t('issues.subtitle', { n: filteredIssues.length }) }}
        </p>
      </div>
      <div class="head-actions">
        <button class="btn" type="button" data-test="new-issue-toggle" :aria-expanded="newOpen" @click="openNewIssue">
          {{ t('issues.new') }}
        </button>
        <button
          class="btn btn--primary"
          type="button"
          :disabled="loading || !trackerId"
          @click="load"
        >
          {{ t('issues.refresh') }}
        </button>
      </div>
    </header>

    <form v-if="newOpen" class="new-issue" data-test="new-issue-form" @submit.prevent="submitNewIssue">
      <p class="new-title">{{ t('issues.newForm.title') }}</p>
      <label class="field"><span>{{ t('issues.newForm.repo') }}</span>
        <select v-model="newForm.trackerId" class="in" data-test="new-issue-repo"><option value="">{{ t('issues.newForm.repoPlaceholder') }}</option><option v-for="r in createRepos" :key="r.tracker_id" :value="r.tracker_id">{{ issueRepoLabel(r) }}</option></select>
      </label>
      <p v-if="!createRepos.length" class="hint-line" data-test="new-issue-no-repo">{{ t('issues.newForm.noRepo') }}</p>
      <label class="field"><span>{{ t('issues.newForm.titleField') }}</span><input v-model="newForm.title" class="in" type="text" maxlength="300" data-test="new-issue-title" /></label>
      <label class="field"><span>{{ t('issues.newForm.desc') }}</span><textarea v-model="newForm.description" class="in" rows="4" data-test="new-issue-desc"></textarea></label>
      <div class="field-row">
        <label class="field"><span>{{ t('issues.newForm.priority') }}</span><select v-model="newForm.priority" class="in" data-test="new-issue-priority"><option v-for="p in ISSUE_PRIORITIES" :key="p.value" :value="String(p.value)">{{ p.label }}</option></select></label>
        <label class="field"><span>{{ t('issues.newForm.type') }}</span><select v-model="newForm.type" class="in" data-test="new-issue-type"><option v-for="tp in ISSUE_TYPES" :key="tp" :value="tp">{{ tp }}</option></select></label>
      </div>
      <p v-if="newError" class="err" role="alert" data-test="new-issue-error">{{ newError }}</p>
      <p v-if="newMsg" class="ok" role="status" data-test="new-issue-ok">{{ newMsg }}</p>
      <div class="actions">
        <button class="btn btn--primary" type="submit" :disabled="newBusy || !!writeBlock" :title="writeBlock || undefined" data-test="new-issue-submit">{{ newBusy ? t('issues.newForm.submitting') : t('issues.newForm.submit') }}</button>
        <button class="btn" type="button" @click="newOpen = false">{{ t('issues.newForm.collapse') }}</button>
      </div>
    </form>

    <section class="meta-panel">
      <div class="repo-row">
        <TrackerRepoPicker :model-value="trackerId" :repos="repos" @update:model-value="pickRepo" />
        <TrackerRepoPath :repo="repo" :placeholder="t('issues.repoPlaceholder')" />
        <button class="btn" type="button" data-test="sync-repo" :disabled="syncing || !trackerId" :title="t('issues.repoSyncTitle')" @click="syncRepo">{{ syncing ? t('issues.repoSyncing') : t('issues.repoSync') }}</button>
      </div>
      <div class="sync-summary">
        <template v-if="repo">
          <span>{{ t('issues.lastSync', { time: fmtTrackerTime(repo.last_sync_at) }) }}</span>
          <span :title="repo.sync_summary || ''">{{ repo.sync_summary || t('issues.noSyncSummary') }}</span>
          <span v-if="syncMsg" data-test="sync-msg" :class="syncMsg.ok ? 'ok' : 'err'" :title="syncMsg.text">{{ syncMsg.text }}<router-link v-if="syncMsg.jobId" :to="`/jobs/${syncMsg.jobId}`"> · {{ t('issues.syncJob', { id: syncMsg.jobId.slice(0, 8) }) }}</router-link></span>
        </template>
      </div>
    </section>

    <!-- 「记忆」页签已独立成 /memory（tabItems 里的 to），点它直接跳过去。 -->
    <Tabs :tabs="tabItems" />

    <section v-if="repos.length === 0" class="empty-panel">
      <span>{{ t('issues.empty.noRepos') }}</span>
      <code>gofer repo init</code>
      <code>gofer repo sync</code>
    </section>

    <template v-else>
      <section class="filter-panel">
        <div class="seg" role="group" :aria-label="t('issues.view.label')">
          <button type="button" class="seg-btn" :class="{ 'seg-btn--on': viewMode === 'tree' }" :aria-pressed="viewMode === 'tree'" data-test="view-tree" @click="setViewMode('tree')">{{ t('issues.view.tree') }}</button>
          <button type="button" class="seg-btn" :class="{ 'seg-btn--on': viewMode === 'flat' }" :aria-pressed="viewMode === 'flat'" data-test="view-flat" @click="setViewMode('flat')">{{ t('issues.view.flat') }}</button>
        </div>
        <FilterChips v-model="statuses" multiple :items="statusItems" :label="t('issues.col.status')" />
        <label class="field field--inline">{{ t('issues.filter.type') }}<input v-model="typeFilter" class="in" :placeholder="t('issues.filter.typePlaceholder')" /></label>
        <label class="field field--inline">{{ t('issues.filter.tag') }}<input v-model="tagFilter" class="in" :placeholder="t('issues.filter.tagPlaceholder')" /></label>
        <label class="field field--inline query-field">{{ t('issues.filter.query') }}<input v-model="query" class="in" :placeholder="t('issues.filter.queryIssues')" /></label>
      </section>
      <p class="hint-line" data-test="select-hint">{{ t('issues.selectHint') }}</p>
      <IssueBatchBar v-if="selectedIds.size > 0" :count="selectedIds.size" :sample-ids="selectedList.slice(0, 8)" :running="batchRunning" @clear="clearSelection" @run="runBatch" />
      <div v-if="batchResult" class="batch-result" :class="{ bad: batchResult.failed > 0 }" data-test="batch-result">
        <div class="batch-result-head"><strong>{{ batchResult.text }}</strong><button type="button" class="link-btn" @click="batchResult = null">{{ t('issues.batch.dismiss') }}</button></div>
        <ul v-if="batchResult.failures.length" class="batch-failures"><li v-for="f in batchResult.failures" :key="f.id"><code>{{ f.id }}</code> {{ f.error }}</li></ul>
      </div>
      <p v-if="error" class="err panel-err">{{ error }}</p>
      <p v-else-if="loading" class="dim">{{ t('issues.loading') }}</p>
      <p v-if="!loading" class="page-summary" data-test="page-summary">
        {{ t('issues.summary', { n: filteredIssues.length }) }}<template v-if="viewMode === 'tree'">{{ t('issues.summaryTree') }}</template> · {{ t('issues.summaryPage', { page: currentPage, pages: pageCount }) }}
      </p>
      <section v-if="!loading" ref="tableEl" class="table" role="table">
        <div class="thead" role="row">
          <span class="check-col"><input type="checkbox" :aria-label="t('issues.table.checkPage')" data-test="check-page" :checked="headState === 'all'" :indeterminate="headState === 'some'" :disabled="pageIds.length === 0" @change="selectedIds = togglePage(selectedIds, pageIds)" /></span>
          <span>{{ t('issues.col.id') }}</span><span>{{ t('issues.col.title') }}</span><span>{{ t('issues.col.status') }}</span><span>{{ t('issues.col.priority') }}</span><span>{{ t('issues.col.type') }}</span><span>{{ t('issues.col.tags') }}</span><span class="updated-col">{{ t('issues.col.updated') }}</span>
        </div>
        <div v-for="{ e, row } in pageRows" :key="row.id" class="trow" :class="{ picked: selectedIds.has(row.id) }" role="button" tabindex="0" data-test="issue-row" :data-depth="e.depth" @click="openIssue(row)" @keydown.enter.self="openIssue(row)">
          <span class="check-col" @click.stop><input type="checkbox" :aria-label="t('issues.table.checkRow', { id: row.id })" data-test="check-row" :checked="selectedIds.has(row.id)" @click.stop="onCheck(row.id, $event)" /></span>
          <span class="idcell mono" :style="{ paddingLeft: `${e.depth * 16}px` }"><button v-if="viewMode === 'tree' && e.visibleChildren > 0" type="button" class="caret" :class="{ open: e.expanded }" :aria-label="e.expanded ? t('issues.caret.collapse') : t('issues.caret.expand')" data-test="caret" @click.stop="toggleExpand(e.id, e.expanded)"></button><span v-else-if="viewMode === 'tree'" class="caret-gap"></span>{{ row.id }}</span>
          <strong class="titlecell"><span>{{ row.data.title || '—' }}</span><small v-if="e.childTotal > 0" class="progress" data-test="progress">{{ t('issues.progress', { closed: e.childClosed, total: e.childTotal }) }}</small><button v-if="viewMode === 'tree' && e.childTotal > 0" type="button" class="family-btn" data-test="family" :title="t('issues.familyTitle')" @click.stop="selectFamily(row.id)">{{ t('issues.family') }}</button><small v-if="e.orphanParent" class="orphan" data-test="orphan"><a href="#" @click.stop.prevent="openById(e.orphanParent!)">{{ t('issues.orphan', { label: parentLabel(e.orphanParent) }) }}</a></small></strong>
          <span><StatusPill :status="(row.data.status || 'open') as any" /></span>
          <span class="mono">{{ row.data.priority ?? 0 }}</span>
          <span>{{ row.data.type || '—' }}</span>
          <span class="dim">{{ (row.data.tags ?? []).join(' · ') || '—' }}</span>
          <span class="mono updated-col">{{ fmtTrackerTime(row.data.updated_at || row.updated_at) }}</span>
        </div>
        <div v-if="filteredIssues.length === 0" class="empty-line">{{ t('issues.table.empty') }}</div>
      </section>
      <nav v-if="!loading && filteredIssues.length > 0" class="pager" data-test="pager" :aria-label="t('issues.pager.label')">
        <label class="pager-size">{{ t('issues.pager.perPage') }}<select :value="pageSize" class="in" data-test="page-size" @change="setPageSize(($event.target as HTMLSelectElement).value)"><option v-for="n in PAGE_SIZES" :key="n" :value="n">{{ n }}</option></select></label>
        <button type="button" class="page-btn" :disabled="currentPage <= 1" data-test="page-prev" @click="gotoPage(currentPage - 1)">{{ t('issues.pager.prev') }}</button>
        <template v-for="(p, i) in pageWindow(currentPage, pageCount)" :key="i"><span v-if="p === null" class="pager-gap">…</span><button v-else type="button" class="page-btn" :class="{ 'page-btn--on': p === currentPage }" :data-test="`page-${p}`" :aria-label="t('issues.pager.page', { page: p })" @click="gotoPage(p)">{{ p }}</button></template>
        <button type="button" class="page-btn" :disabled="currentPage >= pageCount" data-test="page-next" @click="gotoPage(currentPage + 1)">{{ t('issues.pager.next') }}</button>
      </nav>
    </template>
    <IssueDrawer v-if="selected" :issue="selected" :all-issues="issueNodes" @open-issue="openById" :saving="saving" :write-block="writeBlock" :expected-rev="selectedRow?.rev ?? 0" @close="closeDrawer" @save="saveIssue" @comment="addComment" />
  </main>
</template>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--s3);
  margin-bottom: var(--s3);
}
.title {
  margin: 0;
  font-size: var(--fs-lg);
  color: var(--ink);
}
.sub {
  margin: 2px 0 0;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.head-actions {
  display: flex;
  gap: var(--s2);
  align-items: center;
}
.btn {
  display: inline-flex;
  align-items: center;
  min-height: 32px;
  padding: 0 var(--s3);
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  white-space: nowrap;
}
.btn:hover {
  border-color: var(--accent);
}
.btn--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.btn--primary:hover {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.in {
  min-height: 32px;
  padding: 4px var(--s2);
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  min-width: 0;
}
.in:focus-visible {
  border-color: var(--accent);
}
.new-issue {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  max-width: 640px;
  margin-bottom: var(--s3);
  padding: var(--s3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-left: 3px solid var(--accent);
  border-radius: var(--r-panel);
}
.new-title {
  margin: 0;
  font-size: var(--fs-sm);
  font-weight: 600;
  color: var(--ink);
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  min-width: 0;
  flex: 1;
}
.field--inline {
  flex-direction: row;
  align-items: center;
  gap: var(--s1);
  flex: 0 1 200px;
}
.field-row {
  display: flex;
  gap: var(--s2);
  flex-wrap: wrap;
}
.hint-line {
  margin: 0 0 var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
.err {
  color: var(--fail);
  font-size: var(--fs-sm);
  margin: 0;
  overflow-wrap: anywhere;
}
.ok {
  color: var(--done);
  font-size: var(--fs-sm);
  margin: 0;
  overflow-wrap: anywhere;
}
.panel-err {
  border: 1px solid var(--fail);
  border-radius: var(--r-ctl);
  padding: var(--s2);
  margin: 0 0 var(--s3);
}
.actions {
  display: flex;
  gap: var(--s2);
}
.meta-panel {
  display: flex;
  align-items: flex-start;
  gap: var(--s4);
  margin-bottom: var(--s3);
  min-height: 62px;
  padding: var(--s3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  min-width: 0;
}
.repo-row {
  display: flex;
  align-items: center;
  gap: var(--s2);
  min-width: 0;
  flex-wrap: wrap;
  flex: 1;
}
.sync-summary {
  display: flex;
  flex-direction: column;
  gap: 2px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  min-width: 0;
  flex: 1;
  align-self: flex-end;
}
.sync-summary > span {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.seg {
  display: inline-flex;
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  overflow: hidden;
}
.seg-btn {
  min-height: 30px;
  padding: 0 var(--s2);
  background: transparent;
  color: var(--ink-3);
  border: 0;
  border-right: 1px solid var(--line);
  font: inherit;
  font-size: var(--fs-sm);
}
.seg-btn:last-child {
  border-right: 0;
}
.seg-btn--on {
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 600;
}
.filter-panel {
  display: flex;
  align-items: center;
  gap: var(--s3);
  flex-wrap: wrap;
  margin: var(--s3) 0 var(--s2);
  min-width: 0;
}
.query-field {
  flex: 1 1 220px;
}
.table {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  overflow: hidden;
  background: var(--surface);
}
.thead,
.trow {
  display: grid;
  grid-template-columns: 28px minmax(120px, 160px) minmax(180px, 1.4fr) 120px 70px 90px minmax(100px, 1fr) 140px;
  gap: var(--s2);
  align-items: center;
  padding: 9px var(--s3);
}
.thead {
  background: var(--surface-2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
  letter-spacing: 0.06em;
  text-transform: uppercase;
}
.trow {
  border-top: 1px solid var(--line);
  color: var(--ink);
  font-size: var(--fs-sm);
  cursor: pointer;
  outline: none;
}
.trow:hover {
  background: var(--surface-2);
}
.trow:focus-visible {
  box-shadow: inset 2px 0 0 var(--accent);
}
.trow.picked {
  background: var(--accent-soft);
}
.dim {
  color: var(--ink-3);
}
.empty-line {
  color: var(--ink-3);
  text-align: center;
  padding: var(--s6) var(--s3);
  font-size: var(--fs-sm);
}
.empty-panel {
  display: flex;
  justify-content: center;
  align-items: center;
  gap: var(--s2);
  min-height: 150px;
  padding: var(--s4);
  color: var(--ink-3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  font-size: var(--fs-sm);
}
.empty-panel code {
  color: var(--accent);
  background: var(--sunken);
  border: 1px solid var(--line);
  padding: 4px var(--s2);
  border-radius: var(--r-chip);
}
.check-col {
  display: flex;
  align-items: center;
  justify-content: center;
}
.check-col input {
  width: 16px;
  height: 16px;
  accent-color: var(--accent);
  cursor: pointer;
}
.caret {
  background: transparent;
  border: 0;
  width: 18px;
  height: 18px;
  padding: 0;
  flex: none;
  position: relative;
}
.caret::before {
  content: '';
  position: absolute;
  left: 6px;
  top: 4px;
  border-style: solid;
  border-width: 5px 0 5px 7px;
  border-color: transparent transparent transparent var(--ink-3);
}
.caret.open::before {
  left: 4px;
  top: 6px;
  border-width: 7px 5px 0 5px;
  border-color: var(--ink-3) transparent transparent transparent;
}
.caret-gap {
  display: inline-block;
  width: 18px;
  flex: none;
}
.idcell {
  display: flex;
  align-items: center;
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--accent);
}
.titlecell {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  overflow-wrap: anywhere;
}
.progress,
.orphan {
  color: var(--ink-3);
  font-weight: 400;
  font-size: var(--fs-xs);
}
.orphan a {
  color: var(--accent);
}
.family-btn {
  align-self: flex-start;
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--accent);
  font: inherit;
  font-size: var(--fs-xs);
  padding: 1px 6px;
}
.page-summary {
  margin: 0 0 var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.batch-result {
  border: 1px solid var(--done);
  border-radius: var(--r-panel);
  padding: var(--s2) var(--s3);
  margin-bottom: var(--s3);
  font-size: var(--fs-sm);
  color: var(--ink);
}
.batch-result.bad {
  border-color: var(--fail);
}
.batch-result-head {
  display: flex;
  justify-content: space-between;
  gap: var(--s2);
  align-items: center;
}
.link-btn {
  background: none;
  border: 0;
  color: var(--ink-3);
  font: inherit;
  font-size: var(--fs-sm);
  text-decoration: underline;
}
.batch-failures {
  margin: var(--s1) 0 0;
  padding-left: 18px;
  color: var(--fail);
  overflow-wrap: anywhere;
}
.pager {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s1);
  margin-top: var(--s3);
  font-size: var(--fs-sm);
}
.pager-size {
  display: flex;
  align-items: center;
  gap: var(--s1);
  color: var(--ink-3);
  margin-right: var(--s2);
  font-size: var(--fs-xs);
}
.page-btn {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: transparent;
  color: var(--ink);
  padding: 3px 9px;
  font: inherit;
  font-size: var(--fs-xs);
  min-width: 30px;
  min-height: 30px;
}
.page-btn--on {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.page-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.pager-gap {
  color: var(--ink-3);
}

/* 手机（≤760）：面板与筛选纵向排列，表格每行压成卡片，只留 id / 标题 / 状态。 */
@media (max-width: 760px) {
  .page-head {
    flex-direction: column;
    gap: var(--s2);
  }
  .btn,
  .page-btn,
  .seg-btn {
    min-height: 40px;
  }
  .meta-panel {
    flex-direction: column;
    align-items: stretch;
    min-height: 0;
  }
  .sync-summary {
    align-self: stretch;
  }
  .thead {
    display: none;
  }
  .trow {
    display: flex;
    flex-wrap: wrap;
    gap: 4px var(--s2);
    padding: var(--s2) var(--s3);
  }
  .trow > .check-col {
    order: 0;
    flex: none;
  }
  .trow > .idcell {
    order: 1;
    flex: 1 0 100px;
    font-size: var(--fs-xs);
  }
  .trow > .titlecell {
    order: 3;
    flex: 1 0 100%;
  }
  .trow > span:nth-child(4) {
    order: 2;
    margin-left: auto;
  }
  .trow > span:nth-child(n + 5) {
    display: none;
  }
  .field--inline {
    flex: 1 0 100%;
  }
}
</style>
