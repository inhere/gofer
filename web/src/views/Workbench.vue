<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, provide, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ApiError, getMeta } from '../api/client'
import {
  getWorkbenchLayout,
  listWorkbenchThreads,
  markAllWorkbenchThreadsSeen,
  putWorkbenchLayout,
} from '../api/workbench'
import type { WorkbenchAttentionItem, WorkbenchStatus, WorkbenchThread, WorkbenchThreadsResp } from '../api/types'
import WorkbenchAttention from '../components/workbench/WorkbenchAttention.vue'
import WorkbenchComposer from '../components/workbench/WorkbenchComposer.vue'
import WorkbenchCommandPalette from '../components/workbench/WorkbenchCommandPalette.vue'
import LayoutPane from '../components/workbench/LayoutPane.vue'
import WorkbenchSidebar from '../components/workbench/WorkbenchSidebar.vue'
import {
  activateTab,
  activeTab,
  addTab,
  assignThread,
  closePane,
  closeTab,
  createLayoutDocument,
  focusDir,
  nodeAt,
  normalize,
  renameTab,
  setRatio,
  splitPane,
  updateTab,
  type DocumentMutation,
  type FocusDirection,
  type LayoutDocument,
  type SplitDirection,
  type TreeMutation,
} from '../components/workbench/layoutTree'
import { createPoller } from '../utils/poller'

interface FocusedThreadActions {
  stopCurrent(): Promise<void>
  focusTurn(): void
  focusAction(action: string): void
}

const response = ref<WorkbenchThreadsResp>({ projects: [], attention: [], total: 0, since: 0 })
const route = useRoute()
const router = useRouter()
const selectedID = ref('')
const pendingJobID = ref('')
const query = ref('')
const projectFilter = ref('')
const statusFilter = ref<WorkbenchStatus | ''>('')
const projectOptions = ref<string[]>([])
// exec threads are mostly one-shot build/verify commands; keep them out of the
// sidebar unless asked for (or still running/blocked, or the open thread).
const EXEC_PREF_KEY = 'gofer.workbench.showExec'
const showExec = ref(readShowExec())
// 已完成（done）的会话只在侧栏保留最近 DONE_WINDOW_SEC；等你/进行中/空闲的不受限，
// 当前打开的也不隐藏。更早的完成会话仍可在 Board / job 列表里找到。
const DONE_WINDOW_SEC = 3 * 86400
function isVisibleThread(thread: WorkbenchThread, now: number, withExec: boolean): boolean {
  if (thread.id === selectedID.value) return true
  if (thread.status === 'done' && now - thread.updated_at > DONE_WINDOW_SEC) return false
  if (!withExec && thread.agent === 'exec' && thread.status !== 'blocked' && thread.status !== 'working') return false
  return true
}
function filterGroups(withExec: boolean) {
  const now = Math.floor(Date.now() / 1000)
  return response.value.projects
    .map((group) => ({ ...group, threads: group.threads.filter((thread) => isVisibleThread(thread, now, withExec)) }))
    .filter((group) => group.threads.length > 0)
}
const sidebarProjects = computed(() => filterGroups(showExec.value))
const hiddenExecCount = computed(() => {
  if (showExec.value) return 0
  let total = 0
  for (const group of filterGroups(true)) total += group.threads.length
  for (const group of sidebarProjects.value) total -= group.threads.length
  return total
})

function readShowExec(): boolean {
  try { return localStorage.getItem(EXEC_PREF_KEY) === '1' } catch { return false }
}

