// 纯 Bearer 无状态鉴权。token 存 sessionStorage（关掉所有标签页即需重新登录）。
//
// sessionStorage 是按标签页隔离的：从通知 / 钉钉链接打开的新标签页本来拿不到 token，
// 会被送去 /access。为此新标签页没有 token 时，先经 BroadcastChannel 向同源的其他已登录
// 标签页要一份（requestTokenFromPeers）；只在同源标签页之间传递，不落 localStorage。

const TOKEN_KEY = 'gofer-token'

let unauthorizedHandler: (() => void) | null = null

export function getToken(): string | null {
  // 没有 sessionStorage 的环境（服务端渲染、node 里的组件渲染测试）当作未登录：
  // whoami 之类按身份读取的 store 因此不会在渲染时抛错，而是保持 null（界面不隐藏任何操作）。
  if (typeof sessionStorage === 'undefined') return null
  return sessionStorage.getItem(TOKEN_KEY)
}

// 登录变化（换 token / 清 token）的订阅：whoami 之类按身份缓存的 store 据此重取或清空。
const changeListeners: Array<() => void> = []

export function onAuthChange(fn: () => void): void {
  changeListeners.push(fn)
}

function notifyAuthChange(): void {
  for (const fn of changeListeners) fn()
}

export function setToken(token: string): void {
  sessionStorage.setItem(TOKEN_KEY, token)
  notifyAuthChange()
}

// ---- 跨标签页交接 token（BroadcastChannel，同源） ----

const CHANNEL = 'gofer-auth'
type AuthMsg = { type: 'need-token' } | { type: 'token'; token: string }

let channel: BroadcastChannel | null = null

function authChannel(): BroadcastChannel | null {
  if (channel) return channel
  if (typeof BroadcastChannel === 'undefined') return null
  channel = new BroadcastChannel(CHANNEL)
  // 已登录的标签页回应别的标签页的请求。
  channel.addEventListener('message', (ev: MessageEvent<AuthMsg>) => {
    if (ev.data?.type === 'need-token') {
      const t = getToken()
      if (t) channel?.postMessage({ type: 'token', token: t } satisfies AuthMsg)
    }
  })
  return channel
}

// startTokenSharing 让本标签页开始回应其他标签页的 token 请求（main.ts 启动时调用）。
export function startTokenSharing(): void {
  authChannel()
}

// requestTokenFromPeers 向其他已登录的同源标签页要 token，等 timeoutMs 内的第一个回应；
// 拿到就写入本标签页并返回 true。没有 BroadcastChannel 或无人回应时返回 false。
export function requestTokenFromPeers(timeoutMs = 500): Promise<boolean> {
  const ch = authChannel()
  if (!ch || hasToken()) return Promise.resolve(hasToken())
  return new Promise((resolve) => {
    let done = false
    const finish = (ok: boolean) => {
      if (done) return
      done = true
      ch.removeEventListener('message', onMsg)
      clearTimeout(timer)
      resolve(ok)
    }
    const onMsg = (ev: MessageEvent<AuthMsg>) => {
      if (ev.data?.type === 'token' && typeof ev.data.token === 'string' && ev.data.token) {
        setToken(ev.data.token)
        finish(true)
      }
    }
    ch.addEventListener('message', onMsg)
    const timer = setTimeout(() => finish(false), timeoutMs)
    ch.postMessage({ type: 'need-token' } satisfies AuthMsg)
  })
}

export function clearToken(): void {
  sessionStorage.removeItem(TOKEN_KEY)
  notifyAuthChange()
}

export function hasToken(): boolean {
  const t = getToken()
  return t != null && t.length > 0
}

// 注册 401 处理回调（main.ts 注册：清 token + 跳 /access）
export function setUnauthorizedHandler(fn: () => void): void {
  unauthorizedHandler = fn
}

// client 在收到 401 时调用：清 token 并触发回调
export function triggerUnauthorized(): void {
  clearToken()
  if (unauthorizedHandler) {
    unauthorizedHandler()
  }
}
