<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { listWorkbenchThreads, markAllWorkbenchThreadsSeen } from '../api/workbench'
import type { WorkbenchAttentionItem, WorkbenchStatus, WorkbenchThread, WorkbenchThreadsResp } from '../api/types'
import WorkbenchAttention from '../components/workbench/WorkbenchAttention.vue'
import WorkbenchComposer from '../components/workbench/WorkbenchComposer.vue'
import WorkbenchCommandPalette from '../components/workbench/WorkbenchCommandPalette.vue'
import WorkbenchSidebar from '../components/workbench/WorkbenchSidebar.vue'
import WorkbenchThreadPane from '../components/workbench/WorkbenchThreadPane.vue'
import { patchWorkbenchThread } from '../api/workbench'
import { createPoller } from '../utils/poller'

const response = ref<WorkbenchThreadsResp>({ projects: [], attention: [], total: 0, since: 0 })
const router = useRouter()
const selectedID = ref('')
const pendingJobID = ref('')
const query = ref('')
const projectFilter = ref('')
const statusFilter = ref<WorkbenchStatus | ''>('')
const projectOptions = ref<string[]>([])
const loading = ref(false)
const seenAllPending = ref(false)
const error = ref('')
const sidebar = ref<InstanceType<typeof WorkbenchSidebar> | null>(null)
const composer = ref<InstanceType<typeof WorkbenchComposer> | null>(null)
const threadPane = ref<InstanceType<typeof WorkbenchThreadPane> | null>(null)
const workbenchRoot = ref<HTMLElement | null>(null)
const paletteOpen = ref(false)
const mobilePane = ref<'sidebar' | 'main'>('sidebar')
const mru = ref<string[]>([])

const threads = computed(() => response.value.projects.flatMap((project) => project.threads))
const selectedThread = computed(() => threads.value.find((thread) => thread.id === selectedID.value))

