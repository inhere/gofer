<script setup lang="ts">
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch, type ComputedRef, type Ref } from 'vue'
import { useRouter } from 'vue-router'
import { answerInteraction, cancelJob, getJob, puntInteraction } from '../../api/client'
import { appendCapped, streamJob } from '../../api/sse'
import type { Interaction, Job, SSEEvent, SSEInteractionData, SSELogData, WorkbenchThread } from '../../api/types'
import { patchWorkbenchThread, turnWorkbenchThread } from '../../api/workbench'
import AttachTerminal from '../AttachTerminal.vue'
import UncommittedBadge from '../UncommittedBadge.vue'
import ConversationView from './ConversationView.vue'
import ThreadChangesView from './ThreadChangesView.vue'
import InteractionCard from '../InteractionCard.vue'
import LogTape from '../LogTape.vue'
import SessionDrawer from '../SessionDrawer.vue'
import { threadStatusPresentation } from './statusPresentation'

const props = defineProps<{ threadId: string; focused: boolean }>()

// 工作台只看"现在到哪了"：日志从最后 LOG_TAIL_LINES 行开始推，完整日志去 job 详情页。
const LOG_TAIL_LINES = 200

interface FocusedThreadActions {
  stopCurrent(): Promise<void>
  focusTurn(): void
  focusAction(action: string): void
}

interface WorkbenchViewContext {
  threadsByID: ComputedRef<Map<string, WorkbenchThread>>
  focusedActions: Ref<FocusedThreadActions | null>
  acpAgentKeys: Ref<Set<string>>
  acpCapabilityError: Ref<string>
  refresh(): void | Promise<void>
  continued(jobID?: string): void
  back(): void
}

const injectedContext = inject<WorkbenchViewContext>('workbench-view-context')
if (!injectedContext) throw new Error('workbench view context is required')
const context = injectedContext

const router = useRouter()
const root = ref<HTMLElement | null>(null)
const interactionArea = ref<HTMLElement | null>(null)
const turnInput = ref<HTMLTextAreaElement | null>(null)
const changesView = ref<InstanceType<typeof ThreadChangesView> | null>(null)
const activeView = ref<'process' | 'changes'>('process')
const stdout = ref('')
const stderr = ref('')
const liveStatus = ref('')
const interactions = ref<Interaction[]>([])
const streamError = ref('')
const actionError = ref('')
const submittingInteraction = ref<Set<string>>(new Set())
const draft = ref('')
const sending = ref(false)
const stopping = ref(false)
const editingTitle = ref(false)
const titleDraft = ref('')
let streamAbort: AbortController | null = null
let visibilityObserver: IntersectionObserver | null = null
let seenTimer: number | null = null
const paneVisible = ref(false)
const documentVisible = ref(document.visibilityState === 'visible')

const thread = computed(() => context.threadsByID.value.get(props.threadId))
const latestJobID = computed(() => thread.value?.latest_job_id ?? '')
const latestJob = ref<Job | null>(null)
watch([latestJobID, () => thread.value?.raw_status], async () => {
  const id = latestJobID.value
  latestJob.value = null
  if (!id) return
  try {
    const result = await getJob(id)
    if (latestJobID.value === id) latestJob.value = result
  } catch {
    // The thread remains usable if a job detail request races creation/eviction.
  }
}, { immediate: true })
const isACPThread = computed(() => {
  const current = thread.value
  return current?.kind === 'agent' && !!current.agent && context.acpAgentKeys.value.has(current.agent)
})
const rawStatus = computed(() => isACPThread.value
  ? thread.value?.raw_status ?? ''
  : liveStatus.value || thread.value?.raw_status || '')
const live = computed(() => ['queued', 'running', 'awaiting_input', 'waiting_dir', 'recovering', 'pending_interaction'].includes(rawStatus.value))
const finished = computed(() => ['done', 'failed', 'cancelled', 'timeout', 'rejected'].includes(rawStatus.value))
const canTurn = computed(() => thread.value?.kind === 'agent' && thread.value.resumable &&
  (finished.value || (!!latestJob.value?.session && rawStatus.value === 'awaiting_input')) && !sending.value)
const oneShot = computed(() => thread.value?.kind === 'job' && !thread.value.resumable)

