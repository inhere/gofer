<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { answerInteraction, cancelJob, puntInteraction } from '../../api/client'
import { appendCapped, streamJob } from '../../api/sse'
import type { Interaction, Job, SSEEvent, SSEInteractionData, SSELogData, WorkbenchThread } from '../../api/types'
import { patchWorkbenchThread, turnWorkbenchThread } from '../../api/workbench'
import AttachTerminal from '../AttachTerminal.vue'
import InteractionCard from '../InteractionCard.vue'
import LogTape from '../LogTape.vue'
import SessionDrawer from '../SessionDrawer.vue'

const props = defineProps<{ thread: WorkbenchThread }>()
const emit = defineEmits<{
  (e: 'refresh'): void
  (e: 'continued', jobID?: string): void
  (e: 'back'): void
}>()

const router = useRouter()
const root = ref<HTMLElement | null>(null)
const interactionArea = ref<HTMLElement | null>(null)
const turnInput = ref<HTMLTextAreaElement | null>(null)
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

const latestJobID = computed(() => props.thread.latest_job_id ?? '')
const rawStatus = computed(() => liveStatus.value || props.thread.raw_status || '')
const live = computed(() => ['queued', 'running', 'waiting_dir', 'recovering', 'pending_interaction'].includes(rawStatus.value))
const finished = computed(() => ['done', 'failed', 'cancelled', 'timeout', 'rejected'].includes(rawStatus.value))
const canTurn = computed(() => props.thread.kind === 'agent' && props.thread.resumable && finished.value && !sending.value)
const oneShot = computed(() => props.thread.kind === 'job' && !props.thread.resumable)

function resetForThread(): void {
  streamAbort?.abort()
  streamAbort = null
  stdout.value = ''
  stderr.value = ''
  liveStatus.value = props.thread.raw_status ?? ''
  interactions.value = [...(props.thread.pending_interactions ?? [])]
  streamError.value = ''
  actionError.value = ''
  draft.value = ''
  titleDraft.value = props.thread.title
  if (props.thread.kind !== 'relay' && latestJobID.value) void startStream(latestJobID.value)
}

function onEvent(event: SSEEvent): void {
  if (event.type === 'status') {
    const value = event.data as Job
    liveStatus.value = value.status
    emit('refresh')
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
    emit('refresh')
  }
}

