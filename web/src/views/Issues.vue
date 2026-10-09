<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter, type LocationQueryRaw } from 'vue-router'
import IssueBatchBar from '../components/IssueBatchBar.vue'
import IssueDrawer from '../components/IssueDrawer.vue'
import MemoryList from '../components/MemoryList.vue'
import StatusBadge from '../components/StatusBadge.vue'
import {
  batchTrackerIssues,
  commentTrackerIssue,
  deleteTrackerMemory,
  getTrackerIssue,
  listTrackerIssues,
  listTrackerMemories,
  listTrackerRepos,
  syncTrackerRepo,
  getJob,
  logsTail,
  listProjects,
  listScopedMemories,
  updateTrackerIssue,
  updateTrackerMemory,
  updateScopedMemory,
  deleteScopedMemory,
} from '../api/client'
import type { MemoryDoctorFinding, TrackerBatchSet, TrackerIssue, TrackerIssueView, TrackerMemory, TrackerRepo } from '../api/types'
import { buildIssueTree, type IssueNode } from '../utils/issueTree'
import {
  clampPage, descendantIds, normalizePageSize, PAGE_SIZES, pageCheckState, pageWindow, paginateEntries,
  summarizeBatch, toggleId, togglePage, toggleWithDescendants, type BatchSummary,
} from '../utils/issuePaging'
import { runTrackerSync } from '../utils/trackerSync'
import { fmtTrackerTime, MEMORY_KIND_LABEL, MEMORY_KINDS, memoryKindMatches, trackerIssueMatches, trackerMemoryMatches, trackerRepoLabel } from '../utils/trackerView'

type IssueRow = TrackerIssue & { data: TrackerIssue['body'] }
type MemoryRow = TrackerMemory & { data: TrackerMemory['body'] }

const route = useRoute()
const router = useRouter()
const repos = ref<TrackerRepo[]>([])
const trackerId = ref('')
const memoryScope = ref<'repo' | 'project' | 'global'>('repo')
const projectKeys = ref<string[]>([])
const projectKey = ref('')
const tab = ref<'issues' | 'memories'>('issues')
const issues = ref<IssueRow[]>([])
const memories = ref<MemoryRow[]>([])
// P4：服务端 doctor 标记（仅仓库记忆）与记忆筛选
const memoryDoctor = ref<Record<string, MemoryDoctorFinding[]>>({})
const memoryKinds = ref<string[]>([])
const flaggedOnly = ref(false)
function toggleMemoryKind(kind: string): void {
  memoryKinds.value = memoryKinds.value.includes(kind) ? memoryKinds.value.filter((k) => k !== kind) : [...memoryKinds.value, kind]
}
const selected = ref<TrackerIssueView | null>(null)
const selectedRow = ref<IssueRow | null>(null)
const selectedMemory = ref<MemoryRow | null>(null)
const loading = ref(false)
const error = ref('')
const query = ref('')
const typeFilter = ref('')
const tagFilter = ref('')
const statuses = ref(['open', 'in_progress', 'blocked'])
const memoryDraft = ref('')
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

function queryValue(value: unknown): string {
  return Array.isArray(value) ? String(value[0] ?? '') : String(value ?? '')
}

