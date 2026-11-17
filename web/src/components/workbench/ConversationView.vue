<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { streamACPJob } from '../../api/sse'
import type { Interaction, SSEEvent, WorkbenchJobTurn } from '../../api/types'
import { shouldFollowBottom } from '../../utils/sessionPagination'
import { formatTime } from '../../utils/format'
import { t } from '../../i18n'
import ConversationTimeline from '../ConversationTimeline.vue'
import StatusGlyph from '../../ui/StatusGlyph.vue'
import {
  acpNoticeText,
  isACPEvent,
  isACPNotice,
  visibleRoundIDs,
  type ACPEvent,
} from './acpEvents'
import {
  acpEventEntries,
  interactionEntries,
  isTurnUnsent,
  turnUnsentEntry,
  type ConversationEntry,
  type ConversationInteractionEntry,
  type ConversationUnsentEntry,
} from '../../utils/conversationTimeline'

const props = defineProps<{
  threadId: string
  jobIds: string[]
  jobs: WorkbenchJobTurn[]
  latestJobId: string
  latestRunning: boolean
  continuousSession: boolean
  pendingInteractions: Interaction[]
  submittingInteraction: Set<string>
  focused: boolean
}>()

const emit = defineEmits<{
  (event: 'answer', item: Interaction, value: string): void
  (event: 'punt', item: Interaction): void
  (event: 'ended'): void
  (event: 'error', message: string): void
  (event: 'open-diff', path: string): void
  (event: 'files-changed'): void
  (event: 'resend', text: string): void
}>()

const visibleCount = ref(3)
const eventsByJob = ref<Record<string, ACPEvent[]>>({})
const loadingJobs = ref<Set<string>>(new Set())
// openJobs are the rounds whose stream is connected and still open (a live session).
const openJobs = ref<Set<string>>(new Set())
const errorsByJob = ref<Record<string, string>>({})
// noticesByJob holds the server's explanation for a round whose structured record
// cannot arrive (old worker / peer job), shown instead of an endless loading line.
const noticesByJob = ref<Record<string, string>>({})
const loadedJobs = new Set<string>()
const controllers = new Map<string, AbortController>()
const toolKinds = new Map<string, string>()
const conversationEl = ref<HTMLElement | null>(null)
// 没发出去的那一轮（等目录锁超时）：服务端回 turn_unsent，排在那一轮的最后。
const unsentByJob = ref<Record<string, ConversationUnsentEntry[]>>({})
let firstRender = true
let loadedThreadId = ''

const visibleIds = computed(() => visibleRoundIDs(props.jobIds, visibleCount.value))
const rounds = computed(() => visibleIds.value.map((jobId) => ({ jobId, events: eventsByJob.value[jobId] ?? [] })))
const hiddenRounds = computed(() => Math.max(0, props.jobIds.length - visibleIds.value.length))
const statusSignature = computed(() => props.jobs.map((job) => `${job.job_id}:${job.status}`).join('|'))
const runningStatuses = new Set(['queued', 'running', 'awaiting_input', 'waiting_dir', 'recovering', 'pending_interaction'])
const editingToolKinds = new Set(['edit', 'delete', 'move'])
// 条目抬头措辞由工作台给（「你」/「助手」），时间取该轮的开始时间。
const conversationLabels = computed(() => ({ user: t('workbench.conv.you'), agent: t('workbench.conv.assistant') }))

// 内嵌的审批卡片只挂在最后一轮（当前这一轮）下面：历史轮次的待办交互没有处理入口。
function isLatestRound(jobId: string): boolean {
  return jobId === visibleIds.value[visibleIds.value.length - 1]
}

// 一轮的结构化记录 → 时间线条目（工作台、job 详情、会话抽屉共用同一套渲染）。
function roundEntries(round: { jobId: string; events: ACPEvent[] }): ConversationEntry[] {
  const job = props.jobs.find((item) => item.job_id === round.jobId)
  const entries = acpEventEntries(round.events, conversationLabels.value, (event) =>
    job?.started_at && event.seq === 0 ? formatTime(job.started_at) : '')
  entries.push(...(unsentByJob.value[round.jobId] ?? []))
  if (isLatestRound(round.jobId) && props.pendingInteractions.length > 0) {
    entries.push(...interactionEntries(props.pendingInteractions, props.submittingInteraction))
  }
  return entries
}

function onAnswer(entry: ConversationInteractionEntry, value: string): void {
  emit('answer', entry.interaction, value)
}

function onPunt(entry: ConversationInteractionEntry): void {
  emit('punt', entry.interaction)
}

// 「重发」只把原消息交回去，填进输入框由线程视图负责（它持有 Composer 的草稿）。
function onResend(entry: ConversationUnsentEntry): void {
  emit('resend', entry.prompt)
}

