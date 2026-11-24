// 「等我」列表的数据层（整个页面一份）。
//
//  - WS 的 `waiting` 主题是**快照**型：服务端只在该集合变化时推一帧（列表本身不带回合正文），
//    收到即整份替换；断线兜底（liveFallback，见 api/live.ts）时改用 useLiveTopic 的低频轮询，
//    拉 GET /v1/today 的 waiting 段转成同一形状。
//  - 整个 App 共用**一个**订阅：useWaiting() 返回同一个 store，不随组件重复订阅、也不随组件
//    卸载退订（它服务的是 App 级的弹卡 / 通知）。
//  - onEnter(cb)：某个 key **新出现**时回调一次（同一 key 只回调一次，消失后再出现算新的一次），
//    供后续弹卡与浏览器通知使用。首帧只建立基线，不把「打开页面时已经在等你的」全部当成新出现。
import { ref, type Ref } from 'vue'
import { TOPIC_WAITING, type LiveClient } from '../api/live'
import { getToday, type TodayCard } from '../api/today'
import { createLiveTopic } from '../utils/useLiveTopic'
import type { Poller } from '../utils/poller'
import type { WaitingItem } from '../api/types'

export type WaitingEnterHandler = (item: WaitingItem) => void

export interface WaitingStore {
  // items 是当前「等我」列表（与推送 / 兜底拉取同一形状）。
  readonly items: Ref<WaitingItem[]>
  // byKey 按主体 key 查一行（"job:<id>" / "session:<id>" / "plan:<id>" ...）。
  byKey(key: string): WaitingItem | undefined
  // onEnter 注册「新出现」回调，返回退订函数。
  onEnter(cb: WaitingEnterHandler): () => void
  // start 开始订阅（幂等；useWaiting 已经调过）。
  start(): void
  // stop 停订阅（测试与整页卸载用；单例一直跑到页面结束）。
  stop(): void
}

export interface WaitingDeps {
  // client / fetchToday / makePoller 是测试 seam；生产用 live() / getToday / createPoller。
  client?: LiveClient
  fetchToday?: () => Promise<{ waiting: TodayCard[] }>
  makePoller?: (fn: () => void | Promise<void>, ms: number) => Poller
}

// waitingItemOf 把 GET /v1/today 的 waiting 卡转成推送里同一形状（兜底轮询用）。
// 服务端给 waiting 列表里的卡都带上 waiting 类别；没带的（更老的 server）归入「需要你处理」。
export function waitingItemOf(card: TodayCard): WaitingItem {
  return {
    key: card.key,
    category: card.waiting ?? 'attention',
    kind: card.kind,
    project_key: card.project_key,
    waiting_since: card.waiting_since,
    title: card.title,
  }
}

export function createWaitingStore(deps: WaitingDeps = {}): WaitingStore {
  const items = ref<WaitingItem[]>([])
  const handlers = new Set<WaitingEnterHandler>()
  let known = new Map<string, WaitingItem>()
  let baseline = false
  const fetchToday = deps.fetchToday ?? (() => getToday())
  const topic = createLiveTopic(TOPIC_WAITING, {
    // 快照型主题：订阅时服务端就推一份，不必先拉一次；inval / resync 也不会走到这里
    // （服务端的 waiting 只会以 snap 到达），只有兜底轮询会调 fetch。
    initial: false,
    fetch: async () => {
      const resp = await fetchToday()
      apply(resp.waiting.map(waitingItemOf))
    },
    onSnap: (data) => {
      const next = asItems(data)
      if (next) apply(next)
    },
    client: deps.client,
    makePoller: deps.makePoller,
  })

  function apply(next: WaitingItem[]): void {
    const prev = known
    const fresh: WaitingItem[] = []
    for (const it of next) {
      if (!prev.has(it.key)) fresh.push(it)
    }
    items.value = next
    known = new Map(next.map((it) => [it.key, it]))
    // 首帧只建立基线：页面打开时已经在等着的事不该全部弹一遍。
    if (!baseline) {
      baseline = true
      return
    }
    for (const it of fresh) {
      for (const cb of [...handlers]) {
        try {
          cb(it)
        } catch {
          // 单个回调出错不影响其它。
        }
      }
    }
  }

  return {
    items,
    byKey: (key) => items.value.find((it) => it.key === key),
    onEnter(cb) {
      handlers.add(cb)
      return () => handlers.delete(cb)
    },
    start: () => topic.start(),
    stop: () => topic.stop(),
  }
}

// asItems 校验一帧快照（服务端推的总是数组）：不是数组的帧返回 null，由调用方忽略，免得一个
// 坏帧把整个列表清空。
function asItems(data: unknown): WaitingItem[] | null {
  if (!Array.isArray(data)) return null
  return data.filter(isWaitingItem)
}

function isWaitingItem(v: unknown): v is WaitingItem {
  if (!v || typeof v !== 'object') return false
  const it = v as Partial<WaitingItem>
  return typeof it.key === 'string' && it.key !== '' && typeof it.category === 'string'
}

let shared: WaitingStore | null = null

// useWaiting 返回全页面共用的那一个 store（首次调用时订阅）。
export function useWaiting(): WaitingStore {
  if (!shared) shared = createWaitingStore()
  shared.start()
  return shared
}

// setWaitingStoreForTest 替换单例（测试用；null 让下次 useWaiting 重新建）。
export function setWaitingStoreForTest(s: WaitingStore | null): void {
  shared?.stop()
  shared = s
  s?.start()
}
