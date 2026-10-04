// 页面订阅一个推送主题（Q3）。
//
//  - WS 正常：只靠推送。inval / resync / reconnect 触发一次（防抖的）REST 重拉；snap 直接交给
//    onSnap；evt 交给 onEvent。
//  - WS 断开超过 15s（liveFallback）：切回 createPoller 低频兜底轮询（默认 30s）；恢复后停掉
//    轮询，由 reconnect / snap 立即对齐。
//  - 首次进入页面默认先拉一次（initial=true）；快照型主题（stats / pending）服务端订阅时就会推
//    一份快照，传 initial=false 省掉这次请求。
import { getCurrentInstance, onMounted, onUnmounted, watch, type WatchStopHandle } from 'vue'
import { live, liveFallback, type LiveClient, type LiveMessage } from '../api/live'
import { createPoller, type Poller } from './poller'

export interface LiveTopicOpts {
  // REST 重拉：inval / resync / reconnect 与兜底轮询都调它。
  fetch: () => void | Promise<void>
  onSnap?: (data: unknown) => void
  onEvent?: (data: Record<string, unknown>) => void
  onInval?: (data: Record<string, unknown>) => void
  initial?: boolean
  debounceMs?: number
  fallbackMs?: number
  client?: LiveClient
  makePoller?: (fn: () => void | Promise<void>, ms: number) => Poller
}

export interface LiveTopicHandle {
  start(): void
  stop(): void
}

export const DEFAULT_FALLBACK_MS = 30_000
const DEFAULT_DEBOUNCE_MS = 200

export function createLiveTopic(topic: string, opts: LiveTopicOpts): LiveTopicHandle {
  const debounceMs = opts.debounceMs ?? DEFAULT_DEBOUNCE_MS
  const fallbackMs = opts.fallbackMs ?? DEFAULT_FALLBACK_MS
  const makePoller = opts.makePoller ?? createPoller
  let unsub: (() => void) | null = null
  let stopWatch: WatchStopHandle | null = null
  let poller: Poller | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let running = false

  function run(): void {
    void Promise.resolve()
      .then(opts.fetch)
      .catch(() => {
        // 拉取失败不打断页面；下一次推送或兜底轮询再试。
      })
  }

  function refetchSoon(): void {
    if (timer) return
    timer = setTimeout(() => {
      timer = null
      if (running) run()
    }, debounceMs)
  }

  function onMsg(m: LiveMessage): void {
    switch (m.type) {
      case 'snap':
        opts.onSnap?.(m.data)
        return
      case 'evt':
        opts.onEvent?.(m.data)
        return
      case 'inval':
        opts.onInval?.(m.data)
        refetchSoon()
        return
      case 'resync':
      case 'reconnect':
        // 快照型主题：重连/重订后服务端会再推快照，不必再 REST 拉一遍。
        if (!opts.onSnap) refetchSoon()
        return
    }
  }

  return {
    start() {
      if (running) return
      running = true
      unsub = (opts.client ?? live()).subscribe(topic, onMsg)
      if (opts.initial !== false) run()
      stopWatch = watch(
        liveFallback,
        (down) => {
          if (down && !poller) {
            poller = makePoller(opts.fetch, fallbackMs)
            poller.start()
          } else if (!down && poller) {
            poller.stop()
            poller = null
          }
        },
        { immediate: true },
      )
    },
    stop() {
      if (!running) return
      running = false
      unsub?.()
      unsub = null
      stopWatch?.()
      stopWatch = null
      poller?.stop()
      poller = null
      if (timer) clearTimeout(timer)
      timer = null
    },
  }
}

// 组件 setup 里用：挂载订阅，卸载退订。
export function useLiveTopic(topic: string, opts: LiveTopicOpts): LiveTopicHandle {
  const h = createLiveTopic(topic, opts)
  if (getCurrentInstance()) {
    onMounted(() => h.start())
    onUnmounted(() => h.stop())
  }
  return h
}
