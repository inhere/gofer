<script setup lang="ts">
import { computed, onMounted, onUnmounted, provide, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ApiError } from '../api/client'
import { getMetaCached } from '../api/metaCache'
import {
  getWorkbenchLayout,
  listWorkbenchThreads,
  putWorkbenchLayout,
} from '../api/workbench'
import type { WorkbenchStatus, WorkbenchThread, WorkbenchThreadsResp } from '../api/types'
import { t } from '../i18n'
import WorkbenchComposer from '../components/workbench/WorkbenchComposer.vue'
import WorkbenchCommandPalette from '../components/workbench/WorkbenchCommandPalette.vue'
import WorkbenchContext from '../components/workbench/WorkbenchContext.vue'
import LayoutPane from '../components/workbench/LayoutPane.vue'
import WorkbenchSidebar from '../components/workbench/WorkbenchSidebar.vue'
import { orphanWaitingCount } from '../components/workbench/threadGroups'
import Drawer from '../ui/Drawer.vue'
import { useProjectFilter } from '../ui/projectFilter'
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
  panePaths,
  normalize,
  pruneThreads,
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
import { createLiveTopic } from '../utils/useLiveTopic'
import { createThreadAutoFocus } from '../components/workbench/threadAutoFocus'

interface FocusedThreadActions {
  stopCurrent(): Promise<void>
  focusTurn(): void
  showView(view: 'process' | 'changes'): void
}

const response = ref<WorkbenchThreadsResp>({ projects: [], attention: [], total: 0, since: 0 })
const route = useRoute()
const router = useRouter()
const selectedID = ref('')
const query = ref('')
// 项目筛选写进 URL 的 ?project=（设计稿 §6.4）：唯一真源，可分享、可回退。
const {
  project: projectFilter,
  setProject,
  options: projectOptions,
} = useProjectFilter('/workbench')
const statusFilter = ref<WorkbenchStatus | ''>('')
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
// 侧栏按状态分组（需要你 / 活跃 / 空闲与离线 / 已结束，设计稿 §7），不再按项目分组；
// 项目是筛选条件（ProjectFilter + ?project=，§6.4）。
const sidebarThreads = computed(() => filterGroups(showExec.value).flatMap((group) => group.threads))
const hiddenExecCount = computed(() => {
  if (showExec.value) return 0
  let total = 0
  for (const group of filterGroups(true)) total += group.threads.length
  return total - sidebarThreads.value.length
})

// 服务端 counts.orphan_blocked / orphan_review 汇成一行提示（受阻计划 / 待决策，不在会话行里）。
const orphanWaiting = computed(() => orphanWaitingCount(response.value.projects))

// 这些项不是会话（是受阻计划 / 待决策），所以点过去到计划列表的「受阻」视图，
// 带上当前项目筛选。
function openOrphanWaiting(): void {
  const query: Record<string, string> = { status: 'blocked' }
  if (projectFilter.value) query.project = projectFilter.value
  void router.push({ path: '/plans', query })
}

function readShowExec(): boolean {
  try { return localStorage.getItem(EXEC_PREF_KEY) === '1' } catch { return false }
}

function setShowExec(value: boolean): void {
  showExec.value = value
  try { localStorage.setItem(EXEC_PREF_KEY, value ? '1' : '0') } catch { /* per-viewer convenience only */ }
}
const loading = ref(false)
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
const focusedView = ref<'process' | 'changes'>('process')
const stoppingFocused = ref(false)
const wideLayout = ref(false)
// 右侧上下文栏默认收起，点顶部按钮展开（用户 2026-10-23：常开会挤占中间的对话）；状态记在本地。
const CONTEXT_OPEN_KEY = 'gofer.workbench.contextOpen'
function readContextOpen(): boolean {
  try { return localStorage.getItem(CONTEXT_OPEN_KEY) === '1' } catch { return false }
}
const contextOpen = ref(readContextOpen())
function toggleContext() {
  contextOpen.value = !contextOpen.value
  try { localStorage.setItem(CONTEXT_OPEN_KEY, contextOpen.value ? '1' : '0') } catch { /* 无本地存储时只在本页生效 */ }
}
const showContext = computed(() => wideLayout.value && !mobileLayout.value && contextOpen.value)

// 首次拉取是否已经成功过（onMounted 里 await 过 /v1/workbench/threads）。
let threadsLoaded = false
let layoutGeneration = 0
let savedLayoutGeneration = 0
let layoutSaveTimer: number | null = null
let layoutSaveInFlight = false
let prefixTimer: number | null = null
let mobileQuery: MediaQueryList | null = null
let wideQuery: MediaQueryList | null = null
let touchStart: { x: number; y: number } | null = null