function resetForThread(): void {
  streamAbort?.abort()
  streamAbort = null
  stdout.value = ''
  stderr.value = ''
  const current = thread.value
  liveStatus.value = current?.raw_status ?? ''
  interactions.value = [...(current?.pending_interactions ?? [])]
  streamError.value = ''
  actionError.value = ''
  draft.value = ''
  titleDraft.value = current?.title ?? ''
  activeView.value = 'process'
  if (current?.kind !== 'relay' && !isACPThread.value && latestJobID.value) void startStream(latestJobID.value)
}

function onEvent(event: SSEEvent): void {
  if (event.type === 'status') {
    const value = event.data as Job
    liveStatus.value = value.status
    if (value.id === latestJobID.value) latestJob.value = value
    void context.refresh()
    return
  }
  if (event.type === 'log') {
    const value = event.data as SSELogData
    if (value.stream === 'stdout') stdout.value = appendCapped(stdout.value, value.text)
    else stderr.value = appendCapped(stderr.value, value.text)
    return
  }
  if (event.type === 'log-rotated') {
    const value = event.data as { stream: 'stdout' | 'stderr' }
    if (value.stream === 'stdout') stdout.value = ''
    else stderr.value = ''
    return
  }
  if (event.type === 'interaction') {
    const value = (event.data as SSEInteractionData).interaction
    const index = interactions.value.findIndex((item) => item.id === value.id)
    if (value.status === 'pending') {
      if (index >= 0) interactions.value.splice(index, 1, value)
      else interactions.value.push(value)
    } else if (index >= 0) {
      interactions.value.splice(index, 1)
    }
    void context.refresh()
  }
}

async function startStream(jobID: string): Promise<void> {
  const ctrl = new AbortController()
  streamAbort = ctrl
  try {
    await streamJob(jobID, { tail: LOG_TAIL_LINES, signal: ctrl.signal, onEvent })
  } catch (e) {
    if (!ctrl.signal.aborted) streamError.value = e instanceof Error ? e.message : String(e)
  }
}

function setInteractionBusy(id: string, busy: boolean): void {
  const next = new Set(submittingInteraction.value)
  if (busy) next.add(id)
  else next.delete(id)
  submittingInteraction.value = next
}

async function answer(item: Interaction, value: string): Promise<void> {
  setInteractionBusy(item.id, true)
  actionError.value = ''
  try {
    await answerInteraction(item.job_id, item.id, value)
    interactions.value = interactions.value.filter((candidate) => candidate.id !== item.id)
    void context.refresh()
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    setInteractionBusy(item.id, false)
  }
}

async function punt(item: Interaction): Promise<void> {
  setInteractionBusy(item.id, true)
  actionError.value = ''
  try {
    await puntInteraction(item.job_id, item.id)
    void context.refresh()
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    setInteractionBusy(item.id, false)
  }
}

async function sendTurn(): Promise<void> {
  const text = draft.value.trim()
  if (!text || !canTurn.value) return
  sending.value = true
  actionError.value = ''
  try {
    const current = thread.value
    if (!current) return
    const result = await turnWorkbenchThread(current.id, text)
    draft.value = ''
    context.continued(result.job_id)
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    sending.value = false
  }
}

function onTurnKeydown(event: KeyboardEvent): void {
  if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
    event.preventDefault()
    void sendTurn()
  }
}

async function saveTitle(): Promise<void> {
  editingTitle.value = false
  const current = thread.value
  if (!current || titleDraft.value.trim() === current.title) return
  try {
    await patchWorkbenchThread(current.id, { title: titleDraft.value.trim() })
    void context.refresh()
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  }
}

async function togglePin(): Promise<void> {
  const current = thread.value
  if (!current) return
  try {
    await patchWorkbenchThread(current.id, { pinned: !current.pinned })
    void context.refresh()
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  }
}

async function stopCurrent(): Promise<void> {
  if (!latestJobID.value || !live.value || stopping.value) return
  if (!window.confirm(`停止 job ${latestJobID.value}？关闭视图不会停止，只有这个动作会取消执行。`)) return
  stopping.value = true
  actionError.value = ''
  try {
    await cancelJob(latestJobID.value)
    void context.refresh()
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    stopping.value = false
  }
}

function openDetails(): void {
  if (latestJobID.value) void router.push(`/jobs/${encodeURIComponent(latestJobID.value)}`)
}

function showChanges(): void {
  if (thread.value?.kind !== 'relay' && latestJobID.value) activeView.value = 'changes'
}

function openDiff(path: string): void {
  showChanges()
  void nextTick(() => changesView.value?.focusFile(path))
}

