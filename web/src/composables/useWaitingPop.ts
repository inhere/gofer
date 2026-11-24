// 顶部「等我」弹卡的队列状态（design 2026-11-19 seats T5，整个页面一份）。
//
//  - 只收 composables/useWaiting 里**新出现**（onEnter）且属于会话主体「等回复 / 等批准」的项；
//    首帧不触发 onEnter，所以打开页面时已经在等的事不会一次全弹一遍。
//  - 队列按进入顺序排，第一项就是正在弹的那件，其余显示 +N；当前这件从列表里消失（答复 / 批准 /
//    别人处理了 / 稍后）就自动换下一件。
//  - 关闭 = 写 POST /v1/inbox/state 的 seen（个人状态，多端同步）：另一个浏览器再收到同一个主体
//    时读到的是 seen，不再弹。
//  - 卡片正文（标题、摘要、按钮）取 store/today 的 waiting 卡——与今天页同一份；答复 / 批准 / 稍后
//    也走今天页同一套提交路径（撤销窗口 + today.action 审计）。
import { computed, ref, watch, type ComputedRef, type Ref } from 'vue'
import type { TodayAction, TodayCard, TodayResponse } from '../api/today'
import type { InboxResponse } from '../api/inbox'
import type { WaitingItem } from '../api/types'
import { useWaiting, type WaitingStore } from './useWaiting'
import { inboxData, markInbox, scheduleInboxRefresh } from '../store/inbox'
import { actOnCard, scheduleRefresh, snoozeCard, todayData } from '../store/today'
import {
  inboxItemOf,
  inboxSeenOf,
  popApproveAction,
  popEnter,
  popExtra,
  popLink,
  popQueueDrop,
  popQueueReconcile,
  popReplyAction,
  popSnoozeOption,
  subjectKeyOf,
} from '../utils/waitingPop'
import type { SnoozeOption } from '../utils/todaySnooze'

export interface WaitingPopDeps {
  // 以下都是测试 seam，生产用整页共用的 useWaiting() 与 store 里的 today / inbox。
  waiting?: WaitingStore
  inbox?: Ref<InboxResponse | null>
  today?: Ref<TodayResponse | null>
  // write 收件箱个人状态（参数是收件箱条目自己的 key，与今天页的卡 key 同一套）。
  markSeen?: (key: string) => Promise<unknown>
  // 新项入队但 today 重拉里还没它的卡时拉一次（按钮 / 摘要都在卡上）。
  refresh?: (delayMs?: number) => void
  // 新项在收件箱快照里找不到时拉一次（别的端已关过就靠它不再弹）。
  refreshInbox?: (delayMs?: number) => void
  act?: (card: TodayCard, action: TodayAction, text?: string) => void
  snooze?: (card: TodayCard, opt: SnoozeOption) => void
}

export interface WaitingPopStore {
  // queue 按进入顺序排着该弹的主体 key；第一项就是正在弹的那件。
  readonly queue: Ref<string[]>
  // item 是正在弹的那条「等我」项（列表里已经没有它就是 null）。
  readonly item: ComputedRef<WaitingItem | null>
  // card 是它的 GET /v1/today 卡（标题、摘要、动作按钮）；today 还没拉到就是 null。
  readonly card: ComputedRef<TodayCard | null>
  // extra 是当前这件后面还排着几件（+N）。
  readonly extra: ComputedRef<number>
  // link 是「打开」的目标。
  readonly link: ComputedRef<string>
  // replyAction / approveAction 是「作答」「批准」按钮对应的卡上动作（卡还没到就是 null）。
  readonly replyAction: ComputedRef<TodayAction | null>
  readonly approveAction: ComputedRef<TodayAction | null>
  // close 关闭 = 写收件箱 seen（多端同步）并换下一件。
  close(): void
  // reply / approve / snooze 走今天页同一套提交路径；返回 false 表示这张卡没有该动作。
  reply(text: string): boolean
  approve(): boolean
  snooze(): boolean
  start(): void
  stop(): void
}

