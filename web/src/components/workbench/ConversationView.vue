<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { streamACPJob } from '../../api/sse'
import type { AttachmentRef, Interaction, SSEEvent, WorkbenchJobTurn } from '../../api/types'
import { shouldFollowBottom } from '../../utils/sessionPagination'
import { formatTime } from '../../utils/format'
import { t } from '../../i18n'
import MarkdownBlock from '../MarkdownBlock.vue'
import AttachmentThumbs from '../AttachmentThumbs.vue'
import InteractionCard from '../InteractionCard.vue'
import StatusGlyph from '../../ui/StatusGlyph.vue'
import {
  acpNoticeText,
  isACPEvent,
  isACPNotice,
  permissionOutcome,
  timelineBlocks,
  visibleRoundIDs,
  type ACPEvent,
  type ACPTextEvent,
  type ACPToolEvent,
} from './acpEvents'

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
// 工具调用默认折叠（设计稿 §7）：展开状态按轮次记，默认空 = 全部折叠。
const openToolGroups = ref<Set<string>>(new Set())
let firstRender = true
let loadedThreadId = ''

const visibleIds = computed(() => visibleRoundIDs(props.jobIds, visibleCount.value))
const rounds = computed(() => visibleIds.value.map((jobId) => ({ jobId, events: eventsByJob.value[jobId] ?? [] })))
const hiddenRounds = computed(() => Math.max(0, props.jobIds.length - visibleIds.value.length))
const statusSignature = computed(() => props.jobs.map((job) => `${job.job_id}:${job.status}`).join('|'))
const runningStatuses = new Set(['queued', 'running', 'awaiting_input', 'waiting_dir', 'recovering', 'pending_interaction'])
const editingToolKinds = new Set(['edit', 'delete', 'move'])
// 内嵌的审批卡片只挂在最后一轮（当前这一轮）下面：历史轮次的待办交互没有处理入口。
const inlineInteractions = computed(() => {
  const latest = visibleIds.value[visibleIds.value.length - 1]
  return latest ? props.pendingInteractions : []
})

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
  openToolGroups.value = new Set()
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

function toolsKey(jobId: string, index: number): string {
  return `${jobId}:${index}`
}

function isToolsOpen(jobId: string, index: number): boolean {
  return openToolGroups.value.has(toolsKey(jobId, index))
}