function abortAll(): void {
  for (const controller of controllers.values()) controller.abort()
  controllers.clear()
}

function resetThread(): void {
  abortAll()
  loadedJobs.clear()
  toolKinds.clear()
  eventsByJob.value = {}
  errorsByJob.value = {}
  noticesByJob.value = {}
  loadingJobs.value = new Set()
  openJobs.value = new Set()
  unsentByJob.value = {}
  visibleCount.value = 3
  firstRender = true
}

function scrollToLatest(): void {
  void nextTick(() => void nextTick(() => {
    if (conversationEl.value) conversationEl.value.scrollTop = conversationEl.value.scrollHeight
  }))
}

function setLoading(jobId: string, loading: boolean): void {
  const next = new Set(loadingJobs.value)
  if (loading) next.add(jobId)
  else next.delete(jobId)
  loadingJobs.value = next
}

function setOpen(jobId: string, open: boolean): void {
  const next = new Set(openJobs.value)
  if (open) next.add(jobId)
  else next.delete(jobId)
  openJobs.value = next
}

function jobStatus(jobId: string): string {
  return props.jobs.find((job) => job.job_id === jobId)?.status ?? ''
}

function appendEvent(jobId: string, frame: SSEEvent): void {
  if (frame.type === 'acp' && isACPNotice(frame.data)) {
    noticesByJob.value = { ...noticesByJob.value, [jobId]: acpNoticeText(frame.data) }
    return
  }
  // 这一轮没发出去（等目录锁超时）：acp 流里回一条 kind=turn_unsent，带原消息供重发。
  if (isTurnUnsent(frame.data)) {
    const queued = unsentByJob.value[jobId] ?? []
    unsentByJob.value = {
      ...unsentByJob.value,
      [jobId]: [...queued, turnUnsentEntry(frame.data as { text?: string; prompt?: string }, `${jobId}-unsent-${queued.length + 1}`)],
    }
    const unsentEl = conversationEl.value
    if (firstRender || !unsentEl || shouldFollowBottom(unsentEl)) scrollToLatest()
    return
  }
  if (frame.type !== 'acp' || !isACPEvent(frame.data)) return
  const event = frame.data
  if (event.kind === 'tool') {
    const key = `${jobId}:${event.tool_call_id}`
    if (event.tool_kind) toolKinds.set(key, event.tool_kind)
    const kind = event.tool_kind ?? toolKinds.get(key)
    if (
      jobId === props.latestJobId
      && props.latestRunning
      && kind != null
      && editingToolKinds.has(kind)
    ) {
      emit('files-changed')
    }
  }
  eventsByJob.value = {
    ...eventsByJob.value,
    [jobId]: [...(eventsByJob.value[jobId] ?? []), event],
  }
  const el = conversationEl.value
  if (firstRender || !el || shouldFollowBottom(el)) scrollToLatest()
}

async function loadJob(jobId: string): Promise<void> {
  if (loadedJobs.has(jobId) || controllers.has(jobId)) return
  const isLatest = jobId === props.latestJobId
  if (!isLatest && runningStatuses.has(jobStatus(jobId))) {
    errorsByJob.value = { ...errorsByJob.value, [jobId]: t('workbench.conv.emptyRunning') }
    return
  }

  loadedJobs.add(jobId)
  setLoading(jobId, true)
  const controller = new AbortController()
  controllers.set(jobId, controller)
  errorsByJob.value = { ...errorsByJob.value, [jobId]: '' }
  noticesByJob.value = { ...noticesByJob.value, [jobId]: '' }
  eventsByJob.value = { ...eventsByJob.value, [jobId]: [] }
  try {
    await streamACPJob(jobId, {
      tail: isLatest && props.latestRunning ? 300 : undefined,
      signal: controller.signal,
      onEvent: (event) => appendEvent(jobId, event),
      // A live session's stream never ends: once connected, stop showing "loading".
      onOpen: () => {
        setLoading(jobId, false)
        setOpen(jobId, true)
      },
    })
    if (isLatest && !controller.signal.aborted) emit('ended')
  } catch (error) {
    if (!controller.signal.aborted) {
      const message = error instanceof Error ? error.message : String(error)
      errorsByJob.value = { ...errorsByJob.value, [jobId]: message }
      emit('error', `ACP ${jobId}：${message}`)
    }
  } finally {
    if (controllers.get(jobId) === controller) controllers.delete(jobId)
    // An aborted stream whose round was reloaded must not clear the new stream's state.
    if (!controllers.has(jobId)) {
      setLoading(jobId, false)
      setOpen(jobId, false)
    }
  }
}

