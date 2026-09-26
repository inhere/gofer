<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { streamACPJob } from '../../api/sse'
import type { Interaction, SSEEvent, WorkbenchJobTurn } from '../../api/types'
import InteractionCard from '../InteractionCard.vue'
import MarkdownBlock from '../MarkdownBlock.vue'
import {
  groupACPRounds,
  isACPEvent,
  visibleRoundIDs,
  type ACPEvent,
  type ACPToolLocation,
  type ACPUsageEvent,
} from './acpEvents'

const props = defineProps<{
  threadId: string
  jobIds: string[]
  jobs: WorkbenchJobTurn[]
  latestJobId: string
  latestRunning: boolean
  pendingInteractions: Interaction[]
  submittingInteraction: Set<string>
  focused: boolean
}>()

const emit = defineEmits<{
  (event: 'answer', item: Interaction, value: string): void
  (event: 'punt', item: Interaction): void
  (event: 'ended'): void
  (event: 'error', message: string): void
}>()

const visibleCount = ref(3)
const eventsByJob = ref<Record<string, ACPEvent[]>>({})
const loadingJobs = ref<Set<string>>(new Set())
const errorsByJob = ref<Record<string, string>>({})
const loadedJobs = new Set<string>()
const controllers = new Map<string, AbortController>()
let loadedThreadId = ''

const visibleIds = computed(() => visibleRoundIDs(props.jobIds, visibleCount.value))
const rounds = computed(() => groupACPRounds(visibleIds.value, eventsByJob.value))
const hiddenRounds = computed(() => Math.max(0, props.jobIds.length - visibleIds.value.length))
const statusSignature = computed(() => props.jobs.map((job) => `${job.job_id}:${job.status}`).join('|'))
const runningStatuses = new Set(['queued', 'running', 'waiting_dir', 'recovering', 'pending_interaction'])

function abortAll(): void {
  for (const controller of controllers.values()) controller.abort()
  controllers.clear()
}

function resetThread(): void {
  abortAll()
  loadedJobs.clear()
  eventsByJob.value = {}
  errorsByJob.value = {}
  loadingJobs.value = new Set()
  visibleCount.value = 3
}

function setLoading(jobId: string, loading: boolean): void {
  const next = new Set(loadingJobs.value)
  if (loading) next.add(jobId)
  else next.delete(jobId)
  loadingJobs.value = next
}

function jobStatus(jobId: string): string {
  return props.jobs.find((job) => job.job_id === jobId)?.status ?? ''
}

function appendEvent(jobId: string, frame: SSEEvent): void {
  if (frame.type !== 'acp' || !isACPEvent(frame.data)) return
  eventsByJob.value = {
    ...eventsByJob.value,
    [jobId]: [...(eventsByJob.value[jobId] ?? []), frame.data],
  }
}

