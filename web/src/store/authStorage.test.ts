// token 的两处存储（gofer-x5k4）：默认只在本标签页（sessionStorage），勾了「在此浏览器记住
// 登录」才进 localStorage；读取按 sessionStorage → localStorage；退出 / 401 两处一起清；
// 跨标签页交接只落在接收标签页（不改对方的记忆设置）。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

function memStorage(): Storage {
  const m = new Map<string, string>()
  return {
    get length() {
      return m.size
    },
    clear: () => m.clear(),
    getItem: (k: string) => m.get(k) ?? null,
    key: (i: number) => Array.from(m.keys())[i] ?? null,
    removeItem: (k: string) => void m.delete(k),
    setItem: (k: string, v: string) => void m.set(k, v),
  } as Storage
}

const session = memStorage()
const local = memStorage()

beforeEach(() => {
  session.clear()
  local.clear()
  vi.stubGlobal('sessionStorage', session)
  vi.stubGlobal('localStorage', local)
  vi.resetModules()
})
afterEach(() => vi.unstubAllGlobals())

describe('token storage', () => {
  it('keeps a normal sign-in in this tab only', async () => {
    const auth = await import('./auth')
    auth.setToken('tok-tab')
    expect(auth.getToken()).toBe('tok-tab')
    expect(session.getItem('gofer-token')).toBe('tok-tab')
    expect(local.getItem('gofer-token')).toBeNull()
    expect(auth.isRemembered()).toBe(false)
  })

  it('keeps a remembered sign-in in localStorage', async () => {
    const auth = await import('./auth')
    auth.setToken('tok-remembered', { remember: true })
    expect(local.getItem('gofer-token')).toBe('tok-remembered')
    expect(session.getItem('gofer-token')).toBeNull()
    expect(auth.isRemembered()).toBe(true)
  })

  it('reads sessionStorage first and clears the store it did not write', async () => {
    const auth = await import('./auth')
    auth.setToken('old-remembered', { remember: true })
    // 同一标签页里又用「不记住」登录一次：旧的那份必须被清掉，否则关掉标签页后它又回来了。
    auth.setToken('tab-only')
    expect(session.getItem('gofer-token')).toBe('tab-only')
    expect(local.getItem('gofer-token')).toBeNull()
    expect(auth.getToken()).toBe('tab-only')

    // 两处都有值（例如别处留下的）时按 sessionStorage → localStorage 顺序取。
    local.setItem('gofer-token', 'stale-local')
    expect(auth.getToken()).toBe('tab-only')
  })

  it('falls back to the remembered token when the tab has none', async () => {
    const auth = await import('./auth')
    local.setItem('gofer-token', 'tok-remembered')
    expect(auth.hasToken()).toBe(true)
    expect(auth.getToken()).toBe('tok-remembered')
  })

  it('clears both stores on sign-out', async () => {
    const auth = await import('./auth')
    session.setItem('gofer-token', 'a')
    local.setItem('gofer-token', 'b')
    auth.clearToken()
    expect(session.getItem('gofer-token')).toBeNull()
    expect(local.getItem('gofer-token')).toBeNull()
    expect(auth.hasToken()).toBe(false)
    expect(auth.isRemembered()).toBe(false)
  })

  it('clears the remembered token on a 401 too', async () => {
    const auth = await import('./auth')
    auth.setToken('tok-remembered', { remember: true })
    const onUnauthorized = vi.fn()
    auth.setUnauthorizedHandler(onUnauthorized)
    auth.triggerUnauthorized()
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(local.getItem('gofer-token')).toBeNull()
    expect(auth.hasToken()).toBe(false)
  })

  it('hands a token over into the receiving tab only', async () => {
    class FakeChannel {
      static all: FakeChannel[] = []
      listeners: Array<(ev: MessageEvent) => void> = []
      constructor(public name: string) {
        FakeChannel.all.push(this)
      }
      addEventListener(_t: string, fn: (ev: MessageEvent) => void) {
        this.listeners.push(fn)
      }
      removeEventListener(_t: string, fn: (ev: MessageEvent) => void) {
        this.listeners = this.listeners.filter((f) => f !== fn)
      }
      postMessage(data: unknown) {
        for (const ch of FakeChannel.all) {
          if (ch !== this && ch.name === this.name) for (const fn of ch.listeners) fn({ data } as MessageEvent)
        }
      }
    }
    vi.stubGlobal('BroadcastChannel', FakeChannel)
    FakeChannel.all = []
    // 对端是一个「记住登录」的已登录标签页。
    const peer = new FakeChannel('gofer-auth')
    peer.addEventListener('message', (ev) => {
      if ((ev.data as { type: string }).type === 'need-token') peer.postMessage({ type: 'token', token: 'tok-peer' })
    })

    const auth = await import('./auth')
    expect(await auth.requestTokenFromPeers(200)).toBe(true)
    expect(auth.getToken()).toBe('tok-peer')
    // 交接来的 token 只留在本标签页：不替对端（也不替用户）决定要不要记住。
    expect(session.getItem('gofer-token')).toBe('tok-peer')
    expect(local.getItem('gofer-token')).toBeNull()
  })

  it('treats missing or throwing storage as signed out', async () => {
    vi.unstubAllGlobals()
    vi.resetModules()
    const auth = await import('./auth')
    expect(auth.getToken()).toBeNull()
    // 写入不抛错（隐私模式 / 服务端渲染）。
    expect(() => auth.setToken('x', { remember: true })).not.toThrow()
    expect(() => auth.clearToken()).not.toThrow()
  })
})
