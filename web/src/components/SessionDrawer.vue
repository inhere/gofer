<script setup lang="ts">
// 会话中继（SESS-01）详情抽屉：右侧滑入面板。
//  - 头部：元数据（sid 可复制 / agent / project / runner / cwd / transcript /
//    started / last_seen / 状态徽章 / 中继开关）。
//  - 中间：turn 时间线。后端 newest-first，这里反转成最旧在上、最新在下（聊天习惯）；
//    每个 turn 两条气泡：灰 = agent 消息（question），蓝 = 人的回复（answer）。
//  - 底部：输入框 + 发送。仅存在 OPEN turn 时可用，走 POST /v1/sessions/{sid}/say；
//    回复 `/off` 会关闭中继让会话正常停下。Ctrl/Cmd+Enter 发送。
//  - 打开期间 3s 轮询详情（页面可见时）。
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  deleteAgentSession,
  getAgentSession,
  saySession,
  setSessionRelay,
} from '../api/client'
import { fmtAgo, fmtDateTime } from '../api/time'
import type { AgentSession, AgentSessionState, Decision } from '../api/types'

const props = defineProps<{ sid: string }>()
const emit = defineEmits<{
  (e: 'close'): void
  // 会话有变更（中继开关 / 作答 / 删除）→ 父级列表可即时刷新
  (e: 'changed'): void
  (e: 'deleted', sid: string): void
}>()

const POLL_MS = 3000
const TURNS_LIMIT = 50
// 超过此长度的 agent 消息默认折叠
const COLLAPSE_CHARS = 600

const session = ref<AgentSession | null>(null)
const turns = ref<Decision[]>([])
const loading = ref(false)
const error = ref('')
const actionError = ref('')
const draft = ref('')
const sending = ref(false)
const relayBusy = ref(false)
const deleting = ref(false)
const copied = ref(false)
const expanded = ref<Set<string>>(new Set())
const nowSec = ref(Math.floor(Date.now() / 1000))
const timelineEl = ref<HTMLElement | null>(null)

let timer: number | null = null
let clock: number | null = null

const STATE_LABELS: Record<AgentSessionState, string> = {
  running: '执行中',
  idle: '空闲',
  waiting_reply: '等待回复',
  needs_attention: '需注意',
  ended: '已结束',
}

function stateLabel(s: AgentSessionState): string {
  return STATE_LABELS[s] ?? s
}

// 时间线：最旧在上、最新在下
const timeline = computed(() => [...turns.value].reverse())
const openTurn = computed(() => turns.value.find((t) => t.state === 'OPEN') ?? null)
const canSend = computed(() => !!openTurn.value && !sending.value && session.value?.state !== 'ended')

const titleText = computed(() => {
  const s = session.value
  if (!s) {
    return props.sid.slice(0, 8)
  }
  return s.title || `${s.agent} · ${s.session_id.slice(0, 8)}`
})

function shortSid(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id
}

function isLong(text: string): boolean {
  return text.length > COLLAPSE_CHARS
}

function shown(text: string, id: string): string {
  if (!isLong(text) || expanded.value.has(id)) {
    return text
  }
  return `${text.slice(0, COLLAPSE_CHARS)}…`
}

function toggleExpand(id: string): void {
  const next = new Set(expanded.value)
  if (next.has(id)) {
    next.delete(id)
  } else {
    next.add(id)
  }
  expanded.value = next
}

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function scrollToBottom(): void {
  void nextTick(() => {
    const el = timelineEl.value
    if (el) {
      el.scrollTop = el.scrollHeight
    }
  })
}

async function load(opts?: { silent?: boolean }): Promise<void> {
  if (!opts?.silent) {
    loading.value = true
  }
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const resp = await getAgentSession(props.sid, TURNS_LIMIT)
    const prevLast = turns.value[0]?.id
    const prevLen = turns.value.length
    session.value = resp.session
    turns.value = resp.turns ?? []
    error.value = ''
    // 有新 turn 时滚到底部
    if (turns.value.length !== prevLen || turns.value[0]?.id !== prevLast) {
      scrollToBottom()
    }
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    loading.value = false
  }
}

function startPolling(): void {
  stopPolling()
  if (document.hidden) {
    return
  }
  timer = window.setInterval(() => {
    void load({ silent: true })
  }, POLL_MS)
}

function stopPolling(): void {
  if (timer != null) {
    window.clearInterval(timer)
    timer = null
  }
}

function onVisibility(): void {
  if (document.hidden) {
    stopPolling()
  } else {
    void load({ silent: true })
    startPolling()
  }
}

