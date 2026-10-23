<script setup lang="ts">
// 「记忆」页（UI 重设计 P6，issue gofer-hez1）：原 Issues 页的记忆页签独立成 /memory。
// 三档作用域（仓库 / 项目 / 全局）各取自不同接口，取数与筛选在 utils/memories.ts，
// 列表行仍是 components/MemoryList.vue（与迁移前同一份组件）。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import MemoryList from '../components/MemoryList.vue'
import FilterChips from '../ui/FilterChips.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import {
  deleteScopedMemory,
  deleteTrackerMemory,
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
  memoryDoctorOf,
  memoryQueryUpdate,
  memoryRowKey,
  memoryRowsOf,
  readMemoryKey,
  readMemoryProject,
  readMemoryScope,
  readMemorySearch,
  searchQueryUpdate,
  type MemoryRow,
  type MemoryScope,
} from '../utils/memories'
import { useRepoSync } from '../utils/useRepoSync'
import { fmtTrackerTime, MEMORY_KIND_LABEL, MEMORY_KINDS, syncClientLine, trackerRepoLabel } from '../utils/trackerView'
import { t } from '../i18n'
import type { FilterChipItem } from '../ui/filterChips'

const route = useRoute()
const router = useRouter()

const repos = ref<TrackerRepo[]>([])
const projectKeys = ref<string[]>([])
const trackerId = ref(typeof route.query.tracker === 'string' ? route.query.tracker : '')
const scope = ref<MemoryScope>(readMemoryScope(route.query))
const projectKey = ref(readMemoryProject(route.query))
const memories = ref<MemoryRow[]>([])
const doctor = ref<Record<string, MemoryDoctorFinding[]>>({})
const kinds = ref<string[]>([])
const flaggedOnly = ref(false)
const search = ref(readMemorySearch(route.query))
const selected = ref<MemoryRow | null>(null)
const draft = ref('')
const loading = ref(false)
const error = ref('')

// 仓库记忆要有仓库、项目记忆要有项目；全局记忆直接可读。
const canLoad = computed(() => {
  if (scope.value === 'repo') return !!trackerId.value
  if (scope.value === 'project') return !!projectKey.value
  return true
})
const filtered = computed(() => filterMemories(memories.value, search.value, kinds.value, flaggedOnly.value, doctor.value))
const repo = computed(() => repos.value.find((item) => item.tracker_id === trackerId.value))
const repoIds = computed(() => repos.value.map((item) => item.tracker_id))

const scopeItems = computed<FilterChipItem[]>(() => [
  { value: 'repo', label: t('memory.scope.repo') },
  { value: 'project', label: t('memory.scope.project') },
  { value: 'global', label: t('memory.scope.global') },
])
const scopeHelp = computed(() => t(`memory.scope.${scope.value}Help`))
const kindItems = computed<FilterChipItem[]>(() => MEMORY_KINDS.map((kind) => ({ value: kind, label: MEMORY_KIND_LABEL[kind] })))

// 同步状态：作用域 / 项目 / 仓库 / 关键字都写进 URL（可分享、后退可复原）。
function writeQuery(next: Record<string, string>): void {
  void router.replace({ query: { ...next } })
}

function pickScope(value: string): void {
  if (value !== 'repo' && value !== 'project' && value !== 'global') return
  if (value === scope.value) return
  scope.value = value
  selected.value = null
  writeQuery(memoryQueryUpdate(route.query, scope.value, projectKey.value))
  void load()
}

function pickRepo(value: string): void {
  if (!value || value === trackerId.value) return
  trackerId.value = value
  selected.value = null
  writeQuery({ ...memoryQueryUpdate(route.query, scope.value, projectKey.value), tracker: value })
  void load()
}

function changeProject(): void {
  selected.value = null
  writeQuery(memoryQueryUpdate(route.query, scope.value, projectKey.value))
  void load()
}

function clearSearch(): void {
  search.value = ''
}

