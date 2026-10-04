// 全局推送连接（Q3）：整个页面只有一条 /v1/ws。
//
//  - 换 ticket（POST /v1/ws-ticket）→ 建连 → 订阅 → 20s 一次应用层心跳；
//  - 断线指数退避重连（1s 起，上限 30s，带抖动）；401 交给 triggerUnauthorized，不再重连；
//  - 断开超过 15s 视为「兜底中」（fallback=true）：useLiveTopic 据此切回低频轮询；
//    重连成功后 fallback 复位，并向每个订阅者发 reconnect，页面拉一次快照对齐；
//  - 页面隐藏超过 5 分钟主动断开，回到前台再重连。
//
// 服务端帧：hello / snap / evt / inval / resync / pong / error（见设计 Q2）。
import { ref } from 'vue'
import { getToken, triggerUnauthorized } from '../store/auth'
import { noteServerVersion } from '../store/staleBuild'

export type LiveStatus = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'disconnected' | 'paused'

export type LiveMessage =
  | { type: 'snap'; data: unknown }
  | { type: 'evt'; data: Record<string, unknown> }
  | { type: 'inval'; data: Record<string, unknown> }
  | { type: 'resync' }
  | { type: 'reconnect' }

export type LiveHandler = (msg: LiveMessage) => void

// 页面级响应式状态：顶栏状态点读它。
export const liveStatus = ref<LiveStatus>('idle')
// 断开超过 FALLBACK_AFTER_MS：页面各自的兜底轮询据此启动。
export const liveFallback = ref(false)

export const FALLBACK_AFTER_MS = 15_000
export const HIDDEN_DISCONNECT_MS = 5 * 60_000
export const PING_EVERY_MS = 20_000
export const SILENT_LIMIT_MS = 70_000
const BACKOFF_BASE_MS = 1000
const BACKOFF_MAX_MS = 30_000

export interface SocketLike {
  onopen: (() => void) | null
  onmessage: ((ev: { data: unknown }) => void) | null
  onclose: ((ev: { code?: number }) => void) | null
  onerror: (() => void) | null
  send(data: string): void
  close(): void
}

export interface LiveDeps {
  fetchTicket(): Promise<string>
  makeSocket(ticket: string): SocketLike
  isHidden(): boolean
  onVisibility(cb: () => void): () => void
  random(): number
  // 401 之类不可恢复的鉴权失败。
  unauthorized(): void
}

interface TopicEntry {
  handlers: Set<LiveHandler>
  // job:<id> 断线补发游标
  lastSeq?: number
  // 快照型主题（stats / pending）的最近一份：后加入的订阅者立即拿到，不必再等下一次推送。
  lastSnap?: unknown
  hasSnap?: boolean
}

export interface LiveClient {
  subscribe(topic: string, handler: LiveHandler): () => void
  noteSeq(topic: string, seq: number): void
  readonly status: () => LiveStatus
  close(): void
}

export class TicketError extends Error {
  status: number
  constructor(status: number) {
    super(`ws ticket failed: HTTP ${status}`)
    this.status = status
  }
}

