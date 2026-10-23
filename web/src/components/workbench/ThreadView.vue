<script setup lang="ts">
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch, type ComputedRef, type Ref } from 'vue'
import { useRouter } from 'vue-router'
import { answerInteraction, cancelJob, getJob, puntInteraction } from '../../api/client'
import { appendCapped, streamJob } from '../../api/sse'
import { canCancelJob } from '../../utils/jobCancel'
import type { Interaction, Job, SSEEvent, SSEInteractionData, SSELogData, WorkbenchThread } from '../../api/types'
import { patchWorkbenchThread, turnWorkbenchThread } from '../../api/workbench'
import { attachmentCapability } from '../../utils/attachments'
import { formatNumber, formatRelative } from '../../utils/format'
import { t } from '../../i18n'
import AttachmentBar from '../AttachmentBar.vue'
import AttachTerminal from '../AttachTerminal.vue'
import UncommittedBadge from '../UncommittedBadge.vue'
import ConversationView from './ConversationView.vue'
import ThreadChangesView from './ThreadChangesView.vue'
import { clearSendQueue, readSendQueue, writeSendQueue } from './sendQueue'
import InteractionCard from '../InteractionCard.vue'
import LogTape from '../LogTape.vue'
import SessionDrawer from '../SessionDrawer.vue'
import StatusPill from '../../ui/StatusPill.vue'

const props = defineProps<{ threadId: string; focused: boolean }>()

// 工作台只看"现在到哪了"：日志从最后 LOG_TAIL_LINES 行开始推，完整日志去 job 详情页。
const LOG_TAIL_LINES = 200

// 发送模式（设计稿 §7）：直接发送 / 排队等这一轮结束 / 打断当前 job 再发送。
type SendMode = 'send' | 'queue' | 'interrupt'

interface FocusedThreadActions {
  stopCurrent(): Promise<void>
  focusTurn(): void
  showView(view: 'process' | 'changes'): void
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
const turnInput = ref<HTMLTextAreaElement | null>(null)
const sessionDrawer = ref<InstanceType<typeof SessionDrawer> | null>(null)
const menuOpen = ref(false)
const embeddedRelayModes = ['auto', 'on', 'off'] as const
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
const sendMode = ref<SendMode>('send')
// 排队：等这一轮结束再发（设计稿 §7「排队」）；打断：先取消当前 job 再发。
// 队列按会话持久化到 localStorage（review）：刷新 / 重开页面后恢复，发出或丢弃后清除。
const queued = ref<string[]>([])
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
// 「停止」= 取消 job，口径同 job 详情页的取消按钮（gofer-83fe）：排队中、待批等非终态都能停。
const canStop = computed(() => canCancelJob(rawStatus.value))
const canTurn = computed(() => thread.value?.kind === 'agent' && thread.value.resumable &&
  (finished.value || (!!latestJob.value?.session && rawStatus.value === 'awaiting_input')) && !sending.value)
// 附图（gofer-ha0x）：能力位由 thread 带（inline / file 能用，none / 缺失置灰）；
// 上传中或还有失败项时不让发送，免得发出缺图的消息。
const attachCapability = computed(() => attachmentCapability(thread.value))
const attachBar = ref<InstanceType<typeof AttachmentBar> | null>(null)
const attachHint = computed(() => attachBar.value?.hint() ?? '')
const attachBlocked = computed(() => !!attachBar.value && !attachBar.value.canSend())
const canSendTurn = computed(() => canTurn.value && !attachBlocked.value)
const oneShot = computed(() => thread.value?.kind === 'job' && !thread.value.resumable)
const hasQueued = computed(() => queued.value.length > 0)

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
  // 待发队列按会话存在 localStorage 里：切回来（或刷新后重开）还能看到
  queued.value = current ? readSendQueue(current.id) : []
  // 换会话（或判断出别的线程）时丢掉上一轮的附图：它们的责任线程已经不是当前这个。
  attachBar.value?.clear()
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

async function deliver(text: string): Promise<void> {
  const current = thread.value
  if (!current) return
  sending.value = true
  actionError.value = ''
  try {
    const attachmentIDs = attachBar.value?.readyIDs() ?? []
    const result = await turnWorkbenchThread(current.id, text, attachmentIDs)
    // 发送成功才清空附件条（失败时图还在，可直接重试）。
    attachBar.value?.clear()
    context.continued(result.job_id)
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    sending.value = false
  }
}

async function submitTurn(): Promise<void> {
  const text = draft.value.trim()
  if (!text) return
  if (sendMode.value === 'queue' && !canTurn.value) {
    queued.value = [...queued.value, text]
    draft.value = ''
    return
  }
  if (sendMode.value === 'interrupt' && !canTurn.value) {
    // 打断后发送：先取消当前 job，把这条放进队首，等线程空闲时自动发出。
    if (!canStop.value || stopping.value) {
      actionError.value = t('workbench.composer.running')
      return
    }
    queued.value = [text, ...queued.value]
    draft.value = ''
    await stopCurrent()
    return
  }
  if (!canSendTurn.value) return
  await deliver(text)
  if (!actionError.value) draft.value = ''
}

// 队列只管「现在发不了」的消息：线程一有空就按顺序发出。
watch(canTurn, (ready) => {
  if (!ready || sending.value || queued.value.length === 0) return
  const [next, ...rest] = queued.value
  queued.value = rest
  void deliver(next)
})

// 每次变动写回 storage（空队列则删除该键）：发出 / 丢弃 / 入队都就地落盘。
watch(queued, (items) => {
  const id = thread.value?.id
  if (id) writeSendQueue(id, items)
})

function clearQueue(): void {
  queued.value = []
  const id = thread.value?.id
  if (id) clearSendQueue(id)
}

function onTurnKeydown(event: KeyboardEvent): void {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
    event.preventDefault()
    void submitTurn()
  }
}