function setShowExec(value: boolean): void {
  showExec.value = value
  try { localStorage.setItem(EXEC_PREF_KEY, value ? '1' : '0') } catch { /* per-viewer convenience only */ }
}
const loading = ref(false)
const seenAllPending = ref(false)
const error = ref('')
const layoutNotice = ref('')
const sidebar = ref<InstanceType<typeof WorkbenchSidebar> | null>(null)
const composer = ref<InstanceType<typeof WorkbenchComposer> | null>(null)
const workbenchRoot = ref<HTMLElement | null>(null)
const paletteOpen = ref(false)
const mobilePane = ref<'sidebar' | 'main'>('sidebar')
const mobileLayout = ref(false)
const mru = ref<string[]>([])
const layoutDocument = ref<LayoutDocument>(createLayoutDocument())
const layoutVersion = ref(0)
const layoutReady = ref(false)
const layoutSaving = ref(false)
const editingTabID = ref('')
const tabTitleDraft = ref('')
const focusedActions = ref<FocusedThreadActions | null>(null)
const acpAgentKeys = ref<Set<string>>(new Set())
const acpCapabilityError = ref('')
const maximizedPath = ref('')
const prefixActive = ref(false)
const prefixHint = ref('')

let layoutGeneration = 0
let savedLayoutGeneration = 0
let layoutSaveTimer: number | null = null
let layoutSaveInFlight = false
let prefixTimer: number | null = null
let mobileQuery: MediaQueryList | null = null
let touchStart: { x: number; y: number } | null = null

const threads = computed(() => response.value.projects.flatMap((project) => project.threads))
const threadsByID = computed(() => new Map(threads.value.map((thread) => [thread.id, thread])))
const selectedThread = computed(() => threadsByID.value.get(selectedID.value))
const currentTab = computed(() => activeTab(layoutDocument.value))
// exec 会话失败默认不进「等你」：多是 agent 自己跑的探测/构建命令，由发起它的 agent
// 处理。勾选「显示 exec 命令会话」时一并显示；exec 等审批（answer）始终保留。
const attentionItems = computed(() => response.value.attention.filter((item) => {
  if (showExec.value || item.action !== 'review') return true
  return threadsByID.value.get(item.thread_id)?.agent !== 'exec'
}))
const attentionCount = computed(() => attentionItems.value.length)

provide('workbench-view-context', {
  threadsByID,
  focusedActions,
  acpAgentKeys,
  acpCapabilityError,
  refresh: loadThreads,
  continued,
  back: () => { mobilePane.value = 'sidebar' },
})

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
    if (layoutReady.value) locateRequestedThread(false)
    if (!layoutReady.value && !selectedID.value) selectedID.value = next.projects[0]?.threads[0]?.id ?? ''
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const poller = createPoller(loadThreads, 5000)

function isEmptyLayoutBody(value: unknown): boolean {
  return typeof value !== 'object' || value === null || Array.isArray(value) || Object.keys(value).length === 0
}

function focusedThreadID(): string {
  const tab = currentTab.value
  const node = nodeAt(tab.root, tab.focused)
  return node?.kind === 'pane' ? node.thread_id ?? '' : ''
}

function syncSelectedFromLayout(): void {
  selectedID.value = focusedThreadID()
  if (selectedID.value) {
    mru.value = [selectedID.value, ...mru.value.filter((id) => id !== selectedID.value)].slice(0, 30)
  }
}

function scheduleLayoutSave(): void {
  if (layoutSaveTimer != null) window.clearTimeout(layoutSaveTimer)
  layoutSaveTimer = window.setTimeout(() => {
    layoutSaveTimer = null
    void flushLayoutSave()
  }, 800)
}

function markLayoutDirty(): void {
  layoutGeneration++
  scheduleLayoutSave()
}

async function adoptServerLayout(message: string): Promise<void> {
  const server = await getWorkbenchLayout()
  layoutVersion.value = server.version
  layoutDocument.value = normalize(server.body)
  layoutGeneration++
  savedLayoutGeneration = layoutGeneration
  syncSelectedFromLayout()
  layoutNotice.value = message
}

