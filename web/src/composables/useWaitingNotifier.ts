// 「等我」浏览器通知的挂载点（S9 T6）：App 挂一次，全站只有这一个出口。
//
// 数据来自 composables/useWaiting（WS `waiting` 快照里的整份「等我」列表，onEnter 只在某个
// 主体**新出现**时回调一次）。发不发、谁发、点开去哪，判定全在 utils/waitingNotify；这里只做
// 粘合：读浏览器事实、接选主通道、发通知 / 写标题计数。
//
// 浏览器能力（Notification / document / BroadcastChannel / Web Push 订阅）都从 deps 进来，
// 生产用 browserHost() 与真实通道，测试注入假 host（node 环境没有 window / document）。
import { watch, type WatchStopHandle } from 'vue'
import type { WaitingItem } from '../api/types'
import { useWaiting, type WaitingStore } from './useWaiting'
import { currentPushSubscription } from '../utils/webPush'
import {
  WAITING_NOTIFY_CHANNEL,
  createTabElection,
  notifyMode,
  shouldNotifyOnEnter,
  stripCount,
  titleWithCount,
  waitingNotification,
  type ElectionChannel,
  type NotifyEnv,
  type NotifyMode,
  type TabElection,
} from '../utils/waitingNotify'

// 标题的基准值：首次挂载时读页面当前标题（去掉我们自己加的计数前缀），拿不到就用 gofer。
const FALLBACK_TITLE = 'gofer'

export interface NotifyRouter {
  push(to: string): unknown
}

// NotifyHost 是本组合函数需要的浏览器能力（node 环境与测试用假实现）。
export interface NotifyHost {
  secure(): boolean
  hasNotification(): boolean
  permission(): string
  hidden(): boolean
  title(): string
  setTitle(title: string): void
  show(title: string, opts: { body: string; tag: string; onClick: () => void }): void
  focus(): void
  // 订阅可见性变化（visibilitychange），返回退订函数。
  onVisibility(cb: () => void): () => void
}

// browserHost：真实浏览器。所有全局都现读并带 typeof 守卫（页面在 /access 之外才挂载，
// 但单测与预渲染下没有 window / document）。
export function browserHost(): NotifyHost {
  return {
    secure: () => typeof window !== 'undefined' && window.isSecureContext === true,
    hasNotification: () => typeof Notification !== 'undefined',
    permission: () => (typeof Notification !== 'undefined' ? Notification.permission : 'denied'),
    hidden: () => typeof document !== 'undefined' && document.visibilityState === 'hidden',
    title: () => (typeof document !== 'undefined' ? document.title : ''),
    setTitle: (title) => {
      if (typeof document !== 'undefined') document.title = title
    },
    show: (title, opts) => {
      const notification = new Notification(title, {
        body: opts.body,
        tag: opts.tag,
        icon: '/android-chrome-192x192.png',
        badge: '/favicon-48x48.png',
      })
      notification.onclick = () => {
        // 点通知：聚焦窗口（手机 PWA / 桌面浏览器都可能被压在后面），再跳到那一项。
        try {
          window.focus()
        } catch {
          // 有些环境不允许脚本聚焦窗口，跳转照常。
        }
        opts.onClick()
        notification.close()
      }
    },
    focus: () => {
      try {
        window.focus()
      } catch {
        // 同上。
      }
    },
    onVisibility: (cb) => {
      if (typeof document === 'undefined') return () => {}
      document.addEventListener('visibilitychange', cb)
      return () => document.removeEventListener('visibilitychange', cb)
    },
  }
}

// broadcastChannel：同源标签页通道（不支持时返回 null，等价于只有这一个标签页）。
function broadcastChannel(): ElectionChannel | null {
  if (typeof BroadcastChannel === 'undefined') return null
  const ch = new BroadcastChannel(WAITING_NOTIFY_CHANNEL)
  return {
    postMessage: (msg) => ch.postMessage(msg),
    onMessage: (cb) => {
      const handler = (ev: MessageEvent) => cb(ev.data)
      ch.addEventListener('message', handler)
      return () => ch.removeEventListener('message', handler)
    },
    close: () => ch.close(),
  }
}

function randomID(): string {
  try {
    return crypto.randomUUID()
  } catch {
    return Math.random().toString(36).slice(2)
  }
}

// subscribedToPush：本浏览器有没有 Web Push 订阅（没有 SW / 非安全上下文时恒为 false）。
async function subscribedToPush(): Promise<boolean> {
  try {
    return (await currentPushSubscription()) !== null
  } catch {
    return false
  }
}