async function startStream(jobID: string): Promise<void> {
  const ctrl = new AbortController()
  streamAbort = ctrl
  try {
    await streamJob(jobID, { signal: ctrl.signal, onEvent })
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
    emit('refresh')
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
    emit('refresh')
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
    const result = await turnWorkbenchThread(props.thread.id, text)
    draft.value = ''
    emit('continued', result.job_id)
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
  if (titleDraft.value.trim() === props.thread.title) return
  try {
    await patchWorkbenchThread(props.thread.id, { title: titleDraft.value.trim() })
    emit('refresh')
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  }
}

async function togglePin(): Promise<void> {
  try {
    await patchWorkbenchThread(props.thread.id, { pinned: !props.thread.pinned })
    emit('refresh')
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
    emit('refresh')
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    stopping.value = false
  }
}

function openDetails(): void {
  if (latestJobID.value) void router.push(`/jobs/${encodeURIComponent(latestJobID.value)}`)
}

function usageText(): string {
  const total = props.thread.usage?.total_tokens ?? 0
  const cost = props.thread.usage?.cost_usd ?? 0
  if (!total && !cost) return '—'
  return `${total.toLocaleString()} tok${cost ? ` · $${cost.toFixed(4)}` : ''}`
}

function focusTurn(): void {
  turnInput.value?.focus()
}

function focusAction(action: string): void {
  void nextTick(() => {
    if (action === 'answer') interactionArea.value?.scrollIntoView({ block: 'start', behavior: 'smooth' })
    else if (action === 'reply') root.value?.querySelector<HTMLElement>('textarea')?.focus()
    else root.value?.focus()
  })
}

defineExpose({ stopCurrent, focusTurn, focusAction })

watch([() => props.thread.id, () => props.thread.latest_job_id], resetForThread, { immediate: true })
onUnmounted(() => streamAbort?.abort())
</script>

<template>
  <section ref="root" class="thread-pane" tabindex="-1">
    <header class="thread-head">
      <button class="back mono" type="button" @click="emit('back')">← 会话</button>
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
        <span class="status mono" :class="`status--${thread.status}`">{{ thread.status }}<template v-if="thread.stalled"> · stalled</template></span>
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

    <p v-if="actionError" class="error mono">{{ actionError }}</p>
    <div ref="interactionArea" class="interaction-area">
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
      <SessionDrawer
        v-if="thread.kind === 'relay' && thread.relay"
        :sid="thread.relay.session_id"
        :thread-id="thread.id"
        embedded
        @changed="emit('refresh')"
      />
      <AttachTerminal
        v-else-if="thread.interactive && latestJobID"
        :job-id="latestJobID"
        mode="write"
        @exit="emit('refresh')"
        @error="actionError = $event"
      />
      <div v-else-if="latestJobID" class="log-wrap">
        <p v-if="streamError" class="stream-error mono">SSE：{{ streamError }}</p>
        <LogTape :stdout="stdout" :stderr="stderr" :live="live" mode="live" />
      </div>
      <p v-else class="empty mono">这个会话没有关联 job。</p>
    </div>

    <footer v-if="thread.kind !== 'relay' && !thread.interactive" class="turn-composer">
      <textarea
        ref="turnInput"
        v-model="draft"
        class="turn-input mono"
        rows="3"
        :disabled="!canTurn"
        :placeholder="oneShot ? '该一次性批处理没有 session，无法续接' : live ? '当前 job 仍在运行，结束后可继续会话' : '下一句…（Ctrl/Cmd+Enter）'"
        @keydown="onTurnKeydown"
      ></textarea>
      <button class="turn-send mono" type="button" :disabled="!canTurn || !draft.trim()" @click="sendTurn">{{ sending ? '发送中…' : '发送' }}</button>
    </footer>
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
.status--blocked { color: var(--fail); border-color: var(--fail); }
.status--working { color: var(--phosphor); border-color: var(--phosphor); }
.status--review { color: var(--run); border-color: var(--run); }
.status--done { color: var(--done); border-color: var(--done); }
.head-actions { display: flex; gap: 6px; }
.head-actions button, .back { color: var(--paper); background: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: 5px 8px; }
.head-actions button:disabled { opacity: .4; }
.thread-meta { grid-column: 1 / -1; display: flex; gap: 7px; min-width: 0; overflow: hidden; color: var(--queue); font-size: 11px; }
.thread-meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.error, .stream-error { flex: none; margin: 0; padding: 7px 12px; color: var(--fail); background: rgba(200,70,70,.07); }
.interaction-area { flex: none; max-height: 40%; overflow-y: auto; padding: 0 12px; }
.interaction-area:empty { display: none; }
.thread-content { flex: 1; min-height: 0; overflow: hidden; }
.thread-content > :deep(*) { height: 100%; }
.log-wrap { height: 100%; display: flex; flex-direction: column; padding: 10px; }
.log-wrap :deep(.tape) { flex: 1; min-height: 0; }
.turn-composer { flex: none; display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 8px; padding: 10px 12px; background: var(--panel); border-top: 1px solid var(--line); }
.turn-input { resize: vertical; color: var(--paper); background: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: 8px; }
.turn-input:focus { outline: 1px solid var(--phosphor); border-color: var(--phosphor); }
.turn-send { color: var(--ink); background: var(--phosphor); border: 1px solid var(--phosphor); border-radius: var(--radius); padding: 0 16px; font-weight: 700; }
.turn-send:disabled { opacity: .4; }
.empty { color: var(--queue); padding: 24px; }
@media (max-width: 760px) { .thread-head { grid-template-columns: 1fr; } .back { display: inline-block; justify-self: start; } .head-actions { flex-wrap: wrap; } .thread-meta { grid-column: 1; } .turn-composer { grid-template-columns: 1fr; } .turn-send { min-height: 36px; } }
</style>
