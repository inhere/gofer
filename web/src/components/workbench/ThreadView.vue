<script setup lang="ts">
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch, type ComputedRef, type Ref } from 'vue'
import { Ellipsis, Pin, Square } from '@lucide/vue'
import Icon from '../../ui/Icon.vue'
import { useRouter } from 'vue-router'
import { answerInteraction, cancelJob, getJob, puntInteraction } from '../../api/client'
import { appendCapped, streamJob } from '../../api/sse'
import { canCancelJob } from '../../utils/jobCancel'
import type { Interaction, Job, SSEEvent, SSEInteractionData, SSELogData, WorkbenchThread } from '../../api/types'
import { patchWorkbenchThread, turnWorkbenchThread } from '../../api/workbench'
import { attachmentCapability } from '../../utils/attachments'
import { formatNumber, formatRelative } from '../../utils/format'
import { t } from '../../i18n'
import AttachTerminal from '../AttachTerminal.vue'
import UncommittedBadge from '../UncommittedBadge.vue'
import ConversationView from './ConversationView.vue'
import ThreadChangesView from './ThreadChangesView.vue'
import InteractionCard from '../InteractionCard.vue'
import LogTape from '../LogTape.vue'
import SessionDrawer from '../SessionDrawer.vue'
import StatusPill from '../../ui/StatusPill.vue'
import Composer from '../../ui/Composer.vue'

const props = defineProps<{ threadId: string; focused: boolean; initialView?: 'process' | 'changes' }>()

const emit = defineEmits<{
  (event: 'view', view: 'process' | 'changes'): void
}>()

// 工作台只看"现在到哪了"：日志从最后 LOG_TAIL_LINES 行开始推，完整日志去 job 详情页。
const LOG_TAIL_LINES = 200

// 发送方式（直接发送 / 排队 / 打断后发送）由统一的 Composer 提供（设计稿 §8）。

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
const turnInput = ref<InstanceType<typeof Composer> | null>(null)
const sessionDrawer = ref<InstanceType<typeof SessionDrawer> | null>(null)
const menuOpen = ref(false)
const embeddedRelayModes = ['auto', 'on', 'off'] as const
const changesView = ref<InstanceType<typeof ThreadChangesView> | null>(null)
const activeView = ref<'process' | 'changes'>(props.initialView === 'changes' ? 'changes' : 'process')
// 「改动」页签只在第一次打开时才挂载（gofer-yb5x）：打开工作台就拉 diff 会让没有 git
// 基线的会话每次都吃一个 409。挂载后保留，切回「过程」不重新请求。
const changesMounted = ref(activeView.value === 'changes')
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
// 「停止」= 取消 job，口径同 job 详情页的取消按钮（gofer-83fe）：排队中、待批等非终态都能停。
const canStop = computed(() => canCancelJob(rawStatus.value))
const canTurn = computed(() => thread.value?.kind === 'agent' && thread.value.resumable &&
  (finished.value || (!!latestJob.value?.session && rawStatus.value === 'awaiting_input')) && !sending.value)
// 附图（gofer-ha0x）：能力位由 thread 带（inline / file 能用，none / 缺失置灰）；
// 上传中或还有失败项时不让发送，免得发出缺图的消息。
const attachCapability = computed(() => attachmentCapability(thread.value))
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
  // 换会话（或判断出别的线程）时丢掉上一轮的附图：它们的责任线程已经不是当前这个。
  turnInput.value?.clearAttachments()
  titleDraft.value = current?.title ?? ''
  // 不把页签踢回「过程」：同一 pane 里换会话时沿用当前页签（元 pane 把选择存在自己身上），
  // 换到新会话的 diff 也是那一刻才请求。
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
    const attachmentIDs = turnInput.value?.readyIDs() ?? []
    const result = await turnWorkbenchThread(current.id, text, attachmentIDs)
    // 发送成功才清空附件条（失败时图还在，可直接重试）。
    turnInput.value?.clearAttachments()
    context.continued(result.job_id)
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    sending.value = false
  }
}

// Composer 的发送：成功后清空草稿（失败时保留，可直接重试）。
async function onSend(text: string): Promise<void> {
  await deliver(text)
  if (!actionError.value) draft.value = ''
}