function filesChanged(): void {
  void changesView.value?.refresh()
}

function reviewContinued(jobID?: string): void {
  activeView.value = 'process'
  context.continued(jobID)
}

function usageText(): string {
  const total = thread.value?.usage?.total_tokens ?? 0
  const cost = thread.value?.usage?.cost_usd ?? 0
  if (!total && !cost) return '—'
  return `${total.toLocaleString()} tok${cost ? ` · $${cost.toFixed(4)}` : ''}`
}

function focusTurn(): void {
  turnInput.value?.focus()
}

function focusAction(action: string): void {
  void nextTick(() => {
    if (action === 'answer') {
      const target = interactionArea.value ?? root.value?.querySelector<HTMLElement>('[data-interaction-area]')
      target?.scrollIntoView({ block: 'start', behavior: 'smooth' })
    }
    else if (action === 'reply') root.value?.querySelector<HTMLElement>('textarea')?.focus()
    else root.value?.focus()
  })
}

function clearSeenTimer(): void {
  if (seenTimer != null) {
    window.clearTimeout(seenTimer)
    seenTimer = null
  }
}

function canMarkSeen(): boolean {
  return props.focused && paneVisible.value && documentVisible.value && !!thread.value?.id
}

function scheduleSeen(): void {
  clearSeenTimer()
  if (!canMarkSeen()) return
  const threadID = thread.value?.id
  seenTimer = window.setTimeout(() => {
    seenTimer = null
    if (!threadID || !canMarkSeen() || thread.value?.id !== threadID) return
    void patchWorkbenchThread(threadID, { seen: true })
      .then(() => context.refresh())
      .catch((e) => { actionError.value = e instanceof Error ? e.message : String(e) })
  }, 2000)
}

function onDocumentVisibility(): void {
  documentVisible.value = document.visibilityState === 'visible'
}

defineExpose({ stopCurrent, focusTurn, focusAction })

const exposedActions: FocusedThreadActions = { stopCurrent, focusTurn, focusAction }
watch(() => props.focused, (focused) => {
  if (focused) context.focusedActions.value = exposedActions
  else if (context.focusedActions.value === exposedActions) context.focusedActions.value = null
}, { immediate: true })
watch([() => props.threadId, () => thread.value?.latest_job_id, isACPThread], resetForThread, { immediate: true })
watch(
  () => thread.value?.pending_interactions,
  (items) => { interactions.value = [...(items ?? [])] },
  { deep: true },
)
watch([() => props.focused, () => thread.value?.id, paneVisible, documentVisible], scheduleSeen, { immediate: true })
onMounted(() => {
  document.addEventListener('visibilitychange', onDocumentVisibility)
  void nextTick(() => {
    if (!root.value) return
    if (typeof IntersectionObserver === 'undefined') {
      paneVisible.value = true
      return
    }
    visibilityObserver = new IntersectionObserver((entries) => {
      paneVisible.value = entries.some((entry) => entry.isIntersecting && entry.intersectionRatio > 0)
    }, { threshold: [0, 0.01] })
    visibilityObserver.observe(root.value)
  })
})
onUnmounted(() => {
  streamAbort?.abort()
  clearSeenTimer()
  visibilityObserver?.disconnect()
  document.removeEventListener('visibilitychange', onDocumentVisibility)
  if (context.focusedActions.value === exposedActions) context.focusedActions.value = null
})
</script>

