<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { listWorkbenchThreads } from '../api/workbench'
import type { WorkbenchAttentionItem, WorkbenchStatus, WorkbenchThread, WorkbenchThreadsResp } from '../api/types'
import WorkbenchAttention from '../components/workbench/WorkbenchAttention.vue'
import WorkbenchComposer from '../components/workbench/WorkbenchComposer.vue'
import WorkbenchSidebar from '../components/workbench/WorkbenchSidebar.vue'
import { createPoller } from '../utils/poller'

const response = ref<WorkbenchThreadsResp>({ projects: [], attention: [], total: 0, since: 0 })
const selectedID = ref('')
const pendingJobID = ref('')
const query = ref('')
const projectFilter = ref('')
const statusFilter = ref<WorkbenchStatus | ''>('')
const projectOptions = ref<string[]>([])
const loading = ref(false)
const error = ref('')
const sidebar = ref<InstanceType<typeof WorkbenchSidebar> | null>(null)
const composer = ref<InstanceType<typeof WorkbenchComposer> | null>(null)

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
        selectedID.value = match.id
        pendingJobID.value = ''
      }
    }
    if (!selectedID.value || !next.projects.some((project) => project.threads.some((thread) => thread.id === selectedID.value))) {
      selectedID.value = next.projects[0]?.threads[0]?.id ?? ''
    }
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const poller = createPoller(loadThreads, 5000)

function selectThread(thread: WorkbenchThread): void {
  selectedID.value = thread.id
}

function selectAttention(item: WorkbenchAttentionItem): void {
  selectedID.value = item.thread_id
}

function submitted(jobID: string): void {
  pendingJobID.value = jobID
  selectedID.value = `j:${jobID}`
  void loadThreads()
}

function refreshFilter(): void {
  void loadThreads()
}

function isTypingTarget(target: EventTarget | null): boolean {
  const element = target as HTMLElement | null
  return !!element && ['INPUT', 'TEXTAREA', 'SELECT'].includes(element.tagName)
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === '/' && !event.ctrlKey && !event.metaKey && !event.altKey && !isTypingTarget(event.target)) {
    event.preventDefault()
    sidebar.value?.focusSearch()
  }
}

onMounted(() => {
  poller.start()
  window.addEventListener('keydown', onKeydown)
})
onUnmounted(() => {
  poller.stop()
  window.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div class="workbench-page">
    <header class="workbench-top">
      <WorkbenchComposer ref="composer" @submitted="submitted" />
      <WorkbenchAttention :items="response.attention" @select="selectAttention" />
    </header>
    <p v-if="error" class="page-error mono">{{ error }}</p>
    <div class="workbench-body">
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
        <div v-if="selectedThread" class="thread-placeholder">
          <span class="status-dot" :class="`status--${selectedThread.status}`"></span>
          <h1>{{ selectedThread.title }}</h1>
          <p class="mono">{{ selectedThread.agent || selectedThread.kind }} · {{ selectedThread.project_key || '未归属' }} · {{ selectedThread.turns }} turn(s)</p>
          <p class="hint">会话内容、续接与快捷键在下一功能点接入；当前 launcher/列表/attention 已连接真实 workbench API。</p>
        </div>
        <div v-else class="empty-main mono">{{ loading ? '加载会话…' : '从 composer 开始一个新会话，或从左侧选择。' }}</div>
      </main>
    </div>
  </div>
</template>

<style scoped>
.workbench-page { height: calc(100vh - 57px); min-height: 520px; display: flex; flex-direction: column; overflow: hidden; background: var(--ink); }
.workbench-top { flex: none; display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 12px; align-items: center; padding: 10px 12px; background: var(--panel); border-bottom: 1px solid var(--line); }
.page-error { flex: none; margin: 0; padding: 7px 12px; color: var(--fail); background: rgba(200,70,70,.08); border-bottom: 1px solid var(--line); }
.workbench-body { flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(260px, 24vw) minmax(0,1fr); }
.workbench-main { min-width: 0; min-height: 0; overflow: auto; display: grid; place-items: center; padding: 28px; }
.thread-placeholder { max-width: 680px; color: var(--paper); text-align: center; }
.thread-placeholder h1 { margin: 10px 0; font-size: 22px; }
.thread-placeholder p { color: var(--queue); }
.hint { line-height: 1.6; }
.empty-main { color: var(--queue); }
.status-dot { display: inline-block; width: 10px; height: 10px; border-radius: 50%; background: var(--queue); }
.status--blocked { background: var(--fail); }
.status--working { background: var(--phosphor); }
.status--review { background: var(--run); }
.status--done { background: var(--done); }
@media (max-width: 760px) { .workbench-page { height: calc(100vh - 53px); } .workbench-top { grid-template-columns: 1fr; } .workbench-body { grid-template-columns: 1fr; } .workbench-main { display: none; } }
</style>
