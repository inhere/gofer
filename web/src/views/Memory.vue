<script setup lang="ts">
// 「记忆」页（UI 重设计 P6，issue gofer-hez1）：原 Issues 页的记忆页签独立成 /memory。
// 第二版（issue gofer-icqt）：「全部」范围把仓库 / 项目 / 全局三路并发取回再合并（每条带来源），
// 列表分页（Issues 页同一套控件，页码写进 URL），详情改成右侧抽屉（components/MemoryDrawer.vue）。
// 取数与筛选在 utils/memories.ts，列表行仍是 components/MemoryList.vue。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import MemoryList from '../components/MemoryList.vue'
import MemoryDrawer from '../components/MemoryDrawer.vue'
import FilterChips from '../ui/FilterChips.vue'
import TrackerRepoPath from '../ui/TrackerRepoPath.vue'
import TrackerRepoPicker from '../ui/TrackerRepoPicker.vue'
import {
  deleteScopedMemory,
  deleteTrackerMemory,
  flagScopedMemory,
  listProjects,
  listScopedMemories,
  listTrackerMemories,
  listTrackerRepos,
  listTrackerSyncClients,
  updateScopedMemory,
  updateTrackerMemory,
} from '../api/client'
import type { MemoryDoctorFinding, TrackerRepo, TrackerSyncClient } from '../api/types'
import {
  filterMemories,
  MEMORY_PAGE_SIZE_KEY,
  memoryPageSlice,
  memoryPageCount,
  mergeMemoryBatches,
  mergeMemoryDoctor,
  memorySourceKey,
  memoryUpdateBody,
  pageQueryUpdate,
  readMemoryKey,
  readMemoryPage,
  readMemoryProject,
  readMemoryScope,
  readMemorySearch,
  searchQueryUpdate,
  memoryQueryUpdate,
  memoryRowKey,
  memoryRowsOf,
  type MemoryBatch,
  type MemoryEdit,
  type MemoryScope,
  type MemorySource,
  type SourcedMemoryRow,
} from '../utils/memories'
import { clampPage, normalizePageSize, PAGE_SIZES } from '../utils/issuePaging'
import { useRepoSync } from '../utils/useRepoSync'
import { fmtTrackerTime, MEMORY_KIND_LABEL, MEMORY_KINDS, syncClientLine, trackerRepoTitle } from '../utils/trackerView'
import { t } from '../i18n'
import SearchSelect from '../ui/SearchSelect.vue'
import type { SearchOption } from '../ui/searchSelect'
import type { FilterChipItem } from '../ui/filterChips'

const route = useRoute()
const router = useRouter()

const repos = ref<TrackerRepo[]>([])
const projectKeys = ref<string[]>([])
// 可搜索的项目下拉（issue gofer-b8nh）：首项是「未选项目」，与原来的空 option 等价。
const projectItems = computed<SearchOption[]>(() => [
  { value: '', label: t('memory.projectPlaceholder') },
  ...projectKeys.value.map((key) => ({ value: key, label: key })),
])
const trackerId = ref(typeof route.query.tracker === 'string' ? route.query.tracker : '')
const scope = ref<MemoryScope>(readMemoryScope(route.query))
const projectKey = ref(readMemoryProject(route.query))
const memories = ref<SourcedMemoryRow[]>([])
const doctor = ref<Record<string, MemoryDoctorFinding[]>>({})
const kinds = ref<string[]>([])
const flaggedOnly = ref(false)
const search = ref(readMemorySearch(route.query))
const selected = ref<SourcedMemoryRow | null>(null)
const flagged = ref(false)
const saving = ref(false)
const drawerError = ref('')
const loading = ref(false)
const error = ref('')

// 仓库记忆要有仓库、项目记忆要有项目；全局与「全部」直接可读。
const canLoad = computed(() => {
  if (scope.value === 'repo') return !!trackerId.value
  if (scope.value === 'project') return !!projectKey.value
  return true
})
const filtered = computed(() => filterMemories(memories.value, search.value, kinds.value, flaggedOnly.value, doctor.value))
const repo = computed(() => repos.value.find((item) => item.tracker_id === trackerId.value))

// 分页（前端切页：接口本就整仓 / 整项目返回，筛选与 doctor 也要全量）。
const pageSize = ref(loadPageSize())
const page = ref(readMemoryPage(route.query))
const pageCount = computed(() => memoryPageCount(filtered.value.length, pageSize.value))
const currentPage = computed(() => clampPage(page.value, pageCount.value))
const pageRows = computed(() => memoryPageSlice(filtered.value, pageSize.value, currentPage.value))
const listEl = ref<HTMLElement | null>(null)