<template>
  <section v-if="thread" ref="root" class="thread-pane" tabindex="-1">
    <header class="thread-head">
      <button class="back mono" type="button" @click="context.back()">← 会话</button>
      <div class="title-wrap">
        <input
          v-if="editingTitle"
          v-model="titleDraft"
          class="title-input"
          aria-label="重命名会话"
          @blur="saveTitle"
          @keydown.enter.prevent="saveTitle"
          @keydown.esc.prevent="editingTitle = false; titleDraft = thread.title"
        />
        <button v-else class="thread-title" type="button" title="点击重命名" @click="editingTitle = true">{{ thread.title }}</button>
        <span class="status mono" :class="`status--${threadStatusPresentation(thread.status).tone}`">{{ threadStatusPresentation(thread.status).label }}<template v-if="thread.stalled"> · stalled</template></span>
        <UncommittedBadge :count="latestJob?.uncommitted_count" :files="latestJob?.uncommitted_files" />
      </div>
      <div class="head-actions mono">
        <button type="button" @click="togglePin">{{ thread.pinned ? '取消置顶' : '置顶' }}</button>
        <button type="button" :disabled="!live || stopping" @click="stopCurrent">{{ stopping ? '停止中…' : '停止' }}</button>
        <button type="button" :disabled="!latestJobID" @click="openDetails">打开 job 详情</button>
      </div>
      <div class="thread-meta mono">
        <span>{{ thread.agent || thread.kind }}</span><span>·</span>
        <span>{{ thread.project_key || '未归属' }}</span><span>·</span>
        <span :title="thread.cwd">{{ thread.cwd || '—' }}</span><span>·</span>
        <span>{{ thread.turns }} turn(s)</span><span>·</span><span>{{ usageText() }}</span>
      </div>
    </header>

    <nav class="thread-subviews mono" aria-label="会话子视图">
      <button type="button" :aria-current="activeView === 'process' ? 'page' : undefined" @click="activeView = 'process'">过程</button>
      <span>｜</span>
      <button
        type="button"
        :disabled="thread.kind === 'relay' || !latestJobID"
        :aria-current="activeView === 'changes' ? 'page' : undefined"
        @click="showChanges"
      >改动</button>
    </nav>

    <p v-if="actionError" class="error mono">{{ actionError }}</p>
    <p v-if="context.acpCapabilityError.value" class="stream-error mono">
      ACP 能力读取失败，暂用日志视图：{{ context.acpCapabilityError.value }}
    </p>
    <div v-if="!isACPThread" v-show="activeView === 'process'" ref="interactionArea" class="interaction-area">
      <InteractionCard
        v-for="item in interactions"
        :key="item.id"
        :interaction="item"
        :submitting="submittingInteraction.has(item.id)"
        @answer="answer(item, $event)"
        @punt="punt(item)"
      />
    </div>

    <div class="thread-content">
      <div v-show="activeView === 'process'" class="process-view">
        <SessionDrawer
          v-if="thread.kind === 'relay' && thread.relay"
          :sid="thread.relay.session_id"
          :thread-id="thread.id"
          embedded
          @changed="context.refresh()"
        />
        <AttachTerminal
          v-else-if="thread.interactive && latestJobID"
          :job-id="latestJobID"
          mode="write"
          :focused="focused && activeView === 'process'"
          @exit="context.refresh()"
          @error="actionError = $event"
        />
        <ConversationView
          v-else-if="isACPThread && latestJobID"
          :thread-id="thread.id"
          :job-ids="thread.job_ids ?? [latestJobID]"
          :jobs="thread.jobs ?? []"
          :latest-job-id="latestJobID"
          :latest-running="live"
          :continuous-session="!!latestJob?.session"
          :pending-interactions="interactions"
          :submitting-interaction="submittingInteraction"
          :focused="focused && activeView === 'process'"
          @answer="answer"
          @punt="punt"
          @ended="context.refresh()"
          @error="actionError = $event"
          @open-diff="openDiff"
          @files-changed="filesChanged"
        />
        <div v-else-if="latestJobID" class="log-wrap">
          <p v-if="streamError" class="stream-error mono">SSE：{{ streamError }}</p>
          <p class="log-tail-note mono">
            只加载每条流最近 {{ LOG_TAIL_LINES }} 行 ·
            <button type="button" class="link-btn" @click="openDetails">完整日志见 job 详情</button>
          </p>
          <LogTape :stdout="stdout" :stderr="stderr" :live="live" mode="live" :focused="focused && activeView === 'process'" :auto-stderr="false" />
        </div>
        <p v-else class="empty mono">这个会话没有关联 job。</p>
      </div>
      <ThreadChangesView
        v-if="thread.kind !== 'relay' && latestJobID"
        v-show="activeView === 'changes'"
        ref="changesView"
        :thread-id="thread.id"
        :latest-job-id="latestJobID"
        :raw-status="rawStatus"
        :working="live"
        :can-review="canTurn"
        @continued="reviewContinued"
        @changed="context.refresh()"
        @error="actionError = $event"
      />
    </div>

    <footer v-if="activeView === 'process' && thread.kind !== 'relay' && !thread.interactive" class="turn-composer">
      <textarea
        ref="turnInput"
        v-model="draft"
        class="turn-input mono"
        rows="3"
        :disabled="!canTurn"
        :placeholder="oneShot ? '该一次性批处理没有 session，无法续接' : canTurn ? '下一句…（Ctrl/Cmd+Enter）' : live ? '当前 job 仍在运行，结束后可继续会话' : '下一句…（Ctrl/Cmd+Enter）'"
        @keydown="onTurnKeydown"
      ></textarea>
      <button class="turn-send mono" type="button" :disabled="!canTurn || !draft.trim()" @click="sendTurn">{{ sending ? '发送中…' : '发送' }}</button>
    </footer>
  </section>
  <section v-else class="thread-pane missing-thread mono">
    该会话当前不在工作台列表中；清除筛选或等待下一轮刷新。
  </section>
