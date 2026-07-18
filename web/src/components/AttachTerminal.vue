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

const rootEl = ref<HTMLElement | null>(null)
const hostEl = ref<HTMLDivElement | null>(null)
const writeGranted = ref(false)
// serve 端广播的 pty 权威尺寸（hello / r 帧）；只读端始终跟随它渲染（tools-3xy）。
const serverCols = ref(0)
const serverRows = ref(0)
// 聊天式输入区（tools-j0e）：长文本先编辑确认再一次性写入 pty。
const chatText = ref('')
const chatSubmit = ref(true)
const chatCollapsed = ref(localStorage.getItem('attach-chat-collapsed') === '1')
const BASE_FONT_SIZE = 13
const connectionState = ref<ConnectionState>('connecting')
const reconnectAttempts = ref(0)
const showManualReconnect = ref(false)
const exited = ref(false)
const keyMenuEl = ref<HTMLDetailsElement | null>(null)
const terminalActive = ref(false)

let term: Terminal | null = null
let fit: FitAddon | null = null
let ws: WebSocket | null = null
let gotExit = false
let userClosed = false
let firstConnectAt = Date.now()
let resizeTimer: number | null = null
let reconnectTimer: number | null = null
let attachMode: AttachMode = props.mode

interface KeyAction {
  label: string
  data?: string
  paste?: boolean
}

const keyActions: KeyAction[] = [
  { label: 'Esc', data: '\x1b' },
  { label: 'Ctrl+C', data: '\x03' },
  { label: 'Ctrl+V', paste: true },
  { label: 'Ctrl+D', data: '\x04' },
  { label: 'Ctrl+Z', data: '\x1a' },
  { label: 'Ctrl+L', data: '\x0c' },
  { label: 'Enter', data: '\r' },
  { label: 'Tab', data: '\t' },
  { label: 'Up', data: '\x1b[A' },
  { label: 'Down', data: '\x1b[B' },
  { label: 'Left', data: '\x1b[D' },
  { label: 'Right', data: '\x1b[C' },
  { label: 'Home', data: '\x1b[H' },
  { label: 'End', data: '\x1b[F' },
  { label: 'PgUp', data: '\x1b[5~' },
  { label: 'PgDn', data: '\x1b[6~' },
]

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

// 尺寸跟随（tools-3xy）：pty 尺寸的唯一真源在 serve（hello/r 帧）。
//  - 写者：本地 FitAddon 主导（fit → onResize → 发 r → serve 广播），font 恢复默认；
//  - 只读端：**绝不 fit**（手机弹软键盘触发 window resize 会把本地 xterm 改小、与
//    pty 脱钩），改为按容器宽度自适应字号后 resize 到服务端尺寸，放不下横向滚动。
function applyServerSize(): void {
  if (!term) {
    return
  }
  if (writeGranted.value) {
    if (term.options.fontSize !== BASE_FONT_SIZE) {
      term.options.fontSize = BASE_FONT_SIZE
    }
    fit?.fit()
    return
  }
  const cols = serverCols.value
  const rows = serverRows.value
  if (!cols || !rows) {
    return
  }
  const width = hostEl.value?.clientWidth ?? 0
  if (width > 0) {
    // 0.62 ≈ 常见等宽字体的字宽/字号比；夹在 8..13px，放不下由横向滚动兜底。
    const size = Math.max(8, Math.min(BASE_FONT_SIZE, Math.floor(width / (cols * 0.62))))
    if (term.options.fontSize !== size) {
      term.options.fontSize = size
    }
  }
  if (term.cols !== cols || term.rows !== rows) {
    term.resize(cols, rows)
  }
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
    pasteText(text)
  } catch {
    emit('error', '读取剪贴板失败')
  }
}

function pasteText(text: string): void {
  if (!writeGranted.value || !text) {
    return
  }
  const normalized = text.replace(/\r?\n/g, '\r')
  if (term?.modes.bracketedPasteMode && term.options.ignoreBracketedPasteMode !== true) {
    sendInput(`\x1b[200~${normalized}\x1b[201~`)
    return
  }
  sendInput(normalized)
}

// 聊天式输入（tools-j0e）：长文本在 textarea 里编辑确认后一次性写入 pty。
// 文本经 bracketed paste 包裹（TUI 视作一次粘贴而非逐行提交），勾选"发送"时
// 再单独补一个回车提交；不勾则只置入 TUI 输入区供继续编辑。
function sendChat(): void {
  const text = chatText.value
  if (!writeGranted.value || !text.trim()) {
    return
  }
  const normalized = text.replace(/\r?\n/g, '\r')
  if (term?.modes.bracketedPasteMode && term.options.ignoreBracketedPasteMode !== true) {
    sendInput(`\x1b[200~${normalized}\x1b[201~`)
  } else {
    sendInput(normalized)
  }
  if (chatSubmit.value) {
    sendInput('\r')
  }
  chatText.value = ''
}

