<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter, type LocationQueryRaw } from 'vue-router'
import IssueDrawer from '../components/IssueDrawer.vue'
import MemoryList from '../components/MemoryList.vue'
import StatusBadge from '../components/StatusBadge.vue'
import {
  commentTrackerIssue,
  deleteTrackerMemory,
  getTrackerIssue,
  listTrackerIssues,
  listTrackerMemories,
  listTrackerRepos,
  listProjects,
  listScopedMemories,
  updateTrackerIssue,
  updateTrackerMemory,
  updateScopedMemory,
  deleteScopedMemory,
} from '../api/client'
import type { TrackerIssue, TrackerIssueView, TrackerMemory, TrackerRepo } from '../api/types'
import { fmtTrackerTime, trackerIssueMatches, trackerMemoryMatches, trackerRepoLabel } from '../utils/trackerView'

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
const saving = ref(false)

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
const filteredMemories = computed(() => memories.value.filter((row) => trackerMemoryMatches(row.data, row.id, query.value)))

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
    memories.value = (memoryResult.memories ?? []).filter((item) => !item.deleted).map((item) => {
      if ('body' in item) return { ...item, data: item.body }
      return { id: item.key, rev: 1, updated_at: item.updated_at, body: { key: item.key, content: item.content, tags: item.tags, updated_at: item.updated_at, by: item.updated_by }, data: { key: item.key, content: item.content, tags: item.tags, updated_at: item.updated_at, by: item.updated_by } }
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function changeRepo(): Promise<void> {
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
    await loadRepos()
    updateMemoryQuery()
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
  <main class="tracker-page">
    <header class="page-head">
      <div><p class="eyebrow mono">TRACKER</p><h1 class="title mono">Issues</h1></div>
      <button class="primary-btn mono" type="button" :disabled="loading || (tab === 'issues' ? !trackerId : (memoryScope === 'repo' ? !trackerId : memoryScope === 'project' && !projectKey))" @click="load">刷新</button>
    </header>
    <section class="meta-panel">
      <label v-if="tab === 'issues' || memoryScope === 'repo'" class="repo-field mono"><span>仓库</span><select v-model="trackerId" class="filter-select" @change="changeRepo"><option value="">请选择已登记仓库</option><option v-for="item in repos" :key="item.tracker_id" :value="item.tracker_id">{{ trackerRepoLabel(item) }}</option></select></label>
      <div v-if="tab === 'memories'" class="scope-field mono">
        <span>记忆作用域</span>
        <div class="scope-switch" role="radiogroup" aria-label="记忆作用域">
          <button v-for="item in [{ value: 'repo', label: '仓库' }, { value: 'project', label: '项目' }, { value: 'global', label: '全局' }]" :key="item.value" type="button" class="scope-btn" :class="{ active: memoryScope === item.value }" :aria-checked="memoryScope === item.value" role="radio" @click="selectMemoryScope(item.value)">{{ item.label }}</button>
        </div>
        <small class="scope-help">{{ memoryScope === 'repo' ? '随代码存于该仓库 .gofer/tracker' : memoryScope === 'project' ? '存于 server，整个 gofer 项目共享' : '存于 server，所有项目共享' }}</small>
      </div>
      <label v-if="tab === 'memories' && memoryScope === 'project'" class="repo-field mono"><span>项目</span><select v-model="projectKey" class="filter-select" @change="changeMemoryProject"><option value="">请选择项目</option><option v-for="item in projectKeys" :key="item" :value="item">{{ item }}</option></select></label>
      <div v-if="repo && (tab === 'issues' || memoryScope === 'repo')" class="sync-summary mono"><span>上次同步 {{ fmtTrackerTime(repo.last_sync_at) }}</span><span>{{ repo.sync_summary || '暂无同步冲突摘要' }}</span></div>
    </section>
    <section v-if="repos.length === 0 && memoryScope === 'repo'" class="empty-panel mono"><span>暂无已登记仓库</span><code>gofer repo init</code><code>gofer repo sync</code></section>
    <template v-else>
      <nav class="tabs mono"><button type="button" :class="{ active: tab === 'issues' }" @click="tab = 'issues'">Issues</button><button type="button" :class="{ active: tab === 'memories' }" @click="tab = 'memories'">Memories</button></nav>
      <section class="filter-panel">
        <div v-if="tab === 'issues'" class="chips mono"><button v-for="status in ['open', 'in_progress', 'blocked', 'closed']" :key="status" type="button" class="chip" :class="[`chip--${status}`, { selected: statuses.includes(status) }]" @click="toggleStatus(status)">{{ status }}</button></div>
        <label v-if="tab === 'issues'" class="filter-field">类型<input v-model="typeFilter" class="filter-input mono" placeholder="全部" /></label>
        <label v-if="tab === 'issues'" class="filter-field">标签<input v-model="tagFilter" class="filter-input mono" placeholder="标签" /></label>
        <label class="filter-field query-field">关键字<input v-model="query" class="filter-input mono" :placeholder="tab === 'memories' ? 'key 或内容' : 'ID、标题或内容'" /></label>
      </section>
      <p v-if="error" class="error-panel mono">{{ error }}</p><p v-else-if="loading" class="loading mono">加载中…</p>
      <section v-if="tab === 'issues' && !loading" class="table"><div class="thead mono"><span>ID</span><span>标题</span><span>状态</span><span>优先级</span><span>类型</span><span>标签</span><span class="updated-col">更新</span></div><button v-for="row in filteredIssues" :key="row.id" type="button" class="trow" @click="openIssue(row)"><span class="mono">{{ row.id }}</span><strong>{{ row.data.title || '—' }}</strong><span><StatusBadge :status="(row.data.status || 'open') as any" /></span><span>{{ row.data.priority ?? 0 }}</span><span>{{ row.data.type || '—' }}</span><span class="muted">{{ (row.data.tags ?? []).join(' · ') || '—' }}</span><span class="mono updated-col">{{ fmtTrackerTime(row.data.updated_at || row.updated_at) }}</span></button><div v-if="filteredIssues.length === 0" class="table-empty mono">暂无匹配 issue</div></section>
      <MemoryList v-if="tab === 'memories' && !loading" :rows="filteredMemories" :selected="selectedMemory" :draft="memoryDraft" @open="openMemory" @update:draft="memoryDraft = $event" @save="saveMemory" @remove="removeMemory" />
    </template>
    <IssueDrawer v-if="selected" :issue="selected" :saving="saving" :expected-rev="selectedRow?.rev ?? 0" @close="closeDrawer" @save="saveIssue" @comment="addComment" />
  </main>
</template>

<style scoped>
.tracker-page{max-width:1240px;margin:0 auto}.page-head{display:flex;justify-content:space-between;align-items:center;margin-bottom:14px}.eyebrow{color:var(--queue);font-size:10px;letter-spacing:.18em;margin:0}.title{color:var(--paper);font-size:18px;margin:0}.meta-panel,.filter-panel,.empty-panel{border:1px solid var(--line);border-radius:var(--radius);background:var(--panel);padding:12px 14px}.meta-panel{display:flex;align-items:flex-end;gap:18px;margin-bottom:14px}.repo-field,.filter-field,.scope-field{display:flex;flex-direction:column;gap:5px;color:var(--queue);font-size:11px}.repo-field,.scope-field{min-width:360px}.scope-switch{display:flex;gap:0}.scope-btn{border:1px solid var(--line);border-right:0;padding:6px 14px;background:transparent;color:var(--queue);font:inherit}.scope-btn:first-child{border-radius:var(--radius) 0 0 var(--radius)}.scope-btn:last-child{border-right:1px solid var(--line);border-radius:0 var(--radius) var(--radius) 0}.scope-btn.active{background:var(--phosphor);border-color:var(--phosphor);color:var(--ink);font-weight:600}.scope-help{color:var(--queue);opacity:.8}.sync-summary{display:flex;flex-direction:column;gap:4px;color:var(--queue);font-size:11px}.filter-select,.filter-input{background:var(--panel);color:var(--paper);border:1px solid var(--line);border-radius:var(--radius);padding:6px 8px;font-size:12px}.filter-select:focus,.filter-input:focus{border-color:var(--phosphor);outline:none}.primary-btn,.secondary-btn{border-radius:var(--radius);padding:5px 10px;font-size:12px}.primary-btn{background:var(--phosphor);color:var(--ink);border:1px solid var(--phosphor);font-weight:600}.secondary-btn{background:transparent;color:var(--phosphor);border:1px solid var(--line)}.tabs{display:flex;gap:18px;border-bottom:1px solid var(--line);margin-bottom:12px}.tabs button{color:var(--queue);background:transparent;border:0;border-bottom:2px solid transparent;padding:7px 3px}.tabs button.active{color:var(--phosphor);border-bottom-color:var(--phosphor)}.filter-panel{display:flex;align-items:flex-end;gap:12px;flex-wrap:wrap;margin-bottom:12px}.query-field{flex:1 1 220px}.chips{display:flex;gap:6px;flex-wrap:wrap}.chip{border:1px solid var(--line);border-radius:999px;padding:5px 9px;background:transparent;color:var(--queue);font:inherit;opacity:.55}.chip.selected{opacity:1}.chip--open.selected{color:var(--done);border-color:var(--done)}.chip--in_progress.selected{color:var(--run);border-color:var(--run)}.chip--blocked.selected{color:var(--fail);border-color:var(--fail)}.table{border:1px solid var(--line);border-radius:var(--radius);overflow:hidden}.thead,.trow{display:grid;grid-template-columns:130px minmax(180px,1.4fr) 120px 70px 100px minmax(120px,1fr) 150px;gap:10px;align-items:center;padding:9px 12px}.thead{background:var(--panel);color:var(--queue);font-size:11px;letter-spacing:.06em}.trow{width:100%;text-align:left;background:transparent;border:0;border-top:1px solid var(--line);color:var(--paper);font-size:12px}.trow:hover{background:var(--panel)}.muted,.loading,.table-empty{color:var(--queue)}.table-empty{text-align:center;padding:28px}.empty-panel{display:flex;justify-content:center;align-items:center;gap:10px;min-height:150px;color:var(--queue)}.empty-panel code{color:var(--phosphor);background:var(--term-bg);border:1px solid var(--line);padding:5px 8px;border-radius:var(--radius)}.error-panel{color:var(--fail);border:1px solid var(--fail);padding:8px}
@media (max-width:639px){.meta-panel{flex-direction:column;align-items:stretch}.repo-field{min-width:0}.thead,.trow{grid-template-columns:100px minmax(150px,1fr) 100px 55px}.thead span:nth-child(n+5),.trow>span:nth-child(n+5){display:none}}
</style>
