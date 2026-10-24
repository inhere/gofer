import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// 用一个进程内的 BroadcastChannel 假实现：同名频道的实例互相收到消息（不含自己）。
class FakeChannel {
  static all: FakeChannel[] = []
  listeners: Array<(ev: MessageEvent) => void> = []
  constructor(public name: string) { FakeChannel.all.push(this) }
  addEventListener(_t: string, fn: (ev: MessageEvent) => void) { this.listeners.push(fn) }
  removeEventListener(_t: string, fn: (ev: MessageEvent) => void) { this.listeners = this.listeners.filter((f) => f !== fn) }
  postMessage(data: unknown) {
    for (const ch of FakeChannel.all) {
      if (ch !== this && ch.name === this.name) for (const fn of ch.listeners) fn({ data } as MessageEvent)
    }
  }
}

function memStorage() {
  const m = new Map<string, string>()
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) }
}

describe('requestTokenFromPeers', () => {
  beforeEach(() => {
    FakeChannel.all = []
    vi.stubGlobal('BroadcastChannel', FakeChannel)
    vi.stubGlobal('sessionStorage', memStorage())
    vi.resetModules()
  })
  afterEach(() => vi.unstubAllGlobals())

  it('takes the token from another logged-in tab', async () => {
    // 另一个已登录的标签页：一个回应 need-token 的频道
    const peer = new FakeChannel('gofer-auth')
    peer.addEventListener('message', (ev) => {
      if ((ev.data as { type: string }).type === 'need-token') peer.postMessage({ type: 'token', token: 'tok-1' })
    })
    const auth = await import('./auth')
    expect(auth.hasToken()).toBe(false)
    expect(await auth.requestTokenFromPeers(200)).toBe(true)
    expect(auth.getToken()).toBe('tok-1')
  })

  it('gives up when no tab answers', async () => {
    const auth = await import('./auth')
    expect(await auth.requestTokenFromPeers(50)).toBe(false)
    expect(auth.hasToken()).toBe(false)
  })
})

// 回归（2026-10-23）：挂载应用后外壳立刻请求接口；若 token 交接还没完成，这些请求 401 会清掉
// 刚交接来的 token 并跳 /access。main.ts 必须先等交接、再挂载；401 跳转要带回原页面。
describe('boot order', () => {
  const mainSrc = Object.values(
    import.meta.glob('../main.ts', { eager: true, query: '?raw', import: 'default' }),
  )[0] as string
  it('asks peer tabs for the token before mounting the app', () => {
    const ask = mainSrc.indexOf('await requestTokenFromPeers()')
    const mount = mainSrc.indexOf(".mount('#app')")
    expect(ask).toBeGreaterThan(-1)
    expect(mount).toBeGreaterThan(ask)
  })
  it('keeps the page to return to when a 401 sends the tab to /access', () => {
    expect(mainSrc).toContain("query: { redirect: current.fullPath }")
  })
})