async function flushLayoutSave(): Promise<void> {
  if (layoutSaveInFlight || savedLayoutGeneration === layoutGeneration) return
  layoutSaveInFlight = true
  layoutSaving.value = true
  const requestGeneration = layoutGeneration
  try {
    const saved = await putWorkbenchLayout(layoutVersion.value, layoutDocument.value)
    layoutVersion.value = saved.version
    savedLayoutGeneration = requestGeneration
    if (layoutGeneration !== requestGeneration) scheduleLayoutSave()
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      try {
        await adoptServerLayout('布局已在别处更新')
      } catch (refreshError) {
        error.value = refreshError instanceof Error ? refreshError.message : String(refreshError)
      }
    } else {
      error.value = e instanceof Error ? e.message : String(e)
      if (layoutGeneration !== requestGeneration) scheduleLayoutSave()
    }
  } finally {
    layoutSaveInFlight = false
    layoutSaving.value = false
  }
}

function applyDocument(result: DocumentMutation): void {
  if (result.reason) {
    layoutNotice.value = result.reason === 'tab_limit' ? '最多只能打开 8 个标签页' : '布局操作未生效'
    return
  }
  if (result.document === layoutDocument.value) return
  layoutDocument.value = result.document
  syncSelectedFromLayout()
  markLayoutDirty()
}

function updateCurrentTab(root = currentTab.value.root, focused = currentTab.value.focused): void {
  applyDocument(updateTab(layoutDocument.value, currentTab.value.id, root, focused))
}

function layoutRejectMessage(reason: string): string {
  if (reason === 'pane_limit') return '每个标签页最多只能有 4 个窗格'
  if (reason === 'tab_limit') return '最多只能打开 8 个标签页'
  return '布局操作未生效'
}

function applyTreeMutation(result: TreeMutation): void {
  if (result.reason) {
    layoutNotice.value = layoutRejectMessage(result.reason)
    return
  }
  if (maximizedPath.value && !nodeAt(result.root, maximizedPath.value)) maximizedPath.value = ''
  updateCurrentTab(result.root, result.focused)
}

function focusPane(path: string): void {
  if (path === currentTab.value.focused) {
    syncSelectedFromLayout()
    mobilePane.value = 'main'
    return
  }
  if (maximizedPath.value) maximizedPath.value = path
  updateCurrentTab(currentTab.value.root, path)
  mobilePane.value = 'main'
}

function splitFocused(dir: SplitDirection): void {
  applyTreeMutation(splitPane(currentTab.value.root, currentTab.value.focused, dir))
}

function closeFocusedPane(): void {
  maximizedPath.value = ''
  applyTreeMutation(closePane(currentTab.value.root, currentTab.value.focused))
}

function moveFocus(direction: FocusDirection): void {
  const path = focusDir(currentTab.value.root, currentTab.value.focused, direction)
  focusPane(path)
}

function toggleMaximize(): void {
  maximizedPath.value = maximizedPath.value ? '' : currentTab.value.focused
}

function dropThread(path: string, threadID: string, edge: 'center' | 'left' | 'right' | 'top' | 'bottom'): void {
  const thread = threadsByID.value.get(threadID)
  if (!thread) {
    layoutNotice.value = '拖入的会话已不在当前列表中'
    return
  }
  if (edge === 'center') {
    const root = assignThread(currentTab.value.root, path, threadID)
    updateCurrentTab(root, path)
  } else {
    const horizontal = edge === 'left' || edge === 'right'
    applyTreeMutation(splitPane(
      currentTab.value.root,
      path,
      horizontal ? 'h' : 'v',
      threadID,
      edge === 'left' || edge === 'top' ? 'before' : 'after',
    ))
  }
  selectedID.value = threadID
  mobilePane.value = 'main'
  mru.value = [threadID, ...mru.value.filter((id) => id !== threadID)].slice(0, 30)
}

function resizeSplit(path: string, ratio: number): void {
  const root = setRatio(currentTab.value.root, path, ratio)
  if (root !== currentTab.value.root) updateCurrentTab(root, currentTab.value.focused)
}

function selectThread(thread: WorkbenchThread): void {
  const tab = currentTab.value
  const target = nodeAt(tab.root, tab.focused)
  if (target?.kind === 'pane' && target.thread_id === thread.id) {
    selectedID.value = thread.id
    mobilePane.value = 'main'
    mru.value = [thread.id, ...mru.value.filter((id) => id !== thread.id)].slice(0, 30)
    return
  }
  const root = assignThread(tab.root, tab.focused, thread.id)
  updateCurrentTab(root, tab.focused)
  selectedID.value = thread.id
  mobilePane.value = 'main'
  mru.value = [thread.id, ...mru.value.filter((id) => id !== thread.id)].slice(0, 30)
}

