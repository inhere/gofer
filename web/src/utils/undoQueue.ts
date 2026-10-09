// N3 撤销窗口（design §2.3）：操作后卡片立刻收起，toast「已批准 · 撤销 5s」，倒计时结束
// 才调用写接口；撤销（z / 按钮）就什么都不发。同一时刻只有一个待提交的操作——新操作
// 进来时先把上一个立即提交。页面卸载 / 离开路由时 flush 立即发出（keepalive，不丢）。
import { ref, type Ref } from 'vue'

export interface UndoEntry {
  key: string
  label: string
  // 真正的写操作；keepalive=true 表示页面正在卸载（调用方用 fetch keepalive 发出）。
  run: (keepalive: boolean) => Promise<unknown>
  onUndo?: () => void
  onDone?: () => void
  onError?: (e: unknown) => void
}

export interface UndoView {
  key: string
  label: string
  left: number
}

export interface UndoQueue {
  current: Ref<UndoView | null>
  push(entry: UndoEntry): void
  // 立即执行（不进撤销窗口），但先提交还在等的那个。
  now(entry: UndoEntry): void
  undo(): boolean
  flush(keepalive?: boolean): void
}

export interface UndoTimers {
  setInterval: (fn: () => void, ms: number) => unknown
  clearInterval: (h: unknown) => void
}

const realTimers: UndoTimers = {
  setInterval: (fn, ms) => globalThis.setInterval(fn, ms),
  clearInterval: (h) => globalThis.clearInterval(h as ReturnType<typeof setInterval>),
}

export function createUndoQueue(delayMs = 5000, timers: UndoTimers = realTimers): UndoQueue {
  const current = ref<UndoView | null>(null)
  let pending: UndoEntry | null = null
  let handle: unknown = null

  function stopTimer(): void {
    if (handle != null) timers.clearInterval(handle)
    handle = null
  }

  function execute(entry: UndoEntry, keepalive: boolean): void {
    let p: Promise<unknown>
    try {
      p = entry.run(keepalive)
    } catch (e) {
      p = Promise.reject(e)
    }
    p.then(
      () => entry.onDone?.(),
      (e) => entry.onError?.(e),
    )
  }

  function commit(keepalive = false): void {
    const entry = pending
    stopTimer()
    pending = null
    current.value = null
    if (entry) execute(entry, keepalive)
  }

  return {
    current,
    push(entry) {
      commit()
      pending = entry
      const seconds = Math.max(1, Math.round(delayMs / 1000))
      current.value = { key: entry.key, label: entry.label, left: seconds }
      handle = timers.setInterval(() => {
        if (!current.value) return
        const left = current.value.left - 1
        if (left <= 0) commit()
        else current.value = { ...current.value, left }
      }, 1000)
    },
    now(entry) {
      commit()
      execute(entry, false)
    },
    undo() {
      const entry = pending
      if (!entry) return false
      stopTimer()
      pending = null
      current.value = null
      entry.onUndo?.()
      return true
    },
    flush(keepalive = false) {
      commit(keepalive)
    },
  }
}
