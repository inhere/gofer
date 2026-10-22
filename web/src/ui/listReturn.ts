// 列表 → 详情 → 返回列表的往返状态（设计稿 §16「返回列表保留筛选与滚动位置」）。
// 列表页在滚动时把自己的 URL（含筛选）和 scrollY 记进 sessionStorage；详情页的 BackLink
// 读它跳回去，回到列表时列表页再把滚动位置复原。用 sessionStorage 而不是 localStorage：
// 这是「这一次浏览」的位置，新开标签页不该跳回旧滚动点。
export const RETURN_KEY_PREFIX = 'gofer.return.'

export interface ListReturnState {
  url: string
  scrollY: number
}

function safeStorage(): Storage | null {
  try {
    return typeof window !== 'undefined' && window.sessionStorage ? window.sessionStorage : null
  } catch {
    return null
  }
}

export function returnKey(listId: string): string {
  return `${RETURN_KEY_PREFIX}${listId}`
}

export function saveListReturn(listId: string, state: ListReturnState, storage?: Storage | null): void {
  try {
    ;(storage === undefined ? safeStorage() : storage)?.setItem(returnKey(listId), JSON.stringify(state))
  } catch {
    // 存不下就算了：只是滚动位置
  }
}

export function readListReturn(listId: string, storage?: Storage | null): ListReturnState | null {
  try {
    const raw = (storage === undefined ? safeStorage() : storage)?.getItem(returnKey(listId))
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<ListReturnState>
    if (typeof parsed?.url !== 'string' || !parsed.url) return null
    return { url: parsed.url, scrollY: typeof parsed.scrollY === 'number' ? parsed.scrollY : 0 }
  } catch {
    return null
  }
}

export function clearListReturn(listId: string, storage?: Storage | null): void {
  try {
    ;(storage === undefined ? safeStorage() : storage)?.removeItem(returnKey(listId))
  } catch {
    // ignore
  }
}

// 返回目标：记住的列表 URL 优先（带原来的筛选），否则退回 fallback。
export function backTarget(listId: string, fallback: string, storage?: Storage | null): string {
  return readListReturn(listId, storage)?.url || fallback
}