function autoGrowTurn(event: Event): void {
  const el = event.target as HTMLTextAreaElement
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, window.innerHeight * 0.4)}px`
}

function refreshEmbeddedSession(): void {
  void sessionDrawer.value?.load()
}

function setEmbeddedRelay(mode: 'auto' | 'on' | 'off'): void {
  void sessionDrawer.value?.setRelayMode(mode)
}

function removeEmbeddedSession(): void {
  void sessionDrawer.value?.remove()
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
  if (!latestJobID.value || !canStop.value || stopping.value) return
  if (!window.confirm(t('workbench.head.stopConfirm', { id: latestJobID.value }))) return
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

function showProcess(): void {
  activeView.value = 'process'
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
  return `${formatNumber(total)} tok${cost ? ` · $${cost.toFixed(4)}` : ''}`
}

function focusTurn(): void {
  turnInput.value?.focus()
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

defineExpose({ stopCurrent, focusTurn, showView: (view: 'process' | 'changes') => { if (view === 'changes') showChanges(); else showProcess() } })

const exposedActions: FocusedThreadActions = {
  stopCurrent,
  focusTurn,
  showView: (view) => { if (view === 'changes') showChanges(); else showProcess() },
}
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
    <header class="conv-head">
      <button class="icon-btn back" type="button" :aria-label="t('workbench.mobile.list')" @click="context.back()">
        ☰
      </button>
      <div class="ttl">
        <input
          v-if="editingTitle"
          v-model="titleDraft"
          class="title-input"
          :aria-label="t('workbench.head.rename')"
          @blur="saveTitle"
          @keydown.enter.prevent="saveTitle"
          @keydown.esc.prevent="editingTitle = false; titleDraft = thread.title"
        />
        <button
          v-else
          class="thread-title"
          type="button"
          :title="t('workbench.head.renameHint')"
          @click="editingTitle = true"
        >{{ thread.title }}</button>
        <div class="meta-head">
          <StatusPill :status="thread.status" />
          <span class="tag">{{ thread.project_key || t('workbench.list.unassigned') }}</span>
          <span v-if="thread.turns > 1" class="muted">{{ t('workbench.row.turns', { n: thread.turns }) }}</span>
          <UncommittedBadge :count="latestJob?.uncommitted_count" :files="latestJob?.uncommitted_files" />
          <span v-if="thread.stalled" class="muted">· {{ t('workbench.stalled') }}</span>
          <span class="muted mono desk-only">{{ thread.agent || thread.kind }}<template v-if="thread.runner"> · {{ thread.runner }}</template></span>
        </div>
      </div>
      <nav class="seg desk-only" :aria-label="t('workbench.view.label')">
        <button
          type="button"
          :aria-pressed="activeView === 'process'"
          :class="{ on: activeView === 'process' }"
          @click="showProcess"
        >{{ t('workbench.view.process') }}</button>
        <button
          type="button"
          :aria-pressed="activeView === 'changes'"
          :class="{ on: activeView === 'changes' }"
          :disabled="thread.kind === 'relay' || !latestJobID"
          @click="showChanges"
        >{{ t('workbench.view.changes') }}</button>
      </nav>
      <button class="btn sm desk-only" type="button" :disabled="!latestJobID" @click="openDetails">
        {{ t('workbench.head.detail') }}
      </button>
      <button
        class="icon-btn"
        type="button"
        :title="thread.pinned ? t('workbench.head.unpin') : t('workbench.head.pin')"
        :aria-label="thread.pinned ? t('workbench.head.unpin') : t('workbench.head.pin')"
        :aria-pressed="!!thread.pinned"
        @click="togglePin"
      >⌖</button>
      <button
        class="icon-btn stop"
        type="button"
        :title="t('workbench.head.stop')"
        :aria-label="t('workbench.head.stop')"
        :disabled="!canStop || stopping"
        @click="stopCurrent"
      >■</button>
      <button class="icon-btn mobile-menu" type="button" :aria-label="t('workbench.head.more')" @click="menuOpen = !menuOpen">⋯</button>
      <div class="head-actions" :class="{ 'head-actions--open': menuOpen }">
        <button type="button" @click="togglePin">{{ thread.pinned ? t('workbench.head.unpin') : t('workbench.head.pin') }}</button>
        <button type="button" :disabled="!canStop || stopping" @click="stopCurrent">{{ stopping ? t('workbench.head.stopping') : t('workbench.head.stop') }}</button>
        <button type="button" :disabled="!latestJobID" @click="openDetails">{{ t('workbench.head.detail') }}</button>
        <template v-if="thread.kind === 'relay'">
          <button type="button" @click="refreshEmbeddedSession">{{ t('workbench.relay.refresh') }}</button>
          <button v-for="mode in embeddedRelayModes" :key="mode" type="button" @click="setEmbeddedRelay(mode)">{{ t(`workbench.relay.${mode}`) }}</button>
          <button type="button" @click="removeEmbeddedSession">{{ t('workbench.relay.remove') }}</button>
        </template>
        <div class="mobile-meta">
          {{ thread.agent || thread.kind }} · {{ thread.project_key || t('workbench.list.unassigned') }} · {{ thread.cwd || '—' }} · {{ t('workbench.row.turns', { n: thread.turns }) }} · {{ usageText() }}
        </div>
      </div>
    </header>

    <div class="meta-row mono">
      <span :title="thread.cwd">{{ thread.cwd || '—' }}</span>
      <span>·</span>
      <span>{{ t('workbench.row.turns', { n: thread.turns }) }}</span>
      <span>·</span>
      <span>{{ usageText() }}</span>
      <span v-if="thread.updated_at">·</span>
      <span v-if="thread.updated_at">{{ formatRelative(thread.updated_at) }}</span>
    </div>

    <p v-if="actionError" class="error mono">{{ actionError }}</p>
    <p v-if="context.acpCapabilityError.value" class="stream-error mono">
      {{ t('workbench.acp.capError', { msg: context.acpCapabilityError.value }) }}
    </p>
    <div v-if="!isACPThread" v-show="activeView === 'process'" class="interaction-area">
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
        <!-- 中继等待气泡内提供「无需回复」，不结束 Stop hook 等待。 -->
        <SessionDrawer
          v-if="thread.kind === 'relay' && thread.relay"
          :sid="thread.relay.session_id"
          :thread-id="thread.id"
          :attachment-capability="attachCapability"
           embedded
           @changed="context.refresh()"
           @deleted="context.refresh()"
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
            {{ t('workbench.log.tail', { n: LOG_TAIL_LINES }) }} ·
            <button type="button" class="link-btn" @click="openDetails">{{ t('workbench.log.full') }}</button>
          </p>
          <LogTape :stdout="stdout" :stderr="stderr" :live="live" mode="live" :focused="focused && activeView === 'process'" :auto-stderr="false" />
        </div>
        <p v-else class="empty mono">{{ t('workbench.empty.noJob') }}</p>
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

    <footer
      v-if="activeView === 'process' && thread.kind !== 'relay' && !thread.interactive"
      class="turn-composer"
      @dragover="attachBar?.handleDragOver($event)"
      @drop="attachBar?.handleDrop($event)"
    >
      <div class="turn-attach">
        <AttachmentBar
          ref="attachBar"
          :capability="attachCapability"
          @error="actionError = $event"
        />
        <p v-if="attachHint" class="turn-hint mono">{{ attachHint }}</p>
      </div>
      <div class="composer-box">
        <textarea
          ref="turnInput"
          v-model="draft"
          class="turn-input"
          rows="2"
          :disabled="oneShot"
          :placeholder="oneShot ? t('workbench.composer.oneShot') : canTurn ? t('workbench.composer.placeholder') : live ? t('workbench.composer.running') : t('workbench.composer.placeholder')"
          @keydown="onTurnKeydown"
          @input="autoGrowTurn"
          @paste="attachBar?.addFromPaste($event)"
        ></textarea>
        <div class="composer-row">
          <button
            class="icon-btn"
            type="button"
            :title="t('workbench.composer.attach')"
            :aria-label="t('workbench.composer.attach')"
            :disabled="!attachCapability.enabled"
            @click="attachBar?.openPicker()"
          >📎</button>
          <div class="seg mode-seg" role="group" :aria-label="t('act.filter')">
            <button
              v-for="mode in (['send', 'queue', 'interrupt'] as SendMode[])"
              :key="mode"
              type="button"
              :aria-pressed="sendMode === mode"
              :class="{ on: sendMode === mode }"
              :title="t(`workbench.composer.mode.${mode}Title`)"
              :data-mode="mode"
              @click="sendMode = mode"
            >{{ t(`workbench.composer.mode.${mode}`) }}</button>
          </div>
          <span v-if="hasQueued" class="queue-chip">
            {{ t('workbench.composer.queued', { n: queued.length }) }}
            <button type="button" class="link-btn" @click="clearQueue">{{ t('workbench.composer.clearQueue') }}</button>
          </span>
          <span class="grow"></span>
          <button class="btn primary sm" type="button" :disabled="!draft.trim() || (sendMode === 'send' && !canSendTurn)" @click="submitTurn">
            {{ sending ? t('workbench.composer.sending') : t('workbench.composer.send') }}
          </button>
        </div>
      </div>
    </footer>
  </section>
  <section v-else class="thread-pane missing-thread mono">
    {{ t('workbench.missing') }}
  </section>
</template>

<style scoped>
.thread-pane {
  width: 100%;
  height: 100%;
  min-height: 0;
  display: flex;
  flex-direction: column;
  color: var(--fg);
  background: var(--bg);
  outline: none;
}
.conv-head {
  flex: none;
  display: flex;
  align-items: center;
  gap: var(--s2);
  padding: var(--s2) var(--s3);
  background: var(--surface);
  border-bottom: 1px solid var(--line);
}
.back {
  display: none;
}
.icon-btn {
  flex: none;
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--fg-2);
  font: inherit;
  cursor: pointer;
}
.icon-btn:hover {
  color: var(--fg);
  border-color: var(--line-strong);
}
.icon-btn:disabled {
  opacity: 0.4;
  cursor: default;
}
.icon-btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.stop {
  color: var(--fail);
}
.ttl {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.thread-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 0;
  background: none;
  border: 0;
  color: var(--fg);
  font: inherit;
  font-size: var(--fs-md);
  font-weight: 600;
  text-align: left;
  cursor: text;
}
.thread-title:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.title-input {
  min-width: 0;
  flex: 1;
  padding: 2px var(--s1);
  color: var(--fg);
  background: var(--surface-2);
  border: 1px solid var(--accent);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-md);
}
.meta-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s1) var(--s2);
  min-width: 0;
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.tag {
  padding: 0 5px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--fg-2);
  white-space: nowrap;
}
.muted {
  color: var(--fg-3);
}
.meta-row {
  flex: none;
  display: flex;
  align-items: center;
  gap: var(--s1);
  padding: 4px var(--s3);
  color: var(--fg-3);
  font-size: var(--fs-xs);
  overflow: hidden;
  border-bottom: 1px solid var(--line);
}
.meta-row span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.seg {
  flex: none;
  display: inline-flex;
  padding: 2px;
  background: var(--sunken);
  border-radius: var(--r-ctl);
}
.seg button {
  min-height: 26px;
  padding: 0 var(--s2);
  background: none;
  border: 0;
  border-radius: var(--r-chip);
  color: var(--fg-3);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
  white-space: nowrap;
}
.seg button:hover {
  color: var(--fg);
}
.seg button.on {
  background: var(--surface);
  color: var(--fg);
  box-shadow: var(--shadow-1);
}
.seg button:disabled {
  opacity: 0.4;
  cursor: default;
}
.seg button:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.head-actions {
  display: none;
}
.mobile-menu {
  display: none;
}
.error,
.stream-error {
  flex: none;
  margin: 0;
  padding: var(--s1) var(--s3);
  color: var(--fail);
  background: var(--fail-soft, rgba(182, 56, 47, 0.08));
  font-size: var(--fs-sm);
}
.interaction-area {
  flex: none;
  max-height: 40%;
  overflow-y: auto;
  padding: 0 var(--s3);
}
.interaction-area:empty {
  display: none;
}
.thread-content {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}
.thread-content > :deep(*) {
  height: 100%;
}
.process-view {
  height: 100%;
  min-height: 0;
}
.process-view > :deep(*) {
  height: 100%;
}
.log-wrap {
  height: 100%;
  display: flex;
  flex-direction: column;
  padding: var(--s2);
}
.log-wrap :deep(.tape) {
  flex: 1;
  min-height: 0;
}
.turn-composer {
  flex: none;
  display: flex;
  flex-direction: column;
  gap: var(--s1);
  padding: var(--s2) var(--s3) var(--s3);
  background: var(--surface);
  border-top: 1px solid var(--line);
}
.turn-attach {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s2);
}
.turn-hint {
  margin: 0;
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.composer-box {
  border: 1px solid var(--line-strong);
  border-radius: var(--r-panel);
  background: var(--surface-2);
  padding: var(--s2);
}
.composer-box:focus-within {
  border-color: var(--accent);
  outline: 1px solid var(--focus);
}
.turn-input {
  width: 100%;
  resize: vertical;
  min-height: 48px;
  max-height: 40vh;
  padding: 0;
  color: var(--fg);
  background: transparent;
  border: 0;
  font: inherit;
  font-size: var(--fs-sm);
  outline: none;
}
.composer-row {
  display: flex;
  align-items: center;
  gap: var(--s1);
  margin-top: var(--s1);
}
.grow {
  flex: 1;
}
.queue-chip {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  padding: 0 var(--s1);
  color: var(--need-ink, var(--need));
  font-size: var(--fs-xs);
}
.empty {
  color: var(--fg-3);
  padding: var(--s5);
}
.missing-thread {
  display: grid;
  place-items: center;
  padding: var(--s5);
  color: var(--fg-3);
}
.link-btn {
  padding: 0;
  border: 0;
  background: none;
  color: var(--accent);
  cursor: pointer;
  font: inherit;
  text-decoration: underline;
}
@media (max-width: 1279px) {
  .desk-only {
    display: none;
  }
}
@media (max-width: 760px) {
  .conv-head {
    gap: var(--s1);
    padding: var(--s1) var(--s2);
  }
  .back {
    display: grid;
  }
  .thread-title {
    font-size: var(--fs-sm);
  }
  .meta-row {
    display: none;
  }
  .mobile-menu {
    display: grid;
  }
  .head-actions {
    display: none;
    position: absolute;
    z-index: 5;
    top: 48px;
    right: var(--s2);
    flex-direction: column;
    align-items: stretch;
    gap: 2px;
    min-width: 160px;
    padding: var(--s1);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-panel);
    box-shadow: var(--shadow-pop);
  }
  .head-actions--open {
    display: flex;
  }
  .head-actions button {
    min-height: 40px;
    padding: 0 var(--s2);
    background: none;
    border: 0;
    border-radius: var(--r-chip);
    color: var(--fg);
    font: inherit;
    text-align: left;
  }
  .head-actions button:hover {
    background: var(--sunken);
  }
  .mobile-meta {
    padding: var(--s1) var(--s2);
    color: var(--fg-3);
    font-size: var(--fs-xs);
    white-space: normal;
    overflow-wrap: anywhere;
  }
  .turn-composer {
    padding: var(--s1) var(--s2) var(--s2);
  }
  .composer-row .seg button {
    min-height: 40px;
  }
}
</style>