export function createLiveClient(deps: LiveDeps): LiveClient {
  const topics = new Map<string, TopicEntry>()
  let ws: SocketLike | null = null
  let started = false
  let attempt = 0
  let everConnected = false
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let fallbackTimer: ReturnType<typeof setTimeout> | null = null
  let hiddenTimer: ReturnType<typeof setTimeout> | null = null
  let pingTimer: ReturnType<typeof setInterval> | null = null
  let lastRx = 0
  let stopped = false
  let unsubVis: (() => void) | null = null

  function setStatus(s: LiveStatus): void {
    liveStatus.value = s
  }

  function clearTimers(): void {
    if (reconnectTimer) clearTimeout(reconnectTimer)
    if (pingTimer) clearInterval(pingTimer)
    reconnectTimer = null
    pingTimer = null
  }

  function armFallback(): void {
    if (fallbackTimer || liveFallback.value) return
    fallbackTimer = setTimeout(() => {
      fallbackTimer = null
      liveFallback.value = true
      setStatus('disconnected')
    }, FALLBACK_AFTER_MS)
  }

  function clearFallback(): void {
    if (fallbackTimer) clearTimeout(fallbackTimer)
    fallbackTimer = null
    liveFallback.value = false
  }

  function backoffMs(): number {
    const base = Math.min(BACKOFF_MAX_MS, BACKOFF_BASE_MS * 2 ** attempt)
    // ±20% 抖动，避免服务端重启后所有标签页同一时刻回连。
    return Math.round(base * (0.8 + deps.random() * 0.4))
  }

  function scheduleReconnect(): void {
    if (stopped || !started || reconnectTimer) return
    if (topics.size === 0) return
    const delay = backoffMs()
    attempt++
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      void connect()
    }, delay)
  }

  function subFrame(): string {
    const topicList = [...topics.keys()]
    const since: Record<string, number> = {}
    for (const t of topicList) {
      const seq = topics.get(t)?.lastSeq
      if (t.startsWith('job:') && seq !== undefined) since[t] = seq
    }
    return JSON.stringify({ t: 'sub', topics: topicList, since_seq: since })
  }

  function emit(topic: string, msg: LiveMessage): void {
    const entry = topics.get(topic)
    if (!entry) return
    for (const h of [...entry.handlers]) {
      try {
        h(msg)
      } catch {
        // 单个订阅者出错不影响其它。
      }
    }
  }

  function emitAll(msg: LiveMessage): void {
    for (const t of topics.keys()) emit(t, msg)
  }

  function onFrame(raw: unknown): void {
    lastRx = Date.now()
    if (typeof raw !== 'string') return
    let f: { t?: string; topic?: string; data?: unknown; version?: string }
    try {
      f = JSON.parse(raw)
    } catch {
      return
    }
    switch (f.t) {
      case 'hello': {
        const reconnected = everConnected
        everConnected = true
        attempt = 0
        clearFallback()
        setStatus('connected')
        noteServerVersion(f.version)
        ws?.send(subFrame())
        if (reconnected) emitAll({ type: 'reconnect' })
        return
      }
      case 'snap':
        if (f.topic) {
          const e = topics.get(f.topic)
          if (e) {
            e.lastSnap = f.data
            e.hasSnap = true
          }
          emit(f.topic, { type: 'snap', data: f.data })
        }
        return
      case 'evt': {
        const d = (f.data ?? {}) as Record<string, unknown>
        if (f.topic) {
          if (typeof d.seq === 'number') noteSeq(f.topic, d.seq)
          emit(f.topic, { type: 'evt', data: d })
        }
        return
      }
      case 'inval':
        if (f.topic) emit(f.topic, { type: 'inval', data: (f.data ?? {}) as Record<string, unknown> })
        return
      case 'resync':
        // 服务端丢过增量：重订一遍（服务端会重发快照）并让页面全量拉取。
        ws?.send(subFrame())
        emitAll({ type: 'resync' })
        return
      default:
    }
  }

  async function connect(): Promise<void> {
    if (stopped || !started || ws || topics.size === 0) return
    setStatus(liveFallback.value ? 'disconnected' : everConnected ? 'reconnecting' : 'connecting')
    let ticket: string
    try {
      ticket = await deps.fetchTicket()
    } catch (e) {
      if (e instanceof TicketError && e.status === 401) {
        deps.unauthorized()
        return
      }
      armFallback()
      setStatus(liveFallback.value ? 'disconnected' : 'reconnecting')
      scheduleReconnect()
      return
    }
    if (stopped || !started) return
    let sock: SocketLike
    try {
      sock = deps.makeSocket(ticket)
    } catch {
      armFallback()
      scheduleReconnect()
      return
    }
    ws = sock
    lastRx = Date.now()
    sock.onmessage = (ev) => onFrame(ev.data)
    sock.onerror = () => {
      // close 事件随后到达，统一在 onclose 处理。
    }
    sock.onclose = () => {
      if (ws !== sock) return
      ws = null
      if (pingTimer) clearInterval(pingTimer)
      pingTimer = null
      if (stopped || !started) return
      if (liveStatus.value === 'paused') return
      armFallback()
      setStatus(liveFallback.value ? 'disconnected' : 'reconnecting')
      scheduleReconnect()
    }
    pingTimer = setInterval(() => {
      if (!ws) return
      if (Date.now() - lastRx > SILENT_LIMIT_MS) {
        // 半开连接：很久没有任何帧（含 pong），主动断开走重连。
        try {
          ws.close()
        } catch {
          // ignore
        }
        return
      }
      try {
        ws.send('{"t":"ping"}')
      } catch {
        // close 事件会处理
      }
    }, PING_EVERY_MS)
  }

  function noteSeq(topic: string, seq: number): void {
    const e = topics.get(topic)
    if (e && (e.lastSeq === undefined || seq > e.lastSeq)) e.lastSeq = seq
  }

  function disconnectForHidden(): void {
    hiddenTimer = null
    clearTimers()
    clearFallback()
    const sock = ws
    ws = null
    setStatus('paused')
    try {
      sock?.close()
    } catch {
      // ignore
    }
  }

  function onVisibility(): void {
    if (deps.isHidden()) {
      if (!hiddenTimer && started) hiddenTimer = setTimeout(disconnectForHidden, HIDDEN_DISCONNECT_MS)
      return
    }
    if (hiddenTimer) {
      clearTimeout(hiddenTimer)
      hiddenTimer = null
    }
    if (liveStatus.value === 'paused') {
      // 回到前台：重连；hello 之后订阅者会收到 reconnect 并拉快照。
      everConnected = true
      attempt = 0
      void connect()
    }
  }

  function start(): void {
    if (started) return
    started = true
    stopped = false
    unsubVis = deps.onVisibility(onVisibility)
    void connect()
  }

  function stopAll(): void {
    started = false
    stopped = true
    clearTimers()
    clearFallback()
    if (hiddenTimer) clearTimeout(hiddenTimer)
    hiddenTimer = null
    unsubVis?.()
    unsubVis = null
    const sock = ws
    ws = null
    try {
      sock?.close()
    } catch {
      // ignore
    }
    setStatus('idle')
  }

  return {
    subscribe(topic, handler) {
      let entry = topics.get(topic)
      const fresh = !entry
      if (!entry) {
        entry = { handlers: new Set() }
        topics.set(topic, entry)
      }
      entry.handlers.add(handler)
      if (!fresh && entry.hasSnap) {
        const snap = entry.lastSnap
        queueMicrotask(() => {
          if (entry!.handlers.has(handler)) handler({ type: 'snap', data: snap })
        })
      }
      if (!started) {
        start()
      } else if (fresh && ws && liveStatus.value === 'connected') {
        ws.send(JSON.stringify({ t: 'sub', topics: [topic], since_seq: {} }))
      }
      return () => {
        const e = topics.get(topic)
        if (!e) return
        e.handlers.delete(handler)
        if (e.handlers.size === 0) {
          topics.delete(topic)
          if (ws && liveStatus.value === 'connected') {
            try {
              ws.send(JSON.stringify({ t: 'unsub', topics: [topic] }))
            } catch {
              // ignore
            }
          }
          if (topics.size === 0) stopAll()
        }
      }
    },
    noteSeq,
    status: () => liveStatus.value,
    close: stopAll,
  }
}

// ---- 浏览器默认实现 ----

async function fetchTicketHttp(): Promise<string> {
  const token = getToken()
  const res = await fetch('/v1/ws-ticket', {
    method: 'POST',
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
  if (!res.ok) throw new TicketError(res.status)
  const body = (await res.json()) as { ticket: string }
  return body.ticket
}

function browserDeps(): LiveDeps {
  return {
    fetchTicket: fetchTicketHttp,
    makeSocket(ticket) {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      return new WebSocket(`${proto}//${location.host}/v1/ws?ticket=${encodeURIComponent(ticket)}`) as unknown as SocketLike
    },
    isHidden: () => document.hidden,
    onVisibility(cb) {
      document.addEventListener('visibilitychange', cb)
      return () => document.removeEventListener('visibilitychange', cb)
    },
    random: Math.random,
    unauthorized: triggerUnauthorized,
  }
}

let shared: LiveClient | null = null

// 全页面共享的单例（延迟创建，测试可 setLiveClientForTest 替换）。
export function live(): LiveClient {
  if (!shared) shared = createLiveClient(browserDeps())
  return shared
}

export function setLiveClientForTest(c: LiveClient | null): void {
  shared = c
}
