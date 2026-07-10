<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { requestAttachTicket } from '../api/client'
import { buildAttachWsUrl, encodeInput, parseServerFrame } from '../api/attach'

type AttachMode = 'write' | 'read'
type ConnectionState =
  | 'connecting'
  | 'connected'
  | 'read-only'
  | 'reconnecting'
  | 'closed'
  | 'timeout'
  | 'failed'
  | 'exited'

const MAX_RECONNECT_ATTEMPTS = 6
const RECONNECT_WINDOW_MS = 5 * 60_000

const props = withDefaults(
  defineProps<{
    jobId: string
    mode?: AttachMode
  }>(),
  { mode: 'write' },
)

const emit = defineEmits<{
  exit: [code?: number]
  closed: []
  error: [msg: string]
}>()

const hostEl = ref<HTMLDivElement | null>(null)
const writeGranted = ref(false)
const connectionState = ref<ConnectionState>('connecting')
const reconnectAttempts = ref(0)
const showManualReconnect = ref(false)
const exited = ref(false)

let term: Terminal | null = null
let fit: FitAddon | null = null
let ws: WebSocket | null = null
let gotExit = false
let userClosed = false
let firstConnectAt = Date.now()
let resizeTimer: number | null = null
let reconnectTimer: number | null = null
let attachMode: AttachMode = props.mode

const statusText = computed(() => {
  switch (connectionState.value) {
    case 'connecting':
      return '连接中'
    case 'connected':
      return '已连接'
    case 'read-only':
      return '只读'
    case 'reconnecting':
      return `连接断开，重连中… (${reconnectAttempts.value})`
    case 'timeout':
      return '会话超时(5min)'
    case 'failed':
      return '重连失败'
    case 'exited':
      return '进程已退出'
    case 'closed':
      return '断开'
    default:
      return '断开'
  }
})

const readOnlyBanner = computed(() => {
  if (writeGranted.value || connectionState.value === 'reconnecting') {
    return ''
  }
  if (attachMode === 'read') {
    return '只读跟随'
  }
  return '他人正在操作，已只读跟随'
})

const canClaimWrite = computed(
  () =>
    props.mode === 'write' &&
    !writeGranted.value &&
    connectionState.value === 'read-only',
)

function nextReconnectDelay(n: number): number {
  return Math.min(1000 * 2 ** (n - 1), 15000)
}

function shouldReconnect(): boolean {
  if (gotExit || userClosed) {
    return false
  }
  if (Date.now() - firstConnectAt > RECONNECT_WINDOW_MS) {
    return false
  }
  return reconnectAttempts.value < MAX_RECONNECT_ATTEMPTS
}

function token(name: string, fallback: string): string {
  const value = getComputedStyle(document.documentElement)
    .getPropertyValue(name)
    .trim()
  return value || fallback
}

function buildTerminal(): Terminal {
  const background = token('--term-bg', '#08121A')
  const foreground = token('--paper', '#E8E2D4')
  const cursor = token('--phosphor', '#4FB0C6')
  const line = token('--line', '#2A3D49')
  const queue = token('--queue', '#6B8A99')
  const run = token('--run', '#E0A24A')
  const fail = token('--fail', '#C8553D')

  return new Terminal({
    convertEol: false,
    cursorBlink: true,
    disableStdin: true,
    fontFamily: token('--font-mono', 'monospace'),
    fontSize: 13,
    theme: {
      background,
      foreground,
      cursor,
      cursorAccent: background,
      selectionBackground: line,
      black: background,
      red: fail,
      green: token('--done', '#5BA66E'),
      yellow: run,
      blue: cursor,
      magenta: queue,
      cyan: cursor,
      white: foreground,
      brightBlack: line,
      brightRed: fail,
      brightGreen: token('--done', '#5BA66E'),
      brightYellow: run,
      brightBlue: cursor,
      brightMagenta: queue,
      brightCyan: cursor,
      brightWhite: foreground,
    },
  })
}

function setWriteGranted(granted: boolean): void {
  writeGranted.value = granted
  if (term) {
    term.options.disableStdin = !granted
  }
  connectionState.value = granted ? 'connected' : 'read-only'
}

function sendFrame(frame: object): void {
  if (writeGranted.value && ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(frame))
  }
}