export function createWaitingPop(deps: WaitingPopDeps = {}): WaitingPopStore {
  const waiting = deps.waiting ?? useWaiting()
  const inbox = deps.inbox ?? inboxData
  const today = deps.today ?? todayData
  const markSeen = deps.markSeen ?? ((key: string) => markInbox('seen', { keys: [key] }))
  const refresh = deps.refresh ?? scheduleRefresh
  const refreshInbox = deps.refreshInbox ?? scheduleInboxRefresh
  const act = deps.act ?? actOnCard
  const snooze = deps.snooze ?? snoozeCard
  const queue = ref<string[]>([])
  let offEnter: (() => void) | null = null
  let stopWatch: (() => void) | null = null

  const item = computed<WaitingItem | null>(() => {
    const key = queue.value[0]
    return key ? waiting.items.value.find((it) => it.key === key) ?? null : null
  })
  const card = computed<TodayCard | null>(() => {
    const key = queue.value[0]
    if (!key) return null
    return (today.value?.waiting ?? []).find((c) => subjectKeyOf(c) === key) ?? null
  })
  const extra = computed(() => popExtra(queue.value))
  const link = computed(() => {
    const it = item.value
    return it ? popLink(it, card.value) : ''
  })
  const replyAction = computed(() => popReplyAction(card.value))
  const approveAction = computed(() => popApproveAction(card.value))

  // dropCurrent 立即换下一件（不等推送回来）。
  function dropCurrent(): void {
    const key = queue.value[0]
    if (key) queue.value = popQueueDrop(queue.value, key)
  }

  function hasCardOf(key: string): boolean {
    return (today.value?.waiting ?? []).some((c) => subjectKeyOf(c) === key)
  }

  return {
    queue,
    item,
    card,
    extra,
    link,
    replyAction,
    approveAction,
    close() {
      const it = item.value
      if (!it) return
      // seen 的状态键是收件箱条目自己的 key（与今天页的卡 key 同一套，如 "approval:<job>"）。
      const key = inboxItemOf(inbox.value, it.key)?.key ?? card.value?.key
      dropCurrent()
      if (key) void Promise.resolve(markSeen(key)).catch(() => undefined)
    },
    reply(text: string) {
      const c = card.value
      const a = replyAction.value
      if (!c || !a) return false
      act(c, a, text)
      dropCurrent()
      return true
    },
    approve() {
      const c = card.value
      const a = approveAction.value
      if (!c || !a) return false
      act(c, a)
      dropCurrent()
      return true
    },
    snooze() {
      const c = card.value
      const opt = c ? popSnoozeOption(c) : null
      if (!c || !opt) return false
      snooze(c, opt)
      dropCurrent()
      return true
    },
    start() {
      if (offEnter) return
      offEnter = waiting.onEnter((it) => {
        const seen = inboxSeenOf(inbox.value, it.key)
        queue.value = popEnter(queue.value, it, seen === 'seen')
        // 收件箱快照里还没有这一项（刚打开页面 / 刚重连）：拉一次，别的端关过的就不再弹。
        if (seen === 'unknown') refreshInbox(0)
        // `waiting` 推送只带 key / 类别；卡（标题、摘要、按钮）在 GET /v1/today 里，还没拉到就
        // 拉一次——弹卡上没按钮就只能关 / 打开，人还得多跑一趟。
        if (queue.value[0] === it.key && !hasCardOf(it.key)) refresh(0)
      })
      // 当前 / 排队的项被处理掉（答复、批准、别人处理了、稍后到点又走）后自动收敛。
      stopWatch = watch(
        [
          () => waiting.items.value.map((it) => it.key),
          // 收件箱每次重拉 / 写状态都是一个新对象：个人状态变了就重算一遍。
          () => inbox.value,
        ],
        ([keys]) => {
          // 列表里没有的不再弹；收件箱里已经是 seen 的也不再弹（另一个浏览器关过）。
          const live = new Set(keys.filter((k) => inboxSeenOf(inbox.value, k) !== 'seen'))
          queue.value = popQueueReconcile(queue.value, live)
        },
      )
    },
    stop() {
      offEnter?.()
      offEnter = null
      stopWatch?.()
      stopWatch = null
      queue.value = []
    },
  }
}

let shared: WaitingPopStore | null = null

// useWaitingPop 返回全页面共用的那一个弹卡队列（首次调用时开始收 onEnter）。
export function useWaitingPop(): WaitingPopStore {
  if (!shared) shared = createWaitingPop()
  shared.start()
  return shared
}

// setWaitingPopForTest 替换单例（测试用；null 让下次 useWaitingPop 重新建）。
export function setWaitingPopForTest(s: WaitingPopStore | null): void {
  shared?.stop()
  shared = s
  s?.start()
}