function loadPageSize(): number {
  try {
    return normalizePageSize(localStorage.getItem(MEMORY_PAGE_SIZE_KEY))
  } catch {
    return normalizePageSize(null)
  }
}
function setPageSize(value: unknown): void {
  pageSize.value = normalizePageSize(value)
  page.value = 1
  try {
    localStorage.setItem(MEMORY_PAGE_SIZE_KEY, String(pageSize.value))
  } catch {
    /* 无存储时只在本次生效 */
  }
}
function gotoPage(p: number): void {
  page.value = clampPage(p, pageCount.value)
  writeQuery(pageQueryUpdate(route.query, page.value))
  listEl.value?.scrollIntoView?.({ block: 'start' })
}

const scopeItems = computed<FilterChipItem[]>(() => [
  { value: 'all', label: t('memory.scope.all') },
  { value: 'repo', label: t('memory.scope.repo') },
  { value: 'project', label: t('memory.scope.project') },
  { value: 'global', label: t('memory.scope.global') },
])
const scopeHelp = computed(() => t(`memory.scope.${scope.value}Help`))
const kindItems = computed<FilterChipItem[]>(() => MEMORY_KINDS.map((kind) => ({ value: kind, label: MEMORY_KIND_LABEL[kind] })))

// 同步状态：作用域 / 项目 / 仓库 / 关键字 / 页码都写进 URL（可分享、后退可复原）。
function writeQuery(next: Record<string, string>): void {
  void router.replace({ query: { ...next } })
}

function pickScope(value: string): void {
  if (value !== 'repo' && value !== 'project' && value !== 'global' && value !== 'all') return
  if (value === scope.value) return
  scope.value = value
  selected.value = null
  page.value = 1
  writeQuery(pageQueryUpdate(memoryQueryUpdate(route.query, scope.value, projectKey.value), 1))
  void load()
}

function pickRepo(value: string): void {
  if (!value || value === trackerId.value) return
  trackerId.value = value
  selected.value = null
  page.value = 1
  writeQuery(pageQueryUpdate({ ...memoryQueryUpdate(route.query, scope.value, projectKey.value), tracker: value }, 1))
  void load()
}

function changeProject(): void {
  selected.value = null
  page.value = 1
  writeQuery(pageQueryUpdate(memoryQueryUpdate(route.query, scope.value, projectKey.value), 1))
  void load()
}

function clearSearch(): void {
  search.value = ''
}

watch(search, (value) => {
  page.value = 1
  writeQuery(pageQueryUpdate(searchQueryUpdate(route.query, value), 1))
})
// 类型 chip / 「有标记」/ 每页条数只影响本页的切页，回到第 1 页。
watch([kinds, flaggedOnly, pageSize], () => {
  page.value = 1
})
// 后退 / 前进改 URL 时页码跟着走。
watch(
  () => route.query.page,
  (value) => {
    page.value = readMemoryPage({ page: value })
  },
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

function repoSource(item: TrackerRepo | undefined): MemorySource {
  return { scope: 'repo', trackerId: item?.tracker_id ?? trackerId.value, label: item ? trackerRepoTitle(item) : trackerId.value }
}

// 当前作用域对应的那一份来源（单作用域取数也带上，抽屉保存 / 删除就走同一条路径）。
function currentSource(): MemorySource {
  if (scope.value === 'repo') return repoSource(repo.value)
  if (scope.value === 'project') return { scope: 'project', project: projectKey.value }
  return { scope: 'global' }
}

// 「全部」范围：全局一份 + 每个项目一份 + 每个仓库一份，并发取回后合并（同一个失败不影响其余）。
async function loadAll(): Promise<void> {
  const tasks: Array<Promise<MemoryBatch>> = [
    listScopedMemories('global', '', '').then((result): MemoryBatch => ({ source: { scope: 'global' }, result })),
  ]
  for (const key of projectKeys.value) {
    tasks.push(listScopedMemories('project', key, '').then((result): MemoryBatch => ({ source: { scope: 'project', project: key }, result })))
  }
  for (const item of repos.value) {
    tasks.push(listTrackerMemories(item.tracker_id).then((result): MemoryBatch => ({ source: repoSource(item), result })))
  }
  const settled = await Promise.allSettled(tasks)
  const batches: MemoryBatch[] = []
  const failures: string[] = []
  for (const result of settled) {
    if (result.status === 'fulfilled') batches.push(result.value)
    else failures.push(result.reason instanceof Error ? result.reason.message : String(result.reason))
  }
  memories.value = mergeMemoryBatches(batches)
  doctor.value = mergeMemoryDoctor(batches)
  error.value = failures.length > 0 ? t('memory.allError', { n: failures.length, err: failures[0] }) : ''
}

async function load(): Promise<void> {
  if (!canLoad.value) {
    memories.value = []
    doctor.value = {}
    return
  }
  loading.value = true
  error.value = ''
  try {
    if (scope.value === 'all') {
      await loadAll()
    } else {
      const result = scope.value === 'repo'
        ? await listTrackerMemories(trackerId.value)
        : await listScopedMemories(scope.value, scope.value === 'project' ? projectKey.value : '', search.value)
      const source = currentSource()
      memories.value = memoryRowsOf(result).map((row) => ({ ...row, source }))
      doctor.value = mergeMemoryDoctor([{ source, result }])
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    memories.value = []
    doctor.value = {}
  } finally {
    loading.value = false
  }
  if (scope.value === 'global') void loadSyncClients()
}

// 全局记忆的「同步状态」：每台机器最近一次 `gofer memory sync --global` 的记录。
// 单独请求、单独报错：拿不到同步状态不影响记忆列表本身。
const GLOBAL_TRACKER_ID = 'global'
const syncClients = ref<TrackerSyncClient[]>([])
const syncClientsError = ref('')
const syncClientsLoaded = ref(false)
async function loadSyncClients(): Promise<void> {
  try {
    const out = await listTrackerSyncClients(GLOBAL_TRACKER_ID)
    syncClients.value = out.clients ?? []
    syncClientsError.value = ''
  } catch (e) {
    syncClientsError.value = e instanceof Error ? e.message : String(e)
  } finally {
    syncClientsLoaded.value = true
  }
}

// 仓库作用域的「同步」：派发 gofer repo sync，结束后刷新仓库与列表（失败也把 job 报出来）。
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
    syncMsg.value = { ok: true, text: t('memory.syncDone', { time: fmtTrackerTime(repo.value?.last_sync_at) }), jobId: out.jobId }
  } else {
    syncMsg.value = { ok: false, text: t('memory.syncFailed', { err: out.message }), jobId: out.jobId }
  }
}