function updateMemoryQuery(): void {
  const query: LocationQueryRaw = { ...route.query, memory_scope: memoryScope.value }
  if (memoryScope.value === 'project' && projectKey.value) query.memory_project = projectKey.value
  else delete query.memory_project
  void router.replace({ query })
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
const filteredMemories = computed(() =>
  memories.value.filter(
    (row) =>
      trackerMemoryMatches(row.data, row.id, query.value) &&
      memoryKindMatches(row.data, memoryKinds.value, flaggedOnly.value, (memoryDoctor.value[row.data.key || row.id]?.length ?? 0) > 0),
  ),
)

async function loadRepos(): Promise<void> {
  const [result, projects] = await Promise.all([listTrackerRepos(), listProjects()])
  repos.value = result.repos ?? []
  projectKeys.value = projects.projects ?? []
  if (!projectKeys.value.includes(projectKey.value)) projectKey.value = projectKeys.value[0] ?? ''
  const recent = [...repos.value].sort((a, b) => b.last_sync_at - a.last_sync_at)[0]
  if (!trackerId.value || !repos.value.some((item) => item.tracker_id === trackerId.value)) {
    trackerId.value = recent?.tracker_id ?? ''
  }
}

async function load(): Promise<void> {
  if (tab.value === 'issues' && !trackerId.value) return
  if (tab.value === 'memories' && memoryScope.value === 'repo' && !trackerId.value) return
  if (tab.value === 'memories' && memoryScope.value === 'project' && !projectKey.value) return
  loading.value = true
  error.value = ''
  try {
    const issueResult = trackerId.value ? await listTrackerIssues(trackerId.value) : { issues: [] }
    const memoryResult = memoryScope.value === 'repo'
      ? (trackerId.value ? await listTrackerMemories(trackerId.value) : { memories: [] })
      : await listScopedMemories(memoryScope.value, memoryScope.value === 'project' ? projectKey.value : '', query.value)
    issues.value = (issueResult.issues ?? []).map((item) => ({ ...item, data: item.body }))
    const present = new Set(issues.value.map((r) => r.id))
    if ([...selectedIds.value].some((id) => !present.has(id))) selectedIds.value = new Set([...selectedIds.value].filter((id) => present.has(id)))
    memoryDoctor.value = ('doctor' in memoryResult ? memoryResult.doctor : undefined) ?? {}
    memories.value = (memoryResult.memories ?? []).filter((item) => !item.deleted).map((item) => {
      if ('body' in item) return { ...item, data: item.body }
      const body = { key: item.key, content: item.content, tags: item.tags, updated_at: item.updated_at, by: item.updated_by, kind: item.kind, summary: item.summary }
      return { id: item.key, rev: 1, updated_at: item.updated_at, body, data: body }
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// TRK-05：服务端派发 `gofer repo sync`，轮询其 job，结束后刷新仓库与列表。
const syncing = ref(false)
const syncMsg = ref<{ ok: boolean; text: string; jobId: string } | null>(null)
async function syncRepo(): Promise<void> {
  if (syncing.value || !trackerId.value) return
  const id = trackerId.value
  syncing.value = true
  syncMsg.value = null
  try {
    const out = await runTrackerSync({
      start: () => syncTrackerRepo(id),
      getJob: (jid) => getJob(jid),
      stderrTail: (jid) => logsTail(jid, 'stderr', 4096),
      sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
    })
    if (out.state === 'ok') {
      await loadRepos()
      await load()
      syncMsg.value = { ok: true, text: `同步完成 · ${fmtTrackerTime(repo.value?.last_sync_at)}`, jobId: out.jobId }
    } else {
      syncMsg.value = { ok: false, text: `同步失败：${out.message}`, jobId: out.jobId }
    }
  } catch (e) {
    syncMsg.value = { ok: false, text: `同步失败：${e instanceof Error ? e.message : String(e)}`, jobId: '' }
  } finally {
    syncing.value = false
  }
}

async function changeRepo(): Promise<void> {
  syncMsg.value = null
  clearSelection()
  batchResult.value = null
  selected.value = null
  selectedMemory.value = null
  await load()
}

async function changeMemoryScope(): Promise<void> {
  selectedMemory.value = null
  updateMemoryQuery()
  await load()
}

async function changeMemoryProject(): Promise<void> {
  selectedMemory.value = null
  updateMemoryQuery()
  await load()
}

function selectMemoryScope(value: string): void {
  if (value === 'repo' || value === 'project' || value === 'global') {
    memoryScope.value = value
    void changeMemoryScope()
  }
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

function openMemory(row: MemoryRow): void {
  selectedMemory.value = selectedMemory.value?.id === row.id ? null : row
  memoryDraft.value = String(row.data.content ?? '')
}

async function saveMemory(): Promise<void> {
  if (!selectedMemory.value) return
  try {
    if (memoryScope.value === 'repo') await updateTrackerMemory(trackerId.value, selectedMemory.value.id, { content: memoryDraft.value, expected_rev: selectedMemory.value.rev })
    else await updateScopedMemory(memoryScope.value, memoryScope.value === 'project' ? projectKey.value : '', selectedMemory.value.id, { content: memoryDraft.value, tags: selectedMemory.value.data.tags ?? [] })
    selectedMemory.value = null
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function removeMemory(row: MemoryRow): Promise<void> {
  try {
    if (memoryScope.value === 'repo') await deleteTrackerMemory(trackerId.value, row.id)
    else await deleteScopedMemory(memoryScope.value, memoryScope.value === 'project' ? projectKey.value : '', row.id)
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

function toggleStatus(status: string): void {
  statuses.value = statuses.value.includes(status)
    ? statuses.value.filter((item) => item !== status)
    : [...statuses.value, status]
}

onMounted(async () => {
  try {
    const requestedScope = queryValue(route.query.memory_scope)
    if (requestedScope === 'repo' || requestedScope === 'project' || requestedScope === 'global') memoryScope.value = requestedScope
    projectKey.value = queryValue(route.query.memory_project)
    // 「今天」记忆整理卡的标题链接：?tab=memories&tracker=<id>&memory=<key>
    if (queryValue(route.query.tab) === 'memories') tab.value = 'memories'
    const requestedTracker = queryValue(route.query.tracker)
    if (requestedTracker) trackerId.value = requestedTracker
    await loadRepos()
    updateMemoryQuery()
    await load()
    const memoryKey = queryValue(route.query.memory)
    const memoryRow = memoryKey ? memories.value.find((item) => (item.data.key || item.id) === memoryKey) : undefined
    if (memoryRow) openMemory(memoryRow)
    const issueId = String(route.query.issue || '')
    const row = issues.value.find((item) => item.id === issueId)
    if (row) await openIssue(row)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})
</script>

<template>
  <main class="tracker-page">
    <header class="page-head">
      <div><p class="eyebrow mono">TRACKER</p><h1 class="title mono">Issues</h1></div>
      <button class="primary-btn mono" type="button" :disabled="loading || (tab === 'issues' ? !trackerId : (memoryScope === 'repo' ? !trackerId : memoryScope === 'project' && !projectKey))" @click="load">刷新</button>
    </header>
    <!-- The top panel has the SAME structure on both tabs (one select slot + one sync slot, each with
         a reserved height) so switching Issues / Memories never moves it or the tabs below it. The
         memory scope switch lives under the tabs, where growing the page only pushes content down. -->
    <section class="meta-panel">
      <label v-if="tab === 'issues' || memoryScope === 'repo'" class="repo-field mono"><span>仓库</span><select v-model="trackerId" class="filter-select" @change="changeRepo"><option value="">请选择已登记仓库</option><option v-for="item in repos" :key="item.tracker_id" :value="item.tracker_id">{{ trackerRepoLabel(item) }}</option></select><button v-if="tab === 'issues' || memoryScope === 'repo'" class="secondary-btn mono" type="button" data-test="sync-repo" :disabled="syncing || !trackerId" title="让 server 在该仓库目录执行 gofer repo sync" @click="syncRepo">{{ syncing ? '同步中…' : '同步' }}</button></label>
      <label v-else-if="memoryScope === 'project'" class="repo-field mono"><span>项目</span><select v-model="projectKey" class="filter-select" @change="changeMemoryProject"><option value="">请选择项目</option><option v-for="item in projectKeys" :key="item" :value="item">{{ item }}</option></select></label>
      <div v-else class="repo-field mono"><span>仓库</span><div class="repo-note">全局记忆存于 server，无需选择仓库</div></div>
      <div class="sync-summary mono"><template v-if="repo && (tab === 'issues' || memoryScope === 'repo')"><span>上次同步 {{ fmtTrackerTime(repo.last_sync_at) }}</span><span :title="repo.sync_summary || ''">{{ repo.sync_summary || '暂无同步冲突摘要' }}</span><span v-if="syncMsg" data-test="sync-msg" :class="syncMsg.ok ? 'sync-ok' : 'sync-fail'" :title="syncMsg.text">{{ syncMsg.text }}<router-link v-if="syncMsg.jobId" :to="`/jobs/${syncMsg.jobId}`"> · job {{ syncMsg.jobId.slice(0, 8) }}</router-link></span></template></div>
    </section>
    <!-- The tabs stay visible without any registered repository: global and project
         memories live on the server and do not need one. -->
    <nav class="tabs mono"><button type="button" :class="{ active: tab === 'issues' }" @click="tab = 'issues'">Issues</button><button type="button" :class="{ active: tab === 'memories' }" @click="tab = 'memories'">Memories</button></nav>
    <div v-if="tab === 'memories'" class="scope-row mono">
      <span>记忆作用域</span>
      <div class="scope-switch" role="radiogroup" aria-label="记忆作用域">
        <button v-for="item in [{ value: 'repo', label: '仓库' }, { value: 'project', label: '项目' }, { value: 'global', label: '全局' }]" :key="item.value" type="button" class="scope-btn" :class="{ active: memoryScope === item.value }" :aria-checked="memoryScope === item.value" role="radio" @click="selectMemoryScope(item.value)">{{ item.label }}</button>
      </div>
      <small class="scope-help">{{ memoryScope === 'repo' ? '随代码存于该仓库 .gofer/tracker' : memoryScope === 'project' ? '存于 server，整个 gofer 项目共享' : '存于 server，所有项目共享' }}</small>
    </div>
    <section v-if="repos.length === 0 && (tab === 'issues' || memoryScope === 'repo')" class="empty-panel mono"><span>暂无已登记仓库</span><code>gofer repo init</code><code>gofer repo sync</code></section>
    <template v-else>
      <section class="filter-panel">
        <div v-if="tab === 'issues'" class="view-switch mono" role="radiogroup" aria-label="列表视图"><button type="button" class="scope-btn" :class="{ active: viewMode === 'tree' }" data-test="view-tree" @click="setViewMode('tree')">树形</button><button type="button" class="scope-btn" :class="{ active: viewMode === 'flat' }" data-test="view-flat" @click="setViewMode('flat')">平铺</button></div>
        <div v-if="tab === 'issues'" class="chips mono"><button v-for="status in ['open', 'in_progress', 'blocked', 'closed']" :key="status" type="button" class="chip" :class="[`chip--${status}`, { selected: statuses.includes(status) }]" @click="toggleStatus(status)">{{ status }}</button></div>
        <label v-if="tab === 'issues'" class="filter-field">类型<input v-model="typeFilter" class="filter-input mono" placeholder="全部" /></label>
        <label v-if="tab === 'issues'" class="filter-field">标签<input v-model="tagFilter" class="filter-input mono" placeholder="标签" /></label>
        <div v-if="tab === 'memories'" class="chips mono" data-test="memory-kind-filter"><button v-for="kind in MEMORY_KINDS" :key="kind" type="button" class="chip" :class="{ selected: memoryKinds.length === 0 || memoryKinds.includes(kind) }" :aria-pressed="memoryKinds.includes(kind)" :data-kind="kind" @click="toggleMemoryKind(kind)">{{ MEMORY_KIND_LABEL[kind] }}</button><button v-if="memoryScope === 'repo'" type="button" class="chip" :class="{ selected: flaggedOnly }" :aria-pressed="flaggedOnly" data-test="memory-flagged" title="只看服务端 doctor 标记的记忆" @click="flaggedOnly = !flaggedOnly">⚠ 有标记</button></div>
        <label class="filter-field query-field">关键字<input v-model="query" class="filter-input mono" :placeholder="tab === 'memories' ? 'key 或内容' : 'ID、标题或内容'" /></label>
      </section>
      <p v-if="tab === 'issues'" class="select-hint mono" data-test="select-hint">勾选父项不会自动勾选子项；按住 Shift 点击复选框，或点父项上的「连同子项」，可一并选中其子项。选中会跨页保留。</p>
      <IssueBatchBar v-if="tab === 'issues' && selectedIds.size > 0" :count="selectedIds.size" :sample-ids="selectedList.slice(0, 8)" :running="batchRunning" @clear="clearSelection" @run="runBatch" />
      <div v-if="tab === 'issues' && batchResult" class="batch-result mono" :class="{ bad: batchResult.failed > 0 }" data-test="batch-result">
        <div class="batch-result-head"><strong>{{ batchResult.text }}</strong><button type="button" class="batch-dismiss" @click="batchResult = null">收起</button></div>
        <ul v-if="batchResult.failures.length" class="batch-failures"><li v-for="f in batchResult.failures" :key="f.id"><code>{{ f.id }}</code> {{ f.error }}</li></ul>
      </div>
      <p v-if="error" class="error-panel mono">{{ error }}</p><p v-else-if="loading" class="loading mono">加载中…</p>
      <p v-if="tab === 'issues' && !loading" class="page-summary mono" data-test="page-summary">共 {{ filteredIssues.length }} 条<template v-if="viewMode === 'tree'">（本页按「根」分组，父子同页）</template> · 第 {{ currentPage }}/{{ pageCount }} 页</p>
      <section v-if="tab === 'issues' && !loading" ref="tableEl" class="table"><div class="thead mono"><span class="check-col"><input type="checkbox" aria-label="全选本页" data-test="check-page" :checked="headState === 'all'" :indeterminate="headState === 'some'" :disabled="pageIds.length === 0" @change="selectedIds = togglePage(selectedIds, pageIds)" /></span><span>ID</span><span>标题</span><span>状态</span><span>优先级</span><span>类型</span><span>标签</span><span class="updated-col">更新</span></div><div v-for="{ e, row } in pageRows" :key="row.id" class="trow" :class="{ picked: selectedIds.has(row.id) }" role="button" tabindex="0" data-test="issue-row" :data-depth="e.depth" @click="openIssue(row)" @keydown.enter.self="openIssue(row)"><span class="check-col" @click.stop><input type="checkbox" :aria-label="`选择 ${row.id}`" data-test="check-row" :checked="selectedIds.has(row.id)" @click.stop="onCheck(row.id, $event)" /></span><span class="mono idcell" :style="{ paddingLeft: `${e.depth * 16}px` }"><button v-if="viewMode === 'tree' && e.visibleChildren > 0" type="button" class="caret" :class="{ open: e.expanded }" :aria-label="e.expanded ? '折叠' : '展开'" data-test="caret" @click.stop="toggleExpand(e.id, e.expanded)"></button><span v-else-if="viewMode === 'tree'" class="caret-gap"></span>{{ row.id }}</span><strong class="titlecell"><span>{{ row.data.title || '—' }}</span><small v-if="e.childTotal > 0" class="progress mono" data-test="progress">{{ e.childClosed }}/{{ e.childTotal }} 已关闭</small><button v-if="viewMode === 'tree' && e.childTotal > 0" type="button" class="family-btn mono" data-test="family" title="选中该项及其全部子项（当前筛选可见的）" @click.stop="selectFamily(row.id)">连同子项</button><small v-if="e.orphanParent" class="orphan mono" data-test="orphan"><a href="#" @click.stop.prevent="openById(e.orphanParent!)">父：{{ parentLabel(e.orphanParent) }}</a></small></strong><span><StatusBadge :status="(row.data.status || 'open') as any" /></span><span>{{ row.data.priority ?? 0 }}</span><span>{{ row.data.type || '—' }}</span><span class="muted">{{ (row.data.tags ?? []).join(' · ') || '—' }}</span><span class="mono updated-col">{{ fmtTrackerTime(row.data.updated_at || row.updated_at) }}</span></div><div v-if="filteredIssues.length === 0" class="table-empty mono">暂无匹配 issue</div></section>
      <nav v-if="tab === 'issues' && !loading && filteredIssues.length > 0" class="pager mono" data-test="pager" aria-label="分页">
        <label class="pager-size">每页<select :value="pageSize" class="filter-select" data-test="page-size" @change="setPageSize(($event.target as HTMLSelectElement).value)"><option v-for="n in PAGE_SIZES" :key="n" :value="n">{{ n }}</option></select></label>
        <button type="button" class="pager-btn" :disabled="currentPage <= 1" data-test="page-prev" @click="gotoPage(currentPage - 1)">上一页</button>
        <template v-for="(p, i) in pageWindow(currentPage, pageCount)" :key="i"><span v-if="p === null" class="pager-gap">…</span><button v-else type="button" class="pager-btn" :class="{ active: p === currentPage }" :data-test="`page-${p}`" @click="gotoPage(p)">{{ p }}</button></template>
        <button type="button" class="pager-btn" :disabled="currentPage >= pageCount" data-test="page-next" @click="gotoPage(currentPage + 1)">下一页</button>
      </nav>
      <MemoryList v-if="tab === 'memories' && !loading" :doctor="memoryScope === 'repo' ? memoryDoctor : {}" :rows="filteredMemories" :selected="selectedMemory" :draft="memoryDraft" @open="openMemory" @update:draft="memoryDraft = $event" @save="saveMemory" @remove="removeMemory" />
    </template>
    <IssueDrawer v-if="selected" :issue="selected" :all-issues="issueNodes" @open-issue="openById" :saving="saving" :expected-rev="selectedRow?.rev ?? 0" @close="closeDrawer" @save="saveIssue" @comment="addComment" />
  </main>
</template>

<style scoped>
.tracker-page{max-width:1240px;margin:0 auto}.page-head{display:flex;justify-content:space-between;align-items:center;margin-bottom:14px}.eyebrow{color:var(--queue);font-size:10px;letter-spacing:.18em;margin:0}.title{color:var(--paper);font-size:18px;margin:0}.meta-panel,.filter-panel,.empty-panel{border:1px solid var(--line);border-radius:var(--radius);background:var(--panel);padding:12px 14px}.meta-panel{display:flex;align-items:flex-start;gap:18px;margin-bottom:14px;min-height:78px}.repo-field,.filter-field{display:flex;flex-direction:column;gap:5px;color:var(--queue);font-size:11px}.repo-field{min-width:360px}.repo-note{display:flex;align-items:center;min-height:30px;color:var(--queue);font-size:12px}.scope-row{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin-bottom:12px;color:var(--queue);font-size:11px}.scope-switch{display:flex;gap:0}.scope-btn{border:1px solid var(--line);border-right:0;padding:6px 14px;background:transparent;color:var(--queue);font:inherit}.scope-btn:first-child{border-radius:var(--radius) 0 0 var(--radius)}.scope-btn:last-child{border-right:1px solid var(--line);border-radius:0 var(--radius) var(--radius) 0}.scope-btn.active{background:var(--phosphor);border-color:var(--phosphor);color:var(--ink);font-weight:600}.scope-help{color:var(--queue);opacity:.8}.sync-summary{display:flex;flex-direction:column;gap:4px;color:var(--queue);font-size:11px;min-width:0;flex:1;align-self:flex-end;min-height:34px;justify-content:flex-end}.sync-ok{color:var(--done)}.sync-fail{color:var(--fail)}.sync-summary>span{white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.filter-select,.filter-input{background:var(--panel);color:var(--paper);border:1px solid var(--line);border-radius:var(--radius);padding:6px 8px;font-size:12px}.filter-select:focus,.filter-input:focus{border-color:var(--phosphor);outline:none}.primary-btn,.secondary-btn{border-radius:var(--radius);padding:5px 10px;font-size:12px}.primary-btn{background:var(--phosphor);color:var(--ink);border:1px solid var(--phosphor);font-weight:600}.secondary-btn{background:transparent;color:var(--phosphor);border:1px solid var(--line)}.tabs{display:flex;gap:18px;border-bottom:1px solid var(--line);margin-bottom:12px}.tabs button{color:var(--queue);background:transparent;border:0;border-bottom:2px solid transparent;padding:7px 3px}.tabs button.active{color:var(--phosphor);border-bottom-color:var(--phosphor)}.filter-panel{display:flex;align-items:flex-end;gap:12px;flex-wrap:wrap;margin-bottom:12px}.query-field{flex:1 1 220px}.chips{display:flex;gap:6px;flex-wrap:wrap}.chip{border:1px solid var(--line);border-radius:999px;padding:5px 9px;background:transparent;color:var(--queue);font:inherit;opacity:.55}.chip.selected{opacity:1}.chip--open.selected{color:var(--done);border-color:var(--done)}.chip--in_progress.selected{color:var(--run);border-color:var(--run)}.chip--blocked.selected{color:var(--fail);border-color:var(--fail)}.table{border:1px solid var(--line);border-radius:var(--radius);overflow:hidden}.thead,.trow{display:grid;grid-template-columns:28px 170px minmax(180px,1.4fr) 120px 70px 100px minmax(120px,1fr) 150px;gap:10px;align-items:center;padding:9px 12px}.thead{background:var(--panel);color:var(--queue);font-size:11px;letter-spacing:.06em}.trow{width:100%;text-align:left;background:transparent;border:0;border-top:1px solid var(--line);color:var(--paper);font-size:12px}.trow:hover{background:var(--panel)}.muted,.loading,.table-empty{color:var(--queue)}.table-empty{text-align:center;padding:28px}.empty-panel{display:flex;justify-content:center;align-items:center;gap:10px;min-height:150px;color:var(--queue)}.empty-panel code{color:var(--phosphor);background:var(--term-bg);border:1px solid var(--line);padding:5px 8px;border-radius:var(--radius)}.error-panel{color:var(--fail);border:1px solid var(--fail);padding:8px}
@media (max-width:639px){.meta-panel{flex-direction:column;align-items:stretch;min-height:0}.repo-field{min-width:0}.sync-summary{flex:none;align-self:stretch}.thead,.trow{grid-template-columns:24px minmax(70px,.8fr) minmax(0,1.6fr) 84px;gap:6px;padding:9px 8px}.thead>span:nth-child(n+5),.trow>span:nth-child(n+5){display:none}.table{max-width:100%}}
.view-switch{display:flex}.caret{background:transparent;border:0;width:18px;height:18px;padding:0;cursor:pointer;flex:none;position:relative}.caret::before{content:'';position:absolute;left:6px;top:4px;border-style:solid;border-width:5px 0 5px 7px;border-color:transparent transparent transparent var(--phosphor)}.caret.open::before{left:4px;top:6px;border-width:7px 5px 0 5px;border-color:var(--phosphor) transparent transparent transparent}.caret-gap{display:inline-block;width:18px;flex:none}.idcell{display:flex;align-items:center;min-width:0;overflow-wrap:anywhere}.titlecell{display:flex;flex-direction:column;gap:2px;min-width:0;overflow-wrap:anywhere}.progress,.orphan{color:var(--queue);font-weight:400}.orphan a{color:var(--phosphor)}
@media (max-width:639px){.tracker-page{overflow-x:hidden}.idcell{font-size:11px}}
.check-col{display:flex;align-items:center;justify-content:center}.check-col input{width:16px;height:16px;accent-color:var(--phosphor);cursor:pointer}.trow.picked{background:color-mix(in srgb,var(--phosphor) 10%,transparent)}
.select-hint,.page-summary{color:var(--queue);font-size:11px;margin:0 0 8px;overflow-wrap:anywhere}
.family-btn{align-self:flex-start;background:transparent;border:1px solid var(--line);border-radius:var(--radius);color:var(--phosphor);font-size:10px;padding:1px 6px;cursor:pointer}
.batch-result{border:1px solid var(--done);border-radius:var(--radius);padding:8px 12px;margin-bottom:10px;font-size:12px;color:var(--paper)}.batch-result.bad{border-color:var(--fail)}.batch-result-head{display:flex;justify-content:space-between;gap:8px;align-items:center}.batch-dismiss{background:transparent;border:0;color:var(--queue);text-decoration:underline;font:inherit;cursor:pointer}.batch-failures{margin:6px 0 0;padding-left:18px;color:var(--fail);overflow-wrap:anywhere}
.pager{display:flex;align-items:center;flex-wrap:wrap;gap:6px;margin-top:12px;font-size:12px}.pager-size{display:flex;align-items:center;gap:6px;color:var(--queue);margin-right:8px}.pager-btn{border:1px solid var(--line);border-radius:var(--radius);background:transparent;color:var(--paper);padding:4px 9px;font:inherit;cursor:pointer;min-width:30px}.pager-btn.active{background:var(--phosphor);border-color:var(--phosphor);color:var(--ink);font-weight:600}.pager-btn:disabled{opacity:.4;cursor:not-allowed}.pager-gap{color:var(--queue)}
</style>