function toggleTools(jobId: string, index: number): void {
  const key = toolsKey(jobId, index)
  const next = new Set(openToolGroups.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  openToolGroups.value = next
}

function toolTitle(tool: ACPToolEvent): string {
  return tool.title || tool.tool_kind || 'tool'
}

function toolSummary(tool: ACPToolEvent): string {
  const first = tool.locations?.[0]?.path
  if (first) return first
  return tool.raw_input ? tool.raw_input.slice(0, 80) : ''
}

// 历史气泡的附图缩略图：只取 prompt / message 两类文本事件的附图。
function eventAttachments(event: ACPTextEvent): AttachmentRef[] {
  if (event.kind !== 'prompt' && event.kind !== 'message') return []
  return event.attachments ?? []
}

function eventTime(event: { seq: number }): string {
  // 结构化记录没有逐条时间戳：只在该轮有开始时间时给「HH:mm」，否则留空。
  const job = props.jobs.find((item) => item.job_id === visibleIds.value[0])
  return job?.started_at && event.seq === 0 ? formatTime(job.started_at) : ''
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

      <div class="tl">
        <template v-for="(block, index) in timelineBlocks(round.events)" :key="`${block.kind}-${index}`">
          <div v-if="block.kind === 'text' && block.event.kind === 'prompt'" class="msg msg--me">
            <div class="who">{{ t('workbench.conv.you') }}<span class="muted">{{ eventTime(block.event) }}</span></div>
            <div class="bubble bubble--user">
              <div class="plain-text" lang="en">{{ block.event.text }}</div>
              <AttachmentThumbs v-if="eventAttachments(block.event).length > 0" :items="eventAttachments(block.event)" />
              <span v-if="block.event.truncated" class="truncated">{{ t('workbench.conv.truncated') }}</span>
            </div>
          </div>

          <div v-else-if="block.kind === 'text'" class="msg">
            <div class="who">{{ t('workbench.conv.assistant') }}<span class="muted">{{ eventTime(block.event) }}</span></div>
            <div class="txt">
              <MarkdownBlock :text="block.event.text" />
              <AttachmentThumbs v-if="eventAttachments(block.event).length > 0" :items="eventAttachments(block.event)" />
              <span v-if="block.event.truncated" class="truncated">{{ t('workbench.conv.truncated') }}</span>
            </div>
          </div>

          <div v-else-if="block.kind === 'permission'" class="inline-approval" :class="{ done: true }" data-test="inline-permission">
            <div class="ia-head">
              <StatusGlyph
                :family="permissionOutcome(block.event) === 'denied' ? 'fail' : 'done'"
                :size="13"
              />
              <span>{{ t(`workbench.conv.permission.${permissionOutcome(block.event)}`) }}</span>
              <span v-if="block.event.title" class="ia-title">{{ block.event.title }}</span>
            </div>
            <div v-if="block.event.reason" class="ia-reason">{{ block.event.reason }}</div>
          </div>

          <div v-else class="tools" :data-open="isToolsOpen(round.jobId, index)">
            <button
              class="tools-sum"
              type="button"
              :aria-expanded="isToolsOpen(round.jobId, index)"
              data-test="tools-toggle"
              @click="toggleTools(round.jobId, index)"
            >
              <span class="caret" aria-hidden="true">{{ isToolsOpen(round.jobId, index) ? '▾' : '▸' }}</span>
              <span>{{ t('workbench.conv.tools', { n: block.events.length }) }}</span>
              <span v-if="block.failed > 0" class="tools-fail">{{ t('workbench.conv.toolsFail', { n: block.failed }) }}</span>
            </button>
            <div v-if="isToolsOpen(round.jobId, index)" class="tool-list">
              <div v-for="tool in block.events" :key="tool.tool_call_id" class="tool">
                <StatusGlyph :status="tool.status" :size="12" />
                <span class="tool-kind">{{ toolTitle(tool) }}</span>
                <span class="tool-arg" :title="toolSummary(tool)">{{ toolSummary(tool) }}</span>
              </div>
            </div>
          </div>
        </template>
      </div>

      <p
        v-if="!loadingJobs.has(round.jobId) && round.events.length === 0 && !errorsByJob[round.jobId] && !noticesByJob[round.jobId]"
        class="round-note"
      >
        {{ emptyNote(jobStatus(round.jobId), openJobs.has(round.jobId)) }}
      </p>

      <!-- 审批卡片内嵌在对话里，可直接处理（设计稿 §7）。 -->
      <div v-if="round.jobId === visibleIds[visibleIds.length - 1] && inlineInteractions.length > 0" class="conversation-interactions">
        <InteractionCard
          v-for="item in inlineInteractions"
          :key="item.id"
          :interaction="item"
          :submitting="submittingInteraction.has(item.id)"
          @answer="emit('answer', item, $event)"
          @punt="emit('punt', item)"
        />
      </div>
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
  color: var(--fg-3);
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
.tl {
  display: flex;
  flex-direction: column;
  gap: var(--s3);
  padding: var(--s3) 0 0;
}
.msg {
  display: flex;
  flex-direction: column;
  gap: var(--s1);
  max-width: min(88%, 760px);
}
.msg--me {
  align-self: flex-end;
  align-items: flex-end;
}
.who {
  display: flex;
  align-items: center;
  gap: var(--s2);
  color: var(--fg-2);
  font-size: var(--fs-sm);
  font-weight: 500;
}
.muted {
  color: var(--fg-3);
  font-size: var(--fs-xs);
  font-weight: 400;
}
.bubble,
.txt {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  padding: var(--s2) var(--s3);
  background: var(--surface);
  color: var(--fg);
  overflow-wrap: anywhere;
}
.bubble--user {
  background: var(--accent-soft);
  border-color: var(--line);
}
.plain-text {
  white-space: pre-wrap;
}
.truncated {
  display: inline-block;
  margin-top: 5px;
  color: var(--time);
  font-size: var(--fs-xs);
}
.tools {
  align-self: flex-start;
  max-width: min(88%, 760px);
}
.tools-sum {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  padding: var(--s1) var(--s2);
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--fg-2);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.tools-sum:hover {
  color: var(--fg);
  border-color: var(--line-strong);
}
.tools-sum:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.tools-fail {
  color: var(--fail);
}
.tool-list {
  margin-top: var(--s1);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--sunken);
  overflow: hidden;
}
.tool {
  display: grid;
  grid-template-columns: auto auto minmax(0, 1fr);
  gap: var(--s2);
  align-items: center;
  padding: 5px var(--s2);
  color: var(--fg-2);
  font-size: var(--fs-sm);
}
.tool + .tool {
  border-top: 1px solid var(--line);
}
.tool-kind {
  font-weight: 500;
  white-space: nowrap;
}
.tool-arg {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--fg-3);
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
}
.inline-approval {
  align-self: flex-start;
  max-width: min(88%, 760px);
  padding: var(--s2) var(--s3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-left: 3px solid var(--need);
  border-radius: var(--r-panel);
}
.inline-approval.done {
  border-left-color: var(--done);
}
.ia-head {
  display: flex;
  align-items: center;
  gap: var(--s1);
  color: var(--fg);
  font-size: var(--fs-sm);
  font-weight: 500;
}
.ia-title {
  color: var(--fg-3);
  font-weight: 400;
}
.ia-reason {
  margin-top: var(--s1);
  color: var(--fg-2);
  font-size: var(--fs-sm);
}
.round-note,
.round-error {
  margin: 0;
  padding: var(--s2) 0;
  color: var(--fg-3);
  font-size: var(--fs-sm);
}
.round-error {
  color: var(--fail);
}
.round-error .btn {
  margin-left: var(--s2);
}
.conversation-interactions {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  margin-top: var(--s3);
}
@media (max-width: 760px) {
  .conversation {
    padding: var(--s2) var(--s2) var(--s6);
  }
  .msg,
  .tools,
  .inline-approval {
    max-width: 100%;
  }
}
</style>