async function loadJob(jobId: string): Promise<void> {
  if (loadedJobs.has(jobId) || controllers.has(jobId)) return
  const isLatest = jobId === props.latestJobId
  if (!isLatest && runningStatuses.has(jobStatus(jobId))) {
    errorsByJob.value = { ...errorsByJob.value, [jobId]: '历史轮次仍在运行，结束后再加载。' }
    return
  }

  loadedJobs.add(jobId)
  setLoading(jobId, true)
  const controller = new AbortController()
  controllers.set(jobId, controller)
  errorsByJob.value = { ...errorsByJob.value, [jobId]: '' }
  eventsByJob.value = { ...eventsByJob.value, [jobId]: [] }
  try {
    await streamACPJob(jobId, {
      tail: isLatest && props.latestRunning ? 300 : undefined,
      signal: controller.signal,
      onEvent: (event) => appendEvent(jobId, event),
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
    setLoading(jobId, false)
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

function thoughtSummary(text: string): string {
  const compact = text.replace(/\s+/g, ' ').trim()
  return compact.length > 72 ? `${compact.slice(0, 72)}…` : compact || '思考'
}

function formatLocation(location: ACPToolLocation): string {
  return location.line == null ? location.path : `${location.path}:${location.line}`
}

function formatUsage(event: ACPUsageEvent): string {
  const parts: string[] = []
  if (event.total_tokens != null) parts.push(`${event.total_tokens.toLocaleString()} tokens`)
  else if (event.used != null) parts.push(`${event.used.toLocaleString()} used`)
  if (event.input_tokens != null) parts.push(`in ${event.input_tokens.toLocaleString()}`)
  if (event.output_tokens != null) parts.push(`out ${event.output_tokens.toLocaleString()}`)
  if (event.cost_usd != null) parts.push(`$${event.cost_usd.toFixed(4)}`)
  if (parts.length === 0 && event.raw) parts.push(event.raw)
  return parts.join(' · ') || 'usage updated'
}

watch(
  [() => props.threadId, () => visibleIds.value.join('|'), statusSignature, () => props.latestRunning],
  ([threadId]) => {
    if (threadId !== loadedThreadId) {
      loadedThreadId = threadId
      resetThread()
    }
    ensureVisible()
  },
  { immediate: true },
)

onUnmounted(abortAll)
</script>

<template>
  <div class="conversation" :class="{ focused }">
    <div v-if="hiddenRounds > 0" class="load-earlier">
      <button class="mono" type="button" @click="loadEarlier">
        加载更早的轮次（还有 {{ hiddenRounds }} 轮）
      </button>
    </div>

    <section v-for="round in rounds" :key="round.jobId" class="round">
      <header class="round-head mono">
        <span>第 {{ roundNumber(round.jobId) }} 轮</span>
        <span>{{ round.jobId }}</span>
        <span>{{ jobStatus(round.jobId) || 'unknown' }}</span>
      </header>

      <p v-if="loadingJobs.has(round.jobId) && round.events.length === 0" class="round-note mono">加载结构化记录…</p>
      <p v-if="errorsByJob[round.jobId]" class="round-error mono">
        {{ errorsByJob[round.jobId] }}
        <button type="button" @click="retry(round.jobId)">重试</button>
      </p>

      <div class="events">
        <template v-for="(event, index) in round.events" :key="`${event.kind}-${event.seq}-${index}`">
          <div v-if="event.kind === 'truncated'" class="gap mono">
            已省略 {{ event.skipped }} 条较早事件
          </div>

          <div v-else-if="event.kind === 'prompt'" class="bubble bubble--user">
            <div class="role mono">YOU</div>
            <div class="plain-text">{{ event.text }}</div>
            <span v-if="event.truncated" class="truncated mono">已截断</span>
          </div>

          <div v-else-if="event.kind === 'message'" class="bubble bubble--assistant">
            <div class="role mono">ASSISTANT</div>
            <MarkdownBlock :text="event.text" />
            <span v-if="event.truncated" class="truncated mono">已截断</span>
          </div>

          <details v-else-if="event.kind === 'thought'" class="thought">
            <summary class="mono">思考 · {{ thoughtSummary(event.text) }}</summary>
            <pre class="mono">{{ event.text }}</pre>
            <span v-if="event.truncated" class="truncated mono">已截断</span>
          </details>

          <details v-else-if="event.kind === 'tool'" class="tool-card">
            <summary>
              <span class="tool-dot" :class="`tool-dot--${event.status || 'unknown'}`"></span>
              <span class="mono">{{ event.title || event.tool_kind || event.tool_call_id }}</span>
              <span class="tool-status mono">{{ event.status || 'update' }}</span>
            </summary>
            <pre v-if="event.raw_input" class="raw mono">{{ event.raw_input }}</pre>
            <div v-if="event.locations?.length" class="locations mono">
              <span v-for="location in event.locations" :key="formatLocation(location)">{{ formatLocation(location) }}</span>
            </div>
          </details>

          <div v-else-if="event.kind === 'permission'" class="event-line permission mono">
            permission · {{ event.title || event.tool_call_id || 'tool' }} · {{ event.outcome || 'pending' }}
            <template v-if="event.option_id"> · {{ event.option_id }}</template>
          </div>

          <div v-else-if="event.kind === 'plan'" class="plan-card">
            <div class="role mono">PLAN</div>
            <ul>
              <li v-for="(entry, entryIndex) in event.entries ?? []" :key="`${entry.content}-${entryIndex}`">
                <span>{{ entry.content }}</span>
                <span class="mono">{{ entry.status || entry.priority || '' }}</span>
              </li>
            </ul>
          </div>

          <div v-else-if="event.kind === 'usage'" class="event-line mono">{{ formatUsage(event) }}</div>
          <div v-else-if="event.kind === 'stop'" class="turn-end mono">
            本轮结束 · {{ event.stop_reason || 'unknown' }}
          </div>
        </template>
      </div>

      <p v-if="!loadingJobs.has(round.jobId) && round.events.length === 0 && !errorsByJob[round.jobId]" class="round-note mono">
        暂无结构化记录
      </p>

      <div
        v-if="round.jobId === latestJobId && pendingInteractions.length > 0"
        class="conversation-interactions"
        data-interaction-area
      >
        <InteractionCard
          v-for="item in pendingInteractions"
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
.conversation { height: 100%; overflow-y: auto; padding: 12px 14px 28px; background: var(--bg); }
.load-earlier { display: flex; justify-content: center; margin-bottom: 12px; }
.load-earlier button, .round-error button { color: var(--phosphor); background: var(--panel); border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 10px; }
.round { max-width: 920px; margin: 0 auto 18px; border: 1px solid var(--line); border-radius: var(--radius); background: var(--panel); overflow: hidden; }
.round-head { display: flex; flex-wrap: wrap; gap: 8px 14px; padding: 7px 10px; border-bottom: 1px solid var(--line); color: var(--queue); font-size: 10px; }
.events { display: flex; flex-direction: column; gap: 9px; padding: 12px; }
.bubble { max-width: min(84%, 760px); border: 1px solid var(--line); border-radius: 10px; padding: 9px 12px; }
.bubble--user { align-self: flex-end; background: rgba(100, 170, 255, .09); }
.bubble--assistant { align-self: flex-start; background: var(--ink); }
.role { margin-bottom: 5px; color: var(--queue); font-size: 9px; letter-spacing: .08em; }
.plain-text { white-space: pre-wrap; word-break: break-word; }
.thought, .tool-card, .plan-card { border: 1px solid var(--line); border-radius: var(--radius); background: var(--ink); padding: 8px 10px; }
.thought summary, .tool-card summary { cursor: pointer; color: var(--queue); }
.thought pre, .raw { margin: 8px 0 0; white-space: pre-wrap; word-break: break-word; color: var(--paper); }
.tool-dot { display: inline-block; width: 7px; height: 7px; margin-right: 7px; border-radius: 50%; background: var(--queue); }
.tool-dot--pending, .tool-dot--in_progress { background: var(--run); }
.tool-dot--completed { background: var(--done); }
.tool-dot--failed { background: var(--fail); }
.tool-status { margin-left: 8px; color: var(--queue); font-size: 10px; }
.locations { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
.locations span { border: 1px solid var(--line); border-radius: 9px; padding: 1px 7px; color: var(--queue); }
.event-line, .turn-end, .gap, .round-note, .round-error { margin: 0; padding: 7px 10px; color: var(--queue); font-size: 11px; }
.permission { border-left: 2px solid var(--run); background: rgba(255, 190, 80, .06); }
.turn-end { border-top: 1px dashed var(--line); text-align: center; }
.gap { text-align: center; font-style: italic; }
.plan-card ul { margin: 0; padding-left: 20px; }
.plan-card li { display: flex; justify-content: space-between; gap: 12px; margin: 4px 0; }
.truncated { display: inline-block; margin-top: 5px; color: var(--run); font-size: 10px; }
.round-error { color: var(--fail); }
.round-error button { margin-left: 8px; }
.conversation-interactions { padding: 0 12px 12px; }
@media (max-width: 767px) {
  .conversation { padding: 8px 8px 20px; }
  .bubble { max-width: 94%; }
}
</style>