function onChatKeydown(ev: KeyboardEvent): void {
  if ((ev.ctrlKey || ev.metaKey) && ev.key === 'Enter') {
    ev.preventDefault()
    sendChat()
  }
}

function toggleChat(): void {
  chatCollapsed.value = !chatCollapsed.value
  localStorage.setItem('attach-chat-collapsed', chatCollapsed.value ? '1' : '0')
}

function isCtrlKey(ev: KeyboardEvent, key: string): boolean {
  return (ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === key
}

function isEscapeKey(ev: KeyboardEvent): boolean {
  return ev.key === 'Escape' || ev.key === 'Esc' || ev.code === 'Escape' || ev.keyCode === 27
}

function isTerminalActive(target: EventTarget | null): boolean {
  const host = hostEl.value
  if (!host) {
    return false
  }
  if (target instanceof Node && host.contains(target)) {
    return true
  }
  const active = document.activeElement
  return (active instanceof Node && host.contains(active)) || terminalActive.value
}

function consumeShortcut(ev: KeyboardEvent): boolean {
  if (ev.type !== 'keydown') {
    return false
  }
  if (isCtrlKey(ev, 'c')) {
    ev.preventDefault()
    ev.stopPropagation()
    ev.stopImmediatePropagation()
    if (term?.hasSelection()) {
      void copySelection()
    } else {
      sendInput('\x03')
    }
    return true
  }
  if (isCtrlKey(ev, 'v')) {
    ev.stopPropagation()
    ev.stopImmediatePropagation()
    return true
  }
  if (isEscapeKey(ev)) {
    ev.preventDefault()
    ev.stopPropagation()
    ev.stopImmediatePropagation()
    sendInput('\x1b')
    return true
  }
  return false
}

function onTerminalKey(ev: KeyboardEvent): boolean {
  return !consumeShortcut(ev)
}

function onTerminalHostKey(ev: KeyboardEvent): void {
  consumeShortcut(ev)
}

function markTerminalActive(): void {
  terminalActive.value = true
}

function onDocumentKeydown(ev: KeyboardEvent): void {
  if (isTerminalActive(ev.target)) {
    consumeShortcut(ev)
  }
}

function onDocumentPointerDown(ev: PointerEvent): void {
  const target = ev.target
  if (!(target instanceof Node)) {
    return
  }
  const inRoot = !!rootEl.value?.contains(target)
  terminalActive.value = inRoot
  if (inRoot && !hostEl.value?.contains(target)) {
    term?.focus()
  }
}

function onDocumentPaste(ev: ClipboardEvent): void {
  if (!isTerminalActive(ev.target)) {
    return
  }
  ev.preventDefault()
  ev.stopPropagation()
  ev.stopImmediatePropagation()
  pasteText(ev.clipboardData?.getData('text/plain') ?? '')
}

function sendKeyAction(action: KeyAction): void {
  if (action.paste) {
    void pasteClipboard()
  } else if (action.data) {
    sendInput(action.data)
  }
  keyMenuEl.value?.removeAttribute('open')
  term?.focus()
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
      serverCols.value = frame.cols || 80
      serverRows.value = frame.rows || 24
      setWriteGranted(frame.write)
      applyServerSize()
      return
    }
    if (frame.t === 'r') {
      // pty 尺寸广播（tools-3xy）：所有端跟随权威尺寸，写者自己 resize 的回声幂等。
      serverCols.value = frame.cols
      serverRows.value = frame.rows
      applyServerSize()
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
    // 只有写者的视口变化才允许改本地布局并传导给 pty；只读端跟随服务端尺寸。
    applyServerSize()
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
  document.addEventListener('keydown', onDocumentKeydown, true)
  document.addEventListener('pointerdown', onDocumentPointerDown, true)
  document.addEventListener('paste', onDocumentPaste, true)

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
  document.removeEventListener('keydown', onDocumentKeydown, true)
  document.removeEventListener('pointerdown', onDocumentPointerDown, true)
  document.removeEventListener('paste', onDocumentPaste, true)
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
  <section ref="rootEl" class="attach-terminal">
    <header class="terminal-bar">
      <span class="status-dot" :class="connectionState" aria-hidden="true" />
      <span class="status-text mono">{{ statusText }}</span>
      <span v-if="readOnlyBanner" class="readonly-banner mono">
        {{ readOnlyBanner }}
      </span>
      <div class="terminal-tools">
        <details ref="keyMenuEl" class="key-menu">
          <summary class="terminal-btn key-menu-trigger mono">发送键</summary>
          <div class="key-menu-list" role="menu">
            <button
              v-for="action in keyActions"
              :key="action.label"
              class="key-menu-item mono"
              type="button"
              role="menuitem"
              :disabled="!writeGranted"
              @click="sendKeyAction(action)"
            >
              {{ action.label }}
            </button>
          </div>
        </details>
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
      </div>
    </header>
    <div
      ref="hostEl"
      class="terminal-host"
      @focusin="markTerminalActive"
      @keydown.capture="onTerminalHostKey"
      @pointerdown="markTerminalActive"
    />
    <!-- 聊天式输入区（tools-j0e）：长文本编辑确认后一次性写入，终端直输仍可用 -->
    <div v-if="!exited" class="chat-box">
      <button class="chat-toggle mono" type="button" @click="toggleChat">
        {{ chatCollapsed ? '▸ 输入框' : '▾ 输入框' }}
      </button>
      <template v-if="!chatCollapsed">
        <textarea
          v-model="chatText"
          class="chat-input mono"
          rows="3"
          :disabled="!writeGranted"
          :placeholder="writeGranted ? '长文本在这里编辑，Ctrl+Enter 发送；终端里也可直接敲键盘' : '只读跟随中，无法输入'"
          @keydown="onChatKeydown"
        ></textarea>
        <div class="chat-actions">
          <label class="chat-submit-toggle mono">
            <input v-model="chatSubmit" type="checkbox" />
            发送后回车提交
          </label>
          <button
            class="terminal-btn mono"
            type="button"
            :disabled="!writeGranted || !chatText.trim()"
            @click="sendChat"
          >
            发送
          </button>
        </div>
      </template>
    </div>
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

.terminal-tools {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: none;
  margin-left: auto;
}

.terminal-btn {
  flex: none;
  padding: 4px 8px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--term-bg);
  color: var(--phosphor);
  font-size: 12px;
}

.terminal-btn:hover {
  border-color: var(--phosphor);
}

.key-menu {
  position: relative;
  flex: none;
}

.key-menu-trigger {
  display: inline-flex;
  align-items: center;
  cursor: pointer;
  list-style: none;
}

.key-menu-trigger::-webkit-details-marker {
  display: none;
}

.key-menu-trigger::after {
  content: '';
  width: 0;
  height: 0;
  margin-left: 6px;
  border-left: 4px solid transparent;
  border-right: 4px solid transparent;
  border-top: 5px solid currentColor;
}

.key-menu[open] .key-menu-trigger {
  border-color: var(--phosphor);
}

.key-menu-list {
  position: absolute;
  top: calc(100% + 6px);
  right: 0;
  z-index: 5;
  display: grid;
  grid-template-columns: repeat(3, minmax(58px, 1fr));
  gap: 4px;
  min-width: 210px;
  padding: 8px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  box-shadow: 0 12px 28px rgba(0, 0, 0, 0.28);
}

.key-menu-item {
  min-height: 26px;
  padding: 4px 8px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--term-bg);
  color: var(--paper);
  font-size: 11px;
}

