import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  FALLBACK_AFTER_MS,
  HIDDEN_DISCONNECT_MS,
  TicketError,
  createLiveClient,
  liveFallback,
  liveStatus,
  type LiveDeps,
  type LiveMessage,
  type SocketLike,
} from './live'

class FakeSocket implements SocketLike {
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onclose: ((ev: { code?: number }) => void) | null = null
  onerror: (() => void) | null = null
  sent: string[] = []
  closed = false
  send(d: string): void {
    this.sent.push(d)
    // a live server answers the app-level ping
    if (d.includes('"ping"')) queueMicrotask(() => this.recv({ t: 'pong' }))
  }
  close(): void {
    this.closed = true
    this.onclose?.({})
  }
  recv(frame: Record<string, unknown>): void {
    this.onmessage?.({ data: JSON.stringify(frame) })
  }
}

interface Harness {
  deps: LiveDeps
  sockets: FakeSocket[]
  tickets: number
  hidden: { v: boolean }
  fireVisibility(): void
  unauthorized: number
  failTickets: { n: number; status: number }
}

function harness(): Harness {
  const h: Harness = {
    sockets: [],
    tickets: 0,
    hidden: { v: false },
    unauthorized: 0,
    failTickets: { n: 0, status: 500 },
    fireVisibility: () => {},
    deps: undefined as unknown as LiveDeps,
  }
  let visCb: (() => void) | null = null
  h.fireVisibility = () => visCb?.()
  h.deps = {
    async fetchTicket() {
      h.tickets++
      if (h.failTickets.n > 0) {
        h.failTickets.n--
        throw new TicketError(h.failTickets.status)
      }
      return 't' + h.tickets
    },
    makeSocket() {
      const s = new FakeSocket()
      h.sockets.push(s)
      return s
    },
    isHidden: () => h.hidden.v,
    onVisibility(cb) {
      visCb = cb
      return () => {
        visCb = null
      }
    },
    random: () => 0.5,
    unauthorized: () => {
      h.unauthorized++
    },
  }
  return h
}

async function flush(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0)
}