function sendInput(data: string): void {
  sendFrame({ t: 'i', d: encodeInput(data) })
}

async function copySelection(): Promise<void> {
  const text = term?.getSelection() ?? ''
  if (!text) {
    return
  }
  try {
    if (!navigator.clipboard?.writeText) {
      throw new Error('clipboard api unavailable')
    }
    await navigator.clipboard.writeText(text)
  } catch {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  }
}

async function pasteClipboard(): Promise<void> {
  if (!writeGranted.value || !navigator.clipboard?.readText) {
    return
  }
  try {
    const text = await navigator.clipboard.readText()
    if (text) {
      term?.paste(text)
    }
  } catch {
    emit('error', '读取剪贴板失败')
  }
}

function isCtrlKey(ev: KeyboardEvent, key: string): boolean {
  return (ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === key
}

function onTerminalKey(ev: KeyboardEvent): boolean {
  if (ev.type !== 'keydown') {
    return true
  }
  if (isCtrlKey(ev, 'c')) {
    ev.preventDefault()
    ev.stopPropagation()
    if (term?.hasSelection()) {
      void copySelection()
    } else {
      sendInput('\x03')
    }
    return false
  }
  if (isCtrlKey(ev, 'v')) {
    if (!navigator.clipboard?.readText) {
      return true
    }
    ev.preventDefault()
    ev.stopPropagation()
    void pasteClipboard()
    return false
  }
  if (ev.key === 'Escape') {
    ev.preventDefault()
    ev.stopPropagation()
    sendInput('\x1b')
    return false
  }
  return true
}

function closeSocket(): void {
  if (!ws) {
    return
  }
  ws.onopen = null
  ws.onmessage = null
  ws.onerror = null
  ws.onclose = null
  ws.close()
  ws = null
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    reconnectTimer = window.setTimeout(() => {
      reconnectTimer = null
      resolve()
    }, ms)
  })
}

async function connect(): Promise<void> {
  if (userClosed) {
    return
  }
  closeSocket()
  showManualReconnect.value = false
  connectionState.value =
    reconnectAttempts.value > 0 ? 'reconnecting' : 'connecting'

  const { ticket } = await requestAttachTicket(props.jobId, attachMode)
  if (userClosed) {
    return
  }

  const socket = new WebSocket(buildAttachWsUrl(props.jobId, ticket))
  socket.binaryType = 'arraybuffer'
  ws = socket

  socket.onmessage = (ev: MessageEvent) => {
    if (ev.data instanceof ArrayBuffer) {
      term?.write(new Uint8Array(ev.data))
      return
    }
    if (typeof ev.data !== 'string') {
      return
    }

    const frame = parseServerFrame(ev.data)
    if (!frame) {
      return
    }
    if (frame.t === 'hello') {
      reconnectAttempts.value = 0
      const cols = frame.cols || 80
      const rows = frame.rows || 24
      term?.resize(cols, rows)
      setWriteGranted(frame.write)
      return
    }
    if (frame.t === 'x') {
      gotExit = true
      exited.value = true
      connectionState.value = 'exited'
      emit('exit', frame.code)
    }
  }

  socket.onerror = () => {
    emit('error', 'websocket 错误')
  }

  socket.onclose = () => {
    if (ws === socket) {
      ws = null
    }
    void handleClose()
  }
}

async function handleClose(): Promise<void> {
  writeGranted.value = false
  if (term) {
    term.options.disableStdin = true
  }
  if (gotExit || userClosed) {
    connectionState.value = gotExit ? 'exited' : 'closed'
    emit('closed')
    return
  }

  if (Date.now() - firstConnectAt > RECONNECT_WINDOW_MS) {
    connectionState.value = 'timeout'
    showManualReconnect.value = true
    return
  }
  if (!shouldReconnect()) {
    connectionState.value = 'failed'
    showManualReconnect.value = true
    return
  }

  reconnectAttempts.value += 1
  connectionState.value = 'reconnecting'
  await sleep(nextReconnectDelay(reconnectAttempts.value))
  if (!shouldReconnect()) {
    await handleClose()
    return
  }
  try {
    await connect()
  } catch (e) {
    emit('error', e instanceof Error ? e.message : String(e))
    if (!userClosed) {
      void handleClose()
    }
  }
}

