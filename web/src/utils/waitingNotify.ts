// 「等我」的浏览器通知：发不发、由哪个标签页发、点开去哪（S9 T6）。
//
// 全站只有一个出口：
//  - 本浏览器已订阅 Web Push：服务端进门时推一次（T2），前端**不发**，免得两遍；
//  - 没订阅、安全上下文、通知权限已授予：只在页面隐藏（`document.visibilityState === 'hidden'`）
//    时发，且**只由一个标签页发**（选主算法见下），tag 用主体 key（浏览器自己合并同名通知）；
//  - 其余（局域网 http 的 `isSecureContext === false`、浏览器没有 Notification、权限没给）：
//    降级成页面标题计数 `(N) gofer`，N 是「等我」项数，回到 0 恢复原标题。
//
// 选主（tab election）：BroadcastChannel 心跳租约 + 最小 id。
//  - 每个标签页随机取一个 id，启动时广播 `hi`，之后每 `PING_MS` 广播一次 `ping`（带自己的
//    hidden 状态），关闭 / 停止时广播 `bye`；
//  - 对端超过 `LEASE_MS`（= 3 次心跳）没有再出声就认为它没了：崩溃的标签页不必等它自己注销，
//    下一个标签页据此接管；
//  - 发送者 = 存活对端 ∪ 自己 里 **hidden 且 id 最小** 的那个。可见的标签页不参与（它不该发通知，
//    但也不该挡住隐藏标签页发）；没人 hidden 时不发。
//
// 纯逻辑在这里，浏览器能力（Notification / document / BroadcastChannel）由 composables/
// useWaitingNotifier 注入，node 环境可直接单测。
import type { WaitingItem } from '../api/types'
import { t } from '../i18n'

// BroadcastChannel 名（与 store/auth.ts 的 gofer-auth 一样按功能分开）。
export const WAITING_NOTIFY_CHANNEL = 'gofer-waiting-notify'
// 心跳间隔；租约 = 3 次心跳没声就算对端没了。
export const PING_MS = 4_000
export const LEASE_MS = PING_MS * 3

// NotifyEnv 是判定要走哪条出口所需的浏览器事实（每次发之前现读，便于权限 / 订阅变化）。
export interface NotifyEnv {
  // 本浏览器已有 Web Push 订阅（utils/webPush.currentPushSubscription 非空）。
  pushSubscribed: boolean
  // window.isSecureContext：局域网 http 下没有 Service Worker 与 Notification。
  secure: boolean
  hasNotification: boolean
  // Notification.permission：没给（default）也不弹，先降级到标题计数，不主动弹权限框。
  permission: string
  // document.visibilityState === 'hidden'
  hidden: boolean
}

export type NotifyMode = 'push' | 'system' | 'title'

// notifyMode：谁负责通知这件事。
export function notifyMode(env: NotifyEnv): NotifyMode {
  if (env.pushSubscribed) return 'push'
  if (env.secure && env.hasNotification && env.permission === 'granted') return 'system'
  return 'title'
}

// shouldNotifyOnEnter：某个「等我」主体新出现时，本标签页该不该弹系统通知。
// 页面可见不弹（人就看着，页面里有弹卡与徽标），订阅了 Web Push 也不弹（服务端推）。
export function shouldNotifyOnEnter(env: NotifyEnv): boolean {
  return notifyMode(env) === 'system' && env.hidden
}

// TabState 是一个标签页的存活状态：hidden 参与选主，at 是最后一次听到它的时刻。
export interface TabState {
  id: string
  hidden: boolean
}

export interface SeenTab {
  hidden: boolean
  at: number
}

// senderOf：谁发这一条——hidden 的标签页里 id 最小的那个（空串 = 没人 hidden，不发）。
export function senderOf(tabs: TabState[]): string {
  let best = ''
  for (const tab of tabs) {
    if (!tab.hidden) continue
    if (best === '' || tab.id < best) best = tab.id
  }
  return best
}

// liveTabs：去掉租约过期的对端（关掉的、崩掉的），剩下的才参与选主。
export function liveTabs(seen: Map<string, SeenTab>, now: number, leaseMs: number = LEASE_MS): TabState[] {
  const out: TabState[] = []
  for (const [id, tab] of seen) {
    if (now - tab.at < leaseMs) out.push({ id, hidden: tab.hidden })
  }
  return out
}

// ---- 通知内容 ------------------------------------------------------------------------

export interface WaitingNotification {
  // 只带主体名与类别（「周报 在等你回答」），与服务端 Web Push 的标题同口径。
  title: string
  // 恒为空：回合正文不出门（服务端 payload 的 body 同样为空）。
  body: string
  // 浏览器按 tag 合并同一条主体的重复通知。
  tag: string
  // 点开通知去的站内路径。
  url: string
}

export function waitingNotification(item: WaitingItem): WaitingNotification {
  return { title: waitingTitle(item), body: '', tag: item.key, url: waitingURL(item) }
}

