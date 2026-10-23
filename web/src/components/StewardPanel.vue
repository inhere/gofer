<script setup lang="ts">
// 「问管家」：工作页右下角悬浮按钮 + 面板（手机全屏底部抽屉，桌面右下角浮层）。
// 管家是一个常驻的持续 ACP 会话 job：这里订阅它的 ACP 事件流显示对话，发送走
// POST /v1/steward/ask（未启动时首条消息会自动启动管家）。job 换了 = 管家被重建，重新订阅。
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { ApiError } from '../api/client'
import { streamACPJob } from '../api/sse'
import { askSteward, getSteward, restartSteward, type StewardStatus } from '../api/steward'
import type { SSEEvent } from '../api/types'
import { isACPEvent, reduceACPEvents, type ACPEvent } from './workbench/acpEvents'
import { displayPrompt, nextSubscription, quickQuestions, stateLabel, stewardBusyText } from '../utils/steward'
import { t } from '../i18n'
import MarkdownBlock from './MarkdownBlock.vue'
import StewardNotes from './StewardNotes.vue'

const POLL_MS = 4000

const open = ref(false)
const status = ref<StewardStatus | null>(null)
const events = ref<ACPEvent[]>([])
const subscribed = ref('')
const draft = ref('')
const sending = ref(false)
const error = ref('')
const rebuilt = ref(false)
const notesOpen = ref(false)
const bodyEl = ref<HTMLElement | null>(null)
let controller: AbortController | null = null
let timer: number | null = null

const messages = computed(() => reduceACPEvents(events.value).filter((e) => e.kind === 'prompt' || e.kind === 'message'))
const enabled = computed(() => status.value?.enabled ?? true)
const label = computed(() => stateLabel(status.value?.state ?? 'not_started', enabled.value))

function textOf(m: ACPEvent): string {
  return m.kind === 'prompt' || m.kind === 'message' ? m.text : ''
}

function scrollEnd(): void {
  void nextTick(() => {
    if (bodyEl.value) bodyEl.value.scrollTop = bodyEl.value.scrollHeight
  })
}

function unsubscribe(): void {
  controller?.abort()
  controller = null
}

async function subscribe(jobId: string): Promise<void> {
  unsubscribe()
  subscribed.value = jobId
  events.value = []
  if (!jobId) return
  const ctrl = new AbortController()
  controller = ctrl
  try {
    await streamACPJob(jobId, {
      tail: 300,
      signal: ctrl.signal,
      onEvent: (frame: SSEEvent) => {
        if (frame.type !== 'acp' || !isACPEvent(frame.data)) return
        events.value = [...events.value, frame.data]
        scrollEnd()
      },
    })
  } catch (e) {
    if (!ctrl.signal.aborted) error.value = e instanceof Error ? e.message : String(e)
  }
}

function follow(reported: string | undefined): void {
  const sub = nextSubscription(subscribed.value, reported)
  if (!sub.resubscribe) return
  rebuilt.value = sub.rebuilt
  void subscribe(sub.jobId)
}