async function copySid(): Promise<void> {
  try {
    await navigator.clipboard.writeText(props.sid)
    copied.value = true
    window.setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    // 剪贴板不可用（非安全上下文等）时静默：用户仍可手动选择文本复制。
  }
}

// 中继开关：乐观更新，失败回滚。
async function toggleRelay(): Promise<void> {
  const s = session.value
  if (!s || relayBusy.value) {
    return
  }
  const prev = s.relay
  const next = !prev
  relayBusy.value = true
  actionError.value = ''
  s.relay = next
  try {
    const updated = await setSessionRelay(s.session_id, next)
    session.value = updated
    emit('changed')
  } catch (e) {
    if (session.value) {
      session.value.relay = prev
    }
    actionError.value = `切换中继失败：${errorMessage(e)}`
  } finally {
    relayBusy.value = false
  }
}

async function send(): Promise<void> {
  const text = draft.value.trim()
  if (!text || !canSend.value) {
    return
  }
  sending.value = true
  actionError.value = ''
  try {
    await saySession(props.sid, text)
    draft.value = ''
    await load({ silent: true })
    scrollToBottom()
    emit('changed')
  } catch (e) {
    actionError.value = `发送失败：${errorMessage(e)}`
  } finally {
    sending.value = false
  }
}

function onKeydown(ev: KeyboardEvent): void {
  if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) {
    ev.preventDefault()
    void send()
  }
}

async function remove(): Promise<void> {
  if (deleting.value) {
    return
  }
  if (!window.confirm(`移除会话登记 ${shortSid(props.sid)}？只删除 gofer 侧登记，不影响终端里的 agent 进程。`)) {
    return
  }
  deleting.value = true
  actionError.value = ''
  try {
    await deleteAgentSession(props.sid)
    emit('deleted', props.sid)
    emit('close')
  } catch (e) {
    actionError.value = `移除失败：${errorMessage(e)}`
  } finally {
    deleting.value = false
  }
}

function onEsc(ev: KeyboardEvent): void {
  if (ev.key === 'Escape') {
    emit('close')
  }
}

watch(
  () => props.sid,
  () => {
    session.value = null
    turns.value = []
    draft.value = ''
    error.value = ''
    actionError.value = ''
    expanded.value = new Set()
    void load().then(scrollToBottom)
    startPolling()
  },
)

onMounted(() => {
  void load().then(scrollToBottom)
  startPolling()
  clock = window.setInterval(() => {
    nowSec.value = Math.floor(Date.now() / 1000)
  }, 10000)
  document.addEventListener('visibilitychange', onVisibility)
  document.addEventListener('keydown', onEsc)
})

onUnmounted(() => {
  stopPolling()
  if (clock != null) {
    window.clearInterval(clock)
    clock = null
  }
  document.removeEventListener('visibilitychange', onVisibility)
  document.removeEventListener('keydown', onEsc)
})
</script>