function selectAttention(item: WorkbenchAttentionItem): void {
  const thread = threadsByID.value.get(item.thread_id)
  if (thread) selectThread(thread)
  else selectedID.value = item.thread_id
  void nextTick(() => focusedActions.value?.focusAction(item.action))
}

function requestedThreadID(): string {
  const value = route.query.thread
  return typeof value === 'string' ? value : Array.isArray(value) ? value[0] ?? '' : ''
}

function locateRequestedThread(announce: boolean): boolean {
  const id = requestedThreadID()
  if (!id) return false
  const thread = threadsByID.value.get(id)
  if (!thread) {
    if (announce) layoutNotice.value = '通知指向的会话尚未出现，正在等待刷新'
    return false
  }
  const changed = focusedThreadID() !== id
  selectThread(thread)
  if (announce && changed) layoutNotice.value = '已定位到通知对应的会话'
  return true
}

function openFirstAttention(): void {
  const first = attentionItems.value[0]
  if (first) selectAttention(first)
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

function createTab(): void {
  maximizedPath.value = ''
  applyDocument(addTab(layoutDocument.value))
}

function removeTab(tabID: string): void {
  maximizedPath.value = ''
  applyDocument(closeTab(layoutDocument.value, tabID))
}

function selectTab(tabID: string): void {
  maximizedPath.value = ''
  applyDocument(activateTab(layoutDocument.value, tabID))
  mobilePane.value = 'main'
}

function beginRenameTab(tabID: string, title: string): void {
  editingTabID.value = tabID
  tabTitleDraft.value = title
}

function finishRenameTab(): void {
  const tabID = editingTabID.value
  editingTabID.value = ''
  if (tabID) applyDocument(renameTab(layoutDocument.value, tabID, tabTitleDraft.value))
}

function isTypingTarget(target: EventTarget | null): boolean {
  const element = target as HTMLElement | null
  return !!element && ['INPUT', 'TEXTAREA', 'SELECT'].includes(element.tagName)
}

function onMobileQuery(event: MediaQueryListEvent | MediaQueryList): void {
  mobileLayout.value = event.matches
}

function onTouchStart(event: TouchEvent): void {
  if (event.touches.length !== 1) return
  const target = event.target as HTMLElement | null
  if (target?.closest('input, textarea, select, .xterm, .terminal-host')) return
  touchStart = { x: event.touches[0].clientX, y: event.touches[0].clientY }
}

function onTouchEnd(event: TouchEvent): void {
  if (!touchStart || event.changedTouches.length !== 1 || !mobileLayout.value) {
    touchStart = null
    return
  }
  const dx = event.changedTouches[0].clientX - touchStart.x
  const dy = event.changedTouches[0].clientY - touchStart.y
  touchStart = null
  if (Math.abs(dx) < 60 || Math.abs(dx) < Math.abs(dy) * 1.5) return
  if (dx < 0 && mobilePane.value === 'sidebar' && focusedThreadID()) mobilePane.value = 'main'
  else if (dx > 0 && mobilePane.value === 'main') mobilePane.value = 'sidebar'
}

function clearPrefix(): void {
  prefixActive.value = false
  prefixHint.value = ''
  if (prefixTimer != null) {
    window.clearTimeout(prefixTimer)
    prefixTimer = null
  }
}

function startPrefix(): void {
  clearPrefix()
  prefixActive.value = true
  prefixHint.value = 'ctrl+b：% 左右 · " 上下 · 方向键焦点 · x 关闭 · z 最大化 · c 新标签 · n/p/1–8 切标签'
  prefixTimer = window.setTimeout(clearPrefix, 1500)
}

function cycleTab(step: number): void {
  const tabs = layoutDocument.value.tabs
  if (tabs.length < 2) return
  const current = Math.max(0, tabs.findIndex((tab) => tab.id === layoutDocument.value.active_tab_id))
  selectTab(tabs[(current + step + tabs.length) % tabs.length].id)
}

function jumpTab(index: number): void {
  const tab = layoutDocument.value.tabs[index]
  if (tab) selectTab(tab.id)
}

function handlePrefixKey(event: KeyboardEvent): void {
  const key = event.key
  clearPrefix()
  if (key === '%') splitFocused('h')
  else if (key === '"') splitFocused('v')
  else if (key === 'ArrowLeft') moveFocus('left')
  else if (key === 'ArrowRight') moveFocus('right')
  else if (key === 'ArrowUp') moveFocus('up')
  else if (key === 'ArrowDown') moveFocus('down')
  else if (key.toLowerCase() === 'x') closeFocusedPane()
  else if (key.toLowerCase() === 'z') toggleMaximize()
  else if (key.toLowerCase() === 'c') createTab()
  else if (key.toLowerCase() === 'n') cycleTab(1)
  else if (key.toLowerCase() === 'p') cycleTab(-1)
  else if (/^[1-8]$/.test(key)) jumpTab(Number(key) - 1)
  else if (key !== 'Escape') layoutNotice.value = `未知布局快捷键：${key}`
}

function onKeydown(event: KeyboardEvent): void {
  if (event.ctrlKey && !event.altKey && event.key.toLowerCase() === 'b') {
    event.preventDefault()
    event.stopPropagation()
    event.stopImmediatePropagation()
    startPrefix()
    return
  }
  if (prefixActive.value) {
    event.preventDefault()
    event.stopPropagation()
    event.stopImmediatePropagation()
    handlePrefixKey(event)
    return
  }
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
  const available = mru.value.filter((id) => threadsByID.value.has(id))
  if (available.length < 2) return
  const index = Math.max(0, available.indexOf(selectedID.value))
  const next = available[(index + step + available.length) % available.length]
  const thread = threadsByID.value.get(next)
  if (thread) selectThread(thread)
}

function switchProject(project: string): void {
  projectFilter.value = project
  void loadThreads()
}

function paletteAction(action: 'stop' | 'continue' | 'detail' | 'split-h' | 'split-v' | 'close-pane' | 'maximize' | 'new-tab'): void {
  if (action === 'stop') void focusedActions.value?.stopCurrent()
  else if (action === 'continue') focusedActions.value?.focusTurn()
  else if (action === 'detail' && selectedThread.value?.latest_job_id) void router.push(`/jobs/${encodeURIComponent(selectedThread.value.latest_job_id)}`)
  else if (action === 'split-h') splitFocused('h')
  else if (action === 'split-v') splitFocused('v')
  else if (action === 'close-pane') closeFocusedPane()
  else if (action === 'maximize') toggleMaximize()
  else if (action === 'new-tab') createTab()
}

watch(
  () => route.query.thread,
  () => {
    if (layoutReady.value) locateRequestedThread(true)
  },
)

watch(
  attentionCount,
  (count) => {
    document.title = count > 0 ? '(' + count + ') gofer' : 'gofer'
  },
  { immediate: true },
)

onMounted(async () => {
  window.addEventListener('keydown', onKeydown, true)
  mobileQuery = window.matchMedia('(max-width: 767px)')
  onMobileQuery(mobileQuery)
  mobileQuery.addEventListener('change', onMobileQuery)
  const layoutPromise = getWorkbenchLayout().catch((e) => {
    error.value = e instanceof Error ? e.message : String(e)
    return null
  })
  const metaPromise = getMeta()
    .then((meta) => {
      acpAgentKeys.value = new Set(meta.agents.filter((agent) => agent.type === 'acp-agent').map((agent) => agent.key))
      acpCapabilityError.value = ''
    })
    .catch((e) => {
      acpAgentKeys.value = new Set()
      acpCapabilityError.value = e instanceof Error ? e.message : String(e)
    })
  await loadThreads()
  await metaPromise
  const server = await layoutPromise
  const firstThreadID = threads.value[0]?.id ?? null
  if (server) {
    layoutVersion.value = server.version
    const empty = isEmptyLayoutBody(server.body)
    layoutDocument.value = normalize(server.body, empty ? firstThreadID : null)
    if (empty) markLayoutDirty()
  } else {
    layoutDocument.value = createLayoutDocument(firstThreadID)
  }
  layoutReady.value = true
  syncSelectedFromLayout()
  locateRequestedThread(true)
  poller.start()
})

onUnmounted(() => {
  poller.stop()
  if (layoutSaveTimer != null) window.clearTimeout(layoutSaveTimer)
  clearPrefix()
  mobileQuery?.removeEventListener('change', onMobileQuery)
  mobileQuery = null
  window.removeEventListener('keydown', onKeydown, true)
  document.title = 'gofer'
})
</script>

<template>
  <div ref="workbenchRoot" class="workbench-page" tabindex="-1">
    <WorkbenchComposer ref="composer" @submitted="submitted" />
    <p v-if="error" class="page-error mono">{{ error }}</p>
    <p v-if="layoutNotice" class="layout-notice mono">
      {{ layoutNotice }}
      <button type="button" aria-label="关闭布局提示" @click="layoutNotice = ''">×</button>
    </p>
    <button
      v-if="attentionCount > 0"
      class="attention-fallback mono"
      type="button"
      @click="openFirstAttention"
    >
      ⚠ {{ attentionCount }} 项等待处理 · 打开最早一项
    </button>
    <div
      class="workbench-body"
      :class="[`mobile--${mobilePane}`]"
      @touchstart.passive="onTouchStart"
      @touchend.passive="onTouchEnd"
    >
      <WorkbenchSidebar
        ref="sidebar"
        :projects="sidebarProjects"
        :show-exec="showExec"
        :hidden-exec-count="hiddenExecCount"
        :selected-id="selectedID"
        :query="query"
        :project-filter="projectFilter"
        :status-filter="statusFilter"
        :project-options="projectOptions"
        @select="selectThread"
        @update:query="query = $event; refreshFilter()"
        @update:project-filter="projectFilter = $event; refreshFilter()"
        @update:status-filter="statusFilter = $event; refreshFilter()"
        @update:show-exec="setShowExec"
        @new="composer?.focusPrompt()"
      />
      <main class="workbench-main">
        <div class="tabs-row">
        <nav class="layout-tabs" aria-label="工作台标签页">
          <div
            v-for="tab in layoutDocument.tabs"
            :key="tab.id"
            class="layout-tab"
            :class="{ active: tab.id === layoutDocument.active_tab_id }"
          >
            <input
              v-if="editingTabID === tab.id"
              v-model="tabTitleDraft"
              class="tab-title-input mono"
              aria-label="重命名标签页"
              @blur="finishRenameTab"
              @keydown.enter.prevent="finishRenameTab"
              @keydown.esc.prevent="editingTabID = ''"
            />
            <button
              v-else
              class="tab-select mono"
              type="button"
              :aria-current="tab.id === layoutDocument.active_tab_id ? 'page' : undefined"
              @click="selectTab(tab.id)"
              @dblclick="beginRenameTab(tab.id, tab.title)"
            >
              {{ tab.title }}
            </button>
            <button class="tab-close mono" type="button" :aria-label="`关闭 ${tab.title}`" @click="removeTab(tab.id)">×</button>
          </div>
          <button class="tab-new mono" type="button" title="新标签" @click="createTab">＋</button>
          <span v-if="prefixActive" class="prefix-status mono">{{ prefixHint }}</span>
          <span class="layout-version mono">v{{ layoutVersion }}<template v-if="layoutSaving"> · 保存中…</template></span>
        </nav>
          <!-- 「等你」放在标签条同一行、nav 之外：nav 自身 overflow-x 会裁掉下拉 -->
          <WorkbenchAttention
        :items="attentionItems"
        :seen-all-pending="seenAllPending"
        @select="selectAttention"
        @seen-all="markAllSeen"
        />
        </div>
        <div class="layout-surface">
          <LayoutPane
            v-if="layoutReady && currentTab"
            :node="currentTab.root"
            path=""
            :focused-path="currentTab.focused"
            :maximized-path="maximizedPath"
            :solo="mobileLayout"
            :solo-path="currentTab.focused"
            :content-active="!mobileLayout || mobilePane === 'main'"
            @focus="focusPane"
            @ratio="resizeSplit"
            @drop-thread="dropThread"
          />
          <div v-else class="empty-main mono">{{ loading ? '加载会话与布局…' : '点左侧「＋ 新会话」开始，或从左侧选择一个会话。' }}</div>
        </div>
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
.tabs-row { flex: none; display: flex; align-items: center; gap: 8px; padding-right: 8px; background: var(--panel); border-bottom: 1px solid var(--line); }
.tabs-row .layout-tabs { flex: 1 1 auto; border-bottom: 0; }
.page-error { flex: none; margin: 0; padding: 7px 12px; color: var(--fail); background: rgba(200,70,70,.08); border-bottom: 1px solid var(--line); }
.layout-notice { flex: none; display: flex; align-items: center; justify-content: space-between; gap: 12px; margin: 0; padding: 7px 12px; color: var(--run); background: rgba(255,185,80,.08); border-bottom: 1px solid var(--line); }
.layout-notice button { color: inherit; background: transparent; border: 0; font-size: 16px; }
.attention-fallback { display: none; flex: none; width: 100%; margin: 0; padding: 7px 12px; color: var(--run); text-align: left; background: rgba(255,185,80,.12); border: 0; border-bottom: 1px solid var(--line); }
.attention-fallback:hover { color: var(--paper); background: rgba(255,185,80,.18); }
.workbench-body { flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(260px, 24vw) minmax(0,1fr); grid-template-rows: minmax(0,1fr); }
.workbench-main { min-width: 0; min-height: 0; overflow: hidden; display: flex; flex-direction: column; }
.layout-tabs { flex: none; min-width: 0; display: flex; align-items: stretch; gap: 2px; padding: 5px 7px 0; background: var(--panel); border-bottom: 1px solid var(--line); overflow-x: auto; }
.layout-tab { flex: none; display: flex; align-items: center; max-width: 220px; border: 1px solid transparent; border-bottom: 0; border-radius: var(--radius) var(--radius) 0 0; }
.layout-tab.active { background: var(--ink); border-color: var(--line); }
.tab-select, .tab-close, .tab-new { color: var(--queue); background: transparent; border: 0; }
.tab-select { min-width: 70px; max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; padding: 7px 5px 7px 9px; }
.layout-tab.active .tab-select { color: var(--paper); }
.tab-close { padding: 7px 7px 7px 3px; }
.tab-new { padding: 5px 10px; font-size: 16px; }
.tab-select:hover, .tab-close:hover, .tab-new:hover { color: var(--phosphor); }
.tab-title-input { width: 130px; margin: 3px; padding: 3px 5px; color: var(--paper); background: var(--ink); border: 1px solid var(--phosphor); }
.layout-version { margin-left: auto; align-self: center; padding: 0 5px; color: var(--queue); font-size: 10px; white-space: nowrap; }
.prefix-status { align-self: center; margin-left: auto; color: var(--run); font-size: 10px; white-space: nowrap; }
.prefix-status + .layout-version { margin-left: 0; }
.layout-surface { flex: 1; min-width: 0; min-height: 0; position: relative; }
.empty-main { color: var(--queue); }
@media (max-width: 767px) {
  .workbench-page { height: calc(100vh - 53px); }
  .attention-fallback { display: block; }
  .workbench-body { grid-template-columns: 1fr; }
  .workbench-body.mobile--sidebar .workbench-main { display: none; }
  .workbench-body.mobile--main :deep(.wb-sidebar) { display: none; }
  .layout-tabs { padding-right: 48px; }
  .layout-version, .prefix-status { display: none; }
}
</style>