async function refresh(): Promise<void> {
  try {
    status.value = (await getSteward()).status
    follow(status.value.job_id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

function startPolling(): void {
  stopPolling()
  timer = window.setInterval(() => void refresh(), POLL_MS)
}
function stopPolling(): void {
  if (timer !== null) {
    window.clearInterval(timer)
    timer = null
  }
}

watch(open, (v) => {
  if (v) {
    void refresh()
    startPolling()
  } else {
    stopPolling()
    unsubscribe()
    subscribed.value = ''
    events.value = []
  }
})

async function send(text?: string): Promise<void> {
  const t = (text ?? draft.value).trim()
  if (!t || sending.value || !enabled.value) return
  sending.value = true
  error.value = ''
  try {
    const r = await askSteward(t)
    if (text === undefined) draft.value = ''
    follow(r.job_id)
    await refresh()
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      error.value = /busy/i.test(`${e.detail ?? ''} ${e.message}`) ? stewardBusyText() : e.detail || e.message
    } else {
      error.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    sending.value = false
  }
}

function onKey(ev: KeyboardEvent): void {
  if (ev.key === 'Enter' && !ev.shiftKey && !ev.isComposing) {
    ev.preventDefault()
    void send()
  }
}

async function restart(): Promise<void> {
  if (!window.confirm(t('util.steward.panel.restartConfirm'))) return
  error.value = ''
  try {
    const r = await restartSteward()
    status.value = r.status
    follow(r.job_id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

onUnmounted(() => {
  stopPolling()
  unsubscribe()
})

defineExpose({ open })
</script>

<template>
  <div class="steward">
    <button v-if="!open" class="fab mono" type="button" data-test="steward-fab" @click="open = true">{{ t('util.steward.panel.fab') }}</button>

    <section v-if="open" class="panel" role="dialog" :aria-label="t('util.steward.panel.fab')" data-test="steward-panel">
      <header class="head">
        <div class="head-main mono">
          <strong>{{ t('util.steward.panel.title') }}</strong>
          <span v-if="status?.agent" class="muted" data-test="steward-agent">{{ status.agent }}</span>
          <span class="state" :class="`state--${enabled ? status?.state ?? 'not_started' : 'off'}`" data-test="steward-state">{{ label }}</span>
        </div>
        <div class="head-actions">
          <button v-if="enabled" class="mini mono" type="button" data-test="steward-notes-btn" @click="notesOpen = true">{{ t('util.steward.panel.notes') }}</button>
          <button v-if="enabled" class="mini mono" type="button" data-test="steward-restart" @click="restart">{{ t('util.steward.panel.restart') }}</button>
          <button class="mini mono" type="button" data-test="steward-close" @click="open = false">{{ t('util.action.collapse') }}</button>
        </div>
      </header>

      <div v-if="!enabled" class="disabled mono" data-test="steward-disabled">
        {{ t('util.steward.panel.disabled') }}<RouterLink to="/settings/work">{{ t('util.steward.panel.openSettings') }}</RouterLink>
      </div>

      <template v-else>
        <div ref="bodyEl" class="body" data-test="steward-body">
          <p v-if="status?.agent_error" class="err mono">{{ status.agent_error }}</p>
          <p v-if="rebuilt" class="note mono" data-test="steward-rebuilt">{{ t('util.steward.panel.rebuilt') }}</p>
          <p v-if="!messages.length && status?.state === 'not_started'" class="note mono" data-test="steward-hint">{{ t('util.steward.panel.hint') }}</p>
          <div v-for="m in messages" :key="`${m.kind}-${m.seq}`" class="bubble" :class="m.kind === 'prompt' ? 'bubble--user' : 'bubble--bot'" data-test="steward-msg">
            <div class="role mono">{{ m.kind === 'prompt' ? 'YOU' : 'STEWARD' }}</div>
            <div v-if="m.kind === 'prompt'" class="plain">{{ displayPrompt(textOf(m)) }}</div>
            <MarkdownBlock v-else :text="textOf(m)" />
          </div>
        </div>

        <p v-if="error" class="err mono" data-test="steward-error">{{ error }}</p>
        <div class="quick" data-test="steward-quick">
          <button v-for="q in quickQuestions()" :key="q" class="chip mono" type="button" :disabled="sending" @click="send(q)">{{ q }}</button>
        </div>
        <div class="compose">
          <textarea v-model="draft" rows="2" class="mono" :placeholder="t('util.steward.panel.placeholder')" :disabled="sending" data-test="steward-input" @keydown="onKey"></textarea>
          <button class="send mono" type="button" :disabled="sending || !draft.trim()" data-test="steward-send" @click="send()">{{ sending ? t('util.steward.panel.sending') : t('util.steward.panel.send') }}</button>
        </div>
      </template>
    </section>

    <StewardNotes v-if="notesOpen" @close="notesOpen = false" />
  </div>
</template>

<style scoped>
.fab { position: fixed; right: 16px; bottom: calc(16px + env(safe-area-inset-bottom, 0px)); z-index: 60; padding: 10px 16px; border-radius: 22px; border: 1px solid var(--accent); background: var(--accent); color: var(--bg); font-size: 13px; cursor: pointer; box-shadow: 0 4px 14px rgba(0, 0, 0, 0.4); }
.panel { position: fixed; right: 16px; bottom: 16px; z-index: 70; display: flex; flex-direction: column; width: min(420px, calc(100vw - 32px)); height: min(600px, calc(100vh - 32px)); background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-chip); box-shadow: 0 8px 28px rgba(0, 0, 0, 0.5); overflow: hidden; }
.head { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 8px 10px; border-bottom: 1px solid var(--line); flex: none; }
.head-main { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: 12px; color: var(--ink); }
.head-actions { display: flex; gap: 6px; flex: none; }
.mini { padding: 3px 8px; font-size: 11px; color: var(--ink); background: transparent; border: 1px solid var(--line); border-radius: var(--r-chip); cursor: pointer; }
.muted { color: var(--ink-3); }
.state { padding: 0 8px; border: 1px solid var(--line); border-radius: 9px; font-size: 10px; color: var(--ink-3); }
.state--idle { color: var(--done); border-color: var(--done); }
.state--running { color: var(--run); border-color: var(--run); }
.disabled { padding: 24px 14px; font-size: 13px; color: var(--ink); text-align: center; }
.disabled a { color: var(--accent); }
.body { flex: 1; overflow-y: auto; padding: 10px; display: flex; flex-direction: column; gap: 8px; }
.bubble { max-width: 92%; border: 1px solid var(--line); border-radius: 10px; padding: 7px 10px; font-size: 13px; }
.bubble--user { align-self: flex-end; background: rgba(100, 170, 255, 0.09); }
.bubble--bot { align-self: flex-start; background: var(--bg); }
.role { margin-bottom: 4px; font-size: 9px; letter-spacing: 0.08em; color: var(--ink-3); }
.plain { white-space: pre-wrap; word-break: break-word; }
.note { margin: 0; font-size: 11px; color: var(--ink-3); text-align: center; }
.err { margin: 0 10px 6px; padding: 6px 8px; font-size: 11px; color: var(--fail); border: 1px solid var(--fail); border-radius: var(--r-chip); word-break: break-word; }
.quick { display: flex; flex-wrap: wrap; gap: 6px; padding: 0 10px 6px; flex: none; }
.chip { padding: 4px 10px; font-size: 11px; color: var(--ink); background: var(--bg); border: 1px solid var(--line); border-radius: 12px; cursor: pointer; }
.chip:disabled, .send:disabled { opacity: 0.45; cursor: not-allowed; }
.compose { display: flex; gap: 6px; padding: 6px 10px 10px; border-top: 1px solid var(--line); flex: none; }
.compose textarea { flex: 1; min-width: 0; resize: none; color: var(--ink); background: var(--bg); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 6px 8px; font-size: 13px; }
.send { padding: 0 14px; color: var(--bg); background: var(--accent); border: 0; border-radius: var(--r-chip); cursor: pointer; }
@media (max-width: 760px) {
  /* 手机：全屏底部抽屉；悬浮按钮抬高，免得盖住底部操作 */
  .panel { inset: 0; right: 0; bottom: 0; width: 100vw; height: 100dvh; border-radius: 0; }
  .fab { bottom: calc(76px + env(safe-area-inset-bottom, 0px)); }
}
</style>