const threads = computed(() => response.value.projects.flatMap((project) => project.threads))
const threadsByID = computed(() => new Map(threads.value.map((thread) => [thread.id, thread])))
const selectedThread = computed(() => threadsByID.value.get(selectedID.value))
const currentTab = computed(() => activeTab(layoutDocument.value))

provide('workbench-view-context', {
  threadsByID,
  focusedActions,
  acpAgentKeys,
  acpCapabilityError,
  refresh: loadThreads,
  continued,
  back: () => { mobilePane.value = 'sidebar' },
})

// 线程列表默认只要摘要（jobs / 待批交互 / cwd 等重字段很大），打开着的会话用 `full` 要完整的。
// 入口那一拉不知道布局里开了哪些会话，直接要完整的（'all'）；之后每次拉取记下哪些会话是完整的。
let fullCovered: ReadonlySet<string> | 'all' = 'all'
let loadSeq = 0

function wantedFullIDs(): string[] {
  const ids = new Set<string>()
  if (selectedID.value) ids.add(selectedID.value)
  if (layoutReady.value) {
    const root = currentTab.value.root
    for (const path of panePaths(root)) {
      const node = nodeAt(root, path)
      if (node?.kind === 'pane' && node.thread_id) ids.add(node.thread_id)
    }
  }
  return [...ids].sort()
}