<template>
  <div class="drawer-overlay" @click.self="emit('close')">
    <div class="drawer-panel" role="dialog" aria-label="会话详情">
      <div class="drawer-head">
        <div class="head-main">
          <span class="drawer-title mono" :title="titleText">{{ titleText }}</span>
          <span
            v-if="session"
            class="state-badge mono"
            :class="`state--${session.state}`"
          >
            {{ stateLabel(session.state) }}
          </span>
        </div>
        <div class="head-actions mono">
          <label v-if="session" class="relay-toggle" :class="{ on: session.relay, busy: relayBusy }" title="中继开关：开着时会话每次停下都会在这里等你回复">
            <input
              type="checkbox"
              :checked="session.relay"
              :disabled="relayBusy || session.state === 'ended'"
              @change="toggleRelay"
            />
            <span class="relay-track"><span class="relay-knob"></span></span>
            <span class="relay-text">中继 {{ session.relay ? 'ON' : 'OFF' }}</span>
          </label>
          <button class="act mono" type="button" :disabled="loading" @click="load()">
            {{ loading ? '刷新中…' : '刷新' }}
          </button>
          <button class="act act--warn mono" type="button" :disabled="deleting" @click="remove">
            {{ deleting ? '移除中…' : '移除登记' }}
          </button>
          <button class="act mono" type="button" @click="emit('close')">关闭</button>
        </div>
      </div>

      <p v-if="error" class="error mono">{{ error }}</p>

      <dl v-if="session" class="meta mono">
        <dt>sid</dt>
        <dd class="meta-sid">
          <span :title="session.session_id">{{ session.session_id }}</span>
          <button class="copy-btn mono" type="button" @click="copySid">
            {{ copied ? '已复制' : '复制' }}
          </button>
        </dd>
        <dt>agent</dt>
        <dd>{{ session.agent }}</dd>
        <dt>project</dt>
        <dd>{{ session.project_key || '—' }}</dd>
        <dt>runner</dt>
        <dd>{{ session.runner || '—' }}</dd>
        <dt>cwd</dt>
        <dd class="meta-path" :title="session.cwd">{{ session.cwd || '—' }}</dd>
        <dt>transcript</dt>
        <dd class="meta-path" :title="session.transcript">{{ session.transcript || '—' }}</dd>
        <dt v-if="session.tmux_pane">tmux</dt>
        <dd v-if="session.tmux_pane">{{ session.tmux_pane }}</dd>
        <dt>started</dt>
        <dd>{{ fmtDateTime(session.started_at) }}</dd>
        <dt>last_seen</dt>
        <dd :title="fmtDateTime(session.last_seen_at)">
          {{ fmtAgo(session.last_seen_at, nowSec) }}
          <span v-if="session.last_event" class="dim">· {{ session.last_event }}</span>
        </dd>
        <dt v-if="session.ended_at">ended</dt>
        <dd v-if="session.ended_at">{{ fmtDateTime(session.ended_at) }}</dd>
        <dt>turns</dt>
        <dd>{{ session.turn_no }}</dd>
      </dl>

      <div ref="timelineEl" class="timeline">
        <div v-if="!loading && timeline.length === 0" class="empty mono">
          暂无 turn。打开中继后，会话下一次停下时消息会出现在这里。
        </div>
        <template v-for="t in timeline" :key="t.id">
          <div class="turn">
            <div class="bubble bubble--agent">
              <div class="bubble-meta mono">
                <span>{{ session?.agent || 'agent' }}</span>
                <span :title="fmtDateTime(t.asked_at)">{{ fmtAgo(t.asked_at, nowSec) }}</span>
              </div>
              <pre class="bubble-text">{{ shown(t.question, t.id) || '（无消息）' }}</pre>
              <button
                v-if="isLong(t.question)"
                class="link-btn mono"
                type="button"
                @click="toggleExpand(t.id)"
              >
                {{ expanded.has(t.id) ? '收起' : `展开全文（${t.question.length} 字）` }}
              </button>
            </div>
            <div v-if="t.state === 'ANSWERED'" class="bubble bubble--human">
              <div class="bubble-meta mono">
                <span>{{ t.answered_by || 'web' }}</span>
                <span :title="fmtDateTime(t.answered_at)">{{ fmtAgo(t.answered_at, nowSec) }}</span>
              </div>
              <pre class="bubble-text">{{ t.answer }}</pre>
            </div>
            <div v-else-if="t.state === 'EXPIRED'" class="bubble bubble--expired mono">
              已过期 / 未回复
            </div>
            <div v-else class="bubble bubble--pending mono">
              等待回复…
            </div>
          </div>
        </template>
      </div>

      <div class="composer">
        <p v-if="actionError" class="error mono">{{ actionError }}</p>
        <textarea
          v-model="draft"
          class="composer-input mono"
          rows="3"
          :disabled="!canSend"
          :placeholder="canSend ? '回复 agent…（Ctrl/Cmd+Enter 发送；输入 /off 关闭中继，让会话正常停下）' : '会话未在等待回复'"
          @keydown="onKeydown"
        ></textarea>
        <div class="composer-foot mono">
          <span class="hint">
            <template v-if="openTurn">回复将原样进入 agent 上下文；输入 <code>/off</code> 关闭中继并让会话正常停下。</template>
            <template v-else-if="session?.state === 'ended'">会话已结束。</template>
            <template v-else>会话未在等待回复{{ session && !session.relay ? '（中继未开，拨开开关后下一次停下生效）' : '' }}。</template>
          </span>
          <button
            class="act act--primary mono"
            type="button"
            :disabled="!canSend || !draft.trim()"
            @click="send"
          >
            {{ sending ? '发送中…' : '发送' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.drawer-overlay {
  position: fixed;
  inset: 0;
  z-index: 80; /* above EscalationBell (60) and InteractionToast (70): the composer must stay clickable */
  display: flex;
  justify-content: flex-end;
  background: rgba(0, 0, 0, 0.6);
}
.drawer-panel {
  display: flex;
  flex-direction: column;
  width: min(760px, 94vw);
  height: 100%;
  background: var(--panel);
  border-left: 1px solid var(--line);
  overflow: hidden;
}
.drawer-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
  flex: none;
}
.head-main {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  flex: 1 1 auto;
}
.drawer-title {
  min-width: 0;
  color: var(--paper);
  font-size: 12px;
  letter-spacing: 0.04em;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
}
.state-badge {
  flex: none;
  border: 1px solid var(--line);
  border-radius: 9px;
  padding: 1px 7px;
  font-size: 10px;
  color: var(--queue);
}
.state--running {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.state--waiting_reply {
  color: var(--run);
  border-color: var(--run);
}
.state--needs_attention {
  color: var(--fail);
  border-color: var(--fail);
}
.state--ended {
  opacity: 0.6;
}

.relay-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  font-size: 11px;
  color: var(--queue);
  user-select: none;
}
.relay-toggle input {
  position: absolute;
  opacity: 0;
  width: 0;
  height: 0;
}
.relay-track {
  position: relative;
  width: 28px;
  height: 14px;
  border-radius: 7px;
  border: 1px solid var(--line);
  background: transparent;
  transition: background 0.15s, border-color 0.15s;
}
.relay-knob {
  position: absolute;
  top: 1px;
  left: 1px;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--queue);
  transition: transform 0.15s, background 0.15s;
}
.relay-toggle.on .relay-track {
  border-color: var(--phosphor);
  background: rgba(79, 176, 198, 0.18);
}
.relay-toggle.on .relay-knob {
  transform: translateX(14px);
  background: var(--phosphor);
}
.relay-toggle.on .relay-text {
  color: var(--phosphor);
}
.relay-toggle.busy {
  opacity: 0.6;
  cursor: progress;
}