async function loadThreads(): Promise<void> {
  loading.value = true
  try {
    const next = await listWorkbenchThreads({
      project: projectFilter.value || undefined,
      status: statusFilter.value,
      q: query.value.trim() || undefined,
    })
    response.value = next
    const keys = new Set(projectOptions.value)
    next.projects.forEach((project) => keys.add(project.project_key))
    projectOptions.value = [...keys].sort()
    if (pendingJobID.value) {
      const match = next.projects.flatMap((project) => project.threads).find((thread) => thread.job_ids?.includes(pendingJobID.value))
      if (match) {
        selectThread(match)
        pendingJobID.value = ''
      }
    }
    if (!selectedID.value || !next.projects.some((project) => project.threads.some((thread) => thread.id === selectedID.value))) {
      const first = next.projects[0]?.threads[0]
      if (first) selectThread(first, false)
      else selectedID.value = ''
    }
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const poller = createPoller(loadThreads, 5000)

function rawTerminal(thread: WorkbenchThread): boolean {
  return ['done', 'failed', 'cancelled', 'timeout', 'rejected'].includes(thread.raw_status ?? '')
}

function selectThread(thread: WorkbenchThread, markSeen = true): void {
  selectedID.value = thread.id
  mobilePane.value = 'main'
  mru.value = [thread.id, ...mru.value.filter((id) => id !== thread.id)].slice(0, 30)
  if (markSeen && thread.status === 'review' && rawTerminal(thread)) {
    void patchWorkbenchThread(thread.id, { seen: true }).then(loadThreads).catch(() => {})
  }
}

function selectAttention(item: WorkbenchAttentionItem): void {
  const thread = threads.value.find((candidate) => candidate.id === item.thread_id)
  if (thread) selectThread(thread)
  else selectedID.value = item.thread_id
  void nextTick(() => threadPane.value?.focusAction(item.action))
}

async function markAllSeen(): Promise<void> {
  if (seenAllPending.value) return
  seenAllPending.value = true
  try {
    await markAllWorkbenchThreadsSeen()
    await loadThreads()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    seenAllPending.value = false
  }
}

function submitted(jobID: string): void {
  pendingJobID.value = jobID
  selectedID.value = `j:${jobID}`
  void loadThreads()
}

function continued(jobID?: string): void {
  if (jobID) submitted(jobID)
  else void loadThreads()
}

function refreshFilter(): void {
  void loadThreads()
}

function isTypingTarget(target: EventTarget | null): boolean {
  const element = target as HTMLElement | null
  return !!element && ['INPUT', 'TEXTAREA', 'SELECT'].includes(element.tagName)
}

function onKeydown(event: KeyboardEvent): void {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
    event.preventDefault()
    paletteOpen.value = true
    return
  }
  if (event.ctrlKey && event.key === 'Tab') {
    event.preventDefault()
    cycleMRU(event.shiftKey ? -1 : 1)
    return
  }
  if (event.key === 'Escape') {
    if (paletteOpen.value) paletteOpen.value = false
    else {
      ;(document.activeElement as HTMLElement | null)?.blur?.()
      workbenchRoot.value?.focus()
    }
    return
  }
  if (event.key === '/' && !event.ctrlKey && !event.metaKey && !event.altKey && !isTypingTarget(event.target)) {
    event.preventDefault()
    sidebar.value?.focusSearch()
  }
}

function cycleMRU(step: number): void {
  const available = mru.value.filter((id) => threads.value.some((thread) => thread.id === id))
  if (available.length < 2) return
  const index = Math.max(0, available.indexOf(selectedID.value))
  const next = available[(index + step + available.length) % available.length]
  const thread = threads.value.find((candidate) => candidate.id === next)
  if (thread) selectThread(thread)
}

function switchProject(project: string): void {
  projectFilter.value = project
  void loadThreads()
}

function paletteAction(action: 'stop' | 'continue' | 'detail'): void {
  if (action === 'stop') void threadPane.value?.stopCurrent()
  else if (action === 'continue') threadPane.value?.focusTurn()
  else if (selectedThread.value?.latest_job_id) void router.push(`/jobs/${encodeURIComponent(selectedThread.value.latest_job_id)}`)
}

onMounted(() => {
  poller.start()
  window.addEventListener('keydown', onKeydown, true)
})
onUnmounted(() => {
  poller.stop()
  window.removeEventListener('keydown', onKeydown, true)
})
</script>

<template>
  <div ref="workbenchRoot" class="workbench-page" tabindex="-1">
    <header class="workbench-top">
      <WorkbenchComposer ref="composer" @submitted="submitted" />
      <WorkbenchAttention
        :items="response.attention"
        :seen-all-pending="seenAllPending"
        @select="selectAttention"
        @seen-all="markAllSeen"
      />
    </header>
    <p v-if="error" class="page-error mono">{{ error }}</p>
    <div class="workbench-body" :class="[`mobile--${mobilePane}`]">
      <WorkbenchSidebar
        ref="sidebar"
        :projects="response.projects"
        :selected-id="selectedID"
        :query="query"
        :project-filter="projectFilter"
        :status-filter="statusFilter"
        :project-options="projectOptions"
        @select="selectThread"
        @update:query="query = $event; refreshFilter()"
        @update:project-filter="projectFilter = $event; refreshFilter()"
        @update:status-filter="statusFilter = $event; refreshFilter()"
      />
      <main class="workbench-main">
        <WorkbenchThreadPane
          v-if="selectedThread"
          ref="threadPane"
          :key="selectedThread.id"
          :thread="selectedThread"
          @refresh="loadThreads"
          @continued="continued"
          @back="mobilePane = 'sidebar'"
        />
        <div v-else class="empty-main mono">{{ loading ? '加载会话…' : '从 composer 开始一个新会话，或从左侧选择。' }}</div>
      </main>
    </div>
    <WorkbenchCommandPalette
      :open="paletteOpen"
      :threads="threads"
      :current="selectedThread"
      :projects="projectOptions"
      @close="paletteOpen = false"
      @select="selectThread"
      @new="composer?.focusPrompt()"
      @switch-project="switchProject"
      @action="paletteAction"
    />
  </div>
</template>

<style scoped>
.workbench-page { height: calc(100vh - 57px); min-height: 520px; display: flex; flex-direction: column; overflow: hidden; background: var(--ink); }
.workbench-top { flex: none; display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 12px; align-items: center; padding: 10px 12px; background: var(--panel); border-bottom: 1px solid var(--line); }
.page-error { flex: none; margin: 0; padding: 7px 12px; color: var(--fail); background: rgba(200,70,70,.08); border-bottom: 1px solid var(--line); }
.workbench-body { flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(260px, 24vw) minmax(0,1fr); }
.workbench-main { min-width: 0; min-height: 0; overflow: auto; display: grid; place-items: center; padding: 28px; }
.empty-main { color: var(--queue); }
@media (max-width: 760px) {
  .workbench-page { height: calc(100vh - 53px); }
  .workbench-top { grid-template-columns: 1fr; max-height: 42vh; overflow-y: auto; }
  .workbench-body { grid-template-columns: 1fr; }
  .workbench-body.mobile--sidebar .workbench-main { display: none; }
  .workbench-body.mobile--main :deep(.wb-sidebar) { display: none; }
}
</style>