describe('live client', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    liveFallback.value = false
    liveStatus.value = 'idle'
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('connects, subscribes after hello and routes snap / evt / inval by topic', async () => {
    const h = harness()
    const c = createLiveClient(h.deps)
    const got: LiveMessage[] = []
    c.subscribe('stats', (m) => got.push(m))
    c.subscribe('job:j1', (m) => got.push(m))
    await flush()
    expect(h.tickets).toBe(1)
    const sock = h.sockets[0]
    expect(liveStatus.value).toBe('connecting')
    sock.recv({ t: 'hello', server_time: 1, version: 'v1' })
    expect(liveStatus.value).toBe('connected')
    expect(JSON.parse(sock.sent[0])).toMatchObject({ t: 'sub', topics: ['stats', 'job:j1'] })
    sock.recv({ t: 'snap', topic: 'stats', data: { n: 1 } })
    sock.recv({ t: 'evt', topic: 'job:j1', data: { kind: 'event', seq: 7 } })
    sock.recv({ t: 'inval', topic: 'stats', data: {} })
    expect(got.map((m) => m.type)).toEqual(['snap', 'evt', 'inval'])
    c.close()
  })

  it('late subscribers of a snapshot topic get the cached snapshot', async () => {
    const h = harness()
    const c = createLiveClient(h.deps)
    c.subscribe('stats', () => {})
    await flush()
    h.sockets[0].recv({ t: 'hello' })
    h.sockets[0].recv({ t: 'snap', topic: 'stats', data: { n: 5 } })
    const got: LiveMessage[] = []
    c.subscribe('stats', (m) => got.push(m))
    await flush()
    expect(got).toEqual([{ type: 'snap', data: { n: 5 } }])
    c.close()
  })

  it('reconnects with backoff, flips to fallback after 15s and recovers with a reconnect message', async () => {
    const h = harness()
    const c = createLiveClient(h.deps)
    const got: string[] = []
    c.subscribe('jobs', (m) => got.push(m.type))
    await flush()
    h.sockets[0].recv({ t: 'hello' })
    expect(liveFallback.value).toBe(false)

    // server goes away; reconnect attempts fail at the ticket step
    h.failTickets.n = 100
    h.sockets[0].close()
    expect(liveStatus.value).toBe('reconnecting')
    await vi.advanceTimersByTimeAsync(FALLBACK_AFTER_MS - 100)
    expect(liveFallback.value).toBe(false)
    await vi.advanceTimersByTimeAsync(200)
    expect(liveFallback.value).toBe(true)
    expect(liveStatus.value).toBe('disconnected')
    const attemptsWhileDown = h.tickets
    // backoff: 1s,2s,4s,8s ... — far fewer than one attempt per second
    expect(attemptsWhileDown).toBeLessThan(8)

    // server is back
    h.failTickets.n = 0
    await vi.advanceTimersByTimeAsync(40_000)
    const sock = h.sockets[h.sockets.length - 1]
    sock.recv({ t: 'hello' })
    expect(liveFallback.value).toBe(false)
    expect(liveStatus.value).toBe('connected')
    expect(got).toContain('reconnect')
    // the new connection re-subscribes
    const subFrame = sock.sent.map((x) => JSON.parse(x)).find((x) => x.t === 'sub')
    expect(subFrame.topics).toEqual(['jobs'])
    c.close()
  })

  it('a 401 on the ticket stops reconnecting and hands over to the unauthorized handler', async () => {
    const h = harness()
    h.failTickets = { n: 1, status: 401 }
    const c = createLiveClient(h.deps)
    c.subscribe('jobs', () => {})
    await flush()
    expect(h.unauthorized).toBe(1)
    await vi.advanceTimersByTimeAsync(120_000)
    expect(h.tickets).toBe(1)
    c.close()
  })

  it('a resync frame re-subscribes and tells every subscriber', async () => {
    const h = harness()
    const c = createLiveClient(h.deps)
    const got: string[] = []
    c.subscribe('pending', (m) => got.push(m.type))
    await flush()
    const s = h.sockets[0]
    s.recv({ t: 'hello' })
    s.recv({ t: 'resync' })
    expect(got).toEqual(['resync'])
    expect(s.sent.filter((x) => x.includes('"sub"')).length).toBe(2)
    c.close()
  })

  it('disconnects after 5 minutes hidden and reconnects when visible again', async () => {
    const h = harness()
    const c = createLiveClient(h.deps)
    const got: string[] = []
    c.subscribe('jobs', (m) => got.push(m.type))
    await flush()
    h.sockets[0].recv({ t: 'hello' })

    h.hidden.v = true
    h.fireVisibility()
    await vi.advanceTimersByTimeAsync(HIDDEN_DISCONNECT_MS - 1000)
    expect(h.sockets[0].closed).toBe(false)
    await vi.advanceTimersByTimeAsync(2000)
    expect(h.sockets[0].closed).toBe(true)
    expect(liveStatus.value).toBe('paused')
    expect(liveFallback.value).toBe(false)

    h.hidden.v = false
    h.fireVisibility()
    await flush()
    expect(h.sockets.length).toBe(2)
    h.sockets[1].recv({ t: 'hello' })
    expect(got).toContain('reconnect')
    c.close()
  })

  it('does not disconnect when the tab comes back before 5 minutes', async () => {
    const h = harness()
    const c = createLiveClient(h.deps)
    c.subscribe('jobs', () => {})
    await flush()
    h.sockets[0].recv({ t: 'hello' })
    h.hidden.v = true
    h.fireVisibility()
    await vi.advanceTimersByTimeAsync(60_000)
    h.hidden.v = false
    h.fireVisibility()
    await vi.advanceTimersByTimeAsync(HIDDEN_DISCONNECT_MS)
    expect(h.sockets[0].closed).toBe(false)
    c.close()
  })

  it('sends job:<id> resume cursors on reconnect', async () => {
    const h = harness()
    const c = createLiveClient(h.deps)
    c.subscribe('job:j9', () => {})
    await flush()
    h.sockets[0].recv({ t: 'hello' })
    h.sockets[0].recv({ t: 'evt', topic: 'job:j9', data: { kind: 'event', seq: 42 } })
    h.sockets[0].close()
    await vi.advanceTimersByTimeAsync(2000)
    const s = h.sockets[1]
    s.recv({ t: 'hello' })
    expect(JSON.parse(s.sent[0]).since_seq).toEqual({ 'job:j9': 42 })
    c.close()
  })
})