function openMemory(row: SourcedMemoryRow): void {
  selected.value = row
  flagged.value = false
  drawerError.value = ''
}

function closeDrawer(): void {
  selected.value = null
  drawerError.value = ''
}

// 保存 / 删除 / 标记后重取列表；标记后按来源 + key 重新选中那一条（抽屉不关）。
function reselect(key: string): void {
  selected.value = memories.value.find((row) => memorySourceKey(row) === key) ?? null
}

async function saveMemory(edit: MemoryEdit): Promise<void> {
  const row = selected.value
  const source = row?.source
  if (!row || !source) return
  saving.value = true
  drawerError.value = ''
  try {
    const body = memoryUpdateBody(edit)
    if (source.scope === 'repo') {
      await updateTrackerMemory(source.trackerId, row.id, { ...body, expected_rev: row.rev })
    } else {
      await updateScopedMemory(source.scope, source.scope === 'project' ? source.project : '', row.data.key || row.id, body)
    }
    closeDrawer()
    await load()
  } catch (e) {
    drawerError.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

async function removeMemory(): Promise<void> {
  const row = selected.value
  const source = row?.source
  if (!row || !source) return
  saving.value = true
  drawerError.value = ''
  try {
    if (source.scope === 'repo') await deleteTrackerMemory(source.trackerId, row.id)
    else await deleteScopedMemory(source.scope, source.scope === 'project' ? source.project : '', row.data.key || row.id)
    closeDrawer()
    await load()
  } catch (e) {
    drawerError.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

// 标记失效：只有 server 存的 scoped 记忆有接口（仓库记忆没有），理由必填。
async function flagMemory(reason: string): Promise<void> {
  const row = selected.value
  const source = row?.source
  if (!row || !source || source.scope === 'repo') return
  const key = memorySourceKey(row)
  saving.value = true
  drawerError.value = ''
  try {
    await flagScopedMemory(source.scope, source.scope === 'project' ? source.project : '', row.data.key || row.id, reason)
    flagged.value = true
    await load()
    reselect(key)
  } catch (e) {
    drawerError.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

onMounted(async () => {
  try {
    await loadRepos()
    await load()
    const key = readMemoryKey(route.query)
    const row = key ? memories.value.find((item) => memoryRowKey(item) === key) : undefined
    if (row) openMemory(row)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})
</script>

<template>
  <main class="page" data-test="memory-page">
    <header class="page-head">
      <div class="head-main">
        <h1 class="title">{{ t('memory.title') }}</h1>
        <p class="sub">{{ t('memory.subtitle', { n: filtered.length }) }} · {{ t('memory.pageSummary', { page: currentPage, pages: pageCount }) }}</p>
      </div>
      <div class="head-actions">
        <button class="btn btn--primary" type="button" :disabled="loading || !canLoad" data-test="memory-refresh" @click="load">
          {{ t('memory.refresh') }}
        </button>
      </div>
    </header>

    <!-- 作用域：全部（三路合并） / 仓库（随代码） / 项目（server） / 全局（server，所有项目共享）。 -->
    <section class="scope-row">
      <span class="field-label">{{ t('memory.scope.label') }}</span>
      <FilterChips :model-value="scope" :items="scopeItems" :label="t('memory.scope.label')" @update:model-value="pickScope(String($event))" />
      <small class="scope-help">{{ scopeHelp }}</small>
    </section>

    <section v-if="scope !== 'all'" class="meta-panel">
      <div v-if="scope === 'repo'" class="repo-row">
        <TrackerRepoPicker :model-value="trackerId" :repos="repos" @update:model-value="pickRepo" />
        <TrackerRepoPath :repo="repo" :placeholder="t('memory.repoPlaceholder')" />
        <button class="btn" type="button" data-test="sync-repo" :disabled="syncing || !trackerId" :title="t('memory.repoSyncTitle')" @click="syncRepo">
          {{ syncing ? t('memory.repoSyncing') : t('memory.repoSync') }}
        </button>
      </div>
      <div v-else-if="scope === 'project'" class="repo-row">
        <span class="field-label">{{ t('memory.project') }}</span>
        <SearchSelect
          class="mono"
          test-id="memory-project"
          variant="chip"
          :model-value="projectKey"
          :options="projectItems"
          :label="t('memory.project')"
          :placeholder="t('memory.projectPlaceholder')"
          @update:model-value="projectKey = $event"
          @select="changeProject()"
        />
      </div>
      <div v-else-if="scope === 'global'" class="repo-row">
        <span class="field-label">{{ t('memory.repo') }}</span>
        <span class="dim">{{ t('memory.globalNote') }}</span>
      </div>
      <div class="sync-summary">
        <template v-if="repo && scope === 'repo'">
          <span>{{ t('memory.lastSync', { time: fmtTrackerTime(repo.last_sync_at) }) }}</span>
          <span :title="repo.sync_summary || ''">{{ repo.sync_summary || t('memory.noSyncSummary') }}</span>
          <span v-if="syncMsg" class="sync-msg" :class="syncMsg.ok ? 'ok' : 'err'" :title="syncMsg.text">
            {{ syncMsg.text }}<router-link v-if="syncMsg.jobId" :to="`/jobs/${syncMsg.jobId}`"> · {{ t('memory.syncJob', { id: syncMsg.jobId.slice(0, 8) }) }}</router-link>
          </span>
        </template>
      </div>
    </section>

    <!-- 全局记忆：各机器本地副本最近一次与 server 同步的情况（可折叠，默认收起）。 -->
    <details v-if="scope === 'global'" class="sync-clients" data-test="sync-clients">
      <summary>{{ t('memory.syncClients.title') }}<template v-if="syncClients.length"> · {{ t('memory.syncClients.summary', { n: syncClients.length, host: syncClients[0].client_host }) }}</template></summary>
      <p v-if="syncClientsError" class="err">{{ syncClientsError }}</p>
      <p v-else-if="syncClientsLoaded && syncClients.length === 0" class="sync-clients-empty">{{ t('memory.syncClients.empty') }}</p>
      <ul v-else class="sync-clients-list">
        <li v-for="c in syncClients" :key="`${c.client_host}|${c.caller}`" :title="t('memory.syncClients.lastSync', { time: fmtTrackerTime(c.last_sync_at) })">{{ syncClientLine(c) }}</li>
      </ul>
    </details>

    <section v-if="scope === 'repo' && repos.length === 0" class="empty-panel" data-test="memory-no-repos">
      <span>{{ t('memory.empty.noRepos') }}</span>
      <code>gofer repo init</code>
      <code>gofer repo sync</code>
    </section>
    <p v-else-if="scope === 'project' && !projectKey" class="empty-line" data-test="memory-no-project">{{ t('memory.empty.noProject') }}</p>

    <template v-else>
      <section class="filter-panel">
        <div class="kind-chips" data-test="memory-kind-filter">
          <FilterChips v-model="kinds" multiple :items="kindItems" :label="t('memory.filter.type')" />
          <button v-if="scope === 'repo' || scope === 'all'" type="button" class="chip" :class="{ 'chip--on': flaggedOnly }" :aria-pressed="flaggedOnly" data-test="memory-flagged" :title="t('memory.filter.flaggedTitle')" @click="flaggedOnly = !flaggedOnly">{{ t('memory.filter.flagged') }}</button>
        </div>
        <label class="field field--inline query-field">
          {{ t('memory.filter.keyword') }}
          <input v-model="search" class="in" type="search" data-test="memory-search" :placeholder="t('memory.filter.keywordPlaceholder')" @keydown.esc="clearSearch" />
        </label>
      </section>
      <p v-if="error" class="err panel-err" data-test="memory-error">{{ error }}</p>
      <p v-else-if="loading" class="dim">{{ t('memory.loading') }}</p>
      <template v-else>
        <div ref="listEl">
          <MemoryList :doctor="doctor" :rows="pageRows" :show-source="scope === 'all'" :active-key="selected ? memorySourceKey(selected) : ''" @open="openMemory" />
        </div>
        <nav v-if="filtered.length > 0" class="pager" data-test="memory-pager" :aria-label="t('memory.pager.label')">
          <label class="pager-size">{{ t('memory.pager.perPage') }}<select :value="pageSize" class="in" data-test="memory-page-size" @change="setPageSize(($event.target as HTMLSelectElement).value)"><option v-for="n in PAGE_SIZES" :key="n" :value="n">{{ n }}</option></select></label>
          <button type="button" class="page-btn" :disabled="currentPage <= 1" data-test="memory-page-prev" @click="gotoPage(currentPage - 1)">{{ t('memory.pager.prev') }}</button>
          <span class="pager-gap">·</span>
          <button type="button" class="page-btn page-btn--on" :data-test="`memory-page-${currentPage}`" :aria-label="t('memory.pager.page', { page: currentPage })">{{ currentPage }}</button>
          <span class="pager-gap">/ {{ pageCount }}</span>
          <button type="button" class="page-btn" :disabled="currentPage >= pageCount" data-test="memory-page-next" @click="gotoPage(currentPage + 1)">{{ t('memory.pager.next') }}</button>
        </nav>
      </template>
    </template>

    <MemoryDrawer
      v-if="selected"
      :row="selected"
      :doctor="doctor[memorySourceKey(selected)] ?? []"
      :saving="saving"
      :flagged="flagged"
      :error="drawerError"
      @close="closeDrawer"
      @save="saveMemory"
      @remove="removeMemory"
      @flag="flagMemory"
    />
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
  flex: 0 1 220px;
}
.field-label {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  white-space: nowrap;
}
.scope-row {
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
  margin: 0 0 var(--s2);
  font-size: var(--fs-xs);
}
.scope-help {
  color: var(--ink-3);
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
.sync-msg.ok {
  color: var(--done);
}
.sync-msg.err {
  color: var(--fail);
}
.sync-clients {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  background: var(--surface);
  padding: var(--s2) var(--s3);
  margin-bottom: var(--s3);
  font-size: var(--fs-xs);
  color: var(--ink-3);
  max-width: 100%;
}
.sync-clients summary {
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sync-clients-list {
  list-style: none;
  margin: var(--s2) 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  color: var(--ink);
}
.sync-clients-list li,
.sync-clients-empty {
  overflow-wrap: anywhere;
}
.sync-clients-empty {
  margin: var(--s2) 0 0;
}
.filter-panel {
  display: flex;
  align-items: center;
  gap: var(--s3);
  flex-wrap: wrap;
  margin: var(--s3) 0 var(--s2);
  min-width: 0;
}
.kind-chips {
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
  min-width: 0;
}
.chip {
  display: inline-flex;
  align-items: center;
  min-height: 28px;
  padding: 2px var(--s2);
  background: transparent;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: 999px;
  font: inherit;
  font-size: var(--fs-sm);
  white-space: nowrap;
}
.chip--on {
  color: var(--need-ink);
  border-color: var(--need);
  background: var(--need-soft);
}
.query-field {
  flex: 1 1 240px;
}
.dim {
  color: var(--ink-3);
}
.err {
  color: var(--fail);
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
.pager {
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
  margin-top: var(--s3);
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.pager-size {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
}
.page-btn {
  min-width: 32px;
  min-height: 32px;
  padding: 0 var(--s2);
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
}
.page-btn--on {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.page-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.pager-gap {
  color: var(--ink-3);
  font-family: var(--font-mono);
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

/* 手机（≤760）：面板与筛选纵向排列，点按目标 ≥40px。 */
@media (max-width: 760px) {
  .page-head {
    flex-direction: column;
    gap: var(--s2);
  }
  .btn,
  .page-btn {
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
  .field--inline {
    flex: 1 0 100%;
  }
}
</style>