// 打断后发送：Composer 先把这条放进队首，这里只负责取消当前 job；
// 线程回到可发送状态时 Composer 会自动发出队首。
async function onInterrupt(): Promise<void> {
  if (!canStop.value || stopping.value) {
    actionError.value = t('workbench.composer.running')
    return
  }
  await stopCurrent()
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
  if (thread.value?.kind !== 'relay' && latestJobID.value) {
    changesMounted.value = true
    activeView.value = 'changes'
  }
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
  showProcess()
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

// 页签选择告诉 pane（LayoutPane 按 pane 记着它，换会话时据此恢复）：只在用户切换时发，
// 初始值由 initialView 传回去，不会来回打圈。
watch(activeView, (view) => emit('view', view))

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
        class="btn sm pin-btn"
        :class="{ on: !!thread.pinned }"
        type="button"
        :title="thread.pinned ? t('workbench.head.unpin') : t('workbench.head.pin')"
        :aria-label="thread.pinned ? t('workbench.head.unpin') : t('workbench.head.pin')"
        :aria-pressed="!!thread.pinned"
        @click="togglePin"
      >
        <Icon :icon="Pin" size="sm" />
        <span class="pin-text">{{ thread.pinned ? t('workbench.head.unpin') : t('workbench.head.pin') }}</span>
      </button>
      <button
        class="icon-btn stop"
        type="button"
        :title="t('workbench.head.stop')"
        :aria-label="t('workbench.head.stop')"
        :disabled="!canStop || stopping"
        @click="stopCurrent"
      ><Icon :icon="Square" size="sm" /></button>
      <button class="icon-btn mobile-menu" type="button" :aria-label="t('workbench.head.more')" @click="menuOpen = !menuOpen"><Icon :icon="Ellipsis" size="md" /></button>
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
        v-if="changesMounted && thread.kind !== 'relay' && latestJobID"
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
    >
      <Composer
        ref="turnInput"
        v-model="draft"
        :placeholder="oneShot ? t('workbench.composer.oneShot') : canTurn ? t('workbench.composer.placeholder') : live ? t('workbench.composer.running') : t('workbench.composer.placeholder')"
        :disabled="oneShot"
        :sending="sending"
        :can-send="canTurn"
        :can-interrupt="canStop"
        :modes="['send', 'queue', 'interrupt']"
        queue-scope="workbench"
        :queue-key="thread.id"
        submit-on-enter
        attachments
        :attach-capability="attachCapability"
        :aria-label="t('workbench.composer.placeholder')"
        @send="onSend"
        @interrupt="onInterrupt"
        @attachment-error="actionError = $event"
      />
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
  color: var(--ink);
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
  color: var(--ink-2);
  font: inherit;
  cursor: pointer;
}
.icon-btn:hover {
  color: var(--ink);
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
/* 置顶：桌面带文字、手机只留图标（aria-label 不变）；已置顶用强调色边框 + 淡底，
   不用悬停也能看出状态。 */
.pin-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--ink-2);
}
.pin-btn.on {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--accent-soft);
}
@media (max-width: 760px) {
  .pin-btn {
    padding: 0;
    width: 32px;
    min-width: 32px;
    height: 32px;
    justify-content: center;
  }
  .pin-btn .pin-text {
    display: none;
  }
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
  color: var(--ink);
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
  color: var(--ink);
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
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.tag {
  padding: 0 5px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--ink-2);
  white-space: nowrap;
}
.muted {
  color: var(--ink-3);
}
.meta-row {
  flex: none;
  display: flex;
  align-items: center;
  gap: var(--s1);
  padding: 4px var(--s3);
  color: var(--ink-3);
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
  color: var(--ink-3);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
  white-space: nowrap;
}
.seg button:hover {
  color: var(--ink);
}
.seg button.on {
  background: var(--surface);
  color: var(--ink);
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
  color: var(--ink-3);
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
  color: var(--ink);
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
  color: var(--ink-3);
  padding: var(--s5);
}
.missing-thread {
  display: grid;
  place-items: center;
  padding: var(--s5);
  color: var(--ink-3);
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
    color: var(--ink);
    font: inherit;
    text-align: left;
  }
  .head-actions button:hover {
    background: var(--sunken);
  }
  .mobile-meta {
    padding: var(--s1) var(--s2);
    color: var(--ink-3);
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