async function reconnect(mode: AttachMode = attachMode): Promise<void> {
  attachMode = mode
  gotExit = false
  exited.value = false
  reconnectAttempts.value = 0
  firstConnectAt = Date.now()
  showManualReconnect.value = false
  term?.clear()
  try {
    await connect()
  } catch (e) {
    connectionState.value = 'failed'
    showManualReconnect.value = true
    emit('error', e instanceof Error ? e.message : String(e))
  }
}

function onWindowResize(): void {
  if (resizeTimer != null) {
    window.clearTimeout(resizeTimer)
  }
  resizeTimer = window.setTimeout(() => {
    resizeTimer = null
    fit?.fit()
  }, 120)
}

onMounted(async () => {
  firstConnectAt = Date.now()
  term = buildTerminal()
  fit = new FitAddon()
  term.loadAddon(fit)
  term.attachCustomKeyEventHandler(onTerminalKey)
  if (hostEl.value) {
    term.open(hostEl.value)
    fit.fit()
  }
  term.onData((s) => {
    sendInput(s)
  })
  term.onResize(({ cols, rows }) => {
    sendFrame({ t: 'r', cols, rows })
  })
  window.addEventListener('resize', onWindowResize)

  try {
    await connect()
  } catch (e) {
    connectionState.value = 'failed'
    showManualReconnect.value = true
    emit('error', e instanceof Error ? e.message : String(e))
  }
})

onUnmounted(() => {
  userClosed = true
  closeSocket()
  window.removeEventListener('resize', onWindowResize)
  if (resizeTimer != null) {
    window.clearTimeout(resizeTimer)
  }
  if (reconnectTimer != null) {
    window.clearTimeout(reconnectTimer)
  }
  term?.dispose()
  term = null
  fit = null
})
</script>

<template>
  <section class="attach-terminal">
    <header class="terminal-bar">
      <span class="status-dot" :class="connectionState" aria-hidden="true" />
      <span class="status-text mono">{{ statusText }}</span>
      <span v-if="readOnlyBanner" class="readonly-banner mono">
        {{ readOnlyBanner }}
      </span>
      <button
        v-if="canClaimWrite"
        class="terminal-btn mono"
        type="button"
        @click="reconnect('write')"
      >
        抢占写入
      </button>
      <button
        v-if="showManualReconnect"
        class="terminal-btn mono"
        type="button"
        @click="reconnect()"
      >
        重连
      </button>
    </header>
    <div ref="hostEl" class="terminal-host" />
    <footer v-if="exited" class="terminal-foot mono">进程已退出</footer>
  </section>
</template>

<style scoped>
.attach-terminal {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  overflow: hidden;
  background: var(--term-bg);
  border: 1px solid var(--line);
  color: var(--paper);
}

.terminal-bar {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
  min-height: 38px;
  padding: 8px 10px;
  border-bottom: 1px solid var(--line);
  background: var(--panel);
}

.status-dot {
  width: 8px;
  height: 8px;
  flex: none;
  border-radius: 50%;
  background: var(--queue);
}

.status-dot.connected {
  background: var(--run);
}

.status-dot.read-only,
.status-dot.reconnecting {
  background: var(--phosphor);
}

.status-dot.failed,
.status-dot.timeout {
  background: var(--fail);
}

.status-dot.exited,
.status-dot.closed {
  background: var(--line);
}

.status-text {
  flex: none;
  color: var(--paper);
  font-size: 12px;
}

.readonly-banner {
  min-width: 0;
  color: var(--queue);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.terminal-btn {
  flex: none;
  margin-left: auto;
  padding: 4px 8px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--term-bg);
  color: var(--phosphor);
  font-size: 12px;
}

.terminal-btn + .terminal-btn {
  margin-left: 0;
}

.terminal-btn:hover {
  border-color: var(--phosphor);
}

.terminal-host {
  flex: 1 1 auto;
  min-height: 0;
  padding: 8px;
  overflow: hidden;
  background: var(--term-bg);
}

.terminal-host :deep(.xterm) {
  height: 100%;
}

.terminal-host :deep(.xterm-viewport),
.terminal-host :deep(.xterm-screen) {
  background: var(--term-bg) !important;
}

.terminal-foot {
  flex: none;
  padding: 7px 10px;
  border-top: 1px solid var(--line);
  background: var(--panel);
  color: var(--queue);
  font-size: 12px;
}
</style>