</template>

<style scoped>
.thread-pane { width: 100%; height: 100%; min-height: 0; display: flex; flex-direction: column; color: var(--paper); outline: none; }
.thread-head { flex: none; display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 8px 14px; padding: 10px 14px; background: var(--panel); border-bottom: 1px solid var(--line); }
.back { display: none; }
.title-wrap { min-width: 0; display: flex; align-items: center; gap: 9px; }
.thread-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--paper); background: transparent; border: 0; padding: 0; font-size: 16px; font-weight: 650; text-align: left; }
.title-input { min-width: 0; flex: 1; color: var(--paper); background: var(--ink); border: 1px solid var(--phosphor); border-radius: var(--radius); padding: 5px 7px; font-size: 15px; }
.status { flex: none; border: 1px solid var(--line); border-radius: 10px; padding: 2px 7px; color: var(--queue); font-size: 10px; }
.status--attention { color: var(--fail); border-color: var(--fail); }
.status--active { color: var(--phosphor); border-color: var(--phosphor); }
.status--done { color: var(--done); border-color: var(--done); }
.head-actions { display: flex; gap: 6px; }
.head-actions button, .back { color: var(--paper); background: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: 5px 8px; }
.head-actions button:disabled { opacity: .4; }
.thread-meta { grid-column: 1 / -1; display: flex; gap: 7px; min-width: 0; overflow: hidden; color: var(--queue); font-size: 11px; }
.thread-meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.thread-subviews { flex: none; display: flex; align-items: center; gap: 5px; padding: 5px 14px; border-bottom: 1px solid var(--line); background: var(--panel); color: var(--queue); }
.thread-subviews button { padding: 2px 5px; border: 0; background: transparent; color: var(--queue); }
.thread-subviews button[aria-current="page"] { color: var(--phosphor); font-weight: 700; }
.thread-subviews button:disabled { opacity: .35; }
.error, .stream-error { flex: none; margin: 0; padding: 7px 12px; color: var(--fail); background: rgba(200,70,70,.07); }
.interaction-area { flex: none; max-height: 40%; overflow-y: auto; padding: 0 12px; }
.interaction-area:empty { display: none; }
.thread-content { flex: 1; min-height: 0; overflow: hidden; }
.thread-content > :deep(*) { height: 100%; }
.process-view { height: 100%; min-height: 0; }
.process-view > :deep(*) { height: 100%; }
.log-wrap { height: 100%; display: flex; flex-direction: column; padding: 10px; }
.log-wrap :deep(.tape) { flex: 1; min-height: 0; }
.turn-composer { flex: none; display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 8px; padding: 10px 12px; background: var(--panel); border-top: 1px solid var(--line); }
.turn-input { resize: vertical; color: var(--paper); background: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: 8px; }
.turn-input:focus { outline: 1px solid var(--phosphor); border-color: var(--phosphor); }
.turn-send { color: var(--ink); background: var(--phosphor); border: 1px solid var(--phosphor); border-radius: var(--radius); padding: 0 16px; font-weight: 700; }
.turn-send:disabled { opacity: .4; }
.empty { color: var(--queue); padding: 24px; }
.missing-thread { display: grid; place-items: center; padding: 24px; color: var(--queue); }
@media (max-width: 767px) { .thread-head { grid-template-columns: 1fr; } .back { display: inline-block; justify-self: start; } .head-actions { flex-wrap: wrap; } .thread-meta { grid-column: 1; } .turn-composer { grid-template-columns: 1fr; } .turn-send { min-height: 36px; } }
.log-tail-note { margin: 0; padding: 4px 10px; font-size: 11px; color: var(--muted, #8a97a3); }
.link-btn { padding: 0; border: 0; background: none; color: var(--phosphor); cursor: pointer; font: inherit; text-decoration: underline; }
</style>