watch(search, (value) => {
  writeQuery(searchQueryUpdate(route.query, value))
})

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
  if (!canLoad.value) {
    memories.value = []
    doctor.value = {}
    return
  }
  loading.value = true
  error.value = ''
  try {
    const result = scope.value === 'repo'
      ? await listTrackerMemories(trackerId.value)
      : await listScopedMemories(scope.value, scope.value === 'project' ? projectKey.value : '', search.value)
    doctor.value = memoryDoctorOf(result)
    memories.value = memoryRowsOf(result)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
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

function openMemory(row: MemoryRow): void {
  selected.value = selected.value?.id === row.id ? null : row
  draft.value = String(row.data.content ?? '')
}

async function saveMemory(): Promise<void> {
  if (!selected.value) return
  try {
    if (scope.value === 'repo') {
      await updateTrackerMemory(trackerId.value, selected.value.id, { content: draft.value, expected_rev: selected.value.rev })
    } else {
      await updateScopedMemory(scope.value, scope.value === 'project' ? projectKey.value : '', selected.value.id, {
        content: draft.value,
        tags: selected.value.data.tags ?? [],
      })
    }
    selected.value = null
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function removeMemory(row: MemoryRow): Promise<void> {
  try {
    if (scope.value === 'repo') await deleteTrackerMemory(trackerId.value, row.id)
    else await deleteScopedMemory(scope.value, scope.value === 'project' ? projectKey.value : '', row.id)
    selected.value = null
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
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
        <p class="sub">{{ t('memory.subtitle', { n: filtered.length }) }}</p>
      </div>
      <div class="head-actions">
        <button class="btn btn--primary" type="button" :disabled="loading || !canLoad" data-test="memory-refresh" @click="load">
          {{ t('memory.refresh') }}
        </button>
      </div>
    </header>

    <!-- 作用域：仓库（随代码） / 项目（server） / 全局（server，所有项目共享）。 -->
    <section class="scope-row">
      <span class="field-label">{{ t('memory.scope.label') }}</span>
      <FilterChips :model-value="scope" :items="scopeItems" :label="t('memory.scope.label')" @update:model-value="pickScope(String($event))" />
      <small class="scope-help">{{ scopeHelp }}</small>
    </section>

    <section class="meta-panel">
      <div v-if="scope === 'repo'" class="repo-row">
        <ProjectFilter :model-value="trackerId" :options="repoIds" @update:model-value="pickRepo" />
        <span class="repo-current" :title="trackerId">{{ repo ? trackerRepoLabel(repo) : t('memory.repoPlaceholder') }}</span>
        <button class="btn" type="button" data-test="sync-repo" :disabled="syncing || !trackerId" :title="t('memory.repoSyncTitle')" @click="syncRepo">
          {{ syncing ? t('memory.repoSyncing') : t('memory.repoSync') }}
        </button>
      </div>
      <div v-else-if="scope === 'project'" class="repo-row">
        <span class="field-label">{{ t('memory.project') }}</span>
        <select v-model="projectKey" class="in" data-test="memory-project" @change="changeProject">
          <option value="">{{ t('memory.projectPlaceholder') }}</option>
          <option v-for="item in projectKeys" :key="item" :value="item">{{ item }}</option>
        </select>
      </div>
      <div v-else class="repo-row">
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
          <button v-if="scope === 'repo'" type="button" class="chip" :class="{ 'chip--on': flaggedOnly }" :aria-pressed="flaggedOnly" data-test="memory-flagged" :title="t('memory.filter.flaggedTitle')" @click="flaggedOnly = !flaggedOnly">{{ t('memory.filter.flagged') }}</button>
        </div>
        <label class="field field--inline query-field">
          {{ t('memory.filter.keyword') }}
          <input v-model="search" class="in" type="search" data-test="memory-search" :placeholder="t('memory.filter.keywordPlaceholder')" @keydown.esc="clearSearch" />
        </label>
      </section>
      <p v-if="error" class="err panel-err" data-test="memory-error">{{ error }}</p>
      <p v-else-if="loading" class="dim">{{ t('memory.loading') }}</p>
      <MemoryList
        v-if="!loading"
        :doctor="scope === 'repo' ? doctor : {}"
        :rows="filtered"
        :selected="selected"
        :draft="draft"
        @open="openMemory"
        @update:draft="draft = $event"
        @save="saveMemory"
        @remove="removeMemory"
      />
    </template>
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
}
.repo-current {
  color: var(--ink);
  font-size: var(--fs-sm);
  font-family: var(--font-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 40ch;
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
  .btn {
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