async function loadThreads(): Promise<void> {
  loading.value = true
  const seq = ++loadSeq
  const full = wantedFullIDs()
  const summary = threadsLoaded
  try {
    const next = await listWorkbenchThreads({
      project: projectFilter.value || undefined,
      status: statusFilter.value,
      q: query.value.trim() || undefined,
      detail: summary ? 'summary' : undefined,
      full: summary ? full : undefined,
    })
    // 较早发出的请求晚到：丢掉，别盖掉后发的（选中变化会触发一次新拉取）。
    if (seq !== loadSeq) return
    fullCovered = summary ? new Set(full) : 'all'
    response.value = next
    threadsLoaded = true
    const keys = new Set(projectOptions.value)
    next.projects.forEach((project) => keys.add(project.project_key))
    projectOptions.value = [...keys].sort()
    autoFocus.refreshed(threadsByID.value, layoutReady.value)
    if (!layoutReady.value && !selectedID.value) selectedID.value = next.projects[0]?.threads[0]?.id ?? ''
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// 项目 chip 只改 URL，取数由下面的 watch 统一触发（换项目也要重拉）。
watch(projectFilter, () => { void loadThreads() })

// Q3：线程列表由 job（`jobs`）与会话 / 中继决策（`sessions`）汇成；任一失效就重拉，WS 断开
// 超过 15s 才由 30s 一次的兜底轮询接手（恢复后自动停）。
// 选中 / 窗格里换了会话：新会话在上一次响应里只有摘要，补拉一次（同一事件里多次变化合并成一次）。
let fullRefetchTimer: number | null = null
watch(
  () => wantedFullIDs().join(','),
  () => {
    if (!threadsLoaded || fullCovered === 'all') return
    const covered = fullCovered
    if (wantedFullIDs().every((id) => covered.has(id))) return
    if (fullRefetchTimer != null) window.clearTimeout(fullRefetchTimer)
    fullRefetchTimer = window.setTimeout(() => {
      fullRefetchTimer = null
      void loadThreads()
    }, 50)
  },
)

const liveJobs = createLiveTopic('jobs', { initial: false, fetch: loadThreads })
const liveSessions = createLiveTopic('sessions', { initial: false, fetch: loadThreads })
const poller = {
  // onMounted 已经 await 过首拉（布局初始化要用到 threads），这里只补一次「首拉失败」的重试：
  // 否则每次进入工作台都会把 /v1/workbench/threads（约 95 KB）拉两次（gofer 手机版反馈）。
  start(): void {
    if (!threadsLoaded) void loadThreads()
    liveJobs.start()
    liveSessions.start()
  },
  stop(): void {
    liveJobs.stop()
    liveSessions.stop()
  },
}

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
        await adoptServerLayout(t('workbench.notice.moved'))
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
    layoutNotice.value = result.reason === 'tab_limit' ? t('workbench.notice.tabLimit') : t('workbench.notice.rejected')
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
  if (reason === 'pane_limit') return t('workbench.notice.paneLimit')
  if (reason === 'tab_limit') return t('workbench.notice.tabLimit')
  return t('workbench.notice.rejected')
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
    layoutNotice.value = t('workbench.notice.dropMissing')
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

function requestedThreadID(): string {
  const value = route.query.thread
  return typeof value === 'string' ? value : Array.isArray(value) ? value[0] ?? '' : ''
}

function clearRequestedThread(): void {
  if (!requestedThreadID()) return
  const next = { ...route.query }
  delete next.thread
  void router.replace({ query: next })
}

const autoFocus = createThreadAutoFocus({
  requestedThreadID,
  clearRequestedThread,
  focusedThreadID,
  selectThread,
  setSelectedID: (id) => { selectedID.value = id },
  notice: (message) => { layoutNotice.value = message },
})

// `?job=<id>`：Sessions 页新建的 ACP 会话还没拿到 session_id 时带过来的 job；
// 与发起栏新建会话一样登记定位意图，等它出现在线程列表里再聚焦（gofer-k5d4）。
function consumeRequestedJob(): void {
  const value = route.query.job
  const jobID = typeof value === 'string' ? value : Array.isArray(value) ? value[0] ?? '' : ''
  if (!jobID) return
  const next = { ...route.query }
  delete next.job
  void router.replace({ query: next })
  submitted(jobID)
}

function locateRequestedThread(announce: boolean): boolean {
  return autoFocus.locateRequested(threadsByID.value, announce)
}

function submitted(jobID: string): void {
  autoFocus.launched(jobID)
  void loadThreads()
}

function continued(jobID?: string): void {
  autoFocus.continued(jobID)
  void loadThreads()
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
  prefixHint.value = t('workbench.prefix.hint')
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
  else if (key !== 'Escape') layoutNotice.value = t('workbench.notice.unknownKey', { key })
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
  setProject(project)
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
  () => route.query.job,
  () => {
    if (layoutReady.value) consumeRequestedJob()
  },
)


function onWideQuery(event: MediaQueryListEvent | MediaQueryList): void {
  wideLayout.value = event.matches
}

// 右侧上下文栏只在 ≥1280 显示（设计稿 §4.3：1100–1279 不显示右栏）。
function stopFocused(): void {
  if (!focusedActions.value) return
  stoppingFocused.value = true
  void focusedActions.value.stopCurrent().finally(() => { stoppingFocused.value = false })
}

function showFocusedView(view: 'process' | 'changes'): void {
  focusedView.value = view
  focusedActions.value?.showView(view)
}

function openJobDetail(id: string): void {
  void router.push(`/jobs/${encodeURIComponent(id)}`)
}

onMounted(async () => {
  window.addEventListener('keydown', onKeydown, true)
  mobileQuery = window.matchMedia('(max-width: 767px)')
  onMobileQuery(mobileQuery)
  mobileQuery.addEventListener('change', onMobileQuery)
  wideQuery = window.matchMedia('(min-width: 1280px)')
  onWideQuery(wideQuery)
  wideQuery.addEventListener('change', onWideQuery)
  const layoutPromise = getWorkbenchLayout().catch((e) => {
    error.value = e instanceof Error ? e.message : String(e)
    return null
  })
  const metaPromise = getMetaCached()
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
    layoutDocument.value = pruneThreads(
      normalize(server.body, empty ? firstThreadID : null),
      new Set(threads.value.map((thread) => thread.id)),
    )
    if (empty) markLayoutDirty()
  } else {
    layoutDocument.value = createLayoutDocument(firstThreadID)
  }
  layoutReady.value = true
  syncSelectedFromLayout()
  locateRequestedThread(true)
  consumeRequestedJob()
  poller.start()
})

onUnmounted(() => {
  poller.stop()
  if (layoutSaveTimer != null) window.clearTimeout(layoutSaveTimer)
  if (fullRefetchTimer != null) window.clearTimeout(fullRefetchTimer)
  clearPrefix()
  mobileQuery?.removeEventListener('change', onMobileQuery)
  mobileQuery = null
  wideQuery?.removeEventListener('change', onWideQuery)
  wideQuery = null
  window.removeEventListener('keydown', onKeydown, true)
  document.title = 'gofer'
})
</script>

<template>
  <div ref="workbenchRoot" class="workbench-page" tabindex="-1">
    <WorkbenchComposer ref="composer" :session-open="mobilePane === 'main'" @submitted="submitted" />
    <p v-if="error" class="page-error mono">{{ error }}</p>
    <p v-if="layoutNotice" class="layout-notice mono">
      {{ layoutNotice }}
      <button type="button" :aria-label="t('workbench.error.dismiss')" @click="layoutNotice = ''">×</button>
    </p>
    <div
      class="workbench-body"
      :class="[`mobile--${mobilePane}`, { 'workbench-body--wide': showContext && !!selectedThread }]"
      @touchstart.passive="onTouchStart"
      @touchend.passive="onTouchEnd"
    >
      <WorkbenchSidebar
        v-if="!mobileLayout"
        ref="sidebar"
        :threads="sidebarThreads"
        :show-exec="showExec"
        :hidden-exec-count="hiddenExecCount"
        :orphan-waiting="orphanWaiting"
        :selected-id="selectedID"
        :query="query"
        :project-filter="projectFilter"
        :status-filter="statusFilter"
        :project-options="projectOptions"
        @select="selectThread"
        @update:query="query = $event; refreshFilter()"
        @update:project-filter="setProject"
        @update:status-filter="statusFilter = $event; refreshFilter()"
        @update:show-exec="setShowExec"
        @new="composer?.focusPrompt()"
        @open-orphans="openOrphanWaiting"
      />
      <!-- 手机：会话列表是左侧抽屉（设计稿 §6.3） -->
      <Drawer
        v-else
        :model-value="mobilePane === 'sidebar'"
        side="left"
        width="320px"
        :label="t('workbench.mobile.list')"
        @update:model-value="mobilePane = $event ? 'sidebar' : 'main'"
      >
        <WorkbenchSidebar
          ref="sidebar"
          :threads="sidebarThreads"
          :show-exec="showExec"
          :hidden-exec-count="hiddenExecCount"
          :orphan-waiting="orphanWaiting"
          :selected-id="selectedID"
          :query="query"
          :project-filter="projectFilter"
          :status-filter="statusFilter"
          :project-options="projectOptions"
          @select="selectThread"
          @update:query="query = $event; refreshFilter()"
          @update:project-filter="setProject"
          @update:status-filter="statusFilter = $event; refreshFilter()"
          @update:show-exec="setShowExec"
          @new="composer?.focusPrompt()"
          @open-orphans="openOrphanWaiting"
        />
      </Drawer>
      <main class="workbench-main">
        <div class="tabs-row">
        <button
          v-if="mobilePane === 'main'"
          class="session-new"
          type="button"
          :aria-label="t('workbench.new.launch')"
          data-test="mobile-new-session"
          @click="composer?.focusPrompt()"
        >＋</button>
        <nav class="layout-tabs" :aria-label="t('workbench.tabs.label')">
          <div
            v-for="tab in layoutDocument.tabs"
            :key="tab.id"
            class="layout-tab"
            :class="{ active: tab.id === layoutDocument.active_tab_id }"
          >
            <input
              v-if="editingTabID === tab.id"
              v-model="tabTitleDraft"
              class="tab-title-input"
              :aria-label="t('workbench.tabs.rename')"
              @blur="finishRenameTab"
              @keydown.enter.prevent="finishRenameTab"
              @keydown.esc.prevent="editingTabID = ''"
            />
            <button
              v-else
              class="tab-select"
              type="button"
              :aria-current="tab.id === layoutDocument.active_tab_id ? 'page' : undefined"
              @click="selectTab(tab.id)"
              @dblclick="beginRenameTab(tab.id, tab.title)"
            >
              {{ tab.title }}
            </button>
            <button class="tab-close" type="button" :aria-label="t('workbench.tabs.close', { title: tab.title })" @click="removeTab(tab.id)">×</button>
          </div>
          <button class="tab-new" type="button" :title="t('workbench.tabs.new')" @click="createTab">＋</button>
          <span v-if="prefixActive" class="prefix-status mono">{{ prefixHint }}</span>
          <button
            v-if="wideLayout && !mobileLayout && selectedThread"
            class="context-toggle"
            type="button"
            :aria-pressed="contextOpen"
            :title="t(contextOpen ? 'workbench.context.hide' : 'workbench.context.show')"
            @click="toggleContext"
          >{{ t('workbench.context.toggle') }}</button>
          <span class="layout-version mono">v{{ layoutVersion }}<template v-if="layoutSaving"> · {{ t('workbench.tabs.saving') }}</template></span>
        </nav>
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
          <div v-else class="empty-main">{{ loading ? t('workbench.loading') : t('workbench.empty.start') }}</div>
        </div>
      </main>
      <!-- 右侧上下文栏（≥1280，设计稿 §7）：会话信息、轮数、未提交数、关联任务、改动文件、计划、权限 -->
      <WorkbenchContext
        v-if="showContext && selectedThread"
        :thread="selectedThread"
        :view="focusedView"
        :can-stop="!!focusedActions"
        :stopping="stoppingFocused"
        @show-view="showFocusedView"
        @stop="stopFocused"
        @open-job="openJobDetail"
      />
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
.workbench-page { height: calc(100vh - 57px); min-height: 520px; display: flex; flex-direction: column; overflow: hidden; background: var(--bg); }
.tabs-row { flex: none; display: flex; align-items: center; gap: var(--s1); padding-right: var(--s1); background: var(--surface-2); border-bottom: 1px solid var(--line); }
.tabs-row .layout-tabs { flex: 1 1 auto; border-bottom: 0; }
.session-new { display: none; }
.page-error { flex: none; margin: 0; padding: 6px var(--s3); color: var(--fail); background: var(--fail-soft, rgba(182,56,47,.08)); border-bottom: 1px solid var(--line); font-size: var(--fs-sm); }
.layout-notice { flex: none; display: flex; align-items: center; justify-content: space-between; gap: var(--s2); margin: 0; padding: 6px var(--s3); color: var(--run); background: var(--surface); border-bottom: 1px solid var(--line); font-size: var(--fs-sm); }
.layout-notice button { color: inherit; background: transparent; border: 0; font-size: 16px; cursor: pointer; }
.workbench-body { flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(260px, 24vw) minmax(0,1fr); grid-template-rows: minmax(0,1fr); }
/* ≥1280 多一列上下文栏（设计稿 §4.3；1100–1279 不显示） */
.workbench-body--wide { grid-template-columns: minmax(240px, 20vw) minmax(0,1fr) minmax(260px, 20vw); }
.workbench-main { min-width: 0; min-height: 0; overflow: hidden; display: flex; flex-direction: column; }
.layout-tabs { flex: none; min-width: 0; display: flex; align-items: stretch; gap: 2px; padding: 4px var(--s1) 0; background: var(--surface-2); border-bottom: 1px solid var(--line); overflow-x: auto; scrollbar-width: none; }
.layout-tabs::-webkit-scrollbar { display: none; }
.layout-tab { flex: none; display: flex; align-items: center; max-width: 220px; border: 1px solid transparent; border-bottom: 0; border-radius: var(--r-ctl) var(--r-ctl) 0 0; }
.layout-tab.active { background: var(--bg); border-color: var(--line); }
.tab-select, .tab-close, .tab-new { color: var(--ink-3); background: transparent; border: 0; font: inherit; font-size: var(--fs-sm); cursor: pointer; }
.tab-select { min-width: 70px; max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; padding: 6px 4px 6px var(--s2); }
.layout-tab.active .tab-select { color: var(--ink); }
.tab-close { padding: 6px 6px 6px 2px; }
.tab-new { padding: 4px var(--s2); font-size: 16px; }
.tab-select:hover, .tab-close:hover, .tab-new:hover { color: var(--accent); }
.tab-select:focus-visible, .tab-close:focus-visible, .tab-new:focus-visible { outline: 2px solid var(--focus); outline-offset: -1px; }
.tab-title-input { width: 130px; margin: 2px; padding: 2px 4px; color: var(--ink); background: var(--surface); border: 1px solid var(--accent); border-radius: var(--r-chip); font: inherit; font-size: var(--fs-sm); }
.context-toggle { margin-left: auto; align-self: center; padding: 2px 8px; border: 1px solid var(--line, currentColor); border-radius: var(--r-chip); background: transparent; color: var(--ink-2); font-size: var(--fs-xs); cursor: pointer; }
.context-toggle[aria-pressed='true'] { color: var(--accent); background: var(--accent-soft); }
.context-toggle + .layout-version { margin-left: 8px; }
.layout-version { margin-left: auto; align-self: center; padding: 0 5px; color: var(--ink-3); font-size: var(--fs-xs); white-space: nowrap; }
.prefix-status { align-self: center; margin-left: auto; color: var(--run); font-size: var(--fs-xs); white-space: nowrap; }
.prefix-status + .layout-version { margin-left: 0; }
.layout-surface { flex: 1; min-width: 0; min-height: 0; position: relative; }
.empty-main { color: var(--ink-3); padding: var(--s4); }
@media (max-width: 760px) {
  .workbench-page { height: calc(100vh - 53px); }
  .workbench-body, .workbench-body--wide { grid-template-columns: 1fr; }
  .workbench-body.mobile--sidebar .workbench-main { display: none; }
  .layout-tabs { padding-right: 48px; }
  .session-new { display: grid; place-items: center; flex: none; width: 34px; height: 34px; color: var(--bg); background: var(--accent); border: 0; border-radius: var(--r-ctl); font-size: 20px; cursor: pointer; }
  .layout-version, .prefix-status { display: none; }
}
</style>
