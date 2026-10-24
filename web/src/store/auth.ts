// 纯 Bearer 无状态鉴权。token 可以放在两处（gofer-x5k4）：
//
//  - sessionStorage（默认）：关掉所有标签页即需重新登录；
//  - localStorage（接入页「在此浏览器记住登录」，默认勾选）：按 origin 持久，重开浏览器、
//    从桌面通知 / 钉钉链接打开的新标签页都不用再登录。
//
// 读取顺序 sessionStorage → localStorage；写入只落选中的那一处，并把另一处的旧值清掉
// （两处各存一份会让「退出」「切换记住方式」「另一个标签页沿用旧 token」变成同步问题）。
//
// sessionStorage 是按标签页隔离的：从通知链接打开的新标签页本来拿不到 token，会被送去
// /access。为此新标签页没有 token 时，先经 BroadcastChannel 向同源的其他已登录标签页要一份
// （requestTokenFromPeers）；交接只落在接收标签页的 sessionStorage，不改动对端的记忆设置。
//
// 退出登录与 401 一律清两处（clearToken）：勾了「记住登录」的用户点退出，就是「退出并
// 忘记此浏览器」（设置 › 关于）。

const TOKEN_KEY = 'gofer-token'

// 存储可能在服务端渲染、隐私模式或禁用存储的环境里不存在 / 抛错，统一按「取不到」处理。
function sessionStore(): Storage | null {
  try {
    return typeof sessionStorage === 'undefined' ? null : sessionStorage
  } catch {
    return null
  }
}

function localStore(): Storage | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}

function readToken(store: Storage | null): string | null {
  if (!store) return null
  try {
    const v = store.getItem(TOKEN_KEY)
    return v && v.length > 0 ? v : null
  } catch {
    return null
  }
}

function writeToken(store: Storage | null, token: string): void {
  if (!store) return
  try {
    store.setItem(TOKEN_KEY, token)
  } catch {
    // 隐私模式 / 配额：这一处存不下就只保留另一处（setToken 会先写目标再清旧的）。
  }
}

function removeToken(store: Storage | null): void {
  if (!store) return
  try {
    store.removeItem(TOKEN_KEY)
  } catch {
    // 同上：清不掉也不影响读取（读取按 sessionStorage → localStorage 顺序）。
  }
}

let unauthorizedHandler: (() => void) | null = null

export function getToken(): string | null {
  // 没有 sessionStorage 的环境（服务端渲染、node 里的组件渲染测试）当作未登录：
  // whoami 之类按身份读取的 store 因此不会在渲染时抛错，而是保持 null（界面不隐藏任何操作）。
  return readToken(sessionStore()) ?? readToken(localStore())
}

// isRemembered 表示这个浏览器（origin）记着登录（token 在 localStorage）。接入页与
// 设置 › 关于据此说明当前状态，退出时也是要清掉的那一份。
export function isRemembered(): boolean {
  return readToken(localStore()) != null
}

// 登录变化（换 token / 清 token）的订阅：whoami 之类按身份缓存的 store 据此重取或清空。
const changeListeners: Array<() => void> = []

export function onAuthChange(fn: () => void): void {
  changeListeners.push(fn)
}

function notifyAuthChange(): void {
  for (const fn of changeListeners) fn()
}

// setToken 保存本次登录的 token。remember=true 存 localStorage（按 origin 持久），
// 否则存当前标签页的 sessionStorage（默认）。
export function setToken(token: string, opts: { remember?: boolean } = {}): void {
  if (opts.remember === true) {
    writeToken(localStore(), token)
    removeToken(sessionStore())
  } else {
    writeToken(sessionStore(), token)
    removeToken(localStore())
  }
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
  // 已登录的标签页回应别的标签页的请求（token 在任一存储里都算已登录）。
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
        // 交接来的 token 只留在本标签页：对方可能没勾「记住登录」，本标签页也不该替它决定。
        setToken(ev.data.token)
        finish(true)
      }
    }
    ch.addEventListener('message', onMsg)
    const timer = setTimeout(() => finish(false), timeoutMs)
    ch.postMessage({ type: 'need-token' } satisfies AuthMsg)
  })
}

// clearToken 清掉两处存储（退出登录 / 401 / 登录失败兜底）。
export function clearToken(): void {
  removeToken(sessionStore())
  removeToken(localStore())
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