// 读订阅状态最多等这么久：`navigator.serviceWorker.ready` 在没注册 SW 的页面上永不 resolve，
// 不能让它把标题计数也拖死。
const SUBSCRIBE_TIMEOUT_MS = 1500

function withTimeout<T>(promise: Promise<T>, ms: number, fallback: T): Promise<T> {
  return new Promise<T>((resolve) => {
    const timer = setTimeout(() => resolve(fallback), ms)
    const settle = (value: T): void => {
      clearTimeout(timer)
      resolve(value)
    }
    promise.then(settle, () => settle(fallback))
  })
}

export interface WaitingNotifierDeps {
  store: WaitingStore
  router?: NotifyRouter
  host?: NotifyHost
  // 已订阅 Web Push？（默认读 pushManager.getSubscription）。
  subscribed?: () => Promise<boolean>
  // 同源标签页通道（默认 BroadcastChannel）。
  openChannel?: () => ElectionChannel | null
  newID?: () => string
}

export interface WaitingNotifier {
  start(): void
  stop(): void
  // refresh 重读订阅状态并重算标题计数（启动时、可见性变化时调）。
  refresh(): Promise<void>
  // mode 是当前的出口：push（服务端推）/ system（本页弹）/ title（标题计数）。
  mode(): NotifyMode
}

export function createWaitingNotifier(deps: WaitingNotifierDeps): WaitingNotifier {
  const host = deps.host ?? browserHost()
  const subscribedOf = deps.subscribed ?? subscribedToPush
  const openChannel = deps.openChannel ?? broadcastChannel
  const id = (deps.newID ?? randomID)()
  let subscribed = false
  // 订阅状态读到之前不加标题计数：订阅了的设备只靠服务端推，不该先闪一下「(N) gofer」。
  let subscribedKnown = false
  let baseTitle = FALLBACK_TITLE
  let running = false
  let election: TabElection | null = null
  let unsubEnter: (() => void) | null = null
  let unsubVisibility: (() => void) | null = null
  let stopWatch: WatchStopHandle | null = null

  function env(): NotifyEnv {
    return {
      pushSubscribed: subscribed,
      secure: host.secure(),
      hasNotification: host.hasNotification(),
      permission: host.permission(),
      hidden: host.hidden(),
    }
  }

  // applyTitle：只有走标题计数这条出口时才加前缀；其他出口恢复原标题。
  function applyTitle(): void {
    const count = subscribedKnown && notifyMode(env()) === 'title' ? deps.store.items.value.length : 0
    const want = titleWithCount(count, baseTitle)
    if (host.title() !== want) host.setTitle(want)
  }

  function notify(item: WaitingItem): void {
    if (!shouldNotifyOnEnter(env())) return
    if (!election?.isSender()) return
    const payload = waitingNotification(item)
    host.show(payload.title, {
      body: payload.body,
      tag: payload.tag,
      onClick: () => {
        host.focus()
        deps.router?.push(payload.url)
      },
    })
  }

  async function refresh(): Promise<void> {
    subscribed = await withTimeout(subscribedOf(), SUBSCRIBE_TIMEOUT_MS, false)
    subscribedKnown = true
    applyTitle()
  }

  return {
    start() {
      if (running) return
      running = true
      baseTitle = stripCount(host.title() || '') || FALLBACK_TITLE
      election = createTabElection({ id, channel: openChannel(), hidden: () => host.hidden() })
      election.start()
      unsubEnter = deps.store.onEnter(notify)
      stopWatch = watch(deps.store.items, applyTitle)
      unsubVisibility = host.onVisibility(() => {
        election?.announce()
        applyTitle()
        void refresh()
      })
      applyTitle()
      void refresh()
    },
    stop() {
      if (!running) return
      running = false
      unsubEnter?.()
      unsubEnter = null
      unsubVisibility?.()
      unsubVisibility = null
      stopWatch?.()
      stopWatch = null
      election?.stop()
      election = null
      host.setTitle(baseTitle)
    },
    refresh,
    mode: () => notifyMode(env()),
  }
}

let shared: WaitingNotifier | null = null

// startWaitingNotifier：App 在进入导航壳时调一次（与其它 App 级订阅同处起停：/access 下没有
// 凭据，不订阅「等我」主题）；重复调用只是同一个实例的 start（幂等）。
export function startWaitingNotifier(router?: NotifyRouter): WaitingNotifier {
  if (!shared) shared = createWaitingNotifier({ store: useWaiting(), router })
  shared.start()
  return shared
}

// stopWaitingNotifier：离开导航壳（/access）或整页卸载时调；下次 start 重新建。
export function stopWaitingNotifier(): void {
  shared?.stop()
  shared = null
}