// waitingTitle：主体名 + 类别短语。类别是「等我」的七类加投递确认，文案走 i18n。
export function waitingTitle(item: WaitingItem): string {
  const name = (item.title ?? '').trim() || item.key
  switch (item.category) {
    case 'approval':
      return t('util.waitNotify.approval', { name })
    case 'answer':
      return t('util.waitNotify.answer', { name })
    case 'review':
      return t('util.waitNotify.review', { name })
    case 'awaiting_merge':
      return t('util.waitNotify.awaitingMerge', { name })
    case 'plan_stuck':
      return t('util.waitNotify.planStuck', { name })
    case 'failed':
      return t('util.waitNotify.failed', { name })
    case 'delivery':
      return t('util.waitNotify.delivery', { name })
    default:
      return t('util.waitNotify.attention', { name })
  }
}

// waitingURL：会话进工作台的 `r:<sid>` 线程（与服务端 Web Push 的深链一致），job 进详情页
// （待批 / 待验收要看的材料都在那儿），其余主体去收件箱。
export function waitingURL(item: WaitingItem): string {
  const sep = item.key.indexOf(':')
  const kind = sep > 0 ? item.key.slice(0, sep) : ''
  const id = sep > 0 ? item.key.slice(sep + 1) : ''
  if (kind === 'session' && id) return `/workbench?thread=${encodeURIComponent(`r:${id}`)}`
  if (kind === 'job' && id) return `/jobs/${encodeURIComponent(id)}`
  return '/inbox'
}

// titleWithCount：标题计数降级时的写法；0 项恢复原标题。
export function titleWithCount(count: number, base: string): string {
  return count > 0 ? `(${count}) ${base}` : base
}

// COUNT_PREFIX 是我们自己加的计数前缀：基准标题要把它剥掉，免得叠成「(2) (1) gofer」。
const COUNT_PREFIX = /^\(\d+\)\s*/

export function stripCount(title: string): string {
  return title.replace(COUNT_PREFIX, '')
}

// ---- 跨标签页选主 ----------------------------------------------------------------------

export interface ElectionChannel {
  postMessage(msg: unknown): void
  onMessage(cb: (msg: unknown) => void): () => void
  close(): void
}

type ElectionMsgType = 'hi' | 'ping' | 'bye'

interface ElectionMsg {
  ch: string
  t: ElectionMsgType
  id: string
  hidden: boolean
}

export interface TabElectionOpts {
  // 本标签页的 id（随机生成；最小者优先发）。
  id: string
  // 同源标签页通道；没有 BroadcastChannel 时给 null（此时自己是唯一的标签页）。
  channel: ElectionChannel | null
  // 本标签页是否隐藏（每次广播 / 选主时现读）。
  hidden: () => boolean
  now?: () => number
  pingMs?: number
  leaseMs?: number
  setTimer?: (fn: () => void, ms: number) => number
  clearTimer?: (h: number) => void
}

export interface TabElection {
  start(): void
  // 停止：广播 bye 让其他标签页立刻接管，然后关闭通道。
  stop(): void
  // 可见性变化时调用：把自己的 hidden 状态广播出去（选主下一条按新状态算）。
  announce(): void
  // 现在该由哪个标签页发（空串 = 没有隐藏的标签页，不发）。
  sender(): string
  isSender(): boolean
}

// createTabElection：心跳租约 + 最小 id 的选主，见文件头。
export function createTabElection(opts: TabElectionOpts): TabElection {
  const now = opts.now ?? (() => Date.now())
  const pingMs = opts.pingMs ?? PING_MS
  const leaseMs = opts.leaseMs ?? LEASE_MS
  const setTimer = opts.setTimer ?? ((fn, ms) => setTimeout(fn, ms) as unknown as number)
  const clearTimer = opts.clearTimer ?? ((h) => clearTimeout(h as unknown as ReturnType<typeof setTimeout>))
  const seen = new Map<string, SeenTab>()
  let timer: number | null = null
  let unsub: (() => void) | null = null
  let running = false

  function post(type: ElectionMsgType): void {
    opts.channel?.postMessage({ ch: WAITING_NOTIFY_CHANNEL, t: type, id: opts.id, hidden: opts.hidden() } satisfies ElectionMsg)
  }

  function onMessage(raw: unknown): void {
    const msg = raw as Partial<ElectionMsg> | null
    if (!msg || msg.ch !== WAITING_NOTIFY_CHANNEL || typeof msg.id !== 'string' || msg.id === '' || msg.id === opts.id) return
    if (msg.t === 'bye') {
      seen.delete(msg.id)
      return
    }
    if (msg.t !== 'hi' && msg.t !== 'ping') return
    const known = seen.has(msg.id)
    seen.set(msg.id, { hidden: !!msg.hidden, at: now() })
    // 新来的标签页问好时回一声，让它立刻知道现有标签页；ping 不会引来回复，不会来回弹。
    if (msg.t === 'hi' && !known) post('ping')
  }

  function tick(): void {
    post('ping')
    if (running) timer = setTimer(tick, pingMs)
  }

  function sender(): string {
    const tabs = liveTabs(seen, now(), leaseMs)
    if (opts.hidden()) tabs.push({ id: opts.id, hidden: true })
    return senderOf(tabs)
  }

  return {
    start() {
      if (running) return
      running = true
      if (opts.channel) unsub = opts.channel.onMessage(onMessage)
      post('hi')
      timer = setTimer(tick, pingMs)
    },
    stop() {
      if (!running) return
      running = false
      post('bye')
      if (timer !== null) clearTimer(timer)
      timer = null
      unsub?.()
      unsub = null
      opts.channel?.close()
    },
    announce: () => post('hi'),
    sender,
    isSender: () => sender() === opts.id,
  }
}