.key-menu-item:hover:not(:disabled),
.key-menu-item:focus-visible {
  color: var(--phosphor);
  border-color: var(--phosphor);
  outline: none;
}

.key-menu-item:disabled {
  color: var(--queue);
  cursor: default;
  opacity: 0.45;
}

.terminal-host {
  flex: 1 1 auto;
  min-height: 0;
  padding: 8px;
  /* 只读端跟随服务端尺寸时可能超出容器宽度，横向滚动兜底（tools-3xy） */
  overflow: auto;
  background: var(--term-bg);
}

/* 聊天式输入区（tools-j0e）：安静的第二输入面，不与终端争夺视觉 */
.chat-box {
  flex: none;
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 6px 10px 8px;
  border-top: 1px solid var(--line);
  background: var(--panel);
}
.chat-toggle {
  align-self: flex-start;
  padding: 2px 6px;
  border: none;
  background: transparent;
  color: var(--queue);
  font-size: 11px;
  cursor: pointer;
}
.chat-toggle:hover {
  color: var(--phosphor);
}
.chat-input {
  width: 100%;
  resize: vertical;
  min-height: 56px;
  max-height: 200px;
  padding: 7px 9px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--term-bg);
  color: var(--paper);
  font-size: 12px;
  line-height: 1.45;
}
.chat-input:focus {
  border-color: var(--phosphor);
  outline: none;
}
.chat-input:disabled {
  opacity: 0.5;
}
.chat-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
}
.chat-submit-toggle {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--queue);
  font-size: 11px;
  cursor: pointer;
}
.chat-submit-toggle input {
  accent-color: var(--phosphor);
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