.act {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
  cursor: pointer;
}
.act:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.act:disabled {
  opacity: 0.45;
  cursor: default;
}
.act--primary {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.act--primary:hover:not(:disabled) {
  background: var(--phosphor);
  color: var(--ink);
}
.act--warn:hover:not(:disabled) {
  border-color: var(--fail);
  color: var(--fail);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 6px 10px;
  margin: 10px 14px 0;
  word-break: break-word;
}

.meta {
  flex: none;
  display: grid;
  grid-template-columns: 84px minmax(0, 1fr) 84px minmax(0, 1fr);
  gap: 4px 10px;
  margin: 0;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
  font-size: 11px;
}
.meta dt {
  color: var(--queue);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.meta dd {
  margin: 0;
  color: var(--paper);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta-sid {
  display: flex;
  align-items: center;
  gap: 8px;
  grid-column: 2 / span 3;
}
.meta-sid span {
  overflow: hidden;
  text-overflow: ellipsis;
}
.meta-path {
  grid-column: 2 / span 3;
}
.copy-btn {
  flex: none;
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: 3px;
  padding: 0 6px;
  font-size: 10px;
  cursor: pointer;
}
.copy-btn:hover {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.dim {
  color: var(--queue);
}

.timeline {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.turn {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.bubble {
  max-width: 88%;
  border-radius: 8px;
  padding: 8px 11px;
  font-size: 13px;
  line-height: 1.5;
}
.bubble--agent {
  align-self: flex-start;
  background: var(--ink);
  border: 1px solid var(--line);
  color: var(--paper);
}
.bubble--human {
  align-self: flex-end;
  background: rgba(79, 176, 198, 0.16);
  border: 1px solid var(--phosphor);
  color: var(--paper);
}
.bubble--expired,
.bubble--pending {
  align-self: flex-end;
  font-size: 11px;
  color: var(--queue);
  border: 1px dashed var(--line);
}
.bubble--pending {
  color: var(--run);
  border-color: var(--run);
}
.bubble-meta {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  font-size: 10px;
  color: var(--queue);
  margin-bottom: 4px;
}
.bubble-text {
  margin: 0;
  font-family: var(--font-mono);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-word;
}
.link-btn {
  background: transparent;
  border: none;
  color: var(--phosphor);
  padding: 4px 0 0;
  font-size: 11px;
  cursor: pointer;
}
.link-btn:hover {
  text-decoration: underline;
}
.empty {
  color: var(--queue);
  font-size: 12px;
  text-align: center;
  padding: 28px 10px;
}

.composer {
  flex: none;
  border-top: 1px solid var(--line);
  padding: 10px 14px 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.composer .error {
  margin: 0;
}
.composer-input {
  width: 100%;
  box-sizing: border-box;
  resize: vertical;
  min-height: 64px;
  background: var(--ink);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
  font-size: 12px;
  line-height: 1.5;
}
.composer-input:focus {
  outline: none;
  border-color: var(--phosphor);
}
.composer-input:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.composer-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.hint {
  color: var(--queue);
  font-size: 11px;
  line-height: 1.4;
}
.hint code {
  color: var(--phosphor);
}

@media (max-width: 720px) {
  .meta {
    grid-template-columns: 76px minmax(0, 1fr);
  }
  .meta-sid,
  .meta-path {
    grid-column: 2;
  }
  .head-actions .relay-text {
    display: none;
  }
}
</style>