function ensureVisible(): void {
  for (const [jobId, controller] of controllers) {
    const staleLiveRound = jobId !== props.latestJobId && runningStatuses.has(jobStatus(jobId))
    if (!visibleIds.value.includes(jobId) || staleLiveRound) {
      controller.abort()
      controllers.delete(jobId)
      loadedJobs.delete(jobId)
    }
  }
  for (const jobId of visibleIds.value) void loadJob(jobId)
}

function loadEarlier(): void {
  visibleCount.value = Math.min(props.jobIds.length, visibleCount.value + 3)
}

function retry(jobId: string): void {
  loadedJobs.delete(jobId)
  errorsByJob.value = { ...errorsByJob.value, [jobId]: '' }
  void loadJob(jobId)
}

function roundNumber(jobId: string): number {
  return props.jobIds.indexOf(jobId) + 1
}

function emptyNote(status: string, live: boolean): string {
  if (live && status === 'awaiting_input') return t('workbench.conv.emptyReady')
  if (live && status !== '' && !['done', 'failed', 'timeout', 'cancelled'].includes(status)) {
    return t('workbench.conv.emptyRunning')
  }
  return t('workbench.conv.emptyNone')
}

watch(
  [() => props.threadId, () => visibleIds.value.join('|'), statusSignature, () => props.latestRunning],
  ([threadId]) => {
    if (threadId !== loadedThreadId) {
      loadedThreadId = threadId
      resetThread()
    }
    ensureVisible()
    if (firstRender) {
      firstRender = false
      scrollToLatest()
    }
  },
  { immediate: true },
)

onUnmounted(abortAll)
</script>

<template>
  <div ref="conversationEl" class="conversation" :class="{ focused }" data-test="wb-timeline">
    <div v-if="hiddenRounds > 0" class="load-earlier">
      <button class="btn sm" type="button" @click="loadEarlier">
        {{ t('workbench.conv.loadEarlier', { n: hiddenRounds }) }}
      </button>
    </div>

    <section v-for="round in rounds" :key="round.jobId" class="round" :data-job="round.jobId">
      <header class="round-head">
        <span>{{ continuousSession ? t('workbench.conv.continuous') : t('workbench.conv.round', { n: roundNumber(round.jobId) }) }}</span>
        <span class="mono round-id">{{ round.jobId }}</span>
        <span class="round-status">
          <StatusGlyph :status="jobStatus(round.jobId)" :size="12" />
          <span>{{ jobStatus(round.jobId) || 'unknown' }}</span>
        </span>
        <RouterLink class="round-link" :to="`/jobs/${encodeURIComponent(round.jobId)}`">{{ t('workbench.conv.viewProcess') }}</RouterLink>
      </header>

      <p v-if="noticesByJob[round.jobId]" class="round-note">{{ noticesByJob[round.jobId] }}</p>
      <p v-else-if="loadingJobs.has(round.jobId) && round.events.length === 0" class="round-note">{{ t('workbench.conv.loading') }}</p>
      <p v-if="errorsByJob[round.jobId]" class="round-error">
        {{ errorsByJob[round.jobId] }}
        <button class="btn sm" type="button" @click="retry(round.jobId)">{{ t('workbench.conv.retry') }}</button>
      </p>

      <ConversationTimeline
        :entries="roundEntries(round)"
        @answer="onAnswer"
        @punt="onPunt"
        @resend="onResend"
      />

      <p
        v-if="!loadingJobs.has(round.jobId) && round.events.length === 0 && !errorsByJob[round.jobId] && !noticesByJob[round.jobId]"
        class="round-note"
      >
        {{ emptyNote(jobStatus(round.jobId), openJobs.has(round.jobId)) }}
      </p>
    </section>
  </div>
</template>

<style scoped>
.conversation {
  height: 100%;
  overflow-y: auto;
  padding: var(--s3) var(--s4) var(--s8);
  background: var(--bg);
}
.load-earlier {
  display: flex;
  justify-content: center;
  margin-bottom: var(--s3);
}
.round {
  max-width: 920px;
  margin: 0 auto var(--s4);
}
.round-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s1) var(--s3);
  padding: var(--s1) 0 var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
  border-bottom: 1px solid var(--line);
}
.round-id {
  overflow: hidden;
  text-overflow: ellipsis;
}
.round-status {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.round-link {
  margin-left: auto;
  color: var(--accent);
}
.round-note,
.round-error {
  margin: 0;
  padding: var(--s2) 0;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.round-error {
  color: var(--fail);
}
.round-error .btn {
  margin-left: var(--s2);
}
@media (max-width: 760px) {
  .conversation {
    padding: var(--s2) var(--s2) var(--s6);
  }
}
</style>
